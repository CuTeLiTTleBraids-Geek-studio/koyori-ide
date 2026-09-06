//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenHeadlessStateRootRechecksBoundDirectoryMode(t *testing.T) {
	state := t.TempDir()
	if err := os.Chmod(state, 0o770); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(state, 0o700) })

	root, _, err := openHeadlessStateRoot(state)
	if root != nil {
		_ = root.Close()
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("openHeadlessStateRoot error = %v, want ErrInvalidInput", err)
	}
}

func TestOpenHeadlessStateRootRejectsForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing directory ownership requires root")
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignUID := 1
	if foreignUID == os.Geteuid() {
		foreignUID = 2
	}
	if err := os.Chown(state, foreignUID, -1); err != nil {
		t.Skipf("changing directory ownership is unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chown(state, os.Geteuid(), -1) })

	root, _, err := openHeadlessStateRoot(state)
	if root != nil {
		_ = root.Close()
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("openHeadlessStateRoot error = %v, want ErrInvalidInput", err)
	}
}

func TestRootBoundInstanceLockConcurrentCrashCleanupHasSingleWinner(t *testing.T) {
	state := t.TempDir()
	lockPath := filepath.Join(state, "koyori-ide.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	first := NewInstanceLockWithRoot(state, root)
	second := NewInstanceLockWithRoot(state, root)
	firstAtRemove := make(chan struct{})
	releaseFirst := make(chan struct{})
	first.beforeRootRemoveForTest = func() {
		close(firstAtRemove)
		<-releaseFirst
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- first.Acquire() }()
	select {
	case <-firstAtRemove:
	case <-time.After(5 * time.Second):
		t.Fatal("first lock did not reach the deterministic removal window")
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- second.Acquire() }()
	var secondEarly error
	secondReturnedEarly := false
	select {
	case secondEarly = <-secondDone:
		secondReturnedEarly = true
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	firstErr := <-firstDone
	secondErr := secondEarly
	if !secondReturnedEarly {
		secondErr = <-secondDone
	}

	if firstErr == nil && secondErr == nil {
		t.Fatal("two root-bound instance locks both acquired after concurrent crash cleanup")
	}
	if firstErr != nil && secondErr != nil {
		t.Fatalf("neither root-bound instance lock acquired: first=%v second=%v", firstErr, secondErr)
	}

	for _, lock := range []*InstanceLock{first, second} {
		if lock.file != nil {
			_ = lock.file.Close()
			lock.file = nil
		}
	}
	_ = root.Remove("koyori-ide.lock")
}

func TestRootBoundResetUsageDoesNotDeleteReplacementLedger(t *testing.T) {
	parent := t.TempDir()
	state := filepath.Join(parent, "state")
	moved := filepath.Join(parent, "state-moved")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	permission := newAIPermissionService(state, root)
	if err := permission.recordUsageTrusted(UsageRecord{
		Operation: AIOpChat,
		Cost:      1,
	}); err != nil {
		t.Fatalf("recordUsageTrusted: %v", err)
	}
	if err := os.Rename(state, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("replacement-ledger-must-survive")
	if err := os.WriteFile(filepath.Join(state, "usage_log.jsonl"), replacement, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := permission.resetUsageTrusted(); err != nil {
		t.Fatalf("resetUsageTrusted: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(state, "usage_log.jsonl"))
	if err != nil {
		t.Fatalf("read replacement ledger: %v", err)
	}
	if string(after) != string(replacement) {
		t.Fatalf("replacement ledger changed: got %q want %q", after, replacement)
	}
	if _, err := os.Stat(filepath.Join(moved, "usage_log.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bound ledger remains after reset: %v", err)
	}
	if summary := permission.GetUsageSummary("all"); summary.TotalCost != 0 {
		t.Fatalf("usage summary cost = %v, want 0 after durable reset", summary.TotalCost)
	}
}
