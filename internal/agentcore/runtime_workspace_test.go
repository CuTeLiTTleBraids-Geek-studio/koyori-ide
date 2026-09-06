package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type workspaceSwitchingHandler struct {
	generation *uint64
}

func (h *workspaceSwitchingHandler) MutationMode() MutationMode {
	return MutationNone
}

func (h *workspaceSwitchingHandler) Prepare(context.Context, Invocation) (PreparedExecution, error) {
	(*h.generation)++
	return PreparedExecution{
		Summary: "prepared before workspace switch completed",
		Opaque:  json.RawMessage(`{"resolvedPath":"old-workspace/file.txt"}`),
	}, nil
}

func (*workspaceSwitchingHandler) Execute(context.Context, Invocation, PreparedExecution) (ExecutionOutput, error) {
	return ExecutionOutput{Observation: "must not execute"}, nil
}

func TestRuntimeRejectsWorkspaceChangeDuringPrepare(t *testing.T) {
	definition := testTool("read", SourceBuiltin, `{
		"type":"object",
		"properties":{"path":{"type":"string","minLength":1}},
		"required":["path"],
		"additionalProperties":false
	}`)
	registry, err := NewRegistry([]ToolDef{definition})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	generation := uint64(1)
	budget := &testBudget{epoch: 1, remaining: 1}
	approver := &testApprover{approved: true}
	runtime, err := NewRuntime(registry, RuntimeOptions{
		Budget:              budget,
		Approver:            approver,
		Audit:               &recordingAudit{},
		WorkspaceGeneration: func() uint64 { return generation },
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if err := runtime.RegisterHandler("read", &workspaceSwitchingHandler{generation: &generation}); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}

	catalog := registry.Snapshot()
	_, err = runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID:       "chat-1",
		CatalogRevision: catalog.Revision,
		ToolID:          "read",
		Arguments:       json.RawMessage(`{"path":"file.txt"}`),
	})
	if !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("RequestCapability error = %v, want ErrInvalidCapability", err)
	}
	if len(approver.calls) != 0 || budget.reserved != 0 {
		t.Fatalf("workspace switch reached approval/budget: approvals=%d budget=%d", len(approver.calls), budget.reserved)
	}
}
