package services

import (
	"errors"
	"fmt"
	"io"
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
	mu         sync.Mutex
	released   bool
	releaseErr error
	lockPath   string
	lockName   string
	stateRoot  *os.Root
	file       *os.File

	beforeRootRemoveForTest func()
}

var errEmptyInstanceLock = errors.New("empty lock")

const instanceStateGuardName = ".koyori-ide.lock.guard"

var instanceStateGuardProcessMu sync.Mutex

type instanceStateGuard struct {
	file     *os.File
	platform *instanceStatePlatformGuard
}

func (l *InstanceLock) acquireStateGuard() (*instanceStateGuard, error) {
	if l.stateRoot == nil {
		return nil, nil
	}
	instanceStateGuardProcessMu.Lock()
	file, err := openAgentStateRegularFile(
		l.stateRoot, instanceStateGuardName, os.O_CREATE|os.O_RDWR, 0o600,
	)
	if err != nil {
		instanceStateGuardProcessMu.Unlock()
		return nil, fmt.Errorf("open instance state guard: %w", err)
	}
	platform, err := acquireInstanceStatePlatformGuard(file)
	if err != nil {
		closeErr := file.Close()
		instanceStateGuardProcessMu.Unlock()
		return nil, errors.Join(err, closeErr)
	}
	opened, statErr := file.Stat()
	named, lstatErr := l.stateRoot.Lstat(instanceStateGuardName)
	if statErr != nil || lstatErr != nil || !os.SameFile(opened, named) {
		unlockErr := releaseInstanceStatePlatformGuard(file, platform)
		closeErr := file.Close()
		instanceStateGuardProcessMu.Unlock()
		return nil, errors.Join(
			fmt.Errorf("instance state guard identity changed: %w", ErrUsagePersistencePoisoned),
			statErr, lstatErr, unlockErr, closeErr,
		)
	}
	return &instanceStateGuard{file: file, platform: platform}, nil
}

func (g *instanceStateGuard) release() error {
	if g == nil {
		return nil
	}
	unlockErr := releaseInstanceStatePlatformGuard(g.file, g.platform)
	closeErr := g.file.Close()
	instanceStateGuardProcessMu.Unlock()
	return errors.Join(unlockErr, closeErr)
}

// NewInstanceLockWithRoot binds lock creation, stale cleanup and release to a
// retained state capability. It is trusted package wiring, not a renderer API.
func NewInstanceLockWithRoot(configDir string, stateRoot *os.Root) *InstanceLock {
	return &InstanceLock{
		lockPath:  filepath.Join(configDir, "koyori-ide.lock"),
		lockName:  "koyori-ide.lock",
		stateRoot: stateRoot,
	}
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
func (l *InstanceLock) Acquire() (resultErr error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	guard, err := l.acquireStateGuard()
	if err != nil {
		return err
	}
	if guard != nil {
		defer func() { resultErr = errors.Join(resultErr, guard.release()) }()
	}

	if err := l.tryCreateExclusive(); err == nil {
		return nil
	} else if !os.IsExist(err) {
		return fmt.Errorf("create lock file: %w", err)
	}

	// 锁文件已存在 — 检查是否为过期锁
	info, lockIdentity, _, readErr := l.readLockInfo()
	if readErr == nil && info.PID > 0 {
		if lockIsStale(info) {
			// 过期：进程已退出或 PID 被复用
			if err := l.removeLockFile(lockIdentity); err != nil {
				return fmt.Errorf("remove stale lock file: %w", err)
			}
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

	// A process can crash after O_EXCL creation but before writing its PID. The
	// opened identity makes that exact empty file safe to remove; malformed
	// non-empty root-bound locks remain fail-closed.
	if errors.Is(readErr, errEmptyInstanceLock) && lockIdentity != nil {
		if err := l.removeLockFile(lockIdentity); err != nil {
			return fmt.Errorf("remove empty lock file: %w", err)
		}
		if err := l.tryCreateExclusive(); err != nil {
			if os.IsExist(err) {
				return fmt.Errorf("another koyori-ide instance is already running (lock file: %s)", l.lockPath)
			}
			return fmt.Errorf("create lock file after empty cleanup: %w", err)
		}
		return nil
	}

	// Pathname-based desktop compatibility may clean up an unreadable lock;
	// the root-bound headless host refuses unknown identities and parse errors.
	if l.stateRoot != nil && readErr != nil {
		return fmt.Errorf("inspect root-bound lock file: %w", errors.Join(ErrUsagePersistencePoisoned, readErr))
	}
	if readErr != nil {
		if err := l.removeLockFile(lockIdentity); err != nil {
			return fmt.Errorf("remove unreadable lock file: %w", err)
		}
		if err := l.tryCreateExclusive(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("another koyori-ide instance is already running (lock file: %s)", l.lockPath)
}

func (l *InstanceLock) tryCreateExclusive() error {
	var f *os.File
	var err error
	if l.stateRoot != nil {
		f, err = openAgentStateRegularFile(l.stateRoot, l.lockName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	} else {
		f, err = os.OpenFile(l.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	}
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
		identity, _ := f.Stat()
		_ = f.Close()
		_ = l.removeLockFile(identity)
		return fmt.Errorf("write lock file: %w", writeErr)
	}
	l.file = f
	l.released = false
	return nil
}

func (l *InstanceLock) readLockInfo() (lockInfo, os.FileInfo, []byte, error) {
	if l.stateRoot == nil {
		data, err := os.ReadFile(l.lockPath)
		if err != nil {
			return lockInfo{}, nil, nil, err
		}
		info, err := os.Lstat(l.lockPath)
		if err != nil {
			return lockInfo{}, nil, data, err
		}
		parsed, err := parseLockInfo(data)
		return parsed, info, data, err
	}
	file, err := openAgentStateRegularFile(l.stateRoot, l.lockName, os.O_RDONLY, 0)
	if err != nil {
		return lockInfo{}, nil, nil, err
	}
	defer file.Close()
	identity, err := file.Stat()
	if err != nil {
		return lockInfo{}, nil, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return lockInfo{}, identity, nil, err
	}
	parsed, err := parseLockInfo(data)
	return parsed, identity, data, err
}

func (l *InstanceLock) removeLockFile(expected os.FileInfo) error {
	if l.stateRoot == nil {
		if expected != nil {
			current, err := os.Lstat(l.lockPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			if !os.SameFile(expected, current) {
				return fmt.Errorf("lock file identity changed")
			}
		}
		return os.Remove(l.lockPath)
	}
	if expected == nil {
		return fmt.Errorf("root-bound lock identity is unavailable: %w", ErrUsagePersistencePoisoned)
	}
	current, err := openAgentStateRegularFile(l.stateRoot, l.lockName, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open root-bound lock for identity removal: %w", errors.Join(ErrUsagePersistencePoisoned, err))
	}
	currentIdentity, statErr := current.Stat()
	closeErr := current.Close()
	if statErr != nil {
		return fmt.Errorf("stat root-bound lock identity: %w", errors.Join(ErrUsagePersistencePoisoned, statErr))
	}
	if closeErr != nil {
		return fmt.Errorf("close root-bound lock handle: %w", errors.Join(ErrUsagePersistencePoisoned, closeErr))
	}
	if !os.SameFile(expected, currentIdentity) {
		return fmt.Errorf("root-bound lock identity changed: %w", ErrUsagePersistencePoisoned)
	}
	if l.beforeRootRemoveForTest != nil {
		l.beforeRootRemoveForTest()
	}
	if err := l.stateRoot.Remove(l.lockName); err != nil {
		return fmt.Errorf("remove root-bound lock: %w", errors.Join(ErrUsagePersistencePoisoned, err))
	}
	return nil
}

// readLockInfo 读取锁文件并解析持锁进程信息 (H-5)。
// 支持旧格式（仅 PID）和新格式（PID + 启动时间 + 进程名），保持向后兼容。
func readLockInfo(path string) (lockInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return lockInfo{}, err
	}
	return parseLockInfo(b)
}

func parseLockInfo(data []byte) (lockInfo, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return lockInfo{}, errEmptyInstanceLock
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
func (l *InstanceLock) Release() (resultErr error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.released {
		return l.releaseErr
	}
	guard, err := l.acquireStateGuard()
	if err != nil {
		return err
	}
	defer func() {
		if guard != nil {
			guardErr := guard.release()
			if guardErr != nil && !errors.Is(guardErr, ErrUsagePersistencePoisoned) {
				guardErr = errors.Join(ErrUsagePersistencePoisoned, guardErr)
			}
			resultErr = errors.Join(resultErr, guardErr)
		}
		l.releaseErr = resultErr
		l.released = true
	}()

	var firstErr error
	var identity os.FileInfo
	if l.file != nil {
		identity, err = l.file.Stat()
		if err != nil {
			if l.stateRoot != nil {
				err = errors.Join(ErrUsagePersistencePoisoned, err)
			}
			firstErr = errors.Join(firstErr, err)
		}
		if err := l.file.Close(); err != nil {
			if l.stateRoot != nil {
				err = errors.Join(ErrUsagePersistencePoisoned, err)
			}
			firstErr = errors.Join(firstErr, err)
		}
		l.file = nil
	}
	if err := l.removeLockFile(identity); err != nil && !os.IsNotExist(err) {
		if l.stateRoot != nil && !errors.Is(err, ErrUsagePersistencePoisoned) {
			err = errors.Join(ErrUsagePersistencePoisoned, err)
		}
		firstErr = errors.Join(firstErr, err)
	}
	return firstErr
}
