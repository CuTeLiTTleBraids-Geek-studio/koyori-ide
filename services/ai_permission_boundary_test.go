package services

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestAIServiceOperationPermissionRejectsDisabledBeforeProviderCall(t *testing.T) {
	var providerHits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		providerHits.Add(1)
	}))
	defer provider.Close()

	tests := []struct {
		name      string
		operation AIOperation
		call      func(*AIService) error
	}{
		{
			name:      "chat",
			operation: AIOpChat,
			call: func(ai *AIService) error {
				_, err := ai.Send([]ChatMessage{{Role: "user", Content: "hello"}})
				return err
			},
		},
		{
			name:      "inline completion",
			operation: AIOpInlineCompletion,
			call: func(ai *AIService) error {
				_, err := ai.Complete(CompletionRequest{Prefix: "package main", Language: "go"})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			permission := NewAIPermissionService(t.TempDir())
			if err := permission.SetAssignment(ModelAssignment{Operation: tt.operation, Disabled: true}); err != nil {
				t.Fatalf("SetAssignment: %v", err)
			}
			ai := NewAIService()
			ai.setPermissionService(permission)
			if err := ai.SetConfig(AIConfig{APIKey: "global-key", BaseURL: provider.URL, Model: "global-model"}); err != nil {
				t.Fatalf("SetConfig: %v", err)
			}

			err := tt.call(ai)
			if !errors.Is(err, ErrNotAllowed) {
				t.Fatalf("disabled operation error = %v, want ErrNotAllowed", err)
			}
		})
	}

	if got := providerHits.Load(); got != 0 {
		t.Fatalf("disabled operations reached provider %d times", got)
	}
}

func TestAIServiceOperationPermissionUsesBackendHydratedProvider(t *testing.T) {
	var globalHits atomic.Int32
	globalProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		globalHits.Add(1)
		http.Error(w, "global provider must not be used", http.StatusInternalServerError)
	}))
	defer globalProvider.Close()

	tests := []struct {
		name      string
		operation AIOperation
		call      func(*AIService) (string, error)
	}{
		{
			name:      "chat",
			operation: AIOpChat,
			call: func(ai *AIService) (string, error) {
				response, err := ai.Send([]ChatMessage{{Role: "user", Content: "hello"}})
				if err != nil {
					return "", err
				}
				return response.Content, nil
			},
		},
		{
			name:      "inline completion",
			operation: AIOpInlineCompletion,
			call: func(ai *AIService) (string, error) {
				response, err := ai.Complete(CompletionRequest{Prefix: "package main", Language: "go"})
				if err != nil {
					return "", err
				}
				return response.Text, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var assignedHits atomic.Int32
			assignedProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assignedHits.Add(1)
				if got := r.Header.Get("Authorization"); got != "Bearer assigned-key" {
					t.Errorf("Authorization = %q, want assigned provider key", got)
				}
				var request struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if request.Model != "assigned-model" {
					t.Errorf("model = %q, want assigned-model", request.Model)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{
						"message":       map[string]string{"role": "assistant", "content": "assigned response"},
						"finish_reason": "stop",
					}},
				})
			}))
			defer assignedProvider.Close()

			settings := NewSettingsServiceWithPath(filepath.Join(t.TempDir(), "settings.json"))
			if err := settings.SaveSettings(Settings{AIProviderConfigs: []AIProviderConfig{{
				ID: "assigned-provider", Name: "Assigned Provider", APIKey: "assigned-key",
				BaseURL: assignedProvider.URL, Model: "provider-default-model",
			}}}); err != nil {
				t.Fatalf("SaveSettings: %v", err)
			}
			permission := NewAIPermissionService(t.TempDir())
			if err := permission.SetAssignment(ModelAssignment{
				Operation: tt.operation, ProviderID: "assigned-provider", Model: "assigned-model",
			}); err != nil {
				t.Fatalf("SetAssignment: %v", err)
			}
			ai := NewAIService()
			ai.setSettingsService(settings)
			ai.setPermissionService(permission)
			if err := ai.SetConfig(AIConfig{APIKey: "global-key", BaseURL: globalProvider.URL, Model: "global-model"}); err != nil {
				t.Fatalf("SetConfig: %v", err)
			}

			got, err := tt.call(ai)
			if err != nil {
				t.Fatalf("operation call: %v", err)
			}
			if got != "assigned response" {
				t.Fatalf("response = %q, want assigned response", got)
			}
			if assignedHits.Load() != 1 {
				t.Fatalf("assigned provider hits = %d, want 1", assignedHits.Load())
			}
		})
	}

	if got := globalHits.Load(); got != 0 {
		t.Fatalf("assigned operations reached global provider %d times", got)
	}
}

func TestAIServiceOperationPermissionPreservesUnassignedGlobalProvider(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]string{"role": "assistant", "content": "global response"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer provider.Close()

	ai := NewAIService()
	ai.setPermissionService(NewAIPermissionService(t.TempDir()))
	if err := ai.SetConfig(AIConfig{APIKey: "global-key", BaseURL: provider.URL, Model: "global-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	response, err := ai.Send([]ChatMessage{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if response.Content != "global response" {
		t.Fatalf("response = %q, want global response", response.Content)
	}
}

func TestAIServiceOperationPermissionRejectsDisabledTitleBeforeProviderCall(t *testing.T) {
	var providerHits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		providerHits.Add(1)
	}))
	defer provider.Close()

	permission := NewAIPermissionService(t.TempDir())
	if err := permission.SetAssignment(ModelAssignment{Operation: AIOpTitleGeneration, Disabled: true}); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	ai := NewAIService()
	ai.setPermissionService(permission)
	if err := ai.SetConfig(AIConfig{APIKey: "global-key", BaseURL: provider.URL, Model: "global-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	title, err := ai.GenerateTitleWithAI("hello from a disabled operation")
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("disabled title error = %v, want ErrNotAllowed", err)
	}
	if title != GenerateTitle("hello from a disabled operation") {
		t.Fatalf("disabled title fallback = %q, want heuristic fallback", title)
	}
	if got := providerHits.Load(); got != 0 {
		t.Fatalf("disabled title reached provider %d times", got)
	}
}

func TestAIServiceOperationPermissionUsesBackendHydratedTitleProvider(t *testing.T) {
	var globalHits atomic.Int32
	globalProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		globalHits.Add(1)
		http.Error(w, "global provider must not be used", http.StatusInternalServerError)
	}))
	defer globalProvider.Close()

	var assignedHits atomic.Int32
	assignedProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assignedHits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer assigned-key" {
			t.Errorf("Authorization = %q, want assigned provider key", got)
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.Model != "assigned-title-model" {
			t.Errorf("model = %q, want assigned-title-model", request.Model)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": "Assigned title"},
			}},
		})
	}))
	defer assignedProvider.Close()

	settings := NewSettingsServiceWithPath(filepath.Join(t.TempDir(), "settings.json"))
	if err := settings.SaveSettings(Settings{AIProviderConfigs: []AIProviderConfig{{
		ID: "assigned-title-provider", Name: "Assigned Title Provider", APIKey: "assigned-key",
		BaseURL: assignedProvider.URL, Model: "provider-default-title-model",
	}}}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	permission := NewAIPermissionService(t.TempDir())
	if err := permission.SetAssignment(ModelAssignment{
		Operation: AIOpTitleGeneration, ProviderID: "assigned-title-provider", Model: "assigned-title-model",
	}); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	ai := NewAIService()
	ai.setSettingsService(settings)
	ai.setPermissionService(permission)
	if err := ai.SetConfig(AIConfig{APIKey: "global-key", BaseURL: globalProvider.URL, Model: "global-title-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	title, err := ai.GenerateTitleWithAI("hello")
	if err != nil {
		t.Fatalf("GenerateTitleWithAI: %v", err)
	}
	if title != "Assigned title" {
		t.Fatalf("title = %q, want assigned title", title)
	}
	if got := assignedHits.Load(); got != 1 {
		t.Fatalf("assigned provider hits = %d, want 1", got)
	}
	if got := globalHits.Load(); got != 0 {
		t.Fatalf("assigned title reached global provider %d times", got)
	}
}

func TestAIServiceOperationPermissionRejectsDisabledStartStreamBeforeSideEffects(t *testing.T) {
	var providerHits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		providerHits.Add(1)
	}))
	defer provider.Close()

	permission := NewAIPermissionService(t.TempDir())
	if err := permission.SetAssignment(ModelAssignment{Operation: AIOpChat, Disabled: true}); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	ai := NewAIService()
	ai.setApp(application.New(application.Options{}))
	ai.setPermissionService(permission)
	if err := ai.SetConfig(AIConfig{APIKey: "global-key", BaseURL: provider.URL, Model: "global-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	callerCtx, _ := newAIStreamTestCaller(41, "disabled-stream")
	if _, err := ai.StartStream(callerCtx, []ChatMessage{{Role: "user", Content: "must not send"}}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("disabled StartStream = %v, want ErrNotAllowed", err)
	}
	if ai.IsStreaming() {
		t.Fatal("disabled StartStream claimed the provider slot")
	}
	if got := providerHits.Load(); got != 0 {
		t.Fatalf("disabled StartStream reached provider %d times", got)
	}
	if records := permission.usageRecordsSnapshot(); len(records) != 0 {
		t.Fatalf("disabled StartStream published usage: %+v", records)
	}
}

func TestAIServiceOperationPermissionRejectsDisabledStartAgentStreamBeforeSideEffects(t *testing.T) {
	var providerHits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		providerHits.Add(1)
	}))
	defer provider.Close()

	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	permission := NewAIPermissionService(t.TempDir())
	if err := permission.SetAssignment(ModelAssignment{Operation: AIOpChat, Disabled: true}); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	if _, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	ai.setApp(application.New(application.Options{}))
	ai.setPermissionService(permission)
	if err := ai.SetConfig(AIConfig{APIKey: "global-key", BaseURL: provider.URL, Model: "global-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	callerCtx, _ := newAIStreamTestCaller(42, "disabled-agent-stream")
	sessionID, err := agent.CreateAgentSessionForCaller(callerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	if _, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "must not send"}}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("disabled StartAgentStream = %v, want ErrNotAllowed", err)
	}
	if ai.IsStreaming() {
		t.Fatal("disabled StartAgentStream claimed the provider slot")
	}
	if got := providerHits.Load(); got != 0 {
		t.Fatalf("disabled StartAgentStream reached provider %d times", got)
	}
	if records := permission.usageRecordsSnapshot(); len(records) != 0 {
		t.Fatalf("disabled StartAgentStream published usage: %+v", records)
	}
}

func TestAIServiceOperationPermissionUsesBackendHydratedStartStreamProvider(t *testing.T) {
	var globalHits atomic.Int32
	globalProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		globalHits.Add(1)
		http.Error(w, "global provider must not be used", http.StatusInternalServerError)
	}))
	defer globalProvider.Close()
	var assignedHits atomic.Int32
	assignedProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assignedHits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer assigned-stream-key" {
			t.Errorf("Authorization = %q, want assigned stream key", got)
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.Model != "assigned-stream-model" {
			t.Errorf("model = %q, want assigned-stream-model", request.Model)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"assigned\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer assignedProvider.Close()

	settings := NewSettingsServiceWithPath(filepath.Join(t.TempDir(), "settings.json"))
	if err := settings.SaveSettings(Settings{AIProviderConfigs: []AIProviderConfig{{
		ID: "assigned-stream-provider", Name: "Assigned Stream Provider", APIKey: "assigned-stream-key",
		BaseURL: assignedProvider.URL, Model: "provider-default-stream-model",
	}}}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	permission := NewAIPermissionService(t.TempDir())
	if err := permission.SetAssignment(ModelAssignment{
		Operation: AIOpChat, ProviderID: "assigned-stream-provider", Model: "assigned-stream-model",
	}); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	ai := NewAIService()
	ai.setApp(application.New(application.Options{}))
	ai.setSettingsService(settings)
	ai.setPermissionService(permission)
	if err := ai.SetConfig(AIConfig{APIKey: "global-key", BaseURL: globalProvider.URL, Model: "global-stream-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	callerCtx, callerWindow := newAIStreamTestCaller(43, "assigned-stream")
	if _, err := ai.StartStream(callerCtx, []ChatMessage{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatalf("StartStream: %v", err)
	}
	waitForAIStreamEvent(t, callerWindow, "ai:done")
	if got := assignedHits.Load(); got != 1 {
		t.Fatalf("assigned stream provider hits = %d, want 1", got)
	}
	if got := globalHits.Load(); got != 0 {
		t.Fatalf("assigned stream reached global provider %d times", got)
	}
}

func TestAIServiceOperationPermissionUsesBackendHydratedStartAgentStreamProvider(t *testing.T) {
	var globalHits atomic.Int32
	globalProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		globalHits.Add(1)
		http.Error(w, "global provider must not be used", http.StatusInternalServerError)
	}))
	defer globalProvider.Close()
	var assignedHits atomic.Int32
	assignedProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assignedHits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer assigned-agent-stream-key" {
			t.Errorf("Authorization = %q, want assigned agent stream key", got)
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.Model != "assigned-agent-stream-model" {
			t.Errorf("model = %q, want assigned-agent-stream-model", request.Model)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"assigned\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer assignedProvider.Close()

	settings := NewSettingsServiceWithPath(filepath.Join(t.TempDir(), "settings.json"))
	if err := settings.SaveSettings(Settings{AIProviderConfigs: []AIProviderConfig{{
		ID: "assigned-agent-stream-provider", Name: "Assigned Agent Stream Provider", APIKey: "assigned-agent-stream-key",
		BaseURL: assignedProvider.URL, Model: "provider-default-agent-stream-model",
	}}}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	permission := NewAIPermissionService(t.TempDir())
	if err := permission.SetAssignment(ModelAssignment{
		Operation: AIOpChat, ProviderID: "assigned-agent-stream-provider", Model: "assigned-agent-stream-model",
	}); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	if _, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	ai.setApp(application.New(application.Options{}))
	ai.setSettingsService(settings)
	ai.setPermissionService(permission)
	if err := ai.SetConfig(AIConfig{APIKey: "global-key", BaseURL: globalProvider.URL, Model: "global-agent-stream-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	callerCtx, callerWindow := newAIStreamTestCaller(44, "assigned-agent-stream")
	sessionID, err := agent.CreateAgentSessionForCaller(callerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	if _, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatalf("StartAgentStream: %v", err)
	}
	waitForAIStreamEvent(t, callerWindow, "ai:done")
	if got := assignedHits.Load(); got != 1 {
		t.Fatalf("assigned agent stream provider hits = %d, want 1", got)
	}
	if got := globalHits.Load(); got != 0 {
		t.Fatalf("assigned agent stream reached global provider %d times", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for ai.IsStreaming() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ai.IsStreaming() {
		t.Fatal("assigned agent stream remained active after provider completion")
	}
}
