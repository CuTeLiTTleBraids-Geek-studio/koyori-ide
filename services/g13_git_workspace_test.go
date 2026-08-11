package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestG13GitWorktreeUsesWorkspaceContextAndRejectsRendererPathElevation(t *testing.T) {
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideWorkspace := t.TempDir()
	runner := &gitWorktreeTestRunner{}
	service, err := NewGitWorktreeServiceWithDependencies(GitWorktreeDependencies{
		Runner: runner,
		RepositoryValidator: GitWorktreeRepositoryValidatorFunc(func(string) error {
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceContext := NewWorkspaceContext()
	if err := workspaceContext.Set(workspace); err != nil {
		t.Fatal(err)
	}
	service.setWorkspaceContext(workspaceContext)

	insideWorkspace := filepath.Join(workspace, "linked")
	if err := service.AddWorktree(
		context.Background(),
		repo,
		insideWorkspace,
		"HEAD",
		AddWorktreeOptions{},
	); err != nil {
		t.Fatalf("workspace safe root was not honored: %v", err)
	}

	runner.calls = nil
	err = service.AddWorktree(
		context.Background(),
		repo,
		filepath.Join(outsideWorkspace, "elevated"),
		"HEAD",
		AddWorktreeOptions{AllowOutsideRepository: true},
	)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("renderer path elevation error = %v, want ErrInvalidInput", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("renderer path elevation executed git: %#v", runner.calls)
	}

	newWorkspace := t.TempDir()
	if err := workspaceContext.Set(newWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListWorktrees(context.Background(), repo); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("old workspace repository error = %v, want ErrNotAllowed", err)
	}
}

func TestG13GitRebaseRejectsRepositoryAfterWorkspaceSwitch(t *testing.T) {
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	service, err := NewGitRebaseServiceWithDependencies(GitRebaseDependencies{
		RepositoryValidator: GitRebaseRepositoryValidatorFunc(func(string) error {
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceContext := NewWorkspaceContext()
	if err := workspaceContext.Set(workspace); err != nil {
		t.Fatal(err)
	}
	service.setWorkspaceContext(workspaceContext)
	if _, err := service.validateRepoPath(repo); err != nil {
		t.Fatalf("active workspace repository rejected: %v", err)
	}
	if err := workspaceContext.Set(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.validateRepoPath(repo); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("old workspace repository error = %v, want ErrNotAllowed", err)
	}
}
