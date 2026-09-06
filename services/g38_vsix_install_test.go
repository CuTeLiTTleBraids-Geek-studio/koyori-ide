package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func g38CorpusFile(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "build", "e2e-evidence", "p9-g20", "corpus", name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real VSIX corpus file missing: %s", path)
	}
	return path
}

func TestG38_RealVSIXInstallsWithoutKoyoriPermissions(t *testing.T) {
	cases := []struct {
		file, publisher, name, version, sha, entry string
		wantThemes, wantLanguages, wantCommands    int
	}{
		{
			file: "Catppuccin.catppuccin-vsc-3.19.0.vsix", publisher: "Catppuccin", name: "catppuccin-vsc",
			version: "3.19.0", sha: "ebf347664837edbe91c9920ff3d14c96d4a28beeec0b95137c76058326329780",
			entry:      "dist/browser.cjs",
			wantThemes: 1,
		},
		{
			file: "djazair-language.djazair-language-1.1.6.vsix", publisher: "djazair-language", name: "djazair-language",
			version: "1.1.6", sha: "291812a057a6f54390aa37b6ebc057c499803bdaa39225d3c4fda8cf4c1e48b2",
			entry:         "extension.js",
			wantLanguages: 1,
		},
		{
			file: "mechatroner.rainbow-csv-3.24.1.vsix", publisher: "mechatroner", name: "rainbow-csv",
			version: "3.24.1", sha: "0ecb7da3fb2a54517cd41fce8e858d6276ea8523bed6fbfd64d5ed281bd7514a",
			entry:        "extension.js",
			wantCommands: 1,
		},
		{
			file: "PKief.material-icon-theme-5.37.0.vsix", publisher: "PKief", name: "material-icon-theme",
			version: "5.37.0", sha: "ade9adefe3909cea92aed52850ddd00975d1dc1b62fe558831f6fb8b88f7c3ce",
			entry:      "dist/extension/web/extension.cjs",
			wantThemes: 1,
		},
	}
	svc := NewMarketplaceService(t.TempDir())
	security := NewExtensionSecurityService(svc.configDir)
	svc.setSecurityService(security)
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			path := g38CorpusFile(t, tc.file)
			if err := svc.installFromVSIXFile(path, tc.sha, tc.publisher, tc.name, tc.version); err != nil {
				t.Fatalf("install %s: %v", tc.file, err)
			}
			manifest, err := svc.GetExtensionManifest(tc.publisher, tc.name)
			if err != nil {
				t.Fatalf("manifest: %v", err)
			}
			if tc.wantThemes > 0 && len(manifest.ParsedContributes.Themes) < tc.wantThemes && len(manifest.ParsedContributes.IconThemes) == 0 {
				t.Fatalf("expected theme contribution, got themes=%d iconThemes=%d", len(manifest.ParsedContributes.Themes), len(manifest.ParsedContributes.IconThemes))
			}
			if tc.wantLanguages > 0 && len(manifest.ParsedContributes.Languages) < tc.wantLanguages && len(manifest.ParsedContributes.Grammars) == 0 {
				t.Fatalf("expected language contribution, got languages=%d grammars=%d", len(manifest.ParsedContributes.Languages), len(manifest.ParsedContributes.Grammars))
			}
			if tc.wantCommands > 0 && len(manifest.ParsedContributes.Commands) < tc.wantCommands {
				t.Fatalf("expected command contribution, got %d", len(manifest.ParsedContributes.Commands))
			}
			entry, err := svc.ReadExtensionFile(tc.publisher, tc.name, filepath.Join("extension", filepath.FromSlash(strings.TrimPrefix(tc.entry, "./"))))
			if err != nil {
				t.Fatalf("read installed entry %s: %v", tc.entry, err)
			}
			if len(entry) == 0 {
				t.Fatalf("installed entry %s is empty", tc.entry)
			}
			info, err := security.GetSecurityInfo(tc.publisher + "." + tc.name)
			if err != nil {
				t.Fatalf("security: %v", err)
			}
			if info.Enabled {
				t.Fatal("installed corpus packages must stay disabled by default")
			}
		})
	}
}

func TestG38_UnsupportedAPIPackageInstallsWithoutFakeActivationClaim(t *testing.T) {
	path := g38CorpusFile(t, "redhat.vscode-yaml-1.25.2026080708.vsix")
	svc := NewMarketplaceService(t.TempDir())
	security := NewExtensionSecurityService(svc.configDir)
	svc.setSecurityService(security)
	err := svc.installFromVSIXFile(path, "23263c28e7b729656d6898f9f15d5190514decbe7ad38692f8888af9db3f0b78", "redhat", "vscode-yaml", "1.25.2026080708")
	if err != nil {
		t.Fatalf("unsupported-API package must still be installable: %v", err)
	}
	info, err := security.GetSecurityInfo("redhat.vscode-yaml")
	if err != nil {
		t.Fatalf("security: %v", err)
	}
	if info.Enabled {
		t.Fatal("untrusted/restricted or unverified activation must not auto-enable")
	}
}

func TestDeriveExtensionPermissionsCommandOnlyTrustedOrReviewed(t *testing.T) {
	m := &VSCodeExtensionManifest{
		Name: "hello", Publisher: "acme", Main: "dist/main.js",
		ActivationEvents:  []string{"onCommand:acme.hello"},
		ParsedContributes: ExtensionContributes{Commands: []ExtensionCommandContribution{{Command: "acme.hello", Title: "Hello"}}},
	}
	perms := deriveExtensionPermissions(m, "vscode.commands.registerCommand('acme.hello', () => vscode.window.showInformationMessage('hi'));")
	level := NewExtensionSecurityService(t.TempDir()).ClassifyExtension(perms)
	if level != SecurityTrusted && level != SecurityReviewed {
		t.Fatalf("command-only derivation = %s (%v)", level, perms)
	}
}

func TestDeriveExtensionPermissionsNetworkRestricted(t *testing.T) {
	m := &VSCodeExtensionManifest{Name: "net", Publisher: "acme", Main: "dist/main.js"}
	perms := deriveExtensionPermissions(m, "fetch('https://example.test');")
	level := NewExtensionSecurityService(t.TempDir()).ClassifyExtension(perms)
	if level != SecurityRestricted {
		t.Fatalf("network derivation = %s (%v)", level, perms)
	}
}
