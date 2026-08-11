package repo

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type releaseWorkflow struct {
	Jobs map[string]struct {
		Strategy struct {
			Matrix struct {
				Include []map[string]string `yaml:"include"`
			} `yaml:"matrix"`
		} `yaml:"strategy"`
		Env   map[string]string `yaml:"env"`
		Steps []struct {
			Name            string `yaml:"name"`
			Shell           string `yaml:"shell"`
			Run             string `yaml:"run"`
			ContinueOnError bool   `yaml:"continue-on-error"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
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

	wantMatrix := map[string]string{
		"windows-latest/amd64": ".zip",
		"ubuntu-latest/amd64":  ".tar.gz",
		"macos-latest/amd64":   ".zip",
		"macos-latest/arm64":   ".zip",
	}
	for _, entry := range build.Strategy.Matrix.Include {
		key := entry["os"] + "/" + entry["arch"]
		wantSuffix, expected := wantMatrix[key]
		if !expected {
			t.Errorf("unexpected release matrix entry %q", key)
			continue
		}
		if !strings.HasSuffix(entry["artifact_name"], wantSuffix) {
			t.Errorf("%s artifact %q does not end in %s", key, entry["artifact_name"], wantSuffix)
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
	for _, source := range []string{
		"VERSION", "build/config.yml", "build/windows/info.json",
		"build/linux/nfpm/nfpm.yaml", "build/darwin/Info.plist",
	} {
		if !strings.Contains(versionCheck, source) {
			t.Errorf("release metadata verification does not cover %s", source)
		}
	}
	if mac := steps["Package (macOS)"]; !strings.Contains(mac, "ditto -c -k --keepParent") || !strings.Contains(mac, "#app_bundles[@]") {
		t.Error("macOS packaging must create a real zip and reject ambiguous app bundles")
	}
	if linux := steps["Package (Linux)"]; !strings.Contains(linux, "tar -czf") || !strings.Contains(linux, "#artifacts[@]") {
		t.Error("Linux packaging must create tar.gz and assert exactly one binary")
	}
	if windows := steps["Package (Windows)"]; !strings.Contains(windows, "Count -ne 1") {
		t.Error("Windows packaging must reject missing or ambiguous executables")
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
		"build/windows/info.json", "build/linux/nfpm/nfpm.yaml", "build/darwin/Info.plist",
		"tag-triggered workflow has not been run locally", "*.sbom.spdx.json", "provenance.intoto.jsonl",
		"zero unresolved licenses across the union of the",
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
		continueOnError bool
	}{}
	for _, step := range release.Steps {
		steps[step.Name] = struct {
			run             string
			continueOnError bool
		}{step.Run, step.ContinueOnError}
	}
	license := steps["Verify notices and license inventory"]
	if !strings.Contains(license.run, "generate-license-inventory.mjs --full-check") || !strings.Contains(license.run, "test -s NOTICE") {
		t.Error("release must fail closed on missing or stale license notices")
	}
	sbom := steps["Generate mandatory SBOM"]
	if sbom.continueOnError || !strings.Contains(sbom.run, "generate-sbom.sh") || !strings.Contains(sbom.run, "test -s") || !strings.Contains(sbom.run, "JSON.parse") {
		t.Error("release SBOM step must be mandatory, non-empty, and JSON-validated")
	}
	provenance := steps["Generate unsigned provenance statement"]
	if provenance.continueOnError || !strings.Contains(provenance.run, "generate-release-provenance.mjs") || !strings.Contains(provenance.run, "test -s") {
		t.Error("release must generate a non-empty provenance statement")
	}
	checksums := steps["Finalize release checksums"]
	if checksums.continueOnError || !strings.Contains(checksums.run, "sha256sum") || !strings.Contains(checksums.run, "SHA256SUMS) continue") {
		t.Error("release must regenerate final checksums after all assets exist")
	}
	workflowText := string(raw)
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
	raw, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Shell != "bash" || strings.TrimSpace(step.Run) == "" {
				continue
			}
			cmd := exec.Command(bash, "-n")
			cmd.Stdin = strings.NewReader(step.Run)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s/%s has invalid bash syntax: %v\n%s", jobName, step.Name, err, output)
			}
		}
	}
}
