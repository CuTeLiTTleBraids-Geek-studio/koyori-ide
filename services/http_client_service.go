package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

const (
	defaultHTTPClientTimeout      = 30 * time.Second
	maximumHTTPClientTimeout      = 2 * time.Minute
	defaultHTTPClientResponseSize = int64(5 << 20)
	maximumHTTPClientResponseSize = int64(20 << 20)
	maximumHTTPClientRequestSize  = 5 << 20
	maximumHTTPClientHistory      = 100
	maximumHistoryBodyPreview     = 64 << 10
	privateNetworkApprovalTTL     = 2 * time.Minute
)

type privateNetworkApproval struct {
	origin    string
	requestID string
	expiresAt time.Time
}

// HTTPSecretResolver is the only path by which environment secret references
// become plaintext. Implementations must delegate to the backend secrets
// service; environment files and frontend callers never provide plaintext.
type HTTPSecretResolver interface {
	ResolveHTTPSecret(ctx context.Context, reference string) (string, error)
}

// SettingsHTTPSecretResolver adapts the existing encrypted SettingsService
// secret storage. Supported references are ai-api-key and ai-provider:<id>.
type SettingsHTTPSecretResolver struct {
	settings *SettingsService
}

func NewSettingsHTTPSecretResolver(settings *SettingsService) *SettingsHTTPSecretResolver {
	return &SettingsHTTPSecretResolver{settings: settings}
}

func (r *SettingsHTTPSecretResolver) ResolveHTTPSecret(_ context.Context, reference string) (string, error) {
	if r == nil || r.settings == nil {
		return "", fmt.Errorf("secrets service is unavailable")
	}
	switch {
	case reference == keyringAccount:
		return r.settings.getDecryptedAPIKey()
	case strings.HasPrefix(reference, "ai-provider:"):
		id := strings.TrimSpace(strings.TrimPrefix(reference, "ai-provider:"))
		if id == "" {
			return "", fmt.Errorf("secret reference %q has an empty provider id", reference)
		}
		return r.settings.getAPIKeyForConfig(id)
	default:
		return "", fmt.Errorf("secret reference %q is not available from the secrets service", reference)
	}
}

// HTTPClientService executes parsed HTTP requests under strict network and
// resource policies, and persists only sanitized history.
type HTTPClientService struct {
	configDir string
	resolver  HTTPSecretResolver

	mu                      sync.Mutex
	transport               http.RoundTripper
	inFlight                map[string]context.CancelFunc
	history                 []HTTPHistoryEntry
	privateNetworkApprovals map[string]privateNetworkApproval
	approvePrivateNetwork   func(origin string) bool
	privateNetworkNow       func() time.Time
}

func NewHTTPClientService(configDir string, resolver HTTPSecretResolver) *HTTPClientService {
	service := &HTTPClientService{
		configDir:               configDir,
		resolver:                resolver,
		inFlight:                make(map[string]context.CancelFunc),
		privateNetworkApprovals: make(map[string]privateNetworkApproval),
		approvePrivateNetwork:   nativePrivateNetworkApproval,
		privateNetworkNow:       time.Now,
	}
	service.loadHistory()
	return service
}

func nativePrivateNetworkApproval(origin string) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Allow private network request").SetMessage(
		fmt.Sprintf("Allow one HTTP request to private origin?\n\n%s", origin),
	)
	dialog.AddButton("Allow once").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("Cancel").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

// RequestPrivateNetworkAccess asks the backend-owned native UI to authorize a
// single request to one private origin. The returned capability is short-lived
// and bound to both the canonical origin and renderer-generated request ID.
func (s *HTTPClientService) RequestPrivateNetworkAccess(targetOrigin, requestID string) (string, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "", fmt.Errorf("request id is required")
	}
	origin, err := canonicalHTTPOrigin(targetOrigin, true)
	if err != nil {
		return "", fmt.Errorf("invalid private-network origin: %w", err)
	}
	if _, err := ValidateNonPrivateURL(origin); err == nil {
		return "", fmt.Errorf("origin %q is public and does not require private-network approval", origin)
	}
	approver := s.approvePrivateNetwork
	if approver == nil {
		approver = nativePrivateNetworkApproval
	}
	if !approver(origin) {
		return "", fmt.Errorf("private-network access was not approved: %w", ErrNotAllowed)
	}

	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create private-network token: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	now := s.privateNetworkNow
	if now == nil {
		now = time.Now
	}
	s.mu.Lock()
	s.privateNetworkApprovals[token] = privateNetworkApproval{
		origin: origin, requestID: requestID, expiresAt: now().Add(privateNetworkApprovalTTL),
	}
	s.mu.Unlock()
	return token, nil
}

// ParseHTTPFile exposes the pure parser through the Wails service binding.
func (s *HTTPClientService) ParseHTTPFile(content string, environment HTTPEnvironment) ([]HTTPRequest, error) {
	return ParseHTTPFile(content, environment)
}

// ParseHTTPEnvironment exposes the environment parser through the binding.
func (s *HTTPClientService) ParseHTTPEnvironment(content, environmentName string) (HTTPEnvironment, error) {
	return ParseHTTPEnvironment(content, environmentName)
}

// setHTTPTransport injects a transport for tests. The service retains
// ownership of timeout, redirect, body-size, and URL validation policies.
//
//wails:ignore
func (s *HTTPClientService) setHTTPTransport(transport http.RoundTripper) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transport = transport
}

func (s *HTTPClientService) SendRequest(input HTTPRequest, options HTTPRequestOptions) (HTTPResponse, error) {
	started := time.Now()
	requestID := strings.TrimSpace(options.RequestID)
	if requestID == "" {
		requestID = newHTTPRequestID()
	}
	result := HTTPResponse{RequestID: requestID}
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = http.MethodGet
	}
	if !httpMethodPattern.MatchString(method) {
		return result, fmt.Errorf("invalid HTTP method %q", input.Method)
	}
	if len(input.Body) > maximumHTTPClientRequestSize {
		return result, fmt.Errorf("request body exceeds max size %d bytes", maximumHTTPClientRequestSize)
	}
	timeout := time.Duration(options.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultHTTPClientTimeout
	}
	if timeout > maximumHTTPClientTimeout {
		return result, fmt.Errorf("timeout exceeds maximum %s", maximumHTTPClientTimeout)
	}
	maxResponseBytes := options.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultHTTPClientResponseSize
	}
	if maxResponseBytes > maximumHTTPClientResponseSize {
		return result, fmt.Errorf("response body limit exceeds maximum %d bytes", maximumHTTPClientResponseSize)
	}
	maxRedirects := options.MaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = 5
	}
	if maxRedirects > 10 {
		return result, fmt.Errorf("redirect limit exceeds maximum 10")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	s.mu.Lock()
	if _, exists := s.inFlight[requestID]; exists {
		s.mu.Unlock()
		cancel()
		return result, fmt.Errorf("request id %q is already active", requestID)
	}
	s.inFlight[requestID] = cancel
	transport := s.transport
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.inFlight, requestID)
		s.mu.Unlock()
	}()

	urlContainsSecret := requestValueUsesHTTPSecret(input.URL, input.SecretRefs)
	resolved, secretValues, err := s.resolveRequestSecrets(ctx, input)
	if err != nil {
		return result, err
	}
	sensitiveURLs := make([]string, 0, 2)
	if urlContainsSecret {
		sensitiveURLs = append(sensitiveURLs, resolved.URL)
	}
	approvedPrivateOrigin, err := s.authorizeHTTPClientURL(
		resolved.URL,
		requestID,
		strings.TrimSpace(options.PrivateNetworkToken),
	)
	if err != nil {
		return result, fmt.Errorf("request URL blocked: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, resolved.URL, strings.NewReader(resolved.Body))
	if err != nil {
		return result, fmt.Errorf("create HTTP request: %s", sanitizeHTTPError(err.Error(), secretValues, sensitiveURLs...))
	}
	if urlContainsSecret {
		sensitiveURLs = append(sensitiveURLs, req.URL.String())
	}
	for name, value := range resolved.Headers {
		if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return result, fmt.Errorf("HTTP header %q contains a newline", name)
		}
		req.Header.Set(name, value)
	}
	req.Header.Set("User-Agent", "koyori-ide-http-client/1.0")

	if transport == nil {
		if approvedPrivateOrigin != "" {
			transport = http.DefaultTransport.(*http.Transport).Clone()
		} else {
			transport = NewSSRFSafeTransport()
		}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(redirect *http.Request, via []*http.Request) error {
			if urlContainsSecret {
				sensitiveURLs = append(sensitiveURLs, redirect.URL.String())
			}
			if len(via) >= maxRedirects {
				return fmt.Errorf("redirect blocked: stopped after %d redirects", maxRedirects)
			}
			if err := validateHTTPClientRedirectURL(redirect.URL.String(), approvedPrivateOrigin); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			if len(via) > 0 && !sameHTTPOrigin(via[0], redirect) {
				for name := range redirect.Header {
					if isSensitiveHTTPHeader(name) {
						redirect.Header.Del(name)
					}
				}
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		result.DurationMs = time.Since(started).Milliseconds()
		safeMessage := sanitizeHTTPError(err.Error(), secretValues, sensitiveURLs...)
		safeErr := fmt.Errorf("%s", safeMessage)
		historyErr := s.recordHistory(input, result, safeErr, secretValues)
		if historyErr != nil {
			return result, fmt.Errorf("send HTTP request: %s (persist history: %v)", safeMessage, historyErr)
		}
		return result, fmt.Errorf("send HTTP request: %s", safeMessage)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return result, fmt.Errorf("read HTTP response: %s", sanitizeHTTPError(err.Error(), secretValues, sensitiveURLs...))
	}
	if int64(len(body)) > maxResponseBytes {
		err = fmt.Errorf("response body exceeds max size %d bytes", maxResponseBytes)
		result.DurationMs = time.Since(started).Milliseconds()
		_ = s.recordHistory(input, result, err, secretValues)
		return result, err
	}
	result.Status = resp.StatusCode
	result.StatusText = http.StatusText(resp.StatusCode)
	result.Headers = SanitizeHTTPHeaders(flattenHTTPHeaders(resp.Header))
	result.Body = string(body)
	result.DurationMs = time.Since(started).Milliseconds()
	if err := s.recordHistory(input, result, nil, secretValues); err != nil {
		return result, fmt.Errorf("persist HTTP history: %w", err)
	}
	return result, nil
}

func (s *HTTPClientService) resolveRequestSecrets(ctx context.Context, input HTTPRequest) (HTTPRequest, []string, error) {
	resolved := input
	resolved.Headers = cloneStringMap(input.Headers)
	secretValues := make([]string, 0, len(input.SecretRefs))
	for variable, reference := range input.SecretRefs {
		if !requestUsesHTTPSecretVariable(resolved, variable) {
			continue
		}
		if s.resolver == nil {
			return resolved, nil, fmt.Errorf("resolve secret %q: secrets service is unavailable", variable)
		}
		plaintext, err := s.resolver.ResolveHTTPSecret(ctx, reference)
		if err != nil {
			return resolved, nil, fmt.Errorf("resolve secret %q: %w", variable, err)
		}
		if plaintext == "" {
			return resolved, nil, fmt.Errorf("resolve secret %q: secret is empty or missing", variable)
		}
		secretValues = append(secretValues, plaintext)
		placeholder := "{{" + variable + "}}"
		resolved.URL = strings.ReplaceAll(resolved.URL, placeholder, plaintext)
		resolved.Body = strings.ReplaceAll(resolved.Body, placeholder, plaintext)
		for name, value := range resolved.Headers {
			resolved.Headers[name] = strings.ReplaceAll(value, placeholder, plaintext)
		}
	}
	for _, value := range append([]string{resolved.URL, resolved.Body}, mapValues(resolved.Headers)...) {
		if match := httpVariablePattern.FindStringSubmatch(value); match != nil {
			return resolved, nil, fmt.Errorf("unresolved variable %q at send time", match[1])
		}
	}
	return resolved, secretValues, nil
}

func requestUsesHTTPSecretVariable(request HTTPRequest, variable string) bool {
	placeholder := "{{" + variable + "}}"
	if strings.Contains(request.URL, placeholder) || strings.Contains(request.Body, placeholder) {
		return true
	}
	for _, value := range request.Headers {
		if strings.Contains(value, placeholder) {
			return true
		}
	}
	return false
}

func requestValueUsesHTTPSecret(value string, secretRefs map[string]string) bool {
	for variable := range secretRefs {
		if strings.Contains(value, "{{"+variable+"}}") {
			return true
		}
	}
	return false
}

func (s *HTTPClientService) authorizeHTTPClientURL(rawURL, requestID, token string) (string, error) {
	if _, err := ValidateNonPrivateURL(rawURL); err == nil && token == "" {
		return "", nil
	}
	if err := ValidateBaseURL(rawURL); err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("private-network request requires backend approval")
	}
	origin, err := canonicalHTTPOrigin(rawURL, false)
	if err != nil {
		return "", err
	}

	now := s.privateNetworkNow
	if now == nil {
		now = time.Now
	}
	s.mu.Lock()
	approval, exists := s.privateNetworkApprovals[token]
	delete(s.privateNetworkApprovals, token)
	s.mu.Unlock()
	if !exists {
		return "", fmt.Errorf("private-network token is missing, invalid, or already used")
	}
	if !now().Before(approval.expiresAt) {
		return "", fmt.Errorf("private-network token expired")
	}
	if approval.requestID != requestID {
		return "", fmt.Errorf("private-network token request id mismatch")
	}
	if approval.origin != origin {
		return "", fmt.Errorf("private-network token origin mismatch")
	}
	return approval.origin, nil
}

func validateHTTPClientRedirectURL(rawURL, approvedPrivateOrigin string) error {
	if _, err := ValidateNonPrivateURL(rawURL); err == nil {
		return nil
	}
	if err := ValidateBaseURL(rawURL); err != nil {
		return err
	}
	if approvedPrivateOrigin == "" {
		return fmt.Errorf("private-network redirect requires backend approval")
	}
	origin, err := canonicalHTTPOrigin(rawURL, false)
	if err != nil {
		return err
	}
	if origin != approvedPrivateOrigin {
		return fmt.Errorf("private-network redirect origin %q was not approved", origin)
	}
	return nil
}

func canonicalHTTPOrigin(rawURL string, originOnly bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", err
	}
	if err := ValidateBaseURL(parsed.String()); err != nil {
		return "", err
	}
	if parsed.User != nil {
		return "", fmt.Errorf("URL userinfo is not allowed")
	}
	if originOnly && ((parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "") {
		return "", fmt.Errorf("target must be an origin without path, query, or fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	host := hostname
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}

func sameHTTPOrigin(a, b *http.Request) bool {
	return strings.EqualFold(a.URL.Scheme, b.URL.Scheme) && strings.EqualFold(a.URL.Host, b.URL.Host)
}

func (s *HTTPClientService) CancelRequest(requestID string) bool {
	s.mu.Lock()
	cancel, ok := s.inFlight[requestID]
	s.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (s *HTTPClientService) GetHistory() ([]HTTPHistoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]HTTPHistoryEntry(nil), s.history...), nil
}

func (s *HTTPClientService) ClearHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = nil
	if s.configDir == "" {
		return nil
	}
	path := s.historyPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear HTTP history: %w", err)
	}
	return nil
}

func (s *HTTPClientService) recordHistory(request HTTPRequest, response HTTPResponse, sendErr error, secretValues []string) error {
	entry := HTTPHistoryEntry{
		ID:              response.RequestID,
		Name:            request.Name,
		Method:          strings.ToUpper(request.Method),
		URL:             request.URL,
		RequestHeaders:  SanitizeHTTPHeaders(request.Headers),
		Status:          response.Status,
		StatusText:      response.StatusText,
		ResponseHeaders: cloneStringMap(response.Headers),
		ResponseBody:    truncateHTTPHistoryBody(sanitizeHTTPError(response.Body, secretValues)),
		DurationMs:      response.DurationMs,
		CreatedAt:       time.Now().UTC(),
	}
	if sendErr != nil {
		entry.Error = sanitizeHTTPError(sendErr.Error(), secretValues)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append([]HTTPHistoryEntry{entry}, s.history...)
	if len(s.history) > maximumHTTPClientHistory {
		s.history = s.history[:maximumHTTPClientHistory]
	}
	if s.configDir == "" {
		return nil
	}
	return atomicWriteJSON(s.historyPath(), s.history, 0o600)
}

func (s *HTTPClientService) loadHistory() {
	if s.configDir == "" {
		return
	}
	raw, err := os.ReadFile(s.historyPath())
	if err != nil {
		return
	}
	var history []HTTPHistoryEntry
	if json.Unmarshal(raw, &history) != nil {
		return
	}
	if len(history) > maximumHTTPClientHistory {
		history = history[:maximumHTTPClientHistory]
	}
	s.history = history
}

func (s *HTTPClientService) historyPath() string {
	return filepath.Join(s.configDir, "http-client", "history.json")
}

func SanitizeHTTPHeaders(headers map[string]string) map[string]string {
	sanitized := make(map[string]string, len(headers))
	for name, value := range headers {
		if isSensitiveHTTPHeader(name) {
			sanitized[name] = "[REDACTED]"
		} else {
			sanitized[name] = value
		}
	}
	return sanitized
}

func isSensitiveHTTPHeader(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	switch normalized {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "x-auth-token":
		return true
	default:
		return strings.Contains(normalized, "secret") || strings.Contains(normalized, "token")
	}
}

func flattenHTTPHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for name, values := range headers {
		result[name] = strings.Join(values, ", ")
	}
	return result
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func truncateHTTPHistoryBody(body string) string {
	if len(body) <= maximumHistoryBodyPreview {
		return body
	}
	return body[:maximumHistoryBodyPreview]
}

func sanitizeHTTPError(message string, secretValues []string, sensitiveURLs ...string) string {
	urls := append([]string(nil), sensitiveURLs...)
	sort.Slice(urls, func(i, j int) bool { return len(urls[i]) > len(urls[j]) })
	for _, sensitiveURL := range urls {
		if sensitiveURL != "" {
			message = strings.ReplaceAll(message, sensitiveURL, "[REDACTED URL]")
		}
	}

	candidates := make([]string, 0, len(secretValues)*5)
	for _, secret := range secretValues {
		if secret == "" {
			continue
		}
		pathEscaped := url.PathEscape(secret)
		candidates = append(candidates,
			secret,
			url.QueryEscape(secret),
			pathEscaped,
			strings.ReplaceAll(pathEscaped, "%2F", "/"),
			strings.ReplaceAll(secret, " ", "%20"),
		)
	}
	sort.Slice(candidates, func(i, j int) bool { return len(candidates[i]) > len(candidates[j]) })
	for _, candidate := range candidates {
		if candidate != "" {
			message = strings.ReplaceAll(message, candidate, "[REDACTED]")
		}
	}
	return message
}

func newHTTPRequestID() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "http-" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("http-%d", time.Now().UnixNano())
}
