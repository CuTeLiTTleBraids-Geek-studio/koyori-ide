package services

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// TestAIService_N93_SetConfig_ConcurrentReaders_NoRace verifies that
// concurrent SetConfig calls do not race with readers using snapshot().
// Run with `go test -race` to detect data races.
func TestAIService_N93_SetConfig_ConcurrentReaders_NoRace(t *testing.T) {
	svc := NewAIService()

	var stop int32
	var wg sync.WaitGroup

	// Writer: continuously calls SetConfig with different values.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; atomic.LoadInt32(&stop) == 0; i++ {
			if err := svc.SetConfig(AIConfig{
				APIKey:        "key-" + string(rune('a'+(i%26))),
				Model:         "model-" + string(rune('a'+(i%26))),
				SystemPrompt:  "prompt variant",
				MaxTokens:     100 + (i % 50),
				ContextWindow: 8000 + (i % 100),
			}); err != nil {
				t.Errorf("SetConfig failed: %v", err)
				return
			}
		}
	}()

	// Readers: continuously call methods that take snapshots.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for atomic.LoadInt32(&stop) == 0 {
				_ = svc.maxTokens()
				_ = svc.contextWindow()
				_ = svc.effectiveSystemPrompt()
				_ = svc.GetEffectiveAgentSystemPrompt()
				_ = svc.GetEffectiveConversationTitlePrompt()
				_ = svc.GetEffectiveInlineCompletionPrompt()
				_ = svc.completeSystemPrompt("go")
				// snapshot() itself
				snap := svc.snapshot()
				_ = snap.config.APIKey
			}
		}()
	}

	// Let it run briefly to surface races.
	time.Sleep(100 * time.Millisecond)
	atomic.StoreInt32(&stop, 1)
	wg.Wait()
}

// TestAIService_N93_Snapshot_IsStableAfterSetConfig verifies that a
// snapshot taken before a SetConfig call is not mutated by the SetConfig.
// This is the core invariant that makes the snapshot pattern safe: the
// goroutine in StartStream uses the snapshot, so a concurrent SetConfig
// cannot affect the in-flight request.
func TestAIService_N93_Snapshot_IsStableAfterSetConfig(t *testing.T) {
	svc := NewAIService()
	if err := svc.SetConfig(AIConfig{
		APIKey: "original-key",
		Model:  "original-model",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Take a snapshot.
	snap := svc.snapshot()
	if snap.config.APIKey != "original-key" {
		t.Fatalf("snapshot before SetConfig: expected original-key, got %q", snap.config.APIKey)
	}

	// Mutate the service's config.
	if err := svc.SetConfig(AIConfig{
		APIKey: "new-key",
		Model:  "new-model",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// The previously-taken snapshot must be unchanged.
	if snap.config.APIKey != "original-key" {
		t.Errorf("snapshot was mutated by SetConfig: expected original-key, got %q", snap.config.APIKey)
	}
	if snap.config.Model != "original-model" {
		t.Errorf("snapshot was mutated by SetConfig: expected original-model, got %q", snap.config.Model)
	}

	// A fresh snapshot should reflect the new config.
	newSnap := svc.snapshot()
	if newSnap.config.APIKey != "new-key" {
		t.Errorf("fresh snapshot: expected new-key, got %q", newSnap.config.APIKey)
	}
}

// TestAIService_N93_SetProjectRoot_ConcurrentPresetLookups_NoRace
// verifies that concurrent SetProjectRoot calls do not race with
// preset-lookup methods that read presetService and projectRoot.
func TestAIService_N93_SetProjectRoot_ConcurrentPresetLookups_NoRace(t *testing.T) {
	svc := NewAIService()

	var stop int32
	var wg sync.WaitGroup

	// Writer: continuously updates projectRoot.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; atomic.LoadInt32(&stop) == 0; i++ {
			root := "/proj-" + string(rune('a'+(i%26)))
			svc.setProjectRoot(root)
		}
	}()

	// Readers: continuously call preset lookup methods.
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for atomic.LoadInt32(&stop) == 0 {
				_, _ = svc.GetPresetPrompt("explain")
				_ = svc.ListPresets()
				_ = svc.ListPresetsWithSource()
			}
		}()
	}

	time.Sleep(100 * time.Millisecond)
	atomic.StoreInt32(&stop, 1)
	wg.Wait()
}

// TestAIService_N93_StartStream_UsesSnapshotNotLiveConfig verifies that
// StartStream takes a snapshot of the config before launching the goroutine,
// so a concurrent SetConfig call does not change the API key used by the
// in-flight stream. The stream should use the snapshot's API key, not the
// mutated value.
func TestAIService_N93_StartStream_UsesSnapshotNotLiveConfig(t *testing.T) {
	// Set up a test server that records the Authorization header.
	var seenAuth atomic.Value // string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth.Store(r.Header.Get("Authorization"))
		// Write a minimal SSE stream that ends immediately.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	// Build an app so StartStream can emit events.
	app := application.New(application.Options{})

	svc := NewAIService()
	svc.setApp(app)
	if err := svc.SetConfig(AIConfig{
		APIKey:  "snapshot-key",
		BaseURL: srv.URL,
		Model:   "test-model",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Start the stream (uses snapshot with snapshot-key).
	if _, err := svc.StartStream([]ChatMessage{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatalf("StartStream failed: %v", err)
	}

	// Immediately mutate the API key. The in-flight stream should still
	// use the snapshot's key ("snapshot-key"), not "mutated-key".
	if err := svc.SetConfig(AIConfig{
		APIKey:  "mutated-key",
		BaseURL: srv.URL,
		Model:   "test-model",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Wait for the server to receive the request.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v := seenAuth.Load(); v != nil {
			if v.(string) != "Bearer snapshot-key" {
				t.Errorf("expected stream to use snapshot key 'snapshot-key', got %q", v.(string))
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("test server never received the stream request")
}

// TestAIService_N93_StopStream_ConcurrentStartStop_NoRace verifies that
// concurrent StartStream and StopStream calls do not race on the cancel
// field. The cancel field is protected by a.mu (write lock).
// Uses a fast-responding SSE server to avoid goroutine leaks.
func TestAIService_N93_StopStream_ConcurrentStartStop_NoRace(t *testing.T) {
	// A fast SSE server that immediately sends a chunk and [DONE], then
	// returns. This avoids leaking goroutines that block on the handler.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	app := application.New(application.Options{})

	svc := NewAIService()
	svc.setApp(app)
	if err := svc.SetConfig(AIConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "test-model",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	var stop int32
	var wg sync.WaitGroup

	// Starter: repeatedly starts streams.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for atomic.LoadInt32(&stop) == 0 {
			_, _ = svc.StartStream([]ChatMessage{{Role: "user", Content: "x"}})
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// Stopper: repeatedly stops streams.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for atomic.LoadInt32(&stop) == 0 {
			_ = svc.StopStream()
			time.Sleep(2 * time.Millisecond)
		}
	}()

	time.Sleep(150 * time.Millisecond)
	atomic.StoreInt32(&stop, 1)
	wg.Wait()

	// Final cleanup: stop any lingering stream.
	_ = svc.StopStream()
	// Give goroutines time to finish.
	time.Sleep(50 * time.Millisecond)
}

// TestAIService_N93_PrepareMessagesWith_StandaloneFunction verifies the
// standalone helper produces the same output as the method form, and does
// not depend on the AIService receiver (so it can be used with a snapshot).
func TestAIService_N93_PrepareMessagesWith_StandaloneFunction(t *testing.T) {
	cfg := AIConfig{
		SystemPrompt:  "CUSTOM SYSTEM PROMPT",
		ContextWindow: 8000,
	}
	messages := []ChatMessage{
		{Role: "user", Content: "hello"},
	}

	got := prepareMessagesWith(cfg, messages)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(got))
	}
	if got[0].Role != "system" {
		t.Errorf("expected first message role 'system', got %q", got[0].Role)
	}
	if got[0].Content != "CUSTOM SYSTEM PROMPT" {
		t.Errorf("expected system content 'CUSTOM SYSTEM PROMPT', got %q", got[0].Content)
	}
	if got[1].Role != "user" || got[1].Content != "hello" {
		t.Errorf("expected user message preserved, got %+v", got[1])
	}
}

// TestAIService_N93_PrepareMessagesWith_FallsBackToDefaultPrompt verifies
// that when SystemPrompt is empty, the standalone helper uses DefaultSystemPrompt.
func TestAIService_N93_PrepareMessagesWith_FallsBackToDefaultPrompt(t *testing.T) {
	cfg := AIConfig{} // no SystemPrompt
	messages := []ChatMessage{{Role: "user", Content: "hi"}}

	got := prepareMessagesWith(cfg, messages)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Content != DefaultSystemPrompt {
		t.Errorf("expected DefaultSystemPrompt, got different content (len=%d vs %d)",
			len(got[0].Content), len(DefaultSystemPrompt))
	}
}

// TestAIService_N93_EffectiveSystemPromptFrom_Standalone verifies the
// standalone helper respects the config's SystemPrompt override.
func TestAIService_N93_EffectiveSystemPromptFrom_Standalone(t *testing.T) {
	tests := []struct {
		name string
		cfg  AIConfig
		want string
	}{
		{"empty falls back to default", AIConfig{}, DefaultSystemPrompt},
		{"non-empty uses override", AIConfig{SystemPrompt: "OVERRIDE"}, "OVERRIDE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveSystemPromptFrom(tt.cfg); got != tt.want {
				t.Errorf("effectiveSystemPromptFrom() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAIService_N93_MaxTokensFrom_Standalone verifies the standalone helper.
func TestAIService_N93_MaxTokensFrom_Standalone(t *testing.T) {
	tests := []struct {
		name string
		cfg  AIConfig
		want int
	}{
		{"zero falls back to default", AIConfig{}, defaultChatMaxTokens},
		{"positive uses override", AIConfig{MaxTokens: 8192}, 8192},
		{"negative falls back to default", AIConfig{MaxTokens: -1}, defaultChatMaxTokens},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maxTokensFrom(tt.cfg); got != tt.want {
				t.Errorf("maxTokensFrom() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestAIService_N93_ContextWindowFrom_Standalone verifies the standalone helper.
func TestAIService_N93_ContextWindowFrom_Standalone(t *testing.T) {
	tests := []struct {
		name string
		cfg  AIConfig
		want int
	}{
		{"zero falls back to default", AIConfig{}, defaultContextWindow},
		{"positive uses override", AIConfig{ContextWindow: 32000}, 32000},
		{"negative falls back to default", AIConfig{ContextWindow: -1}, defaultContextWindow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contextWindowFrom(tt.cfg); got != tt.want {
				t.Errorf("contextWindowFrom() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestAIService_N93_WithSystemPromptFrom_NoSystemPrompt verifies that
// when the config has no system prompt, messages are returned as-is.
func TestAIService_N93_WithSystemPromptFrom_NoSystemPrompt(t *testing.T) {
	// DefaultSystemPrompt is always non-empty, so withSystemPromptFrom always
	// prepends. To test the "no prepend" path, we'd need an empty default,
	// which isn't the case. Instead, verify it prepends the default.
	cfg := AIConfig{}
	messages := []ChatMessage{{Role: "user", Content: "hello"}}
	got := withSystemPromptFrom(cfg, messages)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages (default system + user), got %d", len(got))
	}
	preview := got[0].Content
	if len(preview) > 50 {
		preview = preview[:50]
	}
	if !strings.HasPrefix(got[0].Content, "You are") {
		t.Errorf("expected default system prompt starting with 'You are', got %q", preview)
	}
}
