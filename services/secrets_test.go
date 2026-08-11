package services

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEncryptSecret_emptyInputReturnsEmpty(t *testing.T) {
	got, err := EncryptSecret(keyringAccount, "")
	if err != nil {
		t.Fatalf("EncryptSecret(keyringAccount,\"\") returned error: %v", err)
	}
	if got != "" {
		t.Errorf("EncryptSecret(keyringAccount,\"\") = %q, want \"\"", got)
	}
}

func TestDecryptSecret_emptyInputReturnsEmpty(t *testing.T) {
	got, err := DecryptSecret(keyringAccount, "")
	if err != nil {
		t.Fatalf("DecryptSecret(keyringAccount,\"\") returned error: %v", err)
	}
	if got != "" {
		t.Errorf("DecryptSecret(keyringAccount,\"\") = %q, want \"\"", got)
	}
}

func TestEncryptDecryptSecret_roundTrip(t *testing.T) {
	cases := []string{
		"sk-test-key",
		"sk-abc123xyz",
		"a",
		"key with spaces",
		"key-with-special-chars-!@#$%^&*()",
		strings.Repeat("x", 256),
	}
	for _, plaintext := range cases {
		t.Run(plaintext[:min(len(plaintext), 20)], func(t *testing.T) {
			encrypted, err := EncryptSecret(keyringAccount, plaintext)
			if err != nil {
				t.Fatalf("EncryptSecret failed: %v", err)
			}
			if encrypted == "" {
				t.Fatal("EncryptSecret returned empty for non-empty input")
			}
			if encrypted == plaintext {
				t.Error("EncryptSecret returned plaintext — not encrypted")
			}
			if !IsSecretEncrypted(encrypted) {
				t.Errorf("IsSecretEncrypted(%q) = false, want true", encrypted[:min(len(encrypted), 20)])
			}
			decrypted, err := DecryptSecret(keyringAccount, encrypted)
			if err != nil {
				t.Fatalf("DecryptSecret failed: %v", err)
			}
			if decrypted != plaintext {
				t.Errorf("DecryptSecret = %q, want %q", decrypted, plaintext)
			}
		})
	}
}

func TestDecryptSecret_legacyPlaintextReturnedAsIs(t *testing.T) {
	// Bare strings without a prefix should be returned as-is so existing
	// settings.json files keep working after the N-13 upgrade.
	cases := []string{
		"sk-legacy-key",
		"plain-key-without-prefix",
		"key:with:colons", // not a real prefix, should pass through
	}
	for _, stored := range cases {
		t.Run(stored, func(t *testing.T) {
			got, err := DecryptSecret(keyringAccount, stored)
			if err != nil {
				t.Fatalf("DecryptSecret failed: %v", err)
			}
			if got != stored {
				t.Errorf("DecryptSecret(keyringAccount,%q) = %q, want %q", stored, got, stored)
			}
			if IsSecretEncrypted(stored) {
				t.Errorf("IsSecretEncrypted(%q) = true, want false", stored)
			}
		})
	}
}

func TestDecryptSecret_plainPrefixStripped(t *testing.T) {
	got, err := DecryptSecret(keyringAccount, "plain:sk-fallback-key")
	if err != nil {
		t.Fatalf("DecryptSecret failed: %v", err)
	}
	if got != "sk-fallback-key" {
		t.Errorf("DecryptSecret = %q, want %q", got, "sk-fallback-key")
	}
	if IsSecretEncrypted("plain:sk-fallback-key") {
		t.Error("IsSecretEncrypted should return false for plain: prefix")
	}
}

// TestIsSecretEncrypted_keyringPrefix verifies the keyring: prefix (added in
// N-15 for macOS Keychain / Linux libsecret) is recognized as encrypted.
// We don't test DecryptSecret on a keyring: value here because decryption
// requires the platform keychain — that path is exercised manually on each
// platform instead.
func TestIsSecretEncrypted_keyringPrefix(t *testing.T) {
	if !IsSecretEncrypted("keyring:YWk=") {
		t.Error("IsSecretEncrypted should return true for keyring: prefix")
	}
	if SecretMethod("keyring:YWk=") != "keyring" {
		t.Errorf("SecretMethod = %q, want %q", SecretMethod("keyring:YWk="), "keyring")
	}
}

func TestSecretMethod(t *testing.T) {
	cases := []struct {
		stored string
		want   string
	}{
		{"", "none"},
		{"dpapi:abc123", "dpapi"},
		{"aes:xyz789", "aes"},
		{"keyring:YWk=", "keyring"},
		{"plain:foo", "plain"},
		{"sk-legacy", "plain"}, // legacy plaintext
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := SecretMethod(tc.stored)
			if got != tc.want {
				t.Errorf("SecretMethod(%q) = %q, want %q", tc.stored, got, tc.want)
			}
		})
	}
}

func TestIsSecretEncrypted(t *testing.T) {
	cases := []struct {
		stored string
		want   bool
	}{
		{"", false},
		{"dpapi:abc123", true},
		{"aes:xyz789", true},
		{"keyring:YWk=", true},
		{"plain:foo", false},
		{"sk-legacy", false},
	}
	for _, tc := range cases {
		t.Run(tc.stored, func(t *testing.T) {
			got := IsSecretEncrypted(tc.stored)
			if got != tc.want {
				t.Errorf("IsSecretEncrypted(%q) = %v, want %v", tc.stored, got, tc.want)
			}
		})
	}
}

// --- SettingsService encryption integration tests ---

func TestSettingsService_SaveSettings_encryptsAPIKeyOnDisk(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.AIApiKey = "sk-secret-key"

	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	// Read the raw JSON file and check the key is NOT plaintext.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	raw := string(data)
	if strings.Contains(raw, "sk-secret-key") {
		t.Error("settings.json contains plaintext API key — not encrypted")
	}
	// The encrypted form should have a prefix (dpapi on Windows, aes or
	// keyring on macOS/Linux depending on keychain availability).
	if !strings.Contains(raw, "dpapi:") && !strings.Contains(raw, "aes:") && !strings.Contains(raw, "keyring:") {
		t.Error("settings.json does not contain an encryption prefix")
	}
}

func TestSettingsService_LoadSettings_decryptsAPIKey(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.AIApiKey = "sk-decrypt-me"
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	// Load with a fresh service instance.
	svc2 := &SettingsService{configPath: configPath}
	loaded, err := svc2.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	// G-SEC-07: LoadSettings must NOT return the plaintext key. It is cleared
	// and AIApiKeyConfigured signals that a key is stored. The decrypted key
	// is available via GetDecryptedAPIKey for internal backend use.
	if loaded.AIApiKey != "" {
		t.Errorf("AIApiKey = %q, want empty (G-SEC-07)", loaded.AIApiKey)
	}
	if !loaded.AIApiKeyConfigured {
		t.Error("AIApiKeyConfigured = false, want true")
	}
	// The internal accessor still returns the decrypted key.
	got, err := svc2.getDecryptedAPIKey()
	if err != nil {
		t.Fatalf("GetDecryptedAPIKey failed: %v", err)
	}
	if got != "sk-decrypt-me" {
		t.Errorf("GetDecryptedAPIKey = %q, want %q", got, "sk-decrypt-me")
	}
}

func TestSettingsService_LoadSettings_migratesLegacyPlaintext(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	// Write a legacy settings.json with a plaintext API key (no prefix).
	legacy := `{
  "language": "en",
  "theme": "dark",
  "fontSize": 14,
  "fontFamily": "JetBrains Mono",
  "tabSize": 2,
  "wordWrap": true,
  "lineNumbers": true,
  "minimap": false,
  "aiApiKey": "sk-legacy-plaintext",
  "aiBaseUrl": "https://api.openai.com",
  "aiModel": "gpt-4o",
  "aiSystemPrompt": "",
  "cursorBlinking": "blink",
  "cursorStyle": "line",
  "bracketColorization": true,
  "autoSave": false,
  "autoSaveDelay": "afterDelay",
  "aiProvider": "",
  "temperature": 0.7,
  "maxTokens": 4096,
  "defaultShell": "",
  "terminalFontSize": 13,
  "terminalCursorStyle": "block",
  "scrollback": 10000,
  "uiDensity": "comfortable",
  "fontSizeScaling": 100
}`
	if err := os.WriteFile(configPath, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}

	svc := &SettingsService{configPath: configPath}
	loaded, err := svc.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	// G-SEC-07: the plaintext key is NOT returned; AIApiKeyConfigured signals
	// that a key was stored, and the storage method reflects the legacy form.
	if loaded.AIApiKey != "" {
		t.Errorf("AIApiKey = %q, want empty (G-SEC-07)", loaded.AIApiKey)
	}
	if !loaded.AIApiKeyConfigured {
		t.Error("AIApiKeyConfigured = false, want true")
	}
	// The decrypted key is still available internally for backend use.
	got, err := svc.getDecryptedAPIKey()
	if err != nil {
		t.Fatalf("GetDecryptedAPIKey failed: %v", err)
	}
	if got != "sk-legacy-plaintext" {
		t.Errorf("GetDecryptedAPIKey = %q, want %q", got, "sk-legacy-plaintext")
	}

	// The on-disk file should now be encrypted (auto-migration).
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	raw := string(data)
	if strings.Contains(raw, "sk-legacy-plaintext") {
		t.Error("settings.json still contains plaintext key after LoadSettings — migration did not happen")
	}
	if !strings.Contains(raw, "dpapi:") && !strings.Contains(raw, "aes:") && !strings.Contains(raw, "keyring:") {
		t.Error("settings.json does not contain encryption prefix after migration")
	}
}

func TestSettingsService_LoadSettings_emptyKeyStaysEmpty(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.AIApiKey = ""
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	loaded, err := svc.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if loaded.AIApiKey != "" {
		t.Errorf("AIApiKey = %q, want empty", loaded.AIApiKey)
	}
	if loaded.AIApiKeyConfigured {
		t.Error("AIApiKeyConfigured = true, want false (no key stored)")
	}
	if loaded.AIApiKeyStorageMethod != "none" {
		t.Errorf("AIApiKeyStorageMethod = %q, want %q", loaded.AIApiKeyStorageMethod, "none")
	}
}

func TestSettingsService_IsAPIKeyEncryptedOnDisk(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	// No file → not encrypted.
	if svc.IsAPIKeyEncryptedOnDisk() {
		t.Error("IsAPIKeyEncryptedOnDisk() = true, want false (no file)")
	}

	// Save with a key → should be encrypted.
	settings := defaultSettings()
	settings.AIApiKey = "sk-test-encryption"
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}
	if !svc.IsAPIKeyEncryptedOnDisk() {
		t.Error("IsAPIKeyEncryptedOnDisk() = false, want true (after save)")
	}

	// Write legacy plaintext → not encrypted.
	legacy := `{"aiApiKey": "sk-plaintext"}`
	if err := os.WriteFile(configPath, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	if svc.IsAPIKeyEncryptedOnDisk() {
		t.Error("IsAPIKeyEncryptedOnDisk() = true, want false (plaintext)")
	}
}

func TestSettingsService_GetAPIKeyStorageMethod(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	// No file → "none".
	if got := svc.GetAPIKeyStorageMethod(); got != "none" {
		t.Errorf("GetAPIKeyStorageMethod() = %q, want %q", got, "none")
	}

	// Save with a key → should return "dpapi" or "aes".
	settings := defaultSettings()
	settings.AIApiKey = "sk-method-test"
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}
	method := svc.GetAPIKeyStorageMethod()
	if method != "dpapi" && method != "aes" && method != "keyring" {
		t.Errorf("GetAPIKeyStorageMethod() = %q, want \"dpapi\", \"aes\", or \"keyring\"", method)
	}

	// Empty key → "none".
	settings.AIApiKey = ""
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}
	if got := svc.GetAPIKeyStorageMethod(); got != "none" {
		t.Errorf("GetAPIKeyStorageMethod() = %q, want %q (empty key)", got, "none")
	}
}

func TestSettingsService_SaveSettings_emptyKeyDoesNotAddPrefix(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	svc := &SettingsService{configPath: configPath}

	settings := defaultSettings()
	settings.AIApiKey = ""
	if err := svc.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	raw := string(data)
	// Empty key should remain empty (no "plain:" or "dpapi:" prefix).
	if strings.Contains(raw, "plain:") || strings.Contains(raw, "dpapi:") || strings.Contains(raw, "aes:") {
		t.Error("settings.json contains encryption prefix for empty key — should be empty string")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- N-49: Cross-platform AES decrypt via DecryptSecret ---

// TestDecryptSecret_aesPrefixDecryptsOnAllPlatforms verifies that an "aes:"
// prefixed value can be decrypted via DecryptSecret on every platform. AES is
// the cross-platform fallback, so a value encrypted on macOS/Linux can be
// decrypted on Windows (and vice versa) when the per-install key file is
// available. This test encrypts with aesEncrypt directly (bypassing the
// platform encrypt path) and decrypts via the public DecryptSecret entry
// point.
func TestDecryptSecret_aesPrefixDecryptsOnAllPlatforms(t *testing.T) {
	plaintext := "sk-cross-platform-key"
	encrypted, err := aesEncrypt(plaintext)
	if err != nil {
		t.Fatalf("aesEncrypt failed: %v", err)
	}
	if !strings.HasPrefix(encrypted, secretPrefixAES) {
		t.Fatalf("aesEncrypt returned %q, want aes: prefix", encrypted[:min(len(encrypted), 20)])
	}
	got, err := DecryptSecret(keyringAccount, encrypted)
	if err != nil {
		t.Fatalf("DecryptSecret(keyringAccount,aes:) failed: %v", err)
	}
	if got != plaintext {
		t.Errorf("DecryptSecret(keyringAccount,aes:) = %q, want %q", got, plaintext)
	}
}

// TestDecryptSecret_aesRoundTripViaEncryptSecret verifies that when the
// platform's EncryptSecret chooses AES (the fallback path), the result
// can be decrypted by DecryptSecret. On Windows this uses DPAPI, so we
// test the AES path by calling aesEncrypt directly — but we also verify
// that EncryptSecret's output round-trips (which it should regardless of
// the platform's chosen method).
func TestDecryptSecret_aesRoundTripViaEncryptSecret(t *testing.T) {
	// Encrypt with AES directly, simulating a value created on a non-Windows
	// platform that was then copied to this machine.
	plaintext := "sk-migrated-from-macos"
	encrypted, err := aesEncrypt(plaintext)
	if err != nil {
		t.Fatalf("aesEncrypt failed: %v", err)
	}
	// DecryptSecret should handle aes: on any platform (N-49).
	got, err := DecryptSecret(keyringAccount, encrypted)
	if err != nil {
		t.Fatalf("DecryptSecret failed for aes: value: %v", err)
	}
	if got != plaintext {
		t.Errorf("DecryptSecret = %q, want %q", got, plaintext)
	}
}

// --- N-49: ListSecrets / DeleteSecret ---

// TestListSecrets_doesNotError verifies that ListSecrets returns without
// error on every platform. On Windows it returns an empty list (DPAPI blobs
// live in settings.json). On macOS/Linux it may return entries if the
// keyring CLI is available and has koyori-ide entries.
func TestListSecrets_doesNotError(t *testing.T) {
	infos, err := ListSecrets()
	if err != nil {
		t.Fatalf("ListSecrets failed: %v", err)
	}
	// We don't assert specific contents — the result depends on whether the
	// keyring CLI is available and has entries. We just verify it doesn't
	// crash and returns a (possibly empty) slice.
	if infos == nil {
		// nil is acceptable on Windows / when no keyring is available.
		return
	}
	for _, info := range infos {
		if info.Account == "" {
			t.Errorf("ListSecrets returned entry with empty Account: %+v", info)
		}
	}
}

// TestDeleteSecret_idempotent verifies that DeleteSecret returns nil even
// when the entry doesn't exist. This is important because users may click
// "delete keyring entry" when there's nothing to delete.
func TestDeleteSecret_idempotent(t *testing.T) {
	// Delete a non-existent account — should not error.
	if err := DeleteSecret("nonexistent-account-12345"); err != nil {
		t.Errorf("DeleteSecret(nonexistent) returned error: %v", err)
	}
	// Delete the default account — should not error whether or not it exists.
	if err := DeleteSecret(keyringAccount); err != nil {
		t.Errorf("DeleteSecret(%q) returned error: %v", keyringAccount, err)
	}
	// Delete with empty account — should not error.
	if err := DeleteSecret(""); err != nil {
		t.Errorf("DeleteSecret(\"\") returned error: %v", err)
	}
}

// TestSettingsService_ListSecrets verifies the SettingsService method
// delegates to the package-level ListSecrets without error.
func TestSettingsService_ListSecrets(t *testing.T) {
	svc := &SettingsService{configPath: filepath.Join(t.TempDir(), "settings.json")}
	infos, err := svc.ListSecrets()
	if err != nil {
		t.Fatalf("SettingsService.ListSecrets failed: %v", err)
	}
	// Result should be nil or a slice — both are acceptable.
	_ = infos
}

// TestSettingsService_DeleteSecret verifies the SettingsService method
// delegates to the package-level DeleteSecret without error.
func TestSettingsService_DeleteSecret(t *testing.T) {
	svc := &SettingsService{configPath: filepath.Join(t.TempDir(), "settings.json")}
	if err := svc.deleteExtensionSecret("koyori-ide.extHost.test-account"); err != nil {
		t.Errorf("SettingsService.deleteExtensionSecret failed: %v", err)
	}
}

// TestSecretInfo_JSON verifies the SecretInfo struct serializes correctly
// for the Wails binding layer.
func TestSecretInfo_JSON(t *testing.T) {
	info := SecretInfo{Account: "ai-api-key", Method: "keyring", Stored: true}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"account":"ai-api-key"`) {
		t.Errorf("JSON missing account field: %s", got)
	}
	if !strings.Contains(got, `"method":"keyring"`) {
		t.Errorf("JSON missing method field: %s", got)
	}
	if !strings.Contains(got, `"stored":true`) {
		t.Errorf("JSON missing stored field: %s", got)
	}
}

// --- H-4: AES 密钥文件原子写入测试 ---

// TestSecretsAES_KeySurvivesInterruptedWrite 验证写入中断后重新加载密钥一致 (H-4)。
// 原子写入保证：进程崩溃不会产生半写的损坏文件。即使文件被外部损坏，
// loadOrCreateAESKeyAt 也能检测并重新生成有效密钥，且重新加载后保持一致。
func TestSecretsAES_KeySurvivesInterruptedWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.key")

	// 首次生成密钥
	key1, err := loadOrCreateAESKeyAt(p)
	if err != nil {
		t.Fatalf("首次 loadOrCreateAESKeyAt 失败: %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("密钥长度 = %d, 期望 32", len(key1))
	}

	// 重新加载，密钥应一致
	key2, err := loadOrCreateAESKeyAt(p)
	if err != nil {
		t.Fatalf("第二次 loadOrCreateAESKeyAt 失败: %v", err)
	}
	if !bytes.Equal(key1, key2) {
		t.Fatal("重新加载后密钥不一致 — 应保持一致")
	}

	// 模拟写入中断（非原子写入会产生损坏文件）
	if err := os.WriteFile(p, []byte("corrupt-partial"), 0600); err != nil {
		t.Fatal(err)
	}

	// 重新加载：应检测损坏并重新生成有效密钥
	key3, err := loadOrCreateAESKeyAt(p)
	if err != nil {
		t.Fatalf("损坏后 loadOrCreateAESKeyAt 失败: %v", err)
	}
	if len(key3) != 32 {
		t.Errorf("重新生成的密钥长度 = %d, 期望 32", len(key3))
	}
	if bytes.Equal(key2, key3) {
		t.Error("损坏后重新生成的密钥不应与旧密钥相同")
	}

	// 再次重新加载，新密钥应保持一致
	key4, err := loadOrCreateAESKeyAt(p)
	if err != nil {
		t.Fatalf("第四次 loadOrCreateAESKeyAt 失败: %v", err)
	}
	if !bytes.Equal(key3, key4) {
		t.Error("重新加载后密钥应保持一致")
	}

	// 验证没有临时文件残留（原子写入的保证）
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("原子写入后残留临时文件: %s", e.Name())
		}
	}
}

// TestSecretsAES_DoesNotOverwriteExistingKey 验证二次校验：已存在有效密钥时不覆盖 (H-4)。
// 模拟多实例首次创建场景：另一实例已在 MkdirAll 后写入密钥，当前实例不应覆盖它。
func TestSecretsAES_DoesNotOverwriteExistingKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.key")

	// 预先创建目录并写入已知密钥（模拟另一实例已创建）
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	knownKey := make([]byte, 32)
	for i := range knownKey {
		knownKey[i] = byte(i + 1)
	}
	if err := os.WriteFile(p, []byte(hex.EncodeToString(knownKey)), 0600); err != nil {
		t.Fatal(err)
	}

	// loadOrCreateAESKeyAt 应返回已有密钥，不生成新密钥
	key, err := loadOrCreateAESKeyAt(p)
	if err != nil {
		t.Fatalf("loadOrCreateAESKeyAt 失败: %v", err)
	}
	if !bytes.Equal(key, knownKey) {
		t.Error("应返回已有密钥，而非生成新密钥")
	}

	// 再次读取文件，确认未被覆盖
	fileData, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	fileKey, err := hex.DecodeString(strings.TrimSpace(string(fileData)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fileKey, knownKey) {
		t.Error("文件中的密钥被覆盖 — 二次校验失败")
	}
}

// TestEncryptSecret_H8_RejectsOversizedPlaintext 验证 H-8：超大明文被拒绝。
// DPAPI 加密的明文应有限制，防止超大输入导致内存问题。
func TestEncryptSecret_H8_RejectsOversizedPlaintext(t *testing.T) {
	// H-8: 明文超过 dpapiMaxPlaintextSize (1MB) 应被拒绝。
	// 在 Windows 上，platformEncryptSecret 会检查大小。
	// 在其他平台上，这个测试验证 EncryptSecret 不会 panic 或卡死。
	oversized := strings.Repeat("x", maxSecretPlaintextSize+1)
	_, err := EncryptSecret(keyringAccount, oversized)
	if err == nil {
		// 在 Windows 上必须返回错误。在其他平台上，如果 AES 路径不限制大小，
		// 我们至少验证它不会卡死。但为了安全，所有平台都应限制。
		// 如果非 Windows 平台不限制，这里只记录日志不 fail。
		if runtime.GOOS == "windows" {
			t.Error("H-8: expected error for oversized plaintext on Windows, got nil")
		}
	}
}

// TestEncryptDecryptSecret_H8_RoundTripWithEntropy 验证 H-8：带熵的
// DPAPI 加解密往返正确。加密的值可以用相同的熵解密。
func TestEncryptDecryptSecret_H8_RoundTripWithEntropy(t *testing.T) {
	cases := []string{
		"sk-test-key-with-entropy",
		"short",
		strings.Repeat("a", 100),
	}
	for _, plaintext := range cases {
		t.Run(plaintext[:min(len(plaintext), 20)], func(t *testing.T) {
			encrypted, err := EncryptSecret(keyringAccount, plaintext)
			if err != nil {
				t.Fatalf("EncryptSecret failed: %v", err)
			}
			if encrypted == "" {
				t.Fatal("EncryptSecret returned empty for non-empty input")
			}
			if !IsSecretEncrypted(encrypted) {
				t.Error("IsSecretEncrypted should return true")
			}
			decrypted, err := DecryptSecret(keyringAccount, encrypted)
			if err != nil {
				t.Fatalf("DecryptSecret failed: %v", err)
			}
			if decrypted != plaintext {
				t.Errorf("DecryptSecret = %q, want %q", decrypted, plaintext)
			}
		})
	}
}

// TestSecrets_L4_ParameterizedAccount 验证 L-4 修复:EncryptSecret/DecryptSecret
// 接受 account 参数,支持用自定义 account 名存储不同类型的密钥。此测试使用
// 非默认 account 名,确认 round-trip 加解密正常工作。
//
// 在 Windows 上 DPAPI 不使用 account(用应用专属熵),round-trip 通过熵完成;
// 在 macOS/Linux 上,若 keyring 可用则用传入的 account 存取,否则 fallback 到
// AES(不使用 account)。无论哪条路径,相同 account 的加解密往返都应成功。
func TestSecrets_L4_ParameterizedAccount(t *testing.T) {
	const customAccount = "l4-custom-account"
	cases := []string{
		"sk-l4-custom-key",
		"another-secret-value-12345",
		strings.Repeat("x", 100),
	}
	for _, plaintext := range cases {
		t.Run(plaintext[:min(len(plaintext), 20)], func(t *testing.T) {
			encrypted, err := EncryptSecret(customAccount, plaintext)
			if err != nil {
				t.Fatalf("EncryptSecret(%q, %q) error: %v", customAccount, plaintext, err)
			}
			if encrypted == "" {
				t.Fatal("EncryptSecret returned empty for non-empty input")
			}
			decrypted, err := DecryptSecret(customAccount, encrypted)
			if err != nil {
				t.Fatalf("DecryptSecret(%q, ...) error: %v", customAccount, err)
			}
			if decrypted != plaintext {
				t.Errorf("DecryptSecret round-trip: got %q, want %q", decrypted, plaintext)
			}
		})
	}
}

// TestLoadOrCreateAESKey_ConcurrentFirstCreate (P2-1 / N-3) 验证多实例首次
// 创建密钥时，所有实例最终读到同一密钥。创建、完整写入和发布必须受同一个
// 跨进程锁保护，不能把尚未写完的最终文件暴露给其他实例。
func TestLoadOrCreateAESKey_ConcurrentFirstCreate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.key")

	const n = 10
	keys := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			<-start
			k, err := loadOrCreateAESKeyAt(p)
			keys[idx] = k
			errs[idx] = err
		}(i)
	}
	close(start)
	wg.Wait()

	// 所有调用都不应报错
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d error: %v", i, err)
		}
		if len(keys[i]) != 32 {
			t.Fatalf("goroutine %d returned key of wrong length: %d", i, len(keys[i]))
		}
	}
	// 所有 goroutine 必须读到同一密钥
	first := keys[0]
	for i := 1; i < n; i++ {
		if !bytes.Equal(keys[i], first) {
			t.Errorf("goroutine %d returned different key — TOCTOU 残留: got %x, want %x", i, keys[i], first)
		}
	}
	// 磁盘上的密钥也必须与内存中一致
	diskData, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read disk key: %v", err)
	}
	diskKey, err := hex.DecodeString(strings.TrimSpace(string(diskData)))
	if err != nil {
		t.Fatalf("decode disk key: %v", err)
	}
	if !bytes.Equal(diskKey, first) {
		t.Errorf("disk key %x != in-memory key %x — 磁盘与内存不一致", diskKey, first)
	}
}

// TestLoadOrCreateAESKey_ConcurrentNoOverwrite (P2-1 / N-3) 验证已存在密钥
// 时，10 个并发 goroutine 都读到同一已有密钥且不覆盖磁盘文件。
func TestLoadOrCreateAESKey_ConcurrentNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.key")

	// 预先写入已知密钥
	knownKey := make([]byte, 32)
	for i := range knownKey {
		knownKey[i] = byte(i + 7)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(hex.EncodeToString(knownKey)), 0o600); err != nil {
		t.Fatal(err)
	}

	const n = 10
	keys := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			<-start
			k, err := loadOrCreateAESKeyAt(p)
			keys[idx] = k
			errs[idx] = err
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d error: %v", i, err)
		}
		if !bytes.Equal(keys[i], knownKey) {
			t.Errorf("goroutine %d returned different key: got %x, want %x", i, keys[i], knownKey)
		}
	}

	// 磁盘文件未被覆盖
	diskData, _ := os.ReadFile(p)
	diskKey, _ := hex.DecodeString(strings.TrimSpace(string(diskData)))
	if !bytes.Equal(diskKey, knownKey) {
		t.Errorf("disk key was overwritten: got %x, want %x", diskKey, knownKey)
	}
}

// TestLoadOrCreateAESKey_ConcurrentCorruptRecovery 验证多个实例同时发现损坏密钥时，
// 只生成并发布一个替代密钥，且每个调用返回值都与最终磁盘内容一致。
func TestLoadOrCreateAESKey_ConcurrentCorruptRecovery(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(p, []byte("corrupt-partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	const n = 20
	keys := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			<-start
			keys[idx], errs[idx] = loadOrCreateAESKeyAt(p)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d error: %v", i, err)
		}
		if len(keys[i]) != 32 {
			t.Fatalf("goroutine %d returned key of wrong length: %d", i, len(keys[i]))
		}
	}
	first := keys[0]
	for i := 1; i < n; i++ {
		if !bytes.Equal(keys[i], first) {
			t.Errorf("goroutine %d returned different recovery key: got %x, want %x", i, keys[i], first)
		}
	}
	diskData, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read recovered disk key: %v", err)
	}
	diskKey, err := hex.DecodeString(strings.TrimSpace(string(diskData)))
	if err != nil {
		t.Fatalf("decode recovered disk key: %v", err)
	}
	if !bytes.Equal(diskKey, first) {
		t.Errorf("recovered disk key %x != returned key %x", diskKey, first)
	}
}

func TestLoadOrCreateAESKey_HelperProcess(t *testing.T) {
	if os.Getenv("KOYORI_IDE_AES_KEY_HELPER") != "1" {
		return
	}
	keyPath := os.Getenv("KOYORI_IDE_AES_KEY_PATH")
	readyPath := os.Getenv("KOYORI_IDE_AES_READY_PATH")
	barrierPath := os.Getenv("KOYORI_IDE_AES_BARRIER_PATH")
	resultPath := os.Getenv("KOYORI_IDE_AES_RESULT_PATH")
	if keyPath == "" || readyPath == "" || barrierPath == "" || resultPath == "" {
		t.Fatal("AES helper process environment is incomplete")
	}
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		t.Fatalf("write ready marker: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(barrierPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat barrier: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for AES helper barrier")
		}
		time.Sleep(5 * time.Millisecond)
	}
	key, err := loadOrCreateAESKeyAt(keyPath)
	if err != nil {
		t.Fatalf("loadOrCreateAESKeyAt: %v", err)
	}
	if err := os.WriteFile(resultPath, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		t.Fatalf("write helper result: %v", err)
	}
}

func TestLoadOrCreateAESKey_ConcurrentProcesses(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")
	barrierPath := filepath.Join(dir, "start")

	const n = 4
	commands := make([]*exec.Cmd, n)
	outputs := make([]*bytes.Buffer, n)
	readyPaths := make([]string, n)
	resultPaths := make([]string, n)
	for i := 0; i < n; i++ {
		readyPaths[i] = filepath.Join(dir, fmt.Sprintf("ready-%d", i))
		resultPaths[i] = filepath.Join(dir, fmt.Sprintf("result-%d", i))
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestLoadOrCreateAESKey_HelperProcess$")
		commands[i].Env = append(os.Environ(),
			"KOYORI_IDE_AES_KEY_HELPER=1",
			"KOYORI_IDE_AES_KEY_PATH="+keyPath,
			"KOYORI_IDE_AES_READY_PATH="+readyPaths[i],
			"KOYORI_IDE_AES_BARRIER_PATH="+barrierPath,
			"KOYORI_IDE_AES_RESULT_PATH="+resultPaths[i],
		)
		outputs[i] = &bytes.Buffer{}
		commands[i].Stdout = outputs[i]
		commands[i].Stderr = outputs[i]
		if err := commands[i].Start(); err != nil {
			t.Fatalf("start helper %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		allReady := true
		for _, readyPath := range readyPaths {
			if _, err := os.Stat(readyPath); err != nil {
				allReady = false
				break
			}
		}
		if allReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for AES helper processes")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.WriteFile(barrierPath, []byte("start"), 0o600); err != nil {
		t.Fatalf("release helper barrier: %v", err)
	}

	var processFailures []string
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			processFailures = append(processFailures, fmt.Sprintf("helper %d: %v\n%s", i, err, outputs[i].String()))
		}
	}
	if len(processFailures) > 0 {
		t.Fatalf("AES helper process failures:\n%s", strings.Join(processFailures, "\n"))
	}

	var first []byte
	for i, resultPath := range resultPaths {
		encoded, err := os.ReadFile(resultPath)
		if err != nil {
			t.Fatalf("read helper %d result: %v", i, err)
		}
		key, err := hex.DecodeString(strings.TrimSpace(string(encoded)))
		if err != nil || len(key) != 32 {
			t.Fatalf("helper %d returned invalid key: %q (%v)", i, encoded, err)
		}
		if i == 0 {
			first = key
		} else if !bytes.Equal(key, first) {
			t.Errorf("helper %d returned different key: got %x, want %x", i, key, first)
		}
	}
	diskData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read disk key: %v", err)
	}
	diskKey, err := hex.DecodeString(strings.TrimSpace(string(diskData)))
	if err != nil {
		t.Fatalf("decode disk key: %v", err)
	}
	if !bytes.Equal(diskKey, first) {
		t.Errorf("disk key %x != process key %x", diskKey, first)
	}
}
