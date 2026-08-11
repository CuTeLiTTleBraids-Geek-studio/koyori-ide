package services

import (
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newApprovedPrivateHTTPService(t *testing.T) *HTTPClientService {
	t.Helper()
	service := NewHTTPClientService(t.TempDir(), nil)
	service.approvePrivateNetwork = func(string) bool { return true }
	service.setHTTPTransport(httpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpTestResponse(req, http.StatusNoContent, ""), nil
	}))
	return service
}

func requestPrivateToken(t *testing.T, service *HTTPClientService, origin, requestID string) string {
	t.Helper()
	token, err := service.RequestPrivateNetworkAccess(origin, requestID)
	if err != nil {
		t.Fatalf("RequestPrivateNetworkAccess(%q, %q): %v", origin, requestID, err)
	}
	return token
}

func TestHTTPClientOptionsCannotCarryRendererPrivateNetworkBoolean(t *testing.T) {
	if _, exists := reflect.TypeOf(HTTPRequestOptions{}).FieldByName("AllowPrivateNetwork"); exists {
		t.Fatal("HTTPRequestOptions still trusts renderer AllowPrivateNetwork")
	}
}

func TestHTTPClientPrivateNetworkRequiresBackendToken(t *testing.T) {
	request := HTTPRequest{Method: http.MethodGet, URL: "http://127.0.0.1:8080/health"}

	t.Run("missing and forged token", func(t *testing.T) {
		service := newApprovedPrivateHTTPService(t)
		if _, err := service.SendRequest(request, HTTPRequestOptions{RequestID: "req-1"}); err == nil {
			t.Fatal("private request succeeded without a backend token")
		}
		if _, err := service.SendRequest(request, HTTPRequestOptions{
			RequestID: "req-1", PrivateNetworkToken: "forged",
		}); err == nil {
			t.Fatal("private request succeeded with a forged token")
		}
	})

	t.Run("approved token is single use", func(t *testing.T) {
		service := newApprovedPrivateHTTPService(t)
		token := requestPrivateToken(t, service, "http://127.0.0.1:8080", "req-1")
		options := HTTPRequestOptions{RequestID: "req-1", PrivateNetworkToken: token}
		if _, err := service.SendRequest(request, options); err != nil {
			t.Fatalf("approved private request failed: %v", err)
		}
		if _, err := service.SendRequest(request, options); err == nil {
			t.Fatal("private-network token replay succeeded")
		}
	})

	t.Run("token is bound to request id", func(t *testing.T) {
		service := newApprovedPrivateHTTPService(t)
		token := requestPrivateToken(t, service, "http://127.0.0.1:8080", "req-1")
		_, err := service.SendRequest(request, HTTPRequestOptions{
			RequestID: "req-2", PrivateNetworkToken: token,
		})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "request") {
			t.Fatalf("cross-request token error = %v", err)
		}
	})

	t.Run("token is bound to origin", func(t *testing.T) {
		service := newApprovedPrivateHTTPService(t)
		token := requestPrivateToken(t, service, "http://127.0.0.1:8080", "req-1")
		_, err := service.SendRequest(
			HTTPRequest{Method: http.MethodGet, URL: "http://127.0.0.1:9090/health"},
			HTTPRequestOptions{RequestID: "req-1", PrivateNetworkToken: token},
		)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "origin") {
			t.Fatalf("cross-origin token error = %v", err)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		service := newApprovedPrivateHTTPService(t)
		now := time.Unix(100, 0)
		service.privateNetworkNow = func() time.Time { return now }
		token := requestPrivateToken(t, service, "http://127.0.0.1:8080", "req-1")
		now = now.Add(privateNetworkApprovalTTL + time.Second)
		_, err := service.SendRequest(request, HTTPRequestOptions{
			RequestID: "req-1", PrivateNetworkToken: token,
		})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "expired") {
			t.Fatalf("expired token error = %v", err)
		}
	})
}

func TestHTTPClientPrivateNetworkApprovalCanBeDenied(t *testing.T) {
	service := NewHTTPClientService(t.TempDir(), nil)
	service.approvePrivateNetwork = func(string) bool { return false }
	if _, err := service.RequestPrivateNetworkAccess("http://127.0.0.1:8080", "req-1"); err == nil {
		t.Fatal("denied private-network approval minted a token")
	}
}

func TestHTTPClientPrivateRedirectsStayBoundToApprovedOrigin(t *testing.T) {
	t.Run("same origin redirect", func(t *testing.T) {
		service := newApprovedPrivateHTTPService(t)
		var calls atomic.Int32
		service.setHTTPTransport(httpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				response := httpTestResponse(req, http.StatusFound, "")
				response.Header.Set("Location", "http://127.0.0.1:8080/next")
				return response, nil
			}
			return httpTestResponse(req, http.StatusNoContent, ""), nil
		}))
		token := requestPrivateToken(t, service, "http://127.0.0.1:8080", "req-1")
		_, err := service.SendRequest(
			HTTPRequest{Method: http.MethodGet, URL: "http://127.0.0.1:8080/start"},
			HTTPRequestOptions{RequestID: "req-1", PrivateNetworkToken: token},
		)
		if err != nil || calls.Load() != 2 {
			t.Fatalf("same-origin redirect error = %v, calls = %d", err, calls.Load())
		}
	})

	t.Run("cross origin redirect", func(t *testing.T) {
		service := newApprovedPrivateHTTPService(t)
		var calls atomic.Int32
		service.setHTTPTransport(httpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			response := httpTestResponse(req, http.StatusFound, "")
			response.Header.Set("Location", "http://127.0.0.1:9090/admin")
			return response, nil
		}))
		token := requestPrivateToken(t, service, "http://127.0.0.1:8080", "req-1")
		_, err := service.SendRequest(
			HTTPRequest{Method: http.MethodGet, URL: "http://127.0.0.1:8080/start"},
			HTTPRequestOptions{RequestID: "req-1", PrivateNetworkToken: token},
		)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "redirect") {
			t.Fatalf("cross-origin private redirect error = %v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("cross-origin private redirect reached transport; calls = %d", calls.Load())
		}
	})
}
