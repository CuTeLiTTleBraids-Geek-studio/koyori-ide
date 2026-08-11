package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// InstanceLock prevents multiple koyori-ide instances from running simultaneously
// and competing for the same settings.json (G-QUAL-05).
//
// Stale locks (crash / Task Manager kill / GUI exit without Release) are
// detected by checking whether the PID written in the lock file is still alive.
type InstanceLock struct {
	mu       sync.Mutex
	released bool
	lockPath string
	file     *os.File
}

// lockInfo 描述持锁进程的身份信息，用于检测 PID 复用 (H-5)。
type lockInfo struct {
	PID       int    // 持锁进程 PID
	StartTime int64  // 持锁进程启动时间戳
	Name      string // 持锁进程可执行文件路径
}

// NewInstanceLock creates a lock at the given path (typically in the user config dir).
func NewInstanceLock(configDir string) *InstanceLock {
	return &InstanceLock{
		lockPath: filepath.Join(configDir, "koyori-ide.lock"),
	}
}

// LockPath returns the absolute lock file path (for error messages / UI).
func (l *InstanceLock) LockPath() string {
	return l.lockPath
}

// Acquire tries to acquire the single-instance lock. Returns an error if
// another *live* instance is already running. Removes stale lock files when
// the recorded PID is not running (common after GUI crash / Force-quit),
// or when the PID has been reused by a different process (H-5).
func (l *InstanceLock) Acquire() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.tryCreateExclusive(); err == nil {
		return nil
	} else if !os.IsExist(err) {
		return fmt.Errorf("create lock file: %w", err)
	}

	// 锁文件已存在 — 检查是否为过期锁
	info, readErr := readLockInfo(l.lockPath)
	if readErr == nil && info.PID > 0 {
		if lockIsStale(info) {
			// 过期：进程已退出或 PID 被复用
			_ = os.Remove(l.lockPath)
			if err := l.tryCreateExclusive(); err != nil {
				if os.IsExist(err) {
					return fmt.Errorf("another koyori-ide instance is already running (lock file: %s)", l.lockPath)
				}
				return fmt.Errorf("create lock file after stale cleanup: %w", err)
			}
			return nil
		}
		// 进程存活且信息匹配 — 真实锁
		return fmt.Errorf("another koyori-ide instance is already running (pid %d, lock: %s)", info.PID, l.lockPath)
	}

	// 不可读/空锁 — 尝试清除后重试
	data, _ := os.ReadFile(l.lockPath)
	if len(strings.TrimSpace(string(data))) == 0 || readErr != nil {
		_ = os.Remove(l.lockPath)
		if err := l.tryCreateExclusive(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("another koyori-ide instance is already running (lock file: %s)", l.lockPath)
}

func (l *InstanceLock) tryCreateExclusive() error {
	f, err := os.OpenFile(l.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	// 写入 PID + 启动时间戳 + 进程名，防止 Windows PID 复用误判 (H-5)
	// 获取进程信息失败时只写 PID（向后兼容旧格式）
	var writeErr error
	if info, infoErr := currentProcessInfo(); infoErr == nil {
		_, writeErr = fmt.Fprintf(f, "%d\n%d\n%s\n", info.PID, info.StartTime, info.Name)
	} else {
		_, writeErr = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	// 检查写入错误 (H-5)
	if writeErr != nil {
		f.Close()
		os.Remove(l.lockPath)
		return fmt.Errorf("write lock file: %w", writeErr)
	}
	l.file = f
	l.released = false
	return nil
}

// readLockInfo 读取锁文件并解析持锁进程信息 (H-5)。
// 支持旧格式（仅 PID）和新格式（PID + 启动时间 + 进程名），保持向后兼容。
func readLockInfo(path string) (lockInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return lockInfo{}, err
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return lockInfo{}, fmt.Errorf("empty lock")
	}
	lines := strings.Split(text, "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return lockInfo{}, err
	}
	info := lockInfo{PID: pid}
	// 向后兼容：旧格式只有 PID 一行
	if len(lines) >= 2 {
		info.StartTime, _ = strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	}
	if len(lines) >= 3 {
		info.Name = strings.TrimSpace(lines[2])
	}
	return info, nil
}

// currentProcessInfo 返回当前进程的锁信息 (H-5)。
func currentProcessInfo() (lockInfo, error) {
	pid := os.Getpid()
	startTime, name, err := processInfo(pid)
	if err != nil {
		return lockInfo{PID: pid}, err
	}
	return lockInfo{PID: pid, StartTime: startTime, Name: name}, nil
}

// lockIsStale 判断锁是否过期 (H-5)：
//   - 持锁进程已退出 → 过期
//   - PID 被复用（启动时间或进程名不匹配）→ 过期
//   - 无法获取进程信息 → 保守判断为非过期（保持向后兼容）
func lockIsStale(info lockInfo) bool {
	if info.PID <= 0 {
		return true
	}
	if !processAlive(info.PID) {
		return true // 进程已退出
	}
	// 进程存活 — 校验是否为同一进程（防止 PID 复用）
	startTime, name, err := processInfo(info.PID)
	if err != nil {
		// 无法获取进程信息 — 保守判断为非过期（保持向后兼容）
		return false
	}
	// 校验启动时间
	if info.StartTime != 0 && startTime != info.StartTime {
		return true // PID 被复用（启动时间不同）
	}
	// 校验进程名（仅在能获取到当前进程名时才校验）
	if info.Name != "" && name != "" && name != info.Name {
		return true // PID 被复用（进程名不同）
	}
	return false
}

// Release releases the single-instance lock.
func (l *InstanceLock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.released {
		return nil
	}
	l.released = true

	var firstErr error
	if l.file != nil {
		if err := l.file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		l.file = nil
	}
	if err := os.Remove(l.lockPath); err != nil && !os.IsNotExist(err) && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
