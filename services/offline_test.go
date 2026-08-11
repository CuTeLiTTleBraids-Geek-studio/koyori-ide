package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// G-OFF-01: Offline operation.
//
// These integration-style tests verify that the core IDE services work
// without any network connectivity. Each service is exercised through its
// normal offline code path:
//
//   - LSPService       — language servers are local stdio processes; detection
//                        uses exec.LookPath (local PATH) and queries against a
//                        not-running server return empty results.
//   - ToolchainService — go/gofmt/tsc/eslint/etc. are local binaries resolved
//                        via exec.LookPath and the ToolPaths override.
//   - GitService       — backed by go-git (pure local filesystem operations;
//                        PlainOpen/Worktree.Status never touch the network).
//   - FileService      — local filesystem read/write/list.
//   - SearchService    — local recursive filesystem walk + regexp match.
//   - SettingsService  — local JSON file read/write.
//
// None of the operations below perform any network I/O. They run against a
// temporary directory so the tests are hermetic and pass with the network
// cable unplugged. The build/CI environment does not need to be online for
// any of these to succeed — that is the G-OFF-01 guarantee.

// TestOffline_LSPService_DetectServersNoNetwork verifies that LSP server
// detection works offline: it only consults the local PATH via
// exec.LookPath and never reaches the network.
func TestOffline_LSPService_DetectServersNoNetwork(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	statuses := svc.DetectLSPServers()
	wantLanguages := []string{"go", "typescript", "javascript", "json", "css", "html", "yaml", "eslint"}
	if len(statuses) != len(wantLanguages) {
		t.Fatalf("expected %d status entries, got %d", len(wantLanguages), len(statuses))
	}
	seen := map[string]bool{}
	for _, st := range statuses {
		if st.Language == "" {
			t.Errorf("empty language in status %+v", st)
		}
		seen[st.Language] = true
		// Available reflects exec.LookPath only — a local operation. Both
		// true and false are acceptable here; the point is that the call
		// completes without network access.
	}
	for _, lang := range wantLanguages {
		if !seen[lang] {
			t.Errorf("missing status for language %q", lang)
		}
	}
}

// TestOffline_LSPService_GetCompletionsNoServerReturnsEmpty verifies that
// requesting completions when no LSP server is running returns an empty
// (non-nil) slice with no error — the graceful offline fallback. No network
// call is attempted.
func TestOffline_LSPService_GetCompletionsNoServerReturnsEmpty(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	items, err := svc.GetCompletions(LSPCompletionRequest{
		Language: "go",
		FilePath: filepath.Join(t.TempDir(), "main.go"),
		Line:     0,
		Column:   0,
		Content:  "package main\n",
	})
	if err != nil {
		t.Fatalf("GetCompletions returned error: %v", err)
	}
	if items == nil {
		t.Fatal("GetCompletions returned nil slice; expected empty non-nil slice")
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 completions with no running server, got %d", len(items))
	}
}

// TestOffline_LSPService_GetHoverNoServerReturnsEmpty verifies the hover
// query also degrades gracefully offline.
func TestOffline_LSPService_GetHoverNoServerReturnsEmpty(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	hover, err := svc.GetHover(LSPCompletionRequest{
		Language: "go",
		FilePath: filepath.Join(t.TempDir(), "main.go"),
		Line:     0,
		Column:   0,
		Content:  "package main\n",
	})
	if err != nil {
		t.Fatalf("GetHover returned error: %v", err)
	}
	if hover != "" {
		t.Fatalf("expected empty hover with no running server, got %q", hover)
	}
}

// TestOffline_ToolchainService_DetectToolchainsNoNetwork verifies that
// toolchain detection works offline: every tool is resolved via
// exec.LookPath (or the ToolPaths override), which is a local PATH lookup.
func TestOffline_ToolchainService_DetectToolchainsNoNetwork(t *testing.T) {
	svc := NewToolchainService()
	detected := svc.DetectToolchains()
	if len(detected) == 0 {
		t.Fatal("expected at least one tool entry from DetectToolchains")
	}
	for _, name := range []string{"go", "gofmt", "golangci-lint", "tsc", "eslint"} {
		if _, ok := detected[name]; !ok {
			t.Errorf("DetectToolchains missing entry for %q", name)
		}
	}
}

// TestOffline_ToolchainService_ListCommandsNoNetwork verifies that
// ListToolchainCommands works offline: it only stats local files
// (go.mod / package.json / Makefile) to filter the catalog.
func TestOffline_ToolchainService_ListCommandsNoNetwork(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module offline\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	svc := NewToolchainService()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	cmds := svc.ListToolchainCommands()
	if len(cmds) == 0 {
		t.Fatal("expected non-empty toolchain command list with go.mod present")
	}
	// With go.mod present, Go commands must be offered.
	sawGo := false
	for _, c := range cmds {
		if c.Language == "go" {
			sawGo = true
			break
		}
	}
	if !sawGo {
		t.Error("expected Go toolchain commands to be listed with go.mod present")
	}
}

// TestOffline_ToolchainService_RunCommandNotInstalledNoNetwork verifies that
// running a command whose tool is not installed returns a NotInstalled
// result (with the install hint) without any network attempt. We force a
// miss by overriding the tool name with a non-existent binary.
func TestOffline_ToolchainService_RunCommandNotInstalledNoNetwork(t *testing.T) {
	svc := NewToolchainService()
	// Override golangci-lint with a bare name that cannot resolve on PATH,
	// so resolveTool returns "" without ever spawning a process or reaching
	// the network.
	svc.setToolPaths(map[string]string{"golangci-lint": "golangci-lint-definitely-not-real-xyz"})
	res, err := svc.RunToolchainCommand("golangci-lint", "")
	if err != nil {
		t.Fatalf("RunToolchainCommand returned error: %v", err)
	}
	if res.Success {
		t.Error("expected Success=false for a missing tool")
	}
	if !res.NotInstalled {
		t.Fatal("expected NotInstalled=true for a missing tool")
	}
	if res.InstallCmd == "" {
		t.Error("expected an install hint command for golangci-lint")
	}
}

// TestOffline_GitService_StatusNoNetwork verifies that GitService operates
// offline via the go-git library (pure local filesystem). It creates a local
// repository, writes a file, commits, and reads status — none of which
// requires network access.
func TestOffline_GitService_StatusNoNetwork(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git.PlainInit failed: %v", err)
	}

	// Write a file and commit it so the worktree has history.
	filePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(filePath, []byte("# offline\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com"},
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}

	// GetStatus reads the local worktree status via go-git — no network.
	changes, err := svc.GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus returned error: %v", err)
	}
	// After committing, the worktree should be clean (no changes).
	if len(changes) != 0 {
		t.Errorf("expected clean worktree after commit, got %d changes", len(changes))
	}

	// ListBranches reads local refs only — no remote fetch.
	branches, err := svc.ListBranches(dir)
	if err != nil {
		t.Fatalf("ListBranches returned error: %v", err)
	}
	if len(branches) == 0 {
		t.Error("expected at least one local branch (master/main)")
	}

	// GetBranchInfo returns local branch info; with no upstream it returns
	// zeros rather than attempting a network fetch.
	info, err := svc.GetBranchInfo(dir)
	if err != nil {
		t.Fatalf("GetBranchInfo returned error: %v", err)
	}
	if info.Name == "" {
		t.Error("expected non-empty branch name")
	}
}

// TestOffline_FileService_ReadWriteListNoNetwork verifies that file
// operations work offline: they are pure local filesystem I/O.
func TestOffline_FileService_ReadWriteListNoNetwork(t *testing.T) {
	dir := t.TempDir()
	svc := NewFileService()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}

	rel := filepath.Join("sub", "hello.txt")
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := svc.WriteFile(full, "hello offline"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	content, err := svc.ReadFile(full)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if content != "hello offline" {
		t.Errorf("expected 'hello offline', got %q", content)
	}

	entries, err := svc.ListDirectory(dir)
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected at least one directory entry")
	}
}

// TestOffline_SearchService_SearchNoNetwork verifies that content search
// works offline: it walks the local filesystem and matches regexps in memory.
func TestOffline_SearchService_SearchNoNetwork(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("foo bar baz\n"), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("nothing here\n"), 0644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	svc := &SearchService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	results, err := svc.Search(dir, "bar", false)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 matching file, got %d", len(results))
	}
	if !strings.HasSuffix(results[0].Path, "a.txt") {
		t.Errorf("expected match in a.txt, got %q", results[0].Path)
	}
	if len(results[0].Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results[0].Matches))
	}
}

// TestOffline_SettingsService_LoadSaveNoNetwork verifies that settings
// persistence works offline: it reads and writes a local JSON file.
func TestOffline_SettingsService_LoadSaveNoNetwork(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "settings.json")
	svc := NewSettingsServiceWithPath(cfgPath)

	// LoadSettings on a missing file returns defaults, no error, no network.
	loaded, err := svc.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings (missing file) returned error: %v", err)
	}

	// Mutate and save — pure local file write.
	loaded.Theme = "dark"
	loaded.FontSize = 14
	loaded.AIApiKey = "" // no key — no secret-store network/keyring access
	if err := svc.SaveSettings(loaded); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	// Reload and confirm round-trip from the local file.
	loaded2, err := svc.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings (after save) returned error: %v", err)
	}
	if loaded2.Theme != "dark" {
		t.Errorf("expected theme 'dark' to round-trip, got %q", loaded2.Theme)
	}
	if loaded2.FontSize != 14 {
		t.Errorf("expected fontSize 14 to round-trip, got %d", loaded2.FontSize)
	}
}
