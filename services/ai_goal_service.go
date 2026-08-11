package services

// Plan 11 Task 10 — Goal 模式（目标驱动自治）。
//
// Goal 模式下 AI 自治连续执行：规划→执行→评估→调整，每轮创建 Checkpoint。
// Plan 与 Goal 互斥：Plan 用户驱动逐步，Goal AI 自治连续。
//
// 终止条件（Step 4）：
//   - 成功标准达成
//   - MaxIterations / MaxCost / MaxDuration 超限
//   - 用户手动中止
//   - 连续 3 次错误
//
// 安全（Step 8-10）：
//   - G-SEC-02：每轮工具调用经 CheckCommand（Step 9）
//   - G-SEC-03：Goal 视同不可信 workflow，创建需显式确认（Step 10）
//   - 安全边界：禁删工作区外文件/禁 git push --force/禁 RiskDangerous（Step 8）

import (
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Goal schema（Step 1）
// ---------------------------------------------------------------------------

// GoalStatus Goal 整体状态。
type GoalStatus string

const (
	GoalStatusCreated   GoalStatus = "created"   // 已创建，未运行
	GoalStatusRunning   GoalStatus = "running"   // 运行中
	GoalStatusPaused    GoalStatus = "paused"    // 用户暂停
	GoalStatusCompleted GoalStatus = "completed" // 成功标准达成
	GoalStatusAborted   GoalStatus = "aborted"   // 用户中止
	GoalStatusFailed    GoalStatus = "failed"    // 终止条件触发（超限/3次错误）
)

// Checkpoint 检查点（Step 5）。
type Checkpoint struct {
	Iteration int       `json:"iteration"`
	Snapshot  string    `json:"snapshot"` // 状态快照（git commit hash 或文件状态摘要）
	Cost      float64   `json:"cost"`     // 累计成本
	CreatedAt time.Time `json:"createdAt"`
	Note      string    `json:"note,omitempty"`
}

// Goal 完整目标（Step 1）。
type Goal struct {
	mu              sync.Mutex
	ID              string        `json:"id"`
	Description     string        `json:"description"`
	SuccessCriteria string        `json:"successCriteria"`
	MaxIterations   int           `json:"maxIterations"`
	MaxCost         float64       `json:"maxCost"`     // 预算上限（美元）
	MaxDuration     time.Duration `json:"maxDuration"` // 最长运行时间
	Checkpoints     []Checkpoint  `json:"checkpoints"`
	Status          GoalStatus    `json:"status"`
	Iteration       int           `json:"iteration"`
	TotalCost       float64       `json:"totalCost"`
	TotalTokens     int           `json:"totalTokens"`
	StartedAt       *time.Time    `json:"startedAt,omitempty"`
	FinishedAt      *time.Time    `json:"finishedAt,omitempty"`
	LastError       string        `json:"lastError,omitempty"`
	consecutiveErrs int           // 连续错误计数（Step 4）
}

// GoalView 是 Goal 的只读视图（避免外部直接操作锁）。
type GoalView struct {
	ID              string        `json:"id"`
	Description     string        `json:"description"`
	SuccessCriteria string        `json:"successCriteria"`
	MaxIterations   int           `json:"maxIterations"`
	MaxCost         float64       `json:"maxCost"`
	MaxDuration     time.Duration `json:"maxDuration"`
	Checkpoints     []Checkpoint  `json:"checkpoints"`
	Status          GoalStatus    `json:"status"`
	Iteration       int           `json:"iteration"`
	TotalCost       float64       `json:"totalCost"`
	TotalTokens     int           `json:"totalTokens"`
	StartedAt       *time.Time    `json:"startedAt,omitempty"`
	FinishedAt      *time.Time    `json:"finishedAt,omitempty"`
	LastError       string        `json:"lastError,omitempty"`
}

// View 返回只读视图（线程安全）。
func (g *Goal) View() GoalView {
	g.mu.Lock()
	defer g.mu.Unlock()
	return GoalView{
		ID:              g.ID,
		Description:     g.Description,
		SuccessCriteria: g.SuccessCriteria,
		MaxIterations:   g.MaxIterations,
		MaxCost:         g.MaxCost,
		MaxDuration:     g.MaxDuration,
		Checkpoints:     append([]Checkpoint(nil), g.Checkpoints...),
		Status:          g.Status,
		Iteration:       g.Iteration,
		TotalCost:       g.TotalCost,
		TotalTokens:     g.TotalTokens,
		StartedAt:       g.StartedAt,
		FinishedAt:      g.FinishedAt,
		LastError:       g.LastError,
	}
}

// ---------------------------------------------------------------------------
// GoalExecutor — 自治循环执行器接口（Step 3）
// ---------------------------------------------------------------------------

// GoalRoundResult 单轮执行结果。
type GoalRoundResult struct {
	Success  bool    `json:"success"`
	Cost     float64 `json:"cost"`     // 本轮成本
	Tokens   int     `json:"tokens"`   // 本轮 Token 数
	Snapshot string  `json:"snapshot"` // 状态快照
	Note     string  `json:"note"`     // 备注
	Error    string  `json:"error,omitempty"`
}

// GoalExecutor 由 AgentService 实现，驱动自治循环的单轮执行。
// G-SEC-02（Step 9）：每轮工具调用经 CheckCommand。
type GoalExecutor interface {
	// Plan 规划本轮步骤。
	Plan(goal *Goal) (steps string, err error)
	// Execute 执行本轮步骤，返回结果。
	Execute(goal *Goal, steps string) (GoalRoundResult, error)
	// Evaluate 评估是否达成成功标准。
	Evaluate(goal *Goal) (achieved bool, err error)
}

// PrototypeExecutor is implemented by GoalExecutor values that are scaffolding
// rather than a working autonomous coding loop (GOAL-P0-04A).
//
// The default executor plans with a fixed string, runs a fixed command
// regardless of that plan, and never evaluates success. Presenting that as
// autonomous coding is dishonest, so RunGoal refuses to drive a prototype
// executor unless the user explicitly opts in. Real executors simply do not
// implement this interface and are therefore unaffected.
type PrototypeExecutor interface {
	// IsPrototype reports that this executor cannot actually accomplish a goal.
	IsPrototype() bool
	// PrototypeLimitation describes, in one user-facing sentence, what the
	// executor actually does. Shown verbatim so the UI cannot overstate it.
	PrototypeLimitation() string
}

// SecurityChecker 安全边界检查（Step 8）。
type SecurityChecker interface {
	// CheckCommand 检查命令是否允许（G-SEC-02）。
	CheckCommand(command string) CommandCheck
	// IsWorkspacePath 检查路径是否在工作区内（Step 8：禁删工作区外文件）。
	IsWorkspacePath(path string) bool
}

// isPrototypeExecutor reports whether exec is scaffolding, and its limitation
// text. A non-prototype executor returns (false, "").
func isPrototypeExecutor(exec GoalExecutor) (bool, string) {
	proto, ok := exec.(PrototypeExecutor)
	if !ok || !proto.IsPrototype() {
		return false, ""
	}
	return true, proto.PrototypeLimitation()
}

// ---------------------------------------------------------------------------
// AIGoalService
// ---------------------------------------------------------------------------

// defaultCheckpointInterval 每 N 轮创建检查点（Step 5）。
const defaultCheckpointInterval = 3

// maxConsecutiveErrors 连续错误上限（Step 4）。
const maxConsecutiveErrors = 3

// AIGoalService 管理 Goal 模式的创建/运行/暂停/终止/检查点（Step 1-11）。
type AIGoalService struct {
	mu            sync.RWMutex
	goals         map[string]*Goal
	active        *Goal            // 当前活动 Goal（Plan 与 Goal 互斥，Step 8）
	snapshotSvc   *SnapshotService // Step 3: Goal 每检查点创建快照（可选）
	workspaceRoot string           // Step 3: 快照工作区根（legacy fallback，见 wsCtx）
	// GOAL-P0-02: 共享 workspace context。非 nil 时优先于 workspaceRoot，
	// 使工作区切换对本服务立即生效，而不是停留在构造期的空字符串。
	wsCtx *WorkspaceContext
	// 内部 executor/checker，当前端通过 Wails bindings 传入 nil 时回退使用。
	internalExecutor GoalExecutor
	internalChecker  SecurityChecker
	// GOAL-P0-04A: 是否允许驱动 prototype executor。默认 false。
	//
	// 默认关闭而不是默认开启，因为内置 executor 是固定脚手架（固定规划、固定命令、
	// 恒不达成）。默认运行它会向用户展示一串看似自治的迭代，而这些迭代在结构上
	// 不可能完成用户的目标。用户必须显式 opt-in 才能运行 prototype。
	prototypeExecutionEnabled bool
}

// NewAIGoalService 创建服务。
func NewAIGoalService() *AIGoalService {
	return &AIGoalService{
		goals: make(map[string]*Goal),
	}
}

// setSnapshotService 注入快照服务与工作区根（Step 3: Goal 每检查点创建快照）。
//
//wails:ignore
func (s *AIGoalService) setSnapshotService(snap *SnapshotService, workspaceRoot string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotSvc = snap
	s.workspaceRoot = workspaceRoot
}

// setWorkspaceContext 注入共享 workspace context（GOAL-P0-02）。
// 注入后本服务的快照根随工作区切换自动更新，不再依赖构造期字符串。
//
//wails:ignore
func (s *AIGoalService) setWorkspaceContext(ctx *WorkspaceContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wsCtx = ctx
}

// GoalExecutorCapability 描述当前生效的 executor 能做什么（GOAL-P0-04A）。
// 前端据此显示 prototype 标记与限制说明，而不是自行编造能力文案。
type GoalExecutorCapability struct {
	// Prototype 为 true 时，executor 是脚手架，结构上无法完成用户目标。
	Prototype bool `json:"prototype"`
	// Limitation 是 executor 自报的一句话限制说明，UI 原样显示。
	Limitation string `json:"limitation"`
	// AutoRunEnabled 报告用户是否已显式允许运行 prototype 自治循环。
	AutoRunEnabled bool `json:"autoRunEnabled"`
}

// GetExecutorCapability 返回当前 executor 的能力声明（GOAL-P0-04A）。
//
// 前端必须用它来决定是否展示"自治执行"能力，而不是假定 Goal 模式总能工作。
func (s *AIGoalService) GetExecutorCapability() GoalExecutorCapability {
	s.mu.RLock()
	exec := s.internalExecutor
	enabled := s.prototypeExecutionEnabled
	s.mu.RUnlock()

	if exec == nil {
		return GoalExecutorCapability{
			Prototype:      true,
			Limitation:     "No Goal executor is installed, so autonomous execution cannot run.",
			AutoRunEnabled: enabled,
		}
	}
	proto, limitation := isPrototypeExecutor(exec)
	return GoalExecutorCapability{
		Prototype:      proto,
		Limitation:     limitation,
		AutoRunEnabled: enabled,
	}
}

// SetPrototypeExecutionEnabled 允许或禁止运行 prototype 自治循环（GOAL-P0-04A）。
//
// 这不是权限提升：它只解锁一个自认无用的 executor，且该 executor 的每条命令
// 仍然要经过 AgentService 的后端审批与 capability token。默认 false，因为
// 默认运行固定脚手架会让用户误以为 Goal 能完成真实编码任务。
func (s *AIGoalService) SetPrototypeExecutionEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prototypeExecutionEnabled = enabled
}

// IsPrototypeExecutionEnabled 报告是否已允许运行 prototype 自治循环。
func (s *AIGoalService) IsPrototypeExecutionEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.prototypeExecutionEnabled
}

// snapshotTarget 解析快照应写入的工作区根。
// ok=false 且 err=nil 表示未注入 SnapshotService（快照功能关闭）；
// 已注入但根不可解析时返回 error，而不是静默跳过。
func (s *AIGoalService) snapshotTarget() (snap *SnapshotService, root string, ok bool, err error) {
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
		return nil, "", false, fmt.Errorf("goal snapshot requires a workspace root: %w", ErrNotAllowed)
	}
	return snap, fallback, true, nil
}

// setInternalExecutor 注入内部 executor/checker，当前端传入 nil 时回退使用。
//
//wails:ignore
func (s *AIGoalService) setInternalExecutor(exec GoalExecutor, checker SecurityChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.internalExecutor = exec
	s.internalChecker = checker
}

// tryCreateSnapshot 在检查点创建快照（Step 3: goal-checkpoint）。
//
// GOAL-P0-02: fail-closed。此前空 workspace root 会让本函数静默返回空串，
// Goal 于是在没有任何恢复点的情况下继续写盘。现在调用方必须在 error 时中止。
// 返回空 ID 且 err=nil 仅表示未注入 SnapshotService。
func (s *AIGoalService) tryCreateSnapshot() (string, error) {
	snap, root, ok, err := s.snapshotTarget()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	created, err := snap.CreateSnapshot(root, string(SnapshotReasonGoalCheckpoint))
	if err != nil {
		return "", fmt.Errorf("create goal snapshot: %w", err)
	}
	if created == nil {
		return "", fmt.Errorf("create goal snapshot: empty result: %w", ErrNotAllowed)
	}
	return created.ID, nil
}

// CreateGoal 创建新 Goal（Step 2）。
// G-SEC-03（Step 10）：Goal 视同不可信 workflow，需 explicitConfirmation=true。
func (s *AIGoalService) CreateGoal(id, description, successCriteria string, maxIterations int, maxCost float64, maxDuration time.Duration, explicitConfirmation bool) (*Goal, error) {
	if id == "" {
		return nil, fmt.Errorf("goal id required: %w", ErrInvalidInput)
	}
	if !explicitConfirmation {
		return nil, fmt.Errorf("goal creation requires explicit confirmation (G-SEC-03): %w", ErrNotAllowed)
	}
	if maxIterations <= 0 {
		maxIterations = 10 // 默认上限
	}
	if maxCost <= 0 {
		maxCost = 1.0 // 默认 $1
	}
	if maxDuration <= 0 {
		maxDuration = 30 * time.Minute // 默认 30 分钟
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.goals[id]; exists {
		return nil, fmt.Errorf("goal %q: %w", id, ErrAlreadyExists)
	}
	g := &Goal{
		ID:              id,
		Description:     description,
		SuccessCriteria: successCriteria,
		MaxIterations:   maxIterations,
		MaxCost:         maxCost,
		MaxDuration:     maxDuration,
		Status:          GoalStatusCreated,
	}
	s.goals[id] = g
	s.active = g
	return g, nil
}

// GetGoal 查询 Goal。
func (s *AIGoalService) GetGoal(id string) (GoalView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.goals[id]
	if !ok {
		return GoalView{}, fmt.Errorf("goal %q: %w", id, ErrNotFound)
	}
	return g.View(), nil
}

// GetActiveGoal 返回当前活动 Goal 视图（Plan 与 Goal 互斥）。
// 无活动 Goal 时返回 nil（与 GetActivePlan 一致）。
// 切勿返回零值 GoalView：Wails 会把它序列化成 {id:""}，前端会当成真实 Goal
// 再调用 GetCostReport("")，触发 `goal "": not found`。
func (s *AIGoalService) GetActiveGoal() *GoalView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active == nil {
		return nil
	}
	v := s.active.View()
	return &v
}

// ListGoals 返回所有 Goal 视图。
func (s *AIGoalService) ListGoals() []GoalView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]GoalView, 0, len(s.goals))
	for _, g := range s.goals {
		out = append(out, g.View())
	}
	return out
}

// ---------------------------------------------------------------------------
// 运行控制（Step 2-3）
// ---------------------------------------------------------------------------

// RunGoal 启动自治循环（Step 2-3）。
// 循环：规划→执行→评估→调整，每轮创建 Checkpoint。
// 终止条件（Step 4）：成功标准/MaxIterations/MaxCost/MaxDuration/用户手动/连续3次错误。
func (s *AIGoalService) RunGoal(id string, executor GoalExecutor, checker SecurityChecker) error {
	s.mu.RLock()
	g, ok := s.goals[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("goal %q: %w", id, ErrNotFound)
	}
	// 前端通过 Wails bindings 调用时 executor/checker 为 nil，回退到内部注入的实现。
	if executor == nil {
		s.mu.RLock()
		executor = s.internalExecutor
		s.mu.RUnlock()
	}
	if executor == nil {
		return fmt.Errorf("executor required (inject via SetInternalExecutor): %w", ErrInvalidInput)
	}

	// GOAL-P0-04A: refuse to drive a prototype executor unless the user opted in.
	//
	// The gate is here rather than in the loop because the goal's status must not
	// change: entering "running" and then failing would render as "the agent tried
	// and could not do it", which is a lie about capability. The goal stays in its
	// current status and the caller gets a distinguishable sentinel error so the UI
	// can explain the boundary instead of reporting a run.
	if proto, limitation := isPrototypeExecutor(executor); proto {
		s.mu.RLock()
		allowed := s.prototypeExecutionEnabled
		s.mu.RUnlock()
		if !allowed {
			return fmt.Errorf("%s: %w", limitation, ErrGoalPrototypeDisabled)
		}
	}

	g.mu.Lock()
	if g.Status != GoalStatusCreated && g.Status != GoalStatusPaused {
		g.mu.Unlock()
		return fmt.Errorf("goal %q cannot run (status=%s): %w", id, g.Status, ErrNotAllowed)
	}
	now := time.Now()
	if g.StartedAt == nil {
		g.StartedAt = &now
	}
	g.Status = GoalStatusRunning
	g.mu.Unlock()

	// 自治循环（Step 3）。
	for {
		// 检查是否被暂停/中止（用户手动，Step 4）。
		g.mu.Lock()
		if g.Status != GoalStatusRunning {
			g.mu.Unlock()
			return nil
		}
		// 终止条件 1：MaxIterations（Step 4）。
		// BUG3: 不再返回错误 — status 已置为 Failed 且 LastError 记录原因，
		// 前端可通过 GoalView.LastError 获取。返回错误会被 Wails 包成
		// RuntimeError，让用户误以为是系统异常。预算耗尽是预期内停止，
		// 不是运行时错误。
		if g.Iteration >= g.MaxIterations {
			g.Status = GoalStatusFailed
			finished := time.Now()
			g.FinishedAt = &finished
			g.LastError = fmt.Sprintf("max iterations (%d) reached", g.MaxIterations)
			g.mu.Unlock()
			return nil
		}
		// 终止条件 2：MaxCost（Step 4/6）。
		if g.TotalCost >= g.MaxCost {
			g.Status = GoalStatusFailed
			finished := time.Now()
			g.FinishedAt = &finished
			g.LastError = fmt.Sprintf("max cost ($%.4f) reached", g.MaxCost)
			g.mu.Unlock()
			return nil
		}
		// 终止条件 3：MaxDuration（Step 4）。
		if g.StartedAt != nil && time.Since(*g.StartedAt) >= g.MaxDuration {
			g.Status = GoalStatusFailed
			finished := time.Now()
			g.FinishedAt = &finished
			g.LastError = fmt.Sprintf("max duration (%v) reached", g.MaxDuration)
			g.mu.Unlock()
			return nil
		}
		// 终止条件 4：连续 3 次错误（Step 4）。
		if g.consecutiveErrs >= maxConsecutiveErrors {
			g.Status = GoalStatusFailed
			finished := time.Now()
			g.FinishedAt = &finished
			g.LastError = fmt.Sprintf("consecutive errors (%d) reached", maxConsecutiveErrors)
			g.mu.Unlock()
			return nil
		}
		g.Iteration++
		g.mu.Unlock()

		// 规划（Step 3）。
		_, planErr := executor.Plan(g)
		if planErr != nil {
			g.mu.Lock()
			g.consecutiveErrs++
			g.LastError = fmt.Sprintf("plan error (iter %d): %v", g.Iteration, planErr)
			g.mu.Unlock()
			continue
		}

		// 执行（Step 3）。
		result, execErr := executor.Execute(g, "")
		if execErr != nil {
			g.mu.Lock()
			g.consecutiveErrs++
			g.LastError = fmt.Sprintf("execute error (iter %d): %v", g.Iteration, execErr)
			g.mu.Unlock()
			continue
		}

		// 成本累计（Step 6）。
		g.mu.Lock()
		g.TotalCost += result.Cost
		g.TotalTokens += result.Tokens
		g.consecutiveErrs = 0 // 成功执行重置错误计数
		g.mu.Unlock()

		// 检查点创建（Step 5）。
		if g.Iteration%defaultCheckpointInterval == 0 {
			s.createCheckpoint(g, result.Snapshot, result.Note)
		}

		// 评估（Step 3）。
		achieved, evalErr := executor.Evaluate(g)
		if evalErr != nil {
			g.mu.Lock()
			g.LastError = fmt.Sprintf("evaluate error (iter %d): %v", g.Iteration, evalErr)
			g.mu.Unlock()
			continue
		}
		// 终止条件 0：成功标准达成（Step 4）。
		if achieved {
			g.mu.Lock()
			g.Status = GoalStatusCompleted
			finished := time.Now()
			g.FinishedAt = &finished
			g.mu.Unlock()
			return nil
		}
	}
}

// PauseGoal 暂停运行（Step 2）。
func (s *AIGoalService) PauseGoal(id string) error {
	s.mu.RLock()
	g, ok := s.goals[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("goal %q: %w", id, ErrNotFound)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Status != GoalStatusRunning {
		return fmt.Errorf("goal %q not running (status=%s): %w", id, g.Status, ErrNotAllowed)
	}
	g.Status = GoalStatusPaused
	return nil
}

// ResumeGoal 恢复运行（Step 2）。
func (s *AIGoalService) ResumeGoal(id string, executor GoalExecutor, checker SecurityChecker) error {
	s.mu.RLock()
	g, ok := s.goals[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("goal %q: %w", id, ErrNotFound)
	}
	// 前端调用时 executor/checker 为 nil，回退到内部实现。
	if executor == nil {
		s.mu.RLock()
		executor = s.internalExecutor
		s.mu.RUnlock()
	}
	if executor == nil {
		return fmt.Errorf("executor required (inject via SetInternalExecutor): %w", ErrInvalidInput)
	}

	// GOAL-P0-04A: gate before the status mutation below. RunGoal gates too, but
	// this function flips the goal to "running" first, so relying only on RunGoal's
	// gate would leave the goal stuck in "running" with no loop driving it.
	if proto, limitation := isPrototypeExecutor(executor); proto {
		s.mu.RLock()
		allowed := s.prototypeExecutionEnabled
		s.mu.RUnlock()
		if !allowed {
			return fmt.Errorf("%s: %w", limitation, ErrGoalPrototypeDisabled)
		}
	}

	g.mu.Lock()
	if g.Status != GoalStatusPaused {
		g.mu.Unlock()
		return fmt.Errorf("goal %q not paused (status=%s): %w", id, g.Status, ErrNotAllowed)
	}
	g.Status = GoalStatusRunning
	g.mu.Unlock()
	// 重新进入自治循环。
	return s.RunGoal(id, executor, checker)
}

// AbortGoal 中止 Goal（Step 2）。
func (s *AIGoalService) AbortGoal(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.goals[id]
	if !ok {
		return fmt.Errorf("goal %q: %w", id, ErrNotFound)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Status = GoalStatusAborted
	now := time.Now()
	g.FinishedAt = &now
	if s.active == g {
		s.active = nil
	}
	return nil
}

// ---------------------------------------------------------------------------
// 检查点与回滚（Step 5）
// ---------------------------------------------------------------------------

// createCheckpoint 创建检查点（Step 5）。调用方需持有外层逻辑控制。
func (s *AIGoalService) createCheckpoint(g *Goal, snapshot, note string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cp := Checkpoint{
		Iteration: g.Iteration,
		Snapshot:  snapshot,
		Cost:      g.TotalCost,
		CreatedAt: time.Now(),
		Note:      note,
	}
	g.Checkpoints = append(g.Checkpoints, cp)
}

// CreateCheckpoint 手动创建检查点（Step 3/5）。
// 若注入了 SnapshotService，会先 best-effort 创建真实文件快照（goal-checkpoint），
// 并将快照 ID 作为 Snapshot 字段记录，供 RollbackToCheckpoint 回滚到文件状态。
// 传入的 snapshot 参数作为 fallback：未注入 SnapshotService 时使用该值。
func (s *AIGoalService) CreateCheckpoint(id, snapshot, note string) error {
	s.mu.RLock()
	g, ok := s.goals[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("goal %q: %w", id, ErrNotFound)
	}
	// GOAL-P0-02: a checkpoint whose snapshot could not be written to the correct
	// workspace root is not a checkpoint. Fail instead of recording a checkpoint
	// that RollbackToCheckpoint cannot actually restore files from.
	snapID, err := s.tryCreateSnapshot()
	if err != nil {
		return err
	}
	if snapID == "" {
		snapID = snapshot
	}
	s.createCheckpoint(g, snapID, note)
	return nil
}

// RollbackToCheckpoint 回滚到指定检查点（Step 5）。
// 回滚 = 将 Iteration/Cost/状态恢复到检查点时的值，后续检查点被丢弃。
func (s *AIGoalService) RollbackToCheckpoint(id string, checkpointIdx int) error {
	s.mu.RLock()
	g, ok := s.goals[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("goal %q: %w", id, ErrNotFound)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if checkpointIdx < 0 || checkpointIdx >= len(g.Checkpoints) {
		return fmt.Errorf("checkpoint index out of range: %w", ErrInvalidInput)
	}
	cp := g.Checkpoints[checkpointIdx]
	// 恢复到检查点状态。
	g.Iteration = cp.Iteration
	g.TotalCost = cp.Cost
	g.Status = GoalStatusPaused
	// 丢弃后续检查点。
	g.Checkpoints = g.Checkpoints[:checkpointIdx+1]
	g.LastError = ""
	g.consecutiveErrs = 0
	return nil
}

// ListCheckpoints 返回检查点列表。
func (s *AIGoalService) ListCheckpoints(id string) ([]Checkpoint, error) {
	s.mu.RLock()
	g, ok := s.goals[id]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("goal %q: %w", id, ErrNotFound)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Checkpoint(nil), g.Checkpoints...), nil
}

// ---------------------------------------------------------------------------
// 成本控制（Step 6）
// ---------------------------------------------------------------------------

// GetCostReport 返回成本报告。
type CostReport struct {
	TotalCost     float64 `json:"totalCost"`
	MaxCost       float64 `json:"maxCost"`
	RemainingCost float64 `json:"remainingCost"`
	TotalTokens   int     `json:"totalTokens"`
	Iteration     int     `json:"iteration"`
	MaxIterations int     `json:"maxIterations"`
}

// GetCostReport 返回实时成本报告（Step 6）。
func (s *AIGoalService) GetCostReport(id string) (CostReport, error) {
	s.mu.RLock()
	g, ok := s.goals[id]
	s.mu.RUnlock()
	if !ok {
		return CostReport{}, fmt.Errorf("goal %q: %w", id, ErrNotFound)
	}
	v := g.View()
	return CostReport{
		TotalCost:     v.TotalCost,
		MaxCost:       v.MaxCost,
		RemainingCost: v.MaxCost - v.TotalCost,
		TotalTokens:   v.TotalTokens,
		Iteration:     v.Iteration,
		MaxIterations: v.MaxIterations,
	}, nil
}

// ---------------------------------------------------------------------------
// 安全边界（Step 8）
// ---------------------------------------------------------------------------

// CheckSecurityBoundary 检查命令是否违反安全边界（Step 8）。
// 禁止：删除工作区外文件 / git push --force / RiskDangerous（除非显式授权）。
func (s *AIGoalService) CheckSecurityBoundary(command string, checker SecurityChecker, allowDangerous bool) error {
	if checker == nil {
		return nil
	}
	// G-SEC-02：每轮工具调用经 CheckCommand（Step 9）。
	cc := checker.CheckCommand(command)
	if cc.Blocked {
		return fmt.Errorf("command blocked by CheckCommand: %s: %w", cc.BlockReason, ErrNotAllowed)
	}
	// Step 8：禁 git push --force。
	if containsAny(command, []string{"git push --force", "git push -f", "git push --force-with-lease"}) {
		if !allowDangerous {
			return fmt.Errorf("git push --force forbidden in Goal mode (Step 8): %w", ErrNotAllowed)
		}
	}
	return nil
}

// CheckPathBoundary 检查路径是否在工作区内（Step 8：禁删工作区外文件）。
func (s *AIGoalService) CheckPathBoundary(path string, checker SecurityChecker) error {
	if checker == nil {
		return nil
	}
	if !checker.IsWorkspacePath(path) {
		return fmt.Errorf("path %q outside workspace (Step 8): %w", path, ErrNotAllowed)
	}
	return nil
}

// containsAny 检查 s 是否包含任意子串。
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
