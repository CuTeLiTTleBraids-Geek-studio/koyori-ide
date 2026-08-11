//go:build !windows

package services

import (
	"testing"
	"time"
)

func TestUnixPty_RealSignalExit(t *testing.T) {
	conn, err := startPty([]string{"sh", "-c", "kill -TERM $$"}, t.TempDir())
	if err != nil {
		t.Fatalf("startPty: %v", err)
	}

	buf := make([]byte, 256)
	for {
		if _, readErr := conn.Read(buf); readErr != nil {
			// Linux PTYs commonly report EIO after the child exits; the
			// process state is the assertion relevant to this regression.
			break
		}
	}
	if closeErr := conn.Close(); closeErr != nil {
		// Wait returns an ExitError when the child was terminated by a
		// signal; that is the expected result under test, not a PTY leak.
		t.Logf("close PTY returned expected signal wait error: %v", closeErr)
	}

	signalCoder, ok := conn.(ptySignalCoder)
	if !ok {
		t.Fatal("unix PTY does not expose signal exit information")
	}
	signal, known := signalCoder.ExitSignal()
	if !known || signal != "terminated" {
		t.Fatalf("signal = %q, known = %v, want terminated/true", signal, known)
	}
}

func TestTerminalService_UnixDefaultAndCustomShell(t *testing.T) {
	for _, shell := range []string{"", "sh"} {
		shell := shell
		name := "custom"
		if shell == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			service := NewTerminalService()
			defer service.Shutdown()
			if err := service.setWorkspaceRoot(t.TempDir()); err != nil {
				t.Fatalf("setWorkspaceRoot: %v", err)
			}

			id := "unix-shell"
			if err := service.StartSession(id, "", shell); err != nil {
				t.Fatalf("StartSession(%q): %v", shell, err)
			}
			if !service.IsSessionRunning(id) {
				t.Fatal("session is not running after StartSession")
			}
			if err := service.WriteSession(id, "exit 0\n"); err != nil {
				t.Fatalf("WriteSession: %v", err)
			}

			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) && service.IsSessionRunning(id) {
				time.Sleep(20 * time.Millisecond)
			}
			if service.IsSessionRunning(id) {
				t.Fatal("session did not exit after exit 0")
			}
		})
	}
}
