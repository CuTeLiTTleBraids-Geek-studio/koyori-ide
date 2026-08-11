package services

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// GOAL-P1-01 regression tests.
//
// Baseline defect: RestoreSnapshot only wrote snapshot files back and never
// removed files created after the snapshot, so "restore snapshot" left the
// workspace in a state that was neither the snapshot nor the pre-restore state.
// The UI called that a full workspace restore, which is a false claim.

func TestCalculateRestoreDiff_ReportsAddedModifiedRemoved(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, "manual")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// Add a file that did not exist at snapshot time.
	if err := os.WriteFile(filepath.Join(ws, "added.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write added.go: %v", err)
	}
	// Modify a file that did exist.
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main // edited\n"), 0o644); err != nil {
		t.Fatalf("modify main.go: %v", err)
	}
	// Delete a file that existed.
	if err := os.Remove(filepath.Join(ws, "README.md")); err != nil {
		t.Fatalf("remove README.md: %v", err)
	}

	diff, err := svc.CalculateRestoreDiff(snap.ID, ws)
	if err != nil {
		t.Fatalf("CalculateRestoreDiff: %v", err)
	}

	if got := diff.AddedAfterSnapshot; len(got) != 1 || got[0] != "added.go" {
		t.Fatalf("AddedAfterSnapshot = %v, want [added.go]", got)
	}
	if got := diff.ModifiedSinceSnapshot; len(got) != 1 || got[0] != "main.go" {
		t.Fatalf("ModifiedSinceSnapshot = %v, want [main.go]", got)
	}
	if got := diff.RemovedFromWorkspace; len(got) != 1 || got[0] != "README.md" {
		t.Fatalf("RemovedFromWorkspace = %v, want [README.md]", got)
	}
}

func TestCalculateRestoreDiff_DoesNotModifyWorkspace(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, "manual")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	addedPath := filepath.Join(ws, "added.go")
	if err := os.WriteFile(addedPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write added.go: %v", err)
	}

	if _, err := svc.CalculateRestoreDiff(snap.ID, ws); err != nil {
		t.Fatalf("CalculateRestoreDiff: %v", err)
	}

	// A preview must be side-effect free: the file it reports for deletion
	// must still be present.
	if _, err := os.Stat(addedPath); err != nil {
		t.Fatalf("preview deleted added.go: %v", err)
	}
}

func TestRestoreSnapshotExact_RemovesPostSnapshotAdditions(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, "manual")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	addedPath := filepath.Join(ws, "added.go")
	if err := os.WriteFile(addedPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write added.go: %v", err)
	}
	nestedAdded := filepath.Join(ws, "src", "extra.go")
	if err := os.WriteFile(nestedAdded, []byte("package utils\n"), 0o644); err != nil {
		t.Fatalf("write src/extra.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("edited\n"), 0o644); err != nil {
		t.Fatalf("modify main.go: %v", err)
	}

	if err := svc.RestoreSnapshotExact(snap.ID, ws, true); err != nil {
		t.Fatalf("RestoreSnapshotExact: %v", err)
	}

	// The core AC: post-snapshot additions must be gone, not silently kept.
	if _, err := os.Stat(addedPath); !os.IsNotExist(err) {
		t.Fatalf("added.go survived an exact restore (err=%v)", err)
	}
	if _, err := os.Stat(nestedAdded); !os.IsNotExist(err) {
		t.Fatalf("src/extra.go survived an exact restore (err=%v)", err)
	}
	// And snapshot content must be restored.
	got, err := os.ReadFile(filepath.Join(ws, "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if string(got) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("main.go = %q, want the snapshot content", string(got))
	}
}

func TestRestoreSnapshotExact_RequiresExplicitConfirmation(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, "manual")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	addedPath := filepath.Join(ws, "added.go")
	if err := os.WriteFile(addedPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write added.go: %v", err)
	}

	err = svc.RestoreSnapshotExact(snap.ID, ws, false)
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("unconfirmed exact restore error = %v, want ErrNotAllowed", err)
	}

	// Cancelling must not modify the workspace at all.
	if _, statErr := os.Stat(addedPath); statErr != nil {
		t.Fatalf("unconfirmed restore deleted added.go: %v", statErr)
	}
}

func TestRestoreSnapshotExact_RejectsForeignWorkspace(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)
	other := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, "manual")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// Restoring snapshot A into workspace B would delete B's files using A's
	// manifest. That must fail closed.
	err = svc.RestoreSnapshotExact(snap.ID, other, true)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("foreign-workspace exact restore error = %v, want ErrInvalidInput", err)
	}
	if _, statErr := os.Stat(filepath.Join(other, "main.go")); statErr != nil {
		t.Fatalf("foreign-workspace restore damaged the other workspace: %v", statErr)
	}
}

func TestRestoreSnapshotExact_IgnoresFilteredDirectories(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, "manual")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// node_modules is excluded from the manifest by isIgnoredDir. An exact
	// restore must not treat it as a post-snapshot addition and delete it:
	// the snapshot never claimed to manage it.
	nodeModules := filepath.Join(ws, "node_modules", "pkg")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	depFile := filepath.Join(nodeModules, "index.js")
	if err := os.WriteFile(depFile, []byte("module.exports = 1;\n"), 0o644); err != nil {
		t.Fatalf("write dep: %v", err)
	}

	if err := svc.RestoreSnapshotExact(snap.ID, ws, true); err != nil {
		t.Fatalf("RestoreSnapshotExact: %v", err)
	}

	if _, err := os.Stat(depFile); err != nil {
		t.Fatalf("exact restore deleted an ignored-directory file: %v", err)
	}
}

func TestRestoreSnapshotExact_RestoresDeletedSnapshotFiles(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, "manual")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// Delete a whole subtree that the snapshot recorded.
	if err := os.RemoveAll(filepath.Join(ws, "src")); err != nil {
		t.Fatalf("remove src: %v", err)
	}

	if err := svc.RestoreSnapshotExact(snap.ID, ws, true); err != nil {
		t.Fatalf("RestoreSnapshotExact: %v", err)
	}

	for _, rel := range []string{"src/utils.go", "src/const.go"} {
		if _, err := os.Stat(filepath.Join(ws, rel)); err != nil {
			t.Fatalf("exact restore did not recreate %s: %v", rel, err)
		}
	}
}

func TestRestoreSnapshotExact_IsIdempotent(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, "manual")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "added.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write added.go: %v", err)
	}

	if err := svc.RestoreSnapshotExact(snap.ID, ws, true); err != nil {
		t.Fatalf("first RestoreSnapshotExact: %v", err)
	}
	// A second exact restore on an already-exact workspace must be a no-op
	// rather than an error, so retrying after a partial failure is safe.
	if err := svc.RestoreSnapshotExact(snap.ID, ws, true); err != nil {
		t.Fatalf("second RestoreSnapshotExact: %v", err)
	}

	diff, err := svc.CalculateRestoreDiff(snap.ID, ws)
	if err != nil {
		t.Fatalf("CalculateRestoreDiff: %v", err)
	}
	if len(diff.AddedAfterSnapshot) != 0 ||
		len(diff.ModifiedSinceSnapshot) != 0 ||
		len(diff.RemovedFromWorkspace) != 0 {
		t.Fatalf("workspace not exact after restore: %+v", diff)
	}
}

func TestRestoreSnapshotExactLeavesLegacyRestoreSemanticsIntact(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, "manual")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	addedPath := filepath.Join(ws, "added.go")
	if err := os.WriteFile(addedPath, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write added.go: %v", err)
	}

	// RestoreSnapshot keeps its documented partial semantics. It is retained
	// deliberately: renaming it would break its Wails binding, and callers that
	// want file-only restore still have a correct API. The distinction is now
	// explicit rather than implied.
	if err := svc.RestoreSnapshot(snap.ID, ws); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	if _, err := os.Stat(addedPath); err != nil {
		t.Fatalf("partial restore unexpectedly deleted added.go: %v", err)
	}
}

func TestCalculateRestoreDiff_SortedOutputIsStable(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, "manual")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	for _, name := range []string{"z.go", "a.go", "m.go"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	diff, err := svc.CalculateRestoreDiff(snap.ID, ws)
	if err != nil {
		t.Fatalf("CalculateRestoreDiff: %v", err)
	}
	got := append([]string(nil), diff.AddedAfterSnapshot...)
	want := append([]string(nil), got...)
	sort.Strings(want)
	// A deletion preview shown to a user must have a deterministic order;
	// map-iteration order would make the confirmation dialog reshuffle.
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("AddedAfterSnapshot = %v, want sorted %v", got, want)
		}
	}
}
