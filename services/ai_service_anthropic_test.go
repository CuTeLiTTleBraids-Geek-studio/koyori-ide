package services

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// TestAnthropicProtocol_Send verifies the non-streaming Anthropic path:
//   - URL is /v1/messages (not /v1/chat/completions)
//   - Auth header is x-api-key (not Bearer)
//   - anthropic-version header is set
//   - System prompt is lifted out of messages into top-level "system" field
//   - Response content[0].text is mapped to ChatResponse.Content
//   - stop_reason is mapped to FinishReason
func TestAnthropicProtocol_Send(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("anthropic: expected /v1/messages, got %s", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "anthropic-key" {
			t.Errorf("anthropic: expected x-api-key header 'anthropic-key', got %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic: expected anthropic-version '2023-06-01', got %q", got)
		}
		// Bearer must NOT be set for Anthropic
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("anthropic: Authorization header should be empty, got %q", got)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("anthropic: failed to decode request body: %v", err)
		}
		// System prompt must be a top-level field, NOT inside messages
		sys, ok := body["system"].(string)
		if !ok || sys == "" {
			t.Errorf("anthropic: expected non-empty top-level 'system' field, got %v", body["system"])
		}
		messages, _ := body["messages"].([]interface{})
		for _, m := range messages {
			msg := m.(map[string]interface{})
			if msg["role"] == "system" {
				t.Errorf("anthropic: 'system' role must not appear inside messages; got %v", msg)
			}
		}
		// temperature must be present
		if _, ok := body["temperature"]; !ok {
			t.Error("anthropic: expected 'temperature' field in request body")
		}
		// max_tokens must be present (Anthropic requires it)
		if _, ok := body["max_tokens"]; !ok {
			t.Error("anthropic: expected 'max_tokens' field in request body")
		}

		resp := map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "Hello from Claude"},
			},
			"stop_reason": "end_turn",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:       "anthropic-key",
		BaseURL:      server.URL,
		Model:        "claude-3-5-sonnet-20241022",
		SystemPrompt: "You are a helpful assistant.",
		Protocol:     "anthropic",
		Temperature:  0.5,
		MaxTokens:    1024,
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	resp, err := ai.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("anthropic Send failed: %v", err)
	}
	if resp.Content != "Hello from Claude" {
		t.Errorf("anthropic: expected content 'Hello from Claude', got %q", resp.Content)
	}
	if resp.FinishReason != "end_turn" {
		t.Errorf("anthropic: expected finish_reason 'end_turn', got %q", resp.FinishReason)
	}
}

// TestAnthropicProtocol_SendStream verifies the streaming Anthropic path:
//   - SSE events of type content_block_delta emit text_delta chunks
//   - message_stop event ends the stream
//   - Chunks are emitted in order
func TestAnthropicProtocol_SendStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("anthropic stream: expected /v1/messages, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		flusher, _ := w.(http.Flusher)
		events := []string{
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":", "}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"world!"}}` + "\n\n",
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
			flusher.Flush()
		}
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:   "anthropic-key",
		BaseURL:  server.URL,
		Model:    "claude-3-5-sonnet-20241022",
		Protocol: "anthropic",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	var collected string
	err := ai.SendStream([]ChatMessage{{Role: "user", Content: "hi"}}, func(chunk string) {
		collected += chunk
	})
	if err != nil {
		t.Fatalf("anthropic SendStream failed: %v", err)
	}
	if collected != "Hello, world!" {
		t.Errorf("anthropic stream: expected 'Hello, world!', got %q", collected)
	}
}

// TestAnthropicProtocol_StartStream_EmitsEvents verifies that StartStream
// emits ai:chunk and ai:done events through the application event bus,
// which is what the frontend listens for.
func TestAnthropicProtocol_StartStream_EmitsEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		events := []string{
			`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"streamed-chunk"}}` + "\n\n",
			`data: {"type":"message_stop"}` + "\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
			flusher.Flush()
		}
	}))
	defer server.Close()

	ai := NewAIService()
	app := application.New(application.Options{})
	ai.setApp(app)
	if err := ai.SetConfig(AIConfig{
		APIKey:   "anthropic-key",
		BaseURL:  server.URL,
		Model:    "claude-3-5-sonnet-20241022",
		Protocol: "anthropic",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Critical 自审修复：Wails EventProcessor 在独立 goroutine 中调用 listener
	// （写入 chunks/streamIDs/doneEmitted），主测试 goroutine 读取这些变量。
	// 没有 mutex 保护会导致 go test -race 在并行执行时报告 data race，
	// 进而干扰 N93 等并发测试。这是测试代码自身的 BUG，非被测代码 BUG。
	var mu sync.Mutex
	var chunks []string
	var streamIDs []string
	var doneEmitted bool
	app.Event.On("ai:chunk", func(e *application.CustomEvent) {
		mu.Lock()
		defer mu.Unlock()
		// prompt-6 Task 2: payload is {streamId, data}
		if m, ok := e.Data.(map[string]interface{}); ok {
			if s, ok := m["data"].(string); ok {
				chunks = append(chunks, s)
			}
			if id, ok := m["streamId"].(string); ok {
				streamIDs = append(streamIDs, id)
			}
			return
		}
		// Legacy string payload (should not happen after Task 2).
		if s, ok := e.Data.(string); ok {
			chunks = append(chunks, s)
		}
	})
	app.Event.On("ai:done", func(e *application.CustomEvent) {
		mu.Lock()
		defer mu.Unlock()
		doneEmitted = true
	})

	sid, err := ai.StartStream([]ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("StartStream failed: %v", err)
	}
	if sid == "" {
		t.Fatal("StartStream should return a non-empty streamId")
	}
	// StartStream is async; poll for completion.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := doneEmitted
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	done := doneEmitted
	collectedChunks := append([]string(nil), chunks...)
	collectedStreamIDs := append([]string(nil), streamIDs...)
	mu.Unlock()
	if !done {
		t.Fatal("anthropic StartStream: ai:done event was not emitted")
	}
	if len(collectedChunks) == 0 || collectedChunks[0] != "streamed-chunk" {
		t.Errorf("anthropic StartStream: expected chunks ['streamed-chunk'], got %v", collectedChunks)
	}
	if len(collectedStreamIDs) > 0 && collectedStreamIDs[0] != sid {
		t.Errorf("chunk streamId %q != StartStream return %q", collectedStreamIDs[0], sid)
	}
}

// TestAnthropicProtocol_Complete_Rejects verifies that inline completion
// is rejected for Anthropic protocol (current implementation limitation).
func TestAnthropicProtocol_Complete_Rejects(t *testing.T) {
	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:   "anthropic-key",
		BaseURL:  "https://example.com",
		Model:    "claude-3-5-sonnet-20241022",
		Protocol: "anthropic",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	_, err := ai.Complete(CompletionRequest{
		Prefix:   "func ",
		Suffix:   "()",
		Language: "go",
	})
	if err == nil {
		t.Error("anthropic: expected Complete to reject with error, got nil")
	} else if !strings.Contains(err.Error(), "Anthropic") {
		t.Errorf("anthropic: expected error mentioning 'Anthropic', got %q", err.Error())
	}
}

// TestAnthropicProtocol_GenerateTitleWithAI_Rejects verifies that title
// generation is rejected for Anthropic protocol.
func TestAnthropicProtocol_GenerateTitleWithAI_Rejects(t *testing.T) {
	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:   "anthropic-key",
		BaseURL:  "https://example.com",
		Model:    "claude-3-5-sonnet-20241022",
		Protocol: "anthropic",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	_, err := ai.GenerateTitleWithAI("user message text")
	if err == nil {
		t.Error("anthropic: expected GenerateTitleWithAI to reject with error, got nil")
	} else if !strings.Contains(err.Error(), "Anthropic") {
		t.Errorf("anthropic: expected error mentioning 'Anthropic', got %q", err.Error())
	}
}

// TestParseAnthropicSSEStream_EmitsChunks verifies the SSE parser directly.
func TestParseAnthropicSSEStream_EmitsChunks(t *testing.T) {
	body := strings.NewReader(
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"foo"}}` + "\n\n" +
			`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"bar"}}` + "\n\n" +
			`data: {"type":"message_stop"}` + "\n\n",
	)
	var chunks []string
	err := parseAnthropicSSEStream(body, func(c string) { chunks = append(chunks, c) })
	if err != nil {
		t.Fatalf("parseAnthropicSSEStream failed: %v", err)
	}
	if len(chunks) != 2 || chunks[0] != "foo" || chunks[1] != "bar" {
		t.Errorf("expected ['foo','bar'], got %v", chunks)
	}
}

// TestParseAnthropicSSEStream_EmptyInput verifies the parser handles empty
// input gracefully (returns nil, no chunks).
func TestParseAnthropicSSEStream_EmptyInput(t *testing.T) {
	err := parseAnthropicSSEStream(strings.NewReader(""), func(string) {
		t.Error("onChunk should not be called for empty input")
	})
	if err != nil {
		t.Errorf("expected nil error for empty input, got %v", err)
	}
}

// TestParseAnthropicSSEStream_IgnoresOrphanInputJSON verifies that
// input_json_delta without a prior tool_use content_block_start does not
// produce text chunks (text still streams).
func TestParseAnthropicSSEStream_IgnoresOrphanInputJSON(t *testing.T) {
	body := strings.NewReader(
		`data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"\"foo\""}}` + "\n\n" +
			`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
			`data: {"type":"message_stop"}` + "\n\n",
	)
	var chunks []string
	err := parseAnthropicSSEStream(body, func(c string) { chunks = append(chunks, c) })
	if err != nil {
		t.Fatalf("parseAnthropicSSEStream failed: %v", err)
	}
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("expected only ['hello'] (orphan input_json is not text), got %v", chunks)
	}
}

// TestParseAnthropicSSEStreamWithTools_ToolUse accumulates tool_use blocks
// (prompt-6 Task 3 / BUG-H5).
func TestParseAnthropicSSEStreamWithTools_ToolUse(t *testing.T) {
	body := strings.NewReader(strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'll read it."}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n"))
	var chunks []string
	calls, err := parseAnthropicSSEStreamWithTools(body, func(c string) {
		chunks = append(chunks, c)
	})
	if err != nil {
		t.Fatalf("parseAnthropicSSEStreamWithTools: %v", err)
	}
	if strings.Join(chunks, "") != "I'll read it." {
		t.Fatalf("text chunks = %v", chunks)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Name != "read" || calls[0].ID != "toolu_1" {
		t.Fatalf("unexpected tool call: %+v", calls[0])
	}
	if calls[0].Arguments != `{"path":"a.go"}` {
		t.Fatalf("arguments = %q", calls[0].Arguments)
	}
}

// TestParseAnthropicSSEStream_N83_ConsecutiveParseErrors verifies that
// after 5 consecutive malformed chunks the parser returns an error.
func TestParseAnthropicSSEStream_N83_ConsecutiveParseErrors(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 6; i++ {
		sb.WriteString("data: {not valid json}\n\n")
	}
	err := parseAnthropicSSEStream(strings.NewReader(sb.String()), func(string) {})
	if err == nil {
		t.Error("expected error after 5 consecutive parse failures, got nil")
	}
}

// TestSplitSystemPrompt verifies that system messages are extracted into
// a separate string and the remaining messages contain no system role.
func TestSplitSystemPrompt(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "You are a test assistant."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
		{Role: "system", Content: "Additional rules."},
		{Role: "user", Content: "Bye"},
	}
	sys, chat := splitSystemPrompt(messages)
	if !strings.Contains(sys, "You are a test assistant.") {
		t.Errorf("expected system prompt to contain first system message, got %q", sys)
	}
	if !strings.Contains(sys, "Additional rules.") {
		t.Errorf("expected system prompt to contain second system message, got %q", sys)
	}
	if len(chat) != 3 {
		t.Errorf("expected 3 non-system messages, got %d", len(chat))
	}
	for _, m := range chat {
		if m.Role == "system" {
			t.Error("chat messages should not contain any system role entries")
		}
	}
}

// TestSetProtocolHeaders_OpenAI verifies the OpenAI/default protocol sets
// Bearer auth and no anthropic-version header.
func TestSetProtocolHeaders_OpenAI(t *testing.T) {
	req := httptest.NewRequest("POST", "https://example.com", nil)
	setProtocolHeaders(req, AIConfig{
		APIKey:   "openai-key",
		Protocol: "openai",
	})
	if got := req.Header.Get("Authorization"); got != "Bearer openai-key" {
		t.Errorf("openai: expected 'Bearer openai-key', got %q", got)
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Errorf("openai: x-api-key should be empty, got %q", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "" {
		t.Errorf("openai: anthropic-version should be empty, got %q", got)
	}
}

// TestSetProtocolHeaders_Anthropic verifies the Anthropic protocol sets
// x-api-key + anthropic-version and NO Bearer auth.
func TestSetProtocolHeaders_Anthropic(t *testing.T) {
	req := httptest.NewRequest("POST", "https://example.com", nil)
	setProtocolHeaders(req, AIConfig{
		APIKey:   "anthropic-key",
		Protocol: "anthropic",
	})
	if got := req.Header.Get("x-api-key"); got != "anthropic-key" {
		t.Errorf("anthropic: expected x-api-key 'anthropic-key', got %q", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic: expected anthropic-version '2023-06-01', got %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("anthropic: Authorization should be empty, got %q", got)
	}
}

// TestSetProtocolHeaders_EmptyProtocolDefaultsToOpenAI verifies that an
// empty Protocol field defaults to OpenAI Bearer auth.
func TestSetProtocolHeaders_EmptyProtocolDefaultsToOpenAI(t *testing.T) {
	req := httptest.NewRequest("POST", "https://example.com", nil)
	setProtocolHeaders(req, AIConfig{
		APIKey:   "some-key",
		Protocol: "",
	})
	if got := req.Header.Get("Authorization"); got != "Bearer some-key" {
		t.Errorf("default: expected 'Bearer some-key', got %q", got)
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Errorf("default: x-api-key should be empty, got %q", got)
	}
}

// TestEffectiveTemperature verifies clamping behavior.
func TestEffectiveTemperature(t *testing.T) {
	cases := []struct {
		input  float64
		expect float64
	}{
		{0, 0.7},   // 0 → default 0.7
		{-1, 0.7},  // negative → default 0.7
		{0.5, 0.5}, // valid → unchanged
		{2, 2},     // upper bound → unchanged
		{2.5, 2},   // > 2 → clamped to 2
		{1.0, 1.0}, // valid → unchanged
	}
	for _, c := range cases {
		got := effectiveTemperature(AIConfig{Temperature: c.input})
		if got != c.expect {
			t.Errorf("effectiveTemperature(%v) = %v, want %v", c.input, got, c.expect)
		}
	}
}

// TestAnthropicProtocol_SendIncludesTemperatureAndMaxTokens verifies that
// user-configured temperature and maxTokens are passed through to the
// Anthropic request body (not just the defaults).
func TestAnthropicProtocol_SendIncludesTemperatureAndMaxTokens(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		resp := `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:      "anthropic-key",
		BaseURL:     server.URL,
		Model:       "claude-3-5-sonnet-20241022",
		Protocol:    "anthropic",
		Temperature: 1.2,
		MaxTokens:   2048,
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	_, err := ai.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if got := captured["temperature"]; got != 1.2 {
		t.Errorf("expected temperature 1.2, got %v", got)
	}
	if got := captured["max_tokens"]; got != float64(2048) {
		t.Errorf("expected max_tokens 2048, got %v", got)
	}
}

// TestAnthropicProtocol_SendMissingAPIKey verifies that Anthropic protocol
// also enforces the API key requirement.
func TestAnthropicProtocol_SendMissingAPIKey(t *testing.T) {
	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:   "",
		BaseURL:  "https://example.com",
		Model:    "claude-3-5-sonnet-20241022",
		Protocol: "anthropic",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	_, err := ai.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Error("anthropic: expected error when API key is missing")
	}
}

// TestAnthropicProtocol_SendStreamWithContext verifies that the streaming
// Anthropic path respects context cancellation.
func TestAnthropicProtocol_SendStreamWithContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		// Emit one chunk then stall until the client cancels.
		w.Write([]byte(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"first"}}` + "\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	ai := NewAIService()
	if err := ai.SetConfig(AIConfig{
		APIKey:   "anthropic-key",
		BaseURL:  server.URL,
		Model:    "claude-3-5-sonnet-20241022",
		Protocol: "anthropic",
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var chunks []string
	err := ai.SendStreamWithContext(ctx, []ChatMessage{{Role: "user", Content: "hi"}}, func(c string) {
		chunks = append(chunks, c)
	})
	if err == nil {
		t.Log("SendStreamWithContext returned nil (context cancellation may be reported as EOF)")
	}
	if len(chunks) == 0 {
		t.Error("expected at least one chunk before context cancellation")
	}
}

// H-7: Anthropic SSE 解析同样必须对单行长度设上限（1MB）。
func TestParseAnthropicSSEStream_H7_RejectsOversizedLine(t *testing.T) {
	bigContent := strings.Repeat("a", (1<<20)+100)
	body := "data: " + bigContent
	err := parseAnthropicSSEStream(strings.NewReader(body), func(c string) {
		t.Errorf("onChunk 不应被调用")
	})
	if err == nil {
		t.Fatal("期望超大行被拒绝并返回错误，但 err == nil")
	}
}

// H-7: parseAnthropicSSEStreamWithTools 同样必须拒绝超大行。
func TestParseAnthropicSSEStreamWithTools_H7_RejectsOversizedLine(t *testing.T) {
	bigContent := strings.Repeat("a", (1<<20)+100)
	body := "data: " + bigContent
	_, err := parseAnthropicSSEStreamWithTools(strings.NewReader(body), func(c string) {
		t.Errorf("onChunk 不应被调用")
	})
	if err == nil {
		t.Fatal("期望超大行被拒绝并返回错误，但 err == nil")
	}
}
