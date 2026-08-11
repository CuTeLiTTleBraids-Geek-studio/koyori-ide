//go:build windows

package services

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"syscall"
	"unsafe"
)

var (
	crypt32DLL         = syscall.NewLazyDLL("crypt32.dll")
	kernel32DLL        = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtect   = crypt32DLL.NewProc("CryptProtectData")
	procCryptUnprotect = crypt32DLL.NewProc("CryptUnprotectData")
	procLocalFree      = kernel32DLL.NewProc("LocalFree")
)

// dpapiEntropy 是 H-8 修复：为 DPAPI 加密提供应用专属熵。
// 没有这个熵，同用户的任何其他进程都可以调用 CryptUnprotectData
// 解密由 Koyori IDE 加密的密钥。有了这个熵，只有传入相同熵的进程
// （即 Koyori IDE 自身）才能解密。
var dpapiEntropy = []byte("koyori-ide-secret-v1")

// dataBlob maps to the Windows DATA_BLOB struct used by DPAPI.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

// platformEncryptSecret encrypts plaintext using Windows DPAPI
// (CryptProtectData). The result is base64-encoded and prefixed with "dpapi:".
// DPAPI encryption is machine-bound: the ciphertext can only be decrypted on
// the same Windows user account on the same machine.
//
// H-8: 传入应用专属熵 dpapiEntropy，使同用户的其他进程无法解密。
//
// L-4: account 参数在 Windows DPAPI 路径中不使用(DPAPI 用应用专属熵
// 而非账户标签),接受该参数以保持跨平台统一接口。
func platformEncryptSecret(account, plaintext string) (string, error) {
	_ = account // DPAPI 不使用 account
	bytes := []byte(plaintext)
	if len(bytes) == 0 {
		return "", nil
	}
	// H-8: 限制明文大小，防止超大输入
	if len(bytes) > maxSecretPlaintextSize {
		return "", fmt.Errorf("dpapi: plaintext exceeds max size %d bytes", maxSecretPlaintextSize)
	}
	blobIn := dataBlob{
		cbData: uint32(len(bytes)),
		pbData: &bytes[0],
	}
	// H-8: 用应用专属熵作为 CryptProtectData 的第 3 参数
	entropyBlob := dataBlob{
		cbData: uint32(len(dpapiEntropy)),
		pbData: &dpapiEntropy[0],
	}
	var blobOut dataBlob
	r, _, err := procCryptProtect.Call(
		uintptr(unsafe.Pointer(&blobIn)),
		0,                                     // description (optional)
		uintptr(unsafe.Pointer(&entropyBlob)), // H-8: reserved → entropy
		0,                                     // prompt
		0,                                     // prompt struct
		0,                                     // flags
		uintptr(unsafe.Pointer(&blobOut)),
	)
	if r == 0 {
		return "", fmt.Errorf("dpapi: CryptProtectData failed: %w", err)
	}
	defer freeLocalBuffer(blobOut.pbData)

	// H-8: 对 cbData 做上限校验再切片
	if blobOut.cbData > maxSecretPlaintextSize*2 {
		return "", fmt.Errorf("dpapi: encrypted output size %d exceeds safety limit", blobOut.cbData)
	}
	out := make([]byte, blobOut.cbData)
	copy(out, (*[1 << 30]byte)(unsafe.Pointer(blobOut.pbData))[:blobOut.cbData])
	return secretPrefixDPAPI + base64.StdEncoding.EncodeToString(out), nil
}

// platformDecryptSecret decrypts a prefixed value using Windows DPAPI
// (CryptUnprotectData) for "dpapi:" values, or AES-256-GCM for "aes:" values
// (N-49: AES is cross-platform, so a value encrypted on macOS/Linux can be
// decrypted on Windows if the per-install AES key file was copied along with
// settings.json). "keyring:" values are markers pointing to macOS Keychain
// or Linux libsecret entries, which Windows cannot access — an error is
// returned so the caller can prompt the user to re-enter the key.
// L-4: account 参数在 Windows 解密路径中不使用(DPAPI 用熵,AES 用
// per-install 密钥文件),接受该参数以保持跨平台统一接口。
func platformDecryptSecret(account, stored string) (string, error) {
	_ = account // DPAPI/AES 解密不使用 account
	if strings.HasPrefix(stored, secretPrefixAES) {
		// N-49: AES is cross-platform — try to decrypt values created on
		// macOS/Linux. This succeeds when the per-install AES key file
		// (~/.config/koyori-ide/secret.key) was migrated alongside
		// settings.json.
		return aesDecrypt(stored)
	}
	if strings.HasPrefix(stored, secretPrefixKeyring) {
		// N-49: "keyring:" is a marker for a macOS Keychain / Linux libsecret
		// entry. Windows cannot access these — return an error so the caller
		// can prompt the user to re-enter the API key.
		return "", fmt.Errorf("keyring: secret was stored in macOS Keychain or Linux libsecret and cannot be accessed on Windows; please re-enter the API key")
	}
	if !strings.HasPrefix(stored, secretPrefixDPAPI) {
		// Not a recognized encrypted prefix — return as-is (legacy plaintext
		// or foreign format the caller can surface).
		return stored, nil
	}
	b64 := strings.TrimPrefix(stored, secretPrefixDPAPI)
	encrypted, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("dpapi: invalid base64: %w", err)
	}
	if len(encrypted) == 0 {
		return "", nil
	}
	blobIn := dataBlob{
		cbData: uint32(len(encrypted)),
		pbData: &encrypted[0],
	}
	// H-8: 用应用专属熵作为 CryptUnprotectData 的第 3 参数
	entropyBlob := dataBlob{
		cbData: uint32(len(dpapiEntropy)),
		pbData: &dpapiEntropy[0],
	}
	var blobOut dataBlob
	r, _, err := procCryptUnprotect.Call(
		uintptr(unsafe.Pointer(&blobIn)),
		0,                                     // description out (optional)
		uintptr(unsafe.Pointer(&entropyBlob)), // H-8: reserved → entropy
		0,                                     // prompt
		0,                                     // prompt struct
		0,                                     // flags
		uintptr(unsafe.Pointer(&blobOut)),
	)
	if r == 0 {
		return "", fmt.Errorf("dpapi: CryptUnprotectData failed: %w", err)
	}
	defer freeLocalBuffer(blobOut.pbData)

	// H-8: 对 cbData 做上限校验再切片
	if blobOut.cbData > maxSecretPlaintextSize*2 {
		return "", fmt.Errorf("dpapi: decrypted output size %d exceeds safety limit", blobOut.cbData)
	}
	out := make([]byte, blobOut.cbData)
	copy(out, (*[1 << 30]byte)(unsafe.Pointer(blobOut.pbData))[:blobOut.cbData])
	return string(out), nil
}

func freeLocalBuffer(ptr *byte) {
	if ptr == nil {
		return
	}
	result, _, err := procLocalFree.Call(uintptr(unsafe.Pointer(ptr)))
	if result != 0 {
		slog.Debug("secrets: LocalFree failed", "err", err)
	}
}

// platformListSecrets returns an empty list on Windows. DPAPI-encrypted
// secrets live as blobs inside settings.json, not in a separate keyring, so
// there are no orphan entries to discover. The settings UI uses
// GetAPIKeyStorageMethod() to inspect the settings.json entry instead.
func platformListSecrets() ([]SecretInfo, error) {
	return nil, nil
}

// platformDeleteSecret is a no-op on Windows. DPAPI secrets are removed by
// clearing the AIApiKey field in settings.json (SaveSettings with an empty
// key). There is no separate keyring entry to delete.
func platformDeleteSecret(account string) error {
	return nil
}
