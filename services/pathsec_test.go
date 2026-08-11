package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestIsRelativePathSafe_RejectsTraversal covers N-91/N-92 lexical name
// validation. These are the cases that ConversationService.pathFor and
// PresetService.writePresetFile rely on to reject malicious ids/names.
func TestIsRelativePathSafe_RejectsTraversal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"simple", "abc", true},
		{"with-dash", "20260105-120000-deadbeef", true},
		{"with-underscore", "my_preset", true},
		{"dot-only", ".", false},                // cleaned to "."
		{"dotdot", "..", false},                 // exact parent
		{"dotdot-slash", "../evil", false},      // parent traversal
		{"dotdot-backslash", "..\\evil", false}, // windows parent traversal
		{"nested-dotdot", "a/../../etc", false}, // nested traversal
		{"unix-abs", "/etc/passwd", false},      // unix absolute
		{"backslash-abs", "\\etc\\passwd", false},
		{"subdir-slash", "sub/file", true},      // SafeNameJoin rejects, but lexical is "safe"
		{"subdir-backslash", "sub\\file", true}, // SafeNameJoin rejects, but lexical is "safe"
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRelativePathSafe(tt.in); got != tt.want {
				t.Errorf("IsRelativePathSafe(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestIsRelativePathSafe_WindowsDrivePaths — only run on Windows where
// filepath.IsAbs recognizes "C:\..." and "C:foo" forms.
func TestIsRelativePathSafe_WindowsDrivePaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive letter tests are Windows-specific")
	}
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"drive-abs", "C:\\evil", false},
		{"drive-forward", "C:/evil", false},
		{"drive-rel-no-sep", "C:evil", false}, // volume-relative form
		{"unc", "\\\\server\\share", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRelativePathSafe(tt.in); got != tt.want {
				t.Errorf("IsRelativePathSafe(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestSafeNameJoin_RejectsSeparators — even though IsRelativePathSafe allows
// "sub/file" lexically, SafeNameJoin must reject it because id-based services
// expect a flat directory.
func TestSafeNameJoin_RejectsSeparators(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		in   string
	}{
		{"subdir-slash", "sub/file"},
		{"subdir-backslash", "sub\\file"},
		{"dotdot", ".."},
		{"dotdot-slash", "../evil"},
		{"unix-abs", "/etc/passwd"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SafeNameJoin(dir, tt.in, ".json")
			if err == nil {
				t.Errorf("SafeNameJoin(%q) should return error, got nil", tt.in)
			}
		})
	}
}

// TestSafeNameJoin_BuildsValidPath — verify a normal name produces the
// expected path with the extension appended.
func TestSafeNameJoin_BuildsValidPath(t *testing.T) {
	dir := t.TempDir()
	got, err := SafeNameJoin(dir, "conv-1", ".json")
	if err != nil {
		t.Fatalf("SafeNameJoin failed: %v", err)
	}
	want := filepath.Join(dir, "conv-1.json")
	if got != want {
		t.Errorf("SafeNameJoin = %q, want %q", got, want)
	}
}

// TestSafeNameJoin_EmptyExt — verify empty extension works (no trailing dot).
func TestSafeNameJoin_EmptyExt(t *testing.T) {
	dir := t.TempDir()
	got, err := SafeNameJoin(dir, "name", "")
	if err != nil {
		t.Fatalf("SafeNameJoin failed: %v", err)
	}
	want := filepath.Join(dir, "name")
	if got != want {
		t.Errorf("SafeNameJoin empty ext = %q, want %q", got, want)
	}
}

// TestValidateNameForFlatDir_MirrorSafeNameJoin — verify the lighter-weight
// validator agrees with SafeNameJoin on reject cases.
func TestValidateNameForFlatDir_MirrorSafeNameJoin(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"valid", "abc", false},
		{"valid-dash", "a-b-c", false},
		{"empty", "", true},
		{"dotdot", "..", true},
		{"traversal", "../x", true},
		{"separator-slash", "a/b", true},
		{"separator-backslash", "a\\b", true},
		{"unix-abs", "/x", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNameForFlatDir(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateNameForFlatDir(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
			}
		})
	}
}

// TestValidatePathWithinRoot_AllowsTargetInsideRoot — happy path: a file
// inside the root is allowed and the resolved absolute path is returned.
func TestValidatePathWithinRoot_AllowsTargetInsideRoot(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "sub")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(subdir, "file.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := ValidatePathWithinRoot(root, target)
	if err != nil {
		t.Fatalf("ValidatePathWithinRoot failed: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("expected absolute path, got %q", got)
	}
}

// TestValidatePathWithinRoot_RejectsTraversalOutsideRoot — a target that
// resolves outside the root must be rejected.
func TestValidatePathWithinRoot_RejectsTraversalOutsideRoot(t *testing.T) {
	root := t.TempDir()
	// Construct a path that escapes via "..".
	escaping := filepath.Join(root, "..", "evil.json")
	_, err := ValidatePathWithinRoot(root, escaping)
	if err == nil {
		t.Error("expected error for path escaping root, got nil")
	}
}

// TestValidatePathWithinRoot_AllowsNonExistentTargetInsideRoot — a target
// that doesn't yet exist but whose parent is inside the root must be allowed
// (this is the create-file use case).
func TestValidatePathWithinRoot_AllowsNonExistentTargetInsideRoot(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "newfile.txt")
	got, err := ValidatePathWithinRoot(root, target)
	if err != nil {
		t.Fatalf("ValidatePathWithinRoot failed: %v", err)
	}
	if !strings.HasSuffix(got, "newfile.txt") {
		t.Errorf("expected path ending with newfile.txt, got %q", got)
	}
}

// TestValidatePathWithinRoot_EmptyRootAllowsAll — when root is empty,
// no sandboxing is applied (legacy mode).
func TestValidatePathWithinRoot_EmptyRootAllowsAll(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "anywhere.txt")
	got, err := ValidatePathWithinRoot("", target)
	if err != nil {
		t.Fatalf("ValidatePathWithinRoot with empty root failed: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("expected absolute path, got %q", got)
	}
}

// TestValidatePathWithinRoot_RejectsSymlinkEscape — a symlink inside the
// root pointing outside must be rejected. This is the N-56 regression test
// that motivated symlink-aware validation.
func TestValidatePathWithinRoot_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	// Create a symlink inside root pointing to the outside temp dir.
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	target := filepath.Join(link, "evil.txt")
	_, err := ValidatePathWithinRoot(root, target)
	if err == nil {
		t.Error("expected error for symlink escaping root, got nil")
	}
}

func TestValidatePathWithinRoot_RejectsMissingPathBeyondSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	target := filepath.Join(link, "missing", "file.txt")
	if _, err := ValidatePathWithinRoot(root, target); err == nil {
		t.Fatal("expected error for missing path beyond symlink escape")
	}
}

// TestIsPathOutsideRoot_BooleanForm — verify the boolean helper agrees with
// ValidatePathWithinRoot on inside/outside verdicts.
func TestIsPathOutsideRoot_BooleanForm(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	outside := filepath.Join(root, "..", "outside.txt")
	if IsPathOutsideRoot(root, inside) {
		t.Error("inside path should report false (not outside)")
	}
	if !IsPathOutsideRoot(root, outside) {
		t.Error("outside path should report true")
	}
	// Empty root means "no restriction".
	if IsPathOutsideRoot("", outside) {
		t.Error("empty root should report false (no restriction)")
	}
}

// Note: evalSymlinksAllowMissing behavior is covered by existing tests in
// file_service_test.go (TestEvalSymlinksAllowMissing_ExistingPath,
// TestEvalSymlinksAllowMissing_NonExistentFileWithExistentParent,
// TestEvalSymlinksAllowMissing_NonExistentParent). The function was moved
// from file_service.go to pathsec.go but the tests remain in the original
// file — see pathsec.go for the implementation.
