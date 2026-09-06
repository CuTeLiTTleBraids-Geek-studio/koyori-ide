//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package services

import (
	"fmt"
	"os"
)

type instanceStatePlatformGuard struct{}

func acquireInstanceStatePlatformGuard(_ *os.File) (*instanceStatePlatformGuard, error) {
	return nil, fmt.Errorf("instance state guard is unsupported on this platform: %w", ErrNotAllowed)
}

func releaseInstanceStatePlatformGuard(_ *os.File, _ *instanceStatePlatformGuard) error {
	return nil
}
