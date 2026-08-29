package services

// extension_security_service_test.go — G-VSC-03 / G-SEC-12 tests.
//
// Covers:
//   - Classification logic (Trusted / Reviewed / Restricted)
//   - SHA-256 integrity checks (correct hash passes, wrong hash fails)
//   - Blacklist checking (built-in + user-added)
//   - Restricted extensions cannot be enabled without explicit approval
//   - Blacklisted extensions are blocked from installation
//   - Extensions without an integrity check cannot be enabled
//   - New installs default to disabled + pending review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestExtensionSecurityService(t *testing.T) (*ExtensionSecurityService, string) {
	t.Helper()
	dir := t.TempDir()
	return NewExtensionSecurityService(dir), dir
}

func writeVSIX(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write vsix: %v", err)
	}
	return path
}

// sha256HexStr is a thin string wrapper over the package-level sha256Hex
// (defined in marketplace_service.go) which takes []byte.
func sha256HexStr(s string) string {
	return sha256Hex([]byte(s))
}

// --- Classification logic ---

func TestExtensionSecurity_ClassifyExtension_Trusted(t *testing.T) {
	s, _ := newTestExtensionSecurityService(t)
	cases := [][]ExtensionPermission{
		nil,
		{},
		{PermFsRead},
		{PermUINotif},
		{PermFsRead, PermUINotif},
		// clipboard alone has no write/shell/network — treat as trusted
		// (read-ish capability).
		{PermClipboard},
		// ui.webview alone — no privileged host access beyond rendering.
		{PermUIWebview},
	}
	for _, perms := range cases {
		if got := s.ClassifyExtension(perms); got != SecurityTrusted {
			t.Errorf("ClassifyExtension(%v) = %q, want %q", perms, got, SecurityTrusted)
		}
	}
}

func TestExtensionSecurity_ClassifyExtension_Reviewed(t *testing.T) {
	s, _ := newTestExtensionSecurityService(t)
	cases := [][]ExtensionPermission{
		{PermFsWrite},
		{PermShellExec},
		{PermTasksExec},
		{PermDebugExec},
		{PermSCMWrite},
		{PermSecretWrite},
		{PermFsRead, PermFsWrite},
		{PermFsRead, PermShellExec},
		{PermFsRead, PermUINotif, PermFsWrite},
		{PermFsRead, PermShellExec, PermUINotif},
	}
	for _, perms := range cases {
		if got := s.ClassifyExtension(perms); got != SecurityReviewed {
			t.Errorf("ClassifyExtension(%v) = %q, want %q", perms, got, SecurityReviewed)
		}
	}
}

func TestExtensionSecurity_ClassifyExtension_Restricted(t *testing.T) {
	s, _ := newTestExtensionSecurityService(t)
	cases := [][]ExtensionPermission{
		{PermNetwork},
		{PermOpenExt},
		{PermFsRead, PermNetwork},
		{PermNetwork, PermShellExec},
		{PermFsRead, PermFsWrite, PermShellExec, PermNetwork},
		{PermFsRead, PermUINotif, PermNetwork, PermUIWebview},
	}
	for _, perms := range cases {
		if got := s.ClassifyExtension(perms); got != SecurityRestricted {
			t.Errorf("ClassifyExtension(%v) = %q, want %q", perms, got, SecurityRestricted)
		}
	}
}

func TestExtensionSecurity_ValidatePermissionsRejectsUnknown(t *testing.T) {
	if _, err := validateExtensionPermissions([]ExtensionPermission{"shell.exec"}); err == nil {
		t.Fatal("expected unknown extension permission to be rejected")
	}
}

func TestExtensionSecurity_ValidatePermissionsDeduplicates(t *testing.T) {
	got, err := validateExtensionPermissions([]ExtensionPermission{PermFsRead, PermFsRead, PermTasksExec})
	if err != nil {
		t.Fatalf("validate permissions: %v", err)
	}
	want := []ExtensionPermission{PermFsRead, PermTasksExec}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("validated permissions = %v, want %v", got, want)
	}
}

// --- SHA-256 integrity checks ---

func TestExtensionSecurity_VerifyExtensionIntegrity_CorrectHashPasses(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	content := "fake vsix payload"
	vsixPath := writeVSIX(t, filepath.Join(dir, "ext.vsix"), content)
	expected := sha256HexStr(content)
	if err := s.VerifyExtensionIntegrity(vsixPath, expected); err != nil {
		t.Errorf("VerifyExtensionIntegrity with correct hash failed: %v", err)
	}
}

func TestExtensionSecurity_VerifyExtensionIntegrity_WrongHashFails(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	content := "fake vsix payload"
	vsixPath := writeVSIX(t, filepath.Join(dir, "ext.vsix"), content)
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	err := s.VerifyExtensionIntegrity(vsixPath, wrongHash)
	if !errors.Is(err, ErrIntegrityMismatch) {
		t.Errorf("VerifyExtensionIntegrity with wrong hash = %v, want ErrIntegrityMismatch", err)
	}
}

func TestExtensionSecurity_VerifyExtensionIntegrity_EmptyHashRejected(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "ext.vsix"), "x")
	if err := s.VerifyExtensionIntegrity(vsixPath, ""); err == nil {
		t.Error("VerifyExtensionIntegrity with empty expected hash should fail")
	}
}

func TestExtensionSecurity_VerifyExtensionIntegrity_CaseInsensitive(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	content := "payload"
	vsixPath := writeVSIX(t, filepath.Join(dir, "ext.vsix"), content)
	upper := strings.ToUpper(sha256HexStr(content))
	// Uppercase hex should still match (EqualFold).
	if err := s.VerifyExtensionIntegrity(vsixPath, upper); err != nil {
		t.Errorf("uppercase hash should match: %v", err)
	}
}

func TestExtensionSecurity_VerifyExtensionSignature_CompatibilityWrapper(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	content := "compatibility payload"
	vsixPath := writeVSIX(t, filepath.Join(dir, "ext.vsix"), content)
	if err := s.VerifyExtensionSignature(vsixPath, sha256HexStr(content)); err != nil {
		t.Errorf("VerifyExtensionSignature compatibility wrapper failed: %v", err)
	}
}

// --- Blacklist checking ---

func TestExtensionSecurity_IsBlacklisted_BuiltInEntries(t *testing.T) {
	s, _ := newTestExtensionSecurityService(t)
	cases := []struct {
		publisher, name string
		want            bool
	}{
		{"anabarban", "anabarban", true},
		{"esbenp", "prettier-vscode-stolen", true},
		{"marinhobrandao", "node-exec-stolen", true},
		// Case-insensitive.
		{"ANABARBAN", "Anabarban", true},
		// Legitimate (not blacklisted).
		{"esbenp", "prettier-vscode", false},
		{"ms-python", "python", false},
	}
	for _, c := range cases {
		if got := s.IsBlacklisted(c.publisher, c.name); got != c.want {
			t.Errorf("IsBlacklisted(%q, %q) = %v, want %v", c.publisher, c.name, got, c.want)
		}
	}
}

func TestExtensionSecurity_AddToBlacklist_UserEntry(t *testing.T) {
	s, _ := newTestExtensionSecurityService(t)
	if s.IsBlacklisted("evilpublisher", "evilname") {
		t.Fatal("extension should not be blacklisted before add")
	}
	if err := s.AddToBlacklist("evilpublisher", "evilname"); err != nil {
		t.Fatalf("AddToBlacklist: %v", err)
	}
	if !s.IsBlacklisted("evilpublisher", "evilname") {
		t.Error("extension should be blacklisted after add")
	}
}

func TestExtensionSecurity_RemoveFromBlacklist_BuiltInRejected(t *testing.T) {
	s, _ := newTestExtensionSecurityService(t)
	if err := s.RemoveFromBlacklist("anabarban", "anabarban"); err == nil {
		t.Error("removing built-in entry should fail")
	}
}

func TestExtensionSecurity_RemoveFromBlacklist_UserEntry(t *testing.T) {
	s, _ := newTestExtensionSecurityService(t)
	if err := s.AddToBlacklist("userpub", "username"); err != nil {
		t.Fatalf("AddToBlacklist: %v", err)
	}
	if err := s.RemoveFromBlacklist("userpub", "username"); err != nil {
		t.Fatalf("RemoveFromBlacklist: %v", err)
	}
	if s.IsBlacklisted("userpub", "username") {
		t.Error("user entry should be removed")
	}
}

// --- Restricted extensions require backend-owned approval capability ---

func TestExtensionSecurity_SetExtensionEnabled_RestrictedRequiresApproval(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "restricted.vsix"), "payload")
	hash := sha256HexStr("payload")

	info, err := s.RegisterInstall(
		"pub.restricted-ext",
		[]ExtensionPermission{PermFsRead, PermNetwork},
		vsixPath,
		hash,
	)
	if err != nil {
		t.Fatalf("RegisterInstall: %v", err)
	}
	if info.Level != SecurityRestricted {
		t.Fatalf("level = %q, want restricted", info.Level)
	}
	if info.Enabled {
		t.Error("new install should be disabled by default")
	}
	if !info.PendingReview {
		t.Error("new install should be pending review")
	}

	// Enabling without explicit approval must fail.
	err = s.configureExtensionEnabled("pub.restricted-ext", true)
	if !errors.Is(err, ErrRestrictedRequiresApproval) {
		t.Errorf("SetExtensionEnabled(true) without approval = %v, want ErrRestrictedRequiresApproval", err)
	}

	// A renderer-provided true boolean must not elevate privilege.
	if err := s.configureExtensionEnabled("pub.restricted-ext", true, true); !errors.Is(err, ErrRestrictedRequiresApproval) {
		t.Fatalf("SetExtensionEnabled(true, true) = %v, want ErrRestrictedRequiresApproval", err)
	}

	s.approveEnable = func(ExtensionSecurityInfo) bool { return true }
	token, err := s.RequestExtensionEnableApproval("pub.restricted-ext")
	if err != nil {
		t.Fatalf("RequestExtensionEnableApproval: %v", err)
	}
	if err := s.EnableExtensionWithApproval("pub.restricted-ext", token); err != nil {
		t.Errorf("EnableExtensionWithApproval failed: %v", err)
	}

	// PendingReview should be cleared after enable.
	got, err := s.GetSecurityInfo("pub.restricted-ext")
	if err != nil {
		t.Fatalf("GetSecurityInfo: %v", err)
	}
	if !got.Enabled {
		t.Error("extension should be enabled after approval")
	}
	if got.PendingReview {
		t.Error("PendingReview should be cleared after enable")
	}
}

func registerRestrictedExtension(t *testing.T, s *ExtensionSecurityService, dir, extensionID string) {
	t.Helper()
	vsixPath := writeVSIX(t, filepath.Join(dir, extensionID+".vsix"), "payload")
	if _, err := s.RegisterInstall(extensionID, []ExtensionPermission{PermNetwork}, vsixPath, sha256HexStr("payload")); err != nil {
		t.Fatalf("RegisterInstall(%q): %v", extensionID, err)
	}
}

func TestExtensionSecurity_EnableExtensionWithApproval_RejectsMissingAndForgedToken(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	registerRestrictedExtension(t, s, dir, "pub.restricted-ext")
	for _, token := range []string{"", strings.Repeat("0", 64)} {
		if err := s.EnableExtensionWithApproval("pub.restricted-ext", token); !errors.Is(err, ErrRestrictedRequiresApproval) {
			t.Errorf("token %q error = %v, want ErrRestrictedRequiresApproval", token, err)
		}
	}
}

func TestExtensionSecurity_EnableExtensionWithApproval_RejectsReplayAndWrongExtension(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	registerRestrictedExtension(t, s, dir, "pub.first")
	registerRestrictedExtension(t, s, dir, "pub.second")
	s.approveEnable = func(ExtensionSecurityInfo) bool { return true }

	wrongExtensionToken, err := s.RequestExtensionEnableApproval("pub.first")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableExtensionWithApproval("pub.second", wrongExtensionToken); !errors.Is(err, ErrRestrictedRequiresApproval) {
		t.Fatalf("wrong-extension error = %v, want ErrRestrictedRequiresApproval", err)
	}
	if err := s.EnableExtensionWithApproval("pub.first", wrongExtensionToken); !errors.Is(err, ErrRestrictedRequiresApproval) {
		t.Fatalf("wrong-extension token was not consumed: %v", err)
	}

	replayToken, err := s.RequestExtensionEnableApproval("pub.first")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableExtensionWithApproval("pub.first", replayToken); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if err := s.EnableExtensionWithApproval("pub.first", replayToken); !errors.Is(err, ErrRestrictedRequiresApproval) {
		t.Fatalf("replay error = %v, want ErrRestrictedRequiresApproval", err)
	}
}

func TestExtensionSecurity_EnableExtensionWithApproval_RejectsExpiryAndGenerationChange(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		s, dir := newTestExtensionSecurityService(t)
		registerRestrictedExtension(t, s, dir, "pub.restricted-ext")
		now := time.Now()
		s.currentTime = func() time.Time { return now }
		s.approveEnable = func(ExtensionSecurityInfo) bool { return true }
		token, err := s.RequestExtensionEnableApproval("pub.restricted-ext")
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(extensionEnableApprovalTTL + time.Nanosecond)
		if err := s.EnableExtensionWithApproval("pub.restricted-ext", token); !errors.Is(err, ErrRestrictedRequiresApproval) {
			t.Fatalf("expired token error = %v, want ErrRestrictedRequiresApproval", err)
		}
	})

	t.Run("install generation", func(t *testing.T) {
		s, dir := newTestExtensionSecurityService(t)
		registerRestrictedExtension(t, s, dir, "pub.restricted-ext")
		s.approveEnable = func(ExtensionSecurityInfo) bool { return true }
		token, err := s.RequestExtensionEnableApproval("pub.restricted-ext")
		if err != nil {
			t.Fatal(err)
		}
		registerRestrictedExtension(t, s, dir, "pub.restricted-ext")
		if err := s.EnableExtensionWithApproval("pub.restricted-ext", token); !errors.Is(err, ErrRestrictedRequiresApproval) {
			t.Fatalf("stale-generation token error = %v, want ErrRestrictedRequiresApproval", err)
		}
	})
}

func TestExtensionSecurity_SetExtensionEnabled_ReviewedDoesNotRequireApprovalFlag(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "reviewed.vsix"), "payload")
	hash := sha256HexStr("payload")

	if _, err := s.RegisterInstall(
		"pub.reviewed-ext",
		[]ExtensionPermission{PermFsRead, PermFsWrite},
		vsixPath,
		hash,
	); err != nil {
		t.Fatalf("RegisterInstall: %v", err)
	}
	// Reviewed can be enabled without the explicitApproval flag — the
	// popup is informational (frontend shows it before calling).
	if err := s.configureExtensionEnabled("pub.reviewed-ext", true); err != nil {
		t.Errorf("SetExtensionEnabled for reviewed ext failed: %v", err)
	}
}

func TestExtensionSecurity_SetExtensionEnabled_TrustedEnableSucceeds(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "trusted.vsix"), "payload")
	hash := sha256HexStr("payload")

	if _, err := s.RegisterInstall(
		"pub.trusted-ext",
		[]ExtensionPermission{PermFsRead},
		vsixPath,
		hash,
	); err != nil {
		t.Fatalf("RegisterInstall: %v", err)
	}
	if err := s.configureExtensionEnabled("pub.trusted-ext", true); err != nil {
		t.Errorf("SetExtensionEnabled for trusted ext failed: %v", err)
	}
}

// --- Blacklisted extensions blocked from installation ---

func TestExtensionSecurity_RegisterInstall_BlacklistedBlocked(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "evil.vsix"), "payload")
	hash := sha256HexStr("payload")

	_, err := s.RegisterInstall(
		"anabarban.anabarban",
		[]ExtensionPermission{PermFsRead},
		vsixPath,
		hash,
	)
	if !errors.Is(err, ErrBlacklisted) {
		t.Errorf("RegisterInstall for blacklisted ext = %v, want ErrBlacklisted", err)
	}
}

func TestExtensionSecurity_CanInstall_BlacklistedBlocked(t *testing.T) {
	s, _ := newTestExtensionSecurityService(t)
	if err := s.CanInstall("anabarban", "anabarban"); !errors.Is(err, ErrBlacklisted) {
		t.Errorf("CanInstall for blacklisted ext = %v, want ErrBlacklisted", err)
	}
	if err := s.CanInstall("ms-python", "python"); err != nil {
		t.Errorf("CanInstall for legit ext = %v, want nil", err)
	}
}

func TestExtensionSecurity_SetExtensionEnabled_BlacklistedBlocked(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "evil.vsix"), "payload")
	hash := sha256HexStr("payload")

	// Register a non-blacklisted extension, then add it to the blacklist,
	// then verify enable is blocked.
	info, err := s.RegisterInstall(
		"pub.some-ext",
		[]ExtensionPermission{PermFsRead},
		vsixPath,
		hash,
	)
	if err != nil {
		t.Fatalf("RegisterInstall: %v", err)
	}
	if err := s.AddToBlacklist("pub", "some-ext"); err != nil {
		t.Fatalf("AddToBlacklist: %v", err)
	}
	if err := s.configureExtensionEnabled(info.ExtensionID, true); !errors.Is(err, ErrBlacklisted) {
		t.Errorf("SetExtensionEnabled for blacklisted ext = %v, want ErrBlacklisted", err)
	}
}

// --- Extensions without a SHA-256 integrity check cannot be enabled ---

func TestExtensionSecurity_SetExtensionEnabled_IntegrityUncheckedRejected(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "unchecked.vsix"), "payload")
	// Register without an expected digest: no integrity check can have passed.
	info, err := s.RegisterInstall(
		"pub.unchecked-ext",
		[]ExtensionPermission{PermFsRead},
		vsixPath,
		"",
	)
	if err != nil {
		t.Fatalf("RegisterInstall: %v", err)
	}
	if info.IntegrityChecked {
		t.Error("extension registered without an expected digest must be integrityUnchecked")
	}
	if info.Verified {
		t.Error("deprecated verified compatibility field must mirror integrityChecked")
	}
	if err := s.configureExtensionEnabled("pub.unchecked-ext", true); !errors.Is(err, ErrIntegrityNotChecked) {
		t.Errorf("SetExtensionEnabled for integrity-unchecked ext = %v, want ErrIntegrityNotChecked", err)
	}
	got, err := s.GetSecurityInfo("pub.unchecked-ext")
	if err != nil {
		t.Fatalf("GetSecurityInfo: %v", err)
	}
	if got.Enabled {
		t.Error("integrity-unchecked extension was enabled despite the fail-closed gate")
	}
}

// --- New installs default to disabled + pending review (G-SEC-12 req 2) ---

func TestExtensionSecurity_RegisterInstall_DefaultsToDisabledPendingReview(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "ext.vsix"), "payload")
	hash := sha256HexStr("payload")

	info, err := s.RegisterInstall(
		"pub.fresh-ext",
		[]ExtensionPermission{PermFsRead, PermUINotif},
		vsixPath,
		hash,
	)
	if err != nil {
		t.Fatalf("RegisterInstall: %v", err)
	}
	if info.Enabled {
		t.Error("new install should default to disabled")
	}
	if !info.PendingReview {
		t.Error("new install should default to pending review")
	}
	if !info.IntegrityChecked {
		t.Error("install with matching hash should be marked integrityChecked")
	}
	if !info.Verified {
		t.Error("deprecated verified compatibility field should mirror integrityChecked")
	}
	if info.Level != SecurityTrusted {
		t.Errorf("level = %q, want trusted", info.Level)
	}
}

// --- Persistence round-trip ---

func TestExtensionSecurity_GetSecurityInfo_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	s1 := NewExtensionSecurityService(dir)
	vsixPath := writeVSIX(t, filepath.Join(dir, "ext.vsix"), "payload")
	hash := sha256HexStr("payload")

	if _, err := s1.RegisterInstall(
		"pub.persist-ext",
		[]ExtensionPermission{PermFsRead, PermFsWrite},
		vsixPath,
		hash,
	); err != nil {
		t.Fatalf("RegisterInstall: %v", err)
	}
	if err := s1.configureExtensionEnabled("pub.persist-ext", true); err != nil {
		t.Fatalf("SetExtensionEnabled: %v", err)
	}

	// New service instance reading the same config dir.
	s2 := NewExtensionSecurityService(dir)
	got, err := s2.GetSecurityInfo("pub.persist-ext")
	if err != nil {
		t.Fatalf("GetSecurityInfo: %v", err)
	}
	if !got.Enabled {
		t.Error("enabled state should persist across instances")
	}
	if got.Level != SecurityReviewed {
		t.Errorf("level = %q, want reviewed", got.Level)
	}
	if !got.IntegrityChecked {
		t.Error("integrityChecked should persist")
	}
	if !got.Verified {
		t.Error("deprecated verified compatibility field should mirror integrityChecked")
	}
}

func TestExtensionSecurity_IntegrityCheckedPersistsInStateFile(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "ext.vsix"), "payload")
	if _, err := s.RegisterInstall("pub.persisted", []ExtensionPermission{PermFsRead}, vsixPath, sha256HexStr("payload")); err != nil {
		t.Fatalf("RegisterInstall: %v", err)
	}

	statePath := filepath.Join(dir, "koyori-ide", extensionSecurityStateFileName)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read security state: %v", err)
	}
	var state struct {
		Extensions map[string]struct {
			IntegrityChecked bool `json:"integrityChecked"`
			Verified         bool `json:"verified"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode security state: %v", err)
	}
	entry, ok := state.Extensions["pub.persisted"]
	if !ok {
		t.Fatalf("persisted security state has no pub.persisted entry: %s", data)
	}
	if !entry.IntegrityChecked {
		t.Error("persisted security state must write integrityChecked=true")
	}
	if !entry.Verified {
		t.Error("deprecated persisted verified field must mirror integrityChecked")
	}
}

func TestExtensionSecurity_LegacyVerifiedStateMigratesToIntegrityChecked(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "koyori-ide", extensionSecurityStateFileName)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	legacy := []byte(`{
  "extensions": {
    "pub.legacy": {
      "level": "trusted",
      "permissions": ["fs.read"],
      "sha256": "legacy-digest",
      "verified": true,
      "enabled": false,
      "pendingReview": true
    }
  }
}`)
	if err := os.WriteFile(statePath, legacy, 0o600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	s := NewExtensionSecurityService(dir)
	info, err := s.GetSecurityInfo("pub.legacy")
	if err != nil {
		t.Fatalf("GetSecurityInfo: %v", err)
	}
	if !info.IntegrityChecked || !info.Verified {
		t.Fatalf("legacy verified state did not migrate to integrityChecked: %+v", info)
	}
	if err := s.configureExtensionEnabled("pub.legacy", true); err != nil {
		t.Fatalf("legacy integrity-checked extension should enable: %v", err)
	}

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read migrated state: %v", err)
	}
	var migrated extensionSecurityStateFile
	if err := json.Unmarshal(data, &migrated); err != nil {
		t.Fatalf("decode migrated state: %v", err)
	}
	if !migrated.Extensions["pub.legacy"].IntegrityChecked {
		t.Fatalf("migrated state did not persist integrityChecked=true: %s", data)
	}
}

func TestExtensionSecurity_ExplicitIntegrityUncheckedStateFailsClosed(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "koyori-ide", extensionSecurityStateFileName)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	state := []byte(`{
  "extensions": {
    "pub.unchecked": {
      "level": "trusted",
      "permissions": ["fs.read"],
      "sha256": "unchecked-digest",
      "integrityChecked": false,
      "verified": true,
      "enabled": false,
      "pendingReview": true
    }
  }
}`)
	if err := os.WriteFile(statePath, state, 0o600); err != nil {
		t.Fatalf("write security state: %v", err)
	}

	s := NewExtensionSecurityService(dir)
	info, err := s.GetSecurityInfo("pub.unchecked")
	if err != nil {
		t.Fatalf("GetSecurityInfo: %v", err)
	}
	if info.IntegrityChecked || info.Verified {
		t.Fatalf("explicit integrityChecked=false must take precedence over legacy verified=true: %+v", info)
	}
	if err := s.configureExtensionEnabled("pub.unchecked", true); !errors.Is(err, ErrIntegrityNotChecked) {
		t.Fatalf("enable unchecked state error = %v, want ErrIntegrityNotChecked", err)
	}
	info, err = s.GetSecurityInfo("pub.unchecked")
	if err != nil {
		t.Fatalf("GetSecurityInfo after rejected enable: %v", err)
	}
	if info.Enabled {
		t.Fatal("explicit integrityChecked=false state was enabled")
	}
}

// --- Disable always succeeds (for non-blacklisted) ---

func TestExtensionSecurity_SetExtensionEnabled_DisableAlwaysSucceeds(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "ext.vsix"), "payload")
	hash := sha256HexStr("payload")

	if _, err := s.RegisterInstall(
		"pub.restricted-ext",
		[]ExtensionPermission{PermNetwork},
		vsixPath,
		hash,
	); err != nil {
		t.Fatalf("RegisterInstall: %v", err)
	}
	// Disabling a restricted ext (even without approval) must work.
	if err := s.configureExtensionEnabled("pub.restricted-ext", false); err != nil {
		t.Errorf("disable should always succeed: %v", err)
	}
}

func TestExtensionSecurity_MutationSecurityAudit(t *testing.T) {
	logs := captureSecurityAudit(t)
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "audit.vsix"), "vsix-payload-secret")
	hash := sha256HexStr("vsix-payload-secret")
	if _, err := s.RegisterInstall("audit.publisher", []ExtensionPermission{PermNetwork}, vsixPath, hash); err != nil {
		t.Fatal(err)
	}
	if err := s.configureExtensionEnabled("audit.publisher", true); !errors.Is(err, ErrRestrictedRequiresApproval) {
		t.Fatalf("unapproved enable error = %v", err)
	}
	if err := s.configureExtensionEnabled("audit.publisher", true, true); !errors.Is(err, ErrRestrictedRequiresApproval) {
		t.Fatalf("renderer approval error = %v", err)
	}
	s.approveEnable = func(ExtensionSecurityInfo) bool { return true }
	token, err := s.RequestExtensionEnableApproval("audit.publisher")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableExtensionWithApproval("audit.publisher", token); err != nil {
		t.Fatal(err)
	}
	if err := s.configureExtensionEnabled("audit.publisher", false); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToBlacklist("audit", "publisher"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFromBlacklist("audit", "publisher"); err != nil {
		t.Fatal(err)
	}

	text := logs.String()
	for _, event := range []string{
		"extension.enable", "extension.disable", "extension.approval",
		"extension.blacklist.add", "extension.blacklist.remove",
	} {
		if !strings.Contains(text, `"event":"`+event+`"`) {
			t.Errorf("missing event %q: %s", event, text)
		}
	}
	if !strings.Contains(text, `"outcome":"success"`) || !strings.Contains(text, `"outcome":"failure"`) {
		t.Errorf("extension audit must include success and failure: %s", text)
	}
	for _, sensitive := range []string{"vsix-payload-secret", vsixPath, hash} {
		if strings.Contains(text, sensitive) {
			t.Errorf("extension audit leaked %q: %s", sensitive, text)
		}
	}
}

// --- ComputeSHA256 ---

func TestExtensionSecurity_ComputeSHA256(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	content := "hello world"
	vsixPath := writeVSIX(t, filepath.Join(dir, "ext.vsix"), content)
	got, err := s.ComputeSHA256(vsixPath)
	if err != nil {
		t.Fatalf("ComputeSHA256: %v", err)
	}
	want := sha256HexStr(content)
	if got != want {
		t.Errorf("ComputeSHA256 = %q, want %q", got, want)
	}
}

// --- ListSecurityInfo ---

func TestExtensionSecurity_ListSecurityInfo(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)
	vsixPath := writeVSIX(t, filepath.Join(dir, "ext.vsix"), "payload")
	hash := sha256HexStr("payload")

	if _, err := s.RegisterInstall("pub.a", []ExtensionPermission{PermFsRead}, vsixPath, hash); err != nil {
		t.Fatalf("RegisterInstall a: %v", err)
	}
	if _, err := s.RegisterInstall("pub.b", []ExtensionPermission{PermNetwork}, vsixPath, hash); err != nil {
		t.Fatalf("RegisterInstall b: %v", err)
	}
	list := s.ListSecurityInfo()
	if len(list) != 2 {
		t.Errorf("ListSecurityInfo returned %d entries, want 2", len(list))
	}
}

// --- M-10: Streaming SHA-256 ---

// TestComputeSHA256_Streaming hashes a large temp file and confirms the
// streaming implementation (io.Copy) produces the correct digest (M-10).
// The expected hash is computed independently via a direct sha256.Write
// on the full content.
func TestComputeSHA256_Streaming(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)

	// Create a 4 MB file with a repeating pattern. This is large enough
	// to exercise multiple read iterations in io.Copy.
	pattern := []byte("Koyori IDE-M10-streaming-test-")
	chunkSize := len(pattern)
	totalSize := 4 * 1024 * 1024 // 4 MB
	vsixPath := filepath.Join(dir, "large.vsix")
	f, err := os.Create(vsixPath)
	if err != nil {
		t.Fatalf("create large file: %v", err)
	}
	// Compute expected hash while writing.
	expectedHasher := sha256.New()
	for written := 0; written < totalSize; written += chunkSize {
		if _, err := f.Write(pattern); err != nil {
			t.Fatalf("write large file: %v", err)
		}
		expectedHasher.Write(pattern)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close large file: %v", err)
	}
	expected := hex.EncodeToString(expectedHasher.Sum(nil))

	got, err := s.ComputeSHA256(vsixPath)
	if err != nil {
		t.Fatalf("ComputeSHA256 on large file failed: %v", err)
	}
	if got != expected {
		t.Errorf("ComputeSHA256 mismatch:\n  got:  %s\n  want: %s", got, expected)
	}
}

// TestComputeSHA256_Streaming_VerifyExtensionIntegrity confirms that
// VerifyExtensionIntegrity also works with the streaming implementation
// on a large file (M-10).
func TestComputeSHA256_Streaming_VerifyExtensionIntegrity(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)

	// Create a 2 MB file with a repeating pattern.
	pattern := []byte("verify-streaming-M10-")
	vsixPath := filepath.Join(dir, "large.vsix")
	f, err := os.Create(vsixPath)
	if err != nil {
		t.Fatalf("create large file: %v", err)
	}
	expectedHasher := sha256.New()
	for written := 0; written < 2*1024*1024; written += len(pattern) {
		if _, err := f.Write(pattern); err != nil {
			t.Fatalf("write: %v", err)
		}
		expectedHasher.Write(pattern)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	expected := hex.EncodeToString(expectedHasher.Sum(nil))

	// VerifyExtensionIntegrity should accept the correct hash.
	if err := s.VerifyExtensionIntegrity(vsixPath, expected); err != nil {
		t.Errorf("VerifyExtensionIntegrity with correct hash failed: %v", err)
	}
	// And reject a wrong hash.
	wrong := "0000000000000000000000000000000000000000000000000000000000000000"
	if err := s.VerifyExtensionIntegrity(vsixPath, wrong); !errors.Is(err, ErrIntegrityMismatch) {
		t.Errorf("VerifyExtensionIntegrity with wrong hash = %v, want ErrIntegrityMismatch", err)
	}
}

// TestComputeSHA256_Streaming_NonexistentFile verifies that the streaming
// implementation returns an error for a non-existent file (M-10).
func TestComputeSHA256_Streaming_NonexistentFile(t *testing.T) {
	s, _ := newTestExtensionSecurityService(t)
	_, err := s.ComputeSHA256(filepath.Join(t.TempDir(), "does-not-exist.vsix"))
	if err == nil {
		t.Error("expected error for non-existent file, got nil")
	}
}

// TestExtensionSecurity_M10_StreamingSHA256 验证 M-10 流式 SHA-256:
// 创建一个已知内容的临时文件,通过 ComputeSHA256(流式 io.Copy)计算
// 哈希,并与独立计算的 sha256.Sum256(contents) 比较,确认二者一致。
func TestExtensionSecurity_M10_StreamingSHA256(t *testing.T) {
	s, dir := newTestExtensionSecurityService(t)

	// 写入已知内容到临时文件。
	content := []byte("M-10 streaming sha256 verification payload — Koyori IDE")
	vsixPath := writeVSIX(t, filepath.Join(dir, "known.vsix"), string(content))

	// 通过流式实现计算哈希。
	got, err := s.ComputeSHA256(vsixPath)
	if err != nil {
		t.Fatalf("ComputeSHA256 failed: %v", err)
	}

	// 独立计算 sha256.Sum256(contents) 作为期望值。
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])

	if got != want {
		t.Errorf("streaming SHA-256 mismatch:\n  got:  %s\n  want: %s", got, want)
	}
}
