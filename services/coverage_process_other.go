//go:build !windows

package services

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureCoverageProcessTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func terminateCoverageProcessTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	err := syscall.Kill(-process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		fallback := process.Kill()
		if errors.Is(fallback, os.ErrProcessDone) {
			return nil
		}
		return fallback
	}
	return nil
}
