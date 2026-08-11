package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const g03MCPHelperMarkerEnv = "KOYORI_IDE_G03_MCP_HELPER_MARKER"

// TestG03MCPHelperProcess is launched by TestG03MCPConnectRejectsEmptyWorkspaceBeforeProcessStart.
// It writes a marker immediately so the parent can distinguish a fail-closed
// rejection from a subprocess that started and only failed later during the
// MCP initialize handshake.
func TestG03MCPHelperProcess(t *testing.T) {
	marker := os.Getenv(g03MCPHelperMarkerEnv)
	if marker == "" {
		return
	}
	if err := os.WriteFile(marker, []byte("started"), 0o600); err != nil {
		t.Fatalf("write helper marker: %v", err)
	}
}

// TestG03MCPProtocolHelperProcess is a real stdio MCP child used by the
// positive integration test below. It deliberately implements only the
// initialize/tools-list surface needed by that test.
func TestG03MCPProtocolHelperProcess(t *testing.T) {
	marker := os.Getenv(g03MCPHelperMarkerEnv)
	if marker == "" {
		return
	}
	if err := os.WriteFile(marker, []byte("started\n"), 0o600); err != nil {
		t.Fatalf("write protocol helper marker: %v", err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			JSONRPC string      `json:"jsonrpc"`
			ID      interface{} `json:"id"`
			Method  string      `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatalf("decode MCP helper request: %v", err)
		}
		if request.ID == nil {
			continue
		}
		result := map[string]interface{}{}
		switch request.Method {
		case "initialize":
			result = map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{},
				"serverInfo":      map[string]string{"name": "g03-helper", "version": "1"},
			}
		case "tools/list":
			result = map[string]interface{}{"tools": []map[string]interface{}{{
				"name":        "g03_probe",
				"description": "G03 real subprocess probe",
				"inputSchema": map[string]interface{}{"type": "object"},
			}}}
		default:
			result = map[string]interface{}{}
		}
		if err := encoder.Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      request.ID,
			"result":  result,
		}); err != nil {
			t.Fatalf("encode MCP helper response: %v", err)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("read MCP helper stdin: %v", err)
	}
}

func TestG03SearchRejectsEmptyWorkspaceBeforeReading(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle"), 0o600); err != nil {
		t.Fatalf("write search fixture: %v", err)
	}

	service := NewSearchService()
	results, err := service.Search(outside, "needle", false)
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("Search without a workspace error = %v, results = %#v; want ErrNotAllowed", err, results)
	}
}

func TestG03MCPConnectRejectsEmptyWorkspaceBeforeProcessStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "mcp-started")
	service := newTestMCPService(t)
	service.setWorkspaceContext(NewWorkspaceContext())
	service.config.Servers = []MCPServerConfig{{
		Name:      "empty-root-probe",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestG03MCPHelperProcess$"},
		Env:       map[string]string{g03MCPHelperMarkerEnv: marker},
		Enabled:   true,
	}}

	err := service.ConnectServer(context.Background(), "empty-root-probe")
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ConnectServer without a workspace error = %v, want ErrNotAllowed", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("MCP subprocess started before empty-workspace rejection; marker stat error = %v", statErr)
	}
}

func TestG03SymbolIndexRejectsEmptyWorkspaceInsteadOfEmptySuccess(t *testing.T) {
	service := NewSymbolIndexServiceWithWorkspaceContext(NewWorkspaceContext())
	results, err := service.SearchSymbols(context.Background(), "anything", 10)
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("SearchSymbols without a workspace error = %v, results = %#v; want ErrNotAllowed", err, results)
	}
}

func TestG03LSPRejectsEmptyWorkspaceBeforeServerResolution(t *testing.T) {
	ctx := NewWorkspaceContext()
	service := NewLSPServiceWithWorkspaceContext(ctx)
	err := service.StartLSPServer("go")
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("StartLSPServer without a workspace error = %v; want ErrNotAllowed", err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.servers) != 0 {
		t.Fatalf("LSP started a server with no workspace: %#v", service.servers)
	}
}

func TestG03FileRevealRejectsEmptyWorkspaceWithoutLaunching(t *testing.T) {
	file := filepath.Join(t.TempDir(), "inside.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write reveal fixture: %v", err)
	}
	service := NewFileServiceWithWorkspaceContext(NewWorkspaceContext())
	launched := false
	service.startReveal = func(*exec.Cmd) error {
		launched = true
		return nil
	}
	err := service.RevealInOS(file)
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("RevealInOS without a workspace error = %v; want ErrNotAllowed", err)
	}
	if launched {
		t.Fatal("RevealInOS invoked the external launcher with no workspace")
	}
}

func TestG03WindowOpenRejectsEmptyWorkspaceWithoutLaunching(t *testing.T) {
	path := t.TempDir()
	service := NewWindowServiceWithWorkspaceContext(NewWorkspaceContext())
	var launches int
	service.startCommand = func(string, ...string) error {
		launches++
		return nil
	}
	for _, test := range []struct {
		name string
		open func(string) error
	}{
		{name: "explorer", open: service.OpenPathInExplorer},
		{name: "vscode", open: service.OpenPathInVSCode},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.open(path)
			if !errors.Is(err, ErrNotAllowed) {
				t.Fatalf("open without a workspace error = %v; want ErrNotAllowed", err)
			}
		})
	}
	if launches != 0 {
		t.Fatalf("window service launched %d external processes with no workspace", launches)
	}
}

func TestG03SearchRejectsResultsAfterWorkspaceSwitch(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	file := filepath.Join(rootA, "match.txt")
	if err := os.WriteFile(file, []byte("needle"), 0o600); err != nil {
		t.Fatalf("write search switch fixture: %v", err)
	}
	ctx := NewWorkspaceContext()
	if err := ctx.Set(rootA); err != nil {
		t.Fatalf("set workspace A: %v", err)
	}
	service := NewSearchService()
	service.setWorkspaceContext(ctx)
	if err := service.setWorkspaceRoot(rootA); err != nil {
		t.Fatalf("set search root A: %v", err)
	}
	walkStarted := make(chan struct{})
	releaseWalk := make(chan struct{})
	service.mu.Lock()
	service.walkDir = func(root string, walkFn fs.WalkDirFunc) error {
		close(walkStarted)
		<-releaseWalk
		return filepath.WalkDir(root, walkFn)
	}
	service.mu.Unlock()
	resultCh := make(chan struct {
		results []SearchResult
		err     error
	}, 1)
	go func() {
		results, err := service.Search(rootA, "needle", false)
		resultCh <- struct {
			results []SearchResult
			err     error
		}{results: results, err: err}
	}()
	select {
	case <-walkStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("search did not enter the walk hook")
	}
	if err := ctx.Set(rootB); err != nil {
		t.Fatalf("switch workspace B: %v", err)
	}
	close(releaseWalk)
	result := <-resultCh
	if !errors.Is(result.err, ErrNotAllowed) {
		t.Fatalf("search after workspace switch error = %v, results = %#v; want ErrNotAllowed", result.err, result.results)
	}
	if len(result.results) != 0 {
		t.Fatalf("search returned stale results after workspace switch: %#v", result.results)
	}
}

func TestG03SymbolIndexRejectsPublishAfterWorkspaceSwitch(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	file := filepath.Join(rootA, "symbols.go")
	if err := os.WriteFile(file, []byte("package fixture\n\nfunc Needle() {}\n"), 0o600); err != nil {
		t.Fatalf("write symbol switch fixture: %v", err)
	}
	ctx := NewWorkspaceContext()
	if err := ctx.Set(rootA); err != nil {
		t.Fatalf("set workspace A: %v", err)
	}
	service := NewSymbolIndexServiceWithWorkspaceContext(ctx)
	service.setWorkspaceRoot(rootA)
	scanStarted := make(chan struct{})
	releaseScan := make(chan struct{})
	service.mu.Lock()
	service.scanSourceFiles = func(context.Context, string, indexBudget) ([]string, error) {
		close(scanStarted)
		<-releaseScan
		return []string{file}, nil
	}
	service.mu.Unlock()
	resultCh := make(chan struct {
		results []IndexedSymbol
		err     error
	}, 1)
	go func() {
		results, err := service.SearchSymbols(context.Background(), "needle", 10)
		resultCh <- struct {
			results []IndexedSymbol
			err     error
		}{results: results, err: err}
	}()
	select {
	case <-scanStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("symbol index did not enter the scan hook")
	}
	if err := ctx.Set(rootB); err != nil {
		t.Fatalf("switch workspace B: %v", err)
	}
	close(releaseScan)
	result := <-resultCh
	if !errors.Is(result.err, ErrNotAllowed) {
		t.Fatalf("symbol search after workspace switch error = %v, results = %#v; want ErrNotAllowed", result.err, result.results)
	}
	if len(result.results) != 0 || service.GetIndexStats().SymbolCount != 0 {
		t.Fatalf("symbol index published stale results after workspace switch: results=%#v stats=%#v", result.results, service.GetIndexStats())
	}
}

func TestG03MCPRejectsResultsAfterWorkspaceSwitch(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	ctx := NewWorkspaceContext()
	if err := ctx.Set(rootA); err != nil {
		t.Fatalf("set workspace A: %v", err)
	}
	service := newTestMCPService(t)
	service.setWorkspaceContext(ctx)
	if err := service.applyWorkspaceRoot(rootA); err != nil {
		t.Fatalf("set MCP root A: %v", err)
	}
	refreshDone := make(chan struct{})
	client := newMCPClient(MCPServerConfig{Name: "switch"})
	client.toolsRefreshDone = refreshDone
	leaseAcquired := make(chan struct{})
	var leaseOnce sync.Once
	service.onWorkspaceLeaseAcquired = func() {
		leaseOnce.Do(func() { close(leaseAcquired) })
	}
	service.mu.Lock()
	service.clients["switch"] = client
	service.mu.Unlock()
	resultCh := make(chan error, 1)
	go func() {
		_, err := service.ListTools(context.Background(), "switch")
		resultCh <- err
	}()
	select {
	case <-leaseAcquired:
	case err := <-resultCh:
		t.Fatalf("ListTools returned before the workspace switch: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("ListTools did not acquire a workspace lease")
	}
	if err := ctx.Set(rootB); err != nil {
		t.Fatalf("switch workspace B: %v", err)
	}
	client.mu.Lock()
	client.toolsCache = []MCPTool{{Name: "stale"}}
	client.toolsCachedAt = time.Now()
	client.toolsCacheValid = true
	client.mu.Unlock()
	close(refreshDone)
	err := <-resultCh
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ListTools after workspace switch error = %v; want ErrNotAllowed", err)
	}
}

func TestG03MCPRealSubprocessBindsAndRevokesWorkspaceGeneration(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	helper := filepath.Join(rootA, "g03-mcp-helper"+filepath.Ext(os.Args[0]))
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatalf("open test executable: %v", err)
	}
	destination, err := os.OpenFile(helper, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		source.Close()
		t.Fatalf("create MCP helper executable: %v", err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		source.Close()
		t.Fatalf("copy MCP helper executable: %v", err)
	}
	if err := destination.Close(); err != nil {
		source.Close()
		t.Fatalf("close MCP helper executable: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close test executable: %v", err)
	}
	marker := filepath.Join(rootA, "mcp-process-started")
	ctx := NewWorkspaceContext()
	if err := ctx.Set(rootA); err != nil {
		t.Fatalf("set workspace A: %v", err)
	}
	service := newTestMCPService(t)
	service.setWorkspaceContext(ctx)
	if err := service.applyWorkspaceRoot(rootA); err != nil {
		t.Fatalf("set MCP root A: %v", err)
	}
	service.config.Servers = []MCPServerConfig{{
		Name:      "g03-real",
		Transport: "stdio",
		Command:   helper,
		Args:      []string{"-test.run=^TestG03MCPProtocolHelperProcess$"},
		Env:       map[string]string{g03MCPHelperMarkerEnv: marker},
		Enabled:   true,
	}}
	// Windows may scan the copied test executable before CreateProcess returns;
	// keep enough budget for the real child handshake without weakening the
	// workspace-generation assertions below.
	connectCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.ConnectServer(connectCtx, "g03-real"); err != nil {
		t.Fatalf("ConnectServer real helper: %v", err)
	}
	started, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read real MCP process marker: %v", err)
	}
	if string(started) != "started\n" {
		t.Fatalf("unexpected real MCP process marker: %q", started)
	}
	tools, err := service.ListTools(context.Background(), "g03-real")
	if err != nil || len(tools) != 1 || tools[0].Name != "g03_probe" {
		t.Fatalf("ListTools real helper = %#v, %v; want g03_probe", tools, err)
	}
	service.mu.RLock()
	client := service.clients["g03-real"]
	service.mu.RUnlock()
	if client == nil {
		t.Fatal("real MCP client was not installed")
	}
	client.mu.Lock()
	transport, ok := client.transport.(*stdioTransport)
	client.mu.Unlock()
	if !ok || transport == nil || transport.cmd == nil || transport.cmd.Process == nil {
		t.Fatalf("real MCP client did not retain stdio process: %#v", transport)
	}
	pid := transport.cmd.Process.Pid
	if err := ctx.Set(rootB); err != nil {
		t.Fatalf("switch workspace B: %v", err)
	}
	if err := service.applyWorkspaceRoot(rootB); err != nil {
		t.Fatalf("apply MCP workspace B: %v", err)
	}
	if transport.cmd.ProcessState == nil {
		t.Fatal("MCP process was not reaped after workspace switch")
	}
	service.mu.RLock()
	_, stillConnected := service.clients["g03-real"]
	service.mu.RUnlock()
	if stillConnected {
		t.Fatal("MCP client survived the workspace switch")
	}
	if _, err := service.ListTools(context.Background(), "g03-real"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old MCP connection after workspace switch error = %v; want ErrNotFound", err)
	}
	t.Logf("real MCP subprocess pid=%d started and was reaped on generation switch", pid)
}

func TestG03WorkspaceLeaseSerializesProcessStartAgainstSwitch(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	ctx := NewWorkspaceContext()
	if err := ctx.Set(rootA); err != nil {
		t.Fatalf("set workspace A: %v", err)
	}
	lease, err := acquireWorkspaceLease(ctx, "", 0)
	if err != nil {
		t.Fatalf("acquire workspace lease: %v", err)
	}
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	startDone := make(chan error, 1)
	go func() {
		startDone <- lease.withCurrent(func() error {
			close(startEntered)
			<-releaseStart
			return nil
		})
	}()
	select {
	case <-startEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("process-start lease did not enter callback")
	}
	switchDone := make(chan error, 1)
	go func() { switchDone <- ctx.Set(rootB) }()
	select {
	case err := <-switchDone:
		t.Fatalf("workspace switched through an admitted process start: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseStart)
	if err := <-startDone; err != nil {
		t.Fatalf("process-start lease failed: %v", err)
	}
	if err := <-switchDone; err != nil {
		t.Fatalf("workspace switch after process start: %v", err)
	}
	if !sameWorkspaceIdentityPath(ctx.Root(), rootB) {
		t.Fatalf("workspace switch did not commit after process start: %q", ctx.Root())
	}
}

func TestG03MCPApprovalLeaseCannotSelectSameNamedClientAfterSwitch(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	ctx := NewWorkspaceContext()
	if err := ctx.Set(rootA); err != nil {
		t.Fatalf("set workspace A: %v", err)
	}
	service := newTestMCPService(t)
	service.setWorkspaceContext(ctx)
	if err := service.applyWorkspaceRoot(rootA); err != nil {
		t.Fatalf("apply MCP workspace A: %v", err)
	}
	oldTransport := newScriptedMCPTransport(func(req *jsonrpcRequest, _ int) *jsonrpcResponse {
		return &jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"content":[{"type":"text","text":"old"}]}`)}
	})
	oldClient := newScriptedMCPClient("same-name", oldTransport)
	service.mu.Lock()
	service.clients["same-name"] = oldClient
	service.mu.Unlock()
	lease, _, err := service.acquireWorkspaceLease()
	if err != nil {
		t.Fatalf("acquire approval lease: %v", err)
	}
	if err := ctx.Set(rootB); err != nil {
		t.Fatalf("switch workspace B: %v", err)
	}
	if err := service.applyWorkspaceRoot(rootB); err != nil {
		t.Fatalf("apply MCP workspace B: %v", err)
	}
	newTransport := newScriptedMCPTransport(func(req *jsonrpcRequest, _ int) *jsonrpcResponse {
		return &jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"content":[{"type":"text","text":"new"}]}`)}
	})
	newClient := newScriptedMCPClient("same-name", newTransport)
	service.mu.Lock()
	service.clients["same-name"] = newClient
	service.mu.Unlock()
	t.Cleanup(func() {
		_ = oldClient.StopServer()
		_ = newClient.StopServer()
	})
	_, err = service.callToolWithLease(context.Background(), "same-name", "echo", nil, lease)
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("old approval lease after same-name reconnect error = %v; want ErrNotAllowed", err)
	}
	newTransport.mu.Lock()
	sendCount := newTransport.sendCount
	newTransport.mu.Unlock()
	if sendCount != 0 {
		t.Fatalf("old approval lease sent %d requests to the new same-name client", sendCount)
	}
}
