package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestG03WindowsWorkspaceCaseVariantKeepsGeneration(t *testing.T) {
	root := t.TempDir()
	ctx := NewWorkspaceContext()
	if err := ctx.Set(root); err != nil {
		t.Fatalf("set workspace root: %v", err)
	}
	generation := ctx.Generation()
	if err := ctx.Set(strings.ToUpper(root)); err != nil {
		t.Fatalf("set case-variant workspace root: %v", err)
	}
	if ctx.Generation() != generation {
		t.Fatalf("case-only workspace spelling changed generation: %d -> %d", generation, ctx.Generation())
	}
}

func TestG03WindowsWorkspaceJunctionUsesTargetIdentity(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	junction := filepath.Join(base, "junction")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create junction target: %v", err)
	}
	output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction: %v\n%s", err, output)
	}
	ctx := NewWorkspaceContext()
	if err := ctx.Set(target); err != nil {
		t.Fatalf("set junction target: %v", err)
	}
	generation := ctx.Generation()
	if err := ctx.Set(junction); err != nil {
		t.Fatalf("set junction path: %v", err)
	}
	if ctx.Generation() != generation {
		t.Fatalf("junction alias changed generation: %d -> %d", generation, ctx.Generation())
	}
	roots, err := canonicalizeExistingWorkspaceRoots([]string{target, junction})
	if err != nil {
		t.Fatalf("canonicalize target and junction roots: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("target and junction were not deduplicated: %#v", roots)
	}
}

func TestG03WindowsUNCIdentityBoundaries(t *testing.T) {
	regularUNC := `\\server\share\Workspace`
	extendedUNC := `\\?\UNC\SERVER\SHARE\workspace`
	if normalizeWindowsWorkspaceIdentityPath(regularUNC) != normalizeWindowsWorkspaceIdentityPath(extendedUNC) {
		t.Fatalf("equivalent UNC spellings did not normalize together: %q != %q",
			normalizeWindowsWorkspaceIdentityPath(regularUNC),
			normalizeWindowsWorkspaceIdentityPath(extendedUNC))
	}
	if normalizeWindowsWorkspaceIdentityPath(`\\server\share-a\workspace`) ==
		normalizeWindowsWorkspaceIdentityPath(`\\server\share-b\workspace`) {
		t.Fatal("different UNC shares collapsed to one workspace identity")
	}
	if normalizeWindowsWorkspaceIdentityPath(`C:\workspace`) ==
		normalizeWindowsWorkspaceIdentityPath(regularUNC) {
		t.Fatal("local drive and UNC workspace collapsed to one identity")
	}
	if IsRelativePathSafe(`\\server\share\workspace\file.txt`) {
		t.Fatal("UNC path was accepted as a renderer relative path")
	}
}
