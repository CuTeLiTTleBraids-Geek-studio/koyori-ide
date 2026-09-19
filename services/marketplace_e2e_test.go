//go:build e2e

package services

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAllowLoopbackMarketplaceFetchesForE2EAdmitsOnlyThatRegistry(t *testing.T) {
	t.Cleanup(func() {
		validateDownloadURL = ValidateNonPrivateURL
		marketplaceTransport = newMarketplaceSSRFTransport
	})

	var registry *httptest.Server
	registry = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/koyori-e2e-g24/runtime-lifecycle" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "g24"})
	}))
	t.Cleanup(registry.Close)

	svc := NewMarketplaceService(t.TempDir())
	metaURL := registry.URL + "/koyori-e2e-g24/runtime-lifecycle"
	if _, err := svc.httpGetJSON(metaURL); err == nil || !strings.Contains(err.Error(), "private/loopback/link-local") {
		t.Fatalf("production gate = %v, want loopback rejection", err)
	}

	restore, err := AllowLoopbackMarketplaceFetchesForE2E(registry.URL)
	if err != nil {
		t.Fatalf("AllowLoopbackMarketplaceFetchesForE2E: %v", err)
	}
	body, err := svc.httpGetJSON(metaURL)
	if err != nil {
		t.Fatalf("admitted registry fetch: %v", err)
	}
	if !strings.Contains(string(body), `"ok":"g24"`) && !strings.Contains(string(body), `"ok": "g24"`) {
		t.Fatalf("admitted registry body = %s", body)
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":"other"}`)
	}))
	t.Cleanup(other.Close)
	if _, err := svc.httpGetJSON(other.URL + "/secret"); err == nil || !strings.Contains(err.Error(), "private/loopback/link-local") {
		t.Fatalf("other loopback host = %v, want still rejected", err)
	}

	restore()
	if _, err := svc.httpGetJSON(metaURL); err == nil || !strings.Contains(err.Error(), "private/loopback/link-local") {
		t.Fatalf("after restore = %v, want loopback rejection", err)
	}
}
