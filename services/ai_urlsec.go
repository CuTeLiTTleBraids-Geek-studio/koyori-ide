package services

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ValidateBaseURL validates a user-supplied API base URL to prevent API key
// leakage to malicious endpoints (N-73).
//
// Rules:
//  1. Must parse as a valid URL with a non-empty host.
//  2. Scheme must be http or https only (rejects file:, data:, ftp:, gopher:,
//     javascript:, etc. which could exfiltrate the API key or read local files).
//  3. Must not contain embedded userinfo (e.g. "http://user:pass@host") — this
//     is a credential-leakage vector and is never needed for AI providers.
//  4. For non-loopback hosts, scheme MUST be https. Loopback hosts
//     (localhost, 127.0.0.1, ::1, *.localhost) are allowed over plain http to
//     support local LLM servers (Ollama, LM Studio, llama.cpp).
//
// The check is intentionally NOT an allowlist of specific provider hosts —
// users need to add custom OpenAI-compatible endpoints. The scheme + loopback
// enforcement blocks the main exfiltration vectors while preserving flexibility.
func ValidateBaseURL(baseURL string) error {
	if baseURL == "" {
		return fmt.Errorf("base URL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid base URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("base URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("base URL must have a host")
	}
	if u.User != nil {
		return fmt.Errorf("base URL must not contain embedded credentials")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("base URL must have a host")
	}
	if !isLoopbackHost(host) && scheme != "https" {
		return fmt.Errorf("base URL for non-loopback host %q must use https", host)
	}
	return nil
}

// NormalizeAIBaseURL trims whitespace and trailing slashes, then strips a
// trailing "/v1" segment. Backend paths always start with "/v1/..."
// (chat/completions, messages, models). Users and some UIs paste
// "https://api.openai.com/v1" or "http://localhost:1234/v1", which previously
// produced "/v1/v1/chat/completions" and a 404 "page not found" body.
func NormalizeAIBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	baseURL = strings.TrimRight(baseURL, "/")
	lower := strings.ToLower(baseURL)
	if strings.HasSuffix(lower, "/v1") {
		baseURL = baseURL[:len(baseURL)-3]
		baseURL = strings.TrimRight(baseURL, "/")
	}
	return baseURL
}

// JoinAIEndpoint joins a user BaseURL with an API path such as
// "/v1/chat/completions" or "/v1/messages". It normalizes the base so a
// trailing "/v1" is not doubled.
func JoinAIEndpoint(baseURL, apiPath string) string {
	base := NormalizeAIBaseURL(baseURL)
	if apiPath == "" {
		return base
	}
	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}
	if base == "" {
		return apiPath
	}
	return base + apiPath
}

// isLoopbackHost reports whether host is a loopback address. It accepts:
//   - "localhost"
//   - "*.localhost" (e.g. "ollama.localhost")
//   - IPv4 loopback "127.0.0.1" (and any 127.x.x.x)
//   - IPv6 loopback "::1"
//
// For IP literals, net.ParseIP is used and the result is checked with
// net.IP.IsLoopback(), which correctly handles the full 127.0.0.0/8 range
// and the IPv6 ::1.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// isPrivateHost reports whether ip points at a network location that must
// never be contacted by transports carrying sensitive headers (e.g. MCP
// http/sse with Authorization). It rejects:
//   - loopback (127.0.0.0/8, ::1)
//   - RFC 1918 private ranges (10/8, 172.16/12, 192.168/16) and IPv6 fc00::/7
//   - link-local unicast (169.254.0.0/16, IPv6 fe80::/10) — covers the
//     cloud metadata endpoint 169.254.169.254
//   - unspecified (0.0.0.0, ::)
//
// A nil ip is treated as private (fail-closed).
func isPrivateHost(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// validateResolvedHosts resolves host via A/AAAA and rejects if any returned
// IP is private (C-1 step 3). Resolution failure is an error (fail-closed),
// because we cannot confirm the host is safe.
func validateResolvedHosts(host string) error {
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if isPrivateHost(ip) {
			return fmt.Errorf("host %q resolves to private/loopback/link-local address %s", host, ip)
		}
	}
	return nil
}

// ValidateNonPrivateURL validates a URL for use in transports that carry
// sensitive headers (MCP http/sse with Authorization — C-1).
//
// It applies ValidateBaseURL's scheme/userinfo/https rules first, then
// additionally rejects ALL private/loopback/link-local IPs (including
// "localhost"), because an MCP server reachable on the local network or
// loopback could exfiltrate credentials to internal services (e.g. cloud
// metadata at 169.254.169.254).
//
// For domain hosts, A/AAAA records are resolved and every returned IP is
// checked. The returned *url.URL is the parsed URL (host already validated).
func ValidateNonPrivateURL(rawURL string) (*url.URL, error) {
	if err := ValidateBaseURL(rawURL); err != nil {
		return nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("url must have a host")
	}
	// "localhost" / "*.localhost" bypass DNS and always resolve to loopback.
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, fmt.Errorf("url must not target loopback host %q", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateHost(ip) {
			return nil, fmt.Errorf("url host %s is a private/loopback/link-local address", host)
		}
	} else {
		if err := validateResolvedHosts(host); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// NewSSRFSafeTransport returns an *http.Transport whose DialContext
// re-validates the resolved IP at connect time. This defeats DNS rebinding:
// a host that resolved to a public IP during ValidateNonPrivateURL could be
// reconfigured to resolve to 169.254.169.254 by the time the dial happens.
// The dialer rejects any address that isPrivateHost flags (C-1 step 4).
func NewSSRFSafeTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("ssrf guard: split host port %q: %w", addr, err)
			}
			// "localhost" / "*.localhost" → always loopback.
			if host == "localhost" || strings.HasSuffix(host, ".localhost") {
				return nil, fmt.Errorf("ssrf guard: refusing loopback host %q", host)
			}
			// If host is an IP literal, validate directly without resolution.
			if ip := net.ParseIP(host); ip != nil {
				if isPrivateHost(ip) {
					return nil, fmt.Errorf("ssrf guard: refusing private address %s", host)
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
			// Domain: resolve via the default resolver and validate every IP.
			ipAddrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("ssrf guard: resolve %q: %w", host, err)
			}
			if len(ipAddrs) == 0 {
				return nil, fmt.Errorf("ssrf guard: no addresses for %q", host)
			}
			for _, ia := range ipAddrs {
				if isPrivateHost(ia.IP) {
					return nil, fmt.Errorf("ssrf guard: host %q resolves to private address %s", host, ia.IP)
				}
			}
			// Dial the first reachable non-private address.
			var lastErr error
			for _, ia := range ipAddrs {
				conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ia.IP.String(), port))
				if derr == nil {
					return conn, nil
				}
				lastErr = derr
			}
			return nil, fmt.Errorf("ssrf guard: dial %q failed: %w", host, lastErr)
		},
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
