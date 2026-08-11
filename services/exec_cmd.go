package services

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// isCmdShim reports whether name is a Windows .cmd/.bat shim that
// CreateProcess cannot launch directly (npx.cmd, npm.cmd, jest.cmd...).
func isCmdShim(name string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat")
}

// command is like exec.Command but hides the child console window on Windows
// so GUI launches do not flash black CMD windows for version probes, git, etc.
// G15: .cmd/.bat shims (npx, npm, jest.cmd) are executed through cmd.exe /c —
// Windows CreateProcess cannot launch script shims directly.
func command(name string, arg ...string) *exec.Cmd {
	if isCmdShim(name) {
		// cmd.exe /c parses the command word itself; a quoted absolute shim
		// path with spaces would be split. Pass the basename and let cmd
		// resolve it from PATH (npx/npm/jest shims all live on PATH or in the
		// project's node_modules/.bin which npx resolves).
		all := append([]string{"/c", filepath.Base(name)}, arg...)
		cmd := exec.Command("cmd.exe", all...)
		hideConsoleWindow(cmd)
		return cmd
	}
	cmd := exec.Command(name, arg...)
	hideConsoleWindow(cmd)
	return cmd
}

// commandContext is like exec.CommandContext with Windows console suppression.
// The caller's context is preserved for .cmd shims too, so timeout/cancel
// keeps working (G15 cancel path).
func commandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	if isCmdShim(name) {
		all := append([]string{"/c", filepath.Base(name)}, arg...)
		cmd := exec.CommandContext(ctx, "cmd.exe", all...)
		hideConsoleWindow(cmd)
		return cmd
	}
	cmd := exec.CommandContext(ctx, name, arg...)
	hideConsoleWindow(cmd)
	return cmd
}
