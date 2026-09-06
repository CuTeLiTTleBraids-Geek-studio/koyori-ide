package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type streamedProviderTurn struct {
	text  string
	calls []NativeToolCall
	err   string
}

type providerToolRoundTripCase struct {
	name                 string
	protocol             string
	path                 string
	firstText            string
	finalText            string
	firstSSE             []string
	finalSSE             []string
	expectCalls          []NativeToolCall
	rejectCalls          bool
	expectExecutionError bool
}

func waitForProviderTurn(t *testing.T, ai *AIService, window *aiStreamTestWindow, streamID string) streamedProviderTurn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		turn := streamedProviderTurn{}
		terminal := false
		for _, event := range window.eventsSnapshot() {
			payload, ok := event.Data.(map[string]interface{})
			if !ok || payload["streamId"] != streamID {
				continue
			}
			switch event.Name {
			case "ai:chunk":
				chunk, _ := payload["data"].(string)
				turn.text += chunk
			case "ai:tool_calls":
				raw, _ := payload["data"].(string)
				if err := json.Unmarshal([]byte(raw), &turn.calls); err != nil {
					t.Fatalf("decode native tool event for stream %s: %v; payload=%q", streamID, err, raw)
				}
			case "ai:error":
				turn.err, _ = payload["data"].(string)
				terminal = true
			case "ai:done":
				terminal = true
			}
		}
		if terminal {
			waitForAIStreamToStop(t, ai, time.Second)
			return turn
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for provider stream %s; events=%+v", streamID, window.eventsSnapshot())
	return streamedProviderTurn{}
}

func providerToolDefs(t *testing.T, catalog AgentToolCatalog, ids ...string) []AIToolDef {
	t.Helper()
	definitions := make([]AIToolDef, 0, len(ids))
	for _, id := range ids {
		found := false
		for _, tool := range catalog.Tools {
			if tool.ID != id {
				continue
			}
			wireName := tool.WireName
			if wireName == "" {
				wireName = tool.ID
			}
			definitions = append(definitions, AIToolDef{
				Type: "function",
				Function: AIToolFunction{
					Name: wireName, Description: tool.Description, Parameters: tool.InputSchema,
				},
			})
			found = true
			break
		}
		if !found {
			t.Fatalf("tool %q is absent from backend catalog", id)
		}
	}
	return definitions
}

func writeProviderSSE(w http.ResponseWriter, events []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, event := range events {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func snapshotProviderRequests(mu *sync.Mutex, requests *[]capturedToolProtocolRequest) []capturedToolProtocolRequest {
	mu.Lock()
	defer mu.Unlock()
	return append([]capturedToolProtocolRequest(nil), (*requests)...)
}

func jsonValue(t *testing.T, value interface{}) interface{} {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture value: %v", err)
	}
	var decoded interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode fixture value: %v", err)
	}
	return decoded
}

func assertProviderToolRequestShape(
	t *testing.T,
	test providerToolRoundTripCase,
	definitions []AIToolDef,
	requests []capturedToolProtocolRequest,
	results []NativeToolResult,
) {
	t.Helper()
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want 2: %#v", len(requests), requests)
	}
	for index, request := range requests {
		if request.path != test.path {
			t.Fatalf("request %d path = %q, want %q", index+1, request.path, test.path)
		}
		if request.body["stream"] != true {
			t.Fatalf("request %d did not use streaming: %#v", index+1, request.body)
		}
	}

	if test.protocol == "anthropic" {
		wantTools := make([]interface{}, 0, len(definitions))
		for _, definition := range definitions {
			wantTools = append(wantTools, map[string]interface{}{
				"name": definition.Function.Name, "description": definition.Function.Description,
				"input_schema": jsonValue(t, definition.Function.Parameters),
			})
		}
		requireToolProtocolJSONEqual(t, "Anthropic tools", requests[0].body["tools"], wantTools)
		if requests[0].body["system"] != "native-system" {
			t.Fatalf("Anthropic system = %#v", requests[0].body["system"])
		}
		requireToolProtocolJSONEqual(t, "Anthropic first-request messages", requests[0].body["messages"], []interface{}{
			map[string]interface{}{"role": "user", "content": "inspect workspace"},
		})
		assistantBlocks := []interface{}{map[string]interface{}{"type": "text", "text": test.firstText}}
		for _, call := range test.expectCalls {
			var input map[string]interface{}
			if err := json.Unmarshal([]byte(call.Arguments), &input); err != nil {
				t.Fatalf("decode expected Anthropic call: %v", err)
			}
			assistantBlocks = append(assistantBlocks, map[string]interface{}{
				"type": "tool_use", "id": call.ID, "name": call.Name, "input": input,
			})
		}
		resultBlocks := make([]interface{}, 0, len(results))
		for _, result := range results {
			block := map[string]interface{}{
				"type": "tool_result", "tool_use_id": result.ToolCallID, "content": result.Content,
			}
			if result.IsError {
				block["is_error"] = true
			}
			resultBlocks = append(resultBlocks, block)
		}
		requireToolProtocolJSONEqual(t, "Anthropic second-request messages", requests[1].body["messages"], []interface{}{
			map[string]interface{}{"role": "user", "content": "inspect workspace"},
			map[string]interface{}{"role": "assistant", "content": assistantBlocks},
			map[string]interface{}{"role": "user", "content": resultBlocks},
		})
		return
	}

	requireToolProtocolJSONEqual(t, "OpenAI tools", requests[0].body["tools"], jsonValue(t, definitions))
	requireToolProtocolJSONEqual(t, "OpenAI first-request messages", requests[0].body["messages"], []interface{}{
		map[string]interface{}{"role": "system", "content": "native-system"},
		map[string]interface{}{"role": "user", "content": "inspect workspace"},
	})
	assistantCalls := make([]interface{}, 0, len(test.expectCalls))
	for _, call := range test.expectCalls {
		assistantCalls = append(assistantCalls, map[string]interface{}{
			"id": call.ID, "type": "function",
			"function": map[string]interface{}{"name": call.Name, "arguments": call.Arguments},
		})
	}
	wantMessages := []interface{}{
		map[string]interface{}{"role": "system", "content": "native-system"},
		map[string]interface{}{"role": "user", "content": "inspect workspace"},
		map[string]interface{}{"role": "assistant", "content": test.firstText, "tool_calls": assistantCalls},
	}
	for _, result := range results {
		wantMessages = append(wantMessages, map[string]interface{}{
			"role": "tool", "tool_call_id": result.ToolCallID, "content": result.Content,
		})
	}
	requireToolProtocolJSONEqual(t, "OpenAI second-request messages", requests[1].body["messages"], wantMessages)
}

func TestAIServiceNativeToolStreamingRoundTripHTTP(t *testing.T) {
	tests := []providerToolRoundTripCase{
		{
			name: "OpenAI-compatible single tool", protocol: "openai", path: "/v1/chat/completions",
			firstText: "Inspecting ", finalText: "Final answer from OpenAI",
			firstSSE: []string{
				`{"choices":[{"delta":{"content":"Inspecting "}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_read_stream","function":{"name":"read","arguments":"{\"path\":\"note.txt\"}"}}]}}]}`,
				`[DONE]`,
			},
			finalSSE: []string{
				`{"choices":[{"delta":{"content":"Final answer from OpenAI"}}]}`,
				`[DONE]`,
			},
			expectCalls: []NativeToolCall{{ID: "call_read_stream", Name: "read", Arguments: `{"path":"note.txt"}`}},
		},
		{
			name: "OpenAI user rejection", protocol: "openai", path: "/v1/chat/completions",
			firstText: "I can inspect that. ", finalText: "I will not inspect the workspace.", rejectCalls: true,
			firstSSE: []string{
				`{"choices":[{"delta":{"content":"I can inspect that. "}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_read_rejected","function":{"name":"read","arguments":"{\"path\":\"note.txt\"}"}}]}}]}`,
				`[DONE]`,
			},
			finalSSE: []string{
				`{"choices":[{"delta":{"content":"I will not inspect the workspace."}}]}`,
				`[DONE]`,
			},
			expectCalls: []NativeToolCall{{ID: "call_read_rejected", Name: "read", Arguments: `{"path":"note.txt"}`}},
		},
		{
			name: "Anthropic batched tools", protocol: "anthropic", path: "/v1/messages",
			firstText: "Inspecting ", finalText: "Final answer from Anthropic",
			firstSSE: []string{
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Inspecting "}}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_read_stream","name":"read"}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"note.txt\"}"}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_search_stream","name":"search"}}`,
				`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"provider-loop-needle\",\"ignoreCase\":true}"}}`,
				`{"type":"content_block_stop","index":2}`,
				`{"type":"message_stop"}`,
			},
			finalSSE: []string{
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Final answer from Anthropic"}}`,
				`{"type":"message_stop"}`,
			},
			expectCalls: []NativeToolCall{
				{ID: "toolu_read_stream", Name: "read", Arguments: `{"path":"note.txt"}`},
				{ID: "toolu_search_stream", Name: "search", Arguments: `{"query":"provider-loop-needle","ignoreCase":true}`},
			},
		},
		{
			name: "Anthropic backend execution error", protocol: "anthropic", path: "/v1/messages",
			firstText: "Inspecting ", finalText: "The requested file could not be read.", expectExecutionError: true,
			firstSSE: []string{
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Inspecting "}}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_read_error","name":"read"}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"missing-provider-file.txt\"}"}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_stop"}`,
			},
			finalSSE: []string{
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"The requested file could not be read."}}`,
				`{"type":"message_stop"}`,
			},
			expectCalls: []NativeToolCall{{ID: "toolu_read_error", Name: "read", Arguments: `{"path":"missing-provider-file.txt"}`}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var hits atomic.Int32
			var requestsMu sync.Mutex
			requests := make([]capturedToolProtocolRequest, 0, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				var body map[string]interface{}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					http.Error(w, "invalid request body", http.StatusBadRequest)
					return
				}
				requestsMu.Lock()
				requests = append(requests, capturedToolProtocolRequest{path: request.URL.Path, body: body})
				requestsMu.Unlock()
				switch hits.Add(1) {
				case 1:
					writeProviderSSE(w, test.firstSSE)
				case 2:
					writeProviderSSE(w, test.finalSSE)
				default:
					http.Error(w, "unexpected provider turn", http.StatusConflict)
				}
			}))
			defer server.Close()

			agent, _, _, root := newExecutionCoreTestServices(t)
			if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("provider-loop-needle\nsecond line"), 0o600); err != nil {
				t.Fatalf("seed workspace file: %v", err)
			}
			permission := NewAIPermissionService(t.TempDir())
			ai := NewAIService()
			lifecycle, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission)
			if err != nil {
				t.Fatalf("WireAgentLifecycle: %v", err)
			}
			ai.setApp(application.New(application.Options{}))
			callerCtx, callerWindow := newAIStreamTestCaller(101, "provider-owner")
			_, otherWindow := newAIStreamTestCaller(102, "provider-peer")
			sessionID, err := agent.CreateAgentSessionForCaller(callerCtx, "chat")
			if err != nil {
				t.Fatalf("CreateAgentSessionForCaller: %v", err)
			}
			catalog, err := agent.GetAgentToolCatalog(callerCtx)
			if err != nil {
				t.Fatalf("GetAgentToolCatalog: %v", err)
			}
			toolIDs := make([]string, 0, len(test.expectCalls))
			for _, call := range test.expectCalls {
				toolIDs = append(toolIDs, call.Name)
			}
			definitions := providerToolDefs(t, catalog, toolIDs...)
			if err := ai.SetConfig(AIConfig{
				APIKey: "fixture-key", BaseURL: server.URL, Model: "fixture-model",
				Protocol: test.protocol, SystemPrompt: "native-system", ContextWindow: 10000,
				Tools: definitions,
			}); err != nil {
				t.Fatalf("SetConfig: %v", err)
			}

			firstMessages := []ChatMessage{{Role: "user", Content: "inspect workspace"}}
			firstStart, err := ai.StartAgentStream(callerCtx, sessionID, firstMessages)
			if err != nil {
				t.Fatalf("first StartAgentStream: %v", err)
			}
			firstTurn := waitForProviderTurn(t, ai, callerWindow, firstStart.StreamID)
			if firstTurn.err != "" || firstTurn.text != test.firstText {
				t.Fatalf("first provider turn = %+v", firstTurn)
			}
			if !reflect.DeepEqual(firstTurn.calls, test.expectCalls) {
				t.Fatalf("native calls = %+v, want %+v", firstTurn.calls, test.expectCalls)
			}

			results := make([]NativeToolResult, 0, len(firstTurn.calls))
			for _, call := range firstTurn.calls {
				var arguments map[string]interface{}
				if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
					t.Fatalf("decode tool arguments for %s: %v", call.ID, err)
				}
				if test.rejectCalls {
					results = append(results, NativeToolResult{ToolCallID: call.ID, Content: "user rejected tool call", IsError: true})
					continue
				}
				execution, executionErr := agent.ExecuteAgentTool(callerCtx, AgentToolExecutionRequest{
					SessionID: sessionID, CatalogRevision: catalog.Revision,
					ToolID: call.Name, Arguments: arguments,
				})
				if test.expectExecutionError {
					if executionErr == nil {
						t.Fatalf("execute provider tool %s/%s unexpectedly succeeded: %+v", call.ID, call.Name, execution)
					}
					results = append(results, NativeToolResult{ToolCallID: call.ID, Content: executionErr.Error(), IsError: true})
					continue
				}
				if executionErr != nil {
					t.Fatalf("execute provider tool %s/%s: %v", call.ID, call.Name, executionErr)
				}
				if execution.Observation == "" {
					t.Fatalf("provider tool %s returned an empty observation", call.ID)
				}
				results = append(results, NativeToolResult{ToolCallID: call.ID, Content: execution.Observation})
			}

			secondMessages := append([]ChatMessage(nil), firstMessages...)
			secondMessages = append(secondMessages,
				ChatMessage{Role: "assistant", Content: firstTurn.text, ToolCalls: firstTurn.calls},
				ChatMessage{Role: "tool", ToolResults: results},
			)
			secondStart, err := ai.StartAgentStream(callerCtx, sessionID, secondMessages)
			if err != nil {
				t.Fatalf("second StartAgentStream: %v", err)
			}
			if secondStart.SessionID != firstStart.SessionID || secondStart.StreamID == firstStart.StreamID {
				t.Fatalf("stream/session identity changed incorrectly: first=%+v second=%+v", firstStart, secondStart)
			}
			secondTurn := waitForProviderTurn(t, ai, callerWindow, secondStart.StreamID)
			if secondTurn.err != "" || secondTurn.text != test.finalText || len(secondTurn.calls) != 0 {
				t.Fatalf("final provider turn = %+v", secondTurn)
			}

			captured := snapshotProviderRequests(&requestsMu, &requests)
			assertProviderToolRequestShape(t, test, definitions, captured, results)
			if hits.Load() != 2 {
				t.Fatalf("provider hits = %d, want 2", hits.Load())
			}
			if events := otherWindow.eventsSnapshot(); len(events) != 0 {
				t.Fatalf("peer renderer received private provider events: %+v", events)
			}
			if ai.IsStreaming() {
				t.Fatal("provider round trip retained the global stream slot")
			}

			session, err := lifecycle.GetByID(sessionID)
			if err != nil {
				t.Fatalf("GetByID: %v", err)
			}
			if session.Status != agentcore.SessionRunning {
				t.Fatalf("persistent Agent session status = %s, want running", session.Status)
			}
			var timelineText strings.Builder
			toolResults := 0
			for _, event := range session.Stream {
				if event.Kind == agentcore.StreamDelta {
					timelineText.WriteString(event.Data)
				}
				if event.Kind == agentcore.StreamToolResult {
					toolResults++
				}
			}
			if !strings.Contains(timelineText.String(), test.firstText) || !strings.Contains(timelineText.String(), test.finalText) {
				t.Fatalf("lifecycle stream lost provider output: %+v", session.Stream)
			}
			wantLifecycleToolResults := len(test.expectCalls)
			if test.rejectCalls {
				wantLifecycleToolResults = 0
			}
			if toolResults != wantLifecycleToolResults {
				t.Fatalf("lifecycle tool-result events = %d, want %d: %+v", toolResults, wantLifecycleToolResults, session.Stream)
			}

			usage := permission.usageRecordsSnapshot()
			wantUsage := 2
			if !test.rejectCalls {
				wantUsage += len(test.expectCalls)
			}
			if len(usage) != wantUsage {
				t.Fatalf("usage records = %d, want %d: %+v", len(usage), wantUsage, usage)
			}
			toolUsage := 0
			for _, record := range usage {
				if record.SessionID != sessionID || record.Pending {
					t.Fatalf("non-terminal provider/tool usage: %+v", usage)
				}
				if record.UnitKind == string(agentcore.UsageUnitTool) {
					toolUsage++
					if test.expectExecutionError {
						if record.Success || record.Error == "" {
							t.Fatalf("failed execution usage was recorded as success: %+v", record)
						}
					} else if !record.Success {
						t.Fatalf("successful tool usage was recorded as failure: %+v", record)
					}
				} else if !record.Success {
					t.Fatalf("provider usage was recorded as failure: %+v", record)
				}
			}
			wantToolUsage := len(test.expectCalls)
			if test.rejectCalls {
				wantToolUsage = 0
			}
			if toolUsage != wantToolUsage {
				t.Fatalf("tool usage records = %d, want %d: %+v", toolUsage, wantToolUsage, usage)
			}
			if err := agent.CloseAgentSessionForCaller(callerCtx, sessionID); err != nil {
				t.Fatalf("CloseAgentSessionForCaller: %v", err)
			}
		})
	}
}
