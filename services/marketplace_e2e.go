//go:build e2e

package services

import (
	"fmt"
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
