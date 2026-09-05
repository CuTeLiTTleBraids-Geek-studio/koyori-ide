package services

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	maxMCPServerNameBytes  = 128
	maxMCPArgs             = 256
	maxMCPArgBytes         = 64 * 1024
	maxMCPMapEntries       = 128
	maxMCPHeaderValueBytes = 64 * 1024
	maxMCPEnvValueBytes    = 64 * 1024
)

// validateMCPConfig validates configuration loaded from disk before it is
// installed. Remote URL DNS/SSRF validation is intentionally deferred when
// resolveRemote is false; startup must not grant authority or make network
// calls, and ConnectServer repeats the full validation at execution time.
func validateMCPConfig(config MCPConfig, resolveRemote bool) error {
	seen := make(map[string]struct{}, len(config.Servers))
	for _, server := range config.Servers {
		if _, exists := seen[server.Name]; exists {
			return fmt.Errorf("duplicate MCP server name %q: %w", server.Name, ErrInvalidInput)
		}
		seen[server.Name] = struct{}{}
		if err := validateMCPServerConfig(server, resolveRemote); err != nil {
			return err
		}
	}
	return nil
}

func validateMCPServerConfig(server MCPServerConfig, resolveRemote bool) error {
	if server.Name == "" || len(server.Name) > maxMCPServerNameBytes || strings.TrimSpace(server.Name) != server.Name {
		return fmt.Errorf("MCP server name is invalid: %w", ErrInvalidInput)
	}
	if server.Name == "." || server.Name == ".." {
		return fmt.Errorf("MCP server name %q is reserved: %w", server.Name, ErrInvalidInput)
	}
	for i := 0; i < len(server.Name); i++ {
		c := server.Name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_' {
			continue
		}
		return fmt.Errorf("MCP server name %q contains an unsupported character: %w", server.Name, ErrInvalidInput)
	}
	if !utf8.ValidString(server.Name) {
		return fmt.Errorf("MCP server name is not valid UTF-8: %w", ErrInvalidInput)
	}

	switch server.Transport {
	case "stdio":
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("MCP stdio command is required: %w", ErrInvalidInput)
		}
		if strings.ContainsRune(server.Command, 0) {
			return fmt.Errorf("MCP stdio command contains NUL: %w", ErrInvalidInput)
		}
	case "http", "sse":
		if _, err := validateMCPRemoteURL(server.URL, resolveRemote); err != nil {
			return fmt.Errorf("MCP %s URL is invalid: %w", server.Transport, err)
		}
	default:
		return fmt.Errorf("invalid MCP transport %q: %w", server.Transport, ErrInvalidInput)
	}
	if len(server.Args) > maxMCPArgs {
		return fmt.Errorf("MCP stdio args exceed %d entries: %w", maxMCPArgs, ErrInvalidInput)
	}
	for _, arg := range server.Args {
		if len(arg) > maxMCPArgBytes || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("MCP stdio argument is invalid: %w", ErrInvalidInput)
		}
	}
	if err := validateMCPEnvironment(server.Env); err != nil {
		return err
	}
	if err := validateMCPHeaders(server.Headers); err != nil {
		return err
	}
	return nil
}

func validateMCPRemoteURL(raw string, resolveRemote bool) (string, error) {
	if resolveRemote {
		_, err := ValidateNonPrivateURL(raw)
		return raw, err
	}
	if err := ValidateBaseURL(raw); err != nil {
		return raw, err
	}
	return raw, nil
}

func validateMCPHeaders(headers map[string]string) error {
	if len(headers) > maxMCPMapEntries {
		return fmt.Errorf("MCP headers exceed %d entries: %w", maxMCPMapEntries, ErrInvalidInput)
	}
	for key, value := range headers {
		if !validMCPHeaderName(key) {
			return fmt.Errorf("MCP header name %q is invalid: %w", key, ErrInvalidInput)
		}
		if len(value) > maxMCPHeaderValueBytes || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("MCP header %q value is invalid: %w", key, ErrInvalidInput)
		}
	}
	return nil
}

func validMCPHeaderName(name string) bool {
	if name == "" || len(name) > 256 || !utf8.ValidString(name) {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

// load reads the MCP config from disk. Missing file is not an error.
// G-SEC-07: Headers/Env values are stored encrypted on disk; after
// unmarshal we decrypt them into the in-memory config so MCP clients
// can use the real values when connecting.
func (s *MCPService) load() error {
	data, err := os.ReadFile(s.cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // fresh start — no config yet
		}
		return fmt.Errorf("read mcp config: %w", err)
	}
	var persisted MCPConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("parse mcp config: %w", err)
	}
	if err := validateMCPConfig(persisted, false); err != nil {
		return fmt.Errorf("validate mcp config: %w", err)
	}
	persistedCopy := MCPConfig{Servers: cloneMCPServerConfigs(persisted.Servers)}
	memoryConfig := MCPConfig{Servers: cloneMCPServerConfigs(persisted.Servers)}
	// Decrypt secret-bearing maps into the in-memory (plaintext) config.
	for i := range memoryConfig.Servers {
		decryptServerSecrets(&memoryConfig.Servers[i])
		// Enabled is a live native-consent decision, not durable authority.
		// Never revive a server merely because a previous process persisted the
		// flag; a fresh process must obtain consent again for the exact config.
		memoryConfig.Servers[i].Enabled = false
		persistedCopy.Servers[i].Enabled = false
	}
	if err := validateMCPConfig(memoryConfig, false); err != nil {
		return fmt.Errorf("validate decrypted mcp config: %w", err)
	}
	s.persistedConfig = persistedCopy
	s.config = memoryConfig
	return nil
}

// persistConfigSnapshot persists an exact copy-on-write candidate via
// atomicWriteJSON with 0600 (G-SEC-09) and returns its encrypted form.
// G-SEC-07: Headers/Env values are encrypted before writing so secrets are
// never stored as plaintext on disk. The in-memory config retains
// plaintext for use by running MCP connections.
func (s *MCPService) persistConfigSnapshot(candidate MCPConfig) (MCPConfig, error) {
	s.mu.RLock()
	previousConfig := MCPConfig{Servers: cloneMCPServerConfigs(s.config.Servers)}
	previousPersisted := MCPConfig{Servers: cloneMCPServerConfigs(s.persistedConfig.Servers)}
	encryptSecret := s.encryptSecret
	deleteSecret := s.deleteSecret
	s.mu.RUnlock()
	if encryptSecret == nil {
		encryptSecret = EncryptSecret
	}
	if deleteSecret == nil {
		deleteSecret = DeleteSecret
	}

	previousPlaintext := mcpSecretValues(previousConfig)
	previousStored := mcpSecretValues(previousPersisted)
	servers := cloneMCPServerConfigs(candidate.Servers)
	var rollbackEntries []mcpSecretRollback
	for i := range servers {
		// Native consent is process- and workspace-generation scoped. The disk
		// representation is configuration only and must never revive authority.
		servers[i].Enabled = false
		if err := encryptServerSecretsForDisk(&servers[i], previousPlaintext, previousStored, encryptSecret, &rollbackEntries); err != nil {
			encryptErr := fmt.Errorf("encrypt mcp server %q secrets: %w", servers[i].Name, err)
			if rollbackErr := rollbackMCPSecretWrites(rollbackEntries, encryptSecret, deleteSecret); rollbackErr != nil {
				return MCPConfig{}, fmt.Errorf("%w (keyring rollback failed: %v)", encryptErr, rollbackErr)
			}
			return MCPConfig{}, encryptErr
		}
	}
	enc := MCPConfig{Servers: servers}
	if s.persistConfig != nil {
		if err := s.persistConfig(enc); err != nil {
			persistErr := fmt.Errorf("write mcp config: %w", err)
			if rollbackErr := rollbackMCPSecretWrites(rollbackEntries, encryptSecret, deleteSecret); rollbackErr != nil {
				return MCPConfig{}, fmt.Errorf("%w (keyring rollback failed: %v)", persistErr, rollbackErr)
			}
			return MCPConfig{}, persistErr
		}
		return enc, nil
	}
	if err := atomicWriteJSON(s.cfgPath, enc, 0600); err != nil {
		persistErr := fmt.Errorf("write mcp config: %w", err)
		if rollbackErr := rollbackMCPSecretWrites(rollbackEntries, encryptSecret, deleteSecret); rollbackErr != nil {
			return MCPConfig{}, fmt.Errorf("%w (keyring rollback failed: %v)", persistErr, rollbackErr)
		}
		return MCPConfig{}, persistErr
	}
	return enc, nil
}

func (s *MCPService) reserveConfigWrite() (<-chan struct{}, chan struct{}) {
	s.mu.Lock()
	previous := s.persistTail
	if previous == nil {
		ready := make(chan struct{})
		close(ready)
		previous = ready
	}
	done := make(chan struct{})
	s.persistTail = done
	s.mu.Unlock()
	return previous, done
}

func cloneMCPServerConfigs(servers []MCPServerConfig) []MCPServerConfig {
	out := make([]MCPServerConfig, len(servers))
	for i, server := range servers {
		out[i] = cloneMCPServerConfig(server)
	}
	return out
}

func cloneMCPServerConfig(server MCPServerConfig) MCPServerConfig {
	out := server
	out.Args = append([]string(nil), server.Args...)
	out.Env = cloneMCPStringMap(server.Env)
	out.Headers = cloneMCPStringMap(server.Headers)
	out.executableIdentity = cloneMCPExecutableIdentity(server.executableIdentity)
	return out
}

func cloneMCPStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

const (
	// mcpSecretMask is the placeholder returned to the frontend in place of a
	// real secret value. The UI does not display Headers/Env, so masking is
	// invisible to the user while keeping plaintext out of the JS heap.
	mcpSecretMask = "***"

	mcpSecretAccountPrefix = "mcp:v1:"
	mcpSecretMapHeaders    = "headers"
	mcpSecretMapEnv        = "env"
)

// maskServerSecretsForView returns a copy of cfg with non-empty
// Headers/Env values replaced by mcpSecretMask. Empty values stay empty.
func maskServerSecretsForView(cfg MCPServerConfig) MCPServerConfig {
	out := cfg
	out.Headers = maskSecretMap(cfg.Headers)
	out.Env = maskSecretMap(cfg.Env)
	return out
}

func maskSecretMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	masked := make(map[string]string, len(m))
	for k, v := range m {
		if v == "" {
			masked[k] = ""
		} else {
			masked[k] = mcpSecretMask
		}
	}
	return masked
}

// isMaskedSecret reports whether a value is the frontend mask placeholder.
func isMaskedSecret(v string) bool { return v == mcpSecretMask }

// mergeSecretMap merges incoming secret map onto existing. For each key:
//   - if the incoming value is the mask placeholder or empty AND the
//     existing value is non-empty, preserve the existing decrypted value
//     (the frontend did not change it — it only round-tripped the mask);
//   - otherwise adopt the incoming value (including newly-set plaintext).
func mergeSecretMap(existing, incoming map[string]string) map[string]string {
	if incoming == nil {
		// An omitted map means the caller did not edit this secret collection.
		return existing
	}
	out := make(map[string]string, len(incoming))
	for k, v := range incoming {
		if (v == "" || isMaskedSecret(v)) && existing[k] != "" {
			out[k] = existing[k]
			continue
		}
		out[k] = v
	}
	return out
}

type mcpSecretRollback struct {
	account           string
	previousPlaintext string
	previousExisted   bool
}

// encryptServerSecretsForDisk encrypts non-empty Headers/Env values in place
// for the on-disk copy. Unchanged encrypted values are reused so native
// keyring entries are not rewritten by unrelated config mutations.
func encryptServerSecretsForDisk(
	cfg *MCPServerConfig,
	previousPlaintext, previousStored map[string]string,
	encryptSecret func(string, string) (string, error),
	rollbackEntries *[]mcpSecretRollback,
) error {
	headers, err := encryptSecretMap(cfg.Name, mcpSecretMapHeaders, cfg.Headers, previousPlaintext, previousStored, encryptSecret, rollbackEntries)
	if err != nil {
		return fmt.Errorf("encrypt headers: %w", err)
	}
	env, err := encryptSecretMap(cfg.Name, mcpSecretMapEnv, cfg.Env, previousPlaintext, previousStored, encryptSecret, rollbackEntries)
	if err != nil {
		return fmt.Errorf("encrypt environment: %w", err)
	}
	cfg.Headers = headers
	cfg.Env = env
	return nil
}

func encryptSecretMap(
	serverName, mapKind string,
	m, previousPlaintext, previousStored map[string]string,
	encryptSecret func(string, string) (string, error),
	rollbackEntries *[]mcpSecretRollback,
) (map[string]string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v == "" || isMaskedSecret(v) || IsSecretEncrypted(v) {
			out[k] = v
			continue
		}
		account := mcpSecretAccount(serverName, mapKind, k)
		storedBefore := previousStored[account]
		if v == previousPlaintext[account] && IsSecretEncrypted(storedBefore) {
			out[k] = storedBefore
			continue
		}
		rollback := mcpSecretRollback{account: account}
		if strings.HasPrefix(storedBefore, secretPrefixKeyring) {
			rollback.previousPlaintext = previousPlaintext[account]
			if rollback.previousPlaintext == "" {
				return nil, fmt.Errorf("read existing secret %q before update: plaintext unavailable", k)
			}
			rollback.previousExisted = true
		}
		enc, err := encryptSecret(account, v)
		if err != nil {
			return nil, fmt.Errorf("encrypt secret %q: %w", k, err)
		}
		if strings.HasPrefix(enc, secretPrefixKeyring) {
			*rollbackEntries = append(*rollbackEntries, rollback)
		}
		out[k] = enc
	}
	return out, nil
}

func mcpSecretValues(config MCPConfig) map[string]string {
	values := make(map[string]string)
	for _, server := range config.Servers {
		for key, value := range server.Headers {
			values[mcpSecretAccount(server.Name, mcpSecretMapHeaders, key)] = value
		}
		for key, value := range server.Env {
			values[mcpSecretAccount(server.Name, mcpSecretMapEnv, key)] = value
		}
	}
	return values
}

func rollbackMCPSecretWrites(
	entries []mcpSecretRollback,
	encryptSecret func(string, string) (string, error),
	deleteSecret func(string) error,
) error {
	var rollbackErr error
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.previousExisted {
			stored, err := encryptSecret(entry.account, entry.previousPlaintext)
			if err == nil && !strings.HasPrefix(stored, secretPrefixKeyring) {
				err = fmt.Errorf("platform did not restore native keyring storage")
			}
			rollbackErr = errors.Join(rollbackErr, err)
			continue
		}
		rollbackErr = errors.Join(rollbackErr, deleteSecret(entry.account))
	}
	return rollbackErr
}

// decryptServerSecrets decrypts Headers/Env values in place (used after
// loading from disk to restore plaintext for in-memory use).
func decryptServerSecrets(cfg *MCPServerConfig) {
	var unsafeSecret bool
	cfg.Headers, unsafeSecret = decryptSecretMap(cfg.Name, mcpSecretMapHeaders, cfg.Headers)
	var unsafeEnvSecret bool
	cfg.Env, unsafeEnvSecret = decryptSecretMap(cfg.Name, mcpSecretMapEnv, cfg.Env)
	if unsafeSecret || unsafeEnvSecret {
		cfg.Enabled = false
	}
}

func decryptSecretMap(serverName, mapKind string, m map[string]string) (map[string]string, bool) {
	if len(m) == 0 {
		return nil, false
	}
	out := make(map[string]string, len(m))
	unsafeSecret := false
	for k, v := range m {
		account := mcpSecretAccount(serverName, mapKind, k)
		isEncrypted := IsSecretEncrypted(v)
		isKeyringMarker := strings.HasPrefix(v, secretPrefixKeyring)
		if isKeyringMarker {
			markerAccount, ok := mcpKeyringMarkerAccount(v)
			if !ok || markerAccount != account {
				slog.Warn("mcp: rejected unscoped keyring marker", "server", serverName, "kind", mapKind, "key", k)
				out[k] = ""
				unsafeSecret = true
				continue
			}
		}
		dec, err := DecryptSecret(account, v)
		if err != nil {
			slog.Debug("mcp: decrypt stored secret failed", "key", k, "err", err)
			if isEncrypted {
				out[k] = ""
				unsafeSecret = true
				continue
			}
			out[k] = v // best-effort: keep raw if decrypt fails
			continue
		}
		out[k] = dec
	}
	return out, unsafeSecret
}

func mcpSecretAccount(serverName, mapKind, key string) string {
	sum := sha256.Sum256([]byte(serverName + "\x00" + mapKind + "\x00" + key))
	return mcpSecretAccountPrefix + fmt.Sprintf("%x", sum)
}

func mcpKeyringMarkerAccount(stored string) (string, bool) {
	if !strings.HasPrefix(stored, secretPrefixKeyring) {
		return "", false
	}
	marker := strings.TrimPrefix(stored, secretPrefixKeyring)
	decoded, err := base64.StdEncoding.DecodeString(marker)
	if err != nil || len(decoded) == 0 {
		return "", false
	}
	return string(decoded), true
}

type mcpSecretRef struct {
	serverName string
	mapKind    string
	key        string
}

func removedMCPSecretRefs(oldCfg, newCfg MCPServerConfig) []mcpSecretRef {
	refs := removedMCPSecretMapRefs(oldCfg.Name, mcpSecretMapHeaders, oldCfg.Headers, newCfg.Headers)
	return append(refs, removedMCPSecretMapRefs(oldCfg.Name, mcpSecretMapEnv, oldCfg.Env, newCfg.Env)...)
}

func mcpSecretRefsForServer(cfg MCPServerConfig) []mcpSecretRef {
	refs := removedMCPSecretMapRefs(cfg.Name, mcpSecretMapHeaders, cfg.Headers, nil)
	return append(refs, removedMCPSecretMapRefs(cfg.Name, mcpSecretMapEnv, cfg.Env, nil)...)
}

func removedMCPSecretMapRefs(serverName, mapKind string, oldMap, newMap map[string]string) []mcpSecretRef {
	refs := make([]mcpSecretRef, 0)
	for key, value := range oldMap {
		if value == "" {
			continue
		}
		if _, stillPresent := newMap[key]; stillPresent {
			continue
		}
		refs = append(refs, mcpSecretRef{serverName: serverName, mapKind: mapKind, key: key})
	}
	return refs
}

func (s *MCPService) cleanupMCPSecrets(refs []mcpSecretRef) {
	if len(refs) == 0 {
		return
	}
	deleteSecret := s.deleteSecret
	if deleteSecret == nil {
		deleteSecret = DeleteSecret
	}
	for _, ref := range refs {
		// The existence check uses only a short state lock. DeleteSecret performs
		// keyring I/O after that lock has been released.
		if s.mcpSecretFieldExists(ref) {
			continue
		}
		if err := deleteSecret(mcpSecretAccount(ref.serverName, ref.mapKind, ref.key)); err != nil {
			slog.Warn("mcp: delete orphaned keyring secret failed", "server", ref.serverName, "kind", ref.mapKind, "key", ref.key, "err", err)
		}
	}
}

func (s *MCPService) mcpSecretFieldExists(ref mcpSecretRef) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, server := range s.config.Servers {
		if server.Name != ref.serverName {
			continue
		}
		var secrets map[string]string
		switch ref.mapKind {
		case mcpSecretMapHeaders:
			secrets = server.Headers
		case mcpSecretMapEnv:
			secrets = server.Env
		default:
			return false
		}
		_, exists := secrets[ref.key]
		return exists
	}
	return false
}

// ListServers returns all configured MCP servers. G-SEC-07: Headers/Env
// secret values are masked so plaintext never crosses the Wails binding.
func (s *MCPService) ListServers() []MCPServerConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MCPServerConfig, len(s.config.Servers))
	for i, srv := range s.config.Servers {
		out[i] = maskServerSecretsForView(srv)
	}
	return out
}

// GetServer returns a single server config by name. G-SEC-07: Headers/Env
// secret values are masked in the returned copy.
func (s *MCPService) GetServer(name string) (MCPServerConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, srv := range s.config.Servers {
		if srv.Name == name {
			return maskServerSecretsForView(srv), nil
		}
	}
	return MCPServerConfig{}, fmt.Errorf("mcp server %q: %w", name, ErrNotFound)
}

// SaveServer adds or updates a server config. Names must be unique.
// G-SEC-12: new servers default to Enabled=false (Restricted).
func (s *MCPService) SaveServer(cfg MCPServerConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("server name required: %w", ErrInvalidInput)
	}
	if cfg.Transport != "stdio" && cfg.Transport != "sse" && cfg.Transport != "http" {
		return fmt.Errorf("invalid transport %q: %w", cfg.Transport, ErrInvalidInput)
	}
	if err := validateMCPServerConfig(cfg, true); err != nil {
		return err
	}
	// C-1: validate http/sse URLs before acquiring the mutex — URL validation
	// may perform DNS resolution and must not block other callers. This also
	// defangs the SSRF vector (http://169.254.169.254/...) and prevents the
	// HTTP transport from later carrying an Authorization header to an
	// internal endpoint.
	previous, done := s.reserveConfigWrite()
	release := func() {
		if done != nil {
			close(done)
			done = nil
		}
	}
	defer release()
	<-previous
	s.transportLifecycleMu.Lock()
	lifecycleLocked := true
	unlockLifecycle := func() {
		if lifecycleLocked {
			lifecycleLocked = false
			s.transportLifecycleMu.Unlock()
		}
	}
	defer unlockLifecycle()

	s.mu.RLock()
	root := s.rootDir
	servers := cloneMCPServerConfigs(s.config.Servers)
	s.mu.RUnlock()
	// Resolve and retain the exact executable path whenever a workspace root
	// is available. ConnectServer repeats this check at the execution boundary
	// for legacy configs created before canonicalization.
	if cfg.Transport == "stdio" && root != "" && cfg.Command != "" {
		canonical, workDir, err := resolveMCPStdioCommand(root, cfg.Command)
		if err != nil {
			return fmt.Errorf("stdio command path outside workspace: %w", err)
		}
		cfg.Command = canonical
		cfg.workDir = workDir
	}
	// Upsert.
	found := false
	connectionChanged := false
	var cleanupRefs []mcpSecretRef
	for i, srv := range servers {
		if srv.Name == cfg.Name {
			// Enabled is changed only through SetServerEnabled so false is never
			// confused with an omitted patch field.
			cfg.Enabled = srv.Enabled
			// G-SEC-07: ListServers masks Headers/Env. When the frontend
			// round-trips a masked/empty value back through SaveServer,
			// preserve the existing decrypted secret rather than overwriting
			// it with the mask placeholder.
			cfg.Headers = mergeSecretMap(srv.Headers, cfg.Headers)
			cfg.Env = mergeSecretMap(srv.Env, cfg.Env)
			connectionChanged = !mcpConnectionConfigEqual(srv, cfg, root)
			if !connectionChanged && srv.Enabled && srv.executableIdentity != nil {
				currentIdentity, identityErr := captureMCPExecutableIdentity(root, cfg.Command)
				connectionChanged = identityErr != nil || !sameMCPExecutableIdentity(srv.executableIdentity, currentIdentity)
			}
			if connectionChanged {
				// Enabled is approval for one exact endpoint identity. Changing
				// command/args/env/URL/headers revokes it and requires a new
				// explicit activation before ConnectServer can run the replacement.
				cfg.Enabled = false
				cfg.executableIdentity = nil
			} else {
				cfg.executableIdentity = cloneMCPExecutableIdentity(srv.executableIdentity)
			}
			cleanupRefs = removedMCPSecretRefs(srv, cfg)
			servers[i] = cfg
			found = true
			break
		}
	}
	if !found {
		// G-SEC-12: new servers start disabled.
		cfg.Enabled = false
		servers = append(servers, cfg)
	}
	candidate := MCPConfig{Servers: servers}
	persisted, err := s.persistConfigSnapshot(candidate)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.config = candidate
	s.persistedConfig = persisted
	s.lifecycleGeneration++
	var detachedClient *MCPClient
	if found && connectionChanged {
		// A connected client is bound to the old endpoint/config. Detach it
		// atomically with the committed config so no approval/catalog can use
		// the old identity under the new name.
		detachedClient = s.clients[cfg.Name]
		delete(s.clients, cfg.Name)
	}
	s.mu.Unlock()
	s.cleanupMCPSecrets(cleanupRefs)
	var stopErr error
	if detachedClient != nil {
		if err := detachedClient.StopServer(); err != nil {
			stopErr = fmt.Errorf("update mcp server %q: %w", cfg.Name, err)
			s.recordTransportLifecycleErrorLocked(stopErr)
		}
	}
	release()
	unlockLifecycle()
	s.notifyToolsChanged()
	return stopErr
}

// SetServerEnabled applies the explicit Restricted-capability decision.
// Disabling also disconnects the running client before returning.
func (s *MCPService) SetServerEnabled(name string, enabled bool) error {
	previous, done := s.reserveConfigWrite()
	release := func() {
		if done != nil {
			close(done)
			done = nil
		}
	}
	defer release()
	<-previous
	s.transportLifecycleMu.Lock()

	s.mu.RLock()
	servers := cloneMCPServerConfigs(s.config.Servers)
	s.mu.RUnlock()
	idx := -1
	for i := range servers {
		if servers[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.transportLifecycleMu.Unlock()
		return fmt.Errorf("mcp server %q: %w", name, ErrNotFound)
	}
	if enabled {
		if err := validateMCPServerConfig(servers[idx], true); err != nil {
			s.transportLifecycleMu.Unlock()
			return fmt.Errorf("enable MCP server %q configuration rejected: %w", name, err)
		}
	}
	// Enabling a server is a native capability decision, not a renderer
	// supplied boolean.  Keep the approval inside the serialized mutation
	// window so a later SaveServer cannot change the endpoint that was shown to
	// the user before the approval is committed.
	if enabled && !servers[idx].Enabled {
		if servers[idx].Transport == "stdio" {
			identity, identityErr := captureMCPExecutableIdentity(s.rootDir, servers[idx].Command)
			if identityErr != nil {
				s.transportLifecycleMu.Unlock()
				return fmt.Errorf("bind MCP executable for native approval: %w", identityErr)
			}
			servers[idx].Command = identity.path
			servers[idx].workDir = identity.workDir
			servers[idx].executableIdentity = identity
		}
		s.mu.RLock()
		approve := s.approveServer
		s.mu.RUnlock()
		approved := false
		if approve != nil {
			approved = approve(maskServerSecretsForView(cloneMCPServerConfig(servers[idx])))
		}
		if !approved {
			s.transportLifecycleMu.Unlock()
			return fmt.Errorf("enable MCP server %q was not approved by the native consent boundary: %w", name, ErrNotAllowed)
		}
		if servers[idx].executableIdentity != nil {
			if err := verifyMCPExecutableIdentity(servers[idx].executableIdentity); err != nil {
				s.transportLifecycleMu.Unlock()
				return fmt.Errorf("MCP executable changed during native approval: %w", err)
			}
		}
	}
	servers[idx].Enabled = enabled
	if !enabled {
		servers[idx].executableIdentity = nil
	}
	candidate := MCPConfig{Servers: servers}
	persisted, err := s.persistConfigSnapshot(candidate)
	if err != nil {
		s.transportLifecycleMu.Unlock()
		return err
	}
	s.mu.Lock()
	s.config = candidate
	s.persistedConfig = persisted
	s.lifecycleGeneration++
	var client *MCPClient
	if !enabled {
		client = s.clients[name]
		delete(s.clients, name)
	}
	s.mu.Unlock()
	var stopErr error
	if client != nil {
		stopErr = client.StopServer()
		if stopErr != nil {
			stopErr = fmt.Errorf("disable mcp server %q: %w", name, stopErr)
			s.recordTransportLifecycleErrorLocked(stopErr)
		}
	}
	// Keep the persistence slot held until the detached transport has fully
	// stopped. A concurrent SaveServer must not observe a teardown in flight.
	release()
	s.transportLifecycleMu.Unlock()
	s.notifyToolsChanged()
	return stopErr
}

// DeleteServer removes a server config and stops its client if running.
func (s *MCPService) DeleteServer(name string) error {
	previous, done := s.reserveConfigWrite()
	release := func() {
		if done != nil {
			close(done)
			done = nil
		}
	}
	defer release()
	<-previous
	s.transportLifecycleMu.Lock()

	s.mu.RLock()
	servers := cloneMCPServerConfigs(s.config.Servers)
	s.mu.RUnlock()
	idx := -1
	for i, srv := range servers {
		if srv.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.transportLifecycleMu.Unlock()
		return fmt.Errorf("mcp server %q: %w", name, ErrNotFound)
	}
	cleanupRefs := mcpSecretRefsForServer(servers[idx])
	servers = append(servers[:idx], servers[idx+1:]...)
	candidate := MCPConfig{Servers: servers}
	persisted, err := s.persistConfigSnapshot(candidate)
	if err != nil {
		s.transportLifecycleMu.Unlock()
		return err
	}
	s.mu.Lock()
	// Detach the client while holding only the state lock. FIX B1: stopping a
	// transport may block on process/network shutdown and must happen outside
	// s.mu so unrelated service operations remain available.
	client := s.clients[name]
	if client != nil {
		delete(s.clients, name)
	}
	s.config = candidate
	s.persistedConfig = persisted
	s.lifecycleGeneration++
	s.mu.Unlock()
	s.cleanupMCPSecrets(cleanupRefs)
	var stopErr error
	if client != nil {
		stopErr = client.StopServer()
		if stopErr != nil {
			stopErr = fmt.Errorf("delete mcp server %q: %w", name, stopErr)
			s.recordTransportLifecycleErrorLocked(stopErr)
		}
		slog.Info("mcp transport", "server", name, "event", "disconnected")
	}
	// Keep the persistence slot held until the detached transport has fully
	// stopped. A concurrent SaveServer must not observe a teardown in flight.
	release()
	s.transportLifecycleMu.Unlock()
	s.notifyToolsChanged()
	return stopErr
}
