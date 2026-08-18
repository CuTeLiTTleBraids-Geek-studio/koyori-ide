//go:build server

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerTransportMiddlewareRejectsUnsafeRuntimeRequests(t *testing.T) {
	called := false
	handler := serverTransportMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		name   string
		method string
		url    string
		origin string
	}{
		{name: "query GET", method: http.MethodGet, url: "http://127.0.0.1:8080/wails/runtime?object=1&method=1"},
		{name: "cross origin POST", method: http.MethodPost, url: "http://127.0.0.1:8080/wails/runtime", origin: "https://attacker.example"},
		{name: "missing origin POST", method: http.MethodPost, url: "http://127.0.0.1:8080/wails/runtime"},
	} {
		t.Run(test.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(test.method, test.url, strings.NewReader(`{"object":1}`))
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusForbidden || called {
				t.Fatalf("response = %d, called = %v; want forbidden without upstream call", response.Code, called)
			}
		})
	}
}

func TestServerTransportMiddlewareAllowsSameOriginRuntimeAndEvents(t *testing.T) {
	handler := serverTransportMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wails/runtime" && r.Body != nil {
			if _, err := io.ReadAll(r.Body); err != nil {
				t.Errorf("read bounded runtime body: %v", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "runtime", method: http.MethodPost, path: "/wails/runtime"},
		{name: "events", method: http.MethodGet, path: "/wails/events"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, "http://127.0.0.1:8080"+test.path, strings.NewReader(`{"ok":true}`))
			req.Header.Set("Origin", "http://127.0.0.1:8080")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusNoContent {
				t.Fatalf("same-origin response = %d, want %d", response.Code, http.StatusNoContent)
			}
		})
	}
}
