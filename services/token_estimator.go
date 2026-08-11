package services

import "fmt"

// N-61: Context window management. Without token counting, long conversations
// exceed the model's context window, triggering 4xx errors and inflating cost.
// This file provides a lightweight token estimator and a truncation helper
// that preserves the system prompt + first user message + most-recent messages.

// estimateTokens returns a rough token count for a string using a character
// heuristic. English text averages ~4 chars/token; CJK text ~2 chars/token.
// We blend the two estimates based on the CJK character ratio. This is
// intentionally approximate — exact tokenization requires the model's
// tokenizer (e.g., tiktoken), which would add a heavy dependency for marginal
// gain in truncation accuracy.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	runes := []rune(s)
	totalChars := len(runes)
	cjkCount := 0
	for _, r := range runes {
		if isCJK(r) {
			cjkCount++
		}
	}
	cjkTokens := cjkCount / 2 // CJK: ~2 chars/token
	asciiTokens := (totalChars - cjkCount) / 4
	tokens := cjkTokens + asciiTokens
	if tokens == 0 && totalChars > 0 {
		tokens = 1
	}
	return tokens
}

// isCJK returns true if the rune is in a common CJK Unicode block.
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3040 && r <= 0x30FF) || // Hiragana, Katakana
		(r >= 0xAC00 && r <= 0xD7AF) || // Hangul
		(r >= 0xFF00 && r <= 0xFFEF) // CJK Compatibility Forms
}

// estimateMessagesTokens sums the token estimates across all messages,
// including a small per-message overhead (3 tokens for role + delimiters,
// approximating OpenAI's chat format overhead).
func estimateMessagesTokens(messages []ChatMessage) int {
	total := 0
	for _, m := range messages {
		total += estimateTokens(m.Content) + 3
	}
	return total
}

// truncateToTokenBudget keeps the system prompt (first message if its role is
// "system"), the first user message, and as many of the most recent messages
// as fit within the budget. If truncation occurs, a placeholder system
// message is inserted indicating how many messages were dropped, so the model
// knows context is missing.
//
// If budget <= 0, no truncation is applied (returns messages unchanged).
// If the head (system + first user) alone exceeds the budget, the head is
// returned without the tail — this is a degraded state but better than
// failing the request entirely.
func truncateToTokenBudget(messages []ChatMessage, budget int) []ChatMessage {
	if budget <= 0 || len(messages) <= 2 {
		return messages
	}
	totalTokens := estimateMessagesTokens(messages)
	if totalTokens <= budget {
		return messages
	}

	// Determine the head: system prompt (if present) + first user message.
	headCount := 1
	if len(messages) > 1 && messages[0].Role == "system" {
		headCount = 2
	}
	head := messages[:headCount]
	headTokens := estimateMessagesTokens(head)

	// Reserve tokens for the truncation placeholder (~20 tokens).
	const placeholderReserve = 20
	remainingBudget := budget - headTokens - placeholderReserve
	if remainingBudget <= 0 {
		// Head alone exceeds budget; return head only (degraded but safe).
		return head
	}

	// Walk backwards from the end, adding messages until budget is exhausted.
	var tail []ChatMessage
	tailTokens := 0
	for i := len(messages) - 1; i >= headCount; i-- {
		msgTokens := estimateTokens(messages[i].Content) + 3
		if tailTokens+msgTokens > remainingBudget {
			break
		}
		tail = append([]ChatMessage{messages[i]}, tail...)
		tailTokens += msgTokens
	}

	droppedCount := len(messages) - headCount - len(tail)
	if droppedCount <= 0 {
		return messages
	}

	placeholder := ChatMessage{
		Role: "system",
		Content: fmt.Sprintf(
			"[Note: %d earlier messages were truncated to fit the context window. Ask the user if you need older context.]",
			droppedCount,
		),
	}

	result := make([]ChatMessage, 0, len(head)+1+len(tail))
	result = append(result, head...)
	result = append(result, placeholder)
	result = append(result, tail...)
	return result
}
