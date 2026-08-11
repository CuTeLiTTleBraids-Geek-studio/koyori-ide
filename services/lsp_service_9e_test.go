package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLSP9ECodeActionParamsIncludeOnlyAndSelection(t *testing.T) {
	params := buildCodeActionParams(LSPCompletionRequest{
		Language:  "go",
		FilePath:  "/workspace/main.go",
		Line:      3,
		Column:    2,
		EndLine:   7,
		EndColumn: 11,
		Only:      []string{"refactor.extract", "refactor.inline", "refactor.rewrite"},
	})
	rangeParam := params["range"].(map[string]interface{})
	if got := rangeParam["end"].(map[string]int); !reflect.DeepEqual(got, map[string]int{"line": 7, "character": 11}) {
		t.Fatalf("range.end = %#v", got)
	}
	contextParam := params["context"].(map[string]interface{})
	if got := contextParam["only"]; !reflect.DeepEqual(got, []string{"refactor.extract", "refactor.inline", "refactor.rewrite"}) {
		t.Fatalf("context.only = %#v", got)
	}
}

func TestLSP9ECodeActionCapabilityDoesNotInventSupport(t *testing.T) {
	supported, kinds := parseCodeActionCapability(json.RawMessage(`{"codeActionProvider":{"codeActionKinds":["quickfix","refactor.extract"]}}`))
	if !supported || !reflect.DeepEqual(kinds, []string{"quickfix", "refactor.extract"}) {
		t.Fatalf("capability = %v %#v", supported, kinds)
	}
	if !codeActionKindsSupported(kinds, []string{"refactor.extract"}) {
		t.Fatal("server-declared refactor.extract was rejected")
	}
	if codeActionKindsSupported(kinds, []string{"refactor.inline"}) {
		t.Fatal("undeclared refactor.inline was invented")
	}
	supported, kinds = parseCodeActionCapability(json.RawMessage(`{"codeActionProvider":false}`))
	if supported || len(kinds) != 0 {
		t.Fatalf("disabled capability = %v %#v", supported, kinds)
	}
}

func TestLSP9EParsesDocumentChangesVersionAndDisabledReason(t *testing.T) {
	raw := json.RawMessage(`[{"title":"Extract method","kind":"refactor.extract","disabled":{"reason":"selection required"},"command":{"title":"Extract method","command":"gopls.extract","arguments":[1,"x"]},"edit":{"documentChanges":[{"textDocument":{"uri":"file:///workspace/main.go","version":4},"edits":[{"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":1}},"newText":"value"}]}]}}]`)
	actions := parseCodeActions(raw)
	if len(actions) != 1 {
		t.Fatalf("actions = %#v", actions)
	}
	action := actions[0]
	if !action.Disabled || action.DisabledReason != "selection required" {
		t.Fatalf("disabled = %v reason=%q", action.Disabled, action.DisabledReason)
	}
	if action.Command != "gopls.extract" || !reflect.DeepEqual(action.CommandArguments, []interface{}{float64(1), "x"}) {
		t.Fatalf("command = %q args=%#v", action.Command, action.CommandArguments)
	}
	if len(action.Edit) != 1 || action.Edit[0].Version == nil || *action.Edit[0].Version != 4 {
		t.Fatalf("edit version lost: %#v", action.Edit)
	}
}

func TestLSP9EWorkspaceEditPreviewUsesUTF16AndHashesBaseline(t *testing.T) {
	raw := json.RawMessage(`{"documentChanges":[{"textDocument":{"uri":"file:///workspace/a.go","version":3},"edits":[{"range":{"start":{"line":0,"character":2},"end":{"line":0,"character":3}},"newText":"X"}]}]}`)
	preview, err := buildWorkspaceEditPreview(raw, func(path string) (string, error) {
		if !strings.HasSuffix(filepathSlash(path), "/workspace/a.go") {
			return "", errors.New("unexpected path")
		}
		return "😀a\n", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Files) != 1 || preview.Files[0].ModifiedContent != "😀X\n" {
		t.Fatalf("preview = %#v", preview)
	}
	if preview.Files[0].BaselineHash != contentHash([]byte("😀a\n")) {
		t.Fatal("baseline hash does not cover original content")
	}
}

func TestLSP9EWorkspaceEditTransactionPreflightsVersionAndHash(t *testing.T) {
	versions := map[string]int{"a.go": 2, "b.go": 5}
	contents := map[string]string{"a.go": "A", "b.go": "B changed"}
	preview := WorkspaceEditPreview{Files: []WorkspaceEditPreviewFile{
		{FilePath: "a.go", Version: intPtr(2), BaselineHash: contentHash([]byte("A")), OriginalContent: "A", ModifiedContent: "A2"},
		{FilePath: "b.go", Version: intPtr(5), BaselineHash: contentHash([]byte("B")), OriginalContent: "B", ModifiedContent: "B2"},
	}}
	writes := 0
	result := applyWorkspaceEditPreviewTransaction(context.Background(), preview,
		func(path string) (int, bool) { v, ok := versions[path]; return v, ok },
		func(path string) (string, error) { return contents[path], nil },
		func(path, content string) error { writes++; contents[path] = content; return nil },
	)
	if result.Applied || writes != 0 || len(result.Conflicts) != 1 || !strings.Contains(result.Conflicts[0], "hash") {
		t.Fatalf("hash conflict result=%#v writes=%d", result, writes)
	}
	contents["b.go"] = "B"
	versions["b.go"] = 6
	result = applyWorkspaceEditPreviewTransaction(context.Background(), preview,
		func(path string) (int, bool) { v, ok := versions[path]; return v, ok },
		func(path string) (string, error) { return contents[path], nil },
		func(path, content string) error { writes++; contents[path] = content; return nil },
	)
	if result.Applied || writes != 0 || len(result.Conflicts) != 1 || !strings.Contains(result.Conflicts[0], "version") {
		t.Fatalf("version conflict result=%#v writes=%d", result, writes)
	}
}

func TestLSP9EWorkspaceEditTransactionRollsBackAndCancels(t *testing.T) {
	contents := map[string]string{"a.go": "A", "b.go": "B"}
	preview := WorkspaceEditPreview{Files: []WorkspaceEditPreviewFile{
		{FilePath: "a.go", BaselineHash: contentHash([]byte("A")), OriginalContent: "A", ModifiedContent: "A2"},
		{FilePath: "b.go", BaselineHash: contentHash([]byte("B")), OriginalContent: "B", ModifiedContent: "B2"},
	}}
	result := applyWorkspaceEditPreviewTransaction(context.Background(), preview, nil,
		func(path string) (string, error) { return contents[path], nil },
		func(path, content string) error {
			if path == "b.go" && content == "B2" {
				return errors.New("write failed")
			}
			contents[path] = content
			return nil
		},
	)
	if result.Applied || contents["a.go"] != "A" || contents["b.go"] != "B" || !strings.Contains(result.FailureReason, "write failed") {
		t.Fatalf("rollback result=%#v contents=%#v", result, contents)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writes := 0
	result = applyWorkspaceEditPreviewTransaction(ctx, preview, nil,
		func(path string) (string, error) { return contents[path], nil },
		func(path, content string) error { writes++; return nil },
	)
	if result.Applied || writes != 0 || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("cancel result=%#v writes=%d", result, writes)
	}
}

func TestLSP9EWorkspaceEditCancellationAfterPreflightAndDuringWrites(t *testing.T) {
	preview := WorkspaceEditPreview{Files: []WorkspaceEditPreviewFile{
		{FilePath: "a.go", BaselineHash: contentHash([]byte("A")), OriginalContent: "A", ModifiedContent: "A2"},
		{FilePath: "b.go", BaselineHash: contentHash([]byte("B")), OriginalContent: "B", ModifiedContent: "B2"},
	}}
	contents := map[string]string{"a.go": "A", "b.go": "B"}
	ctx, cancel := context.WithCancel(context.Background())
	reads := 0
	writes := 0
	result := applyWorkspaceEditPreviewTransaction(ctx, preview, nil,
		func(path string) (string, error) {
			reads++
			if reads == len(preview.Files) {
				cancel()
			}
			return contents[path], nil
		},
		func(path, content string) error { writes++; contents[path] = content; return nil },
	)
	if result.Applied || writes != 0 || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("post-preflight cancel result=%#v writes=%d", result, writes)
	}

	ctx, cancel = context.WithCancel(context.Background())
	writes = 0
	result = applyWorkspaceEditPreviewTransaction(ctx, preview, nil,
		func(path string) (string, error) { return contents[path], nil },
		func(path, content string) error {
			writes++
			contents[path] = content
			if path == "a.go" && content == "A2" {
				cancel()
			}
			return nil
		},
	)
	if result.Applied || !errors.Is(result.Err, context.Canceled) || contents["a.go"] != "A" || contents["b.go"] != "B" {
		t.Fatalf("mid-write cancel result=%#v contents=%#v writes=%d", result, contents, writes)
	}
}

func TestLSP9EApplyPreservesExistingFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve POSIX executable mode bits")
	}
	root := t.TempDir()
	path := filepath.Join(root, "script.sh")
	if err := os.WriteFile(path, []byte("old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	fsvc := NewFileService()
	if err := fsvc.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	svc := NewLSPService(root)
	svc.setFileService(fsvc)
	svc.servers["go"] = &lspServer{docVersions: map[string]int{}, docHashes: map[string]string{}, docLastContent: map[string]string{}, docLastSync: map[string]time.Time{}}
	result := svc.ApplyRefactorWorkspaceEdit(context.Background(), "go", WorkspaceEditPreview{Files: []WorkspaceEditPreviewFile{{
		FilePath: path, BaselineHash: contentHash([]byte("old\n")), OriginalContent: "old\n", ModifiedContent: "new\n",
	}}})
	if !result.Applied {
		t.Fatalf("apply failed: %#v", result)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("mode = %04o, want 0755", got)
	}
}

func filepathSlash(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

func intPtr(value int) *int { return &value }
