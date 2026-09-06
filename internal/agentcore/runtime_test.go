package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type testBudget struct {
	mu           sync.Mutex
	epoch        uint64
	remaining    int
	reserved     int
	released     int
	afterReserve func()
}

func (b *testBudget) Reserve() (uint64, error) {
	b.mu.Lock()
	if b.remaining <= 0 {
		b.mu.Unlock()
		return 0, ErrBudgetExhausted
	}
	b.remaining--
	b.reserved++
	epoch := b.epoch
	hook := b.afterReserve
	b.afterReserve = nil
	b.mu.Unlock()
	if hook != nil {
		hook()
	}
	return epoch, nil
}

func (b *testBudget) ReleaseReservation(epoch uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if epoch != b.epoch {
		return nil
	}
	b.remaining++
	b.released++
	return nil
}

func (b *testBudget) CurrentEpoch() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.epoch
}

func (b *testBudget) reset(limit int) {
	b.mu.Lock()
	b.epoch++
	b.remaining = limit
	b.mu.Unlock()
}

func (b *testBudget) setAfterReserve(hook func()) {
	b.mu.Lock()
	b.afterReserve = hook
	b.mu.Unlock()
}

type testApprover struct {
	mu       sync.Mutex
	approved bool
	calls    []ApprovalRequest
}

type blockingApprover struct {
	entered chan struct{}
	release chan struct{}
}

func (a *blockingApprover) Approve(ctx context.Context, _ ApprovalRequest) (bool, error) {
	close(a.entered)
	select {
	case <-a.release:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (a *testApprover) Approve(_ context.Context, request ApprovalRequest) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, request)
	return a.approved, nil
}

type testHandler struct {
	mu           sync.Mutex
	mutation     MutationMode
	prepared     int
	executed     int
	lastPrepared json.RawMessage
	lastArgs     json.RawMessage
	lastSession  string
}

// transactionTestHandler keeps the generic and transaction entry points
// observable so the runtime test can prove which boundary was invoked.
type transactionTestHandler struct {
	testHandler
	transactionExecuted int
}

func (h *transactionTestHandler) ExecuteWorkspaceTransaction(_ context.Context, invocation Invocation, prepared PreparedExecution) (ExecutionOutput, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.transactionExecuted++
	h.lastPrepared = append(json.RawMessage(nil), prepared.Opaque...)
	h.lastArgs = append(json.RawMessage(nil), invocation.Arguments...)
	h.lastSession = invocation.SessionID
	return ExecutionOutput{Observation: "transaction-ok"}, nil
}

type externalTestHandler struct {
	testHandler
	receipt              ExternalMutationReceipt
	beginErr             error
	beginCalls           int
	executeErr           error
	compensateErr        error
	externalExecuted     int
	compensated          int
	compensationCalls    int
	compensationCtxErr   error
	compensationDeadline bool
	compensationValue    string
	compensatedReceipts  []ExternalMutationReceipt
}

type compensationContextKey struct{}

func (h *externalTestHandler) BeginExternalMutation(_ context.Context, _ Invocation, _ PreparedExecution) (ExternalMutationReceipt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beginCalls++
	return h.receipt, h.beginErr
}

func (h *externalTestHandler) ExecuteExternalTransaction(_ context.Context, invocation Invocation, prepared PreparedExecution) (ExecutionOutput, ExternalMutationReceipt, error) {
	output, err := h.ExecuteExternalTransactionWithReceipt(context.Background(), invocation, prepared, h.receipt)
	return output, h.receipt, err
}

func (h *externalTestHandler) ExecuteExternalTransactionWithReceipt(_ context.Context, invocation Invocation, prepared PreparedExecution, _ ExternalMutationReceipt) (ExecutionOutput, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.externalExecuted++
	h.lastPrepared = append(json.RawMessage(nil), prepared.Opaque...)
	h.lastArgs = append(json.RawMessage(nil), invocation.Arguments...)
	h.lastSession = invocation.SessionID
	return ExecutionOutput{Observation: "external-ok"}, h.executeErr
}

func (h *externalTestHandler) CompensateExternalTransaction(ctx context.Context, _ Invocation, _ PreparedExecution, receipt ExternalMutationReceipt) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.compensated++
	h.compensationCalls++
	h.compensationCtxErr = ctx.Err()
	_, h.compensationDeadline = ctx.Deadline()
	h.compensationValue, _ = ctx.Value(compensationContextKey{}).(string)
	h.compensatedReceipts = append(h.compensatedReceipts, receipt)
	return h.compensateErr
}

func (h *testHandler) MutationMode() MutationMode { return h.mutation }

func (h *testHandler) Prepare(_ context.Context, invocation Invocation) (PreparedExecution, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prepared++
	return PreparedExecution{
		Summary: "prepared " + invocation.Tool.ID,
		Opaque:  json.RawMessage(`{"baseline":"abc123"}`),
	}, nil
}

func (h *testHandler) Execute(_ context.Context, invocation Invocation, prepared PreparedExecution) (ExecutionOutput, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.executed++
	h.lastPrepared = append(json.RawMessage(nil), prepared.Opaque...)
	h.lastArgs = append(json.RawMessage(nil), invocation.Arguments...)
	h.lastSession = invocation.SessionID
	return ExecutionOutput{Observation: "ok"}, nil
}

type recordingAudit struct {
	mu      sync.Mutex
	records []AuditRecord
}

func (a *recordingAudit) RecordAudit(record AuditRecord) error {
	a.mu.Lock()
	a.records = append(a.records, record)
	a.mu.Unlock()
	return nil
}

type failingAudit struct {
	mu        sync.Mutex
	failStage AuditStage
	err       error
	records   []AuditRecord
}

func (a *failingAudit) RecordAudit(record AuditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, record)
	if record.Stage == a.failStage {
		return a.err
	}
	return nil
}

type recordingMeter struct {
	mu      sync.Mutex
	records []UsageRecord
}

type transactionalMeter struct {
	mu             sync.Mutex
	begun          []UsageRecord
	completed      []UsageRecord
	beginErr       error
	completeErr    error
	completeErrors []error
	events         []string
}

type testInvocationPolicy struct {
	mu      sync.Mutex
	allowed bool
	calls   []Invocation
}

func (p *testInvocationPolicy) Authorize(_ context.Context, invocation Invocation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, invocation)
	if !p.allowed {
		return errors.New("session policy denied invocation")
	}
	return nil
}

func (p *testInvocationPolicy) setAllowed(allowed bool) {
	p.mu.Lock()
	p.allowed = allowed
	p.mu.Unlock()
}

func (m *recordingMeter) RecordUsage(record UsageRecord) error {
	m.mu.Lock()
	m.records = append(m.records, record)
	m.mu.Unlock()
	return nil
}

func (m *transactionalMeter) RecordUsage(record UsageRecord) error {
	m.mu.Lock()
	m.completed = append(m.completed, record)
	m.events = append(m.events, "record")
	m.mu.Unlock()
	return nil
}

func (m *transactionalMeter) BeginUsage(record UsageRecord) (UsageReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, "begin")
	if m.beginErr != nil {
		return UsageReceipt{}, m.beginErr
	}
	m.begun = append(m.begun, record)
	return UsageReceipt{UnitID: record.UnitID}, nil
}

func (m *transactionalMeter) CompleteUsage(receipt UsageReceipt, record UsageRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, "complete")
	if receipt.UnitID != record.UnitID {
		return errors.New("receipt does not match usage unit")
	}
	if len(m.completeErrors) > 0 {
		err := m.completeErrors[0]
		m.completeErrors = m.completeErrors[1:]
		if err != nil {
			return err
		}
	}
	if m.completeErr != nil {
		return m.completeErr
	}
	m.completed = append(m.completed, record)
	return nil
}

type orderedHandler struct {
	testHandler
	meter *transactionalMeter
}

func (h *orderedHandler) Execute(ctx context.Context, invocation Invocation, prepared PreparedExecution) (ExecutionOutput, error) {
	h.meter.mu.Lock()
	h.meter.events = append(h.meter.events, "execute")
	h.meter.mu.Unlock()
	return h.testHandler.Execute(ctx, invocation, prepared)
}

func runtimeFixture(t *testing.T, mutation MutationMode) (*Registry, *Runtime, *testBudget, *testApprover, *testHandler, *recordingAudit, *recordingMeter, *uint64) {
	t.Helper()
	def := testTool("read", SourceBuiltin, `{
		"type":"object",
		"properties":{"path":{"type":"string","minLength":1}},
		"required":["path"],
		"additionalProperties":false
	}`)
	def.Mutation = mutation
	if mutation != MutationNone {
		def.Risk = RiskElevated
		def.Approval = ApprovalManual
	}
	registry, err := NewRegistry([]ToolDef{def})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	budget := &testBudget{epoch: 7, remaining: 10}
	approver := &testApprover{approved: true}
	audit := &recordingAudit{}
	meter := &recordingMeter{}
	generation := uint64(11)
	runtime, err := NewRuntime(registry, RuntimeOptions{
		Budget:              budget,
		Approver:            approver,
		Audit:               audit,
		Meter:               meter,
		WorkspaceGeneration: func() uint64 { return generation },
		CapabilityTTL:       time.Minute,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	handler := &testHandler{mutation: mutation}
	if err := runtime.RegisterHandler("read", handler); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}
	return registry, runtime, budget, approver, handler, audit, meter, &generation
}

func transactionalRuntimeFixture(t *testing.T) (*Registry, *Runtime, *testBudget, *testApprover, *orderedHandler, *recordingAudit, *transactionalMeter) {
	t.Helper()
	registry, err := NewRegistry([]ToolDef{testTool("read", SourceBuiltin, `{
		"type":"object",
		"properties":{"path":{"type":"string","minLength":1}},
		"required":["path"],
		"additionalProperties":false
	}`)})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	budget := &testBudget{epoch: 3, remaining: 2}
	approver := &testApprover{approved: true}
	audit := &recordingAudit{}
	meter := &transactionalMeter{}
	runtime, err := NewRuntime(registry, RuntimeOptions{
		Budget: budget, Approver: approver, Audit: audit, Meter: meter,
		WorkspaceGeneration: func() uint64 { return 1 },
		RequireMeter:        true, RequireUsageTransaction: true,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	handler := &orderedHandler{
		testHandler: testHandler{mutation: MutationNone},
		meter:       meter,
	}
	if err := runtime.RegisterHandler("read", handler); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}
	return registry, runtime, budget, approver, handler, audit, meter
}

func externalRuntimeFixture(t *testing.T) (*Registry, *Runtime, *externalTestHandler, *recordingAudit, *transactionalMeter) {
	t.Helper()
	def := testTool("external", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`)
	def.Risk = RiskElevated
	def.Approval = ApprovalManual
	def.Mutation = MutationExternal
	registry, err := NewRegistry([]ToolDef{def})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	audit := &recordingAudit{}
	meter := &transactionalMeter{}
	runtime, err := NewRuntime(registry, RuntimeOptions{
		Budget: &testBudget{epoch: 1, remaining: 4}, Approver: &testApprover{approved: true},
		Audit: audit, Meter: meter, WorkspaceGeneration: func() uint64 { return 1 },
		RequireMeter: true, RequireUsageTransaction: true,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	handler := &externalTestHandler{
		testHandler: testHandler{mutation: MutationExternal},
		receipt: ExternalMutationReceipt{
			ID: "external-receipt-1", Reversible: true,
			Metadata: map[string]string{"operation": "fixture"},
		},
	}
	if err := runtime.RegisterHandler("external", handler); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}
	return registry, runtime, handler, audit, meter
}

func executeExternal(t *testing.T, registry *Registry, runtime *Runtime, sessionID string) (ExecutionResult, error) {
	t.Helper()
	catalog := registry.Snapshot()
	arguments := json.RawMessage(`{}`)
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	return runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: arguments,
	})
}

func TestRuntimeUsesOneApprovalBudgetCapabilityExecutionAuditAndMeterPipeline(t *testing.T) {
	registry, runtime, budget, approver, handler, audit, meter, _ := runtimeFixture(t, MutationNone)
	catalog := registry.Snapshot()

	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID:       "chat-1",
		CatalogRevision: catalog.Revision,
		ToolID:          "read",
		Arguments:       json.RawMessage(`{"path":"README.md"}`),
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	if grant.Token == "" || grant.BudgetEpoch != 7 || grant.WorkspaceGeneration != 11 {
		t.Fatalf("unexpected grant: %+v", grant)
	}
	if len(approver.calls) != 1 || budget.reserved != 1 || handler.prepared != 1 {
		t.Fatalf("prepare pipeline approver=%d budget=%d prepared=%d", len(approver.calls), budget.reserved, handler.prepared)
	}

	result, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token:           grant.Token,
		SessionID:       "chat-1",
		CatalogRevision: catalog.Revision,
		ToolID:          "read",
		Arguments:       json.RawMessage(`{ "path": "README.md" }`),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Observation != "ok" || handler.executed != 1 {
		t.Fatalf("execution result=%+v executed=%d", result, handler.executed)
	}
	if string(handler.lastPrepared) != `{"baseline":"abc123"}` {
		t.Fatalf("prepared state = %s", handler.lastPrepared)
	}
	if handler.lastSession != "chat-1" {
		t.Fatalf("handler session = %q", handler.lastSession)
	}
	if len(audit.records) != 3 || audit.records[0].Stage != AuditCapabilityIssued || audit.records[1].Stage != AuditExecutionStarted || audit.records[2].Stage != AuditExecutionCompleted {
		t.Fatalf("audit records = %+v", audit.records)
	}
	if len(meter.records) != 1 || meter.records[0].UnitKind != UsageUnitTool || meter.records[0].CostBasis != CostNotApplicable || meter.records[0].Estimated {
		t.Fatalf("usage records = %+v", meter.records)
	}

	if _, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat-1", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("replay error = %v, want ErrInvalidCapability", err)
	}
	if handler.executed != 1 {
		t.Fatalf("replay executed handler %d times", handler.executed)
	}
}

func TestRuntimeRequiredMeterFailsBeforePrepareApprovalOrBudget(t *testing.T) {
	registry, runtime, budget, approver, handler, _, _, _ := runtimeFixture(t, MutationNone)
	runtime.SetUsageSink(nil)
	runtime.SetUsageRequirements(true, true)
	_, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:meter-missing", CatalogRevision: registry.Snapshot().Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	})
	if !errors.Is(err, ErrMeterUnavailable) {
		t.Fatalf("RequestCapability error = %v, want ErrMeterUnavailable", err)
	}
	if handler.prepared != 0 || len(approver.calls) != 0 || budget.reserved != 0 {
		t.Fatalf("missing meter reached prepare/approval/budget: prepared=%d approvals=%d budget=%d", handler.prepared, len(approver.calls), budget.reserved)
	}
}

func TestRuntimeRequiredTransactionalMeterFailsBeforePrepareApprovalOrBudget(t *testing.T) {
	registry, runtime, budget, approver, handler, _, meter, _ := runtimeFixture(t, MutationNone)
	runtime.SetUsageSink(meter)
	runtime.SetUsageRequirements(true, true)
	_, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:meter-contract", CatalogRevision: registry.Snapshot().Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	})
	if !errors.Is(err, ErrMeterContract) {
		t.Fatalf("RequestCapability error = %v, want ErrMeterContract", err)
	}
	if handler.prepared != 0 || len(approver.calls) != 0 || budget.reserved != 0 {
		t.Fatalf("invalid meter reached prepare/approval/budget: prepared=%d approvals=%d budget=%d", handler.prepared, len(approver.calls), budget.reserved)
	}
}

func TestRuntimeTransactionalMeterBracketsHandlerExecution(t *testing.T) {
	registry, runtime, _, _, handler, audit, meter := transactionalRuntimeFixture(t)
	catalog := registry.Snapshot()
	arguments := json.RawMessage(`{"path":"README.md"}`)
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:meter-order", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	result, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat:meter-order", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Join(meter.events, ","); got != "begin,execute,complete" {
		t.Fatalf("meter/handler order = %q, want begin,execute,complete", got)
	}
	if len(meter.begun) != 1 || !meter.begun[0].Pending || meter.begun[0].Success {
		t.Fatalf("pending receipt = %+v", meter.begun)
	}
	if len(meter.completed) != 1 || meter.completed[0].Pending || !meter.completed[0].Success || meter.completed[0].UnitID != meter.begun[0].UnitID {
		t.Fatalf("completed usage = %+v, begun = %+v", meter.completed, meter.begun)
	}
	if !result.Usage.Success || result.Usage.UnitID != meter.begun[0].UnitID || handler.executed != 1 {
		t.Fatalf("result=%+v executed=%d", result, handler.executed)
	}
	if len(audit.records) != 3 || audit.records[1].Stage != AuditExecutionStarted || audit.records[2].Stage != AuditExecutionCompleted || !audit.records[2].Success {
		t.Fatalf("audit records = %+v", audit.records)
	}
}

func TestRuntimeTransactionalMeterBeginFailurePreventsSideEffect(t *testing.T) {
	registry, runtime, _, _, handler, audit, meter := transactionalRuntimeFixture(t)
	meter.beginErr = errors.New("ledger is read-only")
	catalog := registry.Snapshot()
	arguments := json.RawMessage(`{"path":"README.md"}`)
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:meter-begin-fail", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	result, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat:meter-begin-fail", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if err == nil || !strings.Contains(err.Error(), "begin execution usage") {
		t.Fatalf("Execute error = %v, want begin usage failure", err)
	}
	if handler.executed != 0 || result.Observation != "" {
		t.Fatalf("begin failure executed side effect: result=%+v executed=%d", result, handler.executed)
	}
	if got := strings.Join(meter.events, ","); got != "begin" {
		t.Fatalf("events = %q, want begin", got)
	}
	if len(audit.records) != 2 || audit.records[0].Stage != AuditCapabilityIssued || audit.records[1].Stage != AuditExecutionFailed {
		t.Fatalf("unexpected audit records = %+v", audit.records)
	}
	if _, replayErr := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat:meter-begin-fail", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	}); !errors.Is(replayErr, ErrInvalidCapability) {
		t.Fatalf("meter-failed capability replay = %v, want ErrInvalidCapability", replayErr)
	}
}

func TestRuntimeTransactionalMeterCompletionFailureLeavesPendingReceipt(t *testing.T) {
	registry, runtime, _, _, handler, audit, meter := transactionalRuntimeFixture(t)
	meter.completeErr = errors.New("disk filled after receipt")
	catalog := registry.Snapshot()
	arguments := json.RawMessage(`{"path":"README.md"}`)
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:meter-complete-fail", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	result, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat:meter-complete-fail", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if err == nil || !strings.Contains(err.Error(), "complete execution usage") {
		t.Fatalf("Execute error = %v, want completion failure", err)
	}
	if handler.executed != 1 || result.Observation != "ok" || result.Usage.Success {
		t.Fatalf("completion failure result=%+v executed=%d", result, handler.executed)
	}
	if len(meter.begun) != 1 || !meter.begun[0].Pending || len(meter.completed) != 0 {
		t.Fatalf("receipt state begun=%+v completed=%+v", meter.begun, meter.completed)
	}
	if len(audit.records) != 3 || audit.records[1].Stage != AuditExecutionStarted || audit.records[2].Stage != AuditExecutionFailed || audit.records[2].Success {
		t.Fatalf("audit records = %+v", audit.records)
	}
}

func TestRuntimeExternalMutationBeginReceiptFailureHasNoSideEffect(t *testing.T) {
	registry, runtime, handler, audit, meter := externalRuntimeFixture(t)
	meter.beginErr = errors.New("external ledger is unavailable")
	result, err := executeExternal(t, registry, runtime, "chat:external-begin-fail")
	if err == nil || !strings.Contains(err.Error(), "begin execution usage") {
		t.Fatalf("Execute error = %v, want begin usage failure", err)
	}
	if handler.externalExecuted != 0 || handler.executed != 0 || handler.compensationCalls != 0 {
		t.Fatalf("begin failure reached external handler: external=%d generic=%d compensation=%d", handler.externalExecuted, handler.executed, handler.compensationCalls)
	}
	if result.Observation != "" || result.Usage.Success {
		t.Fatalf("begin failure result = %+v", result)
	}
	if got := strings.Join(meter.events, ","); got != "begin" {
		t.Fatalf("meter events = %q, want begin", got)
	}
	if len(meter.begun) != 0 {
		t.Fatalf("failed durable begin unexpectedly retained rows: %+v", meter.begun)
	}
	if result.Usage.ExternalReceiptID != handler.receipt.ID || result.Usage.ExternalCompensation != ExternalCompensationPending {
		t.Fatalf("result lost preallocated external receipt: %+v", result.Usage)
	}
	if len(audit.records) != 2 || audit.records[1].Stage != AuditExecutionFailed || audit.records[1].Success {
		t.Fatalf("audit records = %+v", audit.records)
	}
}

func TestRuntimeExternalMutationRequiresExecutionReceipt(t *testing.T) {
	registry, runtime, handler, audit, meter := externalRuntimeFixture(t)
	handler.receipt = ExternalMutationReceipt{}
	result, err := executeExternal(t, registry, runtime, "chat:external-missing-receipt")
	if !errors.Is(err, ErrExternalMutationContract) {
		t.Fatalf("Execute error = %v, want ErrExternalMutationContract", err)
	}
	if handler.beginCalls != 1 || handler.externalExecuted != 0 || handler.executed != 0 || handler.compensationCalls != 0 {
		t.Fatalf("handler calls begin=%d external=%d generic=%d compensation=%d", handler.beginCalls, handler.externalExecuted, handler.executed, handler.compensationCalls)
	}
	if result.Observation != "" || result.Usage.Success || !strings.Contains(result.Usage.Error, ErrExternalMutationContract.Error()) {
		t.Fatalf("missing receipt result = %+v", result)
	}
	if len(meter.begun) != 0 || len(meter.completed) != 0 {
		t.Fatalf("invalid external receipt reached durable usage ledger: begun=%+v completed=%+v", meter.begun, meter.completed)
	}
	if len(audit.records) != 2 || audit.records[1].Stage != AuditExecutionFailed || audit.records[1].Success || !strings.Contains(audit.records[1].Error, ErrExternalMutationContract.Error()) {
		t.Fatalf("audit records = %+v", audit.records)
	}
}

func TestRuntimeExternalMutationCompensatesHandlerFailureWithReceipt(t *testing.T) {
	registry, runtime, handler, audit, meter := externalRuntimeFixture(t)
	executeFailure := errors.New("remote operation failed after mutation")
	handler.executeErr = executeFailure
	result, err := executeExternal(t, registry, runtime, "chat:external-handler-fail")
	if !errors.Is(err, executeFailure) {
		t.Fatalf("Execute error = %v, want handler failure", err)
	}
	if handler.externalExecuted != 1 || handler.compensationCalls != 1 || len(handler.compensatedReceipts) != 1 || handler.compensatedReceipts[0].ID != handler.receipt.ID {
		t.Fatalf("handler external=%d compensation=%d receipts=%+v", handler.externalExecuted, handler.compensationCalls, handler.compensatedReceipts)
	}
	if result.Usage.Success || !strings.Contains(result.Usage.Error, executeFailure.Error()) {
		t.Fatalf("handler failure usage = %+v", result.Usage)
	}
	if len(meter.begun) != 1 || len(meter.completed) != 1 || meter.completed[0].Success {
		t.Fatalf("usage begun=%+v completed=%+v", meter.begun, meter.completed)
	}
	if meter.begun[0].ExternalReceiptID != handler.receipt.ID || meter.begun[0].ExternalCompensation != ExternalCompensationPending || meter.completed[0].ExternalCompensation != ExternalCompensationSucceeded {
		t.Fatalf("external receipt lifecycle begun=%+v completed=%+v", meter.begun, meter.completed)
	}
	if len(audit.records) != 3 || audit.records[1].Stage != AuditExecutionStarted || audit.records[2].Stage != AuditExecutionFailed || audit.records[2].Success {
		t.Fatalf("audit records = %+v", audit.records)
	}
}

func TestRuntimeExternalMutationCompensatesUsageCompletionFailure(t *testing.T) {
	registry, runtime, handler, audit, meter := externalRuntimeFixture(t)
	meter.completeErr = errors.New("external usage completion failed")
	result, err := executeExternal(t, registry, runtime, "chat:external-complete-fail")
	if err == nil || !strings.Contains(err.Error(), "complete execution usage") {
		t.Fatalf("Execute error = %v, want completion failure", err)
	}
	if handler.externalExecuted != 1 || handler.compensationCalls != 1 || len(handler.compensatedReceipts) != 1 || handler.compensatedReceipts[0].ID != handler.receipt.ID {
		t.Fatalf("handler external=%d compensation=%d receipts=%+v", handler.externalExecuted, handler.compensationCalls, handler.compensatedReceipts)
	}
	if result.Usage.Success || !strings.Contains(result.Usage.Error, "complete execution usage") {
		t.Fatalf("completion failure usage = %+v", result.Usage)
	}
	if len(meter.begun) != 1 || !meter.begun[0].Pending || len(meter.completed) != 0 {
		t.Fatalf("pending receipt not preserved: begun=%+v completed=%+v", meter.begun, meter.completed)
	}
	if meter.begun[0].ExternalReceiptID != handler.receipt.ID || meter.begun[0].ExternalCompensation != ExternalCompensationPending || result.Usage.ExternalCompensation != ExternalCompensationSucceeded {
		t.Fatalf("external compensation state result=%+v begun=%+v", result.Usage, meter.begun)
	}
	if len(audit.records) != 3 || audit.records[1].Stage != AuditExecutionStarted || audit.records[2].Stage != AuditExecutionFailed || audit.records[2].Success || !strings.Contains(audit.records[2].Error, "complete execution usage") {
		t.Fatalf("audit records = %+v", audit.records)
	}
}

func TestRuntimeExternalMutationRetriesCompensatedTerminalReceipt(t *testing.T) {
	registry, runtime, handler, audit, meter := externalRuntimeFixture(t)
	firstFailure := errors.New("external usage completion failed once")
	meter.completeErrors = []error{firstFailure, nil}

	result, err := executeExternal(t, registry, runtime, "chat:external-complete-retry")
	if err == nil || !strings.Contains(err.Error(), firstFailure.Error()) {
		t.Fatalf("Execute error = %v, want original completion failure", err)
	}
	if handler.externalExecuted != 1 || handler.compensationCalls != 1 {
		t.Fatalf("handler external=%d compensation=%d, want 1/1", handler.externalExecuted, handler.compensationCalls)
	}
	if len(meter.begun) != 1 || len(meter.completed) != 1 {
		t.Fatalf("usage begun=%+v completed=%+v", meter.begun, meter.completed)
	}
	terminal := meter.completed[0]
	if terminal.Pending || terminal.Success || terminal.ExternalCompensation != ExternalCompensationSucceeded || !strings.Contains(terminal.Error, firstFailure.Error()) {
		t.Fatalf("retried terminal usage = %+v", terminal)
	}
	if got := strings.Join(meter.events, ","); got != "begin,complete,complete" {
		t.Fatalf("meter events = %q, want begin,complete,complete", got)
	}
	if result.Usage.ExternalCompensation != ExternalCompensationSucceeded || result.Usage.Success {
		t.Fatalf("completion retry result = %+v", result.Usage)
	}
	if len(audit.records) != 3 || audit.records[1].Stage != AuditExecutionStarted || audit.records[2].Stage != AuditExecutionFailed || audit.records[2].Success {
		t.Fatalf("audit records = %+v", audit.records)
	}
}

func TestRuntimeExternalCompensationIgnoresCanceledRequestContext(t *testing.T) {
	registry, runtime, handler, _, meter := externalRuntimeFixture(t)
	meter.completeErr = errors.New("external usage completion failed")
	catalog := registry.Snapshot()
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:external-canceled-cleanup", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	requestContext := context.WithValue(context.Background(), compensationContextKey{}, "trusted-trace")
	canceled, cancel := context.WithCancel(requestContext)
	cancel()
	_, err = runtime.Execute(canceled, CapabilityExecution{
		Token: grant.Token, SessionID: "chat:external-canceled-cleanup", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: json.RawMessage(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "complete execution usage") {
		t.Fatalf("Execute error = %v, want usage completion failure", err)
	}
	if handler.compensationCalls != 1 || handler.compensationCtxErr != nil || !handler.compensationDeadline || handler.compensationValue != "trusted-trace" {
		t.Fatalf("compensation calls=%d context error=%v deadline=%v value=%q", handler.compensationCalls, handler.compensationCtxErr, handler.compensationDeadline, handler.compensationValue)
	}
}

func TestRuntimeExternalCompensationFailurePreservesPendingReceiptAndAudit(t *testing.T) {
	registry, runtime, handler, audit, meter := externalRuntimeFixture(t)
	meter.completeErr = errors.New("external usage completion failed")
	handler.compensateErr = errors.New("remote operation is irreversible")
	result, err := executeExternal(t, registry, runtime, "chat:external-compensation-fail")
	if !errors.Is(err, ErrExternalCompensation) {
		t.Fatalf("Execute error = %v, want ErrExternalCompensation", err)
	}
	if !strings.Contains(err.Error(), meter.completeErr.Error()) || !strings.Contains(err.Error(), handler.compensateErr.Error()) {
		t.Fatalf("Execute error lost completion or compensation cause: %v", err)
	}
	if handler.externalExecuted != 1 || handler.compensationCalls != 1 {
		t.Fatalf("handler external=%d compensation=%d", handler.externalExecuted, handler.compensationCalls)
	}
	if result.Usage.Success || !strings.Contains(result.Usage.Error, ErrExternalCompensation.Error()) {
		t.Fatalf("compensation failure usage = %+v", result.Usage)
	}
	if len(meter.begun) != 1 || !meter.begun[0].Pending || len(meter.completed) != 0 {
		t.Fatalf("pending receipt not preserved: begun=%+v completed=%+v", meter.begun, meter.completed)
	}
	if len(audit.records) != 3 || audit.records[1].Stage != AuditExecutionStarted || audit.records[2].Stage != AuditExecutionFailed || audit.records[2].Success || !strings.Contains(audit.records[2].Error, ErrExternalCompensation.Error()) {
		t.Fatalf("audit records = %+v", audit.records)
	}
	if result.Usage.ExternalCompensation != ExternalCompensationFailed || audit.records[2].ExternalReceiptID != handler.receipt.ID || audit.records[2].ExternalCompensation != ExternalCompensationFailed {
		t.Fatalf("compensation failure metadata result=%+v audit=%+v", result.Usage, audit.records[2])
	}
}

func TestRuntimeSuspendedAndClosedSessionsFailClosed(t *testing.T) {
	registry, runtime, _, _, handler, _, _, _ := runtimeFixture(t, MutationNone)
	runtime.enforceSessions = true
	if err := runtime.RegisterSession("chat:owned"); err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	catalog := registry.Snapshot()
	request := CapabilityRequest{
		SessionID: "chat:owned", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	}
	grant, err := runtime.RequestCapability(context.Background(), request)
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	if err := runtime.SuspendSession(request.SessionID); err != nil {
		t.Fatalf("SuspendSession: %v", err)
	}
	if runtime.IsSessionActive(request.SessionID) {
		t.Fatal("suspended session remained active")
	}
	if err := runtime.RegisterSession(request.SessionID); err != nil {
		t.Fatalf("idempotent RegisterSession: %v", err)
	}
	if runtime.IsSessionActive(request.SessionID) {
		t.Fatal("idempotent registration reactivated a suspended session")
	}
	if _, err := runtime.RequestCapability(context.Background(), request); !errors.Is(err, ErrSessionSuspended) {
		t.Fatalf("suspended issuance = %v, want ErrSessionSuspended", err)
	}
	if _, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: request.SessionID, CatalogRevision: request.CatalogRevision,
		ToolID: request.ToolID, Arguments: request.Arguments,
	}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("suspended redemption = %v, want ErrInvalidCapability", err)
	}
	if handler.executed != 0 {
		t.Fatalf("suspended capability executed handler %d times", handler.executed)
	}
	if err := runtime.ActivateSession(request.SessionID); err != nil {
		t.Fatalf("ActivateSession: %v", err)
	}
	if !runtime.IsSessionActive(request.SessionID) {
		t.Fatal("resumed session is not active")
	}
	if err := runtime.UnregisterSession(request.SessionID); err != nil {
		t.Fatalf("UnregisterSession: %v", err)
	}
	if _, err := runtime.RequestCapability(context.Background(), request); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("closed issuance = %v, want ErrUnknownSession", err)
	}
}

func TestRuntimeCapabilityCannotSurviveSessionIncarnationChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Runtime, string) error
	}{
		{
			name: "unregister and same ID registration",
			mutate: func(runtime *Runtime, sessionID string) error {
				if err := runtime.UnregisterSession(sessionID); err != nil {
					return err
				}
				return runtime.RegisterSession(sessionID)
			},
		},
		{
			name: "unregister all and same ID registration",
			mutate: func(runtime *Runtime, sessionID string) error {
				runtime.UnregisterAllSessions()
				return runtime.RegisterSession(sessionID)
			},
		},
		{
			name: "suspend and activate",
			mutate: func(runtime *Runtime, sessionID string) error {
				if err := runtime.SuspendSession(sessionID); err != nil {
					return err
				}
				return runtime.ActivateSession(sessionID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, runtime, _, _, handler, _, _, _ := runtimeFixture(t, MutationNone)
			runtime.enforceSessions = true
			const sessionID = "chat:incarnation"
			if err := runtime.RegisterSession(sessionID); err != nil {
				t.Fatalf("RegisterSession: %v", err)
			}
			catalog := registry.Snapshot()
			request := CapabilityRequest{
				SessionID: sessionID, CatalogRevision: catalog.Revision,
				ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
			}
			grant, err := runtime.RequestCapability(context.Background(), request)
			if err != nil {
				t.Fatalf("RequestCapability: %v", err)
			}
			if err := tt.mutate(runtime, sessionID); err != nil {
				t.Fatalf("change session incarnation: %v", err)
			}
			if _, err := runtime.Execute(context.Background(), CapabilityExecution{
				Token: grant.Token, SessionID: sessionID, CatalogRevision: catalog.Revision,
				ToolID: request.ToolID, Arguments: request.Arguments,
			}); !errors.Is(err, ErrInvalidCapability) {
				t.Fatalf("old capability execution = %v, want ErrInvalidCapability", err)
			}
			if handler.executed != 0 {
				t.Fatalf("old capability executed handler %d times", handler.executed)
			}

			fresh, err := runtime.RequestCapability(context.Background(), request)
			if err != nil {
				t.Fatalf("fresh RequestCapability: %v", err)
			}
			if _, err := runtime.Execute(context.Background(), CapabilityExecution{
				Token: fresh.Token, SessionID: sessionID, CatalogRevision: catalog.Revision,
				ToolID: request.ToolID, Arguments: request.Arguments,
			}); err != nil {
				t.Fatalf("fresh capability execution: %v", err)
			}
		})
	}
}

func TestRuntimeUnregisterAllSessionsDropsOutstandingCapabilities(t *testing.T) {
	registry, runtime, _, _, handler, _, _, _ := runtimeFixture(t, MutationNone)
	runtime.enforceSessions = true
	const sessionID = "chat:workspace-reset"
	if err := runtime.RegisterSession(sessionID); err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	catalog := registry.Snapshot()
	arguments := json.RawMessage(`{"path":"README.md"}`)
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	if len(runtime.capabilities) != 1 {
		t.Fatalf("capabilities before reset = %d, want 1", len(runtime.capabilities))
	}
	runtime.UnregisterAllSessions()
	if len(runtime.capabilities) != 0 {
		t.Fatalf("capabilities after reset = %d, want 0", len(runtime.capabilities))
	}
	if _, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("Execute after reset = %v, want ErrInvalidCapability", err)
	}
	if handler.executed != 0 {
		t.Fatalf("reset capability executed handler %d times", handler.executed)
	}
}

func TestRuntimeWorkspaceSnapshotRestoresSessionButBurnsOutstandingCapability(t *testing.T) {
	registry, runtime, _, _, handler, _, _, _ := runtimeFixture(t, MutationNone)
	runtime.enforceSessions = true
	const sessionID = "chat:workspace-rollback"
	if err := runtime.RegisterSession(sessionID); err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	catalog := registry.Snapshot()
	arguments := json.RawMessage(`{"path":"README.md"}`)
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	snapshot := runtime.CaptureSnapshot()
	runtime.UnregisterAllSessions()
	runtime.RestoreSnapshot(snapshot)
	if !runtime.IsSessionRegistered(sessionID) {
		t.Fatal("workspace rollback did not restore the session namespace")
	}
	if _, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("Execute restored workspace capability = %v, want ErrInvalidCapability", err)
	}
	if handler.executed != 0 {
		t.Fatalf("restored capability executed handler %d times", handler.executed)
	}
}

func TestRuntimeSessionChangeDuringApprovalDoesNotIssueCapability(t *testing.T) {
	registry, runtime, budget, _, _, _, _, _ := runtimeFixture(t, MutationNone)
	runtime.enforceSessions = true
	approver := &blockingApprover{entered: make(chan struct{}), release: make(chan struct{})}
	runtime.approver = approver
	const sessionID = "chat:approval-race"
	if err := runtime.RegisterSession(sessionID); err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	request := CapabilityRequest{
		SessionID: sessionID, CatalogRevision: registry.Snapshot().Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	}
	result := make(chan error, 1)
	go func() {
		_, err := runtime.RequestCapability(context.Background(), request)
		result <- err
	}()
	<-approver.entered
	if err := runtime.UnregisterSession(sessionID); err != nil {
		t.Fatalf("UnregisterSession: %v", err)
	}
	if err := runtime.RegisterSession(sessionID); err != nil {
		t.Fatalf("same-ID RegisterSession: %v", err)
	}
	close(approver.release)
	if err := <-result; !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("RequestCapability after session change = %v, want ErrInvalidCapability", err)
	}
	if budget.reserved != 0 {
		t.Fatalf("session change during approval spent %d budget units", budget.reserved)
	}
}

func TestRuntimePostReserveRandomFailureReleasesReservation(t *testing.T) {
	registry, runtime, budget, _, _, _, _, _ := runtimeFixture(t, MutationNone)
	// An empty reader deterministically fails after Reserve has succeeded.
	runtime.random = strings.NewReader("")
	catalog := registry.Snapshot()
	request := CapabilityRequest{
		SessionID: "chat:random-failure", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	}
	if _, err := runtime.RequestCapability(context.Background(), request); err == nil {
		t.Fatal("RequestCapability unexpectedly succeeded with a failing random source")
	}
	if budget.remaining != 10 || budget.released != 1 {
		t.Fatalf("post-reserve random failure remaining=%d released=%d, want 10/1", budget.remaining, budget.released)
	}
	if len(runtime.capabilities) != 0 {
		t.Fatalf("random failure left %d capability entries", len(runtime.capabilities))
	}

	// A later request can use the restored slot; an issued capability remains
	// charged and is covered by the existing abandoned-capability tests.
	runtime.random = strings.NewReader(strings.Repeat("r", 32))
	if _, err := runtime.RequestCapability(context.Background(), request); err != nil {
		t.Fatalf("RequestCapability after reservation rollback: %v", err)
	}
	if budget.remaining != 9 {
		t.Fatalf("remaining after successful issuance=%d, want 9", budget.remaining)
	}
}

func TestRuntimePostReserveAuditFailureReleasesReservation(t *testing.T) {
	registry, runtime, budget, _, _, _, _, _ := runtimeFixture(t, MutationNone)
	auditFailure := errors.New("audit destination unavailable")
	runtime.audit = &failingAudit{failStage: AuditCapabilityIssued, err: auditFailure}
	catalog := registry.Snapshot()
	request := CapabilityRequest{
		SessionID: "chat:audit-failure", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	}
	if _, err := runtime.RequestCapability(context.Background(), request); !errors.Is(err, auditFailure) {
		t.Fatalf("audit failure = %v, want source audit error", err)
	}
	if budget.remaining != 10 || budget.released != 1 {
		t.Fatalf("post-reserve audit failure remaining=%d released=%d, want 10/1", budget.remaining, budget.released)
	}
	if len(runtime.capabilities) != 0 {
		t.Fatalf("audit failure left %d capability entries", len(runtime.capabilities))
	}
}

func TestRuntimePostReserveSessionChangeReleasesReservation(t *testing.T) {
	registry, runtime, budget, _, _, _, _, _ := runtimeFixture(t, MutationNone)
	runtime.enforceSessions = true
	const sessionID = "chat:post-reserve-race"
	if err := runtime.RegisterSession(sessionID); err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	budget.setAfterReserve(func() {
		if err := runtime.UnregisterSession(sessionID); err != nil {
			t.Errorf("UnregisterSession after Reserve: %v", err)
		}
	})
	catalog := registry.Snapshot()
	_, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	})
	if !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("post-reserve session change = %v, want ErrInvalidCapability", err)
	}
	if budget.remaining != 10 || budget.released != 1 {
		t.Fatalf("post-reserve session change remaining=%d released=%d, want 10/1", budget.remaining, budget.released)
	}
	if len(runtime.capabilities) != 0 {
		t.Fatalf("session change left %d capability entries", len(runtime.capabilities))
	}
}

func TestRuntimeIdempotentSessionRegistrationKeepsCapabilityValid(t *testing.T) {
	registry, runtime, _, _, handler, _, _, _ := runtimeFixture(t, MutationNone)
	runtime.enforceSessions = true
	const sessionID = "chat:idempotent"
	if err := runtime.RegisterSession(sessionID); err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	catalog := registry.Snapshot()
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	if err := runtime.RegisterSession(sessionID); err != nil {
		t.Fatalf("idempotent RegisterSession: %v", err)
	}
	if _, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	}); err != nil {
		t.Fatalf("capability after idempotent registration: %v", err)
	}
	if handler.executed != 1 {
		t.Fatalf("handler executions = %d, want 1", handler.executed)
	}
}

func TestRuntimeDenialAndMissingHandlerDoNotSpendBudget(t *testing.T) {
	registry, runtime, budget, approver, handler, _, _, _ := runtimeFixture(t, MutationNone)
	approver.approved = false
	catalog := registry.Snapshot()
	request := CapabilityRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	}
	if _, err := runtime.RequestCapability(context.Background(), request); !errors.Is(err, ErrApprovalDenied) {
		t.Fatalf("denial error = %v, want ErrApprovalDenied", err)
	}
	if budget.reserved != 0 || handler.prepared != 1 {
		t.Fatalf("denial budget=%d prepared=%d", budget.reserved, handler.prepared)
	}

	if err := runtime.UnregisterHandler("read"); err != nil {
		t.Fatalf("UnregisterHandler: %v", err)
	}
	approver.approved = true
	if _, err := runtime.RequestCapability(context.Background(), request); !errors.Is(err, ErrHandlerUnavailable) {
		t.Fatalf("missing handler error = %v, want ErrHandlerUnavailable", err)
	}
	if budget.reserved != 0 || len(approver.calls) != 1 {
		t.Fatalf("missing handler reached approval/budget: approvals=%d budget=%d", len(approver.calls), budget.reserved)
	}
}

func TestRuntimePolicyDenialIsRecheckedAndBurnsStaleCapability(t *testing.T) {
	registry, runtime, budget, _, handler, _, _, _ := runtimeFixture(t, MutationNone)
	policy := &testInvocationPolicy{}
	runtime.policy = policy
	catalog := registry.Snapshot()
	request := CapabilityRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
	}

	if _, err := runtime.RequestCapability(context.Background(), request); err == nil {
		t.Fatal("policy denial unexpectedly issued a capability")
	}
	if budget.reserved != 0 || handler.prepared != 0 {
		t.Fatalf("pre-prepare policy denial budget=%d prepared=%d", budget.reserved, handler.prepared)
	}

	policy.setAllowed(true)
	grant, err := runtime.RequestCapability(context.Background(), request)
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	policy.setAllowed(false)
	_, err = runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: request.SessionID, CatalogRevision: request.CatalogRevision,
		ToolID: request.ToolID, Arguments: request.Arguments,
	})
	if !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("stale policy execution error = %v, want ErrInvalidCapability", err)
	}
	if handler.executed != 0 {
		t.Fatalf("stale policy executed handler %d times", handler.executed)
	}
	if _, replayErr := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: request.SessionID, CatalogRevision: request.CatalogRevision,
		ToolID: request.ToolID, Arguments: request.Arguments,
	}); !errors.Is(replayErr, ErrInvalidCapability) {
		t.Fatalf("policy-denied capability was not burned: %v", replayErr)
	}
}

func TestRuntimeCapabilityRejectsArgumentSessionEpochWorkspaceAndCatalogChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Registry, *testBudget, *uint64) error
		exec   func(*CapabilityExecution)
	}{
		{
			name: "arguments",
			exec: func(execution *CapabilityExecution) {
				execution.Arguments = json.RawMessage(`{"path":"other.txt"}`)
			},
		},
		{
			name: "session",
			exec: func(execution *CapabilityExecution) {
				execution.SessionID = "chat-2"
			},
		},
		{
			name: "budget epoch",
			mutate: func(_ *Registry, budget *testBudget, _ *uint64) error {
				budget.reset(10)
				return nil
			},
		},
		{
			name: "workspace generation",
			mutate: func(_ *Registry, _ *testBudget, generation *uint64) error {
				(*generation)++
				return nil
			},
		},
		{
			name: "catalog revision",
			mutate: func(registry *Registry, _ *testBudget, _ *uint64) error {
				_, err := registry.ReplaceSource(SourceMCP, []ToolDef{
					testTool("mcp.server.echo", SourceMCP, `{ "type":"object", "additionalProperties":false }`),
				})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, runtime, budget, _, handler, _, _, generation := runtimeFixture(t, MutationNone)
			catalog := registry.Snapshot()
			grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
				SessionID: "chat-1", CatalogRevision: catalog.Revision,
				ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
			})
			if err != nil {
				t.Fatalf("RequestCapability: %v", err)
			}
			if tt.mutate != nil {
				if err := tt.mutate(registry, budget, generation); err != nil {
					t.Fatalf("mutate: %v", err)
				}
			}
			execution := CapabilityExecution{
				Token: grant.Token, SessionID: "chat-1", CatalogRevision: catalog.Revision,
				ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
			}
			if tt.exec != nil {
				tt.exec(&execution)
			}
			if _, err := runtime.Execute(context.Background(), execution); !errors.Is(err, ErrInvalidCapability) {
				t.Fatalf("Execute error = %v, want ErrInvalidCapability", err)
			}
			if handler.executed != 0 {
				t.Fatalf("invalid capability executed handler %d times", handler.executed)
			}
			// Even a mismatch consumes the token so an attacker cannot probe and
			// then retry with the correct binding.
			if _, err := runtime.Execute(context.Background(), CapabilityExecution{
				Token: grant.Token, SessionID: "chat-1", CatalogRevision: registry.Snapshot().Revision,
				ToolID: "read", Arguments: json.RawMessage(`{"path":"README.md"}`),
			}); !errors.Is(err, ErrInvalidCapability) {
				t.Fatalf("second use error = %v, want ErrInvalidCapability", err)
			}
		})
	}
}

func TestRuntimeRequiresHandlerMutationContract(t *testing.T) {
	registry, err := NewRegistry([]ToolDef{
		func() ToolDef {
			def := testTool("write", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`)
			def.Mutation = MutationWorkspaceTransaction
			def.Risk = RiskElevated
			def.Approval = ApprovalManual
			return def
		}(),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	runtime, err := NewRuntime(registry, RuntimeOptions{
		Budget: &testBudget{epoch: 1, remaining: 1}, Approver: &testApprover{approved: true},
		Audit:               &recordingAudit{},
		WorkspaceGeneration: func() uint64 { return 1 },
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if err := runtime.RegisterHandler("write", &testHandler{mutation: MutationNone}); !errors.Is(err, ErrMutationContract) {
		t.Fatalf("RegisterHandler mismatch = %v, want ErrMutationContract", err)
	}
}

func TestNewRuntimeRejectsMissingAuditSink(t *testing.T) {
	registry, err := NewRegistry([]ToolDef{testTool("read", SourceBuiltin, `{
		"type":"object",
		"additionalProperties":false
	}`)})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	_, err = NewRuntime(registry, RuntimeOptions{
		Budget:              &testBudget{epoch: 1, remaining: 1},
		Approver:            &testApprover{approved: true},
		WorkspaceGeneration: func() uint64 { return 1 },
	})
	if err == nil {
		t.Fatal("NewRuntime accepted a missing audit sink")
	}
}

func TestRequestCapabilityRevokesTokenWhenAuditPersistenceFails(t *testing.T) {
	registry, runtime, _, _, handler, _, _, _ := runtimeFixture(t, MutationNone)
	runtime.audit = &failingAudit{
		failStage: AuditCapabilityIssued,
		err:       errors.New("injected audit persistence failure"),
	}
	catalog := registry.Snapshot()
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID:       "chat:audit-failure",
		CatalogRevision: catalog.Revision,
		ToolID:          "read",
		Arguments:       json.RawMessage(`{"path":"README.md"}`),
	})
	if !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("RequestCapability error = %v, want ErrAuditUnavailable", err)
	}
	if grant.Token != "" {
		t.Fatalf("RequestCapability returned token %q after audit failure", grant.Token)
	}
	runtime.mu.Lock()
	remaining := len(runtime.capabilities)
	runtime.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("runtime retained %d capability after audit failure", remaining)
	}
	if handler.executed != 0 {
		t.Fatalf("handler executed %d times after capability audit failure", handler.executed)
	}
}

func TestExecuteDoesNotReachHandlerWhenAdmissionAuditFails(t *testing.T) {
	registry, runtime, _, _, handler, _, meter := transactionalRuntimeFixture(t)
	catalog := registry.Snapshot()
	arguments := json.RawMessage(`{"path":"README.md"}`)
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:audit-admission", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	runtime.audit = &failingAudit{
		failStage: AuditExecutionStarted,
		err:       errors.New("injected execution audit failure"),
	}
	result, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat:audit-admission", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("Execute error = %v, want ErrAuditUnavailable", err)
	}
	if handler.executed != 0 {
		t.Fatalf("handler executed %d times after admission audit failure", handler.executed)
	}
	if result.Observation != "" || result.Usage.Success || result.Usage.Pending {
		t.Fatalf("unexpected result after admission audit failure: %+v", result)
	}
	meter.mu.Lock()
	defer meter.mu.Unlock()
	if len(meter.begun) != 1 || len(meter.completed) != 1 {
		t.Fatalf("usage begin/completion counts = %d/%d, want 1/1", len(meter.begun), len(meter.completed))
	}
	if meter.completed[0].Success || meter.completed[0].Pending {
		t.Fatalf("audit failure usage was not terminal failure: %+v", meter.completed[0])
	}
}

func TestExecuteSuppressesObservationWhenTerminalAuditFails(t *testing.T) {
	registry, runtime, _, _, handler, _, meter := transactionalRuntimeFixture(t)
	catalog := registry.Snapshot()
	arguments := json.RawMessage(`{"path":"README.md"}`)
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:audit-terminal", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	runtime.audit = &failingAudit{
		failStage: AuditExecutionCompleted,
		err:       errors.New("injected terminal audit failure"),
	}
	result, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat:audit-terminal", CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: arguments,
	})
	if !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("Execute error = %v, want ErrAuditUnavailable", err)
	}
	if result.Observation != "" {
		t.Fatalf("terminal audit failure exposed observation %q", result.Observation)
	}
	if handler.executed != 1 {
		t.Fatalf("handler executed %d times, want 1 before terminal audit failure", handler.executed)
	}
	if !result.Usage.Success {
		t.Fatalf("terminal audit failure rewrote durable execution truth: %+v", result.Usage)
	}
	meter.mu.Lock()
	defer meter.mu.Unlock()
	if len(meter.completed) != 1 || !meter.completed[0].Success {
		t.Fatalf("terminal usage = %+v, want one successful durable record", meter.completed)
	}
}

func TestExternalExecutionAuditFailureTerminalizesPreallocatedReceiptWithoutHandler(t *testing.T) {
	registry, runtime, handler, _, meter := externalRuntimeFixture(t)
	catalog := registry.Snapshot()
	arguments := json.RawMessage(`{}`)
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:audit-external", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	runtime.audit = &failingAudit{
		failStage: AuditExecutionStarted,
		err:       errors.New("injected external admission audit failure"),
	}
	result, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat:audit-external", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: arguments,
	})
	if !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("Execute error = %v, want ErrAuditUnavailable", err)
	}
	if handler.beginCalls != 1 || handler.externalExecuted != 0 || handler.compensationCalls != 0 {
		t.Fatalf("external calls begin=%d execute=%d compensate=%d, want 1/0/0", handler.beginCalls, handler.externalExecuted, handler.compensationCalls)
	}
	if result.Usage.Success || result.Usage.ExternalReceiptID != handler.receipt.ID || result.Usage.ExternalCompensation != ExternalCompensationNotNeeded {
		t.Fatalf("external audit failure usage = %+v", result.Usage)
	}
	if len(meter.begun) != 1 || len(meter.completed) != 1 || meter.completed[0].Pending || meter.completed[0].Success {
		t.Fatalf("external usage begun=%+v completed=%+v", meter.begun, meter.completed)
	}
}

func TestRuntimeRequiresWorkspaceTransactionHandlerAtRegistration(t *testing.T) {
	def := testTool("write", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`)
	def.Risk = RiskElevated
	def.Approval = ApprovalManual
	def.Mutation = MutationWorkspaceTransaction
	registry, err := NewRegistry([]ToolDef{def})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	runtime, err := NewRuntime(registry, RuntimeOptions{
		Budget: &testBudget{epoch: 1, remaining: 2}, Approver: &testApprover{approved: true},
		Audit:               &recordingAudit{},
		WorkspaceGeneration: func() uint64 { return 1 },
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if err := runtime.RegisterHandler("write", &testHandler{mutation: MutationWorkspaceTransaction}); !errors.Is(err, ErrMutationContract) {
		t.Fatalf("RegisterHandler without transaction method = %v, want ErrMutationContract", err)
	}
	transactionHandler := &transactionTestHandler{testHandler: testHandler{mutation: MutationWorkspaceTransaction}}
	if err := runtime.RegisterHandler("write", transactionHandler); err != nil {
		t.Fatalf("RegisterHandler transaction handler: %v", err)
	}
}

func TestRuntimeRequiresExternalMutationHandlerAtRegistration(t *testing.T) {
	def := testTool("external", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`)
	def.Risk = RiskElevated
	def.Approval = ApprovalManual
	def.Mutation = MutationExternal
	registry, err := NewRegistry([]ToolDef{def})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	runtime, err := NewRuntime(registry, RuntimeOptions{
		Budget: &testBudget{epoch: 1, remaining: 2}, Approver: &testApprover{approved: true},
		Audit:               &recordingAudit{},
		WorkspaceGeneration: func() uint64 { return 1 },
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if err := runtime.RegisterHandler("external", &testHandler{mutation: MutationExternal}); !errors.Is(err, ErrExternalMutationContract) {
		t.Fatalf("RegisterHandler without external transaction methods = %v, want ErrExternalMutationContract", err)
	}
}

func TestExecuteHandlerRejectsExternalMutationWithoutContract(t *testing.T) {
	def := testTool("external", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`)
	def.Risk = RiskElevated
	def.Approval = ApprovalManual
	def.Mutation = MutationExternal
	handler := &testHandler{mutation: MutationExternal}
	_, _, err := executeHandler(context.Background(), handler, Invocation{Tool: def}, PreparedExecution{})
	if !errors.Is(err, ErrExternalMutationContract) {
		t.Fatalf("executeHandler error = %v, want ErrExternalMutationContract", err)
	}
	if handler.executed != 0 {
		t.Fatalf("generic handler executed %d times", handler.executed)
	}
}

func TestRuntimeExternalMutationBeginFailurePreventsSideEffect(t *testing.T) {
	registry, runtime, handler, audit, meter := externalRuntimeFixture(t)
	meter.beginErr = errors.New("external ledger is read-only")
	catalog := registry.Snapshot()
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:external-begin-fail", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	_, err = runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat:external-begin-fail", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: json.RawMessage(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "begin execution usage") {
		t.Fatalf("Execute error = %v, want begin usage failure", err)
	}
	if handler.externalExecuted != 0 || handler.compensated != 0 {
		t.Fatalf("begin failure executed=%d compensated=%d, want 0/0", handler.externalExecuted, handler.compensated)
	}
	if len(audit.records) != 2 || audit.records[1].Stage != AuditExecutionFailed {
		t.Fatalf("audit records = %+v", audit.records)
	}
}

func TestRuntimeExternalMutationCompletionFailureCompensatesOnce(t *testing.T) {
	registry, runtime, handler, audit, meter := externalRuntimeFixture(t)
	meter.completeErr = errors.New("external ledger completion failed")
	catalog := registry.Snapshot()
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:external-complete-fail", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	result, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat:external-complete-fail", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: json.RawMessage(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "complete execution usage") {
		t.Fatalf("Execute error = %v, want completion failure", err)
	}
	if handler.externalExecuted != 1 || handler.compensated != 1 {
		t.Fatalf("external calls executed=%d compensated=%d, want 1/1", handler.externalExecuted, handler.compensated)
	}
	if result.Usage.Success || len(meter.begun) != 1 || len(meter.completed) != 0 {
		t.Fatalf("result=%+v begun=%+v completed=%+v", result, meter.begun, meter.completed)
	}
	if len(audit.records) != 3 || audit.records[1].Stage != AuditExecutionStarted || audit.records[2].Stage != AuditExecutionFailed || audit.records[2].Success {
		t.Fatalf("audit records = %+v", audit.records)
	}
}

func TestRuntimeExternalExecutionAndMeterFailureCompensatesOnlyOnce(t *testing.T) {
	registry, runtime, handler, _, meter := externalRuntimeFixture(t)
	handler.executeErr = errors.New("external adapter failed after mutation")
	meter.completeErr = errors.New("terminal usage write failed")
	catalog := registry.Snapshot()
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:external-double-fail", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	_, err = runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat:external-double-fail", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: json.RawMessage(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "external adapter failed") || !strings.Contains(err.Error(), "terminal usage write failed") {
		t.Fatalf("Execute error = %v, want joined execution and meter failures", err)
	}
	if handler.externalExecuted != 1 || handler.compensated != 1 {
		t.Fatalf("external calls executed=%d compensated=%d, want 1/1", handler.externalExecuted, handler.compensated)
	}
}

func TestRuntimeExternalIrreversibleReceiptCannotClaimCompensation(t *testing.T) {
	registry, runtime, handler, _, meter := externalRuntimeFixture(t)
	handler.receipt.Reversible = false
	meter.completeErr = errors.New("terminal usage write failed")
	catalog := registry.Snapshot()
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat:external-irreversible", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	_, err = runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat:external-irreversible", CatalogRevision: catalog.Revision,
		ToolID: "external", Arguments: json.RawMessage(`{}`),
	})
	if !errors.Is(err, ErrExternalMutationIrreversible) {
		t.Fatalf("Execute error = %v, want ErrExternalMutationIrreversible", err)
	}
	if handler.compensated != 1 {
		t.Fatalf("compensation calls = %d, want 1", handler.compensated)
	}
}

func TestRuntimeExecutesWorkspaceTransactionEntryPoint(t *testing.T) {
	def := testTool("write", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`)
	def.Risk = RiskElevated
	def.Approval = ApprovalManual
	def.Mutation = MutationWorkspaceTransaction
	registry, err := NewRegistry([]ToolDef{def})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	runtime, err := NewRuntime(registry, RuntimeOptions{
		Budget: &testBudget{epoch: 1, remaining: 2}, Approver: &testApprover{approved: true},
		Audit:               &recordingAudit{},
		WorkspaceGeneration: func() uint64 { return 1 },
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	handler := &transactionTestHandler{testHandler: testHandler{mutation: MutationWorkspaceTransaction}}
	if err := runtime.RegisterHandler("write", handler); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}
	catalog := registry.Snapshot()
	grant, err := runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat-transaction", CatalogRevision: catalog.Revision,
		ToolID: "write", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("RequestCapability: %v", err)
	}
	result, err := runtime.Execute(context.Background(), CapabilityExecution{
		Token: grant.Token, SessionID: "chat-transaction", CatalogRevision: catalog.Revision,
		ToolID: "write", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Observation != "transaction-ok" {
		t.Fatalf("observation = %q, want transaction-ok", result.Observation)
	}
	if handler.transactionExecuted != 1 || handler.executed != 0 {
		t.Fatalf("transaction calls = %d, generic calls = %d; want 1/0", handler.transactionExecuted, handler.executed)
	}
}

func TestRuntimeRejectsDynamicWorkspaceTransactionWithoutHandlerContract(t *testing.T) {
	registry, runtime, budget, approver, _, _, _, _ := runtimeFixture(t, MutationNone)
	def := testTool("mcp.transaction", SourceMCP, `{ "type":"object", "additionalProperties":false }`)
	def.ExecuteKey = "read"
	def.Risk = RiskElevated
	def.Approval = ApprovalManual
	def.Mutation = MutationWorkspaceTransaction
	catalog, err := registry.ReplaceSource(SourceMCP, []ToolDef{def})
	if err != nil {
		t.Fatalf("ReplaceSource: %v", err)
	}
	_, err = runtime.RequestCapability(context.Background(), CapabilityRequest{
		SessionID: "chat-dynamic", CatalogRevision: catalog.Revision,
		ToolID: "mcp.transaction", Arguments: json.RawMessage(`{}`),
	})
	if !errors.Is(err, ErrMutationContract) {
		t.Fatalf("dynamic transaction contract error = %v, want ErrMutationContract", err)
	}
	if budget.reserved != 0 || len(approver.calls) != 0 {
		t.Fatalf("dynamic contract failure reached approval/budget: approvals=%d budget=%d", len(approver.calls), budget.reserved)
	}
}
