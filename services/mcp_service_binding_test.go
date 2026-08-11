package services

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
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
