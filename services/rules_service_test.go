package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// N-112: SaveRules must reject writes through a symlink that escapes the
// project root. The previous lexical-only check (filepath.IsAbs + ".."
// prefix) would accept "link/rules.md" because it looks like a normal
// relative path, but if "link" is a symlink to an outside directory, the
// write would land outside the project. IsPathOutsideRoot resolves
// symlinks before comparing, so this escape is now blocked.
func TestRulesService_SaveRules_N112_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	// Verify the symlink actually resolves. On some Windows configurations
	// os.Symlink succeeds but EvalSymlinks doesn't resolve directory
	// symlinks — in that case the test can't verify the N-112 fix.
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Skipf("EvalSymlinks failed on this platform: %v", err)
	}
	if samePath(resolved, link) {
		t.Skipf("symlink not resolved by EvalSymlinks on this platform (got %q)", resolved)
	}
	svc := NewRulesService("")
	err = svc.SaveRules(root, "link/rules.md", "content")
	if err == nil {
		t.Fatal("expected error for symlink escape, got nil")
	}
	// Verify the file was NOT created outside the project.
	if _, statErr := os.Stat(filepath.Join(outside, "rules.md")); statErr == nil {
		t.Error("rules file should not have been written outside the project root")
	}
}

// samePath compares two paths case-insensitively on Windows, case-sensitively
// elsewhere. Used to detect whether EvalSymlinks resolved a symlink or
// returned the link path unchanged.
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// N-112: SaveRules allows normal writes inside the project root (sanity
// check that the symlink-aware check doesn't reject valid paths).
func TestRulesService_SaveRules_N112_AllowsNormalWrite(t *testing.T) {
	root := t.TempDir()
	svc := NewRulesService("")
	err := svc.SaveRules(root, ".koyori-ide/rules.md", "content")
	if err != nil {
		t.Fatalf("SaveRules failed for normal path: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".koyori-ide", "rules.md"))
	if err != nil {
		t.Fatalf("expected rules file to be written: %v", err)
	}
	if string(data) != "content" {
		t.Errorf("expected 'content', got %q", string(data))
	}
}

// N-112: SaveRules still rejects the classic lexical escapes that the
// previous check already handled. The symlink-aware check must not
// weaken the existing guard. Note: Unix-style absolute paths ("/tmp/...")
// are only rejected on Unix platforms — on Windows, filepath.IsAbs does
// not recognize them and filepath.Join treats them as relative. That
// pre-existing gap is outside N-112's scope.
func TestRulesService_SaveRules_N112_RejectsLexicalEscape(t *testing.T) {
	root := t.TempDir()
	svc := NewRulesService("")
	tests := []struct {
		name string
		path string
	}{
		{"parent-traversal", "../evil.md"},
		{"nested-parent", "sub/../../evil.md"},
	}
	if runtime.GOOS != "windows" {
		tests = append(tests, struct {
			name string
			path string
		}{"unix-abs", "/tmp/evil.md"})
	} else {
		tests = append(tests, struct {
			name string
			path string
		}{"windows-abs", `C:\evil.md`})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.SaveRules(root, tt.path, "content")
			if err == nil {
				t.Errorf("expected error for %q, got nil", tt.path)
			}
		})
	}
}
