package services

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/adrg/xdg"
)

// Keep two retired keys so the current key plus history can decrypt three
// generations. The temporary .previous key remains an additional recovery
// journal only while a rotation is incomplete.
const aesKeyHistoryLimit = 2

// keyFilePath returns the path to the per-install AES key file, stored in the
// XDG config directory. The key is a 32-byte random value generated on first
// use and persisted with 0600 permissions.
func keyFilePath() string {
	return filepath.Join(xdg.ConfigHome, "koyori-ide", "secret.key")
}

// loadOrCreateAESKeyAt 是 loadOrCreateAESKey 的可测试版本，接受自定义路径。
// 创建与损坏恢复由跨进程文件锁串行化，密钥先完整写入同目录临时文件，再通过
// atomicWriteFile 发布。这样其他实例不会读到零长度或部分写入的最终文件。
func loadOrCreateAESKeyAt(p string) (key []byte, retErr error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, fmt.Errorf("aes: failed to create key dir: %w", err)
	}
	lock, err := acquireAESKeyFileLock(p + ".lock")
	if err != nil {
		return nil, fmt.Errorf("aes: failed to lock key file: %w", err)
	}
	defer func() {
		if err := lock.release(); err != nil {
			if retErr == nil {
				key = nil
				retErr = fmt.Errorf("aes: failed to unlock key file: %w", err)
			} else {
				slog.Warn("aes: failed to unlock key file after another error", "err", err)
			}
		}
	}()
	return loadOrCreateAESKeyLocked(p)
}

// loadOrCreateAESKeyLocked requires the caller to hold the key-file lock.
func loadOrCreateAESKeyLocked(p string) ([]byte, error) {
	data, err := os.ReadFile(p)
	if err == nil {
		existingKey, decodeErr := decodeAESKey(data)
		if decodeErr == nil {
			return existingKey, nil
		}
		// A corrupted key cannot decrypt existing ciphertext. Preserve the exact
		// bytes before publishing a replacement so recovery remains diagnosable.
		backupPath := p + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		if err := os.Rename(p, backupPath); err != nil {
			return nil, fmt.Errorf("aes: failed to back up corrupt key file: %w", err)
		}
		slog.Error("aes: corrupt key file backed up",
			"path", p,
			"backup", backupPath,
			"reason", decodeErr,
		)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("aes: failed to read key file: %w", err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("aes: failed to generate key: %w", err)
	}
	encoded := []byte(hex.EncodeToString(key))
	if err := atomicWriteFile(p, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("aes: failed to persist key: %w", err)
	}
	return key, nil
}

func decodeAESKey(data []byte) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid hex key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid key length: got %d, want 32", len(key))
	}
	return key, nil
}

// aesEncrypt encrypts plaintext using AES-256-GCM with the per-install key.
// The nonce is prepended to the ciphertext, base64-encoded, and prefixed
// with the "aes:" marker. This is the fallback path when no platform-native
// keyring is available (or the keyring CLI is missing).
func aesEncrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	keyPath := keyFilePath()
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return "", fmt.Errorf("aes: failed to create key dir: %w", err)
	}
	lock, err := acquireAESKeyFileLock(keyPath + ".lock")
	if err != nil {
		return "", fmt.Errorf("aes: failed to lock key file: %w", err)
	}
	key, encryptErr := loadOrCreateAESKeyLocked(keyPath)
	var encrypted string
	if encryptErr == nil {
		encrypted, encryptErr = aesEncryptWithKey(plaintext, key)
	}
	if unlockErr := lock.release(); unlockErr != nil {
		encryptErr = errors.Join(encryptErr, fmt.Errorf("aes: failed to unlock key file: %w", unlockErr))
	}
	if encryptErr != nil {
		return "", encryptErr
	}
	return encrypted, nil
}

func aesEncryptWithKey(plaintext string, key []byte) (string, error) {
	plaintextBytes := []byte(plaintext)
	if len(plaintextBytes) == 0 {
		return "", nil
	}
	if len(plaintextBytes) > maxSecretPlaintextSize {
		return "", fmt.Errorf("aes: plaintext exceeds max size %d bytes", maxSecretPlaintextSize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes: new cipher failed: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("aes: new gcm failed: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("aes: nonce generation failed: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, plaintextBytes, nil)
	return secretPrefixAES + base64.StdEncoding.EncodeToString(sealed), nil
}

// aesDecrypt decrypts an "aes:"-prefixed value using AES-256-GCM.
func aesDecrypt(stored string) (string, error) {
	keys, err := loadAESDecryptionKeysAt(keyFilePath())
	if err != nil {
		return "", err
	}
	return aesDecryptWithKeys(stored, keys)
}

func aesDecryptWithKeys(stored string, keys [][]byte) (string, error) {
	b64 := strings.TrimPrefix(stored, secretPrefixAES)
	sealed, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("aes: invalid base64: %w", err)
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("aes: no decryption key is available")
	}
	var lastErr error
	for _, key := range keys {
		block, cipherErr := aes.NewCipher(key)
		if cipherErr != nil {
			return "", fmt.Errorf("aes: new cipher failed: %w", cipherErr)
		}
		gcm, gcmErr := cipher.NewGCM(block)
		if gcmErr != nil {
			return "", fmt.Errorf("aes: new gcm failed: %w", gcmErr)
		}
		if len(sealed) < gcm.NonceSize() {
			return "", fmt.Errorf("aes: ciphertext too short")
		}
		nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
		plaintext, openErr := gcm.Open(nil, nonce, ciphertext, nil)
		if openErr == nil {
			return string(plaintext), nil
		}
		lastErr = openErr
	}
	return "", fmt.Errorf("aes: decryption failed: %w", lastErr)
}

func loadAESDecryptionKeysAt(p string) (keys [][]byte, retErr error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, fmt.Errorf("aes: failed to create key dir: %w", err)
	}
	lock, err := acquireAESKeyFileLock(p + ".lock")
	if err != nil {
		return nil, fmt.Errorf("aes: failed to lock key file: %w", err)
	}
	defer func() {
		if err := lock.release(); err != nil {
			if retErr == nil {
				keys = nil
				retErr = fmt.Errorf("aes: failed to unlock key file: %w", err)
			} else {
				slog.Warn("aes: failed to unlock key file after another error", "err", err)
			}
		}
	}()

	current, err := loadOrCreateAESKeyLocked(p)
	if err != nil {
		return nil, err
	}
	keys = appendUniqueAESKey(keys, current)
	previousData, err := os.ReadFile(p + ".previous")
	if err == nil {
		previous, decodeErr := decodeAESKey(previousData)
		if decodeErr != nil {
			return nil, fmt.Errorf("aes: invalid previous rotation key: %w", decodeErr)
		}
		keys = appendUniqueAESKey(keys, previous)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("aes: failed to read previous rotation key: %w", err)
	}
	history, err := loadAESKeyHistory(p + ".history")
	if err != nil {
		return nil, err
	}
	for _, historicalKey := range history {
		keys = appendUniqueAESKey(keys, historicalKey)
	}
	return keys, nil
}

func appendUniqueAESKey(keys [][]byte, key []byte) [][]byte {
	for _, existing := range keys {
		if bytes.Equal(existing, key) {
			return keys
		}
	}
	return append(keys, append([]byte(nil), key...))
}

func loadAESKeyHistory(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("aes: failed to read key history: %w", err)
	}
	var keys [][]byte
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, decodeErr := decodeAESKey([]byte(line))
		if decodeErr != nil {
			return nil, fmt.Errorf("aes: invalid key history entry %d: %w", lineNumber+1, decodeErr)
		}
		keys = appendUniqueAESKey(keys, key)
	}
	return keys, nil
}

func nextAESKeyHistory(retiredKey []byte, existing [][]byte) [][]byte {
	history := appendUniqueAESKey(nil, retiredKey)
	for _, key := range existing {
		history = appendUniqueAESKey(history, key)
		if len(history) == aesKeyHistoryLimit {
			break
		}
	}
	return history
}

func writeAESKeyHistory(path string, keys [][]byte) error {
	if len(keys) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("aes: failed to remove empty key history: %w", err)
		}
		return nil
	}

	var builder strings.Builder
	for i, key := range keys {
		if i == aesKeyHistoryLimit {
			break
		}
		if i > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(hex.EncodeToString(key))
	}
	builder.WriteByte('\n')
	if err := atomicWriteFile(path, []byte(builder.String()), 0o600); err != nil {
		return fmt.Errorf("aes: failed to persist key history: %w", err)
	}
	return nil
}

type aesRotationFile struct {
	path     string
	original []byte
	updated  []byte
}

var aesRotationConfigNames = map[string]struct{}{
	"im.json":          {},
	"mcp-servers.json": {},
	"settings.json":    {},
}

// rotateAESKey rotates the installation key and migrates every AES value in
// Koyori IDE's known JSON configuration files. A previous-key file keeps both
// generations readable if the process stops between the key and data writes;
// calling RotateAESKey again resumes and finishes that interrupted migration.
// Two historical keys remain available after a successful rewrite so values
// from the current and two prior generations remain decryptable.
func rotateAESKey() error {
	keyPath := keyFilePath()
	configRoots := []string{filepath.Dir(keyPath)}
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("aes: failed to locate user config dir: %w", err)
	}
	userConfigRoot := filepath.Join(userConfigDir, "koyori-ide")
	if filepath.Clean(userConfigRoot) != filepath.Clean(configRoots[0]) {
		configRoots = append(configRoots, userConfigRoot)
	}
	return rotateAESKeyAtRoots(keyPath, configRoots)
}

func rotateAESKeyAt(keyPath, configRoot string) (retErr error) {
	return rotateAESKeyAtRoots(keyPath, []string{configRoot})
}

func rotateAESKeyAtRoots(keyPath string, configRoots []string) (retErr error) {
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return fmt.Errorf("aes: failed to create key dir: %w", err)
	}
	lock, err := acquireAESKeyFileLock(keyPath + ".lock")
	if err != nil {
		return fmt.Errorf("aes: failed to lock key file: %w", err)
	}
	defer func() {
		if err := lock.release(); err != nil {
			if retErr == nil {
				retErr = fmt.Errorf("aes: failed to unlock key file: %w", err)
			} else {
				slog.Warn("aes: failed to unlock key file after rotation error", "err", err)
			}
		}
	}()

	currentKey, err := loadOrCreateAESKeyLocked(keyPath)
	if err != nil {
		return err
	}
	previousPath := keyPath + ".previous"
	historyPath := keyPath + ".history"
	historyKeys, err := loadAESKeyHistory(historyPath)
	if err != nil {
		return err
	}
	previousData, previousErr := os.ReadFile(previousPath)
	if previousErr == nil {
		previousKey, decodeErr := decodeAESKey(previousData)
		if decodeErr != nil {
			return fmt.Errorf("aes: invalid previous rotation key: %w", decodeErr)
		}
		if !bytes.Equal(previousKey, currentKey) {
			decryptKeys := appendUniqueAESKey(nil, currentKey)
			decryptKeys = appendUniqueAESKey(decryptKeys, previousKey)
			for _, historicalKey := range historyKeys {
				decryptKeys = appendUniqueAESKey(decryptKeys, historicalKey)
			}
			files, collectErr := collectAESRotationFilesFromRoots(configRoots, decryptKeys, currentKey)
			if collectErr != nil {
				return collectErr
			}
			if err := writeAESRotationFiles(files); err != nil {
				return err
			}
			if err := writeAESKeyHistory(historyPath, nextAESKeyHistory(previousKey, historyKeys)); err != nil {
				return err
			}
			if err := os.Remove(previousPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("aes: failed to remove previous rotation key: %w", err)
			}
			return nil
		}
		// The process stopped after publishing the recovery copy but before
		// switching secret.key. No data migration started, so discard the stale
		// journal and perform a complete rotation in this call.
		if err := os.Remove(previousPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("aes: failed to remove stale previous rotation key: %w", err)
		}
	} else if !os.IsNotExist(previousErr) {
		return fmt.Errorf("aes: failed to read previous rotation key: %w", previousErr)
	}

	newKey := make([]byte, 32)
	if _, err := rand.Read(newKey); err != nil {
		return fmt.Errorf("aes: failed to generate rotation key: %w", err)
	}
	decryptKeys := appendUniqueAESKey(nil, currentKey)
	for _, historicalKey := range historyKeys {
		decryptKeys = appendUniqueAESKey(decryptKeys, historicalKey)
	}
	files, err := collectAESRotationFilesFromRoots(configRoots, decryptKeys, newKey)
	if err != nil {
		return err
	}
	if err := atomicWriteFile(previousPath, []byte(hex.EncodeToString(currentKey)), 0o600); err != nil {
		return fmt.Errorf("aes: failed to persist previous rotation key: %w", err)
	}
	if err := atomicWriteFile(keyPath, []byte(hex.EncodeToString(newKey)), 0o600); err != nil {
		if removeErr := os.Remove(previousPath); removeErr != nil && !os.IsNotExist(removeErr) {
			slog.Warn("aes: failed to clean up previous key after key switch failure", "err", removeErr)
		}
		return fmt.Errorf("aes: failed to persist rotated key: %w", err)
	}
	if err := writeAESRotationFiles(files); err != nil {
		// Keep both key generations. Decryption remains available and a later
		// RotateAESKey call will resume the partially completed migration.
		return err
	}
	if err := writeAESKeyHistory(historyPath, nextAESKeyHistory(currentKey, historyKeys)); err != nil {
		return err
	}
	if err := os.Remove(previousPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("aes: failed to remove previous rotation key: %w", err)
	}
	return nil
}

func collectAESRotationFilesFromRoots(configRoots []string, decryptKeys [][]byte, encryptKey []byte) ([]aesRotationFile, error) {
	files := make([]aesRotationFile, 0)
	seen := make(map[string]struct{})
	for _, configRoot := range configRoots {
		rootFiles, err := collectAESRotationFiles(configRoot, decryptKeys, encryptKey)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, file := range rootFiles {
			cleanPath := filepath.Clean(file.path)
			if _, ok := seen[cleanPath]; ok {
				continue
			}
			seen[cleanPath] = struct{}{}
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

func collectAESRotationFiles(configRoot string, decryptKeys [][]byte, encryptKey []byte) ([]aesRotationFile, error) {
	var files []aesRotationFile
	err := filepath.WalkDir(configRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("aes: failed to scan rotation path %q: %w", path, walkErr)
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if _, ok := aesRotationConfigNames[entry.Name()]; !ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("aes: failed to read rotation config %q: %w", path, err)
		}
		if !bytes.Contains(data, []byte(secretPrefixAES)) {
			return nil
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("aes: failed to parse rotation config %q: %w", path, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				return fmt.Errorf("aes: rotation config %q contains multiple JSON values", path)
			}
			return fmt.Errorf("aes: failed to parse rotation config %q: %w", path, err)
		}
		updatedValue, count, err := reencryptAESValues(value, decryptKeys, encryptKey, "$")
		if err != nil {
			return fmt.Errorf("aes: failed to rotate secrets in %q: %w", path, err)
		}
		if count == 0 {
			return nil
		}
		updated, err := json.MarshalIndent(updatedValue, "", "  ")
		if err != nil {
			return fmt.Errorf("aes: failed to encode rotation config %q: %w", path, err)
		}
		updated = append(updated, '\n')
		files = append(files, aesRotationFile{path: path, original: data, updated: updated})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

func reencryptAESValues(value any, decryptKeys [][]byte, encryptKey []byte, jsonPath string) (any, int, error) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		total := 0
		for _, key := range keys {
			updated, count, err := reencryptAESValues(typed[key], decryptKeys, encryptKey, jsonPath+"."+key)
			if err != nil {
				return nil, 0, err
			}
			typed[key] = updated
			total += count
		}
		return typed, total, nil
	case []any:
		total := 0
		for i := range typed {
			updated, count, err := reencryptAESValues(typed[i], decryptKeys, encryptKey, fmt.Sprintf("%s[%d]", jsonPath, i))
			if err != nil {
				return nil, 0, err
			}
			typed[i] = updated
			total += count
		}
		return typed, total, nil
	case string:
		if !strings.HasPrefix(typed, secretPrefixAES) {
			return typed, 0, nil
		}
		plaintext, err := aesDecryptWithKeys(typed, decryptKeys)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", jsonPath, err)
		}
		rotated, err := aesEncryptWithKey(plaintext, encryptKey)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", jsonPath, err)
		}
		return rotated, 1, nil
	default:
		return value, 0, nil
	}
}

func writeAESRotationFiles(files []aesRotationFile) error {
	for _, file := range files {
		current, err := os.ReadFile(file.path)
		if err != nil {
			return fmt.Errorf("aes: failed to verify rotation config %q: %w", file.path, err)
		}
		if !bytes.Equal(current, file.original) {
			return fmt.Errorf("aes: rotation config %q changed during migration", file.path)
		}
		if err := atomicWriteFile(file.path, file.updated, 0o600); err != nil {
			return fmt.Errorf("aes: failed to write rotation config %q: %w", file.path, err)
		}
	}
	return nil
}
