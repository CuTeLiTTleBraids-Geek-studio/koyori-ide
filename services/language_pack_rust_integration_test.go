package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLanguagePackRealRustLSPToolchainAndDebug(t *testing.T) {
	if testing.Short() {
		t.Skip("real Rust language-pack integration is skipped in short mode")
	}
	if runtime.GOOS == "windows" {
		t.Setenv("RUSTUP_TOOLCHAIN", "1.97.1-x86_64-pc-windows-gnu")
	}
	required := []string{"rustc", "cargo", "rust-analyzer"}
	paths := make(map[string]string, len(required))
	for _, executable := range required {
		path, err := exec.LookPath(executable)
		if err != nil {
			t.Skipf("real Rust language-pack dependency is unavailable: %s", executable)
		}
		paths[executable] = path
	}
	if version := tryVersion(paths["rust-analyzer"], "--version"); !strings.Contains(version, "1.97.1") {
		t.Skipf("real Rust language-pack test requires rust-analyzer 1.97.1, got %q", version)
	}
	if version := tryVersion(paths["rustc"], "--version"); !strings.Contains(version, "1.97.1") {
		t.Skipf("real Rust language-pack test requires rustc 1.97.1, got %q", version)
	}

	defer setActiveExternalLanguagePacks(nil)
	configRoot := t.TempDir()
	packService := NewLanguagePackService(configRoot)
	trustLanguagePackFixturePublisher(t, packService)
	archive := writeLanguagePackFixtureForLanguage(t, configRoot, languagePackFixture{
		id:                   "org.example.rust",
		version:              "1.0.0",
		displayName:          "Example Rust",
		language:             "rust",
		extension:            ".rs",
		rootMarker:           "Cargo.toml",
		serverID:             "rust-analyzer",
		serverOrder:          30,
		serverExecutable:     "rust-analyzer",
		serverKind:           "rust-analyzer",
		serverArgs:           []interface{}{},
		versionExecutable:    "rust-analyzer",
		versionPin:           "1.97.1",
		configurationSection: "rust-analyzer",
		debuggerID:           "lldb",
		debuggerExecutable:   "lldb-dap",
		debuggerArgs:         []interface{}{},
		debuggerInstallHint:  "Install LLVM 22.1.8",
		commandID:            "rust-check",
		commandLabel:         "Rust: Check",
		commandExecutable:    "cargo",
		commandArgs:          []interface{}{"check"},
		commandDescription:   "Check the Rust workspace",
		toolInstallHint:      "Install Rust 1.97.1",
	})
	installed, err := InstallLanguagePackFromTrustedPath(packService, archive)
	if err != nil {
		t.Fatalf("install signed Rust language pack: %v", err)
	}

	parent := t.TempDir()
	rootA := filepath.Join(parent, "rust-a")
	rootB := filepath.Join(parent, "rust-b")
	fileA := writeRustIntegrationProject(t, rootA, "rust_a", "fn main() {\n    let message = String::from(\"hello\");\n    println!(\"{message}\");\n}\n")
	fileB := writeRustIntegrationProject(t, rootB, "rust_b", "fn main() {\n    let count: u32 = 42;\n    println!(\"{count}\");\n}\n")

	workspaceContext := NewWorkspaceContext()
	if err := workspaceContext.SetRoots([]string{rootA, rootB}); err != nil {
		t.Fatal(err)
	}
	lsp := NewLSPServiceWithWorkspaceContext(workspaceContext)
	lsp.setWorkspaceRoots(workspaceContext.State().Roots)
	rustAnalyzerLog := filepath.Join(parent, "rust-analyzer.log")
	t.Setenv("RA_LOG", "rust_analyzer=info")
	t.Setenv("RA_LOG_FILE", rustAnalyzerLog)
	var rustStatus LSPServerStatus
	for _, status := range lsp.DetectAllLSPServers() {
		if status.Language == "rust" {
			rustStatus = status
			break
		}
	}
	if !rustStatus.Available || !strings.Contains(rustStatus.Version, "1.97.1") ||
		rustStatus.SourcePackID != installed.ID || rustStatus.SourcePackVersion != installed.Version {
		t.Fatalf("real Rust LSP detection/version pin failed: %+v", rustStatus)
	}
	if err := lsp.StartLSPServer("rust"); err != nil {
		t.Fatalf("start real rust-analyzer LSP: %v", err)
	}
	t.Cleanup(lsp.StopAll)
	contentB, err := os.ReadFile(fileB)
	if err != nil {
		t.Fatal(err)
	}
	hoverRequest := LSPCompletionRequest{
		Language: "rust", FilePath: fileB, Content: string(contentB), Line: 1, Column: len("    let count: u"),
	}
	var hover string
	hoverDeadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(hoverDeadline) {
		hover, err = lsp.GetHover(hoverRequest)
		if err == nil && strings.TrimSpace(hover) != "" {
			break
		}
		// rust-analyzer can answer with transient LSP -32801 "content modified"
		// while it is still loading workspaces after startup. Retry until the
		// deadline instead of treating that startup race as a hard failure. Cold
		// starts under CI/machine load can exceed 20s before typed hovers resolve.
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || strings.TrimSpace(hover) == "" {
		rawHover := "unavailable"
		if srv, syncErr := lsp.syncDocument(hoverRequest); syncErr != nil {
			rawHover = "sync error: " + syncErr.Error()
		} else if srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
			raw, requestErr := srv.client.request(ctx, "textDocument/hover", map[string]interface{}{
				"textDocument": map[string]string{"uri": pathToURI(hoverRequest.FilePath)},
				"position": map[string]int{
					"line":      hoverRequest.Line,
					"character": hoverRequest.Column,
				},
			})
			cancel()
			if requestErr != nil {
				rawHover = "request error: " + requestErr.Error()
			} else {
				rawHover = string(raw)
			}
		}
		logData, _ := os.ReadFile(rustAnalyzerLog)
		if len(logData) > 16<<10 {
			logData = logData[len(logData)-(16<<10):]
		}
		t.Fatalf("real rust-analyzer multi-root hover failed: hover=%q err=%v raw=%s status=%+v log=%s", hover, err, rawHover, lsp.DetectAllLSPServers(), logData)
	}

	contentA := "fn main() {\n    let message = String::from(\"hello\");\n    message.le\n}\n"
	completionRequest := LSPCompletionRequest{
		Language: "rust", FilePath: fileA, Content: contentA, Line: 2, Column: len("    message.le"),
		TriggerKind: 1,
	}
	var completions []LSPCompletionItem
	completionDeadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(completionDeadline) {
		completions, err = lsp.GetCompletions(completionRequest)
		if err == nil && len(completions) > 0 {
			break
		}
		// See the hover retry above: transient content-modified errors during
		// rust-analyzer startup must not fail the real-toolchain assertion.
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || len(completions) == 0 {
		t.Fatalf("real rust-analyzer completion failed: count=%d err=%v triggers=%v status=%+v", len(completions), err, lsp.GetTriggerCharacters("rust"), lsp.DetectAllLSPServers())
	}

	debugContent := "fn main() {\n    let answer: i32 = 42;\n    println!(\"{answer}\");\n}\n"
	if err := os.WriteFile(fileA, []byte(debugContent), 0o600); err != nil {
		t.Fatal(err)
	}
	toolchain := NewToolchainServiceWithWorkspaceContext(workspaceContext)
	toolResult, err := toolchain.RunToolchainCommand("rust-check", fileA)
	if err != nil || !toolResult.Success || toolResult.NotInstalled {
		t.Fatalf("real Rust toolchain command failed: result=%+v err=%v", toolResult, err)
	}
	build := exec.Command(paths["cargo"], "build", "--quiet")
	build.Dir = rootA
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Rust debug fixture: %v\n%s", err, output)
	}
	programName := "rust_a"
	if runtime.GOOS == "windows" {
		programName += ".exe"
	}
	debugProgram := filepath.Join(rootA, "target", "debug", programName)

	t.Run("lldb dap", func(t *testing.T) {
		lldbPath, err := exec.LookPath("lldb-dap")
		if err != nil {
			t.Skip("real Rust language-pack debugger dependency is unavailable: lldb-dap")
		}
		if version := lldbDAPVersionEvidence(lldbPath); !strings.Contains(version, "22.1.8") {
			t.Skipf("real Rust language-pack test requires lldb-dap 22.1.8, got %q", version)
		}

		debug := NewDebugServiceWithWorkspaceContext(workspaceContext)
		debug.approveProjectExecutable = func(kind, path string) bool {
			return kind == "program" && sameWorkspaceIdentityPath(path, debugProgram)
		}
		t.Cleanup(func() { _ = debug.Stop() })
		if _, err := debug.SetBreakpoint(fileA, 3); err != nil {
			t.Fatalf("set Rust breakpoint: %v", err)
		}
		debugInfo, err := debug.LaunchWithConfig(DebugLaunchConfig{
			Kind: languagePackDebugKind, AdapterID: "lldb", Program: debugProgram, Dir: rootA,
		})
		if err != nil {
			t.Fatalf("launch real lldb-dap language-pack adapter: %v", err)
		}
		if !debugInfo.Running || debugInfo.AdapterID != "lldb" || debugInfo.SourcePackID != installed.ID ||
			debugInfo.SourcePackVersion != installed.Version || debugInfo.Address != "stdio" {
			t.Fatalf("real lldb-dap source metadata missing: %+v", debugInfo)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			state := debug.GetState()
			if state.Session.Stopped && len(state.Stack) > 0 {
				for _, variable := range state.Locals {
					if variable.Name == "answer" && strings.Contains(variable.Value, "42") {
						return
					}
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("real lldb-dap did not expose breakpoint locals: %+v", debug.GetState())
	})
}

func lldbDAPVersionEvidence(lldbPath string) string {
	versions := make([]string, 0, 3)
	if version := strings.TrimSpace(tryVersion(lldbPath, "--version")); version != "" {
		versions = append(versions, version)
	}
	for _, name := range []string{"clang", "llvm-config"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path := filepath.Join(filepath.Dir(lldbPath), name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			if version := strings.TrimSpace(tryVersion(path, "--version")); version != "" {
				versions = append(versions, version)
			}
		}
	}
	return strings.Join(versions, "\n")
}

func writeRustIntegrationProject(t *testing.T, root, packageName, content string) string {
	t.Helper()
	sourceDir := filepath.Join(root, "src")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := "[package]\nname = \"" + packageName + "\"\nversion = \"0.1.0\"\nedition = \"2024\"\n"
	if err := os.WriteFile(filepath.Join(root, "Cargo.toml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sourceDir, "main.rs")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
