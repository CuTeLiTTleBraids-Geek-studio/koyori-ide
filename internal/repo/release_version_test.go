package repo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// GOAL-P0-05: release version consistency gate.
//
// Baseline defect: the version was declared independently in build/config.yml
// (0.2.0), frontend/package.json (0.0.0), README (v0.2.0), and SECURITY.md
// (which simultaneously called 0.4.x and 0.2.x the current release line). None
// of them was authoritative and nothing compared them, so a release could ship
// artifacts whose version disagreed with its own metadata.
//
// VERSION is now the single source. The checks below are a pure function over
// explicit inputs so that drift scenarios can be exercised directly, plus one
// test that feeds the real repository files through the same function.

// releaseMetadata is every place a version is declared, plus the support table.
type releaseMetadata struct {
	// Version is the authoritative value from the VERSION file.
	Version string
	// BuildConfig is the raw text of build/config.yml.
	BuildConfig string
	// PackageJSON is the raw text of frontend/package.json.
	PackageJSON string
	// Changelog is the raw text of docs/CHANGELOG.md.
	Changelog string
	// Security is the raw text of .github/SECURITY.md.
	Security string
	// WindowsManifest is the raw text of build/windows/wails.exe.manifest.
	WindowsManifest string
	// MSIXManifest is the raw text of build/windows/msix/app_manifest.xml.
	MSIXManifest string
}

var (
	buildConfigVersionRe = regexp.MustCompile(`(?m)^\s{2}version:\s*"([^"]+)"`)
	changelogSectionRe   = regexp.MustCompile(`(?m)^## \[([^\]]+)\]`)
	// A supported-version row whose "Supported" cell is a bare check mark.
	// Rows that additionally carry a qualifier (development / high severity
	// only / planned) are not current-release claims.
	securityCurrentRowRe = regexp.MustCompile(`(?m)^\|\s*\*\*([0-9]+\.[0-9]+\.x)\*\*\s*\|\s*✅[^|]*\|`)
	// The first assemblyIdentity element carries the application version.
	windowsManifestVersionRe = regexp.MustCompile(`^[\s\S]*?<assemblyIdentity[^>]*version="([^"]+)"`)
	// The MSIX Identity element carries a four-part package version.
	msixIdentityVersionRe = regexp.MustCompile(`^[\s\S]*?<Identity[^>]*Version="([^"]+)"`)
	numericSemverRe       = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)`)
)

// checkReleaseVersionConsistency returns every inconsistency it finds. An empty
// result means the metadata agrees with VERSION.
//
// It returns all problems rather than the first one so a release engineer sees
// the full drift in one run instead of rediscovering it file by file.
func checkReleaseVersionConsistency(meta releaseMetadata) []string {
	var problems []string

	version := strings.TrimSpace(meta.Version)
	if version == "" {
		return []string{"VERSION is empty; it is the single source of truth and must declare a version"}
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.\-]+)?(\+[0-9A-Za-z.\-]+)?$`).MatchString(version) {
		problems = append(problems, fmt.Sprintf("VERSION %q is not a SemVer version", version))
	}

	// build/config.yml drives the packaged artifact's metadata.
	if m := buildConfigVersionRe.FindStringSubmatch(meta.BuildConfig); m == nil {
		problems = append(problems, "build/config.yml declares no version")
	} else if m[1] != version {
		problems = append(problems, fmt.Sprintf(
			"build/config.yml version %q != VERSION %q", m[1], version))
	}

	// frontend/package.json is not published, but leaving it at 0.0.0 while the
	// app ships 0.2.0 is exactly the drift this gate exists to catch.
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(meta.PackageJSON), &pkg); err != nil {
		problems = append(problems, fmt.Sprintf("frontend/package.json is not valid JSON: %v", err))
	} else if pkg.Version != version {
		problems = append(problems, fmt.Sprintf(
			"frontend/package.json version %q != VERSION %q", pkg.Version, version))
	}

	// Windows application manifest (wails.exe.manifest): the first
	// assemblyIdentity version must equal VERSION.
	if m := windowsManifestVersionRe.FindStringSubmatch(meta.WindowsManifest); m == nil {
		problems = append(problems, "build/windows/wails.exe.manifest declares no assemblyIdentity version")
	} else if m[1] != version {
		problems = append(problems, fmt.Sprintf(
			"build/windows/wails.exe.manifest version %q != VERSION %q", m[1], version))
	}

	// MSIX package manifest: Identity Version is four-part and cannot carry
	// prerelease/build-metadata characters, so the tested mapping is
	// <major>.<minor>.<patch>.0. Unmappable versions already fail the SemVer
	// gate above.
	if m := msixIdentityVersionRe.FindStringSubmatch(meta.MSIXManifest); m == nil {
		problems = append(problems, "build/windows/msix/app_manifest.xml declares no Identity Version")
	} else {
		numeric := numericSemverRe.FindStringSubmatch(version)
		if numeric == nil {
			problems = append(problems, fmt.Sprintf("VERSION %q has no numeric triple for MSIX", version))
		} else {
			wantMSIX := numeric[1] + "." + numeric[2] + "." + numeric[3] + ".0"
			if m[1] != wantMSIX {
				problems = append(problems, fmt.Sprintf(
					"build/windows/msix/app_manifest.xml Version %q != mapped %q (VERSION %q)",
					m[1], wantMSIX, version))
			}
		}
	}

	// The release workflow reads the section matching the tag and fails without
	// it, so the section must exist before the tag is pushed.
	sections := changelogSectionRe.FindAllStringSubmatch(meta.Changelog, -1)
	var found bool
	var seen []string
	for _, s := range sections {
		seen = append(seen, s[1])
		if s[1] == version {
			found = true
		}
	}
	if !found {
		problems = append(problems, fmt.Sprintf(
			"docs/CHANGELOG.md has no '## [%s]' section (found: %s)",
			version, strings.Join(seen, ", ")))
	}
	// [Unreleased] must stay first so the ordering communicates what has
	// actually shipped.
	if len(sections) > 0 && sections[0][1] != "Unreleased" {
		problems = append(problems, fmt.Sprintf(
			"docs/CHANGELOG.md starts with '## [%s]'; [Unreleased] must come first",
			sections[0][1]))
	}

	// At most one release line may be presented as current. Two ✅ rows tell the
	// user to trust two different versions at once.
	current := securityCurrentRowRe.FindAllStringSubmatch(meta.Security, -1)
	if len(current) > 1 {
		var lines []string
		for _, c := range current {
			lines = append(lines, c[1])
		}
		problems = append(problems, fmt.Sprintf(
			"SECURITY.md marks %d release lines as current (%s); only one may be current",
			len(current), strings.Join(lines, ", ")))
	}
	if len(current) == 1 {
		// The current line must be the VERSION's minor line.
		parts := strings.SplitN(version, ".", 3)
		if len(parts) >= 2 {
			want := parts[0] + "." + parts[1] + ".x"
			if current[0][1] != want {
				problems = append(problems, fmt.Sprintf(
					"SECURITY.md current line is %s but VERSION %s implies %s",
					current[0][1], version, want))
			}
		}
	}

	return problems
}

// ---------------------------------------------------------------------------
// Legal scenario: the real repository files must agree.
// ---------------------------------------------------------------------------

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func TestReleaseVersionConsistency(t *testing.T) {
	meta := releaseMetadata{
		Version:         repoFile(t, "VERSION"),
		BuildConfig:     repoFile(t, filepath.Join("build", "config.yml")),
		PackageJSON:     repoFile(t, filepath.Join("frontend", "package.json")),
		Changelog:       repoFile(t, filepath.Join("docs", "CHANGELOG.md")),
		Security:        repoFile(t, filepath.Join(".github", "SECURITY.md")),
		WindowsManifest: repoFile(t, filepath.Join("build", "windows", "wails.exe.manifest")),
		MSIXManifest:    repoFile(t, filepath.Join("build", "windows", "msix", "app_manifest.xml")),
	}

	if problems := checkReleaseVersionConsistency(meta); len(problems) > 0 {
		t.Fatalf("release metadata is inconsistent with VERSION:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
}

// ---------------------------------------------------------------------------
// Drift scenarios: the gate must actually reject each historical failure mode.
// ---------------------------------------------------------------------------

// validMeta returns a self-consistent metadata set that individual drift tests
// mutate one field at a time.
func validMeta() releaseMetadata {
	return releaseMetadata{
		Version:     "0.2.0\n",
		BuildConfig: "info:\n  version: \"0.2.0\" # The application version\n",
		PackageJSON: `{"name":"frontend","version":"0.2.0"}`,
		Changelog:   "## [Unreleased]\n\n- wip\n\n## [0.2.0]\n\n- shipped\n",
		Security: "| 版本 | 支持 | 说明 |\n" +
			"|---|---|---|\n" +
			"| **0.2.x** | ✅ | current |\n" +
			"| **0.1.x** | ❌ | upgrade |\n",
		WindowsManifest: "<assemblyIdentity type=\"win32\" name=\"com.koyori.app\" version=\"0.2.0\" processorArchitecture=\"*\"/>",
		MSIXManifest:    "<Identity Name=\"com.koyori.app\" Version=\"0.2.0.0\" />",
	}
}

func TestReleaseVersionConsistency_AcceptsSelfConsistentMetadata(t *testing.T) {
	if problems := checkReleaseVersionConsistency(validMeta()); len(problems) > 0 {
		t.Fatalf("self-consistent metadata was rejected: %v", problems)
	}
}

func TestReleaseVersionConsistency_RejectsDrift(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*releaseMetadata)
		wantSub string
	}{
		{
			name: "build config version drift",
			mutate: func(m *releaseMetadata) {
				m.BuildConfig = "info:\n  version: \"0.5.0\"\n"
			},
			wantSub: "build/config.yml version",
		},
		{
			// The exact baseline state: app at 0.2.0, package.json left at 0.0.0.
			name: "package.json left at initial version",
			mutate: func(m *releaseMetadata) {
				m.PackageJSON = `{"name":"frontend","version":"0.0.0"}`
			},
			wantSub: "frontend/package.json version",
		},
		{
			name: "changelog missing the released section",
			mutate: func(m *releaseMetadata) {
				m.Changelog = "## [Unreleased]\n\n- wip\n"
			},
			wantSub: "has no '## [0.2.0]' section",
		},
		{
			name: "changelog ordering puts a release above Unreleased",
			mutate: func(m *releaseMetadata) {
				m.Changelog = "## [0.2.0]\n\n- shipped\n\n## [Unreleased]\n\n- wip\n"
			},
			wantSub: "[Unreleased] must come first",
		},
		{
			// The exact baseline state: 0.4.x and 0.2.x both marked ✅.
			name: "two conflicting current release lines",
			mutate: func(m *releaseMetadata) {
				m.Security = "| 版本 | 支持 | 说明 |\n" +
					"|---|---|---|\n" +
					"| **0.4.x** | ✅ | tag v0.4.0 |\n" +
					"| **0.2.x** | ✅ | current release line |\n"
			},
			wantSub: "marks 2 release lines as current",
		},
		{
			name: "current line disagrees with VERSION",
			mutate: func(m *releaseMetadata) {
				m.Security = "| 版本 | 支持 | 说明 |\n" +
					"|---|---|---|\n" +
					"| **0.4.x** | ✅ | current |\n"
			},
			wantSub: "implies 0.2.x",
		},
		{
			name: "empty VERSION",
			mutate: func(m *releaseMetadata) {
				m.Version = "\n"
			},
			wantSub: "VERSION is empty",
		},
		{
			name: "non-SemVer VERSION",
			mutate: func(m *releaseMetadata) {
				m.Version = "0.2\n"
			},
			wantSub: "not a SemVer version",
		},
		{
			name: "build config declares no version",
			mutate: func(m *releaseMetadata) {
				m.BuildConfig = "info:\n  productName: Koyori IDE\n"
			},
			wantSub: "declares no version",
		},
		{
			// The exact baseline state: Windows manifest left at 0.1.0.
			name: "windows manifest version drift",
			mutate: func(m *releaseMetadata) {
				m.WindowsManifest = "<assemblyIdentity type=\"win32\" name=\"com.koyori.app\" version=\"0.1.0\" processorArchitecture=\"*\"/>"
			},
			wantSub: "wails.exe.manifest version",
		},
		{
			// The exact baseline state: MSIX left at 0.1.0.0.
			name: "msix identity version drift",
			mutate: func(m *releaseMetadata) {
				m.MSIXManifest = "<Identity Name=\"com.koyori.app\" Version=\"0.1.0.0\" />"
			},
			wantSub: "app_manifest.xml Version",
		},
		{
			name: "windows manifest declares no version",
			mutate: func(m *releaseMetadata) {
				m.WindowsManifest = "<assemblyIdentity type=\"win32\" name=\"com.koyori.app\" processorArchitecture=\"*\"/>"
			},
			wantSub: "declares no assemblyIdentity version",
		},
		{
			name: "msix identity declares no version",
			mutate: func(m *releaseMetadata) {
				m.MSIXManifest = "<Identity Name=\"com.koyori.app\" />"
			},
			wantSub: "declares no Identity Version",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta := validMeta()
			tc.mutate(&meta)
			problems := checkReleaseVersionConsistency(meta)
			if len(problems) == 0 {
				t.Fatalf("drift was accepted; want a problem containing %q", tc.wantSub)
			}
			joined := strings.Join(problems, "; ")
			if !strings.Contains(joined, tc.wantSub) {
				t.Fatalf("problems = %q, want one containing %q", joined, tc.wantSub)
			}
		})
	}
}

// TestReleaseVersionConsistency_AcceptsBuildMetadata verifies SemVer build
// metadata (+suffix) is a valid VERSION: every consumer keeps the full string
// and MSIX maps to the numeric triple.
func TestReleaseVersionConsistency_AcceptsBuildMetadata(t *testing.T) {
	meta := validMeta()
	meta.Version = "0.2.0+build.7"
	meta.BuildConfig = "info:\n  version: \"0.2.0+build.7\" # The application version\n"
	meta.PackageJSON = `{"name":"frontend","version":"0.2.0+build.7"}`
	meta.WindowsManifest = "<assemblyIdentity type=\"win32\" name=\"com.koyori.app\" version=\"0.2.0+build.7\" processorArchitecture=\"*\"/>"
	// MSIX keeps the numeric triple regardless of the suffix.
	meta.Changelog = "## [Unreleased]\n\n- wip\n\n## [0.2.0+build.7]\n\n- shipped\n"
	meta.MSIXManifest = "<Identity Name=\"com.koyori.app\" Version=\"0.2.0.0\" />"

	if problems := checkReleaseVersionConsistency(meta); len(problems) > 0 {
		t.Fatalf("build-metadata VERSION was rejected: %v", problems)
	}
}

// TestReleaseVersionConsistency_PrereleaseMSIXMapping verifies the explicit
// four-part MSIX mapping under a prerelease VERSION.
func TestReleaseVersionConsistency_PrereleaseMSIXMapping(t *testing.T) {
	meta := validMeta()
	meta.Version = "0.3.0-rc.1"
	meta.BuildConfig = "info:\n  version: \"0.3.0-rc.1\"\n"
	meta.PackageJSON = `{"name":"frontend","version":"0.3.0-rc.1"}`
	meta.WindowsManifest = "<assemblyIdentity type=\"win32\" name=\"com.koyori.app\" version=\"0.3.0-rc.1\" processorArchitecture=\"*\"/>"
	meta.MSIXManifest = "<Identity Name=\"com.koyori.app\" Version=\"0.3.0.0\" />"
	meta.Changelog = "## [Unreleased]\n\n- wip\n\n## [0.3.0-rc.1]\n\n- shipped\n"
	meta.Security = "| 版本 | 支持 | 说明 |\n" +
		"|---|---|---|\n" +
		"| **0.3.x** | ✅ | current |\n" +
		"| **0.2.x** | ❌ | upgrade |\n"

	if problems := checkReleaseVersionConsistency(meta); len(problems) > 0 {
		t.Fatalf("prerelease VERSION with mapped MSIX was rejected: %v", problems)
	}

	// A wrong four-part value under a prerelease VERSION must be rejected.
	meta.MSIXManifest = "<Identity Name=\"com.koyori.app\" Version=\"0.3.0.1\" />"
	problems := checkReleaseVersionConsistency(meta)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "; "), "app_manifest.xml Version") {
		t.Fatalf("MSIX prerelease mapping drift was accepted: %v", problems)
	}
}

// TestReleaseVersionConsistency_ReportsAllDriftAtOnce locks the all-problems
// contract: a release engineer must see the full drift in one run.
func TestReleaseVersionConsistency_ReportsAllDriftAtOnce(t *testing.T) {
	meta := validMeta()
	meta.BuildConfig = "info:\n  version: \"0.5.0\"\n"
	meta.PackageJSON = `{"name":"frontend","version":"0.0.0"}`

	problems := checkReleaseVersionConsistency(meta)
	if len(problems) < 2 {
		t.Fatalf("problems = %v, want both build config and package.json drift", problems)
	}
}
