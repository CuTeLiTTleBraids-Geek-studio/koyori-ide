package agentcore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

var (
	ErrApprovalDenied               = errors.New("agent tool approval denied")
	ErrBudgetExhausted              = errors.New("agent tool budget exhausted")
	ErrHandlerUnavailable           = errors.New("agent tool handler unavailable")
	ErrInvalidCapability            = errors.New("invalid agent tool capability")
	ErrMutationContract             = errors.New("agent tool mutation contract mismatch")
	ErrExternalMutationContract     = errors.New("agent external mutation contract mismatch")
	ErrExternalCompensation         = errors.New("agent external mutation compensation failed")
	ErrExternalMutationIrreversible = errors.New("agent external mutation is irreversible")
	ErrMeterUnavailable             = errors.New("agent usage meter unavailable")
	ErrMeterContract                = errors.New("agent usage meter transaction contract missing")
	ErrAuditUnavailable             = errors.New("agent audit sink unavailable")
	ErrUnknownSession               = errors.New("agent session is not registered")
	ErrSessionSuspended             = errors.New("agent session is suspended")
)

const (
	defaultCapabilityTTL        = 2 * time.Minute
	externalCompensationTimeout = 30 * time.Second
)

// Budget is the single budget-epoch enforcement point used by capability
// issuance. Reserve must atomically check and provisionally spend one unit;
// ReleaseReservation cancels only a reservation for which no capability was
// issued. CurrentEpoch is checked again at redemption so capabilities cannot
// cross an explicit reset. Implementations must make Reserve and
// ReleaseReservation atomic with respect to an epoch reset and concurrent
// reservations.
type Budget interface {
	Reserve() (uint64, error)
	ReleaseReservation(epoch uint64) error
	CurrentEpoch() uint64
}

// Approver is supplied by the host. Desktop may show a native dialog; a
// headless CLI/CI host may apply a trusted policy. Renderer state is never an
// implementation of this interface.
type Approver interface {
	Approve(context.Context, ApprovalRequest) (bool, error)
}

// InvocationPolicy applies host-owned restrictions that are narrower than a
// ToolDef's catalog contract. Skills use it to bind an allowlist to one agent
// session without moving service-specific state into the headless core.
// Runtime checks the policy before preparation, after approval, and again
// after consuming the capability so a policy change cannot leave a usable
// stale token behind.
type InvocationPolicy interface {
	Authorize(context.Context, Invocation) error
}

// ApprovalRequest contains the exact canonical invocation and prepared summary
// that will be capability-bound. Opaque prepared state remains available to a
// trusted host but should not be rendered without an adapter-specific redactor.
type ApprovalRequest struct {
	SessionID           string            `json:"sessionId"`
	Invocation          Invocation        `json:"invocation"`
	Summary             string            `json:"summary"`
	BudgetEpoch         uint64            `json:"budgetEpoch"`
	WorkspaceGeneration uint64            `json:"workspaceGeneration"`
	Metadata            map[string]string `json:"metadata,omitempty"`
}

// PreparedExecution captures identity or conflict state before approval. File
// writes use it for the pre-dialog baseline hash; command adapters use it for
// resolved argv/cwd. It is stored only inside the one-time capability.
type PreparedExecution struct {
	Summary  string            `json:"summary"`
	Opaque   json.RawMessage   `json:"opaque"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ExecutionOutput is the handler observation returned to the model/UI.
type ExecutionOutput struct {
	Observation string            `json:"observation"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	// Usage carries trusted provider-reported details from adapters such as the
	// workflow AI adapter. Runtime still owns UnitID/session/timestamps and
	// terminal state; handlers cannot replace those identity fields.
	Usage *UsageRecord `json:"-"`
}

// Handler is a trusted adapter for one ExecuteKey. Prepare performs all
// pre-approval resolution without side effects; Execute receives precisely the
// stored prepared state after every capability binding has been revalidated.
type Handler interface {
	MutationMode() MutationMode
	Prepare(context.Context, Invocation) (PreparedExecution, error)
	Execute(context.Context, Invocation, PreparedExecution) (ExecutionOutput, error)
}

// WorkspaceTransactionHandler is the execution boundary for tools that
// mutate workspace files. The Runtime calls this method (rather than the
// generic Handler.Execute method) after capability validation, so a
// workspace-mutating ToolDef cannot merely declare a matching mutation string
// while bypassing the edit transaction layer.
type WorkspaceTransactionHandler interface {
	Handler
	ExecuteWorkspaceTransaction(context.Context, Invocation, PreparedExecution) (ExecutionOutput, error)
}

// ExternalMutationReceipt is the adapter-owned proof that an external side
// effect was attempted. Unlike a workspace edit transaction, an MCP call or
// process invocation may be irreversible; the explicit Reversible bit keeps
// that fact visible and prevents the runtime from silently treating a meter
// failure as a successful operation.
type ExternalMutationReceipt struct {
	ID         string            `json:"id"`
	Reversible bool              `json:"reversible"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

const (
	ExternalCompensationPending      = "pending"
	ExternalCompensationNotNeeded    = "not-needed"
	ExternalCompensationSucceeded    = "compensated"
	ExternalCompensationFailed       = "compensation-failed"
	ExternalCompensationIrreversible = "irreversible"
	// ExternalCompensationManualUnknown records an explicit trusted operator
	// disposition after a process restart when the external side effect cannot
	// be reconstructed safely. It never claims that compensation ran.
	ExternalCompensationManualUnknown = "manual-unknown"
)

// ExternalMutationHandler is the common boundary for side effects outside
// workspace_edit_transaction.go. ExecuteExternalTransaction must return a
// non-empty receipt whenever it may have changed external state. The runtime
// invokes CompensateExternalTransaction when durable usage completion fails or
// when execution reports an error after a partial side effect.
type ExternalMutationHandler interface {
	Handler
	ExecuteExternalTransaction(context.Context, Invocation, PreparedExecution) (ExecutionOutput, ExternalMutationReceipt, error)
	CompensateExternalTransaction(context.Context, Invocation, PreparedExecution, ExternalMutationReceipt) error
}

// ExternalMutationTransactionHandler is the production boundary for external
// side effects. BeginExternalMutation must be side-effect free: the runtime
// persists its receipt before ExecuteExternalTransactionWithReceipt is called.
type ExternalMutationTransactionHandler interface {
	ExternalMutationHandler
	BeginExternalMutation(context.Context, Invocation, PreparedExecution) (ExternalMutationReceipt, error)
	ExecuteExternalTransactionWithReceipt(context.Context, Invocation, PreparedExecution, ExternalMutationReceipt) (ExecutionOutput, error)
}

type CapabilityRequest struct {
	SessionID       string          `json:"sessionId"`
	CatalogRevision uint64          `json:"catalogRevision"`
	ToolID          string          `json:"toolId"`
	Arguments       json.RawMessage `json:"arguments"`
}

type CapabilityGrant struct {
	Token               string    `json:"token"`
	ToolID              string    `json:"toolId"`
	ArgumentsHash       string    `json:"argumentsHash"`
	CatalogRevision     uint64    `json:"catalogRevision"`
	BudgetEpoch         uint64    `json:"budgetEpoch"`
	WorkspaceGeneration uint64    `json:"workspaceGeneration"`
	ExpiresAt           time.Time `json:"expiresAt"`
}

type CapabilityExecution struct {
	Token           string          `json:"token"`
	SessionID       string          `json:"sessionId"`
	CatalogRevision uint64          `json:"catalogRevision"`
	ToolID          string          `json:"toolId"`
	Arguments       json.RawMessage `json:"arguments"`
}

type ExecutionResult struct {
	ExecutionOutput
	Usage UsageRecord `json:"usage"`
}

type AuditStage string

const (
	AuditCapabilityIssued   AuditStage = "capability-issued"
	AuditExecutionStarted   AuditStage = "execution-started"
	AuditExecutionCompleted AuditStage = "execution-completed"
	AuditExecutionFailed    AuditStage = "execution-failed"
)

// AuditRecord intentionally stores only hashes, IDs, and outcomes. Tool
// arguments can contain source code or secrets and do not belong in a general
// audit log.
type AuditRecord struct {
	Timestamp                 time.Time  `json:"timestamp"`
	Stage                     AuditStage `json:"stage"`
	SessionID                 string     `json:"sessionId"`
	ToolID                    string     `json:"toolId"`
	ArgumentsHash             string     `json:"argumentsHash"`
	PreparedHash              string     `json:"preparedHash,omitempty"`
	CatalogRevision           uint64     `json:"catalogRevision"`
	BudgetEpoch               uint64     `json:"budgetEpoch"`
	WorkspaceGeneration       uint64     `json:"workspaceGeneration"`
	ExternalReceiptID         string     `json:"externalReceiptId,omitempty"`
	ExternalReceiptReversible bool       `json:"externalReceiptReversible,omitempty"`
	ExternalCompensation      string     `json:"externalCompensation,omitempty"`
	Success                   bool       `json:"success"`
	Error                     string     `json:"error,omitempty"`
}

// AuditSink returns nil only after the record has been accepted durably enough
// for the host's audit contract. Runtime writes an execution-started record
// after the pending usage receipt and before invoking a handler, so a broken
// audit destination cannot silently admit a side effect.
type AuditSink interface {
	RecordAudit(AuditRecord) error
}

type UsageUnitKind string

const (
	UsageUnitTool     UsageUnitKind = "tool"
	UsageUnitAI       UsageUnitKind = "ai"
	UsageUnitChat     UsageUnitKind = "chat"
	UsageUnitPlan     UsageUnitKind = "plan"
	UsageUnitGoal     UsageUnitKind = "goal"
	UsageUnitWorkflow UsageUnitKind = "workflow"
)

type CostBasis string

const (
	CostProviderReported CostBasis = "provider-reported"
	CostEstimated        CostBasis = "estimated"
	CostNotApplicable    CostBasis = "not-applicable"
)

// UsageRecord is the G33 metering contract. CostBasis and Estimated make it
// impossible to present a local estimate as a provider bill.
type UsageRecord struct {
	UnitID                    string        `json:"unitId"`
	SessionID                 string        `json:"sessionId"`
	UnitKind                  UsageUnitKind `json:"unitKind"`
	Operation                 string        `json:"operation"`
	ProviderID                string        `json:"providerId,omitempty"`
	Model                     string        `json:"model,omitempty"`
	TokensIn                  int           `json:"tokensIn"`
	TokensOut                 int           `json:"tokensOut"`
	Cost                      float64       `json:"cost"`
	Currency                  string        `json:"currency,omitempty"`
	CostBasis                 CostBasis     `json:"costBasis"`
	Estimated                 bool          `json:"estimated"`
	StartedAt                 time.Time     `json:"startedAt"`
	CompletedAt               time.Time     `json:"completedAt"`
	Success                   bool          `json:"success"`
	ExternalReceiptID         string        `json:"externalReceiptId,omitempty"`
	ExternalReceiptReversible bool          `json:"externalReceiptReversible,omitempty"`
	ExternalCompensation      string        `json:"externalCompensation,omitempty"`
	// Pending is true only for the durable pre-execution receipt. Ledgers
	// should replace that row with the terminal record sharing UnitID.
	Pending bool   `json:"pending,omitempty"`
	Error   string `json:"error,omitempty"`
}

type UsageSink interface {
	RecordUsage(UsageRecord) error
}

// UsageReceipt identifies a durable, in-progress execution unit. A receipt is
// intentionally opaque to callers; only the trusted runtime and its meter can
// complete it. Persisting the receipt before a handler runs prevents a side
// effect from becoming invisible when the final usage write fails.
type UsageReceipt struct {
	UnitID string
}

// UsageTransactionSink is the stronger metering contract used by production
// execution. BeginUsage must durably record an in-progress unit before the
// handler is invoked. CompleteUsage records the terminal outcome using the
// same receipt and must be idempotent so the runtime can retry a failed
// terminal write after compensating an external mutation. Implementations
// should upsert by UnitID when their ledger is append-only so repeated terminal
// rows cannot be counted twice.
type UsageTransactionSink interface {
	UsageSink
	BeginUsage(UsageRecord) (UsageReceipt, error)
	CompleteUsage(UsageReceipt, UsageRecord) error
}

type RuntimeOptions struct {
	Budget              Budget
	Approver            Approver
	Policy              InvocationPolicy
	Audit               AuditSink
	Meter               UsageSink
	WorkspaceGeneration func() uint64
	CapabilityTTL       time.Duration
	Now                 func() time.Time
	Random              io.Reader
	// EnforceSessions makes capability issuance fail closed for renderer-provided
	// session IDs that were not first registered by trusted host wiring.
	// Generic headless callers may leave this false and manage their own
	// session namespace; the desktop AgentService enables it in production.
	EnforceSessions bool
	// RequireMeter makes capability issuance and execution fail closed until a
	// trusted usage sink is installed. It is enabled by desktop bootstrap after
	// AgentLifecycle wiring; lightweight headless callers can opt in explicitly.
	RequireMeter bool
	// RequireUsageTransaction additionally requires the sink to implement the
	// two-phase receipt contract for tool executions. This closes the gap where a
	// handler side effect succeeds but its only usage write fails afterward.
	RequireUsageTransaction bool
}

type capability struct {
	invocation          Invocation
	prepared            PreparedExecution
	preparedHash        string
	sessionID           string
	sessionGeneration   uint64
	budgetEpoch         uint64
	workspaceGeneration uint64
	expiresAt           time.Time
}

type sessionState struct {
	active     bool
	generation uint64
}

// RuntimeSnapshot is an opaque in-process snapshot used only by a trusted
// workspace rollback. One-time capabilities are deliberately excluded: an
// attempted workspace transition burns every outstanding approval even when
// the surrounding project transaction later rolls back.
type RuntimeSnapshot struct {
	sessions              map[string]sessionState
	nextSessionGeneration uint64
}

// Runtime is the one headless execution facade. Tool handlers are registered
// by trusted Go wiring; renderer-facing APIs can only request and redeem a
// capability through this object.
type Runtime struct {
	registry            *Registry
	budget              Budget
	approver            Approver
	policy              InvocationPolicy
	audit               AuditSink
	meter               UsageSink
	workspaceGeneration func() uint64
	capabilityTTL       time.Duration
	now                 func() time.Time
	random              io.Reader
	enforceSessions     bool
	requireMeter        bool
	requireUsageTxn     bool

	mu                    sync.Mutex
	handlers              map[string]Handler
	capabilities          map[string]capability
	sessions              map[string]sessionState
	nextSessionGeneration uint64
}

func NewRuntime(registry *Registry, options RuntimeOptions) (*Runtime, error) {
	if registry == nil || options.Budget == nil || options.Approver == nil || options.Audit == nil || options.WorkspaceGeneration == nil {
		return nil, fmt.Errorf("registry, budget, approver, audit, and workspace generation are required")
	}
	ttl := options.CapabilityTTL
	if ttl <= 0 {
		ttl = defaultCapabilityTTL
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	return &Runtime{
		registry: registry, budget: options.Budget, approver: options.Approver,
		policy: options.Policy, audit: options.Audit, meter: options.Meter,
		workspaceGeneration: options.WorkspaceGeneration,
		capabilityTTL:       ttl, now: now, random: random,
		enforceSessions: options.EnforceSessions,
		requireMeter:    options.RequireMeter, requireUsageTxn: options.RequireUsageTransaction,
		handlers: make(map[string]Handler), capabilities: make(map[string]capability),
		sessions: make(map[string]sessionState),
	}, nil
}

func (r *Runtime) Registry() *Registry { return r.registry }

func cloneCapability(value capability) capability {
	copy := value
	copy.invocation.Arguments = append(json.RawMessage(nil), value.invocation.Arguments...)
	copy.invocation.Tool = cloneToolDef(value.invocation.Tool)
	copy.prepared.Opaque = append(json.RawMessage(nil), value.prepared.Opaque...)
	copy.prepared.Metadata = cloneStringMap(value.prepared.Metadata)
	return copy
}

// CaptureSnapshot records all runtime authority under the runtime lock. The
// returned value must stay in-process; it is not a persistence format.
func (r *Runtime) CaptureSnapshot() RuntimeSnapshot {
	if r == nil {
		return RuntimeSnapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := RuntimeSnapshot{
		sessions:              make(map[string]sessionState, len(r.sessions)),
		nextSessionGeneration: r.nextSessionGeneration,
	}
	for id, state := range r.sessions {
		snapshot.sessions[id] = state
	}
	return snapshot
}

// RestoreSnapshot reinstates a previously captured runtime namespace. The
// caller is responsible for restoring durable lifecycle rows first; this
// method never claims persistence success and therefore has no error result.
func (r *Runtime) RestoreSnapshot(snapshot RuntimeSnapshot) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = make(map[string]sessionState, len(snapshot.sessions))
	for id, state := range snapshot.sessions {
		r.sessions[id] = state
	}
	r.capabilities = make(map[string]capability)
	r.nextSessionGeneration = snapshot.nextSessionGeneration
}

// RegisterSession records a host-owned session namespace. It is intentionally
// a runtime method rather than a renderer endpoint: callers must reach it via
// trusted lifecycle/bootstrap wiring before a capability can be issued.
// Re-registering an existing ID is idempotent and does not reactivate a
// suspended session; only ActivateSession may cross that lifecycle boundary.
func (r *Runtime) RegisterSession(sessionID string) error {
	if r == nil || sessionID == "" {
		return fmt.Errorf("session ID is required: %w", ErrUnknownSession)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sessions[sessionID]; exists {
		return nil
	}
	generation, err := r.allocateSessionGenerationLocked()
	if err != nil {
		return err
	}
	r.sessions[sessionID] = sessionState{active: true, generation: generation}
	return nil
}

func (r *Runtime) allocateSessionGenerationLocked() (uint64, error) {
	if r.nextSessionGeneration == ^uint64(0) {
		return 0, fmt.Errorf("agent session generation exhausted: %w", ErrInvalidCapability)
	}
	r.nextSessionGeneration++
	return r.nextSessionGeneration, nil
}

// UnregisterAllSessions invalidates the host-owned namespace on workspace
// changes. Existing capabilities are separately invalidated by generation.
func (r *Runtime) UnregisterAllSessions() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.sessions = make(map[string]sessionState)
	// A workspace incarnation change invalidates every outstanding capability,
	// including tokens whose session ID may later be re-used. Drop them now so
	// the revoked authority cannot accumulate in memory until TTL expiry.
	r.capabilities = make(map[string]capability)
	r.mu.Unlock()
}

// SuspendSession keeps the host-owned namespace reserved for a resumable
// execution while preventing any new capability from being issued or
// redeemed. Failure and pause transitions use this instead of unregistering so
// a trusted resume can reactivate the same opaque session ID.
func (r *Runtime) SuspendSession(sessionID string) error {
	if r == nil || sessionID == "" {
		return fmt.Errorf("session ID is required: %w", ErrUnknownSession)
	}
	r.mu.Lock()
	state, registered := r.sessions[sessionID]
	if registered && state.active {
		generation, err := r.allocateSessionGenerationLocked()
		if err != nil {
			r.mu.Unlock()
			return err
		}
		state.active = false
		state.generation = generation
		r.sessions[sessionID] = state
	}
	r.mu.Unlock()
	if !registered {
		return fmt.Errorf("session %q: %w", sessionID, ErrUnknownSession)
	}
	return nil
}

// ActivateSession re-enables a suspended namespace after the lifecycle store
// has accepted a checkpoint-backed resume. It never creates a new namespace.
func (r *Runtime) ActivateSession(sessionID string) error {
	if r == nil || sessionID == "" {
		return fmt.Errorf("session ID is required: %w", ErrUnknownSession)
	}
	r.mu.Lock()
	state, registered := r.sessions[sessionID]
	if registered {
		state.active = true
		r.sessions[sessionID] = state
	}
	r.mu.Unlock()
	if !registered {
		return fmt.Errorf("session %q: %w", sessionID, ErrUnknownSession)
	}
	return nil
}

// UnregisterSession revokes one host-owned namespace. Capability redemption
// rechecks registration, so closing a session also burns outstanding tokens.
func (r *Runtime) UnregisterSession(sessionID string) error {
	if r == nil || sessionID == "" {
		return fmt.Errorf("session ID is required: %w", ErrUnknownSession)
	}
	r.mu.Lock()
	_, registered := r.sessions[sessionID]
	delete(r.sessions, sessionID)
	r.mu.Unlock()
	if !registered {
		return fmt.Errorf("session %q: %w", sessionID, ErrUnknownSession)
	}
	return nil
}

func (r *Runtime) IsSessionRegistered(sessionID string) bool {
	if r == nil || sessionID == "" {
		return false
	}
	r.mu.Lock()
	_, registered := r.sessions[sessionID]
	r.mu.Unlock()
	return registered
}

func (r *Runtime) IsSessionActive(sessionID string) bool {
	if r == nil || sessionID == "" {
		return false
	}
	r.mu.Lock()
	state, registered := r.sessions[sessionID]
	r.mu.Unlock()
	return registered && state.active
}

func (r *Runtime) requireSessionGeneration(sessionID string) (uint64, error) {
	r.mu.Lock()
	state, registered := r.sessions[sessionID]
	r.mu.Unlock()
	if !registered {
		if !r.enforceSessions {
			return 0, nil
		}
		return 0, fmt.Errorf("session %q: %w", sessionID, ErrUnknownSession)
	}
	if !state.active {
		return 0, fmt.Errorf("session %q: %w", sessionID, ErrSessionSuspended)
	}
	return state.generation, nil
}

func (r *Runtime) sessionGenerationMatchesLocked(sessionID string, expected uint64) bool {
	state, registered := r.sessions[sessionID]
	if !registered {
		return expected == 0 && !r.enforceSessions
	}
	return state.active && state.generation == expected
}

// RegisterHandler installs the trusted adapter for an existing ExecuteKey.
// Mutation semantics must agree with every currently registered ToolDef using
// that key; RequestCapability repeats the check for dynamically added tools.
func (r *Runtime) RegisterHandler(executeKey string, handler Handler) error {
	if executeKey == "" || handler == nil {
		return fmt.Errorf("execute key and handler are required: %w", ErrHandlerUnavailable)
	}
	for _, def := range r.registry.Snapshot().Tools {
		if def.ExecuteKey == executeKey {
			if err := validateHandlerMutationContract(def, handler); err != nil {
				return err
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[executeKey]; exists {
		return fmt.Errorf("handler %q already registered: %w", executeKey, ErrHandlerUnavailable)
	}
	r.handlers[executeKey] = handler
	return nil
}

func (r *Runtime) UnregisterHandler(executeKey string) error {
	if executeKey == "" {
		return fmt.Errorf("execute key is required: %w", ErrHandlerUnavailable)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.handlers, executeKey)
	return nil
}

func (r *Runtime) handlerFor(def ToolDef) (Handler, error) {
	r.mu.Lock()
	handler := r.handlers[def.ExecuteKey]
	r.mu.Unlock()
	if handler == nil {
		return nil, fmt.Errorf("tool %q execute key %q: %w", def.ID, def.ExecuteKey, ErrHandlerUnavailable)
	}
	if err := validateHandlerMutationContract(def, handler); err != nil {
		return nil, err
	}
	return handler, nil
}

func validateHandlerMutationContract(def ToolDef, handler Handler) error {
	if handler == nil {
		return fmt.Errorf("tool %q has no handler: %w", def.ID, ErrHandlerUnavailable)
	}
	declared := handler.MutationMode()
	if declared != def.Mutation {
		return fmt.Errorf("tool %q requires %q, handler declares %q: %w", def.ID, def.Mutation, declared, ErrMutationContract)
	}
	if def.Mutation == MutationWorkspaceTransaction {
		if _, ok := handler.(WorkspaceTransactionHandler); !ok {
			return fmt.Errorf("tool %q requires a workspace transaction handler: %w", def.ID, ErrMutationContract)
		}
	}
	if def.Mutation == MutationExternal {
		if _, ok := handler.(ExternalMutationTransactionHandler); !ok {
			return fmt.Errorf("tool %q requires an external mutation transaction handler: %w", def.ID, ErrExternalMutationContract)
		}
	}
	return nil
}

func beginExternalMutation(ctx context.Context, handler Handler, invocation Invocation, prepared PreparedExecution) (ExternalMutationReceipt, error) {
	externalHandler, ok := handler.(ExternalMutationTransactionHandler)
	if !ok {
		return ExternalMutationReceipt{}, fmt.Errorf("tool %q requires an external mutation transaction handler: %w", invocation.Tool.ID, ErrExternalMutationContract)
	}
	receipt, err := externalHandler.BeginExternalMutation(ctx, invocation, prepared)
	if err != nil {
		return ExternalMutationReceipt{}, fmt.Errorf("begin external mutation for tool %q: %w", invocation.Tool.ID, err)
	}
	if receipt.ID == "" || len(receipt.ID) > 256 {
		return ExternalMutationReceipt{}, fmt.Errorf("tool %q returned an invalid external mutation receipt ID: %w", invocation.Tool.ID, ErrExternalMutationContract)
	}
	if len(receipt.Metadata) > 32 {
		return ExternalMutationReceipt{}, fmt.Errorf("tool %q returned too much external receipt metadata: %w", invocation.Tool.ID, ErrExternalMutationContract)
	}
	for key, value := range receipt.Metadata {
		if key == "" || len(key) > 128 || len(value) > 1024 {
			return ExternalMutationReceipt{}, fmt.Errorf("tool %q returned invalid external receipt metadata: %w", invocation.Tool.ID, ErrExternalMutationContract)
		}
	}
	receipt.Metadata = cloneStringMap(receipt.Metadata)
	return receipt, nil
}

func executeHandler(ctx context.Context, handler Handler, invocation Invocation, prepared PreparedExecution) (ExecutionOutput, ExternalMutationReceipt, error) {
	return executeHandlerWithExternalReceipt(ctx, handler, invocation, prepared, ExternalMutationReceipt{})
}

func executeHandlerWithExternalReceipt(ctx context.Context, handler Handler, invocation Invocation, prepared PreparedExecution, receipt ExternalMutationReceipt) (ExecutionOutput, ExternalMutationReceipt, error) {
	if invocation.Tool.Mutation == MutationWorkspaceTransaction {
		transactionHandler, ok := handler.(WorkspaceTransactionHandler)
		if !ok {
			return ExecutionOutput{}, ExternalMutationReceipt{}, fmt.Errorf("tool %q requires a workspace transaction handler: %w", invocation.Tool.ID, ErrMutationContract)
		}
		output, err := transactionHandler.ExecuteWorkspaceTransaction(ctx, invocation, prepared)
		return output, ExternalMutationReceipt{}, err
	}
	if invocation.Tool.Mutation == MutationExternal {
		externalHandler, ok := handler.(ExternalMutationTransactionHandler)
		if !ok {
			return ExecutionOutput{}, ExternalMutationReceipt{}, fmt.Errorf("tool %q requires an external mutation transaction handler: %w", invocation.Tool.ID, ErrExternalMutationContract)
		}
		if receipt.ID == "" {
			return ExecutionOutput{}, ExternalMutationReceipt{}, fmt.Errorf("tool %q has no preallocated external mutation receipt: %w", invocation.Tool.ID, ErrExternalMutationContract)
		}
		output, err := externalHandler.ExecuteExternalTransactionWithReceipt(ctx, invocation, prepared, receipt)
		return output, receipt, err
	}
	output, err := handler.Execute(ctx, invocation, prepared)
	return output, ExternalMutationReceipt{}, err
}

func compensateExternalMutation(ctx context.Context, handler Handler, invocation Invocation, prepared PreparedExecution, receipt ExternalMutationReceipt) error {
	externalHandler, ok := handler.(ExternalMutationHandler)
	if !ok {
		return fmt.Errorf("tool %q has no external compensation handler: %w", invocation.Tool.ID, ErrExternalMutationContract)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), externalCompensationTimeout)
	defer cancel()
	compensationErr := externalHandler.CompensateExternalTransaction(cleanupCtx, invocation, prepared, receipt)
	if !receipt.Reversible {
		compensationErr = errors.Join(ErrExternalMutationIrreversible, compensationErr)
	}
	if compensationErr == nil {
		return nil
	}
	return fmt.Errorf("compensate external mutation %q: %w", receipt.ID, errors.Join(ErrExternalCompensation, compensationErr))
}

func compensationStatus(receipt ExternalMutationReceipt, err error) string {
	if err == nil {
		return ExternalCompensationSucceeded
	}
	if !receipt.Reversible || errors.Is(err, ErrExternalMutationIrreversible) {
		return ExternalCompensationIrreversible
	}
	return ExternalCompensationFailed
}

// RequestCapability performs the only prepare -> approval -> budget -> token
// issuance path. Declined or structurally invalid requests do not spend budget;
// an issued but abandoned capability does.
func (r *Runtime) RequestCapability(ctx context.Context, request CapabilityRequest) (CapabilityGrant, error) {
	if request.SessionID == "" {
		return CapabilityGrant{}, fmt.Errorf("session ID is required: %w", ErrInvalidArguments)
	}
	if err := r.ensureUsagePipeline(); err != nil {
		return CapabilityGrant{}, err
	}
	sessionGeneration, err := r.requireSessionGeneration(request.SessionID)
	if err != nil {
		return CapabilityGrant{}, err
	}
	invocation, err := r.registry.Resolve(request.CatalogRevision, request.ToolID, request.Arguments)
	if err != nil {
		return CapabilityGrant{}, err
	}
	invocation.SessionID = request.SessionID
	if err := r.authorize(ctx, invocation); err != nil {
		return CapabilityGrant{}, err
	}
	handler, err := r.handlerFor(invocation.Tool)
	if err != nil {
		return CapabilityGrant{}, err
	}
	workspaceGeneration := r.workspaceGeneration()
	if workspaceGeneration == 0 {
		return CapabilityGrant{}, fmt.Errorf("workspace generation is not available: %w", ErrInvalidCapability)
	}
	prepared, err := handler.Prepare(ctx, invocation)
	if err != nil {
		return CapabilityGrant{}, fmt.Errorf("prepare tool %q: %w", invocation.Tool.ID, err)
	}
	if currentGeneration := r.workspaceGeneration(); currentGeneration != workspaceGeneration {
		return CapabilityGrant{}, fmt.Errorf("workspace changed during tool preparation: %w", ErrInvalidCapability)
	}
	prepared, preparedHash, err := canonicalPrepared(prepared)
	if err != nil {
		return CapabilityGrant{}, err
	}

	// The budget epoch shown to the approver is advisory only. Reserve below is
	// authoritative and may observe a concurrent explicit epoch change.
	approval := ApprovalRequest{
		SessionID: request.SessionID, Invocation: invocation,
		Summary: prepared.Summary, BudgetEpoch: r.budget.CurrentEpoch(),
		WorkspaceGeneration: workspaceGeneration, Metadata: cloneStringMap(prepared.Metadata),
	}
	approved, err := r.approver.Approve(ctx, approval)
	if err != nil {
		return CapabilityGrant{}, fmt.Errorf("approve tool %q: %w", invocation.Tool.ID, err)
	}
	if !approved {
		return CapabilityGrant{}, fmt.Errorf("tool %q: %w", invocation.Tool.ID, ErrApprovalDenied)
	}

	// Re-resolve after a potentially long native dialog. Dynamic catalogs are
	// allowed to change while the user decides, but stale approval is not.
	invocation, err = r.registry.Resolve(request.CatalogRevision, request.ToolID, invocation.Arguments)
	if err != nil {
		return CapabilityGrant{}, err
	}
	invocation.SessionID = request.SessionID
	if err := r.authorize(ctx, invocation); err != nil {
		return CapabilityGrant{}, err
	}
	if currentGeneration := r.workspaceGeneration(); currentGeneration != workspaceGeneration {
		return CapabilityGrant{}, fmt.Errorf("workspace changed during approval: %w", ErrInvalidCapability)
	}
	currentSessionGeneration, err := r.requireSessionGeneration(request.SessionID)
	if err != nil || currentSessionGeneration != sessionGeneration {
		return CapabilityGrant{}, fmt.Errorf("agent session changed during approval: %w", ErrInvalidCapability)
	}
	budgetEpoch, err := r.budget.Reserve()
	if err != nil {
		if errors.Is(err, ErrBudgetExhausted) {
			return CapabilityGrant{}, err
		}
		return CapabilityGrant{}, fmt.Errorf("reserve tool budget: %w", err)
	}
	// A reservation is provisional until the capability-issued audit record is
	// accepted and the grant is returned. The budget must not charge failures
	// before that point, but an already issued (and later abandoned) capability
	// remains charged by design.
	releaseReservation := func(cause error) error {
		if releaseErr := r.budget.ReleaseReservation(budgetEpoch); releaseErr != nil {
			return errors.Join(cause, fmt.Errorf("release unissued tool budget reservation: %w", releaseErr))
		}
		return cause
	}
	token, err := randomToken(r.random)
	if err != nil {
		return CapabilityGrant{}, releaseReservation(fmt.Errorf("create capability token: %w", err))
	}
	expiresAt := r.now().Add(r.capabilityTTL)
	r.mu.Lock()
	if !r.sessionGenerationMatchesLocked(request.SessionID, sessionGeneration) {
		r.mu.Unlock()
		return CapabilityGrant{}, releaseReservation(fmt.Errorf("agent session changed before capability issuance: %w", ErrInvalidCapability))
	}
	r.capabilities[token] = capability{
		invocation: invocation, prepared: prepared, preparedHash: preparedHash,
		sessionID: request.SessionID, sessionGeneration: sessionGeneration, budgetEpoch: budgetEpoch,
		workspaceGeneration: workspaceGeneration, expiresAt: expiresAt,
	}
	r.mu.Unlock()
	if err := r.recordAudit(AuditRecord{
		Timestamp: r.now(), Stage: AuditCapabilityIssued, SessionID: request.SessionID,
		ToolID: invocation.Tool.ID, ArgumentsHash: invocation.ArgumentsHash,
		PreparedHash: preparedHash, CatalogRevision: invocation.CatalogRevision,
		BudgetEpoch: budgetEpoch, WorkspaceGeneration: workspaceGeneration, Success: true,
	}); err != nil {
		r.mu.Lock()
		delete(r.capabilities, token)
		r.mu.Unlock()
		return CapabilityGrant{}, releaseReservation(err)
	}
	return CapabilityGrant{
		Token: token, ToolID: invocation.Tool.ID, ArgumentsHash: invocation.ArgumentsHash,
		CatalogRevision: invocation.CatalogRevision, BudgetEpoch: budgetEpoch,
		WorkspaceGeneration: workspaceGeneration, ExpiresAt: expiresAt,
	}, nil
}

// Execute consumes the capability before validating its bindings. A mismatch
// is terminal, preventing an attacker from probing a token and retrying it with
// corrected arguments.
func (r *Runtime) Execute(ctx context.Context, request CapabilityExecution) (ExecutionResult, error) {
	if request.Token == "" {
		return ExecutionResult{}, fmt.Errorf("capability token is required: %w", ErrInvalidCapability)
	}
	r.mu.Lock()
	capability, exists := r.capabilities[request.Token]
	if exists {
		delete(r.capabilities, request.Token)
	}
	r.mu.Unlock()
	if !exists {
		return ExecutionResult{}, fmt.Errorf("unknown or already consumed token: %w", ErrInvalidCapability)
	}
	invalid := func(reason string) (ExecutionResult, error) {
		err := fmt.Errorf("%s: %w", reason, ErrInvalidCapability)
		auditErr := r.recordAudit(AuditRecord{
			Timestamp: r.now(), Stage: AuditExecutionFailed, SessionID: capability.sessionID,
			ToolID: capability.invocation.Tool.ID, ArgumentsHash: capability.invocation.ArgumentsHash,
			PreparedHash: capability.preparedHash, CatalogRevision: capability.invocation.CatalogRevision,
			BudgetEpoch: capability.budgetEpoch, WorkspaceGeneration: capability.workspaceGeneration,
			Success: false, Error: reason,
		})
		return ExecutionResult{}, errors.Join(err, auditErr)
	}
	if r.now().After(capability.expiresAt) {
		return invalid("capability expired")
	}
	if request.SessionID != capability.sessionID || request.ToolID != capability.invocation.Tool.ID || request.CatalogRevision != capability.invocation.CatalogRevision {
		return invalid("capability identity does not match request")
	}
	currentSessionGeneration, err := r.requireSessionGeneration(request.SessionID)
	if err != nil || currentSessionGeneration != capability.sessionGeneration {
		return invalid("agent session is no longer active or belongs to a different generation")
	}
	if r.budget.CurrentEpoch() != capability.budgetEpoch {
		return invalid("capability belongs to a previous budget epoch")
	}
	if r.workspaceGeneration() != capability.workspaceGeneration {
		return invalid("capability belongs to a previous workspace generation")
	}
	invocation, err := r.registry.Resolve(request.CatalogRevision, request.ToolID, request.Arguments)
	if err != nil {
		return invalid("catalog or arguments changed after approval")
	}
	invocation.SessionID = request.SessionID
	if invocation.ArgumentsHash != capability.invocation.ArgumentsHash || invocation.Tool.ExecuteKey != capability.invocation.Tool.ExecuteKey || invocation.Tool.Mutation != capability.invocation.Tool.Mutation {
		return invalid("tool definition or arguments changed after approval")
	}
	if err := r.authorize(ctx, invocation); err != nil {
		return invalid("tool is no longer authorized for this session")
	}
	handler, err := r.handlerFor(invocation.Tool)
	if err != nil {
		return invalid("approved handler is no longer available")
	}

	startedAt := r.now()
	usage := UsageRecord{
		UnitID: newUnitID(startedAt, request.Token), SessionID: request.SessionID,
		UnitKind: UsageUnitTool, Operation: invocation.Tool.ID,
		Cost: 0, CostBasis: CostNotApplicable, Estimated: false,
		StartedAt: startedAt, CompletedAt: startedAt, Success: false,
	}
	externalReceipt := ExternalMutationReceipt{}
	if invocation.Tool.Mutation == MutationExternal {
		externalReceipt, err = beginExternalMutation(ctx, handler, invocation, capability.prepared)
		if err != nil {
			usage.CompletedAt = r.now()
			usage.Error = err.Error()
			auditErr := r.recordAudit(AuditRecord{
				Timestamp: usage.CompletedAt, Stage: AuditExecutionFailed, SessionID: request.SessionID,
				ToolID: invocation.Tool.ID, ArgumentsHash: invocation.ArgumentsHash,
				PreparedHash: capability.preparedHash, CatalogRevision: invocation.CatalogRevision,
				BudgetEpoch: capability.budgetEpoch, WorkspaceGeneration: capability.workspaceGeneration,
				Success: false, Error: err.Error(),
			})
			return ExecutionResult{Usage: usage}, errors.Join(err, auditErr)
		}
		usage.ExternalReceiptID = externalReceipt.ID
		usage.ExternalReceiptReversible = externalReceipt.Reversible
		usage.ExternalCompensation = ExternalCompensationPending
	}
	meter, transaction, receipt, meterErr := r.beginExecutionUsage(usage)
	if meterErr != nil {
		// The capability has already been consumed. No handler side effect is
		// allowed when the durable receipt cannot be written.
		auditErr := r.recordAudit(AuditRecord{
			Timestamp: r.now(), Stage: AuditExecutionFailed, SessionID: request.SessionID,
			ToolID: invocation.Tool.ID, ArgumentsHash: invocation.ArgumentsHash,
			PreparedHash: capability.preparedHash, CatalogRevision: invocation.CatalogRevision,
			BudgetEpoch: capability.budgetEpoch, WorkspaceGeneration: capability.workspaceGeneration,
			ExternalReceiptID: externalReceipt.ID, ExternalReceiptReversible: externalReceipt.Reversible,
			Success: false, Error: meterErr.Error(),
		})
		return ExecutionResult{Usage: usage}, errors.Join(meterErr, auditErr)
	}
	if auditErr := r.recordAudit(AuditRecord{
		Timestamp: r.now(), Stage: AuditExecutionStarted, SessionID: request.SessionID,
		ToolID: invocation.Tool.ID, ArgumentsHash: invocation.ArgumentsHash,
		PreparedHash: capability.preparedHash, CatalogRevision: invocation.CatalogRevision,
		BudgetEpoch: capability.budgetEpoch, WorkspaceGeneration: capability.workspaceGeneration,
		ExternalReceiptID: externalReceipt.ID, ExternalReceiptReversible: externalReceipt.Reversible,
		Success: true,
	}); auditErr != nil {
		usage.CompletedAt = r.now()
		usage.Success = false
		usage.Error = ErrAuditUnavailable.Error()
		if externalReceipt.ID != "" {
			usage.ExternalCompensation = ExternalCompensationNotNeeded
		}
		if err := completeExecutionUsage(meter, transaction, receipt, usage); err != nil {
			auditErr = errors.Join(auditErr, fmt.Errorf("complete usage after audit admission failure: %w", err))
		}
		return ExecutionResult{Usage: usage}, auditErr
	}
	output, externalReceipt, executeErr := executeHandlerWithExternalReceipt(ctx, handler, invocation, capability.prepared, externalReceipt)
	completedAt := r.now()
	usage.CompletedAt = completedAt
	if output.Usage != nil {
		usage.ProviderID = output.Usage.ProviderID
		usage.Model = output.Usage.Model
		usage.TokensIn = output.Usage.TokensIn
		usage.TokensOut = output.Usage.TokensOut
		usage.Cost = output.Usage.Cost
		usage.Currency = output.Usage.Currency
		usage.CostBasis = output.Usage.CostBasis
		usage.Estimated = output.Usage.Estimated
	}
	usage.Success = executeErr == nil
	if externalReceipt.ID != "" && executeErr == nil {
		usage.ExternalCompensation = ExternalCompensationNotNeeded
	}
	if executeErr != nil {
		usage.Error = executeErr.Error()
	}
	compensationAttempted := false
	if executeErr != nil && externalReceipt.ID != "" {
		compensationAttempted = true
		if compensationErr := compensateExternalMutation(ctx, handler, invocation, capability.prepared, externalReceipt); compensationErr != nil {
			usage.ExternalCompensation = compensationStatus(externalReceipt, compensationErr)
			executeErr = errors.Join(executeErr, compensationErr)
			usage.Error = executeErr.Error()
		} else {
			usage.ExternalCompensation = ExternalCompensationSucceeded
		}
	}
	if err := completeExecutionUsage(meter, transaction, receipt, usage); err != nil {
		meterErr = fmt.Errorf("complete execution usage: %w", err)
	} else {
		meterErr = nil
	}
	if meterErr != nil {
		if externalReceipt.ID != "" && !compensationAttempted {
			compensationAttempted = true
			if compensationErr := compensateExternalMutation(ctx, handler, invocation, capability.prepared, externalReceipt); compensationErr != nil {
				usage.ExternalCompensation = compensationStatus(externalReceipt, compensationErr)
				meterErr = errors.Join(meterErr, compensationErr)
			} else {
				usage.ExternalCompensation = ExternalCompensationSucceeded
			}
		}
		// The initial pending receipt remains durable even if completion fails.
		// Report the unit as non-successful so callers cannot mistake an
		// unmetered side effect for a completed execution.
		usage.Success = false
		if usage.Error == "" {
			usage.Error = meterErr.Error()
		} else {
			usage.Error = errors.Join(errors.New(usage.Error), meterErr).Error()
		}
		if executeErr == nil {
			executeErr = meterErr
		} else {
			executeErr = errors.Join(executeErr, meterErr)
		}
		if transaction != nil && externalReceipt.ID != "" {
			// Compensation changes the durable disposition of an external
			// receipt. Retry once with the same UnitID so a transient terminal
			// write failure does not strand a successfully compensated receipt
			// as pending. The original completion failure remains an execution
			// error even when this idempotent retry succeeds.
			if err := transaction.CompleteUsage(receipt, usage); err != nil {
				retryErr := fmt.Errorf("retry external usage completion: %w", err)
				executeErr = errors.Join(executeErr, retryErr)
				usage.Error = executeErr.Error()
			}
		}
	}
	stage := AuditExecutionCompleted
	if executeErr != nil {
		stage = AuditExecutionFailed
	}
	audit := AuditRecord{
		Timestamp: completedAt, Stage: stage, SessionID: request.SessionID,
		ToolID: invocation.Tool.ID, ArgumentsHash: invocation.ArgumentsHash,
		PreparedHash: capability.preparedHash, CatalogRevision: invocation.CatalogRevision,
		BudgetEpoch: capability.budgetEpoch, WorkspaceGeneration: capability.workspaceGeneration,
		ExternalReceiptID: externalReceipt.ID, ExternalReceiptReversible: externalReceipt.Reversible,
		ExternalCompensation: usage.ExternalCompensation,
		Success:              executeErr == nil,
	}
	if executeErr != nil {
		audit.Error = executeErr.Error()
	}
	auditErr := r.recordAudit(audit)
	result := ExecutionResult{ExecutionOutput: output, Usage: usage}
	if auditErr != nil {
		result.ExecutionOutput = ExecutionOutput{}
		executeErr = errors.Join(executeErr, auditErr)
	}
	if executeErr != nil {
		return result, executeErr
	}
	return result, nil
}

func (r *Runtime) authorize(ctx context.Context, invocation Invocation) error {
	if r.policy == nil {
		return nil
	}
	if err := r.policy.Authorize(ctx, invocation); err != nil {
		return fmt.Errorf("authorize tool %q for session %q: %w", invocation.Tool.ID, invocation.SessionID, err)
	}
	return nil
}

func (r *Runtime) recordAudit(record AuditRecord) error {
	if r.audit == nil {
		return ErrAuditUnavailable
	}
	if err := r.audit.RecordAudit(record); err != nil {
		return fmt.Errorf("persist agent audit: %w", errors.Join(ErrAuditUnavailable, err))
	}
	return nil
}

// RecordUsage is the single metering entry point for AI and orchestration
// adapters. Tool executions call it internally through the same UsageRecord
// contract. Validation keeps reported billing and local estimates distinct.
func (r *Runtime) RecordUsage(record UsageRecord) error {
	if err := validateUsageRecord(record); err != nil {
		return err
	}
	meter := r.usageMeter()
	if meter == nil {
		if r.meterRequired() {
			return fmt.Errorf("usage sink is not configured: %w", ErrMeterUnavailable)
		}
		return nil
	}
	if err := meter.RecordUsage(record); err != nil {
		return fmt.Errorf("persist usage record: %w", err)
	}
	return nil
}

func (r *Runtime) usageMeter() UsageSink {
	r.mu.Lock()
	meter := r.meter
	r.mu.Unlock()
	return meter
}

func (r *Runtime) meterRequired() bool {
	r.mu.Lock()
	required := r.requireMeter
	r.mu.Unlock()
	return required
}

func (r *Runtime) ensureUsagePipeline() error {
	meter := r.usageMeter()
	r.mu.Lock()
	requireMeter, requireTxn := r.requireMeter, r.requireUsageTxn
	r.mu.Unlock()
	if meter == nil {
		if requireMeter {
			return fmt.Errorf("usage sink is not configured: %w", ErrMeterUnavailable)
		}
		return nil
	}
	if requireTxn {
		if _, ok := meter.(UsageTransactionSink); !ok {
			return fmt.Errorf("usage sink does not support durable execution receipts: %w", ErrMeterContract)
		}
	}
	return nil
}

// beginExecutionUsage writes a durable receipt before invoking a handler.
// Legacy sinks remain supported when the production transaction requirement is
// disabled; the desktop runtime enables that requirement during wiring.
func (r *Runtime) beginExecutionUsage(record UsageRecord) (UsageSink, UsageTransactionSink, UsageReceipt, error) {
	meter := r.usageMeter()
	if meter == nil {
		if r.meterRequired() {
			return nil, nil, UsageReceipt{}, fmt.Errorf("usage sink is not configured: %w", ErrMeterUnavailable)
		}
		return nil, nil, UsageReceipt{}, nil
	}
	transaction, ok := meter.(UsageTransactionSink)
	r.mu.Lock()
	requireTxn := r.requireUsageTxn
	r.mu.Unlock()
	if requireTxn && !ok {
		return nil, nil, UsageReceipt{}, fmt.Errorf("usage sink does not support durable execution receipts: %w", ErrMeterContract)
	}
	if !ok {
		return meter, nil, UsageReceipt{}, nil
	}
	record.Pending = true
	record.CompletedAt = record.StartedAt
	receipt, err := transaction.BeginUsage(record)
	if err != nil {
		return nil, nil, UsageReceipt{}, fmt.Errorf("begin execution usage: %w", err)
	}
	if receipt.UnitID == "" {
		return nil, nil, UsageReceipt{}, fmt.Errorf("usage sink returned an empty receipt: %w", ErrMeterContract)
	}
	if receipt.UnitID != record.UnitID {
		return nil, nil, UsageReceipt{}, fmt.Errorf("usage sink changed receipt identity: %w", ErrMeterContract)
	}
	return meter, transaction, receipt, nil
}

func completeExecutionUsage(meter UsageSink, transaction UsageTransactionSink, receipt UsageReceipt, record UsageRecord) error {
	if transaction != nil {
		return transaction.CompleteUsage(receipt, record)
	}
	if meter != nil {
		return meter.RecordUsage(record)
	}
	return nil
}

// SetUsageSink installs the host-owned meter during trusted bootstrap wiring.
// The lock also makes late test wiring race-free with in-flight executions.
func (r *Runtime) SetUsageSink(meter UsageSink) {
	r.mu.Lock()
	r.meter = meter
	r.mu.Unlock()
}

// SetUsageRequirements is trusted bootstrap wiring. Renderer code has no
// access to this method; production enables both fail-closed meter presence
// and the durable two-phase receipt contract after installing AgentLifecycle.
func (r *Runtime) SetUsageRequirements(requireMeter, requireTransaction bool) {
	r.mu.Lock()
	r.requireMeter = requireMeter
	r.requireUsageTxn = requireTransaction
	r.mu.Unlock()
}

func validateUsageRecord(record UsageRecord) error {
	if record.UnitID == "" || record.SessionID == "" || record.Operation == "" {
		return fmt.Errorf("unit ID, session ID, and operation are required: %w", ErrInvalidUsageRecord)
	}
	switch record.UnitKind {
	case UsageUnitTool, UsageUnitAI, UsageUnitChat, UsageUnitPlan, UsageUnitGoal, UsageUnitWorkflow:
	default:
		return fmt.Errorf("unknown usage unit kind %q: %w", record.UnitKind, ErrInvalidUsageRecord)
	}
	if record.TokensIn < 0 || record.TokensOut < 0 || record.Cost < 0 {
		return fmt.Errorf("tokens and cost must not be negative: %w", ErrInvalidUsageRecord)
	}
	if record.StartedAt.IsZero() || record.CompletedAt.IsZero() || record.CompletedAt.Before(record.StartedAt) {
		return fmt.Errorf("usage timestamps are missing or reversed: %w", ErrInvalidUsageRecord)
	}
	switch record.CostBasis {
	case CostProviderReported:
		if record.Estimated {
			return fmt.Errorf("provider-reported cost cannot be marked estimated: %w", ErrInvalidUsageRecord)
		}
	case CostEstimated:
		if !record.Estimated {
			return fmt.Errorf("estimated cost must be marked estimated: %w", ErrInvalidUsageRecord)
		}
	case CostNotApplicable:
		if record.Estimated || record.Cost != 0 || record.Currency != "" {
			return fmt.Errorf("not-applicable cost must be zero and non-estimated: %w", ErrInvalidUsageRecord)
		}
	default:
		return fmt.Errorf("unknown cost basis %q: %w", record.CostBasis, ErrInvalidUsageRecord)
	}
	if record.Cost > 0 && record.Currency == "" {
		return fmt.Errorf("non-zero cost requires currency: %w", ErrInvalidUsageRecord)
	}
	if record.ExternalReceiptID == "" {
		if record.ExternalReceiptReversible || record.ExternalCompensation != "" {
			return fmt.Errorf("external mutation metadata requires a receipt ID: %w", ErrInvalidUsageRecord)
		}
		return nil
	}
	if record.UnitKind != UsageUnitTool || len(record.ExternalReceiptID) > 256 {
		return fmt.Errorf("invalid external mutation usage receipt: %w", ErrInvalidUsageRecord)
	}
	switch record.ExternalCompensation {
	case ExternalCompensationPending, ExternalCompensationNotNeeded, ExternalCompensationSucceeded, ExternalCompensationFailed, ExternalCompensationIrreversible, ExternalCompensationManualUnknown:
	default:
		return fmt.Errorf("invalid external compensation status %q: %w", record.ExternalCompensation, ErrInvalidUsageRecord)
	}
	return nil
}

func canonicalPrepared(prepared PreparedExecution) (PreparedExecution, string, error) {
	if len(prepared.Opaque) == 0 {
		prepared.Opaque = json.RawMessage(`{}`)
	}
	canonical, err := canonicalArguments(prepared.Opaque)
	if err != nil {
		return PreparedExecution{}, "", fmt.Errorf("prepared execution state must be a JSON object: %w", err)
	}
	prepared.Opaque = canonical
	prepared.Metadata = cloneStringMap(prepared.Metadata)
	sum := sha256.Sum256(canonical)
	return prepared, hex.EncodeToString(sum[:]), nil
}

func randomToken(source io.Reader) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(source, raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func newUnitID(startedAt time.Time, token string) string {
	sum := sha256.Sum256([]byte(startedAt.UTC().Format(time.RFC3339Nano) + "\x00" + token))
	return hex.EncodeToString(sum[:16])
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
