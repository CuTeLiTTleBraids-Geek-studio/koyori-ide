//go:build !linux

package services

import (
	"fmt"
	"os"
	"time"
)

type localHostNoFollowEntry struct {
	Name     string
	IsDir    bool
	Size     int64
	Modified time.Time
}
type localHostNoFollowRoot struct{}

func localHostNoFollowBindRoot(string) localHostNoFollowRoot                   { return localHostNoFollowRoot{} }
func localHostNoFollowRootValid(localHostNoFollowRoot) bool                    { return false }
func localHostNoFollowCloseRoot(*localHostNoFollowRoot)                        {}
func localHostNoFollowRootClosed(localHostNoFollowRoot) bool                   { return true }
func localHostNoFollowInstallBeforePublishHook(func(int, string) error) func() { return func() {} }
func localHostNoFollowReplaceNameForTest(int, string) error                    { return noFollowUnavailable() }

func noFollowUnavailable() error {
	return fmt.Errorf("secure no-follow workspace operations are unavailable on this platform: %w", ErrNotAllowed)
}

func localHostNoFollowReadFile(localHostNoFollowRoot, string, int64) ([]byte, error) {
	return nil, noFollowUnavailable()
}
func localHostNoFollowWriteFile(localHostNoFollowRoot, string, []byte, os.FileMode) error {
	return noFollowUnavailable()
}
func localHostNoFollowReadDir(localHostNoFollowRoot, string) ([]localHostNoFollowEntry, error) {
	return nil, noFollowUnavailable()
}
func localHostNoFollowStat(localHostNoFollowRoot, string) (os.FileInfo, error) {
	return nil, noFollowUnavailable()
}
