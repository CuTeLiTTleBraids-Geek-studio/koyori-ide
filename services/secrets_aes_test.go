package services

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoadOrCreateAESKey_CorruptBackup_B3(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")
	corruptData := []byte("corrupt-key-material")
	if err := os.WriteFile(keyPath, corruptData, 0o600); err != nil {
		t.Fatalf("write corrupt key: %v", err)
	}

	key, err := loadOrCreateAESKeyAt(keyPath)
	if err != nil {
		t.Fatalf("recover corrupt key: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("recovered key length = %d, want 32", len(key))
	}

	backups, err := filepath.Glob(keyPath + ".corrupt-*")
	if err != nil {
		t.Fatalf("glob corrupt key backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("corrupt key backups = %d, want 1", len(backups))
	}
	backupData, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatalf("read corrupt key backup: %v", err)
	}
	if !bytes.Equal(backupData, corruptData) {
		t.Fatalf("backup content = %q, want %q", backupData, corruptData)
	}

	diskData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read replacement key: %v", err)
	}
	diskKey, err := hex.DecodeString(strings.TrimSpace(string(diskData)))
	if err != nil {
		t.Fatalf("decode replacement key: %v", err)
	}
	if !bytes.Equal(diskKey, key) {
		t.Fatal("replacement key on disk does not match returned key")
	}
}

func TestRotateAESKeyAt_SuccessMovesRetiredKeyToHistory(t *testing.T) {
	t.Setenv(secretsAuditLogEnv, "")
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")
	settingsPath := filepath.Join(dir, "settings.json")
	oldKey := testAESKey(1)
	writeTestAESKey(t, keyPath, oldKey)

	stored, err := aesEncryptWithKey("rotation-secret", oldKey)
	if err != nil {
		t.Fatalf("encrypt old secret: %v", err)
	}
	writeTestRotationConfig(t, settingsPath, stored)

	if err := rotateAESKeyAt(keyPath, dir); err != nil {
		t.Fatalf("rotate aes key: %v", err)
	}

	newKey := readTestAESKey(t, keyPath)
	if bytes.Equal(newKey, oldKey) {
		t.Fatal("rotation did not replace the current key")
	}
	history, err := loadAESKeyHistory(keyPath + ".history")
	if err != nil {
		t.Fatalf("load key history: %v", err)
	}
	if len(history) != 1 || !bytes.Equal(history[0], oldKey) {
		t.Fatalf("history = %x, want retired key %x", history, oldKey)
	}
	if _, err := os.Stat(keyPath + ".previous"); !os.IsNotExist(err) {
		t.Fatalf("previous key journal remains after success: %v", err)
	}

	rotated := readTestRotationSecret(t, settingsPath)
	plaintext, err := aesDecryptWithKeys(rotated, [][]byte{newKey})
	if err != nil {
		t.Fatalf("decrypt rotated secret with new key: %v", err)
	}
	if plaintext != "rotation-secret" {
		t.Fatalf("rotated plaintext = %q, want %q", plaintext, "rotation-secret")
	}
}

func TestRotateAESKeyAt_ResumesPreviousJournal(t *testing.T) {
	t.Setenv(secretsAuditLogEnv, "")
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")
	settingsPath := filepath.Join(dir, "settings.json")
	currentKey := testAESKey(11)
	previousKey := testAESKey(22)
	olderKey := testAESKey(33)
	writeTestAESKey(t, keyPath, currentKey)
	writeTestAESKey(t, keyPath+".previous", previousKey)
	if err := writeAESKeyHistory(keyPath+".history", [][]byte{olderKey}); err != nil {
		t.Fatalf("write key history: %v", err)
	}

	stored, err := aesEncryptWithKey("interrupted-secret", previousKey)
	if err != nil {
		t.Fatalf("encrypt interrupted secret: %v", err)
	}
	writeTestRotationConfig(t, settingsPath, stored)

	if err := rotateAESKeyAt(keyPath, dir); err != nil {
		t.Fatalf("resume aes key rotation: %v", err)
	}
	if got := readTestAESKey(t, keyPath); !bytes.Equal(got, currentKey) {
		t.Fatalf("resume changed current key: got %x, want %x", got, currentKey)
	}
	if _, err := os.Stat(keyPath + ".previous"); !os.IsNotExist(err) {
		t.Fatalf("previous key journal remains after recovery: %v", err)
	}
	history, err := loadAESKeyHistory(keyPath + ".history")
	if err != nil {
		t.Fatalf("load recovered history: %v", err)
	}
	if len(history) != 2 || !bytes.Equal(history[0], previousKey) || !bytes.Equal(history[1], olderKey) {
		t.Fatalf("recovered history = %x, want [%x %x]", history, previousKey, olderKey)
	}

	rotated := readTestRotationSecret(t, settingsPath)
	plaintext, err := aesDecryptWithKeys(rotated, [][]byte{currentKey})
	if err != nil {
		t.Fatalf("decrypt recovered secret with current key: %v", err)
	}
	if plaintext != "interrupted-secret" {
		t.Fatalf("recovered plaintext = %q, want %q", plaintext, "interrupted-secret")
	}
}

func TestLoadAESDecryptionKeysAt_DecryptsThreeGenerations(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")
	currentKey := testAESKey(41)
	previousGeneration := testAESKey(42)
	oldestGeneration := testAESKey(43)
	writeTestAESKey(t, keyPath, currentKey)
	if err := writeAESKeyHistory(keyPath+".history", [][]byte{previousGeneration, oldestGeneration}); err != nil {
		t.Fatalf("write key history: %v", err)
	}

	keys, err := loadAESDecryptionKeysAt(keyPath)
	if err != nil {
		t.Fatalf("load decryption keys: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("decryption key count = %d, want 3", len(keys))
	}
	wantKeys := [][]byte{currentKey, previousGeneration, oldestGeneration}
	for i := range wantKeys {
		if !bytes.Equal(keys[i], wantKeys[i]) {
			t.Fatalf("decryption key %d = %x, want %x", i, keys[i], wantKeys[i])
		}
		stored, encryptErr := aesEncryptWithKey(fmt.Sprintf("generation-%d", i), wantKeys[i])
		if encryptErr != nil {
			t.Fatalf("encrypt generation %d: %v", i, encryptErr)
		}
		plaintext, decryptErr := aesDecryptWithKeys(stored, keys)
		if decryptErr != nil {
			t.Fatalf("decrypt generation %d: %v", i, decryptErr)
		}
		if plaintext != fmt.Sprintf("generation-%d", i) {
			t.Fatalf("generation %d plaintext = %q", i, plaintext)
		}
	}
}

func TestSecretAudit_RecordsActionsWithoutValues(t *testing.T) {
	var audit bytes.Buffer
	restore := setSecretsAuditWriterForTest(&audit)
	defer restore()
	t.Setenv("PATH", t.TempDir())

	const key = "api-key\nwith-newline"
	const value = "must-never-appear-in-audit"
	if _, err := EncryptSecret(key, ""); err != nil {
		t.Fatalf("audit set operation: %v", err)
	}
	plaintext, err := DecryptSecret(key, secretPrefixPlain+value)
	if err != nil {
		t.Fatalf("audit get operation: %v", err)
	}
	if plaintext != value {
		t.Fatalf("decrypt plaintext = %q, want %q", plaintext, value)
	}
	if err := DeleteSecret(key); err != nil {
		t.Logf("platform delete result after audit: %v", err)
	}
	recordAESRotationAudit()

	got := audit.String()
	if strings.Contains(got, value) {
		t.Fatalf("audit log leaked secret value: %q", got)
	}
	for _, action := range []string{" set ", " get ", " delete ", " rotate-aes-key started\n"} {
		if !strings.Contains(got, action) {
			t.Errorf("audit log missing action %q: %q", action, got)
		}
	}
	quotedKey := fmt.Sprintf("%q", key)
	if count := strings.Count(got, quotedKey); count != 3 {
		t.Errorf("quoted audit key count = %d, want 3; log=%q", count, got)
	}
}

func TestSecretAudit_EnvironmentPathAndWrappedWriteError(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit", "secrets.log")
	t.Setenv(secretsAuditLogEnv, auditPath)
	if err := writeSecretAudit("set", "environment-key"); err != nil {
		t.Fatalf("write environment audit log: %v", err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read environment audit log: %v", err)
	}
	if !strings.Contains(string(data), `set "environment-key"`) {
		t.Fatalf("environment audit entry = %q", data)
	}

	sentinel := errors.New("audit sink failure")
	restore := setSecretsAuditWriterForTest(failingAuditWriter{err: sentinel})
	defer restore()
	if err := writeSecretAudit("get", "wrapped-key"); !errors.Is(err, sentinel) {
		t.Fatalf("audit write error = %v, want wrapped sentinel", err)
	}
}

func TestSecretAudit_LoadsActiveProfileSetting(t *testing.T) {
	t.Run("custom path", func(t *testing.T) {
		configHome := t.TempDir()
		root := filepath.Join(configHome, "koyori-ide")
		profileDir := filepath.Join(root, "profiles", "work")
		if err := os.MkdirAll(profileDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "profiles-state.json"), []byte(`{"activeProfile":"work"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(t.TempDir(), "secrets-audit.log")
		settings := fmt.Sprintf(`{"secretsAuditLog":%q}`, want)
		if err := os.WriteFile(filepath.Join(profileDir, "settings.json"), []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
		path, enabled, err := loadSecretsAuditLogSettingAt(configHome)
		if err != nil || !enabled || path != want {
			t.Fatalf("audit setting path=%q enabled=%v err=%v, want %q/true", path, enabled, err, want)
		}
	})

	t.Run("profile false overrides legacy true", func(t *testing.T) {
		configHome := t.TempDir()
		root := filepath.Join(configHome, "koyori-ide")
		profileDir := filepath.Join(root, "profiles", defaultProfileName)
		if err := os.MkdirAll(profileDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "settings.json"), []byte(`{"secretsAuditLog":false}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"secretsAuditLog":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		path, enabled, err := loadSecretsAuditLogSettingAt(configHome)
		if err != nil || enabled || path != "" {
			t.Fatalf("profile override path=%q enabled=%v err=%v, want disabled", path, enabled, err)
		}
	})
}

func TestSecretAudit_ConcurrentWritesAreSerialized(t *testing.T) {
	var audit bytes.Buffer
	restore := setSecretsAuditWriterForTest(&audit)
	defer restore()

	const writers = 32
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(index int) {
			defer wg.Done()
			errs <- writeSecretAudit("get", fmt.Sprintf("concurrent-key-%02d", index))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent audit write: %v", err)
		}
	}

	lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
	if len(lines) != writers {
		t.Fatalf("audit line count = %d, want %d; log=%q", len(lines), writers, audit.String())
	}
	for i := 0; i < writers; i++ {
		key := fmt.Sprintf("%q", fmt.Sprintf("concurrent-key-%02d", i))
		if count := strings.Count(audit.String(), key); count != 1 {
			t.Errorf("audit key %s count = %d, want 1", key, count)
		}
	}
}

func TestSerializedSecretAuditWriterCloseIsConcurrentSafe(t *testing.T) {
	writer := newSerializedSecretAuditWriter(&bytes.Buffer{})
	const writers = 64
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, err := writer.Write([]byte("audit\n"))
			errs <- err
		}()
	}
	closed := make(chan struct{})
	go func() {
		<-start
		writer.Close()
		close(closed)
	}()
	close(start)
	wg.Wait()
	<-closed
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("concurrent audit write error: %v", err)
		}
	}
	if _, err := writer.Write([]byte("after close")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write after close error = %v, want io.ErrClosedPipe", err)
	}
	writer.Close()
}

func TestAESKeyFileLock_DefaultTimeout(t *testing.T) {
	if aesKeyFileLockTimeout != 10*time.Second {
		t.Fatalf("aes key file lock timeout = %s, want 10s", aesKeyFileLockTimeout)
	}
}

func TestAESKeyFileLock_SerializesAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "secret.key.lock")
	readyPath := filepath.Join(dir, "ready")
	resultPath := filepath.Join(dir, "result")
	held, err := acquireAESKeyFileLock(lockPath)
	if err != nil {
		t.Fatalf("acquire parent lock: %v", err)
	}
	released := false
	defer func() {
		if !released {
			if releaseErr := held.release(); releaseErr != nil {
				t.Errorf("release parent lock during cleanup: %v", releaseErr)
			}
		}
	}()

	cmd := aesKeyFileLockHelperCommand(lockPath, readyPath, resultPath, "2s")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock helper: %v", err)
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			t.Logf("kill lock helper during cleanup: %v", killErr)
		}
		if waitErr := cmd.Wait(); waitErr != nil {
			t.Logf("wait for killed lock helper: %v", waitErr)
		}
	}()

	if err := waitForAESLockTestFile(readyPath, 2*time.Second); err != nil {
		t.Fatalf("wait for lock helper readiness: %v\n%s", err, output.String())
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(resultPath); err == nil {
		data, readErr := os.ReadFile(resultPath)
		if readErr != nil {
			t.Fatalf("read premature lock helper result: %v", readErr)
		}
		t.Fatalf("helper acquired lock before parent released it: %s", data)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat lock helper result: %v", err)
	}
	if err := held.release(); err != nil {
		t.Fatalf("release parent lock: %v", err)
	}
	released = true
	if err := cmd.Wait(); err != nil {
		finished = true
		t.Fatalf("lock helper failed: %v\n%s", err, output.String())
	}
	finished = true
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read lock helper result: %v", err)
	}
	if string(data) != "acquired" {
		t.Fatalf("lock helper result = %q, want acquired", data)
	}
}

func TestAESKeyFileLock_TimesOutAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "secret.key.lock")
	readyPath := filepath.Join(dir, "ready")
	resultPath := filepath.Join(dir, "result")
	held, err := acquireAESKeyFileLock(lockPath)
	if err != nil {
		t.Fatalf("acquire parent lock: %v", err)
	}
	defer func() {
		if releaseErr := held.release(); releaseErr != nil {
			t.Errorf("release parent lock: %v", releaseErr)
		}
	}()

	cmd := aesKeyFileLockHelperCommand(lockPath, readyPath, resultPath, "150ms")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	started := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("timeout lock helper failed: %v\n%s", err, output.String())
	}
	elapsed := time.Since(started)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("lock helper timed out too early after %s", elapsed)
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read timeout lock helper result: %v", err)
	}
	if !strings.HasPrefix(string(data), "error: timed out acquiring aes key file lock after 150ms:") {
		t.Fatalf("timeout lock helper result = %q", data)
	}
}

func TestAESKeyFileLock_HelperProcess(t *testing.T) {
	if os.Getenv("KOYORI_IDE_AES_LOCK_HELPER") != "1" {
		return
	}
	lockPath := os.Getenv("KOYORI_IDE_AES_LOCK_PATH")
	readyPath := os.Getenv("KOYORI_IDE_AES_LOCK_READY_PATH")
	resultPath := os.Getenv("KOYORI_IDE_AES_LOCK_RESULT_PATH")
	timeout, err := time.ParseDuration(os.Getenv("KOYORI_IDE_AES_LOCK_TIMEOUT"))
	if err != nil {
		t.Fatalf("parse lock helper timeout: %v", err)
	}
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		t.Fatalf("write lock helper ready file: %v", err)
	}
	lock, err := acquireAESKeyFileLockWithTimeout(lockPath, timeout)
	result := "acquired"
	if err != nil {
		result = "error: " + err.Error()
	}
	if writeErr := os.WriteFile(resultPath, []byte(result), 0o600); writeErr != nil {
		t.Fatalf("write lock helper result: %v", writeErr)
	}
	if err != nil {
		return
	}
	if err := lock.release(); err != nil {
		t.Fatalf("release lock helper lock: %v", err)
	}
}

type failingAuditWriter struct {
	err error
}

func (w failingAuditWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func testAESKey(seed byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

func writeTestAESKey(t *testing.T, path string, key []byte) {
	t.Helper()
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		t.Fatalf("write aes key %q: %v", path, err)
	}
}

func readTestAESKey(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read aes key %q: %v", path, err)
	}
	key, err := decodeAESKey(data)
	if err != nil {
		t.Fatalf("decode aes key %q: %v", path, err)
	}
	return key
}

func writeTestRotationConfig(t *testing.T, path, stored string) {
	t.Helper()
	data, err := json.MarshalIndent(map[string]any{
		"apiKey": stored,
		"nested": map[string]any{"unchanged": "plain-value"},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal rotation config: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write rotation config: %v", err)
	}
}

func readTestRotationSecret(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rotation config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse rotation config: %v", err)
	}
	stored, ok := config["apiKey"].(string)
	if !ok {
		t.Fatalf("rotation config apiKey = %#v, want string", config["apiKey"])
	}
	return stored
}

func aesKeyFileLockHelperCommand(lockPath, readyPath, resultPath, timeout string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestAESKeyFileLock_HelperProcess$")
	cmd.Env = append(os.Environ(),
		"KOYORI_IDE_AES_LOCK_HELPER=1",
		"KOYORI_IDE_AES_LOCK_PATH="+lockPath,
		"KOYORI_IDE_AES_LOCK_READY_PATH="+readyPath,
		"KOYORI_IDE_AES_LOCK_RESULT_PATH="+resultPath,
		"KOYORI_IDE_AES_LOCK_TIMEOUT="+timeout,
	)
	return cmd
}

func waitForAESLockTestFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat %q: %w", path, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %q", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
