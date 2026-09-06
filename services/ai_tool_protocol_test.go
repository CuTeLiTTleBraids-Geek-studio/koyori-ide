package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type capturedToolProtocolRequest struct {
	path string
	body map[string]interface{}
}

func requireToolProtocolJSONEqual(t *testing.T, label string, got, want interface{}) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	t.Fatalf("%s mismatch\ngot:  %s\nwant: %s", label, gotJSON, wantJSON)
}

func TestAIServiceNativeToolProtocolHTTP(t *testing.T) {
	t.Run("OpenAI second request uses assistant tool_calls and tool messages", func(t *testing.T) {
		var hits atomic.Int32
		requests := make(chan capturedToolProtocolRequest, 2)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			requests <- capturedToolProtocolRequest{path: r.URL.Path, body: body}
			requestNumber := hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if requestNumber == 1 {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"choices": []interface{}{map[string]interface{}{
						"message": map[string]interface{}{
							"role": "assistant", "content": "",
							"tool_calls": []interface{}{map[string]interface{}{
								"id": "call_read", "type": "function",
								"function": map[string]interface{}{
									"name": "read", "arguments": `{"path":"README.md"}`,
								},
							}},
						},
						"finish_reason": "tool_calls",
					}},
				})
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
		}))
		defer server.Close()

		service := NewAIService()
		if err := service.SetConfig(AIConfig{
			APIKey: "test-key", BaseURL: server.URL, Model: "test-model", SystemPrompt: "native-system",
		}); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		if _, err := service.Send([]ChatMessage{{Role: "user", Content: "inspect"}}); err != nil {
			t.Fatalf("first Send: %v", err)
		}

		calls := []NativeToolCall{
			{ID: "call_read", Name: "read", Arguments: `{"path":"README.md"}`},
			{ID: "call_search", Name: "search", Arguments: `{"query":"TODO","ignoreCase":true}`},
		}
		results := []NativeToolResult{
			{ToolCallID: "call_read", Content: "file body"},
			{ToolCallID: "call_search", Content: "search failed", IsError: true},
		}
		if _, err := service.Send([]ChatMessage{
			{Role: "user", Content: "inspect"},
			{Role: "assistant", Content: "I will inspect it.", ToolCalls: calls},
			{Role: "tool", ToolResults: results},
		}); err != nil {
			t.Fatalf("second Send: %v", err)
		}

		<-requests // The first provider request only establishes the real two-turn sequence.
		second := <-requests
		if second.path != "/v1/chat/completions" {
			t.Fatalf("second request path = %q", second.path)
		}
		requireToolProtocolJSONEqual(t, "OpenAI second-request messages", second.body["messages"], []interface{}{
			map[string]interface{}{"role": "system", "content": "native-system"},
			map[string]interface{}{"role": "user", "content": "inspect"},
			map[string]interface{}{
				"role": "assistant", "content": "I will inspect it.",
				"tool_calls": []interface{}{
					map[string]interface{}{
						"id": "call_read", "type": "function",
						"function": map[string]interface{}{"name": "read", "arguments": `{"path":"README.md"}`},
					},
					map[string]interface{}{
						"id": "call_search", "type": "function",
						"function": map[string]interface{}{"name": "search", "arguments": `{"query":"TODO","ignoreCase":true}`},
					},
				},
			},
			map[string]interface{}{"role": "tool", "tool_call_id": "call_read", "content": "file body"},
			map[string]interface{}{"role": "tool", "tool_call_id": "call_search", "content": "search failed"},
		})
		if got := hits.Load(); got != 2 {
			t.Fatalf("provider hits = %d, want 2", got)
		}
	})

	t.Run("Anthropic second request uses tool_use and tool_result blocks", func(t *testing.T) {
		var hits atomic.Int32
		requests := make(chan capturedToolProtocolRequest, 2)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			requests <- capturedToolProtocolRequest{path: r.URL.Path, body: body}
			requestNumber := hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if requestNumber == 1 {
				_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","id":"toolu_read","name":"read","input":{"path":"README.md"}}],"stop_reason":"tool_use"}`))
				return
			}
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`))
		}))
		defer server.Close()

		service := NewAIService()
		if err := service.SetConfig(AIConfig{
			APIKey: "test-key", BaseURL: server.URL, Model: "test-model", Protocol: "anthropic", SystemPrompt: "native-system",
		}); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		if _, err := service.Send([]ChatMessage{{Role: "user", Content: "inspect"}}); err != nil {
			t.Fatalf("first Send: %v", err)
		}

		if _, err := service.Send([]ChatMessage{
			{Role: "user", Content: "inspect"},
			{Role: "assistant", Content: "I will inspect it.", ToolCalls: []NativeToolCall{
				{ID: "toolu_read", Name: "read", Arguments: `{"path":"README.md"}`},
				{ID: "toolu_search", Name: "search", Arguments: `{"query":"TODO","ignoreCase":true}`},
			}},
			{Role: "tool", ToolResults: []NativeToolResult{
				{ToolCallID: "toolu_read", Content: "file body"},
				{ToolCallID: "toolu_search", Content: "search failed", IsError: true},
			}},
		}); err != nil {
			t.Fatalf("second Send: %v", err)
		}

		<-requests
		second := <-requests
		if second.path != "/v1/messages" {
			t.Fatalf("second request path = %q", second.path)
		}
		requireToolProtocolJSONEqual(t, "Anthropic second-request messages", second.body["messages"], []interface{}{
			map[string]interface{}{"role": "user", "content": "inspect"},
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "I will inspect it."},
					map[string]interface{}{"type": "tool_use", "id": "toolu_read", "name": "read", "input": map[string]interface{}{"path": "README.md"}},
					map[string]interface{}{"type": "tool_use", "id": "toolu_search", "name": "search", "input": map[string]interface{}{"query": "TODO", "ignoreCase": true}},
				},
			},
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_read", "content": "file body"},
					map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_search", "content": "search failed", "is_error": true},
				},
			},
		})
		if got := hits.Load(); got != 2 {
			t.Fatalf("provider hits = %d, want 2", got)
		}
	})
}

func TestAIServiceNativeToolProtocolFailsClosedBeforeHTTP(t *testing.T) {
	validCall := NativeToolCall{ID: "call_1", Name: "read", Arguments: `{}`}
	tests := []struct {
		name       string
		messages   []ChatMessage
		wantErrSub string
	}{
		{
			name: "missing ID",
			messages: []ChatMessage{
				{Role: "assistant", ToolCalls: []NativeToolCall{{Name: "read", Arguments: `{}`}}},
				{Role: "tool", ToolResults: []NativeToolResult{{Content: "x"}}},
			},
			wantErrSub: "ID is missing",
		},
		{
			name:       "orphan result",
			messages:   []ChatMessage{{Role: "tool", ToolResults: []NativeToolResult{{ToolCallID: "orphan", Content: "x"}}}},
			wantErrSub: "unknown call",
		},
		{
			name: "duplicate call ID",
			messages: []ChatMessage{
				{Role: "assistant", ToolCalls: []NativeToolCall{validCall, validCall}},
				{Role: "tool", ToolResults: []NativeToolResult{{ToolCallID: "call_1", Content: "x"}}},
			},
			wantErrSub: "duplicate native tool call ID",
		},
		{
			name: "duplicate result",
			messages: []ChatMessage{
				{Role: "assistant", ToolCalls: []NativeToolCall{validCall}},
				{Role: "tool", ToolResults: []NativeToolResult{
					{ToolCallID: "call_1", Content: "x"},
					{ToolCallID: "call_1", Content: "again"},
				}},
			},
			wantErrSub: "duplicate native tool result",
		},
		{
			name: "partial results",
			messages: []ChatMessage{
				{Role: "assistant", ToolCalls: []NativeToolCall{
					validCall,
					{ID: "call_2", Name: "search", Arguments: `{}`},
				}},
				{Role: "tool", ToolResults: []NativeToolResult{{ToolCallID: "call_1", Content: "x"}}},
			},
			wantErrSub: "has no terminal result",
		},
	}
	protocols := []struct {
		name     string
		protocol string
	}{
		{name: "openai", protocol: "openai"},
		{name: "anthropic", protocol: "anthropic"},
	}

	for _, protocol := range protocols {
		protocol := protocol
		for _, test := range tests {
			test := test
			t.Run(protocol.name+"/"+test.name, func(t *testing.T) {
				var hits atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					hits.Add(1)
					http.Error(w, "provider must not be reached", http.StatusInternalServerError)
				}))
				defer server.Close()

				service := NewAIService()
				if err := service.SetConfig(AIConfig{
					APIKey: "test-key", BaseURL: server.URL, Model: "test-model", Protocol: protocol.protocol,
				}); err != nil {
					t.Fatalf("SetConfig: %v", err)
				}
				if _, err := service.Send(test.messages); err == nil {
					t.Fatal("Send accepted malformed native tool protocol")
				} else if !strings.Contains(err.Error(), test.wantErrSub) {
					t.Fatalf("Send error = %q, want substring %q", err, test.wantErrSub)
				}
				if got := hits.Load(); got != 0 {
					t.Fatalf("provider hits = %d, want 0", got)
				}
			})
		}
	}
}

func TestOpenAIMessagesPreserveNativeToolRound(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "read the file"},
		{
			Role:    "assistant",
			Content: "I will inspect it.",
			ToolCalls: []NativeToolCall{{
				ID: "call_read_1", Name: "read", Arguments: `{"path":"README.md"}`,
			}},
		},
		{
			Role: "tool",
			ToolResults: []NativeToolResult{{
				ToolCallID: "call_read_1", Content: "file body",
			}},
		},
	}
	if err := validateChatMessages(messages); err != nil {
		t.Fatalf("validateChatMessages: %v", err)
	}
	encoded := openAIMessages(messages)
	if len(encoded) != 3 {
		t.Fatalf("OpenAI messages = %d, want 3: %#v", len(encoded), encoded)
	}
	toolCalls, ok := encoded[1]["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("assistant tool_calls = %#v", encoded[1]["tool_calls"])
	}
	function, _ := toolCalls[0]["function"].(map[string]interface{})
	if toolCalls[0]["id"] != "call_read_1" || function["name"] != "read" || function["arguments"] != `{"path":"README.md"}` {
		t.Fatalf("assistant tool call changed identity: %#v", toolCalls[0])
	}
	if encoded[2]["role"] != "tool" || encoded[2]["tool_call_id"] != "call_read_1" || encoded[2]["content"] != "file body" {
		t.Fatalf("OpenAI tool result = %#v", encoded[2])
	}
}

func TestAnthropicMessagesPreserveNativeToolRound(t *testing.T) {
	messages := []ChatMessage{
		{
			Role:    "assistant",
			Content: "I will inspect it.",
			ToolCalls: []NativeToolCall{{
				ID: "toolu_read_1", Name: "read", Arguments: `{"path":"README.md"}`,
			}},
		},
		{
			Role: "tool",
			ToolResults: []NativeToolResult{{
				ToolCallID: "toolu_read_1", Content: "permission denied", IsError: true,
			}},
		},
	}
	if err := validateChatMessages(messages); err != nil {
		t.Fatalf("validateChatMessages: %v", err)
	}
	encoded := anthropicMessages(messages)
	if len(encoded) != 2 {
		t.Fatalf("Anthropic messages = %d, want 2: %#v", len(encoded), encoded)
	}
	assistantBlocks, ok := encoded[0]["content"].([]map[string]interface{})
	if !ok || len(assistantBlocks) != 2 {
		t.Fatalf("assistant content = %#v", encoded[0]["content"])
	}
	if assistantBlocks[1]["type"] != "tool_use" || assistantBlocks[1]["id"] != "toolu_read_1" || assistantBlocks[1]["name"] != "read" {
		t.Fatalf("Anthropic tool_use = %#v", assistantBlocks[1])
	}
	input, ok := assistantBlocks[1]["input"].(map[string]interface{})
	if !ok || input["path"] != "README.md" {
		t.Fatalf("Anthropic tool input = %#v", assistantBlocks[1]["input"])
	}
	resultBlocks, ok := encoded[1]["content"].([]map[string]interface{})
	if !ok || len(resultBlocks) != 1 {
		t.Fatalf("tool result content = %#v", encoded[1]["content"])
	}
	if encoded[1]["role"] != "user" || resultBlocks[0]["type"] != "tool_result" ||
		resultBlocks[0]["tool_use_id"] != "toolu_read_1" || resultBlocks[0]["content"] != "permission denied" ||
		resultBlocks[0]["is_error"] != true {
		t.Fatalf("Anthropic tool_result = %#v", resultBlocks[0])
	}
}

func TestValidateChatMessagesRejectsForgedToolProtocol(t *testing.T) {
	validCall := ChatMessage{
		Role:      "assistant",
		ToolCalls: []NativeToolCall{{ID: "call_1", Name: "read", Arguments: `{}`}},
	}
	tests := []struct {
		name     string
		messages []ChatMessage
	}{
		{name: "orphan result", messages: []ChatMessage{{Role: "tool", ToolResults: []NativeToolResult{{ToolCallID: "missing", Content: "x"}}}}},
		{name: "duplicate result", messages: []ChatMessage{validCall, {Role: "tool", ToolResults: []NativeToolResult{{ToolCallID: "call_1"}, {ToolCallID: "call_1"}}}}},
		{name: "invalid arguments", messages: []ChatMessage{{Role: "assistant", ToolCalls: []NativeToolCall{{ID: "call_1", Name: "read", Arguments: `[]`}}}}},
		{name: "wrong call role", messages: []ChatMessage{{Role: "user", ToolCalls: []NativeToolCall{{ID: "call_1", Name: "read", Arguments: `{}`}}}}},
		{name: "wrong result role", messages: []ChatMessage{validCall, {Role: "user", ToolResults: []NativeToolResult{{ToolCallID: "call_1"}}}}},
		{name: "ordinary message before result", messages: []ChatMessage{validCall, {Role: "user", Content: "continue"}, {Role: "tool", ToolResults: []NativeToolResult{{ToolCallID: "call_1"}}}}},
		{name: "next call batch before result", messages: []ChatMessage{validCall, {Role: "assistant", ToolCalls: []NativeToolCall{{ID: "call_2", Name: "search", Arguments: `{}`}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateChatMessages(test.messages); err == nil {
				t.Fatal("validateChatMessages accepted invalid native tool protocol")
			}
		})
	}
}

func TestValidateOutboundChatMessagesRejectsIncompleteNativeToolRound(t *testing.T) {
	messages := []ChatMessage{{
		Role: "assistant",
		ToolCalls: []NativeToolCall{{
			ID: "call_1", Name: "read", Arguments: `{}`,
		}},
	}}
	if err := validateChatMessages(messages); err != nil {
		t.Fatalf("persistable interrupted round should remain loadable: %v", err)
	}
	if err := validateOutboundChatMessages(messages); err == nil {
		t.Fatal("outbound validation accepted an incomplete native tool round")
	}
}

func TestEstimateMessagesTokensIncludesToolPayloads(t *testing.T) {
	plain := estimateMessagesTokens([]ChatMessage{{Role: "assistant", Content: ""}})
	withTool := estimateMessagesTokens([]ChatMessage{{
		Role: "assistant",
		ToolCalls: []NativeToolCall{{
			ID: "call_1", Name: "write", Arguments: `{"content":"` + string(make([]byte, 4096)) + `"}`,
		}},
	}})
	if withTool <= plain {
		t.Fatalf("tool payload token estimate = %d, plain = %d", withTool, plain)
	}
	if _, err := json.Marshal(openAIMessages([]ChatMessage{{Role: "assistant"}})); err != nil {
		t.Fatalf("baseline OpenAI messages no longer marshal: %v", err)
	}
}

func TestTruncateToTokenBudgetKeepsNativeToolRoundAtomic(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "first"},
		{Role: "assistant", ToolCalls: []NativeToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"` + strings.Repeat("a", 1000) + `"}`}}},
		{Role: "tool", ToolResults: []NativeToolResult{{ToolCallID: "call_1", Content: "ok"}}},
		{Role: "assistant", Content: "latest answer"},
	}
	for budget := 1; budget < estimateMessagesTokens(messages); budget++ {
		truncated := truncateToTokenBudget(messages, budget)
		if err := validateChatMessages(truncated); err != nil {
			t.Fatalf("budget %d produced an orphan native tool message: %v; %#v", budget, err, truncated)
		}
		foundCall := false
		foundResult := false
		for _, message := range truncated {
			foundCall = foundCall || len(message.ToolCalls) > 0
			foundResult = foundResult || len(message.ToolResults) > 0
		}
		if foundCall != foundResult {
			t.Fatalf("budget %d split a native tool round: %#v", budget, truncated)
		}
	}
}
