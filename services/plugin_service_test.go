package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeTestPlugin writes a plugin.json manifest and (optionally) a main.js
// file to a plugin directory under root/<name>/. Returns the plugin dir.
func writeTestPlugin(t *testing.T, root, name string, manifest map[string]any, withMain bool) string {
	t.Helper()
	pluginDir := filepath.Join(root, name)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", pluginDir, err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	if withMain {
		mainPath := filepath.Join(pluginDir, manifest["main"].(string))
		if err := os.MkdirAll(filepath.Dir(mainPath), 0o755); err != nil {
			t.Fatalf("mkdir main dir: %v", err)
		}
		if err := os.WriteFile(mainPath, []byte("export function activate() {}\n"), 0o644); err != nil {
			t.Fatalf("write main.js: %v", err)
		}
	}
	return pluginDir
}

func validManifest() map[string]any {
	return map[string]any{
		"name":             "test-plugin",
		"version":          "1.0.0",
		"main":             "main.js",
		"activationEvents": []string{"onStartup"},
	}
}

// --- Manifest validation ---

func TestPluginManifest_Validate_Valid(t *testing.T) {
	cases := []struct {
		name string
		m    PluginManifest
	}{
		{"minimal", PluginManifest{
			Name: "my-plugin", Version: "1.0.0", Main: "main.js",
			ActivationEvents: []string{"onStartup"},
		}},
		{"with permissions", PluginManifest{
			Name: "my-plugin", Version: "1.0.0", Main: "main.js",
			ActivationEvents: []string{"onStartup"},
			Permissions:      []PluginPermission{"fs.read", "fs.write"},
		}},
		{"with command contribution", PluginManifest{
			Name: "my-plugin", Version: "2.5.1", Main: "main.js",
			ActivationEvents: []string{"onCommand:my-plugin.hello"},
			Contributes: PluginContribution{
				Commands: []PluginCommandContribution{
					{ID: "my-plugin.hello", Title: "Hello"},
				},
			},
		}},
		{"with public command contribution (Proposal E)", PluginManifest{
			Name: "my-plugin", Version: "2.5.1", Main: "main.js",
			ActivationEvents: []string{"onStartup"},
			Contributes: PluginContribution{
				Commands: []PluginCommandContribution{
					{ID: "my-plugin.api", Title: "API", Public: true},
				},
			},
		}},
		{"with view contribution", PluginManifest{
			Name: "my-plugin", Version: "0.1.0", Main: "main.js",
			ActivationEvents: []string{"onStartup"},
			Contributes: PluginContribution{
				Views: []PluginViewContribution{
					{ID: "my-plugin.view", Title: "My View", Location: "sidebar"},
				},
			},
		}},
		{"semver with pre-release", PluginManifest{
			Name: "my-plugin", Version: "1.0.0-beta.1", Main: "main.js",
			ActivationEvents: []string{"onStartup"},
		}},
		{"semver with build metadata", PluginManifest{
			Name: "my-plugin", Version: "1.0.0+build.42", Main: "main.js",
			ActivationEvents: []string{"onStartup"},
		}},
		{"name with multiple kebabs", PluginManifest{
			Name: "my-cool-plugin-2", Version: "1.0.0", Main: "main.js",
			ActivationEvents: []string{"onStartup"},
		}},
		{"schemaVersion 1", PluginManifest{
			SchemaVersion: 1, Name: "my-plugin", Version: "1.0.0", Main: "main.js",
			ActivationEvents: []string{"onStartup"},
		}},
		{"with metadata fields", PluginManifest{
			Name: "my-plugin", Version: "1.0.0", Main: "main.js",
			ActivationEvents: []string{"onStartup"},
			Author:           "jane", Repository: "https://github.com/jane/my-plugin",
			Homepage: "https://jane.dev/my-plugin", License: "MIT",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.m.Validate(); err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
		})
	}
}

func TestPluginManifest_Validate_Invalid(t *testing.T) {
	cases := []struct {
		name      string
		m         PluginManifest
		errSubstr string
	}{
		{
			name:      "empty name",
			m:         PluginManifest{Name: "", Version: "1.0.0", Main: "main.js", ActivationEvents: []string{"onStartup"}},
			errSubstr: "invalid plugin name",
		},
		{
			name:      "uppercase name",
			m:         PluginManifest{Name: "MyPlugin", Version: "1.0.0", Main: "main.js", ActivationEvents: []string{"onStartup"}},
			errSubstr: "invalid plugin name",
		},
		{
			name:      "name with spaces",
			m:         PluginManifest{Name: "my plugin", Version: "1.0.0", Main: "main.js", ActivationEvents: []string{"onStartup"}},
			errSubstr: "invalid plugin name",
		},
		{
			name:      "name with underscores",
			m:         PluginManifest{Name: "my_plugin", Version: "1.0.0", Main: "main.js", ActivationEvents: []string{"onStartup"}},
			errSubstr: "invalid plugin name",
		},
		{
			name:      "invalid version (non-semver)",
			m:         PluginManifest{Name: "my-plugin", Version: "1.0", Main: "main.js", ActivationEvents: []string{"onStartup"}},
			errSubstr: "invalid plugin version",
		},
		{
			name:      "invalid version (v-prefix)",
			m:         PluginManifest{Name: "my-plugin", Version: "v1.0.0", Main: "main.js", ActivationEvents: []string{"onStartup"}},
			errSubstr: "invalid plugin version",
		},
		{
			name:      "empty main",
			m:         PluginManifest{Name: "my-plugin", Version: "1.0.0", Main: "", ActivationEvents: []string{"onStartup"}},
			errSubstr: "main entry point is required",
		},
		{
			name:      "main not .js",
			m:         PluginManifest{Name: "my-plugin", Version: "1.0.0", Main: "main.ts", ActivationEvents: []string{"onStartup"}},
			errSubstr: "must be a .js file",
		},
		{
			name:      "main absolute path",
			m:         PluginManifest{Name: "my-plugin", Version: "1.0.0", Main: "/abs/main.js", ActivationEvents: []string{"onStartup"}},
			errSubstr: "must be a relative path",
		},
		{
			name:      "main parent traversal",
			m:         PluginManifest{Name: "my-plugin", Version: "1.0.0", Main: "../escape.js", ActivationEvents: []string{"onStartup"}},
			errSubstr: "must be a relative path",
		},
		{
			name:      "no activation events",
			m:         PluginManifest{Name: "my-plugin", Version: "1.0.0", Main: "main.js"},
			errSubstr: "at least one activationEvent",
		},
		{
			name:      "activation event not starting with on",
			m:         PluginManifest{Name: "my-plugin", Version: "1.0.0", Main: "main.js", ActivationEvents: []string{"startup"}},
			errSubstr: "must start with \"on\"",
		},
		{
			name:      "unknown permission",
			m:         PluginManifest{Name: "my-plugin", Version: "1.0.0", Main: "main.js", ActivationEvents: []string{"onStartup"}, Permissions: []PluginPermission{"fs.delete"}},
			errSubstr: "unknown permission",
		},
		{
			name: "command contribution missing id",
			m: PluginManifest{
				Name: "my-plugin", Version: "1.0.0", Main: "main.js",
				ActivationEvents: []string{"onStartup"},
				Contributes: PluginContribution{
					Commands: []PluginCommandContribution{{Title: "Hello"}},
				},
			},
			errSubstr: "commands[0].id is required",
		},
		{
			name: "view contribution invalid location",
			m: PluginManifest{
				Name: "my-plugin", Version: "1.0.0", Main: "main.js",
				ActivationEvents: []string{"onStartup"},
				Contributes: PluginContribution{
					Views: []PluginViewContribution{
						{ID: "v", Title: "V", Location: "nowhere"},
					},
				},
			},
			errSubstr: "location",
		},
		{
			name: "schemaVersion negative",
			m: PluginManifest{
				SchemaVersion: -1, Name: "my-plugin", Version: "1.0.0", Main: "main.js",
				ActivationEvents: []string{"onStartup"},
			},
			errSubstr: "schemaVersion cannot be negative",
		},
		{
			name: "schemaVersion future",
			m: PluginManifest{
				SchemaVersion: 2, Name: "my-plugin", Version: "1.0.0", Main: "main.js",
				ActivationEvents: []string{"onStartup"},
			},
			errSubstr: "unsupported schemaVersion",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.m.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.errSubstr)
			}
			if !contains(err.Error(), c.errSubstr) {
				t.Errorf("expected error containing %q, got %q", c.errSubstr, err.Error())
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Proposal I (prompt-4.md): The `public` field on command contributions
// must be a bool. Since the Go struct uses `Public bool`, JSON unmarshalling
// rejects non-bool values at parse time — before Validate() is even called.
// This test verifies that invariant: a manifest with `public: "yes"` (string)
// or `public: 1` (number) fails to parse.
//
// Note: `public: null` is accepted by Go's JSON decoder — it leaves the
// field at its zero value (false = private), which is the safe default.
func TestPluginManifest_PublicField_RejectsNonBoolJSON(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{
			name: `public as string "yes"`,
			json: `{"name":"p","version":"1.0.0","main":"main.js","activationEvents":["onStartup"],"contributes":{"commands":[{"id":"p.cmd","title":"Cmd","public":"yes"}]}}`,
		},
		{
			name: "public as number 1",
			json: `{"name":"p","version":"1.0.0","main":"main.js","activationEvents":["onStartup"],"contributes":{"commands":[{"id":"p.cmd","title":"Cmd","public":1}]}}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m PluginManifest
			err := json.Unmarshal([]byte(c.json), &m)
			if err == nil {
				t.Fatalf("expected JSON parse error for non-bool public field, got nil. Parsed: %+v", m)
			}
		})
	}
}

// Proposal I: A manifest with `public: true` or `public: false` parses
// correctly, and omitting the field defaults to false (private).
func TestPluginManifest_PublicField_AcceptsBoolAndOmits(t *testing.T) {
	cases := []struct {
		name         string
		json         string
		expectPublic bool
	}{
		{
			name:         "public true",
			json:         `{"name":"p","version":"1.0.0","main":"main.js","activationEvents":["onStartup"],"contributes":{"commands":[{"id":"p.cmd","title":"Cmd","public":true}]}}`,
			expectPublic: true,
		},
		{
			name:         "public false",
			json:         `{"name":"p","version":"1.0.0","main":"main.js","activationEvents":["onStartup"],"contributes":{"commands":[{"id":"p.cmd","title":"Cmd","public":false}]}}`,
			expectPublic: false,
		},
		{
			name:         "public omitted (default private)",
			json:         `{"name":"p","version":"1.0.0","main":"main.js","activationEvents":["onStartup"],"contributes":{"commands":[{"id":"p.cmd","title":"Cmd"}]}}`,
			expectPublic: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m PluginManifest
			if err := json.Unmarshal([]byte(c.json), &m); err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			if err := m.Validate(); err != nil {
				t.Fatalf("Validate failed: %v", err)
			}
			got := m.Contributes.Commands[0].Public
			if got != c.expectPublic {
				t.Errorf("Public = %v, want %v", got, c.expectPublic)
			}
		})
	}
}

// --- PluginService discovery ---

func TestPluginService_ListPlugins_Empty(t *testing.T) {
	svc := NewPluginService("")
	plugins, err := svc.ListPlugins("")
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins, got %d", len(plugins))
	}
}

func TestPluginService_ListPlugins_UserLayer(t *testing.T) {
	tmp := t.TempDir()
	svc := NewPluginService(tmp)
	writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), "alpha", validManifest(), true)

	// Rename to match the manifest name field. The manifest name is
	// "test-plugin" but the dir is "alpha" — discovery uses the dir name
	// for the lookup but the manifest's Name field for the plugin ID.
	// Overwrite the manifest to use name "alpha" for clarity.
	pluginDir := filepath.Join(tmp, userPluginsSubdir, "alpha")
	manifest := validManifest()
	manifest["name"] = "alpha"
	data, _ := json.MarshalIndent(manifest, "", "  ")
	_ = os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644)

	plugins, err := svc.ListPlugins("")
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	if plugins[0].Manifest.Name != "alpha" {
		t.Errorf("expected name alpha, got %s", plugins[0].Manifest.Name)
	}
	if plugins[0].Source != PluginSourceUser {
		t.Errorf("expected source user, got %s", plugins[0].Source)
	}
	if !plugins[0].Enabled {
		t.Errorf("expected enabled by default")
	}
	if !plugins[0].MainExists {
		t.Errorf("expected main exists")
	}
}

func TestPluginService_ListPlugins_ProjectOverridesUser(t *testing.T) {
	tmp := t.TempDir()
	projectRoot := t.TempDir()
	svc := NewPluginService(tmp)

	// User layer: plugin "shared" v1.0.0
	userManifest := validManifest()
	userManifest["name"] = "shared"
	userManifest["version"] = "1.0.0"
	writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), "shared", userManifest, true)

	// Project layer: plugin "shared" v2.0.0 (overrides user)
	projManifest := validManifest()
	projManifest["name"] = "shared"
	projManifest["version"] = "2.0.0"
	writeTestPlugin(t, filepath.Join(projectRoot, projectPluginsRel), "shared", projManifest, true)

	plugins, err := svc.ListPlugins(projectRoot)
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin (merged), got %d", len(plugins))
	}
	if plugins[0].Manifest.Version != "2.0.0" {
		t.Errorf("expected project version 2.0.0, got %s", plugins[0].Manifest.Version)
	}
	if plugins[0].Source != PluginSourceProject {
		t.Errorf("expected source project, got %s", plugins[0].Source)
	}
}

func TestPluginService_ListPlugins_SurfacesInvalidManifest(t *testing.T) {
	tmp := t.TempDir()
	svc := NewPluginService(tmp)

	// Valid plugin
	writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), "good", func() map[string]any {
		m := validManifest()
		m["name"] = "good"
		return m
	}(), true)

	// Invalid plugin (no activation events) — manifest exists but fails Validate.
	badDir := filepath.Join(tmp, userPluginsSubdir, "bad")
	_ = os.MkdirAll(badDir, 0o755)
	_ = os.WriteFile(filepath.Join(badDir, "plugin.json"), []byte(`{"name":"bad","version":"1.0.0","main":"main.js"}`), 0o644)

	// Malformed JSON — manifest exists but can't be parsed.
	badDir2 := filepath.Join(tmp, userPluginsSubdir, "broken")
	_ = os.MkdirAll(badDir2, 0o755)
	_ = os.WriteFile(filepath.Join(badDir2, "plugin.json"), []byte(`{not json`), 0o644)

	// No manifest — directory is not a plugin, should be skipped silently.
	_ = os.MkdirAll(filepath.Join(tmp, userPluginsSubdir, "no-manifest"), 0o755)

	plugins, err := svc.ListPlugins("")
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	// N-111: broken plugins are now surfaced (not silently dropped).
	// 1 valid (good) + 2 broken (bad, broken). The no-manifest dir is
	// skipped because it has no plugin.json (not a plugin).
	if len(plugins) != 3 {
		t.Fatalf("expected 3 plugins (1 valid + 2 broken), got %d", len(plugins))
	}

	// Build a map for easy lookup.
	byName := make(map[string]PluginInfo, len(plugins))
	for _, p := range plugins {
		byName[p.Manifest.Name] = p
	}

	good, ok := byName["good"]
	if !ok {
		t.Fatal("expected 'good' plugin in list")
	}
	if good.LoadError != "" {
		t.Errorf("good plugin should have no LoadError, got %q", good.LoadError)
	}

	bad, ok := byName["bad"]
	if !ok {
		t.Fatal("expected 'bad' plugin in list")
	}
	if bad.LoadError == "" {
		t.Error("bad plugin should have LoadError set (invalid manifest)")
	}

	broken, ok := byName["broken"]
	if !ok {
		t.Fatal("expected 'broken' plugin in list")
	}
	if broken.LoadError == "" {
		t.Error("broken plugin should have LoadError set (malformed JSON)")
	}

	// The no-manifest dir should NOT appear.
	if _, exists := byName["no-manifest"]; exists {
		t.Error("no-manifest dir should be skipped (no plugin.json)")
	}
}

func TestPluginService_ListPlugins_SortedByName(t *testing.T) {
	tmp := t.TempDir()
	svc := NewPluginService(tmp)
	for _, name := range []string{"zeta", "alpha", "mike"} {
		m := validManifest()
		m["name"] = name
		writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), name, m, true)
	}
	plugins, err := svc.ListPlugins("")
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(plugins) != 3 {
		t.Fatalf("expected 3 plugins, got %d", len(plugins))
	}
	expected := []string{"alpha", "mike", "zeta"}
	for i, want := range expected {
		if plugins[i].Manifest.Name != want {
			t.Errorf("index %d: expected %s, got %s", i, want, plugins[i].Manifest.Name)
		}
	}
}

// --- Enable/disable persistence ---

func TestPluginService_SetPluginEnabled_Persists(t *testing.T) {
	tmp := t.TempDir()
	svc := NewPluginService(tmp)
	writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), "alpha", func() map[string]any {
		m := validManifest()
		m["name"] = "alpha"
		return m
	}(), true)

	if err := svc.SetPluginEnabled("alpha", false); err != nil {
		t.Fatalf("SetPluginEnabled: %v", err)
	}

	// Verify state file was written.
	statePath := filepath.Join(tmp, "koyori-ide", pluginStateFileName)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var state pluginStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if entry, ok := state.Plugins["alpha"]; !ok || entry.Enabled {
		t.Errorf("expected alpha disabled in state file, got %+v", state.Plugins)
	}

	// Verify ListPlugins reflects the persisted state.
	plugins, err := svc.ListPlugins("")
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if plugins[0].Enabled {
		t.Errorf("expected alpha disabled in ListPlugins, got enabled")
	}
}

func TestPluginService_SetPluginEnabled_ToggleBack(t *testing.T) {
	tmp := t.TempDir()
	svc := NewPluginService(tmp)
	if err := svc.SetPluginEnabled("alpha", false); err != nil {
		t.Fatalf("SetPluginEnabled false: %v", err)
	}
	if err := svc.SetPluginEnabled("alpha", true); err != nil {
		t.Fatalf("SetPluginEnabled true: %v", err)
	}
	_, err := svc.ListPlugins("")
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	// State file should reflect enabled=true.
	state := svc.loadPluginState()
	if entry, ok := state.Plugins["alpha"]; !ok || !entry.Enabled {
		t.Errorf("expected alpha enabled=true in state, got %+v", state.Plugins["alpha"])
	}
}

func TestPluginService_SetPluginEnabled_EmptyName(t *testing.T) {
	svc := NewPluginService(t.TempDir())
	if err := svc.SetPluginEnabled("", true); err == nil {
		t.Errorf("expected error for empty name")
	}
}

func TestPluginService_SetPluginEnabled_NoConfigDir(t *testing.T) {
	svc := NewPluginService("")
	if err := svc.SetPluginEnabled("alpha", true); err == nil {
		t.Errorf("expected error when configDir is empty")
	}
}

// --- ReadPluginFile ---

func TestPluginService_ReadPluginFile_Valid(t *testing.T) {
	tmp := t.TempDir()
	svc := NewPluginService(tmp)
	writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), "alpha", func() map[string]any {
		m := validManifest()
		m["name"] = "alpha"
		return m
	}(), true)

	data, err := svc.ReadPluginFile("alpha", "main.js", "")
	if err != nil {
		t.Fatalf("ReadPluginFile: %v", err)
	}
	if len(data) == 0 {
		t.Errorf("expected non-empty file content")
	}
}

func TestPluginService_ReadPluginFile_PathTraversal(t *testing.T) {
	tmp := t.TempDir()
	svc := NewPluginService(tmp)
	writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), "alpha", func() map[string]any {
		m := validManifest()
		m["name"] = "alpha"
		return m
	}(), true)

	cases := []string{"../escape.js", "/etc/passwd"}
	for _, rel := range cases {
		t.Run(rel, func(t *testing.T) {
			_, err := svc.ReadPluginFile("alpha", rel, "")
			if err == nil {
				t.Errorf("expected error for path %s", rel)
			}
		})
	}
}

func TestPluginService_ReadPluginFile_NotFound(t *testing.T) {
	tmp := t.TempDir()
	svc := NewPluginService(tmp)
	writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), "alpha", func() map[string]any {
		m := validManifest()
		m["name"] = "alpha"
		return m
	}(), true)

	_, err := svc.ReadPluginFile("alpha", "missing.js", "")
	if err == nil {
		t.Errorf("expected error for missing file")
	}
}

func TestPluginService_ReadPluginFile_RejectsOversizedFile(t *testing.T) {
	tmp := t.TempDir()
	svc := NewPluginService(tmp)
	pluginDir := writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), "alpha", func() map[string]any {
		m := validManifest()
		m["name"] = "alpha"
		return m
	}(), false)
	path := filepath.Join(pluginDir, "main.js")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxReadableFileBytes+1); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.ReadPluginFile("alpha", "main.js", ""); err == nil || !strings.Contains(err.Error(), "maximum size") {
		t.Fatalf("ReadPluginFile error = %v, want oversized-file error", err)
	}
}

func TestPluginService_ReadPluginFile_PluginNotFound(t *testing.T) {
	svc := NewPluginService(t.TempDir())
	_, err := svc.ReadPluginFile("nonexistent", "main.js", "")
	if err == nil {
		t.Errorf("expected error for nonexistent plugin")
	}
}

// --- GetPlugin ---

func TestPluginService_GetPlugin_Found(t *testing.T) {
	tmp := t.TempDir()
	svc := NewPluginService(tmp)
	writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), "alpha", func() map[string]any {
		m := validManifest()
		m["name"] = "alpha"
		return m
	}(), true)

	info, err := svc.GetPlugin("alpha", "")
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if info.Manifest.Name != "alpha" {
		t.Errorf("expected alpha, got %s", info.Manifest.Name)
	}
}

func TestPluginService_GetPlugin_NotFound(t *testing.T) {
	svc := NewPluginService(t.TempDir())
	_, err := svc.GetPlugin("nonexistent", "")
	if err == nil {
		t.Errorf("expected error for nonexistent plugin")
	}
}

// --- Plan 58 / N-21: ServePluginAsset tests ---

func TestPluginService_ServePluginAsset_ReturnsContentAndMime(t *testing.T) {
	configDir := t.TempDir()
	svc := NewPluginService(configDir)
	writeTestPlugin(t, filepath.Join(configDir, userPluginsSubdir), "test-plugin", validManifest(), true)

	data, mime, err := svc.ServePluginAsset("test-plugin", "main.js", "")
	if err != nil {
		t.Fatalf("ServePluginAsset failed: %v", err)
	}
	if string(data) != "export function activate() {}\n" {
		t.Errorf("unexpected content: %q", string(data))
	}
	if mime != "application/javascript" {
		t.Errorf("expected application/javascript, got %q", mime)
	}
}

func TestPluginService_ServePluginAsset_PathTraversal(t *testing.T) {
	configDir := t.TempDir()
	svc := NewPluginService(configDir)
	writeTestPlugin(t, filepath.Join(configDir, userPluginsSubdir), "test-plugin", validManifest(), true)

	if _, _, err := svc.ServePluginAsset("test-plugin", "../plugin.json", ""); err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestPluginService_ServePluginAsset_PluginNotFound(t *testing.T) {
	svc := NewPluginService(t.TempDir())
	if _, _, err := svc.ServePluginAsset("nonexistent", "main.js", ""); err == nil {
		t.Error("expected error for nonexistent plugin")
	}
}

func TestPluginService_ServePluginAsset_FileNotFound(t *testing.T) {
	configDir := t.TempDir()
	svc := NewPluginService(configDir)
	writeTestPlugin(t, filepath.Join(configDir, userPluginsSubdir), "test-plugin", validManifest(), true)

	if _, _, err := svc.ServePluginAsset("test-plugin", "nonexistent.js", ""); err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestPluginService_ServePluginAsset_ProjectScopedPlugin(t *testing.T) {
	configDir := t.TempDir()
	projectRoot := t.TempDir()
	svc := NewPluginService(configDir)
	manifest := validManifest()
	manifest["name"] = "proj-plugin"
	writeTestPlugin(t, filepath.Join(projectRoot, projectPluginsRel), "proj-plugin", manifest, true)

	data, mime, err := svc.ServePluginAsset("proj-plugin", "main.js", projectRoot)
	if err != nil {
		t.Fatalf("ServePluginAsset failed: %v", err)
	}
	if string(data) != "export function activate() {}\n" {
		t.Errorf("unexpected content: %q", string(data))
	}
	if mime != "application/javascript" {
		t.Errorf("expected application/javascript, got %q", mime)
	}
}

func TestPluginAssetMimeType(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"main.js", "application/javascript"},
		{"index.mjs", "application/javascript"},
		{"data.json", "application/json"},
		{"style.css", "text/css"},
		{"view.html", "text/html"},
		{"icon.svg", "image/svg+xml"},
		{"logo.png", "image/png"},
		{"photo.jpg", "image/jpeg"},
		{"photo.jpeg", "image/jpeg"},
		{"anim.gif", "image/gif"},
		{"font.woff", "font/woff"},
		{"font.woff2", "font/woff2"},
		{"font.ttf", "font/ttf"},
		{"favicon.ico", "image/x-icon"},
		{"unknown.xyz", "application/octet-stream"},
		{"noext", "application/octet-stream"},
		{"UPPER.JS", "application/javascript"}, // case-insensitive
	}
	for _, c := range cases {
		got := pluginAssetMimeType(c.path)
		if got != c.want {
			t.Errorf("pluginAssetMimeType(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// C-3: PluginService must be safe under concurrent access. SetPluginEnabled
// does a load-modify-save on the state file; without the mutex, two
// concurrent writers would race (TOCTOU) and one update would be lost, and
// the race detector would flag the unsynchronized file access. This test
// runs under `go test -race` to catch both data races and lost updates.
func TestPluginService_ConcurrentSafe_C3(t *testing.T) {
	tmp := t.TempDir()
	svc := NewPluginService(tmp)
	writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), "alpha", func() map[string]any {
		m := validManifest()
		m["name"] = "alpha"
		return m
	}(), true)
	writeTestPlugin(t, filepath.Join(tmp, userPluginsSubdir), "beta", func() map[string]any {
		m := validManifest()
		m["name"] = "beta"
		return m
	}(), true)

	const goroutines = 40
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	// Half the goroutines flip plugin enabled state (writers).
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			name := "alpha"
			if i%2 == 1 {
				name = "beta"
			}
			if err := svc.SetPluginEnabled(name, i%3 == 0); err != nil {
				t.Errorf("SetPluginEnabled(%s): %v", name, err)
			}
		}(i)
		// Half the goroutines list plugins (readers).
		go func() {
			defer wg.Done()
			plugins, err := svc.ListPlugins("")
			if err != nil {
				t.Errorf("ListPlugins: %v", err)
				return
			}
			if len(plugins) != 2 {
				t.Errorf("expected 2 plugins, got %d", len(plugins))
			}
		}()
	}
	wg.Wait()

	// After all the concurrent churn settles, the state file must be
	// internally consistent: both plugins present.
	state := svc.loadPluginState()
	if _, ok := state.Plugins["alpha"]; !ok {
		t.Errorf("alpha state missing after concurrent writes: %+v", state.Plugins)
	}
	if _, ok := state.Plugins["beta"]; !ok {
		t.Errorf("beta state missing after concurrent writes: %+v", state.Plugins)
	}
}

// TestPluginService_L3_PathSecurityUsesIsRelativePathSafe 验证 L-3 修复:
// isPluginPathOutsideRoot 已被删除,路径校验统一使用 pathsec.IsRelativePathSafe。
// 此测试确认重构后,manifest 校验和 ReadPluginFile 仍然拒绝所有危险路径,
// 且接受合法的相对路径(含子目录)。
func TestPluginService_L3_PathSecurityUsesIsRelativePathSafe(t *testing.T) {
	// 1. Manifest Main 字段校验:各种危险路径应被拒绝(跨平台一致)。
	dangerous := []string{
		"../escape.js",  // 父目录遍历(Unix 风格)
		"/abs/main.js",  // Unix 绝对路径
		"..\\escape.js", // 父目录遍历(Windows 风格)
	}
	for _, main := range dangerous {
		m := PluginManifest{
			Name:             "my-plugin",
			Version:          "1.0.0",
			Main:             main,
			ActivationEvents: []string{"onStartup"},
		}
		if err := m.Validate(); err == nil {
			t.Errorf("Validate() with Main=%q should return error, got nil", main)
		} else if !strings.Contains(err.Error(), "must be a relative path") {
			t.Errorf("Validate() with Main=%q returned unexpected error: %v", main, err)
		}
	}

	// 2. 安全的相对路径(含子目录)应通过校验。
	safe := []string{"main.js", "src/main.js", "bundle/index.js"}
	for _, main := range safe {
		m := PluginManifest{
			Name:             "my-plugin",
			Version:          "1.0.0",
			Main:             main,
			ActivationEvents: []string{"onStartup"},
		}
		if err := m.Validate(); err != nil {
			t.Errorf("Validate() with Main=%q should succeed, got error: %v", main, err)
		}
	}

	// 3. ReadPluginFile 拒绝危险 relPath。
	configDir := t.TempDir()
	svc := NewPluginService(configDir)
	writeTestPlugin(t, filepath.Join(configDir, userPluginsSubdir), "alpha", func() map[string]any {
		m := validManifest()
		m["name"] = "alpha"
		return m
	}(), true)
	for _, rel := range dangerous {
		if _, err := svc.ReadPluginFile("alpha", rel, ""); err == nil {
			t.Errorf("ReadPluginFile(alpha, %q) should return error, got nil", rel)
		}
	}
}
