package services

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

type lifecycleCommitFaultPersistence struct {
	mu sync.Mutex

	rows      []agentcore.Session
	saveCalls int

	nextState agentcore.PersistenceCommitState
	nextErr   error
	entered   chan struct{}
	release   chan struct{}
}

func (p *lifecycleCommitFaultPersistence) Load() ([]agentcore.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneLifecycleTestRows(p.rows), nil
}

func (p *lifecycleCommitFaultPersistence) Save(rows []agentcore.Session) (agentcore.PersistenceCommitState, error) {
	p.mu.Lock()
	p.saveCalls++
	state, err := p.nextState, p.nextErr
	entered, release := p.entered, p.release
	p.nextState, p.nextErr, p.entered, p.release = agentcore.PersistenceDurable, nil, nil, nil
	p.mu.Unlock()

	if entered != nil {
		close(entered)
		<-release
	}
	if state == agentcore.PersistenceNotPublished && err == nil {
		state = agentcore.PersistenceDurable
	}
	if state != agentcore.PersistenceNotPublished {
		p.mu.Lock()
		p.rows = cloneLifecycleTestRows(rows)
		p.mu.Unlock()
	}
	return state, err
}

func (p *lifecycleCommitFaultPersistence) saveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saveCalls
}

func (p *lifecycleCommitFaultPersistence) failNext(state agentcore.PersistenceCommitState, err error) {
	p.mu.Lock()
	p.nextState, p.nextErr = state, err
	p.mu.Unlock()
}

func (p *lifecycleCommitFaultPersistence) blockNext() (<-chan struct{}, func()) {
	p.mu.Lock()
	p.entered = make(chan struct{})
	p.release = make(chan struct{})
	entered, release := p.entered, p.release
	p.mu.Unlock()
	return entered, func() { close(release) }
}

func cloneLifecycleTestRows(rows []agentcore.Session) []agentcore.Session {
	data, err := json.Marshal(rows)
	if err != nil {
		panic(err)
	}
	var cloned []agentcore.Session
	if err := json.Unmarshal(data, &cloned); err != nil {
		panic(err)
	}
	return cloned
}

func newFaultInjectedLifecycle(t *testing.T) (*AgentService, *AgentLifecycle, *lifecycleCommitFaultPersistence) {
	t.Helper()
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	persistence := &lifecycleCommitFaultPersistence{nextState: agentcore.PersistenceDurable}
	lifecycle.sessions, err = agentcore.NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("NewPersistentSessionStore: %v", err)
	}
	return agent, lifecycle, persistence
}

func createFaultInjectedChat(t *testing.T, agent *AgentService) string {
	t.Helper()
	id, err := agent.createAgentSessionTrusted(string(agentcore.SessionChat))
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	return id
}

func TestLifecyclePublishedUnknownTransitionsDoNotRestoreRuntimeAuthority(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*testing.T, *AgentLifecycle, string)
		transition func(*AgentLifecycle, string) error
		wantStatus agentcore.SessionStatus
		wantActive bool
	}{
		{
			name:       "pause",
			transition: func(l *AgentLifecycle, id string) error { return l.Pause(agentcore.SessionChat, id) },
			wantStatus: agentcore.SessionPaused,
			wantActive: false,
		},
		{
			name: "fail",
			transition: func(l *AgentLifecycle, id string) error {
				return l.Fail(agentcore.SessionChat, id, errors.New("failed"))
			},
			wantStatus: agentcore.SessionFailed,
			wantActive: false,
		},
		{
			name:       "complete",
			transition: func(l *AgentLifecycle, id string) error { return l.Complete(agentcore.SessionChat, id) },
			wantStatus: agentcore.SessionCompleted,
			wantActive: false,
		},
		{
			name: "close-paused",
			prepare: func(t *testing.T, l *AgentLifecycle, id string) {
				t.Helper()
				if err := l.Pause(agentcore.SessionChat, id); err != nil {
					t.Fatalf("Pause before CloseByID: %v", err)
				}
			},
			transition: func(l *AgentLifecycle, id string) error { return l.CloseByID(id) },
			wantStatus: agentcore.SessionCompleted,
			wantActive: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
			id := createFaultInjectedChat(t, agent)
			if test.prepare != nil {
				test.prepare(t, lifecycle, id)
			}
			fault := errors.New("injected post-publication directory sync failure")
			persistence.failNext(agentcore.PersistencePublishedDurabilityUnknown, fault)
			err := test.transition(lifecycle, id)
			if !errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) || !errors.Is(err, fault) {
				t.Fatalf("transition error = %v, want published-unknown fault", err)
			}
			runtime, err := agent.coreRuntime()
			if err != nil {
				t.Fatalf("coreRuntime: %v", err)
			}
			if runtime.IsSessionActive(id) != test.wantActive {
				t.Fatalf("runtime active = %v, want %v", runtime.IsSessionActive(id), test.wantActive)
			}
			if runtime.IsSessionRegistered(id) || lifecycle.logicalSessionForRuntime(id) != "" {
				t.Fatal("published-unknown row retained runtime authority or owner mapping")
			}
			row, err := lifecycle.GetByID(id)
			if err != nil || row.Status != test.wantStatus {
				t.Fatalf("retained row = %+v, err=%v; want status %s", row, err, test.wantStatus)
			}
		})
	}
}

func TestLifecycleWorkspaceResetPublishedUnknownStillRevokesRuntimeAuthority(t *testing.T) {
	agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
	id := createFaultInjectedChat(t, agent)
	fault := errors.New("injected reset post-publication directory sync failure")
	persistence.failNext(agentcore.PersistencePublishedDurabilityUnknown, fault)

	err := lifecycle.resetForWorkspaceChange()
	if !errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) || !errors.Is(err, fault) {
		t.Fatalf("resetForWorkspaceChange error = %v, want published-unknown fault", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(id) {
		t.Fatal("published-unknown workspace reset retained runtime authority")
	}
	row, err := lifecycle.GetByID(id)
	if err != nil || row.Status != agentcore.SessionCompleted {
		t.Fatalf("published reset row = %+v, err=%v", row, err)
	}
}

func TestLifecyclePrePublishFailuresRestorePriorRuntimeState(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*testing.T, *AgentLifecycle, string)
		transition func(*AgentLifecycle, string) error
		wantStatus agentcore.SessionStatus
		wantActive bool
	}{
		{
			name:       "pause",
			transition: func(l *AgentLifecycle, id string) error { return l.Pause(agentcore.SessionChat, id) },
			wantStatus: agentcore.SessionRunning,
			wantActive: true,
		},
		{
			name: "fail",
			transition: func(l *AgentLifecycle, id string) error {
				return l.Fail(agentcore.SessionChat, id, errors.New("failed"))
			},
			wantStatus: agentcore.SessionRunning,
			wantActive: true,
		},
		{
			name:       "complete",
			transition: func(l *AgentLifecycle, id string) error { return l.Complete(agentcore.SessionChat, id) },
			wantStatus: agentcore.SessionRunning,
			wantActive: true,
		},
		{
			name: "close-paused",
			prepare: func(t *testing.T, l *AgentLifecycle, id string) {
				t.Helper()
				if err := l.Pause(agentcore.SessionChat, id); err != nil {
					t.Fatalf("Pause before CloseByID: %v", err)
				}
			},
			transition: func(l *AgentLifecycle, id string) error { return l.CloseByID(id) },
			wantStatus: agentcore.SessionPaused,
			wantActive: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
			id := createFaultInjectedChat(t, agent)
			if test.prepare != nil {
				test.prepare(t, lifecycle, id)
			}
			fault := errors.New("injected pre-publication failure")
			persistence.failNext(agentcore.PersistenceNotPublished, fault)
			if err := test.transition(lifecycle, id); !errors.Is(err, fault) {
				t.Fatalf("transition error = %v, want pre-publication fault", err)
			}
			runtime, err := agent.coreRuntime()
			if err != nil {
				t.Fatalf("coreRuntime: %v", err)
			}
			if !runtime.IsSessionRegistered(id) || runtime.IsSessionActive(id) != test.wantActive {
				t.Fatalf("runtime registered/active = %v/%v, want true/%v", runtime.IsSessionRegistered(id), runtime.IsSessionActive(id), test.wantActive)
			}
			row, err := lifecycle.GetByID(id)
			if err != nil || row.Status != test.wantStatus {
				t.Fatalf("rolled-back row = %+v, err=%v; want status %s", row, err, test.wantStatus)
			}
		})
	}
}

func TestLifecycleResumePublishedUnknownKeepsRuntimeSuspended(t *testing.T) {
	agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
	id := createFaultInjectedChat(t, agent)
	if _, err := lifecycle.Checkpoint(agentcore.SessionChat, id, "resume", map[string]bool{"ready": true}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := lifecycle.Pause(agentcore.SessionChat, id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	fault := errors.New("injected resume post-publication directory sync failure")
	persistence.failNext(agentcore.PersistencePublishedDurabilityUnknown, fault)

	err := lifecycle.ResumeLatest(agentcore.SessionChat, id)
	if !errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) || !errors.Is(err, fault) {
		t.Fatalf("ResumeLatest error = %v, want published-unknown fault", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(id) || runtime.IsSessionActive(id) || lifecycle.logicalSessionForRuntime(id) != "" {
		t.Fatalf("runtime registered/active/mapped = %v/%v/%q, want revoked", runtime.IsSessionRegistered(id), runtime.IsSessionActive(id), lifecycle.logicalSessionForRuntime(id))
	}
	row, err := lifecycle.GetByID(id)
	if err != nil || row.Status != agentcore.SessionRunning {
		t.Fatalf("published resume row = %+v, err=%v", row, err)
	}
}

func TestLifecycleBeginExistingPublishedUnknownRevokesPreexistingRuntime(t *testing.T) {
	agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	id := "chat:preexisting-runtime-unknown"
	if err := runtime.RegisterSession(id); err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	fault := errors.New("injected owner post-publication directory sync failure")
	persistence.failNext(agentcore.PersistencePublishedDurabilityUnknown, fault)

	_, err = lifecycle.BeginExisting(agentcore.SessionChat, id)
	if !errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) || !errors.Is(err, fault) {
		t.Fatalf("BeginExisting error = %v, want published-unknown fault", err)
	}
	if runtime.IsSessionRegistered(id) || runtime.IsSessionActive(id) {
		t.Fatalf("preexisting runtime registered/active = %v/%v after unknown publication", runtime.IsSessionRegistered(id), runtime.IsSessionActive(id))
	}
	row, err := lifecycle.GetByID(id)
	if err != nil || row.Owner == nil || row.Owner.RuntimeID != id {
		t.Fatalf("published owner row = %+v, err=%v", row, err)
	}
}

func TestLifecycleTerminalPublishedUnknownClearsOpaqueRuntimeOwnerMapping(t *testing.T) {
	agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
	const planID = "opaque-terminal-unknown"
	if _, err := lifecycle.Begin(agentcore.SessionPlan, planID); err != nil {
		t.Fatalf("Begin plan: %v", err)
	}
	logicalID := lifecycleSessionID(agentcore.SessionPlan, planID)
	runtimeID := lifecycle.runtimeSessionID(agentcore.SessionPlan, logicalID)
	if runtimeID == logicalID || lifecycle.logicalSessionForRuntime(runtimeID) != logicalID {
		t.Fatalf("opaque owner mapping = %q -> %q", runtimeID, lifecycle.logicalSessionForRuntime(runtimeID))
	}
	fault := errors.New("injected terminal post-publication directory sync failure")
	persistence.failNext(agentcore.PersistencePublishedDurabilityUnknown, fault)

	err := lifecycle.Complete(agentcore.SessionPlan, planID)
	if !errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) || !errors.Is(err, fault) {
		t.Fatalf("Complete error = %v, want published-unknown fault", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(runtimeID) || lifecycle.logicalSessionForRuntime(runtimeID) != "" {
		t.Fatalf("terminal unknown retained opaque runtime owner mapping for %q", runtimeID)
	}
	row, err := lifecycle.GetByID(logicalID)
	if err != nil || row.Status != agentcore.SessionCompleted {
		t.Fatalf("terminal row = %+v, err=%v", row, err)
	}
}

func TestLifecycleContentPublicationUnknownRevokesAuthorityAndPoisonCannotRestoreIt(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AgentLifecycle, string) error
	}{
		{
			name: "append",
			mutate: func(l *AgentLifecycle, id string) error {
				return l.Append(agentcore.SessionChat, id, agentcore.StreamEventInput{
					Kind: agentcore.StreamDelta, Data: "private stream data",
				})
			},
		},
		{
			name: "checkpoint",
			mutate: func(l *AgentLifecycle, id string) error {
				_, err := l.Checkpoint(agentcore.SessionChat, id, "private checkpoint", map[string]string{"content": "private"})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
			id := createFaultInjectedChat(t, agent)
			fault := errors.New("injected content post-publication directory sync failure")
			persistence.failNext(agentcore.PersistencePublishedDurabilityUnknown, fault)
			if err := test.mutate(lifecycle, id); !errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) || !errors.Is(err, fault) {
				t.Fatalf("mutation error = %v, want published-unknown fault", err)
			}
			runtime, err := agent.coreRuntime()
			if err != nil {
				t.Fatalf("coreRuntime: %v", err)
			}
			if runtime.IsSessionRegistered(id) || lifecycle.logicalSessionForRuntime(id) != "" {
				t.Fatal("content publication unknown retained runtime authority")
			}
			if err := lifecycle.Fail(agentcore.SessionChat, id, errors.New("after poison")); !errors.Is(err, agentcore.ErrSessionPersistencePoisoned) || !errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) {
				t.Fatalf("mutation against poisoned store = %v, want poisoned indeterminate error", err)
			}
			if runtime.IsSessionRegistered(id) || lifecycle.logicalSessionForRuntime(id) != "" {
				t.Fatal("poisoned mutation restored runtime authority")
			}
		})
	}
}

func TestLifecycleUsageObservationPublicationUnknownIsVisibleAndRevokesAuthority(t *testing.T) {
	agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
	id, err := agent.createAgentSessionTrusted(string(agentcore.SessionWorkflow))
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	fault := errors.New("injected usage observation post-publication directory sync failure")
	persistence.failNext(agentcore.PersistencePublishedDurabilityUnknown, fault)
	now := time.Now()
	err = lifecycle.RecordUsage(agentcore.UsageRecord{
		UnitID: "usage-observation-unknown", SessionID: id,
		UnitKind: agentcore.UsageUnitTool, Operation: "workflow.step",
		CostBasis: agentcore.CostNotApplicable,
		StartedAt: now, CompletedAt: now.Add(time.Second), Success: true,
	})
	if !errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) || !errors.Is(err, fault) {
		t.Fatalf("RecordUsage error = %v, want visible published-unknown fault", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(id) || lifecycle.logicalSessionForRuntime(id) != "" {
		t.Fatal("usage observation publication unknown retained runtime authority")
	}
	row, err := lifecycle.GetByID(id)
	if err != nil || len(row.Stream) != 1 || len(row.Checkpoints) != 1 {
		t.Fatalf("retained usage observation row = %+v, err=%v", row, err)
	}
}

func TestLifecycleResumePublishesBeforeRuntimeActivation(t *testing.T) {
	agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
	id := createFaultInjectedChat(t, agent)
	if _, err := lifecycle.Checkpoint(agentcore.SessionChat, id, "resume", map[string]bool{"ready": true}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := lifecycle.Pause(agentcore.SessionChat, id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	entered, release := persistence.blockNext()
	result := make(chan error, 1)
	go func() { result <- lifecycle.ResumeLatest(agentcore.SessionChat, id) }()
	<-entered
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionActive(id) {
		t.Fatal("resume activated runtime authority before durable publication completed")
	}
	release()
	if err := <-result; err != nil {
		t.Fatalf("ResumeLatest: %v", err)
	}
	if !runtime.IsSessionActive(id) {
		t.Fatal("durable resume did not activate runtime authority")
	}
}

func TestLifecycleBeginExistingPublishesOwnerBeforeActivatingPreexistingRuntime(t *testing.T) {
	agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	id := "chat:preexisting-runtime"
	if err := runtime.RegisterSession(id); err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	entered, release := persistence.blockNext()
	result := make(chan error, 1)
	go func() {
		_, err := lifecycle.BeginExisting(agentcore.SessionChat, id)
		result <- err
	}()
	<-entered
	if runtime.IsSessionActive(id) {
		t.Fatal("BeginExisting left preexisting runtime active before owner publication completed")
	}
	release()
	if err := <-result; err != nil {
		t.Fatalf("BeginExisting: %v", err)
	}
	if !runtime.IsSessionActive(id) {
		t.Fatal("BeginExisting did not reactivate runtime after durable owner publication")
	}
	row, err := lifecycle.GetByID(id)
	if err != nil || row.Owner == nil || row.Owner.RuntimeID != id {
		t.Fatalf("durable owner row = %+v, err=%v", row, err)
	}
}
