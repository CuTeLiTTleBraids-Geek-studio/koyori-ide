package services

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func seedUnscopedLifecycleRecoveryRows(t *testing.T, configDir string) map[agentcore.SessionKind]string {
	t.Helper()
	agent := NewAgentService()
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
		configDir,
	)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle seed: %v", err)
	}

	ids := make(map[agentcore.SessionKind]string, 2)
	for _, kind := range []agentcore.SessionKind{agentcore.SessionChat, agentcore.SessionWorkflow} {
		id, err := agent.createAgentSessionTrusted(string(kind))
		if err != nil {
			_ = agent.Close()
			t.Fatalf("CreateAgentSession(%s): %v", kind, err)
		}
		ids[kind] = id
		if _, err := lifecycle.Checkpoint(kind, id, "private-"+string(kind), map[string]string{
			"content": "unscoped-secret-" + string(kind),
		}); err != nil {
			_ = agent.Close()
			t.Fatalf("Checkpoint(%s): %v", kind, err)
		}
		if err := lifecycle.Pause(kind, id); err != nil {
			_ = agent.Close()
			t.Fatalf("Pause(%s): %v", kind, err)
		}
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("Close seed agent: %v", err)
	}
	return ids
}

func wireUnscopedRecoveryLifecycle(t *testing.T, configDir string) (*AgentService, *AgentLifecycle) {
	t.Helper()
	agent := NewAgentService()
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
		configDir,
	)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle restart: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	return agent, lifecycle
}

func TestUnscopedDurableLifecycleRecoveryListsContentFreeAndDiscardsAfterRestart(t *testing.T) {
	configDir := t.TempDir()
	ids := seedUnscopedLifecycleRecoveryRows(t, configDir)
	agent, lifecycle := wireUnscopedRecoveryLifecycle(t, configDir)

	if generation := agent.agentWorkspaceGeneration(); generation != 0 {
		t.Fatalf("unscoped restart generation = %d, want 0", generation)
	}
	entries, err := lifecycle.pendingRecoveryDispositions()
	if err != nil {
		t.Fatalf("pendingRecoveryDispositions: %v", err)
	}
	if len(entries) != len(ids) {
		t.Fatalf("recovery entries = %+v, want %d", entries, len(ids))
	}
	seen := make(map[string]agentRecoveryRow, len(entries))
	for _, entry := range entries {
		seen[entry.ID] = entry
	}
	for kind, id := range ids {
		entry, ok := seen[id]
		if !ok {
			t.Fatalf("missing %s recovery entry %q in %+v", kind, id, entries)
		}
		if entry.Kind != kind || entry.Status != agentcore.SessionPaused ||
			entry.OwnerDomain != lifecycleOwnerDomain(kind) || entry.WorkspaceGeneration != 0 ||
			entry.CheckpointCount != 1 {
			t.Fatalf("%s recovery entry = %+v", kind, entry)
		}
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal recovery entries: %v", err)
	}
	for _, forbidden := range []string{
		"unscoped-secret-chat", "unscoped-secret-workflow", "private-chat", "private-workflow", "content",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("unscoped recovery listing leaked %q: %s", forbidden, encoded)
		}
	}

	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	for kind, id := range ids {
		if runtime.IsSessionRegistered(id) {
			t.Fatalf("%s restart orphan %q regained runtime authority", kind, id)
		}
		disposed, err := lifecycle.applyRecoveryDisposition(kind, id, agentcore.RecoveryDispositionDiscard)
		if err != nil {
			t.Fatalf("applyRecoveryDisposition(%s): %v", kind, err)
		}
		if disposed.Status != agentcore.SessionCompleted || disposed.Recovery != agentcore.SessionRecoveryNone ||
			disposed.RecoveryDisposition != agentcore.RecoveryDispositionDiscard {
			t.Fatalf("disposed %s row = %+v", kind, disposed)
		}
		if runtime.IsSessionRegistered(id) {
			t.Fatalf("discard of %s row %q registered runtime authority", kind, id)
		}
	}

	reloadAgent, reloaded := wireUnscopedRecoveryLifecycle(t, configDir)
	if entries, err := reloaded.pendingRecoveryDispositions(); err != nil || len(entries) != 0 {
		t.Fatalf("recovery entries after persisted discard = %+v, err=%v", entries, err)
	}
	if generation := reloadAgent.agentWorkspaceGeneration(); generation != 0 {
		t.Fatalf("second unscoped restart generation = %d, want 0", generation)
	}
	for kind, id := range ids {
		row, err := reloaded.GetByID(id)
		if err != nil || row.Status != agentcore.SessionCompleted ||
			row.RecoveryDisposition != agentcore.RecoveryDispositionDiscard {
			t.Fatalf("persisted %s disposition row = %+v, err=%v", kind, row, err)
		}
	}
}

func TestUnscopedDurableLifecycleRecoveryCannotBeDisposedAfterWorkspaceOpens(t *testing.T) {
	configDir := t.TempDir()
	ids := seedUnscopedLifecycleRecoveryRows(t, configDir)
	agent, lifecycle := wireUnscopedRecoveryLifecycle(t, configDir)

	entries, err := lifecycle.pendingRecoveryDispositions()
	if err != nil || len(entries) != len(ids) {
		t.Fatalf("unscoped recovery entries before workspace open = %+v, err=%v", entries, err)
	}
	if err := agent.configureWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatalf("configure workspace after unscoped restart: %v", err)
	}
	if generation := agent.agentWorkspaceGeneration(); generation == 0 {
		t.Fatal("workspace open retained generation 0")
	}
	if entries, err := lifecycle.pendingRecoveryDispositions(); err != nil || len(entries) != 0 {
		t.Fatalf("workspace-scoped listing exposed unscoped rows = %+v, err=%v", entries, err)
	}

	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	for kind, id := range ids {
		if _, err := lifecycle.applyRecoveryDisposition(kind, id, agentcore.RecoveryDispositionDiscard); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("workspace-scoped disposition of unscoped %s row error = %v, want ErrNotAllowed", kind, err)
		}
		row, err := lifecycle.GetByID(id)
		if err != nil || row.Status != agentcore.SessionPaused || row.Recovery != agentcore.SessionRecoveryRequired ||
			row.RecoveryDisposition != "" || row.Owner == nil || row.Owner.WorkspaceGeneration != 0 {
			t.Fatalf("rejected unscoped %s row changed = %+v, err=%v", kind, row, err)
		}
		if runtime.IsSessionRegistered(id) {
			t.Fatalf("rejected unscoped %s row %q gained runtime authority", kind, id)
		}
	}
}
