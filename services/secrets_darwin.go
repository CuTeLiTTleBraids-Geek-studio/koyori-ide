//go:build darwin

package services

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

const keychainCommandTimeout = 10 * time.Second

// macosKeychainAvailable reports whether the `security` CLI is on PATH.
// Used to decide whether to try the Keychain path or fall straight through
// to the AES fallback.
func macosKeychainAvailable() bool {
	_, err := exec.LookPath("security")
	return err == nil
}

// keychainStore stores the plaintext in the macOS Keychain via the `security`
// CLI. Returns the prefixed marker ("keyring:" + base64-label) on success.
// The actual plaintext is NOT embedded in the returned value — only a marker
// indicating "look this up in the Keychain" is stored. This keeps the value
// in settings.json opaque even if the file is leaked.
//
// L-4: account 参数化,替代原先硬编码的 keyringAccount 常量。
func keychainStore(account, plaintext string) (string, error) {
	// Update in place so a failed store does not delete the previous secret.
	// Keep -w last: without an inline value, security reads the password from stdin.
	ctx, cancel := context.WithTimeout(context.Background(), keychainCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", keychainStoreCommandArgs(account)...)
	cmd.Stdin = strings.NewReader(plaintext)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("keychain: add-generic-password timed out: %w", ctx.Err())
		}
		return "", fmt.Errorf("keychain: add-generic-password failed: %w", err)
	}
	// Store a marker rather than the secret itself. The marker is just the
	// account name base64-encoded so it's a stable opaque token.
	marker := base64.StdEncoding.EncodeToString([]byte(account))
	return secretPrefixKeyring + marker, nil
}

func keychainStoreCommandArgs(account string) []string {
	return []string{
		"add-generic-password",
		"-U",
		"-s", keyringServiceName,
		"-a", account,
		"-w", // must be last so security reads the password from stdin
	}
}

// keychainLoad retrieves the plaintext from the macOS Keychain via the
// `security find-generic-password -w` CLI. The -w flag writes the password
// directly to stdout (without a "password: " prefix), which correctly handles
// passwords containing newlines (H-9 fix — previously used -g which wrote to
// stderr with a prefix and could truncate multi-line passwords).
//
// L-4: account 参数化,替代原先硬编码的 keyringAccount 常量。marker 仍编码
// account 名,但查找时直接用传入的 account(调用者必须与加密时一致)。
func keychainLoad(account, markerB64 string) (string, error) {
	_ = markerB64 // marker 编码了 account,但查找时直接用传入的 account
	ctx, cancel := context.WithTimeout(context.Background(), keychainCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-s", keyringServiceName,
		"-a", account,
		"-w", // H-9: 直接输出密码到 stdout，避免换行截断
	)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("keychain: find-generic-password timed out: %w", ctx.Err())
		}
		return "", fmt.Errorf("keychain: find-generic-password failed: %w", err)
	}
	return trimCLIOutputNewline(stdout.String()), nil
}

// platformEncryptSecret tries the macOS Keychain first, falling back to the
// per-install AES key file when the `security` CLI is unavailable or the
// Keychain rejects the store (e.g. user denies access). This keeps secrets
// usable in headless / CI environments where the Keychain is locked.
// L-4: account 参数化,传递给 keychainStore 用于 Keychain 条目标签。
// AES fallback 路径不使用 account。
func platformEncryptSecret(account, plaintext string) (string, error) {
	if !secretsTestAESOnly && macosKeychainAvailable() {
		stored, err := keychainStore(account, plaintext)
		if err == nil {
			return stored, nil
		}
		slog.Debug("keychain store failed; using aes fallback", "err", err)
		// Fall through to AES on error.
	}
	return aesEncrypt(plaintext)
}

// platformDecryptSecret dispatches based on the prefix. "keyring:" values
// are loaded from the Keychain (with a CLI availability check — N-49);
// "aes:" values use the cross-platform AES fallback; foreign "dpapi:" values
// (created on Windows) return an error since macOS cannot invoke DPAPI.
// L-4: account 参数化,传递给 keychainLoad 用于 Keychain 条目查找。
// AES/DPAPI 路径不使用 account。
func platformDecryptSecret(account, stored string) (string, error) {
	if strings.HasPrefix(stored, secretPrefixKeyring) {
		// N-49: Check CLI availability before calling the Keychain. In
		// headless / CI environments the `security` CLI may be missing or
		// the Keychain locked. Returning an error lets the caller prompt
		// the user to unlock the Keychain or re-enter the API key, instead
		// of failing with an opaque exec error.
		if !macosKeychainAvailable() {
			return "", fmt.Errorf("keyring: macOS `security` CLI is unavailable or the Keychain is locked; please unlock the Keychain or re-enter the API key")
		}
		markerB64 := strings.TrimPrefix(stored, secretPrefixKeyring)
		return keychainLoad(account, markerB64)
	}
	if strings.HasPrefix(stored, secretPrefixAES) {
		return aesDecrypt(stored)
	}
	if strings.HasPrefix(stored, secretPrefixDPAPI) {
		// N-49: Foreign DPAPI value — Windows-only, cannot decrypt on macOS.
		return "", fmt.Errorf("dpapi: secret was stored on Windows and cannot be accessed on macOS; please re-enter the API key")
	}
	// Unrecognized — return as-is (legacy plaintext).
	return stored, nil
}

// platformListSecrets checks the macOS Keychain for koyori-ide entries.
// Returns at most one entry (the fixed-account AI API key) when the
// `security` CLI is available and the entry exists. Used by the settings UI
// to show users what's in their Keychain so they can clean up orphans (N-49).
func platformListSecrets() ([]SecretInfo, error) {
	if !macosKeychainAvailable() {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), keychainCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "security", "find-generic-password",
		"-s", keyringServiceName,
		"-a", keyringAccount,
	).CombinedOutput()
	if err == nil {
		return []SecretInfo{{Account: keyringAccount, Method: "keyring", Stored: true}}, nil
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("keychain: find-generic-password timed out: %w", ctx.Err())
	}
	if securityItemNotFound(err, output) {
		return nil, nil
	}
	return nil, fmt.Errorf("keychain: find-generic-password failed: %w", err)
}

// platformDeleteSecret removes the secret with the given account from the
// macOS Keychain. Idempotent — returns nil if the entry doesn't exist.
func platformDeleteSecret(account string) error {
	if !macosKeychainAvailable() {
		return nil
	}
	return deleteKeychainEntry(account)
}

func deleteKeychainEntry(account string) error {
	ctx, cancel := context.WithTimeout(context.Background(), keychainCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "security", "delete-generic-password",
		"-s", keyringServiceName,
		"-a", account,
	).CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("keychain: delete-generic-password timed out: %w", ctx.Err())
	}
	if securityItemNotFound(err, output) {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("keychain: delete-generic-password failed: %w", err)
	}
	return fmt.Errorf("keychain: delete-generic-password failed: %w: %s", err, detail)
}

func securityItemNotFound(err error, output []byte) bool {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	if exitErr.ExitCode() == 44 { // errSecItemNotFound
		return true
	}
	detail := strings.ToLower(string(output))
	return strings.Contains(detail, "could not be found") ||
		strings.Contains(detail, "item not found") ||
		strings.Contains(detail, "errsecitemnotfound")
}
