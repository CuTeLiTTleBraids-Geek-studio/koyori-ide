package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestAIService_ListModels_ReturnsModels verifies that ListModels
// correctly fetches and parses the /v1/models endpoint response.
func TestAIService_ListModels_ReturnsModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent header")
		}

		response := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{"id": "gpt-5", "object": "model", "owned_by": "openai"},
				{"id": "gpt-4o", "object": "model", "owned_by": "openai"},
				{"id": "gpt-4o-mini", "object": "model", "owned_by": "openai"},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	svc := NewAIService()
	models, err := svc.ListModels(server.URL, "test-key")
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d: %v", len(models), models)
	}
	expected := []string{"gpt-5", "gpt-4o", "gpt-4o-mini"}
	for i, m := range models {
		if m != expected[i] {
			t.Errorf("model[%d]: expected %q, got %q", i, expected[i], m)
		}
	}
}

// TestAIService_ListModels_NoAPIKey verifies that ListModels works
// without an API key (for local providers like Ollama).
func TestAIService_ListModels_NoAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no auth header, got: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent header")
		}
		response := map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "llama3.2"},
				{"id": "qwen2.5-coder"},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	svc := NewAIService()
	models, err := svc.ListModels(server.URL, "")
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
}

// TestAIService_ListModels_EmptyBaseURL verifies that ListModels
// returns an error when baseURL is empty.
func TestAIService_ListModels_EmptyBaseURL(t *testing.T) {
	svc := NewAIService()
	_, err := svc.ListModels("", "test-key")
	if err == nil {
		t.Fatal("expected error for empty base URL")
	}
}

// TestAIService_ListModels_NonOKStatus verifies that ListModels
// returns an error when the server returns a non-2xx status.
func TestAIService_ListModels_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	defer server.Close()

	svc := NewAIService()
	_, err := svc.ListModels(server.URL, "bad-key")
	if err == nil {
		t.Fatal("expected error for 401 status")
	}
}

// TestAIService_ListModels_EmptyList verifies that ListModels
// returns an empty slice (not nil) when the server returns no models.
func TestAIService_ListModels_EmptyList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"data": []map[string]interface{}{},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	svc := NewAIService()
	models, err := svc.ListModels(server.URL, "test-key")
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("expected 0 models, got %d", len(models))
	}
}

// TestAIService_ListModels_FiltersEmptyIDs verifies that ListModels
// filters out entries with empty IDs.
func TestAIService_ListModels_FiltersEmptyIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "gpt-5"},
				{"id": ""},
				{"id": "gpt-4o"},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	svc := NewAIService()
	models, err := svc.ListModels(server.URL, "test-key")
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models (empty ID filtered), got %d: %v", len(models), models)
	}
}

// TestAIService_ListModels_MalformedJSON verifies that ListModels
// returns an error when the response is not valid JSON.
func TestAIService_ListModels_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not valid json`))
	}))
	defer server.Close()

	svc := NewAIService()
	_, err := svc.ListModels(server.URL, "test-key")
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// TestAIService_ListModels_N73_RejectsMaliciousBaseURL verifies that
// ListModels rejects base URLs that could exfiltrate the API key (N-73).
// The validation happens BEFORE any HTTP request, so the test server is
// never contacted for malicious URLs — we verify this by checking that
// the handler is not invoked.
func TestAIService_ListModels_N73_RejectsMaliciousBaseURL(t *testing.T) {
	handlerCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	maliciousURLs := []string{
		"file:///etc/passwd",
		"data:text/html,<script>alert(1)</script>",
		"ftp://example.com",
		"gopher://example.com",
		"javascript:alert(1)",
		"http://attacker.example.com",     // http on non-loopback
		"http://user:pass@localhost:1234", // embedded credentials
	}
	svc := NewAIService()
	for _, u := range maliciousURLs {
		t.Run(u, func(t *testing.T) {
			handlerCalled = false
			_, err := svc.ListModels(u, "secret-api-key")
			if err == nil {
				t.Errorf("expected error for malicious URL %q, got nil", u)
			}
			if handlerCalled {
				t.Errorf("handler should NOT have been called for malicious URL %q — API key would leak", u)
			}
		})
	}
}

// TestAIService_ListModels_CRIT01_UsesStoredKeyWhenEmpty verifies that
// ListModels uses the backend's stored (decrypted) API key when the caller
// passes apiKey="". This is the G-SEC-07 / CRIT-01 isolation pattern: the
// frontend never holds the plaintext key, so it calls ListModels with an
// empty key and the backend fills it from a.config.APIKey (populated by
// SetConfig when UseStoredKey was true).
func TestAIService_ListModels_CRIT01_UsesStoredKeyWhenEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The stored key must be sent, NOT an empty auth header.
		if got := r.Header.Get("Authorization"); got != "Bearer stored-test-key" {
			t.Errorf("expected stored key auth, got %q (CRIT-01: key must come from backend config)", got)
		}
		response := map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "gpt-4o"},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	svc := NewAIService()
	// Simulate the state left by SetConfig: a.config.APIKey holds the
	// decrypted stored key (populated via SettingsService.GetAPIKeyForConfig).
	svc.mu.Lock()
	svc.config = AIConfig{
		APIKey:  "stored-test-key",
		BaseURL: server.URL,
	}
	svc.mu.Unlock()

	models, err := svc.ListModels(server.URL, "")
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(models) != 1 || models[0] != "gpt-4o" {
		t.Fatalf("unexpected models: %v", models)
	}
}

// TestAIService_ListModels_CRIT01_FallsBackToSettingsService verifies that
// when a.config.APIKey is empty (SetConfig not yet called, or cleared) but
// the backend has a stored key for the configured ConfigID, ListModels
// retrieves it via SettingsService.GetAPIKeyForConfig.
func TestAIService_ListModels_CRIT01_FallsBackToSettingsService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ss-stored-key" {
			t.Errorf("expected SettingsService key, got %q", got)
		}
		response := map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "claude-3"},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Set up a SettingsService with a stored (encrypted) provider key.
	configPath := filepath.Join(t.TempDir(), "settings.json")
	ss := &SettingsService{configPath: configPath}
	settings := defaultSettings()
	settings.AIProviderConfigs = []AIProviderConfig{
		{ID: "cfg-x", Name: "X", Provider: "openai", APIKey: "ss-stored-key", BaseURL: server.URL, Model: "gpt-4o"},
	}
	settings.ActiveAIConfigID = "cfg-x"
	if err := ss.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	svc := NewAIService()
	svc.setSettingsService(ss)
	// a.config has ConfigID set but APIKey empty (UseStoredKey path not yet
	// run, or config cleared). ListModels should fetch from SettingsService.
	svc.mu.Lock()
	svc.config = AIConfig{ConfigID: "cfg-x", BaseURL: server.URL}
	svc.mu.Unlock()

	models, err := svc.ListModels(server.URL, "")
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(models) != 1 || models[0] != "claude-3" {
		t.Fatalf("unexpected models: %v", models)
	}
}
