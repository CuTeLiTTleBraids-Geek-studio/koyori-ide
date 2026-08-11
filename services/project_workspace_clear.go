package services

import "errors"

// clearWorkspaceRoots removes cached roots after the active project is
// removed. Every mutation has an inverse; a failure restores all adapters and
// the exact WorkspaceContext generation instead of leaving a partial clear.
func (p *ProjectService) clearWorkspaceRoots() error {
	actions := make([]workspaceClearAction, 0, 12)
	if p.fileService != nil {
		service := p.fileService
		previous := service.WorkspaceRoots()
		actions = append(actions, workspaceClearAction{
			label:   "file",
			clear:   func() error { return service.setWorkspaceRoots(nil) },
			restore: func() error { return service.setWorkspaceRoots(previous) },
		})
	}
	if p.terminalService != nil {
		service := p.terminalService
		service.mu.Lock()
		previous := service.rootDir
		service.mu.Unlock()
		actions = append(actions, workspaceClearAction{
			label:   "terminal",
			clear:   func() error { return service.setWorkspaceRoot("") },
			restore: func() error { return service.setWorkspaceRoot(previous) },
		})
	}
	if p.agentService != nil {
		service := p.agentService
		previous := service.currentWorkspaceRoot()
		actions = append(actions, workspaceClearAction{
			label:   "agent",
			clear:   func() error { return service.restoreWorkspaceRoot("") },
			restore: func() error { return service.restoreWorkspaceRoot(previous) },
		})
	}
	if p.gitService != nil {
		service := p.gitService
		service.mu.RLock()
		previous := service.workspaceRoot
		service.mu.RUnlock()
		actions = append(actions, workspaceClearAction{
			label:   "git",
			clear:   func() error { return service.setWorkspaceRoot("") },
			restore: func() error { return service.setWorkspaceRoot(previous) },
		})
	}
	if p.searchService != nil {
		service := p.searchService
		previous := service.WorkspaceRoots()
		actions = append(actions, workspaceClearAction{
			label:   "search",
			clear:   func() error { return service.setWorkspaceRoots(nil) },
			restore: func() error { return service.setWorkspaceRoots(previous) },
		})
	}
	if p.toolchainService != nil {
		service := p.toolchainService
		service.mu.Lock()
		previous := service.workspaceRoot
		service.mu.Unlock()
		actions = append(actions, workspaceClearAction{
			label:   "toolchain",
			clear:   func() error { return service.setWorkspaceRoot("") },
			restore: func() error { return service.setWorkspaceRoot(previous) },
		})
	}
	if p.coverageService != nil {
		service := p.coverageService
		service.mu.RLock()
		previous := service.workspaceRoot
		service.mu.RUnlock()
		actions = append(actions, workspaceClearAction{
			label:   "coverage",
			clear:   func() error { return service.setWorkspaceRoot("") },
			restore: func() error { return service.setWorkspaceRoot(previous) },
		})
	}
	if p.eslintService != nil {
		service := p.eslintService
		service.mu.Lock()
		previous := service.workspaceRoot
		service.mu.Unlock()
		actions = append(actions, workspaceClearAction{
			label:   "eslint",
			clear:   func() error { service.setWorkspaceRoot(""); return nil },
			restore: func() error { service.setWorkspaceRoot(previous); return nil },
		})
	}
	if p.mcpService != nil {
		previous := p.currentMCPWorkspaceRoot()
		actions = append(actions, workspaceClearAction{
			label:   "mcp",
			clear:   func() error { return p.restoreMCPWorkspaceRoot("") },
			restore: func() error { return p.restoreMCPWorkspaceRoot(previous) },
		})
	}
	if p.aiService != nil {
		service := p.aiService
		service.mu.RLock()
		previous := service.projectRoot
		service.mu.RUnlock()
		actions = append(actions, workspaceClearAction{
			label:   "ai",
			clear:   func() error { service.setProjectRoot(""); return nil },
			restore: func() error { service.setProjectRoot(previous); return nil },
		})
	}
	if p.lspService != nil {
		service := p.lspService
		previous := service.WorkspaceRoots()
		actions = append(actions, workspaceClearAction{
			label:   "lsp",
			clear:   func() error { service.setWorkspaceRoot(""); return nil },
			restore: func() error { service.setWorkspaceRoots(previous); return nil },
		})
	}
	if p.symbolIndexService != nil {
		service := p.symbolIndexService
		previous := service.WorkspaceRoots()
		actions = append(actions, workspaceClearAction{
			label:   "symbol index",
			clear:   func() error { return service.setWorkspaceRoots(nil) },
			restore: func() error { return service.setWorkspaceRoots(previous) },
		})
	}
	if p.wsCtx != nil {
		context := p.wsCtx
		previous := context.State()
		actions = append(actions, workspaceClearAction{
			label:   "workspace context",
			clear:   func() error { context.Clear(); return nil },
			restore: func() error { context.restoreSnapshot(previous); return nil },
		})
	}

	applied := make([]workspaceClearAction, 0, len(actions))
	for _, action := range actions {
		applied = append(applied, action)
		if err := action.clear(); err != nil {
			return errors.Join(
				&workspaceClearError{label: action.label, cause: err},
				rollbackWorkspaceClear(applied),
			)
		}
	}
	return nil
}

type workspaceClearAction struct {
	label   string
	clear   func() error
	restore func() error
}

func rollbackWorkspaceClear(actions []workspaceClearAction) error {
	var rollbackErrors []error
	for index := len(actions) - 1; index >= 0; index-- {
		action := actions[index]
		if err := action.restore(); err != nil {
			rollbackErrors = append(rollbackErrors, &workspaceClearError{
				label: "rollback " + action.label,
				cause: err,
			})
		}
	}
	return errors.Join(rollbackErrors...)
}

type workspaceClearError struct {
	label string
	cause error
}

func (e *workspaceClearError) Error() string {
	return e.label + " workspace root cleanup failed"
}

func (e *workspaceClearError) Unwrap() error {
	return e.cause
}
