package services

import (
	"fmt"
	"os"
	"testing"
)

func TestInstanceLock_AcquireRelease(t *testing.T) {
	dir := t.TempDir()
	lock := NewInstanceLock(dir)
	if err := lock.Acquire(); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestInstanceLock_SecondAcquireFails(t *testing.T) {
	dir := t.TempDir()
	lock1 := NewInstanceLock(dir)
	lock2 := NewInstanceLock(dir)
	if err := lock1.Acquire(); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer lock1.Release()
	if err := lock2.Acquire(); err == nil {
		t.Error("second acquire should have failed")
	}
}

func TestInstanceLock_CanReacquireAfterRelease(t *testing.T) {
	dir := t.TempDir()
	lock := NewInstanceLock(dir)
	if err := lock.Acquire(); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := lock.Acquire(); err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	lock.Release()
}

func TestInstanceLock_ReleaseIdempotent(t *testing.T) {
	dir := t.TempDir()
	lock := NewInstanceLock(dir)
	if err := lock.Acquire(); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second release should be idempotent: %v", err)
	}
}

func TestInstanceLock_StalePID_Reacquire(t *testing.T) {
	dir := t.TempDir()
	path := dir + string(os.PathSeparator) + "koyori-ide.lock"
	// Fake PID that is not running
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock := NewInstanceLock(dir)
	if err := lock.Acquire(); err != nil {
		t.Fatalf("should clear stale lock and acquire: %v", err)
	}
	lock.Release()
}

// --- H-5: PID 复用检测测试 ---

// TestInstanceLock_PIDReuseDetectedAsStale 模拟 PID 复用场景被拒绝 (H-5)。
// 场景：旧 koyori-ide 进程崩溃后，PID 被新进程复用。
// 锁文件中的 PID 仍存活，但启动时间戳和进程名不匹配 → 应识别为过期锁并清除。
func TestInstanceLock_PIDReuseDetectedAsStale(t *testing.T) {
	dir := t.TempDir()
	lock := NewInstanceLock(dir)
	path := lock.LockPath()

	// 获取当前进程的真实信息
	selfInfo, err := currentProcessInfo()
	if err != nil {
		t.Skipf("无法获取进程信息，跳过 PID 复用测试: %v", err)
	}

	// 写入当前 PID 但使用错误的启动时间戳和进程名（模拟 PID 复用）
	// 旧进程已崩溃，新进程复用了同一 PID，但启动时间和进程名不同
	fakeStartTime := selfInfo.StartTime - 1
	if fakeStartTime == selfInfo.StartTime {
		fakeStartTime = selfInfo.StartTime + 1 // 确保不同
	}
	fakeName := selfInfo.Name + ".old.bak"
	data := fmt.Sprintf("%d\n%d\n%s\n", selfInfo.PID, fakeStartTime, fakeName)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	// 尝试获取锁：应检测到 PID 复用（信息不匹配），清除过期锁并成功获取
	if err := lock.Acquire(); err != nil {
		t.Fatalf("应检测 PID 复用并成功获取锁，但失败: %v", err)
	}
	lock.Release()
}

// TestInstanceLock_GenuineLockNotCleared 验证真实锁不会被误判为过期 (H-5)。
// 锁文件中的 PID、启动时间戳和进程名都与当前进程匹配 → 应视为真实锁，获取失败。
func TestInstanceLock_GenuineLockNotCleared(t *testing.T) {
	dir := t.TempDir()
	lock := NewInstanceLock(dir)
	path := lock.LockPath()

	// 获取当前进程的真实信息
	selfInfo, err := currentProcessInfo()
	if err != nil {
		t.Skipf("无法获取进程信息，跳过真实锁测试: %v", err)
	}

	// 写入当前 PID + 正确的启动时间戳 + 正确的进程名
	data := fmt.Sprintf("%d\n%d\n%s\n", selfInfo.PID, selfInfo.StartTime, selfInfo.Name)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	// 尝试获取锁：应失败（真实锁，PID 存活且信息匹配）
	if err := lock.Acquire(); err == nil {
		lock.Release()
		t.Error("锁被存活且信息匹配的进程持有时，获取应失败")
	}
}

// TestInstanceLock_BackwardCompatOldPIDOnlyFormat 验证旧格式（仅 PID）向后兼容 (H-5)。
// 旧版本写入的锁文件只有 PID 一行，新版本应能正确读取和处理。
func TestInstanceLock_BackwardCompatOldPIDOnlyFormat(t *testing.T) {
	dir := t.TempDir()
	lock := NewInstanceLock(dir)
	path := lock.LockPath()

	// 写入旧格式：只有 PID（一个不存在的 PID）
	if err := os.WriteFile(path, []byte("999999\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// 读取锁信息 — 应正确解析旧格式
	info, err := readLockInfo(path)
	if err != nil {
		t.Fatalf("读取旧格式锁文件失败: %v", err)
	}
	if info.PID != 999999 {
		t.Errorf("PID = %d, 期望 999999", info.PID)
	}
	if info.StartTime != 0 {
		t.Errorf("旧格式 StartTime 应为 0, 得到 %d", info.StartTime)
	}
	if info.Name != "" {
		t.Errorf("旧格式 Name 应为空, 得到 %q", info.Name)
	}

	// 获取锁：PID 不存活 → 过期 → 清除并成功获取
	if err := lock.Acquire(); err != nil {
		t.Fatalf("旧格式过期锁应被清除并获取: %v", err)
	}
	lock.Release()
}
