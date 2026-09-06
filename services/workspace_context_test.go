package services

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// GOAL-P0-02 regression tests.
//
// Baseline defect: bootstrap constructed AIPlanService, AIGoalService,
// DiffService and the default executors with an empty workspace root, and
// ProjectService.buildWorkspaceRootSetters never included them. The empty root
// then made every snapshot trigger silently skip and made
// defaultSecurityChecker.IsWorkspacePath return true for any path, so Goal mode
// wrote to disk with no recovery point and no path boundary.

func TestWorkspaceContext_SetCanonicalizesAndBumpsGeneration(t *testing.T) {
	ctx := NewWorkspaceContext()
	if root := ctx.Root(); root != "" {
		t.Fatalf("fresh context root = %q, want empty", root)
	}
	if gen := ctx.Generation(); gen != 0 {
		t.Fatalf("fresh context generation = %d, want 0", gen)
	}

	dir := canonicalTestPath(t, t.TempDir())
	// Feed a non-canonical form to prove Set normalizes rather than storing raw input.
	if err := ctx.Set(filepath.Join(dir, "sub", "..")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if got := ctx.Root(); got != filepath.Clean(want) {
		t.Fatalf("root = %q, want %q", got, filepath.Clean(want))
	}
	if gen := ctx.Generation(); gen != 1 {
		t.Fatalf("generation after first Set = %d, want 1", gen)
	}

	// Re-setting the same workspace must not invalidate live capabilities.
	if err := ctx.Set(dir); err != nil {
		t.Fatalf("Set same root: %v", err)
	}
	if gen := ctx.Generation(); gen != 1 {
		t.Fatalf("generation after same-root Set = %d, want 1 (no churn)", gen)
	}
}

func TestWorkspaceContext_RejectsEmptyRootAndFailsClosed(t *testing.T) {
	ctx := NewWorkspaceContext()

	// An empty root must be rejected: clearing is explicit via Clear so a bug
	// cannot silently degrade consumers into the unrestricted empty-root state.
	if err := ctx.Set(""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Set(\"\") error = %v, want ErrInvalidInput", err)
	}

	if _, err := ctx.RequireRoot(); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("RequireRoot on empty context = %v, want ErrNotAllowed", err)
	}

	dir := t.TempDir()
	if err := ctx.Set(dir); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := ctx.RequireRoot(); err != nil {
		t.Fatalf("RequireRoot with root set: %v", err)
	}

	// Clear must bump the generation so anything bound to the old workspace
	// stops being accepted.
	genBefore := ctx.Generation()
	ctx.Clear()
	if root := ctx.Root(); root != "" {
		t.Fatalf("root after Clear = %q, want empty", root)
	}
	if gen := ctx.Generation(); gen != genBefore+1 {
		t.Fatalf("generation after Clear = %d, want %d", gen, genBefore+1)
	}
	if _, err := ctx.RequireRoot(); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("RequireRoot after Clear = %v, want ErrNotAllowed", err)
	}
}

func TestWorkspaceContext_SnapshotReadsRootAndGenerationTogether(t *testing.T) {
	ctx := NewWorkspaceContext()
	dir := t.TempDir()
	if err := ctx.Set(dir); err != nil {
		t.Fatalf("Set: %v", err)
	}
	root, gen := ctx.Snapshot()
	if root != ctx.Root() || gen != ctx.Generation() {
		t.Fatalf("Snapshot() = (%q, %d), want (%q, %d)", root, gen, ctx.Root(), ctx.Generation())
	}
}

// newWorkspaceIntegrationGraph builds the same object graph that
// wireServiceDependencies + bindWorkspaceRoots build in main.go, so the test
// exercises real propagation instead of calling per-service setters directly.
type workspaceIntegrationGraph struct {
	ctx      *WorkspaceContext
	project  *ProjectService
	file     *FileService
	agent    *AgentService
	plan     *AIPlanService
	goal     *AIGoalService
	diff     *DiffService
	snapshot *SnapshotService
	stepExec StepExecutor
	goalExec GoalExecutor
	checker  SecurityChecker
}

func newWorkspaceIntegrationGraph(t *testing.T) *workspaceIntegrationGraph {
	t.Helper()

	ctx := NewWorkspaceContext()
	file := &FileService{}
	agent := NewAgentService()
	snapshot := NewSnapshotService(t.TempDir())
	plan := NewAIPlanService()
	goal := NewAIGoalService()
	diff := NewDiffService()

	// Mirror wireServiceDependencies: snapshot service injected with the empty
	// bootstrap root, then the shared context injected on top of it.
	plan.setSnapshotService(snapshot, "")
	goal.setSnapshotService(snapshot, "")
	diff.setSnapshotService(snapshot, "")
	plan.setWorkspaceContext(ctx)
	goal.setWorkspaceContext(ctx)
	diff.setWorkspaceContext(ctx)

	stepExec := NewDefaultStepExecutorWithContext(agent, ctx)
	goalExec := NewDefaultGoalExecutorWithContext(agent, ctx)
	checker := NewDefaultSecurityCheckerWithContext(agent, ctx)
	plan.setInternalExecutor(stepExec)
	goal.setInternalExecutor(goalExec, checker)

	project := &ProjectService{
		configPath:  filepath.Join(t.TempDir(), "projects.json"),
		fileService: file,
	}
	project.setWorkspaceContext(ctx)

	return &workspaceIntegrationGraph{
		ctx: ctx, project: project, file: file, agent: agent,
		plan: plan, goal: goal, diff: diff, snapshot: snapshot,
		stepExec: stepExec, goalExec: goalExec, checker: checker,
	}
}

// TestBootstrapWorkspaceSnapshotIntegration is the AC-named test: it drives a
// real ProjectService.AddProject through the bootstrap-shaped graph and asserts
// every snapshot/executor holder observes the same canonical root.
func TestBootstrapWorkspaceSnapshotIntegration(t *testing.T) {
	g := newWorkspaceIntegrationGraph(t)

	// Before any project is open, every consumer must fail closed.
	if err := g.plan.tryCreateSnapshot(SnapshotReasonPlanStep); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("plan snapshot with no workspace = %v, want ErrNotAllowed", err)
	}
	if _, err := g.goal.tryCreateSnapshot(); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("goal snapshot with no workspace = %v, want ErrNotAllowed", err)
	}
	if _, err := g.diff.CreatePreApplySnapshot(); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("diff snapshot with no workspace = %v, want ErrNotAllowed", err)
	}
	if g.checker.IsWorkspacePath("/etc/passwd") {
		t.Fatal("IsWorkspacePath returned true with no workspace open (fail-open regression)")
	}

	// Open workspace A through the real entry point.
	workspaceA := canonicalTestPath(t, t.TempDir())
	if _, err := g.project.AddProject(workspaceA); err != nil {
		t.Fatalf("AddProject(A): %v", err)
	}

	wantA, err := filepath.Abs(workspaceA)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	wantA = filepath.Clean(wantA)

	// AC: Plan / Goal / Diff / Snapshot / executor all read A's canonical root
	// under the same generation.
	if got := g.ctx.Root(); got != wantA {
		t.Fatalf("context root after AddProject(A) = %q, want %q", got, wantA)
	}
	genA := g.ctx.Generation()
	if genA == 0 {
		t.Fatal("generation did not advance when workspace A was opened")
	}

	assertSnapshotTarget := func(label, got string) {
		t.Helper()
		if got != wantA {
			t.Fatalf("%s snapshot root = %q, want %q", label, got, wantA)
		}
	}
	_, planRoot, planOK, err := g.plan.snapshotTarget()
	if err != nil || !planOK {
		t.Fatalf("plan snapshotTarget after AddProject(A): ok=%v err=%v", planOK, err)
	}
	assertSnapshotTarget("plan", planRoot)

	_, goalRoot, goalOK, err := g.goal.snapshotTarget()
	if err != nil || !goalOK {
		t.Fatalf("goal snapshotTarget after AddProject(A): ok=%v err=%v", goalOK, err)
	}
	assertSnapshotTarget("goal", goalRoot)

	// AC: AI edit creates the snapshot in the correct root.
	if err := os.WriteFile(filepath.Join(workspaceA, "a.txt"), []byte("in A"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := g.plan.tryCreateSnapshot(SnapshotReasonPlanStep); err != nil {
		t.Fatalf("plan snapshot in workspace A: %v", err)
	}

	// AC: the path boundary is now enforced against A, not wide open.
	if !g.checker.IsWorkspacePath(filepath.Join(workspaceA, "a.txt")) {
		t.Fatal("IsWorkspacePath rejected a path inside the open workspace")
	}
	if g.checker.IsWorkspacePath(filepath.Join(t.TempDir(), "outside.txt")) {
		t.Fatal("IsWorkspacePath accepted a path outside the open workspace")
	}

	// AC: switching to B replaces the root everywhere and invalidates generation A.
	workspaceB := canonicalTestPath(t, t.TempDir())
	if _, err := g.project.AddProject(workspaceB); err != nil {
		t.Fatalf("AddProject(B): %v", err)
	}
	wantB, err := filepath.Abs(workspaceB)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	wantB = filepath.Clean(wantB)

	if got := g.ctx.Root(); got != wantB {
		t.Fatalf("context root after AddProject(B) = %q, want %q", got, wantB)
	}
	if genB := g.ctx.Generation(); genB == genA {
		t.Fatalf("generation did not change on workspace switch (still %d)", genB)
	}
	_, planRootB, _, err := g.plan.snapshotTarget()
	if err != nil {
		t.Fatalf("plan snapshotTarget after switch: %v", err)
	}
	if planRootB != wantB {
		t.Fatalf("plan snapshot root after switch = %q, want %q (stale root reused)", planRootB, wantB)
	}
	// A path that was inside A must now be rejected.
	if g.checker.IsWorkspacePath(filepath.Join(workspaceA, "a.txt")) {
		t.Fatal("IsWorkspacePath still accepts workspace A paths after switching to B")
	}
}

// TestWorkspaceContextRollbackKeepsSingleWorkspace covers the AC that a failing
// setter must leave every service on the previous workspace rather than a mixed
// A/B state.
func TestWorkspaceContextRollbackKeepsSingleWorkspace(t *testing.T) {
	g := newWorkspaceIntegrationGraph(t)

	workspaceA := canonicalTestPath(t, t.TempDir())
	if _, err := g.project.AddProject(workspaceA); err != nil {
		t.Fatalf("AddProject(A): %v", err)
	}
	wantA, err := filepath.Abs(workspaceA)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	wantA = filepath.Clean(wantA)
	genA := g.ctx.Generation()

	// A regular file is not a valid workspace: FileService.SetWorkspaceRoot
	// rejects it, which must roll the shared context back to A.
	notADir := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := g.project.AddProject(notADir); err == nil {
		t.Fatal("AddProject with a non-directory path unexpectedly succeeded")
	}

	if got := g.ctx.Root(); got != wantA {
		t.Fatalf("root after failed switch = %q, want %q (mixed A/B state)", got, wantA)
	}
	fileRoots := g.file.WorkspaceRoots()
	if len(fileRoots) != 1 || fileRoots[0] != wantA {
		t.Fatalf("FileService roots after failed switch = %v, want [%q]", fileRoots, wantA)
	}
	_, planRoot, _, err := g.plan.snapshotTarget()
	if err != nil {
		t.Fatalf("plan snapshotTarget after failed switch: %v", err)
	}
	if planRoot != wantA {
		t.Fatalf("plan snapshot root after failed switch = %q, want %q", planRoot, wantA)
	}
	if gen := g.ctx.Generation(); gen != genA {
		t.Fatalf("generation changed on a failed switch: %d, want %d", gen, genA)
	}
}

// TestWorkspaceContextSettersAreHiddenFromRenderer enforces the AC that no
// dangerous root setter is re-exposed to the renderer.
func TestWorkspaceContextSettersAreHiddenFromRenderer(t *testing.T) {
	cases := []struct{ file, exported, internal string }{
		{"ai_plan_service.go", "SetWorkspaceContext", "setWorkspaceContext"},
		{"ai_goal_service.go", "SetWorkspaceContext", "setWorkspaceContext"},
		{"diff_service.go", "SetWorkspaceContext", "setWorkspaceContext"},
		{"project_service.go", "SetWorkspaceContext", "setWorkspaceContext"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), tc.file, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", tc.file, err)
			}
			foundInternal := false
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil {
					continue
				}
				if fn.Name.Name == tc.exported {
					t.Fatalf("%s.%s remains exported to Wails reflection", tc.file, tc.exported)
				}
				if fn.Name.Name == tc.internal {
					foundInternal = true
				}
			}
			if !foundInternal {
				t.Fatalf("%s.%s trusted package method not found", tc.file, tc.internal)
			}
		})
	}
}
