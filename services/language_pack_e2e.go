//go:build e2e

package services

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
)

const languagePackE2ESeedHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

// LanguagePackFixtureSpecForE2E describes a signed conformance fixture. This
// type and its signing key are excluded from every normal product build.
type LanguagePackFixtureSpecForE2E struct {
	ID                  string
	Version             string
	DisplayName         string
	Language            string
	Extension           string
	RootMarker          string
	ServerID            string
	ServerOrder         int
	ServerExecutable    string
	ServerArgs          []string
	VersionArgs         []string
	CommandID           string
	CommandLabel        string
	Configuration       string
	ToolchainExecutable string
	ToolchainArgs       []string
}

func WriteSignedLanguagePackFixtureForE2E(dir string, spec LanguagePackFixtureSpecForE2E) (string, error) {
	stringsAsJSON := func(values []string) []interface{} {
		result := make([]interface{}, len(values))
		for index, value := range values {
			result[index] = value
		}
		return result
	}
	serverExecutable := spec.ServerExecutable
	if serverExecutable == "" {
		serverExecutable = "gopls"
	}
	serverArgs := spec.ServerArgs
	if len(serverArgs) == 0 {
		serverArgs = []string{"serve"}
	}
	versionArgs := spec.VersionArgs
	if len(versionArgs) == 0 {
		versionArgs = []string{"version"}
	}
	toolchainExecutable := spec.ToolchainExecutable
	if toolchainExecutable == "" {
		toolchainExecutable = "go"
	}
	toolchainArgs := spec.ToolchainArgs
	if len(toolchainArgs) == 0 {
		toolchainArgs = []string{"version"}
	}
	manifestValue := map[string]interface{}{
		"schemaVersion": languagePackSchemaVersion,
		"id":            spec.ID,
		"version":       spec.Version,
		"displayName":   spec.DisplayName,
		"compatibility": map[string]interface{}{
			"engineApi": languagePackEngineAPIVersion, "hostProtocol": languagePackLocalHostProtocol,
			"platforms": []interface{}{map[string]interface{}{"os": runtime.GOOS, "arch": runtime.GOARCH}},
		},
		"languages": []interface{}{map[string]interface{}{
			"id": spec.Language, "extensions": []interface{}{spec.Extension}, "filenames": []interface{}{},
		}},
		"rootMarkers": []interface{}{spec.RootMarker},
		"servers": []interface{}{map[string]interface{}{
			"id": spec.ServerID, "statusOrder": json.Number(strconv.Itoa(spec.ServerOrder)),
			"languages": []interface{}{spec.Language}, "aliases": []interface{}{},
			"executables": []interface{}{map[string]interface{}{"commandName": serverExecutable, "kind": spec.ServerID}},
			"args":        stringsAsJSON(serverArgs), "installHint": "Install the language server required by this pack",
			"workspaceNode": false, "initializationProfile": "generic",
			"configurationSections": []interface{}{spec.Configuration}, "configurationResponse": "full",
			"versionArgs": stringsAsJSON(versionArgs), "preferReactWorkspace": false, "reactAware": false,
		}},
		"toolchain": map[string]interface{}{
			"commands": []interface{}{map[string]interface{}{
				"id": spec.CommandID, "label": spec.CommandLabel, "language": spec.Language,
				"executable": toolchainExecutable, "args": stringsAsJSON(toolchainArgs),
				"description": "Run the signed SDK conformance command", "fileScoped": false,
			}},
			"tools": []interface{}{map[string]interface{}{
				"name": toolchainExecutable, "installHint": "Install the toolchain required by this pack",
			}},
		},
		"permissions":         []interface{}{"workspace.read", "process.launch"},
		"configurationSchema": map[string]interface{}{},
		"integrity":           map[string]interface{}{"manifestSha256": ""},
	}
	unsigned := make(map[string]interface{}, len(manifestValue)-1)
	for key, value := range manifestValue {
		if key != "integrity" {
			unsigned[key] = value
		}
	}
	canonical, err := canonicalJSON(unsigned)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(canonical))
	manifestValue["integrity"] = map[string]interface{}{"manifestSha256": hex.EncodeToString(digest[:])}
	manifestRaw, err := json.Marshal(manifestValue)
	if err != nil {
		return "", err
	}
	manifest, err := parseLanguagePackManifest(manifestRaw)
	if err != nil {
		return "", err
	}
	seed, err := hex.DecodeString(languagePackE2ESeedHex)
	if err != nil {
		return "", err
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	signature := languagePackSignature{
		Format: languagePackSignatureFormat, Algorithm: languagePackSignatureAlgorithm,
		KeyID: "org.koyori.sdk-fixtures", PackID: manifest.ID, Version: manifest.Version,
		ManifestSHA256: manifest.Integrity.ManifestSha256, PublicKey: hex.EncodeToString(publicKey),
	}
	payload, err := languagePackSignedPayload(signature)
	if err != nil {
		return "", err
	}
	signature.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	signatureRaw, err := json.Marshal(signature)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	archivePath := filepath.Join(dir, spec.ID+"-"+spec.Version+".koyori-language-pack")
	file, err := os.OpenFile(archivePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	writer := zip.NewWriter(file)
	writeEntry := func(name string, content []byte) error {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			return createErr
		}
		_, writeErr := entry.Write(content)
		return writeErr
	}
	if err := writeEntry("manifest.json", manifestRaw); err != nil {
		_ = writer.Close()
		_ = file.Close()
		_ = os.Remove(archivePath)
		return "", err
	}
	if err := writeEntry("signature.json", signatureRaw); err != nil {
		_ = writer.Close()
		_ = file.Close()
		_ = os.Remove(archivePath)
		return "", err
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		_ = os.Remove(archivePath)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(archivePath)
		return "", err
	}
	return archivePath, nil
}

// InstallLanguagePackWithPublisherTrustForE2E exercises the production
// unknown-publisher onboarding path with an exact, single-use backend approver.
func InstallLanguagePackWithPublisherTrustForE2E(service *LanguagePackService, path string) (LanguagePackInfo, bool, error) {
	if service == nil {
		return LanguagePackInfo{}, false, errors.New("language pack service is unavailable")
	}
	archive, err := readLanguagePackArchive(path)
	if err != nil {
		return LanguagePackInfo{}, false, err
	}
	fingerprint, err := languagePackPublicKeyFingerprint(archive.signature.PublicKey)
	if err != nil {
		return LanguagePackInfo{}, false, err
	}
	expected := languagePackPublisherApproval{
		KeyID: archive.signature.KeyID, Fingerprint: fingerprint,
		PackID: archive.manifest.ID, Version: archive.manifest.Version,
		ArchiveSHA256: archive.archiveSHA256,
	}
	var consumed atomic.Bool
	previous := service.approvePublisher
	service.approvePublisher = func(actual languagePackPublisherApproval) bool {
		if actual != expected || !consumed.CompareAndSwap(false, true) {
			return false
		}
		return true
	}
	defer func() { service.approvePublisher = previous }()
	info, err := service.installFromNativePath(path)
	return info, consumed.Load(), err
}

func EnableLanguagePackForE2E(service *LanguagePackService, id string) error {
	if service == nil {
		return errors.New("language pack service is unavailable")
	}
	return service.enableTrusted(id)
}

func DisableLanguagePackForE2E(service *LanguagePackService, id string) error {
	if service == nil {
		return errors.New("language pack service is unavailable")
	}
	return service.disableTrusted(id)
}

func RollbackLanguagePackForE2E(service *LanguagePackService, id string) (LanguagePackInfo, error) {
	if service == nil {
		return LanguagePackInfo{}, errors.New("language pack service is unavailable")
	}
	return service.rollbackTrusted(id)
}

func UninstallLanguagePackForE2E(service *LanguagePackService, id string) error {
	if service == nil {
		return errors.New("language pack service is unavailable")
	}
	return service.uninstallTrusted(id)
}

func SetToolPathsForE2E(service *ToolchainService, paths map[string]string) error {
	if service == nil {
		return errors.New("toolchain service is unavailable")
	}
	service.setToolPaths(paths)
	return nil
}
