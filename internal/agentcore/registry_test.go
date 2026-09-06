package agentcore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func testTool(id string, source ToolSource, schema string) ToolDef {
	return ToolDef{
		ID:          id,
		Description: "test tool " + id,
		InputSchema: json.RawMessage(schema),
		Source:      source,
		Risk:        RiskReadOnly,
		Approval:    ApprovalBackendPolicy,
		Mutation:    MutationNone,
		ExecuteKey:  id,
	}
}

func TestRegistryRejectsDuplicateAndBuiltinReplacement(t *testing.T) {
	builtin := testTool("read", SourceBuiltin, `{
		"type":"object",
		"properties":{"path":{"type":"string"}},
		"required":["path"],
		"additionalProperties":false
	}`)
	registry, err := NewRegistry([]ToolDef{builtin})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	if _, err := registry.ReplaceSource(SourceMCP, []ToolDef{
		testTool("read", SourceMCP, `{ "type":"object", "additionalProperties":false }`),
	}); !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("replace builtin error = %v, want ErrDuplicateTool", err)
	}

	duplicateA := testTool("mcp.server.first", SourceMCP, `{ "type":"object", "additionalProperties":false }`)
	duplicateB := testTool("mcp.server.first", SourceMCP, `{ "type":"object", "additionalProperties":false }`)
	if _, err := registry.ReplaceSource(SourceMCP, []ToolDef{duplicateA, duplicateB}); !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("duplicate source tools error = %v, want ErrDuplicateTool", err)
	}

	catalog := registry.Snapshot()
	if catalog.Revision != 1 || len(catalog.Tools) != 1 || catalog.Tools[0].ID != "read" {
		t.Fatalf("failed replacement mutated catalog: %+v", catalog)
	}
}

func TestReplaceSourceIsAtomicAndInvalidatesOldRevision(t *testing.T) {
	registry, err := NewRegistry([]ToolDef{
		testTool("read", SourceBuiltin, `{
			"type":"object",
			"properties":{"path":{"type":"string"}},
			"required":["path"],
			"additionalProperties":false
		}`),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	before := registry.Snapshot()

	after, err := registry.ReplaceSource(SourceMCP, []ToolDef{
		testTool("mcp.files.read_text", SourceMCP, `{
			"type":"object",
			"properties":{"path":{"type":"string"}},
			"required":["path"],
			"additionalProperties":false
		}`),
	})
	if err != nil {
		t.Fatalf("ReplaceSource: %v", err)
	}
	if after.Revision != before.Revision+1 {
		t.Fatalf("revision = %d, want %d", after.Revision, before.Revision+1)
	}
	same, err := registry.ReplaceSource(SourceMCP, []ToolDef{
		testTool("mcp.files.read_text", SourceMCP, `{
			"type":"object",
			"properties":{"path":{"type":"string"}},
			"required":["path"],
			"additionalProperties":false
		}`),
	})
	if err != nil {
		t.Fatalf("same ReplaceSource: %v", err)
	}
	if same.Revision != after.Revision {
		t.Fatalf("no-op source refresh changed revision from %d to %d", after.Revision, same.Revision)
	}
	if _, err := registry.Resolve(before.Revision, "read", json.RawMessage(`{"path":"a.txt"}`)); !errors.Is(err, ErrStaleCatalog) {
		t.Fatalf("old revision resolve error = %v, want ErrStaleCatalog", err)
	}

	invalid := testTool("mcp.files.bad", SourceMCP, `{ "type":"array" }`)
	if _, err := registry.ReplaceSource(SourceMCP, []ToolDef{invalid}); !errors.Is(err, ErrInvalidToolDef) {
		t.Fatalf("invalid replacement error = %v, want ErrInvalidToolDef", err)
	}
	unchanged := registry.Snapshot()
	if unchanged.Revision != after.Revision || len(unchanged.Tools) != len(after.Tools) {
		t.Fatalf("invalid replacement partially published: before=%+v after=%+v", after, unchanged)
	}
}

func TestReplaceSourcesPublishesAllDynamicSourcesInOneRevision(t *testing.T) {
	registry, err := NewRegistry([]ToolDef{
		testTool("read", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	before := registry.Snapshot()

	after, err := registry.ReplaceSources(map[ToolSource][]ToolDef{
		SourceMCP: {
			testTool("mcp.files.read", SourceMCP, `{ "type":"object", "additionalProperties":false }`),
		},
		SourceWorkflow: {
			testTool("workflow.inspect.run", SourceWorkflow, `{ "type":"object", "additionalProperties":false }`),
		},
		SourceSkill: {
			testTool("skill.review.activate", SourceSkill, `{ "type":"object", "additionalProperties":false }`),
		},
	})
	if err != nil {
		t.Fatalf("ReplaceSources: %v", err)
	}
	if after.Revision != before.Revision+1 {
		t.Fatalf("revision = %d, want one atomic publication at %d", after.Revision, before.Revision+1)
	}
	want := map[string]bool{
		"read": true, "mcp.files.read": true,
		"workflow.inspect.run": true, "skill.review.activate": true,
	}
	for _, tool := range after.Tools {
		delete(want, tool.ID)
	}
	if len(want) != 0 {
		t.Fatalf("atomic catalog omitted tools: %v", want)
	}

	same, err := registry.ReplaceSources(map[ToolSource][]ToolDef{
		SourceSkill: {
			testTool("skill.review.activate", SourceSkill, `{ "type":"object", "additionalProperties":false }`),
		},
		SourceWorkflow: {
			testTool("workflow.inspect.run", SourceWorkflow, `{ "type":"object", "additionalProperties":false }`),
		},
		SourceMCP: {
			testTool("mcp.files.read", SourceMCP, `{ "type":"object", "additionalProperties":false }`),
		},
	})
	if err != nil {
		t.Fatalf("same ReplaceSources: %v", err)
	}
	if same.Revision != after.Revision {
		t.Fatalf("no-op batch changed revision from %d to %d", after.Revision, same.Revision)
	}
}

func TestReplaceSourcesRejectsWholeBatchWhenOneSourceIsInvalid(t *testing.T) {
	registry, err := NewRegistry([]ToolDef{
		testTool("read", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	before := registry.Snapshot()

	_, err = registry.ReplaceSources(map[ToolSource][]ToolDef{
		SourceMCP: {
			testTool("mcp.files.read", SourceMCP, `{ "type":"object", "additionalProperties":false }`),
		},
		SourceWorkflow: {
			testTool("workflow.bad", SourceMCP, `{ "type":"object", "additionalProperties":false }`),
		},
	})
	if !errors.Is(err, ErrInvalidToolDef) {
		t.Fatalf("ReplaceSources error = %v, want ErrInvalidToolDef", err)
	}
	after := registry.Snapshot()
	if after.Revision != before.Revision || len(after.Tools) != len(before.Tools) {
		t.Fatalf("invalid batch partially published: before=%+v after=%+v", before, after)
	}
}

func TestReplaceSourcesConcurrentReadersNeverObserveMixedBatch(t *testing.T) {
	registry, err := NewRegistry([]ToolDef{
		testTool("read", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	batch := func(version string) map[ToolSource][]ToolDef {
		return map[ToolSource][]ToolDef{
			SourceMCP: {
				testTool("mcp.probe."+version, SourceMCP, `{ "type":"object", "additionalProperties":false }`),
			},
			SourceWorkflow: {
				testTool("workflow.probe."+version, SourceWorkflow, `{ "type":"object", "additionalProperties":false }`),
			},
			SourceSkill: {
				testTool("skill.probe."+version, SourceSkill, `{ "type":"object", "additionalProperties":false }`),
			},
		}
	}
	if _, err := registry.ReplaceSources(batch("old")); err != nil {
		t.Fatalf("seed old batch: %v", err)
	}

	stop := make(chan struct{})
	readerErrors := make(chan string, 1)
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				catalog := registry.Snapshot()
				oldCount, newCount, dynamicCount := 0, 0, 0
				for _, tool := range catalog.Tools {
					if tool.Source == SourceBuiltin {
						continue
					}
					dynamicCount++
					switch {
					case strings.HasSuffix(tool.ID, ".old"):
						oldCount++
					case strings.HasSuffix(tool.ID, ".new"):
						newCount++
					}
				}
				if dynamicCount != 3 || (oldCount != 3 && newCount != 3) {
					select {
					case readerErrors <- fmt.Sprintf("revision=%d dynamic=%d old=%d new=%d", catalog.Revision, dynamicCount, oldCount, newCount):
					default:
					}
					return
				}
			}
		}()
	}
	for index := range 200 {
		version := "old"
		if index%2 == 0 {
			version = "new"
		}
		if _, err := registry.ReplaceSources(batch(version)); err != nil {
			close(stop)
			readers.Wait()
			t.Fatalf("publish %s batch: %v", version, err)
		}
	}
	close(stop)
	readers.Wait()
	select {
	case detail := <-readerErrors:
		t.Fatalf("reader observed a mixed batch: %s", detail)
	default:
	}
}

func TestCatalogUsesProviderSafeUniqueWireNames(t *testing.T) {
	longID := "mcp.server.with.dots." + strings.Repeat("very-long-tool-name-", 6)
	registry, err := NewRegistry([]ToolDef{
		testTool("read", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	catalog, err := registry.ReplaceSource(SourceMCP, []ToolDef{
		testTool("mcp.server.tool.with.dots", SourceMCP, `{ "type":"object", "additionalProperties":false }`),
		testTool(longID, SourceMCP, `{ "type":"object", "additionalProperties":false }`),
	})
	if err != nil {
		t.Fatalf("ReplaceSource: %v", err)
	}

	seen := map[string]bool{}
	for _, tool := range catalog.Tools {
		if !providerToolNamePattern.MatchString(tool.WireName) {
			t.Errorf("wire name %q is not provider-safe", tool.WireName)
		}
		if len(tool.WireName) > maxProviderToolNameLength {
			t.Errorf("wire name length = %d, want <= %d", len(tool.WireName), maxProviderToolNameLength)
		}
		if seen[tool.WireName] {
			t.Fatalf("duplicate wire name %q", tool.WireName)
		}
		seen[tool.WireName] = true

		resolved, ok := registry.ToolByWireName(catalog.Revision, tool.WireName)
		if !ok || resolved.ID != tool.ID {
			t.Errorf("wire lookup %q = %+v, %v; want ID %q", tool.WireName, resolved, ok, tool.ID)
		}
	}
}

func TestResolveCanonicalizesAndValidatesClosedSchema(t *testing.T) {
	registry, err := NewRegistry([]ToolDef{
		testTool("write", SourceBuiltin, `{
			"type":"object",
			"properties":{
				"path":{"type":"string","minLength":1},
				"content":{"type":"string"},
				"options":{
					"type":"object",
					"properties":{"overwrite":{"type":"boolean"}},
					"additionalProperties":false
				}
			},
			"required":["path","content"],
			"additionalProperties":false
		}`),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	revision := registry.Snapshot().Revision

	left, err := registry.Resolve(revision, "write", json.RawMessage(`{
		"content":"hello", "options":{"overwrite":true}, "path":"src/a.txt"
	}`))
	if err != nil {
		t.Fatalf("Resolve(left): %v", err)
	}
	right, err := registry.Resolve(revision, "write", json.RawMessage(`{
		"path":"src/a.txt", "options":{"overwrite":true}, "content":"hello"
	}`))
	if err != nil {
		t.Fatalf("Resolve(right): %v", err)
	}
	if string(left.Arguments) != string(right.Arguments) || left.ArgumentsHash != right.ArgumentsHash {
		t.Fatalf("canonical invocation mismatch:\nleft=%s %s\nright=%s %s", left.Arguments, left.ArgumentsHash, right.Arguments, right.ArgumentsHash)
	}

	tests := []struct {
		name string
		args string
	}{
		{"unknown property", `{"path":"a","content":"x","command":"bad"}`},
		{"missing required", `{"path":"a"}`},
		{"wrong nested type", `{"path":"a","content":"x","options":{"overwrite":"yes"}}`},
		{"unknown nested property", `{"path":"a","content":"x","options":{"mode":"force"}}`},
		{"empty constrained string", `{"path":"","content":"x"}`},
		{"non object", `["a"]`},
		{"trailing json", `{"path":"a","content":"x"} {}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := registry.Resolve(revision, "write", json.RawMessage(tt.args)); !errors.Is(err, ErrInvalidArguments) {
				t.Fatalf("Resolve(%s) error = %v, want ErrInvalidArguments", tt.args, err)
			}
		})
	}
}

func TestResolveRejectsUnknownToolAndWireName(t *testing.T) {
	registry, err := NewRegistry([]ToolDef{
		testTool("read", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	revision := registry.Snapshot().Revision
	if _, err := registry.Resolve(revision, "missing", json.RawMessage(`{}`)); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("unknown ID error = %v, want ErrUnknownTool", err)
	}
	if _, err := registry.ResolveWire(revision, "missing", json.RawMessage(`{}`)); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("unknown wire name error = %v, want ErrUnknownTool", err)
	}
}

func TestRegistryRejectsUnsafeApprovalAndMutationCombinations(t *testing.T) {
	tests := []struct {
		name     string
		risk     Risk
		approval ApprovalMode
		mutation MutationMode
	}{
		{"elevated backend policy", RiskElevated, ApprovalBackendPolicy, MutationNone},
		{"dangerous backend policy", RiskDangerous, ApprovalBackendPolicy, MutationExternal},
		{"mutating backend policy", RiskReadOnly, ApprovalBackendPolicy, MutationWorkspaceTransaction},
		{"read-only external mutation", RiskReadOnly, ApprovalManual, MutationExternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := testTool("unsafe", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`)
			def.Risk = tt.risk
			def.Approval = tt.approval
			def.Mutation = tt.mutation
			if _, err := NewRegistry([]ToolDef{def}); !errors.Is(err, ErrInvalidToolDef) {
				t.Fatalf("NewRegistry error = %v, want ErrInvalidToolDef", err)
			}
		})
	}

	external := testTool("mcp.deploy.release", SourceBuiltin, `{ "type":"object", "additionalProperties":false }`)
	external.Risk = RiskDangerous
	external.Approval = ApprovalManual
	external.Mutation = MutationExternal
	if _, err := NewRegistry([]ToolDef{external}); err != nil {
		t.Fatalf("explicit manual external mutation should be representable: %v", err)
	}
}
