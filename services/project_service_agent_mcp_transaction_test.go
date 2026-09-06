package services

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newProjectAgentMCPWiring(t *testing.T, root string) (*ProjectService, *WorkspaceContext, *AgentService, *MCPService) {
	t.Helper()
	workspace := NewWorkspaceContext()
	if err := workspace.Set(root); err != nil {
		t.Fatalf("workspace.Set: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	mcp := newTestMCPService(t)
	mcp.setWorkspaceContext(workspace)
	if err := mcp.setWorkspaceRoot(root); err != nil {
		t.Fatalf("mcp.setWorkspaceRoot: %v", err)
	}
	skills := NewSkillsService(t.TempDir())
	WireAgentServices(agent, mcp, skills)
	if err := WireAgentExecutionCore(agent, nil, nil, mcp, skills, nil); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	if err := agent.configureWorkspaceRoot(root); err != nil {
		t.Fatalf("agent.configureWorkspaceRoot: %v", err)
	}
	return &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		mcpService:   mcp,
		wsCtx:        workspace,
	}, workspace, agent, mcp
}

// A project switch updates the shared context before the individual service
// roots. Skills and MCP both notify the Agent catalog during their setters, so
// those callbacks must be deferred until every root is committed. This test
// exercises the same trusted wiring used by the application rather than a
// stand-alone fake setter.
func TestProjectServiceWorkspaceSwitchDefersAgentCatalogUntilRootsCommit(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	project, workspace, agent, mcp := newProjectAgentMCPWiring(t, rootA)
	if _, err := project.AddProject(rootB); err != nil {
		t.Fatalf("AddProject with trusted Agent/MCP/Skills wiring: %v", err)
	}
	if got := workspace.Root(); got != rootB {
		t.Fatalf("workspace root = %q, want %q", got, rootB)
	}
	if got := agent.currentWorkspaceRoot(); got != rootB {
		t.Fatalf("agent root = %q, want %q", got, rootB)
	}
	if got := mcp.WorkspaceRoot(); got != rootB {
		t.Fatalf("MCP root = %q, want %q", got, rootB)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	depth, pending := deps.workspaceCatalogDeferralDepth, deps.workspaceCatalogRefreshPending
	deps.mu.RUnlock()
	if depth != 0 || pending {
		t.Fatalf("catalog deferral leaked after commit: depth=%d pending=%v", depth, pending)
	}
	if _, err := agent.GetAgentToolCatalog(context.Background()); err != nil {
		t.Fatalf("GetAgentToolCatalog after committed switch: %v", err)
	}
}

func TestProjectServiceWorkspaceSwitchDefersAgentCatalogAcrossRollback(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	project, workspace, agent, mcp := newProjectAgentMCPWiring(t, rootA)
	saveErr := errors.New("injected project ledger failure")
	project.beforeSave = func([]Project) error { return saveErr }
	if _, err := project.AddProject(rootB); !errors.Is(err, saveErr) {
		t.Fatalf("AddProject error = %v, want save failure", err)
	}
	if got := workspace.Root(); got != rootA {
		t.Fatalf("workspace root after rollback = %q, want %q", got, rootA)
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("agent root after rollback = %q, want %q", got, rootA)
	}
	if got := mcp.WorkspaceRoot(); got != rootA {
		t.Fatalf("MCP root after rollback = %q, want %q", got, rootA)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	depth, pending := deps.workspaceCatalogDeferralDepth, deps.workspaceCatalogRefreshPending
	deps.mu.RUnlock()
	if depth != 0 || pending {
		t.Fatalf("catalog deferral leaked after rollback: depth=%d pending=%v", depth, pending)
	}
	if _, err := agent.GetAgentToolCatalog(context.Background()); err != nil {
		t.Fatalf("GetAgentToolCatalog after rollback: %v", err)
	}
}

func TestProjectServiceWorkspaceSwitchDrainsRefreshAdmissionBeforeSetters(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	project, _, agent, _ := newProjectAgentMCPWiring(t, rootA)

	refreshAdmitted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	setterEntered := make(chan struct{})
	releaseSetter := make(chan struct{})
	var releaseRefreshOnce sync.Once
	var releaseSetterOnce sync.Once
	var refreshReleased atomic.Bool
	var setterCrossedBeforeRefreshRelease atomic.Bool
	releaseBlockedRefresh := func() { releaseRefreshOnce.Do(func() { close(releaseRefresh) }) }
	releaseBlockedSetter := func() { releaseSetterOnce.Do(func() { close(releaseSetter) }) }
	t.Cleanup(releaseBlockedRefresh)
	t.Cleanup(releaseBlockedSetter)

	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.catalogRefreshHook = func(stage string) {
		if stage != agentCatalogRefreshAdmissionBoundary {
			return
		}
		select {
		case <-refreshAdmitted:
		default:
			close(refreshAdmitted)
		}
		<-releaseRefresh
	}
	deps.mu.Unlock()
	project.beforeWorkspaceSetters = func() {
		if !refreshReleased.Load() {
			setterCrossedBeforeRefreshRelease.Store(true)
		}
		close(setterEntered)
		<-releaseSetter
	}

	refreshErr := make(chan error, 1)
	go func() { refreshErr <- agent.refreshDynamicAgentTools(context.Background()) }()
	select {
	case <-refreshAdmitted:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog refresh did not reach its admission boundary")
	}

	addErr := make(chan error, 1)
	go func() {
		_, err := project.AddProject(rootB)
		addErr <- err
	}()

	select {
	case <-setterEntered:
	case <-time.After(200 * time.Millisecond):
	}
	refreshReleased.Store(true)
	releaseBlockedRefresh()
	if err := <-refreshErr; err != nil {
		releaseBlockedSetter()
		t.Fatalf("refresh dynamic Agent tools: %v", err)
	}
	if !setterCrossedBeforeRefreshRelease.Load() {
		select {
		case <-setterEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("workspace transaction did not reach its setters after refresh release")
		}
	}
	releaseBlockedSetter()
	if err := <-addErr; err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if setterCrossedBeforeRefreshRelease.Load() {
		t.Fatal("workspace setters crossed a refresh that had passed deferral admission but not acquired the catalog lock")
	}
}
