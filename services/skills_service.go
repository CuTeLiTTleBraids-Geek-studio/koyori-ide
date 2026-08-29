package services

// Plan 11 Task 5 — Skills（技能系统）。
//
// 技能是可复用的「系统提示 + 工具白名单 + 触发器」组合。用户消息匹配
// 触发器时激活，注入 SystemPrompt 并限制 agent 仅可用 AllowedTools /
// AllowedMCP。多 Skill 可叠加，按 Priority 合并 SystemPrompt。
//
// 存储：
//   - 项目级：`.koyori-ide/skills/*.yaml`（workspace root 下，随项目共享）
//   - 用户级：`<configDir>/koyori-ide/skills/*.yaml`（跨项目）
//
// 安全（G-SEC-02 / G-SEC-03）：
//   - AllowedTools 经 agent CheckCommand 审批，禁止超白名单调用。
//   - 项目级 Skill 首次激活需用户确认（IsProjectScoped → 前端弹窗）。
//   - SystemPrompt 不允许包含执行指令外的越权内容（前端渲染时转义）。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Schema (Step 1)
// ---------------------------------------------------------------------------

// SkillScope 标记技能来源，决定激活前的安全检查。
type SkillScope string

const (
	SkillScopeProject SkillScope = "project" // 项目级，需确认（G-SEC-03）
	SkillScopeUser    SkillScope = "user"    // 用户级
	SkillScopeGlobal  SkillScope = "global"  // 内置全局
)

// SkillTrigger 描述激活条件。任一条件命中即激活：
//   - Keywords：消息包含其中任一关键词（大小写不敏感）。
//   - Regex：消息匹配正则。
//   - Manual：仅手动 @Skill 激活，不自动触发。
type SkillTrigger struct {
	Keywords []string `yaml:"keywords,omitempty"`
	Regex    string   `yaml:"regex,omitempty"`
	Manual   bool     `yaml:"manual,omitempty"`
}

// Skill 是单个技能定义。
type Skill struct {
	ID           string       `yaml:"id" json:"id"`
	Name         string       `yaml:"name" json:"name"`
	Description  string       `yaml:"description" json:"description"`
	Priority     int          `yaml:"priority,omitempty" json:"priority,omitempty"`
	Trigger      SkillTrigger `yaml:"trigger" json:"trigger"`
	SystemPrompt string       `yaml:"systemPrompt" json:"systemPrompt"`
	AllowedTools []string     `yaml:"allowedTools,omitempty" json:"allowedTools,omitempty"`
	AllowedMCP   []string     `yaml:"allowedMcp,omitempty" json:"allowedMcp,omitempty"`
	Examples     []string     `yaml:"examples,omitempty" json:"examples,omitempty"`
	Scope        SkillScope   `yaml:"-" json:"scope"` // 由加载位置决定，非 yaml 字段
	FilePath     string       `yaml:"-" json:"filePath,omitempty"`
	// approvedByUser 标记项目级 Skill 已获用户确认（G-SEC-03）。运行时状态。
	approvedByUser bool `yaml:"-" json:"-"`
}

// IsProjectScoped 返回 true 当 Skill 来自项目目录，首次激活需确认。
func (s *Skill) IsProjectScoped() bool { return s.Scope == SkillScopeProject }

// Matches 检查消息是否命中触发器（Step 3）。
func (s *Skill) Matches(message string) bool {
	msg := strings.ToLower(message)
	// Keywords：任一命中即激活。
	for _, kw := range s.Trigger.Keywords {
		if kw != "" && strings.Contains(msg, strings.ToLower(kw)) {
			return true
		}
	}
	// Regex：编译并匹配。
	if s.Trigger.Regex != "" {
		re, err := regexp.Compile(s.Trigger.Regex)
		if err == nil && re.MatchString(message) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// SkillsService (Step 2)
// ---------------------------------------------------------------------------

// SkillsService 发现、加载、匹配技能。
type SkillsService struct {
	mu               sync.RWMutex
	skills           []Skill
	projectDir       string
	userDir          string
	onMutationChange func() error
}

// skillsWorkspaceState is an in-process policy snapshot owned by the trusted
// ProjectService workspace transaction. Project approvals are restored only
// when the definition loaded from the original root has the same fingerprint.
type skillsWorkspaceState struct {
	projectDir           string
	approvedFingerprints map[string]string
}

func (s *SkillsService) captureWorkspaceState() (skillsWorkspaceState, error) {
	if s == nil {
		return skillsWorkspaceState{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := skillsWorkspaceState{
		projectDir:           s.projectDir,
		approvedFingerprints: make(map[string]string),
	}
	for _, skill := range s.skills {
		if !skill.IsProjectScoped() || !skill.approvedByUser {
			continue
		}
		fingerprint, err := skillFingerprint(skill)
		if err != nil {
			return skillsWorkspaceState{}, fmt.Errorf("fingerprint approved skill %q: %w", skill.ID, err)
		}
		state.approvedFingerprints[skill.ID] = fingerprint
	}
	return state, nil
}

func (s *SkillsService) restoreWorkspaceState(state skillsWorkspaceState) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.projectDir = state.projectDir
	s.skills = nil
	s.mu.Unlock()
	if err := s.notifyMutationChange(); err != nil {
		return fmt.Errorf("clear candidate skill source: %w", err)
	}
	if err := s.Load(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	approvedIndexes := make([]int, 0, len(state.approvedFingerprints))
	matched := make(map[string]struct{}, len(state.approvedFingerprints))
	for index := range s.skills {
		skill := s.skills[index]
		expected, approved := state.approvedFingerprints[skill.ID]
		if !approved {
			continue
		}
		fingerprint, err := skillFingerprint(skill)
		if err != nil {
			return fmt.Errorf("fingerprint restored skill %q: %w", skill.ID, err)
		}
		if !skill.IsProjectScoped() || fingerprint != expected {
			return fmt.Errorf("restored project skill %q changed identity: %w", skill.ID, ErrNotAllowed)
		}
		approvedIndexes = append(approvedIndexes, index)
		matched[skill.ID] = struct{}{}
	}
	if len(matched) != len(state.approvedFingerprints) {
		return fmt.Errorf("one or more approved project skills are unavailable after rollback: %w", ErrNotAllowed)
	}
	for _, index := range approvedIndexes {
		s.skills[index].approvedByUser = true
	}
	return nil
}

// NewSkillsService 创建服务。projectDir 为 workspace root（可空），
// configDir 用于定位用户级 skills 目录。
func NewSkillsService(configDir string) *SkillsService {
	svc := &SkillsService{
		userDir: filepath.Join(configDir, "koyori-ide", "skills"),
	}
	return svc
}

// setOnMutationChange installs the trusted backend callback used to refresh
// the Agent ToolDef source whenever the loaded skill set changes. Renderer
// code cannot replace this callback.
//
//wails:ignore
func (s *SkillsService) setOnMutationChange(callback func() error) {
	s.mu.Lock()
	s.onMutationChange = callback
	s.mu.Unlock()
}

func (s *SkillsService) notifyMutationChange() error {
	s.mu.RLock()
	callback := s.onMutationChange
	s.mu.RUnlock()
	if callback == nil {
		return nil
	}
	if err := callback(); err != nil {
		return fmt.Errorf("refresh skill agent tools: %w", err)
	}
	return nil
}

// setWorkspaceRoot 设置项目目录，项目级 skills 从 <root>/.koyori-ide/skills/ 加载。
//
//wails:ignore
func (s *SkillsService) setWorkspaceRoot(root string) error {
	s.mu.Lock()
	if root == "" {
		s.projectDir = ""
	} else {
		s.projectDir = filepath.Join(root, ".koyori-ide", "skills")
	}
	// A workspace switch revokes every loaded project skill and its approval
	// state before the new source is read. Keeping the old slice here would let
	// the mutation callback republish it under the new workspace generation.
	s.skills = nil
	s.mu.Unlock()
	// Workspace changes alter the source of project skills immediately. The
	// callback therefore burns old capabilities before the subsequent Load.
	return s.notifyMutationChange()
}

// Load 扫描项目级与用户级目录，加载所有 *.yaml 技能（Step 2）。
// 项目级 Skill 标记 Scope=project（G-SEC-03），用户级标记 user。
func (s *SkillsService) Load() error {
	s.mu.RLock()
	userDir := s.userDir
	projectDir := s.projectDir
	s.mu.RUnlock()
	var loaded []Skill
	// 用户级优先级低于项目级（同 ID 时项目级覆盖）。
	if userDir != "" {
		userSkills, err := loadSkillsFromDir(userDir, SkillScopeUser)
		if err != nil {
			return s.failClosedLoad(userDir, projectDir, fmt.Errorf("load user skills: %w", err))
		}
		loaded = append(loaded, userSkills...)
	}
	if projectDir != "" {
		projSkills, err := loadSkillsFromDir(projectDir, SkillScopeProject)
		if err != nil {
			return s.failClosedLoad(userDir, projectDir, fmt.Errorf("load project skills: %w", err))
		}
		loaded = append(loaded, projSkills...)
	}
	// 同 ID 去重：项目级覆盖用户级（后加载覆盖先加载）。
	seen := make(map[string]int)
	for i, sk := range loaded {
		if idx, ok := seen[sk.ID]; ok {
			// 覆盖：项目级（scope=project）优先。
			if loaded[idx].Scope == SkillScopeProject {
				continue // 已有项目级，跳过用户级
			}
			loaded[idx] = sk
		} else {
			seen[sk.ID] = i
		}
	}
	// 压缩为最终列表。
	var deduped []Skill
	for i, sk := range loaded {
		if seen[sk.ID] == i {
			deduped = append(deduped, sk)
		}
	}

	// Do not let a slow load for an old workspace overwrite a newer source.
	s.mu.Lock()
	if s.userDir != userDir || s.projectDir != projectDir {
		s.mu.Unlock()
		return fmt.Errorf("skill source changed while loading: %w", ErrNotAllowed)
	}
	s.skills = deduped
	s.mu.Unlock()
	return s.notifyMutationChange()
}

// failClosedLoad removes the previously published source when a staged load
// fails. The source identity check prevents an older concurrent load from
// clearing skills that have already been loaded for a newer workspace.
func (s *SkillsService) failClosedLoad(userDir, projectDir string, loadErr error) error {
	s.mu.Lock()
	if s.userDir != userDir || s.projectDir != projectDir {
		s.mu.Unlock()
		return loadErr
	}
	s.skills = nil
	s.mu.Unlock()
	if refreshErr := s.notifyMutationChange(); refreshErr != nil {
		return errors.Join(loadErr, refreshErr)
	}
	return loadErr
}

// loadSkillsFromDir 扫描目录加载所有 *.yaml。
func loadSkillsFromDir(dir string, scope SkillScope) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 目录不存在不算错误
		}
		return nil, err
	}
	var skills []Skill
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var sk Skill
		if err := yaml.Unmarshal(data, &sk); err != nil {
			continue
		}
		if sk.ID == "" {
			sk.ID = strings.TrimSuffix(name, filepath.Ext(name))
		}
		sk.Scope = scope
		sk.FilePath = path
		skills = append(skills, sk)
	}
	return skills, nil
}

// ListSkills 返回所有已加载技能的副本。
func (s *SkillsService) ListSkills() []Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Skill, len(s.skills))
	copy(out, s.skills)
	return out
}

// GetSkill 按 ID 返回单个技能。
func (s *SkillsService) GetSkill(id string) (Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sk := range s.skills {
		if sk.ID == id {
			return sk, nil
		}
	}
	return Skill{}, fmt.Errorf("skill %q: %w", id, ErrNotFound)
}

// MatchTriggers 返回消息命中的技能（Step 3）。Manual 技能不自动触发。
// 项目级技能需已获用户批准（G-SEC-03），未批准的返回但标记 pending。
func (s *SkillsService) MatchTriggers(_ context.Context, message string) []Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched []Skill
	for _, sk := range s.skills {
		if sk.Trigger.Manual {
			continue
		}
		if sk.Matches(message) {
			matched = append(matched, sk)
		}
	}
	return matched
}

// ActivateSkill is kept at the binding surface as a deny-only compatibility
// stub. Project skill activation must traverse the unified Agent capability
// pipeline so renderer state cannot manufacture approval.
func (s *SkillsService) ActivateSkill(id string) error {
	return fmt.Errorf("activate skill %q through the Agent skill ToolDef: %w", id, ErrNotAllowed)
}

func (s *SkillsService) activateSkillTrusted(id string) error {
	s.mu.Lock()
	for i := range s.skills {
		if s.skills[i].ID == id {
			s.skills[i].approvedByUser = true
			s.mu.Unlock()
			// Approval is session policy state, not a catalog definition. Do
			// not advance the global revision for an activation that leaves
			// ToolDef metadata unchanged.
			return nil
		}
	}
	s.mu.Unlock()
	return fmt.Errorf("skill %q: %w", id, ErrNotFound)
}

func (s *SkillsService) restoreSkillApprovalTrusted(id string, approved bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.skills {
		if s.skills[i].ID != id {
			continue
		}
		if !s.skills[i].IsProjectScoped() && !approved {
			return nil
		}
		s.skills[i].approvedByUser = approved
		return nil
	}
	return fmt.Errorf("skill %q: %w", id, ErrNotFound)
}

// IsApproved 返回 true 当技能已获用户确认（或无需确认）。
func (s *SkillsService) IsApproved(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sk := range s.skills {
		if sk.ID == id {
			if !sk.IsProjectScoped() {
				return true // 用户级/全局无需确认
			}
			return sk.approvedByUser
		}
	}
	return false
}

// MergeSystemPrompts 合并多个技能的 SystemPrompt（Step 4）。
// 按 Priority 降序排列，拼接为单个提示。Priority 相同时按 Name 排序（稳定）。
func MergeSystemPrompts(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	// 按 Priority 降序排序（高优先级在前）。
	sorted := make([]Skill, len(skills))
	copy(sorted, skills)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority > sorted[j].Priority
		}
		return sorted[i].Name < sorted[j].Name
	})
	var parts []string
	for _, sk := range sorted {
		if sk.SystemPrompt != "" {
			parts = append(parts, fmt.Sprintf("[%s]\n%s", sk.Name, sk.SystemPrompt))
		}
	}
	return strings.Join(parts, "\n\n")
}

// AllowedToolsForSkills 合并多个技能的 AllowedTools 白名单（Step 4 / G-SEC-02）。
// 若任一技能的 AllowedTools 为空，视为「不限制」（返回 nil 表示全部允许）。
func AllowedToolsForSkills(skills []Skill) []string {
	var all []string
	seen := make(map[string]bool)
	for _, sk := range skills {
		if len(sk.AllowedTools) == 0 {
			return nil // 任一不限制 → 全部允许
		}
		for _, t := range sk.AllowedTools {
			if !seen[t] {
				seen[t] = true
				all = append(all, t)
			}
		}
	}
	return all
}
