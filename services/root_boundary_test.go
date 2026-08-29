package services

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootSetters_are_unavailable_to_Wails(t *testing.T) {
	// Given
	files, err := parser.ParseDir(token.NewFileSet(), ".", func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse services package: %v", err)
	}
	forbiddenExports := map[string]bool{
		"SetProjectRoot":    true,
		"SetWorkspaceRoot":  true,
		"SetWorkspaceRoots": true,
	}
	foundInternal := 0

	// When
	for _, file := range files["services"].Files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil {
				continue
			}
			if forbiddenExports[function.Name.Name] {
				t.Errorf("trusted root setter %s remains exported to Wails reflection", function.Name.Name)
			}
			if function.Name.Name == "setProjectRoot" ||
				function.Name.Name == "setWorkspaceRoot" ||
				function.Name.Name == "setWorkspaceRoots" ||
				function.Name.Name == "configureWorkspaceRoot" {
				foundInternal++
			}
		}
	}
	if foundInternal == 0 {
		t.Fatal("no trusted root setters found")
	}
}

func TestFileService_mutations_fail_when_workspace_root_is_empty(t *testing.T) {
	// Given
	outside := t.TempDir()
	source := filepath.Join(outside, "source.txt")
	if err := os.WriteFile(source, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write source fixture: %v", err)
	}
	service := NewFileService()
	tests := []struct {
		name   string
		mutate func() error
	}{
		{name: "write file", mutate: func() error { return service.WriteFile(source, "overwrite") }},
		{name: "create file", mutate: func() error { return service.CreateFile(filepath.Join(outside, "created.txt")) }},
		{name: "create directory", mutate: func() error { return service.CreateDirectory(filepath.Join(outside, "created")) }},
		{name: "delete path", mutate: func() error { return service.DeletePath(source) }},
		{name: "rename path", mutate: func() error { return service.RenamePath(source, filepath.Join(outside, "renamed.txt")) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			err := test.mutate()

			// Then
			if err == nil {
				t.Fatal("mutation succeeded without an active workspace")
			}
		})
	}
}

func TestSearchService_replace_fails_when_workspace_root_is_empty(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatalf("write search fixture: %v", err)
	}
	service := NewSearchService()
	if err := service.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatalf("set initial search root: %v", err)
	}
	if err := service.setWorkspaceRoot(""); err != nil {
		t.Fatalf("clear search root: %v", err)
	}

	// When
	_, err := service.Replace(path, "before", "after", true)

	// Then
	if err == nil {
		t.Fatal("replace succeeded without an active workspace")
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read search fixture: %v", readErr)
	}
	if string(content) != "before" {
		t.Fatalf("replace changed file without a workspace: %q", content)
	}
}

func TestGitService_init_fails_when_workspace_root_is_empty(t *testing.T) {
	// Given
	repository := t.TempDir()
	service := NewGitService()

	// When
	err := service.InitRepo(repository)

	// Then
	if err == nil {
		t.Fatal("git mutation succeeded without an active workspace")
	}
}

func TestAgentService_approval_fails_when_workspace_root_is_empty(t *testing.T) {
	// Given
	service := NewAgentService()
	t.Cleanup(func() { _ = service.Close() })

	// When
	_, err := service.requestCommandApprovalLegacy("go version", t.TempDir())

	// Then
	if err == nil {
		t.Fatal("agent command approval succeeded without an active workspace")
	}
}

func TestTerminalService_start_fails_when_workspace_root_is_empty(t *testing.T) {
	// Given
	service := NewTerminalService()
	t.Cleanup(service.Shutdown)

	// When
	err := service.StartSession("empty-root", t.TempDir(), "bash")

	// Then
	if err == nil {
		t.Fatal("terminal session started without an active workspace")
	}
}

func TestCoverageService_run_fails_when_workspace_root_is_empty(t *testing.T) {
	// Given
	service := NewCoverageService()

	// When
	_, err := service.RunPackageCoverage(t.TempDir())

	// Then
	if err == nil {
		t.Fatal("coverage run accepted a renderer-supplied directory without an active workspace")
	}
}

func TestDebugService_launch_fails_when_workspace_root_is_empty(t *testing.T) {
	// Given
	service := NewDebugServiceWithWorkspaceContext(NewWorkspaceContext())

	// When
	_, err := service.LaunchPackage(t.TempDir())

	// Then
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "workspace") {
		t.Fatalf("debug launch error = %v, want an empty-workspace rejection", err)
	}
}
