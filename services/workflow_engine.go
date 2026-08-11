package services

// Plan 11 Task 11 — 工作流执行引擎。
//
// 职责（Step 3-9）：
//   - Step 3: 并行执行（无 DependsOn 并行，有依赖串行）
//   - Step 4: AI 步骤（Type: "ai" 调用 AI 生成/审查/提交信息）
//   - Step 5: 条件 DSL（{{.StepName.Status}} == "success" / {{.FileName}} matches "*.go"）
//   - Step 7: 运行历史（步骤状态/耗时/输出/错误）
//   - Step 8: G-SEC-03（fileChange 需显式启用 + 防抖 + 最小间隔）
//   - Step 9: G-SEC-02（每步 command 经 CheckCommand）

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 运行历史（Step 7）
// ---------------------------------------------------------------------------

// WorkflowStepStatus 单步执行状态。
type WorkflowStepStatus string

const (
	WFStepPending WorkflowStepStatus = "pending"
	WFStepRunning WorkflowStepStatus = "running"
	WFStepSuccess WorkflowStepStatus = "success"
	WFStepFailed  WorkflowStepStatus = "failed"
	WFStepSkipped WorkflowStepStatus = "skipped"
)

// WorkflowStepResult 单步执行结果（Step 7: 运行历史）。
type WorkflowStepResult struct {
	Name      string             `json:"name"`
	Type      WorkflowStepType   `json:"type"`
	Status    WorkflowStepStatus `json:"status"`
	Output    string             `json:"output,omitempty"`
	Error     string             `json:"error,omitempty"`
	StartedAt *time.Time         `json:"startedAt,omitempty"`
	EndedAt   *time.Time         `json:"endedAt,omitempty"`
	Duration  time.Duration      `json:"duration"`
}

// WorkflowRunStatus 整体运行状态。
type WorkflowRunStatus string

const (
	WFRunRunning WorkflowRunStatus = "running"
	WFRunSuccess WorkflowRunStatus = "success"
	WFRunFailed  WorkflowRunStatus = "failed"
	WFRunAborted WorkflowRunStatus = "aborted"
)

// WorkflowRunHistory 单次运行历史记录（Step 7）。
type WorkflowRunHistory struct {
	ID        string               `json:"id"`
	Workflow  string               `json:"workflow"`
	Status    WorkflowRunStatus    `json:"status"`
	Steps     []WorkflowStepResult `json:"steps"`
	StartedAt time.Time            `json:"startedAt"`
	EndedAt   *time.Time           `json:"endedAt,omitempty"`
	Trigger   string               `json:"trigger,omitempty"` // 触发事件
}

// ---------------------------------------------------------------------------
// StepExecutor 接口（Step 3-4）
// ---------------------------------------------------------------------------

// WorkflowStepExecutor 由调用方注入，执行单个步骤。
// G-SEC-02（Step 9）：每步 command 经 CheckCommand。
type WorkflowStepExecutor interface {
	// ExecuteCommand 执行 command 类型步骤。
	ExecuteCommand(step WorkflowStep) (output string, err error)
	// ExecuteAI 执行 AI 类型步骤（Step 4: 生成/审查/提交信息）。
	ExecuteAI(step WorkflowStep) (output string, err error)
	// CheckCommand 检查命令安全性（G-SEC-02, Step 9）。
	CheckCommand(command string) CommandCheck
}

// ---------------------------------------------------------------------------
// 执行引擎（Step 3）
// ---------------------------------------------------------------------------

// WorkflowEngine 工作流执行引擎。
type WorkflowEngine struct {
	mu       sync.Mutex
	history  []WorkflowRunHistory
	executor WorkflowStepExecutor
	// Step 8: fileChange 防抖
	lastTriggerTime map[string]time.Time
	minInterval     time.Duration
	snapshotSvc     *SnapshotService // Step 3: 工作流每步骤前创建快照（可选）
	workspaceRoot   string           // Step 3: 快照工作区根
}

// NewWorkflowEngine 创建执行引擎。
// minInterval: fileChange 触发的最小间隔（Step 8 防抖）。
func NewWorkflowEngine(executor WorkflowStepExecutor, minInterval time.Duration) *WorkflowEngine {
	if minInterval <= 0 {
		minInterval = 5 * time.Second // 默认 5 秒防抖
	}
	return &WorkflowEngine{
		executor:        executor,
		lastTriggerTime: make(map[string]time.Time),
		minInterval:     minInterval,
	}
}

// SetSnapshotService 注入快照服务与工作区根（Step 3: 工作流每步骤前创建快照）。
func (e *WorkflowEngine) SetSnapshotService(snap *SnapshotService, workspaceRoot string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.snapshotSvc = snap
	e.workspaceRoot = workspaceRoot
}

// tryCreateSnapshot 在每步骤执行前 best-effort 创建快照（Step 3: workflow-step）。
func (e *WorkflowEngine) tryCreateSnapshot() {
	if e.snapshotSvc == nil || e.workspaceRoot == "" {
		return
	}
	_, _ = e.snapshotSvc.CreateSnapshot(e.workspaceRoot, string(SnapshotReasonWorkflowStep))
}

// ShouldTrigger 检查 fileChange 触发是否应该执行（Step 8: 防抖 + 最小间隔）。
func (e *WorkflowEngine) ShouldTrigger(workflowName string, enabled bool) bool {
	if !enabled {
		return false // G-SEC-03: fileChange 需显式启用
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	last, exists := e.lastTriggerTime[workflowName]
	if exists && time.Since(last) < e.minInterval {
		return false // 防抖：间隔太短
	}
	e.lastTriggerTime[workflowName] = time.Now()
	return true
}

// RunWorkflow 执行工作流（Step 3: 并行/串行执行）。
//
// 无 DependsOn 的步骤并行执行，有依赖的步骤等待依赖完成后串行执行。
// G-SEC-02（Step 9）：每步 command 经 CheckCommand。
func (e *WorkflowEngine) RunWorkflow(wf *WorkflowDef, trigger string) *WorkflowRunHistory {
	runID := fmt.Sprintf("%s-%d", wf.Name, time.Now().UnixNano())
	history := &WorkflowRunHistory{
		ID:        runID,
		Workflow:  wf.Name,
		Status:    WFRunRunning,
		StartedAt: time.Now(),
		Trigger:   trigger,
	}

	// 初始化步骤状态。
	results := make(map[string]*WorkflowStepResult)
	stepMap := make(map[string]*WorkflowStep)
	for i := range wf.Steps {
		s := &wf.Steps[i]
		results[s.Name] = &WorkflowStepResult{
			Name:   s.Name,
			Type:   s.Type,
			Status: WFStepPending,
		}
		if s.Type == "" {
			results[s.Name].Type = WorkflowStepCommand
		}
		stepMap[s.Name] = s
	}

	// Step 3: 并行执行无依赖步骤，串行执行有依赖步骤。
	// 使用拓扑层级：每层内的步骤并行执行。
	layers, layerErr := computeLayers(wf)
	aborted := layerErr != nil
	if layerErr != nil {
		for _, result := range results {
			result.Status = WFStepFailed
			result.Error = layerErr.Error()
		}
	}

	for _, layer := range layers {
		if aborted {
			// 标记剩余步骤为 skipped。
			for _, name := range layer {
				results[name].Status = WFStepSkipped
			}
			continue
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		var layerFailed bool

		for _, name := range layer {
			wg.Add(1)
			go func(stepName string) {
				defer wg.Done()
				step := stepMap[stepName]
				result := results[stepName]

				// Step 5: 条件 DSL 检查。
				if step.Condition != "" {
					if !e.EvaluateCondition(step.Condition, results) {
						result.Status = WFStepSkipped
						return
					}
				}

				// G-SEC-02（Step 9）：每步 command 经 CheckCommand。
				if e.executor != nil && step.Command != "" {
					cc := e.executor.CheckCommand(step.Command)
					if cc.Blocked {
						result.Status = WFStepFailed
						result.Error = fmt.Sprintf("blocked by CheckCommand: %s", cc.BlockReason)
						mu.Lock()
						layerFailed = true
						mu.Unlock()
						return
					}
				}

				// 执行步骤。
				// Step 3: 每步骤执行前 best-effort 创建快照（workflow-step）。
				e.tryCreateSnapshot()
				now := time.Now()
				result.Status = WFStepRunning
				result.StartedAt = &now

				var output string
				var err error
				switch step.Type {
				case WorkflowStepAI:
					if e.executor != nil {
						output, err = e.executor.ExecuteAI(*step)
					}
				default: // command / git / file / mcp / skill 都走 ExecuteCommand
					if e.executor != nil {
						output, err = e.executor.ExecuteCommand(*step)
					}
				}

				endTime := time.Now()
				result.EndedAt = &endTime
				result.Duration = endTime.Sub(*result.StartedAt)
				result.Output = output

				if err != nil {
					result.Status = WFStepFailed
					result.Error = err.Error()
					// Step 1: OnFailure 处理。
					onFailure := step.OnFailure
					if onFailure == "" {
						onFailure = OnFailureAbort
					}
					switch onFailure {
					case OnFailureContinue:
						// 继续执行，不标记 layerFailed。
					case OnFailureSkip:
						// 跳过依赖此步骤的步骤（在下一层处理）。
					default: // abort
						mu.Lock()
						layerFailed = true
						mu.Unlock()
					}
				} else {
					result.Status = WFStepSuccess
				}
			}(name)
		}
		wg.Wait()

		if layerFailed {
			aborted = true
		}
	}

	// 构建步骤结果列表（保持原顺序）。
	for _, s := range wf.Steps {
		history.Steps = append(history.Steps, *results[s.Name])
	}

	// 确定最终状态。
	endTime := time.Now()
	history.EndedAt = &endTime
	if aborted {
		history.Status = WFRunFailed
	} else {
		allSuccess := true
		for _, r := range history.Steps {
			if r.Status == WFStepFailed {
				allSuccess = false
				break
			}
		}
		if allSuccess {
			history.Status = WFRunSuccess
		} else {
			history.Status = WFRunFailed
		}
	}

	// 保存历史（Step 7）。
	e.mu.Lock()
	e.history = append(e.history, *history)
	e.mu.Unlock()

	return history
}

// GetHistory 返回运行历史（Step 7）。
func (e *WorkflowEngine) GetHistory() []WorkflowRunHistory {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]WorkflowRunHistory(nil), e.history...)
}

// ClearHistory 清除历史。
func (e *WorkflowEngine) ClearHistory() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = nil
}

// ---------------------------------------------------------------------------
// 条件 DSL（Step 5）
// ---------------------------------------------------------------------------

// EvaluateCondition 评估条件 DSL（Step 5）。
//
// 支持的语法：
//   - {{.StepName.Status}} == "success"
//   - {{.FileName}} matches "*.go"
//   - {{.StepName.Status}} != "failed"
//
// 简化实现：解析 == / != / matches 操作符。
func (e *WorkflowEngine) EvaluateCondition(condition string, results map[string]*WorkflowStepResult) bool {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return true
	}

	// 解析 {{.X}} == "Y" 格式。
	for _, op := range []string{"==", "!=", "matches"} {
		if idx := strings.Index(condition, op); idx > 0 {
			left := strings.TrimSpace(condition[:idx])
			right := strings.TrimSpace(condition[idx+len(op):])
			right = strings.Trim(right, "\"'")
			return evaluateExpr(left, op, right, results)
		}
	}
	// 无法解析的条件默认为 true。
	return true
}

// evaluateExpr 评估单个表达式。
func evaluateExpr(left, op, right string, results map[string]*WorkflowStepResult) bool {
	// 解析 {{.StepName.Status}} 格式。
	value := resolveTemplate(left, results)
	switch op {
	case "==":
		return value == right
	case "!=":
		return value != right
	case "matches":
		matched, _ := filepath.Match(right, value)
		return matched
	}
	return true
}

// resolveTemplate 解析 {{.X.Y}} 模板为实际值。
func resolveTemplate(tmpl string, results map[string]*WorkflowStepResult) string {
	tmpl = strings.TrimSpace(tmpl)
	if strings.HasPrefix(tmpl, "{{") && strings.HasSuffix(tmpl, "}}") {
		inner := strings.TrimSpace(tmpl[2 : len(tmpl)-2])
		if strings.HasPrefix(inner, ".") {
			inner = inner[1:]
		}
		// 解析 StepName.Status
		parts := strings.SplitN(inner, ".", 2)
		if len(parts) == 2 {
			stepName := parts[0]
			field := parts[1]
			if r, ok := results[stepName]; ok {
				switch strings.ToLower(field) {
				case "status":
					return string(r.Status)
				case "output":
					return r.Output
				case "error":
					return r.Error
				}
			}
		}
		return "" // 未知的模板
	}
	return tmpl // 非模板，直接返回
}

// ---------------------------------------------------------------------------
// 拓扑排序（Step 3: 并行/串行分层）
// ---------------------------------------------------------------------------

// computeLayers 计算拓扑层级，每层内的步骤可并行执行。
// 无 DependsOn 的步骤在第 0 层，有依赖的在更深层。
func computeLayers(wf *WorkflowDef) ([][]string, error) {
	// 构建依赖图。
	completed := make(map[string]bool)
	remaining := make(map[string]bool)
	for _, s := range wf.Steps {
		remaining[s.Name] = true
	}

	var layers [][]string
	for len(remaining) > 0 {
		var layer []string
		for name := range remaining {
			step := findStep(wf, name)
			if step == nil {
				continue
			}
			// 检查所有依赖是否已完成。
			depsComplete := true
			for _, dep := range step.DependsOn {
				if !completed[dep] {
					depsComplete = false
					break
				}
			}
			if depsComplete {
				layer = append(layer, name)
			}
		}
		if len(layer) == 0 {
			return nil, fmt.Errorf("workflow %q contains a dependency cycle", wf.Name)
		}
		for _, name := range layer {
			completed[name] = true
			delete(remaining, name)
		}
		layers = append(layers, layer)
	}
	return layers, nil
}

// findStep 在工作流中按名查找步骤。
func findStep(wf *WorkflowDef, name string) *WorkflowStep {
	for i := range wf.Steps {
		if wf.Steps[i].Name == name {
			return &wf.Steps[i]
		}
	}
	return nil
}
