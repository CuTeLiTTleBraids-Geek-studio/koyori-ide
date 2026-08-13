package services

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// GitFileChange represents a single changed file in the working tree.
type GitFileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// BranchInfo describes the current branch state.
type BranchInfo struct {
	Name   string `json:"name"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
}

// BranchRef represents a git branch reference.
type BranchRef struct {
	Name   string `json:"name"`
	IsHead bool   `json:"isHead"`
}

// ListBranches returns all local branches in the repository.
func (g *GitService) ListBranches(repoPath string) ([]BranchRef, error) {
	if err := g.validatePath(repoPath); err != nil {
		return nil, err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return nil, err
	}
	defer release()
	repo, err := g.openRepo(repoPath)
	if err != nil {
		return nil, err
	}
	headRef, err := repo.Head()
	if err != nil {
		return nil, err
	}
	headName := headRef.Name().Short()

	var branches []BranchRef
	iter, err := repo.Branches()
	if err != nil {
		return nil, err
	}
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		branches = append(branches, BranchRef{
			Name:   ref.Name().Short(),
			IsHead: ref.Name().Short() == headName,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return branches, nil
}

// CreateBranch creates a new branch at the current HEAD.
func (g *GitService) CreateBranch(repoPath string, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("branch name cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return err
	}
	defer release()
	repo, err := g.openRepo(repoPath)
	if err != nil {
		return err
	}
	headRef, err := repo.Head()
	if err != nil {
		return err
	}
	refName := plumbing.NewBranchReferenceName(name)
	if err := repo.CreateBranch(&config.Branch{
		Name:   name,
		Remote: "origin",
		Merge:  refName,
	}); err != nil {
		return err
	}
	return repo.Storer.SetReference(plumbing.NewHashReference(refName, headRef.Hash()))
}

// CheckoutBranch switches the working tree to the named branch.
func (g *GitService) CheckoutBranch(repoPath string, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("branch name cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return err
	}
	defer release()
	repo, err := g.openRepo(repoPath)
	if err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	return wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
	})
}

// DeleteBranch removes a local branch by name. Returns an error if the
// branch is currently checked out.
func (g *GitService) DeleteBranch(repoPath string, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("branch name cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return err
	}
	defer release()
	repo, err := g.openRepo(repoPath)
	if err != nil {
		return err
	}
	headRef, err := repo.Head()
	if err != nil {
		return err
	}
	if headRef.Name().Short() == name {
		return errors.New("cannot delete the currently checked-out branch")
	}
	return repo.Storer.RemoveReference(plumbing.NewBranchReferenceName(name))
}

// GitService exposes git operations to the frontend.
// N-67: when workspaceRoot is set via SetWorkspaceRoot, all repoPath/path
// arguments are validated to be within the workspace. This prevents the
// frontend from operating on git repos outside the open project.
type GitService struct {
	// enforceWorkspace is enabled by NewGitService for the renderer-facing
	// instance. Zero-value services remain usable by package-internal tests.
	enforceWorkspace bool
	mu               sync.RWMutex
	workspaceRoot    string
	workspaceRoots   []string

	repoMutationMu    sync.Mutex
	repoMutationGates map[string]*repoMutationGate

	blameCacheMu     sync.RWMutex
	blameCache       map[blameCacheKey]blameCacheEntry
	blameCacheOrder  []blameCacheKey
	blameCacheHits   atomic.Uint64
	blameCacheMisses atomic.Uint64
}

func NewGitService() *GitService {
	return &GitService{enforceWorkspace: true}
}

type repoMutationGate struct {
	permit chan struct{}
	users  int
}

func canonicalRepoMutationKey(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	key := gitCommonDir(filepath.Clean(abs))
	if resolved, err := filepath.EvalSymlinks(key); err == nil {
		key = resolved
	}
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key, nil
}

func gitCommonDir(worktree string) string {
	dotGit := filepath.Join(worktree, ".git")
	info, err := os.Stat(dotGit)
	if err == nil && info.IsDir() {
		return worktree
	}
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logGitDebugError("git: inspect repository metadata path failed", err)
		}
		return worktree
	}

	gitDirValue, err := readGitMetadataPath(dotGit)
	if err != nil {
		logGitDebugError("git: read worktree gitdir pointer failed", err)
		return worktree
	}
	const gitDirPrefix = "gitdir:"
	if !strings.HasPrefix(strings.ToLower(gitDirValue), gitDirPrefix) {
		return worktree
	}
	gitDir := strings.TrimSpace(gitDirValue[len(gitDirPrefix):])
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(dotGit), gitDir)
	}
	gitDir = filepath.Clean(gitDir)

	commonDirValue, err := readGitMetadataPath(filepath.Join(gitDir, "commondir"))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logGitDebugError("git: read common-dir pointer failed", err)
		}
		return gitDir
	}
	if !filepath.IsAbs(commonDirValue) {
		commonDirValue = filepath.Join(gitDir, commonDirValue)
	}
	commonDirValue = filepath.Clean(commonDirValue)
	if filepath.Base(commonDirValue) == ".git" {
		return filepath.Dir(commonDirValue)
	}
	return commonDirValue
}

func readGitMetadataPath(path string) (string, error) {
	const maxGitMetadataPathSize = 4096
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxGitMetadataPathSize+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil {
		return "", errors.Join(readErr, closeErr)
	}
	if len(data) > maxGitMetadataPathSize {
		return "", errors.New("git metadata path exceeds size limit")
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("git metadata path is empty")
	}
	return value, nil
}

// acquireRepoMutation serializes repository-changing operations per repository.
// The registry mutex is held only while updating the in-memory gate map; Git I/O
// runs after it has been released. The returned release function is idempotent.
func (g *GitService) acquireRepoMutation(path string) (func(), error) {
	key, err := canonicalRepoMutationKey(path)
	if err != nil {
		return nil, err
	}

	g.repoMutationMu.Lock()
	if g.repoMutationGates == nil {
		g.repoMutationGates = make(map[string]*repoMutationGate)
	}
	gate := g.repoMutationGates[key]
	if gate == nil {
		gate = &repoMutationGate{permit: make(chan struct{}, 1)}
		gate.permit <- struct{}{}
		g.repoMutationGates[key] = gate
	}
	gate.users++
	g.repoMutationMu.Unlock()

	timer := time.NewTimer(gitMutationWaitTimeout)
	defer timer.Stop()
	select {
	case <-gate.permit:
		var once sync.Once
		return func() {
			once.Do(func() {
				gate.permit <- struct{}{}
				g.repoMutationMu.Lock()
				gate.users--
				if gate.users == 0 && g.repoMutationGates[key] == gate {
					delete(g.repoMutationGates, key)
				}
				g.repoMutationMu.Unlock()
			})
		}, nil
	case <-timer.C:
		g.repoMutationMu.Lock()
		gate.users--
		if gate.users == 0 && g.repoMutationGates[key] == gate {
			delete(g.repoMutationGates, key)
		}
		g.repoMutationMu.Unlock()
		return nil, errors.New("timed out waiting for another git operation")
	}
}

// gitLogError keeps the original cause available to errors.Is/errors.As while
// ensuring debug logs never render paths, stderr, commit messages, or content.
type gitLogError struct {
	cause error
}

func (e *gitLogError) Error() string {
	return fmt.Sprintf("git operation failed (%T)", e.cause)
}

func (e *gitLogError) Unwrap() error { return e.cause }

func logGitDebugError(message string, err error) {
	if err != nil {
		slog.Debug(message, "err", &gitLogError{cause: err})
	}
}

// setWorkspaceRoot sets the directory within which git operations are allowed.
// Pass an empty string to disable sandboxing. The root is resolved to an
// absolute path and must be an existing directory.
//
//wails:ignore
func (g *GitService) setWorkspaceRoot(root string) error {
	if root == "" {
		g.mu.Lock()
		g.workspaceRoot = ""
		g.workspaceRoots = nil
		g.mu.Unlock()
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace root is not a directory: %s", abs)
	}
	g.mu.Lock()
	g.workspaceRoot = abs
	g.workspaceRoots = nil
	g.mu.Unlock()
	return nil
}

// setWorkspaceRoots installs the all-or-none Git boundary for a multi-root
// workspace. The first root remains the primary root for legacy operations,
// while validation accepts a repository inside any configured root.
//
//wails:ignore
func (g *GitService) setWorkspaceRoots(roots []string) error {
	if len(roots) == 0 {
		return g.setWorkspaceRoot("")
	}
	cleaned, err := canonicalizeExistingWorkspaceRoots(roots)
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.workspaceRoot = cleaned[0]
	if len(cleaned) > 1 {
		g.workspaceRoots = append([]string(nil), cleaned...)
	} else {
		g.workspaceRoots = nil
	}
	g.mu.Unlock()
	return nil
}

// validatePath returns nil if path is within the workspace root. Git operations
// are renderer-facing and may mutate repository metadata even when their
// primary result is read-only, so an empty root fails closed uniformly.
func (g *GitService) validatePath(path string) error {
	g.mu.RLock()
	root := g.workspaceRoot
	roots := append([]string(nil), g.workspaceRoots...)
	g.mu.RUnlock()
	if root == "" && g.enforceWorkspace {
		return fmt.Errorf("git workspace root is not configured: %w", ErrNotAllowed)
	}
	var err error
	if len(roots) > 0 {
		_, err = ValidatePathWithinRoots(roots, path)
	} else {
		_, err = ValidatePathWithinRoot(root, path)
	}
	return err
}

// DiscoverRepositories returns Git roots below a trusted workspace root.
// WalkDir never follows directory symlinks, and .git metadata is not walked,
// so discovery cannot turn a symlink or repository metadata tree into an
// escape from the workspace authority.
func (g *GitService) DiscoverRepositories(root string) ([]string, error) {
	if err := g.validatePath(root); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository discovery root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repository discovery root is not a directory: %w", ErrInvalidInput)
	}
	const maxRepositories = 256
	repositories := make([]string, 0, 4)
	err = filepath.WalkDir(abs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			return nil
		}
		if _, statErr := os.Lstat(filepath.Join(path, ".git")); statErr == nil {
			repositories = append(repositories, filepath.Clean(path))
			if len(repositories) >= maxRepositories {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover Git repositories: %w", err)
	}
	return repositories, nil
}

// InitRepo initializes a new git repository at the given path. This is used
// when a project directory is not yet a git repo, allowing the user to
// start tracking changes from the source control panel.
func (g *GitService) InitRepo(path string) error {
	if err := g.validatePath(path); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(path)
	if err != nil {
		return err
	}
	defer release()
	repo, err := git.PlainInit(path, false)
	if err != nil {
		return fmt.Errorf("init repository: %w", err)
	}
	// Create an initial commit so the repo has a HEAD reference.
	// Without this, GetStatus/GetBranchInfo fail with "reference not found".
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree after init: %w", err)
	}
	if err := wt.AddGlob("."); err != nil {
		logGitDebugError("git: stage initial repository contents failed", err)
	}
	_, err = wt.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "koyori-ide",
			Email: "koyori-ide@local",
			When:  time.Now(),
		},
	})
	if err != nil {
		// Even if the initial commit fails (e.g. empty repo), the repo is
		// still initialized and usable. The user can make their first commit
		// from the UI.
		logGitDebugError("git: create initial commit failed", err)
		return nil
	}
	return nil
}

// statusToString converts a go-git status code to a human-readable label.
func statusToString(code git.StatusCode) string {
	switch code {
	case git.Untracked:
		return "Untracked"
	case git.Modified:
		return "Modified"
	case git.Added:
		return "Added"
	case git.Deleted:
		return "Deleted"
	case git.Renamed:
		return "Renamed"
	case git.Copied:
		return "Copied"
	case git.Unmodified:
		return "Unmodified"
	default:
		return "Modified"
	}
}

// GetStatus returns the list of changed files in the working tree at path.
func (g *GitService) GetStatus(path string) ([]GitFileChange, error) {
	if err := g.validatePath(path); err != nil {
		return nil, err
	}
	release, err := g.acquireRepoMutation(path)
	if err != nil {
		return nil, err
	}
	defer release()
	return g.getStatus(path)
}

func (g *GitService) getStatus(path string) ([]GitFileChange, error) {
	repo, err := g.openRepo(path)
	if err != nil {
		return nil, err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	st, err := wt.Status()
	if err != nil {
		return nil, err
	}
	changes := make([]GitFileChange, 0, len(st))
	for path, s := range st {
		code := s.Worktree
		if code == git.Unmodified {
			code = s.Staging
		}
		changes = append(changes, GitFileChange{
			Path:   path,
			Status: statusToString(code),
		})
	}
	return changes, nil
}

// GetBranchInfo returns the current branch name and ahead/behind counts.
func (g *GitService) GetBranchInfo(path string) (BranchInfo, error) {
	if err := g.validatePath(path); err != nil {
		return BranchInfo{}, err
	}
	release, err := g.acquireRepoMutation(path)
	if err != nil {
		return BranchInfo{}, err
	}
	defer release()
	repo, err := g.openRepo(path)
	if err != nil {
		return BranchInfo{}, err
	}
	head, err := repo.Head()
	if err != nil {
		return BranchInfo{}, err
	}
	info := BranchInfo{
		Name: head.Name().Short(),
	}
	// Ahead/behind require a remote reference. If no upstream is configured,
	// return zeros (no upstream to compare against).
	ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", info.Name), true)
	if err != nil {
		logGitDebugError("git: upstream branch unavailable", err)
		return info, nil
	}
	info.Ahead, info.Behind = countAheadBehind(repo, head.Hash(), ref.Hash())
	return info, nil
}

// countAheadBehind returns (ahead, behind) counts: commits reachable from head
// but not upstream, and vice versa. Uses the merge base as the divergence point.
func countAheadBehind(repo *git.Repository, head, upstream plumbing.Hash) (int, int) {
	headCommit, err := repo.CommitObject(head)
	if err != nil {
		logGitDebugError("git: read local commit for ahead/behind failed", err)
		return 0, 0
	}
	upstreamCommit, err := repo.CommitObject(upstream)
	if err != nil {
		logGitDebugError("git: read upstream commit for ahead/behind failed", err)
		return 0, 0
	}
	base, err := headCommit.MergeBase(upstreamCommit)
	var baseHash *plumbing.Hash
	if err == nil && len(base) > 0 {
		h := base[0].Hash
		baseHash = &h
	} else if err != nil {
		logGitDebugError("git: find merge base for ahead/behind failed", err)
	}
	return countReachable(repo, head, baseHash), countReachable(repo, upstream, baseHash)
}

// countReachable counts commits reachable from start, stopping at (excluding)
// the commit identified by stop when non-nil.
func countReachable(repo *git.Repository, start plumbing.Hash, stop *plumbing.Hash) int {
	count := 0
	visited := map[plumbing.Hash]bool{}
	queue := []plumbing.Hash{start}
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if visited[h] {
			continue
		}
		visited[h] = true
		if stop != nil && h == *stop {
			continue
		}
		count++
		c, err := repo.CommitObject(h)
		if err != nil {
			logGitDebugError("git: traverse commit graph for ahead/behind failed", err)
			break
		}
		queue = append(queue, c.ParentHashes...)
	}
	return count
}

// openWorktree opens the git repo and worktree at path.
func openWorktree(path string) (*git.Repository, *git.Worktree, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	repo, err := git.PlainOpen(abs)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return nil, nil, errNotARepo
		}
		return nil, nil, err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, nil, err
	}
	return repo, wt, nil
}

var errNotARepo = errors.New("not a git repository")

// openRepo 打开 git 仓库，将 go-git 的 ErrRepositoryNotExists 转换为可识别的
// errNotARepo，避免 "repository does not exist" 原始错误暴露给前端（BUG1）。
func (g *GitService) openRepo(path string) (*git.Repository, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return nil, errNotARepo
		}
		return nil, err
	}
	return repo, nil
}

// validateFilePath checks that filePath is a safe relative path that does not
// escape the repository at path via parent traversal ("..") or absolute paths.
// It is the M-7 / G-SEC-06 defense for Stage/Unstage/ResolveConflict: the
// filePath argument is forwarded to git add/git reset, so a crafted value
// like "../secret" could otherwise operate on files outside the repo.
//
// The check is lexical first (rejects ".." and absolute paths even when no
// workspace root is configured) and then resolves the joined absolute path
// against the workspace root via ValidatePathWithinRoot for defense in depth.
func (g *GitService) validateFilePath(repoPath, filePath string) error {
	if filePath == "" {
		return errors.New("file path is required")
	}
	// Reject absolute paths (Unix, Windows drive, UNC, and backslash-absolute).
	if strings.HasPrefix(filePath, "/") || strings.HasPrefix(filePath, "\\") || filepath.IsAbs(filePath) {
		return fmt.Errorf("invalid file path %q: absolute paths are not allowed", filePath)
	}
	// Reject parent traversal in any component. Clean first (platform-native),
	// then normalize to forward slashes so the prefix check works on Windows
	// where filepath.Clean converts "/" to "\".
	cleaned := filepath.ToSlash(filepath.Clean(filePath))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("invalid file path %q: parent traversal is not allowed", filePath)
	}
	// Defense in depth: validate the resolved absolute path against the
	// workspace root (if one is configured).
	return g.validatePath(filepath.Join(repoPath, filePath))
}

// Stage adds a file path to the git index.
func (g *GitService) Stage(path, filePath string) error {
	if err := g.validatePath(path); err != nil {
		return err
	}
	if err := g.validateFilePath(path, filePath); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(path)
	if err != nil {
		return err
	}
	defer release()
	_, wt, err := openWorktree(path)
	if err != nil {
		return err
	}
	_, err = wt.Add(filePath)
	return err
}

// Unstage removes a file path from the git index (resets to HEAD).
func (g *GitService) Unstage(path, filePath string) error {
	if err := g.validatePath(path); err != nil {
		return err
	}
	if err := g.validateFilePath(path, filePath); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(path)
	if err != nil {
		return err
	}
	defer release()
	repo, wt, err := openWorktree(path)
	if err != nil {
		return err
	}
	head, err := repo.Head()
	if err != nil {
		// No HEAD yet (no commits) — drop the entry from the index directly,
		// keeping the working-tree file in place so it becomes untracked again.
		idx, err := repo.Storer.Index()
		if err != nil {
			return err
		}
		if _, err := idx.Remove(filePath); err != nil && !errors.Is(err, index.ErrEntryNotFound) {
			return err
		}
		return repo.Storer.SetIndex(idx)
	}
	return wt.Reset(&git.ResetOptions{
		Mode:   git.MixedReset,
		Commit: head.Hash(),
		Files:  []string{filePath},
	})
}

// Commit creates a new commit with the currently staged changes.
func (g *GitService) Commit(path, message string) error {
	if err := g.validatePath(path); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(path)
	if err != nil {
		return err
	}
	defer release()
	repo, wt, err := openWorktree(path)
	if err != nil {
		return err
	}
	st, err := wt.Status()
	if err != nil {
		return err
	}
	hasStaged := false
	for _, s := range st {
		if s.Staging != git.Unmodified && s.Staging != git.Untracked {
			hasStaged = true
			break
		}
	}
	if !hasStaged {
		return errors.New("nothing staged to commit")
	}
	config, err := repo.Config()
	if err != nil {
		return fmt.Errorf("read git identity: %w", err)
	}
	name := config.User.Name
	email := config.User.Email
	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" {
		return errors.New("git user.name and user.email must be configured before committing")
	}
	_, err = wt.Commit(message, &git.CommitOptions{Author: &object.Signature{Name: name, Email: email}})
	return err
}

// Push pushes the current branch to remoteName. An empty remoteName uses origin.
func (g *GitService) Push(repoPath string, remoteName string) error {
	if err := g.validatePath(repoPath); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return err
	}
	defer release()
	repo, err := g.openRepo(repoPath)
	if err != nil {
		return err
	}

	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("get head reference: %w", err)
	}
	if !head.Name().IsBranch() {
		return errors.New("head is not on a branch")
	}
	branchName := head.Name().Short()

	if remoteName == "" {
		remoteName = git.DefaultRemoteName
	}
	remote, err := gitRemote(repo, remoteName)
	if err != nil {
		return err
	}
	remoteBranch := head.Name()
	trackingRemote, trackingBranch, trackingConfigured, err := branchTracking(repo, branchName)
	if err != nil {
		return err
	}
	if trackingConfigured && trackingRemote == remoteName {
		remoteBranch = trackingBranch
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitNetworkOperationTimeout)
	defer cancel()
	err = remote.PushContext(ctx, &git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(head.Name().String() + ":" + remoteBranch.String())},
	})
	if err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			return nil
		}
		return err
	}
	return nil
}

// Pull fetches and merges from remoteName. When remoteName is empty, the
// current branch's tracking remote is used, falling back to origin.
func (g *GitService) Pull(repoPath string, remoteName string) error {
	if err := g.validatePath(repoPath); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return err
	}
	defer release()
	repo, err := g.openRepo(repoPath)
	if err != nil {
		return err
	}

	remoteName, remoteBranch, err := pullTarget(repo, remoteName)
	if err != nil {
		return err
	}
	if _, err := gitRemote(repo, remoteName); err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitNetworkOperationTimeout)
	defer cancel()
	err = wt.PullContext(ctx, &git.PullOptions{
		RemoteName:    remoteName,
		ReferenceName: remoteBranch,
		SingleBranch:  true,
	})
	if err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			return nil
		}
		return err
	}
	return nil
}

func gitRemote(repo *git.Repository, remoteName string) (*git.Remote, error) {
	remote, err := repo.Remote(remoteName)
	if errors.Is(err, git.ErrRemoteNotFound) {
		return nil, fmt.Errorf("remote %s not found", remoteName)
	}
	if err != nil {
		return nil, fmt.Errorf("get remote %s: %w", remoteName, err)
	}
	return remote, nil
}

func pullTarget(repo *git.Repository, remoteName string) (string, plumbing.ReferenceName, error) {
	remoteBranch := plumbing.HEAD
	head, err := repo.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		if remoteName == "" {
			remoteName = git.DefaultRemoteName
		}
		return remoteName, remoteBranch, nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get head reference: %w", err)
	}
	if !head.Name().IsBranch() {
		if remoteName == "" {
			remoteName = git.DefaultRemoteName
		}
		return remoteName, remoteBranch, nil
	}

	branchName := head.Name().Short()
	remoteBranch = head.Name()
	trackingRemote, trackingBranch, trackingConfigured, err := branchTracking(repo, branchName)
	if err != nil {
		return "", "", err
	}
	if remoteName == "" {
		if trackingConfigured {
			return trackingRemote, trackingBranch, nil
		}
		return git.DefaultRemoteName, remoteBranch, nil
	}
	if trackingConfigured && trackingRemote == remoteName {
		remoteBranch = trackingBranch
	}
	return remoteName, remoteBranch, nil

}

func branchTracking(repo *git.Repository, branchName string) (string, plumbing.ReferenceName, bool, error) {
	repoConfig, err := repo.Config()
	if err != nil {
		return "", "", false, fmt.Errorf("read repository config: %w", err)
	}
	branch := repoConfig.Branches[branchName]
	if branch == nil || branch.Remote == "" {
		return "", "", false, nil
	}
	remoteBranch := branch.Merge
	if remoteBranch == "" {
		remoteBranch = plumbing.NewBranchReferenceName(branchName)
	}
	return branch.Remote, remoteBranch, true, nil
}

// GetDiff returns the unified diff for a single file.
// For staged files, diffs HEAD vs index. For unstaged changes, diffs index vs worktree.
// For untracked files, returns the full content as additions.
func (g *GitService) GetDiff(repoPath string, filePath string) (string, error) {
	if err := g.validatePath(repoPath); err != nil {
		return "", err
	}
	if err := g.validateFilePath(repoPath, filePath); err != nil {
		return "", err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return "", err
	}
	defer release()
	return g.getDiff(repoPath, filePath)
}

func (g *GitService) getDiff(repoPath string, filePath string) (string, error) {
	repo, err := g.openRepo(repoPath)
	if err != nil {
		return "", err
	}

	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}

	status, err := wt.Status()
	if err != nil {
		return "", err
	}

	fileStatus, ok := status[filePath]
	if !ok {
		return "", fmt.Errorf("file %s not found in git status", filePath)
	}

	// If fully untracked, return file content as all-added
	if fileStatus.Staging == git.Untracked && fileStatus.Worktree == git.Untracked {
		return g.diffUntrackedFile(repoPath, filePath)
	}

	// For staged changes (Staging is Modified/Added/Deleted), diff HEAD vs index
	if fileStatus.Staging != git.Unmodified && fileStatus.Staging != git.Untracked {
		return g.diffStaged(repo, filePath)
	}

	// For unstaged changes, diff index vs worktree
	return g.diffWorktree(repo, filePath)
}

// diffUntrackedFile returns the full file content as a diff with all lines added.
func (g *GitService) diffUntrackedFile(repoPath, filePath string) (string, error) {
	absPath := filepath.Join(repoPath, filePath)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "diff --git a/%s b/%s\n", filePath, filePath)
	buf.WriteString("new file mode 100644\n")
	buf.WriteString("--- /dev/null\n")
	fmt.Fprintf(&buf, "+++ b/%s\n", filePath)
	for _, line := range strings.Split(string(data), "\n") {
		buf.WriteString("+" + line + "\n")
	}
	return buf.String(), nil
}

// diffStaged diffs the HEAD version vs the index version of a file.
func (g *GitService) diffStaged(repo *git.Repository, filePath string) (string, error) {
	// Get HEAD version
	headData, err := g.getFileFromHead(repo, filePath)
	if err != nil {
		// File is new in index (no HEAD version)
		logGitDebugError("git: staged diff falling back to new-file format", err)
		idxData, err2 := g.getFileFromIndex(repo, filePath)
		if err2 != nil {
			return "", err2
		}
		return g.formatNewFileDiff(filePath, idxData), nil
	}

	// Get index version
	idxData, err := g.getFileFromIndex(repo, filePath)
	if err != nil {
		return "", err
	}

	return myersDiff(filePath, headData, idxData), nil
}

// diffWorktree diffs the index version vs the working tree version of a file.
func (g *GitService) diffWorktree(repo *git.Repository, filePath string) (string, error) {
	idxData, err := g.getFileFromIndex(repo, filePath)
	if err != nil {
		// File not in index — this case is handled by diffUntrackedFile in GetDiff;
		// return the error if we somehow reach here.
		return "", err
	}

	// Read worktree version
	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	absPath := filepath.Join(wt.Filesystem.Root(), filePath)
	wtData, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}

	return myersDiff(filePath, idxData, string(wtData)), nil
}

// getFileFromHead reads the file content from the HEAD commit's tree.
func (g *GitService) getFileFromHead(repo *git.Repository, filePath string) (string, error) {
	headRef, err := repo.Head()
	if err != nil {
		return "", err
	}
	commit, err := repo.CommitObject(headRef.Hash())
	if err != nil {
		return "", err
	}
	tree, err := commit.Tree()
	if err != nil {
		return "", err
	}
	file, err := tree.File(filePath)
	if err != nil {
		return "", err
	}
	reader, err := file.Reader()
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			logGitDebugError("git: close blob reader failed", closeErr)
		}
	}()
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// getFileFromIndex reads the file content from the git index.
func (g *GitService) getFileFromIndex(repo *git.Repository, filePath string) (string, error) {
	idx, err := repo.Storer.Index()
	if err != nil {
		return "", err
	}
	entry, err := idx.Entry(filePath)
	if err != nil {
		return "", err
	}
	blob, err := repo.BlobObject(entry.Hash)
	if err != nil {
		return "", err
	}
	reader, err := blob.Reader()
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			logGitDebugError("git: close object reader failed", closeErr)
		}
	}()
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// formatNewFileDiff returns a diff for a newly added file.
func (g *GitService) formatNewFileDiff(filePath string, content string) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "diff --git a/%s b/%s\n", filePath, filePath)
	buf.WriteString("new file mode 100644\n")
	buf.WriteString("--- /dev/null\n")
	fmt.Fprintf(&buf, "+++ b/%s\n", filePath)
	for _, line := range strings.Split(content, "\n") {
		buf.WriteString("+" + line + "\n")
	}
	return buf.String()
}

// GetFullDiff returns the combined diff of all changed files (staged + unstaged
// + untracked) in the working tree. Each file's diff is preceded by a header
// line of the form "=== filePath ===" for easy parsing. Returns an empty string
// when there are no changes. Used by the AI code review feature (#27).
func (g *GitService) GetFullDiff(repoPath string) (string, error) {
	if err := g.validatePath(repoPath); err != nil {
		return "", err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return "", err
	}
	defer release()

	changes, err := g.getStatus(repoPath)
	if err != nil {
		// 非 git 仓库时返回空 diff 而非错误，使代码审查等功能能优雅降级（BUG1）。
		if errors.Is(err, errNotARepo) {
			logGitDebugError("git: full diff unavailable outside a repository", err)
			return "", nil
		}
		return "", err
	}
	if len(changes) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	for _, c := range changes {
		d, err := g.getDiff(repoPath, c.Path)
		if err != nil {
			// Skip files that fail to diff (e.g. binary, deleted) but continue.
			logGitDebugError("git: full diff skipped a file", err)
			continue
		}
		if d == "" {
			continue
		}
		fmt.Fprintf(&buf, "=== %s ===\n", c.Path)
		buf.WriteString(d)
		if !strings.HasSuffix(d, "\n") {
			buf.WriteString("\n")
		}
		buf.WriteString("\n")
	}
	return buf.String(), nil
}

// Note: unifiedDiff was replaced by myersDiff (Plan 60 / N-27) which
// implements the Myers O(ND) diff algorithm for cleaner diffs.

// ---------------------------------------------------------------------------
// G-FEAT-04: .gitignore template generation, rebase/merge conflict support
// ---------------------------------------------------------------------------

// workspaceRootPath returns the configured workspace root under a read lock.
// Returns an empty string when no root is set (legacy mode). Methods that
// operate on the "current project" (Rebase, ListMergeConflicts, ...) use this
// to locate the repository.
func (g *GitService) workspaceRootPath() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.workspaceRoot
}

// branchNameRe matches git branch names that are safe to pass to the git CLI.
// It rejects shell metacharacters and whitespace, preventing command injection
// via the Rebase branch argument.
var branchNameRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// MergeConflict describes a single file with unresolved merge/rebase conflicts.
// The Ours/Theirs/Base fields hold the blob content of each side (empty string
// when a side is absent, e.g. an add/add conflict has no base).
type MergeConflict struct {
	File   string `json:"file"`
	Ours   string `json:"ours"`
	Theirs string `json:"theirs"`
	Base   string `json:"base"`
}

// GitignoreTemplate returns a .gitignore template for the given project type.
// Supported types: "go", "typescript" (alias "ts"), "javascript" (alias "js"),
// and "general". Matching is case-insensitive. An empty projectType defaults
// to "general".
func (g *GitService) GitignoreTemplate(projectType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(projectType)) {
	case "go":
		return gitignoreGo, nil
	case "typescript", "ts":
		return gitignoreTypeScript, nil
	case "javascript", "js":
		return gitignoreJavaScript, nil
	case "general", "":
		return gitignoreGeneral, nil
	default:
		return "", fmt.Errorf("unknown project type %q: supported types are go, typescript, javascript, general", projectType)
	}
}

// CreateGitignore writes a .gitignore file generated from the given project
// type into the workspace root. It refuses to overwrite an existing
// .gitignore so user customizations are preserved.
func (g *GitService) CreateGitignore(projectType string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	tmpl, err := g.GitignoreTemplate(projectType)
	if err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	target := filepath.Join(root, ".gitignore")
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf(".gitignore already exists at %s: %w", target, err)
		}
		return fmt.Errorf("create .gitignore: %w", err)
	}
	written, writeErr := io.WriteString(f, tmpl)
	if writeErr == nil && written != len(tmpl) {
		writeErr = io.ErrShortWrite
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		removeErr := os.Remove(target)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return fmt.Errorf("write .gitignore: %w", errors.Join(err, removeErr))
	}
	return nil
}

// Rebase starts a rebase of the current branch onto the given branch.
// It shells out to the git CLI because go-git v5 has no rebase API. The
// branch name is validated against branchNameRe to prevent injection.
// Returns the combined stdout/stderr output.
func (g *GitService) Rebase(branch string) (string, error) {
	root := g.workspaceRootPath()
	if root == "" {
		return "", errors.New("no workspace root set")
	}
	if strings.TrimSpace(branch) == "" {
		return "", errors.New("branch name cannot be empty")
	}
	if !branchNameRe.MatchString(branch) {
		return "", fmt.Errorf("invalid branch name %q", branch)
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return "", err
	}
	defer release()
	return g.runGit(root, "rebase", branch)
}

// AbortRebase aborts an in-progress rebase.
func (g *GitService) AbortRebase() error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "rebase", "--abort")
	return err
}

// ContinueRebase continues a rebase after conflicts have been resolved
// (staged via ResolveConflict).
func (g *GitService) ContinueRebase() error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "rebase", "--continue")
	return err
}

// runGit executes the git binary with the given args inside repoPath and
// returns the combined output. Used by the rebase methods which need CLI
// features that go-git does not expose.
//
// M-1: cmd.Env is set to a minimal whitelist (PATH / HOME or USERPROFILE /
// SYSTEMROOT / GIT_TERMINAL_PROMPT=0 / locale) so credential helpers and
// SSH agents inherited from the parent process cannot leak secrets via env.
func (g *GitService) runGit(repoPath string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	return g.runGitContext(ctx, repoPath, args...)
}

func (g *GitService) runGitContext(ctx context.Context, repoPath string, args ...string) (string, error) {
	cmd := commandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	cmd.Env = minimalGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return string(out), fmt.Errorf("git %s: %w", strings.Join(args, " "), ctxErr)
		}
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// runGitStream starts git with the given args and returns a *bufio.Scanner
// over stdout plus a wait function the caller MUST invoke after consuming
// the scanner. stderr is buffered for error reporting. Used by streaming
// consumers (e.g. parseGitBlame — M-2) to avoid loading entire outputs
// into memory. The same minimal env as runGit is applied (M-1).
func (g *GitService) runGitStream(repoPath string, args ...string) (*bufio.Scanner, func() error, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	scanner, wait, err := g.runGitStreamContext(ctx, repoPath, args...)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return scanner, func() error {
		defer cancel()
		return wait()
	}, nil
}

func (g *GitService) runGitStreamContext(ctx context.Context, repoPath string, args ...string) (*bufio.Scanner, func() error, error) {
	cmd := commandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	cmd.Env = minimalGitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("git %s: stdout pipe: %w", strings.Join(args, " "), err)
	}
	if err := cmd.Start(); err != nil {
		startErr := fmt.Errorf("git %s: start: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		closeErr := stdout.Close()
		if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			closeErr = fmt.Errorf("git stdout pipe close after start failure: %w", closeErr)
		} else {
			closeErr = nil
		}
		return nil, nil, errors.Join(startErr, closeErr)
	}
	scanner := bufio.NewScanner(stdout)
	// Allow long lines (rare in blame, but defensive — default 64KB).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var waitOnce sync.Once
	var waitResult error
	wait := func() error {
		waitOnce.Do(func() {
			closeErr := stdout.Close()
			if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				closeErr = fmt.Errorf("git stdout pipe close: %w", closeErr)
			} else {
				closeErr = nil
			}

			waitErr := cmd.Wait()
			if waitErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					waitErr = fmt.Errorf("git %s: %w", strings.Join(args, " "), ctxErr)
				} else {
					waitErr = fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), waitErr, strings.TrimSpace(stderr.String()))
				}
			}
			waitResult = errors.Join(closeErr, waitErr)
		})
		return waitResult
	}
	return scanner, wait, nil
}

// minimalGitEnv constructs a minimal environment whitelist for git
// subprocesses. Only the variables needed for git to function are included;
// everything else (credential helpers, SSH agent sockets, GIT_* overrides
// that could leak secrets or change behaviour) is dropped.
//
// Included variables:
//   - PATH            — so git can spawn helpers (ssh, credential-cache, etc.)
//   - HOME / USERPROFILE / SYSTEMROOT / APPDATA / LOCALAPPDATA — needed for
//     ~/.gitconfig lookup and, on Windows, DLL resolution
//   - GIT_TERMINAL_PROMPT=0 — disable interactive credential prompts
//   - LANG / LC_ALL   — locale (preserved if already set; not invented)
//
// M-1.
func minimalGitEnv() []string {
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	if v := os.Getenv("PATH"); v != "" {
		env = append(env, "PATH="+v)
	}
	if runtime.GOOS == "windows" {
		for _, k := range []string{"USERPROFILE", "SYSTEMROOT", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP"} {
			if v := os.Getenv(k); v != "" {
				env = append(env, k+"="+v)
			}
		}
	} else {
		if v := os.Getenv("HOME"); v != "" {
			env = append(env, "HOME="+v)
		}
	}
	for _, k := range []string{"LANG", "LC_ALL"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// IsRebaseInProgress reports whether a rebase is currently in progress.
// Git records an in-progress rebase via the .git/rebase-merge (interactive)
// or .git/rebase-apply (am-based) directory.
func (g *GitService) IsRebaseInProgress() (bool, error) {
	root := g.workspaceRootPath()
	if root == "" {
		return false, errors.New("no workspace root set")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return false, err
	}
	defer release()
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		info, err := os.Stat(filepath.Join(root, ".git", dir))
		if err == nil && info.IsDir() {
			return true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			logGitDebugError("git: inspect rebase state failed", err)
		}
	}
	return false, nil
}

// ListMergeConflicts returns the files with unresolved merge/rebase conflicts
// in the workspace root repository. A file is conflicted when the index holds
// entries for it at stage 1 (base), 2 (ours), and/or 3 (theirs) instead of a
// single stage-0 (merged) entry. The Ours/Theirs/Base fields are populated
// with each side's blob content (empty when a side is absent).
func (g *GitService) ListMergeConflicts() ([]MergeConflict, error) {
	root := g.workspaceRootPath()
	if root == "" {
		return nil, errors.New("no workspace root set")
	}
	if err := g.validatePath(root); err != nil {
		return nil, err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return nil, err
	}
	defer release()
	repo, err := g.openRepo(root)
	if err != nil {
		return nil, err
	}
	idx, err := repo.Storer.Index()
	if err != nil {
		return nil, err
	}
	type conflictStages struct {
		base, ours, theirs *index.Entry
	}
	files := make(map[string]*conflictStages)
	var order []string
	for i := range idx.Entries {
		e := idx.Entries[i]
		if e.Stage == 0 {
			continue // normal merged entry, no conflict
		}
		s, ok := files[e.Name]
		if !ok {
			s = &conflictStages{}
			files[e.Name] = s
			order = append(order, e.Name)
		}
		switch e.Stage {
		case index.AncestorMode:
			s.base = e
		case index.OurMode:
			s.ours = e
		case index.TheirMode:
			s.theirs = e
		}
	}
	conflicts := make([]MergeConflict, 0, len(order))
	for _, name := range order {
		s := files[name]
		c := MergeConflict{File: name}
		if s.base != nil {
			content, err := readBlobContent(repo, s.base.Hash)
			if err != nil {
				return nil, fmt.Errorf("read base conflict blob for %q: %w", name, err)
			}
			c.Base = content
		}
		if s.ours != nil {
			content, err := readBlobContent(repo, s.ours.Hash)
			if err != nil {
				return nil, fmt.Errorf("read ours conflict blob for %q: %w", name, err)
			}
			c.Ours = content
		}
		if s.theirs != nil {
			content, err := readBlobContent(repo, s.theirs.Hash)
			if err != nil {
				return nil, fmt.Errorf("read theirs conflict blob for %q: %w", name, err)
			}
			c.Theirs = content
		}
		conflicts = append(conflicts, c)
	}
	return conflicts, nil
}

// ResolveConflict marks a conflicted file as resolved by staging it
// (equivalent to `git add <file>`). The filePath is validated against
// path traversal via validateFilePath (M-7).
func (g *GitService) ResolveConflict(filePath string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := g.validateFilePath(root, filePath); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, wt, err := openWorktree(root)
	if err != nil {
		return err
	}
	_, err = wt.Add(filePath)
	return err
}

// BlameLine is the blame info for a single line in a file.
type BlameLine struct {
	Line    int    `json:"line"`
	Commit  string `json:"commit"`  // short SHA
	Author  string `json:"author"`  // author name
	Date    string `json:"date"`    // RFC3339 author timestamp
	Content string `json:"content"` // source line content
	Email   string `json:"email"`   // author email
	Time    string `json:"time"`    // deprecated alias of Date
	Summary string `json:"summary"` // commit message summary
}

type blameCacheKey struct {
	filePath  string
	startLine int
	endLine   int
}

type blameCacheEntry struct {
	contentHash  [sha256.Size]byte
	headRevision string
	lines        []BlameLine
}

type gitStreamRunner func(repoPath string, args ...string) (*bufio.Scanner, func() error, error)

// CommitGraphEntry is one locale-independent git log record for the commit graph.
type CommitGraphEntry struct {
	Hash    string   `json:"hash"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Time    string   `json:"time"`
	Refs    []string `json:"refs"`
	Subject string   `json:"subject"`
}

const (
	maxBlameRange              = 5000
	maxBlameCacheEntries       = 256
	gitCommandTimeout          = 5 * time.Minute
	gitNetworkOperationTimeout = 5 * time.Minute
	gitMutationWaitTimeout     = 5 * time.Minute
	defaultCommitGraphLimit    = 50
	maxCommitGraphLimit        = 200
	commitGraphFieldSep        = "\x1f"
	commitGraphRecordSep       = "\x1e"
)

var (
	commitGraphBranchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	resolvedCommitRe    = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)
)

// GetBlameAtRevision returns blame information for a bounded line range at
// an optional revision. User-provided revisions are resolved after
// --end-of-options and only the resulting hexadecimal object ID is passed to
// blame, so a revision can never become a git option.
func (g *GitService) GetBlameAtRevision(repoPath, filePath string, startLine, endLine int, revision string) ([]BlameLine, error) {
	if err := g.validatePath(repoPath); err != nil {
		return nil, err
	}
	if err := g.validateFilePath(repoPath, filePath); err != nil {
		return nil, err
	}
	if (startLine != 0 || endLine != 0) &&
		(startLine <= 0 || endLine < startLine || endLine-startLine+1 > maxBlameRange) {
		return nil, fmt.Errorf("invalid blame range %d:%d", startLine, endLine)
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return nil, err
	}
	defer release()

	resolvedRevision := ""
	if revision != "" {
		var err error
		resolvedRevision, err = g.resolveGitRevision(repoPath, revision)
		if err != nil {
			return nil, err
		}
	}
	available, err := g.blameTargetAvailable(repoPath, filePath, resolvedRevision)
	if err != nil {
		return nil, err
	}
	if !available {
		return []BlameLine{}, nil
	}

	args := []string{"blame", "--line-porcelain"}
	if startLine > 0 {
		args = append(args, fmt.Sprintf("-L%d,%d", startLine, endLine))
	}
	if resolvedRevision != "" {
		args = append(args, resolvedRevision)
	}
	args = append(args, "--", filePath)
	scanner, wait, err := g.runGitStream(repoPath, args...)
	if err != nil {
		return nil, err
	}
	result, parseErr, waitErr := finishGitBlameStream(scanner, wait)
	if parseErr != nil {
		return nil, errors.Join(parseErr, waitErr)
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return result, nil
}

// GetBlameForRange returns blame information for the inclusive, 1-indexed
// line range. Results are cached by canonical file path, range, SHA-256
// content hash, and HEAD revision so repeated viewport requests avoid another
// blame process while edits and history changes invalidate the cache.
func (g *GitService) GetBlameForRange(repoPath, filePath string, startLine, endLine int) ([]BlameLine, error) {
	return g.getBlameForRange(repoPath, filePath, startLine, endLine, g.runGitStream)
}

func (g *GitService) getBlameForRange(
	repoPath, filePath string,
	startLine, endLine int,
	runStream gitStreamRunner,
) ([]BlameLine, error) {
	if repoPath == "" {
		return nil, errors.New("repository path cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return nil, err
	}
	if err := g.validateFilePath(repoPath, filePath); err != nil {
		return nil, err
	}
	if startLine <= 0 || endLine < startLine || endLine-startLine+1 > maxBlameRange {
		return nil, fmt.Errorf("invalid blame range %d:%d", startLine, endLine)
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return nil, err
	}
	defer release()

	absPath, err := filepath.Abs(filepath.Join(repoPath, filepath.Clean(filePath)))
	if err != nil {
		return nil, fmt.Errorf("resolve blame file: %w", err)
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read blame file: %w", err)
	}
	contentHash := sha256.Sum256(content)
	cacheKey := blameCacheKey{
		filePath:  filepath.Clean(absPath),
		startLine: startLine,
		endLine:   endLine,
	}
	headRevision, err := g.resolveGitRevision(repoPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve blame head: %w", err)
	}
	if lines, ok := g.cachedBlame(cacheKey, contentHash, headRevision); ok {
		return lines, nil
	}

	args := []string{
		"blame",
		"-L", fmt.Sprintf("%d,%d", startLine, endLine),
		"--line-porcelain",
		"--", filePath,
	}
	scanner, wait, err := runStream(repoPath, args...)
	if err != nil {
		return nil, err
	}
	lines, parseErr, waitErr := finishGitBlameStream(scanner, wait)
	if streamErr := errors.Join(parseErr, waitErr); streamErr != nil {
		return nil, streamErr
	}

	// Do not cache output if the file or repository history changed while git
	// blame was running.
	currentContent, readErr := os.ReadFile(absPath)
	currentHead, headErr := g.resolveGitRevision(repoPath, "HEAD")
	if readErr != nil {
		logGitDebugError("git: skip blame cache update after file read failure", readErr)
	}
	if headErr != nil {
		logGitDebugError("git: skip blame cache update after head resolution failure", headErr)
	}
	if readErr == nil && headErr == nil &&
		sha256.Sum256(currentContent) == contentHash && currentHead == headRevision {
		g.storeBlame(cacheKey, contentHash, headRevision, lines)
	}
	return cloneBlameLines(lines), nil
}

func (g *GitService) cachedBlame(
	key blameCacheKey,
	contentHash [sha256.Size]byte,
	headRevision string,
) ([]BlameLine, bool) {
	g.blameCacheMu.Lock()
	entry, ok := g.blameCache[key]
	hit := ok && entry.contentHash == contentHash && entry.headRevision == headRevision
	if hit {
		g.blameCacheOrder = promoteBlameCacheKey(g.blameCacheOrder, key)
	}
	g.blameCacheMu.Unlock()
	g.recordBlameCacheResult(hit)
	if !hit {
		return nil, false
	}
	return cloneBlameLines(entry.lines), true
}

func (g *GitService) recordBlameCacheResult(hit bool) {
	event := "miss"
	if hit {
		event = "hit"
		g.blameCacheHits.Add(1)
	} else {
		g.blameCacheMisses.Add(1)
	}
	hits := g.blameCacheHits.Load()
	misses := g.blameCacheMisses.Load()
	total := hits + misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(hits) / float64(total)
	}
	slog.Debug("git blame cache", "event", event, "hits", hits, "misses", misses, "hit_rate", hitRate)
}

func (g *GitService) storeBlame(
	key blameCacheKey,
	contentHash [sha256.Size]byte,
	headRevision string,
	lines []BlameLine,
) {
	g.blameCacheMu.Lock()
	defer g.blameCacheMu.Unlock()
	if g.blameCache == nil {
		g.blameCache = make(map[blameCacheKey]blameCacheEntry)
	}
	if _, exists := g.blameCache[key]; !exists {
		if len(g.blameCacheOrder) >= maxBlameCacheEntries {
			oldest := g.blameCacheOrder[0]
			g.blameCacheOrder = g.blameCacheOrder[1:]
			delete(g.blameCache, oldest)
		}
		g.blameCacheOrder = append(g.blameCacheOrder, key)
	} else {
		g.blameCacheOrder = promoteBlameCacheKey(g.blameCacheOrder, key)
	}
	g.blameCache[key] = blameCacheEntry{
		contentHash:  contentHash,
		headRevision: headRevision,
		lines:        cloneBlameLines(lines),
	}
}

func promoteBlameCacheKey(order []blameCacheKey, key blameCacheKey) []blameCacheKey {
	for i := range order {
		if order[i] != key {
			continue
		}
		if i < len(order)-1 {
			copy(order[i:], order[i+1:])
			order[len(order)-1] = key
		}
		return order
	}
	return order
}

func cloneBlameLines(lines []BlameLine) []BlameLine {
	cloned := make([]BlameLine, len(lines))
	copy(cloned, lines)
	return cloned
}

// GetCommitGraph returns structured commit graph records.
func (g *GitService) GetCommitGraph(repoPath string, limit int, branch string, all bool) ([]CommitGraphEntry, error) {
	if err := g.validatePath(repoPath); err != nil {
		return nil, err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return nil, err
	}
	defer release()
	if _, err := g.openRepo(repoPath); err != nil {
		return nil, err
	}
	if branch != "" && !validCommitGraphBranch(branch) {
		return nil, fmt.Errorf("invalid branch name: %q", branch)
	}
	if limit <= 0 {
		limit = defaultCommitGraphLimit
	} else if limit > maxCommitGraphLimit {
		limit = maxCommitGraphLimit
	}

	const format = "%H%x1f%P%x1f%an%x1f%ae%x1f%aI%x1f%D%x1f%s%x1e"
	args := []string{
		"log",
		"--topo-order",
		"--date=iso-strict",
		fmt.Sprintf("--max-count=%d", limit),
		"--decorate=full",
		"--pretty=format:" + format,
	}
	if all {
		args = append(args, "--all")
	} else {
		revision := "HEAD"
		if branch != "" {
			revision = "refs/heads/" + branch
		}
		resolved, err := g.resolveGitRevision(repoPath, revision)
		if err != nil {
			if branch == "" {
				logGitDebugError("git: commit graph unavailable without a resolved head", err)
				return []CommitGraphEntry{}, nil
			}
			return nil, err
		}
		args = append(args, resolved)
	}
	args = append(args, "--")
	output, err := g.runGit(repoPath, args...)
	if err != nil {
		if all {
			logGitDebugError("git: all-reference commit graph unavailable", err)
			return []CommitGraphEntry{}, nil
		}
		return nil, err
	}
	return parseCommitGraph(output), nil
}

func validCommitGraphBranch(branch string) bool {
	return commitGraphBranchRe.MatchString(branch) &&
		!strings.Contains(branch, "..") &&
		!strings.Contains(branch, "//") &&
		!strings.Contains(branch, "@{") &&
		!strings.HasSuffix(branch, "/") &&
		!strings.HasSuffix(branch, ".")
}

func (g *GitService) resolveGitRevision(repoPath, revision string) (string, error) {
	if strings.TrimSpace(revision) != revision || revision == "" || strings.ContainsAny(revision, "\x00\r\n") {
		return "", fmt.Errorf("invalid git revision")
	}
	output, err := g.runGit(repoPath, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve git revision: %w", err)
	}
	resolved := strings.TrimSpace(output)
	if !resolvedCommitRe.MatchString(resolved) {
		return "", fmt.Errorf("git returned invalid revision")
	}
	return strings.ToLower(resolved), nil
}

func parseCommitGraph(output string) []CommitGraphEntry {
	records := strings.Split(output, commitGraphRecordSep)
	entries := make([]CommitGraphEntry, 0, len(records))
	for _, record := range records {
		fields := strings.SplitN(record, commitGraphFieldSep, 7)
		if len(fields) != 7 {
			continue
		}
		hash := strings.TrimSpace(fields[0])
		if !resolvedCommitRe.MatchString(hash) {
			continue
		}
		refs := make([]string, 0)
		for _, ref := range strings.Split(fields[5], ",") {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			ref = strings.ReplaceAll(ref, "refs/heads/", "")
			ref = strings.ReplaceAll(ref, "refs/remotes/", "")
			ref = strings.ReplaceAll(ref, "refs/tags/", "tag: ")
			refs = append(refs, ref)
		}
		entries = append(entries, CommitGraphEntry{
			Hash:    strings.ToLower(hash),
			Parents: strings.Fields(fields[1]),
			Author:  fields[2],
			Email:   fields[3],
			Time:    fields[4],
			Refs:    refs,
			Subject: fields[6],
		})
	}
	return entries
}

// GetBlame returns per-line blame information for a file using
// `git blame --line-porcelain`. This powers the inline blame decoration
// in the editor (author + commit message shown at the end of each line).
// Returns an empty slice for non-repo directories or untracked files.
//
// M-2: output is consumed line-by-line via bufio.Scanner over a stdout
// pipe instead of being buffered in full via CombinedOutput, so very
// large files no longer risk OOM. The internal commit metadata cache is
// bounded (recentCommitsLimit) to prevent unbounded growth.
func (g *GitService) GetBlame(repoPath, filePath string) ([]BlameLine, error) {
	return g.GetBlameRange(repoPath, filePath, 0, 0)
}

// GetBlameRange is like GetBlame but limits blame to lines [startLine, endLine]
// (1-indexed, inclusive) via `git blame -L start,end`. Either zero disables
// the range (blame the whole file). Limiting the range is the recommended
// way to bound blame output for very large files.
func (g *GitService) GetBlameRange(repoPath, filePath string, startLine, endLine int) ([]BlameLine, error) {
	if err := g.validatePath(repoPath); err != nil {
		return nil, err
	}
	if err := g.validateFilePath(repoPath, filePath); err != nil {
		return nil, err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return nil, err
	}
	defer release()
	available, err := g.blameTargetAvailable(repoPath, filePath, "")
	if err != nil {
		return nil, err
	}
	if !available {
		return []BlameLine{}, nil
	}
	args := []string{"blame", "--line-porcelain"}
	if startLine > 0 && endLine > 0 {
		args = append(args, fmt.Sprintf("-L%d,%d", startLine, endLine))
	} else if startLine > 0 {
		args = append(args, fmt.Sprintf("-L%d,", startLine))
	}
	args = append(args, "--", filePath)
	scanner, wait, err := g.runGitStream(repoPath, args...)
	if err != nil {
		return nil, err
	}
	result, parseErr, waitErr := finishGitBlameStream(scanner, wait)
	if parseErr != nil {
		return nil, errors.Join(parseErr, waitErr)
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return result, nil
}

// blameTargetAvailable identifies only the documented benign blame states.
// All other repository, object, index, and process failures remain errors.
func (g *GitService) blameTargetAvailable(repoPath, filePath, revision string) (bool, error) {
	repo, err := g.openRepo(repoPath)
	if err != nil {
		if errors.Is(err, errNotARepo) {
			logGitDebugError("git: blame unavailable outside a repository", err)
			return false, nil
		}
		return false, err
	}

	var commit *object.Commit
	if revision != "" {
		commit, err = repo.CommitObject(plumbing.NewHash(revision))
	} else {
		var head *plumbing.Reference
		head, err = repo.Head()
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			logGitDebugError("git: blame unavailable without head", err)
			return false, nil
		}
		if err == nil {
			commit, err = repo.CommitObject(head.Hash())
		}
	}
	if err != nil {
		return false, err
	}

	gitPath := filepath.ToSlash(filepath.Clean(filePath))
	if _, err := commit.File(gitPath); err == nil {
		return true, nil
	} else if !errors.Is(err, object.ErrFileNotFound) {
		return false, err
	} else if revision != "" {
		logGitDebugError("git: blame target absent at revision", err)
		return false, nil
	}

	idx, err := repo.Storer.Index()
	if err != nil {
		return false, err
	}
	if _, err := idx.Entry(gitPath); err == nil {
		return true, nil
	} else if errors.Is(err, index.ErrEntryNotFound) {
		logGitDebugError("git: blame target is untracked", err)
		return false, nil
	} else {
		return false, err
	}
}

// recentCommitsLimit bounds the per-commit metadata cache used by
// parseGitBlameStream. With --line-porcelain, every line carries its own
// metadata block, so the cache is a minor optimisation for adjacent lines
// sharing a commit; bounding it prevents unbounded growth on very large
// files with many distinct commits.
const recentCommitsLimit = 256

// blameCommitInfo holds the cached metadata for a single commit SHA.
type blameCommitInfo struct {
	author, email, time, summary string
}

// parseGitBlame parses a `git blame --line-porcelain` output string into
// BlameLine[]. It is a thin wrapper around parseGitBlameStream kept for
// tests that pass literal strings. Production callers should use the
// streaming variant directly to avoid buffering the whole output.
func parseGitBlame(output string) []BlameLine {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	result := parseGitBlameStream(scanner)
	logGitDebugError("git: parse in-memory blame failed", scanner.Err())
	return result
}

// finishGitBlameStream always reaps the child after parsing. Reading Scanner.Err
// before wait lets callers distinguish parser failures from expected blame
// command failures while wait closes stdout before reaping the process.
func finishGitBlameStream(
	scanner *bufio.Scanner,
	wait func() error,
) ([]BlameLine, error, error) {
	result := parseGitBlameStream(scanner)
	var parseErr error
	if err := scanner.Err(); err != nil {
		parseErr = fmt.Errorf("parse git blame: %w", err)
	}
	return result, parseErr, wait()
}

// parseGitBlameStream parses `git blame --line-porcelain` output from a
// bufio.Scanner into BlameLine[] without buffering the entire output.
//
// Porcelain block layout (per blame line):
//  1. header:  "<40-sha> <orig-line> <final-line> [<num>]"
//  2. metadata: "author", "author-mail", "author-time", "summary", ...
//  3. content:  "\t<line content>"
//
// Because metadata comes AFTER the header, we defer emitting the BlameLine
// until the content line ("\t...") — at which point all metadata for that
// block has been parsed. This also fixes a latent bug in the previous
// implementation where the first occurrence of a commit had empty
// Author/Email/Time/Summary fields.
//
// The per-commit metadata cache is bounded to recentCommitsLimit entries
// (FIFO eviction) so a file with millions of distinct commits cannot grow
// the map unbounded.
func parseGitBlameStream(scanner *bufio.Scanner) []BlameLine {
	var result []BlameLine
	commitInfo := make(map[string]blameCommitInfo, recentCommitsLimit)
	commitOrder := make([]string, 0, recentCommitsLimit)

	var (
		pendingCommit  string
		pendingLine    int
		pendingAuthor  string
		pendingEmail   string
		pendingTime    string
		pendingSummary string
		seenHeader     bool
	)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		switch {
		case line[0] == '\t':
			// Content line — flush the pending BlameLine.
			if seenHeader {
				result = append(result, BlameLine{
					Line:    pendingLine,
					Commit:  shortSHA(pendingCommit),
					Author:  pendingAuthor,
					Date:    pendingTime,
					Content: line[1:],
					Email:   pendingEmail,
					Time:    pendingTime,
					Summary: pendingSummary,
				})
				seenHeader = false
			}
		case strings.HasPrefix(line, "author "):
			pendingAuthor = strings.TrimPrefix(line, "author ")
			updateBlameCache(commitInfo, &commitOrder, pendingCommit, func(i *blameCommitInfo) { i.author = pendingAuthor })
		case strings.HasPrefix(line, "author-mail "):
			mail := strings.TrimPrefix(line, "author-mail ")
			mail = strings.TrimPrefix(mail, "<")
			mail = strings.TrimSuffix(mail, ">")
			pendingEmail = mail
			updateBlameCache(commitInfo, &commitOrder, pendingCommit, func(i *blameCommitInfo) { i.email = mail })
		case strings.HasPrefix(line, "author-time "):
			tsStr := strings.TrimPrefix(line, "author-time ")
			// L-2: 直接使用标准库 strconv.Atoi,删除自定义 strconvAtoi 包装。
			if ts, err := strconv.Atoi(tsStr); err == nil {
				pendingTime = time.Unix(int64(ts), 0).UTC().Format(time.RFC3339)
				updateBlameCache(commitInfo, &commitOrder, pendingCommit, func(i *blameCommitInfo) { i.time = pendingTime })
			}
		case strings.HasPrefix(line, "summary "):
			pendingSummary = strings.TrimPrefix(line, "summary ")
			updateBlameCache(commitInfo, &commitOrder, pendingCommit, func(i *blameCommitInfo) { i.summary = pendingSummary })
		default:
			// Header line: <sha> <orig-line> <final-line> [<num>].
			// Validate the hash and line number so metadata such as a
			// multi-word "committer" value cannot be mistaken for a header.
			commit, finalLine, ok := parseGitBlameHeader(line)
			if !ok {
				continue
			}
			pendingCommit = commit
			pendingLine = finalLine
			if info, ok := commitInfo[pendingCommit]; ok {
				// Move to back of LRU order.
				pendingAuthor = info.author
				pendingEmail = info.email
				pendingTime = info.time
				pendingSummary = info.summary
				commitOrder = removeFromOrder(commitOrder, pendingCommit)
				commitOrder = append(commitOrder, pendingCommit)
			} else {
				pendingAuthor = ""
				pendingEmail = ""
				pendingTime = ""
				pendingSummary = ""
				// Bound cache size.
				if len(commitOrder) >= recentCommitsLimit {
					oldest := commitOrder[0]
					commitOrder = commitOrder[1:]
					delete(commitInfo, oldest)
				}
				commitOrder = append(commitOrder, pendingCommit)
				commitInfo[pendingCommit] = blameCommitInfo{}
			}
			seenHeader = true
		}
	}
	// Flush a trailing pending BlameLine if the output ended without a
	// content line (shouldn't happen with --line-porcelain but defensive).
	if seenHeader {
		result = append(result, BlameLine{
			Line:    pendingLine,
			Commit:  shortSHA(pendingCommit),
			Author:  pendingAuthor,
			Date:    pendingTime,
			Email:   pendingEmail,
			Time:    pendingTime,
			Summary: pendingSummary,
		})
	}
	return result
}

func parseGitBlameHeader(line string) (string, int, bool) {
	parts := strings.Fields(line)
	if len(parts) < 3 || !resolvedCommitRe.MatchString(parts[0]) {
		return "", 0, false
	}
	finalLine, err := strconv.Atoi(parts[2])
	if err != nil || finalLine <= 0 {
		return "", 0, false
	}
	return parts[0], finalLine, true
}

// updateBlameCache applies mutate to the cache entry for commit, evicting
// the oldest entry when the cache is full and the commit is new. It also
// moves the commit to the back of the LRU order slice.
func updateBlameCache(cache map[string]blameCommitInfo, order *[]string, commit string, mutate func(*blameCommitInfo)) {
	if commit == "" {
		return
	}
	info, ok := cache[commit]
	if !ok {
		if len(*order) >= recentCommitsLimit {
			oldest := (*order)[0]
			*order = (*order)[1:]
			delete(cache, oldest)
		}
		*order = append(*order, commit)
	}
	mutate(&info)
	cache[commit] = info
}

// removeFromOrder returns order with the first occurrence of v removed.
// It does not allocate when v is not present.
func removeFromOrder(order []string, v string) []string {
	for i, s := range order {
		if s == v {
			return append(order[:i], order[i+1:]...)
		}
	}
	return order
}

// shortSHA returns the first 8 characters of a SHA (or the whole string if
// shorter). Used for the Commit field of BlameLine.
func shortSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// readBlobContent reads the full content of the blob identified by h from the
// repository's object store. Returns an empty string for a zero hash (missing
// side). Errors are returned so ListMergeConflicts never presents unreadable
// conflict content as an empty side.
func readBlobContent(repo *git.Repository, h plumbing.Hash) (string, error) {
	if h.IsZero() {
		return "", nil
	}
	blob, err := repo.BlobObject(h)
	if err != nil {
		return "", err
	}
	r, err := blob.Reader()
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := r.Close(); closeErr != nil {
			logGitDebugError("git: close conflict blob reader failed", closeErr)
		}
	}()
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// gitignoreGo is the .gitignore template for Go projects.
const gitignoreGo = `# Go
*.exe
*.exe~
*.dll
*.so
*.dylib
*.test
*.out
go.work
go.work.sum
vendor/
.air.toml
`

// gitignoreTypeScript is the .gitignore template for TypeScript projects.
const gitignoreTypeScript = `# TypeScript / Node
node_modules/
dist/
build/
*.js.map
*.tsbuildinfo
.env
.env.*
!.env.example
`

// gitignoreJavaScript is the .gitignore template for JavaScript projects.
const gitignoreJavaScript = `# JavaScript / Node
node_modules/
dist/
build/
.env
.env.*
!.env.example
`

// gitignoreGeneral is the OS/IDE .gitignore template applicable to any project.
const gitignoreGeneral = `# OS files
.DS_Store
Thumbs.db
desktop.ini

# IDE files
.idea/
.vscode/*
!.vscode/settings.json
!.vscode/tasks.json
!.vscode/launch.json
!.vscode/extensions.json
*.swp
*.swo
*~
`

// ---------------------------------------------------------------------------
// 优先级 3: Git Stash / Tag / Amend
// ---------------------------------------------------------------------------

// StashEntry 表示一条 git stash 记录，对应 `git stash list` 的一行输出。
type StashEntry struct {
	Ref        string `json:"ref"`        // stash 引用名，如 stash@{0}
	Message    string `json:"message"`    // stash 提交信息
	Date       string `json:"date"`       // RFC3339 作者时间
	Author     string `json:"author"`     // stash 作者名
	CommitHash string `json:"commitHash"` // 兼容现有调用方的完整 SHA
}

// TagEntry 表示一个 git tag，对应 `git tag -l` 的一行输出。
type TagEntry struct {
	Name       string `json:"name"`       // 标签名（短名，不含 refs/tags/ 前缀）
	CommitHash string `json:"commitHash"` // 标签指向的提交 SHA
	Message    string `json:"message"`    // 标签信息（annotated tag 的 message）
}

// stashRefRe 匹配合法的 stash 引用名（stash@{N}），拒绝 shell 元字符和
// 路径穿越，防止通过 stashRef 参数注入。
var stashRefRe = regexp.MustCompile(`^stash@\{\d+\}$`)

// tagNameRe 匹配合法的 git 标签名：以字母或数字开头，后续可含字母、数字、
// 下划线、连字符、点。拒绝空格、shell 元字符和路径穿越。
var tagNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// remoteNameRe 匹配合法的 git 远程仓库名，与 branchNameRe 保持一致。
var remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// validateStashRef 校验 stashRef 是否为合法的 stash 引用（stash@{N}）。
func validateStashRef(stashRef string) error {
	if !stashRefRe.MatchString(stashRef) {
		return fmt.Errorf("invalid stash reference %q: expected format stash@{N}", stashRef)
	}
	return nil
}

// validateTagName 校验 tagName 是否为合法的 git 标签名。
func validateTagName(name string) error {
	if !tagNameRe.MatchString(name) {
		return fmt.Errorf("invalid tag name %q: must start with alphanumeric and contain only alphanumeric, hyphen, underscore, or period", name)
	}
	return nil
}

// validateRemoteName 校验 remote 是否为合法的 git 远程仓库名。
func validateRemoteName(remote string) error {
	if !remoteNameRe.MatchString(remote) {
		return fmt.Errorf("invalid remote name %q: must contain only alphanumeric, -, _, ., /", remote)
	}
	return nil
}

// commitHashRe 匹配 git commit hash：7-40 位十六进制字符。
// 7 位是 git 短 hash 的最小长度，40 位是完整 SHA-1。
var commitHashRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// validateCommitHash 校验 commitHash 是否为合法的 git commit hash。
// F-4 (prompt-2.md): CherryPick / RevertCommit / BisectStart 使用。
func validateCommitHash(hash string) error {
	if !commitHashRe.MatchString(hash) {
		return fmt.Errorf("invalid commit hash %q: must be 7-40 hexadecimal characters", hash)
	}
	return nil
}

// submodulePathRe 匹配合法的子模块相对路径：允许字母数字、-、_、.、/，
// 但拒绝 ".."（路径穿越）和绝对路径。
var submodulePathRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// validateSubmodulePath 校验子模块路径：非空、无路径穿越、在 workspace 内。
func (g *GitService) validateSubmodulePath(root, subPath string) error {
	if subPath == "" {
		return errors.New("submodule path cannot be empty")
	}
	if filepath.IsAbs(subPath) {
		return fmt.Errorf("submodule path must be relative: %q", subPath)
	}
	cleaned := filepath.Clean(subPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("submodule path traversal blocked: %q", subPath)
	}
	if !submodulePathRe.MatchString(filepath.ToSlash(cleaned)) {
		return fmt.Errorf("invalid submodule path %q: must contain only alphanumeric, -, _, ., /", subPath)
	}
	// 深度防御：校验最终路径在 workspace 内。
	return g.validatePath(filepath.Join(root, cleaned))
}

// validateSubmoduleURL 校验子模块 URL：拒绝 file:// 协议穿越。
// 允许 https://、git://、ssh://、git@host: 等常见格式。
func validateSubmoduleURL(url string) error {
	if url == "" {
		return errors.New("submodule URL cannot be empty")
	}
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "file://") {
		// file:// 协议可能被用于访问 workspace 外的本地仓库。
		return fmt.Errorf("file:// protocol is not allowed for submodule URL: %q", url)
	}
	if strings.ContainsRune(url, 0) {
		return errors.New("submodule URL contains null byte")
	}
	return nil
}

// validateMessage 校验提交/stash/标签信息：非空且不含 null 字节。
// 由于 runGit 使用 exec.Command（不经过 shell），shell 元字符本身不会
// 造成注入，但空信息或 null 字节会导致 git 行为异常。
func validateMessage(msg string) error {
	if strings.TrimSpace(msg) == "" {
		return errors.New("message cannot be empty")
	}
	if strings.ContainsRune(msg, 0) {
		return errors.New("message contains null byte")
	}
	return nil
}

// StashList 返回指定仓库中的所有 stash 记录。使用 unit separator 分隔
// 字段，避免普通提交信息中的竖线破坏解析。
func (g *GitService) StashList(repoPath string) ([]StashEntry, error) {
	if repoPath == "" {
		return nil, errors.New("repository path cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return nil, err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return nil, err
	}
	defer release()
	const fieldSep = "\x1f"
	out, err := g.runGit(repoPath, "stash", "list", "--format=%gd%x1f%H%x1f%an%x1f%aI%x1f%s")
	if err != nil {
		return nil, err
	}
	entries := make([]StashEntry, 0)
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, fieldSep, 5)
		if len(parts) != 5 {
			return nil, errors.New("parse git stash list: malformed record")
		}
		entries = append(entries, StashEntry{
			Ref:        parts[0],
			CommitHash: parts[1],
			Author:     parts[2],
			Date:       parts[3],
			Message:    parts[4],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse git stash list: %w", err)
	}
	return entries, nil
}

// StashCreate 保存指定仓库当前工作区的修改。
func (g *GitService) StashCreate(repoPath string, message string) error {
	if repoPath == "" {
		return errors.New("repository path cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return err
	}
	if err := validateMessage(message); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(repoPath, "stash", "push", "-m", message)
	return err
}

// StashPush 将当前工作区的修改保存到一个新的 stash 中。
func (g *GitService) StashPush(message string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	return g.StashCreate(root, message)
}

// StashPop 应用并移除指定的 stash。stashRef 必须形如 stash@{N}。
func (g *GitService) StashPop(stashRef string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateStashRef(stashRef); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "stash", "pop", stashRef)
	return err
}

// StashApply 应用指定的 stash 但不移除它。stashRef 必须形如 stash@{N}。
func (g *GitService) StashApply(repoPath string, stashRef string) error {
	if repoPath == "" {
		return errors.New("repository path cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return err
	}
	if err := validateStashRef(stashRef); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(repoPath, "stash", "apply", stashRef)
	return err
}

// StashDrop 移除指定的 stash。stashRef 必须形如 stash@{N}。
func (g *GitService) StashDrop(repoPath string, stashRef string) error {
	if repoPath == "" {
		return errors.New("repository path cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return err
	}
	if err := validateStashRef(stashRef); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(repoPath, "stash", "drop", stashRef)
	return err
}

// ListTags 返回工作区根仓库中的所有标签。
// 使用 `git tag -l --format=%(refname:short)|%(objectname)|%(subject)`。
func (g *GitService) ListTags() ([]TagEntry, error) {
	root := g.workspaceRootPath()
	if root == "" {
		return nil, errors.New("no workspace root set")
	}
	if err := g.validatePath(root); err != nil {
		return nil, err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return nil, err
	}
	defer release()
	out, err := g.runGit(root, "tag", "-l", "--format=%(refname:short)|%(objectname)|%(subject)")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return []TagEntry{}, nil
	}
	lines := strings.Split(out, "\n")
	entries := make([]TagEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		entries = append(entries, TagEntry{
			Name:       parts[0],
			CommitHash: parts[1],
			Message:    parts[2],
		})
	}
	return entries, nil
}

// CreateTag 在当前 HEAD 创建一个带注释的标签。
func (g *GitService) CreateTag(name, message string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateTagName(name); err != nil {
		return err
	}
	if err := validateMessage(message); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "tag", "-a", name, "-m", message)
	return err
}

// DeleteTag 删除指定的标签。
func (g *GitService) DeleteTag(name string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateTagName(name); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "tag", "-d", name)
	return err
}

// PushTags 将所有本地标签推送到指定的远程仓库。
func (g *GitService) PushTags(remote string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateRemoteName(remote); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "push", remote, "--tags")
	return err
}

// AmendCommit 使用给定的信息修订最近一次提交（git commit --amend -m）。
func (g *GitService) AmendCommit(message string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateMessage(message); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "commit", "--amend", "-m", message)
	return err
}

// ---------------------------------------------------------------------------
// F-4 (prompt-2.md): Submodule + Cherry-pick + Revert + Bisect
// ---------------------------------------------------------------------------

// SubmoduleInfo 描述一个子模块的状态。镜像 `git submodule status` 输出。
type SubmoduleInfo struct {
	// SHA 是子模块当前检出的 commit hash（短格式）。
	SHA string `json:"sha"`
	// Path 是子模块在工作区中的相对路径。
	Path string `json:"path"`
	// Name 是 .gitmodules 中定义的子模块名称（通常等于 Path）。
	Name string `json:"name"`
	// Branch 是子模块跟踪的分支（如未设置则为空）。
	Branch string `json:"branch,omitempty"`
	// URL 是子模块的远程仓库 URL。
	URL string `json:"url,omitempty"`
	// Initialized 表示子模块是否已初始化（有 .git 目录）。
	Initialized bool `json:"initialized"`
	// Modified 表示子模块是否有未提交的变更。
	Modified bool `json:"modified,omitempty"`
}

// SubmoduleAdd 添加一个子模块到当前工作区。
// F-4 (prompt-2.md): `git submodule add <url> <path>`
// 安全校验：url 非 file:// 协议、path 在 workspace 内。
func (g *GitService) SubmoduleAdd(url, path string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateSubmoduleURL(url); err != nil {
		return err
	}
	if err := g.validateSubmodulePath(root, path); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "submodule", "add", url, path)
	return err
}

// SubmoduleList 列出当前工作区中的所有子模块及其状态。
// F-4 (prompt-2.md): `git submodule status`
func (g *GitService) SubmoduleList() ([]SubmoduleInfo, error) {
	root := g.workspaceRootPath()
	if root == "" {
		return nil, errors.New("no workspace root set")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return nil, err
	}
	defer release()
	output, err := g.runGit(root, "submodule", "status")
	if err != nil {
		return nil, err
	}
	// 解析 .gitmodules 获取 name/branch/url 信息。
	modules, err := parseGitmodules(root)
	if err != nil {
		modules = map[string]SubmoduleInfo{}
	}
	var result []SubmoduleInfo
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		info := parseSubmoduleStatusLine(line)
		// 合并 .gitmodules 中的信息。
		if m, ok := modules[info.Path]; ok {
			info.Name = m.Name
			info.Branch = m.Branch
			info.URL = m.URL
		}
		result = append(result, info)
	}
	return result, nil
}

// SubmoduleUpdate 更新子模块。当 init 为 true 时，先初始化子模块再更新。
// F-4 (prompt-2.md): `git submodule update [--init]`
func (g *GitService) SubmoduleUpdate(init bool) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	args := []string{"submodule", "update"}
	if init {
		args = append(args, "--init")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, args...)
	return err
}

// SubmoduleDeinit 取消初始化指定路径的子模块。
// F-4 (prompt-2.md): `git submodule deinit -f <path>`
func (g *GitService) SubmoduleDeinit(path string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := g.validateSubmodulePath(root, path); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "submodule", "deinit", "-f", path)
	return err
}

// CherryPick 将指定 commit 的变更应用到当前分支。
// F-4 (prompt-2.md): `git cherry-pick <hash>`
// 安全校验：hash 格式为 7-40 位十六进制字符。
func (g *GitService) CherryPick(commitHash string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateCommitHash(commitHash); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "cherry-pick", commitHash)
	return err
}

// RevertCommit 撤销指定 commit 的变更，创建一个新的反向 commit。
// F-4 (prompt-2.md): `git revert --no-edit <hash>`
// 安全校验：hash 格式为 7-40 位十六进制字符。
func (g *GitService) RevertCommit(commitHash string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateCommitHash(commitHash); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "revert", "--no-edit", commitHash)
	return err
}

// BisectStart 启动二分查找，指定好的和坏的 commit。
// F-4 (prompt-2.md): `git bisect start <bad> <good>`
// 安全校验：good 和 bad hash 格式为 7-40 位十六进制字符。
func (g *GitService) BisectStart(good, bad string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateCommitHash(good); err != nil {
		return fmt.Errorf("invalid good commit: %w", err)
	}
	if err := validateCommitHash(bad); err != nil {
		return fmt.Errorf("invalid bad commit: %w", err)
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "bisect", "start", bad, good)
	return err
}

// BisectGood 标记当前 commit 为好的（不含 bug）。
// F-4 (prompt-2.md): `git bisect good`
func (g *GitService) BisectGood() error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "bisect", "good")
	return err
}

// BisectBad 标记当前 commit 为坏的（含 bug）。
// F-4 (prompt-2.md): `git bisect bad`
func (g *GitService) BisectBad() error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "bisect", "bad")
	return err
}

// BisectReset 结束二分查找，回到原始分支。
// F-4 (prompt-2.md): `git bisect reset`
func (g *GitService) BisectReset() error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "bisect", "reset")
	return err
}

// ---------------------------------------------------------------------------
// F-4 辅助函数
// ---------------------------------------------------------------------------

// parseSubmoduleStatusLine 解析 `git submodule status` 的单行输出。
// 行格式：<status_char><sha> <path> (<description>)
// status_char: ' '（未修改）、'+'（修改）、'-'（未初始化）、'U'（冲突）
func parseSubmoduleStatusLine(line string) SubmoduleInfo {
	info := SubmoduleInfo{}
	if len(line) < 1 {
		return info
	}
	// 首字符是状态标志。
	statusChar := line[0]
	switch statusChar {
	case '-':
		info.Initialized = false
	case '+':
		info.Initialized = true
		info.Modified = true
	case 'U':
		info.Initialized = true
		info.Modified = true
	default:
		info.Initialized = true
	}
	// 去掉首字符后按空格分割。
	rest := strings.TrimSpace(line[1:])
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) >= 1 {
		info.SHA = parts[0]
	}
	if len(parts) >= 2 {
		// path 后面可能有 (description)，去掉它。
		pathDesc := strings.TrimSpace(parts[1])
		if idx := strings.Index(pathDesc, "("); idx >= 0 {
			pathDesc = strings.TrimSpace(pathDesc[:idx])
		}
		info.Path = pathDesc
		info.Name = pathDesc
	}
	return info
}

// parseGitmodules 解析 .gitmodules 文件，返回按 path 索引的子模块信息。
func parseGitmodules(root string) (map[string]SubmoduleInfo, error) {
	content, err := os.ReadFile(filepath.Join(root, ".gitmodules"))
	if err != nil {
		return nil, err
	}
	result := make(map[string]SubmoduleInfo)
	var currentPath string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[submodule ") {
			// [submodule "name"]
			name := strings.TrimPrefix(line, "[submodule ")
			name = strings.TrimSuffix(name, "]")
			name = strings.Trim(name, "\"")
			currentPath = ""
			if _, ok := result[currentPath]; !ok {
				result[currentPath] = SubmoduleInfo{Name: name}
			} else {
				info := result[currentPath]
				info.Name = name
				result[currentPath] = info
			}
			continue
		}
		if strings.HasPrefix(line, "path") && currentPath == "" {
			// path = sub/path
			val := strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
			currentPath = val
			info := result[""]
			info.Path = val
			delete(result, "")
			result[currentPath] = info
		} else if strings.HasPrefix(line, "path") {
			val := strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
			currentPath = val
			if info, ok := result[currentPath]; ok {
				info.Path = val
				result[currentPath] = info
			} else {
				result[currentPath] = SubmoduleInfo{Path: val}
			}
		} else if strings.HasPrefix(line, "url") && currentPath != "" {
			val := strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
			info := result[currentPath]
			info.URL = val
			result[currentPath] = info
		} else if strings.HasPrefix(line, "branch") && currentPath != "" {
			val := strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
			info := result[currentPath]
			info.Branch = val
			result[currentPath] = info
		}
	}
	return result, nil
}
