package services

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	UpdateManifestSchemaVersion  = 1
	DefaultManifestMaxAge        = 7 * 24 * time.Hour
	DefaultManifestFutureSkew    = 5 * time.Minute
	RollbackAuthorizationDomain  = "gugacode.update.rollback.v1"
	RollbackAuthorizationPurpose = "authorize-update-rollback"
)

type UpdateManifest struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Channel        string `json:"channel"`
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
	ArtifactName   string `json:"artifactName"`
	ArtifactSHA256 string `json:"artifactSHA256"`
	CreatedAt      string `json:"createdAt"`
	KeyID          string `json:"keyId"`
	Signature      string `json:"signature"`
}

type updateManifestPayload struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Channel        string `json:"channel"`
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
	ArtifactName   string `json:"artifactName"`
	ArtifactSHA256 string `json:"artifactSHA256"`
	CreatedAt      string `json:"createdAt"`
	KeyID          string `json:"keyId"`
}

func (m UpdateManifest) CanonicalPayload() ([]byte, error) {
	return json.Marshal(updateManifestPayload{
		SchemaVersion: m.SchemaVersion, Channel: m.Channel, Version: m.Version,
		Commit: m.Commit, Platform: m.Platform, Arch: m.Arch,
		ArtifactName: m.ArtifactName, ArtifactSHA256: m.ArtifactSHA256,
		CreatedAt: m.CreatedAt, KeyID: m.KeyID,
	})
}

func SignUpdateManifest(m *UpdateManifest, privateKey ed25519.PrivateKey) error {
	if m == nil {
		return errors.New("update manifest is nil")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 private key")
	}
	payload, err := m.CanonicalPayload()
	if err != nil {
		return fmt.Errorf("canonicalize update manifest: %w", err)
	}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return nil
}

type UpdateManifestVerifier struct {
	PinnedKeys map[string]ed25519.PublicKey
	MaxAge     time.Duration
	FutureSkew time.Duration
	Now        func() time.Time

	rollbackMu       sync.Mutex
	consumedRollback map[string]struct{}
}

func NewUpdateManifestVerifier(keys map[string]ed25519.PublicKey) *UpdateManifestVerifier {
	pinned := make(map[string]ed25519.PublicKey, len(keys))
	for id, key := range keys {
		pinned[id] = append(ed25519.PublicKey(nil), key...)
	}
	return &UpdateManifestVerifier{
		PinnedKeys:       pinned,
		MaxAge:           DefaultManifestMaxAge,
		FutureSkew:       DefaultManifestFutureSkew,
		Now:              time.Now,
		consumedRollback: make(map[string]struct{}),
	}
}

func (v *UpdateManifestVerifier) VerifyManifest(m UpdateManifest) error {
	if v == nil {
		return errors.New("update manifest verifier is nil")
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	if err := validateUpdateManifestFields(m, now, v.maxAge(), v.futureSkew()); err != nil {
		return err
	}
	return v.verifySignature(m.KeyID, m.Signature, m.CanonicalPayload)
}

func (v *UpdateManifestVerifier) maxAge() time.Duration {
	if v.MaxAge <= 0 {
		return DefaultManifestMaxAge
	}
	return v.MaxAge
}

func (v *UpdateManifestVerifier) futureSkew() time.Duration {
	if v.FutureSkew < 0 {
		return DefaultManifestFutureSkew
	}
	return v.FutureSkew
}

func (v *UpdateManifestVerifier) verifySignature(keyID, encoded string, payload func() ([]byte, error)) error {
	key, ok := v.PinnedKeys[keyID]
	if !ok || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("update signature key %q is not pinned", keyID)
	}
	if encoded == "" {
		return errors.New("update signature is required")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("update signature encoding is invalid")
	}
	canonical, err := payload()
	if err != nil {
		return fmt.Errorf("canonicalize signed payload: %w", err)
	}
	if !ed25519.Verify(key, canonical, signature) {
		return errors.New("update signature verification failed")
	}
	return nil
}

func validateUpdateManifestFields(m UpdateManifest, now time.Time, maxAge, futureSkew time.Duration) error {
	if m.SchemaVersion != UpdateManifestSchemaVersion {
		return fmt.Errorf("unsupported update manifest schema version %d", m.SchemaVersion)
	}
	if m.Channel != "stable" && m.Channel != "beta" {
		return fmt.Errorf("unsupported update channel %q", m.Channel)
	}
	if _, err := parseG27Version(m.Version); err != nil {
		return fmt.Errorf("invalid update version: %w", err)
	}
	if strings.TrimSpace(m.Commit) == "" || strings.TrimSpace(m.KeyID) == "" {
		return errors.New("update commit and keyId are required")
	}
	if !knownUpdatePlatform(m.Platform) || !knownUpdateArch(m.Arch) {
		return fmt.Errorf("unsupported update target %q/%q", m.Platform, m.Arch)
	}
	if err := validateArtifactName(m.ArtifactName); err != nil {
		return err
	}
	digest, err := hex.DecodeString(m.ArtifactSHA256)
	if err != nil || len(digest) != 32 || strings.ToLower(m.ArtifactSHA256) != m.ArtifactSHA256 {
		return errors.New("artifactSHA256 must be 64 lowercase hexadecimal characters")
	}
	createdAt, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		return errors.New("createdAt must be an RFC3339 timestamp")
	}
	if createdAt.After(now.Add(futureSkew)) {
		return errors.New("update manifest createdAt is in the future")
	}
	if createdAt.Before(now.Add(-maxAge)) {
		return errors.New("update manifest has expired")
	}
	return nil
}

var windowsReservedArtifactName = regexp.MustCompile(`(?i)^(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$`)

func validateArtifactName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) ||
		strings.ContainsRune(name, ':') || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") ||
		windowsReservedArtifactName.MatchString(name) {
		return errors.New("artifactName is not a safe cross-platform base name")
	}
	for _, char := range name {
		if char < 0x20 || char == 0x7f {
			return errors.New("artifactName contains a control character")
		}
	}
	return nil
}

func knownUpdatePlatform(platform string) bool {
	switch platform {
	case "windows", "linux", "darwin":
		return true
	default:
		return false
	}
}

func knownUpdateArch(arch string) bool {
	switch arch {
	case "amd64", "arm64":
		return true
	default:
		return false
	}
}

type UpdateCandidatePolicy struct {
	CurrentVersion         string
	CurrentCommit          string
	CurrentChannel         string
	TransactionID          string
	ExpectedPlatform       string
	ExpectedArch           string
	ExpectedArtifactName   string
	ExpectedArtifactSHA256 string
}

func (v *UpdateManifestVerifier) VerifyCandidate(m UpdateManifest, policy UpdateCandidatePolicy, rollback *RollbackAuthorization) error {
	if err := v.VerifyManifest(m); err != nil {
		return err
	}
	if policy.CurrentChannel != "stable" && policy.CurrentChannel != "beta" {
		return errors.New("current update channel is invalid")
	}
	if m.Channel != policy.CurrentChannel {
		return errors.New("update channel switch is not authorized")
	}
	if m.Platform != policy.ExpectedPlatform || m.Arch != policy.ExpectedArch ||
		m.ArtifactName != policy.ExpectedArtifactName || m.ArtifactSHA256 != policy.ExpectedArtifactSHA256 {
		return errors.New("update artifact identity does not match candidate request")
	}
	comparison, err := compareG27Versions(m.Version, policy.CurrentVersion)
	if err != nil {
		return fmt.Errorf("compare candidate version: %w", err)
	}
	if comparison >= 0 {
		return nil
	}
	if rollback == nil {
		return errors.New("update downgrade requires rollback authorization")
	}
	scope := RollbackAuthorizationScope{
		TransactionID: policy.TransactionID,
		Channel:       m.Channel, Platform: m.Platform, Arch: m.Arch,
		ArtifactName: m.ArtifactName, ArtifactSHA256: m.ArtifactSHA256,
		FromVersion: policy.CurrentVersion, FromCommit: policy.CurrentCommit,
		TargetVersion: m.Version, TargetCommit: m.Commit,
	}
	return v.ConsumeRollbackAuthorization(*rollback, scope)
}

type RollbackAuthorization struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Domain         string `json:"domain"`
	Purpose        string `json:"purpose"`
	TransactionID  string `json:"transactionId"`
	Nonce          string `json:"nonce"`
	Channel        string `json:"channel"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
	ArtifactName   string `json:"artifactName"`
	ArtifactSHA256 string `json:"artifactSHA256"`
	FromVersion    string `json:"fromVersion"`
	FromCommit     string `json:"fromCommit"`
	TargetVersion  string `json:"targetVersion"`
	TargetCommit   string `json:"targetCommit"`
	Reason         string `json:"reason"`
	ExpiresAt      string `json:"expiresAt"`
	KeyID          string `json:"keyId"`
	Signature      string `json:"signature"`
}

type RollbackAuthorizationScope struct {
	TransactionID  string
	Channel        string
	Platform       string
	Arch           string
	ArtifactName   string
	ArtifactSHA256 string
	FromVersion    string
	FromCommit     string
	TargetVersion  string
	TargetCommit   string
}

type rollbackAuthorizationPayload RollbackAuthorization

func (a RollbackAuthorization) CanonicalPayload() ([]byte, error) {
	payload := rollbackAuthorizationPayload(a)
	payload.Signature = ""
	return json.Marshal(payload)
}

func SignRollbackAuthorization(a *RollbackAuthorization, privateKey ed25519.PrivateKey) error {
	if a == nil {
		return errors.New("rollback authorization is nil")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 private key")
	}
	payload, err := a.CanonicalPayload()
	if err != nil {
		return fmt.Errorf("canonicalize rollback authorization: %w", err)
	}
	a.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return nil
}

func (v *UpdateManifestVerifier) VerifyRollbackAuthorization(a RollbackAuthorization, scope RollbackAuthorizationScope) error {
	if err := v.verifyRollbackAuthorization(a, scope); err != nil {
		return err
	}
	v.rollbackMu.Lock()
	defer v.rollbackMu.Unlock()
	if _, consumed := v.consumedRollback[a.Nonce]; consumed {
		return errors.New("rollback authorization has already been consumed")
	}
	return nil
}

func (v *UpdateManifestVerifier) ConsumeRollbackAuthorization(a RollbackAuthorization, scope RollbackAuthorizationScope) error {
	if err := v.verifyRollbackAuthorization(a, scope); err != nil {
		return err
	}
	v.rollbackMu.Lock()
	defer v.rollbackMu.Unlock()
	if v.consumedRollback == nil {
		v.consumedRollback = make(map[string]struct{})
	}
	if _, consumed := v.consumedRollback[a.Nonce]; consumed {
		return errors.New("rollback authorization has already been consumed")
	}
	v.consumedRollback[a.Nonce] = struct{}{}
	return nil
}

func (v *UpdateManifestVerifier) verifyRollbackAuthorization(a RollbackAuthorization, scope RollbackAuthorizationScope) error {
	if v == nil {
		return errors.New("update manifest verifier is nil")
	}
	if a.SchemaVersion != UpdateManifestSchemaVersion {
		return fmt.Errorf("unsupported rollback authorization schema version %d", a.SchemaVersion)
	}
	if _, err := parseG27Version(a.FromVersion); err != nil {
		return fmt.Errorf("invalid rollback source version: %w", err)
	}
	if _, err := parseG27Version(a.TargetVersion); err != nil {
		return fmt.Errorf("invalid rollback target version: %w", err)
	}
	if a.Domain != RollbackAuthorizationDomain || a.Purpose != RollbackAuthorizationPurpose {
		return errors.New("rollback authorization has invalid domain or purpose")
	}
	if strings.TrimSpace(scope.TransactionID) == "" || strings.TrimSpace(scope.Channel) == "" ||
		strings.TrimSpace(scope.Platform) == "" || strings.TrimSpace(scope.Arch) == "" ||
		strings.TrimSpace(scope.ArtifactName) == "" || strings.TrimSpace(scope.ArtifactSHA256) == "" ||
		strings.TrimSpace(scope.FromVersion) == "" || strings.TrimSpace(scope.FromCommit) == "" ||
		strings.TrimSpace(scope.TargetVersion) == "" || strings.TrimSpace(scope.TargetCommit) == "" {
		return errors.New("complete rollback authorization scope is required")
	}
	if a.TransactionID != scope.TransactionID || a.Channel != scope.Channel ||
		a.Platform != scope.Platform || a.Arch != scope.Arch || a.ArtifactName != scope.ArtifactName ||
		a.ArtifactSHA256 != scope.ArtifactSHA256 || a.FromVersion != scope.FromVersion ||
		a.FromCommit != scope.FromCommit || a.TargetVersion != scope.TargetVersion || a.TargetCommit != scope.TargetCommit {
		return errors.New("rollback authorization does not bind requested scope")
	}
	if strings.TrimSpace(a.Nonce) == "" || strings.TrimSpace(a.Reason) == "" || strings.TrimSpace(a.KeyID) == "" {
		return errors.New("rollback nonce, reason, and keyId are required")
	}
	expiresAt, err := time.Parse(time.RFC3339, a.ExpiresAt)
	if err != nil {
		return errors.New("rollback expiresAt must be an RFC3339 timestamp")
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	if !expiresAt.After(now) {
		return errors.New("rollback authorization has expired")
	}
	return v.verifySignature(a.KeyID, a.Signature, a.CanonicalPayload)
}

var g27VersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

type g27Version struct {
	major, minor, patch uint64
	pre                 []string
}

func parseG27Version(raw string) (g27Version, error) {
	matches := g27VersionPattern.FindStringSubmatch(strings.TrimPrefix(raw, "v"))
	if matches == nil {
		return g27Version{}, fmt.Errorf("%q is not a supported semantic version", raw)
	}
	parts := make([]uint64, 3)
	for i := range parts {
		value, err := strconv.ParseUint(matches[i+1], 10, 64)
		if err != nil {
			return g27Version{}, fmt.Errorf("version component is out of range")
		}
		parts[i] = value
	}
	version := g27Version{major: parts[0], minor: parts[1], patch: parts[2]}
	if matches[4] != "" {
		version.pre = strings.Split(matches[4], ".")
		for _, identifier := range version.pre {
			if len(identifier) > 1 && identifier[0] == '0' && identifier[0] >= '0' && identifier[0] <= '9' {
				allNumeric := true
				for _, char := range identifier {
					allNumeric = allNumeric && char >= '0' && char <= '9'
				}
				if allNumeric {
					return g27Version{}, errors.New("numeric prerelease identifier has a leading zero")
				}
			}
		}
	}
	return version, nil
}

func compareG27Versions(left, right string) (int, error) {
	a, err := parseG27Version(left)
	if err != nil {
		return 0, err
	}
	b, err := parseG27Version(right)
	if err != nil {
		return 0, err
	}
	for _, pair := range [][2]uint64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1, nil
		}
		if pair[0] > pair[1] {
			return 1, nil
		}
	}
	if len(a.pre) == 0 && len(b.pre) == 0 {
		return 0, nil
	}
	if len(a.pre) == 0 {
		return 1, nil
	}
	if len(b.pre) == 0 {
		return -1, nil
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := comparePrereleaseIdentifier(a.pre[i], b.pre[i]); c != 0 {
			return c, nil
		}
	}
	if len(a.pre) < len(b.pre) {
		return -1, nil
	}
	if len(a.pre) > len(b.pre) {
		return 1, nil
	}
	return 0, nil
}

func comparePrereleaseIdentifier(a, b string) int {
	ai, aerr := strconv.ParseUint(a, 10, 64)
	bi, berr := strconv.ParseUint(b, 10, 64)
	if aerr == nil && berr == nil {
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
		return 0
	}
	if aerr == nil {
		return -1
	}
	if berr == nil {
		return 1
	}
	return strings.Compare(a, b)
}

type UpdateTransactionState string

const (
	UpdateTransactionPrepared         UpdateTransactionState = "prepared"
	UpdateTransactionStaged           UpdateTransactionState = "staged"
	UpdateTransactionActivated        UpdateTransactionState = "activated"
	UpdateTransactionCommitted        UpdateTransactionState = "committed"
	UpdateTransactionRollbackRequired UpdateTransactionState = "rollback-required"
	UpdateTransactionRolledBack       UpdateTransactionState = "rolled-back"
	UpdateTransactionRollbackFailed   UpdateTransactionState = "rollback-failed"
)

type UpdateTransactionJournal struct {
	TransactionID string                 `json:"transactionId"`
	State         UpdateTransactionState `json:"state"`
	Version       string                 `json:"version"`
	ArtifactName  string                 `json:"artifactName"`
}

func (j *UpdateTransactionJournal) Transition(next UpdateTransactionState) error {
	if j == nil {
		return errors.New("update transaction journal is nil")
	}
	allowed := map[UpdateTransactionState]map[UpdateTransactionState]bool{
		UpdateTransactionPrepared: {
			UpdateTransactionStaged:           true,
			UpdateTransactionRollbackRequired: true,
		},
		UpdateTransactionStaged: {
			UpdateTransactionActivated:        true,
			UpdateTransactionRollbackRequired: true,
		},
		UpdateTransactionActivated: {
			UpdateTransactionCommitted:        true,
			UpdateTransactionRollbackRequired: true,
		},
		UpdateTransactionRollbackRequired: {
			UpdateTransactionRolledBack:     true,
			UpdateTransactionRollbackFailed: true,
		},
	}
	if !allowed[j.State][next] {
		return fmt.Errorf("illegal update transaction transition %q -> %q", j.State, next)
	}
	j.State = next
	return nil
}
