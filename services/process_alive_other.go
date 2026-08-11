//go:build !windows

package services

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// processAlive reports whether a process with the given PID is running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence without killing (POSIX).
	err = p.Signal(syscall.Signal(0))
	return err == nil
}

// processInfo 返回指定 PID 的启动时间戳和可执行文件路径。
// 用于检测 PID 复用误判 (H-5)。
//
// P2-2: macOS 无 /proc 文件系统，原实现直接返回错误 → lockIsStale
// 保守判断为非过期 → PID 复用时误报"另一个实例正在运行"。现按 GOOS 分发：
//   - linux: 读 /proc/[pid]/stat 字段 22（starttime，自启动的时钟节拍数）
//   - darwin: 用 ps -o lstart= -p <pid> 解析启动时间，ps -o comm= -p <pid> 取进程名
//   - 其他 Unix: 返回错误，lockIsStale 保守判断为非过期（保持向后兼容）
func processInfo(pid int) (startTime int64, name string, err error) {
	if pid <= 0 {
		return 0, "", fmt.Errorf("invalid pid: %d", pid)
	}
	switch runtime.GOOS {
	case "darwin":
		return processInfoDarwin(pid)
	default:
		return processInfoLinux(pid)
	}
}

// processInfoLinux 通过 /proc 文件系统读取进程信息（Linux 专属）。
func processInfoLinux(pid int) (startTime int64, name string, err error) {
	// Linux: 通过 /proc/[pid]/stat 读取启动时间
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		return 0, "", err
	}
	// 解析 /proc/[pid]/stat：字段 22 是 starttime（自启动的时钟节拍数）
	// comm 字段可能包含空格和括号，用最后一个 ')' 定位
	stat := string(data)
	idx := strings.LastIndex(stat, ")")
	if idx < 0 {
		return 0, "", fmt.Errorf("parse stat: no closing paren")
	}
	fields := strings.Fields(stat[idx+1:])
	// ')' 之后字段从 3 开始，starttime 是第 22 个字段 → fields[19]
	if len(fields) < 20 {
		return 0, "", fmt.Errorf("parse stat: not enough fields")
	}
	startTime, err = strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("parse starttime: %w", err)
	}
	// 读取可执行文件路径（可选 — 启动时间已足够检测 PID 复用）
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return startTime, "", nil
	}
	return startTime, exe, nil
}

// processInfoDarwin 通过 ps 命令读取 macOS 进程信息 (P2-2)。
// macOS 无 /proc 文件系统，sysctl kern.proc.pid.<pid> 需 cgo 调用 libproc；
// 本实现选用 ps 命令解析，无需 cgo，跨版本兼容性好。
//
// 启动时间格式：ps -o lstart= -p <pid> 输出形如 "Mon Jul 15 10:23:45 2025"，
// 解析为 Unix 时间戳。进程名：ps -o comm= -p <pid> 输出可执行文件路径。
func processInfoDarwin(pid int) (startTime int64, name string, err error) {
	// 启动时间
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, "", fmt.Errorf("ps lstart: %w", err)
	}
	startTime, perr := parseDarwinStartTime(strings.TrimSpace(string(out)))
	if perr != nil {
		return 0, "", fmt.Errorf("parse lstart: %w", perr)
	}
	// 进程名（comm= 输出绝对路径，如 /Applications/koyori-ide.app/Contents/MacOS/koyori-ide）
	out, err = exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err == nil {
		name = strings.TrimSpace(string(out))
	}
	return startTime, name, nil
}

// parseDarwinStartTime 解析 ps -o lstart= 输出的时间字符串为 Unix 时间戳。
// 输入示例: "Mon Jul 15 10:23:45 2025"
// Go 参考时间: "Mon Jan 2 15:04:05 2006"
//
// P2-2: 抽取为纯函数便于测试（CI 无 macOS 时仍可验证解析逻辑）。
func parseDarwinStartTime(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty time string")
	}
	// ps 输出可能有前导空格（多列对齐），已 TrimSpace。
	// 部分系统输出形如 "Mon Jul  5 10:23:45 2025"（日号不足两位用双空格），需归一化。
	normalized := strings.Join(strings.Fields(s), " ")
	t, err := time.Parse("Mon Jan 2 15:04:05 2006", normalized)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", s, err)
	}
	return t.Unix(), nil
}
