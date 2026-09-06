//go:build e2e

package services

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestInstallAgentNativeApprovalSequenceForE2EApproveRejectAndOrder(t *testing.T) {
	writePath := filepath.Join(t.TempDir(), "approved.txt")
	runCwd := t.TempDir()
	agent := &AgentService{}
	probe, restore, err := InstallAgentNativeApprovalSequenceForE2E(agent, []AgentNativeApprovalExpectationForE2E{
		{ToolKind: AgentNativeApprovalToolWriteForE2E, Decision: true, WritePath: writePath, WriteSize: 7},
		{ToolKind: AgentNativeApprovalToolRunForE2E, Decision: false, RunCommand: "tool --check", RunCwd: runCwd, RunRiskLevel: RiskElevated},
	})
	if err != nil {
		t.Fatalf("install approval sequence: %v", err)
	}
	defer restore()

	if !agent.approveWrite(writePath, 7) {
		t.Fatal("expected matching write approval")
	}
	if agent.approveCommand("tool --check", runCwd, RiskElevated) {
		t.Fatal("expected matching run rejection")
	}

	snapshot := probe.Snapshot()
	if snapshot.Expected != 2 || snapshot.Consumed != 2 || snapshot.Remaining != 0 || !snapshot.Complete {
		t.Fatalf("unexpected snapshot summary: %+v", snapshot)
	}
	if len(snapshot.Calls) != 2 {
		t.Fatalf("expected 2 calls, got %+v", snapshot.Calls)
	}
	if first := snapshot.Calls[0]; first.Sequence != 1 || !first.Matched || !first.Consumed || !first.Decision || first.ToolKind != AgentNativeApprovalToolWriteForE2E {
		t.Fatalf("unexpected write call: %+v", first)
	}
	if second := snapshot.Calls[1]; second.Sequence != 2 || !second.Matched || !second.Consumed || second.Decision || second.ToolKind != AgentNativeApprovalToolRunForE2E {
		t.Fatalf("unexpected run call: %+v", second)
	}
}

func TestInstallAgentNativeApprovalSequenceForE2EWrongIdentityAndExhaustionFailClosed(t *testing.T) {
	writePath := filepath.Join(t.TempDir(), "target.txt")
	runCwd := t.TempDir()
	agent := &AgentService{}
	probe, restore, err := InstallAgentNativeApprovalSequenceForE2E(agent, []AgentNativeApprovalExpectationForE2E{
		{ToolKind: AgentNativeApprovalToolWriteForE2E, Decision: true, WritePath: writePath, WriteSize: 4},
		{ToolKind: AgentNativeApprovalToolRunForE2E, Decision: true, RunCommand: "exact command", RunCwd: runCwd, RunRiskLevel: RiskDangerous},
	})
	if err != nil {
		t.Fatalf("install approval sequence: %v", err)
	}
	defer restore()

	if agent.approveCommand("exact command", runCwd, RiskDangerous) {
		t.Fatal("wrong tool kind must fail closed")
	}
	if agent.approveWrite(writePath, 5) {
		t.Fatal("wrong write size must fail closed")
	}
	if !agent.approveWrite(writePath, 4) {
		t.Fatal("mismatches must not consume matching write decision")
	}
	if agent.approveCommand("exact command", filepath.Dir(runCwd), RiskDangerous) {
		t.Fatal("wrong run cwd must fail closed")
	}
	if !agent.approveCommand("exact command", runCwd, RiskDangerous) {
		t.Fatal("mismatch must not consume matching run decision")
	}
	if agent.approveCommand("exact command", runCwd, RiskDangerous) {
		t.Fatal("exhausted sequence must fail closed")
	}

	snapshot := probe.Snapshot()
	if snapshot.Consumed != 2 || !snapshot.Complete || len(snapshot.Calls) != 6 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	for _, index := range []int{0, 1, 3, 5} {
		if snapshot.Calls[index].Consumed || snapshot.Calls[index].Decision {
			t.Fatalf("call %d unexpectedly consumed a decision: %+v", index, snapshot.Calls[index])
		}
	}
	if !snapshot.Calls[2].Consumed || !snapshot.Calls[4].Consumed {
		t.Fatalf("matching decisions were not consumed: %+v", snapshot.Calls)
	}
	if snapshot.Calls[0].ExpectedToolKind != AgentNativeApprovalToolWriteForE2E || snapshot.Calls[5].ExpectedToolKind != "" {
		t.Fatalf("expected-kind evidence is incorrect: %+v", snapshot.Calls)
	}
}

func TestInstallAgentNativeApprovalSequenceForE2ERestoresCallbacksIdempotently(t *testing.T) {
	agent := &AgentService{}
	var previousWriteCalls atomic.Int32
	var previousRunCalls atomic.Int32
	agent.approveWrite = func(string, int64) bool {
		previousWriteCalls.Add(1)
		return false
	}
	agent.approveCommand = func(string, string, RiskLevel) bool {
		previousRunCalls.Add(1)
		return true
	}
	writePath := filepath.Join(t.TempDir(), "target.txt")
	probe, restore, err := InstallAgentNativeApprovalSequenceForE2E(agent, []AgentNativeApprovalExpectationForE2E{
		{ToolKind: AgentNativeApprovalToolWriteForE2E, Decision: true, WritePath: writePath, WriteSize: 1},
	})
	if err != nil {
		t.Fatalf("install approval sequence: %v", err)
	}
	if !agent.approveWrite(writePath, 1) {
		t.Fatal("installed write decision was not used")
	}
	restore()
	restore()
	if agent.approveWrite(writePath, 1) {
		t.Fatal("previous write callback result was not restored")
	}
	if !agent.approveCommand("command", t.TempDir(), RiskElevated) {
		t.Fatal("previous run callback result was not restored")
	}
	if previousWriteCalls.Load() != 1 || previousRunCalls.Load() != 1 {
		t.Fatalf("previous callbacks were not called exactly once: write=%d run=%d", previousWriteCalls.Load(), previousRunCalls.Load())
	}
	snapshot := probe.Snapshot()
	if !snapshot.Restored || len(snapshot.Calls) != 1 {
		t.Fatalf("restore changed probe evidence incorrectly: %+v", snapshot)
	}
}

func TestInstallAgentNativeApprovalSequenceForE2EConcurrentSingleConsumptionAndSnapshots(t *testing.T) {
	writePath := filepath.Join(t.TempDir(), "target.txt")
	agent := &AgentService{}
	probe, restore, err := InstallAgentNativeApprovalSequenceForE2E(agent, []AgentNativeApprovalExpectationForE2E{
		{ToolKind: AgentNativeApprovalToolWriteForE2E, Decision: true, WritePath: writePath, WriteSize: 9},
	})
	if err != nil {
		t.Fatalf("install approval sequence: %v", err)
	}
	defer restore()

	const callers = 64
	start := make(chan struct{})
	var approved atomic.Int32
	var calls sync.WaitGroup
	calls.Add(callers)
	for range callers {
		go func() {
			defer calls.Done()
			<-start
			if agent.approveWrite(writePath, 9) {
				approved.Add(1)
			}
		}()
	}
	stopSnapshots := make(chan struct{})
	var snapshots sync.WaitGroup
	snapshots.Add(1)
	go func() {
		defer snapshots.Done()
		for {
			select {
			case <-stopSnapshots:
				return
			default:
				_ = probe.Snapshot()
			}
		}
	}()
	close(start)
	calls.Wait()
	close(stopSnapshots)
	snapshots.Wait()

	if approved.Load() != 1 {
		t.Fatalf("expected exactly one approval, got %d", approved.Load())
	}
	snapshot := probe.Snapshot()
	if snapshot.Consumed != 1 || len(snapshot.Calls) != callers {
		t.Fatalf("unexpected concurrent snapshot: %+v", snapshot)
	}
	consumed := 0
	for index, call := range snapshot.Calls {
		if call.Sequence != index+1 {
			t.Fatalf("call sequence is not ordered at %d: %+v", index, call)
		}
		if call.Consumed {
			consumed++
		}
	}
	if consumed != 1 {
		t.Fatalf("expected one consumed call, got %d", consumed)
	}

	snapshot.Calls[0].WritePath = "mutated"
	if next := probe.Snapshot(); next.Calls[0].WritePath == "mutated" {
		t.Fatal("Snapshot returned mutable probe storage")
	}
}

func TestInstallAgentNativeApprovalSequenceForE2ERejectsInvalidSpecs(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "value")
	validAgent := &AgentService{}
	tests := []struct {
		name         string
		agent        *AgentService
		expectations []AgentNativeApprovalExpectationForE2E
	}{
		{name: "nil service", expectations: []AgentNativeApprovalExpectationForE2E{{ToolKind: AgentNativeApprovalToolWriteForE2E, WritePath: abs}}},
		{name: "empty sequence", agent: validAgent},
		{name: "unknown kind", agent: validAgent, expectations: []AgentNativeApprovalExpectationForE2E{{ToolKind: "unknown"}}},
		{name: "relative write path", agent: validAgent, expectations: []AgentNativeApprovalExpectationForE2E{{ToolKind: AgentNativeApprovalToolWriteForE2E, WritePath: "relative.txt"}}},
		{name: "negative write size", agent: validAgent, expectations: []AgentNativeApprovalExpectationForE2E{{ToolKind: AgentNativeApprovalToolWriteForE2E, WritePath: abs, WriteSize: -1}}},
		{name: "write with run identity", agent: validAgent, expectations: []AgentNativeApprovalExpectationForE2E{{ToolKind: AgentNativeApprovalToolWriteForE2E, WritePath: abs, RunCommand: "command"}}},
		{name: "run without command", agent: validAgent, expectations: []AgentNativeApprovalExpectationForE2E{{ToolKind: AgentNativeApprovalToolRunForE2E, RunCwd: abs, RunRiskLevel: RiskElevated}}},
		{name: "relative run cwd", agent: validAgent, expectations: []AgentNativeApprovalExpectationForE2E{{ToolKind: AgentNativeApprovalToolRunForE2E, RunCommand: "command", RunCwd: "relative", RunRiskLevel: RiskElevated}}},
		{name: "invalid run risk", agent: validAgent, expectations: []AgentNativeApprovalExpectationForE2E{{ToolKind: AgentNativeApprovalToolRunForE2E, RunCommand: "command", RunCwd: abs, RunRiskLevel: "unknown"}}},
		{name: "run with write identity", agent: validAgent, expectations: []AgentNativeApprovalExpectationForE2E{{ToolKind: AgentNativeApprovalToolRunForE2E, RunCommand: "command", RunCwd: abs, RunRiskLevel: RiskElevated, WritePath: abs}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe, restore, err := InstallAgentNativeApprovalSequenceForE2E(test.agent, test.expectations)
			if err == nil {
				if restore != nil {
					restore()
				}
				t.Fatalf("expected invalid spec rejection, got probe %+v", probe)
			}
			if probe != nil || restore != nil {
				t.Fatalf("invalid install returned live values: probe=%+v restoreNil=%v", probe, restore == nil)
			}
		})
	}
}
