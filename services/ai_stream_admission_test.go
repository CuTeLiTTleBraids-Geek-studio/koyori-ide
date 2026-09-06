package services

import (
	"context"
	"errors"
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

// A session close marks admission before it revokes the lifecycle owner. A
// renderer request that arrives after that mark must be rejected before it can
// claim the provider slot or publish lifecycle/usage side effects.
func TestStartAgentStreamRejectsSessionMarkedClosingBeforeSideEffects(t *testing.T) {
	agent := NewAgentService()
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	permission := NewAIPermissionService(t.TempDir())
	if _, err := WireAgentLifecycle(
		agent,
		ai,
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	ownerCtx := withAgentCallerContext(context.Background(), "wails-window:closing-admission:main")
	sessionID, err := agent.CreateAgentSessionForCaller(ownerCtx, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}

	markAgentSessionClosing(agent, sessionID)
	if _, err := ai.StartAgentStream(ownerCtx, sessionID, []ChatMessage{{
		Role: "user", Content: "must be rejected",
	}}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("StartAgentStream after close mark = %v, want ErrNotAllowed", err)
	}
	if ai.IsStreaming() {
		t.Fatal("closing-session admission claimed the global provider slot")
	}
	if records := permission.usageRecordsSnapshot(); len(records) != 0 {
		t.Fatalf("closing-session admission published usage: %+v", records)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	lifecycle := deps.lifecycle
	deps.mu.RUnlock()
	if lifecycle != nil {
		session, getErr := lifecycle.GetByID(sessionID)
		if getErr != nil {
			t.Fatalf("GetByID after rejected admission: %v", getErr)
		}
		if session.Status != agentcore.SessionRunning {
			t.Fatalf("rejected admission changed lifecycle status: %+v", session)
		}
	}
}
