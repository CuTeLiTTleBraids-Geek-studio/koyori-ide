package services

import (
	"crypto/hmac"
	"errors"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

// AgentExternalReceiptRecoveryEntry is a content-free view of one pending
// external side effect. The UnitID, raw adapter receipt and prepared state are
// intentionally withheld from the operator/renderer DTO.
type AgentExternalReceiptRecoveryEntry struct {
	Handle    string    `json:"handle"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"startedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AgentExternalReceiptDispositionRequest is accepted only by trusted
// headless/bootstrap code. "manual-unknown" records that the side effect is
// unresolved; it never invokes a handler or claims compensation.
type AgentExternalReceiptDispositionRequest struct {
	Handle      string `json:"handle"`
	Disposition string `json:"disposition"`
}

type AgentExternalReceiptDispositionResult struct {
	Handle      string    `json:"handle"`
	Status      string    `json:"status"`
	Disposition string    `json:"disposition"`
	CompletedAt time.Time `json:"completedAt"`
}

// AgentExternalReceiptRecoveryDispatcher is retained only by trusted
// bootstrap/headless code. AgentLifecycle itself is not a Wails service.
type AgentExternalReceiptRecoveryDispatcher interface {
	PendingExternalReceiptDispositions() ([]AgentExternalReceiptRecoveryEntry, error)
	DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest) (AgentExternalReceiptDispositionResult, error)
}

var _ AgentExternalReceiptRecoveryDispatcher = (*AgentLifecycle)(nil)

const externalReceiptDispositionManualUnknown = "manual-unknown"

// receiptRecoveryOwnerTrustedLocked verifies the durable owner claim from the
// lifecycle row that admitted the usage unit. It permits an old process
// incarnation only after runtime authority has been cleared; a renderer ID or
// an active current-incarnation owner cannot be used to dispose a receipt.
func (l *AgentLifecycle) receiptRecoveryOwnerTrustedLocked(record UsageRecord) bool {
	return l.receiptRecoveryOwnerTrustedAtGenerationLocked(record, l.currentWorkspaceGeneration())
}

func (l *AgentLifecycle) receiptRecoveryOwnerTrustedAtGenerationLocked(record UsageRecord, currentGeneration uint64) bool {
	if l == nil || l.sessions == nil || l.runtime == nil || record.SessionID == "" {
		return false
	}
	kind := usageSessionKind(agentcore.UsageRecord{
		SessionID: record.SessionID, UnitKind: agentcore.UsageUnitKind(record.UnitKind), Operation: string(record.Operation),
	})
	logicalID := lifecycleSessionID(kind, record.SessionID)
	if mapped := l.logicalSessionForRuntime(record.SessionID); mapped != "" {
		logicalID = mapped
	}
	session, err := l.sessions.Get(logicalID)
	if err != nil || session.Owner == nil || session.Owner.Domain != lifecycleOwnerDomain(session.Kind) ||
		session.Owner.Incarnation == l.incarnation {
		return false
	}
	if mapped := l.runtimeSessionID(session.Kind, session.ID); mapped != session.ID ||
		l.runtime.IsSessionRegistered(session.ID) ||
		(session.Owner.RuntimeID != "" && l.runtime.IsSessionRegistered(session.Owner.RuntimeID)) {
		return false
	}
	if (session.Owner.WorkspaceGeneration == 0) != (currentGeneration == 0) {
		return false
	}
	root := ""
	if currentGeneration != 0 {
		if l.workspaceLease == nil {
			return false
		}
		lease, leaseErr := l.workspaceLease()
		if leaseErr != nil {
			return false
		}
		if lease.generation != currentGeneration {
			return false
		}
		root = lease.root
		if lease.generation != session.Owner.WorkspaceGeneration {
			return false
		}
	}
	expected := l.workspaceOwnerFingerprint(root, session.ID, session.Kind, *session.Owner)
	return expected != "" && hmac.Equal([]byte(expected), []byte(session.Owner.WorkspaceFingerprint))
}

func publicExternalReceiptRecoveryError(err error) error {
	switch {
	case errors.Is(err, ErrUsagePersistenceIndeterminate), errors.Is(err, ErrUsagePersistencePoisoned):
		return ErrAgentRecoveryPersistenceIndeterminate
	case errors.Is(err, ErrUsageReceiptState):
		return ErrUsageReceiptState
	case errors.Is(err, ErrAgentRecoveryPersistence):
		return ErrAgentRecoveryPersistence
	default:
		return ErrNotAllowed
	}
}

// PendingExternalReceiptDispositions exposes only trusted, old-incarnation
// pending rows. AgentLifecycle is not registered with Wails.
//
//wails:ignore
func (l *AgentLifecycle) PendingExternalReceiptDispositions() ([]AgentExternalReceiptRecoveryEntry, error) {
	if l == nil || l.permission == nil {
		return nil, ErrNotAllowed
	}
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return nil, publicExternalReceiptRecoveryError(err)
	}
	defer workspaceLease.release()
	return l.pendingExternalReceiptDispositionsWithinWorkspaceAuthority()
}

func (l *AgentLifecycle) pendingExternalReceiptDispositionsWithinWorkspaceAuthority() ([]AgentExternalReceiptRecoveryEntry, error) {
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	rows, err := l.permission.pendingExternalReceiptRecovery()
	if err != nil {
		return nil, publicExternalReceiptRecoveryError(err)
	}
	entries := make([]AgentExternalReceiptRecoveryEntry, 0, len(rows))
	for _, row := range rows {
		if !l.receiptRecoveryOwnerTrustedLocked(row.record) {
			continue
		}
		entries = append(entries, AgentExternalReceiptRecoveryEntry{
			Handle: row.handle, Status: "pending", StartedAt: row.record.StartedAt, UpdatedAt: row.record.CompletedAt,
		})
	}
	return entries, nil
}

// DispatchExternalReceiptDisposition records only the generic unknown
// terminal. No adapter is called because receipt metadata/rollback state is
// intentionally not durable. A fresh process must verify the ledger before a
// disposition is accepted; publication-unknown poisons this process.
//
//wails:ignore
func (l *AgentLifecycle) DispatchExternalReceiptDisposition(request AgentExternalReceiptDispositionRequest) (AgentExternalReceiptDispositionResult, error) {
	if l == nil || l.permission == nil || !validExternalReceiptRecoveryHandle(request.Handle) ||
		request.Disposition != externalReceiptDispositionManualUnknown {
		return AgentExternalReceiptDispositionResult{}, ErrInvalidInput
	}
	workspaceLease, err := l.acquireWorkspaceAuthority()
	if err != nil {
		return AgentExternalReceiptDispositionResult{}, publicExternalReceiptRecoveryError(err)
	}
	defer workspaceLease.release()
	return l.dispatchExternalReceiptDispositionWithinWorkspaceAuthority(request)
}

func (l *AgentLifecycle) dispatchExternalReceiptDispositionWithinWorkspaceAuthority(request AgentExternalReceiptDispositionRequest) (AgentExternalReceiptDispositionResult, error) {
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	rows, err := l.permission.pendingExternalReceiptRecovery()
	if err != nil {
		// A terminal manual disposition is also replayable after reload, so look
		// up errors here are intentionally not converted into success.
		return AgentExternalReceiptDispositionResult{}, publicExternalReceiptRecoveryError(err)
	}
	var matched *externalReceiptRecoveryRecord
	for index := range rows {
		if hmac.Equal([]byte(rows[index].handle), []byte(request.Handle)) {
			matched = &rows[index]
			break
		}
	}
	if matched == nil {
		// It may already be a terminal idempotent replay. The permission layer
		// performs the same handle/identity check without exposing the row.
		terminal, _, lookupErr := l.permission.externalReceiptRecoveryForDisposition(request.Handle)
		if lookupErr != nil {
			return AgentExternalReceiptDispositionResult{}, publicExternalReceiptRecoveryError(lookupErr)
		}
		if !l.receiptRecoveryOwnerTrustedLocked(terminal) {
			return AgentExternalReceiptDispositionResult{}, ErrNotAllowed
		}
		return AgentExternalReceiptDispositionResult{
			Handle: request.Handle, Status: "completed", Disposition: externalReceiptDispositionManualUnknown,
			CompletedAt: terminal.CompletedAt,
		}, nil
	}
	expectedGeneration := l.currentWorkspaceGeneration()
	if !l.receiptRecoveryOwnerTrustedAtGenerationLocked(matched.record, expectedGeneration) {
		return AgentExternalReceiptDispositionResult{}, ErrNotAllowed
	}
	var terminal UsageRecord
	if err := l.withCurrentWorkspaceGeneration(expectedGeneration, func() error {
		var dispositionErr error
		terminal, dispositionErr = l.permission.applyExternalReceiptManualUnknown(request.Handle)
		return dispositionErr
	}); err != nil {
		return AgentExternalReceiptDispositionResult{}, publicExternalReceiptRecoveryError(err)
	}
	return AgentExternalReceiptDispositionResult{
		Handle: request.Handle, Status: "completed", Disposition: externalReceiptDispositionManualUnknown,
		CompletedAt: terminal.CompletedAt,
	}, nil
}
