package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef"
const testOrigin = "http://127.0.0.1"

func TestLoadTokenFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		filename string
	}{
		{name: "missing"},
		{name: "short", value: "not-long-enough"},
		{name: "ambiguous", value: testToken, filename: "token.txt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := loadToken(test.value, test.filename); err == nil {
				t.Fatal("loadToken() succeeded; want fail-closed error")
			}
		})
	}
}

func TestLoadTokenFromFile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(filename, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadToken("", filename)
	if err != nil {
		t.Fatalf("loadToken() error = %v", err)
	}
	if string(got) != testToken {
		t.Fatalf("loadToken() = %q, want configured token", got)
	}
}

func TestGatewayRequiresAuthenticationAndStripsCredentials(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("upstream Authorization = %q, want stripped", got)
		}
		if _, err := r.Cookie(sessionCookieName); err == nil {
			t.Error("upstream received gateway session cookie")
		}
		if got := r.Header.Get("Origin"); got != "" {
			t.Errorf("upstream Origin = %q, want stripped after gateway validation", got)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "upstream:"+r.URL.Path)
	}))
	defer upstream.Close()

	handler := newTestGateway(t, upstream.URL, 1024)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, testOrigin+"/wails/runtime", strings.NewReader("{}")))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	authorized := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, testOrigin+"/wails/runtime", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Origin", testOrigin)
	handler.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusOK || authorized.Body.String() != "upstream:/wails/runtime" {
		t.Fatalf("authenticated response = (%d, %q), want upstream success", authorized.Code, authorized.Body.String())
	}
}

func TestGatewayLoginCookieCoversRPCAndWebSocketPaths(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	handler := newTestGateway(t, upstream.URL, 1024)

	login := httptest.NewRecorder()
	form := url.Values{"token": {testToken}}.Encode()
	loginRequest := httptest.NewRequest(http.MethodPost, testOrigin+loginPath, strings.NewReader(form))
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRequest.Header.Set("Origin", testOrigin)
	handler.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want %d", login.Code, http.StatusSeeOther)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("login cookie = %#v, want HttpOnly SameSite=Strict cookie", cookies)
	}

	for _, path := range []string{"/wails/runtime", "/wails/events"} {
		response := httptest.NewRecorder()
		method := http.MethodGet
		if path == "/wails/runtime" {
			method = http.MethodPost
		}
		request := httptest.NewRequest(method, testOrigin+path, nil)
		request.Header.Set("Origin", testOrigin)
		request.AddCookie(cookies[0])
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Errorf("authenticated %s status = %d, want %d", path, response.Code, http.StatusNoContent)
		}
	}
}

func TestGatewayBlocksOriginlessCookieRPCAndGETRPC(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("rejected RPC request reached upstream")
	}))
	defer upstream.Close()
	handler := newTestGateway(t, upstream.URL, 1024)
	cookie := loginCookie(t, handler)

	originless := httptest.NewRecorder()
	originlessRequest := httptest.NewRequest(http.MethodPost, testOrigin+"/wails/runtime", strings.NewReader("{}"))
	originlessRequest.AddCookie(cookie)
	handler.ServeHTTP(originless, originlessRequest)
	if originless.Code != http.StatusForbidden {
		t.Fatalf("originless cookie RPC status = %d, want %d", originless.Code, http.StatusForbidden)
	}

	getRPC := httptest.NewRecorder()
	getRequest := httptest.NewRequest(http.MethodGet, testOrigin+"/wails/runtime?object=0&method=0", nil)
	getRequest.Header.Set("Origin", testOrigin)
	getRequest.AddCookie(cookie)
	handler.ServeHTTP(getRPC, getRequest)
	if getRPC.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET RPC status = %d, want %d", getRPC.Code, http.StatusMethodNotAllowed)
	}

	emptyOrigin := httptest.NewRecorder()
	emptyOriginRequest := httptest.NewRequest(http.MethodPost, testOrigin+"/wails/runtime", strings.NewReader("{}"))
	emptyOriginRequest.Header["Origin"] = []string{" "}
	emptyOriginRequest.AddCookie(cookie)
	handler.ServeHTTP(emptyOrigin, emptyOriginRequest)
	if emptyOrigin.Code != http.StatusForbidden {
		t.Fatalf("empty-Origin RPC status = %d, want %d", emptyOrigin.Code, http.StatusForbidden)
	}
}

func TestGatewayRejectsCrossOriginAndOversizedRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("rejected request reached upstream")
	}))
	defer upstream.Close()
	handler := newTestGateway(t, upstream.URL, 4)

	crossOrigin := httptest.NewRecorder()
	crossOriginRequest := httptest.NewRequest(http.MethodPost, testOrigin+"/wails/runtime", strings.NewReader("{}"))
	crossOriginRequest.Header.Set("Authorization", "Bearer "+testToken)
	crossOriginRequest.Header.Set("Origin", "https://attacker.example")
	handler.ServeHTTP(crossOrigin, crossOriginRequest)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want %d", crossOrigin.Code, http.StatusForbidden)
	}

	oversized := httptest.NewRecorder()
	oversizedRequest := httptest.NewRequest(http.MethodPost, testOrigin+"/wails/runtime", strings.NewReader("12345"))
	oversizedRequest.Header.Set("Authorization", "Bearer "+testToken)
	handler.ServeHTTP(oversized, oversizedRequest)
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want %d", oversized.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestGatewayLeavesHealthCheckUnauthenticated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer upstream.Close()
	handler := newTestGateway(t, upstream.URL, 1024)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, testOrigin+"/health", nil))
	if response.Code != http.StatusOK || response.Body.String() != `{"status":"ok"}` {
		t.Fatalf("health response = (%d, %q)", response.Code, response.Body.String())
	}

	methodNotAllowed := httptest.NewRecorder()
	handler.ServeHTTP(methodNotAllowed, httptest.NewRequest(http.MethodPost, testOrigin+"/health", strings.NewReader("ignored")))
	if methodNotAllowed.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST health status = %d, want %d", methodNotAllowed.Code, http.StatusMethodNotAllowed)
	}
}

func TestBackendEnvironmentForcesLoopbackAndOmitsToken(t *testing.T) {
	environ := []string{
		"PATH=/bin",
		"KOYORI_SERVER_TOKEN=secret",
		"KOYORI_SERVER_TOKEN_FILE=/run/secrets/token",
		"KOYORI_SERVER_GATEWAY_MODE=0",
		"WAILS_SERVER_HOST=0.0.0.0",
		"WAILS_SERVER_PORT=8080",
	}
	got := strings.Join(backendEnvironment(environ, "8081"), "\n")
	for _, forbidden := range []string{"KOYORI_SERVER_TOKEN=", "KOYORI_SERVER_TOKEN_FILE=", "KOYORI_SERVER_GATEWAY_MODE=0", "WAILS_SERVER_HOST=0.0.0.0", "WAILS_SERVER_PORT=8080"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("backend environment contains %q", forbidden)
		}
	}
	for _, required := range []string{"WAILS_SERVER_HOST=127.0.0.1", "WAILS_SERVER_PORT=8081", "KOYORI_SERVER_GATEWAY_MODE=1"} {
		if !strings.Contains(got, required) {
			t.Errorf("backend environment missing %q", required)
		}
	}
}

func newTestGateway(t *testing.T, upstreamURL string, maxBodyBytes int64) http.Handler {
	t.Helper()
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newGateway(gatewayConfig{
		token:        []byte(testToken),
		backendURL:   parsed,
		maxBodyBytes: maxBodyBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func loginCookie(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	response := httptest.NewRecorder()
	form := url.Values{"token": {testToken}}.Encode()
	request := httptest.NewRequest(http.MethodPost, testOrigin+loginPath, strings.NewReader(form))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", testOrigin)
	handler.ServeHTTP(response, request)
	cookies := response.Result().Cookies()
	if response.Code != http.StatusSeeOther || len(cookies) != 1 {
		t.Fatalf("login response = (%d, %#v), want redirect with cookie", response.Code, cookies)
	}
	return cookies[0]
}
