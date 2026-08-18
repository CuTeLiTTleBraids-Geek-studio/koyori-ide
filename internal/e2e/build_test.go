package e2e

import (
	"go/build"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func goFilesForTags(t *testing.T, tags string) []string {
	t.Helper()
	context := build.Default
	if tags != "" {
		context.BuildTags = []string{tags}
	}
	metadata, err := context.ImportDir(".", build.IgnoreVendor)
	if err != nil {
		t.Fatalf("inspect package for tags %q: %v", tags, err)
	}
	return append(metadata.GoFiles, metadata.CgoFiles...)
}

func TestBuildTagsExcludeEndpointFromProduction(t *testing.T) {
	production := goFilesForTags(t, "")
	if slices.Contains(production, "server.go") {
		t.Fatal("production package unexpectedly includes the E2E automation endpoint")
	}
	if !slices.Contains(production, "stub.go") {
		t.Fatal("production package does not include the disabled E2E stub")
	}

	e2e := goFilesForTags(t, "e2e")
	if !slices.Contains(e2e, "server.go") {
		t.Fatal("e2e-tagged package does not include the automation endpoint")
	}
	if slices.Contains(e2e, "stub.go") {
		t.Fatal("e2e-tagged package unexpectedly includes the disabled stub")
	}
}

func TestBuildTagsExcludeLanguagePackFixtureSignerFromProduction(t *testing.T) {
	context := build.Default
	production, err := context.ImportDir("../../services", build.IgnoreVendor)
	if err != nil {
		t.Fatalf("inspect production services package: %v", err)
	}
	if slices.Contains(production.GoFiles, "language_pack_e2e.go") {
		t.Fatal("production services package includes the E2E language pack signer")
	}
	context.BuildTags = []string{"e2e"}
	tagged, err := context.ImportDir("../../services", build.IgnoreVendor)
	if err != nil {
		t.Fatalf("inspect e2e services package: %v", err)
	}
	if !slices.Contains(tagged.GoFiles, "language_pack_e2e.go") {
		t.Fatal("e2e services package does not include the language pack signer")
	}
}

func TestPackagedE2EWorkflowStaysManualUntilThreeRealRuns(t *testing.T) {
	type workflowStep struct {
		Name string            `yaml:"name"`
		If   string            `yaml:"if"`
		Run  string            `yaml:"run"`
		Uses string            `yaml:"uses"`
		With map[string]string `yaml:"with"`
	}
	type workflowJob struct {
		Name  string            `yaml:"name"`
		If    string            `yaml:"if"`
		Needs []string          `yaml:"needs"`
		Env   map[string]string `yaml:"env"`
		Steps []workflowStep    `yaml:"steps"`
	}
	var workflow struct {
		Jobs map[string]workflowJob `yaml:"jobs"`
	}
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}
	job, ok := workflow.Jobs["packaged-e2e"]
	if !ok {
		t.Fatal("packaged-e2e job is missing")
	}
	if job.If != "github.event_name == 'workflow_dispatch'" {
		t.Fatalf("qualification job gate = %q", job.If)
	}
	for _, dependency := range []string{
		"contract-smoke", "wails-bindings", "go-test", "frontend-test", "wails-build", "npm-audit",
	} {
		if !slices.Contains(job.Needs, dependency) {
			t.Errorf("packaged-e2e does not need %s", dependency)
		}
	}
	if job.Env["KOYORI_IDE_E2E_DISPLAY"] != ":99" {
		t.Errorf("KOYORI_IDE_E2E_DISPLAY = %q", job.Env["KOYORI_IDE_E2E_DISPLAY"])
	}

	var allRuns strings.Builder
	var upload workflowStep
	for _, step := range job.Steps {
		allRuns.WriteString(step.Run)
		allRuns.WriteByte('\n')
		if step.Name == "Upload E2E evidence" {
			upload = step
		}
	}
	runs := allRuns.String()
	for _, required := range []string{
		"xvfb", "imagemagick", "gopls@v0.21.1", "node scripts/packaged-e2e.mjs",
	} {
		if !strings.Contains(runs, required) {
			t.Errorf("packaged-e2e steps do not contain %q", required)
		}
	}
	if upload.If != "always()" || upload.With["path"] != "build/e2e-evidence/" {
		t.Errorf("evidence upload gate/path = %q/%q", upload.If, upload.With["path"])
	}
	if !strings.Contains(string(data), "three consecutive manual runs on three distinct commits") {
		t.Fatal("CI qualification comment does not preserve the three-run threshold")
	}

	sourceJob := workflow.Jobs["contract-smoke"]
	var sourceRuns strings.Builder
	for _, step := range sourceJob.Steps {
		sourceRuns.WriteString(step.Run)
		sourceRuns.WriteByte('\n')
	}
	for _, required := range []string{
		"node --test scripts/packaged-e2e-driver.test.mjs",
		"node scripts/packaged-e2e.mjs --dry-run",
	} {
		if !strings.Contains(sourceRuns.String(), required) {
			t.Errorf("required source contract job does not contain %q", required)
		}
	}

	goTestJob := workflow.Jobs["go-test"]
	var goTestRuns strings.Builder
	for _, step := range goTestJob.Steps {
		goTestRuns.WriteString(step.Run)
		goTestRuns.WriteByte('\n')
	}
	if !strings.Contains(goTestRuns.String(), "go test -race -tags e2e ./internal/e2e/... -count=1") {
		t.Error("go-test job does not compile and test the e2e-tagged hook")
	}
}

func TestGoTestWorkflowHasExplicitCrossPlatformRaceMatrix(t *testing.T) {
	type workflowStep struct {
		Run string `yaml:"run"`
	}
	type workflowJob struct {
		Strategy struct {
			Matrix struct {
				OS []string `yaml:"os"`
			} `yaml:"matrix"`
		} `yaml:"strategy"`
		RunsOn string         `yaml:"runs-on"`
		Steps  []workflowStep `yaml:"steps"`
	}
	var workflow struct {
		Jobs map[string]workflowJob `yaml:"jobs"`
	}
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}

	job, ok := workflow.Jobs["go-test"]
	if !ok {
		t.Fatal("go-test job is missing")
	}
	wantOS := []string{"ubuntu-latest", "windows-latest", "macos-latest"}
	if !slices.Equal(job.Strategy.Matrix.OS, wantOS) {
		t.Fatalf("go-test OS matrix = %v, want %v", job.Strategy.Matrix.OS, wantOS)
	}
	if job.RunsOn != "${{ matrix.os }}" {
		t.Fatalf("go-test runs-on = %q, want matrix.os", job.RunsOn)
	}

	var runs []string
	for _, step := range job.Steps {
		if step.Run != "" {
			runs = append(runs, step.Run)
		}
	}
	joined := strings.Join(runs, "\n")
	for _, required := range []string{
		"go vet ./services/... . ./internal/repo/...",
		"go test -race ./services/... . ./internal/repo/... ./build/docker/server-gateway/... -count=1",
		"go test -race -tags e2e ./internal/e2e/... -count=1",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("go-test job does not contain exact command %q", required)
		}
	}
}
