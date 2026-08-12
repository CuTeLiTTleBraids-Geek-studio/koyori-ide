//go:build !windows

package services

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

type unixPty struct {
	ptmx      *os.File
	cmd       *exec.Cmd
	closeOnce sync.Once
	closeErr  error
}

func (u *unixPty) Read(p []byte) (int, error)  { return u.ptmx.Read(p) }
func (u *unixPty) Write(p []byte) (int, error) { return u.ptmx.Write(p) }
func (u *unixPty) Resize(cols, rows int) error {
	return pty.Setsize(u.ptmx, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
}
func (u *unixPty) Close() error {
	u.closeOnce.Do(func() {
		u.closeErr = u.close()
	})
	return u.closeErr
}

func (u *unixPty) ExitCode() (int, bool) {
	if u.cmd == nil || u.cmd.ProcessState == nil {
		return -1, false
	}
	return u.cmd.ProcessState.ExitCode(), true
}

func (u *unixPty) ExitSignal() (string, bool) {
	if u.cmd == nil || u.cmd.ProcessState == nil {
		return "", false
	}
	status, ok := u.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return "", false
	}
	return status.Signal().String(), true
}

func (u *unixPty) close() error {
	var closeErr error
	if u.ptmx != nil {
		if err := u.ptmx.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			closeErr = errors.Join(closeErr, fmt.Errorf("close pty: %w", err))
		}
	}
	if u.cmd != nil && u.cmd.Process != nil {
		if err := u.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			closeErr = errors.Join(closeErr, fmt.Errorf("kill pty process: %w", err))
		}
		// Wait always runs after Kill to reap the child. Signal termination is
		// reported as an ExitError and must remain visible to the caller.
		if err := u.cmd.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			closeErr = errors.Join(closeErr, fmt.Errorf("wait for pty process: %w", err))
		}
	}
	return closeErr
}

// defaultPtyWinsize is the initial PTY geometry used before the renderer's
// first resize arrives. TUI applications (yazi, vim, htop) size their alt
// buffer from the winsize at spawn time; starting at 0x0 makes them render a
// blank screen. A conservative 80x24 matches xterm's own default.
var defaultPtyWinsize = pty.Winsize{
	Cols: 80,
	Rows: 24,
}

func startPty(shell []string, workingDir string) (io.ReadWriteCloser, error) {
	cmd := exec.Command(shell[0], shell[1:]...)
	cmd.Dir = workingDir
	// TERM must be a real terminal capability set. TUI apps (yazi, vim, htop)
	// query terminal capabilities (e.g. yazi's Terminal Response / DSR queries)
	// and refuse to render when TERM is unset or "dumb". Inherit the host TERM
	// when it looks usable, otherwise fall back to a widely-supported value.
	if term := os.Getenv("TERM"); term != "" && term != "dumb" {
		cmd.Env = append(os.Environ(), "TERM="+term)
	} else {
		cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	}
	ptmx, err := pty.StartWithSize(cmd, &defaultPtyWinsize)
	if err != nil {
		return nil, err
	}
	return &unixPty{ptmx: ptmx, cmd: cmd}, nil
}
