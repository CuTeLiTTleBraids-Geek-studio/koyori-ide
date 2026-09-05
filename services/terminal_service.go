package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// TerminalSession represents a single PTY terminal session.
type TerminalSession struct {
	id            string
	conn          io.ReadWriteCloser
	outputBuf     *outputBuffer
	running       bool
	stopRequested bool
	workingDir    string
	shell         []string
	closeOnce     sync.Once
	closeErr      error
}

func (s *TerminalSession) close() error {
	s.closeOnce.Do(func() {
		if s.conn != nil {
			s.closeErr = s.conn.Close()
		}
	})
	return s.closeErr
}

// TerminalService manages multiple terminal sessions.
//
// N-94 / Proposal AB: session.running is protected by t.mu. All reads and
// writes of session.running happen while holding t.mu, eliminating the
// TOCTOU race where WriteSession/ResizeSession/IsSessionRunning observed
// a stale value after releasing the lock.
//
// N-95 / Proposal AC: ctx and cancel provide a cancellation mechanism for
// readLoop goroutines. Shutdown() cancels the context and closes all
// session conns, which unblocks any pending Read calls, then waits for
// all goroutines to exit via wg.
type TerminalService struct {
	mu                          sync.Mutex
	sessions                    map[string]*TerminalSession
	starting                    map[string]struct{}
	shuttingDown                bool
	shutdownDone                chan struct{}
	rootDir                     string
	workspaceContext            *WorkspaceContext
	beforeWorkspaceCommandStart func()
	app                         *application.App
	// N-95: ctx is cancelled by Shutdown() to signal all readLoop goroutines
	// to exit. cancel is the function that cancels ctx.
	ctx    context.Context
	cancel context.CancelFunc
	// N-95: wg tracks all active readLoop goroutines so Shutdown() can wait
	// for them to exit before returning.
	wg sync.WaitGroup
	// startingWG tracks StartSession calls after they reserve an ID. Shutdown
	// closes the admission gate under mu before waiting, so Add and Wait never
	// run concurrently.
	startingWG sync.WaitGroup
}

func NewTerminalService() *TerminalService {
	return newTerminalService(nil)
}

// NewTerminalServiceWithWorkspaceContext creates the renderer-facing service.
// New sessions resolve their root from the shared context at call time.
func NewTerminalServiceWithWorkspaceContext(workspaceContext *WorkspaceContext) *TerminalService {
	return newTerminalService(workspaceContext)
}

func newTerminalService(workspaceContext *WorkspaceContext) *TerminalService {
	ctx, cancel := context.WithCancel(context.Background())
	return &TerminalService{
		sessions:         make(map[string]*TerminalSession),
		starting:         make(map[string]struct{}),
		workspaceContext: workspaceContext,
		ctx:              ctx,
		cancel:           cancel,
	}
}

// setApp links the Wails app for event emission.
//
//wails:ignore
func (t *TerminalService) setApp(app *application.App) {
	t.mu.Lock()
	t.app = app
	t.mu.Unlock()
}

// setWorkspaceRoot sets the directory within which terminal sessions are allowed.
// It is a backend-only capability used by ProjectService; renderer code must not
// be able to alter the terminal sandbox.
//
//wails:ignore
func (t *TerminalService) setWorkspaceRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		t.mu.Lock()
		t.rootDir = ""
		t.mu.Unlock()
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace root is not a directory: %s", abs)
	}
	t.mu.Lock()
	t.rootDir = abs
	t.mu.Unlock()
	return nil
}

// validateWorkingDir checks that the path is within the configured workspace root.
//
// G-SEC-06: validation is delegated to ValidatePathWithinRoot, which
// resolves symlinks on both the target and the root before comparing.
// The previous lexical-only check (filepath.Abs + filepath.Rel) could
// be bypassed by a symlink inside the workspace pointing outside.
func (t *TerminalService) validateWorkingDir(workingDir string) error {
	_, _, err := t.resolveWorkingDirWithLease(workingDir)
	return err
}

func (t *TerminalService) acquireWorkspaceLease() (workspaceLease, error) {
	t.mu.Lock()
	root := t.rootDir
	t.mu.Unlock()
	return acquireWorkspaceLease(t.workspaceContext, root, 0)
}

func (t *TerminalService) resolveWorkingDirWithLease(workingDir string) (string, workspaceLease, error) {
	lease, err := t.acquireWorkspaceLease()
	if err != nil {
		return "", workspaceLease{}, err
	}
	resolved, err := lease.resolve(workingDir)
	if err != nil {
		return "", workspaceLease{}, err
	}
	return resolved, lease, nil
}

// StartSession creates and starts a new terminal session with the given ID.
// allowedShells is the whitelist of shell base names that can be used in
// StartSession (M-4). The check is on the base name of the shell path, so
// "/usr/bin/bash" and "bash" both match. On Windows, the comparison is
// case-insensitive (and the .exe suffix is stripped).
var allowedShells = map[string]bool{
	"bash":       true,
	"sh":         true,
	"zsh":        true,
	"powershell": true,
	"pwsh":       true,
	"cmd":        true,
	"wsl":        true,
}

// isAllowedShell returns true if the shell's base name (with .exe stripped
// on Windows) is in the allowedShells whitelist. This prevents the frontend
// from launching an arbitrary binary as a terminal shell (M-4).
func isAllowedShell(shell string) bool {
	base := filepath.Base(shell)
	// HIGH-01: lowercase before trimming .exe so "CMD.EXE" / "PowerShell.exe"
	// normalize correctly (previously TrimSuffix(".exe") missed uppercase).
	base = strings.ToLower(base)
	base = strings.TrimSuffix(base, ".exe")
	return allowedShells[base]
}

func (t *TerminalService) StartSession(id string, workingDir string, shell string) error {
	if id == "" {
		return fmt.Errorf("session ID cannot be empty")
	}
	if err := t.reserveSessionStart(id); err != nil {
		return err
	}
	reserved := true
	defer func() {
		if reserved {
			t.finishSessionStart(id)
		}
	}()

	resolvedWorkingDir, lease, err := t.resolveWorkingDirWithLease(workingDir)
	if err != nil {
		slog.Warn("terminal: invalid working dir", "sessionId", id, "workingDir", workingDir, "err", err)
		return err
	}
	workingDir = resolvedWorkingDir

	info, err := os.Stat(workingDir)
	if err != nil {
		slog.Warn("terminal: working dir stat failed", "sessionId", id, "workingDir", workingDir, "err", err)
		return fmt.Errorf("invalid working directory: %w", err)
	}
	if !info.IsDir() {
		slog.Warn("terminal: working dir not a directory", "sessionId", id, "workingDir", workingDir)
		return fmt.Errorf("working directory is not a directory: %s", workingDir)
	}

	resolvedShell := defaultShell()
	if shell != "" {
		if !isAllowedShell(shell) {
			slog.Warn("terminal: rejected shell not in whitelist", "sessionId", id, "shell", shell)
			return fmt.Errorf("shell %q is not in the allowed list (M-4: bash/sh/zsh/powershell/pwsh/cmd/wsl)", shell)
		}
		resolvedShell = resolveShellCommand(shell)
	}

	t.mu.Lock()
	shuttingDown := t.shuttingDown
	t.mu.Unlock()
	if shuttingDown {
		return errTerminalServiceShuttingDown
	}
	if t.beforeWorkspaceCommandStart != nil {
		t.beforeWorkspaceCommandStart()
	}
	if err := lease.validateCurrent(); err != nil {
		return err
	}

	conn, err := startPty(resolvedShell, workingDir)
	if err != nil {
		err = closeTerminalConn(conn, err)
		logTerminalSessionError(id, err)
		return err
	}

	session := &TerminalSession{
		id:         id,
		conn:       conn,
		outputBuf:  newOutputBuffer(),
		running:    true,
		workingDir: workingDir,
		shell:      resolvedShell,
	}

	var app *application.App
	var registrationErr error
	t.mu.Lock()
	if t.shuttingDown {
		registrationErr = errTerminalServiceShuttingDown
	} else if _, exists := t.sessions[id]; exists {
		registrationErr = fmt.Errorf("session %s already exists", id)
	} else {
		delete(t.starting, id)
		t.sessions[id] = session
		app = t.app
		// Register the goroutine while holding the lifecycle gate. Shutdown
		// takes the same lock before it starts waiting, so Add cannot race Wait.
		t.wg.Add(1)
		t.startingWG.Done()
		reserved = false
	}
	t.mu.Unlock()
	if registrationErr != nil {
		return closeTerminalConn(conn, registrationErr)
	}

	go func() {
		defer RecoverGoroutinePanic("terminal:read-pump")
		defer t.wg.Done()
		t.readLoop(session, app)
	}()

	slog.Info("terminal session started", "id", id, "cwd", workingDir, "shell", resolvedShell[0])
	return nil
}

var errTerminalServiceShuttingDown = errors.New("terminal service is shutting down")

func (t *TerminalService) reserveSessionStart(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.shuttingDown {
		return errTerminalServiceShuttingDown
	}
	if _, exists := t.sessions[id]; exists {
		return fmt.Errorf("session %s already exists", id)
	}
	if _, exists := t.starting[id]; exists {
		return fmt.Errorf("session %s already exists", id)
	}
	if t.starting == nil {
		t.starting = make(map[string]struct{})
	}
	t.starting[id] = struct{}{}
	t.startingWG.Add(1)
	return nil
}

func (t *TerminalService) finishSessionStart(id string) {
	t.mu.Lock()
	if _, exists := t.starting[id]; exists {
		delete(t.starting, id)
		t.startingWG.Done()
	}
	t.mu.Unlock()
}

func closeTerminalConn(conn io.Closer, cause error) error {
	if conn == nil {
		return cause
	}
	if err := conn.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close terminal connection: %w", err))
	}
	return cause
}

// KillSession kills and removes a specific terminal session.
// N-94: session.running is set to false under t.mu to prevent TOCTOU races.
func (t *TerminalService) KillSession(id string) error {
	t.mu.Lock()
	session, exists := t.sessions[id]
	if !exists {
		t.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	// N-94: set running = false while holding the lock so concurrent
	// WriteSession/ResizeSession callers see the updated state.
	session.running = false
	session.stopRequested = true
	delete(t.sessions, id)
	t.mu.Unlock()

	if err := session.close(); err != nil {
		logTerminalSessionError(id, err)
		return fmt.Errorf("close terminal session %s: %w", id, err)
	}

	slog.Info("terminal: session killed", "sessionId", id)
	return nil
}

// WriteSession writes input to a specific terminal session.
// N-94: checks session.running while holding t.mu to prevent TOCTOU.
// Note: input is string (not []byte) because Wails bindings encode []byte as
// base64, which breaks when the frontend sends raw keystroke strings.
func (t *TerminalService) WriteSession(id string, input string) error {
	t.mu.Lock()
	session, exists := t.sessions[id]
	if !exists || !session.running {
		t.mu.Unlock()
		return ErrTerminalNotRunning
	}
	conn := session.conn
	t.mu.Unlock()
	_, err := conn.Write([]byte(input))
	if err != nil {
		logTerminalSessionError(id, err)
	}
	return err
}

// ResizeSession resizes a specific terminal session.
// N-94: checks session.running while holding t.mu to prevent TOCTOU.
func (t *TerminalService) ResizeSession(id string, cols int, rows int) error {
	t.mu.Lock()
	session, exists := t.sessions[id]
	if !exists || !session.running {
		t.mu.Unlock()
		return ErrTerminalNotRunning
	}
	conn := session.conn
	t.mu.Unlock()
	if r, ok := conn.(ptyResizer); ok {
		err := r.Resize(cols, rows)
		if err != nil {
			logTerminalSessionError(id, err)
		}
		return err
	}
	return nil
}

// IsSessionRunning checks if a specific session is running.
// N-94: reads session.running while holding t.mu to prevent TOCTOU.
func (t *TerminalService) IsSessionRunning(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	session, exists := t.sessions[id]
	return exists && session.running
}

// ListSessions returns the IDs of all active sessions.
func (t *TerminalService) ListSessions() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.sessions))
	for id := range t.sessions {
		ids = append(ids, id)
	}
	return ids
}

// incompleteUTF8TailLen returns the number of trailing bytes of b that form
// a possibly-incomplete UTF-8 sequence (i.e. the last rune's lead byte has
// fewer continuation bytes than its encoding requires). It returns 0 when b
// ends on a complete rune, an ASCII byte, or when the tail is not valid
// UTF-8 anyway (stray continuation bytes with no lead byte in sight).
//
// N-TERM-UTF8: PTY reads are chunked (4096 bytes in readLoop), so a
// multi-byte rune (CJK, emoji, etc.) can be split across two reads. Passing
// such a chunk through Wails event emission corrupts it: encoding/json
// replaces invalid UTF-8 bytes with U+FFFD during marshaling, producing
// mojibake (replacement characters) in xterm on both Windows and Linux. readLoop holds the
// incomplete tail back and prepends it to the next read so events always
// carry whole runes.
func incompleteUTF8TailLen(b []byte) int {
	n := len(b)
	if n == 0 {
		return 0
	}
	// Scan back at most UTFMax-1 bytes to find the last rune start byte.
	maxBack := utf8.UTFMax - 1
	if n < maxBack {
		maxBack = n
	}
	for i := n - 1; i >= n-maxBack; i-- {
		c := b[i]
		if c&0xC0 == 0x80 {
			continue // continuation byte — part of the trailing rune
		}
		// c is ASCII or a rune start byte.
		if c < 0x80 {
			return 0 // buffer ends with a complete ASCII byte
		}
		// Compute the encoding length of this rune from its lead byte
		// (0b110xxxxx → 2, 0b1110xxxx → 3, 0b11110xxx → 4).
		want := 1
		for mask := byte(0x40); c&mask != 0; mask >>= 1 {
			want++
		}
		if remaining := n - i; remaining < want {
			return remaining
		}
		return 0
	}
	// Pure continuation bytes with no lead byte in the scanned window —
	// invalid UTF-8; forwarding it unchanged cannot make it worse.
	return 0
}

// readLoop reads from the session's PTY and emits terminal:output events.
//
// N-94: sets session.running = false under t.mu when the session exits.
// N-65: deletes the session from t.sessions map when the session exits
// naturally (err != nil), preventing memory leaks from dead sessions.
// N-95: checks t.ctx.Done() between reads; Shutdown() cancels the context
// and closes all conns, which unblocks any pending Read.
func (t *TerminalService) readLoop(session *TerminalSession, app *application.App) {
	buf := make([]byte, 4096)
	var eventBatcher *outputBatcher
	if app != nil {
		eventBatcher = newOutputBatcher(func(data string) {
			app.Event.Emit("terminal:output", map[string]string{
				"sessionId": session.id,
				"data":      data,
			})
		})
	}
	// N-TERM-UTF8: bytes of an incomplete trailing UTF-8 rune held back from
	// the previous read. Prepend to the next read so a multi-byte character
	// split across chunk boundaries is never forwarded as invalid UTF-8
	// (which Wails' JSON serialization would corrupt into U+FFFD mojibake).
	var pendingTail []byte
	flushPendingTail := func() {
		if len(pendingTail) == 0 {
			return
		}
		session.outputBuf.Append(pendingTail)
		if eventBatcher != nil {
			eventBatcher.Append(pendingTail)
		}
		pendingTail = pendingTail[:0]
	}
	for {
		// N-95: check for shutdown between reads. The blocking Read below
		// is unblocked by Shutdown() closing the conn.
		select {
		case <-t.ctx.Done():
			if eventBatcher != nil {
				eventBatcher.Close()
			}
			t.cleanupSession(session, app, t.ctx.Err())
			return
		default:
		}

		n, err := session.conn.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if len(pendingTail) > 0 {
				combined := make([]byte, 0, len(pendingTail)+n)
				combined = append(combined, pendingTail...)
				combined = append(combined, chunk...)
				chunk = combined
				pendingTail = pendingTail[:0]
			}
			// Hold back an incomplete trailing rune so it is never split
			// across events (JSON marshaling would corrupt it into U+FFFD).
			if tail := incompleteUTF8TailLen(chunk); tail > 0 {
				pendingTail = append(pendingTail[:0], chunk[len(chunk)-tail:]...)
				chunk = chunk[:len(chunk)-tail]
			}
			if len(chunk) > 0 {
				session.outputBuf.Append(chunk)
				if eventBatcher != nil {
					eventBatcher.Append(chunk)
				}
			}
		}
		if err != nil {
			// EOF/error: flush any held-back tail so it is not lost.
			flushPendingTail()
			if eventBatcher != nil {
				eventBatcher.Close()
			}
			t.cleanupSession(session, app, err)
			return
		}
	}
}

// cleanupSession marks the session as not running, removes it from the
// sessions map (N-65), and emits the terminal:exited event.
// N-94: all mutations of session.running and t.sessions happen under t.mu.
func (t *TerminalService) cleanupSession(session *TerminalSession, app *application.App, err error) {
	t.mu.Lock()
	session.running = false
	stopRequested := session.stopRequested
	// N-65: delete the session from the map so it doesn't leak. Only delete
	// if it still points to our session (KillSession may have already deleted it).
	if cur, ok := t.sessions[session.id]; ok && cur == session {
		delete(t.sessions, session.id)
	}
	t.mu.Unlock()

	if closeErr := session.close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close terminal session %s: %w", session.id, closeErr))
	}
	if err == nil {
		err = io.EOF
	}

	exitCode := terminalSessionExitCode(session.conn)
	exitSignal := terminalSessionExitSignal(session.conn)
	slog.Info("terminal session exited", "id", session.id, "code", exitCode)
	if exitSignal != "" {
		slog.Info("terminal session terminated by signal", "id", session.id, "signal", exitSignal)
	}
	if shouldLogTerminalSessionError(err, exitCode, stopRequested) {
		logTerminalSessionError(session.id, err)
	}
	if app != nil {
		app.Event.Emit("terminal:output", map[string]string{
			"sessionId": session.id,
			"data":      "\r\n\x1b[90m[Process exited]\x1b[0m\r\n",
		})
		// N-47: emit a separate terminal:exited event so the frontend
		// can mark the session as not running immediately, without
		// relying on parsing the [Process exited] text marker. This
		// lets runCommandInSession return promptly instead of waiting
		// for the 5-minute timeout when the PTY dies mid-step.
		app.Event.Emit("terminal:exited", map[string]any{
			"sessionId": session.id,
			"code":      exitCode,
			"signal":    exitSignal,
			"err":       err.Error(),
		})
	}
}

// Shutdown cancels all readLoop goroutines and waits for them to exit
// (N-95 / Proposal AC). Should be called from application.OnShutdown.
// Safe to call multiple times.
func (t *TerminalService) Shutdown() {
	// Close the lifecycle admission gate and snapshot every registered session
	// in one critical section. Every startingWG.Add and wg.Add uses this gate.
	t.mu.Lock()
	if t.shuttingDown {
		done := t.shutdownDone
		t.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	t.shuttingDown = true
	t.shutdownDone = make(chan struct{})
	done := t.shutdownDone
	sessions := make([]*TerminalSession, 0, len(t.sessions))
	for _, session := range t.sessions {
		session.running = false
		session.stopRequested = true
		sessions = append(sessions, session)
	}
	t.mu.Unlock()
	defer close(done)

	// Cancel the context to signal all readLoop goroutines to exit.
	if t.cancel != nil {
		t.cancel()
	}

	// Close all session conns to unblock any pending Read calls.
	// Closing the conn causes Read to return an error, which triggers
	// cleanupSession and goroutine exit.
	for _, session := range sessions {
		if err := session.close(); err != nil {
			slog.Debug("terminal: close session during shutdown failed", "sessionId", session.id, "err", err)
		}
	}

	// A start that already reserved an ID either registered before the gate
	// closed or closes its newly-created PTY before marking itself complete.
	t.startingWG.Wait()

	// Wait for all readLoop goroutines to exit.
	t.wg.Wait()
}

// --- Backward-compatible single-session API (uses "default" session) ---

func (t *TerminalService) Start(workingDir string) error {
	return t.StartSession("default", workingDir, "")
}

func (t *TerminalService) Write(input string) error {
	return t.WriteSession("default", input)
}

func (t *TerminalService) Resize(cols, rows int) error {
	return t.ResizeSession("default", cols, rows)
}

func (t *TerminalService) Kill() {
	if err := t.KillSession("default"); err != nil && !errors.Is(err, ErrTerminalNotRunning) {
		slog.Debug("terminal: kill default session failed", "err", err)
	}
}

func (t *TerminalService) IsRunning() bool {
	return t.IsSessionRunning("default")
}

func (t *TerminalService) ReadOutput(timeout time.Duration) string {
	t.mu.Lock()
	session, exists := t.sessions["default"]
	t.mu.Unlock()
	if !exists {
		return ""
	}
	return session.outputBuf.Read(timeout)
}

type ptyResizer interface {
	Resize(cols, rows int) error
}

type ptyExitCoder interface {
	ExitCode() (int, bool)
}

type ptySignalCoder interface {
	ExitSignal() (string, bool)
}

func terminalSessionExitCode(conn io.ReadWriteCloser) int {
	exitCoder, ok := conn.(ptyExitCoder)
	if !ok {
		return -1
	}
	code, ok := exitCoder.ExitCode()
	if !ok {
		return -1
	}
	return code
}

func terminalSessionExitSignal(conn io.ReadWriteCloser) string {
	signalCoder, ok := conn.(ptySignalCoder)
	if !ok {
		return ""
	}
	signal, ok := signalCoder.ExitSignal()
	if !ok {
		return ""
	}
	return signal
}

func shouldLogTerminalSessionError(err error, exitCode int, stopRequested bool) bool {
	if stopRequested || err == nil || errors.Is(err, io.EOF) ||
		errors.Is(err, context.Canceled) || errors.Is(err, os.ErrClosed) {
		return false
	}
	// A known zero exit code means the PTY read error was only the platform's
	// end-of-stream signal (for example EIO on Unix).
	return exitCode != 0
}

func logTerminalSessionError(id string, err error) {
	if err != nil {
		slog.Warn("terminal session error", "id", id, "error", err)
	}
}

var ErrTerminalNotRunning = errTerminalNotRunning{}

type errTerminalNotRunning struct{}

func (errTerminalNotRunning) Error() string { return "terminal not running" }
