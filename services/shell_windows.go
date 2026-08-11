//go:build windows

package services

// defaultShell returns the default shell command for Windows.
// BUG5: Set the console output encoding to UTF-8 so Chinese characters
// and other non-ASCII content render correctly in xterm. Without this,
// Windows uses the system code page (e.g. 936/GBK on Chinese Windows),
// causing mojibake in the terminal.
// -NoProfile avoids loading the user's PowerShell profile which may
// contain PSReadLine settings that conflict with xterm's rendering.
func defaultShell() []string {
	return []string{
		"powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-Command",
		"[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; [Console]::InputEncoding = [System.Text.Encoding]::UTF8; chcp 65001 | Out-Null; powershell -NoLogo -NoProfile",
	}
}
