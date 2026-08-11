package services

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func publicGitHubTestResolver(host string) ([]net.IP, error) {
	if host != "api.github.com" {
		return nil, fmt.Errorf("unexpected host %q", host)
	}
	return []net.IP{net.ParseIP("192.0.2.1")}, nil
}

func (f updateRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func updateReleaseResponse() *http.Response {
	body := `{"tag_name":"v1.0.0","html_url":"https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/v1.0.0","body":"release notes for 1.0.0","published_at":"2024-01-01T00:00:00Z","assets":[{"name":"koyori-ide-windows-amd64.zip","browser_download_url":"https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/download/v1.0.0/koyori-ide-windows-amd64.zip","digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}]}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// update_crash_service_p10_test.go — 优先级 10 (prompt-1.md 458-466) 测试。
//
// 覆盖自动更新（版本比较、检查更新逻辑）与崩溃报告（写入、列出、读取、
// 删除、清空）。CheckForUpdates 通过注入 one-hop RoundTripper 提供模拟 GitHub
// Releases 响应，不发起真实 HTTP 请求。崩溃报告使用 SetDir 指向临时目录，
// 避免污染用户主目录。

// newTestCrashService 返回一个崩溃目录指向临时目录的 CrashService，
// 并返回该临时目录路径以便测试断言文件状态。
func newTestCrashService(t *testing.T) (*CrashService, string) {
	t.Helper()
	dir := t.TempDir()
	srv := NewCrashService(nil)
	srv.setDir(dir)
	return srv, dir
}

func TestUpdateService_P10_CompareVersions(t *testing.T) {
	s := NewUpdateService()
	cases := []struct {
		name    string
		current string
		latest  string
		want    int
	}{
		{"newer patch", "1.0.0", "1.0.1", -1},
		{"equal", "1.0.0", "1.0.0", 0},
		{"prerelease lower than release", "1.0.0-beta", "1.0.0", -1},
		{"major bump", "2.0.0", "1.9.9", 1},
		{"v prefix stripped", "v1.0.0", "1.0.0", 0},
		{"prerelease vs newer prerelease", "1.0.0-alpha", "1.0.0-beta", -1},
		{"release newer than prerelease", "1.0.0", "1.0.0-beta", 1},
		{"minor bump", "1.2.0", "1.10.0", -1},
		{"missing segment", "1.0", "1.0.0", 0},
		{"different major", "2.0.0", "10.0.0", -1},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := s.CompareVersions(c.current, c.latest)
			if got != c.want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.current, c.latest, got, c.want)
			}
		})
	}
}

// TestUpdateService_P10_CheckForUpdates_NoUpdate 验证当当前版本 >= 最新版本时
// HasUpdate 为 false，且版本字段正确回填。同时验证当远端版本更新时 HasUpdate 为 true。
func TestUpdateService_P10_CheckForUpdates_NoUpdate(t *testing.T) {
	s := NewUpdateService()
	s.setLookupIP(publicGitHubTestResolver)
	var requestedURLs []string
	s.setHTTPTransport(updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestedURLs = append(requestedURLs, req.URL.String())
		return updateReleaseResponse(), nil
	}))

	// 当前版本等于最新版本 → 无更新。
	info, err := s.CheckForUpdates("1.0.0", "")
	if err != nil {
		t.Fatalf("CheckForUpdates: %v", err)
	}
	if info.HasUpdate {
		t.Errorf("HasUpdate = true, want false (current == latest)")
	}
	if info.LatestVersion != "1.0.0" {
		t.Errorf("LatestVersion = %q, want %q", info.LatestVersion, "1.0.0")
	}
	if info.CurrentVersion != "1.0.0" {
		t.Errorf("CurrentVersion = %q, want %q", info.CurrentVersion, "1.0.0")
	}
	if info.ReleaseNotes != "release notes for 1.0.0" {
		t.Errorf("ReleaseNotes = %q, want %q", info.ReleaseNotes, "release notes for 1.0.0")
	}
	if info.ReleaseDate != "2024-01-01T00:00:00Z" {
		t.Errorf("ReleaseDate = %q, want %q", info.ReleaseDate, "2024-01-01T00:00:00Z")
	}
	if info.DownloadURL == "" {
		t.Errorf("DownloadURL should be populated from assets")
	}
	if !strings.HasSuffix(info.DownloadURL, "#sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Errorf("DownloadURL = %q, want verified SHA-256 fragment", info.DownloadURL)
	}

	// 当前版本高于最新版本 → 无更新。
	info2, err := s.CheckForUpdates("1.0.1", "https://api.github.com/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest")
	if err != nil {
		t.Fatalf("CheckForUpdates (newer current): %v", err)
	}
	if info2.HasUpdate {
		t.Errorf("HasUpdate = true, want false (current > latest)")
	}

	// 当前版本低于最新版本 → 有更新。
	info3, err := s.CheckForUpdates("0.9.9", "https://api.github.com/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest")
	if err != nil {
		t.Fatalf("CheckForUpdates (older current): %v", err)
	}
	if !info3.HasUpdate {
		t.Errorf("HasUpdate = false, want true (current < latest)")
	}
	if len(requestedURLs) != 3 || requestedURLs[0] != "https://api.github.com/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest" {
		t.Errorf("requested URLs = %v", requestedURLs)
	}
}

func TestUpdateService_CheckForUpdatesRejectsURLBeforeDoer(t *testing.T) {
	tests := []string{
		"http://api.github.com/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest",
		"https://user:pass@api.github.com/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest",
		"https://api.github.com:443/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest",
		"https://github.com/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest",
		"https://203.0.113.1/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest",
		"https://127.0.0.1/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest",
		"https://localhost/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest",
	}
	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			calls := 0
			s := NewUpdateService()
			s.setHTTPTransport(updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return updateReleaseResponse(), nil
			}))
			if _, err := s.CheckForUpdates("1.0.0", rawURL); err == nil {
				t.Fatalf("CheckForUpdates(%q) expected an error", rawURL)
			}
			if calls != 0 {
				t.Fatalf("doer calls = %d, want 0", calls)
			}
		})
	}
}

func TestUpdateService_CheckRedirectRejectsNonAPIHost(t *testing.T) {
	initial, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	redirect, err := http.NewRequest(http.MethodGet, "https://127.0.0.1/internal", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateUpdateCheckRedirect(redirect, []*http.Request{initial}); err == nil {
		t.Fatal("redirect to loopback should be rejected")
	}
}

func TestUpdateService_DownloadRedirectRejectsNonGitHubHost(t *testing.T) {
	initial, err := http.NewRequest(http.MethodGet, "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/download/v1/app.zip", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"https://127.0.0.1/internal",
		"https://example.com/update.zip",
		"https://release-assets.githubusercontent.com.evil.example/update.zip",
	} {
		redirect, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateUpdateRedirect(redirect, []*http.Request{initial}); err == nil {
			t.Errorf("redirect to %q should be rejected", target)
		}
	}
	allowed, err := http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/github-production-release-asset/app.zip", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateUpdateRedirect(allowed, []*http.Request{initial}); err != nil {
		t.Fatalf("GitHub release asset redirect rejected: %v", err)
	}
}

func TestUpdateService_InjectedTransportCannotBypassRedirectPolicy(t *testing.T) {
	var requests []string
	s := NewUpdateService()
	s.setLookupIP(publicGitHubTestResolver)
	s.setHTTPTransport(updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.String())
		if len(requests) == 1 {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://127.0.0.1/internal"}},
				Body:       io.NopCloser(strings.NewReader("redirect")),
				Request:    req,
			}, nil
		}
		return updateReleaseResponse(), nil
	}))

	if _, err := s.CheckForUpdates("1.0.0", "https://api.github.com/repos/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/latest"); err == nil {
		t.Fatal("CheckForUpdates expected redirect policy error")
	}
	if len(requests) != 1 {
		t.Fatalf("transport requests = %v, want only the validated initial request", requests)
	}
}

func TestUpdateService_InjectedResolverCannotApproveLoopback(t *testing.T) {
	calls := 0
	s := NewUpdateService()
	s.setLookupIP(func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	})
	s.setHTTPTransport(updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return updateReleaseResponse(), nil
	}))

	if _, err := s.CheckForUpdates("1.0.0", ""); err == nil || !strings.Contains(err.Error(), "private/loopback") {
		t.Fatalf("CheckForUpdates error = %v, want private/loopback rejection", err)
	}
	if calls != 0 {
		t.Fatalf("transport calls = %d, want 0", calls)
	}
}

func TestUpdateService_VerifyUpdateStreamsFileIntoHash(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "update_service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "VerifyUpdate" {
			body = fn.Body
			break
		}
	}
	if body == nil {
		t.Fatal("VerifyUpdate function not found")
	}
	var usesReadFile, usesOpen, usesCopy bool
	ast.Inspect(body, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		usesReadFile = usesReadFile || pkg.Name == "os" && selector.Sel.Name == "ReadFile"
		usesOpen = usesOpen || pkg.Name == "os" && selector.Sel.Name == "Open"
		usesCopy = usesCopy || pkg.Name == "io" && selector.Sel.Name == "Copy"
		return true
	})
	if usesReadFile || !usesOpen || !usesCopy {
		t.Fatalf("VerifyUpdate calls: os.ReadFile=%v os.Open=%v io.Copy=%v; want false, true, true", usesReadFile, usesOpen, usesCopy)
	}
}

func TestUpdateService_VerifyUpdatePreservesChecksumBehavior(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.bin")
	content := []byte("stream this update")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	expected := hex.EncodeToString(sum[:])
	s := NewUpdateService()
	if err := s.VerifyUpdate(path, strings.ToUpper(expected)); err != nil {
		t.Fatalf("VerifyUpdate valid checksum: %v", err)
	}
	if err := s.VerifyUpdate(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("VerifyUpdate mismatched checksum should fail")
	}
	if err := s.VerifyUpdate(path, "not-a-digest"); err == nil {
		t.Fatal("VerifyUpdate malformed checksum should fail")
	}
}

func TestUpdateService_DownloadUpdateRequiresAndVerifiesDigest(t *testing.T) {
	content := []byte("verified release package")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	downloadURL := "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/releases/download/v1.0.0/koyori-ide.zip"
	approve := func(s *UpdateService, checksum string) {
		s.downloadMu.Lock()
		s.approvedDownloads[downloadURL] = checksum
		s.downloadMu.Unlock()
	}

	t.Run("missing digest is rejected before network", func(t *testing.T) {
		calls := 0
		s := NewUpdateService()
		approve(s, strings.Repeat("0", 64))
		s.setHTTPTransport(updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return nil, nil
		}))
		if err := s.DownloadUpdate(downloadURL, t.TempDir()); err == nil {
			t.Fatal("DownloadUpdate without digest should fail")
		}
		if calls != 0 {
			t.Fatalf("network calls = %d, want 0", calls)
		}
	})

	t.Run("checksum mismatch leaves no installable file", func(t *testing.T) {
		destDir := t.TempDir()
		s := NewUpdateService()
		approve(s, strings.Repeat("0", 64))
		s.setHTTPTransport(updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(content)))}, nil
		}))
		if err := s.DownloadUpdate(downloadURL+"#sha256="+strings.Repeat("0", 64), destDir); err == nil {
			t.Fatal("DownloadUpdate with mismatched digest should fail")
		}
		if _, err := os.Stat(filepath.Join(destDir, "koyori-ide.zip")); !os.IsNotExist(err) {
			t.Fatalf("unverified destination exists: %v", err)
		}
	})

	t.Run("verified package is published for manual installation", func(t *testing.T) {
		destDir := t.TempDir()
		var requestedURL string
		s := NewUpdateService()
		approve(s, digest)
		s.setHTTPTransport(updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestedURL = req.URL.String()
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(content)))}, nil
		}))
		if err := s.DownloadUpdate(downloadURL+"#sha256="+digest, destDir); err != nil {
			t.Fatalf("DownloadUpdate: %v", err)
		}
		if requestedURL != downloadURL {
			t.Fatalf("requested URL = %q, want fragment-free %q", requestedURL, downloadURL)
		}
		got, err := os.ReadFile(filepath.Join(destDir, "koyori-ide.zip"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(content) {
			t.Fatalf("downloaded content = %q", got)
		}
	})

	t.Run("renderer-forged digest is rejected before network", func(t *testing.T) {
		calls := 0
		s := NewUpdateService()
		approve(s, digest)
		s.setHTTPTransport(updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return nil, nil
		}))
		if err := s.DownloadUpdate(downloadURL+"#sha256="+strings.Repeat("0", 64), t.TempDir()); err == nil {
			t.Fatal("DownloadUpdate with renderer-forged digest should fail")
		}
		if calls != 0 {
			t.Fatalf("network calls = %d, want 0", calls)
		}
	})
}

func TestUpdateService_E2ApplyUpdateAlwaysRejects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verified-package.zip")
	if err := os.WriteFile(path, []byte("package"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewUpdateService().ApplyUpdate(path); err == nil || !strings.Contains(err.Error(), "manual") {
		t.Fatalf("ApplyUpdate error = %v, want manual-install rejection", err)
	}
}

func TestUpdateService_P10_ReportCrash(t *testing.T) {
	s, dir := newTestCrashService(t)
	report := CrashReport{
		Timestamp: time.Now(),
		Message:   "nil pointer dereference",
		Stack:     "goroutine 1 [running]:\nmain.crash()\n\tmain.go:10",
		ErrorType: "panic",
	}
	if err := s.ReportCrash(report); err != nil {
		t.Fatalf("ReportCrash: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 crash file, got %d", len(entries))
	}
	name := entries[0].Name()
	if !strings.HasPrefix(name, crashFilePrefix) || !strings.HasSuffix(name, ".txt") {
		t.Errorf("unexpected crash filename: %s", name)
	}
}

func TestUpdateService_P10_GetCrashReports(t *testing.T) {
	s, _ := newTestCrashService(t)
	base := time.Now()
	for i := 0; i < 3; i++ {
		report := CrashReport{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Message:   "boom",
		}
		if err := s.ReportCrash(report); err != nil {
			t.Fatalf("ReportCrash[%d]: %v", i, err)
		}
	}
	reports, err := s.GetCrashReports()
	if err != nil {
		t.Fatalf("GetCrashReports: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("expected 3 reports, got %d", len(reports))
	}
	// 验证降序排列（最新在前）。
	for i := 1; i < len(reports); i++ {
		if reports[i].Timestamp.After(reports[i-1].Timestamp) {
			t.Errorf("reports not sorted descending at index %d", i)
		}
	}
	// 验证 Size > 0 与 Filename 非空。
	for _, r := range reports {
		if r.Filename == "" {
			t.Errorf("empty filename in report")
		}
		if r.Size <= 0 {
			t.Errorf("report %s size = %d, want > 0", r.Filename, r.Size)
		}
	}
}

func TestUpdateService_P10_GetCrashReport(t *testing.T) {
	s, _ := newTestCrashService(t)
	ts := time.Now()
	report := CrashReport{
		Timestamp: ts,
		Version:   "1.2.3",
		OS:        "linux",
		Stack:     "goroutine 1 [running]:\nmain.foo()\n\tmain.go:42",
		Message:   "index out of range",
		ErrorType: "runtime error",
	}
	if err := s.ReportCrash(report); err != nil {
		t.Fatalf("ReportCrash: %v", err)
	}
	reports, err := s.GetCrashReports()
	if err != nil {
		t.Fatalf("GetCrashReports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	got, err := s.GetCrashReport(reports[0].Filename)
	if err != nil {
		t.Fatalf("GetCrashReport: %v", err)
	}
	if got.Message != "index out of range" {
		t.Errorf("Message = %q, want %q", got.Message, "index out of range")
	}
	if got.ErrorType != "runtime error" {
		t.Errorf("ErrorType = %q, want %q", got.ErrorType, "runtime error")
	}
	if got.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", got.Version, "1.2.3")
	}
	if got.OS != "linux" {
		t.Errorf("OS = %q, want %q", got.OS, "linux")
	}
	if !strings.Contains(got.Stack, "main.foo()") {
		t.Errorf("Stack = %q, want contains %q", got.Stack, "main.foo()")
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, ts)
	}
	if got.Filename != reports[0].Filename {
		t.Errorf("Filename = %q, want %q", got.Filename, reports[0].Filename)
	}
}

func TestUpdateService_P10_DeleteCrashReport(t *testing.T) {
	s, dir := newTestCrashService(t)
	ts := time.Now()
	if err := s.ReportCrash(CrashReport{Timestamp: ts, Message: "x"}); err != nil {
		t.Fatalf("ReportCrash: %v", err)
	}
	reports, err := s.GetCrashReports()
	if err != nil {
		t.Fatalf("GetCrashReports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	filename := reports[0].Filename
	if err := s.DeleteCrashReport(filename); err != nil {
		t.Fatalf("DeleteCrashReport: %v", err)
	}
	// 文件应已删除。
	if _, err := os.Stat(filepath.Join(dir, filename)); !os.IsNotExist(err) {
		t.Errorf("file still exists after delete: %v", err)
	}
	// 列表应为空。
	reports2, err := s.GetCrashReports()
	if err != nil {
		t.Fatalf("GetCrashReports after delete: %v", err)
	}
	if len(reports2) != 0 {
		t.Errorf("expected 0 reports after delete, got %d", len(reports2))
	}
	// 重复删除不报错（幂等）。
	if err := s.DeleteCrashReport(filename); err != nil {
		t.Errorf("idempotent delete should not error: %v", err)
	}
}

func TestUpdateService_P10_ClearAllCrashReports(t *testing.T) {
	s, _ := newTestCrashService(t)
	base := time.Now()
	for i := 0; i < 3; i++ {
		if err := s.ReportCrash(CrashReport{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Message:   "boom",
		}); err != nil {
			t.Fatalf("ReportCrash[%d]: %v", i, err)
		}
	}
	reports, err := s.GetCrashReports()
	if err != nil {
		t.Fatalf("GetCrashReports: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("expected 3 reports before clear, got %d", len(reports))
	}
	if err := s.ClearAllCrashReports(); err != nil {
		t.Fatalf("ClearAllCrashReports: %v", err)
	}
	reports2, err := s.GetCrashReports()
	if err != nil {
		t.Fatalf("GetCrashReports after clear: %v", err)
	}
	if len(reports2) != 0 {
		t.Errorf("expected 0 reports after clear, got %d", len(reports2))
	}
	// 空目录再次清空不报错。
	if err := s.ClearAllCrashReports(); err != nil {
		t.Errorf("clear on empty dir should not error: %v", err)
	}
}

// P9-G08: GetCurrentVersion prefers the build-time-injected VERSION and falls
// back to build-info only when no injection is present.
func TestUpdateService_GetCurrentVersionPrefersInjectedAppVersion(t *testing.T) {
	previous := appVersion
	defer func() { appVersion = previous }()

	svc := &UpdateService{}
	SetAppVersion("9.9.9")
	if got := svc.GetCurrentVersion(); got != "9.9.9" {
		t.Fatalf("injected version = %q, want 9.9.9", got)
	}
	SetAppVersion(" 0.2.0+meta ")
	if got := svc.GetCurrentVersion(); got != "0.2.0+meta" {
		t.Fatalf("trimmed injected version = %q, want 0.2.0+meta", got)
	}
}
