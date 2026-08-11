package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiffService_CommitReceiptSurvivesServiceRecreation(t *testing.T) {
	root, _ := tempWorkspace(t, map[string]string{"target.txt": "old\n"})
	receiptDir := t.TempDir()
	ctx := NewWorkspaceContext()
	if err := ctx.Set(root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.txt")
	diff := FileDiff{Path: target, OldContent: "old\n", NewContent: "new\n"}

	first := NewDiffServiceWithReceiptDir(receiptDir)
	first.setWorkspaceContext(ctx)
	committed := first.ApplyDiff([]FileDiff{diff})
	if !committed.Applied {
		t.Fatalf("ApplyDiff = %+v", committed)
	}

	second := NewDiffServiceWithReceiptDir(receiptDir)
	second.setWorkspaceContext(ctx)
	receipt, err := second.GetLatestCommitReceipt()
	if err != nil {
		t.Fatalf("GetLatestCommitReceipt after recreation: %v", err)
	}
	if receipt.TransactionID != committed.TransactionID {
		t.Fatalf("transaction id = %q, want %q", receipt.TransactionID, committed.TransactionID)
	}
	if receipt.FileHashes[target] != committed.FileHashes[target] {
		t.Fatalf("receipt hash = %q, want %q", receipt.FileHashes[target], committed.FileHashes[target])
	}

	duplicate := second.ApplyDiff([]FileDiff{diff})
	if duplicate.Applied {
		t.Fatalf("duplicate ApplyDiff unexpectedly committed: %+v", duplicate)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "new\n" {
		t.Fatalf("disk after duplicate = %q, err=%v", string(got), err)
	}
}

func TestDiffService_CommitReceiptRejectsDiskDrift(t *testing.T) {
	root, _ := tempWorkspace(t, map[string]string{"target.txt": "old\n"})
	receiptDir := t.TempDir()
	ctx := NewWorkspaceContext()
	if err := ctx.Set(root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.txt")
	service := NewDiffServiceWithReceiptDir(receiptDir)
	service.setWorkspaceContext(ctx)
	if result := service.ApplyDiff([]FileDiff{{
		Path: target, OldContent: "old\n", NewContent: "new\n",
	}}); !result.Applied {
		t.Fatalf("ApplyDiff = %+v", result)
	}
	if err := os.WriteFile(target, []byte("changed outside receipt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetLatestCommitReceipt(); !errors.Is(err, ErrInvalidCommitReceipt) {
		t.Fatalf("GetLatestCommitReceipt error = %v, want ErrInvalidCommitReceipt", err)
	}
}

func TestDiffService_ReceiptPersistenceFailureRollsBack(t *testing.T) {
	root, _ := tempWorkspace(t, map[string]string{"target.txt": "old\n"})
	notADirectory := filepath.Join(t.TempDir(), "receipt-file")
	if err := os.WriteFile(notADirectory, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := NewWorkspaceContext()
	if err := ctx.Set(root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.txt")
	service := NewDiffServiceWithReceiptDir(notADirectory)
	service.setWorkspaceContext(ctx)
	result := service.ApplyDiff([]FileDiff{{
		Path: target, OldContent: "old\n", NewContent: "new\n",
	}})
	if result.Applied || !result.RollbackAttempted || !result.RolledBack {
		t.Fatalf("receipt persistence failure result = %+v", result)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "old\n" {
		t.Fatalf("disk after receipt failure = %q, err=%v", string(got), err)
	}
}
