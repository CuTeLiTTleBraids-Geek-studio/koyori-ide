//go:build e2e

package services

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SetRegistryURLForE2E is available only in the opt-in packaged E2E binary.
// It permits the probe's loopback httptest registry without weakening the
// production SetRegistryURL SSRF guard or adding a renderer binding.
//
//wails:ignore
func (s *MarketplaceService) SetRegistryURLForE2E(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("invalid E2E registry URL")
	}
	s.mu.Lock()
	s.registryURL = strings.TrimRight(rawURL, "/")
	s.mu.Unlock()
	return nil
}

// AllowLoopbackMarketplaceFetchesForE2E lets the packaged G24 probe talk to one
// httptest registry origin. Production ValidateNonPrivateURL still applies to
// every other host, including other loopback/private addresses. Restore before
// returning from the probe so a later fixture cannot keep the hole open.
//
//wails:ignore
func AllowLoopbackMarketplaceFetchesForE2E(registryURL string) (func(), error) {
	registry, err := url.Parse(registryURL)
	if err != nil || registry.User != nil || registry.Host == "" {
		return nil, fmt.Errorf("invalid E2E marketplace registry URL")
	}
	if scheme := strings.ToLower(registry.Scheme); scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("invalid E2E marketplace registry URL")
	}
	originalValidate := validateDownloadURL
	originalTransport := marketplaceTransport
	marketplaceTransport = func() http.RoundTripper { return nil }
	validateDownloadURL = func(raw string) (*url.URL, error) {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(u.Scheme, registry.Scheme) && strings.EqualFold(u.Host, registry.Host) {
			if err := ValidateBaseURL(raw); err != nil {
				return nil, err
			}
			return u, nil
		}
		return ValidateNonPrivateURL(raw)
	}
	return func() {
		validateDownloadURL = originalValidate
		marketplaceTransport = originalTransport
	}, nil
}
