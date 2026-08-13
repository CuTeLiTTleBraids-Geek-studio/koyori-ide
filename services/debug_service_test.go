package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// isPeerClosedWriteError reports whether a socket write failed because the
// peer closed the connection. For a mock DAP server this is the normal
// teardown when the debug service stops, not a server failure: on Linux the
// write yields EPIPE (32) / ECONNRESET (104), and on Windows the wsasend
// error yields WSAECONNABORTED (10053) / WSAECONNRESET (10054). The numeric
// comparisons keep this helper portable across platforms without importing
// platform-only syscall constants.
func isPeerClosedWriteError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) {
		if errno, ok := syscallErr.Err.(syscall.Errno); ok {
			switch uintptr(errno) {
			case 32, 104, 10053, 10054:
				return true
			}
		}
	}
	return false
}

// normalizeMockDAPServerError maps a peer-close write error to a graceful
// nil signal so a mock DAP server that raced the client's stop is not
// reported as a test failure. Genuine protocol errors are preserved.
func normalizeMockDAPServerError(err error) error {
	if isPeerClosedWriteError(err) {
		return nil
	}
	return err
}

func TestHandleDAPOutputEventDoesNotLogProgramOutput(t *testing.T) {
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previousLogger)

	const secret = "token=super-secret-value"
	(&DebugService{}).handleDAPEvent(nil, 0, dapMessage{
		Type:  "event",
		Event: "output",
		Body:  json.RawMessage(`{"output":"` + secret + `\n"}`),
	})

	logged := logs.String()
	if strings.Contains(logged, secret) {
		t.Fatalf("DAP output content leaked to backend logs: %s", logged)
	}
	if !strings.Contains(logged, `"bytes":`) {
		t.Fatalf("DAP output metadata was not logged: %s", logged)
	}
}

type readStartedConn struct {
	net.Conn
	once    sync.Once
	started chan struct{}
}

func (c *readStartedConn) Read(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Read(p)
}

type concurrentReadStartedConn struct {
	net.Conn
	started chan<- struct{}
}

func (c *concurrentReadStartedConn) Read(p []byte) (int, error) {
	c.started <- struct{}{}
	return c.Conn.Read(p)
}

type blockingNodeEvaluator struct {
	once    sync.Once
	started chan struct{}
	release <-chan struct{}
	result  DebugVariable
	err     error
}

func (e *blockingNodeEvaluator) Evaluate(string) (DebugVariable, error) {
	e.once.Do(func() { close(e.started) })
	<-e.release
	return e.result, e.err
}

func TestDebugService_ListSessionsReleasesRegistryBeforeSessionSnapshot(t *testing.T) {
	d := NewDebugService()
	session := d.sessions["default"]
	session.mu.Lock()

	started := make(chan struct{})
	done := make(chan []DebugSessionListItem, 1)
	go func() {
		close(started)
		done <- d.ListSessions()
	}()
	<-started
	for i := 0; i < 32; i++ {
		runtime.Gosched()
	}

	registryAvailable := make(chan struct{})
	go func() {
		d.sessionsMu.Lock()
		close(registryAvailable)
		d.sessionsMu.Unlock()
	}()
	select {
	case <-registryAvailable:
	case <-time.After(time.Second):
		session.mu.Unlock()
		t.Fatal("ListSessions held sessionsMu while waiting for a session lock")
	}
	session.mu.Unlock()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ListSessions did not complete after session unlock")
	}
}

const debugProcessExitHelperEnv = "KOYORI_IDE_DEBUG_PROCESS_EXIT_HELPER"

func TestDebugService_ProcessExitHelper(t *testing.T) {
	if os.Getenv(debugProcessExitHelperEnv) != "1" {
		return
	}
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
	os.Exit(0)
}

func TestDebugService_ListSessionsDoesNotRaceWithProcessExit(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestDebugService_ProcessExitHelper$")
	cmd.Env = append(os.Environ(), debugProcessExitHelperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("create helper stdin: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}

	d := NewDebugService()
	session := d.sessions["default"]
	session.mu.Lock()
	session.cmd = cmd
	session.mu.Unlock()

	const readers = 16
	stop := make(chan struct{})
	ready := make(chan struct{}, readers)
	var wg sync.WaitGroup
	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			d.ListSessions()
			ready <- struct{}{}
			for {
				select {
				case <-stop:
					return
				default:
					d.ListSessions()
				}
			}
		}()
	}
	for i := 0; i < readers; i++ {
		<-ready
	}

	if err := stdin.Close(); err != nil {
		t.Fatalf("release helper process: %v", err)
	}
	waitErr := cmd.Wait()
	close(stop)
	wg.Wait()
	if waitErr != nil {
		t.Fatalf("wait for helper process: %v", waitErr)
	}
}

func TestDebugService_ProcessExitCleansOwningSessionAfterActiveSwitch(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestDebugService_ProcessExitHelper$")
	cmd.Env = append(os.Environ(), debugProcessExitHelperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("create helper stdin: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}

	d := NewDebugService()
	owner := d.DebugSession
	other := newDebugSession()
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.cmd = cmd
	owner.running = true
	owner.addr = "owner-address"
	owner.mode = "node"
	owner.mu.Unlock()
	other.mu.Lock()
	other.running = true
	other.addr = "other-address"
	other.mode = "attach"
	other.stopped = true
	other.mu.Unlock()
	d.sessionsMu.Lock()
	d.sessions["other"] = other
	d.sessionsMu.Unlock()
	if err := d.SetActiveSession("other"); err != nil {
		t.Fatalf("switch active session: %v", err)
	}

	waitDone := make(chan struct{})
	go func() {
		owner.waitForProcessExit(cmd, generation)
		close(waitDone)
	}()
	if err := stdin.Close(); err != nil {
		t.Fatalf("release helper process: %v", err)
	}
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("owning session did not observe child exit")
	}

	items := make(map[string]DebugSessionListItem)
	for _, item := range d.ListSessions() {
		items[item.ID] = item
	}
	if items["default"].Running || items["default"].Address != "" || items["default"].Mode != "" {
		t.Fatalf("owner session was not cleaned after exit: %+v", items["default"])
	}
	if !items["other"].Running || items["other"].Address != "other-address" || items["other"].Mode != "attach" || !items["other"].Stopped {
		t.Fatalf("active non-owner session changed after owner exit: %+v", items["other"])
	}
}

func TestDebugService_ListSessionsConcurrentMutation(t *testing.T) {
	d := NewDebugService()
	const iterations = 1000
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			for _, item := range d.ListSessions() {
				if item.ID == "" {
					t.Error("ListSessions returned an empty ID")
					return
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			id := fmt.Sprintf("session-%d", i%8)
			d.sessionsMu.Lock()
			if i%3 == 0 {
				delete(d.sessions, id)
			} else {
				session := d.sessions[id]
				if session == nil {
					session = newDebugSession()
					d.sessions[id] = session
				}
				session.mu.Lock()
				session.mode = fmt.Sprintf("mode-%d", i)
				session.addr = fmt.Sprintf("addr-%d", i)
				session.stopped = i%2 == 0
				session.mu.Unlock()
			}
			d.sessionsMu.Unlock()
		}
	}()
	close(start)
	wg.Wait()
}

func TestDebugService_StopUnblocksReadLoop(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	started := make(chan struct{})
	conn := &readStartedConn{Conn: client, started: started}
	d := NewDebugService()
	d.mu.Lock()
	d.conn = conn
	d.readerDone = make(chan struct{})
	d.readerDoneOnce = new(sync.Once)
	done := d.readerDone
	doneOnce := d.readerDoneOnce
	d.mu.Unlock()

	go d.readLoop(d.DebugSession, 0, conn, done, doneOnce)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not start reading")
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not finish after Stop closed the connection")
	}
}

func TestDebugService_StopBeforeReadLoopStartsClosesCompletion(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	d := NewDebugService()
	d.mu.Lock()
	d.conn = client
	d.readerDone = make(chan struct{})
	d.readerDoneOnce = new(sync.Once)
	done := d.readerDone
	doneOnce := d.readerDoneOnce
	d.mu.Unlock()

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	go d.readLoop(d.DebugSession, 0, client, done, doneOnce)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("captured reader completion did not close after Stop ran before reader start")
	}
}

func TestDebugService_ConcurrentReadLoopsCloseCompletionOnce(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	started := make(chan struct{}, 16)
	conn := &concurrentReadStartedConn{Conn: client, started: started}
	d := NewDebugService()
	d.mu.Lock()
	d.conn = conn
	d.readerDone = make(chan struct{})
	d.readerDoneOnce = new(sync.Once)
	done := d.readerDone
	doneOnce := d.readerDoneOnce
	d.mu.Unlock()

	const readers = 16
	var wg sync.WaitGroup
	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			d.readLoop(d.DebugSession, 0, conn, done, doneOnce)
		}()
	}
	for i := 0; i < readers; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("not all read loops started reading")
		}
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close server connection: %v", err)
	}

	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("concurrent read loops did not finish")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader completion was not closed")
	}
}

func TestDebugService_StaleReadLoopUsesCapturedGeneration(t *testing.T) {
	clientA, serverA := net.Pipe()
	defer clientA.Close()
	defer serverA.Close()
	clientB, serverB := net.Pipe()
	defer clientB.Close()
	defer serverB.Close()

	startedA := make(chan struct{})
	startedB := make(chan struct{})
	connA := &readStartedConn{Conn: clientA, started: startedA}
	connB := &readStartedConn{Conn: clientB, started: startedB}
	d := NewDebugService()

	d.mu.Lock()
	d.conn = connA
	d.readerDone = make(chan struct{})
	d.readerDoneOnce = new(sync.Once)
	doneA := d.readerDone
	doneOnceA := d.readerDoneOnce
	d.mu.Unlock()

	d.mu.Lock()
	d.conn = connB
	d.readerDone = make(chan struct{})
	d.readerDoneOnce = new(sync.Once)
	doneB := d.readerDone
	doneOnceB := d.readerDoneOnce
	d.mu.Unlock()

	owner := d.DebugSession
	go d.readLoop(owner, 0, connA, doneA, doneOnceA)
	select {
	case <-startedA:
	case <-time.After(time.Second):
		t.Fatal("stale run A did not read its captured connection")
	}
	select {
	case <-startedB:
		t.Fatal("stale run A read run B's connection")
	default:
	}

	go d.readLoop(owner, 0, connB, doneB, doneOnceB)
	select {
	case <-startedB:
	case <-time.After(time.Second):
		t.Fatal("run B did not read its captured connection")
	}

	if err := serverA.Close(); err != nil {
		t.Fatalf("close run A server connection: %v", err)
	}
	select {
	case <-doneA:
	case <-time.After(time.Second):
		t.Fatal("run A completion did not close")
	}
	select {
	case <-doneB:
		t.Fatal("run A closed run B completion")
	default:
	}

	if err := serverB.Close(); err != nil {
		t.Fatalf("close run B server connection: %v", err)
	}
	select {
	case <-doneB:
	case <-time.After(time.Second):
		t.Fatal("run B completion did not close")
	}
}

func TestDebugService_StaleDAPMessagesStayWithOwningSession(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	d := NewDebugService()
	owner := d.DebugSession
	other := newDebugSession()
	ownerResponse := make(chan dapMessage, 1)
	otherResponse := make(chan dapMessage, 1)
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.conn = client
	owner.pending[7] = ownerResponse
	owner.stopped = true
	owner.stopReason = "owner-paused"
	owner.readerDone = make(chan struct{})
	owner.readerDoneOnce = new(sync.Once)
	done := owner.readerDone
	doneOnce := owner.readerDoneOnce
	owner.mu.Unlock()
	other.mu.Lock()
	other.pending[7] = otherResponse
	other.stopped = true
	other.stopReason = "other-paused"
	other.mu.Unlock()
	d.sessionsMu.Lock()
	d.sessions["other"] = other
	d.sessionsMu.Unlock()
	if err := d.SetActiveSession("other"); err != nil {
		t.Fatalf("switch active session: %v", err)
	}

	go d.readLoop(owner, generation, client, done, doneOnce)
	response := dapMessage{Seq: 1, Type: "response", RequestSeq: 7, Success: true}
	if err := writeDAPMessage(server, response); err != nil {
		t.Fatalf("write owner response: %v", err)
	}
	select {
	case got := <-ownerResponse:
		if got.RequestSeq != 7 {
			t.Fatalf("owner response request seq = %d, want 7", got.RequestSeq)
		}
	case <-time.After(time.Second):
		t.Fatal("owner response was not dispatched to owner pending request")
	}
	select {
	case <-otherResponse:
		t.Fatal("stale owner response satisfied active session pending request")
	default:
	}

	event := dapMessage{Seq: 2, Type: "event", Event: "continued"}
	if err := writeDAPMessage(server, event); err != nil {
		t.Fatalf("write owner event: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		owner.mu.Lock()
		ownerStopped := owner.stopped
		ownerReason := owner.stopReason
		owner.mu.Unlock()
		if !ownerStopped && ownerReason == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owner event was not applied to owner session")
		}
		runtime.Gosched()
	}
	other.mu.Lock()
	otherStopped := other.stopped
	otherReason := other.stopReason
	other.mu.Unlock()
	if !otherStopped || otherReason != "other-paused" {
		t.Fatalf("stale owner event mutated active session: stopped=%v reason=%q", otherStopped, otherReason)
	}

	if err := server.Close(); err != nil {
		t.Fatalf("close owner server connection: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("owner read loop did not close completion")
	}
}

func TestDebugService_SameSessionStaleDAPMessagesIgnoredAfterRestart(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	d := NewDebugService()
	owner := d.DebugSession
	oldResponse := make(chan dapMessage, 1)
	newResponse := make(chan dapMessage, 1)
	owner.mu.Lock()
	oldGeneration := owner.beginRunLocked()
	owner.pending[7] = oldResponse
	owner.readerDone = make(chan struct{})
	owner.readerDoneOnce = new(sync.Once)
	done := owner.readerDone
	doneOnce := owner.readerDoneOnce
	owner.mu.Unlock()

	owner.mu.Lock()
	owner.beginRunLocked()
	owner.pending = map[int]chan dapMessage{7: newResponse}
	owner.stopped = true
	owner.stopReason = "new-run-paused"
	owner.mu.Unlock()

	go d.readLoop(owner, oldGeneration, client, done, doneOnce)
	if err := writeDAPMessage(server, dapMessage{Seq: 1, Type: "response", RequestSeq: 7, Success: true}); err != nil {
		t.Fatalf("write stale response: %v", err)
	}
	if err := writeDAPMessage(server, dapMessage{Seq: 2, Type: "event", Event: "continued"}); err != nil {
		t.Fatalf("write stale event: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close stale run server connection: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stale run reader did not finish")
	}
	select {
	case <-oldResponse:
		t.Fatal("stale response was delivered to old pending state after restart")
	default:
	}
	select {
	case <-newResponse:
		t.Fatal("stale response satisfied new run pending state")
	default:
	}
	owner.mu.Lock()
	stopped := owner.stopped
	reason := owner.stopReason
	owner.mu.Unlock()
	if !stopped || reason != "new-run-paused" {
		t.Fatalf("stale event mutated new run: stopped=%v reason=%q", stopped, reason)
	}
}

func TestDebugService_StaleNodePausedCallbackIgnoredAfterRestart(t *testing.T) {
	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	oldGeneration := owner.beginRunLocked()
	owner.mu.Unlock()
	callback := d.nodePausedHandler(owner, oldGeneration, nil)

	owner.mu.Lock()
	owner.beginRunLocked()
	owner.stopped = false
	owner.stopReason = "new-run"
	owner.stack = []DebugStackFrame{{ID: 22, Name: "new-frame"}}
	owner.locals = []DebugVariable{{Name: "new-local", Value: "22"}}
	owner.mu.Unlock()

	callback("stale-pause", []DebugStackFrame{{ID: 11, Name: "old-frame"}}, []DebugVariable{{Name: "old-local", Value: "11"}})
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.stopped || owner.stopReason != "new-run" {
		t.Fatalf("stale Node callback changed new run stop state: stopped=%v reason=%q", owner.stopped, owner.stopReason)
	}
	if len(owner.stack) != 1 || owner.stack[0].ID != 22 || len(owner.locals) != 1 || owner.locals[0].Name != "new-local" {
		t.Fatalf("stale Node callback changed new run data: stack=%+v locals=%+v", owner.stack, owner.locals)
	}
}

func TestDebugService_DAPResponseDeliveryCannotCrossRestart(t *testing.T) {
	d := NewDebugService()
	owner := d.DebugSession
	response := make(chan dapMessage)
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.pending[7] = response
	owner.mu.Unlock()

	handled := make(chan struct{})
	go func() {
		d.handleDAPMessage(owner, generation, dapMessage{Type: "response", RequestSeq: 7, Success: true})
		close(handled)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		owner.mu.Lock()
		_, pending := owner.pending[7]
		owner.mu.Unlock()
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("response handler did not remove the pending request")
		}
		runtime.Gosched()
	}
	owner.mu.Lock()
	owner.beginRunLocked()
	owner.mu.Unlock()

	select {
	case <-response:
		t.Fatal("response captured before restart was delivered after generation changed")
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("response handler remained blocked after atomic delivery")
	}
}

func TestDebugService_RunAwareStackRefreshDoesNotCommitAfterRestart(t *testing.T) {
	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.threadID = 1
	owner.mu.Unlock()
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	request := func(command string, _ map[string]interface{}) (json.RawMessage, error) {
		if command != "stackTrace" {
			return nil, fmt.Errorf("unexpected command %q", command)
		}
		close(requestStarted)
		<-releaseRequest
		return json.RawMessage(`{"stackFrames":[]}`), nil
	}
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- d.refreshStackAndLocalsForRun(owner, generation, request)
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("run-aware stack refresh did not reach the request barrier")
	}
	owner.mu.Lock()
	owner.beginRunLocked()
	owner.stack = []DebugStackFrame{{ID: 22, Name: "new-frame"}}
	owner.locals = []DebugVariable{{Name: "new-local", Value: "22"}}
	owner.mu.Unlock()
	close(releaseRequest)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("run-aware stack refresh failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run-aware stack refresh did not finish")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if len(owner.stack) != 1 || owner.stack[0].ID != 22 || len(owner.locals) != 1 || owner.locals[0].Name != "new-local" {
		t.Fatalf("stale stack refresh committed into new run: stack=%+v locals=%+v", owner.stack, owner.locals)
	}
}

func TestDebugService_RunAwareNodeWatchRefreshDoesNotCommitAfterRestart(t *testing.T) {
	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.watches = []string{"value"}
	owner.mu.Unlock()
	releaseEvaluate := make(chan struct{})
	evaluator := &blockingNodeEvaluator{
		started: make(chan struct{}),
		release: releaseEvaluate,
		err:     fmt.Errorf("old run evaluate failed"),
	}
	refreshDone := make(chan struct{})
	go func() {
		_, _ = d.refreshWatchesForRun(owner, generation, evaluator.Evaluate)
		close(refreshDone)
	}()
	select {
	case <-evaluator.started:
	case <-time.After(time.Second):
		t.Fatal("run-aware watch refresh did not reach the evaluate barrier")
	}
	owner.mu.Lock()
	owner.beginRunLocked()
	owner.watchValues = []DebugVariable{{Name: "new", Value: "22"}}
	owner.lastError = "new-run-error"
	owner.mu.Unlock()
	close(releaseEvaluate)
	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("run-aware watch refresh did not finish")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if len(owner.watchValues) != 1 || owner.watchValues[0].Name != "new" || owner.lastError != "new-run-error" {
		t.Fatalf("stale watch refresh committed into new run: values=%+v lastError=%q", owner.watchValues, owner.lastError)
	}
}

func TestDebugService_ConnectMockDAPNaturalDisconnectCleansCapturedRun(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for mock DAP: %v", err)
	}
	defer listener.Close()
	serverConn := make(chan net.Conn, 1)
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		serverConn <- conn
		reader := bufio.NewReader(conn)
		for {
			msg, err := readDAPMessage(reader)
			if err != nil {
				serverDone <- nil
				return
			}
			body := json.RawMessage(`{}`)
			if msg.Command == "stackTrace" {
				body = json.RawMessage(`{"stackFrames":[]}`)
			}
			if err := writeDAPMessage(conn, dapMessage{
				Seq:        msg.Seq + 1000,
				Type:       "response",
				Command:    msg.Command,
				RequestSeq: msg.Seq,
				Success:    true,
				Body:       body,
			}); err != nil {
				serverDone <- normalizeMockDAPServerError(err)
				return
			}
			if msg.Command == "configurationDone" {
				stoppedBody := json.RawMessage(`{"reason":"entry","threadId":1}`)
				if err := writeDAPMessage(conn, dapMessage{Seq: 2000, Type: "event", Event: "stopped", Body: stoppedBody}); err != nil {
					serverDone <- normalizeMockDAPServerError(err)
					return
				}
			}
		}
	}()

	d := NewDebugService()
	owner := d.DebugSession
	info, err := d.ConnectMockDAP(listener.Addr().String(), map[string]interface{}{"request": "launch", "program": "."})
	if err != nil {
		t.Fatalf("ConnectMockDAP failed: %v", err)
	}
	if !info.Running {
		t.Fatalf("attach session did not become running: %+v", info)
	}
	owner.mu.Lock()
	done := owner.readerDone
	generation := owner.runGeneration
	owner.pending[999] = make(chan dapMessage, 1)
	owner.mu.Unlock()

	conn := <-serverConn
	if err := conn.Close(); err != nil {
		t.Fatalf("close mock DAP peer: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("DAP reader did not finish after peer disconnect")
	}

	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.runGeneration == generation {
		t.Fatal("natural disconnect did not invalidate the captured generation")
	}
	if owner.running || owner.conn != nil || len(owner.pending) != 0 || owner.addr != "" || owner.mode != "" {
		t.Fatalf("natural disconnect left captured run active: running=%v conn=%v pending=%d addr=%q mode=%q", owner.running, owner.conn, len(owner.pending), owner.addr, owner.mode)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("mock DAP server failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("mock DAP server did not observe disconnect")
	}
}

func TestDebugService_ConnectMockDAPInitializationStaysWithCapturedOwnerAfterActiveSwitch(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for mock DAP: %v", err)
	}
	defer listener.Close()
	initializeSeen := make(chan struct{})
	releaseInitialize := make(chan struct{})
	commands := make(chan string, 16)
	responsesWritten := make(chan string, 16)
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for {
			msg, err := readDAPMessage(reader)
			if err != nil {
				serverDone <- nil
				return
			}
			commands <- msg.Command
			if msg.Command == "initialize" {
				close(initializeSeen)
				<-releaseInitialize
			}
			body := json.RawMessage(`{}`)
			switch msg.Command {
			case "setBreakpoints":
				body = json.RawMessage(`{"breakpoints":[{"id":7,"line":10,"verified":true}]}`)
			case "stackTrace":
				body = json.RawMessage(`{"stackFrames":[]}`)
			}
			if err := writeDAPMessage(conn, dapMessage{
				Seq:        msg.Seq + 1000,
				Type:       "response",
				Command:    msg.Command,
				RequestSeq: msg.Seq,
				Success:    true,
				Body:       body,
			}); err != nil {
				serverDone <- normalizeMockDAPServerError(err)
				return
			}
			if msg.Command == "configurationDone" {
				stoppedBody := json.RawMessage(`{"reason":"entry","threadId":1}`)
				if err := writeDAPMessage(conn, dapMessage{Seq: 2000, Type: "event", Event: "stopped", Body: stoppedBody}); err != nil {
					serverDone <- normalizeMockDAPServerError(err)
					return
				}
			}
			responsesWritten <- msg.Command
		}
	}()

	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	owner.breakpoints = []DebugBreakpoint{{File: "main.go", Line: 10}}
	owner.mu.Unlock()
	other := newDebugSession()
	other.mu.Lock()
	otherGeneration := other.beginRunLocked()
	other.running = true
	other.addr = "new-run-address"
	other.mode = "attach"
	other.stopped = true
	other.stopReason = "new-run-paused"
	other.mu.Unlock()
	d.sessionsMu.Lock()
	d.sessions["other"] = other
	d.sessionsMu.Unlock()

	result := make(chan struct {
		info DebugSessionInfo
		err  error
	}, 1)
	go func() {
		info, err := d.ConnectMockDAP(listener.Addr().String(), map[string]interface{}{"request": "launch", "program": "."})
		result <- struct {
			info DebugSessionInfo
			err  error
		}{info: info, err: err}
	}()
	select {
	case <-initializeSeen:
	case <-time.After(time.Second):
		t.Fatal("captured owner did not send initialize")
	}
	if err := d.SetActiveSession("other"); err != nil {
		t.Fatalf("switch active session: %v", err)
	}
	close(releaseInitialize)

	var got struct {
		info DebugSessionInfo
		err  error
	}
	select {
	case got = <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("ConnectMockDAP did not finish after initialize response")
	}
	if got.err != nil {
		t.Fatalf("captured initialization failed after active switch: %v", got.err)
	}
	if !got.info.Running || got.info.Address != listener.Addr().String() {
		t.Fatalf("ConnectMockDAP returned active-session state instead of captured owner: %+v", got.info)
	}

	want := []string{"initialize", "launch", "setBreakpoints", "configurationDone"}
	for i, command := range want {
		select {
		case gotCommand := <-commands:
			if gotCommand != command {
				t.Fatalf("captured initialization command %d = %q, want %q", i, gotCommand, command)
			}
		case <-time.After(time.Second):
			t.Fatalf("captured owner did not receive %q", command)
		}
	}
	// The stopped event starts a background stack refresh while ConnectMockDAP
	// performs its own synchronous refresh. Wait for both response writes to
	// return before closing the client side of the TCP connection; otherwise a
	// valid Windows teardown can surface as WSAECONNABORTED in the mock server.
	stackResponses := 0
	for stackResponses < 2 {
		select {
		case command := <-responsesWritten:
			if command == "stackTrace" {
				stackResponses++
			}
		case err := <-serverDone:
			if err != nil {
				t.Fatalf("mock DAP server failed before stack responses completed: %v", err)
			}
			t.Fatalf("mock DAP server stopped after %d stackTrace response writes, want 2", stackResponses)
		case <-time.After(time.Second):
			t.Fatalf("mock DAP server completed %d stackTrace response writes, want 2", stackResponses)
		}
	}
	other.mu.Lock()
	if other.runGeneration != otherGeneration || !other.running || other.addr != "new-run-address" || other.mode != "attach" || !other.stopped || other.stopReason != "new-run-paused" {
		t.Fatalf("captured initialization changed new active run: generation=%d running=%v addr=%q mode=%q stopped=%v reason=%q", other.runGeneration, other.running, other.addr, other.mode, other.stopped, other.stopReason)
	}
	other.mu.Unlock()

	owner.stop()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("mock DAP server failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("mock DAP server did not stop")
	}
}

func TestDebugService_ConnectMockDAPMarksAttachRunning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for mock DAP: %v", err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for {
			msg, err := readDAPMessage(reader)
			if err != nil {
				serverDone <- nil
				return
			}
			body := json.RawMessage(`{}`)
			if msg.Command == "stackTrace" {
				body = json.RawMessage(`{"stackFrames":[]}`)
			}
			response := dapMessage{
				Seq:        msg.Seq + 1000,
				Type:       "response",
				Command:    msg.Command,
				RequestSeq: msg.Seq,
				Success:    true,
				Body:       body,
			}
			if err := writeDAPMessage(conn, response); err != nil {
				serverDone <- normalizeMockDAPServerError(err)
				return
			}
			if msg.Command == "configurationDone" {
				stoppedBody := json.RawMessage(`{"reason":"entry","threadId":1}`)
				if err := writeDAPMessage(conn, dapMessage{Seq: 2000, Type: "event", Event: "stopped", Body: stoppedBody}); err != nil {
					serverDone <- normalizeMockDAPServerError(err)
					return
				}
			}
		}
	}()

	d := NewDebugService()
	info, err := d.ConnectMockDAP(listener.Addr().String(), map[string]interface{}{"request": "launch", "program": "."})
	if err != nil {
		t.Fatalf("ConnectMockDAP failed: %v", err)
	}
	if !info.Running || !d.GetSession().Running {
		t.Fatalf("attach session did not report running: launch=%+v current=%+v", info, d.GetSession())
	}
	items := d.ListSessions()
	if len(items) != 1 || !items[0].Running {
		t.Fatalf("ListSessions did not report attach running: %+v", items)
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("mock DAP server failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("mock DAP server did not stop")
	}
}

func TestWriteAndReadDAPMessage(t *testing.T) {
	var buf bytes.Buffer
	payload := map[string]interface{}{
		"seq": 1, "type": "request", "command": "initialize",
		"arguments": map[string]interface{}{"clientID": "koyori-ide"},
	}
	if err := writeDAPMessage(&buf, payload); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	if !strings.Contains(raw, "Content-Length:") {
		t.Fatalf("missing header: %q", raw)
	}
	r := bufio.NewReader(&buf)
	msg, err := readDAPMessage(r)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != "request" || msg.Command != "initialize" || msg.Seq != 1 {
		t.Fatalf("parsed %+v", msg)
	}
}

func TestDAPMessage_EventJSON(t *testing.T) {
	body := json.RawMessage(`{"reason":"breakpoint","threadId":1}`)
	raw, _ := json.Marshal(dapMessage{
		Seq: 2, Type: "event", Event: "stopped", Body: body,
	})
	var msg dapMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Event != "stopped" {
		t.Fatalf("event=%q", msg.Event)
	}
}

func TestDebugService_ToggleBreakpointOffline(t *testing.T) {
	d := NewDebugService()
	bps, err := d.ToggleBreakpoint("/tmp/main.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(bps) != 1 || bps[0].Line != 10 {
		t.Fatalf("got %+v", bps)
	}
	bps, err = d.ToggleBreakpoint("/tmp/main.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(bps) != 0 {
		t.Fatalf("expected empty after toggle off, got %+v", bps)
	}
}

func TestDebugService_StatusMessage_NoDlv(t *testing.T) {
	d := NewDebugService()
	msg := d.StatusMessage()
	if msg == "" {
		t.Fatal("expected status message")
	}
}

func TestDebugService_GetState_Empty(t *testing.T) {
	d := NewDebugService()
	st := d.GetState()
	if st.Session.Running {
		t.Fatal("expected not running")
	}
}

func TestDebugService_B7B9DAPRequestPayloads(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	d := NewDebugService()
	owner := d.DebugSession
	done := make(chan struct{})
	doneOnce := &sync.Once{}
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.conn = client
	owner.running = true
	owner.mode = "package"
	owner.stopped = true
	owner.stack = []DebugStackFrame{{ID: 77}}
	owner.readerDone = done
	owner.readerDoneOnce = doneOnce
	owner.mu.Unlock()
	go d.readLoop(owner, generation, client, done, doneOnce)

	requests := make(chan dapMessage, 4)
	serverDone := make(chan error, 1)
	releaseServer := make(chan struct{})
	var releaseServerOnce sync.Once
	release := func() {
		releaseServerOnce.Do(func() { close(releaseServer) })
	}
	defer release()
	go func() {
		defer server.Close()
		reader := bufio.NewReader(server)
		for i := 0; i < 4; i++ {
			msg, err := readDAPMessage(reader)
			if err != nil {
				serverDone <- err
				return
			}
			requests <- msg
			body := json.RawMessage(`{}`)
			switch msg.Command {
			case "setBreakpoints":
				body = json.RawMessage(`{"breakpoints":[{"id":1,"line":11,"verified":true}]}`)
			case "evaluate":
				var args struct {
					Context string `json:"context"`
				}
				if err := json.Unmarshal(msg.Arguments, &args); err != nil {
					serverDone <- err
					return
				}
				encoded, err := json.Marshal(map[string]string{"result": args.Context + "-result", "type": "string"})
				if err != nil {
					serverDone <- err
					return
				}
				body = encoded
			}
			if err := writeDAPMessage(server, dapMessage{
				Seq:        i + 100,
				Type:       "response",
				Command:    msg.Command,
				RequestSeq: msg.Seq,
				Success:    true,
				Body:       body,
			}); err != nil {
				serverDone <- normalizeMockDAPServerError(err)
				return
			}
		}
		<-releaseServer
		serverDone <- nil
	}()

	filePath := filepath.Join(t.TempDir(), "main.go")
	if err := d.SetConditionalBreakpoint(filePath, 11, "count > 2"); err != nil {
		t.Fatalf("SetConditionalBreakpoint: %v", err)
	}
	if err := d.SetLogpoint(filePath, 11, "count={count}"); err != nil {
		t.Fatalf("SetLogpoint: %v", err)
	}
	watch, err := d.EvaluateWatch(" count ")
	if err != nil {
		t.Fatalf("EvaluateWatch: %v", err)
	}
	if watch != "watch-result" {
		t.Fatalf("watch result = %q, want watch-result", watch)
	}
	repl, err := d.EvaluateREPL("print(count)")
	if err != nil {
		t.Fatalf("EvaluateREPL: %v", err)
	}
	if repl != "repl-result" {
		t.Fatalf("repl result = %q, want repl-result", repl)
	}

	got := make([]dapMessage, 0, 4)
	for i := 0; i < 4; i++ {
		select {
		case msg := <-requests:
			got = append(got, msg)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for captured DAP request")
		}
	}
	assertSourceBreakpoint := func(msg dapMessage, wantField, wantValue, absentField string) {
		t.Helper()
		if msg.Command != "setBreakpoints" {
			t.Fatalf("command = %q, want setBreakpoints", msg.Command)
		}
		var args struct {
			Source struct {
				Path string `json:"path"`
			} `json:"source"`
			Breakpoints []map[string]json.RawMessage `json:"breakpoints"`
		}
		if err := json.Unmarshal(msg.Arguments, &args); err != nil {
			t.Fatalf("decode setBreakpoints arguments: %v", err)
		}
		if filepath.Clean(args.Source.Path) != filepath.Clean(filePath) {
			t.Fatalf("source path = %q, want %q", args.Source.Path, filePath)
		}
		if len(args.Breakpoints) != 1 {
			t.Fatalf("breakpoints = %d, want 1", len(args.Breakpoints))
		}
		var line int
		if err := json.Unmarshal(args.Breakpoints[0]["line"], &line); err != nil || line != 11 {
			t.Fatalf("breakpoint line = %d, err = %v", line, err)
		}
		var value string
		if err := json.Unmarshal(args.Breakpoints[0][wantField], &value); err != nil {
			t.Fatalf("decode %s: %v", wantField, err)
		}
		if value != wantValue {
			t.Fatalf("%s = %q, want %q", wantField, value, wantValue)
		}
		if _, ok := args.Breakpoints[0][absentField]; ok {
			t.Fatalf("unexpected %s in breakpoint payload", absentField)
		}
	}
	assertSourceBreakpoint(got[0], "condition", "count > 2", "logMessage")
	assertSourceBreakpoint(got[1], "logMessage", "count={count}", "condition")

	for i, want := range []struct {
		context    string
		expression string
	}{{"watch", "count"}, {"repl", "print(count)"}} {
		msg := got[i+2]
		if msg.Command != "evaluate" {
			t.Fatalf("command = %q, want evaluate", msg.Command)
		}
		var args struct {
			Expression string `json:"expression"`
			FrameID    int    `json:"frameId"`
			Context    string `json:"context"`
		}
		if err := json.Unmarshal(msg.Arguments, &args); err != nil {
			t.Fatalf("decode evaluate arguments: %v", err)
		}
		if args.Context != want.context || args.Expression != want.expression || args.FrameID != 77 {
			t.Fatalf("evaluate arguments = %+v, want expression %q, context %q and frame 77", args, want.expression, want.context)
		}
	}

	release()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("mock DAP server: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("mock DAP server did not finish")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("DAP reader did not stop")
	}
}

func TestDebugService_B8B9EvaluateWithoutStackOmitsFrameID(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	d := NewDebugService()
	owner := d.DebugSession
	done := make(chan struct{})
	doneOnce := &sync.Once{}
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.conn = client
	owner.running = true
	owner.mode = "package"
	owner.stopped = true
	owner.stack = nil
	owner.readerDone = done
	owner.readerDoneOnce = doneOnce
	owner.mu.Unlock()
	go d.readLoop(owner, generation, client, done, doneOnce)

	requests := make(chan dapMessage, 2)
	serverDone := make(chan error, 1)
	releaseServer := make(chan struct{})
	var releaseServerOnce sync.Once
	release := func() {
		releaseServerOnce.Do(func() { close(releaseServer) })
	}
	defer release()
	go func() {
		defer server.Close()
		reader := bufio.NewReader(server)
		for i := 0; i < 2; i++ {
			msg, err := readDAPMessage(reader)
			if err != nil {
				serverDone <- err
				return
			}
			requests <- msg
			if err := writeDAPMessage(server, dapMessage{
				Seq:        i + 200,
				Type:       "response",
				Command:    msg.Command,
				RequestSeq: msg.Seq,
				Success:    true,
				Body:       json.RawMessage(`{"result":"ok","type":"string"}`),
			}); err != nil {
				serverDone <- normalizeMockDAPServerError(err)
				return
			}
		}
		<-releaseServer
		serverDone <- nil
	}()

	if _, err := d.EvaluateWatch("count"); err != nil {
		t.Fatalf("EvaluateWatch without stack: %v", err)
	}
	if _, err := d.EvaluateREPL("print(count)"); err != nil {
		t.Fatalf("EvaluateREPL without stack: %v", err)
	}

	for _, wantContext := range []string{"watch", "repl"} {
		select {
		case msg := <-requests:
			if msg.Command != "evaluate" {
				t.Fatalf("command = %q, want evaluate", msg.Command)
			}
			var args map[string]json.RawMessage
			if err := json.Unmarshal(msg.Arguments, &args); err != nil {
				t.Fatalf("decode evaluate arguments: %v", err)
			}
			if _, exists := args["frameId"]; exists {
				t.Fatalf("evaluate %s unexpectedly included frameId: %s", wantContext, msg.Arguments)
			}
			var gotContext string
			if err := json.Unmarshal(args["context"], &gotContext); err != nil {
				t.Fatalf("decode evaluate context: %v", err)
			}
			if gotContext != wantContext {
				t.Fatalf("evaluate context = %q, want %q", gotContext, wantContext)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s evaluate request", wantContext)
		}
	}

	release()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("mock DAP server: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("mock DAP server did not finish")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("DAP reader did not stop")
	}
}

func TestDebugService_MultiSessionSwitchKeepsBreakpointStateIsolated(t *testing.T) {
	d := NewDebugService()
	first := d.activeSession()
	if _, err := d.SetBreakpointEx(filepath.Join(t.TempDir(), "first.go"), 7, "count > 1", ""); err != nil {
		t.Fatalf("set first breakpoint: %v", err)
	}
	if err := d.SetFunctionBreakpoints([]FunctionBreakpoint{{Name: "main.first", Condition: "ready"}}); err != nil {
		t.Fatalf("set first function breakpoint: %v", err)
	}

	second := d.bindSession(newDebugSession())
	d.sessionsMu.Lock()
	d.sessions["second"] = second
	d.sessionsMu.Unlock()
	if err := d.SetActiveSession("second"); err != nil {
		t.Fatalf("activate second session: %v", err)
	}
	if _, err := d.SetBreakpointEx(filepath.Join(t.TempDir(), "second.go"), 13, "", "value={value}"); err != nil {
		t.Fatalf("set second breakpoint: %v", err)
	}
	if err := d.SetFunctionBreakpoints([]FunctionBreakpoint{{Name: "main.second"}}); err != nil {
		t.Fatalf("set second function breakpoint: %v", err)
	}
	if got := d.ListBreakpoints(); len(got) != 1 || got[0].Line != 13 || got[0].LogMessage == "" {
		t.Fatalf("second session breakpoints = %+v", got)
	}

	if err := d.SetActiveSession("default"); err != nil {
		t.Fatalf("reactivate first session: %v", err)
	}
	if d.activeSession() != first {
		t.Fatal("active session did not return to the original owner")
	}
	if got := d.ListBreakpoints(); len(got) != 1 || got[0].Line != 7 || got[0].Condition != "count > 1" {
		t.Fatalf("first session breakpoints = %+v", got)
	}
	if got := d.ListFunctionBreakpoints(); len(got) != 1 || got[0].Name != "main.first" {
		t.Fatalf("first session function breakpoints = %+v", got)
	}
}

func TestDebugService_LoadStackFramesDoesNotReenterSessionsLock(t *testing.T) {
	d := NewDebugService()
	owner := d.activeSession()
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.supportsDelayedStackTraceLoading = true
	owner.stopped = true
	owner.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := d.LoadStackFrames(ctx, generation, 0, 1)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("LoadStackFrames error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("LoadStackFrames deadlocked while resolving the active session")
	}
}

func TestReadDAPMessageRejectsMalformedAndUnboundedInput(t *testing.T) {
	malformedJSON := `{"seq":1,"type":`
	tests := []struct {
		name string
		wire string
		want string
	}{
		{name: "invalid content length", wire: "Content-Length: nope\r\n\r\n", want: "invalid Content-Length"},
		{name: "missing content length", wire: "X-Test: value\r\n\r\n", want: "missing or invalid content-length"},
		{name: "invalid json", wire: fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(malformedJSON), malformedJSON), want: "decode dap message"},
		{name: "oversized header", wire: strings.Repeat("x", maxDAPHeaderLineLength+1) + "\n", want: "dap header line exceeds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readDAPMessage(bufio.NewReader(strings.NewReader(tc.wire)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("readDAPMessage error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDebugService_ProtocolLogDefaultOffAndBoundedRedactedOutputEvent(t *testing.T) {
	d := NewDebugService()
	d.SetDebugProtocolLog(false)
	events := make(chan map[string]any, 2)
	d.protocolMu.Lock()
	d.protocolEmitter = func(name string, payload any) {
		if name != "output:write" {
			t.Errorf("event name = %q, want output:write", name)
			return
		}
		events <- payload.(map[string]any)
	}
	d.protocolMu.Unlock()

	payload := map[string]any{
		"command": "launch",
		"arguments": map[string]any{
			"env":        map[string]string{"API_TOKEN": "super-secret-token"},
			"expression": "super-secret-expression",
			"note":       strings.Repeat("界", 5000),
		},
	}
	d.activeSession().logProtocol("DAP", "->", payload)
	select {
	case <-events:
		t.Fatal("protocol log emitted while disabled")
	default:
	}

	d.SetDebugProtocolLog(true)
	d.activeSession().logProtocol("DAP", "->", payload)
	select {
	case event := <-events:
		if event["channel"] != "Debug Protocol" {
			t.Fatalf("channel = %v", event["channel"])
		}
		text, ok := event["text"].(string)
		if !ok {
			t.Fatalf("text payload type = %T", event["text"])
		}
		if len(text) > debugProtocolLogMaxBytes {
			t.Fatalf("protocol log length = %d, max = %d", len(text), debugProtocolLogMaxBytes)
		}
		if !utf8.ValidString(text) {
			t.Fatal("protocol log truncation produced invalid UTF-8")
		}
		if strings.Contains(text, "super-secret") || strings.Contains(text, "API_TOKEN") {
			t.Fatalf("protocol log leaked sensitive data: %s", text)
		}
		if !strings.Contains(text, "[redacted]") {
			t.Fatalf("protocol log did not mark redacted fields: %s", text)
		}
	case <-time.After(time.Second):
		t.Fatal("enabled protocol log did not emit an output event")
	}
}

func TestLoadDebugProtocolLogSettingAtUsesActiveProfileAndLegacyFallback(t *testing.T) {
	t.Run("active profile enabled", func(t *testing.T) {
		configDir := t.TempDir()
		root := filepath.Join(configDir, "koyori-ide")
		profileDir := filepath.Join(root, "profiles", "work")
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "profiles-state.json"), []byte(`{"activeProfile":"work"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "settings.json"), []byte(`{"debugProtocolLog":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		enabled, err := loadDebugProtocolLogSettingAt(configDir)
		if err != nil || !enabled {
			t.Fatalf("active profile setting enabled=%v err=%v", enabled, err)
		}
	})

	t.Run("active profile false overrides legacy true", func(t *testing.T) {
		configDir := t.TempDir()
		root := filepath.Join(configDir, "koyori-ide")
		profileDir := filepath.Join(root, "profiles", "default")
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "settings.json"), []byte(`{"debugProtocolLog":false}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"debugProtocolLog":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		enabled, err := loadDebugProtocolLogSettingAt(configDir)
		if err != nil || enabled {
			t.Fatalf("profile override enabled=%v err=%v", enabled, err)
		}
	})

	t.Run("legacy fallback", func(t *testing.T) {
		configDir := t.TempDir()
		root := filepath.Join(configDir, "koyori-ide")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"debugProtocolLog":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		enabled, err := loadDebugProtocolLogSettingAt(configDir)
		if err != nil || !enabled {
			t.Fatalf("legacy setting enabled=%v err=%v", enabled, err)
		}
	})

	t.Run("missing defaults false", func(t *testing.T) {
		enabled, err := loadDebugProtocolLogSettingAt(t.TempDir())
		if err != nil || enabled {
			t.Fatalf("missing setting enabled=%v err=%v", enabled, err)
		}
	})

	t.Run("malformed fails closed", func(t *testing.T) {
		configDir := t.TempDir()
		profileDir := filepath.Join(configDir, "koyori-ide", "profiles", "default")
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "settings.json"), []byte(`{"debugProtocolLog":`), 0o600); err != nil {
			t.Fatal(err)
		}
		enabled, err := loadDebugProtocolLogSettingAt(configDir)
		if err == nil || enabled {
			t.Fatalf("malformed setting enabled=%v err=%v", enabled, err)
		}
	})
}

func TestDebugService_DAPProtocolLogCoversRequestAndResponse(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	d := NewDebugService()
	d.SetDebugProtocolLog(true)
	events := make(chan string, 8)
	d.protocolMu.Lock()
	d.protocolEmitter = func(_ string, payload any) {
		events <- payload.(map[string]any)["text"].(string)
	}
	d.protocolMu.Unlock()

	owner := d.activeSession()
	done := make(chan struct{})
	doneOnce := new(sync.Once)
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.conn = client
	owner.running = true
	owner.readerDone = done
	owner.readerDoneOnce = doneOnce
	owner.mu.Unlock()
	go d.readLoop(owner, generation, client, done, doneOnce)

	serverDone := make(chan error, 1)
	go func() {
		request, err := readDAPMessage(bufio.NewReader(server))
		if err == nil {
			err = writeDAPMessage(server, dapMessage{
				Seq: 2, Type: "response", RequestSeq: request.Seq, Command: request.Command,
				Success: true, Body: json.RawMessage(`{"ok":true}`),
			})
		}
		serverDone <- normalizeMockDAPServerError(err)
	}()
	if _, err := d.dapRequestBodyForRun(owner, generation, client, "threads", map[string]any{}); err != nil {
		t.Fatalf("dap request: %v", err)
	}

	seenOutbound, seenInbound := false, false
	deadline := time.After(time.Second)
	for !seenOutbound || !seenInbound {
		select {
		case text := <-events:
			seenOutbound = seenOutbound || strings.HasPrefix(text, "DAP -> ")
			seenInbound = seenInbound || strings.HasPrefix(text, "DAP <- ")
		case <-deadline:
			t.Fatalf("protocol events outbound=%v inbound=%v", seenOutbound, seenInbound)
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("mock dap server: %v", err)
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("stop debug session: %v", err)
	}
}

func TestDebugService_StopSessionWaitsForReaderAndDisposesRemovedSession(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	started := make(chan struct{})
	conn := &readStartedConn{Conn: client, started: started}
	d := NewDebugService()
	owner := d.activeSession()
	done := make(chan struct{})
	doneOnce := new(sync.Once)
	wg := new(sync.WaitGroup)
	wg.Add(1)
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.conn = conn
	owner.running = true
	owner.readerDone = done
	owner.readerDoneOnce = doneOnce
	owner.readerWG = wg
	owner.breakpoints = []DebugBreakpoint{{File: "main.go", Line: 1}}
	owner.functionBreakpoints = []FunctionBreakpoint{{Name: "main.main"}}
	owner.watches = []string{"value"}
	owner.mu.Unlock()
	d.startDAPReadLoop(owner, generation, conn, done, doneOnce, wg)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dap reader did not start")
	}

	if err := d.StopSession("default"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("StopSession returned before the dap reader exited")
	}
	d.sessionsMu.RLock()
	replacement := d.sessions["default"]
	d.sessionsMu.RUnlock()
	if replacement == nil || replacement == owner {
		t.Fatal("StopSession did not remove and replace the active session mapping")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.conn != nil || owner.running || len(owner.breakpoints) != 0 || len(owner.functionBreakpoints) != 0 || len(owner.watches) != 0 {
		t.Fatalf("removed session retained resources: conn=%v running=%v breakpoints=%d functions=%d watches=%d",
			owner.conn, owner.running, len(owner.breakpoints), len(owner.functionBreakpoints), len(owner.watches))
	}
}

func TestDebugService_StopSessionWaitsForProcessReaper(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestDebugService_ProcessExitHelper$")
	cmd.Env = append(os.Environ(), debugProcessExitHelperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("create helper stdin: %v", err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}

	d := NewDebugService()
	owner := d.activeSession()
	processDone := make(chan struct{})
	processDoneOnce := new(sync.Once)
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.cmd = cmd
	owner.running = true
	owner.processDone = processDone
	owner.processDoneOnce = processDoneOnce
	owner.mu.Unlock()
	go owner.waitForProcessExitTracked(cmd, generation, processDone, processDoneOnce)

	if err := d.StopSession("default"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	select {
	case <-processDone:
	default:
		t.Fatal("StopSession returned before the process wait goroutine completed")
	}
	if cmd.ProcessState == nil {
		t.Fatalf("debug process was not reaped: %+v", cmd.ProcessState)
	}
}
