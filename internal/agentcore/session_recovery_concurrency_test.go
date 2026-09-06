package agentcore

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type recoveryConcurrencyPersistence struct {
	mu   sync.Mutex
	rows []Session
}

func (p *recoveryConcurrencyPersistence) Load() ([]Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneRecoveryConcurrencyRows(p.rows), nil
}

func (p *recoveryConcurrencyPersistence) Save(rows []Session) (PersistenceCommitState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rows = cloneRecoveryConcurrencyRows(rows)
	return PersistenceDurable, nil
}

func cloneRecoveryConcurrencyRows(rows []Session) []Session {
	cloned := make([]Session, len(rows))
	for index := range rows {
		cloned[index] = cloneSession(&rows[index])
	}
	return cloned
}

func newRecoveryConcurrencyStore(t *testing.T, id string) *SessionStore {
	t.Helper()
	persistence := &recoveryConcurrencyPersistence{}
	clock := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	store, err := NewPersistentSessionStore(persistence, func() time.Time { return clock })
	if err != nil {
		t.Fatalf("NewPersistentSessionStore seed: %v", err)
	}
	owner := SessionOwner{
		Domain:               "workflow-service",
		RuntimeID:            "workflow:old-runtime",
		WorkspaceGeneration:  7,
		WorkspaceFingerprint: strings.Repeat("ab", 32),
		Incarnation:          "old-incarnation",
	}
	if _, err := store.BeginOwned(id, SessionWorkflow, owner); err != nil {
		t.Fatalf("BeginOwned seed: %v", err)
	}
	if err := store.Pause(id); err != nil {
		t.Fatalf("Pause seed: %v", err)
	}
	restarted, err := NewPersistentSessionStore(persistence, func() time.Time { return clock.Add(time.Minute) })
	if err != nil {
		t.Fatalf("NewPersistentSessionStore restart: %v", err)
	}
	row, err := restarted.Get(id)
	if err != nil || row.Recovery != SessionRecoveryRequired || row.Owner == nil || row.Owner.RuntimeID != "" {
		t.Fatalf("recovery seed = %+v, err=%v", row, err)
	}
	return restarted
}

type recoveryDispositionResult struct {
	row Session
	err error
}

func raceRecoveryDisposition(
	store *SessionStore,
	id string,
	other func() error,
) (recoveryDispositionResult, error) {
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var disposition recoveryDispositionResult
	var otherErr error
	go func() {
		defer wait.Done()
		<-start
		disposition.row, disposition.err = store.ApplyRecoveryDisposition(id, RecoveryDispositionDiscard)
	}()
	go func() {
		defer wait.Done()
		<-start
		otherErr = other()
	}()
	close(start)
	wait.Wait()
	return disposition, otherErr
}

func assertRecoveryDiscardTerminal(t *testing.T, row Session) {
	t.Helper()
	if row.Status != SessionCompleted || row.Recovery != SessionRecoveryNone ||
		row.RecoveryDisposition != RecoveryDispositionDiscard || row.CompletedAt == nil {
		t.Fatalf("recovery row did not converge to discard terminal state: %+v", row)
	}
	if row.Owner == nil || row.Owner.RuntimeID != "" {
		t.Fatalf("discard terminal row retained runtime authority metadata: %+v", row.Owner)
	}
}

func TestSessionStoreRecoveryDispositionConcurrentTransitionMatrix(t *testing.T) {
	t.Run("BindOwner", func(t *testing.T) {
		const id = "workflow:recovery-race-bind-owner"
		store := newRecoveryConcurrencyStore(t, id)
		newOwner := SessionOwner{
			Domain:               "workflow-service",
			RuntimeID:            "workflow:new-runtime",
			WorkspaceGeneration:  8,
			WorkspaceFingerprint: strings.Repeat("cd", 32),
			Incarnation:          "new-incarnation",
		}
		disposition, bindErr := raceRecoveryDisposition(store, id, func() error {
			return store.BindOwner(id, newOwner)
		})
		if (disposition.err == nil) == (bindErr == nil) {
			t.Fatalf("disposition error=%v, BindOwner error=%v; want exactly one accepted transition", disposition.err, bindErr)
		}
		final, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get final row: %v", err)
		}
		if disposition.err == nil {
			assertRecoveryDiscardTerminal(t, disposition.row)
			assertRecoveryDiscardTerminal(t, final)
			if !errors.Is(bindErr, ErrInvalidSessionTransition) {
				t.Fatalf("BindOwner after discard error=%v, want ErrInvalidSessionTransition", bindErr)
			}
			return
		}
		if !errors.Is(disposition.err, ErrInvalidRecoveryDisposition) {
			t.Fatalf("disposition after BindOwner error=%v, want ErrInvalidRecoveryDisposition", disposition.err)
		}
		if final.Status != SessionPaused || final.Recovery != SessionRecoveryNone ||
			final.RecoveryDisposition != "" || final.Owner == nil || *final.Owner != newOwner || final.CompletedAt != nil {
			t.Fatalf("BindOwner winner left hybrid recovery state: %+v", final)
		}
	})

	for _, test := range []struct {
		name       string
		other      func(*SessionStore, string) error
		checkError func(error) bool
	}{
		{
			name:  "Complete",
			other: func(store *SessionStore, id string) error { return store.Complete(id) },
			checkError: func(err error) bool {
				return errors.Is(err, ErrSessionRecoveryRequired) || errors.Is(err, ErrInvalidSessionTransition)
			},
		},
		{
			name:  "Close",
			other: func(store *SessionStore, id string) error { return store.Close(id) },
			checkError: func(err error) bool {
				return err == nil || errors.Is(err, ErrSessionRecoveryRequired)
			},
		},
		{
			name: "CloseAllDurable",
			other: func(store *SessionStore, _ string) error {
				_, err := store.CloseAllDurable()
				return err
			},
			checkError: func(err error) bool { return err == nil },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := "workflow:recovery-race-" + strings.ToLower(test.name)
			store := newRecoveryConcurrencyStore(t, id)
			disposition, otherErr := raceRecoveryDisposition(store, id, func() error {
				return test.other(store, id)
			})
			if disposition.err != nil {
				t.Fatalf("ApplyRecoveryDisposition: %v", disposition.err)
			}
			if !test.checkError(otherErr) {
				t.Fatalf("concurrent %s error=%v", test.name, otherErr)
			}
			assertRecoveryDiscardTerminal(t, disposition.row)
			final, err := store.Get(id)
			if err != nil {
				t.Fatalf("Get final row: %v", err)
			}
			assertRecoveryDiscardTerminal(t, final)
		})
	}
}
