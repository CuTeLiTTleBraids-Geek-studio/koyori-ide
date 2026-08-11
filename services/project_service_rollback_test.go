package services

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAddProject_RollbackOnFailure verifies that when AddProject fails
// (e.g. because the project list cannot be loaded/saved), all workspace
// roots that were already applied are rolled back to their previous
// values — preventing partial state across services (M-8).
func TestAddProject_RollbackOnFailure(t *testing.T) {
	initialDir := t.TempDir()
	newDir := t.TempDir()

	// Make configPath a directory so load() fails (ReadFile on a
	// directory returns a non-IsNotExist error), triggering rollback.
	configBase := t.TempDir()
	configPath := filepath.Join(configBase, "projects.json")
	if err := os.MkdirAll(configPath, 0755); err != nil {
		t.Fatalf("mkdir configPath: %v", err)
	}

	fs := &FileService{}
	if err := fs.setWorkspaceRoot(initialDir); err != nil {
		t.Fatalf("set initial root: %v", err)
	}

	svc := &ProjectService{
		configPath:  configPath,
		fileService: fs,
	}

	_, err := svc.AddProject(newDir)
	if err == nil {
		t.Fatal("expected AddProject to fail when configPath is a directory")
	}

	// Verify rollback: fileService root should be restored to initialDir.
	fs.mu.Lock()
	currentRoot := fs.rootDir
	fs.mu.Unlock()

	// Both paths are resolved to absolute by SetWorkspaceRoot, so
	// compare directly.
	initialAbs, _ := filepath.Abs(initialDir)
	if currentRoot != initialAbs {
		t.Errorf("expected root rolled back to %q, got %q", initialAbs, currentRoot)
	}
}

// TestAddProject_RollbackMultipleServices verifies that rollback
// restores ALL services when AddProject fails, not just the first one.
func TestAddProject_RollbackMultipleServices(t *testing.T) {
	initialDir := t.TempDir()
	newDir := t.TempDir()

	// Make configPath a directory so load() fails.
	configBase := t.TempDir()
	configPath := filepath.Join(configBase, "projects.json")
	if err := os.MkdirAll(configPath, 0755); err != nil {
		t.Fatalf("mkdir configPath: %v", err)
	}

	fs := &FileService{}
	ts := &TerminalService{}
	gs := &GitService{}

	if err := fs.setWorkspaceRoot(initialDir); err != nil {
		t.Fatalf("set fs initial root: %v", err)
	}
	if err := ts.setWorkspaceRoot(initialDir); err != nil {
		t.Fatalf("set ts initial root: %v", err)
	}
	if err := gs.setWorkspaceRoot(initialDir); err != nil {
		t.Fatalf("set gs initial root: %v", err)
	}

	svc := &ProjectService{
		configPath:      configPath,
		fileService:     fs,
		terminalService: ts,
		gitService:      gs,
	}

	_, err := svc.AddProject(newDir)
	if err == nil {
		t.Fatal("expected AddProject to fail")
	}

	initialAbs, _ := filepath.Abs(initialDir)

	// All services should be rolled back to the initial root.
	fs.mu.Lock()
	fsRoot := fs.rootDir
	fs.mu.Unlock()
	if fsRoot != initialAbs {
		t.Errorf("FileService root: expected %q, got %q", initialAbs, fsRoot)
	}

	ts.mu.Lock()
	tsRoot := ts.rootDir
	ts.mu.Unlock()
	if tsRoot != initialAbs {
		t.Errorf("TerminalService root: expected %q, got %q", initialAbs, tsRoot)
	}

	gs.mu.RLock()
	gsRoot := gs.workspaceRoot
	gs.mu.RUnlock()
	if gsRoot != initialAbs {
		t.Errorf("GitService root: expected %q, got %q", initialAbs, gsRoot)
	}
}

// TestAddProject_NoPartialStateOnSetWorkspaceRootFailure verifies that
// when the first SetWorkspaceRoot fails (invalid path), no project is
// added and no service is left in a partial state.
func TestAddProject_NoPartialStateOnSetWorkspaceRootFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "projects.json")
	fs := &FileService{}
	ts := &TerminalService{}

	svc := &ProjectService{
		configPath:      configPath,
		fileService:     fs,
		terminalService: ts,
	}

	// Non-existent path → fileService.SetWorkspaceRoot fails immediately.
	_, err := svc.AddProject("/nonexistent/path/that/does/not/exist/xyz")
	if err == nil {
		t.Fatal("expected AddProject to fail for non-existent path")
	}

	// No project should have been added.
	projects, err := svc.GetRecentProjects()
	if err != nil {
		t.Fatalf("GetRecentProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(projects))
	}

	// TerminalService root should still be empty (not modified).
	ts.mu.Lock()
	tsRoot := ts.rootDir
	ts.mu.Unlock()
	if tsRoot != "" {
		t.Errorf("TerminalService root should be empty, got %q", tsRoot)
	}
}

// TestAddProject_RollbackRestoresNonErrorServices verifies that
// non-error services (LSPService, SymbolIndexService) are also rolled
// back when AddProject fails (M-8).
func TestAddProject_RollbackRestoresNonErrorServices(t *testing.T) {
	initialDir := t.TempDir()
	newDir := t.TempDir()

	configBase := t.TempDir()
	configPath := filepath.Join(configBase, "projects.json")
	if err := os.MkdirAll(configPath, 0755); err != nil {
		t.Fatalf("mkdir configPath: %v", err)
	}

	fs := &FileService{}
	lsp := &LSPService{servers: map[string]*lspServer{}}

	if err := fs.setWorkspaceRoot(initialDir); err != nil {
		t.Fatalf("set initial root: %v", err)
	}
	lsp.setWorkspaceRoot(initialDir)

	svc := &ProjectService{
		configPath:  configPath,
		fileService: fs,
		lspService:  lsp,
	}

	_, err := svc.AddProject(newDir)
	if err == nil {
		t.Fatal("expected AddProject to fail")
	}

	initialAbs, _ := filepath.Abs(initialDir)

	// LSP root should be rolled back.
	lsp.mu.Lock()
	lspRoot := lsp.workspaceRoot
	lsp.mu.Unlock()
	if lspRoot != initialAbs {
		t.Errorf("LSPService root: expected %q, got %q", initialAbs, lspRoot)
	}
}

// TestProjectService_M8_AddProjectRollbackOnFailure 验证 M-8 回滚模式:
// (1) 当首个 SetWorkspaceRoot 校验失败时,后续服务不被修改;
// (2) 当 SetWorkspaceRoot 已应用但 load/save 失败时,所有已应用的服务
// 都被回滚到先前的根目录。
func TestProjectService_M8_AddProjectRollbackOnFailure(t *testing.T) {
	// 子测试 1: 校验失败阻止任何变更 — 第一个服务 SetWorkspaceRoot
	// 失败(路径不存在),确认后续服务未被修改。
	t.Run("validation_failure_prevents_mutation", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "projects.json")
		fs := &FileService{}
		ts := &TerminalService{}

		svc := &ProjectService{
			configPath:      configPath,
			fileService:     fs,
			terminalService: ts,
		}

		// 不存在的路径 → fileService.SetWorkspaceRoot 立即失败。
		_, err := svc.AddProject("/nonexistent/path/that/does/not/exist/xyz")
		if err == nil {
			t.Fatal("expected AddProject to fail for non-existent path")
		}

		// TerminalService 根应仍为空(未被修改)。
		ts.mu.Lock()
		tsRoot := ts.rootDir
		ts.mu.Unlock()
		if tsRoot != "" {
			t.Errorf("TerminalService root should be empty, got %q", tsRoot)
		}
	})

	// 子测试 2: 已应用的服务被回滚 — 所有 SetWorkspaceRoot 成功后
	// load() 失败(configPath 是目录),确认所有服务回滚到初始根。
	t.Run("rollback_restores_applied_services", func(t *testing.T) {
		initialDir := t.TempDir()
		newDir := t.TempDir()

		// 让 configPath 成为目录,使 load() 失败,触发回滚。
		configBase := t.TempDir()
		configPath := filepath.Join(configBase, "projects.json")
		if err := os.MkdirAll(configPath, 0755); err != nil {
			t.Fatalf("mkdir configPath: %v", err)
		}

		fs := &FileService{}
		ts := &TerminalService{}
		gs := &GitService{}

		if err := fs.setWorkspaceRoot(initialDir); err != nil {
			t.Fatalf("set fs initial root: %v", err)
		}
		if err := ts.setWorkspaceRoot(initialDir); err != nil {
			t.Fatalf("set ts initial root: %v", err)
		}
		if err := gs.setWorkspaceRoot(initialDir); err != nil {
			t.Fatalf("set gs initial root: %v", err)
		}

		svc := &ProjectService{
			configPath:      configPath,
			fileService:     fs,
			terminalService: ts,
			gitService:      gs,
		}

		_, err := svc.AddProject(newDir)
		if err == nil {
			t.Fatal("expected AddProject to fail when configPath is a directory")
		}

		initialAbs, _ := filepath.Abs(initialDir)

		// 所有服务都应回滚到初始根。
		fs.mu.Lock()
		fsRoot := fs.rootDir
		fs.mu.Unlock()
		if fsRoot != initialAbs {
			t.Errorf("FileService root: expected %q, got %q", initialAbs, fsRoot)
		}

		ts.mu.Lock()
		tsRoot := ts.rootDir
		ts.mu.Unlock()
		if tsRoot != initialAbs {
			t.Errorf("TerminalService root: expected %q, got %q", initialAbs, tsRoot)
		}

		gs.mu.RLock()
		gsRoot := gs.workspaceRoot
		gs.mu.RUnlock()
		if gsRoot != initialAbs {
			t.Errorf("GitService root: expected %q, got %q", initialAbs, gsRoot)
		}
	})
}
