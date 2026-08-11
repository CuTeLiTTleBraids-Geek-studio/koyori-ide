package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchService_Search_findsMatches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello world\nfoo bar\nhello again")
	writeFile(t, dir, "b.txt", "nothing here")

	svc := &SearchService{}
	results, err := svc.Search(dir, "hello", false)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 file with matches, got %d", len(results))
	}
	if results[0].Path != "a.txt" {
		t.Errorf("expected path 'a.txt', got %q", results[0].Path)
	}
	if len(results[0].Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(results[0].Matches))
	}
	if results[0].Matches[0].Line != 1 {
		t.Errorf("expected first match on line 1, got %d", results[0].Matches[0].Line)
	}
	if results[0].Matches[0].Preview != "hello world" {
		t.Errorf("expected preview 'hello world', got %q", results[0].Matches[0].Preview)
	}
}

func TestSearchService_Search_caseInsensitive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "Hello World\nHELLO AGAIN")

	svc := &SearchService{}
	results, err := svc.Search(dir, "hello", true)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 file, got %d", len(results))
	}
	if len(results[0].Matches) != 2 {
		t.Errorf("expected 2 case-insensitive matches, got %d", len(results[0].Matches))
	}
}

func TestSearchService_Search_caseSensitive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "Hello World\nHELLO AGAIN")

	svc := &SearchService{}
	results, err := svc.Search(dir, "Hello", false)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 file, got %d", len(results))
	}
	if len(results[0].Matches) != 1 {
		t.Errorf("expected 1 case-sensitive match, got %d", len(results[0].Matches))
	}
}

func TestSearchService_Search_regexPattern(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "foo123\nbar456\nbaz789")

	svc := &SearchService{}
	results, err := svc.Search(dir, "[a-z]+[0-9]+", false)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 file, got %d", len(results))
	}
	if len(results[0].Matches) != 3 {
		t.Errorf("expected 3 regex matches, got %d", len(results[0].Matches))
	}
}

func TestSearchService_Search_skipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello world")
	// Write a file containing null bytes (binary)
	binPath := filepath.Join(dir, "binary.bin")
	if err := os.WriteFile(binPath, []byte{0x00, 0x01, 0x02, 'h', 'e', 'l', 'l', 'o', 0x00}, 0644); err != nil {
		t.Fatal(err)
	}

	svc := &SearchService{}
	results, err := svc.Search(dir, "hello", false)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	// Should only find in a.txt, not binary.bin
	if len(results) != 1 {
		t.Fatalf("expected 1 result (skipping binary), got %d", len(results))
	}
	if results[0].Path != "a.txt" {
		t.Errorf("expected only a.txt, got %q", results[0].Path)
	}
}

func TestSearchService_Search_ignoresNodeModules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello")
	writeFile(t, dir, "node_modules/lib.js", "hello from node_modules")
	writeFile(t, dir, ".git/config", "hello from git")

	svc := &SearchService{}
	results, err := svc.Search(dir, "hello", false)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (ignoring node_modules/.git), got %d", len(results))
	}
	if results[0].Path != "a.txt" {
		t.Errorf("expected only a.txt, got %q", results[0].Path)
	}
}

func TestSearch_InvalidRegex(t *testing.T) {
	dir := t.TempDir()
	svc := &SearchService{}
	_, err := svc.Search(dir, "[invalid", false)
	if err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestSearch_AcceptsPatternAtMaximumLength(t *testing.T) {
	dir := t.TempDir()
	svc := &SearchService{}

	if _, err := svc.Search(dir, strings.Repeat("a", 4096), false); err != nil {
		t.Fatalf("expected 4096-byte pattern to be accepted, got %v", err)
	}
}

func TestSearch_RejectsOversizedPattern(t *testing.T) {
	dir := t.TempDir()
	svc := &SearchService{}

	_, err := svc.Search(dir, strings.Repeat("a", 4097), false)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for oversized pattern, got %v", err)
	}
}

func TestSearch_RejectsOversizedFileBeforeReading(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(searchMaxFileBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = (&SearchService{}).Search(dir, "needle", false)
	if !errors.Is(err, ErrSearchBudgetExceeded) {
		t.Fatalf("Search error = %v, want ErrSearchBudgetExceeded", err)
	}
}

func TestSearch_RejectsExcessiveMatches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "many.txt", strings.Repeat("needle\n", searchMaxMatches+1))

	_, err := (&SearchService{}).Search(dir, "needle", false)
	if !errors.Is(err, ErrSearchBudgetExceeded) {
		t.Fatalf("Search error = %v, want ErrSearchBudgetExceeded", err)
	}
}

func TestSearch_ContextCancellationStopsTraversal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "needle")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (&SearchService{}).searchWithGlobsContext(ctx, dir, "needle", false, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Search error = %v, want context.Canceled", err)
	}
}

func TestSearchWithGlobs_IncludesAndExcludesWorkspaceRelativePaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "needle")
	writeFile(t, dir, "pkg/keep.go", "needle")
	writeFile(t, dir, "pkg/keep_test.go", "needle")
	writeFile(t, dir, "web/app.ts", "needle")

	results, err := (&SearchService{}).SearchWithGlobs(
		context.Background(),
		dir,
		"needle",
		false,
		[]string{"**/*.go"},
		[]string{"**/*_test.go"},
	)
	if err != nil {
		t.Fatalf("SearchWithGlobs failed: %v", err)
	}
	if len(results) != 2 || results[0].Path != "main.go" || results[1].Path != "pkg/keep.go" {
		t.Fatalf("unexpected filtered results: %+v", results)
	}
}

func TestSearchWithGlobs_InvalidGlobReturnsError(t *testing.T) {
	dir := t.TempDir()
	_, err := (&SearchService{}).SearchWithGlobs(context.Background(), dir, "needle", false, []string{"[broken"}, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid include glob") {
		t.Fatalf("expected invalid include glob error, got %v", err)
	}
}

func TestSearchWithGlobs_PropagatesCallerCancellation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "needle")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (&SearchService{}).SearchWithGlobs(ctx, dir, "needle", false, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchWithGlobs error = %v, want context.Canceled", err)
	}
}

func TestSearchService_ReplaceInFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	content := "hello world\nhello go\nbye world\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	svc := &SearchService{}
	result, err := svc.Replace(filePath, "hello", "hi", true)
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}
	if result.Replacements != 2 {
		t.Errorf("expected 2 replacements, got %d", result.Replacements)
	}

	data, _ := os.ReadFile(filePath)
	expected := "hi world\nhi go\nbye world\n"
	if string(data) != expected {
		t.Errorf("file content mismatch:\n got: %q\nwant: %q", string(data), expected)
	}
}

func TestSearchService_Replace_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	content := "Hello HELLO hello\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	svc := &SearchService{}
	result, err := svc.Replace(filePath, "hello", "hi", false)
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}
	if result.Replacements != 3 {
		t.Errorf("expected 3 case-insensitive replacements, got %d", result.Replacements)
	}
}

func TestSearchService_Replace_Regex(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	content := "foo123 bar456\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	svc := &SearchService{}
	result, err := svc.Replace(filePath, `foo(\d+)`, `baz$1`, true)
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}
	if result.Replacements != 1 {
		t.Errorf("expected 1 regex replacement, got %d", result.Replacements)
	}

	data, _ := os.ReadFile(filePath)
	if string(data) != "baz123 bar456\n" {
		t.Errorf("regex replace mismatch: %q", string(data))
	}
}

func TestSearchService_ReplacePreviewRequiresUnchangedBaseline(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := &SearchService{}

	preview, err := svc.PreviewReplace(filePath, "hello", "goodbye", true)
	if err != nil {
		t.Fatalf("PreviewReplace failed: %v", err)
	}
	if preview.Replacements != 1 || preview.OriginalHash == "" || preview.ModifiedContent != "goodbye world\n" {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if data, _ := os.ReadFile(filePath); string(data) != "hello world\n" {
		t.Fatalf("preview wrote the file: %q", data)
	}

	if err := os.WriteFile(filePath, []byte("changed externally\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = svc.ApplyReplacePreview(filePath, preview.OriginalHash, "hello", "goodbye", true)
	if err == nil || !strings.Contains(err.Error(), "changed since preview") {
		t.Fatalf("expected baseline conflict, got %v", err)
	}
	if data, _ := os.ReadFile(filePath); string(data) != "changed externally\n" {
		t.Fatalf("conflict path overwrote the file: %q", data)
	}
}

func TestSearchService_ApplyReplacePreviewWritesSelectedBaseline(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := &SearchService{}
	preview, err := svc.PreviewReplace(filePath, "hello", "hi", true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.ApplyReplacePreview(filePath, preview.OriginalHash, "hello", "hi", true)
	if err != nil {
		t.Fatalf("ApplyReplacePreview failed: %v", err)
	}
	if result.Replacements != 1 {
		t.Fatalf("expected one replacement, got %+v", result)
	}
	if data, _ := os.ReadFile(filePath); string(data) != "hi world\n" {
		t.Fatalf("unexpected applied content: %q", data)
	}
}

// N-67: SearchService workspace sandbox — when SetWorkspaceRoot is set,
// Search and Replace must reject paths outside the workspace. This prevents
// the frontend from searching or modifying files outside the open project.
func TestSearchService_N67_SetWorkspaceRoot_RejectsOutsidePath(t *testing.T) {
	workspace := t.TempDir()
	// Create a directory with content OUTSIDE the workspace.
	outsideDir := t.TempDir()
	writeFile(t, outsideDir, "a.txt", "hello world")
	outsideFile := filepath.Join(outsideDir, "a.txt")

	svc := &SearchService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}

	t.Run("Search", func(t *testing.T) {
		_, err := svc.Search(outsideDir, "hello", false)
		if err == nil {
			t.Error("expected error for Search on path outside workspace")
		}
	})
	t.Run("Replace", func(t *testing.T) {
		_, err := svc.Replace(outsideFile, "hello", "hi", true)
		if err == nil {
			t.Error("expected error for Replace on path outside workspace")
		}
	})
}

// N-67: when SetWorkspaceRoot is set, Search and Replace on paths INSIDE
// the workspace should still work.
func TestSearchService_N67_SetWorkspaceRoot_AllowsInsidePath(t *testing.T) {
	workspace := t.TempDir()
	writeFile(t, workspace, "a.txt", "hello world")
	insideFile := filepath.Join(workspace, "a.txt")

	svc := &SearchService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}

	t.Run("Search", func(t *testing.T) {
		results, err := svc.Search(workspace, "hello", false)
		if err != nil {
			t.Fatalf("Search on inside path failed: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("expected 1 result, got %d", len(results))
		}
	})
	t.Run("Replace", func(t *testing.T) {
		_, err := svc.Replace(insideFile, "hello", "hi", true)
		if err != nil {
			t.Fatalf("Replace on inside path failed: %v", err)
		}
	})
}

// N-67: when no workspace root is set (legacy mode), any path is allowed.
func TestSearchService_N67_NoWorkspaceRoot_AllowsAnyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello")
	svc := &SearchService{} // no SetWorkspaceRoot call
	_, err := svc.Search(dir, "hello", false)
	if err != nil {
		t.Errorf("expected success without workspace root, got: %v", err)
	}
}

// N-67: SetWorkspaceRoot with empty string disables sandboxing.
func TestSearchService_N67_EmptyWorkspaceRoot_DisablesSandbox(t *testing.T) {
	workspace := t.TempDir()
	svc := &SearchService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatal(err)
	}
	// Disable sandbox.
	if err := svc.setWorkspaceRoot(""); err != nil {
		t.Fatal(err)
	}
	// Reads retain the legacy no-root policy; mutation paths must not.
	otherDir := t.TempDir()
	writeFile(t, otherDir, "a.txt", "hello")
	_, err := svc.Search(otherDir, "hello", false)
	if err != nil {
		t.Errorf("expected success after disabling sandbox, got: %v", err)
	}
}
