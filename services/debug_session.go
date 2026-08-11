package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

type debugSessionResources struct {
	conn        net.Conn
	cdp         *nodeCDPClient
	cmd         *exec.Cmd
	browser     *browserLaunch
	readerWG    *sync.WaitGroup
	processDone chan struct{}
}

// cleanupLocked detaches per-session resources and clears in-memory state.
// The caller holds s.mu and must close the returned resources after unlocking.
func (s *DebugSession) cleanupLocked() debugSessionResources {
	resources := debugSessionResources{
		conn:        s.conn,
		cdp:         s.cdp,
		cmd:         s.cmd,
		browser:     s.browserLaunch,
		readerWG:    s.readerWG,
		processDone: s.processDone,
	}
	s.runGeneration++
	s.debugThreadsRunID = ""
	s.touchDebugThreadsStateLocked()
	s.browserTargetEpoch++
	s.conn = nil
	s.cdp = nil
	s.browserLaunch = nil
	s.browserConfig = browserDebugSpec{}
	s.browserTargets = nil
	s.browserTargetID = ""
	s.browserConsole = nil
	s.browserNetwork = nil
	s.cmd = nil
	s.running = false
	s.addr = ""
	s.mode = ""
	s.started = time.Time{}
	s.readerDone = nil
	s.readerDoneOnce = nil
	s.readerWG = nil
	s.dapInitialized = nil
	s.dapInitializedOnce = nil
	s.processDone = nil
	s.processDoneOnce = nil
	s.cwd = ""
	s.stopped = false
	s.threadID = 0
	s.stopReason = ""
	s.stack = nil
	s.stackTotalFrames = 0
	s.stackHasMore = false
	s.supportsDelayedStackTraceLoading = false
	s.supportsAsyncStackTrace = false
	s.asyncStackRootID = ""
	s.asyncStackCounter = 0
	s.asyncStackContinuations = make(map[string]nodeAsyncStackContinuation)
	s.locals = nil
	s.watchValues = nil
	s.lastError = ""
	for _, ch := range s.pending {
		close(ch)
	}
	s.pending = make(map[int]chan dapMessage)
	return resources
}

func (s *DebugSession) waitForProcessExit(cmd *exec.Cmd, generation uint64) {
	s.mu.Lock()
	done := s.processDone
	doneOnce := s.processDoneOnce
	s.mu.Unlock()
	s.waitForProcessExitTracked(cmd, generation, done, doneOnce)
}

func (s *DebugSession) waitForProcessExitTracked(cmd *exec.Cmd, generation uint64, done chan struct{}, doneOnce *sync.Once) {
	if done != nil && doneOnce != nil {
		defer doneOnce.Do(func() { close(done) })
	}
	waitErr := cmd.Wait()
	s.mu.Lock()
	var resources debugSessionResources
	if s.runGeneration == generation && s.cmd == cmd {
		s.running = false
		resources = s.cleanupLocked()
	}
	s.mu.Unlock()
	resources.cmd = nil // Wait above already reaped this process.
	if err := s.closeDetachedResources(resources, false, false, false); err != nil {
		slog.Debug("debug: cleanup failed after process exit", "err", err)
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			slog.Debug("debug: wait for process failed", "err", waitErr)
		}
	}
}

// stop fully stops a DebugSession (close conn, send best-effort disconnect,
// kill child process). Operates on the session it is called on, regardless of
// whether it is the active session (prompt-5 multi-session).
func (s *DebugSession) stop() error {
	s.mu.Lock()
	s.running = false
	resources := s.cleanupLocked()
	s.mu.Unlock()
	closeErr := s.closeDetachedResources(resources, true, true, true)
	if resources.readerWG != nil {
		resources.readerWG.Wait()
	}
	if resources.cmd != nil && resources.processDone != nil {
		<-resources.processDone
	}
	return closeErr
}

func (s *DebugSession) stopAndDispose() error {
	err := s.stop()
	s.mu.Lock()
	s.breakpoints = nil
	s.functionBreakpoints = nil
	s.watches = nil
	s.watchValues = nil
	s.stack = nil
	s.locals = nil
	s.asyncStackContinuations = nil
	s.browserConsole = nil
	s.browserNetwork = nil
	s.mu.Unlock()
	return err
}

func (s *DebugSession) closeDetachedResources(
	resources debugSessionResources,
	graceful bool,
	killProcess bool,
	stopBrowser bool,
) error {
	var errs []error
	if resources.conn != nil {
		// Only send disconnect when this session owns the adapter process.
		// Attach sessions must not ask an external server to terminate, and a
		// fire-and-forget request followed by Close can race its response write.
		if graceful && resources.cmd != nil {
			if err := resources.conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
				slog.Debug("debug: set disconnect deadline failed", "err", err)
			}
			if err := s.sendRequestUnlocked(resources.conn, "disconnect", map[string]interface{}{"restart": false}); err != nil {
				// DAP disconnect is advisory. Continue with Close/Kill so a stuck or
				// already-dead adapter cannot prevent resource reclamation.
				slog.Debug("debug: send disconnect failed", "err", err)
			}
		}
		if err := resources.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("close debug connection: %w", err))
		}
	}
	if resources.cdp != nil {
		if err := resources.cdp.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close cdp connection: %w", err))
		}
	}
	if killProcess && resources.cmd != nil && resources.cmd.Process != nil {
		if err := resources.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			errs = append(errs, fmt.Errorf("kill debug process: %w", err))
		}
	}
	if stopBrowser && resources.browser != nil {
		ctx, cancel := context.WithTimeout(context.Background(), browserStopTimeout)
		if err := resources.browser.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stop browser process: %w", err))
		}
		cancel()
	}
	return errors.Join(errs...)
}

// Stop terminates the active DAP/CDP session and child process (legacy compat).
func (d *DebugService) Stop() error {
	d.sessionsMu.Lock()
	sess := d.DebugSession
	d.sessionsMu.Unlock()
	if sess != nil {
		return sess.stop()
	}
	return nil
}

// Shutdown stops and disposes every DAP/CDP session. It is idempotent and
// rejects new multi-session starts before detaching existing resources.
func (d *DebugService) Shutdown() error {
	d.sessionsMu.Lock()
	if d.closed {
		d.sessionsMu.Unlock()
		return nil
	}
	d.closed = true
	sessions := make([]*DebugSession, 0, len(d.sessions))
	for _, session := range d.sessions {
		sessions = append(sessions, session)
	}
	d.sessions = make(map[string]*DebugSession)
	d.DebugSession = nil
	d.activeSessionID = ""
	d.sessionsMu.Unlock()

	var errs []error
	for _, session := range sessions {
		if err := session.stopAndDispose(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
