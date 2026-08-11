package services

// debug_step_in_target.go — GOAL-P1-03: pass the selected DAP step-in target
// through to the adapter.
//
// Baseline defect: `DebugService.StepIn()` took no arguments, so there was no
// way to express "step into this overload". The frontend fetched the target list
// via StepInTargets, rendered a selection menu, and then discarded the choice —
// `DebugPanel.vue` had `onPickStepInTarget(_targetId: number)` whose body called
// the plain `debugStepIn()`, with a comment stating the backend could not accept
// a target ID. The user picked target B and the debugger stepped into A.
//
// Two things are needed to fix that honestly:
//
//  1. A request path that carries `targetId` into `stepIn.arguments`.
//  2. A way to reject a target that came from a *previous* stop. A target ID is
//     only meaningful for the stop it was enumerated during; after resume the
//     adapter's IDs may refer to nothing, or worse, to something else. The
//     session now carries a `stopSequence` that increments on every transition
//     into the stopped state, and a targeted step-in must present the sequence
//     it was enumerated under.

import (
	"encoding/json"
	"fmt"
)

// StepInTargetSet is a step-in target list bound to the stop that produced it.
//
// The caller must pass StopSequence back to StepInWithTarget. That is what makes
// a stale menu detectable: after a resume/step the session's sequence advances,
// so a selection made against the old list is refused instead of being applied
// to a different program state.
type StepInTargetSet struct {
	// Targets is the adapter-reported target list. Empty means the adapter has
	// no alternatives for this frame and the default step-in should be used.
	Targets []StepInTarget `json:"targets"`
	// StopSequence identifies the stop these targets were enumerated during.
	StopSequence uint64 `json:"stopSequence"`
	// Supported reports whether the adapter implements stepInTargets at all.
	// When false, Targets is empty and only the default step-in is available.
	Supported bool `json:"supported"`
}

// CurrentStopSequence returns the active session's stop sequence.
//
// Returns 0 when there is no session or the session is running: 0 is never a
// valid enumerated sequence because the counter increments before first use, so
// a caller cannot accidentally pass a "valid-looking" zero.
func (d *DebugService) CurrentStopSequence() uint64 {
	owner := d.activeSession()
	if owner == nil {
		return 0
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.stopped {
		return 0
	}
	return owner.stopSequence
}

// StepInTargetsForStop enumerates step-in targets together with the stop
// sequence they belong to (GOAL-P1-03).
//
// This exists alongside the older StepInTargets, which returns a bare list. A
// bare list cannot be validated later, and validating is the whole point: the
// menu the user clicks must be provably the menu for the current stop.
func (d *DebugService) StepInTargetsForStop(frameID int) (StepInTargetSet, error) {
	owner := d.activeSession()
	if owner == nil {
		return StepInTargetSet{}, fmt.Errorf("no debug session")
	}

	owner.mu.Lock()
	stopped := owner.stopped
	sequence := owner.stopSequence
	cdp, mode := owner.cdp, owner.mode
	owner.mu.Unlock()

	if !stopped {
		return StepInTargetSet{}, fmt.Errorf("session is not stopped: %w", ErrNotAllowed)
	}

	// Node/browser CDP has no stepInTargets equivalent. Report that plainly so
	// the UI shows no menu rather than an empty one, and record the sequence so
	// the caller's later default step-in is still staleness-checked.
	if isCDPDebugMode(mode) && cdp != nil {
		return StepInTargetSet{Targets: nil, StopSequence: sequence, Supported: false}, nil
	}

	body, err := d.dapRequestBody("stepInTargets", map[string]interface{}{
		"frameId": frameID,
	})
	if err != nil {
		// An adapter that does not implement the request answers with an error.
		// That is not a failure of this call — it is the answer "unsupported".
		return StepInTargetSet{Targets: nil, StopSequence: sequence, Supported: false}, nil
	}
	var resp struct {
		Targets []StepInTarget `json:"targets"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return StepInTargetSet{}, fmt.Errorf("decode dap stepInTargets response: %w", err)
	}
	return StepInTargetSet{
		Targets:      resp.Targets,
		StopSequence: sequence,
		Supported:    true,
	}, nil
}

// StepInWithTarget steps into a specific target (GOAL-P1-03).
//
// stopSequence must be the value returned by the StepInTargetsForStop call that
// produced the menu. A mismatch means the program advanced between enumeration
// and selection, so the target ID no longer refers to what the user saw; the
// request is refused rather than applied to a different state.
//
// Node/browser CDP cannot honour a target ID. Rather than silently ignoring it —
// the exact dishonesty this goal exists to remove — the caller is told the
// target was not applied, so the UI never offers a choice it cannot deliver.
func (d *DebugService) StepInWithTarget(targetID int, stopSequence uint64) error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	if targetID <= 0 {
		return fmt.Errorf("step-in target id must be positive: %w", ErrInvalidInput)
	}

	owner.mu.Lock()
	stopped := owner.stopped
	current := owner.stopSequence
	cdp, mode := owner.cdp, owner.mode
	owner.mu.Unlock()

	if !stopped {
		return fmt.Errorf("session is not stopped: %w", ErrNotAllowed)
	}
	if stopSequence != current {
		return fmt.Errorf(
			"step-in target is stale (enumerated at stop %d, current stop %d): %w",
			stopSequence, current, ErrInvalidInput,
		)
	}
	if isCDPDebugMode(mode) && cdp != nil {
		return fmt.Errorf(
			"the active adapter does not support step-in targets: %w",
			ErrPlatformUnsupported,
		)
	}

	return d.stepSessionWithArgs(owner, "stepIn", map[string]interface{}{
		"targetId": targetID,
	})
}
