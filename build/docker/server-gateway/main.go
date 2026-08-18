package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultGatewayHost = "0.0.0.0"
	defaultGatewayPort = 8080
	defaultBackendPort = 8081
	defaultBodyLimit   = 32 << 20
	defaultShutdown    = 8 * time.Second
	backendReapTimeout = time.Second
	minimumTokenBytes  = 32
	sessionCookieName  = "koyori_server_session"
	loginPath          = "/__koyori/auth"
	logoutPath         = "/__koyori/logout"
)

type gatewayConfig struct {
	token           []byte
	listenAddress   string
	backendURL      *url.URL
	backendBinary   string
	maxBodyBytes    int64
	tlsCertFile     string
	tlsKeyFile      string
	externalOrigin  *origin
	secureCookie    bool
	shutdownTimeout time.Duration
}

type gateway struct {
	tokenDigest    [sha256.Size]byte
	sessionDigest  [sha256.Size]byte
	secureCookie   bool
	externalOrigin *origin
	maxBodyBytes   int64
	proxy          http.Handler
}

type authenticationMethod uint8

type origin struct {
	scheme string
	host   string
	port   string
}

const (
	authenticationNone authenticationMethod = iota
	authenticationBearer
	authenticationCookie
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--check-env-token" {
		if _, err := loadToken(os.Getenv("KOYORI_SERVER_TOKEN"), ""); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("server gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	backend := exec.Command(cfg.backendBinary)
	configureBackendProcess(backend)
	backend.Env = backendEnvironment(os.Environ(), cfg.backendURL.Port())
	backend.Stdout = os.Stdout
	backend.Stderr = os.Stderr
	if err := backend.Start(); err != nil {
		return fmt.Errorf("start internal Wails server: %w", err)
	}
	if err := trackBackendProcess(backend); err != nil {
		stopBackendWithTimeout(backend, nil, cfg.shutdownTimeout)
		return fmt.Errorf("track internal Wails process tree: %w", err)
	}

	backendDone := make(chan error, 1)
	go func() { backendDone <- backend.Wait() }()

	handler, err := newGateway(cfg)
	if err != nil {
		stopBackendWithTimeout(backend, backendDone, cfg.shutdownTimeout)
		return err
	}
	server := &http.Server{
		Addr:              cfg.listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	serveDone := make(chan error, 1)
	go func() {
		if cfg.tlsCertFile != "" {
			serveDone <- server.ListenAndServeTLS(cfg.tlsCertFile, cfg.tlsKeyFile)
			return
		}
		serveDone <- server.ListenAndServe()
	}()

	slog.Info("authenticated server gateway started",
		"address", cfg.listenAddress,
		"tls", cfg.tlsCertFile != "",
		"backend", cfg.backendURL.String(),
	)

	var result error
	backendExited := false
	select {
	case <-ctx.Done():
	case err := <-backendDone:
		backendExited = true
		if err == nil {
			result = errors.New("internal Wails server stopped unexpectedly")
		} else {
			result = fmt.Errorf("internal Wails server stopped: %w", err)
		}
	case err := <-serveDone:
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("gateway HTTP server: %w", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
	defer shutdownCancel()
	if backendExited {
		// Wait() only reaps the Wails parent. Its terminal/PTY children are
		// still part of the process group/job and must be terminated even when
		// the parent exited before the gateway observed the shutdown signal.
		if err := killBackend(backend); err != nil {
			slog.Warn("failed to clean up internal Wails process tree after exit", "error", err)
		}
	} else {
		if err := signalBackend(backend); err != nil {
			slog.Warn("failed to signal internal Wails server", "error", err)
		}
	}
	if err := server.Shutdown(shutdownCtx); err != nil && result == nil {
		result = fmt.Errorf("shutdown gateway: %w", err)
	}
	if !backendExited {
		_ = waitOrKillBackend(shutdownCtx, backend, backendDone)
	}
	return result
}

func loadConfig() (gatewayConfig, error) {
	token, err := loadToken(os.Getenv("KOYORI_SERVER_TOKEN"), os.Getenv("KOYORI_SERVER_TOKEN_FILE"))
	if err != nil {
		return gatewayConfig{}, err
	}

	gatewayPort, err := envPort("KOYORI_GATEWAY_PORT", defaultGatewayPort)
	if err != nil {
		return gatewayConfig{}, err
	}
	backendPort, err := envPort("KOYORI_INTERNAL_PORT", defaultBackendPort)
	if err != nil {
		return gatewayConfig{}, err
	}
	bodyLimit, err := envPositiveInt64("KOYORI_MAX_REQUEST_BYTES", defaultBodyLimit)
	if err != nil {
		return gatewayConfig{}, err
	}

	certFile := strings.TrimSpace(os.Getenv("KOYORI_TLS_CERT_FILE"))
	keyFile := strings.TrimSpace(os.Getenv("KOYORI_TLS_KEY_FILE"))
	if (certFile == "") != (keyFile == "") {
		return gatewayConfig{}, errors.New("KOYORI_TLS_CERT_FILE and KOYORI_TLS_KEY_FILE must be set together")
	}
	externalOrigin, err := parseOptionalExternalOrigin(os.Getenv("KOYORI_EXTERNAL_ORIGIN"))
	if err != nil {
		return gatewayConfig{}, err
	}
	secureCookie := false
	if certFile != "" || (externalOrigin != nil && externalOrigin.scheme == "https") {
		secureCookie = true
	}

	host := strings.TrimSpace(os.Getenv("KOYORI_GATEWAY_HOST"))
	if host == "" {
		host = defaultGatewayHost
	}
	backendURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", backendPort))
	return gatewayConfig{
		token:           token,
		listenAddress:   net.JoinHostPort(host, strconv.Itoa(gatewayPort)),
		backendURL:      backendURL,
		backendBinary:   envDefault("KOYORI_SERVER_BINARY", "/server-internal"),
		maxBodyBytes:    bodyLimit,
		tlsCertFile:     certFile,
		tlsKeyFile:      keyFile,
		externalOrigin:  externalOrigin,
		secureCookie:    secureCookie,
		shutdownTimeout: defaultShutdown,
	}, nil
}

func loadToken(value, filename string) ([]byte, error) {
	if value != "" && filename != "" {
		return nil, errors.New("set only one of KOYORI_SERVER_TOKEN or KOYORI_SERVER_TOKEN_FILE")
	}
	if filename != "" {
		contents, err := os.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("read KOYORI_SERVER_TOKEN_FILE: %w", err)
		}
		value = strings.TrimSpace(string(contents))
	}
	if len(value) < minimumTokenBytes {
		return nil, fmt.Errorf("KOYORI_SERVER_TOKEN must contain at least %d bytes", minimumTokenBytes)
	}
	return []byte(value), nil
}

func envPort(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be a port between 1 and 65535", name)
	}
	return port, nil
}

func envPositiveInt64(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func parseOptionalExternalOrigin(value string) (*origin, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := parseOrigin(value)
	if err != nil {
		return nil, fmt.Errorf("KOYORI_EXTERNAL_ORIGIN: %w", err)
	}
	if parsed.scheme != "https" && !isLoopbackHost(parsed.host) {
		return nil, errors.New("KOYORI_EXTERNAL_ORIGIN must use https for non-loopback hosts")
	}
	return &parsed, nil
}

func parseOrigin(value string) (origin, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return origin{}, errors.New("must be an absolute HTTP(S) origin")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return origin{}, errors.New("scheme must be http or https")
	}
	if parsed.User != nil || parsed.Hostname() == "" || (parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return origin{}, errors.New("must contain only scheme, host, and optional port")
	}
	port := parsed.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return origin{}, errors.New("port must be between 1 and 65535")
	}
	return origin{scheme: scheme, host: strings.ToLower(parsed.Hostname()), port: port}, nil
}

func requestOrigin(r *http.Request) (origin, error) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return parseOrigin(scheme + "://" + r.Host)
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func requestHostMatches(r *http.Request, expected *origin) bool {
	parsed, err := url.Parse("//" + r.Host)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if expected == nil {
		return isLoopbackHost(host)
	}
	port := parsed.Port()
	if port == "" {
		port = defaultOriginPort(expected.scheme)
	}
	return host == expected.host && port == expected.port
}

func defaultOriginPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

func envDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func backendEnvironment(environ []string, port string) []string {
	filtered := make([]string, 0, len(environ)+2)
	blocked := map[string]struct{}{
		"KOYORI_SERVER_TOKEN":        {},
		"KOYORI_SERVER_TOKEN_FILE":   {},
		"KOYORI_GATEWAY_HOST":        {},
		"KOYORI_GATEWAY_PORT":        {},
		"KOYORI_INTERNAL_PORT":       {},
		"KOYORI_MAX_REQUEST_BYTES":   {},
		"KOYORI_TLS_CERT_FILE":       {},
		"KOYORI_TLS_KEY_FILE":        {},
		"KOYORI_EXTERNAL_ORIGIN":     {},
		"KOYORI_SERVER_GATEWAY_MODE": {},
		"KOYORI_SERVER_BINARY":       {},
		"WAILS_SERVER_HOST":          {},
		"WAILS_SERVER_PORT":          {},
	}
	for _, item := range environ {
		name, _, _ := strings.Cut(item, "=")
		if _, drop := blocked[strings.ToUpper(name)]; !drop {
			filtered = append(filtered, item)
		}
	}
	return append(filtered,
		"WAILS_SERVER_HOST=127.0.0.1",
		"WAILS_SERVER_PORT="+port,
		"KOYORI_SERVER_GATEWAY_MODE=1",
	)
}

func newGateway(cfg gatewayConfig) (http.Handler, error) {
	sessionBytes := make([]byte, 32)
	if _, err := rand.Read(sessionBytes); err != nil {
		return nil, fmt.Errorf("generate session secret: %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(cfg.backendURL)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		slog.Warn("internal Wails server request failed", "error", err)
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}

	g := &gateway{
		tokenDigest:    sha256.Sum256(cfg.token),
		sessionDigest:  sha256.Sum256([]byte(base64.RawURLEncoding.EncodeToString(sessionBytes))),
		secureCookie:   cfg.secureCookie,
		externalOrigin: cfg.externalOrigin,
		maxBodyBytes:   cfg.maxBodyBytes,
		proxy:          proxy,
	}
	return g, nil
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setNoSniffHeaders(w)
	if r.URL.Path == "/health" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Body != nil {
			_ = r.Body.Close()
			r.Body = http.NoBody
			r.ContentLength = 0
		}
		stripCredentials(r)
		g.proxy.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == loginPath {
		g.handleLogin(w, r)
		return
	}
	if r.URL.Path == logoutPath {
		g.handleLogout(w, r)
		return
	}
	if !g.sameOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	authentication := g.authenticationMethod(r)
	if authentication == authenticationNone {
		g.unauthorized(w, r)
		return
	}
	if r.URL.Path == "/wails/runtime" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if authentication == authenticationCookie && len(r.Header.Values("Origin")) == 0 {
			http.Error(w, "origin required for browser RPC", http.StatusForbidden)
			return
		}
	}
	if r.URL.Path == "/wails/events" &&
		authentication == authenticationCookie && len(r.Header.Values("Origin")) == 0 {
		http.Error(w, "origin required for browser event stream", http.StatusForbidden)
		return
	}
	if r.ContentLength > g.maxBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, g.maxBodyBytes)
	}
	stripCredentials(r)
	g.proxy.ServeHTTP(w, r)
}

func (g *gateway) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !g.sameOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil || !g.matchesToken(r.PostForm.Get("token")) {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	sessionValue := base64.RawURLEncoding.EncodeToString(g.sessionDigest[:])
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionValue,
		Path:     "/",
		HttpOnly: true,
		Secure:   g.secureCookie || r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (g *gateway) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !g.sameOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   g.secureCookie || r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (g *gateway) authenticationMethod(r *http.Request) authenticationMethod {
	if scheme, value, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " "); ok &&
		strings.EqualFold(scheme, "Bearer") && g.matchesToken(strings.TrimSpace(value)) {
		return authenticationBearer
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return authenticationNone
	}
	want := base64.RawURLEncoding.EncodeToString(g.sessionDigest[:])
	if constantTimeEqual(cookie.Value, want) {
		return authenticationCookie
	}
	return authenticationNone
}

func (g *gateway) matchesToken(value string) bool {
	digest := sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(digest[:], g.tokenDigest[:]) == 1
}

func constantTimeEqual(left, right string) bool {
	leftDigest := sha256.Sum256([]byte(left))
	rightDigest := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftDigest[:], rightDigest[:]) == 1
}

func (g *gateway) sameOrigin(r *http.Request) bool {
	if !requestHostMatches(r, g.externalOrigin) {
		return false
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin := strings.TrimSpace(origins[0])
	if origin == "" {
		return false
	}
	parsed, err := parseOrigin(origin)
	if err != nil {
		return false
	}
	expected := g.externalOrigin
	if expected == nil {
		request, err := requestOrigin(r)
		if err != nil {
			return false
		}
		expected = &request
	}
	return parsed == *expected
}

func (g *gateway) unauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet && (r.URL.Path == "/" || strings.Contains(r.Header.Get("Accept"), "text/html")) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, loginPage)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"error":"authentication required"}`)
}

func stripCredentials(r *http.Request) {
	r.Header.Del("Authorization")
	// The gateway has already authenticated and origin-checked the request.
	// Do not forward the public Origin to the loopback Wails listener: its
	// same-origin check must not compare the external host with 127.0.0.1.
	r.Header.Del("Origin")
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, cookie := range cookies {
		if cookie.Name != sessionCookieName {
			r.AddCookie(cookie)
		}
	}
}

func setNoSniffHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func stopBackendWithTimeout(cmd *exec.Cmd, done <-chan error, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := signalBackend(cmd); err != nil {
		slog.Warn("failed to signal internal Wails server", "error", err)
	}
	_ = waitOrKillBackend(ctx, cmd, done)
}

func waitOrKillBackend(ctx context.Context, cmd *exec.Cmd, done <-chan error) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	select {
	case err := <-done:
		releaseBackendProcess(cmd)
		return err
	case <-ctx.Done():
		if err := killBackend(cmd); err != nil {
			slog.Warn("failed to kill internal Wails process group", "error", err)
		}
		select {
		case <-done:
			releaseBackendProcess(cmd)
		case <-time.After(backendReapTimeout):
		}
		return ctx.Err()
	}
}

const loginPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Koyori IDE sign in</title></head>
<body><main><h1>Koyori IDE</h1><form method="post" action="/__koyori/auth"><label>Server token <input name="token" type="password" autocomplete="current-password" required autofocus></label><button type="submit">Sign in</button></form></main></body>
</html>`
