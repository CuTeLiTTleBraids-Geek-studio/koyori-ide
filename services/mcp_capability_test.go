package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// P1-03-A fixtures
// ---------------------------------------------------------------------------

// mcpTestToolsCapability declares exactly the tools surface the scripted
// test doubles implement. Fixtures must declare the capabilities they really
// answer, mirroring what production servers must do.
const mcpTestToolsCapability = `{"tools":{"listChanged":false}}`

// scriptedMCPInitializeHandler answers the initialize request with a valid
// result declaring capabilitiesJSON, then delegates every other method to
// next (which may be nil).
func scriptedMCPInitializeHandler(capabilitiesJSON string, next func(*jsonrpcOutboundMessage, int) *jsonrpcResponse) func(*jsonrpcOutboundMessage, int) *jsonrpcResponse {
	return func(req *jsonrpcOutboundMessage, sendNumber int) *jsonrpcResponse {
		if req.Method == "initialize" {
			return &jsonrpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(fmt.Sprintf(
					`{"protocolVersion":"2024-11-05","capabilities":%s,"serverInfo":{"name":"fixture-server","version":"1.0"}}`,
					capabilitiesJSON,
				)),
			}
		}
		if next == nil {
			return nil
		}
		return next(req, sendNumber)
	}
}

// initializeScriptedMCPClient performs the real initialize handshake on a
// scripted client whose transport answers initialize. The capability model
// makes every later list/call API require this handshake.
func initializeScriptedMCPClient(t *testing.T, client *MCPClient) {
	t.Helper()
	if err := client.initialize(context.Background()); err != nil {
		t.Fatalf("initialize scripted client: %v", err)
	}
}

// capturedMCPRequest is one JSON-RPC message a fixture received.
type capturedMCPRequest struct {
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id"`
	Params json.RawMessage `json:"params"`
}

// startMCPHTTPFixture starts a real local HTTP JSON-RPC fixture and points
// the streamable HTTP transport client factory at it. The production SSRF
// guard refuses loopback dials (C-1), so the override exists for fixtures
// only and is restored on cleanup.
func startMCPHTTPFixture(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previousClientFactory := newMCPHTTPClient
	newMCPHTTPClient = func() *http.Client { return server.Client() }
	t.Cleanup(func() { newMCPHTTPClient = previousClientFactory })
	return server
}

// mcpHTTPFixtureHandler records every received JSON-RPC message and answers:
//   - initialize with initializeResult,
//   - notifications with HTTP 202 and an empty body,
//   - every other request via respond (status, JSON-RPC payload).
func mcpHTTPFixtureHandler(record *[]capturedMCPRequest, mu *sync.Mutex, initializeResult string, respond func(req capturedMCPRequest) (int, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req capturedMCPRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		*record = append(*record, req)
		mu.Unlock()
		writePayload := func(status int, payload string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(payload))
		}
		if req.Method == "initialize" {
			writePayload(http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, string(req.ID), initializeResult))
			return
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if respond != nil {
			status, payload := respond(req)
			writePayload(status, payload)
			return
		}
		writePayload(http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"fixture does not implement %s"}}`, string(req.ID), req.Method))
	}
}

func (r capturedMCPRequest) isNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

// newHTTPFixtureClient starts a client over the real HTTP fixture transport.
func newHTTPFixtureClient(t *testing.T, server *httptest.Server, name string) *MCPClient {
	t.Helper()
	client := newMCPClient(MCPServerConfig{Name: name, Transport: "http", URL: server.URL})
	if err := client.StartServer(context.Background()); err != nil {
		t.Fatalf("start %q over HTTP fixture: %v", name, err)
	}
	t.Cleanup(func() { _ = client.StopServer() })
	return client
}

// ---------------------------------------------------------------------------
// P1-03-A: initialize capability exchange
// ---------------------------------------------------------------------------

func TestMCPClientInitializeCapabilitySnapshot_HTTPFixture(t *testing.T) {
	var mu sync.Mutex
	var requests []capturedMCPRequest
	server := startMCPHTTPFixture(t, mcpHTTPFixtureHandler(&requests, &mu,
		`{"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":true},"resources":{"subscribe":true,"listChanged":true},"prompts":{"listChanged":true},"sampling":{},"logging":{}},"serverInfo":{"name":"cap-fixture","version":"2.3.1"},"instructions":"use tools carefully"}`,
		nil))

	client := newHTTPFixtureClient(t, server, "cap-fixture")

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("fixture request count = %d, want initialize + initialized notification", len(requests))
	}
	init := requests[0]
	if init.Method != "initialize" || init.isNotification() {
		t.Fatalf("first fixture request = %+v, want a initialize request with an ID", init)
	}
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if err := json.Unmarshal(init.Params, &params); err != nil {
		t.Fatalf("unmarshal initialize params: %v", err)
	}
	if params.ProtocolVersion != mcpProtocolVersion {
		t.Fatalf("initialize protocolVersion = %q, want %q", params.ProtocolVersion, mcpProtocolVersion)
	}
	if params.ClientInfo.Name != "koyori-ide" || params.ClientInfo.Version != "1.0" {
		t.Fatalf("initialize clientInfo = %+v, want koyori-ide 1.0", params.ClientInfo)
	}
	// The client implements the controlled roots/list handler, so the honest
	// declaration is exactly {"roots":{}} — no sampling, nothing else.
	if len(params.Capabilities) != 1 {
		t.Fatalf("initialize client capabilities = %s, want exactly {roots:{}}", init.Params)
	}
	if _, ok := params.Capabilities["roots"]; !ok {
		t.Fatalf("initialize client capabilities missing roots declaration: %s", init.Params)
	}
	if requests[1].Method != "notifications/initialized" || !requests[1].isNotification() {
		t.Fatalf("second fixture request = %+v, want an initialized notification without an ID", requests[1])
	}

	snapshot, err := client.capabilitySnapshotCopy()
	if err != nil {
		t.Fatalf("capability snapshot after initialize: %v", err)
	}
	if snapshot.ProtocolVersion != mcpProtocolVersion || snapshot.Run == 0 {
		t.Fatalf("snapshot identity = %+v, want protocol %q and a run ID", snapshot, mcpProtocolVersion)
	}
	if snapshot.ServerInfo.Name != "cap-fixture" || snapshot.ServerInfo.Version != "2.3.1" {
		t.Fatalf("snapshot serverInfo = %+v, want cap-fixture 2.3.1", snapshot.ServerInfo)
	}
	if snapshot.Instructions != "use tools carefully" {
		t.Fatalf("snapshot instructions = %q", snapshot.Instructions)
	}
	if snapshot.ServerName != "cap-fixture" {
		t.Fatalf("snapshot server name = %q, want the config identity", snapshot.ServerName)
	}
	assertFeature := func(what string, feature MCPCapabilityFeature, wantState MCPCapabilityState, wantDeclared bool, wantListChanged bool, wantSubscribe bool) {
		t.Helper()
		if feature.State != wantState || feature.Declared != wantDeclared || feature.ListChanged != wantListChanged || feature.Subscribe != wantSubscribe {
			t.Fatalf("%s feature = %+v, want state=%s declared=%v listChanged=%v subscribe=%v", what, feature, wantState, wantDeclared, wantListChanged, wantSubscribe)
		}
	}
	assertFeature("tools", snapshot.Capabilities.Tools, MCPCapabilitySupported, true, true, false)
	assertFeature("resources", snapshot.Capabilities.Resources, MCPCapabilitySupported, true, true, true)
	assertFeature("prompts", snapshot.Capabilities.Prompts, MCPCapabilitySupported, true, true, false)
	assertFeature("sampling", snapshot.Capabilities.Sampling, MCPCapabilityUnsupported, true, false, false)
	assertFeature("elicitation", snapshot.Capabilities.Elicitation, MCPCapabilityUnsupported, false, false, false)
	assertFeature("logging", snapshot.Capabilities.Logging, MCPCapabilityUnsupported, true, false, false)
	if len(snapshot.Capabilities.Unknown) != 0 {
		t.Fatalf("unknown capabilities = %v, want none", snapshot.Capabilities.Unknown)
	}
}

func TestMCPClientInitializeRejectsInvalidResults_HTTPFixture(t *testing.T) {
	cases := []struct {
		what     string
		result   string
		wantErr  error
		fragment string
	}{
		{"missing protocolVersion", `{"capabilities":{},"serverInfo":{"name":"s","version":"1"}}`, ErrInvalidInput, "protocolVersion"},
		{"unsupported protocolVersion", `{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"s","version":"1"}}`, ErrNotAllowed, "not supported"},
		{"missing serverInfo", `{"protocolVersion":"2024-11-05","capabilities":{}}`, ErrInvalidInput, "serverInfo.name"},
		{"missing serverInfo version", `{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"s"}}`, ErrInvalidInput, "serverInfo.version"},
		{"malformed capabilities", `{"protocolVersion":"2024-11-05","capabilities":"yes","serverInfo":{"name":"s","version":"1"}}`, ErrInvalidInput, "malformed initialize capabilities"},
		{"malformed tools capability", `{"protocolVersion":"2024-11-05","capabilities":{"tools":"yes"},"serverInfo":{"name":"s","version":"1"}}`, ErrInvalidInput, "malformed tools capability"},
	}
	for _, testCase := range cases {
		t.Run(testCase.what, func(t *testing.T) {
			var mu sync.Mutex
			var requests []capturedMCPRequest
			server := startMCPHTTPFixture(t, mcpHTTPFixtureHandler(&requests, &mu, testCase.result, nil))

			client := newMCPClient(MCPServerConfig{Name: "invalid-fixture", Transport: "http", URL: server.URL})
			err := client.StartServer(context.Background())
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("StartServer error = %v, want %v", err, testCase.wantErr)
			}
			if !strings.Contains(err.Error(), testCase.fragment) {
				t.Fatalf("StartServer error = %v, want fragment %q", err, testCase.fragment)
			}
			mu.Lock()
			requestCount := len(requests)
			mu.Unlock()
			if requestCount != 1 {
				t.Fatalf("fixture request count = %d, want only the rejected initialize (no initialized notification)", requestCount)
			}
			// The failed handshake must leave no usable snapshot behind.
			if _, snapshotErr := client.capabilitySnapshotCopy(); !errors.Is(snapshotErr, ErrNotFound) {
				t.Fatalf("snapshot after failed initialize = %v, want ErrNotFound", snapshotErr)
			}
			if _, listErr := client.ListTools(context.Background()); !errors.Is(listErr, ErrInvalidInput) {
				t.Fatalf("ListTools after failed initialize = %v, want ErrInvalidInput", listErr)
			}
		})
	}
}

func TestMCPClientInitializeRecordsMissingAndUnknownCapabilities_HTTPFixture(t *testing.T) {
	t.Run("declared but unknown and unsupported keys", func(t *testing.T) {
		var mu sync.Mutex
		var requests []capturedMCPRequest
		server := startMCPHTTPFixture(t, mcpHTTPFixtureHandler(&requests, &mu,
			`{"protocolVersion":"2024-11-05","capabilities":{"sampling":{},"elicitation":{},"vendorFeature":{}},"serverInfo":{"name":"s","version":"1"}}`,
			nil))
		client := newHTTPFixtureClient(t, server, "unknown-cap")

		snapshot, err := client.capabilitySnapshotCopy()
		if err != nil {
			t.Fatalf("capability snapshot: %v", err)
		}
		if snapshot.Capabilities.Tools.State != MCPCapabilityMissing ||
			snapshot.Capabilities.Resources.State != MCPCapabilityMissing ||
			snapshot.Capabilities.Prompts.State != MCPCapabilityMissing {
			t.Fatalf("undeclared list capabilities = %+v, want all missing", snapshot.Capabilities)
		}
		if !snapshot.Capabilities.Sampling.Declared || snapshot.Capabilities.Sampling.State != MCPCapabilityUnsupported {
			t.Fatalf("sampling = %+v, want declared and unsupported", snapshot.Capabilities.Sampling)
		}
		if !snapshot.Capabilities.Elicitation.Declared || snapshot.Capabilities.Elicitation.State != MCPCapabilityUnsupported {
			t.Fatalf("elicitation = %+v, want declared and unsupported", snapshot.Capabilities.Elicitation)
		}
		if snapshot.Capabilities.Logging.Declared || snapshot.Capabilities.Logging.State != MCPCapabilityUnsupported {
			t.Fatalf("logging = %+v, want undeclared and unsupported", snapshot.Capabilities.Logging)
		}
		if len(snapshot.Capabilities.Unknown) != 1 || snapshot.Capabilities.Unknown[0] != "vendorFeature" {
			t.Fatalf("unknown capabilities = %v, want [vendorFeature]", snapshot.Capabilities.Unknown)
		}
	})
	t.Run("capabilities key absent entirely", func(t *testing.T) {
		var mu sync.Mutex
		var requests []capturedMCPRequest
		server := startMCPHTTPFixture(t, mcpHTTPFixtureHandler(&requests, &mu,
			`{"protocolVersion":"2024-11-05","serverInfo":{"name":"s","version":"1"}}`,
			nil))
		client := newHTTPFixtureClient(t, server, "absent-cap")

		snapshot, err := client.capabilitySnapshotCopy()
		if err != nil {
			t.Fatalf("capability snapshot: %v", err)
		}
		if snapshot.Capabilities.Tools.State != MCPCapabilityMissing || snapshot.Capabilities.Sampling.State != MCPCapabilityUnsupported {
			t.Fatalf("snapshot without capabilities = %+v, want honest empty state", snapshot.Capabilities)
		}
	})
}

func TestMCPClientCapabilityGating_HTTPFixture(t *testing.T) {
	var mu sync.Mutex
	var requests []capturedMCPRequest
	server := startMCPHTTPFixture(t, mcpHTTPFixtureHandler(&requests, &mu,
		`{"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":false},"prompts":{"listChanged":false}},"serverInfo":{"name":"gate-fixture","version":"1"}}`,
		func(req capturedMCPRequest) (int, string) {
			id := string(req.ID)
			switch req.Method {
			case "tools/list":
				return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo","description":"echoes","inputSchema":{"type":"object"}}]}}`, id)
			case "tools/call":
				return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"tool-ok"}]}}`, id)
			case "prompts/list":
				return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"prompts":[{"name":"greet"}]}}`, id)
			case "prompts/get":
				return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"messages":[{"role":"user","content":{"type":"text","text":"prompt-ok"}}]}}`, id)
			case "resources/list", "resources/read":
				return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"resources were never declared"}}`, id)
			}
			return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"unimplemented"}}`, id)
		}))

	client := newHTTPFixtureClient(t, server, "gate-fixture")

	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("ListTools = %#v, %v; want the declared echo tool", tools, err)
	}
	result, err := client.CallTool(context.Background(), "echo", map[string]interface{}{"value": "x"})
	if err != nil || result.IsError || len(result.Content) != 1 || result.Content[0].Text != "tool-ok" {
		t.Fatalf("CallTool = %+v, %v; want tool-ok", result, err)
	}
	prompts, err := client.ListPrompts(context.Background())
	if err != nil || len(prompts) != 1 || prompts[0].Name != "greet" {
		t.Fatalf("ListPrompts = %#v, %v; want the declared greet prompt", prompts, err)
	}
	contents, err := client.GetPrompt(context.Background(), "greet", map[string]string{})
	if err != nil || len(contents) != 1 || contents[0].Role != "user" || contents[0].Content.Text != "prompt-ok" {
		t.Fatalf("GetPrompt = %#v, %v; want user role with prompt-ok", contents, err)
	}

	// The server never declared resources, so both resource APIs must fail
	// closed without issuing any JSON-RPC request at all.
	if _, err := client.ListResources(context.Background()); !errors.Is(err, ErrNotAllowed) || !strings.Contains(err.Error(), "did not declare the resources capability") {
		t.Fatalf("ListResources error = %v, want the explicit undeclared-capability error", err)
	}
	if _, err := client.ReadResource(context.Background(), "file:///nothing"); !errors.Is(err, ErrNotAllowed) || !strings.Contains(err.Error(), "did not declare the resources capability") {
		t.Fatalf("ReadResource error = %v, want the explicit undeclared-capability error", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, req := range requests {
		if req.Method == "resources/list" || req.Method == "resources/read" {
			t.Fatalf("undeclared resources capability still issued %s", req.Method)
		}
	}
}

// ---------------------------------------------------------------------------
// P1-03-A: service-level snapshot lifecycle over a real stdio fixture
// ---------------------------------------------------------------------------

func TestMCPServiceCapabilitySnapshotLifecycle_StdioFixture(t *testing.T) {
	root := filepath.Dir(os.Args[0])
	service := newTestMCPService(t)
	if err := service.setWorkspaceRoot(root); err != nil {
		t.Fatalf("setWorkspaceRoot: %v", err)
	}
	cfg := MCPServerConfig{
		Name:      "cap-lifecycle",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestHelperFakeMCPServer$", "-test.timeout=60s"},
		Env:       map[string]string{"KOYORI_IDE_FAKE_MCP": "1"},
	}
	if err := service.SaveServer(cfg); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	if err := service.SetServerEnabled(cfg.Name, true); err != nil {
		t.Fatalf("SetServerEnabled: %v", err)
	}
	if err := service.ConnectServer(context.Background(), cfg.Name); err != nil {
		t.Fatalf("ConnectServer: %v", err)
	}

	snapshot, err := service.ServerCapabilities(cfg.Name)
	if err != nil {
		t.Fatalf("ServerCapabilities: %v", err)
	}
	if snapshot.ServerName != cfg.Name || snapshot.Run == 0 {
		t.Fatalf("snapshot identity = %+v, want config name and a run ID", snapshot)
	}
	if !sameWorkspaceIdentityPath(snapshot.WorkspaceRoot, root) {
		t.Fatalf("snapshot workspace root = %q, want %q", snapshot.WorkspaceRoot, root)
	}
	service.mu.RLock()
	rootGeneration, lifecycleGeneration := service.rootGeneration, service.lifecycleGeneration
	service.mu.RUnlock()
	if snapshot.RootGeneration != rootGeneration || snapshot.LifecycleGeneration != lifecycleGeneration {
		t.Fatalf("snapshot generations = %d/%d, want %d/%d", snapshot.RootGeneration, snapshot.LifecycleGeneration, rootGeneration, lifecycleGeneration)
	}
	if snapshot.ProtocolVersion != mcpProtocolVersion || snapshot.Capabilities.Tools.State != MCPCapabilityMissing {
		t.Fatalf("stdio fixture declares no tools: snapshot = %+v", snapshot.Capabilities)
	}

	if err := service.DisconnectServer(cfg.Name); err != nil {
		t.Fatalf("DisconnectServer: %v", err)
	}
	if _, err := service.ServerCapabilities(cfg.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ServerCapabilities after disconnect = %v, want ErrNotFound", err)
	}

	// Reconnect must produce a brand-new snapshot identity.
	if err := service.ConnectServer(context.Background(), cfg.Name); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	reconnected, err := service.ServerCapabilities(cfg.Name)
	if err != nil {
		t.Fatalf("ServerCapabilities after reconnect: %v", err)
	}
	if reconnected.Run <= snapshot.Run {
		t.Fatalf("reconnected snapshot run = %d, want a new run greater than %d", reconnected.Run, snapshot.Run)
	}

	// An equivalent config save bumps the lifecycle generation without
	// touching the connection, so the established snapshot fails closed
	// until a new initialize completes.
	if err := service.SaveServer(cfg); err != nil {
		t.Fatalf("equivalent SaveServer: %v", err)
	}
	if _, err := service.ServerCapabilities(cfg.Name); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ServerCapabilities after config mutation = %v, want ErrNotAllowed", err)
	}

	// A workspace switch detaches every client: the old snapshot must not be
	// returned or used anymore.
	rootB := t.TempDir()
	if err := service.applyWorkspaceRoot(rootB); err != nil {
		t.Fatalf("applyWorkspaceRoot: %v", err)
	}
	if _, err := service.ServerCapabilities(cfg.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ServerCapabilities after workspace switch = %v, want ErrNotFound", err)
	}
}

func TestMCPClientSnapshotClearedOnStopAndReplacedOnReconnect(t *testing.T) {
	var mu sync.Mutex
	var requests []capturedMCPRequest
	server := startMCPHTTPFixture(t, mcpHTTPFixtureHandler(&requests, &mu,
		`{"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":false}},"serverInfo":{"name":"reconnect-cap","version":"1"}}`,
		func(req capturedMCPRequest) (int, string) {
			if req.Method == "tools/list" {
				return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}}`, string(req.ID))
			}
			return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"unimplemented"}}`, string(req.ID))
		}))

	client := newMCPClient(MCPServerConfig{Name: "reconnect-cap", Transport: "http", URL: server.URL})
	if err := client.StartServer(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	first, err := client.capabilitySnapshotCopy()
	if err != nil {
		t.Fatalf("snapshot before stop: %v", err)
	}
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("ListTools = %#v, %v; want the declared tool", tools, err)
	}

	if err := client.StopServer(); err != nil {
		t.Fatalf("StopServer: %v", err)
	}
	if _, err := client.capabilitySnapshotCopy(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snapshot after stop = %v, want ErrNotFound", err)
	}
	if _, err := client.ListTools(context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ListTools after stop = %v, want ErrInvalidInput", err)
	}

	// Production reconnects build a fresh client; its initialize must produce
	// a new globally unique snapshot identity, never the stopped run's.
	reconnected := newMCPClient(MCPServerConfig{Name: "reconnect-cap", Transport: "http", URL: server.URL})
	if err := reconnected.StartServer(context.Background()); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	second, err := reconnected.capabilitySnapshotCopy()
	if err != nil {
		t.Fatalf("snapshot after reconnect: %v", err)
	}
	if second.Run <= first.Run {
		t.Fatalf("reconnected run = %d, want greater than %d", second.Run, first.Run)
	}
	if _, err := reconnected.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools after reconnect: %v", err)
	}
}

// startInitializedScriptedMCPClient returns a client whose scripted transport
// completes a real initialize handshake (with the given capability JSON)
// before delegating every other method to handler.
func startInitializedScriptedMCPClient(t *testing.T, name, capabilitiesJSON string, handler func(*jsonrpcOutboundMessage, int) *jsonrpcResponse) (*MCPClient, *scriptedMCPTransport) {
	t.Helper()
	transport := newScriptedMCPTransport(scriptedMCPInitializeHandler(capabilitiesJSON, handler))
	client := newScriptedMCPClient(name, transport)
	initializeScriptedMCPClient(t, client)
	t.Cleanup(func() { _ = client.StopServer() })
	return client, transport
}

// ---------------------------------------------------------------------------
// P1-03-B: server notification/request dispatch over a real stdio fixture
// ---------------------------------------------------------------------------

// TestHelperMCPNotificationServer is the reexec stdio MCP server used by the
// P1-03-B dispatch tests. It answers initialize and tools/list (naming each
// response tool-v<count>), and after the first tools/list it performs the
// KOYORI_MCP_AFTER_TRIGGER actions: sending notifications, sending
// server-to-client requests (recording the client's response in
// KOYORI_MCP_RESULT_FILE), and writing a malformed JSON-RPC line. Actions
// wait until KOYORI_MCP_TRIGGER_FILE exists when that path is set.
func TestHelperMCPNotificationServer(t *testing.T) {
	if os.Getenv("KOYORI_MCP_NOTIFICATION_HELPER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	sendLine := func(payload string) {
		if _, err := writer.WriteString(payload + "\n"); err != nil {
			fmt.Fprintf(os.Stderr, "notification helper: write: %v\n", err)
			os.Exit(1)
		}
		if err := writer.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "notification helper: flush: %v\n", err)
			os.Exit(1)
		}
	}
	respond := func(id json.RawMessage, result string) {
		sendLine(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, string(id), result))
	}

	toolsLists := 0
	resourcesLists := 0
	promptsLists := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				URI  string `json:"uri"`
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			fmt.Fprintf(os.Stderr, "notification helper: decode: %v\n", err)
			continue
		}
		switch req.Method {
		case "initialize":
			respond(req.ID, `{"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":true},"resources":{"listChanged":true},"prompts":{"listChanged":true}},"serverInfo":{"name":"notification-fixture","version":"1.0"}}`)
		case "", "notifications/initialized":
			// The initialized notification needs no answer.
		case "tools/list":
			toolsLists++
			respond(req.ID, fmt.Sprintf(`{"tools":[{"name":"tool-v%d","inputSchema":{"type":"object"}}]}`, toolsLists))
			if toolsLists == 1 {
				helperNotificationTriggerActions(t, reader, sendLine)
			}
		case "resources/list":
			resourcesLists++
			respond(req.ID, fmt.Sprintf(`{"resources":[{"uri":"fixture://resource-v%d","name":"resource-v%d","mimeType":"text/plain"}]}`, resourcesLists, resourcesLists))
		case "resources/read":
			if req.Params.URI == "fixture://notes" {
				respond(req.ID, `{"contents":[{"uri":"fixture://notes","mimeType":"text/plain","text":"fixture notes body"}]}`)
				break
			}
			respond(req.ID, `{}`)
		case "prompts/list":
			promptsLists++
			respond(req.ID, fmt.Sprintf(`{"prompts":[{"name":"prompt-v%d","description":"fixture prompt"}]}`, promptsLists))
		case "prompts/get":
			if req.Params.Name == "greet" {
				respond(req.ID, `{"messages":[{"role":"user","content":{"type":"text","text":"fixture greeting"}},{"role":"assistant","content":{"type":"text","text":"fixture reply"}}]}`)
				break
			}
			respond(req.ID, `{}`)
		case "test/trigger":
			respond(req.ID, `{}`)
			helperNotificationTriggerActions(t, reader, sendLine)
		default:
			respond(req.ID, `{}`)
		}
	}
}

// helperNotificationTriggerActions performs the env-configured notification
// and server-request actions exactly once, after the trigger file appears.
func helperNotificationTriggerActions(t *testing.T, reader *bufio.Reader, sendLine func(string)) {
	t.Helper()
	if trigger := os.Getenv("KOYORI_MCP_TRIGGER_FILE"); trigger != "" {
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(trigger); err == nil {
				break
			}
			if time.Now().After(deadline) {
				fmt.Fprintln(os.Stderr, "notification helper: trigger file timeout")
				os.Exit(1)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	var actions []struct {
		Notify    string          `json:"notify"`
		Request   string          `json:"request"`
		ID        json.RawMessage `json:"id"`
		Malformed bool            `json:"malformed"`
	}
	if raw := os.Getenv("KOYORI_MCP_AFTER_TRIGGER"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &actions); err != nil {
			fmt.Fprintf(os.Stderr, "notification helper: decode actions: %v\n", err)
			os.Exit(1)
		}
	}
	resultPath := os.Getenv("KOYORI_MCP_RESULT_FILE")
	for _, action := range actions {
		switch {
		case action.Malformed:
			sendLine(`{"jsonrpc":"2.0"}`)
		case action.Notify != "":
			method, err := json.Marshal(action.Notify)
			if err != nil {
				os.Exit(1)
			}
			sendLine(fmt.Sprintf(`{"jsonrpc":"2.0","method":%s}`, method))
		case action.Request != "":
			method, err := json.Marshal(action.Request)
			if err != nil {
				os.Exit(1)
			}
			sendLine(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":%s,"params":{}}`, string(action.ID), method))
			response, err := reader.ReadString('\n')
			if err != nil {
				fmt.Fprintf(os.Stderr, "notification helper: read rejection: %v\n", err)
				os.Exit(1)
			}
			if resultPath != "" {
				f, err := os.OpenFile(resultPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
				if err != nil {
					fmt.Fprintf(os.Stderr, "notification helper: open result: %v\n", err)
					os.Exit(1)
				}
				_, _ = f.WriteString(strings.TrimRight(response, "\n") + "\n")
				_ = f.Close()
			}
		}
	}
}

// startNotificationFixtureClient starts a real stdio client against the
// TestHelperMCPNotificationServer reexec helper.
func startNotificationFixtureClient(t *testing.T, env map[string]string) *MCPClient {
	t.Helper()
	fullEnv := map[string]string{"KOYORI_MCP_NOTIFICATION_HELPER": "1"}
	for key, value := range env {
		fullEnv[key] = value
	}
	client := newMCPClient(MCPServerConfig{
		Name:      "notification-fixture",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestHelperMCPNotificationServer$", "-test.timeout=120s"},
		Env:       fullEnv,
	})
	// Mirror what MCPService does before StartServer: stamp the committed
	// workspace root the controlled roots/list response may return.
	client.setRootsWorkspaceRoot(`C:\fixture-root`)
	if err := client.StartServer(context.Background()); err != nil {
		t.Fatalf("start notification fixture: %v", err)
	}
	t.Cleanup(func() { _ = client.StopServer() })
	return client
}

func waitForMCPCondition(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestMCPClientToolsListChangedForcesRefetch_StdioFixture(t *testing.T) {
	trigger := filepath.Join(t.TempDir(), "trigger")
	client := startNotificationFixtureClient(t, map[string]string{
		"KOYORI_MCP_TRIGGER_FILE":  trigger,
		"KOYORI_MCP_AFTER_TRIGGER": `[{"notify":"notifications/tools/list_changed"}]`,
	})

	first, err := client.ListTools(context.Background())
	if err != nil || len(first) != 1 || first[0].Name != "tool-v1" {
		t.Fatalf("first ListTools = %#v, %v; want tool-v1", first, err)
	}
	// Second list must be served from the cache: the helper names every
	// tools/list response by an increasing counter, so tool-v1 proves no new
	// request was issued.
	second, err := client.ListTools(context.Background())
	if err != nil || len(second) != 1 || second[0].Name != "tool-v1" {
		t.Fatalf("cached ListTools = %#v, %v; want tool-v1 from cache", second, err)
	}

	if err := os.WriteFile(trigger, []byte("go"), 0o600); err != nil {
		t.Fatalf("write trigger: %v", err)
	}
	waitForMCPCondition(t, "tools list-changed invalidation", 10*time.Second, func() bool {
		return client.listCacheInvalidation("tools").generation >= 1
	})
	invalidation := client.listCacheInvalidation("tools")
	if invalidation.notifications != 1 || invalidation.invalidatedAt.IsZero() {
		t.Fatalf("tools invalidation state = %+v, want one notification with a timestamp", invalidation)
	}
	// A tools list-changed must not touch the other cache families.
	if got := client.listCacheInvalidation("resources"); got.generation != 0 || got.notifications != 0 {
		t.Fatalf("resources invalidation after tools notification = %+v, want untouched", got)
	}
	if got := client.listCacheInvalidation("prompts"); got.generation != 0 || got.notifications != 0 {
		t.Fatalf("prompts invalidation after tools notification = %+v, want untouched", got)
	}

	third, err := client.ListTools(context.Background())
	if err != nil || len(third) != 1 || third[0].Name != "tool-v2" {
		t.Fatalf("refetched ListTools = %#v, %v; want a new JSON-RPC request answering tool-v2", third, err)
	}
	fourth, err := client.ListTools(context.Background())
	if err != nil || len(fourth) != 1 || fourth[0].Name != "tool-v2" {
		t.Fatalf("post-refresh cache ListTools = %#v, %v; want tool-v2 from cache", fourth, err)
	}
}

func TestMCPClientDispatchFamiliesRejectionsAndMalformed_StdioFixture(t *testing.T) {
	result := filepath.Join(t.TempDir(), "rejections.jsonl")
	client := startNotificationFixtureClient(t, map[string]string{
		"KOYORI_MCP_AFTER_TRIGGER": `[` +
			`{"notify":"notifications/resources/list_changed"},` +
			`{"notify":"notifications/prompts/list_changed"},` +
			`{"notify":"notifications/tools/list_changed"},` +
			`{"notify":"notifications/tools/list_changed"},` +
			`{"notify":"notifications/message"},` +
			`{"request":"sampling/createMessage","id":5001},` +
			`{"request":"roots/list","id":5002},` +
			`{"malformed":true}]`,
		"KOYORI_MCP_RESULT_FILE": result,
	})

	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("initial ListTools: %v", err)
	}

	waitForMCPCondition(t, "all list-changed invalidations", 10*time.Second, func() bool {
		resources := client.listCacheInvalidation("resources")
		prompts := client.listCacheInvalidation("prompts")
		tools := client.listCacheInvalidation("tools")
		return resources.generation >= 1 && prompts.generation >= 1 && tools.generation >= 2
	})
	tools := client.listCacheInvalidation("tools")
	if tools.generation != 2 || tools.notifications != 2 {
		t.Fatalf("tools invalidation = %+v, want exactly two recorded notifications", tools)
	}

	// Every unimplemented server-to-client request must be rejected with an
	// explicit JSON-RPC protocol error, never silently dropped, and the
	// rejections must not stall the transport reader.
	waitForMCPCondition(t, "two protocol rejections", 10*time.Second, func() bool {
		lines, err := os.ReadFile(result)
		return err == nil && strings.Count(string(lines), "\n") >= 2
	})
	lines, err := os.ReadFile(result)
	if err != nil {
		t.Fatalf("read rejection log: %v", err)
	}
	samplingRejected := false
	rootsAnswered := false
	for _, line := range strings.Split(strings.TrimSpace(string(lines)), "\n") {
		var response jsonrpcResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		switch fmt.Sprint(response.ID) {
		case "5001":
			// sampling/createMessage is not implemented: explicit -32601.
			if response.Error == nil || response.Error.Code != -32601 {
				t.Fatalf("sampling response = %s, want a -32601 rejection", line)
			}
			samplingRejected = true
		case "5002":
			// The controlled roots/list implementation answers with the
			// workspace root bound to this connection — never an error, never
			// an arbitrary or stale path.
			if response.Error != nil {
				t.Fatalf("roots/list got an error response: %s", line)
			}
			var payload struct {
				Roots []struct {
					URI  string `json:"uri"`
					Name string `json:"name"`
				} `json:"roots"`
			}
			if err := json.Unmarshal(response.Result, &payload); err != nil {
				t.Fatalf("decode roots payload %q: %v", line, err)
			}
			if len(payload.Roots) != 1 || payload.Roots[0].URI != "file:///C:/fixture-root" || payload.Roots[0].Name != "fixture-root" {
				t.Fatalf("roots/list payload = %s, want exactly the bound workspace root", line)
			}
			rootsAnswered = true
		}
	}
	if !samplingRejected || !rootsAnswered {
		t.Fatalf("missing responses: samplingRejected=%v rootsAnswered=%v", samplingRejected, rootsAnswered)
	}

	// The malformed notification and unsupported logging notification must
	// have left the client fully operational; the double tools invalidation
	// merges into one refetch answering tool-v2.
	refreshed, err := client.ListTools(context.Background())
	if err != nil || len(refreshed) != 1 || refreshed[0].Name != "tool-v2" {
		t.Fatalf("ListTools after dispatch stress = %#v, %v; want tool-v2", refreshed, err)
	}
}

func TestMCPClientNotificationHandlerIsBoundedAndStopped(t *testing.T) {
	client, _ := startInitializedScriptedMCPClient(t, "bounded", mcpTestToolsCapability, nil)

	release := make(chan struct{})
	var handlersStarted atomic.Int64
	for range mcpNotificationHandlerLimit {
		if !client.enqueueHandler("test", "notifications/tools/list_changed", func() {
			handlersStarted.Add(1)
			<-release
		}) {
			t.Fatal("handler was not accepted before saturation")
		}
	}
	// The next handler exceeds the bounded slot pool and must be dropped
	// deterministically instead of blocking the caller.
	if client.enqueueHandler("test", "notifications/tools/list_changed", func() {}) {
		t.Fatal("handler beyond the bound was accepted")
	}
	// Unblock the accepted handlers so StopServer's bounded wait can finish.
	close(release)
	if err := client.StopServer(); err != nil {
		t.Fatalf("StopServer with saturated handlers: %v", err)
	}
	if client.enqueueHandler("test", "notifications/tools/list_changed", func() {}) {
		t.Fatal("stopped client accepted a handler")
	}
	if got := handlersStarted.Load(); got != mcpNotificationHandlerLimit {
		t.Fatalf("handlers started = %d, want %d (the dropped handler must not run)", got, mcpNotificationHandlerLimit)
	}
}

// ---------------------------------------------------------------------------
// P1-03-C: resources/prompts caches, content validation, service fail-closed
// ---------------------------------------------------------------------------

func TestMCPClientResourceAndPromptListCaches_StdioFixture(t *testing.T) {
	client := startNotificationFixtureClient(t, map[string]string{
		// No trigger file: the helper performs the actions as soon as it sees
		// the test/trigger request.
		"KOYORI_MCP_AFTER_TRIGGER": `[{"notify":"notifications/resources/list_changed"},{"notify":"notifications/prompts/list_changed"}]`,
	})

	firstResources, err := client.ListResources(context.Background())
	if err != nil || len(firstResources) != 1 || firstResources[0].URI != "fixture://resource-v1" {
		t.Fatalf("first ListResources = %#v, %v; want resource-v1", firstResources, err)
	}
	secondResources, err := client.ListResources(context.Background())
	if err != nil || secondResources[0].URI != "fixture://resource-v1" {
		t.Fatalf("cached ListResources = %#v, %v; want resource-v1 from cache", secondResources, err)
	}
	firstPrompts, err := client.ListPrompts(context.Background())
	if err != nil || len(firstPrompts) != 1 || firstPrompts[0].Name != "prompt-v1" {
		t.Fatalf("first ListPrompts = %#v, %v; want prompt-v1", firstPrompts, err)
	}
	secondPrompts, err := client.ListPrompts(context.Background())
	if err != nil || secondPrompts[0].Name != "prompt-v1" {
		t.Fatalf("cached ListPrompts = %#v, %v; want prompt-v1 from cache", secondPrompts, err)
	}

	// Trigger both list-changed notifications through a real JSON-RPC request.
	if _, err := client.call(context.Background(), "test/trigger", nil); err != nil {
		t.Fatalf("trigger request: %v", err)
	}
	waitForMCPCondition(t, "resources/prompts list-changed invalidation", 10*time.Second, func() bool {
		return client.listCacheInvalidation("resources").generation >= 1 &&
			client.listCacheInvalidation("prompts").generation >= 1
	})

	refreshedResources, err := client.ListResources(context.Background())
	if err != nil || refreshedResources[0].URI != "fixture://resource-v2" {
		t.Fatalf("refetched ListResources = %#v, %v; want resource-v2 from a new JSON-RPC request", refreshedResources, err)
	}
	refreshedPrompts, err := client.ListPrompts(context.Background())
	if err != nil || refreshedPrompts[0].Name != "prompt-v2" {
		t.Fatalf("refetched ListPrompts = %#v, %v; want prompt-v2 from a new JSON-RPC request", refreshedPrompts, err)
	}

	// TTL expiry forces another refresh for both families.
	client.mu.Lock()
	client.resourcesCachedAt = time.Now().Add(-mcpToolsCacheTTL - time.Second)
	client.promptsCachedAt = time.Now().Add(-mcpToolsCacheTTL - time.Second)
	client.mu.Unlock()
	ttlResources, err := client.ListResources(context.Background())
	if err != nil || ttlResources[0].URI != "fixture://resource-v3" {
		t.Fatalf("TTL-expired ListResources = %#v, %v; want resource-v3", ttlResources, err)
	}
	ttlPrompts, err := client.ListPrompts(context.Background())
	if err != nil || ttlPrompts[0].Name != "prompt-v3" {
		t.Fatalf("TTL-expired ListPrompts = %#v, %v; want prompt-v3", ttlPrompts, err)
	}

	if err := client.StopServer(); err != nil {
		t.Fatalf("StopServer: %v", err)
	}
	client.mu.Lock()
	cachesCleared := client.resourcesCache == nil && !client.resourcesCacheValid && client.promptsCache == nil && !client.promptsCacheValid
	client.mu.Unlock()
	if !cachesCleared {
		t.Fatal("stop did not clear the resources/prompts metadata caches")
	}
}

func TestMCPClientResourceAndPromptContentValidation_HTTPFixture(t *testing.T) {
	var mu sync.Mutex
	var requests []capturedMCPRequest
	server := startMCPHTTPFixture(t, mcpHTTPFixtureHandler(&requests, &mu,
		`{"protocolVersion":"2024-11-05","capabilities":{"resources":{"listChanged":false},"prompts":{"listChanged":false}},"serverInfo":{"name":"content-fixture","version":"1"}}`,
		func(req capturedMCPRequest) (int, string) {
			id := string(req.ID)
			var params struct {
				URI  string `json:"uri"`
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &params)
			switch req.Method {
			case "resources/read":
				switch params.URI {
				case "fixture://malformed":
					return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":"not-an-object"}`, id)
				case "fixture://empty":
					return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"contents":[]}}`, id)
				case "fixture://missing-uri":
					return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"contents":[{"text":"orphan"}]}}`, id)
				case "fixture://blob":
					return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"contents":[{"uri":"fixture://blob","type":"blob","blob":"aGk="}]}}`, id)
				case "fixture://oversized":
					return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"contents":[{"uri":"fixture://oversized","text":"%s"}]}}`, id, strings.Repeat("x", mcpContentByteLimit+1))
				case "fixture://ok":
					return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"contents":[{"uri":"fixture://ok","mimeType":"text/plain","text":"part-one"},{"uri":"fixture://ok#2","mimeType":"text/plain","text":"part-two"}]}}`, id)
				}
			case "prompts/get":
				switch params.Name {
				case "empty":
					return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"messages":[]}}`, id)
				case "bad-role":
					return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"messages":[{"role":"system","content":{"type":"text","text":"escalate"}}]}}`, id)
				case "bad-type":
					return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"messages":[{"role":"user","content":{"type":"image","data":"zzz"}}]}}`, id)
				case "oversized":
					return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"messages":[{"role":"user","content":{"type":"text","text":"%s"}}]}}`, id, strings.Repeat("x", mcpContentByteLimit+1))
				case "greet":
					return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"messages":[{"role":"user","content":{"type":"text","text":"hello"}},{"role":"assistant","content":{"type":"text","text":"hi"}}]}}`, id)
				}
			}
			return http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"unimplemented"}}`, id)
		}))

	client := newHTTPFixtureClient(t, server, "content-fixture")

	resourceCases := []struct {
		uri      string
		wantErr  error
		fragment string
	}{
		{"fixture://malformed", ErrInvalidInput, "malformed resource contents"},
		{"fixture://empty", ErrNotFound, "no contents"},
		{"fixture://missing-uri", ErrInvalidInput, "missing its URI"},
		{"fixture://blob", ErrNotAllowed, "not supported"},
		{"fixture://oversized", ErrInvalidInput, "exceeds"},
	}
	for _, testCase := range resourceCases {
		_, err := client.ReadResource(context.Background(), testCase.uri)
		if !errors.Is(err, testCase.wantErr) || !strings.Contains(err.Error(), testCase.fragment) {
			t.Fatalf("ReadResource(%q) error = %v, want %v with %q", testCase.uri, err, testCase.wantErr, testCase.fragment)
		}
	}
	contents, err := client.ReadResource(context.Background(), "fixture://ok")
	if err != nil || len(contents) != 2 || contents[0].Text != "part-one" || contents[1].Text != "part-two" || contents[0].MimeType != "text/plain" {
		t.Fatalf("ReadResource(fixture://ok) = %#v, %v; want both validated content blocks", contents, err)
	}

	promptCases := []struct {
		name     string
		wantErr  error
		fragment string
	}{
		{"empty", ErrNotFound, "no messages"},
		{"bad-role", ErrNotAllowed, "unsupported role"},
		{"bad-type", ErrNotAllowed, "not supported"},
		{"oversized", ErrInvalidInput, "exceeds"},
	}
	for _, testCase := range promptCases {
		_, err := client.GetPrompt(context.Background(), testCase.name, map[string]string{})
		if !errors.Is(err, testCase.wantErr) || !strings.Contains(err.Error(), testCase.fragment) {
			t.Fatalf("GetPrompt(%q) error = %v, want %v with %q", testCase.name, err, testCase.wantErr, testCase.fragment)
		}
	}
	messages, err := client.GetPrompt(context.Background(), "greet", map[string]string{})
	if err != nil || len(messages) != 2 || messages[0].Role != "user" || messages[0].Content.Text != "hello" || messages[1].Role != "assistant" || messages[1].Content.Text != "hi" {
		t.Fatalf("GetPrompt(greet) = %#v, %v; want role/content provenance preserved", messages, err)
	}
}

func TestMCPServiceResourcePromptReadsFailClosedOnWorkspaceSwitch_StdioFixture(t *testing.T) {
	root := filepath.Dir(os.Args[0])
	service := newTestMCPService(t)
	if err := service.setWorkspaceRoot(root); err != nil {
		t.Fatalf("setWorkspaceRoot: %v", err)
	}
	cfg := MCPServerConfig{
		Name:      "resource-fixture",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestHelperMCPNotificationServer$", "-test.timeout=120s"},
		Env:       map[string]string{"KOYORI_MCP_NOTIFICATION_HELPER": "1"},
	}
	if err := service.SaveServer(cfg); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	if err := service.SetServerEnabled(cfg.Name, true); err != nil {
		t.Fatalf("SetServerEnabled: %v", err)
	}
	if err := service.ConnectServer(context.Background(), cfg.Name); err != nil {
		t.Fatalf("ConnectServer: %v", err)
	}

	resources, err := service.ListResources(context.Background(), cfg.Name)
	if err != nil || len(resources) != 1 || resources[0].URI != "fixture://resource-v1" {
		t.Fatalf("service ListResources = %#v, %v; want resource-v1", resources, err)
	}
	read, err := service.ReadResource(context.Background(), cfg.Name, "fixture://notes")
	if err != nil || read == nil {
		t.Fatalf("service ReadResource = %#v, %v", read, err)
	}
	if read.Server != cfg.Name || read.URI != "fixture://notes" || len(read.Contents) != 1 || read.Contents[0].Text != "fixture notes body" {
		t.Fatalf("resource provenance incomplete: %+v", read)
	}
	if read.RootGeneration == 0 && read.LifecycleGeneration == 0 {
		t.Fatalf("resource read did not record generation provenance: %+v", read)
	}
	prompts, err := service.ListPrompts(context.Background(), cfg.Name)
	if err != nil || len(prompts) != 1 || prompts[0].Name != "prompt-v1" {
		t.Fatalf("service ListPrompts = %#v, %v; want prompt-v1", prompts, err)
	}
	render, err := service.GetPrompt(context.Background(), cfg.Name, "greet", map[string]string{})
	if err != nil || render == nil {
		t.Fatalf("service GetPrompt = %#v, %v", render, err)
	}
	if render.Server != cfg.Name || render.Prompt != "greet" || len(render.Messages) != 2 || render.Messages[0].Role != "user" {
		t.Fatalf("prompt provenance incomplete: %+v", render)
	}

	// The workspace switch detaches every client; all four reads must fail
	// closed against the new workspace identity.
	rootB := t.TempDir()
	if err := service.applyWorkspaceRoot(rootB); err != nil {
		t.Fatalf("applyWorkspaceRoot: %v", err)
	}
	if _, err := service.ListResources(context.Background(), cfg.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListResources after workspace switch = %v, want ErrNotFound", err)
	}
	if _, err := service.ReadResource(context.Background(), cfg.Name, "fixture://notes"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadResource after workspace switch = %v, want ErrNotFound", err)
	}
	if _, err := service.ListPrompts(context.Background(), cfg.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListPrompts after workspace switch = %v, want ErrNotFound", err)
	}
	if _, err := service.GetPrompt(context.Background(), cfg.Name, "greet", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetPrompt after workspace switch = %v, want ErrNotFound", err)
	}
}

// TestMCPClientRequestTimeout_HTTPFixture proves the timeout boundary over the
// real streamable HTTP transport: a fixture that never answers the request
// must fail the call with a context deadline, leave no pending waiter behind,
// and keep the client usable for a follow-up request.
func TestMCPClientRequestTimeout_HTTPFixture(t *testing.T) {
	var mu sync.Mutex
	var requests []capturedMCPRequest
	toolLists := 0
	hang := make(chan struct{})
	server := startMCPHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req capturedMCPRequest
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		requests = append(requests, req)
		if req.Method == "tools/list" {
			toolLists++
		}
		isFirstToolList := req.Method == "tools/list" && toolLists == 1
		mu.Unlock()
		if len(req.ID) == 0 || string(req.ID) == "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if req.Method == "initialize" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":false}},"serverInfo":{"name":"slow-fixture","version":"1"}}}`, string(req.ID))))
			return
		}
		if isFirstToolList {
			// The first tools/list hangs until the test releases it, so the
			// client's deadline fires against a real transport.
			<-hang
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}}`, string(req.ID))))
	})

	client := newMCPClient(MCPServerConfig{Name: "slow-fixture", Transport: "http", URL: server.URL})
	if err := client.StartServer(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		close(hang)
		_ = client.StopServer()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := client.ListTools(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ListTools timeout error = %v, want context.DeadlineExceeded", err)
	}

	client.mu.Lock()
	pending := len(client.pending)
	client.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending waiters after timeout = %d, want 0", pending)
	}

	// The cancelled request must not poison the connection: a follow-up
	// request completes normally.
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("post-timeout ListTools = %#v, %v; want a fresh successful call", tools, err)
	}
	mu.Lock()
	toolListCount := toolLists
	mu.Unlock()
	if toolListCount != 2 {
		t.Fatalf("tools/list request count = %d, want the timed-out call plus the retry", toolListCount)
	}
}
