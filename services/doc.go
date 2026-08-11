// Package services 承载 Koyori IDE 的全部后端能力。
//
// Koyori IDE（こより IDE）是一个离线优先的桌面 AI 代码编辑器，
// 面向 Go / TypeScript / JavaScript 开发者。本包是它的「大脑」——
// 46 个服务各自负责一个能力域，通过 Wails v3 生成绑定暴露给
// Vue 3 前端，服务之间用构造器注入解耦。
//
// 能力域一览：
//
//	编辑器与工作区    FileService · ProjectService · SearchService
//	                   DiffService · SnapshotService · RecoveryService
//	AI 与对话          AIService · ConversationService · AIPlanService
//	                   AIGoalService · AIPermissionService · PersonaService
//	自治 Agent         AgentService · TaskService · WorkflowService
//	语言支持           LSPService · ToolchainService · CoverageService
//	Git                GitService · GitWorktreeService · GitRebaseService
//	                   PullRequestService
//	终端               TerminalService
//	扩展与市场         PluginService · MarketplaceService
//	                   ExtensionService · ExtensionSecurityService
//	                   ActivationService · SkillsService · MCPService
//	远程与数据库       RemoteService · DatabaseService
//	窗口与布局         WindowService · LayoutService
//	安全               SecretsService(secrets_*.go) · ExtensionBlacklist
//	                   ai_urlsec · pathsec · atomic_write
//	系统               UpdateService · CrashService · IMService
//	                   PProfService · LogLevelService · RulesService
//	                   ComputerUseService · JsonSchemaResolver
//
// 安全设计（fail-closed）：
//   - 路径沙箱：pathsec.ValidatePathWithinRoot 双侧 EvalSymlinks
//   - 原子写：temp + rename + 0600（secrets 用 AES-256-GCM + 系统钥匙串）
//   - URL 校验：AI BaseURL 经 SSRF 与密钥外泄检测
//   - Agent 审批：所有命令强制人工审批，无 Safe 自动批准旁路
//   - 插件沙箱：Web Worker 隔离 + SHA-256 校验 + 权限分级
//
// 设计/验证记录请见 docs/prompts/prompt-*.md；代码注释中的
// `prompt-N Task X` 引用即指向这些记录。
package services
