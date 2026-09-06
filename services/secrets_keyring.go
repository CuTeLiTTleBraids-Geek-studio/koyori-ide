package services

import "strings"

// Shared keyring constants used by the darwin (macOS Keychain) and linux
// (libsecret) platform files. Defined here — without a build tag — so both
// platform files can reference them without duplication. On platforms where
// the keyring CLI path is never taken (windows, BSD), these constants exist
// harmlessly as unused package-level constants (Go allows unused constants).
const (
	// keyringServiceName is the namespace under which koyori-ide secrets are
	// stored in the platform keyring. On macOS this is the Keychain "service"
	// name; on Linux this becomes a libsecret attribute value. 仅被
	// GOOS=linux/darwin 平台文件引用；Windows 视角显式登记豁免。
	keyringServiceName = "koyori-ide" //nolint:unused // 调用方在 GOOS=linux/darwin 平台文件中

	// keyringAccount is the default account/label for the AI API key.
	//
	// L-4: 此前 keyringAccount 是硬编码常量,EncryptSecret/DecryptSecret 内部
	// 直接使用它,限制了扩展性。现在 account 已参数化(EncryptSecret(account,
	// plaintext) / DecryptSecret(account, stored)),调用者传入 account。此常量
	// 保留作为默认值:现有调用者传 keyringAccount 以保持向后兼容;
	// platformListSecrets 仍用它查找默认条目。新功能可传入自定义 account 名
	// 以存储不同类型的密钥(如 IM BotToken、MCP env 等)。
	keyringAccount = "ai-api-key"
)

// trimCLIOutputNewline removes the single newline written by keyring CLIs
// after a successful lookup. Any whitespace that belongs to the secret,
// including additional trailing newlines, remains intact.
//
// 仅被 GOOS=linux/darwin 的 keyring CLI 路径（secrets_linux.go /
// secrets_darwin.go）调用；Windows 的 lint 视角看不到调用方，显式登记豁免。
//
//nolint:unused // 调用方在 GOOS=linux/darwin 的平台文件中
func trimCLIOutputNewline(value string) string {
	return strings.TrimSuffix(value, "\n")
}
