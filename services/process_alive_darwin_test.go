//go:build !windows

package services

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// TestParseDarwinStartTime (P2-2) 验证 ps -o lstart= 输出解析为 Unix 时间戳。
// macOS 上 ps 输出形如 "Mon Jul 15 10:23:45 2025"，日号不足两位时用双空格。
// CI 无 macOS 时仍可验证纯函数解析逻辑。
func TestParseDarwinStartTime(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int64
	}{
		{
			name:  "standard",
			input: "Mon Jul 15 10:23:45 2025",
			want:  time.Date(2025, 7, 15, 10, 23, 45, 0, time.UTC).Unix(),
		},
		{
			name:  "single-digit-day-double-space",
			input: "Mon Jul  5 10:23:45 2025",
			want:  time.Date(2025, 7, 5, 10, 23, 45, 0, time.UTC).Unix(),
		},
		{
			name:  "leading-space-trimmed",
			input: "  Mon Jan  2 03:04:05 2026",
			want:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix(),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseDarwinStartTime(c.input)
			if err != nil {
				t.Fatalf("parseDarwinStartTime(%q) error: %v", c.input, err)
			}
			if got != c.want {
				t.Errorf("parseDarwinStartTime(%q) = %d, want %d", c.input, got, c.want)
			}
		})
	}
}

// TestParseDarwinStartTime_Errors (P2-2) 验证错误输入被拒绝。
func TestParseDarwinStartTime_Errors(t *testing.T) {
	cases := []string{
		"",
		"garbage",
		"Mon Jul 15 2025",      // 缺时间
		"15 Jul 2025 10:23:45", // 缺星期
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := parseDarwinStartTime(c)
			if err == nil {
				t.Errorf("parseDarwinStartTime(%q) 应返回错误, 但成功", c)
			}
		})
	}
}

// TestProcessInfo_CurrentProcess (P2-2) 验证 processInfo 在当前进程上能返回
// 有效启动时间。在 macOS 上调用真实 ps；在 Linux 上读 /proc/self。
// 在其他 Unix 上 processInfo 返回错误，本测试跳过。
func TestProcessInfo_CurrentProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 不适用")
	}
	pid := os.Getpid()
	startTime, name, err := processInfo(pid)
	if err != nil {
		// 非 linux/darwin 平台 processInfo 返回错误，跳过
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skipf("unsupported GOOS %s: %v", runtime.GOOS, err)
		}
		t.Fatalf("processInfo(self) error on %s: %v", runtime.GOOS, err)
	}
	if startTime <= 0 {
		t.Errorf("processInfo 返回的 StartTime 非正: %d", startTime)
	}
	// 进程名在 Linux 上可能为空（权限），darwin 上应有值
	if runtime.GOOS == "darwin" && name == "" {
		t.Logf("警告: darwin 上 processInfo 返回空 name（可能 ps comm= 失败）")
	}
}

// TestProcessInfo_InvalidPID (P2-2) 验证无效 PID 被拒绝。
func TestProcessInfo_InvalidPID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 不适用")
	}
	_, _, err := processInfo(0)
	if err == nil {
		t.Error("processInfo(0) 应返回错误")
	}
	_, _, err = processInfo(-1)
	if err == nil {
		t.Error("processInfo(-1) 应返回错误")
	}
}

// TestProcessInfo_NonexistentPID (P2-2) 验证不存在的 PID 返回错误。
// 用一个几乎不可能被占用的 PID（如 999999）。
func TestProcessInfo_NonexistentPID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 不适用")
	}
	const ghostPID = 999999
	_, _, err := processInfo(ghostPID)
	if err == nil {
		t.Errorf("processInfo(%d) 应返回错误（PID 不存在）", ghostPID)
	}
}

// TestLockIsStale_PIDReuseDetection (P2-2) 验证 PID 复用检测逻辑：
// 当 PID 存活但启动时间不匹配时，lockIsStale 应返回 true。
// 在 macOS 上，本测试通过手动构造 lockInfo 与当前进程不同启动时间模拟 PID 复用。
func TestLockIsStale_PIDReuseDetection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 不适用")
	}
	pid := os.Getpid()
	_, _, err := processInfo(pid)
	if err != nil {
		t.Skipf("processInfo 不可用: %v", err)
	}
	// 构造 lockInfo：PID 是当前进程（存活），但 StartTime 完全不同
	// → 模拟 PID 被另一进程复用
	info := lockInfo{
		PID:       pid,
		StartTime: 1, // 故意构造一个不可能的旧启动时间
		Name:      "/path/to/different/binary",
	}
	if !lockIsStale(info) {
		t.Error("lockIsStale 应识别 PID 复用（启动时间不匹配）并返回 true")
	}
}
