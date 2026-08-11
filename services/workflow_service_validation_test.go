package services

import (
	"testing"
)

// TestValidateWorkflow_ValidWorkflow verifies that a well-formed workflow
// passes validation with no errors.
func TestValidateWorkflow_ValidWorkflow(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Name: "build-test",
		Steps: []WorkflowStep{
			{Name: "build", Command: "make build"},
			{Name: "test", Command: "make test", DependsOn: []string{"build"}},
		},
	}
	result := svc.ValidateWorkflow(wf)
	if !result.Valid {
		t.Errorf("expected valid, got errors: %v", result.Errors)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d", len(result.Errors))
	}
	if result.WorkflowName != "build-test" {
		t.Errorf("expected workflowName 'build-test', got %q", result.WorkflowName)
	}
}

// TestValidateWorkflow_DuplicateStepName verifies that duplicate step
// names are detected as validation errors.
func TestValidateWorkflow_DuplicateStepName(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Name: "dup",
		Steps: []WorkflowStep{
			{Name: "build", Command: "make build"},
			{Name: "build", Command: "make build-again"},
		},
	}
	result := svc.ValidateWorkflow(wf)
	if result.Valid {
		t.Error("expected invalid due to duplicate step name")
	}
	foundDup := false
	for _, e := range result.Errors {
		if e.Field == "steps[1].name" && contains(e.Message, "duplicate step name") {
			foundDup = true
		}
	}
	if !foundDup {
		t.Errorf("expected duplicate step name error, got: %v", result.Errors)
	}
}

// TestValidateWorkflow_EmptyStepName verifies that empty step names
// are detected as validation errors.
func TestValidateWorkflow_EmptyStepName(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Name: "empty-name",
		Steps: []WorkflowStep{
			{Name: "", Command: "echo hi"},
			{Name: "valid", Command: "echo valid"},
		},
	}
	result := svc.ValidateWorkflow(wf)
	if result.Valid {
		t.Error("expected invalid due to empty step name")
	}
	foundEmpty := false
	for _, e := range result.Errors {
		if e.Field == "steps[0].name" && contains(e.Message, "step name is empty") {
			foundEmpty = true
		}
	}
	if !foundEmpty {
		t.Errorf("expected empty step name error, got: %v", result.Errors)
	}
}

// TestValidateWorkflow_EmptyCommand verifies that empty commands are
// detected as validation errors.
func TestValidateWorkflow_EmptyCommand(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Name: "empty-cmd",
		Steps: []WorkflowStep{
			{Name: "build", Command: ""},
		},
	}
	result := svc.ValidateWorkflow(wf)
	if result.Valid {
		t.Error("expected invalid due to empty command")
	}
	foundEmptyCmd := false
	for _, e := range result.Errors {
		if e.Field == "steps[0].command" && contains(e.Message, "empty command") {
			foundEmptyCmd = true
		}
	}
	if !foundEmptyCmd {
		t.Errorf("expected empty command error, got: %v", result.Errors)
	}
}

// TestValidateWorkflow_UnknownRunOnEvent verifies that an invalid
// runOn.event value is detected (e.g. "file-save" typo).
func TestValidateWorkflow_UnknownRunOnEvent(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Name: "bad-trigger",
		Steps: []WorkflowStep{
			{Name: "lint", Command: "npm run lint"},
		},
		RunOn: &WorkflowTrigger{Event: "file-save"}, // typo: should be "file-saved"
	}
	result := svc.ValidateWorkflow(wf)
	if result.Valid {
		t.Error("expected invalid due to unknown runOn event")
	}
	foundUnknownEvent := false
	for _, e := range result.Errors {
		if e.Field == "runOn.event" && contains(e.Message, "unknown runOn event") {
			foundUnknownEvent = true
		}
	}
	if !foundUnknownEvent {
		t.Errorf("expected unknown runOn event error, got: %v", result.Errors)
	}
}

// TestValidateWorkflow_ValidRunOnEvents verifies that all whitelisted
// runOn.event values pass validation.
func TestValidateWorkflow_ValidRunOnEvents(t *testing.T) {
	events := []string{"file-saved", "startup", "workflow-completed"}
	for _, event := range events {
		svc := NewWorkflowService()
		wf := &WorkflowDef{
			Name: "trigger-" + event,
			Steps: []WorkflowStep{
				{Name: "step1", Command: "echo hi"},
			},
			RunOn: &WorkflowTrigger{Event: event},
		}
		result := svc.ValidateWorkflow(wf)
		if !result.Valid {
			t.Errorf("event %q should be valid, got errors: %v", event, result.Errors)
		}
	}
}

// TestValidateWorkflow_CircularDependency verifies that circular
// dependencies are detected.
func TestValidateWorkflow_CircularDependency(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Name: "cycle",
		Steps: []WorkflowStep{
			{Name: "a", Command: "echo a", DependsOn: []string{"b"}},
			{Name: "b", Command: "echo b", DependsOn: []string{"a"}},
		},
	}
	result := svc.ValidateWorkflow(wf)
	if result.Valid {
		t.Error("expected invalid due to circular dependency")
	}
	foundCycle := false
	for _, e := range result.Errors {
		if e.Field == "dependsOn" && contains(e.Message, "circular dependency") {
			foundCycle = true
		}
	}
	if !foundCycle {
		t.Errorf("expected circular dependency error, got: %v", result.Errors)
	}
}

// TestValidateWorkflow_UnknownDependency verifies that unknown dependencies
// are detected.
func TestValidateWorkflow_UnknownDependency(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Name: "unknown-dep",
		Steps: []WorkflowStep{
			{Name: "build", Command: "make build"},
			{Name: "deploy", Command: "make deploy", DependsOn: []string{"nonexistent"}},
		},
	}
	result := svc.ValidateWorkflow(wf)
	if result.Valid {
		t.Error("expected invalid due to unknown dependency")
	}
	foundUnknownDep := false
	for _, e := range result.Errors {
		if e.Field == "dependsOn" && contains(e.Message, "unknown step") {
			foundUnknownDep = true
		}
	}
	if !foundUnknownDep {
		t.Errorf("expected unknown dependency error, got: %v", result.Errors)
	}
}

// TestValidateWorkflow_NilWorkflow verifies that a nil workflow returns
// an invalid result with a nil error message.
func TestValidateWorkflow_NilWorkflow(t *testing.T) {
	svc := NewWorkflowService()
	result := svc.ValidateWorkflow(nil)
	if result.Valid {
		t.Error("expected invalid for nil workflow")
	}
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(result.Errors))
	}
	if result.Errors[0].Message != "workflow is nil" {
		t.Errorf("expected 'workflow is nil', got %q", result.Errors[0].Message)
	}
}

// TestValidateWorkflow_NoValidSteps verifies that a workflow with no
// valid steps (all empty names/commands) is detected.
func TestValidateWorkflow_NoValidSteps(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Name: "no-steps",
		Steps: []WorkflowStep{
			{Name: "", Command: ""},
			{Name: "", Command: ""},
		},
	}
	result := svc.ValidateWorkflow(wf)
	if result.Valid {
		t.Error("expected invalid due to no valid steps")
	}
	foundNoValid := false
	for _, e := range result.Errors {
		if e.Field == "steps" && contains(e.Message, "no valid steps") {
			foundNoValid = true
		}
	}
	if !foundNoValid {
		t.Errorf("expected 'no valid steps' error, got: %v", result.Errors)
	}
}

// TestValidateAllWorkflows verifies that ValidateAllWorkflows returns
// a result per workflow.
func TestValidateAllWorkflows(t *testing.T) {
	svc := NewWorkflowService()
	wfs := []WorkflowDef{
		{
			Name:  "valid-wf",
			Steps: []WorkflowStep{{Name: "step1", Command: "echo hi"}},
		},
		{
			Name:  "invalid-wf",
			Steps: []WorkflowStep{{Name: "step1", Command: ""}},
		},
	}
	results := svc.ValidateAllWorkflows(wfs)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].WorkflowName != "valid-wf" {
		t.Errorf("expected workflowName 'valid-wf', got %q", results[0].WorkflowName)
	}
	if !results[0].Valid {
		t.Errorf("expected valid-wf to be valid, got errors: %v", results[0].Errors)
	}
	if results[1].WorkflowName != "invalid-wf" {
		t.Errorf("expected workflowName 'invalid-wf', got %q", results[1].WorkflowName)
	}
	if results[1].Valid {
		t.Error("expected invalid-wf to be invalid")
	}
}

// --- G-SEC-03: startup workflow confirmation gate ---

// TestValidateWorkflow_ProjectSourceRequiresConfirmation verifies that a
// workflow loaded from the project's .koyori-ide/workflows directory has
// RequiresConfirmation forced to true (G-SEC-03). This prevents untrusted
// startup workflows in cloned repositories from auto-running shell commands.
func TestValidateWorkflow_ProjectSourceRequiresConfirmation(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Name:   "bootstrap",
		Source: ".koyori-ide/workflows/bootstrap.yml",
		Steps:  []WorkflowStep{{Name: "init", Command: "echo init"}},
		RunOn:  &WorkflowTrigger{Event: "startup"},
	}
	svc.ValidateWorkflow(wf)
	if !wf.RequiresConfirmation {
		t.Error("expected RequiresConfirmation=true for project-source workflow, got false")
	}
}

// TestValidateWorkflow_ProjectSourceRequiresConfirmationEvenWhenExplicitlyFalse
// verifies that a malicious project workflow cannot bypass the confirmation
// gate by setting requiresConfirmation: false in the file. The flag is forced
// true for project sources regardless of the explicit setting.
func TestValidateWorkflow_ProjectSourceRequiresConfirmationEvenWhenExplicitlyFalse(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Name:                 "sneaky",
		Source:               ".koyori-ide/workflows/sneaky.yml",
		Steps:                []WorkflowStep{{Name: "s", Command: "rm -rf /"}},
		RunOn:                &WorkflowTrigger{Event: "startup"},
		RequiresConfirmation: false, // explicit attempt to bypass
	}
	svc.ValidateWorkflow(wf)
	if !wf.RequiresConfirmation {
		t.Error("expected RequiresConfirmation forced true for project source even when explicitly false")
	}
}

// TestValidateWorkflow_NonProjectSourceDoesNotForceConfirmation verifies that
// a workflow without a project Source is not forced into confirmation (the
// flag keeps its original value). This keeps the gate scoped to untrusted
// project-level workflows.
func TestValidateWorkflow_NonProjectSourceDoesNotForceConfirmation(t *testing.T) {
	svc := NewWorkflowService()
	wf := &WorkflowDef{
		Name:   "manual",
		Source: "", // no source — e.g. an in-memory workflow
		Steps:  []WorkflowStep{{Name: "s", Command: "echo hi"}},
	}
	svc.ValidateWorkflow(wf)
	if wf.RequiresConfirmation {
		t.Error("expected RequiresConfirmation=false for non-project workflow, got true")
	}
}

// TestLoadWorkflows_ProjectSourceSetsRequiresConfirmation verifies that
// LoadWorkflows marks every loaded workflow with RequiresConfirmation=true
// so the frontend receives the flag and can show the confirmation gate.
func TestLoadWorkflows_ProjectSourceSetsRequiresConfirmation(t *testing.T) {
	svc := NewWorkflowService()
	tmp := t.TempDir()
	writeWorkflowFile(t, tmp, ".koyori-ide/workflows/startup.yml", `
name: startup
runOn:
  event: startup
steps:
  - name: init
    command: echo init
`)
	out, err := svc.LoadWorkflows(tmp)
	if err != nil {
		t.Fatalf("LoadWorkflows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(out))
	}
	if !out[0].RequiresConfirmation {
		t.Error("expected loaded project workflow to have RequiresConfirmation=true")
	}
}

// TestPendingStartupWorkflows_ReturnsStartupWorkflows verifies that
// PendingStartupWorkflows returns only workflows with runOn.event == "startup".
func TestPendingStartupWorkflows_ReturnsStartupWorkflows(t *testing.T) {
	svc := NewWorkflowService()
	wfs := []WorkflowDef{
		{
			Name:   "bootstrap",
			Source: ".koyori-ide/workflows/bootstrap.yml",
			Steps:  []WorkflowStep{{Name: "init", Command: "echo init"}},
			RunOn:  &WorkflowTrigger{Event: "startup"},
		},
		{
			Name:   "manual",
			Source: ".koyori-ide/workflows/manual.yml",
			Steps:  []WorkflowStep{{Name: "build", Command: "make"}},
		},
		{
			Name:   "auto-test",
			Source: ".koyori-ide/workflows/auto-test.yml",
			Steps:  []WorkflowStep{{Name: "test", Command: "go test"}},
			RunOn:  &WorkflowTrigger{Event: "file-saved", Glob: "**/*.go"},
		},
		{
			Name:   "sync-deps",
			Source: ".koyori-ide/workflows/sync-deps.yml",
			Steps:  []WorkflowStep{{Name: "sync", Command: "go mod download"}},
			RunOn:  &WorkflowTrigger{Event: "startup"},
		},
	}
	pending := svc.PendingStartupWorkflows(wfs)
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending startup workflows, got %d", len(pending))
	}
	names := []string{pending[0].Name, pending[1].Name}
	if names[0] != "bootstrap" && names[1] != "bootstrap" {
		t.Errorf("expected 'bootstrap' in pending list, got %v", names)
	}
	if names[0] != "sync-deps" && names[1] != "sync-deps" {
		t.Errorf("expected 'sync-deps' in pending list, got %v", names)
	}
}

// TestPendingStartupWorkflows_EmptyWhenNoStartupWorkflows verifies that
// PendingStartupWorkflows returns an empty slice when no startup workflows
// are present.
func TestPendingStartupWorkflows_EmptyWhenNoStartupWorkflows(t *testing.T) {
	svc := NewWorkflowService()
	wfs := []WorkflowDef{
		{
			Name:   "manual",
			Source: ".koyori-ide/workflows/manual.yml",
			Steps:  []WorkflowStep{{Name: "build", Command: "make"}},
		},
		{
			Name:   "auto-test",
			Source: ".koyori-ide/workflows/auto-test.yml",
			Steps:  []WorkflowStep{{Name: "test", Command: "go test"}},
			RunOn:  &WorkflowTrigger{Event: "file-saved"},
		},
	}
	pending := svc.PendingStartupWorkflows(wfs)
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending startup workflows, got %d", len(pending))
	}
}

// TestPendingStartupWorkflows_StartupWorkflowsNotAutoExecuted verifies the
// G-SEC-03 guarantee: a startup workflow is returned by PendingStartupWorkflows
// (for user confirmation) and is flagged with RequiresConfirmation=true so the
// frontend knows NOT to auto-execute it. The backend exposes no auto-run path
// for these workflows — they must be explicitly run by the user.
func TestPendingStartupWorkflows_StartupWorkflowsNotAutoExecuted(t *testing.T) {
	svc := NewWorkflowService()
	wfs := []WorkflowDef{
		{
			Name:   "bootstrap",
			Source: ".koyori-ide/workflows/bootstrap.yml",
			Steps:  []WorkflowStep{{Name: "init", Command: "echo init"}},
			RunOn:  &WorkflowTrigger{Event: "startup"},
		},
	}
	// Validate so RequiresConfirmation is applied (mirrors the frontend flow:
	// load → validate → list pending for confirmation).
	for i := range wfs {
		svc.ValidateWorkflow(&wfs[i])
	}
	pending := svc.PendingStartupWorkflows(wfs)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending startup workflow, got %d", len(pending))
	}
	// The pending workflow must be flagged for confirmation — the UI uses this
	// to show the "Pending Confirmation" badge and block auto-execution.
	if !pending[0].RequiresConfirmation {
		t.Error("expected pending startup workflow to have RequiresConfirmation=true")
	}
	// The backend WorkflowService has no Run/AutoRun method — the only way to
	// execute a workflow is via the explicit RunOn trigger listener in the
	// frontend, which no longer fires for "startup" events (G-SEC-03). This
	// test documents that contract: PendingStartupWorkflows is a pure listing
	// function with no execution side effects.
	if pending[0].RunOn == nil || pending[0].RunOn.Event != "startup" {
		t.Error("expected pending workflow to retain its startup trigger")
	}
}
