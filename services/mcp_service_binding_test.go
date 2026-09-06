package services

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMCPService_SetWorkspaceRootUnavailableToWails(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "mcp_service.go", nil, parser.ParseComments)
	if err != nil {
		file, err = parser.ParseFile(token.NewFileSet(), "services/mcp_service.go", nil, parser.ParseComments)
	}
	if err != nil {
		t.Fatalf("parse mcp_service.go: %v", err)
	}

	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || function.Name.Name != "SetWorkspaceRoot" {
			continue
		}
		t.Fatal("MCPService exposes SetWorkspaceRoot to Wails")
	}
}

func TestMCPServiceRootSetterIsNarrowProjectCapability(t *testing.T) {
	setterType := reflect.TypeOf((*MCPServiceRootSetter)(nil)).Elem()
	if setterType.NumMethod() != 1 || setterType.Method(0).Name != "setWorkspaceRoot" {
		t.Fatalf("MCPServiceRootSetter methods = %v, want only setWorkspaceRoot", setterType)
	}

	field, ok := reflect.TypeOf(ProjectService{}).FieldByName("mcpService")
	if !ok {
		t.Fatal("ProjectService.mcpService field not found")
	}
	if field.Type != setterType {
		t.Fatalf("ProjectService.mcpService type = %v, want %v", field.Type, setterType)
	}
}

type recordingMCPRootSetter struct {
	calls []string
}

func (s *recordingMCPRootSetter) setWorkspaceRoot(root string) error {
	s.calls = append(s.calls, root)
	return nil
}

func TestProjectServiceBuildSettersUsesMCPRootCapability(t *testing.T) {
	setter := &recordingMCPRootSetter{}
	service := &ProjectService{}
	service.setMCPService(setter)

	setters := service.buildWorkspaceRootSetters()
	if len(setters) != 1 {
		t.Fatalf("workspace root setters = %d, want 1", len(setters))
	}
	root := t.TempDir()
	if err := setters[0].set(root); err != nil {
		t.Fatalf("set MCP root: %v", err)
	}
	if len(setter.calls) != 1 || setter.calls[0] != root {
		t.Fatalf("MCP root setter calls = %v, want [%q]", setter.calls, root)
	}
}

func TestProjectServiceRejectsEmptyRootBeforeMCPSetter(t *testing.T) {
	setter := &recordingMCPRootSetter{}
	service := &ProjectService{configPath: t.TempDir() + "/projects.json"}
	service.setMCPService(setter)

	if _, err := service.AddProject(""); err == nil {
		t.Fatal("AddProject accepted an empty workspace root")
	}
	if len(setter.calls) != 0 {
		t.Fatalf("MCP root setter was called for an empty root: %v", setter.calls)
	}
}

func TestMCPServiceSetWorkspaceRootRejectsEmptyWithoutChangingState(t *testing.T) {
	service := newTestMCPService(t)
	root := t.TempDir()
	if err := service.setWorkspaceRoot(root); err != nil {
		t.Fatalf("set initial root: %v", err)
	}

	service.mu.RLock()
	beforeRoot := service.rootDir
	beforeRootGeneration := service.rootGeneration
	beforeLifecycleGeneration := service.lifecycleGeneration
	service.mu.RUnlock()

	if err := service.setWorkspaceRoot(""); err == nil {
		t.Fatal("setWorkspaceRoot accepted an empty root")
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.rootDir != beforeRoot ||
		service.rootGeneration != beforeRootGeneration ||
		service.lifecycleGeneration != beforeLifecycleGeneration {
		t.Fatalf(
			"empty root changed MCP state: root=%q rootGeneration=%d lifecycleGeneration=%d",
			service.rootDir,
			service.rootGeneration,
			service.lifecycleGeneration,
		)
	}
}

// TestMCPServiceRendererBindingContract proves the P1-03-D renderer surface:
// the safe, lease-gated read APIs are really exported by the generated Wails
// binding with Go-shaped parameters, while the deny-only execution shims and
// every internal setter stay unreachable from the renderer.
func TestMCPServiceRendererBindingContract(t *testing.T) {
	bindingPath := filepath.Join("..", "frontend", "bindings", "github.com", "CuTeLiTTleBraids-Geek-studio", "koyori-ide", "services", "mcpservice.ts")
	source, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatalf("read generated binding: %v", err)
	}
	text := string(source)

	requiredExports := []string{
		"ListResources(name: string): $CancellablePromise<$models.MCPResource[] | null>",
		"ReadResource(name: string, uri: string): $CancellablePromise<$models.MCPResourceRead | null>",
		"ListPrompts(name: string): $CancellablePromise<$models.MCPPrompt[] | null>",
		"GetPrompt(name: string, prompt: string, args: { [_ in string]?: string } | null): $CancellablePromise<$models.MCPPromptRender | null>",
		"ServerCapabilities(name: string): $CancellablePromise<$models.MCPCapabilitySnapshot>",
	}
	for _, signature := range requiredExports {
		if !strings.Contains(text, signature) {
			t.Fatalf("generated binding is missing the Go-shaped export %q", signature)
		}
	}

	forbiddenExports := []string{
		"CallTool(",
		"RequestToolApproval(",
		"ExecuteApprovedTool(",
		"SetWorkspaceRoot(",
		"setWorkspaceContext(",
		"Close(",
	}
	for _, forbidden := range forbiddenExports {
		if strings.Contains(text, "export function "+forbidden) {
			t.Fatalf("generated binding must not export deny-only method %s", forbidden)
		}
	}
}

// TestMCPServiceRendererReadAPIsFailClosedWithoutWorkspace proves the newly
// exposed read APIs are safe renderer entry points: without a committed
// workspace identity they fail closed before any server interaction.
func TestMCPServiceRendererReadAPIsFailClosedWithoutWorkspace(t *testing.T) {
	service := newTestMCPService(t)
	ctx := context.Background()

	if _, err := service.ListResources(ctx, "srv"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ListResources without workspace = %v, want ErrNotAllowed", err)
	}
	if _, err := service.ReadResource(ctx, "srv", "file:///x"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ReadResource without workspace = %v, want ErrNotAllowed", err)
	}
	if _, err := service.ListPrompts(ctx, "srv"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ListPrompts without workspace = %v, want ErrNotAllowed", err)
	}
	if _, err := service.GetPrompt(ctx, "srv", "p", nil); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("GetPrompt without workspace = %v, want ErrNotAllowed", err)
	}
	if _, err := service.ServerCapabilities("srv"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ServerCapabilities without connection = %v, want ErrNotFound", err)
	}
}
