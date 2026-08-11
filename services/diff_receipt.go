package services

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const diffReceiptFilePrefix = "ai-diff-"

// commitReceiptWriter returns the optional durable receipt hook used by the
// AI diff transaction. Receipts are keyed by workspace identity so opening a
// different workspace cannot consume the previous workspace's state.
func commitReceiptWriter(receiptDir string) func(CommitReceipt) error {
	if strings.TrimSpace(receiptDir) == "" {
		return nil
	}
	return func(receipt CommitReceipt) error {
		root, err := canonicalizeWorkspaceRoot(receipt.WorkspaceRoot)
		if err != nil {
			return fmt.Errorf("canonicalize receipt workspace: %w", err)
		}
		receipt.WorkspaceRoot = root
		path, err := commitReceiptPath(receiptDir, root)
		if err != nil {
			return err
		}
		return atomicWriteJSON(path, receipt, 0600)
	}
}

func commitReceiptPath(receiptDir, root string) (string, error) {
	if strings.TrimSpace(receiptDir) == "" {
		return "", fmt.Errorf("receipt directory is required: %w", ErrInvalidInput)
	}
	canonical, err := canonicalizeWorkspaceRoot(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize receipt root: %w", err)
	}
	key := canonical
	if runtime.GOOS == "windows" {
		key = normalizeWindowsWorkspaceIdentityPath(key)
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(receiptDir, diffReceiptFilePrefix+hex.EncodeToString(sum[:])+".json"), nil
}

func (s *DiffService) activeWorkspaceRoot() (string, error) {
	s.mu.Lock()
	ctx := s.wsCtx
	fallback := s.workspaceRoot
	s.mu.Unlock()
	if ctx != nil {
		return ctx.RequireRoot()
	}
	if strings.TrimSpace(fallback) == "" {
		return "", fmt.Errorf("no workspace is open: %w", ErrNotAllowed)
	}
	return canonicalizeWorkspaceRoot(fallback)
}

// GetLatestCommitReceipt loads and verifies the most recent AI diff receipt
// for the active workspace. It reads from disk on every call, which makes it
// usable after a process restart without relying on an in-memory cache.
func (s *DiffService) GetLatestCommitReceipt() (CommitReceipt, error) {
	var empty CommitReceipt
	root, err := s.activeWorkspaceRoot()
	if err != nil {
		return empty, err
	}
	s.mu.Lock()
	receiptDir := s.receiptDir
	s.mu.Unlock()
	if strings.TrimSpace(receiptDir) == "" {
		return empty, fmt.Errorf("commit receipts are not configured: %w", ErrNoCommitReceipt)
	}
	path, err := commitReceiptPath(receiptDir, root)
	if err != nil {
		return empty, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return empty, fmt.Errorf("%s: %w", path, ErrNoCommitReceipt)
	}
	if err != nil {
		return empty, fmt.Errorf("read commit receipt: %w", err)
	}
	var receipt CommitReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return empty, fmt.Errorf("decode commit receipt: %w: %v", ErrInvalidCommitReceipt, err)
	}
	if err := validateCommitReceipt(receipt, root); err != nil {
		return empty, err
	}
	return receipt, nil
}

func validateCommitReceipt(receipt CommitReceipt, activeRoot string) error {
	if len(receipt.TransactionID) != 32 {
		return fmt.Errorf("transaction id has invalid length: %w", ErrInvalidCommitReceipt)
	}
	if _, err := hex.DecodeString(receipt.TransactionID); err != nil {
		return fmt.Errorf("transaction id is not hexadecimal: %w", ErrInvalidCommitReceipt)
	}
	if receipt.CommittedAt.IsZero() {
		return fmt.Errorf("committed timestamp is missing: %w", ErrInvalidCommitReceipt)
	}
	if !sameWorkspaceIdentityPath(receipt.WorkspaceRoot, activeRoot) {
		return fmt.Errorf("receipt belongs to a different workspace: %w", ErrNotAllowed)
	}
	if len(receipt.AppliedFiles) != len(receipt.FileHashes) {
		return fmt.Errorf("receipt file list and hashes differ: %w", ErrInvalidCommitReceipt)
	}
	seen := make(map[string]struct{}, len(receipt.AppliedFiles))
	for _, path := range receipt.AppliedFiles {
		if _, duplicate := seen[path]; duplicate || strings.TrimSpace(path) == "" {
			return fmt.Errorf("receipt contains a duplicate or empty path: %w", ErrInvalidCommitReceipt)
		}
		seen[path] = struct{}{}
		expected, ok := receipt.FileHashes[path]
		if !ok || len(expected) != sha256.Size*2 {
			return fmt.Errorf("receipt hash missing for %q: %w", path, ErrInvalidCommitReceipt)
		}
		if _, err := hex.DecodeString(expected); err != nil {
			return fmt.Errorf("receipt hash invalid for %q: %w", path, ErrInvalidCommitReceipt)
		}
		resolved, err := ValidateMutatingPathWithinRoot(activeRoot, path)
		if err != nil {
			return fmt.Errorf("receipt path %q is outside workspace: %w", path, ErrInvalidCommitReceipt)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return fmt.Errorf("read receipt file %q: %w: %v", path, ErrInvalidCommitReceipt, err)
		}
		actual := contentHash(data)
		if !strings.EqualFold(actual, expected) {
			return fmt.Errorf("receipt hash mismatch for %q: %w", path, ErrInvalidCommitReceipt)
		}
	}
	return nil
}
