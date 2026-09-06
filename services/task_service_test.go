package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func writeTasksFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func newTaskTestAgent(t *testing.T) *AgentService {
	t.Helper()
	root := t.TempDir()
	workspace := NewWorkspaceContext()
	if err := workspace.Set(root); err != nil {
		t.Fatalf("workspace.Set: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(root); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	return agent
}

func TestTaskServiceWorkflowLifecycleAggregatesOneRun(t *testing.T) {
	agent := newTaskTestAgent(t)
	task := NewTaskService(agent)
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), "build")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	if !strings.HasPrefix(sessionID, "workflow:") {
		t.Fatalf("session ID = %q, want workflow namespace", sessionID)
	}
	initialUsage := permission.usageRecordsSnapshot()
	if len(initialUsage) != 1 || initialUsage[0].UnitKind != string(agentcore.UsageUnitWorkflow) || !initialUsage[0].Pending {
		t.Fatalf("workflow began without pending usage receipt: %+v", initialUsage)
	}
	now := time.Now()
	if err := lifecycle.Record(agentcore.UsageRecord{
		SessionID: sessionID, UnitKind: agentcore.UsageUnitTool, Operation: "workflow.step.one",
		CostBasis: agentcore.CostNotApplicable, StartedAt: now, CompletedAt: now.Add(time.Second), Success: true,
	}); err != nil {
		t.Fatalf("Record first step: %v", err)
	}
	if err := task.FailWorkflowExecution(trustedTaskContext(), sessionID, "retryable step failed"); err != nil {
		t.Fatalf("FailWorkflowExecution: %v", err)
	}
	failed, err := lifecycle.GetByID(sessionID)
	if err != nil || failed.Status != agentcore.SessionFailed {
		t.Fatalf("failed workflow = %+v, err=%v", failed, err)
	}
	if err := task.ResumeWorkflowExecution(trustedTaskContext(), sessionID); err != nil {
		t.Fatalf("ResumeWorkflowExecution: %v", err)
	}
	workflowPending := 0
	for _, record := range permission.usageRecordsSnapshot() {
		if record.UnitKind == string(agentcore.UsageUnitWorkflow) && record.Pending {
			workflowPending++
		}
	}
	if workflowPending != 1 {
		t.Fatalf("resumed workflow pending receipts = %d, want 1", workflowPending)
	}
	if err := task.CompleteWorkflowExecution(trustedTaskContext(), sessionID); err != nil {
		t.Fatalf("CompleteWorkflowExecution: %v", err)
	}
	completed, err := lifecycle.GetByID(sessionID)
	if err != nil || completed.Status != agentcore.SessionCompleted || len(completed.Checkpoints) < 2 {
		t.Fatalf("completed workflow = %+v, err=%v", completed, err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("completed workflow retained runtime capability authority")
	}
	records := permission.usageRecordsSnapshot()
	workflowRecords := 0
	for _, record := range records {
		if record.UnitKind == string(agentcore.UsageUnitWorkflow) {
			workflowRecords++
		}
	}
	if workflowRecords != 2 {
		t.Fatalf("workflow usage records = %+v, want failed attempt and completed retry", records)
	}
}

func TestLoadTasks_NoFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	svc := NewTaskService()
	tasks, err := svc.LoadTasks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestLoadTasks_EmptyRootReturnsError(t *testing.T) {
	svc := NewTaskService()
	_, err := svc.LoadTasks("")
	if err == nil {
		t.Fatal("expected error for empty root, got nil")
	}
}

func TestLoadTasks_DotNknkTasksJson(t *testing.T) {
	dir := t.TempDir()
	writeTasksFile(t, dir, ".koyori-ide/tasks.json", `{
		"version": "1",
		"tasks": [
			{"label": "build", "command": "go", "args": ["build", "./..."]},
			{"label": "test", "command": "go", "args": ["test", "./..."]}
		]
	}`)
	svc := NewTaskService()
	tasks, err := svc.LoadTasks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Label != "build" || tasks[1].Label != "test" {
		t.Errorf("unexpected labels: %q, %q", tasks[0].Label, tasks[1].Label)
	}
}

func TestLoadTasks_LegacyTaskJsonAtRoot(t *testing.T) {
	dir := t.TempDir()
	writeTasksFile(t, dir, "task.json", `{
		"version": "1",
		"tasks": [{"label": "run", "command": "npm start"}]
	}`)
	svc := NewTaskService()
	tasks, err := svc.LoadTasks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Label != "run" {
		t.Errorf("expected 1 task 'run', got %+v", tasks)
	}
}

func TestLoadTasks_DotNknkTakesPriorityOverRoot(t *testing.T) {
	dir := t.TempDir()
	writeTasksFile(t, dir, ".koyori-ide/tasks.json", `{"tasks":[{"label":"from-dotkoyoriIde","command":"a"}]}`)
	writeTasksFile(t, dir, "task.json", `{"tasks":[{"label":"from-root","command":"b"}]}`)
	svc := NewTaskService()
	tasks, err := svc.LoadTasks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Label != "from-dotkoyoriIde" {
		t.Errorf("expected from-dotkoyoriIde, got %+v", tasks)
	}
}

func TestLoadTasks_VSCodeTasksJSONTakesPriorityAndSupportsJSONC(t *testing.T) {
	dir := t.TempDir()
	writeTasksFile(t, dir, ".vscode/tasks.json", `{
		// VS Code 2.0 schema with comments and trailing commas.
		"version": "2.0.0",
		"tasks": [{
			"label": "web",
			"type": "shell",
			"command": "https://example.com/run//safe",
			"args": ["--watch",],
			"options": {
				"cwd": "frontend",
				"env": {"NODE_ENV": "test",},
			},
			"dependsOn": "prepare",
			"group": {"kind": "build", "isDefault": true},
			"problemMatcher": ["$tsc",],
		},],
	}`)
	writeTasksFile(t, dir, ".koyori-ide/tasks.json", `{"tasks":[{"label":"legacy","command":"old"}]}`)

	tasks, err := NewTaskService().LoadTasks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected one VS Code task, got %+v", tasks)
	}
	got := tasks[0]
	if got.Label != "web" || got.Command != "https://example.com/run//safe" || got.Type != "shell" {
		t.Fatalf("unexpected core fields: %+v", got)
	}
	if got.Cwd != "frontend" || got.Env["NODE_ENV"] != "test" {
		t.Fatalf("options were not mapped: %+v", got)
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != "prepare" || got.Group != "build" {
		t.Fatalf("task metadata was not mapped: %+v", got)
	}
	if len(got.ProblemMatcher) != 1 || got.ProblemMatcher[0] != "$tsc" {
		t.Fatalf("problem matcher was not mapped: %+v", got)
	}
}

func TestLoadTasks_InvalidVSCodeJSONDoesNotFallBackToLegacy(t *testing.T) {
	dir := t.TempDir()
	writeTasksFile(t, dir, ".vscode/tasks.json", `{ invalid`)
	writeTasksFile(t, dir, ".koyori-ide/tasks.json", `{"tasks":[{"label":"legacy","command":"old"}]}`)

	_, err := NewTaskService().LoadTasks(dir)
	if err == nil || !strings.Contains(err.Error(), ".vscode") {
		t.Fatalf("expected .vscode parse error, got %v", err)
	}
}

func TestLoadTasks_InvalidJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeTasksFile(t, dir, ".koyori-ide/tasks.json", `{not valid json`)
	svc := NewTaskService()
	_, err := svc.LoadTasks(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestLoadTasks_SkipsTasksWithoutLabelOrCommand(t *testing.T) {
	dir := t.TempDir()
	writeTasksFile(t, dir, ".koyori-ide/tasks.json", `{
		"tasks": [
			{"label": "ok", "command": "echo hi"},
			{"label": "", "command": "no-label"},
			{"label": "no-cmd", "command": ""},
			{"command": "no-label-no-cmd"}
		]
	}`)
	svc := NewTaskService()
	tasks, err := svc.LoadTasks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 valid task, got %d: %+v", len(tasks), tasks)
	}
	if tasks[0].Label != "ok" {
		t.Errorf("expected 'ok', got %q", tasks[0].Label)
	}
}

func TestComposeCommandLine_NoArgs(t *testing.T) {
	td := TaskDef{Command: "ls"}
	if got := td.ComposeCommandLine(); got != "ls" {
		t.Errorf("expected 'ls', got %q", got)
	}
}

func TestComposeCommandLine_WithArgs(t *testing.T) {
	td := TaskDef{Command: "go", Args: []string{"build", "./..."}}
	got := td.ComposeCommandLine()
	want := "go 'build' './...'"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestComposeCommandLine_EscapesSingleQuotes(t *testing.T) {
	td := TaskDef{Command: "echo", Args: []string{"it's"}}
	got := td.ComposeCommandLine()
	// Should wrap arg in single quotes and escape the embedded quote.
	want := "echo 'it'\\''s'"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestShellQuote_LongArgumentUsesBoundedAllocations(t *testing.T) {
	input := strings.Repeat("ab'cd", 1_000)
	want := "'" + strings.Repeat(`ab'\''cd`, 1_000) + "'"
	var got string
	allocs := testing.AllocsPerRun(10, func() {
		got = shellQuote(input)
	})
	if got != want {
		t.Fatal("shellQuote changed the escaped output")
	}
	if allocs > 20 {
		t.Fatalf("shellQuote allocated %.0f objects per call, want at most 20", allocs)
	}
}

func TestTaskServiceExecute_NaturalExitCleansRegistration(t *testing.T) {
	svc := NewTaskService(newTaskTestAgent(t))
	result, err := svc.execute("natural-exit", "go version", "")
	if err != nil {
		t.Fatalf("Execute returned an error: %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "go version") {
		t.Fatalf("unexpected execution result: %+v", result)
	}
	if got := taskServiceActiveCount(svc); got != 0 {
		t.Fatalf("active executions leaked after natural exit: %d", got)
	}
}

func TestTaskServiceStop_TerminatesRunningProcessAndCleansRegistration(t *testing.T) {
	t.Setenv("GO_WANT_TASK_SERVICE_HELPER", "1")
	svc := NewTaskService(newTaskTestAgent(t))
	executionID := "stop-running"
	resultCh := executeTaskForTest(svc, executionID, taskServiceHelperCommand())
	waitForTaskProcess(t, svc, executionID)

	if err := svc.Stop(executionID); err != nil {
		t.Fatalf("Stop returned an error: %v", err)
	}
	result := waitForTaskResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("Execute returned an error after Stop: %v", result.err)
	}
	if result.result.ExitCode != -1 || !strings.Contains(result.result.Stderr, "[command terminated]") {
		t.Fatalf("unexpected terminated result: %+v", result.result)
	}
	if got := taskServiceActiveCount(svc); got != 0 {
		t.Fatalf("active executions leaked after Stop: %d", got)
	}
	// A late/repeated Stop is deliberately idempotent.
	if err := svc.Stop(executionID); err != nil {
		t.Fatalf("repeated Stop returned an error: %v", err)
	}
}

func TestTaskServiceStop_ConcurrentCallsAreIdempotent(t *testing.T) {
	t.Setenv("GO_WANT_TASK_SERVICE_HELPER", "1")
	svc := NewTaskService(newTaskTestAgent(t))
	terminatorStarted := make(chan struct{})
	releaseTerminator := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseTerminator) }) })
	var terminationAttempts atomic.Int32
	svc.terminateProcess = func(process *os.Process) error {
		if terminationAttempts.Add(1) == 1 {
			close(terminatorStarted)
		}
		<-releaseTerminator
		return normalizeTaskTerminationError(process.Kill())
	}
	executionID := "concurrent-stop"
	resultCh := executeTaskForTest(svc, executionID, taskServiceHelperCommand())
	waitForTaskProcess(t, svc, executionID)

	const callers = 12
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.Stop(executionID)
		}()
	}
	close(start)
	select {
	case <-terminatorStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Stop calls did not reach the controlled terminator")
	}
	releaseOnce.Do(func() { close(releaseTerminator) })
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Stop returned an error: %v", err)
		}
	}
	if got := terminationAttempts.Load(); got != 1 {
		t.Fatalf("concurrent Stop spawned %d termination attempts, want 1", got)
	}
	result := waitForTaskResult(t, resultCh)
	if result.err != nil || result.result.ExitCode != -1 {
		t.Fatalf("unexpected result after concurrent Stop: %+v, err=%v", result.result, result.err)
	}
}

func TestTaskServiceStop_AllowsTreeKillFallbackBeforeOuterDeadline(t *testing.T) {
	t.Setenv("GO_WANT_TASK_SERVICE_HELPER", "1")
	svc := NewTaskService(newTaskTestAgent(t))
	const treeKillBudget = 2 * time.Second
	svc.terminateProcess = func(process *os.Process) error {
		// Model taskkill reaching its own deadline before the direct Process.Kill
		// fallback completes. The outer Stop budget must include this handoff.
		time.Sleep(treeKillBudget + 100*time.Millisecond)
		return normalizeTaskTerminationError(process.Kill())
	}
	executionID := "near-deadline-stop"
	resultCh := executeTaskForTest(svc, executionID, taskServiceHelperCommand())
	waitForTaskProcess(t, svc, executionID)

	if err := svc.Stop(executionID); err != nil {
		_ = waitForTaskResult(t, resultCh)
		t.Fatalf("Stop returned before the tree-kill fallback completed: %v", err)
	}
	result := waitForTaskResult(t, resultCh)
	if result.err != nil || result.result.ExitCode != -1 {
		t.Fatalf("unexpected near-deadline termination result: %+v, err=%v", result.result, result.err)
	}
}

func TestTaskServiceStop_TerminatesChildProcessTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("GO_WANT_TASK_SERVICE_TREE_HELPER", "parent")
	t.Setenv("TASK_SERVICE_CHILD_PID_FILE", pidFile)
	svc := NewTaskService(newTaskTestAgent(t))
	executionID := "process-tree"
	commandLine := shellQuote(os.Args[0]) + " '-test.run=TestTaskServiceProcessTreeHelper'"
	resultCh := executeTaskForTest(svc, executionID, commandLine)
	childPID := 0
	resultRead := false
	t.Cleanup(func() {
		// Setup can fail before the child publishes its PID. Always tear down the
		// tracked parent first, then make a best-effort pass over the child PID so
		// a failed assertion cannot leave a process holding t.TempDir open.
		if childPID == 0 {
			childPID = readTaskChildPID(pidFile)
		}
		_ = svc.Stop(executionID)
		if !resultRead {
			select {
			case <-resultCh:
			case <-time.After(5 * time.Second):
			}
		}
		if childPID > 0 {
			if process, err := os.FindProcess(childPID); err == nil {
				_ = process.Kill()
			}
		}
	})
	waitForTaskProcess(t, svc, executionID)
	childPID = waitForTaskChildPID(t, pidFile)

	if err := svc.Stop(executionID); err != nil {
		t.Fatalf("Stop process tree returned an error: %v", err)
	}
	result := waitForTaskResult(t, resultCh)
	resultRead = true
	if result.err != nil || result.result.ExitCode != -1 {
		t.Fatalf("unexpected process-tree result: %+v, err=%v", result.result, result.err)
	}
	waitForTaskProcessExit(t, childPID)
}

func TestTaskServiceExecute_RejectsDuplicateRunningID(t *testing.T) {
	t.Setenv("GO_WANT_TASK_SERVICE_HELPER", "1")
	svc := NewTaskService(newTaskTestAgent(t))
	executionID := "duplicate"
	resultCh := executeTaskForTest(svc, executionID, taskServiceHelperCommand())
	waitForTaskProcess(t, svc, executionID)
	t.Cleanup(func() {
		_ = svc.Stop(executionID)
		_ = waitForTaskResult(t, resultCh)
	})

	_, err := svc.execute(executionID, "go version", "")
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected duplicate execution error, got %v", err)
	}
	if got := taskServiceActiveCount(svc); got != 1 {
		t.Fatalf("duplicate Execute disturbed active task: active=%d", got)
	}
}

func TestTaskServiceStop_BeforeExecutePreventsProcessStart(t *testing.T) {
	t.Setenv("GO_WANT_TASK_SERVICE_HELPER", "1")
	svc := NewTaskService(newTaskTestAgent(t))
	executionID := "stop-before-execute"
	if err := svc.Stop(executionID); err != nil {
		t.Fatalf("early Stop returned an error: %v", err)
	}

	result, err := svc.execute(executionID, taskServiceHelperCommand(), "")
	if err != nil {
		t.Fatalf("Execute returned an error for a pre-stopped task: %v", err)
	}
	if result.ExitCode != -1 || !strings.Contains(result.Stderr, "terminated before start") {
		t.Fatalf("pre-stopped task unexpectedly ran: %+v", result)
	}
	if got := taskServiceActiveCount(svc); got != 0 {
		t.Fatalf("pre-stopped task leaked an active registration: %d", got)
	}
	if got := taskServicePendingStopCount(svc); got != 0 {
		t.Fatalf("consumed stop tombstone was not removed: %d", got)
	}
}

func TestTaskServiceStop_PendingStopsAreBounded(t *testing.T) {
	svc := NewTaskService(&AgentService{})
	for i := 0; i < taskPendingStopLimit+40; i++ {
		if err := svc.Stop(fmt.Sprintf("unknown-%d", i)); err != nil {
			t.Fatalf("Stop unknown-%d: %v", i, err)
		}
	}
	if got := taskServicePendingStopCount(svc); got != taskPendingStopLimit {
		t.Fatalf("pending stop table size = %d, want %d", got, taskPendingStopLimit)
	}
}

func TestTaskServiceExecute_TimeoutTerminatesAndCleansProcess(t *testing.T) {
	t.Setenv("GO_WANT_TASK_SERVICE_HELPER", "1")
	svc := NewTaskService(newTaskTestAgent(t))
	svc.executionLimit = 100 * time.Millisecond

	result, err := svc.execute("timeout", taskServiceHelperCommand(), "")
	if err != nil {
		t.Fatalf("Execute returned an error on timeout: %v", err)
	}
	if result.ExitCode != -1 || !strings.Contains(result.Stderr, "timed out") {
		t.Fatalf("unexpected timeout result: %+v", result)
	}
	if got := taskServiceActiveCount(svc); got != 0 {
		t.Fatalf("timed-out task leaked an active registration: %d", got)
	}
}

func TestTaskServiceExecute_BoundsCapturedStdoutAndStderr(t *testing.T) {
	svc := NewTaskService(newTaskTestAgent(t))
	commandLine := shellQuote(os.Args[0]) + " '-test.run=TestAgentCommandOutputHelper' -- agent-command-output-helper"
	result, err := svc.execute("bounded-output", commandLine, "")
	if err != nil {
		t.Fatalf("execute output helper: %v", err)
	}
	for name, output := range map[string]string{"stdout": result.Stdout, "stderr": result.Stderr} {
		if len(output) > maxAgentCommandOutputBytes {
			t.Fatalf("%s length = %d, want <= %d", name, len(output), maxAgentCommandOutputBytes)
		}
		if !utf8.ValidString(output) {
			t.Fatalf("%s is not valid UTF-8", name)
		}
		if !strings.Contains(output, "[truncated,") {
			t.Fatalf("%s lacks an explicit truncation marker", name)
		}
	}
}

func TestTaskServiceExecute_TerminationFailureReturnsAndKeepsHandleForRetry(t *testing.T) {
	t.Setenv("GO_WANT_TASK_SERVICE_HELPER", "1")
	svc := NewTaskService(newTaskTestAgent(t))
	svc.executionLimit = 75 * time.Millisecond
	svc.terminationWait = 75 * time.Millisecond
	var attempts atomic.Int32
	svc.terminateProcess = func(process *os.Process) error {
		if attempts.Add(1) == 1 {
			return errors.New("injected termination failure")
		}
		return terminateTaskProcessTree(process)
	}

	started := time.Now()
	result, err := svc.execute("retry-termination", taskServiceHelperCommand(), "")
	if err == nil || !strings.Contains(err.Error(), "injected termination failure") {
		t.Fatalf("expected bounded termination error, got result=%+v err=%v", result, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Execute remained blocked after termination failure: %s", elapsed)
	}
	if got := taskServiceActiveCount(svc); got != 1 {
		t.Fatalf("failed termination lost its retryable handle: active=%d", got)
	}
	if err := svc.Stop("retry-termination"); err != nil {
		t.Fatalf("retry Stop failed: %v", err)
	}
	waitForTaskRegistrationCleanup(t, svc, "retry-termination")
	if attempts.Load() < 2 {
		t.Fatalf("termination was not retried: attempts=%d", attempts.Load())
	}
}

func TestTaskServiceStop_HungTerminatorIsBoundedAndSingleFlight(t *testing.T) {
	t.Setenv("GO_WANT_TASK_SERVICE_HELPER", "1")
	svc := NewTaskService(newTaskTestAgent(t))
	svc.executionLimit = 5 * time.Second
	svc.terminationCallLimit = 75 * time.Millisecond
	svc.terminationWait = 75 * time.Millisecond
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
	})
	var attempts atomic.Int32
	svc.terminateProcess = func(process *os.Process) error {
		attempts.Add(1)
		<-release
		return terminateTaskProcessTree(process)
	}
	executionID := "hung-terminator"
	resultCh := executeTaskForTest(svc, executionID, taskServiceHelperCommand())
	waitForTaskProcess(t, svc, executionID)

	started := time.Now()
	err := svc.Stop(executionID)
	if err == nil || !strings.Contains(err.Error(), "did not complete") {
		t.Fatalf("hung terminator should return a bounded error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Stop blocked on hung terminator for %s", elapsed)
	}
	result := waitForTaskResult(t, resultCh)
	if result.err == nil || !strings.Contains(result.err.Error(), "did not complete") {
		t.Fatalf("Execute should return after hung termination attempt, got %+v err=%v", result.result, result.err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("concurrent Stop/Execute spawned %d termination attempts, want 1", attempts.Load())
	}
	if got := taskServiceActiveCount(svc); got != 1 {
		t.Fatalf("hung termination lost its active handle: %d", got)
	}

	releaseOnce.Do(func() { close(release) })
	waitForTaskRegistrationCleanup(t, svc, executionID)
}

func TestTaskServiceShutdown_TerminatesAllAndRejectsNewExecutions(t *testing.T) {
	t.Setenv("GO_WANT_TASK_SERVICE_HELPER", "1")
	svc := NewTaskService(newTaskTestAgent(t))
	first := executeTaskForTest(svc, "shutdown-one", taskServiceHelperCommand())
	second := executeTaskForTest(svc, "shutdown-two", taskServiceHelperCommand())
	waitForTaskProcess(t, svc, "shutdown-one")
	waitForTaskProcess(t, svc, "shutdown-two")
	if err := svc.Stop("pending-before-shutdown"); err != nil {
		t.Fatalf("create pending stop: %v", err)
	}

	if err := svc.Shutdown(); err != nil {
		t.Fatalf("Shutdown returned an error: %v", err)
	}
	for _, resultCh := range []<-chan taskTestResult{first, second} {
		result := waitForTaskResult(t, resultCh)
		if result.err != nil || result.result.ExitCode != -1 {
			t.Fatalf("unexpected shutdown task result: %+v err=%v", result.result, result.err)
		}
	}
	if got := taskServiceActiveCount(svc); got != 0 {
		t.Fatalf("Shutdown left %d active tasks", got)
	}
	if got := taskServicePendingStopCount(svc); got != 0 {
		t.Fatalf("Shutdown left %d pending stops", got)
	}
	if _, err := svc.execute("after-shutdown", "go version", ""); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("Execute after Shutdown should be rejected, got %v", err)
	}
	if err := svc.Shutdown(); err != nil {
		t.Fatalf("repeated Shutdown returned an error: %v", err)
	}
}

func TestTaskServiceShutdown_DoesNotWaitForPreStartValidationFailure(t *testing.T) {
	outsideRoot := t.TempDir()
	agent := newTaskTestAgent(t)
	agent.mu.Lock()
	agentLocked := true
	defer func() {
		if agentLocked {
			agent.mu.Unlock()
		}
	}()

	svc := NewTaskService(agent)
	svc.terminationWait = 500 * time.Millisecond
	resultCh := make(chan taskTestResult, 1)
	go func() {
		result, err := svc.execute("pre-start-validation", "go version", outsideRoot)
		resultCh <- taskTestResult{result: result, err: err}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		svc.mu.Lock()
		_, registered := svc.active["pre-start-validation"]
		svc.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task did not register before cwd validation")
		}
		time.Sleep(time.Millisecond)
	}

	shutdownCh := make(chan error, 1)
	go func() {
		shutdownCh <- svc.Shutdown()
	}()
	deadline = time.Now().Add(5 * time.Second)
	for {
		svc.mu.Lock()
		shuttingDown := svc.shuttingDown
		svc.mu.Unlock()
		if shuttingDown {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Shutdown did not capture the pre-start execution")
		}
		time.Sleep(time.Millisecond)
	}

	agent.mu.Unlock()
	agentLocked = false
	result := waitForTaskResult(t, resultCh)
	if result.err == nil {
		t.Fatal("Execute unexpectedly accepted cwd outside the workspace")
	}
	select {
	case err := <-shutdownCh:
		if err != nil {
			t.Fatalf("Shutdown waited on a completed pre-start execution: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown blocked after pre-start execution returned")
	}
}

func TestTaskServiceStop_AfterWaitCompletionDoesNotRelabelNaturalExit(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	execution := &taskExecution{
		cancel:           cancel,
		terminateProcess: terminateCoverageProcessTree,
	}
	execution.finish()
	svc := NewTaskService(&AgentService{})
	svc.active["finished"] = execution

	if err := svc.Stop("finished"); err != nil {
		t.Fatalf("Stop after completion returned an error: %v", err)
	}
	if reason := execution.stopped(); reason != taskNotStopped {
		t.Fatalf("completed execution was relabelled as stopped: reason=%d", reason)
	}
}

func TestTaskServiceExecute_ValidatesIDAndAgentInjection(t *testing.T) {
	svc := NewTaskService()
	if _, err := svc.execute("", "go version", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty execution ID should return ErrInvalidInput, got %v", err)
	}
	if err := svc.Stop(strings.Repeat("x", taskExecutionIDMaxLength+1)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversized execution ID should return ErrInvalidInput, got %v", err)
	}
	if _, err := svc.execute("no-agent", "go version", ""); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "agent service") {
		t.Fatalf("missing AgentService should return ErrInvalidInput, got %v", err)
	}
	if got := taskServiceActiveCount(svc); got != 0 {
		t.Fatalf("failed Execute leaked an active registration: %d", got)
	}
}

func TestTaskServiceExecute_RequiresBackendApproval(t *testing.T) {
	svc := NewTaskService(&AgentService{})
	result, err := svc.Execute("direct", "go version", "")
	if !errors.Is(err, ErrInvalidInput) || !result.Blocked || !strings.Contains(result.BlockReason, "approval token") {
		t.Fatalf("direct Execute should be blocked, got result=%+v err=%v", result, err)
	}
}

func TestTaskServiceExecuteApproved_BindsExecutionIDAndIsSingleUse(t *testing.T) {
	agent := newTaskTestAgent(t)
	agent.approveCommand = func(command, cwd string, risk RiskLevel) bool { return true }
	svc := NewTaskService(agent)

	token, err := svc.RequestExecutionApproval(trustedTaskContext(), "task-a", "go version", "")
	if err != nil {
		t.Fatalf("RequestExecutionApproval failed: %v", err)
	}
	if _, err := svc.ExecuteApproved(trustedTaskContext(), "task-b", "go version", "", token); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("mismatched execution ID should be rejected, got %v", err)
	}
	if _, err := svc.ExecuteApproved(trustedTaskContext(), "task-a", "go version", "", token); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("mismatched attempt should consume the task approval, got %v", err)
	}

	token, err = svc.RequestExecutionApproval(trustedTaskContext(), "task-c", "go version", "")
	if err != nil {
		t.Fatalf("second RequestExecutionApproval failed: %v", err)
	}
	if _, err := svc.ExecuteApproved(trustedTaskContext(), "task-c", "go version", "", token); err != nil {
		t.Fatalf("ExecuteApproved failed: %v", err)
	}
	if _, err := svc.ExecuteApproved(trustedTaskContext(), "task-c", "go version", "", token); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("task approval should be single-use, got %v", err)
	}
}

type taskTestResult struct {
	result ExecResult
	err    error
}

func executeTaskForTest(svc *TaskService, executionID, command string) <-chan taskTestResult {
	resultCh := make(chan taskTestResult, 1)
	go func() {
		result, err := svc.execute(executionID, command, "")
		resultCh <- taskTestResult{result: result, err: err}
	}()
	return resultCh
}

func waitForTaskResult(t *testing.T, resultCh <-chan taskTestResult) taskTestResult {
	t.Helper()
	select {
	case result := <-resultCh:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for task execution to finish")
		return taskTestResult{}
	}
}

func waitForTaskProcess(t *testing.T, svc *TaskService, executionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		execution := svc.active[executionID]
		svc.mu.Unlock()
		if execution != nil {
			execution.mu.Lock()
			started := execution.cmd != nil && execution.cmd.Process != nil
			execution.mu.Unlock()
			if started {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task execution %q did not start", executionID)
}

func waitForTaskRegistrationCleanup(t *testing.T, svc *TaskService, executionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		_, active := svc.active[executionID]
		svc.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task execution %q was not cleaned up", executionID)
}

func waitForTaskChildPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				return pid
			}
			// The helper publishes the file atomically, but tolerate an older or
			// interrupted writer long enough to report a useful timeout.
			time.Sleep(10 * time.Millisecond)
			continue
		}
		// Windows can briefly deny a second open while the helper's atomic
		// rename/share state settles. Treat all transient open errors as a
		// bounded wait; the timeout below preserves the final diagnostic.
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("timed out waiting for task child pid %q: %v", pidFile, lastErr)
	}
	t.Fatal("timed out waiting for task child pid")
	return 0
}

func readTaskChildPID(pidFile string) int {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func waitForTaskProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !taskProcessExists(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %d survived task termination", pid)
}

func taskProcessExists(pid int) bool {
	if runtime.GOOS == "windows" {
		output, err := command(
			"tasklist.exe",
			"/FI", fmt.Sprintf("PID eq %d", pid),
			"/FO", "CSV",
			"/NH",
		).Output()
		return err == nil && strings.Contains(string(output), fmt.Sprintf("\"%d\"", pid))
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

func taskServiceActiveCount(svc *TaskService) int {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return len(svc.active)
}

func taskServicePendingStopCount(svc *TaskService) int {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return len(svc.pendingStops)
}

func taskServiceHelperCommand() string {
	return shellQuote(os.Args[0]) + " '-test.run=TestTaskServiceHelperProcess'"
}

func TestTaskServiceHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_TASK_SERVICE_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestTaskServiceProcessTreeHelper(t *testing.T) {
	mode := os.Getenv("GO_WANT_TASK_SERVICE_TREE_HELPER")
	if mode == "" {
		return
	}
	if mode == "parent" {
		child := command(os.Args[0], "-test.run=TestTaskServiceProcessTreeHelper")
		child.Env = replaceTaskTestEnv("GO_WANT_TASK_SERVICE_TREE_HELPER", "child")
		if err := child.Start(); err != nil {
			t.Fatalf("start child helper: %v", err)
		}
		pidFile := os.Getenv("TASK_SERVICE_CHILD_PID_FILE")
		pidTemp := pidFile + ".tmp"
		if err := os.WriteFile(pidTemp, []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			_ = child.Process.Kill()
			t.Fatalf("write child pid: %v", err)
		}
		if err := os.Rename(pidTemp, pidFile); err != nil {
			_ = child.Process.Kill()
			t.Fatalf("publish child pid: %v", err)
		}
	}
	for {
		time.Sleep(time.Second)
	}
}

func replaceTaskTestEnv(key, value string) []string {
	prefix := key + "="
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if len(entry) >= len(prefix) && strings.EqualFold(entry[:len(prefix)], prefix) {
			continue
		}
		env = append(env, entry)
	}
	return append(env, prefix+value)
}
