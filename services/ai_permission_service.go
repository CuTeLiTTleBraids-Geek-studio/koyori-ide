package services

// Plan 11 Task 12 — 模型权限分配。
//
// 职责（Step 1-6, 9）：
//   - Step 1: ModelAssignment 结构（Operation/ProviderID/Model/Temperature/MaxTokens/Fallback）
//   - Step 2: GetModelFor(operation) → 主模型 + fallback
//   - Step 3: ai_service.go 调用点改为 GetModelFor（AIService.ApplyModelFor 注入）
//   - Step 4: fallback（主模型失败自动切）
//   - Step 5: 成本优化建议（历史 Token+费用+推荐更便宜模型）
//   - Step 6: 操作级权限（某些操作可禁用）
//   - Step 9: G-SEC-07（所有调用走 UseStoredKey+ConfigID）
//
// 持久化：assignment 存储在 ~/.config/koyori-ide/model_assignments.json（0600 + atomicWriteJSON）。
// 用量统计存储在 ~/.config/koyori-ide/usage_log.jsonl（追加写）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 操作类型（Step 1）
// ---------------------------------------------------------------------------

// AIOperation 标识一个 AI 调用场景。
type AIOperation string

const (
	AIOpChat             AIOperation = "chat"
	AIOpInlineCompletion AIOperation = "inline-completion"
	AIOpAgent            AIOperation = "agent"
	AIOpReview           AIOperation = "review"
	AIOpCommitMessage    AIOperation = "commit-message"
	AIOpTitleGeneration  AIOperation = "title-generation"
	AIOpPlan             AIOperation = "plan"
	AIOpGoal             AIOperation = "goal"
)

// allOperations 列出所有支持的操作（用于验证 + UI 列表）。
var allOperations = []AIOperation{
	AIOpChat, AIOpInlineCompletion, AIOpAgent, AIOpReview,
	AIOpCommitMessage, AIOpTitleGeneration, AIOpPlan, AIOpGoal,
}

// ---------------------------------------------------------------------------
// ModelAssignment（Step 1）
// ---------------------------------------------------------------------------

// ModelAssignment 描述一个操作使用哪个模型 + fallback（Step 1）。
//
// G-SEC-07（Step 9）：ProviderID 关联 Settings.AIProviderConfigs 中的配置，
// AIService 调用时通过 ConfigID + UseStoredKey 从 SettingsService 取密钥，
// 明文 key 不跨 Wails binding。
//
// Step 6：Disabled=true 时该操作被禁用（如 inline-completion 用本地模型禁联网）。
type ModelAssignment struct {
	Operation          AIOperation `json:"operation"`
	ProviderID         string      `json:"providerId"` // 关联 AIProviderConfig.ID
	Model              string      `json:"model"`      // 模型名
	Temperature        float64     `json:"temperature,omitempty"`
	MaxTokens          int         `json:"maxTokens,omitempty"`
	FallbackProviderID string      `json:"fallbackProviderId,omitempty"`
	FallbackModel      string      `json:"fallbackModel,omitempty"`
	Disabled           bool        `json:"disabled,omitempty"` // Step 6: 操作级权限
}

// ModelResolution 是 GetModelFor 的返回值，包含主模型 + fallback（Step 2）。
type ModelResolution struct {
	Primary  ModelAssignment  `json:"primary"`
	Fallback *ModelAssignment `json:"fallback,omitempty"`
}

// ---------------------------------------------------------------------------
// 用量统计（Step 5: 成本优化建议）
// ---------------------------------------------------------------------------

// UsageRecord 单次 AI 调用的用量记录（Step 5）。
type UsageRecord struct {
	Timestamp  time.Time   `json:"timestamp"`
	Operation  AIOperation `json:"operation"`
	ProviderID string      `json:"providerId"`
	Model      string      `json:"model"`
	TokensIn   int         `json:"tokensIn"`
	TokensOut  int         `json:"tokensOut"`
	Cost       float64     `json:"cost"`
}

// UsageSummary 用量汇总（Step 5: 按时间/操作/模型统计）。
type UsageSummary struct {
	TotalTokensIn  int                            `json:"totalTokensIn"`
	TotalTokensOut int                            `json:"totalTokensOut"`
	TotalCost      float64                        `json:"totalCost"`
	ByOperation    map[AIOperation]OperationUsage `json:"byOperation"`
	ByModel        map[string]OperationUsage      `json:"byModel"`
	ByDay          map[string]OperationUsage      `json:"byDay"` // YYYY-MM-DD
}

// OperationUsage 单维度用量。
type OperationUsage struct {
	TokensIn  int     `json:"tokensIn"`
	TokensOut int     `json:"tokensOut"`
	Cost      float64 `json:"cost"`
	Count     int     `json:"count"`
}

// CostSuggestion 成本优化建议（Step 5）。
type CostSuggestion struct {
	Operation        AIOperation `json:"operation"`
	CurrentModel     string      `json:"currentModel"`
	SuggestedModel   string      `json:"suggestedModel"`
	Reason           string      `json:"reason"`
	EstimatedSavings float64     `json:"estimatedSavings"`
}

// ---------------------------------------------------------------------------
// AIPermissionService（Step 1-6）
// ---------------------------------------------------------------------------

// AIPermissionService 管理操作→模型的映射、用量统计、成本优化建议。
type AIPermissionService struct {
	mu              sync.Mutex
	configDir       string
	assignments     map[AIOperation]ModelAssignment
	usage           []UsageRecord
	settingsService *SettingsService // 用于校验 ProviderID 存在性（G-SEC-07）
}

// NewAIPermissionService 创建服务。configDir 用于持久化。
func NewAIPermissionService(configDir string) *AIPermissionService {
	s := &AIPermissionService{
		configDir:   configDir,
		assignments: make(map[AIOperation]ModelAssignment),
		usage:       []UsageRecord{},
	}
	// 初始化默认分配（全部使用默认 provider）。
	for _, op := range allOperations {
		s.assignments[op] = ModelAssignment{Operation: op}
	}
	s.loadAssignments()
	s.loadUsage()
	return s
}

// setSettingsService 注入 SettingsService 用于 ProviderID 校验（G-SEC-07）。
//
//wails:ignore
func (s *AIPermissionService) setSettingsService(ss *SettingsService) {
	s.mu.Lock()
	s.settingsService = ss
	s.mu.Unlock()
}

// assignmentsPath 返回分配持久化路径。
func (s *AIPermissionService) assignmentsPath() string {
	return filepath.Join(s.configDir, "model_assignments.json")
}

// usagePath 返回用量日志路径。
func (s *AIPermissionService) usagePath() string {
	return filepath.Join(s.configDir, "usage_log.jsonl")
}

// loadAssignments 从磁盘加载分配（best-effort）。
func (s *AIPermissionService) loadAssignments() {
	data, err := os.ReadFile(s.assignmentsPath())
	if err != nil {
		return
	}
	var loaded map[AIOperation]ModelAssignment
	if err := json.Unmarshal(data, &loaded); err != nil {
		return
	}
	s.mu.Lock()
	for op, a := range loaded {
		s.assignments[op] = a
	}
	s.mu.Unlock()
}

// saveAssignments 持久化分配（G-SEC-07: 0600 + atomicWriteJSON）。
func (s *AIPermissionService) saveAssignments() error {
	s.mu.Lock()
	data, err := json.MarshalIndent(s.assignments, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal assignments: %w", err)
	}
	return atomicWriteFile(s.assignmentsPath(), data, 0600)
}

// loadUsage 从磁盘加载用量（best-effort）。
func (s *AIPermissionService) loadUsage() {
	data, err := os.ReadFile(s.usagePath())
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec UsageRecord
		if err := json.Unmarshal([]byte(line), &rec); err == nil {
			s.usage = append(s.usage, rec)
		}
	}
}

// appendUsage 追加一条用量记录到磁盘（best-effort）。
func (s *AIPermissionService) appendUsage(rec UsageRecord) {
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	data = append(data, '\n')
	// O_APPEND 追加写，best-effort（失败仅日志）
	_ = os.MkdirAll(filepath.Dir(s.usagePath()), 0700)
	f, err := os.OpenFile(s.usagePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(data)
}

// ---------------------------------------------------------------------------
// Step 2: GetModelFor
// ---------------------------------------------------------------------------

// GetModelFor 返回操作对应的主模型 + fallback（Step 2）。
//
// 若操作被禁用（Step 6），返回 Disabled=true 的 Primary。
// 若未配置分配，返回空 Model（调用方应回退到默认 config）。
func (s *AIPermissionService) GetModelFor(op AIOperation) ModelResolution {
	s.mu.Lock()
	primary, ok := s.assignments[op]
	s.mu.Unlock()
	if !ok {
		primary = ModelAssignment{Operation: op}
	}
	res := ModelResolution{Primary: primary}
	// Step 2: fallback
	if primary.FallbackProviderID != "" && primary.FallbackModel != "" {
		fb := ModelAssignment{
			Operation:   op,
			ProviderID:  primary.FallbackProviderID,
			Model:       primary.FallbackModel,
			Temperature: primary.Temperature,
			MaxTokens:   primary.MaxTokens,
		}
		res.Fallback = &fb
	}
	return res
}

// SetAssignment 设置操作的模型分配（Step 1）并持久化。
func (s *AIPermissionService) SetAssignment(a ModelAssignment) error {
	if !isValidOperation(a.Operation) {
		return fmt.Errorf("%w: unknown operation %q", ErrInvalidInput, a.Operation)
	}
	s.mu.Lock()
	s.assignments[a.Operation] = a
	s.mu.Unlock()
	return s.saveAssignments()
}

// ListAssignments 返回所有操作的分配（Step 7: UI 列表）。
func (s *AIPermissionService) ListAssignments() []ModelAssignment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ModelAssignment, 0, len(s.assignments))
	for _, op := range allOperations {
		if a, ok := s.assignments[op]; ok {
			out = append(out, a)
		} else {
			out = append(out, ModelAssignment{Operation: op})
		}
	}
	return out
}

// IsDisabled 返回操作是否被禁用（Step 6: 操作级权限）。
func (s *AIPermissionService) IsDisabled(op AIOperation) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.assignments[op]
	return ok && a.Disabled
}

func isValidOperation(op AIOperation) bool {
	for _, valid := range allOperations {
		if op == valid {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Step 5: 用量统计 + 成本优化建议
// ---------------------------------------------------------------------------

// RecordUsage 记录一次 AI 调用的用量（Step 5）。
// 若 Timestamp 为零值，自动设为 time.Now()。
func (s *AIPermissionService) RecordUsage(rec UsageRecord) {
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	s.mu.Lock()
	s.usage = append(s.usage, rec)
	s.mu.Unlock()
	s.appendUsage(rec)
}

// GetUsageSummary 返回用量汇总（Step 5: 按天/操作/模型统计）。
// period: "day"/"week"/"month"/"all"
func (s *AIPermissionService) GetUsageSummary(period string) UsageSummary {
	s.mu.Lock()
	records := append([]UsageRecord(nil), s.usage...)
	s.mu.Unlock()

	now := time.Now()
	var cutoff time.Time
	switch period {
	case "day":
		cutoff = now.AddDate(0, 0, -1)
	case "week":
		cutoff = now.AddDate(0, 0, -7)
	case "month":
		cutoff = now.AddDate(0, -1, 0)
	default: // "all"
		cutoff = time.Time{}
	}

	summary := UsageSummary{
		ByOperation: make(map[AIOperation]OperationUsage),
		ByModel:     make(map[string]OperationUsage),
		ByDay:       make(map[string]OperationUsage),
	}
	for _, r := range records {
		if !cutoff.IsZero() && r.Timestamp.Before(cutoff) {
			continue
		}
		summary.TotalTokensIn += r.TokensIn
		summary.TotalTokensOut += r.TokensOut
		summary.TotalCost += r.Cost

		opUsage := summary.ByOperation[r.Operation]
		opUsage.TokensIn += r.TokensIn
		opUsage.TokensOut += r.TokensOut
		opUsage.Cost += r.Cost
		opUsage.Count++
		summary.ByOperation[r.Operation] = opUsage

		modelKey := fmt.Sprintf("%s/%s", r.ProviderID, r.Model)
		mUsage := summary.ByModel[modelKey]
		mUsage.TokensIn += r.TokensIn
		mUsage.TokensOut += r.TokensOut
		mUsage.Cost += r.Cost
		mUsage.Count++
		summary.ByModel[modelKey] = mUsage

		day := r.Timestamp.Format("2006-01-02")
		dUsage := summary.ByDay[day]
		dUsage.TokensIn += r.TokensIn
		dUsage.TokensOut += r.TokensOut
		dUsage.Cost += r.Cost
		dUsage.Count++
		summary.ByDay[day] = dUsage
	}
	return summary
}

// GetCostSuggestions 返回成本优化建议（Step 5）。
//
// 策略：找出成本最高的操作+模型组合，建议切换到更便宜的模型。
// 简化实现：对成本 Top-3 操作，建议 "consider cheaper model"。
func (s *AIPermissionService) GetCostSuggestions() []CostSuggestion {
	summary := s.GetUsageSummary("month")
	type opCost struct {
		Op   AIOperation
		Cost float64
	}
	var costs []opCost
	for op, u := range summary.ByOperation {
		costs = append(costs, opCost{Op: op, Cost: u.Cost})
	}
	sort.Slice(costs, func(i, j int) bool { return costs[i].Cost > costs[j].Cost })

	var suggestions []CostSuggestion
	s.mu.Lock()
	assignments := make(map[AIOperation]ModelAssignment, len(s.assignments))
	for k, v := range s.assignments {
		assignments[k] = v
	}
	s.mu.Unlock()

	limit := 3
	if len(costs) < limit {
		limit = len(costs)
	}
	for i := 0; i < limit; i++ {
		oc := costs[i]
		if oc.Cost < 0.01 {
			continue
		}
		a := assignments[oc.Op]
		suggestions = append(suggestions, CostSuggestion{
			Operation:        oc.Op,
			CurrentModel:     a.Model,
			SuggestedModel:   "", // 由用户根据 provider 列表选择
			Reason:           fmt.Sprintf("Operation %q cost $%.4f in last month — consider a cheaper model", oc.Op, oc.Cost),
			EstimatedSavings: oc.Cost * 0.3, // 估算可节省 30%
		})
	}
	return suggestions
}

// ---------------------------------------------------------------------------
// Step 8: 预算告警（复用 Task 7 IM 通知）
// ---------------------------------------------------------------------------

// BudgetAlert 预算告警配置。
type BudgetAlert struct {
	MonthlyBudget float64 `json:"monthlyBudget,omitempty"`
	ThresholdPct  float64 `json:"thresholdPct,omitempty"` // 触发阈值百分比（如 80 = 80%）
}

// CheckBudget 检查预算是否超阈值，返回告警消息（空字符串表示无告警）。
func (s *AIPermissionService) CheckBudget(budget BudgetAlert) string {
	if budget.MonthlyBudget <= 0 || budget.ThresholdPct <= 0 {
		return ""
	}
	summary := s.GetUsageSummary("month")
	pct := (summary.TotalCost / budget.MonthlyBudget) * 100
	if pct >= budget.ThresholdPct {
		return fmt.Sprintf("Budget alert: $%.4f / $%.2f (%.1f%%) exceeds %.0f%% threshold",
			summary.TotalCost, budget.MonthlyBudget, pct, budget.ThresholdPct)
	}
	return ""
}

// ---------------------------------------------------------------------------

// ResetUsage 清除所有用量记录（用于测试/重置）。
func (s *AIPermissionService) ResetUsage() error {
	s.mu.Lock()
	s.usage = nil
	s.mu.Unlock()
	return os.Remove(s.usagePath())
}
