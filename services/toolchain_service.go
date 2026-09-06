package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ToolchainService exposes Go/TS/JS toolchain commands (build, test, lint,
// format) for the command palette and editor context menu (G-FEAT-03).
//
// Commands run in the workspace root (or a file's directory) and their
// stdout/stderr is captured. Compiler/linter output is parsed into
// Diagnostic entries so the frontend can surface them in the Problems
// panel; the raw output is also returned for the Output panel.
//
// Tool resolution: the service checks the ToolPaths map (populated from
// Settings.ToolPaths) first, then falls back to PATH via exec.LookPath.
// When a tool is not installed, RunToolchainCommand returns a result with
// Success=false and an explanatory message that the frontend can show as a
// notification with the install command.
type ToolchainService struct {
	mu                          sync.Mutex
	workspaceRoot               string
	workspaceContext            *WorkspaceContext
	toolPaths                   map[string]string
	goTargets                   map[string]GoTarget
	goTargetProvider            func() (GoTarget, []GoTarget, error)
	beforeWorkspaceCommandStart func()
	testMu                      sync.Mutex
	activeTestCancel            context.CancelFunc
}

const maxToolchainCommandStreamBytes = 2 << 20

type boundedToolchainCommandOutput struct {
	data      []byte
	truncated bool
}

func (b *boundedToolchainCommandOutput) Write(payload []byte) (int, error) {
	remaining := maxToolchainCommandStreamBytes - len(b.data)
	if remaining > len(payload) {
		remaining = len(payload)
	}
	if remaining > 0 {
		b.data = append(b.data, payload[:remaining]...)
	}
	if remaining < len(payload) {
		b.truncated = true
	}
	return len(payload), nil
}

func (b *boundedToolchainCommandOutput) String() string {
	output := strings.ToValidUTF8(string(b.data), "?")
	if b.truncated {
		output += "\n[toolchain output truncated]\n"
	}
	return output
}

func (b *boundedToolchainCommandOutput) Len() int {
	return len(b.data)
}

// NewToolchainService creates a ToolchainService with no workspace root and
// no trusted backend tool path overrides.
func NewToolchainService() *ToolchainService {
	return newToolchainService(nil)
}

// NewToolchainServiceWithWorkspaceContext creates the renderer-facing
// service. Commands use the shared workspace root at invocation time.
func NewToolchainServiceWithWorkspaceContext(workspaceContext *WorkspaceContext) *ToolchainService {
	return newToolchainService(workspaceContext)
}

func newToolchainService(workspaceContext *WorkspaceContext) *ToolchainService {
	s := &ToolchainService{
		workspaceContext: workspaceContext,
		toolPaths:        map[string]string{},
		goTargets:        map[string]GoTarget{},
	}
	s.goTargetProvider = s.discoverGoTargets
	return s
}

func (s *ToolchainService) acquireWorkspaceLease() (workspaceLease, error) {
	s.mu.Lock()
	root := s.workspaceRoot
	s.mu.Unlock()
	return acquireWorkspaceLease(s.workspaceContext, root, 0)
}

// commandWorkspaceLease keeps the historical no-root behavior only for the
// dependency-free constructor used by focused unit tests. Production always
// injects workspaceContext and therefore cannot take this fallback.
func (s *ToolchainService) commandWorkspaceLease(filePath string) (workspaceLease, error) {
	lease, err := s.acquireWorkspaceLease()
	if err == nil || s.workspaceContext != nil {
		return lease, err
	}
	root := ""
	if filePath != "" {
		abs, absErr := filepath.Abs(filePath)
		if absErr != nil {
			return workspaceLease{}, absErr
		}
		root = filepath.Dir(abs)
	} else {
		root, err = os.Getwd()
		if err != nil {
			return workspaceLease{}, err
		}
	}
	return workspaceLease{root: filepath.Clean(root)}, nil
}

// setWorkspaceRoot sets the directory toolchain commands run in (when no
// per-file directory is supplied). Pass an empty string to disable sandboxing.
// Mirrors the pattern used by FileService and AgentService.
//
//wails:ignore
func (s *ToolchainService) setWorkspaceRoot(root string) error {
	if root == "" {
		s.mu.Lock()
		s.workspaceRoot = ""
		s.mu.Unlock()
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
	s.mu.Lock()
	s.workspaceRoot = abs
	s.mu.Unlock()
	return nil
}

// setToolPaths is reserved for trusted backend wiring and tests. Renderer
// supplied executable paths are not authorization to launch a process.
func (s *ToolchainService) setToolPaths(paths map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if paths == nil {
		s.toolPaths = map[string]string{}
		return
	}
	cp := make(map[string]string, len(paths))
	for k, v := range paths {
		cp[k] = v
	}
	s.toolPaths = cp
}

// GoTarget identifies one supported Go cross-compilation target.
type GoTarget struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

// GoTargetState describes the host and effective target for the active workspace.
type GoTargetState struct {
	Host       GoTarget `json:"host"`
	Current    GoTarget `json:"current"`
	Overridden bool     `json:"overridden"`
}

// ListGoTargets returns the valid GOOS/GOARCH pairs reported by the installed Go toolchain.
func (s *ToolchainService) ListGoTargets() ([]GoTarget, error) {
	_, targets, err := s.goTargetProvider()
	if err != nil {
		return nil, err
	}
	return append([]GoTarget(nil), targets...), nil
}

// GetGoTarget returns the effective target for the active workspace.
func (s *ToolchainService) GetGoTarget() (GoTargetState, error) {
	lease, err := s.acquireWorkspaceLease()
	if err != nil {
		return GoTargetState{}, err
	}
	state, err := s.goTargetForRoot(lease.root)
	if err != nil {
		return GoTargetState{}, err
	}
	if err := lease.validateCurrent(); err != nil {
		return GoTargetState{}, err
	}
	return state, nil
}

func (s *ToolchainService) goTargetForRoot(root string) (GoTargetState, error) {
	host, _, err := s.goTargetProvider()
	if err != nil {
		return GoTargetState{}, err
	}
	s.mu.Lock()
	target, overridden := s.goTargets[root]
	s.mu.Unlock()
	if !overridden {
		target = host
	}
	return GoTargetState{Host: host, Current: target, Overridden: overridden}, nil
}

// SetGoTarget validates and applies a target only to the active workspace.
func (s *ToolchainService) SetGoTarget(goos, goarch string) (GoTargetState, error) {
	lease, leaseErr := s.acquireWorkspaceLease()
	if leaseErr != nil {
		return GoTargetState{}, leaseErr
	}
	wanted := GoTarget{GOOS: strings.TrimSpace(goos), GOARCH: strings.TrimSpace(goarch)}
	host, targets, err := s.goTargetProvider()
	if err != nil {
		return GoTargetState{}, err
	}
	valid := false
	for _, target := range targets {
		if target == wanted {
			valid = true
			break
		}
	}
	if !valid {
		return GoTargetState{}, fmt.Errorf("unsupported Go target %q; choose a pair reported by 'go tool dist list'", wanted.GOOS+"/"+wanted.GOARCH)
	}
	if err := lease.validateCurrent(); err != nil {
		return GoTargetState{}, err
	}
	s.mu.Lock()
	root := lease.root
	s.goTargets[root] = wanted
	s.mu.Unlock()
	return GoTargetState{Host: host, Current: wanted, Overridden: true}, nil
}

// ResetGoTarget removes the active workspace override and restores the host target.
func (s *ToolchainService) ResetGoTarget() (GoTargetState, error) {
	lease, leaseErr := s.acquireWorkspaceLease()
	if leaseErr != nil {
		return GoTargetState{}, leaseErr
	}
	host, _, err := s.goTargetProvider()
	if err != nil {
		return GoTargetState{}, err
	}
	if err := lease.validateCurrent(); err != nil {
		return GoTargetState{}, err
	}
	s.mu.Lock()
	delete(s.goTargets, lease.root)
	s.mu.Unlock()
	return GoTargetState{Host: host, Current: host, Overridden: false}, nil
}

func (s *ToolchainService) discoverGoTargets() (GoTarget, []GoTarget, error) {
	goBin := s.resolveTool("go")
	if goBin == "" {
		return GoTarget{}, nil, fmt.Errorf("go is not installed or not on PATH")
	}
	hostOutput, err := command(goBin, "env", "GOHOSTOS", "GOHOSTARCH").Output()
	if err != nil {
		return GoTarget{}, nil, fmt.Errorf("read host Go target: %w", err)
	}
	hostFields := strings.Fields(string(hostOutput))
	if len(hostFields) < 2 {
		return GoTarget{}, nil, fmt.Errorf("read host Go target: unexpected output %q", strings.TrimSpace(string(hostOutput)))
	}
	host := GoTarget{GOOS: hostFields[0], GOARCH: hostFields[1]}
	distOutput, err := command(goBin, "tool", "dist", "list").Output()
	if err != nil {
		return GoTarget{}, nil, fmt.Errorf("list supported Go targets: %w", err)
	}
	targets := parseGoDistList(string(distOutput))
	if len(targets) == 0 {
		return GoTarget{}, nil, fmt.Errorf("list supported Go targets: no targets returned")
	}
	return host, targets, nil
}

func parseGoDistList(output string) []GoTarget {
	seen := map[GoTarget]bool{}
	targets := make([]GoTarget, 0)
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		target := GoTarget{GOOS: parts[0], GOARCH: parts[1]}
		if seen[target] {
			continue
		}
		seen[target] = true
		targets = append(targets, target)
	}
	return targets
}

func (s *ToolchainService) newToolchainCommand(ctx context.Context, executable string, args []string, dir string) (*exec.Cmd, error) {
	s.mu.Lock()
	root := s.workspaceRoot
	s.mu.Unlock()
	if root == "" {
		root = dir
	}
	return s.newToolchainCommandForWorkspace(ctx, executable, args, dir, root)
}

func (s *ToolchainService) newToolchainCommandForWorkspace(ctx context.Context, executable string, args []string, dir, workspaceRoot string) (*exec.Cmd, error) {
	cmd := commandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if isCmdShim(executable) && filepath.IsAbs(executable) {
		cmd.Env = withPrependedPath(cmd.Env, filepath.Dir(executable))
	}
	toolName := strings.TrimSuffix(strings.ToLower(filepath.Base(executable)), ".exe")
	if toolName != "go" {
		return cmd, nil
	}
	state, err := s.goTargetForRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	cmd.Env = withToolchainEnv(cmd.Env, map[string]string{
		"GOOS":   state.Current.GOOS,
		"GOARCH": state.Current.GOARCH,
	})
	return cmd, nil
}

func withToolchainEnv(base []string, overrides map[string]string) []string {
	env := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, overridden := overrides[strings.ToUpper(key)]; overridden {
				continue
			}
		}
		env = append(env, entry)
	}
	for _, key := range []string{"GOOS", "GOARCH"} {
		if value, ok := overrides[key]; ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func withPrependedPath(base []string, dir string) []string {
	pathValue := ""
	env := make([]string, 0, len(base)+1)
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "PATH") {
			pathValue = value
			continue
		}
		env = append(env, entry)
	}
	if pathValue != "" {
		pathValue = dir + string(os.PathListSeparator) + pathValue
	} else {
		pathValue = dir
	}
	return append(env, "PATH="+pathValue)
}

// ToolchainCommand describes a single toolchain action exposed to the UI.
type ToolchainCommand struct {
	ID                string   `json:"id"`
	Label             string   `json:"label"`
	Language          string   `json:"language"` // "go", "typescript", "javascript", "general"
	Command           string   `json:"command"`  // e.g. "go build", "golangci-lint run"
	Args              []string `json:"args"`
	Description       string   `json:"description"`
	SourcePackID      string   `json:"sourcePackId,omitempty"`
	SourcePackVersion string   `json:"sourcePackVersion,omitempty"`
}

// ToolchainResult is the outcome of running a toolchain command.
type ToolchainResult struct {
	Success  bool                  `json:"success"`
	Output   string                `json:"output"`
	Errors   []ToolchainDiagnostic `json:"errors"`
	Duration int64                 `json:"durationMs"`
	Canceled bool                  `json:"canceled"`
	ExitCode int                   `json:"exitCode"`
	// NotInstalled is true when the tool binary could not be found. The
	// frontend uses this to show an install-command notification instead of
	// a generic error.
	NotInstalled bool   `json:"notInstalled"`
	InstallCmd   string `json:"installCmd,omitempty"`
}

// ToolchainDiagnostic is a single parsed compiler/linter issue. It is
// distinct from the LSP Diagnostic type (which uses an integer severity)
// because toolchain output carries a file path and a string severity.
type ToolchainDiagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // "error", "warning", "info"
	Source   string `json:"source"`   // "go build", "eslint", etc.
}

// allToolchainCommands is sourced from built-in language packs. General
// workspace commands remain IDE-owned because they are not language features.
var generalToolchainCommands = []ToolchainCommand{
	{ID: "npm-scripts", Label: "Run: npm scripts", Language: "general", Command: "npm", Args: []string{"run"}, Description: "List runnable package.json scripts"},
	{ID: "make", Label: "Run: Make", Language: "general", Command: "make", Description: "Run Makefile default target"},
}

var (
	toolchainCatalogMu   sync.RWMutex
	allToolchainCommands = append(builtInLanguagePackToolchainCommands(), generalToolchainCommands...)
)

// installHints are contributed by language packs. The hint is informational;
// execution still resolves the declared basename through the trusted backend.
var installHints = builtInLanguagePackToolchainInstallHints()

func toolchainCatalogSnapshot() ([]ToolchainCommand, map[string]string) {
	toolchainCatalogMu.RLock()
	commands := append([]ToolchainCommand(nil), allToolchainCommands...)
	hints := make(map[string]string, len(installHints))
	for name, hint := range installHints {
		hints[name] = hint
	}
	toolchainCatalogMu.RUnlock()
	return commands, hints
}

func setToolchainLanguagePackCatalog(commands []ToolchainCommand, hints map[string]string) {
	commands = append(append([]ToolchainCommand(nil), commands...), generalToolchainCommands...)
	clonedHints := make(map[string]string, len(hints))
	for name, hint := range hints {
		clonedHints[name] = hint
	}
	toolchainCatalogMu.Lock()
	allToolchainCommands = commands
	installHints = clonedHints
	toolchainCatalogMu.Unlock()
}

// ListToolchainCommands returns the toolchain commands available in the
// current workspace. Go commands are offered when go.mod is present (or go
// is on PATH); TS/JS commands when package.json is present; general
// commands (make / npm) when their respective files exist. When no
// workspace root is set, the full catalog is returned so the palette stays
// populated (commands will report not-installed at run time if unusable).
func (s *ToolchainService) ListToolchainCommands() []ToolchainCommand {
	commands, _ := toolchainCatalogSnapshot()
	s.mu.Lock()
	root := s.workspaceRoot
	s.mu.Unlock()

	if root == "" {
		return commands
	}

	hasGoMod := fileExists(filepath.Join(root, "go.mod"))
	hasGoWork := fileExists(filepath.Join(root, "go.work"))
	hasPkgJSON := fileExists(filepath.Join(root, "package.json"))
	hasMakefile := fileExists(filepath.Join(root, "Makefile")) || fileExists(filepath.Join(root, "makefile"))

	var out []ToolchainCommand
	for _, c := range commands {
		if c.ID == "go-work-sync" {
			if hasGoWork {
				out = append(out, c)
			}
			continue
		}
		switch {
		case c.Language == "go" && !hasGoMod:
			continue
		case (c.Language == "typescript" || c.Language == "javascript") && !hasPkgJSON:
			continue
		case c.ID == "make" && !hasMakefile:
			continue
		case c.ID == "npm-scripts" && !hasPkgJSON:
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		// Fall back to the full catalog so the palette is never empty.
		return commands
	}
	return out
}

// DetectToolchains reports which toolchain binaries are available. The map
// keys are tool names; values are true when the binary resolves (either via
// the ToolPaths override or exec.LookPath on PATH).
func (s *ToolchainService) DetectToolchains() map[string]bool {
	commands, _ := toolchainCatalogSnapshot()
	tools := make([]string, 0, len(commands))
	seen := make(map[string]struct{})
	for _, command := range commands {
		name := strings.TrimSpace(command.Command)
		if fields := strings.Fields(name); len(fields) > 0 {
			name = fields[0]
		}
		if name != "" {
			if _, exists := seen[name]; !exists {
				seen[name] = struct{}{}
				tools = append(tools, name)
			}
		}
	}
	out := make(map[string]bool, len(tools))
	for _, name := range tools {
		out[name] = s.resolveTool(name) != ""
	}
	return out
}

// RunToolchainCommand executes the command identified by cmdID. filePath,
// when non-empty, makes the command run in the file's directory instead of
// the workspace root (useful for linting a single file). The command's
// stdout and stderr are captured; compiler/linter output is parsed into
// Diagnostics.
func (s *ToolchainService) RunToolchainCommand(cmdID string, filePath string) (ToolchainResult, error) {
	commands, hints := toolchainCatalogSnapshot()
	var cmd ToolchainCommand
	found := false
	for _, c := range commands {
		if c.ID == cmdID {
			cmd = c
			found = true
			break
		}
	}
	if !found {
		return ToolchainResult{}, fmt.Errorf("unknown toolchain command: %s", cmdID)
	}
	lease, err := s.commandWorkspaceLease(filePath)
	if err != nil {
		return ToolchainResult{}, err
	}

	// Resolve working directory: file's dir > workspace root > "".
	resolvedFile := ""
	workDir := lease.root
	if filePath != "" {
		resolvedFile, err = lease.resolve(filePath)
		if err != nil {
			return ToolchainResult{}, err
		}
		workDir = filepath.Dir(resolvedFile)
	}
	switch cmdID {
	case "go-test-race", "go-bench", "go-generate", "go-work-sync":
		workDir = lease.root
	}

	// Split the Command field into [tool, ...baseArgs] and resolve the tool.
	tokens := strings.Fields(cmd.Command)
	if len(tokens) == 0 {
		return ToolchainResult{}, fmt.Errorf("empty command for %s", cmdID)
	}
	toolName := tokens[0]
	baseArgs := tokens[1:]

	resolved := s.resolveTool(toolName)
	if resolved == "" {
		return ToolchainResult{
			Success:      false,
			NotInstalled: true,
			InstallCmd:   hints[toolName],
			Output:       fmt.Sprintf("%s is not installed or not on PATH", toolName),
		}, nil
	}

	args := append(append([]string{}, baseArgs...), cmd.Args...)
	// File-scoped language-pack commands append only the backend-resolved
	// target path. The manifest cannot provide an arbitrary path or CWD.
	if filePath != "" && builtInLanguagePackToolchainCommandFileScoped(cmdID) {
		args = append(args, resolvedFile)
	}

	// Execute with a generous timeout so a stuck linter cannot hang the UI.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c, err := s.newToolchainCommandForWorkspace(ctx, resolved, args, workDir, lease.root)
	if err != nil {
		return ToolchainResult{}, err
	}
	// Package-level go test uses file's directory as workDir.

	start := time.Now()
	var stdout, stderr boundedToolchainCommandOutput
	c.Stdout = &stdout
	c.Stderr = &stderr
	if s.beforeWorkspaceCommandStart != nil {
		s.beforeWorkspaceCommandStart()
	}
	if err := lease.validateCurrent(); err != nil {
		return ToolchainResult{}, err
	}
	runErr := c.Run()
	duration := time.Since(start).Milliseconds()

	combined := stdout.String()
	if stderr.Len() > 0 {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr.String()
	}

	// Parse diagnostics from the combined output.
	diags := parseDiagnostics(cmd, combined)

	success := runErr == nil
	result := ToolchainResult{
		Success:  success,
		Output:   combined,
		Errors:   diags,
		Duration: duration,
	}
	return result, nil
}

// RuntimeVersions holds tool versions for StatusBar (prompt-9 9-I).
type RuntimeVersions struct {
	GoVersion   string `json:"goVersion"`
	NodeVersion string `json:"nodeVersion"`
	GoplsVer    string `json:"goplsVersion"`
	HasGoWork   bool   `json:"hasGoWork"`
}

// DetectRuntimeVersions returns go/node/gopls version strings (prompt-9 9-I/N).
func (s *ToolchainService) DetectRuntimeVersions() RuntimeVersions {
	rv := RuntimeVersions{}
	if p, err := exec.LookPath("go"); err == nil {
		if out, err := command(p, "version").Output(); err == nil {
			// "go version go1.22.0 windows/amd64" → go1.22.0
			parts := strings.Fields(string(out))
			if len(parts) >= 3 {
				rv.GoVersion = parts[2]
			} else {
				rv.GoVersion = strings.TrimSpace(string(out))
			}
		}
	}
	if p, err := exec.LookPath("node"); err == nil {
		if out, err := command(p, "--version").Output(); err == nil {
			rv.NodeVersion = strings.TrimSpace(string(out))
		}
	}
	if p, err := exec.LookPath("gopls"); err == nil {
		if out, err := command(p, "version").Output(); err == nil {
			line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
			rv.GoplsVer = line
		}
	}
	s.mu.Lock()
	root := s.workspaceRoot
	s.mu.Unlock()
	if root != "" {
		if _, err := os.Stat(filepath.Join(root, "go.work")); err == nil {
			rv.HasGoWork = true
		}
	}
	return rv
}

// RunTestAtCursor runs the test under the given line (prompt-9 9-C / 9-H).
// language: "go" | "typescript" | "javascript"
// content: full file buffer used to discover TestXxx / it("/test(" names.
func (s *ToolchainService) RunTestAtCursor(language, filePath string, line int, content string) (ToolchainResult, error) {
	lease, err := s.commandWorkspaceLease(filePath)
	if err != nil {
		return ToolchainResult{}, err
	}
	resolvedFile := ""
	workDir := lease.root
	if filePath != "" {
		resolvedFile, err = lease.resolve(filePath)
		if err != nil {
			return ToolchainResult{}, err
		}
		workDir = filepath.Dir(resolvedFile)
	}
	name := findTestNameAtLine(language, content, line)
	if name == "" {
		return ToolchainResult{
			Success: false,
			Output:  "no test found at cursor (expected func TestXxx or it/test(...))",
		}, nil
	}
	var resolved string
	var args []string
	runner := ""
	switch language {
	case "go":
		resolved = s.resolveTool("go")
		if resolved == "" {
			return ToolchainResult{Success: false, NotInstalled: true, InstallCmd: "install Go from https://go.dev", Output: "go not found"}, nil
		}
		// go test -run: TestXxx or TestXxx/sub (prompt-10 10-C)
		runPat := name
		if !strings.Contains(name, "/") {
			runPat = "^" + name + "$"
		}
		args = []string{"test", "-count=1", "-run", runPat, "."}
	case "typescript", "javascript":
		// G15: runner is selected from the project configuration (jest.config*
		// or package.json jest field → Jest; vitest.config*/vite.config* or
		// default → Vitest), not from a fixed file extension heuristic.
		workDir = s.resolveJSProjectRoot(workDir, lease.root)
		runner = s.resolveJSTestRunner(workDir)
		plan, planErr := s.planJSTestRun(runner, name, resolvedFile, workDir)
		if planErr != nil {
			return ToolchainResult{Success: false, NotInstalled: true, InstallCmd: plan.InstallCmd, Output: planErr.Error()}, nil
		}
		resolved = plan.Resolved
		args = plan.Args
	default:
		return ToolchainResult{Success: false, Output: "unsupported language for test-at-cursor: " + language}, nil
	}

	ctx, cancel, err := s.beginTestRun()
	if err != nil {
		return ToolchainResult{}, err
	}
	defer s.finishTestRun(cancel)
	c, err := s.newToolchainCommandForWorkspace(ctx, resolved, args, workDir, lease.root)
	if err != nil {
		return ToolchainResult{}, err
	}
	start := time.Now()
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if s.beforeWorkspaceCommandStart != nil {
		s.beforeWorkspaceCommandStart()
	}
	if err := lease.validateCurrent(); err != nil {
		return ToolchainResult{}, err
	}
	runErr := c.Run()
	combined := stdout.String()
	if stderr.Len() > 0 {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr.String()
	}
	testMatched := strings.Contains(combined, name)
	if runner == "jest" {
		testMatched = strings.Contains(combined, "PASS ")
	}
	if runErr == nil && (runner == "vitest" || runner == "jest") && !testMatched {
		runErr = fmt.Errorf("%s: no test matched %q", runner, name)
		if combined != "" {
			combined += "\n"
		}
		combined += runErr.Error()
	}
	canceled := errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)
	if canceled {
		if combined != "" {
			combined += "\n"
		}
		combined += "test run canceled"
	}
	exitCode := 0
	if runErr != nil {
		exitCode = 1
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			exitCode = exitError.ExitCode()
		}
		if canceled {
			exitCode = -1
		}
	}
	cmdMeta := ToolchainCommand{ID: "test-cursor", Command: language + " test", Language: language}
	diags := parseDiagnostics(cmdMeta, combined)
	// Also parse go test FAIL lines generically
	diags = append(diags, parseGoTestFailures(combined)...)
	return ToolchainResult{
		Success:  runErr == nil,
		Output:   combined,
		Errors:   dedupeToolchainDiags(diags),
		Duration: time.Since(start).Milliseconds(),
		Canceled: canceled,
		ExitCode: exitCode,
	}, nil
}

func (s *ToolchainService) beginTestRun() (context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	s.testMu.Lock()
	defer s.testMu.Unlock()
	if s.activeTestCancel != nil {
		cancel()
		return nil, nil, fmt.Errorf("a test-at-cursor run is already active")
	}
	s.activeTestCancel = cancel
	return ctx, cancel, nil
}

func (s *ToolchainService) finishTestRun(cancel context.CancelFunc) {
	s.testMu.Lock()
	s.activeTestCancel = nil
	s.testMu.Unlock()
	cancel()
}

// CancelTestAtCursor cancels the active test-at-cursor process. It returns
// false when no test-at-cursor process is running.
func (s *ToolchainService) CancelTestAtCursor() bool {
	s.testMu.Lock()
	cancel := s.activeTestCancel
	s.testMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

var (
	goTestFuncRe = regexp.MustCompile(`^\s*func\s+(Test[A-Za-z0-9_]+)`)
	goTestRunRe  = regexp.MustCompile(`\bt\.Run\(\s*["'\x60]([^"'\x60]+)["'\x60]`)
	jsTestRe     = regexp.MustCompile(`^\s*(?:it|test|describe)(?:\.\w+)?\s*(?:\([^)]*\)\s*)?\(\s*['"\x60]([^'"\x60]+)['"\x60]`)
	jsTestEachRe = regexp.MustCompile(`(?:it|test)\.each\s*\([^)]*\)\s*\(\s*['"\x60]([^'"\x60]+)['"\x60]`)
)

// findTestNameAtLine finds the nearest test name at or above line (0-based).
// prompt-10 10-C: also recognizes Go t.Run subtests and vitest test.each / it.each.
func findTestNameAtLine(language, content string, line int) string {
	lines := strings.Split(content, "\n")
	if line < 0 {
		line = 0
	}
	if line >= len(lines) {
		line = len(lines) - 1
	}
	if line < 0 {
		return ""
	}
	var parentGo string
	var subGo string
	for i := 0; i <= line && i < len(lines); i++ {
		l := lines[i]
		if language == "go" {
			if m := goTestFuncRe.FindStringSubmatch(l); m != nil {
				parentGo = m[1]
				subGo = ""
			}
			if m := goTestRunRe.FindStringSubmatch(l); m != nil {
				subGo = m[1]
			}
		}
	}
	if language == "go" {
		// Prefer innermost t.Run at or above cursor; go test -run Parent/Sub
		if parentGo != "" && subGo != "" {
			return parentGo + "/" + subGo
		}
		if parentGo != "" {
			return parentGo
		}
		// fallback scan upward for func only
		for i := line; i >= 0; i-- {
			if m := goTestFuncRe.FindStringSubmatch(lines[i]); m != nil {
				return m[1]
			}
		}
		return ""
	}

	for i := line; i >= 0; i-- {
		l := lines[i]
		if m := jsTestEachRe.FindStringSubmatch(l); m != nil {
			return m[1]
		}
		if m := jsTestRe.FindStringSubmatch(l); m != nil {
			return m[1]
		}
	}
	return ""
}

// GoTestJSONEvent is one line of `go test -json` output (prompt-11 11-F).
type GoTestJSONEvent struct {
	Time    string  `json:"time,omitempty"`
	Action  string  `json:"action"` // run|pass|fail|skip|output|start
	Package string  `json:"package,omitempty"`
	Test    string  `json:"test,omitempty"`
	Output  string  `json:"output,omitempty"`
	Elapsed float64 `json:"elapsed,omitempty"`
}

// GoTestJSONResult aggregates structured test run status for the test explorer.
type GoTestJSONResult struct {
	Success bool              `json:"success"`
	Output  string            `json:"output"`
	Events  []GoTestJSONEvent `json:"events"`
	// StatusByTest maps "Package::TestName" or TestName → pass|fail|skip|run
	StatusByTest map[string]string `json:"statusByTest"`
	DurationMs   int64             `json:"durationMs"`
}

// RunGoTestsJSON runs `go test -json` in packageDir (prompt-11 11-F).
func (s *ToolchainService) RunGoTestsJSON(packageDir, runRegex string) (GoTestJSONResult, error) {
	lease, err := s.acquireWorkspaceLease()
	if err != nil {
		return GoTestJSONResult{}, err
	}
	dir, err := lease.resolve(packageDir)
	if err != nil {
		return GoTestJSONResult{}, err
	}
	goBin := s.resolveTool("go")
	if goBin == "" {
		return GoTestJSONResult{Success: false, Output: "go not found"}, nil
	}
	args := []string{"test", "-json", "-count=1"}
	if runRegex != "" {
		args = append(args, "-run", runRegex)
	}
	args = append(args, ".")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd, cmdErr := s.newToolchainCommandForWorkspace(ctx, goBin, args, dir, lease.root)
	if cmdErr != nil {
		return GoTestJSONResult{}, cmdErr
	}
	start := time.Now()
	if s.beforeWorkspaceCommandStart != nil {
		s.beforeWorkspaceCommandStart()
	}
	if err := lease.validateCurrent(); err != nil {
		return GoTestJSONResult{}, err
	}
	out, err := cmd.CombinedOutput()
	events := parseGoTestJSONLines(string(out))
	status := map[string]string{}
	for _, e := range events {
		if e.Test == "" {
			continue
		}
		key := e.Test
		if e.Package != "" {
			key = e.Package + "::" + e.Test
		}
		switch e.Action {
		case "pass", "fail", "skip", "run":
			status[key] = e.Action
			status[e.Test] = e.Action
		}
	}
	return GoTestJSONResult{
		Success:      err == nil,
		Output:       string(out),
		Events:       events,
		StatusByTest: status,
		DurationMs:   time.Since(start).Milliseconds(),
	}, nil
}

func parseGoTestJSONLines(output string) []GoTestJSONEvent {
	var events []GoTestJSONEvent
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var e GoTestJSONEvent
		if json.Unmarshal([]byte(line), &e) == nil && e.Action != "" {
			events = append(events, e)
		}
	}
	return events
}

// parseGoTestFailures extracts file:line from go test failure output (9-C/9-J).
func parseGoTestFailures(output string) []ToolchainDiagnostic {
	// e.g.     main_test.go:12: expected ...
	re := regexp.MustCompile(`(?m)^\s*([\w./\\-]+\.go):(\d+):\s*(.+)$`)
	var out []ToolchainDiagnostic
	for _, m := range re.FindAllStringSubmatch(output, -1) {
		line, _ := strconv.Atoi(m[2])
		out = append(out, ToolchainDiagnostic{
			File:     m[1],
			Line:     line,
			Column:   1,
			Severity: "error",
			Message:  m[3],
			Source:   "go test",
		})
	}
	return out
}

func dedupeToolchainDiags(in []ToolchainDiagnostic) []ToolchainDiagnostic {
	seen := map[string]bool{}
	var out []ToolchainDiagnostic
	for _, d := range in {
		k := fmt.Sprintf("%s:%d:%s", d.File, d.Line, d.Message)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, d)
	}
	return out
}

// workDirForFile returns the directory to run a command in for the given
// file path, falling back to the workspace root. Empty when neither is set.
func (s *ToolchainService) workDirForFile(filePath string) string {
	if filePath != "" {
		if abs, err := filepath.Abs(filePath); err == nil {
			return filepath.Dir(abs)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspaceRoot
}

func (s *ToolchainService) workDirForCommand(cmdID, filePath string) string {
	switch cmdID {
	case "go-test-race", "go-bench", "go-generate", "go-work-sync":
		s.mu.Lock()
		root := s.workspaceRoot
		s.mu.Unlock()
		if root != "" {
			return root
		}
	}
	return s.workDirForFile(filePath)
}

// resolveTool returns the path to the named tool, checking the ToolPaths
// override first then PATH. Returns "" when not found.
//
// An explicit override is authoritative: when set, the tool is resolved
// solely through the override (absolute path must exist, or bare name must
// resolve on PATH). There is no silent fallback to PATH lookup for that
// tool, so users get deterministic control over which binary runs — a
// broken override surfaces as NotInstalled rather than unexpectedly
// executing a different binary found on PATH.
// jsTestRunPlan describes how to execute one JS test: the resolved binary
// and argv (passed through exec.CommandContext — no shell interpolation).
type jsTestRunPlan struct {
	Resolved     string
	Args         []string
	NotInstalled bool
	InstallCmd   string
	Err          string
}

func (p *jsTestRunPlan) Error() string { return p.Err }

// planJSTestRun builds the argv for a single test run under the chosen runner
// (jest or vitest). The test name and file path are passed as discrete argv
// elements, so shell metacharacters in a test name are never interpreted.
func (s *ToolchainService) planJSTestRun(runner, name, resolvedFile, projectRoot string) (jsTestRunPlan, error) {
	var plan jsTestRunPlan
	namePattern := regexp.QuoteMeta(name)
	resolved := s.resolveJSRunner(runner, projectRoot)
	if resolved == "" {
		plan.InstallCmd = "npm i -D " + runner
		return plan, fmt.Errorf("%s not found in the project or PATH", runner)
	}
	plan.Resolved = resolved
	plan.NotInstalled = false
	switch runner {
	case "jest":
		plan.Args = []string{"--runInBand", "--verbose"}
		if resolvedFile != "" {
			plan.Args = append(plan.Args, resolvedFile)
		}
		plan.Args = append(plan.Args, "-t", namePattern)
		plan.InstallCmd = "npm i -D jest"
		return plan, nil
	case "vitest":
		plan.Args = []string{"run", "--reporter=verbose", "-t", namePattern}
		if resolvedFile != "" {
			plan.Args = append(plan.Args, resolvedFile)
		}
		plan.InstallCmd = "npm i -D vitest"
		return plan, nil
	default:
		return plan, fmt.Errorf("unsupported JavaScript test runner %q", runner)
	}
}

func (s *ToolchainService) resolveJSRunner(runner, projectRoot string) string {
	if projectRoot != "" {
		names := []string{runner}
		if runtime.GOOS == "windows" {
			names = []string{runner + ".cmd", runner + ".exe", runner + ".bat", runner}
		}
		for _, name := range names {
			candidate := filepath.Join(projectRoot, "node_modules", ".bin", name)
			if fileExists(candidate) {
				return candidate
			}
		}
	}
	return s.resolveTool(runner)
}

// resolveJSTestRunner picks the JS test runner from the project layout:
//   - jest.config.js/cjs/mjs/json or a "jest" field in package.json → "jest"
//   - otherwise (vitest.config.*, vite.config.* with a test field, or no
//     config at all) → "vitest" (the modern default)
//
// G15: this replaces the fixed file-extension heuristic so TS/JS projects
// actually run with the runner they are configured for.
func (s *ToolchainService) resolveJSTestRunner(workDir string) string {
	workDir = s.resolveJSProjectRoot(workDir, "")
	for _, name := range []string{"jest.config.js", "jest.config.cjs", "jest.config.mjs", "jest.config.json", "jest.config.ts"} {
		if fileExists(filepath.Join(workDir, name)) {
			return "jest"
		}
	}
	if raw, err := os.ReadFile(filepath.Join(workDir, "package.json")); err == nil {
		var pkg struct {
			Jest json.RawMessage `json:"jest"`
		}
		if json.Unmarshal(raw, &pkg) == nil && pkg.Jest != nil && string(pkg.Jest) != "null" {
			return "jest"
		}
	}
	return "vitest"
}

func (s *ToolchainService) resolveJSProjectRoot(workDir, boundary string) string {
	root, err := filepath.Abs(workDir)
	if err != nil {
		return workDir
	}
	boundaryAbs := ""
	if boundary != "" {
		boundaryAbs, _ = filepath.Abs(boundary)
	}
	for {
		if jsProjectMarker(root) {
			return root
		}
		if root == boundaryAbs || filepath.Dir(root) == root {
			break
		}
		root = filepath.Dir(root)
	}
	return filepath.Clean(workDir)
}

func jsProjectMarker(dir string) bool {
	for _, name := range []string{
		"package.json",
		"jest.config.js", "jest.config.cjs", "jest.config.mjs", "jest.config.json", "jest.config.ts",
		"vitest.config.js", "vitest.config.cjs", "vitest.config.mjs", "vitest.config.ts",
		"vite.config.js", "vite.config.cjs", "vite.config.mjs", "vite.config.ts",
	} {
		if fileExists(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

func (s *ToolchainService) resolveTool(name string) string {
	s.mu.Lock()
	override := s.toolPaths[name]
	root := s.workspaceRoot
	workspaceContext := s.workspaceContext
	s.mu.Unlock()
	if override != "" {
		if filepath.IsAbs(override) {
			if fileExists(override) {
				return override
			}
			return ""
		}
		if p, err := exec.LookPath(override); err == nil {
			return p
		}
		return ""
	}
	if workspaceContext != nil {
		if activeRoot, err := workspaceContext.RequireRoot(); err == nil {
			root = activeRoot
		}
	}
	if root != "" {
		local := filepath.Join(root, "node_modules", ".bin", name)
		if path, err := exec.LookPath(local); err == nil {
			return path
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ---------------------------------------------------------------------------
// Output parsers
// ---------------------------------------------------------------------------

// parseDiagnostics routes the command output to the appropriate parser based
// on the command id / tool. Unknown commands produce no diagnostics.
func parseDiagnostics(cmd ToolchainCommand, output string) []ToolchainDiagnostic {
	toolName := strings.TrimSuffix(strings.ToLower(filepath.Base(strings.TrimSpace(cmd.Command))), ".exe")
	switch {
	case cmd.ID == "golangci-lint":
		return parseGolangciLint(output)
	case cmd.ID == "go-build" || cmd.ID == "go-vet" || strings.HasPrefix(cmd.Command, "go build") || strings.HasPrefix(cmd.Command, "go vet"):
		return parseGoCompiler(output, goToolchainDiagnosticSource(cmd))
	case strings.HasPrefix(cmd.ID, "go-test") || strings.HasPrefix(cmd.Command, "go test"):
		return parseGoCompiler(output, goToolchainDiagnosticSource(cmd))
	case toolName == "tsc" || strings.HasPrefix(cmd.Command, "tsc"):
		return parseTypeScript(output)
	case toolName == "eslint" || strings.HasPrefix(cmd.Command, "eslint"):
		return parseESLint(output)
	}
	return nil
}

func goToolchainDiagnosticSource(cmd ToolchainCommand) string {
	switch {
	case cmd.ID == "go-build":
		return "go build"
	case cmd.ID == "go-vet":
		return "go vet"
	case strings.HasPrefix(cmd.ID, "go-test"):
		return "go test"
	default:
		return cmd.Command
	}
}

// goCompilerRe matches `file.go:line:col: message` and `file.go:line: message`
// (the no-column form used by some compiler errors). The column group is
// optional: when absent, m[3] is "" and parseGoCompiler leaves Column as 0.
var goCompilerRe = regexp.MustCompile(`^(.+\.go):(\d+)(?::(\d+))?:\s*(.*)$`)

// parseGoCompiler parses Go compiler / go vet output:
//
//	main.go:10:3: undefined: foo
//	main.go:12: syntax error
func parseGoCompiler(output, source string) []ToolchainDiagnostic {
	var diags []ToolchainDiagnostic
	for _, line := range splitToolchainLines(output) {
		m := goCompilerRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		col := 0
		if m[3] != "" {
			_, _ = fmt.Sscanf(m[3], "%d", &col)
		}
		var lineNo int
		_, _ = fmt.Sscanf(m[2], "%d", &lineNo)
		severity := "error"
		if strings.Contains(m[4], "warning") || strings.HasPrefix(line, "warning:") {
			severity = "warning"
		}
		diags = append(diags, ToolchainDiagnostic{
			File:     m[1],
			Line:     lineNo,
			Column:   col,
			Message:  m[4],
			Severity: severity,
			Source:   source,
		})
	}
	return diags
}

// golangciLintRe matches `file.go:line:col: message (linter)`.
var golangciLintRe = regexp.MustCompile(`^(.+\.go):(\d+):(\d+):\s*(.+?)\s+\(([^)]+)\)\s*$`)

// parseGolangciLint parses golangci-lint stylish output:
//
//	main.go:10:3: unused variable `foo` (govet)
func parseGolangciLint(output string) []ToolchainDiagnostic {
	var diags []ToolchainDiagnostic
	for _, line := range splitToolchainLines(output) {
		m := golangciLintRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var lineNo, col int
		_, _ = fmt.Sscanf(m[2], "%d", &lineNo)
		_, _ = fmt.Sscanf(m[3], "%d", &col)
		diags = append(diags, ToolchainDiagnostic{
			File:     m[1],
			Line:     lineNo,
			Column:   col,
			Message:  m[4],
			Severity: "warning",
			Source:   "golangci-lint/" + m[5],
		})
	}
	return diags
}

// tsCompilerRe matches `file.ts(line,col): error TS1234: message`. Note:
// there is no colon between the filename and the opening paren.
var tsCompilerRe = regexp.MustCompile(`^(.+\.tsx?)\((\d+),(\d+)\):\s+(error|warning)\s+TS\d+:\s*(.*)$`)

// parseTypeScript parses tsc output:
//
//	src/index.ts(10,3): error TS2322: Type 'string' is not assignable to type 'number'.
func parseTypeScript(output string) []ToolchainDiagnostic {
	var diags []ToolchainDiagnostic
	for _, line := range splitToolchainLines(output) {
		m := tsCompilerRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var lineNo, col int
		_, _ = fmt.Sscanf(m[2], "%d", &lineNo)
		_, _ = fmt.Sscanf(m[3], "%d", &col)
		diags = append(diags, ToolchainDiagnostic{
			File:     m[1],
			Line:     lineNo,
			Column:   col,
			Message:  m[5],
			Severity: m[4],
			Source:   "tsc",
		})
	}
	return diags
}

// eslintRe matches `file:line:col: message rule`.
var eslintRe = regexp.MustCompile(`^(.+):(\d+):(\d+):\s+(.+?)\s+([\w-]+(?:/[a-z-]+)?)\s*$`)

// parseESLint parses eslint stylish output:
//
//	src/index.js:10:3: 'foo' is not defined  no-undef
func parseESLint(output string) []ToolchainDiagnostic {
	var diags []ToolchainDiagnostic
	for _, line := range splitToolchainLines(output) {
		m := eslintRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// Skip lines where the "file" doesn't look like a source path
		// (avoids matching the summary line "✖ N problems (N errors, M warnings)").
		if !looksLikeSourceFile(m[1]) {
			continue
		}
		var lineNo, col int
		_, _ = fmt.Sscanf(m[2], "%d", &lineNo)
		_, _ = fmt.Sscanf(m[3], "%d", &col)
		diags = append(diags, ToolchainDiagnostic{
			File:     m[1],
			Line:     lineNo,
			Column:   col,
			Message:  m[4],
			Severity: "warning",
			Source:   "eslint/" + m[5],
		})
	}
	return diags
}

var sourceExtRe = regexp.MustCompile(`\.(js|mjs|cjs|jsx|ts|mts|cts|tsx|vue|svelte)$`)

func looksLikeSourceFile(s string) bool {
	return sourceExtRe.MatchString(s)
}

// splitToolchainLines splits on \n and \r\n, returning all lines. It is a
// local helper kept distinct from myers_diff.splitLines to avoid a package
// redeclaration conflict.
func splitToolchainLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	// Keep blank lines so indices stay stable if ever needed; the
	// parsers simply won't match them.
	out = append(out, lines...)
	return out
}
