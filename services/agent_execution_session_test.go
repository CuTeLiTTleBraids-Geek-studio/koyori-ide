package services

import (
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func TestExecutionSessionIDResolvesOpaquePlanOwner(t *testing.T) {
	root := t.TempDir()
	workspace := NewWorkspaceContext()
	if err := workspace.Set(root); err != nil {
		t.Fatal(err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	file := NewFileServiceWithWorkspaceContext(workspace)
	if err := file.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	search := NewSearchService()
	search.setWorkspaceContext(workspace)
	if err := search.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	if err := WireAgentExecutionCore(agent, file, search, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	plan := NewAIPlanService()
	if _, err := WireAgentLifecycle(agent, NewAIService(), plan, NewAIGoalService(), NewAIPermissionService(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.CreatePlan("probe", "probe", nil); err != nil {
		t.Fatal(err)
	}
	logical := lifecycleSessionID(agentcore.SessionPlan, "probe")
	resolved := agent.executionSessionID(agentcore.SessionPlan, logical)
	if resolved == logical {
		t.Fatalf("plan execution kept logical ID %q; want opaque runtime owner", logical)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.IsSessionRegistered(resolved) {
		t.Fatalf("resolved runtime session %q is not registered", resolved)
	}
}

func TestDefaultStepExecutorUsesOpaquePlanOwner(t *testing.T) {
	root := t.TempDir()
	workspace := NewWorkspaceContext()
	if err := workspace.Set(root); err != nil {
		t.Fatal(err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	file := NewFileServiceWithWorkspaceContext(workspace)
	if err := file.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	search := NewSearchService()
	search.setWorkspaceContext(workspace)
	if err := search.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	if err := WireAgentExecutionCore(agent, file, search, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	plan := NewAIPlanService()
	if _, err := WireAgentLifecycle(agent, NewAIService(), plan, NewAIGoalService(), NewAIPermissionService(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.CreatePlan("probe", "probe", []PlanStep{{Tool: "command", Args: `{"command":"go version"}`}}); err != nil {
		t.Fatal(err)
	}
	if err := plan.ApproveStep("probe", 0); err != nil {
		t.Fatal(err)
	}
	if err := plan.ExecuteStep("probe", 0, NewDefaultStepExecutorWithContext(agent, workspace)); err != nil {
		t.Fatalf("ExecuteStep: %v", err)
	}
	result, err := plan.GetPlan("probe")
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps[0].Status != PlanStepCompleted || result.Status != PlanStatusCompleted {
		t.Fatalf("plan after execution = %+v", result)
	}
}
