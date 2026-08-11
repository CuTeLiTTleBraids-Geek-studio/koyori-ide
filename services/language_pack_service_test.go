package services

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const languagePackFixtureSeedHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
const languagePackToolchainOutputHelperArg = "--koyori-language-pack-toolchain-output-helper"

func TestLanguagePackToolchainOutputHelper(t *testing.T) {
	if !containsTestArgument(os.Args, languagePackToolchainOutputHelperArg) {
		return
	}
	_, _ = os.Stdout.Write([]byte("language-pack-stdout: "))
	_, _ = os.Stdout.Write(bytes.Repeat([]byte("o"), maxToolchainCommandStreamBytes*4))
	_, _ = os.Stderr.Write([]byte("language-pack-stderr: "))
	_, _ = os.Stderr.Write(bytes.Repeat([]byte("e"), maxToolchainCommandStreamBytes*4))
	os.Exit(23)
}

func TestLanguagePackServiceScopesStorageUnderKoyoriDirectory(t *testing.T) {
	koyoriDir := t.TempDir()
	service := NewLanguagePackService(koyoriDir)
	want := filepath.Join(koyoriDir, "language-packs")
	if service.root != want {
		t.Fatalf("language pack root = %q, want %q", service.root, want)
	}
	if len(service.state.TrustedPublishers) != 0 {
		t.Fatalf("production service shipped trusted fixture publishers: %+v", service.state.TrustedPublishers)
	}
}

func TestLanguagePackServiceNativePublisherTrustIsExplicitPersistentAndKeyPinned(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	root := t.TempDir()
	service := NewLanguagePackService(root)
	archive := writeLanguagePackFixture(t, root, "org.example.python", "1.0.0", "python-check")
	var approval languagePackPublisherApproval
	service.approvePublisher = func(actual languagePackPublisherApproval) bool {
		approval = actual
		return false
	}
	if _, err := service.installFromNativePath(archive); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("declined publisher trust error = %v", err)
	}
	if len(service.state.TrustedPublishers) != 0 || len(service.state.Packs) != 0 {
		t.Fatalf("declined trust changed state: %+v", service.state)
	}
	if approval.KeyID != "org.koyori.sdk-fixtures" || approval.PackID != "org.example.python" ||
		approval.Version != "1.0.0" || len(approval.Fingerprint) != 64 || len(approval.ArchiveSHA256) != 64 {
		t.Fatalf("native trust approval was not bound to exact archive metadata: %+v", approval)
	}

	service.approvePublisher = func(actual languagePackPublisherApproval) bool { return actual == approval }
	info, err := service.installFromNativePath(archive)
	if err != nil {
		t.Fatalf("approved publisher install: %v", err)
	}
	if info.PublisherKeyID != approval.KeyID {
		t.Fatalf("publisher metadata = %+v", info)
	}
	reloaded := NewLanguagePackService(root)
	if reloaded.GetLastError() != "" || len(reloaded.state.TrustedPublishers) != 1 {
		t.Fatalf("publisher trust did not reload: error=%q state=%+v", reloaded.GetLastError(), reloaded.state)
	}

	attackerSeed := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	attacker := writeLanguagePackFixtureForLanguageWithSeed(t, root, languagePackFixture{
		id: "org.example.attacker", version: "1.0.0", displayName: "Attacker", language: "attacker",
		extension: ".attack", rootMarker: ".git", serverID: "attacker-lsp", serverOrder: 41,
		serverExecutable: "attacker-lsp", serverKind: "attacker", serverArgs: []interface{}{"--stdio"},
		configurationSection: "attacker", commandID: "attacker-check", commandLabel: "Attacker Check",
		commandExecutable: "attacker", commandArgs: []interface{}{"check"}, commandDescription: "check",
		toolInstallHint: "Do not install",
	}, attackerSeed)
	prompted := false
	reloaded.approvePublisher = func(languagePackPublisherApproval) bool {
		prompted = true
		return true
	}
	if _, err := reloaded.installFromNativePath(attacker); err == nil || !strings.Contains(err.Error(), "change its public key") {
		t.Fatalf("key substitution error = %v", err)
	}
	if prompted {
		t.Fatal("known publisher key substitution reached the approval prompt")
	}
}

func TestLanguagePackServiceDisableAndUninstallRequireNativeApproval(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	root := t.TempDir()
	service := NewLanguagePackService(root)
	trustLanguagePackFixturePublisher(t, service)
	archive := writeLanguagePackFixture(t, root, "org.example.python", "1.0.0", "python-check")
	if _, err := InstallLanguagePackFromTrustedPath(service, archive); err != nil {
		t.Fatal(err)
	}
	service.approveChange = func(string, string) bool { return false }
	if err := service.DisableLanguagePack("org.example.python"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("declined disable error = %v", err)
	}
	if err := service.UninstallLanguagePack("org.example.python"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("declined uninstall error = %v", err)
	}
	if !service.state.Packs["org.example.python"].Enabled || len(service.state.Packs["org.example.python"].Versions) != 1 {
		t.Fatalf("declined change mutated state: %+v", service.state.Packs["org.example.python"])
	}
	service.approveChange = func(title, id string) bool {
		return title == "Disable language pack" && id == "org.example.python@1.0.0"
	}
	if err := service.DisableLanguagePack("org.example.python"); err != nil {
		t.Fatalf("approved disable: %v", err)
	}
	service.approveChange = func(title, id string) bool {
		return title == "Uninstall language pack" && id == "org.example.python@1.0.0"
	}
	if err := service.UninstallLanguagePack("org.example.python"); err != nil {
		t.Fatalf("approved uninstall: %v", err)
	}
	if _, exists := service.state.Packs["org.example.python"]; exists {
		t.Fatal("approved uninstall retained the pack")
	}
}

func TestLanguagePackServiceUninstallApprovalBindsActiveVersion(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	root := t.TempDir()
	service := NewLanguagePackService(root)
	trustLanguagePackFixturePublisher(t, service)
	for _, fixture := range []struct {
		version string
		command string
	}{{"1.0.0", "python-v1"}, {"2.0.0", "python-v2"}} {
		archive := writeLanguagePackFixture(t, root, "org.example.python", fixture.version, fixture.command)
		if _, err := InstallLanguagePackFromTrustedPath(service, archive); err != nil {
			t.Fatalf("install %s: %v", fixture.version, err)
		}
	}
	service.approveChange = func(title, target string) bool {
		if title != "Uninstall language pack" || target != "org.example.python@2.0.0" {
			t.Fatalf("approval target = %q / %q", title, target)
		}
		service.mu.Lock()
		record := service.state.Packs["org.example.python"]
		record.ActiveVersion = "1.0.0"
		service.state.Packs["org.example.python"] = record
		service.mu.Unlock()
		return true
	}
	if err := service.UninstallLanguagePack("org.example.python"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("stale uninstall approval error = %v", err)
	}
	record := service.state.Packs["org.example.python"]
	if record.ActiveVersion != "1.0.0" || len(record.Versions) != 2 {
		t.Fatalf("stale approval removed the wrong version: %+v", record)
	}
}

func TestLanguagePackServiceInstallUpgradeRollbackAndDisable(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	root := t.TempDir()
	service := NewLanguagePackService(root)
	trustLanguagePackFixturePublisher(t, service)
	v1 := writeLanguagePackFixture(t, root, "org.example.python", "1.0.0", "python-check")
	first, err := InstallLanguagePackFromTrustedPath(service, v1)
	if err != nil {
		t.Fatalf("install v1: %v", err)
	}
	if first.BuiltIn || !first.Enabled || first.Version != "1.0.0" {
		t.Fatalf("unexpected v1 info: %+v", first)
	}
	definition, ok := lspDefinitionForLanguage("python")
	if !ok || definition.sourcePackID != "org.example.python" {
		t.Fatalf("python definition was not contributed by external pack: %+v", definition)
	}
	commands := NewToolchainService().ListToolchainCommands()
	if !containsToolchainCommand(commands, "python-check") {
		t.Fatalf("external toolchain command missing: %+v", commands)
	}

	v2 := writeLanguagePackFixture(t, root, "org.example.python", "2.0.0", "python-check-v2")
	if _, err := InstallLanguagePackFromTrustedPath(service, v2); err != nil {
		t.Fatalf("install v2: %v", err)
	}
	if err := service.disableTrusted("org.example.python"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, ok := lspDefinitionForLanguage("python"); !ok {
		t.Fatal("disabling the pack removed the legacy Python broker")
	}
	if err := service.enableTrusted("org.example.python"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := service.rollbackTrusted("org.example.python"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	definition, ok = lspDefinitionForLanguage("python")
	if !ok || definition.sourcePackVersion != "1.0.0" {
		t.Fatalf("rollback did not activate v1: %+v", definition)
	}
	if err := service.uninstallTrusted("org.example.python"); err != nil {
		t.Fatalf("uninstall active version: %v", err)
	}
	definition, ok = lspDefinitionForLanguage("python")
	if !ok || definition.sourcePackID != "org.example.python" || definition.sourcePackVersion != "2.0.0" {
		t.Fatalf("uninstall did not restore the retained version: %+v", definition)
	}

	reloaded := NewLanguagePackService(root)
	defer setActiveExternalLanguagePacks(nil)
	infos := reloaded.ListLanguagePacks()
	for _, info := range infos {
		if info.ID == "org.example.python" && info.Active && info.Version == "2.0.0" {
			return
		}
	}
	t.Fatalf("reloaded state did not retain rollback target: %+v", infos)
}

func TestLanguagePackServiceRejectsInstallerDowngradeWithoutStateMutation(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	root := t.TempDir()
	service := NewLanguagePackService(root)
	trustLanguagePackFixturePublisher(t, service)
	v2 := writeLanguagePackFixture(t, root, "org.example.python", "2.0.0", "python-v2")
	if _, err := InstallLanguagePackFromTrustedPath(service, v2); err != nil {
		t.Fatalf("install v2: %v", err)
	}
	v1 := writeLanguagePackFixture(t, root, "org.example.python", "1.0.0", "python-v1")
	if _, err := InstallLanguagePackFromTrustedPath(service, v1); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("installer downgrade error = %v", err)
	}
	record := service.state.Packs["org.example.python"]
	if record.ActiveVersion != "2.0.0" || len(record.Versions) != 1 || record.Versions[0].Version != "2.0.0" {
		t.Fatalf("rejected downgrade mutated state: %+v", record)
	}
	if _, err := os.Stat(filepath.Join(service.root, "org.example.python", "1.0.0")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected downgrade published files: %v", err)
	}
}

func TestLanguagePackServiceRejectsSemVerPrereleaseDowngradeWithoutStateMutation(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	root := t.TempDir()
	service := NewLanguagePackService(root)
	trustLanguagePackFixturePublisher(t, service)
	higher := writeLanguagePackFixture(t, root, "org.example.python", "1.0.0-10", "python-pre-higher")
	if _, err := InstallLanguagePackFromTrustedPath(service, higher); err != nil {
		t.Fatalf("install higher prerelease: %v", err)
	}
	lower := writeLanguagePackFixture(t, root, "org.example.python", "1.0.0-2", "python-pre-lower")
	if _, err := InstallLanguagePackFromTrustedPath(service, lower); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("prerelease downgrade error = %v", err)
	}
	record := service.state.Packs["org.example.python"]
	if record.ActiveVersion != "1.0.0-10" || len(record.Versions) != 1 || record.Versions[0].Version != "1.0.0-10" {
		t.Fatalf("rejected prerelease downgrade mutated state: %+v", record)
	}
	if _, err := os.Stat(filepath.Join(service.root, "org.example.python", "1.0.0-2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected prerelease downgrade published files: %v", err)
	}
}

func TestCompareLanguagePackVersionsUsesSemVerPrecedence(t *testing.T) {
	tests := []struct {
		left  string
		right string
		want  int
	}{
		{left: "1.0.0-2", right: "1.0.0-10", want: -1},
		{left: "1.0.0-alpha", right: "1.0.0-alpha.1", want: -1},
		{left: "1.0.0-alpha.1", right: "1.0.0-alpha.beta", want: -1},
		{left: "1.0.0-beta.11", right: "1.0.0-rc.1", want: -1},
		{left: "1.0.0-rc.1", right: "1.0.0", want: -1},
		{left: "1.0.0+build.1", right: "1.0.0+build.2", want: 0},
		{left: "100000000000000000000.0.0", right: "99999999999999999999.0.0", want: 1},
	}
	for _, test := range tests {
		if got := compareLanguagePackVersions(test.left, test.right); got != test.want {
			t.Errorf("compareLanguagePackVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestLanguagePackServiceRuntimeContributionsTrackEnabledLifecycle(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	root := t.TempDir()
	service := NewLanguagePackService(root)
	trustLanguagePackFixturePublisher(t, service)
	archive := writeLanguagePackFixture(t, root, "org.example.python", "1.0.0", "python-check")
	if _, err := InstallLanguagePackFromTrustedPath(service, archive); err != nil {
		t.Fatalf("install external pack: %v", err)
	}

	contributions, err := service.ListActiveExternalLanguagePackContributions()
	if err != nil {
		t.Fatalf("list active runtime contributions: %v", err)
	}
	if len(contributions) != 1 || contributions[0].ID != "org.example.python" ||
		contributions[0].Version != "1.0.0" || len(contributions[0].ManifestSHA256) != 64 ||
		len(contributions[0].Languages) != 1 || contributions[0].Languages[0].ID != "python" ||
		len(contributions[0].Languages[0].Extensions) != 1 || contributions[0].Languages[0].Extensions[0] != ".py" {
		t.Fatalf("unexpected renderer-safe runtime contribution: %+v", contributions)
	}
	contributions[0].Languages[0].Extensions[0] = ".mutated"
	again, err := service.ListActiveExternalLanguagePackContributions()
	if err != nil || again[0].Languages[0].Extensions[0] != ".py" {
		t.Fatalf("renderer snapshot mutated backend state: contributions=%+v err=%v", again, err)
	}

	if err := service.disableTrusted("org.example.python"); err != nil {
		t.Fatalf("disable external pack: %v", err)
	}
	disabled, err := service.ListActiveExternalLanguagePackContributions()
	if err != nil || len(disabled) != 0 {
		t.Fatalf("disabled pack remained in runtime snapshot: %+v / %v", disabled, err)
	}
	if err := service.enableTrusted("org.example.python"); err != nil {
		t.Fatalf("enable external pack: %v", err)
	}
	enabled, err := service.ListActiveExternalLanguagePackContributions()
	if err != nil || len(enabled) != 1 {
		t.Fatalf("enabled pack missing from runtime snapshot: %+v / %v", enabled, err)
	}
	if err := service.uninstallTrusted("org.example.python"); err != nil {
		t.Fatalf("uninstall external pack: %v", err)
	}
	uninstalled, err := service.ListActiveExternalLanguagePackContributions()
	if err != nil || len(uninstalled) != 0 {
		t.Fatalf("uninstalled pack remained in runtime snapshot: %+v / %v", uninstalled, err)
	}
}

func TestLanguagePackServiceInstallsRustPackWithoutCoreChanges(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	root := t.TempDir()
	service := NewLanguagePackService(root)
	trustLanguagePackFixturePublisher(t, service)
	archive := writeLanguagePackFixtureForLanguage(t, root, languagePackFixture{
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
		toolInstallHint:      "Install Rust with rustup",
	})
	info, err := InstallLanguagePackFromTrustedPath(service, archive)
	if err != nil {
		t.Fatalf("install Rust pack: %v", err)
	}
	if info.ID != "org.example.rust" || info.Version != "1.0.0" || !info.Active {
		t.Fatalf("unexpected Rust pack info: %+v", info)
	}
	definition, ok := lspDefinitionForLanguage("rust")
	if !ok || definition.sourcePackID != info.ID || definition.sourcePackVersion != info.Version {
		t.Fatalf("Rust definition was not contributed by the external pack: %+v", definition)
	}
	var rustStatus LSPServerStatus
	for _, status := range NewLSPService("").DetectAllLSPServers() {
		if status.Language == "rust" {
			rustStatus = status
			break
		}
	}
	if rustStatus.SourcePackID != info.ID || rustStatus.SourcePackVersion != info.Version {
		t.Fatalf("Rust LSP status source metadata is missing: %+v", rustStatus)
	}
	var contributed *ToolchainCommand
	for _, command := range NewToolchainService().ListToolchainCommands() {
		if command.ID == "rust-check" {
			candidate := command
			contributed = &candidate
			break
		}
	}
	if contributed == nil || contributed.SourcePackID != info.ID || contributed.SourcePackVersion != info.Version {
		t.Fatalf("Rust toolchain source metadata is missing: %+v", contributed)
	}
	if err := service.disableTrusted(info.ID); err != nil {
		t.Fatalf("disable Rust pack: %v", err)
	}
	definition, ok = lspDefinitionForLanguage("rust")
	if !ok || definition.sourcePackID != "" {
		t.Fatalf("disabling Rust pack did not restore the base broker: %+v", definition)
	}
	if containsToolchainCommand(NewToolchainService().ListToolchainCommands(), "rust-check") {
		t.Fatal("disabled Rust pack still contributes a toolchain command")
	}
}

func TestLanguagePackServiceRejectsUnsignedTamperedAndTraversalArchives(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	service := NewLanguagePackService(t.TempDir())
	unsigned := filepath.Join(t.TempDir(), "unsigned.koyori-language-pack")
	if err := os.WriteFile(unsigned, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallLanguagePackFromTrustedPath(service, unsigned); err == nil {
		t.Fatal("unsigned non-archive was accepted")
	}

	tampered := writeLanguagePackFixture(t, t.TempDir(), "org.example.tampered", "1.0.0", "tampered")
	data, err := os.ReadFile(tampered)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0x01
	if err := os.WriteFile(tampered, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallLanguagePackFromTrustedPath(service, tampered); err == nil {
		t.Fatal("tampered archive was accepted")
	}

	tests := []struct {
		name    string
		entries []hostileLanguagePackArchiveEntry
		want    string
	}{
		{
			name: "path traversal",
			entries: []hostileLanguagePackArchiveEntry{
				{name: "../manifest.json", content: []byte("{}")},
				{name: "signature.json", content: []byte("{}")},
			},
			want: "unsafe language pack archive entry",
		},
		{
			name: "backslash path",
			entries: []hostileLanguagePackArchiveEntry{
				{name: `dir\manifest.json`, content: []byte("{}")},
				{name: "signature.json", content: []byte("{}")},
			},
			want: "unsafe language pack archive entry",
		},
		{
			name: "symlink",
			entries: []hostileLanguagePackArchiveEntry{
				{name: "manifest.json", content: []byte("target"), mode: os.ModeSymlink | 0o777},
				{name: "signature.json", content: []byte("{}")},
			},
			want: "unsafe language pack archive entry",
		},
		{
			name: "duplicate",
			entries: []hostileLanguagePackArchiveEntry{
				{name: "manifest.json", content: []byte("{}")},
				{name: "manifest.json", content: []byte("{}")},
			},
			want: "duplicate language pack archive entry",
		},
		{
			name: "zip bomb entry",
			entries: []hostileLanguagePackArchiveEntry{
				{name: "manifest.json", content: bytes.Repeat([]byte("x"), maxBuiltInLanguagePackBytes+1)},
				{name: "signature.json", content: []byte("{}")},
			},
			want: "exceeds size limit",
		},
		{
			name: "extra payload",
			entries: []hostileLanguagePackArchiveEntry{
				{name: "manifest.json", content: []byte("{}")},
				{name: "payload.exe", content: []byte("MZ")},
			},
			want: "unsupported language pack archive entry",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := writeHostileLanguagePackArchive(t, test.name, test.entries)
			if _, err := InstallLanguagePackFromTrustedPath(service, archive); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("hostile archive error = %v, want %q", err, test.want)
			}
		})
	}
}

type hostileLanguagePackArchiveEntry struct {
	name    string
	content []byte
	mode    os.FileMode
}

func writeHostileLanguagePackArchive(t *testing.T, name string, entries []hostileLanguagePackArchiveEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), strings.ReplaceAll(name, " ", "-")+".koyori-language-pack")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, candidate := range entries {
		header := &zip.FileHeader{Name: candidate.name, Method: zip.Deflate}
		if candidate.mode != 0 {
			header.SetMode(candidate.mode)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(candidate.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLanguagePackToolchainBoundsMaliciousOutputAndIsolatesCrash(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	configRoot := t.TempDir()
	service := NewLanguagePackService(configRoot)
	trustLanguagePackFixturePublisher(t, service)
	archive := writeLanguagePackFixtureForLanguage(t, configRoot, languagePackFixture{
		id: "org.example.output", version: "1.0.0", displayName: "Output Fixture", language: "output-fixture",
		extension: ".output", rootMarker: ".output-root", serverID: "output-lsp", serverOrder: 42,
		serverExecutable: "unavailable-output-lsp", serverKind: "generic", serverArgs: []interface{}{"--stdio"},
		configurationSection: "output-fixture", commandID: "output-fixture-check", commandLabel: "Output Fixture: Check",
		commandExecutable: filepath.Base(executable),
		commandArgs: []interface{}{
			"-test.run=^TestLanguagePackToolchainOutputHelper$", "--", languagePackToolchainOutputHelperArg,
		},
		commandDescription: "Exercise bounded output", toolInstallHint: "Test helper is unavailable",
	})
	if _, err := InstallLanguagePackFromTrustedPath(service, archive); err != nil {
		t.Fatalf("install signed output fixture: %v", err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".output-root"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceContext := NewWorkspaceContext()
	if err := workspaceContext.SetRoots([]string{workspace}); err != nil {
		t.Fatal(err)
	}
	result, err := NewToolchainServiceWithWorkspaceContext(workspaceContext).
		RunToolchainCommand("output-fixture-check", "")
	if err != nil {
		t.Fatalf("run malicious output fixture: %v", err)
	}
	suffix := result.Output
	if len(suffix) > 128 {
		suffix = suffix[len(suffix)-128:]
	}
	if result.Success || !strings.Contains(result.Output, "language-pack-stdout:") ||
		!strings.Contains(result.Output, "language-pack-stderr:") ||
		strings.Count(result.Output, "[toolchain output truncated]") != 2 {
		t.Fatalf("unexpected bounded toolchain result: success=%v bytes=%d output suffix=%q", result.Success, len(result.Output), suffix)
	}
	if len(result.Output) > 2*maxToolchainCommandStreamBytes+256 {
		t.Fatalf("language-pack toolchain output was not bounded: %d bytes", len(result.Output))
	}
}

func TestLanguagePackOfflineInstallAndMissingToolsDegradeWithoutExecution(t *testing.T) {
	defer setActiveExternalLanguagePacks(nil)
	configRoot := t.TempDir()
	service := NewLanguagePackService(configRoot)
	trustLanguagePackFixturePublisher(t, service)
	archive := writeLanguagePackFixture(t, configRoot, "org.example.python", "1.0.0", "python-offline-check")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	installed, err := InstallLanguagePackFromTrustedPath(service, archive)
	if err != nil || !installed.Active {
		t.Fatalf("offline signed install failed: info=%+v err=%v", installed, err)
	}
	var status LSPServerStatus
	for _, candidate := range NewLSPService(t.TempDir()).DetectAllLSPServers() {
		if candidate.Language == "python" {
			status = candidate
			break
		}
	}
	if status.Available || status.Running || status.SourcePackID != installed.ID ||
		status.SourcePackVersion != installed.Version || status.InstallHint == "" {
		t.Fatalf("offline missing LSP status was not diagnostic: %+v", status)
	}
	workspace := t.TempDir()
	workspaceContext := NewWorkspaceContext()
	if err := workspaceContext.SetRoots([]string{workspace}); err != nil {
		t.Fatal(err)
	}
	result, err := NewToolchainServiceWithWorkspaceContext(workspaceContext).
		RunToolchainCommand("python-offline-check", "")
	if err != nil || result.Success || !result.NotInstalled || result.InstallCmd != "Install Python" {
		t.Fatalf("offline missing tool result = %+v err=%v", result, err)
	}
}

func writeLanguagePackFixture(t *testing.T, root, id, version, commandID string) string {
	t.Helper()
	return writeLanguagePackFixtureForLanguage(t, root, languagePackFixture{
		id:                   id,
		version:              version,
		displayName:          "Example Python",
		language:             "python",
		extension:            ".py",
		rootMarker:           ".git",
		serverID:             "pyright",
		serverOrder:          20,
		serverExecutable:     "pyright-langserver",
		serverKind:           "pyright",
		serverArgs:           []interface{}{"--stdio"},
		versionExecutable:    "pyright",
		versionPin:           "1.1.411",
		configurationSection: "pyright",
		debuggerID:           "debugpy",
		debuggerExecutable:   "debugpy-adapter",
		debuggerArgs:         []interface{}{},
		debuggerInstallHint:  "python -m pip install debugpy",
		commandID:            commandID,
		commandLabel:         "Python: Check",
		commandExecutable:    "python",
		commandArgs:          []interface{}{"-m", "py_compile"},
		commandDescription:   "Compile Python files",
		commandFileScoped:    true,
		toolInstallHint:      "Install Python",
	})
}

type languagePackFixture struct {
	id                   string
	version              string
	displayName          string
	language             string
	extension            string
	rootMarker           string
	serverID             string
	serverOrder          int
	serverExecutable     string
	serverKind           string
	serverArgs           []interface{}
	versionExecutable    string
	versionPin           string
	configurationSection string
	debuggerID           string
	debuggerExecutable   string
	debuggerArgs         []interface{}
	debuggerInstallHint  string
	commandID            string
	commandLabel         string
	commandExecutable    string
	commandArgs          []interface{}
	commandDescription   string
	commandFileScoped    bool
	toolInstallHint      string
}

func writeLanguagePackFixtureForLanguage(t *testing.T, root string, fixture languagePackFixture) string {
	t.Helper()
	return writeLanguagePackFixtureForLanguageWithSeed(t, root, fixture, languagePackFixtureSeedHex)
}

func writeLanguagePackFixtureForLanguageWithSeed(t *testing.T, root string, fixture languagePackFixture, seedHex string) string {
	t.Helper()
	manifestValue := map[string]interface{}{
		"schemaVersion": "1.0",
		"id":            fixture.id,
		"version":       fixture.version,
		"displayName":   fixture.displayName,
		"compatibility": map[string]interface{}{
			"engineApi": languagePackEngineAPIVersion, "hostProtocol": languagePackLocalHostProtocol,
			"platforms": []interface{}{map[string]interface{}{"os": runtime.GOOS, "arch": runtime.GOARCH}},
		},
		"languages": []interface{}{map[string]interface{}{
			"id": fixture.language, "extensions": []interface{}{fixture.extension}, "filenames": []interface{}{},
		}},
		"rootMarkers": []interface{}{fixture.rootMarker},
		"servers": []interface{}{map[string]interface{}{
			"id": fixture.serverID, "statusOrder": json.Number(fmt.Sprintf("%d", fixture.serverOrder)), "languages": []interface{}{fixture.language}, "aliases": []interface{}{},
			"executables": []interface{}{map[string]interface{}{"commandName": fixture.serverExecutable, "kind": fixture.serverKind}},
			"args":        fixture.serverArgs, "installHint": "Install " + fixture.serverExecutable,
			"workspaceNode": false, "initializationProfile": "generic", "configurationSections": []interface{}{fixture.configurationSection},
			"configurationResponse": "full", "versionArgs": []interface{}{"--version"},
			"preferReactWorkspace": false, "reactAware": false,
		}},
		"toolchain": map[string]interface{}{
			"commands": []interface{}{map[string]interface{}{
				"id": fixture.commandID, "label": fixture.commandLabel, "language": fixture.language, "executable": fixture.commandExecutable,
				"args": fixture.commandArgs, "description": fixture.commandDescription, "fileScoped": fixture.commandFileScoped,
			}},
			"tools": []interface{}{map[string]interface{}{"name": fixture.commandExecutable, "installHint": fixture.toolInstallHint}},
		},
		"permissions":         []interface{}{"workspace.read", "process.launch"},
		"configurationSchema": map[string]interface{}{},
		"integrity":           map[string]interface{}{"manifestSha256": ""},
	}
	server := manifestValue["servers"].([]interface{})[0].(map[string]interface{})
	if fixture.versionExecutable != "" {
		server["versionExecutable"] = fixture.versionExecutable
	}
	if fixture.versionPin != "" {
		server["versionPin"] = fixture.versionPin
	}
	if fixture.debuggerID != "" {
		manifestValue["debuggers"] = []interface{}{map[string]interface{}{
			"id": fixture.debuggerID, "protocol": "dap", "languages": []interface{}{fixture.language},
			"executable": fixture.debuggerExecutable, "args": fixture.debuggerArgs,
			"installHint": fixture.debuggerInstallHint,
		}}
	}
	canonicalWithoutIntegrity, err := canonicalJSONWithoutField(manifestValue, "integrity")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(canonicalWithoutIntegrity))
	manifestValue["integrity"] = map[string]interface{}{"manifestSha256": hex.EncodeToString(digest[:])}
	manifestRaw, err := json.Marshal(manifestValue)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := parseLanguagePackManifest(manifestRaw)
	if err != nil {
		t.Fatalf("fixture manifest: %v", err)
	}
	publicKey, privateKey := fixtureKeyPairFromSeed(t, seedHex)
	_ = publicKey
	signature := languagePackSignature{
		Format: languagePackSignatureFormat, Algorithm: languagePackSignatureAlgorithm,
		KeyID: "org.koyori.sdk-fixtures", PackID: manifest.ID, Version: manifest.Version,
		ManifestSHA256: manifest.Integrity.ManifestSha256, PublicKey: hex.EncodeToString(publicKey),
	}
	payload, err := languagePackSignedPayload(signature)
	if err != nil {
		t.Fatal(err)
	}
	signature.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	signatureRaw, err := json.Marshal(signature)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, fixture.id+"-"+fixture.version+".koyori-language-pack")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	manifestEntry, err := writer.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = manifestEntry.Write(manifestRaw)
	signatureEntry, err := writer.Create("signature.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = signatureEntry.Write(signatureRaw)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func canonicalJSONWithoutField(value map[string]interface{}, field string) (string, error) {
	copyValue := make(map[string]interface{}, len(value))
	for key, item := range value {
		copyValue[key] = item
	}
	delete(copyValue, field)
	return canonicalJSON(copyValue)
}

func fixtureKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	return fixtureKeyPairFromSeed(t, languagePackFixtureSeedHex)
}

func fixtureKeyPairFromSeed(t *testing.T, seedHex string) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	return privateKey.Public().(ed25519.PublicKey), privateKey
}

func trustLanguagePackFixturePublisher(t *testing.T, service *LanguagePackService) {
	t.Helper()
	publicKey, _ := fixtureKeyPair(t)
	service.mu.Lock()
	defer service.mu.Unlock()
	service.state.TrustedPublishers["org.koyori.sdk-fixtures"] = languagePackPublisherState{
		PublicKey: hex.EncodeToString(publicKey),
		TrustedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := service.persistStateLocked(); err != nil {
		t.Fatalf("persist fixture publisher trust: %v", err)
	}
}
