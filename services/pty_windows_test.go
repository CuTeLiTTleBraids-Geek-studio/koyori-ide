//go:build windows

package services

import (
	"strings"
	"testing"
	"time"
)

// TestBuildWindowsCommandLine_High02 verifies that buildWindowsCommandLine
// uses syscall.EscapeArg to properly escape each argument, preventing
// argument injection when a path or argument contains spaces or special
// characters (HIGH-02).
func TestBuildWindowsCommandLine_High02(t *testing.T) {
	tests := []struct {
		name  string
		shell []string
		// want is the expected output; we assert structural safety
		// properties (spacing, quoting) rather than exact bytes where
		// EscapeArg's internal rules are subtle.
		check func(t *testing.T, got string)
	}{
		{
			name:  "simple args joined by space",
			shell: []string{"powershell.exe", "-NoLogo"},
			check: func(t *testing.T, got string) {
				if got != "powershell.exe -NoLogo" {
					t.Errorf("got %q, want %q", got, "powershell.exe -NoLogo")
				}
			},
		},
		{
			name:  "path with spaces is quoted",
			shell: []string{`C:\Program Files\app.exe`, "--flag"},
			check: func(t *testing.T, got string) {
				// The path with spaces MUST be quoted so it stays a single
				// argument; otherwise CreateProcess would split it.
				if !strings.HasPrefix(got, `"C:\Program Files\app.exe"`) {
					t.Errorf("path with spaces not quoted (HIGH-02 injection risk): %q", got)
				}
			},
		},
		{
			name:  "arg with spaces is quoted",
			shell: []string{"bash", "-c", "echo hello world"},
			check: func(t *testing.T, got string) {
				// "echo hello world" must be quoted so it's a single arg.
				if !strings.Contains(got, `"echo hello world"`) {
					t.Errorf("arg with spaces not quoted (HIGH-02): %q", got)
				}
			},
		},
		{
			name:  "quote injection is escaped",
			shell: []string{"bash", `foo"bar`},
			check: func(t *testing.T, got string) {
				// The embedded quote MUST be escaped (\" or backslash-quoted)
				// so it cannot terminate the argument and inject a new flag.
				if !strings.Contains(got, `\"`) {
					t.Errorf("embedded quote not escaped (HIGH-02 injection risk): %q", got)
				}
			},
		},
		{
			name:  "empty slice returns empty string",
			shell: []string{},
			check: func(t *testing.T, got string) {
				if got != "" {
					t.Errorf("got %q, want empty string", got)
				}
			},
		},
		{
			name:  "single arg no trailing space",
			shell: []string{"cmd"},
			check: func(t *testing.T, got string) {
				if got != "cmd" {
					t.Errorf("got %q, want %q", got, "cmd")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildWindowsCommandLine(tt.shell)
			tt.check(t, got)
		})
	}
}

// TestBuildWindowsCommandLine_NoRawJoin verifies that buildWindowsCommandLine
// does NOT produce the same output as a naive strings.Join for an input
// containing spaces — i.e. the escaping is actually applied. This guards
// against a regression to the old strings.Join(shell, " ") behavior.
func TestBuildWindowsCommandLine_NoRawJoin(t *testing.T) {
	shell := []string{`C:\Program Files\app.exe`, "--flag"}
	got := buildWindowsCommandLine(shell)
	naive := strings.Join(shell, " ")
	if got == naive {
		t.Errorf("buildWindowsCommandLine matched naive strings.Join (HIGH-02 regression): got %q", got)
	}
}

// TestTerminalService_G16_RealExitCodeDetected verifies the G16 exit-code
// protocol fix on real Windows ConPTY: when the child shell exits (cmd
// `exit 7`), the read loop detects the exit promptly, the session is marked
// not running, and the PTY reports the real exit code (7) — the ConPTY pipe
// no longer keeps the session alive after the child has gone.
func TestTerminalService_G16_RealExitCodeDetected(t *testing.T) {
	tv := NewTerminalService()
	dir := t.TempDir()
	if err := tv.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("setWorkspaceRoot: %v", err)
	}
	if err := tv.StartSession("g16", "", "cmd"); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer tv.KillSession("g16")
	tv.mu.Lock()
	sess := tv.sessions["g16"]
	tv.mu.Unlock()
	if sess == nil {
		t.Fatal("session not registered")
	}
	// Wait for the shell to be ready, then ask it to exit with code 7.
	time.Sleep(1200 * time.Millisecond)
	if err := tv.WriteSession("g16", "exit 7\r\n"); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !tv.IsSessionRunning("g16") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if tv.IsSessionRunning("g16") {
		t.Fatal("session did not exit after `exit 7` (ConPTY pipe kept it alive)")
	}
	coder, ok := sess.conn.(ptyExitCoder)
	if !ok {
		t.Fatalf("session conn is not a ptyExitCoder: %T", sess.conn)
	}
	code, ok2 := coder.ExitCode()
	if !ok2 {
		t.Fatal("exit code unavailable")
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
}
