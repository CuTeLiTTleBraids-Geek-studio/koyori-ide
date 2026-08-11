package services

// Plan 11 Task 10 Step 11 — AIGoalService 测试覆盖。
//
// 覆盖：创建（含 G-SEC-03 显式确认）/运行/暂停/恢复/中止/终止条件
// （MaxIterations/MaxCost/MaxDuration/3次错误/成功标准）/检查点创建与回滚
// /成本报告/安全边界（git push --force/路径边界）/Goal 与 Plan 互斥。

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// mock 实现
// ---------------------------------------------------------------------------

// mockGoalExecutor 控制 Plan/Execute/Evaluate 行为。
type mockGoalExecutor struct {
	planErr       error
	executeErr    error
	executeResult GoalRoundResult
	evaluateErr   error
	evaluateOK    bool // 是否达成成功标准
	maxEvaluate   int  // 在第 N 轮后达成（0=立即）
}

func (m *mockGoalExecutor) Plan(goal *Goal) (string, error) {
	if m.planErr != nil {
		return "", m.planErr
	}
	return "plan steps", nil
}

func (m *mockGoalExecutor) Execute(goal *Goal, steps string) (GoalRoundResult, error) {
	if m.executeErr != nil {
		return GoalRoundResult{}, m.executeErr
	}
	return m.executeResult, nil
}

func (m *mockGoalExecutor) Evaluate(goal *Goal) (bool, error) {
	if m.evaluateErr != nil {
		return false, m.evaluateErr
	}
	if m.maxEvaluate > 0 && goal.Iteration < m.maxEvaluate {
		return false, nil
	}
	return m.evaluateOK, nil
}

// mockSecurityChecker 安全边界 mock。
type mockSecurityChecker struct {
	blockedCommands  []string
	workspacePath    string
	allowWorkspaceFn func(path string) bool
}

func (m *mockSecurityChecker) CheckCommand(command string) CommandCheck {
	for _, blocked := range m.blockedCommands {
		if strings.Contains(command, blocked) {
			return CommandCheck{RiskLevel: RiskDangerous, Blocked: true, BlockReason: "blocked by mock"}
		}
	}
	return CommandCheck{RiskLevel: RiskElevated, Blocked: false}
}

func (m *mockSecurityChecker) IsWorkspacePath(path string) bool {
	if m.allowWorkspaceFn != nil {
		return m.allowWorkspaceFn(path)
	}
	return strings.HasPrefix(path, m.workspacePath)
}

func newTestGoalService(t *testing.T) *AIGoalService {
	t.Helper()
	return NewAIGoalService()
}

// ---------------------------------------------------------------------------
// 创建测试
// ---------------------------------------------------------------------------

func TestAIGoalService_CreateGoal(t *testing.T) {
	svc := newTestGoalService(t)
	g, err := svc.CreateGoal("g1", "build feature", "tests pass", 5, 0.5, 10*time.Minute, true)
	if err != nil {
		t.Fatalf("CreateGoal failed: %v", err)
	}
	if g.ID != "g1" {
		t.Errorf("ID = %q, want g1", g.ID)
	}
	if g.Status != GoalStatusCreated {
		t.Errorf("Status = %s, want created", g.Status)
	}
	if g.MaxIterations != 5 {
		t.Errorf("MaxIterations = %d, want 5", g.MaxIterations)
	}
	// Goal 与 Plan 互斥：active 应指向当前 Goal。
	active := svc.GetActiveGoal()
	if active == nil || active.ID != "g1" {
		t.Error("active goal should be g1")
	}
}

func TestAIGoalService_GetActiveGoal_NilWhenNone(t *testing.T) {
	svc := newTestGoalService(t)
	if active := svc.GetActiveGoal(); active != nil {
		t.Fatalf("expected nil active goal, got %+v", active)
	}
}

func TestAIGoalService_CreateGoal_RequiresConfirmation(t *testing.T) {
	// G-SEC-03（Step 10）：创建需显式确认。
	svc := newTestGoalService(t)
	_, err := svc.CreateGoal("g", "desc", "criteria", 5, 0.5, 10*time.Minute, false)
	if err == nil || !strings.Contains(err.Error(), "explicit confirmation") {
		t.Errorf("expected G-SEC-03 confirmation error, got %v", err)
	}
}

func TestAIGoalService_CreateGoal_Duplicate(t *testing.T) {
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("dup", "d", "c", 5, 0.5, 10*time.Minute, true)
	_, err := svc.CreateGoal("dup", "d2", "c2", 5, 0.5, 10*time.Minute, true)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected already-exists, got %v", err)
	}
}

func TestAIGoalService_CreateGoal_Defaults(t *testing.T) {
	svc := newTestGoalService(t)
	g, err := svc.CreateGoal("g", "d", "c", 0, 0, 0, true)
	if err != nil {
		t.Fatalf("CreateGoal failed: %v", err)
	}
	if g.MaxIterations != 10 {
		t.Errorf("default MaxIterations = %d, want 10", g.MaxIterations)
	}
	if g.MaxCost != 1.0 {
		t.Errorf("default MaxCost = %f, want 1.0", g.MaxCost)
	}
	if g.MaxDuration != 30*time.Minute {
		t.Errorf("default MaxDuration = %v, want 30m", g.MaxDuration)
	}
}

// ---------------------------------------------------------------------------
// 运行测试
// ---------------------------------------------------------------------------

func TestAIGoalService_RunGoal_Success(t *testing.T) {
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("g1", "desc", "tests pass", 5, 1.0, 10*time.Minute, true)
	exec := &mockGoalExecutor{
		executeResult: GoalRoundResult{Cost: 0.01, Tokens: 100, Snapshot: "snap1"},
		evaluateOK:    true,
		maxEvaluate:   2, // 第 2 轮后达成
	}
	if err := svc.RunGoal("g1", exec, nil); err != nil {
		t.Fatalf("RunGoal failed: %v", err)
	}
	v, _ := svc.GetGoal("g1")
	if v.Status != GoalStatusCompleted {
		t.Errorf("Status = %s, want completed", v.Status)
	}
	if v.Iteration != 2 {
		t.Errorf("Iteration = %d, want 2", v.Iteration)
	}
	if v.FinishedAt == nil {
		t.Error("FinishedAt should be set")
	}
}

func TestAIGoalService_RunGoal_MaxIterations(t *testing.T) {
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("g1", "desc", "criteria", 3, 10.0, 10*time.Minute, true)
	exec := &mockGoalExecutor{
		executeResult: GoalRoundResult{Cost: 0.001, Tokens: 10},
		evaluateOK:    false, // 永不达成
	}
	// BUG3: 预算耗尽是预期内停止，不再返回错误（避免 Wails 包成 RuntimeError）。
	// 状态置为 Failed，LastError 记录原因。
	err := svc.RunGoal("g1", exec, nil)
	if err != nil {
		t.Errorf("expected nil error on max iterations, got %v", err)
	}
	v, _ := svc.GetGoal("g1")
	if v.Status != GoalStatusFailed {
		t.Errorf("Status = %s, want failed", v.Status)
	}
	if !strings.Contains(v.LastError, "max iterations") {
		t.Errorf("LastError = %q, want it to contain 'max iterations'", v.LastError)
	}
}

func TestAIGoalService_RunGoal_MaxCost(t *testing.T) {
	svc := newTestGoalService(t)
	// 预算 $0.05，每轮 $0.02 → 第 3 轮超限。
	_, _ = svc.CreateGoal("g1", "desc", "criteria", 100, 0.05, 10*time.Minute, true)
	exec := &mockGoalExecutor{
		executeResult: GoalRoundResult{Cost: 0.02, Tokens: 10},
		evaluateOK:    false,
	}
	// BUG3: 预算耗尽是预期内停止，不再返回错误。
	err := svc.RunGoal("g1", exec, nil)
	if err != nil {
		t.Errorf("expected nil error on max cost, got %v", err)
	}
	v, _ := svc.GetGoal("g1")
	if v.Status != GoalStatusFailed {
		t.Errorf("Status = %s, want failed", v.Status)
	}
	if !strings.Contains(v.LastError, "max cost") {
		t.Errorf("LastError = %q, want it to contain 'max cost'", v.LastError)
	}
}

func TestAIGoalService_RunGoal_ConsecutiveErrors(t *testing.T) {
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("g1", "desc", "criteria", 100, 10.0, 10*time.Minute, true)
	exec := &mockGoalExecutor{
		executeErr: errMock("execute failed"),
	}
	// BUG3: 连续错误终止也不返回错误，状态置为 Failed + LastError。
	err := svc.RunGoal("g1", exec, nil)
	if err != nil {
		t.Errorf("expected nil error on consecutive errors, got %v", err)
	}
	v, _ := svc.GetGoal("g1")
	if v.Status != GoalStatusFailed {
		t.Errorf("Status = %s, want failed", v.Status)
	}
	if !strings.Contains(v.LastError, "consecutive errors") {
		t.Errorf("LastError = %q, want it to contain 'consecutive errors'", v.LastError)
	}
}

// errMock 简单实现 error。
type errMock string

func (e errMock) Error() string { return string(e) }

// ---------------------------------------------------------------------------
// 暂停/恢复/中止测试
// ---------------------------------------------------------------------------

func TestAIGoalService_AbortGoal(t *testing.T) {
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("g1", "desc", "criteria", 5, 1.0, 10*time.Minute, true)
	if err := svc.AbortGoal("g1"); err != nil {
		t.Fatalf("AbortGoal failed: %v", err)
	}
	v, _ := svc.GetGoal("g1")
	if v.Status != GoalStatusAborted {
		t.Errorf("Status = %s, want aborted", v.Status)
	}
	if active := svc.GetActiveGoal(); active != nil {
		t.Error("active goal should be cleared after abort")
	}
}

func TestAIGoalService_PauseGoal_NotRunning(t *testing.T) {
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("g1", "desc", "criteria", 5, 1.0, 10*time.Minute, true)
	// Goal 状态是 created，不能暂停。
	err := svc.PauseGoal("g1")
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("expected not-running error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 检查点与回滚测试（Step 5）
// ---------------------------------------------------------------------------

func TestAIGoalService_Checkpoint_AutoCreation(t *testing.T) {
	// defaultCheckpointInterval=3，运行 5 轮应创建 1 个检查点（第 3 轮）。
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("g1", "desc", "criteria", 5, 10.0, 10*time.Minute, true)
	exec := &mockGoalExecutor{
		executeResult: GoalRoundResult{Cost: 0.01, Tokens: 10, Snapshot: "snap"},
		evaluateOK:    true,
		maxEvaluate:   5,
	}
	_ = svc.RunGoal("g1", exec, nil)
	v, _ := svc.GetGoal("g1")
	// 第 3 轮创建检查点，第 5 轮达成。
	if len(v.Checkpoints) < 1 {
		t.Errorf("expected at least 1 checkpoint, got %d", len(v.Checkpoints))
	}
	if v.Checkpoints[0].Iteration != 3 {
		t.Errorf("checkpoint iteration = %d, want 3", v.Checkpoints[0].Iteration)
	}
}

func TestAIGoalService_CreateCheckpoint_Manual(t *testing.T) {
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("g1", "desc", "criteria", 5, 1.0, 10*time.Minute, true)
	if err := svc.CreateCheckpoint("g1", "manual-snap", "manual note"); err != nil {
		t.Fatalf("CreateCheckpoint failed: %v", err)
	}
	cps, _ := svc.ListCheckpoints("g1")
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(cps))
	}
	if cps[0].Snapshot != "manual-snap" {
		t.Errorf("Snapshot = %q, want manual-snap", cps[0].Snapshot)
	}
}

func TestAIGoalService_RollbackToCheckpoint(t *testing.T) {
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("g1", "desc", "criteria", 10, 10.0, 10*time.Minute, true)
	// 手动创建两个检查点。
	_ = svc.CreateCheckpoint("g1", "cp1", "first")
	_ = svc.CreateCheckpoint("g1", "cp2", "second")
	// 回滚到第 0 个检查点。
	if err := svc.RollbackToCheckpoint("g1", 0); err != nil {
		t.Fatalf("RollbackToCheckpoint failed: %v", err)
	}
	v, _ := svc.GetGoal("g1")
	// 后续检查点应被丢弃。
	if len(v.Checkpoints) != 1 {
		t.Errorf("expected 1 checkpoint after rollback, got %d", len(v.Checkpoints))
	}
	if v.Status != GoalStatusPaused {
		t.Errorf("Status = %s, want paused", v.Status)
	}
}

func TestAIGoalService_RollbackToCheckpoint_OutOfRange(t *testing.T) {
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("g1", "desc", "criteria", 5, 1.0, 10*time.Minute, true)
	err := svc.RollbackToCheckpoint("g1", 5)
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("expected out-of-range, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 成本报告测试（Step 6）
// ---------------------------------------------------------------------------

func TestAIGoalService_GetCostReport(t *testing.T) {
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("g1", "desc", "criteria", 5, 0.5, 10*time.Minute, true)
	exec := &mockGoalExecutor{
		executeResult: GoalRoundResult{Cost: 0.1, Tokens: 1000},
		evaluateOK:    true,
		maxEvaluate:   2,
	}
	_ = svc.RunGoal("g1", exec, nil)
	report, err := svc.GetCostReport("g1")
	if err != nil {
		t.Fatalf("GetCostReport failed: %v", err)
	}
	if report.TotalCost != 0.2 {
		t.Errorf("TotalCost = %f, want 0.2", report.TotalCost)
	}
	if report.TotalTokens != 2000 {
		t.Errorf("TotalTokens = %d, want 2000", report.TotalTokens)
	}
	if report.RemainingCost != 0.3 {
		t.Errorf("RemainingCost = %f, want 0.3", report.RemainingCost)
	}
}

// ---------------------------------------------------------------------------
// 安全边界测试（Step 8）
// ---------------------------------------------------------------------------

func TestAIGoalService_CheckSecurityBoundary_BlockedCommand(t *testing.T) {
	svc := newTestGoalService(t)
	checker := &mockSecurityChecker{blockedCommands: []string{"rm -rf"}}
	err := svc.CheckSecurityBoundary("rm -rf /", checker, false)
	if err == nil || !strings.Contains(err.Error(), "blocked by CheckCommand") {
		t.Errorf("expected blocked error, got %v", err)
	}
}

func TestAIGoalService_CheckSecurityBoundary_GitPushForce(t *testing.T) {
	svc := newTestGoalService(t)
	checker := &mockSecurityChecker{}
	// Step 8：禁 git push --force。
	err := svc.CheckSecurityBoundary("git push --force origin main", checker, false)
	if err == nil || !strings.Contains(err.Error(), "git push --force forbidden") {
		t.Errorf("expected git push --force forbidden, got %v", err)
	}
}

func TestAIGoalService_CheckSecurityBoundary_GitPushForce_AllowDangerous(t *testing.T) {
	svc := newTestGoalService(t)
	checker := &mockSecurityChecker{}
	// 显式授权时允许。
	err := svc.CheckSecurityBoundary("git push --force origin main", checker, true)
	if err != nil {
		t.Errorf("expected allowed with allowDangerous, got %v", err)
	}
}

func TestAIGoalService_CheckPathBoundary_Outside(t *testing.T) {
	svc := newTestGoalService(t)
	checker := &mockSecurityChecker{workspacePath: "/workspace"}
	// Step 8：禁删工作区外文件。
	err := svc.CheckPathBoundary("/etc/passwd", checker)
	if err == nil || !strings.Contains(err.Error(), "outside workspace") {
		t.Errorf("expected outside workspace error, got %v", err)
	}
}

func TestAIGoalService_CheckPathBoundary_Inside(t *testing.T) {
	svc := newTestGoalService(t)
	checker := &mockSecurityChecker{workspacePath: "/workspace"}
	err := svc.CheckPathBoundary("/workspace/src/main.go", checker)
	if err != nil {
		t.Errorf("expected allowed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Goal 与 Plan 互斥（Step 8 of Task 9）
// ---------------------------------------------------------------------------

func TestAIGoalService_GoalPlanMutex(t *testing.T) {
	svc := newTestGoalService(t)
	g1, _ := svc.CreateGoal("g1", "d1", "c1", 5, 1.0, 10*time.Minute, true)
	active := svc.GetActiveGoal()
	if active == nil || active.ID != "g1" {
		t.Error("active should be g1")
	}
	g2, _ := svc.CreateGoal("g2", "d2", "c2", 5, 1.0, 10*time.Minute, true)
	if g1 == g2 {
		t.Error("should be different goal instances")
	}
	active = svc.GetActiveGoal()
	if active == nil || active.ID != "g2" {
		t.Error("active should be g2 after new goal")
	}
}

func TestAIGoalService_ListGoals(t *testing.T) {
	svc := newTestGoalService(t)
	_, _ = svc.CreateGoal("g1", "d1", "c1", 5, 1.0, 10*time.Minute, true)
	_, _ = svc.CreateGoal("g2", "d2", "c2", 5, 1.0, 10*time.Minute, true)
	list := svc.ListGoals()
	if len(list) != 2 {
		t.Errorf("expected 2 goals, got %d", len(list))
	}
}

// ---------------------------------------------------------------------------
// GOAL-P0-04A: prototype executor拦截测试
// ---------------------------------------------------------------------------

// protoExecutor 是一个总是报告 IsPrototype() = true 的 GoalExecutor。
// 它不执行任何操作——用来验证 RunGoal 在没有显式 opt-in 时拒绝驱动 prototype。
type protoExecutor struct{}

func (e *protoExecutor) Plan(_ *Goal) (string, error) {
	return "fixed plan for testing", nil
}
func (e *protoExecutor) Execute(_ *Goal, _ string) (GoalRoundResult, error) {
	return GoalRoundResult{Success: true}, nil
}
func (e *protoExecutor) Evaluate(_ *Goal) (bool, error) { return true, nil }
func (e *protoExecutor) IsPrototype() bool              { return true }
func (e *protoExecutor) PrototypeLimitation() string {
	return "test prototype executor: fixed plan, fixed command, always fails evaluate"
}

// TestAIGoalService_PrototypeBlockedByDefault verifies the core P0-04A AC:
// a prototype executor must be refused by default so the user is never
// silently presented with fake autonomous coding output.
func TestAIGoalService_PrototypeBlockedByDefault(t *testing.T) {
	svc := newTestGoalService(t)
	_, err := svc.CreateGoal("g1", "test goal", "done", 3, 1.0, time.Minute, true)
	if err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}

	// Default: prototype execution is disabled.
	if svc.IsPrototypeExecutionEnabled() {
		t.Fatal("prototype execution must be disabled by default (P0-04A)")
	}

	// RunGoal with a prototype executor should return ErrGoalPrototypeDisabled
	// without modifying the goal status, so the UI can explain the boundary.
	err = svc.RunGoal("g1", &protoExecutor{}, nil)
	if !errors.Is(err, ErrGoalPrototypeDisabled) {
		t.Fatalf("RunGoal with prototype executor = %v, want ErrGoalPrototypeDisabled", err)
	}

	// The goal must stay in its original status — not appear "completed" or
	// "running", which would be a lie about capability.
	g, _ := svc.GetGoal("g1")
	if g.Status == GoalStatusCompleted {
		t.Fatal("goal must not appear completed when prototype was blocked")
	}
	if g.Status == GoalStatusRunning {
		t.Fatal("goal must not appear running when prototype was blocked")
	}
}

// TestAIGoalService_PrototypeAllowedAfterOptIn verifies that the user can
// explicitly enable the prototype loop and it then runs (while still being
// bounded by the normal iteration / cost / error limits).
func TestAIGoalService_PrototypeAllowedAfterOptIn(t *testing.T) {
	svc := newTestGoalService(t)
	_, err := svc.CreateGoal("g1", "test goal", "done", 1, 1.0, time.Minute, true)
	if err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}

	svc.SetPrototypeExecutionEnabled(true)
	if !svc.IsPrototypeExecutionEnabled() {
		t.Fatal("prototype should be enabled after SetPrototypeExecutionEnabled(true)")
	}

	// protoExecutor.Evaluate always returns true, so the goal should complete.
	err = svc.RunGoal("g1", &protoExecutor{}, nil)
	if err != nil {
		t.Fatalf("RunGoal with prototype enabled: %v", err)
	}
	g, _ := svc.GetGoal("g1")
	if g.Status != GoalStatusCompleted {
		t.Fatalf("goal status = %s, want %s", g.Status, GoalStatusCompleted)
	}
}

// TestAIGoalService_GetExecutorCapability_PrototypeFlagExposed verifies that
// GetExecutorCapability includes the Prototype flag so the UI can always show
// the prototype banner without a separate RPC call.
func TestAIGoalService_GetExecutorCapability_PrototypeFlagExposed(t *testing.T) {
	svc := newTestGoalService(t)
	svc.setInternalExecutor(&protoExecutor{}, &mockSecurityChecker{})
	_, _ = svc.CreateGoal("g1", "d", "c", 3, 1.0, time.Minute, true)

	cap := svc.GetExecutorCapability()
	if !cap.Prototype {
		t.Fatal("GetExecutorCapability.Prototype must be true when internal executor is prototype")
	}
	if cap.Limitation == "" {
		t.Fatal("GetExecutorCapability.Limitation must be non-empty for prototype executor")
	}
}
