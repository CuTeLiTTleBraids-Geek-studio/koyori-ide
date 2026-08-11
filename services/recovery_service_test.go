package services

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GOAL-P0-03 unit tests for RecoveryService.

func newTestRecoveryService(t *testing.T) (*RecoveryService, *WorkspaceContext, string) {
	t.Helper()
	configDir := t.TempDir()
	svc := NewRecoveryService(configDir)
	ctx := NewWorkspaceContext()
	svc.setWorkspaceContext(ctx)
	wsDir := t.TempDir()
	if err := ctx.Set(wsDir); err != nil {
		t.Fatalf("ctx.Set: %v", err)
	}
	return svc, ctx, wsDir
}

// TestRecoveryService_SaveAndScan writes one dirty buffer and verifies the
// scan returns it with the correct clean status.
func TestRecoveryService_SaveAndScan(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)

	path := filepath.Join(wsDir, "main.go")
	content := "package main\n// unsaved edit\n"
	if err := os.WriteFile(path, []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	baseline, err := svc.ComputeBaseline(path)
	if err != nil {
		t.Fatalf("ComputeBaseline: %v", err)
	}

	if err := svc.SaveDirtyBuffer("win1", path, content, "utf-8", "lf",
		baseline.Mtime, baseline.Hash); err != nil {
		t.Fatalf("SaveDirtyBuffer: %v", err)
	}

	scan, err := svc.ScanRecoverable()
	if err != nil {
		t.Fatalf("ScanRecoverable: %v", err)
	}
	if len(scan.Files) != 1 {
		t.Fatalf("scan.Files len = %d, want 1", len(scan.Files))
	}
	if scan.Files[0].Status != RecoveryStatusClean {
		t.Fatalf("status = %q, want %q", scan.Files[0].Status, RecoveryStatusClean)
	}
	if scan.Files[0].Content != content {
		t.Fatalf("content mismatch")
	}
	if len(scan.Corrupt) != 0 {
		t.Fatalf("corrupt = %v, want none", scan.Corrupt)
	}
}

// TestRecoveryService_ConflictWhenDiskChanged verifies that if the file was
// modified on disk after the buffer was opened, the scan returns Conflict.
func TestRecoveryService_ConflictWhenDiskChanged(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)

	path := filepath.Join(wsDir, "file.go")
	if err := os.WriteFile(path, []byte("v1"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	baseline, err := svc.ComputeBaseline(path)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}

	if err := svc.SaveDirtyBuffer("win1", path, "unsaved", "", "", baseline.Mtime, baseline.Hash); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Simulate an out-of-band write to the file while the user was editing.
	if err := os.WriteFile(path, []byte("v2 — external change"), 0o600); err != nil {
		t.Fatalf("external write: %v", err)
	}

	scan, err := svc.ScanRecoverable()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(scan.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(scan.Files))
	}
	if scan.Files[0].Status != RecoveryStatusConflict {
		t.Fatalf("status = %q, want %q", scan.Files[0].Status, RecoveryStatusConflict)
	}
	if scan.Files[0].DiskContent == "" {
		t.Fatal("DiskContent must be populated for conflict")
	}
}

// TestRecoveryService_MissingFileStatus verifies that a file deleted from disk
// between crash and restart surfaces as Missing, not an error.
func TestRecoveryService_MissingFileStatus(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)

	path := filepath.Join(wsDir, "deleted.go")
	if err := os.WriteFile(path, []byte("exists"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	baseline, _ := svc.ComputeBaseline(path)
	if err := svc.SaveDirtyBuffer("win1", path, "content", "", "", baseline.Mtime, baseline.Hash); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	scan, err := svc.ScanRecoverable()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(scan.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(scan.Files))
	}
	if scan.Files[0].Status != RecoveryStatusMissing {
		t.Fatalf("status = %q, want %q", scan.Files[0].Status, RecoveryStatusMissing)
	}
}

// TestRecoveryService_ClearAfterSave verifies ClearDirtyBuffer removes the
// record and ScanRecoverable returns empty.
func TestRecoveryService_ClearAfterSave(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)
	if _, err := svc.ScanRecoverable(); err != nil {
		t.Fatalf("complete startup scan: %v", err)
	}

	path := filepath.Join(wsDir, "saved.go")
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	baseline, _ := svc.ComputeBaseline(path)
	if err := svc.SaveDirtyBuffer("win1", path, "dirty", "", "", baseline.Mtime, baseline.Hash); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := svc.ClearDirtyBuffer("win1", path); err != nil {
		t.Fatalf("clear: %v", err)
	}
	scan, _ := svc.ScanRecoverable()
	if len(scan.Files) != 0 {
		t.Fatalf("scan after clear = %v, want empty", scan.Files)
	}
}

// TestRecoveryService_CorruptRecordIsolated verifies that a corrupt JSON file
// does not abort the scan; other valid records are still returned.
func TestRecoveryService_CorruptRecordIsolated(t *testing.T) {
	svc, ctx, wsDir := newTestRecoveryService(t)
	wsHash := hashContent([]byte(ctx.Root()))

	// Write a valid record via the API.
	validPath := filepath.Join(wsDir, "good.go")
	if err := os.WriteFile(validPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	baseline, _ := svc.ComputeBaseline(validPath)
	if err := svc.SaveDirtyBuffer("win1", validPath, "edit", "", "", baseline.Mtime, baseline.Hash); err != nil {
		t.Fatalf("save valid: %v", err)
	}

	// Inject a corrupt record by writing garbage JSON directly.
	corruptDir := filepath.Join(svc.rootDir, wsHash, "win1")
	if err := os.WriteFile(filepath.Join(corruptDir, "corrupt.json"),
		[]byte(`not json`), 0o600); err != nil {
		t.Fatalf("inject corrupt: %v", err)
	}

	scan, err := svc.ScanRecoverable()
	if err != nil {
		t.Fatalf("scan must not fail on corrupt record: %v", err)
	}
	if len(scan.Files) != 1 {
		t.Fatalf("valid files = %d, want 1", len(scan.Files))
	}
	if len(scan.Corrupt) != 1 {
		t.Fatalf("corrupt = %d, want 1", len(scan.Corrupt))
	}
}

// TestRecoveryService_QuotaRecord rejects a single buffer that exceeds the per-
// record size limit.
func TestRecoveryService_QuotaRecord(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)

	path := filepath.Join(wsDir, "big.bin")
	if err := os.WriteFile(path, []byte("seed"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	baseline, _ := svc.ComputeBaseline(path)

	huge := strings.Repeat("x", maxRecoveryRecordBytes+1)
	err := svc.SaveDirtyBuffer("win1", path, huge, "", "", baseline.Mtime, baseline.Hash)
	if !errors.Is(err, ErrRecoveryQuota) {
		t.Fatalf("error = %v, want ErrRecoveryQuota", err)
	}
}

// TestRecoveryService_SensitivePathNotJournaled verifies that .env and key
// files are silently skipped rather than their contents copied to configDir.
func TestRecoveryService_SensitivePathNotJournaled(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)

	for _, name := range []string{".env", ".env.local", "id_rsa", "secret.pem", "private.key"} {
		p := filepath.Join(wsDir, name)
		if err := os.WriteFile(p, []byte("secret material"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if err := svc.SaveDirtyBuffer("win1", p, "leaked", "", "", 0, ""); err != nil {
			t.Fatalf("SaveDirtyBuffer %s returned unexpected error: %v", name, err)
		}
	}

	scan, _ := svc.ScanRecoverable()
	if len(scan.Files) != 0 {
		t.Fatalf("sensitive files appeared in scan: %v", scan.Files)
	}
}

// TestRecoveryService_MultiWindowIsolation verifies that two windows editing
// the same path maintain independent records.
func TestRecoveryService_MultiWindowIsolation(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)

	path := filepath.Join(wsDir, "shared.go")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	baseline, _ := svc.ComputeBaseline(path)

	if err := svc.SaveDirtyBuffer("winA", path, "edit from A", "", "", baseline.Mtime, baseline.Hash); err != nil {
		t.Fatalf("save A: %v", err)
	}
	if err := svc.SaveDirtyBuffer("winB", path, "edit from B", "", "", baseline.Mtime, baseline.Hash); err != nil {
		t.Fatalf("save B: %v", err)
	}

	scan, _ := svc.ScanRecoverable()
	if len(scan.Files) != 2 {
		t.Fatalf("files = %d, want 2 (one per window)", len(scan.Files))
	}
	contents := map[string]string{}
	for _, f := range scan.Files {
		contents[f.WindowID] = f.Content
	}
	if contents["winA"] != "edit from A" || contents["winB"] != "edit from B" {
		t.Fatalf("contents = %v, isolation broken", contents)
	}

	decisions := make([]RecoveryDecision, 0, len(scan.Files))
	for _, file := range scan.Files {
		decisions = append(decisions, RecoveryDecision{WindowID: file.WindowID, Path: file.Path})
	}
	if err := svc.CompleteRecovery(decisions, nil); err != nil {
		t.Fatalf("complete explicit recovery: %v", err)
	}

	// Once startup recovery is resolved, clearing one current-session window
	// journal must not affect the other.
	if err := svc.SaveDirtyBuffer("winA", path, "edit from A", "", "", baseline.Mtime, baseline.Hash); err != nil {
		t.Fatalf("save A after recovery: %v", err)
	}
	if err := svc.SaveDirtyBuffer("winB", path, "edit from B", "", "", baseline.Mtime, baseline.Hash); err != nil {
		t.Fatalf("save B after recovery: %v", err)
	}
	if err := svc.ClearWindowJournal("winA"); err != nil {
		t.Fatalf("clear winA: %v", err)
	}
	scan2, _ := svc.ScanRecoverable()
	if len(scan2.Files) != 1 || scan2.Files[0].WindowID != "winB" {
		t.Fatalf("after clear winA, files = %v, want only winB", scan2.Files)
	}
}

// TestRecoveryService_FailsClosedWithNoWorkspace verifies that a missing
// workspace context returns ErrNotAllowed rather than writing to a bad path.
func TestRecoveryService_FailsClosedWithNoWorkspace(t *testing.T) {
	svc := NewRecoveryService(t.TempDir())
	// No SetWorkspaceContext call — wsCtx is nil.
	_, err := svc.ScanRecoverable()
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ScanRecoverable with nil ctx = %v, want ErrNotAllowed", err)
	}

	ctx := NewWorkspaceContext()
	svc.setWorkspaceContext(ctx)
	// Context exists but no workspace is open.
	_, err = svc.ScanRecoverable()
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ScanRecoverable with empty ctx = %v, want ErrNotAllowed", err)
	}
}

// TestRecoveryService_ComputeBaselineHandlesMissingFile verifies that opening
// a brand-new buffer returns Exists=false rather than an error.
func TestRecoveryService_ComputeBaselineHandlesMissingFile(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)

	baseline, err := svc.ComputeBaseline(filepath.Join(wsDir, "new.go"))
	if err != nil {
		t.Fatalf("ComputeBaseline on missing file: %v", err)
	}
	if baseline.Exists {
		t.Fatal("Exists must be false for a missing file")
	}
}

// TestRecoveryService_RecordSchemaVersionCheck verifies that a record with an
// unrecognised schema version ends up in the Corrupt bucket.
func TestRecoveryService_RecordSchemaVersionCheck(t *testing.T) {
	svc, ctx, wsDir := newTestRecoveryService(t)
	wsHash := hashContent([]byte(ctx.Root()))

	winDir := filepath.Join(svc.rootDir, wsHash, "win1")
	if err := os.MkdirAll(winDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	future := DirtyBufferRecord{
		SchemaVersion: 9999,
		Path:          filepath.Join(wsDir, "future.go"),
		Content:       "future content",
	}
	data, _ := json.Marshal(future)
	if err := os.WriteFile(filepath.Join(winDir, "future.json"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	scan, err := svc.ScanRecoverable()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(scan.Files) != 0 {
		t.Fatalf("future record should not appear as recoverable")
	}
	if len(scan.Corrupt) != 1 {
		t.Fatalf("corrupt = %d, want 1 (future schema)", len(scan.Corrupt))
	}
}
