package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeCoverageJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newCoverageServiceAt(t *testing.T, root string) *CoverageService {
	t.Helper()
	svc := NewCoverageService()
	if err := svc.setWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestParseIstanbulCoverageFinal(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src", "math.ts")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("export const value = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(root, "coverage", "coverage-final.json")
	writeCoverageJSON(t, reportPath, map[string]any{
		source: map[string]any{
			"path": source,
			"statementMap": map[string]any{
				"0": map[string]any{"start": map[string]int{"line": 1, "column": 0}, "end": map[string]int{"line": 1, "column": 10}},
				"1": map[string]any{"start": map[string]int{"line": 2, "column": 0}, "end": map[string]int{"line": 2, "column": 10}},
			},
			"fnMap": map[string]any{
				"0": map[string]any{"decl": map[string]any{"start": map[string]int{"line": 2, "column": 0}, "end": map[string]int{"line": 2, "column": 1}}},
			},
			"branchMap": map[string]any{
				"0": map[string]any{"locations": []any{
					map[string]any{"start": map[string]int{"line": 1, "column": 0}, "end": map[string]int{"line": 1, "column": 1}},
					map[string]any{"start": map[string]int{"line": 1, "column": 2}, "end": map[string]int{"line": 1, "column": 3}},
				}},
			},
			"s": map[string]int{"0": 1, "1": 0},
			"f": map[string]int{"0": 1},
			"b": map[string][]int{"0": {1, 0}},
		},
		filepath.Join(root, "..", "outside.ts"): map[string]any{
			"path":         filepath.Join(root, "..", "outside.ts"),
			"statementMap": map[string]any{"0": map[string]any{"start": map[string]int{"line": 1}}},
			"s":            map[string]int{"0": 1},
		},
	})

	report, err := newCoverageServiceAt(t, root).ParseIstanbulCoverage(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.Format != "coverage-final" || len(report.Files) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	file := report.Files[0]
	if file.File != "src/math.ts" {
		t.Fatalf("file path = %q", file.File)
	}
	if file.Statements.Total != 2 || file.Statements.Covered != 1 ||
		file.Branches.Total != 2 || file.Branches.Covered != 1 ||
		file.Functions.Total != 1 || file.Functions.Covered != 1 ||
		file.Lines.Total != 2 || file.Lines.Covered != 1 {
		t.Fatalf("unexpected metrics: %+v", file)
	}
	if len(file.Hits) != 2 || file.Hits[0].Status != CoverageStatusPartial || file.Hits[1].Status != CoverageStatusPartial {
		t.Fatalf("expected partial lines, got %+v", file.Hits)
	}
	if report.Statements != file.Statements || report.Branches != file.Branches {
		t.Fatalf("report totals do not match included file: %+v", report)
	}
}

func TestParseIstanbulJSONSummary(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src", "summary.ts")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	metric := func(total, covered int) map[string]any {
		return map[string]any{"total": total, "covered": covered, "skipped": 0, "pct": 50}
	}
	reportPath := filepath.Join(root, "coverage", "coverage-summary.json")
	writeCoverageJSON(t, reportPath, map[string]any{
		"total": map[string]any{"lines": metric(99, 99), "statements": metric(99, 99), "functions": metric(99, 99), "branches": metric(99, 99)},
		source: map[string]any{
			"lines": metric(4, 3), "statements": metric(5, 4), "functions": metric(2, 1), "branches": metric(6, 3),
		},
		filepath.Join(root, "..", "outside.ts"): map[string]any{
			"lines": metric(10, 10), "statements": metric(10, 10), "functions": metric(10, 10), "branches": metric(10, 10),
		},
	})

	report, err := newCoverageServiceAt(t, root).ParseIstanbulCoverage(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.Format != "json-summary" || len(report.Files) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Statements.Total != 5 || report.Statements.Covered != 4 || report.Lines.Total != 4 || report.Lines.Covered != 3 {
		t.Fatalf("totals must be recomputed from included files: %+v", report)
	}
	if len(report.Files[0].Hits) != 0 {
		t.Fatalf("json-summary cannot provide line locations: %+v", report.Files[0].Hits)
	}
}

func TestParseIstanbulCoverageRejectsUnsafeReport(t *testing.T) {
	root := t.TempDir()
	svc := newCoverageServiceAt(t, root)

	t.Run("outside workspace", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "coverage-final.json")
		writeCoverageJSON(t, outside, map[string]any{})
		if _, err := svc.ParseIstanbulCoverage(outside); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("expected outside-workspace error, got %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(root, "coverage", "coverage-final.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(maxIstanbulCoverageBytes + 1); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ParseIstanbulCoverage(path); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("expected size error, got %v", err)
		}
	})

	t.Run("null file record", func(t *testing.T) {
		path := filepath.Join(root, "coverage", "coverage-final.json")
		writeCoverageJSON(t, path, map[string]any{"src/broken.ts": nil})
		if _, err := svc.ParseIstanbulCoverage(path); err == nil || !strings.Contains(err.Error(), "null") {
			t.Fatalf("expected malformed-record error, got %v", err)
		}
	})
}

func TestCoverageMetricRoundsPercentage(t *testing.T) {
	got := metric(3, 1, 0)
	if got.Pct != 33.33 {
		t.Fatalf("pct = %v, want 33.33", got.Pct)
	}
}

func TestBuildVitestCoverageCommandUsesPackageManagerArgv(t *testing.T) {
	tests := []struct {
		name           string
		packageManager string
		lockfile       string
		executable     string
		prefix         []string
	}{
		{name: "npm default", executable: "npm", prefix: []string{"exec", "--", "vitest"}},
		{name: "pnpm packageManager", packageManager: "pnpm@10.0.0", executable: "pnpm", prefix: []string{"exec", "vitest"}},
		{name: "yarn lock", lockfile: "yarn.lock", executable: "yarn", prefix: []string{"exec", "vitest"}},
		{name: "bun lock", lockfile: "bun.lockb", executable: "bun", prefix: []string{"x", "vitest"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			pkg := map[string]any{"name": "coverage-test"}
			if tt.packageManager != "" {
				pkg["packageManager"] = tt.packageManager
			}
			writeCoverageJSON(t, filepath.Join(root, "package.json"), pkg)
			if tt.lockfile != "" {
				if err := os.WriteFile(filepath.Join(root, tt.lockfile), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			spec, err := BuildVitestCoverageCommand(root)
			if err != nil {
				t.Fatal(err)
			}
			if spec.Executable != tt.executable || !reflect.DeepEqual(spec.Args[:len(tt.prefix)], tt.prefix) {
				t.Fatalf("unsafe or wrong argv: %+v", spec)
			}
			joined := strings.Join(spec.Args, " ")
			for _, want := range []string{"run", "--coverage", "--coverage.reporter=json", "--coverage.reportsDirectory=coverage"} {
				if !strings.Contains(joined, want) {
					t.Fatalf("argv %q missing %q", joined, want)
				}
			}
		})
	}
}

func TestRunVitestCoverageReturnsActionableMissingToolError(t *testing.T) {
	root := t.TempDir()
	writeCoverageJSON(t, filepath.Join(root, "package.json"), map[string]any{"packageManager": "pnpm@10"})
	svc := newCoverageServiceAt(t, root)
	svc.lookPath = func(string) (string, error) { return "", errors.New("not found") }

	result, err := svc.RunVitestCoverage(context.Background(), root, 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !result.NotInstalled || !strings.Contains(result.Output, "pnpm") || !strings.Contains(result.Output, "install") {
		t.Fatalf("expected actionable missing-tool result, got %+v", result)
	}
}

func TestRunVitestCoverageParsesGeneratedReport(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src", "generated.ts")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeCoverageJSON(t, filepath.Join(root, "package.json"), map[string]any{"packageManager": "npm@11"})
	svc := newCoverageServiceAt(t, root)
	svc.lookPath = func(name string) (string, error) {
		if name != "npm" {
			t.Fatalf("unexpected executable lookup: %s", name)
		}
		return "C:/tools/npm.cmd", nil
	}
	svc.runVitest = func(_ context.Context, spec CoverageCommand) ([]byte, error) {
		if spec.Executable != "C:/tools/npm.cmd" || spec.Dir != root {
			t.Fatalf("unexpected command: %+v", spec)
		}
		writeCoverageJSON(t, filepath.Join(root, "coverage", "coverage-final.json"), map[string]any{
			source: map[string]any{
				"path": source,
				"statementMap": map[string]any{
					"0": map[string]any{"start": map[string]int{"line": 8, "column": 0}, "end": map[string]int{"line": 8, "column": 1}},
				},
				"s": map[string]int{"0": 1},
			},
		})
		return []byte("vitest ok"), nil
	}

	result, err := svc.RunVitestCoverage(context.Background(), root, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Output != "vitest ok" || len(result.Report.Files) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if hits := result.Report.Files[0].Hits; len(hits) != 1 || hits[0].Line != 8 || hits[0].Status != CoverageStatusCovered {
		t.Fatalf("unexpected generated hits: %+v", hits)
	}

	// Relative report paths are workspace-relative and remain sandboxed.
	report, err := svc.ParseIstanbulCoverage(filepath.Join("coverage", "coverage-final.json"))
	if err != nil || len(report.Files) != 1 {
		t.Fatalf("relative report path failed: report=%+v err=%v", report, err)
	}
}

func TestRunVitestCoverageReportsCancellation(t *testing.T) {
	root := t.TempDir()
	writeCoverageJSON(t, filepath.Join(root, "package.json"), map[string]any{"name": "cancel-test"})
	svc := newCoverageServiceAt(t, root)
	svc.lookPath = func(string) (string, error) { return "npm", nil }
	svc.runVitest = func(ctx context.Context, _ CoverageCommand) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := svc.RunVitestCoverage(ctx, root, 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !result.Cancelled || !strings.Contains(result.Output, "process tree was terminated") {
		t.Fatalf("unexpected cancellation result: %+v", result)
	}
}

func TestRunVitestCoverageDoesNotReuseStaleReport(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src", "stale.ts")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeCoverageJSON(t, filepath.Join(root, "package.json"), map[string]any{"name": "stale-test"})
	reportPath := filepath.Join(root, "coverage", "coverage-final.json")
	writeCoverageJSON(t, reportPath, map[string]any{
		source: map[string]any{
			"path": source,
			"statementMap": map[string]any{
				"0": map[string]any{"start": map[string]int{"line": 1}, "end": map[string]int{"line": 1}},
			},
			"s": map[string]int{"0": 1},
		},
	})
	svc := newCoverageServiceAt(t, root)
	svc.lookPath = func(string) (string, error) { return "npm", nil }
	svc.runVitest = func(context.Context, CoverageCommand) ([]byte, error) {
		return []byte("completed without reporter"), nil
	}

	result, err := svc.RunVitestCoverage(context.Background(), root, 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Output, "not refreshed") {
		t.Fatalf("stale report must not be accepted: %+v", result)
	}
}
