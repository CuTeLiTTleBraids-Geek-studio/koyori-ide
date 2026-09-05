package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func testAIStreamCallerContext() context.Context {
	ctx := withAgentCallerContext(context.Background(), "wails-window:test:main")
	window := application.NewWindow(application.WebviewWindowOptions{Name: "stream-test"})
	return context.WithValue(ctx, application.WindowKey, window)
}

func TestAIServiceAgentStreamRejectsCrossWindowBeforeSideEffects(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent,
		ai,
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	ownerCtx := withAgentCallerContext(context.Background(), "wails-window:1:main")
	otherCtx := withAgentCallerContext(context.Background(), "wails-window:2:ai")
	sessionID, err := agent.CreateAgentSessionForCaller(ownerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	before, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("GetByID before cross-window start: %v", err)
	}

	if _, err := ai.StartAgentStream(otherCtx, sessionID, []ChatMessage{{
		Role: "user", Content: "send this under another window's session",
	}}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-window StartAgentStream = %v, want ErrNotAllowed", err)
	}
	if ai.IsStreaming() {
		t.Fatal("cross-window StartAgentStream claimed the global stream slot")
	}
	if records := permission.usageRecordsSnapshot(); len(records) != 0 {
		t.Fatalf("cross-window StartAgentStream wrote usage: %+v", records)
	}
	after, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("GetByID after cross-window start: %v", err)
	}
	if after.Status != before.Status || after.Attempt != before.Attempt ||
		len(after.Stream) != len(before.Stream) || len(after.Checkpoints) != len(before.Checkpoints) {
		t.Fatalf("cross-window start changed lifecycle: before=%+v after=%+v", before, after)
	}
	if _, err := ai.StartAgentStream(context.Background(), sessionID, nil); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("contextless StartAgentStream = %v, want ErrNotAllowed", err)
	}
}

func TestAIServiceStopStreamRejectsCrossWindowWithoutCancellingOwner(t *testing.T) {
	ai := NewAIService()
	ownerCtx := withAgentCallerContext(context.Background(), "wails-window:1:main")
	otherCtx := withAgentCallerContext(context.Background(), "wails-window:2:ai")
	var cancelled atomic.Bool
	ai.cancel = &streamCancel{
		fn:    func() { cancelled.Store(true) },
		owner: agentSessionOwner{identity: "wails-window:1:main"},
	}
	ai.activeStreamID = "owner-stream"

	if err := ai.StopStream(otherCtx); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-window StopStream = %v, want ErrNotAllowed", err)
	}
	if cancelled.Load() {
		t.Fatal("cross-window StopStream invoked the owner's cancel function")
	}
	if !ai.IsStreaming() {
		t.Fatal("cross-window StopStream cleared the owner's stream slot")
	}
	if err := ai.StopStream(context.Background()); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("contextless StopStream = %v, want ErrNotAllowed", err)
	}
	if cancelled.Load() {
		t.Fatal("contextless StopStream invoked the owner's cancel function")
	}
	if err := ai.StopStream(ownerCtx); err != nil {
		t.Fatalf("owner StopStream: %v", err)
	}
	if !cancelled.Load() {
		t.Fatal("owner StopStream did not invoke cancel")
	}
}

func TestAIServiceStartStreamRequiresRendererCaller(t *testing.T) {
	ai := NewAIService()
	if _, err := ai.StartStream(context.Background(), nil); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("contextless StartStream = %v, want ErrNotAllowed", err)
	}
	if ai.IsStreaming() {
		t.Fatal("contextless StartStream claimed the global stream slot")
	}
}

func TestAIServiceStartStreamAcceptsWailsWindowCallerIdentity(t *testing.T) {
	window := application.NewWindow(application.WebviewWindowOptions{Name: "owner-test"})
	ctx := context.WithValue(context.Background(), application.WindowKey, window)
	wantIdentity := fmt.Sprintf("wails-window:%d:%s", window.ID(), window.Name())
	owner, ok := agentOwnerForContext(ctx)
	if !ok || owner.trusted || owner.identity != wantIdentity {
		t.Fatalf("Wails caller owner = %+v, ok=%t, want identity %q", owner, ok, wantIdentity)
	}
	if _, err := NewAIService().StartStream(ctx, nil); err == nil || errors.Is(err, ErrNotAllowed) {
		t.Fatalf("StartStream with Wails caller = %v, want provider preflight error rather than ErrNotAllowed", err)
	}
}

func TestAIServiceConcurrentCrossWindowStopNeverCancelsOwner(t *testing.T) {
	ai := NewAIService()
	ownerCtx := withAgentCallerContext(context.Background(), "wails-window:1:main")
	otherCtx := withAgentCallerContext(context.Background(), "wails-window:2:ai")
	var cancelled atomic.Int32
	ai.cancel = &streamCancel{
		fn:    func() { cancelled.Add(1) },
		owner: agentSessionOwner{identity: "wails-window:1:main"},
	}
	ai.activeStreamID = "owner-stream"

	const attempts = 32
	var wg sync.WaitGroup
	errs := make(chan error, attempts)
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ai.StopStream(otherCtx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("cross-window StopStream = %v, want ErrNotAllowed", err)
		}
	}
	if got := cancelled.Load(); got != 0 {
		t.Fatalf("cross-window StopStream invoked owner cancel %d times", got)
	}
	if err := ai.StopStream(ownerCtx); err != nil {
		t.Fatalf("owner StopStream: %v", err)
	}
	if got := cancelled.Load(); got != 1 {
		t.Fatalf("owner StopStream cancel count = %d, want 1", got)
	}
}

func TestAIServiceAgentStreamCloseRaceFailsBeforeProviderSideEffects(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent,
		ai,
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	ownerCtx := withAgentCallerContext(context.Background(), "wails-window:close-race:main")
	sessionID, err := agent.CreateAgentSessionForCaller(ownerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}

	closeAtOwnerDelete := make(chan struct{})
	releaseClose := make(chan struct{})
	var releaseCloseOnce sync.Once
	releaseCloseBarrier := func() { releaseCloseOnce.Do(func() { close(releaseClose) }) }
	t.Cleanup(releaseCloseBarrier)
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.sessionOwnerMutationHook = func(stage string) {
		if stage == "before-owner-delete" {
			close(closeAtOwnerDelete)
			<-releaseClose
		}
	}
	deps.mu.Unlock()
	t.Cleanup(func() {
		deps.mu.Lock()
		deps.sessionOwnerMutationHook = nil
		deps.mu.Unlock()
	})

	closeDone := make(chan error, 1)
	go func() { closeDone <- agent.CloseAgentSessionForCaller(ownerCtx, sessionID) }()
	select {
	case <-closeAtOwnerDelete:
	case <-time.After(5 * time.Second):
		t.Fatal("session close did not reach owner deletion barrier")
	}

	startDone := make(chan error, 1)
	go func() {
		_, startErr := ai.StartAgentStream(ownerCtx, sessionID, []ChatMessage{{Role: "user", Content: "must not send"}})
		startDone <- startErr
	}()
	var startErr error
	select {
	case startErr = <-startDone:
	case <-time.After(100 * time.Millisecond):
	}
	releaseCloseBarrier()
	if err := <-closeDone; err != nil {
		t.Fatalf("CloseAgentSessionForCaller: %v", err)
	}
	if startErr == nil {
		startErr = <-startDone
	}
	if startErr == nil {
		t.Fatal("StartAgentStream succeeded after the session was closed")
	}
	if ai.IsStreaming() {
		t.Fatal("failed close-race StartAgentStream retained the global stream slot")
	}
	if records := permission.usageRecordsSnapshot(); len(records) != 0 {
		t.Fatalf("failed close-race StartAgentStream wrote usage: %+v", records)
	}
	closed, err := lifecycle.GetByID(sessionID)
	if err == nil && closed.Status == agentcore.SessionRunning {
		t.Fatalf("closed session remained running in lifecycle storage: %+v", closed)
	}
}

func TestAIStreamFinishPropagatesAppendFailure(t *testing.T) {
	agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
	sessionID := createFaultInjectedChat(t, agent)
	snap := aiSnapshot{
		config:    AIConfig{Model: "test-model"},
		lifecycle: lifecycle,
	}
	unit, err := beginPersistentChatLifecycle(snap, sessionID, []ChatMessage{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("beginPersistentChatLifecycle: %v", err)
	}
	fault := errors.New("stream append persistence failure")
	persistence.failNext(agentcore.PersistenceNotPublished, fault)
	finishErr := unit.finish(&ChatResponse{Content: "provider output"}, nil)
	if !errors.Is(finishErr, fault) {
		t.Fatalf("finish error = %v, want append fault", finishErr)
	}
	session, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("GetByID after append fault: %v", err)
	}
	if session.Status == agentcore.SessionCompleted {
		t.Fatalf("append fault incorrectly completed session: %+v", session)
	}
}
