package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func nativeDebugExecutableApproval(kind, path string) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Approve debug launch").SetMessage(
		fmt.Sprintf("Allow this workspace-provided %s to run?\n\n%s", kind, path),
	)
	dialog.AddButton("Run once").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("Cancel").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

func (d *DebugService) acquireWorkspaceLease() (workspaceLease, error) {
	return acquireWorkspaceLease(d.workspaceContext, "", 0)
}

func validateDebugWorkspacePath(lease workspaceLease, value, label string, requireFile bool) (string, error) {
	resolved, err := lease.resolve(value)
	if err != nil {
		return "", fmt.Errorf("debug %s is outside the active workspace: %w", label, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("open debug %s: %w", label, err)
	}
	if requireFile && !info.Mode().IsRegular() {
		return "", fmt.Errorf("debug %s is not a regular file", label)
	}
	if !requireFile && !info.IsDir() {
		return "", fmt.Errorf("debug %s is not a directory", label)
	}
	return resolved, nil
}

func (d *DebugService) bindLaunchToWorkspace(cfg DebugLaunchConfig) (DebugLaunchConfig, workspaceLease, error) {
	lease, err := d.acquireWorkspaceLease()
	if err != nil {
		return DebugLaunchConfig{}, workspaceLease{}, err
	}
	if cfg.Dir == "" {
		cfg.Dir = lease.root
	}
	cfg.Dir, err = validateDebugWorkspacePath(lease, cfg.Dir, "working directory", false)
	if err != nil {
		return DebugLaunchConfig{}, workspaceLease{}, err
	}
	if cfg.Program != "" {
		cfg.Program, err = validateDebugWorkspacePath(lease, cfg.Program, "program", true)
		if err != nil {
			return DebugLaunchConfig{}, workspaceLease{}, err
		}
	}
	if cfg.Kind == "browser" {
		if cfg.WebRoot == "" {
			cfg.WebRoot = lease.root
		}
		cfg.WebRoot, err = validateDebugWorkspacePath(lease, cfg.WebRoot, "web root", false)
		if err != nil {
			return DebugLaunchConfig{}, workspaceLease{}, err
		}
		for remote, local := range cfg.PathMappings {
			if strings.TrimSpace(local) == "" {
				continue
			}
			resolved, resolveErr := validateDebugWorkspacePath(lease, local, "path mapping "+remote, false)
			if resolveErr != nil {
				return DebugLaunchConfig{}, workspaceLease{}, resolveErr
			}
			cfg.PathMappings[remote] = resolved
		}
	}
	if cfg.ExecutablePath != "" {
		abs, absErr := filepath.Abs(cfg.ExecutablePath)
		if absErr != nil {
			return DebugLaunchConfig{}, workspaceLease{}, fmt.Errorf("resolve debug executable: %w", absErr)
		}
		info, statErr := os.Stat(abs)
		if statErr != nil || !info.Mode().IsRegular() {
			return DebugLaunchConfig{}, workspaceLease{}, fmt.Errorf("debug executable is not a regular file: %s", cfg.ExecutablePath)
		}
		cfg.ExecutablePath = filepath.Clean(abs)
	}
	return cfg, lease, nil
}

func (d *DebugService) approveWorkspaceProvidedExecutable(cfg DebugLaunchConfig) error {
	kind, path := "", ""
	if cfg.ExecutablePath != "" && (cfg.Request == "" || cfg.Request == "launch") {
		kind, path = "executable", cfg.ExecutablePath
	} else if cfg.Program != "" {
		kind, path = "program", cfg.Program
	}
	if path == "" {
		return nil
	}
	approver := d.approveProjectExecutable
	if approver == nil {
		approver = nativeDebugExecutableApproval
	}
	if !approver(kind, path) {
		return fmt.Errorf("workspace-provided debug %s was not approved: %w", kind, ErrNotAllowed)
	}
	return nil
}

// LaunchPackage starts dlv dap and launches a debug session for packageDir.
func (d *DebugService) LaunchPackage(packageDir string) (DebugSessionInfo, error) {
	return d.LaunchWithConfig(DebugLaunchConfig{Kind: "package", Dir: packageDir, Mode: "debug"})
}

// LaunchTest starts dlv dap and launches a test debug session (-test.run regex).
func (d *DebugService) LaunchTest(packageDir, runRegex string) (DebugSessionInfo, error) {
	return d.LaunchWithConfig(DebugLaunchConfig{Kind: "test", Dir: packageDir, RunRegex: runRegex, Mode: "test"})
}

// LaunchNode starts the built-in Node/TS CDP debugger. It intentionally covers
// a focused inspector workflow rather than the complete js-debug feature set.
func (d *DebugService) LaunchNode(program string, args []string) (DebugSessionInfo, error) {
	return d.LaunchWithConfig(DebugLaunchConfig{
		Kind:    "node",
		Program: program,
		Args:    args,
		Dir:     filepath.Dir(program),
	})
}

// LaunchWithConfig starts a session from a saved/selected profile (prompt-12 12-G).
func (d *DebugService) LaunchWithConfig(cfg DebugLaunchConfig) (DebugSessionInfo, error) {
	var lease workspaceLease
	if d.workspaceContext != nil {
		var err error
		cfg, lease, err = d.bindLaunchToWorkspace(cfg)
		if err != nil {
			return DebugSessionInfo{}, err
		}
		if err := d.approveWorkspaceProvidedExecutable(cfg); err != nil {
			return DebugSessionInfo{}, err
		}
		if d.beforeWorkspaceCommandStart != nil {
			d.beforeWorkspaceCommandStart()
		}
		if err := lease.validateCurrent(); err != nil {
			return DebugSessionInfo{}, err
		}
	}
	kind := cfg.Kind
	if kind == "" {
		kind = "package"
	}
	switch kind {
	case "node":
		return d.launchNode(cfg)
	case "browser":
		return d.launchBrowser(cfg)
	case languagePackDebugKind:
		return d.launchExternalLanguagePackDAP(cfg)
	default:
		mode := cfg.Mode
		if mode == "" {
			if kind == "test" {
				mode = "test"
			} else {
				mode = "debug"
			}
		}
		return d.launchDAP(cfg.Dir, mode, cfg.RunRegex, cfg)
	}
}

// Restart re-launches the last configuration (prompt-12 12-A).
func (d *DebugService) Restart() (DebugSessionInfo, error) {
	owner := d.activeSession()
	if owner == nil {
		return DebugSessionInfo{}, fmt.Errorf("no previous launch to restart")
	}
	owner.mu.Lock()
	spec := owner.lastLaunch
	owner.mu.Unlock()
	if spec.Kind == "" && spec.Dir == "" && spec.Program == "" {
		return DebugSessionInfo{}, fmt.Errorf("no previous launch to restart")
	}
	cfg := DebugLaunchConfig{
		Kind:           spec.Kind,
		AdapterID:      spec.AdapterID,
		Dir:            spec.Dir,
		Program:        spec.Program,
		RunRegex:       spec.RunRegex,
		Args:           spec.Args,
		Env:            spec.Env,
		StopEntry:      spec.StopEntry,
		Mode:           spec.Mode,
		Request:        spec.Request,
		Browser:        spec.Browser,
		ExecutablePath: spec.ExecutablePath,
		URL:            spec.URL,
		Address:        spec.Address,
		RuntimeArgs:    append([]string(nil), spec.RuntimeArgs...),
		TargetID:       spec.TargetID,
		WebRoot:        spec.WebRoot,
		SourceMaps:     spec.SourceMaps,
		PathMappings:   cloneBrowserStringMap(spec.PathMappings),
	}
	if cfg.Kind == "" {
		cfg.Kind = "package"
	}
	return d.LaunchWithConfig(cfg)
}

func (d *DebugService) launchDAP(packageDir, mode, runRegex string, cfg DebugLaunchConfig) (DebugSessionInfo, error) {
	debugger, ok := builtInLanguagePackDebuggerForLanguage("go")
	if !ok || debugger.Protocol != "dap" {
		return DebugSessionInfo{}, fmt.Errorf("no DAP debugger is declared for Go")
	}
	dlv, err := exec.LookPath(debugger.Executable)
	if err != nil {
		return DebugSessionInfo{}, fmt.Errorf("%s not found on PATH: %s", debugger.Executable, debugger.InstallHint)
	}
	// Stop previous session if any (prompt-11: allow relaunch).
	if err := d.Stop(); err != nil {
		return DebugSessionInfo{}, fmt.Errorf("stop previous debug session: %w", err)
	}
	owner := d.activeSession()

	abs := packageDir
	if abs == "" {
		var cwdErr error
		abs, cwdErr = os.Getwd()
		if cwdErr != nil {
			return DebugSessionInfo{}, fmt.Errorf("get working directory: %w", cwdErr)
		}
	}
	if a, err := filepath.Abs(abs); err == nil {
		abs = a
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return DebugSessionInfo{}, fmt.Errorf("package dir invalid: %s", packageDir)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return DebugSessionInfo{}, err
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		return DebugSessionInfo{}, fmt.Errorf("release debug listen probe: %w", err)
	}

	// Use the language-pack argv and append only the backend-generated listen
	// address, so a manifest cannot select an arbitrary executable or socket.
	dapArgs := append([]string(nil), debugger.Args...)
	dapArgs = append(dapArgs, "--listen="+addr)
	cmd := command(dlv, dapArgs...)
	cmd.Dir = abs
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return DebugSessionInfo{}, fmt.Errorf("start dlv dap: %w", err)
	}
	slog.Info("dlv dap launched", "addr", addr, "dir", abs, "pid", cmd.Process.Pid)

	// Wait for port to accept connections.
	var conn net.Conn
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	if conn == nil {
		killErr := cmd.Process.Kill()
		if killErr != nil && errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		waitErr := cmd.Wait()
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				waitErr = nil
			}
		}
		cleanupErr := errors.Join(killErr, waitErr)
		if cleanupErr != nil {
			return DebugSessionInfo{}, fmt.Errorf("could not connect to dlv dap on %s; cleanup failed: %w", addr, cleanupErr)
		}
		return DebugSessionInfo{}, fmt.Errorf("could not connect to dlv dap on %s", addr)
	}

	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.cmd = cmd
	owner.running = true
	owner.addr = addr
	owner.mode = mode
	owner.adapterID = debugger.ID
	owner.sourcePackID = debugger.SourcePackID
	owner.sourcePackVersion = debugger.SourcePackVersion
	owner.started = time.Now()
	owner.conn = conn
	owner.pending = make(map[int]chan dapMessage)
	owner.readerDone = make(chan struct{})
	owner.readerDoneOnce = new(sync.Once)
	owner.readerWG = new(sync.WaitGroup)
	owner.readerWG.Add(1)
	owner.processDone = make(chan struct{})
	owner.processDoneOnce = new(sync.Once)
	readerDone := owner.readerDone
	readerDoneOnce := owner.readerDoneOnce
	readerWG := owner.readerWG
	processDone := owner.processDone
	processDoneOnce := owner.processDoneOnce
	owner.stopped = false
	owner.stack = nil
	owner.locals = nil
	owner.cwd = abs
	// keep breakpoints list (re-apply after launch)
	bpsCopy := append([]DebugBreakpoint(nil), owner.breakpoints...)
	funcBpsCopy := append([]FunctionBreakpoint(nil), owner.functionBreakpoints...)
	owner.mu.Unlock()

	d.startDAPReadLoop(owner, generation, conn, readerDone, readerDoneOnce, readerWG)
	go func() {
		owner.waitForProcessExitTracked(cmd, generation, processDone, processDoneOnce)
		slog.Info("dlv dap exited", "addr", addr)
	}()
	request := func(command string, args map[string]interface{}) (json.RawMessage, error) {
		return d.dapRequestBodyForRun(owner, generation, conn, command, args)
	}

	if err := initializeDAPSessionForRunWithAdapter(owner, generation, request, debugger.ID); err != nil {
		owner.finishDAPRun(generation, conn)
		return DebugSessionInfo{}, fmt.Errorf("dap initialize: %w", err)
	}

	// Launch configuration for Delve DAP.
	launchArgs := map[string]interface{}{
		"request":     "launch",
		"mode":        "debug",
		"program":     abs,
		"cwd":         abs,
		"stopOnEntry": cfg.StopEntry,
	}
	if mode == "test" || cfg.Kind == "test" {
		launchArgs["mode"] = "test"
		launchArgs["program"] = abs
		if runRegex != "" {
			launchArgs["args"] = []string{"-test.run", runRegex}
		}
	}
	if len(cfg.Args) > 0 && mode != "test" {
		launchArgs["args"] = cfg.Args
	}
	if len(cfg.Env) > 0 {
		launchArgs["env"] = cfg.Env
	}
	if _, err := request("launch", launchArgs); err != nil {
		owner.finishDAPRun(generation, conn)
		return DebugSessionInfo{}, fmt.Errorf("dap launch: %w", err)
	}

	// Re-apply breakpoints grouped by file.
	if err := d.applyAllBreakpointsForRun(owner, generation, conn, bpsCopy); err != nil {
		if !dapRunCurrent(owner, generation, conn) {
			return DebugSessionInfo{}, err
		}
		slog.Debug("re-apply breakpoints", "err", err)
	}
	// Re-apply function breakpoints (prompt-5 setFunctionBreakpoints).
	if len(funcBpsCopy) > 0 {
		if _, err := request("setFunctionBreakpoints", map[string]interface{}{
			"breakpoints": funcBpsCopy,
		}); err != nil {
			if !dapRunCurrent(owner, generation, conn) {
				return DebugSessionInfo{}, err
			}
			slog.Debug("re-apply function breakpoints", "err", err)
		}
	}

	if _, err := request("configurationDone", map[string]interface{}{}); err != nil {
		if !dapRunCurrent(owner, generation, conn) {
			return DebugSessionInfo{}, err
		}
		// Some adapters treat this as optional after launch; log only.
		slog.Debug("configurationDone", "err", err)
	}

	owner.mu.Lock()
	if owner.runGeneration != generation || owner.conn != conn {
		owner.mu.Unlock()
		return DebugSessionInfo{}, fmt.Errorf("debug run changed")
	}
	owner.lastLaunch = debugLaunchSpec{
		Kind: cfg.Kind, Dir: abs, RunRegex: runRegex, Mode: mode,
		Args: cfg.Args, Env: cfg.Env, StopEntry: cfg.StopEntry,
	}
	if owner.lastLaunch.Kind == "" {
		if mode == "test" {
			owner.lastLaunch.Kind = "test"
		} else {
			owner.lastLaunch.Kind = "package"
		}
	}
	sessionInfo := dapSessionInfoLocked(owner)
	owner.mu.Unlock()

	return sessionInfo, nil
}

// launchNode: Node/TS via --inspect-brk + CDP into the same Debug panel (prompt-13 13-A).
func (d *DebugService) launchNode(cfg DebugLaunchConfig) (DebugSessionInfo, error) {
	if err := d.Stop(); err != nil {
		return DebugSessionInfo{}, fmt.Errorf("stop previous debug session: %w", err)
	}
	owner := d.activeSession()
	prog := cfg.Program
	if prog == "" {
		return DebugSessionInfo{}, fmt.Errorf("node program path required")
	}
	if a, err := filepath.Abs(prog); err == nil {
		prog = a
	}
	dir := cfg.Dir
	if dir == "" {
		dir = filepath.Dir(prog)
	}
	debugger, ok := builtInLanguagePackDebuggerForPath(prog)
	if !ok || debugger.Protocol != "cdp" {
		return DebugSessionInfo{}, fmt.Errorf("no CDP debugger is declared for %s", filepath.Base(prog))
	}
	nodeBin, err := exec.LookPath(debugger.Executable)
	if err != nil {
		return DebugSessionInfo{}, fmt.Errorf("%s not found on PATH: %s", debugger.Executable, debugger.InstallHint)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return DebugSessionInfo{}, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		return DebugSessionInfo{}, fmt.Errorf("release node debug listen probe: %w", err)
	}

	inspectAddress := fmt.Sprintf("127.0.0.1:%d", port)
	args := make([]string, 0, len(debugger.Args)+1+len(cfg.Args))
	inspectFlag := false
	for _, arg := range debugger.Args {
		if arg == "--inspect-brk" {
			args = append(args, "--inspect-brk="+inspectAddress)
			inspectFlag = true
			continue
		}
		args = append(args, arg)
	}
	if !inspectFlag {
		return DebugSessionInfo{}, fmt.Errorf("language-pack debugger %q does not declare --inspect-brk", debugger.ID)
	}
	args = append(args, prog)
	args = append(args, cfg.Args...)
	cmd := command(nodeBin, args...)
	cmd.Dir = dir
	if len(cfg.Env) > 0 {
		env := os.Environ()
		for k, v := range cfg.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	if err := cmd.Start(); err != nil {
		return DebugSessionInfo{}, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	cdp, cdpErr := connectNodeCDPWithLogger(addr, 8*time.Second, d.emitDebugProtocol)
	if cdpErr != nil {
		slog.Warn("node CDP connect failed; session without live stack", "err", cdpErr)
	}

	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.cmd = cmd
	owner.running = true
	owner.addr = addr
	owner.mode = "node"
	owner.adapterID = debugger.ID
	owner.sourcePackID = debugger.SourcePackID
	owner.sourcePackVersion = debugger.SourcePackVersion
	owner.processDone = make(chan struct{})
	owner.processDoneOnce = new(sync.Once)
	processDone := owner.processDone
	processDoneOnce := owner.processDoneOnce
	owner.cwd = dir
	owner.cdp = cdp
	owner.supportsAsyncStackTrace = cdp != nil && cdp.SupportsAsyncStackTrace()
	owner.stopped = true
	// GOAL-P1-03: stop-at-entry is a stop like any other, so it starts a fresh
	// step-in target epoch.
	owner.stopSequence++
	owner.stopReason = "entry"
	owner.lastError = ""
	if cdpErr != nil {
		owner.lastError = cdpErr.Error()
	}
	owner.lastLaunch = debugLaunchSpec{Kind: "node", Program: prog, Dir: dir, Args: cfg.Args, Env: cfg.Env}
	bpsCopy := append([]DebugBreakpoint(nil), owner.breakpoints...)
	owner.mu.Unlock()
	if cdp != nil {
		cdp.setProtocolLogger(d.emitDebugProtocol)
		cdp.mu.Lock()
		cdp.onPaused = d.nodePausedHandler(owner, generation, cdp)
		cdp.onAsyncStack = d.nodeAsyncStackHandler(owner, generation)
		cdp.mu.Unlock()
		// G14: --inspect-brk waits for the debugger; release it so the runtime
		// starts and pauses at the first statement (Break on start). Without
		// this, stop-at-entry is only an optimistic flag: no Debugger.paused
		// event arrives and a later resume is rejected by the adapter.
		if _, rerr := cdp.callResult("Runtime.runIfWaitingForDebugger", map[string]interface{}{}); rerr != nil {
			slog.Warn("node CDP runIfWaitingForDebugger failed", "err", rerr)
		}
	}

	// Apply existing breakpoints over CDP
	if cdp != nil {
		for _, b := range bpsCopy {
			_, verified, msg, err := cdp.setBreakpointByURL(b.File, b.Line, b.Condition, b.LogMessage)
			owner.mu.Lock()
			if owner.runGeneration != generation {
				owner.mu.Unlock()
				break
			}
			for i := range owner.breakpoints {
				if owner.breakpoints[i].File == b.File && owner.breakpoints[i].Line == b.Line {
					owner.breakpoints[i].Verified = verified
					if err != nil {
						owner.breakpoints[i].Message = err.Error()
						owner.breakpoints[i].Verified = false
					} else {
						owner.breakpoints[i].Message = msg
					}
				}
			}
			owner.mu.Unlock()
		}
	}

	go func() {
		owner.waitForProcessExitTracked(cmd, generation, processDone, processDoneOnce)
	}()

	msg := fmt.Sprintf("Node CDP session on %s — same Debug panel (breakpoints/stack/continue)", addr)
	if cdpErr != nil {
		msg = fmt.Sprintf("Node inspect on %s (CDP connect failed: %v)", addr, cdpErr)
	}
	return DebugSessionInfo{
		Running: true, Address: addr, Mode: "node", Message: msg,
		Stopped: true, StopReason: "entry", AdapterID: debugger.ID,
		SourcePackID: debugger.SourcePackID, SourcePackVersion: debugger.SourcePackVersion,
	}, nil
}

func (d *DebugService) launchBrowser(cfg DebugLaunchConfig) (DebugSessionInfo, error) {
	if err := d.Stop(); err != nil {
		return DebugSessionInfo{}, fmt.Errorf("stop previous debug session: %w", err)
	}
	owner := d.activeSession()
	if owner == nil {
		return DebugSessionInfo{}, fmt.Errorf("no debug session")
	}

	browserCfg := browserDebugConfig{
		Browser: cfg.Browser, Request: cfg.Request, ExecutablePath: cfg.ExecutablePath,
		URL: cfg.URL, Address: cfg.Address, RuntimeArgs: cfg.RuntimeArgs,
		TargetID: cfg.TargetID, WebRoot: cfg.WebRoot, SourceMaps: cfg.SourceMaps,
		PathMappings: cfg.PathMappings,
	}
	spec, err := validateBrowserDebugConfig(browserCfg)
	if err != nil {
		return DebugSessionInfo{}, err
	}
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.mu.Unlock()

	var launch *browserLaunch
	address := spec.Address
	if spec.Request == "launch" {
		launch, address, err = launchBrowserDebug(browserCfg, d.browserDeps)
		if err != nil {
			d.finishBrowserStartFailure(owner, generation, nil, nil)
			return DebugSessionInfo{}, err
		}
	}

	enumerate := d.browserEnumerate
	if enumerate == nil {
		enumerate = waitForBrowserTargets
	}
	targets, err := enumerate(context.Background(), address, browserConnectTimeout)
	if err != nil {
		d.finishBrowserStartFailure(owner, generation, launch, nil)
		return DebugSessionInfo{}, err
	}
	target, err := selectBrowserTarget(targets, spec.TargetID)
	if err != nil {
		d.finishBrowserStartFailure(owner, generation, launch, nil)
		return DebugSessionInfo{}, err
	}
	connect := d.browserConnect
	if connect == nil {
		connect = connectBrowserTarget
	}
	cdp, err := connect(target, browserConnectTimeout)
	if err != nil {
		d.finishBrowserStartFailure(owner, generation, launch, nil)
		return DebugSessionInfo{}, err
	}

	owner.mu.Lock()
	if owner.runGeneration != generation {
		owner.mu.Unlock()
		d.finishBrowserStartFailure(owner, generation, launch, cdp)
		return DebugSessionInfo{}, fmt.Errorf("debug run changed")
	}
	owner.browserTargetEpoch++
	targetEpoch := owner.browserTargetEpoch
	owner.browserLaunch = launch
	owner.browserConfig = spec
	owner.browserConfig.Address = address
	owner.browserTargets = append([]BrowserTarget(nil), targets...)
	owner.browserTargetID = target.ID
	owner.browserConsole = nil
	owner.browserNetwork = nil
	owner.cdp = cdp
	owner.renewDebugThreadsRunLocked()
	owner.running = true
	owner.addr = address
	owner.mode = "browser"
	owner.adapterID = ""
	owner.sourcePackID = ""
	owner.sourcePackVersion = ""
	owner.cwd = cfg.WebRoot
	owner.stopped = false
	owner.stopReason = ""
	owner.lastError = ""
	owner.supportsAsyncStackTrace = cdp.SupportsAsyncStackTrace()
	owner.lastLaunch = debugLaunchSpec{
		Kind: "browser", Dir: cfg.Dir, StopEntry: cfg.StopEntry,
		Request: spec.Request, Browser: spec.Browser, ExecutablePath: spec.ExecutablePath,
		URL: spec.URL, Address: spec.Address, RuntimeArgs: append([]string(nil), spec.RuntimeArgs...),
		TargetID: spec.TargetID, WebRoot: spec.WebRoot, SourceMaps: spec.SourceMaps,
		PathMappings: cloneBrowserStringMap(spec.PathMappings),
	}
	bps := append([]DebugBreakpoint(nil), owner.breakpoints...)
	owner.mu.Unlock()

	d.bindBrowserCDPHandlers(owner, generation, targetEpoch, cdp)
	d.applyBrowserBreakpoints(owner, generation, targetEpoch, cdp, bps)
	if cfg.StopEntry {
		if err := cdp.Pause(); err != nil {
			slog.Debug("debug: pause browser at entry failed", "err", err)
		}
	}
	if launch != nil {
		go d.watchBrowserProcessExit(owner, generation, launch, cdp)
	}
	browserLabel := "Chrome"
	if spec.Browser == "edge" {
		browserLabel = "Edge"
	}
	message := fmt.Sprintf("%s CDP session on %s", browserLabel, address)
	return DebugSessionInfo{Running: true, Address: address, Mode: "browser", Message: message}, nil
}

func (d *DebugService) finishBrowserStartFailure(owner *DebugSession, generation uint64, launch *browserLaunch, cdp *nodeCDPClient) {
	if cdp != nil {
		if err := cdp.Close(); err != nil {
			slog.Debug("debug: close browser cdp after start failure", "err", err)
		}
	}
	if launch != nil {
		ctx, cancel := context.WithTimeout(context.Background(), browserStopTimeout)
		if err := launch.Stop(ctx); err != nil {
			slog.Debug("debug: stop browser after start failure", "err", err)
		}
		cancel()
	}
	owner.mu.Lock()
	var resources debugSessionResources
	if owner.runGeneration == generation {
		resources = owner.cleanupLocked()
	}
	owner.mu.Unlock()
	if resources.cdp == cdp {
		resources.cdp = nil
	}
	if resources.browser == launch {
		resources.browser = nil
	}
	if err := owner.closeDetachedResources(resources, false, true, true); err != nil {
		slog.Debug("debug: close detached browser resources after start failure", "err", err)
	}
}

func (d *DebugService) watchBrowserProcessExit(owner *DebugSession, generation uint64, launch *browserLaunch, cdp *nodeCDPClient) {
	<-launch.Done()
	owner.mu.Lock()
	if owner.runGeneration != generation || owner.browserLaunch != launch {
		owner.mu.Unlock()
		return
	}
	resources := owner.cleanupLocked()
	owner.mu.Unlock()
	if resources.cdp != cdp {
		resources.cdp = cdp
	}
	// The browser process has already exited, so only close remaining I/O.
	resources.browser = nil
	if err := owner.closeDetachedResources(resources, false, false, false); err != nil {
		slog.Debug("debug: cleanup failed after browser exit", "err", err)
	}
}

func (d *DebugService) bindBrowserCDPHandlers(owner *DebugSession, generation, targetEpoch uint64, cdp *nodeCDPClient) {
	cdp.setProtocolLogger(d.emitDebugProtocol)
	cdp.mu.Lock()
	cdp.onPaused = d.nodePausedHandler(owner, generation, cdp)
	cdp.onAsyncStack = d.nodeAsyncStackHandler(owner, generation)
	cdp.onBrowserConsole = d.browserConsoleHandler(owner, generation, targetEpoch)
	cdp.onBrowserNetwork = d.browserNetworkHandler(owner, generation, targetEpoch)
	cdp.mu.Unlock()
}

func (d *DebugService) browserConsoleHandler(owner *DebugSession, generation, targetEpoch uint64) func(BrowserConsoleEntry) {
	return func(entry BrowserConsoleEntry) {
		owner.mu.Lock()
		defer owner.mu.Unlock()
		if owner.runGeneration != generation || owner.browserTargetEpoch != targetEpoch || owner.mode != "browser" {
			return
		}
		entry.Generation = generation
		owner.browserConsole = appendBoundedBrowserConsole(owner.browserConsole, entry)
	}
}

func (d *DebugService) browserNetworkHandler(owner *DebugSession, generation, targetEpoch uint64) func(BrowserNetworkEntry) {
	return func(entry BrowserNetworkEntry) {
		owner.mu.Lock()
		defer owner.mu.Unlock()
		if owner.runGeneration != generation || owner.browserTargetEpoch != targetEpoch || owner.mode != "browser" {
			return
		}
		entry.Generation = generation
		owner.browserNetwork = appendBoundedBrowserNetwork(owner.browserNetwork, entry)
	}
}

func appendBoundedBrowserConsole(entries []BrowserConsoleEntry, entry BrowserConsoleEntry) []BrowserConsoleEntry {
	if len(entries) >= maxBrowserConsoleEntries {
		copy(entries, entries[len(entries)-maxBrowserConsoleEntries+1:])
		entries = entries[:maxBrowserConsoleEntries-1]
	}
	return append(entries, entry)
}

func appendBoundedBrowserNetwork(entries []BrowserNetworkEntry, entry BrowserNetworkEntry) []BrowserNetworkEntry {
	if len(entries) >= maxBrowserNetworkEntries {
		copy(entries, entries[len(entries)-maxBrowserNetworkEntries+1:])
		entries = entries[:maxBrowserNetworkEntries-1]
	}
	return append(entries, entry)
}

func (d *DebugService) applyBrowserBreakpoints(owner *DebugSession, generation, targetEpoch uint64, cdp *nodeCDPClient, breakpoints []DebugBreakpoint) {
	for _, breakpoint := range breakpoints {
		owner.mu.Lock()
		if owner.runGeneration != generation || owner.browserTargetEpoch != targetEpoch || owner.cdp != cdp {
			owner.mu.Unlock()
			return
		}
		spec := owner.browserConfig
		owner.mu.Unlock()
		sourceURL := browserLocalPathToURL(breakpoint.File, spec)
		_, verified, message, setErr := cdp.setBreakpointByURL(sourceURL, breakpoint.Line, breakpoint.Condition, breakpoint.LogMessage)
		owner.mu.Lock()
		if owner.runGeneration == generation && owner.browserTargetEpoch == targetEpoch && owner.cdp == cdp {
			for i := range owner.breakpoints {
				if owner.breakpoints[i].File == breakpoint.File && owner.breakpoints[i].Line == breakpoint.Line {
					owner.breakpoints[i].Verified = verified && setErr == nil
					owner.breakpoints[i].Message = message
					if setErr != nil {
						owner.breakpoints[i].Message = setErr.Error()
					}
				}
			}
		}
		owner.mu.Unlock()
	}
}

func (d *DebugService) SelectBrowserTarget(targetID string) error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	if !owner.running || owner.mode != "browser" {
		owner.mu.Unlock()
		return fmt.Errorf("browser debug session is not running")
	}
	generation := owner.runGeneration
	address := owner.addr
	owner.mu.Unlock()

	enumerate := d.browserEnumerate
	if enumerate == nil {
		enumerate = waitForBrowserTargets
	}
	targets, err := enumerate(context.Background(), address, browserConnectTimeout)
	if err != nil {
		return err
	}
	target, err := selectBrowserTarget(targets, targetID)
	if err != nil {
		return err
	}
	connect := d.browserConnect
	if connect == nil {
		connect = connectBrowserTarget
	}
	cdp, err := connect(target, browserConnectTimeout)
	if err != nil {
		return err
	}

	owner.mu.Lock()
	if owner.runGeneration != generation || !owner.running || owner.mode != "browser" || owner.addr != address {
		owner.mu.Unlock()
		return errors.Join(fmt.Errorf("debug run changed"), cdp.Close())
	}
	oldCDP := owner.cdp
	owner.browserTargetEpoch++
	targetEpoch := owner.browserTargetEpoch
	owner.browserTargets = append([]BrowserTarget(nil), targets...)
	owner.browserTargetID = target.ID
	owner.browserConsole = nil
	owner.browserNetwork = nil
	owner.cdp = cdp
	owner.renewDebugThreadsRunLocked()
	owner.supportsAsyncStackTrace = cdp.SupportsAsyncStackTrace()
	owner.clearAsyncStackLocked()
	bps := append([]DebugBreakpoint(nil), owner.breakpoints...)
	owner.mu.Unlock()

	d.bindBrowserCDPHandlers(owner, generation, targetEpoch, cdp)
	if oldCDP != nil {
		if err := oldCDP.Close(); err != nil {
			slog.Debug("debug: close previous browser target failed", "err", err)
		}
	}
	d.applyBrowserBreakpoints(owner, generation, targetEpoch, cdp, bps)
	return nil
}

func (d *DebugService) nodePausedHandler(owner *DebugSession, generation uint64, evaluator nodeRunEvaluator) func(string, []DebugStackFrame, []DebugVariable) {
	return func(reason string, frames []DebugStackFrame, locals []DebugVariable) {
		owner.mu.Lock()
		if owner.runGeneration != generation || (evaluator != nil && owner.cdp != evaluator) {
			owner.mu.Unlock()
			return
		}
		if owner.mode == "browser" {
			for i := range frames {
				frames[i].File = browserSourceURLToLocal(frames[i].File, owner.browserConfig)
			}
		}
		owner.stopped = true
		// GOAL-P1-03: new stop invalidates any step-in target menu fetched
		// during the previous stop.
		owner.stopSequence++
		owner.stopReason = reason
		if owner.stopReason == "" {
			owner.stopReason = "paused"
		}
		owner.stack = frames
		owner.stackTotalFrames = len(frames)
		owner.stackHasMore = false
		owner.asyncStackRootID = ""
		owner.asyncStackContinuations = make(map[string]nodeAsyncStackContinuation)
		owner.locals = locals
		owner.touchDebugThreadsStateLocked()
		owner.mu.Unlock()
		go func() {
			if evaluator != nil {
				if _, err := d.refreshWatchesForRun(owner, generation, evaluator.Evaluate); err != nil {
					slog.Debug("debug: refresh watches after pause failed", "err", err)
				}
			}
		}()
	}
}

func (d *DebugService) nodeAsyncStackHandler(owner *DebugSession, generation uint64) func(*nodeAsyncStackTrace, *nodeStackTraceID) {
	return func(trace *nodeAsyncStackTrace, traceID *nodeStackTraceID) {
		owner.mu.Lock()
		defer owner.mu.Unlock()
		if owner.runGeneration != generation || !owner.supportsAsyncStackTrace {
			return
		}
		if trace != nil {
			owner.asyncStackRootID = registerNodeAsyncContinuationLocked(owner, generation, nodeAsyncStackContinuation{Trace: trace})
			return
		}
		if traceID != nil && traceID.ID != "" {
			copyID := *traceID
			owner.asyncStackRootID = registerNodeAsyncContinuationLocked(owner, generation, nodeAsyncStackContinuation{ParentID: &copyID})
		}
	}
}

func registerNodeAsyncContinuationLocked(owner *DebugSession, generation uint64, continuation nodeAsyncStackContinuation) string {
	if owner.runGeneration != generation {
		return ""
	}
	if continuation.Trace == nil && (continuation.ParentID == nil || continuation.ParentID.ID == "") {
		return ""
	}
	if owner.asyncStackContinuations == nil {
		owner.asyncStackContinuations = make(map[string]nodeAsyncStackContinuation)
	}
	owner.asyncStackCounter++
	id := fmt.Sprintf("async-%d-%d", generation, owner.asyncStackCounter)
	owner.asyncStackContinuations[id] = continuation
	return id
}

func (d *DebugService) registerNodeAsyncStackForRun(owner *DebugSession, generation uint64, trace *nodeAsyncStackTrace) string {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return registerNodeAsyncContinuationLocked(owner, generation, nodeAsyncStackContinuation{Trace: trace})
}

func (d *DebugService) registerNodeAsyncParentIDForRun(owner *DebugSession, generation uint64, parentID nodeStackTraceID) string {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return registerNodeAsyncContinuationLocked(owner, generation, nodeAsyncStackContinuation{ParentID: &parentID})
}

// LoadAsyncStack resolves one generation-bound CDP async stack continuation.
// Runtime.getStackTrace is used only for an adapter-provided parentId.
func (d *DebugService) LoadAsyncStack(ctx context.Context, expectedGeneration uint64, continuationID string) (DebugAsyncStackSegment, error) {
	if continuationID == "" {
		return DebugAsyncStackSegment{}, fmt.Errorf("async stack continuation required")
	}
	owner := d.activeSession()
	if owner == nil {
		return DebugAsyncStackSegment{}, fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	if owner.runGeneration != expectedGeneration {
		owner.mu.Unlock()
		return DebugAsyncStackSegment{}, fmt.Errorf("debug run changed")
	}
	if !owner.supportsAsyncStackTrace {
		owner.mu.Unlock()
		return DebugAsyncStackSegment{}, fmt.Errorf("adapter does not support async stack traces")
	}
	if !owner.stopped {
		owner.mu.Unlock()
		return DebugAsyncStackSegment{}, fmt.Errorf("debug session is not paused")
	}
	continuation, ok := owner.asyncStackContinuations[continuationID]
	cdp := owner.cdp
	owner.mu.Unlock()
	if !ok {
		return DebugAsyncStackSegment{}, fmt.Errorf("unknown or expired async stack continuation")
	}
	if err := ctx.Err(); err != nil {
		return DebugAsyncStackSegment{}, err
	}

	trace := continuation.Trace
	if trace == nil && continuation.ParentID != nil {
		if cdp == nil {
			return DebugAsyncStackSegment{}, fmt.Errorf("node CDP session unavailable")
		}
		raw, err := cdp.callResultContext(ctx, "Runtime.getStackTrace", map[string]interface{}{
			"stackTraceId": *continuation.ParentID,
		})
		if err != nil {
			return DebugAsyncStackSegment{}, err
		}
		var response struct {
			StackTrace *nodeAsyncStackTrace `json:"stackTrace"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return DebugAsyncStackSegment{}, fmt.Errorf("parse Runtime.getStackTrace response: %w", err)
		}
		trace = response.StackTrace
	}
	if trace == nil {
		return DebugAsyncStackSegment{}, fmt.Errorf("async stack continuation returned no stack trace")
	}

	frames := make([]DebugStackFrame, 0, len(trace.CallFrames))
	for _, frame := range trace.CallFrames {
		name := frame.FunctionName
		if name == "" {
			name = "(anonymous)"
		}
		frames = append(frames, DebugStackFrame{
			Name:   name,
			File:   nodeURLPath(frame.URL),
			Line:   frame.LineNumber + 1,
			Column: frame.ColumnNumber + 1,
		})
	}

	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.runGeneration != expectedGeneration || !owner.stopped {
		return DebugAsyncStackSegment{}, fmt.Errorf("debug run changed")
	}
	parentID := ""
	if trace.Parent != nil {
		parentID = registerNodeAsyncContinuationLocked(owner, expectedGeneration, nodeAsyncStackContinuation{Trace: trace.Parent})
	} else if trace.ParentID != nil && trace.ParentID.ID != "" {
		copyID := *trace.ParentID
		parentID = registerNodeAsyncContinuationLocked(owner, expectedGeneration, nodeAsyncStackContinuation{ParentID: &copyID})
	}
	return DebugAsyncStackSegment{
		Generation:  expectedGeneration,
		ID:          continuationID,
		Description: trace.Description,
		Frames:      frames,
		ParentID:    parentID,
	}, nil
}

func nodeURLPath(rawURL string) string {
	path := rawURL
	if strings.HasPrefix(path, "file:///") {
		path = strings.TrimPrefix(path, "file:///")
	} else if strings.HasPrefix(path, "file://") {
		path = strings.TrimPrefix(path, "file://")
	}
	return path
}

// ConnectMockDAP connects to an existing DAP server (tests / attach) and runs launch (prompt-12 12-E).
func (d *DebugService) ConnectMockDAP(addr string, launchArgs map[string]interface{}) (DebugSessionInfo, error) {
	if err := d.Stop(); err != nil {
		return DebugSessionInfo{}, fmt.Errorf("stop previous debug session: %w", err)
	}
	owner := d.activeSession()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return DebugSessionInfo{}, err
	}
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.conn = conn
	owner.running = true
	owner.addr = addr
	owner.mode = "attach"
	owner.adapterID = ""
	owner.sourcePackID = ""
	owner.sourcePackVersion = ""
	owner.pending = make(map[int]chan dapMessage)
	owner.readerDone = make(chan struct{})
	owner.readerDoneOnce = new(sync.Once)
	owner.readerWG = new(sync.WaitGroup)
	owner.readerWG.Add(1)
	readerDone := owner.readerDone
	readerDoneOnce := owner.readerDoneOnce
	readerWG := owner.readerWG
	owner.stopped = false
	bpsCopy := append([]DebugBreakpoint(nil), owner.breakpoints...)
	owner.mu.Unlock()
	d.startDAPReadLoop(owner, generation, conn, readerDone, readerDoneOnce, readerWG)
	request := func(command string, args map[string]interface{}) (json.RawMessage, error) {
		return d.dapRequestBodyForRun(owner, generation, conn, command, args)
	}

	if err := initializeDAPSessionForRun(owner, generation, request); err != nil {
		owner.finishDAPRun(generation, conn)
		return DebugSessionInfo{}, err
	}
	if launchArgs == nil {
		launchArgs = map[string]interface{}{"request": "launch", "program": "."}
	}
	if _, err := request("launch", launchArgs); err != nil {
		owner.finishDAPRun(generation, conn)
		return DebugSessionInfo{}, err
	}
	if err := d.applyAllBreakpointsForRun(owner, generation, conn, bpsCopy); err != nil && !dapRunCurrent(owner, generation, conn) {
		return DebugSessionInfo{}, err
	}
	if _, err := request("configurationDone", map[string]interface{}{}); err != nil && !dapRunCurrent(owner, generation, conn) {
		return DebugSessionInfo{}, err
	}
	// Wait briefly for stopped event from adapter
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		owner.mu.Lock()
		if owner.runGeneration != generation || owner.conn != conn {
			owner.mu.Unlock()
			return DebugSessionInfo{}, fmt.Errorf("debug run changed")
		}
		st := owner.stopped
		owner.mu.Unlock()
		if st {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := d.refreshStackAndLocalsForRun(owner, generation, request); err != nil && !dapRunCurrent(owner, generation, conn) {
		return DebugSessionInfo{}, err
	}
	owner.mu.Lock()
	if owner.runGeneration != generation || owner.conn != conn {
		owner.mu.Unlock()
		return DebugSessionInfo{}, fmt.Errorf("debug run changed")
	}
	info := dapSessionInfoLocked(owner)
	owner.mu.Unlock()
	return info, nil
}
