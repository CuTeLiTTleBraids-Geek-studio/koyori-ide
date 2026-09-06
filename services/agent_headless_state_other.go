//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package services

import "os"

func agentFileHasMultipleLinks(_ *os.File) (bool, error) {
	return false, nil
}

func agentFileOwnedByCurrentUser(_ *os.File) (bool, error) {
	return true, nil
}

func headlessPathHasLinkBoundary(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return info.Mode()&os.ModeSymlink != 0, nil
}
