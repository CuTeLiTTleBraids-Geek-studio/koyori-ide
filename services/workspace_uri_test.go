package services

import (
	"errors"
	"testing"
)

func TestWorkspaceURICanonicalRoundTrip(t *testing.T) {
	u, err := NewWorkspaceURI("host-1", "workspace_1", "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.String(), "koyori-workspace://host-1/workspace_1/src/main.go"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	roundTrip, err := ParseWorkspaceURI(u.String())
	if err != nil || roundTrip.String() != u.String() || roundTrip.RelativePath() != "src/main.go" {
		t.Fatalf("round trip = %#v, %v", roundTrip, err)
	}
}

func TestWorkspaceURISpecialFilenameRoundTrip(t *testing.T) {
	paths := []string{
		"hello world.txt",
		"目录/文件（最终）.txt",
		"hash#percent%.txt",
		"name (copy).txt",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			u, err := NewWorkspaceURI("host", "ws", path)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseWorkspaceURI(u.String())
			if err != nil {
				t.Fatalf("ParseWorkspaceURI(%q): %v", u.String(), err)
			}
			if parsed.RelativePath() != path || parsed.String() != u.String() {
				t.Fatalf("round trip = %#v, want path %q", parsed, path)
			}
		})
	}
}

func TestWorkspaceURIRejectsEncodedSeparators(t *testing.T) {
	for _, raw := range []string{
		"koyori-workspace://host/ws/dir%2Ffile",
		"koyori-workspace://host/ws/dir%2ffile",
		"koyori-workspace://host/ws/dir%5Cfile",
		"koyori-workspace://host/ws/dir%5cfile",
	} {
		if _, err := ParseWorkspaceURI(raw); !errors.Is(err, ErrInvalidWorkspaceURI) {
			t.Errorf("ParseWorkspaceURI(%q) error = %v", raw, err)
		}
	}
}

func TestWorkspaceURIRejectsInvalidForms(t *testing.T) {
	bad := []string{
		"koyori-workspace://user:pass@host/ws", "koyori-workspace://host/ws?x=1", "koyori-workspace://host/ws#frag",
		"koyori-workspace://host/ws?", "koyori-workspace://host/ws#",
		"koyori-workspace://host/ws/./file", "koyori-workspace://host/ws/../file", "koyori-workspace://host/ws//file",
		"koyori-workspace://host/ws/", "koyori-workspace://host", "koyori-workspace://host/", "https://host/ws",
		"koyori-workspace://host//file", "koyori-workspace://host/ws/%2Ffile", "koyori-workspace://host/ws/%5C..%5Cother",
		"koyori-workspace://host/ws/%77s", "koyori-workspace://host/ws/%66ile", "koyori-workspace://host/ws/%7euser",
		"koyori-workspace://host/ws/..%5Cother",
	}
	for _, raw := range bad {
		if _, err := ParseWorkspaceURI(raw); !errors.Is(err, ErrInvalidWorkspaceURI) {
			t.Errorf("ParseWorkspaceURI(%q) error = %v", raw, err)
		}
	}
}

func TestWorkspaceURIRejectsInvalidIdentitiesAndLocalURI(t *testing.T) {
	if _, err := NewLocalWorkspaceURI("local-workspace", ""); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "bad/id", "bad:id", "bad\nvalue", "主机", ".", "..", "bad space"} {
		if _, err := NewWorkspaceURI(value, "ws", ""); err == nil {
			t.Errorf("host %q accepted", value)
		}
		if _, err := NewWorkspaceURI("host", value, ""); err == nil {
			t.Errorf("workspace %q accepted", value)
		}
	}
	for _, raw := range []string{
		"koyori-workspace://主机/ws",
		"koyori-workspace://host/工作区",
	} {
		if _, err := ParseWorkspaceURI(raw); err == nil {
			t.Errorf("Unicode URI %q accepted", raw)
		}
	}
	for _, path := range []string{`..\elsewhere`, `dir\..\elsewhere`, `\absolute`, `\\server\share`, `C:\absolute`, `C:relative`} {
		if _, err := NewWorkspaceURI("host", "ws", path); err == nil {
			t.Errorf("relative path %q accepted", path)
		}
	}
	for _, nonce := range []string{"", "nonce value", "nonce/value", `nonce\value`, "nonce:value", "nonce?value", "nonce#value", "非ascii", "nonce\nvalue"} {
		if _, err := NewWorkspaceRef("host", "ws", 1, nonce); err == nil {
			t.Errorf("nonce %q accepted", nonce)
		}
	}
}

func TestWorkspaceURIHostIDIsCaseSensitiveOpaqueIdentity(t *testing.T) {
	upper, err := NewWorkspaceURI("HOST", "ws", "")
	if err != nil {
		t.Fatal(err)
	}
	lower, err := NewWorkspaceURI("host", "ws", "")
	if err != nil {
		t.Fatal(err)
	}
	if upper.String() != "koyori-workspace://HOST/ws" {
		t.Fatalf("uppercase HostID was changed: %q", upper.String())
	}
	if lower.String() != "koyori-workspace://host/ws" {
		t.Fatalf("lowercase HostID was changed: %q", lower.String())
	}
	if upper.String() == lower.String() || upper.HostID == lower.HostID {
		t.Fatal("case-distinct HostIDs were conflated")
	}
	parsedUpper, err := ParseWorkspaceURI(upper.String())
	if err != nil {
		t.Fatal(err)
	}
	parsedLower, err := ParseWorkspaceURI(lower.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsedUpper.HostID != "HOST" || parsedLower.HostID != "host" {
		t.Fatalf("parsed HostIDs = %q and %q", parsedUpper.HostID, parsedLower.HostID)
	}
}

func TestWorkspaceScopeValidationAndMismatch(t *testing.T) {
	a, err := NewWorkspaceRef("host", "ws", 1, "nonce-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewWorkspaceRef("other-host", "ws", 1, "nonce-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.MatchesScope(a.Scope()); err != nil {
		t.Fatal(err)
	}
	if err := a.MatchesScope(b.Scope()); !errors.Is(err, ErrStaleWorkspaceScope) {
		t.Fatalf("mismatch error = %v", err)
	}
	if a.Scope().Equal(b.Scope()) {
		t.Fatal("different scopes compare equal")
	}
	if _, err := NewWorkspaceRef("host", "ws", 0, "nonce"); !errors.Is(err, ErrInvalidWorkspaceURI) {
		t.Fatalf("zero generation error = %v", err)
	}
	root, err := NewWorkspaceURI("host", "ws", "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	invalidRoot := WorkspaceRef{HostID: "host", WorkspaceID: "ws", Generation: 1, HostInstanceNonce: "nonce", URI: root}
	if err := invalidRoot.Validate(); !errors.Is(err, ErrInvalidWorkspaceURI) {
		t.Fatalf("non-root ref error = %v", err)
	}
	invalidFields := WorkspaceScope{HostID: "other", WorkspaceID: "ws", Generation: 1, HostInstanceNonce: "nonce", URI: a.URI}
	if err := invalidFields.Validate(); !errors.Is(err, ErrStaleWorkspaceScope) {
		t.Fatalf("mismatched scope fields error = %v", err)
	}
}
