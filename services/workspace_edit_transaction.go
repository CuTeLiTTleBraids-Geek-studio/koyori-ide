package services

// workspace_edit_transaction.go — GOAL-P1-04R
//
// Unified workspace edit transaction for all write entry points:
//   - LSP rename / code action  (ApplyRefactorWorkspaceEdit)
//   - AI diff apply             (DiffService.applyDiffTransaction)
//   - Search-replace batch      (SearchService.applyMultiFileReplaceTransaction)
//
// Guarantees:
//   - All paths validated against workspace root (fail-closed, pathsec.go)
//   - Dirty-buffer check before every text edit
//   - Hash / version preconditions checked before any write
//   - LIFO rollback on any write failure (text edits + resource ops)
//   - create/rename/delete resource operations with conflict detection

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

// ResourceOpKind classifies a workspace resource operation.
type ResourceOpKind string

const (
	ResourceOpCreate ResourceOpKind = "create"
	ResourceOpRename ResourceOpKind = "rename"
	ResourceOpDelete ResourceOpKind = "delete"
)

// ResourceOp describes a single file-system resource operation in a workspace
// edit. All paths must lie within the workspace root; the transaction enforces
// this before any write is attempted.
type ResourceOp struct {
	Kind ResourceOpKind
	// OldPath is the rename source or delete target.
	OldPath string
	// NewPath is the create target or rename destination.
	NewPath string
	// ExpectedHash must match the current disk hash before a delete proceeds.
	// Empty means no hash check.
	ExpectedHash string
	// IgnoreIfExists suppresses "target already exists" conflict for create.
	IgnoreIfExists bool
	// IgnoreIfNotExists suppresses "source not found" conflict for rename/delete.
	IgnoreIfNotExists bool
}

// EditTransactionOptions holds the injectable I/O callbacks used by
// ApplyEditTransaction. Injecting no-op or in-memory implementations makes
// the transaction fully testable without touching the real filesystem.
type EditTransactionOptions struct {
	// Root is the active workspace root. Every path in the transaction is
	// validated against it. An empty Root causes an immediate failure.
	Root string

	// Version resolves the current LSP document version for a path.
	// nil = version checks skipped for all files.
	Version func(path string) (int, bool)

	// Read reads a file from disk (required).
	Read func(path string) (string, error)

	// Write writes a file to disk. Implementations should be atomic
	// (temp-file + rename) to minimise partial-write exposure (required).
	Write func(path, content string) error

	// Rename moves a file. nil = os.Rename.
	Rename func(oldPath, newPath string) error

	// Remove deletes a file. nil = os.Remove.
	Remove func(path string) error

	// IsDirty returns true when the path has in-memory (Monaco buffer) content
	// that differs from disk. A dirty file must not be silently overwritten.
	// nil = dirty check disabled.
	IsDirty func(path string) bool

	// OnCommit persists a commit receipt after all edits are written and before
	// the transaction is reported as applied. A failure rolls the edit back so
	// callers never receive a committed result without its receipt.
	OnCommit func(CommitReceipt) error
}

// CommitReceipt is the durable proof of a successful workspace edit. The
// receipt is validated against disk before it is returned after a restart.
type CommitReceipt struct {
	TransactionID string            `json:"transactionId"`
	WorkspaceRoot string            `json:"workspaceRoot"`
	AppliedFiles  []string          `json:"appliedFiles"`
	FileHashes    map[string]string `json:"fileHashes"`
	CommittedAt   time.Time         `json:"committedAt"`
}

// EditTransaction groups multi-file text edits and optional resource
// operations for atomic application with LIFO rollback on failure.
type EditTransaction struct {
	// TextEdits holds the multi-file text edit preview (path + baseline hash +
	// modified content). Files not present here are untouched.
	TextEdits WorkspaceEditPreview

	// ResourceOps are applied after all text edits succeed, in declaration order.
	ResourceOps []ResourceOp
}

// appliedResourceOp records enough state to undo a resource operation.
type appliedResourceOp struct {
	kind     ResourceOpKind
	oldPath  string
	newPath  string
	original string // stored content for delete rollback
}

// ApplyEditTransaction applies text edits and resource operations atomically.
//
// Execution order:
//  1. Validate every path against opts.Root (fail-closed).
//  2. For each text edit: duplicate check, dirty-buffer check, version check,
//     hash check (reads current disk content as rollback baseline).
//  3. For each resource op: existence / hash precondition checks.
//  4. Apply text edits. On failure: LIFO rollback of applied text edits.
//  5. Apply resource ops. On failure: LIFO rollback of all applied ops
//     (resource ops first, then text edits).
//
// The function is intentionally package-private to force callers to go
// through the domain-specific helpers (ApplyRefactorWorkspaceEdit,
// applyDiffTransaction, applyMultiFileReplaceTransaction).
func applyEditTransaction(ctx context.Context, txn EditTransaction, opts EditTransactionOptions) WorkspaceEditApplyResult {
	result := WorkspaceEditApplyResult{AppliedFiles: []string{}, Conflicts: []string{}}

	if err := ctx.Err(); err != nil {
		result.Err = err
		result.FailureReason = err.Error()
		return result
	}

	// Fail-closed: empty root means "no workspace open".
	if opts.Root == "" {
		err := fmt.Errorf("workspace root is required: %w", ErrNotAllowed)
		result.Err = err
		result.FailureReason = err.Error()
		return result
	}

	// lspTransactionRootSentinel marks callers that already performed path
	// validation externally (LSP path: fsvc.validateMutatingPath). Skip the
	// redundant root check for that sentinel only; all other roots go through
	// ValidatePathWithinRoot normally.
	preValidated := opts.Root == lspTransactionRootSentinel
	validatePath := func(path string) error {
		if preValidated {
			return nil
		}
		_, err := ValidatePathWithinRoot(opts.Root, path)
		return err
	}

	// Effective rename/remove: fall back to OS implementations.
	renameFile := opts.Rename
	if renameFile == nil {
		renameFile = os.Rename
	}
	removeFile := opts.Remove
	if removeFile == nil {
		removeFile = func(p string) error { return os.Remove(p) }
	}

	// ── Phase 1: Path security ─────────────────────────────────────────────────
	for _, f := range txn.TextEdits.Files {
		if err := validatePath(f.FilePath); err != nil {
			result.Conflicts = append(result.Conflicts, f.FilePath+": "+err.Error())
		}
	}
	for _, op := range txn.ResourceOps {
		if op.OldPath != "" {
			if err := validatePath(op.OldPath); err != nil {
				result.Conflicts = append(result.Conflicts, op.OldPath+": "+err.Error())
			}
		}
		if op.NewPath != "" {
			if err := validatePath(op.NewPath); err != nil {
				result.Conflicts = append(result.Conflicts, op.NewPath+": "+err.Error())
			}
		}
	}
	if len(result.Conflicts) > 0 {
		result.FailureReason = "path security violation"
		return result
	}

	// ── Phase 2: Text-edit preconditions ──────────────────────────────────────
	seen := make(map[string]struct{}, len(txn.TextEdits.Files))
	current := make(map[string]string, len(txn.TextEdits.Files))

	for _, f := range txn.TextEdits.Files {
		if _, dup := seen[f.FilePath]; dup {
			result.Conflicts = append(result.Conflicts, f.FilePath+": duplicate workspace edit")
			continue
		}
		seen[f.FilePath] = struct{}{}

		// Dirty-buffer check: a file with unsaved Monaco content must not be
		// silently overwritten by a disk edit.
		if opts.IsDirty != nil && opts.IsDirty(f.FilePath) {
			result.Conflicts = append(result.Conflicts,
				f.FilePath+": dirty buffer conflicts with disk edit")
			continue
		}

		// LSP document version check (optional).
		if f.Version != nil && opts.Version != nil {
			got, ok := opts.Version(f.FilePath)
			if !ok || got != *f.Version {
				result.Conflicts = append(result.Conflicts, fmt.Sprintf(
					"%s: version conflict (want %d, got %d)", f.FilePath, *f.Version, got))
				continue
			}
		}

		// Content hash check + capture rollback baseline.
		content, err := opts.Read(f.FilePath)
		if err != nil {
			result.Conflicts = append(result.Conflicts,
				f.FilePath+": read error: "+err.Error())
			continue
		}
		if contentHash([]byte(content)) != f.BaselineHash {
			result.Conflicts = append(result.Conflicts, f.FilePath+": hash conflict")
			continue
		}
		current[f.FilePath] = content
	}

	// ── Phase 3: Resource-op preconditions ────────────────────────────────────
	resourceOriginals := make(map[string]string)
	for _, op := range txn.ResourceOps {
		switch op.Kind {
		case ResourceOpCreate:
			if _, err := os.Stat(op.NewPath); err == nil && !op.IgnoreIfExists {
				result.Conflicts = append(result.Conflicts,
					op.NewPath+": target already exists (create conflict)")
			}
		case ResourceOpRename:
			info, err := os.Stat(op.OldPath)
			if os.IsNotExist(err) {
				if !op.IgnoreIfNotExists {
					result.Conflicts = append(result.Conflicts,
						op.OldPath+": source not found (rename conflict)")
				}
			} else if err == nil && info != nil {
				if _, err2 := os.Stat(op.NewPath); err2 == nil {
					result.Conflicts = append(result.Conflicts,
						op.NewPath+": rename target already exists")
				}
			}
		case ResourceOpDelete:
			content, err := opts.Read(op.OldPath)
			if err != nil {
				if os.IsNotExist(err) && op.IgnoreIfNotExists {
					continue
				}
				result.Conflicts = append(result.Conflicts,
					op.OldPath+": read error before delete: "+err.Error())
				continue
			}
			if op.ExpectedHash != "" && contentHash([]byte(content)) != op.ExpectedHash {
				result.Conflicts = append(result.Conflicts,
					op.OldPath+": delete hash conflict")
				continue
			}
			resourceOriginals[op.OldPath] = content
		}
	}

	if len(result.Conflicts) > 0 {
		result.FailureReason = "workspace edit conflict"
		return result
	}

	// Allocate the receipt before the first write. If secure ID generation
	// fails, no disk mutation has happened and the transaction can fail cleanly.
	id, idErr := newCommitReceiptID()
	if idErr != nil {
		result.Err = idErr
		result.FailureReason = "generate commit receipt: " + idErr.Error()
		return result
	}
	receipt := CommitReceipt{
		TransactionID: id,
		WorkspaceRoot: opts.Root,
		AppliedFiles:  make([]string, 0, len(txn.TextEdits.Files)),
		FileHashes:    make(map[string]string, len(txn.TextEdits.Files)),
		CommittedAt:   time.Now().UTC(),
	}
	for _, file := range txn.TextEdits.Files {
		receipt.AppliedFiles = append(receipt.AppliedFiles, file.FilePath)
		receipt.FileHashes[file.FilePath] = contentHash([]byte(file.ModifiedContent))
	}

	// ── Phase 4: Apply text edits (with LIFO rollback on failure) ─────────────
	applied := make([]string, 0, len(txn.TextEdits.Files))
	for _, f := range txn.TextEdits.Files {
		if err := ctx.Err(); err != nil {
			result.Err = err
			result.FailureReason = err.Error()
			break
		}
		if err := opts.Write(f.FilePath, f.ModifiedContent); err != nil {
			result.Err = err
			result.FailureReason = err.Error()
			break
		}
		applied = append(applied, f.FilePath)
	}
	if result.FailureReason != "" {
		result.RollbackAttempted = len(applied) > 0
		result.RolledBack = result.RollbackAttempted
		for i := len(applied) - 1; i >= 0; i-- {
			path := applied[i]
			if err := opts.Write(path, current[path]); err != nil {
				result.RolledBack = false
				result.FailureReason += "; rollback " + path + ": " + err.Error()
			}
		}
		return result
	}

	// ── Phase 5: Apply resource ops (with LIFO rollback on failure) ───────────
	appliedOps := make([]appliedResourceOp, 0, len(txn.ResourceOps))
	for _, op := range txn.ResourceOps {
		if err := ctx.Err(); err != nil {
			result.Err = err
			result.FailureReason = err.Error()
			break
		}
		switch op.Kind {
		case ResourceOpCreate:
			if _, statErr := os.Stat(op.NewPath); statErr == nil && op.IgnoreIfExists {
				continue // already exists and that is OK
			}
			if err := opts.Write(op.NewPath, ""); err != nil {
				result.Err = err
				result.FailureReason = "create " + op.NewPath + ": " + err.Error()
			} else {
				appliedOps = append(appliedOps, appliedResourceOp{
					kind: ResourceOpCreate, newPath: op.NewPath,
				})
			}
		case ResourceOpRename:
			if _, statErr := os.Stat(op.OldPath); os.IsNotExist(statErr) && op.IgnoreIfNotExists {
				continue
			}
			if err := renameFile(op.OldPath, op.NewPath); err != nil {
				result.Err = err
				result.FailureReason = fmt.Sprintf("rename %s -> %s: %v", op.OldPath, op.NewPath, err)
			} else {
				appliedOps = append(appliedOps, appliedResourceOp{
					kind: ResourceOpRename, oldPath: op.OldPath, newPath: op.NewPath,
				})
			}
		case ResourceOpDelete:
			original, hasOriginal := resourceOriginals[op.OldPath]
			if _, statErr := os.Stat(op.OldPath); os.IsNotExist(statErr) && op.IgnoreIfNotExists {
				continue
			}
			if err := removeFile(op.OldPath); err != nil {
				result.Err = err
				result.FailureReason = "delete " + op.OldPath + ": " + err.Error()
			} else if hasOriginal {
				appliedOps = append(appliedOps, appliedResourceOp{
					kind: ResourceOpDelete, oldPath: op.OldPath, original: original,
				})
			}
		}
		if result.FailureReason != "" {
			break
		}
	}

	if result.FailureReason != "" {
		// LIFO rollback: resource ops first (in reverse), then text edits.
		rollbackApplied := func() {
			result.RollbackAttempted = len(appliedOps) > 0 || len(applied) > 0
			result.RolledBack = result.RollbackAttempted
			for i := len(appliedOps) - 1; i >= 0; i-- {
				aop := appliedOps[i]
				var rollbackErr error
				switch aop.kind {
				case ResourceOpCreate:
					rollbackErr = removeFile(aop.newPath)
				case ResourceOpRename:
					rollbackErr = renameFile(aop.newPath, aop.oldPath)
				case ResourceOpDelete:
					rollbackErr = opts.Write(aop.oldPath, aop.original)
				}
				if rollbackErr != nil {
					result.RolledBack = false
					result.FailureReason += "; rollback resource operation: " + rollbackErr.Error()
				}
			}
			for i := len(applied) - 1; i >= 0; i-- {
				path := applied[i]
				if err := opts.Write(path, current[path]); err != nil {
					result.RolledBack = false
					result.FailureReason += "; rollback " + path + ": " + err.Error()
				}
			}
		}
		rollbackApplied()
		return result
	}

	// G18: create the receipt before reporting success. The callback persists it
	// after all disk writes; a persistence failure uses the same rollback path as
	// any other post-write failure.
	if opts.OnCommit != nil {
		if err := opts.OnCommit(receipt); err != nil {
			result.Err = err
			result.FailureReason = "persist commit receipt: " + err.Error()
			result.RollbackAttempted = len(appliedOps) > 0 || len(applied) > 0
			result.RolledBack = result.RollbackAttempted
			for i := len(appliedOps) - 1; i >= 0; i-- {
				aop := appliedOps[i]
				var rollbackErr error
				switch aop.kind {
				case ResourceOpCreate:
					rollbackErr = removeFile(aop.newPath)
				case ResourceOpRename:
					rollbackErr = renameFile(aop.newPath, aop.oldPath)
				case ResourceOpDelete:
					rollbackErr = opts.Write(aop.oldPath, aop.original)
				}
				if rollbackErr != nil {
					result.RolledBack = false
					result.FailureReason += "; rollback resource operation: " + rollbackErr.Error()
				}
			}
			for i := len(applied) - 1; i >= 0; i-- {
				path := applied[i]
				if err := opts.Write(path, current[path]); err != nil {
					result.RolledBack = false
					result.FailureReason += "; rollback " + path + ": " + err.Error()
				}
			}
			return result
		}
	}
	result.Applied = true
	result.AppliedFiles = append(result.AppliedFiles, receipt.AppliedFiles...)
	result.TransactionID = receipt.TransactionID
	result.FileHashes = receipt.FileHashes
	return result
}

// newCommitReceiptID returns a random hex id for a commit receipt (G18).
func newCommitReceiptID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
