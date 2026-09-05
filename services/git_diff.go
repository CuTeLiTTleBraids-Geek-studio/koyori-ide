package services

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
)

// Working tree and index diff operations.
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

// GetDiffForSide returns the diff for one side of a status row identity.
// A file can appear twice in the sidebar (staged and unstaged); the row the
// user clicked must show its own side, not GetDiff's staged-first choice:
// staged=true diffs HEAD vs index, staged=false diffs index vs worktree
// (fully untracked files fall back to the all-additions diff).
func (g *GitService) GetDiffForSide(repoPath string, filePath string, staged bool) (string, error) {
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
	repo, err := g.openRepo(repoPath)
	if err != nil {
		return "", err
	}
	if staged {
		return g.diffStaged(repo, filePath)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	status, err := wt.Status()
	if err != nil {
		return "", err
	}
	if fileStatus, ok := status[filePath]; ok &&
		fileStatus.Staging == git.Untracked && fileStatus.Worktree == git.Untracked {
		return g.diffUntrackedFile(repoPath, filePath)
	}
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
	buf.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", filePath, filePath))
	buf.WriteString("new file mode 100644\n")
	buf.WriteString("--- /dev/null\n")
	buf.WriteString(fmt.Sprintf("+++ b/%s\n", filePath))
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
	buf.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", filePath, filePath))
	buf.WriteString("new file mode 100644\n")
	buf.WriteString("--- /dev/null\n")
	buf.WriteString(fmt.Sprintf("+++ b/%s\n", filePath))
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
		buf.WriteString(fmt.Sprintf("=== %s ===\n", c.Path))
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
