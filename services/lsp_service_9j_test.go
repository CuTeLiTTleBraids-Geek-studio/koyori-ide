package services

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLSP9J_FrameworkProjectDetection(t *testing.T) {
	tests := []struct {
		name        string
		deps        map[string]string
		config      string
		wantVue     bool
		wantAngular bool
		wantReact   bool
	}{
		{name: "vue", deps: map[string]string{"vue": "^3.5.0"}, wantVue: true},
		{name: "vue config", config: "vue.config.js", wantVue: true},
		{name: "angular", deps: map[string]string{"@angular/core": "^20.0.0"}, config: "angular.json", wantAngular: true},
		{name: "angular dependency without workspace config", deps: map[string]string{"@angular/core": "^20.0.0"}},
		{name: "react", deps: map[string]string{"react": "^19.0.0"}, wantReact: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeLSP9JPackageJSON(t, root, tt.deps, nil)
			if tt.config != "" {
				writeLSP9JFile(t, filepath.Join(root, tt.config), "{}")
			}
			got := detectWorkspaceFrameworks(root)
			if got.Vue != tt.wantVue || got.Angular != tt.wantAngular || got.React != tt.wantReact {
				t.Fatalf("detectWorkspaceFrameworks() = %+v", got)
			}
		})
	}
}

func TestLSP9J_DetectsWorkspaceLocalFrameworkServers(t *testing.T) {
	vueRoot := t.TempDir()
	writeLSP9JPackageJSON(t, vueRoot, map[string]string{"vue": "^3.5.0"}, map[string]string{
		"@vue/language-server": "^2.2.0",
		"typescript":           "^5.9.0",
	})
	writeLSP9JExecutable(t, vueRoot, "vue-language-server")
	writeLSP9JFile(t, filepath.Join(vueRoot, "node_modules", "typescript", "lib", "tsserverlibrary.js"), "")

	statuses := NewLSPService(vueRoot).DetectLSPServers()
	vue := lsp9JStatus(t, statuses, "vue")
	if !vue.Available || vue.ServerKind != "vue-language-server" || vue.Framework != "vue" {
		t.Fatalf("Vue status = %+v", vue)
	}
	if lsp9JHasStatus(statuses, "angular") {
		t.Fatal("Angular status was exposed for a Vue workspace")
	}
}

func TestLSP9J_AngularOnlyAppliesToAngularWorkspaces(t *testing.T) {
	nonAngular := t.TempDir()
	writeLSP9JExecutable(t, nonAngular, "ngserver")
	if lsp9JHasStatus(NewLSPService(nonAngular).DetectLSPServers(), "angular") {
		t.Fatal("Angular server was exposed outside an Angular project")
	}
	if err := NewLSPService(nonAngular).StartLSPServer("angular"); err == nil || !strings.Contains(err.Error(), "Angular project") {
		t.Fatalf("StartLSPServer(angular) error = %v, want project guidance", err)
	}

	angularRoot := t.TempDir()
	writeLSP9JPackageJSON(t, angularRoot, map[string]string{"@angular/core": "^20.0.0"}, map[string]string{
		"@angular/language-server": "^20.0.0",
		"typescript":               "^5.9.0",
	})
	writeLSP9JFile(t, filepath.Join(angularRoot, "angular.json"), "{}")
	writeLSP9JExecutable(t, angularRoot, "ngserver")
	angular := lsp9JStatus(t, NewLSPService(angularRoot).DetectLSPServers(), "angular")
	if !angular.Available || angular.ServerKind != "angular-language-server" || angular.Framework != "angular" {
		t.Fatalf("Angular status = %+v", angular)
	}
}

func TestLSP9J_FrameworkServerArgv(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		language string
		kind     string
		exe      string
		wantArgs []string
	}{
		{"vue", "vue-language-server", "volar", []string{"volar", "--stdio"}},
		{"angular", "angular-language-server", "ngserver", []string{
			"ngserver",
			"--stdio",
			"--tsProbeLocations", filepath.Join(root, "node_modules"),
			"--ngProbeLocations", filepath.Join(root, "node_modules"),
			"--includeAutomaticOptionalChainCompletions",
			"--includeCompletionsWithSnippetText",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.language, func(t *testing.T) {
			cmd, err := buildLSPServerCommand(tt.language, tt.exe, tt.kind, root)
			if err != nil {
				t.Fatalf("buildLSPServerCommand: %v", err)
			}
			if !reflect.DeepEqual(cmd.Args, tt.wantArgs) {
				t.Fatalf("argv = %#v, want %#v", cmd.Args, tt.wantArgs)
			}
			if cmd.Dir != root {
				t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, root)
			}
		})
	}
}

func TestLSP9J_VueInitializationUsesWorkspaceTypeScriptSDK(t *testing.T) {
	root := t.TempDir()
	tsSDK := filepath.Join(root, "node_modules", "typescript", "lib")
	writeLSP9JFile(t, filepath.Join(tsSDK, "tsserverlibrary.js"), "")
	params := captureLSPInitializeParams(t, "vue", root)
	options, ok := params["initializationOptions"].(map[string]interface{})
	if !ok {
		t.Fatalf("initializationOptions = %#v", params["initializationOptions"])
	}
	typescript, ok := options["typescript"].(map[string]interface{})
	if !ok || typescript["tsdk"] != tsSDK {
		t.Fatalf("typescript initialization options = %#v, want tsdk %q", options["typescript"], tsSDK)
	}
	vue, ok := options["vue"].(map[string]interface{})
	if !ok || vue["hybridMode"] != false {
		t.Fatalf("vue initialization options = %#v, want standalone virtual files", options["vue"])
	}
}

func TestLSP9J_ReactEnhancesTypeScriptWithoutFakeServer(t *testing.T) {
	root := t.TempDir()
	writeLSP9JPackageJSON(t, root, map[string]string{"react": "^19.0.0"}, map[string]string{"typescript": "^5.9.0"})
	tsSDK := filepath.Join(root, "node_modules", "typescript", "lib")
	writeLSP9JFile(t, filepath.Join(tsSDK, "tsserverlibrary.js"), "")

	statuses := NewLSPService(root).DetectLSPServers()
	if lsp9JHasStatus(statuses, "react") {
		t.Fatal("React was incorrectly exposed as an independent LSP server")
	}
	typescript := lsp9JStatus(t, statuses, "typescript")
	if typescript.Framework != "react" {
		t.Fatalf("TypeScript framework = %q, want react", typescript.Framework)
	}

	params := captureLSPInitializeParams(t, "typescript", root)
	options, ok := params["initializationOptions"].(map[string]interface{})
	if !ok {
		t.Fatalf("initializationOptions = %#v", params["initializationOptions"])
	}
	prefs, ok := options["preferences"].(map[string]interface{})
	if !ok || prefs["includeCompletionsForModuleExports"] != true || prefs["includeCompletionsWithSnippetText"] != true {
		t.Fatalf("React TypeScript preferences = %#v", options["preferences"])
	}
	tsserver, ok := options["tsserver"].(map[string]interface{})
	if !ok || tsserver["path"] != filepath.Join(tsSDK, "tsserver.js") {
		t.Fatalf("React tsserver options = %#v", options["tsserver"])
	}
}

func TestLSP9J_MultiWorkspaceResolutionIsIsolated(t *testing.T) {
	plainRoot := t.TempDir()
	vueRoot := t.TempDir()
	writeLSP9JPackageJSON(t, vueRoot, map[string]string{"vue": "^3.5.0"}, map[string]string{"typescript": "^5.9.0"})
	wantPath := writeLSP9JExecutable(t, vueRoot, "vue-language-server")

	resolution := resolveLSPServerForRoots("vue", []string{plainRoot, vueRoot})
	if !resolution.Applicable || resolution.WorkspaceRoot != vueRoot || resolution.Path != wantPath {
		t.Fatalf("multi-root resolution = %+v", resolution)
	}
	if lsp9JHasStatus(NewLSPService(plainRoot).DetectLSPServers(), "vue") {
		t.Fatal("Vue detection leaked into an unrelated LSPService workspace")
	}
	if !lsp9JHasStatus(NewLSPService(vueRoot).DetectLSPServers(), "vue") {
		t.Fatal("Vue workspace did not retain its own framework status")
	}
}

func TestLSP9J_MultiWorkspaceReactUsesItsOwnTypeScriptSDK(t *testing.T) {
	plainRoot := t.TempDir()
	writeLSP9JExecutable(t, plainRoot, "typescript-language-server")
	reactRoot := t.TempDir()
	writeLSP9JPackageJSON(t, reactRoot, map[string]string{"react": "^19.0.0"}, map[string]string{"typescript": "^5.9.0"})
	wantPath := writeLSP9JExecutable(t, reactRoot, "typescript-language-server")
	writeLSP9JFile(t, filepath.Join(reactRoot, "node_modules", "typescript", "lib", "tsserverlibrary.js"), "")

	resolution := resolveLSPServerForRoots("typescript", []string{plainRoot, reactRoot})
	if resolution.WorkspaceRoot != reactRoot || resolution.Path != wantPath {
		t.Fatalf("React TypeScript resolution = %+v", resolution)
	}
	options := lspInitializationOptions("typescript", resolution.WorkspaceRoot)
	typescript, ok := options["typescript"].(map[string]interface{})
	if !ok || typescript["tsdk"] != filepath.Join(reactRoot, "node_modules", "typescript", "lib") {
		t.Fatalf("React TypeScript SDK options = %#v", options)
	}
}

func TestLSP9J_WorkspaceSwitchDropsOldFrameworkStatus(t *testing.T) {
	vueRoot := t.TempDir()
	writeLSP9JPackageJSON(t, vueRoot, map[string]string{"vue": "^3.5.0"}, map[string]string{"typescript": "^5.9.0"})
	writeLSP9JExecutable(t, vueRoot, "vue-language-server")
	writeLSP9JFile(t, filepath.Join(vueRoot, "node_modules", "typescript", "lib", "tsserverlibrary.js"), "")

	svc := NewLSPService(vueRoot)
	if !lsp9JHasStatus(svc.DetectLSPServers(), "vue") {
		t.Fatal("Vue status was not detected before switching workspaces")
	}
	svc.setWorkspaceRoot(t.TempDir())
	if lsp9JHasStatus(svc.DetectLSPServers(), "vue") {
		t.Fatal("Vue status from the previous workspace remained after switching")
	}
}

func TestLSP9J_MissingFrameworkDependencyHasActionableHint(t *testing.T) {
	root := t.TempDir()
	writeLSP9JPackageJSON(t, root, map[string]string{"vue": "^3.5.0"}, nil)
	t.Setenv("PATH", t.TempDir())
	err := NewLSPService(root).StartLSPServer("vue")
	if err == nil {
		t.Fatal("StartLSPServer(vue) unexpectedly succeeded")
	}
	message := err.Error()
	if !strings.Contains(message, "npm install --save-dev @vue/language-server typescript") || !strings.Contains(message, "workspace") {
		t.Fatalf("error is not actionable and workspace-local: %q", message)
	}
}

func TestLSP9J_FrameworkStopWaitsOnceAndKeepsTypeScriptRunning(t *testing.T) {
	cmd, process := startLSP9JTestProcess(t, "block")
	svc := NewLSPService(t.TempDir())
	installLSP9JTestProcess(svc, "vue", cmd, process)
	typescript := &lspServer{running: true}
	svc.servers["typescript"] = typescript

	if err := svc.StopLSPServer("vue"); err != nil {
		t.Fatalf("StopLSPServer(vue): %v", err)
	}
	select {
	case <-process.done:
	case <-time.After(lsp9JTestProcessTimeout):
		t.Fatal("StopLSPServer returned before the Volar process was reaped")
	}
	if cmd.ProcessState == nil {
		t.Fatal("Volar process was killed but not waited")
	}
	if svc.servers["typescript"] != typescript || !typescript.running {
		t.Fatal("stopping Volar changed the independent TypeScript server")
	}
}

func TestLSP9JProcessHelper(t *testing.T) {
	if os.Getenv("KOYORI_IDE_LSP_9J_WAIT_HELPER") == "" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

const lsp9JTestProcessTimeout = 5 * time.Second

func startLSP9JTestProcess(t *testing.T, mode string) (*exec.Cmd, *lspProcess) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLSP9JProcessHelper$")
	cmd.Env = append(os.Environ(), "KOYORI_IDE_LSP_9J_WAIT_HELPER="+mode)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	process := newLSPProcess(cmd)
	t.Cleanup(func() {
		if err := process.stop(lsp9JTestProcessTimeout); err != nil {
			t.Errorf("stop helper process: %v", err)
		}
	})
	return cmd, process
}

func installLSP9JTestProcess(svc *LSPService, language string, cmd *exec.Cmd, process *lspProcess) {
	srv := &lspServer{
		cmd:     cmd,
		process: process,
		running: true,
		client:  &jsonRPCClient{w: io.Discard},
	}
	svc.mu.Lock()
	svc.servers[language] = srv
	svc.mu.Unlock()
	go svc.observeLSPProcess(language, srv)
}

func writeLSP9JPackageJSON(t *testing.T, root string, dependencies, devDependencies map[string]string) {
	t.Helper()
	payload, err := json.Marshal(map[string]interface{}{
		"dependencies":    dependencies,
		"devDependencies": devDependencies,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeLSP9JFile(t, filepath.Join(root, "package.json"), string(payload))
}

func writeLSP9JExecutable(t *testing.T, root, name string) string {
	t.Helper()
	binDir := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		name += ".cmd"
		contents = "@exit /b 0\r\n"
	}
	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func writeLSP9JFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lsp9JStatus(t *testing.T, statuses []LSPServerStatus, language string) LSPServerStatus {
	t.Helper()
	for _, status := range statuses {
		if status.Language == language {
			return status
		}
	}
	t.Fatalf("status %q not found in %+v", language, statuses)
	return LSPServerStatus{}
}

func lsp9JHasStatus(statuses []LSPServerStatus, language string) bool {
	for _, status := range statuses {
		if status.Language == language {
			return true
		}
	}
	return false
}
