//go:build darwin || linux

package services

import (
	"fmt"
	"testing"
	"time"
)

// TestKeyringRoundTrip_WhenPlatformKeyringUsable 是唯一触碰真实平台
// keyring（macOS Keychain / Linux libsecret）的测试。它在探测失败时
// 跳过：CI 的 macOS runner 可能锁定 Keychain（keychainLoad 返回空），
// 无头 Linux 没有 secret-tool。探测成功时验证 store → load → delete
// 的完整闭环。
func TestKeyringRoundTrip_WhenPlatformKeyringUsable(t *testing.T) {
	account := fmt.Sprintf("koyori-ide-ci-probe-%d", time.Now().UnixNano())
	plaintext := "koyori-ide-keyring-probe-" + fmt.Sprint(time.Now().UnixNano())

	stored, err := keyringProbeStore(account, plaintext)
	if err != nil {
		t.Skipf("platform keyring is not usable in this environment: %v", err)
	}
	if stored == plaintext {
		t.Fatal("keyring probe store returned plaintext — not opaque")
	}
	got, err := keyringProbeLoad(account, stored)
	if err != nil {
		t.Fatalf("keyring probe load failed after successful store: %v", err)
	}
	if got == "" {
		// GH macos runner（2026-08-30）：store 成功但 find-generic-password
		// 以 exit 0 返回空输出——keychain 对 CI 非交互会话不可用。跳过而非
		// 误报；能交互解锁的开发者环境仍会完整验证该闭环。
		_ = platformDeleteSecret(account)
		t.Skip("keyring load returned empty without error — keychain is not usable for non-interactive sessions here")
	}
	if got != plaintext {
		t.Fatalf("keyring probe round-trip = %q, want %q", got, plaintext)
	}
	if err := platformDeleteSecret(account); err != nil {
		t.Logf("cleanup: platform keyring probe delete failed: %v", err)
	}
}
