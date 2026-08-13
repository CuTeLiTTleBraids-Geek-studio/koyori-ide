package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type JSONSchemaKind string

type jsonSchemaDefinition struct {
	Kind      JSONSchemaKind
	URL       string
	CacheName string
	FileMatch []string
}

type JSONSchemaSource string

const (
	JSONSchemaSourceNetwork JSONSchemaSource = "network"
	JSONSchemaSourceCache   JSONSchemaSource = "cache"
	JSONSchemaSourceBuiltin JSONSchemaSource = "builtin"
)

type ResolvedJSONSchema struct {
	Kind      JSONSchemaKind
	Source    JSONSchemaSource
	URL       string
	CachePath string
	FileMatch []string
	Schema    json.RawMessage
}

type jsonSchemaResolverOptions struct {
	CacheDir     string
	Client       *http.Client
	AllowedHosts []string
	Timeout      time.Duration
	MaxBodyBytes int64
	Definitions  []jsonSchemaDefinition
}

type JSONSchemaResolver struct {
	cacheDir     string
	client       *http.Client
	allowedHosts map[string]struct{}
	timeout      time.Duration
	maxBodyBytes int64
	definitions  map[JSONSchemaKind]jsonSchemaDefinition
}

const (
	JSONSchemaNone     JSONSchemaKind = ""
	JSONSchemaTSConfig JSONSchemaKind = "tsconfig"
	JSONSchemaJSConfig JSONSchemaKind = "jsconfig"
	JSONSchemaPackage  JSONSchemaKind = "package"
)

var jsonSchemaDefinitions = []jsonSchemaDefinition{
	{
		Kind:      JSONSchemaTSConfig,
		URL:       "https://json.schemastore.org/tsconfig.json",
		CacheName: "tsconfig.schema.json",
		FileMatch: []string{"**/tsconfig.json", "**/tsconfig.*.json"},
	},
	{
		Kind:      JSONSchemaJSConfig,
		URL:       "https://json.schemastore.org/jsconfig.json",
		CacheName: "jsconfig.schema.json",
		FileMatch: []string{"**/jsconfig.json", "**/jsconfig.*.json"},
	},
	{
		Kind:      JSONSchemaPackage,
		URL:       "https://json.schemastore.org/package.json",
		CacheName: "package.schema.json",
		FileMatch: []string{"**/package.json"},
	},
}

const (
	defaultJSONSchemaTimeout      = 5 * time.Second
	defaultJSONSchemaMaxBodyBytes = 2 << 20
	jsonLSPOptionsTimeout         = 3 * time.Second
)

func NewJSONSchemaResolver(cacheDir string) *JSONSchemaResolver {
	return newJSONSchemaResolver(jsonSchemaResolverOptions{
		CacheDir:     cacheDir,
		Client:       http.DefaultClient,
		AllowedHosts: []string{"json.schemastore.org"},
		Timeout:      defaultJSONSchemaTimeout,
		MaxBodyBytes: defaultJSONSchemaMaxBodyBytes,
		Definitions:  jsonSchemaDefinitions,
	})
}

func BuildJSONLSPInitializationOptions(workspaceRoot string) map[string]interface{} {
	cacheDir := filepath.Join(workspaceRoot, ".koyori-ide", "json-schema-cache")
	resolver := NewJSONSchemaResolver(cacheDir)
	ctx, cancel := context.WithTimeout(context.Background(), jsonLSPOptionsTimeout)
	defer cancel()
	options, err := resolver.InitializationOptions(ctx, workspaceRoot)
	if err != nil {
		return map[string]interface{}{"provideFormatter": true}
	}
	return options
}

func newJSONSchemaResolver(options jsonSchemaResolverOptions) *JSONSchemaResolver {
	if options.Client == nil {
		options.Client = http.DefaultClient
	}
	if options.Timeout <= 0 {
		options.Timeout = defaultJSONSchemaTimeout
	}
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = defaultJSONSchemaMaxBodyBytes
	}
	allowedHosts := make(map[string]struct{}, len(options.AllowedHosts))
	for _, host := range options.AllowedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			allowedHosts[host] = struct{}{}
		}
	}
	definitions := make(map[JSONSchemaKind]jsonSchemaDefinition, len(options.Definitions))
	for _, definition := range options.Definitions {
		definition.FileMatch = append([]string(nil), definition.FileMatch...)
		definitions[definition.Kind] = definition
	}
	return &JSONSchemaResolver{
		cacheDir:     options.CacheDir,
		client:       options.Client,
		allowedHosts: allowedHosts,
		timeout:      options.Timeout,
		maxBodyBytes: options.MaxBodyBytes,
		definitions:  definitions,
	}
}

func (r *JSONSchemaResolver) Resolve(ctx context.Context, kind JSONSchemaKind) (ResolvedJSONSchema, error) {
	definition, ok := r.definitions[kind]
	if !ok {
		return ResolvedJSONSchema{}, fmt.Errorf("unsupported JSON schema kind %q", kind)
	}
	if schema, cachePath, ok := r.readCache(definition.CacheName); ok {
		return ResolvedJSONSchema{
			Kind: kind, Source: JSONSchemaSourceCache, URL: definition.URL,
			CachePath: cachePath, FileMatch: append([]string(nil), definition.FileMatch...), Schema: schema,
		}, nil
	}
	schema, err := r.fetch(ctx, definition.URL)
	if err != nil {
		builtin, ok := builtinJSONSchema(kind)
		if !ok {
			return ResolvedJSONSchema{}, err
		}
		return ResolvedJSONSchema{
			Kind: kind, Source: JSONSchemaSourceBuiltin, URL: definition.URL,
			FileMatch: append([]string(nil), definition.FileMatch...), Schema: builtin,
		}, nil
	}
	cachePath, err := r.writeCache(definition.CacheName, schema)
	if err != nil {
		return ResolvedJSONSchema{}, err
	}
	return ResolvedJSONSchema{
		Kind: kind, Source: JSONSchemaSourceNetwork, URL: definition.URL,
		CachePath: cachePath, FileMatch: append([]string(nil), definition.FileMatch...), Schema: schema,
	}, nil
}

func (r *JSONSchemaResolver) InitializationOptions(ctx context.Context, workspaceRoot string) (map[string]interface{}, error) {
	packageNames, err := DiscoverWorkspacePackageNames(workspaceRoot)
	if err != nil {
		return nil, err
	}
	associations := make([]map[string]interface{}, 0, len(jsonSchemaDefinitions))
	for _, definition := range jsonSchemaDefinitions {
		if _, ok := r.definitions[definition.Kind]; !ok {
			continue
		}
		resolved, resolveErr := r.Resolve(ctx, definition.Kind)
		if resolveErr != nil {
			return nil, resolveErr
		}
		schema := []byte(resolved.Schema)
		if definition.Kind == JSONSchemaPackage {
			schema, err = AddWorkspacePackageCompletions(schema, packageNames)
			if err != nil {
				return nil, err
			}
		}
		associations = append(associations, map[string]interface{}{
			"kind":      string(definition.Kind),
			"url":       definition.URL,
			"fileMatch": append([]string(nil), definition.FileMatch...),
			"schema":    json.RawMessage(schema),
		})
	}
	return map[string]interface{}{
		"provideFormatter":      true,
		"schemas":               associations,
		"workspacePackageNames": packageNames,
	}, nil
}

func (r *JSONSchemaResolver) readCache(name string) ([]byte, string, bool) {
	if name == "" || filepath.Base(name) != name {
		return nil, "", false
	}
	cachePath := filepath.Join(r.cacheDir, name)
	file, err := os.Open(cachePath)
	if err != nil {
		return nil, "", false
	}
	body, readErr := io.ReadAll(io.LimitReader(file, r.maxBodyBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(body)) > r.maxBodyBytes || !json.Valid(body) {
		return nil, "", false
	}
	if err := os.Chmod(cachePath, 0o600); err != nil {
		return nil, "", false
	}
	return body, cachePath, true
}

func (r *JSONSchemaResolver) fetch(ctx context.Context, rawURL string) ([]byte, error) {
	if err := r.validateURL(rawURL); err != nil {
		return nil, err
	}
	client := *r.client
	client.Timeout = r.timeout
	client.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		return r.validateURL(req.URL.String())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create schema request: %w", err)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download schema: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("download schema: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, r.maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	if int64(len(body)) > r.maxBodyBytes {
		return nil, fmt.Errorf("schema exceeds %d byte limit", r.maxBodyBytes)
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("schema response is not valid JSON")
	}
	return body, nil
}

func (r *JSONSchemaResolver) validateURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid schema URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.User != nil {
		return fmt.Errorf("schema URL must use HTTPS without credentials")
	}
	host := strings.ToLower(parsed.Hostname())
	if _, ok := r.allowedHosts[host]; !ok {
		return fmt.Errorf("schema host %q is not allowed", host)
	}
	return nil
}

func (r *JSONSchemaResolver) writeCache(name string, schema []byte) (string, error) {
	if name == "" || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid schema cache name %q", name)
	}
	if err := os.MkdirAll(r.cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("create schema cache: %w", err)
	}
	if err := os.Chmod(r.cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("secure schema cache directory: %w", err)
	}
	cachePath := filepath.Join(r.cacheDir, name)
	if err := os.WriteFile(cachePath, schema, 0o600); err != nil {
		return "", fmt.Errorf("write schema cache: %w", err)
	}
	if err := os.Chmod(cachePath, 0o600); err != nil {
		return "", fmt.Errorf("secure schema cache: %w", err)
	}
	return cachePath, nil
}

func jsonSchemaDefinitionForKind(kind JSONSchemaKind) (jsonSchemaDefinition, bool) {
	for _, definition := range jsonSchemaDefinitions {
		if definition.Kind == kind {
			definition.FileMatch = append([]string(nil), definition.FileMatch...)
			return definition, true
		}
	}
	return jsonSchemaDefinition{}, false
}

const builtinTSConfigSchema = `{
  "$schema":"http://json-schema.org/draft-07/schema#",
  "$id":"koyori-ide://schemas/tsconfig",
  "type":"object",
  "properties":{
    "compilerOptions":{
      "type":"object",
      "properties":{
        "target":{"type":"string","enum":["ES3","ES5","ES6","ES2015","ES2016","ES2017","ES2018","ES2019","ES2020","ES2021","ES2022","ESNext"]},
        "module":{"type":"string","enum":["None","CommonJS","AMD","UMD","System","ES6","ES2015","ES2020","ES2022","ESNext","Node16","NodeNext","Preserve"]},
        "moduleResolution":{"type":"string","enum":["Classic","Node","Node10","Node16","NodeNext","Bundler"]},
        "strict":{"type":"boolean"},
        "allowJs":{"type":"boolean"},
        "checkJs":{"type":"boolean"},
        "jsx":{"type":"string","enum":["preserve","react","react-jsx","react-jsxdev","react-native"]},
        "baseUrl":{"type":"string"},
        "paths":{"type":"object","additionalProperties":{"type":"array","items":{"type":"string"}}},
        "types":{"type":"array","items":{"type":"string"}}
      },
      "additionalProperties":true
    },
    "include":{"type":"array","items":{"type":"string"}},
    "exclude":{"type":"array","items":{"type":"string"}},
    "files":{"type":"array","items":{"type":"string"}},
    "extends":{"oneOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]},
    "references":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}
  },
  "additionalProperties":true
}`

const builtinJSConfigSchema = `{
  "$schema":"http://json-schema.org/draft-07/schema#",
  "$id":"koyori-ide://schemas/jsconfig",
  "type":"object",
  "properties":{
    "compilerOptions":{
      "type":"object",
      "properties":{
        "allowJs":{"type":"boolean"},
        "checkJs":{"type":"boolean"},
        "target":{"type":"string"},
        "module":{"type":"string"},
        "moduleResolution":{"type":"string"},
        "jsx":{"type":"string"},
        "baseUrl":{"type":"string"},
        "paths":{"type":"object","additionalProperties":{"type":"array","items":{"type":"string"}}},
        "types":{"type":"array","items":{"type":"string"}}
      },
      "additionalProperties":true
    },
    "include":{"type":"array","items":{"type":"string"}},
    "exclude":{"type":"array","items":{"type":"string"}},
    "files":{"type":"array","items":{"type":"string"}},
    "extends":{"oneOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]}
  },
  "additionalProperties":true
}`

const builtinPackageJSONSchema = `{
  "$schema":"http://json-schema.org/draft-07/schema#",
  "$id":"koyori-ide://schemas/package-json",
  "type":"object",
  "properties":{
    "name":{"type":"string"},
    "version":{"type":"string"},
    "private":{"type":"boolean"},
    "description":{"type":"string"},
    "type":{"type":"string","enum":["commonjs","module"]},
    "main":{"type":"string"},
    "module":{"type":"string"},
    "types":{"type":"string"},
    "scripts":{"type":"object","additionalProperties":{"type":"string"}},
    "dependencies":{"type":"object","additionalProperties":{"type":"string"}},
    "devDependencies":{"type":"object","additionalProperties":{"type":"string"}},
    "peerDependencies":{"type":"object","additionalProperties":{"type":"string"}},
    "optionalDependencies":{"type":"object","additionalProperties":{"type":"string"}},
    "engines":{"type":"object","additionalProperties":{"type":"string"}},
    "packageManager":{"type":"string"},
    "workspaces":{"oneOf":[
      {"type":"array","items":{"type":"string"}},
      {"type":"object","properties":{"packages":{"type":"array","items":{"type":"string"}}},"required":["packages"]}
    ]}
  },
  "additionalProperties":true
}`

func builtinJSONSchema(kind JSONSchemaKind) ([]byte, bool) {
	var schema string
	switch kind {
	case JSONSchemaTSConfig:
		schema = builtinTSConfigSchema
	case JSONSchemaJSConfig:
		schema = builtinJSConfigSchema
	case JSONSchemaPackage:
		schema = builtinPackageJSONSchema
	default:
		return nil, false
	}
	return []byte(schema), true
}

func JSONSchemaKindForPath(filePath string) JSONSchemaKind {
	base := strings.ToLower(path.Base(strings.ReplaceAll(filePath, `\`, "/")))
	switch {
	case base == "tsconfig.json" || strings.HasPrefix(base, "tsconfig.") && strings.HasSuffix(base, ".json"):
		return JSONSchemaTSConfig
	case base == "jsconfig.json" || strings.HasPrefix(base, "jsconfig.") && strings.HasSuffix(base, ".json"):
		return JSONSchemaJSConfig
	case base == "package.json":
		return JSONSchemaPackage
	default:
		return JSONSchemaNone
	}
}

const maxWorkspacePackageJSONBytes = 1 << 20

func DiscoverWorkspacePackageNames(root string) ([]string, error) {
	manifestData, err := readLimitedFile(filepath.Join(root, "package.json"), maxWorkspacePackageJSONBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read workspace package.json: %w", err)
	}
	var manifest struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("parse workspace package.json: %w", err)
	}
	patterns := parseWorkspacePatterns(manifest.Workspaces)
	names := make(map[string]struct{})
	for _, pattern := range patterns {
		pattern = filepath.Clean(filepath.FromSlash(strings.TrimSpace(pattern)))
		if pattern == "." || filepath.IsAbs(pattern) || pattern == ".." || strings.HasPrefix(pattern, ".."+string(filepath.Separator)) {
			continue
		}
		matches, globErr := filepath.Glob(filepath.Join(root, pattern))
		if globErr != nil {
			continue
		}
		for _, match := range matches {
			relative, relErr := filepath.Rel(root, match)
			if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			packagePath := match
			if info, statErr := os.Stat(match); statErr == nil && info.IsDir() {
				packagePath = filepath.Join(match, "package.json")
			}
			name := readWorkspacePackageName(packagePath)
			if name != "" {
				names[name] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func AddWorkspacePackageCompletions(schema []byte, packageNames []string) ([]byte, error) {
	var document map[string]interface{}
	if err := json.Unmarshal(schema, &document); err != nil {
		return nil, fmt.Errorf("parse package schema: %w", err)
	}
	properties, ok := document["properties"].(map[string]interface{})
	if !ok {
		properties = make(map[string]interface{})
		document["properties"] = properties
	}
	for _, section := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		sectionSchema, ok := properties[section].(map[string]interface{})
		if !ok {
			sectionSchema = map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}}
			properties[section] = sectionSchema
		}
		packageProperties, ok := sectionSchema["properties"].(map[string]interface{})
		if !ok {
			packageProperties = make(map[string]interface{})
			sectionSchema["properties"] = packageProperties
		}
		for _, name := range packageNames {
			name = strings.TrimSpace(name)
			if name != "" {
				packageProperties[name] = map[string]interface{}{"type": "string", "description": "Workspace package"}
			}
		}
	}
	augmented, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode package schema: %w", err)
	}
	return augmented, nil
}

func parseWorkspacePatterns(raw json.RawMessage) []string {
	var patterns []string
	if len(raw) == 0 || json.Unmarshal(raw, &patterns) == nil {
		return patterns
	}
	var workspaces struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(raw, &workspaces) != nil {
		return nil
	}
	return workspaces.Packages
}

func readWorkspacePackageName(packagePath string) string {
	data, err := readLimitedFile(packagePath, maxWorkspacePackageJSONBytes)
	if err != nil {
		return ""
	}
	var manifest struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return ""
	}
	return strings.TrimSpace(manifest.Name)
}

func readLimitedFile(filePath string, limit int64) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d byte limit", limit)
	}
	return data, nil
}
