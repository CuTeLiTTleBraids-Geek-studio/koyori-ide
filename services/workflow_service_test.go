package services

import (
	"os"
	"path/filepath"
	"testing"
)

func writeWorkflowFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestWorkflowService_LoadWorkflows_emptyDirReturnsEmpty(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty, got %d", len(out))
	}
}

func TestWorkflowService_LoadWorkflows_missingDirReturnsEmpty(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	// .koyori-ide/workflows does not exist
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty, got %d", len(out))
	}
}

func TestWorkflowService_LoadWorkflows_emptyRootReturnsError(t *testing.T) {
	svc := NewWorkflowService()
	_, err := svc.LoadWorkflows("")
	if err == nil {
		t.Fatal("expected error for empty root")
	}
}

func TestWorkflowService_LoadWorkflows_loadsYAML(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/build.yml", `
name: build
description: Build the project
steps:
  - name: compile
    command: go build ./...
  - name: test
    command: go test ./...
    dependsOn:
      - compile
`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(out))
	}
	wf := out[0]
	if wf.Name != "build" {
		t.Errorf("Name = %q, want %q", wf.Name, "build")
	}
	if wf.Description != "Build the project" {
		t.Errorf("Description = %q", wf.Description)
	}
	if len(wf.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(wf.Steps))
	}
	if wf.Steps[0].Name != "compile" {
		t.Errorf("step 0 name = %q", wf.Steps[0].Name)
	}
	if wf.Steps[0].Command != "go build ./..." {
		t.Errorf("step 0 command = %q", wf.Steps[0].Command)
	}
	if wf.Steps[1].Name != "test" {
		t.Errorf("step 1 name = %q", wf.Steps[1].Name)
	}
	if len(wf.Steps[1].DependsOn) != 1 || wf.Steps[1].DependsOn[0] != "compile" {
		t.Errorf("step 1 dependsOn = %v", wf.Steps[1].DependsOn)
	}
	if wf.Source != filepath.Join(".koyori-ide", "workflows", "build.yml") {
		t.Errorf("Source = %q", wf.Source)
	}
}

func TestWorkflowService_LoadWorkflows_loadsJSON(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/deploy.json", `{
  "name": "deploy",
  "steps": [
    {"name": "push", "command": "git push"},
    {"name": "release", "command": "gh release create", "dependsOn": ["push"]}
  ]
}`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(out))
	}
	if out[0].Name != "deploy" {
		t.Errorf("Name = %q", out[0].Name)
	}
	if len(out[0].Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(out[0].Steps))
	}
}

func TestWorkflowService_LoadWorkflows_loadsYAMLExtension(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/lint.yaml", `
name: lint
steps:
  - name: golint
    command: golangci-lint run
`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(out))
	}
	if out[0].Name != "lint" {
		t.Errorf("Name = %q", out[0].Name)
	}
}

func TestWorkflowService_LoadWorkflows_derivesNameFromFilename(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/ci.yml", `
steps:
  - name: build
    command: make build
`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if out[0].Name != "ci" {
		t.Errorf("derived Name = %q, want %q", out[0].Name, "ci")
	}
}

func TestWorkflowService_LoadWorkflows_skipsInvalidFiles(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	// Not valid YAML
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/bad.yml", `: not: valid: yaml: {{{`)
	// Valid YAML but no steps
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/empty.yml", `
name: empty
steps: []
`)
	// Unrecognized extension
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/readme.txt", `name: readme`)
	// Valid
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/good.yml", `
name: good
steps:
  - name: step1
    command: echo hello
`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 valid workflow, got %d", len(out))
	}
	if out[0].Name != "good" {
		t.Errorf("Name = %q, want %q", out[0].Name, "good")
	}
}

func TestWorkflowService_LoadWorkflows_skipsStepsWithoutNameOrCommand(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/partial.yml", `
name: partial
steps:
  - name: valid
    command: echo ok
  - name: no-command
  - command: echo no-name
`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if len(out[0].Steps) != 1 {
		t.Fatalf("expected 1 valid step, got %d", len(out[0].Steps))
	}
	if out[0].Steps[0].Name != "valid" {
		t.Errorf("step name = %q", out[0].Steps[0].Name)
	}
}

func TestWorkflowService_LoadWorkflows_sortsAlphabetically(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/zebra.yml", "name: z\nsteps:\n  - name: s\n    command: c\n")
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/apple.yml", "name: a\nsteps:\n  - name: s\n    command: c\n")
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/mango.yml", "name: m\nsteps:\n  - name: s\n    command: c\n")
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3, got %d", len(out))
	}
	// Alphabetical by filename: apple, mango, zebra
	if out[0].Name != "a" {
		t.Errorf("first = %q, want %q", out[0].Name, "a")
	}
	if out[1].Name != "m" {
		t.Errorf("second = %q, want %q", out[1].Name, "m")
	}
	if out[2].Name != "z" {
		t.Errorf("third = %q, want %q", out[2].Name, "z")
	}
}

func TestWorkflowService_LoadWorkflow_byName(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/build.yml", `
name: build
steps:
  - name: compile
    command: go build
`)
	wf, err := svc.LoadWorkflow(tmp, "build")
	if err != nil {
		t.Fatalf("LoadWorkflow: %v", err)
	}
	if wf.Name != "build" {
		t.Errorf("Name = %q", wf.Name)
	}
}

func TestWorkflowService_LoadWorkflow_byFilenameStem(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/my-ci.yml", `
steps:
  - name: s
    command: c
`)
	// Name field is empty, so match by filename stem "my-ci"
	wf, err := svc.LoadWorkflow(tmp, "my-ci")
	if err != nil {
		t.Fatalf("LoadWorkflow by stem: %v", err)
	}
	if wf == nil {
		t.Fatal("expected workflow, got nil")
	}
}

func TestWorkflowService_LoadWorkflow_notFoundReturnsError(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	_, err := svc.LoadWorkflow(tmp, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent workflow")
	}
}

func TestWorkflowService_ValidateDependencies_validChain(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Steps: []WorkflowStep{
			{Name: "a", Command: "echo a"},
			{Name: "b", Command: "echo b", DependsOn: []string{"a"}},
			{Name: "c", Command: "echo c", DependsOn: []string{"b"}},
		},
	}
	if err := svc.ValidateDependencies(wf); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestWorkflowService_ValidateDependencies_unknownDep(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Steps: []WorkflowStep{
			{Name: "a", Command: "echo a", DependsOn: []string{"nonexistent"}},
		},
	}
	err := svc.ValidateDependencies(wf)
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
}

func TestWorkflowService_ValidateDependencies_circular(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Steps: []WorkflowStep{
			{Name: "a", Command: "echo a", DependsOn: []string{"c"}},
			{Name: "b", Command: "echo b", DependsOn: []string{"a"}},
			{Name: "c", Command: "echo c", DependsOn: []string{"b"}},
		},
	}
	err := svc.ValidateDependencies(wf)
	if err == nil {
		t.Fatal("expected error for circular dependency")
	}
}

func TestWorkflowService_ValidateDependencies_nilWorkflow(t *testing.T) {
	svc := NewWorkflowService()
	err := svc.ValidateDependencies(nil)
	if err == nil {
		t.Fatal("expected error for nil workflow")
	}
}

func TestWorkflowService_ValidateDependencies_selfDependency(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Steps: []WorkflowStep{
			{Name: "a", Command: "echo a", DependsOn: []string{"a"}},
		},
	}
	err := svc.ValidateDependencies(wf)
	if err == nil {
		t.Fatal("expected error for self-dependency")
	}
}

func TestWorkflowStep_ComposeStepCommandLine(t *testing.T) {
	tests := []struct {
		name string
		step WorkflowStep
		want string
	}{
		{
			name: "no args",
			step: WorkflowStep{Name: "s", Command: "go build"},
			want: "go build",
		},
		{
			name: "with args",
			step: WorkflowStep{Name: "s", Command: "echo", Args: []string{"hello", "world"}},
			want: "echo 'hello' 'world'",
		},
		{
			name: "args with spaces quoted",
			step: WorkflowStep{Name: "s", Command: "echo", Args: []string{"hello world"}},
			want: "echo 'hello world'",
		},
		{
			name: "args with single quote escaped",
			step: WorkflowStep{Name: "s", Command: "echo", Args: []string{"it's"}},
			want: "echo 'it'\\''s'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.step.ComposeStepCommandLine()
			if got != tt.want {
				t.Errorf("ComposeStepCommandLine = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkflowService_LoadWorkflows_watchGlobs(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/autolint.yml", `
name: autolint
steps:
  - name: lint
    command: golangci-lint run
watch:
  - "**/*.go"
  - "src/**/*.ts"
`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if len(out[0].Watch) != 2 {
		t.Fatalf("expected 2 watch patterns, got %d", len(out[0].Watch))
	}
	if out[0].Watch[0] != "**/*.go" {
		t.Errorf("watch[0] = %q", out[0].Watch[0])
	}
	if out[0].Watch[1] != "src/**/*.ts" {
		t.Errorf("watch[1] = %q", out[0].Watch[1])
	}
}

func TestWorkflowService_LoadWorkflows_conditionField(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/conditional.yml", `
name: conditional
steps:
  - name: check
    command: test -f go.mod
  - name: build
    command: go build ./...
    dependsOn:
      - check
    condition: test -f go.mod
`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if len(out[0].Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(out[0].Steps))
	}
	if out[0].Steps[1].Condition != "test -f go.mod" {
		t.Errorf("condition = %q", out[0].Steps[1].Condition)
	}
}

func TestWorkflowService_LoadWorkflows_expectSuccessField(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/tolerant.yml", `
name: tolerant
steps:
  - name: strict
    command: go build ./...
  - name: tolerant
    command: go vet ./...
    expectSuccess: false
`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 || len(out[0].Steps) != 2 {
		t.Fatalf("expected 1 workflow with 2 steps, got %d/%d", len(out), func() int {
			if len(out) > 0 {
				return len(out[0].Steps)
			}
			return 0
		}())
	}
	// Step 0: no expectSuccess set → nil (frontend defaults to true).
	if out[0].Steps[0].ExpectSuccess != nil {
		t.Errorf("step 0 expectSuccess should be nil, got %v", *out[0].Steps[0].ExpectSuccess)
	}
	// Step 1: expectSuccess explicitly false.
	if out[0].Steps[1].ExpectSuccess == nil {
		t.Fatalf("step 1 expectSuccess should be non-nil")
	}
	if *out[0].Steps[1].ExpectSuccess != false {
		t.Errorf("step 1 expectSuccess should be false, got %v", *out[0].Steps[1].ExpectSuccess)
	}
}

func TestWorkflowService_LoadWorkflows_runOnTrigger(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/auto-test.yml", `
name: auto-test
runOn:
  event: file-saved
  glob: "**/*.go"
steps:
  - name: test
    command: go test ./...
`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(out))
	}
	if out[0].RunOn == nil {
		t.Fatal("expected RunOn to be set")
	}
	if out[0].RunOn.Event != "file-saved" {
		t.Errorf("RunOn.Event = %q, want %q", out[0].RunOn.Event, "file-saved")
	}
	if out[0].RunOn.Glob != "**/*.go" {
		t.Errorf("RunOn.Glob = %q, want %q", out[0].RunOn.Glob, "**/*.go")
	}
}

func TestWorkflowService_LoadWorkflows_runOnOmitted(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/manual.yml", `
name: manual
steps:
  - name: build
    command: go build ./...
`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(out))
	}
	if out[0].RunOn != nil {
		t.Errorf("expected RunOn to be nil, got %+v", out[0].RunOn)
	}
}

// ---- prompt-4 Task 12: Create / Save / Delete / Rename ----

func TestWorkflowService_CreateSaveDeleteRename(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()

	def := &WorkflowDef{
		Name:        "build-test",
		Description: "build then test",
		Steps: []WorkflowStep{
			{Name: "build", Command: "go", Args: []string{"build", "./..."}},
			{Name: "test", Command: "go", Args: []string{"test", "./..."}, DependsOn: []string{"build"}},
		},
	}

	if err := svc.CreateWorkflow(tmp, "build-test", def); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	// Duplicate create must fail.
	if err := svc.CreateWorkflow(tmp, "build-test", def); err == nil {
		t.Fatal("expected error on duplicate CreateWorkflow")
	}

	loaded, err := svc.LoadWorkflow(tmp, "build-test")
	if err != nil {
		t.Fatalf("LoadWorkflow: %v", err)
	}
	if !loaded.RequiresConfirmation {
		t.Error("expected RequiresConfirmation true for project workflow")
	}
	if len(loaded.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(loaded.Steps))
	}

	// Save updates description.
	loaded.Description = "updated"
	if err := svc.SaveWorkflow(tmp, "build-test", loaded); err != nil {
		t.Fatalf("SaveWorkflow: %v", err)
	}
	reloaded, err := svc.LoadWorkflow(tmp, "build-test")
	if err != nil {
		t.Fatalf("LoadWorkflow after save: %v", err)
	}
	if reloaded.Description != "updated" {
		t.Errorf("description = %q, want updated", reloaded.Description)
	}

	// Rename.
	if err := svc.RenameWorkflow(tmp, "build-test", "build-test-v2"); err != nil {
		t.Fatalf("RenameWorkflow: %v", err)
	}
	if _, err := svc.LoadWorkflow(tmp, "build-test"); err == nil {
		t.Error("old name should not exist after rename")
	}
	if _, err := svc.LoadWorkflow(tmp, "build-test-v2"); err != nil {
		t.Fatalf("new name should exist: %v", err)
	}

	// Delete.
	if err := svc.DeleteWorkflow(tmp, "build-test-v2"); err != nil {
		t.Fatalf("DeleteWorkflow: %v", err)
	}
	if _, err := svc.LoadWorkflow(tmp, "build-test-v2"); err == nil {
		t.Error("expected not found after delete")
	}
}

func TestWorkflowService_CreateWorkflow_RejectsTraversal(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	def := &WorkflowDef{
		Name:  "evil",
		Steps: []WorkflowStep{{Name: "x", Command: "echo"}},
	}
	if err := svc.CreateWorkflow(tmp, "../evil", def); err == nil {
		t.Fatal("expected error for path traversal name")
	}
	if err := svc.CreateWorkflow(tmp, "sub/dir", def); err == nil {
		t.Fatal("expected error for path separator in name")
	}
}

func TestWorkflowService_CreateWorkflow_RejectsInvalidDef(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	def := &WorkflowDef{Name: "empty", Steps: nil}
	if err := svc.CreateWorkflow(tmp, "empty", def); err == nil {
		t.Fatal("expected error for workflow with no steps")
	}
}
