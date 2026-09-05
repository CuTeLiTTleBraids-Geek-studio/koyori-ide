package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MCP client request multiplexing and protocol operations.
type mcpCallResult struct {
	response *jsonrpcResponse
	err      error
}

type mcpClientRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	calls  sync.WaitGroup
}

func newMCPClientRun() *mcpClientRun {
	ctx, cancel := context.WithCancel(context.Background())
	return &mcpClientRun{ctx: ctx, cancel: cancel}
}

type MCPClient struct {
	cfg              MCPServerConfig
	transport        mcpTransport
	nextID           int64
	mu               sync.Mutex
	sendMu           sync.Mutex
	closed           bool
	run              *mcpClientRun
	pending          map[string]chan mcpCallResult
	dispatchCancel   context.CancelFunc
	dispatchDone     chan struct{}
	dispatchErr      error
	toolsCache       []MCPTool
	toolsCachedAt    time.Time
	toolsCacheValid  bool
	toolsRefreshDone chan struct{}
	// resources/prompts metadata caches mirror the tools cache semantics:
	// TTL, merged concurrent refresh, list-changed invalidation, and stop
	// cleanup. Resource/prompt CONTENT is never cached.
	resourcesCache       []MCPResource
	resourcesCachedAt    time.Time
	resourcesCacheValid  bool
	resourcesRefreshDone chan struct{}
	promptsCache         []MCPPrompt
	promptsCachedAt      time.Time
	promptsCacheValid    bool
	promptsRefreshDone   chan struct{}
	// capabilitySnapshot is the validated initialize result of the current
	// run. It is guarded by mu and cleared on stop so a reconnect, config
	// mutation, or workspace switch can never reuse it.
	capabilitySnapshot *MCPCapabilitySnapshot
	// list invalidation state per capability family, bumped by the matching
	// list-changed notification and reset on stop (guarded by mu).
	toolsInvalidation     mcpListInvalidation
	resourcesInvalidation mcpListInvalidation
	promptsInvalidation   mcpListInvalidation
	// listChangedHandler is invoked for handled list-changed notifications on
	// a bounded goroutine that never blocks the transport reader.
	listChangedHandler func(method string)
	// rootsWorkspaceRoot is the committed workspace root this connection was
	// opened under, stamped by MCPService before StartServer. It is the only
	// value the controlled roots/list response may return (guarded by mu).
	rootsWorkspaceRoot string
	// notificationSlots bounds concurrent notification/request handler
	// goroutines per run; overflow drops deterministically with a log.
	notificationSlots chan struct{}
}

// mcpListInvalidation is the auditable invalidation state of one list cache
// family. generation changes on every matching list-changed notification so
// in-flight fetches can detect that their result became stale.
type mcpListInvalidation struct {
	generation    uint64
	invalidatedAt time.Time
	notifications uint64
}

const mcpToolsCacheTTL = 30 * time.Second

// mcpNotificationHandlerLimit bounds concurrent goroutines that run
// notification handlers or server-request rejections. Overflow drops
// deterministically instead of blocking the transport reader.
const mcpNotificationHandlerLimit = 8

// newMCPClient creates a client for the given config but does not connect.
func newMCPClient(cfg MCPServerConfig) *MCPClient {
	return &MCPClient{
		cfg:               cfg,
		pending:           make(map[string]chan mcpCallResult),
		notificationSlots: make(chan struct{}, mcpNotificationHandlerLimit),
	}
}

// StartServer establishes the connection and performs the MCP initialize
// handshake (Step 2).
func (c *MCPClient) StartServer(ctx context.Context) error {
	var transport mcpTransport
	switch c.cfg.Transport {
	case "stdio":
		t, err := newStdioTransport(ctx, c.cfg)
		if err != nil {
			return err
		}
		transport = t
	case "sse":
		t := newSSETransport(c.cfg)
		if err := t.connect(ctx); err != nil {
			return err
		}
		transport = t
	case "http":
		transport = newHTTPTransport(c.cfg)
	default:
		return fmt.Errorf("unknown transport %q: %w", c.cfg.Transport, ErrInvalidInput)
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.Join(fmt.Errorf("client stopped: %w", ErrInvalidInput), transport.Close())
	}
	if c.transport != nil {
		c.mu.Unlock()
		return errors.Join(fmt.Errorf("client already started: %w", ErrAlreadyExists), transport.Close())
	}
	c.transport = transport
	c.run = newMCPClientRun()
	c.dispatchErr = nil
	c.capabilitySnapshot = nil
	c.toolsInvalidation = mcpListInvalidation{}
	c.resourcesInvalidation = mcpListInvalidation{}
	c.promptsInvalidation = mcpListInvalidation{}
	c.notificationSlots = make(chan struct{}, mcpNotificationHandlerLimit)
	c.toolsCache = nil
	c.toolsCachedAt = time.Time{}
	c.toolsCacheValid = false
	c.resourcesCache = nil
	c.resourcesCachedAt = time.Time{}
	c.resourcesCacheValid = false
	c.promptsCache = nil
	c.promptsCachedAt = time.Time{}
	c.promptsCacheValid = false
	if _, ok := transport.(*httpTransport); !ok {
		c.startResponseDispatcherLocked(transport)
	}
	c.mu.Unlock()

	// Perform initialize handshake.
	if err := c.initialize(ctx); err != nil {
		return errors.Join(err, c.stopTransport(false))
	}
	return nil
}

// startResponseDispatcherLocked starts the single reader for transports whose
// responses arrive independently from Send. c.mu must be held by the caller.
func (c *MCPClient) startResponseDispatcherLocked(transport mcpTransport) {
	// FIX B1: call previously held c.mu while blocking in Recv, serializing all
	// requests. A dedicated reader now dispatches responses to buffered waiters.
	dispatchCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.dispatchCancel = cancel
	c.dispatchDone = done
	go c.dispatchResponses(dispatchCtx, transport, done)
}

func (c *MCPClient) dispatchResponses(ctx context.Context, transport mcpTransport, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			c.failPendingForTransport(transport, fmt.Errorf("response dispatcher stopped: %w", ctx.Err()))
			return
		default:
		}

		resp, err := transport.Recv()
		if err != nil {
			c.failPendingForTransport(transport, fmt.Errorf("receive response: %w", err))
			return
		}
		if resp == nil {
			continue
		}
		c.mu.Lock()
		stale := c.transport != transport
		c.mu.Unlock()
		if stale {
			return
		}

		// JSON-RPC 2.0 classification: a message without an ID is a
		// notification; a message with an ID and a Method is a
		// server-to-client request that must be answered; everything else is
		// a response routed to its pending caller.
		if resp.ID == nil {
			c.handleTransportNotification(resp)
			continue
		}
		if resp.Method != "" {
			c.handleServerRequest(transport, resp)
			continue
		}

		key := fmt.Sprint(resp.ID)
		c.mu.Lock()
		responseCh, ok := c.pending[key]
		if ok {
			delete(c.pending, key)
		}
		c.mu.Unlock()
		if ok {
			// The channel is buffered(1), so an abandoned caller cannot block
			// the one transport reader and starve unrelated requests.
			responseCh <- mcpCallResult{response: resp}
		}
	}
}

// handleTransportNotification processes a server notification on the
// dispatcher goroutine. Only fast, non-blocking cache invalidation happens
// inline; the optional service callback runs on a bounded goroutine so the
// transport reader is never blocked by catalog refresh work.
func (c *MCPClient) handleTransportNotification(resp *jsonrpcResponse) {
	if resp.Method == "" {
		// Malformed JSON-RPC notification (no ID and no method): deterministic
		// observable drop; it can never be answered or routed.
		slog.Warn("mcp server sent a malformed notification without method", "server", c.cfg.Name)
		return
	}
	switch resp.Method {
	case "notifications/tools/list_changed", "notifications/resources/list_changed", "notifications/prompts/list_changed":
		if handler := c.invalidateListCache(resp.Method); handler != nil {
			c.enqueueHandler("list changed", resp.Method, func() { handler(resp.Method) })
		}
	default:
		// Unknown or unsupported server notification (including logging
		// notifications): recorded observably, never treated as implemented.
		slog.Debug("mcp server notification is not handled", "server", c.cfg.Name, "method", resp.Method)
	}
}

// invalidateListCache invalidates exactly the cache family named by the
// list-changed notification and returns the registered handler, if any.
func (c *MCPClient) invalidateListCache(method string) func(string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var state *mcpListInvalidation
	switch method {
	case "notifications/tools/list_changed":
		state = &c.toolsInvalidation
		c.toolsCacheValid = false
	case "notifications/resources/list_changed":
		state = &c.resourcesInvalidation
		c.resourcesCacheValid = false
	case "notifications/prompts/list_changed":
		state = &c.promptsInvalidation
		c.promptsCacheValid = false
	default:
		return nil
	}
	state.generation++
	state.notifications++
	state.invalidatedAt = time.Now()
	slog.Info("mcp list cache invalidated",
		"server", c.cfg.Name,
		"method", method,
		"generation", state.generation,
		"notifications", state.notifications,
	)
	return c.listChangedHandler
}

// handleServerRequest answers a server-to-client request. The only
// implemented server request is the controlled roots/list: it returns exactly
// the committed workspace root this connection was opened under. Everything
// else is rejected with an explicit protocol error and never silently
// accepted and dropped. Rejections are sent from a bounded goroutine so a
// blocked send can never stall the transport reader.
func (c *MCPClient) handleServerRequest(transport mcpTransport, resp *jsonrpcResponse) {
	if resp.Method == "roots/list" {
		c.handleRootsListRequest(transport, resp)
		return
	}
	slog.Info("mcp server request is not implemented",
		"server", c.cfg.Name,
		"method", resp.Method,
	)
	response := &jsonrpcOutboundMessage{
		JSONRPC: "2.0",
		ID:      resp.ID,
		Error: &jsonrpcError{
			Code:    -32601,
			Message: fmt.Sprintf("method %q is not implemented by this MCP client", resp.Method),
		},
	}
	c.enqueueHandler("server request", resp.Method, func() {
		c.mu.Lock()
		live := c.transport == transport && !c.closed
		c.mu.Unlock()
		if !live {
			return
		}
		sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.sendMu.Lock()
		err := transport.Send(sendCtx, response)
		c.sendMu.Unlock()
		if err != nil {
			slog.Debug("mcp server request rejection failed", "server", c.cfg.Name, "method", resp.Method, "error", err)
		}
	})
}

// enqueueHandler runs handler on a bounded goroutine tied to the current
// client run. Overflow drops deterministically with a warning instead of
// blocking the transport reader; StopServer waits for accepted handlers. It
// reports whether the handler was accepted.
func (c *MCPClient) enqueueHandler(what, method string, handler func()) bool {
	c.mu.Lock()
	slots := c.notificationSlots
	run := c.run
	if run != nil {
		// Reserved under mu so stopTransport's run.calls.Wait can never miss
		// an accepted handler.
		run.calls.Add(1)
	}
	c.mu.Unlock()
	if run == nil || slots == nil {
		return false
	}
	select {
	case slots <- struct{}{}:
	default:
		run.calls.Done()
		slog.Warn("mcp notification handler saturated; dropped",
			"server", c.cfg.Name, "what", what, "method", method,
		)
		return false
	}
	go func() {
		defer RecoverGoroutinePanic("mcp:notification-dispatch")
		defer run.calls.Done()
		defer func() { <-slots }()
		handler()
	}()
	return true
}

// setRootsWorkspaceRoot stamps the committed workspace root this connection
// was opened under. MCPService calls it before StartServer; the controlled
// roots/list response may only ever return this root.
func (c *MCPClient) setRootsWorkspaceRoot(root string) {
	c.mu.Lock()
	c.rootsWorkspaceRoot = root
	c.mu.Unlock()
}

// handleRootsListRequest answers the roots/list server request with exactly
// the committed workspace root bound to this connection. The root comes from
// the backend-stamped binding, never from the renderer; a connection whose
// root was not bound yet (or was opened without a workspace) returns an empty
// list, which is the honest fail-closed answer. Old connections cannot answer
// at all after a workspace switch: MCPService detaches them.
func (c *MCPClient) handleRootsListRequest(transport mcpTransport, resp *jsonrpcResponse) {
	c.mu.Lock()
	root := c.rootsWorkspaceRoot
	c.mu.Unlock()
	var roots []map[string]string
	if root != "" {
		uri := "file:///" + strings.TrimPrefix(filepath.ToSlash(root), "/")
		roots = append(roots, map[string]string{"uri": uri, "name": filepath.Base(root)})
	}
	slog.Info("mcp roots/list answered", "server", c.cfg.Name, "rootBound", root != "")
	c.enqueueHandler("roots/list", "roots/list", func() {
		c.mu.Lock()
		live := c.transport == transport && !c.closed
		c.mu.Unlock()
		if !live {
			return
		}
		sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out := &jsonrpcOutboundMessage{
			JSONRPC: "2.0",
			ID:      resp.ID,
			Result:  map[string]interface{}{"roots": roots},
		}
		c.sendMu.Lock()
		err := transport.Send(sendCtx, out)
		c.sendMu.Unlock()
		if err != nil {
			slog.Debug("mcp roots/list response failed", "server", c.cfg.Name, "error", err)
		}
	})
}

// setListChangedHandler registers the callback invoked for handled
// list-changed notifications. The callback runs off the transport reader on
// a bounded goroutine and must be safe for concurrent invocation.
func (c *MCPClient) setListChangedHandler(handler func(method string)) {
	c.mu.Lock()
	c.listChangedHandler = handler
	c.mu.Unlock()
}

// listCacheInvalidation returns a copy of the auditable invalidation state of
// one list cache family ("tools", "resources", or "prompts").
func (c *MCPClient) listCacheInvalidation(family string) mcpListInvalidation {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch family {
	case "tools":
		return c.toolsInvalidation
	case "resources":
		return c.resourcesInvalidation
	case "prompts":
		return c.promptsInvalidation
	}
	return mcpListInvalidation{}
}

func (c *MCPClient) failPendingForTransport(transport mcpTransport, err error) {
	c.mu.Lock()
	if c.transport != transport {
		c.mu.Unlock()
		return
	}
	c.dispatchErr = err
	pending := c.pending
	c.pending = make(map[string]chan mcpCallResult)
	c.mu.Unlock()
	deliverMCPPending(pending, err)
}

func deliverMCPPending(pending map[string]chan mcpCallResult, err error) {
	for _, responseCh := range pending {
		responseCh <- mcpCallResult{err: err}
	}
}

func contextForMCPRun(ctx context.Context, run *mcpClientRun) (context.Context, context.CancelFunc) {
	requestCtx, cancel := context.WithCancel(ctx)
	if run == nil {
		return requestCtx, cancel
	}
	stopLifecycleCancel := context.AfterFunc(run.ctx, cancel)
	return requestCtx, func() {
		stopLifecycleCancel()
		cancel()
	}
}

// initialize sends the MCP initialize request + initialized notification and
// stores the validated server capability snapshot for the current run. The
// initialized notification is only sent after the response passed validation,
// so a malformed or unsupported response can never reach a half-initialized
// session.
func (c *MCPClient) initialize(ctx context.Context) error {
	result, err := c.call(ctx, "initialize", clientMCPInitializeParams())
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	snapshot, err := parseMCPInitializeResult(result)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	c.mu.Lock()
	if c.closed || c.transport == nil {
		c.mu.Unlock()
		return fmt.Errorf("client stopped during initialize: %w", ErrInvalidInput)
	}
	snapshot.ServerName = c.cfg.Name
	snapshot.Run = mcpRunCounter.Add(1)
	snapshot.EstablishedAt = time.Now()
	c.capabilitySnapshot = snapshot
	c.mu.Unlock()
	slog.Info("mcp initialize",
		"server", c.cfg.Name,
		"protocolVersion", snapshot.ProtocolVersion,
		"serverInfo", snapshot.ServerInfo.Name+" "+snapshot.ServerInfo.Version,
		"tools", string(snapshot.Capabilities.Tools.State),
		"resources", string(snapshot.Capabilities.Resources.State),
		"prompts", string(snapshot.Capabilities.Prompts.State),
		"sampling", string(snapshot.Capabilities.Sampling.State),
		"elicitation", string(snapshot.Capabilities.Elicitation.State),
		"logging", string(snapshot.Capabilities.Logging.State),
		"unknownCapabilities", snapshot.Capabilities.Unknown,
	)
	// Send initialized notification (no response expected).
	return c.notify(ctx, "notifications/initialized", map[string]interface{}{})
}

// requireDeclaredCapability fails closed when the current run has no
// validated initialize snapshot or when the server did not declare the
// capability family the API needs. Operations on a server that did not
// declare the feature must produce an explicit error, never a silent success.
func (c *MCPClient) requireDeclaredCapability(family string) error {
	c.mu.Lock()
	snapshot := c.capabilitySnapshot
	c.mu.Unlock()
	if snapshot == nil {
		return fmt.Errorf("mcp server %q has no validated initialize snapshot: %w", c.cfg.Name, ErrInvalidInput)
	}
	feature := snapshot.Capabilities.feature(family)
	if feature.State != MCPCapabilitySupported {
		return fmt.Errorf("server %q did not declare the %s capability (state %s): %w", c.cfg.Name, family, feature.State, ErrNotAllowed)
	}
	return nil
}

// capabilitySnapshotCopy returns a deep copy of the current run's snapshot.
func (c *MCPClient) capabilitySnapshotCopy() (MCPCapabilitySnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.capabilitySnapshot == nil {
		return MCPCapabilitySnapshot{}, fmt.Errorf("mcp server %q has no capability snapshot: %w", c.cfg.Name, ErrNotFound)
	}
	return c.capabilitySnapshot.clone(), nil
}

// bindCapabilitySnapshot stamps the workspace and lifecycle identity the
// service installed this run under. It is called while the service state lock
// is held immediately after install, so the binding cannot lag the map entry.
func (c *MCPClient) bindCapabilitySnapshot(workspaceRoot string, rootGeneration, lifecycleGeneration uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.capabilitySnapshot == nil {
		return
	}
	c.capabilitySnapshot.WorkspaceRoot = workspaceRoot
	c.capabilitySnapshot.RootGeneration = rootGeneration
	c.capabilitySnapshot.LifecycleGeneration = lifecycleGeneration
}

// call sends a JSON-RPC request and waits for the response.
func (c *MCPClient) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed || c.transport == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("client not started: %w", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("call %s: %w", method, err)
	}
	transport := c.transport
	run := c.run
	if run == nil {
		run = newMCPClientRun()
		c.run = run
	}
	run.calls.Add(1)
	id := atomic.AddInt64(&c.nextID, 1)
	req := &jsonrpcOutboundMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	// HTTP transport uses a combined send+recv via postRequest.
	if ht, ok := transport.(*httpTransport); ok {
		c.mu.Unlock()
		defer run.calls.Done()
		requestCtx, cancel := contextForMCPRun(ctx, run)
		defer cancel()
		resp, err := ht.postRequest(requestCtx, req)
		if err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
	if c.dispatchErr != nil {
		err := c.dispatchErr
		c.mu.Unlock()
		run.calls.Done()
		return nil, fmt.Errorf("response dispatcher unavailable: %w", err)
	}

	// stdio / SSE: register before Send so a fast response cannot outrun
	// pending registration. FIX B1: transport I/O must happen outside c.mu;
	// otherwise one blocked Send prevents the dispatcher and StopServer from
	// acquiring the state lock, deadlocking every in-flight request.
	responseCh := make(chan mcpCallResult, 1)
	key := fmt.Sprint(id)
	c.pending[key] = responseCh
	c.mu.Unlock()
	defer run.calls.Done()

	sendCtx, cancelSend := contextForMCPRun(ctx, run)
	c.sendMu.Lock()
	err := transport.Send(sendCtx, req)
	c.sendMu.Unlock()
	cancelSend()
	if err != nil {
		c.mu.Lock()
		if pendingCh, ok := c.pending[key]; ok && pendingCh == responseCh {
			delete(c.pending, key)
		}
		c.mu.Unlock()
		return nil, fmt.Errorf("send %s: %w", method, err)
	}

	var result mcpCallResult
	select {
	case result = <-responseCh:
	case <-ctx.Done():
		c.mu.Lock()
		if pendingCh, ok := c.pending[key]; ok && pendingCh == responseCh {
			delete(c.pending, key)
		}
		c.mu.Unlock()
		return nil, fmt.Errorf("call %s: %w", method, ctx.Err())
	}
	if result.err != nil {
		return nil, fmt.Errorf("receive %s: %w", method, result.err)
	}
	resp := result.response
	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// notify sends a JSON-RPC notification (no id, no response).
func (c *MCPClient) notify(ctx context.Context, method string, params interface{}) error {
	c.mu.Lock()
	if c.closed || c.transport == nil {
		c.mu.Unlock()
		return fmt.Errorf("client not started: %w", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("notify %s: %w", method, err)
	}
	if c.dispatchErr != nil {
		err := c.dispatchErr
		c.mu.Unlock()
		return fmt.Errorf("response dispatcher unavailable: %w", err)
	}
	transport := c.transport
	run := c.run
	if run == nil {
		run = newMCPClientRun()
		c.run = run
	}
	run.calls.Add(1)
	req := &jsonrpcOutboundMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	c.mu.Unlock()
	defer run.calls.Done()

	sendCtx, cancelSend := contextForMCPRun(ctx, run)
	c.sendMu.Lock()
	err := transport.Send(sendCtx, req)
	c.sendMu.Unlock()
	cancelSend()
	if err != nil {
		return fmt.Errorf("notify %s: %w", method, err)
	}
	return nil
}

// ListTools returns the tools exposed by the server (Step 2).
func (c *MCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	return c.listTools(ctx, time.Time{})
}

// listToolsFresh bypasses a still-valid catalog cache. Agent execution uses
// this immediately before allocating an irreversible external receipt so an
// approval cannot rely on a tool definition that the server has removed.
func (c *MCPClient) listToolsFresh(ctx context.Context) ([]MCPTool, error) {
	return c.listTools(ctx, time.Now())
}

func (c *MCPClient) listTools(ctx context.Context, freshAfter time.Time) ([]MCPTool, error) {
	for {
		now := time.Now()
		c.mu.Lock()
		if c.toolsCacheValid {
			age := now.Sub(c.toolsCachedAt)
			cacheIsFresh := freshAfter.IsZero() && age < mcpToolsCacheTTL
			if !freshAfter.IsZero() && !c.toolsCachedAt.Before(freshAfter) {
				cacheIsFresh = true
			}
			if cacheIsFresh {
				tools := cloneMCPTools(c.toolsCache)
				server := c.cfg.Name
				c.mu.Unlock()
				slog.Debug("mcp tools cache hit", "server", server, "count", len(tools), "age", age, "ttl", mcpToolsCacheTTL)
				return tools, nil
			}
		}
		if refreshDone := c.toolsRefreshDone; refreshDone != nil {
			server := c.cfg.Name
			c.mu.Unlock()
			slog.Debug("mcp tools refresh pending", "server", server)
			select {
			case <-refreshDone:
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("list tools: %w", ctx.Err())
			}
		}

		cacheWasValid := c.toolsCacheValid
		cacheAge := now.Sub(c.toolsCachedAt)
		refreshDone := make(chan struct{})
		c.toolsRefreshDone = refreshDone
		fetchGeneration := c.toolsInvalidation.generation
		server := c.cfg.Name
		c.mu.Unlock()

		if !freshAfter.IsZero() {
			slog.Debug("mcp tools forced refresh", "server", server, "age", cacheAge)
		} else if cacheWasValid {
			slog.Debug("mcp tools cache expired", "server", server, "age", cacheAge, "ttl", mcpToolsCacheTTL)
		} else {
			slog.Debug("mcp tools cache miss", "server", server, "ttl", mcpToolsCacheTTL)
		}

		tools, err := c.fetchTools(ctx)
		c.mu.Lock()
		// A list-changed notification that arrived during the fetch bumped the
		// invalidation generation: discarding this result prevents a stale
		// catalog from overwriting the invalidated state.
		if err == nil && fetchGeneration == c.toolsInvalidation.generation && !c.closed && c.transport != nil {
			c.toolsCache = tools
			c.toolsCachedAt = time.Now()
			c.toolsCacheValid = true
		}
		if c.toolsRefreshDone == refreshDone {
			c.toolsRefreshDone = nil
			close(refreshDone)
		}
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		slog.Debug("mcp tools refreshed", "server", server, "count", len(tools))
		return cloneMCPTools(tools), nil
	}
}

func (c *MCPClient) fetchTools(ctx context.Context) ([]MCPTool, error) {
	if err := c.requireDeclaredCapability("tools"); err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	var result struct {
		Tools []MCPTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tools: %w", err)
	}
	return result.Tools, nil
}

func cloneMCPTools(tools []MCPTool) []MCPTool {
	if tools == nil {
		return nil
	}
	cloned := make([]MCPTool, len(tools))
	for i, tool := range tools {
		cloned[i] = tool
		cloned[i].InputSchema = cloneMCPInputSchema(tool.InputSchema)
	}
	return cloned
}

func cloneMCPInputSchema(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(schema))
	for key, value := range schema {
		cloned[key] = cloneMCPJSONValue(value)
	}
	return cloned
}

func cloneMCPJSONValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneMCPInputSchema(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for i, item := range typed {
			cloned[i] = cloneMCPJSONValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case map[string]string:
		return cloneMCPStringMap(typed)
	default:
		return typed
	}
}

// CallTool invokes a tool on the server (Step 2).
func (c *MCPClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (*MCPToolResult, error) {
	if err := c.requireDeclaredCapability("tools"); err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "tools/call", map[string]interface{}{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, fmt.Errorf("call tool %q: %w", name, err)
	}
	var result MCPToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tool result: %w", err)
	}
	return &result, nil
}

// ListResources returns the resources exposed by the server (Step 2). The
// metadata list is cached with the same TTL/merge semantics as tools.
func (c *MCPClient) ListResources(ctx context.Context) ([]MCPResource, error) {
	return c.listResources(ctx, time.Time{})
}

func (c *MCPClient) listResources(ctx context.Context, freshAfter time.Time) ([]MCPResource, error) {
	for {
		now := time.Now()
		c.mu.Lock()
		if c.resourcesCacheValid {
			age := now.Sub(c.resourcesCachedAt)
			cacheIsFresh := freshAfter.IsZero() && age < mcpToolsCacheTTL
			if !freshAfter.IsZero() && !c.resourcesCachedAt.Before(freshAfter) {
				cacheIsFresh = true
			}
			if cacheIsFresh {
				resources := c.resourcesCache
				c.mu.Unlock()
				return resources, nil
			}
		}
		if refreshDone := c.resourcesRefreshDone; refreshDone != nil {
			c.mu.Unlock()
			select {
			case <-refreshDone:
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("list resources: %w", ctx.Err())
			}
		}
		refreshDone := make(chan struct{})
		c.resourcesRefreshDone = refreshDone
		fetchGeneration := c.resourcesInvalidation.generation
		c.mu.Unlock()

		resources, err := c.fetchResources(ctx)
		c.mu.Lock()
		// A resources list-changed during the fetch invalidates this result.
		if err == nil && fetchGeneration == c.resourcesInvalidation.generation && !c.closed && c.transport != nil {
			c.resourcesCache = resources
			c.resourcesCachedAt = time.Now()
			c.resourcesCacheValid = true
		}
		if c.resourcesRefreshDone == refreshDone {
			c.resourcesRefreshDone = nil
			close(refreshDone)
		}
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return resources, nil
	}
}

func (c *MCPClient) fetchResources(ctx context.Context) ([]MCPResource, error) {
	if err := c.requireDeclaredCapability("resources"); err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "resources/list", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	var result struct {
		Resources []MCPResource `json:"resources"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("malformed resource list: %w", ErrInvalidInput)
	}
	for _, resource := range result.Resources {
		if resource.URI == "" {
			return nil, fmt.Errorf("resource list entry is missing its URI: %w", ErrInvalidInput)
		}
	}
	return result.Resources, nil
}

// ReadResource reads a resource by URI (Step 2). Content is never cached and
// every content block is validated before it leaves the client.
func (c *MCPClient) ReadResource(ctx context.Context, uri string) ([]MCPResourceContent, error) {
	if err := c.requireDeclaredCapability("resources"); err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "resources/read", map[string]interface{}{"uri": uri})
	if err != nil {
		return nil, fmt.Errorf("read resource: %w", err)
	}
	contents, err := validateMCPResourceContents(raw)
	if err != nil {
		return nil, fmt.Errorf("read resource %q: %w", uri, err)
	}
	return contents, nil
}

// ListPrompts returns the prompt templates exposed by the server (Step 2).
// The metadata list is cached with the same TTL/merge semantics as tools.
func (c *MCPClient) ListPrompts(ctx context.Context) ([]MCPPrompt, error) {
	return c.listPrompts(ctx, time.Time{})
}

func (c *MCPClient) listPrompts(ctx context.Context, freshAfter time.Time) ([]MCPPrompt, error) {
	for {
		now := time.Now()
		c.mu.Lock()
		if c.promptsCacheValid {
			age := now.Sub(c.promptsCachedAt)
			cacheIsFresh := freshAfter.IsZero() && age < mcpToolsCacheTTL
			if !freshAfter.IsZero() && !c.promptsCachedAt.Before(freshAfter) {
				cacheIsFresh = true
			}
			if cacheIsFresh {
				prompts := c.promptsCache
				c.mu.Unlock()
				return prompts, nil
			}
		}
		if refreshDone := c.promptsRefreshDone; refreshDone != nil {
			c.mu.Unlock()
			select {
			case <-refreshDone:
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("list prompts: %w", ctx.Err())
			}
		}
		refreshDone := make(chan struct{})
		c.promptsRefreshDone = refreshDone
		fetchGeneration := c.promptsInvalidation.generation
		c.mu.Unlock()

		prompts, err := c.fetchPrompts(ctx)
		c.mu.Lock()
		// A prompts list-changed during the fetch invalidates this result.
		if err == nil && fetchGeneration == c.promptsInvalidation.generation && !c.closed && c.transport != nil {
			c.promptsCache = prompts
			c.promptsCachedAt = time.Now()
			c.promptsCacheValid = true
		}
		if c.promptsRefreshDone == refreshDone {
			c.promptsRefreshDone = nil
			close(refreshDone)
		}
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return prompts, nil
	}
}

func (c *MCPClient) fetchPrompts(ctx context.Context) ([]MCPPrompt, error) {
	if err := c.requireDeclaredCapability("prompts"); err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "prompts/list", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("list prompts: %w", err)
	}
	var result struct {
		Prompts []MCPPrompt `json:"prompts"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("malformed prompt list: %w", ErrInvalidInput)
	}
	for _, prompt := range result.Prompts {
		if prompt.Name == "" {
			return nil, fmt.Errorf("prompt list entry is missing its name: %w", ErrInvalidInput)
		}
	}
	return result.Prompts, nil
}

// GetPrompt renders a prompt template by name (Step 2) and preserves the
// role/content provenance of every message.
func (c *MCPClient) GetPrompt(ctx context.Context, name string, args map[string]string) ([]MCPPromptMessage, error) {
	if err := c.requireDeclaredCapability("prompts"); err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "prompts/get", map[string]interface{}{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, fmt.Errorf("get prompt %q: %w", name, err)
	}
	messages, err := validateMCPPromptMessages(raw)
	if err != nil {
		return nil, fmt.Errorf("get prompt %q: %w", name, err)
	}
	return messages, nil
}

// StopServer closes the connection (Step 2).
func (c *MCPClient) StopServer() error {
	return c.stopTransport(true)
}

func (c *MCPClient) stopTransport(markClosed bool) error {
	c.mu.Lock()
	if markClosed && c.closed {
		c.mu.Unlock()
		return nil
	}
	if markClosed {
		c.closed = true
	}
	transport := c.transport
	run := c.run
	dispatchCancel := c.dispatchCancel
	dispatchDone := c.dispatchDone
	pending := c.pending
	c.transport = nil
	c.run = nil
	c.dispatchCancel = nil
	c.dispatchDone = nil
	c.dispatchErr = nil
	c.pending = make(map[string]chan mcpCallResult)
	c.capabilitySnapshot = nil
	c.toolsInvalidation = mcpListInvalidation{}
	c.resourcesInvalidation = mcpListInvalidation{}
	c.promptsInvalidation = mcpListInvalidation{}
	c.notificationSlots = nil
	c.toolsCache = nil
	c.toolsCachedAt = time.Time{}
	c.toolsCacheValid = false
	c.resourcesCache = nil
	c.resourcesCachedAt = time.Time{}
	c.resourcesCacheValid = false
	c.promptsCache = nil
	c.promptsCachedAt = time.Time{}
	c.promptsCacheValid = false
	c.mu.Unlock()

	if dispatchCancel != nil {
		dispatchCancel()
	}
	if run != nil {
		run.cancel()
	}
	deliverMCPPending(pending, fmt.Errorf("client stopped: %w", context.Canceled))

	var closeErr error
	if transport != nil {
		closeErr = transport.Close()
	}
	if dispatchDone != nil {
		<-dispatchDone
	}
	if run != nil {
		run.calls.Wait()
	}
	return closeErr
}
