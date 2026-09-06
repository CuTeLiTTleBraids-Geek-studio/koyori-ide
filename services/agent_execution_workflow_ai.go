package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

type preparedWorkflowAI struct {
	Workflow                  string `json:"workflow"`
	Step                      string `json:"step"`
	Operation                 string `json:"operation"`
	Prompt                    string `json:"prompt"`
	PromptHash                string `json:"promptHash"`
	ConfigFingerprint         string `json:"configFingerprint"`
	FallbackConfigFingerprint string `json:"fallbackConfigFingerprint"`
	RootGeneration            uint64 `json:"rootGeneration"`
}

type agentWorkflowAIHandler struct{ agent *AgentService }

var _ agentcore.ExternalMutationTransactionHandler = (*agentWorkflowAIHandler)(nil)

func (*agentWorkflowAIHandler) MutationMode() agentcore.MutationMode {
	return agentcore.MutationExternal
}

func (h *agentWorkflowAIHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	if invocation.Tool.Metadata["adapter"] != workflowAdapterAI || invocation.Tool.Metadata["workflow"] == "" || invocation.Tool.Metadata["step"] == "" {
		return agentcore.PreparedExecution{}, fmt.Errorf("workflow AI adapter metadata is invalid: %w", ErrNotAllowed)
	}
	step, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	operation, prompt, err := workflowAIInput(step)
	if err != nil || operation != invocation.Tool.Metadata["operation"] || hashWorkflowAIPrompt(prompt) != invocation.Tool.Metadata["promptHash"] {
		return agentcore.PreparedExecution{}, fmt.Errorf("workflow AI source identity is invalid: %w", ErrNotAllowed)
	}
	ai, err := workflowAIService(h.agent)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	config, fallback, err := ai.resolveAgentOperation(AIOperation(operation))
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	state := preparedWorkflowAI{
		Workflow: invocation.Tool.Metadata["workflow"], Step: invocation.Tool.Metadata["step"],
		Operation: operation, Prompt: prompt, PromptHash: hashWorkflowAIPrompt(prompt),
		ConfigFingerprint:         aiConfigFingerprint(config),
		FallbackConfigFingerprint: optionalAIConfigFingerprint(fallback),
		RootGeneration:            h.agent.agentWorkspaceGeneration(),
	}
	opaque, err := json.Marshal(state)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	return agentcore.PreparedExecution{
		Summary: fmt.Sprintf("Run workflow AI operation %q.", operation), Opaque: opaque,
	}, nil
}

func (h *agentWorkflowAIHandler) Execute(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	output, _, err := h.ExecuteExternalTransaction(ctx, invocation, prepared)
	return output, err
}

func (h *agentWorkflowAIHandler) BeginExternalMutation(_ context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExternalMutationReceipt, error) {
	state, ai, err := h.validatePrepared(invocation, prepared)
	if err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	if _, _, err := resolveApprovedWorkflowAIConfigs(ai, state, "after approval"); err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	return newAgentExternalMutationReceipt("ai", false, map[string]string{
		"workflow": state.Workflow, "step": state.Step, "operation": state.Operation,
		"configFingerprint": state.ConfigFingerprint, "fallbackConfigFingerprint": state.FallbackConfigFingerprint,
	})
}

func (h *agentWorkflowAIHandler) ExecuteExternalTransaction(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, agentcore.ExternalMutationReceipt, error) {
	receipt, err := h.BeginExternalMutation(ctx, invocation, prepared)
	if err != nil {
		return agentcore.ExecutionOutput{}, agentcore.ExternalMutationReceipt{}, err
	}
	output, executeErr := h.ExecuteExternalTransactionWithReceipt(ctx, invocation, prepared, receipt)
	return output, receipt, executeErr
}

func (h *agentWorkflowAIHandler) ExecuteExternalTransactionWithReceipt(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) (agentcore.ExecutionOutput, error) {
	state, ai, err := h.validatePrepared(invocation, prepared)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	receiptFallbackFingerprint, receiptHasFallbackFingerprint := receipt.Metadata["fallbackConfigFingerprint"]
	if receipt.ID == "" || receipt.Reversible || receipt.Metadata["workflow"] != state.Workflow ||
		receipt.Metadata["step"] != state.Step || receipt.Metadata["operation"] != state.Operation ||
		receipt.Metadata["configFingerprint"] != state.ConfigFingerprint ||
		!receiptHasFallbackFingerprint || receiptFallbackFingerprint != state.FallbackConfigFingerprint {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workflow AI execution requires its preallocated receipt: %w", agentcore.ErrExternalMutationContract)
	}
	config, fallback, err := resolveApprovedWorkflowAIConfigs(ai, state, "during execution")
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	response, usedConfig, err := sendResolvedAgentOperation(
		ctx,
		config,
		fallback,
		[]ChatMessage{{Role: "user", Content: state.Prompt}},
		ai.sendWithAgentConfig,
	)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if response == nil {
		return agentcore.ExecutionOutput{}, fmt.Errorf("AI provider returned no response: %w", ErrNotAllowed)
	}
	basis := agentcore.CostEstimated
	estimated := true
	if response.usageReported {
		basis = agentcore.CostProviderReported
		estimated = false
	}
	return agentcore.ExecutionOutput{
		Observation: boundAgentObservation(response.Content),
		Usage: &agentcore.UsageRecord{
			ProviderID: usageProviderID(usedConfig), Model: usedConfig.Model,
			TokensIn: response.tokensIn, TokensOut: response.tokensOut,
			CostBasis: basis, Estimated: estimated,
		},
	}, nil
}

func (h *agentWorkflowAIHandler) CompensateExternalTransaction(_ context.Context, _ agentcore.Invocation, _ agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) error {
	if receipt.ID == "" || receipt.Reversible {
		return fmt.Errorf("workflow AI receipt is not an irreversible provider receipt: %w", agentcore.ErrExternalMutationContract)
	}
	return fmt.Errorf("AI provider requests cannot be compensated: %w", agentcore.ErrExternalMutationIrreversible)
}

func (h *agentWorkflowAIHandler) validatePrepared(invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (preparedWorkflowAI, *AIService, error) {
	var state preparedWorkflowAI
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return preparedWorkflowAI{}, nil, err
	}
	if state.Workflow == "" || state.Step == "" || state.Operation == "" || state.Prompt == "" ||
		state.PromptHash != hashWorkflowAIPrompt(state.Prompt) || state.RootGeneration == 0 ||
		state.RootGeneration != h.agent.agentWorkspaceGeneration() ||
		invocation.Tool.Metadata["workflow"] != state.Workflow || invocation.Tool.Metadata["step"] != state.Step ||
		invocation.Tool.Metadata["operation"] != state.Operation || invocation.Tool.Metadata["promptHash"] != state.PromptHash {
		return preparedWorkflowAI{}, nil, fmt.Errorf("workflow AI prepared identity is invalid: %w", ErrNotAllowed)
	}
	if _, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata); err != nil {
		return preparedWorkflowAI{}, nil, err
	}
	ai, err := workflowAIService(h.agent)
	if err != nil {
		return preparedWorkflowAI{}, nil, err
	}
	return state, ai, nil
}

func workflowAIService(agent *AgentService) (*AIService, error) {
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	ai := deps.ai
	deps.mu.RUnlock()
	if ai == nil {
		return nil, fmt.Errorf("AI service is not wired: %w", ErrNotAllowed)
	}
	return ai, nil
}

func resolveApprovedWorkflowAIConfigs(ai *AIService, state preparedWorkflowAI, phase string) (AIConfig, *AIConfig, error) {
	primary, fallback, err := ai.resolveAgentOperation(AIOperation(state.Operation))
	if err != nil {
		return AIConfig{}, nil, err
	}
	if aiConfigFingerprint(primary) != state.ConfigFingerprint ||
		optionalAIConfigFingerprint(fallback) != state.FallbackConfigFingerprint {
		return AIConfig{}, nil, fmt.Errorf("AI provider changed %s: %w", phase, ErrNotAllowed)
	}
	return primary, fallback, nil
}

func isWorkflowAIOperation(value string) bool {
	_, ok := workflowAIOperations[strings.TrimSpace(value)]
	return ok
}
