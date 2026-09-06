package agentcore

import (
	"errors"
	"testing"
)

func TestRegistryRejectsUnsafePolicyCombinations(t *testing.T) {
	tests := []struct {
		name     string
		risk     Risk
		approval ApprovalMode
		mutation MutationMode
	}{
		{
			name:     "dangerous tool cannot use backend policy approval",
			risk:     RiskDangerous,
			approval: ApprovalBackendPolicy,
			mutation: MutationNone,
		},
		{
			name:     "workspace mutation cannot claim read only risk",
			risk:     RiskReadOnly,
			approval: ApprovalManual,
			mutation: MutationWorkspaceTransaction,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			definition := testTool("unsafe", SourceBuiltin, `{
				"type":"object",
				"additionalProperties":false
			}`)
			definition.Risk = tt.risk
			definition.Approval = tt.approval
			definition.Mutation = tt.mutation

			if _, err := NewRegistry([]ToolDef{definition}); !errors.Is(err, ErrInvalidToolDef) {
				t.Fatalf("NewRegistry error = %v, want ErrInvalidToolDef", err)
			}
		})
	}
}

func TestRegistryRejectsUnknownDynamicSource(t *testing.T) {
	registry, err := NewRegistry([]ToolDef{
		testTool("read", SourceBuiltin, `{
			"type":"object",
			"additionalProperties":false
		}`),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	const unknownSource ToolSource = "renderer"
	definition := testTool("renderer.tool", unknownSource, `{
		"type":"object",
		"additionalProperties":false
	}`)
	if _, err := registry.ReplaceSource(unknownSource, []ToolDef{definition}); !errors.Is(err, ErrInvalidToolDef) {
		t.Fatalf("ReplaceSource error = %v, want ErrInvalidToolDef", err)
	}
}
