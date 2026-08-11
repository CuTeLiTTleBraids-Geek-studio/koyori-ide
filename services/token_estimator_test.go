package services

import (
	"strings"
	"testing"
)

func TestEstimateTokens_EmptyString(t *testing.T) {
	if got := estimateTokens(""); got != 0 {
		t.Errorf("estimateTokens(\"\") = %d, want 0", got)
	}
}

func TestEstimateTokens_EnglishText(t *testing.T) {
	// ~4 chars/token for English.
	s := "Hello, world! This is a test."
	got := estimateTokens(s)
	// 28 chars / 4 = 7 tokens. Allow ±2 for rounding.
	if got < 5 || got > 9 {
		t.Errorf("estimateTokens(English) = %d, want ~7 (got=%d for %q)", got, got, s)
	}
}

func TestEstimateTokens_CJKText(t *testing.T) {
	// ~2 chars/token for CJK.
	s := "你好世界，这是一个测试。" // 12 CJK chars
	got := estimateTokens(s)
	// 12/2 = 6 tokens. Allow ±2.
	if got < 4 || got > 8 {
		t.Errorf("estimateTokens(CJK) = %d, want ~6", got)
	}
}

func TestEstimateTokens_MixedText(t *testing.T) {
	// Mix of English and CJK.
	s := "Hello 你好 World 世界"
	got := estimateTokens(s)
	if got <= 0 {
		t.Errorf("estimateTokens(mixed) = %d, want > 0", got)
	}
}

func TestEstimateTokens_SingleChar(t *testing.T) {
	if got := estimateTokens("a"); got != 1 {
		t.Errorf("estimateTokens(\"a\") = %d, want 1", got)
	}
}

func TestEstimateMessagesTokens_SumsAllMessages(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hello there!"},
	}
	got := estimateMessagesTokens(msgs)
	// "You are helpful." = ~4 tokens + 3 overhead = 7
	// "Hello there!" = ~3 tokens + 3 overhead = 6
	// Total ~13. Allow range.
	if got < 10 || got > 18 {
		t.Errorf("estimateMessagesTokens = %d, want ~13", got)
	}
}

func TestEstimateMessagesTokens_EmptySlice(t *testing.T) {
	if got := estimateMessagesTokens(nil); got != 0 {
		t.Errorf("estimateMessagesTokens(nil) = %d, want 0", got)
	}
}

// N-61: truncateToTokenBudget returns messages unchanged when under budget.
func TestTruncateToTokenBudget_NoTruncationWhenUnderBudget(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	result := truncateToTokenBudget(msgs, 1000)
	if len(result) != 3 {
		t.Errorf("expected 3 messages (no truncation), got %d", len(result))
	}
}

// N-61: truncateToTokenBudget returns messages unchanged when budget <= 0.
func TestTruncateToTokenBudget_NoTruncationWhenBudgetZero(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
	}
	result := truncateToTokenBudget(msgs, 0)
	if len(result) != 2 {
		t.Errorf("expected 2 messages (budget=0 = no truncation), got %d", len(result))
	}
}

// N-61: truncateToTokenBudget returns messages unchanged when len <= 2.
func TestTruncateToTokenBudget_NoTruncationForShortConversations(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
	}
	result := truncateToTokenBudget(msgs, 1) // tiny budget
	if len(result) != 2 {
		t.Errorf("expected 2 messages (short conversation), got %d", len(result))
	}
}

// N-61: truncation preserves system prompt + first user + recent messages.
func TestTruncateToTokenBudget_PreservesHeadAndTail(t *testing.T) {
	// Build a conversation with large middle messages that exceed the budget.
	msgs := []ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: strings.Repeat("a", 200)}, // ~53 tokens
		{Role: "user", Content: strings.Repeat("b", 200)},      // ~53 tokens
		{Role: "assistant", Content: strings.Repeat("c", 200)}, // ~53 tokens
		{Role: "user", Content: "recent"},
	}
	// Budget 70: head (~8) + placeholder (20) + tail ("recent" ~4) = 32 < 70.
	// The large middle messages (~53 each) won't fit in the remaining budget.
	result := truncateToTokenBudget(msgs, 70)
	if len(result) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(result))
	}
	// First message must be the system prompt.
	if result[0].Role != "system" || result[0].Content != "sys" {
		t.Errorf("first message should be system prompt, got %v", result[0])
	}
	// Second message must be the first user message.
	if result[1].Role != "user" || result[1].Content != "first" {
		t.Errorf("second message should be first user, got %v", result[1])
	}
	// A placeholder must be present.
	hasPlaceholder := false
	for _, m := range result {
		if strings.Contains(m.Content, "truncated") {
			hasPlaceholder = true
			break
		}
	}
	if !hasPlaceholder {
		t.Errorf("expected a truncation placeholder, got %v", result)
	}
	// Last message must be the last original message.
	if result[len(result)-1].Content != "recent" {
		t.Errorf("last message should be 'recent', got %v", result[len(result)-1])
	}
}

// N-61: truncation inserts a placeholder with the dropped count.
func TestTruncateToTokenBudget_PlaceholderMentionsDroppedCount(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: strings.Repeat("x", 100)}, // large
		{Role: "user", Content: strings.Repeat("y", 100)},
		{Role: "assistant", Content: strings.Repeat("z", 100)},
		{Role: "user", Content: "last"},
	}
	result := truncateToTokenBudget(msgs, 80) // small budget
	// Find the placeholder.
	found := false
	for _, m := range result {
		if strings.Contains(m.Content, "messages were truncated") {
			found = true
			// Should mention a positive count.
			if !strings.Contains(m.Content, "1 ") && !strings.Contains(m.Content, "2 ") && !strings.Contains(m.Content, "3 ") {
				t.Errorf("placeholder should mention dropped count: %s", m.Content)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected truncation placeholder in result: %v", result)
	}
}

// N-61: when head alone exceeds budget, returns head only.
func TestTruncateToTokenBudget_HeadExceedsBudget(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: strings.Repeat("s", 1000)}, // very large
		{Role: "user", Content: strings.Repeat("u", 1000)},   // very large
		{Role: "assistant", Content: "small"},
		{Role: "user", Content: "small"},
	}
	result := truncateToTokenBudget(msgs, 10) // tiny budget
	// Should return at most the head (2 messages).
	if len(result) > 2 {
		t.Errorf("expected at most 2 messages (head only), got %d", len(result))
	}
}

// N-61: prepareMessages applies both system prompt and truncation.
func TestAIService_N61_PrepareMessages_AppliesTruncation(t *testing.T) {
	a := NewAIService()
	a.config.SystemPrompt = "system"
	a.config.ContextWindow = 50 // small budget
	// Build messages that exceed the budget.
	msgs := []ChatMessage{
		{Role: "user", Content: "first message"},
		{Role: "assistant", Content: strings.Repeat("x", 200)},
		{Role: "user", Content: strings.Repeat("y", 200)},
		{Role: "assistant", Content: "recent"},
	}
	result := a.prepareMessages(msgs)
	// prepareMessages should prepend system prompt, so result[0] is system.
	if result[0].Role != "system" {
		t.Errorf("expected first message to be system, got %v", result[0])
	}
	// Should have fewer messages than the original + system prompt (6).
	if len(result) >= len(msgs)+1 {
		t.Errorf("expected truncation to reduce message count, got %d (original+system=%d)", len(result), len(msgs)+1)
	}
}

// N-61: prepareMessages does not truncate when budget is large.
func TestAIService_N61_PrepareMessages_NoTruncationWhenBudgetLarge(t *testing.T) {
	a := NewAIService()
	a.config.SystemPrompt = "system"
	a.config.ContextWindow = 100000 // large budget
	msgs := []ChatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "second"},
		{Role: "user", Content: "third"},
	}
	result := a.prepareMessages(msgs)
	// Should have all 3 original + 1 system prompt = 4.
	if len(result) != 4 {
		t.Errorf("expected 4 messages (no truncation), got %d", len(result))
	}
}
