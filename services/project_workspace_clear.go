package services

import (
	"errors"
	"fmt"
	"sync"
)

// clearWorkspaceRoots removes cached roots after the active project is
// removed. Every mutation has an inverse; a failure restores all adapters and
// the exact WorkspaceContext generation instead of leaving a partial clear.
func (p *ProjectService) clearWorkspaceRoots() error {
	authority := p.beginAgentWorkspaceAuthority()
	defer authority.release()
	transition, err := p.beginWorkspaceRootClearWithinAuthority(authority)
	if err != nil {
		return err
	}
	if err := authority.flushCatalog(); err != nil {
		return errors.Join(fmt.Errorf("refresh Agent catalog: %w", err), transition.rollback())
	}
	transition.commit()
	return nil
}

func (p *ProjectService) beginWorkspaceRootClearWithinAuthority(authority *agentWorkspaceAuthorityGuard) (*workspaceClearTransition, error) {
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
		var transition workspaceRootChange
		actions = append(actions, workspaceClearAction{
			label: "agent",
			clear: func() error {
				var err error
				transition, err = service.beginWorkspaceRootClearTransitionWithinAuthority(authority)
				return err
			},
			restore: func() error {
				if transition != nil {
					return transition.rollback()
				}
				return nil
			},
			commit: func() {
				if transition == nil {
					return
				}
				transition.commit()
			},
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

	if p.beforeWorkspaceSetters != nil {
		p.beforeWorkspaceSetters()
	}
	applied := make([]workspaceClearAction, 0, len(actions))
	for _, action := range actions {
		applied = append(applied, action)
		if err := action.clear(); err != nil {
			rollbackErr := p.poisonAgentWorkspaceRollback(rollbackWorkspaceClear(applied))
			return nil, errors.Join(
				&workspaceClearError{label: action.label, cause: err},
				rollbackErr,
			)
		}
	}
	return &workspaceClearTransition{actions: applied, agent: p.agentService}, nil
}

type workspaceClearTransition struct {
	mu      sync.Mutex
	actions []workspaceClearAction
	agent   agentServiceRootSetter
	done    bool
	result  error
}

func (t *workspaceClearTransition) commit() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	for _, action := range t.actions {
		if action.commit != nil {
			action.commit()
		}
	}
	t.done = true
}

func (t *workspaceClearTransition) rollback() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return t.result
	}
	t.result = rollbackWorkspaceClear(t.actions)
	if t.result != nil && t.agent != nil {
		t.result = t.agent.poisonWorkspaceAuthorityAfterRollback(t.result)
	}
	t.done = true
	return t.result
}

type workspaceClearAction struct {
	label   string
	clear   func() error
	restore func() error
	commit  func()
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
