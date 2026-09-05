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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func TestAgentWorkflowAIAdapterUsesBackendPromptAndRejectsRendererInput(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	var requestMu sync.Mutex
	var requestBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		requestMu.Lock()
		requestBody = body
		requestMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"backend answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`))
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	agent.approveAI = func(string) bool { return true }
	if err := WireAgentExecutionAI(agent, ai); err != nil {
		t.Fatalf("WireAgentExecutionAI: %v", err)
	}
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow directory: %v", err)
	}
	definition := "name: ai\nsteps:\n  - name: summarize\n    type: ai\n    tool: generate\n    input:\n      prompt: backend-owned prompt\n"
	if err := os.WriteFile(filepath.Join(workflowDir, "ai.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := WireAgentWorkflowTools(agent, NewWorkflowService()); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}

	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var aiTool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["adapter"] == workflowAdapterAI {
			aiTool = candidate
			break
		}
	}
	if aiTool == nil {
		t.Fatalf("workflow AI ToolDef missing: %+v", catalog.Tools)
	}
	if aiTool.Metadata["operation"] != "generate" || aiTool.Metadata["promptHash"] == "" || strings.Contains(strings.Join([]string{aiTool.Description, aiTool.Metadata["promptHash"]}, " "), "backend-owned prompt") {
		t.Fatalf("AI ToolDef leaked or lost source identity: %+v", aiTool)
	}

	_, err = agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: aiTool.ID,
		Arguments: map[string]interface{}{"prompt": "renderer override"},
	})
	if !errors.Is(err, agentcore.ErrInvalidArguments) {
		t.Fatalf("renderer prompt injection error = %v, want ErrInvalidArguments", err)
	}
	requestMu.Lock()
	if requestBody != nil {
		t.Fatal("provider was contacted for rejected renderer arguments")
	}
	requestMu.Unlock()

	result, err := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: aiTool.ID,
		Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("execute workflow AI step: %v", err)
	}
	if !strings.Contains(result.Observation, "backend answer") || result.Usage.ProviderID != "openai-compatible" || result.Usage.TokensOut != 2 {
		t.Fatalf("workflow AI result = %+v", result)
	}
	requestMu.Lock()
	messages, ok := requestBody["messages"].([]interface{})
	requestMu.Unlock()
	if !ok || len(messages) == 0 || !strings.Contains(strings.TrimSpace(messages[len(messages)-1].(map[string]interface{})["content"].(string)), "backend-owned prompt") {
		t.Fatalf("provider request did not use backend prompt: %#v", requestBody)
	}
}

func TestAgentWorkflowAIAdapterFailsClosedWithoutProvider(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	ai := NewAIService()
	agent.approveAI = func(string) bool {
		t.Fatal("AI approval ran before provider preflight")
		return true
	}
	if err := WireAgentExecutionAI(agent, ai); err != nil {
		t.Fatalf("WireAgentExecutionAI: %v", err)
	}
	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow directory: %v", err)
	}
	definition := "name: ai\nsteps:\n  - name: summarize\n    type: ai\n    tool: generate\n    input:\n      prompt: backend-owned prompt\n"
	if err := os.WriteFile(filepath.Join(workflowDir, "ai.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := WireAgentWorkflowTools(agent, NewWorkflowService()); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var aiTool *AgentToolDefinition
	for index := range catalog.Tools {
		candidate := &catalog.Tools[index]
		if candidate.Source == string(agentcore.SourceWorkflow) && candidate.Metadata["adapter"] == workflowAdapterAI {
			aiTool = candidate
			break
		}
	}
	if aiTool == nil {
		t.Fatalf("workflow AI ToolDef missing: %+v", catalog.Tools)
	}
	_, err = agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "workflow:verify", CatalogRevision: catalog.Revision, ToolID: aiTool.ID,
		Arguments: map[string]interface{}{},
	})
	if err == nil || !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("missing provider error = %v, want ErrNotAllowed", err)
	}
}

func TestAgentWorkflowAIApprovalFreezesFallbackProviderIdentity(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	var providerCalls atomic.Int32
	providerHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		providerCalls.Add(1)
	})
	primary := httptest.NewServer(providerHandler)
	defer primary.Close()
	fallbackA := httptest.NewServer(providerHandler)
	defer fallbackA.Close()
	fallbackB := httptest.NewServer(providerHandler)
	defer fallbackB.Close()

	stateDir := t.TempDir()
	settings := NewSettingsServiceWithPath(filepath.Join(stateDir, "settings.json"))
	providerSettings := func(primaryKey, fallbackAKey string) Settings {
		return Settings{AIProviderConfigs: []AIProviderConfig{
			{ID: "primary", Name: "Primary", APIKey: primaryKey, BaseURL: primary.URL, Model: "primary-model"},
			{ID: "fallback-a", Name: "Fallback A", APIKey: fallbackAKey, BaseURL: fallbackA.URL, Model: "fallback-a-model"},
			{ID: "fallback-b", Name: "Fallback B", APIKey: "fallback-b-key", BaseURL: fallbackB.URL, Model: "fallback-b-model"},
		}}
	}
	if err := settings.SaveSettings(providerSettings("primary-key", "fallback-a-key")); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	permission := NewAIPermissionService(filepath.Join(stateDir, "permission"))
	permission.setSettingsService(settings)
	assignment := ModelAssignment{
		Operation: AIOpReview, ProviderID: "primary", Model: "primary-model",
		FallbackProviderID: "fallback-a", FallbackModel: "fallback-a-model",
	}
	if err := permission.SetAssignment(assignment); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	ai := NewAIService()
	ai.setSettingsService(settings)
	ai.setPermissionService(permission)
	if err := ai.SetConfig(AIConfig{APIKey: "bootstrap", BaseURL: primary.URL, Model: "bootstrap"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := WireAgentExecutionAI(agent, ai); err != nil {
		t.Fatalf("WireAgentExecutionAI: %v", err)
	}

	workflowDir := filepath.Join(root, ".koyori-ide", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow directory: %v", err)
	}
	definition := "name: review\nsteps:\n  - name: inspect\n    type: ai\n    tool: review\n    input:\n      prompt: inspect backend-owned code\n"
	if err := os.WriteFile(filepath.Join(workflowDir, "review.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := WireAgentWorkflowTools(agent, NewWorkflowService()); err != nil {
		t.Fatalf("WireAgentWorkflowTools: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	var toolID string
	for _, tool := range catalog.Tools {
		if tool.Source == string(agentcore.SourceWorkflow) && tool.Metadata["adapter"] == workflowAdapterAI {
			toolID = tool.ID
			break
		}
	}
	if toolID == "" {
		t.Fatalf("workflow AI tool missing: %+v", catalog.Tools)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	invocation, err := runtime.Registry().Resolve(catalog.Revision, toolID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	handler := &agentWorkflowAIHandler{agent: agent}
	prepared, err := handler.Prepare(context.Background(), invocation)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	var state preparedWorkflowAI
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		t.Fatalf("decode prepared state: %v", err)
	}
	if state.FallbackConfigFingerprint == "" {
		t.Fatal("prepared workflow AI approval omitted fallback fingerprint")
	}
	receipt, err := handler.BeginExternalMutation(context.Background(), invocation, prepared)
	if err != nil {
		t.Fatalf("BeginExternalMutation: %v", err)
	}
	if receipt.Metadata["fallbackConfigFingerprint"] != state.FallbackConfigFingerprint {
		t.Fatalf("receipt omitted fallback fingerprint: %+v", receipt.Metadata)
	}
	if err := settings.SaveSettings(providerSettings("rotated-primary-key", "fallback-a-key")); err != nil {
		t.Fatalf("rotate primary credential: %v", err)
	}
	if _, err := handler.BeginExternalMutation(context.Background(), invocation, prepared); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("primary credential mutation error = %v, want ErrNotAllowed", err)
	}
	if err := settings.SaveSettings(providerSettings("primary-key", "fallback-a-key")); err != nil {
		t.Fatalf("restore primary credential: %v", err)
	}
	if _, err := handler.BeginExternalMutation(context.Background(), invocation, prepared); err != nil {
		t.Fatalf("restored primary identity was rejected: %v", err)
	}

	assignment.FallbackProviderID = "fallback-b"
	assignment.FallbackModel = "fallback-b-model"
	if err := permission.SetAssignment(assignment); err != nil {
		t.Fatalf("mutate fallback assignment: %v", err)
	}
	if _, err := handler.BeginExternalMutation(context.Background(), invocation, prepared); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("fallback mutation error = %v, want ErrNotAllowed", err)
	}
	if _, err := handler.ExecuteExternalTransactionWithReceipt(context.Background(), invocation, prepared, receipt); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("post-receipt fallback mutation error = %v, want ErrNotAllowed", err)
	}
	if got := providerCalls.Load(); got != 0 {
		t.Fatalf("provider contacted after fallback identity drift: calls=%d", got)
	}

	assignment.FallbackProviderID = "fallback-a"
	assignment.FallbackModel = "fallback-a-model"
	if err := permission.SetAssignment(assignment); err != nil {
		t.Fatalf("restore fallback assignment: %v", err)
	}
	if err := settings.SaveSettings(providerSettings("primary-key", "rotated-fallback-a-key")); err != nil {
		t.Fatalf("rotate fallback credential: %v", err)
	}
	if _, err := handler.BeginExternalMutation(context.Background(), invocation, prepared); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("fallback credential mutation error = %v, want ErrNotAllowed", err)
	}
	if got := providerCalls.Load(); got != 0 {
		t.Fatalf("provider contacted after fallback credential drift: calls=%d", got)
	}

	assignment.FallbackProviderID = ""
	assignment.FallbackModel = ""
	if err := permission.SetAssignment(assignment); err != nil {
		t.Fatalf("remove fallback assignment: %v", err)
	}
	if _, err := handler.BeginExternalMutation(context.Background(), invocation, prepared); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("fallback removal error = %v, want ErrNotAllowed", err)
	}
	preparedWithoutFallback, err := handler.Prepare(context.Background(), invocation)
	if err != nil {
		t.Fatalf("Prepare without fallback: %v", err)
	}
	receiptWithoutFallback, err := handler.BeginExternalMutation(context.Background(), invocation, preparedWithoutFallback)
	if err != nil {
		t.Fatalf("BeginExternalMutation without fallback: %v", err)
	}
	if fingerprint, present := receiptWithoutFallback.Metadata["fallbackConfigFingerprint"]; !present || fingerprint != "" {
		t.Fatalf("receipt did not explicitly bind absent fallback: %+v", receiptWithoutFallback.Metadata)
	}
	tamperedReceipt := receiptWithoutFallback
	tamperedReceipt.Metadata = make(map[string]string, len(receiptWithoutFallback.Metadata))
	for key, value := range receiptWithoutFallback.Metadata {
		tamperedReceipt.Metadata[key] = value
	}
	delete(tamperedReceipt.Metadata, "fallbackConfigFingerprint")
	if _, err := handler.ExecuteExternalTransactionWithReceipt(context.Background(), invocation, preparedWithoutFallback, tamperedReceipt); !errors.Is(err, agentcore.ErrExternalMutationContract) {
		t.Fatalf("missing fallback receipt identity error = %v, want ErrExternalMutationContract", err)
	}

	assignment.FallbackProviderID = "fallback-b"
	assignment.FallbackModel = "fallback-b-model"
	if err := permission.SetAssignment(assignment); err != nil {
		t.Fatalf("add fallback assignment: %v", err)
	}
	if _, err := handler.BeginExternalMutation(context.Background(), invocation, preparedWithoutFallback); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("fallback addition error = %v, want ErrNotAllowed", err)
	}
	if got := providerCalls.Load(); got != 0 {
		t.Fatalf("provider contacted across fallback presence drift: calls=%d", got)
	}
}
