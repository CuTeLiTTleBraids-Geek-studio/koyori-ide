package services

import (
	"errors"
	"strings"
	"testing"
)

func TestDefaultStepExecutor_RejectsInvalidCommandArgs(t *testing.T) {
	executor := &defaultStepExecutor{agent: &AgentService{}}
	tests := []struct {
		name string
		args string
	}{
		{name: "empty args", args: ""},
		{name: "raw command", args: "go version | echo should-not-run"},
		{name: "unknown field", args: `{"command":"go version | echo should-not-run","extra":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executor.Execute("command", tt.args)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if !strings.Contains(err.Error(), "invalid command args") {
				t.Fatalf("expected command args validation error, got %v", err)
			}
		})
	}
}

func TestDefaultStepExecutor_ParsesCommandArgs(t *testing.T) {
	command, err := parseStepCommandArgs(`{"command":"go version"}`)
	if err != nil {
		t.Fatalf("parseStepCommandArgs failed: %v", err)
	}
	if command != "go version" {
		t.Fatalf("command = %q, want %q", command, "go version")
	}
}

func TestDefaultStepExecutor_RejectsMalformedCommandArgs(t *testing.T) {
	tests := []struct {
		name string
		args string
	}{
		{name: "empty input", args: ""},
		{name: "raw string", args: `"go version"`},
		{name: "array", args: `[{"command":"go version"}]`},
		{name: "number", args: `42`},
		{name: "null", args: `null`},
		{name: "unknown field", args: `{"command":"go version","extra":true}`},
		{name: "non-string command", args: `{"command":42}`},
		{name: "missing command", args: `{}`},
		{name: "empty command", args: `{"command":""}`},
		{name: "whitespace command", args: `{"command":"   "}`},
		{name: "trailing JSON value", args: `{"command":"go version"}{"command":"go env"}`},
		{name: "trailing data", args: `{"command":"go version"} trailing`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseStepCommandArgs(tt.args)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestDefaultStepExecutor_RejectsUnsupportedTools(t *testing.T) {
	executor := &defaultStepExecutor{agent: &AgentService{}}
	const args = `{"command":"go version | echo should-not-run"}`
	tests := []struct {
		name        string
		tool        string
		wantMessage string
	}{
		{name: "shell", tool: "shell", wantMessage: "unsupported step tool"},
		{name: "empty-tool", tool: "", wantMessage: "unsupported step tool"},
		{name: "unknown", tool: "unknown", wantMessage: "unsupported step tool"},
		{name: "file", tool: "file", wantMessage: "unsupported step tool"},
		{name: "git", tool: "git", wantMessage: "unsupported step tool"},
		{name: "ai", tool: "ai", wantMessage: "unsupported step tool"},
		{name: "mcp", tool: "mcp", wantMessage: "dedicated executor"},
		{name: "skill", tool: "skill", wantMessage: "dedicated executor"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executor.Execute(tt.tool, args)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Fatalf("expected error containing %q, got %v", tt.wantMessage, err)
			}
		})
	}
}

func TestDefaultStepExecutor_BlocksShellSyntaxInCommand(t *testing.T) {
	executor := &defaultStepExecutor{agent: &AgentService{}}
	_, err := executor.Execute("command", `{"command":"go version | echo should-not-run"}`)
	if err == nil || !strings.Contains(err.Error(), "unsupported shell syntax") {
		t.Fatalf("expected shell syntax to be blocked, got %v", err)
	}
}
