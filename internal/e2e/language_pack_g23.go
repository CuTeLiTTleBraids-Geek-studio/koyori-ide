//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"
)

const (
	g23PythonPackID = "org.koyori.e2e.python"
	g23RustPackID   = "org.koyori.e2e.rust"
)

func (s *server) runLanguagePackG23Probe(cmd command) (interface{}, error) {
	if s.services.LanguagePacks == nil || s.services.LSP == nil || s.services.Toolchain == nil {
		return nil, errors.New("G23 language pack automation is not fully wired")
	}
	if cmd.Workspace == "" || !filepath.IsAbs(cmd.Workspace) {
		return nil, errors.New("G23 language pack probe requires an absolute workspace")
	}
	removeG23LanguagePacks(s.services.LanguagePacks)
	defer removeG23LanguagePacks(s.services.LanguagePacks)

	fixtureDir, err := os.MkdirTemp(cmd.Workspace, ".g23-language-packs-")
	if err != nil {
		return nil, fmt.Errorf("create G23 fixture directory: %w", err)
	}
	defer os.RemoveAll(fixtureDir)
	pythonPath := filepath.Join(cmd.Workspace, "g23_probe.py")
	cargoPath := filepath.Join(cmd.Workspace, "Cargo.toml")
	if err := os.WriteFile(pythonPath, []byte("def probe() -> int:\n    return 23\n"), 0o600); err != nil {
		return nil, fmt.Errorf("create Python probe fixture: %w", err)
	}
	if err := os.WriteFile(cargoPath, []byte("[package]\nname = \"g23-probe\"\nversion = \"0.1.0\"\nedition = \"2021\"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("create Rust probe fixture: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cmd.Workspace, "src"), 0o700); err != nil {
		return nil, fmt.Errorf("create Rust source directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cmd.Workspace, "src", "lib.rs"), []byte("pub fn probe() -> i32 { 23 }\n"), 0o600); err != nil {
		return nil, fmt.Errorf("create Rust probe source: %w", err)
	}

	pythonV1, err := services.WriteSignedLanguagePackFixtureForE2E(fixtureDir, services.LanguagePackFixtureSpecForE2E{
		ID: g23PythonPackID, Version: "1.0.0", DisplayName: "G23 Python", Language: "python",
		Extension: ".py", RootMarker: "pyproject.toml", ServerID: "g23-python-lsp", ServerOrder: 20,
		ServerExecutable: "pyright-langserver", ServerArgs: []string{"--stdio"}, VersionArgs: []string{"--version"},
		CommandID: "g23-python-v1", CommandLabel: "G23 Python v1", Configuration: "g23-python",
		ToolchainExecutable: "python", ToolchainArgs: []string{"-m", "py_compile", pythonPath},
	})
	if err != nil {
		return nil, fmt.Errorf("create Python v1 fixture: %w", err)
	}
	pythonV2, err := services.WriteSignedLanguagePackFixtureForE2E(fixtureDir, services.LanguagePackFixtureSpecForE2E{
		ID: g23PythonPackID, Version: "2.0.0", DisplayName: "G23 Python", Language: "python",
		Extension: ".py", RootMarker: "pyproject.toml", ServerID: "g23-python-lsp", ServerOrder: 20,
		ServerExecutable: "pyright-langserver", ServerArgs: []string{"--stdio"}, VersionArgs: []string{"--version"},
		CommandID: "g23-python-v2", CommandLabel: "G23 Python v2", Configuration: "g23-python",
		ToolchainExecutable: "python", ToolchainArgs: []string{"-m", "py_compile", pythonPath},
	})
	if err != nil {
		return nil, fmt.Errorf("create Python v2 fixture: %w", err)
	}
	rustV1, err := services.WriteSignedLanguagePackFixtureForE2E(fixtureDir, services.LanguagePackFixtureSpecForE2E{
		ID: g23RustPackID, Version: "1.0.0", DisplayName: "G23 Rust", Language: "rust",
		Extension: ".rs", RootMarker: "Cargo.toml", ServerID: "g23-rust-lsp", ServerOrder: 30,
		ServerExecutable: "rust-analyzer", ServerArgs: []string{}, VersionArgs: []string{"--version"},
		CommandID: "g23-rust-check", CommandLabel: "G23 Rust Check", Configuration: "g23-rust",
		ToolchainExecutable: "cargo", ToolchainArgs: []string{"check", "--manifest-path", cargoPath},
	})
	if err != nil {
		return nil, fmt.Errorf("create Rust fixture: %w", err)
	}

	pythonV1Info, publisherTrustOnboarded, err := services.InstallLanguagePackWithPublisherTrustForE2E(s.services.LanguagePacks, pythonV1)
	if err != nil {
		return nil, fmt.Errorf("install Python v1 fixture: %w", err)
	}
	if !publisherTrustOnboarded {
		return nil, errors.New("unknown language pack publisher bypassed native trust onboarding")
	}
	pythonV2Info, err := services.InstallLanguagePackFromTrustedPath(s.services.LanguagePacks, pythonV2)
	if err != nil {
		return nil, fmt.Errorf("install Python v2 fixture: %w", err)
	}
	rustInfo, err := services.InstallLanguagePackFromTrustedPath(s.services.LanguagePacks, rustV1)
	if err != nil {
		return nil, fmt.Errorf("install Rust fixture: %w", err)
	}
	for _, info := range []services.LanguagePackInfo{pythonV1Info, pythonV2Info, rustInfo} {
		if len(info.ManifestSHA256) != 64 || len(info.ArchiveSHA256) != 64 || info.PublisherKeyID != "org.koyori.sdk-fixtures" {
			return nil, fmt.Errorf("installed pack %s@%s is missing signed integrity metadata", info.ID, info.Version)
		}
	}
	if !activeLanguagePackVersion(s.services.LanguagePacks, g23PythonPackID, "2.0.0") ||
		!activeLanguagePackVersion(s.services.LanguagePacks, g23RustPackID, "1.0.0") {
		return nil, errors.New("installed language pack versions were not pinned as active")
	}
	if !lspStatusFromPack(s.services.LSP, "python", g23PythonPackID, "2.0.0") ||
		!lspStatusFromPack(s.services.LSP, "rust", g23RustPackID, "1.0.0") {
		return nil, errors.New("external language pack LSP source metadata is missing")
	}
	if !toolchainCommandFromPack(s.services.Toolchain, "g23-python-v2", g23PythonPackID, "2.0.0") ||
		!toolchainCommandFromPack(s.services.Toolchain, "g23-rust-check", g23RustPackID, "1.0.0") {
		return nil, errors.New("external language pack toolchain source metadata is missing")
	}
	pythonRun, err := s.services.Toolchain.RunToolchainCommand("g23-python-v2", "")
	if err != nil || !pythonRun.Success {
		return nil, fmt.Errorf("run Python fixture toolchain command: result=%+v err=%w", pythonRun, err)
	}
	rustRun, err := s.services.Toolchain.RunToolchainCommand("g23-rust-check", "")
	if err != nil || !rustRun.Success {
		return nil, fmt.Errorf("run Rust fixture toolchain command: result=%+v err=%w", rustRun, err)
	}

	if err := services.DisableLanguagePackForE2E(s.services.LanguagePacks, g23RustPackID); err != nil {
		return nil, fmt.Errorf("disable Rust fixture: %w", err)
	}
	if !lspStatusFromPack(s.services.LSP, "rust", "", "") || toolchainCommandExists(s.services.Toolchain, "g23-rust-check") {
		return nil, errors.New("disabled Rust fixture still contributes runtime capabilities")
	}
	if err := services.EnableLanguagePackForE2E(s.services.LanguagePacks, g23RustPackID); err != nil {
		return nil, fmt.Errorf("re-enable Rust fixture: %w", err)
	}
	if !lspStatusFromPack(s.services.LSP, "rust", g23RustPackID, "1.0.0") {
		return nil, errors.New("re-enabled Rust fixture did not restore its LSP contribution")
	}

	rolledBack, err := services.RollbackLanguagePackForE2E(s.services.LanguagePacks, g23PythonPackID)
	if err != nil {
		return nil, fmt.Errorf("roll back Python fixture: %w", err)
	}
	if rolledBack.Version != "1.0.0" || !lspStatusFromPack(s.services.LSP, "python", g23PythonPackID, "1.0.0") ||
		!toolchainCommandFromPack(s.services.Toolchain, "g23-python-v1", g23PythonPackID, "1.0.0") ||
		toolchainCommandExists(s.services.Toolchain, "g23-python-v2") {
		return nil, errors.New("Python rollback did not atomically restore v1 capabilities")
	}
	if err := services.UninstallLanguagePackForE2E(s.services.LanguagePacks, g23PythonPackID); err != nil {
		return nil, fmt.Errorf("uninstall Python v1 fixture: %w", err)
	}
	if !activeLanguagePackVersion(s.services.LanguagePacks, g23PythonPackID, "2.0.0") ||
		!lspStatusFromPack(s.services.LSP, "python", g23PythonPackID, "2.0.0") {
		return nil, errors.New("uninstalling active Python v1 did not restore retained v2")
	}
	if err := services.UninstallLanguagePackForE2E(s.services.LanguagePacks, g23PythonPackID); err != nil {
		return nil, fmt.Errorf("uninstall Python v2 fixture: %w", err)
	}
	if hasExternalLanguagePack(s.services.LanguagePacks, g23PythonPackID) || !lspStatusFromPack(s.services.LSP, "python", "", "") {
		return nil, errors.New("final Python uninstall did not restore the base broker")
	}
	if err := services.UninstallLanguagePackForE2E(s.services.LanguagePacks, g23RustPackID); err != nil {
		return nil, fmt.Errorf("uninstall Rust fixture: %w", err)
	}
	if hasExternalLanguagePack(s.services.LanguagePacks, g23RustPackID) || !lspStatusFromPack(s.services.LSP, "rust", "", "") {
		return nil, errors.New("final Rust uninstall did not restore the base broker")
	}

	return map[string]interface{}{
		"signedArchivesVerified":   true,
		"publisherTrustOnboarded":  true,
		"pythonRustInstalled":      true,
		"versionPinVerified":       true,
		"lspSourcesVerified":       true,
		"toolchainSourcesVerified": true,
		"toolchainExecuted":        true,
		"pythonToolchain":          map[string]interface{}{"success": pythonRun.Success, "output": pythonRun.Output},
		"rustToolchain":            map[string]interface{}{"success": rustRun.Success, "output": rustRun.Output},
		"pythonLsp":                "not-run: probe does not perform a real pyright-langserver protocol session",
		"rustLsp":                  "not-run: probe does not perform a real rust-analyzer protocol session",
		"disableEnableVerified":    true,
		"rollbackVerified":         true,
		"uninstallRestoreVerified": true,
		"pythonV1ArchiveSha256":    pythonV1Info.ArchiveSHA256,
		"pythonV2ArchiveSha256":    pythonV2Info.ArchiveSHA256,
		"rustArchiveSha256":        rustInfo.ArchiveSHA256,
	}, nil
}

func removeG23LanguagePacks(service *services.LanguagePackService) {
	for _, id := range []string{g23PythonPackID, g23RustPackID} {
		for range 4 {
			if !hasExternalLanguagePack(service, id) {
				break
			}
			if err := services.UninstallLanguagePackForE2E(service, id); err != nil {
				break
			}
		}
	}
}

func hasExternalLanguagePack(service *services.LanguagePackService, id string) bool {
	for _, info := range service.ListLanguagePacks() {
		if info.ID == id && !info.BuiltIn {
			return true
		}
	}
	return false
}

func activeLanguagePackVersion(service *services.LanguagePackService, id, version string) bool {
	for _, info := range service.ListLanguagePacks() {
		if info.ID == id && info.Version == version && info.Active && info.Enabled {
			return true
		}
	}
	return false
}

func lspStatusFromPack(service *services.LSPService, language, id, version string) bool {
	for _, status := range service.DetectAllLSPServers() {
		if status.Language == language {
			return status.SourcePackID == id && status.SourcePackVersion == version
		}
	}
	return false
}

func toolchainCommandFromPack(service *services.ToolchainService, commandID, id, version string) bool {
	for _, command := range service.ListToolchainCommands() {
		if command.ID == commandID {
			return command.SourcePackID == id && command.SourcePackVersion == version
		}
	}
	return false
}

func toolchainCommandExists(service *services.ToolchainService, commandID string) bool {
	for _, command := range service.ListToolchainCommands() {
		if command.ID == commandID {
			return true
		}
	}
	return false
}
