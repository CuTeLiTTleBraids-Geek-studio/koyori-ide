package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func paddedAIJSONResponse(t *testing.T, size int) []byte {
	t.Helper()
	prefix := []byte(`{"ok":true}`)
	if size < len(prefix) {
		t.Fatalf("response size %d is smaller than JSON prefix", size)
	}
	body := make([]byte, size)
	copy(body, prefix)
	for i := len(prefix); i < len(body); i++ {
		body[i] = ' '
	}
	return body
}

func TestDecodeAIJSONResponseUsesMaxPlusOneBoundary(t *testing.T) {
	var result struct {
		OK bool `json:"ok"`
	}
	exact := paddedAIJSONResponse(t, int(maxAINonStreamingResponseBytes))
	if err := decodeAIJSONResponse(bytes.NewReader(exact), &result); err != nil {
		t.Fatalf("exact-boundary response failed: %v", err)
	}
	if !result.OK {
		t.Fatal("exact-boundary response was not decoded")
	}

	oversized := paddedAIJSONResponse(t, int(maxAINonStreamingResponseBytes)+1)
	err := decodeAIJSONResponse(bytes.NewReader(oversized), &result)
	if !errors.Is(err, ErrAIProviderOutputBudget) {
		t.Fatalf("one-byte-oversized response error = %v, want ErrAIProviderOutputBudget", err)
	}
}

func TestAINonStreamingProviderPathsRejectOversizedSuccessfulResponses(t *testing.T) {
	body := paddedAIJSONResponse(t, int(maxAINonStreamingResponseBytes)+1)
	tests := []struct {
		name     string
		protocol string
		invoke   func(*AIService, AIConfig) error
	}{
		{
			name: "send OpenAI",
			invoke: func(service *AIService, _ AIConfig) error {
				_, err := service.Send([]ChatMessage{{Role: "user", Content: "hello"}})
				return err
			},
		},
		{
			name:     "send Anthropic",
			protocol: "anthropic",
			invoke: func(service *AIService, _ AIConfig) error {
				_, err := service.Send([]ChatMessage{{Role: "user", Content: "hello"}})
				return err
			},
		},
		{
			name: "inline completion",
			invoke: func(service *AIService, _ AIConfig) error {
				_, err := service.Complete(CompletionRequest{Prefix: "x", Language: "go"})
				return err
			},
		},
		{
			name: "title generation",
			invoke: func(service *AIService, _ AIConfig) error {
				_, err := service.GenerateTitleWithAI("oversized provider response")
				return err
			},
		},
		{
			name: "model list",
			invoke: func(service *AIService, config AIConfig) error {
				_, err := service.ListModels(config.BaseURL, config.APIKey)
				return err
			},
		},
		{
			name: "agent operation OpenAI",
			invoke: func(service *AIService, _ AIConfig) error {
				_, _, err := service.sendAgentOperation(context.Background(), AIOpAgent, []ChatMessage{{Role: "user", Content: "work"}})
				return err
			},
		},
		{
			name:     "agent operation Anthropic",
			protocol: "anthropic",
			invoke: func(service *AIService, _ AIConfig) error {
				_, _, err := service.sendAgentOperation(context.Background(), AIOpAgent, []ChatMessage{{Role: "user", Content: "work"}})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer server.Close()

			config := AIConfig{
				APIKey: "test-key", BaseURL: server.URL, Model: "test-model", Protocol: test.protocol,
			}
			service := NewAIService()
			if err := service.SetConfig(config); err != nil {
				t.Fatalf("SetConfig: %v", err)
			}
			err := test.invoke(service, config)
			if !errors.Is(err, ErrAIProviderOutputBudget) {
				t.Fatalf("error = %v, want ErrAIProviderOutputBudget", err)
			}
		})
	}
}

func TestValidateAIProviderUsageRejectsUnsafeCounters(t *testing.T) {
	tests := []struct {
		name               string
		input              int64
		output             int64
		wantReported       bool
		wantInput          int
		wantOutput         int
		wantErr            bool
		wantBudgetSentinel bool
	}{
		{name: "absent", input: 0, output: 0},
		{name: "normal", input: 123, output: 45, wantReported: true, wantInput: 123, wantOutput: 45},
		{
			name: "exact aggregate limit", input: maxAIProviderReportedTotalTokens - 1, output: 1,
			wantReported: true, wantInput: int(maxAIProviderReportedTotalTokens - 1), wantOutput: 1,
		},
		{name: "negative input", input: -1, wantErr: true},
		{name: "negative output", output: -1, wantErr: true},
		{
			name: "input exceeds limit", input: maxAIProviderReportedTotalTokens + 1,
			wantErr: true, wantBudgetSentinel: true,
		},
		{
			name: "output exceeds limit", output: maxAIProviderReportedTotalTokens + 1,
			wantErr: true, wantBudgetSentinel: true,
		},
		{
			name: "aggregate exceeds limit", input: maxAIProviderReportedTotalTokens, output: 1,
			wantErr: true, wantBudgetSentinel: true,
		},
		{
			name: "hostile int64 cannot overflow aggregate", input: int64(1<<63 - 1), output: int64(1<<63 - 1),
			wantErr: true, wantBudgetSentinel: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reported, input, output, err := validateAIProviderUsage(test.input, test.output)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateAIProviderUsage(%d, %d) error = %v", test.input, test.output, err)
			}
			if test.wantBudgetSentinel && !errors.Is(err, ErrAIProviderOutputBudget) {
				t.Fatalf("error = %v, want ErrAIProviderOutputBudget", err)
			}
			if err == nil && (reported != test.wantReported || input != test.wantInput || output != test.wantOutput) {
				t.Fatalf("result = (%v, %d, %d), want (%v, %d, %d)", reported, input, output, test.wantReported, test.wantInput, test.wantOutput)
			}
		})
	}
}

func TestAINonStreamingProviderPathsRejectOversizedUsage(t *testing.T) {
	overLimit := maxAIProviderReportedTotalTokens + 1
	openAIResponse := fmt.Sprintf(
		`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":0}}`,
		overLimit,
	)
	anthropicResponse := fmt.Sprintf(
		`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":%d}}`,
		overLimit,
	)
	tests := []struct {
		name     string
		protocol string
		response string
		invoke   func(*AIService) error
	}{
		{
			name: "send OpenAI", response: openAIResponse,
			invoke: func(service *AIService) error {
				_, err := service.Send([]ChatMessage{{Role: "user", Content: "hello"}})
				return err
			},
		},
		{
			name: "send Anthropic", protocol: "anthropic", response: anthropicResponse,
			invoke: func(service *AIService) error {
				_, err := service.Send([]ChatMessage{{Role: "user", Content: "hello"}})
				return err
			},
		},
		{
			name: "inline completion", response: openAIResponse,
			invoke: func(service *AIService) error {
				_, err := service.Complete(CompletionRequest{Prefix: "x", Language: "go"})
				return err
			},
		},
		{
			name: "title generation", response: openAIResponse,
			invoke: func(service *AIService) error {
				_, err := service.GenerateTitleWithAI("unsafe provider usage")
				return err
			},
		},
		{
			name: "agent operation OpenAI", response: openAIResponse,
			invoke: func(service *AIService) error {
				_, _, err := service.sendAgentOperation(context.Background(), AIOpAgent, []ChatMessage{{Role: "user", Content: "work"}})
				return err
			},
		},
		{
			name: "agent operation Anthropic", protocol: "anthropic", response: anthropicResponse,
			invoke: func(service *AIService) error {
				_, _, err := service.sendAgentOperation(context.Background(), AIOpAgent, []ChatMessage{{Role: "user", Content: "work"}})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, test.response)
			}))
			defer server.Close()

			service := NewAIService()
			if err := service.SetConfig(AIConfig{
				APIKey: "test-key", BaseURL: server.URL, Model: "test-model", Protocol: test.protocol,
			}); err != nil {
				t.Fatalf("SetConfig: %v", err)
			}
			err := test.invoke(service)
			if !errors.Is(err, ErrAIProviderOutputBudget) {
				t.Fatalf("error = %v, want ErrAIProviderOutputBudget", err)
			}
			if err != nil && !strings.Contains(err.Error(), "token") {
				t.Fatalf("error does not identify unsafe token usage: %v", err)
			}
		})
	}
}
