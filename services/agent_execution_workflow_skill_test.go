package services

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func TestAgentExecutionCorePublishesAndExecutesWorkflowCommandSteps(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	definition := "name: verify\nsteps:\n  - name: go-version\n    type: command\n    command: go\n    args: [version]\n  - name: must-not-run-as-shell\n    type: ai\n    command: payload-that-must-not-run\n"
	if err := os.WriteFile(filepath.Join(workflowDir, "verify.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := WireAgentWorkflowTools(agent, NewWorkflowService()); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}

	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var commandTool *AgentToolDefinition
	for index := range catalog.Tools {
		tool := &catalog.Tools[index]
		if tool.Source != "workflow" {
			continue
		}
		if tool.Metadata["step"] == "must-not-run-as-shell" {
			t.Fatalf("unsupported AI workflow step was published as executable: %+v", tool)
		}
		if tool.Metadata["workflow"] == "verify" && tool.Metadata["step"] == "go-version" {
			commandTool = tool
		}
	}
	if commandTool == nil {
		t.Fatalf("workflow command ToolDef missing from catalog: %+v", catalog.Tools)
	}
	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision,
		ToolID: commandTool.ID, Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("execute workflow command: %v", err)
	}
	if !strings.Contains(result.Observation, "Exit code: 0") {
		t.Fatalf("workflow observation = %q", result.Observation)
	}
}

func TestAgentExecutionCoreWorkflowFileReadUsesCatalogPath(t *testing.T) {
	agent, file, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("catalog-owned content"), 0o600); err != nil {
		t.Fatalf("seed workflow file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("renderer-selected content"), 0o600); err != nil {
		t.Fatalf("seed alternate file: %v", err)
	}
	definition := "name: inspect\nsteps:\n  - name: notes\n    type: file\n    tool: read\n    input:\n      path: ./notes.txt\n"
	if err := os.WriteFile(filepath.Join(workflowDir, "inspect.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := WireAgentWorkflowTools(agent, NewWorkflowService()); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}

	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var fileTool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == "workflow" && candidate.Metadata["workflow"] == "inspect" && candidate.Metadata["step"] == "notes" {
			fileTool = candidate
			break
		}
	}
	if fileTool == nil {
		t.Fatalf("workflow file ToolDef missing: %+v", catalog.Tools)
	}
	if fileTool.Metadata["adapter"] != workflowAdapterFileRead || fileTool.Metadata["path"] != "notes.txt" || fileTool.Mutation != string(agentcore.MutationNone) {
		t.Fatalf("workflow file ToolDef = %+v", fileTool)
	}
	if fileTool.Metadata[workflowSourcePathMetadata] != agentWorkflowDirectory+"/inspect.yaml" ||
		strings.Contains(fileTool.Metadata[workflowSourcePathMetadata], root) ||
		len(fileTool.Metadata[workflowSourceHashMetadata]) != sha256.Size*2 {
		t.Fatalf("workflow source metadata leaked or is incomplete: %+v", fileTool.Metadata)
	}

	readCalls := 0
	file.rootOperationHook = func(operation string) error {
		if operation == "ReadFile" {
			readCalls++
		}
		return nil
	}
	wrongArguments := map[string]interface{}{"path": "other.txt"}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision,
		ToolID: fileTool.ID, Arguments: wrongArguments,
	}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("renderer path redirect error = %v, want ErrNotAllowed", err)
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision,
		ToolID: fileTool.ID, Arguments: map[string]interface{}{"path": "notes.txt", "root": root},
	}); !errors.Is(err, agentcore.ErrInvalidArguments) {
		t.Fatalf("extra file authority error = %v, want ErrInvalidArguments", err)
	}
	if readCalls != 0 {
		t.Fatalf("rejected workflow file requests reached FileService %d times", readCalls)
	}

	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision,
		ToolID: fileTool.ID, Arguments: map[string]interface{}{"path": "notes.txt"},
	})
	if err != nil {
		t.Fatalf("execute workflow file read: %v", err)
	}
	if readCalls != 1 || !strings.Contains(result.Observation, "catalog-owned content") || strings.Contains(result.Observation, "renderer-selected content") || strings.Contains(result.Observation, root) {
		t.Fatalf("workflow file result = %+v, readCalls=%d", result, readCalls)
	}
}

func TestAgentExecutionCoreWorkflowFileWriteUsesBackendOwnedContent(t *testing.T) {
	agent, file, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	target := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	definition := "name: edit\nsteps:\n  - name: notes\n    type: file\n    tool: write\n    input:\n      path: notes.txt\n      content: backend-owned content\n"
	if err := os.WriteFile(filepath.Join(workflowDir, "edit.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := WireAgentWorkflowTools(agent, NewWorkflowService()); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var fileTool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["workflow"] == "edit" && candidate.Metadata["step"] == "notes" {
			fileTool = candidate
			break
		}
	}
	if fileTool == nil {
		t.Fatalf("workflow file write ToolDef missing: %+v", catalog.Tools)
	}
	if fileTool.Metadata["adapter"] != workflowAdapterFileWrite || fileTool.Mutation != string(agentcore.MutationWorkspaceTransaction) ||
		fileTool.Metadata["content"] != "" || fileTool.Metadata["path"] != "notes.txt" || fileTool.Metadata["contentHash"] == "" {
		t.Fatalf("workflow file write ToolDef leaked or has invalid contract: %+v", fileTool)
	}

	writeCalls := 0
	file.rootOperationHook = func(operation string) error {
		if operation == "WriteFile" || operation == "WriteFileIfUnchanged" {
			writeCalls++
		}
		return nil
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: fileTool.ID,
		Arguments: map[string]interface{}{"path": "other.txt", "content": "renderer redirect"},
	}); !errors.Is(err, agentcore.ErrInvalidArguments) {
		t.Fatalf("renderer file write arguments error = %v, want ErrInvalidArguments", err)
	}
	if writeCalls != 0 {
		t.Fatalf("rejected renderer file write reached FileService %d times", writeCalls)
	}

	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: fileTool.ID,
		Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("execute workflow file write: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "backend-owned content" {
		t.Fatalf("disk after workflow write = %q, err=%v", data, err)
	}
	if writeCalls != 1 || !strings.Contains(result.Observation, "notes.txt") || strings.Contains(result.Observation, root) {
		t.Fatalf("workflow file write result=%+v writeCalls=%d", result, writeCalls)
	}
}

func TestAgentExecutionCoreWorkflowFileWriteRejectsSourceMutationBeforeWrite(t *testing.T) {
	agent, file, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	target := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "edit.yaml")
	initial := []byte("name: edit\nsteps:\n  - name: notes\n    type: file\n    tool: write\n    input:\n      path: notes.txt\n      content: initial\n")
	changed := []byte("name: edit\nsteps:\n  - name: notes\n    type: file\n    tool: write\n    input:\n      path: notes.txt\n      content: changed\n")
	if err := os.WriteFile(workflowPath, initial, 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var tool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["workflow"] == "edit" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("workflow file write ToolDef missing: %+v", catalog.Tools)
	}
	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: tool.ID, Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("RequestAgentToolCapability: %v", err)
	}
	if err := os.WriteFile(workflowPath, changed, 0o600); err != nil {
		t.Fatalf("mutate workflow source: %v", err)
	}
	writeCalls := 0
	file.rootOperationHook = func(operation string) error {
		if operation == "WriteFile" {
			writeCalls++
		}
		return nil
	}
	result, err := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: "workflow:verify", CatalogRevision: grant.CatalogRevision,
		ToolID: grant.ToolID, Arguments: map[string]interface{}{},
	})
	if err == nil || (!errors.Is(err, ErrNotAllowed) && !errors.Is(err, agentcore.ErrInvalidCapability)) {
		t.Fatalf("source mutation execution error = %v, want fail-closed rejection", err)
	}
	if writeCalls != 0 || result.Observation != "" {
		t.Fatalf("source mutation reached file writer or exposed output: calls=%d result=%+v", writeCalls, result)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "before" {
		t.Fatalf("target changed after source mutation: %q, err=%v", data, readErr)
	}
}

func TestAgentExecutionCoreWorkflowFileWriteRejectsBaselineConflict(t *testing.T) {
	agent, file, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	target := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "edit.yaml"), []byte("name: edit\nsteps:\n  - name: notes\n    type: file\n    tool: write\n    input:\n      path: notes.txt\n      content: approved\n"), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := WireAgentWorkflowTools(agent, NewWorkflowService()); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var tool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["workflow"] == "edit" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("workflow file write ToolDef missing: %+v", catalog.Tools)
	}
	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: tool.ID, Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("RequestAgentToolCapability: %v", err)
	}
	if err := file.WriteFile(target, "concurrent change"); err != nil {
		t.Fatalf("mutate target after approval: %v", err)
	}
	result, err := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: "workflow:verify", CatalogRevision: grant.CatalogRevision,
		ToolID: grant.ToolID, Arguments: map[string]interface{}{},
	})
	if err == nil || (!errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotAllowed) && !errors.Is(err, agentcore.ErrInvalidCapability)) {
		t.Fatalf("baseline conflict execution error = %v, want fail-closed rejection", err)
	}
	if result.Observation != "" {
		t.Fatalf("baseline conflict exposed output: %+v", result)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "concurrent change" {
		t.Fatalf("baseline conflict overwrote concurrent content: %q, err=%v", data, readErr)
	}
}

func TestAgentExecutionCoreWorkflowFileWriteRejectsPublishRace(t *testing.T) {
	agent, file, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	target := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "edit.yaml"), []byte("name: edit\nsteps:\n  - name: notes\n    type: file\n    tool: write\n    input:\n      path: notes.txt\n      content: approved\n"), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := WireAgentWorkflowTools(agent, NewWorkflowService()); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var tool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["workflow"] == "edit" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("workflow file write ToolDef missing: %+v", catalog.Tools)
	}
	file.writeAtomic = func() error {
		return os.WriteFile(target, []byte("external publish race"), 0o600)
	}
	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: tool.ID, Arguments: map[string]interface{}{},
	})
	if err == nil || (!errors.Is(err, ErrFileConflict) && !errors.Is(err, ErrNotAllowed)) {
		t.Fatalf("publish race execution error = %v, want a fail-closed conflict", err)
	}
	if result.Observation != "" || strings.Contains(err.Error(), root) || strings.Contains(result.Usage.Error, root) {
		t.Fatalf("publish race exposed output: %+v", result)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "external publish race" {
		t.Fatalf("publish race overwrote external content: %q, err=%v", data, readErr)
	}
}

func TestAgentExecutionCoreWorkflowFileWriteRequiresApprovalCallback(t *testing.T) {
	agent, file, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	target := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "edit.yaml"), []byte("name: edit\nsteps:\n  - name: notes\n    type: file\n    tool: write\n    input:\n      path: notes.txt\n      content: denied\n"), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := WireAgentWorkflowTools(agent, NewWorkflowService()); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var tool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["workflow"] == "edit" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("workflow file write ToolDef missing: %+v", catalog.Tools)
	}
	agent.approveWrite = nil
	writeCalls := 0
	file.rootOperationHook = func(operation string) error {
		if operation == "WriteFile" {
			writeCalls++
		}
		return nil
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: tool.ID, Arguments: map[string]interface{}{},
	}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("missing workflow write approver error = %v, want ErrNotAllowed", err)
	}
	if writeCalls != 0 {
		t.Fatalf("missing workflow write approver reached FileService writer %d times", writeCalls)
	}
}

func TestAgentWorkflowLoaderRejectsMalformedSourceBatch(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "valid.yaml"), []byte("name: valid\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [version]\n"), 0o600); err != nil {
		t.Fatalf("write valid workflow: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "broken.yaml"), []byte("name: [broken"), 0o600); err != nil {
		t.Fatalf("write malformed workflow: %v", err)
	}
	err := WireAgentWorkflowTools(agent, NewWorkflowService())
	if err == nil || !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("malformed source batch error = %v, want ErrInvalidInput", err)
	}
	runtime, runtimeErr := agent.coreRuntime()
	if runtimeErr != nil {
		t.Fatalf("coreRuntime: %v", runtimeErr)
	}
	for _, tool := range runtime.Registry().Snapshot().Tools {
		if tool.Source == agentcore.SourceWorkflow {
			t.Fatalf("valid subset survived malformed source batch: %+v", tool)
		}
	}
}

func TestAgentWorkflowLoaderRejectsDuplicateWorkflowNames(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	definition := []byte("name: duplicate\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [version]\n")
	for _, name := range []string{"a.yaml", "b.yaml"} {
		if err := os.WriteFile(filepath.Join(workflowDir, name), definition, 0o600); err != nil {
			t.Fatalf("write duplicate workflow %q: %v", name, err)
		}
	}
	err := WireAgentWorkflowTools(agent, NewWorkflowService())
	if err == nil || !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("duplicate source batch error = %v, want ErrNotAllowed", err)
	}
	runtime, runtimeErr := agent.coreRuntime()
	if runtimeErr != nil {
		t.Fatalf("coreRuntime: %v", runtimeErr)
	}
	for _, tool := range runtime.Registry().Snapshot().Tools {
		if tool.Source == agentcore.SourceWorkflow {
			t.Fatalf("duplicate source batch published a ToolDef: %+v", tool)
		}
	}
}

func TestAgentExecutionCoreWorkflowFileReadRejectsSourceMutationBeforeRead(t *testing.T) {
	agent, file, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("catalog content"), 0o600); err != nil {
		t.Fatalf("seed catalog file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret content"), 0o600); err != nil {
		t.Fatalf("seed alternate file: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "inspect.yaml")
	if err := os.WriteFile(workflowPath, []byte("name: inspect\nsteps:\n  - name: notes\n    type: file\n    tool: read\n    input:\n      path: notes.txt\n"), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var tool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["workflow"] == "inspect" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("workflow file ToolDef missing: %+v", catalog.Tools)
	}
	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: tool.ID,
		Arguments: map[string]interface{}{"path": "notes.txt"},
	})
	if err != nil {
		t.Fatalf("RequestAgentToolCapability: %v", err)
	}
	if err := os.WriteFile(workflowPath, []byte("name: inspect\nsteps:\n  - name: notes\n    type: file\n    tool: read\n    input:\n      path: secret.txt\n"), 0o600); err != nil {
		t.Fatalf("mutate workflow source: %v", err)
	}
	readCalls := 0
	file.rootOperationHook = func(operation string) error {
		if operation == "ReadFile" {
			readCalls++
		}
		return nil
	}
	result, err := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: "workflow:verify", CatalogRevision: grant.CatalogRevision,
		ToolID: grant.ToolID, Arguments: map[string]interface{}{"path": "notes.txt"},
	})
	if err == nil || (!errors.Is(err, ErrNotAllowed) && !errors.Is(err, agentcore.ErrInvalidCapability)) {
		t.Fatalf("source mutation execution error = %v, want fail-closed capability rejection", err)
	}
	if readCalls != 0 || result.Observation != "" || result.Metadata != nil {
		t.Fatalf("source mutation reached file reader or exposed output: calls=%d result=%+v", readCalls, result)
	}
}

func TestAgentExecutionCoreWorkflowCommandRejectsSourceMutationDuringApproval(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "command.yaml")
	initial := []byte("name: command\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [version]\n")
	changed := []byte("name: command\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [env, GOOS]\n")
	if err := os.WriteFile(workflowPath, initial, 0o600); err != nil {
		t.Fatalf("write initial workflow: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var tool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["workflow"] == "command" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("workflow command ToolDef missing: %+v", catalog.Tools)
	}
	agent.approveCommand = func(string, string, RiskLevel) bool {
		if err := os.WriteFile(workflowPath, changed, 0o600); err != nil {
			t.Fatalf("mutate workflow during approval: %v", err)
		}
		return true
	}
	_, err = agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: tool.ID,
		Arguments: map[string]interface{}{},
	})
	if err == nil || !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("approval-window source mutation error = %v, want ErrNotAllowed", err)
	}
}

func TestAgentExecutionCoreWorkflowGitStatusUsesWorkspaceRepository(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	git := NewGitService()
	if err := git.setWorkspaceRoot(root); err != nil {
		t.Fatalf("git.setWorkspaceRoot: %v", err)
	}
	if err := git.InitRepo(root); err != nil {
		t.Fatalf("Git InitRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("workspace-owned"), 0o600); err != nil {
		t.Fatalf("seed Git change: %v", err)
	}
	if err := WireAgentExecutionCore(agent, file, search, nil, nil, nil, git); err != nil {
		t.Fatalf("WireAgentExecutionCore Git: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	if err := workflow.CreateWorkflow(root, "git-status", &WorkflowDef{
		Name:  "git-status",
		Steps: []WorkflowStep{{Name: "status", Type: WorkflowStepGit, Tool: "status", Input: map[string]interface{}{}}},
	}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var statusTool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["workflow"] == "git-status" {
			statusTool = candidate
			break
		}
	}
	if statusTool == nil || statusTool.Metadata["adapter"] != workflowAdapterGitStatus || statusTool.Mutation != string(agentcore.MutationNone) {
		t.Fatalf("workflow Git status ToolDef = %+v", statusTool)
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: statusTool.ID,
		Arguments: map[string]interface{}{"repo": filepath.Join(root, "outside")},
	}); !errors.Is(err, agentcore.ErrInvalidArguments) {
		t.Fatalf("renderer Git repository redirect error = %v, want ErrInvalidArguments", err)
	}
	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: statusTool.ID,
		Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("execute workflow Git status: %v", err)
	}
	if !strings.Contains(result.Observation, "untracked.txt") || strings.Contains(result.Observation, root) {
		t.Fatalf("workflow Git status observation = %q", result.Observation)
	}
}

func TestAgentExecutionCoreWorkflowGitStatusObservationIsBoundedAndSorted(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	gitService := NewGitService()
	if err := gitService.setWorkspaceRoot(root); err != nil {
		t.Fatalf("git.setWorkspaceRoot: %v", err)
	}
	if err := gitService.InitRepo(root); err != nil {
		t.Fatalf("Git InitRepo: %v", err)
	}
	for index := 0; index < 600; index++ {
		name := filepath.Join(root, fmt.Sprintf("status-%04d.txt", index))
		if err := os.WriteFile(name, []byte("changed"), 0o600); err != nil {
			t.Fatalf("seed status file %d: %v", index, err)
		}
	}
	if err := WireAgentExecutionCore(agent, file, search, nil, nil, nil, gitService); err != nil {
		t.Fatalf("WireAgentExecutionCore Git: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	if err := workflow.CreateWorkflow(root, "git-bounded", &WorkflowDef{
		Name:  "git-bounded",
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
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["workflow"] == "git-bounded" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("bounded Git status ToolDef missing: %+v", catalog.Tools)
	}
	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: tool.ID,
		Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("execute bounded Git status: %v", err)
	}
	if len(result.Observation) > maxAgentObservationBytes || !json.Valid([]byte(result.Observation)) {
		t.Fatalf("Git status observation is not bounded valid JSON: bytes=%d observation suffix=%q", len(result.Observation), result.Observation[max(0, len(result.Observation)-64):])
	}
	var changes []GitFileChange
	if err := json.Unmarshal([]byte(result.Observation), &changes); err != nil {
		t.Fatalf("decode Git status observation: %v", err)
	}
	if len(changes) == 0 || len(changes) >= 600 {
		t.Fatalf("Git status observation was not bounded: %d entries", len(changes))
	}
	for index := 1; index < len(changes); index++ {
		if changes[index-1].Path > changes[index].Path {
			t.Fatalf("Git status observation is not sorted: %q before %q", changes[index-1].Path, changes[index].Path)
		}
	}
}

func TestAgentExecutionCoreWorkflowMCPUsesCatalogOwnedInput(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	toolCalls := 0
	transport := newScriptedMCPTransport(scriptedMCPInitializeHandler(mcpTestToolsCapability, func(request *jsonrpcOutboundMessage, _ int) *jsonrpcResponse {
		var result string
		switch request.Method {
		case "tools/list":
			result = `{"tools":[{"name":"lookup","description":"Lookup docs","inputSchema":{"type":"object","properties":{"query":{"type":"string","minLength":1}},"required":["query"]}}]}`
		case "tools/call":
			toolCalls++
			result = `{"content":[{"type":"text","text":"workflow-mcp-ok"}]}`
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
		return server == "docs" && tool == "lookup" &&
			strings.Contains(args, "catalog-owned-query") && risk == RiskElevated
	}
	if err := WireAgentExecutionCore(agent, file, search, mcp, nil, nil); err != nil {
		t.Fatalf("WireAgentExecutionCore MCP: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	if err := workflow.CreateWorkflow(root, "mcp-lookup", &WorkflowDef{
		Name: "mcp-lookup",
		Steps: []WorkflowStep{{
			Name: "lookup", Type: WorkflowStepMCP, Tool: "mcp.docs.lookup",
			Input: map[string]interface{}{"query": "catalog-owned-query"},
		}},
	}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var workflowTool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == "workflow" && candidate.Metadata["workflow"] == "mcp-lookup" && candidate.Metadata["step"] == "lookup" {
			workflowTool = candidate
			break
		}
	}
	if workflowTool == nil {
		t.Fatalf("workflow MCP ToolDef missing: %+v", catalog.Tools)
	}
	if workflowTool.Metadata["adapter"] != workflowAdapterMCP ||
		workflowTool.Metadata["delegatedTool"] != "mcp.docs.lookup" ||
		workflowTool.Mutation != string(agentcore.MutationExternal) {
		t.Fatalf("workflow MCP ToolDef = %+v", workflowTool)
	}
	for key, value := range workflowTool.Metadata {
		if strings.Contains(value, "catalog-owned-query") {
			t.Fatalf("workflow MCP metadata leaked input in %s=%q", key, value)
		}
	}

	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision,
		ToolID: workflowTool.ID, Arguments: map[string]interface{}{"query": "renderer-redirect"},
	}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("renderer MCP input redirect error = %v, want ErrNotAllowed", err)
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision,
		ToolID: workflowTool.ID, Arguments: map[string]interface{}{"query": "catalog-owned-query", "root": root},
	}); !errors.Is(err, agentcore.ErrInvalidArguments) {
		t.Fatalf("extra MCP authority error = %v, want ErrInvalidArguments", err)
	}
	if toolCalls != 0 || approvals != 0 {
		t.Fatalf("rejected MCP requests reached approval/execution: approvals=%d calls=%d", approvals, toolCalls)
	}

	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision,
		ToolID: workflowTool.ID, Arguments: map[string]interface{}{"query": "catalog-owned-query"},
	})
	if err != nil {
		t.Fatalf("execute workflow MCP: %v", err)
	}
	if approvals != 1 || toolCalls != 1 || !strings.Contains(result.Observation, "workflow-mcp-ok") ||
		result.Usage.ExternalReceiptID == "" || result.Usage.ExternalReceiptReversible {
		t.Fatalf("workflow MCP result=%+v approvals=%d calls=%d", result, approvals, toolCalls)
	}
}

func TestAgentExecutionCoreMCPObservationIsBounded(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	transport := newScriptedMCPTransport(scriptedMCPInitializeHandler(mcpTestToolsCapability, func(request *jsonrpcOutboundMessage, _ int) *jsonrpcResponse {
		var result string
		switch request.Method {
		case "tools/list":
			result = `{"tools":[{"name":"lookup","description":"Lookup docs","inputSchema":{"type":"object","properties":{"query":{"type":"string","minLength":1}},"required":["query"]}}]}`
		case "tools/call":
			result = `{"content":[{"type":"text","text":"` + strings.Repeat("x", maxAgentObservationBytes*3) + `"}]}`
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
	mcp.approveTool = func(string, string, string, RiskLevel) bool { return true }
	if err := WireAgentExecutionCore(agent, file, search, mcp, nil, nil); err != nil {
		t.Fatalf("WireAgentExecutionCore MCP: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	if err := workflow.CreateWorkflow(root, "mcp-output", &WorkflowDef{
		Name:  "mcp-output",
		Steps: []WorkflowStep{{Name: "lookup", Type: WorkflowStepMCP, Tool: "mcp.docs.lookup", Input: map[string]interface{}{"query": "bounded"}}},
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
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["workflow"] == "mcp-output" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("bounded MCP workflow ToolDef missing: %+v", catalog.Tools)
	}
	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: tool.ID,
		Arguments: map[string]interface{}{"query": "bounded"},
	})
	if err != nil {
		t.Fatalf("execute bounded MCP workflow: %v", err)
	}
	if len(result.Observation) > maxAgentObservationBytes || !strings.Contains(result.Observation, "[truncated,") {
		t.Fatalf("MCP observation was not bounded: bytes=%d suffix=%q", len(result.Observation), result.Observation[max(0, len(result.Observation)-48):])
	}
}

func TestWorkflowCRUDRefreshesRegistryAndInvalidatesCapabilities(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkflowService, string) error
	}{
		{
			name: "create",
			mutate: func(service *WorkflowService, root string) error {
				return service.CreateWorkflow(root, "added", &WorkflowDef{
					Name:  "added",
					Steps: []WorkflowStep{{Name: "step", Command: "go", Args: []string{"env", "GOOS"}}},
				})
			},
		},
		{
			name: "save",
			mutate: func(service *WorkflowService, root string) error {
				return service.SaveWorkflow(root, "base", &WorkflowDef{
					Name:  "base",
					Steps: []WorkflowStep{{Name: "step", Command: "go", Args: []string{"env", "GOARCH"}}},
				})
			},
		},
		{
			name: "delete",
			mutate: func(service *WorkflowService, root string) error {
				return service.DeleteWorkflow(root, "base")
			},
		},
		{
			name: "rename",
			mutate: func(service *WorkflowService, root string) error {
				return service.RenameWorkflow(root, "base", "renamed")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, _, _, root := newExecutionCoreTestServices(t)
			workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
			if err := os.MkdirAll(workflowDir, 0o700); err != nil {
				t.Fatalf("create workflow dir: %v", err)
			}
			initial := []byte("name: base\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [version]\n")
			if err := os.WriteFile(filepath.Join(workflowDir, "base.yml"), initial, 0o600); err != nil {
				t.Fatalf("write initial workflow: %v", err)
			}
			workflow := NewWorkflowService()
			if err := WireAgentWorkflowTools(agent, workflow); err != nil {
				t.Fatalf("WireAgentWorkflowTools: %v", err)
			}
			runtime, err := agent.coreRuntime()
			if err != nil {
				t.Fatalf("coreRuntime: %v", err)
			}
			before := runtime.Registry().Snapshot()
			var tool *agentcore.ToolDef
			for index := range before.Tools {
				candidate := &before.Tools[index]
				if candidate.Source == agentcore.SourceWorkflow && candidate.Metadata["workflow"] == "base" {
					tool = candidate
					break
				}
			}
			if tool == nil {
				t.Fatalf("base workflow ToolDef missing: %+v", before.Tools)
			}
			arguments := map[string]interface{}{}
			grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
				SessionID: "workflow:verify", CatalogRevision: before.Revision,
				ToolID: tool.ID, Arguments: arguments,
			})
			if err != nil {
				t.Fatalf("issue workflow capability: %v", err)
			}

			if err := test.mutate(workflow, root); err != nil {
				t.Fatalf("mutate workflow: %v", err)
			}
			after := runtime.Registry().Snapshot()
			if after.Revision == before.Revision {
				t.Fatalf("workflow mutation did not refresh registry revision: before=%d after=%d", before.Revision, after.Revision)
			}
			if _, err := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
				Token: grant.Token, SessionID: "workflow:verify", CatalogRevision: grant.CatalogRevision,
				ToolID: grant.ToolID, Arguments: arguments,
			}); !errors.Is(err, agentcore.ErrInvalidCapability) {
				t.Fatalf("old workflow capability remained valid after %s: %v", test.name, err)
			}
		})
	}
}

func TestWorkflowMutationRefreshFailureClearsSourceAndInvalidatesCapabilities(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "base.yml"), []byte("name: base\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [version]\n"), 0o600); err != nil {
		t.Fatalf("write initial workflow: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	before := runtime.Registry().Snapshot()
	var tool *agentcore.ToolDef
	for index := range before.Tools {
		candidate := &before.Tools[index]
		if candidate.Source == agentcore.SourceWorkflow && candidate.Metadata["workflow"] == "base" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("base workflow ToolDef missing: %+v", before.Tools)
	}
	arguments := map[string]interface{}{}
	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: before.Revision,
		ToolID: tool.ID, Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("issue workflow capability: %v", err)
	}

	err = workflow.SaveWorkflow(root, "base", &WorkflowDef{
		Name:  "base",
		Steps: []WorkflowStep{{Name: "step", Command: "echo '", Type: WorkflowStepCommand}},
	})
	if err == nil {
		t.Fatal("invalid command refresh unexpectedly succeeded")
	}
	after := runtime.Registry().Snapshot()
	for _, candidate := range after.Tools {
		if candidate.Source == agentcore.SourceWorkflow {
			t.Fatalf("refresh failure left workflow ToolDef exposed: %+v", candidate)
		}
	}
	if _, err := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: "workflow:verify", CatalogRevision: grant.CatalogRevision,
		ToolID: grant.ToolID, Arguments: arguments,
	}); !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("old workflow capability remained valid after failed refresh: %v", err)
	}
}

func TestWorkflowCatalogRefreshesAreSerialized(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "base.yml"), []byte(
		"name: base\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [version]\n",
	), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}

	firstReady := make(chan struct{})
	secondReady := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseFirstOnce sync.Once
	releaseFirstRefresh := func() { releaseFirstOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(releaseFirstRefresh)
	var publications atomic.Int32
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.catalogRefreshHook = func(stage string) {
		if stage != agentCatalogRefreshBeforeWorkflowPublish {
			return
		}
		switch publications.Add(1) {
		case 1:
			close(firstReady)
			<-releaseFirst
		case 2:
			close(secondReady)
		}
	}
	deps.mu.Unlock()

	firstErr := make(chan error, 1)
	go func() { firstErr <- agent.refreshWorkflowAgentToolsAfterMutation(context.Background()) }()
	select {
	case <-firstReady:
	case <-time.After(2 * time.Second):
		releaseFirstRefresh()
		t.Fatal("first workflow refresh did not reach the publication hook")
	}

	updated := []byte("name: base\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [env, GOOS]\n")
	if err := os.WriteFile(filepath.Join(workflowDir, "base.yml"), updated, 0o600); err != nil {
		t.Fatalf("write updated workflow: %v", err)
	}

	secondErr := make(chan error, 1)
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		secondErr <- agent.refreshDynamicAgentTools(context.Background())
	}()
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		releaseFirstRefresh()
		t.Fatal("second workflow refresh goroutine did not start")
	}
	select {
	case <-secondReady:
		releaseFirstRefresh()
		t.Fatal("second workflow refresh reached publication while the first refresh still held the serialization lock")
	case <-time.After(2 * time.Second):
		releaseFirstRefresh()
	}
	for label, result := range map[string]<-chan error{"first": firstErr, "second": secondErr} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("%s workflow refresh: %v", label, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s workflow refresh did not finish after release", label)
		}
	}

	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	for _, tool := range runtime.Registry().Snapshot().Tools {
		if tool.Source == agentcore.SourceWorkflow && tool.Metadata["workflow"] == "base" {
			if !strings.Contains(tool.Metadata["command"], "GOOS") {
				t.Fatalf("stale workflow publication replaced the newer catalog: %+v", tool)
			}
			return
		}
	}
	t.Fatal("updated workflow ToolDef missing")
}

func TestDynamicAgentCatalogPublishesSourcesAsOneSnapshot(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	skills := &SkillsService{skills: []Skill{{ID: "old-skill", Name: "Old Skill", Scope: SkillScopeUser}}}
	if err := WireAgentExecutionCore(agent, file, search, nil, skills, nil); err != nil {
		t.Fatalf("wire initial skills: %v", err)
	}
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "base.yml")
	if err := os.WriteFile(workflowPath, []byte(
		"name: base\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [version]\n",
	), 0o600); err != nil {
		t.Fatalf("write initial workflow: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if _, err := runtime.Registry().ReplaceSource(agentcore.SourceMCP, []agentcore.ToolDef{{
		ID:          "mcp.old.probe",
		Description: "Old MCP probe.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Source:      agentcore.SourceMCP,
		Risk:        agentcore.RiskElevated,
		Approval:    agentcore.ApprovalManual,
		Mutation:    agentcore.MutationExternal,
		ExecuteKey:  "mcp.call",
	}}); err != nil {
		t.Fatalf("seed old MCP source: %v", err)
	}

	dynamicSignature := func(catalog agentcore.Catalog) string {
		parts := make([]string, 0)
		for _, tool := range catalog.Tools {
			if tool.Source == agentcore.SourceBuiltin {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s:%s:%s:%s", tool.Source, tool.ID, tool.Metadata["command"], tool.Metadata["skillId"]))
		}
		return strings.Join(parts, "|")
	}
	before := runtime.Registry().Snapshot()
	beforeSignature := dynamicSignature(before)
	for _, required := range []string{"mcp.old.probe", "old-skill", "version"} {
		if !strings.Contains(beforeSignature, required) {
			t.Fatalf("initial dynamic catalog lacks %q: %s", required, beforeSignature)
		}
	}

	if err := os.WriteFile(workflowPath, []byte(
		"name: base\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [env, GOOS]\n",
	), 0o600); err != nil {
		t.Fatalf("write updated workflow: %v", err)
	}
	skills.mu.Lock()
	skills.skills = []Skill{{ID: "new-skill", Name: "New Skill", Scope: SkillScopeUser}}
	skills.mu.Unlock()

	beforeWorkflowPublish := make(chan struct{})
	releasePublish := make(chan struct{})
	var publications atomic.Int32
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.catalogRefreshHook = func(stage string) {
		if stage == agentCatalogRefreshBeforeWorkflowPublish && publications.Add(1) == 1 {
			close(beforeWorkflowPublish)
			<-releasePublish
		}
	}
	deps.mu.Unlock()

	refreshErr := make(chan error, 1)
	go func() { refreshErr <- agent.refreshDynamicAgentTools(context.Background()) }()
	select {
	case <-beforeWorkflowPublish:
	case <-time.After(2 * time.Second):
		close(releasePublish)
		t.Fatal("dynamic refresh did not reach workflow publication hook")
	}
	during := runtime.Registry().Snapshot()
	duringSignature := dynamicSignature(during)
	close(releasePublish)
	if err := <-refreshErr; err != nil {
		t.Fatalf("refresh dynamic tools: %v", err)
	}
	if during.Revision != before.Revision || duringSignature != beforeSignature {
		t.Fatalf("reader observed a mixed dynamic catalog: before=%d %q, during=%d %q", before.Revision, beforeSignature, during.Revision, duringSignature)
	}

	after := runtime.Registry().Snapshot()
	if after.Revision != before.Revision+1 {
		t.Fatalf("dynamic refresh published %d revisions, want exactly one", after.Revision-before.Revision)
	}
	afterSignature := dynamicSignature(after)
	for _, required := range []string{"new-skill", "GOOS"} {
		if !strings.Contains(afterSignature, required) {
			t.Fatalf("final dynamic catalog lacks %q: %s", required, afterSignature)
		}
	}
	for _, stale := range []string{"mcp.old.probe", "old-skill", "version"} {
		if strings.Contains(afterSignature, stale) {
			t.Fatalf("final dynamic catalog retained %q: %s", stale, afterSignature)
		}
	}
}

func TestDynamicAgentCatalogMutationClearsAllSourcesBeforeRebuild(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*AgentService) error
	}{
		{
			name: "workflow",
			mutate: func(agent *AgentService) error {
				return agent.refreshWorkflowAgentToolsAfterMutation(context.Background())
			},
		},
		{
			name: "skill",
			mutate: func(agent *AgentService) error {
				return agent.refreshSkillAgentToolsAfterMutation(context.Background())
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent, file, search, root := newExecutionCoreTestServices(t)
			skills := &SkillsService{skills: []Skill{{ID: "stable", Name: "Stable", Scope: SkillScopeUser}}}
			if err := WireAgentExecutionCore(agent, file, search, nil, skills, nil); err != nil {
				t.Fatalf("wire skills: %v", err)
			}
			workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
			if err := os.MkdirAll(workflowDir, 0o700); err != nil {
				t.Fatalf("create workflow dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(workflowDir, "base.yml"), []byte(
				"name: base\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [version]\n",
			), 0o600); err != nil {
				t.Fatalf("write workflow: %v", err)
			}
			if err := WireAgentWorkflowTools(agent, NewWorkflowService()); err != nil {
				t.Fatalf("wire workflow: %v", err)
			}
			runtime, err := agent.coreRuntime()
			if err != nil {
				t.Fatalf("coreRuntime: %v", err)
			}
			if _, err := runtime.Registry().ReplaceSource(agentcore.SourceMCP, []agentcore.ToolDef{{
				ID:          "mcp.stable.probe",
				Description: "Stable MCP probe.",
				InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
				Source:      agentcore.SourceMCP,
				Risk:        agentcore.RiskElevated,
				Approval:    agentcore.ApprovalManual,
				Mutation:    agentcore.MutationExternal,
				ExecuteKey:  "mcp.call",
			}}); err != nil {
				t.Fatalf("seed MCP source: %v", err)
			}

			before := runtime.Registry().Snapshot()
			seenSources := make(map[agentcore.ToolSource]bool)
			for _, tool := range before.Tools {
				if tool.Source != agentcore.SourceBuiltin {
					seenSources[tool.Source] = true
				}
			}
			for _, source := range []agentcore.ToolSource{agentcore.SourceMCP, agentcore.SourceWorkflow, agentcore.SourceSkill} {
				if !seenSources[source] {
					t.Fatalf("initial catalog lacks %s source: %+v", source, before.Tools)
				}
			}

			beforeWorkflowPublish := make(chan struct{})
			releasePublish := make(chan struct{})
			var hookOnce sync.Once
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releasePublish) }) }
			t.Cleanup(release)
			deps := executionDependenciesFor(agent)
			deps.mu.Lock()
			deps.catalogRefreshHook = func(stage string) {
				if stage == agentCatalogRefreshBeforeWorkflowPublish {
					hookOnce.Do(func() {
						close(beforeWorkflowPublish)
						<-releasePublish
					})
				}
			}
			deps.mu.Unlock()

			mutationErr := make(chan error, 1)
			go func() { mutationErr <- test.mutate(agent) }()
			select {
			case <-beforeWorkflowPublish:
			case <-time.After(2 * time.Second):
				release()
				t.Fatal("mutation refresh did not reach workflow publication hook")
			}
			during := runtime.Registry().Snapshot()
			for _, tool := range during.Tools {
				if tool.Source != agentcore.SourceBuiltin {
					release()
					t.Fatalf("mutation exposed a partially cleared dynamic catalog at revision %d: %+v", during.Revision, during.Tools)
				}
			}
			release()
			select {
			case err := <-mutationErr:
				if err != nil {
					t.Fatalf("mutation refresh: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("mutation refresh did not finish after release")
			}
		})
	}
}

func TestDynamicAgentCatalogRefreshFailureDoesNotPublishPartialSources(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	skills := &SkillsService{skills: []Skill{{ID: "stable", Name: "Stable", Scope: SkillScopeUser}}}
	if err := WireAgentExecutionCore(agent, file, search, nil, skills, nil); err != nil {
		t.Fatalf("wire skills: %v", err)
	}
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "base.yml"), []byte(
		"name: base\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [version]\n",
	), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := WireAgentWorkflowTools(agent, NewWorkflowService()); err != nil {
		t.Fatalf("wire workflow: %v", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if _, err := runtime.Registry().ReplaceSource(agentcore.SourceMCP, []agentcore.ToolDef{{
		ID:          "mcp.stable.probe",
		Description: "Stable MCP probe.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Source:      agentcore.SourceMCP,
		Risk:        agentcore.RiskElevated,
		Approval:    agentcore.ApprovalManual,
		Mutation:    agentcore.MutationExternal,
		ExecuteKey:  "mcp.call",
	}}); err != nil {
		t.Fatalf("seed MCP source: %v", err)
	}

	skills.mu.Lock()
	skills.skills = []Skill{{ID: "", Name: "broken"}}
	skills.mu.Unlock()
	if err := agent.refreshDynamicAgentTools(context.Background()); err == nil {
		t.Fatal("refresh with invalid Skill unexpectedly succeeded")
	}
	for _, tool := range runtime.Registry().Snapshot().Tools {
		if tool.Source != agentcore.SourceBuiltin {
			t.Fatalf("failed refresh published a partial dynamic catalog: %+v", tool)
		}
	}
}

func TestWorkflowMutationOtherSourceFailureClearsWorkflowSource(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "base.yml"), []byte("name: base\nsteps:\n  - name: step\n    type: command\n    command: go\n    args: [version]\n"), 0o600); err != nil {
		t.Fatalf("write initial workflow: %v", err)
	}
	skills := &SkillsService{skills: []Skill{{ID: "stable", Name: "Stable", Scope: SkillScopeUser}}}
	if err := WireAgentExecutionCore(agent, file, search, nil, skills, nil); err != nil {
		t.Fatalf("wire skills: %v", err)
	}
	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	before := runtime.Registry().Snapshot()
	var tool *agentcore.ToolDef
	for index := range before.Tools {
		candidate := &before.Tools[index]
		if candidate.Source == agentcore.SourceWorkflow && candidate.Metadata["workflow"] == "base" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("base workflow ToolDef missing: %+v", before.Tools)
	}
	arguments := map[string]interface{}{}
	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: before.Revision,
		ToolID: tool.ID, Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("issue workflow capability: %v", err)
	}

	skills.mu.Lock()
	skills.skills = []Skill{{ID: "", Name: "broken"}}
	skills.mu.Unlock()
	if err := workflow.SaveWorkflow(root, "base", &WorkflowDef{
		Name:  "base",
		Steps: []WorkflowStep{{Name: "step", Command: "go", Args: []string{"env", "GOOS"}}},
	}); err == nil {
		t.Fatal("refresh failure from another dynamic source unexpectedly succeeded")
	}
	after := runtime.Registry().Snapshot()
	for _, candidate := range after.Tools {
		if candidate.Source == agentcore.SourceWorkflow {
			t.Fatalf("other-source refresh failure left workflow ToolDef exposed: %+v", candidate)
		}
	}
	if _, err := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: "workflow:verify", CatalogRevision: grant.CatalogRevision,
		ToolID: grant.ToolID, Arguments: arguments,
	}); !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("old workflow capability remained valid after other-source failure: %v", err)
	}
}

func TestAgentExecutionCoreSkillActivationUsesProjectApproval(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	skills := NewSkillsService(t.TempDir())
	skills.setWorkspaceRoot(root)
	skillDir := filepath.Join(root, ".koyori-ide", "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	definition := "id: guarded\nname: Guarded\ndescription: Project skill\ntrigger:\n  manual: true\nsystemPrompt: Review carefully.\nallowedTools: [read, search]\n"
	if err := os.WriteFile(filepath.Join(skillDir, "guarded.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := skills.Load(); err != nil {
		t.Fatalf("load skills: %v", err)
	}
	if err := WireAgentExecutionCore(agent, nil, nil, nil, skills, nil); err != nil {
		t.Fatalf("wire skills: %v", err)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	approvalCalls := 0
	deps.approveSkill = func(skill Skill) bool {
		approvalCalls++
		return skill.ID == "guarded"
	}
	deps.mu.Unlock()

	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var skillTool *AgentToolDefinition
	for index := range catalog.Tools {
		tool := &catalog.Tools[index]
		if tool.Source == "skill" && tool.Metadata["skillId"] == "guarded" {
			skillTool = tool
			break
		}
	}
	if skillTool == nil {
		t.Fatalf("skill activation ToolDef missing: %+v", catalog.Tools)
	}
	if skills.IsApproved("guarded") {
		t.Fatal("project skill unexpectedly approved before capability execution")
	}
	if _, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat:skill", CatalogRevision: catalog.Revision,
		ToolID: skillTool.ID, Arguments: map[string]interface{}{},
	}); err != nil {
		t.Fatalf("execute skill activation: %v", err)
	}
	if approvalCalls != 1 || !skills.IsApproved("guarded") {
		t.Fatalf("approvalCalls=%d approved=%v", approvalCalls, skills.IsApproved("guarded"))
	}
}

func TestAgentExecutionCoreSkillActivationCompensatesLedgerFailure(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	skills := NewSkillsService(t.TempDir())
	skills.setWorkspaceRoot(root)
	skillDir := filepath.Join(root, ".koyori-ide", "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	definition := "id: reversible\nname: Reversible\ndescription: Project skill\ntrigger:\n  manual: true\nsystemPrompt: Review carefully.\nallowedTools: [read]\n"
	if err := os.WriteFile(filepath.Join(skillDir, "reversible.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := skills.Load(); err != nil {
		t.Fatalf("load skills: %v", err)
	}
	if err := WireAgentExecutionCore(agent, nil, nil, nil, skills, nil); err != nil {
		t.Fatalf("wire skills: %v", err)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.approveSkill = func(skill Skill) bool { return skill.ID == "reversible" }
	deps.mu.Unlock()
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	meter := &externalCompletionFailMeter{completeErr: errors.New("skill terminal usage failed")}
	runtime.SetUsageSink(meter)
	runtime.SetUsageRequirements(true, true)
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	var activationID string
	for _, tool := range catalog.Tools {
		if tool.Source == string(agentcore.SourceSkill) && tool.Metadata["skillId"] == "reversible" {
			activationID = tool.ID
			break
		}
	}
	if activationID == "" {
		t.Fatalf("skill activation ToolDef missing: %+v", catalog.Tools)
	}
	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat:skill", CatalogRevision: catalog.Revision,
		ToolID: activationID, Arguments: map[string]interface{}{},
	})
	if err == nil || !strings.Contains(err.Error(), meter.completeErr.Error()) || errors.Is(err, agentcore.ErrExternalMutationIrreversible) {
		t.Fatalf("ExecuteAgentTool error = %v, want reversible ledger failure", err)
	}
	if skills.IsApproved("reversible") {
		t.Fatal("failed activation left project approval enabled")
	}
	if _, bound := agentSkillBindingSnapshot(agent, "chat:skill", "reversible"); bound {
		t.Fatal("failed activation left a session skill binding")
	}
	if result.Usage.ExternalReceiptID == "" || !result.Usage.ExternalReceiptReversible || result.Usage.ExternalCompensation != agentcore.ExternalCompensationSucceeded || result.Usage.Success {
		t.Fatalf("skill external usage = %+v", result.Usage)
	}
	if len(meter.begun) != 1 || meter.begun[0].ExternalCompensation != agentcore.ExternalCompensationPending || len(meter.completed) != 0 {
		t.Fatalf("skill receipt ledger begun=%+v completed=%+v", meter.begun, meter.completed)
	}
}

func TestAgentExecutionCoreWorkflowSkillActivationCompensatesLedgerFailure(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	skills := &SkillsService{skills: []Skill{{
		ID: "guarded", Name: "Guarded", Description: "Project workflow skill",
		Scope: SkillScopeProject, AllowedTools: []string{"read"},
	}}}
	if err := WireAgentExecutionCore(agent, file, search, nil, skills, nil); err != nil {
		t.Fatalf("WireAgentExecutionCore Skill: %v", err)
	}
	approvalCalls := 0
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.approveSkill = func(skill Skill) bool {
		approvalCalls++
		return skill.ID == "guarded"
	}
	deps.mu.Unlock()

	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	if err := workflow.CreateWorkflow(root, "skill-compensation", &WorkflowDef{
		Name: "skill-compensation",
		Steps: []WorkflowStep{{
			Name: "activate", Type: WorkflowStepSkill, Tool: "activate",
			Input: map[string]interface{}{"id": "guarded"},
		}},
	}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	meter := &externalCompletionFailMeter{completeErr: errors.New("workflow skill terminal usage failed")}
	runtime.SetUsageSink(meter)
	runtime.SetUsageRequirements(true, true)
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var activation *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) &&
			candidate.Metadata["workflow"] == "skill-compensation" && candidate.Metadata["step"] == "activate" {
			activation = candidate
			break
		}
	}
	if activation == nil {
		t.Fatalf("workflow Skill ToolDef missing: %+v", catalog.Tools)
	}
	if activation.Metadata["adapter"] != workflowAdapterSkillActivate ||
		activation.Metadata["skillId"] != "guarded" || activation.Metadata["scope"] != string(SkillScopeProject) ||
		len(activation.Metadata["fingerprint"]) != sha256.Size*2 || activation.Mutation != string(agentcore.MutationExternal) {
		t.Fatalf("workflow Skill ToolDef = %+v", activation)
	}

	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:metered", CatalogRevision: catalog.Revision,
		ToolID: activation.ID, Arguments: map[string]interface{}{},
	})
	if err == nil || !strings.Contains(err.Error(), meter.completeErr.Error()) || errors.Is(err, agentcore.ErrExternalMutationIrreversible) {
		t.Fatalf("ExecuteAgentTool error = %v, want reversible workflow Skill ledger failure", err)
	}
	if approvalCalls != 1 || skills.IsApproved("guarded") {
		t.Fatalf("workflow Skill approval calls=%d approved=%v", approvalCalls, skills.IsApproved("guarded"))
	}
	if _, bound := agentSkillBindingSnapshot(agent, "workflow:metered", "guarded"); bound {
		t.Fatal("failed workflow Skill activation left a session binding")
	}
	if result.Usage.ExternalReceiptID == "" || !result.Usage.ExternalReceiptReversible ||
		result.Usage.ExternalCompensation != agentcore.ExternalCompensationSucceeded || result.Usage.Success {
		t.Fatalf("workflow Skill external usage = %+v", result.Usage)
	}
}

func TestAgentExecutionCoreSkillLoadRefreshesRegistryAndInvalidatesCapabilities(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	skills := NewSkillsService(t.TempDir())
	skills.setWorkspaceRoot(root)
	skillDir := filepath.Join(root, ".koyori-ide", "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	skillPath := filepath.Join(skillDir, "mutable.yaml")
	definition := "id: mutable\nname: Mutable\ndescription: before\ntrigger:\n  manual: true\nsystemPrompt: Inspect.\n"
	if err := os.WriteFile(skillPath, []byte(definition), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := skills.Load(); err != nil {
		t.Fatalf("initial skill load: %v", err)
	}
	if err := WireAgentExecutionCore(agent, nil, nil, nil, skills, nil); err != nil {
		t.Fatalf("wire skills: %v", err)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.approveSkill = func(skill Skill) bool { return skill.ID == "mutable" }
	deps.mu.Unlock()
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	before := runtime.Registry().Snapshot()
	var skillTool *agentcore.ToolDef
	for index := range before.Tools {
		candidate := &before.Tools[index]
		if candidate.Source == agentcore.SourceSkill && candidate.Metadata["skillId"] == "mutable" {
			skillTool = candidate
			break
		}
	}
	if skillTool == nil {
		t.Fatalf("mutable skill ToolDef missing: %+v", before.Tools)
	}
	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat:skill", CatalogRevision: before.Revision,
		ToolID: skillTool.ID, Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("issue skill capability: %v", err)
	}
	updated := "id: mutable\nname: Mutable\ndescription: after\ntrigger:\n  manual: true\nsystemPrompt: Inspect.\n"
	if err := os.WriteFile(skillPath, []byte(updated), 0o600); err != nil {
		t.Fatalf("update skill: %v", err)
	}
	if err := skills.Load(); err != nil {
		t.Fatalf("reload skill: %v", err)
	}
	after := runtime.Registry().Snapshot()
	if after.Revision == before.Revision {
		t.Fatalf("skill reload did not refresh registry revision: before=%d after=%d", before.Revision, after.Revision)
	}
	if _, err := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: "chat:skill", CatalogRevision: grant.CatalogRevision,
		ToolID: grant.ToolID, Arguments: map[string]interface{}{},
	}); !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("old skill capability remained valid after reload: %v", err)
	}
}

func TestAgentExecutionCoreWorkspaceSkillLoadFailureFailsClosed(t *testing.T) {
	agent, _, _, rootA := newExecutionCoreTestServices(t)
	skills := NewSkillsService(t.TempDir())
	skills.setWorkspaceRoot(rootA)
	skillDir := filepath.Join(rootA, ".koyori-ide", "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("create workspace A skill dir: %v", err)
	}
	definition := "id: workspace-a\nname: Workspace A\ndescription: Must not cross workspaces\ntrigger:\n  manual: true\nsystemPrompt: Workspace A only.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "workspace-a.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write workspace A skill: %v", err)
	}
	if err := skills.Load(); err != nil {
		t.Fatalf("load workspace A skills: %v", err)
	}
	agent.setSkillsService(skills)
	if err := WireAgentExecutionCore(agent, nil, nil, nil, skills, nil); err != nil {
		t.Fatalf("wire workspace skills: %v", err)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	approvalCalls := 0
	deps.approveSkill = func(skill Skill) bool {
		approvalCalls++
		return skill.ID == "workspace-a"
	}
	deps.mu.Unlock()

	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog workspace A: %v", err)
	}
	var activationID string
	for _, tool := range catalog.Tools {
		if tool.Source == string(agentcore.SourceSkill) && tool.Metadata["skillId"] == "workspace-a" {
			activationID = tool.ID
			break
		}
	}
	if activationID == "" {
		t.Fatalf("workspace A skill ToolDef missing: %+v", catalog.Tools)
	}
	if _, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat:skill", CatalogRevision: catalog.Revision,
		ToolID: activationID, Arguments: map[string]interface{}{},
	}); err != nil {
		t.Fatalf("activate workspace A skill: %v", err)
	}
	if approvalCalls != 1 || !skills.IsApproved("workspace-a") {
		t.Fatalf("workspace A approvalCalls=%d approved=%v", approvalCalls, skills.IsApproved("workspace-a"))
	}
	if _, bound := agentSkillBindingSnapshot(agent, "chat:skill", "workspace-a"); !bound {
		t.Fatal("workspace A skill was not bound to its session")
	}

	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat:skill", CatalogRevision: catalog.Revision,
		ToolID: activationID, Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("issue workspace A capability: %v", err)
	}

	rootB := t.TempDir()
	metadataDir := filepath.Join(rootB, ".koyori-ide")
	if err := os.MkdirAll(metadataDir, 0o700); err != nil {
		t.Fatalf("create workspace B metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "skills"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create invalid workspace B skill source: %v", err)
	}
	if err := agent.setWorkspaceRoot(rootB); err != nil {
		t.Fatalf("switch to workspace B: %v", err)
	}

	if skills.IsApproved("workspace-a") {
		t.Fatal("workspace A project approval survived the workspace switch")
	}
	if _, err := skills.GetSkill("workspace-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("workspace A skill survived failed workspace B load: %v", err)
	}
	if _, bound := agentSkillBindingSnapshot(agent, "chat:skill", "workspace-a"); bound {
		t.Fatal("workspace A session binding survived the workspace switch")
	}
	if _, err := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: "chat:skill", CatalogRevision: grant.CatalogRevision,
		ToolID: grant.ToolID, Arguments: map[string]interface{}{},
	}); !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("workspace A capability survived the workspace switch: %v", err)
	}

	after, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog workspace B: %v", err)
	}
	for _, tool := range after.Tools {
		if tool.Source == string(agentcore.SourceSkill) && tool.Metadata["skillId"] == "workspace-a" {
			t.Fatalf("workspace A ToolDef was republished in workspace B: %+v", tool)
		}
	}
	if err := agent.registerAgentSession("chat:workspace-b"); err != nil {
		t.Fatalf("register workspace B session: %v", err)
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat:workspace-b", CatalogRevision: after.Revision,
		ToolID: activationID, Arguments: map[string]interface{}{},
	}); err == nil {
		t.Fatal("workspace B issued a capability for workspace A skill")
	}
	if approvalCalls != 1 {
		t.Fatalf("workspace B reused workspace A approval: approvalCalls=%d", approvalCalls)
	}
}

func TestSkillsServicePublicActivationIsDenyOnly(t *testing.T) {
	skills := &SkillsService{skills: []Skill{{ID: "project", Scope: SkillScopeProject}}}
	if err := skills.ActivateSkill("project"); err == nil {
		t.Fatal("public ActivateSkill bypassed the unified agent capability pipeline")
	}
	if skills.IsApproved("project") {
		t.Fatal("deny-only public activation changed project approval state")
	}
}

func TestAgentExecutionCoreSkillAllowlistIsSessionBoundAndFailClosed(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	skills := NewSkillsService(t.TempDir())
	skills.setWorkspaceRoot(root)
	skillDir := filepath.Join(root, ".koyori-ide", "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	definition := "id: readonly\nname: Read only\ndescription: Restrict this session\ntrigger:\n  manual: true\nsystemPrompt: Inspect without mutation.\nallowedTools: [read, search]\n"
	if err := os.WriteFile(filepath.Join(skillDir, "readonly.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := skills.Load(); err != nil {
		t.Fatalf("load skills: %v", err)
	}
	if err := WireAgentExecutionCore(agent, file, search, nil, skills, nil); err != nil {
		t.Fatalf("wire skills: %v", err)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.approveSkill = func(skill Skill) bool { return skill.ID == "readonly" }
	deps.mu.Unlock()
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	var activationID string
	for _, tool := range catalog.Tools {
		if tool.Source == string(agentcore.SourceSkill) && tool.Metadata["skillId"] == "readonly" {
			activationID = tool.ID
			break
		}
	}
	if activationID == "" {
		t.Fatalf("skill activation ToolDef missing: %+v", catalog.Tools)
	}
	const sessionID = "chat:readonly"
	if _, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: activationID, Arguments: map[string]interface{}{},
	}); err != nil {
		t.Fatalf("activate skill: %v", err)
	}

	for _, request := range []AgentToolExecutionRequest{
		{SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "read", Arguments: map[string]interface{}{"path": "missing.txt"}},
		{SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "search", Arguments: map[string]interface{}{"query": "needle", "ignoreCase": true}},
	} {
		_, executeErr := agent.ExecuteAgentTool(context.Background(), request)
		if request.ToolID == "read" && executeErr == nil {
			t.Fatal("read of a missing file unexpectedly succeeded")
		}
		if request.ToolID == "read" && strings.Contains(executeErr.Error(), "allowlist") {
			t.Fatalf("allowed read was rejected by skill policy: %v", executeErr)
		}
		if request.ToolID == "search" && executeErr != nil {
			t.Fatalf("allowed search was rejected: %v", executeErr)
		}
	}

	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "run", Arguments: map[string]interface{}{"command": "go version"},
	}); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("run outside skill allowlist = %v", err)
	}

	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	mcpCatalog, err := runtime.Registry().ReplaceSource(agentcore.SourceMCP, []agentcore.ToolDef{{
		ID: "mcp.server.echo", Description: "Echo", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Source: agentcore.SourceMCP, Risk: agentcore.RiskElevated, Approval: agentcore.ApprovalManual,
		Mutation: agentcore.MutationExternal, ExecuteKey: "test.mcp.echo",
	}})
	if err != nil {
		t.Fatalf("publish MCP fixture: %v", err)
	}
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: mcpCatalog.Revision,
		ToolID: "mcp.server.echo", Arguments: map[string]interface{}{},
	}); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("MCP outside skill allowlist = %v", err)
	}
	// A renderer must not create a fresh ID to escape the readonly allowlist.
	// Only a backend-issued/registered session may reach capability issuance.
	if _, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat:fresh-unregistered", CatalogRevision: mcpCatalog.Revision,
		ToolID: "run", Arguments: map[string]interface{}{"command": "go version"},
	}); !errors.Is(err, agentcore.ErrUnknownSession) {
		t.Fatalf("fresh unregistered session issuance = %v, want ErrUnknownSession", err)
	}

	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: mcpCatalog.Revision,
		ToolID: "search", Arguments: map[string]interface{}{"query": "needle", "ignoreCase": true},
	})
	if err != nil {
		t.Fatalf("issue allowed cross-session probe: %v", err)
	}
	_, err = agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: "chat:other", CatalogRevision: mcpCatalog.Revision,
		ToolID: "search", Arguments: map[string]interface{}{"query": "needle", "ignoreCase": true},
	})
	if !errors.Is(err, agentcore.ErrInvalidCapability) {
		t.Fatalf("cross-session capability = %v, want ErrInvalidCapability", err)
	}
}

func TestAgentExecutionCoreRejectsUnapprovedSkillSessionBinding(t *testing.T) {
	agent, file, search, _ := newExecutionCoreTestServices(t)
	skills := &SkillsService{skills: []Skill{{ID: "pending", Name: "Pending", Scope: SkillScopeProject, AllowedTools: []string{"read"}}}}
	if err := WireAgentExecutionCore(agent, file, search, nil, skills, nil); err != nil {
		t.Fatalf("wire skills: %v", err)
	}
	skill, err := skills.GetSkill("pending")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	fingerprint, err := skillFingerprint(skill)
	if err != nil {
		t.Fatalf("skillFingerprint: %v", err)
	}
	if err := bindAgentSkillSession(agent, "chat:pending", skill, fingerprint); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("unapproved skill binding = %v, want ErrNotAllowed", err)
	}
}
