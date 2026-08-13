package services

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	languagePackStateSchema        = 1
	languagePackSignatureFormat    = "koyori-language-pack-v1"
	languagePackSignatureAlgorithm = "ed25519"
	maxLanguagePackArchiveBytes    = 16 << 20
	maxLanguagePackArchiveEntries  = 8
	maxLanguagePackSignatureBytes  = 16 << 10
)

type languagePackSignature struct {
	Format         string `json:"format"`
	Algorithm      string `json:"algorithm"`
	KeyID          string `json:"keyId"`
	PublicKey      string `json:"publicKey,omitempty"`
	PackID         string `json:"packId"`
	Version        string `json:"version"`
	ManifestSHA256 string `json:"manifestSha256"`
	Signature      string `json:"signature"`
}

type languagePackVersionState struct {
	Version        string `json:"version"`
	ManifestSHA256 string `json:"manifestSha256"`
	ArchiveSHA256  string `json:"archiveSha256"`
	PublisherKeyID string `json:"publisherKeyId"`
	InstalledAt    string `json:"installedAt"`
}

type languagePackRecordState struct {
	ActiveVersion string                     `json:"activeVersion"`
	Enabled       bool                       `json:"enabled"`
	Versions      []languagePackVersionState `json:"versions"`
}

type languagePackPublisherState struct {
	PublicKey string `json:"publicKey"`
	TrustedAt string `json:"trustedAt"`
}

type languagePackDiskState struct {
	SchemaVersion     int                                   `json:"schemaVersion"`
	Packs             map[string]languagePackRecordState    `json:"packs"`
	TrustedPublishers map[string]languagePackPublisherState `json:"trustedPublishers"`
}

// LanguagePackInfo is the renderer-safe inventory shape. It never exposes an
// install path, public key, executable path, or mutable manifest object.
type LanguagePackInfo struct {
	ID             string   `json:"id"`
	Version        string   `json:"version"`
	DisplayName    string   `json:"displayName"`
	Languages      []string `json:"languages"`
	BuiltIn        bool     `json:"builtIn"`
	Enabled        bool     `json:"enabled"`
	Active         bool     `json:"active"`
	ManifestSHA256 string   `json:"manifestSha256"`
	ArchiveSHA256  string   `json:"archiveSha256,omitempty"`
	PublisherKeyID string   `json:"publisherKeyId,omitempty"`
	InstalledAt    string   `json:"installedAt,omitempty"`
}

// LanguagePackLanguageContribution is the renderer-safe selector subset of an
// active external pack. Process declarations and installation metadata remain
// backend-only.
type LanguagePackLanguageContribution struct {
	ID         string   `json:"id"`
	Extensions []string `json:"extensions"`
	Filenames  []string `json:"filenames"`
}

// LanguagePackRuntimeContribution is an immutable-by-value snapshot used by
// the renderer for Monaco language detection. It carries no process authority.
type LanguagePackRuntimeContribution struct {
	ID             string                             `json:"id"`
	Version        string                             `json:"version"`
	ManifestSHA256 string                             `json:"manifestSha256"`
	Languages      []LanguagePackLanguageContribution `json:"languages"`
}

type verifiedLanguagePackArchive struct {
	manifest      languagePackManifest
	manifestRaw   []byte
	signature     languagePackSignature
	archiveSHA256 string
}

type languagePackPublisherApproval struct {
	KeyID         string
	Fingerprint   string
	PackID        string
	Version       string
	ArchiveSHA256 string
}

// LanguagePackService owns native installation and persisted version state.
// Process launch remains in the LSP/toolchain/debug brokers.
type LanguagePackService struct {
	mu               sync.Mutex
	root             string
	state            languagePackDiskState
	lastError        string
	approvePublisher func(languagePackPublisherApproval) bool
	approveChange    func(title, id string) bool
}

func NewLanguagePackService(configDir string) *LanguagePackService {
	if strings.TrimSpace(configDir) == "" {
		if userConfig, err := os.UserConfigDir(); err == nil {
			configDir = filepath.Join(userConfig, "koyori-ide")
		}
	}
	service := &LanguagePackService{
		root: filepath.Join(configDir, "language-packs"),
		state: languagePackDiskState{
			SchemaVersion:     languagePackStateSchema,
			Packs:             make(map[string]languagePackRecordState),
			TrustedPublishers: make(map[string]languagePackPublisherState),
		},
	}
	if err := service.loadAndActivate(); err != nil {
		service.lastError = err.Error()
		setActiveExternalLanguagePacks(nil)
		slog.Error("external language packs disabled", "err", err)
	}
	return service
}

func (s *LanguagePackService) GetLastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastError
}

func (s *LanguagePackService) ListLanguagePacks() []LanguagePackInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	infos := make([]LanguagePackInfo, 0, len(builtInLanguagePacks)+len(s.state.Packs))
	for _, manifest := range builtInLanguagePacks {
		infos = append(infos, languagePackInfoFromManifest(manifest, true, true, true, languagePackVersionState{}))
	}
	for id, record := range s.state.Packs {
		for _, version := range record.Versions {
			manifest, err := s.readInstalledManifest(id, version.Version)
			if err != nil {
				continue
			}
			infos = append(infos, languagePackInfoFromManifest(
				manifest, false, record.Enabled && record.ActiveVersion == version.Version,
				record.ActiveVersion == version.Version, version,
			))
		}
	}
	sort.Slice(infos, func(left, right int) bool {
		if infos[left].ID == infos[right].ID {
			return compareLanguagePackVersions(infos[left].Version, infos[right].Version) > 0
		}
		return infos[left].ID < infos[right].ID
	})
	return infos
}

// ListActiveExternalLanguagePackContributions returns only signature-verified,
// enabled external selectors. A state or signature failure is returned instead
// of exposing a stale or partially validated snapshot.
func (s *LanguagePackService) ListActiveExternalLanguagePackContributions() ([]LanguagePackRuntimeContribution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	manifests, err := s.activeManifestsLocked()
	if err != nil {
		s.lastError = err.Error()
		return nil, err
	}
	contributions := make([]LanguagePackRuntimeContribution, 0, len(manifests))
	for _, manifest := range manifests {
		languages := make([]LanguagePackLanguageContribution, 0, len(manifest.Languages))
		for _, language := range manifest.Languages {
			languages = append(languages, LanguagePackLanguageContribution{
				ID:         language.ID,
				Extensions: append([]string(nil), language.Extensions...),
				Filenames:  append([]string(nil), language.Filenames...),
			})
		}
		contributions = append(contributions, LanguagePackRuntimeContribution{
			ID:             manifest.ID,
			Version:        manifest.Version,
			ManifestSHA256: manifest.Integrity.ManifestSha256,
			Languages:      languages,
		})
	}
	return contributions, nil
}

func languagePackInfoFromManifest(manifest languagePackManifest, builtIn, enabled, active bool, version languagePackVersionState) LanguagePackInfo {
	languages := make([]string, 0, len(manifest.Languages))
	for _, language := range manifest.Languages {
		languages = append(languages, language.ID)
	}
	return LanguagePackInfo{
		ID: manifest.ID, Version: manifest.Version, DisplayName: manifest.DisplayName,
		Languages: languages, BuiltIn: builtIn, Enabled: enabled, Active: active,
		ManifestSHA256: manifest.Integrity.ManifestSha256, ArchiveSHA256: version.ArchiveSHA256,
		PublisherKeyID: version.PublisherKeyID, InstalledAt: version.InstalledAt,
	}
}

// InstallLanguagePack opens a native file picker. A renderer-provided path or
// approval boolean is intentionally not accepted as authorization.
func (s *LanguagePackService) InstallLanguagePack() (LanguagePackInfo, error) {
	app := application.Get()
	if app == nil {
		return LanguagePackInfo{}, fmt.Errorf("native language pack picker is unavailable: %w", ErrNotAllowed)
	}
	dialog := app.Dialog.OpenFile().SetTitle("Install Koyori IDE Language Pack")
	dialog.CanChooseFiles(true).CanChooseDirectories(false).AllowsOtherFileTypes(false)
	dialog.AddFilter("Koyori IDE Language Pack", "*.koyori-language-pack")
	selected, err := dialog.PromptForSingleSelection()
	if err != nil {
		return LanguagePackInfo{}, err
	}
	if selected == "" {
		return LanguagePackInfo{}, errors.New("language pack installation canceled")
	}
	return s.installFromNativePath(selected)
}

func (s *LanguagePackService) DisableLanguagePack(id string) error {
	s.mu.Lock()
	record, ok := s.state.Packs[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("language pack %q is not installed", id)
	}
	if !record.Enabled {
		return nil
	}
	if !s.approveLanguagePackChange("Disable language pack", id+"@"+record.ActiveVersion) {
		return fmt.Errorf("language pack disable was not approved: %w", ErrNotAllowed)
	}
	return s.disableTrustedVersion(id, record.ActiveVersion)
}

func (s *LanguagePackService) disableTrusted(id string) error {
	return s.disableTrustedVersion(id, "")
}

func (s *LanguagePackService) disableTrustedVersion(id, expectedVersion string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.state.Packs[id]
	if !ok {
		return fmt.Errorf("language pack %q is not installed", id)
	}
	if expectedVersion != "" && record.ActiveVersion != expectedVersion {
		return fmt.Errorf("language pack %q active version changed after approval: %w", id, ErrInvalidInput)
	}
	if !record.Enabled {
		return nil
	}
	record.Enabled = false
	s.state.Packs[id] = record
	if err := s.persistStateLocked(); err != nil {
		record.Enabled = true
		s.state.Packs[id] = record
		return err
	}
	return s.activateLocked()
}

// InstallLanguagePackFromTrustedPath is a package-level test/evidence seam.
// Wails only reflects methods on registered service instances, so this helper
// cannot become a renderer capability.
func InstallLanguagePackFromTrustedPath(s *LanguagePackService, path string) (LanguagePackInfo, error) {
	if s == nil {
		return LanguagePackInfo{}, errors.New("language pack service is unavailable")
	}
	return s.installFromTrustedPath(path)
}

func (s *LanguagePackService) EnableLanguagePack(id string) error {
	if !s.approveLanguagePackChange("Enable language pack", id) {
		return fmt.Errorf("language pack enable was not approved: %w", ErrNotAllowed)
	}
	return s.enableTrusted(id)
}

func (s *LanguagePackService) enableTrusted(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.state.Packs[id]
	if !ok || record.ActiveVersion == "" {
		return fmt.Errorf("language pack %q is not installed", id)
	}
	previous := record.Enabled
	record.Enabled = true
	s.state.Packs[id] = record
	if _, err := s.activeManifestsLocked(); err != nil {
		record.Enabled = previous
		s.state.Packs[id] = record
		return err
	}
	if err := s.persistStateLocked(); err != nil {
		record.Enabled = previous
		s.state.Packs[id] = record
		return err
	}
	return s.activateLocked()
}

func (s *LanguagePackService) RollbackLanguagePack(id string) (LanguagePackInfo, error) {
	if !s.approveLanguagePackChange("Roll back language pack", id) {
		return LanguagePackInfo{}, fmt.Errorf("language pack rollback was not approved: %w", ErrNotAllowed)
	}
	return s.rollbackTrusted(id)
}

func (s *LanguagePackService) rollbackTrusted(id string) (LanguagePackInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.state.Packs[id]
	if !ok {
		return LanguagePackInfo{}, fmt.Errorf("language pack %q is not installed", id)
	}
	previous := ""
	for _, candidate := range record.Versions {
		if compareLanguagePackVersions(candidate.Version, record.ActiveVersion) < 0 &&
			(previous == "" || compareLanguagePackVersions(candidate.Version, previous) > 0) {
			previous = candidate.Version
		}
	}
	if previous == "" {
		return LanguagePackInfo{}, fmt.Errorf("language pack %q has no previous version", id)
	}
	oldVersion, oldEnabled := record.ActiveVersion, record.Enabled
	record.ActiveVersion, record.Enabled = previous, true
	s.state.Packs[id] = record
	if _, err := s.activeManifestsLocked(); err != nil {
		record.ActiveVersion, record.Enabled = oldVersion, oldEnabled
		s.state.Packs[id] = record
		return LanguagePackInfo{}, err
	}
	if err := s.persistStateLocked(); err != nil {
		record.ActiveVersion, record.Enabled = oldVersion, oldEnabled
		s.state.Packs[id] = record
		return LanguagePackInfo{}, err
	}
	if err := s.activateLocked(); err != nil {
		return LanguagePackInfo{}, err
	}
	manifest, err := s.readInstalledManifest(id, previous)
	if err != nil {
		return LanguagePackInfo{}, err
	}
	version, _ := findLanguagePackVersion(record, previous)
	return languagePackInfoFromManifest(manifest, false, true, true, version), nil
}

func (s *LanguagePackService) UninstallLanguagePack(id string) error {
	s.mu.Lock()
	record, ok := s.state.Packs[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("language pack %q is not installed", id)
	}
	if !s.approveLanguagePackChange("Uninstall language pack", id+"@"+record.ActiveVersion) {
		return fmt.Errorf("language pack uninstall was not approved: %w", ErrNotAllowed)
	}
	return s.uninstallTrustedVersion(id, record.ActiveVersion)
}

func (s *LanguagePackService) uninstallTrusted(id string) error {
	return s.uninstallTrustedVersion(id, "")
}

func (s *LanguagePackService) uninstallTrustedVersion(id, expectedVersion string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.state.Packs[id]
	if !ok {
		return fmt.Errorf("language pack %q is not installed", id)
	}
	if expectedVersion != "" && record.ActiveVersion != expectedVersion {
		return fmt.Errorf("language pack %q active version changed after approval: %w", id, ErrInvalidInput)
	}
	targetVersion := record.ActiveVersion
	oldRecord := record
	remaining := make([]languagePackVersionState, 0, len(record.Versions)-1)
	for _, version := range record.Versions {
		if version.Version != targetVersion {
			remaining = append(remaining, version)
		}
	}
	if len(remaining) == 0 {
		delete(s.state.Packs, id)
	} else {
		record.Versions = remaining
		record.ActiveVersion = highestLanguagePackVersion(remaining)
		record.Enabled = true
		s.state.Packs[id] = record
	}
	if err := s.persistStateLocked(); err != nil {
		if len(remaining) == 0 {
			s.state.Packs[id] = record
		} else {
			s.state.Packs[id] = oldRecord
		}
		return err
	}
	if err := s.activateLocked(); err != nil {
		return err
	}
	removePath := filepath.Join(s.root, id, targetVersion)
	if err := ensurePathWithin(s.root, removePath); err != nil {
		return err
	}
	if err := os.RemoveAll(removePath); err != nil {
		return fmt.Errorf("remove language pack version: %w", err)
	}
	return nil
}

func nativeApproveLanguagePackChange(title, id string) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle(title).SetMessage(fmt.Sprintf("Apply this change to signed language pack %s?", id))
	dialog.AddButton("Apply").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("Cancel").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

func nativeApproveLanguagePackPublisher(approval languagePackPublisherApproval) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	message := fmt.Sprintf(
		"Trust publisher %s and install %s@%s?\n\nPublic key SHA-256: %s\nArchive SHA-256: %s",
		approval.KeyID,
		approval.PackID,
		approval.Version,
		approval.Fingerprint,
		approval.ArchiveSHA256,
	)
	dialog := app.Dialog.Question().SetTitle("Trust language pack publisher").SetMessage(message)
	dialog.AddButton("Trust and install").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("Cancel").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

func (s *LanguagePackService) approveLanguagePackChange(title, id string) bool {
	if s.approveChange != nil {
		return s.approveChange(title, id)
	}
	return nativeApproveLanguagePackChange(title, id)
}

func (s *LanguagePackService) installFromTrustedPath(path string) (LanguagePackInfo, error) {
	archive, err := readLanguagePackArchive(path)
	if err != nil {
		return LanguagePackInfo{}, err
	}
	return s.installArchive(archive, "")
}

func (s *LanguagePackService) installFromNativePath(path string) (LanguagePackInfo, error) {
	archive, err := readLanguagePackArchive(path)
	if err != nil {
		return LanguagePackInfo{}, err
	}
	s.mu.Lock()
	trusted, alreadyTrusted := s.state.TrustedPublishers[archive.signature.KeyID]
	s.mu.Unlock()
	if alreadyTrusted {
		if archive.signature.PublicKey != "" && !strings.EqualFold(archive.signature.PublicKey, trusted.PublicKey) {
			return LanguagePackInfo{}, fmt.Errorf("language pack publisher %q attempted to change its public key", archive.signature.KeyID)
		}
		return s.installArchive(archive, "")
	}
	if archive.signature.PublicKey == "" {
		return LanguagePackInfo{}, fmt.Errorf("language pack publisher %q is not trusted and did not provide a public key", archive.signature.KeyID)
	}
	if err := verifyLanguagePackManifestSignatureWithKey(archive.manifest, archive.signature, archive.signature.PublicKey); err != nil {
		return LanguagePackInfo{}, err
	}
	fingerprint, err := languagePackPublicKeyFingerprint(archive.signature.PublicKey)
	if err != nil {
		return LanguagePackInfo{}, err
	}
	approval := languagePackPublisherApproval{
		KeyID: archive.signature.KeyID, Fingerprint: fingerprint,
		PackID: archive.manifest.ID, Version: archive.manifest.Version,
		ArchiveSHA256: archive.archiveSHA256,
	}
	approver := s.approvePublisher
	if approver == nil {
		approver = nativeApproveLanguagePackPublisher
	}
	if !approver(approval) {
		return LanguagePackInfo{}, fmt.Errorf("language pack publisher trust was not approved: %w", ErrNotAllowed)
	}
	return s.installArchive(archive, archive.signature.PublicKey)
}

func (s *LanguagePackService) installArchive(archive verifiedLanguagePackArchive, approvedPublicKey string) (LanguagePackInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	trusted, trustedBefore := s.state.TrustedPublishers[archive.signature.KeyID]
	if !trustedBefore {
		if approvedPublicKey == "" || !strings.EqualFold(approvedPublicKey, archive.signature.PublicKey) {
			return LanguagePackInfo{}, fmt.Errorf("language pack publisher %q is not trusted", archive.signature.KeyID)
		}
		trusted = languagePackPublisherState{
			PublicKey: strings.ToLower(approvedPublicKey),
			TrustedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		s.state.TrustedPublishers[archive.signature.KeyID] = trusted
	} else if archive.signature.PublicKey != "" && !strings.EqualFold(archive.signature.PublicKey, trusted.PublicKey) {
		return LanguagePackInfo{}, fmt.Errorf("language pack publisher %q attempted to change its public key", archive.signature.KeyID)
	}
	publisherPersisted := trustedBefore
	if !trustedBefore {
		defer func() {
			if !publisherPersisted {
				delete(s.state.TrustedPublishers, archive.signature.KeyID)
			}
		}()
	}
	if err := verifyLanguagePackManifestSignatureWithKey(archive.manifest, archive.signature, trusted.PublicKey); err != nil {
		return LanguagePackInfo{}, err
	}
	if err := s.validateCandidateLocked(archive.manifest); err != nil {
		return LanguagePackInfo{}, err
	}
	currentRecord := s.state.Packs[archive.manifest.ID]
	if currentRecord.ActiveVersion != "" && compareLanguagePackVersions(archive.manifest.Version, currentRecord.ActiveVersion) < 0 {
		return LanguagePackInfo{}, fmt.Errorf(
			"language pack installer downgrade from %s to %s is not allowed; use the approved rollback operation",
			currentRecord.ActiveVersion,
			archive.manifest.Version,
		)
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return LanguagePackInfo{}, fmt.Errorf("create language pack root: %w", err)
	}
	finalDir := filepath.Join(s.root, archive.manifest.ID, archive.manifest.Version)
	if err := ensurePathWithin(s.root, finalDir); err != nil {
		return LanguagePackInfo{}, err
	}
	newInstall := false
	if _, err := os.Stat(finalDir); errors.Is(err, os.ErrNotExist) {
		stage, stageErr := os.MkdirTemp(s.root, ".install-")
		if stageErr != nil {
			return LanguagePackInfo{}, stageErr
		}
		defer func() {
			_ = os.RemoveAll(stage)
		}()
		if err := os.WriteFile(filepath.Join(stage, "manifest.json"), archive.manifestRaw, 0o600); err != nil {
			return LanguagePackInfo{}, err
		}
		metadata, err := json.MarshalIndent(archive.signature, "", "  ")
		if err != nil {
			return LanguagePackInfo{}, err
		}
		if err := os.WriteFile(filepath.Join(stage, "signature.json"), append(metadata, '\n'), 0o600); err != nil {
			return LanguagePackInfo{}, err
		}
		if err := os.MkdirAll(filepath.Dir(finalDir), 0o700); err != nil {
			return LanguagePackInfo{}, err
		}
		if err := os.Rename(stage, finalDir); err != nil {
			return LanguagePackInfo{}, fmt.Errorf("publish language pack: %w", err)
		}
		newInstall = true
	} else if err != nil {
		return LanguagePackInfo{}, err
	}

	record := s.state.Packs[archive.manifest.ID]
	oldRecord := record
	version, exists := findLanguagePackVersion(record, archive.manifest.Version)
	if exists && (version.ArchiveSHA256 != archive.archiveSHA256 || version.ManifestSHA256 != archive.manifest.Integrity.ManifestSha256) {
		if newInstall {
			_ = os.RemoveAll(finalDir)
		}
		return LanguagePackInfo{}, errors.New("installed language pack version does not match the selected archive")
	}
	if !exists {
		version = languagePackVersionState{
			Version: archive.manifest.Version, ManifestSHA256: archive.manifest.Integrity.ManifestSha256,
			ArchiveSHA256: archive.archiveSHA256, PublisherKeyID: archive.signature.KeyID,
			InstalledAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		record.Versions = append(record.Versions, version)
	}
	record.ActiveVersion = archive.manifest.Version
	record.Enabled = true
	s.state.Packs[archive.manifest.ID] = record
	if _, err := s.activeManifestsLocked(); err != nil {
		s.state.Packs[archive.manifest.ID] = oldRecord
		if newInstall {
			_ = os.RemoveAll(finalDir)
		}
		return LanguagePackInfo{}, err
	}
	if err := s.persistStateLocked(); err != nil {
		s.state.Packs[archive.manifest.ID] = oldRecord
		if newInstall {
			_ = os.RemoveAll(finalDir)
		}
		return LanguagePackInfo{}, err
	}
	publisherPersisted = true
	if err := s.activateLocked(); err != nil {
		return LanguagePackInfo{}, err
	}
	return languagePackInfoFromManifest(archive.manifest, false, true, true, version), nil
}

func (s *LanguagePackService) loadAndActivate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	statePath := filepath.Join(s.root, "state.json")
	raw, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		setActiveExternalLanguagePacks(nil)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read language pack state: %w", err)
	}
	if len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("language pack state has an invalid size")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return fmt.Errorf("decode language pack state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var state languagePackDiskState
	if err := decoder.Decode(&state); err != nil {
		return fmt.Errorf("decode language pack state: %w", err)
	}
	if state.SchemaVersion != languagePackStateSchema || state.Packs == nil {
		return errors.New("unsupported language pack state schema")
	}
	if state.TrustedPublishers == nil {
		state.TrustedPublishers = make(map[string]languagePackPublisherState)
	}
	for keyID, publisher := range state.TrustedPublishers {
		if !languagePackIDPattern.MatchString(keyID) {
			return fmt.Errorf("trusted language pack publisher %q has invalid metadata", keyID)
		}
		if _, err := time.Parse(time.RFC3339Nano, publisher.TrustedAt); err != nil {
			return fmt.Errorf("trusted language pack publisher %q has an invalid trust timestamp", keyID)
		}
		if _, err := languagePackPublicKeyFingerprint(publisher.PublicKey); err != nil {
			return fmt.Errorf("trusted language pack publisher %q has an invalid public key: %w", keyID, err)
		}
	}
	s.state = state
	return s.activateLocked()
}

func (s *LanguagePackService) activateLocked() error {
	manifests, err := s.activeManifestsLocked()
	if err != nil {
		s.lastError = err.Error()
		setActiveExternalLanguagePacks(nil)
		return err
	}
	s.lastError = ""
	setActiveExternalLanguagePacks(manifests)
	return nil
}

func (s *LanguagePackService) activeManifestsLocked() ([]languagePackManifest, error) {
	manifests := make([]languagePackManifest, 0, len(s.state.Packs))
	for id, record := range s.state.Packs {
		if !languagePackIDPattern.MatchString(id) || record.ActiveVersion == "" || len(record.Versions) == 0 {
			return nil, fmt.Errorf("language pack state for %q is invalid", id)
		}
		version, ok := findLanguagePackVersion(record, record.ActiveVersion)
		if !ok || !languagePackSemverPattern.MatchString(version.Version) || !languagePackSHA256Pattern.MatchString(version.ManifestSHA256) || !languagePackSHA256Pattern.MatchString(version.ArchiveSHA256) {
			return nil, fmt.Errorf("language pack state for %q has an invalid active version", id)
		}
		if !record.Enabled {
			continue
		}
		manifest, err := s.readInstalledManifest(id, record.ActiveVersion)
		if err != nil {
			return nil, err
		}
		if manifest.ID != id || manifest.Version != record.ActiveVersion || manifest.Integrity.ManifestSha256 != version.ManifestSHA256 {
			return nil, fmt.Errorf("installed language pack %q does not match state", id)
		}
		signature, err := s.readInstalledSignature(id, record.ActiveVersion)
		if err != nil {
			return nil, err
		}
		if signature.KeyID != version.PublisherKeyID {
			return nil, fmt.Errorf("installed language pack %q publisher does not match state", id)
		}
		publisher, trusted := s.state.TrustedPublishers[signature.KeyID]
		if !trusted {
			return nil, fmt.Errorf("installed language pack %q publisher %q is not trusted", id, signature.KeyID)
		}
		if signature.PublicKey != "" && !strings.EqualFold(signature.PublicKey, publisher.PublicKey) {
			return nil, fmt.Errorf("installed language pack %q publisher key does not match trust state", id)
		}
		if err := verifyLanguagePackManifestSignatureWithKey(manifest, signature, publisher.PublicKey); err != nil {
			return nil, fmt.Errorf("installed language pack %q signature is invalid: %w", id, err)
		}
		manifests = append(manifests, manifest)
	}
	if err := validateExternalLanguagePackSet(manifests); err != nil {
		return nil, err
	}
	sort.Slice(manifests, func(left, right int) bool { return manifests[left].ID < manifests[right].ID })
	return manifests, nil
}

func (s *LanguagePackService) validateCandidateLocked(candidate languagePackManifest) error {
	manifests, err := s.activeManifestsLocked()
	if err != nil {
		return err
	}
	replaced := false
	for index := range manifests {
		if manifests[index].ID == candidate.ID {
			manifests[index] = candidate
			replaced = true
			break
		}
	}
	if !replaced {
		manifests = append(manifests, candidate)
	}
	return validateExternalLanguagePackSet(manifests)
}

func validateExternalLanguagePackSet(external []languagePackManifest) error {
	packIDs := make(map[string]struct{})
	languages := make(map[string]string)
	selectors := make(map[string]string)
	serverOrders := make(map[int]string)
	debuggers := make(map[string]string)
	commands := make(map[string]string)
	sections := make(map[string]string)
	toolHints := make(map[string]string)
	for _, manifest := range append(append([]languagePackManifest(nil), builtInLanguagePacks...), external...) {
		if _, exists := packIDs[manifest.ID]; exists {
			return fmt.Errorf("language pack id %q is already active", manifest.ID)
		}
		packIDs[manifest.ID] = struct{}{}
		for _, language := range manifest.Languages {
			if owner, exists := languages[language.ID]; exists {
				return fmt.Errorf("language %q conflicts between %s and %s", language.ID, owner, manifest.ID)
			}
			languages[language.ID] = manifest.ID
			for _, extension := range language.Extensions {
				selector := "extension:" + strings.ToLower(extension)
				if owner, exists := selectors[selector]; exists {
					return fmt.Errorf("language selector %q conflicts between %s and %s", selector, owner, manifest.ID)
				}
				selectors[selector] = manifest.ID
			}
			for _, filename := range language.Filenames {
				selector := "filename:" + strings.ToLower(filename)
				if owner, exists := selectors[selector]; exists {
					return fmt.Errorf("language selector %q conflicts between %s and %s", selector, owner, manifest.ID)
				}
				selectors[selector] = manifest.ID
			}
		}
		for _, server := range manifest.Servers {
			if owner, exists := serverOrders[server.StatusOrder]; exists {
				return fmt.Errorf("language server order %d conflicts between %s and %s", server.StatusOrder, owner, manifest.ID)
			}
			serverOrders[server.StatusOrder] = manifest.ID
			for _, section := range server.ConfigurationSections {
				if owner, exists := sections[section]; exists {
					return fmt.Errorf("configuration section %q conflicts between %s and %s", section, owner, manifest.ID)
				}
				sections[section] = manifest.ID
			}
		}
		for _, debugger := range manifest.Debuggers {
			if owner, exists := debuggers[debugger.ID]; exists {
				return fmt.Errorf("debugger %q conflicts between %s and %s", debugger.ID, owner, manifest.ID)
			}
			debuggers[debugger.ID] = manifest.ID
		}
		if manifest.Toolchain != nil {
			for _, command := range manifest.Toolchain.Commands {
				if owner, exists := commands[command.ID]; exists {
					return fmt.Errorf("toolchain command %q conflicts between %s and %s", command.ID, owner, manifest.ID)
				}
				commands[command.ID] = manifest.ID
			}
			for _, tool := range manifest.Toolchain.Tools {
				if hint, exists := toolHints[tool.Name]; exists && hint != tool.InstallHint {
					return fmt.Errorf("tool %q has conflicting install hints", tool.Name)
				}
				toolHints[tool.Name] = tool.InstallHint
			}
		}
	}
	return nil
}

func (s *LanguagePackService) readInstalledManifest(id, version string) (languagePackManifest, error) {
	if !languagePackIDPattern.MatchString(id) || !languagePackSemverPattern.MatchString(version) {
		return languagePackManifest{}, errors.New("invalid installed language pack identity")
	}
	path := filepath.Join(s.root, id, version, "manifest.json")
	if err := ensurePathWithin(s.root, path); err != nil {
		return languagePackManifest{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return languagePackManifest{}, fmt.Errorf("read installed language pack %s@%s: %w", id, version, err)
	}
	manifest, err := parseLanguagePackManifest(raw)
	if err != nil {
		return languagePackManifest{}, fmt.Errorf("parse installed language pack %s@%s: %w", id, version, err)
	}
	return manifest, nil
}

func (s *LanguagePackService) readInstalledSignature(id, version string) (languagePackSignature, error) {
	path := filepath.Join(s.root, id, version, "signature.json")
	if err := ensurePathWithin(s.root, path); err != nil {
		return languagePackSignature{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return languagePackSignature{}, fmt.Errorf("read installed language pack signature %s@%s: %w", id, version, err)
	}
	return parseLanguagePackSignature(raw)
}

func (s *LanguagePackService) persistStateLocked() error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return err
	}
	return atomicWriteJSON(filepath.Join(s.root, "state.json"), s.state, 0o600)
}

func readLanguagePackArchive(path string) (verifiedLanguagePackArchive, error) {
	file, err := os.Open(path)
	if err != nil {
		return verifiedLanguagePackArchive{}, err
	}
	defer func() {
		_ = file.Close()
	}()
	info, err := file.Stat()
	if err != nil {
		return verifiedLanguagePackArchive{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxLanguagePackArchiveBytes {
		return verifiedLanguagePackArchive{}, errors.New("language pack archive has an invalid size or type")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxLanguagePackArchiveBytes+1))
	if err != nil {
		return verifiedLanguagePackArchive{}, err
	}
	if len(raw) == 0 || len(raw) > maxLanguagePackArchiveBytes {
		return verifiedLanguagePackArchive{}, errors.New("language pack archive exceeds the size limit")
	}
	digest := sha256.Sum256(raw)
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return verifiedLanguagePackArchive{}, fmt.Errorf("open language pack archive: %w", err)
	}
	if len(reader.File) != 2 || len(reader.File) > maxLanguagePackArchiveEntries {
		return verifiedLanguagePackArchive{}, errors.New("language pack archive must contain exactly manifest.json and signature.json")
	}
	entries := make(map[string][]byte, 2)
	for _, entry := range reader.File {
		name := entry.Name
		if strings.Contains(name, "\\") || filepath.IsAbs(name) || name != filepath.ToSlash(filepath.Clean(name)) || strings.HasPrefix(name, "../") || entry.FileInfo().IsDir() || entry.Mode()&os.ModeSymlink != 0 {
			return verifiedLanguagePackArchive{}, fmt.Errorf("unsafe language pack archive entry %q", name)
		}
		if name != "manifest.json" && name != "signature.json" {
			return verifiedLanguagePackArchive{}, fmt.Errorf("unsupported language pack archive entry %q", name)
		}
		if _, duplicate := entries[name]; duplicate {
			return verifiedLanguagePackArchive{}, fmt.Errorf("duplicate language pack archive entry %q", name)
		}
		limit := int64(maxBuiltInLanguagePackBytes)
		if name == "signature.json" {
			limit = maxLanguagePackSignatureBytes
		}
		content, err := readLanguagePackZipEntry(entry, limit)
		if err != nil {
			return verifiedLanguagePackArchive{}, err
		}
		entries[name] = content
	}
	manifest, err := parseLanguagePackManifest(entries["manifest.json"])
	if err != nil {
		return verifiedLanguagePackArchive{}, err
	}
	signature, err := parseLanguagePackSignature(entries["signature.json"])
	if err != nil {
		return verifiedLanguagePackArchive{}, err
	}
	if signature.PackID != manifest.ID || signature.Version != manifest.Version || signature.ManifestSHA256 != manifest.Integrity.ManifestSha256 {
		return verifiedLanguagePackArchive{}, errors.New("language pack signature metadata does not match the manifest")
	}
	if signature.PublicKey != "" {
		if err := verifyLanguagePackManifestSignatureWithKey(manifest, signature, signature.PublicKey); err != nil {
			return verifiedLanguagePackArchive{}, err
		}
	}
	if _, err := languagePackPublicKeyFingerprint(signature.PublicKey); signature.PublicKey != "" && err != nil {
		return verifiedLanguagePackArchive{}, err
	}
	return verifiedLanguagePackArchive{
		manifest: manifest, manifestRaw: append([]byte(nil), entries["manifest.json"]...),
		signature: signature, archiveSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func verifyLanguagePackManifestSignatureWithKey(manifest languagePackManifest, signature languagePackSignature, publicHex string) error {
	if signature.PackID != manifest.ID || signature.Version != manifest.Version || signature.ManifestSHA256 != manifest.Integrity.ManifestSha256 {
		return errors.New("language pack signature metadata does not match the manifest")
	}
	publicKey, err := hex.DecodeString(publicHex)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("configured language pack publisher key is invalid")
	}
	signatureBytes, err := base64.StdEncoding.DecodeString(signature.Signature)
	if err != nil || len(signatureBytes) != ed25519.SignatureSize {
		return errors.New("language pack signature encoding is invalid")
	}
	payload, err := languagePackSignedPayload(signature)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signatureBytes) {
		return errors.New("language pack signature verification failed")
	}
	return nil
}

func languagePackPublicKeyFingerprint(publicHex string) (string, error) {
	publicKey, err := hex.DecodeString(publicHex)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return "", errors.New("language pack publisher public key is invalid")
	}
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:]), nil
}

func readLanguagePackZipEntry(entry *zip.File, limit int64) ([]byte, error) {
	if entry.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("language pack archive entry %q exceeds size limit", entry.Name)
	}
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = reader.Close()
	}()
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("language pack archive entry %q exceeds size limit", entry.Name)
	}
	return content, nil
}

func parseLanguagePackSignature(raw []byte) (languagePackSignature, error) {
	if len(raw) == 0 || len(raw) > maxLanguagePackSignatureBytes {
		return languagePackSignature{}, errors.New("language pack signature has an invalid size")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return languagePackSignature{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var signature languagePackSignature
	if err := decoder.Decode(&signature); err != nil {
		return languagePackSignature{}, fmt.Errorf("decode language pack signature: %w", err)
	}
	if signature.Format != languagePackSignatureFormat || signature.Algorithm != languagePackSignatureAlgorithm ||
		!languagePackIDPattern.MatchString(signature.KeyID) || !languagePackIDPattern.MatchString(signature.PackID) ||
		!languagePackSemverPattern.MatchString(signature.Version) || !languagePackSHA256Pattern.MatchString(signature.ManifestSHA256) ||
		(signature.PublicKey != "" && !languagePackSHA256Pattern.MatchString(signature.PublicKey)) || signature.Signature == "" {
		return languagePackSignature{}, errors.New("language pack signature metadata is invalid")
	}
	return signature, nil
}

func languagePackSignedPayload(signature languagePackSignature) ([]byte, error) {
	payload := map[string]interface{}{
		"format": signature.Format, "algorithm": signature.Algorithm, "keyId": signature.KeyID,
		"packId": signature.PackID, "version": signature.Version, "manifestSha256": signature.ManifestSHA256,
	}
	if signature.PublicKey != "" {
		payload["publicKey"] = strings.ToLower(signature.PublicKey)
	}
	canonical, err := canonicalJSON(payload)
	return []byte(canonical), err
}

func findLanguagePackVersion(record languagePackRecordState, version string) (languagePackVersionState, bool) {
	for _, candidate := range record.Versions {
		if candidate.Version == version {
			return candidate, true
		}
	}
	return languagePackVersionState{}, false
}

func highestLanguagePackVersion(versions []languagePackVersionState) string {
	highest := ""
	for _, version := range versions {
		if highest == "" || compareLanguagePackVersions(version.Version, highest) > 0 {
			highest = version.Version
		}
	}
	return highest
}

func compareLanguagePackVersions(left, right string) int {
	leftCore, leftPrerelease := splitLanguagePackVersion(left)
	rightCore, rightPrerelease := splitLanguagePackVersion(right)
	for index := range leftCore {
		if comparison := compareLanguagePackNumericIdentifier(leftCore[index], rightCore[index]); comparison != 0 {
			return comparison
		}
	}
	if len(leftPrerelease) == 0 && len(rightPrerelease) > 0 {
		return 1
	}
	if len(leftPrerelease) > 0 && len(rightPrerelease) == 0 {
		return -1
	}
	for index := 0; index < len(leftPrerelease) && index < len(rightPrerelease); index++ {
		leftIdentifier := leftPrerelease[index]
		rightIdentifier := rightPrerelease[index]
		leftNumeric := isLanguagePackNumericIdentifier(leftIdentifier)
		rightNumeric := isLanguagePackNumericIdentifier(rightIdentifier)
		switch {
		case leftNumeric && rightNumeric:
			if comparison := compareLanguagePackNumericIdentifier(leftIdentifier, rightIdentifier); comparison != 0 {
				return comparison
			}
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		default:
			if comparison := strings.Compare(leftIdentifier, rightIdentifier); comparison != 0 {
				return comparison
			}
		}
	}
	if len(leftPrerelease) < len(rightPrerelease) {
		return -1
	}
	if len(leftPrerelease) > len(rightPrerelease) {
		return 1
	}
	return 0
}

func splitLanguagePackVersion(version string) ([3]string, []string) {
	withoutBuild, _, _ := strings.Cut(version, "+")
	core, prerelease, hasPrerelease := strings.Cut(withoutBuild, "-")
	parts := strings.Split(core, ".")
	var result [3]string
	copy(result[:], parts)
	if !hasPrerelease {
		return result, nil
	}
	return result, strings.Split(prerelease, ".")
}

func compareLanguagePackNumericIdentifier(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}

func isLanguagePackNumericIdentifier(identifier string) bool {
	if identifier == "" {
		return false
	}
	for _, value := range identifier {
		if value < '0' || value > '9' {
			return false
		}
	}
	return true
}

func ensurePathWithin(root, candidate string) error {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("language pack path escapes the installation root")
	}
	return nil
}
