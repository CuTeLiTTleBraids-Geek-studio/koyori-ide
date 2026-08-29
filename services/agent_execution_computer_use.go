package services

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"strconv"
	"strings"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

type agentComputerUseHandler struct {
	agent  *AgentService
	action string
}

func (s *AgentService) approveComputerUseAgentTool(request agentcore.ApprovalRequest, prompt ...bool) (bool, error) {
	deps := executionDependenciesFor(s)
	deps.mu.RLock()
	cu := deps.computerUse
	deps.mu.RUnlock()
	if cu == nil || !cu.IsEnabled() {
		return false, fmt.Errorf("computer use is disabled: %w", ErrNotAllowed)
	}
	showPrompt := len(prompt) == 0 || prompt[0]
	if !showPrompt {
		return true, nil
	}
	action := strings.TrimPrefix(request.Invocation.Tool.ID, "computer.")
	details, err := computerUseApprovalDetails(action, request.Invocation.Arguments)
	if err != nil {
		return false, err
	}
	return cu.confirmOperation(action, details), nil
}

func computerUseAgentTools(cu *ComputerUseService) []agentcore.ToolDef {
	if cu == nil || !cu.IsEnabled() {
		return nil
	}
	return []agentcore.ToolDef{
		{
			ID: "computer.screenshot", Description: "Capture a PNG screenshot of the desktop or a region. Requires Computer Use opt-in and approval.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"},"y":{"type":"integer"},"w":{"type":"integer"},"h":{"type":"integer"}},"additionalProperties":false}`),
			Source:      agentcore.SourceComputerUse, Risk: agentcore.RiskDangerous,
			Approval: agentcore.ApprovalManual, Mutation: agentcore.MutationExternal, ExecuteKey: "computer.screenshot",
		},
		{
			ID: "computer.mouse_move", Description: "Move the mouse to screen coordinates. Requires Computer Use opt-in and approval.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"},"y":{"type":"integer"}},"required":["x","y"],"additionalProperties":false}`),
			Source:      agentcore.SourceComputerUse, Risk: agentcore.RiskDangerous,
			Approval: agentcore.ApprovalManual, Mutation: agentcore.MutationExternal, ExecuteKey: "computer.mouse_move",
		},
		{
			ID: "computer.keyboard_type", Description: "Type unicode text. Requires Computer Use opt-in and approval.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","minLength":1}},"required":["text"],"additionalProperties":false}`),
			Source:      agentcore.SourceComputerUse, Risk: agentcore.RiskDangerous,
			Approval: agentcore.ApprovalManual, Mutation: agentcore.MutationExternal, ExecuteKey: "computer.keyboard_type",
		},
	}
}

func WireAgentComputerUse(agent *AgentService, cu *ComputerUseService) error {
	if agent == nil {
		return fmt.Errorf("agent service is required: %w", ErrInvalidInput)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.computerUse = cu
	deps.mu.Unlock()
	runtime, err := agent.coreRuntime()
	if err != nil {
		return err
	}
	if err := runtime.RegisterHandler("computer.screenshot", &agentComputerUseHandler{agent: agent, action: "screenshot"}); err != nil {
		return err
	}
	if err := runtime.RegisterHandler("computer.mouse_move", &agentComputerUseHandler{agent: agent, action: "mouse_move"}); err != nil {
		return err
	}
	if err := runtime.RegisterHandler("computer.keyboard_type", &agentComputerUseHandler{agent: agent, action: "keyboard_type"}); err != nil {
		return err
	}
	return agent.refreshDynamicAgentTools(context.Background())
}

func (*agentComputerUseHandler) MutationMode() agentcore.MutationMode {
	return agentcore.MutationExternal
}

func (h *agentComputerUseHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	if _, err := computerUseApprovalDetails(h.action, invocation.Arguments); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	return agentcore.PreparedExecution{Summary: "Computer Use " + h.action, Opaque: invocation.Arguments}, nil
}

func computerUseApprovalDetails(action string, raw json.RawMessage) (string, error) {
	var args map[string]interface{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", err
		}
	}
	switch action {
	case "screenshot":
		x, _ := args["x"].(float64)
		y, _ := args["y"].(float64)
		w, _ := args["w"].(float64)
		h, _ := args["h"].(float64)
		if w > 0 && h > 0 {
			encoded, err := json.Marshal(screenshotOperationDetails{Region: &image.Rectangle{
				Min: image.Point{X: int(x), Y: int(y)},
				Max: image.Point{X: int(x + w), Y: int(y + h)},
			}})
			return string(encoded), err
		}
		return `{"region":null}`, nil
	case "mouse_move":
		encoded, err := json.Marshal(mouseMoveOperationDetails{X: int(args["x"].(float64)), Y: int(args["y"].(float64))})
		return string(encoded), err
	case "keyboard_type":
		text, _ := args["text"].(string)
		encoded, err := json.Marshal(keyboardTypeOperationDetails{Text: text})
		return string(encoded), err
	default:
		return string(raw), nil
	}
}

func (h *agentComputerUseHandler) Execute(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	output, _, err := h.ExecuteExternalTransaction(ctx, invocation, prepared)
	return output, err
}

func (h *agentComputerUseHandler) BeginExternalMutation(_ context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExternalMutationReceipt, error) {
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	cu := deps.computerUse
	deps.mu.RUnlock()
	if cu == nil || !cu.IsEnabled() {
		return agentcore.ExternalMutationReceipt{}, fmt.Errorf("computer use is disabled: %w", ErrNotAllowed)
	}
	if _, err := computerUseApprovalDetails(h.action, prepared.Opaque); err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	mode, err := (agentCoreApprover{agent: h.agent}).sessionMode(invocation.SessionID)
	if err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	return newAgentExternalMutationReceipt("computer-use", false, map[string]string{
		"action":          h.action,
		"confirmedByUser": strconv.FormatBool(mode != agentcore.SessionPermissionAllowAll),
	})
}

func (h *agentComputerUseHandler) ExecuteExternalTransaction(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, agentcore.ExternalMutationReceipt, error) {
	receipt, err := h.BeginExternalMutation(ctx, invocation, prepared)
	if err != nil {
		return agentcore.ExecutionOutput{}, agentcore.ExternalMutationReceipt{}, err
	}
	output, executeErr := h.ExecuteExternalTransactionWithReceipt(ctx, invocation, prepared, receipt)
	return output, receipt, executeErr
}

func (h *agentComputerUseHandler) ExecuteExternalTransactionWithReceipt(ctx context.Context, _ agentcore.Invocation, prepared agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) (agentcore.ExecutionOutput, error) {
	confirmedByUser, err := strconv.ParseBool(receipt.Metadata["confirmedByUser"])
	if err != nil || receipt.ID == "" || receipt.Reversible || receipt.Metadata["action"] != h.action {
		return agentcore.ExecutionOutput{}, fmt.Errorf("computer use requires its preallocated irreversible receipt: %w", agentcore.ErrExternalMutationContract)
	}
	details, err := computerUseApprovalDetails(h.action, prepared.Opaque)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	cu := deps.computerUse
	deps.mu.RUnlock()
	if cu == nil {
		return agentcore.ExecutionOutput{}, fmt.Errorf("computer use is unavailable: %w", ErrNotAllowed)
	}
	token, err := cu.requestOperationApproval(h.action, details, false, confirmedByUser)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	result, err := cu.ExecuteApprovedOperation(ctx, token)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	return agentcore.ExecutionOutput{Observation: boundAgentObservation(string(encoded))}, nil
}

func (*agentComputerUseHandler) CompensateExternalTransaction(_ context.Context, _ agentcore.Invocation, _ agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) error {
	if receipt.ID == "" || receipt.Reversible {
		return fmt.Errorf("computer use receipt is not an irreversible external receipt: %w", agentcore.ErrExternalMutationContract)
	}
	return fmt.Errorf("computer use receipt %q cannot be rolled back: %w", receipt.ID, agentcore.ErrExternalMutationIrreversible)
}
