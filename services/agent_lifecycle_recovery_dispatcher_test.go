package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"
)

func wireRecoveryDispatcherLifecycle(
	t *testing.T,
	configDir string,
) (*services.AgentService, *services.AgentLifecycle) {
	t.Helper()
	agent := services.NewAgentService()
	lifecycle, err := services.WireAgentLifecycle(
		agent,
		services.NewAIService(),
		services.NewAIPlanService(),
		services.NewAIGoalService(),
		services.NewAIPermissionService(t.TempDir()),
		configDir,
	)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	return agent, lifecycle
}

type seededRecoverySession struct {
	kind agentcore.SessionKind
	id   string
}

func seedSensitiveRecoverySessions(t *testing.T, configDir string) []seededRecoverySession {
	t.Helper()
	agent, lifecycle := wireRecoveryDispatcherLifecycle(t, configDir)
	seeded := make([]seededRecoverySession, 0, 2)
	for _, kind := range []agentcore.SessionKind{agentcore.SessionPlan, agentcore.SessionGoal} {
		session, err := lifecycle.Begin(kind, "secret-marker-"+string(kind)+"-C:/private/source.go")
		if err != nil {
			t.Fatalf("Begin(%s): %v", kind, err)
		}
		if err := lifecycle.Append(kind, session.ID, agentcore.StreamEventInput{
			Kind: agentcore.StreamDelta,
			Data: "private streamed source",
		}); err != nil {
			t.Fatalf("Append(%s): %v", kind, err)
		}
		if _, err := lifecycle.Checkpoint(kind, session.ID, "private-label", map[string]string{
			"content": "private checkpoint source",
		}); err != nil {
			t.Fatalf("Checkpoint(%s): %v", kind, err)
		}
		if err := lifecycle.Pause(kind, session.ID); err != nil {
			t.Fatalf("Pause(%s): %v", kind, err)
		}
		seeded = append(seeded, seededRecoverySession{kind: kind, id: session.ID})
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("Close seed agent: %v", err)
	}
	return seeded
}

func recoveryHandlesByKind(t *testing.T, lifecycle *services.AgentLifecycle) map[string]string {
	t.Helper()
	entries, err := lifecycle.PendingRecoveryDispositions()
	if err != nil {
		t.Fatalf("PendingRecoveryDispositions: %v", err)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal entries: %v", err)
	}
	for _, forbidden := range []string{
		"secret-marker", "private streamed source", "private checkpoint source", "private-label", "C:/private", "content",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("recovery metadata leaked %q: %s", forbidden, encoded)
		}
	}
	handles := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.Handle == "" || entry.Kind == "" || entry.Status == "" || entry.CheckpointCount != 1 {
			t.Fatalf("incomplete recovery entry = %+v", entry)
		}
		if previous := handles[entry.Kind]; previous != "" {
			t.Fatalf("duplicate recovery kind %q: %q and %q", entry.Kind, previous, entry.Handle)
		}
		handles[entry.Kind] = entry.Handle
	}
	return handles
}

func TestAgentRecoveryDispatcherOpaqueHandleIsStableContentFreeAndTerminal(t *testing.T) {
	configDir := t.TempDir()
	seeded := seedSensitiveRecoverySessions(t, configDir)

	restartedAgent, restarted := wireRecoveryDispatcherLifecycle(t, configDir)
	firstHandles := recoveryHandlesByKind(t, restarted)
	if len(firstHandles) != len(seeded) || firstHandles["plan"] == firstHandles["goal"] {
		t.Fatalf("recovery handles = %+v", firstHandles)
	}
	if err := restartedAgent.Close(); err != nil {
		t.Fatalf("Close first restart: %v", err)
	}

	operatorAgent, operatorLifecycle := wireRecoveryDispatcherLifecycle(t, configDir)
	secondHandles := recoveryHandlesByKind(t, operatorLifecycle)
	if !reflect.DeepEqual(secondHandles, firstHandles) {
		t.Fatalf("recovery handles changed across restart: first=%+v second=%+v", firstHandles, secondHandles)
	}
	for _, request := range []services.AgentRecoveryDispositionRequest{
		{Handle: strings.Repeat("0", 64), Disposition: "discard"},
		{Handle: firstHandles["plan"], Disposition: "resume"},
	} {
		if _, err := operatorLifecycle.DispatchRecoveryDisposition(request); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatalf("invalid recovery request result for %+v: %v", request, err)
		}
	}

	for _, seededSession := range seeded {
		handle := firstHandles[string(seededSession.kind)]
		result, err := operatorLifecycle.DispatchRecoveryDisposition(services.AgentRecoveryDispositionRequest{
			Handle: handle, Disposition: "discard",
		})
		if err != nil {
			t.Fatalf("DispatchRecoveryDisposition(%s): %v", seededSession.kind, err)
		}
		if result.Handle != handle || result.Kind != string(seededSession.kind) || result.Status != "completed" ||
			result.Disposition != "discard" || result.CompletedAt.IsZero() {
			t.Fatalf("recovery result = %+v", result)
		}
		resultJSON, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		if strings.Contains(string(resultJSON), "secret-marker") || strings.Contains(string(resultJSON), "private") {
			t.Fatalf("recovery result leaked lifecycle identity/content: %s", resultJSON)
		}
		replay, err := operatorLifecycle.DispatchRecoveryDisposition(services.AgentRecoveryDispositionRequest{
			Handle: handle, Disposition: "discard",
		})
		if err != nil || !reflect.DeepEqual(replay, result) {
			t.Fatalf("idempotent recovery replay = %+v, err=%v; want %+v", replay, err, result)
		}
	}

	catalog, err := operatorAgent.GetAgentToolCatalog(context.Background())
	if err != nil || len(catalog.Tools) == 0 {
		t.Fatalf("GetAgentToolCatalog: tools=%d err=%v", len(catalog.Tools), err)
	}
	for _, seededSession := range seeded {
		if _, err := operatorAgent.RequestAgentToolCapability(context.Background(), services.AgentToolExecutionRequest{
			SessionID: seededSession.id,
			ToolID:    catalog.Tools[0].ID,
			Arguments: map[string]interface{}{},
		}); !errors.Is(err, agentcore.ErrUnknownSession) {
			t.Fatalf("discarded %s capability error = %v, want ErrUnknownSession", seededSession.kind, err)
		}
	}

	_, reloaded := wireRecoveryDispatcherLifecycle(t, configDir)
	if entries, err := reloaded.PendingRecoveryDispositions(); err != nil || len(entries) != 0 {
		t.Fatalf("pending entries after durable discard = %+v, err=%v", entries, err)
	}
}

func TestAgentRecoveryDispatcherPrePublishFailureRemainsPendingAndRetryable(t *testing.T) {
	configDir := t.TempDir()
	seeded := seedSensitiveRecoverySessions(t, configDir)
	_, lifecycle := wireRecoveryDispatcherLifecycle(t, configDir)
	handles := recoveryHandlesByKind(t, lifecycle)
	handle := handles[string(seeded[0].kind)]

	snapshotPath := filepath.Join(configDir, "agent_lifecycle_sessions.json")
	if err := os.Remove(snapshotPath); err != nil {
		t.Fatalf("remove lifecycle snapshot: %v", err)
	}
	if err := os.Mkdir(snapshotPath, 0o700); err != nil {
		t.Fatalf("block lifecycle snapshot publication: %v", err)
	}
	if _, err := lifecycle.DispatchRecoveryDisposition(services.AgentRecoveryDispositionRequest{
		Handle: handle, Disposition: "discard",
	}); !errors.Is(err, services.ErrAgentRecoveryPersistence) || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("pre-publish recovery error = %v, want safe ErrAgentRecoveryPersistence", err)
	}
	if got := recoveryHandlesByKind(t, lifecycle)[string(seeded[0].kind)]; got != handle {
		t.Fatalf("pre-publish failure changed recovery handle: got %q want %q", got, handle)
	}
	if err := os.Remove(snapshotPath); err != nil {
		t.Fatalf("remove snapshot blocker: %v", err)
	}
	if _, err := lifecycle.DispatchRecoveryDisposition(services.AgentRecoveryDispositionRequest{
		Handle: handle, Disposition: "discard",
	}); err != nil {
		t.Fatalf("retry durable recovery disposition: %v", err)
	}
}

func TestAgentRecoveryDispatcherConcurrentDiscardIsIdempotent(t *testing.T) {
	configDir := t.TempDir()
	seeded := seedSensitiveRecoverySessions(t, configDir)
	_, lifecycle := wireRecoveryDispatcherLifecycle(t, configDir)
	handle := recoveryHandlesByKind(t, lifecycle)[string(seeded[0].kind)]

	const callers = 12
	results := make([]services.AgentRecoveryDispositionResult, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := range results {
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] = lifecycle.DispatchRecoveryDisposition(services.AgentRecoveryDispositionRequest{
				Handle: handle, Disposition: "discard",
			})
		}(index)
	}
	close(start)
	wait.Wait()
	for index := range results {
		if errs[index] != nil || !reflect.DeepEqual(results[index], results[0]) {
			t.Fatalf("concurrent result[%d] = %+v, err=%v; first=%+v", index, results[index], errs[index], results[0])
		}
	}
}
