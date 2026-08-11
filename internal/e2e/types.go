package e2e

import "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"

// ServiceSet is the minimal slice of backend services the automation endpoint
// needs. The root main package adapts its bootstrapServices to this struct.
// It lives in a tag-free file so both the production stub and the e2e-tagged
// server can reference it.
type ServiceSet struct {
	Project       *services.ProjectService
	File          *services.FileService
	Git           *services.GitService
	GitWorktree   *services.GitWorktreeService
	GitRebase     *services.GitRebaseService
	Settings      *services.SettingsService
	Terminal      *services.TerminalService
	Search        *services.SearchService
	AI            *services.AIService
	LSP           *services.LSPService
	LanguagePacks *services.LanguagePackService
	Toolchain     *services.ToolchainService
	Marketplace   *services.MarketplaceService
	Debug         *services.DebugService
	Diff          *services.DiffService
	Recovery      *services.RecoveryService
	Window        *services.WindowService
	HTTPClient    *services.HTTPClientService
	ExecJS        func(string)
	ExecAIJS      func(string)
	CloseWindow   func()
}
