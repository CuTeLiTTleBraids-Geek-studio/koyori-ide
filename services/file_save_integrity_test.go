package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newRootedFileService(t *testing.T) (*FileService, string) {
	t.Helper()
	root := t.TempDir()
	service := NewFileService()
	if err := service.setWorkspaceRoot(root); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	return service, root
}

func TestFileService_WriteFile_PreservesExistingFileWhenAtomicWriteFails(t *testing.T) {
	// Given
	service, root := newRootedFileService(t)
	target := filepath.Join(root, "preserved.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	injected := errors.New("injected atomic write failure")
	service.writeAtomic = func(string, []byte, os.FileMode) error { return injected }

	// When
	err := service.WriteFile(target, "replacement")

	// Then
	if !errors.Is(err, injected) {
		t.Fatalf("WriteFile error = %v, want injected failure", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "original" {
		t.Fatalf("target after failure = %q, err=%v", data, readErr)
	}
}

func TestFileService_WriteFile_PreservesExistingMode(t *testing.T) {
	// Given
	service, root := newRootedFileService(t)
	target := filepath.Join(root, "script.sh")
	if err := os.WriteFile(target, []byte("old"), 0o750); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target before save: %v", err)
	}

	// When
	if err := service.WriteFile(target, "new"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Then
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if info.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("target mode = %o, want preserved mode %o", info.Mode().Perm(), before.Mode().Perm())
	}
}

func TestFileService_WriteFileIfUnchanged_WritesMatchingBaseline(t *testing.T) {
	// Given
	service, root := newRootedFileService(t)
	target := filepath.Join(root, "matching.txt")
	if err := os.WriteFile(target, []byte("baseline"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	// When
	err := service.WriteFileIfUnchanged(target, "replacement", contentHash([]byte("baseline")))

	// Then
	if err != nil {
		t.Fatalf("WriteFileIfUnchanged: %v", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "replacement" {
		t.Fatalf("target = %q, err=%v", data, readErr)
	}
}

func TestFileService_WriteFileIfUnchanged_RejectsDiskConflict(t *testing.T) {
	// Given
	service, root := newRootedFileService(t)
	target := filepath.Join(root, "conflict.txt")
	if err := os.WriteFile(target, []byte("external update"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	// When
	err := service.WriteFileIfUnchanged(target, "replacement", contentHash([]byte("old baseline")))

	// Then
	if !errors.Is(err, ErrFileConflict) {
		t.Fatalf("WriteFileIfUnchanged error = %v, want ErrFileConflict", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "external update" {
		t.Fatalf("conflict target = %q, err=%v", data, readErr)
	}
}

func TestFileService_WriteFileIfUnchanged_RejectsOutsideWorkspace(t *testing.T) {
	// Given
	service, _ := newRootedFileService(t)
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	// When
	err := service.WriteFileIfUnchanged(target, "replacement", contentHash([]byte("outside")))

	// Then
	if err == nil {
		t.Fatal("WriteFileIfUnchanged accepted a path outside the workspace")
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "outside" {
		t.Fatalf("outside target = %q, err=%v", data, readErr)
	}
}
