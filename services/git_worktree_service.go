package services

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const gitWorktreeCommandTimeout = 30 * time.Second

// GitWorktreeCommandRunner executes a git command in repoPath. It is exposed
// so embedders can provide a controlled runner and tests do not need to spawn
// real processes.
type GitWorktreeCommandRunner interface {
	RunGit(ctx context.Context, repoPath string, args ...string) (string, error)
}

// GitWorktreeCommandRunnerFunc adapts a function to GitWorktreeCommandRunner.
type GitWorktreeCommandRunnerFunc func(ctx context.Context, repoPath string, args ...string) (string, error)

// RunGit implements GitWorktreeCommandRunner.
func (f GitWorktreeCommandRunnerFunc) RunGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	if f == nil {
		return "", fmt.Errorf("git worktree command runner is nil: %w", ErrInvalidInput)
	}
	return f(ctx, repoPath, args...)
}

// GitWorktreeExecRunner executes git directly without depending on GitService
// implementation details.
type GitWorktreeExecRunner struct{}

// RunGit implements GitWorktreeCommandRunner.
func (GitWorktreeExecRunner) RunGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	commandArgs := make([]string, 0, len(args)+2)
	commandArgs = append(commandArgs, "-C", repoPath)
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	cmd.Env = gitWorktreeCommandEnv()
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// GitWorktreeBranchProvider is the public GitService boundary used by the
// default repository validator. *GitService satisfies this interface through
// its exported ListBranches method, which supports both regular and bare
// repositories with a valid HEAD.
type GitWorktreeBranchProvider interface {
	ListBranches(repoPath string) ([]BranchRef, error)
}

// GitWorktreeRepositoryValidator authorizes and validates a repository before
// any worktree command is executed.
type GitWorktreeRepositoryValidator interface {
	ValidateRepository(repoPath string) error
}

// GitWorktreeRepositoryValidatorFunc adapts a function to a repository
// validator.
type GitWorktreeRepositoryValidatorFunc func(repoPath string) error

// ValidateRepository implements GitWorktreeRepositoryValidator.
func (f GitWorktreeRepositoryValidatorFunc) ValidateRepository(repoPath string) error {
	if f == nil {
		return fmt.Errorf("git worktree repository validator is nil: %w", ErrInvalidInput)
	}
	return f(repoPath)
}

// GitWorktreeGitServiceAdapter validates repositories using only GitService's
// exported ListBranches method. This keeps the worktree module independent from
// GitService's private command runner and workspace fields.
type GitWorktreeGitServiceAdapter struct {
	provider GitWorktreeBranchProvider
}

// NewGitWorktreeGitServiceAdapter creates a public-API-only GitService adapter.
func NewGitWorktreeGitServiceAdapter(provider GitWorktreeBranchProvider) *GitWorktreeGitServiceAdapter {
	if gitWorktreeDependencyIsNil(provider) {
		provider = nil
	}
	return &GitWorktreeGitServiceAdapter{provider: provider}
}

// ValidateRepository implements GitWorktreeRepositoryValidator.
func (a *GitWorktreeGitServiceAdapter) ValidateRepository(repoPath string) error {
	if a == nil || gitWorktreeDependencyIsNil(a.provider) {
		return fmt.Errorf("git service is not injected: %w", ErrInvalidInput)
	}
	if _, err := a.provider.ListBranches(repoPath); err != nil {
		return fmt.Errorf("validate repository through GitService: %w", err)
	}
	return nil
}

// GitWorktreeDependencies configures the module without exposing GitService
// internals. SafeRoots are user-approved directories in which AddWorktree may
// create new worktrees; they do not grant mutation access to arbitrary paths.
type GitWorktreeDependencies struct {
	Runner              GitWorktreeCommandRunner
	RepositoryValidator GitWorktreeRepositoryValidator
	SafeRoots           []string
}

// GitWorktreeService provides isolated worktree operations. Mutations of an
// existing worktree are authorized against `git worktree list --porcelain`;
// SafeRoots apply only when adding a new path.
type GitWorktreeService struct {
	runner              GitWorktreeCommandRunner
	repositoryValidator GitWorktreeRepositoryValidator
	addSafeRoots        []string
	wsCtx               *WorkspaceContext
}

// WorktreeInfo describes one record from `git worktree list --porcelain`.
type WorktreeInfo struct {
	Path     string `json:"path"`
	HEAD     string `json:"head"`
	Branch   string `json:"branch"`
	Bare     bool   `json:"bare"`
	Locked   string `json:"locked,omitempty"`
	Prunable bool   `json:"prunable,omitempty"`
}

// AddWorktreeOptions controls optional flags for AddWorktree.
type AddWorktreeOptions struct {
	NewBranch  string `json:"newBranch,omitempty"`
	Detach     bool   `json:"detach,omitempty"`
	Force      bool   `json:"force,omitempty"`
	NoCheckout bool   `json:"noCheckout,omitempty"`
	// AllowOutsideRepository is retained for source compatibility only. A
	// renderer boolean is not an authorization boundary; trusted safe roots and
	// the shared WorkspaceContext exclusively decide where a worktree may live.
	AllowOutsideRepository bool `json:"-"`
}

// NewGitWorktreeService creates a worktree service backed by GitService.
func NewGitWorktreeService(gitSvc *GitService) *GitWorktreeService {
	service, _ := NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{
		RepositoryValidator: gitWorktreeValidatorForGitService(gitSvc),
	})
	return service
}

// NewGitWorktreeServiceWithRunner creates a worktree service with an injected
// command runner. Repository validation still uses GitService's public API.
func NewGitWorktreeServiceWithRunner(gitSvc *GitService, runner GitWorktreeCommandRunner) *GitWorktreeService {
	service, _ := NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{
		Runner:              runner,
		RepositoryValidator: gitWorktreeValidatorForGitService(gitSvc),
	})
	return service
}

// NewGitWorktreeServiceWithSafeRoots creates a GitService-backed worktree
// service with explicit user-approved roots for AddWorktree.
func NewGitWorktreeServiceWithSafeRoots(gitSvc *GitService, safeRoots []string) (*GitWorktreeService, error) {
	return NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{
		RepositoryValidator: gitWorktreeValidatorForGitService(gitSvc),
		SafeRoots:           safeRoots,
	})
}

func gitWorktreeValidatorForGitService(gitSvc *GitService) GitWorktreeRepositoryValidator {
	if gitSvc == nil {
		return nil
	}
	return NewGitWorktreeGitServiceAdapter(gitSvc)
}

// NewGitWorktreeServiceWithDependencies creates a worktree service from public
// dependencies. A nil Runner selects GitWorktreeExecRunner.
func NewGitWorktreeServiceWithDependencies(deps GitWorktreeDependencies) (*GitWorktreeService, error) {
	roots, err := canonicalizeGitWorktreeSafeRoots(deps.SafeRoots)
	if err != nil {
		return nil, err
	}
	runner := deps.Runner
	if runner == nil {
		runner = GitWorktreeExecRunner{}
	}
	return &GitWorktreeService{
		runner:              runner,
		repositoryValidator: deps.RepositoryValidator,
		addSafeRoots:        roots,
	}, nil
}

// setWorkspaceContext binds repository and target authorization to the same
// workspace identity used by ProjectService.
//
//wails:ignore
func (s *GitWorktreeService) setWorkspaceContext(ctx *WorkspaceContext) {
	if s != nil {
		s.wsCtx = ctx
	}
}

// ListWorktrees lists all linked worktrees for repoPath.
func (s *GitWorktreeService) ListWorktrees(ctx context.Context, repoPath string) ([]WorktreeInfo, error) {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return nil, err
	}
	return s.listWorktrees(ctx, repo)
}

func (s *GitWorktreeService) listWorktrees(ctx context.Context, repoPath string) ([]WorktreeInfo, error) {
	output, err := s.runGit(ctx, repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, worktreeCommandError("list worktrees", output, err)
	}
	worktrees, err := parseGitWorktreePorcelain(output)
	if err != nil {
		return nil, fmt.Errorf("parse worktree list: %w", err)
	}
	return worktrees, nil
}

// AddWorktree creates a linked worktree. Relative paths are resolved from the
// repository directory. A new worktree must remain under repoPath or under the
// user-approved safe roots configured on this service.
func (s *GitWorktreeService) AddWorktree(
	ctx context.Context,
	repoPath, path, branchOrCommit string,
	opts AddWorktreeOptions,
) error {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return err
	}
	target, err := s.validateAddWorktreePath(repo, path)
	if err != nil {
		return err
	}
	if opts.NewBranch != "" {
		if strings.TrimSpace(opts.NewBranch) == "" {
			return fmt.Errorf("new branch must not be blank: %w", ErrInvalidInput)
		}
		if err := validateWorktreeArgument("new branch", opts.NewBranch); err != nil {
			return err
		}
	}
	if opts.Detach && opts.NewBranch != "" {
		return fmt.Errorf("new branch and detach options are mutually exclusive: %w", ErrInvalidInput)
	}
	if branchOrCommit != "" {
		if strings.TrimSpace(branchOrCommit) == "" {
			return fmt.Errorf("branch or commit must not be blank: %w", ErrInvalidInput)
		}
		if err := validateWorktreeArgument("branch or commit", branchOrCommit); err != nil {
			return err
		}
	}

	args := []string{"worktree", "add"}
	if opts.NewBranch != "" {
		args = append(args, "-b", opts.NewBranch)
	}
	if opts.Detach {
		args = append(args, "--detach")
	}
	if opts.Force {
		args = append(args, "--force")
	}
	if opts.NoCheckout {
		args = append(args, "--no-checkout")
	}
	args = append(args, "--", target)
	if branchOrCommit != "" {
		args = append(args, branchOrCommit)
	}
	output, err := s.runGit(ctx, repo, args...)
	if err != nil {
		return worktreeCommandError("add worktree", output, err)
	}
	return nil
}

// RemoveWorktree removes a linked worktree.
func (s *GitWorktreeService) RemoveWorktree(ctx context.Context, repoPath, path string, force bool) error {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return err
	}
	target, err := s.validateRegisteredWorktreePath(ctx, repo, path, false)
	if err != nil {
		return err
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--", target)
	output, err := s.runGit(ctx, repo, args...)
	if err != nil {
		return worktreeCommandError("remove worktree", output, err)
	}
	return nil
}

// PruneWorktrees removes stale worktree administrative data. In dry-run mode
// git reports what would be removed without changing repository state.
func (s *GitWorktreeService) PruneWorktrees(ctx context.Context, repoPath string, dryRun bool) ([]string, error) {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return nil, err
	}
	args := []string{"worktree", "prune"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, "--verbose")
	output, err := s.runGit(ctx, repo, args...)
	if err != nil {
		return nil, worktreeCommandError("prune worktrees", output, err)
	}
	return parseGitWorktreePruneOutput(output), nil
}

// LockWorktree prevents a linked worktree from being pruned or moved.
func (s *GitWorktreeService) LockWorktree(ctx context.Context, repoPath, path, reason string) error {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return err
	}
	target, err := s.validateRegisteredWorktreePath(ctx, repo, path, true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		reason = ""
	}
	if err := validateWorktreeArgument("lock reason", reason); err != nil {
		return err
	}
	args := []string{"worktree", "lock"}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	args = append(args, "--", target)
	output, err := s.runGit(ctx, repo, args...)
	if err != nil {
		return worktreeCommandError("lock worktree", output, err)
	}
	return nil
}

// UnlockWorktree allows a previously locked linked worktree to be maintained.
func (s *GitWorktreeService) UnlockWorktree(ctx context.Context, repoPath, path string) error {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return err
	}
	target, err := s.validateRegisteredWorktreePath(ctx, repo, path, true)
	if err != nil {
		return err
	}
	output, err := s.runGit(ctx, repo, "worktree", "unlock", "--", target)
	if err != nil {
		return worktreeCommandError("unlock worktree", output, err)
	}
	return nil
}

// MoveWorktree relocates a registered linked worktree to an authorized target.
func (s *GitWorktreeService) MoveWorktree(
	ctx context.Context,
	repoPath, oldPath, newPath string,
	force bool,
) error {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return err
	}
	oldTarget, err := s.validateRegisteredWorktreePath(ctx, repo, oldPath, false)
	if err != nil {
		return err
	}
	newTarget, err := s.validateMoveWorktreePath(repo, newPath)
	if err != nil {
		return err
	}
	if worktreePathsEqual(oldTarget, newTarget) {
		return fmt.Errorf("new worktree path must differ from the current path: %w", ErrInvalidInput)
	}

	args := []string{"worktree", "move"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--", oldTarget, newTarget)
	output, err := s.runGit(ctx, repo, args...)
	if err != nil {
		return worktreeCommandError("move worktree", output, err)
	}
	return nil
}

func (s *GitWorktreeService) validateMoveWorktreePath(
	repoPath,
	path string,
) (string, error) {
	target, err := canonicalizeGitWorktreePath(repoPath, path)
	if err != nil {
		return "", err
	}
	if worktreePathsEqual(repoPath, target) {
		return "", fmt.Errorf("new worktree path must not be the repository itself: %w", ErrInvalidInput)
	}
	for _, root := range s.trustedTargetRoots(repoPath) {
		inside, err := worktreePathInsideRoot(root, target)
		if err != nil {
			return "", fmt.Errorf("validate new worktree path: %w", err)
		}
		if inside {
			return target, nil
		}
	}
	return "", fmt.Errorf(
		"new worktree path %q is outside configured safe roots: %w",
		path,
		ErrInvalidInput,
	)
}

func (s *GitWorktreeService) runGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("git worktree service is nil: %w", ErrInvalidInput)
	}
	if s.runner == nil {
		return "", fmt.Errorf("git worktree command runner is not injected: %w", ErrInvalidInput)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	commandCtx, cancel := context.WithTimeout(ctx, gitWorktreeCommandTimeout)
	defer cancel()
	return s.runner.RunGit(commandCtx, repoPath, args...)
}

func (s *GitWorktreeService) validateRepoPath(repoPath string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("git worktree service is nil: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(repoPath) == "" {
		return "", fmt.Errorf("repository path is required: %w", ErrInvalidInput)
	}
	if strings.IndexByte(repoPath, 0) >= 0 {
		return "", fmt.Errorf("repository path contains a NUL byte: %w", ErrInvalidInput)
	}
	cleaned := filepath.Clean(repoPath)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("repository path must be absolute: %w", ErrInvalidInput)
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("validate repository path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path is not a directory: %w", ErrInvalidInput)
	}
	canonical, err := resolveWorktreePath(abs)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	if s.wsCtx != nil {
		root, err := s.wsCtx.RequireRoot()
		if err != nil {
			return "", err
		}
		if _, err := ValidatePathWithinRoot(root, canonical); err != nil {
			return "", fmt.Errorf("repository is outside the active workspace: %w", ErrNotAllowed)
		}
	}
	if s.repositoryValidator == nil {
		return "", fmt.Errorf("git worktree repository validator is not injected: %w", ErrInvalidInput)
	}
	if err := s.repositoryValidator.ValidateRepository(canonical); err != nil {
		return "", fmt.Errorf("validate repository path: %w", err)
	}
	return canonical, nil
}

func (s *GitWorktreeService) validateAddWorktreePath(
	repoPath,
	path string,
) (string, error) {
	target, err := canonicalizeGitWorktreePath(repoPath, path)
	if err != nil {
		return "", err
	}
	if worktreePathsEqual(repoPath, target) {
		return "", fmt.Errorf("worktree path must not be the repository itself: %w", ErrInvalidInput)
	}

	for _, root := range s.trustedTargetRoots(repoPath) {
		inside, err := worktreePathInsideRoot(root, target)
		if err != nil {
			return "", fmt.Errorf("validate worktree path: %w", err)
		}
		if inside {
			return target, nil
		}
	}
	return "", fmt.Errorf("worktree path %q is outside the repository and configured safe roots: %w", path, ErrInvalidInput)
}

func (s *GitWorktreeService) trustedTargetRoots(repoPath string) []string {
	roots := make([]string, 0, len(s.addSafeRoots)+2)
	roots = append(roots, repoPath)
	roots = append(roots, s.addSafeRoots...)
	if s.wsCtx != nil {
		if root := s.wsCtx.Root(); root != "" {
			roots = append(roots, root)
		}
	}
	return roots
}

func (s *GitWorktreeService) validateRegisteredWorktreePath(
	ctx context.Context,
	repoPath, path string,
	allowCurrent bool,
) (string, error) {
	target, err := canonicalizeGitWorktreePath(repoPath, path)
	if err != nil {
		return "", err
	}
	if worktreePathsEqual(repoPath, target) && !allowCurrent {
		return "", fmt.Errorf("worktree path must not be the repository itself: %w", ErrInvalidInput)
	}
	worktrees, err := s.listWorktrees(ctx, repoPath)
	if err != nil {
		return "", err
	}
	for _, worktree := range worktrees {
		registered, err := canonicalizeGitWorktreePath(repoPath, worktree.Path)
		if err != nil {
			return "", fmt.Errorf("validate registered worktree path %q: %w", worktree.Path, err)
		}
		if worktreePathsEqual(target, registered) {
			return registered, nil
		}
	}
	return "", fmt.Errorf("worktree path %q is not registered for repository %q: %w", path, repoPath, ErrInvalidInput)
}

func canonicalizeGitWorktreePath(repoPath, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("worktree path is required: %w", ErrInvalidInput)
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("worktree path contains a NUL byte: %w", ErrInvalidInput)
	}
	if strings.ContainsAny(path, "\r\n") {
		return "", fmt.Errorf("worktree path contains a line break: %w", ErrInvalidInput)
	}
	if containsWorktreeParentTraversal(path) {
		return "", fmt.Errorf("worktree path contains parent traversal: %w", ErrInvalidInput)
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(repoPath, cleaned)
	}
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("worktree path must resolve to an absolute path: %w", ErrInvalidInput)
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve worktree path: %w", err)
	}
	resolved, err := resolveWorktreePath(abs)
	if err != nil {
		return "", fmt.Errorf("resolve worktree path: %w", err)
	}
	return resolved, nil
}

func containsWorktreeParentTraversal(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return true
		}
	}
	return false
}

func worktreePathInsideRoot(root, target string) (bool, error) {
	resolvedRoot, err := resolveWorktreePath(root)
	if err != nil {
		return false, err
	}
	resolvedTarget, err := resolveWorktreePath(target)
	if err != nil {
		return false, err
	}
	comparisonRoot := worktreeComparisonPath(resolvedRoot)
	comparisonTarget := worktreeComparisonPath(resolvedTarget)
	rel, err := filepath.Rel(comparisonRoot, comparisonTarget)
	if err != nil {
		return false, err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, nil
	}
	return true, nil
}

func worktreeComparisonPath(path string) string {
	cleaned := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

func worktreePathsEqual(left, right string) bool {
	return worktreeComparisonPath(left) == worktreeComparisonPath(right)
}

func gitWorktreeDependencyIsNil(dependency any) bool {
	if dependency == nil {
		return true
	}
	value := reflect.ValueOf(dependency)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func canonicalizeGitWorktreeSafeRoots(roots []string) ([]string, error) {
	canonical := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			return nil, fmt.Errorf("safe root is required: %w", ErrInvalidInput)
		}
		if strings.IndexByte(root, 0) >= 0 {
			return nil, fmt.Errorf("safe root contains a NUL byte: %w", ErrInvalidInput)
		}
		cleaned := filepath.Clean(root)
		if !filepath.IsAbs(cleaned) {
			return nil, fmt.Errorf("safe root must be absolute: %w", ErrInvalidInput)
		}
		abs, err := filepath.Abs(cleaned)
		if err != nil {
			return nil, fmt.Errorf("resolve safe root: %v: %w", err, ErrInvalidInput)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("validate safe root %q: %v: %w", root, err, ErrInvalidInput)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("safe root %q is not a directory: %w", root, ErrInvalidInput)
		}
		resolved, err := resolveWorktreePath(abs)
		if err != nil {
			return nil, fmt.Errorf("resolve safe root %q: %v: %w", root, err, ErrInvalidInput)
		}
		duplicate := false
		for _, existing := range canonical {
			if worktreePathsEqual(existing, resolved) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			canonical = append(canonical, resolved)
		}
	}
	return canonical, nil
}

func gitWorktreeCommandEnv() []string {
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	if value := os.Getenv("PATH"); value != "" {
		env = append(env, "PATH="+value)
	}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"USERPROFILE", "SYSTEMROOT", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP"} {
			if value := os.Getenv(key); value != "" {
				env = append(env, key+"="+value)
			}
		}
	} else if value := os.Getenv("HOME"); value != "" {
		env = append(env, "HOME="+value)
	}
	for _, key := range []string{"LANG", "LC_ALL"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

// resolveWorktreePath resolves symlinks in the deepest existing ancestor and
// then rejoins missing path components. This closes the nested-missing-path
// gap left by resolving only the immediate parent of a new directory.
func resolveWorktreePath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	cursor := abs
	missing := make([]string, 0, 4)
	for {
		_, err := os.Lstat(cursor)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(cursor)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return "", err
		}
		missing = append(missing, filepath.Base(cursor))
		cursor = parent
	}
}

func validateWorktreeArgument(name, value string) error {
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s contains a NUL byte: %w", name, ErrInvalidInput)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s contains a line break: %w", name, ErrInvalidInput)
	}
	return nil
}

func worktreeCommandError(operation, output string, err error) error {
	detail := strings.TrimSpace(output)
	if detail == "" || strings.Contains(err.Error(), detail) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w: %s", operation, err, detail)
}

func parseGitWorktreePruneOutput(output string) []string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	entries := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entries = append(entries, strings.TrimPrefix(line, "Removing "))
	}
	return entries
}

func parseGitWorktreePorcelain(output string) ([]WorktreeInfo, error) {
	worktrees := make([]WorktreeInfo, 0)
	var current *WorktreeInfo
	finish := func() error {
		if current == nil {
			return nil
		}
		if current.Path == "" {
			return fmt.Errorf("worktree record has an empty path: %w", ErrInvalidInput)
		}
		worktrees = append(worktrees, *current)
		current = nil
		return nil
	}

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := finish(); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNumber, err)
			}
			continue
		}

		key, value, hasValue := strings.Cut(line, " ")
		if key == "worktree" {
			if err := finish(); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNumber, err)
			}
			if !hasValue || value == "" {
				return nil, fmt.Errorf("line %d: worktree path is missing: %w", lineNumber, ErrInvalidInput)
			}
			decoded, err := decodeWorktreePorcelainValue(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: decode worktree path: %w", lineNumber, err)
			}
			current = &WorktreeInfo{Path: decoded}
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("line %d: field %q appears before a worktree record: %w", lineNumber, key, ErrInvalidInput)
		}

		switch key {
		case "HEAD":
			if !hasValue || value == "" {
				return nil, fmt.Errorf("line %d: HEAD value is missing: %w", lineNumber, ErrInvalidInput)
			}
			decoded, err := decodeWorktreePorcelainValue(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: decode HEAD: %w", lineNumber, err)
			}
			current.HEAD = decoded
		case "branch":
			if !hasValue || value == "" {
				return nil, fmt.Errorf("line %d: branch value is missing: %w", lineNumber, ErrInvalidInput)
			}
			decoded, err := decodeWorktreePorcelainValue(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: decode branch: %w", lineNumber, err)
			}
			current.Branch = strings.TrimPrefix(decoded, "refs/heads/")
		case "detached":
			current.Branch = ""
		case "bare":
			current.Bare = true
		case "locked":
			if !hasValue || value == "" {
				// WorktreeInfo has no separate lock boolean, so retain a stable
				// non-empty marker for locks created without --reason.
				current.Locked = "locked"
				continue
			}
			decoded, err := decodeWorktreePorcelainValue(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: decode lock reason: %w", lineNumber, err)
			}
			current.Locked = decoded
		case "prunable":
			if hasValue && value != "" {
				if _, err := decodeWorktreePorcelainValue(value); err != nil {
					return nil, fmt.Errorf("line %d: decode prunable reason: %w", lineNumber, err)
				}
			}
			current.Prunable = true
		default:
			// Porcelain output can grow new fields; unknown fields are ignored.
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan porcelain output: %w", err)
	}
	if err := finish(); err != nil {
		return nil, err
	}
	return worktrees, nil
}

func decodeWorktreePorcelainValue(value string) (string, error) {
	if !strings.HasPrefix(value, "\"") {
		return value, nil
	}
	decoded, err := strconv.Unquote(value)
	if err != nil {
		return "", fmt.Errorf("invalid quoted value %q: %w", value, err)
	}
	return decoded, nil
}
