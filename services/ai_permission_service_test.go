package services

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// newTestAIPermissionService 创建一个使用临时目录的测试服务。
func newTestAIPermissionService(t *testing.T) *AIPermissionService {
	t.Helper()
	dir := t.TempDir()
	return NewAIPermissionService(dir)
}

func TestAIPermission_SetAssignment_GetModelFor(t *testing.T) {
	s := newTestAIPermissionService(t)

	a := ModelAssignment{
		Operation:   AIOpChat,
		ProviderID:  "provider-1",
		Model:       "gpt-4",
		Temperature: 0.7,
		MaxTokens:   4096,
	}
	if err := s.SetAssignment(a); err != nil {
		t.Fatalf("SetAssignment failed: %v", err)
	}

	res := s.GetModelFor(AIOpChat)
	if res.Primary.Model != "gpt-4" {
		t.Errorf("expected model gpt-4, got %s", res.Primary.Model)
	}
	if res.Primary.ProviderID != "provider-1" {
		t.Errorf("expected providerId provider-1, got %s", res.Primary.ProviderID)
	}
	if res.Fallback != nil {
		t.Errorf("expected no fallback, got %+v", res.Fallback)
	}
}

func TestAIPermission_SetAssignment_InvalidOperation(t *testing.T) {
	s := newTestAIPermissionService(t)
	err := s.SetAssignment(ModelAssignment{Operation: "invalid-op"})
	if err == nil {
		t.Error("expected error for invalid operation")
	}
}

func TestAIPermission_GetModelFor_Fallback(t *testing.T) {
	s := newTestAIPermissionService(t)

	a := ModelAssignment{
		Operation:          AIOpChat,
		ProviderID:         "primary-provider",
		Model:              "gpt-4",
		FallbackProviderID: "fallback-provider",
		FallbackModel:      "gpt-3.5-turbo",
	}
	if err := s.SetAssignment(a); err != nil {
		t.Fatalf("SetAssignment failed: %v", err)
	}

	res := s.GetModelFor(AIOpChat)
	if res.Fallback == nil {
		t.Fatal("expected fallback, got nil")
	}
	if res.Fallback.Model != "gpt-3.5-turbo" {
		t.Errorf("expected fallback model gpt-3.5-turbo, got %s", res.Fallback.Model)
	}
	if res.Fallback.ProviderID != "fallback-provider" {
		t.Errorf("expected fallback provider fallback-provider, got %s", res.Fallback.ProviderID)
	}
}

func TestAIPermission_GetModelFor_NoAssignment_ReturnsEmpty(t *testing.T) {
	s := newTestAIPermissionService(t)
	res := s.GetModelFor(AIOpChat)
	if res.Primary.Model != "" {
		t.Errorf("expected empty model for unassigned operation, got %s", res.Primary.Model)
	}
	if res.Fallback != nil {
		t.Errorf("expected no fallback for unassigned operation")
	}
}

func TestAIPermission_IsDisabled(t *testing.T) {
	s := newTestAIPermissionService(t)

	// 默认未禁用
	if s.IsDisabled(AIOpInlineCompletion) {
		t.Error("expected inline-completion not disabled by default")
	}

	// 禁用 inline-completion（Step 6: 操作级权限）
	if err := s.SetAssignment(ModelAssignment{
		Operation: AIOpInlineCompletion,
		Disabled:  true,
	}); err != nil {
		t.Fatalf("SetAssignment failed: %v", err)
	}

	if !s.IsDisabled(AIOpInlineCompletion) {
		t.Error("expected inline-completion disabled")
	}
	if s.IsDisabled(AIOpChat) {
		t.Error("expected chat not disabled")
	}
}

func TestAIPermission_ListAssignments(t *testing.T) {
	s := newTestAIPermissionService(t)

	if err := s.SetAssignment(ModelAssignment{
		Operation:  AIOpChat,
		ProviderID: "p1",
		Model:      "m1",
	}); err != nil {
		t.Fatalf("SetAssignment failed: %v", err)
	}

	list := s.ListAssignments()
	if len(list) != len(allOperations) {
		t.Errorf("expected %d assignments, got %d", len(allOperations), len(list))
	}

	// 验证 chat 操作有正确值
	found := false
	for _, a := range list {
		if a.Operation == AIOpChat {
			found = true
			if a.Model != "m1" {
				t.Errorf("expected chat model m1, got %s", a.Model)
			}
		}
	}
	if !found {
		t.Error("chat assignment not found in list")
	}
}

func TestAIPermission_Persistence(t *testing.T) {
	dir := t.TempDir()
	s1 := NewAIPermissionService(dir)

	a := ModelAssignment{
		Operation:  AIOpChat,
		ProviderID: "p1",
		Model:      "gpt-4",
	}
	if err := s1.SetAssignment(a); err != nil {
		t.Fatalf("SetAssignment failed: %v", err)
	}

	// 验证文件已写入
	data, err := os.ReadFile(filepath.Join(dir, "model_assignments.json"))
	if err != nil {
		t.Fatalf("expected assignments file, got error: %v", err)
	}
	if len(data) == 0 {
		t.Error("assignments file is empty")
	}

	// 创建新服务实例，验证加载
	s2 := NewAIPermissionService(dir)
	res := s2.GetModelFor(AIOpChat)
	if res.Primary.Model != "gpt-4" {
		t.Errorf("expected model gpt-4 after reload, got %s", res.Primary.Model)
	}
}

func TestAIPermission_Persistence_FilePermission0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not supported on Windows")
	}
	dir := t.TempDir()
	s := NewAIPermissionService(dir)

	if err := s.SetAssignment(ModelAssignment{
		Operation: AIOpChat,
		Model:     "test",
	}); err != nil {
		t.Fatalf("SetAssignment failed: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "model_assignments.json"))
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected file permission 0600, got %o", perm)
	}
}

func TestAIPermission_RecordUsage_GetSummary(t *testing.T) {
	s := newTestAIPermissionService(t)

	// 记录 3 次调用
	s.RecordUsage(UsageRecord{
		Operation:  AIOpChat,
		ProviderID: "p1",
		Model:      "gpt-4",
		TokensIn:   100,
		TokensOut:  200,
		Cost:       0.01,
	})
	s.RecordUsage(UsageRecord{
		Operation:  AIOpChat,
		ProviderID: "p1",
		Model:      "gpt-4",
		TokensIn:   50,
		TokensOut:  100,
		Cost:       0.005,
	})
	s.RecordUsage(UsageRecord{
		Operation:  AIOpAgent,
		ProviderID: "p2",
		Model:      "claude-3",
		TokensIn:   500,
		TokensOut:  1000,
		Cost:       0.05,
	})

	summary := s.GetUsageSummary("all")
	if summary.TotalTokensIn != 650 {
		t.Errorf("expected total tokens in 650, got %d", summary.TotalTokensIn)
	}
	if summary.TotalTokensOut != 1300 {
		t.Errorf("expected total tokens out 1300, got %d", summary.TotalTokensOut)
	}
	if summary.TotalCost != 0.065 {
		t.Errorf("expected total cost 0.065, got %f", summary.TotalCost)
	}

	// 按操作统计
	chatUsage := summary.ByOperation[AIOpChat]
	if chatUsage.Count != 2 {
		t.Errorf("expected chat count 2, got %d", chatUsage.Count)
	}
	agentUsage := summary.ByOperation[AIOpAgent]
	if agentUsage.Count != 1 {
		t.Errorf("expected agent count 1, got %d", agentUsage.Count)
	}

	// 按模型统计
	modelKey := "p1/gpt-4"
	mUsage := summary.ByModel[modelKey]
	if mUsage.Count != 2 {
		t.Errorf("expected model p1/gpt-4 count 2, got %d", mUsage.Count)
	}
}

func TestAIPermission_GetUsageSummary_PeriodDay(t *testing.T) {
	s := newTestAIPermissionService(t)

	// 不记录任何用量，day 汇总应为空
	summary := s.GetUsageSummary("day")
	if summary.TotalCost != 0 {
		t.Errorf("expected 0 cost for empty usage, got %f", summary.TotalCost)
	}
}

func TestAIPermission_GetCostSuggestions(t *testing.T) {
	s := newTestAIPermissionService(t)

	// 设置 chat 用 gpt-4
	if err := s.SetAssignment(ModelAssignment{
		Operation:  AIOpChat,
		ProviderID: "p1",
		Model:      "gpt-4",
	}); err != nil {
		t.Fatalf("SetAssignment failed: %v", err)
	}

	// 记录高成本 chat 调用
	for i := 0; i < 10; i++ {
		s.RecordUsage(UsageRecord{
			Operation:  AIOpChat,
			ProviderID: "p1",
			Model:      "gpt-4",
			TokensIn:   1000,
			TokensOut:  2000,
			Cost:       0.1,
		})
	}

	suggestions := s.GetCostSuggestions()
	if len(suggestions) == 0 {
		t.Fatal("expected at least 1 suggestion, got 0")
	}

	// 第一个建议应该是 chat 操作（成本最高）
	if suggestions[0].Operation != AIOpChat {
		t.Errorf("expected suggestion for chat, got %s", suggestions[0].Operation)
	}
	if suggestions[0].CurrentModel != "gpt-4" {
		t.Errorf("expected current model gpt-4, got %s", suggestions[0].CurrentModel)
	}
	if suggestions[0].EstimatedSavings <= 0 {
		t.Errorf("expected positive savings, got %f", suggestions[0].EstimatedSavings)
	}
}

func TestAIPermission_CheckBudget_NoAlert(t *testing.T) {
	s := newTestAIPermissionService(t)

	// 记录少量用量
	s.RecordUsage(UsageRecord{
		Operation:  AIOpChat,
		ProviderID: "p1",
		Model:      "m1",
		Cost:       0.01,
	})

	alert := s.CheckBudget(BudgetAlert{MonthlyBudget: 100.0, ThresholdPct: 80})
	if alert != "" {
		t.Errorf("expected no alert, got %q", alert)
	}
}

func TestAIPermission_CheckBudget_AlertTriggered(t *testing.T) {
	s := newTestAIPermissionService(t)

	// 记录大量用量，超过预算 80%
	s.RecordUsage(UsageRecord{
		Operation:  AIOpChat,
		ProviderID: "p1",
		Model:      "m1",
		Cost:       90.0,
	})

	alert := s.CheckBudget(BudgetAlert{MonthlyBudget: 100.0, ThresholdPct: 80})
	if alert == "" {
		t.Error("expected budget alert, got empty string")
	}
}

func TestAIPermission_CheckBudget_DisabledWhenNoBudget(t *testing.T) {
	s := newTestAIPermissionService(t)

	s.RecordUsage(UsageRecord{
		Operation:  AIOpChat,
		ProviderID: "p1",
		Model:      "m1",
		Cost:       1000.0,
	})

	// 无预算配置 → 无告警
	alert := s.CheckBudget(BudgetAlert{})
	if alert != "" {
		t.Errorf("expected no alert when budget not set, got %q", alert)
	}
}

func TestAIPermission_ResetUsage(t *testing.T) {
	s := newTestAIPermissionService(t)

	s.RecordUsage(UsageRecord{
		Operation:  AIOpChat,
		ProviderID: "p1",
		Model:      "m1",
		Cost:       1.0,
	})

	if err := s.ResetUsage(); err != nil {
		t.Fatalf("ResetUsage failed: %v", err)
	}

	summary := s.GetUsageSummary("all")
	if summary.TotalCost != 0 {
		t.Errorf("expected 0 cost after reset, got %f", summary.TotalCost)
	}
}

func TestAIPermission_AllOperations(t *testing.T) {
	// 验证 allOperations 包含所有 8 个操作
	expected := map[AIOperation]bool{
		AIOpChat:             true,
		AIOpInlineCompletion: true,
		AIOpAgent:            true,
		AIOpReview:           true,
		AIOpCommitMessage:    true,
		AIOpTitleGeneration:  true,
		AIOpPlan:             true,
		AIOpGoal:             true,
	}
	if len(allOperations) != len(expected) {
		t.Errorf("expected %d operations, got %d", len(expected), len(allOperations))
	}
	for _, op := range allOperations {
		if !expected[op] {
			t.Errorf("unexpected operation %q", op)
		}
	}
}

func TestAIPermission_UsagePersistence(t *testing.T) {
	dir := t.TempDir()
	s1 := NewAIPermissionService(dir)

	s1.RecordUsage(UsageRecord{
		Operation:  AIOpChat,
		ProviderID: "p1",
		Model:      "m1",
		TokensIn:   100,
		TokensOut:  200,
		Cost:       0.05,
	})

	// 创建新服务实例，验证用量加载
	s2 := NewAIPermissionService(dir)
	summary := s2.GetUsageSummary("all")
	if summary.TotalCost != 0.05 {
		t.Errorf("expected cost 0.05 after reload, got %f", summary.TotalCost)
	}
}
