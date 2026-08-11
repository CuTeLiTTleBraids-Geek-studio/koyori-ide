package services

import (
	"archive/zip"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// marketplace_service.go — G-VSC-01: VS Code extension marketplace client.
//
// This service searches, browses, downloads, verifies, and installs VS Code
// extensions (VSIX packages) from a registry. The default registry is the
// Open VSX Registry (https://open-vsx.org/vscode/registry/api), which has no
// legal restrictions on programmatic access. The VS Code Marketplace is
// optional and requires explicit user opt-in (set via SetRegistryURL).
//
// Security (G-SEC-12):
//   - requirement 2: newly installed extensions are disabled by default.
//   - requirement 3: every downloaded VSIX is verified against a registry-
//     provided SHA-256 hash; a mismatch aborts the install.
//   - All extracted paths are validated to stay within the target directory
//     (path traversal protection) — see extractVSIX.

// defaultOpenVSXRegistryAPI is the default registry base URL (Open VSX).
// Open VSX has no legal restrictions on API use, unlike the VS Code
// Marketplace whose ToS require the official client.
// NOTE: The correct API base is https://open-vsx.org/api (NOT
// https://open-vsx.org/vscode/registry/api, which 404s on /-/search).
const defaultOpenVSXRegistryAPI = "https://open-vsx.org/api"

// maxHTTPResponseSize is the upper bound on a single HTTP response body
// read into memory (H-2). VSIX packages can be large (popular extensions
// reach 100+ MB), so 256 MB is a generous ceiling that still prevents OOM
// from a malicious or buggy server streaming an unbounded body.
// It is a var (not const) so tests can temporarily lower it.
var maxHTTPResponseSize = int64(256 * 1024 * 1024)

// extensionsSubdir is the on-disk directory for installed VSIX extensions,
// relative to the config dir: <configDir>/koyori-ide/extensions/<publisher>.<name>/
const extensionsSubdir = "koyori-ide/extensions"

// extensionsStateFileName is the persisted enabled/disabled state file for
// VS Code extensions, written under <configDir>/koyori-ide/.
const extensionsStateFileName = "extensions-state.json"

// installedExtMetaFile is the small metadata file written into each
// installed extension directory recording the installed version. It lets
// ListInstalledExtensions report the version without re-parsing package.json
// (which may not carry the exact installed version after updates).
const installedExtMetaFile = "koyori-ide-ext.json"

// vsixExtensionPrefix is the path prefix inside a VSIX zip for the
// extension payload (VSIX packages place package.json under extension/).
const vsixExtensionPrefix = "extension/"

// vsixDownloadTimeout is the per-request deadline for streaming a VSIX
// package to disk. VSIX files can be large (100+ MB) and the shared
// httpClient's 60s Timeout covers body reading, which aborts big downloads
// mid-stream with "context deadline exceeded". downloadVSIXToTempFile
// therefore uses a dedicated client without the overall Timeout and relies
// on this context deadline instead.
const vsixDownloadTimeout = 10 * time.Minute

// MarketplaceService searches, downloads, verifies, and installs VS Code
// extensions (VSIX) from a registry. The default registry is Open VSX.
type MarketplaceService struct {
	mu          sync.Mutex
	updateMu    sync.Mutex
	configDir   string
	registryURL string
	httpClient  *http.Client
	// G-SEC-12: securityService is used to register installs and check
	// the blacklist before installation. Without this, marketplace installs
	// bypass security classification.
	securityService *ExtensionSecurityService
	// F-3 (prompt-2.md): activationService 接收安装/卸载事件以注册/注销
	// 扩展的 activationEvents。可选依赖；为 nil 时安装流程跳过激活注册。
	activationService *ActivationService
	lifecycleMu       sync.Mutex
	lifecycleRequest  ExtensionLifecycleRequester
	lifecyclePending  map[string]lifecyclePendingRequest
	lifecycleCancel   func()
}

var extensionLifecycleTimeout = 10 * time.Second
var extensionUninstallTimeout = 10 * time.Second

// ExtensionLifecycleRequest is the backend-to-renderer part of the extension
// replacement handshake. The renderer owns Workers and its manifest caches;
// the backend owns the on-disk transaction.
type ExtensionLifecycleRequest struct {
	RequestID   string `json:"requestId"`
	ExtensionID string `json:"extensionId"`
	Publisher   string `json:"publisher"`
	Name        string `json:"name"`
	Action      string `json:"action"`
	WasActive   bool   `json:"wasActive"`
}

// ExtensionLifecycleResult is emitted by the renderer after it has completed
// the requested Worker/cache operation.
type ExtensionLifecycleResult struct {
	RequestID   string `json:"requestId"`
	ExtensionID string `json:"extensionId"`
	Publisher   string `json:"publisher"`
	Name        string `json:"name"`
	Action      string `json:"action"`
	OK          bool   `json:"ok"`
	WasActive   bool   `json:"wasActive"`
	Warning     string `json:"warning,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ExtensionLifecycleRequester allows unit tests and alternate hosts to inject
// the renderer handshake without constructing a Wails application.
type ExtensionLifecycleRequester func(context.Context, ExtensionLifecycleRequest) (ExtensionLifecycleResult, error)

type lifecyclePendingRequest struct {
	request ExtensionLifecycleRequest
	result  chan ExtensionLifecycleResult
	expires time.Time
}

// NewMarketplaceService constructs a MarketplaceService rooted at configDir.
// Installed extensions live under <configDir>/koyori-ide/extensions/. The
// registry defaults to Open VSX; call SetRegistryURL to switch (e.g. to the
// VS Code Marketplace after user opt-in).
func NewMarketplaceService(configDir string) *MarketplaceService {
	return &MarketplaceService{
		configDir:   configDir,
		registryURL: defaultOpenVSXRegistryAPI,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		lifecyclePending: make(map[string]lifecyclePendingRequest),
	}
}

// setExtensionLifecycleRequester injects the renderer handshake. A nil
// requester means that the service runs in standalone/test mode and treats
// renderer lifecycle work as a successful no-op.
//
//wails:ignore
func (s *MarketplaceService) setExtensionLifecycleRequester(requester ExtensionLifecycleRequester) {
	s.lifecycleMu.Lock()
	s.lifecycleRequest = requester
	s.lifecycleMu.Unlock()
}

// setApp connects the service to the application event bus. The listener is
// installed before any marketplace RPC can request a Worker stop.
//
//wails:ignore
func (s *MarketplaceService) setApp(app *application.App) {
	s.lifecycleMu.Lock()
	previousCancel := s.lifecycleCancel
	s.lifecycleCancel = nil
	if app != nil {
		if s.lifecyclePending == nil {
			s.lifecyclePending = make(map[string]lifecyclePendingRequest)
		}
		s.lifecycleRequest = func(ctx context.Context, request ExtensionLifecycleRequest) (ExtensionLifecycleResult, error) {
			return s.requestLifecycleWithApp(ctx, app, request)
		}
		s.lifecycleCancel = app.Event.On(extensionLifecycleResultEvent, func(event *application.CustomEvent) {
			result, ok := decodeExtensionLifecycleResult(event.Data)
			if ok {
				s.acceptExtensionLifecycleResult(result)
			}
		})
	} else {
		s.lifecycleRequest = nil
	}
	s.lifecycleMu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}
}

func (s *MarketplaceService) acceptExtensionLifecycleResult(result ExtensionLifecycleResult) bool {
	s.lifecycleMu.Lock()
	pending, exists := s.lifecyclePending[result.RequestID]
	expired := exists && !pending.expires.IsZero() && !time.Now().Before(pending.expires)
	if !exists || pending.result == nil || expired || validateExtensionLifecycleResult(pending.request, result) != nil {
		if expired {
			delete(s.lifecyclePending, result.RequestID)
		}
		s.lifecycleMu.Unlock()
		return false
	}
	delete(s.lifecyclePending, result.RequestID)
	s.lifecycleMu.Unlock()
	pending.result <- result
	return true
}

func (s *MarketplaceService) requestLifecycleWithApp(
	ctx context.Context,
	app *application.App,
	request ExtensionLifecycleRequest,
) (ExtensionLifecycleResult, error) {
	resultCh := make(chan ExtensionLifecycleResult, 1)
	expires := time.Now().Add(extensionLifecycleTimeout)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(expires) {
		expires = deadline
	}
	s.lifecycleMu.Lock()
	if _, exists := s.lifecyclePending[request.RequestID]; exists {
		s.lifecycleMu.Unlock()
		return ExtensionLifecycleResult{}, fmt.Errorf("duplicate extension lifecycle request ID")
	}
	s.lifecyclePending[request.RequestID] = lifecyclePendingRequest{
		request: request,
		result:  resultCh,
		expires: expires,
	}
	s.lifecycleMu.Unlock()
	if app.Event.Emit(extensionLifecycleRequestEvent, request) {
		s.lifecycleMu.Lock()
		delete(s.lifecyclePending, request.RequestID)
		s.lifecycleMu.Unlock()
		return ExtensionLifecycleResult{}, fmt.Errorf("extension lifecycle request was cancelled")
	}
	timer := time.NewTimer(extensionLifecycleTimeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		return result, nil
	case <-ctx.Done():
		s.lifecycleMu.Lock()
		delete(s.lifecyclePending, request.RequestID)
		s.lifecycleMu.Unlock()
		return ExtensionLifecycleResult{}, ctx.Err()
	case <-timer.C:
		s.lifecycleMu.Lock()
		delete(s.lifecyclePending, request.RequestID)
		s.lifecycleMu.Unlock()
		return ExtensionLifecycleResult{}, fmt.Errorf("extension lifecycle request timed out")
	}
}

func decodeExtensionLifecycleResult(raw any) (ExtensionLifecycleResult, bool) {
	if result, ok := raw.(ExtensionLifecycleResult); ok {
		return result, true
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return ExtensionLifecycleResult{}, false
	}
	var result ExtensionLifecycleResult
	if err := json.Unmarshal(data, &result); err != nil || result.RequestID == "" {
		return ExtensionLifecycleResult{}, false
	}
	return result, true
}

func newExtensionLifecycleRequestID() string {
	var bytes [16]byte
	if _, err := cryptorand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("lifecycle-%d", time.Now().UnixNano())
}

func (s *MarketplaceService) requestExtensionLifecycle(
	ctx context.Context,
	request ExtensionLifecycleRequest,
) (ExtensionLifecycleResult, error) {
	if request.RequestID == "" {
		request.RequestID = newExtensionLifecycleRequestID()
	}
	if err := validateExtensionLifecycleRequest(request); err != nil {
		return ExtensionLifecycleResult{}, err
	}
	s.lifecycleMu.Lock()
	requester := s.lifecycleRequest
	s.lifecycleMu.Unlock()
	if requester == nil {
		return ExtensionLifecycleResult{
			RequestID: request.RequestID, ExtensionID: request.ExtensionID,
			Publisher: request.Publisher, Name: request.Name,
			Action: request.Action, OK: true, WasActive: request.WasActive,
		}, nil
	}
	type lifecycleResponse struct {
		result ExtensionLifecycleResult
		err    error
	}
	response := make(chan lifecycleResponse, 1)
	go func() {
		result, err := requester(ctx, request)
		select {
		case response <- lifecycleResponse{result: result, err: err}:
		case <-ctx.Done():
		}
	}()
	select {
	case <-ctx.Done():
		return ExtensionLifecycleResult{}, ctx.Err()
	case reply := <-response:
		if reply.err != nil {
			return ExtensionLifecycleResult{}, reply.err
		}
		if err := validateExtensionLifecycleResult(request, reply.result); err != nil {
			return ExtensionLifecycleResult{}, err
		}
		return reply.result, nil
	}
}

func validateExtensionLifecycleRequest(request ExtensionLifecycleRequest) error {
	if request.RequestID == "" {
		return fmt.Errorf("extension lifecycle request ID is empty")
	}
	if err := validateExtensionIdent(request.Publisher, request.Name); err != nil {
		return fmt.Errorf("invalid extension lifecycle identity: %w", err)
	}
	if request.ExtensionID != extensionStateKey(request.Publisher, request.Name) {
		return fmt.Errorf("extension lifecycle identity mismatch")
	}
	switch request.Action {
	case "stop", "restore", "invalidate", "commit":
		return nil
	default:
		return fmt.Errorf("unsupported extension lifecycle action %q", request.Action)
	}
}

func validateExtensionLifecycleResult(
	request ExtensionLifecycleRequest,
	result ExtensionLifecycleResult,
) error {
	if result.RequestID != request.RequestID {
		return fmt.Errorf("extension lifecycle result request ID mismatch")
	}
	if result.ExtensionID != request.ExtensionID ||
		result.Publisher != request.Publisher ||
		result.Name != request.Name ||
		result.Action != request.Action {
		return fmt.Errorf("extension lifecycle result identity mismatch")
	}
	return nil
}

func (s *MarketplaceService) finishExtensionLifecycle(
	ctx context.Context,
	request ExtensionLifecycleRequest,
) error {
	result, err := s.requestExtensionLifecycle(ctx, request)
	if err != nil {
		return err
	}
	if !result.OK {
		if result.Error == "" {
			return fmt.Errorf("renderer rejected extension lifecycle action %q", request.Action)
		}
		return fmt.Errorf("renderer lifecycle action %q failed: %s", request.Action, result.Error)
	}
	return nil
}

// setSecurityService injects the ExtensionSecurityService so marketplace
// installs can be registered for security classification and blacklist
// checking (G-SEC-12).
//
//wails:ignore
func (s *MarketplaceService) setSecurityService(ss *ExtensionSecurityService) {
	s.mu.Lock()
	s.securityService = ss
	s.mu.Unlock()
}

// setActivationService 注入 ActivationService（F-3, prompt-2.md）。安装扩展时
// marketplace 会调用 ActivationService.RegisterExtension 注册扩展声明的
// activationEvents，卸载时调用 UnregisterExtension 注销。可选依赖；为 nil
// 时安装/卸载流程跳过激活注册（向后兼容）。
//
//wails:ignore
func (s *MarketplaceService) setActivationService(as *ActivationService) {
	s.mu.Lock()
	s.activationService = as
	s.mu.Unlock()
}

// SetRegistryURL overrides the registry base URL. Used to opt in to the VS
// Code Marketplace API (the user must consent, as its ToS restrict programmatic
// access to the official client). Pass an empty string to reset to Open VSX.
//
// H-3 + N-7: the URL is validated via ValidateNonPrivateURL before being
// stored, rejecting non-http(s) schemes, embedded credentials, non-loopback
// plain http, AND private/loopback/link-local IPs (including "localhost").
// This prevents users from pointing the marketplace at internal servers
// (SSRF vector: the marketplace HTTP client would probe internal endpoints
// even without Authorization headers). Returns an error if the URL is
// invalid (the stored registry URL is left unchanged on error).
func (s *MarketplaceService) SetRegistryURL(url string) error {
	if url == "" {
		s.mu.Lock()
		s.registryURL = defaultOpenVSXRegistryAPI
		s.mu.Unlock()
		return nil
	}
	// N-7: ValidateNonPrivateURL does everything ValidateBaseURL does, plus
	// DNS resolution + private/loopback/link-local IP rejection.
	if _, err := ValidateNonPrivateURL(url); err != nil {
		return fmt.Errorf("invalid registry URL: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registryURL = strings.TrimRight(url, "/")
	return nil
}

// ExtensionSearchResult is a single hit from a registry search.
type ExtensionSearchResult struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	DisplayName   string  `json:"displayName"`
	Publisher     string  `json:"publisher"`
	Description   string  `json:"description"`
	Version       string  `json:"version"`
	Rating        float64 `json:"rating"`
	RatingCount   int     `json:"ratingCount"`
	DownloadCount int     `json:"downloadCount"`
	IconURL       string  `json:"iconUrl"`
}

// ExtensionVersion is a single published version of an extension.
type ExtensionVersion struct {
	Version     string `json:"version"`
	DownloadURL string `json:"downloadUrl"`
	Date        string `json:"date"`
}

// ExtensionDetail is the full metadata for a single extension.
type ExtensionDetail struct {
	ExtensionSearchResult
	Categories []string           `json:"categories"`
	Tags       []string           `json:"tags"`
	License    string             `json:"license"`
	Repository string             `json:"repository"`
	Readme     string             `json:"readme"`
	Versions   []ExtensionVersion `json:"versions"`
}

// InstalledExtension is a locally installed VS Code extension.
type InstalledExtension struct {
	Publisher string `json:"publisher"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Path      string `json:"path"`
	Enabled   bool   `json:"enabled"`
}

// VSCodeExtensionManifest is the subset of extension/package.json parsed
// after extraction (G-VSC-01 Step 3). Unknown fields are ignored.
//
// F-3 (prompt-2.md): ParsedContributes 是 Contributes (json.RawMessage) 解析
// 后的结构化形式，由 parseVSIXManifest 在解析时填充，方便前端/扩展宿主直接
// 使用 contributes.commands / views / grammars / snippets 等而无需二次解析。
// Contributes 原始字段保留，便于未知贡献点的透明透传。
type VSCodeExtensionManifest struct {
	Name             string            `json:"name"`
	Publisher        string            `json:"publisher"`
	Version          string            `json:"version"`
	DisplayName      string            `json:"displayName"`
	Description      string            `json:"description"`
	Engines          map[string]string `json:"engines"`
	ActivationEvents []string          `json:"activationEvents"`
	Contributes      json.RawMessage   `json:"contributes"`
	Capabilities     json.RawMessage   `json:"capabilities"`
	Main             string            `json:"main,omitempty"`
	Browser          string            `json:"browser,omitempty"`
	KoyoriIde        *struct {
		Permissions *[]ExtensionPermission `json:"permissions"`
	} `json:"koyoriIde,omitempty"`
	// F-3: 结构化 contributes，由 parseVSIXManifest 填充。前端扩展宿主直接
	// 使用此字段注入命令面板/侧边栏/Monaco。与 Contributes (json.RawMessage)
	// 并存：Contributes 保留原始 JSON 供安全分类等场景透传，ParsedContributes
	// 提供类型化访问。两者内容等价，前端优先用 ParsedContributes。
	ParsedContributes ExtensionContributes `json:"parsedContributes,omitempty"`
}

func (m *VSCodeExtensionManifest) RequestedPermissions() ([]ExtensionPermission, error) {
	hasExecutableMain := m != nil && (strings.TrimSpace(m.Main) != "" || strings.TrimSpace(m.Browser) != "")
	if m == nil || m.KoyoriIde == nil || m.KoyoriIde.Permissions == nil {
		if hasExecutableMain {
			return nil, fmt.Errorf("executable extensions must declare koyoriIde.permissions explicitly")
		}
		return []ExtensionPermission{}, nil
	}
	return validateExtensionPermissions(*m.KoyoriIde.Permissions)
}

// ExtensionID 返回 "publisher.name" 形式的扩展 ID。如果 publisher 或 name
// 为空则返回空字符串。F-3 (prompt-2.md)。
func (m *VSCodeExtensionManifest) ExtensionID() string {
	if m == nil || m.Publisher == "" || m.Name == "" {
		return ""
	}
	return m.Publisher + "." + m.Name
}

// mpExtensionStateEntry is one row in the marketplace's persisted
// enabled/disabled state file (extensions-state.json). This is intentionally
// separate from ExtensionSecurityService's extensionStateEntry, which tracks
// the richer G-VSC-03 security classification in extension-security.json.
type mpExtensionStateEntry struct {
	Enabled bool `json:"enabled"`
}

// mpExtensionStateFile is the on-disk shape of extensions-state.json.
type mpExtensionStateFile struct {
	Extensions map[string]mpExtensionStateEntry `json:"extensions"`
}

// installedExtMeta is the on-disk shape of koyori-ide-ext.json.
type installedExtMeta struct {
	Publisher string `json:"publisher"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

// --- Open VSX API response shapes (subset) ---

// ovsxFileMap maps file roles (download/readme/icon/license) to URLs within
// an Open VSX version entry. The sha256 key carries the download hash.
type ovsxFileMap map[string]string

// ovsxVersion is a single version entry in the Open VSX response.
type ovsxVersion struct {
	Version   string      `json:"version"`
	Timestamp string      `json:"timestamp"`
	Files     ovsxFileMap `json:"files"`
}

// ovsxExtension is the Open VSX extension object (search hit or detail).
// Note: the detail endpoint (/api/{publisher}/{name}) returns the latest
// version's metadata at the top level (Version + Files), NOT in a versions
// array. The search endpoint returns a versions array. Callers must use
// normalizeVersions() to handle both shapes.
type ovsxExtension struct {
	Name          string        `json:"name"`
	Namespace     string        `json:"namespace"`
	DisplayName   string        `json:"displayName"`
	Description   string        `json:"description"`
	Version       string        `json:"version"`
	Timestamp     string        `json:"timestamp"`
	License       string        `json:"license"`
	Repository    string        `json:"repository"`
	Categories    []string      `json:"categories"`
	Tags          []string      `json:"tags"`
	DownloadCount int           `json:"downloadCount"`
	AverageRating float64       `json:"averageRating"`
	ReviewCount   int           `json:"reviewCount"`
	Files         ovsxFileMap   `json:"files"`
	Versions      []ovsxVersion `json:"versions"`
}

// normalizeVersions ensures e.Versions is populated. The Open VSX detail
// endpoint returns version info at the top level (Version + Files) rather
// than in a versions array. When Versions is empty but Version is set, we
// construct a single entry from the top-level fields.
func (e ovsxExtension) normalizeVersions() []ovsxVersion {
	if len(e.Versions) > 0 {
		return e.Versions
	}
	if e.Version != "" {
		return []ovsxVersion{{
			Version:   e.Version,
			Timestamp: e.Timestamp,
			Files:     e.Files,
		}}
	}
	return nil
}

// ovsxSearchResponse is the search endpoint envelope.
type ovsxSearchResponse struct {
	Extensions []ovsxExtension `json:"extensions"`
}

// SearchExtensions searches the registry for extensions matching query.
// page is 1-based; pageSize caps the result count. Returns an empty slice
// (not nil) when no results are found.
func (s *MarketplaceService) SearchExtensions(query string, page int, pageSize int) ([]ExtensionSearchResult, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize
	reqURL := fmt.Sprintf("%s/-/search?query=%s&size=%d&offset=%d",
		s.registryURL, urlEscape(query), pageSize, offset)
	data, err := s.httpGetJSON(reqURL)
	if err != nil {
		return nil, fmt.Errorf("search extensions: %w", err)
	}
	var resp ovsxSearchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse search response: %w", err)
	}
	out := make([]ExtensionSearchResult, 0, len(resp.Extensions))
	for _, e := range resp.Extensions {
		out = append(out, ovsxToSearchResult(e))
	}
	return out, nil
}

// GetExtensionDetail gets detailed info about a specific extension by
// publisher (namespace) and name.
func (s *MarketplaceService) GetExtensionDetail(publisher, name string) (*ExtensionDetail, error) {
	if err := validateExtensionIdent(publisher, name); err != nil {
		return nil, err
	}
	reqURL := fmt.Sprintf("%s/%s/%s", s.registryURL, urlEscape(publisher), urlEscape(name))
	data, err := s.httpGetJSON(reqURL)
	if err != nil {
		return nil, fmt.Errorf("get extension detail: %w", err)
	}
	var e ovsxExtension
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("parse extension detail: %w", err)
	}
	detail := &ExtensionDetail{
		ExtensionSearchResult: ovsxToSearchResult(e),
		Categories:            append([]string(nil), e.Categories...),
		Tags:                  append([]string(nil), e.Tags...),
		License:               e.License,
		Repository:            e.Repository,
		Readme:                fileURL(e.Files, "readme"),
	}
	// Use normalizeVersions to handle both detail (top-level version) and
	// search (versions array) response shapes.
	versions := e.normalizeVersions()
	for _, v := range versions {
		detail.Versions = append(detail.Versions, ExtensionVersion{
			Version:     v.Version,
			DownloadURL: fileURL(v.Files, "download"),
			Date:        v.Timestamp,
		})
	}
	return detail, nil
}

// DownloadAndInstallExtension downloads a VSIX for the given extension
// version, verifies its SHA-256 against the registry-provided hash, and
// installs it under <configDir>/koyori-ide/extensions/<publisher>.<name>/.
// Newly installed extensions are disabled by default (G-SEC-12 req. 2).
// A hash mismatch aborts the install (G-SEC-12 req. 3).
func (s *MarketplaceService) DownloadAndInstallExtension(publisher, name, version string) error {
	if err := validateExtensionIdent(publisher, name); err != nil {
		return err
	}
	if s.configDir == "" {
		return fmt.Errorf("config directory is not configured")
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	reqURL := fmt.Sprintf("%s/%s/%s", s.registryURL, urlEscape(publisher), urlEscape(name))
	data, err := s.httpGetJSON(reqURL)
	if err != nil {
		return fmt.Errorf("fetch extension metadata: %w", err)
	}
	var e ovsxExtension
	if err := json.Unmarshal(data, &e); err != nil {
		return fmt.Errorf("parse extension metadata: %w", err)
	}
	// Resolve the target version entry. If version is empty, use the latest
	// (first) version returned by the registry. normalizeVersions handles
	// the detail endpoint which returns version info at the top level.
	ver, err := pickVersion(e.normalizeVersions(), version)
	if err != nil {
		return err
	}
	downloadURL := fileURL(ver.Files, "download")
	if downloadURL == "" {
		return fmt.Errorf("extension %s.%s version %s has no download URL", publisher, name, ver.Version)
	}
	wantHash, err := s.resolveSha256(fileURL(ver.Files, "sha256"))
	if err != nil {
		return fmt.Errorf("resolve SHA-256 for %s.%s version %s: %w", publisher, name, ver.Version, err)
	}
	if wantHash == "" {
		// G-SEC-12 req. 3: refuse to install when the registry did not
		// provide a hash to verify against. Installing without verification
		// would defeat the integrity gate.
		return fmt.Errorf("extension %s.%s version %s has no SHA-256 hash from the registry; refusing to install unverified", publisher, name, ver.Version)
	}
	// P2-4: 流式下载 VSIX 到临时文件 + 同时哈希，避免全量驻留内存。
	// 安装时用 zip.OpenReader 从磁盘读取。原 httpGetBytes 把 256MB VSIX
	// 全量读入内存，峰值高；流式方案仅 32KB 缓冲 + 哈希状态。
	tmpPath, gotHash, err := s.downloadVSIXToTempFile(downloadURL)
	if err != nil {
		return fmt.Errorf("download VSIX: %w", err)
	}
	defer os.Remove(tmpPath) // 安装完成后删除临时文件
	// G-SEC-12 req. 3: SHA-256 验证。流式哈希与 wantHash 比对。
	if !strings.EqualFold(gotHash, wantHash) {
		return fmt.Errorf("SHA-256 verification failed for %s.%s: expected %s, got %s (G-SEC-12 req. 3)", publisher, name, wantHash, gotHash)
	}
	return s.installFromVSIXFile(tmpPath, wantHash, publisher, name, ver.Version)
}

// UpdateExtension replaces an installed extension using a rollback-safe
// transaction. Unlike DownloadAndInstallExtension, this path never removes the
// live directory before the staged replacement is ready and validated.
//
// The renderer owns extension Workers. This method coordinates the renderer
// stop/cache transaction around the backend replacement.
func (s *MarketplaceService) UpdateExtension(publisher, name, version string) (err error) {
	if err := validateExtensionIdent(publisher, name); err != nil {
		return err
	}
	if s.configDir == "" {
		return fmt.Errorf("config directory is not configured")
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	if _, err := os.Stat(s.extensionDir(publisher, name)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("extension %s.%s is not installed", publisher, name)
		}
		return fmt.Errorf("inspect installed extension: %w", err)
	}

	if s.securityService != nil && s.securityService.IsBlacklisted(publisher, name) {
		return fmt.Errorf("extension %s.%s is blacklisted (G-SEC-12)", publisher, name)
	}

	// Resolve and verify the new archive before touching the installed version.
	reqURL := fmt.Sprintf("%s/%s/%s", s.registryURL, urlEscape(publisher), urlEscape(name))
	data, err := s.httpGetJSON(reqURL)
	if err != nil {
		return fmt.Errorf("fetch extension metadata: %w", err)
	}
	var e ovsxExtension
	if err := json.Unmarshal(data, &e); err != nil {
		return fmt.Errorf("parse extension metadata: %w", err)
	}
	ver, err := pickVersion(e.normalizeVersions(), version)
	if err != nil {
		return err
	}
	downloadURL := fileURL(ver.Files, "download")
	if downloadURL == "" {
		return fmt.Errorf("extension %s.%s version %s has no download URL", publisher, name, ver.Version)
	}
	wantHash, err := s.resolveSha256(fileURL(ver.Files, "sha256"))
	if err != nil {
		return fmt.Errorf("resolve SHA-256 for %s.%s version %s: %w", publisher, name, ver.Version, err)
	}
	if wantHash == "" {
		return fmt.Errorf("extension %s.%s version %s has no SHA-256 hash from the registry; refusing to update unverified", publisher, name, ver.Version)
	}
	tmpPath, gotHash, err := s.downloadVSIXToTempFile(downloadURL)
	if err != nil {
		return fmt.Errorf("download VSIX: %w", err)
	}
	defer os.Remove(tmpPath)
	if !strings.EqualFold(gotHash, wantHash) {
		return fmt.Errorf("SHA-256 verification failed for %s.%s: expected %s, got %s (G-SEC-12 req. 3)", publisher, name, wantHash, gotHash)
	}

	targetDir := s.extensionDir(publisher, name)
	updatingDir := targetDir + ".updating"
	backupDir := targetDir + ".backup"
	if err := removeAllWithRetry(updatingDir); err != nil {
		return fmt.Errorf("clean stale update temp dir: %w", err)
	}
	if err := removeAllWithRetry(backupDir); err != nil {
		return fmt.Errorf("clean stale update backup dir: %w", err)
	}
	if err := os.MkdirAll(updatingDir, 0o755); err != nil {
		return fmt.Errorf("create update temp dir: %w", err)
	}
	cleanupUpdating := true
	defer func() {
		if cleanupUpdating {
			_ = removeAllWithRetry(updatingDir)
		}
	}()

	if err := extractVSIXFromFile(tmpPath, updatingDir); err != nil {
		return fmt.Errorf("extract update: %w", err)
	}
	manifest, err := parseVSIXManifest(updatingDir)
	if err != nil {
		return fmt.Errorf("parse extension manifest: %w", err)
	}
	if manifest.Publisher != publisher || manifest.Name != name {
		return fmt.Errorf("updated manifest identity %q does not match %s.%s", manifest.ExtensionID(), publisher, name)
	}
	if manifest.Version != "" && manifest.Version != ver.Version {
		return fmt.Errorf("updated manifest version %q does not match requested version %q", manifest.Version, ver.Version)
	}
	permissions, err := manifest.RequestedPermissions()
	if err != nil {
		return fmt.Errorf("validate extension permissions: %w", err)
	}
	metaBytes, err := json.MarshalIndent(installedExtMeta{Publisher: publisher, Name: name, Version: ver.Version}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode extension metadata: %w", err)
	}
	if err := os.WriteFile(filepath.Join(updatingDir, installedExtMetaFile), metaBytes, 0o644); err != nil {
		return fmt.Errorf("write extension metadata: %w", err)
	}

	// Capture every persisted/runtime registration that the transaction changes.
	var securityService *ExtensionSecurityService
	s.mu.Lock()
	state := s.loadExtensionStateLocked()
	key := extensionStateKey(publisher, name)
	oldMarketplaceState, hadMarketplaceState := state.Extensions[key]
	securityService = s.securityService
	activationService := s.activationService
	s.mu.Unlock()
	var oldSecurityInfo *ExtensionSecurityInfo
	if securityService != nil {
		if info, infoErr := securityService.GetSecurityInfo(key); infoErr == nil {
			oldSecurityInfo = info
		}
	}
	extensionID := publisher + "." + name
	var oldActivationEvents []string
	var hadActivationRegistration bool
	if activationService != nil {
		oldActivationEvents, hadActivationRegistration = activationService.extensionEventsSnapshot(extensionID)
	}

	// Hold renderer activation before moving the live directory. The terminal
	// request in the defer refreshes or invalidates renderer caches and releases
	// the hold after the backend transaction has reached a known state.
	lifecycleStopped := false
	lifecycleWasActive := false
	lifecycleCommitted := false
	lifecycleRestoreSafe := true
	stopRequest := ExtensionLifecycleRequest{
		RequestID: newExtensionLifecycleRequestID(), ExtensionID: extensionID,
		Publisher: publisher, Name: name, Action: "stop",
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), extensionLifecycleTimeout)
	defer stopCancel()
	stopResult, stopErr := s.requestExtensionLifecycle(stopCtx, stopRequest)
	if stopErr != nil {
		return fmt.Errorf("stop extension before update: %w", stopErr)
	}
	if !stopResult.OK {
		if stopResult.Error == "" {
			return fmt.Errorf("renderer refused to stop extension %s", extensionID)
		}
		return fmt.Errorf("renderer refused to stop extension %s: %s", extensionID, stopResult.Error)
	}
	lifecycleStopped = true
	lifecycleWasActive = stopResult.WasActive
	defer func() {
		if !lifecycleStopped {
			return
		}
		action := "restore"
		if lifecycleCommitted {
			action = "commit"
		} else if !lifecycleRestoreSafe {
			action = "invalidate"
		}
		terminalRequest := ExtensionLifecycleRequest{
			RequestID: newExtensionLifecycleRequestID(), ExtensionID: extensionID,
			Publisher: publisher, Name: name, Action: action, WasActive: lifecycleWasActive,
		}
		terminalCtx, terminalCancel := context.WithTimeout(context.Background(), extensionLifecycleTimeout)
		terminalErr := s.finishExtensionLifecycle(terminalCtx, terminalRequest)
		terminalCancel()
		if terminalErr != nil {
			if err == nil {
				err = terminalErr
			} else {
				err = fmt.Errorf("%w; %s", err, terminalErr)
			}
		}
	}()

	// Move the old live directory aside, then publish the fully staged tree.
	hadLiveDirectory := false
	if _, statErr := os.Stat(targetDir); statErr == nil {
		hadLiveDirectory = true
		if err := renameWithRetry(targetDir, backupDir); err != nil {
			return fmt.Errorf("backup current extension: %w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect current extension: %w", statErr)
	}
	oldDirectoryMoved := hadLiveDirectory
	if oldDirectoryMoved {
		lifecycleRestoreSafe = false
	}
	rollback := func(cause error) error {
		var rollbackErrs []string
		if err := removeAllWithRetry(targetDir); err != nil {
			rollbackErrs = append(rollbackErrs, "remove new extension: "+err.Error())
		}
		if oldDirectoryMoved {
			if err := renameWithRetry(backupDir, targetDir); err != nil {
				rollbackErrs = append(rollbackErrs, "restore old extension: "+err.Error())
			}
		}
		if err := removeAllWithRetry(updatingDir); err != nil {
			rollbackErrs = append(rollbackErrs, "remove update temp dir: "+err.Error())
		}
		if activationService != nil {
			activationService.UnregisterExtension(extensionID)
			activationService.restoreExtensionEvents(extensionID, oldActivationEvents, hadActivationRegistration)
		}
		if securityService != nil {
			var securityErr error
			if oldSecurityInfo != nil {
				securityErr = securityService.restoreInstall(oldSecurityInfo)
			} else {
				securityErr = securityService.removeInstall(extensionID)
			}
			if securityErr != nil {
				rollbackErrs = append(rollbackErrs, "restore security record: "+securityErr.Error())
			}
		}
		s.mu.Lock()
		state = s.loadExtensionStateLocked()
		if state.Extensions == nil {
			state.Extensions = make(map[string]mpExtensionStateEntry)
		}
		if hadMarketplaceState {
			state.Extensions[key] = oldMarketplaceState
		} else {
			delete(state.Extensions, key)
		}
		stateErr := s.saveExtensionStateLocked(state)
		s.mu.Unlock()
		if stateErr != nil {
			rollbackErrs = append(rollbackErrs, "restore marketplace state: "+stateErr.Error())
		}
		if len(rollbackErrs) > 0 {
			lifecycleRestoreSafe = false
			return fmt.Errorf("%w (rollback failed: %s)", cause, strings.Join(rollbackErrs, "; "))
		}
		lifecycleRestoreSafe = true
		return cause
	}

	if err := renameWithRetry(updatingDir, targetDir); err != nil {
		return rollback(fmt.Errorf("publish updated extension: %w", err))
	}
	cleanupUpdating = false
	if securityService != nil {
		if _, regErr := securityService.RegisterInstallFromFile(extensionID, permissions, tmpPath, wantHash); regErr != nil {
			return rollback(fmt.Errorf("security registration failed: %w", regErr))
		}
	}
	if activationService != nil {
		activationService.UnregisterExtension(extensionID)
		if len(manifest.ActivationEvents) > 0 {
			activationService.RegisterExtension(extensionID, manifest.ActivationEvents)
		}
	}
	// Updates always land disabled. This is also the safe state if the old
	// version was enabled; the renderer can require fresh user approval.
	if err := s.setExtensionEnabled(publisher, name, false); err != nil {
		return rollback(fmt.Errorf("persist disabled update state: %w", err))
	}
	lifecycleCommitted = true
	// Backup cleanup is deliberately best-effort after publication. The live
	// directory is already the validated replacement; attempting to roll back
	// here could remove it after the backup has been partially deleted.
	_ = removeAllWithRetry(backupDir)
	return nil
}

// downloadVSIXToTempFile (P2-4) 流式下载 VSIX 到临时文件，同时用 TeeReader
// 计算 SHA-256。返回临时文件路径与计算出的哈希。调用方负责删除临时文件。
//
// 原 httpGetBytes 实现将整个 VSIX 读入内存（上限 256MB），再单独计算哈希。
// 流式方案降低内存峰值：仅 io.Copy 默认 32KB 缓冲 + 哈希状态，与 VSIX 大小无关。
// 落盘后用 zip.OpenReader 从磁盘读取（见 installFromVSIXFile），避免全量驻留内存。
func (s *MarketplaceService) downloadVSIXToTempFile(downloadURL string) (tmpPath, gotHash string, err error) {
	s.mu.Lock()
	sharedClient := s.httpClient
	s.mu.Unlock()
	if sharedClient == nil {
		sharedClient = http.DefaultClient
	}
	// The shared httpClient's 60s Timeout covers body reading and aborts
	// large VSIX downloads mid-stream ("context deadline exceeded"). Use a
	// dedicated client that reuses the shared transport but has no overall
	// Timeout, and enforce a longer deadline via context instead.
	client := &http.Client{}
	if sharedClient.Transport != nil {
		client.Transport = sharedClient.Transport
	}
	ctx, cancel := context.WithTimeout(context.Background(), vsixDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "koyori-ide-marketplace/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", fmt.Errorf("registry returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// 创建临时文件
	tmp, err := os.CreateTemp("", "vsix-*.download")
	if err != nil {
		return "", "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath = tmp.Name()
	// 失败路径清理临时文件
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			tmpPath = ""
		}
	}()

	// H-2: 限制下载大小为 maxHTTPResponseSize，防止恶意服务器无界流。
	// 用 io.LimitReader + 流式哈希同时控制大小与计算 hash。
	hasher := sha256.New()
	limited := io.LimitReader(resp.Body, maxHTTPResponseSize+1)
	// TeeReader: 读 limited 的字节同时写入 hasher
	tee := io.TeeReader(limited, hasher)
	// 流式写入临时文件
	n, copyErr := io.Copy(tmp, tee)
	if copyErr != nil {
		err = fmt.Errorf("stream VSIX to disk: %w", copyErr)
		return "", "", err
	}
	if n > maxHTTPResponseSize {
		err = fmt.Errorf("VSIX exceeds max size %d bytes", maxHTTPResponseSize)
		return "", "", err
	}
	if err := tmp.Sync(); err != nil {
		err = fmt.Errorf("sync temp VSIX: %w", err)
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		err = fmt.Errorf("close temp VSIX: %w", err)
		return "", "", err
	}
	gotHash = hex.EncodeToString(hasher.Sum(nil))
	return tmpPath, gotHash, nil
}

// installFromVSIXFile (P2-4) 从磁盘临时文件读取 VSIX 并安装。
// 用 zip.OpenReader 直接从磁盘读取，避免全量驻留内存。
// 安装完成后调用方应删除临时文件。
func (s *MarketplaceService) installFromVSIXFile(tmpPath, wantHash, publisher, name, version string) (err error) {
	if s.configDir == "" {
		return fmt.Errorf("config directory is not configured")
	}
	if err := validateExtensionIdent(publisher, name); err != nil {
		return err
	}
	if wantHash == "" {
		return fmt.Errorf("installFromVSIXFile: wantHash is empty")
	}
	if s.securityService != nil {
		if s.securityService.IsBlacklisted(publisher, name) {
			return fmt.Errorf("extension %s.%s is blacklisted (G-SEC-12)", publisher, name)
		}
	}
	fileInfo, err := os.Stat(tmpPath)
	if err != nil {
		return fmt.Errorf("stat VSIX: %w", err)
	}
	if !fileInfo.Mode().IsRegular() {
		return fmt.Errorf("VSIX path is not a regular file")
	}
	if fileInfo.Size() > maxHTTPResponseSize {
		return fmt.Errorf("VSIX exceeds max size %d bytes", maxHTTPResponseSize)
	}
	// Re-hash the file immediately before extraction. The download path already
	// hashes while writing, but this closes the window for a modified temp file
	// and keeps direct file installs subject to the same integrity gate.
	gotHash, err := computeFileSHA256(tmpPath)
	if err != nil {
		return fmt.Errorf("hash VSIX before install: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(gotHash), strings.TrimSpace(wantHash)) {
		return fmt.Errorf("SHA-256 verification failed for %s.%s: expected %s, got %s (G-SEC-12 req. 3)", publisher, name, wantHash, gotHash)
	}
	targetDir := s.extensionDir(publisher, name)
	tmpDir := targetDir + ".installing"
	if err := removeAllWithRetry(tmpDir); err != nil {
		return fmt.Errorf("clean stale install temp dir: %w", err)
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("create install temp dir: %w", err)
	}
	// P2-4: 用 zip.OpenReader 从磁盘读取，避免全量驻留内存
	if err := extractVSIXFromFile(tmpPath, tmpDir); err != nil {
		_ = removeAllWithRetry(tmpDir)
		return err
	}
	manifest, err := parseVSIXManifest(tmpDir)
	if err != nil {
		_ = removeAllWithRetry(tmpDir)
		return fmt.Errorf("parse extension manifest: %w", err)
	}
	permissions, err := manifest.RequestedPermissions()
	if err != nil {
		_ = removeAllWithRetry(tmpDir)
		return fmt.Errorf("validate extension permissions: %w", err)
	}
	meta := installedExtMeta{Publisher: publisher, Name: name, Version: version}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, installedExtMetaFile), metaBytes, 0o644); err != nil {
		_ = removeAllWithRetry(tmpDir)
		return fmt.Errorf("write extension metadata: %w", err)
	}
	extensionID := publisher + "." + name
	stopRequest := ExtensionLifecycleRequest{
		RequestID: newExtensionLifecycleRequestID(), ExtensionID: extensionID,
		Publisher: publisher, Name: name, Action: "stop",
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), extensionLifecycleTimeout)
	stopResult, stopErr := s.requestExtensionLifecycle(stopCtx, stopRequest)
	stopCancel()
	if stopErr != nil {
		_ = removeAllWithRetry(tmpDir)
		return fmt.Errorf("stop extension before install: %w", stopErr)
	}
	if !stopResult.OK {
		_ = removeAllWithRetry(tmpDir)
		if stopResult.Error == "" {
			return fmt.Errorf("renderer refused to stop extension %s", extensionID)
		}
		return fmt.Errorf("renderer refused to stop extension %s: %s", extensionID, stopResult.Error)
	}
	lifecycleCommitted := false
	defer func() {
		action := "invalidate"
		if lifecycleCommitted {
			action = "commit"
		}
		terminalRequest := ExtensionLifecycleRequest{
			RequestID: newExtensionLifecycleRequestID(), ExtensionID: extensionID,
			Publisher: publisher, Name: name, Action: action,
		}
		terminalCtx, terminalCancel := context.WithTimeout(context.Background(), extensionLifecycleTimeout)
		terminalErr := s.finishExtensionLifecycle(terminalCtx, terminalRequest)
		terminalCancel()
		if terminalErr != nil {
			if err == nil {
				err = terminalErr
			} else {
				err = fmt.Errorf("%w; %s", err, terminalErr)
			}
		}
	}()
	_ = removeAllWithRetry(targetDir)
	if err := renameWithRetry(tmpDir, targetDir); err != nil {
		_ = removeAllWithRetry(tmpDir)
		return fmt.Errorf("swap install dir into place: %w", err)
	}
	// Register the original on-disk archive. Registration streams the file for
	// signature verification and never creates an archive-sized []byte or a
	// second VSIX copy.
	if s.securityService != nil {
		_, regErr := s.securityService.RegisterInstallFromFile(extensionID, permissions, tmpPath, wantHash)
		if regErr != nil {
			_ = removeAllWithRetry(targetDir)
			return fmt.Errorf("security registration failed: %w", regErr)
		}
	}
	// F-3 (prompt-2.md): 安装成功后注册扩展的 activationEvents，使
	// ActivationService 能在对应时机（onLanguage/onCommand/workspaceContains
	// /onDebug/onDebugResolve/*）触发激活。
	s.registerActivationEvents(manifest)

	// BUG-FIX-2b: installFromVSIXFile 缺少 setExtensionEnabled 调用。
	// G-SEC-12 req. 2 / G-VSC-03 req. 2: 新安装的扩展必须默认禁用。
	// 修复：参照 installFromVSIXData，安装完成后将扩展设为禁用状态。
	if err := s.setExtensionEnabled(publisher, name, false); err != nil {
		return err
	}
	lifecycleCommitted = true
	return nil
}

// extractVSIXFromFile (P2-4) 从磁盘 VSIX 文件解压到 targetDir。
// 用 zip.OpenReader 直接从磁盘读取，避免 ReaderAt 需要全量在内存。
func extractVSIXFromFile(tmpPath, targetDir string) error {
	zr, err := zip.OpenReader(tmpPath)
	if err != nil {
		return fmt.Errorf("open VSIX zip: %w", err)
	}
	defer zr.Close()
	// zr 是 *zip.ReadCloser，嵌入值类型 zip.Reader；&zr.Reader 取其地址。
	return extractVSIXEntries(&zr.Reader, targetDir)
}

// installFromVSIXData writes an in-memory test fixture once to a temporary file
// and delegates to the production file installer. This keeps tests for hash,
// size, traversal, and registration on the same streaming path used by network
// installs without creating a second archive-sized byte slice.
func (s *MarketplaceService) installFromVSIXData(vsix []byte, wantHash, publisher, name, version string) error {
	tmp, err := os.CreateTemp("", "vsix-test-*.vsix")
	if err != nil {
		return fmt.Errorf("create temporary VSIX: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(vsix); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary VSIX: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary VSIX: %w", err)
	}
	return s.installFromVSIXFile(tmpPath, wantHash, publisher, name, version)
}

// ListInstalledExtensions returns all installed VS Code extensions with their
// enabled state. The result is sorted by publisher then name for deterministic
// display. Missing state entries default to disabled (safe default).
func (s *MarketplaceService) ListInstalledExtensions() ([]InstalledExtension, error) {
	if s.configDir == "" {
		return nil, fmt.Errorf("config directory is not configured")
	}
	dir := filepath.Join(s.configDir, extensionsSubdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []InstalledExtension{}, nil
		}
		return nil, fmt.Errorf("list installed extensions: %w", err)
	}
	state := s.loadExtensionState()
	out := make([]InstalledExtension, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		extDir := filepath.Join(dir, entry.Name())
		meta, err := readInstalledMeta(extDir)
		if err != nil {
			// A directory without metadata isn't a tracked install — skip it.
			continue
		}
		key := extensionStateKey(meta.Publisher, meta.Name)
		enabled := false
		if st, ok := state.Extensions[key]; ok {
			enabled = st.Enabled
		}
		out = append(out, InstalledExtension{
			Publisher: meta.Publisher,
			Name:      meta.Name,
			Version:   meta.Version,
			Path:      extDir,
			Enabled:   enabled,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Publisher != out[j].Publisher {
			return out[i].Publisher < out[j].Publisher
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// UninstallExtension removes an installed extension and all backend-owned
// lifecycle state. The renderer owns the Worker, so the stop handshake happens
// before any on-disk cleanup. A renderer timeout is logged and cleanup
// continues, as an uninstall must not leave backend state behind indefinitely.
func (s *MarketplaceService) UninstallExtension(publisher, name string) error {
	if err := validateExtensionIdent(publisher, name); err != nil {
		return err
	}
	if s.configDir == "" {
		return fmt.Errorf("config directory is not configured")
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	extensionID := publisher + "." + name
	targetDir := s.extensionDir(publisher, name)
	var cleanupErrs []string
	stopRequest := ExtensionLifecycleRequest{
		RequestID: newExtensionLifecycleRequestID(), ExtensionID: extensionID,
		Publisher: publisher, Name: name, Action: "stop",
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), extensionUninstallTimeout)
	stopResult, stopErr := s.requestExtensionLifecycle(stopCtx, stopRequest)
	stopCancel()
	if stopErr != nil {
		slog.Warn("extension Worker stop failed during uninstall; continuing cleanup",
			"extension", extensionID, "error", stopErr)
	} else if !stopResult.OK {
		if stopResult.Error == "" {
			slog.Warn("extension Worker refused to stop during uninstall; continuing cleanup",
				"extension", extensionID)
		} else {
			slog.Warn("extension Worker refused to stop during uninstall; continuing cleanup",
				"extension", extensionID, "error", stopResult.Error)
		}
	}
	if err := removeAllWithRetry(targetDir); err != nil {
		cleanupErrs = append(cleanupErrs, "remove extension directory: "+err.Error())
	}
	for _, suffix := range []string{".installing", ".updating", ".backup"} {
		if err := removeAllWithRetry(targetDir + suffix); err != nil {
			cleanupErrs = append(cleanupErrs, "remove "+suffix+" directory: "+err.Error())
		}
	}
	// F-3 (prompt-2.md): 卸载时注销 activationEvents，避免 ActivationService
	// 仍把已卸载的扩展列为可激活。
	s.mu.Lock()
	as := s.activationService
	securityService := s.securityService
	s.mu.Unlock()
	if as != nil {
		as.UnregisterExtension(extensionID)
	}
	if securityService != nil {
		if err := securityService.removeInstall(extensionID); err != nil {
			cleanupErrs = append(cleanupErrs, "remove security record: "+err.Error())
		}
	}
	if err := s.setExtensionEnabled(publisher, name, false, true); err != nil {
		cleanupErrs = append(cleanupErrs, "remove marketplace state: "+err.Error())
	}
	invalidateRequest := ExtensionLifecycleRequest{
		RequestID: newExtensionLifecycleRequestID(), ExtensionID: extensionID,
		Publisher: publisher, Name: name, Action: "invalidate",
	}
	invalidateCtx, invalidateCancel := context.WithTimeout(context.Background(), extensionUninstallTimeout)
	if err := s.finishExtensionLifecycle(invalidateCtx, invalidateRequest); err != nil {
		cleanupErrs = append(cleanupErrs, "invalidate renderer extension state: "+err.Error())
	}
	invalidateCancel()
	if len(cleanupErrs) > 0 {
		return fmt.Errorf("uninstall extension %s: %s", extensionID, strings.Join(cleanupErrs, "; "))
	}
	return nil
}

// registerActivationEvents (F-3, prompt-2.md) 是安装路径的内部 helper：
// 若 activationService 已注入且 manifest 非空，则用 manifest 的
// ActivationEvents 注册扩展。manifest.Publisher/Name 用于构造扩展 ID；
// 当 manifest 缺少 publisher/name 时（理论上不应发生，因为 install 路径已校验）
// 跳过注册。
func (s *MarketplaceService) registerActivationEvents(manifest *VSCodeExtensionManifest) {
	if manifest == nil {
		return
	}
	s.mu.Lock()
	as := s.activationService
	s.mu.Unlock()
	if as == nil {
		return
	}
	extID := manifest.ExtensionID()
	if extID == "" {
		return
	}
	// ActivationEvents 可能为 nil（扩展未声明激活事件）；注册空切片也行，
	// 但跳过可避免无意义条目。
	if len(manifest.ActivationEvents) == 0 {
		return
	}
	as.RegisterExtension(extID, manifest.ActivationEvents)
}

// GetInstalledExtensionManifests (F-3, prompt-2.md) 返回所有已安装扩展的
// manifest（含 ParsedContributes）。前端扩展宿主用此接口一次性获取所有
// contributes.commands / views / grammars / snippets 等以注入命令面板、
// 侧边栏与 Monaco 语言配置。仅返回已成功解析 manifest 的扩展。
func (s *MarketplaceService) GetInstalledExtensionManifests() ([]VSCodeExtensionManifest, error) {
	if s.configDir == "" {
		return nil, fmt.Errorf("config directory is not configured")
	}
	dir := filepath.Join(s.configDir, extensionsSubdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []VSCodeExtensionManifest{}, nil
		}
		return nil, fmt.Errorf("list installed extensions: %w", err)
	}
	out := make([]VSCodeExtensionManifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		extDir := filepath.Join(dir, entry.Name())
		m, err := parseVSIXManifest(extDir)
		if err != nil {
			// 损坏的扩展目录跳过，不阻断整体列表。
			continue
		}
		out = append(out, *m)
	}
	return out, nil
}

// ReadExtensionFile (F-3, prompt-2.md) 读取已安装扩展内的文件内容。
// relativePath 是相对于扩展根目录的路径（如 "snippets/go.json"、"syntaxes/go.tmLanguage.json"）。
// 用于前端加载 contributes.grammars 和 contributes.snippets 引用的文件。
func (s *MarketplaceService) ReadExtensionFile(publisher, name, relativePath string) ([]byte, error) {
	if err := validateExtensionIdent(publisher, name); err != nil {
		return nil, err
	}
	if s.configDir == "" {
		return nil, fmt.Errorf("config directory is not configured")
	}
	if relativePath == "" {
		return nil, fmt.Errorf("relativePath is empty")
	}
	extDir := s.extensionDir(publisher, name)
	// 防止路径穿越：clean 后确保仍在 extDir 下。
	cleanRel := filepath.Clean(relativePath)
	if filepath.IsAbs(cleanRel) || strings.HasPrefix(cleanRel, "..") {
		return nil, fmt.Errorf("invalid relative path: %q", relativePath)
	}
	fullPath := filepath.Join(extDir, cleanRel)
	// 二次校验：resolve 后的路径必须以 extDir 为前缀。
	absExtDir, err := filepath.Abs(extDir)
	if err != nil {
		return nil, fmt.Errorf("resolve extension dir: %w", err)
	}
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return nil, fmt.Errorf("resolve file path: %w", err)
	}
	if !strings.HasPrefix(absPath, absExtDir+string(filepath.Separator)) && absPath != absExtDir {
		return nil, fmt.Errorf("path traversal blocked: %q", relativePath)
	}
	return readFileLimited(absPath, maxReadableFileBytes)
}

// TriggerActivationOnLanguage (F-3, prompt-2.md) 是 ActivationService 的
// 便捷转发：返回因 onLanguage:<language> 事件而需要激活的扩展 ID 列表。
// 若未注入 ActivationService，返回空切片。
func (s *MarketplaceService) TriggerActivationOnLanguage(language string) []string {
	s.mu.Lock()
	as := s.activationService
	s.mu.Unlock()
	if as == nil {
		return nil
	}
	return as.TriggerOnLanguage(language)
}

// TriggerActivationOnCommand (F-3, prompt-2.md) 转发 ActivationService。
func (s *MarketplaceService) TriggerActivationOnCommand(command string) []string {
	s.mu.Lock()
	as := s.activationService
	s.mu.Unlock()
	if as == nil {
		return nil
	}
	return as.TriggerOnCommand(command)
}

// TriggerActivationWorkspaceContains (F-3, prompt-2.md) 转发 ActivationService。
func (s *MarketplaceService) TriggerActivationWorkspaceContains(workspaceRoot string) []string {
	s.mu.Lock()
	as := s.activationService
	s.mu.Unlock()
	if as == nil {
		return nil
	}
	return as.TriggerWorkspaceContains(workspaceRoot)
}

// TriggerActivationOnDebug (F-3, prompt-2.md) 转发 ActivationService。
func (s *MarketplaceService) TriggerActivationOnDebug() []string {
	s.mu.Lock()
	as := s.activationService
	s.mu.Unlock()
	if as == nil {
		return nil
	}
	return as.TriggerOnDebug()
}

// TriggerActivationOnDebugResolve (F-3, prompt-2.md) 转发 ActivationService。
func (s *MarketplaceService) TriggerActivationOnDebugResolve(debugType string) []string {
	s.mu.Lock()
	as := s.activationService
	s.mu.Unlock()
	if as == nil {
		return nil
	}
	return as.TriggerOnDebugResolve(debugType)
}

// TriggerActivationEager (F-3, prompt-2.md) 转发 ActivationService。调用方
// 应提示用户确认是否启用 eager (*) 扩展。
func (s *MarketplaceService) TriggerActivationEager() []string {
	s.mu.Lock()
	as := s.activationService
	s.mu.Unlock()
	if as == nil {
		return nil
	}
	return as.TriggerEager()
}

// IsExtensionActivated (F-3, prompt-2.md) 返回扩展是否已激活。
func (s *MarketplaceService) IsExtensionActivated(extensionID string) bool {
	s.mu.Lock()
	as := s.activationService
	s.mu.Unlock()
	if as == nil {
		return false
	}
	return as.IsActivated(extensionID)
}

// ReportExtensionActivation records the outcome only after the frontend
// extension host has completed (or failed) the real Worker activation.
func (s *MarketplaceService) ReportExtensionActivation(extensionID string, activated bool) {
	s.mu.Lock()
	as := s.activationService
	s.mu.Unlock()
	if as != nil {
		as.ReportActivationResult(extensionID, activated)
	}
}

// ReportExtensionDeactivated releases backend activation state after the
// frontend extension host has completed teardown.
func (s *MarketplaceService) ReportExtensionDeactivated(extensionID string) {
	s.mu.Lock()
	as := s.activationService
	s.mu.Unlock()
	if as != nil {
		as.ReportDeactivated(extensionID)
	}
}

// SetExtensionEnabled persists the enabled/disabled state for an installed
// extension and its security record as one coordinated operation. Restricted
// extensions require explicitApproval=true.
// BUG-FIX-2c: 当用户启用扩展时，需要重新注册该扩展的 activationEvents，
// 因为 installFromVSIXFile 之前未调用 setExtensionEnabled 导致该扩展的
// 激活事件从未注册到 ActivationService。现在启用时尝试重新从 manifest
// 读取并注册 activationEvents。
func (s *MarketplaceService) SetExtensionEnabled(publisher, name string, enabled bool, explicitApproval ...bool) (err error) {
	if err := validateExtensionIdent(publisher, name); err != nil {
		return err
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	extensionID := publisher + "." + name
	lifecycleStopped := false
	lifecycleWasActive := false
	lifecycleCommitted := enabled
	if !enabled {
		stopRequest := ExtensionLifecycleRequest{
			RequestID: newExtensionLifecycleRequestID(), ExtensionID: extensionID,
			Publisher: publisher, Name: name, Action: "stop",
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), extensionLifecycleTimeout)
		stopResult, stopErr := s.requestExtensionLifecycle(stopCtx, stopRequest)
		stopCancel()
		if stopErr != nil {
			return fmt.Errorf("stop extension before disabling: %w", stopErr)
		}
		if !stopResult.OK {
			if stopResult.Error == "" {
				return fmt.Errorf("renderer refused to stop extension %s", extensionID)
			}
			return fmt.Errorf("renderer refused to stop extension %s: %s", extensionID, stopResult.Error)
		}
		lifecycleStopped = true
		lifecycleWasActive = stopResult.WasActive
		defer func() {
			if !lifecycleStopped || lifecycleCommitted {
				return
			}
			restoreRequest := ExtensionLifecycleRequest{
				RequestID: newExtensionLifecycleRequestID(), ExtensionID: extensionID,
				Publisher: publisher, Name: name, Action: "restore", WasActive: lifecycleWasActive,
			}
			restoreCtx, restoreCancel := context.WithTimeout(context.Background(), extensionLifecycleTimeout)
			restoreErr := s.finishExtensionLifecycle(restoreCtx, restoreRequest)
			restoreCancel()
			if restoreErr != nil {
				if err == nil {
					err = restoreErr
				} else {
					err = fmt.Errorf("%w; %s", err, restoreErr)
				}
			}
		}()
	}

	s.mu.Lock()
	securityService := s.securityService
	if s.configDir == "" {
		s.mu.Unlock()
		return fmt.Errorf("config directory is not configured")
	}
	state := s.loadExtensionStateLocked()
	if state.Extensions == nil {
		state.Extensions = make(map[string]mpExtensionStateEntry)
	}
	key := extensionStateKey(publisher, name)
	previous, hadPrevious := state.Extensions[key]
	state.Extensions[key] = mpExtensionStateEntry{Enabled: enabled}
	if err := s.saveExtensionStateLocked(state); err != nil {
		s.mu.Unlock()
		return err
	}

	if securityService != nil {
		approved := len(explicitApproval) > 0 && explicitApproval[0]
		if err := securityService.configureExtensionEnabled(key, enabled, approved); err != nil {
			if hadPrevious {
				state.Extensions[key] = previous
			} else {
				delete(state.Extensions, key)
			}
			rollbackErr := s.saveExtensionStateLocked(state)
			s.mu.Unlock()
			if rollbackErr != nil {
				return fmt.Errorf("update extension security state: %w (marketplace rollback failed: %v)", err, rollbackErr)
			}
			return fmt.Errorf("update extension security state: %w", err)
		}
	}
	s.mu.Unlock()
	lifecycleCommitted = true
	// BUG-FIX-2c: 启用扩展时，尝试重新注册 activationEvents。
	// 这解决了两个问题：
	//   1. installFromVSIXFile 历史遗漏的激活事件注册
	//   2. 禁用后再启用的扩展能恢复激活能力
	if enabled {
		manifest, manifestErr := s.GetExtensionManifest(publisher, name)
		if manifestErr == nil && manifest != nil {
			s.registerActivationEvents(manifest)
		}
		// manifestErr 不返回给调用方——即使 manifest 解析失败，
		// 扩展的启用/禁用状态已经正确持久化；activationEvents
		// 的缺失只是失去了自动激活能力（用户仍可手动触发命令）。
	}
	return nil
}

// GetExtensionManifest reads and parses the extension/package.json from an
// installed extension directory, returning the manifest subset (Step 3).
func (s *MarketplaceService) GetExtensionManifest(publisher, name string) (*VSCodeExtensionManifest, error) {
	if err := validateExtensionIdent(publisher, name); err != nil {
		return nil, err
	}
	if s.configDir == "" {
		return nil, fmt.Errorf("config directory is not configured")
	}
	return parseVSIXManifest(s.extensionDir(publisher, name))
}

// --- internal helpers ---

// extensionDir returns the absolute install path for an extension.
func (s *MarketplaceService) extensionDir(publisher, name string) string {
	return filepath.Join(s.configDir, extensionsSubdir, extensionStateKey(publisher, name))
}

// setExtensionEnabled updates the persisted state. When remove is true the
// entry is deleted instead of written.
func (s *MarketplaceService) setExtensionEnabled(publisher, name string, enabled bool, remove ...bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configDir == "" {
		return fmt.Errorf("config directory is not configured")
	}
	state := s.loadExtensionStateLocked()
	if state.Extensions == nil {
		state.Extensions = make(map[string]mpExtensionStateEntry)
	}
	key := extensionStateKey(publisher, name)
	if len(remove) > 0 && remove[0] {
		delete(state.Extensions, key)
	} else {
		state.Extensions[key] = mpExtensionStateEntry{Enabled: enabled}
	}
	return s.saveExtensionStateLocked(state)
}

// loadExtensionState reads the persisted enabled/disabled state (best-effort).
func (s *MarketplaceService) loadExtensionState() mpExtensionStateFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadExtensionStateLocked()
}

func (s *MarketplaceService) loadExtensionStateLocked() mpExtensionStateFile {
	if s.configDir == "" {
		return mpExtensionStateFile{Extensions: map[string]mpExtensionStateEntry{}}
	}
	path := filepath.Join(s.configDir, "koyori-ide", extensionsStateFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return mpExtensionStateFile{Extensions: map[string]mpExtensionStateEntry{}}
	}
	var state mpExtensionStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return mpExtensionStateFile{Extensions: map[string]mpExtensionStateEntry{}}
	}
	if state.Extensions == nil {
		state.Extensions = map[string]mpExtensionStateEntry{}
	}
	return state
}

func (s *MarketplaceService) saveExtensionStateLocked(state mpExtensionStateFile) error {
	if s.configDir == "" {
		return fmt.Errorf("config directory is not configured")
	}
	path := filepath.Join(s.configDir, "koyori-ide", extensionsStateFileName)
	// M-5: atomic write (temp+rename+0600) prevents half-written state.
	return atomicWriteJSON(path, state, 0600)
}

func (s *MarketplaceService) saveExtensionState(state mpExtensionStateFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveExtensionStateLocked(state)
}

// extensionStateKey is the map key for an extension's persisted state, also
// used as the on-disk directory name: "<publisher>.<name>".
func extensionStateKey(publisher, name string) string {
	return publisher + "." + name
}

// httpGetJSON fetches a JSON document from the registry.
func (s *MarketplaceService) httpGetJSON(url string) ([]byte, error) {
	return s.httpGet(url, "application/json")
}

// httpGetBytes fetches raw bytes (e.g. a VSIX) from a URL.
func (s *MarketplaceService) httpGetBytes(url string) ([]byte, error) {
	return s.httpGet(url, "")
}

func (s *MarketplaceService) httpGet(url, accept string) ([]byte, error) {
	s.mu.Lock()
	client := s.httpClient
	s.mu.Unlock()
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "koyori-ide-marketplace/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("registry returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// H-2: limit response body to maxHTTPResponseSize to prevent OOM from
	// a malicious or buggy server streaming an unbounded body. We read one
	// extra byte to detect the overflow condition.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(data)) > maxHTTPResponseSize {
		return nil, fmt.Errorf("response body exceeds max size %d bytes", maxHTTPResponseSize)
	}
	return data, nil
}

// extractVSIXEntries extracts zip entries from an on-disk VSIX reader.
// G20: VSIX extraction quotas (fail-closed). These bound zip-bomb / abuse
// vectors: total expanded size, per-entry size, entry count, compression
// ratio, path length and nesting depth.
const (
	vsixMaxTotalBytes       = 200 << 20 // 200 MiB total expanded
	vsixMaxEntryBytes       = 50 << 20  // 50 MiB per entry
	vsixMaxEntryCount       = 5000      // entries per VSIX
	vsixMaxCompressionRatio = 1000      // expanded:compressed ratio guard
	vsixMaxPathLength       = 1024      // cleaned entry path length
	vsixMaxNestingDepth     = 32        // directory nesting depth
)

// vsixExtractStats accumulates budget across entries so a multi-entry bomb
// cannot slip under per-entry limits.
type vsixExtractStats struct {
	totalBytes int64
	entryCount int
}

func extractVSIXEntries(zr *zip.Reader, targetDir string) error {
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("resolve target dir: %w", err)
	}
	if len(zr.File) > vsixMaxEntryCount {
		return fmt.Errorf("VSIX has %d entries, exceeds the %d entry limit", len(zr.File), vsixMaxEntryCount)
	}
	stats := &vsixExtractStats{}
	seen := make(map[string]struct{})
	for _, f := range zr.File {
		if err := extractZipEntry(f, absTarget, stats, seen); err != nil {
			return err
		}
	}
	return nil
}

// extractZipEntry writes a single zip entry into absTarget, after validating
// the resolved path stays within absTarget. Directory entries are created;
// file entries are written with their contents. Symlinks (via the zip's Unix
// symlink bit) are rejected — an extension should not install symlinks.
// G20: quotas are enforced on both the header-declared sizes and the actual
// streamed bytes (a lying header cannot bypass the budget).
func extractZipEntry(f *zip.File, absTarget string, stats *vsixExtractStats, seen map[string]struct{}) error {
	name := f.Name
	// Normalize separators to the OS form and clean. Reject absolute paths
	// and parent traversal before joining.
	name = strings.ReplaceAll(name, "\\", "/")
	cleaned := filepath.Clean(name)
	if strings.HasPrefix(cleaned, "..") || cleaned == ".." {
		return fmt.Errorf("VSIX entry %q escapes the install directory (path traversal)", f.Name)
	}
	// Reject absolute entry paths (Unix "/" or Windows drive/UNC). filepath.IsAbs
	// catches drive/UNC on Windows; the leading-slash check covers Unix-style
	// and a backslash-rooted entry.
	if strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "\\") || filepath.IsAbs(cleaned) {
		return fmt.Errorf("VSIX entry %q is an absolute path (path traversal)", f.Name)
	}
	// Reject Windows volume-relative form ("C:foo").
	if len(cleaned) >= 2 && cleaned[1] == ':' && (len(cleaned) == 2 || (cleaned[2] != '/' && cleaned[2] != '\\')) {
		return fmt.Errorf("VSIX entry %q uses a Windows volume-relative path (path traversal)", f.Name)
	}
	dest := filepath.Join(absTarget, cleaned)
	// Resolve and verify the destination is within absTarget. This catches
	// symlink-based escapes that the lexical check would miss.
	destResolved, err := evalSymlinksAllowMissing(dest)
	if err != nil {
		return fmt.Errorf("resolve VSIX entry path %q: %w", f.Name, err)
	}
	if destResolved != absTarget && !strings.HasPrefix(destResolved, absTarget+string(filepath.Separator)) {
		return fmt.Errorf("VSIX entry %q resolves outside the install directory (path traversal)", f.Name)
	}
	// Reject symlink entries (Unix mode bit). A VSIX should ship real files.
	if f.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("VSIX entry %q is a symlink; symlinks are not allowed", f.Name)
	}
	// G20: duplicate paths and case-insensitive collisions are rejected so a
	// single VSIX cannot contain ambiguous installs (same target twice, or
	// Foo.txt + foo.txt colliding on case-insensitive filesystems).
	if !f.FileInfo().IsDir() {
		normKey := strings.ToLower(filepath.Clean(filepath.Join(absTarget, cleaned)))
		if _, dup := seen[normKey]; dup {
			return fmt.Errorf("VSIX entry %q duplicates an earlier target path (case-insensitive collision)", f.Name)
		}
		seen[normKey] = struct{}{}
	}
	// G20: path length and nesting depth quotas.
	if len(cleaned) > vsixMaxPathLength {
		return fmt.Errorf("VSIX entry %q path length %d exceeds the %d limit", f.Name, len(cleaned), vsixMaxPathLength)
	}
	// Normalize separators for the depth count (filepath.Clean is OS-local).
	normCleaned := strings.ReplaceAll(cleaned, "\\", "/")
	if depth := strings.Count(normCleaned, "/"); depth > vsixMaxNestingDepth {
		return fmt.Errorf("VSIX entry %q nesting depth %d exceeds the %d limit", f.Name, depth, vsixMaxNestingDepth)
	}
	if f.FileInfo().IsDir() {
		return os.MkdirAll(dest, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create dir for %q: %w", f.Name, err)
	}
	// G20: header-declared quotas (single-entry size, compression ratio).
	if f.UncompressedSize64 > vsixMaxEntryBytes {
		return fmt.Errorf("VSIX entry %q expands to %d bytes, exceeds the %d byte limit", f.Name, f.UncompressedSize64, vsixMaxEntryBytes)
	}
	if f.CompressedSize64 > 0 && f.UncompressedSize64/f.CompressedSize64 > vsixMaxCompressionRatio {
		return fmt.Errorf("VSIX entry %q has a suspicious compression ratio (%d:1, zip bomb)", f.Name, f.UncompressedSize64/f.CompressedSize64)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open VSIX entry %q: %w", f.Name, err)
	}
	defer rc.Close()
	w, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create file %q: %w", f.Name, err)
	}
	defer w.Close()
	// G20: budget the ACTUAL streamed bytes (defeats lying headers); the
	// per-entry limit uses LimitReader so a bomb cannot over-allocate.
	written, copyErr := io.Copy(w, io.LimitReader(rc, vsixMaxEntryBytes+1))
	if written > vsixMaxEntryBytes {
		return fmt.Errorf("VSIX entry %q expanded beyond the %d byte limit (zip bomb)", f.Name, vsixMaxEntryBytes)
	}
	stats.totalBytes += written
	if stats.totalBytes > vsixMaxTotalBytes {
		return fmt.Errorf("VSIX total expanded size %d exceeds the %d byte limit (zip bomb)", stats.totalBytes, vsixMaxTotalBytes)
	}
	if copyErr != nil {
		return fmt.Errorf("write VSIX entry %q: %w", f.Name, copyErr)
	}
	return nil
}

// parseVSIXManifest reads extension/package.json from an extracted VSIX
// directory and returns the manifest subset (Step 3). Returns an error if the
// manifest is missing or malformed.
//
// F-3 (prompt-2.md): 解析后调用 ParseExtensionManifest 将 contributes 转为
// 结构化 ParsedContributes（含 configuration 归一化 + views/menus/keybindings/
// themes 等新增字段），供前端扩展宿主直接使用。
func parseVSIXManifest(extDir string) (*VSCodeExtensionManifest, error) {
	manifestPath := filepath.Join(extDir, vsixExtensionPrefix, "package.json")
	data, err := readFileLimited(manifestPath, maxReadableFileBytes)
	if err != nil {
		return nil, fmt.Errorf("read extension manifest: %w", err)
	}
	var m VSCodeExtensionManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse extension manifest: %w", err)
	}
	// F-3: 用 ParseExtensionManifest 做结构化解析（宽松解析，未知字段被忽略，
	// configuration 归一化为数组）。失败不阻断安装流程——ParsedContributes
	// 保持零值，前端扩展宿主会回退到原始 Contributes json.RawMessage。
	if em, perr := ParseExtensionManifest(string(data)); perr == nil {
		m.ParsedContributes = em.Contributes
	}
	return &m, nil
}

// readInstalledMeta reads the koyori-ide-ext.json metadata from an installed
// extension directory.
func readInstalledMeta(extDir string) (*installedExtMeta, error) {
	data, err := readFileLimited(filepath.Join(extDir, installedExtMetaFile), maxReadableFileBytes)
	if err != nil {
		return nil, err
	}
	var meta installedExtMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// pickVersion resolves the version entry to install. Empty version selects
// the latest (first) version returned by the registry.
func pickVersion(versions []ovsxVersion, version string) (ovsxVersion, error) {
	if len(versions) == 0 {
		return ovsxVersion{}, fmt.Errorf("extension has no published versions")
	}
	if version == "" {
		return versions[0], nil
	}
	for _, v := range versions {
		if v.Version == version {
			return v, nil
		}
	}
	return ovsxVersion{}, fmt.Errorf("version %q not found for extension", version)
}

// ovsxToSearchResult maps an Open VSX extension object to the public struct.
func ovsxToSearchResult(e ovsxExtension) ExtensionSearchResult {
	publisher := e.Namespace
	if publisher == "" {
		publisher = e.Name
	}
	icon := fileURL(e.Files, "icon")
	return ExtensionSearchResult{
		ID:            extensionStateKey(publisher, e.Name),
		Name:          e.Name,
		DisplayName:   e.DisplayName,
		Publisher:     publisher,
		Description:   e.Description,
		Version:       e.Version,
		Rating:        e.AverageRating,
		RatingCount:   e.ReviewCount,
		DownloadCount: e.DownloadCount,
		IconURL:       icon,
	}
}

// fileURL returns the URL for a file role from an Open VSX file map.
func fileURL(files ovsxFileMap, role string) string {
	if files == nil {
		return ""
	}
	return files[role]
}

// sha256Hex returns the hex-encoded SHA-256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// resolveSha256 resolves the expected SHA-256 hash for an extension download.
//
// The Open VSX registry returns the "sha256" entry in the files map as a URL
// pointing to a .sha256 file (e.g. ".../ms-python.python-2026.4.0.sha256"),
// NOT as the raw hash value. The .sha256 file content has the format:
//
//	<hex-hash>  <filename>
//
// (like a shasum output). We extract just the first 64-char hex token.
//
// For backward compatibility, if the value is not a URL (doesn't start with
// http), it is treated as the raw hash directly.
func (s *MarketplaceService) resolveSha256(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	// If it doesn't look like a URL, assume it's already a hex hash.
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return strings.TrimSpace(raw), nil
	}
	// Fetch the .sha256 file content.
	data, err := s.httpGetBytes(raw)
	if err != nil {
		return "", fmt.Errorf("fetch sha256 file: %w", err)
	}
	// The file format is "<hex-hash>  <filename>\n". Extract the first token.
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("sha256 file is empty")
	}
	// Take the first whitespace-delimited token (the hash).
	hash := strings.Fields(content)[0]
	// Basic validation: a SHA-256 hex hash is 64 chars.
	if len(hash) != 64 {
		return "", fmt.Errorf("invalid SHA-256 hash length %d (expected 64): %s", len(hash), hash)
	}
	return strings.ToLower(hash), nil
}

// validateExtensionIdent rejects empty or path-bearing publisher/name values
// before they are joined into a filesystem path.
func validateExtensionIdent(publisher, name string) error {
	if publisher == "" {
		return fmt.Errorf("publisher is required")
	}
	if name == "" {
		return fmt.Errorf("extension name is required")
	}
	if strings.ContainsAny(publisher, `/\`) || strings.Contains(publisher, "..") {
		return fmt.Errorf("invalid publisher %q", publisher)
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid extension name %q", name)
	}
	return nil
}

// urlEscape percent-encodes a path segment for use in a registry URL.
// Uses net/url's PathEscape which encodes spaces as %20 and other reserved
// characters as required for URL path segments.
func urlEscape(s string) string {
	return url.PathEscape(s)
}

// ============================================================================
// G-MKT-02: Marketplace enhancements — category browsing, featured list,
// update checking, and README fetching.
// ============================================================================

// BrowseByCategory returns extensions in a specific category (e.g.
// "Programming Languages", "Snippets"). The page is 1-based.
func (s *MarketplaceService) BrowseByCategory(category string, page int, pageSize int) ([]ExtensionSearchResult, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize
	reqURL := fmt.Sprintf("%s/-/search?category=%s&size=%d&offset=%d&sortOrder=desc&sortBy=downloadCount",
		s.registryURL, urlEscape(category), pageSize, offset)
	data, err := s.httpGetJSON(reqURL)
	if err != nil {
		return nil, fmt.Errorf("browse category: %w", err)
	}
	var resp ovsxSearchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse category response: %w", err)
	}
	out := make([]ExtensionSearchResult, 0, len(resp.Extensions))
	for _, e := range resp.Extensions {
		out = append(out, ovsxToSearchResult(e))
	}
	return out, nil
}

// GetFeaturedExtensions returns a curated set of popular extensions for the
// marketplace landing page. It performs a broad search (empty query) sorted
// by download count and returns the top results.
func (s *MarketplaceService) GetFeaturedExtensions() ([]ExtensionSearchResult, error) {
	reqURL := fmt.Sprintf("%s/-/search?size=12&sortOrder=desc&sortBy=downloadCount",
		s.registryURL)
	data, err := s.httpGetJSON(reqURL)
	if err != nil {
		return nil, fmt.Errorf("get featured: %w", err)
	}
	var resp ovsxSearchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse featured response: %w", err)
	}
	out := make([]ExtensionSearchResult, 0, len(resp.Extensions))
	for _, e := range resp.Extensions {
		out = append(out, ovsxToSearchResult(e))
	}
	return out, nil
}

// ExtensionUpdate describes an available update for an installed extension.
type ExtensionUpdate struct {
	Publisher      string `json:"publisher"`
	Name           string `json:"name"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	DownloadURL    string `json:"downloadUrl"`
}

// CheckForUpdates compares installed extension versions against the latest
// versions available in the registry. Returns the list of extensions that
// have an update available.
func (s *MarketplaceService) CheckForUpdates() ([]ExtensionUpdate, error) {
	installed, err := s.ListInstalledExtensions()
	if err != nil {
		return nil, fmt.Errorf("list installed: %w", err)
	}
	var updates []ExtensionUpdate
	for _, ext := range installed {
		detail, err := s.GetExtensionDetail(ext.Publisher, ext.Name)
		if err != nil {
			continue // skip extensions that fail to query
		}
		latestVersion := ext.Version
		var downloadURL string
		if detail != nil && len(detail.Versions) > 0 {
			latestVersion = detail.Versions[0].Version
			downloadURL = detail.Versions[0].DownloadURL
		}
		if latestVersion != ext.Version {
			updates = append(updates, ExtensionUpdate{
				Publisher:      ext.Publisher,
				Name:           ext.Name,
				CurrentVersion: ext.Version,
				LatestVersion:  latestVersion,
				DownloadURL:    downloadURL,
			})
		}
	}
	return updates, nil
}

// GetExtensionReadme fetches and returns the README content for an extension.
// The README URL is obtained from the extension detail's files map.
func (s *MarketplaceService) GetExtensionReadme(publisher, name string) (string, error) {
	if err := validateExtensionIdent(publisher, name); err != nil {
		return "", err
	}
	reqURL := fmt.Sprintf("%s/%s/%s", s.registryURL, urlEscape(publisher), urlEscape(name))
	data, err := s.httpGetJSON(reqURL)
	if err != nil {
		return "", fmt.Errorf("fetch extension for readme: %w", err)
	}
	var e ovsxExtension
	if err := json.Unmarshal(data, &e); err != nil {
		return "", fmt.Errorf("parse extension for readme: %w", err)
	}
	readmeURL := fileURL(e.Files, "readme")
	if readmeURL == "" {
		return "", nil
	}
	readmeData, err := s.httpGetBytes(readmeURL)
	if err != nil {
		return "", fmt.Errorf("download readme: %w", err)
	}
	return string(readmeData), nil
}

// GetCategories returns the list of common extension categories used by the
// Open VSX registry. These are the standard VS Code extension categories.
func (s *MarketplaceService) GetCategories() []string {
	return []string{
		"Programming Languages",
		"Snippets",
		"Linters",
		"Themes",
		"Debuggers",
		"Formatters",
		"Keymaps",
		"Extension Packs",
		"Language Packs",
		"Other",
	}
}

// removeAllWithRetry wraps os.RemoveAll with a verify-and-retry loop.
// On Windows, os.RemoveAll can return nil even when the directory still
// exists (files marked for deletion but handles held by antivirus/indexer).
// This helper verifies the path is actually gone and retries a few times
// before giving up. On Linux/macOS the first attempt always succeeds, so
// the retry loop is a no-op.
func removeAllWithRetry(path string) error {
	const maxRetries = 10
	const retryDelay = 50 * time.Millisecond
	for i := 0; i < maxRetries; i++ {
		if err := os.RemoveAll(path); err != nil {
			// RemoveAll returned an error — wait and retry.
			time.Sleep(retryDelay)
			continue
		}
		// RemoveAll returned nil — verify the path is actually gone.
		// On Windows with Go 1.19+, POSIX delete semantics can cause
		// files to be marked-for-deletion but still visible; we retry
		// until the FS catches up.
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil
		}
		// Directory still exists. Try manual recursive removal as a
		// fallback (walk deepest-first, delete files then dirs).
		_ = manualRemoveAll(path)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil
		}
		time.Sleep(retryDelay)
	}
	return fmt.Errorf("removeAllWithRetry: %q still exists after %d retries", path, maxRetries)
}

// manualRemoveWalkDirFunc classifies entries without loading file metadata.
func manualRemoveWalkDirFunc(root string, files, dirs *[]string) fs.WalkDirFunc {
	return func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip errors, try to delete what we can
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			*dirs = append(*dirs, path)
		} else {
			*files = append(*files, path)
		}
		return nil
	}
}

// manualRemoveAll walks the directory tree and deletes files first (deepest
// first), then directories. This is a fallback for cases where os.RemoveAll
// returns nil but the directory still exists (Windows POSIX delete semantics).
func manualRemoveAll(root string) error {
	var files []string
	var dirs []string
	err := filepath.WalkDir(root, manualRemoveWalkDirFunc(root, &files, &dirs))
	if err != nil {
		return err
	}
	// Delete files first (clear read-only flag to be safe).
	for _, f := range files {
		_ = os.Chmod(f, 0o666)
		_ = os.Remove(f)
	}
	// Delete directories deepest first (reverse sort ensures children
	// are deleted before parents).
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		_ = os.Chmod(d, 0o777)
		_ = os.Remove(d)
	}
	// Finally remove the root.
	return os.Remove(root)
}

// renameWithRetry wraps os.Rename with a retry loop. On Windows, renaming
// over an existing directory can fail with "Access is denied" when the
// target's files are briefly held by antivirus/indexer. The caller must
// have already removed the target dir (via removeAllWithRetry) before
// calling this; the retry covers the residual handle-release window.
func renameWithRetry(old, new string) error {
	const maxRetries = 5
	const retryDelay = 30 * time.Millisecond
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		lastErr = os.Rename(old, new)
		if lastErr == nil {
			return nil
		}
		time.Sleep(retryDelay)
	}
	return lastErr
}
