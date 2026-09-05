package services

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

const maxMCPExecutableBytes int64 = 1 << 30

// mcpExecutableIdentity is backend-only approval state. It binds native
// consent to the exact workspace root and executable file that was reviewed.
// The value is intentionally absent from JSON and Wails models.
type mcpExecutableIdentity struct {
	path         string
	workDir      string
	rootPath     string
	rootIdentity os.FileInfo
	fileIdentity os.FileInfo
	size         int64
	digest       [sha256.Size]byte
}

func cloneMCPExecutableIdentity(identity *mcpExecutableIdentity) *mcpExecutableIdentity {
	if identity == nil {
		return nil
	}
	copy := *identity
	return &copy
}

func sameMCPExecutableIdentity(expected, actual *mcpExecutableIdentity) bool {
	if expected == nil || actual == nil || expected.size != actual.size || expected.digest != actual.digest ||
		!sameWorkspaceIdentityPath(expected.path, actual.path) || !sameWorkspaceIdentityPath(expected.workDir, actual.workDir) ||
		expected.fileIdentity == nil || actual.fileIdentity == nil || !os.SameFile(expected.fileIdentity, actual.fileIdentity) {
		return false
	}
	if expected.rootIdentity == nil || actual.rootIdentity == nil {
		return expected.rootIdentity == nil && actual.rootIdentity == nil
	}
	return sameWorkspaceIdentityPath(expected.rootPath, actual.rootPath) &&
		os.SameFile(expected.rootIdentity, actual.rootIdentity)
}

func verifyMCPExecutableIdentity(expected *mcpExecutableIdentity) error {
	if expected == nil {
		return fmt.Errorf("MCP executable approval identity is missing: %w", ErrNotAllowed)
	}
	actual, err := captureMCPExecutableIdentity(expected.rootPath, expected.path)
	if err != nil {
		return err
	}
	if !sameMCPExecutableIdentity(expected, actual) {
		return fmt.Errorf("MCP executable changed after native approval: %w", ErrNotAllowed)
	}
	return nil
}

func captureMCPExecutableIdentity(root, raw string) (*mcpExecutableIdentity, error) {
	path, workDir, err := resolveMCPStdioCommand(root, raw)
	if err != nil {
		return nil, err
	}
	identity := &mcpExecutableIdentity{path: path, workDir: workDir, rootPath: root}
	if root != "" {
		identity.rootPath = filepath.Clean(root)
	}

	var file *os.File
	var namedInfo os.FileInfo
	if root != "" {
		rootHandle, openErr := os.OpenRoot(root)
		if openErr != nil {
			return nil, fmt.Errorf("open MCP workspace root: %w", openErr)
		}
		defer rootHandle.Close()
		boundRoot, statErr := rootHandle.Stat(".")
		namedRoot, namedRootErr := os.Stat(root)
		if statErr != nil || namedRootErr != nil || !os.SameFile(boundRoot, namedRoot) {
			return nil, fmt.Errorf("MCP workspace root identity changed: %w", ErrNotAllowed)
		}
		identity.rootIdentity = boundRoot
		// P19 CI 修复：root 与 path 须在同一解析形态下做 Rel。path 在
		// resolveMCPStdioCommand 已经过 EvalSymlinks（Windows 8.3 短名 /
		// 符号链接前缀展开），root 仍是调用方原始拼写，纯词法 Rel 会把
		// 同一目录误判为越界。
		rootResolved := root
		if resolvedRoot, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
			rootResolved = filepath.Clean(resolvedRoot)
		}
		relative, relErr := filepath.Rel(rootResolved, path)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("MCP executable left workspace root: %w", ErrNotAllowed)
		}
		file, err = rootHandle.Open(filepath.ToSlash(relative))
		if err == nil {
			namedInfo, err = rootHandle.Stat(filepath.ToSlash(relative))
		}
	} else {
		file, err = os.Open(path)
		if err == nil {
			namedInfo, err = os.Stat(path)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("open MCP executable identity: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || namedInfo == nil || !openedInfo.Mode().IsRegular() || !os.SameFile(openedInfo, namedInfo) {
		return nil, fmt.Errorf("MCP executable identity is not stable: %w", ErrNotAllowed)
	}
	if openedInfo.Size() < 0 || openedInfo.Size() > maxMCPExecutableBytes {
		return nil, fmt.Errorf("MCP executable exceeds %d byte limit: %w", maxMCPExecutableBytes, ErrNotAllowed)
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, maxMCPExecutableBytes+1))
	if err != nil {
		return nil, fmt.Errorf("hash MCP executable: %w", err)
	}
	if written != openedInfo.Size() || written > maxMCPExecutableBytes {
		return nil, fmt.Errorf("MCP executable changed while hashing: %w", ErrNotAllowed)
	}
	afterInfo, statErr := file.Stat()
	closeErr := file.Close()
	file = nil
	if statErr != nil || closeErr != nil || !os.SameFile(openedInfo, afterInfo) || openedInfo.Size() != afterInfo.Size() {
		return nil, errors.Join(fmt.Errorf("MCP executable changed while hashing: %w", ErrNotAllowed), statErr, closeErr)
	}
	identity.fileIdentity = openedInfo
	identity.size = written
	copy(identity.digest[:], hasher.Sum(nil))
	return identity, nil
}

func validateMCPEnvironment(overrides map[string]string) error {
	if len(overrides) > maxMCPMapEntries {
		return fmt.Errorf("MCP environment exceeds %d entries: %w", maxMCPMapEntries, ErrInvalidInput)
	}
	for key := range overrides {
		if mcpEnvironmentKeyForbidden(key) {
			return fmt.Errorf("MCP environment variable %q can alter executable resolution: %w", key, ErrNotAllowed)
		}
		value := overrides[key]
		if key == "" || len(key) > 256 || strings.ContainsAny(key, "=\x00\r\n") || !utf8.ValidString(key) {
			return fmt.Errorf("invalid MCP environment variable name: %w", ErrInvalidInput)
		}
		if len(value) > maxMCPEnvValueBytes || strings.ContainsAny(value, "\x00\r\n") || !utf8.ValidString(value) {
			return fmt.Errorf("MCP environment variable %q value is invalid: %w", key, ErrInvalidInput)
		}
	}
	return nil
}

// resolveMCPStdioCommand turns the configured command into the exact
// executable path that will be launched. A workspace-bound command must stay
// inside that workspace; an unscoped command is resolved through LookPath at
// the execution boundary so a bare name cannot change meaning with a later
// working-directory/PATH lookup.
func resolveMCPStdioCommand(root, raw string) (path, workDir string, err error) {
	if strings.TrimSpace(raw) == "" {
		return "", "", fmt.Errorf("stdio command is empty: %w", ErrInvalidInput)
	}
	if root != "" {
		target := raw
		normalized := strings.ReplaceAll(raw, "\\", "/")
		isWindowsAbsolute := len(normalized) >= 3 && isASCIIAlpha(normalized[0]) && normalized[1] == ':' && normalized[2] == '/'
		isUNC := strings.HasPrefix(normalized, "//")
		if !filepath.IsAbs(raw) && !isWindowsAbsolute && !isUNC {
			target = filepath.Join(root, raw)
		}
		canonical, validateErr := ValidatePathWithinRoot(root, target)
		if validateErr != nil {
			return "", "", validateErr
		}
		resolved, symlinkErr := filepath.EvalSymlinks(canonical)
		if symlinkErr != nil {
			return "", "", fmt.Errorf("resolve stdio executable %q: %w", raw, symlinkErr)
		}
		if _, validateErr = ValidatePathWithinRoot(root, resolved); validateErr != nil {
			return "", "", validateErr
		}
		info, statErr := os.Stat(resolved)
		if statErr != nil {
			return "", "", fmt.Errorf("stat stdio executable %q: %w", raw, statErr)
		}
		if !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("stdio command is not a regular file: %w", ErrInvalidInput)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			return "", "", fmt.Errorf("stdio command is not executable: %w", ErrNotAllowed)
		}
		return filepath.Clean(resolved), filepath.Clean(root), nil
	}

	// Do not allow device namespaces to reach exec even on Windows.
	normalized := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(normalized, "//?/") || strings.HasPrefix(normalized, "//./") {
		return "", "", fmt.Errorf("stdio command uses a device namespace: %w", ErrNotAllowed)
	}
	resolved, lookErr := exec.LookPath(raw)
	if lookErr != nil {
		return "", "", fmt.Errorf("resolve stdio command %q: %w", raw, lookErr)
	}
	absolute, absErr := filepath.Abs(resolved)
	if absErr != nil {
		return "", "", fmt.Errorf("resolve stdio command %q: %w", raw, absErr)
	}
	return filepath.Clean(absolute), "", nil
}

func mcpConnectionConfigEqual(a, b MCPServerConfig, root string) bool {
	if a.Transport != b.Transport ||
		!equalStringSlices(a.Args, b.Args) ||
		!equalStringMaps(a.Env, b.Env) ||
		a.URL != b.URL ||
		!equalStringMaps(a.Headers, b.Headers) {
		return false
	}
	if a.Transport != "stdio" {
		return true
	}
	left, _, leftErr := resolveMCPStdioCommand(root, a.Command)
	right, _, rightErr := resolveMCPStdioCommand(root, b.Command)
	if leftErr != nil || rightErr != nil {
		return a.Command == b.Command
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// mcpChildEnvironment starts from a small, non-secret parent environment and
// then applies values explicitly supplied in the server configuration. The
// latter are user intent and are persisted through the existing encrypted
// config path; ambient parent variables are not.
func mcpChildEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && mcpInheritedEnvAllowed(key) {
			values[key] = value
		}
	}
	for key, value := range overrides {
		if key == "" || mcpEnvironmentKeyForbidden(key) || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			continue
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func mcpInheritedEnvAllowed(key string) bool {
	if strings.HasPrefix(key, "LC_") {
		return true
	}
	switch strings.ToUpper(key) {
	case "PATH", "PATHEXT", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR",
		"HOME", "USERPROFILE", "TMP", "TEMP", "TMPDIR", "LANG", "LANGUAGE",
		"LOGNAME", "USER", "USERNAME", "TERM", "COLORTERM",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME":
		return true
	default:
		return false
	}
}

func mcpEnvironmentKeyForbidden(key string) bool {
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "DYLD_") {
		return true
	}
	switch upper {
	case "PATH", "PATHEXT", "COMSPEC", "SHELL", "LD_PRELOAD", "LD_LIBRARY_PATH",
		"NODE_OPTIONS", "PYTHONPATH", "RUBYOPT", "PERL5LIB", "GIT_EXEC_PATH", "GIT_SSH_COMMAND":
		return true
	default:
		return false
	}
}
