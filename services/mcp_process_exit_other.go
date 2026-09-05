//go:build !windows

package services

import (
	"os"
	"syscall"
)

func mcpProcessExitMatchesKill(state *os.ProcessState) bool {
	if state == nil {
		return false
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}
