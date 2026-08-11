package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// pathsec.go — shared path security helpers (Proposal AA, N-91/N-92/N-67/N-112).
//
// This module centralizes path traversal defense logic that was previously
// duplicated (with varying completeness) across FileService.validatePath,
// PluginService.isPluginPathOutsideRoot, AgentService.validateCwd,
// TerminalService.validateWorkingDir, and RulesService.SaveRules.
//
// Two main classes of validation are provided:
//
//  1. Absolute path validation against a workspace root — resolves symlinks
//     on both the target and the root, then checks the relative path does
//     not escape via "..".
//
//  2. Relative name/id validation — rejects names containing path separators,
//     parent traversal (".."), absolute paths, and Windows volume-relative
//     forms. Used by services that join a user-supplied name/id to a fixed
//     directory (ConversationService, PresetService).

// ValidatePathWithinRoot returns nil if target resolves to a path inside root.
// If root is empty, any path is allowed (returns nil). The target is resolved
// to an absolute path, and symlinks on both target and root are evaluated so
// that a symlink inside the workspace pointing outside is rejected.
//
// For non-existent targets (e.g. a file about to be created), the parent
// directory's symlinks are resolved and the basename is re-joined.
//
// BUG3 fix: When root is set and target is a Unix-style absolute path
// (starts with "/" but has no Windows drive letter), it is treated as
// relative to root. This matches VS Code's behavior where "/file.txt"
// resolves to <workspace>/file.txt.
//
// Returns the resolved absolute path on success.
func ValidatePathWithinRoot(root, target string) (string, error) {
	// BUG3: Treat Unix-style absolute paths (e.g. "/11.md") as relative to
	// root when a root is set. Without this, filepath.Abs("/11.md") on
	// Windows resolves to "C:\11.md" which is outside the workspace.
	if root != "" && strings.HasPrefix(target, "/") && !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	if root == "" {
		return abs, nil
	}
	absResolved, err := evalSymlinksAllowMissing(abs)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks for %s: %w", abs, err)
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		// If the root itself can't be resolved (e.g. temp dir already
		// cleaned in tests), fall back to the lexical root.
		rootResolved = root
	}
	rel, err := filepath.Rel(rootResolved, absResolved)
	if err != nil {
		return "", fmt.Errorf("compute relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %s is outside the workspace root", target)
	}
	return abs, nil
}

// ValidateMutatingPathWithinRoot applies the stricter write-path policy. An
// empty root is never a valid authorization context for a mutation; callers
// that intentionally support unscoped reads should use ValidatePathWithinRoot.
func ValidateMutatingPathWithinRoot(root, target string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("workspace root is required for mutations: %w", ErrNotAllowed)
	}
	return ValidatePathWithinRoot(root, target)
}

// canonicalizeExistingWorkspaceRoots validates a complete workspace-root set
// before any service publishes it. The returned paths are absolute, clean,
// deduplicated, existing directories. Validation is intentionally all-or-none
// so a bad secondary root cannot leave services on different workspaces.
func canonicalizeExistingWorkspaceRoots(roots []string) ([]string, error) {
	cleaned := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace root %q: %w", root, err)
		}
		abs = filepath.Clean(abs)
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("workspace root %q is not accessible: %w", root, ErrInvalidInput)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("workspace root %q is not a directory: %w", root, ErrInvalidInput)
		}
		if _, err := ValidatePathWithinRoot(abs, abs); err != nil {
			return nil, fmt.Errorf("validate workspace root %q: %w", root, err)
		}
		canonical, canonicalErr := canonicalizeWorkspaceRoot(abs)
		if canonicalErr != nil {
			return nil, canonicalErr
		}
		duplicate := false
		for _, previous := range cleaned {
			if sameWorkspaceIdentityPath(previous, canonical) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		cleaned = append(cleaned, canonical)
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("at least one workspace root is required: %w", ErrInvalidInput)
	}
	return cleaned, nil
}

// ValidatePathWithinRoots 是 ValidatePathWithinRoot 的多根版本（Priority 4
// 多根工作区）。target 在 roots 中任一根下都视为合法，返回其解析后的绝对
// 路径。若所有根都拒绝，返回最后一个根产生的错误（保持单根行为兼容）。
//
// 当 roots 为空切片时，等价于 root="" —— 任意路径都放行（仅用于读取场景；
// 写入场景由 FileService.validateMutatingPath 单独兜底拒绝）。
//
// 多根场景下，对于 Unix 风格的绝对路径（"/file.txt"）会以第一个根为基准
// 解析（与单根模式行为一致）。
func ValidatePathWithinRoots(roots []string, target string) (string, error) {
	if len(roots) == 0 {
		return ValidatePathWithinRoot("", target)
	}
	if len(roots) == 1 {
		return ValidatePathWithinRoot(roots[0], target)
	}
	// 多根：尝试每个根；任一通过即返回。
	var lastErr error
	for _, r := range roots {
		abs, err := ValidatePathWithinRoot(r, target)
		if err == nil {
			return abs, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		// 理论不可达，但保守起见给一个错误。
		lastErr = fmt.Errorf("path %s is outside all workspace roots", target)
	}
	return "", lastErr
}

// IsPathOutsideRoot reports whether absTarget escapes rootDir. Returns true
// if the target is outside the root, false if inside. An empty rootDir means
// "no restriction" (returns false). Symlinks are resolved on both paths.
//
// This is the boolean form of ValidatePathWithinRoot for callers that only
// need the escape verdict and not the resolved path.
func IsPathOutsideRoot(rootDir, absTarget string) bool {
	if rootDir == "" {
		return false
	}
	absResolved, err := evalSymlinksAllowMissing(absTarget)
	if err != nil {
		return true // can't resolve → treat as outside for safety
	}
	rootResolved, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		rootResolved = rootDir
	}
	rel, err := filepath.Rel(rootResolved, absResolved)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// IsRelativePathSafe reports whether relPath is a safe relative path that
// does not escape its base directory. It rejects:
//  1. Empty paths (callers should handle empty separately)
//  2. Unix-style absolute paths (leading "/")
//  3. Windows backslash-absolute paths (leading "\")
//  4. Windows drive paths and UNC paths (filepath.IsAbs)
//  5. Windows volume-relative form ("C:foo")
//  6. Parent traversal (".." or "../..." or "..\...")
//  7. Current-directory alias (".") — not a valid filename component
//
// This is the canonical implementation extracted from
// plugin_service.isPluginPathOutsideRoot. It does NOT touch the filesystem
// (pure lexical check) and is safe for validating user-supplied names/ids
// before joining them to a directory.
func IsRelativePathSafe(relPath string) bool {
	if relPath == "" {
		return false
	}
	// Reject Unix-style absolute paths that filepath.IsAbs misses on Windows.
	if strings.HasPrefix(relPath, "/") || strings.HasPrefix(relPath, "\\") {
		return false
	}
	// filepath.IsAbs catches Windows drive paths ("C:\...") and UNC paths.
	if filepath.IsAbs(relPath) {
		return false
	}
	// Reject Windows volume-relative form ("C:foo" — not anchored to root).
	if len(relPath) >= 2 && relPath[1] == ':' {
		if len(relPath) == 2 || (relPath[2] != '/' && relPath[2] != '\\') {
			return false
		}
	}
	// Reject parent traversal and current-directory alias.
	cleaned := filepath.Clean(relPath)
	if cleaned == ".." || cleaned == "." {
		return false
	}
	if strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "..\\") {
		return false
	}
	return true
}

// SafeNameJoin joins baseDir, name, and ext into a path, after validating
// that name is a safe relative path component (no separators, no "..", no
// absolute paths). This is the helper for services that take a user-supplied
// id/name and append a file extension (e.g. ConversationService, PresetService).
//
// ext should include the leading dot (e.g. ".json"). If ext is empty, no
// extension is appended.
//
// Returns an error if name is empty, contains path separators, or would
// escape baseDir via traversal.
func SafeNameJoin(baseDir, name, ext string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if !IsRelativePathSafe(name) {
		return "", fmt.Errorf("invalid name %q: must be a simple filename without path separators or parent traversal", name)
	}
	// Additional check: reject any path separator in the name. Even though
	// IsRelativePathSafe rejects leading separators, a name like "sub/file"
	// would be "safe" by the traversal check but would create a subdirectory.
	// For id-based services we want a flat namespace.
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid name %q: path separators are not allowed", name)
	}
	return filepath.Join(baseDir, name+ext), nil
}

// ValidateNameForFlatDir validates that name is suitable for use as a
// filename in a flat directory (no subdirectories, no traversal). Returns
// nil if valid, an error otherwise.
//
// This is a lighter-weight check than SafeNameJoin for callers that don't
// need the joined path (e.g. delete operations that construct the path
// themselves).
func ValidateNameForFlatDir(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !IsRelativePathSafe(name) {
		return fmt.Errorf("invalid name %q: must be a simple filename without path separators or parent traversal", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid name %q: path separators are not allowed", name)
	}
	return nil
}

// evalSymlinksAllowMissing calls filepath.EvalSymlinks on path. If the path
// does not exist (e.g. a file or directory about to be created), it walks up
// to the nearest existing ancestor, resolves its symlinks, and rejoins the
// missing suffix. This prevents traversal through a symlink followed by one
// or more not-yet-existing path components.
//
// This was previously defined in file_service.go; it is now shared via
// pathsec.go so all services can use it.
func evalSymlinksAllowMissing(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	current := path
	var missing []string
	for {
		missing = append([]string{filepath.Base(current)}, missing...)
		parent := filepath.Dir(current)
		parentResolved, parentErr := filepath.EvalSymlinks(parent)
		if parentErr == nil {
			parts := append([]string{parentResolved}, missing...)
			return filepath.Join(parts...), nil
		}
		if !os.IsNotExist(parentErr) {
			return "", parentErr
		}
		if parent == current {
			return "", parentErr
		}
		current = parent
	}
}
