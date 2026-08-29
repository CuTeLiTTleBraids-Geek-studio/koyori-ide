package services

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// secureWorkspace is an immutable, reference-counted set of os.Root
// capabilities. Root handles stay open while at least one operation holds a
// capability and close when the workspace is idle. Reopening verifies the
// original directory identity before an operation can proceed.
type secureWorkspace struct {
	mu         sync.Mutex
	refs       int
	retired    bool
	closeErr   error
	roots      []secureWorkspaceRoot
	generation uint64
}

type secureWorkspaceRoot struct {
	absolute string
	identity os.FileInfo
	root     *os.Root
}

type fileCapability struct {
	service   *FileService
	workspace *secureWorkspace
	root      *secureWorkspaceRoot
	relative  string
	lease     workspaceLease
	release   func()
}

func newSecureWorkspace(paths []string) (*secureWorkspace, error) {
	workspace := &secureWorkspace{roots: make([]secureWorkspaceRoot, 0, len(paths))}
	for _, path := range paths {
		absolute, err := canonicalSecureRoot(path)
		if err != nil {
			workspace.close()
			return nil, err
		}
		root, err := os.OpenRoot(absolute)
		if err != nil {
			workspace.close()
			return nil, fmt.Errorf("open workspace root %q: %w", absolute, err)
		}
		identity, err := root.Stat(".")
		if err != nil {
			_ = root.Close()
			workspace.close()
			return nil, fmt.Errorf("identify workspace root %q: %w", absolute, err)
		}
		duplicate := false
		for i := range workspace.roots {
			if os.SameFile(workspace.roots[i].identity, identity) {
				duplicate = true
				break
			}
		}
		if duplicate {
			_ = root.Close()
			continue
		}
		canonical := absolute
		if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
			resolvedInfo, statErr := os.Stat(resolved)
			if statErr != nil || !os.SameFile(identity, resolvedInfo) {
				_ = root.Close()
				workspace.close()
				return nil, fmt.Errorf("workspace root changed while binding %q: %w", absolute, ErrNotAllowed)
			}
			canonical = filepath.Clean(resolved)
		}
		workspace.roots = append(workspace.roots, secureWorkspaceRoot{
			absolute: canonical,
			identity: identity,
			root:     root,
		})
	}
	if err := workspace.closeRootsLocked(); err != nil {
		workspace.closeErr = err
		return nil, fmt.Errorf("close idle workspace roots: %w", err)
	}
	return workspace, nil
}

func canonicalSecureRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("workspace root is required: %w", ErrInvalidInput)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	absolute = filepath.Clean(absolute)
	return absolute, nil
}

func (w *secureWorkspace) acquire() (func(), error) {
	if w == nil {
		return nil, fmt.Errorf("workspace root is not configured: %w", ErrNotAllowed)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.retired {
		return nil, fmt.Errorf("workspace root is retired: %w", ErrNotAllowed)
	}
	if w.closeErr != nil {
		return nil, fmt.Errorf("workspace root close failed: %w", w.closeErr)
	}
	if w.refs == 0 {
		if err := w.openRootsLocked(); err != nil {
			return nil, err
		}
	}
	w.refs++
	var once sync.Once
	return func() { once.Do(w.release) }, nil
}

func (w *secureWorkspace) release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refs > 0 {
		w.refs--
	}
	if w.refs == 0 {
		w.closeLocked()
	}
}

func (w *secureWorkspace) retire() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.retired = true
	if w.refs != 0 {
		return nil
	}
	w.closeLocked()
	return w.closeErr
}

func (w *secureWorkspace) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.retired = true
	if w.refs == 0 {
		w.closeLocked()
	}
	return w.closeErr
}

func (w *secureWorkspace) closeLocked() {
	w.closeErr = errorsJoin(w.closeErr, w.closeRootsLocked())
}

func (w *secureWorkspace) closeRootsLocked() error {
	var closeErr error
	for i := range w.roots {
		if w.roots[i].root != nil {
			closeErr = errorsJoin(closeErr, w.roots[i].root.Close())
			w.roots[i].root = nil
		}
	}
	return closeErr
}

func (w *secureWorkspace) openRootsLocked() error {
	opened := make([]int, 0, len(w.roots))
	for i := range w.roots {
		if w.roots[i].root != nil {
			continue
		}
		root, err := os.OpenRoot(w.roots[i].absolute)
		if err != nil {
			return errorsJoin(fmt.Errorf("reopen workspace root %q: %w", w.roots[i].absolute, err), w.closeOpenedRoots(opened))
		}
		identity, err := root.Stat(".")
		if err != nil || !os.SameFile(w.roots[i].identity, identity) {
			_ = root.Close()
			if err == nil {
				err = ErrNotAllowed
			}
			return errorsJoin(
				fmt.Errorf("workspace root identity changed for %q: %w", w.roots[i].absolute, err),
				w.closeOpenedRoots(opened),
			)
		}
		w.roots[i].root = root
		opened = append(opened, i)
	}
	return nil
}

func (w *secureWorkspace) closeOpenedRoots(indices []int) error {
	var closeErr error
	for _, index := range indices {
		if w.roots[index].root != nil {
			closeErr = errorsJoin(closeErr, w.roots[index].root.Close())
			w.roots[index].root = nil
		}
	}
	return closeErr
}

func errorsJoin(left, right error) error {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	return fmt.Errorf("%v; %w", left, right)
}

func (f *FileService) acquireCapability(path string, mutation bool) (fileCapability, error) {
	f.mu.Lock()
	workspace := f.secureWorkspace
	root := f.rootDir
	roots := append([]string(nil), f.rootDirs...)
	ctx := f.workspaceContext
	f.mu.Unlock()
	if workspace == nil {
		return fileCapability{}, fmt.Errorf("no workspace root: open a project before accessing files: %w", ErrNotAllowed)
	}
	release, err := workspace.acquire()
	if err != nil {
		return fileCapability{}, err
	}
	capability := fileCapability{service: f, workspace: workspace, release: release}
	if ctx != nil {
		capability.lease, err = acquireWorkspaceLease(ctx, "", 0)
	} else {
		capability.lease = workspaceLease{root: root}
	}
	if err != nil {
		release()
		return fileCapability{}, err
	}
	if err := capability.lease.validateCurrent(); err != nil {
		release()
		return fileCapability{}, err
	}
	if root == "" || (ctx != nil && !sameWorkspaceIdentityPath(root, capability.lease.root)) {
		release()
		return fileCapability{}, fmt.Errorf("file workspace switch is not committed: %w", ErrNotAllowed)
	}
	if len(roots) == 0 {
		roots = []string{root}
	}
	selected, relative, err := selectSecureRoot(workspace, roots, path)
	if err != nil {
		release()
		return fileCapability{}, err
	}
	if mutation && relative == "." {
		release()
		return fileCapability{}, fmt.Errorf("workspace root cannot be mutated: %w", ErrNotAllowed)
	}
	capability.root = selected
	capability.relative = relative
	return capability, nil
}

func (c *fileCapability) releaseCapability() {
	if c.release != nil {
		c.release()
		c.release = nil
	}
}

func (c fileCapability) displayPath() string {
	if c.relative == "." {
		return c.root.absolute
	}
	return filepath.Join(c.root.absolute, filepath.FromSlash(c.relative))
}

// verifyRootPathIdentity checks the pathname-facing view against the bound
// root identity. Root operations themselves stay handle-relative; callers
// that must invoke a legacy pathname API can use this before and after that
// call and discard the result if the path was exchanged in between.
func (c fileCapability) verifyRootPathIdentity() error {
	if c.root == nil || c.root.identity == nil {
		return fmt.Errorf("file workspace root capability is incomplete: %w", ErrNotAllowed)
	}
	identity, err := os.Stat(c.root.absolute)
	if err != nil {
		return fmt.Errorf("stat file workspace root %q: %w", c.root.absolute, ErrNotAllowed)
	}
	if !os.SameFile(c.root.identity, identity) {
		return fmt.Errorf("file workspace root identity changed for %q: %w", c.root.absolute, ErrNotAllowed)
	}
	return nil
}

func (c fileCapability) withCurrent(fn func() error) error {
	if fn == nil {
		return fmt.Errorf("file capability callback is nil: %w", ErrInvalidInput)
	}
	c.service.mu.Lock()
	defer c.service.mu.Unlock()
	if c.service.secureWorkspace != c.workspace || c.workspace.generation != c.service.rootGeneration {
		return fmt.Errorf("file workspace changed before operation: %w", ErrNotAllowed)
	}
	return c.lease.withCurrent(fn)
}

// resolvedRelative follows symlinks using only the bound Root. Go's Root
// natively handles relative links; this explicit walk additionally preserves
// compatibility with absolute links whose target names the same bound root.
// The returned name is still passed to Root for the actual operation.
func (c fileCapability) resolvedRelative(followTerminal bool) (string, error) {
	path := cleanRootRelative(c.relative)
	parts := splitRootPath(path)
	resolved := make([]string, 0, len(parts))
	for links := 0; len(parts) > 0; {
		part := parts[0]
		parts = parts[1:]
		candidate := cleanRootRelative(filepath.Join(append([]string{"."}, append(resolved, part)...)...))
		info, err := c.root.root.Lstat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				return cleanRootRelative(filepath.Join(append([]string{"."}, append(append(resolved, part), parts...)...)...)), nil
			}
			return "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 || (!followTerminal && len(parts) == 0) {
			resolved = append(resolved, part)
			continue
		}
		links++
		if links > 255 {
			return "", fmt.Errorf("too many symlinks in %s: %w", c.displayPath(), ErrNotAllowed)
		}
		target, err := c.root.root.Readlink(candidate)
		if err != nil {
			return "", err
		}
		var replacement string
		if filepath.IsAbs(target) {
			relative, relErr := filepath.Rel(c.root.absolute, filepath.Clean(target))
			if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("symlink %s escapes workspace root: %w", candidate, ErrNotAllowed)
			}
			replacement = relative
		} else {
			replacement = filepath.Join(append([]string{"."}, append(resolved, target)...)...)
		}
		combined := filepath.Clean(filepath.Join(append([]string{replacement}, parts...)...))
		if combined == ".." || filepath.IsAbs(combined) || strings.HasPrefix(combined, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("symlink %s escapes workspace root: %w", candidate, ErrNotAllowed)
		}
		resolved = resolved[:0]
		parts = splitRootPath(combined)
	}
	return cleanRootRelative(filepath.Join(append([]string{"."}, resolved...)...)), nil
}

func splitRootPath(path string) []string {
	path = filepath.Clean(filepath.FromSlash(path))
	if path == "." {
		return nil
	}
	return strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == rune(filepath.Separator)
	})
}

func selectSecureRoot(workspace *secureWorkspace, paths []string, target string) (*secureWorkspaceRoot, string, error) {
	if target == "" {
		target = "."
	}
	for index := range workspace.roots {
		candidate := &workspace.roots[index]
		if index >= len(paths) {
			break
		}
		rootPath := paths[index]
		_, relative, ok := secureRelativePath(rootPath, target)
		if !ok {
			continue
		}
		return candidate, cleanRootRelative(relative), nil
	}
	return nil, "", fmt.Errorf("path %s is outside all workspace roots", target)
}

func secureRelativePath(root, target string) (string, string, bool) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", false
	}
	if strings.HasPrefix(target, "/") && !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", "", false
	}
	relative, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	return targetAbs, cleanRootRelative(relative), true
}

func cleanRootRelative(path string) string {
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." || path == "" {
		return "."
	}
	return path
}
