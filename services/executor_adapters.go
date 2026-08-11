package services

// Plan 11 — GoalExecutor / StepExecutor / SecurityChecker 的默认实现。
//
// 前端通过 Wails bindings 调用 RunGoal/ResumeGoal/ExecuteStep 时无法传递
// Go 接口实例（executor/checker 会被序列化为 nil）。这些适配器在 main.go
// 中注入到 AIGoalService / AIPlanService，当参数为 nil 时自动回退使用。

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
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

func (e *defaultStepExecutor) Execute(tool, args string) (string, error) {
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
	// Plan approval is not command approval. The internal executor must still
	// obtain a backend-issued, single-use capability for the exact argv/cwd.
	token, err := e.agent.RequestCommandApproval(cmd, root)
	if err != nil {
		return "", err
	}
	result, err := e.agent.ExecuteApprovedCommand(cmd, root, token)
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
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
	// 简化实现：返回基于 goal.Description 的固定规划。
	// 完整实现应调用 AIService 让 AI 生成步骤。
	return fmt.Sprintf("Plan for goal %q: analyze requirements, execute steps, verify", goal.Description), nil
}

func (e *defaultGoalExecutor) Execute(goal *Goal, steps string) (GoalRoundResult, error) {
	if e.agent == nil {
		return GoalRoundResult{}, fmt.Errorf("agent service not injected: %w", ErrInvalidInput)
	}
	// 执行一个跨平台的无害命令来证明 executor 可用。
	// "echo" 在 Windows 上不是独立可执行文件（需要 cmd /c），
	// 使用 AgentService 内置的 echo 兼容处理或改用 go env 命令。
	cmd := "go env GOOS"
	root, err := e.requireRoot()
	if err != nil {
		return GoalRoundResult{Error: err.Error()}, err
	}
	token, err := e.agent.RequestCommandApproval(cmd, root)
	if err != nil {
		return GoalRoundResult{Error: err.Error()}, err
	}
	result, err := e.agent.ExecuteApprovedCommand(cmd, root, token)
	if err != nil {
		return GoalRoundResult{Error: err.Error()}, err
	}
	return GoalRoundResult{
		Success:  result.ExitCode == 0,
		Snapshot: "",
		Note:     fmt.Sprintf("executed step for goal %q", goal.ID),
	}, nil
}

func (e *defaultGoalExecutor) Evaluate(goal *Goal) (bool, error) {
	// 简化实现：不自动判定达成。完整实现应调用 AIService 评估。
	return false, nil
}

// IsPrototype implements PrototypeExecutor (GOAL-P0-04A).
//
// This executor is scaffolding, not an autonomous coding loop: Plan returns a
// fixed sentence built from the goal description, Execute ignores that plan and
// always runs `go env GOOS`, and Evaluate always reports "not achieved". Driving
// it automatically would show the user goal iterations that cannot possibly
// accomplish their goal, so AIGoalService refuses to run it unless the user
// explicitly opts into prototype execution.
func (e *defaultGoalExecutor) IsPrototype() bool { return true }

// PrototypeLimitation implements PrototypeExecutor (GOAL-P0-04A). The text is
// shown verbatim in the UI so the product surface cannot overstate what this
// executor does.
func (e *defaultGoalExecutor) PrototypeLimitation() string {
	return "The built-in Goal executor is a prototype: it plans with a fixed " +
		"sentence, always runs `go env GOOS` regardless of that plan, and never " +
		"evaluates your success criteria. It cannot complete real coding goals."
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
