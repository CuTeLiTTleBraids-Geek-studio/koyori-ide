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
