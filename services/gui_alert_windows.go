//go:build windows

package services

import (
	"syscall"
	"unsafe"
)

// ShowStartupError shows a blocking MessageBox so GUI builds (no console)
// still surface fatal startup errors (e.g. single-instance lock).
func ShowStartupError(title, message string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	msgBox := user32.NewProc("MessageBoxW")
	const mbOK = 0x0
	const mbIconError = 0x10
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(message)
	msgBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbOK|mbIconError)
}
