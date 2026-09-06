package services

import (
	"errors"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

type indeterminateLifecyclePersistence struct {
	fault error
}

func (p *indeterminateLifecyclePersistence) Load() ([]agentcore.Session, error) {
	return nil, nil
}

func (p *indeterminateLifecyclePersistence) Save([]agentcore.Session) (agentcore.PersistenceCommitState, error) {
	return agentcore.PersistencePublishedDurabilityUnknown, p.fault
}

func TestLazyUsageSessionIndeterminatePublicationDoesNotIssueRuntimeAuthority(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	fault := errors.New("injected post-publication durability failure")
	lifecycle.sessions, err = agentcore.NewPersistentSessionStore(
		&indeterminateLifecyclePersistence{fault: fault}, time.Now,
	)
	if err != nil {
		t.Fatalf("NewPersistentSessionStore: %v", err)
	}

	sessionID := "workflow:lazy-indeterminate-publication"
	now := time.Now()
	err = lifecycle.Record(agentcore.UsageRecord{
		SessionID: sessionID, UnitKind: agentcore.UsageUnitWorkflow,
		Operation: "workflow.lazy", CostBasis: agentcore.CostNotApplicable,
		StartedAt: now, CompletedAt: now.Add(time.Second), Success: true,
	})
	if !errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) || !errors.Is(err, fault) {
		t.Fatalf("Record error = %v, want indeterminate publication fault", err)
	}
	runtime, runtimeErr := agent.coreRuntime()
	if runtimeErr != nil {
		t.Fatalf("coreRuntime: %v", runtimeErr)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("runtime authority was issued after indeterminate owner publication")
	}
	row, getErr := lifecycle.GetByID(sessionID)
	if getErr != nil {
		t.Fatalf("GetByID after published-unknown owner row: %v", getErr)
	}
	if row.Owner == nil || row.Owner.Domain != "workflow-service" || row.Owner.RuntimeID != sessionID || row.Owner.WorkspaceFingerprint == "" {
		t.Fatalf("published-unknown row omitted its atomic owner claim: %+v", row.Owner)
	}
	if records := permission.usageRecordsSnapshot(); len(records) != 0 {
		t.Fatalf("usage was metered after indeterminate owner publication: %+v", records)
	}
	if err := lifecycle.sessions.BindOwner(sessionID, *row.Owner); !errors.Is(err, agentcore.ErrSessionPersistencePoisoned) ||
		!errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) {
		t.Fatalf("mutation after indeterminate publication = %v, want poisoned persistence", err)
	}
	if err := lifecycle.Record(agentcore.UsageRecord{
		SessionID: sessionID, UnitKind: agentcore.UsageUnitWorkflow,
		Operation: "workflow.retry", CostBasis: agentcore.CostNotApplicable,
		StartedAt: now, CompletedAt: now.Add(time.Second), Success: true,
	}); !errors.Is(err, agentcore.ErrUnknownSession) {
		t.Fatalf("retry after indeterminate publication = %v, want no runtime authority", err)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("retry against poisoned persistence issued runtime authority")
	}
}

func TestUsageObservationDoesNotCreateOwnerlessLifecycleRow(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	sessionID := "workflow:runtime-without-durable-owner"
	now := time.Now()
	if err := lifecycle.observeUsageSession(agentcore.UsageRecord{
		UnitID: "usage-without-owner", SessionID: sessionID,
		UnitKind: agentcore.UsageUnitWorkflow, Operation: "workflow.observe",
		CostBasis: agentcore.CostNotApplicable,
		StartedAt: now, CompletedAt: now.Add(time.Second), Success: true,
	}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	if _, err := lifecycle.GetByID(sessionID); !errors.Is(err, agentcore.ErrSessionNotFound) {
		t.Fatalf("usage observation created an ownerless lifecycle row: %v", err)
	}
}

func TestCompletedLifecycleAllowsOnlyTrustedFailureUsageObservation(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if _, err := lifecycle.Begin(agentcore.SessionChat, "terminal-usage"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := lifecycle.Complete(agentcore.SessionChat, "terminal-usage"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	now := time.Now()
	base := agentcore.UsageRecord{
		SessionID: lifecycleSessionID(agentcore.SessionChat, "terminal-usage"),
		UnitKind:  agentcore.UsageUnitAI, Operation: "chat.terminal",
		CostBasis: agentcore.CostNotApplicable,
		StartedAt: now, CompletedAt: now.Add(time.Second),
		Success: true,
	}
	if err := lifecycle.Record(base); !errors.Is(err, agentcore.ErrInvalidSessionTransition) {
		t.Fatalf("successful terminal usage = %v, want invalid transition", err)
	}
	base.UnitID = "terminal-failure-observation"
	base.Success = false
	base.Error = "checkpoint failed"
	if err := lifecycle.Record(base); err != nil {
		t.Fatalf("failed terminal usage observation: %v", err)
	}
	if records := permission.usageRecordsSnapshot(); len(records) != 1 || records[0].Success {
		t.Fatalf("terminal failure usage records = %+v, want one failed record", records)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered("chat:terminal-usage") {
		t.Fatal("terminal usage observation restored runtime authority")
	}
}
