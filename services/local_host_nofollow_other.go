//go:build !linux

package services

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type localHostNoFollowEntry struct {
	Name     string
	IsDir    bool
	Size     int64
	Modified time.Time
}

type localHostNoFollowRoot struct {
	root *os.Root
}

func localHostNoFollowPlatformSupported() bool {
	// os.Root is pathname-based and TOCTOU-vulnerable on these platforms.
	return runtime.GOOS != "js" && runtime.GOOS != "plan9"
}

func localHostNoFollowBindRoot(path string) localHostNoFollowRoot {
	if !localHostNoFollowPlatformSupported() {
		return localHostNoFollowRoot{}
	}
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode()&fs.ModeSymlink != 0 {
		return localHostNoFollowRoot{}
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return localHostNoFollowRoot{}
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = root.Close()
		return localHostNoFollowRoot{}
	}
	return localHostNoFollowRoot{root: root}
}

func localHostNoFollowRootValid(root localHostNoFollowRoot) bool { return root.root != nil }

func localHostNoFollowCloseRoot(root *localHostNoFollowRoot) {
	if root != nil && root.root != nil {
		_ = root.root.Close()
		*root = localHostNoFollowRoot{}
	}
}

func localHostNoFollowRootClosed(root localHostNoFollowRoot) bool { return root.root == nil }

// The publish-replacement hook exercises Linux's descriptor-level rename
// implementation. Other platforms use os.Root and have platform-specific
// identity tests in FileService.
func localHostNoFollowInstallBeforePublishHook(func(int, string) error) func() { return func() {} }
func localHostNoFollowReplaceNameForTest(int, string) error                    { return noFollowUnavailable() }

func noFollowUnavailable() error {
	return fmt.Errorf("secure no-follow workspace operations are unavailable on this platform: %w", ErrNotAllowed)
}

func localHostNoFollowReadFile(root localHostNoFollowRoot, relative string, limit int64) ([]byte, error) {
	if !localHostNoFollowRootValid(root) {
		return nil, noFollowUnavailable()
	}
	file, err := root.root.Open(relativeOrDot(relative))
	if err != nil {
		return nil, err
	}
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

func localHostNoFollowWriteFile(root localHostNoFollowRoot, relative string, data []byte, perm os.FileMode) error {
	if !localHostNoFollowRootValid(root) {
		return noFollowUnavailable()
	}
	relative = filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))
	if parent == "" {
		parent = "."
	}
	if err := root.root.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	temp := filepath.ToSlash(filepath.Join(filepath.FromSlash(parent), ".gugacode-tmp-"+hex.EncodeToString(random[:])))
	file, err := root.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		_ = root.root.Remove(temp)
	}()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Chmod(perm); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	named, err := root.root.Lstat(temp)
	if err != nil || !os.SameFile(opened, named) {
		return fmt.Errorf("temporary file identity changed before publish: %w", ErrNotAllowed)
	}
	if err := root.root.Rename(temp, relative); err != nil {
		return err
	}
	return nil
}

func localHostNoFollowReadDir(root localHostNoFollowRoot, relative string) ([]localHostNoFollowEntry, error) {
	if !localHostNoFollowRootValid(root) {
		return nil, noFollowUnavailable()
	}
	base := relativeOrDot(relative)
	entries, err := fs.ReadDir(root.root.FS(), base)
	if err != nil {
		return nil, err
	}
	result := make([]localHostNoFollowEntry, 0, len(entries))
	for _, entry := range entries {
		child := filepath.ToSlash(filepath.Join(filepath.FromSlash(base), entry.Name()))
		info, err := root.root.Stat(child)
		if err != nil {
			return nil, err
		}
		result = append(result, localHostNoFollowEntry{
			Name: entry.Name(), IsDir: info.IsDir(), Size: info.Size(), Modified: info.ModTime(),
		})
	}
	return result, nil
}

func localHostNoFollowStat(root localHostNoFollowRoot, relative string) (os.FileInfo, error) {
	if !localHostNoFollowRootValid(root) {
		return nil, noFollowUnavailable()
	}
	return root.root.Stat(relativeOrDot(relative))
}

func relativeOrDot(relative string) string {
	if relative == "" {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
}
