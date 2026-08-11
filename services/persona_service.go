package services

// Plan 11 Task 8 — Persona（历史人物角色）。
//
// 提供内置 7 个 Persona + 用户自定义，注入 SystemPrompt 影响对话风格。
// 持久化 ~/.config/koyori-ide/personas/*.json（0600 + atomicWriteJSON）。
// 支持知识库附加 + 市场 导出/导入。
//
// 安全：
//   - 用户自定义 Persona 的 SystemPrompt 视同项目级 Skill（不可信内容），
//     但不触发 G-SEC-03 弹窗（Persona 是用户主动创建的）。
//   - Persona ID 经 validPersonaID 清洗（拒绝路径分隔符/遍历），避免
//     personaPath 拼接出逃逸 personasDir 的路径（G-SEC-06）。
//   - 知识库文件内容注入 SystemPrompt（复用 @文件）；路径为用户主动指定的
//     任意可读文件，best-effort 读取（用户读取自己的文件，非服务端拼接）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Persona schema（Step 1）
// ---------------------------------------------------------------------------

// Persona 描述一个 AI 助手角色。
type Persona struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Avatar        string    `json:"avatar,omitempty"` // base64 或 URL
	SystemPrompt  string    `json:"systemPrompt"`
	Tone          string    `json:"tone,omitempty"`          // 如 "严谨" / "幽默"
	Expertise     []string  `json:"expertise,omitempty"`     // 如 ["Go", "DevOps"]
	KnowledgeBase []string  `json:"knowledgeBase,omitempty"` // 文件路径列表
	DefaultModel  string    `json:"defaultModel,omitempty"`
	DefaultMode   string    `json:"defaultMode,omitempty"` // chat/plan/goal/agent
	BuiltIn       bool      `json:"builtIn"`               // 内置不可删除
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// 内置 Persona ID 常量。
const (
	PersonaGoGuru           = "builtin-go-guru"
	PersonaTypeScriptMaster = "builtin-typescript-master"
	PersonaSecurityAuditor  = "builtin-security-auditor"
	PersonaDevOpsEngineer   = "builtin-devops-engineer"
	PersonaTechWriter       = "builtin-tech-writer"
	PersonaCodeReviewer     = "builtin-code-reviewer"
	PersonaJuniorDev        = "builtin-junior-dev"
)

// builtinPersonas 返回 7 个内置 Persona（Step 2）。
func builtinPersonas() []Persona {
	now := time.Now()
	return []Persona{
		{
			ID: PersonaGoGuru, Name: "Go Guru", BuiltIn: true,
			SystemPrompt: "You are a Go expert with deep knowledge of the language spec, runtime, and ecosystem. Provide idiomatic, performant solutions. Prefer stdlib over third-party packages when reasonable.",
			Tone:         "严谨", Expertise: []string{"Go", "并发", "性能优化"}, DefaultMode: "chat",
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: PersonaTypeScriptMaster, Name: "TypeScript Master", BuiltIn: true,
			SystemPrompt: "You are a TypeScript expert with mastery of the type system, generics, and modern ECMAScript features. Provide type-safe solutions and catch type errors proactively.",
			Tone:         "精确", Expertise: []string{"TypeScript", "Vue", "类型系统"}, DefaultMode: "chat",
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: PersonaSecurityAuditor, Name: "Security Auditor", BuiltIn: true,
			SystemPrompt: "You are a security auditor focused on code vulnerabilities. Identify OWASP Top 10 issues, injection risks, auth/authz flaws, and suggest mitigations following industry best practices.",
			Tone:         "警惕", Expertise: []string{"安全审计", "OWASP", "加密"}, DefaultMode: "agent",
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: PersonaDevOpsEngineer, Name: "DevOps Engineer", BuiltIn: true,
			SystemPrompt: "You are a DevOps engineer expert in CI/CD, containerization, and infrastructure as code. Suggest reliable, reproducible pipelines and observability improvements.",
			Tone:         "稳健", Expertise: []string{"CI/CD", "Docker", "Kubernetes"}, DefaultMode: "agent",
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: PersonaTechWriter, Name: "Tech Writer", BuiltIn: true,
			SystemPrompt: "You are a technical writer skilled at clear, concise documentation. Produce READMEs, API docs, and tutorials that are accurate and easy to follow.",
			Tone:         "清晰", Expertise: []string{"文档", "README", "API"}, DefaultMode: "chat",
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: PersonaCodeReviewer, Name: "Code Reviewer", BuiltIn: true,
			SystemPrompt: "You are a meticulous code reviewer. Check correctness, maintainability, performance, and style. Suggest concrete improvements with diffs.",
			Tone:         "批判", Expertise: []string{"代码审查", "重构", "最佳实践"}, DefaultMode: "agent",
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: PersonaJuniorDev, Name: "Junior Dev", BuiltIn: true,
			SystemPrompt: "You are a junior developer eager to learn. Ask clarifying questions, explain your reasoning, and prefer simpler solutions you can fully understand.",
			Tone:         "好学", Expertise: []string{"基础", "学习"}, DefaultMode: "chat",
			CreatedAt: now, UpdatedAt: now,
		},
	}
}

// ---------------------------------------------------------------------------
// PersonaService
// ---------------------------------------------------------------------------

// PersonaService 管理 Persona CRUD + 持久化（Step 1-10）。
type PersonaService struct {
	mu        sync.RWMutex
	personas  map[string]Persona
	configDir string
}

// NewPersonaService 创建服务并加载内置 + 用户自定义 Persona。
func NewPersonaService(configDir string) *PersonaService {
	svc := &PersonaService{
		configDir: configDir,
		personas:  make(map[string]Persona),
	}
	// 加载内置。
	for _, p := range builtinPersonas() {
		svc.personas[p.ID] = p
	}
	// 加载用户自定义（best-effort）。
	_ = svc.loadUserPersonas()
	return svc
}

// personasDir 返回用户 Persona 目录。
func (s *PersonaService) personasDir() string {
	return filepath.Join(s.configDir, "koyori-ide", "personas")
}

// loadUserPersonas 从磁盘加载用户自定义 Persona（Step 3/4）。
func (s *PersonaService) loadUserPersonas() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.personasDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read personas dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.personasDir(), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p Persona
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		p.BuiltIn = false
		s.personas[p.ID] = p
	}
	return nil
}

// personaPath 返回单个 Persona 的文件路径。G-SEC-06：ID 经
// validPersonaID 清洗（调用方负责校验），此处再用 filepath.Base 兜底。
func (s *PersonaService) personaPath(id string) string {
	return filepath.Join(s.personasDir(), filepath.Base(id)+".json")
}

// validPersonaID 校验 Persona ID 是否安全（G-SEC-06）。
// 拒绝空、`.`、`..` 以及包含路径分隔符或 NUL 的 ID，避免 personaPath
// 拼接出逃逸 personasDir 的路径。
func validPersonaID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, `/\`) || strings.ContainsRune(id, 0) {
		return false
	}
	return filepath.Base(id) == id
}

// ListPersonas 返回所有 Persona（内置 + 用户自定义）。
func (s *PersonaService) ListPersonas() []Persona {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Persona, 0, len(s.personas))
	for _, p := range s.personas {
		out = append(out, p)
	}
	return out
}

// GetPersona 按 ID 查找。
func (s *PersonaService) GetPersona(id string) (Persona, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.personas[id]
	if !ok {
		return Persona{}, fmt.Errorf("persona %q: %w", id, ErrNotFound)
	}
	return p, nil
}

// CreatePersona 创建新 Persona 并持久化（Step 3）。
func (s *PersonaService) CreatePersona(p Persona) error {
	if !validPersonaID(p.ID) {
		return fmt.Errorf("invalid persona id: %w", ErrInvalidInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.personas[p.ID]; exists {
		return fmt.Errorf("persona %q: %w", p.ID, ErrAlreadyExists)
	}
	p.BuiltIn = false
	p.CreatedAt = time.Now()
	p.UpdatedAt = p.CreatedAt
	s.personas[p.ID] = p
	return s.savePersonaLocked(p)
}

// UpdatePersona 更新 Persona（Step 3）。
// 内置 Persona 不允许修改 SystemPrompt（只允许关联知识库）。
func (s *PersonaService) UpdatePersona(p Persona) error {
	if !validPersonaID(p.ID) {
		return fmt.Errorf("invalid persona id: %w", ErrInvalidInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.personas[p.ID]
	if !ok {
		return fmt.Errorf("persona %q: %w", p.ID, ErrNotFound)
	}
	if existing.BuiltIn {
		// 内置 Persona：保留原 SystemPrompt/Name，仅允许更新 KnowledgeBase。
		existing.KnowledgeBase = p.KnowledgeBase
		existing.DefaultModel = p.DefaultModel
		existing.DefaultMode = p.DefaultMode
		existing.UpdatedAt = time.Now()
		s.personas[p.ID] = existing
		return nil
	}
	p.BuiltIn = false
	p.CreatedAt = existing.CreatedAt
	p.UpdatedAt = time.Now()
	s.personas[p.ID] = p
	return s.savePersonaLocked(p)
}

// DeletePersona 删除 Persona（内置不可删除）。
func (s *PersonaService) DeletePersona(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.personas[id]
	if !ok {
		return fmt.Errorf("persona %q: %w", id, ErrNotFound)
	}
	if p.BuiltIn {
		return fmt.Errorf("cannot delete builtin persona: %w", ErrNotAllowed)
	}
	delete(s.personas, id)
	// 删除磁盘文件（best-effort）。
	_ = os.Remove(s.personaPath(id))
	return nil
}

// savePersonaLocked 持久化单个 Persona（0600 + atomicWriteJSON）。
// 调用方需持有写锁。
func (s *PersonaService) savePersonaLocked(p Persona) error {
	return atomicWriteJSON(s.personaPath(p.ID), p, 0600)
}

// ---------------------------------------------------------------------------
// 知识库注入（Step 7）
// ---------------------------------------------------------------------------

// BuildSystemPromptWithKnowledge 构造完整 SystemPrompt，注入知识库文件内容。
// Step 7：知识库文件内容注入 SystemPrompt（复用 @文件）。
// Step 9：Token 计数 + 自动截断（防超 context window）。
func (s *PersonaService) BuildSystemPromptWithKnowledge(id string, maxTokens int) (string, error) {
	p, err := s.GetPersona(id)
	if err != nil {
		return "", err
	}
	prompt := p.SystemPrompt
	// 注入知识库文件内容。
	for _, kbPath := range p.KnowledgeBase {
		data, err := os.ReadFile(kbPath)
		if err != nil {
			continue // best-effort
		}
		content := string(data)
		// Step 9：粗略 Token 估算（4 字符 ≈ 1 token），截断防超限。
		maxContentTokens := maxTokens / 4
		if len(content) > maxContentTokens*4 {
			content = content[:maxContentTokens*4] + "\n...[truncated]"
		}
		prompt += "\n\n--- Knowledge: " + filepath.Base(kbPath) + " ---\n" + content
	}
	return prompt, nil
}

// ---------------------------------------------------------------------------
// 市场：导出/导入（Step 8）
// ---------------------------------------------------------------------------

// ExportPersona 导出 Persona 为 JSON 字节（Step 8）。
func (s *PersonaService) ExportPersona(id string) ([]byte, error) {
	p, err := s.GetPersona(id)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(p, "", "  ")
}

// ImportPersona 从 JSON 字节导入 Persona（Step 8）。
// 若 ID 已存在则覆盖（用户自定义）；内置 ID 拒绝覆盖。
func (s *PersonaService) ImportPersona(data []byte) error {
	var p Persona
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("parse persona: %w", err)
	}
	if p.ID == "" {
		return fmt.Errorf("imported persona missing id: %w", ErrInvalidInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.personas[p.ID]; ok && existing.BuiltIn {
		return fmt.Errorf("cannot overwrite builtin persona %q: %w", p.ID, ErrNotAllowed)
	}
	p.BuiltIn = false
	p.CreatedAt = time.Now()
	p.UpdatedAt = p.CreatedAt
	s.personas[p.ID] = p
	return s.savePersonaLocked(p)
}
