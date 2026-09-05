package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func TestTaskServiceCommandApprovalRequiresUnifiedCore(t *testing.T) {
	agent := &AgentService{}
	if err := agent.configureWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatalf("configureWorkspaceRoot: %v", err)
	}
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }

	service := NewTaskService(agent)
	if _, err := service.RequestExecutionApproval(trustedTaskContext(), "no-core", "go version", ""); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("RequestExecutionApproval error = %v, want ErrNotAllowed", err)
	}
}

func TestTaskServiceCommandUsesUnifiedCapabilityBudgetAndAudit(t *testing.T) {
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
	var audit bytes.Buffer
	agent.auditLogger = slog.New(slog.NewTextHandler(&audit, nil))

	service := NewTaskService(agent)
	token, err := service.RequestExecutionApproval(trustedTaskContext(), "core-pipeline", "go version", "")
	if err != nil {
		t.Fatalf("RequestExecutionApproval: %v", err)
	}
	if token == "" {
		t.Fatal("RequestExecutionApproval returned an empty capability")
	}
	if spent := agent.GetToolBudget().Spent; spent != 1 {
		t.Fatalf("tool budget spent = %d, want 1", spent)
	}

	result, err := service.ExecuteApproved(trustedTaskContext(), "core-pipeline", "go version", "", token)
	if err != nil {
		t.Fatalf("ExecuteApproved: %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "go version") {
		t.Fatalf("unexpected task result: %+v", result)
	}

	logOutput := audit.String()
	for _, expected := range []string{
		"msg=\"agent execution core\"",
		"stage=capability-issued",
		"stage=execution-completed",
		"toolId=run",
	} {
		if !strings.Contains(logOutput, expected) {
			t.Errorf("unified audit missing %q:\n%s", expected, logOutput)
		}
	}
}

func TestTaskServiceDoesNotTurnRendererExecutionIDIntoAgentAuthority(t *testing.T) {
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
	var audit bytes.Buffer
	agent.auditLogger = slog.New(slog.NewTextHandler(&audit, nil))
	service := NewTaskService(agent)
	const rendererID = "renderer-controlled"
	token, err := service.RequestExecutionApproval(trustedTaskContext(), rendererID, "go version", "")
	if err != nil {
		t.Fatalf("RequestExecutionApproval: %v", err)
	}
	service.mu.Lock()
	approval := service.approvals[token]
	service.mu.Unlock()
	if approval.executionID != rendererID || !approval.ownsSession {
		t.Fatalf("task approval ownership = %+v", approval)
	}
	if !strings.HasPrefix(approval.sessionID, "workflow:") || strings.Contains(approval.sessionID, rendererID) {
		t.Fatalf("task session ID %q is not backend-random", approval.sessionID)
	}
	coreRuntime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if !coreRuntime.IsSessionActive(approval.sessionID) {
		t.Fatal("backend-owned task session was not active during approval")
	}
	if strings.Contains(audit.String(), "sessionId=task/"+rendererID) {
		t.Fatalf("renderer execution ID became an authority-bearing session:\n%s", audit.String())
	}
	if _, err := service.ExecuteApproved(trustedTaskContext(), rendererID, "go version", "", token); err != nil {
		t.Fatalf("ExecuteApproved: %v", err)
	}
	if coreRuntime.IsSessionRegistered(approval.sessionID) {
		t.Fatal("consumed task approval retained its owned runtime session")
	}
}

func TestTaskServiceCannotReauthorizeCompletedWorkflowSession(t *testing.T) {
	agent := newTaskTestAgent(t)
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	task := NewTaskService(agent)
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
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "completed")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	if err := task.CompleteWorkflowExecution(trustedTaskContext(), sessionID); err != nil {
		t.Fatalf("CompleteWorkflowExecution: %v", err)
	}
	if _, err := task.RequestExecutionApproval(trustedTaskContext(), sessionID, "go version", ""); err == nil {
		t.Fatal("completed renderer-supplied workflow ID was reauthorized")
	}
	coreRuntime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if coreRuntime.IsSessionRegistered(sessionID) {
		t.Fatal("completed workflow was re-registered by task approval")
	}
}

func TestTaskServiceRejectsUnownedAndNonRunningWorkflowSessions(t *testing.T) {
	agent := newTaskTestAgent(t)
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	task := NewTaskService(agent)
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
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	coreRuntime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}

	const forged = "workflow:renderer-forged"
	if _, err := task.RequestExecutionApproval(trustedTaskContext(), forged, "go version", ""); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("forged workflow approval error = %v, want ErrNotAllowed", err)
	}
	if coreRuntime.IsSessionRegistered(forged) {
		t.Fatal("forged workflow ID became a registered runtime session")
	}

	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "paused")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	if err := task.FailWorkflowExecution(trustedTaskContext(), sessionID, "pause before retry"); err != nil {
		t.Fatalf("FailWorkflowExecution: %v", err)
	}
	if _, err := task.RequestExecutionApproval(trustedTaskContext(), sessionID, "go version", ""); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("failed workflow approval error = %v, want ErrNotAllowed", err)
	}
	if !coreRuntime.IsSessionRegistered(sessionID) || coreRuntime.IsSessionActive(sessionID) {
		t.Fatalf("failed workflow runtime registered=%v active=%v", coreRuntime.IsSessionRegistered(sessionID), coreRuntime.IsSessionActive(sessionID))
	}
}

func TestTaskServiceRunningWorkflowApprovalKeepsLifecycleSession(t *testing.T) {
	agent := newTaskTestAgent(t)
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	task := NewTaskService(agent)
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
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "running")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	token, err := task.RequestExecutionApproval(trustedTaskContext(), sessionID, "go version", "")
	if err != nil {
		t.Fatalf("RequestExecutionApproval: %v", err)
	}
	task.mu.Lock()
	approval := task.approvals[token]
	task.mu.Unlock()
	if approval.executionID != sessionID || approval.sessionID != sessionID || approval.ownsSession {
		t.Fatalf("workflow approval ownership = %+v", approval)
	}
	if _, err := task.ExecuteApproved(trustedTaskContext(), sessionID, "go version", "", token); err != nil {
		t.Fatalf("ExecuteApproved: %v", err)
	}
	coreRuntime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if !coreRuntime.IsSessionActive(sessionID) {
		t.Fatal("workflow step consumption closed its lifecycle-owned session")
	}
	if err := task.CompleteWorkflowExecution(trustedTaskContext(), sessionID); err != nil {
		t.Fatalf("CompleteWorkflowExecution: %v", err)
	}
}

func TestTaskServiceWorkflowStepUsesAuthoritativeCatalogTool(t *testing.T) {
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
	file := NewFileServiceWithWorkspaceContext(workspace)
	if err := file.setWorkspaceRoot(root); err != nil {
		t.Fatalf("file.setWorkspaceRoot: %v", err)
	}
	if err := WireAgentExecutionCore(agent, file, nil, nil, nil, nil); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	if err := workflow.CreateWorkflow(root, "catalog-run", &WorkflowDef{
		Name: "catalog-run",
		Steps: []WorkflowStep{{
			Name: "go-version", Type: WorkflowStepCommand, Command: "go", Args: []string{"version"},
		}},
	}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	task := NewTaskService(agent)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	ownerCtx := withAgentCallerContext(context.Background(), "workflow-owner")
	otherCtx := withAgentCallerContext(context.Background(), "workflow-other")
	sessionID, err := task.BeginWorkflowExecution(ownerCtx, "catalog-run")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	if _, err := task.RequestWorkflowStepApproval(otherCtx, sessionID, "catalog-run", "go-version"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-window approval request error = %v, want ErrNotAllowed", err)
	}
	if _, err := task.RequestWorkflowStepApproval(ownerCtx, sessionID, "other-workflow", "go-version"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-workflow approval error = %v, want ErrNotAllowed", err)
	}
	token, err := task.RequestWorkflowStepApproval(ownerCtx, sessionID, "catalog-run", "go-version")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval: %v", err)
	}
	task.mu.Lock()
	approval := task.approvals[token]
	task.mu.Unlock()
	if approval.capability.ToolID == "run" || !strings.HasPrefix(approval.capability.ToolID, "workflow.") {
		t.Fatalf("workflow step capability used %q, want authoritative dynamic ToolDef", approval.capability.ToolID)
	}
	if _, err := task.ExecuteApprovedWorkflowStep(otherCtx, sessionID, "catalog-run", "go-version", token); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-window approval execution error = %v, want ErrNotAllowed", err)
	}
	task.mu.Lock()
	_, tokenStillPending := task.approvals[token]
	task.mu.Unlock()
	if !tokenStillPending {
		t.Fatal("cross-window approval attempt consumed the owner token")
	}
	result, err := task.ExecuteApprovedWorkflowStep(ownerCtx, sessionID, "catalog-run", "go-version", token)
	if err != nil {
		t.Fatalf("ExecuteApprovedWorkflowStep: %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "go version") {
		t.Fatalf("workflow step result = %+v", result)
	}
	if err := task.CompleteWorkflowExecution(ownerCtx, sessionID); err != nil {
		t.Fatalf("CompleteWorkflowExecution: %v", err)
	}
}

func TestTaskServiceWorkflowFileReadReturnsObservationWithoutCommandRunner(t *testing.T) {
	agent, file, _, root := newExecutionCoreTestServices(t)
	if err := file.WriteFile(filepath.Join(root, "notes.txt"), "task bridge content"); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	if err := workflow.CreateWorkflow(root, "file-read", &WorkflowDef{
		Name: "file-read",
		Steps: []WorkflowStep{{
			Name: "notes", Type: WorkflowStepFile, Tool: "read",
			Input: map[string]interface{}{"path": "notes.txt"},
		}},
	}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	task := NewTaskService(agent)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "file-read")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}

	readCalls := 0
	file.rootOperationHook = func(operation string) error {
		if operation == "ReadFile" {
			readCalls++
		}
		return nil
	}
	misusedToken, err := task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, "file-read", "notes")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval for replay probe: %v", err)
	}
	if _, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, "file-read", "other-step", misusedToken); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-step replay error = %v, want ErrInvalidInput", err)
	}
	if _, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, "file-read", "notes", misusedToken); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("replayed burned capability error = %v, want ErrInvalidInput", err)
	}
	if readCalls != 0 {
		t.Fatalf("cross-step replay reached FileService %d times", readCalls)
	}

	token, err := task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, "file-read", "notes")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval: %v", err)
	}
	task.mu.Lock()
	approval := task.approvals[token]
	task.mu.Unlock()
	if approval.adapter != workflowAdapterFileRead || approval.arguments["path"] != "notes.txt" || approval.command != "" || approval.cwd != "" {
		t.Fatalf("file workflow approval = %+v", approval)
	}
	result, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, "file-read", "notes", token)
	if err != nil {
		t.Fatalf("ExecuteApprovedWorkflowStep: %v", err)
	}
	if result.ExitCode != 0 || result.Blocked || !strings.Contains(result.Stdout, "task bridge content") || readCalls != 1 {
		t.Fatalf("file workflow bridge result = %+v, readCalls=%d", result, readCalls)
	}
	if err := task.CompleteWorkflowExecution(trustedTaskContext(), sessionID); err != nil {
		t.Fatalf("CompleteWorkflowExecution: %v", err)
	}
}

func TestTaskServiceWorkflowFileWriteUsesWorkspaceTransactionWithoutCommandRunner(t *testing.T) {
	agent, file, _, root := newExecutionCoreTestServices(t)
	target := filepath.Join(root, "notes.txt")
	if err := file.WriteFile(target, "before"); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	if err := workflow.CreateWorkflow(root, "file-write", &WorkflowDef{
		Name: "file-write",
		Steps: []WorkflowStep{{
			Name: "notes", Type: WorkflowStepFile, Tool: "write",
			Input: map[string]interface{}{"path": "notes.txt", "content": "task-owned content"},
		}},
	}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	task := NewTaskService(agent)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "file-write")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	token, err := task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, "file-write", "notes")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval: %v", err)
	}
	task.mu.Lock()
	approval := task.approvals[token]
	task.mu.Unlock()
	if approval.adapter != workflowAdapterFileWrite || len(approval.arguments) != 0 || approval.command != "" || approval.cwd != "" {
		t.Fatalf("file write workflow approval = %+v", approval)
	}
	result, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, "file-write", "notes", token)
	if err != nil {
		t.Fatalf("ExecuteApprovedWorkflowStep: %v", err)
	}
	if result.ExitCode != 0 || result.Blocked || !strings.Contains(result.Stdout, "Wrote notes.txt") {
		t.Fatalf("file write workflow bridge result = %+v", result)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "task-owned content" {
		t.Fatalf("disk after task file write = %q, err=%v", data, readErr)
	}
	if err := task.CompleteWorkflowExecution(trustedTaskContext(), sessionID); err != nil {
		t.Fatalf("CompleteWorkflowExecution: %v", err)
	}
}

func TestTaskServiceWorkflowMCPApprovalBindsWorkflowAndTool(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	var toolAvailable atomic.Bool
	toolAvailable.Store(true)
	toolCalls := 0
	transport := newScriptedMCPTransport(scriptedMCPInitializeHandler(mcpTestToolsCapability, func(request *jsonrpcOutboundMessage, _ int) *jsonrpcResponse {
		var result string
		switch request.Method {
		case "tools/list":
			if toolAvailable.Load() {
				result = `{"tools":[{"name":"lookup","description":"Lookup docs","inputSchema":{"type":"object","properties":{"query":{"type":"string","minLength":1}},"required":["query"]}}]}`
			} else {
				result = `{"tools":[]}`
			}
		case "tools/call":
			toolCalls++
			result = `{"content":[{"type":"text","text":"task-mcp-ok"}]}`
		default:
			return &jsonrpcResponse{JSONRPC: "2.0", ID: request.ID, Error: &jsonrpcError{Code: -32601, Message: "unexpected"}}
		}
		return &jsonrpcResponse{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(result)}
	}))
	client := newScriptedMCPClient("docs", transport)
	initializeScriptedMCPClient(t, client)
	t.Cleanup(func() { _ = client.StopServer() })
	mcp := newTestMCPService(t)
	mcp.workspaceContext = agent.workspaceContext
	mcp.rootDir = root
	mcp.config.Servers = []MCPServerConfig{{Name: "docs", Transport: "stdio", Enabled: true}}
	mcp.clients["docs"] = client
	approvals := 0
	mcp.approveTool = func(server, tool, args string, risk RiskLevel) bool {
		approvals++
		return server == "docs" && tool == "lookup" && strings.Contains(args, "query") && risk == RiskElevated
	}
	if err := WireAgentExecutionCore(agent, file, search, mcp, nil, nil); err != nil {
		t.Fatalf("WireAgentExecutionCore MCP: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	definition := &WorkflowDef{
		Name: "mcp-task",
		Steps: []WorkflowStep{
			{Name: "lookup", Type: WorkflowStepMCP, Tool: "mcp.docs.lookup", Input: map[string]interface{}{"query": "catalog-query"}},
			{Name: "lookup-again", Type: WorkflowStepMCP, Tool: "mcp.docs.lookup", Input: map[string]interface{}{"query": "catalog-query"}},
		},
	}
	if err := workflow.CreateWorkflow(root, definition.Name, definition); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	task := NewTaskService(agent)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), definition.Name)
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	defer func() { _ = task.FailWorkflowExecution(trustedTaskContext(), sessionID, "test cleanup") }()

	token, err := task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, definition.Name, "lookup")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval: %v", err)
	}
	task.mu.Lock()
	approval := task.approvals[token]
	task.mu.Unlock()
	if approval.adapter != workflowAdapterMCP || approval.command != "" || approval.cwd != "" || approval.arguments["query"] != "catalog-query" {
		t.Fatalf("MCP workflow approval was not catalog-owned: %+v", approval)
	}
	if _, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, definition.Name, "lookup-again", token); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-step MCP token replay error = %v, want ErrInvalidInput", err)
	}
	if toolCalls != 0 {
		t.Fatalf("cross-step MCP replay reached server: %d calls", toolCalls)
	}

	token, err = task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, definition.Name, "lookup")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval after replay: %v", err)
	}
	definition.Steps[0].Input["query"] = "changed-after-approval"
	if err := workflow.SaveWorkflow(root, definition.Name, definition); err != nil {
		t.Fatalf("SaveWorkflow: %v", err)
	}
	if _, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, definition.Name, "lookup", token); !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("stale MCP workflow capability error = %v, want ErrInvalidCapability", err)
	}
	if toolCalls != 0 {
		t.Fatalf("stale MCP workflow capability reached server: %d calls", toolCalls)
	}

	token, err = task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, definition.Name, "lookup-again")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval after workflow refresh: %v", err)
	}
	result, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, definition.Name, "lookup-again", token)
	if err != nil {
		t.Fatalf("ExecuteApprovedWorkflowStep MCP: %v", err)
	}
	if result.ExitCode != 0 || result.Blocked || !strings.Contains(result.Stdout, "task-mcp-ok") || toolCalls != 1 || approvals < 3 {
		t.Fatalf("MCP workflow result=%+v calls=%d approvals=%d", result, toolCalls, approvals)
	}

	token, err = task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, definition.Name, "lookup")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval before MCP catalog removal: %v", err)
	}
	// The server changes its catalog without sending a list_changed
	// notification. Execution-time validation must bypass the ordinary 30s
	// ListTools cache and observe the current server state.
	toolAvailable.Store(false)
	if _, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, definition.Name, "lookup", token); err == nil {
		t.Fatal("workflow MCP execution succeeded after delegated tool disappeared")
	}
	if toolCalls != 1 {
		t.Fatalf("missing MCP tool reached server: %d calls", toolCalls)
	}
}

func TestTaskServiceWorkflowGitStatusReturnsObservationWithoutCommandRunner(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	git := NewGitService()
	if err := git.setWorkspaceRoot(root); err != nil {
		t.Fatalf("git.setWorkspaceRoot: %v", err)
	}
	if err := git.InitRepo(root); err != nil {
		t.Fatalf("Git InitRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "task-status.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatalf("seed Git status: %v", err)
	}
	if err := WireAgentExecutionCore(agent, file, search, nil, nil, nil, git); err != nil {
		t.Fatalf("WireAgentExecutionCore Git: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	if err := workflow.CreateWorkflow(root, "git-task", &WorkflowDef{
		Name:  "git-task",
		Steps: []WorkflowStep{{Name: "status", Type: WorkflowStepGit, Tool: "status", Input: map[string]interface{}{}}},
	}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	task := NewTaskService(agent)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "git-task")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	token, err := task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, "git-task", "status")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval: %v", err)
	}
	task.mu.Lock()
	approval := task.approvals[token]
	task.mu.Unlock()
	if approval.adapter != workflowAdapterGitStatus || approval.command != "" || approval.cwd != "" || len(approval.arguments) != 0 {
		t.Fatalf("Git workflow approval was not catalog-owned: %+v", approval)
	}
	result, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, "git-task", "status", token)
	if err != nil {
		t.Fatalf("ExecuteApprovedWorkflowStep Git: %v", err)
	}
	if result.ExitCode != 0 || result.Blocked || !strings.Contains(result.Stdout, "task-status.txt") {
		t.Fatalf("Git workflow TaskService result = %+v", result)
	}
	if err := task.CompleteWorkflowExecution(trustedTaskContext(), sessionID); err != nil {
		t.Fatalf("CompleteWorkflowExecution: %v", err)
	}
}

func TestTaskServiceWorkflowSkillActivationUsesCatalogAndFailsClosedOnReplay(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	skills := &SkillsService{skills: []Skill{{
		ID: "review", Name: "Review", Description: "Review the current change",
		Scope: SkillScopeProject, AllowedTools: []string{"read", "search"},
	}}}
	if err := WireAgentExecutionCore(agent, file, search, nil, skills, nil); err != nil {
		t.Fatalf("WireAgentExecutionCore Skill: %v", err)
	}
	approvalCalls := 0
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.approveSkill = func(skill Skill) bool {
		approvalCalls++
		return skill.ID == "review" && skill.Scope == SkillScopeProject
	}
	deps.mu.Unlock()
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	definition := &WorkflowDef{
		Name: "skill-task",
		Steps: []WorkflowStep{
			{Name: "review", Type: WorkflowStepSkill, Tool: "activate", Input: map[string]interface{}{"id": "review"}},
			{Name: "review-again", Type: WorkflowStepSkill, Tool: "activate", Input: map[string]interface{}{"id": "review"}},
		},
	}
	if err := workflow.CreateWorkflow(root, definition.Name, definition); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	task := NewTaskService(agent)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), definition.Name)
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	defer func() { _ = task.FailWorkflowExecution(trustedTaskContext(), sessionID, "test cleanup") }()

	token, err := task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, definition.Name, "review")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval: %v", err)
	}
	task.mu.Lock()
	approval := task.approvals[token]
	task.mu.Unlock()
	if approval.adapter != workflowAdapterSkillActivate || approval.command != "" || approval.cwd != "" || len(approval.arguments) != 0 {
		t.Fatalf("Skill workflow approval was not catalog-owned: %+v", approval)
	}
	if _, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, definition.Name, "review-again", token); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-step Skill token replay error = %v, want ErrInvalidInput", err)
	}
	if _, bound := agentSkillBindingSnapshot(agent, sessionID, "review"); bound {
		t.Fatal("cross-step Skill replay activated the Skill")
	}
	if skills.IsApproved("review") {
		t.Fatal("cross-step Skill replay persisted project approval")
	}

	token, err = task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, definition.Name, "review")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval after replay: %v", err)
	}
	skills.mu.Lock()
	skills.skills[0].Description = "Changed after approval"
	skills.mu.Unlock()
	if err := skills.notifyMutationChange(); err != nil {
		t.Fatalf("refresh changed Skill: %v", err)
	}
	if _, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, definition.Name, "review", token); !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("stale Skill workflow capability error = %v, want ErrInvalidCapability", err)
	}
	if _, bound := agentSkillBindingSnapshot(agent, sessionID, "review"); bound {
		t.Fatal("stale Skill workflow capability activated the Skill")
	}
	if skills.IsApproved("review") {
		t.Fatal("stale Skill workflow capability persisted project approval")
	}

	token, err = task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, definition.Name, "review-again")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval after Skill refresh: %v", err)
	}
	result, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, definition.Name, "review-again", token)
	if err != nil {
		t.Fatalf("ExecuteApprovedWorkflowStep Skill: %v", err)
	}
	if result.ExitCode != 0 || result.Blocked || !strings.Contains(result.Stdout, "Activated skill review") {
		t.Fatalf("Skill workflow TaskService result = %+v", result)
	}
	if _, bound := agentSkillBindingSnapshot(agent, sessionID, "review"); !bound {
		t.Fatal("successful Skill workflow did not bind the Skill to its workflow session")
	}
	if !skills.IsApproved("review") || approvalCalls != 3 {
		t.Fatalf("project Skill approval state=%v calls=%d, want approved with three bound attempts", skills.IsApproved("review"), approvalCalls)
	}
}

func TestTaskServiceWorkflowCatalogMutationInvalidatesPendingStep(t *testing.T) {
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
	file := NewFileServiceWithWorkspaceContext(workspace)
	if err := file.setWorkspaceRoot(root); err != nil {
		t.Fatalf("file.setWorkspaceRoot: %v", err)
	}
	if err := WireAgentExecutionCore(agent, file, nil, nil, nil, nil); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	definition := &WorkflowDef{
		Name:  "mutable",
		Steps: []WorkflowStep{{Name: "step", Type: WorkflowStepCommand, Command: "go", Args: []string{"version"}}},
	}
	if err := workflow.CreateWorkflow(root, "mutable", definition); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	task := NewTaskService(agent)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "mutable")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	token, err := task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, "mutable", "step")
	if err != nil {
		t.Fatalf("RequestWorkflowStepApproval: %v", err)
	}
	definition.Steps[0].Args = []string{"env", "GOOS"}
	if err := workflow.SaveWorkflow(root, "mutable", definition); err != nil {
		t.Fatalf("SaveWorkflow: %v", err)
	}
	if _, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, "mutable", "step", token); !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("stale workflow capability error = %v, want ErrInvalidCapability", err)
	}
	if err := task.FailWorkflowExecution(trustedTaskContext(), sessionID, "catalog changed"); err != nil {
		t.Fatalf("FailWorkflowExecution: %v", err)
	}
}

func TestTaskServiceMismatchedExecutionBurnsCapabilityAndOwnedSession(t *testing.T) {
	agent := newTaskTestAgent(t)
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	service := NewTaskService(agent)
	token, err := service.RequestExecutionApproval(trustedTaskContext(), "task-a", "go version", "")
	if err != nil {
		t.Fatalf("RequestExecutionApproval: %v", err)
	}
	service.mu.Lock()
	approval := service.approvals[token]
	service.mu.Unlock()

	if _, err := service.ExecuteApproved(trustedTaskContext(), "task-b", "go version", "", token); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("mismatched ExecuteApproved error = %v, want ErrInvalidInput", err)
	}
	coreRuntime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if coreRuntime.IsSessionRegistered(approval.sessionID) {
		t.Fatal("mismatched execution retained its owned runtime session")
	}
	_, err = agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: approval.capability.Token, SessionID: approval.sessionID,
		CatalogRevision: approval.capability.CatalogRevision, ToolID: "run",
		Arguments: taskAgentRunArguments("go version", ""),
	})
	if !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("abandoned core capability remained redeemable: %v", err)
	}
}

func TestTaskServicePruneAndShutdownRevokePendingApprovals(t *testing.T) {
	agent := newTaskTestAgent(t)
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	service := NewTaskService(agent)
	firstToken, err := service.RequestExecutionApproval(trustedTaskContext(), "expires", "go version", "")
	if err != nil {
		t.Fatalf("first RequestExecutionApproval: %v", err)
	}
	service.mu.Lock()
	first := service.approvals[firstToken]
	first.expiresAt = time.Now().Add(-time.Second)
	service.approvals[firstToken] = first
	service.mu.Unlock()

	secondToken, err := service.RequestExecutionApproval(trustedTaskContext(), "shutdown", "go version", "")
	if err != nil {
		t.Fatalf("second RequestExecutionApproval: %v", err)
	}
	service.mu.Lock()
	second := service.approvals[secondToken]
	service.mu.Unlock()
	coreRuntime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if coreRuntime.IsSessionRegistered(first.sessionID) {
		t.Fatal("expired approval retained its owned runtime session")
	}
	if _, err := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: first.capability.Token, SessionID: first.sessionID,
		CatalogRevision: first.capability.CatalogRevision, ToolID: "run",
		Arguments: taskAgentRunArguments("go version", ""),
	}); !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("expired core capability remained redeemable: %v", err)
	}

	if err := service.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if coreRuntime.IsSessionRegistered(second.sessionID) {
		t.Fatal("Shutdown retained a pending approval session")
	}
	if _, err := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: second.capability.Token, SessionID: second.sessionID,
		CatalogRevision: second.capability.CatalogRevision, ToolID: "run",
		Arguments: taskAgentRunArguments("go version", ""),
	}); !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("Shutdown left core capability redeemable: %v", err)
	}
}

func TestTaskServiceApprovalUsesCoreCapabilityToken(t *testing.T) {
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
	service := NewTaskService(agent)

	outerToken, err := service.RequestExecutionApproval(trustedTaskContext(), "opaque", "go version", "")
	if err != nil {
		t.Fatalf("RequestExecutionApproval: %v", err)
	}
	service.mu.Lock()
	approval, ok := service.approvals[outerToken]
	service.mu.Unlock()
	if !ok {
		t.Fatal("TaskService did not retain the core capability server-side")
	}
	if outerToken != approval.capability.Token {
		t.Fatalf("TaskService token %q does not reuse core capability token %q", outerToken, approval.capability.Token)
	}

	if _, err := service.ExecuteApproved(trustedTaskContext(), "opaque", "go version", "", outerToken); err != nil {
		t.Fatalf("outer token did not remain usable through TaskService: %v", err)
	}
	if _, err = agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: outerToken, SessionID: "workflow:renderer-cannot-know-this",
		CatalogRevision: approval.capability.CatalogRevision, ToolID: "run",
		Arguments: taskAgentRunArguments("go version", ""),
	}); !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("consumed TaskService core token remained usable through generic endpoint: %v", err)
	}
}

func TestTaskServiceDoesNotReferenceLegacyCommandApproval(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not resolve the test file")
	}
	path := filepath.Join(filepath.Dir(testFile), "task_service.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse task_service.go: %v", err)
	}

	forbidden := map[string]bool{
		"requestCommandApprovalLegacy": false,
		"executeApprovedCommandLegacy": false,
		"consumeCommandApproval":       false,
		"discardCommandApproval":       false,
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if ok {
			if _, tracked := forbidden[selector.Sel.Name]; tracked {
				forbidden[selector.Sel.Name] = true
			}
		}
		return true
	})
	var found []string
	for name, present := range forbidden {
		if present {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	if len(found) != 0 {
		t.Fatalf("TaskService still references legacy command approval APIs: %s", strings.Join(found, ", "))
	}
}
