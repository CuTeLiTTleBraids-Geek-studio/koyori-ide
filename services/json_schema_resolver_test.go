package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type jsonSchemaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f jsonSchemaRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestJSONSchemaKindForPath(t *testing.T) {
	tests := []struct {
		path string
		want JSONSchemaKind
	}{
		{path: "/workspace/tsconfig.json", want: JSONSchemaTSConfig},
		{path: `C:\workspace\tsconfig.build.json`, want: JSONSchemaTSConfig},
		{path: "/workspace/jsconfig.json", want: JSONSchemaJSConfig},
		{path: "/workspace/jsconfig.web.json", want: JSONSchemaJSConfig},
		{path: "/workspace/package.json", want: JSONSchemaPackage},
		{path: "/workspace/package-lock.json", want: JSONSchemaNone},
		{path: "/workspace/settings.json", want: JSONSchemaNone},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := JSONSchemaKindForPath(tt.path); got != tt.want {
				t.Fatalf("JSONSchemaKindForPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestBuiltinJSONSchemasExposeExpectedProperties(t *testing.T) {
	tests := []struct {
		kind       JSONSchemaKind
		properties []string
	}{
		{kind: JSONSchemaTSConfig, properties: []string{"compilerOptions", "include", "exclude", "extends"}},
		{kind: JSONSchemaJSConfig, properties: []string{"compilerOptions", "include", "exclude", "extends"}},
		{kind: JSONSchemaPackage, properties: []string{"name", "scripts", "dependencies", "devDependencies", "engines", "workspaces"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			schema, ok := builtinJSONSchema(tt.kind)
			if !ok {
				t.Fatalf("builtinJSONSchema(%q) was not found", tt.kind)
			}
			var document struct {
				Schema     string                     `json:"$schema"`
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(schema, &document); err != nil {
				t.Fatalf("builtin schema is invalid JSON: %v", err)
			}
			if document.Schema == "" {
				t.Error("builtin schema does not identify its JSON Schema dialect")
			}
			for _, property := range tt.properties {
				if _, ok := document.Properties[property]; !ok {
					t.Errorf("builtin schema missing property %q", property)
				}
			}
		})
	}
}

func TestJSONSchemaDefinitionsUseFixedOfficialHTTPSURLs(t *testing.T) {
	wantPaths := map[JSONSchemaKind]string{
		JSONSchemaTSConfig: "/tsconfig.json",
		JSONSchemaJSConfig: "/jsconfig.json",
		JSONSchemaPackage:  "/package.json",
	}
	for kind, wantPath := range wantPaths {
		definition, ok := jsonSchemaDefinitionForKind(kind)
		if !ok {
			t.Fatalf("jsonSchemaDefinitionForKind(%q) was not found", kind)
		}
		parsed, err := url.Parse(definition.URL)
		if err != nil {
			t.Fatalf("parse schema URL: %v", err)
		}
		if parsed.Scheme != "https" || parsed.Hostname() != "json.schemastore.org" || parsed.Path != wantPath {
			t.Errorf("schema URL = %q, want https://json.schemastore.org%s", definition.URL, wantPath)
		}
		if len(definition.FileMatch) == 0 || definition.CacheName == "" {
			t.Errorf("schema definition is incomplete: %+v", definition)
		}
	}
}

func TestJSONSchemaResolverCachesHTTPSResponseWithPrivatePermissions(t *testing.T) {
	remoteSchema := []byte(`{"type":"object","properties":{"remote":{"type":"boolean"}}}`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/schema+json")
		_, _ = w.Write(remoteSchema)
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	resolver := newJSONSchemaResolver(jsonSchemaResolverOptions{
		CacheDir:     cacheDir,
		Client:       server.Client(),
		AllowedHosts: []string{serverURL.Hostname()},
		Timeout:      time.Second,
		MaxBodyBytes: 1024,
		Definitions: []jsonSchemaDefinition{{
			Kind: JSONSchemaTSConfig, URL: server.URL, CacheName: "tsconfig.schema.json", FileMatch: []string{"**/tsconfig.json"},
		}},
	})

	resolved, err := resolver.Resolve(context.Background(), JSONSchemaTSConfig)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Source != JSONSchemaSourceNetwork {
		t.Fatalf("source = %q, want %q", resolved.Source, JSONSchemaSourceNetwork)
	}
	if string(resolved.Schema) != string(remoteSchema) {
		t.Fatalf("resolved schema = %s, want %s", resolved.Schema, remoteSchema)
	}
	cachePath := filepath.Join(cacheDir, "tsconfig.schema.json")
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("stat cache: %v", err)
	}
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("cache permissions = %04o, want 0600", got)
		}
	}
}

func TestJSONSchemaResolverUsesCacheWhenOffline(t *testing.T) {
	cacheDir := t.TempDir()
	cachedSchema := []byte(`{"type":"object","properties":{"cached":{"type":"string"}}}`)
	if err := os.WriteFile(filepath.Join(cacheDir, "package.schema.json"), cachedSchema, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := newJSONSchemaResolver(jsonSchemaResolverOptions{
		CacheDir: cacheDir,
		Client: &http.Client{Transport: jsonSchemaRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})},
		AllowedHosts: []string{"json.schemastore.org"},
		Definitions: []jsonSchemaDefinition{{
			Kind: JSONSchemaPackage, URL: "https://json.schemastore.org/package.json", CacheName: "package.schema.json",
		}},
	})

	resolved, err := resolver.Resolve(context.Background(), JSONSchemaPackage)
	if err != nil {
		t.Fatalf("Resolve offline: %v", err)
	}
	if resolved.Source != JSONSchemaSourceCache || string(resolved.Schema) != string(cachedSchema) {
		t.Fatalf("offline resolution = source %q schema %s, want cache %s", resolved.Source, resolved.Schema, cachedSchema)
	}
}

func TestJSONSchemaResolverFallsBackToBuiltinWhenOfflineWithoutCache(t *testing.T) {
	resolver := newJSONSchemaResolver(jsonSchemaResolverOptions{
		CacheDir: t.TempDir(),
		Client: &http.Client{Transport: jsonSchemaRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})},
		AllowedHosts: []string{"json.schemastore.org"},
		Definitions: []jsonSchemaDefinition{{
			Kind: JSONSchemaJSConfig, URL: "https://json.schemastore.org/jsconfig.json", CacheName: "jsconfig.schema.json",
		}},
	})

	resolved, err := resolver.Resolve(context.Background(), JSONSchemaJSConfig)
	if err != nil {
		t.Fatalf("Resolve offline without cache: %v", err)
	}
	if resolved.Source != JSONSchemaSourceBuiltin || !json.Valid(resolved.Schema) {
		t.Fatalf("fallback = source %q schema %s, want valid builtin", resolved.Source, resolved.Schema)
	}
}

func TestJSONSchemaResolverRejectsMaliciousURLs(t *testing.T) {
	resolver := newJSONSchemaResolver(jsonSchemaResolverOptions{
		AllowedHosts: []string{"json.schemastore.org"},
	})
	for _, rawURL := range []string{
		"http://json.schemastore.org/package.json",
		"https://json.schemastore.org.evil.example/package.json",
		"https://json.schemastore.org@evil.example/package.json",
	} {
		if _, err := resolver.fetch(context.Background(), rawURL); err == nil {
			t.Errorf("fetch(%q) unexpectedly accepted a malicious URL", rawURL)
		}
	}
}

func TestJSONSchemaResolverEnforcesBodyLimitAndTimeout(t *testing.T) {
	t.Run("body limit", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"padding":"` + string(make([]byte, 256)) + `"}`))
		}))
		defer server.Close()
		parsed, _ := url.Parse(server.URL)
		resolver := newJSONSchemaResolver(jsonSchemaResolverOptions{
			Client: server.Client(), AllowedHosts: []string{parsed.Hostname()}, MaxBodyBytes: 64,
		})
		if _, err := resolver.fetch(context.Background(), server.URL); err == nil {
			t.Fatal("oversized schema unexpectedly succeeded")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte(`{"type":"object"}`))
		}))
		defer server.Close()
		parsed, _ := url.Parse(server.URL)
		resolver := newJSONSchemaResolver(jsonSchemaResolverOptions{
			Client: server.Client(), AllowedHosts: []string{parsed.Hostname()}, Timeout: 10 * time.Millisecond,
		})
		if _, err := resolver.fetch(context.Background(), server.URL); err == nil {
			t.Fatal("slow schema response unexpectedly succeeded")
		}
	})
}

func TestDiscoverWorkspacePackageNames(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"package.json":               `{"private":true,"workspaces":["packages/*","apps/*"]}`,
		"packages/core/package.json": `{"name":"@acme/core"}`,
		"packages/ui/package.json":   `{"name":"@acme/ui"}`,
		"apps/web/package.json":      `{"name":"@acme/web"}`,
		"apps/unnamed/package.json":  `{"private":true}`,
	}
	for relativePath, content := range files {
		filePath := filepath.Join(root, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := DiscoverWorkspacePackageNames(root)
	if err != nil {
		t.Fatalf("DiscoverWorkspacePackageNames: %v", err)
	}
	want := []string{"@acme/core", "@acme/ui", "@acme/web"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace package names = %#v, want %#v", got, want)
	}
}

func TestAddWorkspacePackageCompletionsToSchema(t *testing.T) {
	schema, _ := builtinJSONSchema(JSONSchemaPackage)
	augmented, err := AddWorkspacePackageCompletions(schema, []string{"@acme/core", "@acme/ui"})
	if err != nil {
		t.Fatalf("AddWorkspacePackageCompletions: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(augmented, &document); err != nil {
		t.Fatal(err)
	}
	properties := document["properties"].(map[string]any)
	for _, section := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		sectionSchema := properties[section].(map[string]any)
		packageProperties := sectionSchema["properties"].(map[string]any)
		for _, name := range []string{"@acme/core", "@acme/ui"} {
			if _, ok := packageProperties[name]; !ok {
				t.Errorf("%s schema missing workspace package completion %q", section, name)
			}
		}
	}
}

func TestJSONInitializationOptionsIncludeSchemasAndWorkspacePackages(t *testing.T) {
	root := t.TempDir()
	for relativePath, content := range map[string]string{
		"package.json":               `{"workspaces":["packages/*"]}`,
		"packages/core/package.json": `{"name":"@acme/core"}`,
	} {
		filePath := filepath.Join(root, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resolver := newJSONSchemaResolver(jsonSchemaResolverOptions{
		CacheDir: filepath.Join(root, ".cache"),
		Client: &http.Client{Transport: jsonSchemaRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})},
		AllowedHosts: []string{"json.schemastore.org"},
		Definitions:  jsonSchemaDefinitions,
	})

	options, err := resolver.InitializationOptions(context.Background(), root)
	if err != nil {
		t.Fatalf("InitializationOptions: %v", err)
	}
	if options["provideFormatter"] != true {
		t.Fatalf("provideFormatter = %#v, want true", options["provideFormatter"])
	}
	if got := options["workspacePackageNames"]; !reflect.DeepEqual(got, []string{"@acme/core"}) {
		t.Fatalf("workspacePackageNames = %#v", got)
	}
	associations, ok := options["schemas"].([]map[string]interface{})
	if !ok || len(associations) != 3 {
		t.Fatalf("schemas = %#v, want three associations", options["schemas"])
	}
	var packageSchema json.RawMessage
	for _, association := range associations {
		if association["kind"] == string(JSONSchemaPackage) {
			packageSchema, _ = association["schema"].(json.RawMessage)
		}
	}
	if len(packageSchema) == 0 || !json.Valid(packageSchema) {
		t.Fatalf("package schema association is missing or invalid: %s", packageSchema)
	}
	if !strings.Contains(string(packageSchema), `"@acme/core"`) {
		t.Fatalf("package schema does not include workspace completion: %s", packageSchema)
	}
}

func TestLSPJSONInitializationOptionsUseSchemaResolver(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"workspaces":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	options := lspInitializationOptions("json", root)
	associations, ok := options["schemas"].([]map[string]interface{})
	if !ok || len(associations) != 3 {
		t.Fatalf("JSON initialization schemas = %#v, want three resolver associations", options["schemas"])
	}
	if _, ok := options["workspacePackageNames"].([]string); !ok {
		t.Fatalf("workspacePackageNames = %#v, want []string", options["workspacePackageNames"])
	}
}
