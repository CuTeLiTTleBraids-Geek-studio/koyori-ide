package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
)

// G-FEAT-04 / M-7: Stage and Unstage must reject file paths that escape the
// repository via parent traversal (".."). Without validation, a crafted
// filePath like "../secret.txt" could stage or reset files outside the repo.
func TestGitService_M7_Stage_RejectsPathTraversal(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	svc := &GitService{}

	err := svc.Stage(dir, "../evil.txt")
	if err == nil {
		t.Fatal("expected Stage to reject parent-traversal path '../evil.txt', got nil")
	}
}

func TestGitService_M7_Unstage_RejectsPathTraversal(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	svc := &GitService{}
	_ = svc.Stage(dir, "a.txt")

	err := svc.Unstage(dir, "../evil.txt")
	if err == nil {
		t.Fatal("expected Unstage to reject parent-traversal path '../evil.txt', got nil")
	}
}

// M-7: absolute paths must also be rejected.
func TestGitService_M7_Stage_RejectsAbsolutePath(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	svc := &GitService{}

	abs := filepath.Join(t.TempDir(), "outside.txt")
	err := svc.Stage(dir, abs)
	if err == nil {
		t.Fatal("expected Stage to reject absolute path, got nil")
	}
}

// M-7: a normal relative path inside the repo should still work.
func TestGitService_M7_Stage_AllowsSafeRelativePath(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "sub/a.txt", "hello")
	svc := &GitService{}

	if err := svc.Stage(dir, "sub/a.txt"); err != nil {
		t.Fatalf("Stage should accept safe relative path 'sub/a.txt', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// G-FEAT-04: .gitignore template generation
// ---------------------------------------------------------------------------

func TestGitService_GitignoreTemplate_Go(t *testing.T) {
	svc := &GitService{}
	tmpl, err := svc.GitignoreTemplate("go")
	if err != nil {
		t.Fatalf("GitignoreTemplate(go) failed: %v", err)
	}
	for _, want := range []string{"*.exe", "*.test", "*.out", "vendor/", ".air.toml"} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("Go .gitignore template should contain %q, got:\n%s", want, tmpl)
		}
	}
}

func TestGitService_GitignoreTemplate_TypeScript(t *testing.T) {
	svc := &GitService{}
	tmpl, err := svc.GitignoreTemplate("typescript")
	if err != nil {
		t.Fatalf("GitignoreTemplate(typescript) failed: %v", err)
	}
	for _, want := range []string{"node_modules/", "dist/", "*.js.map", ".env"} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("TypeScript .gitignore template should contain %q, got:\n%s", want, tmpl)
		}
	}
}

func TestGitService_GitignoreTemplate_JavaScript(t *testing.T) {
	svc := &GitService{}
	tmpl, err := svc.GitignoreTemplate("javascript")
	if err != nil {
		t.Fatalf("GitignoreTemplate(javascript) failed: %v", err)
	}
	for _, want := range []string{"node_modules/", "dist/", ".env"} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("JavaScript .gitignore template should contain %q, got:\n%s", want, tmpl)
		}
	}
}

func TestGitService_GitignoreTemplate_General(t *testing.T) {
	svc := &GitService{}
	tmpl, err := svc.GitignoreTemplate("general")
	if err != nil {
		t.Fatalf("GitignoreTemplate(general) failed: %v", err)
	}
	for _, want := range []string{".DS_Store", "Thumbs.db", ".idea/", ".vscode/"} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("General .gitignore template should contain %q, got:\n%s", want, tmpl)
		}
	}
}

func TestGitService_GitignoreTemplate_UnknownType(t *testing.T) {
	svc := &GitService{}
	_, err := svc.GitignoreTemplate("cobol")
	if err == nil {
		t.Fatal("expected error for unknown project type")
	}
}

func TestGitService_GitignoreTemplate_CaseInsensitive(t *testing.T) {
	svc := &GitService{}
	tmpl, err := svc.GitignoreTemplate("Go")
	if err != nil {
		t.Fatalf("GitignoreTemplate should be case-insensitive, got: %v", err)
	}
	if !strings.Contains(tmpl, "*.exe") {
		t.Errorf("expected Go template for 'Go', got:\n%s", tmpl)
	}
}

func TestGitService_CreateGitignore(t *testing.T) {
	workspace := t.TempDir()
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	if err := svc.CreateGitignore("go"); err != nil {
		t.Fatalf("CreateGitignore failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".gitignore"))
	if err != nil {
		t.Fatalf("expected .gitignore to be created: %v", err)
	}
	if !strings.Contains(string(data), "*.exe") {
		t.Errorf("created .gitignore should contain Go entries, got:\n%s", string(data))
	}
}

func TestGitService_CreateGitignore_NoWorkspaceRoot(t *testing.T) {
	svc := &GitService{}
	if err := svc.CreateGitignore("go"); err == nil {
		t.Fatal("expected error when no workspace root is set")
	}
}

func TestGitService_CreateGitignore_DoesNotOverwrite(t *testing.T) {
	workspace := t.TempDir()
	existing := "# custom\n*.log\n"
	if err := os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatal(err)
	}
	err := svc.CreateGitignore("go")
	if err == nil {
		t.Fatal("expected error when .gitignore already exists")
	}
	data, readErr := os.ReadFile(filepath.Join(workspace, ".gitignore"))
	if readErr != nil {
		t.Fatalf("read existing .gitignore: %v", readErr)
	}
	if string(data) != existing {
		t.Fatalf("existing .gitignore was modified: %q", data)
	}
}

func TestGitService_CreateGitignore_ConcurrentCreateIsExclusive(t *testing.T) {
	workspace := t.TempDir()
	services := []*GitService{{}, {}}
	for _, svc := range services {
		if err := svc.setWorkspaceRoot(workspace); err != nil {
			t.Fatalf("SetWorkspaceRoot: %v", err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, len(services))
	for _, svc := range services {
		svc := svc
		go func() {
			<-start
			results <- svc.CreateGitignore("go")
		}()
	}
	close(start)

	successes := 0
	existsErrors := 0
	for range services {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, os.ErrExist):
			existsErrors++
		default:
			t.Fatalf("unexpected concurrent CreateGitignore error: %v", err)
		}
	}
	if successes != 1 || existsErrors != 1 {
		t.Fatalf("concurrent create results: successes=%d exists=%d, want 1/1", successes, existsErrors)
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".gitignore"))
	if err != nil {
		t.Fatalf("read created .gitignore: %v", err)
	}
	if !strings.Contains(string(data), "*.exe") {
		t.Fatalf("created .gitignore is incomplete: %q", data)
	}
}

// ---------------------------------------------------------------------------
// G-FEAT-04: merge conflict listing
// ---------------------------------------------------------------------------

// writeConflictedIndex simulates a merge conflict by writing index entries
// at stages 1 (base), 2 (ours), and 3 (theirs) for the given file path.
// The three blob contents are stored in the object store so the hashes resolve.
func writeConflictedIndex(t *testing.T, dir, file, base, ours, theirs string) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	idx, err := repo.Storer.Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	stages := map[index.Stage]string{
		index.AncestorMode: base,
		index.OurMode:      ours,
		index.TheirMode:    theirs,
	}
	for stage, content := range stages {
		h := writeBlob(t, repo, content)
		idx.Entries = append(idx.Entries, &index.Entry{
			Name:  file,
			Hash:  h,
			Stage: stage,
			Mode:  filemode.Regular,
		})
	}
	if err := repo.Storer.SetIndex(idx); err != nil {
		t.Fatalf("SetIndex: %v", err)
	}
}

// writeBlob stores content as a blob in the repo's object store and returns
// its hash.
func writeBlob(t *testing.T, repo *git.Repository, content string) plumbing.Hash {
	t.Helper()
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		t.Fatalf("object Writer: %v", err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close blob: %v", err)
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("SetEncodedObject: %v", err)
	}
	return h
}

func TestGitService_ListMergeConflicts(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "README.md", "initial\n")
	commitAll(t, dir, "initial")
	// Simulate a conflict in the index for "main.go".
	writeConflictedIndex(t, dir, "main.go", "base content", "ours content", "theirs content")

	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	conflicts, err := svc.ListMergeConflicts()
	if err != nil {
		t.Fatalf("ListMergeConflicts failed: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	c := conflicts[0]
	if c.File != "main.go" {
		t.Errorf("expected file 'main.go', got %q", c.File)
	}
	if c.Base != "base content" {
		t.Errorf("expected base 'base content', got %q", c.Base)
	}
	if c.Ours != "ours content" {
		t.Errorf("expected ours 'ours content', got %q", c.Ours)
	}
	if c.Theirs != "theirs content" {
		t.Errorf("expected theirs 'theirs content', got %q", c.Theirs)
	}
}

func TestGitService_ListMergeConflicts_None(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	commitAll(t, dir, "initial")

	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	conflicts, err := svc.ListMergeConflicts()
	if err != nil {
		t.Fatalf("ListMergeConflicts failed: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts in clean repo, got %d", len(conflicts))
	}
}

func TestGitService_IsRebaseInProgress_False(t *testing.T) {
	dir := initBareRepo(t)
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	inProgress, err := svc.IsRebaseInProgress()
	if err != nil {
		t.Fatalf("IsRebaseInProgress failed: %v", err)
	}
	if inProgress {
		t.Error("expected no rebase in progress on a fresh repo")
	}
}

func TestGitService_IsRebaseInProgress_True(t *testing.T) {
	dir := initBareRepo(t)
	// Simulate an in-progress rebase by creating the rebase-merge directory.
	if err := os.MkdirAll(filepath.Join(dir, ".git", "rebase-merge"), 0755); err != nil {
		t.Fatal(err)
	}
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	inProgress, err := svc.IsRebaseInProgress()
	if err != nil {
		t.Fatalf("IsRebaseInProgress failed: %v", err)
	}
	if !inProgress {
		t.Error("expected rebase in progress after creating rebase-merge dir")
	}
}
