package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
	"github.com/adrg/xdg"
	"github.com/google/shlex"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// RiskLevel classifies the potential impact of an agent command (N-1).
type RiskLevel string

const (
	RiskSafe      RiskLevel = "safe"
	RiskElevated  RiskLevel = "elevated"
	RiskDangerous RiskLevel = "dangerous"
)

// denyPattern pairs a regex with a human-readable description for the
// block reason shown in the approval UI.
type denyPattern struct {
	desc string
	re   *regexp.Regexp
}

// dangerousPatterns are regex patterns for commands that are always
// blocked by ExecCommand. The denylist is intentionally conservative —
// it targets only unambiguously destructive operations. The risk level
// classification (elevatedPatterns) handles the broader "use with
// caution" category.
//
// G-SEC-02: denylist 非安全边界，仅作辅助过滤 (denylist is not a
// security boundary, only auxiliary filtering). Determined obfuscation
// (shell escaping, variables, pipes) can bypass these patterns. The
// primary protection is always mandatory user approval — no command is
// auto-approved, including those classified as "Safe".
var dangerousPatterns = []denyPattern{
	{"rm -rf (recursive force delete)", regexp.MustCompile(`(?i)\brm\s+(-\S*r\S*f\S*|-\S*f\S*r\S*)`)},
	{"rm targeting root, home, or wildcard", regexp.MustCompile(`(?i)\brm\s+(-\S+\s+)*[/~*](\s|$)`)},
	{"del /s /f /q (Windows destructive delete)", regexp.MustCompile(`(?i)\bdel\s+/(s|f|q)`)},
	{"format (disk format)", regexp.MustCompile(`(?i)(^|[\n;&|]\s*|\bsudo\s+)format\s+\S`)},
	{"mkfs (filesystem creation)", regexp.MustCompile(`(?i)\bmkfs\b`)},
	{"fork bomb", regexp.MustCompile(`:\s*\(\)\s*\{`)},
	{"shutdown / reboot / halt", regexp.MustCompile(`(?i)\b(shutdown|reboot|halt)\b`)},
	{"dd to raw device", regexp.MustCompile(`(?i)\bdd\b.*\bof=/dev/`)},
	{":(){ :|:& };: fork bomb literal", regexp.MustCompile(`:\s*\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;`)},
}

// elevatedPatterns are regex patterns for commands that modify system
// state and warrant an "elevated" risk badge in the approval UI.
var elevatedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bsudo\b`),
	regexp.MustCompile(`(?i)\bcurl\b[^\|]*\|\s*(sh|bash|zsh)\b`),
	regexp.MustCompile(`(?i)\bwget\b[^\|]*\|\s*(sh|bash|zsh)\b`),
	regexp.MustCompile(`(?i)\bnpm\s+install\b`),
	regexp.MustCompile(`(?i)\bpip\s+install\b`),
	regexp.MustCompile(`(?i)\bapt(-get)?\s+(install|remove|purge)\b`),
	regexp.MustCompile(`(?i)\bbrew\s+(install|uninstall)\b`),
	regexp.MustCompile(`(?i)\bchmod\b`),
	regexp.MustCompile(`(?i)\bchown\b`),
}

// AgentService provides tool-execution primitives for Agent mode (#11).
// ExecCommand requires a workspace root and sandboxes the working directory
// to that root, rejects commands in the denylist,
// classifies the risk level, and writes each execution to an audit log
// (N-1).
type AgentService struct {
	mu                          sync.Mutex
	rootDir                     string
	workspaceContext            *WorkspaceContext
	beforeWorkspaceCommandStart func()
	commandTimeout              time.Duration
	auditMu                     sync.Mutex
	auditLog                    *os.File
	auditLogger                 *slog.Logger
	auditRoot                   *os.Root
	auditRootOwned              bool
	auditName                   string
	auditIdentity               os.FileInfo
	// Plan 11 Task 4 Step 6: MCP service for mcp.<server>.<tool> tool calls.
	// When set, CheckCommand recognizes the mcp.* namespace and applies
	// ClassifyMCPToolRisk instead of the shell-command patterns. CallMCPTool
	// dispatches to MCPService.CallTool after approval.
	mcpService *MCPService
	// Plan 11 Task 5 Step 7: Skills service. When set, the agent consults
	// SkillsService.MatchTriggers to inject SystemPrompt + AllowedTools
	// into the LLM call. AllowedTools are enforced via CheckCommand
	// (G-SEC-02: tool calls outside the active skills' whitelist are
	// rejected). configureWorkspaceRoot propagates to SkillsService so project-
	// scoped skills (G-SEC-03) load from <root>/.koyori-ide/skills/.
	skillsService *SkillsService
	// M-7: MCP tool list cache (TTL 30s). Avoids fetching the full tool
	// list from all MCP servers on every checkMCPCommand call.
	mcpCacheMu        sync.Mutex
	mcpCachedTools    []AgentMCPTool
	mcpCacheFetchedAt time.Time
	mcpCacheTTL       time.Duration // 0 → default 30s
	// mcpLister overrides mcpService for tool listing when non-nil
	// (test injection). In production this is nil and mcpService is used.
	mcpLister      mcpToolLister
	rootGeneration uint64
	approveCommand func(command, cwd string, risk RiskLevel) bool
	// approveAI is a trusted host callback for workflow AI operations. It is
	// intentionally separate from command approval so a prompt can never be
	// reinterpreted as shell input at the approval boundary.
	approveAI    func(operation string) bool
	approveWrite func(targetPath string, size int64) bool
	// GOAL-P1-02: backend-enforced tool-call budget. See agent_budget.go.
	//
	// Lazily initialized via ensureBudget because AgentService is constructed
	// as a bare struct literal in several tests; a nil budget must not panic.
	budgetInit sync.Once
	budget     *toolBudget
	// P12-G33: the renderer/headless shared execution core. Trusted bootstrap
	// wires handlers; public methods below expose only catalog and capability
	// issue/redeem operations.
	executionMu      sync.RWMutex
	executionRuntime *agentcore.Runtime
	executionInitErr error
	// catalogRefreshMu serializes complete dynamic catalog rebuilds. Without a
	// single publication order, an older workflow/MCP/Skill snapshot can finish
	// after a newer refresh and overwrite the authoritative ToolDef source.
	catalogRefreshMu sync.Mutex
}

// mcpToolLister abstracts MCP tool listing for caching (M-7) and test
// injection. *MCPService satisfies this interface.
type mcpToolLister interface {
	ListAgentMCPTools(ctx context.Context) ([]AgentMCPTool, error)
}

// mcpCacheDefaultTTL is the default TTL for the MCP tool list cache.
const mcpCacheDefaultTTL = 30 * time.Second

// NewAgentService creates a new AgentService. It best-effort opens an
// audit log file in the XDG cache directory; if the file cannot be
// opened, audit logging falls back to slog.Default() (stderr). N-11 will
// introduce a unified slog setup across all services.
func NewAgentService() *AgentService {
	return newAgentService(nil)
}

// NewAgentServiceWithWorkspaceContext creates the renderer-facing agent
// service. Command approvals and execution resolve the active root from the
// shared context instead of trusting a cached setter value.
func NewAgentServiceWithWorkspaceContext(workspaceContext *WorkspaceContext) *AgentService {
	return newAgentService(workspaceContext)
}

func newAgentService(workspaceContext *WorkspaceContext) *AgentService {
	return newAgentServiceWithAuditPath(workspaceContext, filepath.Join(xdg.CacheHome, "koyori-ide", "agent-audit.log"))
}

// newAgentServiceWithAuditPath retains the desktop's best-effort audit setup.
// Trusted headless wiring opens and validates its state-bound handle before it
// reaches the service constructor.
func newAgentServiceWithAuditPath(workspaceContext *WorkspaceContext, auditPath string) *AgentService {
	var auditRoot *os.Root
	var auditFile *os.File
	var auditName string
	// P1-a: audit log contains sensitive command/agent activity - restrict
	// to owner-only (0600) instead of world-readable 0644.
	if strings.TrimSpace(auditPath) != "" {
		if root, f, name, err := openAgentAuditLogPath(auditPath); err == nil {
			auditRoot = root
			auditFile = f
			auditName = name
		}
	}
	return newAgentServiceWithAuditDestination(workspaceContext, auditFile, auditRoot, auditName, true)
}

func openAgentAuditLogPath(auditPath string) (*os.Root, *os.File, string, error) {
	directory := filepath.Dir(auditPath)
	name := filepath.Base(auditPath)
	if name == "." || name == string(filepath.Separator) || strings.TrimSpace(name) == "" {
		return nil, nil, "", fmt.Errorf("audit file name is invalid")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, nil, "", err
	}
	file, err := root.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, nil, "", err
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
			_ = root.Close()
		}
	}()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return nil, nil, "", fmt.Errorf("audit log is not a regular file")
	}
	named, err := root.Lstat(name)
	if err != nil || named.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, named) {
		return nil, nil, "", fmt.Errorf("audit log identity changed")
	}
	multipleLinks, err := agentFileHasMultipleLinks(file)
	if err != nil || multipleLinks {
		return nil, nil, "", fmt.Errorf("audit log has an unsafe link identity")
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, nil, "", err
	}
	valid = true
	return root, file, name, nil
}

func newAgentServiceWithAuditRoot(workspaceContext *WorkspaceContext, auditFile *os.File, auditRoot *os.Root, auditName string) *AgentService {
	return newAgentServiceWithAuditDestination(workspaceContext, auditFile, auditRoot, auditName, false)
}

func newAgentServiceWithAuditDestination(workspaceContext *WorkspaceContext, auditFile *os.File, auditRoot *os.Root, auditName string, auditRootOwned bool) *AgentService {
	svc := &AgentService{workspaceContext: workspaceContext}
	svc.approveCommand = nativeCommandApproval
	svc.approveAI = nativeAIOperationApproval
	svc.approveWrite = nativeWriteApproval
	if auditFile != nil {
		identity, err := auditFile.Stat()
		if err == nil && identity.Mode().IsRegular() {
			svc.auditLog = auditFile
			svc.auditLogger = slog.New(slog.NewTextHandler(auditFile, &slog.HandlerOptions{Level: slog.LevelInfo}))
			svc.auditRoot = auditRoot
			svc.auditRootOwned = auditRootOwned
			svc.auditName = auditName
			svc.auditIdentity = identity
		} else {
			_ = auditFile.Close()
			if auditRootOwned && auditRoot != nil {
				_ = auditRoot.Close()
			}
		}
	}
	if err := svc.initializeExecutionCore(); err != nil {
		svc.executionInitErr = err
		slog.Error("initialize agent execution core", "error", err)
	}
	return svc
}

func nativeCommandApproval(command, cwd string, risk RiskLevel) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Approve command execution").SetMessage(
		fmt.Sprintf("Risk: %s\nWorking directory: %s\n\n%s", risk, cwd, command),
	)
	dialog.AddButton("Yes").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("No").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

// nativeWriteApproval shows a native OS dialog asking the user to approve an
// agent file write. It is the production value of AgentService.approveWrite
// and the only surviving piece of the former write-approval pipeline (P19
// P1-03: token minting/redeeming lives solely in the agentcore Runtime now).
func nativeWriteApproval(targetPath string, size int64) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Approve file write").SetMessage(
		fmt.Sprintf("Agent wants to write:\n%s\n(%d bytes)", targetPath, size),
	)
	dialog.AddButton("Yes").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("No").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

func nativeAIOperationApproval(operation string) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Approve workflow AI operation").SetMessage(
		fmt.Sprintf("Allow the workflow AI operation %q?", operation),
	)
	dialog.AddButton("Yes").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("No").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

// Close releases resources held by the service. N-103: the audit log file
// opened in NewAgentService was never closed, leaking a file descriptor
// for the lifetime of the process. This is called from main on shutdown.
// Safe to call multiple times; subsequent calls are no-ops.
//
//wails:ignore
func (s *AgentService) Close() error {
	var errs []error
	if s != nil {
		deps := executionDependenciesFor(s)
		deps.mu.RLock()
		ai := deps.ai
		deps.mu.RUnlock()
		if ai != nil {
			// The provider worker must publish its terminal usage/lifecycle state
			// before Agent resources are released. A timeout is returned to the
			// caller and is never silently converted into a clean close.
			errs = append(errs, ai.cancelAllStreamsAndWait())
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	if s.auditLog != nil {
		errs = append(errs, s.auditLog.Sync(), s.auditLog.Close())
		s.auditLog = nil
	}
	if s.auditRootOwned && s.auditRoot != nil {
		errs = append(errs, s.auditRoot.Close())
	}
	s.auditLogger = nil
	s.auditRoot = nil
	s.auditRootOwned = false
	s.auditName = ""
	s.auditIdentity = nil
	return errors.Join(errs...)
}

// agentServiceRootSetter is the narrow, package-sealed capability used by
// ProjectService to propagate and roll back the agent workspace root.
type agentServiceRootSetter interface {
	setWorkspaceRoot(string) error
	currentWorkspaceRoot() string
	restoreWorkspaceRoot(string) error
	beginProjectWorkspaceAuthority() *agentWorkspaceAuthorityGuard
	beginWorkspaceRootTransitionWithinAuthority(string, *agentWorkspaceAuthorityGuard) (workspaceRootChange, error)
	beginWorkspaceRootClearTransitionWithinAuthority(*agentWorkspaceAuthorityGuard) (workspaceRootChange, error)
	poisonWorkspaceAuthorityAfterRollback(error) error
}

// workspaceRootChange is the private two-phase hook shared by AgentService
// and ProjectService. It is never a Wails binding.
type workspaceRootChange interface {
	commit()
	rollback() error
}

// agentWorkspaceRootTransition is a trusted, package-private two-phase
// workspace change. ProjectService keeps it open while all other services and
// the project ledger are updated; a failure restores the prior session and
// policy authority while deliberately leaving one-time capabilities burned.
type agentWorkspaceRootTransition struct {
	mu              sync.Mutex
	agent           *AgentService
	previousRoot    string
	previousGen     uint64
	previousOwners  map[string]agentSessionOwner
	previousSkills  map[string]map[string]string
	previousRuntime agentcore.RuntimeSnapshot
	lifecycleReset  *agentLifecycleWorkspaceReset
	authority       *agentWorkspaceAuthorityGuard
	ownsAuthority   bool
	previousSkill   skillsWorkspaceState
	skills          *SkillsService
	done            bool
	result          error
}

func cloneAgentSessionOwners(values map[string]agentSessionOwner) map[string]agentSessionOwner {
	if values == nil {
		return nil
	}
	clone := make(map[string]agentSessionOwner, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneAgentSessionSkills(values map[string]map[string]string) map[string]map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]map[string]string, len(values))
	for sessionID, bindings := range values {
		if bindings == nil {
			clone[sessionID] = nil
			continue
		}
		copied := make(map[string]string, len(bindings))
		for skillID, fingerprint := range bindings {
			copied[skillID] = fingerprint
		}
		clone[sessionID] = copied
	}
	return clone
}

func (s *AgentService) poisonWorkspaceAuthorityAfterRollback(cause error) error {
	if cause == nil {
		return nil
	}
	if s != nil {
		deps := executionDependenciesFor(s)
		deps.mu.RLock()
		lifecycle := deps.lifecycle
		deps.mu.RUnlock()
		if lifecycle != nil {
			lifecycle.poisonWorkspaceAuthority(cause)
		} else {
			s.executionMu.RLock()
			runtime := s.executionRuntime
			s.executionMu.RUnlock()
			if runtime != nil {
				runtime.UnregisterAllSessions()
			}
		}
		deps.mu.Lock()
		deps.sessionOwners = make(map[string]agentSessionOwner)
		deps.sessionSkills = make(map[string]map[string]string)
		deps.mu.Unlock()
	}
	return errors.Join(agentcore.ErrSessionPersistencePoisoned, cause)
}

// configureWorkspaceRoot sets the directory within which agent commands are
// allowed to run. It is reserved for trusted bootstrap code; renderer code
// must not be able to alter this security boundary. An empty root is rejected.
//
// Plan 11 Task 5: propagates the workspace root to SkillsService so that
// project-scoped skills (G-SEC-03) load from <root>/.koyori-ide/skills/. The
// A load failure is returned and the transition restores the previous policy;
// accepting a workspace with a partially published Skill source is unsafe.
//
//wails:ignore
func (s *AgentService) configureWorkspaceRoot(root string) error {
	return s.setWorkspaceRoot(root)
}

func (s *AgentService) setWorkspaceRoot(root string) error {
	transition, err := s.beginWorkspaceRootTransition(root)
	if err != nil {
		return err
	}
	if transition == nil {
		return nil
	}
	transition.commit()
	return nil
}

func validateAgentWorkspaceRoot(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("agent workspace root is required: %w", ErrInvalidInput)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace root is not a directory: %s", abs)
	}
	// P19 CI 修复：与 WorkspaceContext/FileService 安全根一致地规范化（Windows
	// 8.3 短名、macOS /var 符号链接前缀）。否则 agent 侧以原始拼写拼接工具
	// 路径，而安全根是解析后的形态，容器判断与审计脱敏会在 CI 环境失效。
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = filepath.Clean(resolved)
	}
	return abs, nil
}

func (s *AgentService) beginWorkspaceRootTransition(root string) (workspaceRootChange, error) {
	abs, err := validateAgentWorkspaceRoot(root)
	if err != nil {
		return nil, err
	}
	authority := s.beginProjectWorkspaceAuthority()
	change, err := s.beginResolvedWorkspaceRootTransition(abs, authority, true)
	if err != nil {
		authority.release()
	}
	return change, err
}

func (s *AgentService) beginWorkspaceRootClearTransition() (workspaceRootChange, error) {
	authority := s.beginProjectWorkspaceAuthority()
	change, err := s.beginResolvedWorkspaceRootTransition("", authority, true)
	if err != nil {
		authority.release()
	}
	return change, err
}

func (s *AgentService) beginWorkspaceRootTransitionWithinAuthority(root string, authority *agentWorkspaceAuthorityGuard) (workspaceRootChange, error) {
	abs, err := validateAgentWorkspaceRoot(root)
	if err != nil {
		return nil, err
	}
	return s.beginResolvedWorkspaceRootTransition(abs, authority, false)
}

func (s *AgentService) beginWorkspaceRootClearTransitionWithinAuthority(authority *agentWorkspaceAuthorityGuard) (workspaceRootChange, error) {
	return s.beginResolvedWorkspaceRootTransition("", authority, false)
}

func (s *AgentService) beginResolvedWorkspaceRootTransition(abs string, authority *agentWorkspaceAuthorityGuard, ownsAuthority bool) (workspaceRootChange, error) {
	if s == nil {
		return nil, fmt.Errorf("agent service is unavailable: %w", ErrNotAllowed)
	}
	if err := authority.validate(s); err != nil {
		return nil, err
	}
	deps := executionDependenciesFor(s)
	deps.mu.RLock()
	lifecycle := deps.lifecycle
	previousOwners := cloneAgentSessionOwners(deps.sessionOwners)
	previousSkills := cloneAgentSessionSkills(deps.sessionSkills)
	deps.mu.RUnlock()

	s.mu.Lock()
	previousRoot := s.rootDir
	previousGeneration := s.rootGeneration
	sk := s.skillsService
	s.mu.Unlock()
	previousSkill := skillsWorkspaceState{}
	if sk != nil {
		var err error
		previousSkill, err = sk.captureWorkspaceState()
		if err != nil {
			return nil, fmt.Errorf("capture workspace skill policy: %w", err)
		}
	}

	transition := &agentWorkspaceRootTransition{
		agent: s, previousRoot: previousRoot, previousGen: previousGeneration,
		previousOwners: previousOwners, previousSkills: previousSkills,
		authority: authority, ownsAuthority: ownsAuthority,
		previousSkill: previousSkill, skills: sk,
	}
	var err error
	// Publish the candidate root before the durable lifecycle reset. Recovery
	// guards intentionally observe this generation while they decide whether an
	// old unscoped receipt may be disposed. The transaction restores both fields
	// if reset publication is rejected.
	s.mu.Lock()
	s.rootDir = abs
	s.rootGeneration++
	s.mu.Unlock()
	if lifecycle != nil {
		transition.lifecycleReset, err = lifecycle.prepareWorkspaceReset()
		if err == nil && transition.lifecycleReset != nil {
			err = transition.lifecycleReset.publish()
		}
	} else {
		// Bare AgentService fixtures can still have a runtime without a lifecycle.
		// Preserve that authority as well instead of silently burning it.
		s.executionMu.RLock()
		runtime := s.executionRuntime
		s.executionMu.RUnlock()
		if runtime != nil {
			transition.previousRuntime = runtime.CaptureSnapshot()
			runtime.UnregisterAllSessions()
		}
	}
	if err != nil {
		s.mu.Lock()
		s.rootDir = previousRoot
		s.rootGeneration = previousGeneration
		s.mu.Unlock()
		if errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) ||
			errors.Is(err, agentcore.ErrSessionPersistencePoisoned) ||
			errors.Is(err, ErrUsagePersistenceIndeterminate) ||
			errors.Is(err, ErrUsagePersistencePoisoned) {
			deps.mu.Lock()
			deps.sessionOwners = make(map[string]agentSessionOwner)
			deps.sessionSkills = make(map[string]map[string]string)
			deps.mu.Unlock()
		}
		if transition.lifecycleReset != nil {
			transition.lifecycleReset.cancel()
		}
		return nil, err
	}
	// Lifecycle reset has already revoked durable/runtime authority. Clear the
	// adapter-side owner maps only after that publication succeeds.
	deps.mu.Lock()
	deps.sessionSkills = make(map[string]map[string]string)
	deps.sessionOwners = make(map[string]agentSessionOwner)
	deps.mu.Unlock()

	if sk != nil {
		if err := sk.setWorkspaceRoot(abs); err != nil {
			rollbackErr := transition.rollback()
			return nil, errors.Join(fmt.Errorf("set workspace skill source: %w", err), rollbackErr)
		}
		if err := sk.Load(); err != nil {
			rollbackErr := transition.rollback()
			return nil, errors.Join(fmt.Errorf("load workspace skills: %w", err), rollbackErr)
		}
	}
	return transition, nil
}

func (t *agentWorkspaceRootTransition) commit() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	if t.lifecycleReset != nil {
		t.lifecycleReset.commit()
	}
	if t.ownsAuthority {
		t.authority.release()
	}
}

func (t *agentWorkspaceRootTransition) rollback() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return t.result
	}
	var errs []error
	if t.agent != nil {
		t.agent.mu.Lock()
		t.agent.rootDir = t.previousRoot
		t.agent.rootGeneration = t.previousGen
		t.agent.mu.Unlock()
	}
	if t.skills != nil {
		if err := t.skills.restoreWorkspaceState(t.previousSkill); err != nil {
			errs = append(errs, fmt.Errorf("restore workspace skill policy: %w", err))
		}
	}
	deps := executionDependenciesFor(t.agent)
	if t.lifecycleReset != nil {
		if err := t.lifecycleReset.rollback(); err != nil {
			errs = append(errs, errors.Join(agentcore.ErrSessionPersistencePoisoned, err))
		}
	} else {
		t.agent.executionMu.RLock()
		runtime := t.agent.executionRuntime
		t.agent.executionMu.RUnlock()
		if runtime != nil {
			runtime.RestoreSnapshot(t.previousRuntime)
		}
	}
	// Never restore adapter owner maps when durable lifecycle restoration failed.
	if len(errs) == 0 {
		deps.mu.Lock()
		deps.sessionOwners = cloneAgentSessionOwners(t.previousOwners)
		deps.sessionSkills = cloneAgentSessionSkills(t.previousSkills)
		deps.mu.Unlock()
	} else {
		if t.lifecycleReset != nil && t.lifecycleReset.lifecycle != nil {
			t.lifecycleReset.lifecycle.poisonWorkspaceAuthority(errors.Join(errs...))
		} else {
			t.agent.executionMu.RLock()
			runtime := t.agent.executionRuntime
			t.agent.executionMu.RUnlock()
			if runtime != nil {
				runtime.UnregisterAllSessions()
			}
		}
		deps.mu.Lock()
		deps.sessionOwners = make(map[string]agentSessionOwner)
		deps.sessionSkills = make(map[string]map[string]string)
		deps.mu.Unlock()
	}
	t.done = true
	t.result = errors.Join(errs...)
	if t.result != nil {
		// Any incomplete authority restoration is a poisoned lifecycle, not a
		// normal retryable setter error. Expose that state on the first failure so
		// callers cannot mistake an internally revoked session for a clean rollback.
		t.result = errors.Join(agentcore.ErrSessionPersistencePoisoned, t.result)
	}
	if t.ownsAuthority {
		t.authority.release()
	}
	return t.result
}

func (s *AgentService) currentWorkspaceRoot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rootDir
}

// restoreWorkspaceRoot is only used by trusted project rollback. Restoring an
// empty pre-project state remains fail-closed and invalidates existing tokens.
func (s *AgentService) restoreWorkspaceRoot(root string) error {
	if root != "" {
		return s.setWorkspaceRoot(root)
	}
	transition, err := s.beginWorkspaceRootClearTransition()
	if err != nil {
		return err
	}
	if transition == nil {
		return nil
	}
	transition.commit()
	return nil
}

// setMCPService injects the MCP service so the agent can dispatch
// mcp.<server>.<tool> tool calls (Plan 11 Task 4 Step 6). Without this,
// MCP namespaced commands are treated as unknown and blocked.
//
//wails:ignore
func (s *AgentService) setMCPService(mcp *MCPService) {
	s.mu.Lock()
	s.mcpService = mcp
	s.mu.Unlock()
	// M-7: invalidate the tool-list cache when the MCP service changes.
	s.InvalidateMCPCache()
}

// setSkillsService injects the Skills service so the agent can apply
// SystemPrompt overrides + AllowedTools whitelist from active skills
// (Plan 11 Task 5 Step 7). Without this, skill matching is skipped.
//
//wails:ignore
func (s *AgentService) setSkillsService(skills *SkillsService) {
	s.mu.Lock()
	s.skillsService = skills
	s.mu.Unlock()
}

// validateCwd returns the absolute working directory to use for the
// command. If cwd is empty, it defaults to the workspace root. If no root is
// configured, validation fails closed. cwd must be inside the root.
//
// G-SEC-06: validation is delegated to ValidatePathWithinRoot, which
// resolves symlinks on both the target and the root before comparing.
// The previous lexical-only check (filepath.Abs + filepath.Rel) could
// be bypassed by a symlink inside the workspace pointing outside.
func (s *AgentService) validateCwd(cwd string) (string, error) {
	resolved, _, err := s.validateCwdWithGeneration(cwd)
	return resolved, err
}

func (s *AgentService) validateCwdWithGeneration(cwd string) (string, uint64, error) {
	s.mu.Lock()
	root := s.rootDir
	generation := s.rootGeneration
	s.mu.Unlock()
	lease, err := acquireWorkspaceLease(s.workspaceContext, root, generation)
	if err != nil {
		return "", generation, err
	}
	resolved, err := lease.resolve(cwd)
	return resolved, lease.generation, err
}

func (s *AgentService) acquireWorkspaceLease() (workspaceLease, error) {
	s.mu.Lock()
	root := s.rootDir
	generation := s.rootGeneration
	s.mu.Unlock()
	return acquireWorkspaceLease(s.workspaceContext, root, generation)
}

// shellMetachars lists shell metacharacters that are rejected by parseCommand.
// HIGH-03: the agent command executor no longer wraps commands in
// `sh -c` / `cmd /c`. Commands are parsed into a simple argv and executed
// directly via exec.CommandContext. Any shell syntax is rejected because
// the raw string is passed to exec without shell interpretation, and
// allowing shell syntax would create an injection surface.
var shellMetachars = []struct {
	char byte
	desc string
}{
	{'|', "pipe (|) is not supported — run each command separately"},
	{'>', "output redirect (>) is not supported"},
	{'<', "input redirect (<) is not supported"},
	{'&', "background/chaining (&) is not supported"},
	{';', "command separator (;) is not supported — run each command separately"},
	{'`', "command substitution (backtick) is not supported"},
	{'$', "variable expansion ($) is not supported — use the literal value"},
	{'*', "glob wildcard (*) is not supported — use the exact filename"},
	{'?', "glob wildcard (?) is not supported — use the exact filename"},
	{'(', "subshell syntax () is not supported"},
	{')', "subshell syntax () is not supported"},
	{'{', "brace expansion {} is not supported"},
	{'}', "brace expansion {} is not supported"},
	{'\n', "multi-line commands are not supported — run each command separately"},
}

// tildeMetacharDesc 保留 ~ 的拒绝语义（home 目录展开），但由
// rejectTokenLeadingTilde 按位置判定：只有 token 起始处的 ~ 才是 shell
// home 展开语法；路径中段的 ~ 是合法字符（Windows 8.3 短名，例如
// C:\Users\RUNNER~1\...），并且命令不经 shell 直接 exec，mid-token 的
// ~ 对 exec 无任何特殊含义。
const tildeMetacharDesc = "home directory expansion (~) is not supported — use the full path"

// rejectTokenLeadingTilde rejects ~ at the start of a token (after
// whitespace or an opening quote) where a shell would expand it to the
// home directory. Mid-token ~ (Windows 8.3 short names) is allowed.
func rejectTokenLeadingTilde(command string) error {
	for i := 0; i < len(command); i++ {
		if command[i] != '~' {
			continue
		}
		if i == 0 {
			return fmt.Errorf("unsupported shell syntax: %s", tildeMetacharDesc)
		}
		switch command[i-1] {
		case ' ', '\t', '"', '\'':
			return fmt.Errorf("unsupported shell syntax: %s", tildeMetacharDesc)
		}
	}
	return nil
}

// parseCommand splits a command line into an argv slice for direct
// execution (HIGH-03). It first scans the raw string for shell
// metacharacters (pipes, redirects, variable expansion, command
// substitution, command chaining, background execution, glob, brace
// expansion) and rejects them with a descriptive error. If the command
// is clean, it uses github.com/google/shlex to tokenize it into an argv
// slice. The returned argv is passed directly to exec.CommandContext
// without a shell wrapper, eliminating the sh -c / cmd /c injection
// surface.
func parseCommand(command string) ([]string, error) {
	for _, mc := range shellMetachars {
		if strings.IndexByte(command, mc.char) >= 0 {
			return nil, fmt.Errorf("unsupported shell syntax: %s", mc.desc)
		}
	}
	if err := rejectTokenLeadingTilde(command); err != nil {
		return nil, err
	}
	argv, err := shlex.Split(command)
	if err != nil {
		return nil, fmt.Errorf("parse command: %w", err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("command is empty after parsing")
	}
	return argv, nil
}

// CommandCheck is the result of evaluating a command without executing
// it. The frontend calls CheckCommand before ExecCommand to display a
// risk badge and block notice in the agent approval UI (N-1).
type CommandCheck struct {
	RiskLevel   RiskLevel `json:"riskLevel"`
	Blocked     bool      `json:"blocked"`
	BlockReason string    `json:"blockReason,omitempty"`
}

// CheckCommand evaluates a command line and returns its risk level and
// whether it would be blocked by the denylist. It does not execute the
// command.
//
// G-SEC-02: ALL non-empty commands return at minimum RiskElevated. No
// command is classified as "Safe" — every command requires manual user
// approval. The "Safe" level is reserved for the empty-command no-op
// case. This closes the auto-approve bypass that previously allowed
// "Safe" commands to execute without explicit approval.
//
// HIGH-03: commands containing shell metacharacters (pipes, redirects,
// variable expansion, etc.) are blocked with a descriptive reason — the
// executor no longer uses `sh -c` / `cmd /c`, so shell syntax is rejected
// rather than silently passed to a shell.
func (s *AgentService) CheckCommand(command string) CommandCheck {
	if strings.TrimSpace(command) == "" {
		return CommandCheck{RiskLevel: RiskSafe}
	}
	// Plan 11 Task 4 Step 8: mcp.<server>.<tool> namespace is dispatched
	// via MCPService, not the shell executor. Classify via
	// ClassifyMCPToolRisk (G-SEC-02: default RiskElevated, write/exec/network
	// RiskDangerous). The actual call happens in CallMCPTool after user
	// approval.
	if strings.HasPrefix(command, "mcp.") {
		return s.checkMCPCommand(command)
	}
	// HIGH-03: reject shell syntax before the denylist check so the
	// block reason explains which feature is unsupported.
	if _, err := parseCommand(command); err != nil {
		return CommandCheck{RiskLevel: RiskDangerous, Blocked: true, BlockReason: err.Error()}
	}
	for _, p := range dangerousPatterns {
		if p.re.MatchString(command) {
			return CommandCheck{RiskLevel: RiskDangerous, Blocked: true, BlockReason: p.desc}
		}
	}
	for _, p := range elevatedPatterns {
		if p.MatchString(command) {
			return CommandCheck{RiskLevel: RiskElevated}
		}
	}
	// G-SEC-02: no command is "Safe" — minimum risk is Elevated so the
	// approval UI always requires manual confirmation.
	return CommandCheck{RiskLevel: RiskElevated}
}

// checkMCPCommand classifies an mcp.<server>.<tool> command. If the MCP
// service is not configured or the server/tool is unknown, the command is
// blocked. Otherwise the risk is determined by ClassifyMCPToolRisk.
func (s *AgentService) checkMCPCommand(command string) CommandCheck {
	lister := s.mcpListerForCache()
	if lister == nil {
		return CommandCheck{
			RiskLevel:   RiskDangerous,
			Blocked:     true,
			BlockReason: "MCP service not configured",
		}
	}
	// Parse mcp.<server>.<tool> — tool name may contain dots, so split
	// into exactly 3 parts: "mcp", server, tool.
	parts := strings.SplitN(command, ".", 3)
	if len(parts) != 3 || parts[0] != "mcp" || parts[1] == "" || parts[2] == "" {
		return CommandCheck{
			RiskLevel:   RiskDangerous,
			Blocked:     true,
			BlockReason: "invalid MCP tool namespace (expected mcp.<server>.<tool>)",
		}
	}
	server := parts[1]
	tool := parts[2]
	// M-7: use cached tool list when within TTL to avoid fetching the
	// full list from all MCP servers on every command check.
	tools, err := s.fetchMCPToolsCached(lister)
	if err != nil {
		return CommandCheck{RiskLevel: RiskElevated}
	}
	for _, t := range tools {
		if t.Server == server && t.Tool == tool {
			return CommandCheck{RiskLevel: t.RiskLevel}
		}
	}
	// Tool not found — block rather than risk an unknown call.
	return CommandCheck{
		RiskLevel:   RiskDangerous,
		Blocked:     true,
		BlockReason: fmt.Sprintf("MCP tool %s.%s not found", server, tool),
	}
}

// mcpListerForCache returns the tool lister to use for cache lookups.
// Returns the test override (mcpLister) if set, otherwise the production
// mcpService. Returns nil if neither is configured.
func (s *AgentService) mcpListerForCache() mcpToolLister {
	if s.mcpLister != nil {
		return s.mcpLister
	}
	s.mu.Lock()
	mcp := s.mcpService
	s.mu.Unlock()
	if mcp == nil {
		return nil
	}
	return mcp
}

// fetchMCPToolsCached returns the MCP tool list, using a TTL-based cache
// (M-7). On cache hit (within TTL), the cached list is returned without
// calling the lister. On cache miss or expiry, a fresh fetch is performed
// and the cache is updated.
func (s *AgentService) fetchMCPToolsCached(lister mcpToolLister) ([]AgentMCPTool, error) {
	ttl := s.mcpCacheTTL
	if ttl <= 0 {
		ttl = mcpCacheDefaultTTL
	}
	s.mcpCacheMu.Lock()
	if s.mcpCachedTools != nil && time.Since(s.mcpCacheFetchedAt) < ttl {
		cached := s.mcpCachedTools
		s.mcpCacheMu.Unlock()
		return cached, nil
	}
	s.mcpCacheMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := lister.ListAgentMCPTools(ctx)
	if err != nil {
		return nil, err
	}
	s.mcpCacheMu.Lock()
	s.mcpCachedTools = tools
	s.mcpCacheFetchedAt = time.Now()
	s.mcpCacheMu.Unlock()
	return tools, nil
}

// InvalidateMCPCache clears the cached MCP tool list so the next
// checkMCPCommand call fetches a fresh list. Call this when MCP server
// configuration changes (M-7).
func (s *AgentService) InvalidateMCPCache() {
	s.mcpCacheMu.Lock()
	s.mcpCachedTools = nil
	s.mcpCacheFetchedAt = time.Time{}
	s.mcpCacheMu.Unlock()
}

// CallMCPTool is retained only for package-level compatibility. Renderer MCP
// execution is owned by the unified Agent capability pipeline, so this
// legacy endpoint is deliberately deny-only and must not be exported.
//
//wails:ignore
func (s *AgentService) CallMCPTool(ctx context.Context, namespace string, args map[string]interface{}) (*MCPToolResult, error) {
	return nil, fmt.Errorf("backend MCP approval token required: %w", ErrInvalidInput)
}

// ExecResult is the outcome of a synchronous command execution.
type ExecResult struct {
	Command     string    `json:"command"`
	Cwd         string    `json:"cwd"`
	Stdout      string    `json:"stdout"`
	Stderr      string    `json:"stderr"`
	ExitCode    int       `json:"exitCode"`
	DurationMs  int64     `json:"durationMs"`
	RiskLevel   RiskLevel `json:"riskLevel"`
	Blocked     bool      `json:"blocked"`
	BlockReason string    `json:"blockReason,omitempty"`
}

const maxAgentCommandOutputBytes = 256 * 1024

const defaultAgentCommandTimeout = 30 * time.Second

type boundedAgentCommandOutput struct {
	data  []byte
	total int64
}

func (o *boundedAgentCommandOutput) Write(p []byte) (int, error) {
	written := len(p)
	o.total += int64(written)
	remaining := maxAgentCommandOutputBytes - len(o.data)
	if remaining > 0 {
		if remaining > written {
			remaining = written
		}
		o.data = append(o.data, p[:remaining]...)
	}
	// Report the full write even after the capture budget is exhausted so the
	// child process can continue and exit instead of observing a short write.
	return written, nil
}

func (o *boundedAgentCommandOutput) String() string {
	return boundAgentText(string(o.data), o.total, maxAgentCommandOutputBytes)
}

func appendAgentCommandNotice(output, notice string) string {
	if notice == "" {
		return output
	}
	notice = strings.ToValidUTF8(notice, "\uFFFD")
	if len(notice) >= maxAgentCommandOutputBytes {
		return boundAgentText(notice, int64(len(notice)), maxAgentCommandOutputBytes)
	}
	return boundAgentText(output, int64(len(output)), maxAgentCommandOutputBytes-len(notice)) + notice
}

func (s *AgentService) executionCommandTimeout() time.Duration {
	s.mu.Lock()
	timeout := s.commandTimeout
	s.mu.Unlock()
	if timeout <= 0 {
		return defaultAgentCommandTimeout
	}
	return timeout
}

// ExecCommand runs the given command line in the given working directory
// and returns the captured stdout/stderr. A 30-second timeout is enforced
// to prevent the agent from hanging on interactive commands.
//
// HIGH-03: the command is parsed into a simple argv (executable + args)
// using github.com/google/shlex and executed directly via
// exec.CommandContext — no shell wrapper (`sh -c` / `cmd /c`). Shell
// syntax (pipes, redirects, variable expansion, command substitution,
// command chaining, background execution, glob) is rejected by
// parseCommand and reported via CommandCheck.BlockReason.
//
// Security (N-1): a workspace root is required and cwd is sandboxed to that
// root (empty defaults to root, paths outside are rejected). Commands
// matching the denylist are blocked before execution. Each execution is
// written to the audit log with redacted command metadata, cwd, exit code,
// duration, and risk level.
func (s *AgentService) ExecCommand(command, cwd string) (ExecResult, error) {
	return ExecResult{Command: command, Cwd: cwd, Blocked: true, BlockReason: "backend approval token required"}, fmt.Errorf("backend approval token required: %w", ErrInvalidInput)
}

func (s *AgentService) executeCommand(command, cwd string) (ExecResult, error) {
	if strings.TrimSpace(command) == "" {
		return ExecResult{Command: command, Cwd: cwd, RiskLevel: RiskSafe, Blocked: true, BlockReason: "command is required"}, fmt.Errorf("command is required: %w", ErrInvalidInput)
	}
	if check := s.CheckCommand(command); check.Blocked {
		result := ExecResult{
			Command: command, Cwd: cwd, RiskLevel: RiskDangerous,
			Blocked: true, BlockReason: check.BlockReason,
		}
		s.audit(cwd, result)
		return result, fmt.Errorf("command blocked: %s", check.BlockReason)
	}
	lease, err := s.acquireWorkspaceLease()
	if err != nil {
		return ExecResult{}, err
	}
	return s.executeCommandWithLease(command, cwd, lease)
}

func (s *AgentService) executeCommandWithLease(command, cwd string, lease workspaceLease) (ExecResult, error) {
	if strings.TrimSpace(command) == "" {
		// 使用 ErrInvalidInput 哨兵错误，使前端可通过 errors.Is 识别（BUG3）。
		// 与 CheckCommand 保持一致：空命令视为安全但无操作，返回结构化结果而非裸 error。
		return ExecResult{Command: command, Cwd: cwd, RiskLevel: RiskSafe, Blocked: true, BlockReason: "command is required"}, fmt.Errorf("command is required: %w", ErrInvalidInput)
	}

	// Denylist + shell-syntax check — block destructive commands and
	// unsupported shell syntax before execution.
	check := s.CheckCommand(command)
	if check.Blocked {
		result := ExecResult{
			Command:     command,
			Cwd:         cwd,
			RiskLevel:   RiskDangerous,
			Blocked:     true,
			BlockReason: check.BlockReason,
		}
		s.audit(result.Cwd, result)
		return result, fmt.Errorf("command blocked: %s", check.BlockReason)
	}

	// Sandbox cwd — default to root, reject paths outside the workspace.
	resolvedCwd, err := s.validateCwd(cwd)
	if err != nil {
		return ExecResult{}, err
	}

	// HIGH-03: parse into argv and execute directly without a shell.
	// CheckCommand already verified the command is parseable, but we
	// call parseCommand again to get the argv slice.
	argv, err := parseCommand(command)
	if err != nil {
		// Should not happen — CheckCommand already validated parsing.
		return ExecResult{}, fmt.Errorf("parse command: %w", err)
	}

	// Use a timeout so a misbehaving command cannot block the agent loop.
	commandTimeout := s.executionCommandTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := commandContext(ctx, argv[0], argv[1:]...)
	if resolvedCwd != "" {
		cmd.Dir = resolvedCwd
	}

	start := time.Now()
	var stdout, stderr boundedAgentCommandOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if s.beforeWorkspaceCommandStart != nil {
		s.beforeWorkspaceCommandStart()
	}
	if err := lease.validateCurrent(); err != nil {
		return ExecResult{Command: command, Cwd: resolvedCwd, Blocked: true, BlockReason: err.Error()}, err
	}

	runErr := cmd.Run()
	duration := time.Since(start).Milliseconds()

	result := ExecResult{
		Command:    command,
		Cwd:        resolvedCwd,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   0,
		DurationMs: duration,
		RiskLevel:  check.RiskLevel,
	}

	if runErr != nil {
		// CommandContext commonly returns *exec.ExitError after killing a timed
		// out child. Classify the context first so timeout is observable and the
		// unified runtime records a failed terminal usage receipt.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Stderr = appendAgentCommandNotice(result.Stderr, fmt.Sprintf("\n[command timed out after %s]", commandTimeout))
			result.ExitCode = -1
			s.audit(resolvedCwd, result)
			return result, fmt.Errorf("command timed out after %s: %w", commandTimeout, context.DeadlineExceeded)
		}
		// If the command ran but exited non-zero, extract the exit code
		// and return a normal result (not an error). The agent should see
		// the stderr and decide what to do.
		// N-106: use errors.As instead of a type assertion so wrapped
		// errors (e.g. fmt.Errorf("...: %w", exitErr)) are still
		// recognized as ExitError.
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			s.audit(resolvedCwd, result)
			return result, nil
		}
		// Other errors (command not found, etc.) are returned as errors.
		s.audit(resolvedCwd, result)
		return result, fmt.Errorf("run command: %w", runErr)
	}

	s.audit(resolvedCwd, result)
	return result, nil
}

// audit writes a structured log entry for an agent command execution.
// If no audit logger is configured (file could not be opened), it falls
// back to slog.Default().
func (s *AgentService) audit(cwd string, r ExecResult) {
	executableHash, argc, commandHash := commandAuditMetadata(r.Command)
	keyvals := []any{
		"executableHash", executableHash,
		"argc", argc,
		"commandHash", commandHash,
		"cwd", cwd,
		"exitCode", r.ExitCode,
		"durationMs", r.DurationMs,
		"riskLevel", string(r.RiskLevel),
		"blocked", r.Blocked,
	}
	s.auditEvent("agent exec", keyvals...)
}

// commandAuditMetadata permits correlation and basic forensic review without
// persisting argv values, which may contain API keys, headers, or URL secrets.
func commandAuditMetadata(command string) (executableHash string, argc int, commandHash string) {
	commandSum := sha256.Sum256([]byte(command))
	commandHash = hex.EncodeToString(commandSum[:])
	argv, err := parseCommand(command)
	if err != nil {
		return "", 0, commandHash
	}
	executableSum := sha256.Sum256([]byte(argv[0]))
	return hex.EncodeToString(executableSum[:]), len(argv), commandHash
}
