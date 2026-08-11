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
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const aiUserAgent = "koyori-ide/1.0 (Wails3 Desktop IDE)"

// noRedirectPolicy prevents the HTTP client from following redirects.
func noRedirectPolicy(req *http.Request, via []*http.Request) error {
	return http.ErrUseLastResponse
}

// aiTransport is a shared transport with connection-level timeouts.
//   - DialContext: 10s to establish the TCP connection
//   - TLSHandshakeTimeout: 10s for TLS
//   - ResponseHeaderTimeout: 30s for the server to send response headers
//   - IdleConnTimeout: 90s (default-like)
var aiTransport = &http.Transport{
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	IdleConnTimeout:       90 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

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
		return fmt.Errorf("AI API error (status %d): %s", resp.StatusCode, aiErr.Error.Message)
	}
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf(
			"AI API returned status 404: %s (check Base URL: use host root only, e.g. https://api.openai.com - do not append /v1; path is added automatically. Gemini OpenAI-compat: https://generativelanguage.googleapis.com/v1beta/openai)",
			snippet,
		)
	}
	return fmt.Errorf("AI API returned status %d: %s", resp.StatusCode, snippet)
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// G12: optional image attachments as data URLs (e.g.
	// "data:image/png;base64,..."). The backend converts them to OpenAI
	// image_url / Anthropic image blocks and enforces count/size/type budgets
	// (fail-closed) before anything is sent to the provider.
	Images []string `json:"images,omitempty"`
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

// openAIMessages converts ChatMessages to the OpenAI chat-completions shape.
// Messages without images keep a string content (backward compatible);
// messages with images use a content array of text + image_url parts.
func openAIMessages(messages []ChatMessage) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(messages))
	for _, m := range messages {
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
	Content      string
	FinishReason string
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

type AIConfig struct {
	APIKey       string
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
	settingsService *SettingsService
}

// snapshot returns a copy of the service's configuration fields under the
// read lock (N-93). Callers use the returned copy instead of reading
// a.config / a.app / a.presetService / a.projectRoot directly, which would
// race with SetConfig / SetApp / SetPresetService / SetProjectRoot.
func (a *AIService) snapshot() aiSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return aiSnapshot{
		config:        a.config,
		app:           a.app,
		presetService: a.presetService,
		projectRoot:   a.projectRoot,
	}
}

// streamCancel wraps a context.CancelFunc so the streaming goroutine
// can check identity (compare-and-swap) before clearing a.cancel.
type streamCancel struct {
	fn context.CancelFunc
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
func emitAIStreamEvent(app *application.App, name, streamID string, fields map[string]interface{}) {
	if app == nil {
		return
	}
	payload := map[string]interface{}{"streamId": streamID}
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

// ResolveModelFor returns the AIConfig for a specific operation (Step 3).
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
// config (backward compatible).
func (a *AIService) ResolveModelFor(op AIOperation) (AIConfig, *AIConfig, error) {
	a.mu.RLock()
	globalConfig := a.config
	ps := a.permissionService
	a.mu.RUnlock()

	if ps == nil {
		return globalConfig, nil, nil
	}

	// Step 6: check if operation is disabled
	if ps.IsDisabled(op) {
		return AIConfig{}, nil, fmt.Errorf("%w: operation %q is disabled", ErrNotAllowed, op)
	}

	resolution := ps.GetModelFor(op)
	primary := resolution.Primary

	// If no model assigned, fall back to global config
	if primary.Model == "" {
		return globalConfig, nil, nil
	}

	// Build primary config derived from global (keeps BaseURL/Protocol/SystemPrompt)
	cfg := globalConfig
	cfg.Model = primary.Model
	cfg.UseStoredKey = true // G-SEC-07
	cfg.ConfigID = primary.ProviderID
	if primary.Temperature > 0 {
		cfg.Temperature = primary.Temperature
	}
	if primary.MaxTokens > 0 {
		cfg.MaxTokens = primary.MaxTokens
	}

	// Step 4: build fallback config if configured
	var fallback *AIConfig
	if resolution.Fallback != nil {
		fb := cfg
		fb.Model = resolution.Fallback.Model
		fb.ConfigID = resolution.Fallback.ProviderID
		fallback = &fb
	}

	return cfg, fallback, nil
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
			PresetFile: PresetFile{
				Name:        p.Name,
				Label:       p.Label,
				Description: p.Description,
				Icon:        p.Icon,
				Prompt:      p.Prompt,
			},
			Source: PresetSourceBuiltin,
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

// withSystemPrompt prepends the system prompt to the messages slice.
// N-93: takes a snapshot to avoid racing with SetConfig.
func (a *AIService) withSystemPrompt(messages []ChatMessage) []ChatMessage {
	return withSystemPromptFrom(a.snapshot().config, messages)
}

func (a *AIService) Send(messages []ChatMessage) (*ChatResponse, error) {
	start := time.Now()
	// N-93: take a snapshot once; use snap.config throughout to avoid races
	// with concurrent SetConfig calls during the HTTP request.
	snap := a.snapshot()
	cfg := snap.config
	if cfg.APIKey == "" {
		slog.Error("ai send: api key not configured")
		return nil, errors.New("API key not configured")
	}

	// N-61: prepareMessagesWith applies context-window truncation to prevent
	// long conversations from exceeding the model's token limit.
	fullMessages := prepareMessagesWith(cfg, messages)
	// G12: image attachments are validated before any provider call.
	if err := validateImages(fullMessages); err != nil {
		slog.Error("ai send: image validation failed", "err", err)
		return nil, err
	}

	// N-69: per-request timeout (300s). The single context spans all retry
	// attempts (N-63), so the total wall time is bounded by chatTimeout.
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
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		slog.Error("ai send: marshal failed", "err", err)
		return nil, err
	}

	// N-63: retry on transient errors (429, 5xx, network). Each attempt
	// rebuilds the request with a fresh body reader (bytes.Reader is
	// consumed after the first send).
	resp, err := doWithRetry(func() (*http.Response, error) {
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Error("ai send: decode failed", "err", err)
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
		Content:      result.Choices[0].Message.Content,
		FinishReason: result.Choices[0].FinishReason,
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
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		slog.Error("ai send: marshal failed", "err", err)
		return nil, err
	}

	resp, err := doWithRetry(func() (*http.Response, error) {
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
		StopReason string `json:"stop_reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Error("ai send: decode failed", "err", err)
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
		Content:      text,
		FinishReason: result.StopReason,
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
func (a *AIService) Complete(req CompletionRequest) (*CompletionResponse, error) {
	// N-93: snapshot once; use throughout.
	snap := a.snapshot()
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

	reqBody := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"max_tokens":  256,
		"temperature": 0.2,
		"stream":      false,
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
	resp, err := doWithRetry(func() (*http.Response, error) {
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Choices) == 0 {
		return &CompletionResponse{Text: ""}, nil
	}

	return &CompletionResponse{Text: strings.TrimSpace(result.Choices[0].Message.Content)}, nil
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
func (a *AIService) GenerateTitleWithAI(firstMessage string) (string, error) {
	fallback := GenerateTitle(firstMessage)
	// N-93: snapshot once; use throughout.
	snap := a.snapshot()
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
	reqBody := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"max_tokens":  32,
		"temperature": 0.3,
		"stream":      false,
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fallback, err
	}
	if len(result.Choices) == 0 {
		return fallback, nil
	}
	title := strings.TrimSpace(result.Choices[0].Message.Content)
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

// SendStream is the legacy streaming API (kept for backward compat).
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
func parseSSEStream(r io.Reader, onChunk func(string)) error {
	_, err := parseSSEStreamWithTools(r, onChunk)
	return err
}

// parseSSEStreamWithTools is like parseSSEStream but also accumulates
// OpenAI-style delta.tool_calls for native function calling (prompt-5 Task H).
func parseSSEStreamWithTools(r io.Reader, onChunk func(string)) ([]NativeToolCall, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20) // 1MB max line
	consecutiveErrors := 0
	const maxConsecutiveErrors = 5
	// index -> partial tool call (name/arguments may arrive across chunks)
	acc := map[int]*NativeToolCall{}
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if len(line) >= 6 && line[:6] == "data: " {
			data := line[6:]
			if data == "[DONE]" {
				return finalizeNativeToolCalls(acc), nil
			}
			var result struct {
				Choices []struct {
					Delta struct {
						Content   string `json:"content"`
						ToolCalls []struct {
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
						onChunk(delta.Content)
					}
					for _, tc := range delta.ToolCalls {
						cur, ok := acc[tc.Index]
						if !ok {
							cur = &NativeToolCall{}
							acc[tc.Index] = cur
						}
						if tc.ID != "" {
							cur.ID = tc.ID
						}
						if tc.Function.Name != "" {
							cur.Name = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							cur.Arguments += tc.Function.Arguments
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
			return nil, fmt.Errorf("sse stream: line exceeds 1MB limit: %w", err)
		}
		return nil, err
	}
	return finalizeNativeToolCalls(acc), nil
}

func finalizeNativeToolCalls(acc map[int]*NativeToolCall) []NativeToolCall {
	if len(acc) == 0 {
		return nil
	}
	// Preserve index order.
	maxIdx := -1
	for i := range acc {
		if i > maxIdx {
			maxIdx = i
		}
	}
	out := make([]NativeToolCall, 0, len(acc))
	for i := 0; i <= maxIdx; i++ {
		if tc, ok := acc[i]; ok && tc.Name != "" {
			out = append(out, *tc)
		}
	}
	return out
}

// anthropicToolAcc accumulates a single Anthropic tool_use content block
// across content_block_start / input_json_delta events (prompt-6 Task 3).
type anthropicToolAcc struct {
	id   string
	name string
	args string
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
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20) // 1MB max line
	consecutiveErrors := 0
	const maxConsecutiveErrors = 5

	toolsByIndex := map[int]*anthropicToolAcc{}
	openToolIndex := -1

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if len(line) >= 6 && line[:6] == "data: " {
			data := line[6:]
			var evt struct {
				Type         string `json:"type"`
				Index        int    `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
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
					if evt.ContentBlock.Type == "tool_use" {
						toolsByIndex[evt.Index] = &anthropicToolAcc{
							id:   evt.ContentBlock.ID,
							name: evt.ContentBlock.Name,
						}
						openToolIndex = evt.Index
					} else {
						openToolIndex = -1
					}
				case "content_block_delta":
					switch evt.Delta.Type {
					case "text_delta":
						if evt.Delta.Text != "" {
							onChunk(evt.Delta.Text)
						}
					case "input_json_delta":
						idx := evt.Index
						if acc, ok := toolsByIndex[idx]; ok {
							acc.args += evt.Delta.PartialJSON
						} else if openToolIndex >= 0 {
							if acc, ok := toolsByIndex[openToolIndex]; ok {
								acc.args += evt.Delta.PartialJSON
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
			return finalizeAnthropicNativeTools(toolsByIndex), fmt.Errorf("sse stream: line exceeds 1MB limit: %w", err)
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
	maxIdx := -1
	for i := range acc {
		if i > maxIdx {
			maxIdx = i
		}
	}
	out := make([]NativeToolCall, 0, len(acc))
	for i := 0; i <= maxIdx; i++ {
		if tc, ok := acc[i]; ok && tc != nil && tc.name != "" {
			out = append(out, NativeToolCall{
				ID:        tc.id,
				Name:      tc.name,
				Arguments: tc.args,
			})
		}
	}
	return out
}

// SendStreamWithContext streams the response and respects ctx cancellation.
// N-93: takes a snapshot at the start; uses snap.config throughout.
func (a *AIService) SendStreamWithContext(ctx context.Context, messages []ChatMessage, onChunk func(chunk string)) error {
	snap := a.snapshot()
	cfg := snap.config
	if cfg.APIKey == "" {
		return errors.New("API key not configured")
	}

	// N-61: prepareMessagesWith applies context-window truncation to prevent
	// long conversations from exceeding the model's token limit.
	fullMessages := prepareMessagesWith(cfg, messages)
	// G12: image attachments are validated before any provider call.
	if err := validateImages(fullMessages); err != nil {
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
		return parseAnthropicSSEStream(resp.Body, onChunk)
	}

	reqBody := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    openAIMessages(fullMessages),
		"stream":      true,
		"max_tokens":  maxTokensFrom(cfg), // N-65: bound response length
		"temperature": effectiveTemperature(cfg),
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

	return parseSSEStream(resp.Body, onChunk)
}

// ErrStreamBusy is returned by StartStream when another stream is already
// active (prompt-5 Task B / BUG-H1: mutual exclusion across main + AI windows).
var ErrStreamBusy = errors.New("another AI stream is already in progress; stop it before starting a new one")

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
func (a *AIService) StartStream(messages []ChatMessage) (string, error) {
	snap := a.snapshot()
	if snap.config.APIKey == "" {
		slog.Error("ai startstream: api key not configured")
		return "", errors.New("API key not configured")
	}
	if snap.app == nil {
		slog.Error("ai startstream: app not initialized")
		return "", errors.New("application not initialized")
	}

	streamID := newStreamID()

	// Mutual exclusion: do not cancel an existing stream - reject instead.
	a.mu.Lock()
	if a.cancel != nil {
		a.mu.Unlock()
		slog.Warn("ai startstream: rejected, stream already active")
		return "", ErrStreamBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	sc := &streamCancel{fn: cancel}
	a.cancel = sc
	a.activeStreamID = streamID
	a.mu.Unlock()

	// Notify all webviews that a global stream is busy (UI can disable send).
	emitAIStreamEvent(snap.app, "ai:stream-busy", streamID, map[string]interface{}{"busy": true})

	slog.Info("ai startstream: starting", "model", snap.config.Model, "messages", len(messages), "streamId", streamID)

	go func() {
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
			idleStreamID := streamID
			if stillBusy {
				idleStreamID = a.activeStreamID
			}
			a.mu.Unlock()
			if !stillBusy {
				emitAIStreamEvent(snap.app, "ai:stream-busy", idleStreamID, map[string]interface{}{"busy": false})
			}
		}()

		err := a.streamWithEvents(ctx, messages, snap, streamID)
		if err != nil {
			slog.Error("ai startstream: failed", "model", snap.config.Model, "err", err, "streamId", streamID)
			emitAIStreamEvent(snap.app, "ai:error", streamID, map[string]interface{}{"data": err.Error()})
			return
		}
		slog.Info("ai startstream: completed", "model", snap.config.Model, "streamId", streamID)
		emitAIStreamEvent(snap.app, "ai:done", streamID, map[string]interface{}{"data": ""})
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
func (a *AIService) streamWithEvents(ctx context.Context, messages []ChatMessage, snap aiSnapshot, streamID string) error {
	cfg := snap.config
	// N-61: prepareMessages applies context-window truncation to prevent
	// long conversations from exceeding the model's token limit.
	fullMessages := prepareMessagesWith(cfg, messages)
	// G12: image attachments are validated before any provider call.
	if err := validateImages(fullMessages); err != nil {
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

		app := snap.app
		// prompt-6 Task 3: full Anthropic tool_use streaming + text dual-track.
		toolCalls, perr := parseAnthropicSSEStreamWithTools(resp.Body, func(chunk string) {
			emitAIStreamEvent(app, "ai:chunk", streamID, map[string]interface{}{"data": chunk})
		})
		if perr != nil {
			return perr
		}
		if len(toolCalls) > 0 {
			payload, merr := json.Marshal(toolCalls)
			if merr == nil {
				emitAIStreamEvent(app, "ai:tool_calls", streamID, map[string]interface{}{"data": string(payload)})
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

	app := snap.app
	toolCalls, err := parseSSEStreamWithTools(resp.Body, func(chunk string) {
		emitAIStreamEvent(app, "ai:chunk", streamID, map[string]interface{}{"data": chunk})
	})
	if err != nil {
		return err
	}
	if len(toolCalls) > 0 {
		payload, merr := json.Marshal(toolCalls)
		if merr == nil {
			emitAIStreamEvent(app, "ai:tool_calls", streamID, map[string]interface{}{"data": string(payload)})
		}
	}
	return nil
}

// StopStream cancels an in-progress stream.
func (a *AIService) StopStream() error {
	a.mu.Lock()
	had := a.cancel != nil
	streamID := a.activeStreamID
	if a.cancel != nil {
		a.cancel.fn()
		a.cancel = nil
	}
	a.activeStreamID = ""
	app := a.app
	a.mu.Unlock()
	if had && app != nil {
		emitAIStreamEvent(app, "ai:stream-busy", streamID, map[string]interface{}{"busy": false})
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
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
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
