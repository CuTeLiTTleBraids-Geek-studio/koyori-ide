package services

import (
	"os"
	"testing"
)

// TestMain 为 services 包测试设置全局前提：
//
//  1. 强制走 AES fallback（secretsTestAESOnly），单元测试永远不读写真实
//     用户 keychain（此前 macOS/Linux 上的测试会污染开发者本机的
//     Keychain/libsecret，且在 CI 的锁定 Keychain 上行为不确定——
//     2026-08-30 GH macos runner 上 keychainLoad 返回空值导致
//     EncryptDecryptSecret 与 AI provider 注水两类测试失败）。
//     keyring 路径的集成覆盖由 secrets_keyring_probe_test.go 以
//     "平台可用性探测 + 跳过" 的方式提供。
func TestMain(m *testing.M) {
	secretsTestAESOnly = true
	code := m.Run()
	secretsTestAESOnly = false
	os.Exit(code)
}
