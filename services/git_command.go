package services

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing/format/index"
)

// Git CLI boundary, ignore templates, rebase, and conflict operations.
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
