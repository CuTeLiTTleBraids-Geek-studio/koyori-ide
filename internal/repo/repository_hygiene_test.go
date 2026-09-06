package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

func TestG17RepositoryHygieneAndIgnoreRules(t *testing.T) {
	entries, err := os.ReadDir("../..")
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	for _, entry := range entries {
		present[entry.Name()] = true
	}
	for _, name := range []string{"koyori-ide.exe", "NUL", "$profile"} {
		if present[name] {
			t.Errorf("repository hygiene file %q still exists", name)
		}
	}

	raw, err := os.ReadFile("../../.gitignore")
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		lines[strings.TrimSpace(line)] = true
	}
	for _, required := range []string{
		"bin/", "*.exe", "/koyori-ide.exe", "/NUL", "/$profile",
		"/.claude/", "/.omo/", ".task/",
	} {
		if !lines[required] {
			t.Errorf(".gitignore is missing exact rule %q", required)
		}
	}
}

func TestG17LocalClaudeSettingsContainOnlyPermissions(t *testing.T) {
	raw, err := os.ReadFile("../../.claude/settings.local.json")
	if err != nil {
		// 该文件是开发者本机的 .claude 本地配置（被 /.claude/ 规则
		// gitignore）。文件不存在（CI、全新 clone）时无从守护，直接跳过；
		// 存在时必须只含 permissions 键。
		if errors.Is(err, fs.ErrNotExist) {
			t.Skip(".claude/settings.local.json is not present on this machine")
		}
		t.Fatal(err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("parse .claude/settings.local.json: %v", err)
	}
	if len(settings) != 1 || settings["permissions"] == nil {
		t.Fatalf("local Claude settings must contain only the permissions key; found keys=%v", mapKeys(settings))
	}
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func TestG17NoticeAndLicenseInventoryMatchDependencyDigests(t *testing.T) {
	notice, err := os.ReadFile("../../NOTICE")
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := os.ReadFile("../../docs/THIRD_PARTY_LICENSES.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"docs/THIRD_PARTY_LICENSES.md", "four supported `desktop,production` Go package closures", "No GPL or", "AGPL identifier", "not a signed", "attestation",
	} {
		if !strings.Contains(string(notice), required) {
			t.Errorf("NOTICE is missing %q", required)
		}
	}
	for _, source := range []string{"../../go.mod", "../../go.sum", "../../frontend/package-lock.json"} {
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		if !strings.Contains(string(inventory), hex.EncodeToString(digest[:])) {
			t.Errorf("license inventory is stale for %s", source)
		}
	}
	for _, required := range []string{
		"Unknown or unclassified licenses: 0",
		"Documented Go source exceptions requiring release review: 0",
		"A zero strong-copyleft count applies only to the classified production closure rows",
	} {
		if !strings.Contains(string(inventory), required) {
			t.Errorf("license inventory is missing review boundary %q", required)
		}
	}
}

func TestG17SBOMScriptsFailClosed(t *testing.T) {
	for _, name := range []string{"../../scripts/generate-sbom.sh", "../../scripts/release-evidence.sh"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, forbidden := range []string{"SKIP_SBOM", "SBOM: skipped", "SBOM (optional)", "|| true"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s retains best-effort SBOM path %q", name, forbidden)
			}
		}
	}
}
