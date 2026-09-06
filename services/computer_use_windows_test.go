//go:build windows

package services

import (
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsMouseMoveHitsDedicatedWindow(t *testing.T) {
	className, err := syscall.UTF16PtrFromString("KoyoriCUTestClass")
	if err != nil {
		t.Fatal(err)
	}
	windowName, err := syscall.UTF16PtrFromString("Koyori Computer Use Test")
	if err != nil {
		t.Fatal(err)
	}
	wndProc := syscall.NewCallback(func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
		if msg == 0x0010 { // WM_CLOSE
			procDestroy := modUser32.NewProc("DestroyWindow")
			procDestroy.Call(hwnd)
			return 0
		}
		ret, _, _ := modUser32.NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), wParam, lParam)
		return ret
	})
	class := struct {
		Size       uint32
		Style      uint32
		WndProc    uintptr
		ClsExtra   int32
		WndExtra   int32
		Instance   windows.Handle
		Icon       windows.Handle
		Cursor     windows.Handle
		Background windows.Handle
		MenuName   *uint16
		ClassName  *uint16
		IconSm     windows.Handle
	}{
		WndProc:   wndProc,
		ClassName: className,
	}
	class.Size = uint32(unsafe.Sizeof(class))
	atom, _, err := modUser32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		t.Fatalf("RegisterClassExW: %v", err)
	}
	hwnd, _, err := modUser32.NewProc("CreateWindowExW").Call(
		0,
		atom,
		uintptr(unsafe.Pointer(windowName)),
		0x00CF0000, // WS_OVERLAPPEDWINDOW
		100, 100, 240, 180,
		0, 0, 0, 0,
	)
	if hwnd == 0 {
		t.Fatalf("CreateWindowExW: %v", err)
	}
	t.Cleanup(func() {
		modUser32.NewProc("DestroyWindow").Call(hwnd)
	})
	modUser32.NewProc("ShowWindow").Call(hwnd, 5) // SW_SHOW
	var rc winRECT
	if r1, _, err := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc))); r1 == 0 {
		t.Fatalf("GetWindowRect: %v", err)
	}
	x := int(rc.Left + (rc.Right-rc.Left)/2)
	y := int(rc.Top + (rc.Bottom-rc.Top)/2)
	exec := &windowsExecutor{}
	if err := exec.MouseMove(x, y); err != nil {
		t.Fatalf("MouseMove: %v", err)
	}
	var pt struct{ X, Y int32 }
	if r1, _, err := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); r1 == 0 {
		t.Fatalf("GetCursorPos: %v", err)
	}
	if int(pt.X) < int(rc.Left) || int(pt.X) > int(rc.Right) || int(pt.Y) < int(rc.Top) || int(pt.Y) > int(rc.Bottom) {
		t.Fatalf("cursor (%d,%d) left dedicated window %v", pt.X, pt.Y, rc)
	}
}
