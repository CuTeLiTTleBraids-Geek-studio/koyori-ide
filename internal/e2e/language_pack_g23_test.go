//go:build e2e

package e2e

import (
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"
)

func TestLanguagePackG23ProbeRunsSignedLifecycle(t *testing.T) {
	set := newTestServiceSet(t)
	workspace := t.TempDir()
	set.LSP = services.NewLSPService(workspace)
	set.LanguagePacks = services.NewLanguagePackService(t.TempDir())
	set.Toolchain = services.NewToolchainService()
	result, err := (&server{services: set}).runLanguagePackG23Probe(command{
		Action: "language-pack-g23-probe", Workspace: workspace,
	})
	if err != nil {
		t.Fatalf("run G23 language pack probe: %v", err)
	}
	values, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected G23 result type %T", result)
	}
	for _, field := range []string{
		"signedArchivesVerified", "pythonRustInstalled", "versionPinVerified",
		"lspSourcesVerified", "toolchainSourcesVerified", "toolchainExecuted",
		"disableEnableVerified", "rollbackVerified", "uninstallRestoreVerified",
	} {
		if values[field] != true {
			t.Errorf("G23 result %s = %v, want true", field, values[field])
		}
	}
}
