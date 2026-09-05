package services

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func seedPendingExternalReceiptForRecovery(t *testing.T, configDir string) (*AgentService, *AgentLifecycle, *AIPermissionService, agentcore.UsageReceipt) {
	t.Helper()
	agent := NewAgentService()
	permission := NewAIPermissionService(configDir)
	lifecycle, err := WireAgentLifecycle(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
		configDir,
	)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	session, err := lifecycle.Begin(agentcore.SessionChat, "receipt-recovery")
	if err != nil {
		_ = agent.Close()
		t.Fatalf("Begin: %v", err)
	}
	started := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	receipt, err := lifecycle.BeginUsage(agentcore.UsageRecord{
		UnitID: "unit-recovery-1", SessionID: session.ID, UnitKind: agentcore.UsageUnitTool,
		Operation: "mcp.call", CostBasis: agentcore.CostNotApplicable,
		StartedAt: started, CompletedAt: started, Pending: true,
		ExternalReceiptID: "mcp:receipt-1", ExternalReceiptReversible: true,
		ExternalCompensation: agentcore.ExternalCompensationPending,
	})
	if err != nil {
		_ = agent.Close()
		t.Fatalf("BeginUsage: %v", err)
	}
	return agent, lifecycle, permission, receipt
}

func TestExternalReceiptRecoveryIsOpaqueStableAndManualUnknownAcrossReload(t *testing.T) {
	configDir := t.TempDir()
	agent, lifecycle, permission, receipt := seedPendingExternalReceiptForRecovery(t, configDir)
	if receipt.UnitID == "" {
		t.Fatal("empty usage receipt")
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}

	operatorAgent := NewAgentService()
	operatorPermission := NewAIPermissionService(configDir)
	operator, err := WireAgentLifecycle(
		operatorAgent,
		NewAIService(), NewAIPlanService(), NewAIGoalService(), operatorPermission, configDir,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle reload: %v", err)
	}
	t.Cleanup(func() { _ = operatorAgent.Close() })
	entries, err := operator.PendingExternalReceiptDispositions()
	if err != nil {
		t.Fatalf("PendingExternalReceiptDispositions: %v", err)
	}
	if len(entries) != 1 || entries[0].Handle == "" || entries[0].Status != "pending" {
		t.Fatalf("pending external receipts = %+v", entries)
	}
	for _, forbidden := range []string{"C:\\", "private", "api-key", "metadata"} {
		if strings.Contains(entries[0].Handle, forbidden) {
			t.Fatalf("recovery handle leaked %q: %q", forbidden, entries[0].Handle)
		}
	}
	handle := entries[0].Handle

	if _, err := operator.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
		Handle: handle, Disposition: "resume",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unsupported receipt disposition error = %v, want ErrInvalidInput", err)
	}
	if _, err := operator.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
		Handle: handle, Disposition: " manual-unknown ",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("non-canonical receipt disposition error = %v, want ErrInvalidInput", err)
	}
	result, err := operator.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
		Handle: handle, Disposition: "manual-unknown",
	})
	if err != nil {
		t.Fatalf("manual-unknown disposition: %v", err)
	}
	if result.Handle != handle || result.Status != "completed" || result.Disposition != "manual-unknown" || result.CompletedAt.IsZero() {
		t.Fatalf("manual disposition result = %+v", result)
	}
	replay, err := operator.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
		Handle: handle, Disposition: "manual-unknown",
	})
	if err != nil || replay != result {
		t.Fatalf("manual disposition replay = %+v, err=%v; want %+v", replay, err, result)
	}

	reloadedRecords := operatorPermission.usageRecordsSnapshot()
	if len(reloadedRecords) != 1 || reloadedRecords[0].UnitID != receipt.UnitID ||
		reloadedRecords[0].ExternalReceiptID != "mcp:receipt-1" || reloadedRecords[0].Pending ||
		reloadedRecords[0].ExternalCompensation != agentcore.ExternalCompensationManualUnknown {
		t.Fatalf("terminal external receipt = %+v", reloadedRecords)
	}
	if pending, err := operator.PendingExternalReceiptDispositions(); err != nil || len(pending) != 0 {
		t.Fatalf("pending receipts after disposition = %+v, err=%v", pending, err)
	}
	verifiedAgent := NewAgentService()
	verified, err := WireAgentLifecycle(
		verifiedAgent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(configDir), configDir,
	)
	if err != nil {
		_ = verifiedAgent.Close()
		t.Fatalf("WireAgentLifecycle verified reload: %v", err)
	}
	t.Cleanup(func() { _ = verifiedAgent.Close() })
	verifiedReplay, err := verified.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
		Handle: handle, Disposition: externalReceiptDispositionManualUnknown,
	})
	if err != nil || verifiedReplay.Handle != result.Handle || verifiedReplay.Status != result.Status ||
		verifiedReplay.Disposition != result.Disposition || !verifiedReplay.CompletedAt.Equal(result.CompletedAt) {
		t.Fatalf("verified reload replay = %+v, err=%v; want %+v", verifiedReplay, err, result)
	}
	_ = lifecycle
	_ = permission
}

func TestExternalReceiptRecoveryPrePublishFailureRemainsRetryable(t *testing.T) {
	configDir := t.TempDir()
	seedAgent, _, _, _ := seedPendingExternalReceiptForRecovery(t, configDir)
	if err := seedAgent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}

	agent := NewAgentService()
	permission := NewAIPermissionService(configDir)
	lifecycle, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle reload: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	entries, err := lifecycle.PendingExternalReceiptDispositions()
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending entries = %+v, err=%v", entries, err)
	}
	ledger := filepath.Join(configDir, "usage_log.jsonl")
	if err := os.Remove(ledger); err != nil {
		t.Fatalf("remove usage ledger: %v", err)
	}
	if err := os.Mkdir(ledger, 0o700); err != nil {
		t.Fatalf("block usage ledger: %v", err)
	}
	if _, err := lifecycle.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
		Handle: entries[0].Handle, Disposition: "manual-unknown",
	}); !errors.Is(err, ErrAgentRecoveryPersistence) {
		t.Fatalf("pre-publish receipt disposition error = %v, want ErrAgentRecoveryPersistence", err)
	}
	if got, err := lifecycle.PendingExternalReceiptDispositions(); err != nil || len(got) != 1 || got[0].Handle != entries[0].Handle {
		t.Fatalf("pending receipt changed after failed publication = %+v, err=%v", got, err)
	}
}

func TestExternalReceiptRecoveryFailsClosedWhenIdentityKeyIsMissingOrCorrupt(t *testing.T) {
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
					t.Fatalf("remove receipt identity key: %v", err)
				}
				return nil
			},
			check: func(t *testing.T, path string, _ []byte) {
				t.Helper()
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing receipt identity key was silently recreated: %v", err)
				}
			},
		},
		{
			name: "corrupt",
			mutate: func(t *testing.T, path string) []byte {
				t.Helper()
				corrupt := []byte("not-a-valid-external-receipt-identity-key")
				if err := os.WriteFile(path, corrupt, 0o600); err != nil {
					t.Fatalf("corrupt receipt identity key: %v", err)
				}
				return corrupt
			},
			check: func(t *testing.T, path string, want []byte) {
				t.Helper()
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read corrupt receipt identity key: %v", err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("corrupt receipt identity key was replaced: got %q want %q", got, want)
				}
				backups, err := filepath.Glob(path + ".corrupt-*")
				if err != nil {
					t.Fatalf("glob corrupt receipt identity backups: %v", err)
				}
				if len(backups) != 0 {
					t.Fatalf("corrupt receipt identity key was backed up and rotated: %v", backups)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			seedAgent, _, _, _ := seedPendingExternalReceiptForRecovery(t, configDir)
			if err := seedAgent.Close(); err != nil {
				t.Fatalf("close seed agent: %v", err)
			}
			keyPath := filepath.Join(configDir, "agent_external_receipt_identity.key")
			wantKeyState := test.mutate(t, keyPath)

			agent := NewAgentService()
			permission := NewAIPermissionService(configDir)
			lifecycle, err := WireAgentLifecycle(
				agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir,
			)
			if err != nil {
				_ = agent.Close()
				t.Fatalf("WireAgentLifecycle reload: %v", err)
			}
			t.Cleanup(func() { _ = agent.Close() })
			if entries, err := lifecycle.PendingExternalReceiptDispositions(); err == nil || entries != nil {
				t.Fatalf("receipt recovery accepted invalid identity key: entries=%+v err=%v", entries, err)
			}
			test.check(t, keyPath, wantKeyState)
		})
	}
}

func TestExternalReceiptRecoveryFailsClosedWhenLedgerCannotBeLoaded(t *testing.T) {
	configDir := t.TempDir()
	seedAgent, _, _, _ := seedPendingExternalReceiptForRecovery(t, configDir)
	if err := seedAgent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}
	ledgerPath := filepath.Join(configDir, "usage_log.jsonl")
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatalf("remove usage ledger: %v", err)
	}
	if err := os.Mkdir(ledgerPath, 0o700); err != nil {
		t.Fatalf("replace usage ledger with directory: %v", err)
	}

	agent := NewAgentService()
	permission := NewAIPermissionService(configDir)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir,
	)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle reload: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if entries, err := lifecycle.PendingExternalReceiptDispositions(); !errors.Is(err, ErrAgentRecoveryPersistenceIndeterminate) || entries != nil {
		t.Fatalf("unreadable receipt ledger recovery = %+v, err=%v", entries, err)
	}
}

func TestExternalReceiptRecoveryPostPublishPoisonsUntilFreshReload(t *testing.T) {
	configDir := t.TempDir()
	seedAgent, _, _, _ := seedPendingExternalReceiptForRecovery(t, configDir)
	if err := seedAgent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}

	agent := NewAgentService()
	permission := NewAIPermissionService(configDir)
	lifecycle, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle reload: %v", err)
	}
	entries, err := lifecycle.PendingExternalReceiptDispositions()
	if err != nil || len(entries) != 1 {
		_ = agent.Close()
		t.Fatalf("pending entries = %+v, err=%v", entries, err)
	}
	permission.usageAppendHook = func(stage string) error {
		if stage == "after-write" {
			return errors.New("injected sync uncertainty")
		}
		return nil
	}
	if _, err := lifecycle.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
		Handle: entries[0].Handle, Disposition: "manual-unknown",
	}); !errors.Is(err, ErrAgentRecoveryPersistenceIndeterminate) {
		_ = agent.Close()
		t.Fatalf("post-publish disposition error = %v, want ErrAgentRecoveryPersistenceIndeterminate", err)
	}
	if _, err := lifecycle.PendingExternalReceiptDispositions(); !errors.Is(err, ErrAgentRecoveryPersistenceIndeterminate) {
		_ = agent.Close()
		t.Fatalf("poisoned inventory error = %v, want ErrAgentRecoveryPersistenceIndeterminate", err)
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("close poisoned agent: %v", err)
	}

	freshAgent := NewAgentService()
	freshPermission := NewAIPermissionService(configDir)
	fresh, err := WireAgentLifecycle(freshAgent, NewAIService(), NewAIPlanService(), NewAIGoalService(), freshPermission, configDir)
	if err != nil {
		_ = freshAgent.Close()
		t.Fatalf("fresh WireAgentLifecycle: %v", err)
	}
	t.Cleanup(func() { _ = freshAgent.Close() })
	if pending, err := fresh.PendingExternalReceiptDispositions(); err != nil || len(pending) != 0 {
		t.Fatalf("fresh pending receipts = %+v, err=%v; want durable manual terminal", pending, err)
	}
	rows := freshPermission.usageRecordsSnapshot()
	if len(rows) != 1 || rows[0].UnitID != "unit-recovery-1" || rows[0].ExternalCompensation != agentcore.ExternalCompensationManualUnknown {
		t.Fatalf("fresh terminal row = %+v", rows)
	}
}

func TestExternalReceiptRecoveryMalformedLedgerPoisonsInventory(t *testing.T) {
	configDir := t.TempDir()
	seedAgent, _, _, _ := seedPendingExternalReceiptForRecovery(t, configDir)
	if err := seedAgent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}
	ledger := filepath.Join(configDir, "usage_log.jsonl")
	file, err := os.OpenFile(ledger, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open usage ledger: %v", err)
	}
	if _, err := file.WriteString("not-json\n"); err != nil {
		_ = file.Close()
		t.Fatalf("append malformed ledger row: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close usage ledger: %v", err)
	}
	agent := NewAgentService()
	lifecycle, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(configDir), configDir)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle malformed reload: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if _, err := lifecycle.PendingExternalReceiptDispositions(); !errors.Is(err, ErrAgentRecoveryPersistenceIndeterminate) {
		t.Fatalf("malformed ledger inventory error = %v, want ErrAgentRecoveryPersistenceIndeterminate", err)
	}
}

func TestExternalReceiptRecoveryConcurrentDispositionIsIdempotent(t *testing.T) {
	configDir := t.TempDir()
	seedAgent, _, _, _ := seedPendingExternalReceiptForRecovery(t, configDir)
	if err := seedAgent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}
	agent := NewAgentService()
	lifecycle, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(configDir), configDir)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle reload: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	entries, err := lifecycle.PendingExternalReceiptDispositions()
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending entries = %+v, err=%v", entries, err)
	}
	const callers = 12
	results := make([]AgentExternalReceiptDispositionResult, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := range results {
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] = lifecycle.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
				Handle: entries[0].Handle, Disposition: "manual-unknown",
			})
		}(index)
	}
	close(start)
	wait.Wait()
	for index := range results {
		if errs[index] != nil || results[index] != results[0] {
			t.Fatalf("concurrent disposition[%d] = %+v, err=%v; first=%+v", index, results[index], errs[index], results[0])
		}
	}
	ledger, err := os.ReadFile(filepath.Join(configDir, "usage_log.jsonl"))
	if err != nil {
		t.Fatalf("read concurrent disposition ledger: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(string(ledger)), "\n"); len(lines) != 2 {
		t.Fatalf("concurrent disposition ledger lines = %d, want pending plus one terminal", len(lines))
	}
}

func TestExternalReceiptRecoveryPersistsLogicalOwnerForOpaquePlanRuntime(t *testing.T) {
	configDir := t.TempDir()
	agent := NewAgentService()
	permission := NewAIPermissionService(configDir)
	lifecycle, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	session, err := lifecycle.Begin(agentcore.SessionPlan, "receipt-plan")
	if err != nil {
		_ = agent.Close()
		t.Fatalf("Begin plan: %v", err)
	}
	runtimeID := lifecycle.runtimeSessionID(agentcore.SessionPlan, session.ID)
	if runtimeID == session.ID {
		_ = agent.Close()
		t.Fatal("plan runtime ID was not opaque")
	}
	started := time.Date(2026, time.August, 17, 9, 0, 0, 0, time.UTC)
	if _, err := lifecycle.BeginUsage(agentcore.UsageRecord{
		UnitID: "unit-plan-recovery", SessionID: runtimeID, UnitKind: agentcore.UsageUnitTool,
		Operation: "skill.activate", CostBasis: agentcore.CostNotApplicable,
		StartedAt: started, CompletedAt: started, Pending: true,
		ExternalReceiptID: "skill:receipt-plan", ExternalReceiptReversible: true,
		ExternalCompensation: agentcore.ExternalCompensationPending,
	}); err != nil {
		_ = agent.Close()
		t.Fatalf("BeginUsage plan: %v", err)
	}
	rows := permission.usageRecordsSnapshot()
	if len(rows) != 1 || rows[0].SessionID != session.ID || rows[0].SessionID == runtimeID {
		_ = agent.Close()
		t.Fatalf("durable plan usage owner = %+v, want logical %q", rows, session.ID)
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}

	restartedAgent := NewAgentService()
	restarted, err := WireAgentLifecycle(
		restartedAgent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(configDir), configDir,
	)
	if err != nil {
		_ = restartedAgent.Close()
		t.Fatalf("WireAgentLifecycle restart: %v", err)
	}
	t.Cleanup(func() { _ = restartedAgent.Close() })
	entries, err := restarted.PendingExternalReceiptDispositions()
	if err != nil || len(entries) != 1 {
		t.Fatalf("plan recovery entries = %+v, err=%v", entries, err)
	}
}

func TestExternalReceiptRecoveryPersistsLogicalOwnerForOpaqueGoalRuntime(t *testing.T) {
	configDir := t.TempDir()
	agent := NewAgentService()
	permission := NewAIPermissionService(configDir)
	lifecycle, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	session, err := lifecycle.Begin(agentcore.SessionGoal, "receipt-goal")
	if err != nil {
		_ = agent.Close()
		t.Fatalf("Begin goal: %v", err)
	}
	runtimeID := lifecycle.runtimeSessionID(agentcore.SessionGoal, session.ID)
	if runtimeID == session.ID {
		_ = agent.Close()
		t.Fatal("goal runtime ID was not opaque")
	}
	started := time.Date(2026, time.August, 17, 9, 30, 0, 0, time.UTC)
	if _, err := lifecycle.BeginUsage(agentcore.UsageRecord{
		UnitID: "unit-goal-recovery", SessionID: runtimeID, UnitKind: agentcore.UsageUnitTool,
		Operation: "mcp.call", CostBasis: agentcore.CostNotApplicable,
		StartedAt: started, CompletedAt: started, Pending: true,
		ExternalReceiptID: "mcp:receipt-goal", ExternalReceiptReversible: true,
		ExternalCompensation: agentcore.ExternalCompensationPending,
	}); err != nil {
		_ = agent.Close()
		t.Fatalf("BeginUsage goal: %v", err)
	}
	rows := permission.usageRecordsSnapshot()
	if len(rows) != 1 || rows[0].SessionID != session.ID || rows[0].SessionID == runtimeID {
		_ = agent.Close()
		t.Fatalf("durable goal usage owner = %+v, want logical %q", rows, session.ID)
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}

	restartedAgent := NewAgentService()
	restarted, err := WireAgentLifecycle(
		restartedAgent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(configDir), configDir,
	)
	if err != nil {
		_ = restartedAgent.Close()
		t.Fatalf("WireAgentLifecycle restart: %v", err)
	}
	t.Cleanup(func() { _ = restartedAgent.Close() })
	entries, err := restarted.PendingExternalReceiptDispositions()
	if err != nil || len(entries) != 1 {
		t.Fatalf("goal recovery entries = %+v, err=%v", entries, err)
	}
}

func TestExternalReceiptRecoveryAllowsPendingReceiptAfterLifecycleCompleted(t *testing.T) {
	configDir := t.TempDir()
	seedAgent, lifecycle, _, _ := seedPendingExternalReceiptForRecovery(t, configDir)
	if err := lifecycle.Complete(agentcore.SessionChat, "receipt-recovery"); err != nil {
		_ = seedAgent.Close()
		t.Fatalf("complete lifecycle with pending receipt: %v", err)
	}
	if err := seedAgent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}

	agent := NewAgentService()
	permission := NewAIPermissionService(configDir)
	restarted, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir,
	)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle restart: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	row, err := restarted.Get(agentcore.SessionChat, "receipt-recovery")
	if err != nil || row.Status != agentcore.SessionCompleted || row.Owner == nil || row.Owner.RuntimeID == "" {
		t.Fatalf("completed lifecycle owner = %+v, err=%v", row, err)
	}
	entries, err := restarted.PendingExternalReceiptDispositions()
	if err != nil || len(entries) != 1 {
		t.Fatalf("completed lifecycle receipt entries = %+v, err=%v", entries, err)
	}
	if _, err := restarted.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
		Handle: entries[0].Handle, Disposition: externalReceiptDispositionManualUnknown,
	}); err != nil {
		t.Fatalf("dispose completed lifecycle receipt: %v", err)
	}
	records := permission.usageRecordsSnapshot()
	if len(records) != 1 || records[0].Pending ||
		records[0].ExternalCompensation != agentcore.ExternalCompensationManualUnknown {
		t.Fatalf("completed lifecycle receipt terminal = %+v", records)
	}
}

func TestExternalReceiptRecoveryRejectsDispositionAcrossWorkspaceSwitch(t *testing.T) {
	configDir := t.TempDir()
	rootA := t.TempDir()
	rootB := t.TempDir()
	seedAgent := newLifecycleTestAgentAtWorkspace(t, rootA)
	seedPermission := NewAIPermissionService(configDir)
	seedLifecycle, err := WireAgentLifecycle(
		seedAgent, NewAIService(), NewAIPlanService(), NewAIGoalService(), seedPermission, configDir,
	)
	if err != nil {
		_ = seedAgent.Close()
		t.Fatalf("WireAgentLifecycle seed: %v", err)
	}
	session, err := seedLifecycle.Begin(agentcore.SessionChat, "receipt-workspace-race")
	if err != nil {
		_ = seedAgent.Close()
		t.Fatalf("Begin seed: %v", err)
	}
	started := time.Date(2026, time.August, 17, 10, 0, 0, 0, time.UTC)
	if _, err := seedLifecycle.BeginUsage(agentcore.UsageRecord{
		UnitID: "unit-workspace-race", SessionID: session.ID, UnitKind: agentcore.UsageUnitTool,
		Operation: "mcp.call", CostBasis: agentcore.CostNotApplicable,
		StartedAt: started, CompletedAt: started, Pending: true,
		ExternalReceiptID: "mcp:receipt-workspace-race", ExternalReceiptReversible: true,
		ExternalCompensation: agentcore.ExternalCompensationPending,
	}); err != nil {
		_ = seedAgent.Close()
		t.Fatalf("BeginUsage seed: %v", err)
	}
	if err := seedAgent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}

	agent := newLifecycleTestAgentAtWorkspace(t, rootA)
	permission := NewAIPermissionService(configDir)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir,
	)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle restart: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	entries, err := lifecycle.PendingExternalReceiptDispositions()
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending workspace receipt = %+v, err=%v", entries, err)
	}
	originalGuard := lifecycle.workspaceGuard
	guardEntered := make(chan struct{})
	releaseGuard := make(chan struct{})
	lifecycle.workspaceGuard = func(expected uint64, fn func() error) error {
		close(guardEntered)
		<-releaseGuard
		return originalGuard(expected, fn)
	}
	dispositionDone := make(chan error, 1)
	go func() {
		_, dispositionErr := lifecycle.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
			Handle: entries[0].Handle, Disposition: externalReceiptDispositionManualUnknown,
		})
		dispositionDone <- dispositionErr
	}()
	<-guardEntered
	if err := agent.workspaceContext.Set(rootB); err != nil {
		close(releaseGuard)
		t.Fatalf("switch workspace context: %v", err)
	}
	close(releaseGuard)
	if err := <-dispositionDone; !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-workspace disposition error = %v, want ErrNotAllowed", err)
	}
	rows := permission.usageRecordsSnapshot()
	if len(rows) != 1 || !rows[0].Pending {
		t.Fatalf("cross-workspace disposition changed receipt = %+v", rows)
	}
}

func TestAIPermissionLegacyLedgerCreatesFirstReceiptIdentityKey(t *testing.T) {
	configDir := t.TempDir()
	legacy := `{"timestamp":"2026-08-17T10:30:00Z","operation":"chat","providerId":"legacy","model":"legacy","tokensIn":1,"tokensOut":1,"cost":0}` + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "usage_log.jsonl"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy usage ledger: %v", err)
	}
	permission := NewAIPermissionService(configDir)
	if permission.receiptIdentityErr != nil || len(permission.receiptIdentityKey) != 32 || permission.usagePersistencePoison != nil {
		t.Fatalf("legacy ledger receipt identity: key=%d identityErr=%v poison=%v", len(permission.receiptIdentityKey), permission.receiptIdentityErr, permission.usagePersistencePoison)
	}
	if _, err := os.Stat(filepath.Join(configDir, "agent_external_receipt_identity.key")); err != nil {
		t.Fatalf("stat migrated receipt identity key: %v", err)
	}
}

func TestAIPermissionMissingReceiptIdentityRejectsNewExternalReceipt(t *testing.T) {
	configDir := t.TempDir()
	permission := NewAIPermissionService(configDir)
	started := time.Date(2026, time.August, 17, 11, 0, 0, 0, time.UTC)
	pending := agentcore.UsageRecord{
		UnitID: "unit-key-history", SessionID: "chat:key-history", UnitKind: agentcore.UsageUnitTool,
		Operation: "mcp.call", CostBasis: agentcore.CostNotApplicable,
		StartedAt: started, CompletedAt: started, Pending: true,
		ExternalReceiptID: "mcp:receipt-key-history", ExternalReceiptReversible: true,
		ExternalCompensation: agentcore.ExternalCompensationPending,
	}
	receipt, err := permission.beginAgentUsage(pending)
	if err != nil {
		t.Fatalf("seed external receipt: %v", err)
	}
	terminal := pending
	terminal.Pending = false
	terminal.Success = true
	terminal.CompletedAt = started.Add(time.Second)
	terminal.ExternalCompensation = agentcore.ExternalCompensationNotNeeded
	if err := permission.completeAgentUsage(receipt, terminal); err != nil {
		t.Fatalf("complete external receipt history: %v", err)
	}
	keyPath := filepath.Join(configDir, "agent_external_receipt_identity.key")
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove receipt identity key: %v", err)
	}
	ledgerPath := filepath.Join(configDir, "usage_log.jsonl")
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read usage ledger before rejected begin: %v", err)
	}

	reloaded := NewAIPermissionService(configDir)
	newPending := pending
	newPending.UnitID = "unit-key-rejected"
	newPending.ExternalReceiptID = "mcp:receipt-key-rejected"
	if _, err := reloaded.beginAgentUsage(newPending); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("external begin without identity key = %v, want ErrNotAllowed", err)
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read usage ledger after rejected begin: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("rejected external receipt mutated usage ledger")
	}
	if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing receipt identity key was recreated: %v", err)
	}
}

func TestAIPermissionExistingReceiptIdentityUsesStrictFirstBootValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "trailing-newline",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read receipt identity key: %v", err)
				}
				data = append(data, '\n')
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatalf("append receipt identity newline: %v", err)
				}
			},
		},
		{
			name: "broad-permissions",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if runtime.GOOS == "windows" {
					t.Skip("Windows does not expose Unix permission bits")
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatalf("broaden receipt identity permissions: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			seed := NewAIPermissionService(configDir)
			if seed.receiptIdentityErr != nil {
				t.Fatalf("seed receipt identity: %v", seed.receiptIdentityErr)
			}
			keyPath := filepath.Join(configDir, "agent_external_receipt_identity.key")
			test.mutate(t, keyPath)
			before, err := os.ReadFile(keyPath)
			if err != nil {
				t.Fatalf("read mutated receipt identity: %v", err)
			}
			permission := NewAIPermissionService(configDir)
			if permission.receiptIdentityErr == nil || permission.usagePersistencePoison == nil {
				t.Fatalf("strict receipt identity accepted: identityErr=%v poison=%v", permission.receiptIdentityErr, permission.usagePersistencePoison)
			}
			after, err := os.ReadFile(keyPath)
			if err != nil {
				t.Fatalf("read rejected receipt identity: %v", err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("strict receipt identity validation rewrote the rejected key")
			}
			started := time.Date(2026, time.August, 17, 11, 30, 0, 0, time.UTC)
			if _, err := permission.beginAgentUsage(agentcore.UsageRecord{
				UnitID: "unit-strict-key", SessionID: "chat:strict-key", UnitKind: agentcore.UsageUnitTool,
				Operation: "mcp.call", CostBasis: agentcore.CostNotApplicable,
				StartedAt: started, CompletedAt: started, Pending: true,
				ExternalReceiptID: "mcp:receipt-strict-key",
			}); !errors.Is(err, ErrNotAllowed) {
				t.Fatalf("external receipt with rejected key = %v, want ErrNotAllowed", err)
			}
		})
	}
}

func TestExternalReceiptRecoveryRejectsOldIncarnationWhileRuntimeAuthorityRemains(t *testing.T) {
	configDir := t.TempDir()
	agent, lifecycle, _, _ := seedPendingExternalReceiptForRecovery(t, configDir)
	t.Cleanup(func() { _ = agent.Close() })
	lifecycle.incarnation += "-simulated-restart"
	if entries, err := lifecycle.PendingExternalReceiptDispositions(); err != nil || len(entries) != 0 {
		t.Fatalf("active runtime receipt recovery entries = %+v, err=%v", entries, err)
	}
}

func TestExternalReceiptRecoveryRejectsUnscopedHandleAfterWorkspaceOpens(t *testing.T) {
	configDir := t.TempDir()
	seedAgent, _, _, _ := seedPendingExternalReceiptForRecovery(t, configDir)
	if err := seedAgent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}
	agent := NewAgentService()
	permission := NewAIPermissionService(configDir)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir,
	)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle restart: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	entries, err := lifecycle.PendingExternalReceiptDispositions()
	if err != nil || len(entries) != 1 {
		t.Fatalf("unscoped receipt entries = %+v, err=%v", entries, err)
	}
	if err := agent.configureWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if _, err := lifecycle.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
		Handle: entries[0].Handle, Disposition: externalReceiptDispositionManualUnknown,
	}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("unscoped disposition after workspace open = %v, want ErrNotAllowed", err)
	}
	rows := permission.usageRecordsSnapshot()
	if len(rows) != 1 || !rows[0].Pending {
		t.Fatalf("rejected unscoped disposition changed receipt = %+v", rows)
	}
}

func TestExternalReceiptRecoveryUnscopedPublicationRacesWorkspaceOpen(t *testing.T) {
	configDir := t.TempDir()
	seedAgent, _, _, _ := seedPendingExternalReceiptForRecovery(t, configDir)
	if err := seedAgent.Close(); err != nil {
		t.Fatalf("close seed agent: %v", err)
	}
	agent := NewAgentService()
	permission := NewAIPermissionService(configDir)
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission, configDir,
	)
	if err != nil {
		_ = agent.Close()
		t.Fatalf("WireAgentLifecycle restart: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	entries, err := lifecycle.PendingExternalReceiptDispositions()
	if err != nil || len(entries) != 1 {
		t.Fatalf("unscoped receipt entries = %+v, err=%v", entries, err)
	}

	originalGuard := lifecycle.workspaceGuard
	guardEntered := make(chan struct{})
	releaseGuard := make(chan struct{})
	lifecycle.workspaceGuard = func(expected uint64, fn func() error) error {
		close(guardEntered)
		<-releaseGuard
		return originalGuard(expected, fn)
	}
	dispositionDone := make(chan error, 1)
	go func() {
		_, dispositionErr := lifecycle.DispatchExternalReceiptDisposition(AgentExternalReceiptDispositionRequest{
			Handle: entries[0].Handle, Disposition: externalReceiptDispositionManualUnknown,
		})
		dispositionDone <- dispositionErr
	}()
	<-guardEntered
	root := canonicalTestPath(t, t.TempDir())
	workspaceDone := make(chan error, 1)
	go func() { workspaceDone <- agent.configureWorkspaceRoot(root) }()
	select {
	case workspaceErr := <-workspaceDone:
		close(releaseGuard)
		t.Fatalf("workspace open crossed an unresolved receipt disposition: %v", workspaceErr)
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseGuard)
	if err := <-dispositionDone; err != nil {
		t.Fatalf("serialized unscoped disposition: %v", err)
	}
	if err := <-workspaceDone; err != nil {
		t.Fatalf("complete workspace open: %v", err)
	}
	if got := filepath.Clean(agent.currentWorkspaceRoot()); got != filepath.Clean(root) {
		t.Fatalf("workspace root after serialized disposition = %q, want %q", got, filepath.Clean(root))
	}
	rows := permission.usageRecordsSnapshot()
	if len(rows) != 1 || rows[0].Pending || rows[0].ExternalCompensation != agentcore.ExternalCompensationManualUnknown {
		t.Fatalf("serialized unscoped disposition receipt = %+v", rows)
	}
}
