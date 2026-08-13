package services

// agent_write_approval.go — G-02: backend write-capability for Agent tool calls.
//
// Problem: executeWriteTool in the frontend called fileService.writeFile directly,
// with no backend-issued token and no workspace-generation binding. Any "approved"
// flag in the renderer was front-end state only, violating "don't trust the renderer".
//
// Solution (mirrors the command-approval flow in agent_service.go):
//   - RequestWriteApproval(targetPath, contentHash, size): validates path against
//     the workspace root, prompts the user via a native dialog, mints a short-lived
//     single-use token bound to (absTargetPath, contentHash, workspace generation, TTL).
//   - ExecuteApprovedWrite(targetPath, content, token): verifies the token, checks
//     the content hash (replay protection), validates the path again, then writes via
//     applyEditTransaction to get the same LIFO-rollback and hash-conflict semantics
//     as all other write entry points.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// writeApprovalTTL is the time after which an unused write-approval token expires.
const writeApprovalTTL = 2 * time.Minute

// writeApproval is a short-lived, single-use capability minted by
// RequestWriteApproval. It binds a write operation to the exact content
// being written (contentHash), the resolved absolute target path, and the
// workspace generation at the time of approval.
type writeApproval struct {
	// targetPath is the resolved absolute path validated against the workspace root.
	targetPath string
	// contentHash is SHA-256(content) of the new content to be written.
	// Verified at execution time so the renderer cannot substitute different content
	// after the user approved the original.
	contentHash string
	size        int64
	// baselineHash is captured when approval is requested, before the user
	// authorizes the write. ExecuteApprovedWrite rejects a later disk change.
	baselineHash  string
	targetExisted bool
	// rootGeneration ensures the token cannot be redeemed after a workspace switch.
	rootGeneration uint64
	// expiresAt is the wall-clock deadline; tokens past this are rejected.
	expiresAt time.Time
}

// contentHashString returns the SHA-256 hex digest of s. Exported-by-value so
// tests and the frontend can compute the same hash before calling
// RequestWriteApproval.
func contentHashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// initWriteApprovals lazily initialises the write-approval map. Called under
// writeApprovalMu.
func (s *AgentService) initWriteApprovals() {
	if s.writeApprovals == nil {
		s.writeApprovals = make(map[string]writeApproval)
	}
}

// nativeWriteApproval shows a native OS dialog asking the user to approve an
// agent file write. It is the production value of AgentService.approveWrite.
func nativeWriteApproval(targetPath string, size int64) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Approve file write").SetMessage(
		fmt.Sprintf("Agent wants to write:\n%s\n(%d bytes)", targetPath, size),
	)
	dialog.AddButton("Yes").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("No").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

// RequestWriteApproval validates targetPath, prompts the user for approval, and
// mints a single-use token binding the write to (absPath, contentHash,
// workspace generation, TTL). The returned token must be passed unchanged to
// ExecuteApprovedWrite.
//
// The renderer must supply contentHash = SHA-256(newContent) to bind the token
// to the exact bytes the user approved. Size is displayed in the approval dialog.
func (s *AgentService) RequestWriteApproval(targetPath, contentHash string, size int64) (string, error) {
	if strings.TrimSpace(targetPath) == "" {
		return "", fmt.Errorf("target path is required: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(contentHash) == "" {
		return "", fmt.Errorf("content hash is required: %w", ErrInvalidInput)
	}
	if size < 0 {
		return "", fmt.Errorf("content size must not be negative: %w", ErrInvalidInput)
	}

	// Validate path and capture generation under the same lock.
	s.mu.Lock()
	root := s.rootDir
	generation := s.rootGeneration
	s.mu.Unlock()

	if root == "" {
		return "", fmt.Errorf("agent workspace root is not configured: %w", ErrNotAllowed)
	}

	absPath, err := ValidateMutatingPathWithinRoot(root, targetPath)
	if err != nil {
		return "", err
	}
	baselineData, baselineErr := os.ReadFile(absPath)
	targetExisted := baselineErr == nil
	if baselineErr != nil && !os.IsNotExist(baselineErr) {
		return "", fmt.Errorf("read write approval baseline: %w", baselineErr)
	}
	baselineHash := contentHashString(string(baselineData))

	approver := s.approveWrite
	if approver == nil {
		approver = nativeWriteApproval
	}
	if !approver(absPath, size) {
		return "", fmt.Errorf("file write was not approved: %w", ErrNotAllowed)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("create write approval token: %w", err)
	}
	token := hex.EncodeToString(raw)

	s.writeApprovalMu.Lock()
	s.initWriteApprovals()
	s.writeApprovals[token] = writeApproval{
		targetPath:     absPath,
		contentHash:    contentHash,
		size:           size,
		baselineHash:   baselineHash,
		targetExisted:  targetExisted,
		rootGeneration: generation,
		expiresAt:      time.Now().Add(writeApprovalTTL),
	}
	s.writeApprovalMu.Unlock()

	return token, nil
}

// ExecuteApprovedWrite verifies the token, checks the content hash, validates
// the path against the current workspace root, then writes the content via
// applyEditTransaction — the same LIFO-rollback path used by Diff, Search-replace,
// and LSP refactors.
//
// Rejection reasons: empty/forged/expired/replayed token, path mismatch, content
// hash mismatch, cross-generation token, path outside workspace root.
func (s *AgentService) ExecuteApprovedWrite(targetPath, content, token string) error {
	if token == "" {
		return fmt.Errorf("write approval token is required: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(targetPath) == "" {
		return fmt.Errorf("target path is required: %w", ErrInvalidInput)
	}

	// Consume the token (single-use — deleted before any write).
	s.writeApprovalMu.Lock()
	approval, ok := s.writeApprovals[token]
	if ok {
		delete(s.writeApprovals, token)
	}
	s.writeApprovalMu.Unlock()

	if !ok || time.Now().After(approval.expiresAt) {
		return fmt.Errorf("invalid or expired write approval token: %w", ErrInvalidInput)
	}

	// Generation check: reject if workspace was switched since approval.
	s.mu.Lock()
	root := s.rootDir
	currentGen := s.rootGeneration
	s.mu.Unlock()

	if approval.rootGeneration != currentGen {
		return fmt.Errorf("write approval was issued for a previous workspace: %w", ErrInvalidInput)
	}

	// Re-resolve target from what the caller supplies and compare to token.
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("resolve target path: %w", ErrInvalidInput)
	}
	absPath = filepath.Clean(absPath)
	if absPath != approval.targetPath {
		return fmt.Errorf("target path does not match write approval: %w", ErrInvalidInput)
	}

	// Content hash check: recompute and compare to token binding.
	actualHash := contentHashString(content)
	if actualHash != approval.contentHash {
		return fmt.Errorf("content hash does not match write approval: %w", ErrInvalidInput)
	}
	if int64(len([]byte(content))) != approval.size {
		return fmt.Errorf("content size does not match write approval: %w", ErrInvalidInput)
	}

	// pathsec check against current workspace root.
	if root == "" {
		return fmt.Errorf("agent workspace root is not configured: %w", ErrNotAllowed)
	}
	if _, err := ValidateMutatingPathWithinRoot(root, absPath); err != nil {
		return err
	}

	_, statErr := os.Stat(absPath)
	if approval.targetExisted && os.IsNotExist(statErr) {
		return fmt.Errorf("write target was deleted after approval: %w", ErrNotAllowed)
	}
	if !approval.targetExisted && statErr == nil {
		return fmt.Errorf("write target was created after approval: %w", ErrNotAllowed)
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("stat write target: %w", statErr)
	}

	txn := EditTransaction{
		TextEdits: WorkspaceEditPreview{
			Files: []WorkspaceEditPreviewFile{{
				FilePath:        absPath,
				BaselineHash:    approval.baselineHash,
				ModifiedContent: content,
			}},
		},
	}

	result := applyEditTransaction(context.Background(), txn, EditTransactionOptions{
		Root: root,
		Read: func(path string) (string, error) {
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				return "", nil // new file — empty baseline
			}
			return string(data), err
		},
		Write: func(path, c string) error {
			return atomicWriteFile(path, []byte(c), 0644)
		},
	})

	if !result.Applied {
		if result.Err != nil {
			return result.Err
		}
		return fmt.Errorf("write transaction failed: %s (conflicts: %v)",
			result.FailureReason, result.Conflicts)
	}
	return nil
}
