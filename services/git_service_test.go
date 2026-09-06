package services

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

var testAuthor = object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()}

func TestGitService_RunGitContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (&GitService{}).runGitContext(ctx, t.TempDir(), "--version")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runGitContext error = %v, want context.Canceled", err)
	}
}

func TestGitService_BlameCacheMetrics(t *testing.T) {
	svc := &GitService{}
	key := blameCacheKey{filePath: "test.go", startLine: 1, endLine: 1}
	hash := sha256.Sum256([]byte("content"))

	if _, ok := svc.cachedBlame(key, hash, "head"); ok {
		t.Fatal("empty blame cache unexpectedly hit")
	}
	svc.storeBlame(key, hash, "head", []BlameLine{{Line: 1, Content: "content"}})
	if _, ok := svc.cachedBlame(key, hash, "head"); !ok {
		t.Fatal("stored blame cache entry missed")
	}
	if hits, misses := svc.blameCacheHits.Load(), svc.blameCacheMisses.Load(); hits != 1 || misses != 1 {
		t.Fatalf("cache metrics hits=%d misses=%d, want 1/1", hits, misses)
	}
}

func TestGitService_BlameCacheConcurrentAccess(t *testing.T) {
	svc := &GitService{}
	key := blameCacheKey{filePath: "concurrent.go", startLine: 1, endLine: 1}
	hash := sha256.Sum256([]byte("content"))
	lines := []BlameLine{{Line: 1, Content: "content"}}
	svc.storeBlame(key, hash, "head", lines)

	var failed atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(writer bool) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if writer {
					svc.storeBlame(key, hash, "head", lines)
					continue
				}
				if _, ok := svc.cachedBlame(key, hash, "head"); !ok {
					failed.Store(true)
				}
			}
		}(i%4 == 0)
	}
	wg.Wait()
	if failed.Load() {
		t.Fatal("concurrent cache reader observed a missing stable entry")
	}
}

func TestGitService_BlameCacheEvictsLeastRecentlyUsed(t *testing.T) {
	svc := &GitService{}
	hash := sha256.Sum256([]byte("content"))
	lines := []BlameLine{{Line: 1, Content: "content"}}
	keys := make([]blameCacheKey, maxBlameCacheEntries)
	for i := range keys {
		keys[i] = blameCacheKey{filePath: fmt.Sprintf("file-%03d.go", i), startLine: 1, endLine: 1}
		svc.storeBlame(keys[i], hash, "head", lines)
	}
	if _, ok := svc.cachedBlame(keys[0], hash, "head"); !ok {
		t.Fatal("recently touched cache entry missed")
	}
	extra := blameCacheKey{filePath: "extra.go", startLine: 1, endLine: 1}
	svc.storeBlame(extra, hash, "head", lines)
	if _, ok := svc.cachedBlame(keys[0], hash, "head"); !ok {
		t.Fatal("recently used entry was evicted")
	}
	if _, ok := svc.cachedBlame(keys[1], hash, "head"); ok {
		t.Fatal("least recently used entry was not evicted")
	}
}

func initBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git.PlainInit failed: %v", err)
	}
	return dir
}

func configureRepoIdentity(t *testing.T, dir string) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("git.PlainOpen failed: %v", err)
	}
	config, err := repo.Config()
	if err != nil {
		t.Fatalf("read repository config: %v", err)
	}
	config.User.Name = "Test User"
	config.User.Email = "test@test.com"
	if err := repo.SetConfig(config); err != nil {
		t.Fatalf("write repository config: %v", err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	_ = wt.AddGlob(".")
	hash, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_ = hash
}

func TestGitService_Status_emptyRepo(t *testing.T) {
	dir := initBareRepo(t)
	svc := &GitService{}
	changes, err := svc.GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("expected 0 changes in fresh repo, got %d", len(changes))
	}
}

func TestGitService_Status_detectsNewFile(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	svc := &GitService{}
	changes, err := svc.GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Path != "a.txt" {
		t.Errorf("expected path 'a.txt', got %q", changes[0].Path)
	}
	if changes[0].Status != "Untracked" {
		t.Errorf("expected status 'Untracked', got %q", changes[0].Status)
	}
	if changes[0].Staged {
		t.Error("untracked file must not be marked staged")
	}
}

func TestGitService_Status_detectsModifiedFile(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	commitAll(t, dir, "initial")
	writeFile(t, dir, "a.txt", "hello world")
	svc := &GitService{}
	changes, err := svc.GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Status != "Modified" {
		t.Errorf("expected status 'Modified', got %q", changes[0].Status)
	}
}

func TestGitService_Status_emitsStagedAndUnstagedRowsSeparately(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	commitAll(t, dir, "initial")
	writeFile(t, dir, "a.txt", "staged")
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := wt.Add("a.txt"); err != nil {
		t.Fatalf("stage: %v", err)
	}
	writeFile(t, dir, "a.txt", "unstaged")
	svc := &GitService{}
	changes, err := svc.GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	var staged, unstaged bool
	for _, change := range changes {
		if change.Path != "a.txt" {
			continue
		}
		if change.Staged {
			staged = true
		} else {
			unstaged = true
		}
	}
	if !staged || !unstaged {
		t.Fatalf("want both staged and unstaged rows, got %+v", changes)
	}
}

func TestGitService_Status_detectsDeletedFile(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	commitAll(t, dir, "initial")
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	svc := &GitService{}
	changes, err := svc.GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Status != "Deleted" {
		t.Errorf("expected status 'Deleted', got %q", changes[0].Status)
	}
}

// stageRemoval stages the deletion of name (worktree file must still exist).
func stageRemoval(t *testing.T, dir, name string) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := wt.Remove(name); err != nil {
		t.Fatalf("stage removal of %s: %v", name, err)
	}
}

func TestGitService_Status_projectsProvenStagedRename(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "old.txt", "same bytes")
	commitAll(t, dir, "initial")
	stageRemoval(t, dir, "old.txt")
	writeFile(t, dir, "new.txt", "same bytes")
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := wt.Add("new.txt"); err != nil {
		t.Fatalf("stage new.txt: %v", err)
	}
	svc := &GitService{}
	changes, err := svc.GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected the delete+add pair to merge into one row, got %+v", changes)
	}
	row := changes[0]
	if row.Status != "Renamed" || !row.Staged {
		t.Fatalf("expected staged Renamed row, got %+v", row)
	}
	if row.Path != "new.txt" || row.OldPath != "old.txt" {
		t.Fatalf("expected rename new.txt (oldPath old.txt), got %+v", row)
	}
}

func TestGitService_Status_keepsUnprovenDeleteAddPair(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "old.txt", "original content")
	commitAll(t, dir, "initial")
	stageRemoval(t, dir, "old.txt")
	writeFile(t, dir, "new.txt", "different content")
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := wt.Add("new.txt"); err != nil {
		t.Fatalf("stage new.txt: %v", err)
	}
	svc := &GitService{}
	changes, err := svc.GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("content-differing delete+add must stay two rows, got %+v", changes)
	}
	for _, row := range changes {
		if row.Status == "Renamed" || row.OldPath != "" {
			t.Fatalf("rename must not be guessed when blobs differ: %+v", changes)
		}
	}
}

func TestGitService_GetDiffForSide_returnsRowIdentitySide(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "v1\n")
	commitAll(t, dir, "initial")
	writeFile(t, dir, "a.txt", "v2\n")
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := wt.Add("a.txt"); err != nil {
		t.Fatalf("stage v2: %v", err)
	}
	writeFile(t, dir, "a.txt", "v3\n")
	svc := &GitService{}
	stagedDiff, err := svc.GetDiffForSide(dir, "a.txt", true)
	if err != nil {
		t.Fatalf("staged side failed: %v", err)
	}
	if !strings.Contains(stagedDiff, "+v2") || strings.Contains(stagedDiff, "+v3") {
		t.Fatalf("staged side must diff HEAD vs index, got:\n%s", stagedDiff)
	}
	unstagedDiff, err := svc.GetDiffForSide(dir, "a.txt", false)
	if err != nil {
		t.Fatalf("unstaged side failed: %v", err)
	}
	if !strings.Contains(unstagedDiff, "+v3") || strings.Contains(unstagedDiff, "+v2") {
		t.Fatalf("unstaged side must diff index vs worktree, got:\n%s", unstagedDiff)
	}
}

func TestGitService_GetDiffForSide_untrackedFallsBackToAllAdditions(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "u.txt", "hello")
	svc := &GitService{}
	diff, err := svc.GetDiffForSide(dir, "u.txt", false)
	if err != nil {
		t.Fatalf("untracked side failed: %v", err)
	}
	if !strings.Contains(diff, "new file mode") || !strings.Contains(diff, "+hello") {
		t.Fatalf("untracked side must return all-additions diff, got:\n%s", diff)
	}
}

func TestGitService_BranchInfo(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	commitAll(t, dir, "initial")
	svc := &GitService{}
	info, err := svc.GetBranchInfo(dir)
	if err != nil {
		t.Fatalf("GetBranchInfo failed: %v", err)
	}
	if info.Name == "" {
		t.Error("expected non-empty branch name")
	}
	if info.Ahead != 0 {
		t.Errorf("expected ahead 0, got %d", info.Ahead)
	}
	if info.Behind != 0 {
		t.Errorf("expected behind 0, got %d", info.Behind)
	}
}

func TestGitService_BranchInfo_notARepo(t *testing.T) {
	dir := t.TempDir()
	svc := &GitService{}
	_, err := svc.GetBranchInfo(dir)
	if err == nil {
		t.Error("expected error for non-repo directory")
	}
}

func TestGitService_Stage(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	svc := &GitService{}
	if err := svc.Stage(dir, "a.txt"); err != nil {
		t.Fatalf("Stage failed: %v", err)
	}
	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()
	st, _ := wt.Status()
	if st.IsUntracked("a.txt") {
		t.Error("expected file to be staged, but still untracked")
	}
}

func TestGitService_ConcurrentStageSerializesIndexWrites(t *testing.T) {
	dir := initBareRepo(t)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeFile(t, dir, name, name)
	}

	svc := &GitService{}
	holdRelease, err := svc.acquireRepoMutation(dir)
	if err != nil {
		t.Fatalf("acquire mutation gate: %v", err)
	}
	t.Cleanup(holdRelease)

	stageDone := make(chan error, 1)
	go func() {
		stageDone <- svc.Stage(filepath.Join(dir, "."), "a.txt")
	}()

	key, err := canonicalRepoMutationKey(dir)
	if err != nil {
		t.Fatalf("canonical mutation key: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		svc.repoMutationMu.Lock()
		gate := svc.repoMutationGates[key]
		users := 0
		if gate != nil {
			users = gate.users
		}
		svc.repoMutationMu.Unlock()
		if users == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Stage did not join the repository mutation gate")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-stageDone:
		t.Fatalf("Stage completed while the repository mutation gate was held: %v", err)
	default:
	}

	holdRelease()
	if err := <-stageDone; err != nil {
		t.Fatalf("Stage after gate release: %v", err)
	}

	readHoldRelease, err := svc.acquireRepoMutation(dir)
	if err != nil {
		t.Fatalf("reacquire mutation gate: %v", err)
	}
	t.Cleanup(readHoldRelease)
	statusDone := make(chan error, 1)
	go func() {
		_, err := svc.GetStatus(dir)
		statusDone <- err
	}()
	deadline = time.Now().Add(5 * time.Second)
	for {
		svc.repoMutationMu.Lock()
		gate := svc.repoMutationGates[key]
		users := 0
		if gate != nil {
			users = gate.users
		}
		svc.repoMutationMu.Unlock()
		if users == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("GetStatus did not join the repository operation gate")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-statusDone:
		t.Fatalf("GetStatus completed while the repository operation gate was held: %v", err)
	default:
	}
	readHoldRelease()
	if err := <-statusDone; err != nil {
		t.Fatalf("GetStatus after gate release: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, name := range []string{"b.txt", "c.txt"} {
		name := name
		go func() {
			<-start
			errs <- svc.Stage(dir, name)
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Stage: %v", err)
		}
	}

	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	idx, err := repo.Storer.Index()
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	staged := make(map[string]bool, len(idx.Entries))
	for _, entry := range idx.Entries {
		staged[entry.Name] = true
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if !staged[name] {
			t.Errorf("concurrent Stage lost index entry %q", name)
		}
	}
}

func TestCanonicalRepoMutationKeyUsesLinkedWorktreeCommonDir(t *testing.T) {
	mainWorktree := t.TempDir()
	commonDir := filepath.Join(mainWorktree, ".git")
	linkedGitDir := filepath.Join(commonDir, "worktrees", "linked")
	if err := os.MkdirAll(linkedGitDir, 0o755); err != nil {
		t.Fatalf("create linked git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(linkedGitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatalf("write commondir: %v", err)
	}

	linkedWorktree := t.TempDir()
	gitDirPointer := "gitdir: " + linkedGitDir + "\n"
	if err := os.WriteFile(filepath.Join(linkedWorktree, ".git"), []byte(gitDirPointer), 0o644); err != nil {
		t.Fatalf("write linked worktree gitdir: %v", err)
	}

	mainKey, err := canonicalRepoMutationKey(mainWorktree)
	if err != nil {
		t.Fatalf("main key: %v", err)
	}
	linkedKey, err := canonicalRepoMutationKey(linkedWorktree)
	if err != nil {
		t.Fatalf("linked key: %v", err)
	}
	if linkedKey != mainKey {
		t.Fatalf("linked worktree key = %q, want common-dir key %q", linkedKey, mainKey)
	}
}

func TestCanonicalRepoMutationKeyStableAcrossRepositoryInitialization(t *testing.T) {
	worktree := t.TempDir()
	before, err := canonicalRepoMutationKey(worktree)
	if err != nil {
		t.Fatalf("key before initialization: %v", err)
	}
	if err := os.Mkdir(filepath.Join(worktree, ".git"), 0o755); err != nil {
		t.Fatalf("create .git directory: %v", err)
	}
	after, err := canonicalRepoMutationKey(worktree)
	if err != nil {
		t.Fatalf("key after initialization: %v", err)
	}
	if after != before {
		t.Fatalf("repository key changed during initialization: before=%q after=%q", before, after)
	}
}

func TestGitService_Unstage(t *testing.T) {
	dir := initBareRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	svc := &GitService{}
	_ = svc.Stage(dir, "a.txt")
	if err := svc.Unstage(dir, "a.txt"); err != nil {
		t.Fatalf("Unstage failed: %v", err)
	}
	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()
	st, _ := wt.Status()
	if !st.IsUntracked("a.txt") {
		t.Error("expected file to be untracked after unstage")
	}
}

func TestGitService_Commit(t *testing.T) {
	dir := initBareRepo(t)
	configureRepoIdentity(t, dir)
	writeFile(t, dir, "a.txt", "hello")
	svc := &GitService{}
	if err := svc.Stage(dir, "a.txt"); err != nil {
		t.Fatalf("Stage failed: %v", err)
	}
	if err := svc.Commit(dir, "test commit"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	// After commit, working tree should be clean
	changes, err := svc.GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("expected 0 changes after commit, got %d", len(changes))
	}
}

func TestGitService_Commit_nothingStaged(t *testing.T) {
	dir := initBareRepo(t)
	svc := &GitService{}
	err := svc.Commit(dir, "empty commit")
	if err == nil {
		t.Error("expected error when committing with nothing staged")
	}
}

func TestGitService_GetDiff_ModifiedFile(t *testing.T) {
	repoDir := t.TempDir()
	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit failed: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree failed: %v", err)
	}

	initialContent := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte(initialContent), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("main.go"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("initial", &git.CommitOptions{Author: &testAuthor}); err != nil {
		t.Fatal(err)
	}

	modifiedContent := "package main\n\nfunc main() {\n    println(\"hello\")\n}\n"
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte(modifiedContent), 0644); err != nil {
		t.Fatal(err)
	}

	svc := &GitService{}
	diff, err := svc.GetDiff(repoDir, "main.go")
	if err != nil {
		t.Fatalf("GetDiff failed: %v", err)
	}
	if diff == "" {
		t.Fatal("expected non-empty diff")
	}
	if !strings.Contains(diff, "println") {
		t.Errorf("diff should contain 'println', got: %s", diff)
	}
}

func TestGitService_GetDiff_UntrackedFile(t *testing.T) {
	repoDir := t.TempDir()
	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit failed: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree failed: %v", err)
	}
	os.WriteFile(filepath.Join(repoDir, "new.go"), []byte("package main\n"), 0644)
	wt.Add("new.go")

	svc := &GitService{}
	diff, err := svc.GetDiff(repoDir, "new.go")
	if err != nil {
		t.Fatalf("GetDiff failed: %v", err)
	}
	if diff == "" {
		t.Fatal("expected non-empty diff for staged new file")
	}
}

func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := initBareRepo(t)
	writeFile(t, dir, "README.md", "initial`n")
	commitAll(t, dir, "initial commit")
	return dir
}

func initBareRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatalf("git.PlainInit bare remote failed: %v", err)
	}
	return dir
}

func addRemote(t *testing.T, repoPath, name, url string) {
	t.Helper()
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("git.PlainOpen failed: %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: name, URLs: []string{url}}); err != nil {
		t.Fatalf("create remote %s failed: %v", name, err)
	}
}

func currentBranchName(t *testing.T, repoPath string) string {
	t.Helper()
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("git.PlainOpen failed: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("read HEAD failed: %v", err)
	}
	return head.Name().Short()
}

func setBranchTrackingRemote(t *testing.T, repoPath, branchName, remoteName, remoteBranchName string) {
	t.Helper()
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("git.PlainOpen failed: %v", err)
	}
	repoConfig, err := repo.Config()
	if err != nil {
		t.Fatalf("read repository config failed: %v", err)
	}
	repoConfig.Branches[branchName] = &config.Branch{
		Name:   branchName,
		Remote: remoteName,
		Merge:  plumbing.NewBranchReferenceName(remoteBranchName),
	}
	if err := repo.SetConfig(repoConfig); err != nil {
		t.Fatalf("set branch tracking config failed: %v", err)
	}
}

func branchHash(t *testing.T, repoPath, branchName string) (plumbing.Hash, bool) {
	t.Helper()
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("git.PlainOpen failed: %v", err)
	}
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return plumbing.ZeroHash, false
	}
	if err != nil {
		t.Fatalf("read branch %s failed: %v", branchName, err)
	}
	return ref.Hash(), true
}

func cloneBranch(t *testing.T, remotePath, branchName string) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := git.PlainClone(dir, false, &git.CloneOptions{
		URL:           remotePath,
		ReferenceName: plumbing.NewBranchReferenceName(branchName),
		SingleBranch:  true,
	}); err != nil {
		t.Fatalf("clone %s failed: %v", remotePath, err)
	}
	return dir
}

func TestGitService_Unit8_PushUsesSelectedRemote(t *testing.T) {
	dir := setupTestRepo(t)
	branchName := currentBranchName(t, dir)
	const upstreamBranch = "unit8-upstream"
	originPath := initBareRemote(t)
	upstreamPath := initBareRemote(t)
	addRemote(t, dir, "origin", originPath)
	addRemote(t, dir, "upstream", upstreamPath)

	svc := &GitService{}
	if err := svc.Push(dir, ""); err != nil {
		t.Fatalf("Push with empty remote failed: %v", err)
	}
	originHash, ok := branchHash(t, originPath, branchName)
	if !ok {
		t.Fatal("Push with empty remote did not push to origin")
	}

	setBranchTrackingRemote(t, dir, branchName, "upstream", upstreamBranch)
	writeFile(t, dir, "upstream-only.txt", "pushed only to upstream\n")
	commitAll(t, dir, "upstream-only change")
	localHash, ok := branchHash(t, dir, branchName)
	if !ok {
		t.Fatalf("local branch %s not found", branchName)
	}
	if err := svc.Push(dir, "upstream"); err != nil {
		t.Fatalf("Push with upstream failed: %v", err)
	}
	upstreamHash, ok := branchHash(t, upstreamPath, upstreamBranch)
	if !ok || upstreamHash != localHash {
		t.Fatalf("upstream branch hash = %s (exists=%t), want local HEAD %s", upstreamHash, ok, localHash)
	}
	if got, ok := branchHash(t, originPath, branchName); !ok || got != originHash {
		t.Fatalf("origin branch changed during upstream push: got %s (exists=%t), want %s", got, ok, originHash)
	}
	if _, ok := branchHash(t, upstreamPath, branchName); ok {
		t.Fatalf("Push ignored configured upstream branch %q and created %q", upstreamBranch, branchName)
	}
}

func TestGitService_Unit8_PullEmptyUsesTrackingRemote(t *testing.T) {
	seedPath := setupTestRepo(t)
	branchName := currentBranchName(t, seedPath)
	const upstreamBranch = "unit8-tracking"
	originPath := initBareRemote(t)
	upstreamPath := initBareRemote(t)
	addRemote(t, seedPath, "origin", originPath)
	addRemote(t, seedPath, "upstream", upstreamPath)

	svc := &GitService{}
	if err := svc.Push(seedPath, "origin"); err != nil {
		t.Fatalf("seed origin push failed: %v", err)
	}
	setBranchTrackingRemote(t, seedPath, branchName, "upstream", upstreamBranch)
	if err := svc.Push(seedPath, "upstream"); err != nil {
		t.Fatalf("seed upstream push failed: %v", err)
	}

	trackingPullRepo := cloneBranch(t, originPath, branchName)
	addRemote(t, trackingPullRepo, "upstream", upstreamPath)
	setBranchTrackingRemote(t, trackingPullRepo, branchName, "upstream", upstreamBranch)

	upstreamWorktree := cloneBranch(t, upstreamPath, upstreamBranch)
	writeFile(t, upstreamWorktree, "upstream.txt", "from upstream\n")
	commitAll(t, upstreamWorktree, "upstream change")
	upstreamRepo, err := git.PlainOpen(upstreamWorktree)
	if err != nil {
		t.Fatalf("open upstream worktree failed: %v", err)
	}
	if err := upstreamRepo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + upstreamBranch + ":refs/heads/" + upstreamBranch)},
	}); err != nil {
		t.Fatalf("push upstream change failed: %v", err)
	}

	originWorktree := cloneBranch(t, originPath, branchName)
	writeFile(t, originWorktree, "origin.txt", "from origin\n")
	commitAll(t, originWorktree, "origin change")
	originRepo, err := git.PlainOpen(originWorktree)
	if err != nil {
		t.Fatalf("open origin worktree failed: %v", err)
	}
	if err := originRepo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + branchName + ":refs/heads/" + branchName)},
	}); err != nil {
		t.Fatalf("push origin change failed: %v", err)
	}

	if err := svc.Pull(trackingPullRepo, ""); err != nil {
		t.Fatalf("tracking upstream Pull failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(trackingPullRepo, "upstream.txt")); err != nil {
		t.Fatalf("tracking upstream Pull did not update worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(trackingPullRepo, "origin.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tracking upstream Pull unexpectedly used origin: %v", err)
	}
}

func TestGitService_Unit8_PullEmptyFallsBackToOriginWithoutTracking(t *testing.T) {
	dir := setupTestRepo(t)
	branchName := currentBranchName(t, dir)
	originPath := initBareRemote(t)
	addRemote(t, dir, "origin", originPath)

	svc := &GitService{}
	if err := svc.Push(dir, ""); err != nil {
		t.Fatalf("seed origin push failed: %v", err)
	}
	originWorktree := cloneBranch(t, originPath, branchName)
	writeFile(t, originWorktree, "fallback.txt", "from origin fallback\n")
	commitAll(t, originWorktree, "origin fallback change")
	originRepo, err := git.PlainOpen(originWorktree)
	if err != nil {
		t.Fatalf("open origin worktree failed: %v", err)
	}
	if err := originRepo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + branchName + ":refs/heads/" + branchName)},
	}); err != nil {
		t.Fatalf("push origin fallback change failed: %v", err)
	}

	if err := svc.Pull(dir, ""); err != nil {
		t.Fatalf("origin fallback Pull failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fallback.txt")); err != nil {
		t.Fatalf("origin fallback Pull did not update worktree: %v", err)
	}
}

func TestGitService_Unit8_RemoteNotFoundError(t *testing.T) {
	dir := setupTestRepo(t)
	svc := &GitService{}

	if err := svc.Push(dir, "missing"); err == nil || err.Error() != "remote missing not found" {
		t.Fatalf("Push missing remote error = %v, want %q", err, "remote missing not found")
	}
	if err := svc.Pull(dir, "missing"); err == nil || err.Error() != "remote missing not found" {
		t.Fatalf("Pull missing remote error = %v, want %q", err, "remote missing not found")
	}

	branchName := currentBranchName(t, dir)
	setBranchTrackingRemote(t, dir, branchName, "missing", branchName)
	if err := svc.Pull(dir, ""); err == nil || err.Error() != "remote missing not found" {
		t.Fatalf("Pull missing tracking remote error = %v, want %q", err, "remote missing not found")
	}
}

func TestGitService_ListBranches(t *testing.T) {
	repoPath := setupTestRepo(t)
	svc := &GitService{}

	branches, err := svc.ListBranches(repoPath)
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
	}
	if len(branches) == 0 {
		t.Fatal("expected at least one branch (default)")
	}
	found := false
	for _, b := range branches {
		if b.Name == "main" || b.Name == "master" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected main/master branch, got %v", branches)
	}
}

func TestGitService_CreateAndCheckoutBranch(t *testing.T) {
	repoPath := setupTestRepo(t)
	svc := &GitService{}

	err := svc.CreateBranch(repoPath, "feature-1")
	if err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	branches, _ := svc.ListBranches(repoPath)
	found := false
	for _, b := range branches {
		if b.Name == "feature-1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("feature-1 not found after creation")
	}

	err = svc.CheckoutBranch(repoPath, "feature-1")
	if err != nil {
		t.Fatalf("CheckoutBranch failed: %v", err)
	}

	info, err := svc.GetBranchInfo(repoPath)
	if err != nil {
		t.Fatalf("GetBranchInfo failed: %v", err)
	}
	if info.Name != "feature-1" {
		t.Fatalf("expected current branch 'feature-1', got '%s'", info.Name)
	}
}

func TestGitService_DeleteBranch(t *testing.T) {
	repoPath := setupTestRepo(t)
	svc := &GitService{}

	_ = svc.CreateBranch(repoPath, "temp-branch")
	_ = svc.CheckoutBranch(repoPath, "temp-branch")
	_ = svc.CreateBranch(repoPath, "keeper")
	_ = svc.CheckoutBranch(repoPath, "keeper")

	err := svc.DeleteBranch(repoPath, "temp-branch")
	if err != nil {
		t.Fatalf("DeleteBranch failed: %v", err)
	}

	branches, _ := svc.ListBranches(repoPath)
	for _, b := range branches {
		if b.Name == "temp-branch" {
			t.Fatal("temp-branch still exists after delete")
		}
	}
}

func TestGitService_DeleteCurrentBranch_Fails(t *testing.T) {
	repoPath := setupTestRepo(t)
	svc := &GitService{}

	_ = svc.CreateBranch(repoPath, "doomed")
	_ = svc.CheckoutBranch(repoPath, "doomed")

	err := svc.DeleteBranch(repoPath, "doomed")
	if err == nil {
		t.Fatal("expected error deleting current branch, got nil")
	}
}

func TestGitService_GetFullDiff_emptyRepo(t *testing.T) {
	dir := initBareRepo(t)
	svc := &GitService{}
	diff, err := svc.GetFullDiff(dir)
	if err != nil {
		t.Fatalf("GetFullDiff failed: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff for repo with no changes, got: %q", diff)
	}
}

func TestGitService_GetFullDiff_multipleChanges(t *testing.T) {
	repoDir := t.TempDir()
	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit failed: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree failed: %v", err)
	}

	// Commit an initial file.
	writeFile(t, repoDir, "a.txt", "initial a\n")
	writeFile(t, repoDir, "b.txt", "initial b\n")
	if _, err := wt.Add("a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("initial", &git.CommitOptions{Author: &testAuthor}); err != nil {
		t.Fatal(err)
	}

	// Modify a.txt, add new c.txt, leave b.txt unchanged.
	writeFile(t, repoDir, "a.txt", "modified a\n")
	writeFile(t, repoDir, "c.txt", "new file c\n")

	svc := &GitService{}
	diff, err := svc.GetFullDiff(repoDir)
	if err != nil {
		t.Fatalf("GetFullDiff failed: %v", err)
	}
	if diff == "" {
		t.Fatal("expected non-empty diff with 2 changed files")
	}
	// Should contain headers for a.txt and c.txt
	if !strings.Contains(diff, "=== a.txt ===") {
		t.Errorf("diff should contain '=== a.txt ===' header, got: %s", diff)
	}
	if !strings.Contains(diff, "=== c.txt ===") {
		t.Errorf("diff should contain '=== c.txt ===' header, got: %s", diff)
	}
	// Should NOT contain b.txt (unchanged)
	if strings.Contains(diff, "=== b.txt ===") {
		t.Errorf("diff should not contain unchanged file b.txt")
	}
	// Should contain the modified content
	if !strings.Contains(diff, "modified a") {
		t.Errorf("diff should contain 'modified a'")
	}
	if !strings.Contains(diff, "new file c") {
		t.Errorf("diff should contain 'new file c'")
	}
}

func TestGitService_GetFullDiff_notARepo(t *testing.T) {
	dir := t.TempDir()
	svc := &GitService{}
	// BUG1: GetFullDiff now gracefully degrades for non-repo directories,
	// returning an empty diff with no error so the AI code review feature
	// does not crash with "repository does not exist".
	diff, err := svc.GetFullDiff(dir)
	if err != nil {
		t.Fatalf("expected nil error for non-repo directory, got %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff for non-repo directory, got %q", diff)
	}
}

// N-67: GitService workspace sandbox — when SetWorkspaceRoot is set, all
// operations must reject paths outside the workspace. This prevents the
// frontend from operating on git repos outside the open project.
func TestGitService_N67_SetWorkspaceRoot_RejectsOutsidePath(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	// Initialize a git repo OUTSIDE the workspace.
	repoDir := initBareRepo(t)
	// Move it outside — actually, initBareRepo already created it in a temp
	// dir. We just need a dir that's outside the workspace. Let's use the
	// parent of the workspace.
	_ = outside
	_ = repoDir

	svc := &GitService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	// Create a repo outside the workspace.
	outsideRepo := t.TempDir()
	_, err := git.PlainInit(outsideRepo, false)
	if err != nil {
		t.Fatalf("git.PlainInit failed: %v", err)
	}
	// All GitService operations on the outside repo should be rejected.
	t.Run("GetStatus", func(t *testing.T) {
		_, err := svc.GetStatus(outsideRepo)
		if err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
	t.Run("GetBranchInfo", func(t *testing.T) {
		_, err := svc.GetBranchInfo(outsideRepo)
		if err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
	t.Run("ListBranches", func(t *testing.T) {
		_, err := svc.ListBranches(outsideRepo)
		if err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
	t.Run("Stage", func(t *testing.T) {
		err := svc.Stage(outsideRepo, "file.txt")
		if err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
	t.Run("Commit", func(t *testing.T) {
		err := svc.Commit(outsideRepo, "msg")
		if err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
	t.Run("Push", func(t *testing.T) {
		err := svc.Push(outsideRepo, "")
		if err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
	t.Run("Pull", func(t *testing.T) {
		err := svc.Pull(outsideRepo, "")
		if err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
	t.Run("GetFullDiff", func(t *testing.T) {
		_, err := svc.GetFullDiff(outsideRepo)
		if err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
}

// N-67: when SetWorkspaceRoot is set, operations on paths INSIDE the
// workspace should still work.
func TestGitService_N67_SetWorkspaceRoot_AllowsInsidePath(t *testing.T) {
	workspace := t.TempDir()
	// Init a git repo inside the workspace.
	repoDir := filepath.Join(workspace, "myrepo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	_, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("git.PlainInit failed: %v", err)
	}
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	// GetStatus on the inside repo should work.
	_, err = svc.GetStatus(repoDir)
	if err != nil {
		t.Errorf("expected success for path inside workspace, got: %v", err)
	}
}

// N-67 / Proposal AJ: additional methods that call validatePath but were
// not covered by TestGitService_N67_SetWorkspaceRoot_RejectsOutsidePath.
// Each subtest verifies the method rejects a repo path outside the workspace.
func TestGitService_N67_SetWorkspaceRoot_RejectsOutsidePath_AdditionalMethods(t *testing.T) {
	workspace := t.TempDir()
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	// Create a valid git repo outside the workspace.
	outsideRepo := t.TempDir()
	if _, err := git.PlainInit(outsideRepo, false); err != nil {
		t.Fatalf("git.PlainInit failed: %v", err)
	}
	// Stage a file so later operations have something to work with.
	writeFile(t, outsideRepo, "file.txt", "content")
	repo, err := git.PlainOpen(outsideRepo)
	if err != nil {
		t.Fatalf("PlainOpen failed: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree failed: %v", err)
	}
	_ = wt.AddGlob(".")
	_, _ = wt.Commit("init", &git.CommitOptions{Author: &testAuthor})

	t.Run("CreateBranch", func(t *testing.T) {
		if err := svc.CreateBranch(outsideRepo, "feature"); err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
	t.Run("CheckoutBranch", func(t *testing.T) {
		if err := svc.CheckoutBranch(outsideRepo, "main"); err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
	t.Run("DeleteBranch", func(t *testing.T) {
		if err := svc.DeleteBranch(outsideRepo, "feature"); err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
	t.Run("Unstage", func(t *testing.T) {
		if err := svc.Unstage(outsideRepo, "file.txt"); err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
	t.Run("GetDiff", func(t *testing.T) {
		if _, err := svc.GetDiff(outsideRepo, "file.txt"); err == nil {
			t.Error("expected error for path outside workspace")
		}
	})
}

// G-01: renderer-facing Git operations fail closed until a project is open.
func TestGitService_N67_NoWorkspaceRoot_RejectsAnyPath(t *testing.T) {
	repoDir := initBareRepo(t)
	svc := NewGitService() // no SetWorkspaceRoot call
	_, err := svc.GetStatus(repoDir)
	if err == nil {
		t.Fatal("expected failure without workspace root")
	}
}

// G-01: clearing the coordinated root restores the fail-closed state.
func TestGitService_N67_EmptyWorkspaceRoot_FailsClosed(t *testing.T) {
	workspace := t.TempDir()
	svc := NewGitService()
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatal(err)
	}
	// Clear the coordinated workspace.
	if err := svc.setWorkspaceRoot(""); err != nil {
		t.Fatal(err)
	}
	// Then paths outside an active workspace remain denied.
	repoDir := initBareRepo(t)
	_, err := svc.GetStatus(repoDir)
	if err == nil {
		t.Fatal("expected failure after clearing workspace root")
	}
}

// ---- M-1: runGit minimal env ----

// TestGitService_M1_MinimalEnv (M-1) verifies that runGit does not inherit the
// parent process env. A "secret" env var set in the test process must not
// be visible to the git subprocess. We use `git var GIT_AUTHOR_NAME`,
// which prints the value git would use for the author name — if
// GIT_AUTHOR_NAME is inherited, the secret appears in the output.
func TestGitService_M1_MinimalEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary not found in PATH: %v", err)
	}

	// Set sensitive env vars that should NOT be inherited by runGit.
	t.Setenv("KOYORI_TEST_LEAK", "secret-leaked-value")
	t.Setenv("GIT_AUTHOR_NAME", "LEAKED_SECRET_NAME")
	t.Setenv("GIT_AUTHOR_EMAIL", "leaked@secret.example")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/should-not-leak")

	// Verify the helper returns only whitelisted keys (no leak).
	env := minimalGitEnv()
	for _, kv := range env {
		if strings.Contains(kv, "secret-leaked-value") {
			t.Errorf("minimalGitEnv leaked KOYORI_TEST_LEAK: %s", kv)
		}
		if strings.Contains(kv, "LEAKED_SECRET_NAME") {
			t.Errorf("minimalGitEnv leaked GIT_AUTHOR_NAME: %s", kv)
		}
		if strings.Contains(kv, "leaked@secret.example") {
			t.Errorf("minimalGitEnv leaked GIT_AUTHOR_EMAIL: %s", kv)
		}
		if strings.Contains(kv, "/tmp/should-not-leak") {
			t.Errorf("minimalGitEnv leaked SSH_AUTH_SOCK: %s", kv)
		}
	}

	// Verify expected keys ARE present.
	keys := make(map[string]string)
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			keys[parts[0]] = parts[1]
		}
	}
	if v, ok := keys["GIT_TERMINAL_PROMPT"]; !ok || v != "0" {
		t.Errorf("expected GIT_TERMINAL_PROMPT=0, got %q (ok=%v)", v, ok)
	}
	if _, ok := keys["PATH"]; !ok {
		t.Error("expected PATH in minimalGitEnv")
	}

	// End-to-end: run `git var GIT_AUTHOR_IDENT` via runGit. GIT_AUTHOR_NAME
	// env (if inherited) overrides -c user.name, so a leak would surface
	// "LEAKED_SECRET_NAME" in the ident. With env stripped, only the -c
	// value (SAFE_NAME) appears. (GIT_AUTHOR_NAME is not a valid `git var`
	// argument, so GIT_AUTHOR_IDENT — the user-facing ident — is used.)
	dir := initBareRepo(t)
	svc := &GitService{}
	out, err := svc.runGit(dir, "-c", "user.name=SAFE_NAME", "-c", "user.email=safe@example.com", "var", "GIT_AUTHOR_IDENT")
	if err != nil {
		t.Fatalf("runGit var GIT_AUTHOR_IDENT: %v", err)
	}
	if strings.Contains(out, "LEAKED_SECRET_NAME") {
		t.Errorf("runGit env leaked GIT_AUTHOR_NAME to git subprocess: %q", out)
	}
	if strings.Contains(out, "leaked@secret.example") {
		t.Errorf("runGit env leaked GIT_AUTHOR_EMAIL to git subprocess: %q", out)
	}
	if !strings.Contains(out, "SAFE_NAME") {
		t.Errorf("expected SAFE_NAME in git ident (env stripped), got %q", out)
	}
}

// ---- M-2: parseGitBlame streaming ----

// TestGitService_M2_ParseGitBlameStreaming (M-2) generates a large fake
// `git blame --line-porcelain` output (with many distinct commit SHAs to
// exercise the bounded commitInfo cache and eviction) and verifies
// parseGitBlameStream handles it correctly without buffering the whole
// output. This confirms no OOM and correct parsing.
func TestGitService_M2_ParseGitBlameStreaming(t *testing.T) {
	// blockCount > recentCommitsLimit (256) to force cache evictions.
	const blockCount = 5000
	const contentPrefix = "line content "

	var sb strings.Builder
	for i := 0; i < blockCount; i++ {
		// 40-char hex SHA — distinct per block (zero-padded decimal).
		sha := fmt.Sprintf("%040d", i)
		fmt.Fprintf(&sb, "%s %d %d 1\n", sha, i+1, i+1)
		fmt.Fprintf(&sb, "author Author-%d\n", i)
		fmt.Fprintf(&sb, "author-mail <%d@example.com>\n", i)
		fmt.Fprintf(&sb, "author-time %d\n", 1700000000+i)
		fmt.Fprintf(&sb, "author-tz +0000\n")
		fmt.Fprintf(&sb, "committer Author-%d\n", i)
		fmt.Fprintf(&sb, "committer-mail <%d@example.com>\n", i)
		fmt.Fprintf(&sb, "committer-time %d\n", 1700000000+i)
		fmt.Fprintf(&sb, "committer-tz +0000\n")
		fmt.Fprintf(&sb, "summary commit %d\n", i)
		fmt.Fprintf(&sb, "filename file.txt\n")
		fmt.Fprintf(&sb, "\t%s%d\n", contentPrefix, i)
	}

	scanner := bufio.NewScanner(strings.NewReader(sb.String()))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	result := parseGitBlameStream(scanner)

	if len(result) != blockCount {
		t.Fatalf("expected %d BlameLines, got %d", blockCount, len(result))
	}

	// Spot-check the first entry.
	first := result[0]
	if first.Line != 1 {
		t.Errorf("first.Line = %d, want 1", first.Line)
	}
	if first.Author != "Author-0" {
		t.Errorf("first.Author = %q, want Author-0", first.Author)
	}
	if first.Email != "0@example.com" {
		t.Errorf("first.Email = %q, want 0@example.com", first.Email)
	}
	if first.Summary != "commit 0" {
		t.Errorf("first.Summary = %q, want 'commit 0'", first.Summary)
	}
	if first.Content != contentPrefix+"0" {
		t.Errorf("first.Content = %q, want %q", first.Content, contentPrefix+"0")
	}
	// Verify the Time field is populated from author-time.
	if first.Time == "" {
		t.Error("first.Time should be populated from author-time, got empty")
	}
	if first.Date != first.Time {
		t.Errorf("first.Date = %q, want Time alias %q", first.Date, first.Time)
	}

	// Spot-check the last entry.
	last := result[blockCount-1]
	if last.Line != blockCount {
		t.Errorf("last.Line = %d, want %d", last.Line, blockCount)
	}
	wantAuthor := fmt.Sprintf("Author-%d", blockCount-1)
	if last.Author != wantAuthor {
		t.Errorf("last.Author = %q, want %q", last.Author, wantAuthor)
	}
}

// TestParseGitBlame_RepeatedCommitMetadataPopulated (M-2)
// When the same commit appears on multiple blame lines, the metadata cache
// is used. This test verifies that the FIRST occurrence of a commit also
// has fully-populated metadata (the previous implementation had a latent
// bug where the first occurrence had empty Author/Email/Time/Summary
// because the BlameLine was emitted before the metadata was parsed).
func TestParseGitBlame_RepeatedCommitMetadataPopulated(t *testing.T) {
	const sha = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	const author = "Jane Doe"
	const email = "jane@example.com"
	const summary = "Initial commit"

	var sb strings.Builder
	// Two blocks with the SAME commit SHA.
	for i := 1; i <= 2; i++ {
		fmt.Fprintf(&sb, "%s %d %d 1\n", sha, i, i)
		fmt.Fprintf(&sb, "author %s\n", author)
		fmt.Fprintf(&sb, "author-mail <%s>\n", email)
		fmt.Fprintf(&sb, "author-time 1700000000\n")
		fmt.Fprintf(&sb, "committer Jane Doe\n")
		fmt.Fprintf(&sb, "committer-mail <%s>\n", email)
		fmt.Fprintf(&sb, "summary %s\n", summary)
		fmt.Fprintf(&sb, "filename file.txt\n")
		fmt.Fprintf(&sb, "\tline %d\n", i)
	}

	result := parseGitBlame(sb.String())
	if len(result) != 2 {
		t.Fatalf("expected 2 BlameLines, got %d", len(result))
	}
	for i, bl := range result {
		if bl.Author != author {
			t.Errorf("BlameLine[%d].Author = %q, want %q (first-occurrence bug fix)", i, bl.Author, author)
		}
		if bl.Email != email {
			t.Errorf("BlameLine[%d].Email = %q, want %q", i, bl.Email, email)
		}
		if bl.Summary != summary {
			t.Errorf("BlameLine[%d].Summary = %q, want %q", i, bl.Summary, summary)
		}
		if bl.Time == "" {
			t.Errorf("BlameLine[%d].Time should be populated", i)
		}
	}
}

// TestGitService_L2_StrconvAtoiReplaced 验证 L-2 修复:自定义 strconvAtoi
// 已替换为标准库 strconv.Atoi。通过 parseGitBlame 解析包含 author-time
// (时间戳整数)和行号的 porcelain 输出,确认整数字段仍被正确解析。
func TestGitService_L2_StrconvAtoiReplaced(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	var sb strings.Builder
	// header: <sha> <orig-line> <final-line> [group]
	fmt.Fprintf(&sb, "%s 42 42 1\n", sha)
	fmt.Fprintf(&sb, "author Test Author\n")
	fmt.Fprintf(&sb, "author-mail <test@example.com>\n")
	fmt.Fprintf(&sb, "author-time 1609459200\n") // 2021-01-01T00:00:00Z
	fmt.Fprintf(&sb, "summary test summary\n")
	fmt.Fprintf(&sb, "filename test.txt\n")
	fmt.Fprintf(&sb, "\tline content\n")

	result := parseGitBlame(sb.String())
	if len(result) != 1 {
		t.Fatalf("expected 1 BlameLine, got %d", len(result))
	}
	bl := result[0]
	if bl.Author != "Test Author" {
		t.Errorf("Author = %q, want %q", bl.Author, "Test Author")
	}
	// author-time 解析依赖 strconv.Atoi;若失败则 Time 为空。
	if bl.Time == "" {
		t.Error("Time should be populated from author-time (strconv.Atoi path)")
	}
	// 行号 42 也通过 strconv.Atoi 解析(parts[2])。
	if bl.Line != 42 {
		t.Errorf("Line = %d, want 42 (strconv.Atoi path)", bl.Line)
	}
}

func TestGitService_B6_GetBlameForRangeAndContentCache(t *testing.T) {
	skipIfNoGit(t)
	dir := initBareRepo(t)
	setLocalGitConfig(t, dir)
	writeFile(t, dir, "range.txt", "one\ntwo\nthree\n")
	commitAll(t, dir, "initial range content")
	writeFile(t, dir, "range.txt", "one\nTWO\nthree\n")
	commitAll(t, dir, "change second line")

	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	blameRuns := 0
	runStream := func(repoPath string, args ...string) (*bufio.Scanner, func() error, error) {
		if len(args) > 0 && args[0] == "blame" {
			blameRuns++
		}
		return svc.runGitStream(repoPath, args...)
	}
	lines, err := svc.getBlameForRange(dir, "range.txt", 2, 3, runStream)
	if err != nil {
		t.Fatalf("GetBlameForRange: %v", err)
	}
	if blameRuns != 1 {
		t.Fatalf("git blame runs = %d, want 1", blameRuns)
	}
	if len(lines) != 2 {
		t.Fatalf("blame line count = %d, want 2", len(lines))
	}
	for i, want := range []struct {
		line    int
		content string
	}{{2, "TWO"}, {3, "three"}} {
		if lines[i].Line != want.line || lines[i].Content != want.content {
			t.Errorf("blame[%d] = %#v, want line %d content %q", i, lines[i], want.line, want.content)
		}
		if lines[i].Commit == "" || lines[i].Author == "" || lines[i].Date == "" {
			t.Errorf("blame[%d] missing required metadata: %#v", i, lines[i])
		}
	}

	// Returned slices are copies: callers cannot mutate the cached value.
	lines[0].Content = "poisoned"
	cached, err := svc.getBlameForRange(dir, "range.txt", 2, 3, runStream)
	if err != nil {
		t.Fatalf("cached GetBlameForRange: %v", err)
	}
	if cached[0].Content != "TWO" {
		t.Fatalf("cached content = %q, want TWO", cached[0].Content)
	}
	if blameRuns != 1 {
		t.Fatalf("cached request reran git blame: runs = %d, want 1", blameRuns)
	}

	// A content change must invalidate the cache and execute blame again.
	writeFile(t, dir, "range.txt", "one\nTWO DIRTY\nthree\n")
	refreshed, err := svc.getBlameForRange(dir, "range.txt", 2, 3, runStream)
	if err != nil {
		t.Fatalf("GetBlameForRange after edit: %v", err)
	}
	if len(refreshed) != 2 || refreshed[0].Content != "TWO DIRTY" {
		t.Fatalf("refreshed blame = %#v, want edited second line", refreshed)
	}
	if blameRuns != 2 {
		t.Fatalf("content change git blame runs = %d, want 2", blameRuns)
	}
}

func TestGitService_B6_GetBlameForRangeInvalidatesCacheWhenHEADChanges(t *testing.T) {
	skipIfNoGit(t)
	dir := initBareRepo(t)
	const fileName = "history.txt"
	commitAs := func(message, author string) {
		t.Helper()
		repo, err := git.PlainOpen(dir)
		if err != nil {
			t.Fatalf("PlainOpen: %v", err)
		}
		wt, err := repo.Worktree()
		if err != nil {
			t.Fatalf("Worktree: %v", err)
		}
		if _, err := wt.Add(fileName); err != nil {
			t.Fatalf("Add: %v", err)
		}
		if _, err := wt.Commit(message, &git.CommitOptions{Author: &object.Signature{
			Name: author, Email: strings.ToLower(author) + "@example.com", When: time.Now().UTC(),
		}}); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}

	writeFile(t, dir, fileName, "same bytes\n")
	commitAs("alice version", "Alice")
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	first, err := svc.GetBlameForRange(dir, fileName, 1, 1)
	if err != nil {
		t.Fatalf("initial GetBlameForRange: %v", err)
	}
	if len(first) != 1 || first[0].Author != "Alice" {
		t.Fatalf("initial blame = %#v, want Alice", first)
	}

	writeFile(t, dir, fileName, "temporary bytes\n")
	commitAs("temporary version", "Carol")
	writeFile(t, dir, fileName, "same bytes\n")
	commitAs("bob restores original bytes", "Bob")

	second, err := svc.GetBlameForRange(dir, fileName, 1, 1)
	if err != nil {
		t.Fatalf("GetBlameForRange after HEAD change: %v", err)
	}
	if len(second) != 1 || second[0].Author != "Bob" {
		t.Fatalf("refreshed blame = %#v, want Bob after identical bytes were restored", second)
	}
	if second[0].Commit == first[0].Commit {
		t.Fatalf("blame commit stayed %q after HEAD/history change", second[0].Commit)
	}
}

func TestGitService_B6_GetBlameForRangeRejectsInvalidInput(t *testing.T) {
	dir := t.TempDir()
	svc := &GitService{}
	for _, tc := range []struct {
		name       string
		file       string
		start, end int
	}{
		{name: "zero start", file: "a.txt", start: 0, end: 1},
		{name: "reversed", file: "a.txt", start: 2, end: 1},
		{name: "too large", file: "a.txt", start: 1, end: maxBlameRange + 1},
		{name: "path traversal", file: "../a.txt", start: 1, end: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.GetBlameForRange(dir, tc.file, tc.start, tc.end); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 优先级 3: Git Stash / Tag / Amend 测试
// ---------------------------------------------------------------------------

// skipIfNoGit 在 git CLI 不可用时跳过测试。
func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary not found in PATH: %v", err)
	}
}

// setLocalGitConfig 在测试仓库中设置本地 user.name / user.email，使
// `git stash`、`git commit --amend`、`git tag -a` 等 CLI 命令能正常工作
// （这些命令会创建提交，需要作者身份信息）。
func setLocalGitConfig(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@test.com"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v: %s", args, err, out)
		}
	}
}

// setupP3TestRepo 创建一个带初始提交的测试仓库并设置本地 git 配置，
// 然后将其设为 workspace root。返回仓库路径和已配置好的 GitService。
// 用于 P3 stash/tag/amend 测试。
func setupP3TestRepo(t *testing.T) (string, *GitService) {
	t.Helper()
	skipIfNoGit(t)
	dir := setupTestRepo(t)
	setLocalGitConfig(t, dir)
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	return dir, svc
}

func TestGitService_P3_StashList(t *testing.T) {
	dir, svc := setupP3TestRepo(t)

	// 新仓库应无 stash。
	stashes, err := svc.StashList(dir)
	if err != nil {
		t.Fatalf("StashList failed: %v", err)
	}
	if len(stashes) != 0 {
		t.Errorf("expected 0 stashes on fresh repo, got %d", len(stashes))
	}

	// 修改已跟踪文件并 stash。
	writeFile(t, dir, "README.md", "modified content")
	if err := svc.StashPush("test stash"); err != nil {
		t.Fatalf("StashPush failed: %v", err)
	}
	stashes, err = svc.StashList(dir)
	if err != nil {
		t.Fatalf("StashList after push failed: %v", err)
	}
	if len(stashes) != 1 {
		t.Fatalf("expected 1 stash, got %d", len(stashes))
	}
	if stashes[0].Ref != "stash@{0}" {
		t.Errorf("expected ref 'stash@{0}', got %q", stashes[0].Ref)
	}
	if stashes[0].CommitHash == "" {
		t.Error("expected non-empty commit hash")
	}
	if !strings.Contains(stashes[0].Message, "test stash") {
		t.Errorf("expected message to contain 'test stash', got %q", stashes[0].Message)
	}
}

func TestGitService_B5_StashLifecycle(t *testing.T) {
	dir, svc := setupP3TestRepo(t)
	baseline, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	const modified = "stash lifecycle content\n"
	writeFile(t, dir, "README.md", modified)

	if err := svc.StashCreate(dir, "lifecycle | message"); err != nil {
		t.Fatalf("StashCreate: %v", err)
	}
	afterCreate, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterCreate) != string(baseline) {
		t.Fatalf("worktree after StashCreate = %q, want baseline %q", afterCreate, baseline)
	}

	stashes, err := svc.StashList(dir)
	if err != nil {
		t.Fatalf("StashList: %v", err)
	}
	if len(stashes) != 1 {
		t.Fatalf("stash count = %d, want 1", len(stashes))
	}
	entry := stashes[0]
	if entry.Ref != "stash@{0}" || !strings.Contains(entry.Message, "lifecycle | message") {
		t.Fatalf("stash entry = %#v", entry)
	}
	if entry.Author != "Test User" || entry.CommitHash == "" {
		t.Errorf("stash metadata incomplete: %#v", entry)
	}
	if _, err := time.Parse(time.RFC3339, entry.Date); err != nil {
		t.Errorf("stash date %q is not RFC3339: %v", entry.Date, err)
	}

	if err := svc.StashApply(dir, entry.Ref); err != nil {
		t.Fatalf("StashApply: %v", err)
	}
	afterApply, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(afterApply), "\r\n", "\n") != modified {
		t.Fatalf("worktree after StashApply = %q, want %q", afterApply, modified)
	}
	if err := svc.StashDrop(dir, entry.Ref); err != nil {
		t.Fatalf("StashDrop: %v", err)
	}
	stashes, err = svc.StashList(dir)
	if err != nil {
		t.Fatalf("StashList after drop: %v", err)
	}
	if len(stashes) != 0 {
		t.Fatalf("stash count after drop = %d, want 0", len(stashes))
	}
}

func TestGitService_P3_StashPush(t *testing.T) {
	dir, svc := setupP3TestRepo(t)

	writeFile(t, dir, "README.md", "modified content")
	if err := svc.StashPush("my stash message"); err != nil {
		t.Fatalf("StashPush failed: %v", err)
	}
	stashes, _ := svc.StashList(dir)
	if len(stashes) != 1 {
		t.Fatalf("expected 1 stash, got %d", len(stashes))
	}
	if !strings.Contains(stashes[0].Message, "my stash message") {
		t.Errorf("expected message to contain 'my stash message', got %q", stashes[0].Message)
	}
	// stash 后工作区应干净。
	changes, err := svc.GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("expected 0 changes after stash, got %d", len(changes))
	}
}

func TestGitService_P3_CreateTag(t *testing.T) {
	dir, svc := setupP3TestRepo(t)

	if err := svc.CreateTag("v1.0.0", "release v1.0.0"); err != nil {
		t.Fatalf("CreateTag failed: %v", err)
	}
	tags, err := svc.ListTags()
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}
	found := false
	for _, tag := range tags {
		if tag.Name == "v1.0.0" {
			found = true
			if tag.CommitHash == "" {
				t.Error("expected non-empty commit hash for tag")
			}
			if !strings.Contains(tag.Message, "release v1.0.0") {
				t.Errorf("expected message to contain 'release v1.0.0', got %q", tag.Message)
			}
			break
		}
	}
	if !found {
		t.Error("expected to find tag 'v1.0.0' in list")
	}
	_ = dir
}

func TestGitService_P3_ListTags(t *testing.T) {
	dir, svc := setupP3TestRepo(t)

	// 新仓库应无标签。
	tags, err := svc.ListTags()
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected 0 tags on fresh repo, got %d", len(tags))
	}

	// 创建多个标签。
	if err := svc.CreateTag("v1.0.0", "first release"); err != nil {
		t.Fatalf("CreateTag v1.0.0 failed: %v", err)
	}
	if err := svc.CreateTag("v2.0.0", "second release"); err != nil {
		t.Fatalf("CreateTag v2.0.0 failed: %v", err)
	}
	tags, err = svc.ListTags()
	if err != nil {
		t.Fatalf("ListTags after create failed: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}

	// 删除一个标签。
	if err := svc.DeleteTag("v1.0.0"); err != nil {
		t.Fatalf("DeleteTag failed: %v", err)
	}
	tags, err = svc.ListTags()
	if err != nil {
		t.Fatalf("ListTags after delete failed: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag after delete, got %d", len(tags))
	}
	if tags[0].Name != "v2.0.0" {
		t.Errorf("expected remaining tag 'v2.0.0', got %q", tags[0].Name)
	}
	_ = dir
}

func TestGitService_P3_AmendCommit(t *testing.T) {
	dir, svc := setupP3TestRepo(t)

	// 暂存修改并 amend。
	writeFile(t, dir, "README.md", "amended content")
	if err := svc.Stage(dir, "README.md"); err != nil {
		t.Fatalf("Stage failed: %v", err)
	}
	if err := svc.AmendCommit("amended commit message"); err != nil {
		t.Fatalf("AmendCommit failed: %v", err)
	}
	// 验证最近一次提交信息已被修订。
	out, err := svc.runGit(dir, "log", "-1", "--format=%s")
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	if strings.TrimSpace(out) != "amended commit message" {
		t.Errorf("expected amended commit message, got %q", strings.TrimSpace(out))
	}
}

// ---- 校验测试：不需要 git CLI ----

func TestGitService_P3_StashPop_RejectsInvalidRef(t *testing.T) {
	svc := &GitService{}
	dir := t.TempDir()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", "stash@0", "main", "../evil", "stash@{0}; rm -rf /", "stash@{0} `whoami`"} {
		if err := svc.StashPop(ref); err == nil {
			t.Errorf("expected StashPop(%q) to fail validation, got nil", ref)
		}
		if err := svc.StashApply(dir, ref); err == nil {
			t.Errorf("expected StashApply(%q) to fail validation, got nil", ref)
		}
		if err := svc.StashDrop(dir, ref); err == nil {
			t.Errorf("expected StashDrop(%q) to fail validation, got nil", ref)
		}
	}
}

func TestGitService_P3_CreateTag_RejectsInvalidName(t *testing.T) {
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "tag with spaces", "tag;rm", "../evil", "tag`x`", "-leading-dash", ".leading-dot"} {
		if err := svc.CreateTag(name, "msg"); err == nil {
			t.Errorf("expected CreateTag(%q) to fail validation, got nil", name)
		}
	}
}

func TestGitService_P3_PushTags_RejectsInvalidRemote(t *testing.T) {
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, remote := range []string{"", "origin; rm", "../evil", "a b", "origin$(whoami)"} {
		if err := svc.PushTags(remote); err == nil {
			t.Errorf("expected PushTags(%q) to fail validation, got nil", remote)
		}
	}
}

func TestGitService_P3_AmendCommit_RejectsEmptyMessage(t *testing.T) {
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"", "   ", "\t\n"} {
		if err := svc.AmendCommit(msg); err == nil {
			t.Errorf("expected AmendCommit(%q) to fail validation, got nil", msg)
		}
	}
}

func TestGitService_P3_NoWorkspaceRoot(t *testing.T) {
	svc := &GitService{}
	if _, err := svc.StashList(""); err == nil {
		t.Error("expected StashList to fail without workspace root")
	}
	if err := svc.StashCreate("", "x"); err == nil {
		t.Error("expected StashCreate to reject an empty repository path")
	}
	if err := svc.StashApply("", "stash@{0}"); err == nil {
		t.Error("expected StashApply to reject an empty repository path")
	}
	if err := svc.StashDrop("", "stash@{0}"); err == nil {
		t.Error("expected StashDrop to reject an empty repository path")
	}
	if err := svc.StashPush("x"); err == nil {
		t.Error("expected StashPush to fail without workspace root")
	}
	if err := svc.StashPop("stash@{0}"); err == nil {
		t.Error("expected StashPop to fail without workspace root")
	}
	if _, err := svc.ListTags(); err == nil {
		t.Error("expected ListTags to fail without workspace root")
	}
	if err := svc.CreateTag("v1", "m"); err == nil {
		t.Error("expected CreateTag to fail without workspace root")
	}
	if err := svc.DeleteTag("v1"); err == nil {
		t.Error("expected DeleteTag to fail without workspace root")
	}
	if err := svc.PushTags("origin"); err == nil {
		t.Error("expected PushTags to fail without workspace root")
	}
	if err := svc.AmendCommit("m"); err == nil {
		t.Error("expected AmendCommit to fail without workspace root")
	}
}
