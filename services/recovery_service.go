package services

// recovery_service.go — GOAL-P0-03: hot exit / dirty-buffer crash recovery.
//
// Problem this solves: unsaved editor content lived only in the renderer's
// reactive store (editorState.openFiles). Any WebView crash, OS kill, or power
// loss discarded it with no trace on disk.
//
// Layering (P0-03 execution point 9): CrashService writes panic reports and is
// explicitly NOT a content backup. It answers "why did the process die".
// RecoveryService answers "what was the user typing". The two never share
// storage, and a crash report is never treated as recoverable content.
//
// Design constraints:
//   - Records are keyed by (workspace, window, file) so two windows editing the
//     same path, or two workspaces containing the same relative path, never read
//     each other's content.
//   - Every record carries the disk baseline (mtime + SHA-256) captured when the
//     buffer was opened. Recovery compares that baseline against current disk
//     state and refuses to silently overwrite a file that changed underneath.
//   - Journal writes are atomic (temp + rename) with 0600 permissions, so a
//     half-written record can never surface as recoverable content, and dirty
//     source text is not world-readable.
//   - A corrupt or future-schema record is isolated and reported, never fatal:
//     the app must still start.
//   - Quotas are enforced per record and per workspace. Exceeding them returns a
//     typed error so the renderer can show a visible warning instead of silently
//     dropping the user's work.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// recoverySchemaVersion is the on-disk journal schema version. A record written
// by a newer version is isolated rather than guessed at.
const recoverySchemaVersion = 1

const recoveryCommitMarker = "COMMITTED.json"

const (
	// maxRecoveryRecordBytes caps a single dirty buffer. Editors already refuse
	// to open pathological files; the journal must not become the component that
	// fills the user's disk.
	maxRecoveryRecordBytes = 8 << 20 // 8 MiB
	// maxRecoveryWorkspaceBytes caps the whole journal for one workspace.
	maxRecoveryWorkspaceBytes = 64 << 20 // 64 MiB
	// maxRecoveryRecords caps record count so a runaway loop cannot create
	// unbounded inodes.
	maxRecoveryRecords = 500
)

// ErrRecoveryQuota reports that a journal write was refused because it would
// exceed a size or count limit. It is distinct from a generic failure so the UI
// can tell the user their work is NOT being journaled.
var ErrRecoveryQuota = errors.New("recovery journal quota exceeded")

// RecoveryPhase is the startup recovery gate for one workspace generation.
// Automatic disk writes, workspace switches, and window close are allowed only
// after the phase reaches RecoveryPhaseResolved.
type RecoveryPhase string

const (
	RecoveryPhaseScanning RecoveryPhase = "scanning"
	RecoveryPhasePending  RecoveryPhase = "pending"
	RecoveryPhaseResolved RecoveryPhase = "resolved"
	RecoveryPhaseFailed   RecoveryPhase = "failed"
)

// RecoveryLifecycleState is safe to expose to renderer and E2E diagnostics.
// The pending identities themselves stay backend-owned so a renderer cannot
// silently omit one record when committing recovery decisions.
type RecoveryLifecycleState struct {
	Phase         RecoveryPhase `json:"phase"`
	WorkspaceRoot string        `json:"workspaceRoot"`
	Generation    uint64        `json:"generation"`
	PendingCount  int           `json:"pendingCount"`
	CorruptCount  int           `json:"corruptCount"`
	Error         string        `json:"error,omitempty"`
}

// RecoveryDecision identifies one record explicitly handled by the user.
type RecoveryDecision struct {
	WindowID string `json:"windowId"`
	Path     string `json:"path"`
}

type recoveryLifecycleEntry struct {
	root    string
	phase   RecoveryPhase
	pending map[string]RecoveryDecision
	corrupt map[string]struct{}
	err     string
}

// Recovery status values for a scanned record.
const (
	// RecoveryStatusClean means disk content still matches the baseline, so the
	// dirty buffer can be restored without losing anything.
	RecoveryStatusClean = "clean"
	// RecoveryStatusConflict means the file changed on disk after the buffer was
	// opened. Restoring blindly would destroy the newer disk version, so the UI
	// must present a choice.
	RecoveryStatusConflict = "conflict"
	// RecoveryStatusMissing means the file no longer exists on disk.
	RecoveryStatusMissing = "missing"
)

// DirtyBufferRecord is one journaled unsaved buffer.
type DirtyBufferRecord struct {
	SchemaVersion int    `json:"schemaVersion"`
	Path          string `json:"path"`
	URI           string `json:"uri"`
	Encoding      string `json:"encoding"`
	EOL           string `json:"eol"`
	BaselineMtime int64  `json:"baselineMtime"`
	BaselineHash  string `json:"baselineHash"`
	Content       string `json:"content"`
	UpdatedAt     int64  `json:"updatedAt"`
	WindowID      string `json:"windowId"`
}

// RecoveryBaseline is the disk state captured when a buffer is opened. The
// renderer must pass it back on every journal write so recovery can detect
// third-party edits.
type RecoveryBaseline struct {
	Path   string `json:"path"`
	Mtime  int64  `json:"mtime"`
	Hash   string `json:"hash"`
	Exists bool   `json:"exists"`
}

// RecoverableFile is one entry offered to the user at startup.
type RecoverableFile struct {
	Path         string `json:"path"`
	WindowID     string `json:"windowId"`
	Status       string `json:"status"`
	Content      string `json:"content"`
	DiskContent  string `json:"diskContent"`
	Encoding     string `json:"encoding"`
	EOL          string `json:"eol"`
	UpdatedAt    int64  `json:"updatedAt"`
	BaselineHash string `json:"baselineHash"`
	CurrentHash  string `json:"currentHash"`
}

// CorruptRecoveryRecord describes a record that could not be decoded. It is
// surfaced instead of dropped so the user learns that some work was lost rather
// than silently seeing fewer files.
type CorruptRecoveryRecord struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// RecoveryScan is the result of inspecting the journal for the active workspace.
type RecoveryScan struct {
	WorkspaceRoot string                  `json:"workspaceRoot"`
	Files         []RecoverableFile       `json:"files"`
	Corrupt       []CorruptRecoveryRecord `json:"corrupt"`
	TotalBytes    int64                   `json:"totalBytes"`
}

// RecoveryService persists unsaved editor buffers so they survive an abnormal
// exit.
type RecoveryService struct {
	mu          sync.Mutex
	rootDir     string
	wsCtx       *WorkspaceContext
	enabled     bool
	fileService *FileService
	lifecycle   map[uint64]recoveryLifecycleEntry
}

// NewRecoveryService creates the service. Journaling defaults to enabled: the
// failure mode of journaling when unwanted is disk usage, while the failure mode
// of not journaling is permanent loss of the user's work.
func NewRecoveryService(configDir string) *RecoveryService {
	return &RecoveryService{
		rootDir:   filepath.Join(configDir, "koyori-ide", "recovery"),
		enabled:   true,
		lifecycle: make(map[uint64]recoveryLifecycleEntry),
	}
}

// setWorkspaceContext links the shared workspace identity (GOAL-P0-02) so the
// journal follows workspace switches instead of pinning a bootstrap-time value.
//
//wails:ignore
func (s *RecoveryService) setWorkspaceContext(ctx *WorkspaceContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wsCtx = ctx
}

// setFileService links FileService so conflict scans read disk content through
// the same sandbox the editor uses.
//
//wails:ignore
func (s *RecoveryService) setFileService(f *FileService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileService = f
}

func recoveryDecisionKey(windowID, path string) string {
	return windowID + "\x00" + filepath.Clean(path)
}

func cloneRecoveryDecisions(in map[string]RecoveryDecision) map[string]RecoveryDecision {
	out := make(map[string]RecoveryDecision, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneRecoveryCorrupt(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for key := range in {
		out[key] = struct{}{}
	}
	return out
}

func (s *RecoveryService) currentWorkspace() (*WorkspaceContext, string, uint64, error) {
	s.mu.Lock()
	ctx := s.wsCtx
	s.mu.Unlock()
	if ctx == nil {
		return nil, "", 0, fmt.Errorf("recovery journal has no workspace context: %w", ErrNotAllowed)
	}
	root, generation := ctx.Snapshot()
	if root == "" {
		return ctx, "", generation, fmt.Errorf("no workspace is open: %w", ErrNotAllowed)
	}
	return ctx, root, generation, nil
}

func (s *RecoveryService) lifecycleFor(root string, generation uint64) recoveryLifecycleEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.lifecycle[generation]
	if !ok || !sameWorkspaceIdentityPath(entry.root, root) {
		return recoveryLifecycleEntry{
			root:    root,
			phase:   RecoveryPhaseScanning,
			pending: make(map[string]RecoveryDecision),
			corrupt: make(map[string]struct{}),
		}
	}
	entry.pending = cloneRecoveryDecisions(entry.pending)
	entry.corrupt = cloneRecoveryCorrupt(entry.corrupt)
	return entry
}

func (s *RecoveryService) publishLifecycle(
	ctx *WorkspaceContext,
	root string,
	generation uint64,
	entry recoveryLifecycleEntry,
) error {
	currentRoot, currentGeneration := ctx.Snapshot()
	if currentGeneration != generation || !sameWorkspaceIdentityPath(currentRoot, root) {
		return fmt.Errorf("workspace changed while publishing recovery state: %w", ErrNotAllowed)
	}
	entry.root = root
	if entry.pending == nil {
		entry.pending = make(map[string]RecoveryDecision)
	}
	if entry.corrupt == nil {
		entry.corrupt = make(map[string]struct{})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lifecycle[generation] = entry
	return nil
}

// GetRecoveryState reports the state for the current workspace generation.
// With no workspace open there is no disk target, so close is allowed and the
// externally visible state is resolved.
func (s *RecoveryService) GetRecoveryState() RecoveryLifecycleState {
	s.mu.Lock()
	ctx := s.wsCtx
	s.mu.Unlock()
	if ctx == nil {
		return RecoveryLifecycleState{Phase: RecoveryPhaseResolved}
	}
	root, generation := ctx.Snapshot()
	if root == "" {
		return RecoveryLifecycleState{Phase: RecoveryPhaseResolved, Generation: generation}
	}
	entry := s.lifecycleFor(root, generation)
	return RecoveryLifecycleState{
		Phase:         entry.phase,
		WorkspaceRoot: root,
		Generation:    generation,
		PendingCount:  len(entry.pending),
		CorruptCount:  len(entry.corrupt),
		Error:         entry.err,
	}
}

func (s *RecoveryService) requireResolved(operation string) error {
	state := s.GetRecoveryState()
	if state.WorkspaceRoot == "" || state.Phase == RecoveryPhaseResolved {
		return nil
	}
	return fmt.Errorf(
		"%s is blocked while recovery is %s for workspace generation %d: %w",
		operation,
		state.Phase,
		state.Generation,
		ErrNotAllowed,
	)
}

func (s *RecoveryService) requireResolvedBeforeWorkspaceChange(targetRoot string) error {
	state := s.GetRecoveryState()
	if state.WorkspaceRoot == "" {
		return nil
	}
	if targetRoot != "" {
		if target, err := canonicalizeWorkspaceRoot(targetRoot); err == nil &&
			sameWorkspaceIdentityPath(state.WorkspaceRoot, target) {
			return nil
		}
	}
	return s.requireResolved("workspace switch")
}

func (s *RecoveryService) currentLifecycleEntry() (
	string,
	uint64,
	recoveryLifecycleEntry,
	error,
) {
	_, root, generation, err := s.currentWorkspace()
	if err != nil {
		return "", 0, recoveryLifecycleEntry{}, err
	}
	return root, generation, s.lifecycleFor(root, generation), nil
}

func recoveryCorruptBelongsToWindow(recordPath, windowID string) bool {
	cleaned := filepath.Clean(recordPath)
	return cleaned == windowID || strings.HasPrefix(cleaned, windowID+string(filepath.Separator))
}

func (s *RecoveryService) requirePendingRecordUnchanged(
	windowID,
	path,
	operation string,
) error {
	_, generation, entry, err := s.currentLifecycleEntry()
	if err != nil {
		return err
	}
	if entry.phase != RecoveryPhasePending {
		return nil
	}
	if _, pending := entry.pending[recoveryDecisionKey(windowID, path)]; !pending {
		return nil
	}
	return fmt.Errorf(
		"%s is blocked for a pending recovery record in workspace generation %d: %w",
		operation,
		generation,
		ErrNotAllowed,
	)
}

func (s *RecoveryService) requireRecordCleanupAllowed(
	windowID,
	path,
	operation string,
) error {
	_, generation, entry, err := s.currentLifecycleEntry()
	if err != nil {
		return err
	}
	switch entry.phase {
	case RecoveryPhaseResolved:
		return nil
	case RecoveryPhasePending:
		if _, pending := entry.pending[recoveryDecisionKey(windowID, path)]; !pending {
			return nil
		}
	}
	return fmt.Errorf(
		"%s is blocked while recovery is %s for workspace generation %d: %w",
		operation,
		entry.phase,
		generation,
		ErrNotAllowed,
	)
}

func (s *RecoveryService) requireWindowCleanupAllowed(windowID, operation string) error {
	_, generation, entry, err := s.currentLifecycleEntry()
	if err != nil {
		return err
	}
	switch entry.phase {
	case RecoveryPhaseResolved:
		return nil
	case RecoveryPhasePending:
		for _, decision := range entry.pending {
			if decision.WindowID == windowID {
				return fmt.Errorf(
					"%s is blocked for a window with pending recovery records in workspace generation %d: %w",
					operation,
					generation,
					ErrNotAllowed,
				)
			}
		}
		for recordPath := range entry.corrupt {
			if recoveryCorruptBelongsToWindow(recordPath, windowID) {
				return fmt.Errorf(
					"%s is blocked for a window with corrupt recovery records in workspace generation %d: %w",
					operation,
					generation,
					ErrNotAllowed,
				)
			}
		}
		return nil
	}
	return fmt.Errorf(
		"%s is blocked while recovery is %s for workspace generation %d: %w",
		operation,
		entry.phase,
		generation,
		ErrNotAllowed,
	)
}

// SetJournalEnabled turns journaling on or off. Disabling clears the current
// workspace journal, because leaving stale records behind would offer the user
// recovery content that is no longer being maintained.
func (s *RecoveryService) SetJournalEnabled(enabled bool) error {
	if enabled {
		s.mu.Lock()
		s.enabled = true
		s.mu.Unlock()
		return nil
	}
	state := s.GetRecoveryState()
	if state.WorkspaceRoot != "" {
		if err := s.ClearWorkspaceJournal(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.enabled = false
	s.mu.Unlock()
	return nil
}

// IsJournalEnabled reports whether dirty buffers are being journaled.
func (s *RecoveryService) IsJournalEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// workspaceDir resolves the journal directory for the active workspace.
// It fails closed: with no workspace open there is no correct place to write.
func (s *RecoveryService) workspaceDir() (string, string, error) {
	s.mu.Lock()
	ctx := s.wsCtx
	rootDir := s.rootDir
	s.mu.Unlock()

	if ctx == nil {
		return "", "", fmt.Errorf("recovery journal has no workspace context: %w", ErrNotAllowed)
	}
	root, err := ctx.RequireRoot()
	if err != nil {
		return "", "", err
	}
	// Hash the canonical root so the directory name is a safe flat token and two
	// workspaces with the same basename cannot collide.
	return filepath.Join(rootDir, hashContent([]byte(root))), root, nil
}

// windowDir resolves the per-window subdirectory, validating the window ID so a
// renderer-supplied value cannot escape the journal root.
func (s *RecoveryService) windowDir(windowID string) (string, string, error) {
	wsDir, root, err := s.workspaceDir()
	if err != nil {
		return "", "", err
	}
	if err := ValidateNameForFlatDir(windowID); err != nil {
		return "", "", fmt.Errorf("invalid window id %q: %w", windowID, err)
	}
	return filepath.Join(wsDir, windowID), root, nil
}

// isSensitiveRecoveryPath reports whether a path must never be journaled.
//
// Journaling copies file content into the config directory. For credential
// material that turns a single secret into two copies with different lifetimes,
// so these are excluded by default and the exclusion is not user-overridable.
func isSensitiveRecoveryPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	lower := strings.ToLower(filepath.ToSlash(path))

	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	switch base {
	case "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
		"credentials", "secrets.json", ".netrc", ".pgpass", ".htpasswd":
		return true
	}
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks"} {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	for _, seg := range []string{"/.ssh/", "/.gnupg/", "/.aws/", "/.kube/", "/.git/"} {
		if strings.Contains(lower, seg) {
			return true
		}
	}
	return false
}

// ComputeBaseline captures the disk state of a file so the renderer can attach
// it to journal writes. A missing file is not an error: opening a brand new
// buffer is legitimate, and recovery treats it as "no baseline to compare".
func (s *RecoveryService) ComputeBaseline(path string) (RecoveryBaseline, error) {
	_, root, err := s.workspaceDir()
	if err != nil {
		return RecoveryBaseline{}, err
	}
	abs, err := ValidatePathWithinRoot(root, path)
	if err != nil {
		return RecoveryBaseline{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return RecoveryBaseline{Path: abs, Exists: false}, nil
		}
		return RecoveryBaseline{}, fmt.Errorf("stat %q: %w", path, err)
	}
	if info.IsDir() {
		return RecoveryBaseline{}, fmt.Errorf("%q is a directory: %w", path, ErrInvalidInput)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return RecoveryBaseline{}, fmt.Errorf("read %q: %w", path, err)
	}
	return RecoveryBaseline{
		Path:   abs,
		Mtime:  info.ModTime().UnixNano(),
		Hash:   hashContent(data),
		Exists: true,
	}, nil
}

// journalUsage returns the current byte total and record count for a workspace.
func journalUsage(wsDir string) (int64, int, error) {
	var total int64
	var count int
	err := filepath.WalkDir(wsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		count++
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0, 0, err
	}
	return total, count, nil
}

// SaveDirtyBuffer journals one unsaved buffer.
//
// The renderer is expected to debounce calls; this method does the atomic write
// and the quota enforcement. Quota rejection returns ErrRecoveryQuota so the UI
// can warn that the buffer is not protected, rather than failing silently.
func (s *RecoveryService) SaveDirtyBuffer(
	windowID, path, content, encoding, eol string,
	baselineMtime int64, baselineHash string,
) error {
	if !s.IsJournalEnabled() {
		return nil
	}
	if len(content) > maxRecoveryRecordBytes {
		return fmt.Errorf(
			"buffer %q is %d bytes, over the %d byte journal limit: %w",
			filepath.Base(path), len(content), maxRecoveryRecordBytes, ErrRecoveryQuota,
		)
	}

	winDir, root, err := s.windowDir(windowID)
	if err != nil {
		return err
	}
	abs, err := ValidatePathWithinRoot(root, path)
	if err != nil {
		return err
	}
	if err := s.requirePendingRecordUnchanged(windowID, abs, "overwrite recovery snapshot"); err != nil {
		return err
	}
	if isSensitiveRecoveryPath(abs) {
		// Not an error: the editor may legitimately open a .env file. We simply
		// refuse to copy its contents into the config directory.
		return nil
	}

	recordPath := filepath.Join(winDir, hashContent([]byte(abs))+".json")

	wsDir, _, err := s.workspaceDir()
	if err != nil {
		return err
	}
	total, count, err := journalUsage(wsDir)
	if err != nil {
		return fmt.Errorf("measure recovery journal: %w", err)
	}
	// An existing record for this path is being replaced, so it does not count
	// against the limits.
	if existing, statErr := os.Stat(recordPath); statErr == nil {
		total -= existing.Size()
		count--
	}
	if total+int64(len(content)) > maxRecoveryWorkspaceBytes {
		return fmt.Errorf(
			"recovery journal for this workspace would exceed %d bytes: %w",
			maxRecoveryWorkspaceBytes, ErrRecoveryQuota,
		)
	}
	if count+1 > maxRecoveryRecords {
		return fmt.Errorf(
			"recovery journal already holds %d records (limit %d): %w",
			count, maxRecoveryRecords, ErrRecoveryQuota,
		)
	}

	if err := os.MkdirAll(winDir, 0o700); err != nil {
		return fmt.Errorf("create recovery dir: %w", err)
	}
	if encoding == "" {
		encoding = "utf-8"
	}
	if eol == "" {
		eol = "lf"
	}
	record := DirtyBufferRecord{
		SchemaVersion: recoverySchemaVersion,
		Path:          abs,
		URI:           pathToURI(abs),
		Encoding:      encoding,
		EOL:           eol,
		BaselineMtime: baselineMtime,
		BaselineHash:  baselineHash,
		Content:       content,
		UpdatedAt:     time.Now().UnixMilli(),
		WindowID:      windowID,
	}
	// 0600: dirty buffers contain the user's source, which must not be
	// world-readable in a shared config directory.
	if err := atomicWriteJSON(recordPath, record, 0o600); err != nil {
		return fmt.Errorf("write recovery record: %w", err)
	}
	return nil
}

// ClearDirtyBuffer removes the journal record for one file. Called after a
// successful save and after the user explicitly discards changes.
func (s *RecoveryService) ClearDirtyBuffer(windowID, path string) error {
	winDir, root, err := s.windowDir(windowID)
	if err != nil {
		return err
	}
	abs, err := ValidatePathWithinRoot(root, path)
	if err != nil {
		return err
	}
	if err := s.requireRecordCleanupAllowed(windowID, abs, "clear recovery record"); err != nil {
		return err
	}
	recordPath := filepath.Join(winDir, hashContent([]byte(abs))+".json")
	if err := os.Remove(recordPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove recovery record: %w", err)
	}
	return nil
}

// ClearWindowJournal removes every record for one window. Called on a clean
// window close where the user has no unsaved work left to protect.
func (s *RecoveryService) ClearWindowJournal(windowID string) error {
	winDir, _, err := s.windowDir(windowID)
	if err != nil {
		return err
	}
	if err := s.requireWindowCleanupAllowed(windowID, "clear window recovery journal"); err != nil {
		return err
	}
	if err := os.RemoveAll(winDir); err != nil {
		return fmt.Errorf("clear window recovery journal: %w", err)
	}
	return nil
}

// ClearWorkspaceJournal removes every record for the active workspace. Called
// when the workspace is removed, so a deleted project leaves no orphaned copies
// of its source behind.
func (s *RecoveryService) ClearWorkspaceJournal() error {
	if err := s.requireResolved("clear workspace recovery journal"); err != nil {
		return err
	}
	wsDir, _, err := s.workspaceDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(wsDir); err != nil {
		return fmt.Errorf("clear workspace recovery journal: %w", err)
	}
	return nil
}

// clearJournalForRoot removes the journal for an explicit root, used when the
// workspace is being removed and the shared context may already have moved on.
//
//wails:ignore
func (s *RecoveryService) clearJournalForRoot(root string) error {
	if root == "" {
		return fmt.Errorf("workspace root is required: %w", ErrInvalidInput)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	s.mu.Lock()
	rootDir := s.rootDir
	s.mu.Unlock()
	target := filepath.Join(rootDir, hashContent([]byte(filepath.Clean(abs))))
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("clear workspace recovery journal: %w", err)
	}
	return nil
}

// decodeRecord reads and validates one journal file.
func decodeRecord(path string) (DirtyBufferRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return DirtyBufferRecord{}, fmt.Errorf("read: %v", err)
	}
	var record DirtyBufferRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return DirtyBufferRecord{}, fmt.Errorf("malformed JSON: %v", err)
	}
	if record.SchemaVersion != recoverySchemaVersion {
		return DirtyBufferRecord{}, fmt.Errorf(
			"unsupported schema version %d (this build understands %d)",
			record.SchemaVersion, recoverySchemaVersion,
		)
	}
	if record.Path == "" {
		return DirtyBufferRecord{}, errors.New("record has no path")
	}
	return record, nil
}

// ScanRecoverable inspects the journal for the active workspace and classifies
// each record against current disk state.
//
// A corrupt record never aborts the scan: the app must start even when part of
// the journal is unreadable, and the user is told which entries were lost.
func (s *RecoveryService) ScanRecoverable() (scan RecoveryScan, err error) {
	ctx, root, generation, err := s.currentWorkspace()
	if err != nil {
		return RecoveryScan{}, err
	}
	s.mu.Lock()
	rootDir := s.rootDir
	s.mu.Unlock()
	wsDir := filepath.Join(rootDir, hashContent([]byte(root)))
	scan = RecoveryScan{
		WorkspaceRoot: root,
		Files:         []RecoverableFile{},
		Corrupt:       []CorruptRecoveryRecord{},
	}
	if err := s.publishLifecycle(ctx, root, generation, recoveryLifecycleEntry{
		phase:   RecoveryPhaseScanning,
		pending: make(map[string]RecoveryDecision),
		corrupt: make(map[string]struct{}),
	}); err != nil {
		return RecoveryScan{}, err
	}
	defer func() {
		if err == nil {
			return
		}
		_ = s.publishLifecycle(ctx, root, generation, recoveryLifecycleEntry{
			phase:   RecoveryPhaseFailed,
			pending: make(map[string]RecoveryDecision),
			corrupt: make(map[string]struct{}),
			err:     err.Error(),
		})
	}()

	entries, err := os.ReadDir(wsDir)
	if err != nil {
		if os.IsNotExist(err) {
			if _, statErr := os.Stat(wsDir); statErr != nil && os.IsNotExist(statErr) {
				err = s.publishLifecycle(ctx, root, generation, recoveryLifecycleEntry{
					phase:   RecoveryPhaseResolved,
					pending: make(map[string]RecoveryDecision),
					corrupt: make(map[string]struct{}),
				})
				return scan, err
			}
		}
		return RecoveryScan{}, fmt.Errorf("read recovery dir: %w", err)
	}

	for _, windowEntry := range entries {
		if !windowEntry.IsDir() {
			continue
		}
		winDir := filepath.Join(wsDir, windowEntry.Name())
		if strings.HasPrefix(windowEntry.Name(), ".recovery-commit-") {
			if _, markerErr := os.Stat(filepath.Join(winDir, recoveryCommitMarker)); markerErr == nil {
				// The transaction committed before a previous process stopped. Its
				// staged records are intentionally no longer recoverable.
				continue
			}
		}
		recordFiles, err := os.ReadDir(winDir)
		if err != nil {
			scan.Corrupt = append(scan.Corrupt, CorruptRecoveryRecord{
				File:   windowEntry.Name(),
				Reason: fmt.Sprintf("unreadable window directory: %v", err),
			})
			continue
		}
		for _, recordEntry := range recordFiles {
			if recordEntry.IsDir() || !strings.HasSuffix(recordEntry.Name(), ".json") {
				continue
			}
			recordPath := filepath.Join(winDir, recordEntry.Name())
			if info, statErr := recordEntry.Info(); statErr == nil {
				scan.TotalBytes += info.Size()
			}
			record, decodeErr := decodeRecord(recordPath)
			if decodeErr != nil {
				scan.Corrupt = append(scan.Corrupt, CorruptRecoveryRecord{
					File:   filepath.Join(windowEntry.Name(), recordEntry.Name()),
					Reason: decodeErr.Error(),
				})
				continue
			}
			// A record whose path escaped the workspace is treated as corrupt
			// rather than honored: restoring it would write outside the sandbox.
			if _, err := ValidatePathWithinRoot(root, record.Path); err != nil {
				scan.Corrupt = append(scan.Corrupt, CorruptRecoveryRecord{
					File:   filepath.Join(windowEntry.Name(), recordEntry.Name()),
					Reason: fmt.Sprintf("path outside workspace: %v", err),
				})
				continue
			}
			scan.Files = append(scan.Files, classifyRecord(record, windowEntry.Name()))
		}
	}

	// Stable ordering keeps the recovery dialog deterministic across restarts.
	sort.Slice(scan.Files, func(i, j int) bool {
		if scan.Files[i].Path != scan.Files[j].Path {
			return scan.Files[i].Path < scan.Files[j].Path
		}
		return scan.Files[i].WindowID < scan.Files[j].WindowID
	})
	sort.Slice(scan.Corrupt, func(i, j int) bool {
		return scan.Corrupt[i].File < scan.Corrupt[j].File
	})
	pending := make(map[string]RecoveryDecision, len(scan.Files))
	for _, file := range scan.Files {
		decision := RecoveryDecision{WindowID: file.WindowID, Path: file.Path}
		pending[recoveryDecisionKey(decision.WindowID, decision.Path)] = decision
	}
	corrupt := make(map[string]struct{}, len(scan.Corrupt))
	for _, record := range scan.Corrupt {
		corrupt[filepath.Clean(record.File)] = struct{}{}
	}
	phase := RecoveryPhaseResolved
	if len(pending) > 0 || len(corrupt) > 0 {
		phase = RecoveryPhasePending
	}
	err = s.publishLifecycle(ctx, root, generation, recoveryLifecycleEntry{
		phase:   phase,
		pending: pending,
		corrupt: corrupt,
	})
	return scan, err
}

type stagedRecoveryMove struct {
	source string
	target string
}

func rollbackRecoveryMoves(moves []stagedRecoveryMove) error {
	var rollbackErr error
	for i := len(moves) - 1; i >= 0; i-- {
		move := moves[i]
		if err := os.MkdirAll(filepath.Dir(move.source), 0o700); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("recreate recovery directory: %w", err))
			continue
		}
		if err := os.Rename(move.target, move.source); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore recovery record: %w", err))
		}
	}
	return rollbackErr
}

func recoverySetMatches[T any](expected map[string]T, actual map[string]T) bool {
	if len(expected) != len(actual) {
		return false
	}
	for key := range expected {
		if _, ok := actual[key]; !ok {
			return false
		}
	}
	return true
}

// CompleteRecovery atomically commits every explicit decision returned by the
// most recent successful scan. Records are first moved into an uncommitted
// transaction directory. A single atomic marker is the commit point: before it
// exists, a crash still exposes every staged record to the next scan; after it
// exists, the transaction is ignored and can be removed best-effort.
func (s *RecoveryService) CompleteRecovery(
	decisions []RecoveryDecision,
	corruptFiles []string,
) error {
	ctx, root, generation, err := s.currentWorkspace()
	if err != nil {
		return err
	}
	entry := s.lifecycleFor(root, generation)
	if entry.phase != RecoveryPhasePending {
		return fmt.Errorf("recovery is %s, not pending: %w", entry.phase, ErrNotAllowed)
	}

	requested := make(map[string]RecoveryDecision, len(decisions))
	for _, decision := range decisions {
		if err := ValidateNameForFlatDir(decision.WindowID); err != nil {
			return fmt.Errorf("invalid recovery window id %q: %w", decision.WindowID, err)
		}
		abs, err := ValidateMutatingPathWithinRoot(root, decision.Path)
		if err != nil {
			return err
		}
		decision.Path = abs
		key := recoveryDecisionKey(decision.WindowID, decision.Path)
		if _, duplicate := requested[key]; duplicate {
			return fmt.Errorf("duplicate recovery decision for %q: %w", decision.Path, ErrInvalidInput)
		}
		requested[key] = decision
	}
	if !recoverySetMatches(entry.pending, requested) {
		return fmt.Errorf("recovery decisions do not match the pending scan: %w", ErrInvalidInput)
	}

	requestedCorrupt := make(map[string]struct{}, len(corruptFiles))
	for _, file := range corruptFiles {
		cleaned := filepath.Clean(file)
		if filepath.IsAbs(cleaned) || cleaned == "." || cleaned == ".." ||
			strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("invalid corrupt recovery path %q: %w", file, ErrInvalidInput)
		}
		requestedCorrupt[cleaned] = struct{}{}
	}
	if !recoverySetMatches(entry.corrupt, requestedCorrupt) {
		return fmt.Errorf("corrupt recovery acknowledgements do not match the pending scan: %w", ErrInvalidInput)
	}

	s.mu.Lock()
	rootDir := s.rootDir
	s.mu.Unlock()
	wsDir := filepath.Join(rootDir, hashContent([]byte(root)))
	txDir, err := os.MkdirTemp(wsDir, ".recovery-commit-")
	if err != nil {
		return fmt.Errorf("create recovery commit transaction: %w", err)
	}
	moves := make([]stagedRecoveryMove, 0, len(decisions)+len(corruptFiles))
	stage := func(source string, index int) error {
		target := filepath.Join(txDir, fmt.Sprintf("%04d-%s.json", index, hashContent([]byte(source))))
		if err := os.Rename(source, target); err != nil {
			return err
		}
		moves = append(moves, stagedRecoveryMove{source: source, target: target})
		return nil
	}

	for _, decision := range decisions {
		source := filepath.Join(wsDir, decision.WindowID, hashContent([]byte(decision.Path))+".json")
		record, decodeErr := decodeRecord(source)
		if decodeErr != nil {
			rollbackErr := rollbackRecoveryMoves(moves)
			return errors.Join(fmt.Errorf("validate recovery decision record: %w", decodeErr), rollbackErr)
		}
		if !sameWorkspaceIdentityPath(record.Path, decision.Path) {
			rollbackErr := rollbackRecoveryMoves(moves)
			return errors.Join(fmt.Errorf("recovery decision path mismatch: %w", ErrInvalidInput), rollbackErr)
		}
		if err := stage(source, len(moves)); err != nil {
			rollbackErr := rollbackRecoveryMoves(moves)
			return errors.Join(fmt.Errorf("stage recovery decision: %w", err), rollbackErr)
		}
	}
	for _, file := range corruptFiles {
		source := filepath.Join(wsDir, filepath.Clean(file))
		if _, err := ValidateMutatingPathWithinRoot(wsDir, source); err != nil {
			rollbackErr := rollbackRecoveryMoves(moves)
			return errors.Join(err, rollbackErr)
		}
		if err := stage(source, len(moves)); err != nil {
			rollbackErr := rollbackRecoveryMoves(moves)
			return errors.Join(fmt.Errorf("stage corrupt recovery record: %w", err), rollbackErr)
		}
	}

	marker := struct {
		SchemaVersion int   `json:"schemaVersion"`
		CommittedAt   int64 `json:"committedAt"`
	}{SchemaVersion: recoverySchemaVersion, CommittedAt: time.Now().UnixMilli()}
	if err := atomicWriteJSON(filepath.Join(txDir, recoveryCommitMarker), marker, 0o600); err != nil {
		rollbackErr := rollbackRecoveryMoves(moves)
		return errors.Join(fmt.Errorf("commit recovery decisions: %w", err), rollbackErr)
	}
	if err := s.publishLifecycle(ctx, root, generation, recoveryLifecycleEntry{
		phase:   RecoveryPhaseResolved,
		pending: make(map[string]RecoveryDecision),
		corrupt: make(map[string]struct{}),
	}); err != nil {
		return err
	}
	_ = os.RemoveAll(txDir)
	return nil
}

// AcknowledgeRecoveryFailure is the explicit user action that releases a
// failed scan. It never deletes or rewrites journal data.
func (s *RecoveryService) AcknowledgeRecoveryFailure() error {
	ctx, root, generation, err := s.currentWorkspace()
	if err != nil {
		return err
	}
	entry := s.lifecycleFor(root, generation)
	if entry.phase != RecoveryPhaseFailed {
		return fmt.Errorf("recovery is %s, not failed: %w", entry.phase, ErrNotAllowed)
	}
	return s.publishLifecycle(ctx, root, generation, recoveryLifecycleEntry{
		phase:   RecoveryPhaseResolved,
		pending: make(map[string]RecoveryDecision),
		corrupt: make(map[string]struct{}),
	})
}

// classifyRecord compares a journaled buffer against the current disk state.
func classifyRecord(record DirtyBufferRecord, windowID string) RecoverableFile {
	out := RecoverableFile{
		Path:         record.Path,
		WindowID:     windowID,
		Content:      record.Content,
		Encoding:     record.Encoding,
		EOL:          record.EOL,
		UpdatedAt:    record.UpdatedAt,
		BaselineHash: record.BaselineHash,
	}
	if record.WindowID != "" {
		out.WindowID = record.WindowID
	}

	diskData, err := os.ReadFile(record.Path)
	if err != nil {
		if os.IsNotExist(err) {
			out.Status = RecoveryStatusMissing
			return out
		}
		// An unreadable file is a conflict, not a clean restore: we cannot prove
		// the disk still holds what the baseline described.
		out.Status = RecoveryStatusConflict
		return out
	}
	out.CurrentHash = hashContent(diskData)
	out.DiskContent = string(diskData)

	switch {
	case record.BaselineHash == "":
		// No baseline captured (buffer was never on disk when opened) but the
		// file exists now — something else created it, so let the user choose.
		out.Status = RecoveryStatusConflict
	case out.CurrentHash == record.BaselineHash:
		out.Status = RecoveryStatusClean
	default:
		out.Status = RecoveryStatusConflict
	}
	return out
}

// DiscardRecoveredSession clears the journal entries the user chose not to
// restore. Called after the recovery dialog is resolved so the same prompt does
// not reappear on the next start.
func (s *RecoveryService) DiscardRecoveredSession(windowID string) error {
	return s.ClearWindowJournal(windowID)
}
