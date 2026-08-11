package services

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

type marketplaceWalkTestDirEntry struct {
	name      string
	mode      fs.FileMode
	infoCalls int
}

func (d *marketplaceWalkTestDirEntry) Name() string      { return d.name }
func (d *marketplaceWalkTestDirEntry) IsDir() bool       { return d.mode.IsDir() }
func (d *marketplaceWalkTestDirEntry) Type() fs.FileMode { return d.mode.Type() }
func (d *marketplaceWalkTestDirEntry) Info() (fs.FileInfo, error) {
	d.infoCalls++
	return nil, nil
}

// marketplace_service_test.go — G-VSC-01 tests.
//
// These tests exercise the security gates and lifecycle of the marketplace
// service without hitting the network. They build mock VSIX (zip) files in
// memory and drive installFromVSIXData directly, covering:
//   - VSIX path traversal protection (G-SEC-12: malicious "../../" entries)
//   - SHA-256 verification (G-SEC-12 req. 3: mismatched hash rejected)
//   - Default-disabled on install (G-SEC-12 req. 2 / G-VSC-03 req. 2)
//   - ListInstalledExtensions
//   - UninstallExtension

// newTestMarketplaceService returns a MarketplaceService rooted at a temp
// config dir. The temp dir is cleaned up automatically by testing.T.
func newTestMarketplaceService(t *testing.T) (*MarketplaceService, string) {
	t.Helper()
	dir := t.TempDir()
	return NewMarketplaceService(dir), dir
}

// zipEntry is a single file to write into a mock VSIX.
type zipEntry struct {
	Name string
	Data []byte
	// Mode is the zip entry's file mode (used to simulate symlinks). Zero
	// means a regular file; directories are inferred from a trailing "/".
	Mode uint32
}

// buildVSIX builds a VSIX (zip) in memory from the given entries. Returns
// the raw bytes and the hex-encoded SHA-256 of those bytes.
func buildVSIX(t *testing.T, entries []zipEntry) ([]byte, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	w := zip.NewWriter(buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{
			Name:   e.Name,
			Method: zip.Deflate,
		}
		if e.Mode != 0 {
			hdr.SetMode(os.FileMode(e.Mode))
		}
		f, err := w.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", e.Name, err)
		}
		if _, err := f.Write(e.Data); err != nil {
			t.Fatalf("write zip entry %q: %v", e.Name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

// validPackageJSON is a minimal VS Code extension package.json payload used
// by the well-formed VSIX fixtures.
const validPackageJSON = `{
  "name": "hello",
  "publisher": "acme",
  "version": "1.0.0",
  "displayName": "Hello",
  "description": "A test extension",
  "engines": { "vscode": "^1.80.0" },
  "activationEvents": ["onStartupFinished"],
  "main": "./dist/main.js",
  "browser": "./dist/browser.js",
  "koyoriIde": { "permissions": [] },
  "contributes": { "commands": [{ "command": "acme.hello", "title": "Hello" }] },
  "capabilities": { "untrustedWorkspaces": { "supported": true } }
}`

// buildValidVSIX builds a well-formed VSIX with extension/package.json and a
// dummy runtime file. Returns the bytes and their SHA-256.
func buildValidVSIX(t *testing.T) ([]byte, string) {
	t.Helper()
	return buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(validPackageJSON)},
		{Name: "extension/dist/main.js", Data: []byte("module.exports = { activate() {} };\n")},
		{Name: "extension/dist/browser.js", Data: []byte("module.exports = { activate() {} };\n")},
		{Name: "[Content_Types].xml", Data: []byte("<Types/>")},
	})
}

func buildUpdateVSIX(t *testing.T, version string, activationEvents []string, marker string) ([]byte, string) {
	t.Helper()
	events, err := json.Marshal(activationEvents)
	if err != nil {
		t.Fatalf("marshal activation events: %v", err)
	}
	pkgJSON := fmt.Sprintf(`{
  "name": "hello",
  "publisher": "acme",
  "version": %q,
  "activationEvents": %s
}`, version, events)
	return buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(pkgJSON)},
		{Name: "extension/marker.txt", Data: []byte(marker)},
	})
}

func configureUpdateRegistry(t *testing.T, svc *MarketplaceService, version string, vsix []byte, hash string) {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/acme/hello":
			_ = json.NewEncoder(w).Encode(ovsxExtension{
				Name:      "hello",
				Namespace: "acme",
				Version:   version,
				Files: ovsxFileMap{
					"download": server.URL + "/download",
					"sha256":   hash,
				},
			})
		case "/download":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(vsix)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	svc.mu.Lock()
	svc.registryURL = server.URL + "/api"
	svc.mu.Unlock()
}

func successfulLifecycleResult(request ExtensionLifecycleRequest, wasActive bool) ExtensionLifecycleResult {
	return ExtensionLifecycleResult{
		RequestID: request.RequestID, ExtensionID: request.ExtensionID,
		Publisher: request.Publisher, Name: request.Name,
		Action: request.Action, OK: true, WasActive: wasActive,
	}
}

func TestMarketplaceLifecycleResultAcceptsOnlyExactPendingIdentityOnce(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	request := ExtensionLifecycleRequest{
		RequestID: "request-1", ExtensionID: "acme.hello",
		Publisher: "acme", Name: "hello", Action: "stop",
	}
	resultCh := make(chan ExtensionLifecycleResult, 1)
	svc.lifecyclePending[request.RequestID] = lifecyclePendingRequest{
		request: request,
		result:  resultCh,
		expires: time.Now().Add(time.Minute),
	}
	valid := successfulLifecycleResult(request, true)
	for _, test := range []struct {
		name   string
		mutate func(*ExtensionLifecycleResult)
	}{
		{name: "unknown request", mutate: func(result *ExtensionLifecycleResult) { result.RequestID = "unknown" }},
		{name: "extension ID", mutate: func(result *ExtensionLifecycleResult) { result.ExtensionID = "acme.other" }},
		{name: "publisher", mutate: func(result *ExtensionLifecycleResult) { result.Publisher = "other" }},
		{name: "name", mutate: func(result *ExtensionLifecycleResult) { result.Name = "other" }},
		{name: "action", mutate: func(result *ExtensionLifecycleResult) { result.Action = "commit" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged := valid
			test.mutate(&forged)
			if svc.acceptExtensionLifecycleResult(forged) {
				t.Fatal("forged lifecycle result was accepted")
			}
			select {
			case got := <-resultCh:
				t.Fatalf("forged lifecycle result reached waiter: %+v", got)
			default:
			}
		})
	}
	if !svc.acceptExtensionLifecycleResult(valid) {
		t.Fatal("valid pending lifecycle result was rejected")
	}
	if got := <-resultCh; got != valid {
		t.Fatalf("accepted lifecycle result = %+v, want %+v", got, valid)
	}
	if svc.acceptExtensionLifecycleResult(valid) {
		t.Fatal("duplicate lifecycle result was accepted")
	}
}

func TestMarketplaceLifecycleResultRejectsExpiredPendingRequest(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	request := ExtensionLifecycleRequest{
		RequestID: "expired-request", ExtensionID: "acme.hello",
		Publisher: "acme", Name: "hello", Action: "stop",
	}
	resultCh := make(chan ExtensionLifecycleResult, 1)
	svc.lifecyclePending[request.RequestID] = lifecyclePendingRequest{
		request: request,
		result:  resultCh,
		expires: time.Now().Add(-time.Second),
	}
	if svc.acceptExtensionLifecycleResult(successfulLifecycleResult(request, true)) {
		t.Fatal("expired lifecycle result was accepted")
	}
	if _, pending := svc.lifecyclePending[request.RequestID]; pending {
		t.Fatal("expired lifecycle request was retained")
	}
	select {
	case result := <-resultCh:
		t.Fatalf("expired lifecycle result reached waiter: %+v", result)
	default:
	}
}

func TestMarketplaceLifecycleRequesterValidatesRequestAndResult(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	request := ExtensionLifecycleRequest{
		RequestID: "request-1", ExtensionID: "acme.hello",
		Publisher: "acme", Name: "hello", Action: "stop",
	}
	called := false
	svc.setExtensionLifecycleRequester(func(_ context.Context, request ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
		called = true
		result := successfulLifecycleResult(request, false)
		result.Action = "commit"
		return result, nil
	})
	if _, err := svc.requestExtensionLifecycle(context.Background(), request); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("forged requester result error = %v, want identity mismatch", err)
	}
	if !called {
		t.Fatal("lifecycle requester was not called")
	}

	called = false
	request.Action = "delete"
	if _, err := svc.requestExtensionLifecycle(context.Background(), request); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported lifecycle request error = %v", err)
	}
	if called {
		t.Fatal("invalid lifecycle request reached requester")
	}
}

func TestMarketplaceLifecycleRequesterHonorsContextDeadline(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	svc.setExtensionLifecycleRequester(func(ctx context.Context, _ ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
		<-ctx.Done()
		return ExtensionLifecycleResult{}, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	request := ExtensionLifecycleRequest{
		RequestID: "request-1", ExtensionID: "acme.hello",
		Publisher: "acme", Name: "hello", Action: "stop",
	}
	if _, err := svc.requestExtensionLifecycle(ctx, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lifecycle timeout error = %v, want context deadline exceeded", err)
	}
}

func TestMarketplaceLifecycleDefaultsToSuccessfulNoopWithoutRequester(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	request := ExtensionLifecycleRequest{
		RequestID: "request-1", ExtensionID: "acme.hello",
		Publisher: "acme", Name: "hello", Action: "invalidate", WasActive: true,
	}
	result, err := svc.requestExtensionLifecycle(context.Background(), request)
	if err != nil {
		t.Fatalf("default lifecycle request: %v", err)
	}
	if result != successfulLifecycleResult(request, true) {
		t.Fatalf("default lifecycle result = %+v", result)
	}
}

// --- SHA-256 verification (G-SEC-12 req. 3) ---

// TestMarketplaceInstall_Sha256MismatchRejected verifies that a VSIX whose
// computed SHA-256 does not match the registry-provided hash is rejected
// before any file is extracted.
func TestMarketplaceInstall_Sha256MismatchRejected(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	vsix, _ := buildValidVSIX(t)
	// Deliberately wrong hash.
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	err := svc.installFromVSIXData(vsix, wrongHash, "acme", "hello", "1.0.0")
	if err == nil {
		t.Fatal("expected SHA-256 mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "SHA-256 verification failed") {
		t.Fatalf("expected SHA-256 verification error, got: %v", err)
	}
	// No extension directory should have been created on rejection.
	dir := filepath.Join(svc.configDir, extensionsSubdir, "acme.hello")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("extension dir should not exist after a rejected install; stat err=%v", err)
	}
}

// TestMarketplaceInstall_Sha256MatchAccepted verifies that a matching hash
// allows the install to proceed.
func TestMarketplaceInstall_Sha256MatchAccepted(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	var lifecycle []ExtensionLifecycleRequest
	svc.setExtensionLifecycleRequester(func(_ context.Context, request ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
		lifecycle = append(lifecycle, request)
		return successfulLifecycleResult(request, false), nil
	})
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install with matching hash failed: %v", err)
	}
	// The extension directory should now exist with the extracted payload.
	manifestPath := filepath.Join(svc.configDir, extensionsSubdir, "acme.hello", "extension", "package.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("extracted package.json should exist: %v", err)
	}
	if len(lifecycle) != 2 || lifecycle[0].Action != "stop" || lifecycle[1].Action != "commit" {
		t.Fatalf("install lifecycle = %+v, want stop then commit", lifecycle)
	}
}

func TestMarketplaceInstall_FailureAfterStopInvalidatesLifecycle(t *testing.T) {
	svc, configDir := newTestMarketplaceService(t)
	var lifecycle []ExtensionLifecycleRequest
	svc.setExtensionLifecycleRequester(func(_ context.Context, request ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
		lifecycle = append(lifecycle, request)
		return successfulLifecycleResult(request, false), nil
	})
	// Block only the state file so installation fails after the Worker hold is acquired.
	blockedStatePath := filepath.Join(configDir, "koyori-ide", extensionsStateFileName)
	if err := os.MkdirAll(blockedStatePath, 0o755); err != nil {
		t.Fatalf("block state file: %v", err)
	}
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err == nil {
		t.Fatal("expected install state persistence failure")
	}
	if len(lifecycle) != 2 || lifecycle[0].Action != "stop" || lifecycle[1].Action != "invalidate" {
		t.Fatalf("failed install lifecycle = %+v, want stop then invalidate", lifecycle)
	}
}

// TestMarketplaceResolveSha256_FromUrl fetches the hash from a .sha256 URL.
// The Open VSX registry returns "sha256" as a URL to a .sha256 file, not the
// raw hash. This test verifies the URL is fetched and the hash extracted.
func TestMarketplaceResolveSha256_FromUrl(t *testing.T) {
	wantHash := "232aeafb01f069824fdd92d3e628c1c442bbcfa1d3cc945ff97076340bb2b4a6"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The .sha256 file format: "<hash>  <filename>\n"
		fmt.Fprintf(w, "%s  ms-python.python-2026.4.0.vsix\n", wantHash)
	}))
	defer server.Close()

	svc, _ := newTestMarketplaceService(t)
	got, err := svc.resolveSha256(server.URL + "/ms-python.python-2026.4.0.sha256")
	if err != nil {
		t.Fatalf("resolveSha256 failed: %v", err)
	}
	if got != wantHash {
		t.Fatalf("expected %s, got %s", wantHash, got)
	}
}

// TestMarketplaceResolveSha256_RawHash passes a raw hex hash (non-URL) and
// verifies it is returned as-is (backward compat for older API responses).
func TestMarketplaceResolveSha256_RawHash(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	raw := "abc123def4567890abc123def4567890abc123def4567890abc123def4567890"
	got, err := svc.resolveSha256(raw)
	if err != nil {
		t.Fatalf("resolveSha256 failed: %v", err)
	}
	if got != raw {
		t.Fatalf("expected %s, got %s", raw, got)
	}
}

// TestMarketplaceResolveSha256_Empty returns "" for empty input.
func TestMarketplaceResolveSha256_Empty(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	got, err := svc.resolveSha256("")
	if err != nil {
		t.Fatalf("resolveSha256 failed: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string, got %s", got)
	}
}

// --- Path traversal protection (G-SEC-12) ---

// TestMarketplaceInstall_PathTraversalRejected verifies that a malicious VSIX
// containing entries with "../" traversal is rejected and nothing is written
// outside the install directory.
func TestMarketplaceInstall_PathTraversalRejected(t *testing.T) {
	svc, configDir := newTestMarketplaceService(t)
	// Build a VSIX whose entry escapes the extension directory.
	malicious, wantHash := buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(validPackageJSON)},
		{Name: "../../evil.txt", Data: []byte("pwned")},
	})
	err := svc.installFromVSIXData(malicious, wantHash, "acme", "hello", "1.0.0")
	if err == nil {
		t.Fatal("expected path traversal rejection, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "traversal") && !strings.Contains(strings.ToLower(err.Error()), "escapes") && !strings.Contains(strings.ToLower(err.Error()), "outside") {
		t.Fatalf("expected traversal-related error, got: %v", err)
	}
	// The malicious payload must NOT have escaped into the config dir's parent.
	evilPath := filepath.Join(configDir, "evil.txt")
	if _, err := os.Stat(evilPath); !os.IsNotExist(err) {
		t.Fatalf("traversal payload leaked to %s; stat err=%v", evilPath, err)
	}
	// And not in the parent of configDir either.
	parentEvil := filepath.Join(filepath.Dir(configDir), "evil.txt")
	if _, err := os.Stat(parentEvil); !os.IsNotExist(err) {
		t.Fatalf("traversal payload leaked to parent %s; stat err=%v", parentEvil, err)
	}
	// The install directory should have been cleaned up (no half-installed ext).
	dir := filepath.Join(svc.configDir, extensionsSubdir, "acme.hello")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("extension dir should not exist after a rejected install; stat err=%v", err)
	}
}

// TestMarketplaceInstall_AbsolutePathEntryRejected verifies that an absolute
// entry path is rejected (another traversal vector).
func TestMarketplaceInstall_AbsolutePathEntryRejected(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	// Use a Unix-style absolute path entry. (Drive-letter absolute forms are
	// platform-specific; the leading-slash form is the cross-platform guard.)
	abs, wantHash := buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(validPackageJSON)},
		{Name: "/etc/evil.txt", Data: []byte("pwned")},
	})
	err := svc.installFromVSIXData(abs, wantHash, "acme", "hello", "1.0.0")
	if err == nil {
		t.Fatal("expected absolute-path rejection, got nil")
	}
}

// TestMarketplaceInstall_SymlinkEntryRejected verifies that a symlink zip
// entry is rejected (symlinks could point outside the install dir).
func TestMarketplaceInstall_SymlinkEntryRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("symlink test skipped in short mode")
	}
	svc, _ := newTestMarketplaceService(t)
	// archive/zip's FileHeader.SetMode checks for Go's fs.ModeSymlink type
	// bit (1<<27), NOT the Unix S_IFLNK bits. So to simulate a real symlink
	// entry (the way a Unix zip tool would encode one), we pass the Go
	// FileMode os.ModeSymlink|0777. SetMode then encodes S_IFLNK into the
	// upper 16 bits of ExternalAttrs; on read-back msModeToFileMode maps
	// that back to fs.ModeSymlink, so f.Mode()&os.ModeSymlink triggers the
	// production guard. Passing raw 0xA1FF would NOT work — SetMode would
	// treat it as a regular file (no ModeSymlink bit) and store S_IFREG.
	symlinkVSIX, wantHash := buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(validPackageJSON)},
		{Name: "extension/link", Data: []byte("../../../../etc/passwd"), Mode: uint32(os.ModeSymlink | 0o777)},
	})
	err := svc.installFromVSIXData(symlinkVSIX, wantHash, "acme", "hello", "1.0.0")
	if err == nil {
		t.Fatal("expected symlink rejection, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got: %v", err)
	}
}

// --- Default disabled (G-SEC-12 req. 2 / G-VSC-03 req. 2) ---

// TestMarketplaceInstall_DefaultDisabled verifies that a freshly installed
// extension is disabled by default.
func TestMarketplaceInstall_DefaultDisabled(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	installed, err := svc.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list installed: %v", err)
	}
	if len(installed) != 1 {
		t.Fatalf("expected 1 installed extension, got %d", len(installed))
	}
	ext := installed[0]
	if ext.Enabled {
		t.Errorf("newly installed extension should be disabled by default (G-SEC-12 req. 2); got Enabled=true")
	}
	if ext.Publisher != "acme" || ext.Name != "hello" {
		t.Errorf("unexpected identity: publisher=%q name=%q", ext.Publisher, ext.Name)
	}
	if ext.Version != "1.0.0" {
		t.Errorf("unexpected version: %q", ext.Version)
	}
}

// --- ListInstalledExtensions ---

// TestMarketplaceListInstalled verifies listing multiple installed extensions
// and that the result is sorted by publisher then name.
func TestMarketplaceListInstalled(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)

	// Install two extensions with different publishers.
	vsix, hash := buildValidVSIX(t)
	// Tweak the package.json publisher/name per install by building distinct
	// VSIXes so the metadata files differ.
	pkgA := strings.ReplaceAll(strings.ReplaceAll(validPackageJSON, `"acme"`, `"alpha"`), `"hello"`, `"one"`)
	vsixA, hashA := buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(pkgA)},
		{Name: "extension/main.js", Data: []byte("export function activate() {}\n")},
	})
	pkgB := strings.ReplaceAll(strings.ReplaceAll(validPackageJSON, `"acme"`, `"beta"`), `"hello"`, `"two"`)
	vsixB, hashB := buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(pkgB)},
		{Name: "extension/main.js", Data: []byte("export function activate() {}\n")},
	})
	_ = vsix
	_ = hash
	if err := svc.installFromVSIXData(vsixA, hashA, "alpha", "one", "1.0.0"); err != nil {
		t.Fatalf("install alpha.one: %v", err)
	}
	if err := svc.installFromVSIXData(vsixB, hashB, "beta", "two", "2.0.0"); err != nil {
		t.Fatalf("install beta.two: %v", err)
	}

	installed, err := svc.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list installed: %v", err)
	}
	if len(installed) != 2 {
		t.Fatalf("expected 2 installed extensions, got %d", len(installed))
	}
	// Sorted: alpha.one before beta.two.
	if installed[0].Publisher != "alpha" || installed[0].Name != "one" {
		t.Errorf("expected alpha.one first, got %s.%s", installed[0].Publisher, installed[0].Name)
	}
	if installed[1].Publisher != "beta" || installed[1].Name != "two" {
		t.Errorf("expected beta.two second, got %s.%s", installed[1].Publisher, installed[1].Name)
	}
	// Both disabled by default.
	for _, e := range installed {
		if e.Enabled {
			t.Errorf("%s.%s should be disabled by default", e.Publisher, e.Name)
		}
	}
}

// TestMarketplaceListInstalled_Empty verifies listing when nothing is
// installed returns an empty (non-nil) slice.
func TestMarketplaceListInstalled_Empty(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	installed, err := svc.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list installed: %v", err)
	}
	if installed == nil {
		t.Fatal("expected non-nil slice")
	}
	if len(installed) != 0 {
		t.Fatalf("expected 0 installed extensions, got %d", len(installed))
	}
}

// --- SetExtensionEnabled ---

// TestMarketplaceSetExtensionEnabled verifies that toggling enabled state
// persists and is reflected by ListInstalledExtensions.
func TestMarketplaceSetExtensionEnabled(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	var lifecycle []ExtensionLifecycleRequest
	svc.setExtensionLifecycleRequester(func(_ context.Context, request ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
		lifecycle = append(lifecycle, request)
		return successfulLifecycleResult(request, true), nil
	})
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	lifecycle = nil
	// Enable it.
	if err := svc.SetExtensionEnabled("acme", "hello", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	installed, err := svc.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(installed) != 1 || !installed[0].Enabled {
		t.Fatalf("expected enabled extension, got %+v", installed)
	}
	// Disable it again.
	if err := svc.SetExtensionEnabled("acme", "hello", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	installed, err = svc.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(installed) != 1 || installed[0].Enabled {
		t.Fatalf("expected disabled extension, got %+v", installed)
	}
	if len(lifecycle) != 1 {
		t.Fatalf("disable lifecycle = %+v, want one stop request", lifecycle)
	}
	stop := lifecycle[0]
	if stop.Action != "stop" || stop.ExtensionID != "acme.hello" || stop.Publisher != "acme" || stop.Name != "hello" {
		t.Fatalf("disable lifecycle request = %+v, want stop with exact identity", stop)
	}
	if stop.WasActive {
		t.Fatal("stop request should not claim an active state that backend did not probe")
	}
}

func TestMarketplaceSetExtensionEnabledFailsClosedWhenLifecycleStopRejects(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	rejectStop := false
	svc.setExtensionLifecycleRequester(func(_ context.Context, request ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
		result := successfulLifecycleResult(request, true)
		if !rejectStop || request.Action != "stop" {
			return result, nil
		}
		result.OK = false
		result.Error = "worker still active"
		return result, nil
	})
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	rejectStop = true
	if err := svc.SetExtensionEnabled("acme", "hello", false); err == nil || !strings.Contains(err.Error(), "worker still active") {
		t.Fatalf("disable error = %v, want lifecycle failure", err)
	}
	installed, err := svc.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(installed) != 1 || installed[0].Enabled {
		t.Fatalf("state changed after rejected stop: %+v", installed)
	}
}

func TestMarketplaceSetExtensionEnabledRestoresLifecycleWhenBackendUpdateFails(t *testing.T) {
	svc, configDir := newTestMarketplaceService(t)
	security := NewExtensionSecurityService(configDir)
	svc.setSecurityService(security)
	var lifecycle []ExtensionLifecycleRequest
	svc.setExtensionLifecycleRequester(func(_ context.Context, request ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
		lifecycle = append(lifecycle, request)
		if request.Action == "restore" {
			return successfulLifecycleResult(request, true), nil
		}
		return successfulLifecycleResult(request, true), nil
	})
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Installation starts disabled. Enable it first so the disable operation
	// has a persisted state to restore when the security write fails.
	if err := svc.SetExtensionEnabled("acme", "hello", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	lifecycle = nil
	vsixPath := filepath.Join(configDir, "restricted.vsix")
	if err := os.WriteFile(vsixPath, vsix, 0o600); err != nil {
		t.Fatalf("write vsix: %v", err)
	}
	if _, err := security.RegisterInstall("acme.hello", []ExtensionPermission{PermNetwork}, vsixPath, wantHash); err != nil {
		t.Fatalf("register restricted install: %v", err)
	}
	blocker := filepath.Join(configDir, "security-state-blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create security persistence blocker: %v", err)
	}
	security.configDir = blocker
	if err := svc.SetExtensionEnabled("acme", "hello", false); err == nil {
		t.Fatal("disable unexpectedly succeeded")
	}
	installed, err := svc.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(installed) != 1 || !installed[0].Enabled {
		t.Fatalf("marketplace state should remain enabled after rollback: %+v", installed)
	}
	if len(lifecycle) != 2 || lifecycle[0].Action != "stop" || lifecycle[1].Action != "restore" || !lifecycle[1].WasActive {
		t.Fatalf("lifecycle rollback = %+v, want stop then restore with WasActive", lifecycle)
	}
}

func TestMarketplaceSetExtensionEnabledRollsBackWhenSecurityRejects(t *testing.T) {
	svc, dir := newTestMarketplaceService(t)
	security := NewExtensionSecurityService(dir)
	svc.setSecurityService(security)
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}

	vsixPath := filepath.Join(dir, "restricted.vsix")
	if err := os.WriteFile(vsixPath, vsix, 0o600); err != nil {
		t.Fatalf("write vsix: %v", err)
	}
	if _, err := security.RegisterInstall("acme.hello", []ExtensionPermission{PermNetwork}, vsixPath, wantHash); err != nil {
		t.Fatalf("register restricted install: %v", err)
	}

	if err := svc.SetExtensionEnabled("acme", "hello", true); !errors.Is(err, ErrRestrictedRequiresApproval) {
		t.Fatalf("enable without approval error = %v, want %v", err, ErrRestrictedRequiresApproval)
	}
	installed, err := svc.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(installed) != 1 || installed[0].Enabled {
		t.Fatalf("marketplace state was not rolled back: %+v", installed)
	}
	info, err := security.GetSecurityInfo("acme.hello")
	if err != nil {
		t.Fatalf("get security info: %v", err)
	}
	if info.Enabled {
		t.Fatal("security state should remain disabled")
	}
}

// --- UninstallExtension ---

// TestMarketplaceUninstall verifies that uninstalling removes the extension
// directory and clears it from the listing.
func TestMarketplaceUninstall(t *testing.T) {
	svc, configDir := newTestMarketplaceService(t)
	security := NewExtensionSecurityService(configDir)
	activation := NewActivationService()
	svc.setSecurityService(security)
	svc.setActivationService(activation)
	var lifecycle []ExtensionLifecycleRequest
	svc.setExtensionLifecycleRequester(func(_ context.Context, request ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
		lifecycle = append(lifecycle, request)
		return successfulLifecycleResult(request, request.Action == "stop"), nil
	})
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	lifecycle = nil
	dir := filepath.Join(svc.configDir, extensionsSubdir, "acme.hello")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("extension dir should exist before uninstall: %v", err)
	}
	if err := svc.UninstallExtension("acme", "hello"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("extension dir should be removed after uninstall; stat err=%v", err)
	}
	installed, err := svc.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list after uninstall: %v", err)
	}
	if len(installed) != 0 {
		t.Fatalf("expected 0 installed after uninstall, got %d", len(installed))
	}
	if _, err := security.GetSecurityInfo("acme.hello"); err == nil {
		t.Fatal("security record should be removed after uninstall")
	}
	if _, registered := activation.extensionEventsSnapshot("acme.hello"); registered {
		t.Fatal("activation events should be removed after uninstall")
	}
	if len(lifecycle) != 2 || lifecycle[0].Action != "stop" || lifecycle[1].Action != "invalidate" {
		t.Fatalf("uninstall lifecycle = %+v, want stop then invalidate", lifecycle)
	}
}

func TestMarketplaceUninstall_StopTimeoutWarnsAndContinuesCleanup(t *testing.T) {
	previousTimeout := extensionUninstallTimeout
	extensionUninstallTimeout = 20 * time.Millisecond
	t.Cleanup(func() { extensionUninstallTimeout = previousTimeout })

	svc, configDir := newTestMarketplaceService(t)
	security := NewExtensionSecurityService(configDir)
	activation := NewActivationService()
	svc.setSecurityService(security)
	svc.setActivationService(activation)
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	lifecycle := make(chan string, 2)
	svc.setExtensionLifecycleRequester(func(ctx context.Context, request ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
		lifecycle <- request.Action
		if request.Action == "stop" {
			<-ctx.Done()
			return ExtensionLifecycleResult{}, ctx.Err()
		}
		return successfulLifecycleResult(request, false), nil
	})
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	if err := svc.UninstallExtension("acme", "hello"); err != nil {
		t.Fatalf("uninstall after stop timeout: %v", err)
	}
	actions := make([]string, 0, 2)
	for len(actions) < 2 {
		select {
		case action := <-lifecycle:
			actions = append(actions, action)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for uninstall lifecycle actions; got %v", actions)
		}
	}
	if strings.Join(actions, ",") != "stop,invalidate" {
		t.Fatalf("uninstall lifecycle = %v", actions)
	}
	if !strings.Contains(logs.String(), "continuing cleanup") || !strings.Contains(logs.String(), "deadline exceeded") {
		t.Fatalf("stop timeout warning = %q", logs.String())
	}
	if _, err := os.Stat(svc.extensionDir("acme", "hello")); !os.IsNotExist(err) {
		t.Fatalf("extension directory survived timeout uninstall: %v", err)
	}
	if _, err := security.GetSecurityInfo("acme.hello"); err == nil {
		t.Fatal("security record survived timeout uninstall")
	}
	if _, registered := activation.extensionEventsSnapshot("acme.hello"); registered {
		t.Fatal("activation events survived timeout uninstall")
	}
}

// TestMarketplaceUninstall_NotInstalled verifies uninstalling an extension
// that was never installed does not error (idempotent removal).
func TestMarketplaceUninstall_NotInstalled(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	if err := svc.UninstallExtension("acme", "ghost"); err != nil {
		t.Fatalf("uninstalling a non-installed extension should not error: %v", err)
	}
}

// --- Manifest parsing (Step 3) ---

// TestMarketplaceGetExtensionManifest verifies that the manifest is parsed
// from the extracted extension/package.json after install.
func TestMarketplaceGetExtensionManifest(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	m, err := svc.GetExtensionManifest("acme", "hello")
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	if m.Name != "hello" {
		t.Errorf("expected name hello, got %q", m.Name)
	}
	if m.Engines["vscode"] != "^1.80.0" {
		t.Errorf("expected engines.vscode ^1.80.0, got %q", m.Engines["vscode"])
	}
	if len(m.ActivationEvents) != 1 || m.ActivationEvents[0] != "onStartupFinished" {
		t.Errorf("unexpected activationEvents: %v", m.ActivationEvents)
	}
	if len(m.Contributes) == 0 {
		t.Errorf("expected non-empty contributes")
	}
	if len(m.Capabilities) == 0 {
		t.Errorf("expected non-empty capabilities")
	}
	if m.Main != "./dist/main.js" || m.Browser != "./dist/browser.js" {
		t.Errorf("entry points = main %q, browser %q", m.Main, m.Browser)
	}
	if m.KoyoriIde == nil || m.KoyoriIde.Permissions == nil || len(*m.KoyoriIde.Permissions) != 0 {
		t.Errorf("koyori-ide metadata = %+v, want explicit empty permissions", m.KoyoriIde)
	}
}

// --- Reinstall / update ---

func TestMarketplaceUpdate_CommitsValidatedReplacementDisabled(t *testing.T) {
	svc, configDir := newTestMarketplaceService(t)
	security := NewExtensionSecurityService(configDir)
	activation := NewActivationService()
	svc.setSecurityService(security)
	svc.setActivationService(activation)
	var lifecycle []ExtensionLifecycleRequest
	svc.setExtensionLifecycleRequester(func(_ context.Context, request ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
		lifecycle = append(lifecycle, request)
		return successfulLifecycleResult(request, request.Action == "stop"), nil
	})

	oldVSIX, oldHash := buildUpdateVSIX(t, "1.0.0", []string{"onLanguage:go"}, "old")
	if err := svc.installFromVSIXData(oldVSIX, oldHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install old version: %v", err)
	}
	lifecycle = nil
	if err := svc.SetExtensionEnabled("acme", "hello", true); err != nil {
		t.Fatalf("enable old version: %v", err)
	}

	newVSIX, newHash := buildUpdateVSIX(t, "2.0.0", []string{"onLanguage:typescript"}, "new")
	configureUpdateRegistry(t, svc, "2.0.0", newVSIX, newHash)
	if err := svc.UpdateExtension("acme", "hello", "2.0.0"); err != nil {
		t.Fatalf("update: %v", err)
	}

	installed, err := svc.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list updated extension: %v", err)
	}
	if len(installed) != 1 || installed[0].Version != "2.0.0" || installed[0].Enabled {
		t.Fatalf("updated extension = %+v, want version 2.0.0 disabled", installed)
	}
	marker, err := os.ReadFile(filepath.Join(svc.extensionDir("acme", "hello"), "extension", "marker.txt"))
	if err != nil {
		t.Fatalf("read updated marker: %v", err)
	}
	if string(marker) != "new" {
		t.Fatalf("updated marker = %q, want new", marker)
	}
	info, err := security.GetSecurityInfo("acme.hello")
	if err != nil {
		t.Fatalf("get updated security record: %v", err)
	}
	if info.SHA256 != newHash || info.Enabled {
		t.Fatalf("updated security record = %+v, want new hash disabled", info)
	}
	events, registered := activation.extensionEventsSnapshot("acme.hello")
	if !registered || len(events) != 1 || events[0] != "onLanguage:typescript" {
		t.Fatalf("updated activation events = %v, registered=%v", events, registered)
	}
	for _, suffix := range []string{".updating", ".backup"} {
		if _, err := os.Stat(svc.extensionDir("acme", "hello") + suffix); !os.IsNotExist(err) {
			t.Fatalf("transaction directory %s should be removed; stat err=%v", suffix, err)
		}
	}
	if len(lifecycle) != 2 || lifecycle[0].Action != "stop" || lifecycle[1].Action != "commit" {
		t.Fatalf("update lifecycle = %+v, want stop then commit", lifecycle)
	}
	if !lifecycle[1].WasActive {
		t.Fatal("commit lifecycle request did not preserve prior active state")
	}
}

func TestMarketplaceUpdate_RollsBackDirectoryStateSecurityAndActivation(t *testing.T) {
	svc, configDir := newTestMarketplaceService(t)
	security := NewExtensionSecurityService(configDir)
	activation := NewActivationService()
	svc.setSecurityService(security)
	svc.setActivationService(activation)
	var lifecycle []ExtensionLifecycleRequest
	svc.setExtensionLifecycleRequester(func(_ context.Context, request ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
		lifecycle = append(lifecycle, request)
		return successfulLifecycleResult(request, request.Action == "stop"), nil
	})

	oldVSIX, oldHash := buildUpdateVSIX(t, "1.0.0", []string{"onLanguage:go"}, "old")
	if err := svc.installFromVSIXData(oldVSIX, oldHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install old version: %v", err)
	}
	lifecycle = nil
	if err := svc.SetExtensionEnabled("acme", "hello", true); err != nil {
		t.Fatalf("enable old version: %v", err)
	}

	newVSIX, newHash := buildUpdateVSIX(t, "2.0.0", []string{"onLanguage:typescript"}, "new")
	configureUpdateRegistry(t, svc, "2.0.0", newVSIX, newHash)
	blocker := filepath.Join(configDir, "security-state-blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create security persistence blocker: %v", err)
	}
	security.configDir = blocker

	err := svc.UpdateExtension("acme", "hello", "2.0.0")
	if err == nil || !strings.Contains(err.Error(), "security registration failed") {
		t.Fatalf("update error = %v, want security registration failure", err)
	}

	installed, listErr := svc.ListInstalledExtensions()
	if listErr != nil {
		t.Fatalf("list rolled-back extension: %v", listErr)
	}
	if len(installed) != 1 || installed[0].Version != "1.0.0" || !installed[0].Enabled {
		t.Fatalf("rolled-back extension = %+v, want version 1.0.0 enabled", installed)
	}
	marker, readErr := os.ReadFile(filepath.Join(svc.extensionDir("acme", "hello"), "extension", "marker.txt"))
	if readErr != nil {
		t.Fatalf("read rolled-back marker: %v", readErr)
	}
	if string(marker) != "old" {
		t.Fatalf("rolled-back marker = %q, want old", marker)
	}
	events, registered := activation.extensionEventsSnapshot("acme.hello")
	if !registered || len(events) != 1 || events[0] != "onLanguage:go" {
		t.Fatalf("rolled-back activation events = %v, registered=%v", events, registered)
	}
	persistedSecurity := NewExtensionSecurityService(configDir)
	info, securityErr := persistedSecurity.GetSecurityInfo("acme.hello")
	if securityErr != nil {
		t.Fatalf("get rolled-back security record: %v", securityErr)
	}
	if info.SHA256 != oldHash || !info.Enabled {
		t.Fatalf("rolled-back security record = %+v, want old hash enabled", info)
	}
	for _, suffix := range []string{".updating", ".backup"} {
		if _, statErr := os.Stat(svc.extensionDir("acme", "hello") + suffix); !os.IsNotExist(statErr) {
			t.Fatalf("transaction directory %s should be removed after rollback; stat err=%v", suffix, statErr)
		}
	}
	if len(lifecycle) != 2 || lifecycle[0].Action != "stop" || lifecycle[1].Action != "restore" {
		t.Fatalf("rollback lifecycle = %+v, want stop then restore", lifecycle)
	}
	if !lifecycle[1].WasActive {
		t.Fatal("restore lifecycle request did not preserve prior active state")
	}
}

// TestMarketplaceReinstall_Overwrites verifies that installing an extension
// that is already installed replaces the prior version cleanly.
func TestMarketplaceReinstall_Overwrites(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install v1: %v", err)
	}
	// Enable it, then reinstall — the reinstall should reset to disabled.
	if err := svc.SetExtensionEnabled("acme", "hello", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	installed, err := svc.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(installed) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(installed))
	}
	if installed[0].Enabled {
		t.Errorf("reinstall should reset enabled to default-disabled; got Enabled=true")
	}
}

// --- ValidateExtensionIdent ---

// TestMarketplaceValidateIdent verifies the publisher/name guard rejects
// path-bearing identifiers that could escape the extensions directory.
func TestMarketplaceValidateIdent(t *testing.T) {
	cases := []struct {
		name      string
		publisher string
		ext       string
		wantErr   bool
	}{
		{"valid", "acme", "hello", false},
		{"empty publisher", "", "hello", true},
		{"empty name", "acme", "", true},
		{"publisher traversal", "..", "hello", true},
		{"name traversal", "acme", "..", true},
		{"publisher slash", "a/b", "hello", true},
		{"name backslash", "acme", "h\\i", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateExtensionIdent(c.publisher, c.ext)
			if c.wantErr && err == nil {
				t.Errorf("expected error for publisher=%q name=%q", c.publisher, c.ext)
			}
			if !c.wantErr && err != nil {
				t.Errorf("unexpected error for publisher=%q name=%q: %v", c.publisher, c.ext, err)
			}
		})
	}
}

// --- State persistence across instances ---

// TestMarketplaceStatePersistsAcrossInstances verifies that enabled state
// written by one service instance is read by a fresh instance pointed at the
// same config dir (the state lives on disk, not in memory).
func TestMarketplaceStatePersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	svc1 := NewMarketplaceService(dir)
	vsix, wantHash := buildValidVSIX(t)
	if err := svc1.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := svc1.SetExtensionEnabled("acme", "hello", true); err != nil {
		t.Fatalf("enable: %v", err)
	}

	svc2 := NewMarketplaceService(dir)
	installed, err := svc2.ListInstalledExtensions()
	if err != nil {
		t.Fatalf("list via second instance: %v", err)
	}
	if len(installed) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(installed))
	}
	if !installed[0].Enabled {
		t.Errorf("enabled state should have persisted across instances; got Enabled=false")
	}
}

// --- State file shape ---

// TestMarketplaceStateFileShape verifies the on-disk state file is valid JSON
// with the expected key after a default-disabled install.
func TestMarketplaceStateFileShape(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	statePath := filepath.Join(svc.configDir, "koyori-ide", extensionsStateFileName)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var state mpExtensionStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("parse state file: %v", err)
	}
	entry, ok := state.Extensions["acme.hello"]
	if !ok {
		t.Fatalf("expected state entry for acme.hello; got %+v", state.Extensions)
	}
	if entry.Enabled {
		t.Errorf("state entry should be Enabled=false (default disabled); got true")
	}
}

// --- CRIT-02 / G-SEC-12: marketplace ↔ security service integration ---

// TestMarketplaceInstall_CRIT02_BlacklistedRejectedWithSecurityService
// verifies that when a MarketplaceService has an ExtensionSecurityService
// wired in, installing a blacklisted extension is rejected at the blacklist
// gate (before any files are written) and the extension directory is never
// created.
func TestMarketplaceInstall_CRIT02_BlacklistedRejectedWithSecurityService(t *testing.T) {
	svc, dir := newTestMarketplaceService(t)
	ss := NewExtensionSecurityService(dir)
	svc.setSecurityService(ss)

	vsix, wantHash := buildValidVSIX(t)
	// "anabarban.anabarban" is in the built-in default blacklist.
	err := svc.installFromVSIXData(vsix, wantHash, "anabarban", "anabarban", "1.0.0")
	if err == nil {
		t.Fatal("expected blacklisted install to be rejected, got nil error (CRIT-02)")
	}
	if !strings.Contains(err.Error(), "blacklisted") {
		t.Errorf("expected blacklisted error, got: %v", err)
	}
	// The extension directory must NOT exist (blacklist gate fires before
	// extraction).
	targetDir := svc.extensionDir("anabarban", "anabarban")
	if _, statErr := os.Stat(targetDir); !os.IsNotExist(statErr) {
		t.Errorf("blacklisted extension directory should not exist: path=%s statErr=%v", targetDir, statErr)
	}
}

// TestMarketplaceInstall_CRIT02_RegistersWithSecurityService verifies that a
// successful install of a legitimate extension triggers
// ExtensionSecurityService.RegisterInstall, producing a security state entry
// with Enabled=false and PendingReview=true (G-SEC-12 req. 2: default
// disabled + pending review).
func TestMarketplaceInstall_CRIT02_RegistersWithSecurityService(t *testing.T) {
	svc, dir := newTestMarketplaceService(t)
	ss := NewExtensionSecurityService(dir)
	svc.setSecurityService(ss)

	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// The security service must have a registered entry for acme.hello.
	info, err := ss.GetSecurityInfo("acme.hello")
	if err != nil {
		t.Fatalf("GetSecurityInfo(acme.hello): %v (CRIT-02: RegisterInstall not called)", err)
	}
	if info.Enabled {
		t.Errorf("registered extension should be Enabled=false (CRIT-02 default disabled); got true")
	}
	if !info.PendingReview {
		t.Errorf("registered extension should be PendingReview=true (CRIT-02 pending review); got false")
	}
}

func TestMarketplaceInstall_RegistersDeclaredPermissions(t *testing.T) {
	svc, dir := newTestMarketplaceService(t)
	ss := NewExtensionSecurityService(dir)
	svc.setSecurityService(ss)
	pkg := `{"name":"runner","publisher":"acme","version":"1.0.0","main":"dist/main.js","koyoriIde":{"permissions":["tasks.execute","fs.read","tasks.execute"]}}`
	vsix, hash := buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(pkg)},
		{Name: "extension/dist/main.js", Data: []byte("module.exports.activate = function() {}")},
	})
	if err := svc.installFromVSIXData(vsix, hash, "acme", "runner", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	info, err := ss.GetSecurityInfo("acme.runner")
	if err != nil {
		t.Fatalf("get security info: %v", err)
	}
	if info.Level != SecurityReviewed {
		t.Fatalf("security level = %q, want %q", info.Level, SecurityReviewed)
	}
	want := []ExtensionPermission{PermTasksExec, PermFsRead}
	if len(info.Permissions) != len(want) || info.Permissions[0] != want[0] || info.Permissions[1] != want[1] {
		t.Fatalf("permissions = %v, want %v", info.Permissions, want)
	}
}

func TestMarketplaceInstall_RejectsExecutableWithoutPermissionDeclaration(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	pkg := `{"name":"runner","publisher":"acme","version":"1.0.0","main":"dist/main.js"}`
	vsix, hash := buildVSIX(t, []zipEntry{{Name: "extension/package.json", Data: []byte(pkg)}})
	err := svc.installFromVSIXData(vsix, hash, "acme", "runner", "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "must declare koyoriIde.permissions") {
		t.Fatalf("expected missing permission declaration error, got %v", err)
	}
	if _, statErr := os.Stat(svc.extensionDir("acme", "runner")); !os.IsNotExist(statErr) {
		t.Fatalf("rejected extension should not be installed: %v", statErr)
	}
}

func TestMarketplaceInstall_RejectsUnknownPermission(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	pkg := `{"name":"runner","publisher":"acme","version":"1.0.0","main":"dist/main.js","koyoriIde":{"permissions":["shell.exec"]}}`
	vsix, hash := buildVSIX(t, []zipEntry{{Name: "extension/package.json", Data: []byte(pkg)}})
	err := svc.installFromVSIXData(vsix, hash, "acme", "runner", "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "unknown extension permission") {
		t.Fatalf("expected unknown permission error, got %v", err)
	}
}

// ============================================================================
// G-MKT-02: Tests for marketplace enhancements (category browse, featured,
// update check, README fetch, categories list).
// ============================================================================

// mockRegistry creates an httptest.Server that responds to Open VSX API
// endpoints with canned data. Returns the server and a cleanup func.
func mockRegistry(t *testing.T, searchHandler, detailHandler, readmeHandler func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *MarketplaceService) {
	t.Helper()
	mux := http.NewServeMux()
	if searchHandler != nil {
		mux.HandleFunc("/api/-/search", searchHandler)
	}
	if detailHandler != nil {
		mux.HandleFunc("/api/", detailHandler)
	}
	if readmeHandler != nil {
		mux.HandleFunc("/readme/", readmeHandler)
	}
	server := httptest.NewServer(mux)
	svc := NewMarketplaceService(t.TempDir())
	// N-7: SetRegistryURL 现在拒绝 loopback（SSRF 校验）。测试直接设置
	// registryURL 字段模拟公网 registry。
	svc.mu.Lock()
	svc.registryURL = server.URL + "/api"
	svc.mu.Unlock()
	t.Cleanup(func() {
		server.Close()
	})
	return server, svc
}

func TestMarketplace_GetCategories_ReturnsStandardCategories(t *testing.T) {
	svc := NewMarketplaceService("")
	cats := svc.GetCategories()
	if len(cats) < 8 {
		t.Errorf("expected at least 8 categories, got %d", len(cats))
	}
	// Verify a few key categories are present.
	seen := map[string]bool{}
	for _, c := range cats {
		seen[c] = true
	}
	for _, expected := range []string{"Programming Languages", "Snippets", "Themes", "Linters"} {
		if !seen[expected] {
			t.Errorf("expected category %q in list", expected)
		}
	}
}

func TestMarketplace_BrowseByCategory_BuildsCorrectURL(t *testing.T) {
	var capturedURL string
	_, svc := mockRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		json.NewEncoder(w).Encode(ovsxSearchResponse{
			Extensions: []ovsxExtension{
				{Name: "python", Namespace: "ms-python", DisplayName: "Python"},
			},
		})
	}, nil, nil)

	results, err := svc.BrowseByCategory("Programming Languages", 1, 20)
	if err != nil {
		t.Fatalf("BrowseByCategory: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Name != "python" {
		t.Errorf("expected 'python', got %q", results[0].Name)
	}
	if !strings.Contains(capturedURL, "category=Programming") {
		t.Errorf("expected category in URL, got %q", capturedURL)
	}
	if !strings.Contains(capturedURL, "sortBy=downloadCount") {
		t.Errorf("expected sortBy=downloadCount in URL, got %q", capturedURL)
	}
}

func TestMarketplace_GetFeaturedExtensions_ReturnsPopular(t *testing.T) {
	_, svc := mockRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.String(), "size=12") {
			t.Errorf("expected size=12 for featured, got %s", r.URL.String())
		}
		if !strings.Contains(r.URL.String(), "sortBy=downloadCount") {
			t.Errorf("expected sortBy=downloadCount for featured, got %s", r.URL.String())
		}
		json.NewEncoder(w).Encode(ovsxSearchResponse{
			Extensions: []ovsxExtension{
				{Name: "go", Namespace: "golang", DisplayName: "Go", DownloadCount: 5000000},
				{Name: "python", Namespace: "ms-python", DisplayName: "Python", DownloadCount: 3000000},
			},
		})
	}, nil, nil)

	results, err := svc.GetFeaturedExtensions()
	if err != nil {
		t.Fatalf("GetFeaturedExtensions: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 featured results, got %d", len(results))
	}
	if results[0].Name != "go" {
		t.Errorf("expected first featured to be 'go', got %q", results[0].Name)
	}
}

func TestMarketplace_GetExtensionReadme_ReturnsContent(t *testing.T) {
	// Single mock server that handles both the detail endpoint and the readme
	// download. The detail response includes a readme URL pointing back to the
	// same mock server's /readme path.
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/acme/hello"):
			readmeURL := server.URL + "/readme/hello.md"
			json.NewEncoder(w).Encode(ovsxExtension{
				Name:      "hello",
				Namespace: "acme",
				Files: ovsxFileMap{
					"readme": readmeURL,
				},
			})
		case strings.Contains(path, "/readme/"):
			w.Write([]byte("# Hello Extension\n\nThis is a test README."))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	svc := NewMarketplaceService(t.TempDir())
	// N-7: SetRegistryURL 现在拒绝 loopback（SSRF 校验）。测试直接设置
	// registryURL 字段模拟公网 registry。
	svc.mu.Lock()
	svc.registryURL = server.URL + "/api"
	svc.mu.Unlock()
	t.Cleanup(server.Close)

	readme, err := svc.GetExtensionReadme("acme", "hello")
	if err != nil {
		t.Fatalf("GetExtensionReadme: %v", err)
	}
	if !strings.Contains(readme, "# Hello Extension") {
		t.Errorf("expected README to contain '# Hello Extension', got %q", readme)
	}
}

func TestMarketplace_GetExtensionReadme_EmptyWhenNoReadme(t *testing.T) {
	_, svc := mockRegistry(t,
		nil,
		func(w http.ResponseWriter, r *http.Request) {
			// Extension with no readme file.
			json.NewEncoder(w).Encode(ovsxExtension{
				Name:      "hello",
				Namespace: "acme",
				Files:     ovsxFileMap{},
			})
		},
		nil,
	)

	readme, err := svc.GetExtensionReadme("acme", "hello")
	if err != nil {
		t.Fatalf("GetExtensionReadme: %v", err)
	}
	if readme != "" {
		t.Errorf("expected empty readme, got %q", readme)
	}
}

func TestMarketplace_CheckForUpdates_DetectsNewVersion(t *testing.T) {
	svc, dir := newTestMarketplaceService(t)

	// Install a fake extension at version 1.0.0.
	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	_ = dir

	// Set up mock registry where the extension has version 2.0.0.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/-/search"):
			json.NewEncoder(w).Encode(ovsxSearchResponse{})
		case strings.Contains(path, "/acme/hello"):
			json.NewEncoder(w).Encode(ovsxExtension{
				Name:      "hello",
				Namespace: "acme",
				Version:   "2.0.0",
				Versions: []ovsxVersion{
					{Version: "2.0.0", Files: ovsxFileMap{"download": "http://example.com/dl"}},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	// N-7: SetRegistryURL 现在拒绝 loopback（SSRF 校验）。测试直接设置
	// registryURL 字段模拟公网 registry。
	svc.mu.Lock()
	svc.registryURL = server.URL + "/api"
	svc.mu.Unlock()
	t.Cleanup(server.Close)

	updates, err := svc.CheckForUpdates()
	if err != nil {
		t.Fatalf("CheckForUpdates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if updates[0].CurrentVersion != "1.0.0" {
		t.Errorf("expected current 1.0.0, got %s", updates[0].CurrentVersion)
	}
	if updates[0].LatestVersion != "2.0.0" {
		t.Errorf("expected latest 2.0.0, got %s", updates[0].LatestVersion)
	}
}

func TestMarketplace_CheckForUpdates_EmptyWhenUpToDate(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)

	vsix, wantHash := buildValidVSIX(t)
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/-/search"):
			json.NewEncoder(w).Encode(ovsxSearchResponse{})
		case strings.Contains(path, "/acme/hello"):
			// Same version — no update.
			json.NewEncoder(w).Encode(ovsxExtension{
				Name:      "hello",
				Namespace: "acme",
				Version:   "1.0.0",
				Versions: []ovsxVersion{
					{Version: "1.0.0"},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	// N-7: SetRegistryURL 现在拒绝 loopback（SSRF 校验）。测试直接设置
	// registryURL 字段模拟公网 registry。
	svc.mu.Lock()
	svc.registryURL = server.URL + "/api"
	svc.mu.Unlock()
	t.Cleanup(server.Close)

	updates, err := svc.CheckForUpdates()
	if err != nil {
		t.Fatalf("CheckForUpdates: %v", err)
	}
	if len(updates) != 0 {
		t.Errorf("expected 0 updates when up to date, got %d", len(updates))
	}
}

// TestMarketplaceService_H2_HTTPGet_RejectsOversizedResponse verifies that
// httpGet rejects a response body exceeding maxHTTPResponseSize (H-2).
// We temporarily lower the limit to keep the test fast.
func TestMarketplaceService_H2_HTTPGet_RejectsOversizedResponse(t *testing.T) {
	// Temporarily lower the limit to 100 bytes for a fast test.
	oldLimit := maxHTTPResponseSize
	maxHTTPResponseSize = 100
	t.Cleanup(func() { maxHTTPResponseSize = oldLimit })

	// Serve a body of 200 bytes (exceeds the 100-byte limit).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(200)
		w.Write(make([]byte, 200))
	}))
	defer server.Close()

	svc := NewMarketplaceService(t.TempDir())
	_, err := svc.httpGetBytes(server.URL)
	if err == nil {
		t.Fatal("H-2: expected error for oversized response, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds max size") {
		t.Errorf("H-2: expected 'exceeds max size' error, got: %v", err)
	}
}

// TestMarketplaceService_H2_HTTPGet_AcceptsNormalResponse verifies that
// normal-sized responses are not rejected by the size limit (H-2).
func TestMarketplaceService_H2_HTTPGet_AcceptsNormalResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	svc := NewMarketplaceService(t.TempDir())
	data, err := svc.httpGetJSON(server.URL)
	if err != nil {
		t.Fatalf("H-2: httpGetJSON failed: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("H-2: unexpected response: %s", string(data))
	}
}

// TestMarketplaceService_H3_SetRegistryURL_RejectsInvalidURLs verifies that
// SetRegistryURL rejects non-http(s) schemes, embedded credentials, and
// non-loopback plain http URLs (H-3).
func TestMarketplaceService_H3_SetRegistryURL_RejectsInvalidURLs(t *testing.T) {
	svc := NewMarketplaceService(t.TempDir())

	tests := []struct {
		name string
		url  string
	}{
		{"empty scheme", "://example.com/api"},
		{"file scheme", "file:///etc/passwd"},
		{"ftp scheme", "ftp://example.com/api"},
		{"data scheme", "data:text/html,<script>alert(1)</script>"},
		{"embedded credentials", "http://user:pass@localhost:1234/api"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.SetRegistryURL(tt.url)
			if err == nil {
				t.Errorf("H-3: expected error for URL %q, got nil", tt.url)
			}
		})
	}
}

// TestMarketplaceService_H3_SetRegistryURL_AcceptsValidURLs verifies that
// valid public http(s) URLs are accepted. N-7: loopback/localhost/private IPs
// are now rejected (moved to TestSetRegistryURLSSRF).
func TestMarketplaceService_H3_SetRegistryURL_AcceptsValidURLs(t *testing.T) {
	svc := NewMarketplaceService(t.TempDir())

	tests := []struct {
		name string
		url  string
	}{
		{"https public", "https://open-vsx.org/api"},
		// N-7: TEST-NET-3 (203.0.113.0/24) 是公网测试 IP，不被 isPrivateHost 拒绝
		{"https public test-net-3", "https://203.0.113.2/api"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := svc.SetRegistryURL(tt.url); err != nil {
				t.Errorf("expected success for URL %q, got: %v", tt.url, err)
			}
		})
	}
}

// TestMarketplaceService_H3_SetRegistryURL_EmptyResetsToDefault verifies
// that passing an empty string resets the registry URL to the default.
func TestMarketplaceService_H3_SetRegistryURL_EmptyResetsToDefault(t *testing.T) {
	svc := NewMarketplaceService(t.TempDir())

	// Set a custom URL first (N-7: use IP literal to avoid DNS resolution in tests).
	if err := svc.SetRegistryURL("https://203.0.113.1/api"); err != nil {
		t.Fatalf("SetRegistryURL failed: %v", err)
	}

	// Reset to default.
	if err := svc.SetRegistryURL(""); err != nil {
		t.Fatalf("SetRegistryURL('') failed: %v", err)
	}

	// Verify the registry URL is back to the default.
	svc.mu.Lock()
	got := svc.registryURL
	svc.mu.Unlock()
	if got != defaultOpenVSXRegistryAPI {
		t.Errorf("H-3: expected default registry %q, got %q", defaultOpenVSXRegistryAPI, got)
	}
}

// TestSetRegistryURLSSRF (N-7) 验证 SetRegistryURL 拒绝内网/loopback/私有 IP，
// 防止用户将 marketplace 指向内部服务器（SSRF 探测向量）。
func TestSetRegistryURLSSRF(t *testing.T) {
	svc := NewMarketplaceService(t.TempDir())

	tests := []struct {
		name string
		url  string
	}{
		// loopback
		{"localhost", "https://localhost/api"},
		{"localhost subdomain", "https://app.localhost/api"},
		{"127.0.0.1", "https://127.0.0.1/api"},
		{"127.0.0.1 http", "http://127.0.0.1:8080/api"},
		// RFC 1918 private
		{"10.0.0.1", "https://10.0.0.1/api"},
		{"172.16.0.1", "https://172.16.0.1/api"},
		{"192.168.1.1", "https://192.168.1.1/api"},
		// link-local (cloud metadata)
		{"169.254.169.254", "https://169.254.169.254/api"},
		// unspecified
		{"0.0.0.0", "https://0.0.0.0/api"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.SetRegistryURL(tt.url)
			if err == nil {
				t.Errorf("N-7: expected SSRF rejection for %q, got nil", tt.url)
			}
		})
	}

	// 验证 registry URL 未被修改（错误时保持不变）
	svc.mu.Lock()
	got := svc.registryURL
	svc.mu.Unlock()
	if got != defaultOpenVSXRegistryAPI {
		t.Errorf("registry URL 应保持默认值，得到 %q", got)
	}
}

// TestInstallVSIXFromTempFile (P2-4) 验证流式下载 + 从磁盘安装路径。
// 用 httptest.Server 提供真实 VSIX 下载，验证：
// 1. downloadVSIXToTempFile 流式哈希与直接 sha256 一致
// 2. installFromVSIXFile 用 zip.OpenReader 从磁盘读取并正确安装
// 3. 安装后临时文件可被调用方删除
func TestInstallVSIXFromTempFile(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	vsixData, wantHash := buildValidVSIX(t)

	// 用 httptest.Server 提供 VSIX 下载
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(200)
		w.Write(vsixData)
	}))
	defer srv.Close()

	// 流式下载到临时文件 + 同时哈希
	tmpPath, gotHash, err := svc.downloadVSIXToTempFile(srv.URL)
	if err != nil {
		t.Fatalf("downloadVSIXToTempFile: %v", err)
	}
	defer os.Remove(tmpPath)

	// 流式哈希必须与直接哈希一致
	if gotHash != wantHash {
		t.Errorf("streaming hash mismatch: got %s, want %s", gotHash, wantHash)
	}

	// 临时文件应存在且大小匹配
	info, err := os.Stat(tmpPath)
	if err != nil {
		t.Fatalf("stat tmp: %v", err)
	}
	if info.Size() != int64(len(vsixData)) {
		t.Errorf("tmp size %d != vsix size %d", info.Size(), len(vsixData))
	}

	// 从磁盘安装
	if err := svc.installFromVSIXFile(tmpPath, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("installFromVSIXFile: %v", err)
	}

	// 验证安装成功（manifest 可读）
	manifest, err := svc.GetExtensionManifest("acme", "hello")
	if err != nil {
		t.Fatalf("GetExtensionManifest after install: %v", err)
	}
	if manifest.Name != "hello" {
		t.Errorf("manifest.Name = %q, want %q", manifest.Name, "hello")
	}
}

func findFunction(t *testing.T, fileName, functionName string) *ast.FuncDecl {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), fileName, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", fileName, err)
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == functionName {
			return fn
		}
	}
	t.Fatalf("function %s not found in %s", functionName, fileName)
	return nil
}

func selectorCall(call *ast.CallExpr) (packageOrReceiver, method string, ok bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", selector.Sel.Name, true
	}
	return ident.Name, selector.Sel.Name, true
}

// TestMarketplaceInstall_RegistrationStaysFileBased is a deterministic
// regression guard for Unit 4. Heap snapshots are noisy in shared CI; the
// production contract is stronger and simpler to assert structurally: both
// the marketplace install and security registration functions must avoid
// whole-file read APIs, and marketplace must pass its original temp path to
// RegisterInstallFromFile.
func TestMarketplaceInstall_RegistrationStaysFileBased(t *testing.T) {
	installFn := findFunction(t, "marketplace_service.go", "installFromVSIXFile")
	registerFn := findFunction(t, "extension_security_service.go", "RegisterInstallFromFile")

	var registrationUsesTmpPath bool
	for _, fn := range []*ast.FuncDecl{installFn, registerFn} {
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			receiver, method, ok := selectorCall(call)
			if !ok {
				return true
			}
			if (receiver == "os" && method == "ReadFile") || (receiver == "io" && method == "ReadAll") {
				t.Errorf("%s must not call %s.%s; VSIX registration must remain file-based", fn.Name.Name, receiver, method)
			}
			if fn == installFn && method == "RegisterInstallFromFile" && len(call.Args) == 4 {
				pathArg, ok := call.Args[2].(*ast.Ident)
				registrationUsesTmpPath = ok && pathArg.Name == "tmpPath"
			}
			return true
		})
	}
	if !registrationUsesTmpPath {
		t.Fatal("installFromVSIXFile must register the original tmpPath with RegisterInstallFromFile")
	}
}

func createSparseLargeVSIX(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create sparse VSIX: %v", err)
	}
	const sparsePrefixSize = int64(51 * 1024 * 1024)
	if _, err := file.Seek(sparsePrefixSize, io.SeekStart); err != nil {
		_ = file.Close()
		t.Fatalf("seek sparse VSIX: %v", err)
	}
	writer := zip.NewWriter(file)
	writer.SetOffset(sparsePrefixSize)
	entries := []zipEntry{
		{Name: "extension/package.json", Data: []byte(validPackageJSON)},
		{Name: "extension/main.js", Data: []byte("export function activate() {}\n")},
		{Name: "[Content_Types].xml", Data: []byte("<Types/>")},
	}
	for _, entry := range entries {
		part, err := writer.CreateHeader(&zip.FileHeader{Name: entry.Name, Method: zip.Deflate})
		if err != nil {
			_ = writer.Close()
			_ = file.Close()
			t.Fatalf("create sparse VSIX entry %q: %v", entry.Name, err)
		}
		if _, err := part.Write(entry.Data); err != nil {
			_ = writer.Close()
			_ = file.Close()
			t.Fatalf("write sparse VSIX entry %q: %v", entry.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		t.Fatalf("close sparse VSIX zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close sparse VSIX: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sparse VSIX: %v", err)
	}
	if info.Size() <= 50*1024*1024 {
		t.Fatalf("large VSIX size = %d, want more than 50 MB", info.Size())
	}
	hash, err := computeFileSHA256(path)
	if err != nil {
		t.Fatalf("hash sparse VSIX: %v", err)
	}
	return hash
}

func TestMarketplaceInstall_LargeVSIXStreamsThroughSecurityRegistration(t *testing.T) {
	svc, configDir := newTestMarketplaceService(t)
	security := NewExtensionSecurityService(configDir)
	svc.setSecurityService(security)
	vsixPath := filepath.Join(t.TempDir(), "large.vsix")
	wantHash := createSparseLargeVSIX(t, vsixPath)

	runtime.GC()
	previousGCPercent := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(previousGCPercent)
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := svc.installFromVSIXFile(vsixPath, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install large VSIX: %v", err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	const maxHeapGrowth = uint64(16 * 1024 * 1024)
	var growth uint64
	if after.HeapAlloc > before.HeapAlloc {
		growth = after.HeapAlloc - before.HeapAlloc
	}
	if growth > maxHeapGrowth {
		t.Fatalf("large VSIX install heap grew by %d bytes, want at most %d", growth, maxHeapGrowth)
	}
	info, err := security.GetSecurityInfo("acme.hello")
	if err != nil {
		t.Fatalf("get security info: %v", err)
	}
	if !info.Verified || !strings.EqualFold(info.SHA256, wantHash) {
		t.Fatalf("security info verification = %v hash = %q, want verified hash %q", info.Verified, info.SHA256, wantHash)
	}
	if info.Enabled || !info.PendingReview {
		t.Fatalf("large VSIX security state = enabled:%v pending:%v, want disabled and pending review", info.Enabled, info.PendingReview)
	}
	if _, err := os.Stat(svc.extensionDir("acme", "hello") + ".vsix"); !os.IsNotExist(err) {
		t.Fatalf("security registration created a second VSIX copy: %v", err)
	}
}

func TestManualRemoveWalkDirFunc_ClassifiesWithoutInfo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	tests := []struct {
		name      string
		path      string
		entry     *marketplaceWalkTestDirEntry
		walkErr   error
		wantFiles []string
		wantDirs  []string
	}{
		{name: "permission error", path: filepath.Join(root, "private"), entry: &marketplaceWalkTestDirEntry{name: "private", mode: fs.ModeDir}, walkErr: fs.ErrPermission},
		{name: "root", path: root, entry: &marketplaceWalkTestDirEntry{name: "root", mode: fs.ModeDir}},
		{name: "directory", path: filepath.Join(root, "nested"), entry: &marketplaceWalkTestDirEntry{name: "nested", mode: fs.ModeDir}, wantDirs: []string{filepath.Join(root, "nested")}},
		{name: "regular file", path: filepath.Join(root, "file.txt"), entry: &marketplaceWalkTestDirEntry{name: "file.txt"}, wantFiles: []string{filepath.Join(root, "file.txt")}},
		{name: "symbolic link", path: filepath.Join(root, "link"), entry: &marketplaceWalkTestDirEntry{name: "link", mode: fs.ModeSymlink}, wantFiles: []string{filepath.Join(root, "link")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var files []string
			var dirs []string
			walkFn := manualRemoveWalkDirFunc(root, &files, &dirs)
			if err := walkFn(tt.path, tt.entry, tt.walkErr); err != nil {
				t.Fatalf("callback returned %v", err)
			}
			if tt.entry.infoCalls != 0 {
				t.Errorf("Info called %d times, want 0", tt.entry.infoCalls)
			}
			if fmt.Sprint(files) != fmt.Sprint(tt.wantFiles) {
				t.Errorf("files = %v, want %v", files, tt.wantFiles)
			}
			if fmt.Sprint(dirs) != fmt.Sprint(tt.wantDirs) {
				t.Errorf("dirs = %v, want %v", dirs, tt.wantDirs)
			}
		})
	}
}
