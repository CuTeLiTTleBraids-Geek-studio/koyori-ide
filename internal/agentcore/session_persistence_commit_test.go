package agentcore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileSessionStorePersistenceReportsPublicationBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	oldRows := []Session{{
		ID: "chat:old", Kind: SessionChat, Status: SessionCompleted,
		Stream: []StreamEvent{}, Checkpoints: []SessionCheckpoint{},
	}}
	newRows := []Session{{
		ID: "chat:new", Kind: SessionChat, Status: SessionCompleted,
		Stream: []StreamEvent{}, Checkpoints: []SessionCheckpoint{},
	}}

	state, err := (FileSessionStorePersistence{Path: path}).Save(oldRows)
	if err != nil || state != PersistenceDurable {
		t.Fatalf("initial Save state = %v, err = %v, want PersistenceDurable", state, err)
	}
	oldBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read initial snapshot: %v", err)
	}

	prePublishFault := errors.New("injected pre-publish failure")
	state, err = (FileSessionStorePersistence{
		Path: path,
		replaceForTest: func(_, _ string) error {
			return prePublishFault
		},
	}).Save(newRows)
	if !errors.Is(err, prePublishFault) || state != PersistenceNotPublished {
		t.Fatalf("pre-publish Save state = %v, err = %v, want PersistenceNotPublished with injected fault", state, err)
	}
	afterPrePublish, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot after pre-publish failure: %v", err)
	}
	if !bytes.Equal(afterPrePublish, oldBytes) {
		t.Fatal("pre-publish failure replaced the last durable snapshot")
	}

	postRenameFault := errors.New("injected directory sync failure")
	state, err = (FileSessionStorePersistence{
		Path: path,
		syncDirectoryForTest: func(string) error {
			return postRenameFault
		},
	}).Save(newRows)
	if !errors.Is(err, postRenameFault) || state != PersistencePublishedDurabilityUnknown {
		t.Fatalf("post-rename Save state = %v, err = %v, want PersistencePublishedDurabilityUnknown with injected fault", state, err)
	}
	newBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot after post-rename failure: %v", err)
	}
	if bytes.Equal(newBytes, oldBytes) {
		t.Fatal("post-rename failure did not leave the newly published snapshot visible")
	}
	loaded, err := (FileSessionStorePersistence{Path: path}).Load()
	if err != nil {
		t.Fatalf("load post-rename snapshot: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "chat:new" {
		t.Fatalf("post-rename snapshot rows = %+v, want chat:new", loaded)
	}
}

func TestSessionStorePrePublishFailureRollsBackMemoryAndCanRetry(t *testing.T) {
	path, beforeMemory, oldBytes := prepareRecoveryDispositionSnapshot(t, "workflow:pre-publish")
	prePublishFault := errors.New("injected pre-publish failure")
	var replaceCalls atomic.Int32
	persistence := FileSessionStorePersistence{
		Path: path,
		replaceForTest: func(temp, target string) error {
			if replaceCalls.Add(1) == 1 {
				return prePublishFault
			}
			return replaceSessionSnapshot(temp, target)
		},
	}
	store, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("load recovery snapshot with pre-publish injector: %v", err)
	}

	if _, err := store.ApplyRecoveryDisposition(beforeMemory.ID, RecoveryDispositionDiscard); !errors.Is(err, prePublishFault) {
		t.Fatalf("ApplyRecoveryDisposition pre-publish error = %v, want injected fault", err)
	}
	afterMemory, err := store.Get(beforeMemory.ID)
	if err != nil {
		t.Fatalf("Get after pre-publish failure: %v", err)
	}
	if !reflect.DeepEqual(afterMemory, beforeMemory) {
		t.Fatalf("pre-publish failure changed memory:\n got  %+v\n want %+v", afterMemory, beforeMemory)
	}
	afterBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot after pre-publish failure: %v", err)
	}
	if !bytes.Equal(afterBytes, oldBytes) {
		t.Fatal("pre-publish failure changed durable bytes")
	}
	fresh, err := NewPersistentSessionStore(FileSessionStorePersistence{Path: path}, time.Now)
	if err != nil {
		t.Fatalf("fresh reload after pre-publish failure: %v", err)
	}
	freshRow, err := fresh.Get(beforeMemory.ID)
	if err != nil {
		t.Fatalf("Get after fresh reload: %v", err)
	}
	if !reflect.DeepEqual(freshRow, beforeMemory) {
		t.Fatalf("fresh reload observed non-durable mutation:\n got  %+v\n want %+v", freshRow, beforeMemory)
	}

	disposed, err := store.ApplyRecoveryDisposition(beforeMemory.ID, RecoveryDispositionDiscard)
	if err != nil {
		t.Fatalf("retry after pre-publish failure: %v", err)
	}
	assertDiscardedRecoverySession(t, disposed)
	if replaceCalls.Load() != 2 {
		t.Fatalf("replace calls = %d, want failed attempt plus durable retry", replaceCalls.Load())
	}
}

func TestSessionStorePostRenameFailureKeepsPublishedMemoryAndPoisonsMutations(t *testing.T) {
	path, beforeMemory, oldBytes := prepareRecoveryDispositionSnapshot(t, "workflow:post-rename")
	postRenameFault := errors.New("injected directory sync failure")
	var syncCalls atomic.Int32
	persistence := FileSessionStorePersistence{
		Path: path,
		syncDirectoryForTest: func(dir string) error {
			if syncCalls.Add(1) == 1 {
				return postRenameFault
			}
			return syncSessionSnapshotDirectory(dir)
		},
	}
	store, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("load recovery snapshot with post-rename injector: %v", err)
	}

	if _, err := store.ApplyRecoveryDisposition(beforeMemory.ID, RecoveryDispositionDiscard); !errors.Is(err, ErrSessionPersistenceIndeterminate) || !strings.Contains(err.Error(), postRenameFault.Error()) {
		t.Fatalf("ApplyRecoveryDisposition post-rename error = %v, want injected fault and ErrSessionPersistenceIndeterminate", err)
	}
	afterMemory, err := store.Get(beforeMemory.ID)
	if err != nil {
		t.Fatalf("Get after post-rename failure: %v", err)
	}
	assertDiscardedRecoverySession(t, afterMemory)
	if reflect.DeepEqual(afterMemory, beforeMemory) {
		t.Fatal("post-rename failure rolled memory back behind the published snapshot")
	}
	newBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot after post-rename failure: %v", err)
	}
	if bytes.Equal(newBytes, oldBytes) {
		t.Fatal("post-rename failure left old durable bytes in place")
	}

	fresh, err := NewPersistentSessionStore(FileSessionStorePersistence{Path: path}, time.Now)
	if err != nil {
		t.Fatalf("fresh reload after post-rename failure: %v", err)
	}
	freshRow, err := fresh.Get(beforeMemory.ID)
	if err != nil {
		t.Fatalf("Get published row after fresh reload: %v", err)
	}
	if !sessionsPersistentlyEqual(t, freshRow, afterMemory) {
		t.Fatalf("fresh reload disagrees with retained memory:\n got  %+v\n want %+v", freshRow, afterMemory)
	}

	if _, err := store.Begin("workflow:must-not-publish", SessionWorkflow); !errors.Is(err, ErrSessionPersistenceIndeterminate) {
		t.Fatalf("mutation after indeterminate publication error = %v, want ErrSessionPersistenceIndeterminate", err)
	}
	afterRejectedMutation, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot after rejected mutation: %v", err)
	}
	if !bytes.Equal(afterRejectedMutation, newBytes) {
		t.Fatal("poisoned store published another mutation")
	}
	if _, err := store.Get("workflow:must-not-publish"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("rejected mutation leaked into memory: %v", err)
	}
}

func TestSessionStorePostRenameUnknownMutationMatrixKeepsPublishedStateAndPoisons(t *testing.T) {
	tests := []struct {
		name   string
		seed   func(*testing.T, *SessionStore)
		mutate func(*testing.T, *SessionStore) error
		assert func(*testing.T, *SessionStore)
	}{
		{
			name: "Begin",
			mutate: func(t *testing.T, store *SessionStore) error {
				t.Helper()
				_, err := store.Begin("workflow:matrix-begin", SessionWorkflow)
				return err
			},
			assert: func(t *testing.T, store *SessionStore) {
				t.Helper()
				row, err := store.Get("workflow:matrix-begin")
				if err != nil || row.Status != SessionRunning {
					t.Fatalf("published Begin row = %+v, err = %v", row, err)
				}
			},
		},
		{
			name: "AppendStream",
			seed: func(t *testing.T, store *SessionStore) {
				t.Helper()
				if _, err := store.Begin("workflow:matrix-stream", SessionWorkflow); err != nil {
					t.Fatalf("seed stream session: %v", err)
				}
			},
			mutate: func(t *testing.T, store *SessionStore) error {
				t.Helper()
				_, err := store.AppendStream("workflow:matrix-stream", StreamEventInput{Kind: StreamDelta, Data: "published-delta"})
				return err
			},
			assert: func(t *testing.T, store *SessionStore) {
				t.Helper()
				row, err := store.Get("workflow:matrix-stream")
				if err != nil || len(row.Stream) != 1 || row.Stream[0].Data != "published-delta" {
					t.Fatalf("published stream row = %+v, err = %v", row, err)
				}
			},
		},
		{
			name: "BindOwner",
			seed: func(t *testing.T, store *SessionStore) {
				t.Helper()
				if _, err := store.Begin("workflow:matrix-owner", SessionWorkflow); err != nil {
					t.Fatalf("seed owner session: %v", err)
				}
			},
			mutate: func(t *testing.T, store *SessionStore) error {
				t.Helper()
				return store.BindOwner("workflow:matrix-owner", SessionOwner{
					Domain: "workflow-service", RuntimeID: "workflow:matrix-runtime", Incarnation: "matrix-incarnation",
				})
			},
			assert: func(t *testing.T, store *SessionStore) {
				t.Helper()
				row, err := store.Get("workflow:matrix-owner")
				if err != nil || row.Owner == nil || row.Owner.Domain != "workflow-service" || row.Owner.RuntimeID != "workflow:matrix-runtime" || row.Owner.Incarnation != "matrix-incarnation" {
					t.Fatalf("published owner row = %+v, err = %v", row, err)
				}
			},
		},
		{
			name: "Delete",
			seed: func(t *testing.T, store *SessionStore) {
				t.Helper()
				if _, err := store.Begin("workflow:matrix-delete", SessionWorkflow); err != nil {
					t.Fatalf("seed delete session: %v", err)
				}
			},
			mutate: func(t *testing.T, store *SessionStore) error {
				t.Helper()
				return store.Delete("workflow:matrix-delete")
			},
			assert: func(t *testing.T, store *SessionStore) {
				t.Helper()
				if _, err := store.Get("workflow:matrix-delete"); !errors.Is(err, ErrSessionNotFound) {
					t.Fatalf("published delete left row in memory: %v", err)
				}
			},
		},
		{
			name: "CloseAllDurable",
			seed: func(t *testing.T, store *SessionStore) {
				t.Helper()
				if _, err := store.Begin("workflow:matrix-close-running", SessionWorkflow); err != nil {
					t.Fatalf("seed running close session: %v", err)
				}
				if _, err := store.Begin("workflow:matrix-close-paused", SessionWorkflow); err != nil {
					t.Fatalf("seed paused close session: %v", err)
				}
				if err := store.Pause("workflow:matrix-close-paused"); err != nil {
					t.Fatalf("pause close session: %v", err)
				}
			},
			mutate: func(t *testing.T, store *SessionStore) error {
				t.Helper()
				_, err := store.CloseAllDurable()
				return err
			},
			assert: func(t *testing.T, store *SessionStore) {
				t.Helper()
				for _, id := range []string{"workflow:matrix-close-running", "workflow:matrix-close-paused"} {
					row, err := store.Get(id)
					if err != nil || row.Status != SessionCompleted || row.CompletedAt == nil {
						t.Fatalf("published CloseAllDurable row %q = %+v, err = %v", id, row, err)
					}
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.json")
			durablePersistence := FileSessionStorePersistence{Path: path}
			state, err := durablePersistence.Save([]Session{})
			if err != nil || state != PersistenceDurable {
				t.Fatalf("seed empty snapshot state = %v, err = %v", state, err)
			}
			store, err := NewPersistentSessionStore(durablePersistence, time.Now)
			if err != nil {
				t.Fatalf("create matrix session store: %v", err)
			}
			if test.seed != nil {
				test.seed(t, store)
			}
			oldBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read pre-mutation snapshot: %v", err)
			}

			postRenameFault := errors.New("injected matrix directory sync failure")
			var syncCalls atomic.Int32
			store.persistence = FileSessionStorePersistence{
				Path: path,
				syncDirectoryForTest: func(string) error {
					syncCalls.Add(1)
					return postRenameFault
				},
			}
			if err := test.mutate(t, store); !errors.Is(err, ErrSessionPersistenceIndeterminate) || !strings.Contains(err.Error(), postRenameFault.Error()) {
				t.Fatalf("%s post-rename error = %v, want publication-indeterminate fault", test.name, err)
			}
			if syncCalls.Load() != 1 {
				t.Fatalf("%s directory sync calls = %d, want 1", test.name, syncCalls.Load())
			}
			test.assert(t, store)

			newBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read post-mutation snapshot: %v", err)
			}
			if bytes.Equal(newBytes, oldBytes) {
				t.Fatalf("%s did not replace the published snapshot", test.name)
			}
			persistedRows, err := durablePersistence.Load()
			if err != nil {
				t.Fatalf("load post-mutation snapshot: %v", err)
			}
			memoryRows := sessionStoreSnapshot(t, store)
			if !sessionSlicesPersistentlyEqual(memoryRows, persistedRows) {
				t.Fatalf("%s memory disagrees with published disk rows:\n memory %+v\n disk   %+v", test.name, memoryRows, persistedRows)
			}

			if _, err := store.Begin("workflow:matrix-poison-probe", SessionWorkflow); !errors.Is(err, ErrSessionPersistenceIndeterminate) || !errors.Is(err, ErrSessionPersistencePoisoned) {
				t.Fatalf("%s poisoned mutation error = %v, want persistence sentinels", test.name, err)
			}
			if syncCalls.Load() != 1 {
				t.Fatalf("%s poisoned store called persistence again: sync calls = %d", test.name, syncCalls.Load())
			}
			if _, err := store.Get("workflow:matrix-poison-probe"); !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("%s poisoned mutation leaked into memory: %v", test.name, err)
			}
			if got := sessionStoreSnapshot(t, store); !sessionSlicesPersistentlyEqual(got, memoryRows) {
				t.Fatalf("%s poisoned mutation changed memory:\n got  %+v\n want %+v", test.name, got, memoryRows)
			}
			afterProbeBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read snapshot after poison probe: %v", err)
			}
			if !bytes.Equal(afterProbeBytes, newBytes) {
				t.Fatalf("%s poisoned mutation changed disk", test.name)
			}
		})
	}
}

func TestSessionStoreConcurrentDispositionUnknownNeverReportsSuccess(t *testing.T) {
	path, beforeMemory, _ := prepareRecoveryDispositionSnapshot(t, "workflow:concurrent-discard")
	postRenameFault := errors.New("injected directory sync failure")
	var syncCalls atomic.Int32
	store, err := NewPersistentSessionStore(FileSessionStorePersistence{
		Path: path,
		syncDirectoryForTest: func(dir string) error {
			if syncCalls.Add(1) == 1 {
				return postRenameFault
			}
			return syncSessionSnapshotDirectory(dir)
		},
	}, time.Now)
	if err != nil {
		t.Fatalf("load recovery snapshot with post-rename injector: %v", err)
	}

	const callers = 12
	type dispositionResult struct {
		session Session
		err     error
	}
	results := make([]dispositionResult, callers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := range results {
		go func(index int) {
			defer wait.Done()
			<-start
			results[index].session, results[index].err = store.ApplyRecoveryDisposition(beforeMemory.ID, RecoveryDispositionDiscard)
		}(index)
	}
	close(start)
	wait.Wait()

	unknownErrors := 0
	poisonedErrors := 0
	for _, result := range results {
		if result.err == nil {
			t.Fatalf("concurrent disposition reported success after unknown publication: %+v", result.session)
		}
		if !errors.Is(result.err, ErrSessionPersistenceIndeterminate) || !strings.Contains(result.err.Error(), postRenameFault.Error()) {
			t.Fatalf("concurrent disposition error = %v, want publication-indeterminate fault", result.err)
		}
		unknownErrors++
		if errors.Is(result.err, ErrSessionPersistencePoisoned) {
			poisonedErrors++
		}
	}
	if unknownErrors != callers || poisonedErrors != callers-1 {
		t.Fatalf("concurrent results: indeterminate=%d poisoned=%d, want %d and %d", unknownErrors, poisonedErrors, callers, callers-1)
	}
	if syncCalls.Load() != 1 {
		t.Fatalf("directory sync calls = %d, want one publication attempt", syncCalls.Load())
	}
	got, err := store.Get(beforeMemory.ID)
	if err != nil {
		t.Fatalf("Get indeterminate disposition: %v", err)
	}
	assertDiscardedRecoverySession(t, got)
	if rows, err := store.RecoveryRequired(); !errors.Is(err, ErrSessionPersistencePoisoned) || rows != nil {
		t.Fatalf("poisoned recovery inventory = %+v, err=%v; want nil/ErrSessionPersistencePoisoned", rows, err)
	}
	if replay, err := store.ApplyRecoveryDisposition(beforeMemory.ID, RecoveryDispositionDiscard); !errors.Is(err, ErrSessionPersistencePoisoned) || replay.ID != "" {
		t.Fatalf("poisoned replay = %+v, err=%v; want ErrSessionPersistencePoisoned", replay, err)
	}
	if _, err := store.Begin("workflow:poisoned-after-convergence", SessionWorkflow); !errors.Is(err, ErrSessionPersistenceIndeterminate) {
		t.Fatalf("new mutation after convergence error = %v, want ErrSessionPersistenceIndeterminate", err)
	}

	reloaded, err := NewPersistentSessionStore(FileSessionStorePersistence{Path: path}, time.Now)
	if err != nil {
		t.Fatalf("reload published disposition: %v", err)
	}
	confirmed, err := reloaded.ApplyRecoveryDisposition(beforeMemory.ID, RecoveryDispositionDiscard)
	if err != nil {
		t.Fatalf("confirm disposition after restart: %v", err)
	}
	assertDiscardedRecoverySession(t, confirmed)
}

func prepareRecoveryDispositionSnapshot(t *testing.T, id string) (string, Session, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.json")
	persistence := FileSessionStorePersistence{Path: path}
	store, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("create persistent session store: %v", err)
	}
	if _, err := store.Begin(id, SessionWorkflow); err != nil {
		t.Fatalf("Begin recovery seed: %v", err)
	}
	if err := store.BindOwner(id, SessionOwner{
		Domain: "workflow-service", RuntimeID: "workflow:old-runtime", Incarnation: "old-incarnation",
	}); err != nil {
		t.Fatalf("BindOwner recovery seed: %v", err)
	}
	if err := store.Pause(id); err != nil {
		t.Fatalf("Pause recovery seed: %v", err)
	}

	recovered, err := NewPersistentSessionStore(persistence, time.Now)
	if err != nil {
		t.Fatalf("normalize recovery snapshot: %v", err)
	}
	row, err := recovered.Get(id)
	if err != nil {
		t.Fatalf("Get normalized recovery row: %v", err)
	}
	if row.Status != SessionPaused || row.Recovery != SessionRecoveryRequired || row.RecoveryDisposition != "" || row.Owner == nil || row.Owner.RuntimeID != "" {
		t.Fatalf("normalized recovery row = %+v", row)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read normalized recovery snapshot: %v", err)
	}
	return path, row, data
}

func assertDiscardedRecoverySession(t *testing.T, session Session) {
	t.Helper()
	if session.Status != SessionCompleted || session.Recovery != SessionRecoveryNone || session.RecoveryDisposition != RecoveryDispositionDiscard || session.CompletedAt == nil {
		t.Fatalf("discarded recovery session = %+v", session)
	}
	if session.Owner == nil || session.Owner.RuntimeID != "" {
		t.Fatalf("discarded recovery session restored runtime authority: %+v", session.Owner)
	}
}

func sessionsPersistentlyEqual(t *testing.T, left, right Session) bool {
	t.Helper()
	return reflect.DeepEqual(normalizeSessionTimes(left), normalizeSessionTimes(right))
}

func sessionSlicesPersistentlyEqual(left, right []Session) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !reflect.DeepEqual(normalizeSessionTimes(left[index]), normalizeSessionTimes(right[index])) {
			return false
		}
	}
	return true
}

func normalizeSessionTimes(session Session) Session {
	session = cloneSession(&session)
	session.StartedAt = session.StartedAt.UTC()
	session.UpdatedAt = session.UpdatedAt.UTC()
	if session.CompletedAt != nil {
		completedAt := session.CompletedAt.UTC()
		session.CompletedAt = &completedAt
	}
	for index := range session.Stream {
		session.Stream[index].Timestamp = session.Stream[index].Timestamp.UTC()
	}
	for index := range session.Checkpoints {
		session.Checkpoints[index].CreatedAt = session.Checkpoints[index].CreatedAt.UTC()
	}
	return session
}

func sessionStoreSnapshot(t *testing.T, store *SessionStore) []Session {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.snapshotLocked()
}
