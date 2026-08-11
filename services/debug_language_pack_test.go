package services

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const languagePackDAPHelperArg = "--koyori-language-pack-dap-helper"
const languagePackDAPCrashArg = "--koyori-language-pack-dap-crash"

func TestLanguagePackDAPHelperProcess(t *testing.T) {
	if !containsTestArgument(os.Args, languagePackDAPHelperArg) {
		return
	}
	serveLanguagePackDAPHelper()
	os.Exit(0)
}

func containsTestArgument(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func serveLanguagePackDAPHelper() {
	if containsTestArgument(os.Args, languagePackDAPCrashArg) {
		_, _ = os.Stderr.Write([]byte("adapter-crash: "))
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), maxLanguagePackAdapterStderrBytes*4))
		return
	}
	reader := bufio.NewReader(os.Stdin)
	sequence := 1
	var pendingLaunch *dapMessage
	write := func(payload map[string]interface{}) {
		payload["seq"] = sequence
		sequence++
		_ = writeDAPMessage(os.Stdout, payload)
	}
	for {
		message, err := readDAPMessage(reader)
		if err != nil {
			return
		}
		body := map[string]interface{}{}
		switch message.Command {
		case "initialize":
			body = map[string]interface{}{"supportsConfigurationDoneRequest": true}
		case "threads":
			body = map[string]interface{}{"threads": []interface{}{map[string]interface{}{"id": 1, "name": "main"}}}
		case "stackTrace":
			body = map[string]interface{}{"stackFrames": []interface{}{map[string]interface{}{
				"id": 1, "name": "main", "line": 1, "column": 1,
				"source": map[string]interface{}{"name": "main.kpy", "path": "main.kpy"},
			}}, "totalFrames": 1}
		case "scopes":
			body = map[string]interface{}{"scopes": []interface{}{map[string]interface{}{
				"name": "Locals", "variablesReference": 10, "expensive": false,
			}}}
		case "variables":
			body = map[string]interface{}{"variables": []interface{}{map[string]interface{}{
				"name": "answer", "value": "42", "type": "int", "variablesReference": 0,
			}}}
		}
		if message.Command == "launch" {
			copy := message
			pendingLaunch = &copy
			write(map[string]interface{}{"type": "event", "event": "initialized", "body": map[string]interface{}{}})
			continue
		}
		write(map[string]interface{}{
			"type": "response", "request_seq": message.Seq, "success": true,
			"command": message.Command, "body": body,
		})
		if message.Command == "configurationDone" {
			if pendingLaunch != nil {
				write(map[string]interface{}{
					"type": "response", "request_seq": pendingLaunch.Seq, "success": true,
					"command": pendingLaunch.Command, "body": map[string]interface{}{},
				})
				pendingLaunch = nil
			}
			write(map[string]interface{}{
				"type": "event", "event": "stopped",
				"body": map[string]interface{}{"reason": "entry", "threadId": 1, "allThreadsStopped": true},
			})
		}
		if message.Command == "disconnect" {
			return
		}
	}
}

func externalDAPTestManifest(executable string, args []string) languagePackManifest {
	return languagePackManifest{
		ID: "org.example.kdebug", Version: "1.0.0",
		Languages: []languagePackLanguage{{ID: "kdebug", Extensions: []string{".kpy"}, Filenames: []string{}}},
		Debuggers: []languagePackDebugger{{
			ID: "kdebug-adapter", Protocol: "dap", Languages: []string{"kdebug"},
			Executable: executable, Args: args, InstallHint: "Install the kdebug adapter",
		}},
	}
}

func TestDebugServiceLaunchesExternalLanguagePackOverStdioDAP(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	setActiveExternalLanguagePacks([]languagePackManifest{externalDAPTestManifest(
		filepath.Base(executable),
		[]string{"-test.run=TestLanguagePackDAPHelperProcess", "--", languagePackDAPHelperArg},
	)})
	workspace := t.TempDir()
	program := filepath.Join(workspace, "compiled-fixture.bin")
	if err := os.WriteFile(program, []byte("answer = 42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := NewDebugService()
	t.Cleanup(func() { _ = d.Stop() })
	info, err := d.LaunchWithConfig(DebugLaunchConfig{
		Kind: languagePackDebugKind, AdapterID: "kdebug-adapter", Program: program, Dir: workspace, Args: []string{"--fixture"},
	})
	if err != nil {
		t.Fatalf("launch external language-pack DAP: %v", err)
	}
	if !info.Running || info.AdapterID != "kdebug-adapter" || info.SourcePackID != "org.example.kdebug" ||
		info.SourcePackVersion != "1.0.0" || info.Address != "stdio" {
		t.Fatalf("unexpected language-pack debug session: %+v", info)
	}
	owner := d.activeSession()
	owner.mu.Lock()
	restartAdapterID := owner.lastLaunch.AdapterID
	owner.mu.Unlock()
	if restartAdapterID != "kdebug-adapter" {
		t.Fatalf("language-pack restart lost adapter id: %q", restartAdapterID)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state := d.GetState()
		if state.Session.Stopped && len(state.Stack) == 1 && len(state.Locals) == 1 {
			if state.Locals[0].Name != "answer" || state.Locals[0].Value != "42" {
				t.Fatalf("unexpected language-pack locals: %+v", state.Locals)
			}
			if err := d.Stop(); err != nil {
				t.Fatalf("stop language-pack DAP: %v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("external language-pack DAP did not expose stopped stack/locals: %+v", d.GetState())
}

func TestDebugServiceRejectsUnknownLanguagePackAdapterIDWithoutPathFallback(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	setActiveExternalLanguagePacks([]languagePackManifest{externalDAPTestManifest("unused-adapter", nil)})
	workspace := t.TempDir()
	program := filepath.Join(workspace, "main.kpy")
	if err := os.WriteFile(program, []byte("answer = 42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := NewDebugService()
	_, err := d.LaunchWithConfig(DebugLaunchConfig{
		Kind: languagePackDebugKind, AdapterID: "missing-adapter", Program: program, Dir: workspace,
	})
	if err == nil || !strings.Contains(err.Error(), `adapter "missing-adapter"`) {
		t.Fatalf("unknown adapter id rejection = %v", err)
	}
	if state := d.GetState(); state.Session.Running {
		t.Fatalf("unknown adapter id left a running session: %+v", state)
	}
}

func TestDebugServiceBoundsLanguagePackAdapterCrashStderr(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	setActiveExternalLanguagePacks([]languagePackManifest{externalDAPTestManifest(
		filepath.Base(executable),
		[]string{"-test.run=TestLanguagePackDAPHelperProcess", "--", languagePackDAPHelperArg, languagePackDAPCrashArg},
	)})
	workspace := t.TempDir()
	program := filepath.Join(workspace, "compiled-fixture.bin")
	if err := os.WriteFile(program, []byte("answer = 42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := NewDebugService()
	_, err = d.LaunchWithConfig(DebugLaunchConfig{
		Kind: languagePackDebugKind, AdapterID: "kdebug-adapter", Program: program, Dir: workspace,
	})
	if err == nil || !strings.Contains(err.Error(), "adapter-crash:") ||
		!strings.Contains(err.Error(), "[adapter stderr truncated]") {
		t.Fatalf("bounded adapter crash error = %v", err)
	}
	if len(err.Error()) > maxLanguagePackAdapterStderrBytes+1024 {
		t.Fatalf("adapter crash error was not bounded: %d bytes", len(err.Error()))
	}
	if state := d.GetState(); state.Session.Running {
		t.Fatalf("crashed adapter left a running session: %+v", state)
	}
}

func TestDebugServiceRejectsWorkspaceResolvedLanguagePackAdapter(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	workspace := t.TempDir()
	program := filepath.Join(workspace, "main.kpy")
	if err := os.WriteFile(program, []byte("answer = 42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	adapterName := "workspace-kdebug-adapter"
	if runtime.GOOS == "windows" {
		adapterName += ".exe"
	}
	adapterPath := filepath.Join(workspace, adapterName)
	destination, err := os.OpenFile(adapterPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", workspace+string(os.PathListSeparator)+os.Getenv("PATH"))
	setActiveExternalLanguagePacks([]languagePackManifest{externalDAPTestManifest(adapterName, nil)})
	d := NewDebugService()
	_, err = d.LaunchWithConfig(DebugLaunchConfig{Kind: languagePackDebugKind, Program: program, Dir: workspace})
	if err == nil || !strings.Contains(err.Error(), "must not resolve from the workspace") {
		t.Fatalf("workspace adapter rejection = %v", err)
	}
	if state := d.GetState(); state.Session.Running {
		t.Fatalf("workspace adapter rejection left a running session: %+v", state)
	}
}
