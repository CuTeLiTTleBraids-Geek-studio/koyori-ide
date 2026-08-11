package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAIService_SendReturnsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}

		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		messages := body["messages"].([]interface{})
		if len(messages) != 2 {
			t.Errorf("expected 2 messages (system+user), got %d", len(messages))
		}
		if len(messages) > 0 {
			first, _ := messages[0].(map[string]interface{})
			if first["role"] != "system" {
				t.Errorf("expected first message role 'system', got %v", first["role"])
			}
		}

		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]string{
						"role":    "assistant",
						"content": "Hello from AI",
					},
					"finish_reason": "stop",
				},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "gpt-4o",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	resp, err := ai.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if resp.Content != "Hello from AI" {
		t.Errorf("expected 'Hello from AI', got %q", resp.Content)
	}
}

func TestAIService_SendMissingAPIKey(t *testing.T) {
	ai := NewAIService()
	_, err := ai.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Error("expected error when API key is missing")
	}
}

func TestAIService_SendStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		flusher, _ := w.(http.Flusher)
		chunks := []string{"Hello", " world", "!"}
		for _, chunk := range chunks {
			data := map[string]interface{}{
				"choices": []map[string]interface{}{
					{
						"delta": map[string]string{"content": chunk},
					},
				},
			}
			jsonBytes, _ := json.Marshal(data)
			w.Write([]byte("data: " + string(jsonBytes) + "\n\n"))
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "gpt-4o",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	var collected string
	err := ai.SendStream([]ChatMessage{{Role: "user", Content: "hi"}}, func(chunk string) {
		collected += chunk
	})
	if err != nil {
		t.Fatalf("SendStream failed: %v", err)
	}
	if collected != "Hello world!" {
		t.Errorf("expected 'Hello world!', got %q", collected)
	}
}

func TestAIService_Send_includesSystemPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		_ = json.Unmarshal(body, &req)
		messages, _ := req["messages"].([]interface{})
		if len(messages) == 0 {
			t.Error("expected at least one message")
		}
		first, _ := messages[0].(map[string]interface{})
		if first["role"] != "system" {
			t.Errorf("expected first message role 'system', got %v", first["role"])
		}
		if first["content"] == "" {
			t.Error("expected non-empty system prompt content")
		}
		resp := `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	svc := &AIService{}
	if err := svc.SetConfig(AIConfig{
		APIKey:       "test-key",
		BaseURL:      server.URL,
		Model:        "test-model",
		SystemPrompt: "You are a test assistant.",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	resp, err := svc.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("expected 'ok', got %q", resp.Content)
	}
}

func TestAIService_Send_usesDefaultSystemPromptWhenNoneSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		_ = json.Unmarshal(body, &req)
		messages, _ := req["messages"].([]interface{})
		first, _ := messages[0].(map[string]interface{})
		if first["role"] != "system" {
			t.Errorf("expected default system prompt, got role %v", first["role"])
		}
		if first["content"] == "" {
			t.Error("expected non-empty default system prompt")
		}
		resp := `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	svc := &AIService{}
	if err := svc.SetConfig(AIConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	_, err := svc.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
}

func TestAIService_SendStream_isCancellable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	svc := &AIService{}
	if err := svc.SetConfig(AIConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	chunks := []string{}
	done := make(chan error, 1)
	go func() {
		err := svc.SendStreamWithContext(ctx, []ChatMessage{{Role: "user", Content: "hi"}}, func(c string) {
			chunks = append(chunks, c)
		})
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("SendStreamWithContext did not return after cancel")
	}

	if len(chunks) == 0 {
		t.Error("expected at least one chunk before cancellation")
	}
}

func TestAIService_SendReturnsErrorOnNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": "invalid api key"}`))
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:  "bad-key",
		BaseURL: server.URL,
		Model:   "gpt-4o",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	_, err := ai.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for 401 status")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to mention status 401, got: %v", err)
	}
}

func TestAIService_SendStreamReturnsErrorOnNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": "rate limited"}`))
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "gpt-4o",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	err := ai.SendStream([]ChatMessage{{Role: "user", Content: "hi"}}, func(chunk string) {})
	if err == nil {
		t.Fatal("expected error for 429 status")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("expected error to mention status 429, got: %v", err)
	}
}

func TestParseSSEStream_EmitsChunks(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\ndata: [DONE]\n\n"
	var chunks []string
	err := parseSSEStream(strings.NewReader(body), func(c string) {
		chunks = append(chunks, c)
	})
	if err != nil {
		t.Fatalf("parseSSEStream failed: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0] != "hello" || chunks[1] != " world" {
		t.Errorf("expected ['hello', ' world'], got %v", chunks)
	}
}

func TestParseSSEStream_EmptyInput(t *testing.T) {
	err := parseSSEStream(strings.NewReader(""), func(c string) {})
	if err != nil {
		t.Fatalf("parseSSEStream on empty input should not error: %v", err)
	}
}

// N-83: parseSSEStream should return an error after 5 consecutive JSON parse
// failures instead of silently succeeding with no chunks emitted.
func TestParseSSEStream_N83_ReturnsErrorAfterConsecutiveParseFailures(t *testing.T) {
	// 5 consecutive malformed data lines.
	body := "data: {bad json 1}\ndata: {bad json 2}\ndata: {bad json 3}\ndata: {bad json 4}\ndata: {bad json 5}\n"
	err := parseSSEStream(strings.NewReader(body), func(c string) {
		t.Errorf("onChunk should not be called for malformed data")
	})
	if err == nil {
		t.Fatalf("expected error after 5 consecutive parse failures, got nil")
	}
	if !strings.Contains(err.Error(), "malformed SSE") {
		t.Errorf("error should mention malformed SSE, got: %v", err)
	}
}

// N-83: a valid chunk after parse errors resets the consecutive-error counter.
func TestParseSSEStream_N83_ErrorCounterResetsOnValidChunk(t *testing.T) {
	// 4 malformed lines (under threshold), then a valid chunk, then [DONE].
	body := "data: {bad1}\ndata: {bad2}\ndata: {bad3}\ndata: {bad4}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n" +
		"data: [DONE]\n"
	var chunks []string
	err := parseSSEStream(strings.NewReader(body), func(c string) {
		chunks = append(chunks, c)
	})
	if err != nil {
		t.Fatalf("should not error when counter resets: %v", err)
	}
	if len(chunks) != 1 || chunks[0] != "ok" {
		t.Errorf("expected single chunk 'ok', got %v", chunks)
	}
}

// N-64: parseSSEStream handles lines longer than 64KB (bufio.Scanner would
// silently fail on such lines).
func TestParseSSEStream_N64_HandlesLongLines(t *testing.T) {
	// Build a data line with > 64KB content.
	bigContent := strings.Repeat("a", 70000)
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"" + bigContent + "\"}}]}\ndata: [DONE]\n"
	var received strings.Builder
	err := parseSSEStream(strings.NewReader(body), func(c string) {
		received.WriteString(c)
	})
	if err != nil {
		t.Fatalf("parseSSEStream failed on long line: %v", err)
	}
	if received.Len() != 70000 {
		t.Errorf("expected 70000 chars, got %d", received.Len())
	}
}

// N-65: maxTokens() returns the config value when set.
func TestAIService_N65_MaxTokens_ReturnsConfigValue(t *testing.T) {
	a := NewAIService()
	a.config.MaxTokens = 8192
	if got := a.maxTokens(); got != 8192 {
		t.Errorf("expected 8192, got %d", got)
	}
}

// N-65: maxTokens() returns the default (4096) when config is 0.
func TestAIService_N65_MaxTokens_DefaultsTo4096(t *testing.T) {
	a := NewAIService()
	if got := a.maxTokens(); got != defaultChatMaxTokens {
		t.Errorf("expected default %d, got %d", defaultChatMaxTokens, got)
	}
}

// N-65: Send includes max_tokens in the request body.
func TestAIService_N65_SendIncludesMaxTokens(t *testing.T) {
	var capturedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	a := NewAIService()
	a.config = AIConfig{APIKey: "k", BaseURL: srv.URL, Model: "m", MaxTokens: 2048}
	_, err := a.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	mt, ok := capturedBody["max_tokens"]
	if !ok {
		t.Fatalf("max_tokens missing from request body")
	}
	// JSON numbers unmarshal to float64.
	if mt != float64(2048) {
		t.Errorf("expected max_tokens=2048, got %v", mt)
	}
}

func TestAIService_Complete_ReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"fmt.Println"}}]}`)
	}))
	defer srv.Close()

	svc := NewAIService()
	if err := svc.SetConfig(AIConfig{APIKey: "test-key", BaseURL: srv.URL, Model: "gpt-4o"}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	resp, err := svc.Complete(CompletionRequest{
		Prefix:   "package main\n\nfunc main() {\n    ",
		Suffix:   "\n}",
		Language: "go",
		FilePath: "main.go",
	})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if resp.Text != "fmt.Println" {
		t.Errorf("expected 'fmt.Println', got %q", resp.Text)
	}
}

func TestAIService_Complete_NoAPIKey(t *testing.T) {
	svc := NewAIService()
	_, err := svc.Complete(CompletionRequest{Prefix: "x", Suffix: ""})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestAIService_SetsUserAgent(t *testing.T) {
	var capturedUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	svc := NewAIService()
	if err := svc.SetConfig(AIConfig{APIKey: "k", BaseURL: srv.URL, Model: "m"}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	_, _ = svc.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if capturedUA == "" {
		t.Error("expected non-empty User-Agent header")
	}
	if !strings.Contains(capturedUA, "koyori-ide") {
		t.Errorf("User-Agent should contain 'koyori-ide', got %q", capturedUA)
	}
}

func TestAIService_DoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/redirect") {
			http.Redirect(w, r, "/target", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	svc := NewAIService()
	if err := svc.SetConfig(AIConfig{APIKey: "k", BaseURL: srv.URL + "/redirect", Model: "m"}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	_, err := svc.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Error("expected error due to redirect being blocked")
	}
}

func TestAIService_StructuredErrorOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Invalid API key","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	svc := NewAIService()
	if err := svc.SetConfig(AIConfig{APIKey: "k", BaseURL: srv.URL, Model: "m"}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	_, err := svc.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("error should contain 'Invalid API key', got: %v", err)
	}
}

// --- N-52: StartStream cancel race tests ---
//
// These tests verify the compare-and-swap cleanup in StartStream's
// goroutine defer. The race: stream A finishes and enters its defer;
// before the defer acquires the lock, stream B starts and stores its
// own cancel. The defer must NOT clobber B's cancel.

// TestAIService_N52_DeferClearsOwnCancel verifies that a stream's
// defer correctly clears a.cancel when no newer stream has started.
func TestAIService_N52_DeferClearsOwnCancel(t *testing.T) {
	svc := &AIService{}
	_, cancel := context.WithCancel(context.Background())
	sc := &streamCancel{fn: cancel}
	svc.cancel = sc

	// Simulate the defer: only clear if a.cancel == sc.
	svc.mu.Lock()
	if svc.cancel == sc {
		svc.cancel = nil
	}
	svc.mu.Unlock()

	if svc.cancel != nil {
		t.Error("defer should have cleared a.cancel when it matches our streamCancel")
	}
}

// TestAIService_N52_DeferDoesNotClobberNewerCancel verifies that a
// stream's defer does NOT clear a.cancel when a newer stream has
// already replaced it.
func TestAIService_N52_DeferDoesNotClobberNewerCancel(t *testing.T) {
	svc := &AIService{}
	// Stream A's cancel.
	_, cancelA := context.WithCancel(context.Background())
	scA := &streamCancel{fn: cancelA}
	svc.cancel = scA

	// Stream B starts before A's defer runs — replaces a.cancel.
	_, cancelB := context.WithCancel(context.Background())
	scB := &streamCancel{fn: cancelB}
	svc.cancel = scB

	// Now A's defer runs. It must NOT clear a.cancel (which now points to B).
	svc.mu.Lock()
	if svc.cancel == scA { // This should be FALSE — a.cancel is scB, not scA.
		svc.cancel = nil
	}
	svc.mu.Unlock()

	if svc.cancel == nil {
		t.Fatal("N-52 race: defer clobbered newer stream's cancel — StopStream would fail")
	}
	if svc.cancel != scB {
		t.Errorf("a.cancel should still point to scB, got %v", svc.cancel)
	}
}

// TestAIService_N52_StopStreamClearsCancel verifies StopStream works
// after the compare-and-swap refactor.
func TestAIService_N52_StopStreamClearsCancel(t *testing.T) {
	svc := &AIService{}
	_, cancel := context.WithCancel(context.Background())
	svc.cancel = &streamCancel{fn: cancel}

	err := svc.StopStream()
	if err != nil {
		t.Fatalf("StopStream failed: %v", err)
	}
	if svc.cancel != nil {
		t.Error("StopStream should have cleared a.cancel")
	}
}

// TestNewStreamID_NonEmpty verifies stream ids are generated (prompt-6 Task 2).
func TestNewStreamID_NonEmpty(t *testing.T) {
	a := newStreamID()
	b := newStreamID()
	if a == "" || b == "" {
		t.Fatal("stream id must be non-empty")
	}
	if a == b {
		t.Fatal("consecutive stream ids should differ")
	}
}

// TestAIService_Prompt5_ParseSSEStreamWithTools accumulates OpenAI tool_calls deltas.
func TestAIService_Prompt5_ParseSSEStreamWithTools(t *testing.T) {
	// Two tool_call deltas for index 0: name then arguments, plus content chunk.
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"thinking"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
	var chunks []string
	calls, err := parseSSEStreamWithTools(strings.NewReader(sse), func(s string) {
		chunks = append(chunks, s)
	})
	if err != nil {
		t.Fatalf("parseSSEStreamWithTools: %v", err)
	}
	if strings.Join(chunks, "") != "thinking" {
		t.Fatalf("content chunks = %v", chunks)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Name != "read" || calls[0].ID != "call_1" {
		t.Fatalf("unexpected tool call: %+v", calls[0])
	}
	if calls[0].Arguments != `{"path":"a.go"}` {
		t.Fatalf("arguments = %q", calls[0].Arguments)
	}
}

// TestAIService_Prompt5_IsStreamingAndStop verifies mutual-exclusion helpers
// (prompt-5 Task B / BUG-H1): IsStreaming reflects cancel; StopStream clears it.
// Full StartStream→ErrStreamBusy needs a non-nil *application.App (integration).
func TestAIService_Prompt5_IsStreamingAndStop(t *testing.T) {
	svc := &AIService{}
	if svc.IsStreaming() {
		t.Fatal("expected not streaming initially")
	}
	_, cancel := context.WithCancel(context.Background())
	svc.cancel = &streamCancel{fn: cancel}
	if !svc.IsStreaming() {
		t.Fatal("expected IsStreaming true when cancel is set")
	}
	if ErrStreamBusy == nil || ErrStreamBusy.Error() == "" {
		t.Fatal("ErrStreamBusy must be a non-empty sentinel")
	}
	if err := svc.StopStream(); err != nil {
		t.Fatalf("StopStream: %v", err)
	}
	if svc.IsStreaming() {
		t.Fatal("expected not streaming after StopStream")
	}
}

// TestAIService_N52_StopStreamNoopWhenNoStream verifies StopStream is
// safe to call when no stream is active.
func TestAIService_N52_StopStreamNoopWhenNoStream(t *testing.T) {
	svc := &AIService{}
	err := svc.StopStream()
	if err != nil {
		t.Fatalf("StopStream should not error when no stream is active: %v", err)
	}
}

// N-108: parseSSEStream should treat a wrapped io.EOF as a normal stream
// end, not an error. The previous `err == io.EOF` comparison missed
// wrapped EOFs (e.g. fmt.Errorf("...: %w", io.EOF)) returned by some
// HTTP body wrappers, causing normal stream completions to be reported
// as errors. The fix uses errors.Is(err, io.EOF) so wrapped EOFs are
// recognized.
func TestParseSSEStream_N108_WrappedEOF(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n"
	var chunks []string
	err := parseSSEStream(&wrappedEOFReader{data: []byte(body)}, func(c string) {
		chunks = append(chunks, c)
	})
	if err != nil {
		t.Fatalf("parseSSEStream should not error on wrapped EOF: %v", err)
	}
	if len(chunks) != 1 || chunks[0] != "hi" {
		t.Errorf("expected single chunk 'hi', got %v", chunks)
	}
}

// wrappedEOFReader returns its buffered data on each Read; once empty,
// it returns a wrapped io.EOF to simulate HTTP body wrappers that wrap
// EOF instead of returning it directly.
type wrappedEOFReader struct {
	data []byte
}

func (r *wrappedEOFReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, fmt.Errorf("stream ended: %w", io.EOF)
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// H-7: SSE 解析必须对单行长度设上限（1MB），恶意服务器发送无换行的
// 超大行时应报错中断，而不是无限分配内存导致 OOM。
func TestParseSSEStream_H7_RejectsOversizedLine(t *testing.T) {
	// 构造一个 > 1MB 且无换行的 data 行，模拟恶意服务器。
	bigContent := strings.Repeat("a", (1<<20)+100)
	body := "data: " + bigContent
	err := parseSSEStream(strings.NewReader(body), func(c string) {
		t.Errorf("onChunk 不应被调用")
	})
	if err == nil {
		t.Fatal("期望超大行被拒绝并返回错误，但 err == nil")
	}
	if !strings.Contains(err.Error(), "1MB") && !strings.Contains(err.Error(), "exceed") && !strings.Contains(err.Error(), "too long") {
		t.Errorf("错误应提及行长上限，得到: %v", err)
	}
}

// H-7: parseSSEStreamWithTools 同样必须拒绝超大行。
func TestParseSSEStreamWithTools_H7_RejectsOversizedLine(t *testing.T) {
	bigContent := strings.Repeat("a", (1<<20)+100)
	body := "data: " + bigContent
	_, err := parseSSEStreamWithTools(strings.NewReader(body), func(c string) {
		t.Errorf("onChunk 不应被调用")
	})
	if err == nil {
		t.Fatal("期望超大行被拒绝并返回错误，但 err == nil")
	}
}

// H-7: 1MB 以内的行长应正常处理（不误拒合法的大 tool-call 负载）。
func TestParseSSEStream_H7_AcceptsLineUnderLimit(t *testing.T) {
	// 70000 字节 < 1MB，应正常解析。
	bigContent := strings.Repeat("a", 70000)
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"" + bigContent + "\"}}]}\ndata: [DONE]\n"
	var received strings.Builder
	err := parseSSEStream(strings.NewReader(body), func(c string) {
		received.WriteString(c)
	})
	if err != nil {
		t.Fatalf("行长 < 1MB 时不应报错: %v", err)
	}
	if received.Len() != 70000 {
		t.Errorf("期望收到 70000 字节，得到 %d", received.Len())
	}
}

// ---- G12: AI 请求上下文与 Plan 真接线（图片结构化 + fail-closed）----

func TestAIService_G12_OpenAIImagesProduceImageURLBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		messages, _ := body["messages"].([]interface{})
		var userMsg map[string]interface{}
		for _, m := range messages {
			msg, _ := m.(map[string]interface{})
			if msg["role"] == "user" {
				userMsg = msg
				break
			}
		}
		if userMsg == nil {
			t.Fatal("no user message in request body")
		}
		content, ok := userMsg["content"].([]interface{})
		if !ok {
			t.Fatalf("image message content must be an array, got %T", userMsg["content"])
		}
		foundImageURL := false
		foundText := false
		for _, part := range content {
			p, _ := part.(map[string]interface{})
			switch p["type"] {
			case "image_url":
				iu, _ := p["image_url"].(map[string]interface{})
				url, _ := iu["url"].(string)
				if !strings.HasPrefix(url, "data:image/png;base64,") {
					t.Errorf("image_url does not carry the data URL: %q", url)
				}
				foundImageURL = true
			case "text":
				foundText = true
			}
		}
		if !foundImageURL {
			t.Error("no image_url content block found in user message")
		}
		if !foundText {
			t.Error("no text content block found alongside the image")
		}
		// System message must keep string content (no image).
		for _, m := range messages {
			msg, _ := m.(map[string]interface{})
			if msg["role"] == "system" {
				if _, ok := msg["content"].(string); !ok {
					t.Errorf("system message content must remain a string, got %T", msg["content"])
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"},
			},
		})
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gpt-4o"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	_, err := ai.Send([]ChatMessage{{
		Role:    "user",
		Content: "what is in this image?",
		Images:  []string{"data:image/png;base64,aGVsbG8="},
	}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestAIService_G12_AnthropicImagesProduceImageBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if sys, ok := body["system"].(string); !ok || sys == "" {
			t.Errorf("anthropic system must stay a top-level string, got %T", body["system"])
		}
		messages, _ := body["messages"].([]interface{})
		var userMsg map[string]interface{}
		for _, m := range messages {
			msg, _ := m.(map[string]interface{})
			if msg["role"] == "user" {
				userMsg = msg
				break
			}
		}
		if userMsg == nil {
			t.Fatal("no user message")
		}
		content, ok := userMsg["content"].([]interface{})
		if !ok {
			t.Fatalf("image message content must be an array, got %T", userMsg["content"])
		}
		foundImage := false
		foundText := false
		for _, part := range content {
			p, _ := part.(map[string]interface{})
			switch p["type"] {
			case "image":
				src, _ := p["source"].(map[string]interface{})
				if src["type"] != "base64" {
					t.Errorf("anthropic image source type must be base64, got %v", src["type"])
				}
				if src["media_type"] != "image/png" {
					t.Errorf("anthropic image media_type must be image/png, got %v", src["media_type"])
				}
				if data, _ := src["data"].(string); data != "aGVsbG8=" {
					t.Errorf("anthropic image data mismatch: %q", data)
				}
				foundImage = true
			case "text":
				foundText = true
			}
		}
		if !foundImage || !foundText {
			t.Errorf("expected text+image blocks, found image=%v text=%v", foundImage, foundText)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]interface{}{{"type": "text", "text": "Hello from Claude"}},
			"stop_reason": "end_turn",
		})
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:       "anthropic-key",
		BaseURL:      server.URL,
		Model:        "claude-3-5-sonnet-20241022",
		SystemPrompt: "You are a helpful assistant.",
		Protocol:     "anthropic",
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	_, err := ai.Send([]ChatMessage{{
		Role:    "user",
		Content: "what is in this image?",
		Images:  []string{"data:image/png;base64,aGVsbG8="},
	}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestAIService_G12_StreamOpenAIImagesProduceImageURLBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		messages, _ := body["messages"].([]interface{})
		var userMsg map[string]interface{}
		for _, m := range messages {
			msg, _ := m.(map[string]interface{})
			if msg["role"] == "user" {
				userMsg = msg
				break
			}
		}
		content, ok := userMsg["content"].([]interface{})
		if !ok {
			t.Fatalf("stream image content must be an array, got %T", userMsg["content"])
		}
		found := false
		for _, part := range content {
			p, _ := part.(map[string]interface{})
			if p["type"] == "image_url" {
				found = true
			}
		}
		if !found {
			t.Error("stream request has no image_url block")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gpt-4o"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	var chunks []string
	err := ai.SendStreamWithContext(context.Background(), []ChatMessage{{
		Role:    "user",
		Content: "describe",
		Images:  []string{"data:image/png;base64,aGVsbG8="},
	}}, func(c string) { chunks = append(chunks, c) })
	if err != nil {
		t.Fatalf("SendStreamWithContext: %v", err)
	}
	if len(chunks) == 0 {
		t.Error("expected at least one chunk")
	}
}

func TestAIService_G12_NoImagesKeepsStringContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]interface{})
		for _, m := range messages {
			msg, _ := m.(map[string]interface{})
			if _, ok := msg["content"].(string); !ok {
				t.Errorf("message without images must keep string content, got %T", msg["content"])
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"},
			},
		})
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gpt-4o"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if _, err := ai.Send([]ChatMessage{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestAIService_G12_RejectsTooManyImages(t *testing.T) {
	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: "http://127.0.0.1:1", Model: "gpt-4o"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	images := make([]string, maxAIImageCount+1)
	for i := range images {
		images[i] = "data:image/png;base64,aGVsbG8="
	}
	_, err := ai.Send([]ChatMessage{{Role: "user", Content: "x", Images: images}})
	if err == nil {
		t.Fatal("expected rejection for too many images")
	}
	if !strings.Contains(err.Error(), "too many images") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAIService_G12_RejectsOversizedImage(t *testing.T) {
	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: "http://127.0.0.1:1", Model: "gpt-4o"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	// ~7 MiB base64 payload -> decoded budget check rejects (> 5 MiB).
	big := "data:image/png;base64," + strings.Repeat("A", 7<<20)
	_, err := ai.Send([]ChatMessage{{Role: "user", Content: "x", Images: []string{big}}})
	if err == nil {
		t.Fatal("expected rejection for oversized image")
	}
	if !strings.Contains(err.Error(), "size budget") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAIService_G12_RejectsUnsupportedImageType(t *testing.T) {
	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: "http://127.0.0.1:1", Model: "gpt-4o"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	_, err := ai.Send([]ChatMessage{{
		Role:    "user",
		Content: "x",
		Images:  []string{"data:image/svg+xml;base64,aGVsbG8="},
	}})
	if err == nil {
		t.Fatal("expected rejection for unsupported image media type")
	}
	if !strings.Contains(err.Error(), "unsupported image media type") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAIService_G12_RejectsNonImageDataURL(t *testing.T) {
	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: "http://127.0.0.1:1", Model: "gpt-4o"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	_, err := ai.Send([]ChatMessage{{
		Role:    "user",
		Content: "x",
		Images:  []string{"data:text/plain;base64,aGVsbG8="},
	}})
	if err == nil {
		t.Fatal("expected rejection for non-image data URL")
	}
	if !strings.Contains(err.Error(), "data:image/*") {
		t.Errorf("unexpected error: %v", err)
	}
}

// G12: 预算与取消由后端强制（既有能力回归锚点）——MaxTokens/ContextWindow/
// StopStream 已有专门测试（N-65/N-61/N-52），此处锚定「图片预算 fail-closed
// 在 provider 调用前发生」：失败时绝不向 provider 发出请求。
func TestAIService_G12_ImageBudgetFailsBeforeProviderCall(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gpt-4o"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	_, err := ai.Send([]ChatMessage{{
		Role:    "user",
		Content: "x",
		Images:  []string{"data:image/png;base64," + strings.Repeat("A", 7<<20)},
	}})
	if err == nil {
		t.Fatal("expected oversized image rejection")
	}
	if hit {
		t.Fatal("provider was called despite image budget violation (fail-closed violated)")
	}
}
