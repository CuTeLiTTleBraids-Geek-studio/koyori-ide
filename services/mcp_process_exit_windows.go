//go:build windows

package services

import "os"

func mcpProcessExitMatchesKill(state *os.ProcessState) bool {
	// os.Process.Kill uses TerminateProcess with exit code 1 on Windows.
	return state != nil && state.ExitCode() == 1
}
