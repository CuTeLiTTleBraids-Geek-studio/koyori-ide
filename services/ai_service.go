package services

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	aiUserAgent                      = "koyori-ide/1.0 (Wails3 Desktop IDE)"
	maxAINonStreamingResponseBytes   = 8 << 20
	maxAIProviderReportedTotalTokens = int64(1_000_000_000)
)

// noRedirectPolicy prevents the HTTP client from following redirects.
func noRedirectPolicy(req *http.Request, via []*http.Request) error {
	return http.ErrUseLastResponse
}

// aiTransport re-validates the resolved IP at dial time (P13-G03 / M6).
// Loopback http remains allowed for local models; metadata/private
// non-loopback addresses are refused even after DNS rebinding.
var aiTransport = NewAISSRFSafeTransport()

// aiHTTPClient has a total timeout for non-streaming requests.
var aiHTTPClient = &http.Client{
	Timeout:       120 * time.Second,
	CheckRedirect: noRedirectPolicy,
	Transport:     aiTransport,
}

// aiStreamHTTPClient has no total timeout (streams can be long),
// but the shared transport enforces connection/header timeouts.
var aiStreamHTTPClient = &http.Client{
	CheckRedirect: noRedirectPolicy,
	Transport:     aiTransport,
}

// aiErrorResponse represents a structured error from an OpenAI-compatible API.
type aiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

type aiHTTPStatusError struct {
	statusCode int
	message    string
}

func (e *aiHTTPStatusError) Error() string {
	if e == nil {
		return "AI API returned an unknown HTTP error"
	}
	return e.message
}

// setCommonHeaders sets headers shared by all AI requests.
func setCommonHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", aiUserAgent)
}

// isAnthropicProtocol returns true when cfg.Protocol is "anthropic".
func isAnthropicProtocol(cfg AIConfig) bool {
	return cfg.Protocol == "anthropic"
}

// effectiveTemperature returns the clamped temperature for chat requests.
// 0 (or negative) defaults to 0.7; values above 2 are clamped to 2.
func effectiveTemperature(cfg AIConfig) float64 {
	t := cfg.Temperature
	if t <= 0 {
		t = 0.7
	}
	if t > 2 {
		t = 2
	}
	return t
}

const (
	reasoningEffortLow    = "low"
	reasoningEffortMedium = "medium"
	reasoningEffortHigh   = "high"
)

func normalizeReasoningEffort(value string) (string, error) {
	effort := strings.ToLower(strings.TrimSpace(value))
	switch effort {
	case "", reasoningEffortLow, reasoningEffortMedium, reasoningEffortHigh:
		return effort, nil
	default:
		return "", fmt.Errorf("%w: invalid reasoning effort %q", ErrInvalidInput, value)
	}
}

const (
	reasoningCapabilitySupported   = "supported"
	reasoningCapabilityUnsupported = "unsupported"
	reasoningCapabilityUnknown     = "unknown"
)

// ReasoningCapability describes whether the selected provider/model has a
// verified request mapping for the provider-agnostic reasoning setting.
// Status is deliberately explicit: unknown providers/models must not look
// supported merely because their protocol resembles a known provider.
type ReasoningCapability struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Protocol     string `json:"protocol"`
	Status       string `json:"status"`
	RequestField string `json:"requestField,omitempty"`
}

func reasoningCapabilityFor(provider, model, protocol string) ReasoningCapability {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "openai"
	}
	capability := ReasoningCapability{
		Provider: provider,
		Model:    model,
		Protocol: protocol,
		Status:   reasoningCapabilityUnknown,
	}
	if provider == "" || model == "" {
		return capability
	}

	switch protocol {
	case "anthropic":
		// Anthropic extended thinking is supported by Claude 3.7+ and Claude 4.
		if provider != "anthropic" {
			return capability
		}
		if strings.Contains(model, "claude-3-7") || strings.Contains(model, "claude-4") ||
			strings.Contains(model, "claude-sonnet-4") || strings.Contains(model, "claude-opus-4") {
			capability.Status = reasoningCapabilitySupported
			capability.RequestField = "thinking.budget_tokens"
			return capability
		}
		if strings.Contains(model, "claude-") {
			capability.Status = reasoningCapabilityUnsupported
		}
	case "openai":
		if provider == "gemini" || provider == "ollama" || provider == "lmstudio" {
			capability.Status = reasoningCapabilityUnsupported
			return capability
		}
		if provider != "openai" && provider != "azure" {
			return capability
		}
		if strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "o1") ||
			strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4") {
			capability.Status = reasoningCapabilitySupported
			capability.RequestField = "reasoning_effort"
			return capability
		}
		if strings.HasPrefix(model, "gpt-") {
			capability.Status = reasoningCapabilityUnsupported
		}
	}
	return capability
}

func validateReasoningCapability(provider, model, protocol, effort string) error {
	normalized, err := normalizeReasoningEffort(effort)
	if err != nil {
		return err
	}
	if normalized == "" || strings.TrimSpace(provider) == "" {
		return nil
	}
	capability := reasoningCapabilityFor(provider, model, protocol)
	if capability.Status != reasoningCapabilitySupported {
		return fmt.Errorf("%w: reasoning effort %q is %s for provider %q model %q", ErrNotAllowed, normalized, capability.Status, provider, model)
	}
	return nil
}

func reasoningBudgetTokens(effort string) (int, error) {
	normalized, err := normalizeReasoningEffort(effort)
	if err != nil {
		return 0, err
	}
	switch normalized {
	case reasoningEffortLow:
		return 1024, nil
	case reasoningEffortMedium:
		return 2048, nil
	case reasoningEffortHigh:
		return 4096, nil
	default:
		return 0, nil
	}
}

func applyReasoningRequestConfig(body map[string]interface{}, cfg AIConfig) error {
	effort, err := normalizeReasoningEffort(cfg.ReasoningEffort)
	if err != nil {
		return err
	}
	if effort == "" {
		return nil
	}
	if err := validateReasoningCapability(cfg.Provider, cfg.Model, cfg.Protocol, effort); err != nil {
		return err
	}
	if isAnthropicProtocol(cfg) {
		budget, err := reasoningBudgetTokens(effort)
		if err != nil {
			return err
		}
		body["thinking"] = map[string]interface{}{"type": "enabled", "budget_tokens": budget}
		delete(body, "temperature")
		return nil
	}
	body["reasoning_effort"] = effort
	return nil
}

// setProtocolHeaders sets auth headers based on the configured protocol.
// Anthropic uses x-api-key + anthropic-version; OpenAI (default) uses Bearer.
func setProtocolHeaders(req *http.Request, cfg AIConfig) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", aiUserAgent)
	if isAnthropicProtocol(cfg) {
		req.Header.Set("x-api-key", cfg.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
}

// splitSystemPrompt separates system messages from the conversation messages.
// Anthropic expects the system prompt as a top-level field, not inside the
// messages array. Multiple system messages are concatenated with newlines.
// Returns (systemPrompt, chatMessages).
func splitSystemPrompt(messages []ChatMessage) (string, []ChatMessage) {
	var systemParts []string
	chatMessages := make([]ChatMessage, 0, len(messages))
	for _, m := range messages {
		if m.Role == "system" {
			if m.Content != "" {
				systemParts = append(systemParts, m.Content)
			}
			continue
		}
		chatMessages = append(chatMessages, m)
	}
	return strings.Join(systemParts, "\n"), chatMessages
}

// parseAIError extracts a human-readable error message from a non-2xx response.
// parseAIError reads the error response body capped at 64 KiB (G-SEC-08 / M-2)
// to prevent a malicious provider from exhausting memory with a huge body.
func parseAIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var aiErr aiErrorResponse
	if err := json.Unmarshal(body, &aiErr); err == nil && aiErr.Error.Message != "" {
		return &aiHTTPStatusError{
			statusCode: resp.StatusCode,
			message:    fmt.Sprintf("AI API error (status %d): %s", resp.StatusCode, aiErr.Error.Message),
		}
	}
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	if resp.StatusCode == http.StatusNotFound {
		return &aiHTTPStatusError{
			statusCode: resp.StatusCode,
			message: fmt.Sprintf(
				"AI API returned status 404: %s (check Base URL: use host root only, e.g. https://api.openai.com - do not append /v1; path is added automatically. Gemini OpenAI-compat: https://generativelanguage.googleapis.com/v1beta/openai)",
				snippet,
			),
		}
	}
	return &aiHTTPStatusError{
		statusCode: resp.StatusCode,
		message:    fmt.Sprintf("AI API returned status %d: %s", resp.StatusCode, snippet),
	}
}

// decodeAIJSONResponse bounds successful non-streaming provider responses
// independently of request-side max_tokens. Reading one byte beyond the limit
// distinguishes an exact-boundary response from a truncated oversized body.
func decodeAIJSONResponse(body io.Reader, destination interface{}) error {
	encoded, err := io.ReadAll(io.LimitReader(body, maxAINonStreamingResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read AI provider response: %w", err)
	}
	if int64(len(encoded)) > maxAINonStreamingResponseBytes {
		return fmt.Errorf("%w: non-streaming JSON response exceeds %d bytes", ErrAIProviderOutputBudget, maxAINonStreamingResponseBytes)
	}
	if err := json.Unmarshal(encoded, destination); err != nil {
		return fmt.Errorf("decode AI provider response: %w", err)
	}
	return nil
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// G12: optional image attachments as data URLs (e.g.
	// "data:image/png;base64,..."). The backend converts them to OpenAI
	// image_url / Anthropic image blocks and enforces count/size/type budgets
	// (fail-closed) before anything is sent to the provider.
	Images []string `json:"images,omitempty"`
	// ToolCalls and ToolResults preserve the provider's native tool protocol
	// across an Agent turn. They carry conversation context only; backend tool
	// capability issuance and execution remain authoritative.
	ToolCalls   []NativeToolCall   `json:"toolCalls,omitempty"`
	ToolResults []NativeToolResult `json:"toolResults,omitempty"`
}

// NativeToolResult is one terminal result for a provider-issued tool call.
// ToolCallID must refer to an earlier NativeToolCall in the same message list.
type NativeToolResult struct {
	ToolCallID string `json:"toolCallId"`
	Content    string `json:"content"`
	IsError    bool   `json:"isError,omitempty"`
}

// G12 image budgets (fail-closed): at most maxAIImageCount images per
// message, each decoded payload at most maxAIImageBytes, media types limited
// to the whitelist below. These mirror the failure paths in GOAL P9-G12
// ("图片过大") — an oversized or unsupported attachment rejects the whole
// request instead of being silently dropped.
const (
	maxAIImageCount = 4
	maxAIImageBytes = 5 << 20 // 5 MiB decoded payload
)

type imagePart struct {
	MediaType string // e.g. "image/png"
	Data      string // base64 payload
}

// parseImageDataURL validates a data:image/<type>;base64,<data> URL and
// returns the media type plus payload. Rejects non-image schemes, missing
// base64 marker, unsupported media types, and oversized payloads.
func parseImageDataURL(raw string) (imagePart, error) {
	const prefix = "data:image/"
	if !strings.HasPrefix(raw, prefix) {
		return imagePart{}, fmt.Errorf("image attachment must be a data:image/* URL")
	}
	rest := raw[len(prefix):]
	semi := strings.IndexByte(rest, ';')
	if semi < 0 {
		return imagePart{}, fmt.Errorf("image attachment is missing the ;base64 marker")
	}
	mediaType := "image/" + rest[:semi]
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
	default:
		return imagePart{}, fmt.Errorf("unsupported image media type %q", mediaType)
	}
	if !strings.HasPrefix(rest[semi:], ";base64,") {
		return imagePart{}, fmt.Errorf("image attachment is not base64 encoded")
	}
	data := rest[semi+len(";base64,"):]
	if data == "" {
		return imagePart{}, fmt.Errorf("image attachment has an empty payload")
	}
	// Budget check without allocating: DecodedLen is an upper bound.
	if base64.StdEncoding.DecodedLen(len(data)) > maxAIImageBytes {
		return imagePart{}, fmt.Errorf("image attachment exceeds the %d MiB size budget", maxAIImageBytes>>20)
	}
	return imagePart{MediaType: mediaType, Data: data}, nil
}

// validateImages rejects any message that violates the image budget so the
// request fails closed before a provider call (no partial/truncated images).
func validateImages(messages []ChatMessage) error {
	for _, m := range messages {
		if len(m.Images) > maxAIImageCount {
			return fmt.Errorf("too many images in one message: %d > %d", len(m.Images), maxAIImageCount)
		}
		for _, img := range m.Images {
			if _, err := parseImageDataURL(img); err != nil {
				return err
			}
		}
	}
	return nil
}

const (
	maxAIInputToolResultBytes          = 1 << 20
	maxAIInputAggregateToolResultBytes = 4 << 20
	maxAIInputToolIDBytes              = 4 << 10
	maxAIInputToolNameBytes            = 256
)

// validateChatMessages validates renderer-provided provider context before a
// request is sent. It does not grant tool authority; it prevents malformed or
// unbounded native protocol records from reaching a provider.
func validateChatMessages(messages []ChatMessage) error {
	if err := validateImages(messages); err != nil {
		return err
	}
	declaredCalls := make(map[string]struct{})
	completedCalls := make(map[string]struct{})
	pendingCalls := make(map[string]struct{})
	toolCallCount := 0
	argumentBytes := 0
	resultBytes := 0
	for _, message := range messages {
		switch message.Role {
		case "system", "user", "assistant", "tool":
		default:
			return fmt.Errorf("unsupported chat message role %q", message.Role)
		}
		if len(pendingCalls) > 0 && len(message.ToolResults) == 0 {
			return errors.New("native tool call batch must be completed before the next message")
		}
		if len(message.ToolCalls) > 0 {
			if message.Role != "assistant" {
				return errors.New("native tool calls require an assistant message")
			}
			if len(message.Images) > 0 || len(message.ToolResults) > 0 {
				return errors.New("assistant tool calls cannot be mixed with images or tool results")
			}
		}
		for _, call := range message.ToolCalls {
			toolCallCount++
			if toolCallCount > maxAIStreamToolCalls {
				return fmt.Errorf("native tool call count exceeds limit of %d", maxAIStreamToolCalls)
			}
			if call.ID == "" || len(call.ID) > maxAIInputToolIDBytes {
				return errors.New("native tool call ID is missing or too long")
			}
			if call.Name == "" || len(call.Name) > maxAIInputToolNameBytes {
				return errors.New("native tool name is missing or too long")
			}
			if _, exists := declaredCalls[call.ID]; exists {
				return fmt.Errorf("duplicate native tool call ID %q", call.ID)
			}
			if len(call.Arguments) > maxAIStreamToolArgumentBytes ||
				len(call.Arguments) > maxAIStreamToolArgumentsBytes-argumentBytes {
				return errors.New("native tool arguments exceed the input budget")
			}
			var arguments map[string]interface{}
			if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil || arguments == nil {
				return fmt.Errorf("native tool arguments for %q must be a JSON object", call.ID)
			}
			argumentBytes += len(call.Arguments)
			declaredCalls[call.ID] = struct{}{}
			pendingCalls[call.ID] = struct{}{}
		}
		if len(message.ToolResults) > 0 {
			if message.Role != "tool" {
				return errors.New("native tool results require a tool message")
			}
			if len(message.Images) > 0 || len(message.ToolCalls) > 0 {
				return errors.New("tool results cannot be mixed with images or tool calls")
			}
		} else if message.Role == "tool" {
			return errors.New("tool message must contain at least one native tool result")
		}
		for _, result := range message.ToolResults {
			if _, exists := declaredCalls[result.ToolCallID]; !exists {
				return fmt.Errorf("native tool result references unknown call %q", result.ToolCallID)
			}
			if _, exists := completedCalls[result.ToolCallID]; exists {
				return fmt.Errorf("duplicate native tool result for %q", result.ToolCallID)
			}
			if _, exists := pendingCalls[result.ToolCallID]; !exists {
				return fmt.Errorf("native tool result %q does not belong to the pending call batch", result.ToolCallID)
			}
			if len(result.Content) > maxAIInputToolResultBytes ||
				len(result.Content) > maxAIInputAggregateToolResultBytes-resultBytes {
				return errors.New("native tool results exceed the input budget")
			}
			resultBytes += len(result.Content)
			completedCalls[result.ToolCallID] = struct{}{}
			delete(pendingCalls, result.ToolCallID)
		}
	}
	return nil
}

func validateOutboundChatMessages(messages []ChatMessage) error {
	if err := validateChatMessages(messages); err != nil {
		return err
	}
	declared := make(map[string]struct{})
	completed := make(map[string]struct{})
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			declared[call.ID] = struct{}{}
		}
		for _, result := range message.ToolResults {
			completed[result.ToolCallID] = struct{}{}
		}
	}
	for id := range declared {
		if _, ok := completed[id]; !ok {
			return fmt.Errorf("native tool call %q has no terminal result", id)
		}
	}
	return nil
}

// openAIMessages converts ChatMessages to the OpenAI chat-completions shape.
// Messages without images keep a string content (backward compatible);
// messages with images use a content array of text + image_url parts.
func openAIMessages(messages []ChatMessage) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(messages))
	for _, m := range messages {
		if len(m.ToolCalls) > 0 {
			toolCalls := make([]map[string]interface{}, 0, len(m.ToolCalls))
			for _, call := range m.ToolCalls {
				toolCalls = append(toolCalls, map[string]interface{}{
					"id":   call.ID,
					"type": "function",
					"function": map[string]interface{}{
						"name": call.Name, "arguments": call.Arguments,
					},
				})
			}
			out = append(out, map[string]interface{}{
				"role": m.Role, "content": m.Content, "tool_calls": toolCalls,
			})
			continue
		}
		if len(m.ToolResults) > 0 {
			for _, result := range m.ToolResults {
				out = append(out, map[string]interface{}{
					"role": "tool", "tool_call_id": result.ToolCallID, "content": result.Content,
				})
			}
			continue
		}
		if len(m.Images) == 0 {
			out = append(out, map[string]interface{}{"role": m.Role, "content": m.Content})
			continue
		}
		parts := []map[string]interface{}{{"type": "text", "text": m.Content}}
		for _, img := range m.Images {
			if _, err := parseImageDataURL(img); err != nil {
				// validateImages ran before; keep the request well-formed.
				continue
			}
			parts = append(parts, map[string]interface{}{
				"type":      "image_url",
				"image_url": map[string]interface{}{"url": img},
			})
		}
		out = append(out, map[string]interface{}{"role": m.Role, "content": parts})
	}
	return out
}

// anthropicMessages converts ChatMessages to the Anthropic /v1/messages shape.
// Messages without images keep a string content; messages with images use a
// content array of text + image blocks (base64 source).
func anthropicMessages(messages []ChatMessage) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(messages))
	for _, m := range messages {
		if len(m.ToolCalls) > 0 {
			parts := make([]map[string]interface{}, 0, len(m.ToolCalls)+1)
			if m.Content != "" {
				parts = append(parts, map[string]interface{}{"type": "text", "text": m.Content})
			}
			for _, call := range m.ToolCalls {
				var input map[string]interface{}
				_ = json.Unmarshal([]byte(call.Arguments), &input)
				parts = append(parts, map[string]interface{}{
					"type": "tool_use", "id": call.ID, "name": call.Name, "input": input,
				})
			}
			out = append(out, map[string]interface{}{"role": "assistant", "content": parts})
			continue
		}
		if len(m.ToolResults) > 0 {
			parts := make([]map[string]interface{}, 0, len(m.ToolResults))
			for _, result := range m.ToolResults {
				part := map[string]interface{}{
					"type": "tool_result", "tool_use_id": result.ToolCallID, "content": result.Content,
				}
				if result.IsError {
					part["is_error"] = true
				}
				parts = append(parts, part)
			}
			out = append(out, map[string]interface{}{"role": "user", "content": parts})
			continue
		}
		if len(m.Images) == 0 {
			out = append(out, map[string]interface{}{"role": m.Role, "content": m.Content})
			continue
		}
		parts := []map[string]interface{}{{"type": "text", "text": m.Content}}
		for _, img := range m.Images {
			ip, err := parseImageDataURL(img)
			if err != nil {
				continue
			}
			parts = append(parts, map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": ip.MediaType,
					"data":       ip.Data,
				},
			})
		}
		out = append(out, map[string]interface{}{"role": m.Role, "content": parts})
	}
	return out
}

type ChatResponse struct {
	Content       string
	FinishReason  string
	usageReported bool
	tokensIn      int
	tokensOut     int
}

type openAIProviderUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

type anthropicProviderUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// validateAIProviderUsage validates provider-controlled counters before they
// are narrowed to int or used by the durable usage ledger. The subtraction
// form keeps the aggregate check overflow-safe even for hostile int64 values.
func validateAIProviderUsage(inputTokens, outputTokens int64) (bool, int, int, error) {
	if inputTokens < 0 || outputTokens < 0 {
		return false, 0, 0, errors.New("AI provider returned negative usage token counters")
	}
	if inputTokens > maxAIProviderReportedTotalTokens || outputTokens > maxAIProviderReportedTotalTokens {
		return false, 0, 0, fmt.Errorf("%w: provider-reported token counter exceeds %d", ErrAIProviderOutputBudget, maxAIProviderReportedTotalTokens)
	}
	if inputTokens > maxAIProviderReportedTotalTokens-outputTokens {
		return false, 0, 0, fmt.Errorf("%w: provider-reported total token count exceeds %d", ErrAIProviderOutputBudget, maxAIProviderReportedTotalTokens)
	}
	return inputTokens > 0 || outputTokens > 0, int(inputTokens), int(outputTokens), nil
}

// AIToolFunction is the function body of an OpenAI-compatible tool definition
// (prompt-5 Task H - native function calling dual-track with fence parsing).
type AIToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// AIToolDef is an OpenAI-compatible tools[] entry.
type AIToolDef struct {
	Type     string         `json:"type"` // "function"
	Function AIToolFunction `json:"function"`
}

// NativeToolCall is a completed tool call assembled from streaming deltas
// (OpenAI tool_calls) or Anthropic tool_use blocks. Emitted as JSON on
// the "ai:tool_calls" event after the stream completes.
type NativeToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON object string
}

func validateNativeToolCallBatch(calls []NativeToolCall) error {
	if len(calls) == 0 {
		return nil
	}
	if err := validateChatMessages([]ChatMessage{{Role: "assistant", ToolCalls: calls}}); err != nil {
		return fmt.Errorf("provider returned invalid native tool calls: %w", err)
	}
	return nil
}

type AIConfig struct {
	APIKey       string
	Provider     string
	BaseURL      string
	Model        string
	SystemPrompt string
	// Plan 54: optional overrides for the other three built-in prompts.
	// When non-empty, the corresponding GetEffective* method returns these
	// instead of the built-in const.
	AgentSystemPrompt       string
	ConversationTitlePrompt string
	InlineCompletionPrompt  string
	// N-65: MaxTokens caps the response length for chat requests. 0 means
	// use the default (4096). Sent as "max_tokens" in the request body so
	// providers don't silently truncate or consume the full output budget.
	MaxTokens int
	// N-61: ContextWindow is the token budget for the input messages. 0 means
	// use the default (8000, conservative for older models). When the
	// conversation exceeds this budget, older messages (between the first
	// user message and the most recent) are truncated with a placeholder.
	ContextWindow int
	// Temperature controls sampling randomness for chat requests. 0 means
	// use the default (0.7). Valid range is 0 to 2; values outside are clamped.
	Temperature float64
	// ReasoningEffort controls provider reasoning depth. Empty preserves the
	// legacy request shape; supported values are low, medium, and high.
	ReasoningEffort string
	// Protocol selects the HTTP API shape: "openai" (default, /v1/chat/
	// completions + Bearer) or "anthropic" (/v1/messages + x-api-key +
	// anthropic-version). Empty defaults to "openai".
	Protocol string
	// G-SEC-07: when UseStoredKey is true, the service fetches the decrypted
	// key from SettingsService using ConfigID instead of using the APIKey
	// field. This lets the frontend call SetConfig without ever holding the
	// plaintext key. When APIKey is non-empty (user entered a new key), it
	// takes precedence over UseStoredKey.
	UseStoredKey bool
	ConfigID     string
	// prompt-5 Task H: optional OpenAI-compatible tool definitions. When
	// non-empty, StartStream attaches them to the request (OpenAI tools /
	// Anthropic tools). Models may return native tool_calls; the frontend
	// still accepts fence-parsed tool calls as a dual-track fallback.
	Tools []AIToolDef
}

// defaultChatMaxTokens is the default response token cap for chat requests
// when AIConfig.MaxTokens is unset. Keeps responses bounded so a single
// request can't consume the model's entire output budget.
const defaultChatMaxTokens = 4096

// maxTokens returns the effective max_tokens for chat requests.
// N-93: takes a snapshot to avoid racing with SetConfig.
func (a *AIService) maxTokens() int {
	return maxTokensFrom(a.snapshot().config)
}

// defaultContextWindow is the conservative default token budget for input
// messages when AIConfig.ContextWindow is unset. 8000 leaves room for the
// response within an 8k-token model window; users with larger-context models
// (16k, 32k, 128k) should increase this in settings.
const defaultContextWindow = 8000

// contextWindow returns the effective input token budget for truncation.
// N-93: takes a snapshot to avoid racing with SetConfig.
func (a *AIService) contextWindow() int {
	return contextWindowFrom(a.snapshot().config)
}

// prepareMessages prepends the system prompt and applies context-window
// truncation (N-61). Called by all chat request paths (Send, SendStream,
// streamWithEvents) to ensure consistent message preparation and prevent
// context-overflow errors on long conversations.
//
// N-93: takes a snapshot at the call site and uses prepareMessagesWith so
// the read of a.config is protected by the read lock.
func (a *AIService) prepareMessages(messages []ChatMessage) []ChatMessage {
	return prepareMessagesWith(a.snapshot().config, messages)
}

// prepareMessagesWith is the standalone form of prepareMessages (N-93).
// It uses only the provided config, so callers that already hold a snapshot
// can avoid re-reading a.config.
func prepareMessagesWith(cfg AIConfig, messages []ChatMessage) []ChatMessage {
	full := withSystemPromptFrom(cfg, messages)
	return truncateToTokenBudget(full, contextWindowFrom(cfg))
}

// effectiveSystemPromptFrom returns the configured prompt or the default,
// based on the provided config (N-93 standalone form).
func effectiveSystemPromptFrom(cfg AIConfig) string {
	if cfg.SystemPrompt != "" {
		return cfg.SystemPrompt
	}
	return DefaultSystemPrompt
}

// withSystemPromptFrom prepends the system prompt to the messages slice,
// using the provided config (N-93 standalone form).
func withSystemPromptFrom(cfg AIConfig, messages []ChatMessage) []ChatMessage {
	sp := effectiveSystemPromptFrom(cfg)
	if sp == "" {
		return messages
	}
	out := make([]ChatMessage, 0, len(messages)+1)
	out = append(out, ChatMessage{Role: "system", Content: sp})
	out = append(out, messages...)
	return out
}

// contextWindowFrom returns the effective input token budget for truncation,
// using the provided config (N-93 standalone form).
func contextWindowFrom(cfg AIConfig) int {
	if cfg.ContextWindow > 0 {
		return cfg.ContextWindow
	}
	return defaultContextWindow
}

// maxTokensFrom returns the effective max_tokens for chat requests,
// using the provided config (N-93 standalone form).
func maxTokensFrom(cfg AIConfig) int {
	if cfg.MaxTokens > 0 {
		return cfg.MaxTokens
	}
	return defaultChatMaxTokens
}

// Per-request timeout budgets (N-69). Using context.WithTimeout at each call
// site allows different request types to have different budgets, instead of
// a single client-wide Timeout that must fit all cases. The HTTP client still
// has a safety-net Timeout for non-context-aware paths.
const (
	// chatTimeout caps non-streaming chat Send requests. Long completions
	// (Claude Opus, GPT-4 with long outputs) can take 60-90s; 300s leaves
	// margin for slow connections.
	chatTimeout = 300 * time.Second
	// completionTimeout caps inline code completion requests.
	completionTimeout = 60 * time.Second
	// titleTimeout caps conversation title generation requests.
	titleTimeout = 30 * time.Second
)

type AIService struct {
	config AIConfig
	app    *application.App
	mu     sync.RWMutex
	// N-52: streamCancel is a *streamCancel (pointer) so it can be
	// compared by identity in the streaming goroutine's defer. The
	// cancel function itself (a context.CancelFunc) cannot be compared
	// with == in Go (function values are not comparable). Wrapping it
	// in a struct pointer allows the compare-and-swap pattern: only
	// clear a.cancel if it still points to OUR streamCancel.
	cancel *streamCancel
	// prompt-6 Task 2: active stream id (empty when idle). Emitted on all
	// ai:* stream events so dual windows can route/filter payloads.
	activeStreamID string
	presetService  *PresetService
	// projectRoot is the currently open project root, used by the preset
	// service to locate project-level presets. Set via SetProjectRoot.
	projectRoot string
	// G-SEC-07: settingsService is used to fetch stored API keys when
	// UseStoredKey is true, so keys never cross the Wails binding.
	settingsService *SettingsService
	// Plan 11 Task 12 Step 3: permissionService provides per-operation
	// model assignment + fallback. When set, ResolveModelFor returns the
	// config for a specific operation (chat/agent/review/etc.) instead
	// of the global config. Callers (agent_service, frontend store) use
	// it to route each operation to its assigned model.
	permissionService *AIPermissionService
	// lifecycle is the shared G33 session/context/metering entry point.
	lifecycle *AgentLifecycle
	// agent is the trusted session-owner authority for renderer Agent streams.
	// It is wired together with lifecycle and is never renderer replaceable.
	agent *AgentService
	// streamShutdownTimeout bounds trusted session/agent teardown. Tests may
	// shorten it; production uses defaultAIStreamShutdownTimeout.
	streamShutdownTimeout time.Duration
	// Provider stream deadlines are backend-owned. Tests may shorten them;
	// renderer configuration cannot extend or disable these limits.
	streamWallTimeout time.Duration
	streamIdleTimeout time.Duration
	// streamAdmissionClosed is sealed under mu by AgentService.Close. Once
	// shutdown begins, a stream that already owns the slot is drained while
	// every later renderer admission fails closed.
	streamAdmissionClosed bool
}

// aiSnapshot is a point-in-time copy of the AIService's configuration
// fields, taken under the read lock (N-93 / Proposal AB). It is used by
// methods that launch goroutines (StartStream) or make long-running HTTP
// requests, so that a concurrent SetConfig call does not cause a data race
// or produce a request with half-updated configuration.
type aiSnapshot struct {
	config        AIConfig
	app           *application.App
	presetService *PresetService
	projectRoot   string
	// G-SEC-07: settingsService is used to fetch stored API keys when
	// UseStoredKey is true, so keys never cross the Wails binding.
	settingsService   *SettingsService
	permissionService *AIPermissionService
	lifecycle         *AgentLifecycle
}

// snapshot returns a copy of the service's configuration fields under the
// read lock (N-93). Callers use the returned copy instead of reading
// a.config / a.app / a.presetService / a.projectRoot directly, which would
// race with SetConfig / SetApp / SetPresetService / SetProjectRoot.
func (a *AIService) snapshot() aiSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return aiSnapshot{
		config:            a.config,
		app:               a.app,
		presetService:     a.presetService,
		projectRoot:       a.projectRoot,
		settingsService:   a.settingsService,
		permissionService: a.permissionService,
		lifecycle:         a.lifecycle,
	}
}

// snapshotForProviderCall orders AI configuration capture after the shared
// Agent workspace read lease. ProjectService takes the write side before its
// first setter, so a provider request can observe either the complete old
// workspace or the complete new workspace, never an old projectRoot after a
// successful switch.
func (a *AIService) snapshotForProviderCall() (aiSnapshot, *agentWorkspaceAuthorityReadLease, error) {
	if a == nil {
		return aiSnapshot{}, nil, fmt.Errorf("AI service is unavailable: %w", ErrNotAllowed)
	}
	a.mu.RLock()
	lifecycle := a.lifecycle
	a.mu.RUnlock()
	if lifecycle == nil {
		snap := a.snapshot()
		if snap.lifecycle != nil {
			return aiSnapshot{}, nil, fmt.Errorf("AI lifecycle changed during provider admission: %w", ErrNotAllowed)
		}
		return snap, nil, nil
	}
	lease, err := lifecycle.acquireWorkspaceAuthority()
	if err != nil {
		return aiSnapshot{}, nil, err
	}
	snap := a.snapshot()
	if snap.lifecycle != lifecycle {
		lease.release()
		return aiSnapshot{}, nil, fmt.Errorf("AI lifecycle changed during provider admission: %w", ErrNotAllowed)
	}
	return snap, lease, nil
}

// streamCancel wraps a context.CancelFunc so the streaming goroutine
// can check identity (compare-and-swap) before clearing a.cancel.
type streamCancel struct {
	fn          context.CancelFunc
	owner       agentSessionOwner
	target      application.Window
	lifecycleID string
	done        chan struct{}
	doneOnce    sync.Once
}

func (s *streamCancel) markDone() {
	if s == nil || s.done == nil {
		return
	}
	s.doneOnce.Do(func() { close(s.done) })
}

// newStreamID returns a random hex stream id (prompt-6 Task 2).
func newStreamID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; fall back to timestamp-based id.
		return fmt.Sprintf("s%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// emitAIStreamEvent emits a structured AI stream event with streamId
// (prompt-6 Task 2). Payload shape is always a map so dual-window
// clients can ignore events for other streams.
func emitAIStreamEvent(window application.Window, name, streamID string, fields map[string]interface{}) {
	if window == nil {
		return
	}
	payload := map[string]interface{}{"streamId": streamID}
	for k, v := range fields {
		payload[k] = v
	}
	window.DispatchWailsEvent(&application.CustomEvent{Name: name, Data: payload})
}

// emitAIStreamBusy remains process-wide because it carries no model output or
// tool arguments. It intentionally omits the owner stream ID so other windows
// learn only that the process-wide provider slot is busy.
func emitAIStreamBusy(app *application.App, name string, fields map[string]interface{}) {
	if app == nil {
		return
	}
	payload := make(map[string]interface{}, len(fields))
	for k, v := range fields {
		payload[k] = v
	}
	app.Event.Emit(name, payload)
}

func NewAIService() *AIService {
	return &AIService{}
}

// SetConfig validates and stores the AI configuration.
// G-SEC-01: BaseURL is validated via ValidateBaseURL before writing. An
// invalid BaseURL (SSRF vector, non-http scheme, non-loopback http) is
// rejected and the previous config is preserved. An empty BaseURL is
// allowed (unconfigured state).
func (a *AIService) SetConfig(config AIConfig) error {
	// N-71 / Proposal AG: cap the SystemPromptOverride length to prevent
	// an excessively long override from consuming the model's context window
	// or acting as an injection vector. Log a warning and truncate.
	if len(config.SystemPrompt) > MaxSystemPromptOverrideLen {
		slog.Warn("ai setconfig: SystemPromptOverride exceeds max length, truncating",
			"len", len(config.SystemPrompt), "max", MaxSystemPromptOverrideLen)
		config.SystemPrompt = config.SystemPrompt[:MaxSystemPromptOverrideLen]
	}
	var err error
	config.Provider = strings.ToLower(strings.TrimSpace(config.Provider))
	config.Protocol = strings.ToLower(strings.TrimSpace(config.Protocol))
	if config.Protocol == "" {
		config.Protocol = "openai"
	}
	config.ReasoningEffort, err = normalizeReasoningEffort(config.ReasoningEffort)
	if err != nil {
		return err
	}
	if err := validateReasoningCapability(config.Provider, config.Model, config.Protocol, config.ReasoningEffort); err != nil {
		return err
	}
	// Normalize before validate so ".../v1" configs match JoinAIEndpoint.
	if config.BaseURL != "" {
		config.BaseURL = NormalizeAIBaseURL(config.BaseURL)
	}
	// G-SEC-01: validate BaseURL to prevent SSRF / API key exfiltration.
	if config.BaseURL != "" {
		if err := ValidateBaseURL(config.BaseURL); err != nil {
			slog.Warn("ai setconfig: rejected base URL", "baseURL", config.BaseURL, "err", err)
			return fmt.Errorf("invalid base URL: %w", err)
		}
	}
	// G-SEC-07: when UseStoredKey is true and no plaintext key was provided,
	// fetch the key from SettingsService so the frontend never has to send it.
	if config.APIKey == "" && config.UseStoredKey && config.ConfigID != "" {
		ss := a.settingsService
		if ss != nil {
			if key, kerr := ss.getAPIKeyForConfig(config.ConfigID); kerr == nil && key != "" {
				config.APIKey = key
			}
		}
	}
	// N-93: write lock protects against concurrent reads in StartStream goroutine.
	a.mu.Lock()
	a.config = config
	a.mu.Unlock()
	return nil
}

// GetReasoningCapability reports the backend-verified reasoning mapping for a
// provider/model pair. It never reads credentials or performs network I/O.
func (a *AIService) GetReasoningCapability(provider, model, protocol string) ReasoningCapability {
	return reasoningCapabilityFor(provider, model, protocol)
}

// setPresetService injects a PresetService so the AI service can resolve
// presets from all three layers (builtin + project + user) (N-17). If nil,
// the AI service falls back to the built-in preset set only.
//
//wails:ignore
func (a *AIService) setPresetService(ps *PresetService) {
	a.mu.Lock()
	a.presetService = ps
	a.mu.Unlock()
}

// setProjectRoot updates the project root used for project-level preset
// lookups. Called from ProjectService.AddProject.
//
//wails:ignore
//wails:ignore
func (a *AIService) setProjectRoot(root string) {
	a.mu.Lock()
	a.projectRoot = root
	a.mu.Unlock()
}

// setApp links the application instance so the service can emit events.
// Called from main.go after the app is created.
//
//wails:ignore
func (a *AIService) setApp(app *application.App) {
	a.mu.Lock()
	a.app = app
	a.mu.Unlock()
}

// setSettingsService injects a SettingsService so AIService can fetch stored
// API keys (G-SEC-07). When SetConfig is called with UseStoredKey=true, the
// service reads the decrypted key from settings.json via this reference.
//
//wails:ignore
func (a *AIService) setSettingsService(ss *SettingsService) {
	a.mu.Lock()
	a.settingsService = ss
	a.mu.Unlock()
}

// setPermissionService injects the AIPermissionService (Plan 11 Task 12 Step 3).
// When set, ResolveModelFor returns per-operation model assignments instead
// of the global config. Callers use it to route operations (chat/agent/review)
// to their assigned models with fallback support (Step 4).
//
//wails:ignore
func (a *AIService) setPermissionService(ps *AIPermissionService) {
	a.mu.Lock()
	a.permissionService = ps
	a.mu.Unlock()
}

// ResolveModelFor returns non-secret assignment metadata for a specific
// operation (Step 3). It remains exported for API compatibility, but never
// returns provider endpoints, protocol details, prompts, or API keys. Backend
// execution must use resolveModelFor followed by provider hydration instead.
func (a *AIService) ResolveModelFor(op AIOperation) (AIConfig, *AIConfig, error) {
	primary, fallback, err := a.resolveModelFor(op)
	if err != nil {
		return AIConfig{}, nil, err
	}
	publicPrimary := redactPublicAIModelResolution(primary)
	if fallback == nil {
		return publicPrimary, nil, nil
	}
	publicFallback := redactPublicAIModelResolution(*fallback)
	return publicPrimary, &publicFallback, nil
}

func redactPublicAIModelResolution(config AIConfig) AIConfig {
	// Model assignment is useful to the renderer for display, but the rest of
	// the resolved provider configuration is backend authority or user data.
	config.APIKey = ""
	config.BaseURL = ""
	config.Protocol = ""
	config.SystemPrompt = ""
	config.AgentSystemPrompt = ""
	config.ConversationTitlePrompt = ""
	config.InlineCompletionPrompt = ""
	config.Tools = nil
	config.UseStoredKey = config.ConfigID != ""
	return config
}

// resolveModelFor builds the backend-only assignment. It intentionally keeps
// the assigned provider ID separate from endpoint/key hydration so callers
// cannot accidentally reuse the global provider boundary.
//
// Plan 11 Task 12 Step 3-4:
//   - If permissionService is set and the operation has an assignment with
//     a non-empty Model, returns a config derived from the global config
//     but with the operation's model/provider/temperature/maxTokens.
//   - G-SEC-07 (Step 9): UseStoredKey=true + ConfigID=ProviderID so the
//     key is fetched from SettingsService (never crosses Wails binding).
//   - Step 6: If the operation is disabled, returns ErrNotAllowed.
//   - Step 4: The returned fallback config (if any) is used when the primary
//     call fails (429/timeout). The caller records usage via RecordUsage.
//
// When permissionService is nil or no assignment exists, returns the global
// config (backward compatible for backend callers).
func (a *AIService) resolveModelFor(op AIOperation) (AIConfig, *AIConfig, error) {
	return resolveModelForSnapshot(a.snapshot(), op)
}

// GetDefaultSystemPrompt returns the built-in default system prompt.
func (a *AIService) GetDefaultSystemPrompt() string {
	return DefaultSystemPrompt
}

// GetAgentSystemPrompt returns the built-in agent-mode system prompt.
// Used by the frontend to let users preview/load the agent prompt.
func (a *AIService) GetAgentSystemPrompt() string {
	return AgentSystemPrompt
}

// GetSystemPrompt returns the named built-in system prompt.
// Supported names: "default", "agent". Returns the default for unknown names.
func (a *AIService) GetSystemPrompt(name string) string {
	switch name {
	case "agent":
		return AgentSystemPrompt
	default:
		return DefaultSystemPrompt
	}
}

// GetPresetPrompt returns the instruction template for the named preset action.
// If a PresetService is configured, it searches all three layers (builtin +
// project + user); otherwise it falls back to the built-in set only (N-17).
// N-93: takes a snapshot to avoid racing with SetPresetService / SetProjectRoot.
func (a *AIService) GetPresetPrompt(name string) (string, error) {
	snap := a.snapshot()
	if snap.presetService != nil {
		return snap.presetService.GetPresetPrompt(name, snap.projectRoot)
	}
	return GetPresetPrompt(name)
}

// ListPresets returns metadata for all available preset actions, ordered for UI display.
// If a PresetService is configured, it merges all three layers (builtin + project + user);
// otherwise it returns the built-in set only (N-17).
// N-93: takes a snapshot to avoid racing with SetPresetService / SetProjectRoot.
func (a *AIService) ListPresets() []PresetMeta {
	snap := a.snapshot()
	if snap.presetService != nil {
		return snap.presetService.ListPresets(snap.projectRoot)
	}
	return ListPresetPrompts()
}

// ListPresetsWithSource returns presets with their source layer (N-17).
// Used by the preset manager UI to show where each preset came from.
// N-93: takes a snapshot to avoid racing with SetPresetService / SetProjectRoot.
func (a *AIService) ListPresetsWithSource() []PresetWithSource {
	snap := a.snapshot()
	if snap.presetService != nil {
		return snap.presetService.ListPresetsWithSource(snap.projectRoot)
	}
	// Fallback: wrap built-in presets with source=builtin.
	result := make([]PresetWithSource, 0, len(builtinPresets))
	for _, p := range builtinPresets {
		result = append(result, PresetWithSource{
			PresetFile: PresetFile(p),
			Source:     PresetSourceBuiltin,
		})
	}
	return result
}

// SaveProjectPreset writes a project-level preset file (N-17).
// N-93: takes a snapshot to avoid racing with SetPresetService / SetProjectRoot.
func (a *AIService) SaveProjectPreset(preset PresetFile) error {
	snap := a.snapshot()
	if snap.presetService == nil {
		return fmt.Errorf("preset service not configured")
	}
	return snap.presetService.SaveProjectPreset(snap.projectRoot, preset)
}

// SaveUserPreset writes a user-global preset file (N-17).
// N-93: takes a snapshot to avoid racing with SetPresetService.
func (a *AIService) SaveUserPreset(preset PresetFile) error {
	snap := a.snapshot()
	if snap.presetService == nil {
		return fmt.Errorf("preset service not configured")
	}
	return snap.presetService.SaveUserPreset(preset)
}

// DeleteProjectPreset removes a project-level preset file (N-17).
// N-93: takes a snapshot to avoid racing with SetPresetService / SetProjectRoot.
func (a *AIService) DeleteProjectPreset(name string) error {
	snap := a.snapshot()
	if snap.presetService == nil {
		return fmt.Errorf("preset service not configured")
	}
	return snap.presetService.DeleteProjectPreset(snap.projectRoot, name)
}

// DeleteUserPreset removes a user-global preset file (N-17).
// N-93: takes a snapshot to avoid racing with SetPresetService.
func (a *AIService) DeleteUserPreset(name string) error {
	snap := a.snapshot()
	if snap.presetService == nil {
		return fmt.Errorf("preset service not configured")
	}
	return snap.presetService.DeleteUserPreset(name)
}

// effectiveSystemPrompt returns the configured prompt or the default.
// N-93: takes a snapshot to avoid racing with SetConfig.
func (a *AIService) effectiveSystemPrompt() string {
	return effectiveSystemPromptFrom(a.snapshot().config)
}

func (a *AIService) Send(messages []ChatMessage) (response *ChatResponse, returnErr error) {
	start := time.Now()
	snap, workspaceLease, err := a.snapshotForProviderCall()
	if err != nil {
		return nil, err
	}
	defer workspaceLease.release()
	snap, err = a.admitProviderOperation(snap, AIOpChat)
	if err != nil {
		return nil, err
	}
	cfg := snap.config
	unit, err := beginChatLifecycleWithWorkspaceLease(snap, newStreamID(), messages, workspaceLease)
	if err != nil {
		return nil, err
	}
	defer func() {
		if lifecycleErr := unit.finish(response, returnErr); lifecycleErr != nil && returnErr == nil {
			returnErr = lifecycleErr
		}
	}()
	if cfg.APIKey == "" {
		slog.Error("ai send: api key not configured")
		return nil, errors.New("API key not configured")
	}

	// N-61: prepareMessagesWith applies context-window truncation to prevent
	// long conversations from exceeding the model's token limit.
	fullMessages, inputTokens, err := a.prepareMessagesForCall(snap, messages)
	if unit != nil && err == nil {
		unit.inputTokens = inputTokens
	}
	if err != nil {
		return nil, err
	}
	// G12: image attachments are validated before any provider call.
	if err := validateOutboundChatMessages(fullMessages); err != nil {
		slog.Error("ai send: image validation failed", "err", err)
		return nil, err
	}

	// N-69: per-request timeout (300s). The single context spans all retry attempts.
	ctx, cancel := context.WithTimeout(context.Background(), chatTimeout)
	defer cancel()

	if isAnthropicProtocol(cfg) {
		return a.sendAnthropic(ctx, cfg, fullMessages, len(messages), start)
	}

	reqBody := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    openAIMessages(fullMessages),
		"max_tokens":  maxTokensFrom(cfg), // N-65: bound response length
		"temperature": effectiveTemperature(cfg),
	}
	if err := applyReasoningRequestConfig(reqBody, cfg); err != nil {
		return nil, err
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		slog.Error("ai send: marshal failed", "err", err)
		return nil, err
	}

	// N-63: retry on transient errors (429, 5xx, network). Each attempt
	// rebuilds the request with a fresh body reader (bytes.Reader is
	// consumed after the first send).
	resp, err := doWithRetryContext(ctx, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", JoinAIEndpoint(cfg.BaseURL, "/v1/chat/completions"), bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		setCommonHeaders(req, cfg.APIKey)
		return aiHTTPClient.Do(req)
	})
	if err != nil {
		slog.Error("ai send: http request failed (after retries)", "model", cfg.Model, "err", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := parseAIError(resp)
		slog.Error("ai send: non-2xx response", "model", cfg.Model, "status", resp.StatusCode, "err", apiErr)
		return nil, apiErr
	}

	var result struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage openAIProviderUsage `json:"usage"`
	}
	if err := decodeAIJSONResponse(resp.Body, &result); err != nil {
		slog.Error("ai send: decode failed", "err", err)
		return nil, err
	}
	usageReported, tokensIn, tokensOut, err := validateAIProviderUsage(
		result.Usage.PromptTokens,
		result.Usage.CompletionTokens,
	)
	if err != nil {
		return nil, err
	}

	if len(result.Choices) == 0 {
		slog.Warn("ai send: no choices in response", "model", cfg.Model)
		return nil, errors.New("no choices in response")
	}

	slog.Info("ai send: completed",
		"model", cfg.Model,
		"messages", len(messages),
		"finish", result.Choices[0].FinishReason,
		"durationMs", time.Since(start).Milliseconds(),
	)
	return &ChatResponse{
		Content:       result.Choices[0].Message.Content,
		FinishReason:  result.Choices[0].FinishReason,
		usageReported: usageReported,
		tokensIn:      tokensIn,
		tokensOut:     tokensOut,
	}, nil
}

// sendAnthropic sends a non-streaming chat request using the Anthropic
// /v1/messages API shape. The system prompt is lifted out of the messages
// array into the top-level "system" field, since Anthropic does not accept a
// "system" role inside "messages". The first text content block's text is
// mapped to ChatResponse.Content and stop_reason to FinishReason.
func (a *AIService) sendAnthropic(ctx context.Context, cfg AIConfig, fullMessages []ChatMessage, msgCount int, start time.Time) (*ChatResponse, error) {
	systemPrompt, chatMessages := splitSystemPrompt(fullMessages)
	reqBody := map[string]interface{}{
		"model":       cfg.Model,
		"max_tokens":  maxTokensFrom(cfg),
		"temperature": effectiveTemperature(cfg),
		"system":      systemPrompt,
		"messages":    anthropicMessages(chatMessages),
	}
	if err := applyReasoningRequestConfig(reqBody, cfg); err != nil {
		return nil, err
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		slog.Error("ai send: marshal failed", "err", err)
		return nil, err
	}

	resp, err := doWithRetryContext(ctx, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", JoinAIEndpoint(cfg.BaseURL, "/v1/messages"), bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		setProtocolHeaders(req, cfg)
		return aiHTTPClient.Do(req)
	})
	if err != nil {
		slog.Error("ai send: http request failed (after retries)", "model", cfg.Model, "err", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := parseAIError(resp)
		slog.Error("ai send: non-2xx response", "model", cfg.Model, "status", resp.StatusCode, "err", apiErr)
		return nil, apiErr
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string                 `json:"stop_reason"`
		Usage      anthropicProviderUsage `json:"usage"`
	}
	if err := decodeAIJSONResponse(resp.Body, &result); err != nil {
		slog.Error("ai send: decode failed", "err", err)
		return nil, err
	}
	usageReported, tokensIn, tokensOut, err := validateAIProviderUsage(
		result.Usage.InputTokens,
		result.Usage.OutputTokens,
	)
	if err != nil {
		return nil, err
	}

	text := ""
	if len(result.Content) > 0 {
		text = result.Content[0].Text
	}

	slog.Info("ai send: completed",
		"model", cfg.Model,
		"messages", msgCount,
		"finish", result.StopReason,
		"durationMs", time.Since(start).Milliseconds(),
	)
	return &ChatResponse{
		Content:       text,
		FinishReason:  result.StopReason,
		usageReported: usageReported,
		tokensIn:      tokensIn,
		tokensOut:     tokensOut,
	}, nil
}

// CompletionRequest holds the context for an inline code completion request.
type CompletionRequest struct {
	Prefix   string `json:"prefix"`
	Suffix   string `json:"suffix"`
	Language string `json:"language"`
	FilePath string `json:"filePath"`
}

// CompletionResponse holds the AI-generated completion text.
type CompletionResponse struct {
	Text string `json:"text"`
}

// completeSystemPrompt returns the system prompt tailored for code completion.
// Uses the InlineCompletionSystemPrompt template with the {{language}}
// placeholder filled in. Plan 54: when the AIService has a user-configured
// inline-completion override, that template is used instead of the built-in.
// N-93: takes a config to avoid racing with SetConfig.
func completeSystemPromptFrom(cfg AIConfig, language string) string {
	if language == "" {
		language = "text"
	}
	tmpl := InlineCompletionSystemPrompt
	if cfg.InlineCompletionPrompt != "" {
		tmpl = cfg.InlineCompletionPrompt
	}
	return strings.ReplaceAll(tmpl, "{{language}}", language)
}

// completeSystemPrompt is the method form of completeSystemPromptFrom (N-93).
// Takes a snapshot so the read of a.config is protected by the read lock.
func (a *AIService) completeSystemPrompt(language string) string {
	return completeSystemPromptFrom(a.snapshot().config, language)
}

// Complete sends a non-streaming completion request and returns the suggested text.
// prompt-6 Task 8 / BUG-M9: skip when a main chat stream is active so inline
// completion does not compete for the same provider quota.
func (a *AIService) Complete(req CompletionRequest) (response *CompletionResponse, returnErr error) {
	snap, workspaceLease, err := a.snapshotForProviderCall()
	if err != nil {
		return nil, err
	}
	defer workspaceLease.release()
	snap, err = a.admitProviderOperation(snap, AIOpInlineCompletion)
	if err != nil {
		return nil, err
	}
	cfg := snap.config
	if cfg.APIKey == "" {
		return nil, errors.New("API key not configured")
	}
	a.mu.RLock()
	busy := a.cancel != nil
	a.mu.RUnlock()
	if busy {
		return nil, errors.New("inline completion paused while a chat stream is active")
	}
	if isAnthropicProtocol(cfg) {
		// Anthropic protocol doesn't support inline completion in this build;
		// return an error so the caller can fall back.
		return nil, errors.New("inline completion not supported for Anthropic protocol")
	}

	userMsg := fmt.Sprintf("File: %s\nLanguage: %s\n\nCode before cursor:\n%s\n\nCode after cursor:\n%s\n\nComplete the code at the cursor:",
		req.FilePath, req.Language, req.Prefix, req.Suffix)

	messages := []ChatMessage{
		{Role: "system", Content: completeSystemPromptFrom(cfg, req.Language)},
		{Role: "user", Content: userMsg},
	}
	unit, err := beginAILifecycleWithWorkspaceLease(snap, newStreamID(), agentcore.UsageUnitAI, AIOpInlineCompletion, messages, workspaceLease)
	if err != nil {
		return nil, err
	}
	var meterResponse *ChatResponse
	defer func() {
		if lifecycleErr := unit.finish(meterResponse, returnErr); lifecycleErr != nil && returnErr == nil {
			returnErr = lifecycleErr
		}
	}()
	if snap.lifecycle != nil {
		inputTokens, err := snap.lifecycle.requireMessages(messages, contextWindowFrom(cfg))
		if err != nil {
			return nil, err
		}
		unit.inputTokens = inputTokens
	}

	reqBody := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"max_tokens":  256,
		"temperature": 0.2,
		"stream":      false,
	}
	if err := applyReasoningRequestConfig(reqBody, cfg); err != nil {
		return nil, err
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	// N-69: 60s timeout for inline completion. The context spans all retries.
	ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
	defer cancel()

	// N-63: retry on transient errors. Inline completion is latency-sensitive
	// but a single 429 shouldn't fail the suggestion.
	resp, err := doWithRetryContext(ctx, func() (*http.Response, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", JoinAIEndpoint(cfg.BaseURL, "/v1/chat/completions"), bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		setCommonHeaders(httpReq, cfg.APIKey)
		return aiHTTPClient.Do(httpReq)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseAIError(resp)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage openAIProviderUsage `json:"usage"`
	}
	if err := decodeAIJSONResponse(resp.Body, &result); err != nil {
		return nil, err
	}
	usageReported, tokensIn, tokensOut, err := validateAIProviderUsage(
		result.Usage.PromptTokens,
		result.Usage.CompletionTokens,
	)
	if err != nil {
		return nil, err
	}
	if len(result.Choices) == 0 {
		meterResponse = &ChatResponse{usageReported: usageReported, tokensIn: tokensIn, tokensOut: tokensOut}
		return &CompletionResponse{Text: ""}, nil
	}

	text := strings.TrimSpace(result.Choices[0].Message.Content)
	meterResponse = &ChatResponse{
		Content: text, usageReported: usageReported,
		tokensIn: tokensIn, tokensOut: tokensOut,
	}
	return &CompletionResponse{Text: text}, nil
}

// GetConversationTitlePrompt returns the built-in conversation title prompt
// template. Exposed so the frontend can preview it in settings.
func (a *AIService) GetConversationTitlePrompt() string {
	return ConversationTitlePrompt
}

// GetInlineCompletionSystemPrompt returns the built-in inline code completion
// system prompt template. Exposed so the frontend can preview it in settings.
func (a *AIService) GetInlineCompletionSystemPrompt() string {
	return InlineCompletionSystemPrompt
}

// GetEffectiveAgentSystemPrompt returns the agent-mode system prompt,
// preferring the user-configured override (Plan 54) over the built-in const.
// Empty override means "use the built-in".
// N-93: takes a snapshot to avoid racing with SetConfig.
func (a *AIService) GetEffectiveAgentSystemPrompt() string {
	cfg := a.snapshot().config
	if cfg.AgentSystemPrompt != "" {
		return cfg.AgentSystemPrompt
	}
	return AgentSystemPrompt
}

// GetEffectiveConversationTitlePrompt returns the conversation-title prompt,
// preferring the user-configured override (Plan 54) over the built-in const.
// Empty override means "use the built-in".
// N-93: takes a snapshot to avoid racing with SetConfig.
func (a *AIService) GetEffectiveConversationTitlePrompt() string {
	cfg := a.snapshot().config
	if cfg.ConversationTitlePrompt != "" {
		return cfg.ConversationTitlePrompt
	}
	return ConversationTitlePrompt
}

// GetEffectiveInlineCompletionPrompt returns the inline-completion system
// prompt, preferring the user-configured override (Plan 54) over the built-in
// const. Empty override means "use the built-in".
// N-93: takes a snapshot to avoid racing with SetConfig.
func (a *AIService) GetEffectiveInlineCompletionPrompt() string {
	cfg := a.snapshot().config
	if cfg.InlineCompletionPrompt != "" {
		return cfg.InlineCompletionPrompt
	}
	return InlineCompletionSystemPrompt
}

// GenerateTitleWithAI uses the AI model to generate a short conversation title
// from the first user message. Returns the generated title. If the AI is
// unavailable (no API key) or returns an error, falls back to the legacy
// GenerateTitle heuristic so callers always get a usable title. The fallback
// is returned alongside a non-nil error so the caller can log it.
func (a *AIService) GenerateTitleWithAI(firstMessage string) (titleResult string, returnErr error) {
	fallback := GenerateTitle(firstMessage)
	snap, workspaceLease, err := a.snapshotForProviderCall()
	if err != nil {
		return fallback, err
	}
	defer workspaceLease.release()
	snap, err = a.admitProviderOperation(snap, AIOpTitleGeneration)
	if err != nil {
		return fallback, err
	}
	cfg := snap.config
	if cfg.APIKey == "" {
		return fallback, nil
	}
	if isAnthropicProtocol(cfg) {
		// Anthropic protocol doesn't support title generation in this build;
		// return an error so the caller can fall back to its own heuristic.
		return "", errors.New("title generation not supported for Anthropic protocol")
	}
	// Plan 54: prefer the user-configured title prompt override if set.
	tmpl := ConversationTitlePrompt
	if cfg.ConversationTitlePrompt != "" {
		tmpl = cfg.ConversationTitlePrompt
	}
	prompt := strings.ReplaceAll(tmpl, "{{first_message}}", firstMessage)
	messages := []ChatMessage{
		{Role: "user", Content: prompt},
	}
	unit, err := beginAILifecycleWithWorkspaceLease(snap, newStreamID(), agentcore.UsageUnitAI, AIOpTitleGeneration, messages, workspaceLease)
	if err != nil {
		return fallback, err
	}
	var meterResponse *ChatResponse
	defer func() {
		if lifecycleErr := unit.finish(meterResponse, returnErr); lifecycleErr != nil && returnErr == nil {
			returnErr = lifecycleErr
		}
	}()
	if snap.lifecycle != nil {
		inputTokens, err := snap.lifecycle.requireMessages(messages, contextWindowFrom(cfg))
		if err != nil {
			return fallback, err
		}
		unit.inputTokens = inputTokens
	}
	reqBody := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"max_tokens":  32,
		"temperature": 0.3,
		"stream":      false,
	}
	if err := applyReasoningRequestConfig(reqBody, cfg); err != nil {
		return fallback, err
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fallback, err
	}
	// N-69: 30s timeout for title generation (was client-wide 120s).
	ctx, cancel := context.WithTimeout(context.Background(), titleTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", JoinAIEndpoint(cfg.BaseURL, "/v1/chat/completions"), bytes.NewReader(bodyBytes))
	if err != nil {
		return fallback, err
	}
	setCommonHeaders(req, cfg.APIKey)
	resp, err := aiHTTPClient.Do(req)
	if err != nil {
		return fallback, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fallback, parseAIError(resp)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage openAIProviderUsage `json:"usage"`
	}
	if err := decodeAIJSONResponse(resp.Body, &result); err != nil {
		return fallback, err
	}
	usageReported, tokensIn, tokensOut, err := validateAIProviderUsage(
		result.Usage.PromptTokens,
		result.Usage.CompletionTokens,
	)
	if err != nil {
		return fallback, err
	}
	meterResponse = &ChatResponse{
		usageReported: usageReported,
		tokensIn:      tokensIn, tokensOut: tokensOut,
	}
	if len(result.Choices) == 0 {
		return fallback, nil
	}
	title := strings.TrimSpace(result.Choices[0].Message.Content)
	meterResponse.Content = title
	// Clean up common model artifacts: surrounding quotes, trailing period,
	// and any stray code fences.
	title = strings.Trim(title, "\"'`")
	title = strings.Trim(title, "`")
	title = strings.TrimRight(title, ".")
	// If the model wrapped the title in a code fence, extract the first line.
	if strings.HasPrefix(title, "```") {
		lines := strings.SplitN(title, "\n", 2)
		if len(lines) > 1 {
			title = strings.TrimSpace(strings.SplitN(lines[1], "```", 2)[0])
		}
	}
	if title == "" {
		return fallback, nil
	}
	slog.Info("ai generate title: completed", "model", cfg.Model, "title", title)
	return title, nil
}

// SendStream is an internal legacy streaming helper. It is intentionally not
// part of the renderer/Wails surface; renderer callers must use StartStream so
// caller ownership, busy admission, and targeted event delivery are enforced.
//
//wails:ignore
func (a *AIService) SendStream(messages []ChatMessage, onChunk func(chunk string)) error {
	return a.SendStreamWithContext(context.Background(), messages, onChunk)
}

// parseSSEStream reads an OpenAI-style Server-Sent Events stream from r and
// invokes onChunk for each non-empty delta content chunk. It returns when the
// stream ends, [DONE] is received, or the underlying reader errors.
//
// N-83: JSON parse errors are logged with slog.Warn (with the data line
// truncated to 200 chars) instead of being silently skipped. After 5
// consecutive parse errors, an error is returned so the caller can surface
// "Provider returned malformed SSE stream" rather than appearing to succeed
// with no chunks emitted.
//
// H-7: use bufio.Scanner with a 1MB max line instead of unbounded ReadString.
// The aggregate limits below are independent of the provider's max_tokens
// promise: custom or compromised endpoints can ignore request-side limits.
const (
	maxAIStreamLineBytes          = 1 << 20
	maxAIStreamTotalBytes         = 8 << 20
	maxAIStreamLines              = 32768
	maxAIStreamTextBytes          = 2 << 20
	maxAIStreamReasoningBytes     = 512 << 10
	maxAIStreamToolCalls          = 64
	maxAIStreamToolIndex          = 4096
	maxAIStreamToolArgumentBytes  = 256 << 10
	maxAIStreamToolArgumentsBytes = 1 << 20
)

var ErrAIProviderOutputBudget = errors.New("AI provider output budget exceeded")

type aiProviderStreamBudget struct {
	totalSSEBytes     int
	lines             int
	textBytes         int
	reasoningBytes    int
	toolArgumentBytes int
	toolIndexes       map[int]struct{}
}

func consumeAIProviderBytes(used *int, amount, limit int, label string) error {
	if amount < 0 || *used < 0 || *used > limit || amount > limit-*used {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrAIProviderOutputBudget, label, limit)
	}
	*used += amount
	return nil
}

func (b *aiProviderStreamBudget) consumeSSELine(line string) error {
	if b.lines >= maxAIStreamLines {
		return fmt.Errorf("%w: SSE event count exceeds %d", ErrAIProviderOutputBudget, maxAIStreamLines)
	}
	b.lines++
	// Scanner strips the delimiter. Count one byte conservatively so streams
	// cannot evade the aggregate budget with arbitrarily many empty lines.
	return consumeAIProviderBytes(&b.totalSSEBytes, len(line)+1, maxAIStreamTotalBytes, "total SSE")
}

func (b *aiProviderStreamBudget) consumeText(text string) error {
	return consumeAIProviderBytes(&b.textBytes, len(text), maxAIStreamTextBytes, "text output")
}

func (b *aiProviderStreamBudget) consumeReasoning(summary string) error {
	return consumeAIProviderBytes(&b.reasoningBytes, len(summary), maxAIStreamReasoningBytes, "reasoning summary")
}

func validateAIProviderToolIndex(index int) error {
	if index < 0 || index > maxAIStreamToolIndex {
		return fmt.Errorf("%w: provider tool index %d is outside allowed range 0..%d", ErrAIProviderOutputBudget, index, maxAIStreamToolIndex)
	}
	return nil
}

func (b *aiProviderStreamBudget) registerTool(index int) error {
	if err := validateAIProviderToolIndex(index); err != nil {
		return err
	}
	if b.toolIndexes == nil {
		b.toolIndexes = make(map[int]struct{})
	}
	if _, exists := b.toolIndexes[index]; exists {
		return nil
	}
	if len(b.toolIndexes) >= maxAIStreamToolCalls {
		return fmt.Errorf("%w: provider tool call count exceeds limit of %d", ErrAIProviderOutputBudget, maxAIStreamToolCalls)
	}
	b.toolIndexes[index] = struct{}{}
	return nil
}

func (b *aiProviderStreamBudget) consumeToolArguments(existing int, fragment string) error {
	fragmentBytes := len(fragment)
	if existing < 0 || existing > maxAIStreamToolArgumentBytes || fragmentBytes > maxAIStreamToolArgumentBytes-existing {
		return fmt.Errorf("%w: provider tool arguments exceed per-call byte budget of %d bytes", ErrAIProviderOutputBudget, maxAIStreamToolArgumentBytes)
	}
	if err := consumeAIProviderBytes(&b.toolArgumentBytes, fragmentBytes, maxAIStreamToolArgumentsBytes, "total tool arguments"); err != nil {
		return err
	}
	return nil
}

// sseDataLine extracts one Server-Sent Events data field. The SSE grammar
// permits both `data: value` and `data:value`; only one optional leading space
// is stripped from the field value. Some OpenAI-compatible providers emit the
// latter form, so requiring the pretty-printed form would silently turn a
// valid stream into an empty assistant response.
func sseDataLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	value := line[len("data:"):]
	value = strings.TrimPrefix(value, " ")
	return value, true
}

// reasoningDetailText accepts only fields explicitly labelled by a provider
// as reasoning. It intentionally does not inspect ordinary content or infer
// hidden chain-of-thought from the assistant answer.
func reasoningDetailText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var items []struct {
		Text    string `json:"text"`
		Summary string `json:"summary"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &items) == nil {
		parts := make([]string, 0, len(items))
		for _, item := range items {
			value := item.Text
			if value == "" {
				value = item.Summary
			}
			if value == "" {
				value = item.Content
			}
			if value != "" {
				parts = append(parts, value)
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

type openAIToolAcc struct {
	id   string
	name string
	args strings.Builder
}

func parseSSEStream(r io.Reader, onChunk func(string)) error {
	_, err := parseSSEStreamWithTools(r, onChunk)
	return err
}

// parseSSEStreamWithTools is like parseSSEStream but also accumulates
// OpenAI-style delta.tool_calls for native function calling (prompt-5 Task H).
func parseSSEStreamWithTools(r io.Reader, onChunk func(string)) ([]NativeToolCall, error) {
	return parseSSEStreamWithToolsAndReasoning(r, onChunk, nil)
}

// parseSSEStreamWithToolsAndReasoning preserves the normal text/tool parser
// while exposing only provider-declared reasoning summaries. It never treats
// ordinary content as reasoning, so hidden chain-of-thought is not inferred.
func parseSSEStreamWithToolsAndReasoning(r io.Reader, onChunk, onReasoning func(string)) ([]NativeToolCall, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxAIStreamLineBytes)
	consecutiveErrors := 0
	const maxConsecutiveErrors = 5
	var budget aiProviderStreamBudget
	// index -> partial tool call (name/arguments may arrive across chunks)
	acc := map[int]*openAIToolAcc{}
	for scanner.Scan() {
		rawLine := scanner.Text()
		if err := budget.consumeSSELine(rawLine); err != nil {
			return nil, err
		}
		line := strings.TrimRight(rawLine, "\r\n")
		if data, ok := sseDataLine(line); ok {
			if data == "[DONE]" {
				return finalizeNativeToolCalls(acc), nil
			}
			var result struct {
				Choices []struct {
					Delta struct {
						Content          string          `json:"content"`
						ReasoningSummary string          `json:"reasoning_summary"`
						ReasoningContent string          `json:"reasoning_content"`
						Reasoning        string          `json:"reasoning"`
						Summary          string          `json:"summary"`
						ReasoningDetails json.RawMessage `json:"reasoning_details"`
						ToolCalls        []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Type     string `json:"type"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if perr := json.Unmarshal([]byte(data), &result); perr != nil {
				// N-83: log the parse error instead of silently
				// skipping. Truncate the data to 200 chars to avoid
				// flooding logs with large payloads.
				preview := data
				if len(preview) > 200 {
					preview = preview[:200] + "..."
				}
				slog.Warn("ai sse: failed to parse data line", "err", perr, "preview", preview)
				consecutiveErrors++
				if consecutiveErrors >= maxConsecutiveErrors {
					return nil, fmt.Errorf("provider returned %d consecutive malformed SSE chunks (last error: %w); check base URL compatibility", consecutiveErrors, perr)
				}
			} else {
				consecutiveErrors = 0
				if len(result.Choices) > 0 {
					delta := result.Choices[0].Delta
					if delta.Content != "" {
						if err := budget.consumeText(delta.Content); err != nil {
							return nil, err
						}
						onChunk(delta.Content)
					}
					reasoning := delta.ReasoningSummary
					if reasoning == "" {
						reasoning = delta.ReasoningContent
					}
					if reasoning == "" {
						reasoning = delta.Reasoning
					}
					if reasoning == "" {
						reasoning = delta.Summary
					}
					if reasoning == "" {
						reasoning = reasoningDetailText(delta.ReasoningDetails)
					}
					if reasoning != "" {
						if err := budget.consumeReasoning(reasoning); err != nil {
							return nil, err
						}
						if onReasoning != nil {
							onReasoning(reasoning)
						}
					}
					for _, tc := range delta.ToolCalls {
						if err := budget.registerTool(tc.Index); err != nil {
							return nil, err
						}
						cur, ok := acc[tc.Index]
						if !ok {
							cur = &openAIToolAcc{}
							acc[tc.Index] = cur
						}
						if tc.ID != "" {
							if len(tc.ID) > maxAIInputToolIDBytes {
								return nil, fmt.Errorf("%w: provider tool ID exceeds %d bytes", ErrAIProviderOutputBudget, maxAIInputToolIDBytes)
							}
							cur.id = tc.ID
						}
						if tc.Function.Name != "" {
							if len(tc.Function.Name) > maxAIInputToolNameBytes {
								return nil, fmt.Errorf("%w: provider tool name exceeds %d bytes", ErrAIProviderOutputBudget, maxAIInputToolNameBytes)
							}
							cur.name = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							if err := budget.consumeToolArguments(cur.args.Len(), tc.Function.Arguments); err != nil {
								return nil, err
							}
							_, _ = cur.args.WriteString(tc.Function.Arguments)
						}
					}
				}
			}
		}
	}
	// N-108: treat wrapped io.EOF as clean end; H-7: ErrTooLong if line > 1MB.
	if err := scanner.Err(); err != nil {
		if errors.Is(err, io.EOF) {
			return finalizeNativeToolCalls(acc), nil
		}
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("%w: SSE line exceeds 1MB limit: %v", ErrAIProviderOutputBudget, err)
		}
		return nil, err
	}
	return finalizeNativeToolCalls(acc), nil
}

func finalizeNativeToolCalls(acc map[int]*openAIToolAcc) []NativeToolCall {
	if len(acc) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(acc))
	for i := range acc {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	out := make([]NativeToolCall, 0, len(acc))
	for _, i := range indexes {
		if tc, ok := acc[i]; ok && tc.name != "" {
			arguments := tc.args.String()
			if arguments == "" {
				arguments = "{}"
			}
			out = append(out, NativeToolCall{ID: tc.id, Name: tc.name, Arguments: arguments})
		}
	}
	return out
}

// anthropicToolAcc accumulates a single Anthropic tool_use content block
// across content_block_start / input_json_delta events (prompt-6 Task 3).
type anthropicToolAcc struct {
	id   string
	name string
	args strings.Builder
}

// parseAnthropicSSEStream reads an Anthropic-style Server-Sent Events stream
// from r and invokes onChunk for each text delta.
func parseAnthropicSSEStream(r io.Reader, onChunk func(string)) error {
	_, err := parseAnthropicSSEStreamWithTools(r, onChunk)
	return err
}

// parseAnthropicSSEStreamWithTools is like parseAnthropicSSEStream but also
// accumulates native tool_use blocks (prompt-6 Task 3 / BUG-H5).
//
// Relevant Anthropic SSE events:
//   - content_block_start  when content_block.type == tool_use (id, name)
//   - content_block_delta  text_delta | input_json_delta
//   - content_block_stop   finalizes the current block
//   - message_stop         stream done
func parseAnthropicSSEStreamWithTools(r io.Reader, onChunk func(string)) ([]NativeToolCall, error) {
	return parseAnthropicSSEStreamWithToolsAndReasoning(r, onChunk, nil)
}

// parseAnthropicSSEStreamWithToolsAndReasoning reports only explicit
// `thinking` content blocks/deltas as reasoning summaries.
func parseAnthropicSSEStreamWithToolsAndReasoning(r io.Reader, onChunk, onReasoning func(string)) ([]NativeToolCall, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxAIStreamLineBytes)
	consecutiveErrors := 0
	const maxConsecutiveErrors = 5
	var budget aiProviderStreamBudget

	toolsByIndex := map[int]*anthropicToolAcc{}
	openToolIndex := -1

	for scanner.Scan() {
		rawLine := scanner.Text()
		if err := budget.consumeSSELine(rawLine); err != nil {
			return nil, err
		}
		line := strings.TrimRight(rawLine, "\r\n")
		if data, ok := sseDataLine(line); ok {
			var evt struct {
				Type         string `json:"type"`
				Index        int    `json:"index"`
				ContentBlock struct {
					Type     string          `json:"type"`
					ID       string          `json:"id"`
					Name     string          `json:"name"`
					Summary  string          `json:"summary"`
					Thinking string          `json:"thinking"`
					Input    json.RawMessage `json:"input"`
				} `json:"content_block"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					Summary     string `json:"summary"`
					Thinking    string `json:"thinking"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if perr := json.Unmarshal([]byte(data), &evt); perr != nil {
				preview := data
				if len(preview) > 200 {
					preview = preview[:200] + "..."
				}
				slog.Warn("ai anthropic sse: failed to parse data line", "err", perr, "preview", preview)
				consecutiveErrors++
				if consecutiveErrors >= maxConsecutiveErrors {
					return finalizeAnthropicNativeTools(toolsByIndex), fmt.Errorf("provider returned %d consecutive malformed SSE chunks (last error: %w); check base URL compatibility", consecutiveErrors, perr)
				}
			} else {
				consecutiveErrors = 0
				switch evt.Type {
				case "content_block_start":
					reasoning := ""
					if evt.ContentBlock.Type == "reasoning_summary" {
						reasoning = evt.ContentBlock.Summary
					} else if evt.ContentBlock.Type == "thinking" {
						reasoning = evt.ContentBlock.Thinking
					}
					if reasoning != "" {
						if err := budget.consumeReasoning(reasoning); err != nil {
							return nil, err
						}
						if onReasoning != nil {
							onReasoning(reasoning)
						}
					}
					if evt.ContentBlock.Type == "tool_use" {
						if err := budget.registerTool(evt.Index); err != nil {
							return nil, err
						}
						if evt.ContentBlock.ID == "" || len(evt.ContentBlock.ID) > maxAIInputToolIDBytes {
							return nil, errors.New("provider returned a missing or oversized Anthropic tool ID")
						}
						if evt.ContentBlock.Name == "" || len(evt.ContentBlock.Name) > maxAIInputToolNameBytes {
							return nil, errors.New("provider returned a missing or oversized Anthropic tool name")
						}
						tool := &anthropicToolAcc{
							id:   evt.ContentBlock.ID,
							name: evt.ContentBlock.Name,
						}
						if len(evt.ContentBlock.Input) > 0 && string(evt.ContentBlock.Input) != "null" {
							if err := budget.consumeToolArguments(0, string(evt.ContentBlock.Input)); err != nil {
								return nil, err
							}
							_, _ = tool.args.Write(evt.ContentBlock.Input)
						}
						toolsByIndex[evt.Index] = tool
						openToolIndex = evt.Index
					} else {
						openToolIndex = -1
					}
				case "content_block_delta":
					switch evt.Delta.Type {
					case "text_delta":
						if evt.Delta.Text != "" {
							if err := budget.consumeText(evt.Delta.Text); err != nil {
								return nil, err
							}
							onChunk(evt.Delta.Text)
						}
					case "reasoning_summary_delta", "thinking_delta":
						reasoning := evt.Delta.Summary
						if reasoning == "" {
							reasoning = evt.Delta.Thinking
						}
						if reasoning != "" {
							if err := budget.consumeReasoning(reasoning); err != nil {
								return nil, err
							}
							if onReasoning != nil {
								onReasoning(reasoning)
							}
						}
					case "input_json_delta":
						idx := evt.Index
						if err := validateAIProviderToolIndex(idx); err != nil {
							return nil, err
						}
						if acc, ok := toolsByIndex[idx]; ok {
							if err := budget.consumeToolArguments(acc.args.Len(), evt.Delta.PartialJSON); err != nil {
								return nil, err
							}
							_, _ = acc.args.WriteString(evt.Delta.PartialJSON)
						} else if openToolIndex >= 0 {
							if acc, ok := toolsByIndex[openToolIndex]; ok {
								if err := budget.consumeToolArguments(acc.args.Len(), evt.Delta.PartialJSON); err != nil {
									return nil, err
								}
								_, _ = acc.args.WriteString(evt.Delta.PartialJSON)
							}
						}
					}
				case "content_block_stop":
					openToolIndex = -1
				case "message_stop":
					return finalizeAnthropicNativeTools(toolsByIndex), nil
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, io.EOF) {
			return finalizeAnthropicNativeTools(toolsByIndex), nil
		}
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("%w: SSE line exceeds 1MB limit: %v", ErrAIProviderOutputBudget, err)
		}
		return finalizeAnthropicNativeTools(toolsByIndex), err
	}
	return finalizeAnthropicNativeTools(toolsByIndex), nil
}

// finalizeAnthropicNativeTools converts index-keyed tool accumulators into
// the shared NativeToolCall slice (prompt-6 Task 3).
func finalizeAnthropicNativeTools(acc map[int]*anthropicToolAcc) []NativeToolCall {
	if len(acc) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(acc))
	for i := range acc {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	out := make([]NativeToolCall, 0, len(acc))
	for _, i := range indexes {
		if tc, ok := acc[i]; ok && tc != nil && tc.name != "" {
			arguments := tc.args.String()
			if arguments == "" {
				arguments = "{}"
			}
			out = append(out, NativeToolCall{
				ID:        tc.id,
				Name:      tc.name,
				Arguments: arguments,
			})
		}
	}
	return out
}

// SendStreamWithContext is an internal provider helper. It is intentionally
// hidden from the renderer/Wails surface; it does not provide the renderer
// stream ownership and event transport contract.
//
// N-93: takes a snapshot at the start; uses snap.config throughout.
//
//wails:ignore
func (a *AIService) SendStreamWithContext(ctx context.Context, messages []ChatMessage, onChunk func(chunk string)) (returnErr error) {
	snap, workspaceLease, err := a.snapshotForProviderCall()
	if err != nil {
		return err
	}
	defer workspaceLease.release()
	cfg := snap.config
	unit, err := beginChatLifecycleWithWorkspaceLease(snap, newStreamID(), messages, workspaceLease)
	if err != nil {
		return err
	}
	var output strings.Builder
	defer func() {
		response := &ChatResponse{Content: output.String()}
		if lifecycleErr := unit.finish(response, returnErr); lifecycleErr != nil && returnErr == nil {
			returnErr = lifecycleErr
		}
	}()
	if cfg.APIKey == "" {
		return errors.New("API key not configured")
	}

	// N-61: prepareMessagesWith applies context-window truncation to prevent
	// long conversations from exceeding the model's token limit.
	fullMessages, inputTokens, err := a.prepareMessagesForCall(snap, messages)
	if unit != nil && err == nil {
		unit.inputTokens = inputTokens
	}
	if err != nil {
		return err
	}
	// G12: image attachments are validated before any provider call.
	if err := validateOutboundChatMessages(fullMessages); err != nil {
		return err
	}

	// Anthropic protocol branch: /v1/messages + x-api-key, system prompt
	// lifted to top-level field, SSE parsed as Anthropic events.
	if isAnthropicProtocol(cfg) {
		systemPrompt, chatMessages := splitSystemPrompt(fullMessages)
		reqBody := map[string]interface{}{
			"model":       cfg.Model,
			"max_tokens":  maxTokensFrom(cfg),
			"temperature": effectiveTemperature(cfg),
			"system":      systemPrompt,
			"messages":    anthropicMessages(chatMessages),
			"stream":      true,
		}
		if err := applyReasoningRequestConfig(reqBody, cfg); err != nil {
			return err
		}
		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		req, err := http.NewRequest("POST", JoinAIEndpoint(cfg.BaseURL, "/v1/messages"), bytes.NewReader(bodyBytes))
		if err != nil {
			return err
		}
		req = req.WithContext(ctx)
		setProtocolHeaders(req, cfg)
		req.Header.Set("Accept", "text/event-stream")

		resp, err := aiStreamHTTPClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return parseAIError(resp)
		}
		return parseAnthropicSSEStream(resp.Body, func(chunk string) {
			output.WriteString(chunk)
			onChunk(chunk)
		})
	}

	reqBody := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    openAIMessages(fullMessages),
		"stream":      true,
		"max_tokens":  maxTokensFrom(cfg), // N-65: bound response length
		"temperature": effectiveTemperature(cfg),
	}
	if err := applyReasoningRequestConfig(reqBody, cfg); err != nil {
		return err
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", JoinAIEndpoint(cfg.BaseURL, "/v1/chat/completions"), bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	setCommonHeaders(req, cfg.APIKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := aiStreamHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAIError(resp)
	}

	return parseSSEStream(resp.Body, func(chunk string) {
		output.WriteString(chunk)
		onChunk(chunk)
	})
}

// ErrStreamBusy is returned by StartStream when another stream is already
// active (prompt-5 Task B / BUG-H1: mutual exclusion across main + AI windows).
var ErrStreamBusy = errors.New("another AI stream is already in progress; stop it before starting a new one")

// ErrAIStreamShutdownTimeout means teardown could not observe the provider
// worker's terminal publication within the bounded shutdown window. Callers
// must treat the associated session as indeterminate rather than proceeding as
// if the stream had been cleanly closed.
var ErrAIStreamShutdownTimeout = errors.New("AI stream shutdown did not complete within the configured timeout")

var (
	ErrAIStreamWallTimeout = errors.New("AI provider stream exceeded its total time limit")
	ErrAIStreamIdleTimeout = errors.New("AI provider stream stopped producing data")
)

const (
	defaultAIStreamShutdownTimeout = 10 * time.Second
	defaultAIStreamWallTimeout     = 10 * time.Minute
	defaultAIStreamIdleTimeout     = 90 * time.Second
)

func (a *AIService) streamDeadlineDurations() (time.Duration, time.Duration) {
	a.mu.RLock()
	wallTimeout := a.streamWallTimeout
	idleTimeout := a.streamIdleTimeout
	a.mu.RUnlock()
	if wallTimeout <= 0 {
		wallTimeout = defaultAIStreamWallTimeout
	}
	if idleTimeout <= 0 {
		idleTimeout = defaultAIStreamIdleTimeout
	}
	return wallTimeout, idleTimeout
}

func newAIStreamDeadlineContext(wallTimeout, idleTimeout time.Duration) (context.Context, context.CancelFunc, func()) {
	wallCtx, cancelWall := context.WithTimeoutCause(context.Background(), wallTimeout, ErrAIStreamWallTimeout)
	ctx, cancelCause := context.WithCancelCause(wallCtx)
	cancel := func() {
		cancelCause(context.Canceled)
		cancelWall()
	}
	activity := make(chan struct{}, 1)
	go func() {
		defer RecoverGoroutinePanic("ai:stream-idle-pump")
		timer := time.NewTimer(idleTimeout)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-activity:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idleTimeout)
			case <-timer.C:
				cancelCause(ErrAIStreamIdleTimeout)
				return
			}
		}
	}()
	return ctx, cancel, func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}
}

type aiStreamActivityReader struct {
	reader   io.Reader
	activity func()
}

func (r *aiStreamActivityReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if n > 0 && r.activity != nil {
		r.activity()
	}
	return n, err
}

func waitForAIStreamShutdown(done <-chan struct{}, timeout time.Duration) error {
	if done == nil {
		return fmt.Errorf("AI stream completion barrier is unavailable: %w", ErrAIStreamShutdownTimeout)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return ErrAIStreamShutdownTimeout
	}
}

func (a *AIService) cancelSessionStreamAndWait(sessionID string) error {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	a.mu.Lock()
	active := a.cancel
	if active == nil || active.lifecycleID != strings.TrimSpace(sessionID) {
		a.mu.Unlock()
		return nil
	}
	done := active.done
	cancel := active.fn
	timeout := a.streamShutdownTimeout
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if timeout <= 0 {
		timeout = defaultAIStreamShutdownTimeout
	}
	return waitForAIStreamShutdown(done, timeout)
}

func (a *AIService) cancelAllStreamsAndWait() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	// Seal admission in the same critical section that snapshots the active
	// worker. A concurrent Start either claims the slot first and is drained
	// below, or observes the seal and is rejected.
	a.streamAdmissionClosed = true
	active := a.cancel
	timeout := a.streamShutdownTimeout
	a.mu.Unlock()
	if active == nil {
		return nil
	}
	if active.fn != nil {
		active.fn()
	}
	if timeout <= 0 {
		timeout = defaultAIStreamShutdownTimeout
	}
	return waitForAIStreamShutdown(active.done, timeout)
}

// AgentStreamStart identifies both the event stream and the persistent Agent
// session that owns tool calls emitted by that stream.
type AgentStreamStart struct {
	StreamID  string `json:"streamId"`
	SessionID string `json:"sessionId"`
}

// StartStream begins an async streaming request. Chunks are emitted via the
// "ai:chunk" event; completion via "ai:done"; errors via "ai:error".
// Returns the streamId immediately after starting the goroutine (prompt-6 Task 2).
//
// prompt-5 Task B / BUG-H1: if a stream is already running, returns
// ErrStreamBusy instead of cancelling the previous stream. This prevents
// dual-window interleaving where chunks from two conversations would
// corrupt each other's UI. Call StopStream first to replace a stream.
//
// N-93: a snapshot of the config and app is taken before the goroutine
// launches, so a concurrent SetConfig call cannot race with the goroutine's
// reads. The goroutine uses the snapshot exclusively.
func (a *AIService) StartStream(ctx context.Context, messages []ChatMessage) (string, error) {
	owner, ok := agentOwnerForContext(ctx)
	if !ok {
		return "", fmt.Errorf("AI stream caller identity is unavailable: %w", ErrNotAllowed)
	}
	target, ok := agentWindowForContext(ctx)
	if !ok || target == nil {
		return "", fmt.Errorf("AI stream renderer target is unavailable: %w", ErrNotAllowed)
	}
	snap, workspaceLease, err := a.snapshotForProviderCall()
	if err != nil {
		return "", err
	}
	snap, err = a.admitProviderOperation(snap, AIOpChat)
	if err != nil {
		workspaceLease.release()
		return "", err
	}
	streamID, err := a.startStreamWithSnapshot(messages, owner, target, "", false, snap, workspaceLease, nil)
	if err != nil {
		workspaceLease.release()
	}
	return streamID, err
}

// StartAgentStream starts an Agent-mode stream bound to a backend-issued
// persistent session. The session remains running across tool observations;
// a later call may reuse the same ID, while forged IDs are rejected by the
// shared lifecycle/runtime gate.
func (a *AIService) StartAgentStream(ctx context.Context, sessionID string, messages []ChatMessage) (AgentStreamStart, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return AgentStreamStart{}, fmt.Errorf("agent session ID is required: %w", ErrInvalidInput)
	}
	owner, ok := agentOwnerForContext(ctx)
	if !ok {
		return AgentStreamStart{}, fmt.Errorf("Agent stream caller identity is unavailable: %w", ErrNotAllowed)
	}
	target, ok := agentWindowForContext(ctx)
	if !ok || target == nil {
		return AgentStreamStart{}, fmt.Errorf("Agent stream renderer target is unavailable: %w", ErrNotAllowed)
	}
	a.mu.RLock()
	agent := a.agent
	a.mu.RUnlock()
	if agent == nil {
		return AgentStreamStart{}, fmt.Errorf("Agent stream owner authority is unavailable: %w", ErrNotAllowed)
	}
	if err := authorizeAgentSessionOwner(agent, ctx, sessionID); err != nil {
		return AgentStreamStart{}, err
	}
	releaseSessionAdmission, err := acquireAgentSessionAdmission(agent, sessionID)
	if err != nil {
		return AgentStreamStart{}, err
	}
	defer releaseSessionAdmission()
	snap, workspaceLease, err := a.snapshotForProviderCall()
	if err != nil {
		return AgentStreamStart{}, err
	}
	defer func() { workspaceLease.release() }()
	snap, err = a.admitProviderOperation(snap, AIOpChat)
	if err != nil {
		return AgentStreamStart{}, err
	}
	if snap.lifecycle == nil {
		return AgentStreamStart{}, fmt.Errorf("agent lifecycle is unavailable: %w", ErrNotAllowed)
	}
	startedAt := snap.lifecycle.now()
	lifecycleStarted := false
	streamID, err := a.startStreamWithSnapshot(messages, owner, target, sessionID, true, snap, workspaceLease, func() error {
		if _, beginErr := snap.lifecycle.beginExistingWithinWorkspaceAuthority(agentcore.SessionChat, sessionID); beginErr != nil {
			return beginErr
		}
		lifecycleStarted = true
		return nil
	})
	if err != nil {
		// A busy rejection happens before lifecycle preparation. It is not an
		// execution unit and must not resume or fail either conversation.
		if lifecycleStarted && !errors.Is(err, ErrStreamBusy) {
			// startStream can fail before the streaming goroutine creates its
			// request-started checkpoint (for example when provider settings are
			// incomplete). Persist a minimal recovery point before failing so a
			// corrected retry can resume the same backend-owned session.
			_, checkpointErr := snap.lifecycle.checkpointWithinWorkspaceAuthority(agentcore.SessionChat, sessionID, "stream-start-failed", map[string]interface{}{
				"phase": "stream-start-failed",
			})
			completedAt := snap.lifecycle.now()
			tokensIn := snap.lifecycle.estimateMessages(withSystemPromptFrom(snap.config, messages))
			basis := agentcore.CostNotApplicable
			estimated := false
			if tokensIn > 0 {
				basis = agentcore.CostEstimated
				estimated = true
			}
			meterErr := snap.lifecycle.recordWithinWorkspaceAuthority(agentcore.UsageRecord{
				SessionID: sessionID, UnitKind: agentcore.UsageUnitChat, Operation: string(AIOpChat),
				ProviderID: usageProviderID(snap.config), Model: snap.config.Model,
				TokensIn: tokensIn, CostBasis: basis, Estimated: estimated,
				StartedAt: startedAt, CompletedAt: completedAt, Success: false, Error: err.Error(),
			})
			failErr := snap.lifecycle.failWithinWorkspaceAuthority(agentcore.SessionChat, sessionID, err)
			return AgentStreamStart{}, errors.Join(err, checkpointErr, meterErr, failErr)
		}
		return AgentStreamStart{}, err
	}
	// The worker now owns the same lease used for snapshot and preflight. The
	// deferred release above sees nil; the worker's idempotent release runs only
	// after lifecycle terminal publication.
	workspaceLease = nil
	return AgentStreamStart{StreamID: streamID, SessionID: sessionID}, nil
}

func (a *AIService) startStreamWithSnapshot(
	messages []ChatMessage,
	owner agentSessionOwner,
	target application.Window,
	lifecycleID string,
	persistentLifecycle bool,
	snap aiSnapshot,
	workspaceLease *agentWorkspaceAuthorityReadLease,
	prepare func() error,
) (string, error) {
	streamID := newStreamID()
	if lifecycleID == "" {
		lifecycleID = streamID
	}
	wallTimeout, idleTimeout := a.streamDeadlineDurations()
	ctx, cancel, streamActivity := newAIStreamDeadlineContext(wallTimeout, idleTimeout)
	sc := &streamCancel{fn: cancel, owner: owner, target: target, lifecycleID: lifecycleID, done: make(chan struct{})}
	// Claim the global stream slot before lifecycle or provider preflight. This
	// makes a busy rejection side-effect free for every persistent session.
	a.mu.Lock()
	if a.streamAdmissionClosed {
		a.mu.Unlock()
		cancel()
		return "", fmt.Errorf("AI stream admission is closed: %w", ErrNotAllowed)
	}
	if a.cancel != nil {
		a.mu.Unlock()
		cancel()
		slog.Warn("ai startstream: rejected, stream already active")
		return "", ErrStreamBusy
	}
	a.cancel = sc
	a.activeStreamID = streamID
	a.mu.Unlock()
	releaseClaim := func() {
		cancel()
		a.mu.Lock()
		if a.cancel == sc {
			a.cancel = nil
			if a.activeStreamID == streamID {
				a.activeStreamID = ""
			}
		}
		a.mu.Unlock()
		sc.markDone()
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			releaseClaim()
			return "", err
		}
	}
	if snap.config.APIKey == "" {
		releaseClaim()
		slog.Error("ai startstream: api key not configured")
		return "", errors.New("API key not configured")
	}
	if snap.app == nil {
		releaseClaim()
		slog.Error("ai startstream: app not initialized")
		return "", errors.New("application not initialized")
	}

	// Notify all webviews that a global stream is busy (UI can disable send).
	emitAIStreamBusy(snap.app, "ai:stream-busy", map[string]interface{}{"busy": true})

	slog.Info("ai startstream: starting", "model", snap.config.Model, "messages", len(messages), "streamId", streamID)

	go func() {
		defer RecoverGoroutinePanic("ai:stream-worker")
		defer func() {
			cancel()
			workspaceLease.release()
			sc.markDone()
		}()
		// N-52: compare-and-swap cleanup. Only clear a.cancel if it
		// still points to OUR streamCancel. Without this check, the
		// following race would lose a newer stream's cancel:
		//   1. Stream A finishes, goroutine A enters defer.
		//   2. Stream B starts, stores its own streamCancel in a.cancel.
		//   3. Goroutine A's defer unconditionally sets a.cancel = nil,
		//      overwriting B - Stream B can no longer be stopped.
		defer func() {
			a.mu.Lock()
			if a.cancel == sc {
				a.cancel = nil
				if a.activeStreamID == streamID {
					a.activeStreamID = ""
				}
			}
			stillBusy := a.cancel != nil
			a.mu.Unlock()
			if !stillBusy {
				emitAIStreamBusy(snap.app, "ai:stream-busy", map[string]interface{}{"busy": false})
			}
		}()

		err := a.streamWithEvents(ctx, messages, snap, target, streamID, lifecycleID, persistentLifecycle, workspaceLease, streamActivity)
		if err != nil {
			cause := context.Cause(ctx)
			if errors.Is(cause, ErrAIStreamWallTimeout) || errors.Is(cause, ErrAIStreamIdleTimeout) {
				err = cause
			}
			slog.Error("ai startstream: failed", "model", snap.config.Model, "err", err, "streamId", streamID)
			emitAIStreamEvent(target, "ai:error", streamID, map[string]interface{}{"data": err.Error()})
			return
		}
		slog.Info("ai startstream: completed", "model", snap.config.Model, "streamId", streamID)
		emitAIStreamEvent(target, "ai:done", streamID, map[string]interface{}{"data": ""})
	}()

	return streamID, nil
}

// IsStreaming reports whether a stream is currently active (prompt-5 Task B).
func (a *AIService) IsStreaming() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancel != nil
}

// streamWithEvents sends the streaming request and emits "ai:chunk" for each chunk.
// N-93: uses the provided snapshot instead of reading a.config / a.app, so
// concurrent SetConfig calls do not cause data races.
// prompt-6 Task 2: streamID is attached to every emitted event payload.
func (a *AIService) streamWithEvents(ctx context.Context, messages []ChatMessage, snap aiSnapshot, target application.Window, streamID, lifecycleID string, persistentLifecycle bool, workspaceLease *agentWorkspaceAuthorityReadLease, streamActivity func()) (returnErr error) {
	cfg := snap.config
	if lifecycleID == "" {
		lifecycleID = streamID
	}
	var unit *chatLifecycleUnit
	var err error
	if persistentLifecycle {
		unit, err = beginPersistentChatLifecycleWithWorkspaceLease(snap, lifecycleID, messages, workspaceLease)
	} else {
		unit, err = beginChatLifecycleWithWorkspaceLease(snap, lifecycleID, messages, workspaceLease)
	}
	if err != nil {
		return err
	}
	var output strings.Builder
	defer func() {
		response := &ChatResponse{Content: output.String()}
		if lifecycleErr := unit.finish(response, returnErr); lifecycleErr != nil && returnErr == nil {
			returnErr = lifecycleErr
		}
	}()
	// N-61: prepareMessages applies context-window truncation to prevent
	// long conversations from exceeding the model's token limit.
	fullMessages, inputTokens, err := a.prepareMessagesForCall(snap, messages)
	if unit != nil && err == nil {
		unit.inputTokens = inputTokens
	}
	if err != nil {
		return err
	}
	// G12: image attachments are validated before any provider call.
	if err := validateOutboundChatMessages(fullMessages); err != nil {
		return err
	}
	maxTok := defaultChatMaxTokens
	if cfg.MaxTokens > 0 {
		maxTok = cfg.MaxTokens
	}

	if isAnthropicProtocol(cfg) {
		systemPrompt, chatMessages := splitSystemPrompt(fullMessages)
		reqBody := map[string]interface{}{
			"model":       cfg.Model,
			"max_tokens":  maxTok,
			"temperature": effectiveTemperature(cfg),
			"system":      systemPrompt,
			"messages":    anthropicMessages(chatMessages),
			"stream":      true,
		}
		if err := applyReasoningRequestConfig(reqBody, cfg); err != nil {
			return err
		}

		// prompt-5 Task H: Anthropic tools shape (name/description/input_schema).
		if len(cfg.Tools) > 0 {
			anthTools := make([]map[string]interface{}, 0, len(cfg.Tools))
			for _, t := range cfg.Tools {
				params := t.Function.Parameters
				if params == nil {
					params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
				}
				anthTools = append(anthTools, map[string]interface{}{
					"name":         t.Function.Name,
					"description":  t.Function.Description,
					"input_schema": params,
				})
			}
			reqBody["tools"] = anthTools
		}
		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		req, err := http.NewRequest("POST", JoinAIEndpoint(cfg.BaseURL, "/v1/messages"), bytes.NewReader(bodyBytes))
		if err != nil {
			return err
		}
		req = req.WithContext(ctx)
		setProtocolHeaders(req, cfg)
		req.Header.Set("Accept", "text/event-stream")

		resp, err := aiStreamHTTPClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return parseAIError(resp)
		}

		// prompt-6 Task 3: full Anthropic tool_use streaming + text dual-track.
		streamReader := &aiStreamActivityReader{reader: resp.Body, activity: streamActivity}
		toolCalls, perr := parseAnthropicSSEStreamWithToolsAndReasoning(streamReader, func(chunk string) {
			output.WriteString(chunk)
			emitAIStreamEvent(target, "ai:chunk", streamID, map[string]interface{}{"data": chunk})
		}, func(summary string) {
			emitAIStreamEvent(target, "ai:reasoning", streamID, map[string]interface{}{"data": summary})
		})
		if perr != nil {
			return perr
		}
		if len(toolCalls) > 0 {
			if err := validateNativeToolCallBatch(toolCalls); err != nil {
				return err
			}
			payload, merr := json.Marshal(toolCalls)
			if merr == nil {
				emitAIStreamEvent(target, "ai:tool_calls", streamID, map[string]interface{}{"data": string(payload)})
			}
		}
		return nil
	}

	reqBody := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    openAIMessages(fullMessages),
		"stream":      true,
		"max_tokens":  maxTok, // N-65: bound response length
		"temperature": effectiveTemperature(cfg),
	}
	if err := applyReasoningRequestConfig(reqBody, cfg); err != nil {
		return err
	}
	// prompt-5 Task H: OpenAI-compatible tools + auto tool_choice.
	if len(cfg.Tools) > 0 {
		reqBody["tools"] = cfg.Tools
		reqBody["tool_choice"] = "auto"
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", JoinAIEndpoint(cfg.BaseURL, "/v1/chat/completions"), bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	setCommonHeaders(req, cfg.APIKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := aiStreamHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAIError(resp)
	}

	streamReader := &aiStreamActivityReader{reader: resp.Body, activity: streamActivity}
	toolCalls, err := parseSSEStreamWithToolsAndReasoning(streamReader, func(chunk string) {
		output.WriteString(chunk)
		emitAIStreamEvent(target, "ai:chunk", streamID, map[string]interface{}{"data": chunk})
	}, func(summary string) {
		emitAIStreamEvent(target, "ai:reasoning", streamID, map[string]interface{}{"data": summary})
	})
	if err != nil {
		return err
	}
	if len(toolCalls) > 0 {
		if err := validateNativeToolCallBatch(toolCalls); err != nil {
			return err
		}
		payload, merr := json.Marshal(toolCalls)
		if merr == nil {
			emitAIStreamEvent(target, "ai:tool_calls", streamID, map[string]interface{}{"data": string(payload)})
		}
	}
	return nil
}

// StopStream cancels only the stream owned by the calling renderer window.
func (a *AIService) StopStream(ctx context.Context) error {
	caller, ok := agentOwnerForContext(ctx)
	if !ok {
		return fmt.Errorf("AI stream caller identity is unavailable: %w", ErrNotAllowed)
	}
	a.mu.Lock()
	active := a.cancel
	if active != nil && (active.owner.trusted || active.owner.identity != caller.identity) {
		a.mu.Unlock()
		return fmt.Errorf("AI stream belongs to another caller: %w", ErrNotAllowed)
	}
	a.mu.Unlock()
	if active != nil {
		// The worker owns slot cleanup. Retaining the identity until its deferred
		// lifecycle finishes prevents an old cancelled stream from failing a new
		// stream that reuses the same persistent session.
		active.fn()
	}
	return nil
}

// ModelInfo represents a model entry from the /v1/models endpoint
// (OpenAI-compatible). N-50/Proposal S.
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// ListModels fetches the list of available models from the provider's
// /v1/models endpoint (OpenAI-compatible). This allows the frontend to
// refresh the model dropdown with current models instead of relying on
// the hardcoded PROVIDER_PRESETS (N-50/Proposal S).
//
// If the request fails or the endpoint is unavailable, an error is
// returned. The frontend falls back to the hardcoded preset list.
//
// N-73: baseURL is validated via ValidateBaseURL before any HTTP request
// is made. This prevents the API key from being sent to a malicious URL
// (e.g. file:, data:, http on a non-loopback host, or a URL with embedded
// credentials).
func (a *AIService) ListModels(baseURL, apiKey string) ([]string, error) {
	if err := ValidateBaseURL(baseURL); err != nil {
		slog.Warn("ai listmodels: rejected base URL", "baseURL", baseURL, "err", err)
		return nil, err
	}
	// CRIT-01/G-SEC-07: when the caller passes an empty apiKey, fall back to
	// the backend's stored key so the frontend never has to hold plaintext.
	// Resolution order:
	//   1. a.config.APIKey (populated by SetConfig via UseStoredKey path)
	//   2. SettingsService.getAPIKeyForConfig(a.config.ConfigID)
	// If neither yields a key, the request is sent without auth - this
	// preserves the local-provider (Ollama) behavior covered by
	// TestAIService_ListModels_NoAPIKey.
	if apiKey == "" {
		a.mu.RLock()
		if a.config.APIKey != "" {
			apiKey = a.config.APIKey
		} else if a.config.ConfigID != "" && a.settingsService != nil {
			if key, kerr := a.settingsService.getAPIKeyForConfig(a.config.ConfigID); kerr == nil && key != "" {
				apiKey = key
			}
		}
		a.mu.RUnlock()
	}
	req, err := http.NewRequest("GET", JoinAIEndpoint(baseURL, "/v1/models"), nil)
	if err != nil {
		slog.Error("ai listmodels: request build failed", "baseURL", baseURL, "err", err)
		return nil, err
	}
	if apiKey != "" {
		setCommonHeaders(req, apiKey)
	} else {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", aiUserAgent)
	}

	resp, err := aiHTTPClient.Do(req)
	if err != nil {
		slog.Error("ai listmodels: http request failed", "baseURL", baseURL, "err", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseAIError(resp)
	}

	var result struct {
		Data []ModelInfo `json:"data"`
	}
	if err := decodeAIJSONResponse(resp.Body, &result); err != nil {
		slog.Error("ai listmodels: decode failed", "err", err)
		return nil, err
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	slog.Info("ai listmodels: completed", "baseURL", baseURL, "count", len(models))
	return models, nil
}
