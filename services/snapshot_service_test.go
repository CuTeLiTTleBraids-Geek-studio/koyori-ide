package services

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type snapshotWalkTestDirEntry struct {
	name      string
	mode      fs.FileMode
	info      fs.FileInfo
	infoErr   error
	infoCalls int
}

func (d *snapshotWalkTestDirEntry) Name() string      { return d.name }
func (d *snapshotWalkTestDirEntry) IsDir() bool       { return d.mode.IsDir() }
func (d *snapshotWalkTestDirEntry) Type() fs.FileMode { return d.mode.Type() }
func (d *snapshotWalkTestDirEntry) Info() (fs.FileInfo, error) {
	d.infoCalls++
	return d.info, d.infoErr
}

// newTestSnapshotService 创建测试用快照服务（临时目录）。
func newTestSnapshotService(t *testing.T) (*SnapshotService, string) {
	t.Helper()
	tmpDir := t.TempDir()
	svc := NewSnapshotService(tmpDir)
	return svc, tmpDir
}

// createTestWorkspace 创建测试工作区。
func createTestWorkspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	// 创建几个测试文件
	files := map[string]string{
		"main.go":      "package main\n\nfunc main() {}\n",
		"README.md":    "# Test Project\n",
		"src/utils.go": "package utils\n\nfunc Helper() {}\n",
		"src/const.go": "package utils\n\nconst Version = \"1.0\"\n",
		".gitignore":   "node_modules/\n",
	}
	for path, content := range files {
		fullPath := filepath.Join(ws, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	// 创建 .git 目录（应被跳过）
	gitDir := filepath.Join(ws, ".git", "refs")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main"), 0o644); err != nil {
		t.Fatalf("write .git/refs/HEAD: %v", err)
	}
	// 创建 node_modules（应被跳过）
	nmDir := filepath.Join(ws, "node_modules", "somepkg")
	if err := os.MkdirAll(nmDir, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nmDir, "index.js"), []byte("module.exports = {};"), 0o644); err != nil {
		t.Fatalf("write node_modules: %v", err)
	}
	return ws
}

func TestSnapshot_CreateSnapshot(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if snap.ID == "" {
		t.Error("expected non-empty ID")
	}
	if snap.Reason != SnapshotReasonManual {
		t.Errorf("expected reason manual, got %s", snap.Reason)
	}
	// 应包含 5 个文件（排除 .git 和 node_modules）
	if snap.FileCount != 5 {
		t.Errorf("expected 5 files, got %d", snap.FileCount)
	}
	if len(snap.Files) != 5 {
		t.Errorf("expected 5 file entries, got %d", len(snap.Files))
	}
	// 每个文件应有 hash
	for _, fs := range snap.Files {
		if fs.Hash == "" {
			t.Errorf("file %s has empty hash", fs.Path)
		}
		if !isValidHash(fs.Hash) {
			t.Errorf("file %s has invalid hash: %s", fs.Path, fs.Hash)
		}
	}
}

func TestSnapshot_CreateSnapshot_SkipsGitAndNodeModules(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	// 验证 .git 文件不在快照中
	for _, fs := range snap.Files {
		if fs.Path == ".git/refs/HEAD" {
			t.Error(".git file should be excluded from snapshot")
		}
		if fs.Path == "node_modules/somepkg/index.js" {
			t.Error("node_modules file should be excluded from snapshot")
		}
	}
}

func TestSnapshot_ListSnapshots(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap1, _ := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	time.Sleep(1 * time.Millisecond)
	snap2, _ := svc.CreateSnapshot(ws, string(SnapshotReasonPlanStep))

	list, err := svc.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(list))
	}
	// 按创建时间降序：snap2 应在前
	if list[0].ID != snap2.ID {
		t.Errorf("expected first snapshot %s, got %s", snap2.ID, list[0].ID)
	}
	if list[1].ID != snap1.ID {
		t.Errorf("expected second snapshot %s, got %s", snap1.ID, list[1].ID)
	}
}

func TestSnapshot_RestoreSnapshot(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// 修改文件
	mainPath := filepath.Join(ws, "main.go")
	if err := os.WriteFile(mainPath, []byte("modified content"), 0o644); err != nil {
		t.Fatalf("modify file: %v", err)
	}
	// 删除文件
	utilsPath := filepath.Join(ws, "src", "utils.go")
	_ = os.Remove(utilsPath)

	// 恢复快照
	if err := svc.RestoreSnapshot(snap.ID, ws); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	// 验证文件恢复
	data, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read restored main.go: %v", err)
	}
	if string(data) != "package main\n\nfunc main() {}\n" {
		t.Errorf("main.go not restored correctly: %s", string(data))
	}
	// 验证删除的文件恢复
	if _, err := os.Stat(utilsPath); err != nil {
		t.Error("utils.go should be restored")
	}
}

func TestSnapshot_RestorePartial(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// 修改两个文件
	mainPath := filepath.Join(ws, "main.go")
	readmePath := filepath.Join(ws, "README.md")
	os.WriteFile(mainPath, []byte("modified main"), 0o644)
	os.WriteFile(readmePath, []byte("modified readme"), 0o644)

	// 只恢复 main.go
	if err := svc.RestorePartial(snap.ID, ws, []string{"main.go"}); err != nil {
		t.Fatalf("RestorePartial: %v", err)
	}

	// main.go 应恢复
	mainData, _ := os.ReadFile(mainPath)
	if string(mainData) != "package main\n\nfunc main() {}\n" {
		t.Error("main.go should be restored")
	}
	// README.md 应保持修改
	readmeData, _ := os.ReadFile(readmePath)
	if string(readmeData) != "modified readme" {
		t.Error("README.md should remain modified")
	}
}

func TestSnapshot_DeleteSnapshot(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap1, _ := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	snap2, _ := svc.CreateSnapshot(ws, string(SnapshotReasonPlanStep))

	// 删除 snap1
	if err := svc.DeleteSnapshot(snap1.ID); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}

	list, _ := svc.ListSnapshots()
	if len(list) != 1 {
		t.Fatalf("expected 1 snapshot after delete, got %d", len(list))
	}
	if list[0].ID != snap2.ID {
		t.Errorf("expected remaining snapshot %s, got %s", snap2.ID, list[0].ID)
	}
}

func TestSnapshot_DeleteSnapshot_NotFound(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	err := svc.DeleteSnapshot("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent snapshot")
	}
}

func TestSnapshot_DiffSnapshots(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	// 第一个快照
	snap1, _ := svc.CreateSnapshot(ws, string(SnapshotReasonManual))

	// 修改文件
	os.WriteFile(filepath.Join(ws, "main.go"), []byte("modified"), 0o644)
	// 新增文件
	os.WriteFile(filepath.Join(ws, "new.go"), []byte("new file"), 0o644)
	// 删除文件
	os.Remove(filepath.Join(ws, "README.md"))

	// 第二个快照
	snap2, _ := svc.CreateSnapshot(ws, string(SnapshotReasonManual))

	diff, err := svc.DiffSnapshots(snap1.ID, snap2.ID)
	if err != nil {
		t.Fatalf("DiffSnapshots: %v", err)
	}
	if len(diff.Added) != 1 || diff.Added[0] != "new.go" {
		t.Errorf("expected added [new.go], got %v", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0] != "README.md" {
		t.Errorf("expected removed [README.md], got %v", diff.Removed)
	}
	if len(diff.Modified) != 1 || diff.Modified[0] != "main.go" {
		t.Errorf("expected modified [main.go], got %v", diff.Modified)
	}
}

func TestSnapshot_DiffSnapshots_NoChange(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap1, _ := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	snap2, _ := svc.CreateSnapshot(ws, string(SnapshotReasonManual))

	diff, err := svc.DiffSnapshots(snap1.ID, snap2.ID)
	if err != nil {
		t.Fatalf("DiffSnapshots: %v", err)
	}
	if len(diff.Added) != 0 || len(diff.Removed) != 0 || len(diff.Modified) != 0 {
		t.Errorf("expected no changes, got added=%v removed=%v modified=%v",
			diff.Added, diff.Removed, diff.Modified)
	}
}

func TestSnapshot_ContentAddressableDedup(t *testing.T) {
	// Step 4: 相同内容不应重复存储
	svc, tmpDir := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	// 创建两个快照（相同内容）
	svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	svc.CreateSnapshot(ws, string(SnapshotReasonManual))

	// 检查 blob 目录中的文件数
	blobDir := filepath.Join(tmpDir, "koyori-ide", "snapshots", "blobs")
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		t.Fatalf("read blob dir: %v", err)
	}
	// 应该只有 5 个 blob（5 个不同文件），不是 10 个
	if len(entries) != 5 {
		t.Errorf("expected 5 blobs (deduplicated), got %d", len(entries))
	}
}

func TestSnapshot_CleanupSnapshots_KeepN(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	// 创建 5 个快照
	for i := 0; i < 5; i++ {
		_, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
		if err != nil {
			t.Fatalf("CreateSnapshot %d: %v", i, err)
		}
		time.Sleep(1 * time.Millisecond)
	}

	// 保留最近 2 个
	deleted, err := svc.CleanupSnapshots(2, 0)
	if err != nil {
		t.Fatalf("CleanupSnapshots: %v", err)
	}
	if deleted != 3 {
		t.Errorf("expected 3 deleted, got %d", deleted)
	}
	list, _ := svc.ListSnapshots()
	if len(list) != 2 {
		t.Errorf("expected 2 remaining, got %d", len(list))
	}
}

func TestSnapshot_CleanupSnapshots_MaxAge(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	// 创建快照（第一个会被判定为过期）
	svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	// sleep 足够长时间确保第一个快照超过 maxAge，
	// 同时 maxAge 足够大确保第二个快照不会因 IO 延迟被误判过期
	time.Sleep(200 * time.Millisecond)
	svc.CreateSnapshot(ws, string(SnapshotReasonManual))

	// 清理超过 100ms 的（应该删除第一个，保留第二个）
	deleted, err := svc.CleanupSnapshots(0, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("CleanupSnapshots: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}
	list, _ := svc.ListSnapshots()
	if len(list) != 1 {
		t.Errorf("expected 1 remaining snapshot, got %d", len(list))
	}
}

func TestSnapshot_HashValidation(t *testing.T) {
	// isValidHash 验证
	validHash := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	if !isValidHash(validHash) {
		t.Error("valid hash rejected")
	}
	// 太短
	if isValidHash("abc123") {
		t.Error("short hash should be rejected")
	}
	// 含非十六进制字符
	if isValidHash("g1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2") {
		t.Error("hash with non-hex chars should be rejected")
	}
}

func TestSnapshot_GetSnapshot(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	snap, _ := svc.CreateSnapshot(ws, string(SnapshotReasonManual))

	found, err := svc.GetSnapshot(snap.ID)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if found.ID != snap.ID {
		t.Errorf("expected ID %s, got %s", snap.ID, found.ID)
	}
}

func TestSnapshot_GetSnapshot_NotFound(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	_, err := svc.GetSnapshot("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent snapshot")
	}
}

func TestSnapshot_IsIgnoredDir(t *testing.T) {
	ignoredDirs := []string{"node_modules", "dist", "build", ".next", "target", "__pycache__"}
	for _, d := range ignoredDirs {
		if !isIgnoredDir(d) {
			t.Errorf("expected %s to be ignored", d)
		}
	}
	if isIgnoredDir("src") {
		t.Error("src should not be ignored")
	}
}

func TestSnapshot_FilePermission0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission test skipped on Windows")
	}
	svc, tmpDir := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	svc.CreateSnapshot(ws, string(SnapshotReasonManual))

	metaPath := filepath.Join(tmpDir, "koyori-ide", "snapshots", "metadata.json")
	info, err := os.Stat(metaPath)
	if err != nil {
		t.Fatalf("stat metadata: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("expected metadata file permission 0600, got %o", perm)
	}
}

// TestCleanupOrphanBlobs_SharedHashPreserved_C4 (C-4)
// 验证 cleanupOrphanBlobs 不会删除被保留快照引用的 blob。
// 修复前 BUG：CleanupSnapshots 在循环中每次调用 cleanupOrphanBlobs
// 时 kept 还不完整，导致被删除快照引用但稍后被保留快照也引用的
// hash 被误删，进而 restore 时读不到 blob。
func TestCleanupOrphanBlobs_SharedHashPreserved_C4(t *testing.T) {
	svc, tmpDir := newTestSnapshotService(t)
	if err := svc.ensureDirs(); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	blobDir := filepath.Join(tmpDir, "koyori-ide", "snapshots", "blobs")

	// 准备 3 个不同 hash 的 blob：h1, h2, h3
	h1 := hashContent([]byte("content-h1"))
	h2 := hashContent([]byte("content-h2"))
	h3 := hashContent([]byte("content-h3"))
	for _, h := range []string{h1, h2, h3} {
		if err := os.WriteFile(filepath.Join(blobDir, h), []byte("blob-"+h), 0o644); err != nil {
			t.Fatalf("write blob %s: %v", h, err)
		}
	}

	// kept 快照引用 h1, h2
	kept := []Snapshot{
		{ID: "snap-kept", Files: []FileSnapshot{
			{Path: "a.go", Hash: h1},
			{Path: "b.go", Hash: h2},
		}},
	}
	// deleted 快照引用 h1（与 kept 共享）, h3（独占）
	deleted := []Snapshot{
		{ID: "snap-deleted", Files: []FileSnapshot{
			{Path: "a.go", Hash: h1},
			{Path: "c.go", Hash: h3},
		}},
	}

	if err := svc.cleanupOrphanBlobs(kept, deleted); err != nil {
		t.Fatalf("cleanupOrphanBlobs: %v", err)
	}

	// h1 应保留（被 kept 引用，即使 deleted 也引用）
	if _, err := os.Stat(filepath.Join(blobDir, h1)); err != nil {
		t.Errorf("h1 (shared with kept) should NOT be deleted: %v", err)
	}
	// h2 应保留（被 kept 独占引用）
	if _, err := os.Stat(filepath.Join(blobDir, h2)); err != nil {
		t.Errorf("h2 (kept-only) should NOT be deleted: %v", err)
	}
	// h3 应被删除（只被 deleted 引用）
	if _, err := os.Stat(filepath.Join(blobDir, h3)); err == nil {
		t.Errorf("h3 (deleted-only) should be removed")
	}
}

// TestCleanupSnapshots_SharedHashNotCorrupted_C4 (C-4)
// 端到端验证：当多个快照共享某些 blob 时，CleanupSnapshots 删除旧快照
// 不会误删被保留快照引用的 blob，保留快照仍可正常 restore。
// 这是修复前 BUG 的回归测试：循环中过早调用 cleanupOrphanBlobs 导致
// 后续保留快照的 blob 被误删，restore 时数据丢失。
func TestCleanupSnapshots_SharedHashNotCorrupted_C4(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	// snap1: 初始工作区
	snap1, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot snap1: %v", err)
	}

	// 修改 main.go 内容（产生新 hash），其他文件不变（共享 hash）
	mainPath := filepath.Join(ws, "main.go")
	modifiedContent := []byte("package main\n\nfunc main() { modified }\n")
	if err := os.WriteFile(mainPath, modifiedContent, 0o644); err != nil {
		t.Fatalf("modify main.go: %v", err)
	}

	// snap2: 与 snap1 共享 README.md/src/utils.go/src/const.go/.gitignore 的 hash
	time.Sleep(1 * time.Millisecond)
	snap2, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot snap2: %v", err)
	}

	// 找出共享的 hash（snap1 和 snap2 都引用）
	snap1Hashes := make(map[string]bool)
	for _, fs := range snap1.Files {
		snap1Hashes[fs.Hash] = true
	}
	sharedHashes := []string{}
	for _, fs := range snap2.Files {
		if snap1Hashes[fs.Hash] {
			sharedHashes = append(sharedHashes, fs.Hash)
		}
	}
	if len(sharedHashes) == 0 {
		t.Fatalf("test precondition: expected shared hashes between snap1 and snap2")
	}

	// 保留最近 1 个（snap2），删除 snap1
	deleted, err := svc.CleanupSnapshots(1, 0)
	if err != nil {
		t.Fatalf("CleanupSnapshots: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted, got %d", deleted)
	}

	// 验证共享 hash 的 blob 仍存在（被 snap2 引用，未被误删）
	// 这是修复前会失败的关键断言
	for _, h := range sharedHashes {
		blobPath := filepath.Join(svc.blobDir, h)
		if _, err := os.Stat(blobPath); err != nil {
			t.Errorf("shared blob %s should NOT be deleted (referenced by kept snap2): %v", h, err)
		}
	}

	// 验证 snap2 仍可正常 restore（blob 未被误删 → restore 不会报错）
	// 这是修复前会失败的关键验证：数据丢失导致 restore 失败
	if err := svc.RestoreSnapshot(snap2.ID, ws); err != nil {
		t.Errorf("RestoreSnapshot snap2 should succeed (shared blobs must be intact): %v", err)
	}

	// 进一步验证：snap2 中 main.go 内容正确恢复
	mainData, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read main.go after restore: %v", err)
	}
	if string(mainData) != string(modifiedContent) {
		t.Errorf("main.go content mismatch after restore: got %q, want %q", string(mainData), string(modifiedContent))
	}
}

// TestCleanupSnapshots_MultipleDeletedBatchKeepsShared_C4 (C-4)
// 验证一次 CleanupSnapshots 删除多个快照时，被保留快照与多个已删除快照
// 共享的 hash 不会被误删。这覆盖了修复前循环内调用 cleanupOrphanBlobs
// 时 kept 不完整的多个快照场景。
func TestCleanupSnapshots_MultipleDeletedBatchKeepsShared_C4(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	// 创建 3 个快照，README.md 内容不变（共享 hash）
	snap1, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot snap1: %v", err)
	}
	time.Sleep(1 * time.Millisecond)

	// 修改 main.go
	mainPath := filepath.Join(ws, "main.go")
	if err := os.WriteFile(mainPath, []byte("package main // v2\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("modify main.go v2: %v", err)
	}
	snap2, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot snap2: %v", err)
	}
	time.Sleep(1 * time.Millisecond)

	// 再次修改 main.go
	if err := os.WriteFile(mainPath, []byte("package main // v3\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("modify main.go v3: %v", err)
	}
	snap3, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot snap3: %v", err)
	}

	// 收集 snap3 中与 snap1/snap2 共享的 hash（README.md/utils.go/const.go/.gitignore）
	snap1Hashes := make(map[string]bool)
	for _, fs := range snap1.Files {
		snap1Hashes[fs.Hash] = true
	}
	snap2Hashes := make(map[string]bool)
	for _, fs := range snap2.Files {
		snap2Hashes[fs.Hash] = true
	}
	sharedWithKept := []string{}
	for _, fs := range snap3.Files {
		if snap1Hashes[fs.Hash] || snap2Hashes[fs.Hash] {
			sharedWithKept = append(sharedWithKept, fs.Hash)
		}
	}
	if len(sharedWithKept) == 0 {
		t.Fatalf("test precondition: expected shared hashes between snap3 and snap1/snap2")
	}

	// 保留最近 1 个（snap3），删除 snap1 和 snap2
	deleted, err := svc.CleanupSnapshots(1, 0)
	if err != nil {
		t.Fatalf("CleanupSnapshots: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("expected 2 deleted, got %d", deleted)
	}

	// 验证共享 hash 的 blob 仍存在
	for _, h := range sharedWithKept {
		blobPath := filepath.Join(svc.blobDir, h)
		if _, err := os.Stat(blobPath); err != nil {
			t.Errorf("shared blob %s should NOT be deleted (referenced by kept snap3): %v", h, err)
		}
	}

	// 验证 snap3 仍可正常 restore
	if err := svc.RestoreSnapshot(snap3.ID, ws); err != nil {
		t.Errorf("RestoreSnapshot snap3 should succeed (shared blobs must be intact): %v", err)
	}

	// snap1 和 snap2 已被删除，应无法 restore
	if err := svc.RestoreSnapshot(snap1.ID, ws); err == nil {
		t.Error("RestoreSnapshot snap1 should fail (deleted)")
	}
	if err := svc.RestoreSnapshot(snap2.ID, ws); err == nil {
		t.Error("RestoreSnapshot snap2 should fail (deleted)")
	}
}

// TestDeleteSnapshot_SharedHashPreserved_C4 (C-4)
// 验证 DeleteSnapshot 删除单个快照时，与剩余快照共享的 blob 不会被误删。
// DeleteSnapshot 也使用新的 cleanupOrphanBlobs(kept, deleted) 签名。
func TestDeleteSnapshot_SharedHashPreserved_C4(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := createTestWorkspace(t)

	// 创建两个快照（内容相同，全部 hash 共享）
	snap1, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot snap1: %v", err)
	}
	time.Sleep(1 * time.Millisecond)
	snap2, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot snap2: %v", err)
	}

	// 收集 snap1 的所有 hash（snap2 与 snap1 完全相同）
	snap1Hashes := []string{}
	for _, fs := range snap1.Files {
		snap1Hashes = append(snap1Hashes, fs.Hash)
	}

	// 删除 snap1（与 snap2 共享所有 hash）
	if err := svc.DeleteSnapshot(snap1.ID); err != nil {
		t.Fatalf("DeleteSnapshot snap1: %v", err)
	}

	// 验证所有 hash 的 blob 仍存在（被 snap2 引用，未被误删）
	for _, h := range snap1Hashes {
		blobPath := filepath.Join(svc.blobDir, h)
		if _, err := os.Stat(blobPath); err != nil {
			t.Errorf("shared blob %s should NOT be deleted (referenced by remaining snap2): %v", h, err)
		}
	}

	// 验证 snap2 仍可正常 restore
	if err := svc.RestoreSnapshot(snap2.ID, ws); err != nil {
		t.Errorf("RestoreSnapshot snap2 should succeed (shared blobs must be intact): %v", err)
	}
}

// ---- M-3: generateSnapshotID random suffix ----

// TestSnapshotService_M3_IDHasRandomSuffix (M-3) generates 1000 snapshot IDs
// and verifies they are all unique. With the old timestamp-only ID, two
// calls in the same nanosecond would collide. With the crypto/rand suffix,
// all 1000 should be distinct. Also verifies the format includes the random
// suffix (16 hex chars after the timestamp).
func TestSnapshotService_M3_IDHasRandomSuffix(t *testing.T) {
	const n = 1000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id := generateSnapshotID()
		if seen[id] {
			t.Fatalf("duplicate snapshot ID after %d iterations: %s", i, id)
		}
		seen[id] = true
	}

	// Verify the format: snap-<nanos>-<16 hex chars>.
	id := generateSnapshotID()
	if !strings.HasPrefix(id, "snap-") {
		t.Errorf("snapshot ID should start with 'snap-', got %q", id)
	}
	parts := strings.SplitN(id, "-", 3)
	if len(parts) != 3 {
		t.Fatalf("snapshot ID should have format snap-<ts>-<randhex>, got %q (parts=%v)", id, parts)
	}
	if len(parts[2]) != 16 {
		t.Errorf("random suffix should be 16 hex chars (8 bytes), got %d chars: %q", len(parts[2]), parts[2])
	}
	// Verify the suffix is valid hex.
	for _, c := range parts[2] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("random suffix should be lowercase hex, found %q in %q", c, parts[2])
		}
	}
}

// ---- M-4: CreateSnapshot streaming hash ----

// TestSnapshotService_M4_StreamingHash (M-4) verifies that streaming hashing
// (io.Copy(hash, file)) produces the same hash as the legacy whole-file
// approach (hashContent(data)). We hash the same content both ways and
// confirm they match.
func TestSnapshotService_M4_StreamingHash(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := t.TempDir()

	content := []byte("streaming hash test content\nwith multiple lines\n")
	if err := os.WriteFile(filepath.Join(ws, "file.txt"), content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	// Expected hash via the legacy whole-file helper.
	expected := hashContent(content)

	snap, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	// Find the file in the snapshot and verify the streaming hash matches.
	var found bool
	for _, fs := range snap.Files {
		if fs.Path == "file.txt" {
			if fs.Hash != expected {
				t.Errorf("streaming hash mismatch: got %s, want %s", fs.Hash, expected)
			}
			if fs.Size != int64(len(content)) {
				t.Errorf("size mismatch: got %d, want %d", fs.Size, len(content))
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("file.txt not found in snapshot")
	}
}

// TestCreateSnapshot_StreamingHash_LargeFile (M-4) verifies the streaming
// approach works for a file larger than the default io.Copy buffer — i.e.
// the hash is computed across multiple chunks. Uses ~256KB to ensure at
// least one full buffer flush.
func TestCreateSnapshot_StreamingHash_LargeFile(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := t.TempDir()

	// 256KB of repeating data.
	content := make([]byte, 256*1024)
	for i := range content {
		content[i] = byte(i % 251) // arbitrary prime for variety
	}
	if err := os.WriteFile(filepath.Join(ws, "big.bin"), content, 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}
	expected := hashContent(content)

	snap, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	for _, fs := range snap.Files {
		if fs.Path == "big.bin" {
			if fs.Hash != expected {
				t.Errorf("streaming hash mismatch on large file: got %s, want %s", fs.Hash, expected)
			}
			return
		}
	}
	t.Fatal("big.bin not found in snapshot")
}

// TestCreateSnapshot_IgnoresDefaultDirs (M-4) verifies that the default
// ignore list (node_modules, dist, build, .git, .next, target) is honored —
// files inside these dirs must not appear in the snapshot.
func TestCreateSnapshot_IgnoresDefaultDirs(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := t.TempDir()

	// Create files in directories that should be ignored.
	ignoredDirs := []string{"node_modules", "dist", "build", ".git", ".next", "target", "__pycache__"}
	for _, dir := range ignoredDirs {
		full := filepath.Join(ws, dir, "file.txt")
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(full, []byte("ignored"), 0o644); err != nil {
			t.Fatalf("write %s: %v", dir, err)
		}
	}

	// Create a regular file that should be included.
	if err := os.WriteFile(filepath.Join(ws, "keep.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("write keep.go: %v", err)
	}

	snap, err := svc.CreateSnapshot(ws, string(SnapshotReasonManual))
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	if snap.FileCount != 1 {
		t.Fatalf("expected 1 file (keep.go), got %d: %v", snap.FileCount, snap.Files)
	}
	if snap.Files[0].Path != "keep.go" {
		t.Errorf("expected keep.go, got %q", snap.Files[0].Path)
	}

	// Defensive: none of the ignored dirs should appear in any file path.
	for _, fs := range snap.Files {
		for _, dir := range ignoredDirs {
			if strings.HasPrefix(fs.Path, dir+"/") || fs.Path == dir {
				t.Errorf("ignored dir %q leaked into snapshot at %q", dir, fs.Path)
			}
		}
	}
}

// TestStoreBlobFromFileConsistency (P2-3 / N-1) 验证 storeBlobFromFile
// 计算出的 hash 与写入 blob 的实际内容严格一致 —— 即修复后不再存在
// 两次 Open 之间的 TOCTOU 窗口。覆盖小文件与跨 io.Copy 缓冲区的大文件。
func TestStoreBlobFromFileConsistency(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := t.TempDir()

	cases := []struct {
		name    string
		content []byte
	}{
		{"small", []byte("hello world\n")},
		{"empty", []byte{}},
		{"multi-chunk", bytes.Repeat([]byte("abcdefg"), 64*1024)}, // ~448KB，确保跨 io.Copy 缓冲区
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(ws, c.name+".bin")
			if err := os.WriteFile(path, c.content, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			hash, err := svc.storeBlobFromFile(path)
			if err != nil {
				t.Fatalf("storeBlobFromFile: %v", err)
			}
			// 期望 hash 等于直接对原内容计算
			expected := hashContent(c.content)
			if hash != expected {
				t.Errorf("hash mismatch: got %s, want %s", hash, expected)
			}
			// 读取 blob 内容并验证其 hash 与返回的 hash 严格一致
			blob, err := svc.readBlob(hash)
			if err != nil {
				t.Fatalf("readBlob: %v", err)
			}
			blobHash := hashContent(blob)
			if blobHash != hash {
				t.Errorf("TOCTOU 残留: storeBlobFromFile 返回 hash=%s, 但 blob 实际内容 hash=%s", hash, blobHash)
			}
			if !bytes.Equal(blob, c.content) {
				t.Errorf("blob 内容与原文件不一致: got %d bytes, want %d bytes", len(blob), len(c.content))
			}
		})
	}
}

// TestStoreBlobFromFileConsistency_Concurrent (P2-3 / N-1) 在 storeBlobFromFile
// 执行期间并发覆盖源文件，验证无论文件如何被外部改写，返回的 hash 与
// 写入的 blob 内容始终一致 —— 即 MultiWriter 同步写入消除了 TOCTOU 窗口。
func TestStoreBlobFromFileConsistency_Concurrent(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := t.TempDir()
	path := filepath.Join(ws, "race.bin")
	initial := bytes.Repeat([]byte("A"), 32*1024)
	if err := os.WriteFile(path, initial, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				content := bytes.Repeat([]byte{byte('B' + i%26)}, 32*1024)
				_ = os.WriteFile(path, content, 0o644)
				i++
			}
		}
	}()

	// 多次调用 storeBlobFromFile，每次都验证 hash 与 blob 内容一致
	for i := 0; i < 30; i++ {
		hash, err := svc.storeBlobFromFile(path)
		if err != nil {
			t.Errorf("iter %d: storeBlobFromFile: %v", i, err)
			continue
		}
		blob, err := svc.readBlob(hash)
		if err != nil {
			t.Errorf("iter %d: readBlob: %v", i, err)
			continue
		}
		blobHash := hashContent(blob)
		if blobHash != hash {
			t.Errorf("iter %d: TOCTOU 检测 — hash=%s 与 blob 实际内容 hash=%s 不一致", i, hash, blobHash)
		}
	}

	close(stop)
	wg.Wait()
}

// TestCreateSnapshotNonBlocking (N-6) 验证 CreateSnapshot 的 walk + hash
// 在锁外执行，期间 ListSnapshots 不被长时间阻塞。
//
// 旧代码 CreateSnapshot 全程持锁，walk 数千文件时 ListSnapshots 被阻塞
// 数十秒。N-6 修复后 walk 在锁外，ListSnapshots 只在 loadMetadata 时
// 短暂加锁（< 100ms）。
func TestCreateSnapshotNonBlocking(t *testing.T) {
	svc, _ := newTestSnapshotService(t)
	ws := t.TempDir()

	// 创建足够多文件让 walk + hash 耗时 > 100ms（确保 ListSnapshots
	// 有机会在 walk 期间执行）
	const fileCount = 800
	for i := 0; i < fileCount; i++ {
		content := bytes.Repeat([]byte{byte(i % 256)}, 4*1024)
		path := filepath.Join(ws, fmt.Sprintf("file_%04d.bin", i))
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatalf("write file %d: %v", i, err)
		}
	}

	// 启动 CreateSnapshot goroutine
	snapDone := make(chan error, 1)
	go func() {
		_, err := svc.CreateSnapshot(ws, "n6-test")
		snapDone <- err
	}()

	// 等待 CreateSnapshot 进入 walk
	time.Sleep(20 * time.Millisecond)

	// 在 CreateSnapshot walk 期间反复调用 ListSnapshots，测量最大耗时
	const nonBlockThreshold = 100 * time.Millisecond
	var maxListDuration time.Duration
	for i := 0; i < 5; i++ {
		start := time.Now()
		_, err := svc.ListSnapshots()
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("ListSnapshots[%d]: %v", i, err)
		}
		if elapsed > maxListDuration {
			maxListDuration = elapsed
		}
		time.Sleep(10 * time.Millisecond)
	}

	if maxListDuration > nonBlockThreshold {
		t.Errorf("ListSnapshots 在 CreateSnapshot 期间被阻塞 %v（阈值 %v），疑似 walk 持锁 (N-6 回归)",
			maxListDuration, nonBlockThreshold)
	}

	// 等待 CreateSnapshot 完成
	if err := <-snapDone; err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	t.Logf("ListSnapshots 最大耗时 %v（walk 在锁外，阈值 %v）", maxListDuration, nonBlockThreshold)
}

func TestSnapshotWalkDirFunc_FiltersBeforeInfo(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(filePath)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		path        string
		entry       *snapshotWalkTestDirEntry
		walkErr     error
		wantResult  error
		wantInfo    int
		wantVisited int
		wantRel     string
	}{
		{name: "permission error", path: filepath.Join(root, "private"), entry: &snapshotWalkTestDirEntry{name: "private", mode: fs.ModeDir, info: fileInfo}, walkErr: fs.ErrPermission},
		{name: "git directory", path: filepath.Join(root, ".git"), entry: &snapshotWalkTestDirEntry{name: ".git", mode: fs.ModeDir, info: fileInfo}, wantResult: filepath.SkipDir},
		{name: "ignored directory", path: filepath.Join(root, "node_modules"), entry: &snapshotWalkTestDirEntry{name: "node_modules", mode: fs.ModeDir, info: fileInfo}, wantResult: filepath.SkipDir},
		{name: "ordinary directory", path: filepath.Join(root, "src"), entry: &snapshotWalkTestDirEntry{name: "src", mode: fs.ModeDir, info: fileInfo}},
		{name: "file info error", path: filepath.Join(root, "unreadable.go"), entry: &snapshotWalkTestDirEntry{name: "unreadable.go", infoErr: fs.ErrPermission}, wantInfo: 1},
		{name: "regular file", path: filePath, entry: &snapshotWalkTestDirEntry{name: "main.go", info: fileInfo}, wantInfo: 1, wantVisited: 1, wantRel: filepath.Join("src", "main.go")},
		{name: "symbolic link", path: filepath.Join(root, "main-link"), entry: &snapshotWalkTestDirEntry{name: "main-link", mode: fs.ModeSymlink, info: fileInfo}, wantInfo: 1, wantVisited: 1, wantRel: "main-link"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			visited := 0
			gotRel := ""
			walkFn := snapshotWalkDirFunc(root, func(_ string, rel string, _ fs.FileInfo) error {
				visited++
				gotRel = rel
				return nil
			})

			got := walkFn(tt.path, tt.entry, tt.walkErr)
			if got != tt.wantResult {
				t.Fatalf("callback result = %v, want %v", got, tt.wantResult)
			}
			if tt.entry.infoCalls != tt.wantInfo {
				t.Errorf("Info calls = %d, want %d", tt.entry.infoCalls, tt.wantInfo)
			}
			if visited != tt.wantVisited {
				t.Errorf("file visits = %d, want %d", visited, tt.wantVisited)
			}
			if gotRel != tt.wantRel {
				t.Errorf("relative path = %q, want %q", gotRel, tt.wantRel)
			}
		})
	}
}
