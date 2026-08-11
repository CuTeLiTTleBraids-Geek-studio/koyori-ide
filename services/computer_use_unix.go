//go:build !windows

package services

// Plan 11 Task 6 — Unix（Linux/macOS）平台的 Computer Use 原生操作 stub。
//
// 返回 ErrPlatformUnsupported。Linux 截图需要 X11/Wayland 绑定，
// macOS 需要 CGO + AppKit，均超出当前范围。安全模型（checkSafety /
// 审计日志）在 computer_use_service.go 中平台无关地实现。

import (
	"fmt"
	"image"
)

// unixExecutor 是 Unix 平台的操作执行器 stub。
type unixExecutor struct{}

func (u *unixExecutor) Screenshot(region *image.Rectangle) ([]byte, error) {
	return nil, fmt.Errorf("unix screenshot not yet implemented: %w", ErrPlatformUnsupported)
}

func (u *unixExecutor) MouseMove(x, y int) error {
	return fmt.Errorf("unix mouse_move not yet implemented: %w", ErrPlatformUnsupported)
}

func (u *unixExecutor) MouseClick(button string) error {
	return fmt.Errorf("unix mouse_click not yet implemented: %w", ErrPlatformUnsupported)
}

func (u *unixExecutor) KeyboardType(text string) error {
	return fmt.Errorf("unix keyboard_type not yet implemented: %w", ErrPlatformUnsupported)
}

func (u *unixExecutor) KeyboardHotkey(keys string) error {
	return fmt.Errorf("unix keyboard_hotkey not yet implemented: %w", ErrPlatformUnsupported)
}

// newPlatformExecutor 返回 Unix 平台执行器。
func newPlatformExecutor() platformExecutor {
	return &unixExecutor{}
}
