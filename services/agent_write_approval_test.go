package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// G-02: RequestWriteApproval + ExecuteApprovedWrite security contract tests.
// All tests follow the red→green pattern: they must compile and assert that
// the security contract is enforced.
// ---------------------------------------------------------------------------

func newAutoApproveWriteAgent(t *testing.T, root string) *AgentService {
	t.Helper()
	svc := NewAgentService()
	t.Cleanup(func() { _ = svc.Close() })
	if root != "" {
		if err := svc.configureWorkspaceRoot(root); err != nil {
			t.Fatalf("SetWorkspaceRoot: %v", err)
		}
	}
	// auto-approve for happy-path tests
	svc.approveWrite = func(targetPath string, size int64) bool { return true }
	return svc
}

func TestAgentService_RequestWriteApproval_RejectsEmptyRoot(t *testing.T) {
	// Given – no workspace root
	svc := NewAgentService()
	t.Cleanup(func() { _ = svc.Close() })
	svc.approveWrite = func(string, int64) bool { return true }

	// When
	_, err := svc.requestWriteApprovalLegacy("some/file.txt", "deadbeef", 10)

	// Then
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("RequestWriteApproval without root = %v, want ErrNotAllowed", err)
	}
}

func TestAgentService_RequestWriteApproval_RejectsOutsidePath(t *testing.T) {
	// Given
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "evil.txt")
	svc := newAutoApproveWriteAgent(t, root)

	// When
	_, err := svc.requestWriteApprovalLegacy(outside, "deadbeef", 5)

	// Then
	if err == nil {
		t.Fatal("RequestWriteApproval with path outside root should fail")
	}
}

func TestAgentService_RequestWriteApproval_RefusesWhenUserDeclines(t *testing.T) {
	// Given
	root := t.TempDir()
	svc := NewAgentService()
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.configureWorkspaceRoot(root); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	svc.approveWrite = func(string, int64) bool { return false } // user declines

	// When
	_, err := svc.requestWriteApprovalLegacy(filepath.Join(root, "file.txt"), "deadbeef", 4)

	// Then
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("RequestWriteApproval on decline = %v, want ErrNotAllowed", err)
	}
}

func TestAgentService_ExecuteApprovedWrite_RejectsEmptyToken(t *testing.T) {
	// Given
	root := t.TempDir()
	svc := newAutoApproveWriteAgent(t, root)

	// When
	err := svc.executeApprovedWriteLegacy(filepath.Join(root, "f.txt"), "content", "")

	// Then
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ExecuteApprovedWrite with empty token = %v, want ErrInvalidInput", err)
	}
}

func TestAgentService_ExecuteApprovedWrite_RejectsForgedToken(t *testing.T) {
	// Given
	root := t.TempDir()
	svc := newAutoApproveWriteAgent(t, root)

	// When – random token that was never issued
	err := svc.executeApprovedWriteLegacy(filepath.Join(root, "f.txt"), "content", "aaaaaaaaaaaaaaaa")

	// Then
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ExecuteApprovedWrite with forged token = %v, want ErrInvalidInput", err)
	}
}

func TestAgentService_ExecuteApprovedWrite_RejectsReplayToken(t *testing.T) {
	// Given
	root := t.TempDir()
	target := filepath.Join(root, "replay.txt")
	svc := newAutoApproveWriteAgent(t, root)

	content := "hello replay"
	h := contentHashString(content)
	token, err := svc.requestWriteApprovalLegacy(target, h, int64(len(content)))
	if err != nil {
		t.Fatalf("RequestWriteApproval: %v", err)
	}

	// First use succeeds
	if err := svc.executeApprovedWriteLegacy(target, content, token); err != nil {
		t.Fatalf("first ExecuteApprovedWrite: %v", err)
	}

	// When – replay
	err = svc.executeApprovedWriteLegacy(target, content, token)

	// Then
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("replay ExecuteApprovedWrite = %v, want ErrInvalidInput", err)
	}
}

func TestAgentService_ExecuteApprovedWrite_RejectsCrossGenerationToken(t *testing.T) {
	// Given – mint token, then switch workspace (bumps generation), then try to use
	root := t.TempDir()
	target := filepath.Join(root, "stale.txt")
	svc := newAutoApproveWriteAgent(t, root)

	content := "stale-gen"
	h := contentHashString(content)
	token, err := svc.requestWriteApprovalLegacy(target, h, int64(len(content)))
	if err != nil {
		t.Fatalf("RequestWriteApproval: %v", err)
	}

	// Switch workspace → new generation
	newRoot := t.TempDir()
	if err := svc.configureWorkspaceRoot(newRoot); err != nil {
		t.Fatalf("SetWorkspaceRoot new: %v", err)
	}

	// When – use old token with new generation
	err = svc.executeApprovedWriteLegacy(filepath.Join(newRoot, "stale.txt"), content, token)

	// Then
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-generation ExecuteApprovedWrite = %v, want ErrInvalidInput", err)
	}
}

func TestAgentService_ExecuteApprovedWrite_RejectsChangedPath(t *testing.T) {
	// Given – token minted for target A, execution attempts B
	root := t.TempDir()
	targetA := filepath.Join(root, "a.txt")
	targetB := filepath.Join(root, "b.txt")
	svc := newAutoApproveWriteAgent(t, root)

	content := "path swap"
	h := contentHashString(content)
	token, err := svc.requestWriteApprovalLegacy(targetA, h, int64(len(content)))
	if err != nil {
		t.Fatalf("RequestWriteApproval: %v", err)
	}

	// When – execute against different path
	err = svc.executeApprovedWriteLegacy(targetB, content, token)

	// Then
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-path ExecuteApprovedWrite = %v, want ErrInvalidInput", err)
	}
}

func TestAgentService_ExecuteApprovedWrite_RejectsHashMismatch(t *testing.T) {
	// Given – token bound to hash of "original" content
	root := t.TempDir()
	target := filepath.Join(root, "hash.txt")
	svc := newAutoApproveWriteAgent(t, root)

	original := "original content"
	h := contentHashString(original)
	token, err := svc.requestWriteApprovalLegacy(target, h, int64(len(original)))
	if err != nil {
		t.Fatalf("RequestWriteApproval: %v", err)
	}

	// When – execute with different content
	err = svc.executeApprovedWriteLegacy(target, "tampered content", token)

	// Then
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("hash-mismatch ExecuteApprovedWrite = %v, want ErrInvalidInput", err)
	}
}

func TestAgentService_ExecuteApprovedWrite_RejectsSizeMismatch(t *testing.T) {
	// Given
	root := t.TempDir()
	target := filepath.Join(root, "size.txt")
	svc := newAutoApproveWriteAgent(t, root)
	content := "larger than approved"
	token, err := svc.requestWriteApprovalLegacy(target, contentHashString(content), 1)
	if err != nil {
		t.Fatalf("RequestWriteApproval: %v", err)
	}

	// When
	err = svc.executeApprovedWriteLegacy(target, content, token)

	// Then
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("size-mismatch ExecuteApprovedWrite = %v, want ErrInvalidInput", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("size-mismatch write created target: %v", statErr)
	}
}

func TestAgentService_ExecuteApprovedWrite_RejectsExpiredToken(t *testing.T) {
	// Given – mint a token, then manually expire it
	root := t.TempDir()
	target := filepath.Join(root, "expire.txt")
	svc := newAutoApproveWriteAgent(t, root)

	content := "about to expire"
	h := contentHashString(content)
	token, err := svc.requestWriteApprovalLegacy(target, h, int64(len(content)))
	if err != nil {
		t.Fatalf("RequestWriteApproval: %v", err)
	}

	// Expire the token
	svc.writeApprovalMu.Lock()
	if a, ok := svc.writeApprovals[token]; ok {
		a.expiresAt = time.Now().Add(-time.Second)
		svc.writeApprovals[token] = a
	}
	svc.writeApprovalMu.Unlock()

	// When
	err = svc.executeApprovedWriteLegacy(target, content, token)

	// Then
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expired ExecuteApprovedWrite = %v, want ErrInvalidInput", err)
	}
}
