package services

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

// AgentLifecycle is the services-layer facade for the headless lifecycle,
// context, and metering primitives. All production execution modes receive
// the same instance during bootstrap.
type AgentLifecycle struct {
	sessions             *agentcore.SessionStore
	context              *agentcore.ContextManager
	estimator            agentcore.TokenEstimator
	runtime              *agentcore.Runtime
	meter                agentcore.UsageSink
	permission           *AIPermissionService
	settings             *SettingsService
	now                  func() time.Time
	workspaceGeneration  func() uint64
	workspaceLease       func() (workspaceLease, error)
	workspaceGuard       func(uint64, func() error) error
	workspaceIdentityKey []byte
	incarnation          string
	sequence             atomic.Uint64
	workspaceAuthorityMu *sync.RWMutex
	transitionMu         sync.Mutex
	// runtimeIDs separates renderer-visible domain IDs from the authority
	// namespace used by agentcore. Plan and Goal IDs are supplied through CRUD
	// bindings and are not trusted entropy, so they must never become runtime
	// capability session IDs directly.
	ownerMu    sync.RWMutex
	runtimeIDs map[string]string // logical lifecycle ID -> opaque runtime ID
}

// agentLifecycleWorkspaceSnapshot is an in-process transaction image. It is
// kept private to the services package so renderer callers cannot ask the
// lifecycle store to resurrect arbitrary historical rows.
type agentLifecycleWorkspaceSnapshot struct {
	sessions   agentcore.SessionStoreSnapshot
	runtime    agentcore.RuntimeSnapshot
	runtimeIDs map[string]string
}

// prepareWorkspaceReset captures authority and acquires the lifecycle
// transition lock without publishing a terminal state. AgentService uses this
// split phase to publish the candidate root first, preserving the recovery
// guard's generation ordering without allowing a concurrent lifecycle owner to
// slip into the candidate workspace.
func (l *AgentLifecycle) prepareWorkspaceReset() (*agentLifecycleWorkspaceReset, error) {
	if l == nil {
		return nil, nil
	}
	l.transitionMu.Lock()
	snapshot, err := l.captureWorkspaceSnapshotLocked()
	if err != nil {
		if lifecyclePersistenceIsIndeterminate(err) || errors.Is(err, agentcore.ErrSessionPersistencePoisoned) {
			l.revokeAllRuntimeAuthority()
		}
		l.transitionMu.Unlock()
		return nil, fmt.Errorf("capture workspace lifecycle snapshot: %w", err)
	}
	return &agentLifecycleWorkspaceReset{lifecycle: l, snapshot: snapshot}, nil
}

// agentLifecycleWorkspaceReset owns transitionMu from begin through either
// commit or rollback. This blocks lifecycle operations from creating authority
// in a candidate workspace while ProjectService is deciding whether to publish
// the switch.
type agentLifecycleWorkspaceReset struct {
	mu        sync.Mutex
	lifecycle *AgentLifecycle
	snapshot  agentLifecycleWorkspaceSnapshot
	published bool
	done      bool
	result    error
}

type agentRecoveryRow struct {
	ID                  string                  `json:"id"`
	Handle              string                  `json:"handle"`
	Kind                agentcore.SessionKind   `json:"kind"`
	Status              agentcore.SessionStatus `json:"status"`
	OwnerDomain         string                  `json:"ownerDomain"`
	WorkspaceGeneration uint64                  `json:"workspaceGeneration"`
	StartedAt           time.Time               `json:"startedAt"`
	UpdatedAt           time.Time               `json:"updatedAt"`
	CheckpointCount     int                     `json:"checkpointCount"`
}

const agentRecoveryHandlePrefix = "recovery-v1:"

var (
	// ErrAgentRecoveryPersistence means a disposition was not published and
	// remains safe to retry after the underlying storage problem is fixed.
	ErrAgentRecoveryPersistence = errors.New("agent recovery disposition was not persisted")
	// ErrAgentRecoveryPersistenceIndeterminate means publication may have
	// happened, so this process is poisoned and a fresh reload must confirm it.
	ErrAgentRecoveryPersistenceIndeterminate = errors.New("agent recovery disposition requires restart verification")
)

// AgentRecoveryEntry is a content-free view of one durable lifecycle row that
// requires trusted disposition after restart. Handle is a keyed, stable opaque
// reference; logical IDs, owner claims, workspace data, streams and checkpoint
// payloads are intentionally excluded.
type AgentRecoveryEntry struct {
	Handle          string    `json:"handle"`
	Kind            string    `json:"kind"`
	Status          string    `json:"status"`
	StartedAt       time.Time `json:"startedAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	CheckpointCount int       `json:"checkpointCount"`
}

// AgentRecoveryDispositionRequest is the narrow input accepted by the
// backend-only recovery dispatcher. Resume is intentionally unsupported until
// a domain owner can prove a fresh caller and rebuild its runtime state.
type AgentRecoveryDispositionRequest struct {
	Handle      string `json:"handle"`
	Disposition string `json:"disposition"`
}

// AgentRecoveryDispositionResult excludes stream, checkpoint, owner and
// workspace data so a headless operator cannot accidentally log source or
// prompt content while recording a manual disposition.
type AgentRecoveryDispositionResult struct {
	Handle      string    `json:"handle"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	Disposition string    `json:"disposition"`
	CompletedAt time.Time `json:"completedAt"`
}

// AgentRecoveryDispatcher is held by trusted bootstrap/headless code. The
// AgentLifecycle object is deliberately absent from the Wails service list, so
// renderer callers cannot enumerate or dispose durable lifecycle rows.
type AgentRecoveryDispatcher interface {
	PendingRecoveryDispositions() ([]AgentRecoveryEntry, error)
	DispatchRecoveryDisposition(AgentRecoveryDispositionRequest) (AgentRecoveryDispositionResult, error)
}

var _ AgentRecoveryDispatcher = (*AgentLifecycle)(nil)

func (t *agentLifecycleWorkspaceReset) publish() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lifecycle == nil || t.done {
		return t.result
	}
	if t.published {
		return nil
	}
	if t.lifecycle.sessions != nil {
		if _, err := t.lifecycle.sessions.CloseAllDurable(); err != nil {
			if lifecyclePersistenceIsIndeterminate(err) || errors.Is(err, agentcore.ErrSessionPersistencePoisoned) {
				t.lifecycle.revokeAllRuntimeAuthority()
			}
			t.result = fmt.Errorf("persist workspace lifecycle reset: %w", err)
			return t.result
		}
	}
	t.lifecycle.revokeAllRuntimeAuthority()
	t.published = true
	return nil
}

func (t *agentLifecycleWorkspaceReset) cancel() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lifecycle == nil || t.done {
		return
	}
	t.done = true
	t.lifecycle.transitionMu.Unlock()
}

func (l *AgentLifecycle) beginWorkspaceReset() (*agentLifecycleWorkspaceReset, error) {
	transition, err := l.prepareWorkspaceReset()
	if err != nil || transition == nil {
		return transition, err
	}
	if err := transition.publish(); err != nil {
		transition.cancel()
		return nil, err
	}
	return transition, nil
}

// resetForWorkspaceChange is the non-transactional compatibility path used by
// direct trusted callers. ProjectService uses beginWorkspaceReset so a later
// setter or project-save failure can restore the prior authority.
func (l *AgentLifecycle) resetForWorkspaceChange() error {
	transition, err := l.beginWorkspaceReset()
	if err != nil {
		return err
	}
	if transition == nil {
		return nil
	}
	transition.commit()
	return nil
}

func cloneLifecycleRuntimeIDs(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func (l *AgentLifecycle) captureWorkspaceSnapshotLocked() (agentLifecycleWorkspaceSnapshot, error) {
	if l == nil {
		return agentLifecycleWorkspaceSnapshot{}, nil
	}
	snapshot := agentLifecycleWorkspaceSnapshot{}
	if l.sessions != nil {
		captured, err := l.sessions.CaptureSnapshot()
		if err != nil {
			return agentLifecycleWorkspaceSnapshot{}, err
		}
		snapshot.sessions = captured
	}
	if l.runtime != nil {
		snapshot.runtime = l.runtime.CaptureSnapshot()
	}
	l.ownerMu.RLock()
	snapshot.runtimeIDs = cloneLifecycleRuntimeIDs(l.runtimeIDs)
	l.ownerMu.RUnlock()
	return snapshot, nil
}

func (l *AgentLifecycle) restoreWorkspaceSnapshotLocked(snapshot agentLifecycleWorkspaceSnapshot) error {
	if l == nil {
		return nil
	}
	if l.sessions != nil {
		if err := l.sessions.RestoreSnapshot(snapshot.sessions); err != nil {
			l.sessions.Poison(err)
			l.revokeAllRuntimeAuthority()
			return fmt.Errorf("restore durable lifecycle rows: %w", err)
		}
	}
	if l.runtime != nil {
		l.runtime.RestoreSnapshot(snapshot.runtime)
	}
	l.ownerMu.Lock()
	l.runtimeIDs = cloneLifecycleRuntimeIDs(snapshot.runtimeIDs)
	l.ownerMu.Unlock()
	return nil
}

func (t *agentLifecycleWorkspaceReset) commit() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lifecycle == nil || t.done {
		return
	}
	t.done = true
	t.lifecycle.transitionMu.Unlock()
}

func (t *agentLifecycleWorkspaceReset) rollback() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lifecycle == nil || t.done {
		return t.result
	}
	err := t.lifecycle.restoreWorkspaceSnapshotLocked(t.snapshot)
	if err != nil {
		// A failed restore is deliberately fail-closed. Do not expose a partial
		// runtime/owner map to later callers.
		t.lifecycle.revokeAllRuntimeAuthority()
	}
	t.done = true
	t.result = err
	t.lifecycle.transitionMu.Unlock()
	return t.result
}

func (l *AgentLifecycle) revokeAllRuntimeAuthority() {
	if l.runtime != nil {
		l.runtime.UnregisterAllSessions()
	}
	l.ownerMu.Lock()
	l.runtimeIDs = make(map[string]string)
	l.ownerMu.Unlock()
}

func (l *AgentLifecycle) poisonWorkspaceAuthority(cause error) {
	if l == nil {
		return
	}
	if l.sessions != nil {
		l.sessions.Poison(cause)
	}
	l.revokeAllRuntimeAuthority()
}

type aiPermissionUsageSink struct {
	permission *AIPermissionService
}

// agentLifecycleRuntimeUsageSink is installed only on AgentService's Runtime.
// Runtime.Execute already holds the shared workspace read authority through
// the complete handler and metering sequence, so this adapter must call the
// held variants instead of recursively acquiring the same RWMutex.
type agentLifecycleRuntimeUsageSink struct {
	lifecycle *AgentLifecycle
}

var (
	_ agentcore.UsageTransactionSink = aiPermissionUsageSink{}
	_ agentcore.UsageTransactionSink = (*AgentLifecycle)(nil)
	_ agentcore.UsageTransactionSink = agentLifecycleRuntimeUsageSink{}
)

type chatLifecycleUnit struct {
	lifecycle       *AgentLifecycle
	id              string
	sessionID       string
	unitKind        agentcore.UsageUnitKind
	operation       AIOperation
	providerID      string
	model           string
	startedAt       time.Time
	inputTokens     int
	completeSession bool
	usageReceipt    agentcore.UsageReceipt
	workspaceLease  *agentWorkspaceAuthorityReadLease
}

type agentWorkspaceAuthorityReadLease struct {
	once      sync.Once
	authority *sync.RWMutex
}

func (l *AgentLifecycle) acquireWorkspaceAuthority() (*agentWorkspaceAuthorityReadLease, error) {
	if l == nil || l.workspaceAuthorityMu == nil {
		return nil, fmt.Errorf("agent workspace authority is unavailable: %w", ErrNotAllowed)
	}
	l.workspaceAuthorityMu.RLock()
	return &agentWorkspaceAuthorityReadLease{authority: l.workspaceAuthorityMu}, nil
}

func (l *agentWorkspaceAuthorityReadLease) release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.authority != nil {
			l.authority.RUnlock()
		}
	})
}

func (s aiPermissionUsageSink) RecordUsage(record agentcore.UsageRecord) error {
	if s.permission == nil {
		return fmt.Errorf("AI permission service is required")
	}
	return s.permission.recordAgentUsage(record)
}

func (s aiPermissionUsageSink) BeginUsage(record agentcore.UsageRecord) (agentcore.UsageReceipt, error) {
	if s.permission == nil {
		return agentcore.UsageReceipt{}, fmt.Errorf("AI permission service is required")
	}
	return s.permission.beginAgentUsage(record)
}

func (s aiPermissionUsageSink) CompleteUsage(receipt agentcore.UsageReceipt, record agentcore.UsageRecord) error {
	if s.permission == nil {
		return fmt.Errorf("AI permission service is required")
	}
	return s.permission.completeAgentUsage(receipt, record)
}

func (s agentLifecycleRuntimeUsageSink) RecordUsage(record agentcore.UsageRecord) error {
	if s.lifecycle == nil {
		return fmt.Errorf("agent lifecycle meter is unavailable")
	}
	return s.lifecycle.recordUsageWithinWorkspaceAuthority(record)
}

func (s agentLifecycleRuntimeUsageSink) BeginUsage(record agentcore.UsageRecord) (agentcore.UsageReceipt, error) {
	if s.lifecycle == nil {
		return agentcore.UsageReceipt{}, fmt.Errorf("agent lifecycle meter is unavailable")
	}
	return s.lifecycle.beginUsageWithinWorkspaceAuthority(record)
}

func (s agentLifecycleRuntimeUsageSink) CompleteUsage(receipt agentcore.UsageReceipt, record agentcore.UsageRecord) error {
	if s.lifecycle == nil {
		return fmt.Errorf("agent lifecycle meter is unavailable")
	}
	return s.lifecycle.completeUsageWithinWorkspaceAuthority(receipt, record)
}

// WireAgentLifecycle installs one lifecycle/context instance and connects the
// existing agent Runtime meter to AIPermissionService. It is package-level so
// Wails cannot expose dependency injection to the renderer.
func WireAgentLifecycle(
	agent *AgentService,
	ai *AIService,
	plan *AIPlanService,
	goal *AIGoalService,
	permission *AIPermissionService,
	persistenceDirs ...string,
) (*AgentLifecycle, error) {
	if agent == nil || ai == nil || plan == nil || goal == nil || permission == nil {
		return nil, fmt.Errorf("agent, AI, plan, goal, and permission services are required: %w", ErrInvalidInput)
	}
	var sessions *agentcore.SessionStore
	var workspaceIdentityKey []byte
	var err error
	if len(persistenceDirs) > 0 && strings.TrimSpace(persistenceDirs[0]) != "" {
		persistenceDir := persistenceDirs[0]
		path := filepath.Join(persistenceDir, "agent_lifecycle_sessions.json")
		identityPath := filepath.Join(persistenceDir, "agent_lifecycle_identity.key")
		snapshotExists, statErr := os.Stat(path)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect durable agent lifecycle snapshot: %w", statErr)
		}
		workspaceIdentityKey, err = loadAgentLifecycleIdentityKey(identityPath, statErr == nil && !snapshotExists.IsDir())
		if err != nil {
			return nil, fmt.Errorf("load agent lifecycle workspace identity key: %w", err)
		}
		sessions, err = agentcore.NewPersistentSessionStore(agentcore.FileSessionStorePersistence{Path: path}, time.Now)
		if err != nil {
			return nil, fmt.Errorf("load durable agent lifecycle: %w", err)
		}
	} else {
		sessions = agentcore.NewSessionStore(time.Now)
		workspaceIdentityKey = make([]byte, 32)
		if _, err := rand.Read(workspaceIdentityKey); err != nil {
			return nil, fmt.Errorf("create ephemeral agent lifecycle workspace identity key: %w", err)
		}
	}
	return wireAgentLifecycleWithStore(agent, ai, plan, goal, permission, sessions, workspaceIdentityKey)
}

// wireAgentLifecycleWithStateRoot is the trusted root-backed variant used by
// non-renderer hosts. Snapshot and identity-key operations share the same
// retained state capability as usage and the instance lock.
func wireAgentLifecycleWithStateRoot(
	agent *AgentService,
	ai *AIService,
	plan *AIPlanService,
	goal *AIGoalService,
	permission *AIPermissionService,
	stateRoot *os.Root,
) (*AgentLifecycle, error) {
	if agent == nil || ai == nil || plan == nil || goal == nil || permission == nil || stateRoot == nil {
		return nil, fmt.Errorf("agent lifecycle state capability is required: %w", ErrInvalidInput)
	}
	const snapshotName = "agent_lifecycle_sessions.json"
	snapshotExists := false
	if info, err := stateRoot.Lstat(snapshotName); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("durable agent lifecycle snapshot is unsafe: %w", ErrUsagePersistencePoisoned)
		}
		snapshotExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect durable agent lifecycle snapshot: %w", ErrUsagePersistencePoisoned)
	}
	workspaceIdentityKey, err := loadOrCreateAgentStateKey(
		stateRoot, "agent_lifecycle_identity.key", snapshotExists,
	)
	if err != nil {
		return nil, fmt.Errorf("load root-bound lifecycle identity key: %w", err)
	}
	sessions, err := agentcore.NewPersistentSessionStore(agentcore.FileSessionStorePersistence{
		Root: agentSessionPersistenceRoot{root: stateRoot}, Name: snapshotName,
	}, time.Now)
	if err != nil {
		return nil, fmt.Errorf("load root-bound durable agent lifecycle: %w", err)
	}
	return wireAgentLifecycleWithStore(agent, ai, plan, goal, permission, sessions, workspaceIdentityKey)
}

func wireAgentLifecycleWithStore(
	agent *AgentService,
	ai *AIService,
	plan *AIPlanService,
	goal *AIGoalService,
	permission *AIPermissionService,
	sessions *agentcore.SessionStore,
	workspaceIdentityKey []byte,
) (*AgentLifecycle, error) {
	if sessions == nil || len(workspaceIdentityKey) == 0 {
		return nil, fmt.Errorf("agent lifecycle persistence is unavailable: %w", ErrUsagePersistence)
	}
	runtime, err := agent.coreRuntime()
	if err != nil {
		return nil, err
	}
	estimator := sharedTokenEstimator{}
	contextManager, err := agentcore.NewContextManager(estimator)
	if err != nil {
		return nil, err
	}
	incarnation, err := newLifecycleIncarnation()
	if err != nil {
		return nil, err
	}
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	settings := deps.settings
	deps.mu.RUnlock()
	lifecycle := &AgentLifecycle{
		sessions:             sessions,
		context:              contextManager,
		estimator:            estimator,
		runtime:              runtime,
		meter:                aiPermissionUsageSink{permission: permission},
		permission:           permission,
		settings:             settings,
		now:                  time.Now,
		workspaceGeneration:  agent.agentWorkspaceGeneration,
		workspaceLease:       agent.acquireWorkspaceLease,
		workspaceGuard:       agent.withCurrentWorkspaceGeneration,
		workspaceIdentityKey: append([]byte(nil), workspaceIdentityKey...),
		incarnation:          incarnation,
		workspaceAuthorityMu: &deps.workspaceAuthorityMu,
		runtimeIDs:           make(map[string]string),
	}
	runtime.SetUsageSink(agentLifecycleRuntimeUsageSink{lifecycle: lifecycle})
	// Desktop execution must not allow a handler side effect to outrun a
	// durable usage receipt. Headless tests/hosts may keep the compatibility
	// default until they install their own sink.
	runtime.SetUsageRequirements(true, true)
	deps.mu.Lock()
	deps.lifecycle = lifecycle
	deps.ai = ai
	deps.mu.Unlock()
	ai.setAgentLifecycle(agent, lifecycle)
	plan.setAgentLifecycle(lifecycle)
	goal.setAgentLifecycle(lifecycle)
	return lifecycle, nil
}

// withCurrentWorkspaceGeneration closes the lifecycle publication race with a
// workspace switch. WorkspaceContext-backed hosts hold its read lock; legacy
// headless hosts hold AgentService's root lock until fn returns.
func (s *AgentService) withCurrentWorkspaceGeneration(expected uint64, fn func() error) error {
	if s == nil || fn == nil {
		return fmt.Errorf("workspace generation guard is unavailable: %w", ErrInvalidInput)
	}
	s.mu.Lock()
	workspace := s.workspaceContext
	if workspace == nil {
		current := uint64(0)
		if s.rootDir != "" {
			current = s.rootGeneration
		}
		if current != expected {
			s.mu.Unlock()
			return fmt.Errorf("workspace changed before lifecycle publication: %w", ErrNotAllowed)
		}
		defer s.mu.Unlock()
		return fn()
	}
	s.mu.Unlock()

	workspace.mu.RLock()
	defer workspace.mu.RUnlock()
	current := uint64(0)
	if workspace.root != "" {
		current = workspace.generation
	}
	if current != expected {
		return fmt.Errorf("workspace changed before lifecycle publication: %w", ErrNotAllowed)
	}
	return fn()
}

func (l *AgentLifecycle) currentWorkspaceGeneration() uint64 {
	if l == nil || l.workspaceGeneration == nil {
		return 0
	}
	return l.workspaceGeneration()
}

func (l *AgentLifecycle) withCurrentWorkspaceGeneration(expected uint64, fn func() error) error {
	if l == nil || fn == nil {
		return fmt.Errorf("workspace generation guard is unavailable: %w", ErrInvalidInput)
	}
	if l.workspaceGuard != nil {
		return l.workspaceGuard(expected, fn)
	}
	if l.currentWorkspaceGeneration() != expected {
		return fmt.Errorf("workspace changed before lifecycle publication: %w", ErrNotAllowed)
	}
	return fn()
}

// loadAgentLifecycleIdentityKey never regenerates an identity key while a
// durable lifecycle snapshot exists. Regeneration would make every persisted
// workspace claim unverifiable and silently strand recovery rows.
func loadAgentLifecycleIdentityKey(path string, snapshotExists bool) ([]byte, error) {
	if !snapshotExists {
		return loadOrCreateAESKeyAt(path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open existing lifecycle identity key: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat existing lifecycle identity key: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("lifecycle identity key permissions %o are too broad", info.Mode().Perm())
	}
	data, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil {
		return nil, fmt.Errorf("read existing lifecycle identity key: %w", err)
	}
	if len(data) > 64 {
		return nil, fmt.Errorf("lifecycle identity key is oversized")
	}
	key, err := decodeAESKey(data)
	if err != nil {
		return nil, fmt.Errorf("decode existing lifecycle identity key: %w", err)
	}
	return key, nil
}

// WireTaskAgentLifecycle connects TaskService's workflow owner to the same
// headless lifecycle used by chat/plan/goal. It is trusted bootstrap wiring,
// never a renderer-callable dependency injection path.
func WireTaskAgentLifecycle(task *TaskService, lifecycle *AgentLifecycle) error {
	if task == nil || lifecycle == nil {
		return fmt.Errorf("task and agent lifecycle are required: %w", ErrInvalidInput)
	}
	task.setAgentLifecycle(lifecycle)
	return nil
}

func (a *AIService) setAgentLifecycle(agent *AgentService, lifecycle *AgentLifecycle) {
	a.mu.Lock()
	a.agent = agent
	a.lifecycle = lifecycle
	a.mu.Unlock()
}

func (s *AIPlanService) setAgentLifecycle(lifecycle *AgentLifecycle) {
	s.mu.Lock()
	s.lifecycle = lifecycle
	s.mu.Unlock()
}

func (s *AIGoalService) setAgentLifecycle(lifecycle *AgentLifecycle) {
	s.mu.Lock()
	s.lifecycle = lifecycle
	s.mu.Unlock()
}

func (a *AIService) prepareMessagesForCall(snap aiSnapshot, messages []ChatMessage) ([]ChatMessage, int, error) {
	if snap.lifecycle == nil {
		prepared := prepareMessagesWith(snap.config, messages)
		return prepared, estimateMessagesTokens(prepared), nil
	}
	return snap.lifecycle.prepareChatMessages(snap.config, messages)
}

func beginChatLifecycle(snap aiSnapshot, id string, messages []ChatMessage) (*chatLifecycleUnit, error) {
	return beginAILifecycle(snap, id, agentcore.UsageUnitChat, AIOpChat, messages)
}

func beginChatLifecycleWithWorkspaceLease(snap aiSnapshot, id string, messages []ChatMessage, lease *agentWorkspaceAuthorityReadLease) (*chatLifecycleUnit, error) {
	return beginAILifecycleWithWorkspaceLease(snap, id, agentcore.UsageUnitChat, AIOpChat, messages, lease)
}

func beginAILifecycle(snap aiSnapshot, id string, unitKind agentcore.UsageUnitKind, operation AIOperation, messages []ChatMessage) (*chatLifecycleUnit, error) {
	return beginAILifecycleMode(snap, id, unitKind, operation, messages, true)
}

func beginAILifecycleWithWorkspaceLease(snap aiSnapshot, id string, unitKind agentcore.UsageUnitKind, operation AIOperation, messages []ChatMessage, lease *agentWorkspaceAuthorityReadLease) (*chatLifecycleUnit, error) {
	return beginAILifecycleModeWithWorkspaceLease(snap, id, unitKind, operation, messages, true, lease)
}

func beginPersistentChatLifecycle(snap aiSnapshot, id string, messages []ChatMessage) (*chatLifecycleUnit, error) {
	return beginAILifecycleMode(snap, id, agentcore.UsageUnitChat, AIOpChat, messages, false)
}

func beginPersistentChatLifecycleWithWorkspaceLease(snap aiSnapshot, id string, messages []ChatMessage, lease *agentWorkspaceAuthorityReadLease) (*chatLifecycleUnit, error) {
	return beginAILifecycleModeWithWorkspaceLease(snap, id, agentcore.UsageUnitChat, AIOpChat, messages, false, lease)
}

func beginAILifecycleMode(snap aiSnapshot, id string, unitKind agentcore.UsageUnitKind, operation AIOperation, messages []ChatMessage, completeSession bool) (*chatLifecycleUnit, error) {
	return beginAILifecycleModeWithWorkspaceLease(snap, id, unitKind, operation, messages, completeSession, nil)
}

func beginAILifecycleModeWithWorkspaceLease(snap aiSnapshot, id string, unitKind agentcore.UsageUnitKind, operation AIOperation, messages []ChatMessage, completeSession bool, workspaceLease *agentWorkspaceAuthorityReadLease) (*chatLifecycleUnit, error) {
	if snap.lifecycle == nil {
		workspaceLease.release()
		return nil, nil
	}
	var err error
	if workspaceLease == nil {
		workspaceLease, err = snap.lifecycle.acquireWorkspaceAuthority()
		if err != nil {
			return nil, err
		}
	} else if workspaceLease.authority != snap.lifecycle.workspaceAuthorityMu {
		workspaceLease.release()
		return nil, fmt.Errorf("AI workspace authority does not match its lifecycle: %w", ErrNotAllowed)
	}
	releaseWorkspace := true
	defer func() {
		if releaseWorkspace {
			workspaceLease.release()
		}
	}()
	var session agentcore.Session
	if completeSession {
		session, err = snap.lifecycle.beginWithinWorkspaceAuthority(agentcore.SessionChat, id)
	} else {
		session, err = snap.lifecycle.beginExistingWithinWorkspaceAuthority(agentcore.SessionChat, id)
	}
	if err != nil {
		return nil, err
	}
	startedAt := snap.lifecycle.now()
	unit := &chatLifecycleUnit{
		lifecycle: snap.lifecycle, id: id, sessionID: session.ID,
		unitKind: unitKind, operation: operation,
		providerID: usageProviderID(snap.config), model: snap.config.Model,
		startedAt:       startedAt,
		inputTokens:     snap.lifecycle.estimateMessages(withSystemPromptFrom(snap.config, messages)),
		completeSession: completeSession,
		workspaceLease:  workspaceLease,
	}
	if _, err := snap.lifecycle.checkpointWithinWorkspaceAuthority(agentcore.SessionChat, id, "request-started", map[string]interface{}{
		"phase": "request-started",
	}); err != nil {
		_ = snap.lifecycle.failWithinWorkspaceAuthority(agentcore.SessionChat, id, err)
		return nil, err
	}
	receipt, err := snap.lifecycle.beginUsageWithinWorkspaceAuthority(agentcore.UsageRecord{
		UnitID:    snap.lifecycle.newUnitID(unitKind, startedAt),
		SessionID: session.ID, UnitKind: unitKind, Operation: string(operation),
		ProviderID: unit.providerID, Model: unit.model,
		TokensIn: unit.inputTokens, CostBasis: agentcore.CostNotApplicable,
		StartedAt: startedAt, CompletedAt: startedAt, Pending: true,
	})
	if err != nil {
		_ = snap.lifecycle.failWithinWorkspaceAuthority(agentcore.SessionChat, id, err)
		return nil, err
	}
	unit.usageReceipt = receipt
	releaseWorkspace = false
	return unit, nil
}

func usageProviderID(cfg AIConfig) string {
	if cfg.ConfigID != "" {
		return cfg.ConfigID
	}
	if isAnthropicProtocol(cfg) {
		return "anthropic"
	}
	return "openai-compatible"
}

func (u *chatLifecycleUnit) finish(response *ChatResponse, callErr error) error {
	if u == nil {
		return nil
	}
	defer u.releaseWorkspaceAuthority()
	if u.lifecycle == nil {
		return nil
	}
	completedAt := u.lifecycle.now()
	tokensIn := u.inputTokens
	tokensOut := 0
	basis := agentcore.CostNotApplicable
	estimated := false
	var appendErr error
	if response != nil {
		if response.Content != "" {
			appendErr = u.lifecycle.appendWithinWorkspaceAuthority(agentcore.SessionChat, u.id, agentcore.StreamEventInput{
				Kind: agentcore.StreamDelta, Data: response.Content,
			})
		}
		if response.usageReported {
			tokensIn = response.tokensIn
			tokensOut = response.tokensOut
		} else {
			tokensOut = u.lifecycle.estimateText(response.Content)
			basis = agentcore.CostEstimated
			estimated = true
		}
	} else if tokensIn > 0 {
		basis = agentcore.CostEstimated
		estimated = true
	}
	if appendErr != nil {
		callErr = errors.Join(callErr, appendErr)
	}
	success := callErr == nil
	if success {
		if _, err := u.lifecycle.checkpointWithinWorkspaceAuthority(agentcore.SessionChat, u.id, "response-completed", map[string]interface{}{
			"phase": "response-completed",
		}); err != nil {
			callErr = err
			success = false
		}
	}
	record := agentcore.UsageRecord{
		UnitID: u.usageReceipt.UnitID, SessionID: u.sessionID,
		UnitKind: u.unitKind, Operation: string(u.operation),
		ProviderID: u.providerID, Model: u.model,
		TokensIn: tokensIn, TokensOut: tokensOut,
		CostBasis: basis, Estimated: estimated,
		StartedAt: u.startedAt, CompletedAt: completedAt, Success: success,
	}
	if !success {
		record.Error = "execution failed"
	}
	var meterErr error
	if u.usageReceipt.UnitID != "" {
		meterErr = u.lifecycle.completeUsageWithinWorkspaceAuthority(u.usageReceipt, record)
	} else {
		meterErr = u.lifecycle.recordWithinWorkspaceAuthority(record)
	}
	if meterErr != nil && success {
		// A successful provider response is not a successful lifecycle unit when
		// its usage row could not be durably recorded. Keep the session resumable
		// instead of completing it with an invisible cost.
		callErr = meterErr
		success = false
	}
	var terminalErr error
	if success && u.completeSession {
		terminalErr = u.lifecycle.completeWithinWorkspaceAuthority(agentcore.SessionChat, u.id)
	} else if !success {
		terminalErr = u.lifecycle.failWithinWorkspaceAuthority(agentcore.SessionChat, u.id, callErr)
	}
	return errors.Join(callErr, meterErr, terminalErr)
}

func (u *chatLifecycleUnit) releaseWorkspaceAuthority() {
	if u == nil {
		return
	}
	u.workspaceLease.release()
}

func lifecycleSessionID(kind agentcore.SessionKind, id string) string {
	prefix := string(kind) + ":"
	if strings.HasPrefix(id, prefix) {
		return id
	}
	return prefix + id
}

func (l *AgentLifecycle) runtimeSessionID(kind agentcore.SessionKind, id string) string {
	logical := lifecycleSessionID(kind, id)
	l.ownerMu.RLock()
	runtimeID := l.runtimeIDs[logical]
	l.ownerMu.RUnlock()
	if runtimeID != "" {
		return runtimeID
	}
	return logical
}

func (l *AgentLifecycle) bindRuntimeSession(logical, runtimeID string) {
	if l == nil || logical == "" || runtimeID == "" {
		return
	}
	l.ownerMu.Lock()
	if l.runtimeIDs == nil {
		l.runtimeIDs = make(map[string]string)
	}
	l.runtimeIDs[logical] = runtimeID
	l.ownerMu.Unlock()
}

func (l *AgentLifecycle) unbindRuntimeSession(logical string) {
	if l == nil || logical == "" {
		return
	}
	l.ownerMu.Lock()
	delete(l.runtimeIDs, logical)
	l.ownerMu.Unlock()
}

func (l *AgentLifecycle) logicalSessionForRuntime(runtimeID string) string {
	if l == nil || runtimeID == "" {
		return ""
	}
	l.ownerMu.RLock()
	defer l.ownerMu.RUnlock()
	for logical, candidate := range l.runtimeIDs {
		if candidate == runtimeID {
			return logical
		}
	}
	return ""
}

func newOpaqueLifecycleRuntimeID(kind agentcore.SessionKind) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("create %s runtime session identity: %w", kind, err)
	}
	return string(kind) + ":owner:" + hex.EncodeToString(raw), nil
}

func newLifecycleIncarnation() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("create lifecycle incarnation: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func lifecycleOwnerDomain(kind agentcore.SessionKind) string {
	switch kind {
	case agentcore.SessionChat:
		return "chat-service"
	case agentcore.SessionPlan:
		return "plan-service"
	case agentcore.SessionGoal:
		return "goal-service"
	case agentcore.SessionWorkflow:
		return "workflow-service"
	default:
		return "unknown"
	}
}

// The first indeterminate error means the replacement may already be visible,
// so runtime state must follow the retained in-memory mutation. A poisoned
// store rejects later writes before publication and those attempts may restore
// the runtime state they suspended or revoked optimistically.
func lifecyclePersistenceIsIndeterminate(err error) bool {
	return errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate)
}

func shouldRestoreRuntimeAfterLifecycleError(err error) bool {
	return !lifecyclePersistenceIsIndeterminate(err)
}

func (l *AgentLifecycle) hasCurrentRuntimeOwner(session agentcore.Session, runtimeID string) bool {
	return session.Owner != nil &&
		session.Owner.Domain == lifecycleOwnerDomain(session.Kind) &&
		session.Owner.RuntimeID == runtimeID &&
		session.Owner.Incarnation == l.incarnation
}

func (l *AgentLifecycle) revokeRuntimeAuthority(logicalID, runtimeID string) {
	if l.runtime != nil && l.runtime.IsSessionRegistered(runtimeID) {
		_ = l.runtime.UnregisterSession(runtimeID)
	}
	l.unbindRuntimeSession(logicalID)
}

func (l *AgentLifecycle) beginOwnedSession(logicalID string, kind agentcore.SessionKind, runtimeID string, permissionMode agentcore.SessionPermissionMode) (agentcore.Session, error) {
	if l == nil || l.sessions == nil {
		return agentcore.Session{}, fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	if !permissionMode.Valid() {
		return agentcore.Session{}, fmt.Errorf("invalid session permission mode %q: %w", permissionMode, ErrInvalidInput)
	}
	generation := uint64(0)
	if l.workspaceGeneration != nil {
		generation = l.workspaceGeneration()
	}
	owner := agentcore.SessionOwner{
		Domain: lifecycleOwnerDomain(kind), RuntimeID: runtimeID,
		WorkspaceGeneration: generation, Incarnation: l.incarnation,
	}
	if generation == 0 {
		owner.WorkspaceFingerprint = l.workspaceOwnerFingerprint("", logicalID, kind, owner)
		return l.sessions.BeginOwnedWithPermissionMode(logicalID, kind, owner, permissionMode)
	}
	if l.workspaceLease == nil {
		return agentcore.Session{}, fmt.Errorf("workspace lease is unavailable: %w", ErrNotAllowed)
	}
	lease, err := l.workspaceLease()
	if err != nil {
		return agentcore.Session{}, err
	}
	owner.WorkspaceGeneration = lease.generation
	owner.WorkspaceFingerprint = l.workspaceOwnerFingerprint(lease.root, logicalID, kind, owner)
	var session agentcore.Session
	if err := lease.withCurrent(func() error {
		var beginErr error
		session, beginErr = l.sessions.BeginOwnedWithPermissionMode(logicalID, kind, owner, permissionMode)
		return beginErr
	}); err != nil {
		return agentcore.Session{}, err
	}
	return session, nil
}

func (l *AgentLifecycle) configuredPermissionMode() agentcore.SessionPermissionMode {
	if l == nil || l.settings == nil {
		return agentcore.SessionPermissionAlwaysAsk
	}
	settings, err := l.settings.LoadSettings()
	if err != nil {
		return agentcore.SessionPermissionAlwaysAsk
	}
	mode := agentcore.SessionPermissionMode(settings.AgentPermissionMode)
	if !mode.Valid() {
		return agentcore.SessionPermissionAlwaysAsk
	}
	return mode
}

func (l *AgentLifecycle) workspaceOwnerFingerprint(root, sessionID string, kind agentcore.SessionKind, owner agentcore.SessionOwner) string {
	if l == nil || len(l.workspaceIdentityKey) == 0 {
		return ""
	}
	if strings.TrimSpace(root) == "" {
		root = "<no-workspace>"
	}
	identity := filepath.ToSlash(filepath.Clean(root))
	if runtime.GOOS == "windows" {
		identity = strings.ToLower(identity)
	}
	claim := struct {
		Version     string                `json:"version"`
		Root        string                `json:"root"`
		SessionID   string                `json:"sessionId"`
		Kind        agentcore.SessionKind `json:"kind"`
		Domain      string                `json:"domain"`
		Incarnation string                `json:"incarnation"`
	}{
		Version: "agent-lifecycle-owner-v1", Root: identity, SessionID: sessionID,
		Kind: kind, Domain: owner.Domain, Incarnation: owner.Incarnation,
	}
	encoded, err := json.Marshal(claim)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, l.workspaceIdentityKey)
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil))
}

// CreateOwnedSession performs the durable-before-authority issuance sequence
// for renderer-facing chat/workflow sessions. A crash between the durable row
// and runtime registration leaves only a recovery-required orphan, never a
// live capability with no lifecycle record.
func (l *AgentLifecycle) CreateOwnedSession(kind agentcore.SessionKind) (string, error) {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return "", err
	}
	defer workspaceLease.release()
	return l.createOwnedSessionWithinWorkspaceAuthority(kind, l.configuredPermissionMode())
}

func (l *AgentLifecycle) createOwnedSessionWithinWorkspaceAuthority(kind agentcore.SessionKind, permissionMode agentcore.SessionPermissionMode) (string, error) {
	if l == nil || l.sessions == nil || l.runtime == nil {
		return "", fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	if kind != agentcore.SessionChat && kind != agentcore.SessionWorkflow {
		return "", fmt.Errorf("session kind %q is domain-owned: %w", kind, ErrNotAllowed)
	}
	if !permissionMode.Valid() {
		return "", fmt.Errorf("invalid session permission mode %q: %w", permissionMode, ErrInvalidInput)
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("create %s session identity: %w", kind, err)
	}
	sessionID := string(kind) + ":" + hex.EncodeToString(raw)
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	if _, err := l.beginOwnedSession(sessionID, kind, sessionID, permissionMode); err != nil {
		return "", err
	}
	if err := l.runtime.RegisterSession(sessionID); err != nil {
		return "", errors.Join(err, l.sessions.Delete(sessionID))
	}
	l.bindRuntimeSession(sessionID, sessionID)
	return sessionID, nil
}

func (l *AgentLifecycle) Begin(kind agentcore.SessionKind, id string) (agentcore.Session, error) {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return agentcore.Session{}, err
	}
	defer workspaceLease.release()
	return l.beginWithinWorkspaceAuthority(kind, id)
}

func (l *AgentLifecycle) beginWithinWorkspaceAuthority(kind agentcore.SessionKind, id string) (agentcore.Session, error) {
	if l == nil || l.sessions == nil {
		return agentcore.Session{}, fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	if strings.TrimSpace(id) == "" {
		return agentcore.Session{}, fmt.Errorf("session ID is required: %w", ErrInvalidInput)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	sessionID := lifecycleSessionID(kind, id)
	// Do not re-register an existing failed/paused session here. Begin is for a
	// fresh owner only; ResumeLatest is the sole resumable transition.
	if _, err := l.sessions.Get(sessionID); err == nil {
		return l.sessions.Begin(sessionID, kind)
	} else if !errors.Is(err, agentcore.ErrSessionNotFound) {
		return agentcore.Session{}, err
	}
	runtimeID := sessionID
	if kind == agentcore.SessionPlan || kind == agentcore.SessionGoal {
		var err error
		runtimeID, err = newOpaqueLifecycleRuntimeID(kind)
		if err != nil {
			return agentcore.Session{}, err
		}
	}
	permissionMode := l.configuredPermissionMode()
	session, err := l.beginOwnedSession(sessionID, kind, runtimeID, permissionMode)
	if err != nil {
		return agentcore.Session{}, err
	}
	if l.runtime != nil && !l.runtime.IsSessionRegistered(runtimeID) {
		if err := l.runtime.RegisterSession(runtimeID); err != nil {
			return agentcore.Session{}, errors.Join(err, l.sessions.Delete(sessionID))
		}
	}
	l.bindRuntimeSession(sessionID, runtimeID)
	return session, err
}

// BeginExisting starts a session that was already issued by trusted runtime
// wiring (for example AgentService.createAgentSessionTrusted). Renderer-supplied IDs
// are never registered here, so this method preserves the ownership boundary
// while allowing a persistent chat/workflow owner to resume across requests.
func (l *AgentLifecycle) BeginExisting(kind agentcore.SessionKind, id string) (agentcore.Session, error) {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return agentcore.Session{}, err
	}
	defer workspaceLease.release()
	return l.beginExistingWithinWorkspaceAuthority(kind, id)
}

func (l *AgentLifecycle) beginExistingWithinWorkspaceAuthority(kind agentcore.SessionKind, id string) (agentcore.Session, error) {
	if l == nil || l.sessions == nil || l.runtime == nil {
		return agentcore.Session{}, fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	id = lifecycleSessionID(kind, id)
	if existing, err := l.sessions.Get(id); err == nil && existing.Recovery == agentcore.SessionRecoveryRequired {
		return agentcore.Session{}, fmt.Errorf("session %q: %w", id, agentcore.ErrSessionRecoveryRequired)
	}
	runtimeID := l.runtimeSessionID(kind, id)
	if !l.runtime.IsSessionRegistered(runtimeID) {
		return agentcore.Session{}, fmt.Errorf("session %q: %w", id, agentcore.ErrUnknownSession)
	}
	if existing, err := l.sessions.Get(id); err == nil {
		if !l.hasCurrentRuntimeOwner(existing, runtimeID) {
			return agentcore.Session{}, fmt.Errorf("session %q has mismatched durable owner metadata: %w", id, ErrNotAllowed)
		}
		if existing.Status == agentcore.SessionCompleted {
			return agentcore.Session{}, fmt.Errorf("session %q is already completed: %w", id, agentcore.ErrInvalidSessionTransition)
		}
		if existing.Status == agentcore.SessionFailed || existing.Status == agentcore.SessionPaused {
			if err := l.resumeLatestLocked(kind, id); err != nil {
				return agentcore.Session{}, err
			}
			return l.sessions.Get(id)
		}
		return existing, nil
	}
	activeBefore := l.runtime.IsSessionActive(runtimeID)
	if activeBefore {
		if err := l.runtime.SuspendSession(runtimeID); err != nil {
			return agentcore.Session{}, err
		}
	}
	permissionMode := l.configuredPermissionMode()
	session, err := l.beginOwnedSession(id, kind, runtimeID, permissionMode)
	if err != nil {
		if activeBefore && shouldRestoreRuntimeAfterLifecycleError(err) {
			if restoreErr := l.runtime.ActivateSession(runtimeID); restoreErr != nil {
				return agentcore.Session{}, errors.Join(err, fmt.Errorf("restore runtime after rejected owner publication: %w", restoreErr))
			}
		} else if lifecyclePersistenceIsIndeterminate(err) {
			l.revokeRuntimeAuthority(id, runtimeID)
		}
		return agentcore.Session{}, err
	}
	l.bindRuntimeSession(id, runtimeID)
	if activeBefore {
		if err := l.runtime.ActivateSession(runtimeID); err != nil {
			l.unbindRuntimeSession(id)
			revokeErr := l.runtime.UnregisterSession(runtimeID)
			return agentcore.Session{}, errors.Join(err, revokeErr)
		}
	}
	return session, nil
}

func (l *AgentLifecycle) Get(kind agentcore.SessionKind, id string) (agentcore.Session, error) {
	return l.GetByID(lifecycleSessionID(kind, id))
}

func (l *AgentLifecycle) GetByID(id string) (agentcore.Session, error) {
	if l == nil || l.sessions == nil {
		return agentcore.Session{}, fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	return l.sessions.Get(id)
}

// recoveryRowsLocked returns only rows whose previous owner claim still binds
// to the current workspace. transitionMu must be held so handle resolution and
// disposition cannot be separated by a lifecycle transition.
func (l *AgentLifecycle) recoveryRowsLocked(includeDisposed bool) ([]agentcore.Session, error) {
	if l == nil || l.sessions == nil || l.runtime == nil {
		return nil, fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	currentGeneration := uint64(0)
	if l.workspaceGeneration != nil {
		currentGeneration = l.workspaceGeneration()
	}
	root := ""
	var rows []agentcore.Session
	loadRows := func() error {
		var err error
		if includeDisposed {
			rows, err = l.sessions.RecoveryDispositionCandidates()
		} else {
			rows, err = l.sessions.RecoveryRequired()
		}
		return err
	}
	if currentGeneration == 0 {
		if err := loadRows(); err != nil {
			return nil, err
		}
	} else {
		if l.workspaceLease == nil {
			return nil, fmt.Errorf("workspace lease is unavailable: %w", ErrNotAllowed)
		}
		lease, err := l.workspaceLease()
		if err != nil {
			return nil, err
		}
		root = lease.root
		if err := lease.withCurrent(loadRows); err != nil {
			return nil, err
		}
	}
	trusted := make([]agentcore.Session, 0, len(rows))
	for _, row := range rows {
		if row.Owner == nil || row.Owner.Domain != lifecycleOwnerDomain(row.Kind) ||
			row.Owner.RuntimeID != "" || row.Owner.WorkspaceFingerprint == "" ||
			row.Owner.Incarnation == l.incarnation ||
			(row.Owner.WorkspaceGeneration == 0) != (currentGeneration == 0) {
			continue
		}
		expected := l.workspaceOwnerFingerprint(root, row.ID, row.Kind, *row.Owner)
		if expected == "" || !hmac.Equal([]byte(row.Owner.WorkspaceFingerprint), []byte(expected)) {
			continue
		}
		if mapped := l.runtimeSessionID(row.Kind, row.ID); mapped != row.ID || l.runtime.IsSessionRegistered(row.ID) {
			continue
		}
		trusted = append(trusted, row)
	}
	return trusted, nil
}

func (l *AgentLifecycle) recoveryHandle(row agentcore.Session) (string, error) {
	if len(l.workspaceIdentityKey) == 0 || row.Owner == nil {
		return "", ErrNotAllowed
	}
	claim := struct {
		Version              int                   `json:"version"`
		ID                   string                `json:"id"`
		Kind                 agentcore.SessionKind `json:"kind"`
		OwnerDomain          string                `json:"ownerDomain"`
		OwnerIncarnation     string                `json:"ownerIncarnation"`
		WorkspaceGeneration  uint64                `json:"workspaceGeneration"`
		WorkspaceFingerprint string                `json:"workspaceFingerprint"`
	}{
		Version: 1, ID: row.ID, Kind: row.Kind,
		OwnerDomain: row.Owner.Domain, OwnerIncarnation: row.Owner.Incarnation,
		WorkspaceGeneration:  row.Owner.WorkspaceGeneration,
		WorkspaceFingerprint: row.Owner.WorkspaceFingerprint,
	}
	encoded, err := json.Marshal(claim)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, l.workspaceIdentityKey)
	_, _ = mac.Write([]byte("koyori-agent-recovery-handle-v1\x00"))
	_, _ = mac.Write(encoded)
	return agentRecoveryHandlePrefix + hex.EncodeToString(mac.Sum(nil)), nil
}

func validAgentRecoveryHandle(handle string) bool {
	if len(handle) != len(agentRecoveryHandlePrefix)+sha256.Size*2 || !strings.HasPrefix(handle, agentRecoveryHandlePrefix) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(handle, agentRecoveryHandlePrefix))
	return err == nil
}

// pendingRecoveryDispositions returns bounded metadata plus the logical ID for
// trusted package-internal tests and orchestration. Public callers receive a
// separate DTO that never contains this ID.
func (l *AgentLifecycle) pendingRecoveryDispositions() ([]agentRecoveryRow, error) {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return nil, err
	}
	defer workspaceLease.release()
	if l == nil {
		return nil, fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	rows, err := l.recoveryRowsLocked(false)
	if err != nil {
		return nil, err
	}
	entries := make([]agentRecoveryRow, 0, len(rows))
	for _, row := range rows {
		handle, err := l.recoveryHandle(row)
		if err != nil {
			return nil, err
		}
		entries = append(entries, agentRecoveryRow{
			ID: row.ID, Handle: handle, Kind: row.Kind, Status: row.Status,
			OwnerDomain: row.Owner.Domain, WorkspaceGeneration: row.Owner.WorkspaceGeneration,
			StartedAt: row.StartedAt, UpdatedAt: row.UpdatedAt,
			CheckpointCount: len(row.Checkpoints),
		})
	}
	return entries, nil
}

// PendingRecoveryDispositions exposes the content-free recovery inventory to
// trusted Go callers. AgentLifecycle is not registered with Wails.
//
//wails:ignore
func (l *AgentLifecycle) PendingRecoveryDispositions() ([]AgentRecoveryEntry, error) {
	rows, err := l.pendingRecoveryDispositions()
	if err != nil {
		return nil, publicAgentRecoveryError(err)
	}
	entries := make([]AgentRecoveryEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, AgentRecoveryEntry{
			Handle: row.Handle, Kind: string(row.Kind), Status: string(row.Status),
			StartedAt: row.StartedAt, UpdatedAt: row.UpdatedAt,
			CheckpointCount: row.CheckpointCount,
		})
	}
	return entries, nil
}

// DispatchRecoveryDisposition applies one explicit terminal decision without
// restoring runtime authority. Only discard is currently valid; accepting a
// renderer-shaped resume request here would turn an old session ID into a
// bearer capability after restart.
//
//wails:ignore
func (l *AgentLifecycle) DispatchRecoveryDisposition(request AgentRecoveryDispositionRequest) (AgentRecoveryDispositionResult, error) {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return AgentRecoveryDispositionResult{}, publicAgentRecoveryError(err)
	}
	defer workspaceLease.release()
	if l == nil || request.Disposition != string(agentcore.RecoveryDispositionDiscard) || !validAgentRecoveryHandle(request.Handle) {
		return AgentRecoveryDispositionResult{}, ErrInvalidInput
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	rows, err := l.recoveryRowsLocked(true)
	if err != nil {
		return AgentRecoveryDispositionResult{}, publicAgentRecoveryError(err)
	}
	var matched *agentcore.Session
	for index := range rows {
		handle, handleErr := l.recoveryHandle(rows[index])
		if handleErr != nil {
			return AgentRecoveryDispositionResult{}, publicAgentRecoveryError(handleErr)
		}
		if hmac.Equal([]byte(handle), []byte(request.Handle)) {
			matched = &rows[index]
			break
		}
	}
	if matched == nil {
		return AgentRecoveryDispositionResult{}, ErrNotAllowed
	}
	disposed, err := l.applyRecoveryDispositionLocked(matched.Kind, matched.ID, agentcore.RecoveryDispositionDiscard)
	if err != nil {
		return AgentRecoveryDispositionResult{}, publicAgentRecoveryError(err)
	}
	if disposed.CompletedAt == nil || disposed.Status != agentcore.SessionCompleted ||
		disposed.Recovery != agentcore.SessionRecoveryNone ||
		disposed.RecoveryDisposition != agentcore.RecoveryDispositionDiscard {
		return AgentRecoveryDispositionResult{}, ErrNotAllowed
	}
	return AgentRecoveryDispositionResult{
		Handle: request.Handle, Kind: string(disposed.Kind), Status: string(disposed.Status),
		Disposition: string(disposed.RecoveryDisposition), CompletedAt: *disposed.CompletedAt,
	}, nil
}

func publicAgentRecoveryError(err error) error {
	switch {
	case errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate),
		errors.Is(err, agentcore.ErrSessionPersistencePoisoned):
		return ErrAgentRecoveryPersistenceIndeterminate
	case errors.Is(err, ErrAgentRecoveryPersistence):
		return ErrAgentRecoveryPersistence
	default:
		return ErrNotAllowed
	}
}

// applyRecoveryDisposition applies a trusted, terminal-only decision to one
// restart orphan. It never registers or activates a runtime session. Resume
// remains unavailable until a domain-specific recovery path can prove current
// workspace and caller ownership.
func (l *AgentLifecycle) applyRecoveryDisposition(kind agentcore.SessionKind, id string, disposition agentcore.RecoveryDisposition) (agentcore.Session, error) {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return agentcore.Session{}, err
	}
	defer workspaceLease.release()
	if l == nil || l.sessions == nil || l.runtime == nil {
		return agentcore.Session{}, fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	id = strings.TrimSpace(id)
	if id == "" || lifecycleSessionID(kind, id) != id {
		return agentcore.Session{}, fmt.Errorf("canonical lifecycle session ID is required: %w", ErrInvalidInput)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	return l.applyRecoveryDispositionLocked(kind, id, disposition)
}

func (l *AgentLifecycle) applyRecoveryDispositionLocked(kind agentcore.SessionKind, id string, disposition agentcore.RecoveryDisposition) (agentcore.Session, error) {
	session, err := l.sessions.Get(id)
	if err != nil {
		return agentcore.Session{}, err
	}
	if session.Kind != kind {
		return agentcore.Session{}, fmt.Errorf("session %q belongs to %s, not %s: %w", id, session.Kind, kind, ErrNotAllowed)
	}
	currentGeneration := uint64(0)
	if l.workspaceGeneration != nil {
		currentGeneration = l.workspaceGeneration()
	}
	if session.Owner == nil || session.Owner.Domain != lifecycleOwnerDomain(kind) || session.Owner.RuntimeID != "" ||
		session.Owner.WorkspaceFingerprint == "" || session.Owner.Incarnation == l.incarnation ||
		(session.Owner.WorkspaceGeneration == 0) != (currentGeneration == 0) {
		return agentcore.Session{}, fmt.Errorf("session %q has no trusted restart owner: %w", id, ErrNotAllowed)
	}
	if mapped := l.runtimeSessionID(kind, id); mapped != id || l.runtime.IsSessionRegistered(id) {
		return agentcore.Session{}, fmt.Errorf("session %q still has runtime authority: %w", id, ErrNotAllowed)
	}
	root := ""
	var lease workspaceLease
	if currentGeneration != 0 {
		if l.workspaceLease == nil {
			return agentcore.Session{}, fmt.Errorf("workspace lease is unavailable: %w", ErrNotAllowed)
		}
		lease, err = l.workspaceLease()
		if err != nil {
			return agentcore.Session{}, err
		}
		root = lease.root
	}
	expected := l.workspaceOwnerFingerprint(root, session.ID, session.Kind, *session.Owner)
	if expected == "" || !hmac.Equal([]byte(session.Owner.WorkspaceFingerprint), []byte(expected)) {
		return agentcore.Session{}, fmt.Errorf("session %q workspace owner claim is invalid: %w", id, ErrNotAllowed)
	}
	if currentGeneration == 0 {
		disposed, dispositionErr := l.sessions.ApplyRecoveryDisposition(id, disposition)
		if dispositionErr != nil {
			return agentcore.Session{}, errors.Join(ErrAgentRecoveryPersistence, dispositionErr)
		}
		return disposed, nil
	}
	var disposed agentcore.Session
	var dispositionErr error
	if err := lease.withCurrent(func() error {
		disposed, dispositionErr = l.sessions.ApplyRecoveryDisposition(id, disposition)
		return dispositionErr
	}); err != nil {
		if dispositionErr != nil {
			return agentcore.Session{}, errors.Join(ErrAgentRecoveryPersistence, dispositionErr)
		}
		return agentcore.Session{}, err
	}
	return disposed, nil
}

func (l *AgentLifecycle) Append(kind agentcore.SessionKind, id string, event agentcore.StreamEventInput) error {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return err
	}
	defer workspaceLease.release()
	return l.appendWithinWorkspaceAuthority(kind, id, event)
}

func (l *AgentLifecycle) appendWithinWorkspaceAuthority(kind agentcore.SessionKind, id string, event agentcore.StreamEventInput) error {
	if l == nil || l.sessions == nil {
		return fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	logicalID := lifecycleSessionID(kind, id)
	runtimeID := l.runtimeSessionID(kind, logicalID)
	_, err := l.sessions.AppendStream(logicalID, event)
	if lifecyclePersistenceIsIndeterminate(err) {
		l.revokeRuntimeAuthority(logicalID, runtimeID)
	}
	return err
}

func (l *AgentLifecycle) Checkpoint(kind agentcore.SessionKind, id, label string, state interface{}) (agentcore.SessionCheckpoint, error) {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return agentcore.SessionCheckpoint{}, err
	}
	defer workspaceLease.release()
	return l.checkpointWithinWorkspaceAuthority(kind, id, label, state)
}

func (l *AgentLifecycle) checkpointWithinWorkspaceAuthority(kind agentcore.SessionKind, id, label string, state interface{}) (agentcore.SessionCheckpoint, error) {
	if l == nil || l.sessions == nil {
		return agentcore.SessionCheckpoint{}, fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return agentcore.SessionCheckpoint{}, fmt.Errorf("encode lifecycle checkpoint: %w", err)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	logicalID := lifecycleSessionID(kind, id)
	runtimeID := l.runtimeSessionID(kind, logicalID)
	checkpoint, err := l.sessions.CreateCheckpoint(logicalID, agentcore.CheckpointInput{
		Label: label,
		State: data,
	})
	if lifecyclePersistenceIsIndeterminate(err) {
		l.revokeRuntimeAuthority(logicalID, runtimeID)
	}
	return checkpoint, err
}

func (l *AgentLifecycle) Pause(kind agentcore.SessionKind, id string) error {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return err
	}
	defer workspaceLease.release()
	return l.pauseWithinWorkspaceAuthority(kind, id)
}

func (l *AgentLifecycle) pauseWithinWorkspaceAuthority(kind agentcore.SessionKind, id string) error {
	if l == nil || l.sessions == nil {
		return fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	return l.pauseLocked(kind, id)
}

func (l *AgentLifecycle) pauseLocked(kind agentcore.SessionKind, id string) error {
	sessionID := lifecycleSessionID(kind, id)
	runtimeID := l.runtimeSessionID(kind, id)
	registered := l.runtime != nil && l.runtime.IsSessionRegistered(runtimeID)
	activeBefore := registered && l.runtime.IsSessionActive(runtimeID)
	if activeBefore {
		if err := l.runtime.SuspendSession(runtimeID); err != nil {
			return err
		}
	}
	if err := l.sessions.Pause(sessionID); err != nil {
		if lifecyclePersistenceIsIndeterminate(err) {
			l.revokeRuntimeAuthority(sessionID, runtimeID)
		} else if activeBefore && shouldRestoreRuntimeAfterLifecycleError(err) {
			_ = l.runtime.ActivateSession(runtimeID)
		}
		return err
	}
	return nil
}

func (l *AgentLifecycle) Fail(kind agentcore.SessionKind, id string, failure error) error {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return err
	}
	defer workspaceLease.release()
	return l.failWithinWorkspaceAuthority(kind, id, failure)
}

func (l *AgentLifecycle) failWithinWorkspaceAuthority(kind agentcore.SessionKind, id string, failure error) error {
	if l == nil || l.sessions == nil {
		return fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	return l.failLocked(kind, id, failure)
}

func (l *AgentLifecycle) failLocked(kind agentcore.SessionKind, id string, failure error) error {
	sessionID := lifecycleSessionID(kind, id)
	runtimeID := l.runtimeSessionID(kind, id)
	registered := l.runtime != nil && l.runtime.IsSessionRegistered(runtimeID)
	activeBefore := registered && l.runtime.IsSessionActive(runtimeID)
	if activeBefore {
		if err := l.runtime.SuspendSession(runtimeID); err != nil {
			return err
		}
	}
	if err := l.sessions.Fail(sessionID, failure); err != nil {
		if lifecyclePersistenceIsIndeterminate(err) {
			l.revokeRuntimeAuthority(sessionID, runtimeID)
		} else if activeBefore && shouldRestoreRuntimeAfterLifecycleError(err) {
			_ = l.runtime.ActivateSession(runtimeID)
		}
		return err
	}
	return nil
}

func (l *AgentLifecycle) ResumeLatest(kind agentcore.SessionKind, id string) error {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return err
	}
	defer workspaceLease.release()
	return l.resumeLatestWithinWorkspaceAuthority(kind, id)
}

func (l *AgentLifecycle) resumeLatestWithinWorkspaceAuthority(kind agentcore.SessionKind, id string) error {
	if l == nil || l.sessions == nil {
		return fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	return l.resumeLatestLocked(kind, id)
}

func (l *AgentLifecycle) resumeLatestLocked(kind agentcore.SessionKind, id string) error {
	session, err := l.Get(kind, id)
	if err != nil {
		return err
	}
	if session.Recovery == agentcore.SessionRecoveryRequired {
		return fmt.Errorf("session %q: %w", session.ID, agentcore.ErrSessionRecoveryRequired)
	}
	if session.Status == agentcore.SessionRunning {
		return nil
	}
	if len(session.Checkpoints) == 0 {
		return fmt.Errorf("session %q has no checkpoint: %w", session.ID, agentcore.ErrInvalidCheckpoint)
	}
	checkpointID := session.Checkpoints[len(session.Checkpoints)-1].ID
	runtimeID := l.runtimeSessionID(session.Kind, session.ID)
	registered := l.runtime != nil && l.runtime.IsSessionRegistered(runtimeID)
	activeBefore := registered && l.runtime.IsSessionActive(runtimeID)
	if !registered {
		if (session.Kind == agentcore.SessionPlan || session.Kind == agentcore.SessionGoal) && runtimeID == session.ID {
			// A plan/goal row without an owner mapping may have been created by a
			// stale or forged domain ID. Never promote it to runtime authority.
			return fmt.Errorf("session %q has no backend runtime owner: %w", session.ID, agentcore.ErrUnknownSession)
		}
	} else if !l.hasCurrentRuntimeOwner(session, runtimeID) {
		return fmt.Errorf("session %q has mismatched durable owner metadata: %w", session.ID, ErrNotAllowed)
	}
	if activeBefore {
		if err := l.runtime.SuspendSession(runtimeID); err != nil {
			return err
		}
	}
	_, err = l.sessions.Resume(session.ID, checkpointID)
	if err != nil {
		if lifecyclePersistenceIsIndeterminate(err) {
			l.revokeRuntimeAuthority(session.ID, runtimeID)
		} else if activeBefore && shouldRestoreRuntimeAfterLifecycleError(err) {
			if restoreErr := l.runtime.ActivateSession(runtimeID); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore runtime after rejected resume: %w", restoreErr))
			}
		}
		return err
	}
	if !registered {
		err = l.runtime.RegisterSession(runtimeID)
	} else {
		err = l.runtime.ActivateSession(runtimeID)
	}
	if err == nil {
		return nil
	}
	compensationErr := l.sessions.Pause(session.ID)
	if registered {
		_ = l.runtime.SuspendSession(runtimeID)
	}
	if compensationErr != nil {
		return errors.Join(err, fmt.Errorf("restore paused lifecycle after runtime activation failure: %w", compensationErr))
	}
	return err
}

func (l *AgentLifecycle) Complete(kind agentcore.SessionKind, id string) error {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return err
	}
	defer workspaceLease.release()
	return l.completeWithinWorkspaceAuthority(kind, id)
}

func (l *AgentLifecycle) completeWithinWorkspaceAuthority(kind agentcore.SessionKind, id string) error {
	if l == nil || l.sessions == nil {
		return fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	return l.completeLocked(kind, id)
}

func (l *AgentLifecycle) completeLocked(kind agentcore.SessionKind, id string) error {
	sessionID := lifecycleSessionID(kind, id)
	previous, err := l.sessions.Get(sessionID)
	if err != nil {
		return err
	}
	runtimeID := l.runtimeSessionID(kind, id)
	registered := l.runtime != nil && l.runtime.IsSessionRegistered(runtimeID)
	activeBefore := registered && l.runtime.IsSessionActive(runtimeID)
	if registered {
		// Revoke before publishing the terminal state. Any capability issued by
		// a concurrent request is therefore burned at redemption even if the
		// caller races this transition.
		if err := l.runtime.UnregisterSession(runtimeID); err != nil {
			return err
		}
	}
	if err := l.sessions.Complete(sessionID); err != nil {
		if registered && previous.Status != agentcore.SessionCompleted && shouldRestoreRuntimeAfterLifecycleError(err) {
			restoreErr := l.runtime.RegisterSession(runtimeID)
			if restoreErr == nil && !activeBefore {
				restoreErr = l.runtime.SuspendSession(runtimeID)
			}
			if restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore runtime session after rejected completion: %w", restoreErr))
			}
		}
		if lifecyclePersistenceIsIndeterminate(err) {
			l.unbindRuntimeSession(sessionID)
		}
		return err
	}
	l.unbindRuntimeSession(sessionID)
	return nil
}

// CloseByID atomically revokes a backend-owned runtime namespace and, when it
// is still running, publishes the corresponding completed lifecycle state.
func (l *AgentLifecycle) CloseByID(id string) error {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return err
	}
	defer workspaceLease.release()
	return l.closeByIDWithinWorkspaceAuthority(id)
}

func (l *AgentLifecycle) closeByIDWithinWorkspaceAuthority(id string) error {
	if l == nil || l.sessions == nil || l.runtime == nil {
		return fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("session ID is required: %w", ErrInvalidInput)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	logicalID := id
	session, err := l.sessions.Get(logicalID)
	if errors.Is(err, agentcore.ErrSessionNotFound) {
		if mapped := l.logicalSessionForRuntime(id); mapped != "" {
			logicalID = mapped
			session, err = l.sessions.Get(logicalID)
		}
	}
	if errors.Is(err, agentcore.ErrSessionNotFound) {
		// A bearer ID must never be enough to revoke an arbitrary runtime
		// namespace. Only rows issued through this lifecycle can be closed.
		return fmt.Errorf("session %q has no lifecycle owner: %w", id, agentcore.ErrSessionNotFound)
	}
	if err != nil {
		return err
	}
	if session.Recovery == agentcore.SessionRecoveryRequired {
		return fmt.Errorf("session %q requires trusted recovery disposition: %w", logicalID, agentcore.ErrSessionRecoveryRequired)
	}
	if session.Status != agentcore.SessionCompleted && l.logicalSessionForRuntime(id) == "" && l.runtimeSessionID(session.Kind, logicalID) == logicalID {
		return fmt.Errorf("session %q has no current owner binding: %w", logicalID, ErrNotAllowed)
	}
	if session.Status == agentcore.SessionRunning {
		return l.completeLocked(session.Kind, logicalID)
	}
	runtimeID := l.runtimeSessionID(session.Kind, logicalID)
	registered := l.runtime.IsSessionRegistered(runtimeID)
	activeBefore := registered && l.runtime.IsSessionActive(runtimeID)
	if registered {
		if err := l.runtime.UnregisterSession(runtimeID); err != nil {
			return err
		}
	}
	if err := l.sessions.Close(logicalID); err != nil {
		if registered && shouldRestoreRuntimeAfterLifecycleError(err) {
			restoreErr := l.runtime.RegisterSession(runtimeID)
			if restoreErr == nil && !activeBefore {
				restoreErr = l.runtime.SuspendSession(runtimeID)
			}
			if restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore runtime session after rejected close: %w", restoreErr))
			}
		}
		if lifecyclePersistenceIsIndeterminate(err) {
			l.unbindRuntimeSession(logicalID)
		}
		return err
	}
	l.unbindRuntimeSession(logicalID)
	return nil
}

func (l *AgentLifecycle) Abort(kind agentcore.SessionKind, id string) error {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return err
	}
	defer workspaceLease.release()
	return l.abortWithinWorkspaceAuthority(kind, id)
}

func (l *AgentLifecycle) abortWithinWorkspaceAuthority(kind agentcore.SessionKind, id string) error {
	if l == nil || l.sessions == nil {
		return fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	session, err := l.Get(kind, id)
	if err != nil {
		return err
	}
	if session.Status == agentcore.SessionPaused || session.Status == agentcore.SessionFailed {
		if err := l.resumeLatestLocked(kind, id); err != nil {
			return err
		}
	}
	if session.Status == agentcore.SessionCompleted {
		return nil
	}
	return l.failLocked(kind, id, errors.New("execution aborted"))
}

func (l *AgentLifecycle) SelectContext(items []agentcore.ContextItem, limit int) (agentcore.ContextSelection, error) {
	if l == nil || l.context == nil {
		return agentcore.ContextSelection{}, fmt.Errorf("agent context manager is unavailable: %w", ErrNotAllowed)
	}
	return l.context.Select(items, limit)
}

func (l *AgentLifecycle) estimateMessages(messages []ChatMessage) int {
	items := make([]agentcore.ContextItem, len(messages))
	for index, message := range messages {
		items[index] = agentcore.ContextItem{
			ID:       fmt.Sprintf("message-%d", index),
			Text:     strings.Repeat(" ", 12) + chatMessageBudgetText(message),
			Required: true,
		}
	}
	selection, err := l.SelectContext(items, math.MaxInt)
	if err != nil {
		return 0
	}
	return selection.Tokens
}

func (l *AgentLifecycle) requireMessages(messages []ChatMessage, limit int) (int, error) {
	items := make([]agentcore.ContextItem, len(messages))
	for index, message := range messages {
		items[index] = agentcore.ContextItem{
			ID:       fmt.Sprintf("message-%d", index),
			Text:     strings.Repeat(" ", 12) + chatMessageBudgetText(message),
			Required: true,
		}
	}
	selection, err := l.SelectContext(items, limit)
	if err != nil {
		return 0, err
	}
	return selection.Tokens, nil
}

func (l *AgentLifecycle) estimateText(text string) int {
	if l == nil || l.estimator == nil {
		return 0
	}
	return l.estimator.EstimateTokens(text)
}

// prepareChatMessages is the single production context selection path. It
// preserves the previous ordering contract while delegating every count and
// inclusion decision to agentcore.ContextManager.
func (l *AgentLifecycle) prepareChatMessages(cfg AIConfig, messages []ChatMessage) ([]ChatMessage, int, error) {
	full := withSystemPromptFrom(cfg, messages)
	if len(full) == 0 {
		return full, 0, nil
	}
	budget := contextWindowFrom(cfg)
	headCount := 1
	if len(full) > 1 && full[0].Role == "system" {
		headCount = 2
	}
	items := make([]agentcore.ContextItem, len(full))
	for index, message := range full {
		items[index] = agentcore.ContextItem{
			ID:       fmt.Sprintf("message-%d", index),
			Text:     strings.Repeat(" ", 12) + chatMessageBudgetText(message),
			Required: index < headCount,
			Priority: index,
		}
	}
	selection, err := l.SelectContext(items, budget)
	if err != nil {
		return nil, 0, err
	}
	if len(selection.Dropped) == 0 {
		return full, selection.Tokens, nil
	}

	const placeholderReserve = 20
	selection, err = l.SelectContext(items, budget-placeholderReserve)
	if err != nil {
		return nil, 0, err
	}
	included := make(map[string]bool, len(selection.Included))
	includedIndexes := make(map[int]bool, len(selection.Included))
	for _, item := range selection.Included {
		included[item.ID] = true
	}
	for index := range full {
		if included[fmt.Sprintf("message-%d", index)] {
			includedIndexes[index] = true
		}
	}
	enforceAtomicNativeToolRounds(full, includedIndexes)
	selectedIndexes := make([]int, 0, len(selection.Included))
	for index := range full {
		if includedIndexes[index] {
			selectedIndexes = append(selectedIndexes, index)
		}
	}
	sort.Ints(selectedIndexes)
	result := make([]ChatMessage, 0, len(selectedIndexes)+1)
	placeholderAdded := false
	for _, index := range selectedIndexes {
		if !placeholderAdded && index >= headCount {
			result = append(result, ChatMessage{
				Role: "system",
				Content: fmt.Sprintf(
					"[Note: %d earlier messages were truncated to fit the context window. Ask the user if you need older context.]",
					len(full)-len(selectedIndexes),
				),
			})
			placeholderAdded = true
		}
		result = append(result, full[index])
	}
	if !placeholderAdded {
		result = append(result, ChatMessage{
			Role:    "system",
			Content: fmt.Sprintf("[Note: %d earlier messages were truncated to fit the context window. Ask the user if you need older context.]", len(full)-len(selectedIndexes)),
		})
	}
	return result, l.estimateMessages(result), nil
}

func (l *AgentLifecycle) newUnitID(kind agentcore.UsageUnitKind, completedAt time.Time) string {
	ordinal := l.sequence.Add(1)
	return fmt.Sprintf("%s-%d-%d", kind, completedAt.UnixNano(), ordinal)
}

func (l *AgentLifecycle) Record(record agentcore.UsageRecord) error {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return err
	}
	defer workspaceLease.release()
	return l.recordWithinWorkspaceAuthority(record)
}

func (l *AgentLifecycle) recordWithinWorkspaceAuthority(record agentcore.UsageRecord) error {
	if l == nil || l.runtime == nil {
		return fmt.Errorf("agent usage runtime is unavailable: %w", ErrNotAllowed)
	}
	if record.UnitID == "" {
		record.UnitID = l.newUnitID(record.UnitKind, record.CompletedAt)
	}
	record = l.canonicalUsageSession(record)
	if err := l.ensureTrustedUsageSession(record); err != nil {
		return err
	}
	return l.runtime.RecordUsage(record)
}

// ensureTrustedUsageSession is used only by service-to-service Record calls,
// never by the renderer-facing runtime sink. Workflow adapters may emit their
// first usage row before explicitly calling Begin, but Plan/Goal rows must
// already have an opaque owner mapping established by their domain service.
func (l *AgentLifecycle) ensureTrustedUsageSession(record agentcore.UsageRecord) error {
	if l == nil || l.runtime == nil || record.SessionID == "" {
		return nil
	}
	kind := usageSessionKind(record)
	logicalID := lifecycleSessionID(kind, record.SessionID)
	// Plan/goal usage may carry the opaque runtime owner ID rather than the
	// renderer-visible logical ID. Resolve that trusted backend mapping before
	// looking up durable owner metadata; treating the opaque ID as a new logical
	// session would either reject valid usage or create a parallel owner row.
	if mapped := l.logicalSessionForRuntime(record.SessionID); mapped != "" {
		logicalID = mapped
	}
	runtimeID := l.runtimeSessionID(kind, logicalID)
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	session, sessionErr := l.sessions.Get(logicalID)
	if sessionErr == nil && session.Recovery == agentcore.SessionRecoveryRequired {
		return fmt.Errorf("usage session %q: %w", logicalID, agentcore.ErrSessionRecoveryRequired)
	}
	registered := l.runtime.IsSessionRegistered(runtimeID)
	if runtimeID != logicalID {
		if sessionErr != nil {
			return fmt.Errorf("usage session %q has no durable owner row: %w", logicalID, agentcore.ErrUnknownSession)
		}
		if !l.hasCurrentRuntimeOwner(session, runtimeID) {
			return fmt.Errorf("usage session %q has mismatched durable owner metadata: %w", logicalID, agentcore.ErrUnknownSession)
		}
		if !registered {
			return fmt.Errorf("usage session %q owner runtime is not registered: %w", logicalID, agentcore.ErrUnknownSession)
		}
		return nil
	}
	if sessionErr == nil {
		// A trusted domain may need to record a failed observation after its
		// lifecycle row has already been terminalized (for example, an admitted
		// iteration whose checkpoint failed). This is ledger-only: successful
		// usage and any runtime authority still require a live registration.
		if session.Status == agentcore.SessionCompleted {
			terminalOwnerID := runtimeID
			if session.Owner != nil && session.Owner.RuntimeID != "" {
				// Complete revokes the in-memory opaque mapping. The durable owner
				// claim remains the proof for this trusted failure observation.
				terminalOwnerID = session.Owner.RuntimeID
			}
			if !l.hasCurrentRuntimeOwner(session, terminalOwnerID) {
				return fmt.Errorf("usage session %q has mismatched durable owner metadata: %w", logicalID, agentcore.ErrUnknownSession)
			}
			if record.Success {
				return fmt.Errorf("usage session %q is completed: %w", logicalID, agentcore.ErrInvalidSessionTransition)
			}
			return nil
		}
		if !l.hasCurrentRuntimeOwner(session, runtimeID) {
			return fmt.Errorf("usage session %q has mismatched durable owner metadata: %w", logicalID, agentcore.ErrUnknownSession)
		}
		if !registered {
			return fmt.Errorf("usage session %q owner runtime is not registered: %w", logicalID, agentcore.ErrUnknownSession)
		}
		return nil
	}
	if kind == agentcore.SessionPlan || kind == agentcore.SessionGoal {
		return fmt.Errorf("usage session %q has no backend runtime owner: %w", record.SessionID, agentcore.ErrUnknownSession)
	}
	if !errors.Is(sessionErr, agentcore.ErrSessionNotFound) {
		return sessionErr
	}
	if kind == agentcore.SessionPlan || kind == agentcore.SessionGoal {
		return fmt.Errorf("usage session %q has no backend runtime owner: %w", record.SessionID, agentcore.ErrUnknownSession)
	}
	activeBefore := registered && l.runtime.IsSessionActive(runtimeID)
	if activeBefore {
		if err := l.runtime.SuspendSession(runtimeID); err != nil {
			return err
		}
	}
	permissionMode := l.configuredPermissionMode()
	if _, err := l.beginOwnedSession(logicalID, kind, runtimeID, permissionMode); err != nil {
		if lifecyclePersistenceIsIndeterminate(err) {
			l.revokeRuntimeAuthority(logicalID, runtimeID)
		} else if activeBefore {
			_ = l.runtime.ActivateSession(runtimeID)
		}
		return err
	}
	if !registered {
		if err := l.runtime.RegisterSession(runtimeID); err != nil {
			deleteErr := l.sessions.Delete(logicalID)
			if lifecyclePersistenceIsIndeterminate(deleteErr) {
				l.revokeRuntimeAuthority(logicalID, runtimeID)
			}
			return errors.Join(err, deleteErr)
		}
	} else if activeBefore {
		if err := l.runtime.ActivateSession(runtimeID); err != nil {
			l.revokeRuntimeAuthority(logicalID, runtimeID)
			return err
		}
	}
	l.bindRuntimeSession(logicalID, runtimeID)
	return nil
}

// RecordUsage implements agentcore.UsageSink. Tool executions may originate
// before a domain service explicitly begins a session (notably workflow ToolDef
// calls), so the sink attaches those units to the same SessionStore first.
// A unit result is not a session terminal signal: multi-step owners explicitly
// complete or fail their session after applying retry and branching semantics.
func (l *AgentLifecycle) RecordUsage(record agentcore.UsageRecord) error {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return err
	}
	defer workspaceLease.release()
	return l.recordUsageWithinWorkspaceAuthority(record)
}

func (l *AgentLifecycle) recordUsageWithinWorkspaceAuthority(record agentcore.UsageRecord) error {
	if l == nil || l.meter == nil {
		return fmt.Errorf("agent lifecycle meter is unavailable")
	}
	record = l.canonicalUsageSession(record)
	if err := l.ensureTrustedUsageSession(record); err != nil {
		return err
	}
	if err := l.meter.RecordUsage(record); err != nil {
		return err
	}
	// Only publish stream/checkpoint observations after the ledger accepts the
	// row. A failed disk write must not look like a successfully metered unit.
	return l.observeUsageSession(record)
}

func (l *AgentLifecycle) BeginUsage(record agentcore.UsageRecord) (agentcore.UsageReceipt, error) {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return agentcore.UsageReceipt{}, err
	}
	defer workspaceLease.release()
	return l.beginUsageWithinWorkspaceAuthority(record)
}

func (l *AgentLifecycle) beginUsageWithinWorkspaceAuthority(record agentcore.UsageRecord) (agentcore.UsageReceipt, error) {
	if l == nil || l.meter == nil {
		return agentcore.UsageReceipt{}, fmt.Errorf("agent lifecycle meter is unavailable")
	}
	record = l.canonicalUsageSession(record)
	if err := l.ensureTrustedUsageSession(record); err != nil {
		return agentcore.UsageReceipt{}, err
	}
	transaction, ok := l.meter.(agentcore.UsageTransactionSink)
	if !ok {
		return agentcore.UsageReceipt{}, fmt.Errorf("agent lifecycle meter lacks transaction support: %w", agentcore.ErrMeterContract)
	}
	return transaction.BeginUsage(record)
}

func (l *AgentLifecycle) CompleteUsage(receipt agentcore.UsageReceipt, record agentcore.UsageRecord) error {
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return err
	}
	defer workspaceLease.release()
	return l.completeUsageWithinWorkspaceAuthority(receipt, record)
}

func (l *AgentLifecycle) completeUsageWithinWorkspaceAuthority(receipt agentcore.UsageReceipt, record agentcore.UsageRecord) error {
	if l == nil || l.meter == nil {
		return fmt.Errorf("agent lifecycle meter is unavailable")
	}
	record = l.canonicalUsageSession(record)
	if err := l.ensureTrustedUsageSession(record); err != nil {
		return err
	}
	transaction, ok := l.meter.(agentcore.UsageTransactionSink)
	if !ok {
		return fmt.Errorf("agent lifecycle meter lacks transaction support: %w", agentcore.ErrMeterContract)
	}
	if err := transaction.CompleteUsage(receipt, record); err != nil {
		return err
	}
	return l.observeUsageSession(record)
}

// canonicalUsageSession persists the logical lifecycle identity rather than
// a process-local opaque runtime ID. This keeps a pending receipt attributable
// after restart without ever restoring the old runtime authority.
func (l *AgentLifecycle) canonicalUsageSession(record agentcore.UsageRecord) agentcore.UsageRecord {
	if l == nil || record.SessionID == "" {
		return record
	}
	if logicalID := l.logicalSessionForRuntime(record.SessionID); logicalID != "" {
		record.SessionID = logicalID
	}
	return record
}

func (l *AgentLifecycle) observeUsageSession(record agentcore.UsageRecord) error {
	if record.SessionID == "" || l.sessions == nil {
		return nil
	}
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	kind := usageSessionKind(record)
	logicalID := lifecycleSessionID(kind, record.SessionID)
	runtimeID := l.runtimeSessionID(kind, logicalID)
	if mapped := l.logicalSessionForRuntime(record.SessionID); mapped != "" {
		logicalID = mapped
		runtimeID = record.SessionID
	}
	if l.runtime != nil && !l.runtime.IsSessionRegistered(runtimeID) {
		// A usage row is observational data, not an authority grant. Do not
		// register an arbitrary renderer/domain ID merely because a provider
		// reported usage for it.
		return nil
	}
	session, err := l.sessions.Get(logicalID)
	if err != nil || session.Status != agentcore.SessionRunning {
		return nil
	}
	// Chat and inline-AI units already have their own response/checkpoint
	// lifecycle. Recording their meter row must not masquerade as a tool event
	// in the user-visible stream; orchestration/tool units retain the shared
	// observation checkpoint below.
	if record.UnitKind == agentcore.UsageUnitChat || record.UnitKind == agentcore.UsageUnitAI {
		return nil
	}
	err = l.sessions.AppendUsageObservation(logicalID, record.UnitID, record.Operation, record.Success)
	if lifecyclePersistenceIsIndeterminate(err) {
		l.revokeRuntimeAuthority(logicalID, runtimeID)
	}
	return err
}

func usageSessionKind(record agentcore.UsageRecord) agentcore.SessionKind {
	switch {
	case strings.HasPrefix(record.SessionID, "workflow:") || record.UnitKind == agentcore.UsageUnitWorkflow:
		return agentcore.SessionWorkflow
	case strings.HasPrefix(record.SessionID, "plan:") || record.SessionID == "plan-step" || record.UnitKind == agentcore.UsageUnitPlan:
		return agentcore.SessionPlan
	case strings.HasPrefix(record.SessionID, "goal:") || strings.HasPrefix(record.SessionID, "goal-") || record.UnitKind == agentcore.UsageUnitGoal:
		return agentcore.SessionGoal
	default:
		return agentcore.SessionChat
	}
}

func (s *AIPlanService) checkpointPlanExecution(planID string, stepIndex int, phase string) error {
	if s.lifecycle == nil {
		return nil
	}
	_, err := s.lifecycle.Checkpoint(agentcore.SessionPlan, planID, phase, map[string]interface{}{
		"phase": phase,
		"step":  stepIndex,
	})
	return err
}

func (s *AIPlanService) beginPlanExecution(planID, operation string, startedAt time.Time) (agentcore.UsageReceipt, error) {
	if s.lifecycle == nil {
		return agentcore.UsageReceipt{}, nil
	}
	return s.lifecycle.BeginUsage(agentcore.UsageRecord{
		UnitID:    s.lifecycle.newUnitID(agentcore.UsageUnitPlan, startedAt),
		SessionID: lifecycleSessionID(agentcore.SessionPlan, planID),
		UnitKind:  agentcore.UsageUnitPlan, Operation: operation,
		CostBasis: agentcore.CostNotApplicable,
		StartedAt: startedAt, CompletedAt: startedAt, Pending: true,
	})
}

func (s *AIPlanService) completePlanExecution(receipt agentcore.UsageReceipt, planID, operation string, startedAt, completedAt time.Time, executionErr error) error {
	if s.lifecycle == nil {
		return nil
	}
	record := agentcore.UsageRecord{
		UnitID:    receipt.UnitID,
		SessionID: lifecycleSessionID(agentcore.SessionPlan, planID),
		UnitKind:  agentcore.UsageUnitPlan, Operation: operation,
		CostBasis: agentcore.CostNotApplicable,
		StartedAt: startedAt, CompletedAt: completedAt, Success: executionErr == nil,
	}
	if executionErr != nil {
		record.Error = "execution failed"
	}
	if receipt.UnitID == "" {
		return s.lifecycle.Record(record)
	}
	return s.lifecycle.CompleteUsage(receipt, record)
}

func (s *AIGoalService) checkpointGoalExecution(goalID, phase string, iteration int) error {
	if s.lifecycle == nil {
		return nil
	}
	_, err := s.lifecycle.Checkpoint(agentcore.SessionGoal, goalID, phase, map[string]interface{}{
		"phase":     phase,
		"iteration": iteration,
	})
	return err
}

func (s *AIGoalService) beginGoalExecution(goalID string, iteration int, startedAt time.Time) (agentcore.UsageReceipt, error) {
	if s.lifecycle == nil {
		return agentcore.UsageReceipt{}, nil
	}
	return s.lifecycle.BeginUsage(agentcore.UsageRecord{
		UnitID:    s.lifecycle.newUnitID(agentcore.UsageUnitGoal, startedAt),
		SessionID: lifecycleSessionID(agentcore.SessionGoal, goalID),
		UnitKind:  agentcore.UsageUnitGoal,
		Operation: fmt.Sprintf("goal.iteration.%d", iteration),
		CostBasis: agentcore.CostNotApplicable,
		StartedAt: startedAt, CompletedAt: startedAt, Pending: true,
	})
}

func (s *AIGoalService) recordGoalExecution(goalID string, iteration int, result GoalRoundResult, startedAt, completedAt time.Time, executionErr error) error {
	if s.lifecycle == nil {
		return nil
	}
	basis := agentcore.CostNotApplicable
	estimated := false
	if result.Cost > 0 || result.Tokens > 0 {
		// GoalExecutor predates provider usage provenance. Its aggregate values
		// are estimates until a provider adapter explicitly reports otherwise.
		basis = agentcore.CostEstimated
		estimated = true
	}
	record := agentcore.UsageRecord{
		SessionID: lifecycleSessionID(agentcore.SessionGoal, goalID),
		UnitKind:  agentcore.UsageUnitGoal,
		Operation: fmt.Sprintf("goal.iteration.%d", iteration),
		TokensOut: result.Tokens, Cost: result.Cost, Currency: func() string {
			if result.Cost > 0 {
				return "USD"
			}
			return ""
		}(),
		CostBasis: basis, Estimated: estimated,
		StartedAt: startedAt, CompletedAt: completedAt, Success: executionErr == nil,
	}
	if executionErr != nil {
		record.Error = "execution failed"
	}
	return s.lifecycle.Record(record)
}

func (s *AIGoalService) completeGoalExecution(receipt agentcore.UsageReceipt, goalID string, iteration int, result GoalRoundResult, startedAt, completedAt time.Time, executionErr error) error {
	if s.lifecycle == nil {
		return nil
	}
	basis := agentcore.CostNotApplicable
	estimated := false
	if result.Cost > 0 || result.Tokens > 0 {
		basis = agentcore.CostEstimated
		estimated = true
	}
	record := agentcore.UsageRecord{
		UnitID:    receipt.UnitID,
		SessionID: lifecycleSessionID(agentcore.SessionGoal, goalID),
		UnitKind:  agentcore.UsageUnitGoal,
		Operation: fmt.Sprintf("goal.iteration.%d", iteration),
		TokensOut: result.Tokens, Cost: result.Cost, Currency: func() string {
			if result.Cost > 0 {
				return "USD"
			}
			return ""
		}(),
		CostBasis: basis, Estimated: estimated,
		StartedAt: startedAt, CompletedAt: completedAt, Success: executionErr == nil,
	}
	if executionErr != nil {
		record.Error = "execution failed"
	}
	if receipt.UnitID == "" {
		return s.lifecycle.Record(record)
	}
	return s.lifecycle.CompleteUsage(receipt, record)
}
