//go:build windows

package services

// Plan 11 Task 6 — Windows 平台的 Computer Use 原生操作 stub。
//
// 当前为 stub 实现：返回 ErrPlatformUnsupported。实际的原生操作
// （GDI 截图 / user32 鼠标键盘）需要引入 golang.org/x/sys/windows
// 或 syscall 包装，留待后续完善（离线环境暂不引入新依赖）。
//
// 安全模型（checkSafety / 审计日志）在 computer_use_service.go 中
// 平台无关地实现，确保即使原生操作未接入，安全边界仍然生效。

import (
	"fmt"
	"image"
)

// windowsExecutor 是 Windows 平台的操作执行器 stub。
type windowsExecutor struct{}

func (w *windowsExecutor) Screenshot(region *image.Rectangle) ([]byte, error) {
	// TODO: 用 golang.org/x/sys/windows + gdi32 实现截图。
	// 当前返回 ErrPlatformUnsupported，安全检查仍由上层执行。
	return nil, fmt.Errorf("windows screenshot not yet implemented: %w", ErrPlatformUnsupported)
}

func (w *windowsExecutor) MouseMove(x, y int) error {
	return fmt.Errorf("windows mouse_move not yet implemented: %w", ErrPlatformUnsupported)
}

func (w *windowsExecutor) MouseClick(button string) error {
	return fmt.Errorf("windows mouse_click not yet implemented: %w", ErrPlatformUnsupported)
}

func (w *windowsExecutor) KeyboardType(text string) error {
	return fmt.Errorf("windows keyboard_type not yet implemented: %w", ErrPlatformUnsupported)
}

func (w *windowsExecutor) KeyboardHotkey(keys string) error {
	return fmt.Errorf("windows keyboard_hotkey not yet implemented: %w", ErrPlatformUnsupported)
}

// newPlatformExecutor 返回 Windows 平台执行器。
func newPlatformExecutor() platformExecutor {
	return &windowsExecutor{}
}
