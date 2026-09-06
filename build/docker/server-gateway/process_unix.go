//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureBackendProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func trackBackendProcess(_ *exec.Cmd) error { return nil }

func releaseBackendProcess(_ *exec.Cmd) {}

func signalBackend(cmd *exec.Cmd) error {
	return signalBackendGroup(cmd, syscall.SIGTERM)
}

func killBackend(cmd *exec.Cmd) error {
	return signalBackendGroup(cmd, syscall.SIGKILL)
}

func signalBackendGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
