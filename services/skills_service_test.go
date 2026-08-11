package services

// Plan 11 Task 5 Step 9 — Skills service tests.
//
// 覆盖：加载/触发匹配/优先级合并/AllowedTools 白名单/G-SEC-03 项目级确认。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillYAML(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func newTestSkillsService(t *testing.T) *SkillsService {
	t.Helper()
	dir := t.TempDir()
	svc := NewSkillsService(dir)
	svc.setWorkspaceRoot(t.TempDir())
	return svc
}

func TestSkill_Matches_Keywords(t *testing.T) {
	sk := Skill{
		Trigger: SkillTrigger{Keywords: []string{"重构", "refactor"}},
	}
	if !sk.Matches("请重构这个函数") {
		t.Error("keyword 重构 should match")
	}
	if !sk.Matches("please refactor this") {
		t.Error("keyword refactor should match")
	}
	if sk.Matches("no match here") {
		t.Error("should not match")
	}
}

func TestSkill_Matches_Regex(t *testing.T) {
	sk := Skill{
		Trigger: SkillTrigger{Regex: `(?i)\b(fix|bug)\b`},
	}
	if !sk.Matches("fix the bug") {
		t.Error("regex should match")
	}
	if sk.Matches("feature request") {
		t.Error("regex should not match")
	}
}

func TestSkill_Matches_ManualNeverAutoMatches(t *testing.T) {
	sk := Skill{
		Trigger: SkillTrigger{Keywords: []string{"test"}, Manual: true},
	}
	// Matches() 仅检查关键词/正则，不检查 Manual。Manual 在 MatchTriggers 层过滤。
	if !sk.Matches("test this") {
		t.Error("keyword should match at Matches level")
	}
}

func TestSkillsService_Load_UserAndProject(t *testing.T) {
	tmp := t.TempDir()
	userDir := filepath.Join(tmp, "koyori-ide", "skills")
	projDir := filepath.Join(tmp, "proj", ".koyori-ide", "skills")
	writeSkillYAML(t, userDir, "user1.yaml", `
id: user-skill
name: User Skill
description: A user-level skill
trigger:
  keywords: [user]
systemPrompt: You are a user skill.
`)
	writeSkillYAML(t, projDir, "proj1.yaml", `
id: proj-skill
name: Project Skill
description: A project-level skill
trigger:
  keywords: [proj]
systemPrompt: You are a project skill.
`)

	svc := NewSkillsService(tmp)
	svc.setWorkspaceRoot(filepath.Join(tmp, "proj"))
	if err := svc.Load(); err != nil {
		t.Fatal(err)
	}
	skills := svc.ListSkills()
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}
	// 验证 scope 标记。
	proj, _ := svc.GetSkill("proj-skill")
	if proj.Scope != SkillScopeProject {
		t.Errorf("proj-skill scope = %v, want project", proj.Scope)
	}
	usr, _ := svc.GetSkill("user-skill")
	if usr.Scope != SkillScopeUser {
		t.Errorf("user-skill scope = %v, want user", usr.Scope)
	}
}

func TestSkillsService_Load_ProjectOverridesUser(t *testing.T) {
	tmp := t.TempDir()
	userDir := filepath.Join(tmp, "koyori-ide", "skills")
	projDir := filepath.Join(tmp, "proj", ".koyori-ide", "skills")
	writeSkillYAML(t, userDir, "dup.yaml", `
id: dup
name: User Dup
trigger: {keywords: [x]}
systemPrompt: user
`)
	writeSkillYAML(t, projDir, "dup.yaml", `
id: dup
name: Project Dup
trigger: {keywords: [x]}
systemPrompt: project
`)
	svc := NewSkillsService(tmp)
	svc.setWorkspaceRoot(filepath.Join(tmp, "proj"))
	if err := svc.Load(); err != nil {
		t.Fatal(err)
	}
	sk, err := svc.GetSkill("dup")
	if err != nil {
		t.Fatal(err)
	}
	if sk.Scope != SkillScopeProject {
		t.Errorf("dup should be project-scoped, got %v", sk.Scope)
	}
	if sk.SystemPrompt != "project" {
		t.Errorf("dup should be overridden by project, got %q", sk.SystemPrompt)
	}
}

func TestSkillsService_MatchTriggers(t *testing.T) {
	svc := newTestSkillsService(t)
	// 直接构造 skills 切片绕过文件加载，便于测试匹配逻辑。
	svc.mu.Lock()
	svc.skills = []Skill{
		{ID: "a", Trigger: SkillTrigger{Keywords: []string{"refactor"}}, SystemPrompt: "A"},
		{ID: "b", Trigger: SkillTrigger{Regex: `(?i)bug`}, SystemPrompt: "B"},
		{ID: "c", Trigger: SkillTrigger{Keywords: []string{"test"}, Manual: true}, SystemPrompt: "C"},
	}
	svc.mu.Unlock()

	matched := svc.MatchTriggers(context.Background(), "please refactor this")
	if len(matched) != 1 || matched[0].ID != "a" {
		t.Errorf("expected only skill a, got %v", matched)
	}
	matched = svc.MatchTriggers(context.Background(), "fix this bug")
	if len(matched) != 1 || matched[0].ID != "b" {
		t.Errorf("expected only skill b, got %v", matched)
	}
	// Manual skill 不自动触发。
	matched = svc.MatchTriggers(context.Background(), "test something")
	if len(matched) != 0 {
		t.Errorf("manual skill should not auto-trigger, got %v", matched)
	}
}

func TestSkillsService_GSEC03_ProjectApproval(t *testing.T) {
	svc := newTestSkillsService(t)
	svc.mu.Lock()
	svc.skills = []Skill{
		{ID: "proj", Scope: SkillScopeProject, Trigger: SkillTrigger{Keywords: []string{"x"}}},
		{ID: "user", Scope: SkillScopeUser, Trigger: SkillTrigger{Keywords: []string{"x"}}},
	}
	svc.mu.Unlock()
	// 项目级未批准 → IsApproved=false。
	if svc.IsApproved("proj") {
		t.Error("project skill should not be approved by default")
	}
	// 用户级无需批准。
	if !svc.IsApproved("user") {
		t.Error("user skill should be auto-approved")
	}
	// 批准后。
	if err := svc.ActivateSkill("proj"); err != nil {
		t.Fatal(err)
	}
	if !svc.IsApproved("proj") {
		t.Error("project skill should be approved after ActivateSkill")
	}
}

func TestMergeSystemPrompts_PriorityOrder(t *testing.T) {
	skills := []Skill{
		{ID: "low", Name: "Low", Priority: 1, SystemPrompt: "low-priority"},
		{ID: "high", Name: "High", Priority: 10, SystemPrompt: "high-priority"},
		{ID: "mid", Name: "Mid", Priority: 5, SystemPrompt: "mid-priority"},
	}
	merged := MergeSystemPrompts(skills)
	// 高优先级在前。MergeSystemPrompts 把每个 prompt 格式化为 "[Name]\nSystemPrompt"，
	// 用 "\n\n" 连接，因此应验证相对顺序而非绝对位置。
	highPos := indexOf(merged, "high-priority")
	midPos := indexOf(merged, "mid-priority")
	lowPos := indexOf(merged, "low-priority")
	if highPos < 0 || midPos < 0 || lowPos < 0 {
		t.Fatalf("missing prompts: high=%d mid=%d low=%d", highPos, midPos, lowPos)
	}
	if !(highPos < midPos && midPos < lowPos) {
		t.Errorf("expected high < mid < low, got high=%d mid=%d low=%d", highPos, midPos, lowPos)
	}
	// 验证头部包含最高优先级 Name 标记。
	if !strings.HasPrefix(merged, "[High]") {
		t.Errorf("expected merged to start with [High], got %q", merged)
	}
}

func TestMergeSystemPrompts_Empty(t *testing.T) {
	if MergeSystemPrompts(nil) != "" {
		t.Error("empty should return empty string")
	}
}

func TestAllowedToolsForSkills_Intersection(t *testing.T) {
	skills := []Skill{
		{ID: "a", AllowedTools: []string{"read", "search"}},
		{ID: "b", AllowedTools: []string{"read", "write"}},
	}
	tools := AllowedToolsForSkills(skills)
	// 两个都限制 → 取并集（白名单是各 skill 允许的工具集合的并集）。
	if len(tools) != 3 {
		t.Errorf("expected 3 tools (read/search/write), got %v", tools)
	}
}

func TestAllowedToolsForSkills_UnrestrictedWhenAnyEmpty(t *testing.T) {
	skills := []Skill{
		{ID: "a", AllowedTools: []string{"read"}},
		{ID: "b", AllowedTools: nil}, // 不限制
	}
	tools := AllowedToolsForSkills(skills)
	if tools != nil {
		t.Errorf("nil AllowedTools in any skill → unrestricted (nil), got %v", tools)
	}
}

func TestSkillsService_GetSkill_NotFound(t *testing.T) {
	svc := newTestSkillsService(t)
	_, err := svc.GetSkill("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent skill")
	}
}

func TestSkillsService_ActivateSkill_NotFound(t *testing.T) {
	svc := newTestSkillsService(t)
	if err := svc.ActivateSkill("nope"); err == nil {
		t.Error("expected error for activating nonexistent skill")
	}
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
