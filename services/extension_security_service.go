package services

// extension_security_service.go — G-VSC-03 / G-SEC-12: VS Code extension
// security gates.
//
// This service implements the BLOCKER security gates for VS Code-style
// extensions (a separate code path from the native plugin system in
// plugin_service.go). It provides:
//
//  1. Permission-based classification: each extension is classified as
//     Trusted / Reviewed / Restricted based on the permissions it requests.
//  2. Untrusted-by-default: newly installed extensions start disabled +
//     "pending review". The first enable attempt surfaces a popup listing
//     the requested API permissions (handled by the frontend store).
//  3. Integrity verification: VSIX files are checked against an expected
//     SHA-256 hash. This detects a mismatched payload but does not authenticate
//     the publisher. Extensions without a successful check are rejected.
//  4. Blacklist enforcement: known-malicious extension IDs are blocked
//     from installation entirely.
//
// The classification uses a richer permission set than the native plugin
// system (which uses fs.read/fs.write/shell.exec/net/ai.send) because VS
// Code extensions declare a broader API surface.

import (
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ExtensionSecurityLevel is the risk tier assigned to an extension based
// on the permissions it requests. G-VSC-03 requirement 1.
type ExtensionSecurityLevel string

const (
	// SecurityTrusted: read-only extensions. Only request fs.read and/or
	// ui.notifications. Enabled-by-default is still pending-review per
	// G-SEC-12 (new installs are disabled + pending review), but the
	// enable popup shows a minimal "read-only" notice.
	SecurityTrusted ExtensionSecurityLevel = "trusted"
	// SecurityReviewed: extensions that request file write or terminal
	// access in addition to read. Enable requires a permission popup
	// (G-VSC-03 requirement 2).
	SecurityReviewed ExtensionSecurityLevel = "reviewed"
	// SecurityRestricted: extensions that request network access or
	// unrestricted shell execution. Disabled by default; enabling
	// requires a popup listing the specific APIs and an explicit
	// confirmation (G-VSC-03 requirement 2, G-SEC-12 requirement 2).
	SecurityRestricted ExtensionSecurityLevel = "restricted"
)

// ExtensionPermission is a capability an extension requests. The set is
// broader than the native PluginPermission to cover the VS Code API
// surface that the compatibility layer exposes.
type ExtensionPermission string

const (
	PermFsRead      ExtensionPermission = "fs.read"
	PermFsWrite     ExtensionPermission = "fs.write"
	PermShellExec   ExtensionPermission = "shell.execute"
	PermNetwork     ExtensionPermission = "network"
	PermClipboard   ExtensionPermission = "clipboard"
	PermUINotif     ExtensionPermission = "ui.notifications"
	PermUIWebview   ExtensionPermission = "ui.webview"
	PermTasksExec   ExtensionPermission = "tasks.execute"
	PermDebugExec   ExtensionPermission = "debug.execute"
	PermSCMRead     ExtensionPermission = "scm.read"
	PermSCMWrite    ExtensionPermission = "scm.write"
	PermOpenExt     ExtensionPermission = "env.openExternal"
	PermSecretRead  ExtensionPermission = "secrets.read"
	PermSecretWrite ExtensionPermission = "secrets.write"
)

var knownExtensionPermissions = map[ExtensionPermission]struct{}{
	PermFsRead: {}, PermFsWrite: {}, PermShellExec: {}, PermNetwork: {},
	PermClipboard: {}, PermUINotif: {}, PermUIWebview: {}, PermTasksExec: {},
	PermDebugExec: {}, PermSCMRead: {}, PermSCMWrite: {}, PermOpenExt: {},
	PermSecretRead: {}, PermSecretWrite: {},
}

func validateExtensionPermissions(permissions []ExtensionPermission) ([]ExtensionPermission, error) {
	validated := make([]ExtensionPermission, 0, len(permissions))
	seen := make(map[ExtensionPermission]struct{}, len(permissions))
	for _, permission := range permissions {
		if _, ok := knownExtensionPermissions[permission]; !ok {
			return nil, fmt.Errorf("unknown extension permission %q", permission)
		}
		if _, ok := seen[permission]; ok {
			continue
		}
		seen[permission] = struct{}{}
		validated = append(validated, permission)
	}
	return validated, nil
}

// ExtensionSecurityInfo is the runtime descriptor for an installed
// extension's security state. Mirrored on the frontend via the Wails
// binding so the permission dialog and PluginsView can render it.
type ExtensionSecurityInfo struct {
	// ExtensionID is the "<publisher>.<name>" identifier (VS Code convention).
	ExtensionID string `json:"extensionId"`
	// Level is the classified security tier.
	Level ExtensionSecurityLevel `json:"level"`
	// Permissions is the full list of requested permissions.
	Permissions []ExtensionPermission `json:"permissions"`
	// SHA256 is the hex-encoded SHA-256 of the installed VSIX payload.
	SHA256 string `json:"sha256"`
	// IntegrityChecked is true when the installed VSIX matched the expected
	// SHA-256 digest. It does not authenticate the publisher.
	IntegrityChecked bool `json:"integrityChecked"`
	// Verified is a deprecated compatibility alias for IntegrityChecked. It is
	// not publisher authentication and must not be presented as such.
	Verified bool `json:"verified"`
	// Enabled is the current enabled state. New installs default to
	// false (G-SEC-12 requirement 2).
	Enabled bool `json:"enabled"`
	// Blacklisted is true when the extension ID is in the known-malicious
	// list. Blacklisted extensions cannot be enabled or installed.
	Blacklisted bool `json:"blacklisted"`
	// PendingReview is true for newly installed extensions that have not
	// yet been explicitly enabled by the user. Cleared on first enable.
	PendingReview bool `json:"pendingReview"`
}

// extensionSecurityStateEntry is one row in the persisted extension
// security state file. Stored under <configDir>/koyori-ide/extension-security.json.
// This is distinct from the simpler extensionStateEntry in
// marketplace_service.go (which only tracks Enabled) because the security
// service tracks classification, permissions, and integrity-check state.
type extensionSecurityStateEntry struct {
	Level            ExtensionSecurityLevel `json:"level"`
	Permissions      []ExtensionPermission  `json:"permissions"`
	SHA256           string                 `json:"sha256"`
	IntegrityChecked bool                   `json:"integrityChecked"`
	// Verified is retained only so older clients/state readers can consume the
	// file. New code must use IntegrityChecked for the SHA-256 integrity gate.
	Verified      bool `json:"verified"`
	Enabled       bool `json:"enabled"`
	PendingReview bool `json:"pendingReview"`
}

// UnmarshalJSON migrates the legacy verified field without allowing it to
// override an explicitly present integrityChecked=false value. The old field
// represented the same SHA-256 comparison; it never represented publisher
// authentication.
func (e *extensionSecurityStateEntry) UnmarshalJSON(data []byte) error {
	var raw struct {
		Level            ExtensionSecurityLevel `json:"level"`
		Permissions      []ExtensionPermission  `json:"permissions"`
		SHA256           string                 `json:"sha256"`
		IntegrityChecked *bool                  `json:"integrityChecked"`
		Verified         bool                   `json:"verified"`
		Enabled          bool                   `json:"enabled"`
		PendingReview    bool                   `json:"pendingReview"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.Level = raw.Level
	e.Permissions = raw.Permissions
	e.SHA256 = raw.SHA256
	if raw.IntegrityChecked != nil {
		e.IntegrityChecked = *raw.IntegrityChecked
	} else {
		e.IntegrityChecked = raw.Verified
	}
	e.Verified = e.IntegrityChecked
	e.Enabled = raw.Enabled
	e.PendingReview = raw.PendingReview
	return nil
}

type extensionSecurityStateFile struct {
	Extensions map[string]extensionSecurityStateEntry `json:"extensions"`
}

// extensionSecurityStateFileName is the on-disk file name for persisted
// extension security state, written under <configDir>/koyori-ide/.
const extensionSecurityStateFileName = "extension-security.json"

const extensionEnableApprovalTTL = 5 * time.Minute

const extensionEnableAction = "enable"

// ErrBlacklisted is returned when an operation targets a blacklisted
// extension. Callers should surface this to the user as "installation
// blocked: known malicious extension".
var ErrBlacklisted = errors.New("extension is on the known-malicious blacklist")

// ErrIntegrityMismatch is returned when a VSIX's computed SHA-256 does not
// match the expected hash.
var ErrIntegrityMismatch = errors.New("extension SHA-256 integrity check failed: digest mismatch")

// ErrSignatureMismatch is a deprecated compatibility alias for the SHA-256
// integrity error.
var ErrSignatureMismatch = ErrIntegrityMismatch

// ErrRestrictedRequiresApproval is returned when a Restricted extension is
// enabled without a valid backend-issued approval capability.
var ErrRestrictedRequiresApproval = errors.New("restricted extensions require explicit user approval to enable")

// ErrIntegrityNotChecked is returned when an extension that has not passed the
// expected SHA-256 integrity check is enabled.
var ErrIntegrityNotChecked = errors.New("extension has not passed the SHA-256 integrity check")

// ErrNotVerified is a deprecated compatibility alias for callers compiled
// against the old name. It does not indicate publisher authentication.
var ErrNotVerified = ErrIntegrityNotChecked

// ExtensionSecurityService implements G-VSC-03 / G-SEC-12. It is
// thread-safe (mu guards the in-memory state and the blacklist).
type ExtensionSecurityService struct {
	mu                sync.Mutex
	configDir         string
	blacklist         map[string]bool
	approvalMu        sync.Mutex
	approvals         map[string]extensionEnableApproval
	installGeneration map[string]uint64
	approveEnable     func(ExtensionSecurityInfo) bool
	currentTime       func() time.Time
}

type extensionEnableApproval struct {
	extensionID string
	action      string
	generation  uint64
	expiresAt   time.Time
}

// NewExtensionSecurityService constructs the service. configDir is the
// user config directory (same one used by PluginService). The built-in
// default blacklist is loaded immediately; a user-overridable copy is
// read from <configDir>/koyori-ide/extension-blacklist.json if present.
func NewExtensionSecurityService(configDir string) *ExtensionSecurityService {
	s := &ExtensionSecurityService{
		configDir:         configDir,
		blacklist:         make(map[string]bool),
		approvals:         make(map[string]extensionEnableApproval),
		installGeneration: make(map[string]uint64),
		approveEnable:     nativeExtensionEnableApproval,
		currentTime:       time.Now,
	}
	// Seed with the built-in defaults. The on-disk file (if any) is
	// layered on top so users can add entries without rebuilding.
	for k := range defaultBlacklist {
		s.blacklist[k] = true
	}
	s.loadBlacklistFile()
	return s
}

func nativeExtensionEnableApproval(info ExtensionSecurityInfo) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Enable restricted extension").SetMessage(
		fmt.Sprintf("Extension: %s\nPermissions: %s", info.ExtensionID, strings.Join(extensionPermissionStrings(info.Permissions), ", ")),
	)
	dialog.AddButton("Enable").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("Cancel").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(extensionEnableApprovalTTL):
		return false
	}
}

func extensionPermissionStrings(permissions []ExtensionPermission) []string {
	values := make([]string, len(permissions))
	for i, permission := range permissions {
		values[i] = string(permission)
	}
	return values
}

// ClassifyExtension determines the security level from the requested
// permissions (G-VSC-03 requirement 1).
//
// Rules:
//   - Only fs.read and/or ui.notifications (or no permissions) → Trusted
//   - Adds fs.write or shell.execute → Reviewed
//   - Adds network or (shell.execute present alongside network) → Restricted
//
// "Unrestricted shell.execute" is treated as Restricted when combined
// with network access; a standalone shell.execute without network is
// Reviewed (terminal-only).
func (s *ExtensionSecurityService) ClassifyExtension(permissions []ExtensionPermission) ExtensionSecurityLevel {
	has := make(map[ExtensionPermission]bool, len(permissions))
	for _, p := range permissions {
		has[p] = true
	}

	hasNetwork := has[PermNetwork] || has[PermOpenExt]
	hasShell := has[PermShellExec]
	hasReviewed := has[PermFsWrite] || has[PermTasksExec] || has[PermDebugExec] ||
		has[PermSCMWrite] || has[PermSecretWrite]

	// Restricted: network access (with or without shell) or unrestricted
	// shell + network. Per G-VSC-03 requirement 1, "network" and
	// "unrestricted shell.execute" both bump to Restricted.
	if hasNetwork {
		return SecurityRestricted
	}
	// shell.execute alongside network is already covered above. A
	// standalone shell.execute is Reviewed. The "unrestricted" qualifier
	// in the spec is operationalized as: shell.execute + network =
	// Restricted (handled above), so we don't double-classify here.

	// Reviewed: file write or terminal access.
	if hasReviewed || hasShell {
		return SecurityReviewed
	}

	// Trusted: only read-only / notification perms (or none).
	return SecurityTrusted
}

// VerifyExtensionIntegrity verifies the SHA-256 hash of a downloaded VSIX file
// against the expected hash (G-SEC-12 requirement 3). This is an integrity
// check only; it does not authenticate the publisher.
//
// expectedSHA256 is the hex-encoded hash published by the marketplace
// (or supplied out-of-band for self-hosted extensions). An empty
// expectedSHA256 is rejected — verification requires a hash to compare
// against. Returns ErrIntegrityMismatch on mismatch.
func (s *ExtensionSecurityService) VerifyExtensionIntegrity(vsixPath string, expectedSHA256 string) error {
	if expectedSHA256 == "" {
		return errors.New("SHA-256 integrity check requires a non-empty expected digest")
	}
	// M-10: stream the file instead of reading it all into memory.
	// This reduces peak memory for large VSIX files.
	actual, err := computeFileSHA256(vsixPath)
	if err != nil {
		return fmt.Errorf("read VSIX for SHA-256 integrity check: %w", err)
	}
	// Lowercase + trim for a robust comparison.
	if !strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(expectedSHA256)) {
		return ErrIntegrityMismatch
	}
	return nil
}

// VerifyExtensionSignature is a deprecated compatibility wrapper. The method
// performs only a SHA-256 integrity check.
// Deprecated: use VerifyExtensionIntegrity.
func (s *ExtensionSecurityService) VerifyExtensionSignature(vsixPath string, expectedSHA256 string) error {
	return s.VerifyExtensionIntegrity(vsixPath, expectedSHA256)
}

// ComputeSHA256 returns the hex-encoded SHA-256 of a file. Used to
// populate ExtensionSecurityInfo.SHA256 after a successful install.
//
// M-10: streams the file via io.Copy instead of reading it all into
// memory, reducing peak memory for large VSIX files.
func (s *ExtensionSecurityService) ComputeSHA256(vsixPath string) (string, error) {
	return computeFileSHA256(vsixPath)
}

// computeFileSHA256 streams the file at path through a SHA-256 hasher
// and returns the hex-encoded digest. The file is opened and closed
// properly; only a small read buffer is held in memory at any time
// (M-10).
func computeFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for sha256: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// IsBlacklisted checks if an extension (by "<publisher>.<name>") is in
// the known-malicious list (G-VSC-03 requirement 3, G-SEC-12 requirement
// 3). Thread-safe.
func (s *ExtensionSecurityService) IsBlacklisted(publisher, name string) bool {
	id := normalizeExtensionID(publisher, name)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blacklist[id]
}

// AddToBlacklist adds an extension to the blacklist and persists the
// update to <configDir>/koyori-ide/extension-blacklist.json so it survives
// restarts. Thread-safe.
func (s *ExtensionSecurityService) AddToBlacklist(publisher, name string) (err error) {
	id := normalizeExtensionID(publisher, name)
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = "failure"
		}
		securityAudit("extension.blacklist.add", outcome, "extension_id", id)
	}()
	if id == "" || id == "." {
		return fmt.Errorf("invalid extension identifier: publisher and name are required")
	}
	s.mu.Lock()
	s.blacklist[id] = true
	s.mu.Unlock()
	return s.saveBlacklistFile()
}

// RemoveFromBlacklist removes a user-added entry. Built-in defaults
// cannot be removed (the entry is re-added on next start); this is
// intentional — the default list represents known-malicious IDs that
// must not be bypassable. Returns an error if the entry is built-in.
func (s *ExtensionSecurityService) RemoveFromBlacklist(publisher, name string) (err error) {
	id := normalizeExtensionID(publisher, name)
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = "failure"
		}
		securityAudit("extension.blacklist.remove", outcome, "extension_id", id)
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if defaultBlacklist[id] {
		return fmt.Errorf("cannot remove built-in blacklist entry %q", id)
	}
	delete(s.blacklist, id)
	return s.saveBlacklistFileLocked()
}

// RegisterInstallFromFile records a newly installed extension's security info
// from the VSIX file at vsixPath. Hashing is streamed from disk, so registration
// does not require a full archive-sized byte slice. New installs are stored as
// disabled + pending review (G-SEC-12 requirement 2). Returns ErrBlacklisted if
// the extension is on the blacklist.
func (s *ExtensionSecurityService) RegisterInstallFromFile(
	extensionID string,
	permissions []ExtensionPermission,
	vsixPath string,
	expectedSHA256 string,
) (*ExtensionSecurityInfo, error) {
	if extensionID == "" {
		return nil, fmt.Errorf("extensionID is required")
	}
	validatedPermissions, err := validateExtensionPermissions(permissions)
	if err != nil {
		return nil, err
	}
	// Blacklist check first — never record state for blacklisted IDs.
	if publisher, name, ok := splitExtensionID(extensionID); ok {
		if s.IsBlacklisted(publisher, name) {
			return nil, ErrBlacklisted
		}
	} else if s.IsBlacklisted("", extensionID) {
		return nil, ErrBlacklisted
	}

	info := &ExtensionSecurityInfo{
		ExtensionID:   extensionID,
		Level:         s.ClassifyExtension(validatedPermissions),
		Permissions:   append([]ExtensionPermission{}, validatedPermissions...),
		Enabled:       false, // G-SEC-12: disabled by default
		PendingReview: true,  // G-SEC-12: pending review
	}

	// SHA-256 integrity check. When expectedSHA256 is empty we record an
	// unchecked extension and fail closed on enable (ErrIntegrityNotChecked).
	if expectedSHA256 != "" {
		if err := s.VerifyExtensionIntegrity(vsixPath, expectedSHA256); err != nil {
			return nil, fmt.Errorf("check VSIX integrity: %w", err)
		}
		info.IntegrityChecked = true
		info.Verified = true // Deprecated compatibility alias only.
		info.SHA256 = expectedSHA256
	} else if vsixPath != "" {
		// Compute the hash for record-keeping but do not claim it was checked
		// against an expected digest.
		if hash, err := s.ComputeSHA256(vsixPath); err == nil {
			info.SHA256 = hash
		}
	}

	s.approvalMu.Lock()
	if err := s.saveSecurityInfo(info); err != nil {
		s.approvalMu.Unlock()
		return nil, fmt.Errorf("persist security info: %w", err)
	}
	s.installGeneration[extensionID]++
	s.approvalMu.Unlock()
	return info, nil
}

// RegisterInstall preserves the existing Wails binding while delegating to the
// file-based registration path used by marketplace installs.
func (s *ExtensionSecurityService) RegisterInstall(
	extensionID string,
	permissions []ExtensionPermission,
	vsixPath string,
	expectedSHA256 string,
) (*ExtensionSecurityInfo, error) {
	return s.RegisterInstallFromFile(extensionID, permissions, vsixPath, expectedSHA256)
}

// removeInstall removes the persisted security record for an extension. It is
// intentionally idempotent so failed install/update cleanup can safely call it.
//
//wails:ignore
func (s *ExtensionSecurityService) removeInstall(extensionID string) error {
	if extensionID == "" {
		return fmt.Errorf("extensionID is required")
	}
	state := s.loadExtensionState()
	if _, ok := state.Extensions[extensionID]; !ok {
		return nil
	}
	delete(state.Extensions, extensionID)
	s.approvalMu.Lock()
	if err := s.saveExtensionState(state); err != nil {
		s.approvalMu.Unlock()
		return err
	}
	s.installGeneration[extensionID]++
	s.approvalMu.Unlock()
	return nil
}

// restoreInstall restores a previously captured security record. It is used by
// rollback paths and is not a renderer-facing operation.
//
//wails:ignore
func (s *ExtensionSecurityService) restoreInstall(info *ExtensionSecurityInfo) error {
	if info == nil || info.ExtensionID == "" {
		return fmt.Errorf("security info is required")
	}
	s.approvalMu.Lock()
	if err := s.saveSecurityInfo(info); err != nil {
		s.approvalMu.Unlock()
		return err
	}
	s.installGeneration[info.ExtensionID]++
	s.approvalMu.Unlock()
	return nil
}

// GetSecurityInfo returns the persisted security info for an installed
// extension. Returns an error if the extension has no recorded state
// (i.e. was never registered via RegisterInstallFromFile).
func (s *ExtensionSecurityService) GetSecurityInfo(extensionID string) (*ExtensionSecurityInfo, error) {
	if extensionID == "" {
		return nil, fmt.Errorf("extensionID is required")
	}
	state := s.loadExtensionState()
	entry, ok := state.Extensions[extensionID]
	if !ok {
		return nil, fmt.Errorf("no security info for extension %q", extensionID)
	}
	// Refresh the blacklist flag from the in-memory set so newly-added
	// entries are reflected without a re-register.
	blacklisted := false
	if publisher, name, ok := splitExtensionID(extensionID); ok {
		blacklisted = s.IsBlacklisted(publisher, name)
	}
	return &ExtensionSecurityInfo{
		ExtensionID:      extensionID,
		Level:            entry.Level,
		Permissions:      append([]ExtensionPermission{}, entry.Permissions...),
		SHA256:           entry.SHA256,
		IntegrityChecked: entry.IntegrityChecked,
		Verified:         entry.IntegrityChecked, // Deprecated compatibility alias.
		Enabled:          entry.Enabled,
		Blacklisted:      blacklisted,
		PendingReview:    entry.PendingReview,
	}, nil
}

// configureExtensionEnabled enables/disables extensions that do not require a
// backend-owned approval capability. The legacy renderer approval boolean is
// intentionally ignored: Restricted extensions must use the token flow below.
//
// Enabling an extension without a successful SHA-256 integrity check is
// rejected with ErrIntegrityNotChecked.
// Enabling a blacklisted extension is rejected with ErrBlacklisted.
//
//wails:ignore
func (s *ExtensionSecurityService) configureExtensionEnabled(extensionID string, enabled bool, _ ...bool) (err error) {
	return s.setExtensionEnabled(extensionID, enabled, false)
}

// RequestExtensionEnableApproval obtains native user confirmation and returns
// a short-lived capability bound to this extension, the enable action, and the
// current installation generation.
func (s *ExtensionSecurityService) RequestExtensionEnableApproval(extensionID string) (string, error) {
	info, err := s.GetSecurityInfo(extensionID)
	if err != nil {
		return "", err
	}
	if info.Blacklisted {
		return "", ErrBlacklisted
	}
	if !info.IntegrityChecked {
		return "", ErrIntegrityNotChecked
	}
	if info.Level != SecurityRestricted {
		return "", fmt.Errorf("extension enable approval is only available for restricted extensions")
	}

	s.approvalMu.Lock()
	generation := s.installGeneration[extensionID]
	s.approvalMu.Unlock()
	if s.approveEnable == nil || !s.approveEnable(*info) {
		return "", ErrRestrictedRequiresApproval
	}

	now := s.currentTime()
	for attempts := 0; attempts < 4; attempts++ {
		token, err := newExtensionEnableApprovalToken()
		if err != nil {
			return "", err
		}
		s.approvalMu.Lock()
		for pendingToken, approval := range s.approvals {
			if !approval.expiresAt.After(now) {
				delete(s.approvals, pendingToken)
			}
		}
		if _, exists := s.approvals[token]; exists {
			s.approvalMu.Unlock()
			continue
		}
		s.approvals[token] = extensionEnableApproval{
			extensionID: extensionID,
			action:      extensionEnableAction,
			generation:  generation,
			expiresAt:   now.Add(extensionEnableApprovalTTL),
		}
		s.approvalMu.Unlock()
		return token, nil
	}
	return "", fmt.Errorf("create unique extension enable approval token")
}

// EnableExtensionWithApproval atomically consumes a backend-issued approval
// capability. Invalid, expired, replayed, cross-extension, or stale-generation
// capabilities fail closed.
func (s *ExtensionSecurityService) EnableExtensionWithApproval(extensionID, token string) error {
	if !isCanonicalExtensionEnableApprovalToken(token) {
		return ErrRestrictedRequiresApproval
	}
	now := s.currentTime()
	s.approvalMu.Lock()
	approval, ok := s.approvals[token]
	if ok {
		delete(s.approvals, token)
	}
	generation := s.installGeneration[extensionID]
	if !ok || approval.extensionID != extensionID || approval.action != extensionEnableAction ||
		!approval.expiresAt.After(now) || approval.generation != generation {
		s.approvalMu.Unlock()
		return ErrRestrictedRequiresApproval
	}
	err := s.setExtensionEnabled(extensionID, true, true)
	s.approvalMu.Unlock()
	return err
}

func newExtensionEnableApprovalToken() (string, error) {
	var raw [32]byte
	if _, err := crypto_rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create extension enable approval token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func isCanonicalExtensionEnableApprovalToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(token)
	return err == nil && hex.EncodeToString(decoded) == token
}

func (s *ExtensionSecurityService) setExtensionEnabled(extensionID string, enabled, approved bool) (err error) {
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = "failure"
		}
		event := "extension.disable"
		if enabled {
			event = "extension.enable"
		}
		securityAudit(event, outcome, "extension_id", extensionID)
		if enabled && (approved || errors.Is(err, ErrRestrictedRequiresApproval)) {
			securityAudit("extension.approval", outcome, "extension_id", extensionID)
		}
	}()
	if extensionID == "" {
		return fmt.Errorf("extensionID is required")
	}
	// Blacklist check — always enforced.
	if publisher, name, ok := splitExtensionID(extensionID); ok {
		if s.IsBlacklisted(publisher, name) {
			return ErrBlacklisted
		}
	}

	state := s.loadExtensionState()
	entry, ok := state.Extensions[extensionID]
	if !ok {
		return fmt.Errorf("no security info for extension %q; register the install first", extensionID)
	}

	if enabled {
		// Integrity gate: extensions whose VSIX was not checked against an
		// expected SHA-256 digest cannot be enabled.
		if !entry.IntegrityChecked {
			return ErrIntegrityNotChecked
		}
		// Restricted gate: requires explicit approval.
		if entry.Level == SecurityRestricted && !approved {
			return ErrRestrictedRequiresApproval
		}
		// Reviewed gate: also surfaces a popup, but we don't hard-block
		// it server-side because the popup is informational. The
		// frontend store is responsible for showing the dialog before
		// calling SetExtensionEnabled. Restricted is the hard gate
		// because network access is the highest-risk capability.
		entry.PendingReview = false
	}
	// Disabling always succeeds (subject to blacklist above).
	entry.Enabled = enabled

	state.Extensions[extensionID] = entry
	return s.saveExtensionState(state)
}

// ListSecurityInfo returns the security info for all registered
// extensions, with the blacklist flag refreshed from the in-memory set.
func (s *ExtensionSecurityService) ListSecurityInfo() []ExtensionSecurityInfo {
	state := s.loadExtensionState()
	out := make([]ExtensionSecurityInfo, 0, len(state.Extensions))
	for id, entry := range state.Extensions {
		blacklisted := false
		if publisher, name, ok := splitExtensionID(id); ok {
			blacklisted = s.IsBlacklisted(publisher, name)
		}
		out = append(out, ExtensionSecurityInfo{
			ExtensionID:      id,
			Level:            entry.Level,
			Permissions:      append([]ExtensionPermission{}, entry.Permissions...),
			SHA256:           entry.SHA256,
			IntegrityChecked: entry.IntegrityChecked,
			Verified:         entry.IntegrityChecked, // Deprecated compatibility alias.
			Enabled:          entry.Enabled,
			Blacklisted:      blacklisted,
			PendingReview:    entry.PendingReview,
		})
	}
	return out
}

// CanInstall is a pre-install gate (G-VSC-03 requirement 3). Returns
// ErrBlacklisted if the extension is on the known-malicious list, nil
// otherwise. The frontend should call this before downloading a VSIX.
func (s *ExtensionSecurityService) CanInstall(publisher, name string) error {
	if s.IsBlacklisted(publisher, name) {
		return ErrBlacklisted
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal: persistence helpers
// ---------------------------------------------------------------------------

func (s *ExtensionSecurityService) saveSecurityInfo(info *ExtensionSecurityInfo) error {
	state := s.loadExtensionState()
	if state.Extensions == nil {
		state.Extensions = make(map[string]extensionSecurityStateEntry)
	}
	state.Extensions[info.ExtensionID] = extensionSecurityStateEntry{
		Level:            info.Level,
		Permissions:      append([]ExtensionPermission{}, info.Permissions...),
		SHA256:           info.SHA256,
		IntegrityChecked: info.IntegrityChecked,
		Verified:         info.IntegrityChecked, // Deprecated compatibility alias.
		Enabled:          info.Enabled,
		PendingReview:    info.PendingReview,
	}
	return s.saveExtensionState(state)
}

func (s *ExtensionSecurityService) loadExtensionState() extensionSecurityStateFile {
	if s.configDir == "" {
		return extensionSecurityStateFile{Extensions: map[string]extensionSecurityStateEntry{}}
	}
	path := filepath.Join(s.configDir, "koyori-ide", extensionSecurityStateFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return extensionSecurityStateFile{Extensions: map[string]extensionSecurityStateEntry{}}
	}
	var state extensionSecurityStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return extensionSecurityStateFile{Extensions: map[string]extensionSecurityStateEntry{}}
	}
	if state.Extensions == nil {
		state.Extensions = map[string]extensionSecurityStateEntry{}
	}
	return state
}

func (s *ExtensionSecurityService) saveExtensionState(state extensionSecurityStateFile) error {
	if s.configDir == "" {
		return fmt.Errorf("user config directory is not configured")
	}
	path := filepath.Join(s.configDir, "koyori-ide", extensionSecurityStateFileName)
	// M-5: atomic write (temp+rename+0600) prevents half-written state.
	return atomicWriteJSON(path, state, 0600)
}

// normalizeExtensionID builds the canonical "<publisher>.<name>" form,
// lowercased and trimmed. Handles the case where the caller already
// passed the combined id (publisher="publisher.name", name="").
func normalizeExtensionID(publisher, name string) string {
	p := strings.ToLower(strings.TrimSpace(publisher))
	n := strings.ToLower(strings.TrimSpace(name))
	if p == "" && n == "" {
		return ""
	}
	if n == "" {
		// publisher already holds the full id.
		return p
	}
	if p == "" {
		return n
	}
	return p + "." + n
}

// splitExtensionID splits "<publisher>.<name>" into its parts. Returns
// ok=false if the id doesn't contain a dot.
func splitExtensionID(id string) (publisher, name string, ok bool) {
	id = strings.TrimSpace(id)
	idx := strings.Index(id, ".")
	if idx <= 0 || idx == len(id)-1 {
		return "", "", false
	}
	return id[:idx], id[idx+1:], true
}
