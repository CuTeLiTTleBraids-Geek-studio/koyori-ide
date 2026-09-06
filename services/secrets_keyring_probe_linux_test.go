//go:build linux

package services

// keyringProbeStore / keyringProbeLoad 把 linux 专属的 libsecret CLI 包装
// 成跨 GOOS 探测测试使用的统一签名。仅在探测测试中使用，绕过
// secretsTestAESOnly 强制回退（否则测试永远不会触碰真实 keyring）。
func keyringProbeStore(account, plaintext string) (string, error) {
	return libsecretStore(account, plaintext)
}

func keyringProbeLoad(account, markerB64 string) (string, error) {
	return libsecretLoad(account, markerB64)
}
