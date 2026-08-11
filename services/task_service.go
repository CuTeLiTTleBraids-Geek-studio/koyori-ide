package services

import (
	"bytes"
	"context"
	crypto_rand "crypto/rand"
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
	taskExecutionTimeout     = 30 * time.Second
	taskTerminationCallLimit = 2 * time.Second
	taskTerminationWait      = 2 * time.Second
	taskPendingStopTTL       = time.Minute
	taskPendingStopLimit     = 256
	taskApprovalLimit        = 256
	taskExecutionIDMaxLength = 256
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
}

type taskExecutionApproval struct {
	executionID string
	agentToken  string
	expiresAt   time.Time
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

// Execute is kept as a deny-only Wails endpoint. Renderer calls must request a
// backend-issued command capability and use ExecuteApproved.
func (s *TaskService) Execute(executionID, command, cwd string) (ExecResult, error) {
	return ExecResult{Command: command, Cwd: cwd, Blocked: true, BlockReason: "backend approval token required"}, fmt.Errorf("backend approval token required: %w", ErrInvalidInput)
}

func (s *TaskService) RequestExecutionApproval(executionID, command, cwd string) (string, error) {
	if err := validateTaskExecutionID(executionID); err != nil {
		return "", err
	}
	if s.agent == nil {
		return "", fmt.Errorf("agent service not injected: %w", ErrInvalidInput)
	}
	agentToken, err := s.agent.RequestCommandApproval(command, cwd)
	if err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := crypto_rand.Read(raw); err != nil {
		s.agent.discardCommandApproval(agentToken)
		return "", fmt.Errorf("create task approval token: %w", err)
	}
	token := hex.EncodeToString(raw)
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		s.agent.discardCommandApproval(agentToken)
		return "", fmt.Errorf("task service is shutting down: %w", ErrInvalidInput)
	}
	now := time.Now()
	for token, approval := range s.approvals {
		if now.After(approval.expiresAt) {
			delete(s.approvals, token)
			s.agent.discardCommandApproval(approval.agentToken)
		}
	}
	if len(s.approvals) >= taskApprovalLimit {
		s.mu.Unlock()
		s.agent.discardCommandApproval(agentToken)
		return "", fmt.Errorf("too many pending task approvals: %w", ErrInvalidInput)
	}
	s.approvals[token] = taskExecutionApproval{
		executionID: executionID,
		agentToken:  agentToken,
		expiresAt:   now.Add(2 * time.Minute),
	}
	s.mu.Unlock()
	return token, nil
}

func (s *TaskService) ExecuteApproved(executionID, command, cwd, approvalToken string) (ExecResult, error) {
	if s.agent == nil {
		return ExecResult{}, fmt.Errorf("agent service not injected: %w", ErrInvalidInput)
	}
	s.mu.Lock()
	approval, ok := s.approvals[approvalToken]
	if ok {
		delete(s.approvals, approvalToken)
	}
	s.mu.Unlock()
	if !ok || approval.executionID != executionID || time.Now().After(approval.expiresAt) {
		if ok {
			s.agent.discardCommandApproval(approval.agentToken)
		}
		err := fmt.Errorf("invalid, expired, or mismatched task approval: %w", ErrInvalidInput)
		return ExecResult{Command: command, Cwd: cwd, Blocked: true, BlockReason: err.Error()}, err
	}
	if err := s.agent.consumeCommandApproval(approval.agentToken, command, cwd); err != nil {
		return ExecResult{Command: command, Cwd: cwd, Blocked: true, BlockReason: err.Error()}, err
	}
	return s.execute(executionID, command, cwd)
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
	var stdout, stderr bytes.Buffer
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
	for token, approval := range s.approvals {
		delete(s.approvals, token)
		s.agent.discardCommandApproval(approval.agentToken)
	}
	executions := make(map[string]*taskExecution, len(s.active))
	for executionID, execution := range s.active {
		executions[executionID] = execution
	}
	s.mu.Unlock()

	if len(executions) == 0 {
		return nil
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
	var shutdownErrors []error
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
		return status
	}
	if strings.HasSuffix(stderr, "\n") {
		return stderr + status
	}
	return stderr + "\n" + status
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
	ctx, cancel := context.WithTimeout(context.Background(), taskTerminationCallLimit)
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
