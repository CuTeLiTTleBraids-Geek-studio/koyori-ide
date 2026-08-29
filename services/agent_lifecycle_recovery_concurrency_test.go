package services

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

type lifecycleRecoveryDispositionResult struct {
	row agentcore.Session
	err error
}

func newLifecycleRecoveryConcurrencyFixture(
	t *testing.T,
) (*AgentService, *AgentLifecycle, string, string) {
	t.Helper()
	configDir := t.TempDir()
	workspaceRoot := t.TempDir()
	id := seedDurableLifecycleRecoveryRow(t, configDir, workspaceRoot, agentcore.SessionWorkflow)
	agent := newLifecycleTestAgentAtWorkspace(t, workspaceRoot)
	t.Cleanup(func() { _ = agent.Close() })
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
		configDir,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle restart: %v", err)
	}
	row, err := lifecycle.GetByID(id)
	if err != nil || row.Recovery != agentcore.SessionRecoveryRequired || row.Owner == nil || row.Owner.RuntimeID != "" {
		t.Fatalf("recovery fixture row=%+v, err=%v", row, err)
	}
	return agent, lifecycle, id, workspaceRoot
}

func raceLifecycleRecoveryDisposition(
	lifecycle *AgentLifecycle,
	id string,
	other func() error,
) (lifecycleRecoveryDispositionResult, error) {
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var disposition lifecycleRecoveryDispositionResult
	var otherErr error
	go func() {
		defer wait.Done()
		<-start
		disposition.row, disposition.err = lifecycle.applyRecoveryDisposition(
			agentcore.SessionWorkflow,
			id,
			agentcore.RecoveryDispositionDiscard,
		)
	}()
	go func() {
		defer wait.Done()
		<-start
		otherErr = other()
	}()
	close(start)
	wait.Wait()
	return disposition, otherErr
}

func assertLifecycleRecoveryAuthorityRevoked(
	t *testing.T,
	agent *AgentService,
	lifecycle *AgentLifecycle,
	id string,
) {
	t.Helper()
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(id) || runtime.IsSessionActive(id) {
		t.Fatalf("recovery race registered runtime authority for %q", id)
	}
	if mapped := lifecycle.runtimeSessionID(agentcore.SessionWorkflow, id); mapped != id {
		t.Fatalf("recovery race installed runtime owner mapping %q -> %q", id, mapped)
	}
}

func assertLifecycleRecoveryTerminalOrQuarantined(t *testing.T, row agentcore.Session) {
	t.Helper()
	if row.Owner == nil || row.Owner.RuntimeID != "" {
		t.Fatalf("recovery race retained runtime owner metadata: %+v", row.Owner)
	}
	switch {
	case row.Status == agentcore.SessionCompleted &&
		row.Recovery == agentcore.SessionRecoveryNone &&
		row.RecoveryDisposition == agentcore.RecoveryDispositionDiscard &&
		row.CompletedAt != nil:
		return
	case row.Status == agentcore.SessionPaused &&
		row.Recovery == agentcore.SessionRecoveryRequired &&
		row.RecoveryDisposition == "" &&
		row.CompletedAt == nil:
		return
	default:
		t.Fatalf("recovery race left a hybrid lifecycle state: %+v", row)
	}
}

func TestLifecycleRecoveryDispositionConcurrentAuthorityRevocationMatrix(t *testing.T) {
	for _, test := range []struct {
		name              string
		operation         func(*testing.T, *AgentService, *AgentLifecycle, string, string) error
		allowQuarantine   bool
		checkOperationErr func(error) bool
	}{
		{
			name: "workspace-reset",
			operation: func(_ *testing.T, agent *AgentService, _ *AgentLifecycle, _, nextWorkspace string) error {
				if err := agent.workspaceContext.Set(nextWorkspace); err != nil {
					return err
				}
				return agent.configureWorkspaceRoot(nextWorkspace)
			},
			allowQuarantine:   true,
			checkOperationErr: func(err error) bool { return err == nil },
		},
		{
			name: "complete",
			operation: func(_ *testing.T, _ *AgentService, lifecycle *AgentLifecycle, id, _ string) error {
				return lifecycle.Complete(agentcore.SessionWorkflow, id)
			},
			checkOperationErr: func(err error) bool {
				return errors.Is(err, agentcore.ErrSessionRecoveryRequired) ||
					errors.Is(err, agentcore.ErrInvalidSessionTransition)
			},
		},
		{
			name: "close",
			operation: func(_ *testing.T, _ *AgentService, lifecycle *AgentLifecycle, id, _ string) error {
				return lifecycle.CloseByID(id)
			},
			checkOperationErr: func(err error) bool {
				return err == nil || errors.Is(err, agentcore.ErrSessionRecoveryRequired)
			},
		},
		{
			name: "bind-owner-usage-path",
			operation: func(_ *testing.T, _ *AgentService, lifecycle *AgentLifecycle, id, _ string) error {
				now := time.Now()
				return lifecycle.ensureTrustedUsageSession(agentcore.UsageRecord{
					SessionID: id,
					UnitKind:  agentcore.UsageUnitWorkflow,
					Operation: "workflow.recovery-race",
					CostBasis: agentcore.CostNotApplicable,
					StartedAt: now, CompletedAt: now,
				})
			},
			checkOperationErr: func(err error) bool {
				return errors.Is(err, agentcore.ErrSessionRecoveryRequired) ||
					errors.Is(err, agentcore.ErrInvalidSessionTransition) ||
					errors.Is(err, agentcore.ErrUnknownSession)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent, lifecycle, id, _ := newLifecycleRecoveryConcurrencyFixture(t)
			nextWorkspace := t.TempDir()
			disposition, operationErr := raceLifecycleRecoveryDisposition(lifecycle, id, func() error {
				return test.operation(t, agent, lifecycle, id, nextWorkspace)
			})
			if !test.checkOperationErr(operationErr) {
				t.Fatalf("concurrent operation error=%v", operationErr)
			}
			if disposition.err != nil && !test.allowQuarantine {
				t.Fatalf("ApplyRecoveryDisposition: %v", disposition.err)
			}
			final, err := lifecycle.GetByID(id)
			if err != nil {
				t.Fatalf("GetByID final row: %v", err)
			}
			assertLifecycleRecoveryTerminalOrQuarantined(t, final)
			if !test.allowQuarantine && final.Recovery == agentcore.SessionRecoveryRequired {
				t.Fatalf("%s left recovery row quarantined after disposition completed: %+v", test.name, final)
			}
			if disposition.err == nil {
				if disposition.row.Status != agentcore.SessionCompleted ||
					disposition.row.RecoveryDisposition != agentcore.RecoveryDispositionDiscard {
					t.Fatalf("disposition result did not terminalize row: %+v", disposition.row)
				}
			} else if final.Recovery != agentcore.SessionRecoveryRequired {
				t.Fatalf("failed disposition error=%v but row was not quarantined: %+v", disposition.err, final)
			}
			assertLifecycleRecoveryAuthorityRevoked(t, agent, lifecycle, id)
		})
	}
}

func TestLifecycleRecoveryDispositionWorkspaceGenerationRaceQuarantinesStaleOwner(t *testing.T) {
	agent, lifecycle, id, _ := newLifecycleRecoveryConcurrencyFixture(t)
	nextWorkspace := t.TempDir()

	// ProjectService publishes WorkspaceContext before invoking the Agent root
	// setter. Hold lifecycle transitions after that first production step so
	// reset and disposition contend with the old owner claim already stale.
	lifecycle.transitionMu.Lock()
	if err := agent.workspaceContext.Set(nextWorkspace); err != nil {
		lifecycle.transitionMu.Unlock()
		t.Fatalf("set shared workspace context: %v", err)
	}
	resetDone := make(chan error, 1)
	go func() {
		resetDone <- agent.configureWorkspaceRoot(nextWorkspace)
	}()
	dispositionDone := make(chan lifecycleRecoveryDispositionResult, 1)
	go func() {
		row, err := lifecycle.applyRecoveryDisposition(
			agentcore.SessionWorkflow,
			id,
			agentcore.RecoveryDispositionDiscard,
		)
		dispositionDone <- lifecycleRecoveryDispositionResult{row: row, err: err}
	}()
	lifecycle.transitionMu.Unlock()

	if err := <-resetDone; err != nil {
		t.Fatalf("configureWorkspaceRoot: %v", err)
	}
	disposition := <-dispositionDone
	if disposition.err == nil {
		t.Fatalf("stale workspace owner was accepted after generation changed: %+v", disposition.row)
	}
	row, err := lifecycle.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID quarantined row: %v", err)
	}
	assertLifecycleRecoveryTerminalOrQuarantined(t, row)
	if row.Recovery != agentcore.SessionRecoveryRequired || row.RecoveryDisposition != "" {
		t.Fatalf("stale workspace owner did not remain quarantined: %+v", row)
	}
	assertLifecycleRecoveryAuthorityRevoked(t, agent, lifecycle, id)
}

func TestAgentRecoveryDispatcherDiscardRacesWorkspaceSwitch(t *testing.T) {
	agent, lifecycle, id, _ := newLifecycleRecoveryConcurrencyFixture(t)
	entries, err := lifecycle.PendingRecoveryDispositions()
	if err != nil || len(entries) != 1 {
		t.Fatalf("PendingRecoveryDispositions = %+v, err=%v", entries, err)
	}
	handle := entries[0].Handle
	nextWorkspace := t.TempDir()

	start := make(chan struct{})
	resetDone := make(chan error, 1)
	dispositionDone := make(chan error, 1)
	go func() {
		<-start
		if err := agent.workspaceContext.Set(nextWorkspace); err != nil {
			resetDone <- err
			return
		}
		resetDone <- agent.configureWorkspaceRoot(nextWorkspace)
	}()
	go func() {
		<-start
		_, dispositionErr := lifecycle.DispatchRecoveryDisposition(AgentRecoveryDispositionRequest{
			Handle: handle, Disposition: "discard",
		})
		dispositionDone <- dispositionErr
	}()
	close(start)

	if resetErr := <-resetDone; resetErr != nil {
		t.Fatalf("configureWorkspaceRoot: %v", resetErr)
	}
	dispositionErr := <-dispositionDone
	if dispositionErr != nil && !errors.Is(dispositionErr, ErrNotAllowed) {
		t.Fatalf("DispatchRecoveryDisposition error = %v, want nil or ErrNotAllowed", dispositionErr)
	}
	row, err := lifecycle.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID final row: %v", err)
	}
	assertLifecycleRecoveryTerminalOrQuarantined(t, row)
	if dispositionErr == nil && row.RecoveryDisposition != agentcore.RecoveryDispositionDiscard {
		t.Fatalf("successful public disposition did not persist discard: %+v", row)
	}
	if dispositionErr != nil && row.Recovery != agentcore.SessionRecoveryRequired {
		t.Fatalf("rejected public disposition did not leave quarantine: %+v", row)
	}
	assertLifecycleRecoveryAuthorityRevoked(t, agent, lifecycle, id)
}
