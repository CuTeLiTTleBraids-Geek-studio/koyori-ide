package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestG12MultiRootCoordinatesFileLSPSearchSymbolAndGeneration(t *testing.T) {
	initial := t.TempDir()
	rootA := t.TempDir()
	rootB := t.TempDir()
	secondaryFile := filepath.Join(rootB, "secondary.go")
	if err := os.WriteFile(secondaryFile, []byte("package secondary\n\nfunc SecondarySymbol() {}\n// needle-secondary\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := NewWorkspaceContext()
	if err := ctx.Set(initial); err != nil {
		t.Fatal(err)
	}
	beforeGeneration := ctx.Generation()
	fileService := NewFileService()
	if err := fileService.setWorkspaceRoot(initial); err != nil {
		t.Fatal(err)
	}
	searchService := NewSearchService()
	if err := searchService.setWorkspaceRoot(initial); err != nil {
		t.Fatal(err)
	}
	lspService := NewLSPService(initial)
	symbolService := NewSymbolIndexService()
	symbolService.setWorkspaceRoot(initial)

	projectService := &ProjectService{
		configPath:         filepath.Join(t.TempDir(), "projects.json"),
		fileService:        fileService,
		searchService:      searchService,
		lspService:         lspService,
		symbolIndexService: symbolService,
		wsCtx:              ctx,
	}
	project, err := projectService.AddMultiRootProject([]string{rootA, rootB}, "")
	if err != nil {
		t.Fatalf("AddMultiRootProject: %v", err)
	}

	wantRoots := []string{mustAbsG12(t, rootA), mustAbsG12(t, rootB)}
	if project.Path != wantRoots[0] || len(project.Roots) != 2 {
		t.Fatalf("project = %+v, want primary %q and two roots", project, wantRoots[0])
	}
	if got, generation := ctx.Snapshot(); got != wantRoots[0] || generation != beforeGeneration+1 {
		t.Fatalf("workspace context = (%q,%d), want (%q,%d)", got, generation, wantRoots[0], beforeGeneration+1)
	}
	assertG12Roots(t, "file", fileService.WorkspaceRoots(), wantRoots)
	assertG12Roots(t, "lsp", lspService.WorkspaceRoots(), wantRoots)
	assertG12Roots(t, "search", searchService.WorkspaceRoots(), wantRoots)
	assertG12Roots(t, "symbol", symbolService.WorkspaceRoots(), wantRoots)

	if _, err := fileService.ReadFile(secondaryFile); err != nil {
		t.Fatalf("FileService rejected secondary root: %v", err)
	}
	results, err := searchService.Search(rootB, "needle-secondary", false)
	if err != nil || len(results) != 1 {
		t.Fatalf("SearchService secondary-root search = (%v,%v), want one result", results, err)
	}
	symbols, err := symbolService.SearchSymbols(context.Background(), "SecondarySymbol", 10)
	if err != nil || len(symbols) != 1 || symbols[0].FilePath != secondaryFile {
		t.Fatalf("SymbolIndex secondary-root search = (%v,%v)", symbols, err)
	}
}

func TestG12MultiRootRejectsInvalidRootWithoutPartialSwitch(t *testing.T) {
	initial := t.TempDir()
	valid := t.TempDir()
	invalid := filepath.Join(t.TempDir(), "missing")
	ctx := NewWorkspaceContext()
	if err := ctx.Set(initial); err != nil {
		t.Fatal(err)
	}
	fileService := NewFileService()
	if err := fileService.setWorkspaceRoot(initial); err != nil {
		t.Fatal(err)
	}
	searchService := NewSearchService()
	if err := searchService.setWorkspaceRoot(initial); err != nil {
		t.Fatal(err)
	}
	lspService := NewLSPService(initial)
	symbolService := NewSymbolIndexService()
	symbolService.setWorkspaceRoot(initial)
	projectService := &ProjectService{
		configPath:         filepath.Join(t.TempDir(), "projects.json"),
		fileService:        fileService,
		searchService:      searchService,
		lspService:         lspService,
		symbolIndexService: symbolService,
		wsCtx:              ctx,
	}
	beforeRoot, beforeGeneration := ctx.Snapshot()

	if _, err := projectService.AddMultiRootProject([]string{valid, invalid}, ""); err == nil {
		t.Fatal("invalid secondary root unexpectedly succeeded")
	}
	if got, generation := ctx.Snapshot(); got != beforeRoot || generation != beforeGeneration {
		t.Fatalf("workspace context changed after rejection: (%q,%d), want (%q,%d)", got, generation, beforeRoot, beforeGeneration)
	}
	want := []string{mustAbsG12(t, initial)}
	assertG12Roots(t, "file", fileService.WorkspaceRoots(), want)
	assertG12Roots(t, "lsp", lspService.WorkspaceRoots(), want)
	assertG12Roots(t, "search", searchService.WorkspaceRoots(), want)
	assertG12Roots(t, "symbol", symbolService.WorkspaceRoots(), want)
}

func TestG12MultiRootPersistenceFailureRestoresGenerationAndRootLists(t *testing.T) {
	initial := t.TempDir()
	rootA := t.TempDir()
	rootB := t.TempDir()
	ctx := NewWorkspaceContext()
	if err := ctx.Set(initial); err != nil {
		t.Fatal(err)
	}
	fileService := NewFileService()
	if err := fileService.setWorkspaceRoot(initial); err != nil {
		t.Fatal(err)
	}
	searchService := NewSearchService()
	if err := searchService.setWorkspaceRoot(initial); err != nil {
		t.Fatal(err)
	}
	lspService := NewLSPService(initial)
	symbolService := NewSymbolIndexService()
	symbolService.setWorkspaceRoot(initial)
	invalidConfigPath := filepath.Join(t.TempDir(), "projects.json")
	if err := os.Mkdir(invalidConfigPath, 0o755); err != nil {
		t.Fatal(err)
	}
	projectService := &ProjectService{
		configPath:         invalidConfigPath,
		fileService:        fileService,
		searchService:      searchService,
		lspService:         lspService,
		symbolIndexService: symbolService,
		wsCtx:              ctx,
	}
	beforeRoot, beforeGeneration := ctx.Snapshot()

	if _, err := projectService.AddMultiRootProject([]string{rootA, rootB}, ""); err == nil {
		t.Fatal("persistence failure unexpectedly succeeded")
	}
	if got, generation := ctx.Snapshot(); got != beforeRoot || generation != beforeGeneration {
		t.Fatalf("workspace context after rollback = (%q,%d), want (%q,%d)", got, generation, beforeRoot, beforeGeneration)
	}
	want := []string{mustAbsG12(t, initial)}
	assertG12Roots(t, "file", fileService.WorkspaceRoots(), want)
	assertG12Roots(t, "lsp", lspService.WorkspaceRoots(), want)
	assertG12Roots(t, "search", searchService.WorkspaceRoots(), want)
	assertG12Roots(t, "symbol", symbolService.WorkspaceRoots(), want)
}

func mustAbsG12(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func assertG12Roots(t *testing.T, service string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s roots = %v, want %v", service, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s roots = %v, want %v", service, got, want)
		}
	}
}
