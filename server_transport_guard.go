//go:build server

package main

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const serverRequestBodyLimit int64 = 32 << 20

const (
	serverGatewayNonceEnv    = "KOYORI_SERVER_GATEWAY_NONCE"
	serverGatewayNonceHeader = "X-Koyori-Gateway-Nonce"
)

// serverTransportMiddleware closes the unsafe defaults in Wails' raw HTTP
// transport. Standalone server mode accepts only same-origin browser traffic;
// the Docker gateway receives a per-process nonce and is the only trusted
// proxy path. A boolean environment mode is deliberately not sufficient.
func serverTransportMiddleware() application.Middleware {
	nonce := strings.TrimSpace(os.Getenv(serverGatewayNonceEnv))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			internal := validGatewayRequest(r, nonce)
			allowed := internal
			if nonce == "" {
				allowed = sameServerOrigin(r)
			}
			if internal {
				r.Header.Del(serverGatewayNonceHeader)
			}
			switch r.URL.Path {
			case "/wails/runtime":
				if r.Method != http.MethodPost || !allowed {
					http.Error(w, "forbidden runtime request", http.StatusForbidden)
					return
				}
				if r.Body != nil {
					r.Body = http.MaxBytesReader(w, r.Body, serverRequestBodyLimit)
				}
			case "/wails/events":
				if r.Method != http.MethodGet || !allowed {
					http.Error(w, "forbidden event request", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func validGatewayRequest(r *http.Request, nonce string) bool {
	if nonce == "" || len(r.Header.Values(serverGatewayNonceHeader)) != 1 || len(r.Header.Values("Origin")) != 0 {
		return false
	}
	got := strings.TrimSpace(r.Header.Get(serverGatewayNonceHeader))
	if got == "" || len(got) != len(nonce) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(nonce)) == 1
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
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
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
