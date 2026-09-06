package main

// bootstrap_services.go — Koyori IDE 后端服务装配层。
//
// prompt-5 Task I: service registration extracted from main.go so the entry
// point stays focused on lifecycle (lock → wire → window → run).
//
// 这里的职责是「把 47 个后端服务用构造器注入组装成一颗可运行的对象图」：
//   - 每个服务通过 appBundle 暴露给入口与测试
//   - 服务之间只依赖注入，不互相 new，保证可测试性
//   - 平台相关的差异由 services 包内部的构建标签（build tags）隔离

import (
	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// appBundle holds every backend service constructed during bootstrap.
// Fields are exported only for tests that need to inspect wiring.
type appBundle struct {
	// GOAL-P0-02: WorkspaceCtx is the single shared workspace identity. Every
	// service that used to capture a bootstrap-time root string (AIPlan, AIGoal,
	// Diff, and the default executors) reads this instead, so opening or
	// switching a project is visible to all of them atomically.
	WorkspaceCtx      *services.WorkspaceContext
	AgentLifecycle    *services.AgentLifecycle
	File              *services.FileService
	Project           *services.ProjectService
	Settings          *services.SettingsService
	HTTPClient        *services.HTTPClientService
	Database          *services.DatabaseService
	Window            *services.WindowService
	Terminal          *services.TerminalService
	AI                *services.AIService
	Git               *services.GitService
	GitWorktree       *services.GitWorktreeService
	GitRebase         *services.GitRebaseService
	PullRequest       *services.PullRequestService
	Search            *services.SearchService
	Conversation      *services.ConversationService
	Task              *services.TaskService
	Workflow          *services.WorkflowService
	Agent             *services.AgentService
	Rules             *services.RulesService
	LogLevel          *services.LogLevelService
	Plugin            *services.PluginService
	Profile           *services.ProfileService
	Layout            *services.LayoutService
	LSP               *services.LSPService
	Toolchain         *services.ToolchainService
	LanguagePacks     *services.LanguagePackService
	Marketplace       *services.MarketplaceService
	ExtensionSecurity *services.ExtensionSecurityService
	// F-3 (prompt-2.md): ActivationService 管理扩展 activationEvents 触发。
	Activation   *services.ActivationService
	MCP          *services.MCPService
	Skills       *services.SkillsService
	ComputerUse  *services.ComputerUseService
	IM           *services.IMService
	Persona      *services.PersonaService
	AIPlan       *services.AIPlanService
	AIGoal       *services.AIGoalService
	AIPermission *services.AIPermissionService
	Diff         *services.DiffService
	Snapshot     *services.SnapshotService
	Preset       *services.PresetService
	Debug        *services.DebugService
	Coverage     *services.CoverageService
	Eslint       *services.EslintService
	SymbolIndex  *services.SymbolIndexService
	InstanceLock *services.InstanceLock
	Update       *services.UpdateService
	Crash        *services.CrashService
	PProf        *services.PProfService
	// F-9 (prompt-2.md 第 537-586 行): RemoteService 管理 SSH 远程开发会话。
	// 在 main.go 实例化后注入；前端通过 Wails bindings 调用 Connect/Disconnect/
	// IsConnected/GetFileSystem/ExecuteCommand/ListConnections。
	Remote *services.RemoteService
	// GOAL-P0-03: RecoveryService 持久化未保存的编辑器缓冲，使异常退出后可恢复。
	// 与 Crash（只写 panic 报告）分层：崩溃日志不是内容备份。
	Recovery *services.RecoveryService
}

// wailsServices returns the application.Service slice registered with Wails.
func (b *appBundle) wailsServices() []application.Service {
	return []application.Service{
		application.NewService(b.File),
		application.NewService(b.Project),
		application.NewService(b.Settings),
		application.NewService(b.HTTPClient),
		application.NewService(b.Database),
		application.NewService(b.Window),
		application.NewService(b.Terminal),
		application.NewService(b.AI),
		application.NewService(b.Git),
		application.NewService(b.GitWorktree),
		application.NewService(b.GitRebase),
		application.NewService(b.PullRequest),
		application.NewService(b.Search),
		application.NewService(b.Conversation),
		application.NewService(b.Task),
		application.NewService(b.Workflow),
		application.NewService(b.Agent),
		application.NewService(b.Rules),
		application.NewService(b.LogLevel),
		application.NewService(b.Plugin),
		application.NewService(b.Profile),
		application.NewService(b.Layout),
		application.NewService(b.LSP),
		application.NewService(b.Toolchain),
		application.NewService(b.LanguagePacks),
		application.NewService(b.Marketplace),
		application.NewService(b.ExtensionSecurity),
		application.NewService(b.Activation),
		application.NewService(b.MCP),
		application.NewService(b.Skills),
		application.NewService(b.ComputerUse),
		application.NewService(b.IM),
		application.NewService(b.Persona),
		application.NewService(b.AIPlan),
		application.NewService(b.AIGoal),
		application.NewService(b.AIPermission),
		application.NewService(b.Diff),
		application.NewService(b.Snapshot),
		application.NewService(b.Debug),
		application.NewService(b.Coverage),
		application.NewService(b.Eslint),
		application.NewService(b.SymbolIndex),
		// Priority 7: PProfService — Go pprof CPU/heap/goroutine 采样与分析。
		application.NewService(b.PProf),
		// 优先级 10: 自动更新 + 崩溃报告。
		application.NewService(b.Update),
		application.NewService(b.Crash),
		// F-9: SSH 远程开发（连接管理 + 远程文件系统 + 远程命令执行）。
		application.NewService(b.Remote),
		// GOAL-P0-03: hot exit / 脏缓冲崩溃恢复。
		application.NewService(b.Recovery),
	}
}
