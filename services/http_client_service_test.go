package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type httpRoundTripFunc func(*http.Request) (*http.Response, error)

func (f httpRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type fakeHTTPSecretResolver struct {
	values map[string]string
	calls  atomic.Int32
}

func (f *fakeHTTPSecretResolver) ResolveHTTPSecret(_ context.Context, ref string) (string, error) {
	f.calls.Add(1)
	return f.values[ref], nil
}

func httpTestResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestHTTPClientServiceRedirectRevalidatesPrivateTarget(t *testing.T) {
	service := NewHTTPClientService(t.TempDir(), nil)
	var calls atomic.Int32
	service.setHTTPTransport(httpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		resp := httpTestResponse(req, http.StatusFound, "")
		resp.Header.Set("Location", "http://127.0.0.1/admin")
		return resp, nil
	}))

	_, err := service.SendRequest(HTTPRequest{Method: "GET", URL: "https://203.0.113.10/start"}, HTTPRequestOptions{})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "redirect") {
		t.Fatalf("expected redirect policy error, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("private redirect reached transport; calls = %d", calls.Load())
	}
}

func TestHTTPClientServiceCancellation(t *testing.T) {
	service := NewHTTPClientService(t.TempDir(), nil)
	started := make(chan struct{})
	service.setHTTPTransport(httpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	}))

	done := make(chan error, 1)
	go func() {
		_, err := service.SendRequest(
			HTTPRequest{Method: "GET", URL: "https://203.0.113.11/slow"},
			HTTPRequestOptions{RequestID: "cancel-me", TimeoutMs: 30_000},
		)
		done <- err
	}()
	<-started
	if !service.CancelRequest("cancel-me") {
		t.Fatal("CancelRequest() = false for active request")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cancel") {
			t.Fatalf("expected cancellation error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SendRequest did not stop after cancellation")
	}
	if service.CancelRequest("cancel-me") {
		t.Fatal("completed request remained in the cancellation registry")
	}
}

func TestHTTPClientServiceTimeoutStopsTransport(t *testing.T) {
	service := NewHTTPClientService(t.TempDir(), nil)
	service.setHTTPTransport(httpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}))

	started := time.Now()
	_, err := service.SendRequest(
		HTTPRequest{Method: "GET", URL: "https://203.0.113.15/slow"},
		HTTPRequestOptions{TimeoutMs: 15},
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "deadline") {
		t.Fatalf("expected deadline error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestHTTPClientServiceStripsSensitiveHeadersOnCrossOriginRedirect(t *testing.T) {
	service := NewHTTPClientService(t.TempDir(), nil)
	var calls atomic.Int32
	service.setHTTPTransport(httpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			resp := httpTestResponse(req, http.StatusFound, "")
			resp.Header.Set("Location", "https://203.0.113.17/next")
			return resp, nil
		}
		if got := req.Header.Get("Authorization"); got != "" {
			t.Fatalf("cross-origin redirect retained Authorization: %q", got)
		}
		return httpTestResponse(req, http.StatusOK, "done"), nil
	}))

	response, err := service.SendRequest(HTTPRequest{
		Method:  "GET",
		URL:     "https://203.0.113.16/start",
		Headers: map[string]string{"Authorization": "Bearer secret"},
	}, HTTPRequestOptions{})
	if err != nil {
		t.Fatalf("SendRequest() error = %v", err)
	}
	if response.Status != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("response = %#v, transport calls = %d", response, calls.Load())
	}
}

func TestHTTPClientServiceRejectsLargeResponseBody(t *testing.T) {
	service := NewHTTPClientService(t.TempDir(), nil)
	service.setHTTPTransport(httpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpTestResponse(req, http.StatusOK, "123456789"), nil
	}))

	_, err := service.SendRequest(
		HTTPRequest{Method: "GET", URL: "https://203.0.113.12/data"},
		HTTPRequestOptions{MaxResponseBytes: 8},
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "exceeds") {
		t.Fatalf("expected response-size error, got %v", err)
	}
}

func TestHTTPClientServiceResolvesSecretsOnlyAtSendAndSanitizesHistory(t *testing.T) {
	dir := t.TempDir()
	resolver := &fakeHTTPSecretResolver{values: map[string]string{"http-client/token": "top-secret"}}
	service := NewHTTPClientService(dir, resolver)
	service.setHTTPTransport(httpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer top-secret" {
			t.Fatalf("resolved Authorization = %q", got)
		}
		resp := httpTestResponse(req, http.StatusOK, `{"echo":"top-secret"}`)
		resp.Header.Set("Set-Cookie", "session=private")
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	}))

	response, err := service.SendRequest(HTTPRequest{
		Name:       "secure",
		Method:     "GET",
		URL:        "https://203.0.113.13/data",
		Headers:    map[string]string{"Authorization": "Bearer {{token}}", "Accept": "application/json"},
		SecretRefs: map[string]string{"token": "http-client/token"},
	}, HTTPRequestOptions{})
	if err != nil {
		t.Fatalf("SendRequest() error = %v", err)
	}
	if response.Headers["Set-Cookie"] != "[REDACTED]" {
		t.Fatalf("response Set-Cookie was not sanitized: %#v", response.Headers)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls.Load())
	}

	history, err := service.GetHistory()
	if err != nil || len(history) != 1 {
		t.Fatalf("GetHistory() = %#v, %v", history, err)
	}
	if history[0].RequestHeaders["Authorization"] != "[REDACTED]" {
		t.Fatalf("history leaked Authorization: %#v", history[0].RequestHeaders)
	}
	if strings.Contains(history[0].ResponseBody, "top-secret") {
		t.Fatalf("history response preview leaked a resolved secret: %q", history[0].ResponseBody)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "http-client", "history.json"))
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if strings.Contains(string(raw), "top-secret") || strings.Contains(string(raw), "session=private") {
		t.Fatalf("history leaked sensitive values: %s", raw)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(filepath.Join(dir, "http-client", "history.json"))
		if statErr != nil {
			t.Fatal(statErr)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("history permission = %o, want 600", got)
		}
	}
}

func TestHTTPClientServiceSanitizesResolvedSecretsFromTransportErrors(t *testing.T) {
	dir := t.TempDir()
	resolver := &fakeHTTPSecretResolver{values: map[string]string{"http-client/token": "top-secret"}}
	service := NewHTTPClientService(dir, resolver)
	service.setHTTPTransport(httpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial %s failed", req.URL.String())
	}))

	_, err := service.SendRequest(HTTPRequest{
		Method:     "GET",
		URL:        "https://203.0.113.14/private/{{token}}",
		SecretRefs: map[string]string{"token": "http-client/token"},
	}, HTTPRequestOptions{})
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("returned error leaked resolved secret: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED") {
		t.Fatalf("returned error did not mark redaction: %v", err)
	}

	history, historyErr := service.GetHistory()
	if historyErr != nil || len(history) != 1 {
		t.Fatalf("GetHistory() = %#v, %v", history, historyErr)
	}
	if strings.Contains(history[0].Error, "top-secret") {
		t.Fatalf("history error leaked resolved secret: %q", history[0].Error)
	}
	raw, readErr := os.ReadFile(filepath.Join(dir, "http-client", "history.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(raw), "top-secret") {
		t.Fatalf("persisted history leaked resolved secret: %s", raw)
	}
}

func TestSanitizeHTTPErrorHandlesEncodedAndOverlappingSecrets(t *testing.T) {
	message := "Get https://example.com/top%20secret/part/abcdef failed"
	got := sanitizeHTTPError(message, []string{"abc", "abcdef", "top secret/part"})
	if strings.Contains(got, "top%20secret") || strings.Contains(got, "abcdef") {
		t.Fatalf("sanitizeHTTPError() leaked encoded/overlapping secrets: %q", got)
	}
}

func TestHTTPClientServicePrivateNetworkRequiresExplicitPermission(t *testing.T) {
	service := NewHTTPClientService(t.TempDir(), nil)
	service.approvePrivateNetwork = func(string) bool { return true }
	service.setHTTPTransport(httpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpTestResponse(req, http.StatusNoContent, ""), nil
	}))
	request := HTTPRequest{Method: "GET", URL: "http://127.0.0.1:8080/health"}

	if _, err := service.SendRequest(request, HTTPRequestOptions{}); err == nil {
		t.Fatal("private network request succeeded without explicit permission")
	}
	token, err := service.RequestPrivateNetworkAccess("http://127.0.0.1:8080", "private-1")
	if err != nil {
		t.Fatalf("RequestPrivateNetworkAccess() error = %v", err)
	}
	if _, err := service.SendRequest(request, HTTPRequestOptions{
		RequestID: "private-1", PrivateNetworkToken: token,
	}); err != nil {
		t.Fatalf("explicitly allowed private request failed: %v", err)
	}
}

func TestSanitizeHTTPHeaders(t *testing.T) {
	got := SanitizeHTTPHeaders(map[string]string{
		"Authorization":       "Bearer secret",
		"Cookie":              "a=b",
		"Proxy-Authorization": "Basic secret",
		"X-API-Key":           "secret",
		"Accept":              "application/json",
	})
	for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization", "X-API-Key"} {
		if got[name] != "[REDACTED]" {
			t.Errorf("%s = %q", name, got[name])
		}
	}
	if got["Accept"] != "application/json" {
		t.Errorf("Accept = %q", got["Accept"])
	}
}
