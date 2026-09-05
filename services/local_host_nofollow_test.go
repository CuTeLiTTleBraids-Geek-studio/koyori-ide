//go:build js || plan9

package services

import (
	"errors"
	"testing"
)

func TestLocalWorkspaceHostNoFollowUnavailableFailsClosed(t *testing.T) {
	host, _, scope := newTestLocalWorkspaceHost(t)
	defer host.Close()
	uri := localTestURI(t, "file.txt")
	rootURI, err := host.RootURI()
	if err != nil {
		t.Fatalf("RootURI: %v", err)
	}
	if rootURI.String() != localTestURI(t, "").String() {
		t.Fatalf("RootURI = %q", rootURI.String())
	}
	resolved, err := host.Resolve(uri, scope)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.String() != uri.String() {
		t.Fatalf("Resolve = %q, want %q", resolved.String(), uri.String())
	}
	if _, err := host.ReadFile(uri, scope); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ReadFile error = %v, want ErrNotAllowed", err)
	}
	if err := host.WriteFile(uri, scope, []byte("denied")); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("WriteFile error = %v, want ErrNotAllowed", err)
	}
	if _, err := host.ListDirectory(localTestURI(t, ""), scope); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ListDirectory error = %v, want ErrNotAllowed", err)
	}
	if _, err := host.Stat(uri, scope); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("Stat error = %v, want ErrNotAllowed", err)
	}
}
