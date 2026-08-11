package services

// GOAL-P1-03 tests — DAP step-in target pass-through.
//
// Baseline defect: `DebugService.StepIn()` took no arguments, so a selected
// target could not reach the adapter. The frontend fetched targets, showed a
// menu, then called the plain step-in and discarded the choice. These tests lock
// the contract: the chosen ID must appear in `stepIn.arguments.targetId`, a
// target from a previous stop must be refused, and adapters without
// stepInTargets support must not regress.

import (
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// AC 1 — the selected ID reaches stepIn.arguments.targetId
// ---------------------------------------------------------------------------

func TestStepInWithTargetSendsTargetIDToAdapter(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	set, err := d.StepInTargetsForStop(1)
	if err != nil {
		t.Fatalf("StepInTargetsForStop: %v", err)
	}
	if !set.Supported {
		t.Fatal("mock advertises supportsStepInTargetsRequest, want Supported=true")
	}
	if len(set.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(set.Targets))
	}
	if set.StopSequence == 0 {
		t.Fatal("StopSequence = 0; a stopped session must report a non-zero sequence")
	}

	// Pick the SECOND target. Picking the first would pass even if the ID were
	// dropped, because a default step-in enters the first target.
	second := set.Targets[1]
	if second.ID != 2 {
		t.Fatalf("target[1].ID = %d, want 2", second.ID)
	}
	if err := d.StepInWithTarget(second.ID, set.StopSequence); err != nil {
		t.Fatalf("StepInWithTarget: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if !mock.stepInTargetPresent {
		t.Fatal("stepIn.arguments.targetId was absent; the selected target was discarded")
	}
	if mock.stepInTargetID != 2 {
		t.Fatalf("stepIn.arguments.targetId = %d, want 2", mock.stepInTargetID)
	}
}

// ---------------------------------------------------------------------------
// AC 4 (default path) — plain StepIn must NOT send a targetId
// ---------------------------------------------------------------------------

func TestPlainStepInOmitsTargetID(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	if err := d.StepIn(); err != nil {
		t.Fatalf("StepIn: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.stepInCalls == 0 {
		t.Fatal("adapter received no stepIn request")
	}
	// Sending targetId:0 would be wrong: DAP treats a present targetId as a
	// real selection, and 0 is not a valid target.
	if mock.stepInTargetPresent {
		t.Fatalf("plain StepIn sent targetId=%d; it must be omitted entirely", mock.stepInTargetID)
	}
}

// ---------------------------------------------------------------------------
// AC 2 — a target from a previous stop is refused
// ---------------------------------------------------------------------------

func TestStepInWithTargetRejectsStaleStopSequence(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	set, err := d.StepInTargetsForStop(1)
	if err != nil {
		t.Fatalf("StepInTargetsForStop: %v", err)
	}
	staleSequence := set.StopSequence

	// Step once. The mock replies then emits a fresh `stopped` event, so the
	// session advances to a new stop — exactly the situation where the old menu's
	// IDs no longer describe what the user saw.
	if err := d.StepIn(); err != nil {
		t.Fatalf("StepIn: %v", err)
	}
	waitStopSequenceAdvanced(t, d, staleSequence, 3*time.Second)

	err = d.StepInWithTarget(2, staleSequence)
	if err == nil {
		t.Fatal("stale target was accepted; it must be refused after a new stop")
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
}

func TestStepInWithTargetAcceptsCurrentStopSequence(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	// Advance one stop first, then enumerate. This proves the check compares
	// against the *live* sequence rather than passing only for the first stop.
	first := d.CurrentStopSequence()
	if err := d.StepIn(); err != nil {
		t.Fatalf("StepIn: %v", err)
	}
	waitStopSequenceAdvanced(t, d, first, 3*time.Second)

	set, err := d.StepInTargetsForStop(1)
	if err != nil {
		t.Fatalf("StepInTargetsForStop: %v", err)
	}
	if set.StopSequence == first {
		t.Fatal("StopSequence did not advance after a step")
	}
	if err := d.StepInWithTarget(1, set.StopSequence); err != nil {
		t.Fatalf("current-sequence target was refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Input validation
// ---------------------------------------------------------------------------

func TestStepInWithTargetRejectsNonPositiveTargetID(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	sequence := d.CurrentStopSequence()
	for _, id := range []int{0, -1} {
		err := d.StepInWithTarget(id, sequence)
		if err == nil {
			t.Fatalf("targetId=%d was accepted; it is not a valid DAP target", id)
		}
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("targetId=%d error = %v, want ErrInvalidInput", id, err)
		}
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.stepInCalls != 0 {
		t.Fatalf("invalid target still issued %d stepIn request(s)", mock.stepInCalls)
	}
}

func TestStepInTargetsForStopRequiresSession(t *testing.T) {
	d := NewDebugService()
	if _, err := d.StepInTargetsForStop(1); err == nil {
		t.Fatal("expected an error with no debug session")
	}
	if err := d.StepInWithTarget(1, 1); err == nil {
		t.Fatal("expected an error with no debug session")
	}
	if seq := d.CurrentStopSequence(); seq != 0 {
		t.Fatalf("CurrentStopSequence = %d with no session, want 0", seq)
	}
}

// ---------------------------------------------------------------------------
// AC 3 — adapters without stepInTargets must not regress
// ---------------------------------------------------------------------------

func TestStepInTargetsForStopReportsUnsupportedWithoutError(t *testing.T) {
	// An adapter that does not implement stepInTargets answers with an error.
	// That must surface as Supported=false, not as a failed call: the UI needs to
	// know "no menu" rather than "something broke".
	mock, addr := startMockDAPNoStepInTargets(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	set, err := d.StepInTargetsForStop(1)
	if err != nil {
		t.Fatalf("StepInTargetsForStop must not fail on an unsupported adapter: %v", err)
	}
	if set.Supported {
		t.Fatal("Supported=true for an adapter that rejects stepInTargets")
	}
	if len(set.Targets) != 0 {
		t.Fatalf("expected no targets, got %d", len(set.Targets))
	}
	if set.StopSequence == 0 {
		t.Fatal("StopSequence must still be reported so the default step-in stays checked")
	}

	// The default path must still work on such an adapter.
	if err := d.StepIn(); err != nil {
		t.Fatalf("default StepIn regressed on unsupported adapter: %v", err)
	}
}

func TestStepInTargetsPreservesLegacyBareListAPI(t *testing.T) {
	// The older StepInTargets is still used by existing callers and bindings.
	// Adding the stop-aware variant must not change its behaviour.
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	targets, err := d.StepInTargets(1)
	if err != nil {
		t.Fatalf("StepInTargets: %v", err)
	}
	if len(targets) != 2 || targets[0].ID != 1 || targets[1].ID != 2 {
		t.Fatalf("legacy StepInTargets changed shape: %+v", targets)
	}
}

// ---------------------------------------------------------------------------
// Not-stopped guard
// ---------------------------------------------------------------------------

func TestStepInTargetOperationsRequireStoppedSession(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	sequence := d.CurrentStopSequence()

	// Force the session to a running state, as a resume would.
	owner := d.activeSession()
	if owner == nil {
		t.Fatal("no active session")
	}
	owner.mu.Lock()
	owner.stopped = false
	owner.mu.Unlock()

	if _, err := d.StepInTargetsForStop(1); err == nil {
		t.Fatal("enumerating targets while running was allowed")
	}
	if err := d.StepInWithTarget(1, sequence); err == nil {
		t.Fatal("targeted step-in while running was allowed")
	}
	if seq := d.CurrentStopSequence(); seq != 0 {
		t.Fatalf("CurrentStopSequence = %d while running, want 0", seq)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// waitStopSequenceAdvanced blocks until the session's stop sequence moves past
// `from`, so tests do not race the adapter's `stopped` event.
func waitStopSequenceAdvanced(t *testing.T, d *DebugService, from uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if seq := d.CurrentStopSequence(); seq != 0 && seq != from {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("stop sequence did not advance past %d within %s", from, timeout)
}
