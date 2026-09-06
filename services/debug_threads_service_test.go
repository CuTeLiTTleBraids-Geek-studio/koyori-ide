package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeDebugThreadsSession struct {
	snapshot      DebugThreadsSessionSnapshot
	stack         []StackFrame
	localsCleared bool
	asyncCleared  bool
	updates       []DebugThreadsSessionUpdate
}

type fakeDebugThreadsRequest struct {
	run     DebugThreadsRunIdentity
	command string
	args    map[string]any
}

type fakeDebugThreadsHandler func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error)

type fakeDebugThreadsBackend struct {
	mu       sync.Mutex
	active   string
	sessions map[string]*fakeDebugThreadsSession
	handlers map[string]fakeDebugThreadsHandler
	requests chan fakeDebugThreadsRequest
}

func newFakeDebugThreadsBackend() *fakeDebugThreadsBackend {
	return &fakeDebugThreadsBackend{
		active: "default",
		sessions: map[string]*fakeDebugThreadsSession{
			"default": {
				snapshot: DebugThreadsSessionSnapshot{
					SessionID:  "default",
					RunID:      "owner-1/run-1",
					Generation: 1,
				},
			},
		},
		handlers: make(map[string]fakeDebugThreadsHandler),
		requests: make(chan fakeDebugThreadsRequest, 64),
	}
}

func (f *fakeDebugThreadsBackend) Snapshot(sessionID string) (DebugThreadsSessionSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if sessionID == "" {
		sessionID = f.active
	}
	session := f.sessions[sessionID]
	if session == nil {
		return DebugThreadsSessionSnapshot{}, fmt.Errorf("unknown fake session %q", sessionID)
	}
	return session.snapshot, nil
}

func (f *fakeDebugThreadsBackend) Request(
	run DebugThreadsRunIdentity,
	command string,
	args map[string]any,
) (json.RawMessage, error) {
	f.mu.Lock()
	session := f.sessions[run.SessionID]
	if session == nil || !sameRun(session.snapshot.Identity(), run) {
		f.mu.Unlock()
		return nil, ErrDebugThreadsStaleRun
	}
	handler := f.handlers[command]
	request := fakeDebugThreadsRequest{run: run, command: command, args: cloneAnyMap(args)}
	f.mu.Unlock()
	f.requests <- request
	if handler == nil {
		return json.RawMessage(`{}`), nil
	}
	return handler(run, args)
}

func (f *fakeDebugThreadsBackend) SetActiveSession(sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessions[sessionID] == nil {
		return fmt.Errorf("unknown fake session %q", sessionID)
	}
	f.active = sessionID
	return nil
}

func (f *fakeDebugThreadsBackend) ApplySessionUpdate(
	expected DebugThreadsSessionSnapshot,
	update DebugThreadsSessionUpdate,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	session := f.sessions[expected.SessionID]
	if session == nil || !sameRun(session.snapshot.Identity(), expected.Identity()) {
		return ErrDebugThreadsStaleRun
	}
	if session.snapshot.StateRevision != expected.StateRevision {
		return ErrDebugThreadsStaleState
	}
	if update.ThreadID != nil {
		session.snapshot.ThreadID = *update.ThreadID
	}
	if update.Stopped != nil {
		session.snapshot.Stopped = *update.Stopped
	}
	if update.StopReason != nil {
		session.snapshot.StopReason = *update.StopReason
	}
	if update.ReplaceStack {
		session.stack = fromDebugStackFrames(update.Stack)
	}
	if update.ClearLocals {
		session.localsCleared = true
	}
	if update.ClearAsyncStack {
		session.asyncCleared = true
	}
	session.updates = append(session.updates, cloneSessionUpdate(update))
	session.snapshot.StateRevision++
	return nil
}

func (f *fakeDebugThreadsBackend) setHandler(command string, handler fakeDebugThreadsHandler) {
	f.mu.Lock()
	f.handlers[command] = handler
	f.mu.Unlock()
}

func (f *fakeDebugThreadsBackend) setSnapshot(update func(*DebugThreadsSessionSnapshot)) {
	f.mu.Lock()
	update(&f.sessions["default"].snapshot)
	f.mu.Unlock()
}

func (f *fakeDebugThreadsBackend) replaceRun(runID string, generation uint64) {
	f.mu.Lock()
	f.sessions["default"] = &fakeDebugThreadsSession{
		snapshot: DebugThreadsSessionSnapshot{
			SessionID:  "default",
			RunID:      runID,
			Generation: generation,
		},
	}
	f.mu.Unlock()
}

func (f *fakeDebugThreadsBackend) sessionCopy() fakeDebugThreadsSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	session := f.sessions["default"]
	copy := *session
	copy.stack = cloneStackFrames(session.stack)
	copy.updates = append([]DebugThreadsSessionUpdate(nil), session.updates...)
	return copy
}

func readFakeDebugThreadsRequest(t *testing.T, requests <-chan fakeDebugThreadsRequest) fakeDebugThreadsRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fake DAP request")
		return fakeDebugThreadsRequest{}
	}
}

type deferredDebugThreadsResponse struct {
	seen    chan struct{}
	release chan struct{}
	once    sync.Once
}

func newDeferredDebugThreadsResponse() *deferredDebugThreadsResponse {
	return &deferredDebugThreadsResponse{seen: make(chan struct{}), release: make(chan struct{})}
}

func (d *deferredDebugThreadsResponse) block() {
	d.once.Do(func() { close(d.seen) })
	<-d.release
}

func (d *deferredDebugThreadsResponse) wait(t *testing.T) {
	t.Helper()
	select {
	case <-d.seen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for deferred response")
	}
}

func TestDebugThreadsService_ListThreadsAndPaginatedStack(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setSnapshot(func(snapshot *DebugThreadsSessionSnapshot) {
		snapshot.Stopped = true
		snapshot.ThreadID = 2
	})
	backend.setHandler("threads", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"threads":[{"id":1,"name":"worker"},{"id":2,"name":""}]}`), nil
	})
	backend.setHandler("stackTrace", func(_ DebugThreadsRunIdentity, args map[string]any) (json.RawMessage, error) {
		start := args["startFrame"].(int)
		if start == 0 {
			return json.RawMessage(`{
				"stackFrames":[
					{"id":10,"name":"first","line":10,"column":1,"endLine":12,"endColumn":3,"moduleId":"core","presentationHint":"subtle","source":{"path":"/tmp/main.go"}},
					{"id":11,"name":"second","line":20,"column":2,"source":{"name":"fallback.go"}}
				],"totalFrames":3
			}`), nil
		}
		return json.RawMessage(`{
			"stackFrames":[{"id":12,"name":"third","line":30,"column":3}],"totalFrames":3
		}`), nil
	})
	events := make(chan string, 8)
	svc := NewDebugThreadsServiceWithEmitter(backend, func(name string, _ any) { events <- name })

	threads, err := svc.ListThreads(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 || threads[0].State != ThreadStateRunning || threads[0].Selected {
		t.Fatalf("threads = %+v", threads)
	}
	if threads[1].Name != "" || threads[1].State != ThreadStateStopped || !threads[1].Selected {
		t.Fatalf("unnamed stopped thread = %+v", threads[1])
	}
	frames, err := svc.GetThreadStackTrace(context.Background(), "default", 2, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 || frames[0].File != "/tmp/main.go" || frames[0].Source != "/tmp/main.go" ||
		frames[0].EndLine != 12 || frames[0].EndColumn != 3 || frames[0].Module != "core" ||
		frames[0].PresentationHint != "subtle" || frames[1].File != "fallback.go" {
		t.Fatalf("frames = %+v", frames)
	}
	if readFakeDebugThreadsRequest(t, backend.requests).command != "threads" {
		t.Fatal("first request was not threads")
	}
	for index, start := range []int{0, 2} {
		request := readFakeDebugThreadsRequest(t, backend.requests)
		if request.command != "stackTrace" || request.args["threadId"] != 2 || request.args["startFrame"] != start {
			t.Fatalf("stack request %d = %+v", index, request)
		}
	}
	if len(backend.sessionCopy().stack) != 3 {
		t.Fatal("selected stack was not applied through the backend")
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
}

func TestDebugThreadsService_RequestedStackPageAndCancellation(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	stackRequests := 0
	backend.setHandler("stackTrace", func(_ DebugThreadsRunIdentity, args map[string]any) (json.RawMessage, error) {
		stackRequests++
		expectedStart := 3 + stackRequests
		expectedLevels := 3 - stackRequests
		if args["threadId"] != 7 || args["startFrame"] != expectedStart || args["levels"] != expectedLevels {
			t.Fatalf("stack page args = %+v", args)
		}
		frameID := 69 + stackRequests
		return json.RawMessage(fmt.Sprintf(`{
			"stackFrames":[{"id":%d,"name":"paged","line":5,"column":1}],
			"totalFrames":9
		}`, frameID)), nil
	})
	svc := NewDebugThreadsService(backend)

	frames, err := svc.GetThreadStackTrace(context.Background(), "default", 7, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0].ID != 70 || frames[1].ID != 71 || stackRequests != 2 {
		t.Fatalf("frames = %+v", frames)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.ListThreads(canceled, "default"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ListThreads error = %v", err)
	}
}

func TestDebugThreadsService_OutOfOrderStackPagesAreBufferedAndMerged(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setSnapshot(func(snapshot *DebugThreadsSessionSnapshot) {
		snapshot.Stopped = true
		snapshot.ThreadID = 1
	})
	prefixGate := newDeferredDebugThreadsResponse()
	backend.setHandler("stackTrace", func(_ DebugThreadsRunIdentity, args map[string]any) (json.RawMessage, error) {
		switch args["startFrame"] {
		case 0:
			prefixGate.block()
			return json.RawMessage(`{
				"stackFrames":[
					{"id":10,"name":"zero","line":1,"column":1},
					{"id":11,"name":"one","line":2,"column":1}
				],"totalFrames":4
			}`), nil
		case 2:
			return json.RawMessage(`{
				"stackFrames":[
					{"id":12,"name":"two","line":3,"column":1},
					{"id":13,"name":"three","line":4,"column":1}
				],"totalFrames":4
			}`), nil
		default:
			return nil, fmt.Errorf("unexpected stack offset %v", args["startFrame"])
		}
	})
	svc := NewDebugThreadsService(backend)

	type stackResult struct {
		frames []StackFrame
		err    error
	}
	prefixResult := make(chan stackResult, 1)
	go func() {
		frames, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 0, 2)
		prefixResult <- stackResult{frames: frames, err: err}
	}()
	prefixGate.wait(t)
	defer func() {
		select {
		case <-prefixGate.release:
		default:
			close(prefixGate.release)
		}
	}()

	later, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertStackFrameIDs(t, later, 12, 13)
	if cached := svc.cachedThreadFrames("default", 1); len(cached) != 0 {
		t.Fatalf("gap page was applied before its prefix: %+v", cached)
	}
	svc.mu.RLock()
	_, buffered := svc.pendingStackPages["default"][1][2]
	svc.mu.RUnlock()
	if !buffered {
		t.Fatal("out-of-order page was not buffered")
	}

	close(prefixGate.release)
	prefix := <-prefixResult
	if prefix.err != nil {
		t.Fatal(prefix.err)
	}
	assertStackFrameIDs(t, prefix.frames, 10, 11)
	assertStackFrameIDs(t, svc.cachedThreadFrames("default", 1), 10, 11, 12, 13)
	svc.mu.RLock()
	remainingPages := len(svc.pendingStackPages["default"][1])
	svc.mu.RUnlock()
	if remainingPages != 0 {
		t.Fatalf("buffered pages remaining after prefix merge = %d", remainingPages)
	}
}

func TestDebugThreadsService_ShortStackPageTruncatesOldTail(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	refresh := false
	backend.setHandler("stackTrace", func(_ DebugThreadsRunIdentity, args map[string]any) (json.RawMessage, error) {
		if !refresh {
			return json.RawMessage(`{
				"stackFrames":[
					{"id":10,"name":"zero","line":1,"column":1},
					{"id":11,"name":"one","line":2,"column":1},
					{"id":12,"name":"two","line":3,"column":1},
					{"id":13,"name":"three","line":4,"column":1}
				],"totalFrames":4
			}`), nil
		}
		if args["startFrame"] != 1 || args["levels"] != 2 {
			return nil, fmt.Errorf("unexpected refresh args: %+v", args)
		}
		return json.RawMessage(`{
			"stackFrames":[{"id":20,"name":"replacement","line":20,"column":1}]
		}`), nil
	})
	svc := NewDebugThreadsService(backend)

	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	assertStackFrameIDs(t, svc.cachedThreadFrames("default", 1), 10, 11, 12, 13)
	refresh = true
	page, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertStackFrameIDs(t, page, 20)
	assertStackFrameIDs(t, svc.cachedThreadFrames("default", 1), 10, 20)
}

func TestDebugThreadsService_StackEndDropsBufferedPagesBeyondEnd(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setHandler("stackTrace", func(_ DebugThreadsRunIdentity, args map[string]any) (json.RawMessage, error) {
		switch args["startFrame"] {
		case 3:
			return json.RawMessage(`{
				"stackFrames":[{"id":13,"name":"stale-later","line":4,"column":1}],
				"totalFrames":4
			}`), nil
		case 0:
			return json.RawMessage(`{
				"stackFrames":[
					{"id":10,"name":"zero","line":1,"column":1},
					{"id":11,"name":"one","line":2,"column":1}
				],"totalFrames":2
			}`), nil
		default:
			return nil, fmt.Errorf("unexpected stack offset %v", args["startFrame"])
		}
	})
	svc := NewDebugThreadsService(backend)

	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 3, 1); err != nil {
		t.Fatal(err)
	}
	svc.mu.RLock()
	_, buffered := svc.pendingStackPages["default"][1][3]
	svc.mu.RUnlock()
	if !buffered {
		t.Fatal("later page was not buffered")
	}
	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 0, 4); err != nil {
		t.Fatal(err)
	}
	assertStackFrameIDs(t, svc.cachedThreadFrames("default", 1), 10, 11)
	svc.mu.RLock()
	remainingPages := len(svc.pendingStackPages["default"][1])
	svc.mu.RUnlock()
	if remainingPages != 0 {
		t.Fatalf("pages beyond the authoritative stack end were retained: %d", remainingPages)
	}
}

func TestDebugThreadsService_ListThreadsDropsRemovedThreadPages(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setHandler("stackTrace", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{
			"stackFrames":[{"id":22,"name":"later","line":3,"column":1}],
			"totalFrames":3
		}`), nil
	})
	backend.setHandler("threads", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"threads":[{"id":1,"name":"remaining"}]}`), nil
	})
	svc := NewDebugThreadsService(backend)

	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 2, 2, 1); err != nil {
		t.Fatal(err)
	}
	svc.mu.RLock()
	_, buffered := svc.pendingStackPages["default"][2][2]
	svc.mu.RUnlock()
	if !buffered {
		t.Fatal("removed thread setup did not create a buffered page")
	}
	threads, err := svc.ListThreads(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].ID != 1 {
		t.Fatalf("threads = %+v", threads)
	}
	svc.mu.RLock()
	_, sessionPending := svc.pendingStackPages["default"]
	svc.mu.RUnlock()
	if sessionPending {
		t.Fatal("removed thread retained buffered stack pages")
	}
}

func TestDebugThreadsService_RunningSnapshotInvalidatesStoppedStackPages(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setSnapshot(func(snapshot *DebugThreadsSessionSnapshot) {
		snapshot.Stopped = true
		snapshot.ThreadID = 1
	})
	backend.setHandler("threads", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"threads":[{"id":1,"name":"worker"}]}`), nil
	})
	stalePageGate := newDeferredDebugThreadsResponse()
	backend.setHandler("stackTrace", func(_ DebugThreadsRunIdentity, args map[string]any) (json.RawMessage, error) {
		switch args["startFrame"] {
		case 0:
			snapshot, err := backend.Snapshot("default")
			if err != nil {
				return nil, err
			}
			if snapshot.Stopped {
				return json.RawMessage(`{
					"stackFrames":[
						{"id":10,"name":"old-zero","line":1,"column":1},
						{"id":11,"name":"old-one","line":2,"column":1}
					],"totalFrames":7
				}`), nil
			}
			return json.RawMessage(`{
				"stackFrames":[
					{"id":20,"name":"new-zero","line":1,"column":1},
					{"id":21,"name":"new-one","line":2,"column":1}
				],"totalFrames":4
			}`), nil
		case 2:
			return json.RawMessage(`{
				"stackFrames":[
					{"id":22,"name":"new-two","line":3,"column":1},
					{"id":23,"name":"new-three","line":4,"column":1}
				],"totalFrames":4
			}`), nil
		case 4:
			return json.RawMessage(`{
				"stackFrames":[{"id":14,"name":"old-four","line":5,"column":1}],
				"totalFrames":7
			}`), nil
		case 6:
			stalePageGate.block()
			return json.RawMessage(`{
				"stackFrames":[{"id":16,"name":"old-six","line":7,"column":1}],
				"totalFrames":7
			}`), nil
		default:
			return nil, fmt.Errorf("unexpected stack offset %v", args["startFrame"])
		}
	})
	svc := NewDebugThreadsService(backend)

	if _, err := svc.ListThreads(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 0, 2); err != nil {
		t.Fatal(err)
	}
	assertStackFrameIDs(t, svc.cachedThreadFrames("default", 1), 10, 11)
	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 4, 1); err != nil {
		t.Fatal(err)
	}
	svc.mu.RLock()
	_, buffered := svc.pendingStackPages["default"][1][4]
	svc.mu.RUnlock()
	if !buffered {
		t.Fatal("stopped stack setup did not create a buffered page")
	}

	type stackResult struct {
		frames []StackFrame
		err    error
	}
	staleResult := make(chan stackResult, 1)
	go func() {
		frames, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 6, 1)
		staleResult <- stackResult{frames: frames, err: err}
	}()
	stalePageGate.wait(t)
	defer func() {
		select {
		case <-stalePageGate.release:
		default:
			close(stalePageGate.release)
		}
	}()

	backend.setSnapshot(func(snapshot *DebugThreadsSessionSnapshot) {
		snapshot.Stopped = false
	})
	threads, err := svc.ListThreads(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].State != ThreadStateRunning || len(threads[0].Frames) != 0 {
		t.Fatalf("running threads retained stopped state: %+v", threads)
	}
	svc.mu.RLock()
	_, pendingAfterResume := svc.pendingStackPages["default"]
	svc.mu.RUnlock()
	if pendingAfterResume {
		t.Fatal("running snapshot retained buffered stopped-stack pages")
	}

	close(stalePageGate.release)
	stale := <-staleResult
	if stale.err != nil {
		t.Fatal(stale.err)
	}
	if len(stale.frames) != 0 {
		t.Fatalf("in-flight stopped-stack page survived running snapshot: %+v", stale.frames)
	}
	svc.mu.RLock()
	_, pendingAfterStaleResponse := svc.pendingStackPages["default"]
	svc.mu.RUnlock()
	if pendingAfterStaleResponse {
		t.Fatal("in-flight stopped-stack page was buffered after resume")
	}

	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 2, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 0, 2); err != nil {
		t.Fatal(err)
	}
	assertStackFrameIDs(t, svc.cachedThreadFrames("default", 1), 20, 21, 22, 23)
}

func TestDebugThreadsService_StoppedEventInvalidatesPreviousStack(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setSnapshot(func(snapshot *DebugThreadsSessionSnapshot) {
		snapshot.Stopped = true
		snapshot.ThreadID = 1
	})
	backend.setHandler("threads", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"threads":[{"id":1,"name":"worker"}]}`), nil
	})
	backend.setHandler("stackTrace", func(_ DebugThreadsRunIdentity, args map[string]any) (json.RawMessage, error) {
		switch args["startFrame"] {
		case 0:
			return json.RawMessage(`{
				"stackFrames":[
					{"id":10,"name":"old-zero","line":1,"column":1},
					{"id":11,"name":"old-one","line":2,"column":1}
				],"totalFrames":4
			}`), nil
		case 3:
			return json.RawMessage(`{
				"stackFrames":[{"id":13,"name":"old-three","line":4,"column":1}],
				"totalFrames":4
			}`), nil
		default:
			return nil, fmt.Errorf("unexpected stack offset %v", args["startFrame"])
		}
	})
	svc := NewDebugThreadsService(backend)

	if _, err := svc.ListThreads(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 0, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 3, 1); err != nil {
		t.Fatal(err)
	}
	assertStackFrameIDs(t, svc.cachedThreadFrames("default", 1), 10, 11)
	svc.mu.RLock()
	_, buffered := svc.pendingStackPages["default"][1][3]
	svc.mu.RUnlock()
	if !buffered {
		t.Fatal("stack setup did not create a buffered page")
	}

	if err := svc.HandleStoppedEvent("default", DebugStoppedEvent{
		Reason:   "breakpoint",
		ThreadID: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if frames := svc.cachedThreadFrames("default", 1); len(frames) != 0 {
		t.Fatalf("new stopped event retained the previous stack: %+v", frames)
	}
	svc.mu.RLock()
	_, pending := svc.pendingStackPages["default"]
	svc.mu.RUnlock()
	if pending {
		t.Fatal("new stopped event retained buffered stack pages")
	}
	state := backend.sessionCopy()
	if len(state.stack) != 0 || !state.localsCleared || !state.asyncCleared {
		t.Fatalf("canonical stopped state retained stale debugger data: %+v", state)
	}
}

func TestDebugThreadsService_ListRemovalInvalidatesInFlightStack(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	stackGate := newDeferredDebugThreadsResponse()
	backend.setHandler("stackTrace", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		stackGate.block()
		return json.RawMessage(`{
			"stackFrames":[{"id":20,"name":"removed","line":2,"column":1}],
			"totalFrames":1
		}`), nil
	})
	backend.setHandler("threads", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"threads":[{"id":1,"name":"remaining"}]}`), nil
	})
	svc := NewDebugThreadsService(backend)
	type stackResult struct {
		frames []StackFrame
		err    error
	}
	result := make(chan stackResult, 1)
	go func() {
		frames, err := svc.GetThreadStackTrace(context.Background(), "default", 2, 0, 1)
		result <- stackResult{frames: frames, err: err}
	}()
	stackGate.wait(t)
	if _, err := svc.ListThreads(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	close(stackGate.release)
	response := <-result
	if response.err != nil {
		t.Fatal(response.err)
	}
	if len(response.frames) != 0 {
		t.Fatalf("removed thread returned stale frames: %+v", response.frames)
	}
	if frames := svc.cachedThreadFrames("default", 2); len(frames) != 0 {
		t.Fatalf("removed thread was recreated by stale response: %+v", frames)
	}
}

func TestDebugThreadsService_RejectsInvalidAndDuplicateStackFrameIDs(t *testing.T) {
	tests := []struct {
		name     string
		response json.RawMessage
	}{
		{
			name:     "invalid",
			response: json.RawMessage(`{"stackFrames":[{"id":0,"name":"invalid","line":1,"column":1}]}`),
		},
		{
			name: "duplicate in page",
			response: json.RawMessage(`{
				"stackFrames":[
					{"id":10,"name":"one","line":1,"column":1},
					{"id":10,"name":"two","line":2,"column":1}
			}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeDebugThreadsBackend()
			backend.setHandler("stackTrace", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
				return test.response, nil
			})
			svc := NewDebugThreadsService(backend)
			if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 0, 0); err == nil {
				t.Fatal("invalid stack response was accepted")
			}
			if frames := svc.cachedThreadFrames("default", 1); len(frames) != 0 {
				t.Fatalf("invalid frames reached cache: %+v", frames)
			}
		})
	}

	t.Run("duplicate across adapter pages", func(t *testing.T) {
		backend := newFakeDebugThreadsBackend()
		backend.setHandler("stackTrace", func(_ DebugThreadsRunIdentity, args map[string]any) (json.RawMessage, error) {
			if args["startFrame"] == 0 {
				return json.RawMessage(`{
					"stackFrames":[{"id":10,"name":"first","line":1,"column":1}],
					"totalFrames":2
				}`), nil
			}
			return json.RawMessage(`{
				"stackFrames":[{"id":10,"name":"repeated","line":2,"column":1}],
				"totalFrames":2
			}`), nil
		})
		svc := NewDebugThreadsService(backend)
		if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 0, 0); err == nil {
			t.Fatal("frame id repeated across pages was accepted")
		}
		if frames := svc.cachedThreadFrames("default", 1); len(frames) != 0 {
			t.Fatalf("duplicate cross-page frames reached cache: %+v", frames)
		}
	})
}

func TestDebugThreadsService_StaleStackResponseReturnsRequestedCachedWindow(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setHandler("stackTrace", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{
			"stackFrames":[
				{"id":10,"name":"zero","line":1,"column":1},
				{"id":11,"name":"one","line":2,"column":1},
				{"id":12,"name":"two","line":3,"column":1},
				{"id":13,"name":"three","line":4,"column":1}
			],"totalFrames":4
		}`), nil
	})
	svc := NewDebugThreadsService(backend)
	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 0, 0); err != nil {
		t.Fatal(err)
	}

	staleGate := newDeferredDebugThreadsResponse()
	backend.setHandler("stackTrace", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		staleGate.block()
		return json.RawMessage(`{
			"stackFrames":[
				{"id":20,"name":"stale-two","line":20,"column":1},
				{"id":21,"name":"stale-three","line":21,"column":1}
			],"totalFrames":4
		}`), nil
	})
	type stackResult struct {
		frames []StackFrame
		err    error
	}
	result := make(chan stackResult, 1)
	go func() {
		frames, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 2, 2)
		result <- stackResult{frames: frames, err: err}
	}()
	staleGate.wait(t)
	if err := svc.HandleThreadEvent("default", "started", 2); err != nil {
		t.Fatal(err)
	}
	close(staleGate.release)
	response := <-result
	if response.err != nil {
		t.Fatal(response.err)
	}
	assertStackFrameIDs(t, response.frames, 12, 13)
	assertStackFrameIDs(t, svc.cachedThreadFrames("default", 1), 10, 11, 12, 13)
}

func TestDebugThreadsService_SelectContinueAndStepUseBackendUpdates(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setSnapshot(func(snapshot *DebugThreadsSessionSnapshot) {
		snapshot.Stopped = true
		snapshot.ThreadID = 2
	})
	backend.setHandler("threads", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"threads":[{"id":1,"name":"worker"},{"id":2,"name":"main"}]}`), nil
	})
	backend.setHandler("continue", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"allThreadsContinued":false}`), nil
	})
	backend.setHandler("next", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	svc := NewDebugThreadsService(backend)
	if _, err := svc.ListThreads(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	_ = readFakeDebugThreadsRequest(t, backend.requests)
	if err := svc.ContinueThread(context.Background(), "default", 1); err == nil {
		t.Fatal("unknown single-thread capability should reject continue")
	}
	if err := svc.StepThread(context.Background(), "default", 1, "next"); err == nil {
		t.Fatal("unknown single-thread capability should reject step")
	}
	if len(backend.requests) != 0 {
		t.Fatal("unsupported controls sent a DAP request")
	}
	if err := svc.SetCapabilities("default", DebugThreadsCapabilities{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ContinueThread(context.Background(), "default", 1); err == nil {
		t.Fatal("explicitly unsupported single-thread continue should fail")
	}
	if err := svc.SetCapabilities("default", DebugThreadsCapabilities{
		SupportsSingleThreadExecutionRequests: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SelectThread(context.Background(), "default", 1); err != nil {
		t.Fatal(err)
	}
	selectedBackend := backend.sessionCopy()
	if selectedBackend.snapshot.ThreadID != 1 || !selectedBackend.localsCleared {
		t.Fatalf("select backend update = %+v", selectedBackend)
	}
	if err := svc.ContinueThread(context.Background(), "default", 1); err != nil {
		t.Fatal(err)
	}
	request := readFakeDebugThreadsRequest(t, backend.requests)
	assertFakeSingleThreadArgs(t, request, "continue", 1)
	assertCachedThreadState(t, svc, "default", 1, ThreadStateRunning, true)
	assertCachedThreadState(t, svc, "default", 2, ThreadStateStopped, false)
	continuedBackend := backend.sessionCopy()
	if !continuedBackend.snapshot.Stopped || !continuedBackend.asyncCleared {
		t.Fatalf("continue backend state = %+v", continuedBackend)
	}
	if err := svc.StepThread(context.Background(), "default", 1, "step-over"); err != nil {
		t.Fatal(err)
	}
	request = readFakeDebugThreadsRequest(t, backend.requests)
	assertFakeSingleThreadArgs(t, request, "next", 1)
	assertCachedThreadState(t, svc, "default", 1, ThreadStateStepping, true)
}

func TestDebugThreadsService_AllThreadControlsAndStoppedEvent(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setSnapshot(func(snapshot *DebugThreadsSessionSnapshot) {
		snapshot.Stopped = true
		snapshot.ThreadID = 2
	})
	backend.setHandler("threads", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"threads":[{"id":1,"name":"worker"},{"id":2,"name":"main"}]}`), nil
	})
	backend.setHandler("continue", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"allThreadsContinued":true}`), nil
	})
	backend.setHandler("pause", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	type emittedEvent struct {
		name    string
		payload any
	}
	events := make(chan emittedEvent, 16)
	svc := NewDebugThreadsServiceWithEmitter(backend, func(name string, payload any) {
		events <- emittedEvent{name: name, payload: payload}
	})

	if _, err := svc.ListThreads(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	_ = readFakeDebugThreadsRequest(t, backend.requests)
	<-events

	if err := svc.ContinueAllThreads(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	continued := readFakeDebugThreadsRequest(t, backend.requests)
	if continued.command != "continue" || continued.args["threadId"] != 2 || continued.args["singleThread"] != false {
		t.Fatalf("continue-all request = %+v", continued)
	}
	assertCachedThreadState(t, svc, "default", 1, ThreadStateRunning, false)
	assertCachedThreadState(t, svc, "default", 2, ThreadStateRunning, true)
	if backend.sessionCopy().snapshot.Stopped {
		t.Fatal("continue all did not resume the backend session")
	}

	if err := svc.PauseAllThreads(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	paused := readFakeDebugThreadsRequest(t, backend.requests)
	_, hasSingleThread := paused.args["singleThread"]
	if paused.command != "pause" || paused.args["threadId"] != 2 || hasSingleThread {
		t.Fatalf("pause-all request = %+v", paused)
	}
	for len(events) > 0 {
		<-events
	}

	if err := svc.HandleStoppedEvent("default", DebugStoppedEvent{
		Reason:            "pause",
		ThreadID:          1,
		AllThreadsStopped: true,
	}); err != nil {
		t.Fatal(err)
	}
	assertCachedThreadState(t, svc, "default", 1, ThreadStateStopped, true)
	assertCachedThreadState(t, svc, "default", 2, ThreadStateStopped, false)
	var stopped DebugThreadStoppedEvent
	for range 3 {
		event := <-events
		if event.name == DebugThreadStoppedEventName {
			var ok bool
			stopped, ok = event.payload.(DebugThreadStoppedEvent)
			if !ok {
				t.Fatalf("stopped payload type = %T", event.payload)
			}
		}
	}
	if stopped.SessionID != "default" || stopped.ThreadID != 1 || stopped.Reason != "pause" || !stopped.AllThreadsStopped {
		t.Fatalf("stopped event = %+v", stopped)
	}
}

func TestDebugThreadsService_OmittedAllThreadsContinuedMeansAll(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setHandler("threads", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"threads":[{"id":1,"name":"one"},{"id":2,"name":"two"}]}`), nil
	})
	backend.setHandler("continue", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	svc := NewDebugThreadsService(backend)
	if _, err := svc.ListThreads(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	_ = readFakeDebugThreadsRequest(t, backend.requests)
	if err := svc.SetCapabilities("default", DebugThreadsCapabilities{SupportsSingleThreadExecutionRequests: true}); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleStopped("default", 1, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.ContinueThread(context.Background(), "default", 1); err != nil {
		t.Fatal(err)
	}
	_ = readFakeDebugThreadsRequest(t, backend.requests)
	assertCachedThreadState(t, svc, "default", 1, ThreadStateRunning, true)
	assertCachedThreadState(t, svc, "default", 2, ThreadStateRunning, false)
	if backend.sessionCopy().snapshot.Stopped {
		t.Fatal("omitted ContinueResponse field should continue all threads")
	}

	if err := svc.HandleStopped("default", 1, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleDAPEvent("default", "continued", json.RawMessage(`{"threadId":1}`)); err != nil {
		t.Fatal(err)
	}
	assertCachedThreadState(t, svc, "default", 2, ThreadStateRunning, false)
	if backend.sessionCopy().snapshot.Stopped {
		t.Fatal("omitted continued-event field should continue all threads")
	}

	if err := svc.HandleStopped("default", 1, true); err != nil {
		t.Fatal(err)
	}
	falseValue := false
	if err := svc.HandleContinuedEvent("default", DebugContinuedEvent{
		ThreadID: 1, AllThreadsContinued: &falseValue,
	}); err != nil {
		t.Fatal(err)
	}
	assertCachedThreadState(t, svc, "default", 1, ThreadStateRunning, true)
	assertCachedThreadState(t, svc, "default", 2, ThreadStateStopped, false)
	if !backend.sessionCopy().snapshot.Stopped {
		t.Fatal("explicit false should preserve another stopped thread")
	}
}

func TestDebugThreadsService_EventsCapabilitiesAndRunTokens(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setHandler("stepInTargets", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"targets":[{"id":7,"label":"callee"}]}`), nil
	})
	backend.setHandler("terminate", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	svc := NewDebugThreadsService(backend)
	token, err := svc.CaptureRunToken("default")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyInitializeCapabilitiesForRun("default", token, json.RawMessage(`{
		"supportsStepInTargetsRequest":true,
		"supportsTerminateRequest":true,
		"supportsSingleThreadExecutionRequests":true
	}`)); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleDAPEventForRun("default", token, "stopped", json.RawMessage(`{
		"reason":"breakpoint","threadId":2,"allThreadsStopped":true
	}`)); err != nil {
		t.Fatal(err)
	}
	backendState := backend.sessionCopy()
	if !backendState.snapshot.Stopped || backendState.snapshot.ThreadID != 2 || backendState.snapshot.StopReason != "breakpoint" {
		t.Fatalf("stopped backend update = %+v", backendState.snapshot)
	}
	if err := svc.HandleDAPEventForRun("default", token, "thread", json.RawMessage(`{"reason":"started","threadId":3}`)); err != nil {
		t.Fatal(err)
	}
	assertCachedThreadState(t, svc, "default", 3, ThreadStateRunning, false)
	if err := svc.HandleDAPEventForRun("default", token, "thread", json.RawMessage(`{"reason":"exited","threadId":2}`)); err != nil {
		t.Fatal(err)
	}
	assertCachedThreadState(t, svc, "default", 3, ThreadStateRunning, true)
	if state := backend.sessionCopy(); state.snapshot.ThreadID != 3 || len(state.stack) != 0 || !state.localsCleared {
		t.Fatalf("selected thread exit backend update = %+v", state)
	}
	targets, err := svc.GetStepInTargets("default", 5)
	if err != nil || len(targets) != 1 || targets[0].ID != 7 {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	_ = readFakeDebugThreadsRequest(t, backend.requests)
	if err := svc.Terminate("default", true); err != nil {
		t.Fatal(err)
	}
	terminateRequest := readFakeDebugThreadsRequest(t, backend.requests)
	if terminateRequest.command != "terminate" || terminateRequest.args["restart"] != true {
		t.Fatalf("terminate request = %+v", terminateRequest)
	}
	if state := backend.sessionCopy(); state.snapshot.Stopped || state.snapshot.StopReason != "terminated" {
		t.Fatalf("terminate backend update = %+v", state.snapshot)
	}

	backend.replaceRun("owner-2/run-1", token.Generation)
	replacement, err := svc.CaptureRunToken("default")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.RunID == token.RunID || replacement.Generation != token.Generation {
		t.Fatalf("replacement token old=%+v new=%+v", token, replacement)
	}
	if err := svc.HandleDAPEventForRun("default", token, "stopped", json.RawMessage(`{"threadId":99}`)); !errors.Is(err, ErrDebugThreadsStaleRun) {
		t.Fatalf("old token event err=%v", err)
	}
	if err := svc.ApplyInitializeCapabilitiesForRun("default", token, json.RawMessage(`{}`)); !errors.Is(err, ErrDebugThreadsStaleRun) {
		t.Fatalf("old token capabilities err=%v", err)
	}
	if threads := svc.cachedThreads("default"); len(threads) != 0 {
		t.Fatalf("replacement retained old cache: %+v", threads)
	}
	if err := svc.HandleDAPEventForRun("default", replacement, "stopped", json.RawMessage(`{"threadId":8}`)); err != nil {
		t.Fatal(err)
	}
	assertCachedThreadState(t, svc, "default", 8, ThreadStateStopped, true)
}

func TestDebugThreadsService_SelectedThreadExitUsesBackendSelection(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	backend.setSnapshot(func(snapshot *DebugThreadsSessionSnapshot) {
		snapshot.Stopped = true
		snapshot.ThreadID = 2
	})
	backend.mu.Lock()
	backend.sessions["default"].stack = []StackFrame{{ID: 20, Name: "old", Line: 1, Column: 1}}
	backend.mu.Unlock()
	type emittedEvent struct {
		name    string
		payload any
	}
	events := make(chan emittedEvent, 8)
	svc := NewDebugThreadsServiceWithEmitter(backend, func(name string, payload any) {
		events <- emittedEvent{name: name, payload: payload}
	})

	if err := svc.HandleThreadEvent("default", "started", 3); err != nil {
		t.Fatal(err)
	}
	<-events
	if err := svc.HandleThreadEvent("default", "exited", 2); err != nil {
		t.Fatal(err)
	}
	assertCachedThreadState(t, svc, "default", 3, ThreadStateRunning, true)
	state := backend.sessionCopy()
	if state.snapshot.ThreadID != 3 || len(state.stack) != 0 || !state.localsCleared || !state.asyncCleared {
		t.Fatalf("selected thread exit backend update = %+v", state)
	}

	selectedSeen := false
	for range 2 {
		event := <-events
		if event.name != DebugThreadSelectedEventName {
			continue
		}
		selected, ok := event.payload.(DebugThreadSelectedEvent)
		if !ok {
			t.Fatalf("selected payload type = %T", event.payload)
		}
		if selected.SessionID != "default" || selected.ThreadID != 3 {
			t.Fatalf("selected event = %+v", selected)
		}
		selectedSeen = true
	}
	if !selectedSeen {
		t.Fatal("selected thread exit did not emit replacement selection")
	}
}

func TestDebugThreadsService_EventRevisionWinsOverDeferredResponses(t *testing.T) {
	backend := newFakeDebugThreadsBackend()
	threadsGate := newDeferredDebugThreadsResponse()
	stackGate := newDeferredDebugThreadsResponse()
	continueGate := newDeferredDebugThreadsResponse()
	stepGate := newDeferredDebugThreadsResponse()
	backend.setHandler("threads", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		threadsGate.block()
		return json.RawMessage(`{"threads":[{"id":1,"name":"main"}]}`), nil
	})
	backend.setHandler("stackTrace", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		stackGate.block()
		return json.RawMessage(`{"stackFrames":[{"id":10,"name":"stale","line":5,"column":1}],"totalFrames":1}`), nil
	})
	backend.setHandler("continue", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		continueGate.block()
		return json.RawMessage(`{"allThreadsContinued":true}`), nil
	})
	backend.setHandler("next", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
		stepGate.block()
		return json.RawMessage(`{}`), nil
	})
	svc := NewDebugThreadsService(backend)
	if err := svc.SetCapabilities("default", DebugThreadsCapabilities{SupportsSingleThreadExecutionRequests: true}); err != nil {
		t.Fatal(err)
	}

	type threadsResult struct {
		threads []ThreadInfo
		err     error
	}
	listed := make(chan threadsResult, 1)
	go func() {
		threads, err := svc.ListThreads(context.Background(), "default")
		listed <- threadsResult{threads: threads, err: err}
	}()
	threadsGate.wait(t)
	if err := svc.HandleStopped("default", 1, true); err != nil {
		t.Fatal(err)
	}
	close(threadsGate.release)
	listResult := <-listed
	if listResult.err != nil || len(listResult.threads) != 1 || listResult.threads[0].State != ThreadStateStopped {
		t.Fatalf("deferred list result=%+v err=%v", listResult.threads, listResult.err)
	}

	type stackResult struct {
		frames []StackFrame
		err    error
	}
	stacked := make(chan stackResult, 1)
	go func() {
		frames, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 0, 0)
		stacked <- stackResult{frames: frames, err: err}
	}()
	stackGate.wait(t)
	if err := svc.HandleContinuedEvent("default", DebugContinuedEvent{ThreadID: 1}); err != nil {
		t.Fatal(err)
	}
	close(stackGate.release)
	stackResponse := <-stacked
	if stackResponse.err != nil || len(stackResponse.frames) != 0 || len(svc.cachedThreadFrames("default", 1)) != 0 {
		t.Fatalf("deferred stack frames=%+v err=%v", stackResponse.frames, stackResponse.err)
	}

	if err := svc.HandleStopped("default", 1, true); err != nil {
		t.Fatal(err)
	}
	continued := make(chan error, 1)
	go func() { continued <- svc.ContinueThread(context.Background(), "default", 1) }()
	continueGate.wait(t)
	if err := svc.HandleStopped("default", 1, true); err != nil {
		t.Fatal(err)
	}
	close(continueGate.release)
	if err := <-continued; err != nil {
		t.Fatal(err)
	}
	assertCachedThreadState(t, svc, "default", 1, ThreadStateStopped, true)

	stepped := make(chan error, 1)
	go func() { stepped <- svc.StepThread(context.Background(), "default", 1, "next") }()
	stepGate.wait(t)
	if err := svc.HandleStopped("default", 1, true); err != nil {
		t.Fatal(err)
	}
	close(stepGate.release)
	if err := <-stepped; err != nil {
		t.Fatal(err)
	}
	assertCachedThreadState(t, svc, "default", 1, ThreadStateStopped, true)
}

func TestDebugThreadsService_ContextCancellationAfterRequestDoesNotCommit(t *testing.T) {
	t.Run("threads", func(t *testing.T) {
		backend := newFakeDebugThreadsBackend()
		gate := newDeferredDebugThreadsResponse()
		backend.setHandler("threads", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
			gate.block()
			return nil, errors.New("adapter failed after cancellation")
		})
		svc := NewDebugThreadsService(backend)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := svc.ListThreads(ctx, "default")
			result <- err
		}()
		gate.wait(t)
		cancel()
		close(gate.release)
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("ListThreads error = %v", err)
		}
		if threads := svc.cachedThreads("default"); len(threads) != 0 {
			t.Fatalf("canceled thread response reached cache: %+v", threads)
		}
	})

	t.Run("stackTrace", func(t *testing.T) {
		backend := newFakeDebugThreadsBackend()
		gate := newDeferredDebugThreadsResponse()
		backend.setHandler("stackTrace", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
			gate.block()
			return json.RawMessage(`{
				"stackFrames":[{"id":10,"name":"main","line":1,"column":1}],
				"totalFrames":1
			}`), nil
		})
		svc := NewDebugThreadsService(backend)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := svc.GetThreadStackTrace(ctx, "default", 1, 0, 1)
			result <- err
		}()
		gate.wait(t)
		cancel()
		close(gate.release)
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("GetThreadStackTrace error = %v", err)
		}
		if frames := svc.cachedThreadFrames("default", 1); len(frames) != 0 {
			t.Fatalf("canceled stack response reached cache: %+v", frames)
		}
	})

	t.Run("continue", func(t *testing.T) {
		backend := newFakeDebugThreadsBackend()
		backend.setSnapshot(func(snapshot *DebugThreadsSessionSnapshot) {
			snapshot.Stopped = true
			snapshot.ThreadID = 1
		})
		gate := newDeferredDebugThreadsResponse()
		backend.setHandler("continue", func(DebugThreadsRunIdentity, map[string]any) (json.RawMessage, error) {
			gate.block()
			return json.RawMessage(`{"allThreadsContinued":false}`), nil
		})
		svc := NewDebugThreadsService(backend)
		if err := svc.SetCapabilities("default", DebugThreadsCapabilities{
			SupportsSingleThreadExecutionRequests: true,
		}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- svc.ContinueThread(ctx, "default", 1) }()
		gate.wait(t)
		cancel()
		close(gate.release)
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("ContinueThread error = %v", err)
		}
		state := backend.sessionCopy()
		if !state.snapshot.Stopped || len(state.updates) != 0 {
			t.Fatalf("canceled continue changed backend state: %+v", state)
		}
	})
}

func TestDebugThreadsService_Validation(t *testing.T) {
	if _, err := NewDebugThreadsService(nil).ListThreads(context.Background(), ""); err == nil {
		t.Fatal("nil backend should fail")
	}
	backend := newFakeDebugThreadsBackend()
	svc := NewDebugThreadsService(backend)
	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 0, 0, 0); err == nil {
		t.Fatal("invalid thread should fail")
	}
	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, -1, 0); err == nil {
		t.Fatal("negative start frame should fail")
	}
	if _, err := svc.GetThreadStackTrace(context.Background(), "default", 1, 0, -1); err == nil {
		t.Fatal("negative levels should fail")
	}
	if _, err := svc.ListThreads(nil, "default"); err == nil { //nolint:staticcheck // SA1012: intentionally nil, asserts fail-closed
		t.Fatal("nil context should fail")
	}
	if err := svc.StepThread(context.Background(), "default", 1, "sideways"); err == nil {
		t.Fatal("invalid step should fail")
	}
	if err := svc.ApplyInitializeCapabilities("default", json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid capabilities should fail")
	}
	if err := svc.HandleDAPEvent("default", "stopped", json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid event should fail")
	}
	if err := svc.HandleDAPEvent("default", "module", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown event should fail")
	}
	if err := svc.HandleDAPEventForRun("default", DebugThreadsRunToken{}, "stopped", json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty token should fail")
	}
}

func TestNormalizeStepCommand(t *testing.T) {
	for input, want := range map[string]string{
		"next": "next", "STEP-OVER": "next", "into": "stepIn", "step_out": "stepOut",
	} {
		got, err := normalizeStepCommand(input)
		if err != nil || got != want {
			t.Fatalf("normalizeStepCommand(%q)=%q, %v", input, got, err)
		}
	}
}

func assertFakeSingleThreadArgs(t *testing.T, request fakeDebugThreadsRequest, command string, threadID int) {
	t.Helper()
	if request.command != command || request.args["threadId"] != threadID || request.args["singleThread"] != true {
		t.Fatalf("request = %+v", request)
	}
}

func assertCachedThreadState(
	t *testing.T,
	svc *DebugThreadsService,
	sessionID string,
	threadID int,
	state string,
	selected bool,
) {
	t.Helper()
	svc.mu.RLock()
	thread := svc.threads[sessionID][threadID]
	var copy ThreadInfo
	if thread != nil {
		copy = cloneThreadInfo(*thread)
	}
	svc.mu.RUnlock()
	if thread == nil || copy.State != state || copy.Selected != selected {
		t.Fatalf("thread %d = %+v, want state=%q selected=%v", threadID, thread, state, selected)
	}
}

func assertStackFrameIDs(t *testing.T, frames []StackFrame, want ...int) {
	t.Helper()
	if len(frames) != len(want) {
		t.Fatalf("frame count = %d, want %d: %+v", len(frames), len(want), frames)
	}
	for index, frame := range frames {
		if frame.ID != want[index] {
			t.Fatalf("frame ids at %d = %d, want %d: %+v", index, frame.ID, want[index], frames)
		}
	}
}

func cloneAnyMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneSessionUpdate(update DebugThreadsSessionUpdate) DebugThreadsSessionUpdate {
	update.Stack = append([]DebugStackFrame(nil), update.Stack...)
	return update
}
