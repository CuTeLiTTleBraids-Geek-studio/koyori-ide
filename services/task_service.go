package services

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

// TaskDef describes a single named command runnable from the UI.
// It mirrors a subset of VS Code's tasks.json schema (simplified).
type TaskDef struct {
	Label          string            `json:"label"`
	Type           string            `json:"type,omitempty"`
	Command        string            `json:"command"`
	Args           []string          `json:"args,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	DependsOn      []string          `json:"dependsOn,omitempty"`
	Group          string            `json:"group,omitempty"`
	ProblemMatcher []string          `json:"problemMatcher,omitempty"`
	// Shell, when true, runs the command line through the user's shell
	// (`sh -c` on unix, `cmd /c` on windows). When false (default), the
	// command is executed directly. The frontend currently always uses
	// shell mode by writing the command into a terminal session, so this
	// field is informational for future non-terminal execution.
	Shell bool `json:"shell,omitempty"`
}

// TaskFile is the on-disk schema for .koyori-ide/tasks.json.
type TaskFile struct {
	Version string       `json:"version"`
	Tasks   []rawTaskDef `json:"tasks"`
}

type rawTaskDef struct {
	Label          string            `json:"label"`
	Type           string            `json:"type,omitempty"`
	Command        string            `json:"command"`
	Args           []string          `json:"args,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	DependsOn      json.RawMessage   `json:"dependsOn,omitempty"`
	Group          json.RawMessage   `json:"group,omitempty"`
	ProblemMatcher json.RawMessage   `json:"problemMatcher,omitempty"`
	Shell          bool              `json:"shell,omitempty"`
	Options        struct {
		Cwd string            `json:"cwd,omitempty"`
		Env map[string]string `json:"env,omitempty"`
	} `json:"options,omitempty"`
}

const (
	taskExecutionTimeout = 30 * time.Second
	// Keep the full two-second budget for taskkill's process-tree operation;
	// the outer single-flight Stop call needs a small handoff margin for the
	// direct Process.Kill fallback and waiter notification.
	taskTerminationCommandLimit = 2 * time.Second
	taskTerminationCallLimit    = taskTerminationCommandLimit + time.Second
	taskTerminationWait         = 2 * time.Second
	taskPendingStopTTL          = time.Minute
	taskPendingStopLimit        = 256
	taskApprovalLimit           = 256
	taskExecutionIDMaxLength    = 256
)

type taskStopReason uint8

const (
	taskNotStopped taskStopReason = iota
	taskStoppedByCaller
	taskStoppedByTimeout
)

type taskExecution struct {
	mu                   sync.Mutex
	cancel               context.CancelFunc
	cmd                  *exec.Cmd
	terminateProcess     func(*os.Process) error
	stopReason           taskStopReason
	terminationSucceeded bool
	terminationAttempt   *taskTerminationAttempt
	finished             bool
	done                 chan struct{}
}

type taskTerminationAttempt struct {
	done chan struct{}
	err  error
}

func (e *taskExecution) start(cmd *exec.Cmd) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopReason != taskNotStopped {
		e.finishLocked()
		return context.Canceled
	}
	e.cmd = cmd
	err := cmd.Start()
	if err != nil {
		e.finishLocked()
	}
	return err
}

func (e *taskExecution) stop(reason taskStopReason, callLimit time.Duration) error {
	e.mu.Lock()
	if e.finished {
		e.mu.Unlock()
		return nil
	}
	if e.stopReason == taskNotStopped {
		e.stopReason = reason
		e.cancel()
	}
	if e.cmd == nil || e.cmd.Process == nil {
		e.mu.Unlock()
		return nil
	}
	if e.terminationSucceeded {
		e.mu.Unlock()
		return nil
	}
	attempt := e.terminationAttempt
	if attempt == nil {
		attempt = &taskTerminationAttempt{done: make(chan struct{})}
		e.terminationAttempt = attempt
		process := e.cmd.Process
		terminateProcess := e.terminateProcess
		go func() {
			attempt.err = normalizeTaskTerminationError(terminateProcess(process))
			e.mu.Lock()
			if attempt.err == nil {
				e.terminationSucceeded = true
			} else if e.terminationAttempt == attempt {
				// A later Stop may retry a completed transient failure.
				e.terminationAttempt = nil
			}
			close(attempt.done)
			e.mu.Unlock()
		}()
	}
	e.mu.Unlock()

	if callLimit <= 0 {
		callLimit = taskTerminationCallLimit
	}
	timer := time.NewTimer(callLimit)
	defer timer.Stop()
	select {
	case <-attempt.done:
		return attempt.err
	case <-timer.C:
		return fmt.Errorf("process termination attempt did not complete within %s", callLimit)
	}
}

func (e *taskExecution) finish() {
	e.mu.Lock()
	e.finishLocked()
	e.mu.Unlock()
}

func (e *taskExecution) finishLocked() {
	if e.finished {
		return
	}
	e.finished = true
	if e.done != nil {
		close(e.done)
	}
}

func (e *taskExecution) stopped() taskStopReason {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopReason
}

// TaskService exposes project-scoped task definitions and tracked task
// execution to the frontend. Tracked executions use caller-provided opaque
// IDs so Stop can target exactly one process, including with concurrent
// extensions, windows, and HMR runtimes.
type TaskService struct {
	mu                   sync.Mutex
	agent                *AgentService
	active               map[string]*taskExecution
	pendingStops         map[string]time.Time
	executionLimit       time.Duration
	terminationCallLimit time.Duration
	terminationWait      time.Duration
	terminateProcess     func(*os.Process) error
	shuttingDown         bool
	approvals            map[string]taskExecutionApproval
	lifecycle            *AgentLifecycle
	workflowAttemptMu    sync.RWMutex
	// workflowAttemptLookupHook is a deterministic package-test hook for the
	// scan-to-terminal admission boundary. Production wiring leaves it nil.
	workflowAttemptLookupHook func()
	// workflowExecutionReturnHook is a deterministic package-test hook for the
	// window after Agent core has released its shared read lock and before the
	// approval cleanup runs. Production wiring leaves it nil.
	workflowExecutionReturnHook func()
}

type workflowUsageAttempt struct {
	receipt   agentcore.UsageReceipt
	startedAt time.Time
}

type taskExecutionApproval struct {
	executionID string
	sessionID   string
	ownsSession bool
	owner       string
	capability  AgentToolCapability
	expiresAt   time.Time
	workflow    string
	step        string
	adapter     string
	command     string
	cwd         string
	arguments   map[string]interface{}
	risk        RiskLevel
}

// NewTaskService creates a new TaskService. The variadic parameter preserves
// compatibility with callers that only use LoadTasks; Execute requires an
// injected AgentService so it can apply the same command policy, cwd sandbox,
// and audit logging as AgentService.ExecCommand.
func NewTaskService(agent ...*AgentService) *TaskService {
	var injected *AgentService
	if len(agent) > 0 {
		injected = agent[0]
	}
	return &TaskService{
		agent:                injected,
		active:               make(map[string]*taskExecution),
		pendingStops:         make(map[string]time.Time),
		executionLimit:       taskExecutionTimeout,
		terminationCallLimit: taskTerminationCallLimit,
		terminationWait:      taskTerminationWait,
		terminateProcess:     terminateTaskProcessTree,
		approvals:            make(map[string]taskExecutionApproval),
	}
}

// setAgentLifecycle is trusted bootstrap wiring. Renderer calls only receive
// the explicit workflow lifecycle methods below; they cannot replace the
// shared lifecycle instance.
func (s *TaskService) setAgentLifecycle(lifecycle *AgentLifecycle) {
	s.mu.Lock()
	s.lifecycle = lifecycle
	s.mu.Unlock()
}

// BeginWorkflowExecution creates one backend-owned session for the complete
// workflow run. Every sequential step reuses this ID, so tool usage records,
// checkpoints, retries, and terminal state aggregate in one SessionStore row.
func (s *TaskService) BeginWorkflowExecution(ctx context.Context, workflowName string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("workflow caller context is required: %w", ErrNotAllowed)
	}
	if strings.TrimSpace(workflowName) == "" {
		return "", fmt.Errorf("workflow name is required: %w", ErrInvalidInput)
	}
	s.workflowAttemptMu.Lock()
	defer s.workflowAttemptMu.Unlock()
	s.mu.Lock()
	agent, lifecycle := s.agent, s.lifecycle
	s.mu.Unlock()
	if agent == nil || lifecycle == nil {
		return "", fmt.Errorf("workflow lifecycle is unavailable: %w", ErrNotAllowed)
	}
	unlockWorkflow := lockAgentWorkflowTransition(agent)
	defer unlockWorkflow()
	var sessionID string
	var err error
	if _, hasCaller := agentOwnerForContext(ctx); hasCaller {
		sessionID, err = agent.CreateAgentSessionForCaller(ctx, "workflow")
	} else {
		sessionID, err = agent.createAgentSessionTrusted("workflow")
	}
	if err != nil {
		return "", err
	}
	if _, err := lifecycle.BeginExisting(agentcore.SessionWorkflow, sessionID); err != nil {
		_ = agent.closeAgentSessionTrusted(sessionID)
		return "", err
	}
	if _, err := lifecycle.Checkpoint(agentcore.SessionWorkflow, sessionID, "workflow-started", map[string]interface{}{
		"workflow": workflowName,
		"phase":    "workflow-started",
	}); err != nil {
		_ = lifecycle.Fail(agentcore.SessionWorkflow, sessionID, err)
		_ = recordWorkflowUsage(lifecycle, sessionID, "workflow.failed", false, err)
		_ = agent.closeAgentSessionTrusted(sessionID)
		return "", err
	}
	if err := s.beginWorkflowUsageAttemptLocked(lifecycle, sessionID); err != nil {
		_ = lifecycle.Fail(agentcore.SessionWorkflow, sessionID, err)
		_ = agent.closeAgentSessionTrusted(sessionID)
		return "", err
	}
	return sessionID, nil
}

func (s *TaskService) CompleteWorkflowExecution(ctx context.Context, sessionID string) error {
	lifecycle := s.workflowLifecycle()
	if err := validateWorkflowSessionID(sessionID); err != nil {
		return err
	}
	if lifecycle == nil {
		return fmt.Errorf("workflow lifecycle is unavailable: %w", ErrNotAllowed)
	}
	s.mu.Lock()
	agent := s.agent
	s.mu.Unlock()
	if agent == nil {
		return fmt.Errorf("workflow agent is unavailable: %w", ErrNotAllowed)
	}
	if err := authorizeTaskWorkflowCaller(agent, ctx, sessionID); err != nil {
		return err
	}
	s.workflowAttemptMu.Lock()
	defer s.workflowAttemptMu.Unlock()
	unlockWorkflow := lockAgentWorkflowTransition(agent)
	defer unlockWorkflow()
	// Persist the terminal usage row before revoking the runtime session. A
	// pre-publication metering failure leaves the session running so the public
	// operation can retry with the same durable receipt.
	if err := s.completeWorkflowUsageAttemptLocked(lifecycle, sessionID, true, nil); err != nil {
		return err
	}
	if _, err := lifecycle.Checkpoint(agentcore.SessionWorkflow, sessionID, "workflow-completed", map[string]interface{}{
		"phase": "workflow-completed",
	}); err != nil {
		return err
	}
	return lifecycle.Complete(agentcore.SessionWorkflow, sessionID)
}

func (s *TaskService) FailWorkflowExecution(ctx context.Context, sessionID, reason string) error {
	lifecycle := s.workflowLifecycle()
	if err := validateWorkflowSessionID(sessionID); err != nil {
		return err
	}
	if lifecycle == nil {
		return fmt.Errorf("workflow lifecycle is unavailable: %w", ErrNotAllowed)
	}
	s.mu.Lock()
	agent := s.agent
	s.mu.Unlock()
	if agent == nil {
		return fmt.Errorf("workflow agent is unavailable: %w", ErrNotAllowed)
	}
	if err := authorizeTaskWorkflowCaller(agent, ctx, sessionID); err != nil {
		return err
	}
	s.workflowAttemptMu.Lock()
	defer s.workflowAttemptMu.Unlock()
	unlockWorkflow := lockAgentWorkflowTransition(agent)
	defer unlockWorkflow()
	if strings.TrimSpace(reason) == "" {
		reason = "workflow execution failed"
	}
	failure := errors.New(reason)
	if err := s.completeWorkflowUsageAttemptLocked(lifecycle, sessionID, false, failure); err != nil {
		return err
	}
	return lifecycle.Fail(agentcore.SessionWorkflow, sessionID, failure)
}

func (s *TaskService) beginWorkflowUsageAttempt(lifecycle *AgentLifecycle, sessionID string) error {
	s.workflowAttemptMu.Lock()
	defer s.workflowAttemptMu.Unlock()
	return s.beginWorkflowUsageAttemptLocked(lifecycle, sessionID)
}

func (s *TaskService) beginWorkflowUsageAttemptLocked(lifecycle *AgentLifecycle, sessionID string) error {
	if _, exists, err := pendingWorkflowUsageAttempt(lifecycle, sessionID); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("workflow usage attempt already active: %w", ErrAlreadyExists)
	}
	startedAt := lifecycle.now()
	_, err := lifecycle.BeginUsage(agentcore.UsageRecord{
		UnitID:    lifecycle.newUnitID(agentcore.UsageUnitWorkflow, startedAt),
		SessionID: sessionID, UnitKind: agentcore.UsageUnitWorkflow,
		Operation: "workflow.attempt", CostBasis: agentcore.CostNotApplicable,
		StartedAt: startedAt, CompletedAt: startedAt, Pending: true,
	})
	return err
}

func (s *TaskService) completeWorkflowUsageAttempt(lifecycle *AgentLifecycle, sessionID string, success bool, failure error) error {
	s.workflowAttemptMu.Lock()
	defer s.workflowAttemptMu.Unlock()
	return s.completeWorkflowUsageAttemptLocked(lifecycle, sessionID, success, failure)
}

func (s *TaskService) completeWorkflowUsageAttemptLocked(lifecycle *AgentLifecycle, sessionID string, success bool, failure error) error {
	attempt, exists, err := pendingWorkflowUsageAttempt(lifecycle, sessionID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("workflow usage attempt is missing: %w", ErrNotAllowed)
	}
	if s.workflowAttemptLookupHook != nil {
		s.workflowAttemptLookupHook()
	}
	record := agentcore.UsageRecord{
		UnitID:    attempt.receipt.UnitID,
		SessionID: sessionID, UnitKind: agentcore.UsageUnitWorkflow,
		Operation: "workflow.attempt", CostBasis: agentcore.CostNotApplicable,
		StartedAt: attempt.startedAt, CompletedAt: lifecycle.now(), Success: success,
	}
	if !success && failure != nil {
		record.Error = failure.Error()
	}
	return lifecycle.CompleteUsage(attempt.receipt, record)
}

func pendingWorkflowUsageAttempt(lifecycle *AgentLifecycle, sessionID string) (workflowUsageAttempt, bool, error) {
	if lifecycle == nil || lifecycle.permission == nil {
		return workflowUsageAttempt{}, false, fmt.Errorf("workflow usage ledger is unavailable: %w", ErrNotAllowed)
	}
	record, exists, err := lifecycle.permission.pendingWorkflowUsageAttempt(sessionID)
	if err != nil {
		return workflowUsageAttempt{}, false, err
	}
	if !exists {
		return workflowUsageAttempt{}, false, nil
	}
	return workflowUsageAttempt{
		receipt:   agentcore.UsageReceipt{UnitID: record.UnitID},
		startedAt: record.StartedAt,
	}, true, nil
}

func recordWorkflowUsage(lifecycle *AgentLifecycle, sessionID, operation string, success bool, failure error) error {
	if lifecycle == nil {
		return fmt.Errorf("workflow lifecycle is unavailable: %w", ErrNotAllowed)
	}
	session, err := lifecycle.GetByID(sessionID)
	if err != nil {
		return err
	}
	completedAt := lifecycle.now()
	record := agentcore.UsageRecord{
		SessionID: sessionID, UnitKind: agentcore.UsageUnitWorkflow,
		Operation: operation, CostBasis: agentcore.CostNotApplicable,
		StartedAt: session.StartedAt, CompletedAt: completedAt, Success: success,
	}
	if !success && failure != nil {
		record.Error = failure.Error()
	}
	return lifecycle.Record(record)
}

func (s *TaskService) ResumeWorkflowExecution(ctx context.Context, sessionID string) error {
	lifecycle := s.workflowLifecycle()
	if err := validateWorkflowSessionID(sessionID); err != nil {
		return err
	}
	if lifecycle == nil {
		return fmt.Errorf("workflow lifecycle is unavailable: %w", ErrNotAllowed)
	}
	s.workflowAttemptMu.Lock()
	defer s.workflowAttemptMu.Unlock()
	s.mu.Lock()
	agent := s.agent
	s.mu.Unlock()
	if agent == nil {
		return fmt.Errorf("workflow agent is unavailable: %w", ErrNotAllowed)
	}
	if err := authorizeTaskWorkflowCaller(agent, ctx, sessionID); err != nil {
		return err
	}
	unlockWorkflow := lockAgentWorkflowTransition(agent)
	defer unlockWorkflow()
	session, err := lifecycle.GetByID(sessionID)
	if err != nil {
		return err
	}
	// Resume is only valid for a non-running lifecycle row. ResumeLatest is
	// intentionally idempotent for Running, but treating that no-op as a new
	// attempt would leave the existing pending receipt alongside a failed row
	// when beginWorkflowUsageAttempt rejects the duplicate.
	if session.Status == agentcore.SessionRunning {
		return fmt.Errorf("workflow execution is already running: %w", ErrAlreadyExists)
	}
	if _, exists, err := pendingWorkflowUsageAttempt(lifecycle, sessionID); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("workflow usage attempt already active: %w", ErrAlreadyExists)
	}
	if err := lifecycle.ResumeLatest(agentcore.SessionWorkflow, sessionID); err != nil {
		return err
	}
	if err := s.beginWorkflowUsageAttemptLocked(lifecycle, sessionID); err != nil {
		return errors.Join(err, lifecycle.Fail(agentcore.SessionWorkflow, sessionID, err))
	}
	return nil
}

func (s *TaskService) workflowLifecycle() *AgentLifecycle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lifecycle
}

func validateWorkflowSessionID(sessionID string) error {
	if strings.TrimSpace(sessionID) == "" || !strings.HasPrefix(sessionID, "workflow:") {
		return fmt.Errorf("invalid workflow session ID: %w", ErrInvalidInput)
	}
	if len(sessionID) > 256 {
		return fmt.Errorf("workflow session ID is too long: %w", ErrInvalidInput)
	}
	return nil
}

// Execute is kept as a deny-only Wails endpoint. Renderer calls must request a
// backend-issued command capability and use ExecuteApproved.
func (s *TaskService) Execute(executionID, command, cwd string) (ExecResult, error) {
	return ExecResult{Command: command, Cwd: cwd, Blocked: true, BlockReason: "backend approval token required"}, fmt.Errorf("backend approval token required: %w", ErrInvalidInput)
}

func (s *TaskService) RequestExecutionApproval(ctx context.Context, executionID, command, cwd string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("task caller context is required: %w", ErrNotAllowed)
	}
	if err := validateTaskExecutionID(executionID); err != nil {
		return "", err
	}
	s.mu.Lock()
	agent := s.agent
	s.mu.Unlock()
	if agent == nil {
		return "", fmt.Errorf("agent service not injected: %w", ErrInvalidInput)
	}
	if strings.HasPrefix(executionID, "workflow:") {
		if err := authorizeTaskWorkflowCaller(agent, ctx, executionID); err != nil {
			return "", err
		}
	}
	if err := s.preflightTaskApproval(); err != nil {
		return "", err
	}
	sessionID, ownsSession, err := s.acquireTaskApprovalSession(agent, executionID, ctx)
	if err != nil {
		return "", err
	}
	pending := taskExecutionApproval{
		executionID: executionID,
		sessionID:   sessionID,
		ownsSession: ownsSession,
		owner:       agentCallerIdentityValue(ctx),
	}
	grant, err := agent.requestInternalAgentToolCapability(
		ctx, sessionID, "run", taskAgentRunArguments(command, cwd),
	)
	if err != nil {
		return "", errors.Join(err, s.releaseTaskApproval(pending))
	}
	pending.capability = grant
	pending.expiresAt = grant.ExpiresAt
	return s.storeTaskApproval(pending)
}

// RequestWorkflowStepApproval resolves execution from the current workflow
// ToolDef catalog. The renderer supplies only stable workflow/step identity;
// adapter inputs, command, cwd, and paths remain backend authoritative.
func (s *TaskService) RequestWorkflowStepApproval(ctx context.Context, sessionID, workflowName, stepName string) (string, error) {
	if err := validateWorkflowSessionID(sessionID); err != nil {
		return "", err
	}
	workflowName = strings.TrimSpace(workflowName)
	stepName = strings.TrimSpace(stepName)
	if workflowName == "" || stepName == "" {
		return "", fmt.Errorf("workflow and step names are required: %w", ErrInvalidInput)
	}
	s.mu.Lock()
	agent := s.agent
	s.mu.Unlock()
	if agent == nil {
		return "", fmt.Errorf("agent service not injected: %w", ErrInvalidInput)
	}
	if err := authorizeTaskWorkflowCaller(agent, ctx, sessionID); err != nil {
		return "", err
	}
	if err := s.preflightTaskApproval(); err != nil {
		return "", err
	}
	ownedSession, ownsSession, err := s.acquireTaskApprovalSession(agent, sessionID, ctx)
	if err != nil {
		return "", err
	}
	if ownsSession || ownedSession != sessionID {
		return "", errors.Join(fmt.Errorf("workflow session ownership mismatch: %w", ErrNotAllowed), agent.closeAgentSessionTrusted(ownedSession))
	}
	lifecycle := s.workflowLifecycle()
	session, err := lifecycle.GetByID(sessionID)
	if err != nil {
		return "", err
	}
	ownedWorkflow, err := workflowNameFromSession(session)
	if err != nil || ownedWorkflow != workflowName {
		return "", fmt.Errorf("workflow session %q does not own workflow %q: %v: %w", sessionID, workflowName, err, ErrNotAllowed)
	}

	catalog, err := agent.GetAgentToolCatalog(ctx)
	if err != nil {
		return "", err
	}
	var matched *AgentToolDefinition
	for index := range catalog.Tools {
		tool := &catalog.Tools[index]
		if tool.Source != string(agentcore.SourceWorkflow) || tool.Metadata["workflow"] != workflowName || tool.Metadata["step"] != stepName {
			continue
		}
		if matched != nil {
			return "", fmt.Errorf("workflow %q step %q has duplicate ToolDefs: %w", workflowName, stepName, ErrNotAllowed)
		}
		matched = tool
	}
	if matched == nil {
		return "", fmt.Errorf("workflow %q step %q has no executable catalog ToolDef: %w", workflowName, stepName, ErrNotAllowed)
	}
	arguments, err := workflowStepCapabilityArguments(agent, matched)
	if err != nil {
		return "", err
	}
	grant, err := agent.RequestAgentToolCapability(ctx, AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: matched.ID, Arguments: arguments,
	})
	if err != nil {
		return "", err
	}
	pending := taskExecutionApproval{
		executionID: sessionID, sessionID: sessionID, capability: grant, expiresAt: grant.ExpiresAt,
		owner:    agentCallerIdentityValue(ctx),
		workflow: workflowName, step: stepName,
		adapter: matched.Metadata["adapter"],
		command: matched.Metadata["command"], cwd: matched.Metadata["cwd"], arguments: arguments,
		risk: workflowToolRisk(matched),
	}
	return s.storeTaskApproval(pending)
}

func (s *TaskService) ExecuteApproved(ctx context.Context, executionID, command, cwd, approvalToken string) (ExecResult, error) {
	if ctx == nil {
		err := fmt.Errorf("task caller context is required: %w", ErrNotAllowed)
		return blockedTaskResult(command, cwd, err), err
	}
	if err := validateTaskExecutionID(executionID); err != nil {
		return ExecResult{}, err
	}
	s.mu.Lock()
	agent := s.agent
	s.mu.Unlock()
	if agent == nil {
		return ExecResult{}, fmt.Errorf("agent service not injected: %w", ErrInvalidInput)
	}
	s.mu.Lock()
	approval, ok := s.approvals[approvalToken]
	if ok {
		if approval.executionID != executionID {
			delete(s.approvals, approvalToken)
			s.mu.Unlock()
			err := fmt.Errorf("invalid, expired, or mismatched task capability: %w", ErrInvalidInput)
			err = errors.Join(err, s.releaseTaskApproval(approval))
			return blockedTaskResult(command, cwd, err), err
		}
		s.mu.Unlock()
		if err := authorizeTaskApprovalCaller(agent, ctx, approval, executionID); err != nil {
			return blockedTaskResult(command, cwd, err), err
		}
		if err := authorizeTaskApprovalOwner(approval, ctx); err != nil {
			return blockedTaskResult(command, cwd, err), err
		}
		s.mu.Lock()
		current, stillPending := s.approvals[approvalToken]
		if stillPending && current.capability.Token == approval.capability.Token {
			delete(s.approvals, approvalToken)
		} else {
			stillPending = false
		}
		ok = stillPending
	}
	s.mu.Unlock()
	if !ok {
		err := fmt.Errorf("invalid or already consumed task capability: %w", ErrInvalidInput)
		return blockedTaskResult(command, cwd, err), err
	}
	if approval.executionID != executionID || approval.workflow != "" || !approval.expiresAt.After(time.Now()) {
		err := fmt.Errorf("invalid, expired, or mismatched task capability: %w", ErrInvalidInput)
		err = errors.Join(err, s.releaseTaskApproval(approval))
		return blockedTaskResult(command, cwd, err), err
	}

	return s.executeTaskApproval(ctx, approval, executionID, "run", taskAgentRunArguments(command, cwd), command, cwd)
}

func (s *TaskService) ExecuteApprovedWorkflowStep(ctx context.Context, sessionID, workflowName, stepName, approvalToken string) (ExecResult, error) {
	if err := validateWorkflowSessionID(sessionID); err != nil {
		return ExecResult{}, err
	}
	workflowName = strings.TrimSpace(workflowName)
	stepName = strings.TrimSpace(stepName)
	s.mu.Lock()
	agent := s.agent
	s.mu.Unlock()
	if agent == nil {
		err := fmt.Errorf("agent service not injected: %w", ErrInvalidInput)
		return blockedTaskResult("", "", err), err
	}
	s.mu.Lock()
	approval, ok := s.approvals[approvalToken]
	if ok {
		s.mu.Unlock()
		if err := authorizeTaskWorkflowCaller(agent, ctx, sessionID); err != nil {
			return blockedTaskResult("", "", err), err
		}
		if err := authorizeTaskApprovalOwner(approval, ctx); err != nil {
			return blockedTaskResult("", "", err), err
		}
		s.mu.Lock()
		current, stillPending := s.approvals[approvalToken]
		if stillPending && current.capability.Token == approval.capability.Token {
			delete(s.approvals, approvalToken)
		} else {
			stillPending = false
		}
		ok = stillPending
	}
	s.mu.Unlock()
	if !ok {
		err := fmt.Errorf("invalid or already consumed workflow step capability: %w", ErrInvalidInput)
		return blockedTaskResult("", "", err), err
	}
	if approval.executionID != sessionID || approval.workflow != workflowName || approval.step != stepName || !approval.expiresAt.After(time.Now()) {
		err := errors.Join(
			fmt.Errorf("invalid, expired, or mismatched workflow step capability: %w", ErrInvalidInput),
			s.releaseTaskApproval(approval),
		)
		return blockedTaskResult(approval.command, approval.cwd, err), err
	}
	return s.executeTaskApproval(
		ctx, approval, sessionID, approval.capability.ToolID, approval.arguments, approval.command, approval.cwd,
	)
}

func (s *TaskService) executeTaskApproval(
	ctx context.Context,
	approval taskExecutionApproval,
	executionID, toolID string,
	arguments map[string]interface{},
	command, cwd string,
) (ExecResult, error) {
	s.mu.Lock()
	agent := s.agent
	s.mu.Unlock()
	if agent == nil {
		return ExecResult{}, fmt.Errorf("agent service not injected: %w", ErrInvalidInput)
	}
	ctx, capture := withAgentRunCapture(ctx, func(preparedCommand, preparedCwd string) (ExecResult, error) {
		return s.execute(executionID, preparedCommand, preparedCwd)
	})
	toolResult, coreErr := agent.ExecuteApprovedAgentTool(ctx, AgentToolCapabilityExecution{
		Token: approval.capability.Token, SessionID: approval.sessionID, CatalogRevision: approval.capability.CatalogRevision,
		ToolID: toolID, Arguments: arguments,
	})
	if s.workflowExecutionReturnHook != nil {
		s.workflowExecutionReturnHook()
	}
	// Runtime.Execute consumes the capability before it can invoke a handler,
	// including every rejected execution path. Do not call Agent core again
	// while another workflow transition may be waiting for its shared writer;
	// only clean up a session that this approval created.
	coreErr = errors.Join(coreErr, s.releaseTaskApprovalAfterExecution(approval))
	if !capture.invoked {
		if coreErr == nil && (approval.adapter == workflowAdapterAI || approval.adapter == workflowAdapterFileRead || approval.adapter == workflowAdapterFileWrite || approval.adapter == workflowAdapterGitStatus || approval.adapter == workflowAdapterMCP || approval.adapter == workflowAdapterSkillActivate) {
			return ExecResult{
				Command: approval.capability.ToolID, Cwd: approval.cwd,
				Stdout: toolResult.Observation, ExitCode: 0,
				RiskLevel: approval.risk,
			}, nil
		}
		if coreErr == nil {
			coreErr = fmt.Errorf("agent execution core did not invoke the task runner: %w", ErrInvalidInput)
		}
		err := errors.Join(coreErr, fmt.Errorf("invalid, expired, or mismatched task capability: %w", ErrInvalidInput))
		return blockedTaskResult(command, cwd, err), err
	}
	return capture.result, coreErr
}

func authorizeTaskWorkflowCaller(agent *AgentService, ctx context.Context, sessionID string) error {
	if ctx == nil {
		return fmt.Errorf("workflow caller context is required: %w", ErrNotAllowed)
	}
	return authorizeAgentSessionOwner(agent, ctx, sessionID)
}

func authorizeTaskApprovalCaller(agent *AgentService, ctx context.Context, approval taskExecutionApproval, executionID string) error {
	if strings.HasPrefix(executionID, "workflow:") {
		if err := authorizeTaskWorkflowCaller(agent, ctx, executionID); err != nil {
			return err
		}
	}
	if approval.sessionID != "" {
		if err := authorizeTaskWorkflowCaller(agent, ctx, approval.sessionID); err != nil {
			return err
		}
	}
	return nil
}

// trustedTaskContext is used only by package-internal orchestration and tests.
// Renderer calls receive a Wails WindowKey context and are never allowed to
// manufacture this trusted path through the generated bindings.
func trustedTaskContext() context.Context { return context.Background() }

func agentCallerIdentityValue(ctx context.Context) string {
	identity, _ := agentCallerIdentity(ctx)
	return identity
}

func authorizeTaskApprovalOwner(approval taskExecutionApproval, ctx context.Context) error {
	if approval.owner == "" {
		return nil
	}
	identity, ok := agentCallerIdentity(ctx)
	if !ok || identity != approval.owner {
		return fmt.Errorf("workflow approval belongs to another caller: %w", ErrNotAllowed)
	}
	return nil
}

func (s *TaskService) preflightTaskApproval() error {
	s.mu.Lock()
	expired := s.pruneTaskApprovalsLocked(time.Now())
	var preflightErr error
	if s.shuttingDown {
		preflightErr = fmt.Errorf("task service is shutting down: %w", ErrInvalidInput)
	} else if len(s.approvals) >= taskApprovalLimit {
		preflightErr = fmt.Errorf("too many pending task approvals: %w", ErrInvalidInput)
	}
	s.mu.Unlock()
	return errors.Join(preflightErr, s.releaseTaskApprovals(expired))
}

func (s *TaskService) storeTaskApproval(pending taskExecutionApproval) (string, error) {
	// The core capability is the only authority-bearing token. This map stores
	// renderer correlation metadata without creating a second token namespace.
	token := pending.capability.Token
	if token == "" {
		return "", errors.Join(fmt.Errorf("agent capability token is empty: %w", ErrNotAllowed), s.releaseTaskApproval(pending))
	}
	s.mu.Lock()
	expired := s.pruneTaskApprovalsLocked(time.Now())
	var storeErr error
	if s.shuttingDown {
		storeErr = fmt.Errorf("task service is shutting down: %w", ErrInvalidInput)
	} else if len(s.approvals) >= taskApprovalLimit {
		storeErr = fmt.Errorf("too many pending task approvals: %w", ErrInvalidInput)
	} else if _, exists := s.approvals[token]; exists {
		storeErr = fmt.Errorf("task approval token collision: %w", ErrInvalidInput)
	} else {
		s.approvals[token] = pending
	}
	s.mu.Unlock()
	cleanupErr := s.releaseTaskApprovals(expired)
	if storeErr == nil && cleanupErr == nil {
		return token, nil
	}
	if storeErr == nil {
		s.mu.Lock()
		if current, exists := s.approvals[token]; exists && current.capability.Token == pending.capability.Token {
			delete(s.approvals, token)
		}
		s.mu.Unlock()
	}
	return "", errors.Join(storeErr, cleanupErr, s.releaseTaskApproval(pending))
}

func workflowNameFromSession(session agentcore.Session) (string, error) {
	for index := len(session.Checkpoints) - 1; index >= 0; index-- {
		checkpoint := session.Checkpoints[index]
		if checkpoint.Label != "workflow-started" {
			continue
		}
		var state struct {
			Workflow string `json:"workflow"`
		}
		if err := json.Unmarshal(checkpoint.State, &state); err != nil {
			return "", fmt.Errorf("decode workflow owner checkpoint: %w", err)
		}
		if strings.TrimSpace(state.Workflow) == "" {
			return "", fmt.Errorf("workflow owner checkpoint is empty: %w", ErrNotAllowed)
		}
		return state.Workflow, nil
	}
	return "", fmt.Errorf("workflow owner checkpoint is missing: %w", ErrNotAllowed)
}

func (s *TaskService) acquireTaskApprovalSession(agent *AgentService, executionID string, ctx context.Context) (string, bool, error) {
	if !strings.HasPrefix(executionID, "workflow:") {
		var sessionID string
		var err error
		if _, hasCaller := agentOwnerForContext(ctx); hasCaller {
			sessionID, err = agent.CreateAgentSessionForCaller(ctx, "workflow")
		} else {
			sessionID, err = agent.createAgentSessionTrusted("workflow")
		}
		if err != nil {
			return "", false, err
		}
		return sessionID, true, nil
	}

	lifecycle := s.workflowLifecycle()
	if lifecycle == nil {
		return "", false, fmt.Errorf("workflow lifecycle is unavailable: %w", ErrNotAllowed)
	}
	session, err := lifecycle.GetByID(executionID)
	if err != nil {
		return "", false, fmt.Errorf("workflow session %q is not owned by TaskService: %v: %w", executionID, err, ErrNotAllowed)
	}
	if session.Kind != agentcore.SessionWorkflow || session.Status != agentcore.SessionRunning {
		return "", false, fmt.Errorf("workflow session %q is %s: %w", executionID, session.Status, ErrNotAllowed)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		return "", false, err
	}
	if !runtime.IsSessionActive(session.ID) {
		return "", false, fmt.Errorf("workflow session %q has no active runtime authority: %w", executionID, ErrNotAllowed)
	}
	return session.ID, false, nil
}

func taskAgentRunArguments(command, cwd string) map[string]interface{} {
	return map[string]interface{}{"command": command, "cwd": cwd}
}

func workflowStepCapabilityArguments(agent *AgentService, definition *AgentToolDefinition) (map[string]interface{}, error) {
	if definition == nil || definition.Source != string(agentcore.SourceWorkflow) ||
		strings.TrimSpace(definition.Metadata["workflow"]) == "" || strings.TrimSpace(definition.Metadata["step"]) == "" {
		return nil, fmt.Errorf("workflow ToolDef ownership metadata is invalid: %w", ErrNotAllowed)
	}
	if agent == nil {
		return nil, fmt.Errorf("AgentService is required for workflow source validation: %w", ErrNotAllowed)
	}
	if _, err := agent.validateCurrentAgentWorkflowTool(definition.Metadata); err != nil {
		return nil, err
	}
	switch definition.Metadata["adapter"] {
	case workflowAdapterCommand:
		if strings.TrimSpace(definition.Metadata["command"]) == "" || definition.Metadata["path"] != "" {
			return nil, fmt.Errorf("workflow command ToolDef metadata is invalid: %w", ErrNotAllowed)
		}
		return map[string]interface{}{}, nil
	case workflowAdapterFileRead:
		if definition.Metadata["command"] != "" || definition.Metadata["cwd"] != "" ||
			definition.Risk != string(agentcore.RiskReadOnly) || definition.Approval != string(agentcore.ApprovalBackendPolicy) ||
			definition.Mutation != string(agentcore.MutationNone) {
			return nil, fmt.Errorf("workflow file read cannot inherit command metadata: %w", ErrNotAllowed)
		}
		pathValue, err := normalizeWorkflowFileReadPath(definition.Metadata["path"])
		if err != nil || pathValue != definition.Metadata["path"] {
			return nil, fmt.Errorf("workflow file read ToolDef path is invalid: %w", ErrNotAllowed)
		}
		return map[string]interface{}{"path": pathValue}, nil
	case workflowAdapterFileWrite:
		if definition.Metadata["command"] != "" || definition.Metadata["cwd"] != "" ||
			definition.Risk != string(agentcore.RiskElevated) || definition.Approval != string(agentcore.ApprovalManual) ||
			definition.Mutation != string(agentcore.MutationWorkspaceTransaction) {
			return nil, fmt.Errorf("workflow file write ToolDef has an invalid mutation contract: %w", ErrNotAllowed)
		}
		pathValue, err := normalizeWorkflowFileReadPath(definition.Metadata["path"])
		if err != nil || pathValue != definition.Metadata["path"] {
			return nil, fmt.Errorf("workflow file write ToolDef path is invalid: %w", ErrNotAllowed)
		}
		if len(definition.Metadata["contentHash"]) != 64 || definition.Metadata["contentBytes"] == "" {
			return nil, fmt.Errorf("workflow file write ToolDef content identity is invalid: %w", ErrNotAllowed)
		}
		contentBytes, parseErr := strconv.Atoi(definition.Metadata["contentBytes"])
		if parseErr != nil || contentBytes < 0 || contentBytes > maxWorkflowFileWriteBytes {
			return nil, fmt.Errorf("workflow file write ToolDef content size is invalid: %w", ErrNotAllowed)
		}
		if _, decodeErr := hex.DecodeString(definition.Metadata["contentHash"]); decodeErr != nil {
			return nil, fmt.Errorf("workflow file write ToolDef content hash is invalid: %w", ErrNotAllowed)
		}
		return map[string]interface{}{}, nil
	case workflowAdapterGitStatus:
		if definition.Metadata["command"] != "" || definition.Metadata["cwd"] != "" || definition.Metadata["path"] != "" ||
			definition.Risk != string(agentcore.RiskReadOnly) || definition.Approval != string(agentcore.ApprovalBackendPolicy) ||
			definition.Mutation != string(agentcore.MutationNone) {
			return nil, fmt.Errorf("workflow Git status ToolDef metadata is invalid: %w", ErrNotAllowed)
		}
		return map[string]interface{}{}, nil
	case workflowAdapterMCP:
		if agent == nil || definition.Metadata["command"] != "" || definition.Metadata["cwd"] != "" || definition.Metadata["path"] != "" ||
			definition.Metadata["delegatedTool"] != "mcp."+definition.Metadata["server"]+"."+definition.Metadata["tool"] || definition.Metadata["inputHash"] == "" ||
			definition.Approval != string(agentcore.ApprovalManual) || definition.Mutation != string(agentcore.MutationExternal) ||
			(definition.Risk != string(agentcore.RiskElevated) && definition.Risk != string(agentcore.RiskDangerous)) {
			return nil, fmt.Errorf("workflow MCP ToolDef metadata is invalid: %w", ErrNotAllowed)
		}
		arguments, err := workflowMCPArgumentsForMetadata(agent, definition.Metadata)
		if err != nil {
			return nil, err
		}
		return arguments, nil
	case workflowAdapterSkillActivate:
		if agent == nil || definition.Metadata["command"] != "" || definition.Metadata["cwd"] != "" || definition.Metadata["path"] != "" ||
			definition.Metadata["workflow"] == "" || definition.Metadata["step"] == "" ||
			definition.Metadata["skillId"] == "" || definition.Metadata["scope"] == "" ||
			definition.Metadata["fingerprint"] == "" || definition.Risk != string(agentcore.RiskElevated) ||
			definition.Approval != string(agentcore.ApprovalManual) || definition.Mutation != string(agentcore.MutationExternal) {
			return nil, fmt.Errorf("workflow Skill activation ToolDef metadata is invalid: %w", ErrNotAllowed)
		}
		if skillID, err := normalizeWorkflowSkillID(definition.Metadata["skillId"]); err != nil || skillID != definition.Metadata["skillId"] ||
			!isAgentSkillScopeValid(SkillScope(definition.Metadata["scope"])) || !isValidSkillFingerprint(definition.Metadata["fingerprint"]) {
			return nil, fmt.Errorf("workflow Skill activation identity is invalid: %w", ErrNotAllowed)
		}
		deps := executionDependenciesFor(agent)
		deps.mu.RLock()
		skills := deps.skills
		deps.mu.RUnlock()
		if skills == nil {
			return nil, fmt.Errorf("skills service is not wired: %w", ErrNotAllowed)
		}
		skill, err := skills.GetSkill(definition.Metadata["skillId"])
		if err != nil {
			return nil, err
		}
		fingerprint, err := skillFingerprint(skill)
		if err != nil || fingerprint != definition.Metadata["fingerprint"] || string(skill.Scope) != definition.Metadata["scope"] {
			return nil, fmt.Errorf("workflow Skill changed after catalog publication: %w", ErrNotAllowed)
		}
		return map[string]interface{}{}, nil
	case workflowAdapterAI:
		if definition.Metadata["command"] != "" || definition.Metadata["cwd"] != "" || definition.Metadata["path"] != "" ||
			definition.Metadata["operation"] == "" || !isWorkflowAIOperation(definition.Metadata["operation"]) ||
			definition.Metadata["promptHash"] == "" || definition.Risk != string(agentcore.RiskElevated) ||
			definition.Approval != string(agentcore.ApprovalManual) || definition.Mutation != string(agentcore.MutationExternal) {
			return nil, fmt.Errorf("workflow AI ToolDef metadata is invalid: %w", ErrNotAllowed)
		}
		if _, err := agent.validateCurrentAgentWorkflowTool(definition.Metadata); err != nil {
			return nil, err
		}
		return map[string]interface{}{}, nil
	default:
		return nil, fmt.Errorf("workflow ToolDef adapter %q is unsupported: %w", definition.Metadata["adapter"], ErrNotAllowed)
	}
}

func workflowToolRisk(definition *AgentToolDefinition) RiskLevel {
	if definition == nil {
		return RiskDangerous
	}
	switch definition.Risk {
	case string(agentcore.RiskReadOnly):
		return RiskSafe
	case string(agentcore.RiskElevated):
		return RiskElevated
	default:
		return RiskDangerous
	}
}

func blockedTaskResult(command, cwd string, err error) ExecResult {
	return ExecResult{Command: command, Cwd: cwd, Blocked: true, BlockReason: err.Error()}
}

func (s *TaskService) releaseTaskApprovals(approvals []taskExecutionApproval) error {
	var releaseErrors []error
	for _, approval := range approvals {
		if err := s.releaseTaskApproval(approval); err != nil {
			releaseErrors = append(releaseErrors, err)
		}
	}
	return errors.Join(releaseErrors...)
}

func (s *TaskService) releaseTaskApproval(approval taskExecutionApproval) error {
	return s.releaseTaskApprovalMode(approval, true)
}

func (s *TaskService) releaseTaskApprovalAfterExecution(approval taskExecutionApproval) error {
	return s.releaseTaskApprovalMode(approval, false)
}

func (s *TaskService) releaseTaskApprovalMode(approval taskExecutionApproval, burnCapability bool) error {
	s.mu.Lock()
	agent := s.agent
	s.mu.Unlock()
	if agent == nil {
		return fmt.Errorf("agent service not injected while releasing task approval: %w", ErrNotAllowed)
	}

	var releaseErrors []error
	if burnCapability && approval.capability.Token != "" {
		// Runtime capabilities are one-shot and consumed before identity checks.
		// Deliberately mismatching the tool ID burns an abandoned capability
		// without invoking its handler or affecting a workflow-owned session.
		_, burnErr := agent.ExecuteApprovedAgentTool(context.Background(), AgentToolCapabilityExecution{
			Token: approval.capability.Token, SessionID: approval.sessionID,
			CatalogRevision: approval.capability.CatalogRevision,
			ToolID:          "", Arguments: map[string]interface{}{},
		})
		if burnErr != nil && !errors.Is(burnErr, agentcore.ErrInvalidCapability) {
			releaseErrors = append(releaseErrors, fmt.Errorf("burn task capability: %w", burnErr))
		}
	}
	if approval.ownsSession && approval.sessionID != "" {
		if err := agent.closeAgentSessionTrusted(approval.sessionID); err != nil {
			releaseErrors = append(releaseErrors, fmt.Errorf("close task agent session: %w", err))
		}
	}
	return errors.Join(releaseErrors...)
}

// execute runs an already-approved command as a tracked task. The opaque
// executionID must also be supplied to Stop.
func (s *TaskService) execute(executionID, command, cwd string) (ExecResult, error) {
	if err := validateTaskExecutionID(executionID); err != nil {
		return ExecResult{}, err
	}

	timeout := s.executionLimit
	if timeout <= 0 {
		timeout = taskExecutionTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	terminateProcess := s.terminateProcess
	if terminateProcess == nil {
		terminateProcess = terminateTaskProcessTree
	}
	execution := &taskExecution{
		cancel:           cancel,
		terminateProcess: terminateProcess,
		done:             make(chan struct{}),
	}
	stoppedBeforeRegistration, err := s.registerExecution(executionID, execution)
	if err != nil {
		cancel()
		return ExecResult{}, err
	}
	unregisterOnReturn := true
	defer func() {
		cancel()
		if unregisterOnReturn {
			execution.finish()
			s.unregisterExecution(executionID, execution)
		}
	}()
	if stoppedBeforeRegistration {
		_ = s.stopExecution(execution, taskStoppedByCaller)
	}

	agent := s.agent
	if agent == nil {
		return ExecResult{}, fmt.Errorf("agent service not injected: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(command) == "" {
		return ExecResult{
			Command:     command,
			Cwd:         cwd,
			RiskLevel:   RiskSafe,
			Blocked:     true,
			BlockReason: "command is required",
		}, fmt.Errorf("command is required: %w", ErrInvalidInput)
	}

	check := agent.CheckCommand(command)
	if check.Blocked {
		result := ExecResult{
			Command:     command,
			Cwd:         cwd,
			RiskLevel:   RiskDangerous,
			Blocked:     true,
			BlockReason: check.BlockReason,
		}
		agent.audit(result.Cwd, result)
		return result, fmt.Errorf("command blocked: %s", check.BlockReason)
	}

	resolvedCwd, err := agent.validateCwd(cwd)
	if err != nil {
		return ExecResult{}, err
	}
	argv, err := parseCommand(command)
	if err != nil {
		return ExecResult{}, fmt.Errorf("parse command: %w", err)
	}

	cmd := commandContext(context.Background(), argv[0], argv[1:]...)
	configureCoverageProcessTree(cmd)
	if resolvedCwd != "" {
		cmd.Dir = resolvedCwd
	}
	var stdout, stderr boundedAgentCommandOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		_ = s.stopExecution(execution, taskStoppedByTimeout)
	}
	startErr := execution.start(cmd)
	if startErr != nil {
		result := ExecResult{
			Command:    command,
			Cwd:        resolvedCwd,
			ExitCode:   -1,
			DurationMs: time.Since(start).Milliseconds(),
			RiskLevel:  check.RiskLevel,
		}
		if reason := execution.stopped(); reason != taskNotStopped {
			if reason == taskStoppedByTimeout {
				result.Stderr = fmt.Sprintf("[command timed out after %s before start]", timeout)
			} else {
				result.Stderr = "[command terminated before start]"
			}
			agent.audit(resolvedCwd, result)
			return result, nil
		}
		agent.audit(resolvedCwd, result)
		return result, fmt.Errorf("run command: %w", startErr)
	}

	waitDone := make(chan error, 1)
	go func() {
		runErr := cmd.Wait()
		// Mark completion before publishing the Wait result. Stop either wins
		// before this point and owns the termination result, or observes
		// finished and cannot relabel a natural exit as terminated.
		execution.finish()
		waitDone <- runErr
	}()

	var runErr error
	select {
	case runErr = <-waitDone:
	case <-ctx.Done():
		reason := taskStoppedByCaller
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = taskStoppedByTimeout
		}
		stopErr := s.stopExecution(execution, reason)
		terminationWait := s.terminationWait
		if terminationWait <= 0 {
			terminationWait = taskTerminationWait
		}
		timer := time.NewTimer(terminationWait)
		select {
		case runErr = <-waitDone:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			// Keep the registration until Wait really finishes so a later Stop
			// can retry termination and cannot accidentally target a reused ID.
			unregisterOnReturn = false
			go func() {
				<-waitDone
				s.unregisterExecution(executionID, execution)
			}()
			result := ExecResult{
				Command:    command,
				Cwd:        resolvedCwd,
				ExitCode:   -1,
				DurationMs: time.Since(start).Milliseconds(),
				RiskLevel:  check.RiskLevel,
			}
			if reason == taskStoppedByTimeout {
				result.Stderr = fmt.Sprintf("[command timed out after %s; process did not exit within %s]", timeout, terminationWait)
			} else {
				result.Stderr = fmt.Sprintf("[command termination requested; process did not exit within %s]", terminationWait)
			}
			agent.audit(resolvedCwd, result)
			if stopErr != nil {
				return result, fmt.Errorf("terminate task %q: %w", executionID, stopErr)
			}
			return result, fmt.Errorf("task %q did not exit after termination request", executionID)
		}
	}

	result := ExecResult{
		Command:    command,
		Cwd:        resolvedCwd,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   0,
		DurationMs: time.Since(start).Milliseconds(),
		RiskLevel:  check.RiskLevel,
	}
	stopReason := execution.stopped()
	if stopReason != taskNotStopped {
		result.ExitCode = -1
		if stopReason == taskStoppedByTimeout {
			result.Stderr = appendTaskStatus(result.Stderr, fmt.Sprintf("[command timed out after %s]", timeout))
		} else {
			result.Stderr = appendTaskStatus(result.Stderr, "[command terminated]")
		}
		agent.audit(resolvedCwd, result)
		return result, nil
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			agent.audit(resolvedCwd, result)
			return result, nil
		}
		agent.audit(resolvedCwd, result)
		return result, fmt.Errorf("run command: %w", runErr)
	}

	agent.audit(resolvedCwd, result)
	return result, nil
}

// Stop terminates a tracked task and its child process tree. Unknown IDs are
// idempotent: a short-lived, bounded tombstone is recorded so a Stop request
// that overtakes its Execute request still prevents that task from starting.
func (s *TaskService) Stop(executionID string) error {
	if err := validateTaskExecutionID(executionID); err != nil {
		return err
	}
	now := time.Now()
	s.mu.Lock()
	s.ensureExecutionStateLocked()
	s.prunePendingStopsLocked(now)
	execution := s.active[executionID]
	if execution == nil {
		if s.shuttingDown {
			s.mu.Unlock()
			return nil
		}
		if len(s.pendingStops) >= taskPendingStopLimit {
			s.evictOldestPendingStopLocked()
		}
		s.pendingStops[executionID] = now.Add(taskPendingStopTTL)
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	return s.stopExecution(execution, taskStoppedByCaller)
}

// Shutdown prevents new tracked executions, clears pending Stop tombstones,
// and terminates every active process tree. It is safe to call repeatedly.
// The application should invoke it before closing AgentService so tracked
// children cannot outlive the backend process during an orderly shutdown.
func (s *TaskService) Shutdown() error {
	s.mu.Lock()
	s.ensureExecutionStateLocked()
	s.shuttingDown = true
	clear(s.pendingStops)
	approvals := make([]taskExecutionApproval, 0, len(s.approvals))
	for token, approval := range s.approvals {
		approvals = append(approvals, approval)
		delete(s.approvals, token)
	}
	executions := make(map[string]*taskExecution, len(s.active))
	for executionID, execution := range s.active {
		executions[executionID] = execution
	}
	s.mu.Unlock()

	var shutdownErrors []error
	if err := s.releaseTaskApprovals(approvals); err != nil {
		shutdownErrors = append(shutdownErrors, err)
	}
	if len(executions) == 0 {
		return errors.Join(shutdownErrors...)
	}
	waitLimit := s.terminationWait
	if waitLimit <= 0 {
		waitLimit = taskTerminationWait
	}
	errorsByExecution := make(chan error, len(executions))
	var wg sync.WaitGroup
	for executionID, execution := range executions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stopErr := s.stopExecution(execution, taskStoppedByCaller)
			timer := time.NewTimer(waitLimit)
			defer timer.Stop()
			select {
			case <-execution.done:
				return
			case <-timer.C:
				if stopErr != nil {
					errorsByExecution <- fmt.Errorf("stop task %q during shutdown: %w", executionID, stopErr)
					return
				}
				errorsByExecution <- fmt.Errorf("task %q did not exit within %s during shutdown", executionID, waitLimit)
			}
		}()
	}
	wg.Wait()
	close(errorsByExecution)
	for err := range errorsByExecution {
		shutdownErrors = append(shutdownErrors, err)
	}
	return errors.Join(shutdownErrors...)
}

func (s *TaskService) stopExecution(execution *taskExecution, reason taskStopReason) error {
	callLimit := s.terminationCallLimit
	if callLimit <= 0 {
		callLimit = taskTerminationCallLimit
	}
	return execution.stop(reason, callLimit)
}

func (s *TaskService) registerExecution(executionID string, execution *taskExecution) (bool, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureExecutionStateLocked()
	s.prunePendingStopsLocked(now)
	if s.shuttingDown {
		return false, fmt.Errorf("task service is shutting down: %w", ErrInvalidInput)
	}
	if _, exists := s.active[executionID]; exists {
		return false, fmt.Errorf("task execution %q is already running: %w", executionID, ErrInvalidInput)
	}
	_, stopped := s.pendingStops[executionID]
	delete(s.pendingStops, executionID)
	s.active[executionID] = execution
	return stopped, nil
}

func (s *TaskService) unregisterExecution(executionID string, execution *taskExecution) {
	s.mu.Lock()
	if s.active[executionID] == execution {
		delete(s.active, executionID)
	}
	s.mu.Unlock()
}

func (s *TaskService) ensureExecutionStateLocked() {
	if s.active == nil {
		s.active = make(map[string]*taskExecution)
	}
	if s.pendingStops == nil {
		s.pendingStops = make(map[string]time.Time)
	}
	if s.approvals == nil {
		s.approvals = make(map[string]taskExecutionApproval)
	}
}

func (s *TaskService) pruneTaskApprovalsLocked(now time.Time) []taskExecutionApproval {
	expired := make([]taskExecutionApproval, 0)
	for token, approval := range s.approvals {
		if !approval.expiresAt.After(now) {
			delete(s.approvals, token)
			expired = append(expired, approval)
		}
	}
	return expired
}

func (s *TaskService) prunePendingStopsLocked(now time.Time) {
	for executionID, expiresAt := range s.pendingStops {
		if !expiresAt.After(now) {
			delete(s.pendingStops, executionID)
		}
	}
}

func (s *TaskService) evictOldestPendingStopLocked() {
	var oldestID string
	var oldestExpiry time.Time
	for executionID, expiresAt := range s.pendingStops {
		if oldestID == "" || expiresAt.Before(oldestExpiry) {
			oldestID = executionID
			oldestExpiry = expiresAt
		}
	}
	if oldestID != "" {
		delete(s.pendingStops, oldestID)
	}
}

func validateTaskExecutionID(executionID string) error {
	if strings.TrimSpace(executionID) == "" {
		return fmt.Errorf("task execution id is required: %w", ErrInvalidInput)
	}
	if len(executionID) > taskExecutionIDMaxLength {
		return fmt.Errorf("task execution id is too long: %w", ErrInvalidInput)
	}
	return nil
}

func appendTaskStatus(stderr, status string) string {
	if stderr == "" {
		return appendAgentCommandNotice("", status)
	}
	if strings.HasSuffix(stderr, "\n") {
		return appendAgentCommandNotice(stderr, status)
	}
	return appendAgentCommandNotice(stderr, "\n"+status)
}

func normalizeTaskTerminationError(err error) error {
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	// Windows os.Process.Kill reports syscall.EINVAL when cmd.Wait has
	// already released the process handle. In this narrow platform-specific
	// case the requested end state has already been reached.
	if runtime.GOOS == "windows" && errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}

func terminateTaskProcessTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	if runtime.GOOS != "windows" {
		return terminateCoverageProcessTree(process)
	}

	// Unlike the shared coverage helper, task termination is reachable from a
	// long-lived bridge call and therefore must not wait indefinitely for
	// taskkill itself. Fixed argv are used; no shell parses the PID.
	ctx, cancel := context.WithTimeout(context.Background(), taskTerminationCommandLimit)
	defer cancel()
	kill := commandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(process.Pid), "/T", "/F")
	if err := kill.Run(); err == nil {
		return nil
	}
	return normalizeTaskTerminationError(process.Kill())
}

// LoadTasks reads the project's task definitions. Returns an empty list
// (not an error) when no tasks file exists, so the frontend can always
// render the Tasks panel.
func (s *TaskService) LoadTasks(projectRoot string) ([]TaskDef, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("projectRoot is required")
	}
	// Prefer the standard VS Code schema, then retain the Koyori IDE legacy formats.
	candidates := []string{
		filepath.Join(projectRoot, ".vscode", "tasks.json"),
		filepath.Join(projectRoot, ".koyori-ide", "tasks.json"),
		filepath.Join(projectRoot, "task.json"),
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		tf, err := decodeTaskFile(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		// Validate: each task must have a label and a command.
		out := make([]TaskDef, 0, len(tf.Tasks))
		for _, raw := range tf.Tasks {
			t, err := raw.toTaskDef()
			if err != nil {
				return nil, fmt.Errorf("parse %s task %q: %w", p, raw.Label, err)
			}
			if t.Label == "" || t.Command == "" {
				continue
			}
			out = append(out, t)
		}
		return out, nil
	}
	return []TaskDef{}, nil
}

func (r rawTaskDef) toTaskDef() (TaskDef, error) {
	dependsOn, err := decodeStringList(r.DependsOn)
	if err != nil {
		return TaskDef{}, fmt.Errorf("dependsOn: %w", err)
	}
	problemMatcher, err := decodeStringList(r.ProblemMatcher)
	if err != nil {
		return TaskDef{}, fmt.Errorf("problemMatcher: %w", err)
	}
	group, err := decodeTaskGroup(r.Group)
	if err != nil {
		return TaskDef{}, fmt.Errorf("group: %w", err)
	}
	cwd := r.Cwd
	if r.Options.Cwd != "" {
		cwd = r.Options.Cwd
	}
	env := make(map[string]string, len(r.Env)+len(r.Options.Env))
	for key, value := range r.Env {
		env[key] = value
	}
	for key, value := range r.Options.Env {
		env[key] = value
	}
	if len(env) == 0 {
		env = nil
	}
	return TaskDef{
		Label:          r.Label,
		Type:           r.Type,
		Command:        r.Command,
		Args:           r.Args,
		Cwd:            cwd,
		Env:            env,
		DependsOn:      dependsOn,
		Group:          group,
		ProblemMatcher: problemMatcher,
		Shell:          r.Shell || r.Type == "shell",
	}, nil
}

func decodeStringList(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func decodeTaskGroup(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single, nil
	}
	var group struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &group); err != nil {
		return "", err
	}
	return group.Kind, nil
}

func decodeTaskFile(data []byte) (TaskFile, error) {
	normalized, err := normalizeJSONC(data)
	if err != nil {
		return TaskFile{}, err
	}
	var taskFile TaskFile
	if err := json.Unmarshal(normalized, &taskFile); err != nil {
		return TaskFile{}, err
	}
	return taskFile, nil
}

func normalizeJSONC(data []byte) ([]byte, error) {
	withoutComments, err := stripJSONComments(data)
	if err != nil {
		return nil, err
	}
	return stripJSONTrailingCommas(withoutComments), nil
}

func stripJSONComments(data []byte) ([]byte, error) {
	result := make([]byte, 0, len(data))
	inString := false
	escaped := false
	lineComment := false
	blockComment := false

	for i := 0; i < len(data); i++ {
		char := data[i]
		var next byte
		if i+1 < len(data) {
			next = data[i+1]
		}
		switch {
		case lineComment:
			if char == '\n' || char == '\r' {
				lineComment = false
				result = append(result, char)
			} else {
				result = append(result, ' ')
			}
		case blockComment:
			if char == '*' && next == '/' {
				result = append(result, ' ', ' ')
				i++
				blockComment = false
			} else if char == '\n' || char == '\r' {
				result = append(result, char)
			} else {
				result = append(result, ' ')
			}
		case inString:
			result = append(result, char)
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
		case char == '"':
			inString = true
			result = append(result, char)
		case char == '/' && next == '/':
			result = append(result, ' ', ' ')
			i++
			lineComment = true
		case char == '/' && next == '*':
			result = append(result, ' ', ' ')
			i++
			blockComment = true
		default:
			result = append(result, char)
		}
	}
	if blockComment {
		return nil, fmt.Errorf("unterminated JSONC block comment")
	}
	return result, nil
}

func stripJSONTrailingCommas(data []byte) []byte {
	result := make([]byte, 0, len(data))
	inString := false
	escaped := false
	for i, char := range data {
		if inString {
			result = append(result, char)
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
			result = append(result, char)
			continue
		}
		if char == ',' {
			next := i + 1
			for next < len(data) && (data[next] == ' ' || data[next] == '\t' || data[next] == '\r' || data[next] == '\n') {
				next++
			}
			if next < len(data) && (data[next] == '}' || data[next] == ']') {
				continue
			}
		}
		result = append(result, char)
	}
	return result
}

// ComposeCommandLine builds a single shell-ready command line from a TaskDef.
// The frontend uses this to write the command into a terminal session.
func (t TaskDef) ComposeCommandLine() string {
	out := t.Command
	for _, a := range t.Args {
		out += " " + shellQuote(a)
	}
	return out
}

// shellQuote wraps a single argument in single quotes, escaping any embedded
// single quotes. This is a best-effort cross-platform quoting; for the
// terminal-write use case the user's shell will re-parse the line.
func shellQuote(s string) string {
	// Replace every ' with '\'' (close quote, escaped quote, reopen quote).
	var quoted strings.Builder
	quoted.Grow(len(s) + 2)
	quoted.WriteByte('\'')
	for _, r := range s {
		if r == '\'' {
			quoted.WriteString(`'\''`)
		} else {
			quoted.WriteRune(r)
		}
	}
	quoted.WriteByte('\'')
	return quoted.String()
}
