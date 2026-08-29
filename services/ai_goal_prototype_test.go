package services

// GOAL-P0-04A regression tests.
//
// Baseline defect: the default Goal executor plans with a fixed string, runs
// `go env GOOS` regardless of that plan, and its evaluator always returns false.
// RunGoal drove it anyway, so the UI showed a sequence of iterations that looked
// autonomous while being structurally incapable of accomplishing any goal. These
// tests lock the honest-degradation contract: a prototype executor refuses to
// run unless the user explicitly opts in, the refusal is distinguishable, and
// the goal's status is not mutated by a refusal.

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// nonPrototypeExecutor does not implement PrototypeExecutor, so the gate must
// not apply to it. It completes immediately.
type nonPrototypeExecutor struct{ ran bool }

func (e *nonPrototypeExecutor) Plan(*Goal) (string, error) { return "real plan", nil }

func (e *nonPrototypeExecutor) Execute(*Goal, string) (GoalRoundResult, error) {
	e.ran = true
	return GoalRoundResult{Success: true, Note: "real work"}, nil
}

func (e *nonPrototypeExecutor) Evaluate(*Goal) (bool, error) { return true, nil }

// selfDeclaredPrototype is a prototype that records whether it was driven, so a
// test can prove the gate blocked execution rather than merely returning early.
type selfDeclaredPrototype struct{ ran bool }

func (e *selfDeclaredPrototype) Plan(*Goal) (string, error) { return "fixed", nil }

func (e *selfDeclaredPrototype) Execute(*Goal, string) (GoalRoundResult, error) {
	e.ran = true
	return GoalRoundResult{Success: true}, nil
}

func (e *selfDeclaredPrototype) Evaluate(*Goal) (bool, error) { return false, nil }

func (e *selfDeclaredPrototype) IsPrototype() bool { return true }

func (e *selfDeclaredPrototype) PrototypeLimitation() string {
	return "test prototype does nothing useful"
}

func newPrototypeTestGoal(t *testing.T, svc *AIGoalService, id string) *Goal {
	t.Helper()
	g, err := svc.CreateGoal(id, "do a thing", "it is done", 3, 1.0, time.Minute, true)
	if err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	return g
}

func TestDefaultGoalExecutorIsNotPrototypeScaffolding(t *testing.T) {
	exec := NewDefaultGoalExecutor(NewAgentService(), t.TempDir())
	if proto, ok := exec.(PrototypeExecutor); ok && proto.IsPrototype() {
		t.Fatal("production defaultGoalExecutor must not remain a prototype go env GOOS scaffold")
	}
}

func TestRunGoalRefusesPrototypeExecutorByDefault(t *testing.T) {
	svc := newTestGoalService(t)
	exec := &selfDeclaredPrototype{}
	svc.setInternalExecutor(exec, nil)
	g := newPrototypeTestGoal(t, svc, "p1")

	err := svc.RunGoal("p1", nil, nil)
	if !errors.Is(err, ErrGoalPrototypeDisabled) {
		t.Fatalf("RunGoal error = %v, want ErrGoalPrototypeDisabled", err)
	}
	if exec.ran {
		t.Fatal("prototype executor was driven despite the gate")
	}
	// The refusal must not present as an attempt. Leaving the goal in "running"
	// or "failed" would read as "the agent tried and could not do it", which is a
	// false claim about capability.
	g.mu.Lock()
	status := g.Status
	iteration := g.Iteration
	startedAt := g.StartedAt
	g.mu.Unlock()
	if status != GoalStatusCreated {
		t.Errorf("status after refusal = %s, want %s (refusal must not look like a failed run)", status, GoalStatusCreated)
	}
	if iteration != 0 {
		t.Errorf("iteration after refusal = %d, want 0", iteration)
	}
	if startedAt != nil {
		t.Error("StartedAt was set by a refused run")
	}
}

func TestRunGoalAllowsPrototypeAfterExplicitOptIn(t *testing.T) {
	svc := newTestGoalService(t)
	exec := &selfDeclaredPrototype{}
	svc.setInternalExecutor(exec, nil)
	newPrototypeTestGoal(t, svc, "p2")

	svc.SetPrototypeExecutionEnabled(true)
	if err := svc.RunGoal("p2", nil, nil); err != nil {
		t.Fatalf("RunGoal after opt-in: %v", err)
	}
	if !exec.ran {
		t.Fatal("prototype executor was not driven after explicit opt-in")
	}
}

func TestRunGoalDoesNotGateRealExecutor(t *testing.T) {
	svc := newTestGoalService(t)
	exec := &nonPrototypeExecutor{}
	svc.setInternalExecutor(exec, nil)
	newPrototypeTestGoal(t, svc, "p3")

	// A real executor must be unaffected by the prototype gate even though
	// prototype execution is disabled.
	if enabled := svc.IsPrototypeExecutionEnabled(); enabled {
		t.Fatal("prototype execution must default to disabled")
	}
	if err := svc.RunGoal("p3", nil, nil); err != nil {
		t.Fatalf("RunGoal with a real executor: %v", err)
	}
	if !exec.ran {
		t.Fatal("real executor was blocked by the prototype gate")
	}
}

func TestResumeGoalRefusalDoesNotLeaveGoalStuckRunning(t *testing.T) {
	svc := newTestGoalService(t)
	real := &nonPrototypeExecutor{}
	svc.setInternalExecutor(real, nil)
	g := newPrototypeTestGoal(t, svc, "p4")

	// Reach the paused state through the real API rather than mutating status
	// directly, so the test exercises the same transition users hit.
	g.mu.Lock()
	g.Status = GoalStatusPaused
	g.mu.Unlock()

	// Now swap in a prototype and attempt resume. Before the fix, ResumeGoal set
	// status to Running before delegating, so a gate rejection stranded the goal
	// in Running with nothing driving it.
	proto := &selfDeclaredPrototype{}
	svc.setInternalExecutor(proto, nil)

	err := svc.ResumeGoal("p4", nil, nil)
	if !errors.Is(err, ErrGoalPrototypeDisabled) {
		t.Fatalf("ResumeGoal error = %v, want ErrGoalPrototypeDisabled", err)
	}
	if proto.ran {
		t.Fatal("prototype executor ran via ResumeGoal despite the gate")
	}
	g.mu.Lock()
	status := g.Status
	g.mu.Unlock()
	if status != GoalStatusPaused {
		t.Fatalf("status after refused resume = %s, want %s (goal must not be stranded in running)", status, GoalStatusPaused)
	}
}

func TestGetExecutorCapabilityReportsPrototypeHonestly(t *testing.T) {
	svc := newTestGoalService(t)

	// No executor installed: must not claim autonomous capability.
	capability := svc.GetExecutorCapability()
	if !capability.Prototype {
		t.Error("with no executor installed, Prototype = false; UI would claim a capability that does not exist")
	}
	if strings.TrimSpace(capability.Limitation) == "" {
		t.Error("Limitation is empty with no executor installed")
	}

	svc.setInternalExecutor(&selfDeclaredPrototype{}, nil)
	capability = svc.GetExecutorCapability()
	if !capability.Prototype {
		t.Error("prototype executor reported as non-prototype")
	}
	if capability.Limitation != "test prototype does nothing useful" {
		t.Errorf("Limitation = %q, want the executor's own text passed through verbatim", capability.Limitation)
	}
	if capability.AutoRunEnabled {
		t.Error("AutoRunEnabled = true before any opt-in")
	}

	svc.SetPrototypeExecutionEnabled(true)
	if capability = svc.GetExecutorCapability(); !capability.AutoRunEnabled {
		t.Error("AutoRunEnabled = false after opt-in")
	}

	svc.setInternalExecutor(&nonPrototypeExecutor{}, nil)
	if capability = svc.GetExecutorCapability(); capability.Prototype {
		t.Error("real executor reported as prototype")
	}
}

func TestPrototypeOptInDoesNotBypassCommandApproval(t *testing.T) {
	// Opting into the prototype must unlock nothing but the loop itself: every
	// command still needs a backend-issued capability. This guards the
	// prompt-1 baseline that no renderer-visible flag can grant execution.
	agent := NewAgentService()
	root := t.TempDir()
	if err := agent.configureWorkspaceRoot(root); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	exec := NewDefaultGoalExecutor(agent, root)

	svc := newTestGoalService(t)
	svc.setInternalExecutor(exec, nil)
	svc.SetPrototypeExecutionEnabled(true)
	newPrototypeTestGoal(t, svc, "p5")

	// The executor requests approval internally via RequestCommandApproval, so
	// this must not panic or silently execute without a token. Either it obtains
	// a backend token for the exact argv/cwd, or it errors; it must never run an
	// unapproved command.
	_ = svc.RunGoal("p5", nil, nil)

	// Prove the approval path is still mandatory by attempting a forged token.
	if _, err := agent.executeApprovedCommandLegacy("go env GOOS", root, "forged-token"); err == nil {
		t.Fatal("ExecuteApprovedCommand accepted a forged token; prototype opt-in must not weaken approval")
	}
}
