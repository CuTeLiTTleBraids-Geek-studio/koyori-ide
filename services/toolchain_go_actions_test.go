package services

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGoAdvancedCommandsUseStructuredArgv(t *testing.T) {
	tests := []struct {
		id   string
		args []string
	}{
		{id: "go-test-race", args: []string{"test", "-race", "./..."}},
		{id: "go-bench", args: []string{"test", "-run=^$", "-bench=.", "-benchmem", "./..."}},
		{id: "go-generate", args: []string{"generate", "./..."}},
		{id: "go-work-sync", args: []string{"work", "sync"}},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			cmd, ok := toolchainCommandByID(tt.id)
			if !ok {
				t.Fatalf("command %q is missing from the toolchain catalog", tt.id)
			}
			if cmd.Command != "go" {
				t.Fatalf("Command = %q, want a single go executable", cmd.Command)
			}
			if !reflect.DeepEqual(cmd.Args, tt.args) {
				t.Fatalf("Args = %#v, want %#v", cmd.Args, tt.args)
			}
			if cmd.Language != "go" {
				t.Fatalf("Language = %q, want go", cmd.Language)
			}
		})
	}
}

func TestListToolchainCommandsGoModuleIncludesAdvancedActions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/app\n\ngo 1.25\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := NewToolchainService()
	if err := svc.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}

	commands := svc.ListToolchainCommands()
	for _, id := range []string{"go-test-race", "go-bench", "go-generate"} {
		if !containsToolchainCommand(commands, id) {
			t.Errorf("Go module command list is missing %q", id)
		}
	}
	if containsToolchainCommand(commands, "go-work-sync") {
		t.Error("go-work-sync must not be offered when the workspace has no go.work")
	}
}

func TestListToolchainCommandsGoWorkIncludesSync(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.25\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := NewToolchainService()
	if err := svc.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}

	commands := svc.ListToolchainCommands()
	if !containsToolchainCommand(commands, "go-work-sync") {
		t.Fatal("go-work-sync must be offered when go.work exists")
	}
}

func TestGoAdvancedCommandsRunAtWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "internal", "worker")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(nested, "worker.go")
	svc := NewToolchainService()
	if err := svc.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"go-test-race", "go-bench", "go-generate", "go-work-sync"} {
		if got := svc.workDirForCommand(id, filePath); got != root {
			t.Errorf("%s work dir = %q, want workspace root %q", id, got, root)
		}
	}
}

func toolchainCommandByID(id string) (ToolchainCommand, bool) {
	for _, cmd := range allToolchainCommands {
		if cmd.ID == id {
			return cmd, true
		}
	}
	return ToolchainCommand{}, false
}

func containsToolchainCommand(commands []ToolchainCommand, id string) bool {
	for _, cmd := range commands {
		if cmd.ID == id {
			return true
		}
	}
	return false
}
