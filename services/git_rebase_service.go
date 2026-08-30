package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	gitRebaseDefaultTimeout = 60 * time.Second
	gitRebaseMaxActions     = 10000
	gitRebaseStateVersion   = 1
	gitRebaseStateFileName  = "koyori-ide-rebase-state.json"
	gitRebaseMaxStateSize   = 2 << 20

	gitRebasePhaseAwaitingApply = "awaitingApply"
	gitRebasePhaseApplying      = "applying"
	gitRebasePhaseReady         = "ready"
	gitRebasePhaseStopped       = "stopped"

	gitRebaseStopReasonCommandError  = "commandError"
	gitRebaseStopReasonSyntheticEdit = "syntheticEdit"
	gitRebaseStopReasonManual        = "manual"
)

var gitRebaseCommitPattern = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)
var gitRebaseHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var gitRebaseRepositoryLocks sync.Map

type gitRebasePersistentState struct {
	Version         int                `json:"version"`
	Phase           string             `json:"phase"`
	OrigHead        string             `json:"origHead"`
	Onto            string             `json:"onto"`
	UpstreamRef     string             `json:"upstreamRef"`
	TodoHash        string             `json:"todoHash"`
	PendingTodoHash string             `json:"pendingTodoHash,omitempty"`
	ExpectedCommits []string           `json:"expectedCommits"`
	RewordMessages  map[string]string  `json:"rewordMessages,omitempty"`
	Actions         []RebaseTodoAction `json:"actions"`
	StopReason      string             `json:"stopReason,omitempty"`
}

func lockGitRebaseRepository(repoPath string) func() {
	value, _ := gitRebaseRepositoryLocks.LoadOrStore(repoPath, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

// RebaseTodoAction is one instruction in an interactive rebase todo list.
type RebaseTodoAction struct {
	Action       string `json:"action"`
	CommitSHA    string `json:"commitSha"`
	ShortMessage string `json:"shortMessage"`
	LongMessage  string `json:"longMessage,omitempty"`
	AuthorName   string `json:"authorName,omitempty"`
	AuthorEmail  string `json:"authorEmail,omitempty"`
	Date         string `json:"date,omitempty"`
}

// GitRebaseStatus distinguishes a Koyori IDE-owned interactive rebase from an
// unrelated Git operation and exposes the persisted recovery phase.
type GitRebaseStatus struct {
	InProgress  bool               `json:"inProgress"`
	Owned       bool               `json:"owned"`
	Phase       string             `json:"phase,omitempty"`
	StopReason  string             `json:"stopReason,omitempty"`
	Upstream    string             `json:"upstream,omitempty"`
	UpstreamRef string             `json:"upstreamRef,omitempty"`
	OrigHead    string             `json:"origHead,omitempty"`
	Actions     []RebaseTodoAction `json:"actions,omitempty"`
}

// GitRebaseCommandRunner executes git without exposing GitService internals.
// extraEnv contains narrowly scoped overrides such as GIT_SEQUENCE_EDITOR.
type GitRebaseCommandRunner interface {
	RunGit(ctx context.Context, repoPath string, extraEnv []string, args ...string) (string, error)
}

// GitRebaseCommandRunnerFunc adapts a function to GitRebaseCommandRunner.
type GitRebaseCommandRunnerFunc func(
	ctx context.Context,
	repoPath string,
	extraEnv []string,
	args ...string,
) (string, error)

// RunGit implements GitRebaseCommandRunner.
func (f GitRebaseCommandRunnerFunc) RunGit(
	ctx context.Context,
	repoPath string,
	extraEnv []string,
	args ...string,
) (string, error) {
	if f == nil {
		return "", fmt.Errorf("git rebase command runner is nil: %w", ErrInvalidInput)
	}
	return f(ctx, repoPath, extraEnv, args...)
}

// GitRebaseExecRunner executes git with a minimal environment. The service
// supplies the command deadline, so every invocation is bounded uniformly.
type GitRebaseExecRunner struct{}

// RunGit implements GitRebaseCommandRunner.
func (GitRebaseExecRunner) RunGit(
	ctx context.Context,
	repoPath string,
	extraEnv []string,
	args ...string,
) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	cmd.Env = append(minimalGitEnv(), extraEnv...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// GitRebaseRepositoryValidator authorizes a repository before any command or
// rebase administrative file access occurs.
type GitRebaseRepositoryValidator interface {
	ValidateRepository(repoPath string) error
}

// GitRebaseRepositoryValidatorFunc adapts a function to a validator.
type GitRebaseRepositoryValidatorFunc func(repoPath string) error

// ValidateRepository implements GitRebaseRepositoryValidator.
func (f GitRebaseRepositoryValidatorFunc) ValidateRepository(repoPath string) error {
	if f == nil {
		return fmt.Errorf("git rebase repository validator is nil: %w", ErrInvalidInput)
	}
	return f(repoPath)
}

// GitRebaseBranchProvider is the public GitService boundary used for
// repository validation.
type GitRebaseBranchProvider interface {
	ListBranches(repoPath string) ([]BranchRef, error)
}

// GitRebaseGitServiceAdapter validates repositories through an exported
// GitService method, preserving GitService's workspace sandbox.
type GitRebaseGitServiceAdapter struct {
	provider GitRebaseBranchProvider
}

// NewGitRebaseGitServiceAdapter creates a public-API-only adapter.
func NewGitRebaseGitServiceAdapter(provider GitRebaseBranchProvider) *GitRebaseGitServiceAdapter {
	if gitRebaseDependencyIsNil(provider) {
		provider = nil
	}
	return &GitRebaseGitServiceAdapter{provider: provider}
}

// ValidateRepository implements GitRebaseRepositoryValidator.
func (a *GitRebaseGitServiceAdapter) ValidateRepository(repoPath string) error {
	if a == nil || gitRebaseDependencyIsNil(a.provider) {
		return fmt.Errorf("git service is not injected: %w", ErrInvalidInput)
	}
	if _, err := a.provider.ListBranches(repoPath); err != nil {
		return fmt.Errorf("validate repository through GitService: %w", err)
	}
	return nil
}

// GitRebaseDependencies configures the standalone rebase module.
type GitRebaseDependencies struct {
	Runner              GitRebaseCommandRunner
	RepositoryValidator GitRebaseRepositoryValidator
	CommandTimeout      time.Duration
}

// GitRebaseService provides interactive rebase operations without reading
// private GitService state or command helpers.
type GitRebaseService struct {
	gitSvc              *GitService
	runner              GitRebaseCommandRunner
	repositoryValidator GitRebaseRepositoryValidator
	commandTimeout      time.Duration
	wsCtx               *WorkspaceContext
}

// NewGitRebaseService creates a service backed by the public GitService
// repository-validation boundary and the real git executable.
func NewGitRebaseService(gitSvc *GitService) *GitRebaseService {
	service, _ := NewGitRebaseServiceWithDependencies(GitRebaseDependencies{
		RepositoryValidator: gitRebaseValidatorForGitService(gitSvc),
	})
	service.gitSvc = gitSvc
	return service
}

// NewGitRebaseServiceWithRunner creates a testable GitService-backed service.
func NewGitRebaseServiceWithRunner(
	gitSvc *GitService,
	runner GitRebaseCommandRunner,
) *GitRebaseService {
	service, _ := NewGitRebaseServiceWithDependencies(GitRebaseDependencies{
		Runner:              runner,
		RepositoryValidator: gitRebaseValidatorForGitService(gitSvc),
	})
	service.gitSvc = gitSvc
	return service
}

// NewGitRebaseServiceWithDependencies creates an independently testable
// service. A zero timeout selects the required 60-second command limit.
func NewGitRebaseServiceWithDependencies(
	deps GitRebaseDependencies,
) (*GitRebaseService, error) {
	if deps.CommandTimeout < 0 {
		return nil, fmt.Errorf("git rebase command timeout must not be negative: %w", ErrInvalidInput)
	}
	runner := deps.Runner
	if gitRebaseDependencyIsNil(runner) {
		runner = GitRebaseExecRunner{}
	}
	timeout := deps.CommandTimeout
	if timeout == 0 {
		timeout = gitRebaseDefaultTimeout
	}
	return &GitRebaseService{
		runner:              runner,
		repositoryValidator: deps.RepositoryValidator,
		commandTimeout:      timeout,
	}, nil
}

// setWorkspaceContext binds every interactive rebase to the same active
// workspace identity used by ProjectService.
//
//wails:ignore
func (s *GitRebaseService) setWorkspaceContext(ctx *WorkspaceContext) {
	if s != nil {
		s.wsCtx = ctx
	}
}

func gitRebaseValidatorForGitService(gitSvc *GitService) GitRebaseRepositoryValidator {
	if gitSvc == nil {
		return nil
	}
	return NewGitRebaseGitServiceAdapter(gitSvc)
}

// GetRebaseTodoList returns the commits that a normal non-merge interactive
// rebase would replay, ordered from oldest to newest.
func (s *GitRebaseService) GetRebaseTodoList(
	ctx context.Context,
	repoPath,
	upstreamBranch string,
) ([]RebaseTodoAction, error) {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return nil, err
	}
	unlock := lockGitRebaseRepository(repo)
	defer unlock()
	gitDir, err := s.gitDir(ctx, repo)
	if err != nil {
		return nil, err
	}
	mergeExists, rebaseDir, err := secureGitRebaseDirectory(gitDir, "rebase-merge")
	if err != nil {
		return nil, err
	}
	applyExists, _, err := secureGitRebaseDirectory(gitDir, "rebase-apply")
	if err != nil {
		return nil, err
	}
	if mergeExists {
		if err := validateGitRebaseUpstreamInput(upstreamBranch); err != nil {
			return nil, err
		}
		state, err := readGitRebaseState(rebaseDir)
		if err != nil {
			return nil, fmt.Errorf("active interactive rebase has no valid Koyori IDE snapshot: %w", err)
		}
		if err := validateGitRebaseStateBinding(rebaseDir, state); err != nil {
			return nil, err
		}
		if upstreamBranch != state.UpstreamRef {
			return nil, fmt.Errorf("requested upstream ref does not match the active rebase snapshot: %w", ErrInvalidInput)
		}
		return cloneGitRebaseActions(state.Actions), nil
	} else if applyExists {
		return nil, fmt.Errorf("an unsupported apply-backend rebase is already in progress: %w", ErrInvalidInput)
	}
	upstream, err := s.resolveUpstream(ctx, repo, upstreamBranch)
	if err != nil {
		return nil, err
	}
	return s.getRebaseActionsForRange(ctx, repo, upstream, "HEAD")
}

func (s *GitRebaseService) getRebaseActionsForRange(
	ctx context.Context,
	repo,
	upstream,
	rangeEnd string,
) ([]RebaseTodoAction, error) {
	const format = "%H%x00%s%x00%b%x00%an%x00%ae%x00%aI"
	output, err := s.runGit(
		ctx,
		repo,
		nil,
		"log",
		"-z",
		"--reverse",
		"--topo-order",
		"--no-merges",
		"--format="+format,
		upstream+".."+rangeEnd,
	)
	if err != nil {
		return nil, err
	}
	actions, err := parseGitRebaseLog(output)
	if err != nil {
		return nil, fmt.Errorf("parse interactive rebase commits: %w", err)
	}
	return actions, nil
}

// StartInteractiveRebase starts a rebase and pauses before the first todo
// action. ApplyRebaseActions can then safely replace the remaining todo file.
func (s *GitRebaseService) StartInteractiveRebase(
	ctx context.Context,
	repoPath,
	upstreamBranch string,
) error {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return err
	}
	unlock := lockGitRebaseRepository(repo)
	defer unlock()
	inProgress, err := s.isRebaseInProgress(ctx, repo)
	if err != nil {
		return err
	}
	if inProgress {
		return fmt.Errorf("a rebase is already in progress: %w", ErrAlreadyExists)
	}
	upstream, err := s.resolveUpstream(ctx, repo, upstreamBranch)
	if err != nil {
		return err
	}
	origHead, err := s.resolveCommit(ctx, repo, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve pre-rebase HEAD: %w", err)
	}
	expectedCommits, err := s.listReplayCommits(ctx, repo, upstream, origHead)
	if err != nil {
		return err
	}
	if len(expectedCommits) == 0 {
		return fmt.Errorf("there are no non-merge commits to rebase: %w", ErrInvalidInput)
	}
	initialActions, err := s.getRebaseActionsForRange(ctx, repo, upstream, origHead)
	if err != nil {
		return err
	}
	if len(initialActions) != len(expectedCommits) {
		return fmt.Errorf("rebase log does not match the replay commit set: %w", ErrInvalidInput)
	}
	for index, action := range initialActions {
		if action.CommitSHA != expectedCommits[index] || action.Action != "pick" {
			return fmt.Errorf("rebase action %d does not match the replay commit set: %w", index+1, ErrInvalidInput)
		}
	}

	editorPath, cleanup, err := createGitRebasePauseEditor()
	if err != nil {
		return err
	}
	defer cleanup()
	extraEnv := []string{
		"GIT_SEQUENCE_EDITOR=" + quoteGitShellWord(filepath.ToSlash(editorPath)),
		"GIT_EDITOR=:",
	}
	_, err = s.runGit(
		ctx,
		repo,
		extraEnv,
		"-c",
		"rebase.rebaseMerges=false",
		"-c",
		"rebase.updateRefs=false",
		"-c",
		"rebase.abbreviateCommands=false",
		"-c",
		"rebase.instructionFormat=%s",
		"rebase",
		"-i",
		"--no-autosquash",
		upstream,
	)
	if err != nil {
		return err
	}

	todoPath, _, err := s.locateRebaseTodo(ctx, repo)
	if err != nil {
		return s.abortAfterStartFailure(ctx, repo, err)
	}
	rebaseDir := filepath.Dir(todoPath)
	if err := validateRebaseAdminCommit(rebaseDir, "orig-head", origHead); err != nil {
		return s.abortAfterStartFailure(ctx, repo, err)
	}
	if err := validateRebaseAdminCommit(rebaseDir, "onto", upstream); err != nil {
		return s.abortAfterStartFailure(ctx, repo, err)
	}
	todo, _, err := readSafeRebaseFile(rebaseDir, "git-rebase-todo", gitRebaseMaxStateSize)
	if err != nil {
		return s.abortAfterStartFailure(ctx, repo, err)
	}
	if err := validateRebaseTodoCommitSet(todo, expectedCommits); err != nil {
		return s.abortAfterStartFailure(ctx, repo, err)
	}
	state := gitRebasePersistentState{
		Version:         gitRebaseStateVersion,
		Phase:           gitRebasePhaseAwaitingApply,
		OrigHead:        origHead,
		Onto:            upstream,
		UpstreamRef:     upstreamBranch,
		TodoHash:        gitRebaseContentHash(todo),
		ExpectedCommits: append([]string(nil), expectedCommits...),
		Actions:         cloneGitRebaseActions(initialActions),
	}
	if err := writeGitRebaseState(rebaseDir, state); err != nil {
		return s.abortAfterStartFailure(ctx, repo, err)
	}
	return nil
}

// ApplyRebaseActions validates and atomically writes the active interactive
// rebase todo file. It never follows a todo-file or rebase-directory symlink.
func (s *GitRebaseService) ApplyRebaseActions(
	ctx context.Context,
	repoPath string,
	actions []RebaseTodoAction,
) error {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return err
	}
	unlock := lockGitRebaseRepository(repo)
	defer unlock()
	todoPath, mode, err := s.locateRebaseTodo(ctx, repo)
	if err != nil {
		return err
	}
	rebaseDir := filepath.Dir(todoPath)
	state, err := readGitRebaseState(rebaseDir)
	if err != nil {
		return err
	}
	if err := validateGitRebaseStateBinding(rebaseDir, state); err != nil {
		return err
	}
	actionSnapshot, err := canonicalizeGitRebaseActions(actions, state.ExpectedCommits)
	if err != nil {
		return err
	}
	serialized, rewordMessages, err := prepareGitRebaseActions(actionSnapshot, state.ExpectedCommits)
	if err != nil {
		return err
	}
	requestedTodo := []byte(serialized)
	requestedHash := gitRebaseContentHash(requestedTodo)
	currentTodo, _, err := readSafeRebaseFile(rebaseDir, "git-rebase-todo", gitRebaseMaxStateSize)
	if err != nil {
		return err
	}
	currentHash := gitRebaseContentHash(currentTodo)

	switch state.Phase {
	case gitRebasePhaseAwaitingApply:
		if currentHash != state.TodoHash {
			return fmt.Errorf("interactive rebase todo changed after start: %w", ErrInvalidInput)
		}
	case gitRebasePhaseApplying:
		if state.PendingTodoHash != requestedHash || !reflect.DeepEqual(state.RewordMessages, rewordMessages) ||
			!reflect.DeepEqual(state.Actions, actionSnapshot) {
			return fmt.Errorf("a different rebase action set was interrupted while applying: %w", ErrInvalidInput)
		}
		if currentHash != state.TodoHash && currentHash != requestedHash {
			return fmt.Errorf("interactive rebase todo changed during recovery: %w", ErrInvalidInput)
		}
	case gitRebasePhaseReady:
		if currentHash == state.TodoHash && currentHash == requestedHash &&
			reflect.DeepEqual(state.RewordMessages, rewordMessages) &&
			reflect.DeepEqual(state.Actions, actionSnapshot) {
			return nil
		}
		return fmt.Errorf("rebase actions have already been applied: %w", ErrInvalidInput)
	default:
		return fmt.Errorf("rebase actions cannot be applied while state is %q: %w", state.Phase, ErrInvalidInput)
	}

	if state.Phase != gitRebasePhaseApplying {
		state.Phase = gitRebasePhaseApplying
		state.PendingTodoHash = requestedHash
		state.RewordMessages = rewordMessages
		state.Actions = cloneGitRebaseActions(actionSnapshot)
		if err := writeGitRebaseState(rebaseDir, state); err != nil {
			return err
		}
	}
	if currentHash != requestedHash {
		latestTodo, _, err := readSafeRebaseFile(rebaseDir, "git-rebase-todo", gitRebaseMaxStateSize)
		if err != nil {
			return err
		}
		if latestHash := gitRebaseContentHash(latestTodo); latestHash != state.TodoHash {
			return fmt.Errorf("interactive rebase todo changed immediately before applying actions: %w", ErrInvalidInput)
		}
		if err := replaceSafeRebaseFile(todoPath, requestedTodo, mode.Perm()); err != nil {
			return fmt.Errorf("write interactive rebase todo: %w", err)
		}
	}
	writtenTodo, _, err := readSafeRebaseFile(rebaseDir, "git-rebase-todo", gitRebaseMaxStateSize)
	if err != nil {
		return err
	}
	if gitRebaseContentHash(writtenTodo) != requestedHash {
		return fmt.Errorf("interactive rebase todo verification failed: %w", ErrInvalidInput)
	}
	state.Phase = gitRebasePhaseReady
	state.TodoHash = requestedHash
	state.PendingTodoHash = ""
	state.StopReason = ""
	if err := writeGitRebaseState(rebaseDir, state); err != nil {
		return err
	}
	return nil
}

// ContinueRebase continues the active rebase without opening an interactive
// editor in the desktop process.
func (s *GitRebaseService) ContinueRebase(ctx context.Context, repoPath string) error {
	lockedRepo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return err
	}
	unlock := lockGitRebaseRepository(lockedRepo)
	defer unlock()
	repo, rebaseDir, state, err := s.loadOwnedRebase(ctx, lockedRepo)
	if err != nil {
		return err
	}
	if state.Phase != gitRebasePhaseReady && state.Phase != gitRebasePhaseStopped {
		return fmt.Errorf("rebase actions must be applied before continuing: %w", ErrInvalidInput)
	}
	if err := s.recoverPendingRewordBeforeContinue(ctx, repo, rebaseDir, &state); err != nil {
		return err
	}
	if err := validateCurrentTodoHash(rebaseDir, state.TodoHash); err != nil {
		return err
	}

	for attempts := 0; attempts <= len(state.ExpectedCommits)+1; attempts++ {
		_, commandErr := s.runGit(
			ctx,
			repo,
			[]string{"GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=:"},
			"rebase",
			"--continue",
		)
		active, activeErr := rebaseMergeDirectoryForRepo(s, ctx, repo)
		if activeErr != nil {
			return errors.Join(commandErr, activeErr)
		}
		if active == "" {
			return commandErr
		}
		if filepath.Clean(active) != filepath.Clean(rebaseDir) {
			return errors.Join(commandErr, fmt.Errorf("interactive rebase administrative directory changed: %w", ErrInvalidInput))
		}
		if commandErr != nil {
			stopReason := gitRebaseStopReasonManual
			unmerged, classificationErr := s.hasUnmergedPaths(ctx, repo)
			if classificationErr == nil && unmerged {
				stopReason = gitRebaseStopReasonCommandError
			}
			persistErr := persistGitRebaseStoppedState(rebaseDir, &state, stopReason)
			return errors.Join(commandErr, classificationErr, persistErr)
		}

		rebaseHead, headErr := s.optionalRebaseHead(ctx, repo)
		message, syntheticReword := state.RewordMessages[rebaseHead]
		if headErr == nil && syntheticReword {
			unmerged, err := s.hasUnmergedPaths(ctx, repo)
			if err != nil {
				return err
			}
			if !unmerged {
				if err := s.amendRewordCommit(ctx, repo, rebaseDir, message); err != nil {
					_ = persistGitRebaseStoppedState(rebaseDir, &state, gitRebaseStopReasonSyntheticEdit)
					return err
				}
				delete(state.RewordMessages, rebaseHead)
				state.Phase = gitRebasePhaseReady
				state.StopReason = ""
				if err := refreshGitRebaseTodoHash(rebaseDir, &state); err != nil {
					return err
				}
				if err := writeGitRebaseState(rebaseDir, state); err != nil {
					return err
				}
				continue
			}
		}
		if err := persistGitRebaseStoppedState(rebaseDir, &state, gitRebaseStopReasonManual); err != nil {
			return errors.Join(headErr, err)
		}
		return headErr
	}
	return fmt.Errorf("interactive rebase exceeded its expected number of stops: %w", ErrInvalidInput)
}

// AbortRebase restores the branch to its pre-rebase state.
func (s *GitRebaseService) AbortRebase(ctx context.Context, repoPath string) error {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return err
	}
	unlock := lockGitRebaseRepository(repo)
	defer unlock()
	if _, _, _, err := s.loadOwnedRebase(ctx, repo); err != nil {
		return err
	}
	return s.runRebaseControl(ctx, repo, "--abort")
}

// SkipCommit skips the commit currently stopping the rebase.
func (s *GitRebaseService) SkipCommit(ctx context.Context, repoPath string) error {
	lockedRepo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return err
	}
	unlock := lockGitRebaseRepository(lockedRepo)
	defer unlock()
	repo, rebaseDir, state, err := s.loadOwnedRebase(ctx, lockedRepo)
	if err != nil {
		return err
	}
	if state.Phase != gitRebasePhaseStopped {
		return fmt.Errorf("rebase is not stopped at a commit: %w", ErrInvalidInput)
	}
	if state.StopReason != gitRebaseStopReasonCommandError {
		return fmt.Errorf("only a failed commit application can be skipped: %w", ErrInvalidInput)
	}
	if err := validateCurrentTodoHash(rebaseDir, state.TodoHash); err != nil {
		return err
	}
	rebaseHead, err := s.optionalRebaseHead(ctx, repo)
	if err != nil || rebaseHead == "" {
		return errors.Join(fmt.Errorf("REBASE_HEAD is required before skipping a commit: %w", ErrInvalidInput), err)
	}
	if !gitRebaseCommitInSet(rebaseHead, state.ExpectedCommits) {
		return fmt.Errorf("REBASE_HEAD does not belong to this rebase: %w", ErrInvalidInput)
	}
	_, commandErr := s.runGit(
		ctx,
		repo,
		[]string{"GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=:"},
		"rebase",
		"--skip",
	)
	active, activeErr := rebaseMergeDirectoryForRepo(s, ctx, repo)
	if activeErr != nil {
		return errors.Join(commandErr, activeErr)
	}
	if active == "" {
		return commandErr
	}
	if commandErr == nil {
		delete(state.RewordMessages, rebaseHead)
	}
	stopReason := gitRebaseStopReasonManual
	if commandErr != nil {
		stopReason = gitRebaseStopReasonCommandError
	}
	if persistErr := persistGitRebaseStoppedState(rebaseDir, &state, stopReason); persistErr != nil {
		return errors.Join(commandErr, persistErr)
	}
	return commandErr
}

// IsRebaseInProgress reports both merge-backend and apply-backend rebases.
func (s *GitRebaseService) IsRebaseInProgress(repoPath string) (bool, error) {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return false, err
	}
	unlock := lockGitRebaseRepository(repo)
	defer unlock()
	return s.isRebaseInProgress(context.Background(), repo)
}

// GetRebaseStatus returns enough persisted state for the frontend to recover
// after an application restart without adopting an unrelated Git rebase.
func (s *GitRebaseService) GetRebaseStatus(repoPath string) (GitRebaseStatus, error) {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return GitRebaseStatus{}, err
	}
	unlock := lockGitRebaseRepository(repo)
	defer unlock()
	ctx := context.Background()
	gitDir, err := s.gitDir(ctx, repo)
	if err != nil {
		return GitRebaseStatus{}, err
	}
	mergeExists, rebaseDir, err := secureGitRebaseDirectory(gitDir, "rebase-merge")
	if err != nil {
		return GitRebaseStatus{}, err
	}
	applyExists, _, err := secureGitRebaseDirectory(gitDir, "rebase-apply")
	if err != nil {
		return GitRebaseStatus{}, err
	}
	if mergeExists && applyExists {
		return GitRebaseStatus{}, fmt.Errorf("multiple rebase backends are active: %w", ErrInvalidInput)
	}
	if !mergeExists {
		return GitRebaseStatus{InProgress: applyExists}, nil
	}
	status := GitRebaseStatus{InProgress: true}
	statePath := filepath.Join(rebaseDir, gitRebaseStateFileName)
	if _, err := os.Lstat(statePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return status, nil
		}
		return GitRebaseStatus{}, fmt.Errorf("inspect Koyori IDE rebase state: %w", err)
	}
	state, err := readGitRebaseState(rebaseDir)
	if err != nil {
		return GitRebaseStatus{}, err
	}
	if err := validateGitRebaseStateBinding(rebaseDir, state); err != nil {
		return GitRebaseStatus{}, err
	}
	status.Owned = true
	status.Phase = state.Phase
	status.StopReason = state.StopReason
	status.Upstream = state.Onto
	status.UpstreamRef = state.UpstreamRef
	status.OrigHead = state.OrigHead
	status.Actions = cloneGitRebaseActions(state.Actions)
	return status, nil
}

func (s *GitRebaseService) runRebaseControl(
	ctx context.Context,
	repoPath,
	action string,
) error {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return err
	}
	_, err = s.runGit(
		ctx,
		repo,
		[]string{"GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=:"},
		"rebase",
		action,
	)
	return err
}

func (s *GitRebaseService) abortAfterStartFailure(
	ctx context.Context,
	repoPath string,
	cause error,
) error {
	_, abortErr := s.runGit(
		ctx,
		repoPath,
		[]string{"GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=:"},
		"rebase",
		"--abort",
	)
	if abortErr != nil {
		return errors.Join(cause, fmt.Errorf("abort rebase after initialization failure: %w", abortErr))
	}
	return cause
}

func (s *GitRebaseService) resolveCommit(
	ctx context.Context,
	repoPath,
	revision string,
) (string, error) {
	output, err := s.runGit(
		ctx,
		repoPath,
		nil,
		"rev-parse",
		"--verify",
		"--end-of-options",
		revision+"^{commit}",
	)
	if err != nil {
		return "", err
	}
	commit := strings.ToLower(strings.TrimSpace(output))
	if !validFullGitRebaseCommit(commit) {
		return "", fmt.Errorf("git returned an invalid commit id for %q: %w", revision, ErrInvalidInput)
	}
	return commit, nil
}

func (s *GitRebaseService) listReplayCommits(
	ctx context.Context,
	repoPath,
	upstream,
	origHead string,
) ([]string, error) {
	output, err := s.runGit(
		ctx,
		repoPath,
		nil,
		"rev-list",
		"--reverse",
		"--topo-order",
		"--no-merges",
		upstream+".."+origHead,
	)
	if err != nil {
		return nil, fmt.Errorf("list commits for interactive rebase: %w", err)
	}
	lines := strings.Fields(output)
	if len(lines) > gitRebaseMaxActions {
		return nil, fmt.Errorf("interactive rebase exceeds %d commits: %w", gitRebaseMaxActions, ErrInvalidInput)
	}
	commits := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		commit := strings.ToLower(line)
		if !validFullGitRebaseCommit(commit) {
			return nil, fmt.Errorf("git rev-list returned invalid commit id %q: %w", line, ErrInvalidInput)
		}
		if _, duplicate := seen[commit]; duplicate {
			return nil, fmt.Errorf("git rev-list returned duplicate commit %s: %w", commit, ErrInvalidInput)
		}
		seen[commit] = struct{}{}
		commits = append(commits, commit)
	}
	return commits, nil
}

func (s *GitRebaseService) loadOwnedRebase(
	ctx context.Context,
	repoPath string,
) (string, string, gitRebasePersistentState, error) {
	repo, err := s.validateRepoPath(repoPath)
	if err != nil {
		return "", "", gitRebasePersistentState{}, err
	}
	rebaseDir, err := rebaseMergeDirectoryForRepo(s, ctx, repo)
	if err != nil {
		return "", "", gitRebasePersistentState{}, err
	}
	if rebaseDir == "" {
		return "", "", gitRebasePersistentState{}, fmt.Errorf("interactive rebase is not in progress: %w", ErrNotFound)
	}
	state, err := readGitRebaseState(rebaseDir)
	if err != nil {
		return "", "", gitRebasePersistentState{}, err
	}
	if err := validateGitRebaseStateBinding(rebaseDir, state); err != nil {
		return "", "", gitRebasePersistentState{}, err
	}
	return repo, rebaseDir, state, nil
}

func rebaseMergeDirectoryForRepo(
	s *GitRebaseService,
	ctx context.Context,
	repoPath string,
) (string, error) {
	gitDir, err := s.gitDir(ctx, repoPath)
	if err != nil {
		return "", err
	}
	exists, rebaseDir, err := secureGitRebaseDirectory(gitDir, "rebase-merge")
	if err != nil {
		return "", err
	}
	if !exists {
		return "", nil
	}
	return rebaseDir, nil
}

func (s *GitRebaseService) optionalRebaseHead(
	ctx context.Context,
	repoPath string,
) (string, error) {
	output, err := s.runGit(
		ctx,
		repoPath,
		nil,
		"rev-parse",
		"--verify",
		"--quiet",
		"REBASE_HEAD^{commit}",
	)
	if err != nil {
		// rev-parse --quiet uses exit status 1 when the pseudo-ref is absent.
		var exitErr *exec.ExitError
		if strings.TrimSpace(output) == "" && errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	commit := strings.ToLower(strings.TrimSpace(output))
	if !validFullGitRebaseCommit(commit) {
		return "", fmt.Errorf("git returned an invalid REBASE_HEAD: %w", ErrInvalidInput)
	}
	return commit, nil
}

func (s *GitRebaseService) hasUnmergedPaths(
	ctx context.Context,
	repoPath string,
) (bool, error) {
	output, err := s.runGit(ctx, repoPath, nil, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return false, err
	}
	return len(output) != 0, nil
}

func (s *GitRebaseService) recoverPendingRewordBeforeContinue(
	ctx context.Context,
	repoPath,
	rebaseDir string,
	state *gitRebasePersistentState,
) error {
	rebaseHead, err := s.optionalRebaseHead(ctx, repoPath)
	if err != nil || rebaseHead == "" {
		return err
	}
	message, pending := state.RewordMessages[rebaseHead]
	if !pending {
		return nil
	}
	unmerged, err := s.hasUnmergedPaths(ctx, repoPath)
	if err != nil {
		return err
	}
	if unmerged {
		if state.Phase == gitRebasePhaseReady {
			state.Phase = gitRebasePhaseStopped
			state.StopReason = gitRebaseStopReasonCommandError
			if err := refreshGitRebaseTodoHash(rebaseDir, state); err != nil {
				return err
			}
			if err := writeGitRebaseState(rebaseDir, *state); err != nil {
				return err
			}
		}
		return nil
	}
	amend := state.Phase != gitRebasePhaseStopped || state.StopReason != gitRebaseStopReasonCommandError
	if err := s.commitRewordCommit(ctx, repoPath, rebaseDir, message, amend); err != nil {
		return err
	}
	delete(state.RewordMessages, rebaseHead)
	state.Phase = gitRebasePhaseReady
	state.StopReason = ""
	if err := refreshGitRebaseTodoHash(rebaseDir, state); err != nil {
		return err
	}
	return writeGitRebaseState(rebaseDir, *state)
}

func (s *GitRebaseService) amendRewordCommit(
	ctx context.Context,
	repoPath,
	rebaseDir,
	message string,
) error {
	return s.commitRewordCommit(ctx, repoPath, rebaseDir, message, true)
}

func (s *GitRebaseService) commitRewordCommit(
	ctx context.Context,
	repoPath,
	rebaseDir,
	message string,
	amend bool,
) error {
	messageFile, err := os.CreateTemp(rebaseDir, ".koyori-ide-reword-message-*")
	if err != nil {
		return fmt.Errorf("create controlled reword message file: %w", err)
	}
	messagePath := messageFile.Name()
	defer os.Remove(messagePath)
	if err := os.Chmod(messagePath, 0o600); err != nil {
		_ = messageFile.Close()
		return fmt.Errorf("protect reword message file: %w", err)
	}
	if _, err := io.WriteString(messageFile, message); err != nil {
		_ = messageFile.Close()
		return fmt.Errorf("write reword message file: %w", err)
	}
	if err := messageFile.Sync(); err != nil {
		_ = messageFile.Close()
		return fmt.Errorf("sync reword message file: %w", err)
	}
	if err := messageFile.Close(); err != nil {
		return fmt.Errorf("close reword message file: %w", err)
	}
	args := []string{"commit"}
	if amend {
		args = append(args, "--amend")
	}
	args = append(args, "--allow-empty", "--allow-empty-message", "--cleanup=verbatim", "-F", messagePath)
	_, err = s.runGit(
		ctx,
		repoPath,
		[]string{"GIT_EDITOR=:"},
		args...,
	)
	if err != nil {
		if amend {
			return fmt.Errorf("apply reword commit message: %w", err)
		}
		return fmt.Errorf("commit reword after conflict resolution: %w", err)
	}
	return nil
}

func (s *GitRebaseService) resolveUpstream(
	ctx context.Context,
	repoPath,
	upstream string,
) (string, error) {
	if err := validateGitRebaseUpstreamInput(upstream); err != nil {
		return "", err
	}
	output, err := s.runGit(
		ctx,
		repoPath,
		nil,
		"rev-parse",
		"--verify",
		"--end-of-options",
		upstream+"^{commit}",
	)
	if err != nil {
		return "", fmt.Errorf("resolve upstream %q: %w", upstream, err)
	}
	resolved := strings.TrimSpace(output)
	if !gitRebaseCommitPattern.MatchString(resolved) || (len(resolved) != 40 && len(resolved) != 64) {
		return "", fmt.Errorf("git returned an invalid upstream commit id: %w", ErrInvalidInput)
	}
	return strings.ToLower(resolved), nil
}

func validateGitRebaseUpstreamInput(upstream string) error {
	if upstream == "" || strings.TrimSpace(upstream) != upstream {
		return fmt.Errorf("upstream branch is required and must not contain surrounding whitespace: %w", ErrInvalidInput)
	}
	if strings.ContainsAny(upstream, "\x00\r\n") {
		return fmt.Errorf("upstream branch contains an invalid control character: %w", ErrInvalidInput)
	}
	return nil
}

func (s *GitRebaseService) validateRepoPath(repoPath string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("git rebase service is nil: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(repoPath) == "" {
		return "", fmt.Errorf("repository path is required: %w", ErrInvalidInput)
	}
	if strings.IndexByte(repoPath, 0) >= 0 {
		return "", fmt.Errorf("repository path contains a NUL byte: %w", ErrInvalidInput)
	}
	abs, err := filepath.Abs(filepath.Clean(repoPath))
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
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve repository path symlinks: %w", err)
	}
	if s.wsCtx != nil {
		root, err := s.wsCtx.RequireRoot()
		if err != nil {
			return "", err
		}
		if _, err := ValidatePathWithinRoot(root, resolved); err != nil {
			return "", fmt.Errorf("repository is outside the active workspace: %w", ErrNotAllowed)
		}
	}
	validator := s.repositoryValidator
	if gitRebaseDependencyIsNil(validator) && s.gitSvc != nil {
		validator = NewGitRebaseGitServiceAdapter(s.gitSvc)
	}
	if gitRebaseDependencyIsNil(validator) {
		return "", fmt.Errorf("git rebase repository validator is not injected: %w", ErrInvalidInput)
	}
	if err := validator.ValidateRepository(resolved); err != nil {
		return "", fmt.Errorf("validate repository: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func (s *GitRebaseService) runGit(
	ctx context.Context,
	repoPath string,
	extraEnv []string,
	args ...string,
) (string, error) {
	if s == nil {
		return "", fmt.Errorf("git rebase service is nil: %w", ErrInvalidInput)
	}
	runner := s.runner
	if gitRebaseDependencyIsNil(runner) {
		runner = GitRebaseExecRunner{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := s.commandTimeout
	if timeout <= 0 {
		timeout = gitRebaseDefaultTimeout
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := runner.RunGit(commandCtx, repoPath, extraEnv, args...)
	if commandErr := commandCtx.Err(); commandErr != nil {
		if errors.Is(commandErr, context.DeadlineExceeded) {
			return output, fmt.Errorf("git %s exceeded its deadline: %w", strings.Join(args, " "), ErrTimeout)
		}
		return output, fmt.Errorf("git %s canceled: %w", strings.Join(args, " "), commandErr)
	}
	if err != nil {
		return output, gitRebaseCommandError(args, output, err)
	}
	return output, nil
}

func (s *GitRebaseService) gitDir(ctx context.Context, repoPath string) (string, error) {
	output, err := s.runGit(ctx, repoPath, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("locate git directory: %w", err)
	}
	value := strings.TrimSpace(output)
	if value == "" || strings.ContainsAny(value, "\x00\r\n") || !filepath.IsAbs(value) {
		return "", fmt.Errorf("git returned an invalid absolute git directory: %w", ErrInvalidInput)
	}
	info, err := os.Lstat(value)
	if err != nil {
		return "", fmt.Errorf("inspect git directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("git directory must be a real directory: %w", ErrInvalidInput)
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", fmt.Errorf("resolve git directory: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func (s *GitRebaseService) isRebaseInProgress(
	ctx context.Context,
	repoPath string,
) (bool, error) {
	gitDir, err := s.gitDir(ctx, repoPath)
	if err != nil {
		return false, err
	}
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		exists, _, err := secureGitRebaseDirectory(gitDir, name)
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}

func (s *GitRebaseService) locateRebaseTodo(
	ctx context.Context,
	repoPath string,
) (string, os.FileMode, error) {
	gitDir, err := s.gitDir(ctx, repoPath)
	if err != nil {
		return "", 0, err
	}
	exists, rebaseDir, err := secureGitRebaseDirectory(gitDir, "rebase-merge")
	if err != nil {
		return "", 0, err
	}
	if !exists {
		return "", 0, fmt.Errorf("interactive rebase is not in progress: %w", ErrNotFound)
	}
	todoPath := filepath.Join(rebaseDir, "git-rebase-todo")
	info, err := os.Lstat(todoPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", 0, fmt.Errorf("interactive rebase todo does not exist: %w", ErrNotFound)
		}
		return "", 0, fmt.Errorf("inspect interactive rebase todo: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("interactive rebase todo must be a regular file: %w", ErrInvalidInput)
	}
	resolvedTodo, err := filepath.EvalSymlinks(todoPath)
	if err != nil {
		return "", 0, fmt.Errorf("resolve interactive rebase todo: %w", err)
	}
	inside, err := gitRebasePathInside(rebaseDir, resolvedTodo)
	if err != nil {
		return "", 0, err
	}
	if !inside || filepath.Base(resolvedTodo) != "git-rebase-todo" {
		return "", 0, fmt.Errorf("interactive rebase todo escaped its administrative directory: %w", ErrInvalidInput)
	}
	return filepath.Clean(resolvedTodo), info.Mode(), nil
}

func secureGitRebaseDirectory(
	gitDir,
	name string,
) (bool, string, error) {
	if name != "rebase-merge" && name != "rebase-apply" {
		return false, "", fmt.Errorf("invalid rebase directory name: %w", ErrInvalidInput)
	}
	path := filepath.Join(gitDir, name)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("inspect %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, "", fmt.Errorf("%s must be a real directory: %w", name, ErrInvalidInput)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, "", fmt.Errorf("resolve %s: %w", name, err)
	}
	inside, err := gitRebasePathInside(gitDir, resolved)
	if err != nil {
		return false, "", err
	}
	if !inside {
		return false, "", fmt.Errorf("%s escaped the git directory: %w", name, ErrInvalidInput)
	}
	return true, filepath.Clean(resolved), nil
}

func gitRebasePathInside(root, target string) (bool, error) {
	// P19 CI 修复：root 与 target 须在同一解析形态下比较。target 已经过
	// EvalSymlinks（macOS /var → /private/var、Windows 8.3 短名展开），
	// root 仍是调用方原始拼写，纯词法 Rel 会把同一目录判为逃逸。
	root = filepath.Clean(root)
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = filepath.Clean(resolved)
	}
	relative, err := filepath.Rel(root, filepath.Clean(target))
	if err != nil {
		return false, fmt.Errorf("compare git administrative paths: %w", err)
	}
	return relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func validFullGitRebaseCommit(commit string) bool {
	return gitRebaseCommitPattern.MatchString(commit) && (len(commit) == 40 || len(commit) == 64)
}

func gitRebaseContentHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func readSafeRebaseFile(
	rebaseDir,
	name string,
	maxSize int64,
) ([]byte, os.FileMode, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, 0, fmt.Errorf("invalid rebase administrative file name %q: %w", name, ErrInvalidInput)
	}
	path := filepath.Join(rebaseDir, name)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, fmt.Errorf("rebase administrative file %q does not exist: %w", name, ErrNotFound)
		}
		return nil, 0, fmt.Errorf("inspect rebase administrative file %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("rebase administrative file %q must be regular: %w", name, ErrInvalidInput)
	}
	if info.Size() < 0 || info.Size() > maxSize {
		return nil, 0, fmt.Errorf("rebase administrative file %q is too large: %w", name, ErrInvalidInput)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve rebase administrative file %q: %w", name, err)
	}
	inside, err := gitRebasePathInside(rebaseDir, resolved)
	if err != nil {
		return nil, 0, err
	}
	if !inside || filepath.Base(resolved) != name {
		return nil, 0, fmt.Errorf("rebase administrative file %q escaped its directory: %w", name, ErrInvalidInput)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, 0, fmt.Errorf("open rebase administrative file %q: %w", name, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("reinspect rebase administrative file %q: %w", name, err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, 0, fmt.Errorf("rebase administrative file %q changed while opening: %w", name, ErrInvalidInput)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return nil, 0, fmt.Errorf("read rebase administrative file %q: %w", name, err)
	}
	if int64(len(content)) > maxSize {
		return nil, 0, fmt.Errorf("rebase administrative file %q is too large: %w", name, ErrInvalidInput)
	}
	return content, info.Mode(), nil
}

func replaceSafeRebaseFile(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if _, _, err := readSafeRebaseFile(dir, name, gitRebaseMaxStateSize); err != nil {
		return err
	}
	return writeSafeRebaseFile(dir, name, content, perm, true)
}

func writeSafeRebaseFile(
	rebaseDir,
	name string,
	content []byte,
	perm os.FileMode,
	requireExisting bool,
) error {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return fmt.Errorf("invalid rebase administrative file name %q: %w", name, ErrInvalidInput)
	}
	if perm == 0 {
		perm = 0o600
	}
	target := filepath.Join(rebaseDir, name)
	info, err := os.Lstat(target)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) || requireExisting {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("rebase administrative file %q does not exist: %w", name, ErrNotFound)
			}
			return fmt.Errorf("inspect rebase administrative file %q: %w", name, err)
		}
	} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("rebase administrative file %q must be regular: %w", name, ErrInvalidInput)
	}
	temporary, err := os.CreateTemp(rebaseDir, ".koyori-ide-rebase-write-*")
	if err != nil {
		return fmt.Errorf("create temporary rebase administrative file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary rebase administrative file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary rebase administrative file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary rebase administrative file: %w", err)
	}
	if err := os.Chmod(temporaryPath, perm); err != nil {
		return fmt.Errorf("protect temporary rebase administrative file: %w", err)
	}
	if current, err := os.Lstat(target); err == nil {
		if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() {
			return fmt.Errorf("rebase administrative file %q became unsafe: %w", name, ErrInvalidInput)
		}
	} else if !errors.Is(err, os.ErrNotExist) || requireExisting {
		return fmt.Errorf("revalidate rebase administrative file %q: %w", name, err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return fmt.Errorf("replace rebase administrative file %q: %w", name, err)
	}
	verified, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("verify rebase administrative file %q: %w", name, err)
	}
	if verified.Mode()&os.ModeSymlink != 0 || !verified.Mode().IsRegular() {
		return fmt.Errorf("rebase administrative file %q became unsafe after writing: %w", name, ErrInvalidInput)
	}
	return nil
}

func readGitRebaseState(rebaseDir string) (gitRebasePersistentState, error) {
	content, _, err := readSafeRebaseFile(rebaseDir, gitRebaseStateFileName, gitRebaseMaxStateSize)
	if err != nil {
		return gitRebasePersistentState{}, fmt.Errorf("read Koyori IDE rebase state: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var state gitRebasePersistentState
	if err := decoder.Decode(&state); err != nil {
		return gitRebasePersistentState{}, fmt.Errorf("decode Koyori IDE rebase state: %w", ErrInvalidInput)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return gitRebasePersistentState{}, fmt.Errorf("Koyori IDE rebase state contains trailing data: %w", ErrInvalidInput)
	}
	if err := validateGitRebaseState(state); err != nil {
		return gitRebasePersistentState{}, err
	}
	return state, nil
}

func writeGitRebaseState(rebaseDir string, state gitRebasePersistentState) error {
	if err := validateGitRebaseState(state); err != nil {
		return err
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Koyori IDE rebase state: %w", err)
	}
	content = append(content, '\n')
	if len(content) > gitRebaseMaxStateSize {
		return fmt.Errorf("Koyori IDE rebase state is too large: %w", ErrInvalidInput)
	}
	if err := writeSafeRebaseFile(rebaseDir, gitRebaseStateFileName, content, 0o600, false); err != nil {
		return fmt.Errorf("write Koyori IDE rebase state: %w", err)
	}
	return nil
}

func validateGitRebaseState(state gitRebasePersistentState) error {
	if state.Version != gitRebaseStateVersion {
		return fmt.Errorf("unsupported Koyori IDE rebase state version %d: %w", state.Version, ErrInvalidInput)
	}
	switch state.Phase {
	case gitRebasePhaseAwaitingApply, gitRebasePhaseApplying, gitRebasePhaseReady, gitRebasePhaseStopped:
	default:
		return fmt.Errorf("invalid Koyori IDE rebase phase %q: %w", state.Phase, ErrInvalidInput)
	}
	if state.Phase == gitRebasePhaseStopped {
		switch state.StopReason {
		case gitRebaseStopReasonCommandError, gitRebaseStopReasonSyntheticEdit, gitRebaseStopReasonManual:
		default:
			return fmt.Errorf("stopped rebase state has an invalid reason %q: %w", state.StopReason, ErrInvalidInput)
		}
	} else if state.StopReason != "" {
		return fmt.Errorf("non-stopped rebase state has a stop reason: %w", ErrInvalidInput)
	}
	if !validFullGitRebaseCommit(state.OrigHead) || state.OrigHead != strings.ToLower(state.OrigHead) {
		return fmt.Errorf("invalid original HEAD in Koyori IDE rebase state: %w", ErrInvalidInput)
	}
	if !validFullGitRebaseCommit(state.Onto) || state.Onto != strings.ToLower(state.Onto) {
		return fmt.Errorf("invalid onto commit in Koyori IDE rebase state: %w", ErrInvalidInput)
	}
	if err := validateGitRebaseUpstreamInput(state.UpstreamRef); err != nil {
		return fmt.Errorf("invalid upstream ref in Koyori IDE rebase state: %w", err)
	}
	if !gitRebaseHashPattern.MatchString(state.TodoHash) {
		return fmt.Errorf("invalid todo hash in Koyori IDE rebase state: %w", ErrInvalidInput)
	}
	if state.Phase == gitRebasePhaseApplying {
		if !gitRebaseHashPattern.MatchString(state.PendingTodoHash) {
			return fmt.Errorf("applying rebase state has no pending todo hash: %w", ErrInvalidInput)
		}
	} else if state.PendingTodoHash != "" {
		return fmt.Errorf("non-applying rebase state has a pending todo hash: %w", ErrInvalidInput)
	}
	if len(state.ExpectedCommits) == 0 || len(state.ExpectedCommits) > gitRebaseMaxActions {
		return fmt.Errorf("invalid expected commit count in Koyori IDE rebase state: %w", ErrInvalidInput)
	}
	expected := make(map[string]struct{}, len(state.ExpectedCommits))
	for _, commit := range state.ExpectedCommits {
		if !validFullGitRebaseCommit(commit) || commit != strings.ToLower(commit) {
			return fmt.Errorf("invalid expected commit %q in Koyori IDE rebase state: %w", commit, ErrInvalidInput)
		}
		if _, duplicate := expected[commit]; duplicate {
			return fmt.Errorf("duplicate expected commit %s in Koyori IDE rebase state: %w", commit, ErrInvalidInput)
		}
		expected[commit] = struct{}{}
	}
	for commit, message := range state.RewordMessages {
		if _, ok := expected[commit]; !ok {
			return fmt.Errorf("reword commit %s is not part of this rebase: %w", commit, ErrInvalidInput)
		}
		if strings.IndexByte(message, 0) >= 0 {
			return fmt.Errorf("reword message for %s contains a NUL byte: %w", commit, ErrInvalidInput)
		}
	}
	if len(state.Actions) == 0 {
		return fmt.Errorf("Koyori IDE rebase state has no action snapshot: %w", ErrInvalidInput)
	}
	canonicalActions, err := canonicalizeGitRebaseActions(state.Actions, state.ExpectedCommits)
	if err != nil {
		return fmt.Errorf("invalid action snapshot in Koyori IDE rebase state: %w", err)
	}
	if !reflect.DeepEqual(canonicalActions, state.Actions) {
		return fmt.Errorf("Koyori IDE rebase action snapshot is not canonical: %w", ErrInvalidInput)
	}
	serialized, plannedRewords, err := prepareGitRebaseActions(state.Actions, state.ExpectedCommits)
	if err != nil {
		return fmt.Errorf("invalid action snapshot in Koyori IDE rebase state: %w", err)
	}
	if state.Phase == gitRebasePhaseApplying && gitRebaseContentHash([]byte(serialized)) != state.PendingTodoHash {
		return fmt.Errorf("applying action snapshot does not match its pending todo hash: %w", ErrInvalidInput)
	}
	for commit, message := range state.RewordMessages {
		if plannedRewords[commit] != message {
			return fmt.Errorf("reword message for %s does not match the action snapshot: %w", commit, ErrInvalidInput)
		}
	}
	if state.Phase == gitRebasePhaseApplying && !reflect.DeepEqual(plannedRewords, state.RewordMessages) {
		return fmt.Errorf("applying reword messages do not match the action snapshot: %w", ErrInvalidInput)
	}
	return nil
}

func validateGitRebaseStateBinding(rebaseDir string, state gitRebasePersistentState) error {
	if err := validateRebaseAdminCommit(rebaseDir, "orig-head", state.OrigHead); err != nil {
		return err
	}
	if err := validateRebaseAdminCommit(rebaseDir, "onto", state.Onto); err != nil {
		return err
	}
	return nil
}

func validateRebaseAdminCommit(rebaseDir, name, expected string) error {
	content, _, err := readSafeRebaseFile(rebaseDir, name, 256)
	if err != nil {
		return err
	}
	actual := strings.ToLower(strings.TrimSpace(string(content)))
	if !validFullGitRebaseCommit(actual) || actual != expected {
		return fmt.Errorf("rebase %s does not match the active Koyori IDE snapshot: %w", name, ErrInvalidInput)
	}
	return nil
}

func validateCurrentTodoHash(rebaseDir, expectedHash string) error {
	content, _, err := readSafeRebaseFile(rebaseDir, "git-rebase-todo", gitRebaseMaxStateSize)
	if err != nil {
		return err
	}
	if gitRebaseContentHash(content) != expectedHash {
		return fmt.Errorf("interactive rebase todo changed outside Koyori IDE: %w", ErrInvalidInput)
	}
	return nil
}

func refreshGitRebaseTodoHash(rebaseDir string, state *gitRebasePersistentState) error {
	content, _, err := readSafeRebaseFile(rebaseDir, "git-rebase-todo", gitRebaseMaxStateSize)
	if err != nil {
		return err
	}
	state.TodoHash = gitRebaseContentHash(content)
	state.PendingTodoHash = ""
	return nil
}

func persistGitRebaseStoppedState(
	rebaseDir string,
	state *gitRebasePersistentState,
	reason string,
) error {
	state.Phase = gitRebasePhaseStopped
	state.StopReason = reason
	if err := refreshGitRebaseTodoHash(rebaseDir, state); err != nil {
		return err
	}
	return writeGitRebaseState(rebaseDir, *state)
}

func gitRebaseCommitInSet(commit string, expected []string) bool {
	for _, item := range expected {
		if item == commit {
			return true
		}
	}
	return false
}

func cloneGitRebaseActions(actions []RebaseTodoAction) []RebaseTodoAction {
	if actions == nil {
		return nil
	}
	return append([]RebaseTodoAction(nil), actions...)
}

func canonicalizeGitRebaseActions(
	actions []RebaseTodoAction,
	expectedCommits []string,
) ([]RebaseTodoAction, error) {
	canonical := cloneGitRebaseActions(actions)
	for index := range canonical {
		if canonical[index].Action == "exec" {
			continue
		}
		commit, err := matchExpectedGitRebaseCommit(canonical[index].CommitSHA, expectedCommits)
		if err != nil {
			return nil, fmt.Errorf("action %d: %w", index+1, err)
		}
		canonical[index].CommitSHA = commit
	}
	return canonical, nil
}

func prepareGitRebaseActions(
	actions []RebaseTodoAction,
	expectedCommits []string,
) (string, map[string]string, error) {
	if len(actions) == 0 {
		return "", nil, fmt.Errorf("interactive rebase todo must contain every expected commit: %w", ErrInvalidInput)
	}
	translated := make([]RebaseTodoAction, 0, len(actions))
	seen := make(map[string]struct{}, len(expectedCommits))
	rewordMessages := make(map[string]string)
	for index, action := range actions {
		if action.Action == "exec" {
			translated = append(translated, action)
			continue
		}
		commit, err := matchExpectedGitRebaseCommit(action.CommitSHA, expectedCommits)
		if err != nil {
			return "", nil, fmt.Errorf("action %d: %w", index+1, err)
		}
		if _, duplicate := seen[commit]; duplicate {
			return "", nil, fmt.Errorf("commit %s appears more than once: %w", commit, ErrInvalidInput)
		}
		seen[commit] = struct{}{}
		action.CommitSHA = commit
		if action.Action == "reword" {
			if strings.IndexByte(action.LongMessage, 0) >= 0 {
				return "", nil, fmt.Errorf("reword action %d contains a NUL byte: %w", index+1, ErrInvalidInput)
			}
			rewordMessages[commit] = buildGitRebaseCommitMessage(action.ShortMessage, action.LongMessage)
			action.Action = "edit"
		}
		translated = append(translated, action)
	}
	for _, expected := range expectedCommits {
		if _, ok := seen[expected]; !ok {
			return "", nil, fmt.Errorf("commit %s is missing; use an explicit drop action: %w", expected, ErrInvalidInput)
		}
	}
	serialized, err := serializeGitRebaseActions(translated)
	if err != nil {
		return "", nil, err
	}
	if len(rewordMessages) == 0 {
		rewordMessages = nil
	}
	return serialized, rewordMessages, nil
}

func matchExpectedGitRebaseCommit(value string, expected []string) (string, error) {
	if value != strings.TrimSpace(value) || !gitRebaseCommitPattern.MatchString(value) {
		return "", fmt.Errorf("invalid commit id %q: %w", value, ErrInvalidInput)
	}
	prefix := strings.ToLower(value)
	match := ""
	for _, commit := range expected {
		if strings.HasPrefix(commit, prefix) {
			if match != "" {
				return "", fmt.Errorf("commit id %q is ambiguous: %w", value, ErrInvalidInput)
			}
			match = commit
		}
	}
	if match == "" {
		return "", fmt.Errorf("commit %q does not belong to this rebase: %w", value, ErrInvalidInput)
	}
	return match, nil
}

func buildGitRebaseCommitMessage(shortMessage, longMessage string) string {
	body := strings.TrimRight(longMessage, "\r\n")
	if body == "" {
		return shortMessage + "\n"
	}
	return shortMessage + "\n\n" + body + "\n"
}

func validateRebaseTodoCommitSet(content []byte, expectedCommits []string) error {
	seen := make(map[string]struct{}, len(expectedCommits))
	for lineNumber, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		command := fields[0]
		switch command {
		case "exec", "x", "break", "b", "noop":
			continue
		case "pick", "p", "reword", "r", "edit", "e", "squash", "s", "fixup", "f", "drop", "d":
		default:
			return fmt.Errorf("unexpected generated rebase instruction %q on line %d: %w", command, lineNumber+1, ErrInvalidInput)
		}
		if len(fields) < 2 {
			return fmt.Errorf("generated rebase instruction on line %d has no commit: %w", lineNumber+1, ErrInvalidInput)
		}
		commit, err := matchExpectedGitRebaseCommit(fields[1], expectedCommits)
		if err != nil {
			return fmt.Errorf("generated rebase instruction on line %d: %w", lineNumber+1, err)
		}
		if _, duplicate := seen[commit]; duplicate {
			return fmt.Errorf("generated rebase todo repeats commit %s: %w", commit, ErrInvalidInput)
		}
		seen[commit] = struct{}{}
	}
	for _, commit := range expectedCommits {
		if _, ok := seen[commit]; !ok {
			return fmt.Errorf("generated rebase todo is missing commit %s: %w", commit, ErrInvalidInput)
		}
	}
	return nil
}

func parseGitRebaseLog(output string) ([]RebaseTodoAction, error) {
	if strings.TrimSpace(output) == "" {
		return []RebaseTodoAction{}, nil
	}
	// GetRebaseTodoList requests -z and emits six NUL-delimited fields per
	// commit. Grouping by a fixed field count remains correct when both subject
	// and body are empty (the old triple-NUL framing did not).
	fields := strings.Split(output, "\x00")
	if len(fields) > 0 && fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%6 != 0 {
		return nil, fmt.Errorf("git log output has %d fields, expected a multiple of 6: %w", len(fields), ErrInvalidInput)
	}
	actions := make([]RebaseTodoAction, 0, len(fields)/6)
	for index := 0; index < len(fields); index += 6 {
		sha := strings.TrimSpace(fields[index])
		if !gitRebaseCommitPattern.MatchString(sha) || (len(sha) != 40 && len(sha) != 64) {
			return nil, fmt.Errorf("git log returned invalid commit id %q: %w", sha, ErrInvalidInput)
		}
		action := RebaseTodoAction{
			Action:       "pick",
			CommitSHA:    strings.ToLower(sha),
			ShortMessage: fields[index+1],
			LongMessage:  strings.TrimRight(fields[index+2], "\r\n"),
			AuthorName:   fields[index+3],
			AuthorEmail:  fields[index+4],
			Date:         strings.TrimSpace(fields[index+5]),
		}
		actions = append(actions, action)
	}
	return actions, nil
}

func serializeGitRebaseActions(actions []RebaseTodoAction) (string, error) {
	if len(actions) == 0 {
		return "", fmt.Errorf("interactive rebase todo must contain at least one action: %w", ErrInvalidInput)
	}
	if len(actions) > gitRebaseMaxActions {
		return "", fmt.Errorf("interactive rebase todo exceeds %d actions: %w", gitRebaseMaxActions, ErrInvalidInput)
	}
	seenCommits := make(map[string]struct{}, len(actions))
	lines := make([]string, 0, len(actions))
	hasReplayTarget := false
	for index, item := range actions {
		if item.Action != strings.ToLower(strings.TrimSpace(item.Action)) {
			return "", fmt.Errorf("action %d has a non-canonical name %q: %w", index+1, item.Action, ErrInvalidInput)
		}
		switch item.Action {
		case "exec":
			command := strings.TrimSpace(item.ShortMessage)
			if command == "" || strings.ContainsAny(command, "\x00\r\n") {
				return "", fmt.Errorf("exec action %d has an invalid command: %w", index+1, ErrInvalidInput)
			}
			lines = append(lines, "exec "+command)
			continue
		case "pick", "reword", "edit", "squash", "fixup", "drop":
			// Valid commit actions continue below.
		default:
			return "", fmt.Errorf("unsupported rebase action %q at position %d: %w", item.Action, index+1, ErrInvalidInput)
		}

		sha := strings.TrimSpace(item.CommitSHA)
		if sha != item.CommitSHA || !gitRebaseCommitPattern.MatchString(sha) {
			return "", fmt.Errorf("action %d has an invalid commit id %q: %w", index+1, item.CommitSHA, ErrInvalidInput)
		}
		key := strings.ToLower(sha)
		if _, duplicate := seenCommits[key]; duplicate {
			return "", fmt.Errorf("commit %s appears more than once: %w", sha, ErrInvalidInput)
		}
		seenCommits[key] = struct{}{}
		if strings.ContainsAny(item.ShortMessage, "\x00\r\n") {
			return "", fmt.Errorf("action %d contains a multiline short message: %w", index+1, ErrInvalidInput)
		}
		if (item.Action == "squash" || item.Action == "fixup") && !hasReplayTarget {
			return "", fmt.Errorf("%s action at position %d has no previous commit: %w", item.Action, index+1, ErrInvalidInput)
		}
		if item.Action != "drop" {
			hasReplayTarget = true
		}
		line := item.Action + " " + sha
		if item.ShortMessage != "" {
			line += " " + item.ShortMessage
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func createGitRebasePauseEditor() (string, func(), error) {
	file, err := os.CreateTemp("", "koyori-ide-rebase-sequence-*.sh")
	if err != nil {
		return "", func() {}, fmt.Errorf("create rebase sequence editor: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	const script = `#!/bin/sh
set -eu
todo=$1
temporary="${todo}.koyori-ide.$$"
trap 'rm -f "$temporary"' EXIT HUP INT TERM
printf 'break\n' > "$temporary"
cat "$todo" >> "$temporary"
mv "$temporary" "$todo"
trap - EXIT HUP INT TERM
`
	if _, err := io.WriteString(file, script); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write rebase sequence editor: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close rebase sequence editor: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("make rebase sequence editor executable: %w", err)
	}
	return path, cleanup, nil
}

func quoteGitShellWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func gitRebaseCommandError(args []string, output string, err error) error {
	detail := strings.TrimSpace(output)
	if len(detail) > 4096 {
		detail = detail[:4096] + "..."
	}
	if detail == "" || strings.Contains(err.Error(), detail) {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, detail)
}

func gitRebaseDependencyIsNil(dependency any) bool {
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
