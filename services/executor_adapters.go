package services

// Plan 11 — GoalExecutor / StepExecutor / SecurityChecker 的默认实现。
//
// 前端通过 Wails bindings 调用 RunGoal/ResumeGoal/ExecuteStep 时无法传递
// Go 接口实例（executor/checker 会被序列化为 nil）。这些适配器在 main.go
// 中注入到 AIGoalService / AIPlanService，当参数为 nil 时自动回退使用。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

// defaultSecurityChecker 用 AgentService.CheckCommand 实现 SecurityChecker。
type defaultSecurityChecker struct {
	agent *AgentService
	root  string
	// GOAL-P0-02: 共享 workspace context。非 nil 时优先于 root，
	// 使工作区切换立即生效，而不是停留在构造期的空字符串。
	wsCtx *WorkspaceContext
}

// resolveRoot 返回当前有效的工作区根。共享 context 优先于构造期字符串。
func (c *defaultSecurityChecker) resolveRoot() string {
	if c.wsCtx != nil {
		return c.wsCtx.Root()
	}
	return c.root
}

func (c *defaultSecurityChecker) CheckCommand(command string) CommandCheck {
	if c.agent == nil {
		return CommandCheck{Blocked: false}
	}
	return c.agent.CheckCommand(command)
}

// IsWorkspacePath 报告 path 是否在当前工作区内。
//
// GOAL-P0-02: fail-closed。此前空 root 返回 true（"未设置根目录时不阻断"），
// 这让 bootstrap 期注入的空 root 变成"允许任意路径"，Goal 的 CheckPathBoundary
// 因此完全失效。现在无法解析工作区根时拒绝，而不是退化为无限制访问。
func (c *defaultSecurityChecker) IsWorkspacePath(path string) bool {
	root := c.resolveRoot()
	if root == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return !IsPathOutsideRoot(root, abs)
}

// defaultStepExecutor 用 AgentService.ExecCommand 实现 StepExecutor。
type defaultStepExecutor struct {
	agent *AgentService
	root  string
	// GOAL-P0-02: 见 defaultSecurityChecker.wsCtx。
	wsCtx *WorkspaceContext
}

// resolveRoot 返回当前有效的工作区根。共享 context 优先于构造期字符串。
func (e *defaultStepExecutor) resolveRoot() string {
	if e.wsCtx != nil {
		return e.wsCtx.Root()
	}
	return e.root
}

// requireRoot 返回工作区根，无法解析时报错。
//
// GOAL-P0-02: fail-closed。此前 root 为空时仍会把 "" 作为 cwd 传给
// RequestCommandApproval，让 Plan 步骤在没有确定工作区的情况下执行命令。
func (e *defaultStepExecutor) requireRoot() (string, error) {
	root := e.resolveRoot()
	if root == "" {
		return "", fmt.Errorf("plan step execution requires an open workspace: %w", ErrNotAllowed)
	}
	return root, nil
}

type stepCommandArgs struct {
	Command string `json:"command"`
}

func parseStepCommandArgs(args string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(args))
	decoder.DisallowUnknownFields()

	var parsed stepCommandArgs
	if err := decoder.Decode(&parsed); err != nil {
		return "", fmt.Errorf("invalid command args: %v: %w", err, ErrInvalidInput)
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return "", fmt.Errorf("invalid command args: %v: %w", err, ErrInvalidInput)
	}

	if strings.TrimSpace(parsed.Command) == "" {
		return "", fmt.Errorf("invalid command args: command must be a non-empty string: %w", ErrInvalidInput)
	}
	return parsed.Command, nil
}

func (e *defaultStepExecutor) Execute(planID string, _ int, tool, args string) (string, error) {
	if strings.TrimSpace(tool) == "" {
		return "", fmt.Errorf("unsupported step tool %q: tool is required: %w", tool, ErrInvalidInput)
	}
	switch tool {
	case "command":
		// The default OS adapter intentionally handles only command steps.
	case "mcp", "skill":
		return "", fmt.Errorf("tool %q requires a dedicated executor: %w", tool, ErrInvalidInput)
	default:
		return "", fmt.Errorf("unsupported step tool %q: %w", tool, ErrInvalidInput)
	}

	if strings.TrimSpace(args) == "" {
		return "", fmt.Errorf("invalid command args: arguments are required: %w", ErrInvalidInput)
	}

	cmd, err := parseStepCommandArgs(args)
	if err != nil {
		return "", err
	}
	if e.agent == nil {
		return "", fmt.Errorf("agent service not injected: %w", ErrInvalidInput)
	}
	// Reject unsupported shell syntax and denylisted commands before the
	// workspace check. CheckCommand needs no root, and reporting "no workspace"
	// for `rm -rf /` would hide the real reason the command is refused.
	if check := e.agent.CheckCommand(cmd); check.Blocked {
		return "", fmt.Errorf("command blocked: %s: %w", check.BlockReason, ErrNotAllowed)
	}
	// GOAL-P0-02: resolve the live workspace root instead of the constructor
	// string, which stays empty for the whole process lifetime in bootstrap.
	root, err := e.requireRoot()
	if err != nil {
		return "", err
	}
	result, err := e.agent.executeInternalAgentTool(
		context.Background(), e.agent.executionSessionID(agentcore.SessionPlan, planID), "run",
		map[string]interface{}{"command": cmd, "cwd": root},
	)
	if err != nil {
		return "", err
	}
	return result.Observation, nil
}

// defaultGoalExecutor 用 AgentService 实现 GoalExecutor（简化版）。
type defaultGoalExecutor struct {
	agent *AgentService
	root  string
	// GOAL-P0-02: 见 defaultSecurityChecker.wsCtx。
	wsCtx *WorkspaceContext
}

// resolveRoot 返回当前有效的工作区根。共享 context 优先于构造期字符串。
func (e *defaultGoalExecutor) resolveRoot() string {
	if e.wsCtx != nil {
		return e.wsCtx.Root()
	}
	return e.root
}

// requireRoot 返回工作区根，无法解析时报错。
//
// GOAL-P0-02: fail-closed。空 root 会让 AgentService 退化到进程 cwd，
// 于是 Goal 在"没有打开工作区"时仍然能执行命令。
func (e *defaultGoalExecutor) requireRoot() (string, error) {
	root := e.resolveRoot()
	if root == "" {
		return "", fmt.Errorf("goal executor requires an open workspace: %w", ErrNotAllowed)
	}
	return root, nil
}

func (e *defaultGoalExecutor) Plan(goal *Goal) (string, error) {
	if _, err := e.requireRoot(); err != nil {
		return "", err
	}
	steps, reason, err := generateCatalogPlan(context.Background(), e.agent, goal.Description, goal.SuccessCriteria)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]interface{}{"steps": steps, "reason": reason})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (e *defaultGoalExecutor) Execute(goal *Goal, steps string) (GoalRoundResult, error) {
	if e.agent == nil {
		return GoalRoundResult{}, fmt.Errorf("agent service not injected: %w", ErrInvalidInput)
	}
	if _, err := e.requireRoot(); err != nil {
		return GoalRoundResult{Error: err.Error()}, err
	}
	parsed := parsePlanSteps(steps)
	if len(parsed) == 0 {
		return GoalRoundResult{Success: false, Note: "empty plan; no invented steps"}, nil
	}
	step := parsed[0]
	if goal != nil && goal.Iteration > 0 && goal.Iteration <= len(parsed) {
		step = parsed[goal.Iteration-1]
	}
	result, err := executePlanStep(e.agent, goal.ID, step.Tool, step.Args)
	if err != nil {
		return GoalRoundResult{Error: err.Error(), Note: err.Error()}, err
	}
	done, blocked, note := evaluateGoalOutcome(goal, result.Observation)
	if blocked {
		return GoalRoundResult{Success: false, Note: note, Error: note}, fmt.Errorf("%s: %w", note, ErrNotAllowed)
	}
	return GoalRoundResult{
		Success: done,
		Note:    note + "\n" + result.Observation,
	}, nil
}

func (e *defaultGoalExecutor) Evaluate(goal *Goal) (bool, error) {
	if goal == nil {
		return false, nil
	}
	goal.mu.Lock()
	defer goal.mu.Unlock()
	if len(goal.Checkpoints) == 0 {
		return false, nil
	}
	last := goal.Checkpoints[len(goal.Checkpoints)-1]
	done, _, _ := evaluateGoalOutcome(goal, last.Note)
	return done, nil
}

// NewDefaultSecurityChecker 创建默认 SecurityChecker。
//
// 仅在调用方能提供一个稳定的、已确定的工作区根时使用。bootstrap 阶段工作区尚未
// 打开，必须改用 NewDefaultSecurityCheckerWithContext（GOAL-P0-02）。
func NewDefaultSecurityChecker(agent *AgentService, workspaceRoot string) SecurityChecker {
	return &defaultSecurityChecker{agent: agent, root: workspaceRoot}
}

// NewDefaultSecurityCheckerWithContext 创建绑定共享 workspace context 的
// SecurityChecker（GOAL-P0-02）。工作区切换后立即生效。
func NewDefaultSecurityCheckerWithContext(agent *AgentService, ctx *WorkspaceContext) SecurityChecker {
	return &defaultSecurityChecker{agent: agent, wsCtx: ctx}
}

// NewDefaultStepExecutor 创建默认 StepExecutor。
//
// bootstrap 阶段请改用 NewDefaultStepExecutorWithContext（GOAL-P0-02）。
func NewDefaultStepExecutor(agent *AgentService, workspaceRoot string) StepExecutor {
	return &defaultStepExecutor{agent: agent, root: workspaceRoot}
}

// NewDefaultStepExecutorWithContext 创建绑定共享 workspace context 的
// StepExecutor（GOAL-P0-02）。
func NewDefaultStepExecutorWithContext(agent *AgentService, ctx *WorkspaceContext) StepExecutor {
	return &defaultStepExecutor{agent: agent, wsCtx: ctx}
}

// NewDefaultGoalExecutor 创建默认 GoalExecutor。
//
// bootstrap 阶段请改用 NewDefaultGoalExecutorWithContext（GOAL-P0-02）。
func NewDefaultGoalExecutor(agent *AgentService, workspaceRoot string) GoalExecutor {
	return &defaultGoalExecutor{agent: agent, root: workspaceRoot}
}

// NewDefaultGoalExecutorWithContext 创建绑定共享 workspace context 的
// GoalExecutor（GOAL-P0-02）。
func NewDefaultGoalExecutorWithContext(agent *AgentService, ctx *WorkspaceContext) GoalExecutor {
	return &defaultGoalExecutor{agent: agent, wsCtx: ctx}
}
