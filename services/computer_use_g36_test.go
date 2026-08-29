package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image/png"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func TestComputerUseCatalogHiddenWhenDisabled(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	cu := newTestComputerUseService(t)
	if err := WireAgentComputerUse(agent, cu); err != nil {
		t.Fatalf("WireAgentComputerUse: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, tool := range catalog.Tools {
		if tool.Source == "computer-use" {
			t.Fatalf("disabled Computer Use leaked into catalog: %+v", tool)
		}
	}
	if _, err := cu.RequestOperationApproval("screenshot", `{"region":null}`); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("disabled screenshot = %v, want ErrNotAllowed", err)
	}
}

func TestComputerUseCatalogAppearsWhenEnabled(t *testing.T) {
	agent, _, _, _ := newExecutionCoreTestServices(t)
	cu := newTestComputerUseService(t)
	if err := cu.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: true}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := WireAgentComputerUse(agent, cu); err != nil {
		t.Fatalf("WireAgentComputerUse: %v", err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	found := false
	for _, tool := range catalog.Tools {
		if tool.ID == "computer.screenshot" {
			found = true
			if tool.InputSchema["additionalProperties"] != false {
				t.Fatalf("screenshot schema = %#v", tool.InputSchema)
			}
		}
	}
	if !found {
		t.Fatal("enabled Computer Use missing from catalog")
	}
}

func TestComputerUseAgentHonorsSessionPermissionMode(t *testing.T) {
	for _, test := range []struct {
		name          string
		mode          agentcore.SessionPermissionMode
		confirm       bool
		wantPlatform  bool
	}{
		{name: "always ask", mode: agentcore.SessionPermissionAlwaysAsk, confirm: false, wantPlatform: false},
		{name: "assist", mode: agentcore.SessionPermissionAssist, confirm: false, wantPlatform: false},
		{name: "allow all", mode: agentcore.SessionPermissionAllowAll, confirm: false, wantPlatform: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			workspace := NewWorkspaceContext()
			if err := workspace.Set(root); err != nil {
				t.Fatal(err)
			}
			agent := NewAgentServiceWithWorkspaceContext(workspace)
			t.Cleanup(func() { _ = agent.Close() })
			if err := agent.configureWorkspaceRoot(root); err != nil {
				t.Fatal(err)
			}
			file := NewFileServiceWithWorkspaceContext(workspace)
			if err := file.setWorkspaceRoot(root); err != nil {
				t.Fatal(err)
			}
			search := NewSearchService()
			search.setWorkspaceContext(workspace)
			if err := search.setWorkspaceRoot(root); err != nil {
				t.Fatal(err)
			}
			if err := WireAgentExecutionCore(agent, file, search, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			settings := NewSettingsServiceWithPath(filepath.Join(t.TempDir(), "settings.json"))
			if err := settings.SaveSettings(Settings{AgentPermissionMode: string(test.mode)}); err != nil {
				t.Fatal(err)
			}
			SetAgentSettingsService(agent, settings)
			if _, err := WireAgentLifecycle(agent, NewAIService(), NewAIPlanService(), NewAIGoalService(), NewAIPermissionService(t.TempDir())); err != nil {
				t.Fatal(err)
			}
			cu := newTestComputerUseService(t)
			if err := cu.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: true}); err != nil {
				t.Fatal(err)
			}
			platform := cu.platform.(*recordingComputerUsePlatform)
			cu.approveOperation = func(string, string) bool { return test.confirm }
			if err := WireAgentComputerUse(agent, cu); err != nil {
				t.Fatal(err)
			}
			sessionID, err := agent.createAgentSessionTrusted("chat")
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := agent.GetAgentToolCatalog(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var toolID string
			for _, tool := range catalog.Tools {
				if tool.ID == "computer.mouse_move" {
					toolID = tool.ID
					break
				}
			}
			if toolID == "" {
				t.Fatal("computer.mouse_move missing from catalog")
			}
			_, err = agent.ExecuteAgentTool(context.Background(), AgentToolExecutionRequest{
				SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: toolID,
				Arguments: map[string]interface{}{"x": 10, "y": 10},
			})
			if test.wantPlatform {
				if err != nil {
					t.Fatalf("allow-all Computer Use execution: %v", err)
				}
				if got := platform.callCount(); got != 1 {
					t.Fatalf("platform calls = %d, want 1", got)
				}
			} else {
				if !errors.Is(err, ErrNotAllowed) {
					t.Fatalf("Computer Use approval error = %v, want ErrNotAllowed", err)
				}
				if got := platform.callCount(); got != 0 {
					t.Fatalf("platform calls = %d, want 0", got)
				}
			}
		})
	}
}

func TestComputerUseWhitelistRejectsUnknownProcess(t *testing.T) {
	prev := computerUseForegroundProcess
	computerUseForegroundProcess = func() (string, error) { return "password-manager.exe", nil }
	t.Cleanup(func() { computerUseForegroundProcess = prev })
	svc := newTestComputerUseService(t)
	if err := svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: false,
		AppWhitelist:         []string{"koyori-ide-test.exe"},
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	_, err := svc.RequestOperationApproval("screenshot", `{"region":null}`)
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("whitelist miss = %v, want ErrNotAllowed", err)
	}
}

func TestWindowsScreenshotReturnsPNGWhenEnabled(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows native Computer Use")
	}
	svc := NewComputerUseService(t.TempDir())
	svc.approveOperation = func(string, string) bool { return true }
	if err := svc.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: false}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	result, err := executeApprovedComputerUseOperation(t, svc, "screenshot", `{"region":{"Min":{"X":0,"Y":0},"Max":{"X":32,"Y":32}}}`)
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(result.Screenshot)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("png decode: %v (stub must not count as success)", err)
	}
	if img.Bounds().Dx() < 1 || img.Bounds().Dy() < 1 {
		t.Fatalf("empty screenshot bounds: %v", img.Bounds())
	}
}
