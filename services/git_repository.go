package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Repository discovery, status, branches, remotes, and core mutations.
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
	// P1-04: prove staged renames before projecting rows. Only an identical
	// HEAD blob (deleted path) and index blob (added path) is a rename; any
	// content difference keeps the honest delete+add pair.
	renamed := detectStagedRenames(repo, st)
	renameNew := make(map[string]string, len(renamed))
	renameOld := make(map[string]bool, len(renamed))
	for _, pair := range renamed {
		renameNew[pair.newPath] = pair.oldPath
		renameOld[pair.oldPath] = true
	}
	changes := make([]GitFileChange, 0, len(st))
	for path, s := range st {
		stagedRenameRow := (renameOld[path] && s.Staging == git.Deleted) ||
			(len(renameNew[path]) > 0 && s.Staging == git.Added)
		if s.Staging != git.Unmodified && s.Staging != git.Untracked && !stagedRenameRow {
			changes = append(changes, GitFileChange{
				Path:   path,
				Status: statusToString(s.Staging),
				Staged: true,
			})
		}
		if s.Worktree != git.Unmodified {
			changes = append(changes, GitFileChange{
				Path:   path,
				Status: statusToString(s.Worktree),
				Staged: false,
			})
		}
	}
	for _, pair := range renamed {
		changes = append(changes, GitFileChange{
			Path:    pair.newPath,
			Status:  statusToString(git.Renamed),
			Staged:  true,
			OldPath: pair.oldPath,
		})
	}
	return changes, nil
}

// renamePair is a proven staged rename: the deleted path's HEAD blob equals
// the added path's index blob.
type renamePair struct {
	oldPath string
	newPath string
}

// detectStagedRenames pairs staged deletions with staged additions whose blob
// hashes match the HEAD blob of the deleted path. Detection is best-effort:
// on any HEAD/index read failure the rename stays unprojected (delete+add
// rows) instead of failing GetStatus.
func detectStagedRenames(repo *git.Repository, st git.Status) []renamePair {
	var deletions []string
	var additions []string
	for path, s := range st {
		switch s.Staging {
		case git.Deleted:
			deletions = append(deletions, path)
		case git.Added:
			additions = append(additions, path)
		}
	}
	if len(deletions) == 0 || len(additions) == 0 {
		return nil
	}
	sort.Strings(deletions)
	sort.Strings(additions)
	tree := headTree(repo)
	if tree == nil {
		return nil
	}
	idx, err := repo.Storer.Index()
	if err != nil {
		logGitDebugError("git: rename detection index unavailable", err)
		return nil
	}
	var pairs []renamePair
	used := make(map[string]bool, len(additions))
	for _, d := range deletions {
		file, err := tree.File(d)
		if err != nil {
			// Deleted path has no HEAD blob, so no rename can be proven.
			continue
		}
		oldHash := file.Hash
		for _, a := range additions {
			if used[a] {
				continue
			}
			entry, err := idx.Entry(a)
			if err != nil {
				continue
			}
			if entry.Hash == oldHash {
				pairs = append(pairs, renamePair{oldPath: d, newPath: a})
				used[a] = true
				break
			}
		}
	}
	return pairs
}

// headTree returns the HEAD commit tree, or nil when HEAD is unavailable
// (fresh repository): rename detection degrades to plain rows there.
func headTree(repo *git.Repository) *object.Tree {
	headRef, err := repo.Head()
	if err != nil {
		logGitDebugError("git: rename detection head unavailable", err)
		return nil
	}
	commit, err := repo.CommitObject(headRef.Hash())
	if err != nil {
		logGitDebugError("git: rename detection commit unavailable", err)
		return nil
	}
	tree, err := commit.Tree()
	if err != nil {
		logGitDebugError("git: rename detection tree unavailable", err)
		return nil
	}
	return tree
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
