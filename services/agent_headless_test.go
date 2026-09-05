package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func headlessPrivateStateDir(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(state, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func TestHeadlessAgentHostRelativePathPolicy(t *testing.T) {
	tests := []struct {
		name string
		path string
		ok   bool
	}{
		{name: "simple", path: "src/main.go", ok: true},
		{name: "empty", path: "", ok: false},
		{name: "absolute unix", path: "/etc/passwd", ok: false},
		{name: "absolute windows", path: `C:\\Windows\\win.ini`, ok: false},
		{name: "unc", path: `\\server\\share\\x`, ok: false},
		{name: "parent", path: "../outside.txt", ok: false},
		{name: "embedded parent", path: "src/../../outside.txt", ok: false},
		{name: "canonical parent", path: "src/../fixture.txt", ok: false},
		{name: "current component", path: "./fixture.txt", ok: false},
		{name: "empty component", path: "src//main.go", ok: false},
		{name: "backslash traversal", path: `src\\..\\outside.txt`, ok: false},
		{name: "windows drive absolute with slashes", path: "C:/Windows/win.ini", ok: false},
		{name: "windows drive relative", path: "C:fixture.txt", ok: false},
		{name: "nul", path: "file\x00.txt", ok: false},
		{name: "oversized", path: strings.Repeat("a", maxHeadlessRelativePathBytes+1), ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateHeadlessRelativePath(test.path)
			if test.ok && err != nil {
				t.Fatalf("validateHeadlessRelativePath(%q): %v", test.path, err)
			}
			if !test.ok && !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("validateHeadlessRelativePath(%q) error = %v, want ErrInvalidInput", test.path, err)
			}
		})
	}
}

func TestHeadlessAgentHostRequiresSeparateStateDirectory(t *testing.T) {
	workspace := t.TempDir()
	stateInside := filepath.Join(workspace, ".state")
	if err := os.Mkdir(stateInside, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHeadlessAgentHost(workspace, stateInside); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("NewHeadlessAgentHost nested state error = %v, want ErrInvalidInput", err)
	}
}

func TestHeadlessAgentHostRequiresAbsoluteDirectories(t *testing.T) {
	if _, err := NewHeadlessAgentHost(".", t.TempDir()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("relative workspace error = %v, want ErrInvalidInput", err)
	}
	if _, err := NewHeadlessAgentHost(t.TempDir(), "."); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("relative state error = %v, want ErrInvalidInput", err)
	}
}

func TestHeadlessAgentHostRejectsStateDirectoryLinksAndBroadUnixPermissions(t *testing.T) {
	workspace := t.TempDir()
	t.Run("directory link", func(t *testing.T) {
		target := t.TempDir()
		parent := t.TempDir()
		link := filepath.Join(parent, "state-link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("directory symlink unavailable: %v", err)
		}
		host, err := NewHeadlessAgentHost(workspace, link)
		if host != nil {
			_ = host.Close()
		}
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("linked state directory error = %v, want ErrInvalidInput", err)
		}
	})
	if runtime.GOOS == "windows" {
		return
	}
	t.Run("group writable", func(t *testing.T) {
		state := headlessPrivateStateDir(t)
		if err := os.Chmod(state, 0o770); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(state, 0o700)
		host, err := NewHeadlessAgentHost(workspace, state)
		if host != nil {
			_ = host.Close()
		}
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("group-writable state error = %v, want ErrInvalidInput", err)
		}
	})
}

func TestHeadlessAgentHostRejectsUnsafeStateLeavesWithoutTouchingTargets(t *testing.T) {
	workspace := t.TempDir()
	tests := []struct {
		name string
		seed func(t *testing.T, state, target string)
	}{
		{
			name: "usage directory",
			seed: func(t *testing.T, state, _ string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(state, "usage_log.jsonl"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "audit directory",
			seed: func(t *testing.T, state, _ string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(state, "agent-audit.log"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "audit symlink",
			seed: func(t *testing.T, state, target string) {
				t.Helper()
				if err := os.Symlink(target, filepath.Join(state, "agent-audit.log")); err != nil {
					t.Skipf("file symlink unavailable: %v", err)
				}
			},
		},
		{
			name: "audit hardlink",
			seed: func(t *testing.T, state, target string) {
				t.Helper()
				if err := os.Link(target, filepath.Join(state, "agent-audit.log")); err != nil {
					t.Skipf("hardlink unavailable: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := headlessPrivateStateDir(t)
			target := filepath.Join(t.TempDir(), "outside.txt")
			original := []byte("outside-state-target")
			if err := os.WriteFile(target, original, 0o640); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			test.seed(t, state, target)
			host, hostErr := NewHeadlessAgentHost(workspace, state)
			if host != nil {
				_ = host.Close()
			}
			if !errors.Is(hostErr, ErrUsagePersistence) && !errors.Is(hostErr, ErrUsagePersistencePoisoned) {
				t.Fatalf("unsafe state leaf error = %v, want usage persistence rejection", hostErr)
			}
			afterData, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if string(afterData) != string(original) {
				t.Fatalf("unsafe state leaf modified outside target: got %q want %q", afterData, original)
			}
			if runtime.GOOS != "windows" {
				after, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if after.Mode().Perm() != before.Mode().Perm() {
					t.Fatalf("unsafe state leaf changed outside mode from %o to %o", before.Mode().Perm(), after.Mode().Perm())
				}
			}
		})
	}
}

func TestAgentAuditLogPathRejectsLinkedLeafWithoutTouchingTarget(t *testing.T) {
	tests := []struct {
		name string
		link func(oldname, newname string) error
	}{
		{name: "symlink", link: os.Symlink},
		{name: "hardlink", link: os.Link},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "outside.txt")
			original := []byte("desktop-audit-outside-target")
			if err := os.WriteFile(target, original, 0o640); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			auditPath := filepath.Join(t.TempDir(), "agent-audit.log")
			if err := test.link(target, auditPath); err != nil {
				t.Skipf("%s unavailable: %v", test.name, err)
			}
			root, file, _, err := openAgentAuditLogPath(auditPath)
			if file != nil {
				_ = file.Close()
				t.Fatalf("openAgentAuditLogPath accepted %s", test.name)
			}
			if root != nil {
				_ = root.Close()
				t.Fatalf("openAgentAuditLogPath retained root for rejected %s", test.name)
			}
			if err == nil {
				t.Fatalf("openAgentAuditLogPath %s error = nil", test.name)
			}
			afterData, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if string(afterData) != string(original) {
				t.Fatalf("audit %s modified target: got %q want %q", test.name, afterData, original)
			}
			if runtime.GOOS != "windows" {
				after, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if after.Mode().Perm() != before.Mode().Perm() {
					t.Fatalf("audit %s changed target mode from %o to %o", test.name, before.Mode().Perm(), after.Mode().Perm())
				}
			}
		})
	}
}

func TestHeadlessAgentHostRejectsBroadExistingStateLeafPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission assertion")
	}
	state := headlessPrivateStateDir(t)
	ledger := filepath.Join(state, "usage_log.jsonl")
	if err := os.WriteFile(ledger, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ledger, 0o644); err != nil {
		t.Fatal(err)
	}
	host, err := NewHeadlessAgentHost(t.TempDir(), state)
	if host != nil {
		_ = host.Close()
	}
	if !errors.Is(err, ErrUsagePersistence) && !errors.Is(err, ErrUsagePersistencePoisoned) {
		t.Fatalf("broad state leaf error = %v, want usage persistence rejection", err)
	}
}

func TestHeadlessAgentHostUsesProductionReadAndDurableUsage(t *testing.T) {
	workspace := t.TempDir()
	state := canonicalTestPath(t, headlessPrivateStateDir(t))
	fixture := filepath.Join(workspace, "fixture.txt")
	content := "headless-host-fixture"
	if err := os.WriteFile(fixture, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	host, err := NewHeadlessAgentHost(workspace, state)
	if err != nil {
		t.Fatalf("NewHeadlessAgentHost: %v", err)
	}
	defer host.Close()
	auditPath := filepath.Join(state, "agent-audit.log")
	if host.agent.auditLog == nil || filepath.Clean(host.agent.auditLog.Name()) != filepath.Clean(auditPath) {
		t.Fatalf("headless audit path = %v, want %q", host.agent.auditLog, auditPath)
	}
	if info, statErr := os.Stat(auditPath); statErr != nil {
		t.Fatalf("headless audit log stat: %v", statErr)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("headless audit mode = %o, want 600", info.Mode().Perm())
	}

	catalog, err := host.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(catalog.Tools) != 1 || catalog.Tools[0].ID != "read" {
		t.Fatalf("headless catalog = %+v, want only production read", catalog)
	}
	result, err := host.Read(context.Background(), "fixture.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if result.Bytes != len(content) {
		t.Fatalf("read bytes = %d, want %d", result.Bytes, len(content))
	}
	if result.SHA256 == "" {
		t.Fatal("read digest is empty")
	}
	if result.SHA256 != headlessSHA256Hex([]byte(content)) {
		t.Fatalf("read digest = %q, want digest of authorized read", result.SHA256)
	}
	if records := host.permission.usageRecordsSnapshot(); len(records) != 1 || records[0].Pending {
		t.Fatalf("usage records = %+v, want one terminal record", records)
	}
}

func headlessSHA256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func TestHeadlessAgentHostLedgerFailurePrecedesHandler(t *testing.T) {
	workspace := t.TempDir()
	state := headlessPrivateStateDir(t)
	fixture := filepath.Join(workspace, "secret.txt")
	marker := "headless-ledger-failure-marker"
	if err := os.WriteFile(fixture, []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(state, "usage_log.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	host, err := NewHeadlessAgentHost(workspace, state)
	if host != nil {
		_ = host.Close()
		t.Fatal("NewHeadlessAgentHost returned a host for a poisoned ledger")
	}
	if !errors.Is(err, ErrUsagePersistencePoisoned) {
		t.Fatalf("NewHeadlessAgentHost error = %v, want ErrUsagePersistencePoisoned", err)
	}
}

func TestHeadlessAgentHostClassifiesCorruptStateAuthorityAsUsageUnavailable(t *testing.T) {
	workspace := t.TempDir()
	tests := []struct {
		name string
		seed func(t *testing.T, state string)
	}{
		{
			name: "malformed instance lock",
			seed: func(t *testing.T, state string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(state, "koyori-ide.lock"), []byte("not-a-lock\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "malformed lifecycle snapshot",
			seed: func(t *testing.T, state string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(state, "agent_lifecycle_identity.key"), []byte(strings.Repeat("00", 32)), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(state, "agent_lifecycle_sessions.json"), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := headlessPrivateStateDir(t)
			test.seed(t, state)
			host, err := NewHeadlessAgentHost(workspace, state)
			if host != nil {
				_ = host.Close()
				t.Fatal("NewHeadlessAgentHost returned a host for corrupt state authority")
			}
			if !errors.Is(err, ErrUsagePersistencePoisoned) {
				t.Fatalf("NewHeadlessAgentHost error = %v, want ErrUsagePersistencePoisoned", err)
			}
			if strings.Contains(err.Error(), state) {
				t.Fatalf("corrupt state error disclosed state path: %v", err)
			}
		})
	}
}

func TestOpenHeadlessAuditLogPreservesPoisonedStateError(t *testing.T) {
	state := headlessPrivateStateDir(t)
	target := filepath.Join(t.TempDir(), "audit-target.log")
	if err := os.WriteFile(target, []byte("outside-audit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(state, "agent-audit.log")); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	root, _, err := openHeadlessStateRoot(state)
	if err != nil {
		t.Fatalf("openHeadlessStateRoot: %v", err)
	}
	defer root.Close()
	file, err := openHeadlessAuditLog(root)
	if file != nil {
		_ = file.Close()
		t.Fatal("openHeadlessAuditLog accepted a multi-link state leaf")
	}
	if !errors.Is(err, ErrUsagePersistencePoisoned) {
		t.Fatalf("openHeadlessAuditLog error = %v, want ErrUsagePersistencePoisoned", err)
	}
}

func TestHeadlessAgentHostCloseIsIdempotentAndReleasesStateLock(t *testing.T) {
	host, err := NewHeadlessAgentHost(t.TempDir(), headlessPrivateStateDir(t))
	if err != nil {
		t.Fatalf("NewHeadlessAgentHost: %v", err)
	}
	lockPath := host.lock.LockPath()
	if err := host.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state lock remains after Close: %v", err)
	}
}

func TestHeadlessAgentHostCloseWaitsForInFlightReadAndRejectsFurtherOperations(t *testing.T) {
	workspace := t.TempDir()
	state := headlessPrivateStateDir(t)
	if err := os.WriteFile(filepath.Join(workspace, "fixture.txt"), []byte("in-flight"), 0o600); err != nil {
		t.Fatal(err)
	}
	host, err := NewHeadlessAgentHost(workspace, state)
	if err != nil {
		t.Fatalf("NewHeadlessAgentHost: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	host.beforeReadExecutionForTest = func() {
		close(entered)
		<-release
	}
	type readOutcome struct {
		result HeadlessReadResult
		err    error
	}
	readDone := make(chan readOutcome, 1)
	go func() {
		result, readErr := host.Read(context.Background(), "fixture.txt")
		readDone <- readOutcome{result: result, err: readErr}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight read did not reach deterministic hook")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- host.Close() }()
	select {
	case closeErr := <-closeDone:
		t.Fatalf("Close returned before in-flight read completed: %v", closeErr)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Stat(host.lock.LockPath()); err != nil {
		t.Fatalf("state lock was released while read remained in flight: %v", err)
	}
	close(release)
	select {
	case outcome := <-readDone:
		if outcome.err != nil || outcome.result.Bytes != len("in-flight") {
			t.Fatalf("in-flight read outcome = %+v, want success", outcome)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight read did not complete")
	}
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatalf("Close: %v", closeErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not complete after read")
	}
	if _, err := host.Read(context.Background(), "fixture.txt"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("post-Close Read error = %v, want ErrNotAllowed", err)
	}
	if _, err := host.Catalog(context.Background()); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("post-Close Catalog error = %v, want ErrNotAllowed", err)
	}
}

func TestHeadlessAgentHostConcurrentCloseIsIdempotent(t *testing.T) {
	host, err := NewHeadlessAgentHost(t.TempDir(), headlessPrivateStateDir(t))
	if err != nil {
		t.Fatalf("NewHeadlessAgentHost: %v", err)
	}
	results := make(chan error, 2)
	go func() { results <- host.Close() }()
	go func() { results <- host.Close() }()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Close %d: %v", i, err)
		}
	}
}

func TestHeadlessAgentHostSessionTerminalPersistenceFailureClearsSuccess(t *testing.T) {
	workspace := t.TempDir()
	state := headlessPrivateStateDir(t)
	if err := os.WriteFile(filepath.Join(workspace, "fixture.txt"), []byte("terminal-failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	host, err := NewHeadlessAgentHost(workspace, state)
	if err != nil {
		t.Fatalf("NewHeadlessAgentHost: %v", err)
	}
	defer host.Close()
	snapshot := filepath.Join(state, "agent_lifecycle_sessions.json")
	host.beforeSessionCloseForTest = func() error {
		if err := os.Remove(snapshot); err != nil {
			return err
		}
		return os.Mkdir(snapshot, 0o700)
	}
	result, err := host.Read(context.Background(), "fixture.txt")
	if !errors.Is(err, ErrUsagePersistence) {
		t.Fatalf("Read error = %v, want ErrUsagePersistence", err)
	}
	if result != (HeadlessReadResult{}) {
		t.Fatalf("Read returned success metadata after terminal persistence failure: %+v", result)
	}
}

func TestHeadlessAgentHostStateDirectoryReplacementCannotSplitPersistence(t *testing.T) {
	workspace := t.TempDir()
	stateParent := t.TempDir()
	state := filepath.Join(stateParent, "state")
	movedState := filepath.Join(stateParent, "state-moved")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "fixture.txt"), []byte("root-bound-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := NewHeadlessAgentHost(workspace, state)
	if err != nil {
		t.Fatalf("first NewHeadlessAgentHost: %v", err)
	}
	if err := os.Rename(state, movedState); err != nil {
		// A platform that prevents renaming the live state directory already
		// closes this replacement sequence at the filesystem boundary.
		if closeErr := first.Close(); closeErr != nil {
			t.Fatalf("first Close after blocked rename: %v", closeErr)
		}
		t.Logf("live state directory rename rejected by platform: %v", err)
		return
	}
	if err := os.Mkdir(state, 0o700); err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	second, err := NewHeadlessAgentHost(workspace, state)
	if err != nil {
		_ = first.Close()
		t.Fatalf("second NewHeadlessAgentHost on replacement: %v", err)
	}
	if _, err := first.Read(context.Background(), "fixture.txt"); err != nil {
		_ = first.Close()
		_ = second.Close()
		t.Fatalf("first Read after pathname replacement: %v", err)
	}
	if _, err := os.Stat(filepath.Join(movedState, "usage_log.jsonl")); err != nil {
		_ = first.Close()
		_ = second.Close()
		t.Fatalf("first host did not persist into its bound state root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "usage_log.jsonl")); !errors.Is(err, os.ErrNotExist) {
		_ = first.Close()
		_ = second.Close()
		t.Fatalf("first host wrote usage into replacement state: %v", err)
	}
	if err := first.Close(); err != nil {
		_ = second.Close()
		t.Fatalf("first Close: %v", err)
	}
	if _, err := os.Stat(second.lock.LockPath()); err != nil {
		_ = second.Close()
		t.Fatalf("first Close removed replacement host lock: %v", err)
	}
	third, err := NewHeadlessAgentHost(workspace, state)
	if third != nil {
		_ = third.Close()
	}
	if !errors.Is(err, ErrNotAllowed) {
		_ = second.Close()
		t.Fatalf("third host error = %v, want replacement lock rejection", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestHeadlessAgentHostRejectsRuntimeUsageHardlinkWithoutAppendingTarget(t *testing.T) {
	workspace := t.TempDir()
	state := headlessPrivateStateDir(t)
	if err := os.WriteFile(filepath.Join(workspace, "fixture.txt"), []byte("hardlink-runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	host, err := NewHeadlessAgentHost(workspace, state)
	if err != nil {
		t.Fatalf("NewHeadlessAgentHost: %v", err)
	}
	defer host.Close()
	target := filepath.Join(t.TempDir(), "outside-ledger.txt")
	original := []byte("outside-ledger-must-not-change")
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(state, "usage_log.jsonl")); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	result, err := host.Read(context.Background(), "fixture.txt")
	if !errors.Is(err, ErrUsagePersistencePoisoned) {
		t.Fatalf("Read through runtime usage hardlink result=%+v error=%v, want ErrUsagePersistencePoisoned", result, err)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("runtime usage hardlink appended outside target: got %q want %q", after, original)
	}
}

func TestHeadlessAgentHostSerializesOneStateDirectory(t *testing.T) {
	workspace := t.TempDir()
	state := headlessPrivateStateDir(t)
	first, err := NewHeadlessAgentHost(workspace, state)
	if err != nil {
		t.Fatalf("first NewHeadlessAgentHost: %v", err)
	}
	if _, err := NewHeadlessAgentHost(workspace, state); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("concurrent NewHeadlessAgentHost error = %v, want ErrNotAllowed", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	reopened, err := NewHeadlessAgentHost(workspace, state)
	if err != nil {
		t.Fatalf("reopen after Close: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("reopened Close: %v", err)
	}
}
