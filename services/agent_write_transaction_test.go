package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentService_ExecuteApprovedWrite_RejectsDiskChangeAfterApproval(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "conflict.txt")
	if err := os.WriteFile(target, []byte("approved baseline"), 0o600); err != nil {
		t.Fatalf("write approved baseline: %v", err)
	}
	svc := newAutoApproveWriteAgent(t, root)
	content := "agent replacement"
	token, err := svc.requestWriteApprovalLegacy(target, contentHashString(content), int64(len(content)))
	if err != nil {
		t.Fatalf("RequestWriteApproval: %v", err)
	}
	if err := os.WriteFile(target, []byte("external update"), 0o600); err != nil {
		t.Fatalf("write external update: %v", err)
	}

	err = svc.executeApprovedWriteLegacy(target, content, token)

	if err == nil {
		t.Fatal("approved write overwrote a disk change made after approval")
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "external update" {
		t.Fatalf("conflict target = %q, err=%v", data, readErr)
	}
}

func TestAgentService_ExecuteApprovedWrite_RejectsEmptyFileCreatedAfterApproval(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "created-empty.txt")
	svc := newAutoApproveWriteAgent(t, root)
	content := "agent replacement"
	token, err := svc.requestWriteApprovalLegacy(target, contentHashString(content), int64(len(content)))
	if err != nil {
		t.Fatalf("RequestWriteApproval: %v", err)
	}
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("create empty target: %v", err)
	}

	err = svc.executeApprovedWriteLegacy(target, content, token)

	if err == nil {
		t.Fatal("approved new-file write overwrote a file created after approval")
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || len(data) != 0 {
		t.Fatalf("created target = %q, err=%v", data, readErr)
	}
}

func TestAgentService_ExecuteApprovedWrite_RejectsEmptyFileDeletedAfterApproval(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "deleted-empty.txt")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("create approved target: %v", err)
	}
	svc := newAutoApproveWriteAgent(t, root)
	content := "agent replacement"
	token, err := svc.requestWriteApprovalLegacy(target, contentHashString(content), int64(len(content)))
	if err != nil {
		t.Fatalf("RequestWriteApproval: %v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatalf("delete approved target: %v", err)
	}

	err = svc.executeApprovedWriteLegacy(target, content, token)

	if err == nil {
		t.Fatal("approved existing-file write recreated a file deleted after approval")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("deleted target was recreated: %v", statErr)
	}
}

func TestAgentService_ExecuteApprovedWrite_HappyPath_WritesFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "hello.txt")
	svc := newAutoApproveWriteAgent(t, root)
	content := "hello world"
	token, err := svc.requestWriteApprovalLegacy(target, contentHashString(content), int64(len(content)))
	if err != nil {
		t.Fatalf("RequestWriteApproval: %v", err)
	}

	if err := svc.executeApprovedWriteLegacy(target, content, token); err != nil {
		t.Fatalf("ExecuteApprovedWrite: %v", err)
	}

	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != content {
		t.Fatalf("file content = %q, err=%v", data, readErr)
	}
}

func TestAgentService_ExecuteApprovedWrite_RejectsOutsidePath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "evil.txt")
	svc := newAutoApproveWriteAgent(t, root)
	content := "evil"
	svc.writeApprovalMu.Lock()
	if svc.writeApprovals == nil {
		svc.writeApprovals = make(map[string]writeApproval)
	}
	svc.mu.Lock()
	generation := svc.rootGeneration
	svc.mu.Unlock()
	svc.writeApprovals["bypass-token"] = writeApproval{
		targetPath: outside, contentHash: contentHashString(content), size: int64(len(content)),
		rootGeneration: generation, expiresAt: time.Now().Add(2 * time.Minute),
	}
	svc.writeApprovalMu.Unlock()

	err := svc.executeApprovedWriteLegacy(outside, content, "bypass-token")

	if err == nil {
		t.Fatal("ExecuteApprovedWrite with path outside workspace root should fail")
	}
}
