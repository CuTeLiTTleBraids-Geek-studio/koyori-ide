//go:build darwin

package services

// keyringProbeStore / keyringProbeLoad 把 darwin 专属的 keychain CLI 包装
// 成跨 GOOS 探测测试使用的统一签名。仅在探测测试中使用，绕过
// secretsTestAESOnly 强制回退（否则测试永远不会触碰真实 keyring）。
func keyringProbeStore(account, plaintext string) (string, error) {
	return keychainStore(account, plaintext)
}

func keyringProbeLoad(account, markerB64 string) (string, error) {
	return keychainLoad(account, markerB64)
}
