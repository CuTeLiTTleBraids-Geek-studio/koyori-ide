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
	t.Cleanup(func() { _ = service.close() })
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
	service.writeAtomic = func() error { return injected }

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

func TestFileService_WriteFileIfUnchanged_CreatesMissingFileWithEmptyBaseline(t *testing.T) {
	service, root := newRootedFileService(t)
	target := filepath.Join(root, "created.txt")
	if err := service.WriteFileIfUnchanged(target, "created", contentHash(nil)); err != nil {
		t.Fatalf("WriteFileIfUnchanged missing target: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "created" {
		t.Fatalf("created target = %q, err=%v", data, err)
	}
}

func TestFileService_WriteFileIfUnchanged_RejectsCreationDuringPublish(t *testing.T) {
	service, root := newRootedFileService(t)
	target := filepath.Join(root, "created-race.txt")
	service.writeAtomic = func() error {
		return os.WriteFile(target, []byte("external create"), 0o600)
	}

	err := service.WriteFileIfUnchanged(target, "replacement", contentHash(nil))
	if !errors.Is(err, ErrFileConflict) {
		t.Fatalf("WriteFileIfUnchanged creation race error = %v, want ErrFileConflict", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "external create" {
		t.Fatalf("creation race target = %q, err=%v", data, readErr)
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

func TestFileService_WriteFileIfUnchanged_RejectsChangeDuringPublish(t *testing.T) {
	service, root := newRootedFileService(t)
	target := filepath.Join(root, "publish-race.txt")
	if err := os.WriteFile(target, []byte("baseline"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	service.writeAtomic = func() error {
		return os.WriteFile(target, []byte("external update"), 0o600)
	}

	err := service.WriteFileIfUnchanged(target, "replacement", contentHash([]byte("baseline")))

	if !errors.Is(err, ErrFileConflict) {
		t.Fatalf("WriteFileIfUnchanged error = %v, want ErrFileConflict", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "external update" {
		t.Fatalf("target after publish race = %q, err=%v", data, readErr)
	}
}

func TestFileService_WriteFileIfUnchanged_RejectsSameContentReplacementDuringPublish(t *testing.T) {
	service, root := newRootedFileService(t)
	target := filepath.Join(root, "publish-aba.txt")
	detached := filepath.Join(root, "publish-aba-detached.txt")
	if err := os.WriteFile(target, []byte("baseline"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	service.writeAtomic = func() error {
		if err := os.Rename(target, detached); err != nil {
			return err
		}
		return os.WriteFile(target, []byte("baseline"), 0o600)
	}

	err := service.WriteFileIfUnchanged(target, "replacement", contentHash([]byte("baseline")))

	if !errors.Is(err, ErrFileConflict) {
		t.Fatalf("WriteFileIfUnchanged error = %v, want ErrFileConflict", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "baseline" {
		t.Fatalf("replacement target after ABA race = %q, err=%v", data, readErr)
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

func TestFileService_WriteFileIfUnchanged_RejectsWorkspaceGenerationSwitch(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	target := filepath.Join(rootA, "generation.txt")
	if err := os.WriteFile(target, []byte("baseline"), 0o600); err != nil {
		t.Fatal(err)
	}
	context := NewWorkspaceContext()
	if err := context.Set(rootA); err != nil {
		t.Fatal(err)
	}
	service := NewFileServiceWithWorkspaceContext(context)
	t.Cleanup(func() { _ = service.close() })
	if err := service.setWorkspaceRoot(rootA); err != nil {
		t.Fatal(err)
	}
	service.rootOperationHook = func(operation string) error {
		if operation == "WriteFileIfUnchanged" {
			return context.Set(rootB)
		}
		return nil
	}
	err := service.WriteFileIfUnchanged(target, "replacement", contentHash([]byte("baseline")))
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("generation switch error = %v, want ErrNotAllowed", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "baseline" {
		t.Fatalf("target after generation switch = %q, err=%v", data, readErr)
	}
}
