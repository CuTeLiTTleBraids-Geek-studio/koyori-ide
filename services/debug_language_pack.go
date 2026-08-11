package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	languagePackDebugKind             = "language-pack"
	maxLanguagePackAdapterStderrBytes = 64 << 10
)

type boundedLanguagePackAdapterStderr struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *boundedLanguagePackAdapterStderr) Write(payload []byte) (int, error) {
	b.mu.Lock()
	remaining := maxLanguagePackAdapterStderrBytes - len(b.data)
	if remaining > len(payload) {
		remaining = len(payload)
	}
	if remaining > 0 {
		b.data = append(b.data, payload[:remaining]...)
	}
	if remaining < len(payload) {
		b.truncated = true
	}
	b.mu.Unlock()
	return len(payload), nil
}

func (b *boundedLanguagePackAdapterStderr) String() string {
	b.mu.Lock()
	text := strings.ToValidUTF8(string(b.data), "?")
	truncated := b.truncated
	b.mu.Unlock()
	text = strings.Map(func(value rune) rune {
		if value == '\n' || value == '\r' || value == '\t' || value >= ' ' {
			return value
		}
		return '?'
	}, text)
	text = strings.TrimSpace(text)
	if truncated {
		text += "\n[adapter stderr truncated]"
	}
	return text
}

func (d *DebugService) launchExternalLanguagePackDAP(cfg DebugLaunchConfig) (DebugSessionInfo, error) {
	program := cfg.Program
	if program == "" {
		return DebugSessionInfo{}, errors.New("language-pack debug program path is required")
	}
	absProgram, err := filepath.Abs(program)
	if err != nil {
		return DebugSessionInfo{}, fmt.Errorf("resolve language-pack debug program: %w", err)
	}
	programInfo, err := os.Stat(absProgram)
	if err != nil || !programInfo.Mode().IsRegular() {
		return DebugSessionInfo{}, fmt.Errorf("language-pack debug program is not a regular file: %s", program)
	}
	cwd := cfg.Dir
	if cwd == "" {
		cwd = filepath.Dir(absProgram)
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return DebugSessionInfo{}, fmt.Errorf("resolve language-pack debug cwd: %w", err)
	}
	cwdInfo, err := os.Stat(absCwd)
	if err != nil || !cwdInfo.IsDir() {
		return DebugSessionInfo{}, fmt.Errorf("language-pack debug cwd is not a directory: %s", cwd)
	}

	var debugger languagePackDebugger
	var ok bool
	if cfg.AdapterID != "" {
		debugger, ok = activeLanguagePackDebuggerForID(cfg.AdapterID)
	} else {
		debugger, ok = builtInLanguagePackDebuggerForPath(absProgram)
	}
	if !ok || debugger.Protocol != "dap" || debugger.SourcePackID == "" || debugger.SourcePackID == "org.koyori.ide.go" {
		if cfg.AdapterID != "" {
			return DebugSessionInfo{}, fmt.Errorf("no active external stdio DAP debugger is declared for adapter %q", cfg.AdapterID)
		}
		return DebugSessionInfo{}, fmt.Errorf("no external stdio DAP debugger is declared for %s", filepath.Base(absProgram))
	}
	adapterPath, err := exec.LookPath(debugger.Executable)
	if err != nil {
		return DebugSessionInfo{}, fmt.Errorf("%s not found on PATH: %s", debugger.Executable, debugger.InstallHint)
	}
	canonicalAdapter, err := canonicalDebugAdapterPath(adapterPath, false)
	if err != nil {
		return DebugSessionInfo{}, fmt.Errorf("resolve language-pack debug adapter: %w", err)
	}
	canonicalCwd, err := canonicalDebugAdapterPath(absCwd, true)
	if err != nil {
		return DebugSessionInfo{}, fmt.Errorf("resolve language-pack debug cwd: %w", err)
	}
	if debugAdapterPathWithin(canonicalCwd, canonicalAdapter) {
		return DebugSessionInfo{}, errors.New("language-pack debug adapter must not resolve from the workspace")
	}

	if err := d.Stop(); err != nil {
		return DebugSessionInfo{}, fmt.Errorf("stop previous debug session: %w", err)
	}
	adapter, err := startDebugAdapter(context.Background(), debugAdapterLaunchPolicy{
		WorkspaceRoot:      canonicalCwd,
		AllowedExecutables: []string{canonicalAdapter},
	}, debugAdapterLaunchRequest{
		Executable: canonicalAdapter,
		Args:       append([]string(nil), debugger.Args...),
		Cwd:        canonicalCwd,
	})
	if err != nil {
		return DebugSessionInfo{}, fmt.Errorf("start language-pack debug adapter: %w", err)
	}
	conn := newStdioDAPConn(adapter.Stdout, adapter.Stdin)
	stderrCapture := &boundedLanguagePackAdapterStderr{}
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderrCapture, adapter.Stderr)
		_ = adapter.Stderr.Close()
		close(stderrDone)
	}()

	owner := d.activeSession()
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.cmd = adapter.Cmd
	owner.running = true
	owner.addr = "stdio"
	owner.mode = languagePackDebugKind
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
	owner.cwd = canonicalCwd
	breakpoints := append([]DebugBreakpoint(nil), owner.breakpoints...)
	functionBreakpoints := append([]FunctionBreakpoint(nil), owner.functionBreakpoints...)
	owner.mu.Unlock()

	d.startDAPReadLoop(owner, generation, conn, readerDone, readerDoneOnce, readerWG)
	go func() {
		owner.waitForProcessExitTracked(adapter.Cmd, generation, processDone, processDoneOnce)
		slog.Info("language-pack DAP adapter exited", "adapter", debugger.ID)
	}()
	request := func(command string, args map[string]interface{}) (json.RawMessage, error) {
		return d.dapRequestBodyForRun(owner, generation, conn, command, args)
	}
	fail := func(label string, cause error) (DebugSessionInfo, error) {
		owner.finishDAPRun(generation, conn)
		select {
		case <-stderrDone:
		case <-time.After(250 * time.Millisecond):
		}
		if stderr := stderrCapture.String(); stderr != "" {
			return DebugSessionInfo{}, fmt.Errorf("%s: %w; adapter stderr: %s", label, cause, stderr)
		}
		return DebugSessionInfo{}, fmt.Errorf("%s: %w", label, cause)
	}
	if err := initializeDAPSessionForRunWithAdapter(owner, generation, request, debugger.ID); err != nil {
		return fail("dap initialize", err)
	}
	launchArgs := map[string]interface{}{
		"request":     "launch",
		"program":     absProgram,
		"cwd":         canonicalCwd,
		"stopOnEntry": cfg.StopEntry,
	}
	if len(cfg.Args) > 0 {
		launchArgs["args"] = append([]string(nil), cfg.Args...)
	}
	if len(cfg.Env) > 0 {
		launchArgs["env"] = cfg.Env
	}
	type dapLaunchResult struct{ err error }
	launchResult := make(chan dapLaunchResult, 1)
	go func() {
		_, launchErr := request("launch", launchArgs)
		launchResult <- dapLaunchResult{err: launchErr}
	}()
	launchCompleted := false
	owner.mu.Lock()
	initialized := owner.dapInitialized
	owner.mu.Unlock()
	select {
	case <-initialized:
	case result := <-launchResult:
		if result.err != nil {
			return fail("dap launch", result.err)
		}
		launchCompleted = true
	case <-time.After(10 * time.Second):
		return fail("dap launch", errors.New("adapter did not initialize"))
	}
	if err := d.applyAllBreakpointsForRun(owner, generation, conn, breakpoints); err != nil && !dapRunCurrent(owner, generation, conn) {
		return DebugSessionInfo{}, err
	}
	if len(functionBreakpoints) > 0 {
		if _, err := request("setFunctionBreakpoints", map[string]interface{}{"breakpoints": functionBreakpoints}); err != nil && !dapRunCurrent(owner, generation, conn) {
			return DebugSessionInfo{}, err
		}
	}
	if _, err := request("configurationDone", map[string]interface{}{}); err != nil && !dapRunCurrent(owner, generation, conn) {
		return DebugSessionInfo{}, err
	}
	if !launchCompleted {
		select {
		case result := <-launchResult:
			if result.err != nil {
				return fail("dap launch", result.err)
			}
		case <-time.After(10 * time.Second):
			return fail("dap launch", errors.New("adapter did not finish launch after configuration"))
		}
	}

	owner.mu.Lock()
	if owner.runGeneration != generation || owner.conn != conn {
		owner.mu.Unlock()
		return DebugSessionInfo{}, errors.New("debug run changed")
	}
	owner.lastLaunch = debugLaunchSpec{
		Kind: languagePackDebugKind, AdapterID: debugger.ID, Program: absProgram, Dir: canonicalCwd,
		Args: append([]string(nil), cfg.Args...), Env: cfg.Env, StopEntry: cfg.StopEntry,
	}
	info := dapSessionInfoLocked(owner)
	owner.mu.Unlock()
	return info, nil
}
