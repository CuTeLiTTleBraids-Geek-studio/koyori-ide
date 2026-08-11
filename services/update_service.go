package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// update_service.go — 优先级 10: 更新检查与已校验包下载（E2）。
//
// 通过 GitHub Releases API 检查最新版本，下载更新包并校验 SHA-256。
// 安装、重启和回滚由用户手动完成。所有网络请求复用 marketplace_service 的 HTTP
// 模式（限制响应体大小、校验 URL scheme）。下载仅允许 HTTPS 且来自
// github.com 域名的资源，防止 SSRF 与中间人替换。

// githubReleasesLatestURL 是默认的 GitHub Releases latest API 端点。
// owner/repo 可通过 SetRepository 覆盖。
const githubReleasesLatestURL = "https://api.github.com/repos/%s/%s/releases/latest"

// githubDownloadHost is the public GitHub release URL host. GitHub currently
// redirects release assets to release-assets.githubusercontent.com.
const githubDownloadHost = "github.com"

const githubReleaseAssetHost = "release-assets.githubusercontent.com"

// maxUpdateDownloadSize 是单个更新包下载体积上限（256 MB）。
var maxUpdateDownloadSize = int64(256 * 1024 * 1024)

// UpdateInfo 描述一个可用的应用更新。
//
// 优先级 10 (prompt-1.md): 字段集对齐任务规范 —— HasUpdate / LatestVersion /
// CurrentVersion / ReleaseNotes / DownloadURL / ReleaseDate。旧字段 Version /
// ReleaseURL / PublishedAt 保留以向后兼容（omitempty）。
type UpdateInfo struct {
	HasUpdate      bool   `json:"hasUpdate"`
	LatestVersion  string `json:"latestVersion"`
	CurrentVersion string `json:"currentVersion"`
	ReleaseNotes   string `json:"releaseNotes"`
	DownloadURL    string `json:"downloadUrl"`
	ReleaseDate    string `json:"releaseDate"`
	// 旧字段（向后兼容）。
	Version     string `json:"version,omitempty"`
	ReleaseURL  string `json:"releaseUrl,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
}

// githubReleaseAsset 是 GitHub Releases API 中 assets 数组的一项。
type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

// githubReleaseResponse 是 GitHub Releases latest API 的响应子集。
type githubReleaseResponse struct {
	TagName     string               `json:"tag_name"`
	HTMLURL     string               `json:"html_url"`
	Body        string               `json:"body"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

// UpdateService 负责检查更新以及下载、校验安装包。
//
// 它通过 GitHub Releases API 查询最新版本，下载更新包并验证 SHA-256。
// E2 模式不自动安装或重启。下载 URL 必须是 HTTPS 且来自
// github.com 域名，防止 SSRF 与中间人替换。
type UpdateService struct {
	owner             string
	repo              string
	httpClient        *http.Client
	lookupIP          func(string) ([]net.IP, error)
	downloadMu        sync.Mutex
	approvedDownloads map[string]string
}

// NewUpdateService 创建一个指向 owner/repo 的 UpdateService。
// 默认使用 CuTeLiTTleBraids-Geek-studio/koyori-ide 仓库（可通过 SetRepository 覆盖）。
func NewUpdateService() *UpdateService {
	service := &UpdateService{
		owner:             "CuTeLiTTleBraids-Geek-studio",
		repo:              "koyori-ide",
		lookupIP:          net.LookupIP,
		approvedDownloads: make(map[string]string),
	}
	client := &http.Client{
		Timeout:       60 * time.Second,
		CheckRedirect: service.validateRedirect,
	}
	service.httpClient = client
	return service
}

// SetRepository 覆盖 GitHub 仓库的 owner 与 repo。
// 两者均不能为空，且不能包含路径分隔符（防止注入到 URL）。
func (s *UpdateService) SetRepository(owner, repo string) error {
	if owner == "" || repo == "" {
		return fmt.Errorf("owner and repo are required")
	}
	if strings.ContainsAny(owner, `/\`) || strings.Contains(owner, "..") {
		return fmt.Errorf("invalid owner %q", owner)
	}
	if strings.ContainsAny(repo, `/\`) || strings.Contains(repo, "..") {
		return fmt.Errorf("invalid repo %q", repo)
	}
	s.owner = owner
	s.repo = repo
	return nil
}

// setHTTPClient 覆盖内部 HTTP 客户端（测试用）。
//
//wails:ignore
func (s *UpdateService) setHTTPClient(c *http.Client) {
	if c != nil {
		owned := *c
		owned.CheckRedirect = s.validateRedirect
		s.httpClient = &owned
	}
}

// setHTTPTransport injects a one-hop transport for tests. Redirect ownership
// remains with the service's HTTP client and cannot be bypassed by the transport.
//
//wails:ignore
func (s *UpdateService) setHTTPTransport(transport http.RoundTripper) {
	if transport != nil {
		owned := *s.httpClient
		owned.Transport = transport
		owned.CheckRedirect = s.validateRedirect
		s.httpClient = &owned
	}
}

// setLookupIP injects deterministic DNS for tests. Production always uses
// net.LookupIP and still rejects every private/loopback result.
//
//wails:ignore
func (s *UpdateService) setLookupIP(lookup func(string) ([]net.IP, error)) {
	if lookup != nil {
		s.lookupIP = lookup
	}
}

func validateGitHubReleaseAPIURLWithLookup(rawURL string, lookup func(string) ([]net.IP, error)) (*url.URL, error) {
	if err := ValidateBaseURL(rawURL); err != nil {
		return nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("update URL must use https")
	}
	if u.User != nil {
		return nil, fmt.Errorf("update URL must not contain credentials")
	}
	if !strings.EqualFold(u.Hostname(), "api.github.com") || u.Port() != "" {
		return nil, fmt.Errorf("update URL host must be exactly api.github.com with no explicit port")
	}
	if lookup == nil {
		return nil, fmt.Errorf("update URL resolver is unavailable")
	}
	ips, err := lookup(u.Hostname())
	if err != nil {
		return nil, fmt.Errorf("resolve update host %q: %w", u.Hostname(), err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("update host %q resolved to no addresses", u.Hostname())
	}
	for _, ip := range ips {
		if isPrivateHost(ip) {
			return nil, fmt.Errorf("update host %q resolves to private/loopback/link-local address %s", u.Hostname(), ip)
		}
	}
	return u, nil
}

func validateGitHubReleaseAPIURL(rawURL string) (*url.URL, error) {
	return validateGitHubReleaseAPIURLWithLookup(rawURL, net.LookupIP)
}

func validateUpdateCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if len(via) == 0 || !strings.EqualFold(via[0].URL.Hostname(), "api.github.com") {
		return nil
	}
	if _, err := validateGitHubReleaseAPIURL(req.URL.String()); err != nil {
		return fmt.Errorf("update redirect blocked: %w", err)
	}
	return nil
}

func validateUpdateRedirect(req *http.Request, via []*http.Request) error {
	return validateUpdateRedirectWithLookup(req, via, net.LookupIP)
}

func validateUpdateRedirectWithLookup(req *http.Request, via []*http.Request, lookup func(string) ([]net.IP, error)) error {
	if len(via) == 0 {
		return nil
	}
	if strings.EqualFold(via[0].URL.Hostname(), "api.github.com") {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if _, err := validateGitHubReleaseAPIURLWithLookup(req.URL.String(), lookup); err != nil {
			return fmt.Errorf("update redirect blocked: %w", err)
		}
		return nil
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if err := validateGitHubDownloadURL(req.URL.String()); err != nil {
		return fmt.Errorf("update download redirect blocked: %w", err)
	}
	return nil
}

func (s *UpdateService) validateRedirect(req *http.Request, via []*http.Request) error {
	return validateUpdateRedirectWithLookup(req, via, s.lookupIP)
}

// CompareVersions 对比语义化版本号 current 与 latest。
// 返回 -1 表示 current < latest，0 表示相等，1 表示 current > latest。
// 支持预发布后缀（如 1.0.0-beta < 1.0.0）。无法解析时按字符串字典序回退比较。
func (s *UpdateService) CompareVersions(current, latest string) int {
	return compareSemVer(current, latest)
}

// CheckForUpdates 查询 GitHub Releases latest 端点，返回最新版本信息。
// currentVersion 为当前应用版本；updateURL 非空时直接使用该 URL，否则按
// owner/repo 构造默认 GitHub Releases latest 端点。返回的 UpdateInfo 中
// HasUpdate 由 CompareVersions(currentVersion, latestVersion) 决定。
// 返回的 DownloadURL 是首个资产的 browser_download_url；若无资产则为空。
func (s *UpdateService) CheckForUpdates(currentVersion string, updateURL string) (*UpdateInfo, error) {
	reqURL := updateURL
	if reqURL == "" {
		reqURL = fmt.Sprintf(githubReleasesLatestURL, s.owner, s.repo)
	}
	if _, err := validateGitHubReleaseAPIURLWithLookup(reqURL, s.lookupIP); err != nil {
		return nil, fmt.Errorf("invalid update URL: %w", err)
	}
	data, err := s.httpGetJSON(reqURL)
	if err != nil {
		return nil, fmt.Errorf("check for updates: %w", err)
	}
	var rel githubReleaseResponse
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, fmt.Errorf("parse release response: %w", err)
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	info := &UpdateInfo{
		HasUpdate:      compareSemVer(currentVersion, latest) < 0,
		LatestVersion:  latest,
		CurrentVersion: currentVersion,
		ReleaseNotes:   rel.Body,
		ReleaseDate:    rel.PublishedAt,
		// 旧字段同步填充以保持向后兼容。
		Version:     latest,
		ReleaseURL:  rel.HTMLURL,
		PublishedAt: rel.PublishedAt,
	}
	approvedDownloads := make(map[string]string)
	for _, asset := range rel.Assets {
		checksum, err := parseUpdateDigest(asset.Digest)
		if err != nil || asset.BrowserDownloadURL == "" {
			continue
		}
		u, err := url.Parse(asset.BrowserDownloadURL)
		if err != nil {
			continue
		}
		// Carry GitHub's release-asset digest to DownloadUpdate without adding
		// another renderer-controlled argument. URL fragments are never sent.
		u.Fragment = "sha256=" + checksum
		info.DownloadURL = u.String()
		u.Fragment = ""
		approvedDownloads[u.String()] = checksum
		break
	}
	s.downloadMu.Lock()
	s.approvedDownloads = approvedDownloads
	s.downloadMu.Unlock()
	return info, nil
}

// DownloadUpdate 下载并校验更新包。downloadURL 必须包含由 CheckForUpdates
// 附加的 SHA-256 fragment；fragment 不会发送到网络。校验成功前只写临时文件，
// 校验失败不会在 destPath 留下可供安装的包。destPath 可为文件或已有目录。
func (s *UpdateService) DownloadUpdate(downloadURL string, destPath string) error {
	u, err := url.Parse(downloadURL)
	if err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}
	expectedChecksum, err := parseUpdateDigest(strings.TrimPrefix(u.Fragment, "sha256="))
	if err != nil || !strings.HasPrefix(u.Fragment, "sha256=") {
		return fmt.Errorf("verified update download requires a valid SHA-256 digest")
	}
	u.Fragment = ""
	requestURL := u.String()
	if err := validateGitHubDownloadURL(requestURL); err != nil {
		return err
	}
	s.downloadMu.Lock()
	approvedChecksum, approved := s.approvedDownloads[requestURL]
	s.downloadMu.Unlock()
	if !approved || approvedChecksum != expectedChecksum {
		return fmt.Errorf("update download was not approved by the latest release check")
	}
	resp, err := s.httpGet(requestURL, "")
	if err != nil {
		return fmt.Errorf("download update: %w", err)
	}
	dest := destPath
	if dest == "" {
		name := filepath.Base(u.Path)
		if name == "" || name == "/" || name == "." {
			name = "update-package"
		}
		tmpDir, err := os.MkdirTemp("", "koyori-ide-update-*")
		if err != nil {
			return fmt.Errorf("create temp dir: %w", err)
		}
		dest = filepath.Join(tmpDir, name)
	} else if info, statErr := os.Stat(dest); statErr == nil && info.IsDir() {
		dest = filepath.Join(dest, filepath.Base(u.Path))
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect update destination: %w", statErr)
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("update destination already exists: %s", dest)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect update destination: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".koyori-ide-update-*")
	if err != nil {
		return fmt.Errorf("create temporary update file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(resp); err != nil {
		tmp.Close()
		return fmt.Errorf("write update file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close update file: %w", err)
	}
	if err := s.VerifyUpdate(tmpPath, expectedChecksum); err != nil {
		return fmt.Errorf("downloaded update rejected: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("publish verified update file: %w", err)
	}
	return nil
}

// VerifyUpdate 校验下载文件的 SHA-256 是否与 expectedChecksum 一致。
// expectedChecksum 为 64 位十六进制字符串（大小写不敏感）。
func (s *UpdateService) VerifyUpdate(filePath string, expectedChecksum string) error {
	expectedChecksum, err := parseUpdateDigest(expectedChecksum)
	if err != nil {
		return err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("read update file: %w", err)
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return fmt.Errorf("read update file: %w", err)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(got, expectedChecksum) {
		return fmt.Errorf("SHA-256 verification failed: expected %s, got %s", expectedChecksum, got)
	}
	return nil
}

// ApplyUpdate is intentionally unavailable in E2 mode. Koyori IDE can download
// and verify a package, but installation, restart, and rollback remain manual.
func (s *UpdateService) ApplyUpdate(filePath string) error {
	slog.Warn("automatic update installation rejected; manual installation is required", "path", filePath)
	return fmt.Errorf("automatic installation is unavailable; install the verified package manually")
}

// appVersion is injected by the desktop entry point (main.go embeds the
// repository VERSION file). An empty value means no build-time injection is
// available and GetCurrentVersion falls back to Go build-info.
var appVersion string

// SetAppVersion records the build-time-injected application version. It is
// called once during startup from the embed of the VERSION file.
//
//wails:ignore
func SetAppVersion(version string) {
	appVersion = strings.TrimSpace(version)
}

// GetCurrentVersion 返回当前应用版本。
// 优先从构建信息（debug.ReadBuildInfo）读取 vcs.revision 或模块版本；
// 读取失败时返回 "dev"。
func (s *UpdateService) GetCurrentVersion() string {
	if appVersion != "" {
		return appVersion
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	// 主模块版本（go install 构建时携带）。
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	// 本地构建（go build）时 Main.Version 为 "(devel)"，回退到 vcs.revision。
	for _, setting := range bi.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			// 取前 12 位作为短哈希版本号。
			if len(setting.Value) > 12 {
				return setting.Value[:12]
			}
			return setting.Value
		}
	}
	return "dev"
}

// --- internal helpers ---

// httpGetJSON 获取 JSON 文档（设置 Accept 头）。
func (s *UpdateService) httpGetJSON(reqURL string) ([]byte, error) {
	return s.httpGet(reqURL, "application/vnd.github+json")
}

// httpGet 发起 GET 请求并返回响应体字节，限制最大体积防止 OOM (H-2)。
func (s *UpdateService) httpGet(reqURL, accept string) ([]byte, error) {
	client := s.httpClient
	if client == nil {
		client = &http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: s.validateRedirect,
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "koyori-ide-updater/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpdateDownloadSize+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(data)) > maxUpdateDownloadSize {
		return nil, fmt.Errorf("response body exceeds max size %d bytes", maxUpdateDownloadSize)
	}
	return data, nil
}

// compareSemVer 对比两个语义化版本字符串。
// 返回 -1 表示 a < b，0 表示相等，1 表示 a > b。
// 规则：
//   - 去掉前导 "v"。
//   - 以 "." 分割主版本号各段，逐段按整数比较；缺失段视为 0。
//   - 预发布后缀（"-" 之后的部分）使版本低于无后缀的同版本（1.0.0-beta < 1.0.0）。
//   - 都有预发布后缀时按字典序比较后缀。
//   - 解析失败的段按字符串字典序回退比较。
func compareSemVer(a, b string) int {
	a = strings.TrimPrefix(strings.TrimSpace(a), "v")
	b = strings.TrimPrefix(strings.TrimSpace(b), "v")
	if a == b {
		return 0
	}
	aCore, aPre := splitPreRelease(a)
	bCore, bPre := splitPreRelease(b)
	if c := compareCore(aCore, bCore); c != 0 {
		return c
	}
	// 同主版本下：无预发布 > 有预发布。
	if aPre == "" && bPre == "" {
		return 0
	}
	if aPre == "" {
		return 1
	}
	if bPre == "" {
		return -1
	}
	// 都有预发布：按字典序比较。
	if aPre < bPre {
		return -1
	}
	if aPre > bPre {
		return 1
	}
	return 0
}

// splitPreRelease 将版本号拆分为核心版本与预发布后缀。
// "1.0.0-beta" → ("1.0.0", "beta")；"1.0.0" → ("1.0.0", "")。
func splitPreRelease(v string) (core, pre string) {
	if idx := strings.Index(v, "-"); idx >= 0 {
		return v[:idx], v[idx+1:]
	}
	return v, ""
}

// compareCore 对比 "." 分割的版本号核心段。
func compareCore(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	n := len(aParts)
	if len(bParts) > n {
		n = len(bParts)
	}
	for i := 0; i < n; i++ {
		av := "0"
		bv := "0"
		if i < len(aParts) {
			av = aParts[i]
		}
		if i < len(bParts) {
			bv = bParts[i]
		}
		ai, aerr := strconv.Atoi(av)
		bi, berr := strconv.Atoi(bv)
		if aerr != nil || berr != nil {
			// 非数字段按字典序回退比较。
			if av < bv {
				return -1
			}
			if av > bv {
				return 1
			}
			continue
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

// validateGitHubDownloadURL 校验下载 URL 是 HTTPS 且来自 github.com。
// 这防止 SSRF（指向内部地址）与中间人替换（非 HTTPS、非 GitHub 域名）。
func validateGitHubDownloadURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("download URL is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}
	if strings.ToLower(u.Scheme) != "https" {
		return fmt.Errorf("download URL must use https, got %q", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host != githubDownloadHost && !strings.HasSuffix(host, "."+githubDownloadHost) && host != githubReleaseAssetHost {
		return fmt.Errorf("download URL must be from GitHub Releases, got %q", host)
	}
	if u.Port() != "" {
		return fmt.Errorf("download URL must not contain an explicit port")
	}
	if u.User != nil {
		return fmt.Errorf("download URL must not contain embedded credentials")
	}
	return nil
}

func parseUpdateDigest(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(strings.ToLower(raw), "sha256:")
	if len(raw) != sha256.Size*2 {
		return "", fmt.Errorf("expected checksum must be a 64-character SHA-256 digest")
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return "", fmt.Errorf("expected checksum must be hexadecimal: %w", err)
	}
	return raw, nil
}
