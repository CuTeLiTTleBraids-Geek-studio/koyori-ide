package services

import (
	"bytes"
	"context"
	crypto_rand "crypto/rand"
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
	{"format (disk format)", regexp.MustCompile(`(?i)\bformat\b`)},
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
	auditLog                    *os.File
	auditLogger                 *slog.Logger
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
	approvalMu     sync.Mutex
	approvals      map[string]commandApproval
	rootGeneration uint64
	approveCommand func(command, cwd string, risk RiskLevel) bool
	// G-02: write-file approval (mirrors the command approval flow).
	writeApprovalMu sync.Mutex
	writeApprovals  map[string]writeApproval
	approveWrite    func(targetPath string, size int64) bool
	// GOAL-P1-02: backend-enforced tool-call budget. See agent_budget.go.
	//
	// Lazily initialized via ensureBudget because AgentService is constructed
	// as a bare struct literal in several tests; a nil budget must not panic.
	budgetInit sync.Once
	budget     *toolBudget
}

type commandApproval struct {
	argv           []string
	cwd            string
	rootGeneration uint64
	expiresAt      time.Time
	// budgetEpoch binds this capability to the tool-budget epoch that issued it
	// (GOAL-P1-02). Redemption re-checks it so a token minted before the user
	// opened a new epoch cannot be spent afterwards, and vice versa. Without
	// this, a caller could mint tokens up to the ceiling, ask the user to
	// "continue", and then spend the old batch on top of the fresh allowance.
	budgetEpoch uint64
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
	svc := &AgentService{workspaceContext: workspaceContext}
	svc.approveCommand = nativeCommandApproval
	logPath := filepath.Join(xdg.CacheHome, "koyori-ide", "agent-audit.log")
	// P1-a: audit log contains sensitive command/agent activity - restrict
	// to owner-only (0600) instead of world-readable 0644.
	if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
		svc.auditLog = f
		svc.auditLogger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
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

// Close releases resources held by the service. N-103: the audit log file
// opened in NewAgentService was never closed, leaking a file descriptor
// for the lifetime of the process. This is called from main on shutdown.
// Safe to call multiple times; subsequent calls are no-ops.
func (s *AgentService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auditLog != nil {
		err := s.auditLog.Close()
		s.auditLog = nil
		return err
	}
	return nil
}

// agentServiceRootSetter is the narrow, package-sealed capability used by
// ProjectService to propagate and roll back the agent workspace root.
type agentServiceRootSetter interface {
	setWorkspaceRoot(string) error
	currentWorkspaceRoot() string
	restoreWorkspaceRoot(string) error
}

// configureWorkspaceRoot sets the directory within which agent commands are
// allowed to run. It is reserved for trusted bootstrap code; renderer code
// must not be able to alter this security boundary. An empty root is rejected.
//
// Plan 11 Task 5: propagates the workspace root to SkillsService so that
// project-scoped skills (G-SEC-03) load from <root>/.koyori-ide/skills/. The
// reload is best-effort: failure is logged but does not block the agent
// (skills are a non-critical enhancement).
//
//wails:ignore
func (s *AgentService) configureWorkspaceRoot(root string) error {
	return s.setWorkspaceRoot(root)
}

func (s *AgentService) setWorkspaceRoot(root string) error {
	if root == "" {
		return fmt.Errorf("agent workspace root is required: %w", ErrInvalidInput)
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
	s.mu.Lock()
	s.rootDir = abs
	s.rootGeneration++
	sk := s.skillsService
	s.mu.Unlock()
	if sk != nil {
		sk.setWorkspaceRoot(abs)
		// Best-effort reload; errors are surfaced via slog, not propagated.
		if err := sk.Load(); err != nil {
			slog.Warn("skills reload on workspace change failed", "err", err)
		}
	}
	return nil
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
	s.mu.Lock()
	s.rootDir = ""
	s.rootGeneration++
	sk := s.skillsService
	s.mu.Unlock()
	if sk != nil {
		sk.setWorkspaceRoot("")
	}
	return nil
}

// RequestCommandApproval creates a short-lived, single-use capability for a
// specific command and working directory. Checking a command in the renderer
// is only advisory; execution requires this backend-issued token.
func (s *AgentService) RequestCommandApproval(command, cwd string) (string, error) {
	check := s.CheckCommand(command)
	if check.Blocked {
		return "", fmt.Errorf("command blocked: %s", check.BlockReason)
	}
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("command is required: %w", ErrInvalidInput)
	}
	lease, err := s.acquireWorkspaceLease()
	if err != nil {
		return "", err
	}
	resolvedCwd, err := lease.resolve(cwd)
	if err != nil {
		return "", err
	}
	generation := lease.generation
	argv, err := parseCommand(command)
	if err != nil {
		return "", err
	}
	// GOAL-P1-02: fail before prompting when the budget is already spent.
	//
	// This pre-check is a UX affordance only — prompting the user and *then*
	// refusing would be worse than refusing up front. It is deliberately not the
	// enforcement point: two concurrent callers can both pass it. The atomic
	// consume below is what actually bounds issuance.
	if err := s.ensureBudget().precheck(); err != nil {
		return "", err
	}
	if s.approveCommand == nil || !s.approveCommand(command, resolvedCwd, check.RiskLevel) {
		return "", fmt.Errorf("command execution was not approved: %w", ErrNotAllowed)
	}
	if err := lease.validateCurrent(); err != nil {
		return "", err
	}
	// GOAL-P1-02: consume atomically, after approval and before minting.
	//
	// Ordering is the contract for execution point 4: a declined approval must
	// not spend budget (otherwise declining is punished), while an approved call
	// must spend it before a token exists (otherwise a caller could obtain
	// tokens beyond the ceiling and spend them later). Concurrent approvals
	// serialize on the budget mutex, so N racing callers consume N distinct
	// slots and the (limit+1)-th is refused.
	budgetEpoch, err := s.ensureBudget().reserve()
	if err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := crypto_rand.Read(raw); err != nil {
		return "", fmt.Errorf("create approval token: %w", err)
	}
	token := hex.EncodeToString(raw)
	s.approvalMu.Lock()
	if s.approvals == nil {
		s.approvals = make(map[string]commandApproval)
	}
	s.approvals[token] = commandApproval{
		argv:           argv,
		cwd:            resolvedCwd,
		rootGeneration: generation,
		budgetEpoch:    budgetEpoch,
		expiresAt:      time.Now().Add(2 * time.Minute),
	}
	s.approvalMu.Unlock()
	return token, nil
}

func (s *AgentService) consumeCommandApproval(token, command, cwd string) error {
	if token == "" {
		return fmt.Errorf("command approval is required: %w", ErrInvalidInput)
	}
	resolvedCwd, generation, err := s.validateCwdWithGeneration(cwd)
	if err != nil {
		return err
	}
	argv, err := parseCommand(command)
	if err != nil {
		return err
	}
	s.approvalMu.Lock()
	approval, ok := s.approvals[token]
	if ok {
		delete(s.approvals, token)
	}
	s.approvalMu.Unlock()
	argvMatches := len(approval.argv) == len(argv)
	if argvMatches {
		for i := range argv {
			if approval.argv[i] != argv[i] {
				argvMatches = false
				break
			}
		}
	}
	if !ok || time.Now().After(approval.expiresAt) || !argvMatches || approval.cwd != resolvedCwd || approval.rootGeneration != generation {
		return fmt.Errorf("invalid, expired, or mismatched command approval: %w", ErrInvalidInput)
	}
	// GOAL-P1-02 AC 3: a capability is bound to the budget epoch that issued it.
	//
	// Without this, a caller could mint tokens right up to the ceiling, ask the
	// user to open a new epoch, and then redeem the stockpiled tokens on top of
	// the fresh allowance — spending 2N calls for an N-call budget. The token is
	// already deleted above, so a rejected cross-epoch token is also burned
	// rather than left available for another attempt.
	if approval.budgetEpoch != s.ensureBudget().currentEpoch() {
		return fmt.Errorf(
			"command approval was issued in a previous tool-budget epoch: %w",
			ErrInvalidInput,
		)
	}
	return nil
}

func (s *AgentService) discardCommandApproval(token string) {
	s.approvalMu.Lock()
	delete(s.approvals, token)
	s.approvalMu.Unlock()
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
	{'~', "home directory expansion (~) is not supported — use the full path"},
	{'\n', "multi-line commands are not supported — run each command separately"},
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

// CallMCPTool dispatches an mcp.<server>.<tool> call to the MCP service
// after the user has approved it (Plan 11 Task 4 Step 6). The args map is
// passed as the tool's arguments. The result is returned as a JSON string
// for the agent to interpret.
func (s *AgentService) CallMCPTool(ctx context.Context, namespace string, args map[string]interface{}) (*MCPToolResult, error) {
	return nil, fmt.Errorf("backend MCP approval token required: %w", ErrInvalidInput)
}

func (s *AgentService) callMCPTool(ctx context.Context, namespace string, args map[string]interface{}) (*MCPToolResult, error) {
	s.mu.Lock()
	mcp := s.mcpService
	s.mu.Unlock()
	if mcp == nil {
		return nil, fmt.Errorf("MCP service not configured: %w", ErrInvalidInput)
	}
	parts := strings.SplitN(namespace, ".", 3)
	if len(parts) != 3 || parts[0] != "mcp" {
		return nil, fmt.Errorf("invalid MCP namespace %q: %w", namespace, ErrInvalidInput)
	}
	server, tool := parts[1], parts[2]
	return mcp.callTool(ctx, server, tool, args)
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

// ExecuteApprovedCommand consumes a backend-issued approval and executes the
// exact command it was issued for. The token cannot be replayed.
func (s *AgentService) ExecuteApprovedCommand(command, cwd, approvalToken string) (ExecResult, error) {
	lease, err := s.acquireWorkspaceLease()
	if err != nil {
		return ExecResult{Command: command, Cwd: cwd, Blocked: true, BlockReason: err.Error()}, err
	}
	if err := s.consumeCommandApproval(approvalToken, command, cwd); err != nil {
		return ExecResult{Command: command, Cwd: cwd, Blocked: true, BlockReason: err.Error()}, err
	}
	return s.executeCommandWithLease(command, cwd, lease)
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := commandContext(ctx, argv[0], argv[1:]...)
	if resolvedCwd != "" {
		cmd.Dir = resolvedCwd
	}

	start := time.Now()
	var stdout, stderr bytes.Buffer
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
		// If the context deadline was exceeded, return a timeout result.
		// N-107: use errors.Is so wrapped context errors are recognized.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Stderr += "\n[command timed out after 30s]"
			result.ExitCode = -1
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
	if s.auditLogger != nil {
		s.auditLogger.Info("agent exec", keyvals...)
		return
	}
	slog.Default().Info("agent exec", keyvals...)
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
