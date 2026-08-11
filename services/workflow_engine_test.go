package services

// Plan 11 Task 11 Step 10 — WorkflowEngine 测试覆盖。
//
// 覆盖：并行执行/串行执行/条件 DSL/失败处理（abort/continue/skip）
// /触发防抖/运行历史/CheckCommand 阻止。

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// mock executor
// ---------------------------------------------------------------------------

type mockWorkflowExecutor struct {
	mu           sync.Mutex
	commandCount int
	aiCount      int
	outputs      map[string]string
	errors       map[string]error
	blocked      []string // 被 CheckCommand 阻止的命令
	execOrder    []string // 执行顺序记录
}

func newMockWorkflowExecutor() *mockWorkflowExecutor {
	return &mockWorkflowExecutor{
		outputs: make(map[string]string),
		errors:  make(map[string]error),
	}
}

func (m *mockWorkflowExecutor) ExecuteCommand(step WorkflowStep) (string, error) {
	m.mu.Lock()
	m.commandCount++
	m.execOrder = append(m.execOrder, step.Name)
	m.mu.Unlock()
	time.Sleep(10 * time.Millisecond) // 模拟执行时间
	if err, ok := m.errors[step.Name]; ok {
		return "", err
	}
	if out, ok := m.outputs[step.Name]; ok {
		return out, nil
	}
	return "ok", nil
}

func (m *mockWorkflowExecutor) ExecuteAI(step WorkflowStep) (string, error) {
	m.mu.Lock()
	m.aiCount++
	m.execOrder = append(m.execOrder, step.Name)
	m.mu.Unlock()
	if err, ok := m.errors[step.Name]; ok {
		return "", err
	}
	return "ai-output", nil
}

func (m *mockWorkflowExecutor) CheckCommand(command string) CommandCheck {
	for _, blocked := range m.blocked {
		if strings.Contains(command, blocked) {
			return CommandCheck{RiskLevel: RiskDangerous, Blocked: true, BlockReason: "blocked by mock"}
		}
	}
	return CommandCheck{RiskLevel: RiskElevated, Blocked: false}
}

// ---------------------------------------------------------------------------
// 并行/串行执行测试（Step 3）
// ---------------------------------------------------------------------------

func TestWorkflowEngine_ParallelExecution(t *testing.T) {
	// 3 个无依赖步骤应并行执行。
	exec := newMockWorkflowExecutor()
	engine := NewWorkflowEngine(exec, 0)
	wf := &WorkflowDef{
		Name: "parallel-test",
		Steps: []WorkflowStep{
			{Name: "a", Command: "echo a"},
			{Name: "b", Command: "echo b"},
			{Name: "c", Command: "echo c"},
		},
	}
	history := engine.RunWorkflow(wf, "manual")
	if history.Status != WFRunSuccess {
		t.Errorf("Status = %s, want success", history.Status)
	}
	if len(history.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(history.Steps))
	}
	for _, s := range history.Steps {
		if s.Status != WFStepSuccess {
			t.Errorf("step %s status = %s, want success", s.Name, s.Status)
		}
	}
}

func TestWorkflowEngine_SerialExecution(t *testing.T) {
	// b 依赖 a，应串行执行（a 完成后 b 才开始）。
	exec := newMockWorkflowExecutor()
	engine := NewWorkflowEngine(exec, 0)
	wf := &WorkflowDef{
		Name: "serial-test",
		Steps: []WorkflowStep{
			{Name: "a", Command: "echo a"},
			{Name: "b", Command: "echo b", DependsOn: []string{"a"}},
		},
	}
	history := engine.RunWorkflow(wf, "manual")
	if history.Status != WFRunSuccess {
		t.Errorf("Status = %s, want success", history.Status)
	}
	// 验证执行顺序：a 应在 b 之前。
	exec.mu.Lock()
	defer exec.mu.Unlock()
	if len(exec.execOrder) < 2 {
		t.Fatalf("expected 2 executions, got %d", len(exec.execOrder))
	}
	if exec.execOrder[0] != "a" || exec.execOrder[1] != "b" {
		t.Errorf("execution order = %v, want [a b]", exec.execOrder)
	}
}

func TestComputeLayers_ReturnsErrorOnCycle(t *testing.T) {
	wf := &WorkflowDef{
		Name: "cycle-test",
		Steps: []WorkflowStep{
			{Name: "a", Command: "echo a", DependsOn: []string{"b"}},
			{Name: "b", Command: "echo b", DependsOn: []string{"a"}},
		},
	}

	layers, err := computeLayers(wf)
	if err == nil {
		t.Fatal("expected error for dependency cycle")
	}
	if len(layers) != 0 {
		t.Fatalf("expected no executable layers for a cycle, got %v", layers)
	}

	exec := newMockWorkflowExecutor()
	history := NewWorkflowEngine(exec, 0).RunWorkflow(wf, "manual")
	if history.Status != WFRunFailed {
		t.Fatalf("workflow status = %s, want failed", history.Status)
	}
	if exec.commandCount != 0 {
		t.Fatalf("cycle executed %d commands, want 0", exec.commandCount)
	}
}

// ---------------------------------------------------------------------------
// 条件 DSL 测试（Step 5）
// ---------------------------------------------------------------------------

func TestWorkflowEngine_ConditionDSL_StatusSuccess(t *testing.T) {
	exec := newMockWorkflowExecutor()
	engine := NewWorkflowEngine(exec, 0)
	wf := &WorkflowDef{
		Name: "condition-test",
		Steps: []WorkflowStep{
			{Name: "build", Command: "make build"},
			{Name: "test", Command: "make test", DependsOn: []string{"build"}, Condition: `{{.build.Status}} == "success"`},
		},
	}
	history := engine.RunWorkflow(wf, "manual")
	// build 成功，test 的条件应满足。
	testStep := findStepInHistory(history, "test")
	if testStep == nil {
		t.Fatal("test step not found")
	}
	if testStep.Status != WFStepSuccess {
		t.Errorf("test step status = %s, want success (condition should pass)", testStep.Status)
	}
}

func TestWorkflowEngine_ConditionDSL_SkipOnFailure(t *testing.T) {
	exec := newMockWorkflowExecutor()
	exec.errors["build"] = errMock("build failed")
	engine := NewWorkflowEngine(exec, 0)
	wf := &WorkflowDef{
		Name: "condition-skip-test",
		Steps: []WorkflowStep{
			{Name: "build", Command: "make build"},
			{Name: "test", Command: "make test", DependsOn: []string{"build"}, Condition: `{{.build.Status}} == "success"`},
		},
	}
	history := engine.RunWorkflow(wf, "manual")
	// build 失败，test 的条件 {{.build.Status}} == "success" 不满足 → 跳过。
	testStep := findStepInHistory(history, "test")
	if testStep == nil {
		t.Fatal("test step not found")
	}
	if testStep.Status != WFStepSkipped {
		t.Errorf("test step status = %s, want skipped (condition should fail)", testStep.Status)
	}
}

func TestWorkflowEngine_ConditionDSL_MatchesGlob(t *testing.T) {
	exec := newMockWorkflowExecutor()
	engine := NewWorkflowEngine(exec, 0)
	results := map[string]*WorkflowStepResult{
		"prev": {Name: "prev", Status: WFStepSuccess, Output: "main.go"},
	}
	// {{.prev.Output}} matches "*.go" → true
	ok := engine.EvaluateCondition(`{{.prev.Output}} matches "*.go"`, results)
	if !ok {
		t.Error("condition should match *.go")
	}
	// {{.prev.Output}} matches "*.ts" → false
	ok = engine.EvaluateCondition(`{{.prev.Output}} matches "*.ts"`, results)
	if ok {
		t.Error("condition should not match *.ts")
	}
}

func TestWorkflowEngine_ConditionDSL_NotEquals(t *testing.T) {
	exec := newMockWorkflowExecutor()
	engine := NewWorkflowEngine(exec, 0)
	results := map[string]*WorkflowStepResult{
		"prev": {Name: "prev", Status: WFStepFailed},
	}
	// {{.prev.Status}} != "success" → true (failed != success)
	ok := engine.EvaluateCondition(`{{.prev.Status}} != "success"`, results)
	if !ok {
		t.Error("condition != should pass for failed status")
	}
}

// ---------------------------------------------------------------------------
// 失败处理测试（OnFailure）
// ---------------------------------------------------------------------------

func TestWorkflowEngine_OnFailure_Abort(t *testing.T) {
	exec := newMockWorkflowExecutor()
	exec.errors["a"] = errMock("step a failed")
	engine := NewWorkflowEngine(exec, 0)
	wf := &WorkflowDef{
		Name: "abort-test",
		Steps: []WorkflowStep{
			{Name: "a", Command: "fail-cmd", OnFailure: OnFailureAbort},
			{Name: "b", Command: "echo b"}, // 无依赖，与 a 并行
		},
	}
	history := engine.RunWorkflow(wf, "manual")
	if history.Status != WFRunFailed {
		t.Errorf("Status = %s, want failed", history.Status)
	}
}

func TestWorkflowEngine_OnFailure_Continue(t *testing.T) {
	exec := newMockWorkflowExecutor()
	exec.errors["a"] = errMock("step a failed")
	engine := NewWorkflowEngine(exec, 0)
	wf := &WorkflowDef{
		Name: "continue-test",
		Steps: []WorkflowStep{
			{Name: "a", Command: "fail-cmd", OnFailure: OnFailureContinue},
			{Name: "b", Command: "echo b"},
		},
	}
	history := engine.RunWorkflow(wf, "manual")
	// OnFailure=continue → b 仍应执行，整体状态取决于 b。
	bStep := findStepInHistory(history, "b")
	if bStep == nil {
		t.Fatal("b step not found")
	}
	if bStep.Status != WFStepSuccess {
		t.Errorf("b status = %s, want success (OnFailure=continue)", bStep.Status)
	}
}

// ---------------------------------------------------------------------------
// 触发防抖测试（Step 8）
// ---------------------------------------------------------------------------

func TestWorkflowEngine_ShouldTrigger_Debounce(t *testing.T) {
	exec := newMockWorkflowExecutor()
	engine := NewWorkflowEngine(exec, 100*time.Millisecond)
	// 第一次触发应允许。
	if !engine.ShouldTrigger("wf1", true) {
		t.Error("first trigger should be allowed")
	}
	// 立即再次触发应被防抖阻止。
	if engine.ShouldTrigger("wf1", true) {
		t.Error("second trigger should be debounced")
	}
	// 等待间隔后应允许。
	time.Sleep(150 * time.Millisecond)
	if !engine.ShouldTrigger("wf1", true) {
		t.Error("trigger after interval should be allowed")
	}
}

func TestWorkflowEngine_ShouldTrigger_NotEnabled(t *testing.T) {
	// G-SEC-03: fileChange 需显式启用。
	exec := newMockWorkflowExecutor()
	engine := NewWorkflowEngine(exec, 0)
	if engine.ShouldTrigger("wf1", false) {
		t.Error("trigger should be blocked when not enabled")
	}
}

// ---------------------------------------------------------------------------
// 运行历史测试（Step 7）
// ---------------------------------------------------------------------------

func TestWorkflowEngine_RunHistory(t *testing.T) {
	exec := newMockWorkflowExecutor()
	engine := NewWorkflowEngine(exec, 0)
	wf := &WorkflowDef{
		Name: "history-test",
		Steps: []WorkflowStep{
			{Name: "a", Command: "echo a"},
		},
	}
	engine.RunWorkflow(wf, "manual")
	engine.RunWorkflow(wf, "startup")
	history := engine.GetHistory()
	if len(history) != 2 {
		t.Errorf("expected 2 history entries, got %d", len(history))
	}
	if history[0].Workflow != "history-test" {
		t.Errorf("workflow name = %q, want history-test", history[0].Workflow)
	}
	if len(history[0].Steps) != 1 {
		t.Errorf("expected 1 step in history, got %d", len(history[0].Steps))
	}
	// 清除历史。
	engine.ClearHistory()
	if len(engine.GetHistory()) != 0 {
		t.Error("history should be empty after clear")
	}
}

// ---------------------------------------------------------------------------
// CheckCommand 阻止测试（Step 9: G-SEC-02）
// ---------------------------------------------------------------------------

func TestWorkflowEngine_CheckCommand_Blocked(t *testing.T) {
	exec := newMockWorkflowExecutor()
	exec.blocked = []string{"rm -rf"}
	engine := NewWorkflowEngine(exec, 0)
	wf := &WorkflowDef{
		Name: "blocked-test",
		Steps: []WorkflowStep{
			{Name: "dangerous", Command: "rm -rf /"},
		},
	}
	history := engine.RunWorkflow(wf, "manual")
	if history.Status != WFRunFailed {
		t.Errorf("Status = %s, want failed (command blocked)", history.Status)
	}
	step := findStepInHistory(history, "dangerous")
	if step == nil {
		t.Fatal("step not found")
	}
	if step.Status != WFStepFailed {
		t.Errorf("step status = %s, want failed", step.Status)
	}
	if !strings.Contains(step.Error, "blocked") {
		t.Errorf("error = %q, want blocked message", step.Error)
	}
}

// ---------------------------------------------------------------------------
// AI 步骤测试（Step 4）
// ---------------------------------------------------------------------------

func TestWorkflowEngine_AIStep(t *testing.T) {
	exec := newMockWorkflowExecutor()
	engine := NewWorkflowEngine(exec, 0)
	wf := &WorkflowDef{
		Name: "ai-test",
		Steps: []WorkflowStep{
			{Name: "generate", Command: "generate commit message", Type: WorkflowStepAI},
		},
	}
	history := engine.RunWorkflow(wf, "manual")
	if history.Status != WFRunSuccess {
		t.Errorf("Status = %s, want success", history.Status)
	}
	if exec.aiCount != 1 {
		t.Errorf("AI execution count = %d, want 1", exec.aiCount)
	}
	if exec.commandCount != 0 {
		t.Errorf("command execution count = %d, want 0 (should use AI executor)", exec.commandCount)
	}
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

func findStepInHistory(h *WorkflowRunHistory, name string) *WorkflowStepResult {
	for i := range h.Steps {
		if h.Steps[i].Name == name {
			return &h.Steps[i]
		}
	}
	return nil
}
