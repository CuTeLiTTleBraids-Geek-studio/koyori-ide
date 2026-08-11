package services

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// File permission policy:
// Files are created with mode 0644 (owner read/write, group/others read-only).
// Directories are created with mode 0755 (owner rwx, group/others rx).
// These fixed modes are used instead of respecting umask to ensure
// consistent behavior across platforms (Windows ignores Unix permission bits,
// macOS/Linux honor them). Users who need different permissions can chmod
// after creation via the terminal.

// DirEntry represents a single file or folder returned by ListDirectory.
type DirEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

// FileService exposes file-system operations to the frontend.
// All file operations require a workspace root set via SetWorkspaceRoot and
// are sandboxed to that directory.
//
// Priority 4 (prompt-1.md): 支持多根工作区。当 rootDirs 非空时，文件在
// 任一已注册根目录下都视为合法。单根模式 (rootDir) 保留作为向后兼容路径。
type FileService struct {
	mu               sync.Mutex
	saveMu           sync.Mutex
	rootDir          string
	workspaceContext *WorkspaceContext
	startReveal      func(*exec.Cmd) error
	// rootDirs 是多根工作区列表（Priority 4）。当列表非空时，validatePath
	// / validateMutatingPath 会优先按多根语义校验：路径在任一根下都通过。
	// 当 rootDirs 仅含一个元素时，行为等同于 rootDir = rootDirs[0]。
	rootDirs []string
	app      *application.App
	// N-5: gitignore 缓存改为实例字段（原为包级全局 var）。
	// 每个 FileService 实例拥有独立缓存，项目切换时旧实例释放即自动回收，
	// 消除包级缓存无界增长问题。懒初始化，首次调用时创建 map。
	gitignoreMu    sync.Mutex
	gitignoreCache map[string]gitignoreCacheEntry
	writeAtomic    atomicFileWriter
}

const maxReadableFileBytes int64 = 20 * 1024 * 1024

// NewFileService creates a new FileService with no workspace root set.
func NewFileService() *FileService {
	return &FileService{}
}

// NewFileServiceWithWorkspaceContext creates the renderer-facing file service.
// The legacy constructor remains available for trusted headless callers that
// install an explicit root through ProjectService.
func NewFileServiceWithWorkspaceContext(workspaceContext *WorkspaceContext) *FileService {
	return &FileService{workspaceContext: workspaceContext}
}

// setWorkspaceContext binds file path checks to the shared workspace
// generation without exposing a renderer root setter.
//
//wails:ignore
func (f *FileService) setWorkspaceContext(ctx *WorkspaceContext) {
	f.mu.Lock()
	f.workspaceContext = ctx
	f.mu.Unlock()
}

// setApp links the application instance so FileService can emit events
// (e.g. "file:saved" after WriteFile). Called from main.go after the app
// is created. When not set, event emission is skipped (Proposal B).
//
//wails:ignore
func (f *FileService) setApp(app *application.App) {
	f.app = app
}

func (f *FileService) emitFileSaved(path string) {
	if f.app != nil {
		f.app.Event.Emit("file:saved", path)
	}
}

// setWorkspaceRoot sets the directory within which file operations are allowed.
// Pass an empty string to clear the workspace and deny file operations.
//
// Priority 4: 调用此方法会清空多根列表，回到单根模式。
//
//wails:ignore
func (f *FileService) setWorkspaceRoot(root string) error {
	if root == "" {
		f.mu.Lock()
		f.rootDir = ""
		f.rootDirs = nil
		f.mu.Unlock()
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace root is not a directory: %s", abs)
	}
	f.mu.Lock()
	f.rootDir = abs
	// 单根模式：清空多根列表，避免状态分裂。
	f.rootDirs = nil
	f.mu.Unlock()
	return nil
}

// setWorkspaceRoots 设置多根工作区列表（Priority 4 多根工作区）。
// roots 中的每一项必须是已存在的目录；任一项校验失败即整体回滚。
// 调用成功后，单根字段 rootDir 会被设为 roots[0]（保持向后兼容），
// 同时 rootDirs 保存全部根（去重后）。传入空切片等价于 SetWorkspaceRoot("")。
//
//wails:ignore
func (f *FileService) setWorkspaceRoots(roots []string) error {
	// 去重 + 复制以避免外部修改。
	cleaned := make([]string, 0, len(roots))
	seen := make(map[string]bool, len(roots))
	for _, r := range roots {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		cleaned = append(cleaned, r)
	}
	if len(cleaned) == 0 {
		return f.setWorkspaceRoot("")
	}
	// 校验每一项都是已存在的目录。
	absRoots := make([]string, 0, len(cleaned))
	for _, r := range cleaned {
		abs, err := filepath.Abs(r)
		if err != nil {
			return err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("workspace root is not a directory: %s", abs)
		}
		absRoots = append(absRoots, abs)
	}
	f.mu.Lock()
	// 单根退化：与 SetWorkspaceRoot 等价 —— rootDirs 清空，仅 rootDir 被设。
	// 多根模式：rootDirs 保留全部根，rootDir = roots[0] 保持向后兼容。
	if len(absRoots) == 1 {
		f.rootDirs = nil
	} else {
		f.rootDirs = absRoots
	}
	// 主根：第一个根目录，保持向后兼容。
	f.rootDir = absRoots[0]
	f.mu.Unlock()
	return nil
}

// WorkspaceRoots 返回当前生效的工作区根列表。当多根模式未启用时，
// 返回仅含 rootDir 的切片（可能为空）。
func (f *FileService) WorkspaceRoots() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rootDirs) > 0 {
		out := make([]string, len(f.rootDirs))
		copy(out, f.rootDirs)
		return out
	}
	if f.rootDir != "" {
		return []string{f.rootDir}
	}
	return nil
}

// validatePath returns the absolute path if it's within a workspace root, or
// an error if it's outside. FileService is renderer-facing, so an empty root
// fails closed for reads, listings, and mutations alike.
//
// G-QUAL-02: path traversal defense is centralized in pathsec.go — see
// ValidatePathWithinRoot for the symlink-resolving implementation (N-56).
// Priority 4: 多根模式下，路径在任一根下都通过校验。
func (f *FileService) validatePath(path string) (string, error) {
	resolved, _, err := f.validatePathWithLease(path)
	return resolved, err
}

func (f *FileService) validatePathWithLease(path string) (string, workspaceLease, error) {
	f.mu.Lock()
	root := f.rootDir
	roots := f.rootDirs
	workspaceContext := f.workspaceContext
	f.mu.Unlock()
	lease := workspaceLease{root: root}
	if workspaceContext != nil {
		var err error
		lease, err = acquireWorkspaceLease(workspaceContext, "", 0)
		if err != nil {
			return "", workspaceLease{}, err
		}
		if root == "" || !sameWorkspaceIdentityPath(root, lease.root) {
			return "", workspaceLease{}, fmt.Errorf("file workspace switch is not committed: %w", ErrNotAllowed)
		}
	}
	if root == "" && len(roots) == 0 {
		return "", workspaceLease{}, fmt.Errorf("no workspace root: open a project before accessing files: %w", ErrNotAllowed)
	}
	var resolved string
	var err error
	if len(roots) > 0 {
		resolved, err = ValidatePathWithinRoots(roots, path)
	} else {
		resolved, err = ValidatePathWithinRoot(root, path)
	}
	if err != nil {
		return "", workspaceLease{}, err
	}
	if err := lease.validateCurrent(); err != nil {
		return "", workspaceLease{}, err
	}
	return resolved, lease, nil
}

// validateMutatingPath documents mutation call sites while sharing the same
// fail-closed renderer boundary as read and list operations.
//
// Priority 4: 多根模式下，写入操作需在任一根下；校验逻辑同 validatePath。
func (f *FileService) validateMutatingPath(path string) (string, error) {
	return f.validatePath(path)
}

// ListDirectory returns the immediate children of path, directories first.
func (f *FileService) ListDirectory(path string) ([]DirEntry, error) {
	abs, err := f.validatePath(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	result := make([]DirEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, DirEntry{
			Name:     entry.Name(),
			Path:     filepath.Join(abs, entry.Name()),
			IsDir:    entry.IsDir(),
			Size:     info.Size(),
			Modified: info.ModTime().UnixMilli(),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// ReadFile reads and returns the full text content of a file.
func (f *FileService) ReadFile(path string) (string, error) {
	abs, err := f.validatePath(path)
	if err != nil {
		return "", err
	}
	// BUG6: Return a user-friendly error when the file doesn't exist
	// instead of the raw OS error ("The system cannot find the file specified").
	if _, statErr := os.Stat(abs); os.IsNotExist(statErr) {
		return "", fmt.Errorf("file not found: %s", abs)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.Size() > maxReadableFileBytes {
		return "", fmt.Errorf("file is too large to open (%d bytes, limit %d): %w", info.Size(), maxReadableFileBytes, ErrInvalidInput)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteFile writes text content to a file, creating or truncating it.
// After a successful write, emits a "file:saved" event with the absolute
// file path so workflow triggers (Proposal B) can match it.
// prompt-6 Task 4: requires a workspace root (empty root → error).
func (f *FileService) WriteFile(path string, content string) error {
	abs, err := f.validateMutatingPath(path)
	if err != nil {
		return err
	}
	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	return f.writeValidatedFile(abs, content)
}

// CreateFile creates an empty file.
// prompt-6 Task 4: requires a workspace root.
func (f *FileService) CreateFile(path string) error {
	abs, err := f.validateMutatingPath(path)
	if err != nil {
		return err
	}
	file, err := os.Create(abs)
	if err != nil {
		return err
	}
	return file.Close()
}

// CreateDirectory creates a directory and any necessary parents.
// prompt-6 Task 4: requires a workspace root.
func (f *FileService) CreateDirectory(path string) error {
	abs, err := f.validateMutatingPath(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, 0755)
}

// DeletePath removes a file or directory recursively.
// prompt-6 Task 4: requires a workspace root.
func (f *FileService) DeletePath(path string) error {
	abs, err := f.validateMutatingPath(path)
	if err != nil {
		return err
	}
	return os.RemoveAll(abs)
}

// RenamePath moves or renames a file or directory.
// prompt-6 Task 4: requires a workspace root.
func (f *FileService) RenamePath(oldPath, newPath string) error {
	oldAbs, err := f.validateMutatingPath(oldPath)
	if err != nil {
		return err
	}
	newAbs, err := f.validateMutatingPath(newPath)
	if err != nil {
		return err
	}
	return os.Rename(oldAbs, newAbs)
}

// PickDirectory opens a native directory-selection dialog and returns the chosen path.
// Returns an empty string if the user cancels.
func (f *FileService) PickDirectory() (string, error) {
	dialog := application.Get().Dialog.OpenFile()
	dialog.SetTitle("Open Folder")
	dialog.CanChooseFiles(false)
	dialog.CanChooseDirectories(true)
	return dialog.PromptForSingleSelection()
}

// quickOpenIgnoreDirs is a hardcoded list of directories that are virtually
// always noise for Quick Open. These are skipped in addition to any patterns
// found in .gitignore. Hidden directories (starting with ".") are also skipped.
var quickOpenIgnoreDirs = map[string]bool{
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"out":          true,
	"target":       true,
	"vendor":       true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	"env":          true,
	".idea":        true,
	".vscode":      true,
	".next":        true,
	".nuxt":        true,
	".svelte-kit":  true,
	".gradle":      true,
	"bin":          true,
	"obj":          true,
	"coverage":     true,
	".cache":       true,
}

// maxQuickOpenFiles caps the number of files returned by ListAllFiles to
// prevent excessive memory use on very large repositories. 10000 is plenty
// for Quick Open — anything beyond that is unlikely to be useful in a
// fuzzy finder and would hurt responsiveness.
const maxQuickOpenFiles = 10000

// ListAllFiles walks the directory tree rooted at rootPath and returns the
// relative paths of all files, using forward slashes for cross-platform
// consistency. It skips:
//   - directories listed in quickOpenIgnoreDirs
//   - hidden directories and files (starting with ".")
//   - patterns listed in the root .gitignore (simple matching: exact name,
//     leading "/" for root-anchored, trailing "/" for dir-only, and "*"
//     wildcards within a single path segment)
//
// The result is sorted lexicographically. If rootPath is not within the
// workspace root (when sandboxing is active), an error is returned.
func (f *FileService) ListAllFiles(rootPath string) ([]string, error) {
	abs, err := f.validatePath(rootPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", abs)
	}

	// Load .gitignore patterns from the root directory.
	patterns := f.loadGitignorePatterns(abs)

	var result []string
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		name := d.Name()
		// Skip the root itself.
		if path == abs {
			return nil
		}
		// Skip hidden entries (starting with ".").
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip ignored directories.
		if d.IsDir() && quickOpenIgnoreDirs[name] {
			return filepath.SkipDir
		}
		// Compute the relative path with forward slashes.
		rel, rerr := filepath.Rel(abs, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// Check .gitignore patterns.
		if matchGitignore(rel, d.IsDir(), patterns) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			result = append(result, rel)
			if len(result) >= maxQuickOpenFiles {
				return errStopWalk
			}
		}
		return nil
	})
	// errStopWalk is a sentinel used to cap the result size; treat it as success.
	if err != nil && err != errStopWalk {
		return nil, err
	}
	sort.Strings(result)
	return result, nil
}

// errStopWalk is a sentinel error used to stop filepath.WalkDir early once
// the result cap (maxQuickOpenFiles) is reached.
var errStopWalk = fmt.Errorf("stop walk: max file count reached")

// gitignorePattern represents a single parsed .gitignore line.
type gitignorePattern struct {
	negate   bool     // "!" prefix
	dirOnly  bool     // trailing "/"
	anchored bool     // leading "/" — pattern is relative to the .gitignore dir
	segments []string // path segments, each may contain "*" / "?" wildcards;
	// "**" is a segment-wide recursive wildcard
}

// gitignoreCacheEntry caches the parsed patterns for one directory keyed by
// the .gitignore file's mtime. M-5: avoids re-reading and re-parsing the
// .gitignore on every ListAllFiles call (which previously hit the disk on
// every invocation).
type gitignoreCacheEntry struct {
	patterns []gitignorePattern
	mtime    time.Time // zero when .gitignore is absent — also cacheable
}

// gitignoreCache is now an instance-level field on FileService (N-5),
// eliminating the unbounded package-level cache. See FileService.gitignoreCache.

// loadGitignorePatterns reads .gitignore from dir (if present) and parses
// it into a list of patterns. Results are cached in the FileService instance
// keyed by (dir, mtime) so repeated calls do not re-read or re-parse the
// file (M-5). Each FileService instance owns its own cache (N-5), so
// switching projects (dropping the old FileService) automatically releases
// the old cache.
//
// Each line is parsed as follows:
//   - empty lines and lines starting with "#" are skipped
//   - leading "!" sets negate=true
//   - leading "/" anchors the pattern to the .gitignore directory
//   - trailing "/" marks the pattern as dir-only
//   - the rest is split on "/" into segments; "**" is recognised as a
//     recursive (multi-segment) wildcard, "*" and "?" are handled per
//     segment by matchSegment at match time
func (f *FileService) loadGitignorePatterns(dir string) []gitignorePattern {
	gitPath := filepath.Join(dir, ".gitignore")
	info, err := os.Stat(gitPath)
	var mtime time.Time
	if err == nil {
		mtime = info.ModTime()
	}

	f.gitignoreMu.Lock()
	defer f.gitignoreMu.Unlock()
	if f.gitignoreCache == nil {
		f.gitignoreCache = make(map[string]gitignoreCacheEntry)
	}
	if entry, ok := f.gitignoreCache[dir]; ok && entry.mtime.Equal(mtime) {
		return entry.patterns
	}

	var patterns []gitignorePattern
	if err == nil {
		data, rerr := os.ReadFile(gitPath)
		if rerr == nil {
			patterns = parseGitignoreContent(string(data))
		}
	}
	f.gitignoreCache[dir] = gitignoreCacheEntry{patterns: patterns, mtime: mtime}
	return patterns
}

// parseGitignoreContent parses the textual contents of a .gitignore file
// into a slice of gitignorePattern. Extracted from loadGitignorePatterns
// so the cache layer is independent of parsing (and tests can call it
// without touching the filesystem / cache).
func parseGitignoreContent(content string) []gitignorePattern {
	var patterns []gitignorePattern
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := gitignorePattern{}
		if strings.HasPrefix(line, "!") {
			p.negate = true
			line = line[1:]
		}
		if strings.HasPrefix(line, "/") {
			p.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		p.segments = strings.Split(line, "/")
		patterns = append(patterns, p)
	}
	return patterns
}

// matchGitignore returns true if relPath should be ignored according to the
// given patterns. relPath is a forward-slash-relative path. isDir indicates
// whether the entry is a directory (used for dir-only patterns).
func matchGitignore(relPath string, isDir bool, patterns []gitignorePattern) bool {
	ignored := false
	for _, p := range patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if !matchPath(relPath, p) {
			continue
		}
		if p.negate {
			ignored = false
		} else {
			ignored = true
		}
	}
	return ignored
}

// matchPath checks whether relPath matches a single gitignore pattern.
// For non-anchored patterns, the pattern matches if ANY path suffix matches
// (mirroring gitignore's "match in any directory" rule). For anchored
// patterns, the full relative path must match.
func matchPath(relPath string, p gitignorePattern) bool {
	segments := strings.Split(relPath, "/")
	if p.anchored {
		return matchSegments(segments, p.segments)
	}
	// Non-anchored: try matching at every suffix.
	for i := 0; i < len(segments); i++ {
		if matchSegments(segments[i:], p.segments) {
			return true
		}
	}
	return false
}

// matchSegments checks whether pathSegs matches patternSegs, supporting the
// recursive "**" wildcard (M-5). A "**" segment matches zero or more path
// segments. If the pattern has fewer segments than the path, the match still
// succeeds (gitignore treats "foo/bar" as matching "foo/bar/anything").
//
// The matcher also handles "?" (single character) and "*" (zero or more
// characters within one segment) via matchSegment.
func matchSegments(pathSegs, patternSegs []string) bool {
	return matchSegmentsRec(pathSegs, patternSegs, 0, 0)
}

// matchSegmentsRec is the recursive workhorse for matchSegments. It uses
// backtracking on "**" segments to try every possible expansion. The
// recursion depth is bounded by len(patternSegs), which for .gitignore
// patterns is small (typically 1–3), so this is safe.
func matchSegmentsRec(pathSegs, patternSegs []string, pi, si int) bool {
	for pi < len(patternSegs) {
		if patternSegs[pi] == "**" {
			// "**" matches zero or more segments. Try every expansion.
			for j := si; j <= len(pathSegs); j++ {
				if matchSegmentsRec(pathSegs, patternSegs, pi+1, j) {
					return true
				}
			}
			return false
		}
		if si >= len(pathSegs) {
			return false
		}
		if !matchSegment(pathSegs[si], patternSegs[pi]) {
			return false
		}
		pi++
		si++
	}
	// All pattern segments matched. Leftover path segments are OK —
	// gitignore treats "foo/bar" as matching "foo/bar/baz".
	return true
}

// matchSegment matches a single path segment against a pattern segment,
// supporting "*" (zero or more characters within the segment) and "?"
// (exactly one character). "**" is handled at the segment level by
// matchSegmentsRec, not here. M-5 added "?" support.
//
// This is an iterative two-pointer matcher with backtracking on "*".
func matchSegment(seg, pattern string) bool {
	// Fast path: no wildcard chars.
	if !strings.ContainsAny(pattern, "*?") {
		return seg == pattern
	}
	si, pi := 0, 0
	starIdx, matchIdx := -1, 0
	for si < len(seg) {
		if pi < len(pattern) && (pattern[pi] == seg[si] || pattern[pi] == '?') {
			// '?' matches any single character.
			si++
			pi++
		} else if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
		} else if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
		} else {
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// RevealInOS opens the host operating system's file explorer and selects
// the given path. On Windows it uses `explorer.exe /select,`; on macOS it
// uses `open -R`; on Linux it uses `xdg-open` on the parent directory
// (no universal "select" flag exists across Linux file managers).
//
// N-105: the previous implementation called cmd.Start() without a paired
// cmd.Wait(), leaving zombie processes on Unix until the parent (the IDE)
// exited. We now start the command and reap it in a goroutine so the
// caller still returns immediately (the explorer launch is non-blocking
// from the user's perspective) but no zombie lingers.
func (f *FileService) RevealInOS(path string) error {
	abs, lease, err := f.validatePathWithLease(path)
	if err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// explorer.exe /select, works with both files and directories.
		cmd = exec.Command("explorer.exe", "/select,", abs)
	case "darwin":
		// `open -R` reveals a file in Finder; for a directory, just open it.
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			cmd = exec.Command("open", abs)
		} else {
			cmd = exec.Command("open", "-R", abs)
		}
	default: // linux and other unix-like
		// xdg-open opens the parent directory; selecting the file is not
		// universally supported across file managers.
		dir := abs
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			dir = filepath.Dir(abs)
		}
		cmd = exec.Command("xdg-open", dir)
	}
	f.mu.Lock()
	startReveal := f.startReveal
	f.mu.Unlock()
	if startReveal == nil {
		startReveal = func(command *exec.Cmd) error { return command.Start() }
	}
	if err := lease.withCurrent(func() error { return startReveal(cmd) }); err != nil {
		return err
	}
	// Reap the child process asynchronously so it doesn't become a
	// zombie. The error (if any) is logged but not surfaced — by the
	// time Wait returns the caller has long moved on, and a non-zero
	// exit from the file manager is not actionable.
	go func() {
		if werr := cmd.Wait(); werr != nil {
			slog.Debug("reveal command exited non-zero", "cmd", cmd.Args, "err", werr)
		}
	}()
	return nil
}
