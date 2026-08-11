//go:build windows

package services

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

const createNewProcessGroup = 0x00000200

func configureCoverageProcessTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
}

func terminateCoverageProcessTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	// taskkill is invoked directly with fixed argv; no shell parses the PID.
	kill := command("taskkill.exe", "/PID", strconv.Itoa(process.Pid), "/T", "/F")
	if err := kill.Run(); err == nil {
		return nil
	}
	err := process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
