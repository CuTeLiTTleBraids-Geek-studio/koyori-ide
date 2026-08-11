//go:build windows

package services

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW prevents Windows from allocating a console for child
// processes. Combined with HideWindow this stops the black CMD flash when
// a GUI (-H=windowsgui) parent runs short-lived console tools (go, node, git…).
const createNoWindow = 0x08000000

// hideConsoleWindow suppresses the brief console window that Windows shows
// when a GUI app spawns a console-subsystem child via os/exec.
func hideConsoleWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
