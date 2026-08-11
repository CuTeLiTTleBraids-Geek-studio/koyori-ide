package services

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// WorkspaceContext is the single shared source of truth for the active
// workspace identity (GOAL-P0-02).
//
// Why a shared pointer instead of per-service strings: services that captured a
// workspace root at construction time (AIPlanService, AIGoalService,
// DiffService, WorkflowEngine, and the default executors) kept an empty string
// forever, because bootstrap runs before any project is opened and nothing
// propagated the later ProjectService.OpenProject value back into them. A
// shared context makes the switch atomic for every holder at once: there is no
// window where one service reports workspace A while another reports B.
//
// Generation increments on every successful Set/Clear. Capabilities, executors,
// and snapshot triggers minted under an older generation can therefore detect
// that they belong to a workspace that is no longer active.
type WorkspaceContext struct {
	mu         sync.RWMutex
	root       string
	roots      []string
	generation uint64
}

// WorkspaceSnapshot is the immutable renderer-facing view of the active
// workspace. Root is always Roots[0] when a workspace is open.
type WorkspaceSnapshot struct {
	Root        string   `json:"root"`
	Roots       []string `json:"roots"`
	Generation  uint64   `json:"generation"`
	ProjectID   string   `json:"projectId,omitempty"`
	ProjectName string   `json:"projectName,omitempty"`
	ProjectPath string   `json:"projectPath,omitempty"`
}

// NewWorkspaceContext returns an empty context. An empty context is a
// fail-closed state: snapshot and path checks must refuse to operate rather
// than fall back to unrestricted access.
func NewWorkspaceContext() *WorkspaceContext {
	return &WorkspaceContext{}
}

// Root returns the canonicalized absolute workspace root, or "" when no
// workspace is active.
func (c *WorkspaceContext) Root() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.root
}

// Generation returns the current workspace generation. It changes on every
// successful Set and Clear.
func (c *WorkspaceContext) Generation() uint64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.generation
}

// Snapshot returns root and generation read under a single lock, so callers
// cannot observe a torn pair where the root belongs to one workspace and the
// generation to another.
func (c *WorkspaceContext) Snapshot() (string, uint64) {
	if c == nil {
		return "", 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.root, c.generation
}

// State returns root, roots, and generation under one lock. The returned roots
// slice is detached from the context and may be retained by callers.
func (c *WorkspaceContext) State() WorkspaceSnapshot {
	if c == nil {
		return WorkspaceSnapshot{Roots: []string{}}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return WorkspaceSnapshot{
		Root:       c.root,
		Roots:      append([]string{}, c.roots...),
		Generation: c.generation,
	}
}

// Set canonicalizes, validates, and installs a new workspace root, bumping the
// generation. An empty root is rejected: clearing is an explicit operation via
// Clear so a bug cannot silently degrade every consumer into the unrestricted
// empty-root state.
//
// The root must be an existing directory. This context is registered as the
// first setter in ProjectService's two-phase commit, so accepting a path that a
// later setter rejects would bump the generation for a switch that never
// happened, spuriously invalidating capabilities that stayed valid.
func (c *WorkspaceContext) Set(root string) error {
	return c.SetRoots([]string{root})
}

// SetRoots validates every workspace root before atomically publishing a new
// root/roots/generation snapshot. A single-root switch uses Set, which delegates
// here so root and roots can never diverge.
func (c *WorkspaceContext) SetRoots(roots []string) error {
	if c == nil {
		return fmt.Errorf("workspace context is nil: %w", ErrInvalidInput)
	}
	cleaned, err := canonicalizeExistingWorkspaceRoots(roots)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if sameWorkspaceRootSet(c.roots, cleaned) {
		// Reopening the same workspace is not a new generation: existing
		// capabilities and executors remain valid.
		return nil
	}
	c.root = cleaned[0]
	c.roots = append([]string(nil), cleaned...)
	c.generation++
	return nil
}

func sameWorkspaceRootSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameWorkspaceIdentityPath(left[index], right[index]) {
			return false
		}
	}
	return true
}

// canonicalizeWorkspaceRoot resolves the active workspace to one stable
// absolute path. Identity comparisons additionally use os.SameFile because
// filepath.EvalSymlinks does not expand every Windows junction spelling.
func canonicalizeWorkspaceRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("workspace root is required: %w", ErrInvalidInput)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	cleaned := filepath.Clean(abs)
	info, err := os.Stat(cleaned)
	if err != nil {
		return "", fmt.Errorf("workspace root %q is not accessible: %w", root, ErrInvalidInput)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace root %q is not a directory: %w", root, ErrInvalidInput)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(cleaned); resolveErr == nil {
		cleaned = filepath.Clean(resolved)
	}
	return cleaned, nil
}

// sameWorkspaceIdentityPath compares canonical workspace identities without
// treating Windows drive, UNC, or junction spelling differences as a switch.
func sameWorkspaceIdentityPath(left, right string) bool {
	if left == "" || right == "" {
		return left == right
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo) {
		return true
	}
	canonical := func(path string) string {
		path = filepath.Clean(path)
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = filepath.Clean(resolved)
		}
		if runtime.GOOS == "windows" {
			path = normalizeWindowsWorkspaceIdentityPath(path)
		}
		return path
	}
	return canonical(left) == canonical(right)
}

func normalizeWindowsWorkspaceIdentityPath(path string) string {
	path = filepath.Clean(path)
	lower := strings.ToLower(path)
	switch {
	case strings.HasPrefix(lower, `\\?\unc\`):
		path = `\\` + path[len(`\\?\UNC\`):]
	case strings.HasPrefix(lower, `\\?\`):
		path = path[len(`\\?\`):]
	}
	return strings.ToLower(filepath.Clean(path))
}

// restoreState puts back an exact (root, generation) pair recorded before a
// workspace switch was attempted.
//
// Rollback must not advance the generation. A switch that aborted never took
// effect, so capabilities minted for the still-current workspace have to remain
// valid; bumping the generation here would revoke them for no reason. Callers
// are trusted bootstrap/rollback paths, so the directory check in Set is
// deliberately skipped: the previous root was already validated when it was
// installed, and refusing to restore it would leave the process in a worse
// state than the one being undone.
func (c *WorkspaceContext) restoreState(root string, generation uint64) {
	roots := []string{}
	if root != "" {
		roots = []string{root}
	}
	c.restoreSnapshot(WorkspaceSnapshot{Root: root, Roots: roots, Generation: generation})
}

func (c *WorkspaceContext) restoreSnapshot(snapshot WorkspaceSnapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.root = snapshot.Root
	c.roots = append([]string(nil), snapshot.Roots...)
	c.generation = snapshot.Generation
}

// Clear drops the active workspace and bumps the generation so that anything
// bound to the previous workspace stops being accepted.
func (c *WorkspaceContext) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.root == "" {
		return
	}
	c.root = ""
	c.roots = nil
	c.generation++
}

// RequireRoot returns the active root or an error. Callers that write to disk,
// create snapshots, or validate paths must use this instead of treating an
// empty root as "no restrictions".
func (c *WorkspaceContext) RequireRoot() (string, error) {
	root := c.Root()
	if root == "" {
		return "", fmt.Errorf("no workspace is open: %w", ErrNotAllowed)
	}
	return root, nil
}

// workspaceLease binds one command launch to the root and generation that
// were active when the renderer-facing call began. Commands revalidate the
// lease immediately before starting a process so a queued request cannot run
// after the user has switched workspaces.
type workspaceLease struct {
	context    *WorkspaceContext
	root       string
	generation uint64
	// allowUnscoped is reserved for explicit legacy/headless constructors.
	// Renderer-facing production services always receive WorkspaceContext and
	// never set this flag.
	allowUnscoped bool
}

func acquireWorkspaceLease(ctx *WorkspaceContext, fallbackRoot string, fallbackGeneration uint64) (workspaceLease, error) {
	if ctx == nil {
		if fallbackRoot == "" {
			return workspaceLease{}, fmt.Errorf("workspace root is not configured: %w", ErrNotAllowed)
		}
		return workspaceLease{root: fallbackRoot, generation: fallbackGeneration}, nil
	}
	root, generation := ctx.Snapshot()
	if root == "" {
		return workspaceLease{}, fmt.Errorf("no workspace is open: %w", ErrNotAllowed)
	}
	return workspaceLease{context: ctx, root: root, generation: generation}, nil
}

func (l workspaceLease) validateCurrent() error {
	if l.allowUnscoped && l.context == nil {
		return nil
	}
	if l.root == "" {
		return fmt.Errorf("workspace root is not configured: %w", ErrNotAllowed)
	}
	if l.context == nil {
		return nil
	}
	root, generation := l.context.Snapshot()
	if generation != l.generation || !sameWorkspaceIdentityPath(root, l.root) {
		return fmt.Errorf("workspace changed before command start: %w", ErrNotAllowed)
	}
	return nil
}

// withCurrent runs fn while the context read lock is held. This closes the
// final check/start race for short-lived external side effects such as
// launching a process. Long-running operations should revalidate at their
// cancellation and publish boundaries instead.
func (l workspaceLease) withCurrent(fn func() error) error {
	if fn == nil {
		return fmt.Errorf("workspace lease callback is nil: %w", ErrInvalidInput)
	}
	if l.allowUnscoped && l.context == nil {
		return fn()
	}
	if l.root == "" {
		return fmt.Errorf("workspace root is not configured: %w", ErrNotAllowed)
	}
	if l.context == nil {
		return fn()
	}
	l.context.mu.RLock()
	defer l.context.mu.RUnlock()
	if l.context.root == "" || l.context.generation != l.generation ||
		!sameWorkspaceIdentityPath(l.context.root, l.root) {
		return fmt.Errorf("workspace changed before side effect: %w", ErrNotAllowed)
	}
	return fn()
}

func (l workspaceLease) resolve(path string) (string, error) {
	if path == "" {
		path = l.root
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(l.root, path)
	}
	resolved, err := ValidatePathWithinRoot(l.root, path)
	if err != nil {
		return "", err
	}
	return resolved, nil
}
