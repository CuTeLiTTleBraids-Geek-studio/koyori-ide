package services

import (
	"context"
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// AgentToolDefinition is the renderer/headless projection of the authoritative
// agentcore ToolDef. ExecuteKey is intentionally absent: dispatch keys are a
// trusted Go implementation detail, never renderer input.
type AgentToolDefinition struct {
	ID          string                 `json:"id"`
	WireName    string                 `json:"wireName"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
	Source      string                 `json:"source"`
	Risk        string                 `json:"risk"`
	Approval    string                 `json:"approval"`
	Mutation    string                 `json:"mutation"`
	Metadata    map[string]string      `json:"metadata,omitempty"`
}

type AgentToolCatalog struct {
	Revision uint64                `json:"revision"`
	Tools    []AgentToolDefinition `json:"tools"`
}

const maxAgentObservationBytes = 8000

const agentCatalogRefreshAdmissionBoundary = "catalog-refresh-admission-boundary"

func boundAgentObservation(value string) string {
	return boundAgentText(value, int64(len(value)), maxAgentObservationBytes)
}

func boundAgentText(value string, totalBytes int64, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if totalBytes < int64(len(value)) {
		totalBytes = int64(len(value))
	}
	normalized := strings.ToValidUTF8(value, "\uFFFD")
	if totalBytes == int64(len(value)) && len(normalized) <= maxBytes {
		return normalized
	}
	suffix := fmt.Sprintf("\n... [truncated, %d total bytes]", totalBytes)
	if len(suffix) >= maxBytes {
		return suffix[:maxBytes]
	}
	keep := maxBytes - len(suffix)
	if keep < 0 {
		keep = 0
	}
	if keep > len(normalized) {
		keep = len(normalized)
	}
	for keep > 0 && keep < len(normalized) && !utf8.RuneStart(normalized[keep]) {
		keep--
	}
	return normalized[:keep] + suffix
}

// AgentToolExecutionRequest is used for capability issuance and for the
// convenience issue+execute operation. Arguments are structured values at the
// binding boundary and are canonicalized by agentcore before authorization.
type AgentToolExecutionRequest struct {
	SessionID       string                 `json:"sessionId"`
	CatalogRevision uint64                 `json:"catalogRevision"`
	ToolID          string                 `json:"toolId"`
	Arguments       map[string]interface{} `json:"arguments"`
}

type AgentToolCapability struct {
	Token               string    `json:"token"`
	ToolID              string    `json:"toolId"`
	ArgumentsHash       string    `json:"argumentsHash"`
	CatalogRevision     uint64    `json:"catalogRevision"`
	BudgetEpoch         uint64    `json:"budgetEpoch"`
	WorkspaceGeneration uint64    `json:"workspaceGeneration"`
	ExpiresAt           time.Time `json:"expiresAt"`
}

type AgentToolCapabilityExecution struct {
	Token           string                 `json:"token"`
	SessionID       string                 `json:"sessionId"`
	CatalogRevision uint64                 `json:"catalogRevision"`
	ToolID          string                 `json:"toolId"`
	Arguments       map[string]interface{} `json:"arguments"`
}

type AgentExecutionUsage struct {
	UnitID            string    `json:"unitId"`
	SessionID         string    `json:"sessionId"`
	UnitKind          string    `json:"unitKind"`
	Operation         string    `json:"operation"`
	ProviderID        string    `json:"providerId,omitempty"`
	Model             string    `json:"model,omitempty"`
	TokensIn          int       `json:"tokensIn"`
	TokensOut         int       `json:"tokensOut"`
	Cost              float64   `json:"cost"`
	Currency          string    `json:"currency,omitempty"`
	CostBasis         string    `json:"costBasis"`
	Estimated         bool      `json:"estimated"`
	StartedAt         time.Time `json:"startedAt"`
	CompletedAt       time.Time `json:"completedAt"`
	Success           bool      `json:"success"`
	ExternalReceiptID string    `json:"externalReceiptId,omitempty"`
	// Keep false on the wire. An irreversible receipt is represented by
	// externalReceiptReversible=false, and omitting that value makes the
	// renderer unable to distinguish it from a missing receipt contract.
	ExternalReceiptReversible bool   `json:"externalReceiptReversible"`
	ExternalCompensation      string `json:"externalCompensation,omitempty"`
	Pending                   bool   `json:"pending,omitempty"`
	Error                     string `json:"error,omitempty"`
}

type AgentToolExecutionResult struct {
	Observation string              `json:"observation"`
	Metadata    map[string]string   `json:"metadata,omitempty"`
	Usage       AgentExecutionUsage `json:"usage"`
}

type agentExecutionDependencies struct {
	mu sync.RWMutex
	// sessionAdmissionMu serializes renderer Agent stream admission with
	// session teardown. A close marks the logical session as closing before it
	// cancels the current provider worker; a concurrent start therefore either
	// enters before the mark (and is included in the drain) or fails closed.
	sessionAdmissionMu sync.Mutex
	closingSessions    map[string]bool
	// workspaceAuthorityMu makes a ProjectService workspace decision atomic
	// with every Agent authority mutation and tool admission. A transition takes
	// the write side; session create/close, catalog reads, capability issuance,
	// and execution keep the read side through their complete operation.
	workspaceAuthorityMu sync.RWMutex
	// workflowExecutionMu is the shared admission barrier for one AgentService
	// authority. Workflow terminal transitions take the write side; every
	// workflow-session core execution takes the read side across receipt
	// admission and handler completion. Keeping it here covers Wails callers
	// and every TaskService wired to the same agent.
	workflowExecutionMu sync.RWMutex
	file                *FileService
	search              *SearchService
	git                 *GitService
	mcp                 *MCPService
	skills              *SkillsService
	workflow            *WorkflowService
	permission          *AIPermissionService
	ai                  *AIService
	computerUse         *ComputerUseService
	lifecycle           *AgentLifecycle
	settings            *SettingsService
	approveSkill        func(Skill) bool

	mcpHandlerRegistered               bool
	workflowHandlerRegistered          bool
	workflowFileReadHandlerRegistered  bool
	workflowFileWriteHandlerRegistered bool
	workflowGitStatusHandlerRegistered bool
	workflowMCPHandlerRegistered       bool
	workflowAIHandlerRegistered        bool
	skillHandlerRegistered             bool
	// catalogRefreshHook is an unexported deterministic race hook used by
	// package tests immediately before a dynamic source publication.
	catalogRefreshHook       func(stage string)
	catalogMCPRefreshTimeout time.Duration
	// sessionOwnerMutationHook is an unexported deterministic race hook used by
	// package tests between lifecycle/runtime publication and adapter owner-map
	// publication. Production leaves it nil.
	sessionOwnerMutationHook func(stage string)
	// sessionSkills records the immutable fingerprint of each skill activated
	// in an Agent session. The runtime policy reads this map on every capability
	// boundary; a renderer cannot widen it by sending a different session ID.
	sessionSkills map[string]map[string]string
	// sessionOwners binds renderer-issued sessions to the Wails window that
	// created them. Trusted service/headless sessions use trusted=true and are
	// only callable from Go-owned paths (contexts without a Wails window).
	sessionOwners map[string]agentSessionOwner
	// workspaceCatalogDeferralDepth is non-zero while a trusted ProjectService
	// workspace transaction is applying its setters. Skill and MCP callbacks can
	// fire while individual setters are being applied; rebuilding the Agent
	// catalog at that point would observe a mixed root state. Such callbacks mark
	// the catalog dirty and the outer authority guard refreshes once every setter
	// has committed or rolled back.
	workspaceCatalogDeferralDepth  int
	workspaceCatalogRefreshPending bool
}

type agentSessionOwner struct {
	identity string
	trusted  bool
}

// agentWorkspaceAuthorityGuard is the single ProjectService-owned write lease
// for an entire workspace transaction. It uses the same workflow/workspace
// barriers as renderer and headless Agent operations; no parallel authority
// system is introduced.
type agentWorkspaceAuthorityGuard struct {
	mu              sync.Mutex
	agent           *AgentService
	deps            *agentExecutionDependencies
	workflowUnlock  func()
	workspaceUnlock func()
	released        bool
}

// agentCallerContextKey is package-private by design. Production renderer
// calls receive their identity from Wails' application.WindowKey; tests and
// trusted adapters may use the helper below without creating a second public
// authorization surface.
type agentCallerContextKey struct{}

func withAgentCallerContext(ctx context.Context, identity string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, agentCallerContextKey{}, strings.TrimSpace(identity))
}

func agentCallerIdentity(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	if identity, ok := ctx.Value(agentCallerContextKey{}).(string); ok && strings.TrimSpace(identity) != "" {
		return strings.TrimSpace(identity), true
	}
	window, ok := ctx.Value(application.WindowKey).(application.Window)
	if !ok || window == nil {
		return "", false
	}
	name := strings.TrimSpace(window.Name())
	if name == "" && window.ID() == 0 {
		return "", false
	}
	return fmt.Sprintf("wails-window:%d:%s", window.ID(), name), true
}

func agentOwnerForContext(ctx context.Context) (agentSessionOwner, bool) {
	identity, ok := agentCallerIdentity(ctx)
	if !ok {
		return agentSessionOwner{}, false
	}
	return agentSessionOwner{identity: identity}, true
}

// agentWindowForContext returns the concrete Wails renderer target when the
// caller arrived through a window-bound binding. The identity-only test/trusted
// context remains useful for backend admission, but it must never be used as a
// fallback target for sensitive renderer events.
func agentWindowForContext(ctx context.Context) (application.Window, bool) {
	if ctx == nil {
		return nil, false
	}
	window, ok := ctx.Value(application.WindowKey).(application.Window)
	if !ok || window == nil {
		return nil, false
	}
	return window, true
}

func bindAgentSessionOwner(agent *AgentService, sessionID string, owner agentSessionOwner) {
	if agent == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	if deps.sessionOwners == nil {
		deps.sessionOwners = make(map[string]agentSessionOwner)
	}
	deps.sessionOwners[strings.TrimSpace(sessionID)] = owner
	deps.mu.Unlock()
}

func authorizeAgentSessionOwner(agent *AgentService, ctx context.Context, sessionID string) error {
	if agent == nil || strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("agent session is unavailable: %w", ErrNotAllowed)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	owner, known := deps.sessionOwners[strings.TrimSpace(sessionID)]
	deps.mu.RUnlock()
	caller, hasCaller := agentOwnerForContext(ctx)
	// Contexts without a Wails window are trusted service/headless calls. They
	// still traverse Runtime's registered-session and capability checks below;
	// this branch does not grant an unregistered session any authority.
	if !hasCaller {
		return nil
	}
	if !known {
		return fmt.Errorf("agent session owner is unavailable: %w", ErrNotAllowed)
	}
	if owner.trusted || owner.identity != caller.identity {
		return fmt.Errorf("agent session belongs to another caller: %w", ErrNotAllowed)
	}
	return nil
}

func newAgentExternalMutationReceipt(kind string, reversible bool, metadata map[string]string) (agentcore.ExternalMutationReceipt, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return agentcore.ExternalMutationReceipt{}, fmt.Errorf("external mutation kind is required: %w", ErrInvalidInput)
	}
	raw := make([]byte, 24)
	if _, err := crypto_rand.Read(raw); err != nil {
		return agentcore.ExternalMutationReceipt{}, fmt.Errorf("create %s external mutation receipt: %w", kind, err)
	}
	return agentcore.ExternalMutationReceipt{
		ID:         kind + ":" + hex.EncodeToString(raw),
		Reversible: reversible,
		Metadata:   cloneStringMapService(metadata),
	}, nil
}

var agentExecutionDeps sync.Map // map[*AgentService]*agentExecutionDependencies

func executionDependenciesFor(agent *AgentService) *agentExecutionDependencies {
	value, _ := agentExecutionDeps.LoadOrStore(agent, &agentExecutionDependencies{})
	return value.(*agentExecutionDependencies)
}

func lockAgentWorkflowTransition(agent *AgentService) func() {
	if agent == nil {
		return func() {}
	}
	deps := executionDependenciesFor(agent)
	deps.workflowExecutionMu.Lock()
	return deps.workflowExecutionMu.Unlock
}

func lockAgentWorkspaceTransition(agent *AgentService) func() {
	if agent == nil {
		return func() {}
	}
	deps := executionDependenciesFor(agent)
	deps.workspaceAuthorityMu.Lock()
	return deps.workspaceAuthorityMu.Unlock
}

func lockAgentWorkspaceAuthority(agent *AgentService) func() {
	if agent == nil {
		return func() {}
	}
	deps := executionDependenciesFor(agent)
	deps.workspaceAuthorityMu.RLock()
	return deps.workspaceAuthorityMu.RUnlock
}

func acquireAgentSessionAdmission(agent *AgentService, sessionID string) (func(), error) {
	if agent == nil || strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("agent session is unavailable: %w", ErrNotAllowed)
	}
	deps := executionDependenciesFor(agent)
	deps.sessionAdmissionMu.Lock()
	if deps.closingSessions != nil && deps.closingSessions[strings.TrimSpace(sessionID)] {
		deps.sessionAdmissionMu.Unlock()
		return nil, fmt.Errorf("agent session is closing: %w", ErrNotAllowed)
	}
	return deps.sessionAdmissionMu.Unlock, nil
}

func markAgentSessionClosing(agent *AgentService, sessionID string) {
	if agent == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	deps := executionDependenciesFor(agent)
	deps.sessionAdmissionMu.Lock()
	if deps.closingSessions == nil {
		deps.closingSessions = make(map[string]bool)
	}
	deps.closingSessions[strings.TrimSpace(sessionID)] = true
	deps.sessionAdmissionMu.Unlock()
}

func (s *AgentService) beginProjectWorkspaceAuthority() *agentWorkspaceAuthorityGuard {
	workflowUnlock := lockAgentWorkflowTransition(s)
	workspaceUnlock := lockAgentWorkspaceTransition(s)
	deps := executionDependenciesFor(s)
	deps.mu.Lock()
	deps.workspaceCatalogDeferralDepth++
	// A workspace root/generation transition changes the metadata attached to
	// dynamic ToolDefs even when no source callback happens to fire.
	deps.workspaceCatalogRefreshPending = true
	deps.mu.Unlock()
	// Drain a refresh that began just before this authority lease. Refresh
	// admission checks the deferral flag while holding catalogRefreshMu, so a
	// caller is either included in this drain or observes the deferral before it
	// can publish. After this hand-off no catalog publication can overlap the
	// root setters below.
	s.catalogRefreshMu.Lock()
	s.catalogRefreshMu.Unlock()
	return &agentWorkspaceAuthorityGuard{
		agent:           s,
		deps:            deps,
		workflowUnlock:  workflowUnlock,
		workspaceUnlock: workspaceUnlock,
	}
}

func (g *agentWorkspaceAuthorityGuard) validate(agent *AgentService) error {
	if g == nil || agent == nil {
		return fmt.Errorf("workspace authority guard is unavailable: %w", ErrNotAllowed)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.released || g.agent != agent {
		return fmt.Errorf("workspace authority guard is stale or belongs to another agent: %w", ErrNotAllowed)
	}
	return nil
}

func (g *agentWorkspaceAuthorityGuard) release() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.released {
		g.mu.Unlock()
		return
	}
	g.released = true
	deps := g.deps
	agent := g.agent
	shouldRefresh := false
	if deps != nil {
		deps.mu.Lock()
		if deps.workspaceCatalogDeferralDepth > 0 {
			deps.workspaceCatalogDeferralDepth--
		}
		if deps.workspaceCatalogDeferralDepth == 0 && deps.workspaceCatalogRefreshPending {
			deps.workspaceCatalogRefreshPending = false
			shouldRefresh = true
		}
		deps.mu.Unlock()
	}
	workspaceUnlock := g.workspaceUnlock
	workflowUnlock := g.workflowUnlock
	g.mu.Unlock()
	// Keep the write barriers held while rebuilding. The roots are coherent at
	// this point, but releasing them first would let a second workspace
	// transaction mutate the shared context while this refresh is still reading
	// MCP/Skill state, recreating the mixed-root window this deferral closes.
	if shouldRefresh && agent != nil {
		if err := agent.refreshDynamicAgentTools(context.Background()); err != nil {
			// A failed rebuild is fail-closed inside refreshDynamicAgentTools (it
			// clears dynamic sources). Keep the failure observable for operators
			// without turning a committed workspace switch into a mixed catalog.
			slog.Warn("refresh agent catalog after workspace transaction", "error", err)
		}
	}
	if workspaceUnlock != nil {
		workspaceUnlock()
	}
	if workflowUnlock != nil {
		workflowUnlock()
	}
}

// flushCatalog publishes one dynamic-tool snapshot while the workspace write
// barrier is still held. ProjectService calls this after all root setters have
// applied, so a refresh failure can still abort and roll back the transaction.
// Bare AgentService fixtures without an execution runtime have no catalog to
// publish and remain compatible with the root-setter tests.
func (g *agentWorkspaceAuthorityGuard) flushCatalog() error {
	if g == nil || g.agent == nil {
		return nil
	}
	if g.deps != nil {
		g.deps.mu.Lock()
		// The snapshot below consumes every mutation observed before this
		// point. A callback racing with the refresh will set this back to true;
		// the guard release then performs one additional coherent refresh.
		g.deps.workspaceCatalogRefreshPending = false
		g.deps.mu.Unlock()
	}
	g.agent.executionMu.RLock()
	runtime := g.agent.executionRuntime
	g.agent.executionMu.RUnlock()
	if runtime == nil {
		return nil
	}
	err := g.agent.refreshDynamicAgentToolsNow(context.Background())
	if err != nil && g.deps != nil {
		g.deps.mu.Lock()
		// A failed candidate refresh must be retried after rollback, when the
		// previous workspace is coherent again. The dynamic registry itself is
		// already cleared fail-closed by refreshDynamicAgentToolsLocked.
		g.deps.workspaceCatalogRefreshPending = true
		g.deps.mu.Unlock()
	}
	return err
}

// deferWorkspaceCatalogRefresh reports whether a workspace transaction owns
// the catalog publication window. Mutation callbacks use this before taking
// catalogRefreshMu so they cannot publish ToolDefs against mixed roots.
func deferWorkspaceCatalogRefresh(agent *AgentService) bool {
	if agent == nil {
		return false
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deferred := deps.workspaceCatalogDeferralDepth > 0
	if deferred {
		deps.workspaceCatalogRefreshPending = true
	}
	deps.mu.Unlock()
	return deferred
}

func lockAgentWorkflowExecution(agent *AgentService, sessionID string) func() {
	if agent == nil || !strings.HasPrefix(strings.TrimSpace(sessionID), "workflow:") {
		return func() {}
	}
	deps := executionDependenciesFor(agent)
	deps.workflowExecutionMu.RLock()
	return deps.workflowExecutionMu.RUnlock
}

func runAgentCatalogRefreshHook(agent *AgentService, stage string) {
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	hook := deps.catalogRefreshHook
	deps.mu.RUnlock()
	if hook != nil {
		hook(stage)
	}
}

func runAgentSessionOwnerMutationHook(agent *AgentService, stage string) {
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	hook := deps.sessionOwnerMutationHook
	deps.mu.RUnlock()
	if hook != nil {
		hook(stage)
	}
}

func builtinAgentToolDefs() []agentcore.ToolDef {
	return []agentcore.ToolDef{
		{
			ID: "read", Description: "Read a text file inside the active workspace.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1}},"required":["path"],"additionalProperties":false}`),
			Source:      agentcore.SourceBuiltin, Risk: agentcore.RiskReadOnly,
			Approval: agentcore.ApprovalBackendPolicy, Mutation: agentcore.MutationNone, ExecuteKey: "builtin.read",
		},
		{
			ID: "write", Description: "Write complete text content through the workspace edit transaction.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"content":{"type":"string"},"selectedHunks":{"type":"array","items":{"type":"integer"}}},"required":["path","content"],"additionalProperties":false}`),
			Source:      agentcore.SourceBuiltin, Risk: agentcore.RiskElevated,
			Approval: agentcore.ApprovalManual, Mutation: agentcore.MutationWorkspaceTransaction, ExecuteKey: "builtin.write",
		},
		{
			ID: "run", Description: "Execute one argv-based command inside the active workspace without a shell wrapper.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","minLength":1},"cwd":{"type":"string"}},"required":["command"],"additionalProperties":false}`),
			Source:      agentcore.SourceBuiltin, Risk: agentcore.RiskElevated,
			Approval: agentcore.ApprovalManual, Mutation: agentcore.MutationExternal, ExecuteKey: "builtin.run",
		},
		{
			ID: "search", Description: "Search text across files in the active workspace.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1},"ignoreCase":{"type":"boolean"}},"required":["query"],"additionalProperties":false}`),
			Source:      agentcore.SourceBuiltin, Risk: agentcore.RiskReadOnly,
			Approval: agentcore.ApprovalBackendPolicy, Mutation: agentcore.MutationNone, ExecuteKey: "builtin.search",
		},
		{
			ID: "codebase", Description: "Text-search the active workspace and return path+line snippets. Not a vector index. Empty results stay empty.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1},"ignoreCase":{"type":"boolean"}},"required":["query"],"additionalProperties":false}`),
			Source:      agentcore.SourceBuiltin, Risk: agentcore.RiskReadOnly,
			Approval: agentcore.ApprovalBackendPolicy, Mutation: agentcore.MutationNone, ExecuteKey: "builtin.codebase",
		},
		{
			ID: "git.status", Description: "Read Git working-tree status for the active workspace. Does not mutate the repository.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Source:      agentcore.SourceBuiltin, Risk: agentcore.RiskReadOnly,
			Approval: agentcore.ApprovalBackendPolicy, Mutation: agentcore.MutationNone, ExecuteKey: "builtin.git.status",
		},
		{
			ID: "git.diff", Description: "Read a unified diff for one workspace-relative file. Does not mutate the repository.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1}},"required":["path"],"additionalProperties":false}`),
			Source:      agentcore.SourceBuiltin, Risk: agentcore.RiskReadOnly,
			Approval: agentcore.ApprovalBackendPolicy, Mutation: agentcore.MutationNone, ExecuteKey: "builtin.git.diff",
		},
		{
			ID: "plan", Description: "Create an ordered plan of catalog tools for a goal. Empty steps are valid when no provider is available; never invent fake steps.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"goal":{"type":"string","minLength":1},"constraints":{"type":"string"}},"required":["goal"],"additionalProperties":false}`),
			Source:      agentcore.SourceBuiltin, Risk: agentcore.RiskReadOnly,
			Approval: agentcore.ApprovalBackendPolicy, Mutation: agentcore.MutationNone, ExecuteKey: "builtin.plan",
		},
	}
}

func (s *AgentService) initializeExecutionCore() error {
	registry, err := agentcore.NewRegistry(builtinAgentToolDefs())
	if err != nil {
		return err
	}
	runtime, err := agentcore.NewRuntime(registry, agentcore.RuntimeOptions{
		Budget: s.ensureBudget(), Approver: agentCoreApprover{s}, Policy: agentCoreInvocationPolicy{s}, Audit: agentCoreAuditSink{s},
		WorkspaceGeneration: s.agentWorkspaceGeneration,
		EnforceSessions:     true,
	})
	if err != nil {
		return err
	}
	for _, handler := range []struct {
		key  string
		impl agentcore.Handler
	}{
		{"builtin.read", &agentReadHandler{agent: s}},
		{"builtin.write", &agentWriteHandler{agent: s}},
		{"builtin.run", &agentRunHandler{agent: s}},
		{"builtin.search", &agentSearchHandler{agent: s}},
		{"builtin.codebase", &agentCodebaseHandler{agent: s}},
		{"builtin.git.status", &agentGitStatusHandler{agent: s}},
		{"builtin.git.diff", &agentGitDiffHandler{agent: s}},
		{"builtin.plan", &agentPlanHandler{agent: s}},
	} {
		if err := runtime.RegisterHandler(handler.key, handler.impl); err != nil {
			return err
		}
	}
	s.executionMu.Lock()
	s.executionRuntime = runtime
	s.executionMu.Unlock()
	return nil
}

func (s *AgentService) agentWorkspaceGeneration() uint64 {
	s.mu.Lock()
	ctx := s.workspaceContext
	generation := s.rootGeneration
	root := s.rootDir
	s.mu.Unlock()
	if ctx != nil {
		workspaceRoot, workspaceGeneration := ctx.Snapshot()
		if workspaceRoot == "" {
			return 0
		}
		return workspaceGeneration
	}
	if root == "" {
		return 0
	}
	return generation
}

func (s *AgentService) coreRuntime() (*agentcore.Runtime, error) {
	s.executionMu.RLock()
	runtime := s.executionRuntime
	initErr := s.executionInitErr
	s.executionMu.RUnlock()
	if runtime == nil {
		if initErr != nil {
			return nil, fmt.Errorf("agent execution core unavailable: %v: %w", initErr, ErrNotAllowed)
		}
		return nil, fmt.Errorf("agent execution core unavailable: %w", ErrNotAllowed)
	}
	return runtime, nil
}

// createAgentSession is the trusted implementation shared by renderer and
// headless callers. Renderer sessions are bound to a Wails window before the
// opaque ID is returned; trusted sessions are never callable from a renderer
// context.
func (s *AgentService) createAgentSession(kind string, owner agentSessionOwner) (string, error) {
	unlockWorkspace := lockAgentWorkspaceAuthority(s)
	defer unlockWorkspace()
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "chat", "workflow":
	case "plan", "goal":
		return "", fmt.Errorf("%s sessions are owned by their domain service: %w", kind, ErrNotAllowed)
	default:
		return "", fmt.Errorf("unsupported agent session kind %q: %w", kind, ErrInvalidInput)
	}
	deps := executionDependenciesFor(s)
	deps.mu.RLock()
	lifecycle := deps.lifecycle
	settings := deps.settings
	deps.mu.RUnlock()
	permissionMode := agentcore.SessionPermissionAlwaysAsk
	if settings != nil {
		loaded, loadErr := settings.LoadSettings()
		if loadErr == nil {
			permissionMode = agentcore.SessionPermissionMode(loaded.AgentPermissionMode)
			if !permissionMode.Valid() {
				permissionMode = agentcore.SessionPermissionAlwaysAsk
			}
		}
	}
	if lifecycle != nil {
		sessionID, err := lifecycle.createOwnedSessionWithinWorkspaceAuthority(agentcore.SessionKind(kind), permissionMode)
		if err == nil {
			runAgentSessionOwnerMutationHook(s, "before-owner-bind")
			bindAgentSessionOwner(s, sessionID, owner)
		}
		return sessionID, err
	}
	runtime, err := s.coreRuntime()
	if err != nil {
		return "", err
	}
	raw := make([]byte, 24)
	if _, err := crypto_rand.Read(raw); err != nil {
		return "", fmt.Errorf("create agent session: %w", err)
	}
	sessionID := fmt.Sprintf("%s:%s", kind, hex.EncodeToString(raw))
	if err := runtime.RegisterSession(sessionID); err != nil {
		return "", err
	}
	runAgentSessionOwnerMutationHook(s, "before-owner-bind")
	bindAgentSessionOwner(s, sessionID, owner)
	return sessionID, nil
}

// createAgentSessionTrusted is a Go-only session factory used by headless and
// service adapters. It is intentionally unexported so it cannot enter the
// Wails service method set; renderer callers must use
// CreateAgentSessionForCaller so the backend can bind the session to their
// actual Wails window.
func (s *AgentService) createAgentSessionTrusted(kind string) (string, error) {
	return s.createAgentSession(kind, agentSessionOwner{trusted: true})
}

// CreateAgentSessionForCaller creates a renderer-owned session. Wails injects
// the calling window into ctx, so omitting that identity fails closed rather
// than creating a bearer session that another window could reuse.
func (s *AgentService) CreateAgentSessionForCaller(ctx context.Context, kind string) (string, error) {
	owner, ok := agentOwnerForContext(ctx)
	if !ok {
		return "", fmt.Errorf("renderer caller identity is unavailable: %w", ErrNotAllowed)
	}
	return s.createAgentSession(kind, owner)
}

func (s *AgentService) closeAgentSession(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session ID is required: %w", ErrInvalidInput)
	}
	// Mark the session before taking the workspace read lease or cancelling
	// the current worker. StartAgentStream holds the matching admission mutex
	// through provider admission, so a start that wins this race is visible to
	// the drain below and a start that follows it is rejected.
	markAgentSessionClosing(s, sessionID)
	runAgentSessionOwnerMutationHook(s, "after-session-admission-seal")
	unlockWorkspace := lockAgentWorkspaceAuthority(s)
	defer unlockWorkspace()
	deps := executionDependenciesFor(s)
	deps.mu.RLock()
	ai := deps.ai
	deps.mu.RUnlock()
	if ai != nil {
		if err := ai.cancelSessionStreamAndWait(sessionID); err != nil {
			return err
		}
	}
	runtime, err := s.coreRuntime()
	if err != nil {
		return err
	}
	deps.mu.RLock()
	lifecycle := deps.lifecycle
	deps.mu.RUnlock()
	if lifecycle != nil {
		if err := lifecycle.closeByIDWithinWorkspaceAuthority(sessionID); err != nil {
			return err
		}
	} else if runtime.IsSessionRegistered(sessionID) {
		if err := runtime.UnregisterSession(sessionID); err != nil {
			return err
		}
	}
	runAgentSessionOwnerMutationHook(s, "before-owner-delete")
	deps.mu.Lock()
	delete(deps.sessionSkills, sessionID)
	delete(deps.sessionOwners, sessionID)
	deps.mu.Unlock()
	return nil
}

// closeAgentSessionTrusted is the Go-only teardown path. It is intentionally
// unexported so a trusted bearer helper cannot enter the Wails service method
// set. Renderer callers use CloseAgentSessionForCaller so a leaked session ID
// cannot revoke another window's active session.
func (s *AgentService) closeAgentSessionTrusted(sessionID string) error {
	return s.closeAgentSession(sessionID)
}

// CloseAgentSessionForCaller revokes a renderer-owned session after proving
// that the Wails caller is the owner that created it.
func (s *AgentService) CloseAgentSessionForCaller(ctx context.Context, sessionID string) error {
	if _, ok := agentOwnerForContext(ctx); !ok {
		return fmt.Errorf("renderer caller identity is unavailable: %w", ErrNotAllowed)
	}
	if err := authorizeAgentSessionOwner(s, ctx, sessionID); err != nil {
		return err
	}
	return s.closeAgentSession(sessionID)
}

// registerAgentSession is the trusted service-side bridge used by lifecycle
// and orchestration adapters. It is deliberately unexported so renderer code
// cannot register an arbitrary session ID and then widen its authority.
func (s *AgentService) registerAgentSession(sessionID string) error {
	unlockWorkspace := lockAgentWorkspaceAuthority(s)
	defer unlockWorkspace()
	runtime, err := s.coreRuntime()
	if err != nil {
		return err
	}
	if err := runtime.RegisterSession(sessionID); err != nil {
		return err
	}
	bindAgentSessionOwner(s, sessionID, agentSessionOwner{trusted: true})
	return nil
}

// WireAgentExecutionCore injects existing services into the single runtime.
// It is a trusted package-level wiring function and is not exposed as a Wails
// method. Nil optional systems remain fail-closed (their tools are not listed).
func WireAgentExecutionCore(agent *AgentService, file *FileService, search *SearchService, mcp *MCPService, skills *SkillsService, permission *AIPermissionService, gitServices ...*GitService) error {
	if agent == nil {
		return fmt.Errorf("agent service is required: %w", ErrInvalidInput)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.file = file
	deps.search = search
	if len(gitServices) > 0 {
		deps.git = gitServices[0]
	}
	deps.mcp = mcp
	deps.skills = skills
	deps.permission = permission
	if deps.sessionSkills == nil {
		deps.sessionSkills = make(map[string]map[string]string)
	}
	if skills != nil && deps.approveSkill == nil {
		deps.approveSkill = nativeSkillApproval
	}
	deps.mu.Unlock()
	if skills != nil {
		skills.setOnMutationChange(func() error {
			return agent.refreshSkillAgentToolsAfterMutation(context.Background())
		})
	}
	return agent.refreshDynamicAgentTools(context.Background())
}

// clearAgentSkillSessions invalidates session-scoped skill policy whenever the
// workspace or skill source changes. The durable lifecycle reset must publish
// before these maps are cleared: a PersistenceNotPublished failure restores
// the old runtime/session state, so dropping the owner map first would strand
// the caller that still owns that restored authority. An indeterminate or
// poisoned reset has already revoked runtime authority and therefore clears
// the maps fail-closed.
func clearAgentSkillSessions(agent *AgentService) error {
	if agent == nil {
		return nil
	}
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	lifecycle := deps.lifecycle
	deps.mu.RUnlock()
	if lifecycle != nil {
		if err := lifecycle.resetForWorkspaceChange(); err != nil {
			if lifecyclePersistenceIsIndeterminate(err) {
				deps.mu.Lock()
				deps.sessionSkills = make(map[string]map[string]string)
				deps.sessionOwners = make(map[string]agentSessionOwner)
				deps.mu.Unlock()
			}
			return err
		}
	} else if runtime, err := agent.coreRuntime(); err == nil {
		runtime.UnregisterAllSessions()
	}
	deps.mu.Lock()
	deps.sessionSkills = make(map[string]map[string]string)
	deps.sessionOwners = make(map[string]agentSessionOwner)
	deps.mu.Unlock()
	return nil
}

func bindAgentSkillSession(agent *AgentService, sessionID string, skill Skill, fingerprint string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("skill activation session is required: %w", ErrInvalidInput)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	skills := deps.skills
	deps.mu.RUnlock()
	if skills == nil {
		return fmt.Errorf("skill policy service is unavailable: %w", ErrNotAllowed)
	}
	current, err := skills.GetSkill(skill.ID)
	if err != nil || !skills.IsApproved(skill.ID) {
		return fmt.Errorf("skill %q is not approved: %w", skill.ID, ErrNotAllowed)
	}
	currentFingerprint, err := skillFingerprint(current)
	if err != nil || currentFingerprint != fingerprint {
		return fmt.Errorf("skill %q changed before session binding: %w", skill.ID, ErrNotAllowed)
	}
	deps.mu.Lock()
	defer deps.mu.Unlock()
	if deps.sessionSkills == nil {
		deps.sessionSkills = make(map[string]map[string]string)
	}
	bindings := deps.sessionSkills[sessionID]
	if bindings == nil {
		bindings = make(map[string]string)
		deps.sessionSkills[sessionID] = bindings
	}
	bindings[skill.ID] = fingerprint
	return nil
}

func agentSkillBindingSnapshot(agent *AgentService, sessionID, skillID string) (string, bool) {
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	defer deps.mu.RUnlock()
	binding, ok := deps.sessionSkills[sessionID][skillID]
	return binding, ok
}

func restoreAgentSkillActivation(agent *AgentService, sessionID, skillID, fingerprint, priorBinding string, priorBindingPresent, priorApproved bool, scope SkillScope) error {
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	bindings := deps.sessionSkills[sessionID]
	currentBinding, currentPresent := bindings[skillID]
	if currentPresent && currentBinding != fingerprint {
		deps.mu.Unlock()
		return fmt.Errorf("skill %q has a newer session binding and cannot be compensated: %w", skillID, agentcore.ErrExternalCompensation)
	}
	if currentPresent {
		if priorBindingPresent {
			bindings[skillID] = priorBinding
		} else {
			delete(bindings, skillID)
		}
		if len(bindings) == 0 {
			delete(deps.sessionSkills, sessionID)
		}
	}
	stillBound := false
	for _, candidate := range deps.sessionSkills {
		if _, ok := candidate[skillID]; ok {
			stillBound = true
			break
		}
	}
	skills := deps.skills
	deps.mu.Unlock()
	if scope == SkillScopeProject && !priorApproved && !stillBound && skills != nil {
		if err := skills.restoreSkillApprovalTrusted(skillID, false); err != nil {
			return err
		}
	}
	return nil
}

type agentCoreInvocationPolicy struct{ agent *AgentService }

func (p agentCoreInvocationPolicy) Authorize(_ context.Context, invocation agentcore.Invocation) error {
	if p.agent == nil || strings.TrimSpace(invocation.SessionID) == "" {
		return fmt.Errorf("agent session is unavailable: %w", ErrNotAllowed)
	}
	if invocation.Tool.Source == agentcore.SourceWorkflow {
		if _, err := p.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata); err != nil {
			return err
		}
	}
	// Activation is the only operation allowed to establish a session policy.
	// Without this exception a skill could not be reached once another skill
	// had narrowed the session's tool set.
	if invocation.Tool.Source == agentcore.SourceSkill ||
		(invocation.Tool.Source == agentcore.SourceWorkflow && invocation.Tool.Metadata["adapter"] == workflowAdapterSkillActivate) {
		return nil
	}
	deps := executionDependenciesFor(p.agent)
	deps.mu.RLock()
	skillBindings := deps.sessionSkills[invocation.SessionID]
	skills := deps.skills
	bindings := make(map[string]string, len(skillBindings))
	for id, fingerprint := range skillBindings {
		bindings[id] = fingerprint
	}
	deps.mu.RUnlock()
	if len(bindings) == 0 {
		return nil
	}
	if skills == nil {
		return fmt.Errorf("skill policy service is unavailable: %w", ErrNotAllowed)
	}
	loaded := make([]Skill, 0, len(bindings))
	for skillID, expectedFingerprint := range bindings {
		skill, err := skills.GetSkill(skillID)
		if err != nil {
			return fmt.Errorf("session skill %q is unavailable: %w", skillID, ErrNotAllowed)
		}
		if !skills.IsApproved(skillID) {
			return fmt.Errorf("session skill %q is not approved: %w", skillID, ErrNotAllowed)
		}
		fingerprint, err := skillFingerprint(skill)
		if err != nil || fingerprint != expectedFingerprint {
			return fmt.Errorf("session skill %q changed after activation: %w", skillID, ErrNotAllowed)
		}
		loaded = append(loaded, skill)
	}
	if invocation.Tool.Source == agentcore.SourceMCP {
		allowed := allowedMCPForSkills(loaded)
		if !allowed[invocation.Tool.ID] && !allowed[invocation.Tool.WireName] {
			return fmt.Errorf("MCP tool %q is outside the active skill allowlist: %w", invocation.Tool.ID, ErrNotAllowed)
		}
		return nil
	}
	allowedTools := AllowedToolsForSkills(loaded)
	if allowedTools == nil {
		return nil
	}
	for _, allowed := range allowedTools {
		if allowed == invocation.Tool.ID || allowed == invocation.Tool.WireName {
			return nil
		}
	}
	return fmt.Errorf("tool %q is outside the active skill allowlist: %w", invocation.Tool.ID, ErrNotAllowed)
}

func allowedMCPForSkills(skills []Skill) map[string]bool {
	allowed := make(map[string]bool)
	for _, skill := range skills {
		for _, tool := range skill.AllowedMCP {
			if strings.TrimSpace(tool) != "" {
				allowed[tool] = true
			}
		}
	}
	return allowed
}

func (s *AgentService) refreshDynamicAgentTools(ctx context.Context) error {
	unlock, admitted := s.lockDynamicAgentCatalogRefresh()
	if !admitted {
		return nil
	}
	defer unlock()
	return s.refreshDynamicAgentToolsLocked(ctx)
}

// lockDynamicAgentCatalogRefresh makes the deferral decision part of catalog
// lock admission. Checking before acquiring catalogRefreshMu leaves a window
// where a ProjectService transaction can set deferral, drain an empty mutex,
// and start changing roots before the already-admitted refresh publishes.
func (s *AgentService) lockDynamicAgentCatalogRefresh() (func(), bool) {
	s.catalogRefreshMu.Lock()
	runAgentCatalogRefreshHook(s, agentCatalogRefreshAdmissionBoundary)
	if deferWorkspaceCatalogRefresh(s) {
		s.catalogRefreshMu.Unlock()
		return nil, false
	}
	return s.catalogRefreshMu.Unlock, true
}

func (s *AgentService) refreshDynamicAgentToolsNow(ctx context.Context) error {
	s.catalogRefreshMu.Lock()
	defer s.catalogRefreshMu.Unlock()
	return s.refreshDynamicAgentToolsLocked(ctx)
}

func emptyDynamicAgentToolSources() map[agentcore.ToolSource][]agentcore.ToolDef {
	return map[agentcore.ToolSource][]agentcore.ToolDef{
		agentcore.SourceMCP:         nil,
		agentcore.SourceWorkflow:    nil,
		agentcore.SourceSkill:       nil,
		agentcore.SourceComputerUse: nil,
	}
}

func clearDynamicAgentTools(runtime *agentcore.Runtime) error {
	_, err := runtime.Registry().ReplaceSources(emptyDynamicAgentToolSources())
	return err
}

func (s *AgentService) refreshDynamicAgentToolsLocked(ctx context.Context) error {
	runtime, err := s.coreRuntime()
	if err != nil {
		return err
	}
	deps := executionDependenciesFor(s)
	deps.mu.RLock()
	mcp := deps.mcp
	workflow := deps.workflow
	skills := deps.skills
	computerUse := deps.computerUse
	deps.mu.RUnlock()
	replacements := emptyDynamicAgentToolSources()
	if s.agentWorkspaceGeneration() == 0 {
		_, err := runtime.Registry().ReplaceSources(replacements)
		return err
	}

	var refreshErrors []error
	var mcpDefinitions []agentcore.ToolDef
	// MCP ToolDefs are added in the MCP boundary adapter. Until a connected
	// server supplies a schema that can be validated fail-closed, the source is
	// empty rather than exposing a stale renderer-only executor.
	if mcp != nil {
		mcpDefinitions, err = s.buildMCPAgentTools(ctx, runtime, mcp)
		if err != nil {
			refreshErrors = append(refreshErrors, err)
			mcpDefinitions = nil
		}
	}
	replacements[agentcore.SourceMCP] = mcpDefinitions

	var loadedSkills []Skill
	if skills != nil {
		loadedSkills = skills.ListSkills()
	}
	workflowDefinitions, workflowErr := s.buildWorkflowAgentTools(runtime, workflow, mcpDefinitions, skills, loadedSkills)
	if workflowErr != nil {
		refreshErrors = append(refreshErrors, workflowErr)
		workflowDefinitions = nil
	}
	replacements[agentcore.SourceWorkflow] = workflowDefinitions

	skillDefinitions, skillErr := s.buildSkillAgentTools(runtime, skills, loadedSkills)
	if skillErr != nil {
		refreshErrors = append(refreshErrors, skillErr)
		skillDefinitions = nil
	}
	replacements[agentcore.SourceSkill] = skillDefinitions
	replacements[agentcore.SourceComputerUse] = computerUseAgentTools(computerUse)

	if len(refreshErrors) > 0 {
		if clearErr := clearDynamicAgentTools(runtime); clearErr != nil {
			refreshErrors = append(refreshErrors, clearErr)
		}
		return errors.Join(refreshErrors...)
	}
	if _, publishErr := runtime.Registry().ReplaceSources(replacements); publishErr != nil {
		clearErr := clearDynamicAgentTools(runtime)
		refreshErrors = append(refreshErrors, fmt.Errorf("publish dynamic Agent catalog: %w", publishErr), clearErr)
	}
	return errors.Join(refreshErrors...)
}

func (s *AgentService) GetAgentToolCatalog(ctx context.Context) (AgentToolCatalog, error) {
	unlockWorkspace := lockAgentWorkspaceAuthority(s)
	defer unlockWorkspace()
	s.catalogRefreshMu.Lock()
	defer s.catalogRefreshMu.Unlock()
	if err := s.refreshDynamicAgentToolsLocked(ctx); err != nil {
		return AgentToolCatalog{}, err
	}
	runtime, err := s.coreRuntime()
	if err != nil {
		return AgentToolCatalog{}, err
	}
	return projectAgentCatalog(runtime.Registry().Snapshot())
}

func (s *AgentService) RequestAgentToolCapability(ctx context.Context, request AgentToolExecutionRequest) (AgentToolCapability, error) {
	unlockWorkflow := lockAgentWorkflowExecution(s, request.SessionID)
	defer unlockWorkflow()
	unlockWorkspace := lockAgentWorkspaceAuthority(s)
	defer unlockWorkspace()
	if err := authorizeAgentSessionOwner(s, ctx, request.SessionID); err != nil {
		return AgentToolCapability{}, err
	}
	runtime, err := s.coreRuntime()
	if err != nil {
		return AgentToolCapability{}, err
	}
	arguments, err := json.Marshal(request.Arguments)
	if err != nil {
		return AgentToolCapability{}, fmt.Errorf("encode agent tool arguments: %w", ErrInvalidInput)
	}
	grant, err := runtime.RequestCapability(ctx, agentcore.CapabilityRequest{
		SessionID: request.SessionID, CatalogRevision: request.CatalogRevision,
		ToolID: request.ToolID, Arguments: arguments,
	})
	if err != nil {
		return AgentToolCapability{}, redactAgentWorkspaceError(s, err)
	}
	return AgentToolCapability{
		Token: grant.Token, ToolID: grant.ToolID, ArgumentsHash: grant.ArgumentsHash,
		CatalogRevision: grant.CatalogRevision, BudgetEpoch: grant.BudgetEpoch,
		WorkspaceGeneration: grant.WorkspaceGeneration, ExpiresAt: grant.ExpiresAt,
	}, nil
}

func (s *AgentService) ExecuteApprovedAgentTool(ctx context.Context, request AgentToolCapabilityExecution) (AgentToolExecutionResult, error) {
	unlockWorkflow := lockAgentWorkflowExecution(s, request.SessionID)
	defer unlockWorkflow()
	unlockWorkspace := lockAgentWorkspaceAuthority(s)
	defer unlockWorkspace()
	if err := authorizeAgentSessionOwner(s, ctx, request.SessionID); err != nil {
		return AgentToolExecutionResult{}, err
	}
	runtime, err := s.coreRuntime()
	if err != nil {
		return AgentToolExecutionResult{}, err
	}
	arguments, err := json.Marshal(request.Arguments)
	if err != nil {
		return AgentToolExecutionResult{}, fmt.Errorf("encode agent tool arguments: %w", ErrInvalidInput)
	}
	result, err := runtime.Execute(ctx, agentcore.CapabilityExecution{
		Token: request.Token, SessionID: request.SessionID,
		CatalogRevision: request.CatalogRevision, ToolID: request.ToolID, Arguments: arguments,
	})
	projected := projectAgentExecutionResult(s, result, err)
	return projected, redactAgentWorkspaceError(s, err)
}

// ExecuteAgentTool is a convenience facade for a user-initiated execution. It
// still obtains and redeems the same backend capability; no direct handler API
// exists at the renderer boundary.
func (s *AgentService) ExecuteAgentTool(ctx context.Context, request AgentToolExecutionRequest) (AgentToolExecutionResult, error) {
	grant, err := s.RequestAgentToolCapability(ctx, request)
	if err != nil {
		return AgentToolExecutionResult{}, err
	}
	return s.ExecuteApprovedAgentTool(ctx, AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: request.SessionID, CatalogRevision: request.CatalogRevision,
		ToolID: request.ToolID, Arguments: request.Arguments,
	})
}

func (s *AgentService) requestInternalAgentToolCapability(
	ctx context.Context,
	sessionID string,
	toolID string,
	arguments map[string]interface{},
) (AgentToolCapability, error) {
	runtime, err := s.coreRuntime()
	if err != nil {
		return AgentToolCapability{}, err
	}
	revision := runtime.Registry().Snapshot().Revision
	return s.RequestAgentToolCapability(ctx, AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: revision, ToolID: toolID, Arguments: arguments,
	})
}

// executeInternalAgentTool is the trusted service-to-service adapter used by
// Plan/Goal/Task/Workflow. It still traverses the exact same capability
// runtime; the only difference from the renderer facade is that it reads the
// current catalog revision directly instead of accepting one over IPC.
func (s *AgentService) executeInternalAgentTool(
	ctx context.Context,
	sessionID string,
	toolID string,
	arguments map[string]interface{},
) (AgentToolExecutionResult, error) {
	grant, err := s.requestInternalAgentToolCapability(ctx, sessionID, toolID, arguments)
	if err != nil {
		return AgentToolExecutionResult{}, err
	}
	return s.ExecuteApprovedAgentTool(ctx, AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: sessionID, CatalogRevision: grant.CatalogRevision,
		ToolID: toolID, Arguments: arguments,
	})
}

func projectAgentCatalog(catalog agentcore.Catalog) (AgentToolCatalog, error) {
	projected := AgentToolCatalog{Revision: catalog.Revision, Tools: make([]AgentToolDefinition, 0, len(catalog.Tools))}
	for _, definition := range catalog.Tools {
		var schema map[string]interface{}
		if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
			return AgentToolCatalog{}, fmt.Errorf("decode catalog schema for %q: %w", definition.ID, err)
		}
		projected.Tools = append(projected.Tools, AgentToolDefinition{
			ID: definition.ID, WireName: definition.WireName, Description: definition.Description,
			InputSchema: schema, Source: string(definition.Source), Risk: string(definition.Risk),
			Approval: string(definition.Approval), Mutation: string(definition.Mutation),
			Metadata: cloneStringMapService(definition.Metadata),
		})
	}
	return projected, nil
}

func projectAgentExecutionResult(agent *AgentService, result agentcore.ExecutionResult, executionErr error) AgentToolExecutionResult {
	usage := result.Usage
	return AgentToolExecutionResult{
		Observation: boundAgentObservation(redactAgentWorkspaceText(agent, result.Observation)), Metadata: cloneStringMapService(result.Metadata),
		Usage: AgentExecutionUsage{
			UnitID: usage.UnitID, SessionID: usage.SessionID, UnitKind: string(usage.UnitKind),
			Operation: usage.Operation, ProviderID: usage.ProviderID, Model: usage.Model,
			TokensIn: usage.TokensIn, TokensOut: usage.TokensOut, Cost: usage.Cost,
			Currency: usage.Currency, CostBasis: string(usage.CostBasis), Estimated: usage.Estimated,
			StartedAt: usage.StartedAt, CompletedAt: usage.CompletedAt,
			Success:                   usage.Success,
			ExternalReceiptID:         usage.ExternalReceiptID,
			ExternalReceiptReversible: usage.ExternalReceiptReversible,
			ExternalCompensation:      usage.ExternalCompensation,
			Pending:                   usage.Pending, Error: publicAgentUsageError(agent, usage.Error, executionErr),
		},
	}
}

func redactAgentWorkspaceText(agent *AgentService, value string) string {
	if agent == nil || value == "" {
		return value
	}
	root := filepath.Clean(agent.currentWorkspaceRoot())
	if root == "." || root == "" {
		agent.mu.Lock()
		workspace := agent.workspaceContext
		agent.mu.Unlock()
		if workspace != nil {
			root, _ = workspace.Snapshot()
			root = filepath.Clean(root)
		}
	}
	if root == "." || root == "" {
		return value
	}
	value = strings.ReplaceAll(value, root, "<workspace>")
	slashedRoot := filepath.ToSlash(root)
	if slashedRoot != root {
		value = strings.ReplaceAll(value, slashedRoot, "<workspace>")
	}
	return value
}

type redactedAgentWorkspaceError struct {
	message string
	cause   error
}

func (e redactedAgentWorkspaceError) Error() string { return e.message }
func (e redactedAgentWorkspaceError) Unwrap() error { return e.cause }

func publicAgentUsageError(agent *AgentService, value string, err error) string {
	switch {
	case errors.Is(err, ErrUsagePersistence):
		return ErrUsagePersistence.Error()
	case errors.Is(err, ErrUsagePersistenceIndeterminate):
		return ErrUsagePersistenceIndeterminate.Error()
	case errors.Is(err, ErrUsagePersistencePoisoned):
		return ErrUsagePersistencePoisoned.Error()
	case errors.Is(err, ErrUsageReceiptState):
		return ErrUsageReceiptState.Error()
	default:
		return redactAgentWorkspaceText(agent, value)
	}
}

func redactAgentWorkspaceError(agent *AgentService, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, agentcore.ErrApprovalDenied) {
		return redactedAgentWorkspaceError{
			message: "agent tool approval denied: not allowed",
			cause:   errors.Join(err, ErrNotAllowed),
		}
	}
	if errors.Is(err, agentcore.ErrAuditUnavailable) {
		return redactedAgentWorkspaceError{message: agentcore.ErrAuditUnavailable.Error(), cause: err}
	}
	for _, public := range []error{
		ErrUsagePersistence,
		ErrUsagePersistenceIndeterminate,
		ErrUsagePersistencePoisoned,
		ErrUsageReceiptState,
	} {
		if errors.Is(err, public) {
			return redactedAgentWorkspaceError{message: public.Error(), cause: err}
		}
	}
	message := redactAgentWorkspaceText(agent, err.Error())
	if message == err.Error() {
		return err
	}
	return redactedAgentWorkspaceError{message: message, cause: err}
}

func cloneStringMapService(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

type agentCoreAuditSink struct{ agent *AgentService }

func (sink agentCoreAuditSink) RecordAudit(record agentcore.AuditRecord) error {
	if sink.agent == nil {
		return agentcore.ErrAuditUnavailable
	}
	return sink.agent.requiredAuditEvent("agent execution core",
		"stage", record.Stage, "sessionId", record.SessionID, "toolId", record.ToolID,
		"argumentsHash", record.ArgumentsHash, "preparedHash", record.PreparedHash,
		"catalogRevision", record.CatalogRevision, "budgetEpoch", record.BudgetEpoch,
		"workspaceGeneration", record.WorkspaceGeneration,
		"externalReceiptId", record.ExternalReceiptID,
		"externalReceiptReversible", record.ExternalReceiptReversible,
		"externalCompensation", record.ExternalCompensation,
		"success", record.Success,
		"error", redactAgentWorkspaceText(sink.agent, record.Error),
	)
}

func (s *AgentService) requiredAuditEvent(msg string, keyvals ...any) error {
	if s == nil {
		return agentcore.ErrAuditUnavailable
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	if s.auditLogger == nil || s.auditLog == nil {
		return agentcore.ErrAuditUnavailable
	}
	record := slog.NewRecord(time.Now(), slog.LevelInfo, msg, 0)
	record.Add(keyvals...)
	if err := s.auditLogger.Handler().Handle(context.Background(), record); err != nil {
		return fmt.Errorf("write required agent audit: %w", errors.Join(agentcore.ErrAuditUnavailable, err))
	}
	if err := s.auditLog.Sync(); err != nil {
		return fmt.Errorf("sync required agent audit: %w", errors.Join(agentcore.ErrAuditUnavailable, err))
	}
	if err := s.verifyRequiredAuditIdentityLocked(); err != nil {
		return fmt.Errorf("verify required agent audit: %w", errors.Join(agentcore.ErrAuditUnavailable, err))
	}
	return nil
}

// verifyRequiredAuditIdentityLocked proves that the flushed handle is still
// the authoritative root-relative leaf, syncs the containing directory, then
// checks the identity again. The caller holds auditMu.
func (s *AgentService) verifyRequiredAuditIdentityLocked() error {
	if s.auditLog == nil {
		return fmt.Errorf("audit handle is unavailable")
	}
	opened, err := s.auditLog.Stat()
	if err != nil {
		return fmt.Errorf("stat audit handle: %w", err)
	}
	if opened == nil || !opened.Mode().IsRegular() {
		return fmt.Errorf("audit handle is not a regular file")
	}
	if s.auditIdentity != nil && !os.SameFile(s.auditIdentity, opened) {
		return fmt.Errorf("audit handle identity changed")
	}
	if s.auditIdentity == nil {
		s.auditIdentity = opened
	}
	owned, ownerErr := agentFileOwnedByCurrentUser(s.auditLog)
	multipleLinks, linkErr := agentFileHasMultipleLinks(s.auditLog)
	if ownerErr != nil || linkErr != nil || !owned || multipleLinks {
		return fmt.Errorf("audit handle ownership or link identity is unsafe: %w", errors.Join(ownerErr, linkErr))
	}

	root := s.auditRoot
	name := s.auditName
	closeRoot := false
	if root == nil {
		path := s.auditLog.Name()
		name = filepath.Base(path)
		if !validAgentStateLeaf(name) {
			return fmt.Errorf("audit leaf name is invalid")
		}
		root, err = os.OpenRoot(filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("open audit root: %w", err)
		}
		closeRoot = true
	}
	if closeRoot {
		defer root.Close()
	}
	if root == nil || !validAgentStateLeaf(name) {
		return fmt.Errorf("audit root identity is unavailable")
	}
	verifyNamed := func() error {
		named, err := root.Lstat(name)
		if err != nil {
			return fmt.Errorf("inspect audit leaf identity: %w", err)
		}
		if named == nil || named.Mode()&os.ModeSymlink != 0 || !named.Mode().IsRegular() || !os.SameFile(opened, named) {
			return fmt.Errorf("audit leaf identity changed")
		}
		return nil
	}
	if err := verifyNamed(); err != nil {
		return err
	}
	if err := syncAgentStateRoot(root); err != nil {
		return fmt.Errorf("sync audit root: %w", err)
	}
	return verifyNamed()
}

type agentCoreApprover struct{ agent *AgentService }

func (approver agentCoreApprover) sessionMode(sessionID string) (agentcore.SessionPermissionMode, error) {
	if approver.agent == nil {
		return agentcore.SessionPermissionAlwaysAsk, fmt.Errorf("agent session is unavailable: %w", ErrNotAllowed)
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return agentcore.SessionPermissionAlwaysAsk, nil
	}
	deps := executionDependenciesFor(approver.agent)
	deps.mu.RLock()
	lifecycle := deps.lifecycle
	_, trusted := deps.sessionOwners[sessionID]
	deps.mu.RUnlock()
	if lifecycle == nil {
		return agentcore.SessionPermissionAlwaysAsk, nil
	}
	logicalID := sessionID
	session, err := lifecycle.GetByID(logicalID)
	if err != nil {
		if mapped := lifecycle.logicalSessionForRuntime(sessionID); mapped != "" {
			logicalID = mapped
			session, err = lifecycle.GetByID(logicalID)
		}
	}
	if err != nil && trusted {
		return lifecycle.configuredPermissionMode(), nil
	}
	if err != nil {
		return agentcore.SessionPermissionAlwaysAsk, fmt.Errorf("session permission mode is unavailable: %w", ErrNotAllowed)
	}
	if !session.PermissionMode.Valid() {
		return agentcore.SessionPermissionAlwaysAsk, fmt.Errorf("session permission mode is invalid: %w", ErrNotAllowed)
	}
	return session.PermissionMode, nil
}

func (approver agentCoreApprover) Approve(_ context.Context, request agentcore.ApprovalRequest) (bool, error) {
	mode, err := approver.sessionMode(request.SessionID)
	if err != nil {
		return false, err
	}
	if mode == agentcore.SessionPermissionAllowAll {
		// allow-all skips only the user-facing prompt. Every adapter below still
		// performs its structural, identity, workspace, and danger checks.
		return approver.validateWithoutPrompt(request)
	}
	if mode == agentcore.SessionPermissionAssist && request.Invocation.Tool.Approval == agentcore.ApprovalBackendPolicy &&
		request.Invocation.Tool.Risk == agentcore.RiskReadOnly && request.Invocation.Tool.Mutation == agentcore.MutationNone {
		return approver.validateWithoutPrompt(request)
	}
	return approver.validateWithPrompt(request)
}

func (approver agentCoreApprover) validateWithPrompt(request agentcore.ApprovalRequest) (bool, error) {
	return approver.validate(request, true)
}

func (approver agentCoreApprover) validateWithoutPrompt(request agentcore.ApprovalRequest) (bool, error) {
	return approver.validate(request, false)
}

func (approver agentCoreApprover) validate(request agentcore.ApprovalRequest, prompt bool) (bool, error) {
	if approver.agent == nil {
		return false, fmt.Errorf("agent service is unavailable: %w", ErrNotAllowed)
	}
	switch request.Invocation.Tool.ID {
	case "read", "search", "codebase", "git.status", "git.diff", "plan":
		return request.Invocation.Tool.Approval == agentcore.ApprovalBackendPolicy && request.Invocation.Tool.Risk == agentcore.RiskReadOnly && request.Invocation.Tool.Mutation == agentcore.MutationNone, nil
	case "run":
		var prepared preparedRun
		if err := json.Unmarshal(request.Invocation.Arguments, &prepared); err != nil {
			return false, err
		}
		currentGeneration := approver.agent.agentWorkspaceGeneration()
		if request.WorkspaceGeneration == 0 || request.WorkspaceGeneration != currentGeneration {
			return false, fmt.Errorf("prepared run workspace generation is stale: %w", ErrNotAllowed)
		}
		check := approver.agent.CheckCommand(prepared.Command)
		if check.Blocked {
			return false, fmt.Errorf("command blocked: %s", check.BlockReason)
		}
		cwd := request.Metadata["resolvedCwd"]
		if cwd == "" || !filepath.IsAbs(cwd) {
			return false, fmt.Errorf("prepared run cwd is missing or not absolute: %w", ErrNotAllowed)
		}
		canonicalCwd, err := approver.agent.validateCwd(cwd)
		if err != nil || !sameWorkspaceIdentityPath(canonicalCwd, cwd) {
			return false, fmt.Errorf("prepared run cwd is outside or not canonical workspace: %w", ErrNotAllowed)
		}
		if !prompt {
			return true, nil
		}
		if approver.agent.approveCommand == nil {
			return false, nil
		}
		return approver.agent.approveCommand(prepared.Command, canonicalCwd, check.RiskLevel), nil
	case "write":
		var args writeToolArguments
		if err := json.Unmarshal(request.Invocation.Arguments, &args); err != nil {
			return false, err
		}
		absPath := request.Metadata["absPath"]
		if absPath == "" {
			return false, fmt.Errorf("prepared write path is missing: %w", ErrNotAllowed)
		}
		if !prompt {
			return true, nil
		}
		if approver.agent.approveWrite == nil {
			return false, nil
		}
		return approver.agent.approveWrite(absPath, int64(len([]byte(args.Content)))), nil
	default:
		return approver.agent.approveDynamicAgentTool(request, prompt)
	}
}

type pathToolArguments struct {
	Path string `json:"path"`
}

type writeToolArguments struct {
	Path          string `json:"path"`
	Content       string `json:"content"`
	SelectedHunks []int  `json:"selectedHunks,omitempty"`
}

type preparedWrite struct {
	AbsPath        string `json:"absPath"`
	BaselineHash   string `json:"baselineHash"`
	TargetExisted  bool   `json:"targetExisted"`
	RootGeneration uint64 `json:"rootGeneration"`
}

type preparedRun struct {
	Command        string `json:"command"`
	Cwd            string `json:"cwd,omitempty"`
	RootGeneration uint64 `json:"rootGeneration,omitempty"`
}

type searchToolArguments struct {
	Query      string `json:"query"`
	IgnoreCase bool   `json:"ignoreCase"`
}

type agentReadHandler struct{ agent *AgentService }

func (*agentReadHandler) MutationMode() agentcore.MutationMode { return agentcore.MutationNone }
func (h *agentReadHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	var args pathToolArguments
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	root := h.agent.currentWorkspaceRoot()
	absPath, err := ValidatePathWithinRoot(root, filepath.Join(root, filepath.FromSlash(args.Path)))
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	opaque, _ := json.Marshal(map[string]interface{}{"absPath": absPath, "relativePath": args.Path, "rootGeneration": h.agent.agentWorkspaceGeneration()})
	return agentcore.PreparedExecution{Summary: "Read " + args.Path, Opaque: opaque}, nil
}
func (h *agentReadHandler) Execute(_ context.Context, _ agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	var state struct {
		AbsPath        string `json:"absPath"`
		RelativePath   string `json:"relativePath"`
		RootGeneration uint64 `json:"rootGeneration"`
	}
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if state.RootGeneration != h.agent.agentWorkspaceGeneration() {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workspace changed after read approval: %w", ErrNotAllowed)
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	file := deps.file
	deps.mu.RUnlock()
	if file == nil {
		return agentcore.ExecutionOutput{}, fmt.Errorf("file service is not wired: %w", ErrNotAllowed)
	}
	content, err := file.ReadFile(state.AbsPath)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	digest := sha256.Sum256([]byte(content))
	return agentcore.ExecutionOutput{
		Observation: boundAgentObservation("Read " + state.RelativePath + ":\n" + content),
		Metadata: map[string]string{
			"bytes":  strconv.Itoa(len([]byte(content))),
			"sha256": hex.EncodeToString(digest[:]),
		},
	}, nil
}

type agentWriteHandler struct{ agent *AgentService }

var _ agentcore.WorkspaceTransactionHandler = (*agentWriteHandler)(nil)

func (*agentWriteHandler) MutationMode() agentcore.MutationMode {
	return agentcore.MutationWorkspaceTransaction
}
func (h *agentWriteHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	var args writeToolArguments
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	root := h.agent.currentWorkspaceRoot()
	absPath, err := ValidateMutatingPathWithinRoot(root, filepath.Join(root, filepath.FromSlash(args.Path)))
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	file := deps.file
	deps.mu.RUnlock()
	if file == nil {
		return agentcore.PreparedExecution{}, fmt.Errorf("file service is not wired: %w", ErrNotAllowed)
	}
	baseline, readErr := file.ReadFile(absPath)
	existed := readErr == nil
	if readErr != nil {
		if _, statErr := os.Stat(absPath); statErr == nil || !os.IsNotExist(statErr) {
			return agentcore.PreparedExecution{}, readErr
		}
		baseline = ""
	}
	diff := NewDiffService().ComputeFileDiff(args.Path, baseline, args.Content)
	encodedDiff, _ := json.Marshal(diff)
	state := preparedWrite{
		AbsPath: absPath, BaselineHash: contentHash([]byte(baseline)), TargetExisted: existed,
		RootGeneration: h.agent.agentWorkspaceGeneration(),
	}
	opaque, _ := json.Marshal(state)
	return agentcore.PreparedExecution{
		Summary: "Write " + absPath, Opaque: opaque,
		Metadata: map[string]string{"absPath": absPath, "diff": string(encodedDiff), "baseline": baseline},
	}, nil
}
func (h *agentWriteHandler) Execute(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	return h.ExecuteWorkspaceTransaction(ctx, invocation, prepared)
}

func (h *agentWriteHandler) ExecuteWorkspaceTransaction(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	var args writeToolArguments
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	var state preparedWrite
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if state.RootGeneration != h.agent.agentWorkspaceGeneration() {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workspace changed after write approval: %w", ErrNotAllowed)
	}
	root := h.agent.currentWorkspaceRoot()
	if _, err := ValidateMutatingPathWithinRoot(root, state.AbsPath); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	file := deps.file
	deps.mu.RUnlock()
	if file == nil {
		return agentcore.ExecutionOutput{}, fmt.Errorf("file service is not wired: %w", ErrNotAllowed)
	}
	if _, statErr := os.Stat(state.AbsPath); state.TargetExisted && os.IsNotExist(statErr) {
		return agentcore.ExecutionOutput{}, fmt.Errorf("write target was deleted after approval: %w", ErrNotAllowed)
	} else if !state.TargetExisted && statErr == nil {
		return agentcore.ExecutionOutput{}, fmt.Errorf("write target was created after approval: %w", ErrNotAllowed)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return agentcore.ExecutionOutput{}, statErr
	}
	baseline := ""
	if state.TargetExisted {
		current, readErr := file.ReadFile(state.AbsPath)
		if readErr != nil {
			return agentcore.ExecutionOutput{}, readErr
		}
		baseline = current
	}
	content := args.Content
	if args.SelectedHunks != nil {
		preview := NewDiffService().ComputeFileDiff(args.Path, baseline, args.Content)
		content = NewDiffService().ApplySelectedHunks(preview, args.SelectedHunks)
	}
	transaction := EditTransaction{TextEdits: WorkspaceEditPreview{Files: []WorkspaceEditPreviewFile{{
		FilePath: state.AbsPath, BaselineHash: state.BaselineHash, ModifiedContent: content,
	}}}}
	result := applyEditTransaction(ctx, transaction, EditTransactionOptions{
		Root: root,
		Read: func(path string) (string, error) {
			content, err := file.ReadFile(path)
			if err != nil && !state.TargetExisted {
				if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
					return "", nil
				}
			}
			return content, err
		},
		Write:            file.WriteFile,
		WriteIfUnchanged: file.WriteFileIfUnchanged,
	})
	if result.Err != nil {
		return agentcore.ExecutionOutput{}, result.Err
	}
	if len(result.Conflicts) > 0 || result.FailureReason != "" {
		joined := strings.Join(result.Conflicts, "; ")
		if strings.Contains(joined, "hash conflict") || strings.Contains(result.FailureReason, "hash conflict") {
			return agentcore.ExecutionOutput{}, fmt.Errorf("workspace write CAS conflict: %s: %w", joined, ErrConflict)
		}
		return agentcore.ExecutionOutput{}, fmt.Errorf("workspace write transaction rejected: %s %s: %w", result.FailureReason, joined, ErrNotAllowed)
	}
	return agentcore.ExecutionOutput{Observation: fmt.Sprintf("Wrote %s (%d bytes).", args.Path, len([]byte(content)))}, nil
}

type agentRunHandler struct{ agent *AgentService }

var _ agentcore.ExternalMutationTransactionHandler = (*agentRunHandler)(nil)

type agentRunCaptureContextKey struct{}

type agentRunCapture struct {
	execute func(command, cwd string) (ExecResult, error)
	result  ExecResult
	invoked bool
}

func withAgentRunCapture(ctx context.Context, execute func(command, cwd string) (ExecResult, error)) (context.Context, *agentRunCapture) {
	if ctx == nil {
		ctx = context.Background()
	}
	capture := &agentRunCapture{execute: execute}
	return context.WithValue(ctx, agentRunCaptureContextKey{}, capture), capture
}

func (*agentRunHandler) MutationMode() agentcore.MutationMode { return agentcore.MutationExternal }
func (h *agentRunHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	var args preparedRun
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	if check := h.agent.CheckCommand(args.Command); check.Blocked {
		return agentcore.PreparedExecution{}, fmt.Errorf("command blocked: %s", check.BlockReason)
	}
	lease, err := h.agent.acquireWorkspaceLease()
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	resolvedCwd, err := lease.resolve(args.Cwd)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	if _, err := parseCommand(args.Command); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	args.Cwd = resolvedCwd
	args.RootGeneration = h.agent.agentWorkspaceGeneration()
	opaque, _ := json.Marshal(args)
	return agentcore.PreparedExecution{
		Summary:  fmt.Sprintf("Run %q in %s", args.Command, resolvedCwd),
		Opaque:   opaque,
		Metadata: map[string]string{"resolvedCwd": resolvedCwd},
	}, nil
}
func (h *agentRunHandler) Execute(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	output, _, err := h.ExecuteExternalTransaction(ctx, invocation, prepared)
	return output, err
}

func (h *agentRunHandler) BeginExternalMutation(_ context.Context, _ agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExternalMutationReceipt, error) {
	var state preparedRun
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	if state.RootGeneration != h.agent.agentWorkspaceGeneration() {
		return agentcore.ExternalMutationReceipt{}, fmt.Errorf("workspace changed after command approval: %w", ErrNotAllowed)
	}
	return newAgentExternalMutationReceipt("command", false, nil)
}

func (h *agentRunHandler) ExecuteExternalTransaction(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, agentcore.ExternalMutationReceipt, error) {
	receipt, err := h.BeginExternalMutation(ctx, invocation, prepared)
	if err != nil {
		return agentcore.ExecutionOutput{}, agentcore.ExternalMutationReceipt{}, err
	}
	output, executeErr := h.ExecuteExternalTransactionWithReceipt(ctx, invocation, prepared, receipt)
	return output, receipt, executeErr
}

func (h *agentRunHandler) ExecuteExternalTransactionWithReceipt(ctx context.Context, _ agentcore.Invocation, prepared agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) (agentcore.ExecutionOutput, error) {
	var state preparedRun
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if state.RootGeneration != h.agent.agentWorkspaceGeneration() {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workspace changed after command approval: %w", ErrNotAllowed)
	}
	if receipt.ID == "" || receipt.Reversible {
		return agentcore.ExecutionOutput{}, fmt.Errorf("command execution requires its preallocated irreversible receipt: %w", agentcore.ErrExternalMutationContract)
	}
	var result ExecResult
	var executeErr error
	var capture *agentRunCapture
	if ctx != nil {
		capture, _ = ctx.Value(agentRunCaptureContextKey{}).(*agentRunCapture)
	}
	if capture != nil && capture.execute != nil {
		result, executeErr = capture.execute(state.Command, state.Cwd)
		capture.result = result
		capture.invoked = true
	} else {
		lease, leaseErr := h.agent.acquireWorkspaceLease()
		if leaseErr != nil {
			return agentcore.ExecutionOutput{}, leaseErr
		}
		result, executeErr = h.agent.executeCommandWithLease(state.Command, state.Cwd, lease)
	}
	observation := fmt.Sprintf("Ran: %s\nExit code: %d (%dms)\n", result.Command, result.ExitCode, result.DurationMs)
	if result.Stdout != "" {
		observation += "stdout:\n" + result.Stdout + "\n"
	}
	if result.Stderr != "" {
		observation += "stderr:\n" + result.Stderr + "\n"
	}
	return agentcore.ExecutionOutput{Observation: observation, Metadata: map[string]string{"exitCode": fmt.Sprint(result.ExitCode)}}, executeErr
}

func (*agentRunHandler) CompensateExternalTransaction(_ context.Context, _ agentcore.Invocation, _ agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) error {
	if receipt.ID == "" || receipt.Reversible {
		return fmt.Errorf("command receipt is not an irreversible external receipt: %w", agentcore.ErrExternalMutationContract)
	}
	return fmt.Errorf("command execution receipt %q cannot be rolled back: %w", receipt.ID, agentcore.ErrExternalMutationIrreversible)
}

type agentSearchHandler struct{ agent *AgentService }

func (*agentSearchHandler) MutationMode() agentcore.MutationMode { return agentcore.MutationNone }
func (h *agentSearchHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	var args searchToolArguments
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	root := h.agent.currentWorkspaceRoot()
	if root == "" {
		return agentcore.PreparedExecution{}, fmt.Errorf("workspace root is required: %w", ErrNotAllowed)
	}
	opaque, _ := json.Marshal(map[string]interface{}{"query": args.Query, "ignoreCase": args.IgnoreCase, "root": root, "rootGeneration": h.agent.agentWorkspaceGeneration()})
	return agentcore.PreparedExecution{Summary: fmt.Sprintf("Search %q in %s", args.Query, root), Opaque: opaque}, nil
}
func (h *agentSearchHandler) Execute(_ context.Context, _ agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	var state struct {
		Query          string `json:"query"`
		IgnoreCase     bool   `json:"ignoreCase"`
		Root           string `json:"root"`
		RootGeneration uint64 `json:"rootGeneration"`
	}
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if state.RootGeneration != h.agent.agentWorkspaceGeneration() || state.Root != h.agent.currentWorkspaceRoot() {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workspace changed after search approval: %w", ErrNotAllowed)
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	search := deps.search
	deps.mu.RUnlock()
	if search == nil {
		return agentcore.ExecutionOutput{}, fmt.Errorf("search service is not wired: %w", ErrNotAllowed)
	}
	results, err := search.Search(state.Root, state.Query, state.IgnoreCase)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	hits := collectWorkspaceSearchHits(results, 10)
	encoded, _ := json.Marshal(hits)
	return agentcore.ExecutionOutput{Observation: fmt.Sprintf("Search %q: %s", state.Query, encoded)}, nil
}

type workspaceSearchHit struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Preview string `json:"preview"`
}

func collectWorkspaceSearchHits(results []SearchResult, limit int) []workspaceSearchHit {
	if limit <= 0 {
		limit = 10
	}
	hits := make([]workspaceSearchHit, 0)
	for _, result := range results {
		for _, item := range result.Matches {
			hits = append(hits, workspaceSearchHit{Path: result.Path, Line: item.Line, Column: item.Column, Preview: item.Preview})
			if len(hits) == limit {
				break
			}
		}
		if len(hits) == limit {
			break
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].Line < hits[j].Line
	})
	return hits
}

type agentCodebaseHandler struct{ agent *AgentService }

func (*agentCodebaseHandler) MutationMode() agentcore.MutationMode { return agentcore.MutationNone }

func (h *agentCodebaseHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	return (&agentSearchHandler{agent: h.agent}).Prepare(context.Background(), invocation)
}

func (h *agentCodebaseHandler) Execute(_ context.Context, _ agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	var state struct {
		Query          string `json:"query"`
		IgnoreCase     bool   `json:"ignoreCase"`
		Root           string `json:"root"`
		RootGeneration uint64 `json:"rootGeneration"`
	}
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if state.RootGeneration != h.agent.agentWorkspaceGeneration() || state.Root != h.agent.currentWorkspaceRoot() {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workspace changed after codebase search approval: %w", ErrNotAllowed)
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	search := deps.search
	deps.mu.RUnlock()
	if search == nil {
		return agentcore.ExecutionOutput{}, fmt.Errorf("search service is not wired: %w", ErrNotAllowed)
	}
	results, err := search.Search(state.Root, state.Query, state.IgnoreCase)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	hits := collectWorkspaceSearchHits(results, 10)
	if hits == nil {
		hits = []workspaceSearchHit{}
	}
	encoded, _ := json.Marshal(hits)
	return agentcore.ExecutionOutput{
		Observation: boundAgentObservation(fmt.Sprintf("Codebase text search %q (%d hits, not a vector index): %s", state.Query, len(hits), encoded)),
		Metadata:    map[string]string{"hits": strconv.Itoa(len(hits)), "kind": "text-search"},
	}, nil
}

type preparedBuiltinGit struct {
	Root           string `json:"root"`
	RootGeneration uint64 `json:"rootGeneration"`
	RelativePath   string `json:"relativePath,omitempty"`
}

func lookupWiredGitAndFile(agent *AgentService) (*GitService, *FileService, error) {
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	git := deps.git
	file := deps.file
	deps.mu.RUnlock()
	if git == nil || file == nil {
		return nil, nil, fmt.Errorf("Git workspace capabilities are unavailable: %w", ErrNotAllowed)
	}
	return git, file, nil
}

func withWorkspaceRootGit[T any](agent *AgentService, root string, generation uint64, fn func(git *GitService) (T, error)) (T, error) {
	var zero T
	if root == "" || generation == 0 || root != agent.currentWorkspaceRoot() || generation != agent.agentWorkspaceGeneration() {
		return zero, fmt.Errorf("workspace changed after Git tool approval: %w", ErrNotAllowed)
	}
	git, file, err := lookupWiredGitAndFile(agent)
	if err != nil {
		return zero, err
	}
	capability, err := file.acquireCapability(root, false)
	if err != nil {
		return zero, err
	}
	defer capability.releaseCapability()
	if capability.relative != "." {
		return zero, fmt.Errorf("Git tools must use the workspace root capability: %w", ErrNotAllowed)
	}
	var value T
	if err := capability.withCurrent(func() error {
		if err := capability.verifyRootPathIdentity(); err != nil {
			return err
		}
		got, runErr := fn(git)
		if runErr != nil {
			return runErr
		}
		if err := capability.verifyRootPathIdentity(); err != nil {
			return err
		}
		value = got
		return nil
	}); err != nil {
		return zero, err
	}
	return value, nil
}

type agentGitStatusHandler struct{ agent *AgentService }

func (*agentGitStatusHandler) MutationMode() agentcore.MutationMode { return agentcore.MutationNone }

func (h *agentGitStatusHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	var extra map[string]interface{}
	if len(invocation.Arguments) > 0 && strings.TrimSpace(string(invocation.Arguments)) != "" && strings.TrimSpace(string(invocation.Arguments)) != "null" {
		if err := json.Unmarshal(invocation.Arguments, &extra); err != nil {
			return agentcore.PreparedExecution{}, err
		}
		if len(extra) != 0 {
			return agentcore.PreparedExecution{}, fmt.Errorf("git.status does not accept renderer-chosen paths: %w", ErrInvalidInput)
		}
	}
	if _, _, err := lookupWiredGitAndFile(h.agent); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	root := h.agent.currentWorkspaceRoot()
	if root == "" {
		return agentcore.PreparedExecution{}, fmt.Errorf("workspace root is required: %w", ErrNotAllowed)
	}
	opaque, err := json.Marshal(preparedBuiltinGit{Root: root, RootGeneration: h.agent.agentWorkspaceGeneration()})
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	return agentcore.PreparedExecution{Summary: "Read Git status", Opaque: opaque}, nil
}

func (h *agentGitStatusHandler) Execute(_ context.Context, _ agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	var state preparedBuiltinGit
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	changes, err := withWorkspaceRootGit(h.agent, state.Root, state.RootGeneration, func(git *GitService) ([]GitFileChange, error) {
		return git.GetStatus(state.Root)
	})
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	encoded, metadata, err := encodeWorkflowGitStatusObservation(changes)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	return agentcore.ExecutionOutput{Observation: boundAgentObservation("Git status:\n" + encoded), Metadata: metadata}, nil
}

type agentGitDiffHandler struct{ agent *AgentService }

func (*agentGitDiffHandler) MutationMode() agentcore.MutationMode { return agentcore.MutationNone }

func (h *agentGitDiffHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	var args pathToolArguments
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	if strings.TrimSpace(args.Path) == "" {
		return agentcore.PreparedExecution{}, fmt.Errorf("git.diff path is required: %w", ErrInvalidInput)
	}
	git, _, err := lookupWiredGitAndFile(h.agent)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	root := h.agent.currentWorkspaceRoot()
	if root == "" {
		return agentcore.PreparedExecution{}, fmt.Errorf("workspace root is required: %w", ErrNotAllowed)
	}
	if err := git.validateFilePath(root, args.Path); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	opaque, err := json.Marshal(preparedBuiltinGit{Root: root, RootGeneration: h.agent.agentWorkspaceGeneration(), RelativePath: args.Path})
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	return agentcore.PreparedExecution{Summary: "Read Git diff for " + args.Path, Opaque: opaque}, nil
}

func (h *agentGitDiffHandler) Execute(_ context.Context, _ agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	var state preparedBuiltinGit
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	diff, err := withWorkspaceRootGit(h.agent, state.Root, state.RootGeneration, func(git *GitService) (string, error) {
		return git.GetDiff(state.Root, state.RelativePath)
	})
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	return agentcore.ExecutionOutput{
		Observation: boundAgentObservation("Git diff " + state.RelativePath + ":\n" + diff),
		Metadata:    map[string]string{"path": state.RelativePath, "bytes": strconv.Itoa(len(diff))},
	}, nil
}

type planToolArguments struct {
	Goal        string `json:"goal"`
	Constraints string `json:"constraints"`
}

type agentPlanHandler struct{ agent *AgentService }

func (*agentPlanHandler) MutationMode() agentcore.MutationMode { return agentcore.MutationNone }

func (h *agentPlanHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	var args planToolArguments
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	if strings.TrimSpace(args.Goal) == "" {
		return agentcore.PreparedExecution{}, fmt.Errorf("plan goal is required: %w", ErrInvalidInput)
	}
	opaque, err := json.Marshal(args)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	return agentcore.PreparedExecution{Summary: "Plan " + args.Goal, Opaque: opaque}, nil
}

func (h *agentPlanHandler) Execute(ctx context.Context, _ agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	var args planToolArguments
	if err := json.Unmarshal(prepared.Opaque, &args); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	steps, reason, err := generateCatalogPlan(ctx, h.agent, args.Goal, args.Constraints)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if steps == nil {
		steps = []catalogPlanStep{}
	}
	payload, err := json.Marshal(map[string]interface{}{"steps": steps, "reason": reason})
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	return agentcore.ExecutionOutput{
		Observation: boundAgentObservation(string(payload)),
		Metadata:    map[string]string{"steps": strconv.Itoa(len(steps)), "reason": reason},
	}, nil
}
