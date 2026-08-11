package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLanguagePackRealPythonLSPToolchainAndDebug(t *testing.T) {
	if testing.Short() {
		t.Skip("real Python language-pack integration is skipped in short mode")
	}
	required := []string{"python", "pyright", "pyright-langserver", "debugpy-adapter"}
	for _, executable := range required {
		if _, err := exec.LookPath(executable); err != nil {
			t.Skipf("real Python language-pack dependency is unavailable: %s", executable)
		}
	}
	pyrightPath, _ := exec.LookPath("pyright")
	if version := tryVersion(pyrightPath, "--version"); !strings.Contains(version, "1.1.411") {
		t.Skipf("real Python language-pack test requires pyright 1.1.411, got %q", version)
	}

	defer setActiveExternalLanguagePacks(nil)
	configRoot := t.TempDir()
	packService := NewLanguagePackService(configRoot)
	trustLanguagePackFixturePublisher(t, packService)
	archive := writeLanguagePackFixture(t, configRoot, "org.example.python", "1.0.0", "python-check")
	installed, err := InstallLanguagePackFromTrustedPath(packService, archive)
	if err != nil {
		t.Fatalf("install signed Python language pack: %v", err)
	}
	if installed.ID != "org.example.python" || installed.Version != "1.0.0" || !installed.Active {
		t.Fatalf("unexpected installed Python pack: %+v", installed)
	}

	parent := t.TempDir()
	rootA := filepath.Join(parent, "python-a")
	rootB := filepath.Join(parent, "python-b")
	for _, root := range []string{rootA, rootB} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "pyrightconfig.json"), []byte("{\"typeCheckingMode\":\"strict\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fileA := filepath.Join(rootA, "main.py")
	contentA := "message: str = 'hello'\nmessage.\n"
	if err := os.WriteFile(fileA, []byte(contentA), 0o600); err != nil {
		t.Fatal(err)
	}
	fileB := filepath.Join(rootB, "other.py")
	contentB := "count: int = 42\ncount.bit_length()\n"
	if err := os.WriteFile(fileB, []byte(contentB), 0o600); err != nil {
		t.Fatal(err)
	}
	const largeWorkspaceFiles = 5000
	generatedDir := filepath.Join(rootA, "generated")
	if err := os.MkdirAll(generatedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < largeWorkspaceFiles; index++ {
		path := filepath.Join(generatedDir, fmt.Sprintf("module_%04d.py", index))
		if err := os.WriteFile(path, []byte("value: int = 42\n"), 0o600); err != nil {
			t.Fatalf("write large-workspace fixture %d: %v", index, err)
		}
	}

	workspaceContext := NewWorkspaceContext()
	if err := workspaceContext.SetRoots([]string{rootA, rootB}); err != nil {
		t.Fatal(err)
	}
	lsp := NewLSPServiceWithWorkspaceContext(workspaceContext)
	lsp.setWorkspaceRoots(workspaceContext.State().Roots)
	var pythonStatus LSPServerStatus
	for _, status := range lsp.DetectAllLSPServers() {
		if status.Language == "python" {
			pythonStatus = status
			break
		}
	}
	if !pythonStatus.Available || !strings.Contains(pythonStatus.Version, "1.1.411") ||
		pythonStatus.SourcePackID != installed.ID || pythonStatus.SourcePackVersion != installed.Version {
		t.Fatalf("real Python LSP detection/version pin failed: %+v", pythonStatus)
	}
	largeWorkspaceStarted := time.Now()
	if err := lsp.StartLSPServer("python"); err != nil {
		t.Fatalf("start real Pyright LSP: %v", err)
	}
	t.Cleanup(lsp.StopAll)
	completions, err := lsp.GetCompletions(LSPCompletionRequest{
		Language: "python", FilePath: fileA, Content: contentA, Line: 1, Column: len("message."),
	})
	if err != nil || len(completions) == 0 {
		t.Fatalf("real Pyright completion failed: count=%d err=%v", len(completions), err)
	}
	if elapsed := time.Since(largeWorkspaceStarted); elapsed > 30*time.Second {
		t.Fatalf("real Pyright exceeded the 5,000-file workspace budget: %v", elapsed)
	}
	hover, err := lsp.GetHover(LSPCompletionRequest{
		Language: "python", FilePath: fileB, Content: contentB, Line: 0, Column: 2,
	})
	if err != nil || strings.TrimSpace(hover) == "" {
		t.Fatalf("real Pyright multi-root hover failed: hover=%q err=%v", hover, err)
	}
	if err := os.WriteFile(fileA, []byte("message: str = 'hello'\nmessage.upper()\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	toolchain := NewToolchainServiceWithWorkspaceContext(workspaceContext)
	toolResult, err := toolchain.RunToolchainCommand("python-check", fileA)
	if err != nil || !toolResult.Success || toolResult.NotInstalled {
		t.Fatalf("real Python toolchain command failed: result=%+v err=%v", toolResult, err)
	}

	debugProgram := filepath.Join(rootA, "debug_fixture.py")
	debugContent := "def main():\n    answer = 42\n    print(answer)\n\nmain()\n"
	if err := os.WriteFile(debugProgram, []byte(debugContent), 0o600); err != nil {
		t.Fatal(err)
	}
	debug := NewDebugServiceWithWorkspaceContext(workspaceContext)
	debug.approveProjectExecutable = func(kind, path string) bool {
		return kind == "program" && sameWorkspaceIdentityPath(path, debugProgram)
	}
	t.Cleanup(func() { _ = debug.Stop() })
	if _, err := debug.SetBreakpoint(debugProgram, 3); err != nil {
		t.Fatalf("set Python breakpoint: %v", err)
	}
	debugInfo, err := debug.LaunchWithConfig(DebugLaunchConfig{
		Kind: languagePackDebugKind, Program: debugProgram, Dir: rootA,
	})
	if err != nil {
		t.Fatalf("launch real debugpy language-pack adapter: %v", err)
	}
	if !debugInfo.Running || debugInfo.AdapterID != "debugpy" || debugInfo.SourcePackID != installed.ID ||
		debugInfo.SourcePackVersion != installed.Version || debugInfo.Address != "stdio" {
		t.Fatalf("real debugpy source metadata missing: %+v", debugInfo)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		state := debug.GetState()
		if state.Session.Stopped && len(state.Stack) > 0 {
			for _, variable := range state.Locals {
				if variable.Name == "answer" && variable.Value == "42" {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("real debugpy did not expose breakpoint locals: %+v", debug.GetState())
}
