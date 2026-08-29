package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func newAgentCoreAuditTestService(t *testing.T) (*AgentService, string) {
	t.Helper()
	auditPath := filepath.Join(t.TempDir(), "agent-audit.log")
	auditFile, err := os.OpenFile(auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open audit fixture: %v", err)
	}
	agent := &AgentService{
		auditLog:    auditFile,
		auditLogger: slog.New(slog.NewTextHandler(auditFile, nil)),
	}
	t.Cleanup(func() { _ = agent.Close() })
	return agent, auditPath
}

type blockingAgentMCPToolLister struct {
	started chan struct{}
}

func (l *blockingAgentMCPToolLister) ListAgentMCPTools(ctx context.Context) ([]AgentMCPTool, error) {
	close(l.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAgentCatalogMCPRefreshIsBoundedAndFailClosed(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if _, err := runtime.Registry().ReplaceSource(agentcore.SourceMCP, []agentcore.ToolDef{{
		ID: "mcp.stale.tool", Description: "stale tool",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Source:      agentcore.SourceMCP, Risk: agentcore.RiskElevated, Approval: agentcore.ApprovalManual,
		Mutation: agentcore.MutationExternal, ExecuteKey: "mcp.call",
	}}); err != nil {
		t.Fatalf("seed stale MCP source: %v", err)
	}

	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.catalogMCPRefreshTimeout = 25 * time.Millisecond
	deps.mu.Unlock()
	lister := &blockingAgentMCPToolLister{started: make(chan struct{})}
	startedAt := time.Now()
	err = agent.refreshMCPAgentTools(context.Background(), runtime, lister)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refresh error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("bounded MCP refresh took %s", elapsed)
	}
	select {
	case <-lister.started:
	default:
		t.Fatal("MCP lister was not called")
	}
	for _, tool := range runtime.Registry().Snapshot().Tools {
		if tool.Source == agentcore.SourceMCP {
			t.Fatalf("stale MCP tool remained published after timeout: %s", tool.ID)
		}
	}
}

func TestAgentCoreAuditSinkIncludesExternalReceiptState(t *testing.T) {
	agent, auditPath := newAgentCoreAuditTestService(t)
	if err := (agentCoreAuditSink{agent: agent}).RecordAudit(agentcore.AuditRecord{
		Stage: agentcore.AuditExecutionFailed, SessionID: "chat:audit", ToolID: "mcp.publish",
		ExternalReceiptID: "mcp:receipt-1", ExternalReceiptReversible: true,
		ExternalCompensation: agentcore.ExternalCompensationIrreversible,
	}); err != nil {
		t.Fatalf("RecordAudit: %v", err)
	}
	loggedBytes, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit fixture: %v", err)
	}
	logged := string(loggedBytes)
	for _, expected := range []string{
		"externalReceiptId=mcp:receipt-1",
		"externalReceiptReversible=true",
		"externalCompensation=irreversible",
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("audit log missing %q: %s", expected, logged)
		}
	}
}

func TestProjectAgentExecutionResultSerializesIrreversibleReceiptState(t *testing.T) {
	result := projectAgentExecutionResult(nil, agentcore.ExecutionResult{
		Usage: agentcore.UsageRecord{
			UnitID: "tool-unit-1", SessionID: "plan-1", UnitKind: agentcore.UsageUnitTool,
			Operation: "run", Success: true,
			ExternalReceiptID: "command:receipt-1", ExternalReceiptReversible: false,
			ExternalCompensation: agentcore.ExternalCompensationNotNeeded,
		},
	}, nil)
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal AgentToolExecutionResult: %v", err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(payload, &encoded); err != nil {
		t.Fatalf("decode AgentToolExecutionResult: %v", err)
	}
	usage, ok := encoded["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage payload missing: %s", payload)
	}
	if value, ok := usage["externalReceiptReversible"]; !ok || value != false {
		t.Fatalf("externalReceiptReversible = %#v, want explicit false: %s", value, payload)
	}
	if usage["externalReceiptId"] != "command:receipt-1" || usage["externalCompensation"] != string(agentcore.ExternalCompensationNotNeeded) {
		t.Fatalf("receipt metadata changed at renderer boundary: %s", payload)
	}
}

func TestAgentCoreAuditSinkRedactsWorkspacePaths(t *testing.T) {
	root := t.TempDir()
	workspace := NewWorkspaceContext()
	if err := workspace.Set(root); err != nil {
		t.Fatalf("workspace.Set: %v", err)
	}
	agent, auditPath := newAgentCoreAuditTestService(t)
	agent.workspaceContext = workspace
	if err := (agentCoreAuditSink{agent: agent}).RecordAudit(agentcore.AuditRecord{
		Stage: agentcore.AuditExecutionFailed, SessionID: "workflow:audit", ToolID: "workflow.file.write",
		Error: fmt.Sprintf("write failed at %s", filepath.Join(root, "notes.txt")),
	}); err != nil {
		t.Fatalf("RecordAudit: %v", err)
	}
	logged, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit fixture: %v", err)
	}
	if strings.Contains(string(logged), root) || !strings.Contains(string(logged), "<workspace>") {
		t.Fatalf("audit path was not redacted: %s", logged)
	}
}

func TestAgentCoreAuditSinkPropagatesWriteFailure(t *testing.T) {
	auditFile, err := os.CreateTemp(t.TempDir(), "agent-audit-*.log")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	agent := &AgentService{
		auditLog:    auditFile,
		auditLogger: slog.New(slog.NewTextHandler(auditFile, nil)),
	}
	if err := auditFile.Close(); err != nil {
		t.Fatalf("close audit fixture: %v", err)
	}
	err = agentCoreAuditSink{agent: agent}.RecordAudit(agentcore.AuditRecord{
		Stage: agentcore.AuditExecutionStarted, SessionID: "chat:audit-failure", ToolID: "read",
	})
	if !errors.Is(err, agentcore.ErrAuditUnavailable) {
		t.Fatalf("RecordAudit error = %v, want ErrAuditUnavailable", err)
	}
	publicErr := redactAgentWorkspaceError(agent, err)
	if publicErr.Error() != agentcore.ErrAuditUnavailable.Error() || !errors.Is(publicErr, agentcore.ErrAuditUnavailable) {
		t.Fatalf("public audit error = %v, want bounded ErrAuditUnavailable", publicErr)
	}
	agent.auditLog = nil
}

func TestAgentCoreAuditSinkRejectsReplacedRootBoundLeaf(t *testing.T) {
	state := t.TempDir()
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()
	auditFile, err := openAgentStateRegularFile(root, "agent-audit.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open audit leaf: %v", err)
	}
	agent := &AgentService{
		auditLog:    auditFile,
		auditLogger: slog.New(slog.NewTextHandler(auditFile, nil)),
	}
	defer func() { _ = agent.Close() }()
	if err := root.Rename("agent-audit.log", "detached-audit.log"); err != nil {
		t.Fatalf("detach audit leaf: %v", err)
	}
	replacement, err := root.OpenFile("agent-audit.log", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create replacement audit leaf: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatalf("close replacement audit leaf: %v", err)
	}
	err = agentCoreAuditSink{agent: agent}.RecordAudit(agentcore.AuditRecord{
		Stage: agentcore.AuditExecutionStarted, SessionID: "chat:audit-replaced", ToolID: "read",
	})
	if !errors.Is(err, agentcore.ErrAuditUnavailable) {
		t.Fatalf("RecordAudit error = %v, want ErrAuditUnavailable", err)
	}
	replacementData, err := os.ReadFile(filepath.Join(state, "agent-audit.log"))
	if err != nil {
		t.Fatalf("read replacement audit leaf: %v", err)
	}
	if len(replacementData) != 0 {
		t.Fatalf("replacement audit leaf was modified: %q", replacementData)
	}
}

type externalCompletionFailMeter struct {
	begun          []agentcore.UsageRecord
	completed      []agentcore.UsageRecord
	completeErr    error
	completeErrors []error
	completeCalls  int
}

func (m *externalCompletionFailMeter) RecordUsage(record agentcore.UsageRecord) error {
	m.completed = append(m.completed, record)
	return nil
}

func (m *externalCompletionFailMeter) BeginUsage(record agentcore.UsageRecord) (agentcore.UsageReceipt, error) {
	m.begun = append(m.begun, record)
	return agentcore.UsageReceipt{UnitID: record.UnitID}, nil
}

func (m *externalCompletionFailMeter) CompleteUsage(_ agentcore.UsageReceipt, record agentcore.UsageRecord) error {
	m.completeCalls++
	if len(m.completeErrors) > 0 {
		err := m.completeErrors[0]
		m.completeErrors = m.completeErrors[1:]
		if err != nil {
			return err
		}
	}
	if m.completeErr != nil {
		return m.completeErr
	}
	m.completed = append(m.completed, record)
	return nil
}

func newExecutionCoreTestServices(t *testing.T) (*AgentService, *FileService, *SearchService, string) {
	t.Helper()
	root := t.TempDir()
	workspace := NewWorkspaceContext()
	if err := workspace.Set(root); err != nil {
		t.Fatalf("workspace.Set: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(root); err != nil {
		t.Fatalf("configureWorkspaceRoot: %v", err)
	}
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	agent.approveWrite = func(string, int64) bool { return true }
	file := NewFileServiceWithWorkspaceContext(workspace)
	if err := file.setWorkspaceRoot(root); err != nil {
		t.Fatalf("file.setWorkspaceRoot: %v", err)
	}
	search := NewSearchService()
	search.setWorkspaceContext(workspace)
	if err := search.setWorkspaceRoot(root); err != nil {
		t.Fatalf("search.setWorkspaceRoot: %v", err)
	}
	git := NewGitService()
	if err := git.setWorkspaceRoot(root); err != nil {
		t.Fatalf("git.setWorkspaceRoot: %v", err)
	}
	if err := WireAgentExecutionCore(agent, file, search, nil, nil, nil, git); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	// Production runtimes require backend-owned session registration. These
	// fixture IDs stand in for the IDs a renderer receives from
	// CreateAgentSession, while keeping the focused execution tests readable.
	for _, sessionID := range []string{
		"chat-1", "chat-2", "chat-3", "chat-4", "plan-1", "workflow-1",
		"workflow:verify", "chat:skill", "chat:pending", "chat:readonly",
		"chat:other", "workflow:metered", "workflow:multi-step",
	} {
		if err := agent.registerAgentSession(sessionID); err != nil {
			t.Fatalf("register fixture session %q: %v", sessionID, err)
		}
	}
	return agent, file, search, root
}

func TestNewAgentServiceInstallsNativeWriteApprover(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	if agent.approveWrite == nil {
		t.Fatal("production AgentService must install the native write approver")
	}
}

func TestAgentExecutionCoreCatalogAndReadWriteSearchUseOneRuntime(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("alpha beta"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	if catalog.Revision == 0 {
		t.Fatal("catalog has zero revision")
	}
	want := map[string]bool{"read": false, "write": false, "run": false, "search": false, "codebase": false, "git.status": false, "git.diff": false, "plan": false}
	for _, tool := range catalog.Tools {
		if _, exists := want[tool.ID]; exists {
			want[tool.ID] = true
		}
	}
	for id, found := range want {
		if !found {
			t.Fatalf("builtin tool %q missing from catalog: %+v", id, catalog.Tools)
		}
	}

	readResult, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "read",
		Arguments: map[string]interface{}{"path": "note.txt"},
	})
	if err != nil {
		t.Fatalf("read ExecuteAgentTool: %v", err)
	}
	if readResult.Observation == "" {
		t.Fatal("read observation is empty")
	}

	writeResult, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "write",
		Arguments: map[string]interface{}{"path": "note.txt", "content": "gamma delta"},
	})
	if err != nil {
		t.Fatalf("write ExecuteAgentTool: %v", err)
	}
	if writeResult.Usage.UnitKind != string(agentcore.UsageUnitTool) || writeResult.Usage.Estimated {
		t.Fatalf("write usage = %+v", writeResult.Usage)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "gamma delta" {
		t.Fatalf("disk after write = %q, err=%v", data, err)
	}

	searchResult, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "search",
		Arguments: map[string]interface{}{"query": "gamma", "ignoreCase": true},
	})
	if err != nil {
		t.Fatalf("search ExecuteAgentTool: %v", err)
	}
	if searchResult.Observation == "" {
		t.Fatal("search observation is empty")
	}
}

func TestAgentExecutionCoreCodebaseSearchReturnsPathLineAndRejectsEscape(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("unique-needle-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range catalog.Tools {
		if tool.ID == "codebase" {
			found = true
			if extra, ok := tool.InputSchema["additionalProperties"]; !ok || extra != false {
				t.Fatalf("codebase schema additionalProperties = %#v", tool.InputSchema["additionalProperties"])
			}
		}
	}
	if !found {
		t.Fatal("codebase missing from catalog")
	}
	hit, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "codebase",
		Arguments: map[string]interface{}{"query": "unique-needle-token"},
	})
	if err != nil {
		t.Fatalf("codebase: %v", err)
	}
	if !strings.Contains(hit.Observation, "note.txt") || !strings.Contains(hit.Observation, "unique-needle-token") {
		t.Fatalf("codebase observation missing path/line hit: %q", hit.Observation)
	}
	if !strings.Contains(hit.Observation, "not a vector index") {
		t.Fatalf("codebase must say it is text search: %q", hit.Observation)
	}
	emptyAgent, _, _, emptyRoot := newExecutionCoreTestServices(t)
	_ = emptyRoot
	emptyCatalog, err := emptyAgent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	emptyHit, err := emptyAgent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: emptyCatalog.Revision, ToolID: "codebase",
		Arguments: map[string]interface{}{"query": "unique-needle-token"},
	})
	if err != nil {
		t.Fatalf("empty codebase: %v", err)
	}
	if strings.Contains(emptyHit.Observation, "note.txt") || strings.Contains(emptyHit.Observation, "unique-needle-token") && !strings.Contains(emptyHit.Observation, "0 hits") {
		if !strings.Contains(emptyHit.Observation, "0 hits") && !strings.Contains(emptyHit.Observation, "[]") {
			t.Fatalf("empty workspace invented a hit: %q", emptyHit.Observation)
		}
	}
}

func TestAgentExecutionCoreGitStatusAndDiffUseWorkspaceRoot(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	git := deps.git
	deps.mu.RUnlock()
	if git == nil {
		t.Fatal("git service is not wired")
	}
	if err := git.InitRepo(root); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("hello world"), 0o600); err != nil {
		t.Fatalf("seed untracked: %v", err)
	}
	outside := t.TempDir()
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	status, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "git.status",
		Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("git.status: %v", err)
	}
	if !strings.Contains(status.Observation, "tracked.txt") {
		t.Fatalf("git.status observation missing tracked.txt: %q", status.Observation)
	}
	if _, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "git.status",
		Arguments: map[string]interface{}{"path": outside},
	}); err == nil {
		t.Fatal("git.status accepted a renderer-chosen path")
	}
	diff, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "git.diff",
		Arguments: map[string]interface{}{"path": "tracked.txt"},
	})
	if err != nil {
		t.Fatalf("git.diff: %v", err)
	}
	if !strings.Contains(diff.Observation, "hello world") {
		t.Fatalf("git.diff observation missing content: %q", diff.Observation)
	}
	if _, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "git.diff",
		Arguments: map[string]interface{}{"path": filepath.Join("..", filepath.Base(outside), "secret.txt")},
	}); err == nil {
		t.Fatal("git.diff accepted a path outside the workspace")
	}
}

func TestAgentExecutionCoreRunApprovalUsesCanonicalPreparedCwd(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	var approvedCwd string
	agent.approveCommand = func(_ string, cwd string, _ RiskLevel) bool {
		approvedCwd = cwd
		return true
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "plan-1", CatalogRevision: catalog.Revision, ToolID: "run",
		Arguments: map[string]interface{}{"command": "go version", "cwd": "."},
	}); err != nil {
		t.Fatalf("RequestAgentToolCapability: %v", err)
	}
	if approvedCwd == "" || !filepath.IsAbs(approvedCwd) || !sameWorkspaceIdentityPath(approvedCwd, root) {
		t.Fatalf("approval cwd = %q, want canonical workspace root %q", approvedCwd, root)
	}
}

func TestAgentCoreApproverRejectsUntrustedRunCwdMetadata(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	currentGeneration := agent.agentWorkspaceGeneration()
	var approvalCalls int
	agent.approveCommand = func(_ string, _ string, _ RiskLevel) bool {
		approvalCalls++
		return true
	}
	tool := agentcore.ToolDef{ID: "run", Risk: agentcore.RiskDangerous}
	invocation := agentcore.Invocation{
		Tool:      tool,
		Arguments: json.RawMessage(`{"command":"go version","cwd":"."}`),
	}
	approver := agentCoreApprover{agent: agent}
	for name, testCase := range map[string]struct {
		metadata   map[string]string
		generation uint64
	}{
		"missing":  {metadata: nil, generation: currentGeneration},
		"relative": {metadata: map[string]string{"resolvedCwd": "."}, generation: currentGeneration},
		"outside":  {metadata: map[string]string{"resolvedCwd": filepath.Dir(root)}, generation: currentGeneration},
		"stale":    {metadata: map[string]string{"resolvedCwd": root}, generation: currentGeneration + 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := approver.Approve(context.Background(), agentcore.ApprovalRequest{
				Invocation: invocation, Metadata: testCase.metadata, WorkspaceGeneration: testCase.generation,
			}); err == nil {
				t.Fatal("untrusted cwd metadata was accepted")
			}
		})
	}
	if approvalCalls != 0 {
		t.Fatalf("native approver called %d times for rejected metadata", approvalCalls)
	}
	if _, err := approver.Approve(context.Background(), agentcore.ApprovalRequest{
		Invocation: invocation, WorkspaceGeneration: currentGeneration,
		Metadata: map[string]string{"resolvedCwd": root},
	}); err != nil {
		t.Fatalf("canonical cwd metadata rejected: %v", err)
	}
	if approvalCalls != 1 {
		t.Fatalf("native approver calls = %d, want 1", approvalCalls)
	}
}

func TestAgentExecutionCoreBoundsRunAndSearchObservationsWithoutChangingUsage(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}

	longOutput := strings.Repeat("界", maxAgentObservationBytes)
	ctx, capture := withAgentRunCapture(context.Background(), func(command, cwd string) (ExecResult, error) {
		return ExecResult{
			Command: command,
			Cwd:     cwd,
			Stdout:  longOutput,
			Stderr:  longOutput,
		}, nil
	})
	runResult, err := agent.ExecuteAgentTool(ctx, AgentToolExecutionRequest{
		SessionID: "plan-1", CatalogRevision: catalog.Revision, ToolID: "run",
		Arguments: map[string]interface{}{"command": "go version"},
	})
	if err != nil {
		t.Fatalf("run ExecuteAgentTool: %v", err)
	}
	if !capture.invoked {
		t.Fatal("run handler was not invoked")
	}
	assertBoundedTerminalAgentObservation(t, "run", runResult)

	searchLine := "needle " + strings.Repeat("界", maxAgentObservationBytes)
	if err := os.WriteFile(filepath.Join(root, "large-search.txt"), []byte(searchLine), 0o600); err != nil {
		t.Fatalf("seed search file: %v", err)
	}
	searchResult, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "search",
		Arguments: map[string]interface{}{"query": "needle", "ignoreCase": false},
	})
	if err != nil {
		t.Fatalf("search ExecuteAgentTool: %v", err)
	}
	assertBoundedTerminalAgentObservation(t, "search", searchResult)
}

func assertBoundedTerminalAgentObservation(t *testing.T, operation string, result AgentToolExecutionResult) {
	t.Helper()
	if len(result.Observation) > maxAgentObservationBytes {
		t.Fatalf("%s observation length = %d, want <= %d", operation, len(result.Observation), maxAgentObservationBytes)
	}
	if !utf8.ValidString(result.Observation) {
		t.Fatalf("%s observation is not valid UTF-8", operation)
	}
	if !strings.Contains(result.Observation, "[truncated,") {
		t.Fatalf("%s observation lacks an explicit truncation marker: %q", operation, result.Observation)
	}
	if result.Usage.UnitID == "" || result.Usage.Operation != operation || result.Usage.UnitKind != string(agentcore.UsageUnitTool) ||
		!result.Usage.Success || result.Usage.Pending {
		t.Fatalf("%s terminal usage changed while bounding output: %+v", operation, result.Usage)
	}
}

func TestAgentExecutionCoreWriteConflictBurnsCapabilityAndPreservesDisk(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	path := filepath.Join(root, "conflict.txt")
	if err := os.WriteFile(path, []byte("baseline"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "write",
		Arguments: map[string]interface{}{"path": "conflict.txt", "content": "agent content"},
	})
	if err != nil {
		t.Fatalf("RequestAgentToolCapability: %v", err)
	}
	if err := os.WriteFile(path, []byte("user content"), 0o600); err != nil {
		t.Fatalf("concurrent write: %v", err)
	}
	_, err = agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: "chat-1", CatalogRevision: catalog.Revision,
		ToolID: "write", Arguments: map[string]interface{}{"path": "conflict.txt", "content": "agent content"},
	})
	if err == nil {
		t.Fatal("write succeeded despite baseline conflict")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "user content" {
		t.Fatalf("conflict clobbered disk: %q err=%v", data, readErr)
	}
	if _, retryErr := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: "chat-1", CatalogRevision: catalog.Revision,
		ToolID: "write", Arguments: map[string]interface{}{"path": "conflict.txt", "content": "agent content"},
	}); !errors.Is(retryErr, agentcore.ErrInvalidCapability) {
		t.Fatalf("replay after conflict = %v, want ErrInvalidCapability", retryErr)
	}
}

func TestAgentExecutionCoreCommandCapabilitySharesBudgetAndOldEpochIsInvalid(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	agent.StartNewToolBudgetEpoch(2)
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "plan-1", CatalogRevision: catalog.Revision, ToolID: "run",
		Arguments: map[string]interface{}{"command": "go version"},
	})
	if err != nil {
		t.Fatalf("RequestAgentToolCapability: %v", err)
	}
	if spent := agent.GetToolBudget().Spent; spent != 1 {
		t.Fatalf("budget spent = %d, want 1", spent)
	}
	agent.StartNewToolBudgetEpoch(2)
	_, err = agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: "plan-1", CatalogRevision: catalog.Revision, ToolID: "run",
		Arguments: map[string]interface{}{"command": "go version"},
	})
	if !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("cross-epoch run = %v, want ErrInvalidCapability", err)
	}
}

func TestAgentExecutionCoreRunPersistsReceiptBeforeIrreversibleSideEffect(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	meter := &externalCompletionFailMeter{completeErr: errors.New("terminal usage ledger failed")}
	runtime.SetUsageSink(meter)
	runtime.SetUsageRequirements(true, true)
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	pendingSeen := false
	ctx, capture := withAgentRunCapture(context.Background(), func(command, cwd string) (ExecResult, error) {
		pendingSeen = len(meter.begun) == 1 && meter.begun[0].Pending && meter.begun[0].ExternalReceiptID != "" && meter.begun[0].ExternalCompensation == agentcore.ExternalCompensationPending
		return ExecResult{Command: command, ExitCode: 0}, nil
	})
	result, err := agent.ExecuteAgentTool(ctx, AgentToolExecutionRequest{
		SessionID: "plan-1", CatalogRevision: catalog.Revision, ToolID: "run",
		Arguments: map[string]interface{}{"command": "go version"},
	})
	if !errors.Is(err, agentcore.ErrExternalMutationIrreversible) || !strings.Contains(err.Error(), meter.completeErr.Error()) {
		t.Fatalf("ExecuteAgentTool error = %v, want ledger and irreversible errors", err)
	}
	if !capture.invoked || !pendingSeen {
		t.Fatalf("run capture invoked=%v pending-before-side-effect=%v begun=%+v", capture.invoked, pendingSeen, meter.begun)
	}
	if result.Usage.ExternalReceiptID == "" || result.Usage.ExternalReceiptReversible || result.Usage.ExternalCompensation != agentcore.ExternalCompensationIrreversible || result.Usage.Success {
		t.Fatalf("run external usage = %+v", result.Usage)
	}
	if len(meter.completed) != 0 {
		t.Fatalf("failed terminal usage write was recorded as complete: %+v", meter.completed)
	}
}

func TestAgentExecutionCoreRunRetriesIrreversibleReceiptTerminalWrite(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	firstFailure := errors.New("terminal usage ledger failed once")
	meter := &externalCompletionFailMeter{completeErrors: []error{firstFailure, nil}}
	runtime.SetUsageSink(meter)
	runtime.SetUsageRequirements(true, true)
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	ctx, capture := withAgentRunCapture(context.Background(), func(command, cwd string) (ExecResult, error) {
		return ExecResult{Command: command, ExitCode: 0}, nil
	})
	result, err := agent.ExecuteAgentTool(ctx, AgentToolExecutionRequest{
		SessionID: "plan-1", CatalogRevision: catalog.Revision, ToolID: "run",
		Arguments: map[string]interface{}{"command": "go version"},
	})
	if !errors.Is(err, agentcore.ErrExternalMutationIrreversible) || !strings.Contains(err.Error(), firstFailure.Error()) {
		t.Fatalf("ExecuteAgentTool error = %v, want original ledger and irreversible errors", err)
	}
	if !capture.invoked || meter.completeCalls != 2 || len(meter.completed) != 1 {
		t.Fatalf("capture=%v completeCalls=%d completed=%+v", capture.invoked, meter.completeCalls, meter.completed)
	}
	terminal := meter.completed[0]
	if terminal.Pending || terminal.Success || terminal.ExternalCompensation != agentcore.ExternalCompensationIrreversible || !strings.Contains(terminal.Error, firstFailure.Error()) {
		t.Fatalf("retried terminal usage = %+v", terminal)
	}
	if result.Usage.Success || result.Usage.ExternalCompensation != agentcore.ExternalCompensationIrreversible {
		t.Fatalf("run result usage = %+v", result.Usage)
	}
}

func TestAgentExecutionCorePublicResultRedactsUsagePersistenceDetails(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	privateDetail := filepath.Join(t.TempDir(), "secret-marker-usage-ledger")
	meterErr := errors.Join(ErrUsagePersistencePoisoned, errors.New(privateDetail))
	meter := &externalCompletionFailMeter{completeErr: meterErr}
	runtime.SetUsageSink(meter)
	runtime.SetUsageRequirements(true, true)
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	ctx, capture := withAgentRunCapture(context.Background(), func(command, cwd string) (ExecResult, error) {
		return ExecResult{Command: command, ExitCode: 0}, nil
	})
	result, err := agent.ExecuteAgentTool(ctx, AgentToolExecutionRequest{
		SessionID: "plan-1", CatalogRevision: catalog.Revision, ToolID: "run",
		Arguments: map[string]interface{}{"command": "go version"},
	})
	if !capture.invoked || !errors.Is(err, ErrUsagePersistencePoisoned) {
		t.Fatalf("public execution capture=%v error=%v, want usage persistence poison", capture.invoked, err)
	}
	if strings.Contains(err.Error(), privateDetail) || strings.Contains(err.Error(), "secret-marker") ||
		err.Error() != ErrUsagePersistencePoisoned.Error() {
		t.Fatalf("public execution error leaked persistence detail: %v", err)
	}
	if strings.Contains(result.Usage.Error, privateDetail) || strings.Contains(result.Usage.Error, "secret-marker") ||
		result.Usage.Error != ErrUsagePersistencePoisoned.Error() {
		t.Fatalf("public usage error leaked persistence detail: %q", result.Usage.Error)
	}
}

func TestAgentExecutionCorePublicExecutionEndpointsRemainDenyOnly(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	if _, err := agent.ExecCommand("go version", ""); err == nil {
		t.Fatal("ExecCommand bypassed capability runtime")
	}
	if _, err := agent.CallMCPTool(context.Background(), "mcp.server.tool", nil); err == nil {
		t.Fatal("CallMCPTool bypassed capability runtime")
	}
}

func TestAgentExecutionCoreBackendOwnsSessionLifecycle(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat:renderer-forged", CatalogRevision: catalog.Revision,
		ToolID: "search", Arguments: map[string]interface{}{"query": "needle"},
	}); !errors.Is(err, agentcore.ErrUnknownSession) {
		t.Fatalf("forged session issuance = %v, want ErrUnknownSession", err)
	}
	sessionID, err := agent.createAgentSessionTrusted("chat")
	if err != nil || sessionID == "" {
		t.Fatalf("CreateAgentSession = %q, err=%v", sessionID, err)
	}
	if _, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "search",
		Arguments: map[string]interface{}{"query": "needle"},
	}); err != nil {
		t.Fatalf("backend-owned session execution: %v", err)
	}
	if err := agent.closeAgentSessionTrusted(sessionID); err != nil {
		t.Fatalf("CloseAgentSession: %v", err)
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "search",
		Arguments: map[string]interface{}{"query": "needle"},
	}); !errors.Is(err, agentcore.ErrUnknownSession) {
		t.Fatalf("closed session issuance = %v, want ErrUnknownSession", err)
	}
}

func TestAgentExecutionCoreRendererSessionOwnerProof(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	ctxA := withAgentCallerContext(context.Background(), "wails-window:1:main")
	ctxB := withAgentCallerContext(context.Background(), "wails-window:2:ai")

	if _, err := agent.CreateAgentSessionForCaller(context.Background(), "chat"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("renderer session without caller context = %v, want ErrNotAllowed", err)
	}
	sessionID, err := agent.CreateAgentSessionForCaller(ctxA, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	request := AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "search",
		Arguments: map[string]interface{}{"query": "owner-proof"},
	}
	spentBefore := agent.GetToolBudget().Spent
	if _, err := agent.RequestAgentToolCapability(ctxB, request); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-window capability request = %v, want ErrNotAllowed", err)
	}
	if spent := agent.GetToolBudget().Spent; spent != spentBefore {
		t.Fatalf("cross-window request consumed budget: before=%d after=%d", spentBefore, spent)
	}
	grant, err := agent.RequestAgentToolCapability(ctxA, request)
	if err != nil {
		t.Fatalf("owner capability request: %v", err)
	}
	execution := AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "search", Arguments: request.Arguments,
	}
	if _, err := agent.ExecuteApprovedAgentTool(ctxB, execution); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-window capability execution = %v, want ErrNotAllowed", err)
	}
	if _, err := agent.ExecuteApprovedAgentTool(ctxA, execution); err != nil {
		t.Fatalf("owner capability execution: %v", err)
	}
	if err := agent.CloseAgentSessionForCaller(context.Background(), sessionID); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("renderer session close without caller context = %v, want ErrNotAllowed", err)
	}
	if err := agent.CloseAgentSessionForCaller(ctxB, sessionID); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-window session close = %v, want ErrNotAllowed", err)
	}
	if err := agent.CloseAgentSessionForCaller(ctxA, sessionID); err != nil {
		t.Fatalf("owner session close: %v", err)
	}
}

func TestAgentExecutionCoreTrustedDomainSessionRejectsRendererOwner(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	trustedSession, err := agent.createAgentSessionTrusted("workflow")
	if err != nil {
		t.Fatalf("trusted workflow session: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	ctx := withAgentCallerContext(context.Background(), "wails-window:9:main")
	if _, err := agent.RequestAgentToolCapability(ctx, AgentToolExecutionRequest{
		SessionID: trustedSession, CatalogRevision: catalog.Revision, ToolID: "search",
		Arguments: map[string]interface{}{"query": "domain-owner"},
	}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("renderer access to trusted domain session = %v, want ErrNotAllowed", err)
	}
}

func TestCreateAgentSessionRejectsDomainOwnedKinds(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	for _, kind := range []string{"plan", "goal"} {
		if sessionID, err := agent.createAgentSessionTrusted(kind); !errors.Is(err, ErrNotAllowed) || sessionID != "" {
			t.Fatalf("CreateAgentSession(%q) = %q, %v; want ErrNotAllowed", kind, sessionID, err)
		}
	}
}

func TestInternalAgentCapabilityRequiresTrustedSessionOwner(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	const forged = "workflow:renderer-controlled"
	spentBefore := agent.GetToolBudget().Spent
	if _, err := agent.requestInternalAgentToolCapability(
		context.Background(), forged, "search", map[string]interface{}{"query": "needle"},
	); !errors.Is(err, agentcore.ErrUnknownSession) {
		t.Fatalf("internal capability for unowned session = %v, want ErrUnknownSession", err)
	}
	if runtime.IsSessionRegistered(forged) {
		t.Fatal("internal capability helper registered an unowned session")
	}
	if spent := agent.GetToolBudget().Spent; spent != spentBefore {
		t.Fatalf("unowned session request spent budget: before=%d after=%d", spentBefore, spent)
	}
}

func TestAgentExecutionCoreMCPToolUsesCatalogAndSameCapabilityPipeline(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	workspace := agent.workspaceContext
	transport := newScriptedMCPTransport(scriptedMCPInitializeHandler(mcpTestToolsCapability, func(request *jsonrpcOutboundMessage, _ int) *jsonrpcResponse {
		var result string
		switch request.Method {
		case "tools/list":
			result = `{"tools":[{"name":"echo.tool","description":"Echo nested payload","inputSchema":{"type":"object","properties":{"payload":{"type":"object","description":"nested","properties":{"text":{"type":"string"}},"required":["text"]}},"required":["payload"]}}]}`
		case "tools/call":
			result = `{"content":[{"type":"text","text":"mcp-ok"}]}`
		default:
			return &jsonrpcResponse{JSONRPC: "2.0", ID: request.ID, Error: &jsonrpcError{Code: -32601, Message: "unexpected"}}
		}
		return &jsonrpcResponse{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(result)}
	}))
	client := newScriptedMCPClient("server.with.dot", transport)
	initializeScriptedMCPClient(t, client)
	t.Cleanup(func() { _ = client.StopServer() })
	mcp := newTestMCPService(t)
	mcp.workspaceContext = workspace
	mcp.rootDir = root
	mcp.config.Servers = []MCPServerConfig{{Name: "server.with.dot", Transport: "stdio", Enabled: true}}
	mcp.clients["server.with.dot"] = client
	approvals := 0
	mcp.approveTool = func(server, tool, args string, risk RiskLevel) bool {
		approvals++
		return server == "server.with.dot" && tool == "echo.tool" && strings.Contains(args, "hello") && risk == RiskElevated
	}
	if err := WireAgentExecutionCore(agent, file, search, mcp, nil, nil); err != nil {
		t.Fatalf("WireAgentExecutionCore MCP: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var mcpTool *AgentToolDefinition
	for index := range catalog.Tools {
		if catalog.Tools[index].ID == "mcp.server.with.dot.echo.tool" {
			mcpTool = &catalog.Tools[index]
			break
		}
	}
	if mcpTool == nil {
		t.Fatalf("MCP tool missing from catalog: %+v", catalog.Tools)
	}
	if strings.Contains(mcpTool.WireName, ".") || mcpTool.Source != string(agentcore.SourceMCP) || mcpTool.Mutation != string(agentcore.MutationExternal) {
		t.Fatalf("MCP ToolDef = %+v", mcpTool)
	}
	payload := map[string]interface{}{"payload": map[string]interface{}{"text": "hello"}}
	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow-1", CatalogRevision: catalog.Revision,
		ToolID: mcpTool.ID, Arguments: payload,
	})
	if err != nil {
		t.Fatalf("ExecuteAgentTool MCP: %v", err)
	}
	if !strings.Contains(result.Observation, "mcp-ok") || approvals != 1 {
		t.Fatalf("MCP result=%+v approvals=%d", result, approvals)
	}
	if len(mcp.approvals) != 0 {
		t.Fatalf("parallel MCP approval map was used: %+v", mcp.approvals)
	}
}

func TestAgentExecutionCoreMCPInvalidSchemaRemovesSourceAtomically(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	transport := newScriptedMCPTransport(scriptedMCPInitializeHandler(mcpTestToolsCapability, func(request *jsonrpcOutboundMessage, _ int) *jsonrpcResponse {
		return &jsonrpcResponse{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(
			`{"tools":[{"name":"unsafe","description":"open args","inputSchema":{"type":"object","additionalProperties":true}}]}`,
		)}
	}))
	client := newScriptedMCPClient("unsafe-server", transport)
	initializeScriptedMCPClient(t, client)
	t.Cleanup(func() { _ = client.StopServer() })
	mcp := newTestMCPService(t)
	mcp.workspaceContext = agent.workspaceContext
	mcp.rootDir = root
	mcp.config.Servers = []MCPServerConfig{{Name: "unsafe-server", Transport: "stdio", Enabled: true}}
	mcp.clients["unsafe-server"] = client
	mcp.approveTool = func(string, string, string, RiskLevel) bool { return true }
	if err := WireAgentExecutionCore(agent, file, search, mcp, nil, nil); err == nil {
		t.Fatal("invalid MCP schema was accepted during wiring")
	}
	if _, err := agent.GetAgentToolCatalog(context.Background()); err == nil {
		t.Fatal("invalid MCP schema was accepted during catalog refresh")
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	for _, tool := range runtime.Registry().Snapshot().Tools {
		if tool.Source == agentcore.SourceMCP {
			t.Fatalf("invalid refresh left stale MCP tool exposed: %+v", tool)
		}
	}
}
