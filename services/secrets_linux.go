//go:build linux

package services

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

const libsecretCommandTimeout = 10 * time.Second

// libsecretAvailable reports whether the `secret-tool` CLI is on PATH.
// `secret-tool` is part of the libsecret-tools package on Debian/Ubuntu and
// libsecret on Fedora. It speaks the Secret Service API over D-Bus, so any
// compatible backend (GNOME Keyring, KWallet via the ssue proxy, KeePassXC)
// will work.
func libsecretAvailable() bool {
	_, err := exec.LookPath("secret-tool")
	return err == nil
}

// libsecretStore stores the plaintext in the user's secret service via
// `secret-tool store`. The secret is read from stdin; the attributes
// (koyori-ide:service, koyori-ide:account) are used to look it up later.
// Returns the prefixed marker ("keyring:" + base64-label) on success.
//
// L-4: account 参数化,替代原先硬编码的 keyringAccount 常量。
func libsecretStore(account, plaintext string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), libsecretCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "secret-tool", "store",
		"--label", keyringServiceName+"/"+account,
		"koyori-ide:service", keyringServiceName,
		"koyori-ide:account", account,
	)
	cmd.Stdin = strings.NewReader(plaintext)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("libsecret: store timed out: %w", ctx.Err())
		}
		return "", fmt.Errorf("libsecret: store failed: %w", err)
	}
	marker := base64.StdEncoding.EncodeToString([]byte(account))
	return secretPrefixKeyring + marker, nil
}

// libsecretLoad retrieves the plaintext from the user's secret service via
// `secret-tool lookup`. The secret is written to stdout.
//
// L-4: account 参数化,替代原先硬编码的 keyringAccount 常量。调用者必须
// 传入与加密时相同的 account。
func libsecretLoad(account, markerB64 string) (string, error) {
	_ = markerB64 // marker 编码了 account,但查找时直接用传入的 account
	ctx, cancel := context.WithTimeout(context.Background(), libsecretCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "secret-tool", "lookup",
		"koyori-ide:service", keyringServiceName,
		"koyori-ide:account", account,
	)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("libsecret: lookup timed out: %w", ctx.Err())
		}
		return "", fmt.Errorf("libsecret: lookup failed: %w", err)
	}
	out := trimCLIOutputNewline(stdout.String())
	if out == "" {
		return "", fmt.Errorf("libsecret: no secret found for service=%q account=%q",
			keyringServiceName, account)
	}
	return out, nil
}

// platformEncryptSecret tries libsecret first, falling back to the per-install
// AES key file when `secret-tool` is unavailable or the secret service is not
// running (common in headless / CI environments). This keeps secrets usable
// across desktop and server Linux.
// L-4: account 参数化,传递给 libsecretStore 用于 secret service 条目属性。
// AES fallback 路径不使用 account。
func platformEncryptSecret(account, plaintext string) (string, error) {
	if !secretsTestAESOnly && libsecretAvailable() {
		stored, err := libsecretStore(account, plaintext)
		if err == nil {
			return stored, nil
		}
		slog.Debug("libsecret store failed; using aes fallback", "err", err)
		// Fall through to AES on error.
	}
	return aesEncrypt(plaintext)
}

// platformDecryptSecret dispatches based on the prefix. "keyring:" values
// are loaded from libsecret (with a CLI availability check — N-49); "aes:"
// values use the cross-platform AES fallback; foreign "dpapi:" values
// (created on Windows) return an error since Linux cannot invoke DPAPI.
// L-4: account 参数化,传递给 libsecretLoad 用于 secret service 条目查找。
// AES/DPAPI 路径不使用 account。
func platformDecryptSecret(account, stored string) (string, error) {
	if strings.HasPrefix(stored, secretPrefixKeyring) {
		// N-49: Check CLI availability before calling libsecret. In headless
		// / CI environments `secret-tool` may be missing or the secret
		// service not running. Returning an error lets the caller prompt
		// the user to start the service or re-enter the API key.
		if !libsecretAvailable() {
			return "", fmt.Errorf("keyring: `secret-tool` CLI is unavailable or the secret service is not running; please start the secret service or re-enter the API key")
		}
		markerB64 := strings.TrimPrefix(stored, secretPrefixKeyring)
		return libsecretLoad(account, markerB64)
	}
	if strings.HasPrefix(stored, secretPrefixAES) {
		return aesDecrypt(stored)
	}
	if strings.HasPrefix(stored, secretPrefixDPAPI) {
		// N-49: Foreign DPAPI value — Windows-only, cannot decrypt on Linux.
		return "", fmt.Errorf("dpapi: secret was stored on Windows and cannot be accessed on Linux; please re-enter the API key")
	}
	// Unrecognized — return as-is (legacy plaintext).
	return stored, nil
}

// platformListSecrets checks libsecret for koyori-ide entries. Returns at
// most one entry (the fixed-account AI API key) when `secret-tool` is
// available and the entry exists. Used by the settings UI to show users
// what's in their secret service so they can clean up orphans (N-49).
func platformListSecrets() ([]SecretInfo, error) {
	if !libsecretAvailable() {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), libsecretCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "secret-tool", "lookup",
		"koyori-ide:service", keyringServiceName,
		"koyori-ide:account", keyringAccount,
	)
	var stderr strings.Builder
	cmd.Stdout = io.Discard // lookup prints the secret; ListSecrets only needs existence.
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return []SecretInfo{{Account: keyringAccount, Method: "keyring", Stored: true}}, nil
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("libsecret: lookup timed out: %w", ctx.Err())
	}
	if libsecretItemNotFound(err, []byte(stderr.String())) {
		return nil, nil
	}
	return nil, fmt.Errorf("libsecret: lookup failed: %w", err)
}

// platformDeleteSecret removes the secret with the given account from the
// secret service via `secret-tool clear`. Idempotent — returns nil even if
// the entry doesn't exist.
func platformDeleteSecret(account string) error {
	if !libsecretAvailable() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), libsecretCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "secret-tool", "clear",
		"koyori-ide:service", keyringServiceName,
		"koyori-ide:account", account,
	).CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("libsecret: clear timed out: %w", ctx.Err())
	}
	if libsecretItemNotFound(err, output) {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("libsecret: clear failed: %w", err)
	}
	return fmt.Errorf("libsecret: clear failed: %w: %s", err, detail)
}

func libsecretItemNotFound(err error, output []byte) bool {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	detail := strings.ToLower(strings.TrimSpace(string(output)))
	if strings.Contains(detail, "not found") ||
		strings.Contains(detail, "no matching") ||
		strings.Contains(detail, "no such item") {
		return true
	}
	// secret-tool commonly exits 1 without output when no item matches.
	return exitErr.ExitCode() == 1 && detail == ""
}
