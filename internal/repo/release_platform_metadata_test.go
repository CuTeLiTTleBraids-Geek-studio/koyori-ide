package repo

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	releaseBundleID    = "com.koyori.app"
	releaseHomepage    = "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide"
	releaseProductName = "Koyori IDE"
	releaseVendor      = "Koyori IDE Contributors"
)

func readPlistStrings(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	values := make(map[string]string)
	decoder := xml.NewDecoder(f)
	var pendingKey string
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("parse %s: %v", path, err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			if err := decoder.DecodeElement(&pendingKey, &start); err != nil {
				t.Fatalf("parse key in %s: %v", path, err)
			}
		case "string":
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				t.Fatalf("parse value in %s: %v", path, err)
			}
			if pendingKey != "" {
				values[pendingKey] = value
				pendingKey = ""
			}
		}
	}
	return values
}

func TestReleaseMetadataSyncCheck(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	cmd := exec.Command(node, filepath.Join("..", "..", "scripts", "sync-release-metadata.mjs"), "--check")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("release metadata sync check: %v\n%s", err, output)
	}
}

func TestPlatformReleaseMetadataMatchesVERSION(t *testing.T) {
	version := strings.TrimSpace(repoFile(t, "VERSION"))

	var windows struct {
		Fixed struct {
			FileVersion    string `json:"file_version"`
			ProductVersion string `json:"product_version"`
		} `json:"fixed"`
		Info map[string]struct {
			ProductVersion string `json:"ProductVersion"`
			ProductName    string `json:"ProductName"`
			CompanyName    string `json:"CompanyName"`
		} `json:"info"`
	}
	if err := json.Unmarshal([]byte(repoFile(t, filepath.Join("build", "windows", "info.json"))), &windows); err != nil {
		t.Fatalf("parse Windows metadata: %v", err)
	}
	if windows.Fixed.FileVersion != version {
		t.Errorf("Windows file_version = %q, want %q", windows.Fixed.FileVersion, version)
	}
	if windows.Fixed.ProductVersion != version {
		t.Errorf("Windows product_version = %q, want %q", windows.Fixed.ProductVersion, version)
	}
	// The string table language key is en-US (0409); every table must carry
	// the authoritative ProductVersion.
	foundProductVersion := false
	for _, table := range windows.Info {
		if table.ProductVersion == version {
			foundProductVersion = true
		}
		if table.ProductName != releaseProductName {
			t.Errorf("Windows ProductName = %q, want %q", table.ProductName, releaseProductName)
		}
		if table.CompanyName != releaseVendor {
			t.Errorf("Windows CompanyName = %q, want %q", table.CompanyName, releaseVendor)
		}
	}
	if !foundProductVersion {
		t.Errorf("Windows info string tables (%d) do not carry ProductVersion %q", len(windows.Info), version)
	}

	var linux struct {
		Version  string `yaml:"version"`
		Vendor   string `yaml:"vendor"`
		Homepage string `yaml:"homepage"`
	}
	if err := yaml.Unmarshal([]byte(repoFile(t, filepath.Join("build", "linux", "nfpm", "nfpm.yaml"))), &linux); err != nil {
		t.Fatalf("parse Linux metadata: %v", err)
	}
	if linux.Version != version {
		t.Errorf("nfpm version = %q, want %q", linux.Version, version)
	}
	if linux.Vendor != releaseVendor {
		t.Errorf("nfpm vendor = %q, want %q", linux.Vendor, releaseVendor)
	}
	if linux.Homepage != releaseHomepage {
		t.Errorf("nfpm homepage = %q, want %q", linux.Homepage, releaseHomepage)
	}

	darwin := readPlistStrings(t, filepath.Join("..", "..", "build", "darwin", "Info.plist"))
	for _, key := range []string{"CFBundleVersion", "CFBundleShortVersionString"} {
		if darwin[key] != version {
			t.Errorf("Info.plist %s = %q, want %q", key, darwin[key], version)
		}
	}
	if darwin["CFBundleExecutable"] != "koyori-ide" {
		t.Errorf("Info.plist executable = %q, want koyori-ide", darwin["CFBundleExecutable"])
	}
	if darwin["CFBundleName"] != releaseProductName {
		t.Errorf("Info.plist CFBundleName = %q, want %q", darwin["CFBundleName"], releaseProductName)
	}
	if darwin["CFBundleIdentifier"] != releaseBundleID {
		t.Errorf("Info.plist bundle ID = %q, want %q", darwin["CFBundleIdentifier"], releaseBundleID)
	}
	if raw := repoFile(t, filepath.Join("build", "darwin", "Info.plist")); strings.Contains(raw, "gugacode") || strings.Contains(raw, "My Company") || strings.Contains(raw, "koyori-ide.exe") {
		t.Error("Info.plist still contains template identity values")
	}
}

func TestBuildScriptsReadVERSIONAndFailClosed(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	probe := filepath.Join(t.TempDir(), "path-probe.sh")
	if err := os.WriteFile(probe, []byte("#!/usr/bin/env bash\nprintf 'bash-path-ok\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	probeOutput, err := exec.Command(bash, probe).CombinedOutput()
	if err != nil || strings.TrimSpace(string(probeOutput)) != "bash-path-ok" {
		t.Skipf("bash cannot execute native fixture paths (%s): %v", bash, err)
	}
	want := strings.TrimSpace(repoFile(t, "VERSION"))
	for _, rel := range []string{
		filepath.Join("build", "scripts", "build-macos.sh"),
		filepath.Join("build", "scripts", "build-linux.sh"),
	} {
		rel := rel
		t.Run(filepath.Base(rel), func(t *testing.T) {
			cmd := exec.Command(bash, filepath.Join("..", "..", rel), "--print-version")
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("print version: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != want {
				t.Fatalf("printed version = %q, want %q", got, want)
			}

			fakeRoot := t.TempDir()
			fakeScriptDir := filepath.Join(fakeRoot, "build", "scripts")
			if err := os.MkdirAll(fakeScriptDir, 0o755); err != nil {
				t.Fatal(err)
			}
			fakeScript := filepath.Join(fakeScriptDir, filepath.Base(rel))
			if err := os.WriteFile(fakeScript, []byte(repoFile(t, rel)), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd = exec.Command(bash, fakeScript, "--print-version")
			output, err = cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("missing VERSION unexpectedly succeeded: %s", output)
			}
			if !strings.Contains(string(output), "VERSION") {
				t.Fatalf("missing VERSION failure is not actionable: %s", output)
			}
		})
	}
}

func TestReleaseVersionReaderPreservesOnlyOneLineEnding(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	tempDir, err := os.MkdirTemp(".", ".version-reader-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)
	script := "../../scripts/read-release-version.sh"
	cases := []struct {
		name    string
		content string
		wantOK  bool
	}{
		{name: "no terminator", content: "1.2.3", wantOK: true},
		{name: "LF", content: "1.2.3\n", wantOK: true},
		{name: "CRLF", content: "1.2.3\r\n", wantOK: true},
		{name: "lone CR", content: "1.2.3\r"},
		{name: "two LF", content: "1.2.3\n\n"},
		{name: "two CRLF", content: "1.2.3\r\n\r\n"},
		{name: "embedded NUL", content: "1.2.3\x00\n"},
		{name: "trailing data", content: "1.2.3\njunk"},
		{name: "leading zero", content: "01.2.3\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := filepath.Join(tempDir, "VERSION")
			if err := os.WriteFile(fixture, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command(bash, script, filepath.ToSlash(fixture)).CombinedOutput()
			if tc.wantOK {
				if err != nil {
					t.Fatalf("reader rejected valid VERSION: %v\n%s", err, output)
				}
				if got := string(output); !strings.HasSuffix(got, "1.2.3\n") {
					t.Fatalf("reader output = %q, want a final %q", got, "1.2.3\\n")
				}
				return
			}
			if err == nil {
				t.Fatalf("reader accepted malformed VERSION: %q", tc.content)
			}
		})
	}
}

func TestBuildScriptsRequireViteCompatibleNode(t *testing.T) {
	if lock := repoFile(t, filepath.Join("frontend", "package-lock.json")); !strings.Contains(lock, `"node": "^20.19.0 || >=22.12.0"`) {
		t.Fatal("frontend lockfile no longer contains the Vite Node engine boundary; update this contract with the toolchain")
	}

	tests := []struct {
		rel      string
		required []string
	}{
		{
			rel: filepath.Join("build", "scripts", "build-linux.sh"),
			required: []string{
				`[ "$NODE_MAJOR" -eq 20 ] && [ "$NODE_MINOR" -ge 19 ]`,
				`[ "$NODE_MAJOR" -eq 22 ] && [ "$NODE_MINOR" -ge 12 ]`,
				`[ "$NODE_MAJOR" -gt 22 ]`,
			},
		},
		{
			rel: filepath.Join("build", "scripts", "build-macos.sh"),
			required: []string{
				`[ "$NODE_MAJOR" -eq 20 ] && [ "$NODE_MINOR" -ge 19 ]`,
				`[ "$NODE_MAJOR" -eq 22 ] && [ "$NODE_MINOR" -ge 12 ]`,
				`[ "$NODE_MAJOR" -gt 22 ]`,
			},
		},
		{
			rel: filepath.Join("build", "scripts", "build-windows.ps1"),
			required: []string{
				`$NodeMajor -eq 20 -and $NodeMinor -ge 19`,
				`$NodeMajor -eq 22 -and $NodeMinor -ge 12`,
				`$NodeMajor -gt 22`,
			},
		},
	}
	for _, test := range tests {
		script := repoFile(t, test.rel)
		for _, required := range append(test.required, "^20.19.0", ">=22.12.0") {
			if !strings.Contains(script, required) {
				t.Errorf("%s does not enforce Vite's Node engine boundary %q", test.rel, required)
			}
		}
		for _, stale := range []string{"Node.js 18+", "需要 18+", "NODE_MAJOR\" -lt 18", "NodeMajor -lt 18"} {
			if strings.Contains(script, stale) {
				t.Errorf("%s retains stale Node requirement %q", test.rel, stale)
			}
		}
	}
}

func TestBuildScriptsRequireStableVersions(t *testing.T) {
	stableVersion := `^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`
	for _, rel := range []string{
		filepath.Join("build", "scripts", "build-linux.sh"),
		filepath.Join("build", "scripts", "build-macos.sh"),
	} {
		script := repoFile(t, rel)
		if !strings.Contains(script, "read-release-version.sh") {
			t.Errorf("%s does not use the exact shared VERSION reader", rel)
		}
		if strings.Contains(script, "sed 's/\\r$//'") || strings.Contains(script, "tr -d") {
			t.Errorf("%s contains a lossy VERSION normalizer", rel)
		}
	}
	reader := repoFile(t, filepath.Join("scripts", "read-release-version.sh"))
	if !strings.Contains(reader, stableVersion) || !strings.Contains(reader, "exactly one value") {
		t.Error("shared VERSION reader must enforce stable syntax and exact line shape")
	}
	for _, rel := range []string{
		filepath.Join("build", "scripts", "build-windows.ps1"),
		filepath.Join("build", "scripts", "build-msi.ps1"),
	} {
		script := repoFile(t, rel)
		if !strings.Contains(script, stableVersion) {
			t.Errorf("%s does not enforce the stable VERSION policy", rel)
		}
	}
	for _, rel := range []string{
		filepath.Join("build", "scripts", "build-linux.sh"),
		filepath.Join("build", "scripts", "build-macos.sh"),
		filepath.Join("build", "scripts", "build-windows.ps1"),
		filepath.Join("build", "scripts", "build-msi.ps1"),
	} {
		if script := repoFile(t, rel); !strings.Contains(script, "node scripts/sync-release-metadata.mjs --check") {
			t.Errorf("%s can build without verifying synchronized release metadata", rel)
		}
	}
	msi := repoFile(t, filepath.Join("build", "scripts", "build-msi.ps1"))
	for _, required := range []string{
		"$VersionMatch = [regex]::Match",
		"$VersionMatch.Groups[1].Value",
		"$VersionMatch.Groups[2].Value",
		"$VersionMatch.Groups[3].Value",
		"VERSION exceeds MSI limits",
	} {
		if !strings.Contains(msi, required) {
			t.Errorf("build-msi.ps1 does not map SemVer to numeric MSI ProductVersion via %q", required)
		}
	}
	if strings.Contains(msi, "$MsiVersion = ($Version -split '-')[0]") {
		t.Error("build-msi.ps1 must derive ProductVersion from validated numeric groups")
	}
}
