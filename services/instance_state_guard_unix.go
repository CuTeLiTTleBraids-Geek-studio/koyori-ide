//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package services

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const (
	instanceStateGuardTimeout       = 10 * time.Second
	instanceStateGuardRetryInterval = 10 * time.Millisecond
)

type instanceStatePlatformGuard struct{}

func acquireInstanceStatePlatformGuard(file *os.File) (*instanceStatePlatformGuard, error) {
	deadline := time.Now().Add(instanceStateGuardTimeout)
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
	for {
		err := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
		if err == nil {
			return &instanceStatePlatformGuard{}, nil
		}
		if !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("acquire instance state guard: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out acquiring instance state guard: %w", err)
		}
		delay := time.Until(deadline)
		if delay > instanceStateGuardRetryInterval {
			delay = instanceStateGuardRetryInterval
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
}

func releaseInstanceStatePlatformGuard(file *os.File, _ *instanceStatePlatformGuard) error {
	unlock := unix.Flock_t{Type: unix.F_UNLCK, Whence: 0, Start: 0, Len: 1}
	if err := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &unlock); err != nil {
		return fmt.Errorf("release instance state guard: %w", err)
	}
	return nil
}
