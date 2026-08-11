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
	aesKeyFileLockTimeout       = 10 * time.Second
	aesKeyFileLockRetryInterval = 10 * time.Millisecond
)

type aesKeyFileLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquireAESKeyFileLock(path string) (*aesKeyFileLock, error) {
	return acquireAESKeyFileLockWithTimeout(path, aesKeyFileLockTimeout)
}

func acquireAESKeyFileLockWithTimeout(path string, timeout time.Duration) (*aesKeyFileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open aes key file lock: %w", err)
	}
	lock := &aesKeyFileLock{file: file}
	deadline := time.Now().Add(timeout)
	for {
		err = windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&lock.overlapped,
		)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errors.Join(
				fmt.Errorf("acquire aes key file lock: %w", err),
				wrapAESKeyFileLockCloseError(file.Close()),
			)
		}
		if time.Now().After(deadline) {
			return nil, errors.Join(
				fmt.Errorf("timed out acquiring aes key file lock after %s: %w", timeout, err),
				wrapAESKeyFileLockCloseError(file.Close()),
			)
		}
		delay := time.Until(deadline)
		if delay > aesKeyFileLockRetryInterval {
			delay = aesKeyFileLockRetryInterval
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
}

func (l *aesKeyFileLock) release() error {
	unlockErr := windows.UnlockFileEx(
		windows.Handle(l.file.Fd()),
		0,
		1,
		0,
		&l.overlapped,
	)
	closeErr := l.file.Close()
	if unlockErr != nil {
		unlockErr = fmt.Errorf("release aes key file lock: %w", unlockErr)
	}
	return errors.Join(unlockErr, wrapAESKeyFileLockCloseError(closeErr))
}

func wrapAESKeyFileLockCloseError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close aes key file lock: %w", err)
}
