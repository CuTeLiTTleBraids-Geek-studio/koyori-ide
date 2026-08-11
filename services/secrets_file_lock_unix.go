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
	aesKeyFileLockTimeout       = 10 * time.Second
	aesKeyFileLockRetryInterval = 10 * time.Millisecond
)

var aesKeyProcessFileLock = func() chan struct{} {
	lock := make(chan struct{}, 1)
	lock <- struct{}{}
	return lock
}()

type aesKeyFileLock struct {
	file *os.File
}

func acquireAESKeyFileLock(path string) (*aesKeyFileLock, error) {
	return acquireAESKeyFileLockWithTimeout(path, aesKeyFileLockTimeout)
}

func acquireAESKeyFileLockWithTimeout(path string, timeout time.Duration) (*aesKeyFileLock, error) {
	deadline := time.Now().Add(timeout)
	if err := acquireAESKeyProcessFileLock(deadline, timeout); err != nil {
		return nil, err
	}
	processLockHeld := true
	defer func() {
		if processLockHeld {
			aesKeyProcessFileLock <- struct{}{}
		}
	}()

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open aes key file lock: %w", err)
	}

	lockSpec := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
	for {
		err = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lockSpec)
		if err == nil {
			processLockHeld = false
			return &aesKeyFileLock{file: file}, nil
		}
		if !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
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
	lockSpec := unix.Flock_t{Type: unix.F_UNLCK, Whence: 0, Start: 0, Len: 1}
	unlockErr := unix.FcntlFlock(l.file.Fd(), unix.F_SETLK, &lockSpec)
	closeErr := l.file.Close()
	aesKeyProcessFileLock <- struct{}{}
	if unlockErr != nil {
		unlockErr = fmt.Errorf("release aes key file lock: %w", unlockErr)
	}
	return errors.Join(unlockErr, wrapAESKeyFileLockCloseError(closeErr))
}

func acquireAESKeyProcessFileLock(deadline time.Time, timeout time.Duration) error {
	select {
	case <-aesKeyProcessFileLock:
		return nil
	default:
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return fmt.Errorf("timed out acquiring aes key file lock after %s", timeout)
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-aesKeyProcessFileLock:
		return nil
	case <-timer.C:
		return fmt.Errorf("timed out acquiring aes key file lock after %s", timeout)
	}
}

func wrapAESKeyFileLockCloseError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close aes key file lock: %w", err)
}
