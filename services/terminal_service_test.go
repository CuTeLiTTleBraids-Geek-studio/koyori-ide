package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// skipIfNoConsole skips tests that require a real PTY/ConPTY (N-6).
// On Windows, CreatePseudoConsole returns E_HANDLE (0x80070006) when
// stdout is redirected (e.g. by test runners or CI), because ConPTY
// needs a real console host. On Unix, PTY creation can also fail in
// headless environments. We detect this by checking (1) the CI env
// var and (2) whether stdout is a character device.
func skipIfNoConsole(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Skip("skipping PTY/ConPTY test in CI environment (N-6)")
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		t.Skip("skipping PTY/ConPTY test: cannot stat stdout (N-6)")
	}
	if (fi.Mode() & os.ModeCharDevice) == 0 {
		t.Skip("skipping PTY/ConPTY test: stdout is not a console (N-6)")
	}
}

func TestTerminalService_StartAndRead(t *testing.T) {
	skipIfNoConsole(t)
	ts := NewTerminalService()
	defer ts.Kill()
	if err := ts.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	if err := ts.Start(""); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if ts.IsRunning() != true {
		t.Error("expected IsRunning() to be true after Start")
	}

	// Send a command and wait for output
	ts.Write("echo hello_pty\n")

	// Poll for the expected output — PowerShell may emit its banner first
	// and the echo output arrives a bit later.
	deadline := time.Now().Add(5 * time.Second)
	var output string
	for time.Now().Before(deadline) {
		output += ts.ReadOutput(500 * time.Millisecond)
		if strings.Contains(output, "hello_pty") {
			break
		}
	}
	if !strings.Contains(output, "hello_pty") {
		t.Errorf("expected output to contain 'hello_pty', got: %q", output)
	}
}

func TestTerminalService_Kill(t *testing.T) {
	skipIfNoConsole(t)
	ts := NewTerminalService()
	if err := ts.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := ts.Start(""); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	ts.Kill()

	if ts.IsRunning() != false {
		t.Error("expected IsRunning() to be false after Kill")
	}
}

func TestTerminalService_WriteWhenNotRunning(t *testing.T) {
	ts := NewTerminalService()
	err := ts.Write("test")
	if err == nil {
		t.Error("expected error when writing to non-running terminal")
	}
}

func TestTerminalService_Resize(t *testing.T) {
	skipIfNoConsole(t)
	ts := NewTerminalService()
	defer ts.Kill()
	if err := ts.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	if err := ts.Start(""); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	err := ts.Resize(80, 24)
	if err != nil {
		t.Errorf("Resize failed: %v", err)
	}
}

func TestTerminalService_ResizeWhenNotRunning(t *testing.T) {
	ts := NewTerminalService()
	err := ts.Resize(80, 24)
	if err == nil {
		t.Error("expected error when resizing non-running terminal")
	}
}

func TestTerminalService_StartWithInvalidWorkingDir(t *testing.T) {
	ts := NewTerminalService()
	defer ts.Kill()
	if err := ts.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	err := ts.Start("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("expected error for non-existent working directory")
	}
}

func TestTerminalService_StartWithFileAsWorkingDir(t *testing.T) {
	ts := NewTerminalService()
	defer ts.Kill()
	if err := ts.setWorkspaceRoot("."); err != nil {
		t.Fatal(err)
	}
	// Pass a file path instead of a directory — should fail.
	err := ts.Start("terminal_service.go")
	if err == nil {
		t.Error("expected error when working directory is a file")
	}
}

func TestTerminalService_ValidateWorkingDir_RejectsOutsideWorkspace(t *testing.T) {
	svc := NewTerminalService()
	if err := svc.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	err := svc.validateWorkingDir(t.TempDir())
	if err == nil {
		t.Fatal("expected error for workingDir outside workspace")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("error should mention 'outside', got: %v", err)
	}
}

func TestTerminalService_ValidateWorkingDir_AcceptsInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	subDir := filepath.Join(workspace, "subdir")
	os.MkdirAll(subDir, 0755)

	svc := NewTerminalService()
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatal(err)
	}
	err := svc.validateWorkingDir(subDir)
	if err != nil {
		t.Fatalf("expected no error for path inside workspace, got: %v", err)
	}
}

func TestTerminalService_ValidateWorkingDir_NoRootRejects(t *testing.T) {
	svc := NewTerminalService()
	err := svc.validateWorkingDir(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected missing workspace root error, got: %v", err)
	}
}

func TestTerminalService_EmptyWorkspaceRootRemainsFailClosed(t *testing.T) {
	svc := NewTerminalService()
	if err := svc.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := svc.setWorkspaceRoot(""); err != nil {
		t.Fatal(err)
	}
	if err := svc.validateWorkingDir(t.TempDir()); err == nil {
		t.Fatal("clearing the workspace root relaxed terminal path validation")
	}
}

func TestTerminalService_ValidateWorkingDir_EmptyAllowed(t *testing.T) {
	svc := NewTerminalService()
	if err := svc.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	err := svc.validateWorkingDir("")
	if err != nil {
		t.Fatalf("expected no error for empty workingDir, got: %v", err)
	}
}

func TestTerminalService_MultiSession(t *testing.T) {
	skipIfNoConsole(t)
	ts := NewTerminalService()
	if err := ts.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer ts.KillSession("s1")
	defer ts.KillSession("s2")

	if err := ts.StartSession("s1", "", ""); err != nil {
		t.Fatalf("StartSession s1 failed: %v", err)
	}
	if err := ts.StartSession("s2", "", ""); err != nil {
		t.Fatalf("StartSession s2 failed: %v", err)
	}

	sessions := ts.ListSessions()
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}

	if !ts.IsSessionRunning("s1") || !ts.IsSessionRunning("s2") {
		t.Error("expected both sessions to be running")
	}

	// Kill one session
	if err := ts.KillSession("s1"); err != nil {
		t.Fatalf("KillSession s1 failed: %v", err)
	}

	if ts.IsSessionRunning("s1") {
		t.Error("expected s1 to not be running after kill")
	}
	if !ts.IsSessionRunning("s2") {
		t.Error("expected s2 to still be running")
	}

	sessions = ts.ListSessions()
	if len(sessions) != 1 {
		t.Errorf("expected 1 session after kill, got %d", len(sessions))
	}
}

func TestTerminalService_DuplicateSession(t *testing.T) {
	skipIfNoConsole(t)
	ts := NewTerminalService()
	if err := ts.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer ts.KillSession("dup")

	if err := ts.StartSession("dup", "", ""); err != nil {
		t.Fatalf("first StartSession failed: %v", err)
	}
	err := ts.StartSession("dup", "", "")
	if err == nil {
		t.Error("expected error starting duplicate session")
	}
}

func TestTerminalService_EmptySessionID(t *testing.T) {
	ts := NewTerminalService()
	if err := ts.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	err := ts.StartSession("", "", "")
	if err == nil {
		t.Error("expected error for empty session ID")
	}
}

func TestTerminalService_WriteSessionWhenNotRunning(t *testing.T) {
	ts := NewTerminalService()
	err := ts.WriteSession("nonexistent", "test")
	if err == nil {
		t.Error("expected error when writing to non-existent session")
	}
}

func TestTerminalService_ResizeSessionWhenNotRunning(t *testing.T) {
	ts := NewTerminalService()
	err := ts.ResizeSession("nonexistent", 80, 24)
	if err == nil {
		t.Error("expected error when resizing non-existent session")
	}
}

func TestTerminalService_KillSessionNotFound(t *testing.T) {
	ts := NewTerminalService()
	err := ts.KillSession("nonexistent")
	if err == nil {
		t.Error("expected error when killing non-existent session")
	}
}

// --- HIGH-01: shell whitelist ---

// TestIsAllowedShell verifies that isAllowedShell accepts whitelisted shells
// (case-insensitive, with/without .exe, with/without path) and rejects
// non-whitelisted binaries (fish, tcsh, malicious paths).
func TestIsAllowedShell(t *testing.T) {
	accepted := []string{
		"bash", "sh", "zsh", "powershell", "pwsh", "cmd", "wsl",
		"BASH", "PowerShell", "PWSH",
		"bash.exe", "CMD.EXE", "powershell.exe",
		"/usr/bin/bash", "/bin/sh", "/usr/local/bin/zsh",
	}
	// Windows absolute paths are only valid on Windows — the isAllowedShell
	// implementation uses filepath.Base which treats backslash as a separator
	// only on Windows.
	if runtime.GOOS == "windows" {
		accepted = append(accepted,
			`C:\Windows\System32\cmd.exe`,
			`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		)
	}
	for _, s := range accepted {
		if !isAllowedShell(s) {
			t.Errorf("isAllowedShell(%q) = false, want true (HIGH-01 whitelist)", s)
		}
	}
	rejected := []string{
		"fish", "tcsh", "csh", "ksh",
		"bash2", "powershell2",
		"/tmp/malicious", "./evil",
		"", "   ",
	}
	for _, s := range rejected {
		if isAllowedShell(s) {
			t.Errorf("isAllowedShell(%q) = true, want false (HIGH-01 whitelist)", s)
		}
	}
}

// TestTerminalService_StartSession_RejectsNonWhitelistedShell verifies that
// StartSession rejects a shell not in the whitelist BEFORE attempting to
// create a PTY. This test does not require a real console (the rejection
// happens before startPty).
func TestTerminalService_StartSession_RejectsNonWhitelistedShell(t *testing.T) {
	ts := NewTerminalService()
	if err := ts.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	err := ts.StartSession("session-high01", "", "fish")
	if err == nil {
		t.Fatal("expected error for non-whitelisted shell 'fish', got nil (HIGH-01)")
	}
	if !strings.Contains(err.Error(), "allowed list") {
		t.Errorf("expected 'allowed list' error, got: %v", err)
	}
}

// --- N-TERM-UTF8: incomplete UTF-8 tail detection ---

func TestIncompleteUTF8TailLen(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "hello", 0},
		{"ascii after rune", "中文x", 0},
		{"complete 2-byte", "caf\xc3\xa9", 0},      // café (é complete)
		{"complete 3-byte", "\xe4\xb8\xad", 0},     // 中 complete
		{"complete 4-byte", "\xf0\x9f\x98\x80", 0}, // 😀 complete
		{"complete 3-byte then ascii", "\xe4\xb8\xad!", 0},
		{"lead only 3-byte", "\xe4", 1},
		{"2 of 3 bytes", "\xe4\xb8", 2},
		{"lead only 2-byte", "\xc3", 1},
		{"3 of 4 bytes", "\xf0\x9f\x98", 3},
		{"ascii then partial", "a\xe6\x96", 2},
		{"ascii then lead only", "a\xf0\x9f", 2},
		{"stray continuation bytes", "\x80\x80", 0},
		// 一 (complete) + stray BD: invalid UTF-8 — forwarding as-is is
		// acceptable; there is no rune boundary to preserve.
		{"complete then stray continuation", "\xe4\xb8\x80\xbd", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := incompleteUTF8TailLen([]byte(tc.in)); got != tc.want {
				t.Errorf("incompleteUTF8TailLen(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestIncompleteUTF8TailLen_RoundTripSplitRune verifies that a multi-byte
// rune split across two 4096-byte-style chunks is reassembled byte-exactly
// when the tail-holding logic is applied (simulating two consecutive PTY
// reads). The reassembled sequence must be valid UTF-8 with no U+FFFD.
func TestIncompleteUTF8TailLen_RoundTripSplitRune(t *testing.T) {
	full := []byte("中文😀输出 test")
	// Split at every byte boundary: each split must reassemble to the
	// original bytes with no loss and no replacement characters.
	for split := 1; split < len(full); split++ {
		first := full[:split]
		second := full[split:]

		tail := incompleteUTF8TailLen(first)
		if !utf8.Valid(first[:len(first)-tail]) {
			t.Fatalf("split=%d: forwarded prefix is not valid UTF-8", split)
		}
		held := first[len(first)-tail:]
		combined := append(append([]byte{}, held...), second...)
		reassembled := append(append([]byte{}, first[:len(first)-tail]...), combined...)
		if string(reassembled) != string(full) {
			t.Errorf("split=%d: reassembled %q != original %q", split, reassembled, full)
		}
		if !utf8.Valid(reassembled) {
			t.Errorf("split=%d: reassembled bytes are not valid UTF-8", split)
		}
	}
}
