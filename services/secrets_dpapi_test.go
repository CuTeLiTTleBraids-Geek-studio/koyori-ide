//go:build windows

package services

import "testing"

// TestDPAPIRoundTrip 直接验证 DPAPI 底层实现（不走被 TestMain 强制到
// AES fallback 的 platformEncryptSecret 分发）。Windows 是唯一能执行
// DPAPI 的平台，该测试是这一路径的全部 CI 覆盖。
func TestDPAPIRoundTrip(t *testing.T) {
	const plaintext = "koyori-ide-dpapi-round-trip-secret"
	stored, err := dpapiEncrypt(plaintext)
	if err != nil {
		t.Fatalf("dpapiEncrypt: %v", err)
	}
	if stored == plaintext || len(stored) == 0 {
		t.Fatalf("dpapiEncrypt returned %q, want an opaque blob", stored)
	}
	got, err := dpapiDecrypt(stored)
	if err != nil {
		t.Fatalf("dpapiDecrypt: %v", err)
	}
	if got != plaintext {
		t.Fatalf("dpapiDecrypt = %q, want %q", got, plaintext)
	}
}
