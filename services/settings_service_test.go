package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSettingsService_LoadSettings_returnsDefaultsWhenNoFile(t *testing.T) {
	svc := &SettingsService{configPath: filepath.Join(t.TempDir(), "settings.json")}
	settings, err := svc.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if settings.FontSize != 14 {
		t.Errorf("expected default font size 14, got %d", settings.FontSize)
	}
	if settings.Theme != "dark" {
		t.Errorf("expected default theme 'dark', got '%s'", settings.Theme)
	}
	if settings.WordWrap != true {
		t.Error("expected default wordWrap true")
	}
}

func TestSettingsService_AIWindowPreferencesRoundTrip(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}
	settings := defaultSettings()
	settings.AIWindowTheme = "claude-light"
	settings.AISidebarWidth = 336
	settings.AITerminalWidth = 512

	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	loaded, err := svc.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if loaded.AIWindowTheme != "claude-light" {
		t.Fatalf("expected claude-light, got %q", loaded.AIWindowTheme)
	}
	if loaded.AISidebarWidth != 336 {
		t.Fatalf("expected sidebar width 336, got %d", loaded.AISidebarWidth)
	}
	if loaded.AITerminalWidth != 512 {
		t.Fatalf("expected terminal width 512, got %d", loaded.AITerminalWidth)
	}
}

func TestSettingsService_AIWindowPreferencesLegacyDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(configPath, []byte(`{"language":"zh","theme":"dark"}`), 0o600); err != nil {
		t.Fatalf("write legacy settings: %v", err)
	}

	svc := &SettingsService{configPath: configPath}
	loaded, err := svc.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if loaded.AIWindowTheme != "apple-dark" {
		t.Fatalf("expected apple-dark, got %q", loaded.AIWindowTheme)
	}
	if loaded.AISidebarWidth != 288 {
		t.Fatalf("expected sidebar width 288, got %d", loaded.AISidebarWidth)
	}
	if loaded.AITerminalWidth != 440 {
		t.Fatalf("expected terminal width 440, got %d", loaded.AITerminalWidth)
	}
}

func TestSettingsService_SaveAndLoad(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.FontSize = 18
	settings.TabSize = 4
	settings.WordWrap = false

	err := svc.SaveSettings(settings)
	if err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	svc2 := &SettingsService{configPath: configPath}
	loaded, err := svc2.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if loaded.FontSize != 18 {
		t.Errorf("expected font size 18, got %d", loaded.FontSize)
	}
	if loaded.TabSize != 4 {
		t.Errorf("expected tab size 4, got %d", loaded.TabSize)
	}
	if loaded.WordWrap != false {
		t.Error("expected wordWrap false")
	}
	if loaded.Version != 1 {
		t.Errorf("expected version 1 after first save, got %d", loaded.Version)
	}
}

// prompt-7 Task F / BUG-M14: settings version CAS.
func TestSettingsService_Save_VersionConflict(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}
	s := defaultSettings()
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	// Advance disk version.
	if err := svc.SaveSettings(loaded); err != nil {
		t.Fatal(err)
	}
	// Stale CAS should fail.
	stale := int64(1)
	loaded.ExpectedVersion = &stale
	loaded.FontSize = 99
	if err := svc.SaveSettings(loaded); err == nil {
		t.Fatal("expected version conflict")
	}
	// Matching version should succeed.
	cur, _ := svc.LoadSettings()
	match := cur.Version
	cur.ExpectedVersion = &match
	cur.FontSize = 20
	if err := svc.SaveSettings(cur); err != nil {
		t.Fatalf("matching CAS: %v", err)
	}
	final, _ := svc.LoadSettings()
	if final.FontSize != 20 || final.Version != match+1 {
		t.Fatalf("font=%d version=%d", final.FontSize, final.Version)
	}
}

func TestSettingsService_SaveSettings_readsExistingFileOnce(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(configPath, []byte(`{
		"version": 7,
		"aiApiKey": "plain:legacy-key",
		"aiProviderConfigs": [{"id":"cfg-a","apiKey":"plain:provider-key"}]
	}`), 0o600); err != nil {
		t.Fatalf("write existing settings: %v", err)
	}

	readCount := 0
	svc := &SettingsService{
		configPath: configPath,
		readFile: func(path string) ([]byte, error) {
			readCount++
			return os.ReadFile(path)
		},
	}
	expectedVersion := int64(7)
	settings := defaultSettings()
	settings.ExpectedVersion = &expectedVersion
	settings.AIApiKeyConfigured = true
	settings.AIProviderConfigs = []AIProviderConfig{{
		ID:               "cfg-a",
		APIKeyConfigured: true,
	}}

	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}
	if readCount != 1 {
		t.Fatalf("SaveSettings read settings file %d times, want 1", readCount)
	}
}

func TestSettingsService_SaveSettings_mergesUnknownFieldsFromSnapshot(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(configPath, []byte(`{
		"version": 4,
		"theme": "stale-theme",
		"accentTheme": "stale-accent",
		"expectedVersion": 999,
		"aiApiKey": "plain:legacy-key",
		"futurePluginSetting": {"enabled":true,"mode":"fast"}
	}`), 0o600); err != nil {
		t.Fatalf("write existing settings: %v", err)
	}

	expectedVersion := int64(4)
	settings := defaultSettings()
	settings.ExpectedVersion = &expectedVersion
	settings.Theme = "light"
	settings.AccentTheme = ""
	settings.AIApiKey = "replacement-key"
	svc := &SettingsService{configPath: configPath}
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read saved settings: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode saved settings: %v", err)
	}
	var futureSetting struct {
		Enabled bool   `json:"enabled"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(raw["futurePluginSetting"], &futureSetting); err != nil {
		t.Fatalf("unknown field changed or was removed: %v", err)
	}
	if !futureSetting.Enabled || futureSetting.Mode != "fast" {
		t.Errorf("unknown field changed: %+v", futureSetting)
	}
	if got := string(raw["theme"]); got != `"light"` {
		t.Errorf("known field did not override snapshot: theme=%s", got)
	}
	if _, ok := raw["accentTheme"]; ok {
		t.Error("omitempty known field retained stale snapshot value")
	}
	if _, ok := raw["expectedVersion"]; ok {
		t.Error("write-intent expectedVersion was persisted")
	}
	if got := string(raw["aiApiKey"]); containsStr(got, "legacy-key") || containsStr(got, "replacement-key") {
		t.Errorf("saved API key contains an old or plaintext secret: %s", got)
	}
}

func TestSettingsService_MigratesLegacySchemaWithoutLosingAIConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	legacy := `{
		"version": 8,
		"aiProvider": "anthropic",
		"aiModel": "claude-test",
		"aiSystemPrompt": "system prompt",
		"aiAgentSystemPrompt": "agent prompt",
		"aiConversationTitlePrompt": "title prompt",
		"aiInlineCompletionPrompt": "inline prompt",
		"aiProviderConfigs": [{
			"id": "preset-1",
			"name": "Saved preset",
			"provider": "anthropic",
			"apiKey": "",
			"baseUrl": "https://example.invalid",
			"model": "claude-test"
		}],
		"activeAIConfigId": "preset-1",
		"toolApprovalConfig": {"run": "never-approve"},
		"futureSetting": {"keep": true}
	}`
	if err := os.WriteFile(configPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := (&SettingsService{configPath: configPath}).LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AIProvider != "anthropic" || loaded.AIModel != "claude-test" {
		t.Fatalf("provider/model lost during migration: %#v", loaded)
	}
	if loaded.AISystemPrompt != "system prompt" || loaded.AIAgentSystemPrompt != "agent prompt" ||
		loaded.AIConversationTitlePrompt != "title prompt" || loaded.AIInlineCompletionPrompt != "inline prompt" {
		t.Fatal("prompt overrides were lost during migration")
	}
	if len(loaded.AIProviderConfigs) != 1 || loaded.ActiveAIConfigID != "preset-1" {
		t.Fatalf("provider preset was lost during migration: %#v", loaded.AIProviderConfigs)
	}
	if loaded.ToolApprovalConfig["run"] != "never-approve" {
		t.Fatalf("tool permission was lost during migration: %#v", loaded.ToolApprovalConfig)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if got := string(raw["schemaVersion"]); got != "1" {
		t.Fatalf("migrated schemaVersion = %s, want 1", got)
	}
}

func TestSettingsService_RejectsSavingOverFutureSchema(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":99,"version":4,"theme":"future"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &SettingsService{configPath: configPath}
	settings := defaultSettings()
	expected := int64(4)
	settings.ExpectedVersion = &expected
	if err := svc.SaveSettings(settings); err == nil || !strings.Contains(err.Error(), "settings schema") {
		t.Fatalf("future schema save error = %v, want settings schema rejection", err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"theme":"future"`) {
		t.Fatalf("future schema file was overwritten: %s", raw)
	}
}

func TestSettingsService_SaveSettingsFailsClosedWhenSnapshotReadFails(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}
	const account = "koyori-ide.extHost.snapshot-read-failure.token"
	const plaintext = "preserved-extension-secret"
	if err := svc.storeExtensionSecret(account, plaintext); err != nil {
		t.Fatalf("StoreSecret failed: %v", err)
	}
	t.Cleanup(func() {
		svc.readFile = nil
		_ = svc.deleteExtensionSecret(account)
	})

	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read settings before failure: %v", err)
	}
	readErr := errors.New("injected settings snapshot read failure")
	svc.readFile = func(string) ([]byte, error) {
		return nil, readErr
	}

	if err := svc.SaveSettings(defaultSettings()); !errors.Is(err, readErr) {
		t.Fatalf("SaveSettings error = %v, want wrapped read error", err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read settings after failure: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("SaveSettings rewrote settings after snapshot read failure")
	}

	svc.readFile = nil
	got, err := svc.getExtensionSecret(account)
	if err != nil {
		t.Fatalf("GetSecret after failed save: %v", err)
	}
	if got != plaintext {
		t.Fatalf("GetSecret after failed save = %q, want preserved value", got)
	}
}

func TestSettingsService_SaveSettingsFailsClosedWhenSnapshotIsInvalidJSON(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	before := []byte(`{"extensionSecrets":`)
	if err := os.WriteFile(configPath, before, 0o600); err != nil {
		t.Fatalf("write invalid settings: %v", err)
	}

	svc := &SettingsService{configPath: configPath}
	if err := svc.SaveSettings(defaultSettings()); err == nil {
		t.Fatal("SaveSettings accepted an invalid settings snapshot")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read invalid settings after failed save: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("SaveSettings overwrote invalid settings instead of failing closed")
	}
}

func TestSettingsService_LoadSettings_corruptFileReturnsDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	// Write corrupt JSON
	svc.configPath = configPath
	writeCorruptSettings(t, configPath)

	settings, err := svc.LoadSettings()
	if err != nil {
		t.Fatalf("should not return error for corrupt file: %v", err)
	}
	if settings.FontSize != 14 {
		t.Errorf("expected defaults from corrupt file, got font size %d", settings.FontSize)
	}
}

func writeCorruptSettings(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{not valid json"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsService_SaveAndLoadAIConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.AIApiKey = "sk-test-key"
	settings.AIBaseURL = "https://api.openai.com"
	settings.AIModel = "gpt-4o"

	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	svc2 := &SettingsService{configPath: configPath}
	loaded, err := svc2.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	// G-SEC-07: LoadSettings must NOT return the decrypted API key to the
	// frontend. The key is cleared and AIApiKeyConfigured signals presence.
	if loaded.AIApiKey != "" {
		t.Errorf("expected AIApiKey to be empty (G-SEC-07), got %q", loaded.AIApiKey)
	}
	if !loaded.AIApiKeyConfigured {
		t.Error("expected AIApiKeyConfigured true, got false")
	}
	if loaded.AIApiKeyStorageMethod != "dpapi" && loaded.AIApiKeyStorageMethod != "aes" && loaded.AIApiKeyStorageMethod != "keyring" {
		t.Errorf("expected AIApiKeyStorageMethod dpapi/aes/keyring, got %q", loaded.AIApiKeyStorageMethod)
	}
	if loaded.AIBaseURL != "https://api.openai.com" {
		t.Errorf("expected AIBaseURL 'https://api.openai.com', got %q", loaded.AIBaseURL)
	}
	if loaded.AIModel != "gpt-4o" {
		t.Errorf("expected AIModel 'gpt-4o', got %q", loaded.AIModel)
	}
}

func TestSettingsService_LoadSettingsRedactsProviderKeysWhenPrimaryDecryptFails(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	const malformedKey = "dpapi:not-valid-base64"
	const providerSecret = "plain:provider-secret"
	data := []byte(`{
  "schemaVersion": 1,
  "aiApiKey": "` + malformedKey + `",
  "aiProviderConfigs": [
    {"id":"cfg-a","apiKey":"` + providerSecret + `"}
  ]
}`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write settings fixture: %v", err)
	}

	loaded, err := (&SettingsService{configPath: configPath}).LoadSettings()
	if err == nil {
		t.Fatal("LoadSettings accepted an undecryptable primary API key")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "dpapi") {
		t.Fatalf("LoadSettings error = %v, want original DPAPI decryption error", err)
	}
	if loaded.AIApiKey != "" {
		t.Fatalf("LoadSettings exposed primary API key marker %q", loaded.AIApiKey)
	}
	if len(loaded.AIProviderConfigs) != 1 {
		t.Fatalf("provider config count = %d, want 1", len(loaded.AIProviderConfigs))
	}
	if loaded.AIProviderConfigs[0].APIKey != "" {
		t.Fatalf("LoadSettings exposed provider API key %q", loaded.AIProviderConfigs[0].APIKey)
	}
	if !loaded.AIProviderConfigs[0].APIKeyConfigured {
		t.Fatal("LoadSettings did not preserve provider key presence metadata")
	}
}

// G-SEC-07: GetDecryptedAPIKey returns the decrypted key for internal
// backend use (not exposed to the frontend via LoadSettings).
func TestSettingsService_GetDecryptedAPIKey(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.AIApiKey = "sk-internal-key"
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	svc2 := &SettingsService{configPath: configPath}
	got, err := svc2.getDecryptedAPIKey()
	if err != nil {
		t.Fatalf("GetDecryptedAPIKey failed: %v", err)
	}
	if got != "sk-internal-key" {
		t.Errorf("GetDecryptedAPIKey = %q, want %q", got, "sk-internal-key")
	}
}

// G-SEC-07: GetDecryptedAPIKey returns empty when no key is stored.
func TestSettingsService_GetDecryptedAPIKey_emptyWhenNoKey(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	if err := svc.SaveSettings(defaultSettings()); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	svc2 := &SettingsService{configPath: configPath}
	got, err := svc2.getDecryptedAPIKey()
	if err != nil {
		t.Fatalf("GetDecryptedAPIKey failed: %v", err)
	}
	if got != "" {
		t.Errorf("GetDecryptedAPIKey = %q, want empty", got)
	}
}

// G-SEC-07: an unrelated save (with empty AIApiKey but AIApiKeyConfigured
// true) must NOT wipe the existing on-disk key. The frontend no longer holds
// the plaintext key, so it saves with empty + configured=true; the backend
// preserves the stored key.
func TestSettingsService_SaveSettings_preservesKeyWhenEmptyButConfigured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	// Save a key initially.
	settings := defaultSettings()
	settings.AIApiKey = "sk-preserve-me"
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	// Simulate the frontend saving an unrelated change: AIApiKey empty (not
	// loaded, G-SEC-07) but AIApiKeyConfigured true.
	again := defaultSettings()
	again.AIApiKey = ""
	again.AIApiKeyConfigured = true
	again.FontSize = 20
	if err := svc.SaveSettings(again); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	// The on-disk key must still be present (decryptable).
	svc2 := &SettingsService{configPath: configPath}
	got, err := svc2.getDecryptedAPIKey()
	if err != nil {
		t.Fatalf("GetDecryptedAPIKey failed: %v", err)
	}
	if got != "sk-preserve-me" {
		t.Errorf("key was wiped by unrelated save: got %q, want %q", got, "sk-preserve-me")
	}
}

// G-SEC-07: a genuine clear (AIApiKey empty AND AIApiKeyConfigured false)
// must wipe the key, even if a key was previously stored.
func TestSettingsService_SaveSettings_clearsKeyWhenEmptyAndNotConfigured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.AIApiKey = "sk-clear-me"
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	again := defaultSettings()
	again.AIApiKey = ""
	again.AIApiKeyConfigured = false
	if err := svc.SaveSettings(again); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	svc2 := &SettingsService{configPath: configPath}
	got, _ := svc2.getDecryptedAPIKey()
	if got != "" {
		t.Errorf("key was not cleared: got %q, want empty", got)
	}
}

func TestSettingsService_SaveAndLoadAISystemPrompt(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.AISystemPrompt = "You are a Rust expert."

	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	svc2 := &SettingsService{configPath: configPath}
	loaded, err := svc2.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if loaded.AISystemPrompt != "You are a Rust expert." {
		t.Errorf("expected AISystemPrompt 'You are a Rust expert.', got %q", loaded.AISystemPrompt)
	}
}

func TestSettingsService_DefaultInlineCompletionEnabled(t *testing.T) {
	svc := &SettingsService{configPath: filepath.Join(t.TempDir(), "settings.json")}
	settings, err := svc.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if !settings.InlineCompletionEnabled {
		t.Error("expected default InlineCompletionEnabled true")
	}
}

func TestSettingsService_DefaultEmmetEnabled(t *testing.T) {
	svc := &SettingsService{configPath: filepath.Join(t.TempDir(), "settings.json")}
	settings, err := svc.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if !settings.EmmetEnabled {
		t.Error("expected default EmmetEnabled true")
	}
}

// N-76: concurrent SetConfigPath + SaveSettings must not race on configPath.
// Run with `go test -race` to detect data races. Before the fix, this test
// would trigger the race detector; after the fix, the pathMu protects all
// access to configPath.
func TestSettingsService_N76_ConcurrentSetConfigPathAndSave_NoRace(t *testing.T) {
	svc := &SettingsService{configPath: filepath.Join(t.TempDir(), "profile1.json")}

	settings := defaultSettings()
	settings.Theme = "dark"

	var wg sync.WaitGroup
	// Writer: rapidly save settings.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = svc.SaveSettings(settings)
		}
	}()
	// Switcher: rapidly swap configPath between two profiles.
	wg.Add(1)
	go func() {
		defer wg.Done()
		path1 := filepath.Join(t.TempDir(), "profile1.json")
		path2 := filepath.Join(t.TempDir(), "profile2.json")
		for i := 0; i < 100; i++ {
			if i%2 == 0 {
				svc.setConfigPath(path1)
			} else {
				svc.setConfigPath(path2)
			}
		}
	}()
	// Reader: rapidly load settings.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, _ = svc.LoadSettings()
		}
	}()
	wg.Wait()
}

// N-76: SetConfigPath acquires the write lock; SaveSettings holds the read
// lock for the full write, so a save that started before SetConfigPath
// completes with the OLD path. This test verifies the save goes to the
// original path, not the new one.
func TestSettingsService_N76_SaveCompletesOnOldPathAfterSwitch(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.json")
	newPath := filepath.Join(dir, "new.json")
	svc := &SettingsService{configPath: oldPath}

	// Save to old path.
	settings := defaultSettings()
	settings.Theme = "dark"
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}
	// Switch path.
	svc.setConfigPath(newPath)
	// Save again — should go to new path, not old.
	settings.Theme = "light"
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}
	// Old path should still have "dark".
	oldData, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("read old path: %v", err)
	}
	if !containsStr(string(oldData), "dark") {
		t.Errorf("old path should contain 'dark', got: %s", oldData)
	}
	// New path should have "light".
	newData, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("read new path: %v", err)
	}
	if !containsStr(string(newData), "light") {
		t.Errorf("new path should contain 'light', got: %s", newData)
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSettingsService_SaveAndLoadInlineCompletionEnabled(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.InlineCompletionEnabled = false

	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	svc2 := &SettingsService{configPath: configPath}
	loaded, err := svc2.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if loaded.InlineCompletionEnabled != false {
		t.Errorf("expected InlineCompletionEnabled false, got %v", loaded.InlineCompletionEnabled)
	}
}

func TestSettingsService_SaveAndLoadEmmetSettings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.EmmetEnabled = false
	settings.EmmetIncludeLanguages = map[string]string{"templ": "html"}
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	loaded, err := (&SettingsService{configPath: configPath}).LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if loaded.EmmetEnabled {
		t.Error("expected EmmetEnabled false")
	}
	if loaded.EmmetIncludeLanguages["templ"] != "html" {
		t.Fatalf("expected includeLanguages to round-trip, got %#v", loaded.EmmetIncludeLanguages)
	}
}

// CRIT-01: multi-provider config keys must be encrypted at rest, cleared in
// LoadSettings (only APIKeyConfigured exposed), and decryptable via
// GetAPIKeyForConfig so the frontend never holds plaintext keys.
func TestSettingsService_CRIT01_ProviderKeyIsolation(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.AIProviderConfigs = []AIProviderConfig{
		{ID: "cfg-a", Name: "A", Provider: "openai", APIKey: "sk-provider-a", BaseURL: "https://api.openai.com", Model: "gpt-4o"},
		{ID: "cfg-b", Name: "B", Provider: "anthropic", Protocol: "anthropic", APIKey: "sk-provider-b", BaseURL: "https://api.anthropic.com", Model: "claude-3"},
	}
	settings.ActiveAIConfigID = "cfg-a"
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	// Disk must NOT contain plaintext provider keys.
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	rawStr := string(raw)
	if containsStr(rawStr, "sk-provider-a") || containsStr(rawStr, "sk-provider-b") {
		t.Errorf("on-disk settings contain plaintext provider key (CRIT-01): %s", rawStr)
	}

	// LoadSettings must clear each config's APIKey + set APIKeyConfigured.
	svc2 := &SettingsService{configPath: configPath}
	loaded, err := svc2.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if len(loaded.AIProviderConfigs) != 2 {
		t.Fatalf("expected 2 provider configs, got %d", len(loaded.AIProviderConfigs))
	}
	for i, cfg := range loaded.AIProviderConfigs {
		if cfg.APIKey != "" {
			t.Errorf("config[%d] APIKey not cleared (CRIT-01): %q", i, cfg.APIKey)
		}
		if !cfg.APIKeyConfigured {
			t.Errorf("config[%d] APIKeyConfigured false, want true (CRIT-01)", i)
		}
	}

	// GetAPIKeyForConfig must decrypt the stored key for each config.
	for _, want := range []struct{ id, key string }{
		{"cfg-a", "sk-provider-a"},
		{"cfg-b", "sk-provider-b"},
	} {
		got, err := svc2.getAPIKeyForConfig(want.id)
		if err != nil {
			t.Fatalf("GetAPIKeyForConfig(%s): %v", want.id, err)
		}
		if got != want.key {
			t.Errorf("GetAPIKeyForConfig(%s) = %q, want %q", want.id, got, want.key)
		}
	}
}

// CRIT-01: an unrelated save (empty provider key + configured=true) must
// preserve the existing on-disk provider key, INDEPENDENT of the legacy
// AIApiKey state. This verifies the provider-key preservation scope fix:
// previously the preservation was nested inside the legacy-key if-block, so
// when the legacy key was non-empty the provider keys were wiped.
func TestSettingsService_CRIT01_PreservesProviderKeyIndependentOfLegacyKey(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.AIApiKey = "sk-legacy"
	settings.AIProviderConfigs = []AIProviderConfig{
		{ID: "cfg-a", Name: "A", Provider: "openai", APIKey: "sk-provider-a", BaseURL: "https://api.openai.com", Model: "gpt-4o"},
	}
	settings.ActiveAIConfigID = "cfg-a"
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	// Frontend save with a NON-empty legacy key AND empty provider key +
	// configured=true (preserve provider key). The provider key must be
	// preserved regardless of the legacy key state.
	again := defaultSettings()
	again.AIApiKey = "sk-legacy-new"
	again.AIApiKeyConfigured = true
	again.AIProviderConfigs = []AIProviderConfig{
		{ID: "cfg-a", Name: "A", Provider: "openai", APIKey: "", APIKeyConfigured: true, BaseURL: "https://api.openai.com", Model: "gpt-4o"},
	}
	again.ActiveAIConfigID = "cfg-a"
	if err := svc.SaveSettings(again); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	svc2 := &SettingsService{configPath: configPath}
	got, err := svc2.getAPIKeyForConfig("cfg-a")
	if err != nil {
		t.Fatalf("GetAPIKeyForConfig: %v", err)
	}
	if got != "sk-provider-a" {
		t.Errorf("provider key wiped when legacy key non-empty (scope bug): got %q, want %q", got, "sk-provider-a")
	}
}

func TestSettingsService_SaveSettingsPreservesUndecryptableKeys(t *testing.T) {
	const malformedKey = "dpapi:not-valid-base64"

	t.Run("primary API key", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "settings.json")
		before := []byte(`{
  "schemaVersion": 1,
  "version": 7,
  "theme": "dark",
  "aiApiKey": "` + malformedKey + `"
}`)
		if err := os.WriteFile(configPath, before, 0o600); err != nil {
			t.Fatalf("write settings fixture: %v", err)
		}

		settings := defaultSettings()
		settings.Theme = "light"
		settings.AIApiKeyConfigured = true
		err := (&SettingsService{configPath: configPath}).SaveSettings(settings)
		if err == nil {
			t.Fatal("SaveSettings accepted an undecryptable primary API key")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "dpapi") {
			t.Fatalf("SaveSettings error = %v, want DPAPI decryption error", err)
		}
		after, readErr := os.ReadFile(configPath)
		if readErr != nil {
			t.Fatalf("read settings after rejected save: %v", readErr)
		}
		if !bytes.Equal(after, before) {
			t.Fatal("SaveSettings modified settings after primary key decryption failure")
		}
	})

	t.Run("provider API key", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "settings.json")
		before := []byte(`{
  "schemaVersion": 1,
  "version": 9,
  "theme": "dark",
  "aiProviderConfigs": [
    {"id":"cfg-a","name":"A","apiKey":"` + malformedKey + `"}
  ]
}`)
		if err := os.WriteFile(configPath, before, 0o600); err != nil {
			t.Fatalf("write settings fixture: %v", err)
		}

		settings := defaultSettings()
		settings.Theme = "light"
		settings.AIProviderConfigs = []AIProviderConfig{{
			ID:               "cfg-a",
			Name:             "A",
			APIKeyConfigured: true,
		}}
		err := (&SettingsService{configPath: configPath}).SaveSettings(settings)
		if err == nil {
			t.Fatal("SaveSettings accepted an undecryptable provider API key")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "dpapi") {
			t.Fatalf("SaveSettings error = %v, want DPAPI decryption error", err)
		}
		after, readErr := os.ReadFile(configPath)
		if readErr != nil {
			t.Fatalf("read settings after rejected save: %v", readErr)
		}
		if !bytes.Equal(after, before) {
			t.Fatal("SaveSettings modified settings after provider key decryption failure")
		}
	})
}

func TestSettingsService_SecretBridgeEncryptedPersistence(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}
	const account = "koyori-ide.extHost.example.token"
	const plaintext = "extension-secret-value"
	t.Cleanup(func() {
		svc.writeJSON = nil
		_ = svc.deleteExtensionSecret(account)
	})

	if err := svc.storeExtensionSecret(account, plaintext); err != nil {
		t.Fatalf("StoreSecret failed: %v", err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read secret store: %v", err)
	}
	if strings.Contains(string(raw), plaintext) {
		t.Fatal("secret store contains plaintext secret")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode secret store: %v", err)
	}
	var stored map[string]string
	if err := json.Unmarshal(document[extensionSecretsSettingsKey], &stored); err != nil {
		t.Fatalf("decode encrypted secret map: %v", err)
	}
	if !IsSecretEncrypted(stored[account]) {
		t.Fatalf("stored value is not encrypted: method=%q", SecretMethod(stored[account]))
	}

	// A fresh service instance must recover the value from disk/keyring.
	reloaded := &SettingsService{configPath: configPath}
	got, err := reloaded.getExtensionSecret(account)
	if err != nil {
		t.Fatalf("GetSecret failed: %v", err)
	}
	if got != plaintext {
		t.Fatalf("GetSecret = %q, want stored value", got)
	}

	loaded, err := reloaded.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	exposed, err := json.Marshal(loaded)
	if err != nil {
		t.Fatalf("marshal loaded settings: %v", err)
	}
	if strings.Contains(string(exposed), plaintext) || strings.Contains(string(exposed), account) {
		t.Fatal("LoadSettings exposed extension secret storage")
	}

	// An unrelated settings save must preserve the non-exported secret map.
	settings := defaultSettings()
	settings.Theme = "light"
	if err := reloaded.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}
	got, err = reloaded.getExtensionSecret(account)
	if err != nil {
		t.Fatalf("GetSecret after SaveSettings failed: %v", err)
	}
	if got != plaintext {
		t.Fatalf("GetSecret after SaveSettings = %q, want stored value", got)
	}

	if err := reloaded.deleteExtensionSecret(account); err != nil {
		t.Fatalf("DeleteSecret failed: %v", err)
	}
	got, err = reloaded.getExtensionSecret(account)
	if err != nil {
		t.Fatalf("GetSecret after deletion failed: %v", err)
	}
	if got != "" {
		t.Fatalf("GetSecret after deletion = %q, want empty", got)
	}
}

func TestSettingsService_RawSecretMethodsUnavailableToWails(t *testing.T) {
	serviceType := reflect.TypeOf((*SettingsService)(nil))
	for _, method := range []string{"GetSecret", "StoreSecret", "DeleteSecret"} {
		if _, exposed := serviceType.MethodByName(method); exposed {
			t.Fatalf("SettingsService.%s remains exported to Wails reflection", method)
		}
	}
}

func TestSettingsService_SecretBridgeValidatesInputs(t *testing.T) {
	svc := &SettingsService{configPath: filepath.Join(t.TempDir(), "settings.json")}
	const secret = "must-not-appear-in-errors"

	if _, err := svc.getExtensionSecret(" \t\r\n"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("GetSecret blank account error = %v, want ErrInvalidInput", err)
	}
	err := svc.storeExtensionSecret("", secret)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("StoreSecret blank account error = %v, want ErrInvalidInput", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("StoreSecret error exposed the secret value")
	}
	if err := svc.storeExtensionSecret(strings.Repeat("a", maxSettingsSecretAccountSize+1), secret); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("StoreSecret oversized account error = %v, want ErrInvalidInput", err)
	}
	oversized := strings.Repeat("v", maxSecretPlaintextSize+1)
	err = svc.storeExtensionSecret("koyori-ide.extHost.account", oversized)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("StoreSecret oversized value error = %v, want ErrInvalidInput", err)
	}
	if strings.Contains(err.Error(), oversized) {
		t.Fatal("oversized-value error exposed the secret value")
	}

	if _, err := svc.getExtensionSecret(keyringAccount); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("getExtensionSecret application account error = %v, want ErrInvalidInput", err)
	}
	if err := svc.storeExtensionSecret(keyringAccount, secret); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("storeExtensionSecret application account error = %v, want ErrInvalidInput", err)
	}
	if err := svc.deleteExtensionSecret(keyringAccount); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("deleteExtensionSecret application account error = %v, want ErrInvalidInput", err)
	}
}

func TestSettingsService_SecretBridgePropagatesReadErrorsWithoutLeakingValue(t *testing.T) {
	readErr := errors.New("injected secret-store read failure")
	svc := &SettingsService{
		configPath: filepath.Join(t.TempDir(), "settings.json"),
		readFile: func(string) ([]byte, error) {
			return nil, readErr
		},
	}
	const secret = "sensitive-extension-secret"

	err := svc.storeExtensionSecret("koyori-ide.extHost.account", secret)
	if !errors.Is(err, readErr) {
		t.Fatalf("StoreSecret error = %v, want wrapped read error", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("StoreSecret error exposed the secret value")
	}
	if _, err := svc.getExtensionSecret("koyori-ide.extHost.account"); !errors.Is(err, readErr) {
		t.Fatalf("GetSecret error = %v, want wrapped read error", err)
	}
}

func TestSettingsService_SecretBridgeRejectsUnencryptedStoredValue(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	const plaintext = "manually-injected-plaintext"
	const account = "koyori-ide.extHost.account"
	if err := os.WriteFile(configPath, []byte(`{"extensionSecrets":{"`+account+`":"`+plaintext+`"}}`), 0o600); err != nil {
		t.Fatalf("write malformed secret store: %v", err)
	}

	_, err := (&SettingsService{configPath: configPath}).getExtensionSecret(account)
	if err == nil {
		t.Fatal("GetSecret accepted an unencrypted stored value")
	}
	if strings.Contains(err.Error(), plaintext) {
		t.Fatal("GetSecret error exposed the stored plaintext")
	}
}

func TestSettingsService_SecretBridgePersistenceFailurePreservesOldValue(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}
	const account = "koyori-ide.extHost.rollback.token"
	const oldValue = "old-secret-value"
	const newValue = "new-secret-value"
	t.Cleanup(func() {
		svc.writeJSON = nil
		_ = svc.deleteExtensionSecret(account)
	})

	if err := svc.storeExtensionSecret(account, oldValue); err != nil {
		t.Fatalf("store initial secret: %v", err)
	}
	writeErr := errors.New("injected atomic write failure")
	svc.writeJSON = func(string, interface{}, os.FileMode) error {
		return writeErr
	}

	err := svc.storeExtensionSecret(account, newValue)
	if !errors.Is(err, writeErr) {
		t.Fatalf("StoreSecret error = %v, want wrapped write error", err)
	}
	if strings.Contains(err.Error(), newValue) {
		t.Fatal("persistence error exposed the new secret value")
	}
	got, err := svc.getExtensionSecret(account)
	if err != nil {
		t.Fatalf("GetSecret after failed update: %v", err)
	}
	if got != oldValue {
		t.Fatalf("failed update published %q, want old value", got)
	}

	err = svc.deleteExtensionSecret(account)
	if !errors.Is(err, writeErr) {
		t.Fatalf("DeleteSecret error = %v, want wrapped write error", err)
	}
	got, err = svc.getExtensionSecret(account)
	if err != nil {
		t.Fatalf("GetSecret after failed deletion: %v", err)
	}
	if got != oldValue {
		t.Fatalf("failed deletion changed value to %q, want old value", got)
	}

	svc.writeJSON = nil
	if err := svc.storeExtensionSecret(account, ""); err != nil {
		t.Fatalf("StoreSecret empty value deletion failed: %v", err)
	}
	got, err = svc.getExtensionSecret(account)
	if err != nil {
		t.Fatalf("GetSecret after empty-value deletion: %v", err)
	}
	if got != "" {
		t.Fatalf("GetSecret after empty-value deletion = %q, want empty", got)
	}
}

func TestSettingsService_SecretBridgeConcurrentStoresPreserveAccounts(t *testing.T) {
	// Force the deterministic AES fallback on Unix so this test never waits on
	// or mutates a desktop keyring. Windows uses DPAPI without consulting PATH.
	t.Setenv("PATH", "")
	svc := &SettingsService{configPath: filepath.Join(t.TempDir(), "settings.json")}
	cases := []struct {
		account string
		value   string
	}{
		{"koyori-ide.extHost.concurrent.alpha", "alpha-secret"},
		{"koyori-ide.extHost.concurrent.beta", "beta-secret"},
		{"koyori-ide.extHost.concurrent.gamma", "gamma-secret"},
		{"koyori-ide.extHost.concurrent.delta", "delta-secret"},
	}
	t.Cleanup(func() {
		for _, tc := range cases {
			_ = svc.deleteExtensionSecret(tc.account)
		}
	})

	start := make(chan struct{})
	errs := make(chan error, len(cases))
	var wg sync.WaitGroup
	for _, tc := range cases {
		tc := tc
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.storeExtensionSecret(tc.account, tc.value)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent StoreSecret failed: %v", err)
		}
	}

	for _, tc := range cases {
		got, err := svc.getExtensionSecret(tc.account)
		if err != nil {
			t.Fatalf("GetSecret(%q): %v", tc.account, err)
		}
		if got != tc.value {
			t.Fatalf("GetSecret(%q) = %q, want %q", tc.account, got, tc.value)
		}
	}
}

// P9-G11: two windows editing different fields must not lose either change.
// Window A commits field X (version bumps). Window B's stale read-modify-write
// hits CAS, reloads the latest (containing A's X), reapplies its own field,
// and commits with the fresh version. The disk then holds both changes.
func TestSettingsService_TwoWindowsDifferentFieldsPreserved(t *testing.T) {
	dir := t.TempDir()
	svc := NewSettingsServiceWithPath(filepath.Join(dir, "settings.json"))

	initial := Settings{Theme: "light", FontSize: 12}
	initial.ExpectedVersion = nil
	if err := svc.SaveSettings(initial); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	loadedA, err := svc.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	loadedB, err := svc.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}

	// Window A changes Theme with the current version.
	verA := loadedA.Version
	loadedA.ExpectedVersion = &verA
	loadedA.Theme = "dark"
	if err := svc.SaveSettings(loadedA); err != nil {
		t.Fatalf("window A save: %v", err)
	}

	// Window B holds the stale version and changes FontSize: CAS must reject.
	stale := loadedB.Version
	loadedB.ExpectedVersion = &stale
	loadedB.FontSize = 16
	if err := svc.SaveSettings(loadedB); err == nil {
		t.Fatal("stale window B save unexpectedly succeeded")
	} else if !strings.Contains(err.Error(), "version conflict") {
		t.Fatalf("expected version conflict, got %v", err)
	}

	// B reloads the latest (theme=dark) and reapplies its own change.
	reloaded, err := svc.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Theme != "dark" {
		t.Fatalf("reload lost window A change: theme=%q", reloaded.Theme)
	}
	cur := reloaded.Version
	reloaded.ExpectedVersion = &cur
	reloaded.FontSize = 16
	if err := svc.SaveSettings(reloaded); err != nil {
		t.Fatalf("window B retry save: %v", err)
	}

	final, err := svc.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if final.Theme != "dark" || final.FontSize != 16 {
		t.Fatalf("both windows' changes not preserved: %+v", final)
	}
}

// P9-G11: two windows editing the same field produce a deterministic conflict
// that is surfaced (ErrSettingsConflict) rather than silently overwritten.
func TestSettingsService_TwoWindowsSameFieldConflictVisible(t *testing.T) {
	dir := t.TempDir()
	svc := NewSettingsServiceWithPath(filepath.Join(dir, "settings.json"))
	if err := svc.SaveSettings(Settings{Theme: "light"}); err != nil {
		t.Fatal(err)
	}
	a, err := svc.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	verA := a.Version
	a.ExpectedVersion = &verA
	a.Theme = "dark"
	if err := svc.SaveSettings(a); err != nil {
		t.Fatalf("window A: %v", err)
	}
	stale := b.Version
	b.ExpectedVersion = &stale
	b.Theme = "blue"
	err = svc.SaveSettings(b)
	if err == nil || !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("same-field stale save = %v, want ErrSettingsConflict", err)
	}
	latest, _ := svc.LoadSettings()
	if latest.Theme != "dark" {
		t.Fatalf("conflict silently overwrote window A: theme=%q", latest.Theme)
	}
}

// P9-G11: an out-of-order response carrying an old expected version must never
// overwrite a newer on-disk version.
func TestSettingsService_StaleResponseDoesNotOverwriteNewer(t *testing.T) {
	dir := t.TempDir()
	svc := NewSettingsServiceWithPath(filepath.Join(dir, "settings.json"))
	if err := svc.SaveSettings(Settings{Theme: "light"}); err != nil {
		t.Fatal(err)
	}
	// advance disk to version 2 via one save
	first, _ := svc.LoadSettings()
	v1 := first.Version
	first.ExpectedVersion = &v1
	first.Theme = "dark"
	if err := svc.SaveSettings(first); err != nil {
		t.Fatal(err)
	}
	// a stale response claims version 1 and changes FontSize
	stale := int64(1)
	staleSave := Settings{Theme: "ignored", FontSize: 99}
	staleSave.ExpectedVersion = &stale
	if err := svc.SaveSettings(staleSave); err == nil || !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale response = %v, want ErrSettingsConflict", err)
	}
	latest, _ := svc.LoadSettings()
	if latest.FontSize == 99 || latest.Theme != "dark" {
		t.Fatalf("stale response overwrote newer settings: %+v", latest)
	}
}
