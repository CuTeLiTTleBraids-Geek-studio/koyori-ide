package services

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func wireWorkflowAttemptTestLifecycle(
	t *testing.T,
	agent *AgentService,
	permission *AIPermissionService,
	persistenceDir string,
) *AgentLifecycle {
	t.Helper()
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
		persistenceDir,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	return lifecycle
}

func TestTaskServiceWorkflowAttemptSurvivesTaskServiceRewire(t *testing.T) {
	agent := newTaskTestAgent(t)
	stateDir := t.TempDir()
	permission := NewAIPermissionService(stateDir)
	lifecycle := wireWorkflowAttemptTestLifecycle(t, agent, permission, stateDir)

	first := NewTaskService(agent)
	if err := WireTaskAgentLifecycle(first, lifecycle); err != nil {
		t.Fatalf("wire first TaskService: %v", err)
	}
	sessionID, err := first.BeginWorkflowExecution(trustedTaskContext(), "durable-attempt")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	pending := pendingWorkflowAttemptRecords(permission, sessionID)
	if len(pending) != 1 {
		t.Fatalf("pending workflow attempts = %+v, want one", pending)
	}

	second := NewTaskService(agent)
	if err := WireTaskAgentLifecycle(second, lifecycle); err != nil {
		t.Fatalf("wire replacement TaskService: %v", err)
	}
	if err := second.CompleteWorkflowExecution(trustedTaskContext(), sessionID); err != nil {
		t.Fatalf("replacement TaskService could not complete durable attempt: %v", err)
	}
	rows := permission.usageRecordsSnapshot()
	terminal := workflowAttemptRecordByUnitID(rows, pending[0].UnitID)
	if terminal == nil || terminal.Pending || !terminal.Success {
		t.Fatalf("durable attempt was not terminalized with the original unit ID: %+v", rows)
	}
}

func TestTaskServiceWorkflowAttemptCompletionFailureIsRetryable(t *testing.T) {
	agent := newTaskTestAgent(t)
	stateDir := t.TempDir()
	permission := NewAIPermissionService(stateDir)
	lifecycle := wireWorkflowAttemptTestLifecycle(t, agent, permission, stateDir)
	task := NewTaskService(agent)
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "retry-terminal-write")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	pending := pendingWorkflowAttemptRecords(permission, sessionID)
	if len(pending) != 1 {
		t.Fatalf("pending workflow attempts = %+v, want one", pending)
	}

	ledgerPath := filepath.Join(stateDir, "usage_log.jsonl")
	ledger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read pending ledger: %v", err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatalf("remove pending ledger: %v", err)
	}
	if err := os.Mkdir(ledgerPath, 0o700); err != nil {
		t.Fatalf("replace ledger with blocking directory: %v", err)
	}
	firstErr := task.CompleteWorkflowExecution(trustedTaskContext(), sessionID)
	if !errors.Is(firstErr, ErrUsagePersistence) {
		t.Fatalf("first completion error = %v, want ErrUsagePersistence", firstErr)
	}
	session, err := lifecycle.GetByID(sessionID)
	if err != nil || session.Status != agentcore.SessionRunning {
		t.Fatalf("failed terminal publication changed lifecycle authority: session=%+v err=%v", session, err)
	}
	if got := pendingWorkflowAttemptRecords(permission, sessionID); len(got) != 1 || got[0].UnitID != pending[0].UnitID {
		t.Fatalf("failed terminal publication consumed the pending receipt: %+v", got)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatalf("remove blocking ledger directory: %v", err)
	}
	if err := os.WriteFile(ledgerPath, ledger, 0o600); err != nil {
		t.Fatalf("restore pending ledger: %v", err)
	}

	if err := task.CompleteWorkflowExecution(trustedTaskContext(), sessionID); err != nil {
		t.Fatalf("retry completion with the original receipt: %v", err)
	}
	rows := permission.usageRecordsSnapshot()
	terminal := workflowAttemptRecordByUnitID(rows, pending[0].UnitID)
	if terminal == nil || terminal.Pending || !terminal.Success {
		t.Fatalf("retry did not terminalize original attempt: %+v", rows)
	}
	session, err = lifecycle.GetByID(sessionID)
	if err != nil || session.Status != agentcore.SessionCompleted {
		t.Fatalf("retry did not complete lifecycle consistently: session=%+v err=%v", session, err)
	}
}

func TestTaskServiceWorkflowAttemptResumeRunningIsRejectedWithoutMutation(t *testing.T) {
	agent := newTaskTestAgent(t)
	stateDir := t.TempDir()
	permission := NewAIPermissionService(stateDir)
	lifecycle := wireWorkflowAttemptTestLifecycle(t, agent, permission, stateDir)
	task := NewTaskService(agent)
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "resume-running")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	pendingBefore := pendingWorkflowAttemptRecords(permission, sessionID)
	if len(pendingBefore) != 1 {
		t.Fatalf("pending workflow attempts = %+v, want one", pendingBefore)
	}

	err = task.ResumeWorkflowExecution(trustedTaskContext(), sessionID)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("ResumeWorkflowExecution error = %v, want ErrAlreadyExists", err)
	}
	session, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("GetByID after rejected resume: %v", err)
	}
	if session.Status != agentcore.SessionRunning {
		t.Fatalf("rejected running resume changed lifecycle status to %s", session.Status)
	}
	pendingAfter := pendingWorkflowAttemptRecords(permission, sessionID)
	if len(pendingAfter) != 1 || pendingAfter[0].UnitID != pendingBefore[0].UnitID {
		t.Fatalf("rejected running resume changed pending receipt: before=%+v after=%+v", pendingBefore, pendingAfter)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if !runtime.IsSessionActive(sessionID) {
		t.Fatal("rejected running resume revoked runtime authority")
	}

	if err := task.CompleteWorkflowExecution(trustedTaskContext(), sessionID); err != nil {
		t.Fatalf("CompleteWorkflowExecution after rejected resume: %v", err)
	}
}

func TestTaskServiceWorkflowAttemptFailurePersistenceIsRetryable(t *testing.T) {
	agent := newTaskTestAgent(t)
	stateDir := t.TempDir()
	permission := NewAIPermissionService(stateDir)
	lifecycle := wireWorkflowAttemptTestLifecycle(t, agent, permission, stateDir)
	task := NewTaskService(agent)
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "retry-failure-terminal-write")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	pending := pendingWorkflowAttemptRecords(permission, sessionID)
	if len(pending) != 1 {
		t.Fatalf("pending workflow attempts = %+v, want one", pending)
	}

	ledgerPath := filepath.Join(stateDir, "usage_log.jsonl")
	ledger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read pending ledger: %v", err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatalf("remove pending ledger: %v", err)
	}
	if err := os.Mkdir(ledgerPath, 0o700); err != nil {
		t.Fatalf("replace ledger with blocking directory: %v", err)
	}
	firstErr := task.FailWorkflowExecution(trustedTaskContext(), sessionID, "expected workflow failure")
	if !errors.Is(firstErr, ErrUsagePersistence) {
		t.Fatalf("first failure publication error = %v, want ErrUsagePersistence", firstErr)
	}
	session, err := lifecycle.GetByID(sessionID)
	if err != nil || session.Status != agentcore.SessionRunning {
		t.Fatalf("failed failure publication changed lifecycle authority: session=%+v err=%v", session, err)
	}
	if got := pendingWorkflowAttemptRecords(permission, sessionID); len(got) != 1 || got[0].UnitID != pending[0].UnitID {
		t.Fatalf("failed failure publication consumed the pending receipt: %+v", got)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatalf("remove blocking ledger directory: %v", err)
	}
	if err := os.WriteFile(ledgerPath, ledger, 0o600); err != nil {
		t.Fatalf("restore pending ledger: %v", err)
	}

	if err := task.FailWorkflowExecution(trustedTaskContext(), sessionID, "expected workflow failure"); err != nil {
		t.Fatalf("retry failure with the original receipt: %v", err)
	}
	rows := permission.usageRecordsSnapshot()
	terminal := workflowAttemptRecordByUnitID(rows, pending[0].UnitID)
	if terminal == nil || terminal.Pending || terminal.Success {
		t.Fatalf("retry did not publish one failed terminal attempt: %+v", rows)
	}
	session, err = lifecycle.GetByID(sessionID)
	if err != nil || session.Status != agentcore.SessionFailed {
		t.Fatalf("retry did not fail lifecycle consistently: session=%+v err=%v", session, err)
	}
}

func TestTaskServiceWorkflowAttemptRawIncompletePendingPoisonsReload(t *testing.T) {
	stateDir := t.TempDir()
	ledgerPath := filepath.Join(stateDir, "usage_log.jsonl")
	raw := []byte(`{"sessionId":"workflow:forged","unitKind":"workflow","operation":"workflow.attempt","pending":true}` + "\n")
	if err := os.WriteFile(ledgerPath, raw, 0o600); err != nil {
		t.Fatalf("write incomplete pending ledger row: %v", err)
	}

	permission := NewAIPermissionService(stateDir)
	if rows := permission.usageRecordsSnapshot(); len(rows) != 0 {
		t.Fatalf("incomplete pending row was normalized into authority: %+v", rows)
	}
	if _, _, err := permission.pendingWorkflowUsageAttempt("workflow:forged"); !errors.Is(err, ErrUsagePersistencePoisoned) {
		t.Fatalf("incomplete pending reload error = %v, want ErrUsagePersistencePoisoned", err)
	}
}

func TestTaskServiceWorkflowAttemptRejectsAmbiguousPendingRows(t *testing.T) {
	agent := newTaskTestAgent(t)
	stateDir := t.TempDir()
	permission := NewAIPermissionService(stateDir)
	lifecycle := wireWorkflowAttemptTestLifecycle(t, agent, permission, stateDir)
	task := NewTaskService(agent)
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "ambiguous-attempt")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	startedAt := time.Now().UTC()
	if _, err := permission.beginAgentUsage(agentcore.UsageRecord{
		UnitID:    "workflow-unit-duplicate",
		SessionID: sessionID,
		UnitKind:  agentcore.UsageUnitWorkflow,
		Operation: "workflow.attempt",
		CostBasis: agentcore.CostNotApplicable,
		StartedAt: startedAt, CompletedAt: startedAt, Pending: true,
	}); err != nil {
		t.Fatalf("seed duplicate pending attempt: %v", err)
	}

	err = task.CompleteWorkflowExecution(trustedTaskContext(), sessionID)
	if !errors.Is(err, ErrUsageReceiptState) {
		t.Fatalf("ambiguous completion error = %v, want ErrUsageReceiptState", err)
	}
	if strings.Contains(err.Error(), "workflow-unit-duplicate") {
		t.Fatalf("ambiguous completion leaked a durable unit ID: %v", err)
	}
	if got := pendingWorkflowAttemptRecords(permission, sessionID); len(got) != 2 {
		t.Fatalf("ambiguous attempts were mutated: %+v", got)
	}
}

func TestTaskServiceWorkflowAttemptReloadDoesNotRestoreRuntimeAuthority(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	firstAgent := newTaskTestAgentAtRoot(t, root)
	firstPermission := NewAIPermissionService(stateDir)
	firstLifecycle := wireWorkflowAttemptTestLifecycle(t, firstAgent, firstPermission, stateDir)
	firstTask := NewTaskService(firstAgent)
	if err := WireTaskAgentLifecycle(firstTask, firstLifecycle); err != nil {
		t.Fatalf("wire first TaskService: %v", err)
	}
	sessionID, err := firstTask.BeginWorkflowExecution(trustedTaskContext(), "restart-authority")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	ledgerPath := filepath.Join(stateDir, "usage_log.jsonl")
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read pending ledger: %v", err)
	}

	secondAgent := newTaskTestAgentAtRoot(t, root)
	secondPermission := NewAIPermissionService(stateDir)
	secondLifecycle := wireWorkflowAttemptTestLifecycle(t, secondAgent, secondPermission, stateDir)
	secondTask := NewTaskService(secondAgent)
	if err := WireTaskAgentLifecycle(secondTask, secondLifecycle); err != nil {
		t.Fatalf("wire reloaded TaskService: %v", err)
	}
	err = secondTask.CompleteWorkflowExecution(trustedTaskContext(), sessionID)
	if err == nil || strings.Contains(err.Error(), "attempt is missing") {
		t.Fatalf("reloaded completion error = %v, want recovered attempt with revoked runtime authority", err)
	}
	after, readErr := os.ReadFile(ledgerPath)
	if readErr != nil {
		t.Fatalf("read ledger after rejected completion: %v", readErr)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("rejected reloaded completion mutated the usage ledger")
	}
	if got := pendingWorkflowAttemptRecords(secondPermission, sessionID); len(got) != 1 {
		t.Fatalf("reloaded pending attempt changed after authority rejection: %+v", got)
	}
}

func TestTaskServiceWorkflowAttemptRejectsMalformedPendingIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*UsageRecord)
	}{
		{name: "wrong operation", mutate: func(record *UsageRecord) { record.Operation = "workflow.other" }},
		{name: "wrong kind", mutate: func(record *UsageRecord) { record.UnitKind = string(agentcore.UsageUnitTool) }},
		{name: "unexpected provider", mutate: func(record *UsageRecord) { record.ProviderID = "forged-provider" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := newTaskTestAgent(t)
			stateDir := t.TempDir()
			permission := NewAIPermissionService(stateDir)
			lifecycle := wireWorkflowAttemptTestLifecycle(t, agent, permission, stateDir)
			task := NewTaskService(agent)
			if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
				t.Fatalf("WireTaskAgentLifecycle: %v", err)
			}
			sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "malformed-attempt")
			if err != nil {
				t.Fatalf("BeginWorkflowExecution: %v", err)
			}
			startedAt := time.Now().UTC()
			forged := UsageRecord{
				UnitID: "workflow-unit-forged", SessionID: sessionID,
				UnitKind: string(agentcore.UsageUnitWorkflow), Operation: "workflow.attempt",
				CostBasis: string(agentcore.CostNotApplicable), StartedAt: startedAt,
				CompletedAt: startedAt, Timestamp: startedAt, Pending: true,
			}
			test.mutate(&forged)
			if err := permission.recordUsageTrusted(forged); err != nil {
				t.Fatalf("seed malformed pending identity: %v", err)
			}
			ledgerPath := filepath.Join(stateDir, "usage_log.jsonl")
			before, err := os.ReadFile(ledgerPath)
			if err != nil {
				t.Fatalf("read ledger before rejection: %v", err)
			}
			err = task.CompleteWorkflowExecution(trustedTaskContext(), sessionID)
			if !errors.Is(err, ErrUsageReceiptState) {
				t.Fatalf("malformed completion error = %v, want ErrUsageReceiptState", err)
			}
			if strings.Contains(err.Error(), forged.UnitID) {
				t.Fatalf("malformed completion leaked durable unit ID: %v", err)
			}
			after, readErr := os.ReadFile(ledgerPath)
			if readErr != nil {
				t.Fatalf("read ledger after rejection: %v", readErr)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("malformed completion mutated the usage ledger")
			}
		})
	}
}

func TestTaskServiceWorkflowAttemptConcurrentCompletePublishesOneTerminal(t *testing.T) {
	agent := newTaskTestAgent(t)
	stateDir := t.TempDir()
	permission := NewAIPermissionService(stateDir)
	lifecycle := wireWorkflowAttemptTestLifecycle(t, agent, permission, stateDir)
	task := NewTaskService(agent)
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "concurrent-complete")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	pending := pendingWorkflowAttemptRecords(permission, sessionID)
	if len(pending) != 1 {
		t.Fatalf("pending workflow attempts = %+v, want one", pending)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			results <- task.CompleteWorkflowExecution(trustedTaskContext(), sessionID)
		}()
	}
	ready.Wait()
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent completion successes = %d, want exactly one", successes)
	}
	rows := permission.usageRecordsSnapshot()
	terminal := workflowAttemptRecordByUnitID(rows, pending[0].UnitID)
	if terminal == nil || terminal.Pending || !terminal.Success {
		t.Fatalf("concurrent completion did not preserve one successful terminal: %+v", rows)
	}
	session, err := lifecycle.GetByID(sessionID)
	if err != nil || session.Status != agentcore.SessionCompleted {
		t.Fatalf("concurrent completion left lifecycle non-terminal: %+v, err=%v", session, err)
	}
}

func TestTaskServiceWorkflowAttemptTerminalBlocksConcurrentToolAdmission(t *testing.T) {
	agent := newTaskTestAgent(t)
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	stateDir := t.TempDir()
	permission := NewAIPermissionService(stateDir)
	lifecycle := wireWorkflowAttemptTestLifecycle(t, agent, permission, stateDir)
	task := NewTaskService(agent)
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "terminal-admission-barrier")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	token, err := task.RequestExecutionApproval(trustedTaskContext(), sessionID, "go version", "")
	if err != nil {
		t.Fatalf("RequestExecutionApproval: %v", err)
	}

	lookupReached := make(chan struct{})
	releaseTerminal := make(chan struct{})
	task.workflowAttemptLookupHook = func() {
		close(lookupReached)
		<-releaseTerminal
	}
	usagePublished := make(chan struct{})
	var usagePublishedOnce sync.Once
	permission.usageAppendHook = func(stage string) error {
		if stage == "after-write" {
			usagePublishedOnce.Do(func() { close(usagePublished) })
		}
		return nil
	}

	completeDone := make(chan error, 1)
	go func() { completeDone <- task.CompleteWorkflowExecution(trustedTaskContext(), sessionID) }()
	select {
	case <-lookupReached:
	case <-time.After(5 * time.Second):
		t.Fatal("workflow completion did not reach the post-lookup barrier")
	}

	executeStarted := make(chan struct{})
	executeDone := make(chan error, 1)
	go func() {
		close(executeStarted)
		_, executeErr := task.ExecuteApproved(trustedTaskContext(), sessionID, "go version", "", token)
		executeDone <- executeErr
	}()
	<-executeStarted
	crossedTerminalBoundary := false
	select {
	case <-usagePublished:
		crossedTerminalBoundary = true
	case <-time.After(2 * time.Second):
	}
	close(releaseTerminal)
	completeErr := <-completeDone
	executeErr := <-executeDone
	permission.usageAppendHook = nil
	task.workflowAttemptLookupHook = nil

	if crossedTerminalBoundary {
		t.Fatal("workflow tool published usage after terminal lookup but before terminal publication")
	}
	if completeErr != nil {
		t.Fatalf("CompleteWorkflowExecution: %v", completeErr)
	}
	if executeErr == nil {
		t.Fatal("workflow tool executed after lifecycle terminal publication")
	}
	for _, record := range permission.usageRecordsSnapshot() {
		if record.SessionID == sessionID && record.UnitKind == string(agentcore.UsageUnitTool) {
			t.Fatalf("tool usage crossed workflow terminal boundary: %+v", record)
		}
	}
}

func TestTaskServiceWorkflowAttemptCrossInstanceExecutionReleaseDoesNotDeadlock(t *testing.T) {
	agent := newTaskTestAgent(t)
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	stateDir := t.TempDir()
	permission := NewAIPermissionService(stateDir)
	lifecycle := wireWorkflowAttemptTestLifecycle(t, agent, permission, stateDir)
	first := NewTaskService(agent)
	second := NewTaskService(agent)
	if err := WireTaskAgentLifecycle(first, lifecycle); err != nil {
		t.Fatalf("Wire first TaskService: %v", err)
	}
	if err := WireTaskAgentLifecycle(second, lifecycle); err != nil {
		t.Fatalf("Wire second TaskService: %v", err)
	}
	sessionID, err := first.BeginWorkflowExecution(trustedTaskContext(), "cross-instance-release")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	token, err := first.RequestExecutionApproval(trustedTaskContext(), sessionID, "go version", "")
	if err != nil {
		t.Fatalf("RequestExecutionApproval: %v", err)
	}

	executionReturned := make(chan struct{})
	releaseExecution := make(chan struct{})
	var releaseExecutionOnce sync.Once
	first.workflowExecutionReturnHook = func() {
		close(executionReturned)
		<-releaseExecution
	}
	terminalLookup := make(chan struct{})
	releaseTerminal := make(chan struct{})
	var releaseTerminalOnce sync.Once
	second.workflowAttemptLookupHook = func() {
		close(terminalLookup)
		<-releaseTerminal
	}
	defer func() {
		releaseExecutionOnce.Do(func() { close(releaseExecution) })
		releaseTerminalOnce.Do(func() { close(releaseTerminal) })
		first.workflowExecutionReturnHook = nil
		second.workflowAttemptLookupHook = nil
	}()

	executeDone := make(chan error, 1)
	go func() {
		_, executeErr := first.ExecuteApproved(trustedTaskContext(), sessionID, "go version", "", token)
		executeDone <- executeErr
	}()
	select {
	case <-executionReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not reach post-core return hook")
	}

	completeDone := make(chan error, 1)
	go func() { completeDone <- second.CompleteWorkflowExecution(trustedTaskContext(), sessionID) }()
	select {
	case <-terminalLookup:
	case <-time.After(5 * time.Second):
		t.Fatal("second TaskService did not acquire workflow terminal barrier")
	}

	// Keep the terminal writer held while execution cleanup runs. A cleanup
	// path that re-enters Agent core to burn the already-consumed token would
	// wait behind this writer and deadlock the two TaskService instances.
	releaseExecutionOnce.Do(func() { close(releaseExecution) })
	select {
	case executeErr := <-executeDone:
		if executeErr != nil {
			t.Fatalf("cross-instance ExecuteApproved: %v", executeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cross-instance execution cleanup blocked behind terminal writer")
	}
	releaseTerminalOnce.Do(func() { close(releaseTerminal) })
	select {
	case completeErr := <-completeDone:
		if completeErr != nil {
			t.Fatalf("cross-instance CompleteWorkflowExecution: %v", completeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cross-instance terminal transition did not complete")
	}
}

func TestTaskServiceWorkflowAttemptTerminalBlocksDirectAgentToolAdmission(t *testing.T) {
	agent := newTaskTestAgent(t)
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	stateDir := t.TempDir()
	permission := NewAIPermissionService(stateDir)
	lifecycle := wireWorkflowAttemptTestLifecycle(t, agent, permission, stateDir)
	task := NewTaskService(agent)
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "direct-agent-terminal-barrier")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}

	lookupReached := make(chan struct{})
	releaseTerminal := make(chan struct{})
	task.workflowAttemptLookupHook = func() {
		close(lookupReached)
		<-releaseTerminal
	}
	usagePublished := make(chan struct{})
	var usagePublishedOnce sync.Once
	permission.usageAppendHook = func(stage string) error {
		if stage == "after-write" {
			usagePublishedOnce.Do(func() { close(usagePublished) })
		}
		return nil
	}

	completeDone := make(chan error, 1)
	go func() { completeDone <- task.CompleteWorkflowExecution(trustedTaskContext(), sessionID) }()
	select {
	case <-lookupReached:
	case <-time.After(5 * time.Second):
		t.Fatal("workflow completion did not reach the post-lookup barrier")
	}

	executeStarted := make(chan struct{})
	executeDone := make(chan error, 1)
	go func() {
		close(executeStarted)
		_, executeErr := agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
			SessionID:       sessionID,
			CatalogRevision: catalog.Revision,
			ToolID:          "run",
			Arguments:       taskAgentRunArguments("go version", ""),
		})
		executeDone <- executeErr
	}()
	<-executeStarted
	crossedTerminalBoundary := false
	select {
	case <-usagePublished:
		crossedTerminalBoundary = true
	case <-time.After(2 * time.Second):
	}
	close(releaseTerminal)
	completeErr := <-completeDone
	executeErr := <-executeDone
	permission.usageAppendHook = nil
	task.workflowAttemptLookupHook = nil

	if crossedTerminalBoundary {
		t.Fatal("direct Agent tool published usage after workflow terminal lookup")
	}
	if completeErr != nil {
		t.Fatalf("CompleteWorkflowExecution: %v", completeErr)
	}
	if executeErr == nil {
		t.Fatal("direct Agent tool executed after workflow lifecycle terminal publication")
	}
	for _, record := range permission.usageRecordsSnapshot() {
		if record.SessionID == sessionID && record.UnitKind == string(agentcore.UsageUnitTool) {
			t.Fatalf("direct tool usage crossed workflow terminal boundary: %+v", record)
		}
	}
}

func newTaskTestAgentAtRoot(t *testing.T, root string) *AgentService {
	t.Helper()
	workspace := NewWorkspaceContext()
	if err := workspace.Set(root); err != nil {
		t.Fatalf("workspace.Set: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(root); err != nil {
		t.Fatalf("configure workspace root: %v", err)
	}
	return agent
}

func pendingWorkflowAttemptRecords(permission *AIPermissionService, sessionID string) []UsageRecord {
	rows := permission.usageRecordsSnapshot()
	pending := make([]UsageRecord, 0)
	for _, row := range rows {
		if row.SessionID == sessionID && row.UnitKind == string(agentcore.UsageUnitWorkflow) &&
			row.Operation == AIOperation("workflow.attempt") && row.Pending {
			pending = append(pending, row)
		}
	}
	return pending
}

func workflowAttemptRecordByUnitID(rows []UsageRecord, unitID string) *UsageRecord {
	for index := range rows {
		if rows[index].UnitID == unitID {
			return &rows[index]
		}
	}
	return nil
}
