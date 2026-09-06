package services

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func TestAgentLifecycleEntryPointsUseWorkspaceAuthority(t *testing.T) {
	root := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, root)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	existingID, err := agent.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("create existing chat session: %v", err)
	}

	cases := []struct {
		name string
		run  func() error
	}{
		{name: "begin-chat", run: func() error {
			_, err := lifecycle.Begin(agentcore.SessionChat, "authority-chat")
			return err
		}},
		{name: "begin-plan", run: func() error {
			_, err := lifecycle.Begin(agentcore.SessionPlan, "authority-plan")
			return err
		}},
		{name: "begin-goal", run: func() error {
			_, err := lifecycle.Begin(agentcore.SessionGoal, "authority-goal")
			return err
		}},
		{name: "begin-existing-chat", run: func() error {
			_, err := lifecycle.BeginExisting(agentcore.SessionChat, existingID)
			return err
		}},
		{name: "create-owned-chat", run: func() error {
			_, err := lifecycle.CreateOwnedSession(agentcore.SessionChat)
			return err
		}},
		{name: "create-owned-workflow", run: func() error {
			_, err := lifecycle.CreateOwnedSession(agentcore.SessionWorkflow)
			return err
		}},
		{name: "checkpoint-existing-chat", run: func() error {
			_, err := lifecycle.Checkpoint(agentcore.SessionChat, existingID, "authority", map[string]bool{"ready": true})
			return err
		}},
		{name: "close-existing-chat", run: func() error {
			return lifecycle.CloseByID(existingID)
		}},
	}
	deps := executionDependenciesFor(agent)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps.workspaceAuthorityMu.Lock()
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				close(started)
				done <- tc.run()
			}()
			<-started
			var early error
			returnedEarly := false
			select {
			case early = <-done:
				returnedEarly = true
			case <-time.After(150 * time.Millisecond):
			}
			deps.workspaceAuthorityMu.Unlock()
			if returnedEarly {
				t.Fatalf("lifecycle entry returned while workspace authority was exclusively held: %v", early)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("lifecycle entry after authority release: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("lifecycle entry remained blocked after workspace authority release")
			}
		})
	}
}

func TestAgentLifecycleUsageAdmissionUsesWorkspaceAuthority(t *testing.T) {
	root := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, root)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	deps := executionDependenciesFor(agent)
	deps.workspaceAuthorityMu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Record(agentcore.UsageRecord{
			SessionID:   "workflow:workspace-authority-record",
			UnitID:      "workspace-authority-record",
			UnitKind:    agentcore.UsageUnitWorkflow,
			Operation:   "workflow.test",
			CostBasis:   agentcore.CostNotApplicable,
			StartedAt:   time.Now(),
			CompletedAt: time.Now(),
			Success:     true,
		})
	}()
	var early error
	returnedEarly := false
	select {
	case early = <-done:
		returnedEarly = true
	case <-time.After(150 * time.Millisecond):
	}
	deps.workspaceAuthorityMu.Unlock()
	if returnedEarly {
		t.Fatalf("usage admission crossed workspace authority: %v", early)
	}
	select {
	case recordErr := <-done:
		if recordErr != nil {
			t.Fatalf("Record after workspace authority release: %v", recordErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("usage admission remained blocked after workspace authority release")
	}
}

func TestExternalReceiptRecoveryEntryPointsUseWorkspaceAuthority(t *testing.T) {
	root := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, root)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	fakeHandle := externalReceiptRecoveryHandlePrefix + strings.Repeat("0", 64)
	cases := []struct {
		name string
		run  func()
	}{
		{name: "pending", run: func() {
			_, _ = lifecycle.PendingExternalReceiptDispositions()
		}},
		{name: "dispatch", run: func() {
			_, _ = lifecycle.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
				Handle: fakeHandle, Disposition: externalReceiptDispositionManualUnknown,
			})
		}},
	}
	deps := executionDependenciesFor(agent)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps.workspaceAuthorityMu.Lock()
			started := make(chan struct{})
			done := make(chan struct{})
			go func() {
				close(started)
				tc.run()
				close(done)
			}()
			<-started
			returnedEarly := false
			select {
			case <-done:
				returnedEarly = true
			case <-time.After(150 * time.Millisecond):
			}
			deps.workspaceAuthorityMu.Unlock()
			if returnedEarly {
				t.Fatal("external receipt recovery crossed workspace authority")
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("external receipt recovery remained blocked after workspace authority release")
			}
		})
	}
}

func TestExternalReceiptRecoveryWaitsForProjectWorkspaceTransaction(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	setterEntered := make(chan struct{})
	setterRelease := make(chan struct{})
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		beforeWorkspaceSetters: func() {
			close(setterEntered)
			<-setterRelease
		},
	}
	addDone := make(chan error, 1)
	go func() {
		_, addErr := service.AddProject(rootB)
		addDone <- addErr
	}()
	select {
	case <-setterEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("project transaction did not reach the first-setter barrier")
	}
	fakeHandle := externalReceiptRecoveryHandlePrefix + strings.Repeat("0", 64)
	started := make(chan struct{}, 2)
	done := make(chan struct{}, 2)
	for _, run := range []func(){
		func() { _, _ = lifecycle.PendingExternalReceiptDispositions() },
		func() {
			_, _ = lifecycle.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
				Handle: fakeHandle, Disposition: externalReceiptDispositionManualUnknown,
			})
		},
	} {
		go func(run func()) {
			started <- struct{}{}
			run()
			done <- struct{}{}
		}(run)
	}
	<-started
	<-started
	select {
	case <-done:
		close(setterRelease)
		t.Fatal("external receipt recovery crossed an unresolved Project transaction")
	case <-time.After(200 * time.Millisecond):
	}
	close(setterRelease)
	if err := <-addDone; err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("external receipt recovery remained blocked after Project commit")
		}
	}
	if got := agent.currentWorkspaceRoot(); got != rootB {
		t.Fatalf("agent root after Project transaction = %q, want %q", got, rootB)
	}
}

func TestRuntimeUsageAdapterDoesNotReenterWorkspaceAuthority(t *testing.T) {
	root := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, root)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	sessionID, err := agent.createAgentSessionTrusted("chat")
	if err != nil {
		t.Fatalf("createAgentSessionTrusted: %v", err)
	}
	lease, err := lifecycle.acquireWorkspaceAuthority()
	if err != nil {
		t.Fatalf("acquireWorkspaceAuthority: %v", err)
	}
	released := false
	defer func() {
		if !released {
			lease.release()
		}
	}()
	sink := agentLifecycleRuntimeUsageSink{lifecycle: lifecycle}
	startedAt := time.Now()
	receipt, err := sink.BeginUsage(agentcore.UsageRecord{
		UnitID: "runtime-held-usage", SessionID: sessionID,
		UnitKind: agentcore.UsageUnitChat, Operation: "runtime.test",
		CostBasis: agentcore.CostNotApplicable,
		StartedAt: startedAt, CompletedAt: startedAt, Pending: true,
	})
	if err != nil {
		t.Fatalf("BeginUsage: %v", err)
	}
	deps := executionDependenciesFor(agent)
	writerStarted := make(chan struct{})
	writerAcquired := make(chan struct{})
	go func() {
		close(writerStarted)
		deps.workspaceAuthorityMu.Lock()
		close(writerAcquired)
		deps.workspaceAuthorityMu.Unlock()
	}()
	<-writerStarted
	select {
	case <-writerAcquired:
		t.Fatal("workspace writer acquired while runtime usage lease was held")
	case <-time.After(150 * time.Millisecond):
	}
	completeDone := make(chan error, 1)
	go func() {
		completeDone <- sink.CompleteUsage(receipt, agentcore.UsageRecord{
			UnitID: receipt.UnitID, SessionID: sessionID,
			UnitKind: agentcore.UsageUnitChat, Operation: "runtime.test",
			CostBasis: agentcore.CostNotApplicable,
			StartedAt: startedAt, CompletedAt: time.Now(), Success: true,
		})
	}()
	select {
	case completeErr := <-completeDone:
		if completeErr != nil {
			t.Fatalf("CompleteUsage: %v", completeErr)
		}
	case <-time.After(2 * time.Second):
		lease.release()
		released = true
		t.Fatal("runtime usage adapter recursively waited on workspace authority")
	}
	lease.release()
	released = true
	select {
	case <-writerAcquired:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace writer remained blocked after runtime usage completion")
	}
}

func TestChatLifecycleUnitHoldsWorkspaceAuthorityUntilFinish(t *testing.T) {
	root := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, root)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	ai := NewAIService()
	if _, err := WireAgentLifecycle(
		agent,
		ai,
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	unit, err := beginChatLifecycle(ai.snapshot(), "authority-provider-call", []ChatMessage{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("beginChatLifecycle: %v", err)
	}
	deps := executionDependenciesFor(agent)
	acquired := make(chan struct{})
	go func() {
		deps.workspaceAuthorityMu.Lock()
		close(acquired)
		deps.workspaceAuthorityMu.Unlock()
	}()
	select {
	case <-acquired:
		t.Fatal("workspace transition acquired authority before the provider lifecycle unit finished")
	case <-time.After(150 * time.Millisecond):
	}
	callErr := errors.New("injected provider failure")
	if err := unit.finish(nil, callErr); !errors.Is(err, callErr) {
		t.Fatalf("finish error = %v, want %v", err, callErr)
	}
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace transition remained blocked after provider lifecycle finish")
	}
}

func TestAIProviderSnapshotWaitsForWorkspaceCommit(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	ai := NewAIService()
	ai.setProjectRoot(rootA)
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
	setterEntered := make(chan struct{})
	setterRelease := make(chan struct{})
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		aiService:    ai,
		wsCtx:        agent.workspaceContext,
		beforeWorkspaceSetters: func() {
			close(setterEntered)
			<-setterRelease
		},
	}
	addDone := make(chan error, 1)
	go func() {
		_, addErr := service.AddProject(rootB)
		addDone <- addErr
	}()
	select {
	case <-setterEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace transaction did not reach its first-setter barrier")
	}
	type snapshotResult struct {
		snap  aiSnapshot
		lease *agentWorkspaceAuthorityReadLease
		err   error
	}
	snapshotDone := make(chan snapshotResult, 1)
	go func() {
		snap, lease, err := ai.snapshotForProviderCall()
		snapshotDone <- snapshotResult{snap: snap, lease: lease, err: err}
	}()
	select {
	case result := <-snapshotDone:
		result.lease.release()
		t.Fatalf("provider snapshot crossed an unfinished workspace transaction: root=%q err=%v", result.snap.projectRoot, result.err)
	case <-time.After(150 * time.Millisecond):
	}
	close(setterRelease)
	if err := <-addDone; err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	select {
	case result := <-snapshotDone:
		defer result.lease.release()
		if result.err != nil {
			t.Fatalf("snapshotForProviderCall: %v", result.err)
		}
		if result.snap.projectRoot != rootB {
			t.Fatalf("provider project root = %q, want committed root %q", result.snap.projectRoot, rootB)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provider snapshot remained blocked after workspace commit")
	}
}

func TestStartStreamEarlyFailureReleasesWorkspaceAuthority(t *testing.T) {
	root := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, root)
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
	if _, err := ai.StartStream(testAIStreamCallerContext(), []ChatMessage{{Role: "user", Content: "hello"}}); err == nil {
		t.Fatal("StartStream unexpectedly succeeded without an API key")
	}
	acquired := make(chan struct{})
	deps := executionDependenciesFor(agent)
	go func() {
		deps.workspaceAuthorityMu.Lock()
		close(acquired)
		deps.workspaceAuthorityMu.Unlock()
	}()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace authority remained locked after StartStream preflight failure")
	}
}
