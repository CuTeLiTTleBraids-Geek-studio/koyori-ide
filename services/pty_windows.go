//go:build windows

package services

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/UserExistsError/conpty"
)

type windowsPty struct {
	cpty         *conpty.ConPty
	process      *os.Process
	mu           sync.Mutex
	processState *os.ProcessState
	// done is closed when the child process has been reaped by waitExit.
	// G16: readLoop (or Close) can wait on it; waitExit also closes the
	// ConPTY pipe so a pending Read unblocks and the exit is detected
	// promptly (ConPTY keeps the pipe open after the child exits, which
	// previously delayed/never emitted the structured exit event).
	done       chan struct{}
	doneOnce   sync.Once
	closeOnce  sync.Once
	closeErr   error
	cptyClosed bool
}

// Done is closed when the PTY child process has exited (G16 exit protocol).
func (w *windowsPty) Done() <-chan struct{} {
	return w.done
}

func (w *windowsPty) Read(p []byte) (int, error)  { return w.cpty.Read(p) }
func (w *windowsPty) Write(p []byte) (int, error) { return w.cpty.Write(p) }
func (w *windowsPty) Resize(cols, rows int) error {
	if w.cpty == nil {
		return nil
	}
	return w.cpty.Resize(cols, rows)
}

func (w *windowsPty) closeCPty() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cpty == nil || w.cptyClosed {
		return nil
	}
	w.cptyClosed = true
	return w.cpty.Close()
}

// waitExit reaps the child (the only caller of process.Wait) and, once it has
// exited, closes the ConPTY pipe so a blocked Read in readLoop returns.
func (w *windowsPty) waitExit() {
	state, err := w.process.Wait()
	w.mu.Lock()
	w.processState = state
	w.mu.Unlock()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		// Keep the error observable via Close() below.
		w.mu.Lock()
		w.closeErr = errors.Join(w.closeErr, fmt.Errorf("wait for pty process: %w", err))
		w.mu.Unlock()
	}
	w.doneOnce.Do(func() { close(w.done) })
	// Unblock a pending Read so readLoop notices the exit immediately.
	_ = w.closeCPty()
}

func (w *windowsPty) Close() error {
	w.closeOnce.Do(func() {
		if w.process != nil {
			select {
			case <-w.done:
				// process already reaped by waitExit; nothing to kill.
			default:
				if err := w.process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					w.mu.Lock()
					w.closeErr = errors.Join(w.closeErr, fmt.Errorf("kill pty process: %w", err))
					w.mu.Unlock()
				}
			}
		}
		// Closing the pseudo console also terminates attached processes and
		// closes its pipes, ensuring a concurrent Read is unblocked.
		if err := w.closeCPty(); err != nil {
			w.mu.Lock()
			w.closeErr = errors.Join(w.closeErr, fmt.Errorf("close pseudo console: %w", err))
			w.mu.Unlock()
		}
		// waitExit owns process.Wait; wait for it to finish reaping.
		select {
		case <-w.done:
		case <-time.After(3 * time.Second):
			w.mu.Lock()
			w.closeErr = errors.Join(w.closeErr, errors.New("pty process wait timed out"))
			w.mu.Unlock()
		}
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closeErr
}

func (w *windowsPty) ExitCode() (int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.processState == nil {
		return -1, false
	}
	return w.processState.ExitCode(), true
}

func startPty(shell []string, workingDir string) (io.ReadWriteCloser, error) {
	// M-3/HIGH-02: use syscall.EscapeArg to properly quote each argument.
	// The previous strings.Join(shell, " ") did not escape spaces or special
	// characters, allowing argument injection when a path contained spaces.
	commandLine := buildWindowsCommandLine(shell)
	opts := []conpty.ConPtyOption{}
	if workingDir != "" {
		opts = append(opts, conpty.ConPtyWorkDir(workingDir))
	}
	cpty, err := conpty.Start(commandLine, opts...)
	if err != nil {
		return nil, err
	}
	process, err := os.FindProcess(cpty.Pid())
	if err != nil {
		return nil, fmt.Errorf("find pty process: %w", errors.Join(err, cpty.Close()))
	}
	w := &windowsPty{cpty: cpty, process: process, done: make(chan struct{})}
	go w.waitExit()
	return w, nil
}

// buildWindowsCommandLine builds a properly escaped Windows command-line
// string from a shell argv slice using syscall.EscapeArg. Extracted from
// startPty for testability (HIGH-02): ConPTY tests are skipped in CI
// (skipIfNoConsole), so this function lets us verify the escaping logic
// directly without a real console.
func buildWindowsCommandLine(shell []string) string {
	var commandLine string
	for i, arg := range shell {
		if i > 0 {
			commandLine += " "
		}
		commandLine += syscall.EscapeArg(arg)
	}
	return commandLine
}
