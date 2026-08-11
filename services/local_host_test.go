package services

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestLocalWorkspaceHost(t *testing.T) (*LocalWorkspaceHost, *WorkspaceContext, WorkspaceScope) {
	t.Helper()
	ctx := NewWorkspaceContext()
	if err := ctx.Set(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	host, err := NewLocalWorkspaceHost(ctx, "test-workspace", "test-instance")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := host.WorkspaceRef()
	if err != nil {
		t.Fatal(err)
	}
	return host, ctx, ref.Scope()
}

func localTestURI(t *testing.T, relative string) WorkspaceURI {
	t.Helper()
	uri, err := NewLocalWorkspaceURI("test-workspace", relative)
	if err != nil {
		t.Fatal(err)
	}
	return uri
}

func TestLocalWorkspaceHostUnopenedFailsClosed(t *testing.T) {
	host, err := NewLocalWorkspaceHost(NewWorkspaceContext(), "test-workspace", "test-instance")
	if err != nil {
		t.Fatal(err)
	}
	uri := localTestURI(t, "file.txt")
	ref, err := NewLocalWorkspaceRef("test-workspace", 1, "test-instance")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.RootURI(); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("RootURI error = %v", err)
	}
	if _, err := host.ReadFile(uri, ref.Scope()); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ReadFile error = %v", err)
	}
	if err := host.WriteFile(uri, ref.Scope(), []byte("denied")); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("WriteFile error = %v", err)
	}
}

func TestLocalWorkspaceHostReadWriteListStat(t *testing.T) {
	host, ctx, scope := newTestLocalWorkspaceHost(t)
	fileURI := localTestURI(t, "src/main.txt")
	if err := host.WriteFile(fileURI, scope, []byte("hello")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := host.ReadFile(fileURI, scope)
	if err != nil || string(data) != "hello" {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}
	stat, err := host.Stat(fileURI, scope)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.URI.String() != fileURI.String() || stat.IsDir || stat.Size != 5 {
		t.Fatalf("Stat = %#v", stat)
	}
	entries, err := host.ListDirectory(localTestURI(t, "src"), scope)
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "main.txt" || entries[0].URI.String() != fileURI.String() {
		t.Fatalf("entries = %#v", entries)
	}
	if strings.Contains(entries[0].URI.String(), filepath.ToSlash(ctx.Root())) || filepath.IsAbs(entries[0].URI.RelativePath()) {
		t.Fatalf("child URI leaked absolute path: %q", entries[0].URI.String())
	}
	resolved, err := host.Resolve(fileURI, scope)
	if err != nil || resolved.String() != fileURI.String() {
		t.Fatalf("Resolve = %q, %v", resolved.String(), err)
	}
}

func TestLocalWorkspaceHostListsSpecialFilenamesWithoutPathLeak(t *testing.T) {
	host, ctx, scope := newTestLocalWorkspaceHost(t)
	for _, name := range []string{"hello world.txt", "中文#百分号%.txt", "name (copy).txt"} {
		if err := host.WriteFile(localTestURI(t, name), scope, []byte(name)); err != nil {
			t.Fatalf("WriteFile(%q): %v", name, err)
		}
	}
	entries, err := host.ListDirectory(localTestURI(t, ""), scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %#v", entries)
	}
	for _, entry := range entries {
		if strings.Contains(entry.URI.String(), filepath.ToSlash(ctx.Root())) || filepath.IsAbs(entry.URI.RelativePath()) {
			t.Fatalf("child URI leaked absolute path: %q", entry.URI.String())
		}
		parsed, err := ParseWorkspaceURI(entry.URI.String())
		if err != nil || parsed.RelativePath() != entry.Name {
			t.Fatalf("child URI %q round trip = %#v, %v", entry.URI.String(), parsed, err)
		}
	}
}

func TestLocalWorkspaceHostErrorsDoNotLeakRoot(t *testing.T) {
	host, ctx, scope := newTestLocalWorkspaceHost(t)
	missing := localTestURI(t, "missing/file.txt")
	checks := []struct {
		name string
		err  error
		want error
	}{
		{name: "read", err: func() error { _, err := host.ReadFile(missing, scope); return err }(), want: ErrNotFound},
		{name: "list", err: func() error { _, err := host.ListDirectory(missing, scope); return err }(), want: ErrNotFound},
		{name: "stat", err: func() error { _, err := host.Stat(missing, scope); return err }(), want: ErrNotFound},
	}
	for _, check := range checks {
		if !errors.Is(check.err, check.want) {
			t.Errorf("%s error = %v, want %v", check.name, check.err, check.want)
		}
		if strings.Contains(check.err.Error(), ctx.Root()) {
			t.Errorf("%s error leaked root: %q", check.name, check.err)
		}
	}

	if runtime.GOOS != "windows" {
		blocked := filepath.Join(ctx.Root(), "blocked")
		if err := os.Mkdir(blocked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
		_, err := host.ListDirectory(localTestURI(t, "blocked"), scope)
		if os.Geteuid() != 0 {
			if !errors.Is(err, os.ErrPermission) {
				t.Fatalf("permission error = %v", err)
			}
			if strings.Contains(err.Error(), ctx.Root()) {
				t.Fatalf("permission error leaked root: %q", err)
			}
		}
	}
}

func TestLocalWorkspaceHostRejectsRemoteAndStaleIdentity(t *testing.T) {
	host, ctx, scope := newTestLocalWorkspaceHost(t)
	local := localTestURI(t, "file.txt")
	remote, err := NewWorkspaceURI("remote", "test-workspace", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Resolve(remote, scope); !errors.Is(err, ErrUnsupportedWorkspace) {
		t.Fatalf("remote URI error = %v", err)
	}

	tests := []struct {
		name  string
		scope WorkspaceScope
	}{
		{name: "generation", scope: func() WorkspaceScope { s := scope; s.Generation++; return s }()},
		{name: "nonce", scope: func() WorkspaceScope { s := scope; s.HostInstanceNonce = "other"; return s }()},
	}
	otherRef, err := NewLocalWorkspaceRef("other-workspace", scope.Generation, scope.HostInstanceNonce)
	if err != nil {
		t.Fatal(err)
	}
	tests = append(tests, struct {
		name  string
		scope WorkspaceScope
	}{name: "workspace", scope: otherRef.Scope()})
	remoteRef, err := NewWorkspaceRef("remote", "test-workspace", scope.Generation, scope.HostInstanceNonce)
	if err != nil {
		t.Fatal(err)
	}
	tests = append(tests, struct {
		name  string
		scope WorkspaceScope
	}{name: "host", scope: remoteRef.Scope()})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := host.Resolve(local, test.scope); err == nil {
				t.Fatal("mismatched scope accepted")
			}
		})
	}

	oldScope := scope
	ctx.Clear()
	if err := ctx.Set(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Resolve(local, oldScope); !errors.Is(err, ErrStaleWorkspaceScope) {
		t.Fatalf("old generation error = %v", err)
	}
}

func TestLocalWorkspaceHostRejectsEscapeDotAbsoluteAndSymlink(t *testing.T) {
	host, ctx, scope := newTestLocalWorkspaceHost(t)
	forged := []WorkspaceURI{
		{HostID: LocalHostID, WorkspaceID: "test-workspace", relative: "../escape"},
		{HostID: LocalHostID, WorkspaceID: "test-workspace", relative: "."},
		{HostID: LocalHostID, WorkspaceID: "test-workspace", relative: "/absolute"},
	}
	if runtime.GOOS == "windows" {
		forged = append(forged, WorkspaceURI{HostID: LocalHostID, WorkspaceID: "test-workspace", relative: `C:\absolute`})
	}
	for _, uri := range forged {
		if _, err := host.Resolve(uri, scope); err == nil {
			t.Errorf("forged path %q accepted", uri.RelativePath())
		}
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ctx.Root(), "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable on Windows: %v", err)
		}
		t.Fatal(err)
	}
	uri := localTestURI(t, "outside-link/secret.txt")
	if _, err := host.ReadFile(uri, scope); err == nil {
		t.Fatal("symlink escape accepted for read")
	}
	if err := host.WriteFile(uri, scope, []byte("changed")); err == nil {
		t.Fatal("symlink escape accepted for write")
	}
}
