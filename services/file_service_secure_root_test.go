package services

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestSplitRootPathPreservesUnicodeWithSeparatorLowByte(t *testing.T) {
	path := filepath.Join("first", "slash-low-\u012f", "backslash-low-\u015c", "last")
	want := []string{"first", "slash-low-\u012f", "backslash-low-\u015c", "last"}
	if got := splitRootPath(path); !reflect.DeepEqual(got, want) {
		t.Fatalf("splitRootPath(%q) = %#v, want %#v", path, got, want)
	}
}

func TestSecureWorkspaceClosesRootHandlesWhileIdle(t *testing.T) {
	service := newFileServiceAt(t, t.TempDir())
	workspace := service.secureWorkspace
	if workspace.roots[0].root != nil {
		t.Fatal("new workspace retained an idle root handle")
	}

	capability, err := service.acquireCapability(service.WorkspaceRoots()[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.roots[0].root == nil {
		t.Fatal("active capability did not open the root handle")
	}
	capability.releaseCapability()
	if workspace.roots[0].root != nil {
		t.Fatal("last capability release retained the idle root handle")
	}
}
