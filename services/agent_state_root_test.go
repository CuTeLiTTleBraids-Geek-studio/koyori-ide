package services

import (
	"errors"
	"os"
	"testing"
)

func TestValidAgentStateLeafRejectsPlatformSpecificPathSyntax(t *testing.T) {
	tests := []string{
		"",
		".",
		"..",
		"nested/leaf",
		`nested\leaf`,
		"C:leaf",
	}
	for _, name := range tests {
		if validAgentStateLeaf(name) {
			t.Errorf("validAgentStateLeaf(%q) = true, want false", name)
		}
	}
	if !validAgentStateLeaf("agent_lifecycle_sessions.json") {
		t.Fatal("validAgentStateLeaf rejected a canonical state leaf")
	}
}

func TestRootBoundInstanceLockRecoversEmptyCrashFile(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(state+string(os.PathSeparator)+"koyori-ide.lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	lock := NewInstanceLockWithRoot(state, root)
	if err := lock.Acquire(); err != nil {
		t.Fatalf("Acquire after empty crash lock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestRootBoundInstanceLockMissingIdentityIsPoisoned(t *testing.T) {
	state := t.TempDir()
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	lock := NewInstanceLockWithRoot(state, root)
	if err := lock.removeLockFile(nil); !errors.Is(err, ErrUsagePersistencePoisoned) {
		t.Fatalf("removeLockFile without identity = %v, want ErrUsagePersistencePoisoned", err)
	}
}

func TestRootBoundInstanceLockReleaseIdentityFailureIsPoisoned(t *testing.T) {
	state := t.TempDir()
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	lock := NewInstanceLockWithRoot(state, root)
	if err := lock.Acquire(); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.file.Close(); err != nil {
		t.Fatalf("pre-close lock handle: %v", err)
	}
	firstErr := lock.Release()
	if !errors.Is(firstErr, ErrUsagePersistencePoisoned) {
		t.Fatalf("Release after identity loss = %v, want ErrUsagePersistencePoisoned", firstErr)
	}
	secondErr := lock.Release()
	if !errors.Is(secondErr, ErrUsagePersistencePoisoned) {
		t.Fatalf("second Release after identity loss = %v, want stable ErrUsagePersistencePoisoned", secondErr)
	}
}

func TestRootBoundInstanceLockRemovalOpenFailureIsPoisoned(t *testing.T) {
	state := t.TempDir()
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	lock := NewInstanceLockWithRoot(state, root)
	expected, err := os.Stat(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lock.removeLockFile(expected); !errors.Is(err, ErrUsagePersistencePoisoned) {
		t.Fatalf("removeLockFile with unavailable root = %v, want ErrUsagePersistencePoisoned", err)
	}
}
