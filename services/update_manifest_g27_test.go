package services

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"
)

func newG27Fixture(t *testing.T) (UpdateManifest, *UpdateManifestVerifier, ed25519.PrivateKey, time.Time) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	manifest := UpdateManifest{
		SchemaVersion: 1, Channel: "stable", Version: "2.0.0", Commit: "abc123",
		Platform: "linux", Arch: "amd64", ArtifactName: "koyori-linux-amd64.zip",
		ArtifactSHA256: strings.Repeat("a", 64), CreatedAt: now.Add(-time.Hour).Format(time.RFC3339), KeyID: "release-2026",
	}
	if err := SignUpdateManifest(&manifest, privateKey); err != nil {
		t.Fatal(err)
	}
	verifier := NewUpdateManifestVerifier(map[string]ed25519.PublicKey{"release-2026": publicKey})
	verifier.Now = func() time.Time { return now }
	return manifest, verifier, privateKey, now
}

func TestUpdateManifestSignatureAndTamper(t *testing.T) {
	manifest, verifier, _, _ := newG27Fixture(t)
	if err := verifier.VerifyManifest(manifest); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	tests := []struct {
		name   string
		tamper func(*UpdateManifest)
	}{
		{"payload", func(m *UpdateManifest) { m.Commit = "different" }},
		{"digest", func(m *UpdateManifest) { m.ArtifactSHA256 = strings.Repeat("b", 64) }},
		{"version", func(m *UpdateManifest) { m.Version = "2.0.1" }},
		{"channel", func(m *UpdateManifest) { m.Channel = "beta" }},
		{"key", func(m *UpdateManifest) { m.KeyID = "unknown" }},
		{"signature", func(m *UpdateManifest) { m.Signature = strings.Repeat("A", len(m.Signature)) }},
		{"missing signature", func(m *UpdateManifest) { m.Signature = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := manifest
			test.tamper(&candidate)
			if err := verifier.VerifyManifest(candidate); err == nil {
				t.Fatal("tampered manifest accepted")
			}
		})
	}
}

func TestUpdateManifestFailClosedFieldsAndTime(t *testing.T) {
	manifest, verifier, privateKey, now := newG27Fixture(t)
	tests := []struct {
		name   string
		change func(*UpdateManifest)
	}{
		{"schema", func(m *UpdateManifest) { m.SchemaVersion = 2 }},
		{"unknown channel", func(m *UpdateManifest) { m.Channel = "nightly" }},
		{"unknown platform", func(m *UpdateManifest) { m.Platform = "plan9" }},
		{"unknown arch", func(m *UpdateManifest) { m.Arch = "386" }},
		{"bad digest", func(m *UpdateManifest) { m.ArtifactSHA256 = "bad" }},
		{"missing field", func(m *UpdateManifest) { m.ArtifactName = "" }},
		{"expired", func(m *UpdateManifest) { m.CreatedAt = now.Add(-8 * 24 * time.Hour).Format(time.RFC3339) }},
		{"future", func(m *UpdateManifest) { m.CreatedAt = now.Add(6 * time.Minute).Format(time.RFC3339) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := manifest
			test.change(&candidate)
			if err := SignUpdateManifest(&candidate, privateKey); err != nil {
				t.Fatal(err)
			}
			if err := verifier.VerifyManifest(candidate); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestUpdateManifestArtifactNameCrossPlatform(t *testing.T) {
	manifest, verifier, privateKey, _ := newG27Fixture(t)
	invalid := []string{"", ".", "..", "dir/file.zip", `dir\file.zip`, `C:\file.zip`, `\\server\share.zip`, "CON", "con.txt", "NUL.zip", "LPT1", "name.", "name ", "bad\x00name", "bad\nname"}
	for _, name := range invalid {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			candidate := manifest
			candidate.ArtifactName = name
			if err := SignUpdateManifest(&candidate, privateKey); err != nil {
				t.Fatal(err)
			}
			if err := verifier.VerifyManifest(candidate); err == nil {
				t.Fatalf("unsafe artifactName accepted: %q", name)
			}
		})
	}
	for _, name := range []string{"koyori-linux-amd64.zip", "com10.zip", "release..zip"} {
		candidate := manifest
		candidate.ArtifactName = name
		if err := SignUpdateManifest(&candidate, privateKey); err != nil {
			t.Fatal(err)
		}
		if err := verifier.VerifyManifest(candidate); err != nil {
			t.Fatalf("safe artifactName %q rejected: %v", name, err)
		}
	}
}

func TestUpdateManifestCandidateBindingAndDowngrade(t *testing.T) {
	manifest, verifier, privateKey, _ := newG27Fixture(t)
	policy := UpdateCandidatePolicy{
		CurrentVersion: "1.0.0", CurrentCommit: "previous123", CurrentChannel: "stable", TransactionID: "tx-upgrade",
		ExpectedPlatform: "linux", ExpectedArch: "amd64",
		ExpectedArtifactName: manifest.ArtifactName, ExpectedArtifactSHA256: manifest.ArtifactSHA256,
	}
	if err := verifier.VerifyCandidate(manifest, policy, nil); err != nil {
		t.Fatalf("upgrade rejected: %v", err)
	}

	checks := []struct {
		name string
		edit func(*UpdateCandidatePolicy)
	}{
		{"channel", func(p *UpdateCandidatePolicy) { p.CurrentChannel = "beta" }},
		{"platform", func(p *UpdateCandidatePolicy) { p.ExpectedPlatform = "windows" }},
		{"arch", func(p *UpdateCandidatePolicy) { p.ExpectedArch = "arm64" }},
		{"name", func(p *UpdateCandidatePolicy) { p.ExpectedArtifactName = "other.zip" }},
		{"digest", func(p *UpdateCandidatePolicy) { p.ExpectedArtifactSHA256 = strings.Repeat("b", 64) }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			changed := policy
			check.edit(&changed)
			if err := verifier.VerifyCandidate(manifest, changed, nil); err == nil {
				t.Fatal("mismatched candidate accepted")
			}
		})
	}

	manifest.Version = "0.9.0"
	if err := SignUpdateManifest(&manifest, privateKey); err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyCandidate(manifest, policy, nil); err == nil {
		t.Fatal("unauthorized downgrade accepted")
	}
}

func TestRollbackAuthorization(t *testing.T) {
	manifest, verifier, privateKey, now := newG27Fixture(t)
	manifest.Version = "0.9.0"
	if err := SignUpdateManifest(&manifest, privateKey); err != nil {
		t.Fatal(err)
	}
	policy := UpdateCandidatePolicy{
		CurrentVersion: "1.0.0", CurrentCommit: "previous123", CurrentChannel: "stable", TransactionID: "tx-rollback-1",
		ExpectedPlatform: manifest.Platform, ExpectedArch: manifest.Arch,
		ExpectedArtifactName: manifest.ArtifactName, ExpectedArtifactSHA256: manifest.ArtifactSHA256,
	}
	authorization := RollbackAuthorization{
		SchemaVersion: 1, Domain: RollbackAuthorizationDomain, Purpose: RollbackAuthorizationPurpose,
		TransactionID: policy.TransactionID, Nonce: "nonce-rollback-1", Channel: manifest.Channel,
		Platform: manifest.Platform, Arch: manifest.Arch, ArtifactName: manifest.ArtifactName,
		ArtifactSHA256: manifest.ArtifactSHA256, FromVersion: policy.CurrentVersion, FromCommit: policy.CurrentCommit,
		TargetVersion: manifest.Version, TargetCommit: manifest.Commit, Reason: "critical regression",
		ExpiresAt: now.Add(time.Hour).Format(time.RFC3339), KeyID: manifest.KeyID,
	}
	if err := SignRollbackAuthorization(&authorization, privateKey); err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyCandidate(manifest, policy, &authorization); err != nil {
		t.Fatalf("authorized rollback rejected: %v", err)
	}
	if err := verifier.VerifyCandidate(manifest, policy, &authorization); err == nil {
		t.Fatal("consumed rollback authorization replay accepted")
	}

	tampered := authorization
	tampered.Reason = "different"
	if err := verifier.VerifyCandidate(manifest, policy, &tampered); err == nil {
		t.Fatal("tampered rollback authorization accepted")
	}
	expired := authorization
	expired.ExpiresAt = now.Format(time.RFC3339)
	if err := SignRollbackAuthorization(&expired, privateKey); err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyCandidate(manifest, policy, &expired); err == nil {
		t.Fatal("expired rollback authorization accepted")
	}
}

func TestRollbackAuthorizationScopeAndConcurrentReplay(t *testing.T) {
	manifest, verifier, privateKey, now := newG27Fixture(t)
	scope := RollbackAuthorizationScope{
		TransactionID: "tx-scope", Channel: manifest.Channel, Platform: manifest.Platform, Arch: manifest.Arch,
		ArtifactName: manifest.ArtifactName, ArtifactSHA256: manifest.ArtifactSHA256,
		FromVersion: "2.0.0", FromCommit: "fromcommit", TargetVersion: "1.9.0", TargetCommit: "targetcommit",
	}
	authorization := RollbackAuthorization{
		SchemaVersion: 1, Domain: RollbackAuthorizationDomain, Purpose: RollbackAuthorizationPurpose,
		TransactionID: scope.TransactionID, Nonce: "nonce-concurrent", Channel: scope.Channel,
		Platform: scope.Platform, Arch: scope.Arch, ArtifactName: scope.ArtifactName,
		ArtifactSHA256: scope.ArtifactSHA256, FromVersion: scope.FromVersion, FromCommit: scope.FromCommit,
		TargetVersion: scope.TargetVersion, TargetCommit: scope.TargetCommit, Reason: "regression",
		ExpiresAt: now.Add(time.Hour).Format(time.RFC3339), KeyID: manifest.KeyID,
	}
	if err := SignRollbackAuthorization(&authorization, privateKey); err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyRollbackAuthorization(authorization, RollbackAuthorizationScope{}); err == nil {
		t.Fatal("incomplete rollback scope accepted")
	}
	mutations := []func(*RollbackAuthorizationScope){
		func(s *RollbackAuthorizationScope) { s.TransactionID = "other-tx" },
		func(s *RollbackAuthorizationScope) { s.Channel = "beta" },
		func(s *RollbackAuthorizationScope) { s.Platform = "windows" },
		func(s *RollbackAuthorizationScope) { s.Arch = "arm64" },
		func(s *RollbackAuthorizationScope) { s.ArtifactName = "other.zip" },
		func(s *RollbackAuthorizationScope) { s.ArtifactSHA256 = strings.Repeat("b", 64) },
		func(s *RollbackAuthorizationScope) { s.FromCommit = "other-from" },
		func(s *RollbackAuthorizationScope) { s.TargetVersion = "1.8.0" },
		func(s *RollbackAuthorizationScope) { s.TargetCommit = "other-target" },
	}
	for i, mutate := range mutations {
		changed := scope
		mutate(&changed)
		if err := verifier.VerifyRollbackAuthorization(authorization, changed); err == nil {
			t.Fatalf("scope mutation %d accepted", i)
		}
	}

	const attempts = 16
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- verifier.ConsumeRollbackAuthorization(authorization, scope)
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent consumption successes = %d, want 1", successes)
	}
}

func TestUpdateTransactionLegalTransitions(t *testing.T) {
	journal := UpdateTransactionJournal{State: UpdateTransactionPrepared}
	for _, next := range []UpdateTransactionState{UpdateTransactionStaged, UpdateTransactionActivated, UpdateTransactionCommitted} {
		if err := journal.Transition(next); err != nil {
			t.Fatalf("legal transition to %q rejected: %v", next, err)
		}
	}

	for _, initial := range []UpdateTransactionState{UpdateTransactionPrepared, UpdateTransactionStaged, UpdateTransactionActivated} {
		journal.State = initial
		if err := journal.Transition(UpdateTransactionRollbackRequired); err != nil {
			t.Fatalf("rollback-required from %q rejected: %v", initial, err)
		}
		journal.State = UpdateTransactionRollbackRequired
		if err := journal.Transition(UpdateTransactionRolledBack); err != nil {
			t.Fatalf("rolled-back rejected: %v", err)
		}
		journal.State = UpdateTransactionRollbackRequired
		if err := journal.Transition(UpdateTransactionRollbackFailed); err != nil {
			t.Fatalf("rollback-failed rejected: %v", err)
		}
	}
}

func TestUpdateTransactionIllegalTransitions(t *testing.T) {
	states := []UpdateTransactionState{
		UpdateTransactionPrepared, UpdateTransactionStaged, UpdateTransactionActivated, UpdateTransactionCommitted,
		UpdateTransactionRollbackRequired, UpdateTransactionRolledBack, UpdateTransactionRollbackFailed,
	}
	allowed := map[UpdateTransactionState]map[UpdateTransactionState]bool{
		UpdateTransactionPrepared:         {UpdateTransactionStaged: true, UpdateTransactionRollbackRequired: true},
		UpdateTransactionStaged:           {UpdateTransactionActivated: true, UpdateTransactionRollbackRequired: true},
		UpdateTransactionActivated:        {UpdateTransactionCommitted: true, UpdateTransactionRollbackRequired: true},
		UpdateTransactionRollbackRequired: {UpdateTransactionRolledBack: true, UpdateTransactionRollbackFailed: true},
	}
	for _, from := range states {
		for _, to := range states {
			journal := UpdateTransactionJournal{State: from}
			err := journal.Transition(to)
			if allowed[from][to] && err != nil {
				t.Fatalf("legal transition %q -> %q rejected: %v", from, to, err)
			}
			if !allowed[from][to] && err == nil {
				t.Fatalf("illegal transition %q -> %q accepted", from, to)
			}
		}
	}

	journal := UpdateTransactionJournal{State: UpdateTransactionCommitted}
	if err := journal.Transition(UpdateTransactionCommitted); err == nil {
		t.Fatal("duplicate commit accepted")
	}
	journal.State = UpdateTransactionRollbackFailed
	if err := journal.Transition(UpdateTransactionCommitted); err == nil {
		t.Fatal("success after failure accepted")
	}
}
