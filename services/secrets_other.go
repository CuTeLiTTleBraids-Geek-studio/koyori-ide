//go:build !windows && !darwin && !linux

package services

import (
	"fmt"
	"strings"
)

// platformEncryptSecret encrypts plaintext using AES-256-GCM (the AES
// fallback path). On BSD, illumos, and other non-mainstream platforms where
// we don't have a native keyring CLI wrapper, the per-install AES key file
// is the only available secret store.
//
// On Windows, DPAPI is used instead (see secrets_windows.go). On macOS and
// Linux, the native Keychain/libsecret CLI is tried first with AES as the
// fallback (see secrets_darwin.go / secrets_linux.go).
// L-4: account 参数在此平台不使用(AES fallback 用 per-install 密钥文件),
// 接受该参数以保持跨平台统一接口。
func platformEncryptSecret(account, plaintext string) (string, error) {
	_ = account
	return aesEncrypt(plaintext)
}

// platformDecryptSecret dispatches based on the prefix. On this platform
// only "aes:" values can be decrypted (AES is the only cross-platform
// method). Foreign "dpapi:" and "keyring:" values return an error (N-49)
// since this platform has no DPAPI or native keyring CLI.
// L-4: account 参数在此平台不使用(AES 是唯一可用方法)。
func platformDecryptSecret(account, stored string) (string, error) {
	_ = account
	if strings.HasPrefix(stored, secretPrefixAES) {
		return aesDecrypt(stored)
	}
	if strings.HasPrefix(stored, secretPrefixDPAPI) || strings.HasPrefix(stored, secretPrefixKeyring) {
		// N-49: Foreign prefix — cannot decrypt on this platform.
		return "", fmt.Errorf("secret was stored on another platform (%s) and cannot be accessed here; please re-enter the API key", SecretMethod(stored))
	}
	// Unrecognized — return as-is (legacy plaintext).
	return stored, nil
}

// platformListSecrets returns an empty list on platforms without a native
// keyring. AES-encrypted secrets live inside settings.json, not a separate
// keyring, so there are no orphan entries to discover.
func platformListSecrets() ([]SecretInfo, error) {
	return nil, nil
}

// platformDeleteSecret is a no-op on platforms without a native keyring.
// AES secrets are removed by clearing the AIApiKey field in settings.json.
func platformDeleteSecret(account string) error {
	return nil
}
