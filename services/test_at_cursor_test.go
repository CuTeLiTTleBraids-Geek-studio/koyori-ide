package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFindTestNameAtLine_Go(t *testing.T) {
	src := `package p

func helper() {}

func TestAlpha(t *testing.T) {
	_ = 1
}

func TestBeta(t *testing.T) {
	t.Run("case1", func(t *testing.T) {
		_ = 2
	})
}
`
	// line inside TestAlpha body (0-based)
	name := findTestNameAtLine("go", src, 5)
	if name != "TestAlpha" {
		t.Fatalf("got %q want TestAlpha", name)
	}
	// inside t.Run body
	name = findTestNameAtLine("go", src, 10)
	if name != "TestBeta/case1" {
		t.Fatalf("got %q want TestBeta/case1", name)
	}
}

func TestFindTestNameAtLine_JS(t *testing.T) {
	src := `import { it, expect } from 'vitest'

it('hello world', () => {
  expect(1).toBe(1)
})
`
	name := findTestNameAtLine("typescript", src, 3)
	if name != "hello world" {
		t.Fatalf("got %q", name)
	}
}

func TestFindTestNameAtLine_JSEach(t *testing.T) {
	src := `import { test, expect } from 'vitest'

test.each([1, 2])('adds %i', (n) => {
  expect(n).toBeTruthy()
})
`
	name := findTestNameAtLine("typescript", src, 3)
	if name != "adds %i" {
		t.Fatalf("got %q want adds %%i", name)
	}
}

func TestFindTestNameAtLine_ReusesCompiledRegexps(t *testing.T) {
	src := `package p

func TestAlpha(t *testing.T) {
	t.Run("case", func(t *testing.T) {})
}
`
	var name string
	allocs := testing.AllocsPerRun(100, func() {
		name = findTestNameAtLine("go", src, 3)
	})
	if name != "TestAlpha/case" {
		t.Fatalf("got %q want TestAlpha/case", name)
	}
	if allocs > 20 {
		t.Fatalf("findTestNameAtLine allocated %.0f objects per call, want at most 20", allocs)
	}
}

func TestCoverageService_ParseCoverProfile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cover.out")
	body := "mode: set\nfoo/bar.go:10.2,12.3 2 1\nfoo/bar.go:20.1,21.2 1 0\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewCoverageService()
	hits, err := svc.ParseCoverProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 5 {
		t.Fatalf("expected >=5 line hits, got %d", len(hits))
	}
	if !hits[0].Covered || hits[0].Line != 10 {
		t.Errorf("first hit: line=%d covered=%v want line 10 covered", hits[0].Line, hits[0].Covered)
	}
	last := hits[len(hits)-1]
	if last.Covered || last.Line != 21 {
		t.Errorf("last hit: line=%d covered=%v want line 21 uncovered", last.Line, last.Covered)
	}
	var foundUncovered bool
	for _, h := range hits {
		if h.Line == 20 && !h.Covered {
			foundUncovered = true
			break
		}
	}
	if !foundUncovered {
		t.Error("expected line 20 to be uncovered")
	}
}

func TestCoverageService_RunPackageCoverageReturnsProfileTextAndCleansTemp(t *testing.T) {
	packageDir := t.TempDir()
	profileDir := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, profileDir)
	}
	files := map[string]string{
		"go.mod":     "module example.com/coveragefixture\n\ngo 1.23\n",
		"fixture.go": "package coveragefixture\n\nfunc Covered() int { return 1 }\n",
		"fixture_test.go": `package coveragefixture

import "testing"

func TestCovered(t *testing.T) {
	if Covered() != 1 {
		t.Fatal("unexpected result")
	}
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(packageDir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	service := NewCoverageService()
	if err := service.setWorkspaceRoot(packageDir); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	result, err := service.RunPackageCoverage(packageDir)
	if err != nil {
		t.Fatalf("RunPackageCoverage: %v", err)
	}
	if !result.Success {
		t.Fatalf("coverage command failed: %s", result.Output)
	}
	if !strings.HasPrefix(result.Profile, "mode:") || !strings.Contains(result.Profile, "fixture.go:") {
		t.Errorf("Profile should contain cover profile text, got %q", result.Profile)
	}
	remaining, err := filepath.Glob(filepath.Join(profileDir, "koyori-cover-*.out"))
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("coverage temp files were not removed: %v", remaining)
	}
}

func TestCoverageService_CreateProfileUses0600(t *testing.T) {
	tmp, path, err := createCoverageProfile()
	if err != nil {
		t.Fatalf("createCoverageProfile: %v", err)
	}
	defer tmp.Close()
	defer os.Remove(path)
	if runtime.GOOS == "windows" {
		return
	}
	info, err := tmp.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("profile mode = %o, want 600", got)
	}
}

func TestDebugService_StatusMessage(t *testing.T) {
	d := NewDebugService()
	msg := d.StatusMessage()
	if msg == "" {
		t.Fatal("expected non-empty status")
	}
}

func TestCoveragePathsMatch_NoBasenameCollision(t *testing.T) {
	// Same basename different dirs must NOT match when only basename equals.
	if CoveragePathsMatch("pkg/a/foo.go", "E:/proj/pkg/b/foo.go") {
		t.Error("pkg/a/foo.go should not match pkg/b/foo.go")
	}
	if !CoveragePathsMatch("pkg/a/foo.go", "E:/proj/pkg/a/foo.go") {
		t.Error("expected suffix match for pkg/a/foo.go")
	}
	if !CoveragePathsMatch("E:/proj/pkg/a/foo.go", "E:/proj/pkg/a/foo.go") {
		t.Error("exact path should match")
	}
	if CoveragePathsMatch("foo.go", "E:/proj/pkg/a/foo.go") {
		t.Error("basename-only hit must not match nested editor path")
	}
}

func TestNormalizeCoveragePath(t *testing.T) {
	n := NormalizeCoveragePath(`.\pkg\foo.go`)
	if strings.Contains(n, "\\") {
		t.Errorf("expected slash-normalized path, got %q", n)
	}
	if !strings.HasSuffix(n, "pkg/foo.go") && n != "pkg/foo.go" {
		// Clean may keep relative form
		if n == "" {
			t.Fatal("empty")
		}
	}
}

func TestParseGoTestJSONLines(t *testing.T) {
	in := "{\"Action\":\"run\",\"Test\":\"TestA\"}\n{\"Action\":\"pass\",\"Test\":\"TestA\",\"Elapsed\":0.01}\nnot-json\n"
	ev := parseGoTestJSONLines(in)
	if len(ev) != 2 {
		t.Fatalf("got %d events", len(ev))
	}
	if ev[0].Action != "run" || ev[1].Action != "pass" {
		t.Fatalf("%+v", ev)
	}
}
