package services

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func waitForAIStreamTerminalEvent(t *testing.T, window *aiStreamTestWindow, timeout time.Duration) *application.CustomEvent {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		for _, event := range window.eventsSnapshot() {
			if event.Name == "ai:error" || event.Name == "ai:done" {
				return event
			}
		}
		select {
		case <-window.notify:
		case <-timer.C:
			t.Fatalf("timed out waiting for AI stream terminal event: %+v", window.eventsSnapshot())
		}
	}
}

func assertAIStreamDeadlineError(t *testing.T, event *application.CustomEvent, streamID string, target error) {
	t.Helper()
	if event.Name != "ai:error" {
		t.Fatalf("terminal event = %s, want ai:error: %+v", event.Name, event.Data)
	}
	payload, ok := event.Data.(map[string]interface{})
	if !ok || payload["streamId"] != streamID {
		t.Fatalf("deadline event lost stream identity: %+v", event.Data)
	}
	message, _ := payload["data"].(string)
	if !strings.Contains(message, target.Error()) {
		t.Fatalf("deadline error = %q, want %q", message, target.Error())
	}
}

func TestAIStreamIdleTimeoutCancelsProviderAndReleasesSlot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	t.Cleanup(server.Close)

	ai := NewAIService()
	ai.streamIdleTimeout = 25 * time.Millisecond
	ai.streamWallTimeout = time.Second
	ai.setApp(application.New(application.Options{}))
	if err := ai.SetConfig(AIConfig{APIKey: "secret", BaseURL: server.URL, Model: "test"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	callerCtx, callerWindow := newAIStreamTestCaller(81, "idle-timeout")
	streamID, err := ai.StartStream(callerCtx, []ChatMessage{{Role: "user", Content: "wait"}})
	if err != nil {
		t.Fatalf("StartStream: %v", err)
	}
	event := waitForAIStreamTerminalEvent(t, callerWindow, time.Second)
	assertAIStreamDeadlineError(t, event, streamID, ErrAIStreamIdleTimeout)
	waitForAIStreamToStop(t, ai, time.Second)
}

func TestAIStreamWallTimeoutWinsDespiteProviderHeartbeats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for index := 0; index < 50; index++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
				_, _ = fmt.Fprint(w, ": heartbeat\n\n")
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	ai := NewAIService()
	ai.streamIdleTimeout = 250 * time.Millisecond
	ai.streamWallTimeout = 600 * time.Millisecond
	ai.setApp(application.New(application.Options{}))
	if err := ai.SetConfig(AIConfig{APIKey: "secret", BaseURL: server.URL, Model: "test"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	callerCtx, callerWindow := newAIStreamTestCaller(82, "wall-timeout")
	streamID, err := ai.StartStream(callerCtx, []ChatMessage{{Role: "user", Content: "keep going"}})
	if err != nil {
		t.Fatalf("StartStream: %v", err)
	}
	event := waitForAIStreamTerminalEvent(t, callerWindow, time.Second)
	assertAIStreamDeadlineError(t, event, streamID, ErrAIStreamWallTimeout)
	waitForAIStreamToStop(t, ai, time.Second)
}

func TestAgentStreamIdleTimeoutPublishesTerminalUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	agent := NewAgentService()
	ai := NewAIService()
	permission := NewAIPermissionService(t.TempDir())
	if _, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	ai.streamIdleTimeout = 25 * time.Millisecond
	ai.streamWallTimeout = time.Second
	ai.setApp(application.New(application.Options{}))
	if err := ai.SetConfig(AIConfig{APIKey: "secret", BaseURL: server.URL, Model: "test"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	callerCtx, callerWindow := newAIStreamTestCaller(83, "agent-idle-timeout")
	sessionID, err := agent.CreateAgentSessionForCaller(callerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	started, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "wait"}})
	if err != nil {
		t.Fatalf("StartAgentStream: %v", err)
	}
	event := waitForAIStreamTerminalEvent(t, callerWindow, time.Second)
	assertAIStreamDeadlineError(t, event, started.StreamID, ErrAIStreamIdleTimeout)
	waitForAIStreamToStop(t, ai, time.Second)
	records := permission.usageRecordsSnapshot()
	if len(records) != 1 || records[0].SessionID != sessionID || records[0].Pending || records[0].Success {
		t.Fatalf("Agent deadline usage was not terminal: %+v", records)
	}
	if records[0].Error != "execution failed" {
		t.Fatalf("Agent deadline usage error = %q, want stable redacted error", records[0].Error)
	}
}

func waitForAIStreamToStop(t *testing.T, ai *AIService, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for ai.IsStreaming() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if ai.IsStreaming() {
		t.Fatal("AI stream deadline returned without releasing the global stream slot")
	}
}
