package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func saveG04RecoveryRecord(
	t *testing.T,
	svc *RecoveryService,
	windowID string,
	path string,
	disk string,
	dirty string,
) RecoveryDecision {
	t.Helper()
	if err := os.WriteFile(path, []byte(disk), 0o600); err != nil {
		t.Fatalf("write disk fixture: %v", err)
	}
	baseline, err := svc.ComputeBaseline(path)
	if err != nil {
		t.Fatalf("compute baseline: %v", err)
	}
	if err := svc.SaveDirtyBuffer(
		windowID,
		path,
		dirty,
		"utf-8",
		"lf",
		baseline.Mtime,
		baseline.Hash,
	); err != nil {
		t.Fatalf("save dirty buffer: %v", err)
	}
	return RecoveryDecision{WindowID: windowID, Path: path}
}

func TestG04RecoveryLifecycleRequiresExactExplicitCompletion(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)
	if state := svc.GetRecoveryState(); state.Phase != RecoveryPhaseScanning {
		t.Fatalf("initial phase = %q, want %q", state.Phase, RecoveryPhaseScanning)
	}

	decisionA := saveG04RecoveryRecord(
		t, svc, "crashed-a", filepath.Join(wsDir, "a.txt"), "disk-a", "dirty-a",
	)
	decisionB := saveG04RecoveryRecord(
		t, svc, "crashed-b", filepath.Join(wsDir, "b.txt"), "disk-b", "dirty-b",
	)
	scan, err := svc.ScanRecoverable()
	if err != nil {
		t.Fatalf("scan recoverable: %v", err)
	}
	if len(scan.Files) != 2 {
		t.Fatalf("scan files = %d, want 2", len(scan.Files))
	}
	state := svc.GetRecoveryState()
	if state.Phase != RecoveryPhasePending || state.PendingCount != 2 {
		t.Fatalf("pending state = %+v", state)
	}
	if err := svc.requireResolved("automatic save"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("pending automatic save error = %v, want ErrNotAllowed", err)
	}

	if err := svc.CompleteRecovery([]RecoveryDecision{decisionA}, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("incomplete decision error = %v, want ErrInvalidInput", err)
	}
	if state := svc.GetRecoveryState(); state.Phase != RecoveryPhasePending {
		t.Fatalf("phase after incomplete completion = %q, want pending", state.Phase)
	}

	if err := svc.CompleteRecovery([]RecoveryDecision{decisionA, decisionB}, nil); err != nil {
		t.Fatalf("complete recovery: %v", err)
	}
	if state := svc.GetRecoveryState(); state.Phase != RecoveryPhaseResolved {
		t.Fatalf("phase after completion = %q, want resolved", state.Phase)
	}
	rescan, err := svc.ScanRecoverable()
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if len(rescan.Files) != 0 || len(rescan.Corrupt) != 0 {
		t.Fatalf("completed records appeared again: %+v", rescan)
	}
}

func TestG04RecoveryCompletionFailureKeepsOriginalSnapshot(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)
	path := filepath.Join(wsDir, "main.txt")
	decision := saveG04RecoveryRecord(t, svc, "crashed", path, "disk", "dirty")
	if _, err := svc.ScanRecoverable(); err != nil {
		t.Fatalf("scan recoverable: %v", err)
	}
	winDir, _, err := svc.windowDir(decision.WindowID)
	if err != nil {
		t.Fatalf("window dir: %v", err)
	}
	recordPath := filepath.Join(winDir, hashContent([]byte(path))+".json")
	if err := os.WriteFile(recordPath, []byte("{broken"), 0o600); err != nil {
		t.Fatalf("corrupt pending record: %v", err)
	}

	if err := svc.CompleteRecovery([]RecoveryDecision{decision}, nil); err == nil {
		t.Fatal("CompleteRecovery unexpectedly succeeded for a damaged record")
	}
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("original recovery record was not preserved: %v", err)
	}
	if state := svc.GetRecoveryState(); state.Phase != RecoveryPhasePending {
		t.Fatalf("phase after failed completion = %q, want pending", state.Phase)
	}
}

func TestG04RecoveryScanFailureStaysClosedUntilAcknowledged(t *testing.T) {
	svc, _, _ := newTestRecoveryService(t)
	wsJournal, _, err := svc.workspaceDir()
	if err != nil {
		t.Fatalf("workspace dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(wsJournal), 0o700); err != nil {
		t.Fatalf("create recovery parent: %v", err)
	}
	if err := os.WriteFile(wsJournal, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create unreadable journal fixture: %v", err)
	}

	if _, err := svc.ScanRecoverable(); err == nil {
		t.Fatal("ScanRecoverable unexpectedly succeeded")
	}
	state := svc.GetRecoveryState()
	if state.Phase != RecoveryPhaseFailed || state.Error == "" {
		t.Fatalf("failed state = %+v", state)
	}
	if err := svc.requireResolved("window close"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("failed close gate error = %v, want ErrNotAllowed", err)
	}
	if err := svc.AcknowledgeRecoveryFailure(); err != nil {
		t.Fatalf("acknowledge recovery failure: %v", err)
	}
	if state := svc.GetRecoveryState(); state.Phase != RecoveryPhaseResolved {
		t.Fatalf("phase after acknowledgement = %q, want resolved", state.Phase)
	}
	if data, err := os.ReadFile(wsJournal); err != nil || string(data) != "not a directory" {
		t.Fatalf("scan failure fixture changed: data=%q err=%v", data, err)
	}
}

func TestG04WorkspaceGenerationStartsScanningAgain(t *testing.T) {
	svc, ctx, _ := newTestRecoveryService(t)
	if _, err := svc.ScanRecoverable(); err != nil {
		t.Fatalf("resolve first workspace: %v", err)
	}
	if state := svc.GetRecoveryState(); state.Phase != RecoveryPhaseResolved {
		t.Fatalf("first workspace phase = %q", state.Phase)
	}
	next := t.TempDir()
	if err := ctx.Set(next); err != nil {
		t.Fatalf("set next workspace: %v", err)
	}
	if state := svc.GetRecoveryState(); state.Phase != RecoveryPhaseScanning {
		t.Fatalf("new generation phase = %q, want scanning", state.Phase)
	}
}

func TestG04PendingRecoveryBlocksWorkspaceSwitchAndWindowClose(t *testing.T) {
	configDir := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	ctx := NewWorkspaceContext()
	if err := ctx.Set(workspaceA); err != nil {
		t.Fatalf("set workspace A: %v", err)
	}
	recovery := NewRecoveryService(configDir)
	recovery.setWorkspaceContext(ctx)
	project := &ProjectService{
		configPath:      filepath.Join(configDir, "projects.json"),
		wsCtx:           ctx,
		recoveryService: recovery,
	}
	window := &WindowService{}
	window.setRecoveryService(recovery)

	if _, err := project.AddProject(workspaceB); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("pending workspace switch error = %v, want ErrNotAllowed", err)
	}
	if !sameWorkspaceIdentityPath(ctx.Root(), workspaceA) {
		t.Fatalf("workspace changed while recovery pending: %q", ctx.Root())
	}
	if err := window.Close(); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("pending window close error = %v, want ErrNotAllowed", err)
	}

	if _, err := recovery.ScanRecoverable(); err != nil {
		t.Fatalf("scan empty recovery journal: %v", err)
	}
	if err := window.Close(); err != nil {
		t.Fatalf("resolved nil-window close: %v", err)
	}
	if _, err := project.AddProject(workspaceB); err != nil {
		t.Fatalf("resolved workspace switch: %v", err)
	}
	if !sameWorkspaceIdentityPath(ctx.Root(), workspaceB) {
		t.Fatalf("workspace after resolved switch = %q, want %q", ctx.Root(), workspaceB)
	}
}

func TestG04PendingRecoveryRejectsSnapshotCleanupBypasses(t *testing.T) {
	tests := []struct {
		name string
		act  func(*RecoveryService, RecoveryDecision) error
	}{
		{
			name: "clear dirty buffer",
			act: func(svc *RecoveryService, decision RecoveryDecision) error {
				return svc.ClearDirtyBuffer(decision.WindowID, decision.Path)
			},
		},
		{
			name: "clear window journal",
			act: func(svc *RecoveryService, decision RecoveryDecision) error {
				return svc.ClearWindowJournal(decision.WindowID)
			},
		},
		{
			name: "discard recovered session",
			act: func(svc *RecoveryService, decision RecoveryDecision) error {
				return svc.DiscardRecoveredSession(decision.WindowID)
			},
		},
		{
			name: "clear workspace journal",
			act: func(svc *RecoveryService, _ RecoveryDecision) error {
				return svc.ClearWorkspaceJournal()
			},
		},
		{
			name: "disable journal",
			act: func(svc *RecoveryService, _ RecoveryDecision) error {
				return svc.SetJournalEnabled(false)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, wsDir := newTestRecoveryService(t)
			path := filepath.Join(wsDir, "main.txt")
			decision := saveG04RecoveryRecord(t, svc, "crashed", path, "disk", "dirty")
			if _, err := svc.ScanRecoverable(); err != nil {
				t.Fatalf("scan recoverable: %v", err)
			}
			winDir, _, err := svc.windowDir(decision.WindowID)
			if err != nil {
				t.Fatalf("window dir: %v", err)
			}
			recordPath := filepath.Join(winDir, hashContent([]byte(path))+".json")

			if err := tt.act(svc, decision); !errors.Is(err, ErrNotAllowed) {
				t.Fatalf("cleanup error = %v, want ErrNotAllowed", err)
			}
			if _, err := os.Stat(recordPath); err != nil {
				t.Fatalf("pending recovery snapshot was removed: %v", err)
			}
			if !svc.IsJournalEnabled() {
				t.Fatal("journal was disabled while recovery was pending")
			}
			if state := svc.GetRecoveryState(); state.Phase != RecoveryPhasePending {
				t.Fatalf("phase after cleanup attempt = %q, want pending", state.Phase)
			}
		})
	}
}

func TestG04PendingRecoveryCannotOverwriteOriginalSnapshot(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)
	path := filepath.Join(wsDir, "main.txt")
	decision := saveG04RecoveryRecord(t, svc, "crashed", path, "disk", "original dirty")
	if _, err := svc.ScanRecoverable(); err != nil {
		t.Fatalf("scan recoverable: %v", err)
	}

	if err := svc.SaveDirtyBuffer(
		decision.WindowID,
		decision.Path,
		"replacement dirty",
		"utf-8",
		"lf",
		0,
		"",
	); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("overwrite pending snapshot error = %v, want ErrNotAllowed", err)
	}

	winDir, _, err := svc.windowDir(decision.WindowID)
	if err != nil {
		t.Fatalf("window dir: %v", err)
	}
	recordPath := filepath.Join(winDir, hashContent([]byte(path))+".json")
	record, err := decodeRecord(recordPath)
	if err != nil {
		t.Fatalf("decode original recovery record: %v", err)
	}
	if record.Content != "original dirty" {
		t.Fatalf("pending snapshot content = %q, want original dirty", record.Content)
	}
}

func TestG04PendingRecoveryAllowsIndependentCurrentWindowJournal(t *testing.T) {
	svc, _, wsDir := newTestRecoveryService(t)
	path := filepath.Join(wsDir, "main.txt")
	saveG04RecoveryRecord(t, svc, "crashed", path, "disk", "original dirty")
	if _, err := svc.ScanRecoverable(); err != nil {
		t.Fatalf("scan recoverable: %v", err)
	}

	if err := svc.SaveDirtyBuffer(
		"current",
		path,
		"current dirty",
		"utf-8",
		"lf",
		0,
		"",
	); err != nil {
		t.Fatalf("save independent current-window journal: %v", err)
	}
	if err := svc.ClearDirtyBuffer("current", path); err != nil {
		t.Fatalf("clear independent current-window journal: %v", err)
	}
	if state := svc.GetRecoveryState(); state.Phase != RecoveryPhasePending || state.PendingCount != 1 {
		t.Fatalf("pending state after independent journal cleanup = %+v", state)
	}
}
