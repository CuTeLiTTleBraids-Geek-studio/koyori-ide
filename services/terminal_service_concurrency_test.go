package services

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockConn is a controllable io.ReadWriteCloser for terminal service tests.
// It does NOT require a real PTY/ConPTY, so these tests run in any environment.
type mockConn struct {
	mu          sync.Mutex
	closed      bool
	exitCode    int
	exitKnown   bool
	exitSignal  string
	signalKnown bool
	readCh      chan []byte
	writeMu     sync.Mutex
	written     []byte
	closeCh     chan struct{}
}

func newMockConn() *mockConn {
	return &mockConn{
		readCh:  make(chan []byte, 16),
		closeCh: make(chan struct{}),
	}
}

func (m *mockConn) Read(p []byte) (int, error) {
	select {
	case data := <-m.readCh:
		n := copy(p, data)
		return n, nil
	case <-m.closeCh:
		return 0, io.EOF
	}
}

func (m *mockConn) Write(p []byte) (int, error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.mu.Lock()
	if m.closed {
		return 0, io.ErrClosedPipe
	}
	m.mu.Unlock()
	m.written = append(m.written, p...)
	return len(p), nil
}

func (m *mockConn) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	close(m.closeCh)
	return nil
}

func (m *mockConn) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *mockConn) getWritten() []byte {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	return m.written
}

func (m *mockConn) ExitCode() (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.exitCode, m.exitKnown
}

func (m *mockConn) ExitSignal() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.exitSignal, m.signalKnown
}

func TestOutputBatcher_FlushesOnIntervalAndSize(t *testing.T) {
	t.Run("interval", func(t *testing.T) {
		emitted := make(chan string, 1)
		batcher := newOutputBatcher(func(data string) { emitted <- data })
		defer batcher.Close()

		batcher.Append([]byte("hello "))
		batcher.Append([]byte("world"))
		select {
		case got := <-emitted:
			if got != "hello world" {
				t.Fatalf("batched output = %q, want %q", got, "hello world")
			}
		case <-time.After(10 * terminalOutputBatchInterval):
			t.Fatal("output batcher did not flush on interval")
		}
	})

	t.Run("size", func(t *testing.T) {
		emitted := make(chan string, 1)
		batcher := newOutputBatcher(func(data string) { emitted <- data })
		defer batcher.Close()

		want := strings.Repeat("x", terminalOutputBatchMaxBytes)
		batcher.Append([]byte(want))
		select {
		case got := <-emitted:
			if got != want {
				t.Fatalf("size-triggered batch length = %d, want %d", len(got), len(want))
			}
		case <-time.After(10 * terminalOutputBatchInterval):
			t.Fatal("output batcher did not flush at size threshold")
		}
	})
}

func TestTerminalSessionExitCodeAndErrorClassification(t *testing.T) {
	conn := newMockConn()
	conn.exitCode = 7
	conn.exitKnown = true
	session := &TerminalSession{conn: conn}
	if err := session.close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	if got := terminalSessionExitCode(conn); got != 7 {
		t.Fatalf("exit code = %d, want 7", got)
	}
	if !shouldLogTerminalSessionError(io.ErrUnexpectedEOF, 7, false) {
		t.Fatal("non-zero unexpected exit should be logged as a session error")
	}
	if shouldLogTerminalSessionError(io.ErrUnexpectedEOF, 7, true) {
		t.Fatal("a requested stop should not be logged as an unexpected session error")
	}
	if shouldLogTerminalSessionError(io.EOF, -1, false) {
		t.Fatal("EOF should be treated as a normal terminal exit")
	}
}

func TestTerminalSessionExitSignal(t *testing.T) {
	conn := newMockConn()
	conn.exitSignal = "terminated"
	conn.signalKnown = true
	if got := terminalSessionExitSignal(conn); got != "terminated" {
		t.Fatalf("exit signal = %q, want terminated", got)
	}

	conn.signalKnown = false
	if got := terminalSessionExitSignal(conn); got != "" {
		t.Fatalf("unknown exit signal = %q, want empty", got)
	}
}

// --- N-94: TOCTOU race tests ---

// TestTerminalService_N94_WriteSession_ChecksRunningUnderLock verifies that
// WriteSession checks session.running while holding t.mu. After KillSession
// sets running=false, a concurrent WriteSession must return ErrTerminalNotRunning
// rather than writing to the dead conn.
func TestTerminalService_N94_WriteSession_ChecksRunningUnderLock(t *testing.T) {
	ts := NewTerminalService()
	defer ts.Shutdown()

	conn := newMockConn()
	session := &TerminalSession{
		id:        "test-94-w",
		conn:      conn,
		outputBuf: newOutputBuffer(),
		running:   true,
	}
	ts.mu.Lock()
	ts.sessions["test-94-w"] = session
	ts.mu.Unlock()

	// Kill the session (sets running=false under lock, deletes from map).
	if err := ts.KillSession("test-94-w"); err != nil {
		t.Fatalf("KillSession failed: %v", err)
	}

	// WriteSession must return ErrTerminalNotRunning, not write to conn.
	err := ts.WriteSession("test-94-w", "hello")
	if err != ErrTerminalNotRunning {
		t.Errorf("expected ErrTerminalNotRunning after Kill, got %v", err)
	}
}

// TestTerminalService_N94_ResizeSession_ChecksRunningUnderLock verifies the
// same TOCTOU fix for ResizeSession.
func TestTerminalService_N94_ResizeSession_ChecksRunningUnderLock(t *testing.T) {
	ts := NewTerminalService()
	defer ts.Shutdown()

	conn := newMockConn()
	session := &TerminalSession{
		id:        "test-94-r",
		conn:      conn,
		outputBuf: newOutputBuffer(),
		running:   true,
	}
	ts.mu.Lock()
	ts.sessions["test-94-r"] = session
	ts.mu.Unlock()

	if err := ts.KillSession("test-94-r"); err != nil {
		t.Fatalf("KillSession failed: %v", err)
	}

	err := ts.ResizeSession("test-94-r", 80, 24)
	if err != ErrTerminalNotRunning {
		t.Errorf("expected ErrTerminalNotRunning after Kill, got %v", err)
	}
}

// TestTerminalService_N94_IsSessionRunning_ChecksUnderLock verifies that
// IsSessionRunning returns false immediately after KillSession.
func TestTerminalService_N94_IsSessionRunning_ChecksUnderLock(t *testing.T) {
	ts := NewTerminalService()
	defer ts.Shutdown()

	conn := newMockConn()
	session := &TerminalSession{
		id:        "test-94-i",
		conn:      conn,
		outputBuf: newOutputBuffer(),
		running:   true,
	}
	ts.mu.Lock()
	ts.sessions["test-94-i"] = session
	ts.mu.Unlock()

	if !ts.IsSessionRunning("test-94-i") {
		t.Error("expected session to be running before Kill")
	}

	if err := ts.KillSession("test-94-i"); err != nil {
		t.Fatalf("KillSession failed: %v", err)
	}

	if ts.IsSessionRunning("test-94-i") {
		t.Error("expected session to NOT be running after Kill")
	}
}

// TestTerminalService_N94_WriteSession_ConcurrentKillNoRace verifies that
// concurrent WriteSession and KillSession calls don't race on session.running.
// Run with -race to detect data races.
func TestTerminalService_N94_WriteSession_ConcurrentKillNoRace(t *testing.T) {
	ts := NewTerminalService()
	defer ts.Shutdown()

	conn := newMockConn()
	session := &TerminalSession{
		id:        "test-94-cr",
		conn:      conn,
		outputBuf: newOutputBuffer(),
		running:   true,
	}
	ts.mu.Lock()
	ts.sessions["test-94-cr"] = session
	ts.mu.Unlock()

	var wg sync.WaitGroup
	// Critical 自审修复：stop 必须用 atomic 访问，否则 go test -race
	// 会检测到测试代码自身的 data race（写 goroutine 读 stop，
	// 主 goroutine 写 stop）。这是测试代码 BUG，非被测代码 BUG。
	var stop atomic.Int32

	// Writer: repeatedly writes to the session.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			if stop.Load() != 0 {
				return
			}
			_ = ts.WriteSession("test-94-cr", "x")
		}
	}()

	// Killer: kills the session once.
	time.Sleep(10 * time.Millisecond)
	ts.KillSession("test-94-cr")
	stop.Store(1)
	wg.Wait()
}

// --- N-65: session cleanup tests ---

// TestTerminalService_N65_ReadLoop_DeletesSessionOnExit verifies that when
// the PTY conn returns an error (e.g. EOF), the readLoop deletes the session
// from the sessions map, preventing memory leaks.
func TestTerminalService_N65_ReadLoop_DeletesSessionOnExit(t *testing.T) {
	ts := NewTerminalService()
	defer ts.Shutdown()

	conn := newMockConn()
	session := &TerminalSession{
		id:        "test-65",
		conn:      conn,
		outputBuf: newOutputBuffer(),
		running:   true,
	}
	ts.mu.Lock()
	ts.sessions["test-65"] = session
	ts.mu.Unlock()

	// Start the readLoop manually (bypass StartSession which needs a real PTY).
	ts.wg.Add(1)
	go func() {
		defer ts.wg.Done()
		ts.readLoop(session, nil)
	}()

	// Close the conn to simulate the PTY exiting.
	conn.Close()

	// Wait for the readLoop to process the close and clean up.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ts.mu.Lock()
		_, exists := ts.sessions["test-65"]
		ts.mu.Unlock()
		if !exists {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	ts.mu.Lock()
	_, exists := ts.sessions["test-65"]
	ts.mu.Unlock()
	if exists {
		t.Error("N-65: session should have been deleted from map after readLoop exit")
	}
}

// TestTerminalService_N65_ReadLoop_DoesNotDeleteReplacedSession verifies that
// cleanupSession doesn't delete a session that was already replaced by a new
// one with the same ID (avoids deleting the wrong session).
func TestTerminalService_N65_ReadLoop_DoesNotDeleteReplacedSession(t *testing.T) {
	ts := NewTerminalService()
	defer ts.Shutdown()

	conn1 := newMockConn()
	oldSession := &TerminalSession{
		id:        "test-65-repl",
		conn:      conn1,
		outputBuf: newOutputBuffer(),
		running:   true,
	}
	ts.mu.Lock()
	ts.sessions["test-65-repl"] = oldSession
	ts.mu.Unlock()

	// Start readLoop for old session.
	ts.wg.Add(1)
	go func() {
		defer ts.wg.Done()
		ts.readLoop(oldSession, nil)
	}()

	// Replace the session with a new one (simulating KillSession + StartSession
	// with the same ID).
	conn2 := newMockConn()
	newSession := &TerminalSession{
		id:        "test-65-repl",
		conn:      conn2,
		outputBuf: newOutputBuffer(),
		running:   true,
	}
	ts.mu.Lock()
	delete(ts.sessions, "test-65-repl")
	ts.sessions["test-65-repl"] = newSession
	ts.mu.Unlock()

	// Close the OLD conn — old readLoop should exit but NOT delete the new session.
	conn1.Close()

	// Wait for old readLoop to exit.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ts.mu.Lock()
		cur := ts.sessions["test-65-repl"]
		ts.mu.Unlock()
		if cur == newSession {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	ts.mu.Lock()
	cur := ts.sessions["test-65-repl"]
	ts.mu.Unlock()
	if cur != newSession {
		t.Error("N-65: old readLoop deleted the replacement session — identity check failed")
	}
}

// --- N-95: readLoop cancellation and Shutdown tests ---

// TestTerminalService_N95_Shutdown_CancelsReadLoop verifies that Shutdown()
// causes all readLoop goroutines to exit. We track goroutine exit via the
// WaitGroup (Shutdown blocks until wg.Wait() returns).
func TestTerminalService_N95_Shutdown_CancelsReadLoop(t *testing.T) {
	ts := NewTerminalService()

	conn := newMockConn()
	session := &TerminalSession{
		id:        "test-95",
		conn:      conn,
		outputBuf: newOutputBuffer(),
		running:   true,
	}
	ts.mu.Lock()
	ts.sessions["test-95"] = session
	ts.mu.Unlock()

	// Start a readLoop that blocks on conn.Read (no data incoming).
	ts.wg.Add(1)
	go func() {
		defer ts.wg.Done()
		ts.readLoop(session, nil)
	}()

	// Give the readLoop time to start.
	time.Sleep(50 * time.Millisecond)

	// Shutdown should close the conn, unblock the readLoop, and wait for it.
	done := make(chan struct{})
	go func() {
		ts.Shutdown()
		close(done)
	}()

	select {
	case <-done:
		// Success: Shutdown returned, meaning the readLoop exited.
	case <-time.After(5 * time.Second):
		t.Fatal("N-95: Shutdown did not return within 5s — readLoop goroutine leaked")
	}

	if !conn.isClosed() {
		t.Error("N-95: Shutdown should have closed the conn")
	}
}

// TestTerminalService_N95_Shutdown_MultipleCallsSafe verifies that calling
// Shutdown() multiple times is safe (no panic, no hang).
func TestTerminalService_N95_Shutdown_MultipleCallsSafe(t *testing.T) {
	ts := NewTerminalService()
	ts.Shutdown()
	ts.Shutdown()
	ts.Shutdown()
}

// TestTerminalService_N95_Shutdown_NoSessionsReturnsQuickly verifies that
// Shutdown() returns quickly when there are no active sessions.
func TestTerminalService_N95_Shutdown_NoSessionsReturnsQuickly(t *testing.T) {
	ts := NewTerminalService()
	done := make(chan struct{})
	go func() {
		ts.Shutdown()
		close(done)
	}()
	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("N-95: Shutdown with no sessions should return immediately")
	}
}

// TestTerminalService_N95_Shutdown_MultipleSessions verifies that Shutdown()
// closes all sessions and waits for all readLoop goroutines.
func TestTerminalService_N95_Shutdown_MultipleSessions(t *testing.T) {
	ts := NewTerminalService()

	conns := make([]*mockConn, 3)
	for i := 0; i < 3; i++ {
		conn := newMockConn()
		conns[i] = conn
		session := &TerminalSession{
			id:        "test-95-multi-" + string(rune('a'+i)),
			conn:      conn,
			outputBuf: newOutputBuffer(),
			running:   true,
		}
		ts.mu.Lock()
		ts.sessions[session.id] = session
		ts.mu.Unlock()

		ts.wg.Add(1)
		go func(s *TerminalSession) {
			defer ts.wg.Done()
			ts.readLoop(s, nil)
		}(session)
	}

	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		ts.Shutdown()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("N-95: Shutdown with 3 sessions did not return within 5s")
	}

	for i, conn := range conns {
		if !conn.isClosed() {
			t.Errorf("N-95: conn %d was not closed by Shutdown", i)
		}
	}
}

// TestTerminalService_N95_ReadLoop_EmitsExitedEvent verifies that the readLoop
// emits the terminal:exited event when the session exits. Since we can't
// easily test event emission without a real app, we verify that cleanupSession
// is called (which is the function that emits the event).
func TestTerminalService_N95_ReadLoop_CleanupSetsRunningFalse(t *testing.T) {
	ts := NewTerminalService()
	defer ts.Shutdown()

	conn := newMockConn()
	session := &TerminalSession{
		id:        "test-95-cleanup",
		conn:      conn,
		outputBuf: newOutputBuffer(),
		running:   true,
	}
	ts.mu.Lock()
	ts.sessions["test-95-cleanup"] = session
	ts.mu.Unlock()

	ts.wg.Add(1)
	go func() {
		defer ts.wg.Done()
		ts.readLoop(session, nil)
	}()

	conn.Close()

	// Wait for readLoop to exit and cleanup.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ts.mu.Lock()
		running := session.running
		ts.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	ts.mu.Lock()
	running := session.running
	ts.mu.Unlock()
	if running {
		t.Error("N-95: session.running should be false after readLoop cleanup")
	}
}
