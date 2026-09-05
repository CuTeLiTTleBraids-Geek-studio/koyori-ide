//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package services

import (
	"os"

	"golang.org/x/sys/unix"
)

func agentFileHasMultipleLinks(file *os.File) (bool, error) {
	var info unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &info); err != nil {
		return false, err
	}
	return info.Nlink > 1, nil
}

func agentFileOwnedByCurrentUser(file *os.File) (bool, error) {
	var info unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &info); err != nil {
		return false, err
	}
	return info.Uid == uint32(os.Geteuid()), nil
}

func headlessPathHasLinkBoundary(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return info.Mode()&os.ModeSymlink != 0, nil
}
