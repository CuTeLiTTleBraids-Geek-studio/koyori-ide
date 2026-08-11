package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P9-G09: the release workflow must stay runnable on the macOS default Bash
// 3.2 (no mapfile/readarray/declare -A/${var^^}) and BSD find (no
// -maxdepth/-printf). This static gate is T-level: it locks the portable
// constructs in and the non-portable ones out. Real macOS execution (AC1/AC4)
// requires a GitHub macOS runner and is recorded separately.
func TestReleaseWorkflowIsBSDAndBash32Portable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	for _, forbidden := range []string{
		"mapfile", "readarray", "declare -A", "${var^^", "${var,,",
		"-maxdepth", "-printf", "sort -z",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("release.yml still uses non-macOS-portable construct %q", forbidden)
		}
	}

	// Deterministic single/zero-candidate selection must be present for every
	// platform artifact list and for the release-assets enumeration.
	for _, required := range []string{
		"while IFS= read -r line; do config_versions+=",
		"while IFS= read -r line; do windows_versions+=",
		"while IFS= read -r line; do nfpm_versions+=",
		"while IFS= read -r line; do darwin_versions+=",
		"for candidate in bin/koyori-ide*; do",
		"for candidate in bin/koyori-ide*.app; do",
		"for candidate in release-assets/*; do",
		"for candidate in ./*; do",
		"-ne 1", // exactly one Linux/macOS candidate
		"-eq 0", // no candidates fails closed
	} {
		if !strings.Contains(text, required) {
			t.Errorf("release.yml is missing portable construct %q", required)
		}
	}
}

// TestReleaseWorkflowChecksumsExcludeMarker verifies the final checksum step
// still excludes its own output and regenerates after every asset exists.
func TestReleaseWorkflowChecksumsExcludeMarker(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "SHA256SUMS) continue") {
		t.Error("final checksum step must exclude the SHA256SUMS marker")
	}
}

// TestReleaseWorkflowChecksumFallback verifies the macOS-compatible checksum
// fallback: sha256sum is a GNU utility absent on macOS, so the workflow must
// fall back to `shasum -a 256` (BSD/macOS native).
func TestReleaseWorkflowChecksumFallback(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `command -v sha256sum`) {
		t.Error("checksum step must probe for sha256sum")
	}
	if !strings.Contains(text, `shasum -a 256`) {
		t.Error("checksum step must fall back to macOS-native shasum -a 256")
	}
}
