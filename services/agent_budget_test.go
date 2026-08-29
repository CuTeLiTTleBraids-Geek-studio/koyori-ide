package services

// GOAL-P1-02 regression tests: backend-enforced Agent tool-call budget.
//
// Baseline defect: the only ceiling was `MAX_TOOL_CALLS = 20` in frontend
// `stores/agent.ts`, and reaching it produced `notifyWarning` + `pushOutput`
// only. The user kept approving, a renderer refresh reset the counter to zero,
// and a forged `agentState.toolCallCount` was never checked against anything.
// There was no backend budget of any kind.
//
// These tests lock the enforced contract: issuance is bounded, the bound holds
// under concurrency, retries do not refund, and only an explicit user-initiated
// epoch reset lifts an exhausted budget — invalidating prior capabilities.

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// newBudgetTestService returns a service with auto-approval and a real
// workspace, so tests exercise the true issuance path rather than a stub.
func newBudgetTestService(t *testing.T) *AgentService {
	t.Helper()
	svc := NewAgentService()
	svc.approveCommand = func(command, cwd string, risk RiskLevel) bool { return true }
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
		t.Fatalf("fresh budget Remaining = %d, want %d", status.Remaining, defaultToolBudgetCalls)
	}
	if status.Exhausted {
		t.Fatal("fresh budget reports Exhausted")
	}
}

// AC 1 + AC 2: the backend refuses issuance past the ceiling. The renderer's
// count is irrelevant — there is no renderer in this test at all.
func TestRequestCommandApprovalRefusesPastBudget(t *testing.T) {
	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(3)

	for i := 0; i < 3; i++ {
		if _, err := svc.requestCommandApprovalLegacy("go version", ""); err != nil {
			t.Fatalf("approval %d within budget failed: %v", i+1, err)
		}
	}

	_, err := svc.requestCommandApprovalLegacy("go version", "")
	if !errors.Is(err, ErrAgentBudgetExhausted) {
		t.Fatalf("approval past budget error = %v, want ErrAgentBudgetExhausted", err)
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
		if _, err := svc.requestCommandApprovalLegacy("go version", ""); !errors.Is(err, ErrAgentBudgetExhausted) {
			t.Fatalf("retry %d after exhaustion error = %v, want ErrAgentBudgetExhausted", i+1, err)
		}
	}
}

// AC 1 + AC 4: N concurrent approvals must consume N distinct slots. If the
// check-then-increment were not atomic, more than `limit` tokens would be
// issued. Run with -race.
func TestRequestCommandApprovalIsAtomicUnderConcurrency(t *testing.T) {
	const limit = 8
	const racers = 64

	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(limit)

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
			_, err := svc.requestCommandApprovalLegacy("go version", "")
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

	if _, err := svc.requestCommandApprovalLegacy("go version", ""); err != nil {
		t.Fatalf("first approval failed: %v", err)
	}
	if _, err := svc.requestCommandApprovalLegacy("go version", ""); !errors.Is(err, ErrAgentBudgetExhausted) {
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
	if _, err := svc.requestCommandApprovalLegacy("go version", ""); err != nil {
		t.Fatalf("approval after explicit reset failed: %v", err)
	}
}

// AC 3: a capability minted before a reset must not be redeemable after it.
// Without this, a caller could stockpile tokens while under budget, ask the
// user to extend, and then spend the pre-reset tokens on top of the new budget.
func TestCapabilityFromPreviousEpochIsRejected(t *testing.T) {
	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(5)

	token, err := svc.requestCommandApprovalLegacy("go version", "")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}

	// User explicitly opens a new budget epoch.
	svc.StartNewToolBudgetEpoch(5)

	if err := svc.consumeCommandApproval(token, "go version", ""); err == nil {
		t.Fatal("capability from the previous epoch was accepted after a budget reset")
	}
}

// AC 3 negative control: within a single epoch the capability still works, so
// the epoch check has not broken the normal path.
func TestCapabilityWithinSameEpochStillRedeems(t *testing.T) {
	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(5)

	token, err := svc.requestCommandApprovalLegacy("go version", "")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	if err := svc.consumeCommandApproval(token, "go version", ""); err != nil {
		t.Fatalf("same-epoch capability rejected: %v", err)
	}
}

// Execution point 4: a declined approval must not spend budget. Charging for a
// decline would punish the user for saying no.
func TestDeclinedApprovalDoesNotSpendBudget(t *testing.T) {
	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(4)
	svc.approveCommand = func(command, cwd string, risk RiskLevel) bool { return false }

	if _, err := svc.requestCommandApprovalLegacy("go version", ""); err == nil {
		t.Fatal("expected a declined approval to fail")
	}
	if spent := svc.GetToolBudget().Spent; spent != 0 {
		t.Fatalf("Spent after a decline = %d, want 0 (declining must be free)", spent)
	}
}

// Execution point 4: a blocked command must not spend budget either — it never
// reached the user and never produced a capability.
func TestBlockedCommandDoesNotSpendBudget(t *testing.T) {
	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(4)

	// Shell syntax is rejected by CheckCommand before the budget is touched.
	if _, err := svc.requestCommandApprovalLegacy("go version && rm -rf /", ""); err == nil {
		t.Fatal("expected a blocked command to fail")
	}
	if spent := svc.GetToolBudget().Spent; spent != 0 {
		t.Fatalf("Spent after a blocked command = %d, want 0", spent)
	}
}

// Execution point 4: an issued-then-unused capability still counts. Otherwise
// "request, abandon, request again" is an unbounded loop.
func TestAbandonedCapabilityStillSpendsBudget(t *testing.T) {
	svc := newBudgetTestService(t)
	svc.StartNewToolBudgetEpoch(3)

	for i := 0; i < 3; i++ {
		if _, err := svc.requestCommandApprovalLegacy("go version", ""); err != nil {
			t.Fatalf("approval %d failed: %v", i+1, err)
		}
		// Deliberately never redeemed.
	}
	if _, err := svc.requestCommandApprovalLegacy("go version", ""); !errors.Is(err, ErrAgentBudgetExhausted) {
		t.Fatal("abandoning capabilities refunded budget; retry becomes a free bypass")
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
func TestToolBudgetWindowExpiryRefusesIssuance(t *testing.T) {
	svc := newBudgetTestService(t)
	budget := svc.ensureBudget()

	budget.mu.Lock()
	budget.limit = 100
	budget.window = time.Millisecond
	budget.startedAt = time.Now().Add(-time.Second) // already past the window
	budget.mu.Unlock()

	_, err := svc.requestCommandApprovalLegacy("go version", "")
	if !errors.Is(err, ErrAgentBudgetExhausted) {
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

	for i := 0; i < 2; i++ {
		if _, err := svc.requestCommandApprovalLegacy("go version", ""); err != nil {
			t.Fatalf("approval %d failed: %v", i+1, err)
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
