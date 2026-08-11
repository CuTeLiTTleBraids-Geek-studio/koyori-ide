package services

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type g20VSIXCorpusManifest struct {
	Main      string `json:"main"`
	Browser   string `json:"browser"`
	KoyoriIde *struct {
		Permissions *[]ExtensionPermission `json:"permissions"`
	} `json:"koyoriIde"`
}

func g20VSIXRequiresPermissionDeclaration(path string) (bool, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return false, err
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if entry.Name != "extension/package.json" {
			continue
		}
		r, err := entry.Open()
		if err != nil {
			return false, err
		}
		data, readErr := io.ReadAll(r)
		closeErr := r.Close()
		if readErr != nil {
			return false, readErr
		}
		if closeErr != nil {
			return false, closeErr
		}
		var manifest g20VSIXCorpusManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return false, err
		}
		executable := strings.TrimSpace(manifest.Main) != "" || strings.TrimSpace(manifest.Browser) != ""
		missingPermissions := manifest.KoyoriIde == nil || manifest.KoyoriIde.Permissions == nil
		return executable && missingPermissions, nil
	}
	return false, os.ErrNotExist
}

// TestG20_VSIX_RealCorpusInstallMatrix is opt-in because the corpus is
// downloaded from Open VSX and is intentionally not stored in the source
// tree. Each record is path|publisher|name|version|sha256.
func TestG20_VSIX_RealCorpusInstallMatrix(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("KOYORI_IDE_G20_VSIX_CORPUS"))
	if raw == "" {
		t.Skip("set KOYORI_IDE_G20_VSIX_CORPUS to run the pinned external VSIX corpus")
	}
	service := NewMarketplaceService(t.TempDir())
	security := NewExtensionSecurityService(service.configDir)
	service.setSecurityService(security)

	records := strings.Split(raw, ";")
	if len(records) < 2 {
		t.Fatalf("real VSIX corpus must contain at least two records")
	}
	installedCount := 0
	rejectedCount := 0
	for _, record := range records {
		fields := strings.Split(record, "|")
		if len(fields) != 5 {
			t.Fatalf("invalid VSIX corpus record %q", record)
		}
		path, publisher, name, version, expectedHash := fields[0], fields[1], fields[2], fields[3], fields[4]
		if !filepath.IsAbs(path) {
			t.Fatalf("VSIX corpus path must be absolute: %q", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("VSIX corpus file %q: %v", path, err)
		}
		expectedPermissionRejection, err := g20VSIXRequiresPermissionDeclaration(path)
		if err != nil {
			t.Fatalf("inspect real VSIX %s.%s: %v", publisher, name, err)
		}
		installErr := service.installFromVSIXFile(path, expectedHash, publisher, name, version)
		if expectedPermissionRejection {
			if installErr == nil || !strings.Contains(installErr.Error(), "koyoriIde.permissions explicitly") {
				t.Fatalf("install real VSIX %s.%s error = %v, want explicit permission rejection", publisher, name, installErr)
			}
			if _, statErr := os.Stat(service.extensionDir(publisher, name)); !os.IsNotExist(statErr) {
				t.Fatalf("permission-rejected VSIX %s.%s left install directory: %v", publisher, name, statErr)
			}
			if _, securityErr := security.GetSecurityInfo(publisher + "." + name); securityErr == nil {
				t.Fatalf("permission-rejected VSIX %s.%s left security state", publisher, name)
			}
			rejectedCount++
			continue
		}
		if installErr != nil {
			t.Fatalf("install real VSIX %s.%s: %v", publisher, name, installErr)
		}
		manifest, err := service.GetExtensionManifest(publisher, name)
		if err != nil {
			t.Fatalf("read installed manifest %s.%s: %v", publisher, name, err)
		}
		if manifest.Publisher != publisher || manifest.Name != name {
			t.Fatalf("manifest identity = %s.%s, want %s.%s", manifest.Publisher, manifest.Name, publisher, name)
		}
		info, err := security.GetSecurityInfo(publisher + "." + name)
		if err != nil {
			t.Fatalf("read security info %s.%s: %v", publisher, name, err)
		}
		if !info.Verified || info.Enabled {
			t.Fatalf("security state %s.%s = verified:%v enabled:%v, want verified and disabled", publisher, name, info.Verified, info.Enabled)
		}
		if err := service.UninstallExtension(publisher, name); err != nil {
			t.Fatalf("uninstall real VSIX %s.%s: %v", publisher, name, err)
		}
		if _, err := os.Stat(service.extensionDir(publisher, name)); !os.IsNotExist(err) {
			t.Fatalf("uninstall left extension directory %s.%s: %v", publisher, name, err)
		}
		installedCount++
	}
	t.Logf("real Open VSX corpus matrix: installed=%d rejected-for-missing-permissions=%d total=%d", installedCount, rejectedCount, len(records))
}
