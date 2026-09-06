package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func TestPlanToolEmptyWithoutProvider(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	var plan *AgentToolDefinition
	for i := range catalog.Tools {
		if catalog.Tools[i].ID == "plan" {
			plan = &catalog.Tools[i]
			break
		}
	}
	if plan == nil {
		t.Fatal("plan missing from catalog")
	}
	if plan.InputSchema["additionalProperties"] != false {
		t.Fatalf("plan schema additionalProperties = %#v", plan.InputSchema["additionalProperties"])
	}
	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "plan",
		Arguments: map[string]interface{}{"goal": "document the gap"},
	})
	if err != nil {
		t.Fatalf("plan tool: %v", err)
	}
	if !strings.Contains(result.Observation, `"steps":[]`) && !strings.Contains(result.Observation, `"steps": []`) && !strings.Contains(result.Observation, `"steps":null`) {
		t.Fatalf("empty plan observation = %q", result.Observation)
	}
	if result.Metadata["reason"] == "" && !strings.Contains(result.Observation, "no provider") {
		t.Fatalf("empty plan must explain why: %q meta=%v", result.Observation, result.Metadata)
	}
}

func TestPlanToolLoopbackProviderKeepsCatalogTools(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	payload, err := json.Marshal(map[string]interface{}{
		"steps": []catalogPlanStep{
			{Title: "Read README", Description: "inspect", Tool: "read", Args: `{"path":"README.md"}`},
			{Title: "Search", Description: "find docs", Tool: "search", Args: `{"query":"gap"}`},
			{Title: "Git", Description: "status", Tool: "git.status", Args: "{}"},
			{Title: "Invented", Description: "no", Tool: "not-a-tool", Args: "{}"},
		},
	})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]string{"content": string(payload)},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 4, "completion_tokens": 8},
		})
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := WireAgentExecutionAI(agent, ai); err != nil {
		t.Fatalf("WireAgentExecutionAI: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "plan",
		Arguments: map[string]interface{}{"goal": "document the gap"},
	})
	if err != nil {
		t.Fatalf("plan tool: %v", err)
	}
	steps := parsePlanSteps(result.Observation)
	if len(steps) < 3 {
		t.Fatalf("want >=3 catalog steps, got %#v from %q", steps, result.Observation)
	}
	ids := map[string]struct{}{}
	for _, tool := range catalog.Tools {
		ids[tool.ID] = struct{}{}
	}
	for _, step := range steps {
		if _, ok := ids[step.Tool]; !ok {
			t.Fatalf("invented tool leaked: %#v", step)
		}
	}
}

func TestDefaultGoalExecutorRequiresOptInThenRunsRead(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello goal"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := agent.registerAgentSession(agent.executionSessionID(agentcore.SessionGoal, "g-read")); err != nil {
		t.Fatalf("register goal session: %v", err)
	}
	prev := generateCatalogPlanHook
	generateCatalogPlanHook = func(_ context.Context, _ *AgentService, _, _ string, _ []string) ([]catalogPlanStep, string, error) {
		return []catalogPlanStep{{
			Title: "Read README", Description: "inspect", Tool: "read", Args: `{"path":"README.md"}`,
		}}, "", nil
	}
	t.Cleanup(func() { generateCatalogPlanHook = prev })

	exec := NewDefaultGoalExecutor(agent, root)
	svc := newTestGoalService(t)
	svc.setInternalExecutor(exec, nil)
	if _, err := svc.CreateGoal("g-read", "read the readme", "hello goal", 2, 1.0, time.Minute, true); err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	if err := svc.RunGoal("g-read", nil, nil); !errors.Is(err, ErrGoalPrototypeDisabled) {
		t.Fatalf("default LLM executor without opt-in = %v, want ErrGoalPrototypeDisabled", err)
	}
	svc.SetPrototypeExecutionEnabled(true)
	if err := svc.RunGoal("g-read", nil, nil); err != nil {
		t.Fatalf("opt-in RunGoal: %v", err)
	}
}

func TestGoalRejectedWriteDoesNotRetryOverwrite(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := agent.registerAgentSession(agent.executionSessionID(agentcore.SessionGoal, "g-write")); err != nil {
		t.Fatalf("register: %v", err)
	}
	agent.approveWrite = func(string, int64) bool { return false }
	prev := generateCatalogPlanHook
	generateCatalogPlanHook = func(_ context.Context, _ *AgentService, _, _ string, _ []string) ([]catalogPlanStep, string, error) {
		return []catalogPlanStep{{
			Title: "Write note", Description: "overwrite", Tool: "write",
			Args: `{"path":"note.txt","content":"hacked"}`,
		}}, "", nil
	}
	t.Cleanup(func() { generateCatalogPlanHook = prev })
	exec := NewDefaultGoalExecutor(agent, root)
	svc := newTestGoalService(t)
	svc.setInternalExecutor(exec, nil)
	svc.SetPrototypeExecutionEnabled(true)
	if _, err := svc.CreateGoal("g-write", "change note", "hacked", 3, 1.0, time.Minute, true); err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	_ = svc.RunGoal("g-write", nil, nil)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "original" {
		t.Fatalf("rejected write mutated disk: %q", data)
	}
}
