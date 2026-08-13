package services

// agent_budget.go — GOAL-P1-02: backend-enforced Agent tool-call budget.
//
// Baseline defect: the only tool-call ceiling was `MAX_TOOL_CALLS = 20` in
// frontend `stores/agent.ts`, and reaching it produced `notifyWarning` +
// `pushOutput` only. The user kept approving, a renderer refresh reset the
// counter to zero, and a forged `agentState.toolCallCount` was never checked
// against anything. There was no backend budget of any kind.
//
// The budget is enforced at capability issuance (RequestCommandApproval),
// because that is the single choke point every tool execution must pass to
// obtain a token. Enforcing at execution time instead would let a caller
// stockpile tokens while under budget and spend them after.

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Default budget limits. These are the backend's authoritative values; the
// renderer displays them but cannot change them.
const (
	// defaultToolBudgetCalls matches the historical frontend MAX_TOOL_CALLS so
	// the enforced ceiling is not a surprise regression for existing users.
	defaultToolBudgetCalls = 20
	// defaultToolBudgetWindow bounds an epoch in wall-clock time. A session
	// that stays under the call ceiling for hours is still a runaway agent.
	defaultToolBudgetWindow = 30 * time.Minute
)

// ToolBudgetStatus is the renderer-visible budget state (GOAL-P1-02).
//
// This is the single source of truth the frontend must render. It is
// deliberately read-only: there is no setter, so a compromised renderer can
// display a wrong number but cannot raise the ceiling.
type ToolBudgetStatus struct {
	// Epoch increments each time the user explicitly opens a new budget.
	// Capability tokens are bound to the epoch that issued them.
	Epoch uint64 `json:"epoch"`
	// Spent is the number of capabilities issued in this epoch.
	Spent int `json:"spent"`
	// Limit is the maximum number of capabilities this epoch may issue.
	Limit int `json:"limit"`
	// Remaining is Limit-Spent, floored at zero.
	Remaining int `json:"remaining"`
	// Exhausted reports that no further capability will be issued until the
	// user opens a new epoch.
	Exhausted bool `json:"exhausted"`
	// StartedAt is when this epoch opened.
	StartedAt time.Time `json:"startedAt"`
	// ExpiresAt is when this epoch stops issuing capabilities regardless of
	// remaining call count.
	ExpiresAt time.Time `json:"expiresAt"`
	// TimedOut reports that the epoch's wall-clock window elapsed.
	TimedOut bool `json:"timedOut"`
}

// toolBudget tracks one budget epoch.
//
// All fields are guarded by mu. Reserve performs check-and-increment under a
// single lock hold, which is what makes concurrent approval requests unable to
// overshoot the ceiling: two goroutines racing at spent==limit-1 serialize, and
// exactly one of them observes room.
type toolBudget struct {
	mu        sync.Mutex
	epoch     uint64
	spent     int
	limit     int
	startedAt time.Time
	window    time.Duration
}

func newToolBudget() *toolBudget {
	return &toolBudget{
		epoch:     1,
		limit:     defaultToolBudgetCalls,
		startedAt: time.Now(),
		window:    defaultToolBudgetWindow,
	}
}

// reserve consumes one unit of budget and returns the epoch it was charged to.
//
// Retry/cancel semantics (GOAL-P1-02 execution point 4): a reservation is
// charged when the capability is issued and is never refunded. A failed or
// cancelled execution still consumed a reservation. Refunding would make retry
// a free operation and turn the ceiling into a suggestion — a caller could loop
// "request, fail, retry" without bound.
func (b *toolBudget) reserve() (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.window > 0 && time.Since(b.startedAt) >= b.window {
		return 0, fmt.Errorf(
			"agent tool budget epoch %d expired after %s; start a new budget to continue: %w",
			b.epoch, b.window, ErrAgentBudgetExhausted,
		)
	}
	if b.spent >= b.limit {
		return 0, fmt.Errorf(
			"agent tool budget exhausted (%d/%d calls in epoch %d); start a new budget to continue: %w",
			b.spent, b.limit, b.epoch, ErrAgentBudgetExhausted,
		)
	}
	b.spent++
	return b.epoch, nil
}

// precheck reports whether a reservation would currently succeed, without
// consuming anything.
//
// This exists purely so a caller can refuse before prompting the user. It is
// explicitly NOT an enforcement point: two concurrent callers can both pass it
// and only one may survive `reserve`. Any caller that treats a passing
// precheck as permission is wrong.
func (b *toolBudget) precheck() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.window > 0 && time.Since(b.startedAt) >= b.window {
		return fmt.Errorf(
			"agent tool budget epoch %d expired after %s; start a new budget to continue: %w",
			b.epoch, b.window, ErrAgentBudgetExhausted,
		)
	}
	if b.spent >= b.limit {
		return fmt.Errorf(
			"agent tool budget exhausted (%d/%d calls in epoch %d); start a new budget to continue: %w",
			b.spent, b.limit, b.epoch, ErrAgentBudgetExhausted,
		)
	}
	return nil
}

// currentEpoch reports the active epoch without consuming budget.
func (b *toolBudget) currentEpoch() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.epoch
}

// status snapshots the budget for display.
func (b *toolBudget) status() ToolBudgetStatus {
	b.mu.Lock()
	defer b.mu.Unlock()

	remaining := b.limit - b.spent
	if remaining < 0 {
		remaining = 0
	}
	timedOut := b.window > 0 && time.Since(b.startedAt) >= b.window
	return ToolBudgetStatus{
		Epoch:     b.epoch,
		Spent:     b.spent,
		Limit:     b.limit,
		Remaining: remaining,
		Exhausted: b.spent >= b.limit || timedOut,
		StartedAt: b.startedAt,
		ExpiresAt: b.startedAt.Add(b.window),
		TimedOut:  timedOut,
	}
}

// newEpoch opens a fresh budget and invalidates every capability bound to the
// previous epoch. Returns the new epoch number.
func (b *toolBudget) newEpoch(limit int) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.epoch++
	b.spent = 0
	if limit > 0 {
		b.limit = limit
	}
	b.startedAt = time.Now()
	return b.epoch
}

// ---------------------------------------------------------------------------
// AgentService surface
// ---------------------------------------------------------------------------

// ensureBudget lazily initializes the budget. AgentService is constructed as a
// bare struct literal in several tests, so a nil budget must not panic.
func (s *AgentService) ensureBudget() *toolBudget {
	s.budgetInit.Do(func() {
		if s.budget == nil {
			s.budget = newToolBudget()
		}
	})
	return s.budget
}

// GetToolBudget returns the authoritative budget state for display.
//
// The renderer must derive its counter from this rather than maintaining an
// independent count: a second source of truth drifts, and the drift is exactly
// what made the old frontend ceiling meaningless.
func (s *AgentService) GetToolBudget() ToolBudgetStatus {
	return s.ensureBudget().status()
}

// StartNewToolBudgetEpoch opens a new budget epoch after the user explicitly
// asks to continue (GOAL-P1-02 execution point 3).
//
// This is the only way past an exhausted budget, it is always user-initiated,
// and it is audit-logged. Capabilities issued under the previous epoch stop
// being redeemable, so a token stockpiled before exhaustion cannot be spent
// after the reset.
func (s *AgentService) StartNewToolBudgetEpoch(limit int) ToolBudgetStatus {
	budget := s.ensureBudget()
	previous := budget.status()
	epoch := budget.newEpoch(limit)

	s.auditEvent("agent tool budget epoch opened",
		"previousEpoch", previous.Epoch,
		"previousSpent", previous.Spent,
		"previousLimit", previous.Limit,
		"newEpoch", epoch,
		"newLimit", budget.status().Limit,
	)
	return budget.status()
}

// auditEvent writes a non-exec audit record.
//
// The existing `audit` helper is exec-specific (it takes an ExecResult), but
// budget epoch changes must also be auditable per execution point 3. This
// mirrors `audit`'s fallback behaviour: the dedicated audit log when available,
// otherwise the default logger, so an unopenable log file never silently
// discards the record.
func (s *AgentService) auditEvent(msg string, keyvals ...any) {
	if s.auditLogger != nil {
		s.auditLogger.Info(msg, keyvals...)
		return
	}
	slog.Default().Info(msg, keyvals...)
}
