package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func contextWithMissingAIStreamTarget(identity string) context.Context {
	ctx := withAgentCallerContext(context.Background(), identity)
	// Keep the backend caller identity while deliberately replacing the Wails
	// window value with a non-window. This models a malformed/internal call
	// reaching a renderer-only stream entry point.
	return context.WithValue(ctx, application.WindowKey, struct{}{})
}

func TestAIServiceStartStreamRejectsMissingRendererTargetBeforeProviderSideEffects(t *testing.T) {
	var providerHits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		providerHits.Add(1)
	}))
	defer provider.Close()

	ai := NewAIService()
	ai.setApp(application.New(application.Options{}))
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: provider.URL, Model: "test-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	_, err := ai.StartStream(
		contextWithMissingAIStreamTarget("wails-window:missing-target:main"),
		[]ChatMessage{{Role: "user", Content: "must not send"}},
	)
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("StartStream without renderer target = %v, want ErrNotAllowed", err)
	}
	if got := providerHits.Load(); got != 0 {
		t.Fatalf("missing-target StartStream reached provider %d times", got)
	}
	if ai.IsStreaming() {
		t.Fatal("missing-target StartStream claimed the provider slot")
	}
}

func TestAIServiceStartAgentStreamRejectsMissingRendererTargetBeforeSideEffects(t *testing.T) {
	var providerHits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		providerHits.Add(1)
	}))
	defer provider.Close()

	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	permission := NewAIPermissionService(t.TempDir())
	if _, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	ai.setApp(application.New(application.Options{}))
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: provider.URL, Model: "test-model"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	ownerCtx := testAIStreamCallerContext()
	sessionID, err := agent.CreateAgentSessionForCaller(ownerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	missingTargetCtx := contextWithMissingAIStreamTarget("wails-window:test:main")
	if _, err := ai.StartAgentStream(missingTargetCtx, sessionID, []ChatMessage{{Role: "user", Content: "must not send"}}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("StartAgentStream without renderer target = %v, want ErrNotAllowed", err)
	}
	if got := providerHits.Load(); got != 0 {
		t.Fatalf("missing-target StartAgentStream reached provider %d times", got)
	}
	if ai.IsStreaming() {
		t.Fatal("missing-target StartAgentStream claimed the provider slot")
	}
	if records := permission.usageRecordsSnapshot(); len(records) != 0 {
		t.Fatalf("missing-target StartAgentStream published usage: %+v", records)
	}
}
