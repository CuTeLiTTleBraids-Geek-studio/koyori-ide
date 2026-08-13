package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// DebugSession holds per-session DAP/CDP state (prompt-5: 多会话).
// A DebugService embeds the active *DebugSession so existing single-session
// methods keep operating on the active session via field promotion.
type DebugSession struct {
	mu                        sync.Mutex
	writeMu                   sync.Mutex // serializes DAP writes on conn
	cmd                       *exec.Cmd
	running                   bool
	addr                      string
	mode                      string // "package" | "test" | "node" | "attach"
	adapterID                 string
	sourcePackID              string
	sourcePackVersion         string
	started                   time.Time
	conn                      net.Conn
	seq                       int64
	pending                   map[int]chan dapMessage
	readerDone                chan struct{}
	readerDoneOnce            *sync.Once
	readerWG                  *sync.WaitGroup
	dapInitialized            chan struct{}
	dapInitializedOnce        *sync.Once
	processDone               chan struct{}
	processDoneOnce           *sync.Once
	runGeneration             uint64
	debugThreadsRunID         string
	debugThreadsStateRevision uint64

	// Stack loading is capability-driven. DAP adapters must explicitly opt in
	// to delayed stack loading; Node CDP enables async stacks separately.
	supportsDelayedStackTraceLoading bool
	supportsAsyncStackTrace          bool
	stackTotalFrames                 int
	stackHasMore                     bool
	asyncStackRootID                 string
	asyncStackCounter                uint64
	asyncStackContinuations          map[string]nodeAsyncStackContinuation

	// session UI state
	stopped bool
	// stopSequence increments on every transition into the stopped state
	// (GOAL-P1-03 execution point 5).
	//
	// A step-in target list is only meaningful for the stop it was fetched
	// during: after resuming, the frame is gone and the target IDs the adapter
	// handed out refer to nothing. Without this counter a stale menu left open
	// across a resume would send a target ID that the adapter either rejects
	// obscurely or, worse, silently reinterprets against a different frame.
	// Callers echo the sequence back and a mismatch is refused.
	stopSequence uint64
	threadID     int
	stopReason   string
	breakpoints  []DebugBreakpoint
	stack        []DebugStackFrame
	locals       []DebugVariable
	watches      []string
	watchValues  []DebugVariable
	cwd          string

	// last launch for Restart (prompt-12 12-A/G)
	lastLaunch debugLaunchSpec

	// prompt-13 13-A: Node CDP session (same panel as DAP)
	cdp *nodeCDPClient
	// Browser CDP reuses the same debugger state while retaining independent
	// process ownership and target-selection epochs.
	browserLaunch      *browserLaunch
	browserConfig      browserDebugSpec
	browserTargets     []BrowserTarget
	browserTargetID    string
	browserTargetEpoch uint64
	browserConsole     []BrowserConsoleEntry
	browserNetwork     []BrowserNetworkEntry
	// last evaluate/watch error for UI (prompt-13 13-C)
	lastError string

	// prompt-5: function breakpoints persisted on this session (DAP setFunctionBreakpoints)
	functionBreakpoints []FunctionBreakpoint

	protocolLog debugProtocolLogger
}

// DebugService provides an in-IDE DAP client over Delve `dlv dap` (prompt-11/12).
// Capabilities: breakpoints (condition), F5/Restart, step, stack, locals, watch/evaluate.
// prompt-5: supports multiple debug sessions via a sessionID → *DebugSession map.
// The embedded *DebugSession is the currently active session; all legacy
// single-session methods operate on it through field promotion.
type DebugService struct {
	*DebugSession
	workspaceContext            *WorkspaceContext
	approveProjectExecutable    func(kind, path string) bool
	beforeWorkspaceCommandStart func()
	sessionsMu                  sync.RWMutex
	sessions                    map[string]*DebugSession
	activeSessionID             string
	sessionCounter              int64
	browserDeps                 browserDebugDeps
	browserEnumerate            func(context.Context, string, time.Duration) ([]BrowserTarget, error)
	browserConnect              func(BrowserTarget, time.Duration) (*nodeCDPClient, error)
	debugProtocolLog            atomic.Bool
	protocolMu                  sync.RWMutex
	protocolEmitter             func(string, any)
	closed                      bool
}

var _ DebugThreadsBackend = (*DebugService)(nil)

type debugProtocolLogger func(protocol, direction string, payload any)

type debugProtocolRawMessage []byte

const (
	debugProtocolLogMaxBytes       = 4 << 10
	debugProtocolInspectMaxBytes   = 16 << 10
	debugProtocolMaxDepth          = 8
	debugProtocolMaxCollectionSize = 64
	debugProtocolMaxItems          = 256
	debugProtocolMaxStringBytes    = 512
)

// SetDebugProtocolLog toggles DAP/CDP tracing. It is intentionally disabled
// by default; the settings UI can call this binding when its setting changes.
func (d *DebugService) SetDebugProtocolLog(enabled bool) {
	if d == nil {
		return
	}
	d.debugProtocolLog.Store(enabled)
}

func loadDebugProtocolLogSetting() (bool, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return false, fmt.Errorf("resolve debug config directory: %w", err)
	}
	return loadDebugProtocolLogSettingAt(configDir)
}

func loadDebugProtocolLogSettingAt(configDir string) (bool, error) {
	root := filepath.Join(configDir, "koyori-ide")
	activeProfile := defaultProfileName
	statePath := filepath.Join(root, "profiles-state.json")
	stateData, err := os.ReadFile(statePath)
	if err == nil {
		var state struct {
			ActiveProfile string `json:"activeProfile"`
		}
		if err := json.Unmarshal(stateData, &state); err != nil {
			return false, fmt.Errorf("decode debug profile state: %w", err)
		}
		if state.ActiveProfile != "" {
			if !profileNameRe.MatchString(state.ActiveProfile) {
				return false, fmt.Errorf("invalid active debug profile %q", state.ActiveProfile)
			}
			activeProfile = state.ActiveProfile
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read debug profile state: %w", err)
	}

	paths := []string{
		filepath.Join(root, "profiles", activeProfile, "settings.json"),
		filepath.Join(root, "settings.json"),
	}
	for _, settingsPath := range paths {
		data, err := os.ReadFile(settingsPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("read debug settings: %w", err)
		}
		var setting struct {
			DebugProtocolLog *bool `json:"debugProtocolLog"`
		}
		if err := json.Unmarshal(data, &setting); err != nil {
			return false, fmt.Errorf("decode debug settings: %w", err)
		}
		if setting.DebugProtocolLog == nil {
			return false, nil
		}
		return *setting.DebugProtocolLog, nil
	}
	return false, nil
}

// DebugProtocolLogEnabled reports the current protocol tracing setting.
func (d *DebugService) DebugProtocolLogEnabled() bool {
	return d != nil && d.debugProtocolLog.Load()
}

func (d *DebugService) emitDebugProtocol(protocol, direction string, payload any) {
	if d == nil || !d.debugProtocolLog.Load() {
		return
	}
	text := formatDebugProtocolLog(protocol, direction, payload)
	payloadEvent := map[string]any{
		"channel": "Debug Protocol",
		"text":    text,
	}

	d.protocolMu.RLock()
	emit := d.protocolEmitter
	d.protocolMu.RUnlock()
	if emit != nil {
		emit("output:write", payloadEvent)
		return
	}
	if app := application.Get(); app != nil {
		app.Event.Emit("output:write", payloadEvent)
	}
}

func formatDebugProtocolLog(protocol, direction string, payload any) string {
	sanitizer := debugProtocolSanitizer{remaining: debugProtocolMaxItems}
	safe := sanitizer.value(payload, "", 0)
	body, err := json.Marshal(safe)
	if err != nil {
		body = []byte(`{"message":"protocol payload unavailable"}`)
	}
	prefix := strings.ToUpper(strings.TrimSpace(protocol)) + " " + strings.TrimSpace(direction) + " "
	if strings.TrimSpace(protocol) == "" {
		prefix = "DEBUG " + strings.TrimSpace(direction) + " "
	}
	const suffix = "\n"
	available := debugProtocolLogMaxBytes - len(prefix) - len(suffix)
	if available < 0 {
		available = 0
	}
	bodyText := truncateDebugProtocolString(string(body), available)
	return prefix + bodyText + suffix
}

type debugProtocolSanitizer struct {
	remaining int
}

func (s *debugProtocolSanitizer) value(value any, key string, depth int) any {
	if debugProtocolSensitiveKey(key) {
		return "[redacted]"
	}
	if s.remaining <= 0 {
		return "[item limit]"
	}
	s.remaining--
	if depth >= debugProtocolMaxDepth {
		return "[depth limit]"
	}

	switch typed := value.(type) {
	case nil, bool, json.Number:
		return typed
	case string:
		return truncateDebugProtocolString(typed, debugProtocolMaxStringBytes)
	case json.RawMessage:
		return s.rawMessage(typed, depth+1)
	case debugProtocolRawMessage:
		return s.rawMessage([]byte(typed), depth+1)
	case dapMessage:
		message := map[string]any{
			"seq":         typed.Seq,
			"type":        typed.Type,
			"request_seq": typed.RequestSeq,
			"success":     typed.Success,
			"command":     typed.Command,
			"event":       typed.Event,
			"message":     typed.Message,
		}
		if len(typed.Arguments) > 0 {
			message["arguments"] = s.rawMessage(typed.Arguments, depth+1)
		}
		if len(typed.Body) > 0 {
			message["body"] = s.rawMessage(typed.Body, depth+1)
		}
		return s.value(message, "", depth+1)
	}

	rv := reflect.ValueOf(value)
	for rv.IsValid() && (rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer) {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return nil
	}
	switch rv.Kind() {
	case reflect.Bool:
		return rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	case reflect.String:
		return truncateDebugProtocolString(rv.String(), debugProtocolMaxStringBytes)
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return "[unsupported map]"
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		if len(keys) > debugProtocolMaxCollectionSize {
			keys = keys[:debugProtocolMaxCollectionSize]
		}
		out := make(map[string]any, len(keys)+1)
		for _, mapKey := range keys {
			name := truncateDebugProtocolString(mapKey.String(), 128)
			out[name] = s.value(rv.MapIndex(mapKey).Interface(), name, depth+1)
		}
		if rv.Len() > len(keys) {
			out["_omitted_items"] = rv.Len() - len(keys)
		}
		return out
	case reflect.Slice, reflect.Array:
		limit := rv.Len()
		if limit > debugProtocolMaxCollectionSize {
			limit = debugProtocolMaxCollectionSize
		}
		out := make([]any, 0, limit+1)
		for i := 0; i < limit; i++ {
			out = append(out, s.value(rv.Index(i).Interface(), "", depth+1))
		}
		if rv.Len() > limit {
			out = append(out, map[string]any{"_omitted_items": rv.Len() - limit})
		}
		return out
	case reflect.Struct:
		typeInfo := rv.Type()
		out := make(map[string]any)
		for i := 0; i < rv.NumField() && len(out) < debugProtocolMaxCollectionSize; i++ {
			fieldInfo := typeInfo.Field(i)
			field := rv.Field(i)
			if !fieldInfo.IsExported() || !field.CanInterface() {
				continue
			}
			name := fieldInfo.Name
			if tag := strings.Split(fieldInfo.Tag.Get("json"), ",")[0]; tag != "" {
				if tag == "-" {
					continue
				}
				name = tag
			}
			out[name] = s.value(field.Interface(), name, depth+1)
		}
		return out
	default:
		return "[unsupported value]"
	}
}

func (s *debugProtocolSanitizer) rawMessage(raw []byte, depth int) any {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > debugProtocolInspectMaxBytes {
		return map[string]any{"_omitted_bytes": len(raw)}
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return map[string]any{"_invalid_json_bytes": len(raw)}
	}
	return s.value(value, "", depth)
}

func debugProtocolSensitiveKey(key string) bool {
	normalized := strings.ToLower(key)
	normalized = strings.NewReplacer("_", "", "-", "", ".", "").Replace(normalized)
	switch normalized {
	case "data", "description", "env", "environment", "error", "expression", "headers",
		"message", "output", "result", "text", "url", "value":
		return true
	}
	for _, fragment := range []string{
		"apikey", "authorization", "cookie", "credential", "password", "passwd",
		"privatekey", "secret", "sessionkey", "token",
	} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func truncateDebugProtocolString(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	const marker = "...[truncated]"
	if maxBytes <= len(marker) {
		return marker[:maxBytes]
	}
	cut := maxBytes - len(marker)
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut] + marker
}

// FunctionBreakpoint is a breakpoint on a function name (DAP setFunctionBreakpoints).
type FunctionBreakpoint struct {
	Name         string `json:"name"`
	Condition    string `json:"condition,omitempty"`
	HitCondition string `json:"hitCondition,omitempty"`
}

// InlineValue is a simplified DAP InlineValue entry (prompt-5).
type InlineValue struct {
	Type              string `json:"type"`
	Text              string `json:"text,omitempty"`
	VariableReference int    `json:"variableReference,omitempty"`
}

// ---------------------------------------------------------------------------
// F-5 (prompt-2.md): Data Breakpoint types
// ---------------------------------------------------------------------------

// DataBreakpointInfo describes a variable that can have a data breakpoint set
// on it. Returned by DataBreakpointInfo() → DAP dataBreakpointInfoRequest.
type DataBreakpointInfo struct {
	DataID      string   `json:"dataId"`
	Description string   `json:"description"`
	AccessTypes []string `json:"accessTypes,omitempty"` // "read"/"write"/"readWrite"
	CanPersist  bool     `json:"canPersist,omitempty"`
}

// DataBreakpoint represents a data breakpoint set on a variable's dataId.
// Sent to the debugger via SetDataBreakpoints() → DAP setDataBreakpointsRequest.
type DataBreakpoint struct {
	DataID       string `json:"dataId"`
	AccessType   string `json:"accessType"` // "read"/"write"/"readWrite"
	Condition    string `json:"condition,omitempty"`
	HitCondition string `json:"hitCondition,omitempty"`
}

// ---------------------------------------------------------------------------
// F-7 (prompt-2.md): Debug auxiliary types
// ---------------------------------------------------------------------------

// ExceptionInfoResp describes an exception that caused the debuggee to stop.
// Returned by ExceptionInfo() → DAP exceptionInfoRequest.
type ExceptionInfoResp struct {
	ExceptionID string            `json:"exceptionId"`
	Description string            `json:"description"`
	BreakMode   string            `json:"breakMode"` // "never"/"always"/"unhandled"/"userUnhandled"
	Details     *ExceptionDetails `json:"details,omitempty"`
}

// ExceptionDetails provides extended exception information.
type ExceptionDetails struct {
	Message        string            `json:"message,omitempty"`
	TypeName       string            `json:"typeName,omitempty"`
	FullTypeName   string            `json:"fullTypeName,omitempty"`
	StackTrace     string            `json:"stackTrace,omitempty"`
	InnerException *ExceptionDetails `json:"innerException,omitempty"`
}

// DebugSource describes a source file loaded in the debugger.
type DebugSource struct {
	Name            string `json:"name"`
	Path            string `json:"path"`
	SourceReference int    `json:"sourceReference,omitempty"`
}

// DebugModule describes a module loaded in the debugger.
type DebugModule struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path,omitempty"`
	Version      string `json:"version,omitempty"`
	SymbolStatus string `json:"symbolStatus,omitempty"`
}

// DebugCompletionItem represents a completion item in the debug console.
type DebugCompletionItem struct {
	Label  string `json:"label"`
	Text   string `json:"text,omitempty"`
	Type   string `json:"type,omitempty"`
	Start  int    `json:"start,omitempty"`
	Length int    `json:"length,omitempty"`
}

// StepInTarget represents a specific target that can be stepped into
// (e.g. a specific overload among multiple call targets on one line).
type StepInTarget struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

// BreakpointLocation represents a valid location where a breakpoint can be set.
type BreakpointLocation struct {
	Line      int `json:"line"`
	EndLine   int `json:"endLine,omitempty"`
	Column    int `json:"column,omitempty"`
	EndColumn int `json:"endColumn,omitempty"`
}

// DebugConfig is the session-start configuration (alias of DebugLaunchConfig).
type DebugConfig = DebugLaunchConfig

// debugLaunchSpec remembers how to restart the current configuration.
type debugLaunchSpec struct {
	Kind           string // package | test | node | browser
	AdapterID      string
	Dir            string
	RunRegex       string
	Program        string
	Args           []string
	Env            map[string]string
	Mode           string // debug | test
	StopEntry      bool
	Request        string
	Browser        string
	ExecutablePath string
	URL            string
	Address        string
	RuntimeArgs    []string
	TargetID       string
	WebRoot        string
	SourceMaps     bool
	PathMappings   map[string]string
}

// DebugBreakpoint is a source breakpoint (prompt-12: condition + verified UI).
type DebugBreakpoint struct {
	ID         int    `json:"id"`
	File       string `json:"file"`
	Line       int    `json:"line"` // 1-based
	Verified   bool   `json:"verified"`
	Condition  string `json:"condition,omitempty"`
	LogMessage string `json:"logMessage,omitempty"` // logpoint text
	Message    string `json:"message,omitempty"`    // adapter message when unverified
}

// DebugStackFrame is one stack frame.
type DebugStackFrame struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	File             string `json:"file"`
	Line             int    `json:"line"`
	Column           int    `json:"column"`
	PresentationHint string `json:"presentationHint,omitempty"`
	AsyncBoundary    bool   `json:"asyncBoundary,omitempty"`
}

// DebugAdapterCapabilities is the subset of adapter capabilities used by the
// call-stack UI. An omitted capability remains false and is never inferred.
type DebugAdapterCapabilities struct {
	SupportsDelayedStackTraceLoading bool `json:"supportsDelayedStackTraceLoading"`
}

// DebugStackPage is one adapter-provided DAP stackTrace page.
type DebugStackPage struct {
	Generation  uint64            `json:"generation"`
	Frames      []DebugStackFrame `json:"frames"`
	TotalFrames int               `json:"totalFrames"`
	HasMore     bool              `json:"hasMore"`
}

// DebugAsyncStackSegment is one CDP Runtime.StackTrace segment. ParentID is an
// opaque, generation-bound continuation understood only by DebugService.
type DebugAsyncStackSegment struct {
	Generation  uint64            `json:"generation"`
	ID          string            `json:"id"`
	Description string            `json:"description"`
	Frames      []DebugStackFrame `json:"frames"`
	ParentID    string            `json:"parentId,omitempty"`
}

// DebugVariable is a local or scope variable.
type DebugVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  string `json:"type"`
	// G14: the adapter-owned variablesReference. Preserved from the real DAP
	// scopes/variables responses so the UI can expand nested variables and
	// target setVariable/dataBreakpointInfo at the correct reference instead
	// of a hardcoded id. 0 means the variable has no children.
	VariablesReference int `json:"variablesReference"`
}

// DebugSessionInfo is returned after launch / status queries.
type DebugSessionInfo struct {
	Running           bool   `json:"running"`
	Address           string `json:"address"`
	Mode              string `json:"mode"`
	Message           string `json:"message"`
	Stopped           bool   `json:"stopped"`
	StopReason        string `json:"stopReason"`
	ThreadID          int    `json:"threadId"`
	AdapterID         string `json:"adapterId,omitempty"`
	SourcePackID      string `json:"sourcePackId,omitempty"`
	SourcePackVersion string `json:"sourcePackVersion,omitempty"`
}

// DebugStateSnapshot is polled by the frontend for stack/locals/bps/watches.
type DebugStateSnapshot struct {
	Session                          DebugSessionInfo      `json:"session"`
	Breakpoints                      []DebugBreakpoint     `json:"breakpoints"`
	Stack                            []DebugStackFrame     `json:"stack"`
	Locals                           []DebugVariable       `json:"locals"`
	Watches                          []DebugVariable       `json:"watches"`
	StopReason                       string                `json:"stopReason"`
	LastError                        string                `json:"lastError,omitempty"` // 13-C: condition/watch/eval errors
	Generation                       uint64                `json:"generation"`
	StackTotalFrames                 int                   `json:"stackTotalFrames"`
	StackHasMore                     bool                  `json:"stackHasMore"`
	SupportsDelayedStackTraceLoading bool                  `json:"supportsDelayedStackTraceLoading"`
	SupportsAsyncStackTrace          bool                  `json:"supportsAsyncStackTrace"`
	AsyncStackRootID                 string                `json:"asyncStackRootId,omitempty"`
	BrowserTargets                   []BrowserTarget       `json:"browserTargets,omitempty"`
	BrowserTargetID                  string                `json:"browserTargetId,omitempty"`
	BrowserConsole                   []BrowserConsoleEntry `json:"browserConsole,omitempty"`
	BrowserNetwork                   []BrowserNetworkEntry `json:"browserNetwork,omitempty"`
}

// DebugLaunchConfig is a persisted launch profile (prompt-12 12-G).
type DebugLaunchConfig struct {
	Name           string            `json:"name"`
	Kind           string            `json:"kind"` // package | test | node | browser | language-pack
	AdapterID      string            `json:"adapterId,omitempty"`
	Dir            string            `json:"dir"`
	Program        string            `json:"program,omitempty"`
	RunRegex       string            `json:"runRegex,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	StopEntry      bool              `json:"stopOnEntry,omitempty"`
	Mode           string            `json:"mode,omitempty"` // debug | test (delve)
	Request        string            `json:"request,omitempty"`
	Browser        string            `json:"browser,omitempty"`
	ExecutablePath string            `json:"executablePath,omitempty"`
	URL            string            `json:"url,omitempty"`
	Address        string            `json:"address,omitempty"`
	RuntimeArgs    []string          `json:"runtimeArgs,omitempty"`
	TargetID       string            `json:"targetId,omitempty"`
	WebRoot        string            `json:"webRoot,omitempty"`
	SourceMaps     bool              `json:"sourceMaps,omitempty"`
	PathMappings   map[string]string `json:"pathMappings,omitempty"`
}

// dapMessage is a subset of the Debug Adapter Protocol message envelope.
type dapMessage struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"` // request | response | event
	Command    string          `json:"command,omitempty"`
	RequestSeq int             `json:"request_seq,omitempty"`
	Success    bool            `json:"success,omitempty"`
	Message    string          `json:"message,omitempty"`
	Event      string          `json:"event,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
}

type dapRunRequest func(string, map[string]interface{}) (json.RawMessage, error)
type debugRunEvaluate func(string) (DebugVariable, error)

type nodeRunEvaluator interface {
	Evaluate(string) (DebugVariable, error)
}

func isCDPDebugMode(mode string) bool {
	return mode == "node" || mode == "browser"
}

const (
	maxBrowserConsoleEntries = 500
	maxBrowserNetworkEntries = 500
	browserConnectTimeout    = 8 * time.Second
	browserStopTimeout       = 2 * time.Second
)

var debugThreadsRunCounter uint64

// newDebugSession creates a fresh DebugSession with an empty pending map.
func newDebugSession() *DebugSession {
	return &DebugSession{
		pending:                 make(map[int]chan dapMessage),
		asyncStackContinuations: make(map[string]nodeAsyncStackContinuation),
	}
}

func (d *DebugService) bindSession(session *DebugSession) *DebugSession {
	if session != nil {
		session.protocolLog = d.emitDebugProtocol
	}
	return session
}

func (d *DebugService) activeSession() *DebugSession {
	if d == nil {
		return nil
	}
	d.sessionsMu.RLock()
	session := d.DebugSession
	d.sessionsMu.RUnlock()
	return session
}

func (s *DebugSession) beginRunLocked() uint64 {
	s.runGeneration++
	s.renewDebugThreadsRunLocked()
	s.browserTargetEpoch++
	s.supportsDelayedStackTraceLoading = false
	s.supportsAsyncStackTrace = false
	s.stackTotalFrames = 0
	s.stackHasMore = false
	s.asyncStackRootID = ""
	s.asyncStackCounter = 0
	s.asyncStackContinuations = make(map[string]nodeAsyncStackContinuation)
	s.browserLaunch = nil
	s.browserConfig = browserDebugSpec{}
	s.browserTargets = nil
	s.browserTargetID = ""
	s.browserConsole = nil
	s.browserNetwork = nil
	s.dapInitialized = make(chan struct{})
	s.dapInitializedOnce = new(sync.Once)
	return s.runGeneration
}

func (s *DebugSession) renewDebugThreadsRunLocked() {
	runNumber := atomic.AddUint64(&debugThreadsRunCounter, 1)
	s.debugThreadsRunID = "debug-run-" + strconv.FormatUint(runNumber, 10)
	s.debugThreadsStateRevision++
}

func (s *DebugSession) touchDebugThreadsStateLocked() {
	s.debugThreadsStateRevision++
}

func (s *DebugSession) clearAsyncStackLocked() {
	s.asyncStackRootID = ""
	s.asyncStackContinuations = make(map[string]nodeAsyncStackContinuation)
	s.stackHasMore = false
}

func (s *DebugSession) finishDAPRun(generation uint64, conn net.Conn) {
	s.mu.Lock()
	if s.runGeneration != generation || s.conn != conn {
		s.mu.Unlock()
		return
	}
	resources := s.cleanupLocked()
	s.mu.Unlock()
	if err := s.closeDetachedResources(resources, false, true, false); err != nil {
		slog.Debug("debug: cleanup failed dap run", "err", err)
	}
}

func dapRunCurrent(owner *DebugSession, generation uint64, conn net.Conn) bool {
	if owner == nil {
		return false
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.runGeneration == generation && owner.conn == conn
}

// NewDebugService creates the debug service with a default ("default") session
// that is the initially active session (prompt-5 multi-session).
func NewDebugService() *DebugService {
	return newDebugService(nil)
}

// NewDebugServiceWithWorkspaceContext creates the renderer-facing debug
// service. Launches resolve the active workspace at call time and fail closed
// while no project is open.
func NewDebugServiceWithWorkspaceContext(workspaceContext *WorkspaceContext) *DebugService {
	return newDebugService(workspaceContext)
}

func newDebugService(workspaceContext *WorkspaceContext) *DebugService {
	sess := newDebugSession()
	d := &DebugService{
		DebugSession:     sess,
		workspaceContext: workspaceContext,
		sessions:         map[string]*DebugSession{"default": sess},
		activeSessionID:  "default",
		browserEnumerate: waitForBrowserTargets,
		browserConnect:   connectBrowserTarget,
	}
	d.bindSession(sess)
	if enabled, err := loadDebugProtocolLogSetting(); err != nil {
		slog.Debug("load debug protocol setting failed", "err", err)
	} else {
		d.debugProtocolLog.Store(enabled)
	}
	return d
}

// GetActiveSession returns the currently active session ID (prompt-5).
// 返回 session ID 字符串而非 *DebugSession 指针，便于 Wails JSON 序列化
// (DebugSession 含 sync.Mutex 等不可序列化字段)。
func (d *DebugService) GetActiveSession() string {
	d.sessionsMu.RLock()
	defer d.sessionsMu.RUnlock()
	return d.activeSessionID
}

// DebugSessionListItem 描述调试会话的简略状态 (prompt-5)。
type DebugSessionListItem struct {
	ID      string `json:"id"`
	Active  bool   `json:"active"`
	Running bool   `json:"running"`
	Stopped bool   `json:"stopped"`
	Mode    string `json:"mode"`
	Address string `json:"address"`
}

// ListSessions enumerates all debug sessions for the session switcher UI (prompt-5).
func (d *DebugService) ListSessions() []DebugSessionListItem {
	type sessionEntry struct {
		id      string
		session *DebugSession
	}
	d.sessionsMu.RLock()
	active := d.activeSessionID
	entries := make([]sessionEntry, 0, len(d.sessions))
	for id, s := range d.sessions {
		entries = append(entries, sessionEntry{id: id, session: s})
	}
	d.sessionsMu.RUnlock()

	out := make([]DebugSessionListItem, 0, len(entries))
	for _, entry := range entries {
		id, s := entry.id, entry.session
		s.mu.Lock()
		out = append(out, DebugSessionListItem{
			ID:      id,
			Active:  id == active,
			Running: s.running,
			Stopped: s.stopped,
			Mode:    s.mode,
			Address: s.addr,
		})
		s.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SetActiveSession switches the active session to sessionID (prompt-5).
func (d *DebugService) SetActiveSession(sessionID string) error {
	d.sessionsMu.Lock()
	defer d.sessionsMu.Unlock()
	sess, ok := d.sessions[sessionID]
	if !ok {
		return fmt.Errorf("unknown debug session %q", sessionID)
	}
	d.DebugSession = sess
	d.activeSessionID = sessionID
	return nil
}

func (d *DebugService) debugThreadsSessionLocked(sessionID string) (string, *DebugSession, error) {
	if sessionID == "" {
		sessionID = d.activeSessionID
	}
	owner := d.sessions[sessionID]
	if owner == nil {
		return "", nil, fmt.Errorf("unknown debug session %q", sessionID)
	}
	return sessionID, owner, nil
}

// Snapshot implements DebugThreadsBackend with an owner-bound, atomic view.
func (d *DebugService) Snapshot(sessionID string) (DebugThreadsSessionSnapshot, error) {
	if d == nil {
		return DebugThreadsSessionSnapshot{}, fmt.Errorf("debug service is unavailable")
	}
	d.sessionsMu.Lock()
	sessionID, owner, err := d.debugThreadsSessionLocked(sessionID)
	if err != nil {
		d.sessionsMu.Unlock()
		return DebugThreadsSessionSnapshot{}, err
	}
	owner.mu.Lock()
	if !owner.running || owner.debugThreadsRunID == "" {
		owner.mu.Unlock()
		d.sessionsMu.Unlock()
		return DebugThreadsSessionSnapshot{}, fmt.Errorf("debug session %q is not running", sessionID)
	}
	snapshot := DebugThreadsSessionSnapshot{
		SessionID:     sessionID,
		RunID:         owner.debugThreadsRunID,
		Generation:    owner.runGeneration,
		StateRevision: owner.debugThreadsStateRevision,
		Stopped:       owner.stopped,
		ThreadID:      owner.threadID,
		StopReason:    owner.stopReason,
	}
	owner.mu.Unlock()
	d.sessionsMu.Unlock()
	return snapshot, nil
}

// Request implements DebugThreadsBackend. It reserves the DAP request while
// both the session owner mapping and run identity are still atomically valid.
func (d *DebugService) Request(
	run DebugThreadsRunIdentity,
	command string,
	args map[string]any,
) (json.RawMessage, error) {
	if d == nil {
		return nil, fmt.Errorf("debug service is unavailable")
	}
	if run.SessionID == "" || run.RunID == "" {
		return nil, ErrDebugThreadsStaleRun
	}
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("dap command is required")
	}

	d.sessionsMu.Lock()
	owner := d.sessions[run.SessionID]
	if owner == nil {
		d.sessionsMu.Unlock()
		return nil, ErrDebugThreadsStaleRun
	}
	owner.mu.Lock()
	if !owner.running || owner.runGeneration != run.Generation || owner.debugThreadsRunID != run.RunID {
		owner.mu.Unlock()
		d.sessionsMu.Unlock()
		return nil, ErrDebugThreadsStaleRun
	}
	conn := owner.conn
	if conn == nil {
		owner.mu.Unlock()
		d.sessionsMu.Unlock()
		return nil, fmt.Errorf("debug session %q has no dap connection", run.SessionID)
	}
	seq, response := reserveDAPPendingRequestLocked(owner)
	owner.mu.Unlock()
	d.sessionsMu.Unlock()

	body, err := d.completeDAPRequest(owner, run.Generation, conn, seq, response, command, args)
	if err != nil {
		return nil, err
	}
	d.sessionsMu.Lock()
	mapped := d.sessions[run.SessionID] == owner
	owner.mu.Lock()
	current := mapped && owner.runGeneration == run.Generation && owner.debugThreadsRunID == run.RunID
	owner.mu.Unlock()
	d.sessionsMu.Unlock()
	if !current {
		return nil, ErrDebugThreadsStaleRun
	}
	return body, nil
}

// ApplySessionUpdate implements DebugThreadsBackend with atomic stale-run and
// stale-revision rejection.
func (d *DebugService) ApplySessionUpdate(
	expected DebugThreadsSessionSnapshot,
	update DebugThreadsSessionUpdate,
) error {
	if d == nil {
		return fmt.Errorf("debug service is unavailable")
	}
	d.sessionsMu.Lock()
	owner := d.sessions[expected.SessionID]
	if owner == nil {
		d.sessionsMu.Unlock()
		return ErrDebugThreadsStaleRun
	}
	owner.mu.Lock()
	if !owner.running || owner.runGeneration != expected.Generation || owner.debugThreadsRunID != expected.RunID {
		owner.mu.Unlock()
		d.sessionsMu.Unlock()
		return ErrDebugThreadsStaleRun
	}
	if owner.debugThreadsStateRevision != expected.StateRevision {
		owner.mu.Unlock()
		d.sessionsMu.Unlock()
		return ErrDebugThreadsStaleState
	}
	if update.ThreadID != nil {
		owner.threadID = *update.ThreadID
	}
	if update.Stopped != nil {
		owner.stopped = *update.Stopped
	}
	if update.StopReason != nil {
		owner.stopReason = *update.StopReason
	}
	if update.ClearLocals {
		owner.locals = nil
	}
	if update.ClearAsyncStack {
		owner.clearAsyncStackLocked()
	}
	if update.ReplaceStack {
		owner.stack = append([]DebugStackFrame(nil), update.Stack...)
		owner.stackTotalFrames = update.StackTotal
		if owner.stackTotalFrames < len(owner.stack) {
			owner.stackTotalFrames = len(owner.stack)
		}
		owner.stackHasMore = update.StackHasMore
	}
	owner.touchDebugThreadsStateLocked()
	owner.mu.Unlock()
	d.sessionsMu.Unlock()
	return nil
}

// StartSession creates a new debug session, stores its launch config, makes it
// active, and attempts to launch per the config (prompt-5). Returns the new
// session ID. If launch fails, the session slot persists (state isolation is
// still testable) and the error is returned alongside the ID.
func (d *DebugService) StartSession(config DebugConfig) (string, error) {
	if config.Kind == "" {
		config.Kind = "package"
	}
	d.sessionsMu.Lock()
	if d.closed {
		d.sessionsMu.Unlock()
		return "", fmt.Errorf("debug service is shut down: %w", ErrInvalidInput)
	}
	id := d.allocSessionIDLocked()
	sess := d.bindSession(newDebugSession())
	sess.lastLaunch = debugLaunchSpec{
		Kind: config.Kind, AdapterID: config.AdapterID, Dir: config.Dir, RunRegex: config.RunRegex,
		Program: config.Program, Args: config.Args, Env: config.Env,
		StopEntry: config.StopEntry, Mode: config.Mode,
		Request: config.Request, Browser: config.Browser, ExecutablePath: config.ExecutablePath,
		URL: config.URL, Address: config.Address, RuntimeArgs: config.RuntimeArgs,
		TargetID: config.TargetID, WebRoot: config.WebRoot, SourceMaps: config.SourceMaps,
		PathMappings: config.PathMappings,
	}
	if sess.lastLaunch.Kind == "" {
		sess.lastLaunch.Kind = "package"
	}
	d.sessions[id] = sess
	d.DebugSession = sess
	d.activeSessionID = id
	d.sessionsMu.Unlock()

	_, err := d.LaunchWithConfig(config)
	if err != nil {
		return id, err
	}
	return id, nil
}

// allocSessionIDLocked returns a unique session ID. Caller holds sessionsMu.
func (d *DebugService) allocSessionIDLocked() string {
	for {
		n := int(atomic.AddInt64(&d.sessionCounter, 1))
		id := fmt.Sprintf("sess-%d", n)
		if _, exists := d.sessions[id]; !exists {
			return id
		}
	}
}

// StopSession stops and removes the given session (prompt-5). If it was the
// active session, a replacement active session is selected (or a fresh empty
// default session is created when none remain).
func (d *DebugService) StopSession(sessionID string) error {
	d.sessionsMu.Lock()
	sess, ok := d.sessions[sessionID]
	if !ok {
		d.sessionsMu.Unlock()
		return fmt.Errorf("unknown debug session %q", sessionID)
	}
	delete(d.sessions, sessionID)
	isActive := d.DebugSession == sess
	if isActive {
		var nextID string
		var nextSess *DebugSession
		for nid, ns := range d.sessions {
			nextID, nextSess = nid, ns
			break
		}
		if nextSess == nil {
			nextSess = d.bindSession(newDebugSession())
			nextID = "default"
			d.sessions[nextID] = nextSess
		}
		d.DebugSession = nextSess
		d.activeSessionID = nextID
	}
	d.sessionsMu.Unlock()

	return sess.stopAndDispose()
}

// IsAvailable reports whether the built-in Go language-pack debugger is on PATH.
func (d *DebugService) IsAvailable() bool {
	debugger, ok := builtInLanguagePackDebuggerForLanguage("go")
	return ok && debugger.Protocol == "dap" && lookPathExists(debugger.Executable)
}

// StatusMessage returns a user-facing status string.
func (d *DebugService) StatusMessage() string {
	owner := d.activeSession()
	if owner == nil {
		return debugIdleStatusMessage(d.IsAvailable())
	}
	owner.mu.Lock()
	running := owner.running
	stopped := owner.stopped
	stopReason := owner.stopReason
	addr := owner.addr
	mode := owner.mode
	owner.mu.Unlock()
	if running {
		if stopped {
			return fmt.Sprintf("Debugging paused (%s) on %s", stopReason, addr)
		}
		return fmt.Sprintf("Debugging active (%s) DAP %s", mode, addr)
	}
	if d.IsAvailable() {
		return "Delve available — F5 / Debug Package for in-IDE DAP"
	}
	return "Delve not installed (go install github.com/go-delve/delve/cmd/dlv@latest)"
}

// IsRunning reports whether a DAP session is active.
func (d *DebugService) IsRunning() bool {
	owner := d.activeSession()
	if owner == nil {
		return false
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.running
}

// GetSession returns current session state.
func (d *DebugService) GetSession() DebugSessionInfo {
	owner := d.activeSession()
	if owner == nil {
		return DebugSessionInfo{Running: false, Message: debugIdleStatusMessage(d.IsAvailable())}
	}
	owner.mu.Lock()
	session := debugSessionInfoLocked(owner)
	owner.mu.Unlock()
	if !session.Running {
		session.Message = debugIdleStatusMessage(d.IsAvailable())
	}
	return session
}

func debugSessionInfoLocked(owner *DebugSession) DebugSessionInfo {
	if owner.running {
		msg := fmt.Sprintf("DAP session on %s (%s)", owner.addr, owner.mode)
		if owner.stopped {
			msg = fmt.Sprintf("Paused: %s — %s", owner.stopReason, owner.addr)
		}
		return DebugSessionInfo{
			Running: owner.running, Address: owner.addr, Mode: owner.mode, Message: msg,
			Stopped: owner.stopped, StopReason: owner.stopReason, ThreadID: owner.threadID,
			AdapterID: owner.adapterID, SourcePackID: owner.sourcePackID, SourcePackVersion: owner.sourcePackVersion,
		}
	}
	return DebugSessionInfo{Running: false}
}

func debugIdleStatusMessage(available bool) string {
	if available {
		return "Delve available — F5 / Debug Package for in-IDE DAP"
	}
	return "Delve not installed"
}

// GetState returns full snapshot for the debug panel.
func (d *DebugService) GetState() DebugStateSnapshot {
	owner := d.activeSession()
	if owner == nil {
		return DebugStateSnapshot{Session: DebugSessionInfo{Message: debugIdleStatusMessage(d.IsAvailable())}}
	}
	owner.mu.Lock()
	bps := append([]DebugBreakpoint(nil), owner.breakpoints...)
	stack := append([]DebugStackFrame(nil), owner.stack...)
	locals := append([]DebugVariable(nil), owner.locals...)
	watches := append([]DebugVariable(nil), owner.watchValues...)
	browserTargets := append([]BrowserTarget(nil), owner.browserTargets...)
	browserConsole := append([]BrowserConsoleEntry(nil), owner.browserConsole...)
	browserNetwork := append([]BrowserNetworkEntry(nil), owner.browserNetwork...)
	snapshot := DebugStateSnapshot{
		Session:                          debugSessionInfoLocked(owner),
		Breakpoints:                      bps,
		Stack:                            stack,
		Locals:                           locals,
		Watches:                          watches,
		StopReason:                       owner.stopReason,
		LastError:                        owner.lastError,
		Generation:                       owner.runGeneration,
		StackTotalFrames:                 owner.stackTotalFrames,
		StackHasMore:                     owner.stackHasMore,
		SupportsDelayedStackTraceLoading: owner.supportsDelayedStackTraceLoading,
		SupportsAsyncStackTrace:          owner.supportsAsyncStackTrace,
		AsyncStackRootID:                 owner.asyncStackRootID,
		BrowserTargets:                   browserTargets,
		BrowserTargetID:                  owner.browserTargetID,
		BrowserConsole:                   browserConsole,
		BrowserNetwork:                   browserNetwork,
	}
	owner.mu.Unlock()
	if !snapshot.Session.Running {
		snapshot.Session.Message = debugIdleStatusMessage(d.IsAvailable())
	}
	return snapshot
}
