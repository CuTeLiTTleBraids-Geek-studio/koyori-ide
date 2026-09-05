//go:build windows

package services

import (
	"path/filepath"
	"strings"
)

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

// resolveShellCommand returns the argv for launching a whitelisted shell.
// BUG1: cmd.exe is launched raw via ConPTY and stays on the OEM code page
// (e.g. 936/GBK on Chinese Windows), so its CJK/box-drawing output mojibakes
// and renders with a different font than the user's UTF-8 input. Mirror the
// PowerShell default (BUG5) by switching cmd to the UTF-8 code page first;
// /K keeps the interactive prompt open after chcp runs.
func resolveShellCommand(shell string) []string {
	base := strings.ToLower(filepath.Base(shell))
	base = strings.TrimSuffix(base, ".exe")
	if base == "cmd" {
		return []string{"cmd.exe", "/Q", "/D", "/K", "chcp 65001 >nul"}
	}
	return []string{shell}
}
