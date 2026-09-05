package agentcore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var (
	ErrSessionNotFound                 = errors.New("agent execution session not found")
	ErrSessionExists                   = errors.New("agent execution session already exists")
	ErrInvalidSessionTransition        = errors.New("invalid agent session transition")
	ErrInvalidCheckpoint               = errors.New("invalid agent session checkpoint")
	ErrSessionRecoveryRequired         = errors.New("agent execution session requires trusted recovery")
	ErrContextBudgetExceeded           = errors.New("agent context budget exceeded")
	ErrInvalidUsageRecord              = errors.New("invalid agent usage record")
	ErrInvalidRecoveryDisposition      = errors.New("invalid agent recovery disposition")
	ErrSessionPersistenceIndeterminate = errors.New("agent session snapshot was published but durability is indeterminate")
	ErrSessionPersistencePoisoned      = errors.New("agent session persistence is poisoned")
)

type SessionKind string

const (
	SessionChat     SessionKind = "chat"
	SessionPlan     SessionKind = "plan"
	SessionGoal     SessionKind = "goal"
	SessionWorkflow SessionKind = "workflow"
)

// SessionPermissionMode is the only user-facing approval intent. Runtime
// safety checks remain authoritative regardless of this preference.
type SessionPermissionMode string

const (
	SessionPermissionAlwaysAsk SessionPermissionMode = "always-ask"
	SessionPermissionAssist    SessionPermissionMode = "assist"
	SessionPermissionAllowAll  SessionPermissionMode = "allow-all"
)

func (mode SessionPermissionMode) Valid() bool {
	switch mode {
	case SessionPermissionAlwaysAsk, SessionPermissionAssist, SessionPermissionAllowAll:
		return true
	default:
		return false
	}
}

type SessionStatus string

const (
	SessionRunning   SessionStatus = "running"
	SessionPaused    SessionStatus = "paused"
	SessionFailed    SessionStatus = "failed"
	SessionCompleted SessionStatus = "completed"
)

// SessionRecoveryState records whether a durable session was observed from a
// previous process incarnation. Such a row remains available for diagnostics,
// but it must be explicitly rebound by trusted domain wiring before it can
// resume or receive capabilities.
type SessionRecoveryState string

const (
	SessionRecoveryNone     SessionRecoveryState = ""
	SessionRecoveryRequired SessionRecoveryState = "recovery-required"
)

// RecoveryDisposition is an explicit trusted decision for a durable row that
// was marked recovery-required after a process restart. Renderer input must
// never be allowed to manufacture this transition.
type RecoveryDisposition string

const (
	RecoveryDispositionDiscard RecoveryDisposition = "discard"
)

// SessionOwner is persisted as metadata only. RuntimeID is never restored as
// an authority: a new process must mint a fresh runtime namespace and call
// BindOwner after validating the domain owner and current workspace.
type SessionOwner struct {
	Domain               string `json:"domain"`
	RuntimeID            string `json:"runtimeId,omitempty"`
	WorkspaceGeneration  uint64 `json:"workspaceGeneration"`
	WorkspaceFingerprint string `json:"workspaceFingerprint,omitempty"`
	Incarnation          string `json:"incarnation"`
}

type StreamEventKind string

const (
	StreamDelta       StreamEventKind = "delta"
	StreamToolRequest StreamEventKind = "tool-request"
	StreamToolResult  StreamEventKind = "tool-result"
	StreamStatus      StreamEventKind = "status"
)

type StreamEventInput struct {
	Kind StreamEventKind `json:"kind"`
	Data string          `json:"data"`
}

type StreamEvent struct {
	Sequence  uint64          `json:"sequence"`
	Kind      StreamEventKind `json:"kind"`
	Data      string          `json:"data"`
	Timestamp time.Time       `json:"timestamp"`
}

type CheckpointInput struct {
	Label string          `json:"label"`
	State json.RawMessage `json:"state"`
}

type SessionCheckpoint struct {
	ID             string          `json:"id"`
	Label          string          `json:"label"`
	State          json.RawMessage `json:"state"`
	StreamSequence uint64          `json:"streamSequence"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// Session is the shared lifecycle snapshot used by chat, plan, goal, and
// workflow. Specific services may store domain state inside a checkpoint, but
// they do not redefine pause/fail/resume semantics.
type Session struct {
	ID                  string                `json:"id"`
	Kind                SessionKind           `json:"kind"`
	PermissionMode      SessionPermissionMode `json:"permissionMode"`
	Status              SessionStatus         `json:"status"`
	Recovery            SessionRecoveryState  `json:"recovery,omitempty"`
	RecoveryDisposition RecoveryDisposition   `json:"recoveryDisposition,omitempty"`
	Owner               *SessionOwner         `json:"owner,omitempty"`
	Attempt             uint64                `json:"attempt"`
	ResumedFrom         string                `json:"resumedFrom,omitempty"`
	Failure             string                `json:"failure,omitempty"`
	StartedAt           time.Time             `json:"startedAt"`
	UpdatedAt           time.Time             `json:"updatedAt"`
	CompletedAt         *time.Time            `json:"completedAt,omitempty"`
	Stream              []StreamEvent         `json:"stream"`
	Checkpoints         []SessionCheckpoint   `json:"checkpoints"`
}

type SessionStore struct {
	mu                sync.Mutex
	now               func() time.Time
	sessions          map[string]*Session
	persistence       SessionStorePersistence
	persistencePoison error
}

// SessionStoreSnapshot is an opaque, in-process snapshot used by a trusted
// workspace transaction. It is deliberately not serializable or exposed to
// renderer-facing APIs: restoring a lifecycle row is only valid when the
// caller still owns the same transition lock that captured it.
type SessionStoreSnapshot struct {
	rows []Session
}

// PersistenceCommitState distinguishes a failure before publication from a
// failure after the replacement became visible but before its directory entry
// was durably synchronized.
type PersistenceCommitState uint8

const (
	PersistenceNotPublished PersistenceCommitState = iota
	PersistenceDurable
	PersistencePublishedDurabilityUnknown
)

// SessionStorePersistence is intentionally small so the headless core does
// not depend on a filesystem or desktop service. Save must report its exact
// publication boundary; callers may only roll back PersistenceNotPublished.
type SessionStorePersistence interface {
	Load() ([]Session, error)
	Save([]Session) (PersistenceCommitState, error)
}

func NewSessionStore(now func() time.Time) *SessionStore {
	if now == nil {
		now = time.Now
	}
	return &SessionStore{now: now, sessions: make(map[string]*Session)}
}

// NewPersistentSessionStore loads a durable lifecycle snapshot. Non-terminal
// rows are marked recovery-required and their previous owner authority is
// cleared before the snapshot is exposed to callers.
func NewPersistentSessionStore(persistence SessionStorePersistence, now func() time.Time) (*SessionStore, error) {
	if persistence == nil {
		return nil, fmt.Errorf("session persistence is required")
	}
	store := NewSessionStore(now)
	rows, err := persistence.Load()
	if err != nil {
		return nil, err
	}
	store.persistence = persistence
	dirty := false
	for _, row := range rows {
		if row.ID == "" || !validSessionKind(row.Kind) {
			return nil, fmt.Errorf("invalid durable session %q: %w", row.ID, ErrInvalidSessionTransition)
		}
		if row.PermissionMode == "" {
			row.PermissionMode = SessionPermissionAlwaysAsk
			dirty = true
		} else if !row.PermissionMode.Valid() {
			return nil, fmt.Errorf("invalid durable session %q permission mode %q: %w", row.ID, row.PermissionMode, ErrInvalidSessionTransition)
		}
		if _, exists := store.sessions[row.ID]; exists {
			return nil, fmt.Errorf("duplicate durable session %q: %w", row.ID, ErrSessionExists)
		}
		if row.Status != SessionRunning && row.Status != SessionPaused && row.Status != SessionFailed && row.Status != SessionCompleted {
			return nil, fmt.Errorf("invalid durable session %q status %q: %w", row.ID, row.Status, ErrInvalidSessionTransition)
		}
		if row.Owner != nil && (row.Owner.Domain == "" || row.Owner.Incarnation == "") {
			return nil, fmt.Errorf("invalid durable owner for session %q: %w", row.ID, ErrInvalidSessionTransition)
		}
		if row.Owner != nil && row.Owner.WorkspaceFingerprint != "" && !validWorkspaceFingerprint(row.Owner.WorkspaceFingerprint) {
			return nil, fmt.Errorf("invalid durable workspace fingerprint for session %q: %w", row.ID, ErrInvalidSessionTransition)
		}
		if row.Recovery != SessionRecoveryNone && row.Recovery != SessionRecoveryRequired {
			return nil, fmt.Errorf("invalid durable recovery state for session %q: %w", row.ID, ErrInvalidSessionTransition)
		}
		if row.RecoveryDisposition != "" && row.RecoveryDisposition != RecoveryDispositionDiscard {
			return nil, fmt.Errorf("invalid durable recovery disposition for session %q: %w", row.ID, ErrInvalidRecoveryDisposition)
		}
		if row.RecoveryDisposition != "" && (row.Status != SessionCompleted || row.Recovery != SessionRecoveryNone || row.CompletedAt == nil) {
			return nil, fmt.Errorf("incomplete durable recovery disposition for session %q: %w", row.ID, ErrInvalidRecoveryDisposition)
		}
		if len(row.Stream) > 1<<20 || len(row.Checkpoints) > 1<<16 {
			return nil, fmt.Errorf("durable session %q exceeds lifecycle limits", row.ID)
		}
		for index, event := range row.Stream {
			if event.Sequence != uint64(index+1) || !validStreamKind(event.Kind) {
				return nil, fmt.Errorf("invalid durable stream for %q: %w", row.ID, ErrInvalidSessionTransition)
			}
		}
		for _, checkpoint := range row.Checkpoints {
			if checkpoint.ID == "" || checkpoint.StreamSequence > uint64(len(row.Stream)) {
				return nil, fmt.Errorf("invalid durable checkpoint for %q: %w", row.ID, ErrInvalidCheckpoint)
			}
			if _, err := canonicalArguments(checkpoint.State); err != nil {
				return nil, fmt.Errorf("invalid durable checkpoint state for %q: %w", row.ID, ErrInvalidCheckpoint)
			}
		}
		if row.Status != SessionCompleted {
			if row.Recovery != SessionRecoveryRequired || (row.Owner != nil && row.Owner.RuntimeID != "") {
				dirty = true
			}
			row.Recovery = SessionRecoveryRequired
			if row.Owner != nil {
				owner := *row.Owner
				owner.RuntimeID = ""
				row.Owner = &owner
			}
		}
		store.sessions[row.ID] = cloneSessionPtr(&row)
	}
	if dirty {
		if _, err := store.persistLocked(); err != nil {
			return nil, fmt.Errorf("persist recovery markers: %w", err)
		}
	}
	return store, nil
}

func (s *SessionStore) snapshotLocked() []Session {
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := make([]Session, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, cloneSession(s.sessions[id]))
	}
	return rows
}

// CaptureSnapshot takes a complete lifecycle snapshot before a trusted
// workspace transition. A poisoned store cannot participate in a rollback
// transaction because its last publication is already uncertain.
func (s *SessionStore) CaptureSnapshot() (SessionStoreSnapshot, error) {
	if s == nil {
		return SessionStoreSnapshot{}, fmt.Errorf("session store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persistencePoisonErrorLocked(); err != nil {
		return SessionStoreSnapshot{}, err
	}
	return SessionStoreSnapshot{rows: s.snapshotLocked()}, nil
}

func sessionMapFromRows(rows []Session) (map[string]*Session, error) {
	result := make(map[string]*Session, len(rows))
	for index := range rows {
		row := rows[index]
		if row.ID == "" || !validSessionKind(row.Kind) {
			return nil, fmt.Errorf("invalid workspace rollback session %q: %w", row.ID, ErrInvalidSessionTransition)
		}
		if _, exists := result[row.ID]; exists {
			return nil, fmt.Errorf("duplicate workspace rollback session %q: %w", row.ID, ErrSessionExists)
		}
		result[row.ID] = cloneSessionPtr(&row)
	}
	return result, nil
}

// RestoreSnapshot publishes the exact rows captured by CaptureSnapshot. The
// previous in-memory rows are restored only when the replacement was never
// published. A post-publication failure intentionally leaves the store
// poisoned, so callers must revoke runtime authority instead of guessing.
func (s *SessionStore) RestoreSnapshot(snapshot SessionStoreSnapshot) error {
	if s == nil {
		return fmt.Errorf("session store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persistencePoisonErrorLocked(); err != nil {
		return err
	}
	replacement, err := sessionMapFromRows(snapshot.rows)
	if err != nil {
		return err
	}
	previous := s.sessions
	s.sessions = replacement
	if state, persistErr := s.persistLocked(); persistErr != nil {
		if shouldRollbackPersistence(state) {
			s.sessions = previous
		}
		return fmt.Errorf("persist workspace lifecycle rollback: %w", persistErr)
	}
	return nil
}

func (s *SessionStore) persistencePoisonErrorLocked() error {
	if s.persistencePoison == nil {
		return nil
	}
	return errors.Join(ErrSessionPersistencePoisoned, s.persistencePoison)
}

// Poison rejects every later persistence-backed transition in this process.
// Trusted transaction coordinators use it when a compensating publication
// failed: even a pre-publication error means the intended cross-service
// rollback did not complete, so issuing fresh runtime authority would be
// dishonest until a restart reloads the durable snapshot.
func (s *SessionStore) Poison(cause error) {
	if s == nil {
		return
	}
	if cause == nil {
		cause = ErrSessionPersistencePoisoned
	}
	s.mu.Lock()
	s.persistencePoison = errors.Join(s.persistencePoison, ErrSessionPersistencePoisoned, cause)
	s.mu.Unlock()
}

func (s *SessionStore) persistLocked() (PersistenceCommitState, error) {
	if err := s.persistencePoisonErrorLocked(); err != nil {
		return PersistenceNotPublished, err
	}
	if s.persistence == nil {
		return PersistenceDurable, nil
	}
	state, err := s.persistence.Save(s.snapshotLocked())
	switch {
	case state == PersistenceDurable && err == nil:
		return state, nil
	case state == PersistenceNotPublished && err != nil:
		return state, err
	case state == PersistencePublishedDurabilityUnknown && err != nil:
		indeterminate := errors.Join(ErrSessionPersistenceIndeterminate, err)
		s.persistencePoison = indeterminate
		return state, indeterminate
	default:
		contractErr := fmt.Errorf("%w: invalid save result state=%d err=%v", ErrSessionPersistenceIndeterminate, state, err)
		s.persistencePoison = contractErr
		return PersistencePublishedDurabilityUnknown, contractErr
	}
}

func shouldRollbackPersistence(state PersistenceCommitState) bool {
	return state == PersistenceNotPublished
}

func (s *SessionStore) restoreSessionLocked(id string, previous *Session) {
	if previous == nil {
		delete(s.sessions, id)
		return
	}
	s.sessions[id] = cloneSessionPtr(previous)
}

func (s *SessionStore) Begin(id string, kind SessionKind) (Session, error) {
	if id == "" || !validSessionKind(kind) {
		return Session{}, fmt.Errorf("session ID and supported kind are required: %w", ErrInvalidSessionTransition)
	}
	return s.beginOwned(id, kind, nil, SessionPermissionAlwaysAsk)
}

// BeginOwned publishes a lifecycle row with the conservative default mode.
// Callers that have an explicit renderer session intent use
// BeginOwnedWithPermissionMode below.
func (s *SessionStore) BeginOwned(id string, kind SessionKind, owner SessionOwner) (Session, error) {
	return s.BeginOwnedWithPermissionMode(id, kind, owner, SessionPermissionAlwaysAsk)
}

// BeginOwnedWithPermissionMode persists the session permission intent in the
// same durable row as its owner, before runtime authority is registered.
func (s *SessionStore) BeginOwnedWithPermissionMode(id string, kind SessionKind, owner SessionOwner, mode SessionPermissionMode) (Session, error) {
	if !mode.Valid() {
		return Session{}, fmt.Errorf("invalid session permission mode %q: %w", mode, ErrInvalidSessionTransition)
	}
	if err := validateSessionOwner(owner); err != nil {
		return Session{}, err
	}
	return s.beginOwned(id, kind, &owner, mode)
}

func (s *SessionStore) beginOwned(id string, kind SessionKind, owner *SessionOwner, mode SessionPermissionMode) (Session, error) {
	if id == "" || !validSessionKind(kind) || !mode.Valid() {
		return Session{}, fmt.Errorf("session ID, supported kind, and permission mode are required: %w", ErrInvalidSessionTransition)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[id]; exists {
		return Session{}, fmt.Errorf("session %q: %w", id, ErrSessionExists)
	}
	now := s.now()
	session := &Session{
		ID: id, Kind: kind, PermissionMode: mode, Status: SessionRunning, Attempt: 1,
		StartedAt: now, UpdatedAt: now, Stream: []StreamEvent{}, Checkpoints: []SessionCheckpoint{},
	}
	if owner != nil {
		ownerCopy := *owner
		session.Owner = &ownerCopy
	}
	s.sessions[id] = session
	if state, err := s.persistLocked(); err != nil {
		if shouldRollbackPersistence(state) {
			delete(s.sessions, id)
		}
		return Session{}, fmt.Errorf("persist session %q: %w", id, err)
	}
	return cloneSession(session), nil
}

func (s *SessionStore) Get(id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	if session == nil {
		return Session{}, fmt.Errorf("session %q: %w", id, ErrSessionNotFound)
	}
	return cloneSession(session), nil
}

func (s *SessionStore) AppendStream(id string, input StreamEventInput) (StreamEvent, error) {
	if !validStreamKind(input.Kind) {
		return StreamEvent{}, fmt.Errorf("unsupported stream event kind %q: %w", input.Kind, ErrInvalidSessionTransition)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	if session == nil {
		return StreamEvent{}, fmt.Errorf("session %q: %w", id, ErrSessionNotFound)
	}
	if session.Status != SessionRunning {
		return StreamEvent{}, fmt.Errorf("cannot append stream while session is %s: %w", session.Status, ErrInvalidSessionTransition)
	}
	if session.Recovery == SessionRecoveryRequired {
		return StreamEvent{}, fmt.Errorf("session %q: %w", id, ErrSessionRecoveryRequired)
	}
	previous := cloneSessionPtr(session)
	now := s.now()
	event := StreamEvent{Sequence: uint64(len(session.Stream) + 1), Kind: input.Kind, Data: input.Data, Timestamp: now}
	session.Stream = append(session.Stream, event)
	session.UpdatedAt = now
	if state, err := s.persistLocked(); err != nil {
		if shouldRollbackPersistence(state) {
			s.restoreSessionLocked(id, previous)
		}
		return StreamEvent{}, fmt.Errorf("persist stream event for %q: %w", id, err)
	}
	return event, nil
}

func (s *SessionStore) CreateCheckpoint(id string, input CheckpointInput) (SessionCheckpoint, error) {
	state, err := canonicalArguments(input.State)
	if err != nil {
		return SessionCheckpoint{}, fmt.Errorf("checkpoint state must be a JSON object: %w", ErrInvalidCheckpoint)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	if session == nil {
		return SessionCheckpoint{}, fmt.Errorf("session %q: %w", id, ErrSessionNotFound)
	}
	if session.Status != SessionRunning && session.Status != SessionPaused {
		return SessionCheckpoint{}, fmt.Errorf("cannot checkpoint session in %s: %w", session.Status, ErrInvalidSessionTransition)
	}
	if session.Recovery == SessionRecoveryRequired {
		return SessionCheckpoint{}, fmt.Errorf("session %q: %w", id, ErrSessionRecoveryRequired)
	}
	previous := cloneSessionPtr(session)
	now := s.now()
	sequence := uint64(len(session.Stream))
	checkpoint := SessionCheckpoint{
		ID:    checkpointID(id, uint64(len(session.Checkpoints)+1), state),
		Label: input.Label, State: state, StreamSequence: sequence, CreatedAt: now,
	}
	session.Checkpoints = append(session.Checkpoints, checkpoint)
	session.UpdatedAt = now
	if state, err := s.persistLocked(); err != nil {
		if shouldRollbackPersistence(state) {
			s.restoreSessionLocked(id, previous)
		}
		return SessionCheckpoint{}, fmt.Errorf("persist checkpoint for %q: %w", id, err)
	}
	return cloneCheckpoint(checkpoint), nil
}

func (s *SessionStore) Pause(id string) error {
	return s.transition(id, func(session *Session, now time.Time) error {
		if session.Status != SessionRunning {
			return fmt.Errorf("cannot pause session in %s: %w", session.Status, ErrInvalidSessionTransition)
		}
		if session.Recovery == SessionRecoveryRequired {
			return fmt.Errorf("session %q: %w", id, ErrSessionRecoveryRequired)
		}
		session.Status = SessionPaused
		session.UpdatedAt = now
		return nil
	})
}

func (s *SessionStore) Fail(id string, failure error) error {
	if failure == nil {
		return fmt.Errorf("failure reason is required: %w", ErrInvalidSessionTransition)
	}
	return s.transition(id, func(session *Session, now time.Time) error {
		if session.Status != SessionRunning {
			return fmt.Errorf("cannot fail session in %s: %w", session.Status, ErrInvalidSessionTransition)
		}
		if session.Recovery == SessionRecoveryRequired {
			return fmt.Errorf("session %q: %w", id, ErrSessionRecoveryRequired)
		}
		session.Status = SessionFailed
		session.Failure = failure.Error()
		session.UpdatedAt = now
		return nil
	})
}

func (s *SessionStore) Resume(id, checkpointID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	if session == nil {
		return Session{}, fmt.Errorf("session %q: %w", id, ErrSessionNotFound)
	}
	if session.Status != SessionPaused && session.Status != SessionFailed {
		return Session{}, fmt.Errorf("cannot resume session in %s: %w", session.Status, ErrInvalidSessionTransition)
	}
	if session.Recovery == SessionRecoveryRequired {
		return Session{}, fmt.Errorf("session %q: %w", id, ErrSessionRecoveryRequired)
	}
	checkpointExists := false
	for _, checkpoint := range session.Checkpoints {
		if checkpoint.ID == checkpointID {
			checkpointExists = true
			break
		}
	}
	if !checkpointExists {
		return Session{}, fmt.Errorf("checkpoint %q does not belong to session %q: %w", checkpointID, id, ErrInvalidCheckpoint)
	}
	previous := cloneSessionPtr(session)
	now := s.now()
	session.Status = SessionRunning
	session.Attempt++
	session.ResumedFrom = checkpointID
	session.Failure = ""
	session.CompletedAt = nil
	session.UpdatedAt = now
	if state, err := s.persistLocked(); err != nil {
		if shouldRollbackPersistence(state) {
			s.restoreSessionLocked(id, previous)
		}
		return Session{}, fmt.Errorf("persist resume for %q: %w", id, err)
	}
	return cloneSession(session), nil
}

func (s *SessionStore) Complete(id string) error {
	return s.transition(id, func(session *Session, now time.Time) error {
		if session.Recovery == SessionRecoveryRequired {
			return fmt.Errorf("session %q: %w", id, ErrSessionRecoveryRequired)
		}
		if session.Status != SessionRunning {
			return fmt.Errorf("cannot complete session in %s: %w", session.Status, ErrInvalidSessionTransition)
		}
		session.Status = SessionCompleted
		session.UpdatedAt = now
		completedAt := now
		session.CompletedAt = &completedAt
		return nil
	})
}

// Close publishes a terminal state for an explicitly closed owner regardless
// of whether its last resumable state was running, paused, or failed.
func (s *SessionStore) Close(id string) error {
	return s.transition(id, func(session *Session, now time.Time) error {
		if session.Recovery == SessionRecoveryRequired {
			return fmt.Errorf("session %q: %w", id, ErrSessionRecoveryRequired)
		}
		if session.Status == SessionCompleted {
			return nil
		}
		switch session.Status {
		case SessionRunning, SessionPaused, SessionFailed:
		default:
			return fmt.Errorf("cannot close session in %s: %w", session.Status, ErrInvalidSessionTransition)
		}
		session.Status = SessionCompleted
		session.UpdatedAt = now
		completedAt := now
		session.CompletedAt = &completedAt
		return nil
	})
}

// CloseAll terminalizes every non-terminal owner during a workspace
// incarnation change. The rows remain available for diagnostics, but no
// resumable session survives the authority reset.
func (s *SessionStore) CloseAll() []Session {
	closed, _ := s.CloseAllDurable()
	return closed
}

// CloseAllDurable is the fail-closed variant used by production workspace
// reset. A pre-publication failure restores the in-memory rows. If publication
// succeeded but directory durability is unknown, the new terminal state is
// retained and the store is poisoned so stale state cannot be written back.
func (s *SessionStore) CloseAllDurable() ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := make(map[string]*Session, len(s.sessions))
	for id, session := range s.sessions {
		previous[id] = cloneSessionPtr(session)
	}
	now := s.now()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	closed := make([]Session, 0, len(ids))
	for _, id := range ids {
		session := s.sessions[id]
		if session.Recovery == SessionRecoveryRequired {
			continue
		}
		if session.Status != SessionCompleted {
			session.Status = SessionCompleted
			session.UpdatedAt = now
			completedAt := now
			session.CompletedAt = &completedAt
		}
		closed = append(closed, cloneSession(session))
	}
	if len(closed) == 0 {
		return closed, nil
	}
	if state, err := s.persistLocked(); err != nil {
		if shouldRollbackPersistence(state) {
			s.sessions = previous
		}
		return nil, fmt.Errorf("persist workspace session close: %w", err)
	}
	return closed, nil
}

// BindOwner records the trusted domain owner for a lifecycle row. It is the
// only operation that clears recovery-required state while preserving a
// resumable row; renderer IDs alone can never reattach a durable session.
func (s *SessionStore) BindOwner(id string, owner SessionOwner) error {
	if id == "" {
		return fmt.Errorf("session ID is required: %w", ErrInvalidSessionTransition)
	}
	if err := validateSessionOwner(owner); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	if session == nil {
		return fmt.Errorf("session %q: %w", id, ErrSessionNotFound)
	}
	if session.Status == SessionCompleted {
		return fmt.Errorf("session %q is completed: %w", id, ErrInvalidSessionTransition)
	}
	previous := cloneSessionPtr(session)
	ownerCopy := owner
	session.Owner = &ownerCopy
	session.Recovery = SessionRecoveryNone
	session.UpdatedAt = s.now()
	if state, err := s.persistLocked(); err != nil {
		if shouldRollbackPersistence(state) {
			s.restoreSessionLocked(id, previous)
		}
		return fmt.Errorf("persist owner for %q: %w", id, err)
	}
	return nil
}

func validateSessionOwner(owner SessionOwner) error {
	if owner.Domain == "" || owner.Incarnation == "" {
		return fmt.Errorf("session owner metadata is incomplete: %w", ErrInvalidSessionTransition)
	}
	if owner.WorkspaceGeneration == 0 {
		if owner.WorkspaceFingerprint != "" && !validWorkspaceFingerprint(owner.WorkspaceFingerprint) {
			return fmt.Errorf("unscoped session owner fingerprint is invalid: %w", ErrInvalidSessionTransition)
		}
	} else if !validWorkspaceFingerprint(owner.WorkspaceFingerprint) {
		return fmt.Errorf("workspace-scoped session owner fingerprint is invalid: %w", ErrInvalidSessionTransition)
	}
	return nil
}

func validWorkspaceFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

// Delete removes a just-created owner during trusted wiring rollback. It is
// intentionally not exposed through any service binding and rejects rows that
// already contain stream/checkpoint history.
func (s *SessionStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	if session == nil {
		return fmt.Errorf("session %q: %w", id, ErrSessionNotFound)
	}
	if len(session.Stream) != 0 || len(session.Checkpoints) != 0 {
		return fmt.Errorf("session %q has execution history: %w", id, ErrInvalidSessionTransition)
	}
	delete(s.sessions, id)
	if state, err := s.persistLocked(); err != nil {
		if shouldRollbackPersistence(state) {
			// Restore the row only if the delete was never published.
			s.sessions[id] = session
		}
		return fmt.Errorf("persist session delete for %q: %w", id, err)
	}
	return nil
}

// RecoveryRequired returns durable rows that need an explicit trusted owner
// rebind. It never returns a runtime capability or restores old authority. A
// poisoned store cannot report a healthy inventory because the latest
// publication may be visible without confirmed durability.
func (s *SessionStore) RecoveryRequired() ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persistencePoisonErrorLocked(); err != nil {
		return nil, err
	}
	rows := make([]Session, 0)
	for _, session := range s.sessions {
		if session.Recovery == SessionRecoveryRequired {
			rows = append(rows, cloneSession(session))
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

// RecoveryDispositionCandidates returns only rows that are awaiting a manual
// disposition or already have a confirmed discard. The latter supports
// idempotent operator retries after a healthy reload. Callers still need to
// authenticate the row owner before exposing or mutating it.
func (s *SessionStore) RecoveryDispositionCandidates() ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persistencePoisonErrorLocked(); err != nil {
		return nil, err
	}
	rows := make([]Session, 0)
	for _, session := range s.sessions {
		if session.Recovery == SessionRecoveryRequired ||
			(session.Status == SessionCompleted && session.Recovery == SessionRecoveryNone &&
				session.RecoveryDisposition == RecoveryDispositionDiscard) {
			rows = append(rows, cloneSession(session))
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

// ApplyRecoveryDisposition terminalizes one recovery-required row without
// restoring its old runtime authority. The caller must first prove the current
// trusted domain owner; this method only performs the durable state transition.
func (s *SessionStore) ApplyRecoveryDisposition(id string, disposition RecoveryDisposition) (Session, error) {
	if id == "" || disposition != RecoveryDispositionDiscard {
		return Session{}, fmt.Errorf("session %q: %w", id, ErrInvalidRecoveryDisposition)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persistencePoisonErrorLocked(); err != nil {
		return Session{}, err
	}
	session := s.sessions[id]
	if session == nil {
		return Session{}, fmt.Errorf("session %q: %w", id, ErrSessionNotFound)
	}
	if session.Status == SessionCompleted && session.Recovery == SessionRecoveryNone && session.RecoveryDisposition == disposition {
		return cloneSession(session), nil
	}
	if session.Recovery != SessionRecoveryRequired {
		return Session{}, fmt.Errorf("session %q is not awaiting recovery: %w", id, ErrInvalidRecoveryDisposition)
	}
	previous := cloneSessionPtr(session)
	now := s.now()
	session.Status = SessionCompleted
	session.Recovery = SessionRecoveryNone
	session.RecoveryDisposition = disposition
	session.UpdatedAt = now
	completedAt := now
	session.CompletedAt = &completedAt
	if state, err := s.persistLocked(); err != nil {
		if shouldRollbackPersistence(state) {
			s.restoreSessionLocked(id, previous)
		}
		return Session{}, fmt.Errorf("persist recovery disposition for %q: %w", id, err)
	}
	return cloneSession(session), nil
}

// AppendUsageObservation atomically publishes one metered tool observation
// and its checkpoint. Replaying an idempotent terminal receipt is a no-op once
// the same UnitID is present in a prior usage-recorded checkpoint.
func (s *SessionStore) AppendUsageObservation(id, unitID, operation string, success bool) error {
	if id == "" || unitID == "" || operation == "" {
		return fmt.Errorf("session, usage unit, and operation are required: %w", ErrInvalidUsageRecord)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	if session == nil {
		return fmt.Errorf("session %q: %w", id, ErrSessionNotFound)
	}
	if session.Status != SessionRunning {
		return fmt.Errorf("cannot append usage while session is %s: %w", session.Status, ErrInvalidSessionTransition)
	}
	if session.Recovery == SessionRecoveryRequired {
		return fmt.Errorf("session %q: %w", id, ErrSessionRecoveryRequired)
	}
	previous := cloneSessionPtr(session)
	for _, checkpoint := range session.Checkpoints {
		if checkpoint.Label != "usage-recorded" {
			continue
		}
		var state struct {
			UnitID string `json:"unitId"`
		}
		if json.Unmarshal(checkpoint.State, &state) == nil && state.UnitID == unitID {
			return nil
		}
	}
	now := s.now()
	event := StreamEvent{Sequence: uint64(len(session.Stream) + 1), Kind: StreamToolResult, Data: operation, Timestamp: now}
	session.Stream = append(session.Stream, event)
	state, err := canonicalArguments(json.RawMessage(fmt.Sprintf(`{"unitId":%q,"operation":%q,"success":%t}`, unitID, operation, success)))
	if err != nil {
		return fmt.Errorf("encode usage observation: %w", ErrInvalidCheckpoint)
	}
	checkpoint := SessionCheckpoint{
		ID: checkpointID(id, uint64(len(session.Checkpoints)+1), state), Label: "usage-recorded",
		State: state, StreamSequence: uint64(len(session.Stream)), CreatedAt: now,
	}
	session.Checkpoints = append(session.Checkpoints, checkpoint)
	session.UpdatedAt = now
	if state, err := s.persistLocked(); err != nil {
		if shouldRollbackPersistence(state) {
			s.restoreSessionLocked(id, previous)
		}
		return fmt.Errorf("persist usage observation for %q: %w", id, err)
	}
	return nil
}

func (s *SessionStore) transition(id string, apply func(*Session, time.Time) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	if session == nil {
		return fmt.Errorf("session %q: %w", id, ErrSessionNotFound)
	}
	previous := cloneSessionPtr(session)
	if err := apply(session, s.now()); err != nil {
		return err
	}
	if state, err := s.persistLocked(); err != nil {
		if shouldRollbackPersistence(state) {
			s.restoreSessionLocked(id, previous)
		}
		return fmt.Errorf("persist session %q transition: %w", id, err)
	}
	return nil
}

func validSessionKind(kind SessionKind) bool {
	switch kind {
	case SessionChat, SessionPlan, SessionGoal, SessionWorkflow:
		return true
	default:
		return false
	}
}

func validStreamKind(kind StreamEventKind) bool {
	switch kind {
	case StreamDelta, StreamToolRequest, StreamToolResult, StreamStatus:
		return true
	default:
		return false
	}
}

func checkpointID(sessionID string, ordinal uint64, state []byte) string {
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("%s\x00%d\x00", sessionID, ordinal)), state...))
	return hex.EncodeToString(sum[:16])
}

func cloneCheckpoint(checkpoint SessionCheckpoint) SessionCheckpoint {
	copy := checkpoint
	copy.State = append(json.RawMessage(nil), checkpoint.State...)
	return copy
}

func cloneSession(session *Session) Session {
	copy := *session
	copy.Stream = append([]StreamEvent(nil), session.Stream...)
	copy.Checkpoints = make([]SessionCheckpoint, len(session.Checkpoints))
	for index, checkpoint := range session.Checkpoints {
		copy.Checkpoints[index] = cloneCheckpoint(checkpoint)
	}
	if session.CompletedAt != nil {
		completedAt := *session.CompletedAt
		copy.CompletedAt = &completedAt
	}
	if session.Owner != nil {
		owner := *session.Owner
		copy.Owner = &owner
	}
	return copy
}

func cloneSessionPtr(session *Session) *Session {
	if session == nil {
		return nil
	}
	copy := cloneSession(session)
	return &copy
}

// TokenEstimator is the only context-counting dependency accepted by the
// shared context manager. The services package adapts token_estimator.go here;
// chat/plan/goal/workflow therefore cannot silently choose separate heuristics.
type TokenEstimator interface {
	EstimateTokens(text string) int
}

type ContextItem struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Required bool   `json:"required"`
	Priority int    `json:"priority"`
}

type ContextSelection struct {
	Included []ContextItem `json:"included"`
	Dropped  []ContextItem `json:"dropped"`
	Tokens   int           `json:"tokens"`
	Limit    int           `json:"limit"`
}

type ContextManager struct {
	estimator TokenEstimator
}

func NewContextManager(estimator TokenEstimator) (*ContextManager, error) {
	if estimator == nil {
		return nil, fmt.Errorf("token estimator is required")
	}
	return &ContextManager{estimator: estimator}, nil
}

// Select always includes required items in input order, then optional items by
// descending priority with stable input-order ties. Required overflow fails;
// it never truncates system or safety context behind the caller's back.
func (m *ContextManager) Select(items []ContextItem, limit int) (ContextSelection, error) {
	if limit <= 0 {
		return ContextSelection{}, fmt.Errorf("context limit must be positive: %w", ErrContextBudgetExceeded)
	}
	type measured struct {
		item  ContextItem
		token int
		index int
	}
	measuredItems := make([]measured, len(items))
	for index, item := range items {
		if item.ID == "" {
			return ContextSelection{}, fmt.Errorf("context item ID is required")
		}
		tokens := m.estimator.EstimateTokens(item.Text)
		if tokens < 0 {
			return ContextSelection{}, fmt.Errorf("estimator returned negative count for %q", item.ID)
		}
		measuredItems[index] = measured{item: item, token: tokens, index: index}
	}
	selection := ContextSelection{Limit: limit}
	included := make(map[int]bool, len(items))
	for _, candidate := range measuredItems {
		if !candidate.item.Required {
			continue
		}
		if selection.Tokens+candidate.token > limit {
			return ContextSelection{}, fmt.Errorf("required context %q exceeds %d-token limit: %w", candidate.item.ID, limit, ErrContextBudgetExceeded)
		}
		selection.Included = append(selection.Included, candidate.item)
		selection.Tokens += candidate.token
		included[candidate.index] = true
	}
	optional := append([]measured(nil), measuredItems...)
	sort.SliceStable(optional, func(i, j int) bool { return optional[i].item.Priority > optional[j].item.Priority })
	for _, candidate := range optional {
		if candidate.item.Required {
			continue
		}
		if selection.Tokens+candidate.token <= limit {
			selection.Included = append(selection.Included, candidate.item)
			selection.Tokens += candidate.token
			included[candidate.index] = true
		}
	}
	for index, item := range items {
		if !included[index] {
			selection.Dropped = append(selection.Dropped, item)
		}
	}
	return selection, nil
}
