package agentcore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

type headlessBudget struct{}

func (headlessBudget) Reserve() (uint64, error)        { return 1, nil }
func (headlessBudget) ReleaseReservation(uint64) error { return nil }
func (headlessBudget) CurrentEpoch() uint64            { return 1 }

type headlessApprover struct{}

func (headlessApprover) Approve(context.Context, agentcore.ApprovalRequest) (bool, error) {
	return true, nil
}

type headlessAudit struct{}

func (headlessAudit) RecordAudit(agentcore.AuditRecord) error { return nil }

type headlessHandler struct{}

type headlessMeter struct {
	begun     int
	completed int
}

func (m *headlessMeter) RecordUsage(agentcore.UsageRecord) error { return nil }

func (m *headlessMeter) BeginUsage(record agentcore.UsageRecord) (agentcore.UsageReceipt, error) {
	m.begun++
	return agentcore.UsageReceipt{UnitID: record.UnitID}, nil
}

func (m *headlessMeter) CompleteUsage(_ agentcore.UsageReceipt, _ agentcore.UsageRecord) error {
	m.completed++
	return nil
}

func (headlessHandler) MutationMode() agentcore.MutationMode { return agentcore.MutationNone }
func (headlessHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	return agentcore.PreparedExecution{Summary: "headless probe", Opaque: json.RawMessage(`{"ready":true}`)}, nil
}
func (headlessHandler) Execute(_ context.Context, _ agentcore.Invocation, _ agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	return agentcore.ExecutionOutput{Observation: "headless-ok"}, nil
}

// This external-package test is the reusable CLI/CI boundary: it consumes
// agentcore through exported APIs only and imports no desktop service or Wails
// package. Host applications provide policy, budget, and handlers while the
// core retains catalog/capability/session enforcement.
func TestHeadlessConsumerUsesExportedRuntime(t *testing.T) {
	registry, err := agentcore.NewRegistry([]agentcore.ToolDef{{
		ID: "probe", Description: "Headless probe",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
		Source:      agentcore.SourceBuiltin, Risk: agentcore.RiskReadOnly,
		Approval: agentcore.ApprovalBackendPolicy, Mutation: agentcore.MutationNone,
		ExecuteKey: "probe.execute",
	}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	meter := &headlessMeter{}
	runtime, err := agentcore.NewRuntime(registry, agentcore.RuntimeOptions{
		Budget: headlessBudget{}, Approver: headlessApprover{}, Audit: headlessAudit{},
		Meter: meter, WorkspaceGeneration: func() uint64 { return 1 }, EnforceSessions: true,
		RequireMeter: true, RequireUsageTransaction: true,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if err := runtime.RegisterHandler("probe.execute", headlessHandler{}); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}
	if err := runtime.RegisterSession("ci:probe"); err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	catalog := registry.Snapshot()
	arguments := json.RawMessage(`{"value":"ok"}`)
	grant, err := runtime.RequestCapability(context.Background(), agentcore.CapabilityRequest{
		SessionID: "ci:probe", CatalogRevision: catalog.Revision,
		ToolID: "probe", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	result, err := runtime.Execute(context.Background(), agentcore.CapabilityExecution{
		Token: grant.Token, SessionID: "ci:probe", CatalogRevision: catalog.Revision,
		ToolID: "probe", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Observation != "headless-ok" {
		t.Fatalf("observation = %q", result.Observation)
	}
	if meter.begun != 1 || meter.completed != 1 {
		t.Fatalf("headless meter begin/complete = %d/%d, want 1/1", meter.begun, meter.completed)
	}
}
