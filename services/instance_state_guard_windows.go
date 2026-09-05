//go:build windows

package services

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const (
	instanceStateGuardTimeout       = 10 * time.Second
	instanceStateGuardRetryInterval = 10 * time.Millisecond
)

type instanceStatePlatformGuard struct {
	overlapped windows.Overlapped
}

func acquireInstanceStatePlatformGuard(file *os.File) (*instanceStatePlatformGuard, error) {
	guard := &instanceStatePlatformGuard{}
	deadline := time.Now().Add(instanceStateGuardTimeout)
	for {
		err := windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&guard.overlapped,
		)
		if err == nil {
			return guard, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
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

func releaseInstanceStatePlatformGuard(file *os.File, guard *instanceStatePlatformGuard) error {
	if guard == nil {
		return nil
	}
	if err := windows.UnlockFileEx(
		windows.Handle(file.Fd()), 0, 1, 0, &guard.overlapped,
	); err != nil {
		return fmt.Errorf("release instance state guard: %w", err)
	}
	return nil
}
