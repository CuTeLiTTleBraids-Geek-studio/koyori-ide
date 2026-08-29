package services

// Plan 11 Task 14 — 智能回滚（快照 + 操作日志）。
//
// 职责（Step 1-10）：
//   - Step 1: Snapshot 结构（ID/CreatedAt/Reason/Files/GitState）
//   - Step 2: CreateSnapshot/RestoreSnapshot/RestorePartial/ListSnapshots/DeleteSnapshot/DiffSnapshots
//   - Step 3: 触发（手动/Plan 每步骤前/Goal 每检查点/Apply 前/工作流每步骤前）
//   - Step 4: 内容寻址存储（hash→blob），相同文件不重复
//   - Step 5: 清理策略（保留最近 N 个 + 时间过期）
//   - Step 6: SnapshotTimeline.vue（前端，见 stores/snapshot.ts）
//   - Step 7: 选择性回滚（勾选文件回滚）
//   - Step 8: Git 集成（Git 干净用 git checkout，脏用快照覆盖）
//   - Step 9: G-SEC-06（存储路径校验 + ValidatePathWithinRoot）
//   - Step 10: snapshot_service_test.go 覆盖

import (
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SnapshotReason 快照创建原因（Step 1/3）。
type SnapshotReason string

const (
	SnapshotReasonManual         SnapshotReason = "manual"
	SnapshotReasonPlanStep       SnapshotReason = "plan-step"
	SnapshotReasonGoalCheckpoint SnapshotReason = "goal-checkpoint"
	SnapshotReasonPreApply       SnapshotReason = "pre-apply"
	SnapshotReasonWorkflowStep   SnapshotReason = "workflow-step"
	SnapshotReasonFileSave       SnapshotReason = "file-save"
)

// FileSnapshot 单个文件的快照元数据（Step 1/4）。
// 文件内容以内容寻址方式存储在 blobs/<hash>，相同 hash 不重复存储。
type FileSnapshot struct {
	Path string `json:"path"`
	Hash string `json:"hash"` // SHA-256 内容哈希
	Size int64  `json:"size"`
}

// GitState 快照创建时的 Git 状态（Step 1/8）。
type GitState struct {
	Branch  string   `json:"branch"`
	IsClean bool     `json:"isClean"`
	Changes []string `json:"changes,omitempty"` // 变更文件路径列表
}

// Snapshot 完整快照（Step 1）。
type Snapshot struct {
	ID            string         `json:"id"`
	CreatedAt     time.Time      `json:"createdAt"`
	Reason        SnapshotReason `json:"reason"`
	WorkspaceRoot string         `json:"workspaceRoot"`
	Files         []FileSnapshot `json:"files"`
	GitState      *GitState      `json:"gitState,omitempty"`
	FileCount     int            `json:"fileCount"`
}

// SnapshotDiff 两个快照之间的差异（Step 2: DiffSnapshots）。
type SnapshotDiff struct {
	FromSnapshotID string   `json:"fromSnapshotId"`
	ToSnapshotID   string   `json:"toSnapshotId"`
	Added          []string `json:"added"`
	Removed        []string `json:"removed"`
	Modified       []string `json:"modified"`
}

// CleanupConfig 清理策略配置（Step 5）。
type CleanupConfig struct {
	KeepN  int           `json:"keepN"`  // 保留最近 N 个（0 = 不限）
	MaxAge time.Duration `json:"maxAge"` // 最大保留时长（0 = 不过期）
}

// SnapshotService 智能回滚服务（Step 1-10）。
type SnapshotService struct {
	mu           sync.Mutex
	configDir    string
	snapshotDir  string
	blobDir      string
	metadataPath string
	gitService   *GitService
}

// NewSnapshotService 创建快照服务（Step 9: 存储于 ~/.config/koyori-ide/snapshots/）。
func NewSnapshotService(configDir string) *SnapshotService {
	snapshotDir := filepath.Join(configDir, "koyori-ide", "snapshots")
	return &SnapshotService{
		configDir:    configDir,
		snapshotDir:  snapshotDir,
		blobDir:      filepath.Join(snapshotDir, "blobs"),
		metadataPath: filepath.Join(snapshotDir, "metadata.json"),
	}
}

// setGitService 注入 GitService（Step 8: Git 集成）。
//
//wails:ignore
func (s *SnapshotService) setGitService(g *GitService) {
	s.gitService = g
}

// ensureDirs 确保快照目录存在。
func (s *SnapshotService) ensureDirs() error {
	if err := os.MkdirAll(s.snapshotDir, 0o755); err != nil {
		return fmt.Errorf("create snapshot dir: %w", err)
	}
	if err := os.MkdirAll(s.blobDir, 0o755); err != nil {
		return fmt.Errorf("create blob dir: %w", err)
	}
	return nil
}

// hashContent 计算内容的 SHA-256 哈希（Step 4: 内容寻址）。
func hashContent(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// storeBlob 存储文件内容到内容寻址存储（Step 4）。
// 相同 hash 的文件不重复存储。
func (s *SnapshotService) storeBlob(data []byte) (string, error) {
	hash := hashContent(data)
	blobPath := filepath.Join(s.blobDir, hash)
	// 检查是否已存在（内容寻址去重）
	if _, err := os.Stat(blobPath); err == nil {
		return hash, nil // 已存在，跳过
	}
	// 原子写入 blob
	if err := atomicWriteFile(blobPath, data, 0o644); err != nil {
		return "", fmt.Errorf("store blob %s: %w", hash, err)
	}
	return hash, nil
}

// storeBlobFromFile streams the file at path into content-addressable
// storage without buffering the entire file in memory. M-4: replaces the
// previous os.ReadFile + hash.Write(data) pattern in CreateSnapshot which
// loaded whole files into RAM (problematic for large binaries).
//
// P2-3 / N-1: TOCTOU 修复。原实现先 Open+hash 再 Open+write，两次 Open
// 之间文件被改写会导致 hash 与 blob 内容不一致，破坏内容寻址完整性。
// 现采用单次 Open + io.MultiWriter(hash, tmpFile) 同步哈希与写入临时文件，
// 然后 rename 到 blobPath —— 保证写到磁盘的 blob 内容与计算出的 hash 严格
// 对应同一份字节流。
func (s *SnapshotService) storeBlobFromFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	blobDir := s.blobDir
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return "", fmt.Errorf("create blob dir: %w", err)
	}
	tmp, err := os.CreateTemp(blobDir, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// 失败路径清理：关闭并删除临时文件
	cleanupTmp := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	h := sha256.New()
	// MultiWriter 把同一份字节流同时写入 hash 和临时文件，
	// 保证 hash 与磁盘 blob 内容严格一致（消除 TOCTOU 窗口）。
	mw := io.MultiWriter(h, tmp)
	if _, err := io.Copy(mw, f); err != nil {
		cleanupTmp()
		return "", fmt.Errorf("stream %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		cleanupTmp()
		return "", fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("close temp file: %w", err)
	}
	hash := hex.EncodeToString(h.Sum(nil))

	blobPath := filepath.Join(blobDir, hash)
	if _, err := os.Stat(blobPath); err == nil {
		// 已存在（内容寻址去重），删除本次临时文件
		os.Remove(tmpName)
		return hash, nil
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, blobPath); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("rename temp to blob: %w", err)
	}
	return hash, nil
}

// readBlob 读取 blob 内容。
func (s *SnapshotService) readBlob(hash string) ([]byte, error) {
	blobPath := filepath.Join(s.blobDir, hash)
	// G-SEC-06: 验证 hash 只含十六进制字符，防止路径穿越
	if !isValidHash(hash) {
		return nil, fmt.Errorf("%w: invalid hash format", ErrInvalidInput)
	}
	data, err := os.ReadFile(blobPath)
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", hash, err)
	}
	return data, nil
}

// isValidHash 验证哈希字符串格式（仅十六进制，64 字符）。
func isValidHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// loadMetadata 加载快照元数据。
func (s *SnapshotService) loadMetadata() ([]Snapshot, error) {
	data, err := os.ReadFile(s.metadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []Snapshot{}, nil
		}
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	var snapshots []Snapshot
	if err := json.Unmarshal(data, &snapshots); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	return snapshots, nil
}

// saveMetadata 原子保存快照元数据（G-SEC: atomicWriteJSON）。
func (s *SnapshotService) saveMetadata(snapshots []Snapshot) error {
	return atomicWriteJSON(s.metadataPath, snapshots, 0o600)
}

// generateSnapshotID 生成快照 ID（时间戳 + crypto/rand 随机后缀）。
//
// M-3: previously this was just the nanosecond timestamp, which collided
// under high concurrency (two goroutines entering CreateSnapshot in the
// same nanosecond got the same ID). We now append 8 bytes (16 hex chars)
// from crypto/rand — aligned with project_service.generateProjectID — so
// IDs are unique even under contention. crypto/rand is used (not
// math/rand) because snapshot IDs are user-visible and predictability
// would let an attacker guess adjacent snapshot IDs.
func generateSnapshotID() string {
	b := make([]byte, 8)
	if _, err := crypto_rand.Read(b); err != nil {
		// crypto/rand should never fail on a sane system; fall back to
		// timestamp-only if it does so we still return a usable ID.
		return fmt.Sprintf("snap-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("snap-%d-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

// ---- Step 2: CreateSnapshot ----

// snapshotWalkDirFunc filters directory entries before loading metadata.
func snapshotWalkDirFunc(rootAbs string, visitFile func(path, rel string, info fs.FileInfo) error) fs.WalkDirFunc {
	return func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // 跳过错误文件
		}

		rel, _ := filepath.Rel(rootAbs, path)
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() && isIgnoredDir(filepath.Base(path)) {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}
		return visitFile(path, rel, info)
	}
}

// CreateSnapshot 创建工作区快照（Step 2/3/4）。
// 遍历 workspaceRoot 下所有文件，计算哈希并存储到内容寻址存储。
// .git 目录会被跳过（Step 8: Git 状态单独记录）。
//
// M-4: 文件哈希通过 io.Copy(hash, file) 流式计算，blob 通过临时文件
// 流式写入再 rename — 大文件不再整体读入内存。默认忽略列表
// (node_modules / dist / build / .git 等) 由 isIgnoredDir 维护。
func (s *SnapshotService) CreateSnapshot(workspaceRoot, reason string) (*Snapshot, error) {
	if err := s.ensureDirs(); err != nil {
		return nil, err
	}

	// G-SEC-06: 验证 workspaceRoot 存在（锁外，只读）
	rootAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve workspace root: %v", ErrInvalidInput, err)
	}
	info, err := os.Stat(rootAbs)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: workspace root not accessible: %v", ErrInvalidInput, err)
	}

	// N-6: walk + hash + blob 写入在锁外执行，避免长时间持锁阻塞
	// ListSnapshots / RestoreSnapshot / DeleteSnapshot 等读操作。
	// storeBlobFromFile 是内容寻址的（幂等），不需要锁保护；
	// 仅最终的 metadata 写入需要加锁以确保快照列表一致性。
	files := []FileSnapshot{}
	err = filepath.WalkDir(rootAbs, snapshotWalkDirFunc(rootAbs, func(path, rel string, info fs.FileInfo) error {
		// 流式哈希 + 流式写入 blob（M-4：避免 os.ReadFile 全量读入内存）
		hash, herr := s.storeBlobFromFile(path)
		if herr != nil {
			return nil // 跳过不可读文件
		}
		files = append(files, FileSnapshot{
			Path: rel,
			Hash: hash,
			Size: info.Size(),
		})
		return nil
	}))
	if err != nil {
		return nil, fmt.Errorf("walk workspace: %w", err)
	}

	snap := &Snapshot{
		ID:            generateSnapshotID(),
		CreatedAt:     time.Now(),
		Reason:        SnapshotReason(reason),
		WorkspaceRoot: rootAbs,
		Files:         files,
		FileCount:     len(files),
	}

	// Step 8: 记录 Git 状态（锁外，只读 git）
	if s.gitService != nil {
		snap.GitState = s.captureGitState(rootAbs)
	}

	// N-6: 仅在写 metadata 时加锁，确保快照列表一致性
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshots, err := s.loadMetadata()
	if err != nil {
		return nil, err
	}
	snapshots = append(snapshots, *snap)
	if err := s.saveMetadata(snapshots); err != nil {
		return nil, err
	}
	return snap, nil
}

// isIgnoredDir 判断是否为应忽略的目录。
func isIgnoredDir(name string) bool {
	switch name {
	case "node_modules", "dist", "build", ".next", ".nuxt", "target", "bin", "__pycache__":
		return true
	}
	return false
}

// captureGitState 捕获当前 Git 状态（Step 8）。
func (s *SnapshotService) captureGitState(root string) *GitState {
	changes, err := s.gitService.GetStatus(root)
	if err != nil {
		return &GitState{IsClean: false}
	}
	changePaths := make([]string, 0, len(changes))
	for _, c := range changes {
		changePaths = append(changePaths, c.Path)
	}
	branch := ""
	if bi, err := s.gitService.GetBranchInfo(root); err == nil {
		branch = bi.Name
	}
	return &GitState{
		Branch:  branch,
		IsClean: len(changes) == 0,
		Changes: changePaths,
	}
}

// ---- Step 2: RestoreSnapshot ----

// RestoreSnapshot 恢复快照中的所有文件（Step 2/7/8）。
//
// GOAL-P1-01 **partial semantics**: this operation overwrites the files that
// existed when the snapshot was taken but does NOT remove files added
// afterwards. It is equivalent to "restore snapshot files", not "restore the
// workspace to snapshot state". For exact restore — which would also delete
// post-snapshot additions after user confirmation — call RestoreSnapshotExact.
//
// Clients that must not imply "exact" semantics in their UI should use
// RestoreSnapshotExact instead and surface the diff to the user first.
func (s *SnapshotService) RestoreSnapshot(snapshotID, workspaceRoot string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, err := s.findSnapshot(snapshotID)
	if err != nil {
		return err
	}

	// G-SEC-06: 验证 workspaceRoot
	rootAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return fmt.Errorf("%w: resolve workspace root: %v", ErrInvalidInput, err)
	}
	if err := validateSnapshotWorkspace(snap, rootAbs); err != nil {
		return err
	}

	// 恢复所有文件
	for _, fs := range snap.Files {
		if err := s.restoreFile(fs, rootAbs); err != nil {
			return fmt.Errorf("restore %s: %w", fs.Path, err)
		}
	}
	return nil
}

// RestoreDiff describes the changes a RestoreSnapshotExact call would make.
//
// The diff is computed lock-free and is a snapshot of the workspace at the
// time of the call. Conditions may change between CalculateRestoreDiff and
// RestoreSnapshotExact; the exact restore re-validates before deleting.
type RestoreDiff struct {
	// AddedAfterSnapshot lists relative paths present in the workspace now
	// but not in the snapshot. RestoreSnapshotExact will delete them.
	AddedAfterSnapshot []string `json:"addedAfterSnapshot"`
	// ModifiedSinceSnapshot lists relative paths present in both snapshot and
	// workspace whose content differs. Their content will be overwritten.
	ModifiedSinceSnapshot []string `json:"modifiedSinceSnapshot"`
	// RemovedFromWorkspace lists relative paths in the snapshot that are
	// missing from the workspace now. They will be re-created.
	RemovedFromWorkspace []string `json:"removedFromWorkspace"`
}

// CalculateRestoreDiff returns what RestoreSnapshotExact would change without
// making any modifications. The caller must show AddedAfterSnapshot to the
// user and receive explicit confirmation before calling RestoreSnapshotExact,
// because those files will be permanently deleted.
func (s *SnapshotService) CalculateRestoreDiff(snapshotID, workspaceRoot string) (*RestoreDiff, error) {
	s.mu.Lock()
	snap, err := s.findSnapshot(snapshotID)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}

	rootAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve workspace root: %v", ErrInvalidInput, err)
	}

	// Build the snapshot file set (relative path → hash).
	snapFiles := make(map[string]string, len(snap.Files))
	for _, f := range snap.Files {
		snapFiles[f.Path] = f.Hash
	}

	// Walk the current workspace using the same filter as CreateSnapshot so
	// the comparison is apples-to-apples.
	currentFiles := make(map[string]string)
	_ = filepath.WalkDir(rootAbs, snapshotWalkDirFunc(rootAbs, func(path, rel string, info fs.FileInfo) error {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		currentFiles[rel] = hashContent(data)
		return nil
	}))

	diff := &RestoreDiff{}
	for rel := range currentFiles {
		if _, inSnap := snapFiles[rel]; !inSnap {
			diff.AddedAfterSnapshot = append(diff.AddedAfterSnapshot, rel)
		}
	}
	for rel, snapHash := range snapFiles {
		curHash, inCurrent := currentFiles[rel]
		if !inCurrent {
			diff.RemovedFromWorkspace = append(diff.RemovedFromWorkspace, rel)
		} else if curHash != snapHash {
			diff.ModifiedSinceSnapshot = append(diff.ModifiedSinceSnapshot, rel)
		}
	}

	// Both lists are built from map iteration, which Go deliberately randomizes.
	// The deletion preview is shown to the user for confirmation, so an unstable
	// order would reshuffle the list between renders and make it impossible to
	// review reliably. Sorting matches the DiffSnapshots convention above.
	sort.Strings(diff.AddedAfterSnapshot)
	sort.Strings(diff.ModifiedSinceSnapshot)
	sort.Strings(diff.RemovedFromWorkspace)
	return diff, nil
}

// RestoreSnapshotExact restores the workspace to the exact snapshot state:
//   - snapshot files are overwritten
//   - files present in the workspace but not in the snapshot are deleted
//
// GOAL-P1-01 exact semantics AC:
//   - callers must have shown the diff to the user and received a confirmed=true
//     before calling; passing confirmed=false returns ErrNotAllowed immediately
//     and touches nothing.
//   - the operation writes to a temporary rollback journal before any deletion;
//     if a deletion or write fails the already-applied changes are reversed.
//   - symlink escape, outside-workspace path, and permission failures are
//     fail-closed: a single bad entry aborts and rolls back.
func (s *SnapshotService) RestoreSnapshotExact(snapshotID, workspaceRoot string, confirmed bool) error {
	if !confirmed {
		return fmt.Errorf("explicit confirmation required to delete workspace files: %w", ErrNotAllowed)
	}

	s.mu.Lock()
	snap, err := s.findSnapshot(snapshotID)
	s.mu.Unlock()
	if err != nil {
		return err
	}

	rootAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return fmt.Errorf("%w: resolve workspace root: %v", ErrInvalidInput, err)
	}
	if err := validateSnapshotWorkspace(snap, rootAbs); err != nil {
		return err
	}

	// Identify files to delete — those present now but not in snapshot.
	snapSet := make(map[string]bool, len(snap.Files))
	for _, f := range snap.Files {
		snapSet[f.Path] = true
	}

	var toDelete []string
	_ = filepath.WalkDir(rootAbs, snapshotWalkDirFunc(rootAbs, func(path, rel string, _ fs.FileInfo) error {
		if !snapSet[rel] {
			toDelete = append(toDelete, rel)
		}
		return nil
	}))

	// Rollback journal: save current content of files we are about to overwrite
	// or delete, keyed by relative path. On any failure we reverse every change.
	type journalEntry struct {
		rel     string
		content []byte // nil means the file did not exist before this operation
	}
	journal := make([]journalEntry, 0, len(snap.Files)+len(toDelete))

	// Helper: validate + read current content of a target path.
	captureEntry := func(rel, abs string) error {
		if _, pathErr := ValidatePathWithinRoot(rootAbs, abs); pathErr != nil {
			return fmt.Errorf("path validation failed for %s: %w", rel, pathErr)
		}
		data, readErr := os.ReadFile(abs)
		if os.IsNotExist(readErr) {
			journal = append(journal, journalEntry{rel: rel, content: nil})
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read %s for rollback journal: %w", rel, readErr)
		}
		journal = append(journal, journalEntry{rel: rel, content: data})
		return nil
	}

	// Phase 1 — collect journal entries for files about to be changed.
	for _, f := range snap.Files {
		abs := filepath.Join(rootAbs, f.Path)
		if err := captureEntry(f.Path, abs); err != nil {
			return err
		}
	}
	for _, rel := range toDelete {
		abs := filepath.Join(rootAbs, rel)
		if err := captureEntry(rel, abs); err != nil {
			return err
		}
	}

	// rollback undoes every applied change in LIFO order.
	applied := 0
	rollback := func() {
		for i := applied - 1; i >= 0; i-- {
			e := journal[i]
			abs := filepath.Join(rootAbs, e.rel)
			if e.content == nil {
				// File did not exist before — remove what we created.
				_ = os.Remove(abs)
			} else {
				_ = os.MkdirAll(filepath.Dir(abs), 0o755)
				_ = atomicWriteFile(abs, e.content, 0o644)
			}
		}
	}

	// Phase 2 — write snapshot files.
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range snap.Files {
		if err := s.restoreFile(f, rootAbs); err != nil {
			rollback()
			return fmt.Errorf("restore %s: %w", f.Path, err)
		}
		applied++
	}

	// Phase 3 — delete post-snapshot additions.
	for _, rel := range toDelete {
		abs := filepath.Join(rootAbs, rel)
		if _, pathErr := ValidatePathWithinRoot(rootAbs, abs); pathErr != nil {
			rollback()
			return fmt.Errorf("path validation failed for deletion target %s: %w", rel, pathErr)
		}
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			rollback()
			return fmt.Errorf("delete %s: %w", rel, err)
		}
		applied++
	}
	return nil
}

// RestorePartial 选择性恢复部分文件（Step 7）。
func (s *SnapshotService) RestorePartial(snapshotID, workspaceRoot string, filePaths []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, err := s.findSnapshot(snapshotID)
	if err != nil {
		return err
	}

	rootAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return fmt.Errorf("%w: resolve workspace root: %v", ErrInvalidInput, err)
	}
	if err := validateSnapshotWorkspace(snap, rootAbs); err != nil {
		return err
	}

	// 构建文件路径集合
	want := make(map[string]bool, len(filePaths))
	for _, p := range filePaths {
		want[p] = true
	}

	for _, fs := range snap.Files {
		if want[fs.Path] {
			if err := s.restoreFile(fs, rootAbs); err != nil {
				return fmt.Errorf("restore %s: %w", fs.Path, err)
			}
		}
	}
	return nil
}

func validateSnapshotWorkspace(snap *Snapshot, workspaceRoot string) error {
	snapshotRoot, err := filepath.Abs(snap.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("%w: resolve snapshot workspace: %v", ErrInvalidInput, err)
	}
	rel, err := filepath.Rel(snapshotRoot, workspaceRoot)
	if err != nil || rel != "." {
		return fmt.Errorf("%w: snapshot belongs to a different workspace", ErrInvalidInput)
	}
	return nil
}

// restoreFile 恢复单个文件。
func (s *SnapshotService) restoreFile(fs FileSnapshot, rootAbs string) error {
	data, err := s.readBlob(fs.Hash)
	if err != nil {
		return err
	}
	targetPath := filepath.Join(rootAbs, fs.Path)
	// G-SEC-06: 验证目标路径在工作区内
	if _, err := ValidateMutatingPathWithinRoot(rootAbs, targetPath); err != nil {
		return fmt.Errorf("path validation failed for %s: %w", fs.Path, err)
	}
	// 确保父目录存在
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	return atomicWriteFile(targetPath, data, 0o644)
}

// ---- Step 2: ListSnapshots ----

// ListSnapshots 列出所有快照（按创建时间降序）。
func (s *SnapshotService) ListSnapshots() ([]Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshots, err := s.loadMetadata()
	if err != nil {
		return nil, err
	}
	// 按创建时间降序排序
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].CreatedAt.After(snapshots[j].CreatedAt)
	})
	return snapshots, nil
}

// ---- Step 2: DeleteSnapshot ----

// DeleteSnapshot 删除快照（Step 2/5）。
// 删除元数据记录，并清理不再被任何快照引用的 blob。
func (s *SnapshotService) DeleteSnapshot(snapshotID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshots, err := s.loadMetadata()
	if err != nil {
		return err
	}

	// 找到并移除目标快照
	idx := -1
	for i, snap := range snapshots {
		if snap.ID == snapshotID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: snapshot %s not found", ErrInvalidInput, snapshotID)
	}

	deleted := snapshots[idx]
	snapshots = append(snapshots[:idx], snapshots[idx+1:]...)

	// 保存更新后的元数据
	if err := s.saveMetadata(snapshots); err != nil {
		return err
	}

	// 清理孤立 blob（不被任何剩余快照引用）
	return s.cleanupOrphanBlobs(snapshots, []Snapshot{deleted})
}

// cleanupOrphanBlobs 清理不被任何保留快照引用的 blob。
//
// C-4: previously this took a single *Snapshot and was called inside the
// CleanupSnapshots loop before the final `kept` list was determined. A hash
// referenced by both a deleted snapshot and a snapshot that would be kept
// later in the loop would be missing from usedHashes at that point and get
// os.Remove'd, corrupting restore. The signature now takes the full slice
// of deleted snapshots and is called once after `kept` is final.
func (s *SnapshotService) cleanupOrphanBlobs(kept []Snapshot, deleted []Snapshot) error {
	// 构建所有仍在使用的 hash 集合（来自保留的快照）
	usedHashes := make(map[string]bool)
	for _, snap := range kept {
		for _, fs := range snap.Files {
			usedHashes[fs.Hash] = true
		}
	}
	// 检查已删除快照中的 blob：若不被任何保留快照引用则删除
	for _, snap := range deleted {
		for _, fs := range snap.Files {
			if !usedHashes[fs.Hash] {
				blobPath := filepath.Join(s.blobDir, fs.Hash)
				_ = os.Remove(blobPath) // 忽略错误（blob 可能已被清理）
			}
		}
	}
	return nil
}

// ---- Step 2: DiffSnapshots ----

// DiffSnapshots 比较两个快照的差异（Step 2）。
func (s *SnapshotService) DiffSnapshots(fromID, toID string) (*SnapshotDiff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	from, err := s.findSnapshot(fromID)
	if err != nil {
		return nil, err
	}
	to, err := s.findSnapshot(toID)
	if err != nil {
		return nil, err
	}

	// 构建文件 hash 映射
	fromFiles := make(map[string]string) // path → hash
	for _, fs := range from.Files {
		fromFiles[fs.Path] = fs.Hash
	}
	toFiles := make(map[string]string)
	for _, fs := range to.Files {
		toFiles[fs.Path] = fs.Hash
	}

	diff := &SnapshotDiff{
		FromSnapshotID: fromID,
		ToSnapshotID:   toID,
	}

	// 找出 added（to 有 from 无）
	for path := range toFiles {
		if _, exists := fromFiles[path]; !exists {
			diff.Added = append(diff.Added, path)
		}
	}
	// 找出 removed（from 有 to 无）
	for path := range fromFiles {
		if _, exists := toFiles[path]; !exists {
			diff.Removed = append(diff.Removed, path)
		}
	}
	// 找出 modified（都有但 hash 不同）
	for path, fromHash := range fromFiles {
		if toHash, exists := toFiles[path]; exists && toHash != fromHash {
			diff.Modified = append(diff.Modified, path)
		}
	}

	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Modified)
	return diff, nil
}

// ---- Step 5: CleanupSnapshots ----

// CleanupSnapshots 清理旧快照（Step 5）。
//
// 保留条件采用 AND 语义——快照必须同时满足两个条件才会保留：
//   - keepByCount: keepN == 0 表示不限数量（全部保留），否则仅保留最近 keepN 个
//   - keepByAge  : maxAge == 0 表示不过期（全部保留），否则仅保留创建未超过 maxAge 的
//
// 任一条件不满足即删除。这样：
//   - CleanupSnapshots(2, 0)          → 保留最近 2 个，删除其余
//   - CleanupSnapshots(0, 24h)        → 删除所有超过 24h 的快照
//   - CleanupSnapshots(5, 24h)        → 仅保留最近 5 个中未过期的
func (s *SnapshotService) CleanupSnapshots(keepN int, maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshots, err := s.loadMetadata()
	if err != nil {
		return 0, err
	}

	// 按创建时间降序排序（最新在前）
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].CreatedAt.After(snapshots[j].CreatedAt)
	})

	now := time.Now()
	var kept []Snapshot
	var deleted []Snapshot
	deletedCount := 0

	for i, snap := range snapshots {
		keepByCount := keepN == 0 || i < keepN
		keepByAge := maxAge == 0 || now.Sub(snap.CreatedAt) <= maxAge
		if keepByCount && keepByAge {
			kept = append(kept, snap)
			continue
		}
		deletedCount++
		deleted = append(deleted, snap)
	}

	if deletedCount > 0 {
		if err := s.saveMetadata(kept); err != nil {
			return 0, err
		}
		// C-4: cleanup blobs AFTER the kept list is fully determined,
		// so a hash referenced by both a deleted and a kept snapshot is
		// not removed (which would corrupt restore). Previously the call
		// happened inside the loop with an incomplete `kept` slice.
		_ = s.cleanupOrphanBlobs(kept, deleted)
	}
	return deletedCount, nil
}

// ---- 辅助方法 ----

// findSnapshot 查找指定 ID 的快照。
func (s *SnapshotService) findSnapshot(id string) (*Snapshot, error) {
	snapshots, err := s.loadMetadata()
	if err != nil {
		return nil, err
	}
	for _, snap := range snapshots {
		if snap.ID == id {
			snapCopy := snap
			return &snapCopy, nil
		}
	}
	return nil, fmt.Errorf("%w: snapshot %s not found", ErrInvalidInput, id)
}

// GetSnapshot 获取单个快照详情（供前端调用）。
func (s *SnapshotService) GetSnapshot(id string) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findSnapshot(id)
}
