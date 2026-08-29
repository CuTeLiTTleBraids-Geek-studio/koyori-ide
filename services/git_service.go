package services

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

// GitFileChange represents a single changed file in the working tree.
type GitFileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	// Staged is backend-owned: true when the status comes from the index, false
	// for worktree/untracked changes. A file with both index and worktree
	// changes is emitted twice rather than guessed by the renderer.
	Staged bool `json:"staged"`
	// OldPath is set only on proven staged renames (identical HEAD blob and
	// index blob, P1-04): Path is the new name, OldPath the deleted name. An
	// unproven delete+add pair stays as two rows with OldPath empty.
	OldPath string `json:"oldPath,omitempty"`
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
