package services

// Plan 11 Task 9 Step 10 — AIPlanService 测试覆盖。

import (
	"fmt"
	"strings"
	"testing"
)

// mockStepExecutor 用于测试 ExecuteStep。
type mockStepExecutor struct {
	result string
	err    error
}

func (m *mockStepExecutor) Execute(tool, args string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.result, nil
}

func newTestPlanService(t *testing.T) *AIPlanService {
	t.Helper()
	return NewAIPlanService()
}

func TestAIPlanService_CreatePlan(t *testing.T) {
	svc := newTestPlanService(t)
	steps := []PlanStep{
		{Title: "Step 1", Description: "First", Tool: "read_file"},
		{Title: "Step 2", Description: "Second", Tool: "write_file"},
	}
	p, err := svc.CreatePlan("p1", "Build feature", steps)
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}
	if p.ID != "p1" {
		t.Errorf("ID = %q, want p1", p.ID)
	}
	if len(p.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(p.Steps))
	}
	if p.Steps[0].Status != PlanStepPending {
		t.Errorf("step 0 status = %s, want pending", p.Steps[0].Status)
	}
	if p.Status != PlanStatusPending {
		t.Errorf("plan status = %s, want pending", p.Status)
	}
	// Plan 与 Goal 互斥：active 应指向当前 Plan。
	if svc.GetActivePlan() != p {
		t.Error("active plan should be set")
	}
}

func TestAIPlanService_CreatePlan_Duplicate(t *testing.T) {
	svc := newTestPlanService(t)
	_, _ = svc.CreatePlan("dup", "g", nil)
	_, err := svc.CreatePlan("dup", "g2", nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected already-exists, got %v", err)
	}
}

func TestAIPlanService_ApproveStep(t *testing.T) {
	svc := newTestPlanService(t)
	p, _ := svc.CreatePlan("p", "g", []PlanStep{{Title: "s1"}})
	if err := svc.ApproveStep("p", 0); err != nil {
		t.Fatalf("ApproveStep failed: %v", err)
	}
	if p.Steps[0].Status != PlanStepApproved {
		t.Errorf("status = %s, want approved", p.Steps[0].Status)
	}
	if p.ApprovedAt == nil {
		t.Error("ApprovedAt should be set")
	}
}

func TestAIPlanService_ApproveStep_NotPending(t *testing.T) {
	svc := newTestPlanService(t)
	_, _ = svc.CreatePlan("p", "g", []PlanStep{{Title: "s1", Status: PlanStepCompleted}})
	err := svc.ApproveStep("p", 0)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("expected not-allowed for non-pending step, got %v", err)
	}
}

func TestAIPlanService_ApproveAll(t *testing.T) {
	svc := newTestPlanService(t)
	p, _ := svc.CreatePlan("p", "g", []PlanStep{{Title: "s1"}, {Title: "s2"}, {Title: "s3"}})
	_ = svc.ApproveAll("p")
	for i, s := range p.Steps {
		if s.Status != PlanStepApproved {
			t.Errorf("step %d status = %s, want approved", i, s.Status)
		}
	}
}

func TestAIPlanService_RejectAll(t *testing.T) {
	svc := newTestPlanService(t)
	p, _ := svc.CreatePlan("p", "g", []PlanStep{{Title: "s1"}, {Title: "s2"}})
	_ = svc.RejectAll("p")
	for i, s := range p.Steps {
		if s.Status != PlanStepSkipped {
			t.Errorf("step %d status = %s, want skipped", i, s.Status)
		}
	}
}

func TestAIPlanService_EditStep(t *testing.T) {
	svc := newTestPlanService(t)
	_, _ = svc.CreatePlan("p", "g", []PlanStep{{Title: "old"}})
	err := svc.EditStep("p", 0, PlanStep{Title: "new", Tool: "edit_tool"})
	if err != nil {
		t.Fatalf("EditStep failed: %v", err)
	}
	p, _ := svc.GetPlan("p")
	if p.Steps[0].Title != "new" {
		t.Errorf("Title = %q, want new", p.Steps[0].Title)
	}
	if p.Steps[0].Status != PlanStepApproved {
		t.Errorf("status = %s, want approved", p.Steps[0].Status)
	}
}

func TestAIPlanService_ExecuteStep_Success(t *testing.T) {
	svc := newTestPlanService(t)
	p, _ := svc.CreatePlan("p", "g", []PlanStep{{Title: "s1", Tool: "read_file"}})
	_ = svc.ApproveStep("p", 0)
	exec := &mockStepExecutor{result: "file contents"}
	if err := svc.ExecuteStep("p", 0, exec); err != nil {
		t.Fatalf("ExecuteStep failed: %v", err)
	}
	if p.Steps[0].Status != PlanStepCompleted {
		t.Errorf("status = %s, want completed", p.Steps[0].Status)
	}
	if p.Steps[0].Result != "file contents" {
		t.Errorf("Result = %q, want file contents", p.Steps[0].Result)
	}
	if p.Status != PlanStatusCompleted {
		t.Errorf("plan status = %s, want completed", p.Status)
	}
	if p.FinishedAt == nil {
		t.Error("FinishedAt should be set")
	}
}

func TestAIPlanService_ExecuteStep_NotApproved(t *testing.T) {
	svc := newTestPlanService(t)
	_, _ = svc.CreatePlan("p", "g", []PlanStep{{Title: "s1"}})
	err := svc.ExecuteStep("p", 0, &mockStepExecutor{})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("expected not-approved error, got %v", err)
	}
}

func TestAIPlanService_ExecuteStep_Failure_Pauses(t *testing.T) {
	svc := newTestPlanService(t)
	p, _ := svc.CreatePlan("p", "g", []PlanStep{{Title: "s1"}})
	_ = svc.ApproveStep("p", 0)
	exec := &mockStepExecutor{err: fmt.Errorf("tool error")}
	if err := svc.ExecuteStep("p", 0, exec); err == nil {
		t.Error("expected error from executor")
	}
	if p.Steps[0].Status != PlanStepFailed {
		t.Errorf("step status = %s, want failed", p.Steps[0].Status)
	}
	if p.Steps[0].Error != "tool error" {
		t.Errorf("Error = %q, want tool error", p.Steps[0].Error)
	}
	// Step 7：失败暂停 Plan。
	if p.Status != PlanStatusPaused {
		t.Errorf("plan status = %s, want paused", p.Status)
	}
}

func TestAIPlanService_SkipStep(t *testing.T) {
	svc := newTestPlanService(t)
	p, _ := svc.CreatePlan("p", "g", []PlanStep{{Title: "s1"}})
	_ = svc.ApproveStep("p", 0)
	_ = svc.ExecuteStep("p", 0, &mockStepExecutor{err: fmt.Errorf("err")})
	// 暂停后跳过该步骤。
	if err := svc.SkipStep("p", 0); err != nil {
		t.Fatalf("SkipStep failed: %v", err)
	}
	if p.Steps[0].Status != PlanStepSkipped {
		t.Errorf("status = %s, want skipped", p.Steps[0].Status)
	}
	// 暂停状态应恢复。
	if p.Status != PlanStatusExecuting {
		t.Errorf("plan status = %s, want executing", p.Status)
	}
}

func TestAIPlanService_Replan(t *testing.T) {
	svc := newTestPlanService(t)
	p, _ := svc.CreatePlan("p", "g", []PlanStep{
		{Title: "done", Status: PlanStepCompleted},
		{Title: "pending", Status: PlanStepPending},
	})
	newSteps := []PlanStep{{Title: "new step"}}
	if err := svc.Replan("p", newSteps); err != nil {
		t.Fatalf("Replan failed: %v", err)
	}
	// 应保留已完成步骤 + 新步骤。
	if len(p.Steps) != 2 {
		t.Fatalf("expected 2 steps after replan, got %d", len(p.Steps))
	}
	if p.Steps[0].Title != "done" {
		t.Errorf("step 0 title = %q, want done", p.Steps[0].Title)
	}
	if p.Steps[1].Title != "new step" {
		t.Errorf("step 1 title = %q, want new step", p.Steps[1].Title)
	}
	if p.Steps[1].Status != PlanStepPending {
		t.Errorf("new step status = %s, want pending", p.Steps[1].Status)
	}
	if p.Status != PlanStatusPending {
		t.Errorf("plan status = %s, want pending", p.Status)
	}
}

func TestAIPlanService_AbortPlan(t *testing.T) {
	svc := newTestPlanService(t)
	p, _ := svc.CreatePlan("p", "g", []PlanStep{{Title: "s1"}})
	if err := svc.AbortPlan("p"); err != nil {
		t.Fatalf("AbortPlan failed: %v", err)
	}
	if p.Status != PlanStatusAborted {
		t.Errorf("status = %s, want aborted", p.Status)
	}
	if svc.GetActivePlan() != nil {
		t.Error("active plan should be cleared after abort")
	}
}

func TestAIPlanService_GetStepResult(t *testing.T) {
	svc := newTestPlanService(t)
	_, _ = svc.CreatePlan("p", "g", []PlanStep{{Title: "s1", Tool: "t", Args: "a"}})
	step, err := svc.GetStepResult("p", 0)
	if err != nil {
		t.Fatalf("GetStepResult failed: %v", err)
	}
	if step.Tool != "t" {
		t.Errorf("Tool = %q, want t", step.Tool)
	}
}

func TestAIPlanService_ListPlans(t *testing.T) {
	svc := newTestPlanService(t)
	_, _ = svc.CreatePlan("p1", "g1", nil)
	_, _ = svc.CreatePlan("p2", "g2", nil)
	list := svc.ListPlans()
	if len(list) != 2 {
		t.Errorf("expected 2 plans, got %d", len(list))
	}
}

func TestAIPlanService_PlanGoalMutex(t *testing.T) {
	// Step 8：Plan 与 Goal 互斥。创建新 Plan 会替换 active。
	svc := newTestPlanService(t)
	p1, _ := svc.CreatePlan("p1", "g1", nil)
	if svc.GetActivePlan() != p1 {
		t.Error("active should be p1")
	}
	p2, _ := svc.CreatePlan("p2", "g2", nil)
	if svc.GetActivePlan() != p2 {
		t.Error("active should be p2 after new plan")
	}
}
