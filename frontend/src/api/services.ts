// Compatibility barrel for existing imports and Vitest module mocks.
// Koyori IDE 模块 · Services；交互服务：内置终端（TerminalService）、Git 集成（GitService）、AI 对话（AIService）、对话历史（ConversationService）、自治 Agent（AgentService）、任务（TaskService）、工作流（WorkflowService）、文件系统（FileService）、项目（ProjectService）、设置（SettingsService）、离线 LSP（LSPService）、调试（DebugService）、覆盖率（CoverageService）、ESLint 工具链（EslintService）、工具链（ToolchainService）、插件（PluginService）、插件市场（MarketplaceService）、Profile（ProfileService）、窗口（WindowService）、布局（LayoutService）、恢复（RecoveryService）、更新（UpdateService）、崩溃报告（CrashService）、远程开发（RemoteService）、数据库（DatabaseService）、性能分析（PProfService）、符号索引（SymbolIndexService）、Computer Use（ComputerUseService）。
// 喵，这是 Koyori IDE 的 Services 模块（前端实现）~
export { pullRequestService } from "./pullRequests";
export { unwrapNullable, requireNonNull } from "./boundary";
export {
  fileService, computerUseService, projectService, settingsService, windowService,
  terminalService, fromBindingSettings, toBindingSettings,
} from "./workspace";
export {
  aiService, gitService, searchService,
} from "./aiGitSearch";
export type { GitBlameLine, GitCommitGraphEntry } from "./aiGitSearch";
export {
  conversationService, taskService, workflowService, agentService, rulesService,
  logLevelService, pluginService, profileService, layoutService, toolchainService,
  fromBindingWorkflow, toBindingWorkflow,
} from "./automation";
export {
  lspService, fromBindingLSPCompletionItem, toBindingLSPCompletionItem,
  fromBindingSemanticToken, fromBindingInlayHint,
} from "./lsp";
export { debugService, eslintService, coverageService } from "./debugQuality";
export {
  marketplaceService, symbolIndexService, fromBindingExtensionManifest,
} from "./extensions";
export {
  databaseService, pprofService, updateService, crashService, remoteService,
  recoveryService,
} from "./platform";
