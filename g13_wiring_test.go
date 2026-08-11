package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestG13AdvancedGitServicesAreConstructedAndRegistered(t *testing.T) {
	cfg := bootstrapConfig{
		configDir: t.TempDir(),
		koyoriDir: t.TempDir(),
	}
	serviceSet := buildCoreServices(cfg)
	if serviceSet.GitWorktree == nil {
		t.Fatal("GitWorktreeService was not constructed")
	}
	if serviceSet.GitRebase == nil {
		t.Fatal("GitRebaseService was not constructed")
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bootstrap_services.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		selector, ok := call.Args[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "b" {
			registered[selector.Sel.Name] = true
		}
		return true
	})
	for _, name := range []string{"GitWorktree", "GitRebase"} {
		if !registered[name] {
			t.Errorf("appBundle.%s is not registered with Wails", name)
		}
	}
}
