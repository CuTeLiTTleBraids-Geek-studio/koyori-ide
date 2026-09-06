package services

import (
	"fmt"
	"io"
	"io/fs"
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
	rootDirs        []string
	secureWorkspace *secureWorkspace
	rootGeneration  uint64
	app             *application.App
	// N-5: gitignore 缓存改为实例字段（原为包级全局 var）。
	// 每个 FileService 实例拥有独立缓存，项目切换时旧实例释放即自动回收，
	// 消除包级缓存无界增长问题。懒初始化，首次调用时创建 map。
	gitignoreMu       sync.Mutex
	gitignoreCache    map[string]gitignoreCacheEntry
	writeAtomic       atomicFileWriter
	rootOperationHook func(string) error
	readFileAfterStat func() error
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
		return f.publishSecureWorkspace(nil, nil)
	}
	return f.setWorkspaceRoots([]string{root})
}

func (f *FileService) publishSecureWorkspace(workspace *secureWorkspace, roots []string) error {
	f.mu.Lock()
	old := f.secureWorkspace
	f.rootGeneration++
	if workspace != nil {
		workspace.generation = f.rootGeneration
	}
	f.secureWorkspace = workspace
	if len(roots) == 0 {
		f.rootDir = ""
		f.rootDirs = nil
	} else {
		f.rootDir = roots[0]
		if len(roots) == 1 {
			f.rootDirs = nil
		} else {
			f.rootDirs = append([]string(nil), roots...)
		}
	}
	f.mu.Unlock()
	if old != nil {
		return old.retire()
	}
	return nil
}

// close releases the active workspace handles. It is used by shutdown and
// tests; switching workspaces retires old handles automatically.
//
//wails:ignore
func (f *FileService) close() error {
	return f.publishSecureWorkspace(nil, nil)
}

// ServiceShutdown releases bound workspace handles during application shutdown.
// Wails treats this lifecycle method as internal rather than a renderer binding.
func (f *FileService) ServiceShutdown() error {
	return f.close()
}

func openSecureWorkspace(roots []string) (*secureWorkspace, []string, error) {
	if len(roots) == 0 {
		return nil, nil, nil
	}
	absolute := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		path, err := canonicalSecureRoot(root)
		if err != nil {
			return nil, nil, err
		}
		key := filepath.Clean(path)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		absolute = append(absolute, path)
	}
	if len(absolute) == 0 {
		return nil, nil, nil
	}
	workspace, err := newSecureWorkspace(absolute)
	if err != nil {
		return nil, nil, err
	}
	bound := make([]string, len(workspace.roots))
	for i := range workspace.roots {
		bound[i] = workspace.roots[i].absolute
	}
	return workspace, bound, nil
}

// setWorkspaceRoots 设置多根工作区列表（Priority 4 多根工作区）。
// roots 中的每一项必须是已存在的目录；任一项校验失败即整体回滚。
// 调用成功后，单根字段 rootDir 会被设为 roots[0]（保持向后兼容），
// 同时 rootDirs 保存全部根（去重后）。传入空切片等价于 SetWorkspaceRoot("")。
//
//wails:ignore
func (f *FileService) setWorkspaceRoots(roots []string) error {
	workspace, absolute, err := openSecureWorkspace(roots)
	if err != nil {
		return err
	}
	if err := f.publishSecureWorkspace(workspace, absolute); err != nil {
		return err
	}
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
	capability, err := f.acquireCapability(path, false)
	if err != nil {
		return nil, err
	}
	defer capability.releaseCapability()
	if err := f.runRootOperationHook("ListDirectory"); err != nil {
		return nil, err
	}
	var result []DirEntry
	err = capability.withCurrent(func() error {
		resolved, resolveErr := capability.resolvedRelative(true)
		if resolveErr != nil {
			return resolveErr
		}
		entries, readErr := fs.ReadDir(capability.root.root.FS(), resolved)
		if readErr != nil {
			return readErr
		}
		result = make([]DirEntry, 0, len(entries))
		for _, entry := range entries {
			child := cleanRootRelative(filepath.Join(filepath.FromSlash(resolved), entry.Name()))
			info, statErr := capability.root.root.Stat(child)
			if statErr != nil {
				continue
			}
			result = append(result, DirEntry{
				Name:     entry.Name(),
				Path:     filepath.Join(capability.displayPath(), entry.Name()),
				IsDir:    info.IsDir(),
				Size:     info.Size(),
				Modified: info.ModTime().UnixMilli(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
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
	capability, err := f.acquireCapability(path, false)
	if err != nil {
		return "", err
	}
	defer capability.releaseCapability()
	if err := f.runRootOperationHook("ReadFile"); err != nil {
		return "", err
	}
	var data []byte
	err = capability.withCurrent(func() error {
		resolved, resolveErr := capability.resolvedRelative(true)
		if resolveErr != nil {
			return resolveErr
		}
		file, openErr := capability.root.root.Open(resolved)
		if os.IsNotExist(openErr) {
			return fmt.Errorf("file not found: %s", capability.displayPath())
		}
		if openErr != nil {
			return openErr
		}
		closed := false
		defer func() {
			if !closed {
				_ = file.Close()
			}
		}()
		info, statErr := file.Stat()
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("path is not a regular file: %s: %w", capability.displayPath(), ErrInvalidInput)
		}
		if info.Size() > maxReadableFileBytes {
			return fmt.Errorf("file is too large to open (%d bytes, limit %d): %w", info.Size(), maxReadableFileBytes, ErrInvalidInput)
		}
		if f.readFileAfterStat != nil {
			if hookErr := f.readFileAfterStat(); hookErr != nil {
				return hookErr
			}
		}
		var readErr error
		data, readErr = io.ReadAll(io.LimitReader(file, maxReadableFileBytes+1))
		closeErr := file.Close()
		closed = true
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if int64(len(data)) > maxReadableFileBytes {
			data = nil
			return fmt.Errorf("file is too large to open (limit %d bytes): %w", maxReadableFileBytes, ErrInvalidInput)
		}
		return nil
	})
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
	capability, err := f.acquireCapability(path, true)
	if err != nil {
		return err
	}
	defer capability.releaseCapability()
	displayPath := capability.displayPath()
	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	if err := f.runRootOperationHook("WriteFile"); err != nil {
		return err
	}
	if err := capability.withCurrent(func() error { return f.writeCapabilityFile(capability, content, nil) }); err != nil {
		return err
	}
	f.emitFileSaved(displayPath)
	return nil
}

// CreateFile creates an empty file.
// prompt-6 Task 4: requires a workspace root.
func (f *FileService) CreateFile(path string) error {
	capability, err := f.acquireCapability(path, true)
	if err != nil {
		return err
	}
	defer capability.releaseCapability()
	if err := f.runRootOperationHook("CreateFile"); err != nil {
		return err
	}
	return capability.withCurrent(func() error {
		resolved, resolveErr := capability.resolvedRelative(true)
		if resolveErr != nil {
			return resolveErr
		}
		file, createErr := capability.root.root.Create(resolved)
		if createErr != nil {
			return createErr
		}
		return file.Close()
	})
}

// CreateDirectory creates a directory and any necessary parents.
// prompt-6 Task 4: requires a workspace root.
func (f *FileService) CreateDirectory(path string) error {
	capability, err := f.acquireCapability(path, true)
	if err != nil {
		return err
	}
	defer capability.releaseCapability()
	if err := f.runRootOperationHook("CreateDirectory"); err != nil {
		return err
	}
	return capability.withCurrent(func() error {
		resolved, resolveErr := capability.resolvedRelative(true)
		if resolveErr != nil {
			return resolveErr
		}
		return capability.root.root.MkdirAll(resolved, 0755)
	})
}

// DeletePath removes a file or directory recursively.
// prompt-6 Task 4: requires a workspace root.
func (f *FileService) DeletePath(path string) error {
	capability, err := f.acquireCapability(path, true)
	if err != nil {
		return err
	}
	defer capability.releaseCapability()
	if err := f.runRootOperationHook("DeletePath"); err != nil {
		return err
	}
	return capability.withCurrent(func() error { return capability.root.root.RemoveAll(capability.relative) })
}

// RenamePath moves or renames a file or directory.
// prompt-6 Task 4: requires a workspace root.
func (f *FileService) RenamePath(oldPath, newPath string) error {
	oldCapability, err := f.acquireCapability(oldPath, true)
	if err != nil {
		return err
	}
	defer oldCapability.releaseCapability()
	newCapability, err := f.acquireCapability(newPath, true)
	if err != nil {
		return err
	}
	defer newCapability.releaseCapability()
	if oldCapability.workspace != newCapability.workspace || oldCapability.root != newCapability.root {
		return fmt.Errorf("cross-root rename is not allowed: %w", ErrNotAllowed)
	}
	if err := f.runRootOperationHook("RenamePath"); err != nil {
		return err
	}
	return oldCapability.withCurrent(func() error {
		oldRelative, resolveErr := oldCapability.resolvedRelative(false)
		if resolveErr != nil {
			return resolveErr
		}
		newRelative, resolveErr := newCapability.resolvedRelative(false)
		if resolveErr != nil {
			return resolveErr
		}
		return oldCapability.root.root.Rename(oldRelative, newRelative)
	})
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
	capability, err := f.acquireCapability(rootPath, false)
	if err != nil {
		return nil, err
	}
	defer capability.releaseCapability()
	if err := f.runRootOperationHook("ListAllFiles"); err != nil {
		return nil, err
	}
	var result []string
	err = capability.withCurrent(func() error {
		resolved, resolveErr := capability.resolvedRelative(true)
		if resolveErr != nil {
			return resolveErr
		}
		info, statErr := capability.root.root.Stat(resolved)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() {
			return fmt.Errorf("path is not a directory: %s", capability.displayPath())
		}
		patterns := f.loadGitignorePatternsCapability(capability, resolved)
		return fs.WalkDir(capability.root.root.FS(), resolved, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if path == resolved {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() && quickOpenIgnoreDirs[name] {
				return fs.SkipDir
			}
			rel, relErr := filepath.Rel(filepath.FromSlash(resolved), filepath.FromSlash(path))
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if matchGitignore(rel, d.IsDir(), patterns) {
				if d.IsDir() {
					return fs.SkipDir
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
//
// loadGitignorePatterns is retained for direct cache tests. Renderer-facing
// ListAllFiles exclusively uses loadGitignorePatternsCapability.
func (f *FileService) loadGitignorePatterns(dir string) []gitignorePattern {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil
	}
	defer root.Close()
	info, err := root.Stat(".gitignore")
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
		data, rerr := root.ReadFile(".gitignore")
		if rerr == nil {
			patterns = parseGitignoreContent(string(data))
		}
	}
	f.gitignoreCache[dir] = gitignoreCacheEntry{patterns: patterns, mtime: mtime}
	return patterns
}

func (f *FileService) loadGitignorePatternsCapability(capability fileCapability, resolved string) []gitignorePattern {
	gitPath := cleanRootRelative(filepath.Join(filepath.FromSlash(resolved), ".gitignore"))
	info, err := capability.root.root.Stat(gitPath)
	var mtime time.Time
	if err == nil {
		mtime = info.ModTime()
	}
	cacheKey := fmt.Sprintf("%s:%d:%s", capability.root.absolute, capability.workspace.generation, capability.relative)
	f.gitignoreMu.Lock()
	defer f.gitignoreMu.Unlock()
	if f.gitignoreCache == nil {
		f.gitignoreCache = make(map[string]gitignoreCacheEntry)
	}
	if entry, ok := f.gitignoreCache[cacheKey]; ok && entry.mtime.Equal(mtime) {
		return entry.patterns
	}
	var patterns []gitignorePattern
	if err == nil {
		if data, readErr := capability.root.root.ReadFile(gitPath); readErr == nil {
			patterns = parseGitignoreContent(string(data))
		}
	}
	f.gitignoreCache[cacheKey] = gitignoreCacheEntry{patterns: patterns, mtime: mtime}
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
	capability, err := f.acquireCapability(path, false)
	if err != nil {
		return err
	}
	defer capability.releaseCapability()
	if err := f.runRootOperationHook("RevealInOS"); err != nil {
		return err
	}
	var cmd *exec.Cmd
	var startReveal func(*exec.Cmd) error
	err = capability.withCurrent(func() error {
		resolved, resolveErr := capability.resolvedRelative(true)
		if resolveErr != nil {
			return resolveErr
		}
		info, statErr := capability.root.root.Stat(resolved)
		if statErr != nil {
			return statErr
		}
		// U-H1-REVEAL: explorer APIs accept only a pathname, not an os.Root
		// handle. This action performs no file mutation or content read; the path
		// is generated only after Root.Stat and while the workspace lease is held.
		displayPath := capability.displayPath()
		switch runtime.GOOS {
		case "windows":
			cmd = exec.Command("explorer.exe", "/select,", displayPath)
		case "darwin":
			if info.IsDir() {
				cmd = exec.Command("open", displayPath)
			} else {
				cmd = exec.Command("open", "-R", displayPath)
			}
		default:
			dir := displayPath
			if !info.IsDir() {
				dir = filepath.Dir(displayPath)
			}
			cmd = exec.Command("xdg-open", dir)
		}
		startReveal = f.startReveal
		if startReveal == nil {
			startReveal = func(command *exec.Cmd) error { return command.Start() }
		}
		return startReveal(cmd)
	})
	if err != nil {
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

func (f *FileService) runRootOperationHook(operation string) error {
	f.mu.Lock()
	hook := f.rootOperationHook
	f.mu.Unlock()
	if hook != nil {
		return hook(operation)
	}
	return nil
}
