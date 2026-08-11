//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"
)

// newTestServiceSet builds the minimal real service graph the endpoint needs:
// the shared WorkspaceContext links Project/File/Terminal so AddProject's
// two-phase root switch works exactly as it does in the packaged app (the
// main package wires the same five services into e2e.ServiceSet).
func newTestServiceSet(t *testing.T) ServiceSet {
	t.Helper()
	configDir := t.TempDir()
	ctx := services.NewWorkspaceContext()
	file := &services.FileService{}
	terminal := services.NewTerminalServiceWithWorkspaceContext(ctx)
	agent := services.NewAgentServiceWithWorkspaceContext(ctx)
	ai := services.NewAIService()
	project := services.NewProjectService(file, terminal, agent, ai)
	services.SetProjectWorkspaceContext(project, ctx)
	lsp := services.NewLSPService("")
	services.SetLSPFileService(lsp, file)
	recovery := services.NewRecoveryService(configDir)
	services.SetRecoveryWorkspaceContext(recovery, ctx)
	window := services.NewWindowServiceWithWorkspaceContext(ctx)
	services.WireRecoveryGuards(project, window, recovery)
	t.Cleanup(func() {
		lsp.StopAll()
		terminal.Shutdown()
	})
	return ServiceSet{
		Project:  project,
		File:     file,
		Terminal: terminal,
		LSP:      lsp,
		Recovery: recovery,
		Window:   window,
	}
}

func TestStartRequiresBuildTagAndExplicitEnvironmentOptIn(t *testing.T) {
	set := newTestServiceSet(t)
	t.Setenv(envOptIn, "")
	t.Setenv(envToken, strings.Repeat("ab", 32))
	handshakePath := filepath.Join(t.TempDir(), "handshake.json")
	t.Setenv(envHandshake, handshakePath)

	cleanup, err := Start(set)
	if err != nil {
		t.Fatalf("disabled automation returned an error: %v", err)
	}
	if cleanup != nil {
		t.Fatal("automation returned cleanup without KOYORI_IDE_E2E=1")
	}
	if _, err := os.Stat(handshakePath); !os.IsNotExist(err) {
		t.Fatalf("disabled automation wrote a handshake: %v", err)
	}
}

func TestStartListensOnLoopbackAndRejectsTokenReplay(t *testing.T) {
	set := newTestServiceSet(t)
	initialToken := strings.Repeat("cd", 32)
	handshakePath := filepath.Join(t.TempDir(), "handshake.json")
	t.Setenv(envOptIn, "1")
	t.Setenv(envToken, initialToken)
	t.Setenv(envHandshake, handshakePath)

	cleanup, err := Start(set)
	if err != nil {
		t.Fatalf("start automation: %v", err)
	}
	if cleanup == nil {
		t.Fatal("enabled automation did not return cleanup")
	}
	t.Cleanup(cleanup)

	data, err := os.ReadFile(handshakePath)
	if err != nil {
		t.Fatalf("read handshake: %v", err)
	}
	var hs handshake
	if err := json.Unmarshal(data, &hs); err != nil {
		t.Fatalf("decode handshake: %v", err)
	}
	if !strings.HasPrefix(hs.URL, "http://127.0.0.1:") {
		t.Fatalf("automation URL %q is not loopback-only", hs.URL)
	}

	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(filePath, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, status := sendCommand(t, hs.URL, initialToken, command{
		Action:    "open-workspace",
		Workspace: workspace,
	})
	if status != http.StatusOK || next == "" || next == initialToken {
		t.Fatalf("first command status=%d nextTokenChanged=%v", status, next != "" && next != initialToken)
	}

	_, replayStatus := sendCommand(t, hs.URL, initialToken, command{
		Action: "open-file",
		Path:   filePath,
	})
	if replayStatus != http.StatusUnauthorized {
		t.Fatalf("replayed token status=%d, want %d", replayStatus, http.StatusUnauthorized)
	}
	_, currentStatus := sendCommand(t, hs.URL, next, command{
		Action: "open-file",
		Path:   filePath,
	})
	if currentStatus != http.StatusOK {
		t.Fatalf("rotated token status=%d, want %d", currentStatus, http.StatusOK)
	}
}

func sendCommand(
	t *testing.T,
	url, token string,
	cmd command,
) (string, int) {
	t.Helper()
	body, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url+"/v1/command", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send command: %v", err)
	}
	defer response.Body.Close()
	return response.Header.Get("X-Koyori-IDE-E2E-Token"), response.StatusCode
}

func TestEditSaveAndRecoveryUseRealServices(t *testing.T) {
	set := newTestServiceSet(t)
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "main.go")
	initial := "package fixture\n"
	saved := "package fixture\n\nfunc Saved() {}\n"
	dirty := saved + "\n// unsaved\n"
	if err := os.WriteFile(filePath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Project.AddProject(workspace); err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if _, err := set.Recovery.ScanRecoverable(); err != nil {
		t.Fatalf("initial recovery scan: %v", err)
	}
	automation := &server{services: set}

	editResult, err := automation.execute(command{
		Action:  "edit",
		Path:    filePath,
		Content: saved,
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	baselineHash, _ := editResult.(map[string]interface{})["baselineHash"].(string)
	if baselineHash == "" {
		t.Fatal("edit did not capture a baseline hash")
	}
	if _, err := automation.execute(command{
		Action:       "save",
		Path:         filePath,
		Content:      saved,
		BaselineHash: baselineHash,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	written, err := os.ReadFile(filePath)
	if err != nil || string(written) != saved {
		t.Fatalf("saved bytes=%q err=%v", written, err)
	}

	if _, err := automation.execute(command{
		Action:  "edit",
		Path:    filePath,
		Content: dirty,
	}); err != nil {
		t.Fatalf("dirty edit: %v", err)
	}
	scanResult, err := automation.execute(command{Action: "recovery-scan"})
	if err != nil {
		t.Fatalf("recovery scan: %v", err)
	}
	scan := scanResult.(services.RecoveryScan)
	if len(scan.Files) != 1 || scan.Files[0].Content != dirty || scan.Files[0].Status != services.RecoveryStatusClean {
		t.Fatalf("unexpected recovery scan: %+v", scan)
	}
}

func TestRecoveryGuardProbeRejectsEveryPendingCleanupPath(t *testing.T) {
	set := newTestServiceSet(t)
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	filePath := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(filePath, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Project.AddProject(workspace); err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if _, err := set.Recovery.ScanRecoverable(); err != nil {
		t.Fatalf("resolve initial scan: %v", err)
	}
	if _, err := (&server{services: set}).execute(command{
		Action:   "edit",
		Path:     filePath,
		Content:  "package fixture\n// dirty\n",
		WindowID: "crashed",
	}); err != nil {
		t.Fatalf("journal crash record: %v", err)
	}
	if _, err := set.Recovery.ScanRecoverable(); err != nil {
		t.Fatalf("scan pending record: %v", err)
	}

	automation := &server{services: set}
	result, err := automation.execute(command{
		Action:    "recovery-guard-probe",
		Workspace: otherWorkspace,
		WindowID:  "crashed",
		Path:      filePath,
	})
	if err != nil {
		t.Fatalf("guard probe: %v", err)
	}
	guard, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("guard result type = %T", result)
	}
	rejections, ok := guard["rejections"].(map[string]string)
	if !ok || len(rejections) != 7 {
		t.Fatalf("guard rejections = %#v", guard["rejections"])
	}
	if enabled, _ := guard["journalEnabled"].(bool); !enabled {
		t.Fatal("guard probe disabled the recovery journal")
	}
	if state := set.Recovery.GetRecoveryState(); state.Phase != services.RecoveryPhasePending {
		t.Fatalf("recovery phase after guard probe = %q", state.Phase)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("workspace file changed during guard probe: %v", err)
	}
}

func TestNativeWindowCloseProbeInvokesInjectedWindowClose(t *testing.T) {
	set := newTestServiceSet(t)
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(filePath, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Project.AddProject(workspace); err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if _, err := set.Recovery.ScanRecoverable(); err != nil {
		t.Fatalf("resolve initial scan: %v", err)
	}
	if err := set.Recovery.SaveDirtyBuffer(
		"crashed", filePath, "dirty", "utf-8", "lf", 0, "",
	); err != nil {
		t.Fatalf("save recovery record: %v", err)
	}
	if _, err := set.Recovery.ScanRecoverable(); err != nil {
		t.Fatalf("scan pending record: %v", err)
	}
	closeCalls := 0
	set.CloseWindow = func() { closeCalls++ }

	if _, err := (&server{services: set}).execute(command{Action: "native-window-close-probe"}); err != nil {
		t.Fatalf("native close probe: %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("native close calls = %d, want 1", closeCalls)
	}
}
