package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// resolveAgentOperation is the backend-only provider boundary used by the
// unified workflow AI adapter. Renderer input never selects a provider. When
// an operation is assigned to a named provider, every endpoint/protocol/key
// field is reloaded from SettingsService and the operation assignment may only
// narrow model/token parameters.
func (a *AIService) resolveAgentOperation(operation AIOperation) (AIConfig, *AIConfig, error) {
	snap := a.snapshot()
	primary, fallback, err := resolveModelForSnapshot(snap, operation)
	if err != nil {
		return AIConfig{}, nil, err
	}
	primary, err = hydrateProviderAIConfig(primary, snap.settingsService)
	if err != nil {
		return AIConfig{}, nil, err
	}
	if fallback == nil {
		return primary, nil, nil
	}
	hydratedFallback, err := hydrateProviderAIConfig(*fallback, snap.settingsService)
	if err != nil {
		return AIConfig{}, nil, err
	}
	return primary, &hydratedFallback, nil
}

// resolveModelForSnapshot applies one operation assignment to a single
// AIService snapshot. Disabled and assigned state come from the same locked
// permission read, while provider credentials remain outside this metadata
// step and are hydrated only by trusted backend callers.
func resolveModelForSnapshot(snap aiSnapshot, operation AIOperation) (AIConfig, *AIConfig, error) {
	globalConfig := snap.config
	permission := snap.permissionService
	if permission == nil {
		return globalConfig, nil, nil
	}

	resolution := permission.GetModelFor(operation)
	primary := resolution.Primary
	if primary.Disabled {
		return AIConfig{}, nil, fmt.Errorf("%w: operation %q is disabled", ErrNotAllowed, operation)
	}
	if primary.Model == "" {
		return globalConfig, nil, nil
	}

	config := globalConfig
	config.Model = primary.Model
	config.Provider = ""
	config.UseStoredKey = true
	config.ConfigID = primary.ProviderID
	config.ReasoningEffort = primary.ReasoningEffort
	if primary.Temperature > 0 {
		config.Temperature = primary.Temperature
	}
	if primary.MaxTokens > 0 {
		config.MaxTokens = primary.MaxTokens
	}

	var fallback *AIConfig
	if resolution.Fallback != nil {
		fallbackConfig := config
		fallbackConfig.Model = resolution.Fallback.Model
		fallbackConfig.Provider = ""
		fallbackConfig.ConfigID = resolution.Fallback.ProviderID
		fallbackConfig.ReasoningEffort = resolution.Fallback.ReasoningEffort
		fallback = &fallbackConfig
	}
	return config, fallback, nil
}

// admitProviderOperation resolves operation policy before lifecycle creation
// or network I/O, then replaces the snapshot config with the backend-hydrated
// primary provider. Fallback execution remains owned by callers that can bind
// both provider identities to their approval and usage receipts.
func (a *AIService) admitProviderOperation(snap aiSnapshot, operation AIOperation) (aiSnapshot, error) {
	// Preserve the legacy backend-only behavior when operation permissions have
	// not been wired yet. Renderer-facing production wiring always installs the
	// permission service before admitting provider work.
	if snap.permissionService == nil {
		return snap, nil
	}
	primary, _, err := resolveModelForSnapshot(snap, operation)
	if err != nil {
		return aiSnapshot{}, err
	}
	primary, err = hydrateProviderAIConfig(primary, snap.settingsService)
	if err != nil {
		return aiSnapshot{}, err
	}
	snap.config = primary
	return snap, nil
}

func hydrateProviderAIConfig(config AIConfig, settings *SettingsService) (AIConfig, error) {
	if strings.TrimSpace(config.ConfigID) == "" {
		if config.APIKey == "" {
			return AIConfig{}, fmt.Errorf("AI provider is not configured: %w", ErrNotAllowed)
		}
		if err := validateReasoningCapability(config.Provider, config.Model, config.Protocol, config.ReasoningEffort); err != nil {
			return AIConfig{}, err
		}
		return config, nil
	}
	if settings == nil {
		return AIConfig{}, fmt.Errorf("AI provider %q is unavailable: %w", config.ConfigID, ErrNotAllowed)
	}
	provider, err := settings.getAIProviderConfig(config.ConfigID)
	if err != nil {
		return AIConfig{}, err
	}
	if provider.BaseURL == "" || provider.APIKey == "" {
		return AIConfig{}, fmt.Errorf("AI provider %q is incomplete: %w", config.ConfigID, ErrNotAllowed)
	}
	baseURL := NormalizeAIBaseURL(provider.BaseURL)
	if err := ValidateBaseURL(baseURL); err != nil {
		return AIConfig{}, fmt.Errorf("AI provider %q has invalid base URL: %w", config.ConfigID, err)
	}
	config.Provider = strings.ToLower(strings.TrimSpace(provider.Provider))
	config.APIKey = provider.APIKey
	config.BaseURL = baseURL
	config.Protocol = strings.ToLower(strings.TrimSpace(provider.Protocol))
	if config.Protocol == "" {
		config.Protocol = "openai"
	}
	config.SystemPrompt = provider.SystemPrompt
	config.UseStoredKey = false
	if config.Model == "" {
		config.Model = provider.Model
	}
	if config.Model == "" {
		return AIConfig{}, fmt.Errorf("AI provider %q has no model: %w", config.ConfigID, ErrNotAllowed)
	}
	if config.Temperature == 0 {
		config.Temperature = provider.Temperature
	}
	if config.ReasoningEffort == "" {
		config.ReasoningEffort = provider.ReasoningEffort
	}
	if config.ReasoningEffort, err = normalizeReasoningEffort(config.ReasoningEffort); err != nil {
		return AIConfig{}, err
	}
	if err := validateReasoningCapability(config.Provider, config.Model, config.Protocol, config.ReasoningEffort); err != nil {
		return AIConfig{}, fmt.Errorf("AI provider %q reasoning capability: %w", config.ConfigID, err)
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = provider.MaxTokens
	}
	return config, nil
}

func aiConfigFingerprint(config AIConfig) string {
	credentialHash := sha256.Sum256([]byte(config.APIKey))
	encoded, _ := json.Marshal(struct {
		ConfigID        string
		Provider        string
		BaseURL         string
		Protocol        string
		Model           string
		SystemPrompt    string
		CredentialHash  string
		MaxTokens       int
		ContextWindow   int
		Temperature     float64
		ReasoningEffort string
	}{
		ConfigID: config.ConfigID, Provider: config.Provider, BaseURL: config.BaseURL, Protocol: config.Protocol,
		Model: config.Model, SystemPrompt: config.SystemPrompt,
		CredentialHash: hex.EncodeToString(credentialHash[:]),
		MaxTokens: config.MaxTokens, ContextWindow: config.ContextWindow, Temperature: config.Temperature,
		ReasoningEffort: config.ReasoningEffort,
	})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func optionalAIConfigFingerprint(config *AIConfig) string {
	if config == nil {
		return ""
	}
	return aiConfigFingerprint(*config)
}

// sendAgentOperation performs one bounded non-streaming provider call without
// creating a second chat lifecycle unit. The surrounding Agent Runtime owns
// approval, receipt, and tool usage; this helper only returns provider usage.
func (a *AIService) sendAgentOperation(ctx context.Context, operation AIOperation, messages []ChatMessage) (*ChatResponse, AIConfig, error) {
	primary, fallback, err := a.resolveAgentOperation(operation)
	if err != nil {
		return nil, AIConfig{}, err
	}
	return sendResolvedAgentOperation(ctx, primary, fallback, messages, a.sendWithAgentConfig)
}

type agentOperationSender func(context.Context, AIConfig, []ChatMessage) (*ChatResponse, error)

func sendResolvedAgentOperation(
	ctx context.Context,
	primary AIConfig,
	fallback *AIConfig,
	messages []ChatMessage,
	send agentOperationSender,
) (*ChatResponse, AIConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationCtx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()
	response, err := send(operationCtx, primary, messages)
	if err == nil || fallback == nil || !shouldFallbackAgentOperation(operationCtx, err) {
		return response, primary, err
	}
	fallbackResponse, fallbackErr := send(operationCtx, *fallback, messages)
	if fallbackErr != nil {
		return nil, AIConfig{}, errors.Join(err, fallbackErr)
	}
	return fallbackResponse, *fallback, nil
}

func shouldFallbackAgentOperation(ctx context.Context, err error) bool {
	if err == nil || isContextError(err) || (ctx != nil && ctx.Err() != nil) {
		return false
	}
	var statusErr *aiHTTPStatusError
	if errors.As(err, &statusErr) {
		return isRetryableStatus(statusErr.statusCode)
	}
	var operationErr *net.OpError
	if errors.As(err, &operationErr) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary())
}

func (a *AIService) sendWithAgentConfig(ctx context.Context, config AIConfig, messages []ChatMessage) (*ChatResponse, error) {
	if config.APIKey == "" || config.BaseURL == "" || config.Model == "" {
		return nil, fmt.Errorf("AI provider configuration is incomplete: %w", ErrNotAllowed)
	}
	fullMessages := prepareMessagesWith(config, messages)
	if err := validateOutboundChatMessages(fullMessages); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()
	if isAnthropicProtocol(config) {
		return a.sendAnthropic(requestCtx, config, fullMessages, len(messages), time.Now())
	}
	reqBody := map[string]interface{}{
		"model": config.Model, "messages": openAIMessages(fullMessages),
		"max_tokens": maxTokensFrom(config), "temperature": effectiveTemperature(config),
	}
	if err := applyReasoningRequestConfig(reqBody, config); err != nil {
		return nil, err
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	resp, err := doWithRetryContext(requestCtx, func() (*http.Response, error) {
		req, requestErr := http.NewRequestWithContext(requestCtx, http.MethodPost,
			JoinAIEndpoint(config.BaseURL, "/v1/chat/completions"), bytes.NewReader(body))
		if requestErr != nil {
			return nil, requestErr
		}
		setCommonHeaders(req, config.APIKey)
		return aiHTTPClient.Do(req)
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
			FinishReason string `json:"finish_reason"`
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
		return nil, errors.New("no choices in response")
	}
	return &ChatResponse{
		Content: result.Choices[0].Message.Content, FinishReason: result.Choices[0].FinishReason,
		usageReported: usageReported,
		tokensIn:      tokensIn, tokensOut: tokensOut,
	}, nil
}
