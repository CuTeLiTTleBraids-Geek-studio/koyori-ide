package services

import (
	"context"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
)

// isCmdShim reports whether name is a Windows .cmd/.bat shim that
// CreateProcess cannot launch directly (npx.cmd, npm.cmd, jest.cmd...).
func isCmdShim(name string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat")
}

// command is like exec.Command but hides the child console window on Windows
// so GUI launches do not flash black CMD windows for version probes, git, etc.
// G15: .cmd/.bat shims (npx, npm, jest.cmd) are executed through cmd.exe /c —
// Windows CreateProcess cannot launch script shims directly.
func command(name string, arg ...string) *exec.Cmd {
	if isCmdShim(name) {
		cmd := exec.Command("cmd.exe")
		hideConsoleWindow(cmd)
		setCmdLine(cmd, "cmd.exe "+cmdShimLine(name, arg...))
		return cmd
	}
	cmd := exec.Command(name, arg...)
	hideConsoleWindow(cmd)
	return cmd
}

// commandContext is like exec.CommandContext with Windows console suppression.
// The caller's context is preserved for .cmd shims too, so timeout/cancel
// keeps working (G15 cancel path).
func commandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	if isCmdShim(name) {
		cmd := exec.CommandContext(ctx, "cmd.exe")
		hideConsoleWindow(cmd)
		setCmdLine(cmd, "cmd.exe "+cmdShimLine(name, arg...))
		return cmd
	}
	cmd := exec.CommandContext(ctx, name, arg...)
	hideConsoleWindow(cmd)
	return cmd
}

// cmdShimLine returns the command line consumed by cmd.exe itself. Keeping
// this in CmdLine avoids os/exec quoting the /c payload as a second argv list.
func cmdShimLine(name string, arg ...string) string {
	parts := make([]string, 0, len(arg)+1)
	parts = append(parts, escapeCmdMeta(name))
	for _, value := range arg {
		parts = append(parts, escapeCmdArg(value))
	}
	// /s strips the first and last quote around the /c command. The nested
	// quotes are therefore retained for the executable and its arguments.
	return `/d /v:off /s /c "` + strings.Join(parts, " ") + `"`
}

// escapeCmdArg quotes one token for a cmd.exe /c command that invokes a batch
// shim. The first cmd parser consumes one caret layer and the batch shim's
// argument expansion consumes the second. /v:off additionally keeps ! literal.
func escapeCmdArg(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 4)
	b.WriteByte('"')
	for i := 0; i < len(value); {
		start := i
		for i < len(value) && value[i] == '\\' {
			i++
		}
		slashes := i - start
		if i == len(value) {
			b.WriteString(strings.Repeat("\\", slashes*2))
			break
		}
		if value[i] == '"' {
			b.WriteString(strings.Repeat("\\", slashes*2+1))
			b.WriteByte('"')
		} else {
			b.WriteString(strings.Repeat("\\", slashes))
			b.WriteByte(value[i])
		}
		i++
	}
	b.WriteByte('"')
	return escapeCmdMeta(escapeCmdMeta(b.String()))
}

func escapeCmdMeta(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		if strings.ContainsRune(`()[]%!^"`+"`"+`<>&|;, *?`, rune(value[i])) {
			b.WriteByte('^')
		}
		b.WriteByte(value[i])
	}
	return b.String()
}

// setCmdLine uses reflection because syscall.SysProcAttr has different fields
// on different operating systems. On Windows the exported CmdLine field is
// consumed by os/exec; on other systems this is a harmless no-op.
func setCmdLine(cmd *exec.Cmd, line string) {
	if cmd == nil || cmd.SysProcAttr == nil {
		return
	}
	attr := reflect.ValueOf(cmd.SysProcAttr)
	if attr.Kind() != reflect.Ptr || attr.IsNil() {
		return
	}
	field := attr.Elem().FieldByName("CmdLine")
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.String {
		field.SetString(line)
	}
}
