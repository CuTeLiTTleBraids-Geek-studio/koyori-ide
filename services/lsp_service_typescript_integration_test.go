package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLSPServiceRealTypeScriptWorkspaceLocalServer(t *testing.T) {
	if testing.Short() {
		t.Skip("real TypeScript LSP integration is skipped in short mode")
	}
	serviceDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	frontendNodeModules := filepath.Clean(filepath.Join(serviceDir, "..", "frontend", "node_modules"))
	serverShim := filepath.Join(frontendNodeModules, ".bin", "typescript-language-server")
	if runtime.GOOS == "windows" {
		serverShim += ".cmd"
	}
	for _, required := range []string{
		serverShim,
		filepath.Join(frontendNodeModules, "typescript", "lib", "tsserver.js"),
	} {
		if info, statErr := os.Stat(required); statErr != nil || info.IsDir() {
			t.Skipf("real TypeScript LSP dependency is unavailable: %s", required)
		}
	}

	workspace := t.TempDir()
	nodeModulesLink := filepath.Join(workspace, "node_modules")
	if err := os.Symlink(frontendNodeModules, nodeModulesLink); err != nil {
		if runtime.GOOS != "windows" {
			t.Skipf("workspace-local node_modules link is unavailable: %v", err)
		}
		output, junctionErr := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", nodeModulesLink, frontendNodeModules).CombinedOutput()
		if junctionErr != nil {
			t.Skipf("workspace-local node_modules junction is unavailable: %v (%s)", junctionErr, output)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "package.json"), []byte("{\"name\":\"koyori-lsp-integration\",\"private\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "tsconfig.json"), []byte("{\"compilerOptions\":{\"strict\":true,\"target\":\"ES2022\"},\"include\":[\"index.ts\"]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(workspace, "index.ts")
	content := "export const answer: number = 42;\nanswer.toF\n"
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	workspaceContext := NewWorkspaceContext()
	if err := workspaceContext.Set(workspace); err != nil {
		t.Fatal(err)
	}
	service := NewLSPServiceWithWorkspaceContext(workspaceContext)
	t.Cleanup(service.StopAll)
	service.setWorkspaceRoot(workspace)
	if _, err := exec.LookPath("gopls"); err == nil {
		if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module koyori-lsp-integration\n\ngo 1.25.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := service.StartLSPServer("go"); err != nil {
			t.Fatalf("start concurrent real gopls: %v", err)
		}
	}
	started := time.Now()
	if err := service.StartLSPServer("typescript"); err != nil {
		t.Fatalf("start real TypeScript LSP: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("real TypeScript LSP startup exceeded bound: %s", elapsed)
	}

	items, err := service.GetCompletions(LSPCompletionRequest{
		Language: "typescript", FilePath: filePath, Content: content, Line: 1, Column: 10,
	})
	if err != nil {
		t.Fatalf("real TypeScript completion: %v", err)
	}
	status := service.GetCallStatus("typescript")
	if status.Code != "ok" {
		t.Fatalf("real TypeScript completion status = %+v", status)
	}
	if len(items) == 0 {
		t.Fatal("real TypeScript LSP returned no completions")
	}
	if err := service.StopLSPServer("typescript"); err != nil {
		t.Fatalf("stop real TypeScript LSP: %v", err)
	}
}
