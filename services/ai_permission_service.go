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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
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

// legacyUsageSequence prevents legacy records that share an injected or
// same-nanosecond timestamp from colliding on their synthetic UnitID.
var legacyUsageSequence atomic.Uint64

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
	Operation               AIOperation `json:"operation"`
	ProviderID              string      `json:"providerId"` // 关联 AIProviderConfig.ID
	Model                   string      `json:"model"`      // 模型名
	ReasoningEffort         string      `json:"reasoningEffort,omitempty"`
	FallbackReasoningEffort string      `json:"fallbackReasoningEffort,omitempty"`
	Temperature             float64     `json:"temperature,omitempty"`
	MaxTokens               int         `json:"maxTokens,omitempty"`
	FallbackProviderID      string      `json:"fallbackProviderId,omitempty"`
	FallbackModel           string      `json:"fallbackModel,omitempty"`
	Disabled                bool        `json:"disabled,omitempty"` // Step 6: 操作级权限
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
	Timestamp                 time.Time   `json:"timestamp"`
	UnitID                    string      `json:"unitId,omitempty"`
	SessionID                 string      `json:"sessionId,omitempty"`
	UnitKind                  string      `json:"unitKind,omitempty"`
	Operation                 AIOperation `json:"operation"`
	ProviderID                string      `json:"providerId"`
	Model                     string      `json:"model"`
	TokensIn                  int         `json:"tokensIn"`
	TokensOut                 int         `json:"tokensOut"`
	Cost                      float64     `json:"cost"`
	Currency                  string      `json:"currency,omitempty"`
	CostBasis                 string      `json:"costBasis,omitempty"`
	Estimated                 bool        `json:"estimated,omitempty"`
	StartedAt                 time.Time   `json:"startedAt,omitempty"`
	CompletedAt               time.Time   `json:"completedAt,omitempty"`
	Success                   bool        `json:"success"`
	ExternalReceiptID         string      `json:"externalReceiptId,omitempty"`
	ExternalReceiptReversible bool        `json:"externalReceiptReversible,omitempty"`
	ExternalCompensation      string      `json:"externalCompensation,omitempty"`
	Pending                   bool        `json:"pending,omitempty"`
	Error                     string      `json:"error,omitempty"`
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
	usageWriteMu    sync.Mutex
	configDir       string
	stateRoot       *os.Root
	assignments     map[AIOperation]ModelAssignment
	usage           []UsageRecord
	settingsService *SettingsService // 用于校验 ProviderID 存在性（G-SEC-07）
	// receiptIdentityKey binds recovery handles to this config directory. It
	// is never returned through a binding and is deliberately separate from
	// adapter receipt metadata.
	receiptIdentityKey []byte
	receiptIdentityErr error
	// A write that may have reached the append-only ledger but whose durability
	// is unknown poisons trusted recovery until a fresh process reloads it.
	usagePersistencePoison error
	// usageAppendHook is an unexported deterministic fault hook. Production
	// wiring leaves it nil; package tests use it to exercise the exact
	// post-publication poison boundary without weakening filesystem semantics.
	usageAppendHook func(stage string) error
}

// NewAIPermissionService 创建服务。configDir 用于持久化。
func NewAIPermissionService(configDir string) *AIPermissionService {
	return newAIPermissionService(configDir, nil)
}

// newAIPermissionService binds trusted non-renderer consumers to a retained
// state capability. Every persistence operation uses the root when supplied.
func newAIPermissionService(configDir string, stateRoot *os.Root) *AIPermissionService {
	s := &AIPermissionService{
		configDir:   configDir,
		stateRoot:   stateRoot,
		assignments: make(map[AIOperation]ModelAssignment),
		usage:       []UsageRecord{},
	}
	// 初始化默认分配（全部使用默认 provider）。
	for _, op := range allOperations {
		s.assignments[op] = ModelAssignment{Operation: op}
	}
	s.loadAssignments()
	s.loadUsage()
	if strings.TrimSpace(configDir) != "" {
		if stateRoot != nil {
			s.receiptIdentityKey, s.receiptIdentityErr = loadOrCreateAgentStateKey(
				stateRoot, "agent_external_receipt_identity.key", s.requiresStableReceiptIdentity(),
			)
		} else {
			s.receiptIdentityKey, s.receiptIdentityErr = loadStableReceiptIdentityKey(
				filepath.Join(configDir, "agent_external_receipt_identity.key"),
				s.requiresStableReceiptIdentity(),
			)
		}
		if s.receiptIdentityErr != nil {
			s.usagePersistencePoison = errors.Join(s.usagePersistencePoison, ErrUsagePersistencePoisoned, s.receiptIdentityErr)
		}
	}
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
	var data []byte
	var err error
	if s.stateRoot != nil {
		data, err = readAgentStateFile(s.stateRoot, "model_assignments.json", 16<<20)
	} else {
		data, err = os.ReadFile(s.assignmentsPath())
	}
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
	if s.stateRoot != nil {
		return atomicWriteAgentStateFile(s.stateRoot, "model_assignments.json", data, 0o600)
	}
	return atomicWriteFile(s.assignmentsPath(), data, 0600)
}

// loadUsage 从磁盘加载用量（best-effort）。
func (s *AIPermissionService) loadUsage() {
	var data []byte
	var err error
	if s.stateRoot != nil {
		data, err = readAgentStateFile(s.stateRoot, "usage_log.jsonl", -1)
	} else {
		data, err = os.ReadFile(s.usagePath())
	}
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.usagePersistencePoison = errors.Join(ErrUsagePersistencePoisoned, err)
		return
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec UsageRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// A malformed tail is not safe to silently skip: it may hide a
			// pending external side effect. Recovery callers must get a poison
			// result and require an explicit fresh repair/reload.
			s.usagePersistencePoison = errors.Join(ErrUsagePersistencePoisoned, err)
			return
		}
		// No legacy release produced durable pending receipts. Never synthesize
		// authority-bearing identity or timestamps for a raw pending row: missing
		// fields indicate a truncated, forged, or incompatible ledger entry.
		if rec.Pending && !hasCanonicalPendingPersistenceIdentity(rec) {
			s.usagePersistencePoison = errors.Join(ErrUsagePersistencePoisoned, ErrUsageReceiptState)
			return
		}
		rec = normalizeLegacyUsage(rec)
		if _, _, transitionErr := s.usageTransitionLocked(rec); transitionErr != nil {
			// A conflicting tail could otherwise make a terminal receipt appear
			// pending (or vice versa) after restart.
			s.usagePersistencePoison = errors.Join(ErrUsagePersistencePoisoned, transitionErr)
			return
		}
		s.upsertUsageLocked(rec)
	}
}

func hasCanonicalPendingPersistenceIdentity(rec UsageRecord) bool {
	return strings.TrimSpace(rec.UnitID) != "" && strings.TrimSpace(rec.SessionID) != "" &&
		rec.UnitKind != "" && rec.Operation != "" &&
		rec.CostBasis != "" && !rec.Timestamp.IsZero() && !rec.StartedAt.IsZero() &&
		!rec.CompletedAt.IsZero() && rec.CompletedAt.Equal(rec.StartedAt) &&
		rec.Timestamp.Equal(rec.CompletedAt)
}

func (s *AIPermissionService) requiresStableReceiptIdentity() bool {
	if s.usagePersistencePoison != nil {
		return true
	}
	for _, rec := range s.usage {
		if rec.ExternalReceiptID != "" {
			return true
		}
	}
	return false
}

func usageIdentityEqual(a, b UsageRecord) bool {
	return a.UnitID == b.UnitID &&
		a.SessionID == b.SessionID &&
		a.UnitKind == b.UnitKind &&
		a.Operation == b.Operation &&
		a.ProviderID == b.ProviderID &&
		a.Model == b.Model &&
		a.StartedAt.Equal(b.StartedAt) &&
		a.ExternalReceiptID == b.ExternalReceiptID &&
		a.ExternalReceiptReversible == b.ExternalReceiptReversible
}

func usageTerminalEqual(a, b UsageRecord) bool {
	return usageIdentityEqual(a, b) &&
		a.TokensIn == b.TokensIn &&
		a.TokensOut == b.TokensOut &&
		a.Cost == b.Cost &&
		a.Currency == b.Currency &&
		a.CostBasis == b.CostBasis &&
		a.Estimated == b.Estimated &&
		a.Success == b.Success &&
		a.ExternalCompensation == b.ExternalCompensation &&
		a.Error == b.Error
}

// usageTransitionLocked validates the monotonic state machine for one UnitID.
// It returns the existing index and whether the incoming row is an idempotent
// no-op. Callers hold s.mu; trusted writers serialize the surrounding disk
// append with usageWriteMu.
func (s *AIPermissionService) usageTransitionLocked(rec UsageRecord) (int, bool, error) {
	for index := range s.usage {
		if s.usage[index].UnitID == rec.UnitID {
			existing := s.usage[index]
			if existing.Pending {
				if rec.Pending {
					if usageIdentityEqual(existing, rec) {
						return index, true, nil
					}
					return index, false, fmt.Errorf("pending usage receipt identity changed for %q: %w", rec.UnitID, ErrUsageReceiptState)
				}
				if !usageIdentityEqual(existing, rec) {
					return index, false, fmt.Errorf("terminal usage receipt identity changed for %q: %w", rec.UnitID, ErrUsageReceiptState)
				}
				return index, false, nil
			}
			if rec.Pending {
				return index, false, fmt.Errorf("terminal usage receipt %q cannot return to pending: %w", rec.UnitID, ErrUsageReceiptState)
			}
			if usageTerminalEqual(existing, rec) {
				return index, true, nil
			}
			return index, false, fmt.Errorf("divergent terminal usage receipt %q: %w", rec.UnitID, ErrUsageReceiptState)
		}
	}
	return -1, false, nil
}

// upsertUsageLocked folds receipt updates by UnitID. The on-disk ledger is
// append-only for crash safety, while in-memory summaries expose one logical
// row per execution unit rather than counting the pending and terminal rows
// twice. Conflicting rows are ignored during reload so a stale/corrupt tail
// cannot regress a terminal state.
func (s *AIPermissionService) upsertUsageLocked(rec UsageRecord) {
	index, noop, err := s.usageTransitionLocked(rec)
	if err != nil || noop {
		return
	}
	if index >= 0 {
		s.usage[index] = rec
		return
	}
	s.usage = append(s.usage, rec)
}

// appendUsage appends one usage record durably enough for the local ledger.
// Errors are returned to the trusted caller; a missing ledger must never be
// reported as a successful metering operation.
type usagePersistenceCommitState uint8

const (
	usagePersistenceNotPublished usagePersistenceCommitState = iota
	usagePersistenceDurable
	usagePersistencePublishedUnknown
)

// appendUsage reports the publication boundary instead of flattening all I/O
// errors into one class. Once bytes may have reached the append-only file, a
// retry in the same process could create a divergent receipt; callers poison
// trusted recovery until a fresh reload verifies the ledger.
func (s *AIPermissionService) appendUsage(rec UsageRecord) (usagePersistenceCommitState, error) {
	data, err := json.Marshal(rec)
	if err != nil {
		return usagePersistenceNotPublished, fmt.Errorf("marshal usage record: %w", err)
	}
	data = append(data, '\n')
	var f *os.File
	if s.stateRoot != nil {
		f, err = openAgentStateRegularFile(s.stateRoot, "usage_log.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	} else {
		if err := os.MkdirAll(filepath.Dir(s.usagePath()), 0700); err != nil {
			return usagePersistenceNotPublished, fmt.Errorf("create usage directory: %w", err)
		}
		f, err = os.OpenFile(s.usagePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	}
	if err != nil {
		return usagePersistenceNotPublished, fmt.Errorf("open usage ledger: %w", err)
	}
	var openedIdentity os.FileInfo
	if s.stateRoot != nil {
		openedIdentity, err = f.Stat()
		if err != nil || openedIdentity == nil || !openedIdentity.Mode().IsRegular() {
			_ = f.Close()
			return usagePersistenceNotPublished, fmt.Errorf("identify usage ledger after open: %w", errors.Join(ErrUsagePersistence, err))
		}
	}
	n, writeErr := f.Write(data)
	if writeErr != nil {
		_ = f.Close()
		state := usagePersistenceNotPublished
		if n > 0 {
			state = usagePersistencePublishedUnknown
		}
		return state, fmt.Errorf("write usage ledger: %w", writeErr)
	}
	if n != len(data) {
		_ = f.Close()
		return usagePersistencePublishedUnknown, fmt.Errorf("short usage ledger write: wrote %d of %d bytes", n, len(data))
	}
	if s.usageAppendHook != nil {
		if err := s.usageAppendHook("after-write"); err != nil {
			_ = f.Close()
			return usagePersistencePublishedUnknown, fmt.Errorf("usage ledger post-write hook: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return usagePersistencePublishedUnknown, fmt.Errorf("sync usage ledger: %w", err)
	}
	if err := f.Close(); err != nil {
		return usagePersistencePublishedUnknown, fmt.Errorf("close usage ledger: %w", err)
	}
	if s.stateRoot != nil {
		if s.usageAppendHook != nil {
			if err := s.usageAppendHook("after-close"); err != nil {
				return usagePersistencePublishedUnknown, fmt.Errorf("usage ledger post-close hook: %w", err)
			}
		}
		named, lstatErr := s.stateRoot.Lstat("usage_log.jsonl")
		if lstatErr != nil || named == nil || !named.Mode().IsRegular() || !os.SameFile(openedIdentity, named) {
			return usagePersistencePublishedUnknown, fmt.Errorf("usage ledger identity changed after close: %w", errors.Join(ErrUsagePersistenceIndeterminate, lstatErr))
		}
		if s.usageAppendHook != nil {
			if err := s.usageAppendHook("before-root-sync"); err != nil {
				return usagePersistencePublishedUnknown, fmt.Errorf("usage ledger root sync hook: %w", err)
			}
		}
		if err := syncAgentStateRoot(s.stateRoot); err != nil {
			return usagePersistencePublishedUnknown, fmt.Errorf("sync usage ledger directory: %w", errors.Join(ErrUsagePersistenceIndeterminate, err))
		}
		if s.usageAppendHook != nil {
			if err := s.usageAppendHook("after-root-sync"); err != nil {
				return usagePersistencePublishedUnknown, fmt.Errorf("usage ledger post-sync hook: %w", err)
			}
		}
		named, lstatErr = s.stateRoot.Lstat("usage_log.jsonl")
		if lstatErr != nil || named == nil || !named.Mode().IsRegular() || !os.SameFile(openedIdentity, named) {
			return usagePersistencePublishedUnknown, fmt.Errorf("usage ledger identity changed after directory sync: %w", errors.Join(ErrUsagePersistenceIndeterminate, lstatErr))
		}
	}
	return usagePersistenceDurable, nil
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
			Operation:       op,
			ProviderID:      primary.FallbackProviderID,
			Model:           primary.FallbackModel,
			ReasoningEffort: primary.FallbackReasoningEffort,
			Temperature:     primary.Temperature,
			MaxTokens:       primary.MaxTokens,
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
	var err error
	if a.ReasoningEffort, err = normalizeReasoningEffort(a.ReasoningEffort); err != nil {
		return err
	}
	if a.FallbackReasoningEffort, err = normalizeReasoningEffort(a.FallbackReasoningEffort); err != nil {
		return err
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

// RecordUsage is intentionally deny-only at the renderer boundary. Usage is
// an authority-bearing ledger and may only be written by the trusted agent
// lifecycle sink below.
func (s *AIPermissionService) RecordUsage(_ UsageRecord) error {
	return fmt.Errorf("renderer usage recording is not permitted: %w", ErrNotAllowed)
}

func normalizeLegacyUsage(rec UsageRecord) UsageRecord {
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = rec.Timestamp
	}
	if rec.CompletedAt.IsZero() {
		rec.CompletedAt = rec.Timestamp
	}
	if rec.UnitID == "" {
		rec.UnitID = fmt.Sprintf("legacy-%d-%d", rec.Timestamp.UnixNano(), legacyUsageSequence.Add(1))
	}
	if rec.SessionID == "" {
		rec.SessionID = "legacy:" + string(rec.Operation)
	}
	if rec.UnitKind == "" {
		rec.UnitKind = string(agentcore.UsageUnitAI)
	}
	if rec.CostBasis == "" {
		if rec.Cost == 0 && rec.TokensIn == 0 && rec.TokensOut == 0 {
			rec.CostBasis = string(agentcore.CostNotApplicable)
			rec.Estimated = false
		} else {
			// The legacy API never carried provider billing provenance. Treating
			// those numbers as estimates is the only honest migration.
			rec.CostBasis = string(agentcore.CostEstimated)
			rec.Estimated = true
		}
	}
	if !rec.Pending && !rec.Success && rec.Error == "" && rec.CostBasis == string(agentcore.CostNotApplicable) {
		// Legacy callers predate success tracking and only recorded completed
		// calls. Preserve that meaning when loading old JSONL rows.
		rec.Success = true
	}
	return rec
}

func agentUsageRecord(record agentcore.UsageRecord) UsageRecord {
	rec := UsageRecord{
		Timestamp: record.CompletedAt, UnitID: record.UnitID, SessionID: record.SessionID,
		UnitKind: string(record.UnitKind), Operation: AIOperation(record.Operation),
		ProviderID: record.ProviderID, Model: record.Model,
		TokensIn: record.TokensIn, TokensOut: record.TokensOut,
		Cost: record.Cost, Currency: record.Currency,
		CostBasis: string(record.CostBasis), Estimated: record.Estimated,
		StartedAt: record.StartedAt, CompletedAt: record.CompletedAt,
		Success:                   record.Success,
		ExternalReceiptID:         record.ExternalReceiptID,
		ExternalReceiptReversible: record.ExternalReceiptReversible,
		ExternalCompensation:      record.ExternalCompensation,
		Pending:                   record.Pending,
	}
	if record.Error != "" {
		// Usage logs are operational metadata. Provider/tool error bodies may
		// contain paths or prompt fragments, so persist only a bounded class.
		rec.Error = "execution failed"
	}
	return rec
}

func (s *AIPermissionService) recordAgentUsage(record agentcore.UsageRecord) error {
	return s.recordUsageTrusted(agentUsageRecord(record))
}

// beginAgentUsage persists the receipt before a tool handler is allowed to
// perform a side effect. The same UnitID is returned for terminal upsert.
func (s *AIPermissionService) beginAgentUsage(record agentcore.UsageRecord) (agentcore.UsageReceipt, error) {
	record.Pending = true
	record.Success = false
	record.CompletedAt = record.StartedAt
	rec := agentUsageRecord(record)
	if rec.UnitID == "" {
		return agentcore.UsageReceipt{}, fmt.Errorf("usage unit ID is required: %w", ErrInvalidInput)
	}
	if rec.ExternalReceiptID != "" && (s.receiptIdentityErr != nil || len(s.receiptIdentityKey) == 0) {
		return agentcore.UsageReceipt{}, fmt.Errorf("external receipt recovery identity is unavailable: %w", ErrNotAllowed)
	}
	if err := s.recordUsageTrusted(rec); err != nil {
		return agentcore.UsageReceipt{}, err
	}
	return agentcore.UsageReceipt{UnitID: rec.UnitID}, nil
}

func (s *AIPermissionService) completeAgentUsage(receipt agentcore.UsageReceipt, record agentcore.UsageRecord) error {
	if receipt.UnitID == "" {
		return fmt.Errorf("usage receipt is empty: %w", ErrInvalidInput)
	}
	record.UnitID = receipt.UnitID
	record.Pending = false
	return s.recordUsageTrustedMode(agentUsageRecord(record), true)
}

// recordUsageTrusted is the only in-process writer for the usage ledger. It is
// package-private and reached from the backend lifecycle meter, never from a
// Wails binding. Persist first so a failed write cannot appear in summaries.
func (s *AIPermissionService) recordUsageTrusted(rec UsageRecord) error {
	return s.recordUsageTrustedMode(rec, false)
}

func (s *AIPermissionService) recordUsageTrustedMode(rec UsageRecord, requireExisting bool) error {
	rec = normalizeLegacyUsage(rec)
	s.usageWriteMu.Lock()
	defer s.usageWriteMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usagePersistencePoison != nil {
		return ErrUsagePersistencePoisoned
	}
	index, noop, transitionErr := s.usageTransitionLocked(rec)
	if transitionErr != nil {
		return transitionErr
	}
	if requireExisting && index < 0 {
		return fmt.Errorf("usage receipt %q has no durable begin row: %w", rec.UnitID, ErrUsageReceiptState)
	}
	if noop {
		return nil
	}
	commitState, appendErr := s.appendUsage(rec)
	if appendErr != nil {
		if commitState == usagePersistencePublishedUnknown {
			s.usagePersistencePoison = errors.Join(ErrUsagePersistenceIndeterminate, appendErr)
			return ErrUsagePersistenceIndeterminate
		}
		if errors.Is(appendErr, ErrUsagePersistencePoisoned) {
			s.usagePersistencePoison = errors.Join(ErrUsagePersistencePoisoned, appendErr)
			return ErrUsagePersistencePoisoned
		}
		// Do not update the in-memory projection when persistence failed. A
		// caller may retry the same UnitID; the monotonic transition check below
		// remains authoritative for the logical state.
		return ErrUsagePersistence
	}
	if index >= 0 {
		s.usage[index] = rec
	} else {
		s.usage = append(s.usage, rec)
	}
	return nil
}

func (s *AIPermissionService) usageRecordsSnapshot() []UsageRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]UsageRecord(nil), s.usage...)
}

// pendingWorkflowUsageAttempt returns the single durable workflow attempt for
// a session. Any other pending row makes the session ambiguous: completing a
// workflow while a tool or a second attempt is still in flight would publish a
// false terminal state. Callers receive only bounded sentinel errors; ledger
// paths and unit IDs remain backend-only.
func (s *AIPermissionService) pendingWorkflowUsageAttempt(sessionID string) (UsageRecord, bool, error) {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return UsageRecord{}, false, fmt.Errorf("workflow usage lookup is unavailable: %w", ErrNotAllowed)
	}
	s.usageWriteMu.Lock()
	defer s.usageWriteMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usagePersistencePoison != nil {
		return UsageRecord{}, false, ErrUsagePersistencePoisoned
	}
	var candidate UsageRecord
	found := false
	for _, record := range s.usage {
		if record.SessionID != sessionID || !record.Pending {
			continue
		}
		if record.UnitKind != string(agentcore.UsageUnitWorkflow) ||
			record.Operation != AIOperation("workflow.attempt") ||
			!validPendingWorkflowUsageAttempt(record) || found {
			return UsageRecord{}, false, fmt.Errorf("workflow usage ledger is ambiguous: %w", ErrUsageReceiptState)
		}
		candidate = record
		found = true
	}
	return candidate, found, nil
}

func validPendingWorkflowUsageAttempt(record UsageRecord) bool {
	return strings.TrimSpace(record.UnitID) != "" &&
		strings.TrimSpace(record.SessionID) != "" &&
		record.UnitKind == string(agentcore.UsageUnitWorkflow) &&
		record.Operation == AIOperation("workflow.attempt") &&
		record.CostBasis == string(agentcore.CostNotApplicable) &&
		record.TokensIn == 0 && record.TokensOut == 0 && record.Cost == 0 &&
		record.Currency == "" && !record.Estimated && !record.Success &&
		record.ProviderID == "" && record.Model == "" && record.Error == "" &&
		record.ExternalReceiptID == "" && !record.ExternalReceiptReversible &&
		record.ExternalCompensation == "" &&
		!record.StartedAt.IsZero() && record.CompletedAt.Equal(record.StartedAt)
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
// ResetUsage is deny-only at the renderer boundary. Clearing an authority
// ledger requires trusted maintenance code and cannot be requested by UI
// state.
func (s *AIPermissionService) ResetUsage() error {
	return fmt.Errorf("renderer usage reset is not permitted: %w", ErrNotAllowed)
}

func (s *AIPermissionService) resetUsageTrusted() error {
	s.usageWriteMu.Lock()
	defer s.usageWriteMu.Unlock()

	var err error
	if s.stateRoot != nil {
		err = removeAgentStateFileIfIdentity(s.stateRoot, "usage_log.jsonl", nil)
	} else {
		err = os.Remove(s.usagePath())
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.usage = nil
	s.mu.Unlock()
	return nil
}
