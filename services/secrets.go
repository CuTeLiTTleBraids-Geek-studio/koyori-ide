package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adrg/xdg"
)

const secretsAuditLogEnv = "KOYORI_IDE_SECRETS_AUDIT_LOG"

var secretsAuditLog struct {
	sync.RWMutex
	writer io.Writer
}

type secretAuditWriteRequest struct {
	data   []byte
	result chan secretAuditWriteResult
}

type secretAuditWriteResult struct {
	n   int
	err error
}

type serializedSecretAuditWriter struct {
	requests  chan secretAuditWriteRequest
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	closed    bool
	inflight  sync.WaitGroup
}

func newSerializedSecretAuditWriter(writer io.Writer) *serializedSecretAuditWriter {
	serialized := &serializedSecretAuditWriter{
		requests: make(chan secretAuditWriteRequest),
		done:     make(chan struct{}),
	}
	go func() {
		defer close(serialized.done)
		for request := range serialized.requests {
			n, err := writer.Write(request.data)
			request.result <- secretAuditWriteResult{n: n, err: err}
		}
	}()
	return serialized
}

func (w *serializedSecretAuditWriter) Write(data []byte) (int, error) {
	copyOfData := append([]byte(nil), data...)
	result := make(chan secretAuditWriteResult, 1)
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	w.inflight.Add(1)
	w.mu.Unlock()
	defer w.inflight.Done()
	w.requests <- secretAuditWriteRequest{data: copyOfData, result: result}
	writeResult := <-result
	return writeResult.n, writeResult.err
}

func (w *serializedSecretAuditWriter) Close() {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		w.inflight.Wait()
		close(w.requests)
	})
	<-w.done
}

// Secret method prefixes identify how a stored secret was encrypted.
//   - "dpapi:"    — Windows DPAPI (CryptProtectData), machine-bound
//   - "aes:"      — AES-256-GCM with a per-install key file (non-Windows fallback)
//   - "keyring:"  — macOS Keychain / Linux libsecret via CLI wrapper (N-15)
//   - "plain:"    — explicit plaintext marker (only used when encryption is unavailable)
//
// Bare strings (no prefix) are treated as legacy plaintext for backward
// compatibility with settings.json files written before N-13.
// secretsTestAESOnly 是仅供测试使用的开关：置真时 darwin/linux 的
// platformEncryptSecret 直接走 AES fallback，测试因此不会读写真实用户
// keychain。由 services 包的 TestMain 设置；生产路径恒为 false。
var secretsTestAESOnly = false

const (
	secretPrefixDPAPI   = "dpapi:"
	secretPrefixAES     = "aes:"
	secretPrefixKeyring = "keyring:"
	secretPrefixPlain   = "plain:"
)

// maxSecretPlaintextSize 限制加密明文的最大大小（H-8）。防止超大输入导致
// 内存问题。1MB 对 API key 等敏感数据绰绰有余。所有平台共用此限制。
const maxSecretPlaintextSize = 1 << 20 // 1 MB

// EncryptSecret encrypts a plaintext secret using the platform's preferred
// method. On Windows it uses DPAPI; on macOS/Linux it tries the native
// keychain (Keychain / libsecret) and falls back to AES-256-GCM with a
// per-install key file when the keyring CLI is unavailable. An empty input
// returns an empty string (no prefix), so empty fields stay empty.
//
// L-4: account 参数化。account 是 keyring 存储/查找时使用的账户标签
// (macOS Keychain 的 "-a" 参数 / Linux libsecret 的 "koyori-ide:account"
// 属性)。DPAPI 和 AES fallback 路径不使用 account(前者用应用专属熵,
// 后者用 per-install 密钥文件),但接受该参数以保持统一接口。现有调用者
// 应传 keyringAccount("ai-api-key")以保持向后兼容。
func EncryptSecret(account, plaintext string) (string, error) {
	recordSecretAudit("set", account)
	if plaintext == "" {
		return "", nil
	}
	return platformEncryptSecret(account, plaintext)
}

// DecryptSecret decrypts a value produced by EncryptSecret. It handles:
//   - "dpapi:", "aes:", and "keyring:" prefixed values (decrypted via the
//     matching platform path)
//   - "plain:" prefixed values (stripped)
//   - bare strings (returned as-is, for backward compatibility)
//   - empty string (returned as-is)
//
// L-4: account 参数化。account 仅在 "keyring:" 前缀路径中使用(用于查找
// 对应的 keychain/libsecret 条目)。调用者必须传入与加密时相同的 account。
func DecryptSecret(account, stored string) (string, error) {
	recordSecretAudit("get", account)
	if stored == "" {
		return "", nil
	}
	if strings.HasPrefix(stored, secretPrefixPlain) {
		return strings.TrimPrefix(stored, secretPrefixPlain), nil
	}
	if strings.HasPrefix(stored, secretPrefixDPAPI) ||
		strings.HasPrefix(stored, secretPrefixAES) ||
		strings.HasPrefix(stored, secretPrefixKeyring) {
		return platformDecryptSecret(account, stored)
	}
	// Legacy plaintext — return as-is so existing settings keep working.
	return stored, nil
}

// IsSecretEncrypted returns true when the stored value carries an encryption
// prefix ("dpapi:", "aes:", or "keyring:"). Bare strings and "plain:" return
// false.
func IsSecretEncrypted(stored string) bool {
	return strings.HasPrefix(stored, secretPrefixDPAPI) ||
		strings.HasPrefix(stored, secretPrefixAES) ||
		strings.HasPrefix(stored, secretPrefixKeyring)
}

// SecretMethod returns a human-readable label for the encryption method used
// by the stored value: "dpapi", "aes", "keyring", "plain", or "none" (for
// empty/legacy).
func SecretMethod(stored string) string {
	switch {
	case stored == "":
		return "none"
	case strings.HasPrefix(stored, secretPrefixDPAPI):
		return "dpapi"
	case strings.HasPrefix(stored, secretPrefixAES):
		return "aes"
	case strings.HasPrefix(stored, secretPrefixKeyring):
		return "keyring"
	case strings.HasPrefix(stored, secretPrefixPlain):
		return "plain"
	default:
		return "plain"
	}
}

// SecretInfo describes a secret entry discovered in the platform keyring
// (macOS Keychain / Linux libsecret). Used by ListSecrets so the settings UI
// can show users what's stored and let them delete orphans (N-49).
type SecretInfo struct {
	Account string `json:"account"` // keyring account/label
	Method  string `json:"method"`  // "dpapi", "aes", "keyring", "plain", "none"
	Stored  bool   `json:"stored"`  // whether an entry exists in the keyring
}

// ListSecrets returns information about secrets stored in the platform
// keyring (macOS Keychain / Linux libsecret). On Windows, where DPAPI blobs
// live inside settings.json rather than a separate keyring, it returns an
// empty list. This is used by the settings UI to show users what's in their
// keyring so they can clean up orphan entries left behind when AIApiKey was
// cleared (N-49).
func ListSecrets() ([]SecretInfo, error) {
	return platformListSecrets()
}

// DeleteSecret removes the secret with the given account from the platform
// keyring. On Windows this is a no-op (DPAPI secrets are in settings.json).
// Returns nil if the entry didn't exist (idempotent). This lets users clean
// up orphan keyring entries that remain after clearing AIApiKey in
// settings.json (N-49).
func DeleteSecret(account string) error {
	recordSecretAudit("delete", account)
	return platformDeleteSecret(account)
}

// RotateAESKey replaces the per-install AES key and re-encrypts every AES
// secret in Koyori IDE's settings, profile, IM, and MCP configuration files.
// Interrupted rotations remain recoverable through a temporary .previous key.
// Callers must pause configuration writers while this maintenance operation
// runs; after a successful migration, the two newest retired keys remain in
// history so three generations can still be decrypted.
func RotateAESKey() error {
	recordAESRotationAudit()
	return rotateAESKey()
}

// recordSecretAudit writes a best-effort audit record without secret values.
// Set KOYORI_IDE_SECRETS_AUDIT_LOG to a file path to enable it. Values such as
// "1", "true", "on", and "yes" use the default XDG cache path. Logging is
// intentionally opt-in so existing installations keep their current behavior.
func recordSecretAudit(action, key string) {
	if err := writeSecretAudit(action, key); err != nil {
		slog.Warn("secrets: failed to write audit log", "error", err, "action", action, "key", key)
	}
}

func recordAESRotationAudit() {
	if err := writeSecretAudit("rotate-aes-key started", ""); err != nil {
		slog.Warn("secrets: failed to write audit log", "error", err, "action", "rotate-aes-key started")
	}
}

func writeSecretAudit(action, key string) error {
	secretsAuditLog.RLock()
	writer := secretsAuditLog.writer
	secretsAuditLog.RUnlock()
	if writer != nil {
		return writeSecretAuditRecord(writer, action, key)
	}

	path, enabled, err := configuredSecretsAuditLogPath()
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secrets: failed to create audit log dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("secrets: failed to open audit log: %w", err)
	}
	writeErr := writeSecretAuditRecord(file, action, key)
	closeErr := file.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("secrets: failed to close audit log: %w", closeErr)
	}
	return errors.Join(writeErr, closeErr)
}

func writeSecretAuditRecord(writer io.Writer, action, key string) error {
	timestamp := time.Now().UTC().Format(time.RFC3339)
	var line string
	if action == "rotate-aes-key started" {
		line = fmt.Sprintf("%s %s\n", timestamp, action)
	} else {
		line = fmt.Sprintf("%s %s %s\n", timestamp, action, strconv.Quote(key))
	}
	if _, err := io.WriteString(writer, line); err != nil {
		return fmt.Errorf("secrets: failed to write audit record: %w", err)
	}
	return nil
}

func secretsAuditLogPath(setting string) (string, bool) {
	setting = strings.TrimSpace(setting)
	switch strings.ToLower(setting) {
	case "", "0", "false", "off", "no":
		return "", false
	case "1", "true", "on", "yes":
		return filepath.Join(xdg.CacheHome, "koyori-ide", "secrets-audit.log"), true
	default:
		return setting, true
	}
}

func configuredSecretsAuditLogPath() (string, bool, error) {
	if setting, exists := os.LookupEnv(secretsAuditLogEnv); exists {
		path, enabled := secretsAuditLogPath(setting)
		return path, enabled, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", false, fmt.Errorf("secrets: resolve config directory: %w", err)
	}
	return loadSecretsAuditLogSettingAt(configDir)
}

func loadSecretsAuditLogSettingAt(configHome string) (string, bool, error) {
	root := filepath.Join(configHome, "koyori-ide")
	activeProfile := defaultProfileName
	stateData, err := os.ReadFile(filepath.Join(root, "profiles-state.json"))
	if err == nil {
		var state struct {
			ActiveProfile string `json:"activeProfile"`
		}
		if err := json.Unmarshal(stateData, &state); err != nil {
			return "", false, fmt.Errorf("secrets: decode profile state: %w", err)
		}
		if state.ActiveProfile != "" {
			if !profileNameRe.MatchString(state.ActiveProfile) {
				return "", false, fmt.Errorf("secrets: invalid active profile %q", state.ActiveProfile)
			}
			activeProfile = state.ActiveProfile
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("secrets: read profile state: %w", err)
	}

	paths := []string{
		filepath.Join(root, "profiles", activeProfile, "settings.json"),
		filepath.Join(root, "settings.json"),
	}
	for _, settingsPath := range paths {
		data, err := os.ReadFile(settingsPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("secrets: read audit setting: %w", err)
		}
		var settings map[string]json.RawMessage
		if err := json.Unmarshal(data, &settings); err != nil {
			return "", false, fmt.Errorf("secrets: decode audit setting: %w", err)
		}
		raw, ok := settings["secretsAuditLog"]
		if !ok {
			continue
		}
		var enabled bool
		if err := json.Unmarshal(raw, &enabled); err == nil {
			if !enabled {
				return "", false, nil
			}
			return filepath.Join(xdg.CacheHome, "koyori-ide", "secrets-audit.log"), true, nil
		}
		var path string
		if err := json.Unmarshal(raw, &path); err != nil {
			return "", false, fmt.Errorf("secrets: audit setting must be a boolean or path string: %w", err)
		}
		path, enabled = secretsAuditLogPath(path)
		return path, enabled, nil
	}
	return "", false, nil
}

func setSecretsAuditWriterForTest(writer io.Writer) func() {
	serialized := newSerializedSecretAuditWriter(writer)
	secretsAuditLog.Lock()
	previous := secretsAuditLog.writer
	secretsAuditLog.writer = serialized
	secretsAuditLog.Unlock()
	return func() {
		secretsAuditLog.Lock()
		if secretsAuditLog.writer == serialized {
			secretsAuditLog.writer = previous
		}
		secretsAuditLog.Unlock()
		serialized.Close()
	}
}
