//go:build windows

package services

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// processAlive reports whether a process with the given PID is running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	// STILL_ACTIVE = 259
	return code == 259
}

// processInfo 返回指定 PID 的启动时间戳（FILETIME 100 纳秒间隔，自 1601 起）
// 和可执行文件路径。用于检测 Windows PID 复用误判 (H-5)。
func processInfo(pid int) (startTime int64, name string, err error) {
	if pid <= 0 {
		return 0, "", fmt.Errorf("invalid pid: %d", pid)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, "", err
	}
	defer windows.CloseHandle(h)

	// 获取进程创建时间（FILETIME）
	var creation, exitTime, kernelTime, userTime windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exitTime, &kernelTime, &userTime); err != nil {
		return 0, "", err
	}
	// FILETIME 转为 int64（100 纳秒间隔），不同进程实例的创建时间不同
	startTime = int64(creation.HighDateTime)<<32 + int64(creation.LowDateTime)

	// 获取可执行文件路径（可选 — 启动时间已足够检测 PID 复用）
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		// 返回已获取的启动时间，进程名留空
		return startTime, "", nil
	}
	name = windows.UTF16ToString(buf[:n])
	return startTime, name, nil
}
