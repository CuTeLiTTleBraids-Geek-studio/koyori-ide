package services

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

type g05MCPRootSetter struct {
	root      string
	failSet   string
	failClear bool
}

func (s *g05MCPRootSetter) setWorkspaceRoot(root string) error {
	if root == s.failSet && root != "" {
		return errors.New("injected workspace switch failure")
	}
	s.root = root
	return nil
}

func (s *g05MCPRootSetter) restoreWorkspaceRoot(root string) error {
	if root == "" && s.failClear {
		return errors.New("injected workspace clear failure")
	}
	s.root = root
	return nil
}

func (s *g05MCPRootSetter) WorkspaceRoot() string {
	return s.root
}

type g05ProjectGraph struct {
	project  *ProjectService
	context  *WorkspaceContext
	file     *FileService
	terminal *TerminalService
	agent    *AgentService
}

func newG05ProjectGraph(t *testing.T, mcp MCPServiceRootSetter) *g05ProjectGraph {
	t.Helper()
	context := NewWorkspaceContext()
	file := NewFileServiceWithWorkspaceContext(context)
	terminal := NewTerminalServiceWithWorkspaceContext(context)
	agent := NewAgentServiceWithWorkspaceContext(context)
	project := NewProjectService(file, terminal, agent, nil)
	project.configPath = t.TempDir() + "/projects.json"
	project.setWorkspaceContext(context)
	if mcp != nil {
		project.setMCPService(mcp)
	}
	return &g05ProjectGraph{
		project: project, context: context, file: file, terminal: terminal, agent: agent,
	}
}

func TestG05WorkspaceSnapshotPublishesRootRootsAndGeneration(t *testing.T) {
	graph := newG05ProjectGraph(t, nil)
	var events []WorkspaceSnapshot
	graph.project.workspaceSnapshotSink = func(snapshot WorkspaceSnapshot) {
		events = append(events, snapshot)
	}

	rootA := t.TempDir()
	projectA, err := graph.project.AddProject(rootA)
	if err != nil {
		t.Fatalf("add project A: %v", err)
	}
	snapshotA := graph.project.GetWorkspaceSnapshot()
	if snapshotA.Generation != 1 || snapshotA.Root == "" ||
		!sameWorkspaceRootSet(snapshotA.Roots, []string{snapshotA.Root}) {
		t.Fatalf("single-root snapshot = %+v", snapshotA)
	}
	if snapshotA.ProjectID != projectA.ID || snapshotA.ProjectPath != projectA.Path {
		t.Fatalf("single-root project identity = %+v, want %+v", snapshotA, projectA)
	}

	rootB := t.TempDir()
	rootC := t.TempDir()
	projectBC, err := graph.project.AddMultiRootProject([]string{rootB, rootC}, "")
	if err != nil {
		t.Fatalf("add multi-root project: %v", err)
	}
	snapshotBC := graph.project.GetWorkspaceSnapshot()
	if snapshotBC.Generation != 2 || len(snapshotBC.Roots) != 2 ||
		!sameWorkspaceIdentityPath(snapshotBC.Root, snapshotBC.Roots[0]) {
		t.Fatalf("multi-root snapshot = %+v", snapshotBC)
	}
	if snapshotBC.ProjectID != projectBC.ID || snapshotBC.ProjectPath != projectBC.Path {
		t.Fatalf("multi-root project identity = %+v, want %+v", snapshotBC, projectBC)
	}
	if !sameWorkspaceRootSet(graph.file.WorkspaceRoots(), snapshotBC.Roots) {
		t.Fatalf("file roots = %v, snapshot roots = %v", graph.file.WorkspaceRoots(), snapshotBC.Roots)
	}
	if len(events) != 2 || !reflect.DeepEqual(events[1], snapshotBC) {
		t.Fatalf("workspace events = %+v, final snapshot = %+v", events, snapshotBC)
	}
}

func TestG05ConcurrentWorkspaceSwitchesSerializeAndLatestSnapshotWins(t *testing.T) {
	graph := newG05ProjectGraph(t, nil)
	var events []WorkspaceSnapshot
	graph.project.workspaceSnapshotSink = func(snapshot WorkspaceSnapshot) {
		events = append(events, snapshot)
	}

	roots := []string{t.TempDir(), t.TempDir()}
	start := make(chan struct{})
	errorsByCall := make(chan error, len(roots))
	var wait sync.WaitGroup
	for _, root := range roots {
		root := root
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := graph.project.AddProject(root)
			errorsByCall <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByCall)
	for err := range errorsByCall {
		if err != nil {
			t.Fatalf("concurrent switch: %v", err)
		}
	}

	if len(events) != 2 || events[0].Generation != 1 || events[1].Generation != 2 {
		t.Fatalf("serialized events = %+v", events)
	}
	final := graph.project.GetWorkspaceSnapshot()
	if !reflect.DeepEqual(final, events[1]) {
		t.Fatalf("final snapshot = %+v, latest event = %+v", final, events[1])
	}
	if !sameWorkspaceRootSet(graph.file.WorkspaceRoots(), final.Roots) ||
		!sameWorkspaceIdentityPath(graph.agent.currentWorkspaceRoot(), final.Root) {
		t.Fatalf("service roots diverged: file=%v agent=%q final=%+v",
			graph.file.WorkspaceRoots(), graph.agent.currentWorkspaceRoot(), final)
	}
}

func TestG05WorkspaceSwitchFailureRollsBackAndDoesNotBroadcast(t *testing.T) {
	mcp := &g05MCPRootSetter{}
	graph := newG05ProjectGraph(t, mcp)
	var events []WorkspaceSnapshot
	graph.project.workspaceSnapshotSink = func(snapshot WorkspaceSnapshot) {
		events = append(events, snapshot)
	}

	rootA := t.TempDir()
	if _, err := graph.project.AddProject(rootA); err != nil {
		t.Fatalf("add project A: %v", err)
	}
	before := graph.project.GetWorkspaceSnapshot()
	rootB := t.TempDir()
	mcp.failSet = rootB
	if _, err := graph.project.AddProject(rootB); err == nil {
		t.Fatal("failing workspace switch unexpectedly succeeded")
	}
	after := graph.project.GetWorkspaceSnapshot()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("snapshot changed after failed switch: before=%+v after=%+v", before, after)
	}
	if len(events) != 1 {
		t.Fatalf("failed switch broadcast %d events, want 1 initial event", len(events))
	}
	if !sameWorkspaceRootSet(graph.file.WorkspaceRoots(), before.Roots) ||
		!sameWorkspaceIdentityPath(mcp.root, before.Root) {
		t.Fatalf("services were not rolled back: file=%v mcp=%q before=%+v",
			graph.file.WorkspaceRoots(), mcp.root, before)
	}
}

func TestG05WindowReopenReadsSameSnapshotAndClearRollsBackOnFailure(t *testing.T) {
	mcp := &g05MCPRootSetter{}
	graph := newG05ProjectGraph(t, mcp)
	var events []WorkspaceSnapshot
	graph.project.workspaceSnapshotSink = func(snapshot WorkspaceSnapshot) {
		events = append(events, snapshot)
	}

	root := t.TempDir()
	project, err := graph.project.AddProject(root)
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	first := graph.project.GetWorkspaceSnapshot()
	if reopened := graph.project.GetWorkspaceSnapshot(); !reflect.DeepEqual(reopened, first) {
		t.Fatalf("reopened window snapshot = %+v, want %+v", reopened, first)
	}

	mcp.failClear = true
	if err := graph.project.RemoveProject(project.ID); err == nil {
		t.Fatal("workspace clear unexpectedly succeeded")
	}
	if afterFailure := graph.project.GetWorkspaceSnapshot(); !reflect.DeepEqual(afterFailure, first) {
		t.Fatalf("failed clear changed snapshot: got %+v want %+v", afterFailure, first)
	}
	projects, err := graph.project.GetRecentProjects()
	if err != nil || len(projects) != 1 || projects[0].ID != project.ID {
		t.Fatalf("failed clear did not restore recent projects: projects=%+v err=%v", projects, err)
	}
	if len(events) != 1 {
		t.Fatalf("failed clear broadcast workspace event: %+v", events)
	}

	mcp.failClear = false
	if err := graph.project.RemoveProject(project.ID); err != nil {
		t.Fatalf("clear workspace: %v", err)
	}
	cleared := graph.project.GetWorkspaceSnapshot()
	if cleared.Root != "" || len(cleared.Roots) != 0 || cleared.Generation != first.Generation+1 {
		t.Fatalf("cleared snapshot = %+v", cleared)
	}
	if len(events) != 2 || !reflect.DeepEqual(events[1], cleared) {
		t.Fatalf("clear events = %+v, cleared = %+v", events, cleared)
	}
}
