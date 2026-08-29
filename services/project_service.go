package services

import (
	crypto_rand "crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/adrg/xdg"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:templates
var templateFS embed.FS

func generateProjectID() string {
	b := make([]byte, 8)
	_, _ = crypto_rand.Read(b)
	return hex.EncodeToString(b)
}

// Project represents a recently-opened project folder.
//
// Priority 4 (prompt-1.md): 多根工作区支持。
//   - Path：主根路径（向后兼容）。多根模式下为 roots[0]。
//   - Roots：全部根路径列表。单根项目时为空或仅含 Path；多根项目时含全部根。
//   - IsWorkspace：标记此项目是否来源于 .code-workspace 文件。
//
// F-9 (prompt-2.md 第 537-586 行): Remote 字段标识此项目为远程项目。
//   - Remote 为 nil 时表示本地项目（默认）。
//   - Remote 非 nil 时表示项目通过 SSH 挂载，Path 字段保存远程路径。
type Project struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Path  string   `json:"path"`
	Roots []string `json:"roots,omitempty"`
	// IsWorkspace 标记此项目来源于 .code-workspace 文件（Priority 4）。
	IsWorkspace bool  `json:"isWorkspace,omitempty"`
	CreatedAt   int64 `json:"createdAt"`
	LastOpened  int64 `json:"lastOpened"`
	// Exists reports whether the project directory still exists on disk.
	// This is computed on-the-fly by GetRecentProjects and is not persisted.
	Exists bool `json:"exists"`
	// F-9: 远程项目配置。为 nil 表示本地项目；非 nil 表示 SSH 远程项目。
	// 使用指针 + omitempty 以保持向后兼容（本地项目序列化时不含此字段）。
	Remote *RemoteConfig `json:"remote,omitempty"`
	// RemoteOnly reports that this entry names a path on a remote host and
	// therefore cannot be opened as a local workspace (GOAL-P0-07A).
	//
	// Computed by GetRecentProjects, never persisted. It exists so the UI can
	// distinguish "the directory is missing" (Exists=false) from "this is a
	// remote entry and local open is not a supported operation", which are two
	// different things that Exists alone conflates.
	RemoteOnly bool `json:"remoteOnly,omitempty"`
}

// ProjectService manages the list of recent projects, persisted as JSON.
type ProjectService struct {
	workspaceMu           sync.Mutex
	configPath            string
	fileService           *FileService
	terminalService       *TerminalService
	agentService          agentServiceRootSetter
	aiService             *AIService
	gitService            *GitService
	searchService         *SearchService
	toolchainService      *ToolchainService
	lspService            *LSPService
	symbolIndexService    *SymbolIndexService
	coverageService       *CoverageService
	eslintService         *EslintService
	mcpService            MCPServiceRootSetter
	recoveryService       *RecoveryService
	pprofService          *PProfService
	mcpWorkspaceRoot      string
	app                   *application.App
	activeProject         Project
	workspaceSnapshotSink func(WorkspaceSnapshot)
	// beforeSave is an unexported deterministic fault hook for package tests.
	// Production leaves it nil.
	beforeSave func([]Project) error
	// beforeWorkspaceSetters is an unexported deterministic ordering hook used
	// by package tests immediately before the first workspace setter. Production
	// leaves it nil.
	beforeWorkspaceSetters func()
	// GOAL-P0-02: 共享 workspace identity。参与 OpenProject 的两阶段提交，
	// 因此所有持有它的服务（Plan/Goal/Diff/executor）与 FileService 等
	// 一起原子切换，不会出现 A / B 混合状态。
	wsCtx *WorkspaceContext
}

// NewProjectService creates a ProjectService that stores data in the
// OS-specific config directory (via XDG). If fileService is non-nil, it
// sets the workspace root for path sandboxing when a project is added.
// If terminalService is non-nil, the terminal workspace root is also set.
// If agentService is non-nil, the agent workspace root (command sandbox)
// is also set (N-1). If aiService is non-nil, the AI project root is set
// for project-level preset lookups (N-17).
// If gitService is non-nil (N-67), the git workspace root is set.
// If searchService is non-nil (N-67), the search workspace root is set.
func NewProjectService(fs *FileService, ts *TerminalService, as agentServiceRootSetter, ais *AIService) *ProjectService {
	return &ProjectService{
		configPath:      filepath.Join(xdg.ConfigHome, "koyori-ide", "projects.json"),
		fileService:     fs,
		terminalService: ts,
		agentService:    as,
		aiService:       ais,
	}
}

// setGitService links the GitService so its workspace root is updated when
// a project is added (N-67). Called from main.go after construction.
//
//wails:ignore
func (p *ProjectService) setGitService(g *GitService) {
	p.gitService = g
}

// setSearchService links the SearchService so its workspace root is updated
// when a project is added (N-67). Called from main.go after construction.
//
//wails:ignore
func (p *ProjectService) setSearchService(s *SearchService) {
	p.searchService = s
}

// setPProfService links the PProfService so profile outputs are sandboxed to
// the current workspace root (P19 P1-02). Called from main.go after
// construction.
//
//wails:ignore
func (p *ProjectService) setPProfService(s *PProfService) {
	p.pprofService = s
}

// setLSPService links the LSPService so its workspace root is updated when
// a project is added (G-FEAT-02). Called from main.go after construction.
//
//wails:ignore
func (p *ProjectService) setLSPService(l *LSPService) {
	p.lspService = l
}

// setToolchainService links the ToolchainService so its workspace root is
// updated when a project is added (G-FEAT-03). Called from main.go after
// construction.
//
//wails:ignore
func (p *ProjectService) setToolchainService(t *ToolchainService) {
	p.toolchainService = t
}

// setSymbolIndexService links the SymbolIndexService so its workspace root
// is updated when a project is added (G-COMP-01). Called from main.go after
// construction.
//
//wails:ignore
func (p *ProjectService) setSymbolIndexService(s *SymbolIndexService) {
	p.symbolIndexService = s
}

// setCoverageService links CoverageService to the coordinated workspace switch.
//
//wails:ignore
func (p *ProjectService) setCoverageService(service *CoverageService) {
	p.coverageService = service
}

// setEslintService links EslintService to the coordinated workspace switch.
//
//wails:ignore
func (p *ProjectService) setEslintService(service *EslintService) {
	p.eslintService = service
}

// setWorkspaceContext links the shared workspace identity (GOAL-P0-02).
// Once linked, OpenProject / RemoveProject update the context inside the same
// two-phase commit as every other service, so no holder can observe workspace A
// while another observes B.
//
//wails:ignore
func (p *ProjectService) setWorkspaceContext(ctx *WorkspaceContext) {
	p.wsCtx = ctx
}

//wails:ignore
func (p *ProjectService) setRecoveryService(recovery *RecoveryService) {
	p.recoveryService = recovery
}

//wails:ignore
func (p *ProjectService) setMCPService(s MCPServiceRootSetter) {
	p.mcpService = s
	if reader, ok := s.(interface{ WorkspaceRoot() string }); ok {
		p.mcpWorkspaceRoot = reader.WorkspaceRoot()
	}
}

// setApp links the application instance so ProjectService can emit events
// (e.g. "project:removed" when a project is deleted). Called from main.go
// after the app is created.
//
//wails:ignore
func (p *ProjectService) setApp(app *application.App) {
	p.app = app
}

func (p *ProjectService) load() ([]Project, error) {
	data, err := os.ReadFile(p.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []Project{}, nil
		}
		return nil, err
	}
	var projects []Project
	if err := json.Unmarshal(data, &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

func (p *ProjectService) save(projects []Project) error {
	if p.beforeSave != nil {
		if err := p.beforeSave(projects); err != nil {
			return err
		}
	}
	// M-5: atomic write (temp+rename+0600) prevents half-written state.
	return atomicWriteJSON(p.configPath, projects, 0600)
}

// GetRecentProjects returns all saved projects, most-recently-opened first.
// The Exists field is populated on-the-fly by checking os.Stat on each path.
func (p *ProjectService) GetRecentProjects() ([]Project, error) {
	projects, err := p.load()
	if err != nil {
		return nil, err
	}
	for i := range projects {
		// GOAL-P0-07A: a remote entry's Path names a directory on the remote host.
		// Stat-ing it locally is meaningless and actively misleading: when a
		// same-named directory happens to exist locally, Exists=true tells the UI
		// the remote project is ready to open, and opening it edits local files.
		// Remote entries are reported as not locally openable instead.
		if projects[i].Remote != nil {
			projects[i].Exists = false
			projects[i].RemoteOnly = true
			continue
		}
		info, statErr := os.Stat(projects[i].Path)
		projects[i].Exists = statErr == nil && info.IsDir()
	}
	sortProjectsByRecency(projects)
	return projects, nil
}

// rejectRemoteProjectPath refuses a path recorded as a remote project.
//
// GOAL-P0-07A: Remote is an SSH/SFTP file-and-command surface, not a workspace
// host. There is no remote PTY, no remote LSP, no remote git, and no remote
// debugger, so there is no honest way to "open" a remote project in the local
// IDE. Failing loudly is the only correct behaviour; silently resolving the
// remote path against the local disk is the defect this closes.
func (p *ProjectService) rejectRemoteProjectPath(path string) error {
	projects, err := p.load()
	if err != nil {
		// A load failure must not become an implicit "not remote" pass. Callers
		// hit the same load in Phase 2 and surface the error there.
		return nil //nolint:nilerr // Phase 2 reports the load failure.
	}
	for _, proj := range projects {
		if proj.Remote == nil || proj.Path != path {
			continue
		}
		return fmt.Errorf(
			"project %q is a remote (SSH/SFTP) entry and cannot be opened as a local workspace; "+
				"Remote provides file transfer and restricted command execution only, "+
				"with no remote terminal, language server, git, or debugger: %w",
			proj.Name, ErrNotAllowed,
		)
	}
	return nil
}

// AddProject records a project by path. If the path already exists, its
// LastOpened timestamp is updated and no duplicate is created.
// If a FileService is linked, the workspace root is set for path sandboxing.
// N-67: GitService and SearchService workspace roots are also set.
//
// M-8: Uses a rollback pattern — if any SetWorkspaceRoot call fails (or
// if loading/saving the project list fails), all previously-applied
// workspace roots are restored to their previous values, preventing
// partial state across services.
func (p *ProjectService) beginAgentWorkspaceAuthority() *agentWorkspaceAuthorityGuard {
	if p == nil || p.agentService == nil {
		return nil
	}
	return p.agentService.beginProjectWorkspaceAuthority()
}

func (p *ProjectService) AddProject(path string) (Project, error) {
	p.workspaceMu.Lock()
	defer p.workspaceMu.Unlock()
	authority := p.beginAgentWorkspaceAuthority()
	defer authority.release()
	project, err := p.addProject(path, authority)
	if err != nil {
		return Project{}, err
	}
	p.activeProject = project
	p.publishWorkspaceSnapshotLocked()
	return project, nil
}

func (p *ProjectService) addProject(path string, authority *agentWorkspaceAuthorityGuard) (Project, error) {
	if path == "" {
		return Project{}, fmt.Errorf("workspace root is required: %w", ErrInvalidInput)
	}
	if p.recoveryService != nil {
		if err := p.recoveryService.requireResolvedBeforeWorkspaceChange(path); err != nil {
			return Project{}, err
		}
	}

	// GOAL-P0-07A: refuse to dispatch a remote project into the local IDE chain.
	//
	// Project.Remote is never written by current code, but it is still
	// deserialized from projects.json, so a legacy or hand-edited entry can carry
	// it. Every consumer below (FileService, terminal, LSP, git, search) resolves
	// the path against the local disk. Without this guard, a remote path like
	// /home/user/project silently opens whatever happens to live at that path on
	// the local machine — the user edits the wrong files believing they are
	// remote. Checked before Phase 1 so no service root is mutated first.
	if err := p.rejectRemoteProjectPath(path); err != nil {
		return Project{}, err
	}

	setters := p.buildWorkspaceRootSetters(authority)
	if p.beforeWorkspaceSetters != nil {
		p.beforeWorkspaceSetters()
	}

	// Phase 1: apply workspace root to all services with rollback on failure.
	prevRoots := make([]string, len(setters))
	changes := make([]workspaceRootChange, len(setters))
	rollback := func(count int) error {
		var rollbackErrs []error
		if count > len(changes) {
			count = len(changes)
		}
		for index := count - 1; index >= 0; index-- {
			if changes[index] == nil {
				continue
			}
			if err := changes[index].rollback(); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback workspace setter %d: %w", index, err))
			}
		}
		return p.poisonAgentWorkspaceRollback(errors.Join(rollbackErrs...))
	}
	for i, s := range setters {
		prevRoots[i] = s.current()
		change, err := s.apply(path, prevRoots[i])
		changes[i] = change
		if err != nil {
			// Include the setter that returned the error: several adapters can
			// mutate first and report a post-publication failure.
			return Project{}, errors.Join(err, rollback(i+1))
		}
	}
	// Dynamic Agent tools depend on the roots owned by both Agent/Skills and
	// MCP. Their mutation callbacks are deferred while the authority guard is
	// held; flush once every setter agrees on the candidate so a refresh failure
	// can still roll the whole workspace transaction back.
	if err := authority.flushCatalog(); err != nil {
		return Project{}, errors.Join(fmt.Errorf("refresh Agent catalog: %w", err), rollback(len(changes)))
	}

	// Phase 2: load, update, and save the project list. If this fails,
	// rollback all workspace roots to their previous values.
	rollbackAll := func() error { return rollback(len(changes)) }
	commitAll := func() {
		for index := range changes {
			if changes[index] == nil {
				continue
			}
			changes[index].commit()
		}
	}

	projects, err := p.load()
	if err != nil {
		return Project{}, errors.Join(err, rollbackAll())
	}
	now := time.Now().UnixMilli()
	for i, proj := range projects {
		if proj.Path == path {
			projects[i].LastOpened = now
			if err := p.save(projects); err != nil {
				return Project{}, errors.Join(err, rollbackAll())
			}
			commitAll()
			return projects[i], nil
		}
	}
	proj := Project{
		ID:         generateProjectID(),
		Name:       filepath.Base(path),
		Path:       path,
		CreatedAt:  now,
		LastOpened: now,
	}
	projects = append(projects, proj)
	if err := p.save(projects); err != nil {
		return Project{}, errors.Join(err, rollbackAll())
	}
	commitAll()
	return proj, nil
}

// workspaceRootSetter pairs a SetWorkspaceRoot-style function with a
// getter that returns the current root. Used by AddProject for rollback
// support (M-8).
type workspaceRootSetter struct {
	set     func(string) error
	current func() string
	restore func(string) error
	begin   func(string) (workspaceRootChange, error)
}

type workspaceRootChangeFuncs struct {
	commitFn   func()
	rollbackFn func() error
}

func (c *workspaceRootChangeFuncs) commit() {
	if c == nil || c.commitFn == nil {
		return
	}
	c.commitFn()
}

func (c *workspaceRootChangeFuncs) rollback() error {
	if c == nil || c.rollbackFn == nil {
		return nil
	}
	return c.rollbackFn()
}

func (s workspaceRootSetter) apply(root, previous string) (workspaceRootChange, error) {
	if s.begin != nil {
		return s.begin(root)
	}
	if s.set == nil {
		return nil, fmt.Errorf("workspace root setter is unavailable: %w", ErrNotAllowed)
	}
	err := s.set(root)
	// A setter is allowed to report an error after mutating. The compensating
	// closure therefore exists even when apply returned an error.
	return &workspaceRootChangeFuncs{rollbackFn: func() error {
		return restoreWorkspaceRootSetter(s, previous)
	}}, err
}

func restoreWorkspaceRootSetter(setter workspaceRootSetter, root string) error {
	if setter.restore != nil {
		return setter.restore(root)
	}
	return setter.set(root)
}

// buildWorkspaceRootSetters assembles the ordered list of workspace-root
// setters for all linked services. Error-returning services are listed
// first so that a validation failure aborts before non-error services
// are touched. Non-error services (AIService, LSPService,
// SymbolIndexService) are wrapped to always return nil.
func (p *ProjectService) buildWorkspaceRootSetters(authorities ...*agentWorkspaceAuthorityGuard) []workspaceRootSetter {
	var authority *agentWorkspaceAuthorityGuard
	if len(authorities) > 0 {
		authority = authorities[0]
	}
	var setters []workspaceRootSetter

	// GOAL-P0-02: the shared workspace context goes first. It canonicalizes the
	// root and bumps the generation, so a malformed path aborts the switch before
	// any service is touched, and every later holder reads one consistent value.
	if p.wsCtx != nil {
		ctx := p.wsCtx
		// Captured before any setter runs, so it is the pre-switch generation.
		// Rollback restores this exact value instead of bumping again: a switch
		// that aborted never took effect, and advancing the generation would
		// revoke capabilities that stayed valid for the unchanged workspace.
		priorSnapshot := ctx.State()
		setters = append(setters, workspaceRootSetter{
			set:     ctx.Set,
			current: ctx.Root,
			restore: func(root string) error {
				ctx.restoreSnapshot(priorSnapshot)
				return nil
			},
		})
	}

	// Error-returning services (validated first).
	if p.fileService != nil {
		fs := p.fileService
		setters = append(setters, workspaceRootSetter{
			set:     fs.setWorkspaceRoot,
			current: func() string { fs.mu.Lock(); defer fs.mu.Unlock(); return fs.rootDir },
		})
	}
	if p.terminalService != nil {
		ts := p.terminalService
		setters = append(setters, workspaceRootSetter{
			set:     ts.setWorkspaceRoot,
			current: func() string { ts.mu.Lock(); defer ts.mu.Unlock(); return ts.rootDir },
		})
	}
	if p.agentService != nil {
		as := p.agentService
		setters = append(setters, workspaceRootSetter{
			set:     as.setWorkspaceRoot,
			current: as.currentWorkspaceRoot,
			restore: as.restoreWorkspaceRoot,
			begin: func(root string) (workspaceRootChange, error) {
				return as.beginWorkspaceRootTransitionWithinAuthority(root, authority)
			},
		})
	}
	if p.gitService != nil {
		gs := p.gitService
		setters = append(setters, workspaceRootSetter{
			set:     gs.setWorkspaceRoot,
			current: func() string { gs.mu.RLock(); defer gs.mu.RUnlock(); return gs.workspaceRoot },
		})
	}
	if p.searchService != nil {
		ss := p.searchService
		setters = append(setters, workspaceRootSetter{
			set:     ss.setWorkspaceRoot,
			current: func() string { ss.mu.RLock(); defer ss.mu.RUnlock(); return ss.workspaceRoot },
		})
	}
	if p.toolchainService != nil {
		ts := p.toolchainService
		setters = append(setters, workspaceRootSetter{
			set:     ts.setWorkspaceRoot,
			current: func() string { ts.mu.Lock(); defer ts.mu.Unlock(); return ts.workspaceRoot },
		})
	}
	if p.coverageService != nil {
		coverage := p.coverageService
		setters = append(setters, workspaceRootSetter{
			set:     coverage.setWorkspaceRoot,
			current: func() string { coverage.mu.RLock(); defer coverage.mu.RUnlock(); return coverage.workspaceRoot },
		})
	}
	if p.eslintService != nil {
		eslint := p.eslintService
		setters = append(setters, workspaceRootSetter{
			set:     func(root string) error { eslint.setWorkspaceRoot(root); return nil },
			current: func() string { eslint.mu.Lock(); defer eslint.mu.Unlock(); return eslint.workspaceRoot },
		})
	}
	if p.mcpService != nil {
		setters = append(setters, workspaceRootSetter{
			set:     p.setMCPWorkspaceRoot,
			current: p.currentMCPWorkspaceRoot,
			restore: p.restoreMCPWorkspaceRoot,
		})
	}

	// Non-error services (applied last, wrapped to return nil).
	if p.aiService != nil {
		ais := p.aiService
		setters = append(setters, workspaceRootSetter{
			set:     func(s string) error { ais.setProjectRoot(s); return nil },
			current: func() string { ais.mu.RLock(); defer ais.mu.RUnlock(); return ais.projectRoot },
		})
	}
	if p.lspService != nil {
		ls := p.lspService
		setters = append(setters, workspaceRootSetter{
			set:     func(s string) error { ls.setWorkspaceRoot(s); return nil },
			current: func() string { ls.mu.Lock(); defer ls.mu.Unlock(); return ls.workspaceRoot },
		})
	}
	if p.symbolIndexService != nil {
		sis := p.symbolIndexService
		setters = append(setters, workspaceRootSetter{
			set:     func(s string) error { sis.setWorkspaceRoot(s); return nil },
			current: func() string { sis.mu.RLock(); defer sis.mu.RUnlock(); return sis.workspaceRoot },
		})
	}
	if p.pprofService != nil {
		ps := p.pprofService
		setters = append(setters, workspaceRootSetter{
			set:     func(root string) error { ps.setWorkspaceRoot(root); return nil },
			current: ps.currentWorkspaceRoot,
		})
	}

	return setters
}

func (p *ProjectService) setMCPWorkspaceRoot(root string) error {
	if root == "" {
		return fmt.Errorf("MCP workspace root is required: %w", ErrInvalidInput)
	}
	if err := p.mcpService.setWorkspaceRoot(root); err != nil {
		return err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve MCP workspace root: %w", err)
	}
	p.mcpWorkspaceRoot = filepath.Clean(abs)
	return nil
}

func (p *ProjectService) currentMCPWorkspaceRoot() string {
	if reader, ok := p.mcpService.(interface{ WorkspaceRoot() string }); ok {
		return reader.WorkspaceRoot()
	}
	return p.mcpWorkspaceRoot
}

func (p *ProjectService) restoreMCPWorkspaceRoot(root string) error {
	if root != "" {
		return p.setMCPWorkspaceRoot(root)
	}
	restorer, ok := p.mcpService.(interface{ restoreWorkspaceRoot(string) error })
	if !ok {
		return fmt.Errorf("MCP workspace root cannot be restored: %w", ErrInvalidInput)
	}
	if err := restorer.restoreWorkspaceRoot(""); err != nil {
		return err
	}
	p.mcpWorkspaceRoot = ""
	return nil
}

// isValidProjectID checks that an ID is a valid hex string with optional hyphens.
// This prevents path traversal attacks through the ID (which is used in filenames).
func isValidProjectID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || c == '-') {
			return false
		}
	}
	return true
}

// RemoveProject deletes a project from the recent list by ID.
// Emits a "project:removed" event so the frontend can close open files,
// clear the current project, and refresh the project list.
func (p *ProjectService) RemoveProject(id string) error {
	p.workspaceMu.Lock()
	defer p.workspaceMu.Unlock()
	authority := p.beginAgentWorkspaceAuthority()
	defer authority.release()
	before := WorkspaceSnapshot{}
	if p.wsCtx != nil {
		before = p.wsCtx.State()
	}
	if err := p.removeProject(id, authority); err != nil {
		return err
	}
	after := WorkspaceSnapshot{}
	if p.wsCtx != nil {
		after = p.wsCtx.State()
	}
	if after.Generation != before.Generation {
		p.activeProject = Project{}
		p.publishWorkspaceSnapshotLocked()
	}
	return nil
}

func (p *ProjectService) removeProject(id string, authority *agentWorkspaceAuthorityGuard) error {
	if !isValidProjectID(id) {
		return fmt.Errorf("invalid project ID: %s", id)
	}
	projects, err := p.load()
	if err != nil {
		return err
	}
	for i, proj := range projects {
		if proj.ID == id {
			activeWorkspace := p.projectMatchesActiveWorkspace(proj)
			if p.recoveryService != nil && activeWorkspace {
				if err := p.recoveryService.requireResolved("remove active workspace"); err != nil {
					return err
				}
			}
			projects = append(projects[:i], projects[i+1:]...)
			// GOAL-P0-02: if the removed project is the active workspace, drop
			// the shared context. Leaving it pointed at a removed path would let
			// Plan / Goal / Diff keep creating snapshots for a workspace the user
			// just deleted. Clearing bumps the generation, so capabilities and
			// executors bound to it stop being accepted; the consumers are
			// fail-closed on an empty root, so this does not widen access.
			var clearTransition *workspaceClearTransition
			if activeWorkspace {
				clearTransition, err = p.beginWorkspaceRootClearWithinAuthority(authority)
				if err != nil {
					return fmt.Errorf("clear active workspace roots: %w", err)
				}
				if err := authority.flushCatalog(); err != nil {
					return errors.Join(
						fmt.Errorf("refresh Agent catalog after workspace clear: %w", err),
						clearTransition.rollback(),
					)
				}
			}
			if err := p.save(projects); err != nil {
				if clearTransition != nil {
					return errors.Join(err, clearTransition.rollback())
				}
				return err
			}
			if clearTransition != nil {
				clearTransition.commit()
			}
			// Emit project:removed so the frontend cleans up (closes open
			// files under this path, clears currentProject if it matches).
			if p.app != nil {
				p.app.Event.Emit("project:removed", map[string]string{
					"id":   proj.ID,
					"path": proj.Path,
					"name": proj.Name,
				})
			}
			return nil
		}
	}
	return fmt.Errorf("project not found: %s", id)
}

// isSameWorkspacePath reports whether two paths denote the same workspace.
// Both sides are canonicalized because the context stores a cleaned absolute
// path while the persisted project record keeps whatever the user supplied.
func (p *ProjectService) isSameWorkspacePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	return sameWorkspaceIdentityPath(absA, absB)
}

func sortProjectsByRecency(projects []Project) {
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].LastOpened > projects[j].LastOpened
	})
}

// ============================================================================
// G-FEAT-01: New Project scaffolding wizard.
//
// The wizard generates Go/TypeScript/JavaScript/Monorepo/Fullstack/HTML/Vue/uni-app projects
// from embedded templates (services/templates/*). Template variables are
// strictly validated before rendering so that user-supplied module/project
// names cannot inject content into go.mod / package.json or escape the target
// directory.
// ============================================================================

// ProjectTemplate describes a scaffolding template the wizard can generate.
type ProjectTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Language    string `json:"language"`
}

// CreateProjectRequest is the payload for CreateProject.
type CreateProjectRequest struct {
	TemplateID  string `json:"templateId"`
	ProjectName string `json:"projectName"`
	TargetDir   string `json:"targetDir"`
	ModuleName  string `json:"moduleName"` // for Go: module path
}

// projectTemplateData is the data passed to text/template when rendering.
type projectTemplateData struct {
	ProjectName string
	ModuleName  string
	TemplateID  string
}

// moduleAndProjectNamePattern restricts template inputs to a safe character
// set. Go module paths allow letters, digits, and the punctuation ".", "-",
// "_", "/". Project/package names used in package.json are tighter (no "/").
// Crucially this rejects shell metacharacters (";", spaces, quotes, "|", "&",
// "<", ">", "$", backticks, backslashes, "*") so values like
// `"; rm -rf /"` cannot be rendered into go.mod / package.json.
var (
	moduleNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// builtInTemplates is the static catalog returned by ListProjectTemplates.
// The IDs map to subdirectories under services/templates/.
var builtInTemplates = []ProjectTemplate{
	{
		ID:          "go",
		Name:        "Go Service",
		Description: "HTTP server with Makefile, golangci-lint config, Dockerfile, and CI.",
		Language:    "Go",
	},
	{
		ID:          "typescript",
		Name:        "TypeScript Project",
		Description: "Strict tsconfig, ESLint flat config, Vitest, and an entry point.",
		Language:    "TypeScript",
	},
	{
		ID:          "javascript",
		Name:        "JavaScript Project",
		Description: "ESM JavaScript project with ESLint and Vitest.",
		Language:    "JavaScript",
	},
	{
		ID:          "monorepo",
		Name:        "Monorepo",
		Description: "pnpm workspace with a web app and a shared package.",
		Language:    "TypeScript",
	},
	{
		ID:          "fullstack",
		Name:        "Fullstack",
		Description: "Go backend API and a Vue/Vite frontend in one repo.",
		Language:    "Go + TypeScript",
	},
	{
		ID:          "html",
		Name:        "HTML Site",
		Description: "Accessible HTML page with a stylesheet and JavaScript entry point.",
		Language:    "HTML",
	},
	{
		ID:          "vue",
		Name:        "Vue App",
		Description: "Vue 3 and Vite starter with TypeScript and a minimal app shell.",
		Language:    "Vue + TypeScript",
	},
	{
		ID:          "uniapp",
		Name:        "uni-app",
		Description: "Cross-platform uni-app starter with a Vue page and manifest.",
		Language:    "uni-app + Vue",
	},
}

// ListProjectTemplates returns the available project templates for the wizard.
func (s *ProjectService) ListProjectTemplates() []ProjectTemplate {
	out := make([]ProjectTemplate, len(builtInTemplates))
	copy(out, builtInTemplates)
	return out
}

// CreateProject generates a new project from the named template into
// TargetDir. It validates the template ID, sanitizes/validates the project
// and module names, ensures the target directory is safe, walks the embedded
// template tree, renders each .tmpl file with text/template, and writes the
// results to disk. The created project path is returned.
func (s *ProjectService) CreateProject(req CreateProjectRequest) (string, error) {
	if !isValidTemplateID(req.TemplateID) {
		return "", fmt.Errorf("invalid template ID: %q", req.TemplateID)
	}
	name, err := sanitizeProjectName(req.ProjectName)
	if err != nil {
		return "", err
	}
	moduleName, err := sanitizeModuleName(req.ModuleName, req.TemplateID)
	if err != nil {
		return "", err
	}
	targetDir, err := resolveTargetDir(req.TargetDir, name)
	if err != nil {
		return "", err
	}
	data := projectTemplateData{
		ProjectName: name,
		ModuleName:  moduleName,
		TemplateID:  req.TemplateID,
	}
	if err := renderTemplateTree(req.TemplateID, targetDir, data); err != nil {
		return "", err
	}
	return targetDir, nil
}

// isValidTemplateID checks that the ID is one of the known template IDs.
// This prevents path traversal through the ID (which is joined into the
// embed.FS path).
func isValidTemplateID(id string) bool {
	for _, t := range builtInTemplates {
		if t.ID == id {
			return true
		}
	}
	return false
}

// sanitizeProjectName validates that name contains only safe characters for
// use as a package name / directory name. Empty names are rejected.
func sanitizeProjectName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("project name is required")
	}
	if len(name) > 214 {
		return "", fmt.Errorf("project name too long (max 214 chars)")
	}
	if !projectNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid project name %q: only letters, digits, '.', '-', '_' allowed (must start alphanumeric)", name)
	}
	return name, nil
}

// sanitizeModuleName validates the Go module path. For non-Go templates the
// module name is unused and may be empty; otherwise it must match the module
// path pattern. The moduleNamePattern rejects shell metacharacters so values
// like `"; rm -rf /"` cannot be injected into go.mod.
func sanitizeModuleName(moduleName, templateID string) (string, error) {
	if templateID != "go" && templateID != "fullstack" {
		return "", nil
	}
	moduleName = strings.TrimSpace(moduleName)
	if moduleName == "" {
		return "", fmt.Errorf("module name is required for Go templates")
	}
	if len(moduleName) > 512 {
		return "", fmt.Errorf("module name too long")
	}
	if !moduleNamePattern.MatchString(moduleName) {
		return "", fmt.Errorf("invalid module name %q: only letters, digits, '.', '-', '_', '/' allowed (must start alphanumeric)", moduleName)
	}
	return moduleName, nil
}

// resolveTargetDir resolves and validates the target directory. If targetDir
// is empty, the project is created under the OS temp dir. The resolved path
// must not already exist (to avoid clobbering), and is checked for traversal
// safety via IsRelativePathSafe on the project name component.
func resolveTargetDir(targetDir, projectName string) (string, error) {
	targetDir = strings.TrimSpace(targetDir)
	if targetDir == "" {
		targetDir = os.TempDir()
	}
	abs, err := filepath.Abs(targetDir)
	if err != nil {
		return "", fmt.Errorf("resolve target directory: %w", err)
	}
	// The final project directory is <targetDir>/<projectName>. Validate the
	// name component lexically so a malicious name cannot escape via "..".
	if !IsRelativePathSafe(projectName) {
		return "", fmt.Errorf("unsafe project name for directory: %q", projectName)
	}
	finalDir := filepath.Join(abs, projectName)
	// Refuse to overwrite an existing non-empty directory.
	if info, err := os.Stat(finalDir); err == nil && info.IsDir() {
		entries, _ := os.ReadDir(finalDir)
		if len(entries) > 0 {
			return "", fmt.Errorf("target directory already exists and is not empty: %s", finalDir)
		}
	}
	return finalDir, nil
}

// renderTemplateTree walks the embedded templates/<templateID>/ subtree and
// renders every file (stripping the .tmpl suffix) into targetDir.
func renderTemplateTree(templateID, targetDir string, data projectTemplateData) error {
	root := "templates/" + templateID
	// Verify the template directory exists in the embed.FS.
	if _, err := templateFS.ReadDir(root); err != nil {
		return fmt.Errorf("template %q not found in embed.FS: %w", templateID, err)
	}
	return fs.WalkDir(templateFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("compute relative path for %s: %w", path, err)
		}
		// Convert embed path separators (always '/') to OS separators.
		relOS := filepath.FromSlash(rel)
		// Strip the .tmpl suffix to produce the output filename.
		outName := strings.TrimSuffix(relOS, ".tmpl")
		if outName == "" {
			return fmt.Errorf("template file %s has empty output name", path)
		}
		outPath := filepath.Join(targetDir, outName)
		// Ensure the parent directory exists.
		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			return fmt.Errorf("create directory for %s: %w", outPath, err)
		}
		raw, err := templateFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded template %s: %w", path, err)
		}
		// Parse and execute the template. text/template does not auto-escape,
		// but the inputs are pre-validated to a safe charset, so injection
		// into go.mod / package.json is prevented at the validation layer.
		tmpl, err := template.New(path).Parse(string(raw))
		if err != nil {
			return fmt.Errorf("parse template %s: %w", path, err)
		}
		var buf strings.Builder
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("execute template %s: %w", path, err)
		}
		if err := os.WriteFile(outPath, []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
		return nil
	})
}

// ============================================================================
// Priority 4 (prompt-1.md): 多根工作区 Workspace Folders。
//
// 支持 .code-workspace 文件（VS Code 多根工作区文件，JSON 格式）：
//   {
//     "folders": [
//       { "path": "frontend" },
//       { "path": "backend", "name": "API" }
//     ],
//     "settings": { ... }
//   }
//
// path 字段支持：
//   - 绝对路径（POSIX 与 Windows）
//   - 相对于 .code-workspace 文件所在目录的相对路径
//   - URI 形式（file://...）会被规范化为本地路径
// ============================================================================

// codeWorkspaceFolder 是 .code-workspace 文件中的单个 folder 条目。
type codeWorkspaceFolder struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
	URI  string `json:"uri,omitempty"`
}

// codeWorkspaceFile 是 .code-workspace 文件的 JSON 结构（仅解析需要的字段）。
type codeWorkspaceFile struct {
	Folders []codeWorkspaceFolder `json:"folders"`
}

// IsCodeWorkspaceFile 判断给定路径是否以 .code-workspace 扩展名结尾
// （大小写不敏感，匹配 VS Code 行为）。
func IsCodeWorkspaceFile(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".code-workspace")
}

// ParseCodeWorkspaceFile 读取 .code-workspace 文件并返回解析出的所有
// folder 路径（已规范化为绝对路径）。空 path 或非 .code-workspace 文件
// 返回错误。每个 folder 的 path 字段若是相对路径，则以 .code-workspace
// 文件所在目录为基准解析。
//
// 解析后返回的路径顺序与文件中 folders 数组顺序一致；空路径与重复路径
// 会被过滤掉。
func ParseCodeWorkspaceFile(filePath string) ([]string, error) {
	if filePath == "" {
		return nil, fmt.Errorf("code-workspace file path is empty")
	}
	if !IsCodeWorkspaceFile(filePath) {
		return nil, fmt.Errorf("not a .code-workspace file: %s", filePath)
	}
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute path for %s: %w", filePath, err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read code-workspace file %s: %w", absPath, err)
	}
	var ws codeWorkspaceFile
	if err := json.Unmarshal(data, &ws); err != nil {
		return nil, fmt.Errorf("parse code-workspace JSON %s: %w", absPath, err)
	}
	baseDir := filepath.Dir(absPath)
	out := make([]string, 0, len(ws.Folders))
	seen := make(map[string]bool, len(ws.Folders))
	for i, f := range ws.Folders {
		// 优先使用 path 字段；若缺失则尝试 URI 字段（部分工具使用 uri 而非 path）。
		raw := f.Path
		if raw == "" && f.URI != "" {
			var uriErr error
			raw, uriErr = uriToLocalPath(f.URI)
			if uriErr != nil {
				return nil, fmt.Errorf("code-workspace folder[%d] URI %q: %w", i, f.URI, uriErr)
			}
		}
		if raw == "" {
			return nil, fmt.Errorf("code-workspace folder[%d]: missing path/uri", i)
		}
		abs, rerr := resolveCodeWorkspaceFolder(raw, baseDir)
		if rerr != nil {
			return nil, fmt.Errorf("code-workspace folder[%d] path %q: %w", i, raw, rerr)
		}
		identity := workspacePathIdentity(abs)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		out = append(out, abs)
	}
	return out, nil
}

// resolveCodeWorkspaceFolder 解析单个 folder 条目的 path 字段到绝对路径。
//   - 绝对路径：直接返回（已 ToSlash 规范化后 FromSlash）。
//   - 相对路径：以 baseDir 为基准解析。
//   - file:// URI：剥离协议前缀后转为本地路径。
func resolveCodeWorkspaceFolder(path, baseDir string) (string, error) {
	if strings.HasPrefix(strings.ToLower(path), "file://") {
		var err error
		path, err = uriToLocalPath(path)
		if err != nil {
			return "", err
		}
	}
	if isWindowsDeviceWorkspacePath(path) {
		return "", fmt.Errorf("Windows device namespace is not a workspace path")
	}
	if isIncompleteUNCWorkspacePath(path) {
		return "", fmt.Errorf("incomplete UNC workspace path: %s", path)
	}
	if isDriveRelativeWorkspacePath(path) {
		return "", fmt.Errorf("unsupported drive-relative workspace path: %s", path)
	}
	if isWindowsAbsoluteWorkspacePath(path) && runtime.GOOS != "windows" {
		return "", fmt.Errorf("Windows absolute workspace path is incompatible with this host: %s", path)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// uriToLocalPath 将 file:// URI 转换为本地文件路径。
// 跨平台兼容：file:///C:/... → C:/...（Windows）。
func uriToLocalPath(uri string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(uri), "file://") {
		return uri, nil
	}
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("invalid file URI: %w", err)
	}
	if u.Scheme == "" || !strings.EqualFold(u.Scheme, "file") {
		return "", fmt.Errorf("unsupported URI scheme %q", u.Scheme)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("file URI must not contain userinfo, query, or fragment")
	}

	// Decode each URI path segment independently. Encoded separators and NUL
	// would change the path structure after authorization, so reject them.
	escapedPath := u.EscapedPath()
	pathParts := make([]string, 0)
	for _, escapedPart := range strings.Split(escapedPath, "/") {
		part, unescapeErr := url.PathUnescape(escapedPart)
		if unescapeErr != nil {
			return "", fmt.Errorf("invalid file URI path escape: %w", unescapeErr)
		}
		if strings.ContainsAny(part, "/\\\x00") {
			return "", fmt.Errorf("file URI contains an encoded path separator or NUL")
		}
		pathParts = append(pathParts, part)
	}
	path := strings.Join(pathParts, "/")

	authority := u.Host
	if authority != "" && !strings.EqualFold(authority, "localhost") {
		if strings.ContainsAny(authority, "/\\\x00") {
			return "", fmt.Errorf("invalid file URI authority")
		}
		// A file URI authority is a UNC server name, not a URI host with
		// ports, userinfo, or device aliases. Requiring a real server and
		// share prevents file://server and file://./GLOBALROOT from being
		// reinterpreted as local/device paths later.
		if authority == "." || authority == ".." {
			return "", fmt.Errorf("invalid file URI authority")
		}
		if len(authority) == 2 && authority[1] == ':' && isASCIIAlpha(authority[0]) {
			return filepath.FromSlash(authority + ensureLeadingSlash(path)), nil
		}
		if strings.Contains(authority, ":") {
			return "", fmt.Errorf("invalid file URI authority")
		}
		shareParts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		// The authority supplies the server; the first URI path segment is the
		// UNC share. A child folder is optional, so file://server/share is a
		// valid root while file://server and file://server/ remain incomplete.
		if len(shareParts) < 1 || shareParts[0] == "" {
			return "", fmt.Errorf("file URI UNC authority requires a server and share")
		}
		return filepath.FromSlash("//" + authority + ensureLeadingSlash(path)), nil
	}
	// file:///C:/... and file://localhost/C:/... → C:/...
	if len(path) >= 3 && path[0] == '/' && isASCIIAlpha(path[1]) && path[2] == ':' {
		path = path[1:]
	}
	if path == "" {
		path = "/"
	}
	return filepath.FromSlash(path), nil
}

func ensureLeadingSlash(path string) string {
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}

func isASCIIAlpha(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z')
}

func isDriveRelativeWorkspacePath(path string) bool {
	if len(path) >= 2 && isASCIIAlpha(path[0]) && path[1] == ':' {
		return len(path) == 2 || (len(path) > 2 && path[2] != '\\' && path[2] != '/')
	}
	return len(path) > 0 && path[0] == '\\' && (len(path) == 1 || path[1] != '\\')
}

// isWindowsDeviceWorkspacePath rejects Windows device/extended namespaces.
// They are not ordinary workspace folders and must never be accepted through
// a URI or a host with different path semantics.
func isWindowsDeviceWorkspacePath(path string) bool {
	normalized := strings.ReplaceAll(path, "\\", "/")
	return strings.HasPrefix(normalized, "//?/") ||
		strings.HasPrefix(normalized, "//./")
}

// isWindowsAbsoluteWorkspacePath recognizes Windows absolute syntax even when
// filepath.IsAbs is evaluating it on a non-Windows host. This prevents a
// foreign absolute path from being silently joined to the local workspace.
func isWindowsAbsoluteWorkspacePath(path string) bool {
	normalized := strings.ReplaceAll(path, "\\", "/")
	if len(normalized) >= 3 && isASCIIAlpha(normalized[0]) && normalized[1] == ':' && normalized[2] == '/' {
		return true
	}
	if strings.HasPrefix(normalized, "//") {
		parts := strings.Split(strings.TrimPrefix(normalized, "//"), "/")
		return len(parts) >= 2 && parts[0] != "" && parts[1] != ""
	}
	return false
}

func isIncompleteUNCWorkspacePath(path string) bool {
	normalized := strings.ReplaceAll(path, "\\", "/")
	if !strings.HasPrefix(normalized, "//") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(normalized, "//"), "/")
	return len(parts) < 2 || parts[0] == "" || parts[1] == ""
}

func workspacePathIdentity(path string) string {
	clean := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}

// AddMultiRootProject 添加一个多根工作区项目（Priority 4）。
// roots 为全部根目录列表；workspacePath 为 .code-workspace 文件路径
// （可空，表示非 .code-workspace 项目但具有多根）。
//
// 行为：
//   - 若 roots 仅含一个元素且 workspacePath 为空，等价于 AddProject(roots[0])。
//   - 否则把所有根传播给已链接的 FileService / LSPService / TerminalService 等
//     （通过 SetWorkspaceRoots 调用，回退到 SetWorkspaceRoot 当服务不支持多根）。
//   - 主根 Path = roots[0]；Roots 字段保留全部根；IsWorkspace 在 workspacePath
//     非空时为 true。
//
// M-8 兼容：使用与 AddProject 相同的回滚机制，任何 setter 失败都回滚到旧根。
func (p *ProjectService) AddMultiRootProject(roots []string, workspacePath string) (Project, error) {
	p.workspaceMu.Lock()
	defer p.workspaceMu.Unlock()
	authority := p.beginAgentWorkspaceAuthority()
	defer authority.release()
	project, err := p.addMultiRootProject(roots, workspacePath, authority)
	if err != nil {
		return Project{}, err
	}
	p.activeProject = project
	p.publishWorkspaceSnapshotLocked()
	return project, nil
}

func (p *ProjectService) addMultiRootProject(roots []string, workspacePath string, authority *agentWorkspaceAuthorityGuard) (Project, error) {
	// A workspace file is the authority for its roots. Renderer-supplied roots
	// are accepted only when they exactly match the parsed file; an empty list
	// asks the backend to parse it directly.
	if workspacePath != "" {
		absWorkspacePath, err := filepath.Abs(workspacePath)
		if err != nil {
			return Project{}, fmt.Errorf("resolve code-workspace path: %w", err)
		}
		workspacePath = filepath.Clean(absWorkspacePath)
		parsedRoots, err := ParseCodeWorkspaceFile(workspacePath)
		if err != nil {
			return Project{}, err
		}
		if len(roots) == 0 {
			roots = parsedRoots
		} else {
			requested, err := canonicalizeExistingWorkspaceRoots(roots)
			if err != nil {
				return Project{}, err
			}
			parsed, err := canonicalizeExistingWorkspaceRoots(parsedRoots)
			if err != nil {
				return Project{}, err
			}
			if !stringSlicesEqual(requested, parsed) {
				return Project{}, fmt.Errorf("workspace roots do not match %s: %w", workspacePath, ErrInvalidInput)
			}
			roots = parsed
		}
	}
	cleaned, err := canonicalizeExistingWorkspaceRoots(roots)
	if err != nil {
		return Project{}, err
	}
	if p.recoveryService != nil && len(cleaned) > 0 {
		if err := p.recoveryService.requireResolvedBeforeWorkspaceChange(cleaned[0]); err != nil {
			return Project{}, err
		}
	}
	// Phase 1：把多根传播给已链接服务（带回滚）。
	if p.beforeWorkspaceSetters != nil {
		p.beforeWorkspaceSetters()
	}
	prevRoots, err := p.applyMultiRoots(cleaned, authority)
	if err != nil {
		return Project{}, err
	}
	rollback := func() error { return p.rollbackMultiRoots(prevRoots) }
	if err := authority.flushCatalog(); err != nil {
		return Project{}, errors.Join(fmt.Errorf("refresh Agent catalog: %w", err), rollback())
	}
	commit := func() {
		for index := range prevRoots {
			if prevRoots[index].change == nil {
				continue
			}
			prevRoots[index].change.commit()
		}
	}

	// Phase 2：加载、更新、保存项目列表。
	projects, err := p.load()
	if err != nil {
		return Project{}, errors.Join(err, rollback())
	}
	now := time.Now().UnixMilli()
	// 若 .code-workspace 路径已存在则更新；否则按 roots[0] 作为查重键。
	dedupKey := workspacePath
	if dedupKey == "" {
		dedupKey = cleaned[0]
	}
	for i, proj := range projects {
		if proj.Path == dedupKey || (workspacePath == "" && proj.Path == cleaned[0]) {
			projects[i].LastOpened = now
			projects[i].Roots = cleaned
			projects[i].IsWorkspace = workspacePath != ""
			if workspacePath != "" {
				projects[i].Path = workspacePath
			}
			if err := p.save(projects); err != nil {
				return Project{}, errors.Join(err, rollback())
			}
			commit()
			return projects[i], nil
		}
	}
	name := filepath.Base(cleaned[0])
	if workspacePath != "" {
		name = strings.TrimSuffix(filepath.Base(workspacePath), ".code-workspace")
	}
	proj := Project{
		ID:          generateProjectID(),
		Name:        name,
		Path:        cleaned[0],
		Roots:       cleaned,
		IsWorkspace: workspacePath != "",
		CreatedAt:   now,
		LastOpened:  now,
	}
	if workspacePath != "" {
		proj.Path = workspacePath
	}
	projects = append(projects, proj)
	if err := p.save(projects); err != nil {
		return Project{}, errors.Join(err, rollback())
	}
	commit()
	return proj, nil
}

// multiRootSnapshot 记录一个服务在多根应用前的状态，用于回滚。
type multiRootSnapshot struct {
	// kind 标识服务类型，决定回滚时调用 SetWorkspaceRoot 还是 SetWorkspaceRoots。
	kind string
	// singleRoot 为单根服务的旧根。
	singleRoot string
	// multiRoots 为多根服务的旧根列表（仅 kind=multi 时有效）。
	multiRoots []string
	// setterRoots 用于回滚（单根）。
	setSingle func(string) error
	// beginSingle is the transactional form used by AgentService. When present,
	// it keeps lifecycle authority reversible until the project transaction
	// commits.
	beginSingle func(string) (workspaceRootChange, error)
	change      workspaceRootChange
	// restoreSingle restores state when rollback requires an internal-only path.
	restoreSingle func(string) error
	// setterRoots 用于回滚（多根）。
	setMulti func([]string) error
}

// applyMultiRoots 把 roots 应用到所有已链接服务（带快照供回滚）。
// 返回每个服务的快照；调用方在失败时调用 rollbackMultiRoots。
func (p *ProjectService) applyMultiRoots(roots []string, authority *agentWorkspaceAuthorityGuard) ([]multiRootSnapshot, error) {
	var snaps []multiRootSnapshot
	// The shared context remains a primary-root identity, but participates in
	// the same rollback as every multi-root service. Its exact generation is
	// restored if a later phase fails.
	if p.wsCtx != nil {
		ctx := p.wsCtx
		previous := ctx.State()
		snap := multiRootSnapshot{
			kind:       "single",
			singleRoot: previous.Root,
			setSingle: func(string) error {
				return ctx.SetRoots(roots)
			},
			restoreSingle: func(string) error {
				ctx.restoreSnapshot(previous)
				return nil
			},
		}
		snaps = append(snaps, snap)
		if err := ctx.SetRoots(roots); err != nil {
			return nil, errors.Join(err, p.rollbackMultiRoots(snaps))
		}
	}

	// FileService：支持多根。
	if p.fileService != nil {
		fs := p.fileService
		prev := fs.WorkspaceRoots()
		snaps = append(snaps, multiRootSnapshot{
			kind:       "multi",
			multiRoots: prev,
			setMulti:   fs.setWorkspaceRoots,
		})
		if err := fs.setWorkspaceRoots(roots); err != nil {
			return nil, errors.Join(err, p.rollbackMultiRoots(snaps))
		}
	}
	// LSPService：支持多根。
	if p.lspService != nil {
		ls := p.lspService
		prev := ls.WorkspaceRoots()
		snaps = append(snaps, multiRootSnapshot{
			kind:       "multi",
			multiRoots: prev,
			// LSPService.SetWorkspaceRoots 不返回 error，包装成返回 nil。
			setMulti: func(rs []string) error {
				ls.setWorkspaceRoots(rs)
				return nil
			},
		})
		ls.setWorkspaceRoots(roots)
	}
	if p.searchService != nil {
		ss := p.searchService
		prev := ss.WorkspaceRoots()
		snaps = append(snaps, multiRootSnapshot{
			kind:       "multi",
			multiRoots: prev,
			setMulti:   ss.setWorkspaceRoots,
		})
		if err := ss.setWorkspaceRoots(roots); err != nil {
			return nil, errors.Join(err, p.rollbackMultiRoots(snaps))
		}
	}
	// GitService: repository operations may target any configured root.
	if p.gitService != nil {
		gs := p.gitService
		gs.mu.RLock()
		previousRoot := gs.workspaceRoot
		previousRoots := append([]string(nil), gs.workspaceRoots...)
		gs.mu.RUnlock()
		previous := append([]string(nil), previousRoots...)
		if len(previous) == 0 && previousRoot != "" {
			previous = []string{previousRoot}
		}
		snaps = append(snaps, multiRootSnapshot{
			kind:       "multi",
			multiRoots: previous,
			setMulti:   gs.setWorkspaceRoots,
		})
		if err := gs.setWorkspaceRoots(roots); err != nil {
			return nil, errors.Join(err, p.rollbackMultiRoots(snaps))
		}
	}
	if p.symbolIndexService != nil {
		sis := p.symbolIndexService
		prev := sis.WorkspaceRoots()
		snaps = append(snaps, multiRootSnapshot{
			kind:       "multi",
			multiRoots: prev,
			setMulti:   sis.setWorkspaceRoots,
		})
		if err := sis.setWorkspaceRoots(roots); err != nil {
			return nil, errors.Join(err, p.rollbackMultiRoots(snaps))
		}
	}
	// 不支持多根的服务：用 roots[0] 作为单根回退。
	singleSetters, err := p.buildSingleRootSetters(roots[0], snaps, authority)
	if err != nil {
		return nil, err
	}
	for _, s := range singleSetters {
		snaps = append(snaps, s)
	}
	return snaps, nil
}

// buildSingleRootSetters 为不支持多根的服务构造单根快照并应用 roots[0]。
// 已应用的 snaps 用于跳过 FileService / LSPService（这两者已用多根设置）。
func (p *ProjectService) buildSingleRootSetters(primary string, applied []multiRootSnapshot, authority *agentWorkspaceAuthorityGuard) ([]multiRootSnapshot, error) {
	var snaps []multiRootSnapshot
	apply := func(snap multiRootSnapshot) error {
		snaps = append(snaps, snap)
		if snap.beginSingle != nil {
			change, err := snap.beginSingle(primary)
			snaps[len(snaps)-1].change = change
			if err != nil {
				// The Agent transactional begin contract restores its own state on
				// failure. Re-running restoreSingle here would recursively acquire
				// the Project-owned workspace write guard and deadlock. Roll back
				// only adapters that completed before this transaction.
				rollbackTargets := make([]multiRootSnapshot, 0, len(applied)+len(snaps)-1)
				rollbackTargets = append(rollbackTargets, applied...)
				rollbackTargets = append(rollbackTargets, snaps[:len(snaps)-1]...)
				return errors.Join(err, p.rollbackMultiRoots(rollbackTargets))
			}
			return nil
		}
		if snap.setSingle != nil {
			if err := snap.setSingle(primary); err != nil {
				rollbackTargets := make([]multiRootSnapshot, 0, len(applied)+len(snaps))
				rollbackTargets = append(rollbackTargets, applied...)
				rollbackTargets = append(rollbackTargets, snaps...)
				return errors.Join(err, p.rollbackMultiRoots(rollbackTargets))
			}
		}
		return nil
	}
	if p.terminalService != nil {
		ts := p.terminalService
		ts.mu.Lock()
		prev := ts.rootDir
		ts.mu.Unlock()
		if err := apply(multiRootSnapshot{
			kind:       "single",
			singleRoot: prev,
			setSingle:  ts.setWorkspaceRoot,
		}); err != nil {
			return nil, err
		}
	}
	if p.agentService != nil {
		as := p.agentService
		if err := apply(multiRootSnapshot{
			kind:          "single",
			singleRoot:    as.currentWorkspaceRoot(),
			setSingle:     as.setWorkspaceRoot,
			restoreSingle: as.restoreWorkspaceRoot,
			beginSingle: func(root string) (workspaceRootChange, error) {
				return as.beginWorkspaceRootTransitionWithinAuthority(root, authority)
			},
		}); err != nil {
			return nil, err
		}
	}
	if p.toolchainService != nil {
		ts := p.toolchainService
		ts.mu.Lock()
		prev := ts.workspaceRoot
		ts.mu.Unlock()
		if err := apply(multiRootSnapshot{
			kind:       "single",
			singleRoot: prev,
			setSingle:  ts.setWorkspaceRoot,
		}); err != nil {
			return nil, err
		}
	}
	if p.mcpService != nil {
		if err := apply(multiRootSnapshot{
			kind:          "single",
			singleRoot:    p.currentMCPWorkspaceRoot(),
			setSingle:     p.setMCPWorkspaceRoot,
			restoreSingle: p.restoreMCPWorkspaceRoot,
		}); err != nil {
			return nil, err
		}
	}
	if p.aiService != nil {
		ais := p.aiService
		ais.mu.RLock()
		prev := ais.projectRoot
		ais.mu.RUnlock()
		// AIService 用 SetProjectRoot 而非 SetWorkspaceRoot；包装成单根 setter。
		if err := apply(multiRootSnapshot{
			kind:       "single",
			singleRoot: prev,
			setSingle: func(s string) error {
				ais.setProjectRoot(s)
				return nil
			},
		}); err != nil {
			return nil, err
		}
	}
	return snaps, nil
}

// rollbackMultiRoots 把所有服务恢复到应用多根前的状态，并暴露任何
// compensating failure。静默吞掉恢复错误会把服务留在混合 workspace。
func (p *ProjectService) rollbackMultiRoots(snaps []multiRootSnapshot) error {
	var rollbackErrs []error
	for i := len(snaps) - 1; i >= 0; i-- {
		s := snaps[i]
		if s.change != nil {
			if err := s.change.rollback(); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback %s workspace root: %w", s.kind, err))
			}
			continue
		}
		switch s.kind {
		case "multi":
			if s.setMulti != nil {
				if err := s.setMulti(s.multiRoots); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback multi workspace roots: %w", err))
				}
			}
		default:
			if s.restoreSingle != nil {
				if err := s.restoreSingle(s.singleRoot); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback single workspace root: %w", err))
				}
			} else if s.setSingle != nil {
				if err := s.setSingle(s.singleRoot); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback single workspace root: %w", err))
				}
			}
		}
	}
	return p.poisonAgentWorkspaceRollback(errors.Join(rollbackErrs...))
}

func (p *ProjectService) poisonAgentWorkspaceRollback(rollbackErr error) error {
	if rollbackErr == nil || p == nil || p.agentService == nil {
		return rollbackErr
	}
	return p.agentService.poisonWorkspaceAuthorityAfterRollback(rollbackErr)
}
