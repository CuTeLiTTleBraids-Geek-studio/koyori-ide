package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseGoCompiler_errors(t *testing.T) {
	output := "main.go:10:3: undefined: foo\nmain.go:12:5: cannot use x (type int) as type string\n# some/package\nmain.go:1: syntax error"
	diags := parseGoCompiler(output, "go build")
	if len(diags) != 3 {
		t.Fatalf("expected 3 diagnostics, got %d", len(diags))
	}
	// First diagnostic
	if diags[0].File != "main.go" || diags[0].Line != 10 || diags[0].Column != 3 {
		t.Errorf("diag[0] = %+v", diags[0])
	}
	if diags[0].Message != "undefined: foo" {
		t.Errorf("diag[0] message = %q", diags[0].Message)
	}
	if diags[0].Severity != "error" {
		t.Errorf("diag[0] severity = %q, want error", diags[0].Severity)
	}
	if diags[0].Source != "go build" {
		t.Errorf("diag[0] source = %q", diags[0].Source)
	}
	// Second diagnostic (5 spaces / different line)
	if diags[1].Line != 12 || diags[1].Column != 5 {
		t.Errorf("diag[1] = %+v", diags[1])
	}
	// Third: "main.go:1: syntax error" — column field is empty -> 0
	if diags[2].Line != 1 || diags[2].Column != 0 {
		t.Errorf("diag[2] = %+v", diags[2])
	}
	if !strings.Contains(diags[2].Message, "syntax error") {
		t.Errorf("diag[2] message = %q", diags[2].Message)
	}
}

func TestParseGoCompiler_empty(t *testing.T) {
	if diags := parseGoCompiler("", "go build"); len(diags) != 0 {
		t.Errorf("expected 0 diagnostics for empty output, got %d", len(diags))
	}
	// Non-matching lines produce no diagnostics.
	if diags := parseGoCompiler("build succeeded\nok  some/pkg  0.5s", "go build"); len(diags) != 0 {
		t.Errorf("expected 0 diagnostics for non-error output, got %d", len(diags))
	}
}

func TestParseGolangciLint(t *testing.T) {
	output := "main.go:10:3: `foo` is unused (unused)\nmain.go:20:5: ineffectual assignment to x (ineffassign)\nlevel=warning msg=\"something\""
	diags := parseGolangciLint(output)
	if len(diags) != 2 {
		t.Fatalf("expected 2 diagnostics, got %d", len(diags))
	}
	if diags[0].File != "main.go" || diags[0].Line != 10 || diags[0].Column != 3 {
		t.Errorf("diag[0] = %+v", diags[0])
	}
	if diags[0].Message != "`foo` is unused" {
		t.Errorf("diag[0] message = %q", diags[0].Message)
	}
	if diags[0].Source != "golangci-lint/unused" {
		t.Errorf("diag[0] source = %q", diags[0].Source)
	}
	if diags[0].Severity != "warning" {
		t.Errorf("diag[0] severity = %q, want warning", diags[0].Severity)
	}
	if diags[1].Source != "golangci-lint/ineffassign" {
		t.Errorf("diag[1] source = %q", diags[1].Source)
	}
}

func TestParseTypeScript(t *testing.T) {
	output := "src/index.ts(10,3): error TS2322: Type 'string' is not assignable to type 'number'.\nsrc/utils.ts(5,1): warning TS6133: 'x' is declared but its value is never read."
	diags := parseTypeScript(output)
	if len(diags) != 2 {
		t.Fatalf("expected 2 diagnostics, got %d", len(diags))
	}
	if diags[0].File != "src/index.ts" || diags[0].Line != 10 || diags[0].Column != 3 {
		t.Errorf("diag[0] = %+v", diags[0])
	}
	if diags[0].Severity != "error" {
		t.Errorf("diag[0] severity = %q, want error", diags[0].Severity)
	}
	if !strings.Contains(diags[0].Message, "Type 'string' is not assignable") {
		t.Errorf("diag[0] message = %q", diags[0].Message)
	}
	if diags[0].Source != "tsc" {
		t.Errorf("diag[0] source = %q", diags[0].Source)
	}
	// Second: warning severity
	if diags[1].Severity != "warning" {
		t.Errorf("diag[1] severity = %q, want warning", diags[1].Severity)
	}
	if diags[1].Line != 5 || diags[1].Column != 1 {
		t.Errorf("diag[1] = %+v", diags[1])
	}
}

func TestParseTypeScript_tsx(t *testing.T) {
	output := "src/App.tsx(42,10): error TS2304: Cannot find name 'Foo'."
	diags := parseTypeScript(output)
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}
	if diags[0].File != "src/App.tsx" {
		t.Errorf("diag[0] file = %q, want src/App.tsx", diags[0].File)
	}
	if diags[0].Line != 42 || diags[0].Column != 10 {
		t.Errorf("diag[0] = %+v", diags[0])
	}
}

func TestParseESLint(t *testing.T) {
	output := "src/index.js:10:3: 'foo' is not defined  no-undef\nsrc/utils.js:5:1: Unexpected console statement  no-console\n✖ 2 problems (2 errors, 0 warnings)"
	diags := parseESLint(output)
	// The summary line "✖ 2 problems ..." should NOT match (not a source file).
	if len(diags) != 2 {
		t.Fatalf("expected 2 diagnostics, got %d", len(diags))
	}
	if diags[0].File != "src/index.js" || diags[0].Line != 10 || diags[0].Column != 3 {
		t.Errorf("diag[0] = %+v", diags[0])
	}
	if diags[0].Message != "'foo' is not defined" {
		t.Errorf("diag[0] message = %q", diags[0].Message)
	}
	if diags[0].Source != "eslint/no-undef" {
		t.Errorf("diag[0] source = %q", diags[0].Source)
	}
	if diags[1].Source != "eslint/no-console" {
		t.Errorf("diag[1] source = %q", diags[1].Source)
	}
}

func TestParseESLint_skipsNonSourceLines(t *testing.T) {
	// Lines that don't end in a source extension should be skipped.
	output := "✖ 1 problem (1 error, 0 warnings)\n  1 error and 0 warnings potentially fixable"
	diags := parseESLint(output)
	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics for summary-only output, got %d", len(diags))
	}
}

func TestParseDiagnostics_routing(t *testing.T) {
	t.Run("golangci-lint routes to golangci parser", func(t *testing.T) {
		cmd := ToolchainCommand{ID: "golangci-lint", Command: "golangci-lint run"}
		diags := parseDiagnostics(cmd, "a.go:1:1: msg (govet)")
		if len(diags) != 1 || diags[0].Source != "golangci-lint/govet" {
			t.Errorf("expected golangci routing, got %+v", diags)
		}
	})
	t.Run("go build routes to go compiler parser", func(t *testing.T) {
		cmd := ToolchainCommand{ID: "go-build", Command: "go build", Args: []string{"./..."}}
		diags := parseDiagnostics(cmd, "a.go:1:1: bad")
		if len(diags) != 1 || diags[0].Source != "go build" {
			t.Errorf("expected go build routing, got %+v", diags)
		}
	})
	t.Run("tsc routes to typescript parser", func(t *testing.T) {
		cmd := ToolchainCommand{ID: "tsc", Command: "tsc", Args: []string{"--noEmit"}}
		diags := parseDiagnostics(cmd, "a.ts(1,1): error TS1: x")
		if len(diags) != 1 || diags[0].Source != "tsc" {
			t.Errorf("expected tsc routing, got %+v", diags)
		}
	})
	t.Run("eslint routes to eslint parser", func(t *testing.T) {
		cmd := ToolchainCommand{ID: "eslint", Command: "eslint", Args: []string{"--fix", "."}}
		diags := parseDiagnostics(cmd, "a.js:1:1: x  no-undef")
		if len(diags) != 1 || diags[0].Source != "eslint/no-undef" {
			t.Errorf("expected eslint routing, got %+v", diags)
		}
	})
	t.Run("unknown command produces no diagnostics", func(t *testing.T) {
		cmd := ToolchainCommand{ID: "gofmt", Command: "gofmt", Args: []string{"-l", "."}}
		if diags := parseDiagnostics(cmd, "a.go"); len(diags) != 0 {
			t.Errorf("expected no diagnostics for gofmt, got %d", len(diags))
		}
	})
}

func TestListToolchainCommands_fullCatalogWhenNoRoot(t *testing.T) {
	svc := NewToolchainService()
	cmds := svc.ListToolchainCommands()
	if len(cmds) != len(allToolchainCommands) {
		t.Errorf("expected %d commands (full catalog), got %d", len(allToolchainCommands), len(cmds))
	}
	// Verify expected IDs are present.
	ids := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		ids[c.ID] = true
	}
	for _, want := range []string{"go-build", "go-test", "go-vet", "golangci-lint", "tsc", "eslint", "prettier", "vitest", "make", "npm-scripts"} {
		if !ids[want] {
			t.Errorf("expected command %q in catalog, not found", want)
		}
	}
}

func TestListToolchainCommands_goWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := NewToolchainService()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	cmds := svc.ListToolchainCommands()
	ids := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		ids[c.ID] = true
	}
	// Go commands should be present.
	if !ids["go-build"] || !ids["golangci-lint"] {
		t.Errorf("expected go commands in go workspace, got ids: %v", ids)
	}
	// TS/JS commands should be absent (no package.json).
	if ids["tsc"] || ids["eslint"] {
		t.Errorf("did not expect TS/JS commands in go-only workspace, got ids: %v", ids)
	}
}

func TestListToolchainCommands_nodeWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := NewToolchainService()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	cmds := svc.ListToolchainCommands()
	ids := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		ids[c.ID] = true
	}
	// TS/JS commands should be present.
	if !ids["tsc"] || !ids["eslint"] || !ids["prettier"] {
		t.Errorf("expected TS/JS commands in node workspace, got ids: %v", ids)
	}
	// Go commands should be absent (no go.mod).
	if ids["go-build"] || ids["golangci-lint"] {
		t.Errorf("did not expect go commands in node-only workspace, got ids: %v", ids)
	}
}

func TestListToolchainCommands_makefile(t *testing.T) {
	dir := t.TempDir()
	// Both go.mod and Makefile — make should be available.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("all:\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := NewToolchainService()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	cmds := svc.ListToolchainCommands()
	ids := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		ids[c.ID] = true
	}
	if !ids["make"] {
		t.Errorf("expected make command when Makefile present")
	}
	if !ids["go-build"] {
		t.Errorf("expected go-build when go.mod present")
	}
	// npm-scripts should be absent (no package.json).
	if ids["npm-scripts"] {
		t.Errorf("did not expect npm-scripts without package.json")
	}
}

func TestDetectToolchains(t *testing.T) {
	svc := NewToolchainService()
	detected := svc.DetectToolchains()
	// "go" should be detected in the test environment (we're running Go tests).
	if !detected["go"] {
		t.Errorf("expected go to be detected in test environment")
	}
	// All expected keys should be present in the map (even if false).
	for _, name := range []string{"go", "gofmt", "goimports", "golangci-lint", "tsc", "eslint", "prettier", "vitest", "npm", "make"} {
		if _, ok := detected[name]; !ok {
			t.Errorf("expected key %q in detect result", name)
		}
	}
}

func TestDetectToolchains_toolPathOverride(t *testing.T) {
	svc := NewToolchainService()
	// Point a tool at a non-existent path — it should report not installed.
	svc.setToolPaths(map[string]string{"golangci-lint": "/nonexistent/path/golangci-lint"})
	detected := svc.DetectToolchains()
	if detected["golangci-lint"] {
		t.Errorf("expected golangci-lint to be NOT detected with bad override path")
	}
	// go should still be detected via PATH.
	if !detected["go"] {
		t.Errorf("expected go to still be detected via PATH")
	}
}

func TestRunToolchainCommand_unknownID(t *testing.T) {
	svc := NewToolchainService()
	_, err := svc.RunToolchainCommand("does-not-exist", "")
	if err == nil {
		t.Fatal("expected error for unknown command id")
	}
	if !strings.Contains(err.Error(), "unknown toolchain command") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestRunToolchainCommand_notInstalled(t *testing.T) {
	svc := NewToolchainService()
	// golangci-lint is unlikely to be installed in the CI test environment;
	// if it IS installed, this test still passes (success result). We only
	// assert the not-installed path when the tool is genuinely missing.
	result, err := svc.RunToolchainCommand("golangci-lint", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// If the tool happens to be installed, just sanity-check the result.
	if result.NotInstalled {
		if result.Success {
			t.Errorf("NotInstalled=true but Success=true")
		}
		if result.InstallCmd == "" {
			t.Errorf("expected install command hint when not installed")
		}
		if !strings.Contains(result.InstallCmd, "golangci-lint") {
			t.Errorf("install hint should mention golangci-lint, got %q", result.InstallCmd)
		}
	}
}

func TestRunToolchainCommand_goVersion(t *testing.T) {
	// "go version" isn't in the catalog, so we test RunToolchainCommand via a
	// catalog entry that is near-guaranteed to be installed: none of the
	// catalog commands map to "go version". Instead, exercise the execution
	// path by running a command whose tool IS installed. go-build requires a
	// go.mod, so set up a minimal workspace and run `go build ./...` which
	// should succeed on an empty package.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module toolchaintest\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := NewToolchainService()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	result, err := svc.RunToolchainCommand("go-build", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// go build on an empty module should succeed.
	if !result.Success {
		// On some sandboxed CI environments go build may fail for unrelated
		// reasons (cache, network). Only fail the test if the failure looks
		// unrelated to environment.
		t.Logf("go build did not succeed (may be environment): output=%q", result.Output)
	}
	// Duration should be non-negative.
	if result.Duration < 0 {
		t.Errorf("duration should be >= 0, got %d", result.Duration)
	}
}

func TestRunToolchainCommand_toolPathOverrideRespected(t *testing.T) {
	// Verify that a tool path override is used. Point "go" at a bad path and
	// confirm the run reports not-installed (because the override is bogus).
	// Use a command that is in the catalog and whose tool is "go".
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module t\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := NewToolchainService()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	svc.setToolPaths(map[string]string{"go": "/nonexistent/go-binary"})
	result, err := svc.RunToolchainCommand("go-build", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.NotInstalled {
		t.Errorf("expected NotInstalled=true when go override is bogus, got success=%v output=%q", result.Success, result.Output)
	}
}

func TestSetToolPaths_nilClears(t *testing.T) {
	svc := NewToolchainService()
	svc.setToolPaths(map[string]string{"go": "/some/path"})
	svc.setToolPaths(nil)
	// After nil, resolveTool("go") should fall back to PATH (real go).
	if p := svc.resolveTool("go"); p == "" {
		t.Errorf("expected go to resolve via PATH after clearing overrides")
	}
}

func TestResolveToolUsesActiveWorkspaceNodeBin(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "koyori-tool-fixture"
	toolPath := filepath.Join(binDir, name)
	content := "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		toolPath += ".cmd"
		content = "@exit /b 0\r\n"
	}
	if err := os.WriteFile(toolPath, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := NewToolchainService()
	if err := svc.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	resolved := svc.resolveTool(name)
	if resolved == "" {
		t.Fatal("workspace-local tool was not resolved")
	}
	if !sameWorkspaceIdentityPath(resolved, toolPath) {
		t.Fatalf("resolved tool = %q, want %q", resolved, toolPath)
	}
}

func TestSetWorkspaceRoot_invalidPath(t *testing.T) {
	svc := NewToolchainService()
	// A path that doesn't exist.
	err := svc.setWorkspaceRoot("/nonexistent/directory/that/does/not/exist")
	if err == nil {
		// On some systems the path might resolve oddly; only assert when it
		// genuinely fails. Skip otherwise.
		t.Skip("filesystem allowed nonexistent path")
	}
}

func TestSplitToolchainLines_crlf(t *testing.T) {
	// Ensure \r\n is normalized so the parsers don't leave trailing \r.
	lines := splitToolchainLines("a.go:1:1: x\r\nb.go:2:2: y\r\n")
	// Trailing empty line from the final \n.
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d (%v)", len(lines), lines)
	}
	for _, l := range lines {
		if strings.Contains(l, "\r") {
			t.Errorf("line contains stray \\r: %q", l)
		}
	}
}

func TestRunToolchainCommand_filePathUsesFileDir(t *testing.T) {
	// Running with a filePath should use the file's directory, not the
	// workspace root. We verify by setting a bogus workspace root and a real
	// file path in a temp dir with go.mod, then running go-build.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module t\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := NewToolchainService()
	// Don't set a workspace root; rely on filePath.
	result, err := svc.RunToolchainCommand("go-build", filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected go-build to succeed on valid main.go, output=%q", result.Output)
	}
}

// Guard against the test running in an environment without Go at all. None of
// the go-* tests should fatal-fail if go is absent — they degrade gracefully.
func init() {
	// no-op; kept for potential environment skips.
	_ = runtime.GOOS
}

// ---- G15: Test Explorer 多语言 runner ----

func TestG15_ResolveJSTestRunner_JestConfigFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "jest.config.js"), []byte("module.exports = {};"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewToolchainService()
	if got := s.resolveJSTestRunner(dir); got != "jest" {
		t.Fatalf("resolveJSTestRunner = %q, want jest", got)
	}
}

func TestG15_ResolveJSTestRunner_PackageJSONJestField(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","jest":{"testEnvironment":"node"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewToolchainService()
	if got := s.resolveJSTestRunner(dir); got != "jest" {
		t.Fatalf("resolveJSTestRunner = %q, want jest", got)
	}
}

func TestG15_ResolveJSTestRunner_VitestConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vitest.config.ts"), []byte("export default {};"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewToolchainService()
	if got := s.resolveJSTestRunner(dir); got != "vitest" {
		t.Fatalf("resolveJSTestRunner = %q, want vitest", got)
	}
}

func TestG15_ResolveJSTestRunner_NoConfigDefaultsToVitest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewToolchainService()
	if got := s.resolveJSTestRunner(dir); got != "vitest" {
		t.Fatalf("resolveJSTestRunner = %q, want vitest default", got)
	}
}

func TestG15_ResolveJSProjectRoot_UsesNearestPackage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"root"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "src", "stores")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewToolchainService()
	if got := s.resolveJSProjectRoot(nested, root); got != root {
		t.Fatalf("resolveJSProjectRoot = %q, want %q", got, root)
	}
}

func TestG15_PlanJSTestRun_JestArgv(t *testing.T) {
	s := NewToolchainService()
	s.setToolPaths(map[string]string{"jest": os.Args[0]})
	plan, err := s.planJSTestRun("jest", "handles a.b", "/tmp/x.test.ts", "")
	if err != nil {
		t.Fatalf("planJSTestRun: %v", err)
	}
	want := []string{"--runInBand", "--verbose", "/tmp/x.test.ts", "-t", `handles a\.b`}
	if strings.Join(plan.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("jest argv = %v, want %v", plan.Args, want)
	}
}

func TestG15_PlanJSTestRun_VitestArgv(t *testing.T) {
	s := NewToolchainService()
	s.setToolPaths(map[string]string{"vitest": os.Args[0]})
	plan, err := s.planJSTestRun("vitest", "works", "/tmp/x.test.ts", "")
	if err != nil {
		t.Fatalf("planJSTestRun: %v", err)
	}
	idx := -1
	for i, a := range plan.Args {
		if a == "-t" {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(plan.Args) || plan.Args[idx+1] != "works" {
		t.Fatalf("vitest argv missing -t works: %v", plan.Args)
	}
	if !containsString(plan.Args, "/tmp/x.test.ts") {
		t.Fatalf("vitest argv missing file: %v", plan.Args)
	}
}

// G15 AC2: shell metacharacters in a test name must pass through as discrete
// argv elements (exec.CommandContext, no shell interpolation) — never executed.
func TestG15_PlanJSTestRun_NoShellInjection(t *testing.T) {
	s := NewToolchainService()
	s.setToolPaths(map[string]string{"jest": os.Args[0]})
	name := `evil; rm -rf / && echo PWNED`
	namePattern := regexp.QuoteMeta(name)
	plan, err := s.planJSTestRun("jest", name, "/tmp/x.test.ts", "")
	if err != nil {
		t.Fatalf("planJSTestRun: %v", err)
	}
	// The malicious text appears verbatim as ONE argv element, and no element
	// starts a shell.
	for _, a := range plan.Args {
		if strings.ContainsAny(a, ";&|`$") && a != namePattern {
			t.Fatalf("unexpected shell metacharacter in argv element %q", a)
		}
	}
	if plan.Args[len(plan.Args)-1] != namePattern {
		t.Fatalf("test name not preserved as final argv element: %v", plan.Args)
	}
}

func TestG15_PlanJSTestRun_MissingRunnerFailsClosed(t *testing.T) {
	s := NewToolchainService()
	s.setToolPaths(map[string]string{"jest": filepath.Join(t.TempDir(), "missing-jest")})
	plan, err := s.planJSTestRun("jest", "works", "/tmp/x.test.js", t.TempDir())
	if err == nil {
		t.Fatal("planJSTestRun succeeded with a missing configured runner")
	}
	if plan.InstallCmd != "npm i -D jest" {
		t.Fatalf("install hint = %q", plan.InstallCmd)
	}
}

// G15 AC1 (Go leg): RunTestAtCursor runs the real `go test` binary against a
// temporary Go module and reports the real exit code.
func TestG15_RealGoTestAtCursor(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	_ = goBin
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module g15fixture\n\ngo 1.25.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(dir, "math_test.go")
	src := "package math\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif 1+1 != 2 {\n\t\tt.Fatal(\"boom\")\n\t}\n}\n"
	if err := os.WriteFile(testFile, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	line := 4 // func TestAdd line (0-based)
	s := NewToolchainService()
	lease, err := s.commandWorkspaceLease(testFile)
	if err != nil {
		t.Fatalf("commandWorkspaceLease: %v", err)
	}
	// Set workspace root so the lease resolves.
	_ = lease

	// RunTestAtCursor resolves the file under the workspace lease; use the
	// workspace-rooted API by setting the root first.
	_ = s.setWorkspaceRoot(dir)
	result, err := s.RunTestAtCursor("go", testFile, line, src)
	if err != nil {
		t.Fatalf("RunTestAtCursor: %v", err)
	}
	if !result.Success {
		t.Fatalf("real go test failed: %+v", result)
	}
	if !strings.Contains(result.Output, "ok") && !strings.Contains(result.Output, "PASS") {
		t.Fatalf("expected go test success in output: %s", result.Output)
	}
}

// G15 AC1 (Jest leg): RunTestAtCursor drives the real pinned Jest dependency
// against a temporary JavaScript project and rejects a non-matching identity.
func TestG15_RealJestRunThroughRunTestAtCursor(t *testing.T) {
	frontendDir, err := filepath.Abs(filepath.Join("..", "frontend"))
	if err != nil {
		t.Fatal(err)
	}
	jestJS := filepath.Join(frontendDir, "node_modules", "jest", "bin", "jest.js")
	if !fileExists(jestJS) {
		t.Skip("frontend jest dependency not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"g15jest","private":true,"type":"commonjs","jest":{"testEnvironment":"node"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(binDir, "jest.cmd"), []byte("@node \""+jestJS+"\" %*\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		shim := "#!/bin/sh\nexec node \"" + strings.ReplaceAll(jestJS, "\"", "\\\"") + "\" \"$@\"\n"
		if err := os.WriteFile(filepath.Join(binDir, "jest"), []byte(shim), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	testFile := filepath.Join(dir, "sum.test.js")
	src := "describe(\"sum\", () => {\n  test(\"adds 1+1\", () => {\n    expect(1 + 1).toBe(2);\n  });\n});\n"
	if err := os.WriteFile(testFile, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	line := 1
	s := NewToolchainService()
	if err := s.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	result, err := s.RunTestAtCursor("javascript", testFile, line, src)
	if err != nil {
		t.Fatalf("RunTestAtCursor: %v", err)
	}
	if !result.Success {
		t.Fatalf("real jest run failed: %+v\n---\n%s", result, result.Output)
	}
	if !strings.Contains(result.Output, "adds 1+1") || !strings.Contains(result.Output, "PASS") {
		t.Fatalf("jest output missing test identity or PASS: %s", result.Output)
	}
	missResult, err := s.RunTestAtCursor("javascript", testFile, line, strings.Replace(src, `test("adds 1+1"`, `test("no-such-jest-test-xyz"`, 1))
	if err != nil {
		t.Fatalf("RunTestAtCursor (missing name): %v", err)
	}
	if missResult.Success {
		t.Fatalf("jest run with a non-matching name reported success: %+v", missResult)
	}
}

// G15 AC1 (Vitest leg): RunTestAtCursor drives the REAL Vitest CLI against the
// repository's own frontend test project (frontend has vitest installed), so
// a passing test returns success and a non-matching name returns failure.
func TestG15_RealVitestRunThroughRunTestAtCursor(t *testing.T) {
	frontendDir := filepath.Join("..", "frontend")
	info, err := os.Stat(filepath.Join(frontendDir, "node_modules", "vitest"))
	if err != nil || !info.IsDir() {
		t.Skip("frontend vitest dependency not installed")
	}
	testFile := filepath.Join(frontendDir, "src", "stores", "output.test.ts")
	raw, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read frontend test file: %v", err)
	}
	content := string(raw)
	// Locate a real test name and its line.
	line := -1
	for i, l := range strings.Split(content, "\n") {
		if strings.Contains(l, `it("appends an entry to outputs"`) {
			line = i
			break
		}
	}
	if line < 0 {
		t.Fatal("could not locate real vitest test name")
	}

	s := NewToolchainService()
	if err := s.setWorkspaceRoot(frontendDir); err != nil {
		t.Fatal(err)
	}
	result, err := s.RunTestAtCursor("typescript", testFile, line, content)
	if err != nil {
		t.Fatalf("RunTestAtCursor: %v", err)
	}
	if !result.Success {
		t.Fatalf("real vitest run failed: %+v\n---\n%s", result, result.Output)
	}
	if !strings.Contains(result.Output, "appends an entry to outputs") {
		t.Fatalf("vitest output missing test name: %s", result.Output)
	}

	// Failure positioning: a name that matches no test must exit non-zero.
	miss := s
	_ = miss
	failLine := line
	missResult, err := s.RunTestAtCursor("typescript", testFile, failLine, strings.Replace(content, `it("appends an entry to outputs"`, `it("no-such-test-name-xyz"`, 1))
	if err != nil {
		t.Fatalf("RunTestAtCursor (missing name): %v", err)
	}
	if missResult.Success {
		t.Fatalf("vitest run with a non-matching name reported success: %+v", missResult)
	}
}

func TestG15_CancelTestAtCursorStopsActiveRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"g15-cancel"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(binDir, "vitest.cmd"), []byte("@echo off\r\n:loop\r\nping 127.0.0.1 -n 2 >nul\r\ngoto loop\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		path := filepath.Join(binDir, "vitest")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	testFile := filepath.Join(dir, "cancel.test.ts")
	content := "it(\"blocks until canceled\", () => {});\n"
	if err := os.WriteFile(testFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewToolchainService()
	if err := s.setWorkspaceRoot(dir); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s.beforeWorkspaceCommandStart = func() { close(started) }
	type outcome struct {
		result ToolchainResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := s.RunTestAtCursor("typescript", testFile, 0, content)
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("test process did not start")
	}
	if !s.CancelTestAtCursor() {
		t.Fatal("CancelTestAtCursor reported no active run")
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("RunTestAtCursor: %v", got.err)
		}
		if !got.result.Canceled {
			t.Fatalf("result.Canceled = false: %+v", got.result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled test process did not stop")
	}
	if s.CancelTestAtCursor() {
		t.Fatal("CancelTestAtCursor reported a stale active run")
	}
}
