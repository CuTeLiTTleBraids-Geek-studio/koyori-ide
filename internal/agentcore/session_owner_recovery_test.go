package agentcore

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type ownerPublicationPersistence struct {
	file      FileSessionStorePersistence
	saveCalls int
	published [][]Session
}

func (p *ownerPublicationPersistence) Load() ([]Session, error) {
	return p.file.Load()
}

func (p *ownerPublicationPersistence) Save(rows []Session) (PersistenceCommitState, error) {
	p.saveCalls++
	snapshot := make([]Session, len(rows))
	for index := range rows {
		row := rows[index]
		snapshot[index] = cloneSession(&row)
	}
	p.published = append(p.published, snapshot)
	return p.file.Save(rows)
}

func TestPersistentSessionStoreBeginOwnedPublishesOwnerWithRowAndQuarantinesOnRestart(t *testing.T) {
	persistence := &ownerPublicationPersistence{
		file: FileSessionStorePersistence{Path: filepath.Join(t.TempDir(), "sessions.json")},
	}
	clock := time.Date(2026, 8, 16, 4, 0, 0, 0, time.UTC)
	store, err := NewPersistentSessionStore(persistence, func() time.Time { return clock })
	if err != nil {
		t.Fatalf("NewPersistentSessionStore: %v", err)
	}
	owner := SessionOwner{
		Domain:               "workflow-service",
		RuntimeID:            "workflow:runtime-authority",
		WorkspaceGeneration:  17,
		WorkspaceFingerprint: strings.Repeat("ab", 32),
		Incarnation:          "incarnation-before-restart",
	}

	created, err := store.BeginOwned("workflow:owned-publication", SessionWorkflow, owner)
	if err != nil {
		t.Fatalf("BeginOwned: %v", err)
	}
	if created.Owner == nil || *created.Owner != owner {
		t.Fatalf("created owner = %+v, want %+v", created.Owner, owner)
	}
	if persistence.saveCalls != 1 {
		t.Fatalf("BeginOwned Save calls = %d, want one atomic row+owner publication", persistence.saveCalls)
	}
	if len(persistence.published) != 1 || len(persistence.published[0]) != 1 {
		t.Fatalf("published snapshots = %+v, want one snapshot containing one row", persistence.published)
	}
	published := persistence.published[0][0]
	if published.ID != created.ID || published.Kind != SessionWorkflow || published.Owner == nil || *published.Owner != owner {
		t.Fatalf("first durable publication = %+v, want row and owner together", published)
	}

	restarted, err := NewPersistentSessionStore(persistence, func() time.Time { return clock.Add(time.Minute) })
	if err != nil {
		t.Fatalf("restart persistent store: %v", err)
	}
	recovered, err := restarted.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if recovered.Status != SessionRunning || recovered.Recovery != SessionRecoveryRequired {
		t.Fatalf("recovered lifecycle state = %+v, want running row quarantined for recovery", recovered)
	}
	if recovered.Owner == nil {
		t.Fatal("restart discarded the durable owner claim")
	}
	if recovered.Owner.RuntimeID != "" {
		t.Fatalf("restart retained runtime authority %q", recovered.Owner.RuntimeID)
	}
	if recovered.Owner.Domain != owner.Domain ||
		recovered.Owner.Incarnation != owner.Incarnation ||
		recovered.Owner.WorkspaceFingerprint != owner.WorkspaceFingerprint ||
		recovered.Owner.WorkspaceGeneration != owner.WorkspaceGeneration {
		t.Fatalf("restart changed durable owner claim = %+v, want metadata from %+v", recovered.Owner, owner)
	}
}
