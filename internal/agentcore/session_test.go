package agentcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type countingEstimator struct {
	calls int
}

func TestPersistentSessionStoreRequiresTrustedRebindAfterRestart(t *testing.T) {
	dir := t.TempDir()
	persistence := FileSessionStorePersistence{Path: filepath.Join(dir, "sessions.json")}
	clock := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	store, err := NewPersistentSessionStore(persistence, func() time.Time { return clock })
	if err != nil {
		t.Fatalf("initial persistent store: %v", err)
	}
	if _, err := store.Begin("chat:restart", SessionChat); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := store.BindOwner("chat:restart", SessionOwner{
		Domain: "chat-service", RuntimeID: "chat:runtime-old", Incarnation: "incarnation-old",
	}); err != nil {
		t.Fatalf("BindOwner: %v", err)
	}
	checkpoint, err := store.CreateCheckpoint("chat:restart", CheckpointInput{Label: "resume", State: json.RawMessage(`{"ok":true}`)})
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if err := store.Pause("chat:restart"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	restarted, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("reload persistent store: %v", err)
	}
	session, err := restarted.Get("chat:restart")
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if session.Recovery != SessionRecoveryRequired {
		t.Fatalf("restarted recovery state = %q, want %q", session.Recovery, SessionRecoveryRequired)
	}
	if session.Owner == nil || session.Owner.RuntimeID != "" {
		t.Fatalf("restarted owner retained runtime authority: %+v", session.Owner)
	}
	if _, err := restarted.Resume("chat:restart", checkpoint.ID); !errors.Is(err, ErrSessionRecoveryRequired) {
		t.Fatalf("Resume after restart error = %v, want ErrSessionRecoveryRequired", err)
	}
	rows, err := restarted.RecoveryRequired()
	if err != nil {
		t.Fatalf("RecoveryRequired: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "chat:restart" {
		t.Fatalf("recovery rows = %+v", rows)
	}
	data, err := os.ReadFile(persistence.Path)
	if err != nil {
		t.Fatalf("read normalized snapshot: %v", err)
	}
	if !strings.Contains(string(data), string(SessionRecoveryRequired)) {
		t.Fatalf("normalized snapshot omitted recovery disposition: %s", data)
	}
}

func TestPersistentSessionStoreDiscardRecoveryIsDurableAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	persistence := FileSessionStorePersistence{Path: filepath.Join(dir, "sessions.json")}
	store, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("initial persistent store: %v", err)
	}
	if _, err := store.Begin("workflow:discard", SessionWorkflow); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := store.BindOwner("workflow:discard", SessionOwner{
		Domain: "workflow-service", RuntimeID: "workflow:old-runtime", Incarnation: "old-incarnation",
	}); err != nil {
		t.Fatalf("BindOwner: %v", err)
	}
	if _, err := store.CreateCheckpoint("workflow:discard", CheckpointInput{
		Label: "pending", State: json.RawMessage(`{"step":1}`),
	}); err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if err := store.Pause("workflow:discard"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	restarted, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("reload persistent store: %v", err)
	}
	disposed, err := restarted.ApplyRecoveryDisposition("workflow:discard", RecoveryDispositionDiscard)
	if err != nil {
		t.Fatalf("ApplyRecoveryDisposition: %v", err)
	}
	if disposed.Status != SessionCompleted || disposed.Recovery != SessionRecoveryNone || disposed.RecoveryDisposition != RecoveryDispositionDiscard || disposed.CompletedAt == nil {
		t.Fatalf("disposed session = %+v", disposed)
	}
	if disposed.Owner == nil || disposed.Owner.RuntimeID != "" {
		t.Fatalf("disposed session restored runtime authority: %+v", disposed.Owner)
	}
	if replay, err := restarted.ApplyRecoveryDisposition("workflow:discard", RecoveryDispositionDiscard); err != nil || replay.UpdatedAt != disposed.UpdatedAt {
		t.Fatalf("idempotent disposition = %+v, err=%v", replay, err)
	}
	if rows, err := restarted.RecoveryRequired(); err != nil || len(rows) != 0 {
		t.Fatalf("recovery rows after disposition = %+v", rows)
	}

	reloaded, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("reload disposed store: %v", err)
	}
	got, err := reloaded.Get("workflow:discard")
	if err != nil {
		t.Fatalf("Get disposed session: %v", err)
	}
	if got.Status != SessionCompleted || got.RecoveryDisposition != RecoveryDispositionDiscard || got.Owner == nil || got.Owner.RuntimeID != "" {
		t.Fatalf("reloaded disposed session = %+v", got)
	}
}

func TestPersistentSessionStoreRecoveryDispositionFailsClosedAndRollsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	persistence := FileSessionStorePersistence{Path: path}
	store, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("initial persistent store: %v", err)
	}
	if _, err := store.Begin("chat:rollback", SessionChat); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := store.BindOwner("chat:rollback", SessionOwner{
		Domain: "chat-service", RuntimeID: "chat:old-runtime", Incarnation: "old-incarnation",
	}); err != nil {
		t.Fatalf("BindOwner: %v", err)
	}
	if err := store.Pause("chat:rollback"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	restarted, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("reload persistent store: %v", err)
	}
	if _, err := restarted.ApplyRecoveryDisposition("chat:rollback", RecoveryDisposition("resume")); !errors.Is(err, ErrInvalidRecoveryDisposition) {
		t.Fatalf("unsupported disposition error = %v, want ErrInvalidRecoveryDisposition", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove snapshot: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("block snapshot publish: %v", err)
	}
	if _, err := restarted.ApplyRecoveryDisposition("chat:rollback", RecoveryDispositionDiscard); err == nil {
		t.Fatal("recovery disposition succeeded without durable publication")
	}
	got, err := restarted.Get("chat:rollback")
	if err != nil {
		t.Fatalf("Get after rejected disposition: %v", err)
	}
	if got.Status != SessionPaused || got.Recovery != SessionRecoveryRequired || got.RecoveryDisposition != "" || got.Owner == nil || got.Owner.RuntimeID != "" {
		t.Fatalf("rejected disposition changed session = %+v", got)
	}
}

func TestPersistentSessionStoreRecoveryCannotBypassExplicitDisposition(t *testing.T) {
	persistence := FileSessionStorePersistence{Path: filepath.Join(t.TempDir(), "sessions.json")}
	store, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("initial persistent store: %v", err)
	}
	if _, err := store.Begin("workflow:quarantine", SessionWorkflow); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := store.BindOwner("workflow:quarantine", SessionOwner{
		Domain: "workflow-service", RuntimeID: "workflow:old-runtime", Incarnation: "old-incarnation",
	}); err != nil {
		t.Fatalf("BindOwner: %v", err)
	}
	if err := store.Pause("workflow:quarantine"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	restarted, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("reload persistent store: %v", err)
	}
	if err := restarted.Complete("workflow:quarantine"); !errors.Is(err, ErrSessionRecoveryRequired) {
		t.Fatalf("Complete recovery row error = %v, want ErrSessionRecoveryRequired", err)
	}
	if err := restarted.Close("workflow:quarantine"); !errors.Is(err, ErrSessionRecoveryRequired) {
		t.Fatalf("Close recovery row error = %v, want ErrSessionRecoveryRequired", err)
	}
	closed, err := restarted.CloseAllDurable()
	if err != nil {
		t.Fatalf("CloseAllDurable: %v", err)
	}
	if len(closed) != 0 {
		t.Fatalf("CloseAllDurable closed quarantine rows: %+v", closed)
	}
	row, err := restarted.Get("workflow:quarantine")
	if err != nil {
		t.Fatalf("Get quarantine row: %v", err)
	}
	if row.Status != SessionPaused || row.Recovery != SessionRecoveryRequired || row.RecoveryDisposition != "" {
		t.Fatalf("ordinary terminal path changed quarantine row = %+v", row)
	}
}

func TestPersistentSessionStoreRejectsUntrustedSnapshotShape(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "unknown top-level field",
			data: `{"version":1,"sessions":[],"unexpected":true}`,
		},
		{
			name: "unknown session field",
			data: `{"version":1,"sessions":[{"id":"chat:unknown","kind":"chat","status":"completed","unexpected":true}]}`,
		},
		{
			name: "missing status",
			data: `{"version":1,"sessions":[{"id":"chat:missing-status","kind":"chat"}]}`,
		},
		{
			name: "incomplete owner",
			data: `{"version":1,"sessions":[{"id":"chat:bad-owner","kind":"chat","status":"completed","owner":{"domain":"","incarnation":"old"}}]}`,
		},
		{
			name: "trailing JSON value",
			data: `{"version":1,"sessions":[]} {"version":1,"sessions":[]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.json")
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatalf("write snapshot: %v", err)
			}
			persistence := FileSessionStorePersistence{Path: path}
			if _, err := NewPersistentSessionStore(persistence, time.Now); err == nil {
				t.Fatal("untrusted snapshot shape was accepted")
			}
		})
	}
}

func TestFileSessionStorePersistenceRejectsOversizedSaveWithoutReplacingSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	persistence := FileSessionStorePersistence{Path: path}
	valid := []Session{{
		ID: "chat:valid", Kind: SessionChat, Status: SessionCompleted,
		Stream: []StreamEvent{}, Checkpoints: []SessionCheckpoint{},
	}}
	if state, err := persistence.Save(valid); err != nil || state != PersistenceDurable {
		t.Fatalf("save valid snapshot: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read valid snapshot: %v", err)
	}

	oversized := []Session{{
		ID: "chat:oversized", Kind: SessionChat, Status: SessionRunning,
		Stream:      []StreamEvent{{Sequence: 1, Kind: StreamDelta, Data: strings.Repeat("x", maxSessionSnapshotBytes)}},
		Checkpoints: []SessionCheckpoint{},
	}}
	if state, err := persistence.Save(oversized); err == nil || state != PersistenceNotPublished {
		t.Fatal("oversized snapshot save succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot after rejected save: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("rejected oversized save replaced the last valid snapshot")
	}
}

func (e *countingEstimator) EstimateTokens(text string) int {
	e.calls++
	if text == "" {
		return 0
	}
	return len(text)
}

func TestSessionStoreSharesLifecycleAcrossAllExecutionKinds(t *testing.T) {
	clock := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	store := NewSessionStore(func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	})

	for _, kind := range []SessionKind{SessionChat, SessionPlan, SessionGoal, SessionWorkflow} {
		id := string(kind) + "-1"
		session, err := store.Begin(id, kind)
		if err != nil {
			t.Fatalf("Begin(%s): %v", kind, err)
		}
		if session.Status != SessionRunning || session.Attempt != 1 {
			t.Fatalf("Begin(%s) = %+v", kind, session)
		}
		event, err := store.AppendStream(id, StreamEventInput{Kind: StreamDelta, Data: "part"})
		if err != nil {
			t.Fatalf("AppendStream(%s): %v", kind, err)
		}
		if event.Sequence != 1 {
			t.Fatalf("%s sequence = %d, want 1", kind, event.Sequence)
		}
		checkpoint, err := store.CreateCheckpoint(id, CheckpointInput{
			Label: "after first unit",
			State: json.RawMessage(`{"cursor":1,"nested":{"ok":true}}`),
		})
		if err != nil {
			t.Fatalf("CreateCheckpoint(%s): %v", kind, err)
		}
		if err := store.Fail(id, errors.New("temporary provider failure")); err != nil {
			t.Fatalf("Fail(%s): %v", kind, err)
		}
		resumed, err := store.Resume(id, checkpoint.ID)
		if err != nil {
			t.Fatalf("Resume(%s): %v", kind, err)
		}
		if resumed.Status != SessionRunning || resumed.Attempt != 2 || resumed.ResumedFrom != checkpoint.ID {
			t.Fatalf("Resume(%s) = %+v", kind, resumed)
		}
		if err := store.Complete(id); err != nil {
			t.Fatalf("Complete(%s): %v", kind, err)
		}
		if _, err := store.Resume(id, checkpoint.ID); !errors.Is(err, ErrInvalidSessionTransition) {
			t.Fatalf("resume completed %s error = %v, want ErrInvalidSessionTransition", kind, err)
		}
	}
}

func TestSessionStoreCheckpointAndResumeFailClosed(t *testing.T) {
	store := NewSessionStore(time.Now)
	if _, err := store.Begin("goal-1", SessionGoal); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := store.CreateCheckpoint("goal-1", CheckpointInput{State: json.RawMessage(`[1,2]`)}); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("array checkpoint error = %v, want ErrInvalidCheckpoint", err)
	}
	checkpoint, err := store.CreateCheckpoint("goal-1", CheckpointInput{State: json.RawMessage(`{"b":2,"a":1}`)})
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if string(checkpoint.State) != `{"a":1,"b":2}` {
		t.Fatalf("checkpoint state = %s, want canonical JSON", checkpoint.State)
	}
	if _, err := store.Resume("goal-1", checkpoint.ID); !errors.Is(err, ErrInvalidSessionTransition) {
		t.Fatalf("resume running error = %v, want ErrInvalidSessionTransition", err)
	}
	if err := store.Pause("goal-1"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := store.Resume("goal-1", "forged-checkpoint"); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("forged checkpoint error = %v, want ErrInvalidCheckpoint", err)
	}
	if snapshot, err := store.Get("goal-1"); err != nil || snapshot.Status != SessionPaused {
		t.Fatalf("failed resume changed session: snapshot=%+v err=%v", snapshot, err)
	}
}

func TestContextManagerIsSingleEstimatorAndTruncationEntryPoint(t *testing.T) {
	estimator := &countingEstimator{}
	manager, err := NewContextManager(estimator)
	if err != nil {
		t.Fatalf("NewContextManager: %v", err)
	}
	items := []ContextItem{
		{ID: "system", Text: "12345", Required: true},
		{ID: "newest", Text: "6789", Priority: 10},
		{ID: "older", Text: "abc", Priority: 1},
	}
	selection, err := manager.Select(items, 9)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(selection.Included) != 2 || selection.Included[0].ID != "system" || selection.Included[1].ID != "newest" {
		t.Fatalf("included = %+v", selection.Included)
	}
	if len(selection.Dropped) != 1 || selection.Dropped[0].ID != "older" || selection.Tokens != 9 {
		t.Fatalf("selection = %+v", selection)
	}
	if estimator.calls != len(items) {
		t.Fatalf("estimator calls = %d, want %d", estimator.calls, len(items))
	}

	if _, err := manager.Select([]ContextItem{{ID: "required", Text: "too-long", Required: true}}, 2); !errors.Is(err, ErrContextBudgetExceeded) {
		t.Fatalf("required overflow error = %v, want ErrContextBudgetExceeded", err)
	}
}

func TestRuntimeRecordUsageDistinguishesReportedEstimatedAndNotApplicableCost(t *testing.T) {
	_, runtime, _, _, _, _, meter, _ := runtimeFixture(t, MutationNone)
	now := time.Now()
	tests := []struct {
		name    string
		record  UsageRecord
		wantErr bool
	}{
		{
			name: "reported",
			record: UsageRecord{UnitID: "u1", SessionID: "chat-1", UnitKind: UsageUnitAI, Operation: "chat",
				TokensIn: 10, TokensOut: 5, Cost: 0.001, Currency: "USD", CostBasis: CostProviderReported,
				Estimated: false, StartedAt: now, CompletedAt: now.Add(time.Second), Success: true},
		},
		{
			name: "estimated",
			record: UsageRecord{UnitID: "u2", SessionID: "plan-1", UnitKind: UsageUnitAI, Operation: "plan",
				TokensIn: 10, TokensOut: 5, Cost: 0.001, Currency: "USD", CostBasis: CostEstimated,
				Estimated: true, StartedAt: now, CompletedAt: now.Add(time.Second), Success: true},
		},
		{
			name: "estimate mislabeled as bill",
			record: UsageRecord{UnitID: "u3", SessionID: "goal-1", UnitKind: UsageUnitAI, Operation: "goal",
				Cost: 0.001, Currency: "USD", CostBasis: CostEstimated, Estimated: false,
				StartedAt: now, CompletedAt: now.Add(time.Second), Success: true},
			wantErr: true,
		},
		{
			name: "not applicable with cost",
			record: UsageRecord{UnitID: "u4", SessionID: "workflow-1", UnitKind: UsageUnitTool, Operation: "read",
				Cost: 1, CostBasis: CostNotApplicable, StartedAt: now, CompletedAt: now.Add(time.Second), Success: true},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runtime.RecordUsage(tt.record)
			if tt.wantErr && !errors.Is(err, ErrInvalidUsageRecord) {
				t.Fatalf("RecordUsage error = %v, want ErrInvalidUsageRecord", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("RecordUsage: %v", err)
			}
		})
	}
	if len(meter.records) != 2 {
		t.Fatalf("meter records = %d, want 2 valid records", len(meter.records))
	}
}
