package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitServiceDiscoverRepositoriesFindsNestedAndSecondaryRoots(t *testing.T) {
	workspace := t.TempDir()
	secondary := t.TempDir()
	nested := filepath.Join(workspace, "packages", "nested")
	secondaryRepo := filepath.Join(secondary, "service")
	for _, path := range []string{
		filepath.Join(nested, ".git"),
		filepath.Join(secondaryRepo, ".git"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	service := NewGitService()
	if err := service.setWorkspaceRoots([]string{workspace, secondary}); err != nil {
		t.Fatalf("setWorkspaceRoots: %v", err)
	}
	found, err := service.DiscoverRepositories(workspace)
	if err != nil {
		t.Fatalf("DiscoverRepositories(workspace): %v", err)
	}
	if len(found) != 1 || filepath.Clean(found[0]) != filepath.Clean(nested) {
		t.Fatalf("workspace repositories = %v, want [%s]", found, nested)
	}
	found, err = service.DiscoverRepositories(secondary)
	if err != nil {
		t.Fatalf("DiscoverRepositories(secondary): %v", err)
	}
	if len(found) != 1 || filepath.Clean(found[0]) != filepath.Clean(secondaryRepo) {
		t.Fatalf("secondary repositories = %v, want [%s]", found, secondaryRepo)
	}
}

func TestGitServiceDiscoverRepositoriesRejectsOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	service := NewGitService()
	if err := service.setWorkspaceRoot(workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DiscoverRepositories(outside); err == nil {
		t.Fatal("DiscoverRepositories accepted a root outside the workspace")
	}
}
