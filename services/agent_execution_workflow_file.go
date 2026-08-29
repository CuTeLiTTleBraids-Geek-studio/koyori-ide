package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

const workflowFileWriteInputSchema = `{"type":"object","properties":{},"additionalProperties":false}`

func workflowFileWriteInput(step WorkflowStep) (string, string, error) {
	if !workflowFileWriteInputIsValid(step) {
		return "", "", fmt.Errorf("file write workflow input is invalid: %w", ErrInvalidInput)
	}
	pathValue, _ := step.Input["path"].(string)
	content, _ := step.Input["content"].(string)
	return pathValue, content, nil
}

func hashWorkflowFileContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func workflowFileWriteToolDef(workflowName string, step WorkflowStep) (agentcore.ToolDef, error) {
	pathValue, content, err := workflowFileWriteInput(step)
	if err != nil {
		return agentcore.ToolDef{}, err
	}
	return agentcore.ToolDef{
		ID:          dynamicAgentToolID("workflow-file", workflowName, step.Name),
		Description: fmt.Sprintf("Write the backend-owned file content for workflow %q step %q.", workflowName, step.Name),
		InputSchema: json.RawMessage(workflowFileWriteInputSchema),
		Source:      agentcore.SourceWorkflow,
		Risk:        agentcore.RiskElevated,
		Approval:    agentcore.ApprovalManual,
		Mutation:    agentcore.MutationWorkspaceTransaction,
		ExecuteKey:  "workflow.file.write",
		Metadata: map[string]string{
			"workflow":     workflowName,
			"step":         step.Name,
			"adapter":      workflowAdapterFileWrite,
			"path":         pathValue,
			"contentHash":  hashWorkflowFileContent(content),
			"contentBytes": strconv.Itoa(len([]byte(content))),
		},
	}, nil
}

type preparedWorkflowFileWrite struct {
	Path         string                      `json:"path"`
	ContentHash  string                      `json:"contentHash"`
	ContentBytes int                         `json:"contentBytes"`
	Delegated    agentcore.PreparedExecution `json:"delegated"`
}

type agentWorkflowFileWriteHandler struct{ agent *AgentService }

var _ agentcore.WorkspaceTransactionHandler = (*agentWorkflowFileWriteHandler)(nil)

func (*agentWorkflowFileWriteHandler) MutationMode() agentcore.MutationMode {
	return agentcore.MutationWorkspaceTransaction
}

func workflowWriteInvocation(base agentcore.Invocation, path, content string) (agentcore.Invocation, error) {
	arguments, err := json.Marshal(map[string]interface{}{"path": path, "content": content})
	if err != nil {
		return agentcore.Invocation{}, fmt.Errorf("encode workflow file write arguments: %w", err)
	}
	base.Tool = agentcore.ToolDef{
		ID:          "write",
		Description: "Write complete text content through the workspace edit transaction.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`),
		Source:      agentcore.SourceBuiltin,
		Risk:        agentcore.RiskElevated,
		Approval:    agentcore.ApprovalManual,
		Mutation:    agentcore.MutationWorkspaceTransaction,
		ExecuteKey:  "builtin.write",
	}
	base.Arguments = arguments
	return base, nil
}

func (h *agentWorkflowFileWriteHandler) Prepare(ctx context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	if h == nil || h.agent == nil || invocation.Tool.Metadata["adapter"] != workflowAdapterFileWrite {
		return agentcore.PreparedExecution{}, fmt.Errorf("workflow file write adapter metadata is invalid: %w", ErrNotAllowed)
	}
	var request map[string]interface{}
	if err := json.Unmarshal(invocation.Arguments, &request); err != nil || len(request) != 0 || string(invocation.Arguments) != "{}" {
		return agentcore.PreparedExecution{}, fmt.Errorf("workflow file write capability arguments must be empty: %w", agentcore.ErrInvalidArguments)
	}
	step, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	pathValue, content, err := workflowFileWriteInput(step)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	delegated, err := workflowWriteInvocation(invocation, pathValue, content)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	prepared, err := (&agentWriteHandler{agent: h.agent}).Prepare(ctx, delegated)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	state := preparedWorkflowFileWrite{
		Path: pathValue, ContentHash: hashWorkflowFileContent(content),
		ContentBytes: len([]byte(content)), Delegated: prepared,
	}
	opaque, err := json.Marshal(state)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	return agentcore.PreparedExecution{
		Summary: fmt.Sprintf("Write workflow file %s", pathValue),
		Opaque:  opaque,
		Metadata: map[string]string{
			"workflow": invocation.Tool.Metadata["workflow"],
			"step":     invocation.Tool.Metadata["step"],
			"path":     pathValue,
		},
	}, nil
}

func (h *agentWorkflowFileWriteHandler) Execute(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	return h.ExecuteWorkspaceTransaction(ctx, invocation, prepared)
}

func (h *agentWorkflowFileWriteHandler) ExecuteWorkspaceTransaction(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	if h == nil || h.agent == nil {
		return agentcore.ExecutionOutput{}, fmt.Errorf("AgentService is required for workflow file write: %w", ErrNotAllowed)
	}
	step, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	pathValue, content, err := workflowFileWriteInput(step)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	var state preparedWorkflowFileWrite
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if state.Path != pathValue || state.ContentHash != hashWorkflowFileContent(content) || state.ContentBytes != len([]byte(content)) {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workflow file write source changed after approval: %w", ErrNotAllowed)
	}
	delegated, err := workflowWriteInvocation(invocation, pathValue, content)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	return (&agentWriteHandler{agent: h.agent}).ExecuteWorkspaceTransaction(ctx, delegated, state.Delegated)
}
