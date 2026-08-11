package services

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
)

func TestGitService_8B_APISurface(t *testing.T) {
	typeOfService := reflect.TypeOf(&GitService{})
	for _, method := range []string{"GetBlameAtRevision", "GetCommitGraph"} {
		if _, ok := typeOfService.MethodByName(method); !ok {
			t.Errorf("GitService is missing %s", method)
		}
	}
}

func setup8BGitRepo(t *testing.T) (string, *GitService) {
	t.Helper()
	skipIfNoGit(t)
	dir := initBareRepo(t)
	setLocalGitConfig(t, dir)
	writeFile(t, dir, "tracked.txt", "first\nsecond\n")
	commitAll(t, dir, "initial | subject")
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	return dir, svc
}

func TestGitService_8B_BlameAtRevisionAndRange(t *testing.T) {
	dir, svc := setup8BGitRepo(t)
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, dir, "tracked.txt", "changed\nsecond\n")
	commitAll(t, dir, "change first line")

	lines, err := svc.GetBlameAtRevision(dir, "tracked.txt", 1, 1, initial.Hash().String())
	if err != nil {
		t.Fatalf("GetBlameAtRevision: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("blame line count = %d, want 1", len(lines))
	}
	if lines[0].Line != 1 || lines[0].Commit != shortSHA(initial.Hash().String()) {
		t.Fatalf("blame = %#v, want initial commit line 1", lines[0])
	}
}

func TestGitService_8B_BlameRejectsUnsafeInputs(t *testing.T) {
	dir, svc := setup8BGitRepo(t)

	for _, tc := range []struct {
		name       string
		file       string
		start, end int
		revision   string
	}{
		{name: "path traversal", file: "../outside.txt", start: 1, end: 1},
		{name: "reversed range", file: "tracked.txt", start: 2, end: 1},
		{name: "partial range", file: "tracked.txt", start: 1, end: 0},
		{name: "oversized range", file: "tracked.txt", start: 1, end: 5001},
		{name: "revision option", file: "tracked.txt", start: 1, end: 1, revision: "--help"},
		{name: "revision shell text", file: "tracked.txt", start: 1, end: 1, revision: "HEAD;touch owned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.GetBlameAtRevision(dir, tc.file, tc.start, tc.end, tc.revision); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGitService_8B_BlameTreatsFilePathAsSingleArgv(t *testing.T) {
	dir, svc := setup8BGitRepo(t)
	const fileName = "safe;touch-owned.txt"
	writeFile(t, dir, fileName, "content\n")
	commitAll(t, dir, "add metacharacter filename")

	lines, err := svc.GetBlameAtRevision(dir, fileName, 1, 1, "")
	if err != nil {
		t.Fatalf("GetBlameAtRevision: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("blame line count = %d, want 1", len(lines))
	}
	if _, err := os.Stat(filepath.Join(dir, "owned.txt")); !os.IsNotExist(err) {
		t.Fatalf("shell-like file path created an unexpected file: %v", err)
	}
}

func TestGitService_8B_CommitGraphStructuredOutput(t *testing.T) {
	dir, svc := setup8BGitRepo(t)
	writeFile(t, dir, "tracked.txt", "second commit\n")
	commitAll(t, dir, "second | subject")

	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	branch := head.Name().Short()

	entries, err := svc.GetCommitGraph(dir, 10, branch, false)
	if err != nil {
		t.Fatalf("GetCommitGraph: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("graph entries = %d, want 2", len(entries))
	}
	first := entries[0]
	if first.Hash != head.Hash().String() || first.Subject != "second | subject" {
		t.Fatalf("first entry = %#v", first)
	}
	if len(first.Parents) != 1 || first.Author == "" || first.Email == "" || first.Time == "" {
		t.Fatalf("incomplete structured entry = %#v", first)
	}
	if len(first.Refs) == 0 || !strings.Contains(strings.Join(first.Refs, ","), branch) {
		t.Fatalf("refs = %#v, want current branch %q", first.Refs, branch)
	}
}

func TestGitService_8B_CommitGraphEmptyRepo(t *testing.T) {
	skipIfNoGit(t)
	dir := initBareRepo(t)
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := svc.GetCommitGraph(dir, 50, "", false)
	if err != nil {
		t.Fatalf("GetCommitGraph empty repo: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("empty repo entries = %d, want 0", len(entries))
	}
}

func TestGitService_8B_CommitGraphRejectsUnsafeBranchAndWorkspace(t *testing.T) {
	dir, svc := setup8BGitRepo(t)
	for _, branch := range []string{"-n999", "main;touch owned", "../outside", "main $(whoami)"} {
		if _, err := svc.GetCommitGraph(dir, 50, branch, false); err == nil {
			t.Errorf("GetCommitGraph branch %q should fail", branch)
		}
	}

	outside := t.TempDir()
	if _, err := svc.GetCommitGraph(outside, 50, "", false); err == nil {
		t.Fatal("GetCommitGraph should reject repo outside workspace")
	}
}

func TestGitService_8B_CommitGraphCapsLimit(t *testing.T) {
	dir, svc := setup8BGitRepo(t)
	for i := 0; i < 205; i++ {
		writeFile(t, dir, "counter.txt", fmt.Sprintf("%d\n", i))
		commitAll(t, dir, fmt.Sprintf("commit %03d", i))
	}

	entries, err := svc.GetCommitGraph(dir, 10_000, "", false)
	if err != nil {
		t.Fatalf("GetCommitGraph: %v", err)
	}
	if len(entries) != 200 {
		t.Fatalf("capped graph entries = %d, want 200", len(entries))
	}
}
