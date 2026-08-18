package repo

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type releaseWorkflow struct {
	On          map[string]yaml.Node          `yaml:"on"`
	Permissions map[string]string             `yaml:"permissions"`
	Env         map[string]string             `yaml:"env"`
	Jobs        map[string]releaseWorkflowJob `yaml:"jobs"`
}

type releaseWorkflowJob struct {
	Strategy struct {
		Matrix struct {
			Include []map[string]string `yaml:"include"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Uses        string                `yaml:"uses"`
	Needs       workflowNeeds         `yaml:"needs"`
	Environment string                `yaml:"environment"`
	Permissions map[string]string     `yaml:"permissions"`
	Env         map[string]string     `yaml:"env"`
	Outputs     map[string]string     `yaml:"outputs"`
	Secrets     map[string]string     `yaml:"secrets"`
	Steps       []releaseWorkflowStep `yaml:"steps"`
}

type releaseWorkflowStep struct {
	Name            string            `yaml:"name"`
	ID              string            `yaml:"id"`
	Shell           string            `yaml:"shell"`
	Run             string            `yaml:"run"`
	Uses            string            `yaml:"uses"`
	Env             map[string]string `yaml:"env"`
	With            map[string]string `yaml:"with"`
	ContinueOnError bool              `yaml:"continue-on-error"`
}

type workflowNeeds []string

func (needs *workflowNeeds) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case 0:
		return nil
	case yaml.ScalarNode:
		*needs = []string{value.Value}
		return nil
	case yaml.SequenceNode:
		for _, item := range value.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("needs entry must be a scalar, got YAML kind %d", item.Kind)
			}
			*needs = append(*needs, item.Value)
		}
		return nil
	default:
		return fmt.Errorf("needs must be a scalar or sequence, got YAML kind %d", value.Kind)
	}
}

func TestReleaseWorkflowContract(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}
	build, ok := workflow.Jobs["build"]
	if !ok {
		t.Fatal("release workflow has no build job")
	}
	if got := build.Env["GOARCH"]; got != "${{ matrix.arch }}" {
		t.Fatalf("build GOARCH = %q, want matrix.arch", got)
	}
	qualityGate, ok := workflow.Jobs["quality-gate"]
	if !ok {
		t.Fatal("release workflow has no quality-gate job")
	}
	if got := qualityGate.Permissions["checks"]; got != "read" {
		t.Fatalf("quality-gate checks permission = %q, want read", got)
	}
	if got := qualityGate.Permissions["actions"]; got != "read" {
		t.Fatalf("quality-gate actions permission = %q, want read", got)
	}
	if !containsString(build.Needs, "quality-gate") {
		t.Error("build job must wait for the same-commit quality-gate job")
	}
	packagedE2E, ok := workflow.Jobs["release-packaged-e2e"]
	if !ok {
		t.Fatal("release workflow has no packaged E2E gate")
	}
	if !containsString(packagedE2E.Needs, "quality-gate") || !containsString(build.Needs, "release-packaged-e2e") {
		t.Error("release builds must wait for real packaged E2E on the verified commit")
	}
	packagedText := ""
	for _, step := range packagedE2E.Steps {
		packagedText += step.Run + "\n" + step.Uses + "\n" + step.With["ref"] + "\n" + step.With["name"] + "\n"
	}
	for _, requirement := range []string{
		"${{ needs.quality-gate.outputs.commit_sha }}",
		"node scripts/packaged-e2e.mjs",
		"actions/upload-artifact@",
		"qualification-packaged-e2e-evidence",
	} {
		if !strings.Contains(packagedText, requirement) {
			t.Errorf("release packaged E2E gate is missing %q", requirement)
		}
	}
	publisher, ok := workflow.Jobs["release"]
	if !ok || !containsString(publisher.Needs, "release-packaged-e2e") {
		t.Error("final release publisher must wait for packaged E2E")
	}
	qualitySteps := map[string]string{}
	for _, step := range qualityGate.Steps {
		qualitySteps[step.Name] = step.Run
	}
	qualityRun := qualitySteps["Require successful checks for this commit"]
	for _, requirement := range []string{
		`tag_object_sha="$(gh api`, "tag_object_type", `GITHUB_SHA" != "$tag_object_sha"`,
		`GITHUB_SHA" != "$sha"`, `commit_sha=${sha}`,
		`sha="$(gh api`, "target_type", "actions/workflows/ci.yml/runs",
		`-f head_sha="$sha"`, "-f event=push", `.path == ".github/workflows/ci.yml"`,
		`.head_branch == "main"`, `run_id="$(jq -r '.id // empty'`,
		`run_conclusion="$(jq -r '.conclusion // ""'`, "/attempts/${run_attempt}/jobs",
		"match_count", "check-runs", `.app.slug == "github-actions"`, ".check_suite.id",
		"head_sha", "completed", "success",
		"Contract smoke, not packaged E2E (ubuntu-latest)",
		"Wails bindings (required)",
		"Go Build & Test (ubuntu-latest)",
		"Go Build & Test (windows-latest)",
		"Go Build & Test (macos-latest)",
		"Go Lint",
		"Frontend Check & Test (ubuntu-latest)",
		"Frontend Check & Test (windows-latest)",
		"Frontend Check & Test (macos-latest)",
		"Frontend Coverage",
		"Wails Production Build",
		"Govulncheck",
		"Performance Benchmark",
		"NPM Audit",
		"sleep 20",
	} {
		if !strings.Contains(qualityRun, requirement) {
			t.Errorf("quality-gate does not bind check lookup to %q", requirement)
		}
	}
	if got, want := qualityGate.Outputs["commit_sha"], "${{ steps.verify.outputs.commit_sha }}"; got != want {
		t.Errorf("quality-gate commit output = %q, want %q", got, want)
	}
	if strings.Contains(qualityRun, `sha" != "$GITHUB_SHA"`) {
		t.Error("quality-gate must tolerate either documented annotated-tag event SHA representation")
	}
	if strings.Contains(qualityRun, "@tsv") || strings.Contains(qualityRun, "IFS=$'\\t' read") {
		t.Error("quality-gate must preserve empty CI fields with JSON parsing instead of whitespace-delimited TSV")
	}
	if strings.Contains(qualityRun, `commits/${sha}/check-runs`) {
		t.Error("quality-gate must not aggregate same-name checks directly across all runs on a commit")
	}
	workflowText := string(raw)
	if !strings.Contains(workflowText, `- "v*.*.*"`) ||
		!strings.Contains(workflowText, `- "!v*.*.*-*"`) ||
		!strings.Contains(workflowText, `- '!v*.*.*\+*'`) {
		t.Error("release trigger must include stable vX.Y.Z tags and exclude prerelease/build-metadata tags")
	}

	wantMatrix := map[string]string{
		"windows-latest/amd64": ".zip",
		"ubuntu-latest/amd64":  ".tar.gz",
		"macos-15-intel/amd64": ".zip",
		"macos-15/arm64":       ".zip",
	}
	for _, entry := range build.Strategy.Matrix.Include {
		key := entry["os"] + "/" + entry["arch"]
		wantSuffix, expected := wantMatrix[key]
		if !expected {
			t.Errorf("unexpected release matrix entry %q", key)
			continue
		}
		artifactSuffix := entry["artifact_suffix"]
		if !strings.HasSuffix(artifactSuffix, wantSuffix) || strings.Contains(artifactSuffix, "${{") {
			t.Errorf("%s artifact suffix %q is not static and does not end in %s", key, artifactSuffix, wantSuffix)
		}
		delete(wantMatrix, key)
	}
	for missing := range wantMatrix {
		t.Errorf("release matrix is missing %s", missing)
	}

	steps := map[string]string{}
	for _, step := range build.Steps {
		steps[step.Name] = step.Run
		if strings.Contains(step.Run, "head -1") || strings.Contains(step.Run, "head -n 1") {
			t.Errorf("step %q silently selects the first build product", step.Name)
		}
	}
	versionCheck := steps["Verify tag matches all release metadata"]
	if !strings.Contains(versionCheck, "supported release tag") || !strings.Contains(versionCheck, "strict semantic version") {
		t.Error("release must validate a strict tag/version shape before using tag-derived paths")
	}
	if strings.Contains(versionCheck, "([.-][0-9A-Za-z.-]+)?") || !strings.Contains(versionCheck, "^v(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$") {
		t.Error("release tag validation must use the same strict X.Y.Z shape as VERSION")
	}
	for _, source := range []string{
		"VERSION", "build/config.yml", "build/windows/info.json",
		"build/linux/nfpm/nfpm.yaml", "build/darwin/Info.plist",
	} {
		if !strings.Contains(versionCheck, source) {
			t.Errorf("release metadata verification does not cover %s", source)
		}
	}
	for _, requirement := range []string{
		"refs/release-tags/", "git fetch --force --no-tags origin",
		`git cat-file -t "$tag_ref"`, `tag_object_sha="$(git rev-parse "$tag_ref")"`,
		`${tag_ref}^{commit}`, `tag_commit" != "$EXPECTED_COMMIT"`,
	} {
		if !strings.Contains(versionCheck, requirement) {
			t.Errorf("release tag verification is missing %q", requirement)
		}
	}
	if strings.Contains(versionCheck, `tag_commit" != "$GITHUB_SHA"`) {
		t.Error("release build must consume the independently peeled quality-gate commit, not reinterpret the event SHA")
	}
	if !strings.Contains(string(raw), "ref: ${{ needs.quality-gate.outputs.commit_sha }}") {
		t.Error("release build/final checkout must use the peeled quality-gate commit")
	}
	macArchitecture := steps["Verify macOS bundle architecture"]
	for _, requirement := range []string{"file -b", "wrong architecture", "-verify_arch", "lipo -archs", `expected_arch="${GOARCH}"`, "single ${expected_arch} slice"} {
		if !strings.Contains(macArchitecture, requirement) {
			t.Errorf("macOS architecture verification is missing %q", requirement)
		}
	}
	if linux := steps["Package (Linux)"]; !strings.Contains(linux, "tar -czf") || !strings.Contains(linux, "#artifacts[@]") {
		t.Error("Linux packaging must create tar.gz and assert exactly one binary")
	}
	if windows := steps["Stage unsigned Windows executable"]; !strings.Contains(windows, "Count -ne 1") || !strings.Contains(windows, "unsigned-portable") {
		t.Error("Windows build must reject ambiguous executables and stage an unsigned payload")
	}
	if mac := steps["Stage unsigned macOS app bundle"]; !strings.Contains(mac, "#app_bundles[@]") || !strings.Contains(mac, "tar -cf") || !strings.Contains(mac, ".payload") {
		t.Error("macOS build must reject ambiguous app bundles and stage an opaque unsigned payload")
	}
	if build.Environment != "" {
		t.Errorf("unsigned portable build environment = %q, want no protected release environment", build.Environment)
	}
	signer, ok := workflow.Jobs["sign-portable"]
	if !ok {
		t.Fatal("release workflow has no dedicated portable signing job")
	}
	if signer.Environment != "release" || !containsString(signer.Needs, "build") {
		t.Errorf("portable signer environment/needs = %q/%v, want release and build", signer.Environment, signer.Needs)
	}
	signerSteps := map[string]string{}
	for _, step := range signer.Steps {
		signerSteps[step.Name] = step.Run
	}
	packageWindows := signerSteps["Package signed Windows portable artifact"]
	if !strings.Contains(packageWindows, "Count -ne 1") || !strings.Contains(packageWindows, "Compress-Archive") {
		t.Error("portable signer must package exactly one signed Windows executable")
	}
	packageMac := signerSteps["Package signed macOS portable artifact"]
	if !strings.Contains(packageMac, "ditto -c -k --keepParent") || !strings.Contains(packageMac, "test -d") {
		t.Error("portable signer must package the signed macOS app")
	}
	for name, run := range steps {
		if strings.Contains(run, "${{ github.ref_name }}") || strings.Contains(run, "${{ matrix.artifact_name }}") {
			t.Errorf("release step %q interpolates a Git ref directly into a shell command", name)
		}
	}
}

func TestPackageWorkflowCoversArtifactInputs(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/package.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On map[string]struct {
			Paths []string `yaml:"paths"`
		} `yaml:"on"`
		Jobs map[string]releaseWorkflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("parse package workflow: %v", err)
	}

	want := []string{
		"VERSION", "go.mod", "go.sum", "*.go", "services/**", "internal/**",
		"frontend/**", "Taskfile.yml", "scripts/**", "build/**",
		".github/workflows/package.yml",
	}
	for _, event := range []string{"push", "pull_request"} {
		trigger, ok := workflow.On[event]
		if !ok {
			t.Errorf("package workflow has no %s trigger", event)
			continue
		}
		for _, path := range want {
			if !containsString(trigger.Paths, path) {
				t.Errorf("package workflow %s paths do not cover %q", event, path)
			}
		}
	}
	metadata, ok := workflow.Jobs["metadata"]
	if !ok {
		t.Fatal("package workflow has no release metadata preflight job")
	}
	metadataText := ""
	for _, step := range metadata.Steps {
		metadataText += step.Run + "\n" + step.Uses + "\n"
	}
	for _, requirement := range []string{
		"bash scripts/read-release-version.sh VERSION",
		"node scripts/sync-release-metadata.mjs --check",
	} {
		if !strings.Contains(metadataText, requirement) {
			t.Errorf("package metadata preflight is missing %q", requirement)
		}
	}
	for _, jobName := range []string{"macos", "linux", "windows"} {
		if !containsString(workflow.Jobs[jobName].Needs, "metadata") {
			t.Errorf("package %s job must wait for release metadata preflight", jobName)
		}
	}
	macOS, ok := workflow.Jobs["macos"]
	if !ok {
		t.Fatal("package workflow has no macos job")
	}
	wantMacRunners := map[string]string{
		"arm64": "macos-15",
		"amd64": "macos-15-intel",
	}
	for _, entry := range macOS.Strategy.Matrix.Include {
		arch := entry["arch"]
		wantRunner, expected := wantMacRunners[arch]
		if !expected {
			continue
		}
		if got := entry["runner"]; got != wantRunner {
			t.Errorf("package macOS %s runner = %q, want %q", arch, got, wantRunner)
		}
		delete(wantMacRunners, arch)
	}
	for arch := range wantMacRunners {
		t.Errorf("package macOS matrix is missing %s", arch)
	}
	text := string(raw)
	for _, requirement := range []string{
		"create-dmg/archive/refs/tags/v1.3.0.tar.gz",
		"c50d2bc97c3d6292642bac55f530d247eaf4bf65ee605f26b4caf339383e381c",
		"choco install wixtoolset --version=3.14.1.20250415",
		"app_binary=bin/koyori-ide.app/Contents/MacOS/koyori-ide",
		"test -s bin/koyori-ide",
		"-verify_arch",
		"macOS app must contain exactly the ${expected_arch} slice",
		"Expected exactly one non-empty macOS DMG",
		`version="$(bash scripts/read-release-version.sh VERSION)"`,
		`expected_appimage="bin/koyori-ide-${version}-linux-${{ matrix.arch }}.AppImage"`,
		`$expectedMsi = "bin/koyori-ide-v$version-windows-${{ matrix.arch }}.msi"`,
		"Get-Item -LiteralPath $artifact -ErrorAction Stop",
		"Empty Windows artifact",
		"Expected exactly one Windows MSI named",
		"if-no-files-found: error",
	} {
		if !strings.Contains(text, requirement) {
			t.Errorf("package workflow does not pin installer toolchain requirement %q", requirement)
		}
	}
	if strings.Contains(text, "brew install create-dmg") {
		t.Error("package workflow installs mutable Homebrew create-dmg instead of the checksummed source")
	}

	stableVersionFragments := []string{
		`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`,
	}
	for _, jobName := range []string{"macos", "linux", "windows"} {
		job := workflow.Jobs[jobName]
		var verify, upload *releaseWorkflowStep
		for i := range job.Steps {
			step := &job.Steps[i]
			if step.ID == "verify" {
				verify = step
			}
			if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				upload = step
			}
		}
		if verify == nil || upload == nil {
			t.Fatalf("package %s job must have id=verify and an upload-artifact step", jobName)
		}
		for _, fragment := range stableVersionFragments {
			if !strings.Contains(verify.Run, fragment) {
				t.Errorf("package %s verification does not enforce stable VERSION %q", jobName, fragment)
			}
		}
		if upload.With["if-no-files-found"] != "error" {
			t.Errorf("package %s upload must set if-no-files-found: error, got %q", jobName, upload.With["if-no-files-found"])
		}
		if strings.Contains(upload.With["path"], "*") {
			t.Errorf("package %s upload path must be an exact verified allowlist, got %q", jobName, upload.With["path"])
		}
	}
	wantUploadPaths := map[string][]string{
		"macos":   {"${{ steps.verify.outputs.portable_path }}", "${{ steps.verify.outputs.dmg_path }}"},
		"linux":   {"${{ steps.verify.outputs.portable_path }}", "${{ steps.verify.outputs.deb_path }}", "${{ steps.verify.outputs.rpm_path }}"},
		"windows": {"${{ steps.verify.outputs.exe_path }}", "${{ steps.verify.outputs.msi_path }}"},
	}
	for jobName, requiredPaths := range wantUploadPaths {
		for _, step := range workflow.Jobs[jobName].Steps {
			if !strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				continue
			}
			for _, required := range requiredPaths {
				if !strings.Contains(step.With["path"], required) {
					t.Errorf("package %s upload path is missing verified entry %q", jobName, required)
				}
			}
		}
	}
	for _, jobName := range []string{"macos", "linux"} {
		job := workflow.Jobs[jobName]
		for _, step := range job.Steps {
			if step.ID != "verify" {
				continue
			}
			for _, requirement := range []string{"portable_archive=", `tar -czf "$portable_archive"`, `test -s "$portable_archive"`, "portable_path="} {
				if !strings.Contains(step.Run, requirement) {
					t.Errorf("package %s verification does not preserve executable metadata via %q", jobName, requirement)
				}
			}
		}
	}
	macBuild, err := os.ReadFile("../../build/scripts/build-macos.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(macBuild), "DMG creation failed") || !strings.Contains(string(macBuild), "[ ! -s \"$DMG_PATH\" ]") {
		t.Error("macOS build script must fail closed when create-dmg does not produce an artifact")
	}
}

func TestCIQualityGatesCoverServerGateway(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, requirement := range []string{
		"./build/docker/server-gateway/...",
		"gofmt -l services/ internal/repo/ build/docker/server-gateway/ *.go",
		"goimports -l services/ internal/repo/ build/docker/server-gateway/ *.go",
	} {
		if !strings.Contains(text, requirement) {
			t.Errorf("CI quality gates do not cover server gateway requirement %q", requirement)
		}
	}
}

func TestReleaseDocumentationInvokesRealGates(t *testing.T) {
	raw, err := os.ReadFile("../../docs/RELEASING.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "go test . -run") {
		t.Error("release documentation must not run a root package test that can match no tests")
	}
	for _, requirement := range []string{
		"go test ./internal/repo -run 'Version|Release|PlatformReleaseMetadata'",
		"node scripts/backend-gate.mjs",
		"node scripts/npm-audit-gate.mjs",
		"node scripts/check-release-assets.mjs --check --require-dist",
		"release-packaged-e2e",
	} {
		if !strings.Contains(text, requirement) {
			t.Errorf("release documentation is missing gate %q", requirement)
		}
	}
	pinGate, err := os.ReadFile("../../scripts/check-wails-pin.mjs")
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range []string{".github/workflows/release-installers.yml", "build/scripts/build-windows.ps1"} {
		if !strings.Contains(string(pinGate), requirement) {
			t.Errorf("Wails pin gate does not cover %q", requirement)
		}
	}
}

func TestReleaseInstallerArtifactsUseExactNamesAndPortableChecksums(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/release-installers.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, requirement := range []string{
		`base="bin/koyori-ide-${version}-linux-${{ matrix.arch }}"`,
		`artifacts=("${base}.AppImage" "${base}.deb" "${base}.rpm")`,
		"release-linux/",
		`sha256sum "$artifact_name" > "$artifact_name.sha256"`,
		`shasum -a 256 "$artifact_name" > "$artifact_name.sha256"`,
		"[IO.File]::WriteAllText($checksumPath,",
		"[Text.Encoding]::ASCII)",
	} {
		if !strings.Contains(text, requirement) {
			t.Errorf("release installer workflow is missing exact artifact contract %q", requirement)
		}
	}
}

func TestPerformanceWorkflowDoesNotBootstrapItsOwnBaseline(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, requirement := range []string{
		"BASE_SHA:",
		"git worktree add --detach",
		"go test -run '^$' -bench=. -benchmem -count=10 ./services/...",
		"-format csv benchmark_baseline.txt benchmark_results.txt",
		"node --test scripts/check-benchmark-regressions.test.mjs",
		"scripts/check-benchmark-regressions.mjs benchmark_stats.csv",
	} {
		if !strings.Contains(text, requirement) {
			t.Errorf("performance workflow missing %q", requirement)
		}
	}
	for _, forbidden := range []string{".benchmark-baseline.txt", "cp benchmark_results.txt .benchmark-baseline.txt", "if [ -f .benchmark-baseline.txt ]"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("performance workflow still contains self-bootstrapping baseline logic %q", forbidden)
		}
	}
	if strings.Contains(text, "-benchmem -count=10 ./services/... 2>&1") {
		t.Error("performance workflow must keep Go diagnostics out of benchstat input")
	}
}

func TestReleasePublishingIsSingleGatedJob(t *testing.T) {
	release, raw := readReleaseWorkflow(t, "../../.github/workflows/release.yml")
	installers, installersRaw := readReleaseWorkflow(t, "../../.github/workflows/release-installers.yml")

	if _, ok := installers.On["workflow_call"]; !ok {
		t.Error("release-installers workflow must be reusable through workflow_call")
	}
	if _, ok := installers.On["release"]; ok {
		t.Error("release-installers workflow must not listen for release events")
	}
	if strings.Contains(strings.ToLower(string(installersRaw)), "--clobber") {
		t.Error("release-installers workflow must not overwrite existing Release assets with --clobber")
	}
	if !strings.Contains(string(installersRaw), `^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`) ||
		!strings.Contains(string(installersRaw), "only stable vX.Y.Z tags") {
		t.Error("reusable installer workflow must reject prerelease and build-metadata tags")
	}
	for _, requirement := range []string{
		"refs/release-tags/", "git fetch --force --no-tags origin",
		`git cat-file -t "$tag_ref"`, `git rev-parse "$tag_ref"`,
		"tag_target_type", "EXPECTED_COMMIT", "caller-verified commit", "git merge-base --is-ancestor",
		`git checkout --detach "$tag_target_sha"`, `commit_sha=${tag_target_sha}`,
	} {
		if !strings.Contains(string(installersRaw), requirement) {
			t.Errorf("reusable installer tag verification is missing %q", requirement)
		}
	}

	installerCall, ok := release.Jobs["installers"]
	if !ok {
		t.Fatal("release workflow has no installers job")
	}
	if got, want := installerCall.Uses, "./.github/workflows/release-installers.yml"; got != want {
		t.Fatalf("installers job uses %q, want reusable workflow %q", got, want)
	}
	if !strings.Contains(string(installersRaw), "expected_commit:") ||
		!strings.Contains(string(raw), "expected_commit: ${{ needs.quality-gate.outputs.commit_sha }}") {
		t.Error("installer workflow must receive the caller-verified commit from the release job")
	}
	if len(installerCall.Steps) != 0 {
		t.Error("reusable installers job must not also contain inline steps")
	}
	validate, ok := installers.Jobs["validate"]
	if !ok {
		t.Fatal("reusable installer workflow has no validate job")
	}
	if got, want := validate.Outputs["commit_sha"], "${{ steps.source.outputs.commit_sha }}"; got != want {
		t.Errorf("installer validate commit output = %q, want %q", got, want)
	}
	if strings.Contains(string(installersRaw), "inputs.expected_commit || inputs.tag_name") {
		t.Error("installer workflow retains a mutable tag checkout fallback")
	}
	if got := strings.Count(string(installersRaw), "ref: ${{ needs.validate.outputs.commit_sha }}"); got != 4 {
		t.Errorf("installer downstream immutable checkout count = %d, want 4", got)
	}
	if got := strings.Count(string(installersRaw), "ref: ${{ steps.bootstrap.outputs.sha }}"); got != 1 {
		t.Errorf("installer validator bootstrap checkout count = %d, want one validated immutable step output", got)
	}
	if !strings.Contains(string(installersRaw), `candidate="${EXPECTED_COMMIT:-$GITHUB_SHA}"`) ||
		!strings.Contains(string(installersRaw), "installer bootstrap ref must be a full commit SHA") ||
		!strings.Contains(string(installersRaw), "tr '[:upper:]' '[:lower:]'") {
		t.Error("installer validator must normalize and validate an immutable bootstrap SHA before checkout")
	}
	if strings.Contains(string(raw), "expected_commit: ${{ github.sha }}") ||
		strings.Contains(string(raw), "EXPECTED_COMMIT: ${{ github.sha }}") {
		t.Error("release jobs must not pass the annotated tag event SHA as a commit")
	}

	final, ok := release.Jobs["release"]
	if !ok {
		t.Fatal("release workflow has no final release job")
	}
	if final.Environment != "release" {
		t.Errorf("final release job environment = %q, want release", final.Environment)
	}
	for permission, want := range map[string]string{
		"contents":     "write",
		"id-token":     "write",
		"attestations": "write",
	} {
		if got := final.Permissions[permission]; got != want {
			t.Errorf("final release job permission %s = %q, want %q", permission, got, want)
		}
	}
	for _, dependency := range []string{"build", "sign-portable", "installers"} {
		if !containsString(final.Needs, dependency) {
			t.Errorf("final release job must wait for %s", dependency)
		}
	}
	if !containsString(final.Needs, "quality-gate") {
		t.Error("final release job must retain the same-commit quality-gate dependency")
	}
	finalSteps := map[string]string{}
	revalidateIndex := -1
	for index, step := range final.Steps {
		finalSteps[step.Name] = step.Run
		if step.Name == "Revalidate tag and release absence before publishing" {
			revalidateIndex = index
		}
	}
	revalidate := finalSteps["Revalidate tag and release absence before publishing"]
	for _, requirement := range []string{
		"tag_object_type", "tag_target_type", "EXPECTED_COMMIT",
		"verification.verified", "verification.reason", "release tag changed",
		"releases/tags/${RELEASE_TAG}", `404)`, `200)`, "refusing to merge artifacts from a rerun",
	} {
		if !strings.Contains(revalidate, requirement) {
			t.Errorf("final release job is missing tag revalidation requirement %q", requirement)
		}
	}
	if !strings.Contains(string(raw), "pattern: release-*") ||
		strings.Contains(string(raw), "name: portable-${{ matrix.os }}") {
		t.Error("final release download must select only release-prefixed finished artifacts")
	}
	publisherCount := 0
	publisherIndex := -1
	attestationCount := 0
	attestationIndex := -1
	for workflowName, workflow := range map[string]releaseWorkflow{
		"release.yml":            release,
		"release-installers.yml": installers,
	} {
		for jobName, job := range workflow.Jobs {
			for stepIndex, step := range job.Steps {
				if isReleasePublisher(step) {
					publisherCount++
					if workflowName != "release.yml" || jobName != "release" {
						t.Errorf("%s job %s step %q can publish a Release; only release.yml/release may publish", workflowName, jobName, step.Name)
					}
					if step.Uses != "" || !strings.Contains(step.Run, `gh release create "$RELEASE_TAG" release-assets/*`) ||
						!strings.Contains(step.Run, "--verify-tag") {
						t.Errorf("Release publisher must use the create-only gh API path, got uses=%q run=%q", step.Uses, step.Run)
					}
					publisherIndex = stepIndex
				}
				if strings.HasPrefix(step.Uses, "actions/attest-build-provenance@") {
					attestationCount++
					if workflowName != "release.yml" || jobName != "release" {
						t.Errorf("build-provenance attestation belongs in the final release job, found in %s/%s", workflowName, jobName)
					}
					if !regexp.MustCompile(`^actions/attest-build-provenance@[0-9a-f]{40}$`).MatchString(step.Uses) {
						t.Errorf("attest-build-provenance must be pinned to a full commit SHA, got %q", step.Uses)
					}
					if got := step.With["subject-checksums"]; got != "release-assets/SHA256SUMS" {
						t.Errorf("attestation subject-checksums = %q, want final SHA256SUMS", got)
					}
					attestationIndex = stepIndex
				}
			}
		}
	}
	if publisherCount != 1 {
		t.Errorf("release workflows contain %d Release publishers, want exactly 1", publisherCount)
	}
	if attestationCount != 1 {
		t.Errorf("release workflows contain %d build-provenance attestation steps, want exactly 1", attestationCount)
	}
	if attestationIndex < 0 || publisherIndex < 0 || attestationIndex >= publisherIndex {
		t.Error("final assets must be attested before the single Release publisher runs")
	}
	if revalidateIndex < 0 || publisherIndex < 0 || revalidateIndex+1 != publisherIndex {
		t.Error("signed tag, peeled commit, and release absence must be revalidated immediately before publishing")
	}
}

func TestReleaseSigningSecretsAreStepScoped(t *testing.T) {
	secretExpression := regexp.MustCompile(`\$\{\{\s*secrets(?:\.|\[)`)
	signingSecrets := map[string]bool{
		"WINDOWS_CERT_PFX":            true,
		"WINDOWS_CERT_PASSWORD":       true,
		"MACOS_CERT_P12":              true,
		"MACOS_CERT_PASSWORD":         true,
		"APPLE_ID":                    true,
		"APPLE_APP_SPECIFIC_PASSWORD": true,
		"APPLE_TEAM_ID":               true,
	}
	release, _ := readReleaseWorkflow(t, "../../.github/workflows/release.yml")
	installerCall, ok := release.Jobs["installers"]
	if !ok {
		t.Fatal("release workflow has no reusable installers job")
	}
	if len(installerCall.Secrets) != 0 {
		t.Errorf("release.yml/installers must not forward repository-level secrets; signing jobs resolve secrets through the release Environment, got %v", installerCall.Secrets)
	}
	installers, installersRaw := readReleaseWorkflow(t, "../../.github/workflows/release-installers.yml")
	for _, jobName := range []string{"windows-sign-app", "windows-sign", "macos-sign"} {
		job, ok := installers.Jobs[jobName]
		if !ok {
			t.Errorf("release-installers workflow has no %s signing job", jobName)
			continue
		}
		if job.Environment != "release" {
			t.Errorf("release-installers.yml/%s environment = %q, want release", jobName, job.Environment)
		}
	}

	for workflowName, path := range map[string]string{
		"release.yml":            "../../.github/workflows/release.yml",
		"release-installers.yml": "../../.github/workflows/release-installers.yml",
	} {
		workflow, _ := readReleaseWorkflow(t, path)
		assertNoSecretExpressions(t, workflowName+" workflow env", workflow.Env, secretExpression)
		for jobName, job := range workflow.Jobs {
			assertNoSecretExpressions(t, workflowName+"/"+jobName+" job env", job.Env, secretExpression)
			usesSigningSecret := false
			for _, step := range job.Steps {
				if secretExpression.MatchString(step.Run) {
					t.Errorf("%s/%s step %q references a secret directly in run; pass it through signing-step env", workflowName, jobName, step.Name)
				}
				assertNoSecretExpressions(t, workflowName+"/"+jobName+" step "+step.Name+" with", step.With, secretExpression)
				for key, value := range step.Env {
					if !secretExpression.MatchString(value) {
						continue
					}
					if !strings.Contains(strings.ToLower(step.Name), "sign") {
						t.Errorf("%s/%s step %q exposes signing secret %s outside a signing step", workflowName, jobName, step.Name, key)
					}
					if !signingSecrets[key] {
						t.Errorf("%s/%s signing step %q exposes unexpected secret env %s", workflowName, jobName, step.Name, key)
					} else {
						usesSigningSecret = true
					}
					if want := "${{ secrets." + key + " }}"; value != want {
						t.Errorf("%s/%s signing step %q env %s = %q, want direct same-name secret reference %q", workflowName, jobName, step.Name, key, value, want)
					}
				}
			}
			if usesSigningSecret && job.Environment != "release" {
				t.Errorf("%s/%s uses signing secrets but environment = %q, want release", workflowName, jobName, job.Environment)
			}
		}
	}
	for _, jobName := range []string{"windows-sign-app", "windows-sign", "macos-sign"} {
		job := installers.Jobs[jobName]
		for _, step := range job.Steps {
			hasSigningSecret := false
			for _, value := range step.Env {
				if secretExpression.MatchString(value) {
					hasSigningSecret = true
					break
				}
			}
			if !hasSigningSecret {
				continue
			}
			if _, ok := step.Env["REQUIRE_CODE_SIGN_ENV"]; !ok {
				t.Errorf("release-installers/%s step %q must read REQUIRE_CODE_SIGN from its release Environment", jobName, step.Name)
			}
			if _, ok := step.Env["REQUIRE_CODE_SIGN_INPUT"]; !ok {
				t.Errorf("release-installers/%s step %q must retain the explicit workflow input", jobName, step.Name)
			}
		}
	}
	releaseRaw, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(releaseRaw), "vars.REQUIRE_CODE_SIGN") ||
		!strings.Contains(string(releaseRaw), "require_code_sign: true") ||
		!strings.Contains(string(releaseRaw), `REQUIRE_CODE_SIGN: "true"`) {
		t.Error("tag-triggered release must require signing literally and must not consult a repository variable")
	}
	for _, requirement := range []string{
		`REQUIRE_CODE_SIGN_ENV: ${{ vars.REQUIRE_CODE_SIGN }}`,
		`$requireCodeSign = $inputPolicy -ceq "true" -or $environmentPolicy -ceq "true"`,
		`$environmentPolicy -cnotin @("true", "false")`,
		`[ "$REQUIRE_CODE_SIGN_INPUT" != "true" ] && [ "${REQUIRE_CODE_SIGN_ENV:-}" != "true" ]`,
		"REQUIRE_CODE_SIGN must be exactly true or false when set",
	} {
		if !strings.Contains(string(installersRaw), requirement) {
			t.Errorf("installer signing policy is missing %q", requirement)
		}
	}
}

func TestMacReleaseSigningUsesOnlyTheTemporaryKeychainIdentity(t *testing.T) {
	tests := []struct {
		path     string
		job      string
		stepName string
	}{
		{"../../.github/workflows/release.yml", "sign-portable", "Code sign and notarize portable macOS app"},
		{"../../.github/workflows/release-installers.yml", "macos-sign", "Sign macOS app payload"},
	}
	for _, test := range tests {
		workflow, _ := readReleaseWorkflow(t, test.path)
		job, ok := workflow.Jobs[test.job]
		if !ok {
			t.Errorf("%s has no %s job", test.path, test.job)
			continue
		}
		var run string
		for _, step := range job.Steps {
			if step.Name == test.stepName {
				run = step.Run
				break
			}
		}
		if run == "" {
			t.Errorf("%s/%s has no %q step", test.path, test.job, test.stepName)
			continue
		}
		for _, requirement := range []string{"base64 -D", "security find-identity", `--keychain "$keychain"`, `--sign "${signing_identities[0]}"`} {
			if !strings.Contains(run, requirement) {
				t.Errorf("%s/%s step %q does not contain %q", test.path, test.job, test.stepName, requirement)
			}
		}
		if strings.Contains(run, `--sign "Developer ID Application"`) {
			t.Errorf("%s/%s selects a macOS identity by a generic label instead of the temporary-keychain fingerprint", test.path, test.job)
		}
	}
}

func TestReleaseSigningJobsUseFreshArtifactOnlyRunners(t *testing.T) {
	tests := []struct {
		path string
		jobs []string
	}{
		{"../../.github/workflows/release.yml", []string{"sign-portable"}},
		{"../../.github/workflows/release-installers.yml", []string{"windows-sign-app", "windows-sign", "macos-sign"}},
	}
	for _, test := range tests {
		workflow, _ := readReleaseWorkflow(t, test.path)
		for _, jobName := range test.jobs {
			job, ok := workflow.Jobs[jobName]
			if !ok {
				t.Errorf("%s has no dedicated signer job %s", test.path, jobName)
				continue
			}
			if job.Environment != "release" {
				t.Errorf("%s/%s environment = %q, want release", test.path, jobName, job.Environment)
			}
			if len(job.Steps) == 0 || !strings.HasPrefix(job.Steps[0].Uses, "actions/download-artifact@") {
				t.Errorf("%s/%s must start from a downloaded unsigned artifact", test.path, jobName)
			}
			for _, step := range job.Steps {
				if strings.HasPrefix(step.Uses, "actions/checkout@") {
					t.Errorf("%s/%s checks out repository code on a signing runner", test.path, jobName)
				}
				for _, forbidden := range []string{"build/scripts/", "npm ci", "go build", "wails3 build"} {
					if strings.Contains(step.Run, forbidden) {
						t.Errorf("%s/%s executes repository build command %q on a signing runner", test.path, jobName, forbidden)
					}
				}
			}
		}
	}

	release, _ := readReleaseWorkflow(t, "../../.github/workflows/release.yml")
	if got := release.Jobs["build"].Environment; got != "" {
		t.Errorf("portable build environment = %q, want no protected environment", got)
	}
	installers, _ := readReleaseWorkflow(t, "../../.github/workflows/release-installers.yml")
	for _, jobName := range []string{"windows-build", "windows-msi", "linux", "macos-build"} {
		if got := installers.Jobs[jobName].Environment; got != "" {
			t.Errorf("installer builder %s environment = %q, want no protected environment", jobName, got)
		}
	}
}

func readReleaseWorkflow(t *testing.T, path string) (releaseWorkflow, []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return workflow, raw
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func isReleasePublisher(step releaseWorkflowStep) bool {
	uses := strings.ToLower(step.Uses)
	for _, action := range []string{
		"softprops/action-gh-release@",
		"ncipollo/release-action@",
		"actions/create-release@",
		"svenstaro/upload-release-action@",
		"marvinpinto/action-automatic-releases@",
	} {
		if strings.HasPrefix(uses, action) {
			return true
		}
	}
	return regexp.MustCompile(`(?m)(^|[;&|[:space:]])gh[[:space:]]+release[[:space:]]+(create|upload|edit|delete)([[:space:]]|$)`).MatchString(strings.ToLower(step.Run))
}

func assertNoSecretExpressions(t *testing.T, scope string, values map[string]string, expression *regexp.Regexp) {
	t.Helper()
	for key, value := range values {
		if expression.MatchString(value) {
			t.Errorf("%s must not expose secret expression through %s", scope, key)
		}
	}
}

func TestReleasingDocsMatchWorkflowArtifacts(t *testing.T) {
	raw, err := os.ReadFile("../../docs/RELEASING.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, required := range []string{
		"windows-amd64.zip", "linux-amd64.tar.gz", "darwin-amd64.zip", "darwin-arm64.zip",
		"macos-15-intel", "macos-15", "single `x86_64` slice asserted",
		"build/windows/info.json", "build/linux/nfpm/nfpm.yaml", "build/darwin/Info.plist",
		"tag-triggered workflow has not been run locally", "*.sbom.spdx.json", "provenance.intoto.jsonl",
		"zero unresolved licenses across the union of the", "LICENSE", "executable/installer artifacts",
		"artifact service does not preserve Unix", "portable `.tar.gz`",
		"independently peeled release commit", "annotated tag object's",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("docs/RELEASING.md is missing %q", required)
		}
	}
}

func TestReleaseWorkflowRequiresSBOMProvenanceAndFinalChecksums(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}
	release, ok := workflow.Jobs["release"]
	if !ok {
		t.Fatal("release workflow has no release job")
	}
	steps := map[string]struct {
		run             string
		env             map[string]string
		continueOnError bool
	}{}
	for _, step := range release.Steps {
		steps[step.Name] = struct {
			run             string
			env             map[string]string
			continueOnError bool
		}{step.Run, step.Env, step.ContinueOnError}
	}
	license := steps["Verify notices and license inventory"]
	if !strings.Contains(license.run, "generate-license-inventory.mjs --full-check") || !strings.Contains(license.run, "test -s LICENSE") || !strings.Contains(license.run, "test -s NOTICE") {
		t.Error("release must fail closed on missing or stale license notices")
	}
	sbom := steps["Generate mandatory SBOM"]
	if sbom.continueOnError || sbom.env["SYFT_MODE"] != "docker" || !strings.Contains(sbom.run, "generate-sbom.sh") || !strings.Contains(sbom.run, "test -s") || !strings.Contains(sbom.run, "JSON.parse") {
		t.Error("release SBOM step must be mandatory, non-empty, and JSON-validated")
	}
	provenance := steps["Generate unsigned provenance statement"]
	if provenance.continueOnError || provenance.env["RELEASE_COMMIT_SHA"] != "${{ needs.quality-gate.outputs.commit_sha }}" ||
		!strings.Contains(provenance.run, "generate-release-provenance.mjs") || !strings.Contains(provenance.run, "test -s") {
		t.Error("release must generate a non-empty provenance statement")
	}
	checksums := steps["Finalize release checksums"]
	if checksums.continueOnError || !strings.Contains(checksums.run, "sha256sum") || !strings.Contains(checksums.run, "SHA256SUMS) continue") {
		t.Error("release must regenerate final checksums after all assets exist")
	}
	workflowText := string(raw)
	for _, requirement := range []string{
		"expected_artifact_names=(",
		"koyori-ide-v${version}-windows-amd64.zip",
		"koyori-ide-v${version}-darwin-arm64.zip",
		"koyori-ide-${version}-linux-arm64.rpm",
		"koyori-ide-${version}-macos-arm64.dmg",
		"Release checksum names do not match the artifact allowlist",
		"checksum_line_count=\"$(awk 'END { print NR }' \"$checksum\")\"",
		"Checksum must contain exactly one SHA-256 line bound to $artifact_name",
		"if [ -e release-assets ] || [ -L release-assets ]",
		"refusing to publish pre-existing files",
		"Initial release staging does not match the exact allowlist",
		"release-asset-allowlist.txt",
		"Final release staging does not match the exact allowlist",
		`[ ! -f "$candidate" ] || [ -L "$candidate" ]`,
		`[ "$checksum_line" != "$digest  $artifact_name" ]`,
	} {
		if !strings.Contains(workflowText, requirement) {
			t.Errorf("release workflow is missing exact artifact allowlist contract %q", requirement)
		}
	}
	if got := strings.Count(workflowText, `printf '%s  %s\n' "$digest" "$artifact_name" > "$artifact_name.sha256"`); got != 2 {
		t.Errorf("release workflow has %d canonical portable checksum writers, want 2", got)
	}
	if !strings.Contains(workflowText, "cp LICENSE NOTICE docs/THIRD_PARTY_LICENSES.md docs/RELEASE_ASSET_LICENSES.md release-assets/") {
		t.Error("release workflow must attach the repository LICENSE with the notices and inventories")
	}
	for _, forbidden := range []string{"Generate SBOM (optional)", "skipping SBOM", "continue-on-error: true\n        shell: bash\n        run: |\n          if command -v syft"} {
		if strings.Contains(workflowText, forbidden) {
			t.Errorf("release workflow retains optional SBOM behavior %q", forbidden)
		}
	}
}

func TestReleaseWorkflowBashStepsHaveValidSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed on this platform")
	}
	for workflowName, path := range map[string]string{
		"release.yml":            "../../.github/workflows/release.yml",
		"release-installers.yml": "../../.github/workflows/release-installers.yml",
	} {
		workflow, _ := readReleaseWorkflow(t, path)
		for jobName, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if step.Shell != "bash" || strings.TrimSpace(step.Run) == "" {
					continue
				}
				cmd := exec.Command(bash, "-n")
				cmd.Stdin = strings.NewReader(step.Run)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("%s/%s/%s has invalid bash syntax: %v\n%s", workflowName, jobName, step.Name, err, output)
				}
			}
		}
	}
}

func TestReleaseWorkflowPowerShellStepsHaveValidSyntax(t *testing.T) {
	powershell := ""
	for _, candidate := range []string{"pwsh", "powershell"} {
		if path, err := exec.LookPath(candidate); err == nil {
			powershell = path
			break
		}
	}
	if powershell == "" {
		t.Skip("PowerShell is not installed on this platform")
	}
	for workflowName, path := range map[string]string{
		"release.yml":            "../../.github/workflows/release.yml",
		"release-installers.yml": "../../.github/workflows/release-installers.yml",
	} {
		workflow, _ := readReleaseWorkflow(t, path)
		for jobName, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if step.Shell != "pwsh" || strings.TrimSpace(step.Run) == "" {
					continue
				}
				cmd := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command",
					`$source = [Console]::In.ReadToEnd(); [void][ScriptBlock]::Create($source)`)
				cmd.Stdin = strings.NewReader(step.Run)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("%s/%s/%s has invalid PowerShell syntax: %v\n%s", workflowName, jobName, step.Name, err, output)
				}
			}
		}
	}
}
