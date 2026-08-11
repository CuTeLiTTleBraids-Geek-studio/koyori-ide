package services

import (
	"strings"
	"testing"
)

func TestDefaultSystemPrompt_IsNotEmpty(t *testing.T) {
	p := DefaultSystemPrompt
	if strings.TrimSpace(p) == "" {
		t.Error("DefaultSystemPrompt must not be empty")
	}
}

func TestDefaultSystemPrompt_MentionsCodeAssistant(t *testing.T) {
	p := DefaultSystemPrompt
	lower := strings.ToLower(p)
	if !strings.Contains(lower, "code") || !strings.Contains(lower, "assistant") {
		t.Error("DefaultSystemPrompt should mention 'code' and 'assistant'")
	}
}

func TestDefaultSystemPrompt_MentionsSafety(t *testing.T) {
	p := DefaultSystemPrompt
	lower := strings.ToLower(p)
	if !strings.Contains(lower, "safety") && !strings.Contains(lower, "destructive") {
		t.Error("DefaultSystemPrompt should mention safety/destructive operations")
	}
}

func TestDefaultSystemPrompt_MentionsFencedCodeBlocks(t *testing.T) {
	p := DefaultSystemPrompt
	lower := strings.ToLower(p)
	if !strings.Contains(lower, "fenced") && !strings.Contains(lower, "code block") {
		t.Error("DefaultSystemPrompt should mention fenced code blocks")
	}
}

func TestDefaultSystemPrompt_MentionsIDEEnvironment(t *testing.T) {
	p := DefaultSystemPrompt
	lower := strings.ToLower(p)
	if !strings.Contains(lower, "ide") {
		t.Error("DefaultSystemPrompt should mention the IDE environment")
	}
}

func TestPresetPrompts_ContainsAllExpectedActions(t *testing.T) {
	expected := []string{"explain", "refactor", "fix", "implement", "generate_docs", "generate_tests", "optimize", "review", "security", "commit_message"}
	for _, name := range expected {
		if _, ok := PresetPrompts[name]; !ok {
			t.Errorf("PresetPrompts missing key %q", name)
		}
	}
}

func TestPresetPrompts_HaveNonEmptyTemplates(t *testing.T) {
	for name, tmpl := range PresetPrompts {
		if strings.TrimSpace(tmpl) == "" {
			t.Errorf("PresetPrompts[%q] template is empty", name)
		}
	}
}

func TestPresetPrompts_DoNotContainCodePlaceholder(t *testing.T) {
	// Preset prompts are instruction-only; the frontend handles context injection.
	for name, tmpl := range PresetPrompts {
		if strings.Contains(tmpl, "{{code}}") {
			t.Errorf("PresetPrompts[%q] should not contain {{code}} placeholder", name)
		}
	}
}

func TestGetPresetPrompt_ReturnsTemplate(t *testing.T) {
	got, err := GetPresetPrompt("explain")
	if err != nil {
		t.Fatalf("GetPresetPrompt failed: %v", err)
	}
	if strings.TrimSpace(got) == "" {
		t.Errorf("explain template should not be empty, got: %q", got)
	}
}

func TestGetPresetPrompt_NewPresets(t *testing.T) {
	newPresets := []string{"optimize", "review", "security", "commit_message"}
	for _, name := range newPresets {
		got, err := GetPresetPrompt(name)
		if err != nil {
			t.Errorf("GetPresetPrompt(%q) failed: %v", name, err)
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("%q template should not be empty, got: %q", name, got)
		}
	}
}

func TestGetPresetPrompt_UnknownReturnsError(t *testing.T) {
	_, err := GetPresetPrompt("nonexistent")
	if err == nil {
		t.Error("expected error for unknown preset name")
	}
}

func TestBuildPrompt_ReplacesCodePlaceholder(t *testing.T) {
	got := BuildPrompt("Explain this:\n{{code}}", "func foo() {}")
	if !strings.Contains(got, "func foo() {}") {
		t.Errorf("expected code in result, got: %q", got)
	}
	if strings.Contains(got, "{{code}}") {
		t.Errorf("placeholder should be replaced, got: %q", got)
	}
}

func TestBuildPrompt_ReplacesLanguagePlaceholder(t *testing.T) {
	got := BuildPrompt("Language: {{language}}\n{{code}}", "x")
	if !strings.Contains(got, "Language: text") {
		t.Errorf("{{language}} placeholder should be replaced with 'text', got: %q", got)
	}
}

func TestBuildPromptWithMeta_ReplacesAllPlaceholders(t *testing.T) {
	got := BuildPromptWithMeta("Lang: {{language}}, Path: {{filepath}}\n{{code}}", "code", "python", "main.py")
	if !strings.Contains(got, "Lang: python") {
		t.Errorf("expected language 'python', got: %q", got)
	}
	if !strings.Contains(got, "Path: main.py") {
		t.Errorf("expected filepath 'main.py', got: %q", got)
	}
	if !strings.Contains(got, "code") {
		t.Errorf("expected code in result, got: %q", got)
	}
}

func TestBuildPromptWithMeta_DefaultsEmptyLanguage(t *testing.T) {
	got := BuildPromptWithMeta("{{language}}", "x", "", "")
	if !strings.Contains(got, "text") {
		t.Errorf("expected default language 'text', got: %q", got)
	}
}

func TestListPresetPrompts_ReturnsOrderedSlice(t *testing.T) {
	result := ListPresetPrompts()
	if len(result) != len(PresetOrder) {
		t.Errorf("expected %d presets, got %d", len(PresetOrder), len(result))
	}
	for i, expected := range PresetOrder {
		if result[i].Name != expected {
			t.Errorf("preset at index %d: expected %q, got %q", i, expected, result[i].Name)
		}
	}
}

func TestListPresetPrompts_EachHasLabelAndDescription(t *testing.T) {
	result := ListPresetPrompts()
	for _, meta := range result {
		if strings.TrimSpace(meta.Label) == "" {
			t.Errorf("preset %q has empty Label", meta.Name)
		}
		if strings.TrimSpace(meta.Description) == "" {
			t.Errorf("preset %q has empty Description", meta.Name)
		}
	}
}

func TestAIService_GetDefaultSystemPrompt(t *testing.T) {
	svc := NewAIService()
	got := svc.GetDefaultSystemPrompt()
	if got != DefaultSystemPrompt {
		t.Error("GetDefaultSystemPrompt should return DefaultSystemPrompt")
	}
}

func TestAIService_GetPresetPrompt(t *testing.T) {
	svc := NewAIService()
	got, err := svc.GetPresetPrompt("fix")
	if err != nil {
		t.Fatalf("GetPresetPrompt failed: %v", err)
	}
	if strings.TrimSpace(got) == "" {
		t.Error("fix preset should not be empty")
	}
}

func TestAIService_ListPresets(t *testing.T) {
	svc := NewAIService()
	got := svc.ListPresets()
	if len(got) != len(PresetOrder) {
		t.Errorf("expected %d presets, got %d", len(PresetOrder), len(got))
	}
}

func TestAgentSystemPrompt_IsNotEmpty(t *testing.T) {
	if strings.TrimSpace(AgentSystemPrompt) == "" {
		t.Error("AgentSystemPrompt must not be empty")
	}
}

func TestAgentSystemPrompt_MentionsAgentOrAutonomous(t *testing.T) {
	lower := strings.ToLower(AgentSystemPrompt)
	if !strings.Contains(lower, "agent") && !strings.Contains(lower, "autonomous") {
		t.Error("AgentSystemPrompt should mention agent/autonomous role")
	}
}

func TestAgentSystemPrompt_MentionsSafetyAndApproval(t *testing.T) {
	lower := strings.ToLower(AgentSystemPrompt)
	if !strings.Contains(lower, "approval") {
		t.Error("AgentSystemPrompt should mention user approval for actions")
	}
	if !strings.Contains(lower, "destructive") {
		t.Error("AgentSystemPrompt should mention destructive operations")
	}
}

func TestAIService_GetAgentSystemPrompt(t *testing.T) {
	svc := NewAIService()
	got := svc.GetAgentSystemPrompt()
	if got != AgentSystemPrompt {
		t.Error("GetAgentSystemPrompt should return AgentSystemPrompt")
	}
}

func TestAIService_GetSystemPrompt_Default(t *testing.T) {
	svc := NewAIService()
	got := svc.GetSystemPrompt("default")
	if got != DefaultSystemPrompt {
		t.Error("GetSystemPrompt(\"default\") should return DefaultSystemPrompt")
	}
}

func TestAIService_GetSystemPrompt_Agent(t *testing.T) {
	svc := NewAIService()
	got := svc.GetSystemPrompt("agent")
	if got != AgentSystemPrompt {
		t.Error("GetSystemPrompt(\"agent\") should return AgentSystemPrompt")
	}
}

func TestAIService_GetSystemPrompt_UnknownReturnsDefault(t *testing.T) {
	svc := NewAIService()
	got := svc.GetSystemPrompt("nonexistent")
	if got != DefaultSystemPrompt {
		t.Error("GetSystemPrompt with unknown name should return DefaultSystemPrompt")
	}
}

// --- Plan 52: AI prompt system enhancement tests ---

func TestDefaultSystemPrompt_MentionsApplyFriendlyCodeBlocks(t *testing.T) {
	lower := strings.ToLower(DefaultSystemPrompt)
	// The new "Apply-Friendly Code Blocks" section should guide the model
	// to produce code blocks that apply cleanly via the diff modal.
	if !strings.Contains(lower, "apply") {
		t.Error("DefaultSystemPrompt should mention apply-friendly code blocks")
	}
	if !strings.Contains(lower, "diff") {
		t.Error("DefaultSystemPrompt should warn against diff syntax")
	}
	if !strings.Contains(lower, "complete") {
		t.Error("DefaultSystemPrompt should ask for complete file/function content")
	}
}

func TestDefaultSystemPrompt_MentionsNoLineNumbers(t *testing.T) {
	lower := strings.ToLower(DefaultSystemPrompt)
	if !strings.Contains(lower, "line number") {
		t.Error("DefaultSystemPrompt should instruct against line-number prefixes")
	}
}

func TestAgentSystemPrompt_MentionsObservationFeedback(t *testing.T) {
	lower := strings.ToLower(AgentSystemPrompt)
	if !strings.Contains(lower, "observation") {
		t.Error("AgentSystemPrompt should mention [Observation] feedback format")
	}
	if !strings.Contains(lower, "rejection") {
		t.Error("AgentSystemPrompt should mention [Rejection] feedback format")
	}
}

func TestAgentSystemPrompt_MentionsIterationBudget(t *testing.T) {
	lower := strings.ToLower(AgentSystemPrompt)
	if !strings.Contains(lower, "budget") && !strings.Contains(lower, "iteration") {
		t.Error("AgentSystemPrompt should mention the iteration/tool-call budget")
	}
}

func TestConversationTitlePrompt_IsNotEmpty(t *testing.T) {
	if strings.TrimSpace(ConversationTitlePrompt) == "" {
		t.Error("ConversationTitlePrompt must not be empty")
	}
}

func TestConversationTitlePrompt_HasPlaceholder(t *testing.T) {
	if !strings.Contains(ConversationTitlePrompt, "{{first_message}}") {
		t.Error("ConversationTitlePrompt must contain {{first_message}} placeholder")
	}
}

func TestConversationTitlePrompt_MentionsWordCount(t *testing.T) {
	lower := strings.ToLower(ConversationTitlePrompt)
	// The prompt should constrain the title length so titles are consistent.
	if !strings.Contains(lower, "4") || !strings.Contains(lower, "8") {
		t.Error("ConversationTitlePrompt should specify a 4-8 word length")
	}
}

func TestConversationTitlePrompt_RequestsOnlyTitle(t *testing.T) {
	lower := strings.ToLower(ConversationTitlePrompt)
	if !strings.Contains(lower, "only") {
		t.Error("ConversationTitlePrompt should ask for ONLY the title text")
	}
}

func TestInlineCompletionSystemPrompt_IsNotEmpty(t *testing.T) {
	if strings.TrimSpace(InlineCompletionSystemPrompt) == "" {
		t.Error("InlineCompletionSystemPrompt must not be empty")
	}
}

func TestInlineCompletionSystemPrompt_HasLanguagePlaceholder(t *testing.T) {
	if !strings.Contains(InlineCompletionSystemPrompt, "{{language}}") {
		t.Error("InlineCompletionSystemPrompt must contain {{language}} placeholder")
	}
}

func TestInlineCompletionSystemPrompt_ForbidsRepetition(t *testing.T) {
	lower := strings.ToLower(InlineCompletionSystemPrompt)
	if !strings.Contains(lower, "repeat") {
		t.Error("InlineCompletionSystemPrompt should forbid repeating code before the cursor")
	}
}

func TestInlineCompletionSystemPrompt_ForbidsImports(t *testing.T) {
	lower := strings.ToLower(InlineCompletionSystemPrompt)
	if !strings.Contains(lower, "import") {
		t.Error("InlineCompletionSystemPrompt should mention import statements")
	}
}

func TestCompleteSystemPrompt_ReplacesLanguagePlaceholder(t *testing.T) {
	a := &AIService{}
	got := a.completeSystemPrompt("python")
	if !strings.Contains(got, "python") {
		t.Errorf("expected language 'python' in prompt, got: %q", got)
	}
	if strings.Contains(got, "{{language}}") {
		t.Errorf("{{language}} placeholder should be replaced, got: %q", got)
	}
}

func TestCompleteSystemPrompt_DefaultsEmptyLanguageToText(t *testing.T) {
	a := &AIService{}
	got := a.completeSystemPrompt("")
	if !strings.Contains(got, "text") {
		t.Errorf("expected default language 'text', got: %q", got)
	}
}

func TestCompleteSystemPrompt_UsesConstTemplate(t *testing.T) {
	// The refactored function should produce output derived from the const,
	// not a hardcoded string. Verify the key rule phrases are present.
	a := &AIService{}
	got := a.completeSystemPrompt("go")
	lower := strings.ToLower(got)
	if !strings.Contains(lower, "only the text") {
		t.Error("completeSystemPrompt should include the 'only the text' rule from the const")
	}
	if !strings.Contains(lower, "indentation") {
		t.Error("completeSystemPrompt should include the indentation rule from the const")
	}
}

func TestAIService_GetConversationTitlePrompt(t *testing.T) {
	svc := NewAIService()
	got := svc.GetConversationTitlePrompt()
	if got != ConversationTitlePrompt {
		t.Error("GetConversationTitlePrompt should return ConversationTitlePrompt")
	}
}

func TestAIService_GenerateTitleWithAI_NoAPIKey_ReturnsFallback(t *testing.T) {
	svc := NewAIService()
	// No API key configured → should return the legacy fallback without error.
	got, err := svc.GenerateTitleWithAI("Help me refactor the auth middleware")
	if err != nil {
		t.Fatalf("expected no error when API key is missing, got: %v", err)
	}
	if got == "" {
		t.Error("fallback title should not be empty")
	}
	// Fallback is the truncated first message.
	if !strings.Contains(got, "refactor") {
		t.Errorf("fallback title should contain the message text, got: %q", got)
	}
}

func TestAIService_GenerateTitleWithAI_EmptyMessage_ReturnsFallback(t *testing.T) {
	svc := NewAIService()
	got, err := svc.GenerateTitleWithAI("")
	if err != nil {
		t.Fatalf("expected no error for empty message, got: %v", err)
	}
	if got != "(new conversation)" {
		t.Errorf("expected '(new conversation)' fallback for empty input, got: %q", got)
	}
}

// --- Plan 54: GetEffective* methods ---

func TestAIService_GetInlineCompletionSystemPrompt(t *testing.T) {
	svc := NewAIService()
	got := svc.GetInlineCompletionSystemPrompt()
	if got != InlineCompletionSystemPrompt {
		t.Error("GetInlineCompletionSystemPrompt should return InlineCompletionSystemPrompt")
	}
}

func TestAIService_GetEffectiveAgentSystemPrompt_DefaultsToBuiltin(t *testing.T) {
	svc := NewAIService()
	got := svc.GetEffectiveAgentSystemPrompt()
	if got != AgentSystemPrompt {
		t.Error("GetEffectiveAgentSystemPrompt should return AgentSystemPrompt when no override is set")
	}
}

func TestAIService_GetEffectiveAgentSystemPrompt_UsesOverride(t *testing.T) {
	svc := &AIService{config: AIConfig{AgentSystemPrompt: "CUSTOM AGENT PROMPT"}}
	got := svc.GetEffectiveAgentSystemPrompt()
	if got != "CUSTOM AGENT PROMPT" {
		t.Errorf("expected override 'CUSTOM AGENT PROMPT', got: %q", got)
	}
}

func TestAIService_GetEffectiveAgentSystemPrompt_EmptyOverrideFallsBack(t *testing.T) {
	svc := &AIService{config: AIConfig{AgentSystemPrompt: ""}}
	got := svc.GetEffectiveAgentSystemPrompt()
	if got != AgentSystemPrompt {
		t.Error("empty AgentSystemPrompt should fall back to the built-in const")
	}
}

func TestAIService_GetEffectiveConversationTitlePrompt_DefaultsToBuiltin(t *testing.T) {
	svc := NewAIService()
	got := svc.GetEffectiveConversationTitlePrompt()
	if got != ConversationTitlePrompt {
		t.Error("GetEffectiveConversationTitlePrompt should return ConversationTitlePrompt when no override is set")
	}
}

func TestAIService_GetEffectiveConversationTitlePrompt_UsesOverride(t *testing.T) {
	svc := &AIService{config: AIConfig{ConversationTitlePrompt: "CUSTOM TITLE PROMPT {{first_message}}"}}
	got := svc.GetEffectiveConversationTitlePrompt()
	if got != "CUSTOM TITLE PROMPT {{first_message}}" {
		t.Errorf("expected override, got: %q", got)
	}
}

func TestAIService_GetEffectiveInlineCompletionPrompt_DefaultsToBuiltin(t *testing.T) {
	svc := NewAIService()
	got := svc.GetEffectiveInlineCompletionPrompt()
	if got != InlineCompletionSystemPrompt {
		t.Error("GetEffectiveInlineCompletionPrompt should return InlineCompletionSystemPrompt when no override is set")
	}
}

func TestAIService_GetEffectiveInlineCompletionPrompt_UsesOverride(t *testing.T) {
	svc := &AIService{config: AIConfig{InlineCompletionPrompt: "CUSTOM INLINE {{language}}"}}
	got := svc.GetEffectiveInlineCompletionPrompt()
	if got != "CUSTOM INLINE {{language}}" {
		t.Errorf("expected override, got: %q", got)
	}
}

func TestAIService_CompleteSystemPrompt_UsesOverrideWhenSet(t *testing.T) {
	svc := &AIService{config: AIConfig{InlineCompletionPrompt: "Complete this {{language}} code"}}
	got := svc.completeSystemPrompt("rust")
	if !strings.Contains(got, "Complete this rust code") {
		t.Errorf("expected override with language substituted, got: %q", got)
	}
	// The built-in phrase should NOT be present when an override is set.
	if strings.Contains(strings.ToLower(got), "inline code completion engine") {
		t.Error("override should fully replace the built-in prompt, not append to it")
	}
}

func TestAIService_CompleteSystemPrompt_FallsBackWhenNoOverride(t *testing.T) {
	svc := NewAIService()
	got := svc.completeSystemPrompt("go")
	if !strings.Contains(strings.ToLower(got), "inline code completion engine") {
		t.Error("expected the built-in prompt when no override is set")
	}
	if !strings.Contains(got, "go") {
		t.Error("expected language placeholder to be substituted")
	}
}

// --- Plan 106: N-66 injection guardrails + N-70 structure/few-shot ---

// N-66: DefaultSystemPrompt must instruct the model to treat external content
// as untrusted data, not instructions.
func TestDefaultSystemPrompt_N66_HasInjectionGuardrails(t *testing.T) {
	lower := strings.ToLower(DefaultSystemPrompt)
	if !strings.Contains(lower, "injection") {
		t.Error("N-66: DefaultSystemPrompt should mention 'injection'")
	}
	if !strings.Contains(lower, "untrusted") {
		t.Error("N-66: DefaultSystemPrompt should mention 'untrusted' data")
	}
	if !strings.Contains(lower, "ignore previous instructions") {
		t.Error("N-66: DefaultSystemPrompt should warn about 'ignore previous instructions' attacks")
	}
}

// N-66: AgentSystemPrompt must have stronger injection guardrails since it
// consumes tool observations (file contents, command output).
func TestAgentSystemPrompt_N66_HasInjectionGuardrails(t *testing.T) {
	lower := strings.ToLower(AgentSystemPrompt)
	if !strings.Contains(lower, "untrusted") {
		t.Error("N-66: AgentSystemPrompt should mark tool observations as untrusted")
	}
	if !strings.Contains(lower, "exfiltrate") {
		t.Error("N-66: AgentSystemPrompt should forbid exfiltrating secrets/system prompt")
	}
}

// N-70: AgentSystemPrompt should include few-shot examples for tool use
// (read, write, run, search) so the model emits the correct format.
func TestAgentSystemPrompt_N70_HasFewShotToolUseExamples(t *testing.T) {
	lower := strings.ToLower(AgentSystemPrompt)
	if !strings.Contains(lower, "example 1") || !strings.Contains(lower, "example 2") {
		t.Error("N-70: AgentSystemPrompt should have at least 2 few-shot examples")
	}
	// Each tool verb should appear in an example.
	for _, verb := range []string{"read:", "write:", "run:", "search:"} {
		if !strings.Contains(lower, verb) {
			t.Errorf("N-70: AgentSystemPrompt should include example with %q", verb)
		}
	}
}

// N-70: AgentSystemPrompt should instruct project context awareness.
func TestAgentSystemPrompt_N70_HasProjectContextAwareness(t *testing.T) {
	lower := strings.ToLower(AgentSystemPrompt)
	if !strings.Contains(lower, "package.json") && !strings.Contains(lower, "go.mod") {
		t.Error("N-70: AgentSystemPrompt should mention project manifest files")
	}
	if !strings.Contains(lower, "convention") {
		t.Error("N-70: AgentSystemPrompt should mention matching project conventions")
	}
}

// N-70: DefaultSystemPrompt should include a concrete few-shot example
// showing the apply-friendly code block format.
func TestDefaultSystemPrompt_N70_HasApplyFriendlyExample(t *testing.T) {
	lower := strings.ToLower(DefaultSystemPrompt)
	if !strings.Contains(lower, "example of a well-formed") {
		t.Error("N-70: DefaultSystemPrompt should include a concrete code block example")
	}
}
