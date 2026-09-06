package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type gitWorktreeTestCall struct {
	repo        string
	args        []string
	hasDeadline bool
}

type gitWorktreeTestResponse struct {
	output string
	err    error
}

type gitWorktreeTestRunner struct {
	output    string
	err       error
	responses []gitWorktreeTestResponse
	calls     []gitWorktreeTestCall
}

func (r *gitWorktreeTestRunner) RunGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	callIndex := len(r.calls)
	_, hasDeadline := ctx.Deadline()
	r.calls = append(r.calls, gitWorktreeTestCall{
		repo:        repoPath,
		args:        append([]string(nil), args...),
		hasDeadline: hasDeadline,
	})
	if callIndex < len(r.responses) {
		response := r.responses[callIndex]
		return response.output, response.err
	}
	return r.output, r.err
}

type gitWorktreeBranchProviderStub struct {
	paths []string
	err   error
}

func (s *gitWorktreeBranchProviderStub) ListBranches(path string) ([]BranchRef, error) {
	s.paths = append(s.paths, path)
	return nil, s.err
}

func TestParseGitWorktreePorcelain(t *testing.T) {
	output := strings.Join([]string{
		"worktree /repo/main",
		"HEAD 1111111111111111111111111111111111111111",
		"branch refs/heads/main",
		"future-field ignored",
		"",
		`worktree "/repo/feature worktree"`,
		"HEAD 2222222222222222222222222222222222222222",
		"detached",
		`locked "maintenance window"`,
		`prunable "gitdir file points to non-existent location"`,
		"",
		"worktree /repo/bare.git",
		"bare",
		"",
		"worktree /repo/reasonless-lock",
		"HEAD 3333333333333333333333333333333333333333",
		"branch refs/heads/locked",
		"locked",
	}, "\r\n")

	got, err := parseGitWorktreePorcelain(output)
	if err != nil {
		t.Fatalf("parseGitWorktreePorcelain failed: %v", err)
	}
	want := []WorktreeInfo{
		{Path: "/repo/main", HEAD: "1111111111111111111111111111111111111111", Branch: "main"},
		{
			Path:     "/repo/feature worktree",
			HEAD:     "2222222222222222222222222222222222222222",
			Branch:   "",
			Locked:   "maintenance window",
			Prunable: true,
		},
		{Path: "/repo/bare.git", Bare: true},
		{
			Path:   "/repo/reasonless-lock",
			HEAD:   "3333333333333333333333333333333333333333",
			Branch: "locked",
			Locked: "locked",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed worktrees mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseGitWorktreePorcelainEmptyAndMalformedOutput(t *testing.T) {
	got, err := parseGitWorktreePorcelain("")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("expected a non-nil empty slice, got %#v", got)
	}

	tests := []struct {
		name   string
		output string
	}{
		{name: "field before record", output: "HEAD abc\n"},
		{name: "missing worktree path", output: "worktree\n"},
		{name: "empty worktree path", output: "worktree \n"},
		{name: "missing head", output: "worktree /repo/wt\nHEAD\n"},
		{name: "missing branch", output: "worktree /repo/wt\nbranch \n"},
		{name: "invalid quoted path", output: "worktree \"unterminated\n"},
		{name: "invalid quoted head", output: "worktree /repo/wt\nHEAD \"unterminated\n"},
		{name: "invalid quoted branch", output: "worktree /repo/wt\nbranch \"unterminated\n"},
		{name: "invalid quoted lock reason", output: "worktree /repo/wt\nlocked \"unterminated\n"},
		{name: "invalid quoted prune reason", output: "worktree /repo/wt\nprunable \"unterminated\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseGitWorktreePorcelain(tt.output); err == nil {
				t.Fatalf("expected malformed output to fail: %q", tt.output)
			}
		})
	}
}

func TestGitWorktreeGitServiceAdapterUsesOnlyPublicBranchBoundary(t *testing.T) {
	repo := t.TempDir()
	provider := &gitWorktreeBranchProviderStub{}
	adapter := NewGitWorktreeGitServiceAdapter(provider)
	if err := adapter.ValidateRepository(repo); err != nil {
		t.Fatalf("ValidateRepository failed: %v", err)
	}
	if !reflect.DeepEqual(provider.paths, []string{repo}) {
		t.Fatalf("ListBranches paths = %#v, want %q", provider.paths, repo)
	}

	wantErr := errors.New("workspace denied")
	provider.err = wantErr
	if err := adapter.ValidateRepository(repo); !errors.Is(err, wantErr) {
		t.Fatalf("expected provider error to be preserved, got %v", err)
	}
	if err := NewGitWorktreeGitServiceAdapter(nil).ValidateRepository(repo); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected nil provider rejection, got %v", err)
	}
	var typedNilProvider *gitWorktreeBranchProviderStub
	if err := NewGitWorktreeGitServiceAdapter(typedNilProvider).ValidateRepository(repo); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected typed-nil provider rejection, got %v", err)
	}
}

func TestGitWorktreeServiceListUsesPorcelainAndPropagatesErrors(t *testing.T) {
	repo := t.TempDir()
	runner := &gitWorktreeTestRunner{output: "worktree /repo/main\nHEAD abc\nbranch refs/heads/main\n"}
	svc := newGitWorktreeTestService(t, runner)

	worktrees, err := svc.ListWorktrees(context.Background(), repo)
	if err != nil {
		t.Fatalf("ListWorktrees failed: %v", err)
	}
	if len(worktrees) != 1 || worktrees[0].Branch != "main" {
		t.Fatalf("unexpected worktrees: %#v", worktrees)
	}
	assertGitWorktreeCall(t, runner.calls, 0, repo, []string{"worktree", "list", "--porcelain"})

	wantErr := errors.New("git unavailable")
	errorRunner := &gitWorktreeTestRunner{output: "fatal: not a git repository", err: wantErr}
	_, err = newGitWorktreeTestService(t, errorRunner).ListWorktrees(context.Background(), repo)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped runner error, got %v", err)
	}
	if !strings.Contains(err.Error(), "fatal: not a git repository") {
		t.Fatalf("expected command output in error, got %v", err)
	}
}

func TestGitWorktreeServiceAddArgumentCombinations(t *testing.T) {
	repo := canonicalTestPath(t, t.TempDir())
	tests := []struct {
		name string
		ref  string
		opts AddWorktreeOptions
		want []string
	}{
		{
			name: "existing branch",
			ref:  "main",
			want: []string{"worktree", "add", "--", filepath.Join(repo, "wt"), "main"},
		},
		{
			name: "new branch",
			ref:  "HEAD",
			opts: AddWorktreeOptions{NewBranch: "feature/worktree"},
			want: []string{"worktree", "add", "-b", "feature/worktree", "--", filepath.Join(repo, "wt"), "HEAD"},
		},
		{
			name: "detached and forced",
			ref:  "v1.0.0",
			opts: AddWorktreeOptions{Detach: true, Force: true},
			want: []string{"worktree", "add", "--detach", "--force", "--", filepath.Join(repo, "wt"), "v1.0.0"},
		},
		{
			name: "implicit revision",
			opts: AddWorktreeOptions{Force: true},
			want: []string{"worktree", "add", "--force", "--", filepath.Join(repo, "wt")},
		},
		{
			name: "no checkout",
			ref:  "main",
			opts: AddWorktreeOptions{NoCheckout: true},
			want: []string{"worktree", "add", "--no-checkout", "--", filepath.Join(repo, "wt"), "main"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &gitWorktreeTestRunner{}
			svc := newGitWorktreeTestService(t, runner)
			if err := svc.AddWorktree(context.Background(), repo, "wt", tt.ref, tt.opts); err != nil {
				t.Fatalf("AddWorktree failed: %v", err)
			}
			assertGitWorktreeCall(t, runner.calls, 0, repo, tt.want)
		})
	}
}

func TestParseGitWorktreePruneOutput(t *testing.T) {
	got := parseGitWorktreePruneOutput(strings.Join([]string{
		"Removing worktrees/stale: gitdir file points to non-existent location",
		"  Removing worktrees/old: worktree directory is missing  ",
		"",
	}, "\r\n"))
	want := []string{
		"worktrees/stale: gitdir file points to non-existent location",
		"worktrees/old: worktree directory is missing",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prune entries = %#v, want %#v", got, want)
	}
}

func TestGitWorktreeServiceAppliesCommandContext(t *testing.T) {
	repo := t.TempDir()
	var remaining time.Duration
	runner := GitWorktreeCommandRunnerFunc(func(ctx context.Context, _ string, _ ...string) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("runner context has no deadline")
		}
		remaining = time.Until(deadline)
		return "", ctx.Err()
	})
	svc := newGitWorktreeTestService(t, runner)
	if _, err := svc.ListWorktrees(context.Background(), repo); err != nil {
		t.Fatalf("ListWorktrees failed: %v", err)
	}
	if remaining <= 0 || remaining > gitWorktreeCommandTimeout {
		t.Fatalf("runner deadline remaining = %v, want within (0, %v]", remaining, gitWorktreeCommandTimeout)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	runner = GitWorktreeCommandRunnerFunc(func(ctx context.Context, _ string, _ ...string) (string, error) {
		return "", ctx.Err()
	})
	_, err := newGitWorktreeTestService(t, runner).ListWorktrees(cancelled, repo)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected caller cancellation to propagate, got %v", err)
	}
}

func TestGitWorktreeServiceAddRejectsInvalidOptions(t *testing.T) {
	repo := t.TempDir()
	tests := []struct {
		name string
		ref  string
		opts AddWorktreeOptions
	}{
		{name: "new branch with detach", ref: "HEAD", opts: AddWorktreeOptions{NewBranch: "feature", Detach: true}},
		{name: "blank new branch", ref: "HEAD", opts: AddWorktreeOptions{NewBranch: "  "}},
		{name: "blank ref", ref: " \t "},
		{name: "ref newline", ref: "main\n--force"},
		{name: "new branch NUL", ref: "HEAD", opts: AddWorktreeOptions{NewBranch: "feature\x00bad"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &gitWorktreeTestRunner{}
			err := newGitWorktreeTestService(t, runner).AddWorktree(context.Background(), repo, "wt", tt.ref, tt.opts)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("invalid request executed git: %#v", runner.calls)
			}
		})
	}
}

func TestGitWorktreeServiceRegisteredMutationArguments(t *testing.T) {
	repo := canonicalTestPath(t, t.TempDir())
	target := filepath.Join(repo, "wt")
	movedTarget := filepath.Join(repo, "moved")
	registered := gitWorktreePorcelain(repo, target)
	tests := []struct {
		name string
		run  func(*GitWorktreeService) error
		want []string
	}{
		{name: "remove", run: func(s *GitWorktreeService) error { return s.RemoveWorktree(context.Background(), repo, target, false) }, want: []string{"worktree", "remove", "--", target}},
		{name: "force remove", run: func(s *GitWorktreeService) error { return s.RemoveWorktree(context.Background(), repo, target, true) }, want: []string{"worktree", "remove", "--force", "--", target}},
		{name: "lock", run: func(s *GitWorktreeService) error {
			return s.LockWorktree(context.Background(), repo, target, "maintenance window")
		}, want: []string{"worktree", "lock", "--reason", "maintenance window", "--", target}},
		{name: "lock without reason", run: func(s *GitWorktreeService) error { return s.LockWorktree(context.Background(), repo, target, "") }, want: []string{"worktree", "lock", "--", target}},
		{name: "lock with blank reason", run: func(s *GitWorktreeService) error { return s.LockWorktree(context.Background(), repo, target, "  ") }, want: []string{"worktree", "lock", "--", target}},
		{name: "unlock", run: func(s *GitWorktreeService) error { return s.UnlockWorktree(context.Background(), repo, target) }, want: []string{"worktree", "unlock", "--", target}},
		{name: "move", run: func(s *GitWorktreeService) error {
			return s.MoveWorktree(context.Background(), repo, target, movedTarget, true)
		}, want: []string{"worktree", "move", "--force", "--", target, movedTarget}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &gitWorktreeTestRunner{responses: []gitWorktreeTestResponse{{output: registered}, {}}}
			svc := newGitWorktreeTestService(t, runner)
			if err := tt.run(svc); err != nil {
				t.Fatalf("operation failed: %v", err)
			}
			assertGitWorktreeCall(t, runner.calls, 0, repo, []string{"worktree", "list", "--porcelain"})
			assertGitWorktreeCall(t, runner.calls, 1, repo, tt.want)
		})
	}

	pruneTests := []struct {
		name   string
		dryRun bool
		want   []string
	}{
		{name: "prune", want: []string{"worktree", "prune", "--verbose"}},
		{name: "dry run", dryRun: true, want: []string{"worktree", "prune", "--dry-run", "--verbose"}},
	}
	for _, tt := range pruneTests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &gitWorktreeTestRunner{output: "Removing worktrees/stale: missing\n"}
			entries, err := newGitWorktreeTestService(t, runner).PruneWorktrees(context.Background(), repo, tt.dryRun)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(entries, []string{"worktrees/stale: missing"}) {
				t.Fatalf("prune entries = %#v", entries)
			}
			assertGitWorktreeCall(t, runner.calls, 0, repo, tt.want)
		})
	}
}

func TestGitWorktreeServiceMovePathAuthorization(t *testing.T) {
	root := canonicalTestPath(t, t.TempDir())
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	oldTarget := filepath.Join(repo, "linked")
	outsideTarget := filepath.Join(root, "moved")
	insideTarget := filepath.Join(repo, "moved")
	porcelain := gitWorktreePorcelain(repo, oldTarget)

	t.Run("configured safe roots authorize move", func(t *testing.T) {
		runner := &gitWorktreeTestRunner{responses: []gitWorktreeTestResponse{{output: porcelain}, {}}}
		svc := newGitWorktreeTestService(t, runner, root)
		if err := svc.MoveWorktree(
			context.Background(),
			repo,
			oldTarget,
			outsideTarget,
			false,
		); err != nil {
			t.Fatal(err)
		}
		assertGitWorktreeCall(t, runner.calls, 0, repo, []string{"worktree", "list", "--porcelain"})
		assertGitWorktreeCall(t, runner.calls, 1, repo, []string{"worktree", "move", "--", oldTarget, outsideTarget})
	})

	t.Run("renderer cannot authorize an outside target", func(t *testing.T) {
		runner := &gitWorktreeTestRunner{
			responses: []gitWorktreeTestResponse{{output: porcelain}, {}},
		}
		svc := newGitWorktreeTestService(t, runner)
		err := svc.MoveWorktree(
			context.Background(),
			repo,
			oldTarget,
			outsideTarget,
			false,
		)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("error = %v, want ErrInvalidInput", err)
		}
		assertGitWorktreeCall(t, runner.calls, 0, repo, []string{"worktree", "list", "--porcelain"})
		if len(runner.calls) != 1 {
			t.Fatalf("unauthorized move calls = %#v", runner.calls)
		}
	})

	t.Run("repository targets remain allowed by default", func(t *testing.T) {
		runner := &gitWorktreeTestRunner{
			responses: []gitWorktreeTestResponse{{output: porcelain}, {}},
		}
		svc := newGitWorktreeTestService(t, runner)
		if err := svc.MoveWorktree(
			context.Background(),
			repo,
			oldTarget,
			insideTarget,
			false,
		); err != nil {
			t.Fatal(err)
		}
		assertGitWorktreeCall(
			t,
			runner.calls,
			1,
			repo,
			[]string{"worktree", "move", "--", oldTarget, insideTarget},
		)
	})
}
func TestGitWorktreeServiceCurrentLinkedMutationPolicy(t *testing.T) {
	root := canonicalTestPath(t, t.TempDir())
	mainPath := filepath.Join(root, "main")
	linkedPath := filepath.Join(root, "linked")
	for _, path := range []string{mainPath, linkedPath} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	porcelain := gitWorktreePorcelain(mainPath, linkedPath)

	for _, operation := range []struct {
		name string
		run  func(*GitWorktreeService) error
		want []string
	}{
		{
			name: "lock",
			run: func(service *GitWorktreeService) error {
				return service.LockWorktree(context.Background(), linkedPath, linkedPath, "current")
			},
			want: []string{"worktree", "lock", "--reason", "current", "--", linkedPath},
		},
		{
			name: "unlock",
			run: func(service *GitWorktreeService) error {
				return service.UnlockWorktree(context.Background(), linkedPath, linkedPath)
			},
			want: []string{"worktree", "unlock", "--", linkedPath},
		},
	} {
		t.Run(operation.name+" current linked worktree", func(t *testing.T) {
			runner := &gitWorktreeTestRunner{responses: []gitWorktreeTestResponse{{output: porcelain}, {}}}
			if err := operation.run(newGitWorktreeTestService(t, runner)); err != nil {
				t.Fatal(err)
			}
			assertGitWorktreeCall(t, runner.calls, 0, linkedPath, []string{"worktree", "list", "--porcelain"})
			assertGitWorktreeCall(t, runner.calls, 1, linkedPath, operation.want)
		})
	}

	for _, operation := range []struct {
		name string
		run  func(*GitWorktreeService) error
	}{
		{
			name: "remove",
			run: func(service *GitWorktreeService) error {
				return service.RemoveWorktree(context.Background(), linkedPath, linkedPath, true)
			},
		},
		{
			name: "move",
			run: func(service *GitWorktreeService) error {
				return service.MoveWorktree(
					context.Background(),
					linkedPath,
					linkedPath,
					filepath.Join(root, "moved"),
					true,
				)
			},
		},
	} {
		t.Run("rejects "+operation.name+" of current linked worktree", func(t *testing.T) {
			runner := &gitWorktreeTestRunner{}
			err := operation.run(newGitWorktreeTestService(t, runner))
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("current worktree mutation executed git: %#v", runner.calls)
			}
		})
	}
}

func TestGitWorktreeServiceRegisteredExternalAuthorization(t *testing.T) {
	repo := t.TempDir()
	externalRoot := t.TempDir()
	registered := filepath.Join(externalRoot, "registered")
	unregistered := filepath.Join(externalRoot, "unregistered")
	prefixOnly := registered + "-other"
	porcelain := gitWorktreePorcelain(repo, registered)

	t.Run("registered external path is allowed", func(t *testing.T) {
		operations := []struct {
			name string
			run  func(*GitWorktreeService) error
		}{
			{name: "remove", run: func(s *GitWorktreeService) error {
				return s.RemoveWorktree(context.Background(), repo, registered, true)
			}},
			{name: "lock", run: func(s *GitWorktreeService) error {
				return s.LockWorktree(context.Background(), repo, registered, "external")
			}},
			{name: "unlock", run: func(s *GitWorktreeService) error { return s.UnlockWorktree(context.Background(), repo, registered) }},
		}
		for _, operation := range operations {
			t.Run(operation.name, func(t *testing.T) {
				runner := &gitWorktreeTestRunner{responses: []gitWorktreeTestResponse{{output: porcelain}, {}}}
				if err := operation.run(newGitWorktreeTestService(t, runner)); err != nil {
					t.Fatalf("registered external path was rejected: %v", err)
				}
				if len(runner.calls) != 2 {
					t.Fatalf("expected list plus mutation, got %#v", runner.calls)
				}
			})
		}
	})

	for _, target := range []string{unregistered, prefixOnly} {
		t.Run("rejects unregistered "+filepath.Base(target), func(t *testing.T) {
			runner := &gitWorktreeTestRunner{output: porcelain}
			err := newGitWorktreeTestService(t, runner).RemoveWorktree(context.Background(), repo, target, true)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected unregistered path rejection, got %v", err)
			}
			if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, []string{"worktree", "list", "--porcelain"}) {
				t.Fatalf("unregistered path reached mutation command: %#v", runner.calls)
			}
		})
	}
}

func TestGitWorktreeServiceCommandErrorPreservesCauseAndOutput(t *testing.T) {
	repo := t.TempDir()
	target := filepath.Join(repo, "wt")
	wantErr := errors.New("exit status 128")
	runner := &gitWorktreeTestRunner{responses: []gitWorktreeTestResponse{
		{output: gitWorktreePorcelain(repo, target)},
		{output: "fatal: worktree is dirty", err: wantErr},
	}}
	err := newGitWorktreeTestService(t, runner).RemoveWorktree(context.Background(), repo, target, true)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped command error, got %v", err)
	}
	if !strings.Contains(err.Error(), "remove worktree") || !strings.Contains(err.Error(), "worktree is dirty") {
		t.Fatalf("expected operation and git output in error, got %v", err)
	}
}

func TestGitWorktreeServiceAddPathSecurity(t *testing.T) {
	t.Run("allows repository child", func(t *testing.T) {
		repo := canonicalTestPath(t, t.TempDir())
		runner := &gitWorktreeTestRunner{}
		svc := newGitWorktreeTestService(t, runner)
		if err := svc.AddWorktree(context.Background(), repo, filepath.Join("nested", "wt"), "HEAD", AddWorktreeOptions{Detach: true}); err != nil {
			t.Fatalf("expected repository child to be allowed: %v", err)
		}
		assertGitWorktreeCall(t, runner.calls, 0, repo, []string{
			"worktree", "add", "--detach", "--", filepath.Join(repo, "nested", "wt"), "HEAD",
		})
	})

	t.Run("rejects arbitrary parent by default", func(t *testing.T) {
		root := canonicalTestPath(t, t.TempDir())
		repo := filepath.Join(root, "repo")
		if err := os.Mkdir(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		runner := &gitWorktreeTestRunner{}
		err := newGitWorktreeTestService(t, runner).AddWorktree(context.Background(), repo, filepath.Join("..", "outside"), "HEAD", AddWorktreeOptions{})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expected traversal rejection, got %v", err)
		}
		if len(runner.calls) != 0 {
			t.Fatal("git should not run for a rejected path")
		}
	})

	t.Run("rejects an absolute external path despite renderer confirmation", func(t *testing.T) {
		root := canonicalTestPath(t, t.TempDir())
		repo := filepath.Join(root, "repo")
		if err := os.Mkdir(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "confirmed-worktree")
		runner := &gitWorktreeTestRunner{}
		svc := newGitWorktreeTestService(t, runner)
		err := svc.AddWorktree(
			context.Background(),
			repo,
			target,
			"HEAD",
			AddWorktreeOptions{AllowOutsideRepository: true},
		)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("error = %v, want ErrInvalidInput", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("renderer path elevation executed git: %#v", runner.calls)
		}
	})

	t.Run("allows sibling under explicitly configured safe root", func(t *testing.T) {
		root := canonicalTestPath(t, t.TempDir())
		repo := filepath.Join(root, "repo")
		if err := os.Mkdir(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		runner := &gitWorktreeTestRunner{}
		svc := newGitWorktreeTestService(t, runner, root)
		if err := svc.AddWorktree(context.Background(), repo, filepath.Join(root, "sibling-worktree"), "HEAD", AddWorktreeOptions{}); err != nil {
			t.Fatalf("expected approved sibling to be allowed: %v", err)
		}
		assertGitWorktreeCall(t, runner.calls, 0, repo, []string{
			"worktree", "add", "--", filepath.Join(root, "sibling-worktree"), "HEAD",
		})
	})

	t.Run("rejects repository itself and empty paths", func(t *testing.T) {
		repo := canonicalTestPath(t, t.TempDir())
		for _, path := range []string{"", "   ", ".", repo, "bad\x00path", "bad\npath"} {
			runner := &gitWorktreeTestRunner{}
			err := newGitWorktreeTestService(t, runner).AddWorktree(context.Background(), repo, path, "HEAD", AddWorktreeOptions{})
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("path %q: expected ErrInvalidInput, got %v", path, err)
			}
			if len(runner.calls) != 0 {
				t.Errorf("path %q executed git: %#v", path, runner.calls)
			}
		}
	})

	t.Run("rejects symlink escape with missing descendants", func(t *testing.T) {
		repo := canonicalTestPath(t, t.TempDir())
		outside := canonicalTestPath(t, t.TempDir())
		link := filepath.Join(repo, "link")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		runner := &gitWorktreeTestRunner{}
		err := newGitWorktreeTestService(t, runner).AddWorktree(context.Background(), repo, filepath.Join("link", "missing", "wt"), "HEAD", AddWorktreeOptions{})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expected symlink escape rejection, got %v", err)
		}
		if len(runner.calls) != 0 {
			t.Fatal("git should not run for a symlink escape")
		}
	})
}

func TestGitWorktreeServiceSafeRootConfiguration(t *testing.T) {
	validator := GitWorktreeRepositoryValidatorFunc(func(string) error { return nil })
	runner := &gitWorktreeTestRunner{}
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"", "relative", "bad\x00root", filepath.Join(t.TempDir(), "missing"), fileRoot} {
		_, err := NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{
			Runner:              runner,
			RepositoryValidator: validator,
			SafeRoots:           []string{root},
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("safe root %q: expected ErrInvalidInput, got %v", root, err)
		}
	}

	root := t.TempDir()
	svc, err := NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{
		Runner:              runner,
		RepositoryValidator: validator,
		SafeRoots:           []string{root, root},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.addSafeRoots) != 1 {
		t.Fatalf("duplicate safe roots were not collapsed: %#v", svc.addSafeRoots)
	}
}

func TestGitWorktreeServiceRepositoryValidation(t *testing.T) {
	repo := canonicalTestPath(t, t.TempDir())
	wantErr := errors.New("repository denied")
	runner := &gitWorktreeTestRunner{}
	svc, err := NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{
		Runner: runner,
		RepositoryValidator: GitWorktreeRepositoryValidatorFunc(func(path string) error {
			if path != repo {
				t.Fatalf("validator path = %q, want %q", path, repo)
			}
			return wantErr
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListWorktrees(context.Background(), repo); !errors.Is(err, wantErr) {
		t.Fatalf("expected validator error, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner called after repository rejection: %#v", runner.calls)
	}

	file := filepath.Join(t.TempDir(), "repo.txt")
	if err := os.WriteFile(file, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	allow := GitWorktreeRepositoryValidatorFunc(func(string) error { return nil })
	validatingService, err := NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{Runner: runner, RepositoryValidator: allow})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", " \t ", ".", "bad\x00repo", file} {
		if _, err := validatingService.ListWorktrees(context.Background(), path); err == nil {
			t.Errorf("repository path %q should be rejected", path)
		}
	}
}

func TestGitWorktreeServiceRequiresDependencies(t *testing.T) {
	repo := t.TempDir()
	for _, svc := range []*GitWorktreeService{nil, NewGitWorktreeService(nil)} {
		if _, err := svc.ListWorktrees(context.Background(), repo); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expected missing injection error, got %v", err)
		}
	}

	withoutValidator, err := NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutValidator.ListWorktrees(context.Background(), repo); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected missing validator error, got %v", err)
	}

	var runner GitWorktreeCommandRunnerFunc
	nilRunnerService, err := NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{
		Runner:              runner,
		RepositoryValidator: GitWorktreeRepositoryValidatorFunc(func(string) error { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nilRunnerService.ListWorktrees(context.Background(), repo); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected nil runner function error, got %v", err)
	}

	var validator GitWorktreeRepositoryValidatorFunc
	nilValidatorService, err := NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{
		Runner:              &gitWorktreeTestRunner{},
		RepositoryValidator: validator,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nilValidatorService.ListWorktrees(context.Background(), repo); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected nil validator function error, got %v", err)
	}
}

func TestGitWorktreeServiceGitServiceWorkspaceValidationUsesPublicAdapter(t *testing.T) {
	workspace := t.TempDir()
	repo := t.TempDir()
	gitSvc := &GitService{}
	if err := gitSvc.setWorkspaceRoot(workspace); err != nil {
		t.Fatal(err)
	}
	runner := &gitWorktreeTestRunner{}
	svc := NewGitWorktreeServiceWithRunner(gitSvc, runner)
	if _, err := svc.ListWorktrees(context.Background(), repo); err == nil {
		t.Fatal("expected repository outside GitService workspace to be rejected")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner called after public adapter rejection: %#v", runner.calls)
	}
}

func TestGitWorktreeServiceDefaultAdapterListsBareRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := canonicalTestPath(t, t.TempDir())
	source := filepath.Join(root, "source")
	bare := filepath.Join(root, "repo.git")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	initializeGitWorktreeTestRepo(t, source)
	runGitWorktreeTestCommand(t, bare, "init", "--bare", "-q")
	runGitWorktreeTestCommand(t, source, "push", bare, "HEAD:refs/heads/main")
	runGitWorktreeTestCommand(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	gitSvc := &GitService{}
	if err := gitSvc.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	worktrees, err := NewGitWorktreeService(gitSvc).ListWorktrees(context.Background(), bare)
	if err != nil {
		t.Fatalf("list bare repository worktrees through default adapter: %v", err)
	}
	if len(worktrees) != 1 || !worktrees[0].Bare {
		t.Fatalf("expected one bare worktree record, got %#v", worktrees)
	}
	if !worktreePathsEqual(worktrees[0].Path, bare) {
		t.Fatalf("bare worktree path = %q, want %q", worktrees[0].Path, bare)
	}
}

func TestGitWorktreeServiceRealGitLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initializeGitWorktreeTestRepo(t, repo)

	gitSvc := &GitService{}
	if err := gitSvc.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	svc, err := NewGitWorktreeServiceWithSafeRoots(gitSvc, []string{root})
	if err != nil {
		t.Fatal(err)
	}

	detachedPath := filepath.Join(root, "detached-worktree")
	if err := svc.AddWorktree(context.Background(), repo, detachedPath, "HEAD", AddWorktreeOptions{Detach: true}); err != nil {
		t.Fatalf("add detached sibling worktree: %v", err)
	}
	worktrees, err := svc.ListWorktrees(context.Background(), repo)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	detached := findGitWorktree(worktrees, detachedPath)
	if detached == nil || detached.Branch != "" || detached.HEAD == "" {
		t.Fatalf("detached worktree not parsed correctly: %#v", worktrees)
	}
	if err := svc.LockWorktree(context.Background(), repo, detachedPath, "integration test"); err != nil {
		t.Fatalf("lock sibling worktree: %v", err)
	}
	worktrees, err = svc.ListWorktrees(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	locked := findGitWorktree(worktrees, detachedPath)
	if locked == nil || locked.Locked != "integration test" {
		t.Fatalf("lock reason not parsed: %#v", locked)
	}
	if err := svc.UnlockWorktree(context.Background(), repo, detachedPath); err != nil {
		t.Fatalf("unlock sibling worktree: %v", err)
	}
	if err := svc.RemoveWorktree(context.Background(), repo, detachedPath, true); err != nil {
		t.Fatalf("remove sibling worktree: %v", err)
	}

	branchPath := filepath.Join(root, "branch-worktree")
	if err := svc.AddWorktree(context.Background(), repo, branchPath, "HEAD", AddWorktreeOptions{NewBranch: "feature/worktree"}); err != nil {
		t.Fatalf("add branch worktree: %v", err)
	}
	worktrees, err = svc.ListWorktrees(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	branch := findGitWorktree(worktrees, branchPath)
	if branch == nil || branch.Branch != "feature/worktree" {
		t.Fatalf("branch worktree not parsed correctly: %#v", worktrees)
	}
	movedBranchPath := filepath.Join(root, "moved-branch-worktree")
	if err := svc.MoveWorktree(context.Background(), repo, branchPath, movedBranchPath, false); err != nil {
		t.Fatalf("move branch worktree: %v", err)
	}
	if err := svc.RemoveWorktree(context.Background(), repo, movedBranchPath, true); err != nil {
		t.Fatalf("remove branch worktree: %v", err)
	}

	externalRoot := t.TempDir()
	externalPath := filepath.Join(externalRoot, "registered-external")
	runGitWorktreeTestCommand(t, repo, "worktree", "add", "--detach", externalPath, "HEAD")
	if err := svc.LockWorktree(context.Background(), repo, externalPath, "registered elsewhere"); err != nil {
		t.Fatalf("lock registered external worktree: %v", err)
	}
	if err := svc.UnlockWorktree(context.Background(), repo, externalPath); err != nil {
		t.Fatalf("unlock registered external worktree: %v", err)
	}
	if err := svc.RemoveWorktree(context.Background(), repo, externalPath, true); err != nil {
		t.Fatalf("remove registered external worktree: %v", err)
	}

	unregisteredExternal := filepath.Join(externalRoot, "unregistered")
	if err := svc.RemoveWorktree(context.Background(), repo, unregisteredExternal, true); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected unregistered external rejection, got %v", err)
	}
	if err := svc.AddWorktree(context.Background(), repo, unregisteredExternal, "HEAD", AddWorktreeOptions{Detach: true}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected Add outside safe roots rejection, got %v", err)
	}
	if _, err := svc.PruneWorktrees(context.Background(), repo, true); err != nil {
		t.Fatalf("dry-run prune: %v", err)
	}
}

func newGitWorktreeTestService(t *testing.T, runner GitWorktreeCommandRunner, safeRoots ...string) *GitWorktreeService {
	t.Helper()
	service, err := NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{
		Runner: runner,
		RepositoryValidator: GitWorktreeRepositoryValidatorFunc(func(string) error {
			return nil
		}),
		SafeRoots: safeRoots,
	})
	if err != nil {
		t.Fatalf("create test worktree service: %v", err)
	}
	return service
}

func gitWorktreePorcelain(repo string, paths ...string) string {
	records := []string{fmt.Sprintf("worktree %s\nHEAD abc\nbranch refs/heads/main\n", repo)}
	for _, path := range paths {
		records = append(records, fmt.Sprintf("worktree %s\nHEAD def\ndetached\n", path))
	}
	return strings.Join(records, "\n")
}

func assertGitWorktreeCall(t *testing.T, calls []gitWorktreeTestCall, index int, repo string, args []string) {
	t.Helper()
	if len(calls) <= index {
		t.Fatalf("missing command call %d; got %#v", index, calls)
	}
	wantRepo, err := resolveWorktreePath(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !worktreePathsEqual(calls[index].repo, wantRepo) {
		t.Errorf("repo = %q, want %q", calls[index].repo, wantRepo)
	}
	if !calls[index].hasDeadline {
		t.Errorf("command call %d did not receive a deadline", index)
	}
	if !reflect.DeepEqual(calls[index].args, args) {
		t.Errorf("args = %#v, want %#v", calls[index].args, args)
	}
}

func initializeGitWorktreeTestRepo(t *testing.T, repo string) {
	t.Helper()
	runGitWorktreeTestCommand(t, repo, "init", "-q")
	runGitWorktreeTestCommand(t, repo, "config", "user.name", "Koyori IDE Test")
	runGitWorktreeTestCommand(t, repo, "config", "user.email", "koyori-ide@example.invalid")
	runGitWorktreeTestCommand(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitWorktreeTestCommand(t, repo, "add", "README.md")
	runGitWorktreeTestCommand(t, repo, "commit", "-q", "-m", "initial")
}

func runGitWorktreeTestCommand(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", cmdArgs...)
	cmd.Env = gitWorktreeCommandEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
}

func findGitWorktree(worktrees []WorktreeInfo, path string) *WorktreeInfo {
	want, err := resolveWorktreePath(path)
	if err != nil {
		return nil
	}
	for i := range worktrees {
		candidate, err := resolveWorktreePath(worktrees[i].Path)
		if err == nil && worktreePathsEqual(candidate, want) {
			return &worktrees[i]
		}
	}
	return nil
}

func ExampleGitWorktreeCommandRunnerFunc() {
	runner := GitWorktreeCommandRunnerFunc(func(_ context.Context, repoPath string, args ...string) (string, error) {
		return fmt.Sprintf("%s: git %s", repoPath, strings.Join(args, " ")), nil
	})
	output, _ := runner.RunGit(context.Background(), "/repo", "worktree", "list", "--porcelain")
	fmt.Println(output)
	// Output: /repo: git worktree list --porcelain
}
