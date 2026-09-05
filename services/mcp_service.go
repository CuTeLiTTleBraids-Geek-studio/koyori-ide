package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/adrg/xdg"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// ---------------------------------------------------------------------------
// MCPService (Step 4/9)
// ---------------------------------------------------------------------------

// MCPService manages multiple MCP server connections and persists their
// configuration. It is the Wails-bound entry point for the frontend.
//
// G-SEC-09: config is persisted via atomicWriteJSON with 0600 permissions.
// G-SEC-12: MCP servers are treated as Restricted — Enabled defaults to
// false and activation requires explicit user approval.
type MCPService struct {
	mu                   sync.RWMutex
	transportLifecycleMu sync.Mutex
	// transportLifecycleErr is guarded by transportLifecycleMu. Teardown
	// failures remain observable to the final Close even after the client was
	// detached from the active map.
	transportLifecycleErr error
	auditMu               sync.Mutex
	auditWG               sync.WaitGroup
	connectWG             sync.WaitGroup
	toolCallWG            sync.WaitGroup
	disconnectWG          sync.WaitGroup
	config                MCPConfig
	// persistedConfig retains the encrypted representation corresponding to
	// config so unchanged secrets can be reused without mutating the keyring.
	persistedConfig MCPConfig
	// persistTail serializes config and workspace-root mutations without
	// holding mu while waiting for an earlier atomic write to finish.
	persistTail              <-chan struct{}
	persistConfig            func(MCPConfig) error
	encryptSecret            func(string, string) (string, error)
	deleteSecret             func(string) error
	cfgPath                  string
	clients                  map[string]*MCPClient
	rootDir                  string // workspace root for path validation (empty = no sandbox)
	rootGeneration           uint64
	workspaceContext         *WorkspaceContext
	onWorkspaceLeaseAcquired func()
	// lifecycleGeneration invalidates outstanding tool approvals whenever a
	// server connection or configuration can change what a server name means.
	lifecycleGeneration uint64
	onToolsChanged      func()
	auditLog            mcpAuditLog
	closed              bool
	closeErr            error
	shutdownCtx         context.Context
	shutdownCancel      context.CancelFunc
	approvalMu          sync.Mutex
	approvals           map[string]mcpToolApproval
	approveTool         func(server, tool, args string, risk RiskLevel) bool
	approveServer       func(MCPServerConfig) bool
}

// MCPServiceRootSetter is the narrow internal capability ProjectService uses
// to update the MCP workspace sandbox.
type MCPServiceRootSetter interface {
	setWorkspaceRoot(string) error
}

var _ MCPServiceRootSetter = (*MCPService)(nil)

type mcpToolApproval struct {
	server              string
	tool                string
	argsJSON            string
	rootGeneration      uint64
	lifecycleGeneration uint64
	expiresAt           time.Time
}

type mcpAuditLog interface {
	WriteString(string) (int, error)
	Close() error
}

// NewMCPService creates a new MCPService. The config is loaded from
// <configDir>/koyori-ide/mcp-servers.json (G-SEC-09: 0600).
func NewMCPService() *MCPService {
	cfgPath := filepath.Join(xdg.ConfigHome, "koyori-ide", "mcp-servers.json")
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	s := &MCPService{
		cfgPath:        cfgPath,
		clients:        make(map[string]*MCPClient),
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
		approveTool:    nativeMCPToolApproval,
		approveServer:  nativeMCPServerApproval,
	}
	if err := s.load(); err != nil {
		slog.Warn("mcp: failed to load config", "error", err, "path", cfgPath)
	}
	// Best-effort audit log (matches AgentService pattern).
	logPath := filepath.Join(xdg.CacheHome, "koyori-ide", "mcp-audit.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		slog.Warn("mcp: create audit log directory failed", "error", err)
	} else if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
		slog.Warn("mcp: open audit log failed", "error", err)
	} else {
		s.auditLog = f
	}
	return s
}

func nativeMCPServerApproval(cfg MCPServerConfig) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	target := cfg.URL
	if cfg.Transport == "stdio" {
		target = cfg.Command
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Enable MCP server").SetMessage(
		fmt.Sprintf(
			"Server: %s\nTransport: %s\nTarget: %s\nArguments: %d\nEnvironment entries: %d\n\nAllow this server to start and expose tools?",
			cfg.Name, cfg.Transport, target, len(cfg.Args), len(cfg.Env),
		),
	)
	dialog.AddButton("Enable").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("Cancel").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

func nativeMCPToolApproval(server, tool, args string, risk RiskLevel) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Approve MCP tool call").SetMessage(
		fmt.Sprintf("Risk: %s\nServer: %s\nTool: %s\n\nArguments:\n%s", risk, server, tool, args),
	)
	dialog.AddButton("Yes").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("No").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

// setWorkspaceRoot sets the workspace root for stdio command path validation.
// When set, stdio Command paths must resolve within this root. System/PATH
// executables are intentionally not a renderer bypass once a workspace is
// active.
// The unexported capability is intentionally reachable only through
// ProjectService, never through a renderer binding.
func (s *MCPService) setWorkspaceRoot(root string) error {
	if root == "" {
		return fmt.Errorf("MCP workspace root is required: %w", ErrInvalidInput)
	}
	return s.applyWorkspaceRoot(root)
}

// setWorkspaceContext injects the shared workspace identity. MCP keeps its
// local root only to stop transports during coordinated switches; permission
// checks read the shared root and generation at call time.
//
//wails:ignore
func (s *MCPService) setWorkspaceContext(ctx *WorkspaceContext) {
	s.mu.Lock()
	s.workspaceContext = ctx
	s.mu.Unlock()
}

func (s *MCPService) acquireWorkspaceLease() (workspaceLease, uint64, error) {
	s.mu.RLock()
	ctx := s.workspaceContext
	root := s.rootDir
	rootGeneration := s.rootGeneration
	s.mu.RUnlock()
	if ctx == nil && root == "" {
		// MCP is renderer-reachable and stdio commands must never fall back to
		// the process PATH when no committed workspace identity is available.
		return workspaceLease{}, 0, fmt.Errorf("MCP workspace context is unavailable: %w", ErrNotAllowed)
	}
	lease, err := acquireWorkspaceLease(ctx, root, rootGeneration)
	if err != nil {
		return workspaceLease{}, 0, err
	}
	if ctx != nil && (root == "" || !sameWorkspaceIdentityPath(root, lease.root)) {
		return workspaceLease{}, 0, fmt.Errorf("MCP workspace switch is not committed: %w", ErrNotAllowed)
	}
	return lease, rootGeneration, nil
}

func (s *MCPService) notifyWorkspaceLeaseAcquired() {
	s.mu.RLock()
	hook := s.onWorkspaceLeaseAcquired
	s.mu.RUnlock()
	if hook != nil {
		hook()
	}
}

// restoreWorkspaceRoot is used only by ProjectService rollback. It can restore
// the pre-project empty state without exposing an empty-root sandbox bypass as
// a service method or Wails binding.
func (s *MCPService) restoreWorkspaceRoot(root string) error {
	return s.applyWorkspaceRoot(root)
}

func (s *MCPService) applyWorkspaceRoot(root string) error {
	if root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return fmt.Errorf("resolve MCP workspace root: %w", err)
		}
		root = filepath.Clean(abs)
	}
	previous, done := s.reserveConfigWrite()
	<-previous
	s.transportLifecycleMu.Lock()
	s.mu.Lock()
	if s.rootDir == root {
		s.mu.Unlock()
		close(done)
		s.transportLifecycleMu.Unlock()
		return nil
	}
	previousRoot := s.rootDir
	s.rootDir = root
	s.rootGeneration++
	s.lifecycleGeneration++
	for i := range s.config.Servers {
		// Activation consent is bound to the workspace generation. In
		// particular, a relative stdio command must not silently resolve to a
		// different executable after switching roots.
		s.config.Servers[i].Enabled = false
		s.config.Servers[i].executableIdentity = nil
	}
	clients := s.clients
	s.clients = make(map[string]*MCPClient)
	callback := s.onToolsChanged
	s.mu.Unlock()
	var stopErrs []error
	for name, client := range clients {
		if err := client.StopServer(); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("stop mcp server %q after workspace change: %w", name, err))
			slog.Warn("mcp: disconnect after workspace change failed", "server", name, "err", err)
		}
	}
	stopErr := errors.Join(stopErrs...)
	if stopErr != nil {
		// ProjectService does not roll back the setter that itself returned an
		// error. Restore our own root identity, but deliberately keep clients
		// detached and advance both generations again so old authority cannot
		// become valid merely because the pathname was restored.
		s.mu.Lock()
		s.rootDir = previousRoot
		s.rootGeneration++
		s.lifecycleGeneration++
		s.mu.Unlock()
	}
	s.recordTransportLifecycleErrorLocked(stopErr)
	close(done)
	s.transportLifecycleMu.Unlock()
	if callback != nil {
		callback()
	}
	return stopErr
}

func (s *MCPService) recordTransportLifecycleErrorLocked(err error) {
	if err != nil {
		s.transportLifecycleErr = errors.Join(s.transportLifecycleErr, err)
	}
}

func (s *MCPService) WorkspaceRoot() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rootDir
}

//wails:ignore
func (s *MCPService) setOnToolsChanged(callback func()) {
	s.mu.Lock()
	s.onToolsChanged = callback
	s.mu.Unlock()
}

func (s *MCPService) notifyToolsChanged() {
	s.mu.RLock()
	callback := s.onToolsChanged
	s.mu.RUnlock()
	if callback != nil {
		callback()
	}
}

func (s *MCPService) shutdownContextLocked() context.Context {
	if s.shutdownCtx == nil {
		s.shutdownCtx, s.shutdownCancel = context.WithCancel(context.Background())
	}
	return s.shutdownCtx
}

// ConnectServer starts the MCP client for a configured server.
// G-SEC-12: the caller must have explicitly set Enabled=true via SaveServer
// (which requires user approval in the UI).
func (s *MCPService) ConnectServer(ctx context.Context, name string) error {
	lease, rootGeneration, err := s.acquireWorkspaceLease()
	if err != nil {
		// Preserve the stable public error for a missing/disabled configuration
		// before applying the workspace admission gate. An enabled server still
		// receives the fail-closed workspace error; this branch only prevents an
		// unrelated empty-workspace state from masking basic lookup semantics.
		s.mu.RLock()
		closed := s.closed
		cfg, cfgErr := s.getServerLocked(name)
		s.mu.RUnlock()
		if closed {
			return fmt.Errorf("mcp service closed: %w", ErrInvalidInput)
		}
		if cfgErr != nil {
			return cfgErr
		}
		if !cfg.Enabled {
			return fmt.Errorf("server %q not enabled (G-SEC-12): %w", name, ErrUnauthorized)
		}
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("mcp service closed: %w", ErrInvalidInput)
	}
	cfg, err := s.getServerLocked(name)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if !cfg.Enabled {
		s.mu.Unlock()
		return fmt.Errorf("server %q not enabled (G-SEC-12): %w", name, ErrUnauthorized)
	}
	if _, ok := s.clients[name]; ok {
		s.mu.Unlock()
		return fmt.Errorf("server %q already connected: %w", name, ErrAlreadyExists)
	}
	cfg = cloneMCPServerConfig(cfg)
	root := lease.root
	if err := validateMCPServerConfig(cfg, false); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("server %q configuration rejected: %w", name, err)
	}
	shutdownCtx := s.shutdownContextLocked()
	s.connectWG.Add(1)
	s.mu.Unlock()
	defer s.connectWG.Done()
	if cfg.Transport == "http" || cfg.Transport == "sse" {
		if _, err := ValidateNonPrivateURL(cfg.URL); err != nil {
			return fmt.Errorf("server %q remote URL rejected at execution boundary: %w", name, err)
		}
	}
	connectCtx, cancelConnect := context.WithCancel(shutdownCtx)
	callerCancelDone := make(chan struct{})
	var callerCancelDoneOnce sync.Once
	finishCallerCancel := func() {
		callerCancelDoneOnce.Do(func() { close(callerCancelDone) })
	}
	stopCallerCancel := context.AfterFunc(ctx, func() {
		cancelConnect()
		finishCallerCancel()
	})
	if ctx.Err() != nil {
		cancelConnect()
	}
	callerHookDetached := false
	detachCallerHook := func() {
		if callerHookDetached {
			return
		}
		if stopCallerCancel() {
			finishCallerCancel()
		}
		<-callerCancelDone
		callerHookDetached = true
	}
	installed := false
	defer func() {
		detachCallerHook()
		if !installed {
			cancelConnect()
		}
	}()
	if cfg.Transport == "stdio" {
		identity, identityErr := captureMCPExecutableIdentity(root, cfg.Command)
		if identityErr != nil {
			return fmt.Errorf("resolve stdio command for server %q: %w", name, identityErr)
		}
		if cfg.executableIdentity == nil || !sameMCPExecutableIdentity(cfg.executableIdentity, identity) {
			return fmt.Errorf("server %q executable changed after native approval: %w", name, ErrNotAllowed)
		}
		cfg.Command = identity.path
		cfg.workDir = identity.workDir
		cfg.executableIdentity = cloneMCPExecutableIdentity(cfg.executableIdentity)
	}
	if err := lease.validateCurrent(); err != nil {
		return err
	}
	// Construct the client only after renderer-supplied command input has been
	// replaced with the backend-owned canonical path and working directory.
	// MCPClient stores cfg by value, so constructing it earlier would execute
	// the untrusted pre-resolution command even though the local cfg was fixed.
	client := newMCPClient(cfg)
	// The controlled roots/list response may only return the workspace root
	// this connection was opened under; stamp it before the transport starts.
	client.setRootsWorkspaceRoot(root)
	// Server list-changed notifications invalidate the client cache first;
	// the agent catalog refresh then runs off the transport reader on the
	// client's bounded handler goroutine.
	client.setListChangedHandler(func(method string) {
		if method == "notifications/tools/list_changed" {
			s.notifyToolsChanged()
		}
	})
	if err := client.StartServer(connectCtx); err != nil {
		return fmt.Errorf("start server %q: %w", name, err)
	}
	detachCallerHook()
	if err := connectCtx.Err(); err != nil {
		callerErr := ctx.Err()
		if callerErr == nil {
			callerErr = err
		}
		return errors.Join(
			fmt.Errorf("connect server %q canceled: %w", name, callerErr),
			client.StopServer(),
		)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("mcp service closed during connect: %w", ErrInvalidInput),
			client.StopServer(),
		)
	}
	if s.rootGeneration != rootGeneration || !sameWorkspaceIdentityPath(s.rootDir, root) {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("workspace changed during MCP connect: %w", ErrInvalidInput),
			client.StopServer(),
		)
	}
	if err := lease.validateCurrent(); err != nil {
		s.mu.Unlock()
		return errors.Join(err, client.StopServer())
	}
	currentCfg, err := s.getServerLocked(name)
	if err != nil {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("server %q was deleted during connect: %w", name, ErrNotFound),
			client.StopServer(),
		)
	}
	if !currentCfg.Enabled {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("server %q was disabled during connect: %w", name, ErrUnauthorized),
			client.StopServer(),
		)
	}
	if currentCfg.Transport == "stdio" {
		identity, identityErr := captureMCPExecutableIdentity(root, currentCfg.Command)
		if identityErr != nil {
			s.mu.Unlock()
			return errors.Join(
				fmt.Errorf("server %q command is no longer resolvable: %w", name, identityErr),
				client.StopServer(),
			)
		}
		if currentCfg.executableIdentity == nil || !sameMCPExecutableIdentity(currentCfg.executableIdentity, identity) {
			s.mu.Unlock()
			return errors.Join(
				fmt.Errorf("server %q executable changed during connect: %w", name, ErrNotAllowed),
				client.StopServer(),
			)
		}
		currentCfg.Command = identity.path
		currentCfg.workDir = identity.workDir
	}
	if !reflect.DeepEqual(currentCfg, cfg) {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("server %q configuration changed during connect: %w", name, ErrInvalidInput),
			client.StopServer(),
		)
	}
	if _, ok := s.clients[name]; ok {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("server %q already connected (concurrent): %w", name, ErrAlreadyExists),
			client.StopServer(),
		)
	}
	s.clients[name] = client
	s.lifecycleGeneration++
	// Stamp the capability snapshot with the exact workspace and lifecycle
	// identity this install runs under, atomically with the map entry, so a
	// later ServerCapabilities read can fail closed against stale state.
	client.bindCapabilitySnapshot(root, rootGeneration, s.lifecycleGeneration)
	installed = true
	s.mu.Unlock()
	slog.Info("mcp transport", "server", name, "event", "connected")
	s.audit("connect", name, "")
	s.notifyToolsChanged()
	return nil
}

// ServerCapabilities returns the validated initialize capability snapshot of
// a connected server. The snapshot is bound to the client run, server config
// identity, workspace identity, and lifecycle generation it was established
// under: a reconnect, config mutation, or workspace switch fails closed until
// a new initialize completes.
func (s *MCPService) ServerCapabilities(name string) (MCPCapabilitySnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return MCPCapabilitySnapshot{}, fmt.Errorf("mcp service closed: %w", ErrInvalidInput)
	}
	client, ok := s.clients[name]
	if !ok {
		return MCPCapabilitySnapshot{}, fmt.Errorf("server %q not connected: %w", name, ErrNotFound)
	}
	snapshot, err := client.capabilitySnapshotCopy()
	if err != nil {
		return MCPCapabilitySnapshot{}, err
	}
	if !sameWorkspaceIdentityPath(snapshot.WorkspaceRoot, s.rootDir) ||
		snapshot.RootGeneration != s.rootGeneration ||
		snapshot.LifecycleGeneration != s.lifecycleGeneration {
		return MCPCapabilitySnapshot{}, fmt.Errorf("capability snapshot for server %q is bound to an older workspace or lifecycle generation: %w", name, ErrNotAllowed)
	}
	return snapshot, nil
}

// DisconnectServer stops a running MCP client.
func (s *MCPService) DisconnectServer(name string) error {
	s.transportLifecycleMu.Lock()
	s.mu.Lock()
	client, ok := s.clients[name]
	if !ok {
		s.mu.Unlock()
		s.transportLifecycleMu.Unlock()
		return fmt.Errorf("server %q not connected: %w", name, ErrNotFound)
	}
	s.disconnectWG.Add(1)
	delete(s.clients, name)
	s.lifecycleGeneration++
	s.mu.Unlock()
	err := client.StopServer()
	s.disconnectWG.Done()
	if err != nil {
		err = fmt.Errorf("disconnect server %q: %w", name, err)
		s.recordTransportLifecycleErrorLocked(err)
	}
	s.transportLifecycleMu.Unlock()
	slog.Info("mcp transport", "server", name, "event", "disconnected")
	s.audit("disconnect", name, "")
	s.notifyToolsChanged()
	return err
}

// ListTools queries a connected server for its tools.
func (s *MCPService) ListTools(ctx context.Context, name string) ([]MCPTool, error) {
	return s.listTools(ctx, name, false)
}

// listToolsFresh is a trusted execution-boundary query. Renderer-facing tool
// discovery keeps the bounded cache, while receipt allocation must observe a
// tools/list response completed after the fresh query began.
func (s *MCPService) listToolsFresh(ctx context.Context, name string) ([]MCPTool, error) {
	return s.listTools(ctx, name, true)
}

func (s *MCPService) listTools(ctx context.Context, name string, fresh bool) ([]MCPTool, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	s.notifyWorkspaceLeaseAcquired()
	client, err := s.getClient(name)
	if err != nil {
		return nil, err
	}
	var tools []MCPTool
	if fresh {
		tools, err = client.listToolsFresh(ctx)
	} else {
		tools, err = client.ListTools(ctx)
	}
	if err != nil {
		return nil, err
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	return tools, nil
}

// CallTool is retained only for compatibility with package-level callers. It
// is not a renderer API: AgentService owns the only production MCP execution
// capability, and the legacy endpoint is deny-only.
//
//wails:ignore
func (s *MCPService) CallTool(ctx context.Context, server, tool string, args map[string]interface{}) (*MCPToolResult, error) {
	return nil, fmt.Errorf("backend MCP approval token required: %w", ErrInvalidInput)
}

// RequestToolApproval creates a short-lived, single-use capability bound to
// one connected server, tool, argument payload, and workspace generation.
// The legacy token pipeline is no longer a renderer contract; calls must use
// AgentService.RequestAgentToolCapability instead.
//
//wails:ignore
func (s *MCPService) RequestToolApproval(ctx context.Context, server, tool string, args map[string]interface{}) (string, error) {
	return "", fmt.Errorf("use AgentService.RequestAgentToolCapability: %w", ErrInvalidInput)
}

// ExecuteApprovedTool consumes a backend-issued capability and invokes the
// exact tool call it authorizes. Tokens cannot be replayed. This exported Go
// method is retained as a deny-only compatibility shim and is not a renderer
// API; production execution is owned by AgentService.
//
//wails:ignore
func (s *MCPService) ExecuteApprovedTool(ctx context.Context, server, tool string, args map[string]interface{}, approvalToken string) (*MCPToolResult, error) {
	return nil, fmt.Errorf("use AgentService.ExecuteApprovedAgentTool: %w", ErrInvalidInput)
}

func (s *MCPService) executeApprovedToolLegacy(ctx context.Context, server, tool string, args map[string]interface{}, approvalToken string) (*MCPToolResult, error) {
	argsBytes, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode MCP tool arguments: %w", err)
	}
	lease, rootGeneration, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	lifecycleGeneration := s.lifecycleGeneration
	s.mu.RUnlock()
	s.approvalMu.Lock()
	approval, ok := s.approvals[approvalToken]
	if ok {
		delete(s.approvals, approvalToken)
	}
	s.approvalMu.Unlock()
	if !ok || approvalToken == "" || time.Now().After(approval.expiresAt) ||
		approval.server != server || approval.tool != tool || approval.argsJSON != string(argsBytes) ||
		approval.rootGeneration != rootGeneration || approval.lifecycleGeneration != lifecycleGeneration {
		return nil, fmt.Errorf("invalid, expired, or mismatched MCP approval: %w", ErrInvalidInput)
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	return s.callToolWithLease(ctx, server, tool, args, lease)
}

func (s *MCPService) callTool(ctx context.Context, server, tool string, args map[string]interface{}) (*MCPToolResult, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	return s.callToolWithLease(ctx, server, tool, args, lease)
}

func (s *MCPService) callToolWithLease(ctx context.Context, server, tool string, args map[string]interface{}, lease workspaceLease) (*MCPToolResult, error) {
	started := time.Now()
	var client *MCPClient
	var finishCall func()
	err := lease.withCurrent(func() error {
		var beginErr error
		client, finishCall, beginErr = s.beginToolCall(server)
		return beginErr
	})
	if finishCall != nil {
		defer finishCall()
	}
	if err != nil {
		slog.Warn("mcp tool call failed", "server", server, "tool", tool, "duration", time.Since(started), "error", err)
		s.audit("call_tool_failed", server, tool)
		return nil, err
	}
	result, err := client.CallTool(ctx, tool, args)
	duration := time.Since(started)
	if err != nil {
		redacted := redactMCPToolCallError(err, client.cfg, args)
		slog.Warn("mcp tool call failed", "server", server, "tool", tool, "duration", duration, "error", redacted)
		s.audit("call_tool_failed", server, tool)
		return nil, &mcpToolCallError{message: redacted, cause: err}
	}
	if err := lease.validateCurrent(); err != nil {
		slog.Warn("mcp tool result rejected after workspace switch", "server", server, "tool", tool, "duration", duration, "error", err)
		s.audit("call_tool_failed", server, tool)
		return nil, err
	}
	if result != nil && result.IsError {
		slog.Warn("mcp tool call failed", "server", server, "tool", tool, "duration", duration, "error", "tool returned an error result")
		s.audit("call_tool_failed", server, tool)
		return result, nil
	}
	slog.Info("mcp tool call", "server", server, "tool", tool, "duration", duration)
	s.audit("call_tool", server, tool)
	return result, nil
}

func (s *MCPService) beginToolCall(server string) (*MCPClient, func(), error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, nil, fmt.Errorf("mcp service closed: %w", ErrInvalidInput)
	}
	s.toolCallWG.Add(1)
	client, ok := s.clients[server]
	s.mu.RUnlock()

	var once sync.Once
	finish := func() { once.Do(s.toolCallWG.Done) }
	if !ok {
		return nil, finish, fmt.Errorf("server %q not connected: %w", server, ErrNotFound)
	}
	return client, finish, nil
}

func redactMCPToolCallError(err error, cfg MCPServerConfig, args map[string]interface{}) string {
	if err == nil {
		return ""
	}
	secretValues := make([]string, 0, len(cfg.Headers)+len(cfg.Env))
	for _, value := range cfg.Headers {
		secretValues = append(secretValues, value)
	}
	for _, value := range cfg.Env {
		secretValues = append(secretValues, value)
	}
	appendMCPArgumentStrings(&secretValues, args)
	return sanitizeHTTPError(err.Error(), secretValues, cfg.URL)
}

// mcpToolCallError keeps errors.Is/errors.As behavior for cancellation and
// transport diagnostics while exposing only a sanitized message to renderer,
// usage, audit, and structured logging boundaries.
type mcpToolCallError struct {
	message string
	cause   error
}

func (e *mcpToolCallError) Error() string { return e.message }
func (e *mcpToolCallError) Unwrap() error { return e.cause }

func appendMCPArgumentStrings(values *[]string, value interface{}) {
	switch typed := value.(type) {
	case string:
		*values = append(*values, typed)
	case map[string]interface{}:
		for _, nested := range typed {
			appendMCPArgumentStrings(values, nested)
		}
	case []interface{}:
		for _, nested := range typed {
			appendMCPArgumentStrings(values, nested)
		}
	case map[string]string:
		for _, nested := range typed {
			*values = append(*values, nested)
		}
	case []string:
		*values = append(*values, typed...)
	}
}

// MCPResourceRead is a workspace-lease-validated resource read carrying the
// provenance the AI context chain requires (server identity, URI, and the
// generations the read was validated against).
type MCPResourceRead struct {
	Server              string               `json:"server"`
	URI                 string               `json:"uri"`
	Contents            []MCPResourceContent `json:"contents"`
	RootGeneration      uint64               `json:"rootGeneration"`
	LifecycleGeneration uint64               `json:"lifecycleGeneration"`
}

// MCPPromptRender is a workspace-lease-validated prompt rendering. Message
// role/content provenance is preserved; prompt content stays untrusted
// context and is never promoted to system authority.
type MCPPromptRender struct {
	Server              string             `json:"server"`
	Prompt              string             `json:"prompt"`
	Messages            []MCPPromptMessage `json:"messages"`
	RootGeneration      uint64             `json:"rootGeneration"`
	LifecycleGeneration uint64             `json:"lifecycleGeneration"`
}

// ListResources queries a connected server for its resources. The call is
// lease- and generation-gated and returns untrusted server metadata only.
func (s *MCPService) ListResources(ctx context.Context, name string) ([]MCPResource, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	client, err := s.getClient(name)
	if err != nil {
		return nil, err
	}
	resources, err := client.ListResources(ctx)
	if err != nil {
		return nil, err
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	return resources, nil
}

// ReadResource reads a resource by URI from a connected server. The read is
// lease- and generation-validated before and after the request; a workspace
// or lifecycle change rejects the result. Content is size-capped untrusted
// text with full provenance.
func (s *MCPService) ReadResource(ctx context.Context, name, uri string) (*MCPResourceRead, error) {
	lease, rootGeneration, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	lifecycleGeneration := s.lifecycleGeneration
	s.mu.RUnlock()
	client, err := s.getClient(name)
	if err != nil {
		return nil, err
	}
	contents, err := client.ReadResource(ctx, uri)
	if err != nil {
		return nil, err
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	changed := s.lifecycleGeneration != lifecycleGeneration
	s.mu.RUnlock()
	if changed {
		return nil, fmt.Errorf("MCP server %q lifecycle changed during resource read: %w", name, ErrNotAllowed)
	}
	return &MCPResourceRead{
		Server:              name,
		URI:                 uri,
		Contents:            contents,
		RootGeneration:      rootGeneration,
		LifecycleGeneration: lifecycleGeneration,
	}, nil
}

// ListPrompts queries a connected server for its prompts. The call is
// lease- and generation-gated and returns untrusted server metadata only.
func (s *MCPService) ListPrompts(ctx context.Context, name string) ([]MCPPrompt, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	client, err := s.getClient(name)
	if err != nil {
		return nil, err
	}
	prompts, err := client.ListPrompts(ctx)
	if err != nil {
		return nil, err
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	return prompts, nil
}

// GetPrompt renders a prompt template from a connected server with full
// role/content provenance. The render is lease- and generation-validated
// before and after the request.
func (s *MCPService) GetPrompt(ctx context.Context, name, prompt string, args map[string]string) (*MCPPromptRender, error) {
	lease, rootGeneration, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	lifecycleGeneration := s.lifecycleGeneration
	s.mu.RUnlock()
	client, err := s.getClient(name)
	if err != nil {
		return nil, err
	}
	messages, err := client.GetPrompt(ctx, prompt, args)
	if err != nil {
		return nil, err
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	changed := s.lifecycleGeneration != lifecycleGeneration
	s.mu.RUnlock()
	if changed {
		return nil, fmt.Errorf("MCP server %q lifecycle changed during prompt render: %w", name, ErrNotAllowed)
	}
	return &MCPPromptRender{
		Server:              name,
		Prompt:              prompt,
		Messages:            messages,
		RootGeneration:      rootGeneration,
		LifecycleGeneration: lifecycleGeneration,
	}, nil
}

// Close shuts down all running MCP clients. Called on app shutdown.
//
//wails:ignore
func (s *MCPService) Close() error {
	s.transportLifecycleMu.Lock()
	defer s.transportLifecycleMu.Unlock()
	s.mu.Lock()
	if s.closed {
		closeErr := s.closeErr
		s.mu.Unlock()
		return closeErr
	}
	s.closed = true
	shutdownCancel := s.shutdownCancel
	type namedClient struct {
		name   string
		client *MCPClient
	}
	clients := make([]namedClient, 0, len(s.clients))
	for name, client := range s.clients {
		clients = append(clients, namedClient{name: name, client: client})
	}
	s.clients = make(map[string]*MCPClient)
	s.lifecycleGeneration++
	s.mu.Unlock()
	var closeErrs []error
	if s.transportLifecycleErr != nil {
		closeErrs = append(closeErrs, s.transportLifecycleErr)
	}
	for _, entry := range clients {
		if err := entry.client.StopServer(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("stop mcp server %q: %w", entry.name, err))
		}
		slog.Info("mcp transport", "server", entry.name, "event", "disconnected")
	}
	// Stop installed clients under the single transport-lifecycle owner before
	// canceling in-flight connections. The closed gate prevents new connects;
	// shutdownCtx now interrupts only ConnectServer instances that have not yet
	// installed a client, while stdioTransport remains the sole process reaper.
	if shutdownCancel != nil {
		shutdownCancel()
	}
	// ConnectServer reserves connectWG while holding s.mu before starting any
	// transport. The closed gate prevents new reservations after this point.
	s.connectWG.Wait()
	// CallTool reserves toolCallWG under s.mu before looking up its client. This
	// covers response decoding, structured logging, and the final audit record,
	// all of which happen after the client's transport-level call has finished.
	s.toolCallWG.Wait()
	s.disconnectWG.Wait()
	s.auditMu.Lock()
	auditLog := s.auditLog
	s.auditLog = nil
	s.auditMu.Unlock()
	if auditLog != nil {
		s.auditWG.Wait()
		if err := auditLog.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("close mcp audit log: %w", err))
		}
	}
	closeErr := errors.Join(closeErrs...)
	s.mu.Lock()
	s.closeErr = closeErr
	s.mu.Unlock()
	return closeErr
}

// ---------------------------------------------------------------------------
// Agent integration (Step 6/8)
// ---------------------------------------------------------------------------

// AgentMCPTool describes an MCP tool registered with the agent. The name
// follows the mcp.<server>.<tool> namespace (Step 6). Approval remains a
// backend session-level decision; RiskLevel is execution metadata.
type AgentMCPTool struct {
	Namespace   string                 `json:"namespace"` // mcp.<server>.<tool>
	Server      string                 `json:"server"`
	Tool        string                 `json:"tool"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
	RiskLevel   RiskLevel              `json:"riskLevel"`
}

// ListAgentMCPTools returns all tools from all connected MCP servers,
// namespaced as mcp.<server>.<tool> (Step 6).
func (s *MCPService) ListAgentMCPTools(ctx context.Context) ([]AgentMCPTool, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	names := make([]string, 0, len(s.clients))
	for name := range s.clients {
		names = append(names, name)
	}
	s.mu.RUnlock()
	var tools []AgentMCPTool
	for _, server := range names {
		client, err := s.getClient(server)
		if err != nil {
			continue
		}
		serverTools, err := client.ListTools(ctx)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("list MCP tools from %q: %w", server, ctxErr)
			}
			slog.Warn("mcp: list tools failed", "server", server, "error", err)
			continue
		}
		for _, t := range serverTools {
			risk := ClassifyMCPToolRisk(t.Name, t.Description)
			tools = append(tools, AgentMCPTool{
				Namespace:   fmt.Sprintf("mcp.%s.%s", server, t.Name),
				Server:      server,
				Tool:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
				RiskLevel:   risk,
			})
		}
		if err := lease.validateCurrent(); err != nil {
			return nil, err
		}
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	return tools, nil
}

// ClassifyMCPToolRisk determines the risk level of an MCP tool (Step 8).
//
// G-SEC-02: all MCP tools default to RiskElevated. Tools whose names or
// descriptions suggest write/network/exec operations are classified as
// RiskDangerous. No MCP tool is ever classified as RiskSafe; approval remains
// a backend session-policy decision and the audit log records every call.
func ClassifyMCPToolRisk(name, description string) RiskLevel {
	combined := strings.ToLower(name + " " + description)
	// RiskDangerous: write/exec/network/file operations.
	dangerousKeywords := []string{
		"write", "create", "delete", "remove", "exec", "run", "shell",
		"command", "spawn", "kill", "move", "rename", "upload", "download",
		"fetch", "request", "post", "put", "patch",
	}
	for _, kw := range dangerousKeywords {
		if strings.Contains(combined, kw) {
			return RiskDangerous
		}
	}
	return RiskElevated
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (s *MCPService) getClient(name string) (*MCPClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	client, ok := s.clients[name]
	if !ok {
		return nil, fmt.Errorf("server %q not connected: %w", name, ErrNotFound)
	}
	return client, nil
}

func (s *MCPService) getServerLocked(name string) (MCPServerConfig, error) {
	for _, srv := range s.config.Servers {
		if srv.Name == name {
			return srv, nil
		}
	}
	return MCPServerConfig{}, fmt.Errorf("mcp server %q: %w", name, ErrNotFound)
}

func (s *MCPService) audit(action, server, tool string) {
	s.auditMu.Lock()
	auditLog := s.auditLog
	if auditLog != nil {
		s.auditWG.Add(1)
	}
	s.auditMu.Unlock()
	if auditLog == nil {
		return
	}
	defer s.auditWG.Done()
	ts := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf("%s\t%s\tserver=%q\ttool=%q\n", ts, action, server, tool)
	_, err := auditLog.WriteString(line)
	if err != nil {
		slog.Warn("mcp: write audit log failed", "error", err, "action", action, "server", server, "tool", tool)
	}
}
