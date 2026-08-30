package services

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newFileServiceAt(t *testing.T, root string) *FileService {
	t.Helper()
	svc := NewFileService()
	if err := svc.setWorkspaceRoot(root); err != nil {
		t.Fatalf("SetWorkspaceRoot(%q): %v", root, err)
	}
	t.Cleanup(func() { _ = svc.close() })
	return svc
}

func TestFileService_ListDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	entries, err := svc.ListDirectory(dir)
	if err != nil {
		t.Fatalf("ListDirectory failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if !entries[0].IsDir {
		t.Error("expected directory to sort first")
	}
	if entries[0].Name != "subdir" {
		t.Errorf("expected subdir first, got %s", entries[0].Name)
	}
}

func TestFileService_ReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world"), 0644)

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	content, err := svc.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if content != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", content)
	}
}

func TestFileService_ReadFileRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxReadableFileBytes+1); err != nil {
		t.Fatal(err)
	}

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	if _, err := svc.ReadFile(path); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("ReadFile error = %v, want oversized-file error", err)
	}
}

func TestFileService_ReadFileLeafReplacementUsesOpenedHandle(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	detached := filepath.Join(dir, "target-detached.txt")
	if err := os.WriteFile(target, []byte("opened-object"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newFileServiceAt(t, dir)
	svc.readFileAfterStat = func() error {
		if err := os.Rename(target, detached); err != nil {
			return err
		}
		replacement, err := os.Create(target)
		if err != nil {
			return err
		}
		if err := replacement.Truncate(maxReadableFileBytes + 1); err != nil {
			_ = replacement.Close()
			return err
		}
		return replacement.Close()
	}
	content, err := svc.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile after leaf replacement: %v", err)
	}
	if content != "opened-object" {
		t.Fatalf("ReadFile returned replacement content (%d bytes), want opened object", len(content))
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != maxReadableFileBytes+1 {
		t.Fatalf("replacement size = %d, want %d", info.Size(), maxReadableFileBytes+1)
	}
}

func TestFileService_WriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	err := svc.WriteFile(path, "written content")
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "written content" {
		t.Errorf("expected 'written content', got '%s'", string(data))
	}
}

func TestFileService_WriteFileCreatesMissingParents(t *testing.T) {
	dir := t.TempDir()
	svc := newFileServiceAt(t, dir)
	target := filepath.Join(dir, "missing", "deep", "file.txt")
	if err := svc.WriteFile(target, "nested content"); err != nil {
		t.Fatalf("WriteFile with missing parents: %v", err)
	}
	assertFileContent(t, target, "nested content")
}

// TestFileService_H1_InitialBypass_ParentSymlinkSwapAfterValidation captures
// H1's initial bypass condition. A pathname write after validation would follow
// the replacement link and modify outsideFile. This test describes that bypass
// case; it does not claim a historical test execution or result.
func TestFileService_H1_InitialBypass_ParentSymlinkSwapAfterValidation(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	parent := filepath.Join(workspace, "parent")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(workspace, "symlink-probe")
	if !trySymlinkOrFail(t, probe, outside) {
		return
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(parent, "escape.txt")
	outsideFile := filepath.Join(outside, "escape.txt")
	if err := os.WriteFile(outsideFile, []byte("outside-original"), 0644); err != nil {
		t.Fatal(err)
	}
	movedParent := parent + "-old"
	swapped := false
	svc := newFileServiceAt(t, workspace)
	svc.rootOperationHook = func(operation string) error {
		if operation != "WriteFile" {
			return nil
		}
		if err := os.Rename(parent, movedParent); err != nil {
			return err
		}
		if err := os.Symlink(outside, parent); err != nil {
			return err
		}
		swapped = true
		return nil
	}

	if err := svc.WriteFile(target, "must not leave workspace"); err == nil {
		t.Fatal("WriteFile succeeded through a parent replaced by an external symlink")
	}
	if !swapped {
		t.Fatal("test hook did not replace the validated parent")
	}
	gotOutside, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotOutside) != "outside-original" {
		t.Fatalf("outside file was modified after parent replacement: %q", gotOutside)
	}
	if _, err := os.Stat(filepath.Join(movedParent, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("detached parent was modified: %v", err)
	}
}

func TestFileService_H1_WriteFile_RootSymlinkSwapAfterValidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 的 os.Root 目录句柄会阻止 rename（本地探针验证：句柄打开期间 rename 恒失败），无法用 rename 模拟 root 替换攻击；该平台的绑定根保护由句柄本身强制，强于 unix 的路径解析检查。unix CI 腿仍然执行本测试。")
	}
	workspace := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "escape.txt")
	if err := os.WriteFile(outsideFile, []byte("outside-original"), 0644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workspace, "escape.txt")
	oldWorkspace := workspace + "-old"
	probe := filepath.Join(workspace, "symlink-probe")
	if !trySymlinkOrFail(t, probe, outside) {
		return
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	swapped := false
	svc := newFileServiceAt(t, workspace)
	svc.rootOperationHook = func(operation string) error {
		if operation != "WriteFile" {
			return nil
		}
		if err := os.Rename(workspace, oldWorkspace); err != nil {
			return err
		}
		if err := os.Symlink(outside, workspace); err != nil {
			return err
		}
		swapped = true
		return nil
	}

	writeErr := svc.WriteFile(target, "must stay in workspace")
	if !swapped {
		t.Fatal("test hook did not replace the validated root")
	}
	gotOutside, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotOutside) != "outside-original" {
		t.Fatalf("outside file was modified after parent replacement: %q", gotOutside)
	}
	if writeErr != nil {
		t.Fatalf("WriteFile: %v", writeErr)
	}
	gotInside, err := os.ReadFile(filepath.Join(oldWorkspace, "escape.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotInside) != "must stay in workspace" {
		t.Fatalf("bound workspace file = %q", gotInside)
	}
}

func TestFileService_H1_ParentSwap_AllOperationsStayInsideRoot(t *testing.T) {
	operations := []struct {
		name string
		run  func(*FileService, string) error
	}{
		{"ReadFile", func(s *FileService, parent string) error {
			_, err := s.ReadFile(filepath.Join(parent, "item.txt"))
			return err
		}},
		{"WriteFile", func(s *FileService, parent string) error {
			return s.WriteFile(filepath.Join(parent, "item.txt"), "changed")
		}},
		{"CreateFile", func(s *FileService, parent string) error { return s.CreateFile(filepath.Join(parent, "created.txt")) }},
		{"CreateDirectory", func(s *FileService, parent string) error {
			return s.CreateDirectory(filepath.Join(parent, "created-dir"))
		}},
		{"DeletePath", func(s *FileService, parent string) error { return s.DeletePath(filepath.Join(parent, "item.txt")) }},
		{"RenamePath", func(s *FileService, parent string) error {
			return s.RenamePath(filepath.Join(parent, "item.txt"), filepath.Join(parent, "renamed.txt"))
		}},
		{"ListDirectory", func(s *FileService, parent string) error { _, err := s.ListDirectory(parent); return err }},
		{"ListAllFiles", func(s *FileService, parent string) error { _, err := s.ListAllFiles(parent); return err }},
		{"WriteFileIfUnchanged", func(s *FileService, parent string) error {
			return s.WriteFileIfUnchanged(filepath.Join(parent, "item.txt"), "changed", contentHash([]byte("inside")))
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			base := t.TempDir()
			workspace := filepath.Join(base, "workspace")
			outside := filepath.Join(base, "outside")
			parent := filepath.Join(workspace, "parent")
			if err := os.MkdirAll(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(parent, "item.txt"), []byte("inside"), 0o640); err != nil {
				t.Fatal(err)
			}
			outsideFile := filepath.Join(outside, "item.txt")
			if err := os.WriteFile(outsideFile, []byte("outside-sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			if !trySymlinkOrFail(t, filepath.Join(workspace, "probe"), outside) {
				return
			}
			if err := os.Remove(filepath.Join(workspace, "probe")); err != nil {
				t.Fatal(err)
			}
			detached := parent + "-detached"
			svc := newFileServiceAt(t, workspace)
			svc.rootOperationHook = func(got string) error {
				if got != operation.name {
					return nil
				}
				if err := os.Rename(parent, detached); err != nil {
					return err
				}
				return os.Symlink(outside, parent)
			}
			if err := operation.run(svc, parent); err == nil {
				t.Fatalf("%s succeeded after parent swap", operation.name)
			}
			assertFileContent(t, outsideFile, "outside-sentinel")
			if _, err := os.Stat(filepath.Join(outside, "created.txt")); !os.IsNotExist(err) {
				t.Fatalf("outside create result changed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(outside, "created-dir")); !os.IsNotExist(err) {
				t.Fatalf("outside mkdir result changed: %v", err)
			}
		})
	}
}

func TestFileService_H1_RootSwap_AllOperationsUseBoundRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 的 os.Root 目录句柄会阻止 rename（本地探针验证：句柄打开期间 rename 恒失败），无法用 rename 模拟 root 替换攻击；该平台的绑定根保护由句柄本身强制，强于 unix 的路径解析检查。unix CI 腿仍然执行本测试。")
	}
	operations := []struct {
		name string
		run  func(*FileService, string) error
	}{
		{"ReadFile", func(s *FileService, root string) error {
			_, err := s.ReadFile(filepath.Join(root, "item.txt"))
			return err
		}},
		{"WriteFile", func(s *FileService, root string) error {
			return s.WriteFile(filepath.Join(root, "item.txt"), "changed")
		}},
		{"CreateFile", func(s *FileService, root string) error { return s.CreateFile(filepath.Join(root, "created.txt")) }},
		{"CreateDirectory", func(s *FileService, root string) error { return s.CreateDirectory(filepath.Join(root, "created-dir")) }},
		{"DeletePath", func(s *FileService, root string) error { return s.DeletePath(filepath.Join(root, "item.txt")) }},
		{"RenamePath", func(s *FileService, root string) error {
			return s.RenamePath(filepath.Join(root, "item.txt"), filepath.Join(root, "renamed.txt"))
		}},
		{"ListDirectory", func(s *FileService, root string) error { _, err := s.ListDirectory(root); return err }},
		{"ListAllFiles", func(s *FileService, root string) error { _, err := s.ListAllFiles(root); return err }},
		{"WriteFileIfUnchanged", func(s *FileService, root string) error {
			return s.WriteFileIfUnchanged(filepath.Join(root, "item.txt"), "changed", contentHash([]byte("inside")))
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			base := t.TempDir()
			workspace := filepath.Join(base, "workspace")
			outside := filepath.Join(base, "outside")
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "item.txt"), []byte("inside"), 0o640); err != nil {
				t.Fatal(err)
			}
			outsideFile := filepath.Join(outside, "item.txt")
			if err := os.WriteFile(outsideFile, []byte("outside-sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			if !trySymlinkOrFail(t, filepath.Join(workspace, "probe"), outside) {
				return
			}
			if err := os.Remove(filepath.Join(workspace, "probe")); err != nil {
				t.Fatal(err)
			}
			detached := workspace + "-detached"
			svc := newFileServiceAt(t, workspace)
			svc.rootOperationHook = func(got string) error {
				if got != operation.name {
					return nil
				}
				if err := os.Rename(workspace, detached); err != nil {
					return err
				}
				return os.Symlink(outside, workspace)
			}
			if err := operation.run(svc, workspace); err != nil {
				t.Fatalf("%s on bound root: %v", operation.name, err)
			}
			assertFileContent(t, outsideFile, "outside-sentinel")
			if _, err := os.Stat(filepath.Join(outside, "created.txt")); !os.IsNotExist(err) {
				t.Fatalf("outside create result changed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(outside, "created-dir")); !os.IsNotExist(err) {
				t.Fatalf("outside mkdir result changed: %v", err)
			}
		})
	}
}

func TestFileService_H1_WorkspaceSwitchRetiresRootsAfterCapabilityRelease(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	svc := newFileServiceAt(t, rootA)
	capability, err := svc.acquireCapability(filepath.Join(rootA, "held.txt"), true)
	if err != nil {
		t.Fatal(err)
	}
	old := capability.workspace
	if err := svc.setWorkspaceRoot(rootB); err != nil {
		t.Fatal(err)
	}
	if old.roots[0].root == nil {
		t.Fatal("retired root closed while an operation still held a capability")
	}
	if err := capability.withCurrent(func() error { return nil }); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("old capability error = %v, want ErrNotAllowed", err)
	}
	capability.releaseCapability()
	if old.roots[0].root != nil {
		t.Fatal("retired root handle remained open after capability release")
	}
}

func TestFileService_H1_SetWorkspaceRootsFailureRollsBack(t *testing.T) {
	root := canonicalTestPath(t, t.TempDir())
	svc := newFileServiceAt(t, root)
	previous := svc.secureWorkspace
	err := svc.setWorkspaceRoots([]string{t.TempDir(), filepath.Join(t.TempDir(), "missing")})
	if err == nil {
		t.Fatal("SetWorkspaceRoots accepted a missing secondary root")
	}
	if svc.secureWorkspace != previous || svc.WorkspaceRoots()[0] != root {
		t.Fatal("failed root set changed the active workspace")
	}
	capability, err := svc.acquireCapability(root, false)
	if err != nil {
		t.Fatalf("acquire root after failed switch: %v", err)
	}
	defer capability.releaseCapability()
	if _, err := capability.root.root.Stat("."); err != nil {
		t.Fatalf("active root was closed by failed switch: %v", err)
	}
}

func TestFileService_H1_MultiRootRenameFailsClosed(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	source := filepath.Join(rootA, "source.txt")
	destination := filepath.Join(rootB, "destination.txt")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewFileService()
	t.Cleanup(func() { _ = svc.close() })
	if err := svc.setWorkspaceRoots([]string{rootA, rootB}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RenamePath(source, destination); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-root rename error = %v, want ErrNotAllowed", err)
	}
	assertFileContent(t, source, "source")
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("cross-root destination changed: %v", err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func TestFileService_CreateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	err := svc.CreateFile(path)
	if err != nil {
		t.Fatalf("CreateFile failed: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Size() != 0 {
		t.Errorf("expected empty file, got size %d", info.Size())
	}
}

func TestFileService_CreateDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c")

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	err := svc.CreateDirectory(path)
	if err != nil {
		t.Fatalf("CreateDirectory failed: %v", err)
	}
	info, _ := os.Stat(path)
	if !info.IsDir() {
		t.Error("expected directory to exist")
	}
}

func TestFileService_DeletePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")
	os.WriteFile(path, []byte("x"), 0644)

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	err := svc.DeletePath(path)
	if err != nil {
		t.Fatalf("DeletePath failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestFileService_RenamePath(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	newPath := filepath.Join(dir, "new.txt")
	os.WriteFile(oldPath, []byte("x"), 0644)

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	err := svc.RenamePath(oldPath, newPath)
	if err != nil {
		t.Fatalf("RenamePath failed: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Error("expected new file to exist")
	}
}

// --- Path sandboxing tests ---

func TestFileService_NoWorkspace_RejectsFileOperations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "free.txt")
	if err := os.WriteFile(path, []byte("unchanged"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	svc := &FileService{}
	if err := svc.WriteFile(path, "data"); err == nil {
		t.Fatal("WriteFile should fail without workspace root")
	}
	if err := svc.CreateFile(path); err == nil {
		t.Fatal("CreateFile should fail without workspace root")
	}
	if err := svc.DeletePath(path); err == nil {
		t.Fatal("DeletePath should fail without workspace root")
	}
	if _, err := svc.ReadFile(path); err == nil {
		t.Fatal("ReadFile should fail without workspace root")
	}
	if _, err := svc.ListDirectory(dir); err == nil {
		t.Fatal("ListDirectory should fail without workspace root")
	}
	if _, err := svc.ListAllFiles(dir); err == nil {
		t.Fatal("ListAllFiles should fail without workspace root")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("rejected operations changed the outside file: data=%q err=%v", data, err)
	}
}

func TestFileService_WorkspaceAllowsInsidePath(t *testing.T) {
	workspace := t.TempDir()
	innerFile := filepath.Join(workspace, "inside.txt")

	svc := newFileServiceAt(t, workspace)
	if err := svc.WriteFile(innerFile, "data"); err != nil {
		t.Fatalf("WriteFile inside workspace should succeed: %v", err)
	}
}

func TestFileService_WorkspaceRejectsOutsidePath(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	if err := svc.WriteFile(outsideFile, "data"); err == nil {
		t.Error("WriteFile outside workspace should fail")
	}
	if _, err := svc.ReadFile(outsideFile); err == nil {
		t.Error("ReadFile outside workspace should fail")
	}
	if err := svc.CreateFile(outsideFile); err == nil {
		t.Error("CreateFile outside workspace should fail")
	}
	if err := svc.DeletePath(outsideFile); err == nil {
		t.Error("DeletePath outside workspace should fail")
	}
	if _, err := svc.ListDirectory(outside); err == nil {
		t.Error("ListDirectory outside workspace should fail")
	}
	data, err := os.ReadFile(outsideFile)
	if err != nil || string(data) != "secret" {
		t.Fatalf("rejected operations changed the outside file: data=%q err=%v", data, err)
	}
}

func TestFileService_WorkspaceRejectsTraversalPath(t *testing.T) {
	workspace := t.TempDir()
	// Create a subdirectory and try to traverse up
	os.Mkdir(filepath.Join(workspace, "subdir"), 0755)
	traversalPath := filepath.Join(workspace, "subdir", "..", "..", "etc", "passwd")

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	if _, err := svc.ReadFile(traversalPath); err == nil {
		t.Error("ReadFile with traversal path should fail")
	}
}

func TestFileService_SetWorkspaceRootInvalidPath(t *testing.T) {
	svc := &FileService{}
	if err := svc.setWorkspaceRoot("/nonexistent/path/xyz"); err == nil {
		t.Error("SetWorkspaceRoot with non-existent path should fail")
	}
}

func TestFileService_SetWorkspaceRootEmptyRejectsWrite(t *testing.T) {
	// prompt-6 Task 4: clearing workspace root re-enables the empty-root write ban.
	workspace := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "free.txt")

	svc := &FileService{}
	svc.setWorkspaceRoot(workspace)
	svc.setWorkspaceRoot("")
	if err := svc.WriteFile(outsideFile, "data"); err == nil {
		t.Error("WriteFile should fail after clearing workspace root")
	}
}

func TestFileService_RenamePathBothMustBeInside(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	insideFile := filepath.Join(workspace, "inside.txt")
	outsideFile := filepath.Join(outside, "outside.txt")
	os.WriteFile(insideFile, []byte("x"), 0644)

	svc := &FileService{}
	svc.setWorkspaceRoot(workspace)
	// Rename from inside to outside should fail
	if err := svc.RenamePath(insideFile, outsideFile); err == nil {
		t.Error("RenamePath from inside to outside should fail")
	}
}

func TestFileService_ListDirectory_RespectsSandbox(t *testing.T) {
	fs := NewFileService()
	root := t.TempDir()
	fs.setWorkspaceRoot(root)

	os.WriteFile(filepath.Join(root, "inside.txt"), []byte("hello"), 0644)

	// List inside workspace — should work
	entries, err := fs.ListDirectory(root)
	if err != nil {
		t.Fatalf("ListDirectory inside workspace failed: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}

	// List outside workspace — should fail
	outside := t.TempDir()
	_, err = fs.ListDirectory(outside)
	if err == nil {
		t.Fatal("expected error for listing outside workspace")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("error should mention 'outside', got: %v", err)
	}
}

func TestFileService_ListDirectory_NoRootRejects(t *testing.T) {
	fs := NewFileService()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("x"), 0644)
	if _, err := fs.ListDirectory(dir); err == nil {
		t.Fatal("ListDirectory should fail without workspace root")
	}
}

// --- Plan 55: ListAllFiles tests ---

func TestFileService_ListAllFiles_BasicWalk(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0755)
	os.WriteFile(filepath.Join(dir, "sub", "c.ts"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "sub", "deep", "d.py"), []byte("x"), 0644)

	svc := newFileServiceAt(t, dir)
	files, err := svc.ListAllFiles(dir)
	if err != nil {
		t.Fatalf("ListAllFiles failed: %v", err)
	}
	expected := []string{"a.txt", "b.go", "sub/c.ts", "sub/deep/d.py"}
	if len(files) != len(expected) {
		t.Fatalf("expected %d files, got %d: %v", len(expected), len(files), files)
	}
	for i, f := range files {
		if f != expected[i] {
			t.Errorf("file[%d] = %q, want %q", i, f, expected[i])
		}
	}
	// Result should be sorted.
	for i := 1; i < len(files); i++ {
		if files[i-1] > files[i] {
			t.Errorf("result is not sorted: %q > %q", files[i-1], files[i])
		}
	}
}

func TestFileService_ListAllFiles_UsesForwardSlashes(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src", "util"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "util", "helper.go"), []byte("x"), 0644)

	svc := newFileServiceAt(t, dir)
	files, err := svc.ListAllFiles(dir)
	if err != nil {
		t.Fatalf("ListAllFiles failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0] != "src/util/helper.go" {
		t.Errorf("expected forward slashes, got %q", files[0])
	}
}

func TestFileService_ListAllFiles_SkipsHiddenFilesAndDirs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644)

	svc := newFileServiceAt(t, dir)
	files, err := svc.ListAllFiles(dir)
	if err != nil {
		t.Fatalf("ListAllFiles failed: %v", err)
	}
	// .hidden and .git/* should be skipped. .gitignore is also hidden, so skipped.
	if len(files) != 1 {
		t.Fatalf("expected 1 visible file, got %d: %v", len(files), files)
	}
	if files[0] != "visible.txt" {
		t.Errorf("expected visible.txt, got %q", files[0])
	}
}

func TestFileService_ListAllFiles_SkipsIgnoreDirs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "keep.go"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(dir, "dist"), 0755)
	os.WriteFile(filepath.Join(dir, "dist", "bundle.js"), []byte("x"), 0644)

	svc := newFileServiceAt(t, dir)
	files, err := svc.ListAllFiles(dir)
	if err != nil {
		t.Fatalf("ListAllFiles failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file (keep.go), got %d: %v", len(files), files)
	}
	if files[0] != "keep.go" {
		t.Errorf("expected keep.go, got %q", files[0])
	}
}

func TestFileService_ListAllFiles_RespectsGitignore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\nbuild/\n/temp\n"), 0644)
	os.WriteFile(filepath.Join(dir, "keep.go"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "debug.log"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(dir, "build"), 0755)
	os.WriteFile(filepath.Join(dir, "build", "out.js"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(dir, "temp"), 0755)
	os.WriteFile(filepath.Join(dir, "temp", "tmp.txt"), []byte("x"), 0644)

	svc := newFileServiceAt(t, dir)
	files, err := svc.ListAllFiles(dir)
	if err != nil {
		t.Fatalf("ListAllFiles failed: %v", err)
	}
	// .gitignore is hidden so skipped by the hidden rule. *.log, build/,
	// /temp should be skipped by gitignore. Only keep.go remains.
	if len(files) != 1 {
		t.Fatalf("expected 1 file (keep.go), got %d: %v", len(files), files)
	}
	if files[0] != "keep.go" {
		t.Errorf("expected keep.go, got %q", files[0])
	}
}

func TestFileService_ListAllFiles_GitignoreWildcardSegment(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.min.js\n"), 0644)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "vendor.min.js"), []byte("x"), 0644)

	svc := newFileServiceAt(t, dir)
	files, err := svc.ListAllFiles(dir)
	if err != nil {
		t.Fatalf("ListAllFiles failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file (app.js), got %d: %v", len(files), files)
	}
	if files[0] != "app.js" {
		t.Errorf("expected app.js, got %q", files[0])
	}
}

func TestFileService_ListAllFiles_GitignoreNegation(t *testing.T) {
	dir := t.TempDir()
	// *.log is ignored, but important.log is re-included with !.
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n!important.log\n"), 0644)
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "debug.log"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "important.log"), []byte("x"), 0644)

	svc := newFileServiceAt(t, dir)
	files, err := svc.ListAllFiles(dir)
	if err != nil {
		t.Fatalf("ListAllFiles failed: %v", err)
	}
	want := map[string]bool{"app.go": true, "important.log": true}
	if len(files) != len(want) {
		t.Fatalf("expected %d files, got %d: %v", len(want), len(files), files)
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("unexpected file %q", f)
		}
	}
}

func TestFileService_ListAllFiles_GitignoreAnchoredPattern(t *testing.T) {
	dir := t.TempDir()
	// /gen is anchored — only matches a top-level gen dir, not nested ones.
	// ("gen" is NOT in the hardcoded quickOpenIgnoreDirs list, so only the
	// gitignore anchored pattern controls skipping here.)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/gen/\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "gen"), 0755)
	os.WriteFile(filepath.Join(dir, "gen", "out.js"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(dir, "src", "gen"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "gen", "keep.js"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "root.txt"), []byte("x"), 0644)

	svc := newFileServiceAt(t, dir)
	files, err := svc.ListAllFiles(dir)
	if err != nil {
		t.Fatalf("ListAllFiles failed: %v", err)
	}
	want := map[string]bool{"root.txt": true, "src/gen/keep.js": true}
	if len(files) != len(want) {
		t.Fatalf("expected %d files, got %d: %v", len(want), len(files), files)
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("unexpected file %q (anchored /gen/ should only skip top-level gen)", f)
		}
	}
}

func TestFileService_ListAllFiles_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	svc := newFileServiceAt(t, dir)
	files, err := svc.ListAllFiles(dir)
	if err != nil {
		t.Fatalf("ListAllFiles failed: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d: %v", len(files), files)
	}
}

func TestFileService_ListAllFiles_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	os.WriteFile(file, []byte("x"), 0644)
	svc := newFileServiceAt(t, dir)
	if _, err := svc.ListAllFiles(file); err == nil {
		t.Error("expected error when ListAllFiles is called on a file, got nil")
	}
}

func TestFileService_ListAllFiles_RespectsSandbox(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "free.txt"), []byte("x"), 0644)

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	if _, err := svc.ListAllFiles(outside); err == nil {
		t.Error("expected error for ListAllFiles outside workspace")
	}
}

func TestFileService_ListAllFiles_NestedGitignoreNotLoaded(t *testing.T) {
	// Only the root .gitignore is loaded — nested .gitignore files in
	// subdirectories are NOT loaded (documented limitation). This test
	// documents that behavior so it doesn't silently change.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	// Nested .gitignore that would ignore *.go — but it should NOT be applied
	// because we only load the root .gitignore.
	os.WriteFile(filepath.Join(dir, "sub", ".gitignore"), []byte("*.go\n"), 0644)
	os.WriteFile(filepath.Join(dir, "sub", "keep.go"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "sub", "debug.log"), []byte("x"), 0644)

	svc := newFileServiceAt(t, dir)
	files, err := svc.ListAllFiles(dir)
	if err != nil {
		t.Fatalf("ListAllFiles failed: %v", err)
	}
	// *.log is ignored by root .gitignore. *.go in sub/.gitignore is NOT applied.
	want := map[string]bool{"sub/keep.go": true}
	if len(files) != len(want) {
		t.Fatalf("expected %d files, got %d: %v", len(want), len(files), files)
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("unexpected file %q", f)
		}
	}
}

func TestMatchSegment(t *testing.T) {
	cases := []struct {
		seg, pattern string
		want         bool
	}{
		{"foo", "foo", true},
		{"foo", "bar", false},
		{"foo.js", "*.js", true},
		{"foo.ts", "*.js", false},
		{"vendor.min.js", "*.min.js", true},
		{"a.b.c", "a.*.c", true},
		{"a.b.c", "a.*.d", false},
		{"", "*", true},
		{"abc", "abc*", true},
		{"abc", "*abc", true},
		{"xabcx", "*abc*", true},
		{"xabcy", "*abcd*", false},
		{"foo", "foo*", true},
		{"foobar", "foo*", true},
		{"barfoo", "*foo", true},
	}
	for _, c := range cases {
		got := matchSegment(c.seg, c.pattern)
		if got != c.want {
			t.Errorf("matchSegment(%q, %q) = %v, want %v", c.seg, c.pattern, got, c.want)
		}
	}
}

func TestLoadGitignorePatterns_EmptyAndComments(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# comment\n\n   \n*.log\n"), 0644)
	f := NewFileService()
	patterns := f.loadGitignorePatterns(dir)
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}
	if patterns[0].segments[0] != "*.log" {
		t.Errorf("expected *.log, got %q", patterns[0].segments[0])
	}
}

func TestLoadGitignorePatterns_NoFile(t *testing.T) {
	dir := t.TempDir()
	f := NewFileService()
	patterns := f.loadGitignorePatterns(dir)
	if patterns != nil {
		t.Errorf("expected nil when .gitignore is absent, got %v", patterns)
	}
}

// TestFileService_M5_GitignoreCache (M-5) verifies that
// loadGitignorePatterns caches parsed patterns keyed by (dir, mtime):
//   - when the .gitignore mtime is unchanged, the cached result is
//     served even if the file's CONTENT has changed on disk (proving
//     the cache is consulted rather than re-reading the file);
//   - when the mtime changes, the cache is invalidated and the new
//     content is read and cached.
//
// We pin mtimes explicitly via os.Chtimes to avoid filesystem mtime
// resolution flakiness.
func TestFileService_M5_GitignoreCache(t *testing.T) {
	dir := t.TempDir()
	giPath := filepath.Join(dir, ".gitignore")
	// N-5: 缓存现在是实例字段，用同一 FileService 实例验证缓存命中/失效
	f := NewFileService()

	writeAt := func(content string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(giPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		if err := os.Chtimes(giPath, mtime, mtime); err != nil {
			t.Fatalf("os.Chtimes: %v", err)
		}
	}

	firstSeg := func(ps []gitignorePattern) string {
		t.Helper()
		if len(ps) == 0 {
			t.Fatalf("expected at least 1 pattern, got 0")
		}
		if len(ps[0].segments) == 0 {
			t.Fatalf("expected at least 1 segment, got 0")
		}
		return ps[0].segments[0]
	}

	t1 := time.Unix(1700000000, 0)
	t2 := t1.Add(2 * time.Second)

	// 1) Initial load ("foo" at t1) — populates the cache.
	writeAt("foo\n", t1)
	p1 := f.loadGitignorePatterns(dir)
	if got := firstSeg(p1); got != "foo" {
		t.Fatalf("initial load: expected foo, got %q", got)
	}

	// 2) Overwrite content with "bar" but keep mtime at t1. Because the
	//    cache is keyed by (dir, mtime) and mtime is unchanged, the
	//    cache MUST serve the stale "foo" result instead of re-reading
	//    the file. This can only happen if the cache is in use.
	writeAt("bar\n", t1)
	p2 := f.loadGitignorePatterns(dir)
	if got := firstSeg(p2); got != "foo" {
		t.Errorf("cache hit expected stale foo (mtime unchanged), got %q — cache not consulted?", got)
	}

	// 3) Bump mtime to t2 — cache must invalidate and re-read "bar".
	writeAt("bar\n", t2)
	p3 := f.loadGitignorePatterns(dir)
	if got := firstSeg(p3); got != "bar" {
		t.Errorf("cache invalidation expected bar (mtime changed), got %q", got)
	}

	// 4) Unchanged file + same mtime -> still "bar" (served from cache).
	p4 := f.loadGitignorePatterns(dir)
	if got := firstSeg(p4); got != "bar" {
		t.Errorf("second cached read expected bar, got %q", got)
	}
}

// TestGitignoreCacheBound (N-5) 验证 gitignoreCache 是实例字段：
// 不同 FileService 实例拥有独立缓存，切换 200 个项目目录后每个实例
// 只缓存自己访问的目录，实例释放后缓存不残留到其他实例。
func TestGitignoreCacheBound(t *testing.T) {
	// 实例 A 访问 200 个不同项目目录
	fA := NewFileService()
	for i := 0; i < 200; i++ {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		fA.loadGitignorePatterns(dir)
	}
	fA.gitignoreMu.Lock()
	aCount := len(fA.gitignoreCache)
	fA.gitignoreMu.Unlock()
	if aCount != 200 {
		t.Errorf("实例 A 缓存条目 = %d, 期望 200（每个访问的目录一条）", aCount)
	}

	// 实例 B 只访问 1 个目录 — 缓存应独立于 A
	fB := NewFileService()
	dirB := t.TempDir()
	os.WriteFile(filepath.Join(dirB, ".gitignore"), []byte("*.txt\n"), 0o644)
	fB.loadGitignorePatterns(dirB)
	fB.gitignoreMu.Lock()
	bCount := len(fB.gitignoreCache)
	fB.gitignoreMu.Unlock()
	if bCount != 1 {
		t.Errorf("实例 B 缓存条目 = %d, 期望 1（独立于实例 A 的 200 条）", bCount)
	}

	// 验证 A 的缓存不受 B 的操作影响
	fA.gitignoreMu.Lock()
	aCountAfter := len(fA.gitignoreCache)
	fA.gitignoreMu.Unlock()
	if aCountAfter != 200 {
		t.Errorf("实例 A 缓存条目在 B 操作后 = %d, 期望 200（实例隔离）", aCountAfter)
	}

	// 模拟项目切换：释放实例 A，验证 B 的缓存不受影响
	fA = nil
	runtime.GC()
	fB.gitignoreMu.Lock()
	bCountFinal := len(fB.gitignoreCache)
	fB.gitignoreMu.Unlock()
	if bCountFinal != 1 {
		t.Errorf("释放 A 后实例 B 缓存条目 = %d, 期望 1", bCountFinal)
	}
}

// --- N-56: Symlink path traversal tests ---
//
// On Windows, creating symlinks requires either administrator privileges
// or Developer Mode enabled. We attempt to create one and skip the test
// if the OS refuses — this keeps the suite portable.

// trySymlinkOrFail skips the test if the OS refuses to create a symlink
// at linkPath pointing at target. Returns true if the test should
// continue.
//
// N-56 note: on Windows, os.Symlink may return nil even when the user
// lacks the SeCreateSymbolicLinkPrivilege (or Developer Mode) — the
// symlink is silently NOT created. We therefore verify with Lstat
// after creation and skip the test if the link doesn't actually exist.
func trySymlinkOrFail(t *testing.T, linkPath, target string) bool {
	t.Helper()
	if err := os.Symlink(target, linkPath); err != nil {
		t.Skipf("symlink creation failed (likely missing privileges on Windows): %v", err)
		return false
	}
	if _, err := os.Lstat(linkPath); err != nil {
		t.Skipf("symlink was not actually created (likely missing privileges on Windows): %v", err)
		return false
	}
	return true
}

func TestFileService_N56_RejectsSymlinkEscapingWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	// outside/secret.txt
	outsideFile := filepath.Join(outside, "secret.txt")
	os.WriteFile(outsideFile, []byte("top-secret"), 0644)
	// workspace/link -> outside/secret.txt
	linkPath := filepath.Join(workspace, "link")
	if !trySymlinkOrFail(t, linkPath, outsideFile) {
		return
	}

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	// ReadFile via the symlink should be rejected — the symlink resolves
	// to a path outside the workspace.
	if _, err := svc.ReadFile(linkPath); err == nil {
		t.Error("ReadFile via symlink escaping workspace should fail (N-56)")
	}
	// WriteFile via the symlink should also be rejected.
	if err := svc.WriteFile(linkPath, "tampered"); err == nil {
		t.Error("WriteFile via symlink escaping workspace should fail (N-56)")
	}
}

func TestFileService_N56_AllowsSymlinkInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	// workspace/real.txt
	realFile := filepath.Join(workspace, "real.txt")
	os.WriteFile(realFile, []byte("ok"), 0644)
	// workspace/link -> workspace/real.txt
	linkPath := filepath.Join(workspace, "link")
	if !trySymlinkOrFail(t, linkPath, realFile) {
		return
	}

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	// Reading through a symlink that resolves INSIDE the workspace is fine.
	data, err := svc.ReadFile(linkPath)
	if err != nil {
		t.Fatalf("ReadFile via symlink inside workspace should succeed: %v", err)
	}
	if data != "ok" {
		t.Errorf("expected 'ok', got %q", data)
	}
}

func TestFileService_N56_RejectsSymlinkDirEscapingWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	// outside/secret.txt
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0644)
	// workspace/links -> outside  (symlink to a directory)
	linkDir := filepath.Join(workspace, "links")
	if !trySymlinkOrFail(t, linkDir, outside) {
		return
	}

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	// Listing through the symlinked directory should be rejected.
	if _, err := svc.ListDirectory(linkDir); err == nil {
		t.Error("ListDirectory via symlinked dir escaping workspace should fail (N-56)")
	}
}

func TestFileService_N56_RejectsTraversalThroughSymlinkedSubdir(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	// outside/secret.txt
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0644)
	// workspace/sub/escape -> outside  (symlink to a directory inside subdir)
	os.MkdirAll(filepath.Join(workspace, "sub"), 0755)
	escapeLink := filepath.Join(workspace, "sub", "escape")
	if !trySymlinkOrFail(t, escapeLink, outside) {
		return
	}

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	// Accessing through the symlinked subdir should be rejected.
	target := filepath.Join(workspace, "sub", "escape", "secret.txt")
	if _, err := svc.ReadFile(target); err == nil {
		t.Error("ReadFile via symlinked subdir escaping workspace should fail (N-56)")
	}
}

func TestFileService_N56_CreateFileThroughSymlinkedParentRejected(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	// workspace/links -> outside  (symlink to outside dir)
	linkDir := filepath.Join(workspace, "links")
	if !trySymlinkOrFail(t, linkDir, outside) {
		return
	}

	svc := &FileService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	// Creating a file through the symlinked parent dir would write to
	// outside/evil.txt — must be rejected even though the file doesn't
	// exist yet (evalSymlinksAllowMissing resolves the parent).
	target := filepath.Join(linkDir, "evil.txt")
	if err := svc.CreateFile(target); err == nil {
		t.Error("CreateFile through symlinked parent escaping workspace should fail (N-56)")
	}
}

func TestFileService_RejectsCreateDirectoryBeyondSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	linkDir := filepath.Join(workspace, "links")
	if !trySymlinkOrFail(t, linkDir, outside) {
		return
	}

	svc := newFileServiceAt(t, workspace)
	target := filepath.Join(linkDir, "missing", "nested")
	if err := svc.CreateDirectory(target); err == nil {
		t.Fatal("CreateDirectory beyond an escaping symlink should fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "missing")); !os.IsNotExist(err) {
		t.Fatalf("escaping CreateDirectory touched outside workspace: %v", err)
	}
}

// --- N-56: Plugin service symlink tests ---

func TestPluginService_N56_RejectsSymlinkEscapingPluginDir(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping plugin symlink test in short mode")
	}
	tmp := t.TempDir()
	// Plugins are discovered at <projectRoot>/.koyori-ide/plugins/<name>/plugin.json
	projectDir := filepath.Join(tmp, ".koyori-ide", "plugins", "myplugin")
	os.MkdirAll(projectDir, 0755)
	// Write a minimal plugin.json manifest.
	manifest := []byte(`{"name":"myplugin","version":"1.0.0","main":"main.js"}`)
	os.WriteFile(filepath.Join(projectDir, "plugin.json"), manifest, 0644)

	// outside/secret.js
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.js")
	os.WriteFile(outsideFile, []byte("evil"), 0644)

	// projectDir/link.js -> outside/secret.js
	linkPath := filepath.Join(projectDir, "link.js")
	if !trySymlinkOrFail(t, linkPath, outsideFile) {
		return
	}

	svc := &PluginService{}
	_, err := svc.ReadPluginFile("myplugin", "link.js", tmp)
	if err == nil {
		t.Error("ReadPluginFile via symlink escaping plugin dir should fail (N-56)")
	}
}

func TestPluginService_N56_AllowsSymlinkInsidePluginDir(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping plugin symlink test in short mode")
	}
	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, ".koyori-ide", "plugins", "myplugin")
	os.MkdirAll(projectDir, 0755)
	manifest := []byte(`{"name":"myplugin","version":"1.0.0","main":"main.js"}`)
	os.WriteFile(filepath.Join(projectDir, "plugin.json"), manifest, 0644)

	// projectDir/real.js (real file inside the plugin dir)
	realFile := filepath.Join(projectDir, "real.js")
	os.WriteFile(realFile, []byte("ok"), 0644)
	// projectDir/link.js -> projectDir/real.js (symlink inside plugin dir)
	linkPath := filepath.Join(projectDir, "link.js")
	if !trySymlinkOrFail(t, linkPath, realFile) {
		return
	}

	svc := &PluginService{}
	data, err := svc.ReadPluginFile("myplugin", "link.js", tmp)
	if err != nil {
		t.Fatalf("ReadPluginFile via symlink inside plugin dir should succeed: %v", err)
	}
	if string(data) != "ok" {
		t.Errorf("expected 'ok', got %q", string(data))
	}
}

// --- N-56: evalSymlinksAllowMissing unit tests (no symlink privileges required) ---

func TestEvalSymlinksAllowMissing_ExistingPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "exists.txt")
	os.WriteFile(file, []byte("x"), 0644)
	// For an existing path, the helper should behave like EvalSymlinks.
	got, err := evalSymlinksAllowMissing(file)
	if err != nil {
		t.Fatalf("evalSymlinksAllowMissing failed: %v", err)
	}
	expected, _ := filepath.EvalSymlinks(file)
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestEvalSymlinksAllowMissing_NonExistentFileWithExistentParent(t *testing.T) {
	dir := t.TempDir()
	// Non-existent file under an existing parent directory.
	file := filepath.Join(dir, "newfile.txt")
	got, err := evalSymlinksAllowMissing(file)
	if err != nil {
		t.Fatalf("evalSymlinksAllowMissing failed: %v", err)
	}
	// Should resolve the parent and rejoin with the basename.
	expectedParent, _ := filepath.EvalSymlinks(dir)
	expected := filepath.Join(expectedParent, "newfile.txt")
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestEvalSymlinksAllowMissing_NonExistentParent(t *testing.T) {
	dir := canonicalTestPath(t, t.TempDir())
	// Both the file and its parent don't exist.
	file := filepath.Join(dir, "missing-subdir", "newfile.txt")
	got, err := evalSymlinksAllowMissing(file)
	if err != nil {
		t.Fatalf("evalSymlinksAllowMissing failed: %v", err)
	}
	// Should fall back to lexical resolution (parent missing → no
	// symlinks to follow).
	expected := filepath.Join(dir, "missing-subdir", "newfile.txt")
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestFileService_N56_RejectsTraversalPath_Lexical(t *testing.T) {
	// This test does NOT require symlink privileges — it verifies the
	// existing lexical traversal check still works (defense in depth).
	workspace := t.TempDir()
	os.Mkdir(filepath.Join(workspace, "subdir"), 0755)
	traversalPath := filepath.Join(workspace, "subdir", "..", "..", "outside.txt")

	svc := newFileServiceAt(t, workspace)
	if _, err := svc.ReadFile(traversalPath); err == nil {
		t.Error("ReadFile with lexical traversal should fail")
	}
	if err := svc.WriteFile(traversalPath, "data"); err == nil {
		t.Error("WriteFile with lexical traversal should fail")
	}
}

// ============================================================================
// Priority 4 (prompt-1.md): 多根工作区 Workspace Folders 测试
// ============================================================================

// TestFileService_P4_MultiRootValidation 验证多根校验：文件在 rootA 下合法、
// 在 rootB 下合法、在两者之外被拒绝。同时验证单根退化路径行为不变。
func TestFileService_P4_MultiRootValidation(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	outside := t.TempDir()

	// rootA/a.txt
	fileA := filepath.Join(rootA, "a.txt")
	if err := os.WriteFile(fileA, []byte("A"), 0644); err != nil {
		t.Fatalf("write fileA: %v", err)
	}
	// rootB/b.txt
	fileB := filepath.Join(rootB, "b.txt")
	if err := os.WriteFile(fileB, []byte("B"), 0644); err != nil {
		t.Fatalf("write fileB: %v", err)
	}
	// outside/secret.txt
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0644); err != nil {
		t.Fatalf("write outsideFile: %v", err)
	}

	svc := &FileService{}
	absA, _ := filepath.Abs(rootA)
	absB, _ := filepath.Abs(rootB)
	if err := svc.setWorkspaceRoots([]string{absA, absB}); err != nil {
		t.Fatalf("SetWorkspaceRoots failed: %v", err)
	}

	// 验证 WorkspaceRoots() 返回两个根。
	roots := svc.WorkspaceRoots()
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots, got %d (%v)", len(roots), roots)
	}

	// 读 rootA 下文件应通过。
	if _, err := svc.ReadFile(fileA); err != nil {
		t.Errorf("ReadFile under rootA should succeed, got: %v", err)
	}
	// 读 rootB 下文件应通过。
	if _, err := svc.ReadFile(fileB); err != nil {
		t.Errorf("ReadFile under rootB should succeed, got: %v", err)
	}
	// 读 rootA/rootB 之外的文件应被拒绝。
	if _, err := svc.ReadFile(outsideFile); err == nil {
		t.Error("ReadFile outside all roots should fail")
	}

	// 写 rootA 下应通过。
	if err := svc.WriteFile(filepath.Join(rootA, "new.txt"), "data"); err != nil {
		t.Errorf("WriteFile under rootA should succeed, got: %v", err)
	}
	// 写 rootB 下应通过。
	if err := svc.WriteFile(filepath.Join(rootB, "new.txt"), "data"); err != nil {
		t.Errorf("WriteFile under rootB should succeed, got: %v", err)
	}
	// 写 outside 下应被拒绝。
	if err := svc.WriteFile(outsideFile, "tampered"); err == nil {
		t.Error("WriteFile outside all roots should fail")
	}
}

// TestFileService_P4_SingleRootDegradation 验证多根接口在传入单个根时
// 退化为单根行为，且 rootDir 字段同步更新。
func TestFileService_P4_SingleRootDegradation(t *testing.T) {
	root := canonicalTestPath(t, t.TempDir())
	svc := &FileService{}
	if err := svc.setWorkspaceRoots([]string{root}); err != nil {
		t.Fatalf("SetWorkspaceRoots(single) failed: %v", err)
	}
	// 单根退化：rootDirs 应为空（仅 rootDir 被设），与 SetWorkspaceRoot 等价。
	svc.mu.Lock()
	rootDirsCopy := append([]string(nil), svc.rootDirs...)
	rootDir := svc.rootDir
	svc.mu.Unlock()
	if len(rootDirsCopy) > 0 {
		t.Errorf("expected rootDirs empty for single-root degradation, got %v", rootDirsCopy)
	}
	absRoot, _ := filepath.Abs(root)
	if rootDir != absRoot {
		t.Errorf("rootDir = %q, want %q", rootDir, absRoot)
	}
	// WorkspaceRoots 应仍返回单元素列表。
	roots := svc.WorkspaceRoots()
	if len(roots) != 1 || roots[0] != absRoot {
		t.Errorf("WorkspaceRoots = %v, want [%q]", roots, absRoot)
	}
}

// TestFileService_P4_MultiRootRejectsTraversal 验证多根模式下跨根目录
// 遍历攻击（如 rootA/../outside）仍被拒绝。
func TestFileService_P4_MultiRootRejectsTraversal(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	svc := &FileService{}
	absA, _ := filepath.Abs(rootA)
	absB, _ := filepath.Abs(rootB)
	if err := svc.setWorkspaceRoots([]string{absA, absB}); err != nil {
		t.Fatalf("SetWorkspaceRoots failed: %v", err)
	}
	// rootA/../<sibling> 应被拒绝（sibling 不在任一根下）。
	traversal := filepath.Join(rootA, "..", filepath.Base(t.TempDir())+"-nonexistent")
	// 这里我们用 rootA/../<某不存在路径>，由于兄弟目录不存在，
	// evalSymlinksAllowMissing 会解析父目录；rootA 父目录本身在 rootA 之外，
	// 所以校验应失败。
	if _, err := svc.ReadFile(traversal); err == nil {
		t.Error("ReadFile via traversal escaping all roots should fail")
	}
}
