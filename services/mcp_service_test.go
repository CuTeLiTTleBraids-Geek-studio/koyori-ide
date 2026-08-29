package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Plan 11 Task 4 Step 11 — MCP service tests.
//
// These tests cover:
//   - ClassifyMCPToolRisk risk classification (Step 8 / G-SEC-02).
//   - MCPConfig persistence: SaveServer/DeleteServer/ListServers/GetServer (Step 9).
//   - G-SEC-12: new servers default to Enabled=false.
//   - Invalid transport rejection.
//   - atomicWriteJSON 0600 permissions (Step 9).
//   - Agent CheckCommand for mcp.* namespace (Step 6/8).
//   - CallMCPTool namespace parsing (Step 6).
//
// Integration tests (actually connecting to an MCP server over stdio/SSE/HTTP)
// are not included here because they require a live MCP server process, which
// is not available in the test environment. The transport implementations are
// covered by the compile-time interface check + manual testing.

type mcpRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f mcpRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// ---------------------------------------------------------------------------
// ClassifyMCPToolRisk (Step 8 / G-SEC-02)
// ---------------------------------------------------------------------------

func TestClassifyMCPToolRisk_DefaultElevated(t *testing.T) {
	// A tool with a benign name/description defaults to RiskElevated.
	got := ClassifyMCPToolRisk("search", "Search the documentation")
	if got != RiskElevated {
		t.Errorf("expected RiskElevated for benign tool, got %s", got)
	}
}

func TestClassifyMCPToolRisk_DangerousWrite(t *testing.T) {
	cases := []struct {
		name string
		desc string
	}{
		{"write_file", "Write content to a file"},
		{"delete_file", "Delete a file from disk"},
		{"run_command", "Execute a shell command"},
		{"fetch_url", "Fetch a URL from the network"},
		{"create_dir", "Create a directory"},
		{"exec_script", "Run a script"},
		{"upload_file", "Upload a file to a server"},
		{"download_file", "Download a file"},
	}
	for _, c := range cases {
		got := ClassifyMCPToolRisk(c.name, c.desc)
		if got != RiskDangerous {
			t.Errorf("expected RiskDangerous for %q (%s), got %s", c.name, c.desc, got)
		}
	}
}

// ---------------------------------------------------------------------------
// MCPConfig persistence (Step 9)
// ---------------------------------------------------------------------------

// newTestMCPService creates an MCPService with its config in a temp dir.
func newTestMCPService(t *testing.T) *MCPService {
	t.Helper()
	dir := t.TempDir()
	s := &MCPService{
		cfgPath: filepath.Join(dir, "mcp-servers.json"),
		clients: make(map[string]*MCPClient),
		// Tests model the native consent boundary explicitly. Production
		// construction installs the real dialog callback; a zero-value service
		// must remain fail-closed.
		approveServer: func(MCPServerConfig) bool { return true },
	}
	return s
}

func TestMCPService_SetServerEnabled_RequiresNativeApproval(t *testing.T) {
	s := newTestMCPService(t)
	if err := s.SaveServer(MCPServerConfig{Name: "srv", Transport: "stdio", Command: os.Args[0]}); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	s.approveServer = nil
	if err := s.SetServerEnabled("srv", true); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("SetServerEnabled without native approval = %v, want ErrNotAllowed", err)
	}
	got, err := s.GetServer("srv")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if got.Enabled {
		t.Fatal("server became enabled without native approval")
	}
}

func TestMCPService_SetServerEnabled_DeniedNativeApprovalDoesNotPersist(t *testing.T) {
	s := newTestMCPService(t)
	if err := s.SaveServer(MCPServerConfig{Name: "srv", Transport: "stdio", Command: os.Args[0]}); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	var approved MCPServerConfig
	s.approveServer = func(cfg MCPServerConfig) bool {
		approved = cfg
		return false
	}
	if err := s.SetServerEnabled("srv", true); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("SetServerEnabled denied = %v, want ErrNotAllowed", err)
	}
	if approved.Name != "srv" || !filepath.IsAbs(approved.Command) || !sameWorkspaceIdentityPath(approved.Command, os.Args[0]) {
		t.Fatalf("native approval saw wrong identity: %+v", approved)
	}
	got, err := s.GetServer("srv")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if got.Enabled {
		t.Fatal("denied server became enabled")
	}
}

func TestMCPService_ConnectServerRejectsNilWorkspaceContextBeforeStdioLaunch(t *testing.T) {
	service := newTestMCPService(t)
	if err := service.SaveServer(MCPServerConfig{
		Name: "path-bypass", Transport: "stdio", Command: os.Args[0],
	}); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	service.approveServer = func(MCPServerConfig) bool { return true }
	if err := service.SetServerEnabled("path-bypass", true); err != nil {
		t.Fatalf("SetServerEnabled: %v", err)
	}

	// The zero-value fixture has neither a shared workspace context nor a
	// fallback root. A renderer-visible MCP call must fail closed instead of
	// resolving the command through PATH.
	if _, _, err := service.acquireWorkspaceLease(); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("acquireWorkspaceLease without workspace context = %v, want ErrNotAllowed", err)
	}
	if err := service.ConnectServer(context.Background(), "path-bypass"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ConnectServer without workspace context = %v, want ErrNotAllowed", err)
	}
	service.mu.RLock()
	connected := len(service.clients)
	service.mu.RUnlock()
	if connected != 0 {
		t.Fatalf("clients after nil-context rejection = %d, want 0", connected)
	}
}

func TestMCPService_LoadDoesNotRevivePersistedNativeApproval(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp-servers.json")
	service := &MCPService{
		cfgPath:       cfgPath,
		clients:       make(map[string]*MCPClient),
		approveServer: func(MCPServerConfig) bool { return true },
	}
	if err := service.SaveServer(MCPServerConfig{Name: "srv", Transport: "stdio", Command: os.Args[0]}); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	if err := service.SetServerEnabled("srv", true); err != nil {
		t.Fatalf("SetServerEnabled: %v", err)
	}
	disk, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var persisted MCPConfig
	if err := json.Unmarshal(disk, &persisted); err != nil {
		t.Fatalf("decode persisted config: %v", err)
	}
	if len(persisted.Servers) != 1 || persisted.Servers[0].Enabled {
		t.Fatalf("disk persisted native authority: %+v", persisted.Servers)
	}

	reloaded := &MCPService{
		cfgPath: cfgPath,
		clients: make(map[string]*MCPClient),
	}
	if err := reloaded.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	got, err := reloaded.GetServer("srv")
	if err != nil {
		t.Fatalf("GetServer after reload: %v", err)
	}
	if got.Enabled {
		t.Fatal("persisted native approval revived after reload")
	}
	if err := reloaded.ConnectServer(context.Background(), "srv"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ConnectServer after reload = %v, want ErrUnauthorized", err)
	}
}

func TestMCPService_WorkspaceSwitchRevokesNativeApproval(t *testing.T) {
	s := newTestMCPService(t)
	if err := s.SaveServer(MCPServerConfig{Name: "srv", Transport: "stdio", Command: os.Args[0]}); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	if err := s.SetServerEnabled("srv", true); err != nil {
		t.Fatalf("SetServerEnabled: %v", err)
	}
	if err := s.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatalf("setWorkspaceRoot: %v", err)
	}
	got, err := s.GetServer("srv")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if got.Enabled {
		t.Fatal("workspace switch retained native activation")
	}
}

func TestMCPAuditEscapesUntrustedFields(t *testing.T) {
	auditFile, err := os.CreateTemp(t.TempDir(), "mcp-audit-*.log")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer func() {
		if err := auditFile.Close(); err != nil {
			t.Errorf("close audit log: %v", err)
		}
	}()

	svc := &MCPService{auditLog: auditFile}
	svc.audit("call_tool", "server\nforged", "tool\tname\ncall_tool_failed")
	if err := auditFile.Sync(); err != nil {
		t.Fatalf("sync audit log: %v", err)
	}
	data, err := os.ReadFile(auditFile.Name())
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	line := string(data)
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("untrusted fields injected additional audit lines: %q", line)
	}
	if !strings.Contains(line, `server="server\nforged"`) {
		t.Errorf("server field was not escaped: %q", line)
	}
	if !strings.Contains(line, `tool="tool\tname\ncall_tool_failed"`) {
		t.Errorf("tool field was not escaped: %q", line)
	}
}

type blockingMCPAuditLog struct {
	writeStarted chan struct{}
	allowWrite   chan struct{}
	closed       chan struct{}
	startOnce    sync.Once
	releaseOnce  sync.Once
	closeOnce    sync.Once
}

func newBlockingMCPAuditLog() *blockingMCPAuditLog {
	return &blockingMCPAuditLog{
		writeStarted: make(chan struct{}),
		allowWrite:   make(chan struct{}),
		closed:       make(chan struct{}),
	}
}

func (l *blockingMCPAuditLog) WriteString(value string) (int, error) {
	l.startOnce.Do(func() { close(l.writeStarted) })
	<-l.allowWrite
	return len(value), nil
}

func (l *blockingMCPAuditLog) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *blockingMCPAuditLog) unblock() {
	l.releaseOnce.Do(func() { close(l.allowWrite) })
}

func TestMCPAuditCloseWaitsForInflightWrite(t *testing.T) {
	auditLog := newBlockingMCPAuditLog()
	t.Cleanup(auditLog.unblock)
	svc := &MCPService{auditLog: auditLog, clients: make(map[string]*MCPClient)}

	auditDone := make(chan struct{})
	go func() {
		svc.audit("call_tool", "server", "tool")
		close(auditDone)
	}()
	select {
	case <-auditLog.writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("audit write did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- svc.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before the in-flight audit completed: %v", err)
	case <-auditLog.closed:
		t.Fatal("audit log closed before the in-flight write completed")
	case <-time.After(50 * time.Millisecond):
	}

	auditLog.unblock()
	select {
	case <-auditDone:
	case <-time.After(5 * time.Second):
		t.Fatal("audit write did not finish")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after the audit write")
	}
	select {
	case <-auditLog.closed:
	default:
		t.Fatal("audit log was not closed")
	}
}

func TestMCPServiceCloseWaitsForAdmittedToolCallBeforeAuditClose(t *testing.T) {
	auditLog := newBlockingMCPAuditLog()
	auditLog.unblock()
	svc := &MCPService{auditLog: auditLog, clients: make(map[string]*MCPClient)}

	_, finishCall, err := svc.beginToolCall("not-connected")
	if !errors.Is(err, ErrNotFound) || finishCall == nil {
		t.Fatalf("beginToolCall error=%v finish=%v, want ErrNotFound with release", err, finishCall != nil)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- svc.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before admitted tool call finished: %v", err)
	case <-auditLog.closed:
		t.Fatal("audit log closed before admitted tool call finished")
	case <-time.After(50 * time.Millisecond):
	}

	finishCall()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after admitted tool call released")
	}
	select {
	case <-auditLog.closed:
	default:
		t.Fatal("audit log was not closed")
	}
}

func TestMCPServiceCloseWaitsForDisconnectBeforeAuditClose(t *testing.T) {
	transport := newB1BlockingCloseTransport()
	client := clientWithBlockingClose(transport)
	auditLog := newBlockingMCPAuditLog()
	auditLog.unblock()
	svc := &MCPService{auditLog: auditLog, clients: map[string]*MCPClient{"disconnect": client}}

	disconnectDone := make(chan error, 1)
	go func() { disconnectDone <- svc.DisconnectServer("disconnect") }()
	select {
	case <-transport.closeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("DisconnectServer did not start closing the transport")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- svc.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before DisconnectServer finished: %v", err)
	case <-auditLog.closed:
		t.Fatal("audit log closed before DisconnectServer finished")
	case <-time.After(50 * time.Millisecond):
	}

	transport.release()
	select {
	case err := <-disconnectDone:
		if err != nil {
			t.Fatalf("DisconnectServer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DisconnectServer did not finish after transport release")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after DisconnectServer")
	}
}

func TestMCPService_SaveServer_NewDefaultsDisabled(t *testing.T) {
	s := newTestMCPService(t)
	// G-SEC-12: new servers must default to Enabled=false even if the caller
	// sets Enabled=true — activation requires explicit re-save after review.
	err := s.SaveServer(MCPServerConfig{
		Name:      "test-server",
		Transport: "stdio",
		Command:   "echo",
		Enabled:   true, // should be ignored for new servers
	})
	if err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	got, err := s.GetServer("test-server")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if got.Enabled {
		t.Error("G-SEC-12: new server should default to Enabled=false")
	}
}

func TestMCPService_SaveServer_InvalidTransport(t *testing.T) {
	s := newTestMCPService(t)
	err := s.SaveServer(MCPServerConfig{
		Name:      "bad",
		Transport: "ftp", // unsupported
	})
	if err == nil {
		t.Fatal("expected error for invalid transport")
	}
}

func TestMCPService_SaveServer_EmptyName(t *testing.T) {
	s := newTestMCPService(t)
	err := s.SaveServer(MCPServerConfig{
		Name:      "",
		Transport: "stdio",
	})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestMCPService_SaveServer_RejectsUnsafeConfigFields(t *testing.T) {
	tests := []struct {
		name string
		cfg  MCPServerConfig
	}{
		{name: "empty stdio command", cfg: MCPServerConfig{Name: "empty-command", Transport: "stdio"}},
		{
			name: "NUL environment value",
			cfg: MCPServerConfig{
				Name: "nul-env", Transport: "stdio", Command: "helper",
				Env: map[string]string{"SAFE": "bad\x00value"},
			},
		},
		{
			name: "invalid header name",
			cfg: MCPServerConfig{
				Name: "bad-header", Transport: "http", URL: "https://203.0.113.1",
				Headers: map[string]string{"Bad Header": "value"},
			},
		},
		{
			name: "NUL header value",
			cfg: MCPServerConfig{
				Name: "nul-header", Transport: "http", URL: "https://203.0.113.1",
				Headers: map[string]string{"X-Test": "bad\x00value"},
			},
		},
		{name: "control character in name", cfg: MCPServerConfig{Name: "bad\nname", Transport: "stdio", Command: "helper"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := newTestMCPService(t).SaveServer(test.cfg); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("SaveServer error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestMCPService_LoadRejectsUnsafeConfigFields(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp-servers.json")
	if err := os.WriteFile(cfgPath, []byte(`{"servers":[{"name":"bad","transport":"stdio","command":""}]}`), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}
	service := &MCPService{cfgPath: cfgPath, clients: make(map[string]*MCPClient)}
	if err := service.load(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("load error = %v, want ErrInvalidInput", err)
	}
	if got := service.ListServers(); len(got) != 0 {
		t.Fatalf("invalid config was installed: %#v", got)
	}
}

func TestMCPTransportRejectsRedirectFollowing(t *testing.T) {
	for _, test := range []struct {
		name      string
		transport *http.Client
	}{
		{name: "http", transport: newHTTPTransport(MCPServerConfig{Transport: "http", URL: "https://203.0.113.1"}).client},
		{name: "sse", transport: newSSETransport(MCPServerConfig{Transport: "sse", URL: "https://203.0.113.1"}).client},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.transport.CheckRedirect == nil {
				t.Fatal("MCP HTTP client follows redirects by default")
			}
			if err := test.transport.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
				t.Fatalf("CheckRedirect error = %v, want http.ErrUseLastResponse", err)
			}
		})
	}
}

func TestMCPService_SaveServer_UpdateRevokesEnabledForNewConnectionIdentity(t *testing.T) {
	s := newTestMCPService(t)
	// Create a new server (defaults to disabled).
	if err := s.SaveServer(MCPServerConfig{
		Name: "srv", Transport: "stdio", Command: "echo",
	}); err != nil {
		t.Fatal(err)
	}
	// Manually enable it (simulating user approval).
	s.mu.Lock()
	s.config.Servers[0].Enabled = true
	s.mu.Unlock()
	// Changing connection arguments creates a new endpoint identity and must
	// revoke the existing approval.
	if err := s.SaveServer(MCPServerConfig{
		Name: "srv", Transport: "stdio", Command: "echo", Args: []string{"hi"},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetServer("srv")
	if got.Enabled {
		t.Error("connection config update should revoke Enabled")
	}
	if len(got.Args) != 1 || got.Args[0] != "hi" {
		t.Errorf("update should set Args, got %v", got.Args)
	}
}

func TestMCPService_SaveServer_EquivalentRoundTripPreservesEnabled(t *testing.T) {
	s := newTestMCPService(t)
	server := MCPServerConfig{Name: "srv", Transport: "stdio", Command: os.Args[0]}
	if err := s.SaveServer(server); err != nil {
		t.Fatal(err)
	}
	if err := s.SetServerEnabled(server.Name, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveServer(server); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetServer(server.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Error("equivalent config round-trip revoked Enabled")
	}
}

func TestMCPService_DeleteServer(t *testing.T) {
	s := newTestMCPService(t)
	s.SaveServer(MCPServerConfig{Name: "a", Transport: "stdio", Command: "echo"})
	s.SaveServer(MCPServerConfig{Name: "b", Transport: "http", URL: "https://203.0.113.1"}) // C-1: public TEST-NET-3 IP
	if len(s.ListServers()) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(s.ListServers()))
	}
	if err := s.DeleteServer("a"); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	servers := s.ListServers()
	if len(servers) != 1 || servers[0].Name != "b" {
		t.Errorf("expected only b remaining, got %v", servers)
	}
}

func TestMCPService_DeleteServer_NotFound(t *testing.T) {
	s := newTestMCPService(t)
	err := s.DeleteServer("nope")
	if err == nil {
		t.Fatal("expected error for missing server")
	}
}

type trackingCloseMCPTransport struct {
	mu         sync.Mutex
	closeCount int
}

func (t *trackingCloseMCPTransport) Send(context.Context, *jsonrpcOutboundMessage) error {
	return nil
}

func (t *trackingCloseMCPTransport) Recv() (*jsonrpcResponse, error) {
	return nil, io.EOF
}

func (t *trackingCloseMCPTransport) Close() error {
	t.mu.Lock()
	t.closeCount++
	t.mu.Unlock()
	return nil
}

func (t *trackingCloseMCPTransport) closes() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closeCount
}

type mcpRuntimeSnapshot struct {
	config              MCPConfig
	persistedConfig     MCPConfig
	lifecycleGeneration uint64
	clients             map[string]*MCPClient
}

func snapshotMCPRuntime(service *MCPService) mcpRuntimeSnapshot {
	service.mu.RLock()
	defer service.mu.RUnlock()
	clients := make(map[string]*MCPClient, len(service.clients))
	for name, client := range service.clients {
		clients[name] = client
	}
	return mcpRuntimeSnapshot{
		config:              MCPConfig{Servers: cloneMCPServerConfigs(service.config.Servers)},
		persistedConfig:     MCPConfig{Servers: cloneMCPServerConfigs(service.persistedConfig.Servers)},
		lifecycleGeneration: service.lifecycleGeneration,
		clients:             clients,
	}
}

func assertMCPRuntimeSnapshot(t *testing.T, service *MCPService, want mcpRuntimeSnapshot) {
	t.Helper()
	got := snapshotMCPRuntime(service)
	if !reflect.DeepEqual(got.config, want.config) {
		t.Fatalf("MCP config changed: got %+v, want %+v", got.config, want.config)
	}
	if !reflect.DeepEqual(got.persistedConfig, want.persistedConfig) {
		t.Fatalf("MCP persisted config changed: got %+v, want %+v", got.persistedConfig, want.persistedConfig)
	}
	if got.lifecycleGeneration != want.lifecycleGeneration {
		t.Fatalf("lifecycle generation = %d, want %d", got.lifecycleGeneration, want.lifecycleGeneration)
	}
	if !reflect.DeepEqual(got.clients, want.clients) {
		t.Fatalf("MCP clients changed: got %v, want %v", got.clients, want.clients)
	}
}

func newMCPPersistenceFailureFixture(t *testing.T) (*MCPService, *trackingCloseMCPTransport) {
	t.Helper()
	service := newTestMCPService(t)
	if err := service.SaveServer(MCPServerConfig{Name: "srv", Transport: "stdio", Command: os.Args[0]}); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	if err := service.SetServerEnabled("srv", true); err != nil {
		t.Fatalf("SetServerEnabled: %v", err)
	}

	transport := &trackingCloseMCPTransport{}
	client := newMCPClient(MCPServerConfig{Name: "srv", Transport: "stdio"})
	client.mu.Lock()
	client.transport = transport
	client.run = newMCPClientRun()
	client.mu.Unlock()
	service.mu.Lock()
	service.clients["srv"] = client
	service.mu.Unlock()
	return service, transport
}

func TestMCPServicePersistenceFailureLeavesRuntimeStateUnchanged(t *testing.T) {
	persistErr := errors.New("injected MCP persistence failure")
	tests := []struct {
		name   string
		mutate func(*MCPService) error
	}{
		{
			name: "SaveServer",
			mutate: func(service *MCPService) error {
				return service.SaveServer(MCPServerConfig{Name: "srv", Transport: "stdio", Command: "updated"})
			},
		},
		{
			name: "SetServerEnabled",
			mutate: func(service *MCPService) error {
				return service.SetServerEnabled("srv", false)
			},
		},
		{
			name: "DeleteServer",
			mutate: func(service *MCPService) error {
				return service.DeleteServer("srv")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, transport := newMCPPersistenceFailureFixture(t)
			before := snapshotMCPRuntime(service)
			beforeDisk, err := os.ReadFile(service.cfgPath)
			if err != nil {
				t.Fatalf("read initial config: %v", err)
			}
			service.persistConfig = func(MCPConfig) error { return persistErr }

			err = test.mutate(service)
			if !errors.Is(err, persistErr) {
				t.Fatalf("mutation error = %v, want %v", err, persistErr)
			}
			assertMCPRuntimeSnapshot(t, service, before)
			if transport.closes() != 0 {
				t.Fatalf("client transport closed %d times after failed persistence", transport.closes())
			}
			afterDisk, err := os.ReadFile(service.cfgPath)
			if err != nil {
				t.Fatalf("read config after failure: %v", err)
			}
			if !bytes.Equal(afterDisk, beforeDisk) {
				t.Fatal("on-disk config changed after failed persistence")
			}
		})
	}
}

func TestMCPServicePersistenceFailureRollsBackKeyringWrites(t *testing.T) {
	persistErr := errors.New("injected MCP persistence failure")
	tests := []struct {
		name          string
		previousValue string
		newValue      string
	}{
		{name: "overwrite existing entry", previousValue: "old-secret", newValue: "new-secret"},
		{name: "remove new entry", newValue: "new-secret"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMCPService(t)
			account := mcpSecretAccount("srv", mcpSecretMapHeaders, "token")
			marker := secretPrefixKeyring + base64.StdEncoding.EncodeToString([]byte(account))
			server := MCPServerConfig{Name: "srv", Transport: "stdio", Command: "echo"}
			persistedServer := cloneMCPServerConfig(server)
			keyring := make(map[string]string)
			if test.previousValue != "" {
				server.Headers = map[string]string{"token": test.previousValue}
				persistedServer.Headers = map[string]string{"token": marker}
				keyring[account] = test.previousValue
			}
			service.config = MCPConfig{Servers: []MCPServerConfig{server}}
			service.persistedConfig = MCPConfig{Servers: []MCPServerConfig{persistedServer}}
			service.lifecycleGeneration = 7
			before := snapshotMCPRuntime(service)
			service.encryptSecret = func(gotAccount, plaintext string) (string, error) {
				keyring[gotAccount] = plaintext
				return secretPrefixKeyring + base64.StdEncoding.EncodeToString([]byte(gotAccount)), nil
			}
			service.deleteSecret = func(gotAccount string) error {
				delete(keyring, gotAccount)
				return nil
			}
			service.persistConfig = func(MCPConfig) error { return persistErr }

			err := service.SaveServer(MCPServerConfig{
				Name:      "srv",
				Transport: "stdio",
				Command:   "echo",
				Headers:   map[string]string{"token": test.newValue},
			})
			if !errors.Is(err, persistErr) {
				t.Fatalf("SaveServer error = %v, want %v", err, persistErr)
			}
			assertMCPRuntimeSnapshot(t, service, before)
			gotValue, present := keyring[account]
			if test.previousValue == "" {
				if present {
					t.Fatalf("new keyring entry survived failed persistence with value %q", gotValue)
				}
				return
			}
			if !present || gotValue != test.previousValue {
				t.Fatalf("keyring entry after rollback = %q, present=%v; want %q", gotValue, present, test.previousValue)
			}
		})
	}
}

func TestMCPServiceEncryptionFailureRollsBackEarlierKeyringWrite(t *testing.T) {
	service := newTestMCPService(t)
	headerAccount := mcpSecretAccount("srv", mcpSecretMapHeaders, "token")
	envAccount := mcpSecretAccount("srv", mcpSecretMapEnv, "TOKEN")
	marker := secretPrefixKeyring + base64.StdEncoding.EncodeToString([]byte(headerAccount))
	service.config = MCPConfig{Servers: []MCPServerConfig{{
		Name:      "srv",
		Transport: "stdio",
		Command:   "echo",
		Headers:   map[string]string{"token": "old-secret"},
	}}}
	service.persistedConfig = MCPConfig{Servers: []MCPServerConfig{{
		Name:      "srv",
		Transport: "stdio",
		Command:   "echo",
		Headers:   map[string]string{"token": marker},
	}}}
	service.lifecycleGeneration = 4
	before := snapshotMCPRuntime(service)
	keyring := map[string]string{headerAccount: "old-secret"}
	encryptErr := errors.New("injected secret encryption failure")
	service.encryptSecret = func(account, plaintext string) (string, error) {
		if account == envAccount {
			return "", encryptErr
		}
		keyring[account] = plaintext
		return secretPrefixKeyring + base64.StdEncoding.EncodeToString([]byte(account)), nil
	}
	service.deleteSecret = func(account string) error {
		delete(keyring, account)
		return nil
	}
	var persistenceCalls int
	service.persistConfig = func(MCPConfig) error {
		persistenceCalls++
		return nil
	}

	err := service.SaveServer(MCPServerConfig{
		Name:      "srv",
		Transport: "stdio",
		Command:   "echo",
		Headers:   map[string]string{"token": "new-secret"},
		Env:       map[string]string{"TOKEN": "new-env-secret"},
	})
	if !errors.Is(err, encryptErr) {
		t.Fatalf("SaveServer error = %v, want %v", err, encryptErr)
	}
	if persistenceCalls != 0 {
		t.Fatalf("persistence called %d times after encryption failure", persistenceCalls)
	}
	assertMCPRuntimeSnapshot(t, service, before)
	if got := keyring[headerAccount]; got != "old-secret" {
		t.Fatalf("keyring entry after rollback = %q, want old-secret", got)
	}
	if _, present := keyring[envAccount]; present {
		t.Fatal("failed encryption created an environment keyring entry")
	}
}

func TestMCPServiceUnchangedSecretReusesPersistedKeyringMarker(t *testing.T) {
	service := newTestMCPService(t)
	account := mcpSecretAccount("srv", mcpSecretMapHeaders, "token")
	marker := secretPrefixKeyring + base64.StdEncoding.EncodeToString([]byte(account))
	server := MCPServerConfig{
		Name:      "srv",
		Transport: "stdio",
		Command:   os.Args[0],
		Headers:   map[string]string{"token": "secret"},
	}
	service.config = MCPConfig{Servers: []MCPServerConfig{server}}
	persistedServer := cloneMCPServerConfig(server)
	persistedServer.Headers["token"] = marker
	service.persistedConfig = MCPConfig{Servers: []MCPServerConfig{persistedServer}}
	service.encryptSecret = func(string, string) (string, error) {
		t.Fatal("unchanged secret was re-encrypted")
		return "", nil
	}
	var persisted MCPConfig
	service.persistConfig = func(candidate MCPConfig) error {
		persisted = candidate
		return nil
	}

	if err := service.SetServerEnabled("srv", true); err != nil {
		t.Fatalf("SetServerEnabled: %v", err)
	}
	if got := persisted.Servers[0].Headers["token"]; got != marker {
		t.Fatalf("persisted marker = %q, want %q", got, marker)
	}
	if got := service.persistedConfig.Servers[0].Headers["token"]; got != marker {
		t.Fatalf("committed persisted marker = %q, want %q", got, marker)
	}
}

func TestMCPServiceSaveServerCommitsOnlyAfterPersistence(t *testing.T) {
	service, _ := newMCPPersistenceFailureFixture(t)
	before := snapshotMCPRuntime(service)
	entered := make(chan MCPConfig, 1)
	release := make(chan struct{})
	service.persistConfig = func(candidate MCPConfig) error {
		entered <- candidate
		<-release
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- service.SaveServer(MCPServerConfig{Name: "srv", Transport: "stdio", Command: "updated"})
	}()

	select {
	case candidate := <-entered:
		if len(candidate.Servers) != 1 || candidate.Servers[0].Command != "updated" {
			t.Fatalf("persisted candidate = %+v, want updated server", candidate)
		}
	case <-time.After(time.Second):
		t.Fatal("SaveServer did not enter persistence")
	}
	assertMCPRuntimeSnapshot(t, service, before)
	select {
	case err := <-done:
		t.Fatalf("SaveServer returned before persistence completed: %v", err)
	default:
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SaveServer: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SaveServer did not finish after persistence completed")
	}
	after := snapshotMCPRuntime(service)
	if len(after.config.Servers) != 1 || after.config.Servers[0].Command != "updated" {
		t.Fatalf("committed config = %+v, want updated server", after.config)
	}
	if after.lifecycleGeneration != before.lifecycleGeneration+1 {
		t.Fatalf("lifecycle generation = %d, want %d", after.lifecycleGeneration, before.lifecycleGeneration+1)
	}
	if len(after.clients) != 0 {
		t.Fatalf("SaveServer kept a client after connection config changed: %v", after.clients)
	}
}

func TestMCPServiceConcurrentMutationsSerializeWithoutLostUpdates(t *testing.T) {
	service := newTestMCPService(t)
	type persistenceEntry struct {
		ordinal       int32
		candidate     MCPConfig
		lockAvailable bool
	}
	enteredPersistence := make(chan persistenceEntry, 2)
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(release)
	var persistenceCalls atomic.Int32
	service.persistConfig = func(candidate MCPConfig) error {
		lockAvailable := service.mu.TryLock()
		if lockAvailable {
			service.mu.Unlock()
		}
		ordinal := persistenceCalls.Add(1)
		enteredPersistence <- persistenceEntry{
			ordinal:       ordinal,
			candidate:     MCPConfig{Servers: cloneMCPServerConfigs(candidate.Servers)},
			lockAvailable: lockAvailable,
		}
		if ordinal == 1 {
			<-releaseFirst
		}
		return nil
	}

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- service.SaveServer(MCPServerConfig{Name: "first", Transport: "stdio", Command: "echo"})
	}()
	select {
	case entry := <-enteredPersistence:
		if entry.ordinal != 1 {
			t.Fatalf("first persistence ordinal = %d, want 1", entry.ordinal)
		}
		if !entry.lockAvailable {
			t.Fatal("SaveServer called persistence while holding the service state lock")
		}
		if len(entry.candidate.Servers) != 1 || entry.candidate.Servers[0].Name != "first" {
			t.Fatalf("first persisted candidate = %+v, want only first server", entry.candidate)
		}
	case <-time.After(time.Second):
		t.Fatal("first SaveServer did not enter persistence")
	}

	service.mu.RLock()
	firstTail := service.persistTail
	service.mu.RUnlock()
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- service.SaveServer(MCPServerConfig{Name: "second", Transport: "stdio", Command: "echo"})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if service.mu.TryRLock() {
			secondReserved := service.persistTail != firstTail
			service.mu.RUnlock()
			if secondReserved {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("second SaveServer did not reserve its persistence slot")
		}
		runtime.Gosched()
	}
	select {
	case entry := <-enteredPersistence:
		release()
		t.Fatalf("second persistence entered before the first completed: ordinal=%d", entry.ordinal)
	case <-time.After(100 * time.Millisecond):
	}
	release()

	select {
	case entry := <-enteredPersistence:
		if entry.ordinal != 2 {
			t.Fatalf("second persistence ordinal = %d, want 2", entry.ordinal)
		}
		if !entry.lockAvailable {
			t.Fatal("queued SaveServer called persistence while holding the service state lock")
		}
		if len(entry.candidate.Servers) != 2 || entry.candidate.Servers[0].Name != "first" || entry.candidate.Servers[1].Name != "second" {
			t.Fatalf("second persisted candidate = %+v, want first and second servers", entry.candidate)
		}
	case <-time.After(time.Second):
		t.Fatal("second SaveServer did not enter persistence after the first completed")
	}
	if err := <-firstResult; err != nil {
		t.Fatalf("first SaveServer: %v", err)
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second SaveServer: %v", err)
	}

	after := snapshotMCPRuntime(service)
	if len(after.config.Servers) != 2 || after.config.Servers[0].Name != "first" || after.config.Servers[1].Name != "second" {
		t.Fatalf("committed config = %+v, want first and second servers", after.config)
	}
	if after.lifecycleGeneration != 2 {
		t.Fatalf("lifecycle generation = %d, want 2", after.lifecycleGeneration)
	}
}

func TestMCPServiceMutationQueueIncludesSecretCleanup(t *testing.T) {
	service := newTestMCPService(t)
	service.config = MCPConfig{Servers: []MCPServerConfig{{
		Name:      "srv",
		Transport: "stdio",
		Command:   "echo",
		Headers:   map[string]string{"token": "old-secret"},
	}}}

	var persistenceCalls atomic.Int32
	secondPersistence := make(chan struct{}, 1)
	service.persistConfig = func(MCPConfig) error {
		if persistenceCalls.Add(1) == 2 {
			secondPersistence <- struct{}{}
		}
		return nil
	}
	cleanupEntered := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var releaseCleanupOnce sync.Once
	release := func() { releaseCleanupOnce.Do(func() { close(releaseCleanup) }) }
	t.Cleanup(release)
	service.deleteSecret = func(string) error {
		close(cleanupEntered)
		<-releaseCleanup
		return nil
	}

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- service.SaveServer(MCPServerConfig{
			Name:      "srv",
			Transport: "stdio",
			Command:   "echo",
			Headers:   map[string]string{},
		})
	}()
	select {
	case <-cleanupEntered:
	case <-time.After(time.Second):
		t.Fatal("first SaveServer did not enter secret cleanup")
	}

	service.mu.RLock()
	firstTail := service.persistTail
	service.mu.RUnlock()
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- service.SaveServer(MCPServerConfig{
			Name:      "srv",
			Transport: "stdio",
			Command:   "echo",
			Headers:   map[string]string{"token": mcpSecretMask},
		})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if service.mu.TryRLock() {
			secondReserved := service.persistTail != firstTail
			service.mu.RUnlock()
			if secondReserved {
				break
			}
		}
		if time.Now().After(deadline) {
			release()
			t.Fatal("second SaveServer did not reserve its persistence slot")
		}
		runtime.Gosched()
	}
	select {
	case <-secondPersistence:
		release()
		t.Fatal("queued SaveServer persisted before prior secret cleanup completed")
	case <-time.After(100 * time.Millisecond):
	}
	release()

	if err := <-firstResult; err != nil {
		t.Fatalf("first SaveServer: %v", err)
	}
	select {
	case <-secondPersistence:
	case <-time.After(time.Second):
		t.Fatal("second SaveServer did not persist after secret cleanup completed")
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second SaveServer: %v", err)
	}
	service.mu.RLock()
	_, secretPresent := service.config.Servers[0].Headers["token"]
	service.mu.RUnlock()
	if !secretPresent {
		t.Fatal("queued SaveServer lost the re-added secret field")
	}
}

func TestMCPService_GetServer_NotFound(t *testing.T) {
	s := newTestMCPService(t)
	_, err := s.GetServer("nope")
	if err == nil {
		t.Fatal("expected error for missing server")
	}
}

func TestMCPService_ListServers_ReturnsCopy(t *testing.T) {
	s := newTestMCPService(t)
	s.SaveServer(MCPServerConfig{Name: "a", Transport: "stdio", Command: "echo"})
	list := s.ListServers()
	list[0].Name = "mutated"
	// Original should be unchanged.
	got, _ := s.GetServer("a")
	if got.Name != "a" {
		t.Error("ListServers should return a copy, not a reference")
	}
}

// ---------------------------------------------------------------------------
// Persistence: 0600 permissions (Step 9 / G-SEC-09)
// ---------------------------------------------------------------------------

func TestMCPService_PersistConfig_0600Permissions(t *testing.T) {
	// Windows does not honor Unix permission bits: os.Chmod only toggles
	// the read-only attribute, and os.Stat reports 0666 for writable
	// files. The 0600 contract (atomicWriteJSON perm) is enforced by the
	// shared helper and is therefore unverifiable on Windows. See
	// atomic_write_test.go for the same platform skip.
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	s := newTestMCPService(t)
	if err := s.SaveServer(MCPServerConfig{
		Name: "srv", Transport: "stdio", Command: "echo",
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.cfgPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	// G-SEC-09: config file must be 0600 (owner read/write only).
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %o", info.Mode().Perm())
	}
}

func TestMCPService_LoadConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp-servers.json")
	// Save with one service, load with another.
	s1 := &MCPService{cfgPath: cfgPath, clients: make(map[string]*MCPClient)}
	s1.SaveServer(MCPServerConfig{
		Name: "persisted", Transport: "http", URL: "https://203.0.113.1:8080", // C-1: public TEST-NET-3 IP
		Headers: map[string]string{"Authorization": "Bearer secret"},
	})
	s1.SaveServer(MCPServerConfig{
		Name: "stdio-srv", Transport: "stdio", Command: "/usr/bin/python3",
		Args: []string{"-m", "mcp_server"},
	})
	// New service loading from the same path.
	s2 := &MCPService{cfgPath: cfgPath, clients: make(map[string]*MCPClient)}
	if err := s2.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	servers := s2.ListServers()
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
	// Verify the persisted config survived the round-trip.
	httpSrv, _ := s2.GetServer("persisted")
	if httpSrv.URL != "https://203.0.113.1:8080" {
		t.Errorf("URL mismatch: %s", httpSrv.URL)
	}
	// G-SEC-07: the public GetServer masks secret-bearing Header values.
	if httpSrv.Headers["Authorization"] != mcpSecretMask {
		t.Errorf("expected masked header, got %q", httpSrv.Headers["Authorization"])
	}
	// The internal (in-memory) config retains the decrypted plaintext for
	// use by running MCP connections.
	httpInternal, _ := s2.getServerLocked("persisted")
	if httpInternal.Headers["Authorization"] != "Bearer secret" {
		t.Errorf("internal header not preserved: %q", httpInternal.Headers["Authorization"])
	}
	// Re-saving a masked round-trip must preserve the existing secret.
	if err := s2.SaveServer(httpSrv); err != nil {
		t.Fatalf("re-save masked: %v", err)
	}
	httpInternal2, _ := s2.getServerLocked("persisted")
	if httpInternal2.Headers["Authorization"] != "Bearer secret" {
		t.Errorf("secret lost after masked round-trip: %q", httpInternal2.Headers["Authorization"])
	}
	stdioSrv, _ := s2.GetServer("stdio-srv")
	if stdioSrv.Command != "/usr/bin/python3" {
		t.Errorf("Command mismatch: %s", stdioSrv.Command)
	}
	if len(stdioSrv.Args) != 2 || stdioSrv.Args[0] != "-m" {
		t.Errorf("Args not preserved: %v", stdioSrv.Args)
	}
}

func TestMCPService_LoadConfigIgnoresLegacyAutoApprove(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp-servers.json")
	if err := os.WriteFile(cfgPath, []byte(`{"servers":[{"name":"legacy","transport":"stdio","command":"echo","autoApprove":["dangerous_tool"]}]}`), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	service := &MCPService{cfgPath: cfgPath, clients: make(map[string]*MCPClient)}
	if err := service.load(); err != nil {
		t.Fatalf("load legacy config: %v", err)
	}
	internal, err := service.getServerLocked("legacy")
	if err != nil {
		t.Fatalf("get legacy server: %v", err)
	}
	if internal.Enabled {
		t.Fatal("legacy config revived enabled state")
	}
	visible, err := service.GetServer("legacy")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	visibleJSON, err := json.Marshal(visible)
	if err != nil {
		t.Fatalf("marshal visible server: %v", err)
	}
	var visibleFields map[string]json.RawMessage
	if err := json.Unmarshal(visibleJSON, &visibleFields); err != nil {
		t.Fatalf("decode visible server: %v", err)
	}
	if _, ok := visibleFields["autoApprove"]; ok {
		t.Fatal("renderer-visible DTO exposes legacy autoApprove")
	}
	if err := service.SaveServer(visible); err != nil {
		t.Fatalf("re-save legacy server: %v", err)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read re-saved config: %v", err)
	}
	var savedFields struct {
		Servers []map[string]json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(saved, &savedFields); err != nil {
		t.Fatalf("decode re-saved config: %v", err)
	}
	if len(savedFields.Servers) != 1 {
		t.Fatalf("expected one re-saved server, got %d", len(savedFields.Servers))
	}
	if _, ok := savedFields.Servers[0]["autoApprove"]; ok {
		t.Fatal("re-saved config retained legacy autoApprove")
	}
}


// TestMCPService_SecretsEncryptedOnDisk verifies G-SEC-07: Header/Env
// secrets are encrypted on disk (not stored as plaintext).
func TestMCPService_SecretsEncryptedOnDisk(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp-enc.json")
	svc := &MCPService{cfgPath: cfgPath, clients: make(map[string]*MCPClient)}
	if err := svc.SaveServer(MCPServerConfig{
		Name: "s", Transport: "http", URL: "https://203.0.113.2", // C-1: public TEST-NET-3 IP
		Headers: map[string]string{"Authorization": "Bearer topsecret"},
		Env:     map[string]string{"API_KEY": "sk-abc"},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Read the raw file and ensure the plaintext secrets do NOT appear.
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "topsecret") || strings.Contains(string(raw), "sk-abc") {
		t.Errorf("plaintext secret found on disk:\n%s", raw)
	}
	// But the in-memory config holds plaintext for connections.
	internal, _ := svc.getServerLocked("s")
	if internal.Headers["Authorization"] != "Bearer topsecret" || internal.Env["API_KEY"] != "sk-abc" {
		t.Errorf("in-memory plaintext lost: %+v", internal)
	}
}

// ---------------------------------------------------------------------------
// Agent integration: CheckCommand for mcp.* (Step 6/8)
// ---------------------------------------------------------------------------

func TestAgentService_CheckCommand_MCPNamespace_NoService(t *testing.T) {
	// An AgentService without MCPService should block mcp.* commands.
	agent := NewAgentService()
	check := agent.CheckCommand("mcp.server.tool")
	if !check.Blocked {
		t.Error("mcp.* command should be blocked when MCPService not set")
	}
	if check.RiskLevel != RiskDangerous {
		t.Errorf("expected RiskDangerous, got %s", check.RiskLevel)
	}
}

func TestAgentService_CheckCommand_MCPNamespace_InvalidFormat(t *testing.T) {
	agent := NewAgentService()
	agent.setMCPService(newTestMCPService(t))
	// Too few parts.
	check := agent.CheckCommand("mcp.server")
	if !check.Blocked {
		t.Error("invalid mcp namespace should be blocked")
	}
	// Missing tool.
	check = agent.CheckCommand("mcp..")
	if !check.Blocked {
		t.Error("empty server/tool should be blocked")
	}
}

func TestAgentService_CheckCommand_MCPNamespace_UnknownTool(t *testing.T) {
	agent := NewAgentService()
	service := newTestMCPService(t)
	if err := service.setWorkspaceRoot(filepath.Dir(os.Args[0])); err != nil {
		t.Fatalf("setWorkspaceRoot: %v", err)
	}
	agent.setMCPService(service)
	// No servers connected → tool not found → blocked.
	check := agent.CheckCommand("mcp.unknownsrv.unknowntool")
	if !check.Blocked {
		t.Error("unknown MCP tool should be blocked")
	}
}

func TestAgentService_CheckCommand_NonMCPStillWorks(t *testing.T) {
	agent := NewAgentService()
	agent.setMCPService(newTestMCPService(t))
	// Regular shell commands should still be classified normally.
	check := agent.CheckCommand("ls -la")
	if check.Blocked {
		t.Error("ls should not be blocked")
	}
	if check.RiskLevel != RiskElevated {
		t.Errorf("expected RiskElevated for ls, got %s", check.RiskLevel)
	}
}

func TestAgentService_CallMCPTool_NoService(t *testing.T) {
	agent := NewAgentService()
	_, err := agent.CallMCPTool(context.Background(), "mcp.srv.tool", nil)
	if err == nil {
		t.Error("expected error when MCPService not configured")
	}
}

func TestAgentService_CallMCPTool_InvalidNamespace(t *testing.T) {
	agent := NewAgentService()
	agent.setMCPService(newTestMCPService(t))
	_, err := agent.CallMCPTool(context.Background(), "not-mcp", nil)
	if err == nil {
		t.Error("expected error for non-mcp namespace")
	}
	_, err = agent.CallMCPTool(context.Background(), "mcp.only-two", nil)
	if err == nil {
		t.Error("expected error for 2-part namespace")
	}
}

// ---------------------------------------------------------------------------
// Transport config validation (Step 3)
// ---------------------------------------------------------------------------

func TestMCPService_SaveServer_AllTransports(t *testing.T) {
	s := newTestMCPService(t)
	// stdio
	if err := s.SaveServer(MCPServerConfig{
		Name: "stdio-srv", Transport: "stdio", Command: "node",
		Args: []string{"server.js"}, Env: map[string]string{"NODE_ENV": "production"},
	}); err != nil {
		t.Fatalf("stdio: %v", err)
	}
	// SSE
	if err := s.SaveServer(MCPServerConfig{
		Name: "sse-srv", Transport: "sse", URL: "https://203.0.113.1:3001/sse", // C-1: public TEST-NET-3 IP
	}); err != nil {
		t.Fatalf("sse: %v", err)
	}
	// HTTP
	if err := s.SaveServer(MCPServerConfig{
		Name: "http-srv", Transport: "http", URL: "https://203.0.113.1:3002/mcp", // C-1: public TEST-NET-3 IP
		Headers: map[string]string{"X-API-Key": "test"},
	}); err != nil {
		t.Fatalf("http: %v", err)
	}
	servers := s.ListServers()
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(servers))
	}
}


// ---------------------------------------------------------------------------
// ConnectServer requires Enabled=true (G-SEC-12)
// ---------------------------------------------------------------------------

func TestMCPService_ConnectServer_RequiresEnabled(t *testing.T) {
	s := newTestMCPService(t)
	s.SaveServer(MCPServerConfig{
		Name: "srv", Transport: "stdio", Command: "echo",
	})
	// New servers default to Enabled=false → ConnectServer should refuse.
	err := s.ConnectServer(context.Background(), "srv")
	if err == nil {
		t.Error("ConnectServer should refuse disabled server (G-SEC-12)")
	}
}

func TestMCPService_DisconnectServer_NotConnected(t *testing.T) {
	s := newTestMCPService(t)
	err := s.DisconnectServer("nope")
	if err == nil {
		t.Error("expected error for disconnecting non-connected server")
	}
}

// ---------------------------------------------------------------------------
// MCPClient lifecycle (unit tests without real server)
// ---------------------------------------------------------------------------

func TestMCPClient_StopServer_Idempotent(t *testing.T) {
	c := newMCPClient(MCPServerConfig{Name: "test", Transport: "stdio"})
	// StopServer on an unstarted client should be a safe no-op.
	if err := c.StopServer(); err != nil {
		t.Errorf("StopServer on unstarted client: %v", err)
	}
	// Double-stop should also be safe.
	if err := c.StopServer(); err != nil {
		t.Errorf("double StopServer: %v", err)
	}
}

func TestMCPClient_Call_NotStarted(t *testing.T) {
	c := newMCPClient(MCPServerConfig{Name: "test", Transport: "stdio"})
	_, err := c.call(context.Background(), "tools/list", nil)
	if err == nil {
		t.Error("call on unstarted client should fail")
	}
}

func TestStdioTransport_ConnectAndDisconnect(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	service := newTestMCPService(t)
	if err := service.setWorkspaceRoot(filepath.Dir(os.Args[0])); err != nil {
		t.Fatalf("setWorkspaceRoot: %v", err)
	}
	cfg := MCPServerConfig{
		Name:      "stdio-lifecycle",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestHelperFakeMCPServer$", "-test.timeout=60s"},
		Env: map[string]string{
			"KOYORI_IDE_FAKE_MCP": "1",
		},
	}
	if err := service.SaveServer(cfg); err != nil {
		t.Fatalf("save stdio MCP client: %v", err)
	}
	if err := service.SetServerEnabled(cfg.Name, true); err != nil {
		t.Fatalf("enable stdio MCP client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := service.ConnectServer(ctx, cfg.Name); err != nil {
		t.Fatalf("start stdio MCP client: %v", err)
	}
	if err := service.DisconnectServer(cfg.Name); err != nil {
		t.Fatalf("stop stdio MCP client: %v", err)
	}

	events := make(map[string]bool)
	decoder := json.NewDecoder(strings.NewReader(logs.String()))
	for {
		var entry map[string]interface{}
		if err := decoder.Decode(&entry); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode transport lifecycle log: %v", err)
		}
		if entry["msg"] == "mcp transport" && entry["server"] == cfg.Name {
			events[fmt.Sprint(entry["event"])] = true
		}
	}
	if !events["connected"] || !events["disconnected"] {
		t.Fatalf("missing MCP transport lifecycle logs: %s", logs.String())
	}
}

type blockingMCPWriteCloser struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func newBlockingMCPWriteCloser() *blockingMCPWriteCloser {
	return &blockingMCPWriteCloser{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (w *blockingMCPWriteCloser) Write([]byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.closed
	return 0, io.ErrClosedPipe
}

func (w *blockingMCPWriteCloser) Close() error {
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}

func TestStdioTransport_SendConcurrentWithClose(t *testing.T) {
	writer := newBlockingMCPWriteCloser()
	transport := &stdioTransport{stdin: writer}
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- transport.Send(context.Background(), &jsonrpcOutboundMessage{
			JSONRPC: "2.0",
			ID:      1,
			Method:  "tools/list",
		})
	}()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("stdio Send did not reach the writer")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- transport.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close stdio transport: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stdio Close did not unblock the active Send")
	}
	select {
	case err := <-sendDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("Send error = %v, want closed pipe", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stdio Send remained blocked after Close")
	}
}

type scriptedMCPTransport struct {
	mu        sync.Mutex
	handler   func(*jsonrpcOutboundMessage, int) *jsonrpcResponse
	responses chan *jsonrpcResponse
	closed    chan struct{}
	closeOnce sync.Once
	sendCount int
}

func newScriptedMCPTransport(handler func(*jsonrpcOutboundMessage, int) *jsonrpcResponse) *scriptedMCPTransport {
	return &scriptedMCPTransport{
		handler:   handler,
		responses: make(chan *jsonrpcResponse, 16),
		closed:    make(chan struct{}),
	}
}

func (t *scriptedMCPTransport) Send(ctx context.Context, req *jsonrpcOutboundMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.closed:
		return io.ErrClosedPipe
	default:
	}
	t.mu.Lock()
	t.sendCount++
	sendNumber := t.sendCount
	handler := t.handler
	t.mu.Unlock()
	if handler == nil {
		return nil
	}
	resp := handler(req, sendNumber)
	if resp == nil {
		return nil
	}
	select {
	case t.responses <- resp:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.closed:
		return io.ErrClosedPipe
	}
}

func (t *scriptedMCPTransport) Recv() (*jsonrpcResponse, error) {
	select {
	case resp := <-t.responses:
		return resp, nil
	case <-t.closed:
		return nil, io.EOF
	}
}

func (t *scriptedMCPTransport) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

func (t *scriptedMCPTransport) sends() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sendCount
}

func newScriptedMCPClient(name string, transport mcpTransport) *MCPClient {
	client := newMCPClient(MCPServerConfig{Name: name, Transport: "stdio"})
	client.mu.Lock()
	client.transport = transport
	client.run = newMCPClientRun()
	client.startResponseDispatcherLocked(transport)
	client.mu.Unlock()
	return client
}

func TestMCPClient_HTTPInitializeSendsNotification(t *testing.T) {
	var mu sync.Mutex
	var requests []jsonrpcOutboundMessage
	transport := &httpTransport{
		url: "https://mcp.example/rpc",
		client: &http.Client{Transport: mcpRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			var rpcReq jsonrpcOutboundMessage
			if err := json.Unmarshal(body, &rpcReq); err != nil {
				return nil, err
			}
			mu.Lock()
			requests = append(requests, rpcReq)
			mu.Unlock()
			if rpcReq.ID == nil {
				return &http.Response{
					StatusCode: http.StatusAccepted,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			}
			responseBody, err := json.Marshal(jsonrpcResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Result:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":false}},"serverInfo":{"name":"http","version":"1.0"}}`),
			})
			if err != nil {
				return nil, err
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(responseBody)),
			}, nil
		})},
	}
	client := newMCPClient(MCPServerConfig{Name: "http", Transport: "http"})
	client.transport = transport
	client.run = newMCPClientRun()
	t.Cleanup(func() { _ = client.StopServer() })

	if err := client.initialize(context.Background()); err != nil {
		t.Fatalf("initialize HTTP client: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("HTTP request count = %d, want initialize + initialized notification", len(requests))
	}
	if requests[0].Method != "initialize" || requests[0].ID == nil {
		t.Fatalf("first HTTP request = %+v, want initialize request", requests[0])
	}
	if requests[1].Method != "notifications/initialized" || requests[1].ID != nil {
		t.Fatalf("second HTTP request = %+v, want initialized notification", requests[1])
	}
	params, ok := requests[0].Params.(map[string]interface{})
	if !ok {
		t.Fatalf("initialize params = %#v, want an object", requests[0].Params)
	}
	if params["protocolVersion"] != "2024-11-05" {
		t.Fatalf("initialize protocolVersion = %v, want 2024-11-05", params["protocolVersion"])
	}
	// The client implements exactly one server-to-client capability — the
	// controlled roots/list handler — so the initialize request declares
	// exactly {"roots":{}} and nothing else.
	capabilities, ok := params["capabilities"].(map[string]interface{})
	if !ok || len(capabilities) != 1 {
		t.Fatalf("initialize capabilities = %#v, want exactly {roots:{}}", params["capabilities"])
	}
	if _, ok := capabilities["roots"]; !ok {
		t.Fatalf("initialize capabilities missing roots: %#v", capabilities)
	}
	clientInfo, ok := params["clientInfo"].(map[string]interface{})
	if !ok || clientInfo["name"] != "koyori-ide" || clientInfo["version"] != "1.0" {
		t.Fatalf("initialize clientInfo = %#v, want koyori-ide 1.0", params["clientInfo"])
	}
	snapshot, snapshotErr := client.capabilitySnapshotCopy()
	if snapshotErr != nil {
		t.Fatalf("capability snapshot after initialize: %v", snapshotErr)
	}
	if snapshot.Capabilities.Tools.State != MCPCapabilitySupported || snapshot.ServerInfo.Name != "http" {
		t.Fatalf("capability snapshot = %+v, want supported tools from server http", snapshot)
	}
}

func TestSSETransport_StandardEndpointWaitsAndResolvesRelativeURL(t *testing.T) {
	transport := newSSETransport(MCPServerConfig{
		Name:      "standard-sse",
		Transport: "sse",
		URL:       "https://203.0.113.1:3001/sse",
	})
	requests := make(chan *http.Request, 1)
	transport.client = &http.Client{Transport: mcpRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})}

	reader, writer := io.Pipe()
	readDone := make(chan struct{})
	transport.mu.Lock()
	transport.body = reader
	transport.readDone = readDone
	transport.wg.Add(1)
	transport.mu.Unlock()
	go func() {
		defer transport.wg.Done()
		transport.readLoop(reader)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- transport.Send(ctx, &jsonrpcOutboundMessage{JSONRPC: "2.0", Method: "notifications/initialized"})
	}()
	select {
	case err := <-sendDone:
		t.Fatalf("Send returned before endpoint event: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	if _, err := io.WriteString(writer, "event: endpoint\ndata: /messages?session=abc\n\n"); err != nil {
		t.Fatalf("write standard endpoint event: %v", err)
	}
	select {
	case req := <-requests:
		if got, want := req.URL.String(), "https://203.0.113.1:3001/messages?session=abc"; got != want {
			t.Fatalf("resolved post URL = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("Send did not POST after standard endpoint event")
	}
	if err := <-sendDone; err != nil {
		t.Fatalf("Send after endpoint event: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close SSE writer: %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("close SSE transport: %v", err)
	}
}

func TestMCPClient_CallTool_Success(t *testing.T) {
	client, _ := startInitializedScriptedMCPClient(t, "success", mcpTestToolsCapability, func(req *jsonrpcOutboundMessage, _ int) *jsonrpcResponse {
		return &jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`),
		}
	})

	result, err := client.CallTool(context.Background(), "echo", map[string]interface{}{"value": "secret-input"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError || len(result.Content) != 1 || result.Content[0].Text != "ok" {
		t.Fatalf("unexpected tool result: %+v", result)
	}
}

func TestMCPClient_CallTool_RPCFailure(t *testing.T) {
	client, _ := startInitializedScriptedMCPClient(t, "failure", mcpTestToolsCapability, func(req *jsonrpcOutboundMessage, _ int) *jsonrpcResponse {
		return &jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonrpcError{Code: -32000, Message: "tool failed"},
		}
	})

	_, err := client.CallTool(context.Background(), "fail", nil)
	if err == nil || !strings.Contains(err.Error(), "rpc error -32000: tool failed") {
		t.Fatalf("CallTool error = %v, want JSON-RPC failure", err)
	}
}

func TestMCPService_CallToolStructuredLogsRedactArguments(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previousLogger)

	toolCalls := 0
	transport := newScriptedMCPTransport(scriptedMCPInitializeHandler(mcpTestToolsCapability, func(req *jsonrpcOutboundMessage, _ int) *jsonrpcResponse {
		if req.Method != "tools/call" {
			return &jsonrpcResponse{JSONRPC: "2.0", ID: req.ID}
		}
		toolCalls++
		if toolCalls == 1 {
			return &jsonrpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`),
			}
		}
		return &jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonrpcError{Code: -32000, Message: "tool failed for secret-input"},
		}
	}))
	client := newScriptedMCPClient("logged-server", transport)
	initializeScriptedMCPClient(t, client)
	t.Cleanup(func() { _ = client.StopServer() })
	service := newTestMCPService(t)
	if err := service.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatalf("set workspace root: %v", err)
	}
	service.mu.Lock()
	service.clients["logged-server"] = client
	service.mu.Unlock()

	if _, err := service.callTool(context.Background(), "logged-server", "success", map[string]interface{}{"token": "secret-success"}); err != nil {
		t.Fatalf("successful CallTool: %v", err)
	}
	_, callErr := service.callTool(context.Background(), "logged-server", "failure", map[string]interface{}{"token": "secret-input"})
	if callErr == nil {
		t.Fatal("failed CallTool unexpectedly succeeded")
	}
	if strings.Contains(callErr.Error(), "secret-input") || !strings.Contains(callErr.Error(), "[REDACTED]") {
		t.Fatalf("failed CallTool returned an unredacted error: %v", callErr)
	}

	rawLogs := logs.String()
	for _, secret := range []string{"secret-success", "secret-input"} {
		if strings.Contains(rawLogs, secret) {
			t.Fatalf("structured MCP log leaked argument %q: %s", secret, rawLogs)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(rawLogs))
	seenSuccess := false
	seenFailure := false
	for {
		var entry map[string]interface{}
		if err := decoder.Decode(&entry); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode structured MCP log: %v", err)
		}
		message, _ := entry["msg"].(string)
		if message != "mcp tool call" && message != "mcp tool call failed" {
			continue
		}
		if entry["server"] != "logged-server" || entry["tool"] == nil || entry["duration"] == nil {
			t.Fatalf("missing structured MCP log attributes: %v", entry)
		}
		if message == "mcp tool call" {
			seenSuccess = true
		} else {
			seenFailure = true
			if !strings.Contains(fmt.Sprint(entry["error"]), "[REDACTED]") {
				t.Fatalf("failed tool log did not redact echoed argument: %v", entry)
			}
		}
	}
	if !seenSuccess || !seenFailure {
		t.Fatalf("missing success/failure MCP tool logs: %s", rawLogs)
	}
}

func TestMCPService_CallToolRequiresBackendApproval(t *testing.T) {
	service := newTestMCPService(t)
	if _, err := service.CallTool(context.Background(), "server", "tool", nil); err == nil || !strings.Contains(err.Error(), "approval token required") {
		t.Fatalf("CallTool error = %v, want backend approval requirement", err)
	}
}

func TestMCPService_LegacyRendererApprovalShimsAreDenyOnly(t *testing.T) {
	service := newTestMCPService(t)
	ctx := context.Background()

	if token, err := service.RequestToolApproval(ctx, "server", "tool", nil); err == nil || token != "" {
		t.Fatalf("RequestToolApproval = (%q, %v), want empty deny-only result", token, err)
	}
	if result, err := service.ExecuteApprovedTool(ctx, "server", "tool", nil, "forged"); err == nil || result != nil {
		t.Fatalf("ExecuteApprovedTool = (%v, %v), want nil deny-only result", result, err)
	}
}

func TestMCPService_ApprovedToolTokenIsArgumentBoundAndSingleUse(t *testing.T) {
	client, _ := startInitializedScriptedMCPClient(t, "approved-server", mcpTestToolsCapability, func(req *jsonrpcOutboundMessage, _ int) *jsonrpcResponse {
		return &jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}
	})
	_ = client
	service := newTestMCPService(t)
	if err := service.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatalf("set workspace root: %v", err)
	}
	service.mu.Lock()
	service.clients["approved-server"] = client
	rootGeneration := service.rootGeneration
	lifecycleGeneration := service.lifecycleGeneration
	service.mu.Unlock()

	args := map[string]interface{}{"value": "approved"}
	argsJSON, _ := json.Marshal(args)
	service.approvals = map[string]mcpToolApproval{
		"mismatch": {server: "approved-server", tool: "echo", argsJSON: string(argsJSON), rootGeneration: rootGeneration, lifecycleGeneration: lifecycleGeneration, expiresAt: time.Now().Add(time.Minute)},
		"valid":    {server: "approved-server", tool: "echo", argsJSON: string(argsJSON), rootGeneration: rootGeneration, lifecycleGeneration: lifecycleGeneration, expiresAt: time.Now().Add(time.Minute)},
	}
	if _, err := service.executeApprovedToolLegacy(context.Background(), "approved-server", "echo", map[string]interface{}{"value": "changed"}, "mismatch"); err == nil {
		t.Fatal("mismatched arguments unexpectedly accepted")
	}
	result, err := service.executeApprovedToolLegacy(context.Background(), "approved-server", "echo", args, "valid")
	if err != nil || result == nil || len(result.Content) != 1 || result.Content[0].Text != "ok" {
		t.Fatalf("approved tool call = (%+v, %v), want success", result, err)
	}
	if _, err := service.executeApprovedToolLegacy(context.Background(), "approved-server", "echo", args, "valid"); err == nil {
		t.Fatal("approval token replay unexpectedly accepted")
	}
}

func TestMCPService_ExpiredApprovedToolTokenIsRejected(t *testing.T) {
	service := newTestMCPService(t)
	args := map[string]interface{}{"value": "approved"}
	argsJSON, _ := json.Marshal(args)
	service.approvals = map[string]mcpToolApproval{
		"expired": {
			server: "approved-server", tool: "echo", argsJSON: string(argsJSON),
			expiresAt: time.Now().Add(-time.Second),
		},
	}

	if _, err := service.executeApprovedToolLegacy(context.Background(), "approved-server", "echo", args, "expired"); err == nil {
		t.Fatal("expired approval token unexpectedly accepted")
	}
}

func TestMCPService_WorkspaceRootChangeDisconnectsAndInvalidatesApproval(t *testing.T) {
	service := newTestMCPService(t)
	if err := service.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatalf("set initial workspace root: %v", err)
	}
	client := newScriptedMCPClient("approved-server", newScriptedMCPTransport(func(req *jsonrpcOutboundMessage, _ int) *jsonrpcResponse {
		return &jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}
	}))
	service.mu.Lock()
	service.clients["approved-server"] = client
	rootGeneration := service.rootGeneration
	lifecycleGeneration := service.lifecycleGeneration
	service.mu.Unlock()
	args := map[string]interface{}{"value": "approved"}
	argsJSON, _ := json.Marshal(args)
	service.approvals = map[string]mcpToolApproval{
		"old-root": {
			server: "approved-server", tool: "echo", argsJSON: string(argsJSON),
			rootGeneration: rootGeneration, lifecycleGeneration: lifecycleGeneration,
			expiresAt: time.Now().Add(time.Minute),
		},
	}

	if err := service.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatalf("change workspace root: %v", err)
	}
	service.mu.RLock()
	connected := len(service.clients)
	service.mu.RUnlock()
	if connected != 0 {
		t.Fatalf("connected clients after root change = %d, want 0", connected)
	}
	client.mu.Lock()
	closed := client.closed
	client.mu.Unlock()
	if !closed {
		t.Fatal("workspace root change did not stop the connected MCP client")
	}
	if _, err := service.executeApprovedToolLegacy(context.Background(), "approved-server", "echo", args, "old-root"); err == nil {
		t.Fatal("approval issued for the previous workspace generation unexpectedly accepted")
	}
}

func TestMCPService_ListAgentMCPToolsPropagatesContextCancellation(t *testing.T) {
	service := newTestMCPService(t)
	if err := service.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatalf("set workspace root: %v", err)
	}
	client, _ := startInitializedScriptedMCPClient(t, "blocked", mcpTestToolsCapability, nil)
	service.mu.Lock()
	service.clients["blocked"] = client
	service.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ListAgentMCPTools(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListAgentMCPTools error = %v, want context.Canceled", err)
	}
}

func TestMCPService_ApprovedToolTokenIsLifecycleBound(t *testing.T) {
	transport := newScriptedMCPTransport(func(req *jsonrpcOutboundMessage, _ int) *jsonrpcResponse {
		return &jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}
	})
	client := newScriptedMCPClient("approved-server", transport)
	t.Cleanup(func() { _ = client.StopServer() })
	service := newTestMCPService(t)
	service.mu.Lock()
	service.clients["approved-server"] = client
	service.lifecycleGeneration = 7
	service.mu.Unlock()

	args := map[string]interface{}{"value": "approved"}
	argsJSON, _ := json.Marshal(args)
	service.approvals = map[string]mcpToolApproval{
		"before-disconnect": {
			server: "approved-server", tool: "echo", argsJSON: string(argsJSON),
			lifecycleGeneration: 7, expiresAt: time.Now().Add(time.Minute),
		},
	}

	if err := service.DisconnectServer("approved-server"); err != nil {
		t.Fatalf("DisconnectServer: %v", err)
	}
	if _, err := service.executeApprovedToolLegacy(context.Background(), "approved-server", "echo", args, "before-disconnect"); err == nil {
		t.Fatal("approval issued before disconnect unexpectedly accepted")
	}
}

func TestMCPClient_CallTool_Timeout(t *testing.T) {
	client, _ := startInitializedScriptedMCPClient(t, "timeout", mcpTestToolsCapability, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := client.CallTool(ctx, "slow", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CallTool error = %v, want context deadline exceeded", err)
	}
}

func TestMCPClient_ListToolsCacheTTL(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previousLogger)

	// Name tool responses by their tools/list ordinal so the handshake's
	// initialize request and initialized notification do not shift them.
	toolsListed := 0
	transport := newScriptedMCPTransport(scriptedMCPInitializeHandler(mcpTestToolsCapability, func(req *jsonrpcOutboundMessage, sendNumber int) *jsonrpcResponse {
		if req.Method != "tools/list" {
			return &jsonrpcResponse{JSONRPC: "2.0", ID: req.ID}
		}
		toolsListed++
		result := fmt.Sprintf(`{"tools":[{"name":"tool-%d","inputSchema":{"type":"object"}}]}`, toolsListed)
		return &jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(result)}
	}))
	client := newScriptedMCPClient("cache", transport)
	initializeScriptedMCPClient(t, client)
	t.Cleanup(func() { _ = client.StopServer() })

	first, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("first ListTools: %v", err)
	}
	first[0].InputSchema["type"] = "mutated"
	second, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("cached ListTools: %v", err)
	}
	if got := transport.sends(); got != 3 {
		t.Fatalf("transport sends after cache hit = %d, want initialize + initialized + tools/list", got)
	}
	if second[0].Name != "tool-1" || second[0].InputSchema["type"] != "object" {
		t.Fatalf("cached tools were not isolated from caller mutation: %+v", second)
	}

	client.mu.Lock()
	client.toolsCachedAt = time.Now().Add(-mcpToolsCacheTTL - time.Second)
	client.mu.Unlock()
	third, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("expired ListTools: %v", err)
	}
	if got := transport.sends(); got != 4 {
		t.Fatalf("transport sends after TTL expiry = %d, want handshake + two tools/list", got)
	}
	if third[0].Name != "tool-2" {
		t.Fatalf("refreshed tool = %q, want tool-2", third[0].Name)
	}
	for _, message := range []string{"mcp tools cache miss", "mcp tools cache hit", "mcp tools cache expired", "mcp tools refreshed"} {
		if !strings.Contains(logs.String(), `"msg":"`+message+`"`) {
			t.Fatalf("missing %q debug log: %s", message, logs.String())
		}
	}
}

func TestParseSSEFrame_MultiLineAndMultipleEvents(t *testing.T) {
	body := []byte(": keepalive\r\n\r\nevent: message\r\ndata: {\"jsonrpc\":\"2.0\",\r\ndata: \"id\":7,\r\ndata: \"result\":{\"frame\":1}}\r\n\r\nevent: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":8,\"result\":{\"frame\":2}}\r\n\r\n")
	resp, err := parseSSEFrame(body)
	if err != nil {
		t.Fatalf("parseSSEFrame: %v", err)
	}
	if fmt.Sprint(resp.ID) != "7" {
		t.Fatalf("response id = %v, want 7", resp.ID)
	}
	var result map[string]int
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal SSE result: %v", err)
	}
	if result["frame"] != 1 {
		t.Fatalf("first SSE frame result = %v", result)
	}
}

func TestSSETransport_ReceivesMultipleFrames(t *testing.T) {
	stream := strings.Join([]string{
		"event: message",
		`data: {"jsonrpc":"2.0","id":1,"result":{"frame":1}}`,
		"",
		"event: message",
		`data: {"jsonrpc":"2.0","id":2,"result":{"frame":2}}`,
		"",
	}, "\n")
	transport := newSSETransport(MCPServerConfig{Name: "sse-frames", Transport: "sse"})
	transport.readLoop(io.NopCloser(strings.NewReader(stream)))
	t.Cleanup(func() { _ = transport.Close() })

	for wantID := 1; wantID <= 2; wantID++ {
		resp, err := transport.Recv()
		if err != nil {
			t.Fatalf("Recv frame %d: %v", wantID, err)
		}
		if fmt.Sprint(resp.ID) != fmt.Sprint(wantID) {
			t.Fatalf("frame id = %v, want %d", resp.ID, wantID)
		}
	}
	if _, err := transport.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after final frame = %v, want EOF", err)
	}
}

type b1DispatchTransport struct {
	mu        sync.Mutex
	requests  []*jsonrpcOutboundMessage
	responses chan *jsonrpcResponse
	closed    chan struct{}
	closeOnce sync.Once
}

func newB1DispatchTransport() *b1DispatchTransport {
	return &b1DispatchTransport{
		responses: make(chan *jsonrpcResponse, 3),
		closed:    make(chan struct{}),
	}
}

func (t *b1DispatchTransport) Send(ctx context.Context, req *jsonrpcOutboundMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.closed:
		return io.ErrClosedPipe
	default:
	}

	t.mu.Lock()
	t.requests = append(t.requests, req)
	if len(t.requests) == 3 {
		// Release responses only after every request has been sent. Returning
		// them in reverse order also verifies ID-based response routing.
		for i := len(t.requests) - 1; i >= 0; i-- {
			sent := t.requests[i]
			result, _ := json.Marshal(map[string]string{"method": sent.Method})
			t.responses <- &jsonrpcResponse{
				JSONRPC: "2.0",
				ID:      sent.ID,
				Result:  result,
			}
		}
	}
	t.mu.Unlock()
	return nil
}

func (t *b1DispatchTransport) Recv() (*jsonrpcResponse, error) {
	select {
	case resp := <-t.responses:
		return resp, nil
	case <-t.closed:
		return nil, io.EOF
	case <-time.After(750 * time.Millisecond):
		return nil, errors.New("timed out waiting for concurrent sends")
	}
}

func (t *b1DispatchTransport) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

func (t *b1DispatchTransport) requestCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.requests)
}

// C-3 / B1: three calls must all reach Send before any response is available.
// The old call-level mutex deadlocked this transport after the first request.
func TestMCPClient_CallConcurrentDispatch_C3_B1(t *testing.T) {
	transport := newB1DispatchTransport()
	client := newMCPClient(MCPServerConfig{Name: "concurrent", Transport: "stdio"})
	client.mu.Lock()
	client.transport = transport
	client.startResponseDispatcherLocked(transport)
	client.mu.Unlock()
	t.Cleanup(func() {
		if err := client.StopServer(); err != nil {
			t.Errorf("stop client: %v", err)
		}
	})

	methods := []string{"tools/one", "tools/two", "tools/three"}
	type outcome struct {
		method string
		got    string
		err    error
	}
	outcomes := make(chan outcome, len(methods))
	start := make(chan struct{})
	for _, method := range methods {
		method := method
		go func() {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			raw, err := client.call(ctx, method, nil)
			var result map[string]string
			if err == nil {
				err = json.Unmarshal(raw, &result)
			}
			outcomes <- outcome{method: method, got: result["method"], err: err}
		}()
	}
	close(start)

	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	for range methods {
		select {
		case result := <-outcomes:
			if result.err != nil {
				t.Errorf("call %s: %v", result.method, result.err)
				continue
			}
			if result.got != result.method {
				t.Errorf("call %s received response for %s", result.method, result.got)
			}
		case <-deadline.C:
			t.Fatal("concurrent MCP calls did not complete")
		}
	}
	if got := transport.requestCount(); got != len(methods) {
		t.Fatalf("sent requests = %d, want %d", got, len(methods))
	}
}

type b1BlockingTransport struct {
	sent      chan struct{}
	closed    chan struct{}
	sendOnce  sync.Once
	closeOnce sync.Once
}

func newB1BlockingTransport() *b1BlockingTransport {
	return &b1BlockingTransport{
		sent:   make(chan struct{}),
		closed: make(chan struct{}),
	}
}

func (t *b1BlockingTransport) Send(ctx context.Context, _ *jsonrpcOutboundMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.closed:
		return io.ErrClosedPipe
	default:
	}
	t.sendOnce.Do(func() { close(t.sent) })
	return nil
}

func (t *b1BlockingTransport) Recv() (*jsonrpcResponse, error) {
	<-t.closed
	return nil, io.EOF
}

func (t *b1BlockingTransport) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

func startB1TestClient(transport mcpTransport) *MCPClient {
	client := newMCPClient(MCPServerConfig{Name: "blocking", Transport: "stdio"})
	client.mu.Lock()
	client.transport = transport
	client.startResponseDispatcherLocked(transport)
	client.mu.Unlock()
	return client
}

func TestMCPClient_CallCancellationRemovesPending_B1(t *testing.T) {
	transport := newB1BlockingTransport()
	client := startB1TestClient(transport)
	t.Cleanup(func() { _ = client.StopServer() })

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := client.call(ctx, "tools/cancel", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call error = %v, want context deadline exceeded", err)
	}

	client.mu.Lock()
	pendingCount := len(client.pending)
	client.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending calls after cancellation = %d, want 0", pendingCount)
	}
}

func TestMCPClient_StopServerUnblocksPending_B1(t *testing.T) {
	transport := newB1BlockingTransport()
	client := startB1TestClient(transport)

	callDone := make(chan error, 1)
	go func() {
		_, err := client.call(context.Background(), "tools/block", nil)
		callDone <- err
	}()
	select {
	case <-transport.sent:
	case <-time.After(time.Second):
		t.Fatal("request was not sent")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- client.StopServer() }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("stop client: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StopServer deadlocked waiting for response dispatcher")
	}

	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pending call error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StopServer did not unblock pending call")
	}
}

type b1BlockedSendTransport struct {
	mu                sync.Mutex
	firstRequestID    interface{}
	sendCount         int
	firstSent         chan struct{}
	secondSendStarted chan struct{}
	closed            chan struct{}
	closeOnce         sync.Once
}

func newB1BlockedSendTransport() *b1BlockedSendTransport {
	return &b1BlockedSendTransport{
		firstSent:         make(chan struct{}),
		secondSendStarted: make(chan struct{}),
		closed:            make(chan struct{}),
	}
}

func (t *b1BlockedSendTransport) Send(_ context.Context, req *jsonrpcOutboundMessage) error {
	t.mu.Lock()
	t.sendCount++
	sendNumber := t.sendCount
	if sendNumber == 1 {
		t.firstRequestID = req.ID
		close(t.firstSent)
	}
	if sendNumber == 2 {
		close(t.secondSendStarted)
	}
	t.mu.Unlock()

	if sendNumber == 1 {
		return nil
	}
	<-t.closed
	return io.ErrClosedPipe
}

func (t *b1BlockedSendTransport) Recv() (*jsonrpcResponse, error) {
	select {
	case <-t.secondSendStarted:
		t.mu.Lock()
		id := t.firstRequestID
		t.firstRequestID = nil
		t.mu.Unlock()
		if id != nil {
			result, _ := json.Marshal(map[string]string{"status": "first-complete"})
			return &jsonrpcResponse{JSONRPC: "2.0", ID: id, Result: result}, nil
		}
	case <-t.closed:
		return nil, io.EOF
	}
	<-t.closed
	return nil, io.EOF
}

func (t *b1BlockedSendTransport) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

// FIX B1: a blocked writer must not own the client state lock. The dispatcher
// must still route an earlier response, and StopServer must be able to close
// the transport to release the blocked Send.
func TestMCPClient_BlockedSendDoesNotBlockDispatchOrStop_B1(t *testing.T) {
	transport := newB1BlockedSendTransport()
	client := startB1TestClient(transport)

	firstDone := make(chan error, 1)
	go func() {
		raw, err := client.call(context.Background(), "tools/first", nil)
		if err == nil {
			var result map[string]string
			err = json.Unmarshal(raw, &result)
			if err == nil && result["status"] != "first-complete" {
				err = fmt.Errorf("unexpected first result: %v", result)
			}
		}
		firstDone <- err
	}()
	select {
	case <-transport.firstSent:
	case <-time.After(time.Second):
		t.Fatal("first request was not sent")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := client.call(context.Background(), "tools/blocked", nil)
		secondDone <- err
	}()
	select {
	case <-transport.secondSendStarted:
	case <-time.After(time.Second):
		t.Fatal("second Send did not reach the blocking transport")
	}

	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first call was not dispatched while another Send blocked: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("response dispatcher was blocked behind the second Send")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- client.StopServer() }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("StopServer: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StopServer could not close a transport with a blocked Send")
	}
	select {
	case err := <-secondDone:
		if err == nil {
			t.Fatal("blocked call unexpectedly succeeded after StopServer")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Send goroutine was not reclaimed")
	}
}

type b1RoundTripFunc func(*http.Request) (*http.Response, error)

func (f b1RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestMCPClient_HTTPStopCancelsAndWaitsForActiveCall_B1(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	transport := b1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-request.Context().Done()
		close(requestCanceled)
		return nil, request.Context().Err()
	})

	client := newMCPClient(MCPServerConfig{Name: "http-stop", Transport: "http"})
	client.mu.Lock()
	client.transport = &httpTransport{
		url:    "https://mcp.invalid/rpc",
		client: &http.Client{Transport: transport},
	}
	client.run = newMCPClientRun()
	client.mu.Unlock()

	callDone := make(chan error, 1)
	go func() {
		_, err := client.call(context.Background(), "tools/block", nil)
		callDone <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("HTTP request did not start")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- client.StopServer() }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("StopServer: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StopServer did not cancel and await the active HTTP request")
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("HTTP handler did not observe request cancellation")
	}
	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("HTTP call error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP call goroutine outlived StopServer")
	}
}

type b1BlockingCloseTransport struct {
	closeStarted chan struct{}
	releaseClose chan struct{}
	startOnce    sync.Once
	releaseOnce  sync.Once
}

func newB1BlockingCloseTransport() *b1BlockingCloseTransport {
	return &b1BlockingCloseTransport{
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
}

func (t *b1BlockingCloseTransport) Send(context.Context, *jsonrpcOutboundMessage) error {
	return nil
}

func (t *b1BlockingCloseTransport) Recv() (*jsonrpcResponse, error) {
	return nil, io.EOF
}

func (t *b1BlockingCloseTransport) Close() error {
	t.startOnce.Do(func() { close(t.closeStarted) })
	<-t.releaseClose
	return nil
}

func (t *b1BlockingCloseTransport) release() {
	t.releaseOnce.Do(func() { close(t.releaseClose) })
}

func clientWithBlockingClose(transport mcpTransport) *MCPClient {
	client := newMCPClient(MCPServerConfig{Name: "blocking-close", Transport: "stdio"})
	client.mu.Lock()
	client.transport = transport
	client.run = newMCPClientRun()
	client.mu.Unlock()
	return client
}

func assertServiceStateLockAvailable(t *testing.T, service *MCPService) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = service.ListServers()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("MCPService state lock remained held during transport shutdown")
	}
}

func TestMCPService_StopOperationsReleaseStateLock_B1(t *testing.T) {
	t.Run("delete server", func(t *testing.T) {
		service := newTestMCPService(t)
		if err := service.SaveServer(MCPServerConfig{Name: "srv", Transport: "stdio", Command: "echo"}); err != nil {
			t.Fatalf("SaveServer: %v", err)
		}
		transport := newB1BlockingCloseTransport()
		defer transport.release()
		service.mu.Lock()
		service.clients["srv"] = clientWithBlockingClose(transport)
		service.mu.Unlock()

		deleteDone := make(chan error, 1)
		go func() { deleteDone <- service.DeleteServer("srv") }()
		select {
		case <-transport.closeStarted:
		case <-time.After(time.Second):
			t.Fatal("DeleteServer did not start transport shutdown")
		}
		assertServiceStateLockAvailable(t, service)
		if _, err := service.GetServer("srv"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted server remained visible while shutdown blocked: %v", err)
		}
		transport.release()
		select {
		case err := <-deleteDone:
			if err != nil {
				t.Fatalf("DeleteServer: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("DeleteServer did not finish after transport close was released")
		}
	})

	t.Run("close service", func(t *testing.T) {
		service := newTestMCPService(t)
		transport := newB1BlockingCloseTransport()
		defer transport.release()
		service.mu.Lock()
		service.clients["srv"] = clientWithBlockingClose(transport)
		service.mu.Unlock()

		closeDone := make(chan error, 1)
		go func() { closeDone <- service.Close() }()
		select {
		case <-transport.closeStarted:
		case <-time.After(time.Second):
			t.Fatal("Close did not start transport shutdown")
		}
		assertServiceStateLockAvailable(t, service)
		service.mu.RLock()
		remainingClients := len(service.clients)
		service.mu.RUnlock()
		if remainingClients != 0 {
			t.Fatalf("clients were not detached before shutdown: %d", remainingClients)
		}
		transport.release()
		select {
		case err := <-closeDone:
			if err != nil {
				t.Fatalf("Close: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Close did not finish after transport close was released")
		}
	})
}

// C-2: an SSE server that advertises a cross-origin postURL must be rejected
// and the stream torn down. We feed a synthetic SSE body straight into
// readLoop so no real network dial is needed (offline-safe).
func TestSSETransport_PostURLCrossOriginRejected_C2(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		payload string
		errSub  string
	}{
		{
			name:    "different host",
			baseURL: "https://203.0.113.1:3001/sse",
			payload: "event: endpoint\ndata: https://evil.example.com:443/msg\n\n",
			errSub:  "same-origin",
		},
		{
			name:    "different port",
			baseURL: "https://203.0.113.1:3001/sse",
			payload: "event: endpoint\ndata: https://203.0.113.1:9999/msg\n\n",
			errSub:  "same-origin",
		},
		{
			name:    "different scheme http",
			baseURL: "https://203.0.113.1:3001/sse",
			payload: "event: endpoint\ndata: http://203.0.113.1:3001/msg\n\n",
			errSub:  "same-origin",
		},
		{
			name:    "postURL targets internal IP even if same-origin-ish host",
			baseURL: "https://203.0.113.1:3001/sse",
			payload: "event: endpoint\ndata: https://169.254.169.254:3001/msg\n\n",
			errSub:  "same-origin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newSSETransport(MCPServerConfig{Transport: "sse", URL: tc.baseURL})
			tr.readLoop(io.NopCloser(strings.NewReader(tc.payload)))
			if tr.postURL != "" {
				t.Errorf("postURL should remain empty on rejection, got %q", tr.postURL)
			}
			if tr.postURLErr == nil {
				t.Fatal("expected postURLErr, got nil")
			}
			if !strings.Contains(tr.postURLErr.Error(), tc.errSub) {
				t.Errorf("postURLErr = %q, want substring %q", tr.postURLErr, tc.errSub)
			}
			// Recv must surface the rejection rather than a plain EOF.
			_, err := tr.Recv()
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("Recv err = %v, want substring %q", err, tc.errSub)
			}
		})
	}
}

// C-2: a same-origin, SSRF-safe postURL is accepted.
func TestSSETransport_PostURLSameOriginAccepted_C2(t *testing.T) {
	tr := newSSETransport(MCPServerConfig{Transport: "sse", URL: "https://203.0.113.1:3001/sse"})
	tr.readLoop(io.NopCloser(strings.NewReader("event: endpoint\ndata: https://203.0.113.1:3001/msg\n\n")))
	if tr.postURL != "https://203.0.113.1:3001/msg" {
		t.Errorf("postURL = %q, want accepted same-origin URL", tr.postURL)
	}
	if tr.postURLErr != nil {
		t.Errorf("postURLErr should be nil for same-origin, got: %v", tr.postURLErr)
	}
}

// C-2: unit-test the same-origin helper directly for edge cases.
func TestSameOriginURL_C2(t *testing.T) {
	parse := func(s string) *url.URL {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return u
	}
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "https://h:1/p", "https://h:1/q", true},
		{"scheme case", "HTTPS://h:1/p", "https://h:1/p", true},
		{"host case", "https://H:1/p", "https://h:1/p", true},
		{"default ports", "https://h", "https://h:443", true},
		{"diff scheme", "http://h:1/p", "https://h:1/p", false},
		{"diff host", "https://a:1/p", "https://b:1/p", false},
		{"diff port", "https://h:1/p", "https://h:2/p", false},
		{"nil a", "", "https://h:1/p", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a, b *url.URL
			if tc.a != "" {
				a = parse(tc.a)
			}
			if tc.b != "" {
				b = parse(tc.b)
			}
			if got := sameOriginURL(a, b); got != tc.want {
				t.Errorf("sameOriginURL(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// blockingReadCloser 模拟一个永不发送数据的 SSE 连接：Read 阻塞直到
// Close 被调用，复现 readLoop 卡在 scanner.Scan() 上的场景（H-6）。
type blockingReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (b *blockingReadCloser) Read(p []byte) (int, error) {
	<-b.closed
	return 0, errors.New("connection closed")
}

func (b *blockingReadCloser) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// H-6: Close 后 readLoop goroutine 必须退出，不能泄漏。
// readLoop 阻塞在 scanner.Scan() 上时，仅 close(done) 无法唤醒它；
// Close 必须先关闭 body 解除 Scan 阻塞，再用 WaitGroup 等待 readLoop 退出。
func TestSSETransport_H6_CloseReclaimsReadLoopGoroutine(t *testing.T) {
	tr := newSSETransport(MCPServerConfig{Transport: "sse", URL: "https://203.0.113.1:3001/sse"})
	body := newBlockingReadCloser()

	// 在启动 readLoop 前测量基线 goroutine 数。
	baseline := runtime.NumGoroutine()

	// 模拟 connect() 设置 body 引用并启动 readLoop。
	tr.body = body
	tr.wg.Add(1)
	go func() {
		defer tr.wg.Done()
		tr.readLoop(body)
	}()

	// 等待 readLoop 启动并阻塞在 scanner.Scan() 上。
	time.Sleep(100 * time.Millisecond)
	withLoop := runtime.NumGoroutine()
	if withLoop <= baseline {
		t.Logf("警告: readLoop 似乎未启动 (baseline=%d, withLoop=%d)", baseline, withLoop)
	}

	// Close 必须解除 readLoop 阻塞并等待其退出；若未关闭 body 或未等待，
	// 要么 Close 立即返回但 goroutine 泄漏，要么 Close 永久挂起。
	done := make(chan error, 1)
	go func() { done <- tr.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close 返回错误: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close 超时 — readLoop goroutine 泄漏（未解除 Scan 阻塞）")
	}

	// 等待 goroutine 回收后检查计数：修复后 readLoop 应已退出，
	// goroutine 数应回到基线水平。
	time.Sleep(200 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > baseline {
		t.Errorf("goroutine 泄漏: 基线=%d, close 后=%d (readLoop 未退出)", baseline, after)
	}
}

// H-6: 多次调用 Close 不应 panic 或死锁（幂等性）。
func TestSSETransport_H6_CloseIdempotent(t *testing.T) {
	tr := newSSETransport(MCPServerConfig{Transport: "sse", URL: "https://203.0.113.1:3001/sse"})
	body := newBlockingReadCloser()
	tr.body = body
	tr.wg.Add(1)
	go func() {
		defer tr.wg.Done()
		tr.readLoop(body)
	}()

	time.Sleep(50 * time.Millisecond)

	// 连续两次 Close 不应 panic。
	if err := tr.Close(); err != nil {
		t.Fatalf("第一次 Close 错误: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("第二次 Close 错误: %v", err)
	}
}

// TestSSETransportConcurrentClose (N-2) 验证并发 connect / Close 不产生
// race / panic / goroutine 泄漏。原实现 body/wg 字段无锁保护，并发场景下：
//   - Close 读 body==nil 跳过 wg.Wait，但 connect 随后 wg.Add(1) → goroutine 泄漏
//   - 或 wg.Wait 在 wg.Add 之前调用 → panic
//
// 修复后 mu 保护 body/wg。本测试模拟 connect 成功后 readLoop 运行中场景，
// 并发 Close + Send + Recv 100 次迭代，验证无 race 无泄漏。
// 用 blockingReadCloser 模拟阻塞 SSE 流，避免依赖真实网络（HTTP transport
// 内部 goroutine 在连接失败后会滞留，干扰 goroutine 计数）。
func TestSSETransportConcurrentClose(t *testing.T) {
	baseline := runtime.NumGoroutine()

	const iterations = 100
	for i := 0; i < iterations; i++ {
		tr := newSSETransport(MCPServerConfig{Transport: "sse", URL: "https://203.0.113.1:3001/sse"})
		body := newBlockingReadCloser()

		// 模拟 connect 成功：持锁设置 body + wg.Add（与 connect() 修复后逻辑一致）
		tr.mu.Lock()
		tr.body = body
		tr.wg.Add(1)
		tr.mu.Unlock()
		go func() {
			defer tr.wg.Done()
			tr.readLoop(body)
		}()

		// 等待 readLoop 阻塞在 scanner.Scan() 上
		time.Sleep(2 * time.Millisecond)

		// 并发 Close + Recv + Send（Send 因 postURL 为空快速失败）
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = tr.Close()
		}()
		go func() {
			defer wg.Done()
			_, _ = tr.Recv()
		}()
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_ = tr.Send(ctx, &jsonrpcOutboundMessage{Method: "ping"})
		}()
		wg.Wait()
	}

	// 等待 goroutine 回收
	time.Sleep(300 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > baseline+5 { // 允许少量波动
		t.Errorf("goroutine 泄漏: 基线=%d, 100 次迭代后=%d", baseline, after)
	}
}

// TestSSETransportConcurrentClose_AfterConnect (N-2) 验证单次 readLoop 运行中
// 并发 Close / Send / Recv 不产生 race（-race detector 重点场景）。
func TestSSETransportConcurrentClose_AfterConnect(t *testing.T) {
	tr := newSSETransport(MCPServerConfig{Transport: "sse", URL: "https://203.0.113.1:3001/sse"})
	body := newBlockingReadCloser()

	// 模拟 connect 已成功设置 body 并启动 readLoop
	tr.mu.Lock()
	tr.body = body
	tr.wg.Add(1)
	tr.mu.Unlock()
	go func() {
		defer tr.wg.Done()
		tr.readLoop(body)
	}()

	time.Sleep(50 * time.Millisecond)

	// 并发 Close + Recv + Send（Send 会因 postURL 为空快速失败）
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		_ = tr.Close()
	}()
	go func() {
		defer wg.Done()
		_, _ = tr.Recv()
	}()
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = tr.Send(ctx, &jsonrpcOutboundMessage{Method: "ping"})
	}()
	wg.Wait()
}

// --- N-4: ConnectServer 与 DeleteServer 生命周期竞态 ---

// TestHelperFakeMCPServer 是 test binary reexec helper (N-4)。
// 当 KOYORI_IDE_FAKE_MCP=1 时，它作为 stdio MCP server 运行：
// 读 initialize 请求 → 响应 → 读 initialized notification → hang。
// 响应前可选 sleep（KOYORI_IDE_FAKE_MCP_DELAY），让父测试有机会在
// StartServer 期间执行 DeleteServer。
//
// Go testing framework 的 === RUN / --- PASS 等输出写到 stderr，
// stdout 仅含本 helper 显式写入的 MCP JSON-RPC 响应，不会干扰协议。
func TestHelperFakeMCPServer(t *testing.T) {
	if os.Getenv("KOYORI_IDE_FAKE_MCP") != "1" {
		t.Skip("helper: only runs as reexec fake MCP server")
	}
	reader := bufio.NewReader(os.Stdin)
	if d := os.Getenv("KOYORI_IDE_FAKE_MCP_DELAY"); d != "" {
		if dur, err := time.ParseDuration(d); err == nil {
			time.Sleep(dur)
		}
	}
	// 读 initialize 请求（一行 JSON-RPC）
	line, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-mcp: read initialize: %v\n", err)
		os.Exit(1)
	}
	var req struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		fmt.Fprintf(os.Stderr, "fake-mcp: parse initialize: %v\n", err)
		os.Exit(1)
	}
	// 响应 initialize（MCP 2024-11-05 协议）
	resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"capabilities":{},"protocolVersion":"2024-11-05","serverInfo":{"name":"fake","version":"1.0"}}}`+"\n", req.ID)
	if _, err := os.Stdout.WriteString(resp); err != nil {
		fmt.Fprintf(os.Stderr, "fake-mcp: write response: %v\n", err)
		os.Exit(1)
	}
	// 读 initialized notification（无响应）
	reader.ReadString('\n')
	// hang，等待被 kill（父测试的 client.StopServer → stdioTransport.Close 会 kill 进程）
	select {}
}

// TestConnectDeleteRace (N-4) 验证 ConnectServer 进行中调用 DeleteServer
// 不产生孤儿连接。
//
// 场景：ConnectServer 释放锁后调用 StartServer（耗时），期间 DeleteServer
// 删除配置。StartServer 成功后重新加锁，必须检测到配置已删除并清理 client。
func TestConnectDeleteRace(t *testing.T) {
	s := newTestMCPService(t)
	if err := s.setWorkspaceRoot(filepath.Dir(os.Args[0])); err != nil {
		t.Fatalf("setWorkspaceRoot: %v", err)
	}
	cfg := MCPServerConfig{
		Name:      "race-srv",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestHelperFakeMCPServer$", "-test.timeout=60s"},
		Env: map[string]string{
			"KOYORI_IDE_FAKE_MCP":       "1",
			"KOYORI_IDE_FAKE_MCP_DELAY": "300ms",
		},
	}
	if err := s.SaveServer(cfg); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	// G-SEC-12: new servers require an explicit enable operation.
	if err := s.SetServerEnabled(cfg.Name, true); err != nil {
		t.Fatalf("SetServerEnabled: %v", err)
	}

	// 启动 ConnectServer（StartServer 会阻塞在 fake server 的 300ms delay 上）
	connectErr := make(chan error, 1)
	go func() {
		connectErr <- s.ConnectServer(context.Background(), "race-srv")
	}()

	// 等待 ConnectServer 进入 StartServer
	time.Sleep(100 * time.Millisecond)

	// 在 StartServer 期间删除 server 配置
	if err := s.DeleteServer("race-srv"); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}

	// 等待 ConnectServer 完成
	err := <-connectErr
	if err == nil {
		t.Fatal("ConnectServer 应失败（server 在连接期间被删除），但返回 nil")
	}
	if !strings.Contains(err.Error(), "deleted during connect") {
		t.Fatalf("期望 'deleted during connect' 错误，得到: %v", err)
	}

	// 验证无孤儿连接：clients 中不应残留 race-srv
	s.mu.Lock()
	_, hasClient := s.clients["race-srv"]
	s.mu.Unlock()
	if hasClient {
		t.Fatal("产生孤儿连接：clients 中残留 race-srv (N-4 回归)")
	}

	// 给 fake server 进程一点时间被 kill 回收
	time.Sleep(100 * time.Millisecond)
}

// TestConnectDeleteRace_NoOrphan_WhenConfigSurvives (N-4) 验证正常路径：
// ConnectServer 期间不删除配置时，连接成功建立。
func TestConnectDeleteRace_NoOrphan_WhenConfigSurvives(t *testing.T) {
	s := newTestMCPService(t)
	if err := s.setWorkspaceRoot(filepath.Dir(os.Args[0])); err != nil {
		t.Fatalf("setWorkspaceRoot: %v", err)
	}
	cfg := MCPServerConfig{
		Name:      "ok-srv",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestHelperFakeMCPServer$", "-test.timeout=60s"},
		Env: map[string]string{
			"KOYORI_IDE_FAKE_MCP": "1",
		},
	}
	if err := s.SaveServer(cfg); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	if err := s.SetServerEnabled(cfg.Name, true); err != nil {
		t.Fatalf("SetServerEnabled: %v", err)
	}

	// 正常连接，不删除配置
	if err := s.ConnectServer(context.Background(), "ok-srv"); err != nil {
		t.Fatalf("ConnectServer 正常路径应成功: %v", err)
	}
	// 验证 client 已建立
	s.mu.Lock()
	client, ok := s.clients["ok-srv"]
	s.mu.Unlock()
	if !ok {
		t.Fatal("正常连接后 clients 应包含 ok-srv")
	}
	// 清理：断开连接
	if err := s.DisconnectServer("ok-srv"); err != nil {
		t.Logf("cleanup DisconnectServer: %v", err)
	}
	_ = client
	time.Sleep(100 * time.Millisecond)
}
