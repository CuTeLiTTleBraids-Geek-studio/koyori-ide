package services

// Plan 11 Task 8 Step 10 — PersonaService 测试覆盖。
//
// 覆盖：
//   - 内置 7 个 Persona 加载
//   - CRUD：Create/Update/Delete
//   - 内置不可删除/不可覆盖 SystemPrompt
//   - 知识库注入（BuildSystemPromptWithKnowledge）
//   - Token 截断（Step 9）
//   - 市场 导出/导入

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestPersonaService(t *testing.T) *PersonaService {
	t.Helper()
	dir := t.TempDir()
	return NewPersonaService(dir)
}

// --- Step 2: 7 个内置 Persona ---

func TestPersonaService_BuiltinPersonas(t *testing.T) {
	svc := newTestPersonaService(t)
	list := svc.ListPersonas()
	if len(list) < 7 {
		t.Fatalf("expected at least 7 builtin personas, got %d", len(list))
	}
	expected := []string{
		PersonaGoGuru, PersonaTypeScriptMaster, PersonaSecurityAuditor,
		PersonaDevOpsEngineer, PersonaTechWriter, PersonaCodeReviewer, PersonaJuniorDev,
	}
	for _, id := range expected {
		p, err := svc.GetPersona(id)
		if err != nil {
			t.Errorf("builtin persona %q not found: %v", id, err)
			continue
		}
		if !p.BuiltIn {
			t.Errorf("persona %q should be BuiltIn", id)
		}
		if p.SystemPrompt == "" {
			t.Errorf("persona %q should have SystemPrompt", id)
		}
	}
}

func TestPersonaService_GetPersona_NotFound(t *testing.T) {
	svc := newTestPersonaService(t)
	_, err := svc.GetPersona("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent persona")
	}
}

// --- Step 3: CRUD ---

func TestPersonaService_CreatePersona(t *testing.T) {
	svc := newTestPersonaService(t)
	p := Persona{
		ID:           "custom-1",
		Name:         "My Custom",
		SystemPrompt: "You are a custom assistant.",
	}
	if err := svc.CreatePersona(p); err != nil {
		t.Fatalf("CreatePersona failed: %v", err)
	}
	// 持久化验证。
	svc2 := NewPersonaService(svc.configDir)
	got, err := svc2.GetPersona("custom-1")
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if got.Name != "My Custom" {
		t.Errorf("Name = %q, want My Custom", got.Name)
	}
	if got.BuiltIn {
		t.Error("custom persona should not be BuiltIn")
	}
}

func TestPersonaService_CreatePersona_Duplicate(t *testing.T) {
	svc := newTestPersonaService(t)
	p := Persona{ID: "dup", Name: "First"}
	if err := svc.CreatePersona(p); err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	err := svc.CreatePersona(Persona{ID: "dup", Name: "Second"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected already-exists error, got %v", err)
	}
}

// TestPersonaService_InvalidID_TraversalRejected 验证 G-SEC-06：含路径
// 分隔符或遍历的 ID 被拒绝，避免 personaPath 逃逸 personasDir。
func TestPersonaService_InvalidID_TraversalRejected(t *testing.T) {
	svc := newTestPersonaService(t)
	cases := []string{"", ".", "..", "../etc", "a/b", `a\b`, "x\x00y"}
	for _, id := range cases {
		err := svc.CreatePersona(Persona{ID: id, Name: "bad"})
		if err == nil {
			t.Errorf("expected error for persona id %q, got nil", id)
		}
	}
	// 合法 ID 应通过。
	if err := svc.CreatePersona(Persona{ID: "einstein", Name: "ok"}); err != nil {
		t.Fatalf("valid id rejected: %v", err)
	}
}

func TestPersonaService_UpdatePersona(t *testing.T) {
	svc := newTestPersonaService(t)
	_ = svc.CreatePersona(Persona{ID: "upd", Name: "Old", SystemPrompt: "old"})
	err := svc.UpdatePersona(Persona{ID: "upd", Name: "New", SystemPrompt: "new"})
	if err != nil {
		t.Fatalf("UpdatePersona failed: %v", err)
	}
	got, _ := svc.GetPersona("upd")
	if got.Name != "New" {
		t.Errorf("Name = %q, want New", got.Name)
	}
}

func TestPersonaService_DeletePersona(t *testing.T) {
	svc := newTestPersonaService(t)
	_ = svc.CreatePersona(Persona{ID: "del", Name: "To Delete"})
	if err := svc.DeletePersona("del"); err != nil {
		t.Fatalf("DeletePersona failed: %v", err)
	}
	if _, err := svc.GetPersona("del"); err == nil {
		t.Error("persona should be deleted")
	}
}

func TestPersonaService_DeletePersona_BuiltinRejected(t *testing.T) {
	svc := newTestPersonaService(t)
	err := svc.DeletePersona(PersonaGoGuru)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("expected not-allowed error for builtin delete, got %v", err)
	}
}

// 内置 Persona 不允许修改 SystemPrompt
func TestPersonaService_UpdateBuiltin_PreservesSystemPrompt(t *testing.T) {
	svc := newTestPersonaService(t)
	original, _ := svc.GetPersona(PersonaGoGuru)
	originalPrompt := original.SystemPrompt
	// 尝试修改 SystemPrompt（应被忽略）。
	err := svc.UpdatePersona(Persona{
		ID:            PersonaGoGuru,
		Name:          "Renamed",
		SystemPrompt:  "HACKED",
		KnowledgeBase: []string{"/tmp/x"},
	})
	if err != nil {
		t.Fatalf("UpdatePersona builtin failed: %v", err)
	}
	got, _ := svc.GetPersona(PersonaGoGuru)
	if got.SystemPrompt != originalPrompt {
		t.Errorf("builtin SystemPrompt should be preserved, got %q", got.SystemPrompt)
	}
	if got.Name == "Renamed" {
		t.Error("builtin Name should be preserved")
	}
	// KnowledgeBase 应允许更新。
	if len(got.KnowledgeBase) != 1 || got.KnowledgeBase[0] != "/tmp/x" {
		t.Errorf("KnowledgeBase should be updatable, got %v", got.KnowledgeBase)
	}
}

// --- Step 7: 知识库注入 ---

func TestPersonaService_BuildSystemPromptWithKnowledge(t *testing.T) {
	svc := newTestPersonaService(t)
	// 创建临时知识库文件。
	kbPath := filepath.Join(svc.configDir, "kb.txt")
	if err := os.WriteFile(kbPath, []byte("Knowledge content here."), 0644); err != nil {
		t.Fatalf("write kb file: %v", err)
	}
	_ = svc.CreatePersona(Persona{
		ID:            "kb-test",
		Name:          "KB",
		SystemPrompt:  "Base prompt.",
		KnowledgeBase: []string{kbPath},
	})
	prompt, err := svc.BuildSystemPromptWithKnowledge("kb-test", 10000)
	if err != nil {
		t.Fatalf("BuildSystemPromptWithKnowledge failed: %v", err)
	}
	if !strings.Contains(prompt, "Base prompt.") {
		t.Error("prompt should contain base SystemPrompt")
	}
	if !strings.Contains(prompt, "Knowledge content here.") {
		t.Error("prompt should contain knowledge content")
	}
	if !strings.Contains(prompt, "kb.txt") {
		t.Error("prompt should contain knowledge file name")
	}
}

// --- Step 9: Token 截断 ---

func TestPersonaService_BuildSystemPrompt_TokenTruncation(t *testing.T) {
	svc := newTestPersonaService(t)
	// 创建大知识库文件。
	longContent := strings.Repeat("A", 10000)
	kbPath := filepath.Join(svc.configDir, "big.txt")
	_ = os.WriteFile(kbPath, []byte(longContent), 0644)
	_ = svc.CreatePersona(Persona{
		ID:            "trunc-test",
		Name:          "Trunc",
		SystemPrompt:  "Base.",
		KnowledgeBase: []string{kbPath},
	})
	// maxTokens=100 → 内容应被截断。
	prompt, err := svc.BuildSystemPromptWithKnowledge("trunc-test", 100)
	if err != nil {
		t.Fatalf("BuildSystemPromptWithKnowledge failed: %v", err)
	}
	if !strings.Contains(prompt, "truncated") {
		t.Error("prompt should contain truncation marker")
	}
}

// --- Step 8: 市场 导出/导入 ---

func TestPersonaService_ExportImport(t *testing.T) {
	svc := newTestPersonaService(t)
	_ = svc.CreatePersona(Persona{
		ID:           "export-test",
		Name:         "Export",
		SystemPrompt: "Export me.",
	})
	data, err := svc.ExportPersona("export-test")
	if err != nil {
		t.Fatalf("ExportPersona failed: %v", err)
	}
	// 验证 JSON 可解析。
	var p Persona
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("exported data not valid JSON: %v", err)
	}
	if p.Name != "Export" {
		t.Errorf("exported Name = %q, want Export", p.Name)
	}
	// 导入到新服务。
	svc2 := newTestPersonaService(t)
	if err := svc2.ImportPersona(data); err != nil {
		t.Fatalf("ImportPersona failed: %v", err)
	}
	got, _ := svc2.GetPersona("export-test")
	if got.Name != "Export" {
		t.Errorf("imported Name = %q, want Export", got.Name)
	}
}

func TestPersonaService_ImportPersona_RejectBuiltinOverwrite(t *testing.T) {
	svc := newTestPersonaService(t)
	// 构造内置 ID 的导入数据。
	data, _ := json.Marshal(Persona{ID: PersonaGoGuru, Name: "Hacked"})
	err := svc.ImportPersona(data)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("expected not-allowed for builtin overwrite, got %v", err)
	}
}

func TestPersonaService_ImportPersona_MissingID(t *testing.T) {
	svc := newTestPersonaService(t)
	data, _ := json.Marshal(Persona{Name: "NoID"})
	err := svc.ImportPersona(data)
	if err == nil || !strings.Contains(err.Error(), "missing id") {
		t.Errorf("expected missing-id error, got %v", err)
	}
}
