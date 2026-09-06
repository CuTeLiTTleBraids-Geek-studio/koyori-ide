package services

// Plan 11 Task 9 — Plan 模式（先规划后执行）。
//
// Agent catalog 现含只读 `plan` 工具。CreatePlan 仍接受调用方提供的步骤；
// 空步骤是合法的，UI 必须提示用户 replan 或手填，不得伪造步骤。
// 用户审批：单步/全部批准/全部拒绝/编辑后批准（Step 4）。
// Plan 与 Goal 互斥：Plan 用户驱动逐步；Goal 默认仍要求 opt-in 后才跑 LLM/catalog 循环。
//
// 安全（G-SEC-02）：
//   - 每步 Tool 调用经 AgentService.CheckCommand 审批（Step 9）。
//   - 步骤执行失败暂停 + 重试/跳过/重新规划（Step 7）。

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

// ---------------------------------------------------------------------------
// Plan schema（Step 1）
// ---------------------------------------------------------------------------

// PlanStepStatus 单步状态。
type PlanStepStatus string

const (
	PlanStepPending   PlanStepStatus = "pending"
	PlanStepApproved  PlanStepStatus = "approved"
	PlanStepExecuting PlanStepStatus = "executing"
	PlanStepCompleted PlanStepStatus = "completed"
	PlanStepFailed    PlanStepStatus = "failed"
	PlanStepSkipped   PlanStepStatus = "skipped"
)

// PlanStatus 整体状态。
type PlanStatus string

const (
	PlanStatusDraft     PlanStatus = "draft"     // AI 生成中，未提交
	PlanStatusPending   PlanStatus = "pending"   // 待用户审批
	PlanStatusExecuting PlanStatus = "executing" // 执行中
	PlanStatusPaused    PlanStatus = "paused"    // 失败暂停
	PlanStatusCompleted PlanStatus = "completed"
	PlanStatusAborted   PlanStatus = "aborted"
)

// PlanStep 单个步骤。
type PlanStep struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Status      PlanStepStatus `json:"status"`
	Tool        string         `json:"tool,omitempty"`   // 要调用的工具名
	Args        string         `json:"args,omitempty"`   // 工具参数（JSON）
	Result      string         `json:"result,omitempty"` // 执行结果
	Error       string         `json:"error,omitempty"`  // 失败原因
	StartedAt   *time.Time     `json:"startedAt,omitempty"`
	FinishedAt  *time.Time     `json:"finishedAt,omitempty"`
}

// Plan 完整计划。
type Plan struct {
	ID         string     `json:"id"`
	Goal       string     `json:"goal"`
	Steps      []PlanStep `json:"steps"`
	Status     PlanStatus `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	ApprovedAt *time.Time `json:"approvedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// ---------------------------------------------------------------------------
// AIPlanService
// ---------------------------------------------------------------------------

// AIPlanService 管理 Plan 模式的创建/审批/执行/回放（Step 1-10）。
type AIPlanService struct {
	mu            sync.RWMutex
	plans         map[string]*Plan
	active        *Plan            // 当前活动 Plan（Plan 与 Goal 互斥，Step 8）
	snapshotSvc   *SnapshotService // Step 3: 每步骤前创建快照（可选）
	workspaceRoot string           // Step 3: 快照工作区根（构造期 fallback）
	// wsCtx is the shared workspace identity (GOAL-P0-02). When set it wins over
	// workspaceRoot, because workspaceRoot is captured at bootstrap time — before
	// any project is open — and would otherwise stay empty forever.
	wsCtx *WorkspaceContext
	// 内部 executor，当前端通过 Wails bindings 传入 nil 时回退使用。
	internalExecutor StepExecutor
	lifecycle        *AgentLifecycle
}

// NewAIPlanService 创建服务。
func NewAIPlanService() *AIPlanService {
	return &AIPlanService{
		plans: make(map[string]*Plan),
	}
}

// setSnapshotService 注入快照服务与工作区根（Step 3: Plan 每步骤前创建快照）。
// 两者都设置后，ExecuteStep 会在执行前 best-effort 创建快照。
//
//wails:ignore
func (s *AIPlanService) setSnapshotService(snap *SnapshotService, workspaceRoot string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotSvc = snap
	s.workspaceRoot = workspaceRoot
}

// setWorkspaceContext injects the shared workspace identity (GOAL-P0-02).
// After this call the service always reads the live root, so opening or
// switching a project is visible here without a second setter call.
//
//wails:ignore
func (s *AIPlanService) setWorkspaceContext(ctx *WorkspaceContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wsCtx = ctx
}

// setInternalExecutor 注入内部 executor，当前端传入 nil 时回退使用。
//
//wails:ignore
func (s *AIPlanService) setInternalExecutor(exec StepExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.internalExecutor = exec
}

// snapshotTarget resolves the workspace root snapshots must be written into.
// It returns ok=false only when no SnapshotService was injected at all
// (snapshots disabled); an injected service with an unresolvable root is an
// error, not a silent skip.
func (s *AIPlanService) snapshotTarget() (snap *SnapshotService, root string, ok bool, err error) {
	s.mu.RLock()
	snap = s.snapshotSvc
	ctx := s.wsCtx
	fallback := s.workspaceRoot
	s.mu.RUnlock()

	if snap == nil {
		return nil, "", false, nil
	}
	if ctx != nil {
		root, err = ctx.RequireRoot()
		if err != nil {
			return nil, "", false, err
		}
		return snap, root, true, nil
	}
	if fallback == "" {
		return nil, "", false, fmt.Errorf("plan snapshot requires a workspace root: %w", ErrNotAllowed)
	}
	return snap, fallback, true, nil
}

// tryCreateSnapshot 在执行前创建快照（Step 3）。
//
// GOAL-P0-02: this is fail-closed. Previously an empty workspace root made this
// return silently, so a Plan step would keep writing to disk with no recovery
// point. Now the caller must abort when this returns an error.
func (s *AIPlanService) tryCreateSnapshot(reason SnapshotReason) error {
	snap, root, ok, err := s.snapshotTarget()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if _, err := snap.CreateSnapshot(root, string(reason)); err != nil {
		return fmt.Errorf("create plan snapshot: %w", err)
	}
	return nil
}

// CreatePlan 创建新 Plan（Step 2）。
// Plan 模式下 AI 只能用 plan 工具生成步骤（Step 3）。
func (s *AIPlanService) CreatePlan(id, goal string, steps []PlanStep) (*Plan, error) {
	if id == "" {
		return nil, fmt.Errorf("plan id required: %w", ErrInvalidInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.plans[id]; exists {
		return nil, fmt.Errorf("plan %q: %w", id, ErrAlreadyExists)
	}
	if s.lifecycle != nil {
		if _, err := s.lifecycle.Begin(agentcore.SessionPlan, id); err != nil {
			return nil, err
		}
		if _, err := s.lifecycle.Checkpoint(agentcore.SessionPlan, id, "plan-created", map[string]interface{}{
			"phase": "plan-created",
			"steps": len(steps),
		}); err != nil {
			_ = s.lifecycle.Fail(agentcore.SessionPlan, id, err)
			return nil, err
		}
	}
	// 初始化步骤状态。
	for i := range steps {
		if steps[i].Status == "" {
			steps[i].Status = PlanStepPending
		}
	}
	p := &Plan{
		ID:        id,
		Goal:      goal,
		Steps:     steps,
		Status:    PlanStatusPending,
		CreatedAt: time.Now(),
	}
	s.plans[id] = p
	s.active = p
	return p, nil
}

// GetPlan 查询 Plan。
func (s *AIPlanService) GetPlan(id string) (*Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.plans[id]
	if !ok {
		return nil, fmt.Errorf("plan %q: %w", id, ErrNotFound)
	}
	return p, nil
}

// GetActivePlan 返回当前活动 Plan（Step 8：Plan 与 Goal 互斥）。
func (s *AIPlanService) GetActivePlan() *Plan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

// ---------------------------------------------------------------------------
// 审批（Step 4）
// ---------------------------------------------------------------------------

// ApproveStep 批准单步（Step 4）。
func (s *AIPlanService) ApproveStep(planID string, stepIdx int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[planID]
	if !ok {
		return fmt.Errorf("plan %q: %w", planID, ErrNotFound)
	}
	if stepIdx < 0 || stepIdx >= len(p.Steps) {
		return fmt.Errorf("step index out of range: %w", ErrInvalidInput)
	}
	if p.Steps[stepIdx].Status != PlanStepPending {
		return fmt.Errorf("step %d not pending (status=%s): %w", stepIdx, p.Steps[stepIdx].Status, ErrNotAllowed)
	}
	p.Steps[stepIdx].Status = PlanStepApproved
	now := time.Now()
	if p.ApprovedAt == nil {
		p.ApprovedAt = &now
	}
	return nil
}

// ApproveAll 批准所有 pending 步骤（Step 4：全部批准）。
func (s *AIPlanService) ApproveAll(planID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[planID]
	if !ok {
		return fmt.Errorf("plan %q: %w", planID, ErrNotFound)
	}
	for i := range p.Steps {
		if p.Steps[i].Status == PlanStepPending {
			p.Steps[i].Status = PlanStepApproved
		}
	}
	now := time.Now()
	p.ApprovedAt = &now
	return nil
}

// RejectAll 拒绝所有 pending 步骤（Step 4：全部拒绝）。
func (s *AIPlanService) RejectAll(planID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[planID]
	if !ok {
		return fmt.Errorf("plan %q: %w", planID, ErrNotFound)
	}
	for i := range p.Steps {
		if p.Steps[i].Status == PlanStepPending {
			p.Steps[i].Status = PlanStepSkipped
		}
	}
	return nil
}

// EditStep 编辑步骤后批准（Step 4：编辑后批准）。
func (s *AIPlanService) EditStep(planID string, stepIdx int, newStep PlanStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[planID]
	if !ok {
		return fmt.Errorf("plan %q: %w", planID, ErrNotFound)
	}
	if stepIdx < 0 || stepIdx >= len(p.Steps) {
		return fmt.Errorf("step index out of range: %w", ErrInvalidInput)
	}
	if p.Steps[stepIdx].Status != PlanStepPending {
		return fmt.Errorf("can only edit pending step: %w", ErrNotAllowed)
	}
	newStep.Status = PlanStepApproved
	p.Steps[stepIdx] = newStep
	return nil
}

// ---------------------------------------------------------------------------
// 执行（Step 2 / Step 6 / Step 7）
// ---------------------------------------------------------------------------

// ExecuteStep 执行单个已批准步骤（Step 2）。
// toolExecutor 由调用方注入（实际调用 AgentService.CheckCommand + 执行）。
// G-SEC-02：每步 Tool 调用经 CheckCommand（Step 9）。
func (s *AIPlanService) ExecuteStep(planID string, stepIdx int, executor StepExecutor) error {
	s.mu.Lock()
	p, ok := s.plans[planID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("plan %q: %w", planID, ErrNotFound)
	}
	if stepIdx < 0 || stepIdx >= len(p.Steps) {
		s.mu.Unlock()
		return fmt.Errorf("step index out of range: %w", ErrInvalidInput)
	}
	step := &p.Steps[stepIdx]
	if step.Status != PlanStepApproved {
		s.mu.Unlock()
		return fmt.Errorf("step %d not approved (status=%s): %w", stepIdx, step.Status, ErrNotAllowed)
	}
	step.Status = PlanStepExecuting
	now := time.Now()
	step.StartedAt = &now
	p.Status = PlanStatusExecuting
	lifecycle := s.lifecycle
	s.mu.Unlock()
	operation := fmt.Sprintf("plan.step.%d", stepIdx)
	var usageReceipt agentcore.UsageReceipt
	failStep := func(cause error) error {
		finished := time.Now()
		s.mu.Lock()
		step.Status = PlanStepFailed
		step.Error = cause.Error()
		step.FinishedAt = &finished
		p.Status = PlanStatusPaused
		s.mu.Unlock()
		meterErr := s.completePlanExecution(usageReceipt, planID, operation, now, finished, cause)
		var lifecycleErr error
		if lifecycle != nil {
			lifecycleErr = lifecycle.Fail(agentcore.SessionPlan, planID, cause)
		}
		return errors.Join(cause, meterErr, lifecycleErr)
	}

	if lifecycle != nil {
		if err := s.checkpointPlanExecution(planID, stepIdx, "step-started"); err != nil {
			return failStep(err)
		}
		if err := lifecycle.Append(agentcore.SessionPlan, planID, agentcore.StreamEventInput{
			Kind: agentcore.StreamStatus, Data: "step-started",
		}); err != nil {
			return failStep(err)
		}
		var err error
		usageReceipt, err = s.beginPlanExecution(planID, operation, now)
		if err != nil {
			finished := time.Now()
			s.mu.Lock()
			step.Status = PlanStepFailed
			step.Error = err.Error()
			step.FinishedAt = &finished
			p.Status = PlanStatusPaused
			s.mu.Unlock()
			return errors.Join(err, lifecycle.Fail(agentcore.SessionPlan, planID, err))
		}
	}

	// 前端调用时 executor 为 nil，回退到内部注入的实现。
	if executor == nil {
		s.mu.RLock()
		executor = s.internalExecutor
		s.mu.RUnlock()
	}
	if executor == nil {
		return failStep(fmt.Errorf("executor required (inject via SetInternalExecutor): %w", ErrInvalidInput))
	}

	// Step 3 / GOAL-P0-02: create the pre-step snapshot before any write. A
	// snapshot failure aborts the step instead of executing without a recovery
	// point, so a mis-wired workspace root can no longer cause silent data loss.
	if err := s.tryCreateSnapshot(SnapshotReasonPlanStep); err != nil {
		return failStep(err)
	}

	// 执行（不持锁，允许长时间运行）。
	result, err := executor.Execute(planID, stepIdx, step.Tool, step.Args)

	finished := time.Now()
	if err != nil {
		return failStep(err)
	}
	if meterErr := s.completePlanExecution(usageReceipt, planID, operation, now, finished, nil); meterErr != nil {
		return s.failPlanMetering(planID, stepIdx, meterErr)
	}

	s.mu.Lock()
	step.FinishedAt = &finished
	step.Status = PlanStepCompleted
	step.Result = result
	// 检查是否全部完成。
	allDone := true
	for i := range p.Steps {
		if p.Steps[i].Status != PlanStepCompleted && p.Steps[i].Status != PlanStepSkipped {
			allDone = false
			break
		}
	}
	if allDone {
		p.Status = PlanStatusCompleted
		p.FinishedAt = &finished
	}
	s.mu.Unlock()

	if lifecycle != nil {
		if allDone {
			if err := lifecycle.Complete(agentcore.SessionPlan, planID); err != nil {
				return err
			}
		} else if err := s.checkpointPlanExecution(planID, stepIdx, "step-completed"); err != nil {
			return err
		}
	}
	return nil
}

// StepExecutor 是步骤执行器接口（Step 2）。
// 由 AgentService 实现（调用 CheckCommand + 实际工具执行）。
type StepExecutor interface {
	Execute(planID string, stepIndex int, tool, args string) (result string, err error)
}

// SkipStep 跳过步骤（Step 7）。
// failPlanMetering keeps the domain state honest when a successful step could
// not be durably recorded. A completed step with a missing ledger row would
// make retries and cost summaries disagree about what actually ran.
func (s *AIPlanService) failPlanMetering(planID string, stepIdx int, cause error) error {
	if cause == nil {
		cause = fmt.Errorf("plan metering failed: %w", ErrNotAllowed)
	}
	s.mu.Lock()
	p, ok := s.plans[planID]
	if ok && stepIdx >= 0 && stepIdx < len(p.Steps) {
		finished := time.Now()
		p.Steps[stepIdx].Status = PlanStepFailed
		p.Steps[stepIdx].Error = cause.Error()
		p.Steps[stepIdx].FinishedAt = &finished
		p.Status = PlanStatusPaused
	}
	lifecycle := s.lifecycle
	s.mu.Unlock()
	var lifecycleErr error
	if lifecycle != nil {
		lifecycleErr = lifecycle.Fail(agentcore.SessionPlan, planID, cause)
	}
	return errors.Join(cause, lifecycleErr)
}

func (s *AIPlanService) SkipStep(planID string, stepIdx int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[planID]
	if !ok {
		return fmt.Errorf("plan %q: %w", planID, ErrNotFound)
	}
	if stepIdx < 0 || stepIdx >= len(p.Steps) {
		return fmt.Errorf("step index out of range: %w", ErrInvalidInput)
	}
	p.Steps[stepIdx].Status = PlanStepSkipped
	// 如果因失败暂停，尝试恢复。
	if p.Status == PlanStatusPaused {
		if s.lifecycle != nil {
			if err := s.lifecycle.ResumeLatest(agentcore.SessionPlan, planID); err != nil {
				return err
			}
		}
		p.Status = PlanStatusExecuting
	}
	return nil
}

// Replan 重新规划（Step 7）。
// 保留已完成步骤，替换剩余步骤。
func (s *AIPlanService) Replan(planID string, newSteps []PlanStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[planID]
	if !ok {
		return fmt.Errorf("plan %q: %w", planID, ErrNotFound)
	}
	if s.lifecycle != nil && p.Status == PlanStatusPaused {
		if err := s.lifecycle.ResumeLatest(agentcore.SessionPlan, planID); err != nil {
			return err
		}
	}
	// 保留已完成/跳过的步骤。
	var remaining []PlanStep
	for _, step := range p.Steps {
		if step.Status == PlanStepCompleted || step.Status == PlanStepSkipped {
			remaining = append(remaining, step)
		}
	}
	// 初始化新步骤状态。
	for i := range newSteps {
		newSteps[i].Status = PlanStepPending
	}
	p.Steps = append(remaining, newSteps...)
	p.Status = PlanStatusPending
	return nil
}

// AbortPlan 中止 Plan（Step 2）。
func (s *AIPlanService) AbortPlan(planID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[planID]
	if !ok {
		return fmt.Errorf("plan %q: %w", planID, ErrNotFound)
	}
	p.Status = PlanStatusAborted
	now := time.Now()
	p.FinishedAt = &now
	if s.active == p {
		s.active = nil
	}
	if s.lifecycle != nil {
		return s.lifecycle.Abort(agentcore.SessionPlan, planID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 回放（Step 6）
// ---------------------------------------------------------------------------

// GetStepResult 返回步骤执行详情用于回放（Step 6）。
func (s *AIPlanService) GetStepResult(planID string, stepIdx int) (PlanStep, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.plans[planID]
	if !ok {
		return PlanStep{}, fmt.Errorf("plan %q: %w", planID, ErrNotFound)
	}
	if stepIdx < 0 || stepIdx >= len(p.Steps) {
		return PlanStep{}, fmt.Errorf("step index out of range: %w", ErrInvalidInput)
	}
	return p.Steps[stepIdx], nil
}

// ListPlans 返回所有 Plan。
func (s *AIPlanService) ListPlans() []*Plan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Plan, 0, len(s.plans))
	for _, p := range s.plans {
		out = append(out, p)
	}
	return out
}
