package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

const agentMCPProcessHelperName = "koyori-agent-mcp-process-helper"
const agentMCPProcessHelperEnv = "KOYORI_AGENT_MCP_PROCESS_HELPER"
const agentMCPProcessExitHelperEnv = "KOYORI_AGENT_MCP_PROCESS_EXIT_HELPER"

func TestAgentMCPProcessExitHelper(t *testing.T) {
	exitCode := os.Getenv(agentMCPProcessExitHelperEnv)
	if exitCode == "" {
		t.Skip("helper only runs from the reexec process")
	}
	code, err := strconv.Atoi(exitCode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exit helper: parse code: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "exiting")
	os.Exit(code)
}

func TestStdioTransportClosePreservesUnexpectedProcessExit(t *testing.T) {
	cmd := command(os.Args[0], "-test.run=^TestAgentMCPProcessExitHelper$")
	cmd.Env = append(os.Environ(), agentMCPProcessExitHelperEnv+"=17")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("create helper stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		t.Fatalf("create helper stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		t.Fatalf("start exit helper: %v", err)
	}
	transport := &stdioTransport{
		cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), stdoutPipe: stdout,
		processDone: startMCPProcessWaiter(cmd),
	}
	line, err := transport.stdout.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "exiting" {
		_ = transport.Close()
		t.Fatalf("read exit helper readiness: line=%q err=%v", line, err)
	}
	if _, err := transport.stdout.ReadByte(); !errors.Is(err, io.EOF) {
		_ = transport.Close()
		t.Fatalf("wait for exit helper stdout EOF: %v", err)
	}

	closeErr := transport.Close()
	var exitErr *exec.ExitError
	if !errors.As(closeErr, &exitErr) || exitErr.ExitCode() != 17 {
		t.Fatalf("Close error=%v, want preserved exit status 17", closeErr)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("exit helper was not reaped: %+v", cmd.ProcessState)
	}
	secondErr := transport.Close()
	if secondErr == nil || secondErr.Error() != closeErr.Error() {
		t.Fatalf("idempotent Close error=%v, want=%v", secondErr, closeErr)
	}
}

// TestAgentMCPRealStdioHelper is re-executed from a workspace-local copy of
// the current test binary. It intentionally stays alive until MCPService
// closes the stdio transport, so the Go test harness never writes PASS to the
// protocol stdout stream.
func TestAgentMCPRealStdioHelper(t *testing.T) {
	if os.Getenv(agentMCPProcessHelperEnv) != "1" {
		t.Skip("helper only runs from the workspace-local reexec binary")
	}

	reader := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for reader.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(reader.Bytes(), &request); err != nil {
			fmt.Fprintf(os.Stderr, "real-mcp helper: decode request: %v\n", err)
			os.Exit(2)
		}
		if len(request.ID) == 0 || string(request.ID) == "null" {
			continue
		}

		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      request.ID,
		}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]interface{}{
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"protocolVersion": "2024-11-05",
				"serverInfo": map[string]string{
					"name": "real-process-test", "version": "1.0",
				},
			}
		case "tools/list":
			response["result"] = map[string]interface{}{
				"tools": []interface{}{map[string]interface{}{
					"name":        "lookup",
					"description": "Look up a catalog entry in the real helper process",
					"inputSchema": map[string]interface{}{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]interface{}{
							"query": map[string]interface{}{"type": "string", "minLength": 1},
						},
						"required": []string{"query"},
					},
				}},
			}
		case "tools/call":
			var call struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			if err := json.Unmarshal(request.Params, &call); err != nil || call.Name != "lookup" {
				response["error"] = map[string]interface{}{"code": -32602, "message": "invalid tool call"}
				break
			}
			query, _ := call.Arguments["query"].(string)
			response["result"] = map[string]interface{}{
				"content": []interface{}{map[string]interface{}{
					"type": "text",
					"text": fmt.Sprintf("real-mcp-pid=%d query=%s", os.Getpid(), query),
				}},
			}
		default:
			response["error"] = map[string]interface{}{"code": -32601, "message": "method not found"}
		}
		if err := encoder.Encode(response); err != nil {
			fmt.Fprintf(os.Stderr, "real-mcp helper: encode response: %v\n", err)
			os.Exit(3)
		}
	}
	if err := reader.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "real-mcp helper: read request: %v\n", err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func copyAgentMCPProcessHelper(t *testing.T, root string) string {
	t.Helper()
	source, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve current test binary: %v", err)
	}
	name := agentMCPProcessHelperName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	destination := filepath.Join(root, name)

	in, err := os.Open(source)
	if err != nil {
		t.Fatalf("open current test binary: %v", err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatalf("create workspace-local MCP helper: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("copy workspace-local MCP helper: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close workspace-local MCP helper: %v", err)
	}
	if err := os.Chmod(destination, 0o700); err != nil {
		t.Fatalf("mark workspace-local MCP helper executable: %v", err)
	}
	return destination
}

type blockingSingleOwnerMCPTransport struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	calls     atomic.Int32
	err       error
}

func (t *blockingSingleOwnerMCPTransport) Send(context.Context, *jsonrpcOutboundMessage) error {
	return nil
}

func (t *blockingSingleOwnerMCPTransport) Recv() (*jsonrpcResponse, error) {
	return nil, io.EOF
}

func (t *blockingSingleOwnerMCPTransport) Close() error {
	t.calls.Add(1)
	t.startOnce.Do(func() { close(t.started) })
	<-t.release
	return t.err
}

func waitForMCPConfigWriteReservation(service *MCPService, previous <-chan struct{}) bool {
	deadline := time.Now().Add(5 * time.Second)
	for {
		service.mu.RLock()
		current := service.persistTail
		service.mu.RUnlock()
		if current != nil && current != previous {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		runtime.Gosched()
	}
}

func TestMCPServiceConcurrentCloseUsesSingleTransportOwner(t *testing.T) {
	transportErr := errors.New("controlled transport close failure")
	transport := &blockingSingleOwnerMCPTransport{
		started: make(chan struct{}), release: make(chan struct{}), err: transportErr,
	}
	client := newMCPClient(MCPServerConfig{Name: "single-owner", Transport: "stdio"})
	client.mu.Lock()
	client.transport = transport
	client.run = newMCPClientRun()
	client.mu.Unlock()
	service := &MCPService{clients: map[string]*MCPClient{"single-owner": client}}

	firstDone := make(chan error, 1)
	go func() { firstDone <- service.Close() }()
	select {
	case <-transport.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first Close did not reach transport owner")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- service.Close() }()
	select {
	case err := <-secondDone:
		t.Fatalf("concurrent Close returned before the transport owner: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(transport.release)

	firstErr := <-firstDone
	secondErr := <-secondDone
	if !errors.Is(firstErr, transportErr) || !errors.Is(secondErr, transportErr) || firstErr.Error() != secondErr.Error() {
		t.Fatalf("concurrent Close results differ: first=%v second=%v", firstErr, secondErr)
	}
	if calls := transport.calls.Load(); calls != 1 {
		t.Fatalf("transport Close calls=%d, want one owner", calls)
	}
	thirdErr := service.Close()
	if !errors.Is(thirdErr, transportErr) || thirdErr.Error() != firstErr.Error() {
		t.Fatalf("idempotent Close result=%v want=%v", thirdErr, firstErr)
	}
}

func TestMCPServiceCloseWaitsForConcurrentTeardownOwner(t *testing.T) {
	tests := []struct {
		name      string
		operation func(*MCPService, string) error
	}{
		{name: "disconnect", operation: func(service *MCPService, _ string) error {
			return service.DisconnectServer("single-owner")
		}},
		{name: "delete", operation: func(service *MCPService, _ string) error {
			return service.DeleteServer("single-owner")
		}},
		{name: "disable", operation: func(service *MCPService, _ string) error {
			return service.SetServerEnabled("single-owner", false)
		}},
		{name: "workspace-switch", operation: func(service *MCPService, nextRoot string) error {
			return service.applyWorkspaceRoot(nextRoot)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			nextRoot := filepath.Join(root, "next")
			if err := os.Mkdir(nextRoot, 0o700); err != nil {
				t.Fatalf("create next workspace: %v", err)
			}
			transport := &blockingSingleOwnerMCPTransport{
				started: make(chan struct{}), release: make(chan struct{}),
			}
			client := newMCPClient(MCPServerConfig{Name: "single-owner", Transport: "stdio"})
			client.mu.Lock()
			client.transport = transport
			client.run = newMCPClientRun()
			client.mu.Unlock()
			config := MCPConfig{Servers: []MCPServerConfig{{Name: "single-owner", Transport: "stdio", Enabled: true}}}
			service := &MCPService{
				clients: map[string]*MCPClient{"single-owner": client},
				config:  config, persistedConfig: config, rootDir: root,
				persistConfig: func(MCPConfig) error { return nil },
			}

			operationDone := make(chan error, 1)
			go func() { operationDone <- test.operation(service, nextRoot) }()
			select {
			case <-transport.started:
			case <-time.After(5 * time.Second):
				t.Fatal("teardown operation did not reach transport owner")
			}
			closeDone := make(chan error, 1)
			go func() { closeDone <- service.Close() }()
			select {
			case err := <-closeDone:
				t.Fatalf("Close returned before concurrent teardown completed: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			close(transport.release)
			if err := <-operationDone; err != nil {
				t.Fatalf("teardown operation: %v", err)
			}
			if err := <-closeDone; err != nil {
				t.Fatalf("Close after teardown: %v", err)
			}
			if calls := transport.calls.Load(); calls != 1 {
				t.Fatalf("transport Close calls=%d, want one owner", calls)
			}
		})
	}
}

func TestMCPServiceTeardownReleasesLifecycleOwnerBeforeCatalogCallback(t *testing.T) {
	tests := []struct {
		name      string
		operation func(*MCPService, string) error
	}{
		{name: "disconnect", operation: func(service *MCPService, _ string) error {
			return service.DisconnectServer("single-owner")
		}},
		{name: "delete", operation: func(service *MCPService, _ string) error {
			return service.DeleteServer("single-owner")
		}},
		{name: "disable", operation: func(service *MCPService, _ string) error {
			return service.SetServerEnabled("single-owner", false)
		}},
		{name: "workspace-switch", operation: func(service *MCPService, nextRoot string) error {
			return service.applyWorkspaceRoot(nextRoot)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			nextRoot := filepath.Join(root, "next")
			if err := os.Mkdir(nextRoot, 0o700); err != nil {
				t.Fatalf("create next workspace: %v", err)
			}
			transport := &blockingSingleOwnerMCPTransport{
				started: make(chan struct{}), release: make(chan struct{}),
			}
			client := newMCPClient(MCPServerConfig{Name: "single-owner", Transport: "stdio"})
			client.mu.Lock()
			client.transport = transport
			client.run = newMCPClientRun()
			client.mu.Unlock()
			config := MCPConfig{Servers: []MCPServerConfig{{Name: "single-owner", Transport: "stdio", Enabled: true}}}
			service := &MCPService{
				clients: map[string]*MCPClient{"single-owner": client},
				config:  config, persistedConfig: config, rootDir: root,
				persistConfig: func(MCPConfig) error { return nil },
			}
			callbackStarted := make(chan struct{})
			callbackRelease := make(chan struct{})
			service.onToolsChanged = func() {
				close(callbackStarted)
				<-callbackRelease
			}

			operationDone := make(chan error, 1)
			go func() { operationDone <- test.operation(service, nextRoot) }()
			select {
			case <-transport.started:
			case <-time.After(5 * time.Second):
				t.Fatal("teardown operation did not reach transport owner")
			}
			close(transport.release)
			select {
			case <-callbackStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("catalog callback did not start")
			}

			closeDone := make(chan error, 1)
			go func() { closeDone <- service.Close() }()
			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatalf("Close while callback blocked: %v", err)
				}
			case <-time.After(200 * time.Millisecond):
				close(callbackRelease)
				<-operationDone
				<-closeDone
				t.Fatal("Close blocked behind catalog callback while lifecycle owner was held")
			}
			close(callbackRelease)
			if err := <-operationDone; err != nil {
				t.Fatalf("teardown operation: %v", err)
			}
		})
	}
}

func TestMCPServiceCloseObservesConcurrentTeardownFailure(t *testing.T) {
	tests := []struct {
		name      string
		operation func(*MCPService, string) error
	}{
		{name: "disconnect", operation: func(service *MCPService, _ string) error {
			return service.DisconnectServer("single-owner")
		}},
		{name: "delete", operation: func(service *MCPService, _ string) error {
			return service.DeleteServer("single-owner")
		}},
		{name: "disable", operation: func(service *MCPService, _ string) error {
			return service.SetServerEnabled("single-owner", false)
		}},
		{name: "workspace-switch", operation: func(service *MCPService, nextRoot string) error {
			return service.applyWorkspaceRoot(nextRoot)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			nextRoot := filepath.Join(root, "next")
			if err := os.Mkdir(nextRoot, 0o700); err != nil {
				t.Fatalf("create next workspace: %v", err)
			}
			transportErr := errors.New("controlled teardown failure")
			transport := &blockingSingleOwnerMCPTransport{
				started: make(chan struct{}), release: make(chan struct{}), err: transportErr,
			}
			client := newMCPClient(MCPServerConfig{Name: "single-owner", Transport: "stdio"})
			client.mu.Lock()
			client.transport = transport
			client.run = newMCPClientRun()
			client.mu.Unlock()
			config := MCPConfig{Servers: []MCPServerConfig{{Name: "single-owner", Transport: "stdio", Enabled: true}}}
			service := &MCPService{
				clients: map[string]*MCPClient{"single-owner": client},
				config:  config, persistedConfig: config, rootDir: root,
				persistConfig: func(MCPConfig) error { return nil },
			}

			operationDone := make(chan error, 1)
			go func() { operationDone <- test.operation(service, nextRoot) }()
			select {
			case <-transport.started:
			case <-time.After(5 * time.Second):
				t.Fatal("teardown operation did not reach transport owner")
			}
			closeDone := make(chan error, 1)
			go func() { closeDone <- service.Close() }()
			close(transport.release)

			if err := <-operationDone; !errors.Is(err, transportErr) {
				t.Fatalf("teardown error=%v, want %v", err, transportErr)
			}
			if got := service.WorkspaceRoot(); got != root {
				t.Fatalf("workspace root after failed teardown=%q, want rollback to %q", got, root)
			}
			if err := <-closeDone; !errors.Is(err, transportErr) {
				t.Fatalf("Close error=%v, want prior teardown failure %v", err, transportErr)
			}
			if calls := transport.calls.Load(); calls != 1 {
				t.Fatalf("transport Close calls=%d, want one owner", calls)
			}
		})
	}
}

func TestProjectServiceMCPTeardownFailureRollsBackRootWithoutRevivingApproval(t *testing.T) {
	root := t.TempDir()
	nextRoot := t.TempDir()
	transportErr := errors.New("controlled MCP workspace teardown failure")
	transport := &blockingSingleOwnerMCPTransport{
		started: make(chan struct{}), release: make(chan struct{}), err: transportErr,
	}
	close(transport.release)
	client := newMCPClient(MCPServerConfig{Name: "single-owner", Transport: "stdio"})
	client.mu.Lock()
	client.transport = transport
	client.run = newMCPClientRun()
	client.mu.Unlock()
	service := &MCPService{
		clients: map[string]*MCPClient{"single-owner": client},
		rootDir: root, rootGeneration: 7, lifecycleGeneration: 11,
	}
	service.approvals = map[string]mcpToolApproval{
		"before-failed-switch": {
			server: "single-owner", tool: "lookup", argsJSON: `{"query":"old"}`,
			rootGeneration: 7, lifecycleGeneration: 11, expiresAt: time.Now().Add(time.Minute),
		},
	}
	project := &ProjectService{
		configPath: filepath.Join(t.TempDir(), "projects.json"), mcpService: service,
	}

	if _, err := project.AddProject(nextRoot); !errors.Is(err, transportErr) {
		t.Fatalf("AddProject error=%v, want MCP teardown failure %v", err, transportErr)
	}
	if got := service.WorkspaceRoot(); got != root {
		t.Fatalf("MCP root after failed project switch=%q, want %q", got, root)
	}
	service.mu.RLock()
	rootGeneration := service.rootGeneration
	lifecycleGeneration := service.lifecycleGeneration
	_, clientStillInstalled := service.clients["single-owner"]
	service.mu.RUnlock()
	if rootGeneration <= 8 || lifecycleGeneration <= 12 {
		t.Fatalf("failed switch did not burn both generations: root=%d lifecycle=%d", rootGeneration, lifecycleGeneration)
	}
	if clientStillInstalled {
		t.Fatal("failed project switch revived the stopped MCP client")
	}
	if _, err := service.executeApprovedToolLegacy(
		context.Background(), "single-owner", "lookup", map[string]interface{}{"query": "old"}, "before-failed-switch",
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("old MCP approval after rollback error=%v, want ErrInvalidInput", err)
	}
	if err := service.Close(); !errors.Is(err, transportErr) {
		t.Fatalf("Close error=%v, want retained teardown failure %v", err, transportErr)
	}
}

func TestMCPWorkspaceRollbackSerializesConcurrentConfigSave(t *testing.T) {
	root := t.TempDir()
	nextRoot := t.TempDir()
	commandPath := filepath.Join(nextRoot, "transient-workspace-command")
	if err := os.WriteFile(commandPath, []byte("test"), 0o700); err != nil {
		t.Fatalf("create transient workspace command: %v", err)
	}
	transportErr := errors.New("controlled MCP workspace teardown failure")
	transport := &blockingSingleOwnerMCPTransport{
		started: make(chan struct{}), release: make(chan struct{}), err: transportErr,
	}
	client := newMCPClient(MCPServerConfig{Name: "single-owner", Transport: "stdio"})
	client.mu.Lock()
	client.transport = transport
	client.run = newMCPClientRun()
	client.mu.Unlock()
	persisted := make(chan struct{})
	service := &MCPService{
		clients: map[string]*MCPClient{"single-owner": client}, rootDir: root,
		persistConfig: func(MCPConfig) error {
			close(persisted)
			return nil
		},
	}

	switchDone := make(chan error, 1)
	go func() { switchDone <- service.applyWorkspaceRoot(nextRoot) }()
	select {
	case <-transport.started:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace switch did not reach blocking teardown")
	}
	service.mu.RLock()
	workspaceSwitchSlot := service.persistTail
	service.mu.RUnlock()
	saveDone := make(chan error, 1)
	go func() {
		saveDone <- service.SaveServer(MCPServerConfig{
			Name: "transient", Transport: "stdio", Command: commandPath,
		})
	}()
	if !waitForMCPConfigWriteReservation(service, workspaceSwitchSlot) {
		close(transport.release)
		<-switchDone
		<-saveDone
		t.Fatal("concurrent SaveServer did not reserve its persistence slot")
	}
	select {
	case <-persisted:
		close(transport.release)
		<-switchDone
		<-saveDone
		t.Fatal("SaveServer persisted config against a transient workspace root")
	case err := <-saveDone:
		close(transport.release)
		<-switchDone
		t.Fatalf("SaveServer returned before workspace rollback completed: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(transport.release)
	if err := <-switchDone; !errors.Is(err, transportErr) {
		t.Fatalf("workspace switch error=%v, want %v", err, transportErr)
	}
	if err := <-saveDone; err == nil {
		t.Fatal("SaveServer accepted command outside the restored workspace")
	}
	select {
	case <-persisted:
		t.Fatal("SaveServer persisted after the workspace rollback rejected its command")
	default:
	}
	if got := service.WorkspaceRoot(); got != root {
		t.Fatalf("workspace root after rollback=%q, want %q", got, root)
	}
	if err := service.Close(); !errors.Is(err, transportErr) {
		t.Fatalf("Close error=%v, want retained teardown failure %v", err, transportErr)
	}
}

func TestMCPTeardownKeepsConfigPersistenceSlotUntilTransportStops(t *testing.T) {
	for _, operation := range []string{"disable", "delete"} {
		t.Run(operation, func(t *testing.T) {
			transport := &blockingSingleOwnerMCPTransport{
				started: make(chan struct{}), release: make(chan struct{}),
			}
			client := newMCPClient(MCPServerConfig{Name: "single-owner", Transport: "stdio"})
			client.mu.Lock()
			client.transport = transport
			client.run = newMCPClientRun()
			client.mu.Unlock()
			service := &MCPService{
				clients: map[string]*MCPClient{"single-owner": client},
				config: MCPConfig{Servers: []MCPServerConfig{{
					Name: "single-owner", Transport: "stdio", Enabled: true,
				}}},
			}
			persisted := make(chan MCPConfig, 4)
			service.persistConfig = func(candidate MCPConfig) error {
				persisted <- candidate
				return nil
			}

			operationDone := make(chan error, 1)
			go func() {
				if operation == "disable" {
					operationDone <- service.SetServerEnabled("single-owner", false)
				} else {
					operationDone <- service.DeleteServer("single-owner")
				}
			}()
			select {
			case <-transport.started:
			case <-time.After(5 * time.Second):
				t.Fatal("teardown did not reach transport owner")
			}
			select {
			case <-persisted:
			case <-time.After(time.Second):
				t.Fatal("teardown did not persist its own config snapshot")
			}
			service.mu.RLock()
			teardownSlot := service.persistTail
			service.mu.RUnlock()

			saveDone := make(chan error, 1)
			go func() {
				saveDone <- service.SaveServer(MCPServerConfig{
					Name: "queued", Transport: "stdio", Command: "queued-command",
				})
			}()
			if !waitForMCPConfigWriteReservation(service, teardownSlot) {
				close(transport.release)
				<-operationDone
				<-saveDone
				t.Fatal("concurrent SaveServer did not reserve its persistence slot")
			}
			select {
			case <-persisted:
				close(transport.release)
				<-operationDone
				<-saveDone
				t.Fatal("SaveServer persisted while transport teardown was still blocked")
			case <-time.After(200 * time.Millisecond):
			}

			close(transport.release)
			if err := <-operationDone; err != nil {
				t.Fatalf("%s: %v", operation, err)
			}
			if err := <-saveDone; err != nil {
				t.Fatalf("queued SaveServer: %v", err)
			}
			select {
			case <-persisted:
			case <-time.After(time.Second):
				t.Fatal("queued SaveServer did not persist after teardown completed")
			}
			if calls := transport.calls.Load(); calls != 1 {
				t.Fatalf("transport Close calls=%d, want one owner", calls)
			}
		})
	}
}

func TestTaskServiceWorkflowMCPRealStdioProcessUsesUnifiedPipeline(t *testing.T) {
	agent, file, search, root := newExecutionCoreTestServices(t)
	t.Setenv(agentMCPProcessHelperEnv, "1")
	helperPath := copyAgentMCPProcessHelper(t, root)

	mcp := newTestMCPService(t)
	mcp.setWorkspaceContext(agent.workspaceContext)
	if err := mcp.setWorkspaceRoot(root); err != nil {
		t.Fatalf("set MCP workspace root: %v", err)
	}
	mcpClosed := false
	t.Cleanup(func() {
		if !mcpClosed {
			if err := mcp.Close(); err != nil {
				t.Errorf("close MCP service: %v", err)
			}
		}
	})
	server := MCPServerConfig{
		Name: "real-process", Transport: "stdio", Command: helperPath,
		Args: []string{"-test.run=^TestAgentMCPRealStdioHelper$", "-test.timeout=60s"},
		Env:  map[string]string{agentMCPProcessHelperEnv: "1"},
	}
	if err := mcp.SaveServer(server); err != nil {
		t.Fatalf("save real-process MCP server: %v", err)
	}
	if err := mcp.SetServerEnabled(server.Name, true); err != nil {
		t.Fatalf("enable real-process MCP server: %v", err)
	}
	// Windows may spend tens of seconds scanning a copied Go test binary before
	// CreateProcess returns; keep this integration deadline separate from the
	// normal request timeout so startup latency is not misreported as teardown.
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelConnect()
	if err := mcp.ConnectServer(connectCtx, server.Name); err != nil {
		t.Fatalf("connect real-process MCP server: %v", err)
	}
	mcp.mu.RLock()
	connectedClient := mcp.clients[server.Name]
	mcp.mu.RUnlock()
	if connectedClient == nil {
		t.Fatal("real-process MCP client was not installed")
	}
	connectedClient.mu.Lock()
	stdio, ok := connectedClient.transport.(*stdioTransport)
	var helperCommand *exec.Cmd
	if ok && stdio != nil {
		helperCommand = stdio.cmd
	}
	connectedClient.mu.Unlock()
	if !ok || helperCommand == nil {
		t.Fatalf("real-process transport = %T, want stdio", connectedClient.transport)
	}

	approvals := 0
	mcp.approveTool = func(gotServer, tool, arguments string, risk RiskLevel) bool {
		approvals++
		return gotServer == server.Name && tool == "lookup" &&
			strings.Contains(arguments, "catalog-query") && risk == RiskElevated
	}
	if err := WireAgentExecutionCore(agent, file, search, mcp, nil, nil); err != nil {
		t.Fatalf("wire real-process MCP into Agent core: %v", err)
	}

	workflow := NewWorkflowService()
	if err := WireAgentWorkflowTools(agent, workflow); err != nil {
		t.Fatalf("wire workflow tools: %v", err)
	}
	definition := &WorkflowDef{
		Name: "real-mcp-workflow",
		Steps: []WorkflowStep{{
			Name: "lookup", Type: WorkflowStepMCP, Tool: "mcp.real-process.lookup",
			Input: map[string]interface{}{"query": "catalog-query"},
		}},
	}
	if err := workflow.CreateWorkflow(root, definition.Name, definition); err != nil {
		t.Fatalf("create real-process MCP workflow: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("get Agent catalog: %v", err)
	}
	var catalogToolID string
	for _, tool := range catalog.Tools {
		if tool.Metadata["workflow"] == definition.Name && tool.Metadata["step"] == "lookup" {
			catalogToolID = tool.ID
			if tool.Metadata["adapter"] != workflowAdapterMCP || tool.Mutation != string(agentcore.MutationExternal) {
				t.Fatalf("real-process catalog ToolDef contract = %+v", tool)
			}
		}
	}
	if catalogToolID == "" {
		t.Fatalf("real-process MCP ToolDef missing from catalog: %+v", catalog.Tools)
	}
	catalogRevision := catalog.Revision

	permissionDir := t.TempDir()
	permission := NewAIPermissionService(permissionDir)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission,
	)
	if err != nil {
		t.Fatalf("wire Agent lifecycle: %v", err)
	}
	task := NewTaskService(agent)
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("wire TaskService lifecycle: %v", err)
	}

	sessionID, err := task.BeginWorkflowExecution(trustedTaskContext(), definition.Name)
	if err != nil {
		t.Fatalf("begin MCP workflow execution: %v", err)
	}
	defer func() { _ = task.FailWorkflowExecution(trustedTaskContext(), sessionID, "test cleanup") }()
	approvalToken, err := task.RequestWorkflowStepApproval(trustedTaskContext(), sessionID, definition.Name, "lookup")
	if err != nil {
		t.Fatalf("request MCP workflow approval: %v", err)
	}
	task.mu.Lock()
	approval := task.approvals[approvalToken]
	task.mu.Unlock()
	if approval.capability.ToolID != catalogToolID || approval.capability.CatalogRevision != catalogRevision {
		t.Fatalf("approval drifted from catalog: tool=%q revision=%d want tool=%q revision=%d", approval.capability.ToolID, approval.capability.CatalogRevision, catalogToolID, catalogRevision)
	}
	result, err := task.ExecuteApprovedWorkflowStep(trustedTaskContext(), sessionID, definition.Name, "lookup", approvalToken)
	if err != nil {
		t.Fatalf("execute MCP workflow step: %v", err)
	}
	if approvals != 1 || result.Blocked || result.ExitCode != 0 {
		t.Fatalf("real-process MCP result=%+v approvals=%d", result, approvals)
	}

	var observed MCPToolResult
	if err := json.Unmarshal([]byte(result.Stdout), &observed); err != nil {
		t.Fatalf("decode MCP observation %q: %v", result.Stdout, err)
	}
	if len(observed.Content) != 1 {
		t.Fatalf("MCP observation content=%+v", observed.Content)
	}
	parts := strings.Fields(observed.Content[0].Text)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "real-mcp-pid=") || parts[1] != "query=catalog-query" {
		t.Fatalf("MCP process observation=%q", observed.Content[0].Text)
	}
	helperPID, err := strconv.Atoi(strings.TrimPrefix(parts[0], "real-mcp-pid="))
	if err != nil || helperPID <= 0 || helperPID == os.Getpid() {
		t.Fatalf("MCP helper PID=%d err=%v parent=%d", helperPID, err, os.Getpid())
	}

	var externalUsage []UsageRecord
	for _, record := range permission.usageRecordsSnapshot() {
		if record.ExternalReceiptID != "" {
			externalUsage = append(externalUsage, record)
		}
	}
	if len(externalUsage) != 1 {
		t.Fatalf("external MCP usage rows=%+v", externalUsage)
	}
	receipt := externalUsage[0]
	if receipt.SessionID != sessionID || receipt.UnitKind != string(agentcore.UsageUnitTool) ||
		receipt.Operation != AIOperation(catalogToolID) || receipt.Pending || !receipt.Success ||
		receipt.ExternalReceiptReversible || receipt.ExternalCompensation != agentcore.ExternalCompensationNotNeeded {
		t.Fatalf("terminal MCP usage receipt=%+v", receipt)
	}
	reloadedPermission := NewAIPermissionService(permissionDir)
	var reloaded []UsageRecord
	for _, record := range reloadedPermission.usageRecordsSnapshot() {
		if record.ExternalReceiptID != "" {
			reloaded = append(reloaded, record)
		}
	}
	if len(reloaded) != 1 || reloaded[0].UnitID != receipt.UnitID || reloaded[0].ExternalReceiptID != receipt.ExternalReceiptID ||
		reloaded[0].Operation != receipt.Operation || reloaded[0].Pending {
		t.Fatalf("reloaded MCP usage receipt=%+v want=%+v", reloaded, receipt)
	}
	if strings.Contains(result.Stdout, helperPath) || strings.Contains(result.Stdout, root) {
		t.Fatalf("MCP observation leaked workspace path: %q", result.Stdout)
	}
	if err := task.CompleteWorkflowExecution(trustedTaskContext(), sessionID); err != nil {
		t.Fatalf("complete MCP workflow execution: %v", err)
	}

	if err := mcp.Close(); err != nil {
		mcpClosed = true
		t.Fatalf("close MCP service: %v", err)
	}
	mcpClosed = true
	if helperCommand.ProcessState == nil || helperCommand.ProcessState.Pid() != helperPID || helperCommand.ProcessState.Success() {
		t.Fatalf("MCP helper process was not reaped with the expected forced status: state=%v pid=%d", helperCommand.ProcessState, helperPID)
	}
	if err := mcp.Close(); err != nil {
		t.Fatalf("second Close should be idempotent: %v", err)
	}
}
