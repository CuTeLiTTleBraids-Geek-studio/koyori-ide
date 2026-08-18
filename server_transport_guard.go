//go:build server

package main

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const serverRequestBodyLimit int64 = 32 << 20

// serverTransportMiddleware closes the unsafe defaults in Wails' raw HTTP
// transport. Remote deployments must still use the token-authenticated
// gateway; this guard protects the loopback-only standalone server from local
// browser CSRF and unauthenticated query-triggered RPC calls.
func serverTransportMiddleware() application.Middleware {
	if os.Getenv("KOYORI_SERVER_GATEWAY_MODE") == "1" {
		return nil
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/wails/runtime":
				if r.Method != http.MethodPost || !sameServerOrigin(r) {
					http.Error(w, "forbidden runtime request", http.StatusForbidden)
					return
				}
				if r.Body != nil {
					r.Body = http.MaxBytesReader(w, r.Body, serverRequestBodyLimit)
				}
			case "/wails/events":
				if r.Method != http.MethodGet || !sameServerOrigin(r) {
					http.Error(w, "forbidden event request", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func sameServerOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) != 1 {
		return false
	}
	origin := strings.TrimSpace(values[0])
	if origin == "" || strings.EqualFold(origin, "null") {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Hostname() == "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	requestURL := &url.URL{Scheme: scheme, Host: r.Host}
	return strings.EqualFold(parsed.Scheme, requestURL.Scheme) &&
		strings.EqualFold(parsed.Hostname(), requestURL.Hostname()) &&
		normalizedOriginPort(parsed) == normalizedOriginPort(requestURL)
}

func normalizedOriginPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	return "80"
}
