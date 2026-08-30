package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

type projectRollbackMCPSetter struct {
	root         string
	err          error
	failRoot     string
	restoreErr   error
	restoreRoot  string
	mutate       bool
	beforeReturn func(string)
}

func (s *projectRollbackMCPSetter) setWorkspaceRoot(root string) error {
	if s.mutate {
		s.root = root
	}
	if s.beforeReturn != nil {
		s.beforeReturn(root)
	}
	// 工作区根在进入 setter 前已规范化（符号链接/8.3 短名解析），注入的
	// failRoot/restoreRoot 是测试传入的原始拼写，二者须按路径身份比较。
	if s.err != nil && sameWorkspaceIdentityPath(root, s.failRoot) {
		return s.err
	}
	if s.restoreErr != nil && sameWorkspaceIdentityPath(root, s.restoreRoot) {
		return s.restoreErr
	}
	return nil
}

func (s *projectRollbackMCPSetter) WorkspaceRoot() string { return s.root }

type projectRollbackClearMCPSetter struct {
	root        string
	err         error
	setErr      error
	failSetRoot string
}

func (s *projectRollbackClearMCPSetter) setWorkspaceRoot(root string) error {
	s.root = root
	// 注入的 failSetRoot 是原始拼写；进入 setter 的根已规范化，按路径身份比较。
	if s.setErr != nil && sameWorkspaceIdentityPath(root, s.failSetRoot) {
		return s.setErr
	}
	return nil
}

func (s *projectRollbackClearMCPSetter) restoreWorkspaceRoot(root string) error {
	s.root = root
	if root == "" {
		return s.err
	}
	return nil
}

func (s *projectRollbackClearMCPSetter) WorkspaceRoot() string { return s.root }

type projectRollbackBlockingMCPSetter struct {
	mu       sync.Mutex
	root     string
	failRoot string
	err      error
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (s *projectRollbackBlockingMCPSetter) setWorkspaceRoot(root string) error {
	s.mu.Lock()
	s.root = root
	s.mu.Unlock()
	// 注入的 failRoot 是原始拼写；进入 setter 的根已规范化，按路径身份比较。
	if !sameWorkspaceIdentityPath(root, s.failRoot) {
		return nil
	}
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.err
}

func (s *projectRollbackBlockingMCPSetter) WorkspaceRoot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root
}

type projectRollbackPersistence struct {
	mu          sync.Mutex
	rows        []agentcore.Session
	failClose   bool
	failRestore bool
	err         error
}

func (p *projectRollbackPersistence) Load() ([]agentcore.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]agentcore.Session(nil), p.rows...), nil
}

func (p *projectRollbackPersistence) Save(rows []agentcore.Session) (agentcore.PersistenceCommitState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failRestore {
		for _, row := range rows {
			if row.Status == agentcore.SessionRunning {
				return agentcore.PersistenceNotPublished, p.err
			}
		}
	}
	if p.failClose {
		for _, row := range rows {
			if row.Status == agentcore.SessionCompleted {
				return agentcore.PersistenceNotPublished, p.err
			}
		}
	}
	p.rows = append([]agentcore.Session(nil), rows...)
	return agentcore.PersistenceDurable, nil
}

func TestProjectServiceRollbackPreservesAgentLifecycleAuthority(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, NewAIPermissionService(t.TempDir())); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:rollback")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	beforeGeneration := agent.agentWorkspaceGeneration()
	before, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("lifecycle.GetByID before switch: %v", err)
	}
	if before.Status != agentcore.SessionRunning {
		t.Fatalf("session status before switch = %q, want running", before.Status)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime before switch: %v", err)
	}
	if !runtime.IsSessionRegistered(sessionID) {
		t.Fatal("session was not registered before switch")
	}

	setterErr := errors.New("injected late workspace setter failure")
	mcp := &projectRollbackMCPSetter{root: rootA, err: setterErr, failRoot: rootB, mutate: true}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		mcpService:   mcp,
	}

	if _, err := service.AddProject(rootB); !errors.Is(err, setterErr) {
		t.Fatalf("AddProject error = %v, want late setter error", err)
	}
	if got := mcp.WorkspaceRoot(); got != rootA {
		t.Fatalf("MCP root after rollback = %q, want %q", got, rootA)
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("agent root after rollback = %q, want %q", got, rootA)
	}
	if got := agent.agentWorkspaceGeneration(); got != beforeGeneration {
		t.Fatalf("agent generation after rollback = %d, want %d", got, beforeGeneration)
	}
	after, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("lifecycle.GetByID after rollback: %v", err)
	}
	if after.Status != agentcore.SessionRunning {
		t.Fatalf("session status after rollback = %q, want running", after.Status)
	}
	runtime, err = agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime after rollback: %v", err)
	}
	if !runtime.IsSessionRegistered(sessionID) {
		t.Fatal("rollback revoked the old runtime session")
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); err != nil {
		t.Fatalf("original caller owner after rollback: %v", err)
	}
}

func TestProjectServiceAdapterRollbackFailurePoisonsAgentAuthority(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:adapter-rollback-poison")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	setterErr := errors.New("injected candidate MCP setter failure")
	restoreErr := errors.New("injected MCP rollback failure")
	mcp := &projectRollbackMCPSetter{
		root: rootA, err: setterErr, failRoot: rootB,
		restoreErr: restoreErr, restoreRoot: rootA, mutate: true,
	}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		mcpService:   mcp,
	}
	_, err = service.AddProject(rootB)
	if !errors.Is(err, setterErr) || !errors.Is(err, restoreErr) || !errors.Is(err, agentcore.ErrSessionPersistencePoisoned) {
		t.Fatalf("AddProject error = %v, want setter, rollback, and poison errors", err)
	}
	row, getErr := lifecycle.GetByID(sessionID)
	if getErr != nil || row.Status != agentcore.SessionRunning {
		t.Fatalf("durable row after adapter rollback poison = %+v, err=%v", row, getErr)
	}
	runtime, runtimeErr := agent.coreRuntime()
	if runtimeErr != nil {
		t.Fatalf("coreRuntime: %v", runtimeErr)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("adapter rollback failure left restored runtime authority active")
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("owner after adapter rollback poison = %v, want ErrNotAllowed", err)
	}
	if _, createErr := agent.CreateAgentSessionForCaller(caller, "chat"); !errors.Is(createErr, agentcore.ErrSessionPersistencePoisoned) {
		t.Fatalf("new session after adapter rollback poison = %v, want ErrSessionPersistencePoisoned", createErr)
	}
}

func TestProjectServiceMultiRootAdapterRollbackFailurePoisonsAgentAuthority(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	rootC := canonicalTestPath(t, t.TempDir())
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:multi-root-adapter-rollback-poison")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	setterErr := errors.New("injected multi-root MCP setter failure")
	restoreErr := errors.New("injected multi-root MCP rollback failure")
	mcp := &projectRollbackMCPSetter{
		root: rootA, err: setterErr, failRoot: rootB,
		restoreErr: restoreErr, restoreRoot: rootA, mutate: true,
	}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		mcpService:   mcp,
	}
	_, err = service.AddMultiRootProject([]string{rootB, rootC}, "")
	if !errors.Is(err, setterErr) || !errors.Is(err, restoreErr) || !errors.Is(err, agentcore.ErrSessionPersistencePoisoned) {
		t.Fatalf("AddMultiRootProject error = %v, want setter, rollback, and poison errors", err)
	}
	row, getErr := lifecycle.GetByID(sessionID)
	if getErr != nil || row.Status != agentcore.SessionRunning {
		t.Fatalf("durable row after multi-root adapter rollback poison = %+v, err=%v", row, getErr)
	}
	runtime, runtimeErr := agent.coreRuntime()
	if runtimeErr != nil {
		t.Fatalf("coreRuntime: %v", runtimeErr)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("multi-root adapter rollback failure left restored runtime authority active")
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("owner after multi-root adapter rollback poison = %v, want ErrNotAllowed", err)
	}
	if _, createErr := agent.CreateAgentSessionForCaller(caller, "chat"); !errors.Is(createErr, agentcore.ErrSessionPersistencePoisoned) {
		t.Fatalf("new session after multi-root adapter rollback poison = %v, want ErrSessionPersistencePoisoned", createErr)
	}
}

func TestProjectServiceWorkspaceAuthorityPrecedesFirstSetter(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	workspace := NewWorkspaceContext()
	if err := workspace.Set(rootA); err != nil {
		t.Fatalf("set workspace context: %v", err)
	}
	file := NewFileServiceWithWorkspaceContext(workspace)
	if err := file.setWorkspaceRoot(rootA); err != nil {
		t.Fatalf("set file workspace: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(rootA); err != nil {
		t.Fatalf("configure agent workspace: %v", err)
	}
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, file, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	setterEntered := make(chan struct{})
	setterRelease := make(chan struct{})
	service := &ProjectService{
		configPath:  filepath.Join(t.TempDir(), "projects.json"),
		fileService: file, agentService: agent, wsCtx: workspace,
		beforeWorkspaceSetters: func() {
			close(setterEntered)
			<-setterRelease
		},
	}
	addDone := make(chan error, 1)
	go func() {
		_, addErr := service.AddProject(rootB)
		addDone <- addErr
	}()
	select {
	case <-setterEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace transaction did not reach the first-setter barrier")
	}
	if got := workspace.Root(); got != rootA {
		t.Fatalf("workspace context published before first setter barrier: got %q want %q", got, rootA)
	}
	if got := file.WorkspaceRoots(); len(got) != 1 || got[0] != rootA {
		t.Fatalf("file roots published before first setter barrier: got %v want [%q]", got, rootA)
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("agent root published before first setter barrier: got %q want %q", got, rootA)
	}
	beginStarted := make(chan struct{})
	beginDone := make(chan error, 1)
	go func() {
		close(beginStarted)
		_, beginErr := lifecycle.Begin(agentcore.SessionPlan, "during-project-switch")
		beginDone <- beginErr
	}()
	<-beginStarted
	var early error
	returnedEarly := false
	select {
	case early = <-beginDone:
		returnedEarly = true
	case <-time.After(200 * time.Millisecond):
	}
	close(setterRelease)
	if addErr := <-addDone; addErr != nil {
		t.Fatalf("AddProject: %v", addErr)
	}
	if returnedEarly {
		t.Fatalf("lifecycle authority was published before the project transaction acquired its barrier: %v", early)
	}
	select {
	case beginErr := <-beginDone:
		if beginErr != nil {
			t.Fatalf("lifecycle begin after project commit: %v", beginErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle begin remained blocked after project commit")
	}
}

func TestProjectServiceWorkspaceAuthorityHeldThroughSnapshotPublication(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	workspace := NewWorkspaceContext()
	if err := workspace.Set(rootA); err != nil {
		t.Fatalf("set workspace context: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(rootA); err != nil {
		t.Fatalf("configure agent workspace: %v", err)
	}
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	published := make(chan WorkspaceSnapshot, 1)
	publishRelease := make(chan struct{})
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		wsCtx:        workspace,
		workspaceSnapshotSink: func(snapshot WorkspaceSnapshot) {
			published <- snapshot
			<-publishRelease
		},
	}
	addDone := make(chan error, 1)
	go func() {
		_, addErr := service.AddProject(rootB)
		addDone <- addErr
	}()
	var snapshot WorkspaceSnapshot
	select {
	case snapshot = <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace transaction did not reach snapshot publication")
	}
	if snapshot.Root != rootB || snapshot.ProjectPath != rootB || snapshot.ProjectID == "" {
		t.Fatalf("published snapshot before release = %+v, want committed project at %q", snapshot, rootB)
	}
	beginDone := make(chan error, 1)
	go func() {
		_, beginErr := lifecycle.Begin(agentcore.SessionPlan, "during-snapshot-publication")
		beginDone <- beginErr
	}()
	select {
	case beginErr := <-beginDone:
		t.Fatalf("lifecycle admission crossed snapshot publication: %v", beginErr)
	case <-time.After(150 * time.Millisecond):
	}
	close(publishRelease)
	if addErr := <-addDone; addErr != nil {
		t.Fatalf("AddProject: %v", addErr)
	}
	select {
	case beginErr := <-beginDone:
		if beginErr != nil {
			t.Fatalf("lifecycle admission after snapshot publication: %v", beginErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle admission remained blocked after snapshot publication")
	}
}

func TestProjectServiceMultiRootAuthorityPrecedesFirstSetter(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	rootC := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	setterEntered := make(chan struct{})
	setterRelease := make(chan struct{})
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		beforeWorkspaceSetters: func() {
			close(setterEntered)
			<-setterRelease
		},
	}
	addDone := make(chan error, 1)
	go func() {
		_, addErr := service.AddMultiRootProject([]string{rootB, rootC}, "")
		addDone <- addErr
	}()
	select {
	case <-setterEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("multi-root transaction did not reach its first-setter barrier")
	}
	beginDone := make(chan error, 1)
	go func() {
		_, beginErr := lifecycle.Begin(agentcore.SessionPlan, "during-multi-root-switch")
		beginDone <- beginErr
	}()
	select {
	case beginErr := <-beginDone:
		t.Fatalf("lifecycle admission crossed multi-root transaction: %v", beginErr)
	case <-time.After(150 * time.Millisecond):
	}
	close(setterRelease)
	if addErr := <-addDone; addErr != nil {
		t.Fatalf("AddMultiRootProject: %v", addErr)
	}
	select {
	case beginErr := <-beginDone:
		if beginErr != nil {
			t.Fatalf("lifecycle admission after multi-root commit: %v", beginErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle admission remained blocked after multi-root commit")
	}
}

func TestProjectServiceClearAuthorityPrecedesFirstSetter(t *testing.T) {
	root := t.TempDir()
	workspace := NewWorkspaceContext()
	if err := workspace.Set(root); err != nil {
		t.Fatalf("set workspace context: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(root); err != nil {
		t.Fatalf("configure agent workspace: %v", err)
	}
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		wsCtx:        workspace,
	}
	project, err := service.AddProject(root)
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	setterEntered := make(chan struct{})
	setterRelease := make(chan struct{})
	service.beforeWorkspaceSetters = func() {
		close(setterEntered)
		<-setterRelease
	}
	removeDone := make(chan error, 1)
	go func() { removeDone <- service.RemoveProject(project.ID) }()
	select {
	case <-setterEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace clear did not reach its first-setter barrier")
	}
	beginDone := make(chan error, 1)
	go func() {
		_, beginErr := lifecycle.Begin(agentcore.SessionPlan, "during-workspace-clear")
		beginDone <- beginErr
	}()
	select {
	case beginErr := <-beginDone:
		t.Fatalf("lifecycle admission crossed workspace clear: %v", beginErr)
	case <-time.After(150 * time.Millisecond):
	}
	close(setterRelease)
	if removeErr := <-removeDone; removeErr != nil {
		t.Fatalf("RemoveProject: %v", removeErr)
	}
	select {
	case <-beginDone:
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle admission remained blocked after workspace clear")
	}
}

func TestProjectServiceMultiRootAgentBeginFailureDoesNotReenterWorkspaceAuthority(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	rootC := canonicalTestPath(t, t.TempDir())
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	persistenceErr := errors.New("injected lifecycle close rejection")
	persistence := &projectRollbackPersistence{err: persistenceErr}
	store, err := agentcore.NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("NewPersistentSessionStore: %v", err)
	}
	lifecycle, err := wireAgentLifecycleWithStore(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
		store,
		[]byte("multi-root-agent-begin-failure-key"),
	)
	if err != nil {
		t.Fatalf("wireAgentLifecycleWithStore: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:multi-root-begin-failure")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	beforeGeneration := agent.agentWorkspaceGeneration()
	persistence.mu.Lock()
	persistence.failClose = true
	persistence.mu.Unlock()
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
	}
	done := make(chan error, 1)
	go func() {
		_, addErr := service.AddMultiRootProject([]string{rootB, rootC}, "")
		done <- addErr
	}()
	select {
	case addErr := <-done:
		if !errors.Is(addErr, persistenceErr) {
			t.Fatalf("AddMultiRootProject error = %v, want lifecycle persistence rejection", addErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AddMultiRootProject deadlocked by re-entering its workspace authority guard")
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("agent root after rejected multi-root switch = %q, want %q", got, rootA)
	}
	if got := agent.agentWorkspaceGeneration(); got != beforeGeneration {
		t.Fatalf("agent generation after rejected multi-root switch = %d, want %d", got, beforeGeneration)
	}
	row, err := lifecycle.GetByID(sessionID)
	if err != nil || row.Status != agentcore.SessionRunning {
		t.Fatalf("lifecycle row after rejected multi-root switch = %+v, err=%v", row, err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil || !runtime.IsSessionRegistered(sessionID) {
		t.Fatalf("runtime after rejected multi-root switch registered=%v err=%v", runtime != nil && runtime.IsSessionRegistered(sessionID), err)
	}
}

func TestProjectServiceLoadFailureRestoresAgentLifecycleAuthority(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:rollback-load")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	beforeGeneration := agent.agentWorkspaceGeneration()
	service := &ProjectService{
		// Reading a directory as projects.json fails after every workspace setter
		// has applied, exercising the Phase 2 rollback rather than a setter error.
		configPath:   t.TempDir(),
		agentService: agent,
	}
	if _, err := service.AddProject(rootB); err == nil {
		t.Fatal("AddProject unexpectedly succeeded with an unreadable project ledger")
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("agent root after load rollback = %q, want %q", got, rootA)
	}
	if got := agent.agentWorkspaceGeneration(); got != beforeGeneration {
		t.Fatalf("agent generation after load rollback = %d, want %d", got, beforeGeneration)
	}
	row, err := lifecycle.GetByID(sessionID)
	if err != nil || row.Status != agentcore.SessionRunning {
		t.Fatalf("lifecycle row after load rollback = %+v, err=%v", row, err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil || !runtime.IsSessionRegistered(sessionID) {
		t.Fatalf("runtime after load rollback registered=%v err=%v", runtime != nil && runtime.IsSessionRegistered(sessionID), err)
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); err != nil {
		t.Fatalf("renderer owner after load rollback: %v", err)
	}
}

func TestProjectServiceSaveFailureRestoresAgentLifecycleAuthority(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:rollback-save")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	beforeGeneration := agent.agentWorkspaceGeneration()
	saveErr := errors.New("injected project ledger save failure")
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		beforeSave:   func([]Project) error { return saveErr },
	}
	if _, err := service.AddProject(rootB); !errors.Is(err, saveErr) {
		t.Fatalf("AddProject error = %v, want save failure", err)
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("agent root after save rollback = %q, want %q", got, rootA)
	}
	if got := agent.agentWorkspaceGeneration(); got != beforeGeneration {
		t.Fatalf("agent generation after save rollback = %d, want %d", got, beforeGeneration)
	}
	row, err := lifecycle.GetByID(sessionID)
	if err != nil || row.Status != agentcore.SessionRunning {
		t.Fatalf("lifecycle row after save rollback = %+v, err=%v", row, err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil || !runtime.IsSessionRegistered(sessionID) {
		t.Fatalf("runtime after save rollback registered=%v err=%v", runtime != nil && runtime.IsSessionRegistered(sessionID), err)
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); err != nil {
		t.Fatalf("renderer owner after save rollback: %v", err)
	}
}

func TestProjectServiceSingleRootMultiProjectUsesOneTransactionalSave(t *testing.T) {
	root := canonicalTestPath(t, t.TempDir())
	secondSaveErr := errors.New("unexpected second project ledger save")
	saveCalls := 0
	service := &ProjectService{
		configPath: filepath.Join(t.TempDir(), "projects.json"),
		beforeSave: func([]Project) error {
			saveCalls++
			if saveCalls > 1 {
				return secondSaveErr
			}
			return nil
		},
	}
	project, err := service.AddMultiRootProject([]string{root}, "")
	if err != nil {
		t.Fatalf("AddMultiRootProject: %v", err)
	}
	if saveCalls != 1 {
		t.Fatalf("project ledger save calls = %d, want 1", saveCalls)
	}
	if len(project.Roots) != 1 || project.Roots[0] != root || project.IsWorkspace {
		t.Fatalf("single-root project metadata = %+v", project)
	}
}

func TestProjectServiceRollbackSerializesChatExecutionAndBurnsOutstandingCapability(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(rootA, "note.txt"), []byte("workspace-a"), 0o600); err != nil {
		t.Fatalf("seed workspace A: %v", err)
	}
	workspace := NewWorkspaceContext()
	if err := workspace.Set(rootA); err != nil {
		t.Fatalf("set workspace context: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(rootA); err != nil {
		t.Fatalf("configure agent workspace: %v", err)
	}
	file := NewFileServiceWithWorkspaceContext(workspace)
	if err := file.setWorkspaceRoot(rootA); err != nil {
		t.Fatalf("configure file workspace: %v", err)
	}
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, file, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	if _, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:rollback-capability")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(caller)
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	grant, err := agent.RequestAgentToolCapability(caller, AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "read",
		Arguments: map[string]interface{}{"path": "note.txt"},
	})
	if err != nil {
		t.Fatalf("RequestAgentToolCapability: %v", err)
	}

	setterErr := errors.New("injected blocked workspace setter failure")
	mcp := &projectRollbackBlockingMCPSetter{
		root: rootA, failRoot: rootB, err: setterErr,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	service := &ProjectService{
		configPath:  filepath.Join(t.TempDir(), "projects.json"),
		fileService: file, agentService: agent, mcpService: mcp, wsCtx: workspace,
	}
	addDone := make(chan error, 1)
	go func() {
		_, addErr := service.AddProject(rootB)
		addDone <- addErr
	}()
	select {
	case <-mcp.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace switch did not reach the blocked late setter")
	}

	executeDone := make(chan error, 1)
	executeStarted := make(chan struct{})
	go func() {
		close(executeStarted)
		_, executeErr := agent.ExecuteApprovedAgentTool(caller, AgentToolCapabilityExecution{
			Token: grant.Token, SessionID: sessionID, CatalogRevision: grant.CatalogRevision,
			ToolID: grant.ToolID, Arguments: map[string]interface{}{"path": "note.txt"},
		})
		executeDone <- executeErr
	}()
	<-executeStarted
	select {
	case executeErr := <-executeDone:
		t.Fatalf("chat capability returned while workspace transaction was unresolved: %v", executeErr)
	case <-time.After(200 * time.Millisecond):
	}

	close(mcp.release)
	if addErr := <-addDone; !errors.Is(addErr, setterErr) {
		t.Fatalf("AddProject error = %v, want late setter failure", addErr)
	}
	select {
	case executeErr := <-executeDone:
		if !errors.Is(executeErr, agentcore.ErrInvalidCapability) {
			t.Fatalf("execution after rollback = %v, want burned capability", executeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat capability remained blocked after workspace rollback")
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("agent root after rollback = %q, want %q", got, rootA)
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); err != nil {
		t.Fatalf("renderer owner after rollback: %v", err)
	}
}

func TestProjectServiceRollbackRestoresProjectSkillApprovalAndBinding(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	skillDir := filepath.Join(rootA, ".koyori-ide", "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("create project skill directory: %v", err)
	}
	definition := "id: rollback-skill\nname: Rollback Skill\ndescription: Verify rollback policy\ntrigger:\n  manual: true\nsystemPrompt: Inspect the active workspace.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "rollback-skill.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write project skill: %v", err)
	}
	workspace := NewWorkspaceContext()
	if err := workspace.Set(rootA); err != nil {
		t.Fatalf("set workspace context: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(rootA); err != nil {
		t.Fatalf("configure agent workspace: %v", err)
	}
	skills := NewSkillsService(t.TempDir())
	if err := skills.setWorkspaceRoot(rootA); err != nil {
		t.Fatalf("set skill workspace: %v", err)
	}
	if err := skills.Load(); err != nil {
		t.Fatalf("load project skill: %v", err)
	}
	agent.setSkillsService(skills)
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, skills, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	if _, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	); err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.approveSkill = func(skill Skill) bool { return skill.ID == "rollback-skill" }
	deps.mu.Unlock()
	caller := withAgentCallerContext(context.Background(), "wails-window:rollback-skill")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(caller)
	if err != nil {
		t.Fatalf("GetAgentToolCatalog: %v", err)
	}
	activationID := ""
	for _, tool := range catalog.Tools {
		if tool.Source == string(agentcore.SourceSkill) && tool.Metadata["skillId"] == "rollback-skill" {
			activationID = tool.ID
			break
		}
	}
	if activationID == "" {
		t.Fatalf("rollback project skill ToolDef missing: %+v", catalog.Tools)
	}
	if _, err := agent.ExecuteAgentTool(caller, AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: activationID, Arguments: map[string]interface{}{},
	}); err != nil {
		t.Fatalf("activate project skill: %v", err)
	}
	if !skills.IsApproved("rollback-skill") {
		t.Fatal("project skill was not approved before workspace switch")
	}
	beforeBinding, bound := agentSkillBindingSnapshot(agent, sessionID, "rollback-skill")
	if !bound {
		t.Fatal("project skill was not bound before workspace switch")
	}

	setterErr := errors.New("injected late setter failure after skill reload")
	mcp := &projectRollbackMCPSetter{root: rootA, err: setterErr, failRoot: rootB, mutate: true}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent, mcpService: mcp, wsCtx: workspace,
	}
	if _, err := service.AddProject(rootB); !errors.Is(err, setterErr) {
		t.Fatalf("AddProject error = %v, want late setter failure", err)
	}
	if !skills.IsApproved("rollback-skill") {
		t.Fatal("workspace rollback dropped the project skill approval")
	}
	afterBinding, bound := agentSkillBindingSnapshot(agent, sessionID, "rollback-skill")
	if !bound || afterBinding != beforeBinding {
		t.Fatalf("skill binding after rollback = %q/%v, want %q/true", afterBinding, bound, beforeBinding)
	}
	afterCatalog, err := agent.GetAgentToolCatalog(caller)
	if err != nil {
		t.Fatalf("GetAgentToolCatalog after rollback: %v", err)
	}
	activationID = ""
	for _, tool := range afterCatalog.Tools {
		if tool.Source == string(agentcore.SourceSkill) && tool.Metadata["skillId"] == "rollback-skill" {
			activationID = tool.ID
			break
		}
	}
	if activationID == "" {
		t.Fatal("rollback did not republish the original project skill ToolDef")
	}
	if _, err := agent.RequestAgentToolCapability(caller, AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: afterCatalog.Revision,
		ToolID: activationID, Arguments: map[string]interface{}{},
	}); err != nil {
		t.Fatalf("restored project skill policy rejected a fresh capability: %v", err)
	}
}

func TestProjectServiceSkillRollbackIdentityFailurePoisonsAuthority(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	skillDir := filepath.Join(rootA, ".koyori-ide", "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("create project skill directory: %v", err)
	}
	skillPath := filepath.Join(skillDir, "rollback-skill.yaml")
	original := "id: rollback-skill\nname: Rollback Skill\ndescription: Original policy\ntrigger:\n  manual: true\nsystemPrompt: Inspect the active workspace.\n"
	if err := os.WriteFile(skillPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write project skill: %v", err)
	}
	workspace := NewWorkspaceContext()
	if err := workspace.Set(rootA); err != nil {
		t.Fatalf("set workspace context: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(rootA); err != nil {
		t.Fatalf("configure agent workspace: %v", err)
	}
	skills := NewSkillsService(t.TempDir())
	if err := skills.setWorkspaceRoot(rootA); err != nil {
		t.Fatalf("set skill workspace: %v", err)
	}
	if err := skills.Load(); err != nil {
		t.Fatalf("load project skill: %v", err)
	}
	if err := skills.activateSkillTrusted("rollback-skill"); err != nil {
		t.Fatalf("approve project skill: %v", err)
	}
	agent.setSkillsService(skills)
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, skills, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:rollback-skill-poison")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	setterErr := errors.New("injected late setter failure after skill identity drift")
	var rewriteErr error
	mcp := &projectRollbackMCPSetter{
		root: rootA, err: setterErr, failRoot: rootB, mutate: true,
		beforeReturn: func(root string) {
			if root != rootB {
				return
			}
			changed := strings.Replace(original, "Original policy", "Changed policy", 1)
			rewriteErr = os.WriteFile(skillPath, []byte(changed), 0o600)
		},
	}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent, mcpService: mcp, wsCtx: workspace,
	}
	_, err = service.AddProject(rootB)
	if rewriteErr != nil {
		t.Fatalf("rewrite original skill: %v", rewriteErr)
	}
	if !errors.Is(err, setterErr) || !errors.Is(err, agentcore.ErrSessionPersistencePoisoned) {
		t.Fatalf("AddProject error = %v, want setter error plus ErrSessionPersistencePoisoned", err)
	}
	row, getErr := lifecycle.GetByID(sessionID)
	if getErr != nil {
		t.Fatalf("lifecycle.GetByID after poison: %v", getErr)
	}
	if row.Status != agentcore.SessionRunning {
		t.Fatalf("durable lifecycle status after skill rollback poison = %q, want original running fact", row.Status)
	}
	runtime, runtimeErr := agent.coreRuntime()
	if runtimeErr != nil {
		t.Fatalf("coreRuntime: %v", runtimeErr)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("skill rollback poison restored runtime authority")
	}
	if _, createErr := agent.CreateAgentSessionForCaller(caller, "chat"); !errors.Is(createErr, agentcore.ErrSessionPersistencePoisoned) {
		t.Fatalf("new session after skill rollback poison = %v, want ErrSessionPersistencePoisoned", createErr)
	}
}

func TestProjectServiceRollbackSerializesSessionOwnerPublication(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	ownerEntered := make(chan struct{})
	ownerRelease := make(chan struct{})
	var ownerOnce sync.Once
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.sessionOwnerMutationHook = func(stage string) {
		if stage != "before-owner-bind" {
			return
		}
		ownerOnce.Do(func() { close(ownerEntered) })
		<-ownerRelease
	}
	deps.mu.Unlock()
	t.Cleanup(func() {
		deps.mu.Lock()
		deps.sessionOwnerMutationHook = nil
		deps.mu.Unlock()
	})

	caller := withAgentCallerContext(context.Background(), "wails-window:rollback-owner-race")
	createDone := make(chan struct {
		id  string
		err error
	}, 1)
	go func() {
		id, createErr := agent.CreateAgentSessionForCaller(caller, "chat")
		createDone <- struct {
			id  string
			err error
		}{id: id, err: createErr}
	}()
	select {
	case <-ownerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("session create did not reach the owner publication window")
	}

	setterErr := errors.New("injected late setter failure after session create")
	mcp := &projectRollbackBlockingMCPSetter{
		root: rootA, failRoot: rootB, err: setterErr,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent, mcpService: mcp,
	}
	addDone := make(chan error, 1)
	go func() {
		_, addErr := service.AddProject(rootB)
		addDone <- addErr
	}()
	select {
	case <-mcp.entered:
		t.Fatal("workspace transition passed a session whose owner was not published")
	case <-time.After(200 * time.Millisecond):
	}
	close(ownerRelease)
	created := <-createDone
	if created.err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", created.err)
	}
	select {
	case <-mcp.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace transition did not resume after owner publication")
	}
	close(mcp.release)
	if addErr := <-addDone; !errors.Is(addErr, setterErr) {
		t.Fatalf("AddProject error = %v, want late setter failure", addErr)
	}
	row, err := lifecycle.GetByID(created.id)
	if err != nil || row.Status != agentcore.SessionRunning {
		t.Fatalf("restored lifecycle row = %+v, err=%v", row, err)
	}
	if err := authorizeAgentSessionOwner(agent, caller, created.id); err != nil {
		t.Fatalf("restored renderer owner: %v", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil || !runtime.IsSessionRegistered(created.id) {
		t.Fatalf("restored runtime session registered=%v err=%v", runtime != nil && runtime.IsSessionRegistered(created.id), err)
	}
}

func TestProjectServiceRollbackSerializesSessionOwnerRemoval(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:rollback-close-race")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	ownerEntered := make(chan struct{})
	ownerRelease := make(chan struct{})
	var ownerOnce sync.Once
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.sessionOwnerMutationHook = func(stage string) {
		if stage != "before-owner-delete" {
			return
		}
		ownerOnce.Do(func() { close(ownerEntered) })
		<-ownerRelease
	}
	deps.mu.Unlock()
	t.Cleanup(func() {
		deps.mu.Lock()
		deps.sessionOwnerMutationHook = nil
		deps.mu.Unlock()
	})
	closeDone := make(chan error, 1)
	go func() { closeDone <- agent.CloseAgentSessionForCaller(caller, sessionID) }()
	select {
	case <-ownerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("session close did not reach the owner removal window")
	}

	setterErr := errors.New("injected late setter failure after session close")
	mcp := &projectRollbackBlockingMCPSetter{
		root: rootA, failRoot: rootB, err: setterErr,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent, mcpService: mcp,
	}
	addDone := make(chan error, 1)
	go func() {
		_, addErr := service.AddProject(rootB)
		addDone <- addErr
	}()
	select {
	case <-mcp.entered:
		t.Fatal("workspace transition captured a lifecycle close before owner removal")
	case <-time.After(200 * time.Millisecond):
	}
	close(ownerRelease)
	if closeErr := <-closeDone; closeErr != nil {
		t.Fatalf("CloseAgentSessionForCaller: %v", closeErr)
	}
	select {
	case <-mcp.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace transition did not resume after owner removal")
	}
	close(mcp.release)
	if addErr := <-addDone; !errors.Is(addErr, setterErr) {
		t.Fatalf("AddProject error = %v, want late setter failure", addErr)
	}
	row, err := lifecycle.GetByID(sessionID)
	if err != nil || row.Status != agentcore.SessionCompleted {
		t.Fatalf("restored closed lifecycle row = %+v, err=%v", row, err)
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("closed renderer owner after rollback = %v, want ErrNotAllowed", err)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime: %v", err)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("workspace rollback resurrected a session closed before its snapshot")
	}
}

func TestProjectServiceMultiRootRollbackPreservesAgentLifecycleAuthority(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	rootC := canonicalTestPath(t, t.TempDir())
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, NewAIPermissionService(t.TempDir())); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:multi-root-rollback")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	beforeGeneration := agent.agentWorkspaceGeneration()

	setterErr := errors.New("injected multi-root late setter failure")
	mcp := &projectRollbackMCPSetter{root: rootA, err: setterErr, failRoot: rootB, mutate: true}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		mcpService:   mcp,
	}
	if _, err := service.AddMultiRootProject([]string{rootB, rootC}, ""); !errors.Is(err, setterErr) {
		t.Fatalf("AddMultiRootProject error = %v, want late setter error", err)
	}
	if got := mcp.WorkspaceRoot(); got != rootA {
		t.Fatalf("MCP root after multi-root rollback = %q, want %q", got, rootA)
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("agent root after multi-root rollback = %q, want %q", got, rootA)
	}
	if got := agent.agentWorkspaceGeneration(); got != beforeGeneration {
		t.Fatalf("agent generation after multi-root rollback = %d, want %d", got, beforeGeneration)
	}
	after, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("lifecycle.GetByID after multi-root rollback: %v", err)
	}
	if after.Status != agentcore.SessionRunning {
		t.Fatalf("multi-root session status after rollback = %q, want running", after.Status)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime after multi-root rollback: %v", err)
	}
	if !runtime.IsSessionRegistered(sessionID) {
		t.Fatal("multi-root rollback revoked the old runtime session")
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); err != nil {
		t.Fatalf("original caller owner after multi-root rollback: %v", err)
	}
}

func TestProjectServiceAgentLifecycleRollbackFailurePoisonsAuthority(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	rootB := canonicalTestPath(t, t.TempDir())
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, NewAIPermissionService(t.TempDir())); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	persistence := &projectRollbackPersistence{err: errors.New("injected rollback publication failure")}
	store, err := agentcore.NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("NewPersistentSessionStore: %v", err)
	}
	lifecycle, err := wireAgentLifecycleWithStore(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
		store,
		[]byte("project-workspace-rollback-poison-key"),
	)
	if err != nil {
		t.Fatalf("wireAgentLifecycleWithStore: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:rollback-poison")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	persistence.mu.Lock()
	persistence.failRestore = true
	persistence.mu.Unlock()

	setterErr := errors.New("injected late workspace setter failure")
	mcp := &projectRollbackMCPSetter{root: rootA, err: setterErr, failRoot: rootB, mutate: true}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		mcpService:   mcp,
	}
	_, err = service.AddProject(rootB)
	if !errors.Is(err, setterErr) || !errors.Is(err, agentcore.ErrSessionPersistencePoisoned) {
		t.Fatalf("AddProject error = %v, want setter error plus ErrSessionPersistencePoisoned", err)
	}
	if got := mcp.WorkspaceRoot(); got != rootA {
		t.Fatalf("MCP root after poisoned rollback = %q, want %q", got, rootA)
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("agent root after poisoned rollback = %q, want %q", got, rootA)
	}
	row, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("lifecycle.GetByID after poisoned rollback: %v", err)
	}
	if row.Status != agentcore.SessionCompleted {
		t.Fatalf("lifecycle status after poisoned rollback = %q, want completed", row.Status)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime after poisoned rollback: %v", err)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("poisoned rollback restored runtime authority")
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("poisoned rollback owner check = %v, want ErrNotAllowed", err)
	}
	if _, err := agent.CreateAgentSessionForCaller(caller, "chat"); !errors.Is(err, agentcore.ErrSessionPersistencePoisoned) {
		t.Fatalf("new session after poisoned rollback = %v, want ErrSessionPersistencePoisoned", err)
	}
}

func TestProjectServiceClearRollbackPreservesAgentLifecycleAuthority(t *testing.T) {
	rootA := canonicalTestPath(t, t.TempDir())
	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	t.Cleanup(func() { _ = agent.Close() })
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, NewAIPermissionService(t.TempDir())); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	clearErr := errors.New("injected late workspace clear failure")
	mcp := &projectRollbackClearMCPSetter{root: rootA, err: clearErr}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		mcpService:   mcp,
		wsCtx:        NewWorkspaceContext(),
	}
	project, err := service.AddProject(rootA)
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:clear-rollback")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	beforeGeneration := agent.agentWorkspaceGeneration()

	if err := service.RemoveProject(project.ID); !errors.Is(err, clearErr) {
		t.Fatalf("RemoveProject error = %v, want clear failure", err)
	}
	if got := mcp.WorkspaceRoot(); got != rootA {
		t.Fatalf("MCP root after clear rollback = %q, want %q", got, rootA)
	}
	if got := agent.currentWorkspaceRoot(); got != rootA {
		t.Fatalf("agent root after clear rollback = %q, want %q", got, rootA)
	}
	if got := agent.agentWorkspaceGeneration(); got != beforeGeneration {
		t.Fatalf("agent generation after clear rollback = %d, want %d", got, beforeGeneration)
	}
	row, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("lifecycle.GetByID after clear rollback: %v", err)
	}
	if row.Status != agentcore.SessionRunning {
		t.Fatalf("clear rollback session status = %q, want running", row.Status)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		t.Fatalf("coreRuntime after clear rollback: %v", err)
	}
	if !runtime.IsSessionRegistered(sessionID) {
		t.Fatal("clear rollback revoked the old runtime session")
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); err != nil {
		t.Fatalf("original caller owner after clear rollback: %v", err)
	}
	projects, err := service.GetRecentProjects()
	if err != nil || len(projects) != 1 || projects[0].ID != project.ID {
		t.Fatalf("project ledger after clear rollback = %+v, err=%v", projects, err)
	}
}

func TestProjectServiceClearRollbackFailurePoisonsAgentAuthority(t *testing.T) {
	root := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, root)
	t.Cleanup(func() { _ = agent.Close() })
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	mcp := &projectRollbackClearMCPSetter{root: root}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		mcpService:   mcp,
		wsCtx:        NewWorkspaceContext(),
	}
	project, err := service.AddProject(root)
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:clear-rollback-poison")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	saveErr := errors.New("injected project removal save failure")
	restoreErr := errors.New("injected MCP restore after clear failure")
	service.beforeSave = func([]Project) error { return saveErr }
	mcp.setErr = restoreErr
	mcp.failSetRoot = root
	err = service.RemoveProject(project.ID)
	if !errors.Is(err, saveErr) || !errors.Is(err, restoreErr) || !errors.Is(err, agentcore.ErrSessionPersistencePoisoned) {
		t.Fatalf("RemoveProject error = %v, want save, rollback, and poison errors", err)
	}
	row, getErr := lifecycle.GetByID(sessionID)
	if getErr != nil || row.Status != agentcore.SessionRunning {
		t.Fatalf("durable row after clear rollback poison = %+v, err=%v", row, getErr)
	}
	runtime, runtimeErr := agent.coreRuntime()
	if runtimeErr != nil {
		t.Fatalf("coreRuntime: %v", runtimeErr)
	}
	if runtime.IsSessionRegistered(sessionID) {
		t.Fatal("clear rollback failure left restored runtime authority active")
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("owner after clear rollback poison = %v, want ErrNotAllowed", err)
	}
	if _, createErr := agent.CreateAgentSessionForCaller(caller, "chat"); !errors.Is(createErr, agentcore.ErrSessionPersistencePoisoned) {
		t.Fatalf("new session after clear rollback poison = %v, want ErrSessionPersistencePoisoned", createErr)
	}
}

func TestProjectServiceRemoveSavesInsideReversibleWorkspaceClear(t *testing.T) {
	root := canonicalTestPath(t, t.TempDir())
	workspace := NewWorkspaceContext()
	if err := workspace.Set(root); err != nil {
		t.Fatalf("set workspace context: %v", err)
	}
	agent := NewAgentServiceWithWorkspaceContext(workspace)
	t.Cleanup(func() { _ = agent.Close() })
	if err := agent.configureWorkspaceRoot(root); err != nil {
		t.Fatalf("configure agent workspace: %v", err)
	}
	permission := NewAIPermissionService(t.TempDir())
	if err := WireAgentExecutionCore(agent, nil, nil, nil, nil, permission); err != nil {
		t.Fatalf("WireAgentExecutionCore: %v", err)
	}
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	service := &ProjectService{
		configPath:   filepath.Join(t.TempDir(), "projects.json"),
		agentService: agent,
		wsCtx:        workspace,
	}
	project, err := service.AddProject(root)
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	caller := withAgentCallerContext(context.Background(), "wails-window:remove-save-rollback")
	sessionID, err := agent.CreateAgentSessionForCaller(caller, "chat")
	if err != nil {
		t.Fatalf("CreateAgentSessionForCaller: %v", err)
	}
	beforeGeneration := agent.agentWorkspaceGeneration()
	saveErr := errors.New("injected remove ledger save failure")
	orderingErr := errors.New("project deletion was saved before workspace clear prepare")
	service.beforeSave = func([]Project) error {
		if agent.currentWorkspaceRoot() != "" {
			return orderingErr
		}
		return saveErr
	}
	if err := service.RemoveProject(project.ID); !errors.Is(err, saveErr) || errors.Is(err, orderingErr) {
		t.Fatalf("RemoveProject error = %v, want save failure after reversible clear", err)
	}
	service.beforeSave = nil
	if got := agent.currentWorkspaceRoot(); got != root {
		t.Fatalf("agent root after remove save rollback = %q, want %q", got, root)
	}
	if got := agent.agentWorkspaceGeneration(); got != beforeGeneration {
		t.Fatalf("agent generation after remove save rollback = %d, want %d", got, beforeGeneration)
	}
	row, err := lifecycle.GetByID(sessionID)
	if err != nil || row.Status != agentcore.SessionRunning {
		t.Fatalf("lifecycle row after remove save rollback = %+v, err=%v", row, err)
	}
	if err := authorizeAgentSessionOwner(agent, caller, sessionID); err != nil {
		t.Fatalf("renderer owner after remove save rollback: %v", err)
	}
	projects, err := service.GetRecentProjects()
	if err != nil || len(projects) != 1 || projects[0].ID != project.ID {
		t.Fatalf("project ledger after remove save rollback = %+v, err=%v", projects, err)
	}
}
