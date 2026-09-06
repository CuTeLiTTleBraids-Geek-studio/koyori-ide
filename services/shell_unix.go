//go:build !windows

package services

import "os"

func defaultShell() []string {
	shell := os.Getenv("SHELL")
	// HIGH-01: validate $SHELL against the whitelist. If the user's login
	// shell is a non-whitelisted binary (e.g. fish), fall back to bash so a
	// non-whitelisted binary is never launched as a terminal session.
	if shell == "" || !isAllowedShell(shell) {
		shell = "bash"
	}
	return []string{shell}
}

// resolveShellCommand returns the argv for launching a whitelisted shell.
// On Unix the shell is launched as-is; cmd.exe is a Windows-only shell and
// gets its UTF-8 code-page wrapper in shell_windows.go (BUG1).
func resolveShellCommand(shell string) []string {
	return []string{shell}
}
