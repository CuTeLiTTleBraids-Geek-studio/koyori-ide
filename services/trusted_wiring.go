package services

import (
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// These package functions are the trusted Go-only wiring boundary. Wails
// reflects methods on registered service instances; it cannot bind package
// functions, so dependency injection never becomes a renderer capability.

func WireFoundationServices(
	ai *AIService,
	preset *PresetService,
	profile *ProfileService,
	settings *SettingsService,
	layout *LayoutService,
	worktree *GitWorktreeService,
	rebase *GitRebaseService,
	workspace *WorkspaceContext,
) {
	ai.setPresetService(preset)
	profile.setOnSwitch(func(settingsPath string) {
		settings.setConfigPath(settingsPath)
		layout.setLayoutPath(filepath.Join(filepath.Dir(settingsPath), "layout.json"))
	})
	worktree.setWorkspaceContext(workspace)
	rebase.setWorkspaceContext(workspace)
}

func WireEditorServices(
	lsp *LSPService,
	file *FileService,
	marketplace *MarketplaceService,
	security *ExtensionSecurityService,
	activation *ActivationService,
) {
	lsp.setFileService(file)
	marketplace.setSecurityService(security)
	marketplace.setActivationService(activation)
}

func WireAgentServices(agent *AgentService, mcp *MCPService, skills *SkillsService) {
	agent.setMCPService(mcp)
	mcp.setOnToolsChanged(agent.InvalidateMCPCache)
	agent.setSkillsService(skills)
}

// SetMCPWorkspaceContext binds MCP transport authorization to the same
// workspace identity used by every other renderer-facing service.
func SetMCPWorkspaceContext(mcp *MCPService, workspace *WorkspaceContext) {
	if mcp != nil {
		mcp.setWorkspaceContext(workspace)
	}
}

func WireAnalysisServices(
	snapshot *SnapshotService,
	git *GitService,
	plan *AIPlanService,
	goal *AIGoalService,
	diff *DiffService,
	search *SearchService,
	recovery *RecoveryService,
	file *FileService,
	workspace *WorkspaceContext,
	stepExecutor StepExecutor,
	goalExecutor GoalExecutor,
	securityChecker SecurityChecker,
) {
	snapshot.setGitService(git)
	plan.setSnapshotService(snapshot, "")
	goal.setSnapshotService(snapshot, "")
	diff.setSnapshotService(snapshot, "")
	plan.setWorkspaceContext(workspace)
	goal.setWorkspaceContext(workspace)
	diff.setWorkspaceContext(workspace)
	diff.setFileService(file)
	search.setWorkspaceContext(workspace)
	file.setWorkspaceContext(workspace)
	plan.setInternalExecutor(stepExecutor)
	goal.setInternalExecutor(goalExecutor, securityChecker)
	recovery.setWorkspaceContext(workspace)
}

func WireWorkspaceServices(
	project *ProjectService,
	workspace *WorkspaceContext,
	git *GitService,
	search *SearchService,
	lsp *LSPService,
	toolchain *ToolchainService,
	symbolIndex *SymbolIndexService,
	coverage *CoverageService,
	eslint *EslintService,
	mcp *MCPService,
) {
	project.setWorkspaceContext(workspace)
	project.setGitService(git)
	project.setSearchService(search)
	project.setLSPService(lsp)
	project.setToolchainService(toolchain)
	project.setSymbolIndexService(symbolIndex)
	project.setCoverageService(coverage)
	project.setEslintService(eslint)
	project.setMCPService(mcp)
}

func WireRecoveryGuards(
	project *ProjectService,
	window *WindowService,
	recovery *RecoveryService,
) {
	project.setRecoveryService(recovery)
	window.setRecoveryService(recovery)
}

func AttachWindowService(
	windowService *WindowService,
	app *application.App,
	window *application.WebviewWindow,
) {
	windowService.setApp(app)
	windowService.setWindow(window)
}

func AttachApplicationServices(
	app *application.App,
	marketplace *MarketplaceService,
	ai *AIService,
	settings *SettingsService,
	permission *AIPermissionService,
	terminal *TerminalService,
	file *FileService,
	project *ProjectService,
) {
	marketplace.setApp(app)
	ai.setApp(app)
	ai.setSettingsService(settings)
	ai.setPermissionService(permission)
	permission.setSettingsService(settings)
	terminal.setApp(app)
	file.setApp(app)
	project.setApp(app)
}

func SetFileServiceWorkspaceRoot(file *FileService, root string) error {
	return file.setWorkspaceRoot(root)
}

func AttachFileService(file *FileService, app *application.App) {
	file.setApp(app)
}

func SetProjectWorkspaceContext(project *ProjectService, workspace *WorkspaceContext) {
	project.setWorkspaceContext(workspace)
}

func SetLSPFileService(lsp *LSPService, file *FileService) {
	lsp.setFileService(file)
}

func SetRecoveryWorkspaceContext(recovery *RecoveryService, workspace *WorkspaceContext) {
	recovery.setWorkspaceContext(workspace)
}
