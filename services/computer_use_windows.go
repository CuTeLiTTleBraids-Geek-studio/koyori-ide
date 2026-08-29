//go:build windows

package services

// P14-G36: Windows Computer Use uses gdi32/user32 via golang.org/x/sys/windows.
// Tests bind a dedicated window and never click the real taskbar.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32 = windows.NewLazySystemDLL("user32.dll")
	modGdi32  = windows.NewLazySystemDLL("gdi32.dll")
	modKernel = windows.NewLazySystemDLL("kernel32.dll")

	procGetDesktopWindow    = modUser32.NewProc("GetDesktopWindow")
	procGetWindowRect       = modUser32.NewProc("GetWindowRect")
	procGetDC               = modUser32.NewProc("GetDC")
	procReleaseDC           = modUser32.NewProc("ReleaseDC")
	procGetCursorPos        = modUser32.NewProc("GetCursorPos")
	procSetCursorPos        = modUser32.NewProc("SetCursorPos")
	procMouseEvent          = modUser32.NewProc("mouse_event")
	procSendInput           = modUser32.NewProc("SendInput")
	procGetForegroundWindow = modUser32.NewProc("GetForegroundWindow")
	procGetWindowThreadProc = modUser32.NewProc("GetWindowThreadProcessId")
	procWindowFromPoint     = modUser32.NewProc("WindowFromPoint")

	procCreateCompatibleDC     = modGdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = modGdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = modGdi32.NewProc("SelectObject")
	procBitBlt                 = modGdi32.NewProc("BitBlt")
	procGetDIBits              = modGdi32.NewProc("GetDIBits")
	procDeleteObject           = modGdi32.NewProc("DeleteObject")
	procDeleteDC               = modGdi32.NewProc("DeleteDC")

	procOpenProcess      = modKernel.NewProc("OpenProcess")
	procQueryFullProcess = modKernel.NewProc("QueryFullProcessImageNameW")
	procCloseHandle      = modKernel.NewProc("CloseHandle")
)

const (
	srcCopy              = 0x00CC0020
	dibRGBColors         = 0
	mouseEventLeftDown   = 0x0002
	mouseEventLeftUp     = 0x0004
	mouseEventRightDown  = 0x0008
	mouseEventRightUp    = 0x0010
	mouseEventMiddleDown = 0x0020
	mouseEventMiddleUp   = 0x0040
	inputKeyboard        = 1
	keyeventfKeyup       = 0x0002
	keyeventfUnicode     = 0x0004
	processQueryLimited  = 0x1000
)

type winRECT struct {
	Left, Top, Right, Bottom int32
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type keyboardInput struct {
	Type uint32
	_    uint32
	Ki   struct {
		Vk        uint16
		Scan      uint16
		Flags     uint32
		Time      uint32
		ExtraInfo uintptr
	}
}

type windowsExecutor struct{}

func newPlatformExecutor() platformExecutor {
	return &windowsExecutor{}
}

func (w *windowsExecutor) Screenshot(region *image.Rectangle) ([]byte, error) {
	hwnd, _, _ := procGetDesktopWindow.Call()
	if hwnd == 0 {
		return nil, fmt.Errorf("GetDesktopWindow failed: %w", ErrPlatformUnsupported)
	}
	var rc winRECT
	if r1, _, err := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc))); r1 == 0 {
		return nil, fmt.Errorf("GetWindowRect: %w", err)
	}
	full := image.Rect(int(rc.Left), int(rc.Top), int(rc.Right), int(rc.Bottom))
	target := full
	if region != nil {
		target = region.Intersect(full)
	}
	if target.Empty() {
		return nil, fmt.Errorf("screenshot region is empty: %w", ErrInvalidInput)
	}
	width := target.Dx()
	height := target.Dy()
	hdc, _, _ := procGetDC.Call(hwnd)
	if hdc == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(hwnd, hdc)
	memDC, _, _ := procCreateCompatibleDC.Call(hdc)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)
	bmp, _, _ := procCreateCompatibleBitmap.Call(hdc, uintptr(width), uintptr(height))
	if bmp == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bmp)
	prev, _, _ := procSelectObject.Call(memDC, bmp)
	if prev == 0 {
		return nil, fmt.Errorf("SelectObject failed")
	}
	defer procSelectObject.Call(memDC, prev)
	if r1, _, err := procBitBlt.Call(memDC, 0, 0, uintptr(width), uintptr(height), hdc, uintptr(target.Min.X), uintptr(target.Min.Y), srcCopy); r1 == 0 {
		return nil, fmt.Errorf("BitBlt: %w", err)
	}
	header := bitmapInfoHeader{
		Size:     40,
		Width:    int32(width),
		Height:   -int32(height),
		Planes:   1,
		BitCount: 32,
	}
	buf := make([]byte, width*height*4)
	if r1, _, err := procGetDIBits.Call(memDC, bmp, 0, uintptr(height), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&header)), dibRGBColors); r1 == 0 {
		return nil, fmt.Errorf("GetDIBits: %w", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			i := (y*width + x) * 4
			img.SetRGBA(x, y, color.RGBA{R: buf[i+2], G: buf[i+1], B: buf[i], A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return nil, err
	}
	if encoded.Len() == 0 {
		return nil, fmt.Errorf("encoded screenshot is empty")
	}
	return encoded.Bytes(), nil
}

func (w *windowsExecutor) MouseMove(x, y int) error {
	if r1, _, err := procSetCursorPos.Call(uintptr(x), uintptr(y)); r1 == 0 {
		return fmt.Errorf("SetCursorPos: %w", err)
	}
	return nil
}

func (w *windowsExecutor) MouseClick(button string) error {
	down, up := mouseEventLeftDown, mouseEventLeftUp
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "", "left":
	case "right":
		down, up = mouseEventRightDown, mouseEventRightUp
	case "middle":
		down, up = mouseEventMiddleDown, mouseEventMiddleUp
	default:
		return fmt.Errorf("unsupported mouse button %q: %w", button, ErrInvalidInput)
	}
	if r1, _, err := procMouseEvent.Call(uintptr(down), 0, 0, 0, 0); r1 == 0 && err != windows.ERROR_SUCCESS {
		return fmt.Errorf("mouse down: %w", err)
	}
	if r1, _, err := procMouseEvent.Call(uintptr(up), 0, 0, 0, 0); r1 == 0 && err != windows.ERROR_SUCCESS {
		return fmt.Errorf("mouse up: %w", err)
	}
	return nil
}

func (w *windowsExecutor) KeyboardType(text string) error {
	for _, r := range text {
		if err := sendUnicode(r, false); err != nil {
			return err
		}
		if err := sendUnicode(r, true); err != nil {
			return err
		}
	}
	return nil
}

func sendUnicode(r rune, up bool) error {
	var inp keyboardInput
	inp.Type = inputKeyboard
	inp.Ki.Scan = uint16(r)
	inp.Ki.Flags = keyeventfUnicode
	if up {
		inp.Ki.Flags |= keyeventfKeyup
	}
	n, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
	if n == 0 {
		return fmt.Errorf("SendInput: %w", err)
	}
	return nil
}

func (w *windowsExecutor) KeyboardHotkey(keys string) error {
	parts := strings.Split(strings.ToLower(keys), "+")
	vks := make([]uint16, 0, len(parts))
	for _, part := range parts {
		vk, ok := virtualKey(strings.TrimSpace(part))
		if !ok {
			return fmt.Errorf("unsupported hotkey %q: %w", keys, ErrInvalidInput)
		}
		vks = append(vks, vk)
	}
	for _, vk := range vks {
		if err := sendVK(vk, false); err != nil {
			return err
		}
	}
	for i := len(vks) - 1; i >= 0; i-- {
		if err := sendVK(vks[i], true); err != nil {
			return err
		}
	}
	return nil
}

func sendVK(vk uint16, up bool) error {
	var inp keyboardInput
	inp.Type = inputKeyboard
	inp.Ki.Vk = vk
	if up {
		inp.Ki.Flags = keyeventfKeyup
	}
	n, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
	if n == 0 {
		return fmt.Errorf("SendInput: %w", err)
	}
	return nil
}

func virtualKey(name string) (uint16, bool) {
	switch name {
	case "ctrl", "control":
		return 0x11, true
	case "alt":
		return 0x12, true
	case "shift":
		return 0x10, true
	case "win", "super":
		return 0x5B, true
	case "enter", "return":
		return 0x0D, true
	case "esc", "escape":
		return 0x1B, true
	case "tab":
		return 0x09, true
	case "space":
		return 0x20, true
	default:
		if len(name) == 1 {
			ch := name[0]
			if ch >= 'a' && ch <= 'z' {
				return uint16(ch - 32), true
			}
			if ch >= '0' && ch <= '9' {
				return uint16(ch), true
			}
		}
		if strings.HasPrefix(name, "f") && len(name) <= 3 {
			var n int
			if _, err := fmt.Sscanf(name, "f%d", &n); err == nil && n >= 1 && n <= 12 {
				return uint16(0x70 + n - 1), true
			}
		}
		return 0, false
	}
}

func platformForegroundProcessName() (string, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return "", fmt.Errorf("no foreground window")
	}
	var pid uint32
	procGetWindowThreadProc.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return "", fmt.Errorf("foreground pid is 0")
	}
	handle, _, err := procOpenProcess.Call(processQueryLimited, 0, uintptr(pid))
	if handle == 0 {
		return "", fmt.Errorf("OpenProcess: %w", err)
	}
	defer procCloseHandle.Call(handle)
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	r1, _, err := procQueryFullProcess.Call(handle, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r1 == 0 {
		return "", fmt.Errorf("QueryFullProcessImageNameW: %w", err)
	}
	path := windows.UTF16ToString(buf)
	base := path
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		base = path[i+1:]
	}
	return strings.ToLower(base), nil
}
