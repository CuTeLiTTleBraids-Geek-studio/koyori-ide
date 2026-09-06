package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A workflow bearer ID must not be enough to cross a Wails window boundary.
func TestTaskServiceWorkflowSessionRejectsCrossServiceTerminalization(t *testing.T) {
	agent := newTaskTestAgent(t)
	ownerTask := NewTaskService(agent)
	otherTask := NewTaskService(agent)
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := WireTaskAgentLifecycle(ownerTask, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle owner: %v", err)
	}
	if err := WireTaskAgentLifecycle(otherTask, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle other: %v", err)
	}
	ownerCtx := withAgentCallerContext(context.Background(), "owner-window")
	otherCtx := withAgentCallerContext(context.Background(), "other-window")
	sessionID, err := ownerTask.BeginWorkflowExecution(ownerCtx, "owner-proof")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}
	if _, err := otherTask.BeginWorkflowExecution(nil, "missing-context"); !errors.Is(err, ErrNotAllowed) { //nolint:staticcheck // SA1012: intentionally nil, asserts fail-closed
		t.Fatalf("contextless BeginWorkflowExecution = %v, want ErrNotAllowed", err)
	}
	if err := otherTask.CompleteWorkflowExecution(otherCtx, sessionID); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-service CompleteWorkflowExecution = %v, want ErrNotAllowed", err)
	}
	if err := otherTask.ResumeWorkflowExecution(otherCtx, sessionID); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-service ResumeWorkflowExecution = %v, want ErrNotAllowed", err)
	}
	if _, err := otherTask.RequestWorkflowStepApproval(otherCtx, sessionID, "owner-proof", "step"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-service RequestWorkflowStepApproval = %v, want ErrNotAllowed", err)
	}
	if err := otherTask.FailWorkflowExecution(otherCtx, sessionID, "forged terminalization"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-service FailWorkflowExecution = %v, want ErrNotAllowed", err)
	}
	row, err := lifecycle.GetByID(sessionID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if row.Status != "running" {
		t.Fatalf("cross-service terminalization changed status to %s", row.Status)
	}
	if err := ownerTask.FailWorkflowExecution(ownerCtx, sessionID, "owner cleanup"); err != nil {
		t.Fatalf("owner FailWorkflowExecution: %v", err)
	}
}

func TestTaskServiceWorkflowApprovalRejectsCrossWindowBeforeTokenConsumption(t *testing.T) {
	agent := newTaskTestAgent(t)
	ownerTask := NewTaskService(agent)
	otherTask := NewTaskService(agent)
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := WireTaskAgentLifecycle(ownerTask, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle owner: %v", err)
	}
	if err := WireTaskAgentLifecycle(otherTask, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle other: %v", err)
	}
	ownerCtx := withAgentCallerContext(context.Background(), "approval-owner")
	otherCtx := withAgentCallerContext(context.Background(), "approval-other")
	sessionID, err := ownerTask.BeginWorkflowExecution(ownerCtx, "owner-proof")
	if err != nil {
		t.Fatalf("BeginWorkflowExecution: %v", err)
	}

	spentBefore := agent.GetToolBudget().Spent
	if _, err := otherTask.RequestWorkflowStepApproval(otherCtx, sessionID, "owner-proof", "step"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-window RequestWorkflowStepApproval = %v, want ErrNotAllowed", err)
	}
	if _, err := otherTask.RequestExecutionApproval(otherCtx, sessionID, "go", ""); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-window generic workflow approval = %v, want ErrNotAllowed", err)
	}
	if spent := agent.GetToolBudget().Spent; spent != spentBefore {
		t.Fatalf("cross-window approval request consumed budget: before=%d after=%d", spentBefore, spent)
	}
	otherTask.mu.Lock()
	if len(otherTask.approvals) != 0 {
		t.Fatalf("cross-window request stored %d approvals", len(otherTask.approvals))
	}
	otherTask.mu.Unlock()

	const token = "owner-capability"
	otherTask.mu.Lock()
	otherTask.approvals[token] = taskExecutionApproval{
		executionID: sessionID,
		sessionID:   sessionID,
		owner:       "approval-owner",
		workflow:    "owner-proof",
		step:        "step",
		capability:  AgentToolCapability{Token: token},
		expiresAt:   time.Now().Add(time.Minute),
	}
	otherTask.mu.Unlock()
	if _, err := otherTask.ExecuteApprovedWorkflowStep(otherCtx, sessionID, "owner-proof", "step", token); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-window ExecuteApprovedWorkflowStep = %v, want ErrNotAllowed", err)
	}
	otherTask.mu.Lock()
	_, stillPending := otherTask.approvals[token]
	otherTask.mu.Unlock()
	if !stillPending {
		t.Fatal("cross-window execution consumed approval before owner check")
	}
	if err := ownerTask.FailWorkflowExecution(ownerCtx, sessionID, "owner cleanup"); err != nil {
		t.Fatalf("owner FailWorkflowExecution: %v", err)
	}
}

func TestTaskServiceGenericApprovalRejectsCrossWindowBeforeTokenConsumption(t *testing.T) {
	agent := newTaskTestAgent(t)
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	task := NewTaskService(agent)
	permission := NewAIPermissionService(t.TempDir())
	lifecycle, err := WireAgentLifecycle(
		agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), permission,
	)
	if err != nil {
		t.Fatalf("WireAgentLifecycle: %v", err)
	}
	if err := WireTaskAgentLifecycle(task, lifecycle); err != nil {
		t.Fatalf("WireTaskAgentLifecycle: %v", err)
	}
	ownerCtx := withAgentCallerContext(context.Background(), "generic-owner")
	otherCtx := withAgentCallerContext(context.Background(), "generic-other")
	token, err := task.RequestExecutionApproval(ownerCtx, "generic-task", "go", "")
	if err != nil {
		t.Fatalf("RequestExecutionApproval: %v", err)
	}
	spentBefore := agent.GetToolBudget().Spent
	if _, err := task.ExecuteApproved(otherCtx, "generic-task", "go", "", token); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("cross-window ExecuteApproved = %v, want ErrNotAllowed", err)
	}
	if spent := agent.GetToolBudget().Spent; spent != spentBefore {
		t.Fatalf("cross-window execution changed budget: before=%d after=%d", spentBefore, spent)
	}
	task.mu.Lock()
	_, stillPending := task.approvals[token]
	task.mu.Unlock()
	if !stillPending {
		t.Fatal("cross-window generic execution consumed the owner token")
	}
	if _, err := task.ExecuteApproved(ownerCtx, "generic-task", "go", "", token); err != nil {
		t.Fatalf("owner ExecuteApproved: %v", err)
	}
}
