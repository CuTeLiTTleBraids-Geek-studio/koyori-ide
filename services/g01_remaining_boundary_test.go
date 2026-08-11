package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateMutatingPathWithinRoot_EmptyRootFailsClosed(t *testing.T) {
	target := filepath.Join(t.TempDir(), "outside.txt")
	_, err := ValidateMutatingPathWithinRoot("", target)
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("empty-root mutating validation = %v, want ErrNotAllowed", err)
	}
}

func TestProjectService_RemoveProjectClearsCoordinatedRoots(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "projects.json")
	fileService := NewFileService()
	terminalService := NewTerminalService()
	agentService := NewAgentService()
	coverageService := NewCoverageService()
	eslintService := NewEslintService()
	gitService := &GitService{}
	searchService := &SearchService{}
	toolchainService := NewToolchainService()
	context := NewWorkspaceContext()
	t.Cleanup(terminalService.Shutdown)
	t.Cleanup(func() { _ = agentService.Close() })

	projectService := NewProjectService(fileService, terminalService, agentService, nil)
	projectService.configPath = configPath
	projectService.setWorkspaceContext(context)
	projectService.setGitService(gitService)
	projectService.setSearchService(searchService)
	projectService.setToolchainService(toolchainService)
	projectService.setCoverageService(coverageService)
	projectService.setEslintService(eslintService)

	project, err := projectService.AddProject(root)
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("workspace fixture disappeared: %v", err)
	}
	if err := projectService.RemoveProject(project.ID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}

	if _, err := context.RequireRoot(); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("workspace context after removal = %v, want ErrNotAllowed", err)
	}
	if roots := fileService.WorkspaceRoots(); len(roots) != 0 {
		t.Fatalf("file roots after removal = %v, want empty", roots)
	}
	if got := gitService.workspaceRootPath(); got != "" {
		t.Fatalf("git root after removal = %q, want empty", got)
	}
	if searchService.workspaceRoot != "" {
		t.Fatalf("search root after removal = %q, want empty", searchService.workspaceRoot)
	}
	if toolchainService.workspaceRoot != "" {
		t.Fatalf("toolchain root after removal = %q, want empty", toolchainService.workspaceRoot)
	}
	if coverageService.workspaceRoot != "" {
		t.Fatalf("coverage root after removal = %q, want empty", coverageService.workspaceRoot)
	}
	if eslintService.workspaceRoot != "" {
		t.Fatalf("eslint root after removal = %q, want empty", eslintService.workspaceRoot)
	}
	if err := fileService.WriteFile(filepath.Join(root, "must-not-write.txt"), "blocked"); err == nil {
		t.Fatal("file write succeeded after removing the active project")
	}
}

func TestLSPService_ClearingWorkspaceDoesNotRestartManagedServers(t *testing.T) {
	cmd, process := startLSPTestProcess(t, "block")
	service := NewLSPService(t.TempDir())
	installLSPTestProcess(service, "go", cmd, process)

	service.setWorkspaceRoot("")

	select {
	case <-process.done:
	default:
		t.Fatal("clearing the workspace did not stop the managed LSP process")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.servers) != 0 {
		t.Fatalf("clearing the workspace restarted LSP servers: %v", service.servers)
	}
}
