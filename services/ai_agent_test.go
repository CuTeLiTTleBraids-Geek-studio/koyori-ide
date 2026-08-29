package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newAgentFallbackTestService(t *testing.T, primaryURL, fallbackURL string) *AIService {
	t.Helper()
	stateDir := t.TempDir()
	settings := NewSettingsServiceWithPath(filepath.Join(stateDir, "settings.json"))
	if err := settings.SaveSettings(Settings{AIProviderConfigs: []AIProviderConfig{
		{ID: "primary", Name: "Primary", Protocol: "openai", APIKey: "primary-key", BaseURL: primaryURL, Model: "primary-model"},
		{ID: "fallback", Name: "Fallback", Protocol: "openai", APIKey: "fallback-key", BaseURL: fallbackURL, Model: "fallback-model"},
	}}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	permission := NewAIPermissionService(filepath.Join(stateDir, "permission"))
	permission.setSettingsService(settings)
	if err := permission.SetAssignment(ModelAssignment{
		Operation: AIOpAgent, ProviderID: "primary", Model: "primary-model",
		FallbackProviderID: "fallback", FallbackModel: "fallback-model",
	}); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	ai := NewAIService()
	ai.setSettingsService(settings)
	ai.setPermissionService(permission)
	if err := ai.SetConfig(AIConfig{APIKey: "bootstrap", BaseURL: primaryURL, Model: "bootstrap"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	return ai
}

func TestResolveAgentOperationUsesAssignedProviderBoundary(t *testing.T) {
	primary := httptest.NewServer(nil)
	defer primary.Close()
	assigned := httptest.NewServer(nil)
	defer assigned.Close()

	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	settings := NewSettingsServiceWithPath(settingsPath)
	if err := settings.SaveSettings(Settings{AIProviderConfigs: []AIProviderConfig{{
		ID: "provider-b", Name: "Provider B", Protocol: "anthropic", APIKey: "provider-b-key",
		BaseURL: assigned.URL, Model: "provider-b-model",
	}}}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("settings file missing: %v", err)
	}

	permission := NewAIPermissionService(t.TempDir())
	permission.setSettingsService(settings)
	if err := permission.SetAssignment(ModelAssignment{
		Operation: AIOpAgent, ProviderID: "provider-b", Model: "assigned-model",
	}); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	ai := NewAIService()
	ai.setSettingsService(settings)
	ai.setPermissionService(permission)
	if err := ai.SetConfig(AIConfig{APIKey: "global-key", BaseURL: primary.URL, Model: "global-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	config, _, err := ai.resolveAgentOperation(AIOpAgent)
	if err != nil {
		t.Fatalf("resolveAgentOperation: %v", err)
	}
	if config.ConfigID != "provider-b" || config.APIKey != "provider-b-key" || config.BaseURL != assigned.URL || config.Protocol != "anthropic" || config.Model != "assigned-model" {
		t.Fatalf("resolved provider crossed global boundary: %#v", config)
	}

	publicConfig, publicFallback, err := ai.ResolveModelFor(AIOpAgent)
	if err != nil {
		t.Fatalf("ResolveModelFor: %v", err)
	}
	if publicFallback != nil {
		t.Fatalf("unexpected fallback: %#v", publicFallback)
	}
	if publicConfig.Model != "assigned-model" || publicConfig.ConfigID != "provider-b" ||
		publicConfig.APIKey != "" || publicConfig.BaseURL != "" || publicConfig.Protocol != "" {
		t.Fatalf("public model resolution leaked provider details: %#v", publicConfig)
	}
}

func TestResolveModelForRedactsGlobalProviderDetails(t *testing.T) {
	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey: "global-secret", BaseURL: "https://api.example.test", Protocol: "anthropic", Model: "global-model",
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	publicConfig, fallback, err := ai.ResolveModelFor(AIOpChat)
	if err != nil {
		t.Fatalf("ResolveModelFor: %v", err)
	}
	if fallback != nil {
		t.Fatalf("unexpected fallback: %#v", fallback)
	}
	if publicConfig.Model != "global-model" || publicConfig.APIKey != "" || publicConfig.BaseURL != "" || publicConfig.Protocol != "" {
		t.Fatalf("global provider details leaked: %#v", publicConfig)
	}
}

func TestSendAgentOperationDoesNotFallbackOnPermanentOrProtocolErrors(t *testing.T) {
	tests := []struct {
		name       string
		primary    http.HandlerFunc
		wantErrSub string
	}{
		{
			name: "unauthorized",
			primary: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = fmt.Fprint(w, `{"error":{"message":"invalid primary credential"}}`)
			},
			wantErrSub: "status 401",
		},
		{
			name: "malformed success response",
			primary: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{not valid json`)
			},
			wantErrSub: "invalid character",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			primary := httptest.NewServer(tt.primary)
			defer primary.Close()
			var fallbackCalls atomic.Int32
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"fallback"},"finish_reason":"stop"}]}`)
			}))
			defer fallback.Close()

			ai := newAgentFallbackTestService(t, primary.URL, fallback.URL)
			response, _, err := ai.sendAgentOperation(context.Background(), AIOpAgent, []ChatMessage{{Role: "user", Content: "private source"}})
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("sendAgentOperation response=%+v error=%v, want primary error containing %q", response, err, tt.wantErrSub)
			}
			if got := fallbackCalls.Load(); got != 0 {
				t.Fatalf("permanent primary error reached fallback provider %d time(s)", got)
			}
		})
	}
}

func TestSendAgentOperationDeadlineInterruptsPrimaryRetryBackoff(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"error":{"message":"temporarily unavailable"}}`)
	}))
	defer primary.Close()
	var fallbackCalls atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"fallback"}}]}`)
	}))
	defer fallback.Close()
	ai := newAgentFallbackTestService(t, primary.URL, fallback.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := ai.sendAgentOperation(ctx, AIOpAgent, []ChatMessage{{Role: "user", Content: "stop promptly"}})
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("sendAgentOperation error = %v, want context deadline", err)
	}
	if elapsed >= 400*time.Millisecond {
		t.Fatalf("deadline took %s; retry backoff ignored cancellation", elapsed)
	}
	if got := fallbackCalls.Load(); got != 0 {
		t.Fatalf("expired primary deadline reached fallback %d time(s)", got)
	}
}

func TestShouldFallbackAgentOperationClassifiesOnlyTransientFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "canceled", err: context.Canceled, want: false},
		{name: "deadline", err: context.DeadlineExceeded, want: false},
		{name: "unauthorized", err: &aiHTTPStatusError{statusCode: http.StatusUnauthorized, message: "unauthorized"}, want: false},
		{name: "rate limited", err: &aiHTTPStatusError{statusCode: http.StatusTooManyRequests, message: "rate limited"}, want: true},
		{name: "server unavailable", err: &aiHTTPStatusError{statusCode: http.StatusServiceUnavailable, message: "unavailable"}, want: true},
		{name: "malformed success", err: errors.New("invalid character in provider response"), want: false},
		{name: "temporary DNS", err: &net.DNSError{Err: "temporary lookup failure", IsTemporary: true}, want: true},
		{name: "permanent DNS", err: &net.DNSError{Err: "host not found", IsNotFound: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFallbackAgentOperation(context.Background(), tt.err); got != tt.want {
				t.Fatalf("shouldFallbackAgentOperation(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldFallbackAgentOperation(expired, &aiHTTPStatusError{statusCode: http.StatusServiceUnavailable, message: "unavailable"}) {
		t.Fatal("expired operation context allowed fallback")
	}
}

func TestSendResolvedAgentOperationUsesOneDeadlineForTransientFallback(t *testing.T) {
	primary := AIConfig{ConfigID: "primary", Model: "primary-model"}
	fallback := AIConfig{ConfigID: "fallback", Model: "fallback-model"}
	var deadlines []time.Time
	var providerOrder []string
	sender := func(ctx context.Context, config AIConfig, _ []ChatMessage) (*ChatResponse, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("provider call has no total deadline")
		}
		deadlines = append(deadlines, deadline)
		providerOrder = append(providerOrder, config.ConfigID)
		if config.ConfigID == primary.ConfigID {
			return nil, &aiHTTPStatusError{statusCode: http.StatusServiceUnavailable, message: "unavailable"}
		}
		return &ChatResponse{Content: "fallback response"}, nil
	}

	response, used, err := sendResolvedAgentOperation(context.Background(), primary, &fallback, nil, sender)
	if err != nil {
		t.Fatalf("sendResolvedAgentOperation: %v", err)
	}
	if response == nil || response.Content != "fallback response" || used.ConfigID != fallback.ConfigID {
		t.Fatalf("fallback result mismatch: response=%+v config=%+v", response, used)
	}
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("provider deadlines are not shared: %v", deadlines)
	}
	if fmt.Sprint(providerOrder) != "[primary fallback]" {
		t.Fatalf("provider order = %v", providerOrder)
	}
}
