//go:build linux

package services

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type localHostNoFollowEntry struct {
	Name     string
	IsDir    bool
	Size     int64
	Modified time.Time
}

type localHostNoFollowRoot struct {
	fd    int
	valid bool
}

func localHostNoFollowPlatformSupported() bool { return true }

func localHostNoFollowBindRoot(path string) localHostNoFollowRoot {
	var before unix.Stat_t
	if err := unix.Lstat(path, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFDIR {
		return localHostNoFollowRoot{}
	}
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return localHostNoFollowRoot{}
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino || after.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return localHostNoFollowRoot{}
	}
	return localHostNoFollowRoot{fd: fd, valid: true}
}
func localHostNoFollowRootValid(root localHostNoFollowRoot) bool { return root.valid }
func localHostNoFollowCloseRoot(root *localHostNoFollowRoot) {
	if root != nil && root.valid {
		_ = unix.Close(root.fd)
		*root = localHostNoFollowRoot{}
	}
}

func localHostNoFollowRootClosed(root localHostNoFollowRoot) bool {
	if !root.valid {
		return true
	}
	_, err := unix.FcntlInt(uintptr(root.fd), unix.F_GETFD, 0)
	return err == unix.EBADF
}

var localHostNoFollowHook struct {
	sync.RWMutex
	beforePublish func(int, string) error
}

func localHostNoFollowInstallBeforePublishHook(hook func(int, string) error) func() {
	localHostNoFollowHook.Lock()
	previous := localHostNoFollowHook.beforePublish
	localHostNoFollowHook.beforePublish = hook
	localHostNoFollowHook.Unlock()
	return func() {
		localHostNoFollowHook.Lock()
		localHostNoFollowHook.beforePublish = previous
		localHostNoFollowHook.Unlock()
	}
}

func localHostNoFollowRunBeforePublishHook(parentFD int, name string) error {
	localHostNoFollowHook.RLock()
	hook := localHostNoFollowHook.beforePublish
	localHostNoFollowHook.RUnlock()
	if hook != nil {
		return hook(parentFD, name)
	}
	return nil
}

func localHostNoFollowReplaceNameForTest(parentFD int, name string) error {
	if err := unix.Unlinkat(parentFD, name, 0); err != nil {
		return err
	}
	fd, err := unix.Openat(parentFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

const localHostResolveFlags = unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS

func localHostOpen(root localHostNoFollowRoot, relative string, flags int, mode uint32) (int, error) {
	how := &unix.OpenHow{Flags: uint64(flags), Mode: uint64(mode), Resolve: localHostResolveFlags}
	fd, err := unix.Openat2(root.fd, relativeOrDot(relative), how)
	if err != nil {
		return -1, err
	}
	return fd, nil
}

func relativeOrDot(relative string) string {
	if relative == "" {
		return "."
	}
	return filepath.FromSlash(relative)
}

func localHostNoFollowReadFile(root localHostNoFollowRoot, relative string, limit int64) ([]byte, error) {
	fd, err := localHostOpen(root, relative, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "workspace")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("workspace resource is not a regular file: %w", ErrInvalidInput)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d byte read limit: %w", limit, ErrInvalidInput)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d byte read limit: %w", limit, ErrInvalidInput)
	}
	return data, nil
}

func localHostNoFollowStat(root localHostNoFollowRoot, relative string) (os.FileInfo, error) {
	fd, err := localHostOpen(root, relative, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "workspace")
	defer file.Close()
	return file.Stat()
}

func localHostNoFollowReadDir(root localHostNoFollowRoot, relative string) ([]localHostNoFollowEntry, error) {
	fd, err := localHostOpen(root, relative, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "workspace")
	defer file.Close()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	result := make([]localHostNoFollowEntry, 0, len(entries))
	for _, entry := range entries {
		child := entry.Name()
		childFD, err := localHostOpen(root, joinRelative(relative, child), unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		childFile := os.NewFile(uintptr(childFD), "workspace")
		info, statErr := childFile.Stat()
		childFile.Close()
		if statErr != nil {
			return nil, statErr
		}
		result = append(result, localHostNoFollowEntry{Name: child, IsDir: info.IsDir(), Size: info.Size(), Modified: info.ModTime()})
	}
	return result, nil
}

func joinRelative(parent, child string) string {
	if strings.Trim(parent, "/") == "" {
		return child
	}
	return parent + "/" + child
}

func localHostNoFollowWriteFile(root localHostNoFollowRoot, relative string, data []byte, perm os.FileMode) error {
	if err := localHostEnsureParent(root, relative); err != nil {
		return err
	}
	parent, name := filepath.Split(relative)
	parent = strings.TrimSuffix(parent, "/")
	parentFD, err := localHostOpen(root, parent, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	tmp := -1
	var tmpName string
	for attempts := 0; attempts < 32; attempts++ {
		var random [16]byte
		if _, err = rand.Read(random[:]); err != nil {
			return err
		}
		tmpName = ".gugacode-tmp-" + hex.EncodeToString(random[:])
		tmp, err = unix.Openat2(parentFD, tmpName, &unix.OpenHow{Flags: uint64(unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC), Mode: uint64(perm), Resolve: localHostResolveFlags})
		if err == nil {
			break
		}
		if err != unix.EEXIST {
			return err
		}
	}
	if tmp < 0 {
		return fmt.Errorf("create unique temporary file: %w", err)
	}
	tmpFile := os.NewFile(uintptr(tmp), "workspace")
	var opened unix.Stat_t
	if err = unix.Fstat(tmp, &opened); err != nil {
		_ = tmpFile.Close()
		return err
	}
	removeTemp := true
	defer func() {
		_ = tmpFile.Close()
		if removeTemp && localHostNoFollowNameMatches(parentFD, tmpName, opened) {
			_ = unix.Unlinkat(parentFD, tmpName, 0)
		}
	}()
	if _, err = tmpFile.Write(data); err != nil {
		return err
	}
	if err = tmpFile.Sync(); err != nil {
		return err
	}
	if err = tmpFile.Close(); err != nil {
		return err
	}
	if err = localHostNoFollowRunBeforePublishHook(parentFD, tmpName); err != nil {
		return err
	}
	if !localHostNoFollowNameMatches(parentFD, tmpName, opened) {
		return fmt.Errorf("temporary file identity changed before publish: %w", ErrNotAllowed)
	}
	if err = unix.Renameat(parentFD, tmpName, parentFD, filepath.Base(name)); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func localHostNoFollowNameMatches(parentFD int, name string, opened unix.Stat_t) bool {
	var named unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false
	}
	return named.Dev == opened.Dev && named.Ino == opened.Ino
}

func localHostEnsureParent(root localHostNoFollowRoot, relative string) error {
	parent := filepath.ToSlash(filepath.Dir(relative))
	if parent == "." || parent == "" {
		return nil
	}
	parts := strings.Split(parent, "/")
	for i := range parts {
		path := strings.Join(parts[:i+1], "/")
		fd, err := localHostOpen(root, path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err == nil {
			unix.Close(fd)
			continue
		}
		parentFD, parentErr := localHostOpen(root, strings.Join(parts[:i], "/"), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if parentErr != nil {
			return parentErr
		}
		mkdirErr := unix.Mkdirat(parentFD, parts[i], 0755)
		unix.Close(parentFD)
		if mkdirErr != nil && mkdirErr != unix.EEXIST {
			return mkdirErr
		}
	}
	return nil
}
