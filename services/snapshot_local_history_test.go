package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotRestoreRejectsDifferentWorkspace(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	source := createTestWorkspace(t)
	target := createTestWorkspace(t)
	snapshot, err := svc.CreateSnapshot(source, "file-save")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	err = svc.RestoreSnapshot(snapshot.ID, target)
	if err == nil || !strings.Contains(err.Error(), "different workspace") {
		t.Fatalf("expected cross-workspace restore rejection, got %v", err)
	}
}

func TestSnapshotRestorePartialRejectsDifferentWorkspace(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	source := createTestWorkspace(t)
	target := createTestWorkspace(t)
	targetMain := filepath.Join(target, "main.go")
	if err := os.WriteFile(targetMain, []byte("target content"), 0644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.CreateSnapshot(source, "file-save")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	err = svc.RestorePartial(snapshot.ID, target, []string{"main.go"})
	if err == nil || !strings.Contains(err.Error(), "different workspace") {
		t.Fatalf("expected cross-workspace partial restore rejection, got %v", err)
	}
	data, readErr := os.ReadFile(targetMain)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "target content" {
		t.Fatalf("target workspace was modified: %q", data)
	}
}
