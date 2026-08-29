package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

type workspaceResetPersistence struct {
	mu    sync.Mutex
	state agentcore.PersistenceCommitState
	err   error
	rows  []agentcore.Session
}

func (p *workspaceResetPersistence) Load() ([]agentcore.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]agentcore.Session(nil), p.rows...), nil
}

func (p *workspaceResetPersistence) Save(rows []agentcore.Session) (agentcore.PersistenceCommitState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.state, p.err
	}
	p.rows = append([]agentcore.Session(nil), rows...)
	return agentcore.PersistenceDurable, nil
}

func newWorkspaceResetOwnerFixture(t *testing.T) (*AgentService, *workspaceResetPersistence, string, string, context.Context, string) {
	t.Helper()
	rootA := t.TempDir()
	rootB := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, NewAIPermissionService(t.TempDir())); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	persistence := &workspaceResetPersistence{}
	store, err := agentcore.NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("NewPersistentSessionStore: %v", err)
	}
	permission := NewAIPermissionService(t.TempDir())
	_, err = wireAgentLifecycleWithStore(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission,
		store, bytesForWorkspaceResetOwnerKey(),
	)
	if err != nil {
		t.Fatalf("wireAgentLifecycleWithStore: %v", err)
	}
	ctx := withAgentCallerContext(context.Background(), "wails-window:reset-owner")
	sessionID, err := agent.CreateAgentSessionForCaller(ctx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	return agent, persistence, rootA, rootB, ctx, sessionID
}

func bytesForWorkspaceResetOwnerKey() []byte {
	return []byte("workspace-reset-owner-test-key")
}

func TestAgentWorkspaceResetPrePublicationPreservesRendererOwner(t *testing.T) {
	agent, persistence, rootA, rootB, ctx, sessionID := newWorkspaceResetOwnerFixture(t)
	persistence.mu.Lock()
	persistence.state = agentcore.PersistenceNotPublished
	persistence.err = errors.New("injected pre-publication reset failure")
	persistence.mu.Unlock()

	if err := agent.setWorkspaceRoot(rootB); err == nil {
		t.Fatal("setWorkspaceRoot unexpectedly succeeded during pre-publication failure")
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("workspace root after rollback = %q, want %q", got, rootA)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if !runtime.IsSessionRegistered(sessionID) {
		t.Fatal("pre-publication reset revoked the old runtime session")
	}
	if err := authorizeAgentSessionOwner(agent, ctx, sessionID); err != nil {
		t.Fatalf("original caller owner after pre-publication rollback: %v", err)
	}
}

func TestAgentWorkspaceResetIndeterminateRevokesRendererOwner(t *testing.T) {
	agent, persistence, rootA, rootB, ctx, sessionID := newWorkspaceResetOwnerFixture(t)
	persistence.mu.Lock()
	persistence.state = agentcore.PersistencePublishedDurabilityUnknown
	persistence.err = errors.New("injected post-publication reset failure")
	persistence.mu.Unlock()

	if err := agent.setWorkspaceRoot(rootB); err == nil {
		t.Fatal("setWorkspaceRoot unexpectedly succeeded during indeterminate failure")
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("workspace root after indeterminate rollback = %q, want %q", got, rootA)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("indeterminate reset retained the old runtime session")
	}
	if err := authorizeAgentSessionOwner(agent, ctx, sessionID); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("original caller owner after indeterminate reset = %v, want ErrNotAllowed", err)
	}
}
