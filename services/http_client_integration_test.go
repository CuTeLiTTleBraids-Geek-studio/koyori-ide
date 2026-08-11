package services

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func privateOriginForTest(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	return parsed.Scheme + "://" + parsed.Host
}

func approvePrivateRequestForTest(
	t *testing.T,
	service *HTTPClientService,
	rawURL string,
	requestID string,
) string {
	t.Helper()
	token, err := service.RequestPrivateNetworkAccess(
		privateOriginForTest(t, rawURL),
		requestID,
	)
	if err != nil {
		t.Fatalf("approve private request: %v", err)
	}
	return token
}

func TestHTTPClientServiceRealLoopbackIntegration(t *testing.T) {
	var primaryCalls atomic.Int32
	var redirectedCalls atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer redirectTarget.Close()

	startedSlow := make(chan struct{}, 2)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		primaryCalls.Add(1)
		switch request.URL.Path {
		case "/response":
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
			}
			if request.Method != http.MethodPost || string(body) != "payload" {
				t.Errorf("request = %s %q", request.Method, body)
			}
			if request.Header.Get("X-Probe") != "real-network" {
				t.Errorf("X-Probe = %q", request.Header.Get("X-Probe"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Set-Cookie", "session=must-not-cross-binding")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"ok":true}`)
		case "/redirect-same":
			http.Redirect(w, request, "/redirect-ok", http.StatusFound)
		case "/redirect-ok":
			w.WriteHeader(http.StatusAccepted)
		case "/redirect-cross":
			http.Redirect(w, request, redirectTarget.URL+"/target", http.StatusFound)
		case "/slow":
			startedSlow <- struct{}{}
			<-request.Context().Done()
		default:
			http.NotFound(w, request)
		}
	}))
	defer primary.Close()

	service := NewHTTPClientService(t.TempDir(), nil)
	service.approvePrivateNetwork = func(string) bool { return true }

	t.Run("missing token is rejected before network", func(t *testing.T) {
		before := primaryCalls.Load()
		_, err := service.SendRequest(
			HTTPRequest{Method: http.MethodGet, URL: primary.URL + "/response"},
			HTTPRequestOptions{RequestID: "missing-token"},
		)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "approval") {
			t.Fatalf("missing-token error = %v", err)
		}
		if primaryCalls.Load() != before {
			t.Fatal("missing-token request reached the loopback server")
		}
	})

	t.Run("approved response and token replay", func(t *testing.T) {
		const requestID = "real-response"
		token := approvePrivateRequestForTest(t, service, primary.URL, requestID)
		request := HTTPRequest{
			Method:  http.MethodPost,
			URL:     primary.URL + "/response",
			Headers: map[string]string{"X-Probe": "real-network"},
			Body:    "payload",
		}
		options := HTTPRequestOptions{RequestID: requestID, PrivateNetworkToken: token}
		response, err := service.SendRequest(request, options)
		if err != nil {
			t.Fatalf("approved loopback request: %v", err)
		}
		if response.Status != http.StatusCreated || response.StatusText != "Created" {
			t.Fatalf("response status = %d %q", response.Status, response.StatusText)
		}
		if response.Headers["Content-Type"] != "application/json" {
			t.Fatalf("response headers = %#v", response.Headers)
		}
		if response.Headers["Set-Cookie"] != "[REDACTED]" {
			t.Fatalf("Set-Cookie crossed binding unsanitized: %#v", response.Headers)
		}
		if response.Body != `{"ok":true}` || response.DurationMs < 0 {
			t.Fatalf("response body/duration = %q/%d", response.Body, response.DurationMs)
		}
		beforeReplay := primaryCalls.Load()
		if _, err := service.SendRequest(request, options); err == nil || !strings.Contains(strings.ToLower(err.Error()), "already used") {
			t.Fatalf("token replay error = %v", err)
		}
		if primaryCalls.Load() != beforeReplay {
			t.Fatal("replayed token reached the loopback server")
		}
	})

	t.Run("same-origin redirect is allowed", func(t *testing.T) {
		const requestID = "same-redirect"
		token := approvePrivateRequestForTest(t, service, primary.URL, requestID)
		response, err := service.SendRequest(
			HTTPRequest{Method: http.MethodGet, URL: primary.URL + "/redirect-same"},
			HTTPRequestOptions{RequestID: requestID, PrivateNetworkToken: token},
		)
		if err != nil || response.Status != http.StatusAccepted {
			t.Fatalf("same-origin redirect response = %#v, %v", response, err)
		}
	})

	t.Run("cross-origin redirect is rejected before target", func(t *testing.T) {
		const requestID = "cross-redirect"
		token := approvePrivateRequestForTest(t, service, primary.URL, requestID)
		_, err := service.SendRequest(
			HTTPRequest{Method: http.MethodGet, URL: primary.URL + "/redirect-cross"},
			HTTPRequestOptions{RequestID: requestID, PrivateNetworkToken: token},
		)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "redirect") {
			t.Fatalf("cross-origin redirect error = %v", err)
		}
		if redirectedCalls.Load() != 0 {
			t.Fatalf("cross-origin redirect reached target %d times", redirectedCalls.Load())
		}
	})

	t.Run("cancellation stops the real request", func(t *testing.T) {
		const requestID = "cancel-real"
		token := approvePrivateRequestForTest(t, service, primary.URL, requestID)
		done := make(chan error, 1)
		go func() {
			_, err := service.SendRequest(
				HTTPRequest{Method: http.MethodGet, URL: primary.URL + "/slow"},
				HTTPRequestOptions{
					RequestID: requestID, PrivateNetworkToken: token, TimeoutMs: 30_000,
				},
			)
			done <- err
		}()
		select {
		case <-startedSlow:
		case <-time.After(time.Second):
			t.Fatal("real cancellation request did not reach server")
		}
		if !service.CancelRequest(requestID) {
			t.Fatal("CancelRequest returned false for real in-flight request")
		}
		select {
		case err := <-done:
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cancel") {
				t.Fatalf("real cancellation error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("real cancellation did not stop SendRequest")
		}
	})

	t.Run("timeout stops the real request", func(t *testing.T) {
		const requestID = "timeout-real"
		token := approvePrivateRequestForTest(t, service, primary.URL, requestID)
		_, err := service.SendRequest(
			HTTPRequest{Method: http.MethodGet, URL: primary.URL + "/slow"},
			HTTPRequestOptions{
				RequestID: requestID, PrivateNetworkToken: token, TimeoutMs: 20,
			},
		)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "deadline") {
			t.Fatalf("real timeout error = %v", err)
		}
	})
}
