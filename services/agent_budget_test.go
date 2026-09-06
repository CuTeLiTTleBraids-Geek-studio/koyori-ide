package services

// GOAL-P1-02 regression tests: backend-enforced Agent tool-call budget.
//
// Baseline defect: the only ceiling was `MAX_TOOL_CALLS = 20` in frontend
// `stores/agent.ts`, and reaching it produced `notifyWarning` + `pushOutput`
// only. The user kept approving, a renderer refresh reset the counter to zero,
// and a forged `agentState.toolCallCount` was never checked against anything.
// There was no backend budget of any kind.
//
// These tests lock the enforced contract at the toolBudget layer — the same
// enforcement point the agentcore Runtime drives (P19 P1-03 removed the
// parallel legacy approval pipeline, so reservations are exercised directly).
// Issuance is bounded, the bound holds under concurrency, retries do not
// refund, and only an explicit user-initiated epoch reset lifts an exhausted
// budget.

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// newBudgetTestService returns a service with a real workspace, so tests
// exercise the true budget wiring rather than a stub.
func newBudgetTestService(t *testing.T) *AgentService {
	t.Helper()
	svc := NewAgentService()
	if err := svc.configureWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatalf("set workspace root: %v", err)
	}
	return svc
}

func TestToolBudgetDefaultsMatchHistoricalFrontendCeiling(t *testing.T) {
	svc := newBudgetTestService(t)
	status := svc.GetToolBudget()

	// The enforced ceiling must equal the number the frontend used to display,
	// so enabling enforcement is not a surprise regression for existing users.
	if status.Limit != defaultToolBudgetCalls {
		t.Fatalf("Limit = %d, want %d", status.Limit, defaultToolBudgetCalls)
	}
	if status.Spent != 0 {
		t.Fatalf("fresh budget Spent = %d, want 0", status.Spent)
	}
	if status.Remaining != defaultToolBudgetCalls {
		t.Fatalf("Remaining = %d, want %d", status.Remaining, defaultToolBudgetCalls)
	}
	if status.Exhausted {
		t.Fatal("fresh budget reports Exhausted")
	}
}

// AC 1 + AC 2: the backend refuses reservations past the ceiling. The
// renderer's count is irrelevant — there is no renderer in this test at all.
func TestToolBudgetRefusesReservationsPastCeiling(t *testing.T) {
	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(3)
	budget := svc.ensureBudget()

	for i := 0; i < 3; i++ {
		if _, err := budget.Reserve(); err != nil {
			t.Fatalf("reservation %d within budget failed: %v", i+1, err)
		}
	}

	if _, err := budget.Reserve(); !errors.Is(err, ErrAgentBudgetExhausted) {
		t.Fatalf("reservation past budget error = %v, want ErrAgentBudgetExhausted", err)
	}

	status := svc.GetToolBudget()
	if !status.Exhausted {
		t.Fatal("budget does not report Exhausted after the ceiling was reached")
	}
	if status.Remaining != 0 {
		t.Fatalf("Remaining = %d, want 0", status.Remaining)
	}
	// AC 2: the refusal is terminal, not a queue. Repeating it must keep failing
	// rather than eventually letting one through.
	for i := 0; i < 5; i++ {
		if _, err := budget.Reserve(); !errors.Is(err, ErrAgentBudgetExhausted) {
			t.Fatalf("retry %d after exhaustion error = %v, want ErrAgentBudgetExhausted", i+1, err)
		}
	}
}

// AC 1 + AC 4: N concurrent reservations must consume N distinct slots. If the
// check-then-increment were not atomic, more than `limit` would be issued.
// Run with -race.
func TestToolBudgetReservationIsAtomicUnderConcurrency(t *testing.T) {
	const limit = 8
	const racers = 64

	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(limit)
	budget := svc.ensureBudget()

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		granted   int
		refused   int
		otherErrs []error
	)

	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // maximize contention on the budget mutex
			_, err := budget.Reserve()
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				granted++
			case errors.Is(err, ErrAgentBudgetExhausted):
				refused++
			default:
				otherErrs = append(otherErrs, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(otherErrs) > 0 {
		t.Fatalf("unexpected non-budget errors: %v", otherErrs)
	}
	if granted != limit {
		t.Fatalf("granted = %d, want exactly %d (over-issue means the check is not atomic)", granted, limit)
	}
	if refused != racers-limit {
		t.Fatalf("refused = %d, want %d", refused, racers-limit)
	}
	if spent := svc.GetToolBudget().Spent; spent != limit {
		t.Fatalf("Spent = %d, want %d", spent, limit)
	}
}

// AC 3: an exhausted budget is only liftable by an explicit epoch reset.
func TestStartNewToolBudgetEpochIsTheOnlyWayPastExhaustion(t *testing.T) {
	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(1)
	budget := svc.ensureBudget()

	if _, err := budget.Reserve(); err != nil {
		t.Fatalf("first reservation failed: %v", err)
	}
	if _, err := budget.Reserve(); !errors.Is(err, ErrAgentBudgetExhausted) {
		t.Fatal("expected exhaustion before the reset")
	}

	before := svc.GetToolBudget()
	after := svc.StartNewToolBudgetEpoch(2)

	if after.Epoch != before.Epoch+1 {
		t.Fatalf("epoch = %d, want %d", after.Epoch, before.Epoch+1)
	}
	if after.Spent != 0 {
		t.Fatalf("Spent after reset = %d, want 0", after.Spent)
	}
	if after.Limit != 2 {
		t.Fatalf("Limit after reset = %d, want 2", after.Limit)
	}
	if _, err := budget.Reserve(); err != nil {
		t.Fatalf("reservation after explicit reset failed: %v", err)
	}
}

// Execution point 4: an issued-then-unused reservation still counts. Otherwise
// "request, abandon, request again" is an unbounded loop.
func TestAbandonedReservationStillSpendsBudget(t *testing.T) {
	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(3)
	budget := svc.ensureBudget()

	for i := 0; i < 3; i++ {
		if _, err := budget.Reserve(); err != nil {
			t.Fatalf("reservation %d failed: %v", i+1, err)
		}
		// Deliberately never released.
	}
	if _, err := budget.Reserve(); !errors.Is(err, ErrAgentBudgetExhausted) {
		t.Fatal("abandoning reservations refunded budget; retry becomes a free bypass")
	}
}

func TestToolBudgetReleaseReservationIsEpochBound(t *testing.T) {
	budget := newToolBudget()
	budget.mu.Lock()
	budget.limit = 1
	budget.mu.Unlock()

	epoch, err := budget.Reserve()
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := budget.ReleaseReservation(epoch); err != nil {
		t.Fatalf("ReleaseReservation: %v", err)
	}
	if status := budget.status(); status.Spent != 0 || status.Remaining != 1 {
		t.Fatalf("status after release = spent %d remaining %d, want 0/1", status.Spent, status.Remaining)
	}
	if err := budget.ReleaseReservation(epoch); err == nil {
		t.Fatal("duplicate ReleaseReservation unexpectedly succeeded")
	}

	oldEpoch, err := budget.Reserve()
	if err != nil {
		t.Fatalf("Reserve after release: %v", err)
	}
	budget.newEpoch(1)
	if err := budget.ReleaseReservation(oldEpoch); err != nil {
		t.Fatalf("stale ReleaseReservation: %v", err)
	}
	if status := budget.status(); status.Spent != 0 || status.Epoch != oldEpoch+1 {
		t.Fatalf("status after stale release = epoch %d spent %d, want %d/0", status.Epoch, status.Spent, oldEpoch+1)
	}
}

// The wall-clock window bounds an epoch even when the call ceiling is not hit.
func TestToolBudgetWindowExpiryRefusesReservation(t *testing.T) {
	svc := newBudgetTestService(t)
	budget := svc.ensureBudget()

	budget.mu.Lock()
	budget.limit = 100
	budget.window = time.Millisecond
	budget.startedAt = time.Now().Add(-time.Second) // already past the window
	budget.mu.Unlock()

	if _, err := budget.Reserve(); !errors.Is(err, ErrAgentBudgetExhausted) {
		t.Fatalf("expired-window error = %v, want ErrAgentBudgetExhausted", err)
	}
	if !svc.GetToolBudget().TimedOut {
		t.Fatal("status does not report TimedOut for an expired window")
	}
}

// The status struct is the frontend's only source of truth, so it must remain
// internally consistent — a UI computing Remaining itself would be a second
// source that drifts.
func TestToolBudgetStatusIsSelfConsistent(t *testing.T) {
	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(5)
	budget := svc.ensureBudget()

	for i := 0; i < 2; i++ {
		if _, err := budget.Reserve(); err != nil {
			t.Fatalf("reservation %d failed: %v", i+1, err)
		}
	}

	status := svc.GetToolBudget()
	if status.Spent+status.Remaining != status.Limit {
		t.Fatalf("Spent(%d) + Remaining(%d) != Limit(%d)", status.Spent, status.Remaining, status.Limit)
	}
	if status.ExpiresAt.Before(status.StartedAt) {
		t.Fatalf("ExpiresAt %v is before StartedAt %v", status.ExpiresAt, status.StartedAt)
	}
}

// A bare struct literal must not panic — several existing tests construct
// AgentService that way, so lazy budget initialization has to hold.
func TestBareAgentServiceBudgetDoesNotPanic(t *testing.T) {
	svc := &AgentService{}
	status := svc.GetToolBudget()
	if status.Limit != defaultToolBudgetCalls {
		t.Fatalf("bare service Limit = %d, want %d", status.Limit, defaultToolBudgetCalls)
	}
}
