//go:build windows

package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func TestAgentExecutionWorkflowGitStatusRejectsWorkspaceJunctionSwap(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("create outside: %v", err)
	}

	workspaceContext := NewWorkspaceContext()
	if err := workspaceContext.Set(workspace); err != nil {
		t.Fatalf("workspace.Set: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspaceContext)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(workspace); err != nil {
		t.Fatalf("configure workspace root: %v", err)
	}
	if err := agent.registerAgentSession("workflow:verify"); err != nil {
		t.Fatalf("register workflow session: %v", err)
	}
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	file := NewFileServiceWithWorkspaceContext(workspaceContext)
	if err := file.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("file.setWorkspaceRoot: %v", err)
	}
	search := NewSearchService()
	search.setWorkspaceContext(workspaceContext)
	if err := search.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("search.setWorkspaceRoot: %v", err)
	}
	gitService := NewGitService()
	if err := gitService.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("git.setWorkspaceRoot: %v", err)
	}
	outsideGit := NewGitService()
	if err := outsideGit.setWorkspaceRoot(outside); err != nil {
		t.Fatalf("outside git.setWorkspaceRoot: %v", err)
	}
	if err := outsideGit.InitRepo(outside); err != nil {
		t.Fatalf("outside Git InitRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside-secret.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatalf("seed outside status: %v", err)
	}
	if err := WireAgentExecutionCore(agent, file, search, nil, nil, nil, gitService); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	if err := workflow.CreateWorkflow(workspace, "git-junction", &WorkflowDef{
		Name:  "git-junction",
		Steps: []WorkflowStep{{Name: "status", Type: WorkflowStepGit, Tool: "status", Input: map[string]interface{}{}}},
	}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var tool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["workflow"] == "git-junction" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("Git status ToolDef missing: %+v", catalog.Tools)
	}

	detached := workspace + "-detached"
	if err := os.Rename(workspace, detached); err != nil {
		t.Fatalf("detach workspace: %v", err)
	}
	createWindowsJunction(t, workspace, outside)

	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: tool.ID,
		Arguments: map[string]interface{}{},
	})
	if err == nil || !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("Git status succeeded after workspace junction swap: result=%+v err=%v", result, err)
	}
	if result.Observation != "" || result.Metadata != nil {
		t.Fatalf("rejected Git status exposed outside observation: %+v", result)
	}
}

func TestAgentExecutionWorkflowCatalogRejectsWorkflowDirectoryJunction(t *testing.T) {
	agent, _, _, workspace := newExecutionCoreTestServices(t)
	outside := t.TempDir()
	workflowParent := filepath.Join(workspace, ".koyori-ide")
	if err := os.MkdirAll(workflowParent, 0o700); err != nil {
		t.Fatalf("create workflow parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside.yaml"), []byte(
		"name: outside\nsteps:\n  - name: escape\n    type: command\n    command: cmd\n    args: [/c, echo, outside]\n",
	), 0o600); err != nil {
		t.Fatalf("write outside workflow: %v", err)
	}
	createWindowsJunction(t, filepath.Join(workflowParent, "workflows"), outside)

	err := WireAgentWorkflowTools(agent, NewWorkflowService())
	if err == nil || !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("workflow junction wiring error = %v, want ErrNotAllowed", err)
	}
	runtime, runtimeErr := agent.coreRuntime()
	if runtimeErr != nil {
		t.Fatalf("coreRuntime: %v", runtimeErr)
	}
	for _, tool := range runtime.Registry().Snapshot().Tools {
		if tool.Source == agentcore.SourceWorkflow {
			t.Fatalf("outside workflow survived failed refresh: %+v", tool)
		}
	}
}

func TestAgentExecutionWorkflowCatalogRejectsDirectorySwapAfterEnumeration(t *testing.T) {
	agent, _, _, workspace := newExecutionCoreTestServices(t)
	outside := t.TempDir()
	workflowParent := filepath.Join(workspace, ".koyori-ide")
	workflowDir := filepath.Join(workflowParent, "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "inside.yaml"), []byte(
		"name: inside\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [version]\n",
	), 0o600); err != nil {
		t.Fatalf("write inside workflow: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside.yaml"), []byte(
		"name: outside\nsteps:\n  - name: escape\n    type: command\n    command: cmd\n    args: [/c, echo, outside]\n",
	), 0o600); err != nil {
		t.Fatalf("write outside workflow: %v", err)
	}
	workflow := NewWorkflowService()
	swapped := false
	workflow.setAgentWorkflowLoadHook(func(stage, relativePath string) error {
		if stage != agentWorkflowLoadAfterReadDir || swapped || relativePath != agentWorkflowDirectory {
			return nil
		}
		swapped = true
		detached := workflowDir + "-detached"
		if err := os.Rename(workflowDir, detached); err != nil {
			return err
		}
		createWindowsJunction(t, workflowDir, outside)
		return nil
	})
	err := WireAgentWorkflowTools(agent, workflow)
	if err == nil || !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("directory swap wiring error = %v, want ErrNotAllowed", err)
	}
	runtime, runtimeErr := agent.coreRuntime()
	if runtimeErr != nil {
		t.Fatalf("coreRuntime: %v", runtimeErr)
	}
	for _, tool := range runtime.Registry().Snapshot().Tools {
		if tool.Source == agentcore.SourceWorkflow {
			t.Fatalf("workflow source survived directory swap: %+v", tool)
		}
	}
}
