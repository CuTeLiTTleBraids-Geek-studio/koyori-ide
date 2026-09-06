package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

type lifecycleRecoverySnapshot struct {
	Version  int                 `json:"version"`
	Sessions []agentcore.Session `json:"sessions"`
}

func seedDurableLifecycleRecoveryRow(t *testing.T, configDir, workspaceRoot string, kind agentcore.SessionKind) string {
	t.Helper()
	agent := newLifecycleTestAgentAtWorkspace(t, workspaceRoot)
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
		configDir,
	)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle seed: %v", err)
	}
	id, err := agent.createAgentSessionTrusted(string(kind))
	if err != nil {
		_ = agent.Close()
		t.Fatalf("CreateAgentSession seed: %v", err)
	}
	if err := lifecycle.Pause(kind, id); err != nil {
		_ = agent.Close()
		t.Fatalf("Pause seed: %v", err)
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("Close seed agent: %v", err)
	}
	return id
}

func readLifecycleRecoverySnapshot(t *testing.T, configDir string) lifecycleRecoverySnapshot {
	t.Helper()
	path := filepath.Join(configDir, "agent_lifecycle_sessions.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lifecycle snapshot: %v", err)
	}
	var snapshot lifecycleRecoverySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("decode lifecycle snapshot: %v", err)
	}
	return snapshot
}

func writeLifecycleRecoverySnapshot(t *testing.T, configDir string, snapshot lifecycleRecoverySnapshot) {
	t.Helper()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatalf("encode lifecycle snapshot: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(configDir, "agent_lifecycle_sessions.json"), data, 0o600); err != nil {
		t.Fatalf("write lifecycle snapshot: %v", err)
	}
}

func lifecycleSnapshotRow(t *testing.T, snapshot *lifecycleRecoverySnapshot, id string) *agentcore.Session {
	t.Helper()
	for index := range snapshot.Sessions {
		if snapshot.Sessions[index].ID == id {
			return &snapshot.Sessions[index]
		}
	}
	t.Fatalf("lifecycle row %q not found in snapshot", id)
	return nil
}

func TestWireAgentLifecycleFailsClosedWhenSnapshotIdentityKeyIsMissingOrCorrupt(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string) []byte
		check  func(*testing.T, string, []byte)
	}{
		{
			name: "missing",
			mutate: func(t *testing.T, path string) []byte {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove lifecycle identity key: %v", err)
				}
				return nil
			},
			check: func(t *testing.T, path string, _ []byte) {
				t.Helper()
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing lifecycle identity key was silently recreated: %v", err)
				}
			},
		},
		{
			name: "corrupt",
			mutate: func(t *testing.T, path string) []byte {
				t.Helper()
				corrupt := []byte("not-a-valid-agent-lifecycle-identity-key")
				if err := os.WriteFile(path, corrupt, 0o600); err != nil {
					t.Fatalf("corrupt lifecycle identity key: %v", err)
				}
				return corrupt
			},
			check: func(t *testing.T, path string, want []byte) {
				t.Helper()
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read corrupt lifecycle identity key after rejected wiring: %v", err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("corrupt lifecycle identity key was silently replaced: got %q, want %q", got, want)
				}
				backups, err := filepath.Glob(path + ".corrupt-*")
				if err != nil {
					t.Fatalf("glob corrupt key backups: %v", err)
				}
				if len(backups) != 0 {
					t.Fatalf("wiring rewrote corrupt lifecycle key and created backups: %v", backups)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			workspaceRoot := t.TempDir()
			seedDurableLifecycleRecoveryRow(t, configDir, workspaceRoot, agentcore.SessionWorkflow)
			keyPath := filepath.Join(configDir, "agent_lifecycle_identity.key")
			wantKeyState := test.mutate(t, keyPath)

			agent := newLifecycleTestAgentAtWorkspace(t, workspaceRoot)
			_, wireErr := WireAgentLifecycle(
				agent,
				NewAIService(),
				NewAIPlanService(),
				NewAIGoalService(),
				NewAIPermissionService(t.TempDir()),
				configDir,
			)
			_ = agent.Close()
			test.check(t, keyPath, wantKeyState)
			if wireErr == nil {
				t.Fatal("WireAgentLifecycle accepted a durable snapshot without its original identity key")
			}
		})
	}
}

func TestLifecycleRecoveryFingerprintCannotBeCopiedBetweenRows(t *testing.T) {
	configDir := t.TempDir()
	workspaceRoot := t.TempDir()
	agent := newLifecycleTestAgentAtWorkspace(t, workspaceRoot)
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
		configDir,
	)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle seed: %v", err)
	}
	ids := make([]string, 2)
	for index := range ids {
		ids[index], err = agent.createAgentSessionTrusted(string(agentcore.SessionWorkflow))
		if err != nil {
			_ = agent.Close()
			t.Fatalf("CreateAgentSession[%d]: %v", index, err)
		}
		if err := lifecycle.Pause(agentcore.SessionWorkflow, ids[index]); err != nil {
			_ = agent.Close()
			t.Fatalf("Pause[%d]: %v", index, err)
		}
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("Close seed agent: %v", err)
	}

	snapshot := readLifecycleRecoverySnapshot(t, configDir)
	source := lifecycleSnapshotRow(t, &snapshot, ids[0])
	target := lifecycleSnapshotRow(t, &snapshot, ids[1])
	if source.Owner == nil || target.Owner == nil {
		t.Fatalf("seed owners are incomplete: source=%+v target=%+v", source.Owner, target.Owner)
	}
	if source.Owner.WorkspaceFingerprint == target.Owner.WorkspaceFingerprint {
		t.Fatal("same-workspace lifecycle rows share a fingerprint; the claim is not bound to row identity")
	}
	target.Owner.WorkspaceFingerprint = source.Owner.WorkspaceFingerprint
	writeLifecycleRecoverySnapshot(t, configDir, snapshot)

	restartedAgent := newLifecycleTestAgentAtWorkspace(t, workspaceRoot)
	t.Cleanup(func() { _ = restartedAgent.Close() })
	restarted, err := WireAgentLifecycle(
		restartedAgent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		NewAIPermissionService(t.TempDir()),
		configDir,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle restart: %v", err)
	}
	entries, err := restarted.pendingRecoveryDispositions()
	if err != nil {
		t.Fatalf("pendingRecoveryDispositions: %v", err)
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.ID] = true
	}
	if !seen[ids[0]] || seen[ids[1]] {
		t.Fatalf("pending recovery IDs = %+v, want valid source only", seen)
	}
	if _, err := restarted.applyRecoveryDisposition(agentcore.SessionWorkflow, ids[1], agentcore.RecoveryDispositionDiscard); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("copied-claim disposition error = %v, want ErrNotAllowed", err)
	}
	row, err := restarted.GetByID(ids[1])
	if err != nil || row.Recovery != agentcore.SessionRecoveryRequired || row.RecoveryDisposition != "" {
		t.Fatalf("copied-claim row changed = %+v, err=%v", row, err)
	}
}

func TestLifecycleRecoveryFingerprintBindsKindDomainAndIncarnation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*agentcore.Session)
	}{
		{
			name: "kind",
			mutate: func(row *agentcore.Session) {
				row.Kind = agentcore.SessionChat
			},
		},
		{
			name: "domain",
			mutate: func(row *agentcore.Session) {
				row.Owner.Domain = "chat-service"
			},
		},
		{
			name: "incarnation",
			mutate: func(row *agentcore.Session) {
				row.Owner.Incarnation += "-tampered"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			workspaceRoot := t.TempDir()
			id := seedDurableLifecycleRecoveryRow(t, configDir, workspaceRoot, agentcore.SessionWorkflow)
			snapshot := readLifecycleRecoverySnapshot(t, configDir)
			row := lifecycleSnapshotRow(t, &snapshot, id)
			if row.Owner == nil || row.Owner.WorkspaceFingerprint == "" {
				t.Fatalf("seed owner is incomplete: %+v", row.Owner)
			}
			test.mutate(row)
			mutatedKind := row.Kind
			writeLifecycleRecoverySnapshot(t, configDir, snapshot)

			agent := newLifecycleTestAgentAtWorkspace(t, workspaceRoot)
			t.Cleanup(func() { _ = agent.Close() })
			lifecycle, err := WireAgentLifecycle(
				agent,
				NewAIService(),
				NewAIPlanService(),
				NewAIGoalService(),
				NewAIPermissionService(t.TempDir()),
				configDir,
			)
			if err != nil {
				t.Fatalf("WireAgentLifecycle restart: %v", err)
			}
			entries, err := lifecycle.pendingRecoveryDispositions()
			if err != nil {
				t.Fatalf("pendingRecoveryDispositions: %v", err)
			}
			for _, entry := range entries {
				if entry.ID == id {
					t.Fatalf("tampered %s entered recovery listing: %+v", test.name, entry)
				}
			}
			if _, err := lifecycle.applyRecoveryDisposition(mutatedKind, id, agentcore.RecoveryDispositionDiscard); err == nil {
				t.Fatalf("tampered %s recovery disposition succeeded", test.name)
			}
			persisted, err := lifecycle.GetByID(id)
			if err != nil || persisted.Recovery != agentcore.SessionRecoveryRequired || persisted.RecoveryDisposition != "" {
				t.Fatalf("tampered %s row changed = %+v, err=%v", test.name, persisted, err)
			}
		})
	}
}
