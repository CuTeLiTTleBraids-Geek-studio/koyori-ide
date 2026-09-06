package services

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/adrg/xdg"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

// Settings holds all persisted application settings.
//
// The AIApiKey field is special (N-13): on disk it is stored encrypted with a
// "dpapi:" (Windows) or "aes:" (other platforms) prefix. SaveSettings always
// re-encrypts before writing. Legacy plaintext keys (no prefix) are
// auto-migrated to encrypted form on the first LoadSettings.
//
// G-SEC-07: LoadSettings no longer returns the decrypted key in AIApiKey —
// that field is cleared ("") in the returned struct so the plaintext key is
// never sent to the frontend via the Wails binding (where it would live in
// the JS heap and be vulnerable to XSS). Instead, AIApiKeyConfigured signals
// whether a key is stored, and AIApiKeyStorageMethod labels how it is stored
// ("dpapi"/"aes"/"keyring"/"plain"/"none"). The decrypted key remains
// available to the backend via getDecryptedAPIKey.
//
// CustomShortcuts (N-8) maps a shortcut label (e.g. "Save File") to a
// user-defined key combination that overrides the default binding. The map
// may be nil when no customizations have been made.
type Settings struct {
	// SchemaVersion identifies the persisted JSON shape independently from
	// Version, which remains the dual-window CAS counter.
	SchemaVersion int `json:"schemaVersion"`
	// Version is a monotonic counter bumped on every successful SaveSettings
	// (prompt-7 Task F / BUG-M14). Clients send ExpectedVersion for CAS.
	Version int64 `json:"version"`
	// ExpectedVersion is write-intent only (not stored). When non-nil and the
	// file already has a version, Save fails if disk.Version != *ExpectedVersion.
	ExpectedVersion *int64 `json:"expectedVersion,omitempty"`
	Language        string `json:"language"`
	Theme           string `json:"theme"`
	FontSize        int    `json:"fontSize"`
	FontFamily      string `json:"fontFamily"`
	TabSize         int    `json:"tabSize"`
	WordWrap        bool   `json:"wordWrap"`
	LineNumbers     bool   `json:"lineNumbers"`
	Minimap         bool   `json:"minimap"`
	AIApiKey        string `json:"aiApiKey"`
	AIBaseURL       string `json:"aiBaseUrl"`
	AIModel         string `json:"aiModel"`
	AISystemPrompt  string `json:"aiSystemPrompt"`
	// G-SEC-07: AIApiKeyConfigured is true when a (decryptable) key is stored
	// on disk. It is recomputed by LoadSettings and is the frontend's signal
	// that a key exists without exposing the plaintext. AIApiKeyStorageMethod
	// labels the on-disk storage method; "none" means no key.
	AIApiKeyConfigured    bool   `json:"aiApiKeyConfigured"`
	AIApiKeyStorageMethod string `json:"aiApiKeyStorageMethod"`
	// Plan 54: optional overrides for the other three built-in prompts.
	// When non-empty, the AIService returns these instead of the built-in
	// const. Empty string means "use the built-in".
	AIAgentSystemPrompt       string  `json:"aiAgentSystemPrompt,omitempty"`
	AIConversationTitlePrompt string  `json:"aiConversationTitlePrompt,omitempty"`
	AIInlineCompletionPrompt  string  `json:"aiInlineCompletionPrompt,omitempty"`
	CursorBlinking            string  `json:"cursorBlinking"`
	CursorStyle               string  `json:"cursorStyle"`
	BracketColorization       bool    `json:"bracketColorization"`
	AutoSave                  bool    `json:"autoSave"`
	AutoSaveDelay             string  `json:"autoSaveDelay"`
	AIProvider                string  `json:"aiProvider"`
	Temperature               float64 `json:"temperature"`
	MaxTokens                 int     `json:"maxTokens"`
	DefaultShell              string  `json:"defaultShell"`
	TerminalFontSize          int     `json:"terminalFontSize"`
	TerminalCursorStyle       string  `json:"terminalCursorStyle"`
	Scrollback                int     `json:"scrollback"`
	UIDensity                 string  `json:"uiDensity"`
	FontSizeScaling           int     `json:"fontSizeScaling"`
	InlineCompletionEnabled   bool    `json:"inlineCompletionEnabled"`
	// prompt-9 Task 9-A: format buffer via LSP before save (default true).
	FormatOnSave bool `json:"formatOnSave"`
	// G-EDIT-01/02/03: editor hygiene settings (match VSCode defaults).
	TrimTrailingWhitespace bool `json:"trimTrailingWhitespace"`
	InsertSpaces           bool `json:"insertSpaces"`
	InsertFinalNewline     bool `json:"insertFinalNewline"`
	// G-BLAME-01: inline git blame decoration (default off).
	GitBlameEnabled       bool                    `json:"gitBlameEnabled"`
	EmmetEnabled          bool                    `json:"emmetEnabled"`
	EmmetIncludeLanguages map[string]string       `json:"emmetIncludeLanguages,omitempty"`
	CustomShortcuts       map[string]ShortcutKeys `json:"customShortcuts,omitempty"`
	// N-20: layout state. AiChatPosition omitempty is safe (empty defaults to
	// "right" on the frontend). ActivityBarVisible must NOT use omitempty —
	// otherwise "false" (the zero value) would be dropped and reload as "true".
	AiChatPosition     string `json:"aiChatPosition,omitempty"`
	ActivityBarVisible bool   `json:"activityBarVisible"`
	// P16 P1-01: one session-wide approval intent. Backend safety checks remain authoritative.
	AgentPermissionMode string `json:"agentPermissionMode"`
	// Plan 48: accent theme key. Can be a built-in ("blue", "teal", ...)
	// or "custom". Empty defaults to "blue" on the frontend.
	AccentTheme string `json:"accentTheme,omitempty"`
	// Plan 48: custom accent theme definition. Only set when AccentTheme
	// === "custom". Pointer so nil is distinct from a zero-value struct.
	CustomAccent *CustomAccentTheme `json:"customAccent,omitempty"`
	// N-29: plugin sandbox mode. When true, plugins run in isolated Web
	// Workers with no DOM access. Defaults to true (v2 behavior). Users
	// can disable it for compatibility with v1 main-thread plugins.
	// omitempty is NOT used — false must round-trip correctly.
	EnablePluginSandbox bool `json:"enablePluginSandbox"`
	// Multi-provider AI configs (CC Switch-style). AIProviderConfigs holds
	// an unordered list of named configurations (each with its own provider
	// / apiKey / baseUrl / model / temperature / maxTokens / systemPrompt).
	// ActiveAIConfigID points at the currently active config's ID. The
	// legacy single-config fields (AIApiKey/AIBaseURL/AIModel/AIProvider/
	// Temperature/MaxTokens/AISystemPrompt) are kept as a mirror of the
	// active config so existing AI call paths work unchanged; switching
	// the active config syncs these fields.
	AIProviderConfigs []AIProviderConfig `json:"aiProviderConfigs,omitempty"`
	ActiveAIConfigID  string             `json:"activeAIConfigId,omitempty"`
	// G-FEAT-03: optional overrides for toolchain binary paths. Keys are
	// tool names (e.g. "golangci-lint", "eslint"), values are absolute or
	// PATH-resolved executables. The ToolchainService checks this map first,
	// then falls back to PATH. omitempty is safe — an empty map is equivalent
	// to all-default (PATH lookup).
	ToolPaths map[string]string `json:"toolPaths,omitempty"`
	// Plan 11 Task 15: personalization (code area + chat background images,
	// avatars, fonts, bubble styles). omitempty safe — zero value = defaults.
	Personalization *PersonalizationConfig `json:"personalization,omitempty"`
	// prompt-5 Task C / BUG-L6: whether to open the AI companion OS window
	// automatically on app startup. Default false — users open it on demand.
	// Must NOT use omitempty so false round-trips correctly.
	OpenAIWindowOnStartup bool `json:"openAIWindowOnStartup"`
	// AI companion-window-only presentation preferences. These are separate
	// from the main editor theme and layout settings.
	AIWindowTheme   string `json:"aiWindowTheme"`
	AISidebarWidth  int    `json:"aiSidebarWidth"`
	AITerminalWidth int    `json:"aiTerminalWidth"`
	// F-2 (prompt-2.md): per-section LSP 配置，用于响应 workspace/configuration
	// 请求。key 为 section 名（如 "gopls"、"typescript"），value 为配置对象。
	// 前端 settings UI 可编辑此字段；main.go 在 LoadSettings 后注入到 LSPService。
	LSPConfigs map[string]interface{} `json:"lspConfigs,omitempty"`
}

// PersonalizationConfig holds user personalization settings (Task 15 Step 1).
// Image fields store relative paths under <configDir>/koyori-ide/assets/
// (Step 2: images are copied there, not stored as base64).
type PersonalizationConfig struct {
	CodeEditorBgImage   string            `json:"codeEditorBgImage,omitempty"`   // assets/<name>
	CodeEditorBgOpacity float64           `json:"codeEditorBgOpacity,omitempty"` // 0-1
	CodeEditorBgBlur    float64           `json:"codeEditorBgBlur,omitempty"`    // px
	ChatBgImage         string            `json:"chatBgImage,omitempty"`
	ChatBgOpacity       float64           `json:"chatBgOpacity,omitempty"`
	ChatBgBlur          float64           `json:"chatBgBlur,omitempty"`
	UserAvatar          string            `json:"userAvatar,omitempty"`
	AiAvatar            string            `json:"aiAvatar,omitempty"`
	PersonaAvatars      map[string]string `json:"personaAvatars,omitempty"`
	FontFamily          string            `json:"fontFamily,omitempty"`
	FontSize            int               `json:"fontSize,omitempty"`
	BubbleStyle         string            `json:"bubbleStyle,omitempty"` // rounded/sharp/bubble
	BubbleOpacity       float64           `json:"bubbleOpacity,omitempty"`
	MessageSpacing      int               `json:"messageSpacing,omitempty"`
}

// AIProviderConfig is a single named AI provider configuration. Users can
// save any number of these and switch between them from the chat panel or
// settings page (similar to CC Switch). The Protocol field controls which
// HTTP API shape the backend uses: "openai" (default, /v1/chat/completions
// + Bearer) or "anthropic" (/v1/messages + x-api-key + anthropic-version).
type AIProviderConfig struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Provider        string  `json:"provider"`
	Protocol        string  `json:"protocol,omitempty"` // "openai" | "anthropic", default "openai"
	APIKey          string  `json:"apiKey"`
	BaseURL         string  `json:"baseUrl"`
	Model           string  `json:"model"`
	Temperature     float64 `json:"temperature,omitempty"`
	ReasoningEffort string  `json:"reasoningEffort,omitempty"`
	MaxTokens       int     `json:"maxTokens,omitempty"`
	SystemPrompt    string  `json:"systemPrompt,omitempty"`
	// G-SEC-07: signals whether a key is stored on disk for this config.
	// Recomputed by LoadSettings (true when the on-disk APIKey is non-empty).
	// The frontend reads this to show "key configured" status without ever
	// holding the plaintext. SaveSettings preserves the on-disk key when
	// APIKey is empty but APIKeyConfigured is true.
	APIKeyConfigured bool `json:"apiKeyConfigured,omitempty"`
}

// CustomAccentTheme is a user-defined accent theme (Plan 48). The base Color
// is used to derive accent CSS tokens and register a Monaco theme. Optional
// token overrides take precedence over derived values.
type CustomAccentTheme struct {
	Name               string `json:"name"`
	Color              string `json:"color"`
	Primary            string `json:"primary,omitempty"`
	PrimaryHover       string `json:"primaryHover,omitempty"`
	PrimaryLight       string `json:"primaryLight,omitempty"`
	PrimaryContainer   string `json:"primaryContainer,omitempty"`
	OnPrimary          string `json:"onPrimary,omitempty"`
	OnPrimaryContainer string `json:"onPrimaryContainer,omitempty"`
}

// ShortcutKeys is a persisted key combination for a custom shortcut (N-8).
type ShortcutKeys struct {
	Key   string `json:"key"`
	Ctrl  bool   `json:"ctrl"`
	Shift bool   `json:"shift"`
	Alt   bool   `json:"alt"`
}

// SettingsService loads and saves settings as JSON in the config directory.
//
// Profile-aware (Plan 50): the configPath points at the active profile's
// settings.json. ProfileService.SetActiveProfile calls SetConfigPath to
// redirect this service to the new profile's settings file.
//
// N-76: pathMu protects configPath from concurrent access. Without it, a
// profile switch (SetConfigPath) racing with an in-flight SaveSettings
// could write the old profile's settings to the new profile's path,
// corrupting the new profile. All public methods that read or write
// configPath hold pathMu for the duration of the operation so the path
// cannot change mid-operation.
type SettingsService struct {
	configPath string
	pathMu     sync.RWMutex
	// readFile defaults to os.ReadFile and provides a deterministic I/O seam.
	readFile func(string) ([]byte, error)
	// writeJSON defaults to atomicWriteJSON and is used by the encrypted
	// extension-secret store for deterministic persistence-failure tests.
	writeJSON func(string, interface{}, os.FileMode) error
}

// extensionSecretsSettingsKey is deliberately not represented on Settings.
// Encrypted values remain on disk and are available only to trusted Go
// callers. Renderer-side permission checks are not an authorization boundary.
const extensionSecretsSettingsKey = "extensionSecrets"

const currentSettingsSchemaVersion = 1

func (s *SettingsService) readConfigFile() ([]byte, error) {
	if s.readFile != nil {
		return s.readFile(s.configPath)
	}
	return os.ReadFile(s.configPath)
}

// NewSettingsService creates a SettingsService using the XDG config path.
func NewSettingsService() *SettingsService {
	return &SettingsService{
		configPath: filepath.Join(xdg.ConfigHome, "koyori-ide", "settings.json"),
	}
}

// NewSettingsServiceWithPath creates a SettingsService that reads and
// writes settings at the given absolute path. Used by main.go when the
// ProfileService has determined the active profile's settings path.
func NewSettingsServiceWithPath(path string) *SettingsService {
	return &SettingsService{configPath: path}
}

// setConfigPath redirects the service to read/write settings at the
// given path. Called by ProfileService (via the onSwitch callback) when
// the active profile changes. The next LoadSettings/SaveSettings call
// uses the new path.
//
// N-76: takes the write lock so it doesn't race with an in-flight
// Load/Save. If a Load/Save is in progress, setConfigPath waits for it
// to finish before swapping the path, preventing cross-profile writes.
//
//wails:ignore
func (s *SettingsService) setConfigPath(path string) {
	s.pathMu.Lock()
	defer s.pathMu.Unlock()
	s.configPath = path
}

// assetsDir returns the personalization assets directory derived from the
// config path: <configDir>/koyori-ide/assets/. Callers hold pathMu.
func (s *SettingsService) assetsDir() string {
	return filepath.Join(filepath.Dir(s.configPath), "assets")
}

// SavePersonalizationAsset stores an uploaded image (Step 2: copy to
// <configDir>/koyori-ide/assets/<filename>, not base64). G-SEC-06: the
// filename is sanitized to a basename (no path separators/traversal) and
// the resolved path is validated to be within the assets dir.
// Returns the relative path "assets/<filename>" for storage in PersonalizationConfig.
func (s *SettingsService) SavePersonalizationAsset(filename string, data []byte) (string, error) {
	// Sanitize filename: keep only the basename, reject empty.
	clean := filepath.Base(filename)
	if clean == "" || clean == "." || clean == ".." {
		return "", fmt.Errorf("%w: invalid asset filename", ErrInvalidInput)
	}
	s.pathMu.RLock()
	assetsDir := s.assetsDir()
	s.pathMu.RUnlock()
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		return "", fmt.Errorf("create assets dir: %w", err)
	}
	targetPath := filepath.Join(assetsDir, clean)
	// G-SEC-06: validate the resolved path is within the assets dir.
	if _, err := ValidateMutatingPathWithinRoot(assetsDir, targetPath); err != nil {
		return "", fmt.Errorf("asset path validation failed: %w", err)
	}
	// Limit asset size to 8MB to prevent abuse (Step 2).
	const maxAssetSize = 8 << 20
	if len(data) > maxAssetSize {
		return "", fmt.Errorf("%w: asset exceeds 8MB limit", ErrInvalidInput)
	}
	if err := atomicWriteFile(targetPath, data, 0o644); err != nil {
		return "", fmt.Errorf("write asset: %w", err)
	}
	return "assets/" + clean, nil
}

// ReadPersonalizationAsset reads an asset by its relative path (e.g.
// "assets/avatar.png"). G-SEC-06: validates the path is within the assets dir.
func (s *SettingsService) ReadPersonalizationAsset(relPath string) ([]byte, error) {
	s.pathMu.RLock()
	assetsDir := s.assetsDir()
	s.pathMu.RUnlock()
	fullPath := filepath.Join(assetsDir, filepath.Base(relPath))
	if _, err := ValidatePathWithinRoot(assetsDir, fullPath); err != nil {
		return nil, fmt.Errorf("asset path validation failed: %w", err)
	}
	return os.ReadFile(fullPath)
}

// DeletePersonalizationAsset removes an asset by relative path.
func (s *SettingsService) DeletePersonalizationAsset(relPath string) error {
	s.pathMu.RLock()
	assetsDir := s.assetsDir()
	s.pathMu.RUnlock()
	fullPath := filepath.Join(assetsDir, filepath.Base(relPath))
	if _, err := ValidateMutatingPathWithinRoot(assetsDir, fullPath); err != nil {
		return fmt.Errorf("asset path validation failed: %w", err)
	}
	return os.Remove(fullPath)
}

// LoadSettings reads settings from disk, falling back to defaults if the file
// is missing or corrupt. If the on-disk key is legacy plaintext (no encryption
// prefix), it is auto-migrated to encrypted form and re-saved (N-13).
//
// G-SEC-07: the decrypted API key is NOT returned in Settings.AIApiKey (it is
// cleared to "") so the plaintext never crosses the Wails binding into the
// frontend JS heap. Instead AIApiKeyConfigured reports whether a key is stored
// and AIApiKeyStorageMethod labels the storage method. The decrypted key is
// available to the backend via getDecryptedAPIKey.
//
// N-76: holds the read lock for the entire operation so a concurrent
// SetConfigPath cannot swap the path mid-load (which would read from the
// new profile's file using the old profile's expectations, or vice versa).
func (s *SettingsService) LoadSettings() (Settings, error) {
	s.pathMu.RLock()
	defer s.pathMu.RUnlock()
	settings := defaultSettings()
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return settings, err
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		// N-109: the previous implementation silently returned defaults
		// with nil error, so a corrupt settings file invisibly reset the
		// user's preferences. We still return defaults (the app must be
		// able to launch with a corrupt file), but now log the parse
		// error so it's visible in the log file and on stderr — making
		// the silent reset diagnosable instead of invisible.
		slog.Warn("settings file is corrupt, falling back to defaults",
			"path", s.configPath, "err", err)
		return defaultSettings(), nil
	}
	if settings.SchemaVersion < 0 || settings.SchemaVersion > currentSettingsSchemaVersion {
		return defaultSettings(), fmt.Errorf(
			"settings schema %d is not supported by this client (current %d)",
			settings.SchemaVersion,
			currentSettingsSchemaVersion,
		)
	}
	// Missing schemaVersion is the legacy schema 0. Its fields already map to
	// the current struct, so migration only advances the independent marker.
	settings.SchemaVersion = currentSettingsSchemaVersion
	settings.AgentPermissionMode = normalizeAgentPermissionMode(settings.AgentPermissionMode)
	settings.AIWindowTheme = normalizeAIWindowTheme(settings.AIWindowTheme)
	settings.AISidebarWidth = clampInt(settings.AISidebarWidth, 288, 260, 380)
	settings.AITerminalWidth = clampInt(settings.AITerminalWidth, 440, 340, 960)
	// G-SEC-07: strip every provider key before any operation that can return.
	// In particular, a primary-key decryption error must not bypass provider
	// redaction and expose raw persisted values to the renderer.
	for i := range settings.AIProviderConfigs {
		cfg := &settings.AIProviderConfigs[i]
		cfg.APIKeyConfigured = cfg.APIKey != ""
		cfg.APIKey = ""
	}
	// Decrypt the API key (handles legacy plaintext, dpapi:, aes:, plain:).
	rawKey := settings.AIApiKey
	decrypted, derr := DecryptSecret(keyringAccount, rawKey)
	if derr != nil {
		// Decryption failed — clear the key to avoid exposing ciphertext, retain
		// presence metadata, and return the original error so callers cannot
		// mistake an unavailable key for an empty setting.
		settings.AIApiKey = ""
		settings.AIApiKeyConfigured = rawKey != ""
		settings.AIApiKeyStorageMethod = SecretMethod(rawKey)
		return settings, derr
	}
	// Auto-migrate legacy plaintext to encrypted form (N-13). Best-effort:
	// errors are ignored so load still succeeds even if migration fails.
	// saveSettingsLocked is used (not SaveSettings) because we already hold
	// the read lock — Go's sync.RWMutex is NOT reentrant, so calling
	// SaveSettings (which tries to RLock again) would deadlock. The
	// decrypted key is used for the re-save, then cleared from the returned
	// struct (G-SEC-07).
	if rawKey != "" && !IsSecretEncrypted(rawKey) {
		migrationSettings := settings
		migrationSettings.AIApiKey = decrypted
		_ = s.saveSettingsLocked(migrationSettings)
	}
	// G-SEC-07: do NOT return the plaintext key. Signal presence via the
	// boolean and label the storage method for the frontend.
	settings.AIApiKey = ""
	settings.AIApiKeyConfigured = decrypted != ""
	settings.AIApiKeyStorageMethod = SecretMethod(rawKey)
	return settings, nil
}

// encryptSecretForSettingsForTest allows SaveSettings encryption failures to be
// injected by tests. A nil hook preserves the production EncryptSecret path.
var encryptSecretForSettingsForTest func(account, plaintext string) (string, error)

func encryptSecretForSettings(account, plaintext string) (string, error) {
	if encryptSecretForSettingsForTest != nil {
		return encryptSecretForSettingsForTest(account, plaintext)
	}
	return EncryptSecret(account, plaintext)
}

// SaveSettings writes settings to disk as pretty-printed JSON. The API key is
// encrypted before writing (N-13). If encryption fails, saving fails rather
// than persisting the key in plaintext.
//
// N-76: holds the read lock so a concurrent SetConfigPath cannot swap the
// path mid-save (which would write the old profile's data to the new
// profile's file).
func (s *SettingsService) SaveSettings(settings Settings) error {
	s.pathMu.RLock()
	defer s.pathMu.RUnlock()
	return s.saveSettingsLocked(settings)
}

// saveSettingsLocked encrypts the API key and writes to disk. Caller MUST
// hold s.pathMu (read or write). Used internally by LoadSettings (which
// already holds the lock) and SaveSettings.
//
// G-SEC-07: when the caller passes an empty AIApiKey but signals
// AIApiKeyConfigured (the frontend no longer holds the plaintext key, so it
// saves unrelated changes with empty + configured=true), the existing
// on-disk key is preserved so unrelated saves don't wipe the stored key. A
// genuine clear passes AIApiKeyConfigured=false, so the key is written empty.
// ErrSettingsConflict is returned when settings CAS fails (prompt-7 Task F).
var ErrSettingsConflict = fmt.Errorf("settings version conflict: disk was modified by another window")

func (s *SettingsService) saveSettingsLocked(settings Settings) error {
	// Make a shallow copy so we don't mutate the caller's struct.
	copy := settings
	snapshot, err := s.readSettingsSnapshotLocked()
	if err != nil {
		return err
	}
	if snapshot.schemaVersion < 0 || snapshot.schemaVersion > currentSettingsSchemaVersion {
		return fmt.Errorf(
			"settings schema %d is not supported by this client (current %d)",
			snapshot.schemaVersion,
			currentSettingsSchemaVersion,
		)
	}
	copy.SchemaVersion = currentSettingsSchemaVersion
	copy.AgentPermissionMode = normalizeAgentPermissionMode(copy.AgentPermissionMode)

	// prompt-7 Task F / BUG-M14: optional version CAS + monotonic bump.
	if snapshot.versionOK {
		if copy.ExpectedVersion != nil && *copy.ExpectedVersion != snapshot.version {
			return fmt.Errorf("%w (expected %d, disk %d)", ErrSettingsConflict, *copy.ExpectedVersion, snapshot.version)
		}
		copy.Version = snapshot.version + 1
	} else if copy.Version <= 0 {
		copy.Version = 1
	}
	copy.ExpectedVersion = nil

	// G-SEC-07: preserve the existing on-disk key when the frontend saves
	// without the plaintext key. Decrypt the stored value to plaintext so the
	// normal encryption path below re-encrypts it (no double-encryption).
	if copy.AIApiKey == "" && copy.AIApiKeyConfigured {
		if existing := snapshot.aiAPIKey; existing != "" {
			plaintext, derr := DecryptSecret(keyringAccount, existing)
			if derr != nil {
				return fmt.Errorf("preserve existing API key: %w", derr)
			}
			copy.AIApiKey = plaintext
		}
	}
	// G-SEC-07/CRIT-01: preserve on-disk keys for multi-provider configs. The
	// frontend sends empty apiKey + apiKeyConfigured=true when the user didn't
	// enter a new key. Read the existing configs from disk and restore their
	// keys so unrelated saves don't wipe stored keys.
	//
	// CRIT-01 scope fix: this block is OUTSIDE the legacy-key if-block so
	// provider keys are preserved regardless of the legacy key state.
	// Previously it was nested inside, so when the legacy key was non-empty
	for i := range copy.AIProviderConfigs {
		cfg := &copy.AIProviderConfigs[i]
		normalized, reasoningErr := normalizeReasoningEffort(cfg.ReasoningEffort)
		if reasoningErr != nil {
			return fmt.Errorf("invalid reasoning effort for provider %q: %w", cfg.ID, reasoningErr)
		}
		if reasoningErr = validateReasoningCapability(cfg.Provider, cfg.Model, cfg.Protocol, normalized); reasoningErr != nil {
			return fmt.Errorf("invalid reasoning capability for provider %q: %w", cfg.ID, reasoningErr)
		}
		cfg.ReasoningEffort = normalized
		if cfg.APIKey == "" && cfg.APIKeyConfigured {
			for _, ec := range snapshot.aiProviderConfigs {
				if ec.ID == cfg.ID && ec.APIKey != "" {
					plaintext, derr := DecryptSecret(keyringAccount, ec.APIKey)
					if derr != nil {
						return fmt.Errorf("preserve API key for provider %q: %w", cfg.ID, derr)
					}
					cfg.APIKey = plaintext
					break
				}
			}
		}
	}
	encrypted, err := encryptSecretForSettings(keyringAccount, copy.AIApiKey)
	if err != nil {
		return fmt.Errorf("encrypt API key: %w", err)
	} else {
		copy.AIApiKey = encrypted
	}
	// CRIT-01: encrypt each provider config's API key before writing to disk
	// so multi-provider keys are never stored in plaintext. This mirrors the
	// legacy key encryption above and uses the same EncryptSecret path
	// (DPAPI on Windows, AES-256-GCM elsewhere).
	for i := range copy.AIProviderConfigs {
		cfg := &copy.AIProviderConfigs[i]
		if cfg.APIKey == "" {
			continue
		}
		enc, encErr := encryptSecretForSettings(keyringAccount, cfg.APIKey)
		if encErr != nil {
			return fmt.Errorf("encrypt API key for provider %q: %w", cfg.ID, encErr)
		} else {
			cfg.APIKey = enc
		}
	}
	// G-SEC-09: atomic write (temp file + rename) so a crash mid-write
	// cannot leave a half-written settings file. 0600 because the file
	// holds an (encrypted) API key.
	merged, err := mergeSettingsSnapshot(snapshot.raw, copy)
	if err != nil {
		return err
	}
	return atomicWriteJSON(s.configPath, merged, 0600)
}

type settingsDiskSnapshot struct {
	raw               map[string]json.RawMessage
	schemaVersion     int
	version           int64
	versionOK         bool
	aiAPIKey          string
	aiProviderConfigs []AIProviderConfig
}

// readSettingsSnapshotLocked reads and parses the settings file once for all
// save-time CAS, secret-preservation, and unknown-field merge decisions.
func (s *SettingsService) readSettingsSnapshotLocked() (settingsDiskSnapshot, error) {
	data, err := s.readConfigFile()
	if err != nil {
		if os.IsNotExist(err) {
			return settingsDiskSnapshot{}, nil
		}
		return settingsDiskSnapshot{}, fmt.Errorf("read settings snapshot: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return settingsDiskSnapshot{}, fmt.Errorf("decode settings snapshot: %w", err)
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}
	snapshot := settingsDiskSnapshot{raw: raw, versionOK: true}
	if value, ok := raw["schemaVersion"]; ok {
		if err := json.Unmarshal(value, &snapshot.schemaVersion); err != nil {
			return settingsDiskSnapshot{}, fmt.Errorf("decode settings schema: %w", err)
		}
	}
	if value, ok := raw["version"]; ok {
		if err := json.Unmarshal(value, &snapshot.version); err != nil {
			snapshot.versionOK = false
		}
	}
	if value, ok := raw["aiApiKey"]; ok {
		_ = json.Unmarshal(value, &snapshot.aiAPIKey)
	}
	if value, ok := raw["aiProviderConfigs"]; ok {
		_ = json.Unmarshal(value, &snapshot.aiProviderConfigs)
	}
	return snapshot, nil
}

func mergeSettingsSnapshot(raw map[string]json.RawMessage, settings Settings) (map[string]json.RawMessage, error) {
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	var known map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &known); err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}

	merged := make(map[string]json.RawMessage, len(raw)+len(known))
	for key, value := range raw {
		merged[key] = value
	}
	settingsType := reflect.TypeOf(settings)
	knownNames := make(map[string]struct{}, settingsType.NumField())
	for i := 0; i < settingsType.NumField(); i++ {
		name := strings.Split(settingsType.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			knownNames[strings.ToLower(name)] = struct{}{}
		}
	}
	for key := range merged {
		if _, ok := knownNames[strings.ToLower(key)]; ok {
			delete(merged, key)
		}
	}
	for key, value := range known {
		merged[key] = value
	}
	return merged, nil
}

// readRawAPIKeyLocked reads the raw (possibly encrypted) aiApiKey value from
// the on-disk settings file. Returns "" if the file is missing, corrupt, or
// the key is absent. Caller MUST hold s.pathMu (read or write).
func (s *SettingsService) readRawAPIKeyLocked() string {
	data, err := s.readConfigFile()
	if err != nil {
		return ""
	}
	var raw struct {
		AIApiKey string `json:"aiApiKey"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	return raw.AIApiKey
}

// readRawProviderConfigsLocked reads the raw AIProviderConfigs from the on-disk
// settings file for getAPIKeyForConfig. Returns an empty slice if the file is
// missing, corrupt, or has no configs. Caller MUST hold s.pathMu (read or write).
func (s *SettingsService) readRawProviderConfigsLocked() []AIProviderConfig {
	data, err := s.readConfigFile()
	if err != nil {
		return nil
	}
	var raw struct {
		AIProviderConfigs []AIProviderConfig `json:"aiProviderConfigs"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return raw.AIProviderConfigs
}

// getAPIKeyForConfig returns the plaintext API key for the given config ID.
// Used by AIService.SetConfig when UseStoredKey is true (G-SEC-07) so the
// backend can make AI calls without the key ever crossing the Wails binding.
// Returns ("", nil) when the config or its key is not found.
//
// CRIT-01: provider keys are stored encrypted on disk (via EncryptSecret).
// DecryptSecret handles both encrypted ("dpapi:"/"aes:"/"keyring:") and
// legacy plaintext values (returned as-is for backward compatibility).
//
//wails:ignore
func (s *SettingsService) getAPIKeyForConfig(configID string) (string, error) {
	s.pathMu.RLock()
	defer s.pathMu.RUnlock()
	configs := s.readRawProviderConfigsLocked()
	for _, c := range configs {
		if c.ID == configID {
			if c.APIKey == "" {
				return "", nil
			}
			return DecryptSecret(keyringAccount, c.APIKey)
		}
	}
	return "", nil
}

// IsAPIKeyEncryptedOnDisk reads the raw settings file and returns true if the
// stored API key carries an encryption prefix ("dpapi:" or "aes:"). Returns
// false if the key is plaintext, empty, or the file is missing/corrupt.
//
// N-76: holds the read lock so configPath cannot change mid-read.
func (s *SettingsService) IsAPIKeyEncryptedOnDisk() bool {
	s.pathMu.RLock()
	defer s.pathMu.RUnlock()
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return false
	}
	var raw struct {
		AIApiKey string `json:"aiApiKey"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	return IsSecretEncrypted(raw.AIApiKey)
}

// GetAPIKeyStorageMethod returns a human-readable label for how the API key is
// stored on disk: "dpapi", "aes", "plain", or "none" (when empty or missing).
//
// N-76: holds the read lock so configPath cannot change mid-read.
func (s *SettingsService) GetAPIKeyStorageMethod() string {
	s.pathMu.RLock()
	defer s.pathMu.RUnlock()
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return "none"
	}
	var raw struct {
		AIApiKey string `json:"aiApiKey"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "none"
	}
	return SecretMethod(raw.AIApiKey)
}

// getDecryptedAPIKey reads the on-disk API key and returns the decrypted
// plaintext. It is intended for internal backend use (e.g. the AIService
// making API calls) so the decrypted key never has to travel to the frontend
// via LoadSettings (G-SEC-07). Returns ("", nil) when no key is stored.
//
// N-76: holds the read lock so configPath cannot change mid-read.
//
//wails:ignore
func (s *SettingsService) getDecryptedAPIKey() (string, error) {
	s.pathMu.RLock()
	defer s.pathMu.RUnlock()
	rawKey := s.readRawAPIKeyLocked()
	if rawKey == "" {
		return "", nil
	}
	return DecryptSecret(keyringAccount, rawKey)
}

const (
	maxSettingsSecretAccountSize = 4 << 10
	extensionSecretAccountPrefix = "koyori-ide.extHost."
)

func validateExtensionSecretAccount(account string) error {
	if strings.TrimSpace(account) == "" {
		return fmt.Errorf("secret account is required: %w", ErrInvalidInput)
	}
	if len(account) > maxSettingsSecretAccountSize {
		return fmt.Errorf("secret account exceeds %d bytes: %w", maxSettingsSecretAccountSize, ErrInvalidInput)
	}
	if strings.ContainsRune(account, '\x00') {
		return fmt.Errorf("secret account contains a NUL byte: %w", ErrInvalidInput)
	}
	if !strings.HasPrefix(account, extensionSecretAccountPrefix) {
		return fmt.Errorf("secret account is outside the extension namespace: %w", ErrInvalidInput)
	}
	return nil
}

// readExtensionSecretsLocked reads the complete settings document together
// with its encrypted extension-secret map. Caller MUST hold s.pathMu.
func (s *SettingsService) readExtensionSecretsLocked() (map[string]json.RawMessage, map[string]string, error) {
	data, err := s.readConfigFile()
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]json.RawMessage), make(map[string]string), nil
		}
		return nil, nil, fmt.Errorf("read secret store: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("decode secret store: %w", err)
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}

	secrets := make(map[string]string)
	if encoded, ok := raw[extensionSecretsSettingsKey]; ok {
		if err := json.Unmarshal(encoded, &secrets); err != nil {
			return nil, nil, fmt.Errorf("decode encrypted secrets: %w", err)
		}
		if secrets == nil {
			secrets = make(map[string]string)
		}
	}
	return raw, secrets, nil
}

// writeExtensionSecretsLocked atomically persists encrypted markers or
// ciphertext alongside settings. Caller MUST hold s.pathMu.
func (s *SettingsService) writeExtensionSecretsLocked(raw map[string]json.RawMessage, secrets map[string]string) error {
	if len(secrets) == 0 {
		delete(raw, extensionSecretsSettingsKey)
	} else {
		encoded, err := json.Marshal(secrets)
		if err != nil {
			return fmt.Errorf("encode encrypted secrets: %w", err)
		}
		raw[extensionSecretsSettingsKey] = encoded
	}

	writeJSON := s.writeJSON
	if writeJSON == nil {
		writeJSON = atomicWriteJSON
	}
	if err := writeJSON(s.configPath, raw, 0o600); err != nil {
		return fmt.Errorf("write secret store: %w", err)
	}
	return nil
}

// getExtensionSecret returns the decrypted value stored for account. The encrypted
// representation remains in settings.json and is never included in Settings.
// An absent account returns ("", nil).
func (s *SettingsService) getExtensionSecret(account string) (string, error) {
	if err := validateExtensionSecretAccount(account); err != nil {
		return "", err
	}

	s.pathMu.RLock()
	defer s.pathMu.RUnlock()
	_, secrets, err := s.readExtensionSecretsLocked()
	if err != nil {
		return "", err
	}
	stored := secrets[account]
	if stored == "" {
		return "", nil
	}
	if !IsSecretEncrypted(stored) {
		return "", fmt.Errorf("decrypt secret: stored value is not encrypted")
	}
	plaintext, err := DecryptSecret(account, stored)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return plaintext, nil
}

// storeExtensionSecret encrypts and atomically persists value for account.
// Empty values are treated as deletion.
func (s *SettingsService) storeExtensionSecret(account, value string) error {
	if err := validateExtensionSecretAccount(account); err != nil {
		return err
	}
	if len(value) > maxSecretPlaintextSize {
		return fmt.Errorf("secret value exceeds %d bytes: %w", maxSecretPlaintextSize, ErrInvalidInput)
	}
	if value == "" {
		return s.deleteExtensionSecret(account)
	}

	s.pathMu.Lock()
	defer s.pathMu.Unlock()
	raw, secrets, err := s.readExtensionSecretsLocked()
	if err != nil {
		return err
	}

	previousStored := secrets[account]
	var previousPlaintext string
	if strings.HasPrefix(previousStored, secretPrefixKeyring) {
		previousPlaintext, err = DecryptSecret(account, previousStored)
		if err != nil {
			return fmt.Errorf("read existing secret before update: %w", err)
		}
	}

	stored, err := EncryptSecret(account, value)
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}
	if !IsSecretEncrypted(stored) {
		return fmt.Errorf("encrypt secret: platform returned an insecure storage format")
	}
	secrets[account] = stored
	if err := s.writeExtensionSecretsLocked(raw, secrets); err != nil {
		// Native keyring encryption publishes before the settings marker is
		// written. Restore the previous keyring state if persistence fails.
		if strings.HasPrefix(stored, secretPrefixKeyring) {
			var rollbackErr error
			if strings.HasPrefix(previousStored, secretPrefixKeyring) {
				_, rollbackErr = EncryptSecret(account, previousPlaintext)
			} else {
				rollbackErr = DeleteSecret(account)
			}
			if rollbackErr != nil {
				return fmt.Errorf("%w (keyring rollback failed: %v)", err, rollbackErr)
			}
		}
		return err
	}
	return nil
}

// ListSecrets returns information about secrets stored in the platform
// keyring (N-49). On Windows this returns an empty list (DPAPI blobs live in
// settings.json). On macOS/Linux it queries the Keychain / libsecret for
// koyori-ide entries, allowing the settings UI to show users what's stored
// and help them clean up orphan entries.
func (s *SettingsService) ListSecrets() ([]SecretInfo, error) {
	return ListSecrets()
}

// deleteExtensionSecret removes the persisted encrypted blob for account and, for a
// keyring-backed value, its Keychain/libsecret entry. DPAPI/AES values are
// deleted by removing their settings.json blob. Accounts without a persisted
// blob still delegate to the platform cleanup API for orphan removal.
func (s *SettingsService) deleteExtensionSecret(account string) error {
	if err := validateExtensionSecretAccount(account); err != nil {
		return err
	}

	s.pathMu.Lock()
	defer s.pathMu.Unlock()
	raw, secrets, err := s.readExtensionSecretsLocked()
	if err != nil {
		return err
	}
	stored, ok := secrets[account]
	if !ok {
		return DeleteSecret(account)
	}

	// Delete a native keyring entry first. DPAPI/AES values have no platform
	// entry; their encrypted blob is the complete persisted state. If the
	// subsequent atomic file update fails, restore a deleted keyring entry so
	// the old on-disk marker remains valid.
	var plaintext string
	if strings.HasPrefix(stored, secretPrefixKeyring) {
		plaintext, err = DecryptSecret(account, stored)
		if err != nil {
			return fmt.Errorf("read secret before deletion: %w", err)
		}
		if err := DeleteSecret(account); err != nil {
			return fmt.Errorf("delete platform secret: %w", err)
		}
	}

	delete(secrets, account)
	if err := s.writeExtensionSecretsLocked(raw, secrets); err != nil {
		if strings.HasPrefix(stored, secretPrefixKeyring) {
			if _, rollbackErr := EncryptSecret(account, plaintext); rollbackErr != nil {
				return fmt.Errorf("%w (keyring rollback failed: %v)", err, rollbackErr)
			}
		}
		return err
	}
	return nil
}

func defaultSettings() Settings {
	return Settings{
		SchemaVersion:           currentSettingsSchemaVersion,
		Language:                "en",
		Theme:                   "dark",
		FontSize:                14,
		FontFamily:              "JetBrains Mono",
		TabSize:                 2,
		WordWrap:                true,
		LineNumbers:             true,
		Minimap:                 false,
		AIApiKey:                "",
		AIBaseURL:               "https://api.openai.com",
		AIModel:                 "gpt-4o",
		AISystemPrompt:          "",
		CursorBlinking:          "blink",
		CursorStyle:             "line",
		BracketColorization:     true,
		AutoSave:                false,
		AutoSaveDelay:           "afterDelay",
		AIProvider:              "",
		Temperature:             0.7,
		MaxTokens:               4096,
		DefaultShell:            "",
		TerminalFontSize:        13,
		TerminalCursorStyle:     "block",
		Scrollback:              10000,
		UIDensity:               "comfortable",
		FontSizeScaling:         100,
		InlineCompletionEnabled: true,
		// prompt-9 Task 9-A: format via LSP before save by default.
		FormatOnSave: true,
		// G-EDIT-01/02/03: editor hygiene defaults (match VSCode defaults).
		InsertSpaces:          true,
		InsertFinalNewline:    true,
		EmmetEnabled:          true,
		EmmetIncludeLanguages: map[string]string{},
		AiChatPosition:        "right",
		ActivityBarVisible:    true,
		AgentPermissionMode:   string(agentcore.SessionPermissionAlwaysAsk),
		// N-29: sandbox enabled by default (v2 behavior).
		EnablePluginSandbox: true,
		// prompt-5 Task C: do not auto-pop AI window on every launch.
		OpenAIWindowOnStartup: false,
		AIWindowTheme:         "apple-dark",
		AISidebarWidth:        288,
		AITerminalWidth:       440,
	}
}

func normalizeAIWindowTheme(value string) string {
	switch value {
	case "apple-dark", "apple-light", "claude-dark", "claude-light", "system":
		return value
	default:
		return "apple-dark"
	}
}

func normalizeAgentPermissionMode(value string) string {
	mode := agentcore.SessionPermissionMode(strings.TrimSpace(value))
	if mode.Valid() {
		return string(mode)
	}
	return string(agentcore.SessionPermissionAlwaysAsk)
}

func clampInt(value, fallback, min, max int) int {
	if value == 0 {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
