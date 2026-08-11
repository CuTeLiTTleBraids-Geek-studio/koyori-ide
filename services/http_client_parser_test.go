package services

import (
	"strings"
	"testing"
)

func TestParseHTTPFileRequestsVariablesAndNames(t *testing.T) {
	env := HTTPEnvironment{
		Values: map[string]string{"baseUrl": "https://api.example.com", "version": "v1"},
	}
	content := strings.Join([]string{
		"@userId = 42",
		"### Fetch user",
		"# @name fetchUser",
		"GET {{baseUrl}}/{{version}}/users/{{userId}} HTTP/1.1",
		"Accept: application/json",
		"X-Trace: request-{{userId}}",
		"",
		"###",
		"// @name createUser",
		"POST {{baseUrl}}/{{version}}/users",
		"Content-Type: application/json",
		"",
		`{"name":"Ada","managerId":{{userId}}}`,
	}, "\n")

	requests, err := ParseHTTPFile(content, env)
	if err != nil {
		t.Fatalf("ParseHTTPFile() error = %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("len(requests) = %d, want 2", len(requests))
	}
	first := requests[0]
	if first.Name != "fetchUser" || first.Method != "GET" {
		t.Fatalf("first request = %#v", first)
	}
	if first.URL != "https://api.example.com/v1/users/42" {
		t.Errorf("first URL = %q", first.URL)
	}
	if first.Headers["X-Trace"] != "request-42" {
		t.Errorf("X-Trace = %q", first.Headers["X-Trace"])
	}
	if first.StartLine != 4 || first.EndLine != 7 {
		t.Errorf("first line range = %d..%d, want 4..7", first.StartLine, first.EndLine)
	}
	second := requests[1]
	if second.Name != "createUser" || second.Method != "POST" {
		t.Fatalf("second request = %#v", second)
	}
	if second.Body != `{"name":"Ada","managerId":42}` {
		t.Errorf("second body = %q", second.Body)
	}
}

func TestParseHTTPEnvironmentKeepsSecretReferencesOpaque(t *testing.T) {
	content := `{
		"dev": {
			"baseUrl": "https://dev.example.com",
			"token": {"$secret": "http-client/dev-token"},
			"unused": {"$secret": "http-client/unused"}
		}
	}`

	env, err := ParseHTTPEnvironment(content, "dev")
	if err != nil {
		t.Fatalf("ParseHTTPEnvironment() error = %v", err)
	}
	if env.Values["baseUrl"] != "https://dev.example.com" {
		t.Errorf("baseUrl = %q", env.Values["baseUrl"])
	}
	if env.SecretRefs["token"] != "http-client/dev-token" {
		t.Errorf("secret ref = %q", env.SecretRefs["token"])
	}
	if _, leaked := env.Values["token"]; leaked {
		t.Fatal("secret reference must not be copied into plaintext environment values")
	}

	requests, err := ParseHTTPFile("GET {{baseUrl}}/me\nAuthorization: Bearer {{token}}", env)
	if err != nil {
		t.Fatalf("ParseHTTPFile() with secret ref error = %v", err)
	}
	if requests[0].Headers["Authorization"] != "Bearer {{token}}" {
		t.Fatalf("secret placeholder was resolved before send: %q", requests[0].Headers["Authorization"])
	}
	if requests[0].SecretRefs["token"] != "http-client/dev-token" {
		t.Fatalf("request secret refs = %#v", requests[0].SecretRefs)
	}
	if _, ok := requests[0].SecretRefs["unused"]; ok {
		t.Fatalf("unused secret was attached to request: %#v", requests[0].SecretRefs)
	}
}

func TestParseHTTPEnvironmentRejectsInlineSecretObjects(t *testing.T) {
	_, err := ParseHTTPEnvironment(`{"dev":{"token":{"value":"plaintext"}}}`, "dev")
	if err == nil || !strings.Contains(err.Error(), "$secret") {
		t.Fatalf("expected explicit secret-reference error, got %v", err)
	}
}

func TestParseHTTPFileRejectsUnresolvedVariables(t *testing.T) {
	_, err := ParseHTTPFile("GET https://example.com/{{missing}}", HTTPEnvironment{})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing-variable error, got %v", err)
	}
}

func TestParseHTTPFileRejectsMalformedHeaders(t *testing.T) {
	_, err := ParseHTTPFile("GET https://example.com\nNot-A-Header", HTTPEnvironment{})
	if err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("expected header error, got %v", err)
	}
}
