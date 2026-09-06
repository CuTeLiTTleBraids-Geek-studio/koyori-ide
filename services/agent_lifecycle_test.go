package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type usageObservingStepExecutor struct {
	observe func()
}

func newLifecycleTestAgentAtWorkspace(t *testing.T, root string) *AgentService {
	t.Helper()
	workspace := NewWorkspaceContext()
	if err := workspace.Set(root); err != nil {
		t.Fatalf("set workspace context: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	if err := agent.configureWorkspaceRoot(root); err != nil {
		_ = agent.Close()
		t.Fatalf("configure agent workspace: %v", err)
	}
	return agent
}

func (e usageObservingStepExecutor) Execute(string, int, string, string) (string, error) {
	if e.observe != nil {
		e.observe()
	}
	return "ok", nil
}

type usageObservingGoalExecutor struct {
	observe func()
}

func (e usageObservingGoalExecutor) Plan(*Goal) (string, error) {
	if e.observe != nil {
		e.observe()
	}
	return "plan", nil
}

func (usageObservingGoalExecutor) Execute(*Goal, string) (GoalRoundResult, error) {
	return GoalRoundResult{Success: true, Tokens: 12, Cost: 0.02}, nil
}

func (usageObservingGoalExecutor) Evaluate(*Goal) (bool, error) { return true, nil }

func TestAIServiceSendUsesSharedContextAndHonestMeter(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`)
	}))
	t.Cleanup(server.Close)

	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	ai := NewAIService()
	lifecycle, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := ai.SetConfig(AIConfig{
		APIKey: "test-key", BaseURL: server.URL, Model: "test-model",
		SystemPrompt: "required-system-context", ContextWindow: 1,
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if _, err := ai.Send([]ChatMessage{{Role: "user", Content: "hello"}}); !errors.Is(err, agentcore.ErrContextBudgetExceeded) {
		t.Fatalf("required context overflow error = %v, want ErrContextBudgetExceeded", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("provider received %d requests after context overflow", requests.Load())
	}

	if err := ai.SetConfig(AIConfig{
		APIKey: "test-key", BaseURL: server.URL, Model: "test-model",
		SystemPrompt: "required-system-context", ContextWindow: 100,
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if _, err := ai.Send([]ChatMessage{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 2 {
		t.Fatalf("chat usage records = %+v, want failed overflow and provider call", records)
	}
	record := records[1]
	if record.UnitKind != string(agentcore.UsageUnitChat) || record.ProviderID != "openai-compatible" || record.Model != "test-model" || record.TokensIn != 9 || record.TokensOut != 4 {
		t.Fatalf("chat usage = %+v", record)
	}
	if record.CostBasis != string(agentcore.CostNotApplicable) || record.Estimated || record.Cost != 0 {
		t.Fatalf("provider token counts were presented as known cost: %+v", record)
	}
	session, err := lifecycle.GetByID(record.SessionID)
	if err != nil || session.Status != agentcore.SessionCompleted {
		t.Fatalf("chat session = %+v, err=%v", session, err)
	}
}

func TestAIProviderCallStartsAfterDurableUsageReceipt(t *testing.T) {
	permission := NewAIPermissionService(t.TempDir())
	pendingSeen := make(chan UsageRecord, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		records := permission.usageRecordsSnapshot()
		if len(records) == 1 {
			pendingSeen <- records[0]
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(server.Close)

	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	if _, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: server.URL, Model: "test-model", ContextWindow: 10000}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if _, err := ai.Send([]ChatMessage{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case pending := <-pendingSeen:
		if !pending.Pending || pending.Success || pending.UnitKind != string(agentcore.UsageUnitChat) {
			t.Fatalf("provider observed usage row = %+v, want pending chat receipt", pending)
		}
	default:
		t.Fatal("provider was called before a pending usage receipt was observable")
	}
	final := permission.usageRecordsSnapshot()
	if len(final) != 1 || final[0].Pending || !final[0].Success {
		t.Fatalf("final usage rows = %+v", final)
	}
}

func TestAIProviderIsNotCalledWhenUsageReceiptFails(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"unexpected"}}]}`)
	}))
	t.Cleanup(server.Close)
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatalf("create ledger blocker: %v", err)
	}
	permission := NewAIPermissionService(filepath.Join(blocker, "nested"))
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	if _, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: server.URL, Model: "test-model", ContextWindow: 10000}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if _, err := ai.Send([]ChatMessage{{Role: "user", Content: "hello"}}); err == nil {
		t.Fatal("Send succeeded without a durable usage receipt")
	}
	if requests.Load() != 0 {
		t.Fatalf("provider received %d requests without a durable usage receipt", requests.Load())
	}
}

func TestAIServiceStreamUsesSharedLifecycleAndEstimatedMeter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"streamed\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	ai := NewAIService()
	lifecycle, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: server.URL, Model: "test-model", SystemPrompt: "sys", ContextWindow: 100}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	var output string
	if err := ai.SendStreamWithContext(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}, func(chunk string) {
		output += chunk
	}); err != nil {
		t.Fatalf("SendStreamWithContext: %v", err)
	}
	if output != "streamed" {
		t.Fatalf("stream output = %q", output)
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 1 {
		t.Fatalf("stream usage records = %+v", records)
	}
	record := records[0]
	if record.UnitKind != string(agentcore.UsageUnitChat) || record.CostBasis != string(agentcore.CostEstimated) || !record.Estimated {
		t.Fatalf("stream usage provenance = %+v", record)
	}
	session, err := lifecycle.GetByID(record.SessionID)
	if err != nil || session.Status != agentcore.SessionCompleted || len(session.Stream) != 1 || session.Stream[0].Data != "streamed" {
		t.Fatalf("stream session = %+v, err=%v", session, err)
	}
}

func TestAICompletionAndTitleUseUnifiedMeter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"result"}}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`)
	}))
	t.Cleanup(server.Close)

	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	ai := NewAIService()
	if _, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: server.URL, Model: "test-model", ContextWindow: 10000}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if _, err := ai.Complete(CompletionRequest{Prefix: "func main() {", Suffix: "}", Language: "go"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := ai.GenerateTitleWithAI("first message"); err != nil {
		t.Fatalf("GenerateTitleWithAI: %v", err)
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 2 {
		t.Fatalf("AI operation records = %+v", records)
	}
	if records[0].Operation != AIOpInlineCompletion || records[1].Operation != AIOpTitleGeneration {
		t.Fatalf("AI operation records = %+v", records)
	}
	for _, record := range records {
		if record.UnitKind != string(agentcore.UsageUnitAI) || record.TokensIn != 7 || record.TokensOut != 2 || record.CostBasis != string(agentcore.CostNotApplicable) {
			t.Fatalf("AI operation provenance = %+v", record)
		}
	}
}

func TestWireAgentLifecycleUsesOneStoreContextAndMeter(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	permission := NewAIPermissionService(t.TempDir())
	ai := NewAIService()
	plan := NewAIPlanService()
	goal := NewAIGoalService()

	lifecycle, err := WireAgentLifecycle(agent, ai, plan, goal, permission)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if lifecycle == nil || ai.lifecycle != lifecycle || plan.lifecycle != lifecycle || goal.lifecycle != lifecycle {
		t.Fatal("chat, plan, and goal must receive the same lifecycle instance")
	}

	workflow, err := lifecycle.Begin(agentcore.SessionWorkflow, "build")
	if err != nil {
		t.Fatalf("begin workflow lifecycle: %v", err)
	}
	if workflow.Kind != agentcore.SessionWorkflow || workflow.Status != agentcore.SessionRunning {
		t.Fatalf("workflow lifecycle = %+v", workflow)
	}

	selection, err := lifecycle.SelectContext([]agentcore.ContextItem{
		{ID: "system", Text: "required", Required: true},
		{ID: "recent", Text: "context", Priority: 10},
	}, 100)
	if err != nil || len(selection.Included) != 2 {
		t.Fatalf("shared context selection = %+v, err=%v", selection, err)
	}

	path := filepath.Join(root, "metered.txt")
	if err := os.WriteFile(path, []byte("meter me"), 0o600); err != nil {
		t.Fatalf("seed metered file: %v", err)
	}
	if err := WireAgentExecutionCore(agent, file, search, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	if _, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:metered", CatalogRevision: catalog.Revision, ToolID: "read",
		Arguments: map[string]interface{}{"path": "metered.txt"},
	}); err != nil {
		t.Fatalf("ExecuteAgentTool: %v", err)
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 1 {
		t.Fatalf("usage records = %+v, want one tool record", records)
	}
	record := records[0]
	if record.UnitKind != string(agentcore.UsageUnitTool) || record.CostBasis != string(agentcore.CostNotApplicable) || record.Estimated {
		t.Fatalf("tool usage provenance was weakened: %+v", record)
	}
	meteredWorkflow, err := lifecycle.GetByID("workflow:metered")
	if err != nil || meteredWorkflow.Kind != agentcore.SessionWorkflow || meteredWorkflow.Status != agentcore.SessionRunning {
		t.Fatalf("metered workflow lifecycle = %+v, err=%v", meteredWorkflow, err)
	}
	if err := lifecycle.Complete(agentcore.SessionWorkflow, "metered"); err != nil {
		t.Fatalf("complete metered workflow: %v", err)
	}
}

func TestDomainLifecycleIDsDoNotBecomeRuntimeAuthority(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	plan := NewAIPlanService()
	goal := NewAIGoalService()
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), plan, goal, NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}

	if _, err := plan.CreatePlan("renderer-plan-id", "inspect", nil); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	planLogical := "plan:renderer-plan-id"
	planRuntime := lifecycle.runtimeSessionID(agentcore.SessionPlan, "renderer-plan-id")
	if planRuntime == planLogical {
		t.Fatal("plan domain ID was reused as the runtime authority ID")
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(planLogical) || !runtime.IsSessionRegistered(planRuntime) {
		t.Fatalf("plan runtime registration logical=%v opaque=%v", runtime.IsSessionRegistered(planLogical), runtime.IsSessionRegistered(planRuntime))
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: planLogical, CatalogRevision: runtime.Registry().Snapshot().Revision,
		ToolID: "read", Arguments: map[string]interface{}{"path": "missing"},
	}); !errors.Is(err, agentcore.ErrUnknownSession) {
		t.Fatalf("forged plan session capability error = %v, want ErrUnknownSession", err)
	}

	if _, err := goal.CreateGoal("renderer-goal-id", "inspect", "done", 1, 1, time.Minute, true); err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	goalLogical := "goal:renderer-goal-id"
	goalRuntime := lifecycle.runtimeSessionID(agentcore.SessionGoal, "renderer-goal-id")
	if goalRuntime == goalLogical {
		t.Fatal("goal domain ID was reused as the runtime authority ID")
	}
	if runtime.IsSessionRegistered(goalLogical) || !runtime.IsSessionRegistered(goalRuntime) {
		t.Fatalf("goal runtime registration logical=%v opaque=%v", runtime.IsSessionRegistered(goalLogical), runtime.IsSessionRegistered(goalRuntime))
	}
}

func TestCreateOwnedSessionPersistsBeforeRuntimeAuthority(t *testing.T) {
	configDir := t.TempDir()
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	if _, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	lifecycle := deps.lifecycle
	deps.mu.RUnlock()
	if lifecycle == nil {
		t.Fatal("lifecycle was not wired")
	}
	sessionID, err := agent.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	if _, err := lifecycle.GetByID(sessionID); err != nil {
		t.Fatalf("durable lifecycle row missing: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, "agent_lifecycle_sessions.json"))
	if err != nil {
		t.Fatalf("read lifecycle snapshot: %v", err)
	}
	if !strings.Contains(string(data), sessionID) || !strings.Contains(string(data), "chat-service") {
		t.Fatalf("snapshot omitted owner row: %s", data)
	}
	if err := lifecycle.CloseByID("workflow:forged-bearer"); !errors.Is(err, agentcore.ErrSessionNotFound) {
		t.Fatalf("forged CloseByID error = %v, want ErrSessionNotFound", err)
	}
}

func TestLazyUsageSessionPersistenceFailureDoesNotIssueRuntimeAuthority(t *testing.T) {
	configDir := t.TempDir()
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}

	snapshotPath := filepath.Join(configDir, "agent_lifecycle_sessions.json")
	if err := os.Mkdir(snapshotPath, 0o700); err != nil {
		t.Fatalf("block lifecycle snapshot: %v", err)
	}
	sessionID := "workflow:lazy-persistence-failure"
	now := time.Now()
	err = lifecycle.Record(agentcore.UsageRecord{
		SessionID: sessionID, UnitKind: agentcore.UsageUnitWorkflow,
		Operation: "workflow.lazy", CostBasis: agentcore.CostNotApplicable,
		StartedAt: now, CompletedAt: now.Add(time.Second), Success: true,
	})
	if err == nil {
		t.Fatal("Record succeeded without a durable lifecycle row")
	}
	runtime, runtimeErr := agent.coreRuntime()
	if runtimeErr != nil {
		t.Fatalf("coreRuntime: %v", runtimeErr)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("runtime authority was issued after lifecycle persistence failed")
	}
	if _, getErr := lifecycle.GetByID(sessionID); !errors.Is(getErr, agentcore.ErrSessionNotFound) {
		t.Fatalf("failed durable row remained visible: %v", getErr)
	}
	if records := permission.usageRecordsSnapshot(); len(records) != 0 {
		t.Fatalf("usage was metered after lifecycle persistence failed: %+v", records)
	}
}

func TestDurableLifecycleRestartDoesNotRestoreRuntimeAuthority(t *testing.T) {
	configDir := t.TempDir()
	permissionDir := t.TempDir()
	agent1 := NewAgentService()
	permission1 := NewAIPermissionService(permissionDir)
	if _, err := WireAgentLifecycle(agent1, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission1, configDir); err != nil {
		t.Fatalf("first WireAgentLifecycle: %v", err)
	}
	firstDeps := executionDependenciesFor(agent1)
	firstDeps.mu.RLock()
	firstLifecycle := firstDeps.lifecycle
	firstDeps.mu.RUnlock()
	sessionID, err := agent1.createAgentSessionTrusted("workflow")
	if err != nil {
		t.Fatalf("first CreateAgentSession: %v", err)
	}
	if err := firstLifecycle.Pause(agentcore.SessionWorkflow, sessionID); err != nil {
		t.Fatalf("pause first workflow: %v", err)
	}
	_ = agent1.Close()

	agent2 := NewAgentService()
	t.Cleanup(func() { _ = agent2.Close() })
	permission2 := NewAIPermissionService(permissionDir)
	if _, err := WireAgentLifecycle(agent2, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission2, configDir); err != nil {
		t.Fatalf("second WireAgentLifecycle: %v", err)
	}
	secondDeps := executionDependenciesFor(agent2)
	secondDeps.mu.RLock()
	secondLifecycle := secondDeps.lifecycle
	secondDeps.mu.RUnlock()
	row, err := secondLifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("reloaded workflow row: %v", err)
	}
	if row.Recovery != agentcore.SessionRecoveryRequired || row.Owner == nil || row.Owner.RuntimeID != "" {
		t.Fatalf("reloaded owner state = %+v", row)
	}
	if err := secondLifecycle.ResumeLatest(agentcore.SessionWorkflow, sessionID); !errors.Is(err, agentcore.ErrSessionRecoveryRequired) {
		t.Fatalf("resume stale workflow error = %v, want recovery-required", err)
	}
	if runtime, err := agent2.coreRuntime(); err != nil {
		t.Fatalf("second coreRuntime: %v", err)
	} else if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("stale workflow runtime authority was restored")
	}
}

func TestDurableLifecycleRecoveryDiscardIsTrustedContentFreeAndPersistent(t *testing.T) {
	configDir := t.TempDir()
	permissionDir := t.TempDir()
	workspaceRoot := t.TempDir()
	agent1 := newLifecycleTestAgentAtWorkspace(t, workspaceRoot)
	permission1 := NewAIPermissionService(permissionDir)
	lifecycle1, err := WireAgentLifecycle(agent1, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission1, configDir)
	if err != nil {
		t.Fatalf("first WireAgentLifecycle: %v", err)
	}
	sessionID, err := agent1.createAgentSessionTrusted("workflow")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	if _, err := lifecycle1.Checkpoint(agentcore.SessionWorkflow, sessionID, "sensitive", map[string]interface{}{
		"prompt": "must not appear in recovery listing",
	}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := lifecycle1.Pause(agentcore.SessionWorkflow, sessionID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	_ = agent1.Close()

	agent2 := newLifecycleTestAgentAtWorkspace(t, workspaceRoot)
	t.Cleanup(func() { _ = agent2.Close() })
	lifecycle2, err := WireAgentLifecycle(agent2, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(permissionDir), configDir)
	if err != nil {
		t.Fatalf("second WireAgentLifecycle: %v", err)
	}
	entries, err := lifecycle2.pendingRecoveryDispositions()
	if err != nil {
		t.Fatalf("PendingRecoveryDispositions: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != sessionID || entries[0].Kind != agentcore.SessionWorkflow || entries[0].OwnerDomain != "workflow-service" || entries[0].CheckpointCount != 1 {
		t.Fatalf("recovery entries = %+v", entries)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal recovery entries: %v", err)
	}
	if strings.Contains(string(encoded), "must not appear") || strings.Contains(string(encoded), "prompt") {
		t.Fatalf("recovery listing leaked checkpoint content: %s", encoded)
	}
	if _, err := lifecycle2.applyRecoveryDisposition(agentcore.SessionChat, sessionID, agentcore.RecoveryDispositionDiscard); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-domain disposition error = %v, want ErrInvalidInput", err)
	}
	runtime2, err := agent2.coreRuntime()
	if err != nil {
		t.Fatalf("second coreRuntime: %v", err)
	}
	if runtime2.IsSessionRegistered(sessionID) {
		t.Fatal("restart orphan unexpectedly had runtime authority")
	}
	disposed, err := lifecycle2.applyRecoveryDisposition(agentcore.SessionWorkflow, sessionID, agentcore.RecoveryDispositionDiscard)
	if err != nil {
		t.Fatalf("ApplyRecoveryDisposition: %v", err)
	}
	if disposed.Status != agentcore.SessionCompleted || disposed.Recovery != agentcore.SessionRecoveryNone || disposed.RecoveryDisposition != agentcore.RecoveryDispositionDiscard {
		t.Fatalf("disposed session = %+v", disposed)
	}
	if runtime2.IsSessionRegistered(sessionID) {
		t.Fatal("recovery disposition registered runtime authority")
	}

	agent3 := newLifecycleTestAgentAtWorkspace(t, workspaceRoot)
	t.Cleanup(func() { _ = agent3.Close() })
	lifecycle3, err := WireAgentLifecycle(agent3, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(permissionDir), configDir)
	if err != nil {
		t.Fatalf("third WireAgentLifecycle: %v", err)
	}
	entries, err = lifecycle3.pendingRecoveryDispositions()
	if err != nil || len(entries) != 0 {
		t.Fatalf("recovery entries after reload = %+v, err=%v", entries, err)
	}
	reloaded, err := lifecycle3.GetByID(sessionID)
	if err != nil || reloaded.Status != agentcore.SessionCompleted || reloaded.RecoveryDisposition != agentcore.RecoveryDispositionDiscard {
		t.Fatalf("reloaded disposed session = %+v, err=%v", reloaded, err)
	}
}

func TestDurableLifecycleRecoveryDiscardPersistenceFailureKeepsAuthorityRevoked(t *testing.T) {
	configDir := t.TempDir()
	permissionDir := t.TempDir()
	workspaceRoot := t.TempDir()
	agent1 := newLifecycleTestAgentAtWorkspace(t, workspaceRoot)
	lifecycle1, err := WireAgentLifecycle(agent1, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(permissionDir), configDir)
	if err != nil {
		t.Fatalf("first WireAgentLifecycle: %v", err)
	}
	sessionID, err := agent1.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	if err := lifecycle1.Pause(agentcore.SessionChat, sessionID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	_ = agent1.Close()

	agent2 := newLifecycleTestAgentAtWorkspace(t, workspaceRoot)
	t.Cleanup(func() { _ = agent2.Close() })
	lifecycle2, err := WireAgentLifecycle(agent2, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(permissionDir), configDir)
	if err != nil {
		t.Fatalf("second WireAgentLifecycle: %v", err)
	}
	snapshotPath := filepath.Join(configDir, "agent_lifecycle_sessions.json")
	if err := os.Remove(snapshotPath); err != nil {
		t.Fatalf("remove lifecycle snapshot: %v", err)
	}
	if err := os.Mkdir(snapshotPath, 0o700); err != nil {
		t.Fatalf("block lifecycle snapshot publication: %v", err)
	}
	if _, err := lifecycle2.applyRecoveryDisposition(agentcore.SessionChat, sessionID, agentcore.RecoveryDispositionDiscard); err == nil {
		t.Fatal("recovery disposition succeeded without durable publication")
	}
	row, err := lifecycle2.GetByID(sessionID)
	if err != nil {
		t.Fatalf("GetByID after rejected disposition: %v", err)
	}
	if row.Status != agentcore.SessionPaused || row.Recovery != agentcore.SessionRecoveryRequired || row.RecoveryDisposition != "" || row.Owner == nil || row.Owner.RuntimeID != "" {
		t.Fatalf("rejected disposition changed lifecycle row = %+v", row)
	}
	runtime, err := agent2.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("rejected recovery disposition restored runtime authority")
	}
}

func TestDurableLifecycleRecoveryDispositionRejectsDifferentWorkspaceAndLegacyOwner(t *testing.T) {
	configDir := t.TempDir()
	permissionDir := t.TempDir()
	rootA := t.TempDir()
	rootB := t.TempDir()
	agent1 := newLifecycleTestAgentAtWorkspace(t, rootA)
	lifecycle1, err := WireAgentLifecycle(agent1, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(permissionDir), configDir)
	if err != nil {
		t.Fatalf("first WireAgentLifecycle: %v", err)
	}
	sessionID, err := agent1.createAgentSessionTrusted("workflow")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	if err := lifecycle1.Pause(agentcore.SessionWorkflow, sessionID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	_ = agent1.Close()

	agentB := newLifecycleTestAgentAtWorkspace(t, rootB)
	t.Cleanup(func() { _ = agentB.Close() })
	lifecycleB, err := WireAgentLifecycle(agentB, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(permissionDir), configDir)
	if err != nil {
		t.Fatalf("second WireAgentLifecycle: %v", err)
	}
	if entries, err := lifecycleB.pendingRecoveryDispositions(); err != nil || len(entries) != 0 {
		t.Fatalf("different-workspace recovery entries = %+v, err=%v", entries, err)
	}
	if _, err := lifecycleB.applyRecoveryDisposition(agentcore.SessionWorkflow, sessionID, agentcore.RecoveryDispositionDiscard); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("different-workspace disposition error = %v, want ErrNotAllowed", err)
	}
	row, err := lifecycleB.GetByID(sessionID)
	if err != nil || row.Recovery != agentcore.SessionRecoveryRequired {
		t.Fatalf("different-workspace disposition changed row = %+v, err=%v", row, err)
	}

	legacyConfigDir := t.TempDir()
	legacyAgent1 := NewAgentService()
	legacyLifecycle1, err := WireAgentLifecycle(legacyAgent1, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir()), legacyConfigDir)
	if err != nil {
		t.Fatalf("legacy first WireAgentLifecycle: %v", err)
	}
	legacyID, err := legacyAgent1.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("legacy CreateAgentSession: %v", err)
	}
	if err := legacyLifecycle1.Pause(agentcore.SessionChat, legacyID); err != nil {
		t.Fatalf("legacy Pause: %v", err)
	}
	_ = legacyAgent1.Close()
	legacyAgent2 := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = legacyAgent2.Close() })
	legacyLifecycle2, err := WireAgentLifecycle(legacyAgent2, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir()), legacyConfigDir)
	if err != nil {
		t.Fatalf("legacy second WireAgentLifecycle: %v", err)
	}
	if _, err := legacyLifecycle2.applyRecoveryDisposition(agentcore.SessionChat, legacyID, agentcore.RecoveryDispositionDiscard); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("legacy owner disposition error = %v, want ErrNotAllowed", err)
	}
}

func TestWorkspaceResetPersistenceFailureRollsBackAgentIdentity(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	configDir := t.TempDir()
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(rootA); err != nil {
		t.Fatalf("configure root A: %v", err)
	}
	permission := NewAIPermissionService(t.TempDir())
	if _, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	sessionID, err := agent.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	beforeRoot := agent.currentWorkspaceRoot()
	agent.mu.Lock()
	beforeGeneration := agent.rootGeneration
	agent.mu.Unlock()

	snapshotPath := filepath.Join(configDir, "agent_lifecycle_sessions.json")
	if err := os.Remove(snapshotPath); err != nil {
		t.Fatalf("remove lifecycle snapshot: %v", err)
	}
	if err := os.Mkdir(snapshotPath, 0o700); err != nil {
		t.Fatalf("replace snapshot with blocking directory: %v", err)
	}
	if err := agent.configureWorkspaceRoot(rootB); err == nil {
		t.Fatal("workspace reset succeeded without a durable lifecycle disposition")
	}
	if got := agent.currentWorkspaceRoot(); got != beforeRoot {
		t.Fatalf("agent root after failed reset = %q, want %q", got, beforeRoot)
	}
	agent.mu.Lock()
	afterGeneration := agent.rootGeneration
	agent.mu.Unlock()
	if afterGeneration != beforeGeneration {
		t.Fatalf("agent generation after failed reset = %d, want %d", afterGeneration, beforeGeneration)
	}
	if !runtime.IsSessionRegistered(sessionID) {
		t.Fatal("failed durable reset revoked the prior runtime authority")
	}
}

func TestUsageObservationUsesLogicalSessionForOpaquePlanOwner(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	plan := NewAIPlanService()
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), plan, NewAIGoalService(), NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if _, err := plan.CreatePlan("opaque-observe", "inspect", nil); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	runtimeID := lifecycle.runtimeSessionID(agentcore.SessionPlan, "opaque-observe")
	logicalID := lifecycleSessionID(agentcore.SessionPlan, "opaque-observe")
	if runtimeID == logicalID {
		t.Fatal("plan owner did not receive an opaque runtime ID")
	}
	now := time.Now()
	usage := agentcore.UsageRecord{
		UnitID: "usage-opaque-observe", SessionID: runtimeID,
		UnitKind: agentcore.UsageUnitPlan, Operation: "plan.step",
		CostBasis: agentcore.CostNotApplicable, StartedAt: now, CompletedAt: now,
		Success: true,
	}
	if err := lifecycle.RecordUsage(usage); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	if err := lifecycle.RecordUsage(usage); err != nil {
		t.Fatalf("idempotent RecordUsage: %v", err)
	}
	session, err := lifecycle.GetByID(logicalID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	foundEvent := false
	eventCount := 0
	for _, event := range session.Stream {
		if event.Kind == agentcore.StreamToolResult && event.Data == "plan.step" {
			foundEvent = true
			eventCount++
		}
	}
	if !foundEvent {
		t.Fatalf("logical session missing usage stream event: %+v", session.Stream)
	}
	if eventCount != 1 {
		t.Fatalf("idempotent usage observation appended %d stream events, want 1: %+v", eventCount, session.Stream)
	}
	foundCheckpoint := false
	checkpointCount := 0
	for _, checkpoint := range session.Checkpoints {
		if checkpoint.Label == "usage-recorded" {
			foundCheckpoint = true
			checkpointCount++
		}
	}
	if !foundCheckpoint {
		t.Fatalf("logical session missing usage checkpoint: %+v", session.Checkpoints)
	}
	if checkpointCount != 1 {
		t.Fatalf("idempotent usage observation appended %d checkpoints, want 1: %+v", checkpointCount, session.Checkpoints)
	}
}

func TestProductionRuntimeUsageReceiptPreventsWriteWhenLedgerUnavailable(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	configDir := t.TempDir()
	permission := NewAIPermissionService(configDir)
	ledgerPath := filepath.Join(configDir, "usage_log.jsonl")
	if err := os.Mkdir(ledgerPath, 0o700); err != nil {
		t.Fatalf("create ledger blocker: %v", err)
	}
	if _, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	sessionID, err := agent.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "write",
		Arguments: map[string]interface{}{"path": "meter-fail.txt", "content": "must not exist"},
	})
	if !errors.Is(err, ErrUsagePersistence) || err.Error() != ErrUsagePersistence.Error() {
		t.Fatalf("ExecuteAgentTool error = %v, want safe ErrUsagePersistence", err)
	}
	if result.Usage.Error != ErrUsagePersistence.Error() || strings.Contains(result.Usage.Error, configDir) {
		t.Fatalf("public usage error = %q, want redacted persistence failure", result.Usage.Error)
	}
	if _, statErr := os.Stat(filepath.Join(root, "meter-fail.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("write ran without a usage receipt: stat error=%v", statErr)
	}
	if records := permission.usageRecordsSnapshot(); len(records) != 0 {
		t.Fatalf("failed receipt appeared in memory ledger: %+v", records)
	}
}

func TestProductionRuntimeUsageReceiptUpsertsTerminalRecord(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	configDir := t.TempDir()
	permission := NewAIPermissionService(configDir)
	if _, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	sessionID, err := agent.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "write",
		Arguments: map[string]interface{}{"path": "metered.txt", "content": "recorded"},
	})
	if err != nil {
		t.Fatalf("ExecuteAgentTool: %v", err)
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 1 || records[0].Pending || !records[0].Success || records[0].UnitID != result.Usage.UnitID {
		t.Fatalf("logical usage rows = %+v, result=%+v", records, result)
	}
	ledger, err := os.ReadFile(filepath.Join(configDir, "usage_log.jsonl"))
	if err != nil {
		t.Fatalf("read usage ledger: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(ledger)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"pending":true`) || strings.Contains(lines[1], `"pending":true`) {
		t.Fatalf("usage receipt ledger = %q", ledger)
	}
	reloaded := NewAIPermissionService(configDir).usageRecordsSnapshot()
	if len(reloaded) != 1 || reloaded[0].Pending || !reloaded[0].Success || reloaded[0].UnitID != result.Usage.UnitID {
		t.Fatalf("reloaded logical usage rows = %+v", reloaded)
	}
}

func TestProductionRuntimePersistsExternalReceiptWithoutPrivateMetadata(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	configDir := t.TempDir()
	permission := NewAIPermissionService(configDir)
	if _, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	sessionID, err := agent.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	ctx, capture := withAgentRunCapture(context.Background(), func(command, _ string) (ExecResult, error) {
		return ExecResult{Command: command, ExitCode: 0}, nil
	})
	result, err := agent.ExecuteAgentTool(ctx, AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "run",
		Arguments: map[string]interface{}{"command": "go version"},
	})
	if err != nil {
		t.Fatalf("ExecuteAgentTool: %v", err)
	}
	if !capture.invoked || result.Usage.ExternalReceiptID == "" || result.Usage.ExternalReceiptReversible || result.Usage.ExternalCompensation != agentcore.ExternalCompensationNotNeeded {
		t.Fatalf("external result capture=%v usage=%+v", capture.invoked, result.Usage)
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 1 || records[0].ExternalReceiptID != result.Usage.ExternalReceiptID || records[0].ExternalReceiptReversible || records[0].ExternalCompensation != string(agentcore.ExternalCompensationNotNeeded) {
		t.Fatalf("persisted external usage = %+v", records)
	}
	ledger, err := os.ReadFile(filepath.Join(configDir, "usage_log.jsonl"))
	if err != nil {
		t.Fatalf("read usage ledger: %v", err)
	}
	if !strings.Contains(string(ledger), `"externalReceiptId"`) || strings.Contains(string(ledger), `"externalReceiptMetadata"`) {
		t.Fatalf("external receipt ledger leaked private metadata or lost ID: %s", ledger)
	}
}

func TestOrchestrationExecutorsDoNotRunWithoutUsageReceipt(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatalf("create ledger blocker: %v", err)
	}
	permission := NewAIPermissionService(filepath.Join(blocker, "nested"))
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	plan := NewAIPlanService()
	goal := NewAIGoalService()
	if _, err := WireAgentLifecycle(agent, NewAIService(), plan, goal, permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}

	if _, err := plan.CreatePlan("meter-plan", "inspect", []PlanStep{{Title: "read", Tool: "read"}}); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if err := plan.ApproveStep("meter-plan", 0); err != nil {
		t.Fatalf("ApproveStep: %v", err)
	}
	var planCalls atomic.Int32
	err := plan.ExecuteStep("meter-plan", 0, usageObservingStepExecutor{observe: func() { planCalls.Add(1) }})
	if err == nil || planCalls.Load() != 0 {
		t.Fatalf("plan execution error=%v calls=%d, want receipt failure and zero calls", err, planCalls.Load())
	}

	if _, err := goal.CreateGoal("meter-goal", "finish", "done", 1, 1, time.Minute, true); err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	var goalCalls atomic.Int32
	err = goal.RunGoal("meter-goal", usageObservingGoalExecutor{observe: func() { goalCalls.Add(1) }}, nil)
	if err == nil || goalCalls.Load() != 0 {
		t.Fatalf("goal execution error=%v calls=%d, want receipt failure and zero calls", err, goalCalls.Load())
	}
}

func TestAgentLifecycleUsageDoesNotTerminateMultiStepWorkflow(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}

	now := time.Now()
	recordStep := func(operation string, success bool) {
		t.Helper()
		record := agentcore.UsageRecord{
			SessionID: "workflow:multi-step", UnitKind: agentcore.UsageUnitTool,
			Operation: operation, CostBasis: agentcore.CostNotApplicable,
			StartedAt: now, CompletedAt: now.Add(time.Second), Success: success,
		}
		if !success {
			record.Error = "step failed"
		}
		if err := lifecycle.Record(record); err != nil {
			t.Fatalf("Record(%q): %v", operation, err)
		}
	}

	recordStep("workflow.step.prepare", true)
	session, err := lifecycle.GetByID("workflow:multi-step")
	if err != nil {
		t.Fatalf("GetByID after first step: %v", err)
	}
	if session.Status != agentcore.SessionRunning {
		t.Fatalf("workflow status after first step = %s, want running", session.Status)
	}

	recordStep("workflow.step.retryable", false)
	session, err = lifecycle.GetByID("workflow:multi-step")
	if err != nil {
		t.Fatalf("GetByID after retryable step: %v", err)
	}
	if session.Status != agentcore.SessionRunning {
		t.Fatalf("workflow status after retryable step = %s, want running", session.Status)
	}
	if len(session.Stream) != 2 || len(session.Checkpoints) != 2 {
		t.Fatalf("workflow observations = stream %d/checkpoints %d, want 2/2", len(session.Stream), len(session.Checkpoints))
	}

	if err := lifecycle.Fail(agentcore.SessionWorkflow, "multi-step", errors.New("workflow failed")); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	failed, err := lifecycle.Get(agentcore.SessionWorkflow, "multi-step")
	if err != nil {
		t.Fatalf("Get failed workflow: %v", err)
	}
	if failed.Status != agentcore.SessionFailed || failed.Failure != "workflow failed" {
		t.Fatalf("failed workflow = %+v", failed)
	}
	if err := lifecycle.ResumeLatest(agentcore.SessionWorkflow, "multi-step"); err != nil {
		t.Fatalf("ResumeLatest: %v", err)
	}
	resumed, err := lifecycle.Get(agentcore.SessionWorkflow, "multi-step")
	if err != nil {
		t.Fatalf("Get resumed workflow: %v", err)
	}
	if resumed.Status != agentcore.SessionRunning || resumed.Attempt != 2 || resumed.ResumedFrom == "" {
		t.Fatalf("resumed workflow = %+v", resumed)
	}
	if err := lifecycle.Complete(agentcore.SessionWorkflow, "multi-step"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	completed, err := lifecycle.Get(agentcore.SessionWorkflow, "multi-step")
	if err != nil {
		t.Fatalf("Get completed workflow: %v", err)
	}
	if completed.Status != agentcore.SessionCompleted || completed.CompletedAt == nil {
		t.Fatalf("completed workflow = %+v", completed)
	}
}

func TestAgentLifecycleTerminalStateRevokesRuntimeAuthority(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if _, err := lifecycle.Begin(agentcore.SessionWorkflow, "terminal"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := lifecycle.Checkpoint(agentcore.SessionWorkflow, "terminal", "ready", map[string]interface{}{"step": 1}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	const sessionID = "workflow:terminal"
	if err := lifecycle.Fail(agentcore.SessionWorkflow, "terminal", errors.New("retry")); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if !runtime.IsSessionRegistered(sessionID) || runtime.IsSessionActive(sessionID) {
		t.Fatalf("failed session registered=%v active=%v, want true/false", runtime.IsSessionRegistered(sessionID), runtime.IsSessionActive(sessionID))
	}
	if err := lifecycle.ResumeLatest(agentcore.SessionWorkflow, "terminal"); err != nil {
		t.Fatalf("ResumeLatest: %v", err)
	}
	if !runtime.IsSessionActive(sessionID) {
		t.Fatal("resumed lifecycle did not reactivate runtime session")
	}
	if err := lifecycle.Complete(agentcore.SessionWorkflow, "terminal"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("completed lifecycle left runtime session registered")
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "search",
		Arguments: map[string]interface{}{"query": "needle"},
	}); !errors.Is(err, agentcore.ErrUnknownSession) {
		t.Fatalf("completed session issuance = %v, want ErrUnknownSession", err)
	}
}

func TestAgentLifecycleCompletionBurnsCapabilityAcrossTrustedReregistration(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	const sessionID = "workflow:reincarnation"
	if _, err := lifecycle.Begin(agentcore.SessionWorkflow, sessionID); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	arguments := map[string]interface{}{"query": "needle"}
	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "search", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("RequestAgentToolCapability: %v", err)
	}
	if err := lifecycle.Complete(agentcore.SessionWorkflow, sessionID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := agent.registerAgentSession(sessionID); err != nil {
		t.Fatalf("trusted same-ID registration: %v", err)
	}
	if _, err := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "search", Arguments: arguments,
	}); !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("old capability after same-ID registration = %v, want ErrInvalidCapability", err)
	}
}

func TestAgentLifecycleRejectedTerminalTransitionsPreserveRuntimeState(t *testing.T) {
	tests := []struct {
		name       string
		transition func(*AgentLifecycle) error
		duplicate  func(*AgentLifecycle) error
		wantStatus agentcore.SessionStatus
	}{
		{
			name: "duplicate fail",
			transition: func(lifecycle *AgentLifecycle) error {
				return lifecycle.Fail(agentcore.SessionWorkflow, "duplicate", errors.New("first failure"))
			},
			duplicate: func(lifecycle *AgentLifecycle) error {
				return lifecycle.Fail(agentcore.SessionWorkflow, "duplicate", errors.New("second failure"))
			},
			wantStatus: agentcore.SessionFailed,
		},
		{
			name: "duplicate pause",
			transition: func(lifecycle *AgentLifecycle) error {
				return lifecycle.Pause(agentcore.SessionWorkflow, "duplicate")
			},
			duplicate: func(lifecycle *AgentLifecycle) error {
				return lifecycle.Pause(agentcore.SessionWorkflow, "duplicate")
			},
			wantStatus: agentcore.SessionPaused,
		},
		{
			name: "complete failed session",
			transition: func(lifecycle *AgentLifecycle) error {
				return lifecycle.Fail(agentcore.SessionWorkflow, "duplicate", errors.New("terminal failure"))
			},
			duplicate: func(lifecycle *AgentLifecycle) error {
				return lifecycle.Complete(agentcore.SessionWorkflow, "duplicate")
			},
			wantStatus: agentcore.SessionFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := NewAgentService()
			t.Cleanup(func() { _ = agent.Close() })
			lifecycle, err := WireAgentLifecycle(
				agent,
				NewAIService(),
				NewAIPlanService(),
				NewAIGoalService(),
				NewAIPermissionService(t.TempDir()),
			)
			if err != nil {
				t.Fatalf("WireAgentLifecycle: %v", err)
			}
			if _, err := lifecycle.Begin(agentcore.SessionWorkflow, "duplicate"); err != nil {
				t.Fatalf("Begin: %v", err)
			}
			if _, err := lifecycle.Checkpoint(agentcore.SessionWorkflow, "duplicate", "ready", map[string]interface{}{"step": 1}); err != nil {
				t.Fatalf("Checkpoint: %v", err)
			}
			if err := tt.transition(lifecycle); err != nil {
				t.Fatalf("initial transition: %v", err)
			}
			if err := tt.duplicate(lifecycle); !errors.Is(err, agentcore.ErrInvalidSessionTransition) {
				t.Fatalf("rejected transition = %v, want ErrInvalidSessionTransition", err)
			}
			session, err := lifecycle.Get(agentcore.SessionWorkflow, "duplicate")
			if err != nil || session.Status != tt.wantStatus {
				t.Fatalf("session after rejected transition = %+v, err=%v", session, err)
			}
			runtime, err := agent.coreRuntime()
			if err != nil {
				t.Fatalf("coreRuntime: %v", err)
			}
			const sessionID = "workflow:duplicate"
			if !runtime.IsSessionRegistered(sessionID) || runtime.IsSessionActive(sessionID) {
				t.Fatalf("runtime after rejected transition registered=%v active=%v, want true/false", runtime.IsSessionRegistered(sessionID), runtime.IsSessionActive(sessionID))
			}
		})
	}
}

func TestCloseAgentSessionCompletesRunningLifecycle(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	sessionID, err := agent.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	if _, err := lifecycle.BeginExisting(agentcore.SessionChat, sessionID); err != nil {
		t.Fatalf("BeginExisting: %v", err)
	}
	if err := agent.closeAgentSessionTrusted(sessionID); err != nil {
		t.Fatalf("CloseAgentSession: %v", err)
	}
	session, err := lifecycle.GetByID(sessionID)
	if err != nil || session.Status != agentcore.SessionCompleted {
		t.Fatalf("closed session = %+v, err=%v", session, err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("closed session retained runtime authority")
	}
	if err := agent.closeAgentSessionTrusted(sessionID); err != nil {
		t.Fatalf("idempotent CloseAgentSession: %v", err)
	}
}

func TestCloseAgentSessionMakesFailedLifecycleTerminal(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	sessionID, err := agent.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	if _, err := lifecycle.BeginExisting(agentcore.SessionChat, sessionID); err != nil {
		t.Fatalf("BeginExisting: %v", err)
	}
	if _, err := lifecycle.Checkpoint(agentcore.SessionChat, sessionID, "ready", map[string]interface{}{"step": 1}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := lifecycle.Fail(agentcore.SessionChat, sessionID, errors.New("provider failed")); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := agent.closeAgentSessionTrusted(sessionID); err != nil {
		t.Fatalf("CloseAgentSession: %v", err)
	}
	closed, err := lifecycle.GetByID(sessionID)
	if err != nil || closed.Status != agentcore.SessionCompleted {
		t.Fatalf("closed failed session = %+v, err=%v", closed, err)
	}
	if err := lifecycle.ResumeLatest(agentcore.SessionChat, sessionID); !errors.Is(err, agentcore.ErrInvalidSessionTransition) {
		t.Fatalf("ResumeLatest after close = %v, want ErrInvalidSessionTransition", err)
	}
}

func TestCloseAgentSessionAndResumeConvergeOnRevokedTerminalState(t *testing.T) {
	for iteration := 0; iteration < 25; iteration++ {
		agent := NewAgentService()
		lifecycle, err := WireAgentLifecycle(
			agent,
			NewAIService(),
			NewAIPlanService(),
			NewAIGoalService(),
			NewAIPermissionService(t.TempDir()),
		)
		if err != nil {
			t.Fatalf("iteration %d WireAgentLifecycle: %v", iteration, err)
		}
		sessionID, err := agent.createAgentSessionTrusted("chat")
		if err != nil {
			t.Fatalf("iteration %d CreateAgentSession: %v", iteration, err)
		}
		if _, err := lifecycle.BeginExisting(agentcore.SessionChat, sessionID); err != nil {
			t.Fatalf("iteration %d BeginExisting: %v", iteration, err)
		}
		if _, err := lifecycle.Checkpoint(agentcore.SessionChat, sessionID, "ready", map[string]interface{}{"iteration": iteration}); err != nil {
			t.Fatalf("iteration %d Checkpoint: %v", iteration, err)
		}
		if err := lifecycle.Fail(agentcore.SessionChat, sessionID, errors.New("retryable")); err != nil {
			t.Fatalf("iteration %d Fail: %v", iteration, err)
		}

		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			_ = lifecycle.ResumeLatest(agentcore.SessionChat, sessionID)
		}()
		go func() {
			defer wait.Done()
			_ = agent.closeAgentSessionTrusted(sessionID)
		}()
		wait.Wait()

		closed, err := lifecycle.GetByID(sessionID)
		if err != nil || closed.Status != agentcore.SessionCompleted {
			t.Fatalf("iteration %d final lifecycle = %+v, err=%v", iteration, closed, err)
		}
		runtime, err := agent.coreRuntime()
		if err != nil {
			t.Fatalf("iteration %d coreRuntime: %v", iteration, err)
		}
		if runtime.IsSessionRegistered(sessionID) {
			t.Fatalf("iteration %d retained runtime authority", iteration)
		}
		if err := agent.Close(); err != nil {
			t.Fatalf("iteration %d agent.Close: %v", iteration, err)
		}
	}
}

func TestWorkspaceChangeClosesLifecycleOwnersAndRuntimeSessions(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	plan := NewAIPlanService()
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), plan, NewAIGoalService(), NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := agent.configureWorkspaceRoot(rootA); err != nil {
		t.Fatalf("configure workspace A: %v", err)
	}
	if _, err := plan.CreatePlan("workspace-plan", "inspect", nil); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	planRuntimeID := lifecycle.runtimeSessionID(agentcore.SessionPlan, "workspace-plan")
	chatID, err := agent.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	if _, err := lifecycle.BeginExisting(agentcore.SessionChat, chatID); err != nil {
		t.Fatalf("BeginExisting chat: %v", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if !runtime.IsSessionRegistered(planRuntimeID) || !runtime.IsSessionRegistered(chatID) {
		t.Fatalf("sessions not registered before switch: plan=%v chat=%v", runtime.IsSessionRegistered(planRuntimeID), runtime.IsSessionRegistered(chatID))
	}

	if err := agent.configureWorkspaceRoot(rootB); err != nil {
		t.Fatalf("configure workspace B: %v", err)
	}
	for _, tc := range []struct {
		kind agentcore.SessionKind
		id   string
	}{
		{kind: agentcore.SessionPlan, id: "workspace-plan"},
		{kind: agentcore.SessionChat, id: chatID},
	} {
		session, err := lifecycle.Get(tc.kind, tc.id)
		if err != nil || session.Status != agentcore.SessionCompleted {
			t.Fatalf("workspace-reset session %s/%s = %+v, err=%v", tc.kind, tc.id, session, err)
		}
	}
	if runtime.IsSessionRegistered(planRuntimeID) || runtime.IsSessionRegistered(chatID) {
		t.Fatalf("workspace switch retained runtime authority: plan=%v chat=%v", runtime.IsSessionRegistered(planRuntimeID), runtime.IsSessionRegistered(chatID))
	}
	if got := lifecycle.runtimeSessionID(agentcore.SessionPlan, "workspace-plan"); got != lifecycleSessionID(agentcore.SessionPlan, "workspace-plan") {
		t.Fatalf("workspace switch retained owner mapping %q", got)
	}
}

func TestStartAgentStreamBusyDoesNotFailPersistentSession(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	// A non-nil inert pointer is sufficient because the busy branch returns
	// before emitting events. application.New would mutate Wails' process-global
	// App and make later non-GUI tests accidentally open native dialogs.
	ai.app = &application.App{}
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", Model: "test-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		ai,
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	callerCtx := testAIStreamCallerContext()
	sessionID, err := agent.CreateAgentSessionForCaller(callerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	if _, err := lifecycle.BeginExisting(agentcore.SessionChat, sessionID); err != nil {
		t.Fatalf("BeginExisting: %v", err)
	}
	_, cancel := context.WithCancel(context.Background())
	ai.cancel = &streamCancel{fn: cancel}
	ai.activeStreamID = "already-running"
	t.Cleanup(func() {
		cancel()
		ai.mu.Lock()
		ai.cancel = nil
		ai.activeStreamID = ""
		ai.app = nil
		ai.mu.Unlock()
	})
	if _, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "duplicate"}}); !errors.Is(err, ErrStreamBusy) {
		t.Fatalf("duplicate StartAgentStream = %v, want ErrStreamBusy", err)
	}
	session, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if session.Status != agentcore.SessionRunning {
		t.Fatalf("duplicate start changed session to %s, want running", session.Status)
	}
}

func TestStartAgentStreamBusyDoesNotResumeFailedSessionOrRunPreflight(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent,
		ai,
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	callerCtx := testAIStreamCallerContext()
	sessionID, err := agent.CreateAgentSessionForCaller(callerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	if _, err := lifecycle.BeginExisting(agentcore.SessionChat, sessionID); err != nil {
		t.Fatalf("BeginExisting: %v", err)
	}
	if _, err := lifecycle.Checkpoint(agentcore.SessionChat, sessionID, "retry", map[string]interface{}{"step": 1}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := lifecycle.Fail(agentcore.SessionChat, sessionID, errors.New("provider failed")); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	_, cancel := context.WithCancel(context.Background())
	ai.cancel = &streamCancel{fn: cancel}
	ai.activeStreamID = "already-running"
	t.Cleanup(func() {
		cancel()
		ai.mu.Lock()
		ai.cancel = nil
		ai.activeStreamID = ""
		ai.mu.Unlock()
	})

	if _, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "duplicate"}}); !errors.Is(err, ErrStreamBusy) {
		t.Fatalf("StartAgentStream = %v, want ErrStreamBusy before provider preflight", err)
	}
	session, err := lifecycle.GetByID(sessionID)
	if err != nil || session.Status != agentcore.SessionFailed || session.Attempt != 1 {
		t.Fatalf("busy request changed failed session = %+v, err=%v", session, err)
	}
	if records := permission.usageRecordsSnapshot(); len(records) != 0 {
		t.Fatalf("busy rejection produced usage records: %+v", records)
	}
}

func TestStartAgentStreamPreflightFailureLeavesResumableCheckpoint(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent,
		ai,
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	callerCtx := testAIStreamCallerContext()
	sessionID, err := agent.CreateAgentSessionForCaller(callerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	if _, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "retry me"}}); err == nil {
		t.Fatal("StartAgentStream succeeded without provider configuration")
	}
	failed, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if failed.Status != agentcore.SessionFailed || len(failed.Checkpoints) == 0 {
		t.Fatalf("preflight failure session = %+v, want failed with checkpoint", failed)
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 1 || records[0].UnitKind != string(agentcore.UsageUnitChat) || records[0].Success {
		t.Fatalf("preflight failure usage records = %+v, want one failed chat unit", records)
	}
	resumed, err := lifecycle.BeginExisting(agentcore.SessionChat, sessionID)
	if err != nil {
		t.Fatalf("BeginExisting after corrected preflight: %v", err)
	}
	if resumed.Status != agentcore.SessionRunning || resumed.Attempt != 2 {
		t.Fatalf("resumed session = %+v", resumed)
	}
}

func TestAgentLifecycleContextMarksDroppedMiddleMessages(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	prepared, _, err := lifecycle.prepareChatMessages(AIConfig{
		SystemPrompt: "system", ContextWindow: 35,
	}, []ChatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: strings.Repeat("x", 100)},
		{Role: "user", Content: "recent"},
	})
	if err != nil {
		t.Fatalf("prepareChatMessages: %v", err)
	}
	if len(prepared) != 4 || prepared[0].Content != "system" || prepared[1].Content != "first" || prepared[3].Content != "recent" {
		t.Fatalf("prepared context order = %+v", prepared)
	}
	if prepared[2].Role != "system" || !strings.Contains(prepared[2].Content, "1 earlier messages were truncated") {
		t.Fatalf("missing truncation marker between head and recent tail: %+v", prepared)
	}
}

func TestPlanAndGoalExecutionUseSharedLifecycleAndMeter(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	plan := NewAIPlanService()
	goal := NewAIGoalService()
	lifecycle, err := WireAgentLifecycle(agent, NewAIService(), plan, goal, permission)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}

	if _, err := plan.CreatePlan("p1", "inspect", []PlanStep{{Title: "read", Tool: "read"}}); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if err := plan.ApproveStep("p1", 0); err != nil {
		t.Fatalf("ApproveStep: %v", err)
	}
	assertPending := func(kind agentcore.UsageUnitKind) {
		t.Helper()
		for _, record := range permission.usageRecordsSnapshot() {
			if record.UnitKind == string(kind) && record.Pending && !record.Success {
				return
			}
		}
		t.Fatalf("no pending %s receipt before executor call: %+v", kind, permission.usageRecordsSnapshot())
	}
	if err := plan.ExecuteStep("p1", 0, usageObservingStepExecutor{
		observe: func() { assertPending(agentcore.UsageUnitPlan) },
	}); err != nil {
		t.Fatalf("ExecuteStep: %v", err)
	}
	planSession, err := lifecycle.Get(agentcore.SessionPlan, "p1")
	if err != nil || planSession.Status != agentcore.SessionCompleted || len(planSession.Checkpoints) == 0 {
		t.Fatalf("plan session = %+v, err=%v", planSession, err)
	}

	if _, err := goal.CreateGoal("g1", "finish", "done", 2, 1, time.Minute, true); err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	executor := usageObservingGoalExecutor{
		observe: func() { assertPending(agentcore.UsageUnitGoal) },
	}
	if err := goal.RunGoal("g1", executor, nil); err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	goalSession, err := lifecycle.Get(agentcore.SessionGoal, "g1")
	if err != nil || goalSession.Status != agentcore.SessionCompleted || len(goalSession.Checkpoints) == 0 {
		t.Fatalf("goal session = %+v, err=%v", goalSession, err)
	}

	records := permission.usageRecordsSnapshot()
	if len(records) != 2 {
		t.Fatalf("orchestration usage records = %+v, want plan and goal", records)
	}
	if records[0].UnitKind != string(agentcore.UsageUnitPlan) || records[0].CostBasis != string(agentcore.CostNotApplicable) {
		t.Fatalf("plan usage = %+v", records[0])
	}
	if records[1].UnitKind != string(agentcore.UsageUnitGoal) || records[1].CostBasis != string(agentcore.CostEstimated) || !records[1].Estimated {
		t.Fatalf("goal usage = %+v", records[1])
	}
}

func TestUnifiedMeterPreservesProviderReportedBasis(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if _, err := lifecycle.Begin(agentcore.SessionChat, "reported"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	now := time.Now()
	if err := lifecycle.Record(agentcore.UsageRecord{
		SessionID: lifecycleSessionID(agentcore.SessionChat, "reported"),
		UnitKind:  agentcore.UsageUnitAI, Operation: string(AIOpChat),
		TokensIn: 3, TokensOut: 2, Cost: 0.01, Currency: "USD",
		CostBasis: agentcore.CostProviderReported,
		StartedAt: now, CompletedAt: now.Add(time.Second), Success: true,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 1 || records[0].CostBasis != string(agentcore.CostProviderReported) || records[0].Estimated {
		t.Fatalf("provider-reported usage = %+v", records)
	}
}
