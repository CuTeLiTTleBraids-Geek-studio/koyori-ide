package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// newAIProviderBoundaryAgent keeps these cases on the same real lifecycle and
// renderer path as production Agent streams.
func newAIProviderBoundaryAgent(t *testing.T, serverURL, protocol string, tools []AIToolDef) (*AIService, *AgentService, *AgentLifecycle, *AIPermissionService, context.Context, *aiStreamTestWindow, string) {
	t.Helper()
	agent, _, _, _ := newExecutionCoreTestServices(t)
	permission := NewAIPermissionService(t.TempDir())
	ai := NewAIService()
	lifecycle, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	ai.setApp(application.New(application.Options{}))
	if err := ai.SetConfig(AIConfig{
		APIKey: "boundary-key", BaseURL: serverURL, Model: "boundary-model",
		Protocol: protocol, SystemPrompt: "boundary-system", ContextWindow: 10000,
		Tools: tools,
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	callerCtx, callerWindow := newAIStreamTestCaller(uint(201+len(protocol)), "provider-boundary")
	sessionID, err := agent.CreateAgentSessionForCaller(callerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	return ai, agent, lifecycle, permission, callerCtx, callerWindow, sessionID
}

func assertAIProviderBoundaryUsage(t *testing.T, lifecycle *AgentLifecycle, permission *AIPermissionService, sessionID string, status agentcore.SessionStatus, success bool) {
	t.Helper()
	session, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if session.Status != status {
		t.Fatalf("persistent session status = %s, want %s", session.Status, status)
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 1 || records[0].SessionID != sessionID || records[0].Pending || records[0].Success != success {
		t.Fatalf("usage terminal state = %+v, want one terminal success=%t", records, success)
	}
	if !success && records[0].Error != "execution failed" {
		t.Fatalf("usage error = %q, want stable redacted error", records[0].Error)
	}
}

func TestAIProviderStreamBoundaryMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeProviderSSE(w, []string{"{", "not-json", "[", "\\", "{"})
	}))
	t.Cleanup(server.Close)

	ai, _, lifecycle, permission, callerCtx, callerWindow, sessionID := newAIProviderBoundaryAgent(t, server.URL, "openai", nil)
	started, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "malformed"}})
	if err != nil {
		t.Fatalf("StartAgentStream: %v", err)
	}
	event := waitForAIStreamTerminalEvent(t, callerWindow, time.Second)
	if event.Name != "ai:error" {
		t.Fatalf("terminal event = %s, want ai:error: %+v", event.Name, event.Data)
	}
	payload, _ := event.Data.(map[string]interface{})
	if payload["streamId"] != started.StreamID {
		t.Fatalf("malformed error lost stream identity: %+v", event.Data)
	}
	message, _ := payload["data"].(string)
	if !strings.Contains(message, "consecutive malformed SSE chunks") {
		t.Fatalf("malformed response error = %q", message)
	}
	waitForAIStreamToStop(t, ai, time.Second)
	assertAIProviderBoundaryUsage(t, lifecycle, permission, sessionID, agentcore.SessionFailed, false)
}

func TestAIProviderStreamBoundaryProviderTimeout(t *testing.T) {
	requestAccepted := make(chan struct{})
	providerReleased := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(requestAccepted)
		<-r.Context().Done()
		close(providerReleased)
	}))
	t.Cleanup(server.Close)

	ai, _, lifecycle, permission, callerCtx, callerWindow, sessionID := newAIProviderBoundaryAgent(t, server.URL, "openai", nil)
	ai.streamIdleTimeout = 25 * time.Millisecond
	ai.streamWallTimeout = time.Second
	started, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "timeout"}})
	if err != nil {
		t.Fatalf("StartAgentStream: %v", err)
	}
	select {
	case <-requestAccepted:
	case <-time.After(time.Second):
		t.Fatal("provider did not accept timeout request")
	}
	event := waitForAIStreamTerminalEvent(t, callerWindow, time.Second)
	assertAIStreamDeadlineError(t, event, started.StreamID, ErrAIStreamIdleTimeout)
	select {
	case <-providerReleased:
	case <-time.After(time.Second):
		t.Fatal("provider request context was not released after timeout")
	}
	waitForAIStreamToStop(t, ai, time.Second)
	assertAIProviderBoundaryUsage(t, lifecycle, permission, sessionID, agentcore.SessionFailed, false)
}

func TestAIProviderStreamBoundaryStopReleasesProviderAndDropsLateChunk(t *testing.T) {
	requestAccepted := make(chan struct{})
	providerReleased := make(chan struct{})
	var lateWrite atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"before-stop\"}}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(requestAccepted)
		<-r.Context().Done()
		close(providerReleased)
		if _, err := fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"late-chunk\"}}]}\n\ndata: [DONE]\n\n"); err == nil {
			lateWrite.Store(true)
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(server.Close)

	ai, _, lifecycle, permission, callerCtx, callerWindow, sessionID := newAIProviderBoundaryAgent(t, server.URL, "openai", nil)
	started, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "stop"}})
	if err != nil {
		t.Fatalf("StartAgentStream: %v", err)
	}
	select {
	case <-requestAccepted:
	case <-time.After(time.Second):
		t.Fatal("provider did not accept stop request")
	}
	if err := ai.StopStream(callerCtx); err != nil {
		t.Fatalf("StopStream: %v", err)
	}
	event := waitForAIStreamTerminalEvent(t, callerWindow, time.Second)
	if event.Name != "ai:error" {
		t.Fatalf("stop terminal event = %s, want ai:error: %+v", event.Name, event.Data)
	}
	payload, _ := event.Data.(map[string]interface{})
	if payload["streamId"] != started.StreamID {
		t.Fatalf("stop error lost stream identity: %+v", event.Data)
	}
	select {
	case <-providerReleased:
	case <-time.After(time.Second):
		t.Fatal("provider handler was not released by StopStream")
	}
	waitForAIStreamToStop(t, ai, time.Second)
	for _, candidate := range callerWindow.eventsSnapshot() {
		if candidate.Name != "ai:chunk" {
			continue
		}
		data, _ := candidate.Data.(map[string]interface{})
		chunk, _ := data["data"].(string)
		if chunk == "late-chunk" || strings.Contains(chunk, "late-chunk") {
			t.Fatalf("late provider chunk reached renderer: %+v", candidate.Data)
		}
	}
	_ = lateWrite.Load() // The server attempts the late write; client cancellation is the assertion boundary.
	assertAIProviderBoundaryUsage(t, lifecycle, permission, sessionID, agentcore.SessionFailed, false)
}

func TestAIProviderStreamBoundaryEmptyTextNativeToolCall(t *testing.T) {
	serverRequest := make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		serverRequest <- body
		writeProviderSSE(w, []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_empty_read","function":{"name":"read","arguments":"{}"}}]}}]}`,
			`[DONE]`,
		})
	}))
	t.Cleanup(server.Close)

	ai, providerAgent, lifecycle, permission, callerCtx, callerWindow, sessionID := newAIProviderBoundaryAgent(t, server.URL, "openai", nil)
	catalog, err := providerAgent.GetAgentToolCatalog(callerCtx)
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	definitions := providerToolDefs(t, catalog, "read")
	if err := ai.SetConfig(AIConfig{
		APIKey: "boundary-key", BaseURL: server.URL, Model: "boundary-model",
		Protocol: "openai", SystemPrompt: "boundary-system", ContextWindow: 10000,
		Tools: definitions,
	}); err != nil {
		t.Fatalf("SetConfig with tool definition: %v", err)
	}
	started, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "call read"}})
	if err != nil {
		t.Fatalf("StartAgentStream: %v", err)
	}
	turn := waitForProviderTurn(t, ai, callerWindow, started.StreamID)
	if turn.err != "" || turn.text != "" || len(turn.calls) != 1 || turn.calls[0].ID != "call_empty_read" || turn.calls[0].Name != "read" || turn.calls[0].Arguments != "{}" {
		t.Fatalf("empty-text native turn = %+v", turn)
	}
	var request map[string]interface{}
	select {
	case request = <-serverRequest:
	case <-time.After(time.Second):
		t.Fatal("provider request was not captured")
	}
	if request["tool_choice"] != "auto" {
		t.Fatalf("OpenAI request tool_choice = %v, want auto", request["tool_choice"])
	}
	tools, ok := request["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("OpenAI request tools = %v, want one tool definition", request["tools"])
	}
	waitForAIStreamToStop(t, ai, time.Second)
	assertAIProviderBoundaryUsage(t, lifecycle, permission, sessionID, agentcore.SessionRunning, true)
}
