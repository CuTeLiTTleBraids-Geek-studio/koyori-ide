package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

const defaultAgentCatalogMCPRefreshTimeout = 15 * time.Second

// refreshMCPAgentTools is the MCP/catalog boundary extracted from
// mcp_service.go. A refresh builds every ToolDef off to the side and atomically
// replaces SourceMCP. If one schema cannot be validated, the complete MCP
// source is removed before the error is returned so stale tools cannot remain
// executable.
func (s *AgentService) refreshMCPAgentTools(ctx context.Context, runtime *agentcore.Runtime, mcp mcpToolLister) error {
	definitions, err := s.buildMCPAgentTools(ctx, runtime, mcp)
	if err != nil {
		_, clearErr := runtime.Registry().ReplaceSource(agentcore.SourceMCP, nil)
		return errors.Join(err, clearErr)
	}
	_, err = runtime.Registry().ReplaceSource(agentcore.SourceMCP, definitions)
	return err
}

func (s *AgentService) buildMCPAgentTools(ctx context.Context, runtime *agentcore.Runtime, mcp mcpToolLister) ([]agentcore.ToolDef, error) {
	deps := executionDependenciesFor(s)
	deps.mu.Lock()
	refreshTimeout := deps.catalogMCPRefreshTimeout
	if refreshTimeout <= 0 {
		refreshTimeout = defaultAgentCatalogMCPRefreshTimeout
	}
	if !deps.mcpHandlerRegistered {
		if err := runtime.RegisterHandler("mcp.call", &agentMCPHandler{agent: s}); err != nil {
			deps.mu.Unlock()
			return nil, err
		}
		deps.mcpHandlerRegistered = true
	}
	deps.mu.Unlock()

	// Catalog refreshes serialize publication. Bound the only external I/O in
	// that critical section so an unresponsive MCP server cannot hold every
	// workflow, Skill, and catalog refresh indefinitely.
	refreshCtx, cancelRefresh := context.WithTimeout(ctx, refreshTimeout)
	defer cancelRefresh()
	tools, err := mcp.ListAgentMCPTools(refreshCtx)
	if err != nil {
		return nil, err
	}
	definitions := make([]agentcore.ToolDef, 0, len(tools))
	for _, tool := range tools {
		schema, schemaErr := normalizeMCPAgentSchema(tool.InputSchema)
		if schemaErr != nil {
			return nil, fmt.Errorf("MCP tool %q schema: %w", tool.Namespace, schemaErr)
		}
		risk := agentcore.RiskElevated
		if tool.RiskLevel == RiskDangerous {
			risk = agentcore.RiskDangerous
		}
		definitions = append(definitions, agentcore.ToolDef{
			ID: tool.Namespace, Description: tool.Description, InputSchema: schema,
			Source: agentcore.SourceMCP, Risk: risk, Approval: agentcore.ApprovalManual,
			// MCP servers are an external trust boundary. Even a tool named
			// "search" can perform side effects, so none is modeled as read-only.
			Mutation: agentcore.MutationExternal, ExecuteKey: "mcp.call",
			Metadata: map[string]string{"server": tool.Server, "tool": tool.Tool},
		})
	}
	return definitions, nil
}

// normalizeMCPAgentSchema closes object schemas recursively. Missing
// additionalProperties in JSON Schema means true; accepting that at an
// authorization boundary would allow argument substitution, so the adapter
// makes the provider definition explicit. An explicitly open schema is
// rejected rather than silently narrowed.
func normalizeMCPAgentSchema(input map[string]interface{}) (json.RawMessage, error) {
	if input == nil {
		input = map[string]interface{}{"type": "object"}
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode schema: %w", err)
	}
	var clone map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&clone); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	if len(clone) == 0 {
		clone["type"] = "object"
	}
	if err := closeMCPAgentSchema(clone, "inputSchema"); err != nil {
		return nil, err
	}
	closed, err := json.Marshal(clone)
	if err != nil {
		return nil, fmt.Errorf("encode closed schema: %w", err)
	}
	return closed, nil
}

func closeMCPAgentSchema(schema map[string]interface{}, path string) error {
	typeName, ok := schema["type"].(string)
	if !ok || typeName == "" {
		return fmt.Errorf("%s.type is required", path)
	}
	switch typeName {
	case "object":
		if rawAdditional, exists := schema["additionalProperties"]; exists {
			additional, ok := rawAdditional.(bool)
			if !ok || additional {
				return fmt.Errorf("%s.additionalProperties must be false", path)
			}
		} else {
			schema["additionalProperties"] = false
		}
		rawProperties, exists := schema["properties"]
		if !exists {
			schema["properties"] = map[string]interface{}{}
			return nil
		}
		properties, ok := rawProperties.(map[string]interface{})
		if !ok {
			return fmt.Errorf("%s.properties must be an object", path)
		}
		for name, rawProperty := range properties {
			property, ok := rawProperty.(map[string]interface{})
			if !ok {
				return fmt.Errorf("%s.properties.%s must be an object", path, name)
			}
			if err := closeMCPAgentSchema(property, path+".properties."+name); err != nil {
				return err
			}
		}
	case "array":
		items, ok := schema["items"].(map[string]interface{})
		if !ok {
			return fmt.Errorf("%s.items must be an object", path)
		}
		return closeMCPAgentSchema(items, path+".items")
	case "string", "boolean", "number", "integer", "null":
		return nil
	default:
		return fmt.Errorf("%s.type %q is unsupported", path, typeName)
	}
	return nil
}

func (s *AgentService) approveDynamicAgentTool(request agentcore.ApprovalRequest, prompt ...bool) (bool, error) {
	showPrompt := true
	if len(prompt) > 0 {
		showPrompt = prompt[0]
	}
	switch request.Invocation.Tool.Source {
	case agentcore.SourceWorkflow:
		return s.approveWorkflowAgentTool(request, showPrompt)
	case agentcore.SourceSkill:
		return s.approveSkillAgentTool(request, showPrompt)
	case agentcore.SourceComputerUse:
		return s.approveComputerUseAgentTool(request, showPrompt)
	case agentcore.SourceMCP:
		// Continue through the MCP-specific adapter below.
	default:
		return false, fmt.Errorf("tool source %q has no approval adapter: %w", request.Invocation.Tool.Source, ErrNotAllowed)
	}
	server := request.Invocation.Tool.Metadata["server"]
	tool := request.Invocation.Tool.Metadata["tool"]
	if server == "" || tool == "" {
		return false, fmt.Errorf("MCP ToolDef metadata is incomplete: %w", ErrNotAllowed)
	}
	deps := executionDependenciesFor(s)
	deps.mu.RLock()
	mcp := deps.mcp
	deps.mu.RUnlock()
	if mcp == nil {
		return false, fmt.Errorf("MCP approval service is unavailable: %w", ErrNotAllowed)
	}
	if !showPrompt {
		return true, nil
	}
	if mcp.approveTool == nil {
		return false, fmt.Errorf("MCP approval service is unavailable: %w", ErrNotAllowed)
	}
	risk := RiskElevated
	if request.Invocation.Tool.Risk == agentcore.RiskDangerous {
		risk = RiskDangerous
	}
	return mcp.approveTool(server, tool, string(request.Invocation.Arguments), risk), nil
}

type preparedMCPCall struct {
	Server              string `json:"server"`
	Tool                string `json:"tool"`
	InputSchemaHash     string `json:"inputSchemaHash"`
	RootGeneration      uint64 `json:"rootGeneration"`
	LifecycleGeneration uint64 `json:"lifecycleGeneration"`
}

type agentMCPHandler struct{ agent *AgentService }

var _ agentcore.ExternalMutationTransactionHandler = (*agentMCPHandler)(nil)

func (*agentMCPHandler) MutationMode() agentcore.MutationMode { return agentcore.MutationExternal }

func (h *agentMCPHandler) Prepare(ctx context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	server := invocation.Tool.Metadata["server"]
	tool := invocation.Tool.Metadata["tool"]
	if server == "" || tool == "" {
		return agentcore.PreparedExecution{}, fmt.Errorf("MCP ToolDef metadata is incomplete: %w", ErrNotAllowed)
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	mcp := deps.mcp
	deps.mu.RUnlock()
	if mcp == nil {
		return agentcore.PreparedExecution{}, fmt.Errorf("MCP service is not wired: %w", ErrNotAllowed)
	}
	_, rootGeneration, err := mcp.acquireWorkspaceLease()
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	mcp.mu.RLock()
	lifecycleGeneration := mcp.lifecycleGeneration
	connected := mcp.clients[server] != nil
	enabled := false
	for _, config := range mcp.config.Servers {
		if config.Name == server {
			enabled = config.Enabled
			break
		}
	}
	mcp.mu.RUnlock()
	if !connected || !enabled {
		return agentcore.PreparedExecution{}, fmt.Errorf("MCP server %q is not enabled and connected: %w", server, ErrNotAllowed)
	}
	tools, err := mcp.ListTools(ctx, server)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	candidate, err := findMCPTool(tools, tool)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	inputSchemaHash, err := mcpToolSchemaHash(candidate.InputSchema)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	state := preparedMCPCall{
		Server: server, Tool: tool, InputSchemaHash: inputSchemaHash, RootGeneration: rootGeneration,
		LifecycleGeneration: lifecycleGeneration,
	}
	opaque, _ := json.Marshal(state)
	return agentcore.PreparedExecution{
		Summary: fmt.Sprintf("Call MCP %s.%s", server, tool), Opaque: opaque,
		Metadata: map[string]string{"server": server, "tool": tool},
	}, nil
}

func findMCPTool(tools []MCPTool, tool string) (MCPTool, error) {
	var selected MCPTool
	found := false
	for _, candidate := range tools {
		if candidate.Name != tool {
			continue
		}
		if found {
			return MCPTool{}, fmt.Errorf("MCP tool %q is duplicated in the connected catalog: %w", tool, agentcore.ErrDuplicateTool)
		}
		selected = candidate
		found = true
	}
	if !found {
		return MCPTool{}, fmt.Errorf("MCP tool %q is no longer available: %w", tool, ErrNotFound)
	}
	return selected, nil
}

func mcpToolSchemaHash(schema map[string]interface{}) (string, error) {
	normalized, err := normalizeMCPAgentSchema(schema)
	if err != nil {
		return "", fmt.Errorf("encode MCP tool schema: %w", ErrInvalidInput)
	}
	sum := sha256.Sum256(normalized)
	return hex.EncodeToString(sum[:]), nil
}

func (h *agentMCPHandler) Execute(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	output, _, err := h.ExecuteExternalTransaction(ctx, invocation, prepared)
	return output, err
}

func (h *agentMCPHandler) BeginExternalMutation(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExternalMutationReceipt, error) {
	var state preparedMCPCall
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	var args map[string]interface{}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	mcp := deps.mcp
	deps.mu.RUnlock()
	if mcp == nil {
		return agentcore.ExternalMutationReceipt{}, fmt.Errorf("MCP service is not wired: %w", ErrNotAllowed)
	}
	_, rootGeneration, err := mcp.acquireWorkspaceLease()
	if err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	mcp.mu.RLock()
	lifecycleGeneration := mcp.lifecycleGeneration
	mcp.mu.RUnlock()
	if rootGeneration != state.RootGeneration || lifecycleGeneration != state.LifecycleGeneration {
		return agentcore.ExternalMutationReceipt{}, fmt.Errorf("MCP server or workspace changed after approval: %w", ErrNotAllowed)
	}
	// The connected server can change its tool catalog after capability
	// preparation. Re-list immediately before allocating the external receipt;
	// a disappeared or schema-changed tool must never reach tools/call.
	tools, err := mcp.listToolsFresh(ctx, state.Server)
	if err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	candidate, err := findMCPTool(tools, state.Tool)
	if err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	currentSchemaHash, err := mcpToolSchemaHash(candidate.InputSchema)
	if err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	if currentSchemaHash != state.InputSchemaHash || !mcpInvocationSchemaMatches(invocation.Tool.InputSchema, candidate.InputSchema) {
		return agentcore.ExternalMutationReceipt{}, fmt.Errorf("MCP tool %s.%s changed after approval: %w", state.Server, state.Tool, ErrNotAllowed)
	}
	return newAgentExternalMutationReceipt("mcp", false, map[string]string{"server": state.Server, "tool": state.Tool})
}

func mcpInvocationSchemaMatches(invocationSchema json.RawMessage, currentSchema map[string]interface{}) bool {
	normalizedCurrent, err := normalizeMCPAgentSchema(currentSchema)
	if err != nil {
		return false
	}
	return string(invocationSchema) == string(normalizedCurrent)
}

func (h *agentMCPHandler) ExecuteExternalTransaction(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, agentcore.ExternalMutationReceipt, error) {
	receipt, err := h.BeginExternalMutation(ctx, invocation, prepared)
	if err != nil {
		return agentcore.ExecutionOutput{}, agentcore.ExternalMutationReceipt{}, err
	}
	output, executeErr := h.ExecuteExternalTransactionWithReceipt(ctx, invocation, prepared, receipt)
	return output, receipt, executeErr
}

func (h *agentMCPHandler) ExecuteExternalTransactionWithReceipt(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) (agentcore.ExecutionOutput, error) {
	var state preparedMCPCall
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	var args map[string]interface{}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if receipt.ID == "" || receipt.Reversible || receipt.Metadata["server"] != state.Server || receipt.Metadata["tool"] != state.Tool {
		return agentcore.ExecutionOutput{}, fmt.Errorf("MCP call requires its preallocated irreversible receipt: %w", agentcore.ErrExternalMutationContract)
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	mcp := deps.mcp
	deps.mu.RUnlock()
	if mcp == nil {
		return agentcore.ExecutionOutput{}, fmt.Errorf("MCP service is not wired: %w", ErrNotAllowed)
	}
	lease, rootGeneration, err := mcp.acquireWorkspaceLease()
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	mcp.mu.RLock()
	lifecycleGeneration := mcp.lifecycleGeneration
	mcp.mu.RUnlock()
	if rootGeneration != state.RootGeneration || lifecycleGeneration != state.LifecycleGeneration {
		return agentcore.ExecutionOutput{}, fmt.Errorf("MCP server or workspace changed after approval: %w", ErrNotAllowed)
	}
	result, callErr := mcp.callToolWithLease(ctx, state.Server, state.Tool, args, lease)
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return agentcore.ExecutionOutput{}, marshalErr
	}
	output := agentcore.ExecutionOutput{
		Observation: boundAgentObservation(string(encoded)),
		Metadata:    map[string]string{"server": state.Server, "tool": state.Tool},
	}
	if callErr != nil {
		return output, callErr
	}
	if result != nil && result.IsError {
		return output, fmt.Errorf("MCP tool %s.%s returned an error result", state.Server, state.Tool)
	}
	return output, nil
}

func (*agentMCPHandler) CompensateExternalTransaction(_ context.Context, _ agentcore.Invocation, _ agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) error {
	if receipt.ID == "" || receipt.Reversible {
		return fmt.Errorf("MCP receipt is not an irreversible external receipt: %w", agentcore.ErrExternalMutationContract)
	}
	return fmt.Errorf("MCP call receipt %q cannot be rolled back: %w", receipt.ID, agentcore.ErrExternalMutationIrreversible)
}
