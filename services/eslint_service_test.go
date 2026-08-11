package services

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func init() {
	if os.Getenv("GO_WANT_ESLINT_HELPER") != "1" {
		return
	}
	var output string
	exitCode := 0
	switch os.Getenv("ESLINT_HELPER_MODE") {
	case "diagnostics":
		output = `[{"filePath":"input.ts","messages":[{"line":1,"column":1,"severity":2,"message":"problem","ruleId":"test-rule"}]}]`
		exitCode = 1
	case "failure":
		output = "eslint command failed"
		exitCode = 2
	case "success":
		output = `[{"filePath":"input.ts","messages":[]}]`
	default:
		output = "unknown helper mode"
		exitCode = 3
	}
	if _, err := os.Stdout.WriteString(output); err != nil {
		os.Exit(4)
	}
	os.Exit(exitCode)
}

func TestEslint_FailureReportedWhenDiagsExist(t *testing.T) {
	helperDir := t.TempDir()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	helperBinary := filepath.Join(helperDir, "eslint")
	if runtime.GOOS == "windows" {
		helperBinary += ".exe"
	}
	data, err := os.ReadFile(testBinary)
	if err != nil {
		t.Fatalf("read test executable: %v", err)
	}
	if err := os.WriteFile(helperBinary, data, 0755); err != nil {
		t.Fatalf("write eslint helper: %v", err)
	}
	t.Setenv("PATH", helperDir)
	t.Setenv("GO_WANT_ESLINT_HELPER", "1")

	tests := []struct {
		name        string
		mode        string
		wantSuccess bool
		wantDiags   int
	}{
		{name: "diagnostics", mode: "diagnostics", wantSuccess: false, wantDiags: 1},
		{name: "command failure without diagnostics", mode: "failure", wantSuccess: false},
		{name: "command success without diagnostics", mode: "success", wantSuccess: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ESLINT_HELPER_MODE", tt.mode)
			result, err := NewEslintService().LintFile(filepath.Join(helperDir, "input.ts"), "const value = 1", "")
			if err != nil {
				t.Fatalf("LintFile returned error: %v", err)
			}
			if result.Success != tt.wantSuccess {
				t.Errorf("Success = %v, want %v; output = %q", result.Success, tt.wantSuccess, result.Output)
			}
			if len(result.Diagnostics) != tt.wantDiags {
				t.Errorf("diagnostics = %d, want %d", len(result.Diagnostics), tt.wantDiags)
			}
		})
	}
}
