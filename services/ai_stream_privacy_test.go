package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// aiStreamTestWindow is a Wails Window substitute that records the exact
// per-window dispatch path. Embedding the interface keeps the test focused on
// the methods used by caller identity and event delivery.
type aiStreamTestWindow struct {
	application.Window
	id     uint
	name   string
	mu     sync.Mutex
	events []*application.CustomEvent
	notify chan struct{}
}

func newAIStreamTestCaller(id uint, name string) (context.Context, *aiStreamTestWindow) {
	window := &aiStreamTestWindow{id: id, name: name, notify: make(chan struct{}, 32)}
	return context.WithValue(context.Background(), application.WindowKey, application.Window(window)), window
}

func (w *aiStreamTestWindow) ID() uint     { return w.id }
func (w *aiStreamTestWindow) Name() string { return w.name }

func (w *aiStreamTestWindow) DispatchWailsEvent(event *application.CustomEvent) {
	if event == nil {
		return
	}
	w.mu.Lock()
	w.events = append(w.events, &application.CustomEvent{Name: event.Name, Data: event.Data, Sender: event.Sender})
	w.mu.Unlock()
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *aiStreamTestWindow) eventsSnapshot() []*application.CustomEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*application.CustomEvent(nil), w.events...)
}

func waitForAIStreamEvent(t *testing.T, window *aiStreamTestWindow, name string) []*application.CustomEvent {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		events := window.eventsSnapshot()
		for _, event := range events {
			if event.Name == name {
				return events
			}
		}
		select {
		case <-window.notify:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s; events=%+v", name, events)
		}
	}
}

func TestAIServiceSensitiveStreamEventsStayInCallerWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"private-output\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	app := application.New(application.Options{})
	ai := NewAIService()
	ai.setApp(app)
	if err := ai.SetConfig(AIConfig{APIKey: "secret", BaseURL: server.URL, Model: "test"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	ownerCtx, ownerWindow := newAIStreamTestCaller(11, "main")
	_, otherWindow := newAIStreamTestCaller(12, "assistant")

	var globalSensitive atomic.Int32
	for _, name := range []string{"ai:chunk", "ai:error", "ai:done", "ai:tool_calls"} {
		app.Event.On(name, func(*application.CustomEvent) { globalSensitive.Add(1) })
	}
	var busyStreamIDLeaked atomic.Bool
	busySeen := make(chan struct{}, 2)
	app.Event.On("ai:stream-busy", func(event *application.CustomEvent) {
		if payload, ok := event.Data.(map[string]interface{}); ok {
			if _, hasStreamID := payload["streamId"]; hasStreamID {
				busyStreamIDLeaked.Store(true)
			}
		}
		select {
		case busySeen <- struct{}{}:
		default:
		}
	})

	streamID, err := ai.StartStream(ownerCtx, []ChatMessage{{Role: "user", Content: "private-input"}})
	if err != nil {
		t.Fatalf("StartStream: %v", err)
	}
	events := waitForAIStreamEvent(t, ownerWindow, "ai:done")
	var gotChunk bool
	for _, event := range events {
		payload, _ := event.Data.(map[string]interface{})
		if event.Name == "ai:chunk" && payload["streamId"] == streamID && payload["data"] == "private-output" {
			gotChunk = true
		}
	}
	if !gotChunk {
		t.Fatalf("caller window did not receive its private chunk: %+v", events)
	}
	if leaked := globalSensitive.Load(); leaked != 0 {
		t.Fatalf("sensitive stream events reached the global event bus: %d", leaked)
	}
	select {
	case <-busySeen:
	case <-time.After(5 * time.Second):
		t.Fatal("global busy state was not emitted")
	}
	if busyStreamIDLeaked.Load() {
		t.Fatal("global busy state exposed the owner stream ID")
	}
	if events := otherWindow.eventsSnapshot(); len(events) != 0 {
		t.Fatalf("non-owner window received stream events: %+v", events)
	}
}

func startBlockingAgentStream(t *testing.T) (*AgentService, *AIService, *AIPermissionService, context.Context, string) {
	t.Helper()
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
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
	ai.setApp(application.New(application.Options{}))
	if err := ai.SetConfig(AIConfig{APIKey: "secret", BaseURL: server.URL, Model: "test"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	callerCtx, _ := newAIStreamTestCaller(21, "main")
	sessionID, err := agent.CreateAgentSessionForCaller(callerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	if _, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "wait"}}); err != nil {
		t.Fatalf("StartAgentStream: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider request did not start")
	}
	t.Cleanup(func() {
		_ = ai.StopStream(callerCtx)
		deadline := time.Now().Add(5 * time.Second)
		for ai.IsStreaming() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		_ = agent.Close()
	})
	return agent, ai, permission, callerCtx, sessionID
}

func TestCloseAgentSessionCancelsAndWaitsForProviderStream(t *testing.T) {
	agent, ai, permission, callerCtx, sessionID := startBlockingAgentStream(t)
	if err := agent.CloseAgentSessionForCaller(callerCtx, sessionID); err != nil {
		t.Fatalf("CloseAgentSessionForCaller: %v", err)
	}
	if ai.IsStreaming() {
		t.Fatal("session close returned before the provider worker terminated")
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 1 || records[0].Pending {
		t.Fatalf("session close returned before terminal usage publication: %+v", records)
	}
}

func TestAgentServiceCloseCancelsAndWaitsForProviderStream(t *testing.T) {
	agent, ai, permission, _, _ := startBlockingAgentStream(t)
	if err := agent.Close(); err != nil {
		t.Fatalf("AgentService.Close: %v", err)
	}
	if ai.IsStreaming() {
		t.Fatal("AgentService.Close returned before the provider worker terminated")
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 1 || records[0].Pending {
		t.Fatalf("AgentService.Close returned before terminal usage publication: %+v", records)
	}
}

func TestAgentServiceCloseSealsStreamAdmissionBeforeResourceTeardown(t *testing.T) {
	agent := NewAgentService()
	ai := NewAIService()
	permission := NewAIPermissionService(t.TempDir())
	if _, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	ai.setApp(application.New(application.Options{}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	if err := ai.SetConfig(AIConfig{APIKey: "secret", BaseURL: server.URL, Model: "test"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	// Hold the Agent resource lock so Close must pause after its stream-drain
	// admission point. The synthetic completed worker tells the test exactly
	// when Close has captured and cancelled the old stream.
	agent.mu.Lock()
	agentLocked := true
	defer func() {
		if agentLocked {
			agent.mu.Unlock()
		}
	}()
	cancelSeen := make(chan struct{})
	oldDone := make(chan struct{})
	close(oldDone)
	oldStream := &streamCancel{
		fn: func() { close(cancelSeen) },
		owner: agentSessionOwner{
			identity: "wails-window:old:main",
		},
		lifecycleID: "old-stream",
		done:        oldDone,
	}
	ai.mu.Lock()
	ai.cancel = oldStream
	ai.activeStreamID = "old-stream"
	ai.mu.Unlock()

	closeResult := make(chan error, 1)
	go func() { closeResult <- agent.Close() }()
	select {
	case <-cancelSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("AgentService.Close did not reach stream cancellation")
	}

	// Model the old worker's compare-and-swap cleanup while Close remains
	// paused on agent.mu. A new renderer stream must not enter this gap.
	ai.mu.Lock()
	if ai.cancel != oldStream {
		ai.mu.Unlock()
		t.Fatal("Close replaced the active stream identity")
	}
	ai.cancel = nil
	ai.activeStreamID = ""
	ai.mu.Unlock()
	callerCtx, _ := newAIStreamTestCaller(23, "late-start")
	if _, err := ai.StartStream(callerCtx, []ChatMessage{{Role: "user", Content: "must be rejected"}}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("StartStream during AgentService.Close = %v, want ErrNotAllowed", err)
	}
	if ai.IsStreaming() {
		t.Fatal("AgentService.Close admitted a new provider stream after drain began")
	}

	agent.mu.Unlock()
	agentLocked = false
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("AgentService.Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AgentService.Close did not finish after resource lock release")
	}
}

func TestCloseAgentSessionSealsRestartAdmission(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	agent := NewAgentService()
	ai := NewAIService()
	permission := NewAIPermissionService(t.TempDir())
	if _, err := WireAgentLifecycle(agent, ai, NewAIPlanService(), NewAIGoalService(), permission); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	ai.setApp(application.New(application.Options{}))
	if err := ai.SetConfig(AIConfig{APIKey: "secret", BaseURL: server.URL, Model: "test"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	callerCtx, _ := newAIStreamTestCaller(24, "session-close")
	sessionID, err := agent.CreateAgentSessionForCaller(callerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	if _, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "first"}}); err != nil {
		t.Fatalf("initial StartAgentStream: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for ai.IsStreaming() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ai.IsStreaming() {
		t.Fatal("initial provider stream did not finish")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("initial provider request count = %d, want 1", got)
	}

	sealed := make(chan struct{})
	release := make(chan struct{})
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.sessionOwnerMutationHook = func(stage string) {
		if stage == "after-session-admission-seal" {
			close(sealed)
			<-release
		}
	}
	deps.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		deps.mu.Lock()
		deps.sessionOwnerMutationHook = nil
		deps.mu.Unlock()
		_ = agent.Close()
	})

	closeResult := make(chan error, 1)
	go func() { closeResult <- agent.CloseAgentSessionForCaller(callerCtx, sessionID) }()
	select {
	case <-sealed:
	case <-time.After(5 * time.Second):
		t.Fatal("session close did not seal admission")
	}

	if _, err := ai.StartAgentStream(callerCtx, sessionID, []ChatMessage{{Role: "user", Content: "restart"}}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("StartAgentStream during session close = %v, want ErrNotAllowed", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("restart attempted a provider request after close seal: count=%d", got)
	}
	close(release)
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("CloseAgentSessionForCaller: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CloseAgentSessionForCaller did not finish")
	}
	if ai.IsStreaming() {
		t.Fatal("session close returned with an active provider stream")
	}
}

func TestAIStreamShutdownTimeoutIsObservable(t *testing.T) {
	var cancelled atomic.Bool
	ai := &AIService{streamShutdownTimeout: 20 * time.Millisecond}
	ai.cancel = &streamCancel{
		fn:          func() { cancelled.Store(true) },
		lifecycleID: "chat:blocked",
		done:        make(chan struct{}),
	}
	if err := ai.cancelSessionStreamAndWait("chat:blocked"); !errors.Is(err, ErrAIStreamShutdownTimeout) {
		t.Fatalf("cancelSessionStreamAndWait = %v, want ErrAIStreamShutdownTimeout", err)
	}
	if !cancelled.Load() {
		t.Fatal("timed out shutdown did not first cancel the provider stream")
	}
}
