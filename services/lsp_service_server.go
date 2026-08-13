package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// lspVersionProbeTimeout bounds version probing of a language server binary
// during detection. A hung or broken server binary must never wedge the
// detect flow (and with it the status-bar LSP menu behind lspState.busy).
const lspVersionProbeTimeout = 5 * time.Second

// rootsToWorkspaceFolders 将根目录列表转换为 LSP WorkspaceFolder[]
// 形如 [{"uri": "file://...", "name": "<base>"}]。
func rootsToWorkspaceFolders(roots []string) []map[string]string {
	out := make([]map[string]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		out = append(out, map[string]string{
			"uri":  pathToURI(r),
			"name": filepath.Base(r),
		})
	}
	return out
}

// discoverLSPWorkspaceRoots expands common monorepo manifests into LSP
// workspace folders while retaining the selected root as the primary folder.
func discoverLSPWorkspaceRoots(root string) []string {
	if root == "" {
		return nil
	}
	roots := []string{root}
	if data, err := os.ReadFile(filepath.Join(root, "go.work")); err == nil && len(data) <= maxLSPPackageJSONBytes {
		inUseBlock := false
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(strings.SplitN(line, "//", 2)[0])
			switch {
			case line == "use (":
				inUseBlock = true
				continue
			case inUseBlock && line == ")":
				inUseBlock = false
				continue
			case strings.HasPrefix(line, "use "):
				line = strings.TrimSpace(strings.TrimPrefix(line, "use "))
			case !inUseBlock:
				continue
			}
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			candidate := strings.Trim(fields[0], `"`)
			if candidate != "" {
				roots = appendExistingLSPWorkspaceRoot(roots, root, candidate)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml")); err == nil && len(data) <= maxLSPPackageJSONBytes {
		var manifest struct {
			Packages []string `yaml:"packages"`
		}
		if yaml.Unmarshal(data, &manifest) == nil {
			for _, pattern := range manifest.Packages {
				matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
				for _, match := range matches {
					roots = appendExistingLSPWorkspaceRoot(roots, "", match)
				}
			}
		}
	}
	return dedupRoots(roots)
}

func appendExistingLSPWorkspaceRoot(roots []string, base, candidate string) []string {
	if base != "" && !filepath.IsAbs(candidate) {
		candidate = filepath.Join(base, candidate)
	}
	candidate = filepath.Clean(candidate)
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return append(roots, candidate)
	}
	return roots
}

type lspExecutableCandidate struct {
	name string
	kind string
}

type lspServerDefinition struct {
	language              string
	aliases               []string
	candidates            []lspExecutableCandidate
	args                  []string
	extensions            []string
	documentLanguages     []languagePackLanguage
	installHint           string
	workspaceNode         bool
	detectFromWorkspace   bool
	framework             string
	localOnly             bool
	statusOrder           int
	initializationProfile string
	configurationSections []string
	configurationResponse string
	versionExecutable     string
	versionArgs           []string
	versionPin            string
	preferReactWorkspace  bool
	reactAware            bool
	sourcePackID          string
	sourcePackVersion     string
}

// lspServerDefinitions is ordered so status output remains stable across
// calls. JavaScript and TypeScript intentionally keep independent processes;
// likewise every 10F server owns its process, JSON-RPC client and document map.
var (
	lspServerDefinitionsMu sync.RWMutex
	lspServerDefinitions   = buildLSPServerDefinitions()
)

var baseLSPServerDefinitions = []lspServerDefinition{
	{
		language:            "python",
		statusOrder:         20,
		candidates:          []lspExecutableCandidate{{name: "pyright-langserver", kind: "pyright-langserver"}},
		args:                []string{"--stdio"},
		extensions:          []string{".py", ".pyi"},
		installHint:         "npm install --save-dev pyright",
		workspaceNode:       true,
		detectFromWorkspace: true,
	},
	{
		language:            "rust",
		statusOrder:         30,
		candidates:          []lspExecutableCandidate{{name: "rust-analyzer", kind: "rust-analyzer"}},
		extensions:          []string{".rs"},
		installHint:         "install rust-analyzer with rustup component add rust-analyzer",
		detectFromWorkspace: true,
	},
	{
		language:      "json",
		statusOrder:   60,
		candidates:    []lspExecutableCandidate{{name: "vscode-json-languageserver", kind: "vscode-json-languageserver"}},
		args:          []string{"--stdio"},
		installHint:   "npm install --save-dev vscode-langservers-extracted",
		workspaceNode: true,
	},
	{
		language:      "css",
		statusOrder:   70,
		candidates:    []lspExecutableCandidate{{name: "vscode-css-languageserver", kind: "vscode-css-languageserver"}},
		args:          []string{"--stdio"},
		installHint:   "npm install --save-dev vscode-langservers-extracted",
		workspaceNode: true,
	},
	{
		language:      "html",
		statusOrder:   80,
		candidates:    []lspExecutableCandidate{{name: "vscode-html-languageserver", kind: "vscode-html-languageserver"}},
		args:          []string{"--stdio"},
		installHint:   "npm install --save-dev vscode-langservers-extracted",
		workspaceNode: true,
	},
	{
		language:      "yaml",
		statusOrder:   90,
		candidates:    []lspExecutableCandidate{{name: "yaml-language-server", kind: "yaml-language-server"}},
		args:          []string{"--stdio"},
		installHint:   "npm install --save-dev yaml-language-server",
		workspaceNode: true,
	},
	{
		language:      "eslint",
		statusOrder:   100,
		candidates:    []lspExecutableCandidate{{name: "vscode-eslint-language-server", kind: "vscode-eslint-language-server"}},
		args:          []string{"--stdio"},
		installHint:   "npm install --save-dev vscode-langservers-extracted eslint",
		workspaceNode: true,
	},
	{
		language:      "vue",
		statusOrder:   110,
		candidates:    []lspExecutableCandidate{{name: "vue-language-server", kind: "vue-language-server"}},
		args:          []string{"--stdio"},
		installHint:   "npm install --save-dev @vue/language-server typescript",
		workspaceNode: true,
		framework:     "vue",
		localOnly:     true,
	},
	{
		language:      "angular",
		statusOrder:   120,
		candidates:    []lspExecutableCandidate{{name: "ngserver", kind: "angular-language-server"}},
		installHint:   "npm install --save-dev @angular/language-server typescript",
		workspaceNode: true,
		framework:     "angular",
		localOnly:     true,
	},
}

func buildLSPServerDefinitions() []lspServerDefinition {
	definitions := languagePackServerDefinitions(activeLanguagePackSnapshot())
	packLanguages := make(map[string]struct{})
	for _, definition := range definitions {
		packLanguages[definition.language] = struct{}{}
		for _, alias := range definition.aliases {
			packLanguages[alias] = struct{}{}
		}
	}
	for _, definition := range baseLSPServerDefinitions {
		if _, replaced := packLanguages[definition.language]; replaced {
			continue
		}
		definitions = append(definitions, definition)
	}
	sort.SliceStable(definitions, func(left, right int) bool {
		return definitions[left].statusOrder < definitions[right].statusOrder
	})
	return definitions
}

func lspServerDefinitionsSnapshot() []lspServerDefinition {
	lspServerDefinitionsMu.RLock()
	definitions := append([]lspServerDefinition(nil), lspServerDefinitions...)
	lspServerDefinitionsMu.RUnlock()
	return definitions
}

func setLSPServerDefinitions(definitions []lspServerDefinition) {
	lspServerDefinitionsMu.Lock()
	lspServerDefinitions = append([]lspServerDefinition(nil), definitions...)
	lspServerDefinitionsMu.Unlock()
}

func lspServerKey(language string) string {
	normalized := strings.ToLower(strings.TrimSpace(language))
	for _, definition := range lspServerDefinitionsSnapshot() {
		if definition.language == normalized {
			return definition.language
		}
		for _, alias := range definition.aliases {
			if alias == normalized {
				return definition.language
			}
		}
	}
	switch normalized {
	case "python", "py":
		return "python"
	case "rust":
		return "rust"
	case "json", "jsonc":
		return "json"
	case "css", "scss", "less":
		return "css"
	case "html":
		return "html"
	case "yaml", "yml":
		return "yaml"
	case "eslint":
		return "eslint"
	case "vue", "volar":
		return "vue"
	case "angular":
		return "angular"
	default:
		return ""
	}
}

type workspaceFrameworks struct {
	Vue           bool
	Angular       bool
	React         bool
	TypeScriptSDK string
}

const maxLSPPackageJSONBytes = 1 << 20

func detectWorkspaceFrameworks(root string) workspaceFrameworks {
	var result workspaceFrameworks
	if root == "" {
		return result
	}

	dependencies := make(map[string]json.RawMessage)
	packagePath := filepath.Join(root, "package.json")
	if file, err := os.Open(packagePath); err == nil {
		var manifest struct {
			Dependencies    map[string]json.RawMessage `json:"dependencies"`
			DevDependencies map[string]json.RawMessage `json:"devDependencies"`
		}
		decoder := json.NewDecoder(io.LimitReader(file, maxLSPPackageJSONBytes+1))
		decodeErr := decoder.Decode(&manifest)
		closeErr := file.Close()
		if decodeErr == nil && closeErr == nil {
			for name, value := range manifest.Dependencies {
				dependencies[name] = value
			}
			for name, value := range manifest.DevDependencies {
				dependencies[name] = value
			}
		}
	}

	result.Vue = hasLSPDependency(dependencies, "vue") || workspaceHasAnyFile(root,
		"vue.config.js", "vue.config.cjs", "vue.config.mjs", "vue.config.ts")
	result.Angular = hasLSPDependency(dependencies, "@angular/core") &&
		workspaceHasAnyFile(root, "angular.json")
	result.React = hasLSPDependency(dependencies, "react")
	result.TypeScriptSDK = workspaceTypeScriptSDK(root)
	return result
}

func detectLSPWorkspaceLanguages(roots []string) map[string]bool {
	extensionLanguages := make(map[string]string)
	languageCount := 0
	for _, definition := range lspServerDefinitionsSnapshot() {
		if !definition.detectFromWorkspace {
			continue
		}
		languageCount++
		for _, extension := range definition.extensions {
			extensionLanguages[strings.ToLower(extension)] = definition.language
		}
	}

	found := make(map[string]bool, languageCount)
	allFound := errors.New("all optional LSP workspace languages found")
	for _, root := range roots {
		if root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				if path == root {
					return nil
				}
				switch strings.ToLower(entry.Name()) {
				case ".git", "node_modules", ".venv", "venv", ".tox", "__pycache__", "target":
					return filepath.SkipDir
				}
				return nil
			}
			language := extensionLanguages[strings.ToLower(filepath.Ext(entry.Name()))]
			if language == "" || found[language] {
				return nil
			}
			found[language] = true
			if len(found) == languageCount {
				return allFound
			}
			return nil
		})
		if errors.Is(err, allFound) {
			break
		}
	}
	return found
}

func hasLSPDependency(dependencies map[string]json.RawMessage, name string) bool {
	_, ok := dependencies[name]
	return ok
}

func workspaceHasAnyFile(root string, names ...string) bool {
	for _, name := range names {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func workspaceTypeScriptSDK(root string) string {
	if root == "" {
		return ""
	}
	sdk := filepath.Join(root, "node_modules", "typescript", "lib")
	for _, name := range []string{"tsserverlibrary.js", "typescript.js", "tsserver.js"} {
		if info, err := os.Stat(filepath.Join(sdk, name)); err == nil && !info.IsDir() {
			return sdk
		}
	}
	return ""
}

func frameworkApplies(definition lspServerDefinition, frameworks workspaceFrameworks) bool {
	switch definition.framework {
	case "vue":
		return frameworks.Vue
	case "angular":
		return frameworks.Angular
	default:
		return true
	}
}

type lspServerResolution struct {
	Path          string
	Version       string
	VersionError  string
	Kind          string
	WorkspaceRoot string
	Applicable    bool
}

func resolveLSPServerForRoots(language string, roots []string) lspServerResolution {
	definition, ok := lspDefinitionForLanguage(language)
	if !ok {
		return lspServerResolution{}
	}
	roots = dedupRoots(roots)
	applicableRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if frameworkApplies(definition, detectWorkspaceFrameworks(root)) {
			applicableRoots = append(applicableRoots, root)
		}
	}
	if definition.framework != "" && len(applicableRoots) == 0 {
		return lspServerResolution{}
	}
	if definition.framework == "" {
		applicableRoots = roots
	}
	if definition.preferReactWorkspace {
		reactRoots := make([]string, 0, len(applicableRoots))
		otherRoots := make([]string, 0, len(applicableRoots))
		for _, root := range applicableRoots {
			if detectWorkspaceFrameworks(root).React {
				reactRoots = append(reactRoots, root)
			} else {
				otherRoots = append(otherRoots, root)
			}
		}
		applicableRoots = append(reactRoots, otherRoots...)
	}
	resolution := lspServerResolution{Applicable: true}
	if len(applicableRoots) > 0 {
		resolution.WorkspaceRoot = applicableRoots[0]
	}
	if definition.workspaceNode {
		for _, root := range applicableRoots {
			for _, candidate := range definition.candidates {
				local := filepath.Join(root, "node_modules", ".bin", candidate.name)
				if path, err := exec.LookPath(local); err == nil {
					resolution.Path = path
					resolution.Kind = candidate.kind
					resolution.WorkspaceRoot = root
					resolution.Version = detectLSPServerVersion(definition, path, root)
					resolution.VersionError = validateLSPServerVersionPin(definition, resolution.Version)
					return resolution
				}
			}
		}
	}
	if definition.localOnly {
		return resolution
	}
	for _, candidate := range definition.candidates {
		if path, err := exec.LookPath(candidate.name); err == nil {
			resolution.Path = path
			resolution.Kind = candidate.kind
			resolution.Version = detectLSPServerVersion(definition, path, resolution.WorkspaceRoot)
			resolution.VersionError = validateLSPServerVersionPin(definition, resolution.Version)
			return resolution
		}
	}
	return resolution
}

func validateLSPServerVersionPin(definition lspServerDefinition, version string) string {
	if definition.versionPin == "" {
		return ""
	}
	if version == "" {
		return fmt.Sprintf("could not verify required language server version %s", definition.versionPin)
	}
	pattern := regexp.MustCompile(`(^|[^0-9A-Za-z.-])` + regexp.QuoteMeta(definition.versionPin) + `($|[^0-9A-Za-z.-])`)
	if !pattern.MatchString(version) {
		return fmt.Sprintf("language server version %q does not match required version %s", version, definition.versionPin)
	}
	return ""
}

func detectLSPServerVersion(definition lspServerDefinition, serverPath, workspaceRoot string) string {
	if len(definition.versionArgs) == 0 {
		return ""
	}
	versionPath := serverPath
	if definition.versionExecutable != "" {
		versionPath = ""
		if definition.workspaceNode && workspaceRoot != "" {
			local := filepath.Join(workspaceRoot, "node_modules", ".bin", definition.versionExecutable)
			if path, err := exec.LookPath(local); err == nil {
				versionPath = path
			}
		}
		if versionPath == "" {
			if path, err := exec.LookPath(definition.versionExecutable); err == nil {
				versionPath = path
			}
		}
	}
	if versionPath == "" {
		return ""
	}
	return tryVersion(versionPath, definition.versionArgs...)
}

func lspDefinitionForLanguage(language string) (lspServerDefinition, bool) {
	key := lspServerKey(language)
	for _, definition := range lspServerDefinitionsSnapshot() {
		if definition.language == key {
			return definition, true
		}
	}
	return lspServerDefinition{}, false
}

// serverNameForLanguage returns a preferred executable label for errors.
func serverNameForLanguage(language string) (exe string, ok bool) {
	definition, ok := lspDefinitionForLanguage(language)
	if !ok || len(definition.candidates) == 0 {
		return "", false
	}
	return definition.candidates[0].name, true
}

// DetectLSPServers keeps the original eight-item result for empty workspaces
// and adds optional language servers when their source files are present.
func (s *LSPService) DetectLSPServers() []LSPServerStatus {
	return s.detectLSPServers(false)
}

// DetectAllLSPServers includes optional language extensions such as Python
// and Rust without changing the existing DetectLSPServers contract.
func (s *LSPService) DetectAllLSPServers() []LSPServerStatus {
	return s.detectLSPServers(true)
}

func (s *LSPService) detectLSPServers(includeOptional bool) []LSPServerStatus {
	s.mu.Lock()
	wsRoot := s.workspaceRoot
	wsRoots := append([]string(nil), s.workspaceRoots...)
	workspaceContext := s.workspaceContext
	running := make(map[string]bool, len(s.servers))
	for lang, srv := range s.servers {
		running[lang] = srv != nil && srv.running
	}
	errs := make(map[string]string, len(s.lastErrors))
	for k, v := range s.lastErrors {
		errs[k] = v
	}
	s.mu.Unlock()
	if workspaceContext != nil {
		activeRoot, err := workspaceContext.RequireRoot()
		if err != nil || wsRoot == "" || !sameWorkspaceIdentityPath(activeRoot, wsRoot) {
			definitions := lspServerDefinitionsSnapshot()
			statuses := make([]LSPServerStatus, 0, len(definitions))
			for _, definition := range definitions {
				statuses = append(statuses, LSPServerStatus{
					Language:          definition.language,
					InstallHint:       definition.installHint,
					Framework:         definition.framework,
					LastError:         "Open a workspace before detecting language servers",
					SourcePackID:      definition.sourcePackID,
					SourcePackVersion: definition.sourcePackVersion,
				})
			}
			return statuses
		}
	}
	if len(wsRoots) == 0 && wsRoot != "" {
		wsRoots = []string{wsRoot}
	}
	workspaceLanguages := map[string]bool(nil)
	if !includeOptional {
		workspaceLanguages = detectLSPWorkspaceLanguages(wsRoots)
	}

	definitions := lspServerDefinitionsSnapshot()
	statuses := make([]LSPServerStatus, 0, len(definitions))
	for _, definition := range definitions {
		lang := definition.language
		if definition.detectFromWorkspace && !includeOptional && !workspaceLanguages[lang] && !running[lang] && errs[lang] == "" {
			continue
		}
		resolution := resolveLSPServerForRoots(lang, wsRoots)
		if !resolution.Applicable {
			continue
		}
		st := LSPServerStatus{
			Language:          lang,
			InstallHint:       definition.installHint,
			ServerPath:        resolution.Path,
			Version:           resolution.Version,
			ServerKind:        resolution.Kind,
			Available:         resolution.Path != "" && resolution.VersionError == "",
			Framework:         definition.framework,
			WorkspaceRoot:     resolution.WorkspaceRoot,
			SourcePackID:      definition.sourcePackID,
			SourcePackVersion: definition.sourcePackVersion,
		}
		if definition.reactAware && workspaceRootsContainFramework(wsRoots, "react") {
			st.Framework = "react"
		}
		st.Running = running[lang]
		st.LastError = errs[lang]
		if st.LastError == "" {
			st.LastError = resolution.VersionError
		}
		statuses = append(statuses, st)
	}
	return statuses
}

// detectServerPath finds the language server executable.
// Returns path, version, kind. Empty path if not found.
//
// prompt-8 Task 8-B / BUG-IDE-02: for TS/JS prefer typescript-language-server
// or vtsls over raw tsserver (proprietary protocol).
func detectServerPath(language, workspaceRoot string) (path, version, kind string) {
	roots := []string(nil)
	if workspaceRoot != "" {
		roots = []string{workspaceRoot}
	}
	resolution := resolveLSPServerForRoots(language, roots)
	return resolution.Path, resolution.Version, resolution.Kind
}

func workspaceRootsContainFramework(roots []string, framework string) bool {
	for _, root := range roots {
		frameworks := detectWorkspaceFrameworks(root)
		switch framework {
		case "react":
			if frameworks.React {
				return true
			}
		case "vue":
			if frameworks.Vue {
				return true
			}
		case "angular":
			if frameworks.Angular {
				return true
			}
		}
	}
	return false
}

// tryVersion runs `<exe> <flag>` and returns the trimmed first line of output.
// A short timeout bounds the probe: a hung language-server binary (broken
// install, slow first-run) must not block LSP detection indefinitely (which
// would also wedge the status-bar menu behind lspState.busy).
func tryVersion(exe string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), lspVersionProbeTimeout)
	defer cancel()
	cmd := commandContext(ctx, exe, args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0])
	}
	return ""
}

// StartLSPServer starts an LSP server for the given language. It is a no-op
// (returns nil) if the server is already running. Returns an error if the
// server binary is not installed or fails to start.
func (s *LSPService) StartLSPServer(language string) error {
	key := lspServerKey(language)
	if key == "" {
		return fmt.Errorf("unsupported language: %s", language)
	}
	if !s.lifecycleMu.TryRLock() {
		s.mu.Lock()
		switching := s.switching
		s.mu.Unlock()
		if switching {
			return errWorkspaceSwitching
		}
		s.lifecycleMu.RLock()
	}
	defer s.lifecycleMu.RUnlock()
	languageLock := s.languageLifecycleLock(key)
	languageLock.Lock()
	defer languageLock.Unlock()
	return s.startLSPServer(key, false)
}

// startLSPServer runs with lifecycleMu held for writing, or with its read lock
// and the language-specific lifecycle lock held. allowSwitching is used only
// by phase three of a workspace switch while public queries remain gated.
func (s *LSPService) startLSPServer(language string, allowSwitching bool) error {
	requestedLanguage := language
	language = lspServerKey(language)
	if language == "" {
		return fmt.Errorf("unsupported language: %s", requestedLanguage)
	}

	s.mu.Lock()
	if s.switching && !allowSwitching {
		s.mu.Unlock()
		return errWorkspaceSwitching
	}
	if srv, ok := s.servers[language]; ok && srv != nil && srv.running {
		s.mu.Unlock()
		return nil // already running
	}
	workspaceRoot := s.workspaceRoot
	workspaceRoots := append([]string(nil), s.workspaceRoots...)
	workspaceContext := s.workspaceContext
	s.mu.Unlock()
	lease := workspaceLease{root: workspaceRoot}
	if workspaceContext != nil {
		var err error
		lease, err = acquireWorkspaceLease(workspaceContext, "", 0)
		if err != nil {
			return err
		}
		if workspaceRoot == "" || !sameWorkspaceIdentityPath(workspaceRoot, lease.root) {
			return fmt.Errorf("LSP workspace switch is not committed: %w", ErrNotAllowed)
		}
	}

	_, ok := serverNameForLanguage(language)
	if !ok {
		return fmt.Errorf("unsupported language: %s", requestedLanguage)
	}
	definition, _ := lspDefinitionForLanguage(language)
	roots := append([]string(nil), workspaceRoots...)
	if len(roots) == 0 && workspaceRoot != "" {
		roots = []string{workspaceRoot}
	}
	resolution := resolveLSPServerForRoots(language, roots)
	if !resolution.Applicable {
		frameworkName := strings.ToUpper(definition.framework[:1]) + definition.framework[1:]
		err := fmt.Errorf("%s language server is only available in an %s project workspace", frameworkName, frameworkName)
		if definition.framework == "vue" {
			err = fmt.Errorf("vue language server is only available in a Vue project workspace")
		}
		s.setLastError(language, err)
		return err
	}
	if resolution.Path == "" {
		scope := ""
		if definition.localOnly {
			scope = " in this workspace"
		}
		err := fmt.Errorf("language server not installed%s for %s; install it with: %s", scope, language, definition.installHint)
		s.setLastError(language, err)
		// A10: optional Python/Rust servers degrade gracefully when absent.
		if language == "python" || language == "rust" {
			return nil
		}
		return err
	}
	if resolution.VersionError != "" {
		err := errors.New(resolution.VersionError)
		s.setLastError(language, err)
		return err
	}
	serverRoot := resolution.WorkspaceRoot
	if serverRoot == "" {
		serverRoot = workspaceRoot
	}
	if definition.framework != "" && workspaceTypeScriptSDK(serverRoot) == "" {
		err := fmt.Errorf("workspace TypeScript SDK is missing for %s; install it with: %s", language, definition.installHint)
		s.setLastError(language, err)
		return err
	}
	if workspaceContext != nil {
		if err := lease.validateCurrent(); err != nil {
			return err
		}
	}

	process, stdin, stdout, err := startServerProcess(language, resolution.Path, resolution.Kind, serverRoot)
	if err != nil {
		s.setLastError(language, err)
		return fmt.Errorf("failed to start %s: %w", resolution.Kind, err)
	}
	cmd := process.cmd

	client := newJSONRPCClientWithHandler(stdout, stdin, s.handleServerRequest)
	// F-2 (prompt-2.md): 注册 server→client request handler，使 gopls /
	// ts-server 能读取客户端配置（workspace/configuration）、主动应用编辑
	// （workspace/applyEdit）、查询工作区（workspace/workspaceFolders）。
	srv := &lspServer{
		cmd:                   cmd,
		process:               process,
		running:               false,
		stdin:                 stdin,
		stdout:                stdout,
		client:                client,
		managed:               true,
		docVersions:           make(map[string]int),
		docHashes:             make(map[string]string),
		docLastContent:        make(map[string]string),
		docLastSync:           make(map[string]time.Time),
		pendingChanges:        make(map[string]*pendingDocumentChange),
		syncKind:              1,
		semanticTokenResults:  make(map[string]map[string][]int),
		semanticTokenLatest:   make(map[string]string),
		semanticLatestRequest: make(map[string]uint64),
		diags:                 make(map[string][]Diagnostic),
		diagResultIDs:         make(map[string]string),
		diagEpochs:            make(map[string]uint64),
		diagLatestRequests:    make(map[string]uint64),
	}
	client.setRequestHandler(func(method string, params json.RawMessage) (interface{}, error) {
		return s.handleServerRequestForLanguage(language, method, params)
	})
	client.setWriteFailureHandler(func(writeErr error) {
		s.rebuildLSPServerAfterWriteFailures(language, client, writeErr)
	})
	// G-FEAT-02: collect published diagnostics so GetDiagnostics can return
	// them. The server pushes diagnostics asynchronously after didOpen.
	srv.client.onNotification("textDocument/publishDiagnostics", func(params json.RawMessage) {
		s.handlePublishedDiagnostics(language, srv, params)
	})
	refreshDiagnostics := func(json.RawMessage) {
		s.markDiagnosticsRefresh(language)
	}
	// LSP 3.17 uses workspace/diagnostic/refresh as a request. Accept the
	// historical notification spelling too for compatibility with servers that
	// shipped the draft protocol.
	srv.client.onNotification("workspace/diagnostic/refresh", refreshDiagnostics)
	srv.client.onNotification("workspace/refreshDiagnostics", refreshDiagnostics)
	// Send the LSP initialize handshake. Priority 4: 把多根列表传入，
	// initializeLocked 会据此构造 workspaceFolders 参数。
	if err := s.initializeLocked(srv, language, serverRoot, workspaceRoots); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), lspProcessStopTimeout)
		stopErr := stopLSPServerProcess(ctx, srv, errLSPServerStopping)
		cancel()
		s.setLastError(language, err)
		if stopErr != nil {
			return fmt.Errorf("LSP initialize failed for %s: %w (stop failed: %v)", language, err, stopErr)
		}
		return fmt.Errorf("LSP initialize failed for %s: %w", language, err)
	}
	if workspaceContext != nil {
		if err := lease.validateCurrent(); err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), lspProcessStopTimeout)
			stopErr := stopLSPServerProcess(ctx, srv, errWorkspaceSwitching)
			cancel()
			return errors.Join(err, stopErr)
		}
	}

	s.mu.Lock()
	if s.switching && !allowSwitching {
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), lspProcessStopTimeout)
		_ = stopLSPServerProcess(ctx, srv, errLSPServerStopping)
		cancel()
		return errWorkspaceSwitching
	}
	srv.running = true
	s.servers[language] = srv
	if s.lastErrors != nil {
		delete(s.lastErrors, language)
	}
	s.mu.Unlock()
	go s.observeLSPProcess(language, srv)
	return nil
}

// buildLSPServerCommand builds argv directly; no shell command string is ever
// interpreted. It is separate from process startup so registry argv is testable.
func buildLSPServerCommand(language, exePath, kind, workspaceRoot string) (*exec.Cmd, error) {
	definition, ok := lspDefinitionForLanguage(language)
	if !ok {
		return nil, fmt.Errorf("unsupported language: %s", language)
	}
	kindAllowed := false
	for _, candidate := range definition.candidates {
		if candidate.kind == kind {
			kindAllowed = true
			break
		}
	}
	if !kindAllowed {
		return nil, fmt.Errorf("unsupported %s language server kind: %s", definition.language, kind)
	}
	args := append([]string(nil), definition.args...)
	if definition.language == "angular" {
		nodeModules := filepath.Join(workspaceRoot, "node_modules")
		args = []string{
			"--stdio",
			"--tsProbeLocations", nodeModules,
			"--ngProbeLocations", nodeModules,
			"--includeAutomaticOptionalChainCompletions",
			"--includeCompletionsWithSnippetText",
		}
	}
	cmd := command(exePath, args...)
	if workspaceRoot != "" {
		cmd.Dir = workspaceRoot
	}
	return cmd, nil
}

// startServerProcess launches the language server process and returns its
// stdin/stdout pipes. Each returned lspProcess creates the sole Cmd.Wait owner.
func startServerProcess(language, exePath, kind, workspaceRoot string) (*lspProcess, io.WriteCloser, io.ReadCloser, error) {
	cmd, err := buildLSPServerCommand(language, exePath, kind, workspaceRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	cmd.Env = os.Environ()
	if isCmdShim(exePath) && filepath.IsAbs(exePath) {
		cmd.Env = withPrependedPath(cmd.Env, filepath.Dir(exePath))
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, nil, err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, nil, nil, err
	}
	return newLSPProcess(cmd), stdin, stdout, nil
}

// buildLSPClientCapabilities 构造 LSP initialize 请求中声明的客户端能力
// （ClientCapabilities）。Priority 4 (prompt-1.md) 重点声明：
//   - workspace.workspaceFolders = true
//
// Architecture C (prompt-1.md 491-500): 补全 LSP 客户端能力声明，使服务器
// 启用 callHierarchy / typeHierarchy / declaration / documentLink /
// selectionRange / foldingRange / colorProvider / inlineValue /
// linkedEditingRange，并将 semanticTokens.range 置为 true。
//
// 抽成独立函数便于单元测试直接断言 capability 结构（避免启动真实 LSP 进程）。
func buildLSPClientCapabilities() map[string]interface{} {
	// prompt-8 M20: declare client capabilities so servers enable completion,
	// hover, definition, formatting, rename, etc.
	// G-COMP-02: added documentSymbol, semanticTokens, workspace.symbol support.
	// Priority 4 (prompt-1.md): declare workspace folder support. The client
	// capability is a boolean; supported/changeNotifications belong to the
	// server capability returned from initialize.
	return map[string]interface{}{
		"textDocument": map[string]interface{}{
			"synchronization": map[string]interface{}{
				"dynamicRegistration": false,
				"willSave":            true,
				"willSaveWaitUntil":   true,
				"didSave":             true,
				"save":                map[string]interface{}{"includeText": true},
			},
			"completion": map[string]interface{}{
				"completionItem": map[string]interface{}{
					"snippetSupport":          true,
					"documentationFormat":     []string{"markdown", "plaintext"},
					"commitCharactersSupport": true,
					"preselectSupport":        true,
					"tagSupport":              map[string]interface{}{"valueSet": []int{1}},
					"insertReplaceSupport":    true,
					"insertTextModeSupport":   map[string]interface{}{"valueSet": []int{1, 2}},
					"resolveSupport": map[string]interface{}{
						"properties": []string{"documentation", "detail", "additionalTextEdits", "labelDetails"},
					},
					"labelDetailsSupport": true,
				},
				"completionList": map[string]interface{}{
					"itemDefaults": []string{"commitCharacters", "editRange", "insertTextFormat", "insertTextMode"},
				},
			},
			"hover":              map[string]interface{}{"contentFormat": []string{"markdown", "plaintext"}},
			"definition":         map[string]interface{}{},
			"references":         map[string]interface{}{},
			"rename":             map[string]interface{}{"prepareSupport": false},
			"formatting":         map[string]interface{}{},
			"publishDiagnostics": map[string]interface{}{},
			"diagnostic": map[string]interface{}{
				"dynamicRegistration":    false,
				"relatedDocumentSupport": true,
			},
			"documentSymbol": map[string]interface{}{
				"hierarchicalDocumentSymbolSupport": true,
			},
			"semanticTokens": map[string]interface{}{
				"dynamicRegistration": false,
				"requests": map[string]interface{}{
					"full": map[string]interface{}{"delta": true},
					// Architecture C (prompt-1.md 499): range=true so servers
					// honor textDocument/semanticTokens/range requests for
					// viewport-scoped highlighting.
					"range": true,
				},
				"tokenTypes":              append([]string(nil), canonicalSemanticTokenTypes...),
				"tokenModifiers":          append([]string(nil), canonicalSemanticTokenModifiers...),
				"formats":                 []string{"relative"},
				"multilineTokenSupport":   true,
				"overlappingTokenSupport": false,
			},
			// Priority 1: inlayHint — enable textDocument/inlayHint so servers
			// return inline type/parameter annotations. resolveSupport lets the
			// server fill in tooltip / label parts lazily.
			"inlayHint": map[string]interface{}{
				"dynamicRegistration": true,
				"resolveSupport": map[string]interface{}{
					"properties": []string{"tooltip", "textEdits", "label.tooltip", "label.location"},
				},
			},
			// Architecture C (prompt-1.md 497): callHierarchy — enables
			// textDocument/prepareCallHierarchy + incoming/outgoing calls for
			// refactor navigation.
			"callHierarchy": map[string]interface{}{
				"dynamicRegistration": false,
			},
			"codeLens": map[string]interface{}{
				"dynamicRegistration": true,
				"resolveSupport": map[string]interface{}{
					"properties": []string{"command"},
				},
			},
			"codeAction": map[string]interface{}{
				"dynamicRegistration": true,
				"dataSupport":         true,
				"isPreferredSupport":  true,
				"resolveSupport": map[string]interface{}{
					"properties": []string{"edit", "command"},
				},
			},
			// Architecture C (prompt-1.md 497): typeHierarchy — enables
			// textDocument/prepareTypeHierarchy + supertypes/subtypes.
			"typeHierarchy": map[string]interface{}{
				"dynamicRegistration": true,
			},
			// Architecture C (prompt-1.md 498): declaration — enables
			// textDocument/declaration. linkSupport=true lets the server return
			// LocationLink[] (multiple targets) instead of just Location.
			"declaration": map[string]interface{}{
				"dynamicRegistration": true,
				"linkSupport":         true,
			},
			// Architecture C (prompt-1.md 498): documentLink — enables
			// textDocument/documentLink for clickable URLs in code.
			// tooltipSupport=true lets the server provide hover tooltips on links.
			"documentLink": map[string]interface{}{
				"dynamicRegistration": true,
				"tooltipSupport":      true,
			},
			// Architecture C (prompt-1.md 498): selectionRange — enables
			// textDocument/selectionRange for expand/shrink selection.
			"selectionRange": map[string]interface{}{
				"dynamicRegistration": true,
			},
			// Architecture C (prompt-1.md 493): foldingRange — enables
			// textDocument/foldingRange for code folding regions.
			"foldingRange": map[string]interface{}{
				"dynamicRegistration": true,
			},
			// Architecture C (prompt-1.md 493): colorProvider — enables
			// textDocument/documentColor for color picker integration.
			"colorProvider": map[string]interface{}{
				"dynamicRegistration": true,
			},
			// Architecture C (prompt-1.md 500): inlineValue — enables
			// textDocument/inlineValue for debugger inline value display.
			"inlineValue": map[string]interface{}{
				"dynamicRegistration": true,
			},
			// Architecture C (prompt-1.md 493): linkedEditingRange — enables
			// textDocument/linkedEditingRange for synchronized tag editing.
			"linkedEditingRange": map[string]interface{}{
				"dynamicRegistration": true,
			},
		},
		"workspace": map[string]interface{}{
			"workspaceFolders": true,
			// F-2 (prompt-2.md): 声明客户端支持 workspace/configuration 请求
			// （server 拉取 per-resource 配置）与 workspace.applyEdit 请求
			// （server 推送 WorkspaceEdit 让客户端应用编辑）。
			"configuration": true,
			"applyEdit":     true,
			"diagnostics": map[string]interface{}{
				"refreshSupport": true,
			},
			"symbol": map[string]interface{}{
				"symbolKind": map[string]interface{}{
					"valueSet": []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26},
				},
			},
		},
	}
}

func lspInitializationOptions(language, workspaceRoot string) map[string]interface{} {
	if definition, ok := lspDefinitionForLanguage(language); ok {
		switch definition.initializationProfile {
		case "go":
			return map[string]interface{}{
				"completion.usePlaceholders":  true,
				"completion.deep":             true,
				"completion.budget":           "100ms",
				"completion.matcher":          "fuzzy",
				"staticcheck":                 true,
				"ui.completion.documentation": true,
				"ui.diagnostic.staticcheck":   true,
				"allExperiments":              true,
			}
		case "typescript":
			return typescriptLSPInitializationOptions(workspaceRoot)
		}
	}
	switch lspServerKey(language) {
	case "python":
		return map[string]interface{}{
			"python": map[string]interface{}{
				"analysis": map[string]interface{}{
					"autoImportCompletions":  true,
					"autoSearchPaths":        true,
					"diagnosticMode":         "workspace",
					"typeCheckingMode":       "basic",
					"useLibraryCodeForTypes": true,
				},
			},
		}
	case "rust":
		return map[string]interface{}{
			"cargo":     map[string]interface{}{"allFeatures": true},
			"check":     map[string]interface{}{"command": "check"},
			"procMacro": map[string]interface{}{"enable": true},
			"inlayHints": map[string]interface{}{
				"bindingModeHints": map[string]interface{}{"enable": true},
				"typeHints":        map[string]interface{}{"enable": true},
			},
		}
	case "vue":
		return map[string]interface{}{
			"typescript": map[string]interface{}{
				"tsdk": workspaceTypeScriptSDK(workspaceRoot),
			},
			// Standalone mode lets Volar own Vue virtual files. Hybrid mode
			// requires a separate TypeScript plugin host that Koyori IDE does not run.
			"vue": map[string]interface{}{
				"hybridMode": false,
			},
		}
	case "json":
		return BuildJSONLSPInitializationOptions(workspaceRoot)
	case "css", "html":
		return map[string]interface{}{"provideFormatter": true}
	case "yaml":
		return map[string]interface{}{"isKubernetes": false}
	case "eslint":
		folder := map[string]interface{}{
			"name": filepath.Base(workspaceRoot),
			"uri":  pathToURI(workspaceRoot),
		}
		return map[string]interface{}{
			"codeActionOnSave": map[string]interface{}{"enable": false, "mode": "all"},
			"format":           false,
			"nodePath":         nil,
			"onIgnoredFiles":   "off",
			"options":          map[string]interface{}{},
			"packageManager":   "npm",
			"problems":         map[string]interface{}{"shortenToSingleLine": false},
			"quiet":            false,
			"run":              "onType",
			"useESLintClass":   false,
			"validate":         "on",
			"workingDirectory": map[string]interface{}{"mode": "location"},
			"workspaceFolder":  folder,
		}
	default:
		return nil
	}
}

func typescriptLSPInitializationOptions(workspaceRoot string) map[string]interface{} {
	frameworks := detectWorkspaceFrameworks(workspaceRoot)
	preferences := map[string]interface{}{
		"includeCompletionsForImportStatements":     true,
		"includeCompletionsForModuleExports":        true,
		"includeCompletionsForSnippetText":          true,
		"includeCompletionsWithSnippetText":         true,
		"includeCompletionsWithInsertText":          true,
		"includeCompletionsWithClassMemberSnippets": true,
		"includeAutomaticOptionalChainCompletions":  true,
		"allowIncompleteCompletions":                true,
	}
	if frameworks.React {
		preferences["jsxAttributeCompletionStyle"] = "auto"
	}
	options := map[string]interface{}{
		"hostInfo":    "koyori-ide",
		"preferences": preferences,
	}
	if frameworks.TypeScriptSDK != "" {
		options["typescript"] = map[string]interface{}{"tsdk": frameworks.TypeScriptSDK}
		options["tsserver"] = map[string]interface{}{
			"path": filepath.Join(frameworks.TypeScriptSDK, "tsserver.js"),
		}
	}
	return options
}

// initializeLocked sends the LSP initialize/initialized handshake. The legacy
// name is retained for tests and callers; the lifecycle read lock plus the
// language-specific lock serialize each server without blocking other languages.
func (s *LSPService) initializeLocked(srv *lspServer, language, workspaceRoot string, workspaceRoots []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()

	caps := buildLSPClientCapabilities()
	// prompt-9 BUG-IDE-11: pass real process id when available.
	pid := os.Getpid()
	initParams := map[string]interface{}{
		"processId":        pid,
		"rootUri":          pathToURI(workspaceRoot),
		"capabilities":     caps,
		"workspaceFolders": []map[string]string{},
	}
	if options := lspInitializationOptions(language, workspaceRoot); options != nil {
		initParams["initializationOptions"] = options
	}
	// Priority 4: 多根模式下使用全部根；单根模式退化到旧逻辑。
	if len(workspaceRoots) > 0 {
		initParams["workspaceFolders"] = rootsToWorkspaceFolders(workspaceRoots)
		// rootUri 在多根场景下应为第一个根（LSP 规范允许两者并存，服务器
		// 会优先采用 workspaceFolders）。
		if workspaceRoot == "" && len(workspaceRoots) > 0 {
			initParams["rootUri"] = pathToURI(workspaceRoots[0])
		}
	} else if workspaceRoot != "" {
		initParams["workspaceFolders"] = []map[string]string{
			{"uri": pathToURI(workspaceRoot), "name": filepath.Base(workspaceRoot)},
		}
	}
	raw, err := srv.client.request(ctx, "initialize", initParams)
	if err != nil {
		return err
	}
	// prompt-13 13-F: negotiate TextDocumentSync Kind
	srv.syncKind = 1 // default Full
	var initRes struct {
		Capabilities struct {
			TextDocumentSync   interface{} `json:"textDocumentSync"`
			CompletionProvider struct {
				TriggerCharacters []string `json:"triggerCharacters"`
			} `json:"completionProvider"`
			CodeActionProvider     json.RawMessage `json:"codeActionProvider"`
			DiagnosticProvider     json.RawMessage `json:"diagnosticProvider"`
			SemanticTokensProvider json.RawMessage `json:"semanticTokensProvider"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(raw, &initRes) == nil {
		srv.process.setTriggerCharacters(initRes.Capabilities.CompletionProvider.TriggerCharacters)
		srv.diagnosticProviderKnown, srv.pullDiagnosticsSupported = parseStaticLSPCapability(initRes.Capabilities.DiagnosticProvider)
		tokenTypes, tokenModifiers := parseSemanticTokenLegend(initRes.Capabilities.SemanticTokensProvider)
		srv.setSemanticTokenLegend(tokenTypes, tokenModifiers)
		capabilityRaw, _ := json.Marshal(map[string]json.RawMessage{
			"codeActionProvider": initRes.Capabilities.CodeActionProvider,
		})
		srv.codeActionSupported, srv.codeActionKinds = parseCodeActionCapability(capabilityRaw)
		switch v := initRes.Capabilities.TextDocumentSync.(type) {
		case float64:
			if int(v) == 2 {
				srv.syncKind = 2
			}
		case map[string]interface{}:
			if ch, ok := v["change"].(float64); ok && int(ch) == 2 {
				srv.syncKind = 2
			}
		}
	}
	if err := srv.client.notify("initialized", map[string]interface{}{}); err != nil {
		return err
	}
	return nil
}

// GetTriggerCharacters returns the server-advertised completion triggers.
// A defensive copy prevents frontend callers from mutating process state.
func (s *LSPService) GetTriggerCharacters(language string) []string {
	language = lspServerKey(language)
	if language == "" {
		return []string{}
	}
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return []string{}
	}
	srv := s.servers[language]
	s.mu.Unlock()
	if srv == nil {
		return []string{}
	}
	return srv.process.getTriggerCharacters()
}

func (s *LSPService) serverForLanguage(language string) (*lspServer, error) {
	language = lspServerKey(language)
	if language == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.switching {
		return nil, errWorkspaceSwitching
	}
	return s.servers[language], nil
}

// StopLSPServer stops a running LSP server for the given language. It is a
// no-op (returns nil) if no server is running.
func (s *LSPService) StopLSPServer(language string) error {
	key := lspServerKey(language)
	if key == "" {
		return nil
	}
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	languageLock := s.languageLifecycleLock(key)
	languageLock.Lock()
	defer languageLock.Unlock()

	s.mu.Lock()
	srv, ok := s.servers[key]
	if !ok || srv == nil {
		s.mu.Unlock()
		return nil
	}
	delete(s.servers, key)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), lspProcessStopTimeout)
	defer cancel()
	return stopLSPServerProcess(ctx, srv, errLSPServerStopping)
}

// StopAll stops every running LSP server. Called on application shutdown.
func (s *LSPService) StopAll() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.Lock()
	// FIX A9: detach the complete map before waiting so queries never queue
	// behind process shutdown and timed-out servers cannot remain registered.
	s.switching = true
	servers := s.servers
	s.servers = make(map[string]*lspServer)
	s.mu.Unlock()
	stopLSPServersConcurrently(servers, errWorkspaceSwitching)
	s.mu.Lock()
	s.switching = false
	s.mu.Unlock()
}
