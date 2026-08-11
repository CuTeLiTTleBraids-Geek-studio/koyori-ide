package services

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newGitRebaseTestService(
	t *testing.T,
	runner GitRebaseCommandRunner,
) *GitRebaseService {
	t.Helper()
	service, err := NewGitRebaseServiceWithDependencies(GitRebaseDependencies{
		Runner: runner,
		RepositoryValidator: GitRebaseRepositoryValidatorFunc(func(string) error {
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("NewGitRebaseServiceWithDependencies: %v", err)
	}
	return service
}

func TestGitRebaseServiceGetTodoListParsesOldestFirstMetadata(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const upstreamSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const firstSHA = "1111111111111111111111111111111111111111"
	const secondSHA = "2222222222222222222222222222222222222222"
	var calls [][]string
	runner := GitRebaseCommandRunnerFunc(func(
		ctx context.Context,
		repoPath string,
		_ []string,
		args ...string,
	) (string, error) {
		if repoPath != repo {
			t.Fatalf("repoPath = %q, want %q", repoPath, repo)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("git command has no deadline")
		}
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"rev-parse", "--verify", "--end-of-options", "main^{commit}"}):
			return upstreamSHA + "\n", nil
		case reflect.DeepEqual(args, []string{"rev-parse", "--absolute-git-dir"}):
			return gitDir + "\n", nil
		case args[0] == "log":
			return firstSHA + "\x00first subject\x00first body\n\x00Ada\x00ada@example.com\x002026-07-17T10:00:00Z\x00" +
				secondSHA + "\x00second subject\x00\x00Lin\x00lin@example.com\x002026-07-18T10:00:00Z\x00", nil
		default:
			t.Fatalf("unexpected git command: %v", args)
			return "", nil
		}
	})

	service := newGitRebaseTestService(t, runner)
	actions, err := service.GetRebaseTodoList(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("GetRebaseTodoList: %v", err)
	}
	want := []RebaseTodoAction{
		{
			Action:       "pick",
			CommitSHA:    firstSHA,
			ShortMessage: "first subject",
			LongMessage:  "first body",
			AuthorName:   "Ada",
			AuthorEmail:  "ada@example.com",
			Date:         "2026-07-17T10:00:00Z",
		},
		{
			Action:       "pick",
			CommitSHA:    secondSHA,
			ShortMessage: "second subject",
			AuthorName:   "Lin",
			AuthorEmail:  "lin@example.com",
			Date:         "2026-07-18T10:00:00Z",
		},
	}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("actions = %#v, want %#v", actions, want)
	}
	if len(calls) != 3 {
		t.Fatalf("git calls = %d, want 3", len(calls))
	}
	if got := strings.Join(calls[1], " "); got != "rev-parse --verify --end-of-options main^{commit}" {
		t.Fatalf("resolve command = %q", got)
	}
	logCommand := strings.Join(calls[2], " ")
	for _, fragment := range []string{"-z", "--reverse", "--topo-order", "--no-merges", upstreamSHA + "..HEAD"} {
		if !strings.Contains(logCommand, fragment) {
			t.Fatalf("log command %q does not contain %q", logCommand, fragment)
		}
	}
}

func TestSerializeGitRebaseActions(t *testing.T) {
	actions := []RebaseTodoAction{
		{Action: "pick", CommitSHA: "aaaaaaa", ShortMessage: "first"},
		{Action: "reword", CommitSHA: "bbbbbbb", ShortMessage: "second"},
		{Action: "fixup", CommitSHA: "ccccccc", ShortMessage: "third"},
		{Action: "exec", ShortMessage: "go test ./..."},
		{Action: "drop", CommitSHA: "ddddddd", ShortMessage: "unused"},
	}
	serialized, err := serializeGitRebaseActions(actions)
	if err != nil {
		t.Fatalf("serializeGitRebaseActions: %v", err)
	}
	want := "pick aaaaaaa first\n" +
		"reword bbbbbbb second\n" +
		"fixup ccccccc third\n" +
		"exec go test ./...\n" +
		"drop ddddddd unused\n"
	if serialized != want {
		t.Fatalf("serialized = %q, want %q", serialized, want)
	}
}

func TestSerializeGitRebaseActionsRejectsUnsafeOrInvalidInstructions(t *testing.T) {
	tests := []struct {
		name    string
		actions []RebaseTodoAction
	}{
		{name: "empty", actions: nil},
		{name: "unsupported", actions: []RebaseTodoAction{{Action: "merge", CommitSHA: "aaaaaaa"}}},
		{name: "non canonical", actions: []RebaseTodoAction{{Action: "PICK", CommitSHA: "aaaaaaa"}}},
		{name: "sha injection", actions: []RebaseTodoAction{{Action: "pick", CommitSHA: "aaaaaaa\nexec calc"}}},
		{name: "message injection", actions: []RebaseTodoAction{{Action: "pick", CommitSHA: "aaaaaaa", ShortMessage: "ok\ndrop bbbbbbb"}}},
		{name: "first squash", actions: []RebaseTodoAction{{Action: "squash", CommitSHA: "aaaaaaa"}}},
		{name: "drop then fixup", actions: []RebaseTodoAction{
			{Action: "drop", CommitSHA: "aaaaaaa"},
			{Action: "fixup", CommitSHA: "bbbbbbb"},
		}},
		{name: "duplicate", actions: []RebaseTodoAction{
			{Action: "pick", CommitSHA: "aaaaaaa"},
			{Action: "edit", CommitSHA: "AAAAAAA"},
		}},
		{name: "multiline exec", actions: []RebaseTodoAction{{Action: "exec", ShortMessage: "go test\nrm file"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := serializeGitRebaseActions(test.actions); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestGitRebaseServiceApplyActionsWritesOnlyActiveTodo(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	rebaseDir := filepath.Join(gitDir, "rebase-merge")
	if err := os.MkdirAll(rebaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	todoPath := filepath.Join(rebaseDir, "git-rebase-todo")
	const firstSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const secondSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	initialTodo := []byte("pick " + firstSHA + " first\npick " + secondSHA + " second\n")
	if err := os.WriteFile(todoPath, initialTodo, 0o640); err != nil {
		t.Fatal(err)
	}
	writeGitRebaseTestSnapshot(t, rebaseDir, initialTodo, []string{firstSHA, secondSHA})
	runner := GitRebaseCommandRunnerFunc(func(
		_ context.Context,
		_ string,
		_ []string,
		args ...string,
	) (string, error) {
		if strings.Join(args, " ") != "rev-parse --absolute-git-dir" {
			t.Fatalf("unexpected git command: %v", args)
		}
		return gitDir + "\n", nil
	})
	service := newGitRebaseTestService(t, runner)
	actions := []RebaseTodoAction{
		{Action: "pick", CommitSHA: firstSHA, ShortMessage: "first"},
		{Action: "edit", CommitSHA: secondSHA, ShortMessage: "second"},
	}
	if err := service.ApplyRebaseActions(context.Background(), repo, actions); err != nil {
		t.Fatalf("ApplyRebaseActions: %v", err)
	}
	content, err := os.ReadFile(todoPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "pick "+firstSHA+" first\nedit "+secondSHA+" second\n"; got != want {
		t.Fatalf("todo = %q, want %q", got, want)
	}
	info, err := os.Stat(todoPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("todo mode = %o, want 640", info.Mode().Perm())
	}
}

func TestGitRebaseServiceApplyActionsRecoversInterruptedTransition(t *testing.T) {
	const firstSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const secondSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	actions := []RebaseTodoAction{
		{Action: "reword", CommitSHA: firstSHA, ShortMessage: "renamed", LongMessage: "body"},
		{Action: "drop", CommitSHA: secondSHA, ShortMessage: "second"},
	}
	serialized, messages, err := prepareGitRebaseActions(actions, []string{firstSHA, secondSHA})
	if err != nil {
		t.Fatal(err)
	}
	for _, todoAlreadyWritten := range []bool{false, true} {
		name := "before todo write"
		if todoAlreadyWritten {
			name = "after todo write"
		}
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			gitDir := filepath.Join(repo, ".git")
			rebaseDir := filepath.Join(gitDir, "rebase-merge")
			if err := os.MkdirAll(rebaseDir, 0o755); err != nil {
				t.Fatal(err)
			}
			initialTodo := []byte("pick " + firstSHA + " first\npick " + secondSHA + " second\n")
			todo := initialTodo
			if todoAlreadyWritten {
				todo = []byte(serialized)
			}
			if err := os.WriteFile(filepath.Join(rebaseDir, "git-rebase-todo"), todo, 0o600); err != nil {
				t.Fatal(err)
			}
			writeGitRebaseTestSnapshot(t, rebaseDir, initialTodo, []string{firstSHA, secondSHA})
			state, err := readGitRebaseState(rebaseDir)
			if err != nil {
				t.Fatal(err)
			}
			state.Phase = gitRebasePhaseApplying
			state.PendingTodoHash = gitRebaseContentHash([]byte(serialized))
			state.RewordMessages = messages
			state.Actions = cloneGitRebaseActions(actions)
			if err := writeGitRebaseState(rebaseDir, state); err != nil {
				t.Fatal(err)
			}
			runner := GitRebaseCommandRunnerFunc(func(
				_ context.Context,
				_ string,
				_ []string,
				args ...string,
			) (string, error) {
				if !reflect.DeepEqual(args, []string{"rev-parse", "--absolute-git-dir"}) {
					t.Fatalf("unexpected git command: %v", args)
				}
				return gitDir, nil
			})
			service := newGitRebaseTestService(t, runner)
			if err := service.ApplyRebaseActions(context.Background(), repo, actions); err != nil {
				t.Fatalf("ApplyRebaseActions recovery: %v", err)
			}
			state, err = readGitRebaseState(rebaseDir)
			if err != nil {
				t.Fatal(err)
			}
			if state.Phase != gitRebasePhaseReady || state.PendingTodoHash != "" {
				t.Fatalf("recovered state = %#v", state)
			}
		})
	}
}

func TestGitRebaseServiceApplyActionsRejectsSymlinkTodo(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	rebaseDir := filepath.Join(gitDir, "rebase-merge")
	if err := os.MkdirAll(rebaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-todo")
	if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rebaseDir, "git-rebase-todo")); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	runner := GitRebaseCommandRunnerFunc(func(
		_ context.Context,
		_ string,
		_ []string,
		_ ...string,
	) (string, error) {
		return gitDir, nil
	})
	service := newGitRebaseTestService(t, runner)
	err := service.ApplyRebaseActions(context.Background(), repo, []RebaseTodoAction{
		{Action: "pick", CommitSHA: "aaaaaaa", ShortMessage: "first"},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	content, readErr := os.ReadFile(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "unchanged" {
		t.Fatalf("outside file was modified: %q", content)
	}
}

func TestGitRebaseServiceIsRebaseInProgressChecksBothBackends(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := GitRebaseCommandRunnerFunc(func(
		_ context.Context,
		_ string,
		_ []string,
		_ ...string,
	) (string, error) {
		return gitDir, nil
	})
	service := newGitRebaseTestService(t, runner)
	inProgress, err := service.IsRebaseInProgress(repo)
	if err != nil || inProgress {
		t.Fatalf("initial IsRebaseInProgress = %v, %v", inProgress, err)
	}
	if err := os.Mkdir(filepath.Join(gitDir, "rebase-apply"), 0o755); err != nil {
		t.Fatal(err)
	}
	inProgress, err = service.IsRebaseInProgress(repo)
	if err != nil || !inProgress {
		t.Fatalf("apply backend IsRebaseInProgress = %v, %v", inProgress, err)
	}
}

func TestGitRebaseServiceGetRebaseStatusDistinguishesOwnershipAndPhases(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := GitRebaseCommandRunnerFunc(func(
		_ context.Context,
		_ string,
		_ []string,
		args ...string,
	) (string, error) {
		if !reflect.DeepEqual(args, []string{"rev-parse", "--absolute-git-dir"}) {
			t.Fatalf("unexpected git command: %v", args)
		}
		return gitDir, nil
	})
	service := newGitRebaseTestService(t, runner)
	status, err := service.GetRebaseStatus(repo)
	if err != nil || status.InProgress || status.Owned {
		t.Fatalf("idle status = %#v, %v", status, err)
	}

	applyDir := filepath.Join(gitDir, "rebase-apply")
	if err := os.Mkdir(applyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	status, err = service.GetRebaseStatus(repo)
	if err != nil || !status.InProgress || status.Owned {
		t.Fatalf("foreign apply status = %#v, %v", status, err)
	}
	if err := os.Remove(applyDir); err != nil {
		t.Fatal(err)
	}

	const commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rebaseDir := filepath.Join(gitDir, "rebase-merge")
	if err := os.Mkdir(rebaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	todo := []byte("pick " + commit + "\n")
	if err := os.WriteFile(filepath.Join(rebaseDir, "git-rebase-todo"), todo, 0o600); err != nil {
		t.Fatal(err)
	}
	status, err = service.GetRebaseStatus(repo)
	if err != nil || !status.InProgress || status.Owned {
		t.Fatalf("foreign merge status = %#v, %v", status, err)
	}
	if err := service.AbortRebase(context.Background(), repo); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign AbortRebase error = %v, want ErrNotFound", err)
	}
	writeGitRebaseTestSnapshot(t, rebaseDir, todo, []string{commit})
	for _, phase := range []string{
		gitRebasePhaseAwaitingApply,
		gitRebasePhaseApplying,
		gitRebasePhaseReady,
		gitRebasePhaseStopped,
	} {
		t.Run(phase, func(t *testing.T) {
			state, err := readGitRebaseState(rebaseDir)
			if err != nil {
				t.Fatal(err)
			}
			state.Phase = phase
			state.PendingTodoHash = ""
			state.StopReason = ""
			if phase == gitRebasePhaseStopped {
				state.StopReason = gitRebaseStopReasonManual
			}
			if phase == gitRebasePhaseApplying {
				serialized, _, err := prepareGitRebaseActions(state.Actions, state.ExpectedCommits)
				if err != nil {
					t.Fatal(err)
				}
				state.PendingTodoHash = gitRebaseContentHash([]byte(serialized))
			}
			if err := writeGitRebaseState(rebaseDir, state); err != nil {
				t.Fatal(err)
			}
			status, err := service.GetRebaseStatus(repo)
			if err != nil {
				t.Fatal(err)
			}
			if !status.InProgress || !status.Owned || status.Phase != phase ||
				status.StopReason != state.StopReason ||
				status.Upstream != state.Onto || status.UpstreamRef != state.UpstreamRef ||
				status.OrigHead != state.OrigHead ||
				!reflect.DeepEqual(status.Actions, state.Actions) {
				t.Fatalf("status = %#v, state = %#v", status, state)
			}
		})
	}
}

func TestGitRebaseServiceStartInjectsPauseSequenceEditor(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const upstreamSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const origHeadSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	seenRebase := false
	runner := GitRebaseCommandRunnerFunc(func(
		_ context.Context,
		_ string,
		extraEnv []string,
		args ...string,
	) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"rev-parse", "--absolute-git-dir"}):
			return gitDir, nil
		case reflect.DeepEqual(args, []string{"rev-parse", "--verify", "--end-of-options", "main^{commit}"}):
			return upstreamSHA, nil
		case reflect.DeepEqual(args, []string{"rev-parse", "--verify", "--end-of-options", "HEAD^{commit}"}):
			return origHeadSHA, nil
		case reflect.DeepEqual(args, []string{
			"rev-list", "--reverse", "--topo-order", "--no-merges", upstreamSHA + ".." + origHeadSHA,
		}):
			return origHeadSHA + "\n", nil
		case len(args) > 0 && args[0] == "log":
			return origHeadSHA + "\x00subject\x00\x00Rebase Test\x00rebase@example.com\x002026-07-18T10:00:00Z\x00", nil
		case len(args) > 4 && args[len(args)-4] == "rebase":
			seenRebase = true
			want := "-c rebase.rebaseMerges=false -c rebase.updateRefs=false " +
				"-c rebase.abbreviateCommands=false -c rebase.instructionFormat=%s " +
				"rebase -i --no-autosquash " + upstreamSHA
			if got := strings.Join(args, " "); got != want {
				t.Fatalf("rebase command = %q", got)
			}
			var editorSetting string
			for _, item := range extraEnv {
				if strings.HasPrefix(item, "GIT_SEQUENCE_EDITOR=") {
					editorSetting = strings.TrimPrefix(item, "GIT_SEQUENCE_EDITOR=")
				}
			}
			if editorSetting == "" {
				t.Fatal("GIT_SEQUENCE_EDITOR was not injected")
			}
			editorPath := filepath.FromSlash(strings.Trim(editorSetting, "'"))
			script, err := os.ReadFile(editorPath)
			if err != nil {
				t.Fatalf("read injected sequence editor: %v", err)
			}
			if !strings.Contains(string(script), "printf 'break\\n'") {
				t.Fatalf("sequence editor does not pause: %q", script)
			}
			rebaseDir := filepath.Join(gitDir, "rebase-merge")
			if err := os.MkdirAll(rebaseDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{
				"orig-head":       origHeadSHA + "\n",
				"onto":            upstreamSHA + "\n",
				"git-rebase-todo": "pick " + origHeadSHA + " subject\n",
			} {
				if err := os.WriteFile(filepath.Join(rebaseDir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			return "", nil
		default:
			t.Fatalf("unexpected git command: %v", args)
			return "", nil
		}
	})
	service := newGitRebaseTestService(t, runner)
	if err := service.StartInteractiveRebase(context.Background(), repo, "main"); err != nil {
		t.Fatalf("StartInteractiveRebase: %v", err)
	}
	if !seenRebase {
		t.Fatal("rebase command was not executed")
	}
}

func TestGitRebaseServiceCommandsHaveTimeout(t *testing.T) {
	repo := t.TempDir()
	runner := GitRebaseCommandRunnerFunc(func(
		ctx context.Context,
		_ string,
		_ []string,
		_ ...string,
	) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	service, err := NewGitRebaseServiceWithDependencies(GitRebaseDependencies{
		Runner: runner,
		RepositoryValidator: GitRebaseRepositoryValidatorFunc(func(string) error {
			return nil
		}),
		CommandTimeout: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = service.ContinueRebase(context.Background(), repo)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
}

func TestGitRebaseServiceContinueClassifiesNonConflictFailureAsManual(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	rebaseDir := filepath.Join(gitDir, "rebase-merge")
	if err := os.MkdirAll(rebaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	todo := []byte("pick " + commit + " subject\n")
	if err := os.WriteFile(filepath.Join(rebaseDir, "git-rebase-todo"), todo, 0o600); err != nil {
		t.Fatal(err)
	}
	writeGitRebaseTestSnapshot(t, rebaseDir, todo, []string{commit})
	state, err := readGitRebaseState(rebaseDir)
	if err != nil {
		t.Fatal(err)
	}
	state.Phase = gitRebasePhaseReady
	if err := writeGitRebaseState(rebaseDir, state); err != nil {
		t.Fatal(err)
	}

	continueFailure := errors.New("non-conflict continue failure")
	runner := GitRebaseCommandRunnerFunc(func(
		_ context.Context,
		_ string,
		_ []string,
		args ...string,
	) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"rev-parse", "--absolute-git-dir"}):
			return gitDir, nil
		case reflect.DeepEqual(args, []string{
			"rev-parse",
			"--verify",
			"--quiet",
			"REBASE_HEAD^{commit}",
		}):
			return commit, nil
		case reflect.DeepEqual(args, []string{"rebase", "--continue"}):
			return "", continueFailure
		case reflect.DeepEqual(args, []string{
			"diff",
			"--name-only",
			"--diff-filter=U",
			"-z",
		}):
			return "", nil
		default:
			t.Fatalf("unexpected git command: %v", args)
			return "", nil
		}
	})
	service := newGitRebaseTestService(t, runner)
	if err := service.ContinueRebase(context.Background(), repo); !errors.Is(err, continueFailure) {
		t.Fatalf("ContinueRebase error = %v, want wrapped failure", err)
	}
	state, err = readGitRebaseState(rebaseDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != gitRebasePhaseStopped || state.StopReason != gitRebaseStopReasonManual {
		t.Fatalf("stopped state = %#v", state)
	}
	if err := service.SkipCommit(context.Background(), repo); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("SkipCommit error = %v, want ErrInvalidInput", err)
	}
}
func TestGitRebaseGitServiceAdapterUsesPublicBoundary(t *testing.T) {
	provider := &gitRebaseTestBranchProvider{}
	adapter := NewGitRebaseGitServiceAdapter(provider)
	if err := adapter.ValidateRepository("/repo"); err != nil {
		t.Fatalf("ValidateRepository: %v", err)
	}
	if provider.path != "/repo" {
		t.Fatalf("provider path = %q", provider.path)
	}
	if err := NewGitRebaseGitServiceAdapter(nil).ValidateRepository("/repo"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil adapter error = %v", err)
	}
}

func TestGitRebaseServiceRealGitLogAllowsEmptySubjectAndBody(t *testing.T) {
	repo := newRealGitRebaseRepository(t)
	baseSHA := commitRealGitRebaseFile(t, repo, "base.txt", "base\n", "base")
	runGitRebaseTestCommand(t, repo, "commit", "-q", "--allow-empty", "--allow-empty-message", "-m", "")
	commitRealGitRebaseFile(t, repo, "later.txt", "later\n", "later")

	service := newGitRebaseTestService(t, GitRebaseExecRunner{})
	actions, err := service.GetRebaseTodoList(context.Background(), repo, baseSHA)
	if err != nil {
		t.Fatalf("GetRebaseTodoList: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("actions = %#v, want two commits", actions)
	}
	if actions[0].ShortMessage != "" || actions[0].LongMessage != "" {
		t.Fatalf("empty commit metadata = %#v", actions[0])
	}
	if actions[1].ShortMessage != "later" {
		t.Fatalf("second commit = %#v", actions[1])
	}
}

func TestGitRebaseServiceRealGitRewordChangesMessage(t *testing.T) {
	repo := newRealGitRebaseRepository(t)
	baseSHA := commitRealGitRebaseFile(t, repo, "base.txt", "base\n", "base")
	commitRealGitRebaseFile(t, repo, "first.txt", "first\n", "first")
	commitRealGitRebaseFile(t, repo, "second.txt", "second\n", "second")

	service := newGitRebaseTestService(t, GitRebaseExecRunner{})
	ctx := context.Background()
	actions, err := service.GetRebaseTodoList(ctx, repo, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	actions[0].Action = "reword"
	actions[0].ShortMessage = "renamed first"
	actions[0].LongMessage = "controlled body"
	if err := service.StartInteractiveRebase(ctx, repo, baseSHA); err != nil {
		t.Fatalf("StartInteractiveRebase: %v", err)
	}
	defer func() { _ = service.AbortRebase(context.Background(), repo) }()
	if err := service.ApplyRebaseActions(ctx, repo, actions); err != nil {
		t.Fatalf("ApplyRebaseActions: %v", err)
	}
	if err := service.ContinueRebase(ctx, repo); err != nil {
		t.Fatalf("ContinueRebase: %v", err)
	}

	subjects := strings.Fields(runGitRebaseTestCommand(t, repo, "log", "--reverse", "--format=%s", baseSHA+"..HEAD"))
	if got, want := strings.Join(subjects, " "), "renamed first second"; got != want {
		t.Fatalf("subjects = %q, want %q", got, want)
	}
	firstRewritten := strings.TrimSpace(runGitRebaseTestCommand(t, repo, "rev-list", "--reverse", baseSHA+"..HEAD"))
	firstRewritten = strings.Fields(firstRewritten)[0]
	message := strings.ReplaceAll(runGitRebaseTestCommand(t, repo, "show", "-s", "--format=%B", firstRewritten), "\r\n", "\n")
	if strings.TrimSpace(message) != "renamed first\n\ncontrolled body" {
		t.Fatalf("rewritten message = %q", message)
	}
}

func TestGitRebaseServiceRejectsControlBeforeActionsAreApplied(t *testing.T) {
	repo := newRealGitRebaseRepository(t)
	baseSHA := commitRealGitRebaseFile(t, repo, "base.txt", "base\n", "base")
	origHead := commitRealGitRebaseFile(t, repo, "change.txt", "change\n", "change")
	runGitRebaseTestCommand(t, repo, "branch", "upstream-ref", baseSHA)
	service := newGitRebaseTestService(t, GitRebaseExecRunner{})
	ctx := context.Background()
	if err := service.StartInteractiveRebase(ctx, repo, "upstream-ref"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.AbortRebase(context.Background(), repo) }()
	runGitRebaseTestCommand(t, repo, "branch", "-f", "upstream-ref", origHead)
	restartedService := newGitRebaseTestService(t, GitRebaseExecRunner{})
	recoveredActions, err := restartedService.GetRebaseTodoList(ctx, repo, "upstream-ref")
	if err != nil {
		t.Fatalf("GetRebaseTodoList after restart: %v", err)
	}
	if len(recoveredActions) != 1 || recoveredActions[0].ShortMessage != "change" {
		t.Fatalf("recovered actions = %#v", recoveredActions)
	}
	if _, err := restartedService.GetRebaseTodoList(ctx, repo, "different-upstream"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("wrong upstream recovery error = %v, want ErrInvalidInput", err)
	}
	if err := service.ContinueRebase(ctx, repo); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ContinueRebase error = %v, want ErrInvalidInput", err)
	}
	if err := service.SkipCommit(ctx, repo); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("SkipCommit error = %v, want ErrInvalidInput", err)
	}
}

func TestGitRebaseServiceApplyRejectsForeignMissingAndStaleActions(t *testing.T) {
	repo := newRealGitRebaseRepository(t)
	baseSHA := commitRealGitRebaseFile(t, repo, "base.txt", "base\n", "base")
	commitRealGitRebaseFile(t, repo, "first.txt", "first\n", "first")
	commitRealGitRebaseFile(t, repo, "second.txt", "second\n", "second")
	service := newGitRebaseTestService(t, GitRebaseExecRunner{})
	ctx := context.Background()
	actions, err := service.GetRebaseTodoList(ctx, repo, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.StartInteractiveRebase(ctx, repo, baseSHA); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.AbortRebase(context.Background(), repo) }()

	foreign := append([]RebaseTodoAction(nil), actions...)
	foreign[0].CommitSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err := service.ApplyRebaseActions(ctx, repo, foreign); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("foreign ApplyRebaseActions error = %v, want ErrInvalidInput", err)
	}
	if err := service.ApplyRebaseActions(ctx, repo, actions[:1]); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing ApplyRebaseActions error = %v, want ErrInvalidInput", err)
	}

	rebaseDir := filepath.Join(repo, ".git", "rebase-merge")
	todoPath := filepath.Join(rebaseDir, "git-rebase-todo")
	if err := os.WriteFile(todoPath, []byte("pick "+actions[0].CommitSHA+" tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyRebaseActions(ctx, repo, actions); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("stale todo ApplyRebaseActions error = %v, want ErrInvalidInput", err)
	}
}

func TestGitRebaseServiceApplyRejectsMissingOrSymlinkState(t *testing.T) {
	for _, useSymlink := range []bool{false, true} {
		name := "missing"
		if useSymlink {
			name = "symlink"
		}
		t.Run(name, func(t *testing.T) {
			repo := newRealGitRebaseRepository(t)
			baseSHA := commitRealGitRebaseFile(t, repo, "base.txt", "base\n", "base")
			commitRealGitRebaseFile(t, repo, "change.txt", "change\n", "change")
			service := newGitRebaseTestService(t, GitRebaseExecRunner{})
			ctx := context.Background()
			actions, err := service.GetRebaseTodoList(ctx, repo, baseSHA)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.StartInteractiveRebase(ctx, repo, baseSHA); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = service.AbortRebase(context.Background(), repo) }()
			statePath := filepath.Join(repo, ".git", "rebase-merge", gitRebaseStateFileName)
			if err := os.Remove(statePath); err != nil {
				t.Fatal(err)
			}
			if useSymlink {
				outside := filepath.Join(t.TempDir(), "outside-state")
				if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, statePath); err != nil {
					t.Skipf("symlink is unavailable: %v", err)
				}
			}
			err = service.ApplyRebaseActions(ctx, repo, actions)
			if useSymlink {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("error = %v, want ErrInvalidInput", err)
				}
			} else if !errors.Is(err, ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestGitRebaseServiceRealGitReorderDropAndAbort(t *testing.T) {
	t.Run("reorder and drop", func(t *testing.T) {
		repo := newRealGitRebaseRepository(t)
		baseSHA := commitRealGitRebaseFile(t, repo, "base.txt", "base\n", "base")
		commitRealGitRebaseFile(t, repo, "a.txt", "a\n", "a")
		commitRealGitRebaseFile(t, repo, "b.txt", "b\n", "b")
		commitRealGitRebaseFile(t, repo, "c.txt", "c\n", "c")
		service := newGitRebaseTestService(t, GitRebaseExecRunner{})
		ctx := context.Background()
		actions, err := service.GetRebaseTodoList(ctx, repo, baseSHA)
		if err != nil {
			t.Fatal(err)
		}
		actions[1].Action = "drop"
		actions = []RebaseTodoAction{actions[2], actions[0], actions[1]}
		if err := service.StartInteractiveRebase(ctx, repo, baseSHA); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = service.AbortRebase(context.Background(), repo) }()
		if err := service.ApplyRebaseActions(ctx, repo, actions); err != nil {
			t.Fatal(err)
		}
		if err := service.ContinueRebase(ctx, repo); err != nil {
			t.Fatal(err)
		}
		subjects := strings.Fields(runGitRebaseTestCommand(t, repo, "log", "--reverse", "--format=%s", baseSHA+"..HEAD"))
		if got, want := strings.Join(subjects, " "), "c a"; got != want {
			t.Fatalf("subjects = %q, want %q", got, want)
		}
	})

	t.Run("abort", func(t *testing.T) {
		repo := newRealGitRebaseRepository(t)
		baseSHA := commitRealGitRebaseFile(t, repo, "base.txt", "base\n", "base")
		originalHead := commitRealGitRebaseFile(t, repo, "change.txt", "change\n", "change")
		service := newGitRebaseTestService(t, GitRebaseExecRunner{})
		if err := service.StartInteractiveRebase(context.Background(), repo, baseSHA); err != nil {
			t.Fatal(err)
		}
		if err := service.AbortRebase(context.Background(), repo); err != nil {
			t.Fatal(err)
		}
		if head := strings.TrimSpace(runGitRebaseTestCommand(t, repo, "rev-parse", "HEAD")); head != originalHead {
			t.Fatalf("HEAD = %s, want %s", head, originalHead)
		}
		inProgress, err := service.IsRebaseInProgress(repo)
		if err != nil || inProgress {
			t.Fatalf("IsRebaseInProgress = %v, %v", inProgress, err)
		}
	})
}

func TestGitRebaseServiceRealEditAndConflictStops(t *testing.T) {
	t.Run("real edit remains user controlled", func(t *testing.T) {
		repo := newRealGitRebaseRepository(t)
		baseSHA := commitRealGitRebaseFile(t, repo, "base.txt", "base\n", "base")
		commitRealGitRebaseFile(t, repo, "change.txt", "change\n", "change")
		service := newGitRebaseTestService(t, GitRebaseExecRunner{})
		ctx := context.Background()
		actions, err := service.GetRebaseTodoList(ctx, repo, baseSHA)
		if err != nil {
			t.Fatal(err)
		}
		actions[0].Action = "edit"
		if err := service.StartInteractiveRebase(ctx, repo, baseSHA); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = service.AbortRebase(context.Background(), repo) }()
		if err := service.ApplyRebaseActions(ctx, repo, actions); err != nil {
			t.Fatal(err)
		}
		if err := service.ContinueRebase(ctx, repo); err != nil {
			t.Fatalf("ContinueRebase to edit stop: %v", err)
		}
		inProgress, err := service.IsRebaseInProgress(repo)
		if err != nil || !inProgress {
			t.Fatalf("edit stop IsRebaseInProgress = %v, %v", inProgress, err)
		}
		if err := service.SkipCommit(ctx, repo); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("SkipCommit at edit stop error = %v, want ErrInvalidInput", err)
		}
		runGitRebaseTestCommand(t, repo, "commit", "--amend", "-q", "-m", "user edited")
		if err := service.ContinueRebase(ctx, repo); err != nil {
			t.Fatalf("ContinueRebase after edit: %v", err)
		}
		if subject := strings.TrimSpace(runGitRebaseTestCommand(t, repo, "show", "-s", "--format=%s", "HEAD")); subject != "user edited" {
			t.Fatalf("subject = %q, want user edited", subject)
		}
	})

	t.Run("conflict can be skipped only after stop", func(t *testing.T) {
		repo := newRealGitRebaseRepository(t)
		baseSHA := commitRealGitRebaseFile(t, repo, "value.txt", "base\n", "base")
		runGitRebaseTestCommand(t, repo, "checkout", "-q", "-b", "feature")
		featureSHA := commitRealGitRebaseFile(t, repo, "value.txt", "feature\n", "feature")
		runGitRebaseTestCommand(t, repo, "checkout", "-q", "-b", "upstream", baseSHA)
		upstreamSHA := commitRealGitRebaseFile(t, repo, "value.txt", "upstream\n", "upstream")
		runGitRebaseTestCommand(t, repo, "checkout", "-q", "feature")
		service := newGitRebaseTestService(t, GitRebaseExecRunner{})
		ctx := context.Background()
		actions, err := service.GetRebaseTodoList(ctx, repo, upstreamSHA)
		if err != nil {
			t.Fatal(err)
		}
		if len(actions) != 1 || actions[0].CommitSHA != featureSHA {
			t.Fatalf("actions = %#v", actions)
		}
		if err := service.StartInteractiveRebase(ctx, repo, upstreamSHA); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = service.AbortRebase(context.Background(), repo) }()
		if err := service.ApplyRebaseActions(ctx, repo, actions); err != nil {
			t.Fatal(err)
		}
		if err := service.ContinueRebase(ctx, repo); err == nil {
			t.Fatal("ContinueRebase unexpectedly succeeded despite conflict")
		}
		status, err := service.GetRebaseStatus(repo)
		if err != nil {
			t.Fatal(err)
		}
		if status.Phase != gitRebasePhaseStopped || status.StopReason != gitRebaseStopReasonCommandError {
			t.Fatalf("conflict status = %#v", status)
		}
		if err := service.SkipCommit(ctx, repo); err != nil {
			t.Fatalf("SkipCommit: %v", err)
		}
		if head := strings.TrimSpace(runGitRebaseTestCommand(t, repo, "rev-parse", "HEAD")); head != upstreamSHA {
			t.Fatalf("HEAD = %s, want upstream %s", head, upstreamSHA)
		}
	})

	t.Run("reword survives conflict resolution", func(t *testing.T) {
		repo := newRealGitRebaseRepository(t)
		baseSHA := commitRealGitRebaseFile(t, repo, "value.txt", "base\n", "base")
		runGitRebaseTestCommand(t, repo, "checkout", "-q", "-b", "feature")
		commitRealGitRebaseFile(t, repo, "value.txt", "feature\n", "feature")
		runGitRebaseTestCommand(t, repo, "checkout", "-q", "-b", "upstream", baseSHA)
		upstreamSHA := commitRealGitRebaseFile(t, repo, "value.txt", "upstream\n", "upstream")
		runGitRebaseTestCommand(t, repo, "checkout", "-q", "feature")
		service := newGitRebaseTestService(t, GitRebaseExecRunner{})
		ctx := context.Background()
		actions, err := service.GetRebaseTodoList(ctx, repo, upstreamSHA)
		if err != nil {
			t.Fatal(err)
		}
		actions[0].Action = "reword"
		actions[0].ShortMessage = "reworded after conflict"
		actions[0].LongMessage = "resolved body"
		if err := service.StartInteractiveRebase(ctx, repo, upstreamSHA); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = service.AbortRebase(context.Background(), repo) }()
		if err := service.ApplyRebaseActions(ctx, repo, actions); err != nil {
			t.Fatal(err)
		}
		if err := service.ContinueRebase(ctx, repo); err == nil {
			t.Fatal("ContinueRebase unexpectedly succeeded despite conflict")
		}
		if err := os.WriteFile(filepath.Join(repo, "value.txt"), []byte("resolved\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitRebaseTestCommand(t, repo, "add", "value.txt")
		if err := service.ContinueRebase(ctx, repo); err != nil {
			t.Fatalf("ContinueRebase after conflict resolution: %v", err)
		}
		message := strings.ReplaceAll(runGitRebaseTestCommand(t, repo, "show", "-s", "--format=%B", "HEAD"), "\r\n", "\n")
		if strings.TrimSpace(message) != "reworded after conflict\n\nresolved body" {
			t.Fatalf("message = %q", message)
		}
	})
}

func TestGitRebaseServiceRealGitLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	runGitRebaseTestCommand(t, repo, "init", "-q")
	runGitRebaseTestCommand(t, repo, "config", "user.name", "Rebase Test")
	runGitRebaseTestCommand(t, repo, "config", "user.email", "rebase@example.com")
	runGitRebaseTestCommand(t, repo, "config", "commit.gpgsign", "false")

	filePath := filepath.Join(repo, "value.txt")
	if err := os.WriteFile(filePath, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitRebaseTestCommand(t, repo, "add", "value.txt")
	runGitRebaseTestCommand(t, repo, "commit", "-q", "-m", "base")
	baseSHA := strings.TrimSpace(runGitRebaseTestCommand(t, repo, "rev-parse", "HEAD"))

	if err := os.WriteFile(filePath, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitRebaseTestCommand(t, repo, "add", "value.txt")
	runGitRebaseTestCommand(t, repo, "commit", "-q", "-m", "first")
	if err := os.WriteFile(filePath, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitRebaseTestCommand(t, repo, "add", "value.txt")
	runGitRebaseTestCommand(t, repo, "commit", "-q", "-m", "second")

	service := newGitRebaseTestService(t, GitRebaseExecRunner{})
	ctx := context.Background()
	actions, err := service.GetRebaseTodoList(ctx, repo, baseSHA)
	if err != nil {
		t.Fatalf("GetRebaseTodoList with real git: %v", err)
	}
	if len(actions) != 2 || actions[0].ShortMessage != "first" || actions[1].ShortMessage != "second" {
		t.Fatalf("unexpected real git todo: %#v", actions)
	}

	if err := service.StartInteractiveRebase(ctx, repo, baseSHA); err != nil {
		t.Fatalf("StartInteractiveRebase with real git: %v", err)
	}
	defer func() { _ = service.AbortRebase(context.Background(), repo) }()
	inProgress, err := service.IsRebaseInProgress(repo)
	if err != nil || !inProgress {
		t.Fatalf("IsRebaseInProgress after start = %v, %v", inProgress, err)
	}
	actions[1].Action = "drop"
	if err := service.ApplyRebaseActions(ctx, repo, actions); err != nil {
		t.Fatalf("ApplyRebaseActions with real git: %v", err)
	}
	if err := service.ContinueRebase(ctx, repo); err != nil {
		t.Fatalf("ContinueRebase with real git: %v", err)
	}
	inProgress, err = service.IsRebaseInProgress(repo)
	if err != nil || inProgress {
		t.Fatalf("IsRebaseInProgress after continue = %v, %v", inProgress, err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(content), "\r\n", "\n") != "one\n" {
		t.Fatalf("worktree content = %q, want first commit only", content)
	}
}

func runGitRebaseTestCommand(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", cmdArgs...)
	cmd.Env = minimalGitEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output)
}

func newRealGitRebaseRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	runGitRebaseTestCommand(t, repo, "init", "-q")
	runGitRebaseTestCommand(t, repo, "config", "user.name", "Rebase Test")
	runGitRebaseTestCommand(t, repo, "config", "user.email", "rebase@example.com")
	runGitRebaseTestCommand(t, repo, "config", "commit.gpgsign", "false")
	return repo
}

func commitRealGitRebaseFile(
	t *testing.T,
	repo,
	name,
	content,
	message string,
) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitRebaseTestCommand(t, repo, "add", "--", name)
	runGitRebaseTestCommand(t, repo, "commit", "-q", "-m", message)
	return strings.TrimSpace(runGitRebaseTestCommand(t, repo, "rev-parse", "HEAD"))
}

func writeGitRebaseTestSnapshot(
	t *testing.T,
	rebaseDir string,
	todo []byte,
	expectedCommits []string,
) {
	t.Helper()
	const origHead = "cccccccccccccccccccccccccccccccccccccccc"
	const onto = "dddddddddddddddddddddddddddddddddddddddd"
	for name, content := range map[string]string{
		"orig-head": origHead + "\n",
		"onto":      onto + "\n",
	} {
		if err := os.WriteFile(filepath.Join(rebaseDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state := gitRebasePersistentState{
		Version:         gitRebaseStateVersion,
		Phase:           gitRebasePhaseAwaitingApply,
		OrigHead:        origHead,
		Onto:            onto,
		UpstreamRef:     "test-upstream",
		TodoHash:        gitRebaseContentHash(todo),
		ExpectedCommits: append([]string(nil), expectedCommits...),
		Actions:         make([]RebaseTodoAction, len(expectedCommits)),
	}
	for index, commit := range expectedCommits {
		state.Actions[index] = RebaseTodoAction{Action: "pick", CommitSHA: commit}
	}
	if err := writeGitRebaseState(rebaseDir, state); err != nil {
		t.Fatalf("writeGitRebaseState: %v", err)
	}
}

type gitRebaseTestBranchProvider struct {
	path string
}

func (p *gitRebaseTestBranchProvider) ListBranches(repoPath string) ([]BranchRef, error) {
	p.path = repoPath
	return []BranchRef{{Name: "main"}}, nil
}
