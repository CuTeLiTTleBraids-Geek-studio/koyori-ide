package services

import "errors"

// Sentinel errors for consistent error checking across services (G-QUAL-01).
//
// Use errors.Is(err, services.ErrNotFound) to discriminate rather than
// string-matching on err.Error(). Wrap these with fmt.Errorf("...: %w", err)
// when returning so callers can still unwrap them.
var (
	ErrNotFound             = errors.New("not found")
	ErrAlreadyExists        = errors.New("already exists")
	ErrInvalidInput         = errors.New("invalid input")
	ErrNoCommitReceipt      = errors.New("no commit receipt")
	ErrInvalidCommitReceipt = errors.New("invalid commit receipt")
	ErrUnauthorized         = errors.New("unauthorized")
	ErrTimeout              = errors.New("timeout")
	// ErrNotAllowed 表示操作被安全策略拒绝（G-SEC-02 / G-SEC-12）。
	// 例如：Computer Use 未启用、坐标落入禁止区域、快捷键在黑名单中。
	ErrNotAllowed = errors.New("not allowed")
	// ErrGoalBudgetExhausted 表示 Goal 自治循环达到预算上限（迭代次数/成本/时长）。
	// 这是正常的终止条件，不应被视为安全策略拒绝（BUG2）。
	ErrGoalBudgetExhausted = errors.New("budget exhausted")
	// ErrPlatformUnsupported 表示当前平台不支持该操作。
	// 例如：Linux/macOS 上的 Computer Use 截图/鼠标键盘原生操作。
	ErrPlatformUnsupported = errors.New("platform unsupported")
	// ErrGoalPrototypeDisabled 表示 Goal 自治执行被拒绝，因为当前生效的 executor
	// 是 prototype 脚手架而非真实实现，且用户未显式开启 prototype 执行
	// （GOAL-P0-04A）。
	//
	// 这与 ErrGoalBudgetExhausted 和 ErrNotAllowed 都不同：预算耗尽是真实运行后的
	// 正常终止，安全拒绝是策略判定，而本错误表示"这个功能还不存在"。必须是独立
	// sentinel，否则前端无法区分"跑完了但没达成"和"根本没跑"。
	ErrGoalPrototypeDisabled = errors.New("goal autonomous execution is a disabled prototype")
	// ErrAgentBudgetExhausted 表示 Agent 的后端工具调用预算已耗尽（GOAL-P1-02）。
	//
	// 与 ErrGoalBudgetExhausted 区分：那个是 Goal 自治循环的预算，本错误是 Agent
	// 工具调用的 session 预算。两者都是"正常的达限停止"而非安全拒绝，所以不能
	// 复用 ErrNotAllowed —— 前端需要据此显示"开启新预算"而不是"操作被禁止"。
	ErrAgentBudgetExhausted = errors.New("agent tool budget exhausted")
)
