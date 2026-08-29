package services

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func TestAgentRecoveryDispatcherUnknownPublicationPoisonsAllCallersUntilReload(t *testing.T) {
	agent, lifecycle, persistence := newFaultInjectedLifecycle(t)
	session, err := lifecycle.Begin(agentcore.SessionPlan, "secret-marker-plan-C:/private/source.go")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := lifecycle.Pause(agentcore.SessionPlan, session.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	runtimeID := lifecycle.runtimeSessionID(agentcore.SessionPlan, session.ID)
	if runtimeID == session.ID {
		t.Fatalf("plan session did not receive an opaque runtime owner: %q", runtimeID)
	}

	restartedStore, err := agentcore.NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("reload recovery store: %v", err)
	}
	lifecycle.revokeAllRuntimeAuthority()
	lifecycle.sessions = restartedStore
	lifecycle.incarnation += "-restart"
	entries, err := lifecycle.PendingRecoveryDispositions()
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending recovery entries = %+v, err=%v", entries, err)
	}
	handle := entries[0].Handle
	if handle == "" || strings.Contains(handle, "secret-marker") {
		t.Fatalf("unsafe recovery handle %q", handle)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(runtimeID) {
		t.Fatal("restart recovery row retained runtime authority")
	}

	fault := errors.New("secret-marker-storage-detail")
	persistence.failNext(agentcore.PersistencePublishedDurabilityUnknown, fault)
	beforeSaves := persistence.saveCount()
	const callers = 12
	errs := make([]error, callers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := range errs {
		go func(index int) {
			defer wait.Done()
			<-start
			_, errs[index] = lifecycle.DispatchRecoveryDisposition(AgentRecoveryDispositionRequest{
				Handle: handle, Disposition: "discard",
			})
		}(index)
	}
	close(start)
	wait.Wait()
	for index, dispositionErr := range errs {
		if !errors.Is(dispositionErr, ErrAgentRecoveryPersistenceIndeterminate) {
			t.Fatalf("caller[%d] error = %v, want ErrAgentRecoveryPersistenceIndeterminate", index, dispositionErr)
		}
		if strings.Contains(dispositionErr.Error(), "secret-marker") || strings.Contains(dispositionErr.Error(), session.ID) {
			t.Fatalf("caller[%d] error leaked private detail: %v", index, dispositionErr)
		}
	}
	if saves := persistence.saveCount() - beforeSaves; saves != 1 {
		t.Fatalf("recovery publication attempts = %d, want 1", saves)
	}
	if entries, inventoryErr := lifecycle.PendingRecoveryDispositions(); entries != nil ||
		!errors.Is(inventoryErr, ErrAgentRecoveryPersistenceIndeterminate) {
		t.Fatalf("poisoned inventory = %+v, err=%v", entries, inventoryErr)
	}
	if _, replayErr := lifecycle.DispatchRecoveryDisposition(AgentRecoveryDispositionRequest{
		Handle: handle, Disposition: "discard",
	}); !errors.Is(replayErr, ErrAgentRecoveryPersistenceIndeterminate) {
		t.Fatalf("poisoned replay error = %v", replayErr)
	}
	if runtime.IsSessionRegistered(runtimeID) {
		t.Fatal("unknown publication restored runtime authority")
	}

	confirmedStore, err := agentcore.NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("reload published disposition: %v", err)
	}
	lifecycle.sessions = confirmedStore
	lifecycle.incarnation += "-verified"
	confirmed, err := lifecycle.DispatchRecoveryDisposition(AgentRecoveryDispositionRequest{
		Handle: handle, Disposition: "discard",
	})
	if err != nil || confirmed.Status != "completed" || confirmed.Disposition != "discard" {
		t.Fatalf("confirmed disposition = %+v, err=%v", confirmed, err)
	}
}
