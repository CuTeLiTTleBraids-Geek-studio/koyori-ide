package services

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const languagePackSchemaVersion = "1.0"
const languagePackEngineAPIVersion = "1.0"
const languagePackLocalHostProtocol = "language.local.v1"
const maxBuiltInLanguagePackBytes = 128 << 10

var languagePackIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`)
var languagePackCommandPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var languagePackSemverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
var languagePackSHA256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// The built-in packs are embedded in the backend. The renderer imports the
// same source files for detection, while only this backend may launch a server.
//
//go:embed languagepacks/go.language-pack.json languagepacks/typescript.language-pack.json
var builtInLanguagePackFiles embed.FS

type languagePackManifest struct {
	SchemaVersion       string                     `json:"schemaVersion"`
	ID                  string                     `json:"id"`
	Version             string                     `json:"version"`
	DisplayName         string                     `json:"displayName"`
	Compatibility       *languagePackCompatibility `json:"compatibility"`
	Languages           []languagePackLanguage     `json:"languages"`
	RootMarkers         []string                   `json:"rootMarkers"`
	Servers             []languagePackServer       `json:"servers"`
	Debuggers           []languagePackDebugger     `json:"debuggers,omitempty"`
	Toolchain           *languagePackToolchain     `json:"toolchain,omitempty"`
	Permissions         []string                   `json:"permissions"`
	ConfigurationSchema map[string]interface{}     `json:"configurationSchema"`
	Integrity           *languagePackIntegrity     `json:"integrity"`
}

type languagePackCompatibility struct {
	EngineAPI    string                 `json:"engineApi"`
	HostProtocol string                 `json:"hostProtocol"`
	Platforms    []languagePackPlatform `json:"platforms"`
}

type languagePackPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type languagePackLanguage struct {
	ID         string   `json:"id"`
	Extensions []string `json:"extensions"`
	Filenames  []string `json:"filenames"`
}

type languagePackServer struct {
	ID                    string                   `json:"id"`
	StatusOrder           int                      `json:"statusOrder"`
	Languages             []string                 `json:"languages"`
	Aliases               []string                 `json:"aliases"`
	Executables           []languagePackExecutable `json:"executables"`
	Args                  []string                 `json:"args"`
	InstallHint           string                   `json:"installHint"`
	WorkspaceNode         *bool                    `json:"workspaceNode"`
	InitializationProfile string                   `json:"initializationProfile"`
	ConfigurationSections []string                 `json:"configurationSections"`
	ConfigurationResponse string                   `json:"configurationResponse"`
	VersionExecutable     string                   `json:"versionExecutable,omitempty"`
	VersionArgs           []string                 `json:"versionArgs"`
	VersionPin            string                   `json:"versionPin,omitempty"`
	PreferReactWorkspace  *bool                    `json:"preferReactWorkspace"`
	ReactAware            *bool                    `json:"reactAware"`
}

type languagePackExecutable struct {
	CommandName string `json:"commandName"`
	Kind        string `json:"kind"`
}

type languagePackToolchain struct {
	Commands []languagePackToolchainCommand `json:"commands"`
	Tools    []languagePackToolchainTool    `json:"tools"`
}

type languagePackDebugger struct {
	ID                string   `json:"id"`
	Protocol          string   `json:"protocol"`
	Languages         []string `json:"languages"`
	Executable        string   `json:"executable"`
	Args              []string `json:"args"`
	InstallHint       string   `json:"installHint"`
	SourcePackID      string   `json:"-"`
	SourcePackVersion string   `json:"-"`
}

type languagePackToolchainCommand struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Language    string   `json:"language"`
	Executable  string   `json:"executable"`
	Args        []string `json:"args"`
	Description string   `json:"description"`
	FileScoped  bool     `json:"fileScoped"`
}

type languagePackToolchainTool struct {
	Name        string `json:"name"`
	InstallHint string `json:"installHint"`
}

type languagePackIntegrity struct {
	ManifestSha256 string `json:"manifestSha256"`
}

func mustLoadBuiltInLanguagePacks() []languagePackManifest {
	paths := []string{
		"languagepacks/go.language-pack.json",
		"languagepacks/typescript.language-pack.json",
	}
	packs := make([]languagePackManifest, 0, len(paths))
	packIDs := make(map[string]struct{}, len(paths))
	orders := make(map[int]string)
	languageOwners := make(map[string]string)
	selectorOwners := make(map[string]string)
	debuggerOwners := make(map[string]string)
	configurationOwners := make(map[string]string)
	for _, path := range paths {
		raw, err := builtInLanguagePackFiles.ReadFile(path)
		if err != nil {
			panic(fmt.Sprintf("read embedded language pack %s: %v", path, err))
		}
		manifest, err := parseLanguagePackManifest(raw)
		if err != nil {
			panic(fmt.Sprintf("invalid embedded language pack %s: %v", path, err))
		}
		if _, exists := packIDs[manifest.ID]; exists {
			panic(fmt.Sprintf("duplicate embedded language pack %q", manifest.ID))
		}
		packIDs[manifest.ID] = struct{}{}
		for _, server := range manifest.Servers {
			if owner, exists := orders[server.StatusOrder]; exists {
				panic(fmt.Sprintf("language pack server order %d is used by %s and %s", server.StatusOrder, owner, server.ID))
			}
			orders[server.StatusOrder] = server.ID
			for _, section := range server.ConfigurationSections {
				if owner, exists := configurationOwners[section]; exists {
					panic(fmt.Sprintf("language pack configuration section %q is used by %s and %s", section, owner, server.ID))
				}
				configurationOwners[section] = server.ID
			}
		}
		for _, language := range manifest.Languages {
			if owner, exists := languageOwners[language.ID]; exists {
				panic(fmt.Sprintf("language id %q is provided by %s and %s", language.ID, owner, manifest.ID))
			}
			languageOwners[language.ID] = manifest.ID
			for _, extension := range language.Extensions {
				selector := "extension:" + strings.ToLower(extension)
				if owner, exists := selectorOwners[selector]; exists {
					panic(fmt.Sprintf("language selector %q is provided by %s and %s", selector, owner, manifest.ID))
				}
				selectorOwners[selector] = manifest.ID
			}
			for _, filename := range language.Filenames {
				selector := "filename:" + strings.ToLower(filename)
				if owner, exists := selectorOwners[selector]; exists {
					panic(fmt.Sprintf("language selector %q is provided by %s and %s", selector, owner, manifest.ID))
				}
				selectorOwners[selector] = manifest.ID
			}
		}
		for _, debugger := range manifest.Debuggers {
			if owner, exists := debuggerOwners[debugger.ID]; exists {
				panic(fmt.Sprintf("language pack debugger %q is provided by both %s and %s", debugger.ID, owner, manifest.ID))
			}
			debuggerOwners[debugger.ID] = manifest.ID
		}
		packs = append(packs, manifest)
	}
	return packs
}

var builtInLanguagePacks = mustLoadBuiltInLanguagePacks()

var languagePackRuntimeState = struct {
	sync.RWMutex
	external []languagePackManifest
}{}

func activeLanguagePackSnapshot() []languagePackManifest {
	languagePackRuntimeState.RLock()
	external := append([]languagePackManifest(nil), languagePackRuntimeState.external...)
	languagePackRuntimeState.RUnlock()
	packs := make([]languagePackManifest, 0, len(builtInLanguagePacks)+len(external))
	packs = append(packs, builtInLanguagePacks...)
	packs = append(packs, external...)
	return packs
}

// setActiveExternalLanguagePacks is called only by the native installer after
// signature, integrity, state, and conflict validation. Renderer input never
// reaches this boundary directly.
func setActiveExternalLanguagePacks(packs []languagePackManifest) {
	languagePackRuntimeState.Lock()
	languagePackRuntimeState.external = append([]languagePackManifest(nil), packs...)
	languagePackRuntimeState.Unlock()
	refreshLanguagePackDerivedCatalogs()
}

// parseLanguagePackManifest is deliberately stricter than encoding/json's
// default behavior: duplicate keys are rejected before typed decoding so a
// hostile manifest cannot hide a security field behind a later duplicate.
func parseLanguagePackManifest(raw []byte) (languagePackManifest, error) {
	if len(raw) == 0 || len(raw) > maxBuiltInLanguagePackBytes {
		return languagePackManifest{}, fmt.Errorf("manifest size must be between 1 and %d bytes", maxBuiltInLanguagePackBytes)
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return languagePackManifest{}, err
	}
	var manifest languagePackManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return languagePackManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return languagePackManifest{}, fmt.Errorf("manifest contains multiple JSON values")
		}
		return languagePackManifest{}, fmt.Errorf("manifest has trailing data: %w", err)
	}
	if err := validateLanguagePackManifest(manifest, raw); err != nil {
		return languagePackManifest{}, err
	}
	return manifest, nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeJSONValue(decoder); err != nil {
		return fmt.Errorf("invalid manifest JSON: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("manifest contains multiple JSON values")
		}
		return fmt.Errorf("manifest has trailing data: %w", err)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	_, err = decoder.Token()
	return err
}

func validateLanguagePackManifest(manifest languagePackManifest, raw []byte) error {
	if manifest.SchemaVersion != languagePackSchemaVersion {
		return fmt.Errorf("unsupported language pack schema version %q", manifest.SchemaVersion)
	}
	if !languagePackIDPattern.MatchString(manifest.ID) {
		return fmt.Errorf("invalid language pack id %q", manifest.ID)
	}
	if !languagePackSemverPattern.MatchString(manifest.Version) || manifest.DisplayName == "" {
		return fmt.Errorf("language pack %q has invalid version or display name", manifest.ID)
	}
	if manifest.Compatibility == nil || manifest.Languages == nil || len(manifest.Languages) == 0 || manifest.RootMarkers == nil || manifest.Servers == nil || manifest.Permissions == nil || manifest.ConfigurationSchema == nil || manifest.Integrity == nil {
		return fmt.Errorf("language pack %q is missing a required field", manifest.ID)
	}
	if manifest.Compatibility.EngineAPI != languagePackEngineAPIVersion {
		return fmt.Errorf("language pack %q requires unsupported engine API %q", manifest.ID, manifest.Compatibility.EngineAPI)
	}
	if manifest.Compatibility.HostProtocol != languagePackLocalHostProtocol {
		return fmt.Errorf("language pack %q requires unsupported host protocol %q", manifest.ID, manifest.Compatibility.HostProtocol)
	}
	if len(manifest.Compatibility.Platforms) == 0 || len(manifest.Compatibility.Platforms) > 6 {
		return fmt.Errorf("language pack %q has an invalid platform compatibility set", manifest.ID)
	}
	platforms := make(map[string]struct{}, len(manifest.Compatibility.Platforms))
	currentPlatform := false
	for _, platform := range manifest.Compatibility.Platforms {
		if platform.OS != "windows" && platform.OS != "darwin" && platform.OS != "linux" {
			return fmt.Errorf("language pack %q has unsupported operating system %q", manifest.ID, platform.OS)
		}
		if platform.Arch != "amd64" && platform.Arch != "arm64" {
			return fmt.Errorf("language pack %q has unsupported architecture %q", manifest.ID, platform.Arch)
		}
		key := platform.OS + "/" + platform.Arch
		if _, duplicate := platforms[key]; duplicate {
			return fmt.Errorf("language pack %q repeats platform %q", manifest.ID, key)
		}
		platforms[key] = struct{}{}
		currentPlatform = currentPlatform || platform.OS == runtime.GOOS && platform.Arch == runtime.GOARCH
	}
	if !currentPlatform {
		return fmt.Errorf("language pack %q is incompatible with platform %s/%s", manifest.ID, runtime.GOOS, runtime.GOARCH)
	}
	if len(manifest.ConfigurationSchema) != 0 {
		return fmt.Errorf("language pack %q configuration schemas are not supported", manifest.ID)
	}
	permissionSet := make(map[string]struct{}, len(manifest.Permissions))
	for _, permission := range manifest.Permissions {
		switch permission {
		case "workspace.read", "process.launch", "tool.execute":
		default:
			return fmt.Errorf("language pack %q permissions must be exactly from the supported set; unsupported permission %q", manifest.ID, permission)
		}
		if _, exists := permissionSet[permission]; exists {
			return fmt.Errorf("language pack %q repeats permission %q", manifest.ID, permission)
		}
		permissionSet[permission] = struct{}{}
	}
	if _, ok := permissionSet["workspace.read"]; !ok {
		return fmt.Errorf("language pack %q must request workspace.read", manifest.ID)
	}
	if len(manifest.Servers) > 0 || len(manifest.Debuggers) > 0 {
		if _, ok := permissionSet["process.launch"]; !ok {
			return fmt.Errorf("language pack %q must request process.launch", manifest.ID)
		}
	}
	if manifest.Toolchain != nil {
		if _, ok := permissionSet["process.launch"]; !ok {
			return fmt.Errorf("language pack %q toolchain requires process.launch", manifest.ID)
		}
	}
	if !languagePackSHA256Pattern.MatchString(manifest.Integrity.ManifestSha256) {
		return fmt.Errorf("language pack %q has an invalid manifest SHA-256", manifest.ID)
	}
	canonical, err := canonicalManifestPayload(raw)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	if hex.EncodeToString(digest[:]) != manifest.Integrity.ManifestSha256 {
		return fmt.Errorf("language pack %q manifest SHA-256 does not match", manifest.ID)
	}

	languages := make(map[string]languagePackLanguage, len(manifest.Languages))
	selectors := make(map[string]string)
	markers := make(map[string]struct{}, len(manifest.RootMarkers))
	for _, marker := range manifest.RootMarkers {
		if marker == "" || strings.ContainsRune(marker, 0) {
			return fmt.Errorf("language pack %q has an invalid root marker", manifest.ID)
		}
		if _, exists := markers[marker]; exists {
			return fmt.Errorf("language pack %q repeats root marker %q", manifest.ID, marker)
		}
		markers[marker] = struct{}{}
	}
	for index, language := range manifest.Languages {
		if !languagePackIDPattern.MatchString(language.ID) || language.Extensions == nil || language.Filenames == nil || len(language.Extensions) == 0 && len(language.Filenames) == 0 {
			return fmt.Errorf("language pack %q languages[%d] is invalid", manifest.ID, index)
		}
		if _, exists := languages[language.ID]; exists {
			return fmt.Errorf("language pack %q repeats language %q", manifest.ID, language.ID)
		}
		languages[language.ID] = language
		for _, extension := range language.Extensions {
			if len(extension) < 2 || extension[0] != '.' {
				return fmt.Errorf("language pack %q has invalid extension %q", manifest.ID, extension)
			}
			selector := "extension:" + strings.ToLower(extension)
			if owner, exists := selectors[selector]; exists {
				return fmt.Errorf("language pack %q selector %q conflicts with %q", manifest.ID, selector, owner)
			}
			selectors[selector] = language.ID
		}
		for _, filename := range language.Filenames {
			if filename == "" || strings.ContainsAny(filename, `/\\`) || filename == "." || filename == ".." {
				return fmt.Errorf("language pack %q has invalid filename %q", manifest.ID, filename)
			}
			selector := "filename:" + strings.ToLower(filename)
			if owner, exists := selectors[selector]; exists {
				return fmt.Errorf("language pack %q selector %q conflicts with %q", manifest.ID, selector, owner)
			}
			selectors[selector] = language.ID
		}
	}
	serverIDs := make(map[string]struct{}, len(manifest.Servers))
	serverLanguages := make(map[string]string)
	orders := make(map[int]struct{}, len(manifest.Servers))
	for index, server := range manifest.Servers {
		if !languagePackIDPattern.MatchString(server.ID) || server.StatusOrder <= 0 || server.WorkspaceNode == nil || server.PreferReactWorkspace == nil || server.ReactAware == nil || server.InstallHint == "" || len(server.Executables) == 0 || server.Languages == nil || len(server.Languages) == 0 || server.Aliases == nil || server.Args == nil || server.ConfigurationSections == nil || server.VersionArgs == nil || (server.VersionExecutable != "" && !languagePackCommandPattern.MatchString(server.VersionExecutable)) || (server.VersionPin != "" && !languagePackSemverPattern.MatchString(server.VersionPin)) {
			return fmt.Errorf("language pack %q servers[%d] is invalid", manifest.ID, index)
		}
		if _, exists := serverIDs[server.ID]; exists {
			return fmt.Errorf("language pack %q repeats server %q", manifest.ID, server.ID)
		}
		serverIDs[server.ID] = struct{}{}
		if _, exists := orders[server.StatusOrder]; exists {
			return fmt.Errorf("language pack %q repeats server status order %d", manifest.ID, server.StatusOrder)
		}
		orders[server.StatusOrder] = struct{}{}
		if server.InitializationProfile != "go" && server.InitializationProfile != "typescript" && server.InitializationProfile != "generic" {
			return fmt.Errorf("language pack %q server %q has unsupported initialization profile", manifest.ID, server.ID)
		}
		if server.ConfigurationResponse != "full" && server.ConfigurationResponse != "preferences" {
			return fmt.Errorf("language pack %q server %q has unsupported configuration response", manifest.ID, server.ID)
		}
		if len(server.ConfigurationSections) == 0 {
			return fmt.Errorf("language pack %q server %q has no configuration sections", manifest.ID, server.ID)
		}
		for _, languageID := range append(append([]string(nil), server.Languages...), server.Aliases...) {
			if _, exists := languages[languageID]; !exists {
				return fmt.Errorf("language pack %q server %q references unknown language %q", manifest.ID, server.ID, languageID)
			}
			if owner, exists := serverLanguages[languageID]; exists {
				return fmt.Errorf("language pack %q assigns language %q to both %q and %q", manifest.ID, languageID, owner, server.ID)
			}
			serverLanguages[languageID] = server.ID
		}
		executables := make(map[string]struct{}, len(server.Executables))
		for _, executable := range server.Executables {
			if !languagePackCommandPattern.MatchString(executable.CommandName) || !languagePackIDPattern.MatchString(executable.Kind) {
				return fmt.Errorf("language pack %q server %q has an unsafe executable", manifest.ID, server.ID)
			}
			identity := executable.CommandName + "\x00" + executable.Kind
			if _, exists := executables[identity]; exists {
				return fmt.Errorf("language pack %q server %q repeats an executable", manifest.ID, server.ID)
			}
			executables[identity] = struct{}{}
		}
		sections := make(map[string]struct{}, len(server.ConfigurationSections))
		for _, section := range server.ConfigurationSections {
			if section == "" || strings.ContainsRune(section, 0) {
				return fmt.Errorf("language pack %q server %q has an invalid configuration section", manifest.ID, server.ID)
			}
			if _, exists := sections[section]; exists {
				return fmt.Errorf("language pack %q server %q repeats configuration section %q", manifest.ID, server.ID, section)
			}
			sections[section] = struct{}{}
		}
		for _, arg := range append(append([]string(nil), server.Args...), server.VersionArgs...) {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("language pack %q server %q contains a NUL argument", manifest.ID, server.ID)
			}
		}
	}
	if manifest.Toolchain != nil {
		if err := validateLanguagePackToolchain(manifest, *manifest.Toolchain); err != nil {
			return err
		}
	}
	if manifest.Debuggers != nil {
		if err := validateLanguagePackDebuggers(manifest, manifest.Debuggers); err != nil {
			return err
		}
	}
	return nil
}

func validateLanguagePackDebuggers(manifest languagePackManifest, debuggers []languagePackDebugger) error {
	languageIDs := make(map[string]struct{}, len(manifest.Languages))
	for _, language := range manifest.Languages {
		languageIDs[language.ID] = struct{}{}
	}
	debuggerIDs := make(map[string]struct{}, len(debuggers))
	languageOwners := make(map[string]string)
	for index, debugger := range debuggers {
		if !languagePackIDPattern.MatchString(debugger.ID) || (debugger.Protocol != "dap" && debugger.Protocol != "cdp") || !languagePackCommandPattern.MatchString(debugger.Executable) || debugger.Args == nil || debugger.Languages == nil || len(debugger.Languages) == 0 || debugger.InstallHint == "" || strings.ContainsRune(debugger.InstallHint, 0) {
			return fmt.Errorf("language pack %q debuggers[%d] is invalid", manifest.ID, index)
		}
		if _, exists := debuggerIDs[debugger.ID]; exists {
			return fmt.Errorf("language pack %q repeats debugger %q", manifest.ID, debugger.ID)
		}
		debuggerIDs[debugger.ID] = struct{}{}
		languages := make(map[string]struct{}, len(debugger.Languages))
		for _, languageID := range debugger.Languages {
			if _, exists := languageIDs[languageID]; !exists {
				return fmt.Errorf("language pack %q debugger %q references unknown language %q", manifest.ID, debugger.ID, languageID)
			}
			if _, exists := languages[languageID]; exists {
				return fmt.Errorf("language pack %q debugger %q repeats language %q", manifest.ID, debugger.ID, languageID)
			}
			languages[languageID] = struct{}{}
			if owner, exists := languageOwners[languageID]; exists {
				return fmt.Errorf("language pack %q assigns language %q to both debuggers %q and %q", manifest.ID, languageID, owner, debugger.ID)
			}
			languageOwners[languageID] = debugger.ID
		}
		for _, arg := range debugger.Args {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("language pack %q debugger %q contains a NUL argument", manifest.ID, debugger.ID)
			}
		}
	}
	return nil
}

func validateLanguagePackToolchain(manifest languagePackManifest, toolchain languagePackToolchain) error {
	if toolchain.Commands == nil || toolchain.Tools == nil || len(toolchain.Commands) == 0 {
		return fmt.Errorf("language pack %q toolchain must declare commands and tools", manifest.ID)
	}
	languageIDs := make(map[string]struct{}, len(manifest.Languages))
	for _, language := range manifest.Languages {
		languageIDs[language.ID] = struct{}{}
	}
	toolNames := make(map[string]struct{}, len(toolchain.Tools))
	for index, tool := range toolchain.Tools {
		if !languagePackCommandPattern.MatchString(tool.Name) || tool.InstallHint == "" || strings.ContainsRune(tool.InstallHint, 0) {
			return fmt.Errorf("language pack %q toolchain.tools[%d] has an unsafe tool declaration", manifest.ID, index)
		}
		if _, exists := toolNames[tool.Name]; exists {
			return fmt.Errorf("language pack %q repeats toolchain tool %q", manifest.ID, tool.Name)
		}
		toolNames[tool.Name] = struct{}{}
	}
	commandIDs := make(map[string]struct{}, len(toolchain.Commands))
	for index, command := range toolchain.Commands {
		if !languagePackIDPattern.MatchString(command.ID) || command.Label == "" || command.Description == "" || !languagePackIDPattern.MatchString(command.Language) || !languagePackCommandPattern.MatchString(command.Executable) || command.Args == nil {
			return fmt.Errorf("language pack %q toolchain.commands[%d] is invalid", manifest.ID, index)
		}
		if _, exists := languageIDs[command.Language]; !exists {
			return fmt.Errorf("language pack %q toolchain command %q references unknown language %q", manifest.ID, command.ID, command.Language)
		}
		if _, exists := commandIDs[command.ID]; exists {
			return fmt.Errorf("language pack %q repeats toolchain command %q", manifest.ID, command.ID)
		}
		commandIDs[command.ID] = struct{}{}
		if _, declared := toolNames[command.Executable]; !declared {
			return fmt.Errorf("language pack %q toolchain command %q uses undeclared tool %q", manifest.ID, command.ID, command.Executable)
		}
		for _, arg := range command.Args {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("language pack %q toolchain command %q contains a NUL argument", manifest.ID, command.ID)
			}
		}
	}
	return nil
}

func canonicalManifestPayload(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode manifest for integrity: %w", err)
	}
	delete(value, "integrity")
	canonical, err := canonicalJSON(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize manifest: %w", err)
	}
	return []byte(canonical), nil
}

func canonicalJSON(value interface{}) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "null", nil
	case bool:
		if typed {
			return "true", nil
		}
		return "false", nil
	case string:
		encoded, err := json.Marshal(typed)
		return string(encoded), err
	case json.Number:
		return typed.String(), nil
	case []interface{}:
		parts := make([]string, len(typed))
		for index, item := range typed {
			part, err := canonicalJSON(item)
			if err != nil {
				return "", err
			}
			parts[index] = part
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			encodedKey, err := json.Marshal(key)
			if err != nil {
				return "", err
			}
			encodedValue, err := canonicalJSON(typed[key])
			if err != nil {
				return "", err
			}
			parts = append(parts, string(encodedKey)+":"+encodedValue)
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	default:
		return "", fmt.Errorf("unsupported JSON value %T", value)
	}
}

func languagePackServerDefinitions(packs []languagePackManifest) []lspServerDefinition {
	definitions := make([]lspServerDefinition, 0)
	for _, pack := range packs {
		languages := make(map[string]languagePackLanguage, len(pack.Languages))
		for _, language := range pack.Languages {
			languages[language.ID] = language
		}
		for _, server := range pack.Servers {
			serverLanguages := append([]string(nil), server.Languages...)
			serverLanguages = append(serverLanguages, server.Aliases...)
			candidateLanguages := make([]languagePackLanguage, 0, len(serverLanguages))
			for _, languageID := range serverLanguages {
				candidateLanguages = append(candidateLanguages, languages[languageID])
			}
			candidates := make([]lspExecutableCandidate, 0, len(server.Executables))
			for _, executable := range server.Executables {
				candidates = append(candidates, lspExecutableCandidate{name: executable.CommandName, kind: executable.Kind})
			}
			for _, language := range server.Languages {
				definitions = append(definitions, lspServerDefinition{
					language:              language,
					aliases:               append([]string(nil), server.Aliases...),
					candidates:            append([]lspExecutableCandidate(nil), candidates...),
					args:                  append([]string(nil), server.Args...),
					extensions:            languagePackExtensions(candidateLanguages),
					documentLanguages:     append([]languagePackLanguage(nil), candidateLanguages...),
					installHint:           server.InstallHint,
					workspaceNode:         *server.WorkspaceNode,
					statusOrder:           server.StatusOrder,
					initializationProfile: server.InitializationProfile,
					configurationSections: append([]string(nil), server.ConfigurationSections...),
					configurationResponse: server.ConfigurationResponse,
					versionExecutable:     server.VersionExecutable,
					versionArgs:           append([]string(nil), server.VersionArgs...),
					versionPin:            server.VersionPin,
					preferReactWorkspace:  *server.PreferReactWorkspace,
					reactAware:            *server.ReactAware,
					detectFromWorkspace:   pack.ID != "org.koyori.ide.go" && pack.ID != "org.koyori.ide.typescript",
					sourcePackID:          pack.ID,
					sourcePackVersion:     pack.Version,
				})
			}
		}
	}
	return definitions
}

func languagePackExtensions(languages []languagePackLanguage) []string {
	var extensions []string
	seen := make(map[string]struct{})
	for _, language := range languages {
		for _, extension := range language.Extensions {
			extension = strings.ToLower(extension)
			if _, exists := seen[extension]; exists {
				continue
			}
			seen[extension] = struct{}{}
			extensions = append(extensions, extension)
		}
	}
	return extensions
}

func builtInLanguagePackToolchainCommands() []ToolchainCommand {
	commands := make([]ToolchainCommand, 0)
	seen := make(map[string]string)
	for _, pack := range activeLanguagePackSnapshot() {
		if pack.Toolchain == nil {
			continue
		}
		for _, command := range pack.Toolchain.Commands {
			if owner, exists := seen[command.ID]; exists {
				panic(fmt.Sprintf("language pack toolchain command %q is provided by both %s and %s", command.ID, owner, pack.ID))
			}
			seen[command.ID] = pack.ID
			commands = append(commands, ToolchainCommand{
				ID:                command.ID,
				Label:             command.Label,
				Language:          command.Language,
				Command:           command.Executable,
				Args:              append([]string(nil), command.Args...),
				Description:       command.Description,
				SourcePackID:      pack.ID,
				SourcePackVersion: pack.Version,
			})
		}
	}
	return commands
}

func builtInLanguagePackToolchainInstallHints() map[string]string {
	hints := make(map[string]string)
	for _, pack := range activeLanguagePackSnapshot() {
		if pack.Toolchain == nil {
			continue
		}
		for _, tool := range pack.Toolchain.Tools {
			if tool.InstallHint != "" {
				if existing, exists := hints[tool.Name]; exists && existing != tool.InstallHint {
					panic(fmt.Sprintf("language pack tool %q has conflicting install hints", tool.Name))
				}
				hints[tool.Name] = tool.InstallHint
			}
		}
	}
	return hints
}

func builtInLanguagePackToolchainCommandFileScoped(commandID string) bool {
	for _, pack := range activeLanguagePackSnapshot() {
		if pack.Toolchain == nil {
			continue
		}
		for _, command := range pack.Toolchain.Commands {
			if command.ID == commandID {
				return command.FileScoped
			}
		}
	}
	return false
}

func builtInLanguagePackDebuggerForLanguage(languageID string) (languagePackDebugger, bool) {
	for _, pack := range activeLanguagePackSnapshot() {
		for _, debugger := range pack.Debuggers {
			for _, candidate := range debugger.Languages {
				if candidate == languageID {
					return languagePackDebugger{
						ID: debugger.ID, Protocol: debugger.Protocol, Languages: append([]string(nil), debugger.Languages...),
						Executable: debugger.Executable, Args: append([]string(nil), debugger.Args...), InstallHint: debugger.InstallHint,
						SourcePackID: pack.ID, SourcePackVersion: pack.Version,
					}, true
				}
			}
		}
	}
	return languagePackDebugger{}, false
}

func activeLanguagePackDebuggerForID(debuggerID string) (languagePackDebugger, bool) {
	for _, pack := range activeLanguagePackSnapshot() {
		for _, debugger := range pack.Debuggers {
			if debugger.ID == debuggerID {
				return languagePackDebugger{
					ID: debugger.ID, Protocol: debugger.Protocol, Languages: append([]string(nil), debugger.Languages...),
					Executable: debugger.Executable, Args: append([]string(nil), debugger.Args...), InstallHint: debugger.InstallHint,
					SourcePackID: pack.ID, SourcePackVersion: pack.Version,
				}, true
			}
		}
	}
	return languagePackDebugger{}, false
}

func builtInLanguagePackDebuggerForPath(filePath string) (languagePackDebugger, bool) {
	fileName := strings.ToLower(filepathBasePortable(filePath))
	for _, pack := range activeLanguagePackSnapshot() {
		for _, language := range pack.Languages {
			matched := false
			for _, filename := range language.Filenames {
				if strings.EqualFold(filename, fileName) {
					matched = true
					break
				}
			}
			if !matched {
				for _, extension := range language.Extensions {
					if strings.HasSuffix(fileName, strings.ToLower(extension)) {
						matched = true
						break
					}
				}
			}
			if matched {
				return builtInLanguagePackDebuggerForLanguage(language.ID)
			}
		}
	}
	return languagePackDebugger{}, false
}

func languagePackDocumentLanguage(definition lspServerDefinition, filePath string) string {
	if definition.sourcePackID == "" {
		return ""
	}
	fileName := strings.ToLower(filepathBasePortable(filePath))
	for _, language := range definition.documentLanguages {
		for _, filename := range language.Filenames {
			if strings.EqualFold(filename, fileName) {
				return language.ID
			}
		}
		for _, extension := range language.Extensions {
			if strings.HasSuffix(fileName, strings.ToLower(extension)) {
				return language.ID
			}
		}
	}
	return ""
}

func languagePackServerForPath(filePath string) string {
	for _, definition := range lspServerDefinitionsSnapshot() {
		if languagePackDocumentLanguage(definition, filePath) != "" {
			return definition.language
		}
	}
	return ""
}

func languagePackConfiguration(section string) (language, response string, ok bool) {
	for _, definition := range lspServerDefinitionsSnapshot() {
		if definition.sourcePackID == "" {
			continue
		}
		for _, candidate := range definition.configurationSections {
			if candidate == section {
				return definition.language, definition.configurationResponse, true
			}
		}
	}
	return "", "", false
}

func filepathBasePortable(filePath string) string {
	filePath = strings.ReplaceAll(filePath, "\\", "/")
	if index := strings.LastIndexByte(filePath, '/'); index >= 0 {
		return filePath[index+1:]
	}
	return filePath
}

func refreshLanguagePackDerivedCatalogs() {
	setLSPServerDefinitions(buildLSPServerDefinitions())
	setToolchainLanguagePackCatalog(
		builtInLanguagePackToolchainCommands(),
		builtInLanguagePackToolchainInstallHints(),
	)
}
