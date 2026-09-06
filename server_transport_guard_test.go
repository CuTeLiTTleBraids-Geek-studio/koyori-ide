//go:build server

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestServerTransportRejectsDirectRPCWithoutOriginOrNonce(t *testing.T) {
	t.Setenv(serverGatewayNonceEnv, "private-nonce")
	handler := serverTransportMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/wails/runtime", strings.NewReader("{}"))
	request.Header.Set("Origin", "http://127.0.0.1:8081")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("direct RPC status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestServerTransportAcceptsOnlyMatchingGatewayNonce(t *testing.T) {
	t.Setenv(serverGatewayNonceEnv, "private-nonce")
	handler := serverTransportMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(serverGatewayNonceHeader) != "" {
			t.Error("gateway nonce reached application handler")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, nonce := range []string{"wrong", "private-nonce"} {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/wails/runtime", strings.NewReader("{}"))
		request.Header.Set(serverGatewayNonceHeader, nonce)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusForbidden
		if nonce == "private-nonce" {
			want = http.StatusNoContent
		}
		if response.Code != want {
			t.Errorf("nonce %q status = %d, want %d", nonce, response.Code, want)
		}
	}
}

func TestServerTransportAcceptsSameOriginStandaloneRPC(t *testing.T) {
	previous, had := os.LookupEnv(serverGatewayNonceEnv)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(serverGatewayNonceEnv, previous)
		} else {
			_ = os.Unsetenv(serverGatewayNonceEnv)
		}
	})
	_ = os.Unsetenv(serverGatewayNonceEnv)
	handler := serverTransportMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/wails/runtime", strings.NewReader("{}"))
	request.Header.Set("Origin", "http://127.0.0.1:8081")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("same-origin RPC status = %d, want %d", response.Code, http.StatusNoContent)
	}
}
