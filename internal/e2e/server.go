//go:build e2e

// Package e2e hosts the packaged-build E2E automation endpoint. The endpoint
// is compiled ONLY when the explicit `e2e` build tag is present; every normal
// build compiles the empty stub in stub.go instead. Even in an e2e build the
// server stays dormant unless KOYORI_IDE_E2E=1 is set, listens on loopback only,
// and rotates a 256-bit bearer token after every request.
//
// The root main package adapts its service bundle to e2e.ServiceSet at the
// call site (main.go), so this package never depends on package main.
package e2e

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	envOptIn                       = "KOYORI_IDE_E2E"
	envToken                       = "KOYORI_IDE_E2E_TOKEN"
	envHandshake                   = "KOYORI_IDE_E2E_HANDSHAKE"
	envRunID                       = "KOYORI_IDE_E2E_RUN_ID"
	windowID                       = "packaged-e2e"
	httpClientResultEvent          = "e2e:http-client-result"
	recoveryResultEvent            = "e2e:recovery-result"
	workspaceResultEvent           = "e2e:g05-workspace-result"
	runtimeRoleResultEvent         = "e2e:g06-runtime-role-result"
	monacoResultEvent              = "e2e:g10-monaco-result"
	extensionAPIResultEvent        = "e2e:g13-extension-api-result"
	testExplorerResultEvent        = "e2e:g15-test-explorer-result"
	terminalReconnectResultEvent   = "e2e:g16-terminal-reconnect-result"
	extensionHostG24ResultEvent    = "e2e:g24-extension-host-result"
	agentToolRoundResultEvent      = "e2e:agent-tool-round-result"
	conversationHandoffResultEvent = "e2e:conversation-handoff-result"
	bodyLimit                      = 2 << 20
)

type handshake struct {
	URL       string `json:"url"`
	PID       int    `json:"pid"`
	StartedAt string `json:"startedAt"`
	RunID     string `json:"runId"`
}

type command struct {
	Action             string `json:"action"`
	Workspace          string `json:"workspace,omitempty"`
	SecondaryWorkspace string `json:"secondaryWorkspace,omitempty"`
	Path               string `json:"path,omitempty"`
	Content            string `json:"content,omitempty"`
	Marker             string `json:"marker,omitempty"`
	Replacement        string `json:"replacement,omitempty"`
	PresetName         string `json:"presetName,omitempty"`
	BaselineHash       string `json:"baselineHash,omitempty"`
	WindowID           string `json:"windowId,omitempty"`
	Command            string `json:"command,omitempty"`
	Expected           string `json:"expected,omitempty"`
	Language           string `json:"language,omitempty"`
	CompletionLine     int    `json:"completionLine,omitempty"`
	CompletionColumn   int    `json:"completionColumn,omitempty"`
	HoverLine          int    `json:"hoverLine,omitempty"`
	HoverColumn        int    `json:"hoverColumn,omitempty"`
	PrimaryOrigin      string `json:"primaryOrigin,omitempty"`
	SecondaryOrigin    string `json:"secondaryOrigin,omitempty"`
	PublicURL          string `json:"publicUrl,omitempty"`
	ProbeMode          string `json:"probeMode,omitempty"`
	CrashContent       string `json:"crashContent,omitempty"`
	PendingContent     string `json:"pendingContent,omitempty"`
}

type response struct {
	OK     bool        `json:"ok"`
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

type server struct {
	services     ServiceSet
	mu           sync.Mutex
	token        string
	probeMu      sync.Mutex
	probeResults map[string]chan map[string]interface{}
}

type rendererExecutor func(string) bool

func mainRendererExecutor(execute func(string)) rendererExecutor {
	return func(script string) bool {
		if execute == nil {
			return false
		}
		execute(script)
		return true
	}
}

// Start launches the loopback-only E2E automation server when KOYORI_IDE_E2E=1
// is present, writes the handshake file, and returns a cleanup func. It
// returns (nil, nil) when the opt-in env var is unset — mirroring the stub's
// behavior so callers do not need to know which build they run in.
func Start(set ServiceSet) (func(), error) {
	if os.Getenv(envOptIn) != "1" {
		return nil, nil
	}
	if set.Project == nil || set.File == nil || set.Terminal == nil ||
		set.LSP == nil || set.Recovery == nil {
		return nil, errors.New("E2E automation services are not fully wired")
	}

	token := os.Getenv(envToken)
	if err := validateToken(token); err != nil {
		return nil, err
	}
	runID := os.Getenv(envRunID)
	if err := validateRunID(runID); err != nil {
		return nil, err
	}

	handshakePath := os.Getenv(envHandshake)
	if handshakePath == "" || !filepath.IsAbs(handshakePath) {
		return nil, errors.New("KOYORI_IDE_E2E_HANDSHAKE must be an absolute path")
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for E2E automation: %w", err)
	}
	automation := &server{
		services:     set,
		token:        token,
		probeResults: make(map[string]chan map[string]interface{}),
	}
	var removeHTTPProbeListener func()
	var removeRecoveryProbeListener func()
	var removeWorkspaceProbeListener func()
	var removeRuntimeRoleProbeListener func()
	var removeMonacoProbeListener func()
	var removeExtensionAPIProbeListener func()
	var removeTestExplorerProbeListener func()
	var removeTerminalReconnectProbeListener func()
	var removeExtensionHostG24ProbeListener func()
	var removeAgentToolRoundProbeListener func()
	var removeConversationHandoffProbeListener func()
	if app := application.Get(); app != nil {
		removeHTTPProbeListener = app.Event.On(httpClientResultEvent, automation.receiveRendererProbeResult)
		removeRecoveryProbeListener = app.Event.On(recoveryResultEvent, automation.receiveRendererProbeResult)
		removeWorkspaceProbeListener = app.Event.On(workspaceResultEvent, automation.receiveRendererProbeResult)
		removeRuntimeRoleProbeListener = app.Event.On(runtimeRoleResultEvent, automation.receiveRendererProbeResult)
		removeMonacoProbeListener = app.Event.On(monacoResultEvent, automation.receiveRendererProbeResult)
		removeExtensionAPIProbeListener = app.Event.On(extensionAPIResultEvent, automation.receiveRendererProbeResult)
		removeTestExplorerProbeListener = app.Event.On(testExplorerResultEvent, automation.receiveRendererProbeResult)
		removeTerminalReconnectProbeListener = app.Event.On(terminalReconnectResultEvent, automation.receiveRendererProbeResult)
		removeExtensionHostG24ProbeListener = app.Event.On(extensionHostG24ResultEvent, automation.receiveRendererProbeResult)
		removeAgentToolRoundProbeListener = app.Event.On(agentToolRoundResultEvent, automation.receiveRendererProbeResult)
		removeConversationHandoffProbeListener = app.Event.On(conversationHandoffResultEvent, automation.receiveRendererProbeResult)
	}
	srv := &http.Server{
		Handler:           automation,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      180 * time.Second,
		IdleTimeout:       15 * time.Second,
	}
	hs := handshake{
		URL:       "http://" + listener.Addr().String(),
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		RunID:     runID,
	}
	if err := writeHandshake(handshakePath, hs); err != nil {
		_ = listener.Close()
		return nil, err
	}

	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("E2E automation server stopped", "err", err)
		}
	}()
	slog.Info("E2E automation listening on loopback", "address", listener.Addr().String(), "runId", runID)

	var once sync.Once
	return func() {
		once.Do(func() {
			if removeHTTPProbeListener != nil {
				removeHTTPProbeListener()
			}
			if removeRecoveryProbeListener != nil {
				removeRecoveryProbeListener()
			}
			if removeWorkspaceProbeListener != nil {
				removeWorkspaceProbeListener()
			}
			if removeMonacoProbeListener != nil {
				removeMonacoProbeListener()
			}
			if removeExtensionAPIProbeListener != nil {
				removeExtensionAPIProbeListener()
			}
			if removeTestExplorerProbeListener != nil {
				removeTestExplorerProbeListener()
			}
			if removeTerminalReconnectProbeListener != nil {
				removeTerminalReconnectProbeListener()
			}
			if removeRuntimeRoleProbeListener != nil {
				removeRuntimeRoleProbeListener()
			}
			if removeExtensionHostG24ProbeListener != nil {
				removeExtensionHostG24ProbeListener()
			}
			if removeAgentToolRoundProbeListener != nil {
				removeAgentToolRoundProbeListener()
			}
			if removeConversationHandoffProbeListener != nil {
				removeConversationHandoffProbeListener()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
			_ = os.Remove(handshakePath)
		})
	}, nil
}

func validateToken(token string) error {
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) < 32 {
		return errors.New("KOYORI_IDE_E2E_TOKEN must contain at least 32 random bytes encoded as hex")
	}
	return nil
}

func nextToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate next E2E token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func validateRunID(runID string) error {
	if len(runID) != 64 || runID != strings.ToLower(runID) {
		return errors.New("KOYORI_IDE_E2E_RUN_ID must contain 32 random bytes encoded as lowercase hex")
	}
	decoded, err := hex.DecodeString(runID)
	if err != nil || len(decoded) != 32 {
		return errors.New("KOYORI_IDE_E2E_RUN_ID must contain 32 random bytes encoded as lowercase hex")
	}
	var nonZero byte
	for _, value := range decoded {
		nonZero |= value
	}
	if nonZero == 0 {
		return errors.New("KOYORI_IDE_E2E_RUN_ID must not be all zeroes")
	}
	return nil
}

func writeHandshake(path string, hs handshake) error {
	data, err := json.Marshal(hs)
	if err != nil {
		return fmt.Errorf("encode E2E handshake: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create E2E handshake directory: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write E2E handshake: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish E2E handshake: %w", err)
	}
	return nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost || r.URL.Path != "/v1/command" {
		writeJSON(w, http.StatusNotFound, response{OK: false, Error: "not found"})
		return
	}

	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	authorized := subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) == 1
	if !authorized {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, response{OK: false, Error: "unauthorized"})
		return
	}
	next, err := nextToken()
	if err != nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, response{OK: false, Error: err.Error()})
		return
	}
	s.token = next
	s.mu.Unlock()
	w.Header().Set("X-Koyori-IDE-E2E-Token", next)

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, bodyLimit))
	decoder.DisallowUnknownFields()
	var cmd command
	if err := decoder.Decode(&cmd); err != nil {
		writeJSON(w, http.StatusBadRequest, response{OK: false, Error: "invalid command: " + err.Error()})
		return
	}
	if err := ensureEOF(decoder); err != nil {
		writeJSON(w, http.StatusBadRequest, response{OK: false, Error: err.Error()})
		return
	}
	result, err := s.execute(cmd)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, response{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response{OK: true, Result: result})
}

func ensureEOF(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid command: multiple JSON values")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, resp response) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *server) execute(cmd command) (interface{}, error) {
	switch cmd.Action {
	case "open-workspace":
		return s.services.Project.AddProject(cmd.Workspace)
	case "open-file":
		content, err := s.services.File.ReadFile(cmd.Path)
		return map[string]interface{}{"content": content}, err
	case "create-file":
		// P9-G24: post-fault edit/save verification targets a workspace file
		// that does not exist yet. The real product creates the file on disk
		// first (CreateFile), then opens it for editing and saves through
		// WriteFileIfUnchanged against the empty-file baseline. Mirror that
		// ordering here so the packaged driver exercises the same path.
		if cmd.Path == "" {
			return nil, errors.New("create-file requires a path")
		}
		if err := s.services.File.CreateFile(cmd.Path); err != nil {
			return nil, err
		}
		return map[string]interface{}{"created": true}, nil
	case "edit":
		return s.recordDirtyBuffer(cmd)
	case "save":
		return s.saveBuffer(cmd)
	case "terminal-command":
		return s.runTerminalCommand(cmd)
	case "lsp-hover-completion":
		return s.runLSPAction(cmd)
	case "language-pack-g23-probe":
		return s.runLanguagePackG23Probe(cmd)
	case "language-pack-builtins-g23-probe":
		return s.runLanguagePackBuiltinsG23Probe(cmd)
	case "recovery-scan":
		return s.services.Recovery.ScanRecoverable()
	case "recovery-state":
		return s.services.Recovery.GetRecoveryState(), nil
	case "recovery-renderer-probe":
		return s.runRecoveryRendererProbe(cmd)
	case "recovery-guard-probe":
		return s.runRecoveryGuardProbe(cmd)
	case "native-window-close-probe":
		return s.runNativeWindowCloseProbe()
	case "http-client-renderer-probe":
		return s.runHTTPClientRendererProbe(cmd)
	case "g05-workspace-probe":
		return s.runG05WorkspaceProbe(cmd)
	case "g06-runtime-role-probe":
		return s.runG06RuntimeRoleProbe()
	case "g10-monaco-probe":
		return s.runG10MonacoProbe(cmd)
	case "search-replace":
		return s.runSearchReplace(cmd)
	case "git-diff":
		return s.runGitDiff(cmd)
	case "ai-fail-cancel":
		return s.runAIFailCancel(cmd)
	case "settings-concurrent":
		return s.runSettingsConcurrent(cmd)
	case "ai-request-context-probe":
		return s.runAIRequestContextProbe(cmd)
	case "agent-tool-round-probe":
		return s.runAgentToolRoundProbe(cmd)
	case "conversation-handoff-probe":
		return s.runConversationHandoffProbe(cmd)
	case "extension-api-g13-probe":
		return s.runExtensionAPIG13Probe(cmd)
	case "debug-g14-probe":
		return s.runDebugG14Probe(cmd)
	case "test-explorer-g15-probe":
		return s.runTestExplorerG15Probe(cmd)
	case "terminal-exit-probe":
		return s.runTerminalExitProbe(cmd)
	case "terminal-reconnect-probe":
		return s.runTerminalReconnectProbe(cmd)
	case "extension-host-g24-probe":
		return s.runExtensionHostG24Probe(cmd)
	case "git-worktree-probe":
		return s.runGitWorktreeProbe(cmd)
	case "git-rebase-probe":
		return s.runGitRebaseProbe(cmd)
	case "ai-diff-receipt-probe":
		return s.runAIDiffReceiptProbe(cmd)
	case "ai-diff-receipt-recovery-probe":
		return s.runAIDiffReceiptRecoveryProbe(cmd)
	default:
		return nil, fmt.Errorf("unsupported E2E action %q", cmd.Action)
	}
}

func (s *server) runG05WorkspaceProbe(cmd command) (interface{}, error) {
	if s.services.Project == nil || s.services.Search == nil || s.services.AI == nil ||
		s.services.Terminal == nil || s.services.Window == nil || s.services.ExecJS == nil ||
		s.services.ExecAIJS == nil {
		return nil, errors.New("G05 workspace automation is not fully wired")
	}
	if cmd.Workspace == "" || cmd.SecondaryWorkspace == "" || cmd.Marker == "" || cmd.PresetName == "" {
		return nil, errors.New("G05 workspace probe requires two workspaces, marker, and presetName")
	}
	first, err := s.services.Project.AddProject(cmd.Workspace)
	if err != nil {
		return nil, fmt.Errorf("open primary workspace: %w", err)
	}
	firstSnapshot := s.services.Project.GetWorkspaceSnapshot()
	if err := waitForRecoveryResolved(s.services.Recovery, firstSnapshot.Generation); err != nil {
		return nil, err
	}
	s.services.Window.OpenAIWindow()
	deadline := time.Now().Add(15 * time.Second)
	for !s.services.Window.IsAIWindowVisible() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !s.services.Window.IsAIWindowOpen() {
		return nil, errors.New("AI companion window did not open")
	}
	second, err := s.services.Project.AddProject(cmd.SecondaryWorkspace)
	if err != nil {
		return nil, fmt.Errorf("switch secondary workspace: %w", err)
	}
	secondSnapshot := s.services.Project.GetWorkspaceSnapshot()
	if err := waitForRecoveryResolved(s.services.Recovery, secondSnapshot.Generation); err != nil {
		return nil, err
	}
	mainResult, err := s.runWorkspaceRendererProbe("main", mainRendererExecutor(s.services.ExecJS), cmd)
	if err != nil {
		return nil, err
	}
	aiResult, err := s.runWorkspaceRendererProbe("ai", s.services.ExecAIJS, cmd)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"primaryProject":    first,
		"secondaryProject":  second,
		"primarySnapshot":   firstSnapshot,
		"secondarySnapshot": secondSnapshot,
		"aiWindowOpen":      s.services.Window.IsAIWindowOpen(),
		"aiWindowVisible":   s.services.Window.IsAIWindowVisible(),
		"mainRenderer":      mainResult,
		"aiRenderer":        aiResult,
	}, nil
}

func (s *server) runG06RuntimeRoleProbe() (interface{}, error) {
	if s.services.Window == nil || s.services.ExecJS == nil || s.services.ExecAIJS == nil {
		return nil, errors.New("G06 runtime-role automation is not fully wired")
	}
	forgedToken, err := nextToken()
	if err != nil {
		return nil, err
	}
	mainResult, err := s.runRuntimeRoleRendererProbe("main", mainRendererExecutor(s.services.ExecJS), forgedToken)
	if err != nil {
		return nil, err
	}
	s.services.Window.OpenAIWindow()
	if err := waitForAIWindowState(s.services.Window, true); err != nil {
		return nil, err
	}
	aiFirst, err := s.runRuntimeRoleRendererProbe("ai", s.services.ExecAIJS, forgedToken)
	if err != nil {
		return nil, err
	}

	s.services.Window.CloseAIWindow()
	if err := waitForAIWindowState(s.services.Window, false); err != nil {
		return nil, fmt.Errorf("AI close did not settle: %w", err)
	}
	s.services.Window.OpenAIWindow()
	if err := waitForAIWindowState(s.services.Window, true); err != nil {
		return nil, fmt.Errorf("AI reopen did not settle: %w", err)
	}
	aiReopen, err := s.runRuntimeRoleRendererProbe("ai", s.services.ExecAIJS, forgedToken)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"main":        mainResult,
		"aiFirst":     aiFirst,
		"aiReopen":    aiReopen,
		"runtimeRole": services.RuntimeRoleStatsForE2E(s.services.Window),
		"aiOpen":      s.services.Window.IsAIWindowOpen(),
		"aiVisible":   s.services.Window.IsAIWindowVisible(),
	}, nil
}

func (s *server) runConversationHandoffProbe(cmd command) (interface{}, error) {
	if s.services.Window == nil || s.services.ExecJS == nil || s.services.ExecAIJS == nil {
		return nil, errors.New("conversation handoff automation is not fully wired")
	}
	if strings.TrimSpace(cmd.Marker) == "" {
		return nil, errors.New("conversation handoff probe requires a marker")
	}

	s.services.Window.OpenAIWindow()
	if err := waitForAIWindowState(s.services.Window, true); err != nil {
		return nil, fmt.Errorf("open AI window for conversation handoff: %w", err)
	}
	ready, err := s.runConversationHandoffRendererProbe(
		s.services.ExecAIJS,
		map[string]interface{}{"action": "ready"},
	)
	if err != nil {
		return nil, err
	}
	rendererInstanceID, err := requiredRendererString(ready, "rendererInstanceId")
	if err != nil {
		return nil, fmt.Errorf("AI handoff ready result: %w", err)
	}
	windowBaseline := services.RuntimeRoleStatsForE2E(s.services.Window)

	firstMarker := cmd.Marker + "_A"
	firstMain, err := s.runConversationHandoffRendererProbe(
		mainRendererExecutor(s.services.ExecJS),
		map[string]interface{}{
			"action": "handoff",
			"marker": firstMarker,
			"mode":   "chat",
		},
	)
	if err != nil {
		return nil, err
	}
	firstID, err := requiredRendererString(firstMain, "conversationId")
	if err != nil {
		return nil, fmt.Errorf("first main handoff result: %w", err)
	}
	firstRevision, err := requiredRendererRevision(firstMain)
	if err != nil {
		return nil, fmt.Errorf("first main handoff result: %w", err)
	}
	firstRequestID, err := validateConversationHandoffAck(firstMain)
	if err != nil {
		return nil, fmt.Errorf("first main handoff result: %w", err)
	}
	firstMainRendererID, err := requiredRendererString(firstMain, "rendererInstanceId")
	if err != nil {
		return nil, fmt.Errorf("first main handoff result: %w", err)
	}
	firstReceiverEpoch, _ := firstMain["receiverEpoch"].(string)
	firstAI, err := s.runConversationHandoffRendererProbe(
		s.services.ExecAIJS,
		map[string]interface{}{
			"action":                     "inspect",
			"marker":                     firstMarker,
			"mode":                       "chat",
			"expectedConversationId":     firstID,
			"expectedRevision":           firstRevision,
			"expectedRendererInstanceId": rendererInstanceID,
		},
	)
	if err != nil {
		return nil, err
	}
	if err := validateConversationHandoffInspection(firstAI); err != nil {
		return nil, fmt.Errorf("first AI handoff result: %w", err)
	}

	secondMarker := cmd.Marker + "_B"
	secondMain, err := s.runConversationHandoffRendererProbe(
		mainRendererExecutor(s.services.ExecJS),
		map[string]interface{}{
			"action": "handoff",
			"marker": secondMarker,
			"mode":   "agent",
		},
	)
	if err != nil {
		return nil, err
	}
	secondID, err := requiredRendererString(secondMain, "conversationId")
	if err != nil {
		return nil, fmt.Errorf("second main handoff result: %w", err)
	}
	if secondID == firstID {
		return nil, errors.New("second handoff reused the first conversation ID")
	}
	secondRevision, err := requiredRendererRevision(secondMain)
	if err != nil {
		return nil, fmt.Errorf("second main handoff result: %w", err)
	}
	secondRequestID, err := validateConversationHandoffAck(secondMain)
	if err != nil {
		return nil, fmt.Errorf("second main handoff result: %w", err)
	}
	if secondRequestID == firstRequestID {
		return nil, errors.New("second handoff reused the first request ID")
	}
	secondMainRendererID, err := requiredRendererString(secondMain, "rendererInstanceId")
	if err != nil {
		return nil, fmt.Errorf("second main handoff result: %w", err)
	}
	if secondMainRendererID != firstMainRendererID {
		return nil, errors.New("main renderer remounted between conversation handoffs")
	}
	if secondMainRendererID == rendererInstanceID {
		return nil, errors.New("main and AI handoff probes reported the same renderer identity")
	}
	secondReceiverEpoch, _ := secondMain["receiverEpoch"].(string)
	if secondReceiverEpoch != firstReceiverEpoch {
		return nil, errors.New("AI conversation receiver remounted between handoffs")
	}
	secondAI, err := s.runConversationHandoffRendererProbe(
		s.services.ExecAIJS,
		map[string]interface{}{
			"action":                     "inspect",
			"marker":                     secondMarker,
			"mode":                       "agent",
			"expectedConversationId":     secondID,
			"expectedRevision":           secondRevision,
			"expectedRendererInstanceId": rendererInstanceID,
			"forbiddenMarker":            firstMarker,
		},
	)
	if err != nil {
		return nil, err
	}
	if err := validateConversationHandoffInspection(secondAI); err != nil {
		return nil, fmt.Errorf("second AI handoff result: %w", err)
	}
	secondRendererInstanceID, err := requiredRendererString(secondAI, "rendererInstanceId")
	if err != nil {
		return nil, fmt.Errorf("second AI handoff result: %w", err)
	}
	if secondRendererInstanceID != rendererInstanceID {
		return nil, errors.New("AI renderer remounted between conversation handoffs")
	}
	windowAfter := services.RuntimeRoleStatsForE2E(s.services.Window)
	if windowAfter.AIWindowsCreated != windowBaseline.AIWindowsCreated ||
		windowAfter.AIWindowsClosed != windowBaseline.AIWindowsClosed {
		return nil, fmt.Errorf(
			"AI native window identity changed during handoff: before=%+v after=%+v",
			windowBaseline,
			windowAfter,
		)
	}

	return map[string]interface{}{
		"ok":                              true,
		"aiWindowOpen":                    s.services.Window.IsAIWindowOpen(),
		"aiWindowVisible":                 s.services.Window.IsAIWindowVisible(),
		"sameRendererInstance":            true,
		"sameNativeWindow":                true,
		"sameReceiverEpoch":               true,
		"rendererInstanceId":              rendererInstanceID,
		"mainRendererInstanceId":          firstMainRendererID,
		"receiverEpoch":                   firstReceiverEpoch,
		"windowStatsBefore":               windowBaseline,
		"windowStatsAfter":                windowAfter,
		"firstConversationId":             firstID,
		"firstRevision":                   firstRevision,
		"firstMarkerObserved":             firstAI["markerObserved"] == true,
		"firstDOMMarkerObserved":          firstAI["domMarkerObserved"] == true,
		"firstActiveConversationMatches":  firstAI["activeConversationMatches"] == true,
		"firstMode":                       firstAI["mode"],
		"firstAcknowledged":               firstMain["acknowledged"] == true,
		"secondConversationId":            secondID,
		"secondRevision":                  secondRevision,
		"secondMarkerObserved":            secondAI["markerObserved"] == true,
		"secondDOMMarkerObserved":         secondAI["domMarkerObserved"] == true,
		"secondActiveConversationMatches": secondAI["activeConversationMatches"] == true,
		"secondMode":                      secondAI["mode"],
		"secondAcknowledged":              secondMain["acknowledged"] == true,
		"mainRendererFirst":               firstMain,
		"aiRendererFirst":                 firstAI,
		"mainRendererSecond":              secondMain,
		"aiRendererSecond":                secondAI,
	}, nil
}

func (s *server) runConversationHandoffRendererProbe(
	execute rendererExecutor,
	configuration map[string]interface{},
) (map[string]interface{}, error) {
	if execute == nil {
		return nil, errors.New("conversation handoff renderer executor is unavailable")
	}
	runID, err := nextToken()
	if err != nil {
		return nil, err
	}
	configuration["runId"] = runID
	encoded, err := json.Marshal(configuration)
	if err != nil {
		return nil, fmt.Errorf("encode conversation handoff renderer configuration: %w", err)
	}
	resultValue, err := s.runRendererProbeWithExecutor(
		execute,
		"__koyoriIdeRunConversationHandoffProbe",
		conversationHandoffResultEvent,
		"conversation handoff",
		encoded,
	)
	if err != nil {
		return nil, err
	}
	result, ok := resultValue.(map[string]interface{})
	if !ok {
		return nil, errors.New("conversation handoff renderer returned an invalid result")
	}
	if result["ok"] != true {
		detail, _ := result["error"].(string)
		if detail == "" {
			detail = "renderer probe failed"
		}
		return nil, fmt.Errorf("conversation handoff renderer: %s", detail)
	}
	return result, nil
}

func requiredRendererString(result map[string]interface{}, field string) (string, error) {
	value, _ := result[field].(string)
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("missing %s", field)
	}
	return value, nil
}

func requiredRendererRevision(result map[string]interface{}) (int, error) {
	value, ok := result["revision"].(float64)
	if !ok || value < 1 || value != float64(int(value)) {
		return 0, errors.New("missing positive conversation revision")
	}
	return int(value), nil
}

func validateConversationHandoffAck(result map[string]interface{}) (string, error) {
	if result["acknowledged"] != true {
		return "", errors.New("conversation target was not acknowledged")
	}
	requestID, err := requiredRendererString(result, "requestId")
	if err != nil {
		return "", err
	}
	for _, field := range []string{"sourceOrigin", "sourceEpoch", "receiverEpoch"} {
		if _, err := requiredRendererString(result, field); err != nil {
			return "", err
		}
	}
	recipientEpoch, err := requiredRendererString(result, "recipientEpoch")
	if err != nil {
		return "", err
	}
	receiverEpoch, _ := result["receiverEpoch"].(string)
	if recipientEpoch != receiverEpoch {
		return "", errors.New("acknowledgement receiver epoch does not match the target recipient")
	}
	sequence, ok := result["sequence"].(float64)
	if !ok || sequence < 1 || sequence != float64(int(sequence)) {
		return "", errors.New("acknowledgement is missing a valid target sequence")
	}
	return requestID, nil
}

func validateConversationHandoffInspection(result map[string]interface{}) error {
	for _, field := range []string{
		"markerObserved",
		"domMarkerObserved",
		"activeConversationMatches",
		"windowMounted",
	} {
		if result[field] != true {
			return fmt.Errorf("%s was not observed", field)
		}
	}
	return nil
}

func waitForAIWindowState(window *services.WindowService, open bool) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if window.IsAIWindowOpen() == open && (!open || window.IsAIWindowVisible()) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("AI window state did not settle: open=%t actual=%t", open, window.IsAIWindowOpen())
}

func (s *server) runRuntimeRoleRendererProbe(role string, execute rendererExecutor, forgedToken string) (interface{}, error) {
	runID, err := nextToken()
	if err != nil {
		return nil, err
	}
	configuration, err := json.Marshal(map[string]string{
		"runId":        runID,
		"expectedRole": role,
		"forgedToken":  forgedToken,
	})
	if err != nil {
		return nil, fmt.Errorf("encode G06 runtime-role probe configuration: %w", err)
	}
	return s.runRendererProbeWithExecutor(
		execute,
		"__koyoriIdeRunG06RuntimeRoleProbe",
		runtimeRoleResultEvent,
		"G06 runtime role",
		configuration,
	)
}

func waitForRecoveryResolved(recovery *services.RecoveryService, generation uint64) error {
	if recovery == nil {
		return errors.New("G05 workspace automation has no RecoveryService")
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		state := recovery.GetRecoveryState()
		if state.Generation == generation && state.Phase == services.RecoveryPhaseResolved {
			return nil
		}
		if state.Generation == generation && state.Phase == services.RecoveryPhaseFailed {
			return fmt.Errorf("recovery scan failed for workspace generation %d: %s", generation, state.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}
	state := recovery.GetRecoveryState()
	return fmt.Errorf("recovery scan did not settle for workspace generation %d: phase=%s currentGeneration=%d", generation, state.Phase, state.Generation)
}

func (s *server) runWorkspaceRendererProbe(role string, execute rendererExecutor, cmd command) (interface{}, error) {
	if execute == nil {
		return nil, fmt.Errorf("%s renderer executor is unavailable", role)
	}
	runID, err := nextToken()
	if err != nil {
		return nil, err
	}
	configuration, err := json.Marshal(map[string]string{
		"runId":      runID,
		"role":       role,
		"workspace":  cmd.SecondaryWorkspace,
		"marker":     cmd.Marker,
		"presetName": cmd.PresetName,
	})
	if err != nil {
		return nil, fmt.Errorf("encode G05 workspace probe configuration: %w", err)
	}
	return s.runRendererProbeWithExecutor(execute, "__koyoriIdeRunG05WorkspaceProbe", workspaceResultEvent, "G05 workspace", configuration)
}

func (s *server) receiveRendererProbeResult(event *application.CustomEvent) {
	encoded, err := json.Marshal(event.Data)
	if err != nil {
		return
	}
	var result map[string]interface{}
	if json.Unmarshal(encoded, &result) != nil {
		return
	}
	runID, _ := result["runId"].(string)
	if runID == "" {
		return
	}
	s.probeMu.Lock()
	resultChannel := s.probeResults[runID]
	s.probeMu.Unlock()
	if resultChannel == nil {
		return
	}
	select {
	case resultChannel <- result:
	default:
	}
}

func (s *server) runRendererProbe(
	globalHook,
	resultEvent,
	label string,
	configuration []byte,
) (interface{}, error) {
	runID := ""
	var decoded map[string]interface{}
	if err := json.Unmarshal(configuration, &decoded); err != nil {
		return nil, fmt.Errorf("decode %s renderer probe configuration: %w", label, err)
	}
	if value, ok := decoded["runId"].(string); ok {
		runID = value
	}
	if runID == "" {
		return nil, fmt.Errorf("%s renderer probe configuration has no run ID", label)
	}

	resultChannel := make(chan map[string]interface{}, 1)
	s.probeMu.Lock()
	if s.probeResults == nil {
		s.probeResults = make(map[string]chan map[string]interface{})
	}
	s.probeResults[runID] = resultChannel
	s.probeMu.Unlock()
	defer func() {
		s.probeMu.Lock()
		delete(s.probeResults, runID)
		s.probeMu.Unlock()
	}()

	script := fmt.Sprintf(`(() => {
	const config = %s;
	const deadline = Date.now() + 30000;
	const start = () => {
		const probe = globalThis[%q];
		if (typeof probe === "function") {
			void probe(config);
			return;
		}
		if (Date.now() >= deadline) {
			import("/wails/runtime.js").then(({ Events }) => Events.Emit(%q, {
				runId: config.runId,
				ok: false,
				error: %q,
			}));
			return;
		}
		setTimeout(start, 100);
	};
	start();
})()`, configuration, globalHook, resultEvent, label+" renderer probe hook was not installed")
	s.services.ExecJS(script)

	select {
	case result := <-resultChannel:
		return result, nil
	case <-time.After(60 * time.Second):
		return nil, fmt.Errorf("timed out waiting for %s renderer probe result", label)
	}
}

func (s *server) runRendererProbeWithExecutor(
	execute rendererExecutor,
	globalHook,
	resultEvent,
	label string,
	configuration []byte,
) (interface{}, error) {
	if execute == nil {
		return nil, fmt.Errorf("%s renderer executor is unavailable", label)
	}
	runID := ""
	var decoded map[string]interface{}
	if err := json.Unmarshal(configuration, &decoded); err != nil {
		return nil, fmt.Errorf("decode %s renderer probe configuration: %w", label, err)
	}
	if value, ok := decoded["runId"].(string); ok {
		runID = value
	}
	if runID == "" {
		return nil, fmt.Errorf("%s renderer probe configuration has no run ID", label)
	}
	resultChannel := make(chan map[string]interface{}, 1)
	s.probeMu.Lock()
	if s.probeResults == nil {
		s.probeResults = make(map[string]chan map[string]interface{})
	}
	s.probeResults[runID] = resultChannel
	s.probeMu.Unlock()
	defer func() {
		s.probeMu.Lock()
		delete(s.probeResults, runID)
		s.probeMu.Unlock()
	}()

	script := fmt.Sprintf(`(() => {
	const config = %s;
	const deadline = Date.now() + 30000;
	const start = () => {
		const probe = globalThis[%q];
		if (typeof probe === "function") {
			void probe(config);
			return;
		}
		if (Date.now() >= deadline) {
			import("/wails/runtime.js").then(({ Events }) => Events.Emit(%q, {
				runId: config.runId,
				ok: false,
				error: %q,
			}));
			return;
		}
		setTimeout(start, 100);
	};
	start();
})()`, configuration, globalHook, resultEvent, label+" renderer probe hook was not installed")
	if !execute(script) {
		return nil, fmt.Errorf("%s renderer executor rejected the script", label)
	}

	select {
	case result := <-resultChannel:
		return result, nil
	case <-time.After(60 * time.Second):
		return nil, fmt.Errorf("timed out waiting for %s renderer probe result", label)
	}
}

func (s *server) runHTTPClientRendererProbe(cmd command) (interface{}, error) {
	if s.services.HTTPClient == nil || s.services.ExecJS == nil {
		return nil, errors.New("HTTP-client renderer automation is not wired")
	}
	if cmd.PrimaryOrigin == "" || cmd.SecondaryOrigin == "" || cmd.PublicURL == "" {
		return nil, errors.New("HTTP-client renderer probe requires primaryOrigin, secondaryOrigin, and publicUrl")
	}

	const expiredRequestID = "packaged-expired-token"
	expiredToken, err := services.IssueExpiredHTTPClientE2EToken(
		s.services.HTTPClient,
		cmd.PrimaryOrigin,
		expiredRequestID,
	)
	if err != nil {
		return nil, err
	}
	restoreApprover := services.ConfigureHTTPClientE2EApprovalSequence(
		s.services.HTTPClient,
		[]bool{false, true, true, true, true, true},
	)
	defer restoreApprover()

	runID, err := nextToken()
	if err != nil {
		return nil, err
	}
	configuration, err := json.Marshal(map[string]string{
		"runId":            runID,
		"primaryOrigin":    cmd.PrimaryOrigin,
		"secondaryOrigin":  cmd.SecondaryOrigin,
		"publicUrl":        cmd.PublicURL,
		"expiredToken":     expiredToken,
		"expiredRequestId": expiredRequestID,
	})
	if err != nil {
		return nil, fmt.Errorf("encode HTTP-client renderer probe configuration: %w", err)
	}
	return s.runRendererProbe(
		"__koyoriIdeRunHTTPClientProbe",
		httpClientResultEvent,
		"HTTP-client",
		configuration,
	)
}

func (s *server) runRecoveryRendererProbe(cmd command) (interface{}, error) {
	if s.services.ExecJS == nil {
		return nil, errors.New("recovery renderer automation is not wired")
	}
	if cmd.ProbeMode == "" || cmd.Path == "" {
		return nil, errors.New("recovery renderer probe requires probeMode and path")
	}
	runID, err := nextToken()
	if err != nil {
		return nil, err
	}
	configuration, err := json.Marshal(map[string]string{
		"runId":          runID,
		"mode":           cmd.ProbeMode,
		"path":           cmd.Path,
		"diskContent":    cmd.Expected,
		"crashContent":   cmd.CrashContent,
		"pendingContent": cmd.PendingContent,
	})
	if err != nil {
		return nil, fmt.Errorf("encode recovery renderer probe configuration: %w", err)
	}
	return s.runRendererProbe(
		"__koyoriIdeRunRecoveryProbe",
		recoveryResultEvent,
		"recovery",
		configuration,
	)
}

func requireE2ENotAllowed(label string, operation func() error) (string, error) {
	err := operation()
	if !errors.Is(err, services.ErrNotAllowed) {
		return "", fmt.Errorf("%s returned %v, want ErrNotAllowed", label, err)
	}
	return err.Error(), nil
}

func (s *server) runRecoveryGuardProbe(cmd command) (interface{}, error) {
	if s.services.Window == nil {
		return nil, errors.New("recovery window guard automation is not wired")
	}
	if cmd.Workspace == "" || cmd.WindowID == "" || cmd.Path == "" {
		return nil, errors.New("recovery guard probe requires workspace, windowId, and path")
	}
	results := make(map[string]string)
	checks := []struct {
		label string
		run   func() error
	}{
		{label: "titlebar close", run: s.services.Window.Close},
		{label: "workspace switch", run: func() error {
			_, err := s.services.Project.AddProject(cmd.Workspace)
			return err
		}},
		{label: "clear pending record", run: func() error {
			return s.services.Recovery.ClearDirtyBuffer(cmd.WindowID, cmd.Path)
		}},
		{label: "clear pending window", run: func() error {
			return s.services.Recovery.ClearWindowJournal(cmd.WindowID)
		}},
		{label: "discard pending session", run: func() error {
			return s.services.Recovery.DiscardRecoveredSession(cmd.WindowID)
		}},
		{label: "clear pending workspace", run: s.services.Recovery.ClearWorkspaceJournal},
		{label: "disable journal", run: func() error {
			return s.services.Recovery.SetJournalEnabled(false)
		}},
	}
	for _, check := range checks {
		detail, err := requireE2ENotAllowed(check.label, check.run)
		if err != nil {
			return nil, err
		}
		results[check.label] = detail
	}
	state := s.services.Recovery.GetRecoveryState()
	if state.Phase != services.RecoveryPhasePending || !s.services.Recovery.IsJournalEnabled() {
		return nil, fmt.Errorf("recovery guard state changed unexpectedly: %+v", state)
	}
	return map[string]interface{}{
		"rejections":     results,
		"state":          state,
		"journalEnabled": true,
	}, nil
}

func (s *server) runNativeWindowCloseProbe() (interface{}, error) {
	if s.services.CloseWindow == nil {
		return nil, errors.New("native window close automation is not wired")
	}
	state := s.services.Recovery.GetRecoveryState()
	if state.Phase != services.RecoveryPhasePending {
		return nil, fmt.Errorf("native close probe requires pending recovery, got %s", state.Phase)
	}
	s.services.CloseWindow()
	time.Sleep(250 * time.Millisecond)
	after := s.services.Recovery.GetRecoveryState()
	if after.Phase != services.RecoveryPhasePending {
		return nil, fmt.Errorf("native close changed recovery state to %s", after.Phase)
	}
	return map[string]interface{}{
		"hookInvoked": true,
		"state":       after,
	}, nil
}

func (s *server) recordDirtyBuffer(cmd command) (interface{}, error) {
	window := cmd.WindowID
	if window == "" {
		window = windowID
	}
	baseline, err := s.services.Recovery.ComputeBaseline(cmd.Path)
	if err != nil {
		return nil, err
	}
	if err := s.services.Recovery.SaveDirtyBuffer(
		window,
		cmd.Path,
		cmd.Content,
		"utf-8",
		"lf",
		baseline.Mtime,
		baseline.Hash,
	); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"baselineHash":  baseline.Hash,
		"baselineMtime": baseline.Mtime,
		"exists":        baseline.Exists,
	}, nil
}

func (s *server) saveBuffer(cmd command) (interface{}, error) {
	if cmd.BaselineHash == "" {
		return nil, errors.New("save requires baselineHash")
	}
	if err := s.services.File.WriteFileIfUnchanged(cmd.Path, cmd.Content, cmd.BaselineHash); err != nil {
		return nil, err
	}
	window := cmd.WindowID
	if window == "" {
		window = windowID
	}
	if err := s.services.Recovery.ClearDirtyBuffer(window, cmd.Path); err != nil {
		return nil, err
	}
	return map[string]interface{}{"saved": true}, nil
}

func (s *server) runTerminalCommand(cmd command) (interface{}, error) {
	if cmd.Command == "" || cmd.Expected == "" {
		return nil, errors.New("terminal-command requires command and expected output")
	}
	if err := s.services.Terminal.Start(cmd.Workspace); err != nil {
		return nil, err
	}
	defer s.services.Terminal.Kill()
	if err := s.services.Terminal.Write(cmd.Command + "\n"); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(8 * time.Second)
	var output strings.Builder
	for time.Now().Before(deadline) {
		output.WriteString(s.services.Terminal.ReadOutput(250 * time.Millisecond))
		if strings.Contains(output.String(), cmd.Expected) {
			return map[string]interface{}{"output": output.String()}, nil
		}
	}
	return nil, fmt.Errorf("terminal output did not contain %q; got %q", cmd.Expected, output.String())
}

func (s *server) runLSPAction(cmd command) (interface{}, error) {
	if cmd.Language == "" {
		cmd.Language = "go"
	}
	if err := s.services.LSP.StartLSPServer(cmd.Language); err != nil {
		return nil, err
	}
	completionRequest := services.LSPCompletionRequest{
		Language: cmd.Language,
		FilePath: cmd.Path,
		Content:  cmd.Content,
		Line:     cmd.CompletionLine,
		Column:   cmd.CompletionColumn,
	}
	// The packaged E2E isolates APPDATA per launch, so gopls starts cold and
	// its first request can exceed the single-request timeout. Retry a few
	// times with a settle pause; once the server is warm, results are real.
	var items []services.LSPCompletionItem
	var hover string
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		var err error
		items, err = s.services.LSP.GetCompletions(completionRequest)
		if err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}
		// GetCompletions deliberately converts request timeouts into an empty
		// result with a nil error for the production UI. The packaged probe must
		// inspect the structured call status as well, otherwise a cold gopls
		// startup is mistaken for a valid empty completion response and the
		// retry loop never runs.
		completionStatus := s.services.LSP.GetCallStatus(cmd.Language)
		if completionStatus.Code != "ok" {
			lastErr = fmt.Errorf("LSP completion %s: %s", completionStatus.Code, completionStatus.Message)
			time.Sleep(2 * time.Second)
			continue
		}
		hoverRequest := completionRequest
		hoverRequest.Line = cmd.HoverLine
		hoverRequest.Column = cmd.HoverColumn
		hover, err = s.services.LSP.GetHover(hoverRequest)
		if err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}
		if len(items) == 0 && strings.TrimSpace(hover) == "" {
			lastErr = errors.New("LSP returned neither completion nor hover content")
			time.Sleep(2 * time.Second)
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return map[string]interface{}{
		"completionCount": len(items),
		"hover":           hover,
		"status":          s.services.LSP.GetCallStatus(cmd.Language),
	}, nil
}

func (s *server) runG10MonacoProbe(cmd command) (interface{}, error) {
	if s.services.ExecJS == nil {
		return nil, errors.New("G10 monaco automation is not fully wired")
	}
	runID, err := nextToken()
	if err != nil {
		return nil, err
	}
	configuration, err := json.Marshal(map[string]string{
		"runId":     runID,
		"workspace": cmd.Workspace,
		"filePath":  cmd.Path,
	})
	if err != nil {
		return nil, fmt.Errorf("encode G10 monaco probe configuration: %w", err)
	}
	return s.runRendererProbeWithExecutor(
		mainRendererExecutor(s.services.ExecJS),
		"__koyoriIdeRunG10MonacoProbe",
		monacoResultEvent,
		"G10 Monaco",
		configuration,
	)
}

func (s *server) runSearchReplace(cmd command) (interface{}, error) {
	if s.services.Search == nil {
		return nil, errors.New("search-replace automation is not fully wired")
	}
	if cmd.Workspace == "" || cmd.Path == "" || cmd.Marker == "" || cmd.Replacement == "" {
		return nil, errors.New("search-replace requires workspace, path, marker, and replacement")
	}
	matches, err := s.services.Search.Search(cmd.Workspace, cmd.Marker, false)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("search did not find marker %q under %s", cmd.Marker, cmd.Workspace)
	}
	result, err := s.services.Search.Replace(cmd.Path, cmd.Marker, cmd.Replacement, false)
	if err != nil {
		return nil, err
	}
	if result.Replacements == 0 {
		return nil, fmt.Errorf("replace applied no replacements in %s", cmd.Path)
	}
	return map[string]interface{}{
		"matches":      len(matches),
		"replacements": result.Replacements,
	}, nil
}
func (s *server) runGitDiff(cmd command) (interface{}, error) {
	if s.services.Git == nil {
		return nil, errors.New("git-diff automation is not fully wired")
	}
	if cmd.Workspace == "" || cmd.Path == "" || cmd.Content == "" {
		return nil, errors.New("git-diff requires workspace, path, and content")
	}
	if err := s.services.Git.InitRepo(cmd.Workspace); err != nil {
		return nil, err
	}
	// InitRepo makes an initial commit of everything present, so write a
	// brand-new file to exercise a real untracked diff deterministically.
	if err := os.WriteFile(cmd.Path, []byte(cmd.Content), 0o600); err != nil {
		return nil, err
	}
	status, err := s.services.Git.GetStatus(cmd.Workspace)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(cmd.Workspace, cmd.Path)
	if err != nil {
		return nil, err
	}
	changed := false
	for _, change := range status {
		if change.Path == rel {
			changed = true
		}
	}
	if !changed {
		return nil, fmt.Errorf("git status did not report %s as changed", rel)
	}
	diff, err := s.services.Git.GetDiff(cmd.Workspace, rel)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(diff) == "" {
		return nil, errors.New("git diff for the fixture file is empty")
	}
	return map[string]interface{}{"changed": changed, "diff": diff}, nil
}
func (s *server) runAIFailCancel(cmd command) (interface{}, error) {
	if s.services.AI == nil {
		return nil, errors.New("ai-fail-cancel automation is not fully wired")
	}
	// P9-G10: without credentials the AI service must fail closed rather than
	// report success. The packaged E2E env never injects API keys.
	_, sendErr := s.services.AI.Send([]services.ChatMessage{{Role: "user", Content: "ping"}})
	if sendErr == nil {
		return nil, errors.New("AI Send succeeded without credentials; fail-closed violated")
	}
	app := application.Get()
	if app == nil {
		return nil, errors.New("ai-fail-cancel has no Wails application")
	}
	window, ok := app.Window.GetByName("main")
	if !ok || window == nil {
		return nil, errors.New("ai-fail-cancel has no main renderer window")
	}
	callerCtx := context.WithValue(context.Background(), application.WindowKey, window)
	_, startErr := s.services.AI.StartStream(callerCtx, []services.ChatMessage{{Role: "user", Content: "ping"}})
	stopped := true
	if startErr == nil {
		_ = s.services.AI.StopStream(callerCtx)
		stopped = !s.services.AI.IsStreaming()
	}
	return map[string]interface{}{
		"sendFailed":     sendErr != nil,
		"sendError":      sendErr.Error(),
		"streamStarted":  startErr == nil,
		"streamStopped":  stopped,
		"streamStartErr": errString(startErr),
	}, nil
}

func (s *server) runSettingsConcurrent(cmd command) (interface{}, error) {
	if s.services.Settings == nil {
		return nil, errors.New("settings-concurrent automation is not fully wired")
	}
	svc := s.services.Settings

	// Seed a deterministic baseline in the isolated E2E config dir (the
	// packaged harness redirects XDG_CONFIG_HOME per launch).
	if err := svc.SaveSettings(services.Settings{Theme: "light", FontSize: 12}); err != nil {
		return nil, fmt.Errorf("seed settings: %w", err)
	}

	// Two windows each load the same baseline version.
	loadedA, err := svc.LoadSettings()
	if err != nil {
		return nil, fmt.Errorf("window A load: %w", err)
	}
	loadedB, err := svc.LoadSettings()
	if err != nil {
		return nil, fmt.Errorf("window B load: %w", err)
	}

	// Window A commits Theme against the current version.
	verA := loadedA.Version
	loadedA.ExpectedVersion = &verA
	loadedA.Theme = "dark"
	if err := svc.SaveSettings(loadedA); err != nil {
		return nil, fmt.Errorf("window A save: %w", err)
	}

	// Window B still holds the stale version and changes FontSize: the CAS
	// must reject the write so A's change is not silently overwritten.
	stale := loadedB.Version
	loadedB.ExpectedVersion = &stale
	loadedB.FontSize = 16
	staleErr := svc.SaveSettings(loadedB)
	if staleErr == nil {
		return nil, errors.New("stale window B save unexpectedly succeeded; CAS not enforced")
	}
	if !strings.Contains(staleErr.Error(), "version conflict") {
		return nil, fmt.Errorf("expected version conflict, got %v", staleErr)
	}

	// B reloads the latest snapshot (must contain A's Theme change) and
	// replays its own FontSize change against the fresh version.
	reloaded, err := svc.LoadSettings()
	if err != nil {
		return nil, fmt.Errorf("window B reload: %w", err)
	}
	if reloaded.Theme != "dark" {
		return nil, fmt.Errorf("window B reload lost window A change: theme=%q", reloaded.Theme)
	}
	cur := reloaded.Version
	reloaded.ExpectedVersion = &cur
	reloaded.FontSize = 16
	if err := svc.SaveSettings(reloaded); err != nil {
		return nil, fmt.Errorf("window B retry save: %w", err)
	}

	final, err := svc.LoadSettings()
	if err != nil {
		return nil, fmt.Errorf("final load: %w", err)
	}
	both := final.Theme == "dark" && final.FontSize == 16
	if !both {
		return nil, fmt.Errorf("both windows' changes not preserved on disk: %+v", final)
	}
	return map[string]interface{}{
		"windowAApplied":    true,
		"staleBRejected":    true,
		"staleBError":       staleErr.Error(),
		"bReloadSawA":       true,
		"bRetryApplied":     true,
		"finalTheme":        final.Theme,
		"finalFontSize":     final.FontSize,
		"finalVersion":      final.Version,
		"bothFieldsPresent": both,
	}, nil
}
func (s *server) runAIRequestContextProbe(cmd command) (interface{}, error) {
	if s.services.AI == nil {
		return nil, errors.New("ai-request-context-probe automation is not fully wired")
	}
	// G12 AC1: a checkable local protocol service (httptest) receives the
	// exact structured fields the UI builds — system prompt (plan/persona)
	// plus image_url content blocks — through the real packaged service graph.
	var mu sync.Mutex
	var captured map[string]interface{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		captured = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"},
			},
		})
	}))
	defer mock.Close()

	systemPrompt := "You are koyori-ide E2E assistant.\n" +
		"Active plan (user-approved goal):\nGoal: Fix HTTP retry\nSteps:\n  1. [approved] Extract client\n" +
		"Persona: Senior Go Reviewer"
	if err := s.services.AI.SetConfig(services.AIConfig{
		APIKey:       "e2e-key",
		BaseURL:      mock.URL,
		Model:        "gpt-4o",
		SystemPrompt: systemPrompt,
		MaxTokens:    128,
	}); err != nil {
		return nil, fmt.Errorf("set AI config for request-context probe: %w", err)
	}
	if _, err := s.services.AI.Send([]services.ChatMessage{{
		Role:    "user",
		Content: "analyze the screenshot",
		Images:  []string{"data:image/png;base64,aGVsbG8="},
	}}); err != nil {
		return nil, fmt.Errorf("AI Send against local protocol service: %w", err)
	}

	mu.Lock()
	body := captured
	mu.Unlock()
	if body == nil {
		return nil, errors.New("local protocol service captured no request body")
	}
	messages, _ := body["messages"].([]interface{})
	var systemContent, userContent interface{}
	for _, m := range messages {
		msg, _ := m.(map[string]interface{})
		switch msg["role"] {
		case "system":
			systemContent = msg["content"]
		case "user":
			userContent = msg["content"]
		}
	}
	sysText, sysOK := systemContent.(string)
	if !sysOK || !strings.Contains(sysText, "Active plan") || !strings.Contains(sysText, "Fix HTTP retry") ||
		!strings.Contains(sysText, "Extract client") || !strings.Contains(sysText, "Persona: Senior Go Reviewer") {
		return nil, fmt.Errorf("provider system prompt lost plan/persona fields: %v", systemContent)
	}
	parts, partsOK := userContent.([]interface{})
	if !partsOK {
		return nil, fmt.Errorf("provider user content is not an array (image missing): %T", userContent)
	}
	imageSeen := false
	for _, part := range parts {
		p, _ := part.(map[string]interface{})
		if p["type"] == "image_url" {
			imageSeen = true
		}
	}
	if !imageSeen {
		return nil, errors.New("provider request has no image_url block")
	}
	evidence := map[string]interface{}{
		"systemPromptReachedProvider": sysOK && strings.Contains(sysText, "Active plan"),
		"planInSystemPrompt":          strings.Contains(sysText, "Fix HTTP retry") && strings.Contains(sysText, "Extract client"),
		"personaInSystemPrompt":       strings.Contains(sysText, "Persona: Senior Go Reviewer"),
		"imageBlockReachedProvider":   imageSeen,
		"captured":                    true,
	}
	if cmd.Workspace != "" || cmd.Path != "" || cmd.Marker != "" {
		if cmd.Workspace == "" || cmd.Path == "" || cmd.Marker == "" {
			return nil, errors.New("packaged AI fixture must provide the complete Agent tool-round configuration")
		}
		agentRound, err := s.runAgentToolRoundProbe(cmd)
		if err != nil {
			return nil, fmt.Errorf("packaged Agent tool round: %w", err)
		}
		evidence["agentToolRounds"] = agentRound
	}
	return evidence, nil
}

func providerRequestHasTool(body map[string]interface{}, name string) bool {
	tools, _ := body["tools"].([]interface{})
	for _, raw := range tools {
		tool, _ := raw.(map[string]interface{})
		function, _ := tool["function"].(map[string]interface{})
		if function["name"] == name {
			return true
		}
	}
	return false
}

func providerRequestMessageContains(body map[string]interface{}, role, marker string) bool {
	messages, _ := body["messages"].([]interface{})
	for _, raw := range messages {
		message, _ := raw.(map[string]interface{})
		if message["role"] != role {
			continue
		}
		if content, ok := message["content"].(string); ok && strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

func validateOpenAINativeToolResultRequest(
	body map[string]interface{},
	callID, toolName, arguments, marker string,
) error {
	messages, ok := body["messages"].([]interface{})
	if !ok {
		return errors.New("provider request has no messages array")
	}
	callIndex := -1
	for index, raw := range messages {
		message, _ := raw.(map[string]interface{})
		if message["role"] == "user" {
			if content, _ := message["content"].(string); strings.Contains(content, marker) {
				return errors.New("provider request used a legacy user observation")
			}
		}
		calls, hasCalls := message["tool_calls"].([]interface{})
		if !hasCalls {
			continue
		}
		if callIndex >= 0 {
			return errors.New("provider request contains multiple assistant tool-call messages")
		}
		if message["role"] != "assistant" || len(calls) != 1 {
			return errors.New("provider request has an invalid assistant tool-call batch")
		}
		call, _ := calls[0].(map[string]interface{})
		function, _ := call["function"].(map[string]interface{})
		if call["id"] != callID || call["type"] != "function" ||
			function["name"] != toolName || function["arguments"] != arguments {
			return errors.New("provider request changed the native tool-call identity")
		}
		callIndex = index
	}
	if callIndex < 0 || callIndex+1 >= len(messages) {
		return errors.New("provider request has no assistant tool call followed by a result")
	}
	result, _ := messages[callIndex+1].(map[string]interface{})
	content, _ := result["content"].(string)
	if result["role"] != "tool" || result["tool_call_id"] != callID ||
		!strings.Contains(content, marker) {
		return errors.New("provider request has no matching native tool result")
	}
	for index, raw := range messages {
		if index == callIndex+1 {
			continue
		}
		message, _ := raw.(map[string]interface{})
		if message["role"] == "tool" {
			return errors.New("provider request contains an unexpected extra tool result")
		}
	}
	return nil
}

type agentToolRoundSpec struct {
	Name              string
	ToolKind          string
	ApprovalMode      string
	ExpectedDecision  string
	ExpectedOutcome   string
	ToolCallID        string
	FinalAssistant    string
	InitialUserPrompt string
	CatalogApproval   string
	CatalogRisk       string
	CatalogMutation   string
	Arguments         func(relativePath, marker string) map[string]interface{}
	Observation       func(relativePath, marker string) string
}

type agentNativeApprovalContract struct {
	Expectation services.AgentNativeApprovalExpectationForE2E
	ExpectCall  bool
}

func readAgentToolRoundSpec() agentToolRoundSpec {
	return agentToolRoundSpec{
		Name: "readAuto", ToolKind: "read", ApprovalMode: "auto-approve",
		ExpectedDecision: "approve", ExpectedOutcome: "executed",
		ToolCallID: "call_packaged_agent_read", FinalAssistant: "PACKAGED_AGENT_READ_ROUND_COMPLETE",
		InitialUserPrompt: "Read the packaged Agent fixture with the read tool, then report completion after the observation.",
		CatalogApproval:   "backend-policy", CatalogRisk: "read-only", CatalogMutation: "none",
		Arguments: func(relativePath, _ string) map[string]interface{} {
			return map[string]interface{}{"path": relativePath}
		},
		Observation: func(_, marker string) string { return marker },
	}
}

func searchAgentToolRoundSpec() agentToolRoundSpec {
	return agentToolRoundSpec{
		Name: "searchAuto", ToolKind: "search", ApprovalMode: "auto-approve",
		ExpectedDecision: "approve", ExpectedOutcome: "executed",
		ToolCallID: "call_packaged_agent_search", FinalAssistant: "PACKAGED_AGENT_SEARCH_ROUND_COMPLETE",
		InitialUserPrompt: "Search the packaged workspace for the unique marker, then report completion after the observation.",
		CatalogApproval:   "backend-policy", CatalogRisk: "read-only", CatalogMutation: "none",
		Arguments: func(_ string, marker string) map[string]interface{} {
			return map[string]interface{}{"query": marker, "ignoreCase": false}
		},
		Observation: func(_, marker string) string { return marker },
	}
}

func writeAgentToolRoundSpec(name, decision, outcome string) agentToolRoundSpec {
	return agentToolRoundSpec{
		Name: name, ToolKind: "write", ApprovalMode: "ask",
		ExpectedDecision: decision, ExpectedOutcome: outcome,
		ToolCallID:        "call_packaged_agent_write_" + decision,
		FinalAssistant:    "PACKAGED_AGENT_WRITE_" + strings.ToUpper(decision) + "_ROUND_COMPLETE",
		InitialUserPrompt: "Use the write tool exactly as requested, then report completion after the tool result.",
		CatalogApproval:   "manual", CatalogRisk: "elevated", CatalogMutation: "workspace-transaction",
		Arguments: func(relativePath, marker string) map[string]interface{} {
			return map[string]interface{}{"path": relativePath, "content": marker}
		},
		Observation: func(relativePath, _ string) string {
			if outcome == "rejected" {
				return fmt.Sprintf("User rejected the write action on %q", relativePath)
			}
			return "Wrote " + relativePath
		},
	}
}

func runAgentToolRoundSpec(name, decision, outcome, command string) agentToolRoundSpec {
	return agentToolRoundSpec{
		Name: name, ToolKind: "run", ApprovalMode: "ask",
		ExpectedDecision: decision, ExpectedOutcome: outcome,
		ToolCallID:        "call_packaged_agent_run_" + decision,
		FinalAssistant:    "PACKAGED_AGENT_RUN_" + strings.ToUpper(decision) + "_ROUND_COMPLETE",
		InitialUserPrompt: "Use the run tool exactly as requested, then report completion after the tool result.",
		CatalogApproval:   "manual", CatalogRisk: "elevated", CatalogMutation: "external",
		Arguments: func(_, _ string) map[string]interface{} {
			return map[string]interface{}{"command": command, "cwd": "."}
		},
		Observation: func(_, marker string) string {
			if outcome == "rejected" {
				return fmt.Sprintf("User rejected the run action on %q", command)
			}
			return marker
		},
	}
}

func agentToolRoundRunCommand(relativePath, marker string) (string, error) {
	if runtime.GOOS == "windows" {
		systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
		if systemRoot == "" {
			return "", errors.New("SystemRoot is unavailable for the packaged Agent run fixture")
		}
		executable := filepath.Join(systemRoot, "System32", "findstr.exe")
		info, err := os.Stat(executable)
		if err != nil {
			return "", fmt.Errorf("resolve packaged Agent run executable: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("packaged Agent run executable is not a regular file")
		}
		return fmt.Sprintf("%s /L /C:%s %s", filepath.ToSlash(executable), marker, relativePath), nil
	}
	const executable = "/usr/bin/grep"
	info, err := os.Stat(executable)
	if err != nil {
		return "", fmt.Errorf("resolve packaged Agent run executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("packaged Agent run executable is not a regular file")
	}
	return fmt.Sprintf("%s -F %s %s", executable, marker, relativePath), nil
}

func validateAgentToolRoundRenderer(
	renderer map[string]interface{},
	spec agentToolRoundSpec,
	expectedObservation string,
) (string, string, error) {
	for _, field := range []string{
		"ok",
		"rendererSubmitted",
		"agentModeConfigured",
		"storedProviderLoaded",
		"nativeToolCallObserved",
		"decisionObserved",
		"nativeProtocolResultSubmitted",
		"finalAssistantObserved",
	} {
		if renderer[field] != true {
			return "", "", fmt.Errorf("Agent tool-round renderer did not prove %s: %v", field, renderer["error"])
		}
	}
	if renderer["toolCallId"] != spec.ToolCallID || renderer["toolKind"] != spec.ToolKind {
		return "", "", fmt.Errorf("Agent tool-round renderer reported unexpected tool call ID %v", renderer["toolCallId"])
	}
	if renderer["approvalMode"] != spec.ApprovalMode || renderer["expectedDecision"] != spec.ExpectedDecision || renderer["outcome"] != spec.ExpectedOutcome {
		return "", "", fmt.Errorf(
			"Agent tool-round renderer returned the wrong decision contract: mode=%v decision=%v outcome=%v",
			renderer["approvalMode"], renderer["expectedDecision"], renderer["outcome"],
		)
	}
	if assistant, _ := renderer["assistantContent"].(string); !strings.Contains(assistant, spec.FinalAssistant) {
		return "", "", errors.New("Agent tool-round renderer did not retain the second provider completion")
	}
	if spec.ApprovalMode == "ask" {
		for _, field := range []string{
			"manualControlRequired",
			"manualControlRendered",
			"manualControlClicked",
			"manualControlClickEventObserved",
			"manualControlWasEnabled",
		} {
			if renderer[field] != true {
				return "", "", fmt.Errorf("Agent tool-round renderer did not prove %s", field)
			}
		}
		if renderer["manualControlAction"] != spec.ExpectedDecision ||
			renderer["manualControlCallId"] != spec.ToolCallID ||
			renderer["manualControlKind"] != spec.ToolKind {
			return "", "", errors.New("Agent tool-round renderer clicked the wrong manual control")
		}
	} else if renderer["manualControlRequired"] != false || renderer["manualControlClicked"] == true {
		return "", "", errors.New("auto-approved Agent round unexpectedly used a manual control")
	}

	if spec.ExpectedOutcome == "rejected" {
		for _, field := range []string{
			"approvalObserved",
			"approvalPrecededExecution",
			"backendExecutionObserved",
			"executionUsageObserved",
			"observationSubmitted",
		} {
			if renderer[field] != false {
				return "", "", fmt.Errorf("rejected Agent round unexpectedly reported %s", field)
			}
		}
		if renderer["rejectionSubmitted"] != true {
			return "", "", errors.New("rejected Agent round did not submit a native rejection")
		}
		if rejection, _ := renderer["rejection"].(string); !strings.Contains(rejection, expectedObservation) {
			return "", "", errors.New("rejected Agent round lost the rejection observation")
		}
		for _, field := range []string{"usageUnitId", "usageSessionId", "usageOperation"} {
			if value, present := renderer[field]; present && value != nil && value != "" {
				return "", "", fmt.Errorf("rejected Agent round unexpectedly returned %s", field)
			}
		}
		return "", "", nil
	}

	for _, field := range []string{
		"approvalObserved",
		"approvalPrecededExecution",
		"backendExecutionObserved",
		"executionUsageObserved",
		"usageSuccess",
		"usageSessionMatchesRequest",
		"usageObservationMatchesResult",
		"observationSubmitted",
	} {
		if renderer[field] != true {
			return "", "", fmt.Errorf("Agent tool-round renderer did not prove %s: %v", field, renderer["error"])
		}
	}
	if renderer["rejectionSubmitted"] != false {
		return "", "", errors.New("executed Agent round unexpectedly submitted a rejection")
	}
	usageUnitID, _ := renderer["usageUnitId"].(string)
	usageSessionID, _ := renderer["usageSessionId"].(string)
	if strings.TrimSpace(usageUnitID) == "" || strings.TrimSpace(usageSessionID) == "" {
		return "", "", errors.New("Agent tool-round renderer did not retain backend usage identity")
	}
	if renderer["usageOperation"] != spec.ToolKind || renderer["usagePending"] != false {
		return "", "", fmt.Errorf("Agent tool-round renderer returned invalid terminal usage: operation=%v pending=%v", renderer["usageOperation"], renderer["usagePending"])
	}
	if spec.ToolKind == "run" {
		receiptID, _ := renderer["externalReceiptId"].(string)
		if strings.TrimSpace(receiptID) == "" || renderer["externalReceiptReversible"] != false || renderer["externalCompensation"] != "not-needed" {
			return "", "", errors.New("Agent run round did not retain its irreversible external receipt")
		}
	}
	if observation, _ := renderer["observation"].(string); !strings.Contains(observation, expectedObservation) {
		return "", "", errors.New("Agent tool-round renderer observation did not contain the backend result marker")
	}
	return usageUnitID, usageSessionID, nil
}

func (s *server) runAgentToolRoundProbe(cmd command) (result interface{}, returnErr error) {
	workspace, fixturePath, err := resolveAgentToolRoundFixture(cmd)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(fixturePath, []byte(cmd.Marker+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write Agent tool-round fixture: %w", err)
	}
	readSpec := readAgentToolRoundSpec()
	searchSpec := searchAgentToolRoundSpec()
	readRound, err := s.runSingleAgentToolRoundProbe(cmd, readSpec, nil)
	if err != nil {
		return nil, fmt.Errorf("read auto round: %w", err)
	}
	baseline, err := fingerprintAgentToolRoundWorkspace(workspace)
	if err != nil {
		return nil, fmt.Errorf("fingerprint Agent workspace before search round: %w", err)
	}
	searchRound, err := s.runSingleAgentToolRoundProbe(cmd, searchSpec, nil)
	if err != nil {
		return nil, fmt.Errorf("search auto round: %w", err)
	}
	after, err := fingerprintAgentToolRoundWorkspace(workspace)
	if err != nil {
		return nil, fmt.Errorf("fingerprint Agent workspace after search round: %w", err)
	}
	if baseline != after {
		return nil, errors.New("search Agent round modified the workspace")
	}

	roundToken, err := nextToken()
	if err != nil {
		return nil, err
	}
	writeApprovePath := filepath.Join(workspace, "agent-write-approve-"+roundToken[:12]+".txt")
	writeRejectPath := filepath.Join(workspace, "agent-write-reject-"+roundToken[:12]+".txt")
	for _, target := range []string{writeApprovePath, writeRejectPath} {
		if _, err := os.Lstat(target); err == nil {
			return nil, fmt.Errorf("Agent write target unexpectedly exists: %s", filepath.Base(target))
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect Agent write target: %w", err)
		}
	}

	writeApproveContent := cmd.Marker + "_WRITE_APPROVE_CONTENT\n"
	writeApproveCmd := cmd
	writeApproveCmd.Path = writeApprovePath
	writeApproveCmd.Marker = writeApproveContent
	writeApproveBaseline, err := fingerprintAgentToolRoundWorkspace(workspace, writeApprovePath)
	if err != nil {
		return nil, fmt.Errorf("fingerprint workspace before approved write: %w", err)
	}
	writeApproveSpec := writeAgentToolRoundSpec("writeManualApprove", "approve", "executed")
	writeApproveRound, err := s.runSingleAgentToolRoundProbe(
		writeApproveCmd,
		writeApproveSpec,
		&agentNativeApprovalContract{
			Expectation: services.AgentNativeApprovalExpectationForE2E{
				ToolKind: services.AgentNativeApprovalToolWriteForE2E,
				Decision: true, WritePath: writeApprovePath, WriteSize: int64(len([]byte(writeApproveContent))),
			},
			ExpectCall: true,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("write manual approve round: %w", err)
	}
	written, err := os.ReadFile(writeApprovePath)
	if err != nil {
		return nil, fmt.Errorf("read approved Agent write target: %w", err)
	}
	if string(written) != writeApproveContent {
		return nil, errors.New("approved Agent write target does not match the exact requested content")
	}
	writeApproveAfter, err := fingerprintAgentToolRoundWorkspace(workspace, writeApprovePath)
	if err != nil {
		return nil, fmt.Errorf("fingerprint workspace after approved write: %w", err)
	}
	if writeApproveBaseline != writeApproveAfter {
		return nil, errors.New("approved Agent write changed an unrelated workspace file")
	}
	writeApproveDigest := sha256.Sum256(written)
	writeApproveRound["beforeExists"] = false
	writeApproveRound["afterExists"] = true
	writeApproveRound["afterSha256"] = hex.EncodeToString(writeApproveDigest[:])
	writeApproveRound["expectedContentSha256"] = hex.EncodeToString(writeApproveDigest[:])
	writeApproveRound["diskMatchesRequestedContent"] = true
	writeApproveRound["unrelatedWorkspaceUnchanged"] = true

	writeRejectCmd := cmd
	writeRejectCmd.Path = writeRejectPath
	writeRejectCmd.Marker = cmd.Marker + "_WRITE_REJECT_CONTENT\n"
	writeRejectBaseline, err := fingerprintAgentToolRoundWorkspace(workspace)
	if err != nil {
		return nil, fmt.Errorf("fingerprint workspace before rejected write: %w", err)
	}
	writeRejectSpec := writeAgentToolRoundSpec("writeManualReject", "reject", "rejected")
	writeRejectRound, err := s.runSingleAgentToolRoundProbe(
		writeRejectCmd,
		writeRejectSpec,
		&agentNativeApprovalContract{
			Expectation: services.AgentNativeApprovalExpectationForE2E{
				ToolKind: services.AgentNativeApprovalToolWriteForE2E,
				Decision: false, WritePath: writeRejectPath, WriteSize: int64(len([]byte(writeRejectCmd.Marker))),
			},
			ExpectCall: false,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("write manual reject round: %w", err)
	}
	if _, err := os.Lstat(writeRejectPath); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("rejected Agent write created or changed its target")
	}
	writeRejectAfter, err := fingerprintAgentToolRoundWorkspace(workspace)
	if err != nil {
		return nil, fmt.Errorf("fingerprint workspace after rejected write: %w", err)
	}
	if writeRejectBaseline != writeRejectAfter {
		return nil, errors.New("rejected Agent write changed the workspace")
	}
	writeRejectRound["beforeExists"] = false
	writeRejectRound["afterExists"] = false
	writeRejectRound["diskUnchanged"] = true
	writeRejectRound["workspaceUnchanged"] = true

	relativeFixture, err := filepath.Rel(workspace, fixturePath)
	if err != nil {
		return nil, fmt.Errorf("resolve Agent run fixture: %w", err)
	}
	runCommand, err := agentToolRoundRunCommand(filepath.ToSlash(relativeFixture), cmd.Marker)
	if err != nil {
		return nil, err
	}
	runCheck := s.services.Agent.CheckCommand(runCommand)
	if runCheck.Blocked {
		return nil, fmt.Errorf("packaged Agent run fixture is blocked: %s", runCheck.BlockReason)
	}
	runBaseline, err := fingerprintAgentToolRoundWorkspace(workspace)
	if err != nil {
		return nil, fmt.Errorf("fingerprint workspace before approved run: %w", err)
	}
	runApproveSpec := runAgentToolRoundSpec("runManualApprove", "approve", "executed", runCommand)
	runApproveRound, err := s.runSingleAgentToolRoundProbe(
		cmd,
		runApproveSpec,
		&agentNativeApprovalContract{
			Expectation: services.AgentNativeApprovalExpectationForE2E{
				ToolKind: services.AgentNativeApprovalToolRunForE2E,
				Decision: true, RunCommand: runCommand, RunCwd: workspace, RunRiskLevel: runCheck.RiskLevel,
			},
			ExpectCall: true,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("run manual approve round: %w", err)
	}
	runAfter, err := fingerprintAgentToolRoundWorkspace(workspace)
	if err != nil {
		return nil, fmt.Errorf("fingerprint workspace after approved run: %w", err)
	}
	if runBaseline != runAfter {
		return nil, errors.New("approved Agent run changed the workspace")
	}
	runApproveRound["processOutputObserved"] = strings.Contains(fmt.Sprint(runApproveRound["backendObservation"]), cmd.Marker)
	runApproveRound["workspaceUnchanged"] = true

	runRejectBaseline, err := fingerprintAgentToolRoundWorkspace(workspace)
	if err != nil {
		return nil, fmt.Errorf("fingerprint workspace before rejected run: %w", err)
	}
	runRejectSpec := runAgentToolRoundSpec("runManualReject", "reject", "rejected", runCommand)
	runRejectRound, err := s.runSingleAgentToolRoundProbe(
		cmd,
		runRejectSpec,
		&agentNativeApprovalContract{
			Expectation: services.AgentNativeApprovalExpectationForE2E{
				ToolKind: services.AgentNativeApprovalToolRunForE2E,
				Decision: false, RunCommand: runCommand, RunCwd: workspace, RunRiskLevel: runCheck.RiskLevel,
			},
			ExpectCall: false,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("run manual reject round: %w", err)
	}
	runRejectAfter, err := fingerprintAgentToolRoundWorkspace(workspace)
	if err != nil {
		return nil, fmt.Errorf("fingerprint workspace after rejected run: %w", err)
	}
	if runRejectBaseline != runRejectAfter {
		return nil, errors.New("rejected Agent run changed the workspace")
	}
	runRejectRound["processOutputObserved"] = false
	runRejectRound["workspaceUnchanged"] = true

	return map[string]interface{}{
		"readAuto":           readRound,
		"searchAuto":         searchRound,
		"writeManualApprove": writeApproveRound,
		"writeManualReject":  writeRejectRound,
		"runManualApprove":   runApproveRound,
		"runManualReject":    runRejectRound,
		"workspaceUnchanged": true,
	}, nil
}

func resolveAgentToolRoundFixture(cmd command) (string, string, error) {
	if cmd.Workspace == "" || cmd.Path == "" || cmd.Marker == "" {
		return "", "", errors.New("Agent tool-round probe requires workspace, path, and marker")
	}
	workspace, err := filepath.Abs(cmd.Workspace)
	if err != nil {
		return "", "", fmt.Errorf("resolve Agent tool-round workspace: %w", err)
	}
	fixturePath, err := filepath.Abs(cmd.Path)
	if err != nil {
		return "", "", fmt.Errorf("resolve Agent tool-round fixture path: %w", err)
	}
	relativePath, err := filepath.Rel(workspace, fixturePath)
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return "", "", errors.New("Agent tool-round fixture must be inside the active workspace")
	}
	return workspace, fixturePath, nil
}

func fingerprintAgentToolRoundWorkspace(workspace string, ignoredPaths ...string) (string, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return "", err
	}
	defer root.Close()
	ignored := make(map[string]struct{}, len(ignoredPaths))
	for _, value := range ignoredPaths {
		path := value
		if filepath.IsAbs(path) {
			path, err = filepath.Rel(workspace, path)
			if err != nil || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("ignored fingerprint path is outside the workspace: %q", value)
			}
		}
		ignored[filepath.ToSlash(filepath.Clean(path))] = struct{}{}
	}
	digest := sha256.New()
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." || entry.IsDir() {
			return nil
		}
		if _, skip := ignored[filepath.ToSlash(filepath.Clean(path))]; skip {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace fingerprint rejects symlink %q", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("workspace fingerprint rejects non-regular file %q", path)
		}
		_, _ = io.WriteString(digest, filepath.ToSlash(path))
		digest.Write([]byte{0})
		file, err := root.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		digest.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (s *server) runSingleAgentToolRoundProbe(
	cmd command,
	spec agentToolRoundSpec,
	nativeApproval *agentNativeApprovalContract,
) (result map[string]interface{}, returnErr error) {
	if s.services.Settings == nil || s.services.ExecJS == nil {
		return nil, errors.New("Agent tool-round renderer automation is not fully wired")
	}
	if s.services.Agent == nil {
		return nil, errors.New("Agent tool-round backend authority is not wired")
	}
	workspace, fixturePath, err := resolveAgentToolRoundFixture(cmd)
	if err != nil {
		return nil, err
	}
	relativePath, err := filepath.Rel(workspace, fixturePath)
	if err != nil {
		return nil, fmt.Errorf("resolve Agent tool-round relative fixture: %w", err)
	}
	relativePath = filepath.ToSlash(relativePath)

	arguments := spec.Arguments(relativePath, cmd.Marker)
	toolArguments, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("encode Agent tool arguments: %w", err)
	}
	expectedObservation := spec.Observation(relativePath, cmd.Marker)
	if strings.TrimSpace(expectedObservation) == "" {
		return nil, errors.New("Agent tool-round expected observation is empty")
	}

	var providerMu sync.Mutex
	providerRequests := make([]map[string]interface{}, 0, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected provider request", http.StatusNotFound)
			return
		}
		var body map[string]interface{}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, bodyLimit))
		if err := decoder.Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		providerMu.Lock()
		requestIndex := len(providerRequests)
		providerRequests = append(providerRequests, body)
		providerMu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		var payload map[string]interface{}
		switch requestIndex {
		case 0:
			payload = map[string]interface{}{
				"choices": []interface{}{map[string]interface{}{
					"delta": map[string]interface{}{
						"tool_calls": []interface{}{map[string]interface{}{
							"index": 0,
							"id":    spec.ToolCallID,
							"type":  "function",
							"function": map[string]interface{}{
								"name":      spec.ToolKind,
								"arguments": string(toolArguments),
							},
						}},
					},
				}},
			}
		case 1:
			if protocolErr := validateOpenAINativeToolResultRequest(
				body,
				spec.ToolCallID,
				spec.ToolKind,
				string(toolArguments),
				expectedObservation,
			); protocolErr != nil {
				http.Error(w, protocolErr.Error(), http.StatusBadRequest)
				return
			}
			payload = map[string]interface{}{
				"choices": []interface{}{map[string]interface{}{
					"delta": map[string]interface{}{"content": spec.FinalAssistant},
				}},
			}
		default:
			http.Error(w, "unexpected third provider turn", http.StatusConflict)
			return
		}
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			http.Error(w, marshalErr.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	}))
	defer provider.Close()

	originalSettings, err := s.services.Settings.LoadSettings()
	if err != nil {
		return nil, fmt.Errorf("load settings before Agent tool-round probe: %w", err)
	}
	runID, err := nextToken()
	if err != nil {
		return nil, err
	}
	providerID := "packaged-agent-" + spec.ToolKind + "-" + runID[:12]
	probeSettings := originalSettings
	probeSettings.AIProviderConfigs = append(
		[]services.AIProviderConfig(nil),
		originalSettings.AIProviderConfigs...,
	)
	probeSettings.AIProviderConfigs = append(probeSettings.AIProviderConfigs, services.AIProviderConfig{
		ID:          providerID,
		Name:        "Packaged Agent Tool Round",
		Provider:    "openai",
		Protocol:    "openai",
		APIKey:      "packaged-e2e-key",
		BaseURL:     provider.URL,
		Model:       "packaged-agent-loopback",
		Temperature: 0,
		MaxTokens:   128,
	})
	probeSettings.ActiveAIConfigID = providerID
	probeSettings.AIBaseURL = provider.URL
	probeSettings.AIModel = "packaged-agent-loopback"
	probeSettings.AIProvider = "openai"
	probeSettings.Temperature = 0
	probeSettings.MaxTokens = 128
	if spec.ApprovalMode == "auto-approve" {
		probeSettings.AgentPermissionMode = "assist"
	} else {
		probeSettings.AgentPermissionMode = "always-ask"
	}
	expectedVersion := originalSettings.Version
	probeSettings.ExpectedVersion = &expectedVersion
	if err := s.services.Settings.SaveSettings(probeSettings); err != nil {
		return nil, fmt.Errorf("save Agent tool-round provider settings: %w", err)
	}
	defer func() {
		current, err := s.services.Settings.LoadSettings()
		if err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("reload settings after Agent tool-round probe: %w", err))
			return
		}
		restored := originalSettings
		restoreVersion := current.Version
		restored.ExpectedVersion = &restoreVersion
		if err := s.services.Settings.SaveSettings(restored); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("restore settings after Agent tool-round probe: %w", err))
		}
	}()

	var approvalProbe *services.AgentNativeApprovalProbeForE2E
	var restoreApproval func()
	if nativeApproval != nil {
		approvalProbe, restoreApproval, err = services.InstallAgentNativeApprovalSequenceForE2E(
			s.services.Agent,
			[]services.AgentNativeApprovalExpectationForE2E{nativeApproval.Expectation},
		)
		if err != nil {
			return nil, fmt.Errorf("install Agent native approval probe: %w", err)
		}
		defer func() {
			if restoreApproval != nil {
				restoreApproval()
			}
		}()
	}

	configuration, err := json.Marshal(map[string]string{
		"runId":                  runID,
		"providerId":             providerID,
		"providerBaseUrl":        provider.URL,
		"providerModel":          "packaged-agent-loopback",
		"prompt":                 spec.InitialUserPrompt,
		"toolKind":               spec.ToolKind,
		"approvalMode":           spec.ApprovalMode,
		"expectedDecision":       spec.ExpectedDecision,
		"expectedOutcome":        spec.ExpectedOutcome,
		"expectedUsageOperation": spec.ToolKind,
		"expectedToolCallId":     spec.ToolCallID,
		"expectedObservation":    expectedObservation,
		"expectedFinalAssistant": spec.FinalAssistant,
	})
	if err != nil {
		return nil, fmt.Errorf("encode Agent tool-round renderer configuration: %w", err)
	}
	rendererRaw, err := s.runRendererProbeWithExecutor(
		mainRendererExecutor(s.services.ExecJS),
		"__koyoriIdeRunAgentToolRoundProbe",
		agentToolRoundResultEvent,
		"Agent tool round",
		configuration,
	)
	if err != nil {
		return nil, err
	}
	nativeApprovalEvidence := map[string]interface{}{
		"backendNativeApprovalObserved":  false,
		"backendNativeApprovalCallCount": 0,
	}
	if approvalProbe != nil {
		restoreApproval()
		restoreApproval = nil
		nativeApprovalEvidence, err = validateAgentNativeApprovalProbe(
			approvalProbe.Snapshot(),
			nativeApproval.Expectation,
			nativeApproval.ExpectCall,
		)
		if err != nil {
			return nil, err
		}
	}
	renderer, ok := rendererRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("Agent tool-round renderer returned %T", rendererRaw)
	}
	usageUnitID, usageSessionID, err := validateAgentToolRoundRenderer(
		renderer,
		spec,
		expectedObservation,
	)
	if err != nil {
		return nil, err
	}

	providerMu.Lock()
	captured := append([]map[string]interface{}(nil), providerRequests...)
	providerMu.Unlock()
	if len(captured) != 2 {
		return nil, fmt.Errorf("Agent loopback provider captured %d requests, want 2", len(captured))
	}
	firstOfferedTool := providerRequestHasTool(captured[0], spec.ToolKind)
	firstContainedUserTurn := providerRequestMessageContains(captured[0], "user", spec.InitialUserPrompt)
	secondNativeProtocol := validateOpenAINativeToolResultRequest(
		captured[1],
		spec.ToolCallID,
		spec.ToolKind,
		string(toolArguments),
		expectedObservation,
	) == nil
	if !firstOfferedTool || !firstContainedUserTurn || !secondNativeProtocol {
		return nil, fmt.Errorf(
			"Agent provider request chain was incomplete: tool=%t userTurn=%t nativeToolResult=%t",
			firstOfferedTool,
			firstContainedUserTurn,
			secondNativeProtocol,
		)
	}
	backendPolicyObserved, err := validateAgentToolRoundCatalog(s.services.Agent, spec)
	if err != nil {
		return nil, err
	}

	roundResult := map[string]interface{}{
		"ok":                                  true,
		"round":                               spec.Name,
		"toolKind":                            spec.ToolKind,
		"approvalMode":                        spec.ApprovalMode,
		"expectedDecision":                    spec.ExpectedDecision,
		"outcome":                             spec.ExpectedOutcome,
		"providerRequestCount":                len(captured),
		"firstRequestOfferedTool":             firstOfferedTool,
		"firstRequestContainedUserTurn":       firstContainedUserTurn,
		"backendApprovalPolicyObserved":       backendPolicyObserved,
		"backendCatalogPolicyObserved":        backendPolicyObserved,
		"nativeToolCallObserved":              renderer["nativeToolCallObserved"],
		"decisionObserved":                    renderer["decisionObserved"],
		"approvalObserved":                    renderer["approvalObserved"],
		"approvalPrecededExecution":           renderer["approvalPrecededExecution"],
		"backendCapabilityExecutionObserved":  renderer["backendExecutionObserved"],
		"executionUsageObserved":              renderer["executionUsageObserved"],
		"usageUnitId":                         usageUnitID,
		"usageSessionId":                      usageSessionID,
		"usageOperation":                      renderer["usageOperation"],
		"usageSuccess":                        renderer["usageSuccess"],
		"usagePending":                        renderer["usagePending"],
		"usageSessionMatchesRequest":          renderer["usageSessionMatchesRequest"],
		"usageObservationMatchesResult":       renderer["usageObservationMatchesResult"],
		"externalReceiptId":                   renderer["externalReceiptId"],
		"externalReceiptReversible":           renderer["externalReceiptReversible"],
		"externalCompensation":                renderer["externalCompensation"],
		"nativeProtocolResultSubmitted":       renderer["nativeProtocolResultSubmitted"],
		"backendObservation":                  renderer["observation"],
		"backendRejection":                    renderer["rejection"],
		"observationSubmitted":                renderer["observationSubmitted"],
		"rejectionSubmitted":                  renderer["rejectionSubmitted"],
		"secondRequestContainedObservation":   secondNativeProtocol,
		"secondRequestUsedNativeToolProtocol": secondNativeProtocol,
		"finalAssistantObserved":              true,
		"finalAssistant":                      renderer["assistantContent"],
		"toolCallId":                          spec.ToolCallID,
		"fixturePath":                         relativePath,
		"manualControlRequired":               renderer["manualControlRequired"],
		"manualControlRendered":               renderer["manualControlRendered"],
		"manualControlClicked":                renderer["manualControlClicked"],
		"manualControlClickEventObserved":     renderer["manualControlClickEventObserved"],
		"manualControlWasEnabled":             renderer["manualControlWasEnabled"],
		"manualControlAction":                 renderer["manualControlAction"],
		"manualControlCallId":                 renderer["manualControlCallId"],
		"manualControlKind":                   renderer["manualControlKind"],
		"renderer":                            renderer,
	}
	for key, value := range nativeApprovalEvidence {
		roundResult[key] = value
	}
	return roundResult, nil
}

func validateAgentToolRoundCatalog(agent *services.AgentService, spec agentToolRoundSpec) (bool, error) {
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		return false, fmt.Errorf("load Agent tool catalog for %s: %w", spec.Name, err)
	}
	for _, tool := range catalog.Tools {
		if tool.ID != spec.ToolKind {
			continue
		}
		if tool.Source != "builtin" || tool.Approval != spec.CatalogApproval || tool.Risk != spec.CatalogRisk || tool.Mutation != spec.CatalogMutation {
			return false, fmt.Errorf(
				"Agent %s ToolDef policy changed: source=%s approval=%s risk=%s mutation=%s",
				spec.ToolKind, tool.Source, tool.Approval, tool.Risk, tool.Mutation,
			)
		}
		return true, nil
	}
	return false, fmt.Errorf("Agent catalog did not contain %s", spec.ToolKind)
}

func validateAgentNativeApprovalProbe(
	snapshot services.AgentNativeApprovalSnapshotForE2E,
	expectation services.AgentNativeApprovalExpectationForE2E,
	expectCall bool,
) (map[string]interface{}, error) {
	if snapshot.Expected != 1 || !snapshot.Restored {
		return nil, fmt.Errorf("Agent native approval probe lifecycle is incomplete: %+v", snapshot)
	}
	result := map[string]interface{}{
		"backendApprovalSource":              "e2e-exact-native-approver",
		"backendNativeApprovalObserved":      expectCall,
		"backendNativeApprovalCallCount":     len(snapshot.Calls),
		"backendNativeApprovalExpectedCalls": 0,
	}
	if !expectCall {
		if snapshot.Consumed != 0 || snapshot.Remaining != 1 || snapshot.Complete || len(snapshot.Calls) != 0 {
			return nil, fmt.Errorf("rejected renderer round reached backend native approval: %+v", snapshot)
		}
		return result, nil
	}
	result["backendNativeApprovalExpectedCalls"] = 1
	if snapshot.Consumed != 1 || snapshot.Remaining != 0 || !snapshot.Complete || len(snapshot.Calls) != 1 {
		return nil, fmt.Errorf("Agent native approval was not consumed exactly once: %+v", snapshot)
	}
	call := snapshot.Calls[0]
	if !call.Matched || !call.Consumed || call.Decision != expectation.Decision || call.ToolKind != expectation.ToolKind {
		return nil, fmt.Errorf("Agent native approval identity or decision changed: %+v", call)
	}
	result["backendNativeApprovalDecision"] = call.Decision
	result["backendNativeApprovalSequence"] = call.Sequence
	switch expectation.ToolKind {
	case services.AgentNativeApprovalToolWriteForE2E:
		result["approvedPath"] = call.WritePath
		result["approvedBytes"] = call.WriteSize
	case services.AgentNativeApprovalToolRunForE2E:
		result["approvedCommand"] = call.RunCommand
		result["approvedCwd"] = call.RunCwd
		result["approvedRisk"] = call.RunRiskLevel
	default:
		return nil, fmt.Errorf("Agent native approval returned unsupported tool kind %q", expectation.ToolKind)
	}
	return result, nil
}

func (s *server) runExtensionAPIG13Probe(cmd command) (interface{}, error) {
	if s.services.ExecJS == nil {
		return nil, errors.New("G13 extension API automation is not fully wired")
	}
	runID, err := nextToken()
	if err != nil {
		return nil, err
	}
	configuration, err := json.Marshal(map[string]string{"runId": runID})
	if err != nil {
		return nil, fmt.Errorf("encode G13 extension API probe configuration: %w", err)
	}
	return s.runRendererProbeWithExecutor(
		mainRendererExecutor(s.services.ExecJS),
		"__koyoriIdeRunG13ExtensionApiProbe",
		extensionAPIResultEvent,
		"G13 extension API",
		configuration,
	)
}

// runTestExplorerG15Probe creates a real Go test package, then drives the
// packaged renderer's Test Explorer store through pass and fail exit codes.
func (s *server) runTestExplorerG15Probe(cmd command) (interface{}, error) {
	if s.services.Project == nil || s.services.ExecJS == nil {
		return nil, errors.New("G15 Test Explorer automation is not fully wired")
	}
	if cmd.Workspace == "" {
		return nil, errors.New("G15 Test Explorer probe requires workspace")
	}
	if _, err := s.services.Project.AddProject(cmd.Workspace); err != nil {
		return nil, fmt.Errorf("open workspace for G15 probe: %w", err)
	}
	dir := filepath.Join(cmd.Workspace, "g15-test-fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create G15 fixture: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module g15fixture\n\ngo 1.25.0\n"), 0o600); err != nil {
		return nil, err
	}
	content := "package fixture\n\nimport \"testing\"\n\nfunc TestPass(t *testing.T) {}\n\nfunc TestFail(t *testing.T) {\n\tt.Fatal(\"G15_EXPECTED_FAILURE\")\n}\n"
	testPath := filepath.Join(dir, "fixture_test.go")
	if err := os.WriteFile(testPath, []byte(content), 0o600); err != nil {
		return nil, err
	}
	passLine, failLine := -1, -1
	for index, line := range strings.Split(content, "\n") {
		switch {
		case strings.Contains(line, "func TestPass"):
			passLine = index
		case strings.Contains(line, "func TestFail"):
			failLine = index
		}
	}
	if passLine < 0 || failLine < 0 {
		return nil, errors.New("G15 fixture test identities were not found")
	}
	runID, err := nextToken()
	if err != nil {
		return nil, err
	}
	configuration, err := json.Marshal(map[string]interface{}{
		"runId":     runID,
		"workspace": cmd.Workspace,
		"filePath":  testPath,
		"content":   content,
		"passLine":  passLine,
		"failLine":  failLine,
	})
	if err != nil {
		return nil, fmt.Errorf("encode G15 Test Explorer probe: %w", err)
	}
	return s.runRendererProbeWithExecutor(
		mainRendererExecutor(s.services.ExecJS),
		"__koyoriIdeRunG15TestExplorerProbe",
		testExplorerResultEvent,
		"G15 Test Explorer",
		configuration,
	)
}

// runDebugG14Probe drives the real Delve DAP adapter inside the packaged
// process (P-level evidence for GOAL P9-G14): breakpoint -> stop -> nested
// variables expanded through adapter-owned references -> single step -> stop.
func (s *server) runDebugG14Probe(cmd command) (interface{}, error) {
	if s.services.Debug == nil || s.services.Project == nil {
		return nil, errors.New("debug-g14 automation is not fully wired")
	}
	if cmd.Workspace == "" {
		return nil, errors.New("debug-g14 probe requires workspace")
	}
	if _, err := s.services.Project.AddProject(cmd.Workspace); err != nil {
		return nil, fmt.Errorf("open workspace for debug probe: %w", err)
	}
	if _, err := exec.LookPath("dlv"); err != nil {
		return nil, errors.New("dlv not found; real Delve adapter probe skipped")
	}

	dir := filepath.Join(cmd.Workspace, "g14-delve-fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create debug fixture dir: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module g14fixture\n\ngo 1.25.0\n"), 0o600); err != nil {
		return nil, err
	}
	mainSrc := "package main\n\nimport \"fmt\"\n\ntype Inner struct {\n\tZ int\n}\n\ntype Outer struct {\n\tName string\n\tIn   Inner\n}\n\nfunc main() {\n\to := Outer{Name: \"hello\", In: Inner{Z: 42}}\n\tfmt.Println(o)\n}\n"
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(mainSrc), 0o600); err != nil {
		return nil, err
	}
	bpLine := 0
	for i, line := range strings.Split(mainSrc, "\n") {
		if strings.Contains(line, "fmt.Println(o)") {
			bpLine = i + 1
			break
		}
	}
	if bpLine == 0 {
		return nil, errors.New("could not locate breakpoint line")
	}
	if _, err := s.services.Debug.SetBreakpointEx(mainPath, bpLine, "", ""); err != nil {
		return nil, fmt.Errorf("SetBreakpointEx: %w", err)
	}
	info, err := s.services.Debug.LaunchPackage(dir)
	if err != nil {
		return nil, fmt.Errorf("LaunchPackage (real dlv): %w", err)
	}
	if info.Address == "" {
		return nil, fmt.Errorf("no dlv dap address: %+v", info)
	}
	defer s.services.Debug.Stop()

	// Wait for the breakpoint stop.
	if err := waitForDebugStop(s.services.Debug, 30*time.Second); err != nil {
		return nil, err
	}
	if err := s.services.Debug.RefreshStackAndLocals(); err != nil {
		return nil, fmt.Errorf("RefreshStackAndLocals: %w", err)
	}
	state := s.services.Debug.GetState()
	var outer *services.DebugVariable
	for i := range state.Locals {
		v := &state.Locals[i]
		if v.Name == "o" {
			outer = v
			break
		}
	}
	if outer == nil || outer.VariablesReference <= 0 {
		return nil, fmt.Errorf("Outer variable missing adapter-owned reference: %+v", state.Locals)
	}
	fields, err := s.services.Debug.GetVariables(outer.VariablesReference, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetVariables(outer): %w", err)
	}
	var inner *services.DebugVariable
	for i := range fields {
		v := &fields[i]
		if v.Name == "In" {
			inner = v
			break
		}
	}
	if inner == nil || inner.VariablesReference <= 0 {
		return nil, fmt.Errorf("nested In missing reference: %+v", fields)
	}
	innerFields, err := s.services.Debug.GetVariables(inner.VariablesReference, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetVariables(inner): %w", err)
	}
	zFound := false
	for _, v := range innerFields {
		if v.Name == "Z" && v.Value == "42" {
			zFound = true
		}
	}
	if !zFound {
		return nil, fmt.Errorf("Z != 42: %+v", innerFields)
	}
	if err := s.services.Debug.StepOver(); err != nil {
		return nil, fmt.Errorf("StepOver: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	advanced := false
	for time.Now().Before(deadline) {
		st := s.services.Debug.GetState()
		if st.Session.Stopped && st.Session.StopReason != "entry" {
			advanced = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !advanced {
		return nil, fmt.Errorf("StepOver did not produce a new stop")
	}
	return map[string]interface{}{
		"dlvLaunch":         true,
		"breakpointStop":    true,
		"nestedExpanded":    zFound,
		"singleStep":        true,
		"adapterReference":  outer.VariablesReference,
		"nestedReference":   inner.VariablesReference,
		"fixtureDir":        dir,
		"stopReason":        state.Session.StopReason,
		"adapterId":         info.AdapterID,
		"sourcePackId":      info.SourcePackID,
		"sourcePackVersion": info.SourcePackVersion,
	}, nil
}

func waitForDebugStop(d *services.DebugService, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st := d.GetState()
		if st.Session.Stopped || st.Session.StopReason == "entry" {
			return nil
		}
		time.Sleep(30 * time.Millisecond)
	}
	return errors.New("timed out waiting for debug stop")
}

// runTerminalReconnectProbe verifies the renderer-level G16 reconnect flow.
// The probe exits a real PTY, clicks the real tab action, and writes through
// the same session after reconnecting. It is only reachable from an e2e build.
func (s *server) runTerminalReconnectProbe(cmd command) (interface{}, error) {
	if s.services.ExecJS == nil || s.services.Terminal == nil {
		return nil, errors.New("terminal-reconnect automation is not fully wired")
	}
	if cmd.Workspace == "" {
		return nil, errors.New("terminal-reconnect probe requires workspace")
	}
	shell := "sh"
	exitInput := "exit 7\n"
	if runtime.GOOS == "windows" {
		shell = "cmd"
		exitInput = "exit 7\r\n"
	}
	runID, err := nextToken()
	if err != nil {
		return nil, err
	}
	configuration, err := json.Marshal(map[string]string{
		"runId":     runID,
		"workspace": cmd.Workspace,
		"shell":     shell,
		"exitInput": exitInput,
		"marker":    "KOYORI_IDE_G16_RECONNECT_OK",
	})
	if err != nil {
		return nil, fmt.Errorf("encode G16 terminal reconnect probe configuration: %w", err)
	}
	return s.runRendererProbe(
		"__koyoriIdeRunTerminalReconnectProbe",
		terminalReconnectResultEvent,
		"G16 terminal reconnect",
		configuration,
	)
}

// runTerminalExitProbe verifies the G16 exit-code protocol in the packaged
// process: illegal shell rejected (fail-closed), real PTY exit 7 delivered as
// a structured terminal:exited event, and resize accepted.
func (s *server) runTerminalExitProbe(cmd command) (interface{}, error) {
	if s.services.Terminal == nil {
		return nil, errors.New("terminal-exit automation is not fully wired")
	}
	// 1) non-whitelisted shell must be rejected before any process starts.
	if err := s.services.Terminal.StartSession("g16-illegal", cmd.Workspace, "fish"); err == nil {
		return nil, errors.New("illegal shell accepted; shell whitelist violated")
	}
	app := application.Get()
	if app == nil {
		return nil, errors.New("no application for terminal:exited events")
	}
	exitCh := make(chan map[string]interface{}, 1)
	remove := app.Event.On("terminal:exited", func(event *application.CustomEvent) {
		encoded, merr := json.Marshal(event.Data)
		if merr != nil {
			return
		}
		var payload map[string]interface{}
		if json.Unmarshal(encoded, &payload) != nil {
			return
		}
		if sid, _ := payload["sessionId"].(string); sid == "g16-exit" {
			select {
			case exitCh <- payload:
			default:
			}
		}
	})
	defer remove()

	// Use cmd.exe (whitelisted): its interactive shell exits deterministically
	// with the requested code, unlike PowerShell which swallows early `exit`.
	if err := s.services.Terminal.StartSession("g16-exit", cmd.Workspace, "cmd"); err != nil {
		return nil, fmt.Errorf("start cmd shell: %w", err)
	}
	defer s.services.Terminal.KillSession("g16-exit")
	if err := s.services.Terminal.ResizeSession("g16-exit", 100, 30); err != nil {
		return nil, fmt.Errorf("resize: %w", err)
	}
	// Let the shell banner finish before sending input; a command written
	// during startup can be swallowed by the banner.
	time.Sleep(1200 * time.Millisecond)
	// cmd exits with the given code via `exit 7` (CRLF line ending).
	if err := s.services.Terminal.WriteSession("g16-exit", "exit 7\r\n"); err != nil {
		return nil, fmt.Errorf("write exit command: %w", err)
	}
	select {
	case payload := <-exitCh:
		code, _ := payload["code"].(float64)
		if int(code) != 7 {
			return nil, fmt.Errorf("exit code = %v, want 7", code)
		}
		return map[string]interface{}{
			"illegalShellRejected": true,
			"resizeOk":             true,
			"exitEventReceived":    true,
			"exitCode":             int(code),
		}, nil
	case <-time.After(15 * time.Second):
		return nil, errors.New("timed out waiting for terminal:exited with code 7")
	}
}

// runGitWorktreeProbe verifies the G17 sibling-worktree flow in the packaged
// process: a worktree added inside the workspace succeeds, appears in
// `git worktree list`, and an out-of-workspace path is rejected.
func (s *server) runGitWorktreeProbe(cmd command) (interface{}, error) {
	if s.services.GitWorktree == nil || s.services.Project == nil || s.services.Git == nil {
		return nil, errors.New("git-worktree automation is not fully wired")
	}
	if cmd.Workspace == "" {
		return nil, errors.New("git-worktree probe requires workspace")
	}
	if _, err := s.services.Project.AddProject(cmd.Workspace); err != nil {
		return nil, fmt.Errorf("open workspace for git-worktree probe: %w", err)
	}
	repo := filepath.Join(cmd.Workspace, "g17-repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return nil, err
	}
	// Seed a file so InitRepo's initial commit has content and HEAD exists.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("g17 fixture\n"), 0o600); err != nil {
		return nil, err
	}
	if err := s.services.Git.InitRepo(repo); err != nil {
		return nil, fmt.Errorf("init repo: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sibling := filepath.Join(cmd.Workspace, "g17-sibling")
	if err := s.services.GitWorktree.AddWorktree(ctx, repo, sibling, "HEAD", services.AddWorktreeOptions{Detach: true}); err != nil {
		return nil, fmt.Errorf("add sibling worktree inside workspace: %w", err)
	}
	wts, err := s.services.GitWorktree.ListWorktrees(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	siblingFound := false
	for _, w := range wts {
		if filepath.Clean(w.Path) == filepath.Clean(sibling) {
			siblingFound = true
			break
		}
	}
	if !siblingFound {
		return nil, fmt.Errorf("sibling worktree not listed: %+v", wts)
	}

	// Out-of-workspace path must be rejected (safe roots = workspace root).
	outside := filepath.Join(os.TempDir(), "koyori-ide-g17-outside-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	defer os.RemoveAll(outside)
	if err := s.services.GitWorktree.AddWorktree(ctx, repo, outside, "HEAD", services.AddWorktreeOptions{Detach: true}); err == nil {
		return nil, errors.New("out-of-workspace worktree path was accepted (safe roots violated)")
	}
	return map[string]interface{}{
		"repoInitialized": true,
		"siblingCreated":  true,
		"siblingListed":   true,
		"outsideRejected": true,
	}, nil
}

// runGitRebaseProbe exercises the real interactive rebase service in the
// packaged process. The fixture history is prepared with git, while every
// rebase operation under test goes through GitRebaseService.
func (s *server) runGitRebaseProbe(cmd command) (interface{}, error) {
	if s.services.GitRebase == nil || s.services.Git == nil || s.services.Project == nil {
		return nil, errors.New("git-rebase automation is not fully wired")
	}
	if cmd.Workspace == "" {
		return nil, errors.New("git-rebase probe requires workspace")
	}
	if _, err := s.services.Project.AddProject(cmd.Workspace); err != nil {
		return nil, fmt.Errorf("open workspace for git-rebase probe: %w", err)
	}
	repo := filepath.Join(cmd.Workspace, "g17-rebase-repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return nil, fmt.Errorf("create rebase repo: %w", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("g17 rebase fixture\n"), 0o600); err != nil {
		return nil, fmt.Errorf("seed rebase repo: %w", err)
	}
	if err := s.services.Git.InitRepo(repo); err != nil {
		return nil, fmt.Errorf("init rebase repo: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	git := func(args ...string) (string, error) {
		return runE2EGitCommand(ctx, repo, args...)
	}
	// InitRepo uses an explicit author for its initial commit; set repository
	// identity before creating the fixture's subsequent commits.
	for _, config := range [][2]string{{"user.name", "koyori-ide-e2e"}, {"user.email", "koyori-ide-e2e@local"}} {
		if _, err := git("config", config[0], config[1]); err != nil {
			return nil, fmt.Errorf("configure fixture git identity: %w", err)
		}
	}
	base, err := git("rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve fixture base: %w", err)
	}
	base = strings.TrimSpace(base)
	if _, err := git("branch", "upstream", base); err != nil {
		return nil, fmt.Errorf("create upstream branch: %w", err)
	}
	if _, err := git("branch", "-M", "feature"); err != nil {
		return nil, fmt.Errorf("rename feature branch: %w", err)
	}
	for index, content := range []string{"feature one\n", "feature two\n"} {
		name := fmt.Sprintf("feature-%d.txt", index+1)
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", name, err)
		}
		if _, err := git("add", name); err != nil {
			return nil, fmt.Errorf("stage %s: %w", name, err)
		}
		if _, err := git("commit", "-m", fmt.Sprintf("feature %d", index+1)); err != nil {
			return nil, fmt.Errorf("commit feature %d: %w", index+1, err)
		}
	}

	actions, err := s.services.GitRebase.GetRebaseTodoList(ctx, repo, "upstream")
	if err != nil {
		return nil, fmt.Errorf("get rebase todo: %w", err)
	}
	if len(actions) != 2 || actions[0].Action != "pick" || actions[1].Action != "pick" {
		return nil, fmt.Errorf("unexpected rebase todo: %+v", actions)
	}
	if err := s.services.GitRebase.StartInteractiveRebase(ctx, repo, "upstream"); err != nil {
		return nil, fmt.Errorf("start interactive rebase: %w", err)
	}
	started, err := s.services.GitRebase.GetRebaseStatus(repo)
	if err != nil {
		return nil, fmt.Errorf("read started rebase status: %w", err)
	}
	if !started.InProgress || !started.Owned || started.Phase == "" {
		return nil, fmt.Errorf("started rebase status is not owned/in progress: %+v", started)
	}
	if err := s.services.GitRebase.ApplyRebaseActions(ctx, repo, actions); err != nil {
		return nil, fmt.Errorf("apply rebase actions: %w", err)
	}
	ready, err := s.services.GitRebase.GetRebaseStatus(repo)
	if err != nil {
		return nil, fmt.Errorf("read ready rebase status: %w", err)
	}
	if !ready.InProgress || !ready.Owned || ready.Phase != "ready" {
		return nil, fmt.Errorf("ready rebase status is invalid: %+v", ready)
	}
	if err := s.services.GitRebase.ContinueRebase(ctx, repo); err != nil {
		return nil, fmt.Errorf("continue rebase: %w", err)
	}
	inProgress, err := s.services.GitRebase.IsRebaseInProgress(repo)
	if err != nil {
		return nil, fmt.Errorf("check completed rebase: %w", err)
	}
	finalStatus, err := s.services.GitRebase.GetRebaseStatus(repo)
	if err != nil {
		return nil, fmt.Errorf("read completed rebase status: %w", err)
	}
	if inProgress || finalStatus.InProgress {
		return nil, fmt.Errorf("rebase remains in progress: inProgress=%t status=%+v", inProgress, finalStatus)
	}
	subjects, err := git("log", "--format=%s", "--reverse", "upstream..HEAD")
	if err != nil {
		return nil, fmt.Errorf("inspect rebased commits: %w", err)
	}
	if strings.TrimSpace(subjects) != "feature 1\nfeature 2" {
		return nil, fmt.Errorf("rebased commit subjects = %q", subjects)
	}
	return map[string]interface{}{
		"todoLoaded":         true,
		"rebaseStarted":      true,
		"actionsApplied":     true,
		"rebaseCompleted":    true,
		"noRebaseInProgress": true,
		"commitCount":        len(actions),
	}, nil
}

func runE2EGitCommand(ctx context.Context, repo string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// runAIDiffReceiptProbe verifies the G18 commit-receipt protocol in the
// packaged process: ApplyDiff commits once with a receipt (transaction id +
// on-disk hashes), the disk actually changes, and a second apply of the same
// diff is rejected (no duplicate commit).
func (s *server) runAIDiffReceiptProbe(cmd command) (interface{}, error) {
	if s.services.Diff == nil || s.services.Project == nil {
		return nil, errors.New("ai-diff-receipt automation is not fully wired")
	}
	if cmd.Workspace == "" {
		return nil, errors.New("ai-diff-receipt probe requires workspace")
	}
	if _, err := s.services.Project.AddProject(cmd.Workspace); err != nil {
		return nil, fmt.Errorf("open workspace for ai-diff-receipt probe: %w", err)
	}
	target := filepath.Join(cmd.Workspace, "g18-diff-target.txt")
	if err := os.WriteFile(target, []byte("old line\n"), 0o600); err != nil {
		return nil, err
	}
	diff := services.FileDiff{
		Path:       target,
		OldContent: "old line\n",
		NewContent: "new line\n",
	}

	first := s.services.Diff.ApplyDiff([]services.FileDiff{diff})
	if !first.Applied {
		return nil, fmt.Errorf("first ApplyDiff did not commit: %+v", first)
	}
	if first.TransactionID == "" {
		return nil, errors.New("commit receipt missing transactionId")
	}
	if len(first.FileHashes) != 1 {
		return nil, fmt.Errorf("commit receipt missing file hashes: %+v", first.FileHashes)
	}
	disk, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	if string(disk) != "new line\n" {
		return nil, fmt.Errorf("disk content = %q, want %q (commit did not persist)", string(disk), "new line\n")
	}

	// A second apply of the same diff must be rejected (baseline no longer
	// matches) — never a duplicate disk commit.
	second := s.services.Diff.ApplyDiff([]services.FileDiff{diff})
	if second.Applied {
		return nil, errors.New("second ApplyDiff committed again; duplicate commit not prevented")
	}
	disk2, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	if string(disk2) != "new line\n" {
		return nil, fmt.Errorf("disk changed after rejected second apply: %q", string(disk2))
	}
	return map[string]interface{}{
		"committedOnce":         true,
		"transactionId":         first.TransactionID,
		"fileHashesRecorded":    len(first.FileHashes) == 1,
		"diskMatchesCommit":     string(disk) == "new line\n",
		"duplicateRejected":     !second.Applied,
		"diskUnchangedOnReject": string(disk2) == "new line\n",
	}, nil
}

// runAIDiffReceiptRecoveryProbe is called by the packaged driver only after
// the artifact has been killed and relaunched. It proves that the new service
// instance can recover the durable receipt, verify disk, and reject the old
// diff without a second write.
func (s *server) runAIDiffReceiptRecoveryProbe(cmd command) (interface{}, error) {
	if s.services.Diff == nil || s.services.Project == nil {
		return nil, errors.New("ai-diff-receipt recovery automation is not fully wired")
	}
	if cmd.Workspace == "" {
		return nil, errors.New("ai-diff-receipt recovery probe requires workspace")
	}
	if _, err := s.services.Project.AddProject(cmd.Workspace); err != nil {
		return nil, fmt.Errorf("open workspace for ai-diff-receipt recovery probe: %w", err)
	}
	receipt, err := s.services.Diff.GetLatestCommitReceipt()
	if err != nil {
		return nil, fmt.Errorf("load durable commit receipt after restart: %w", err)
	}
	target := filepath.Join(cmd.Workspace, "g18-diff-target.txt")
	diskBefore, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(diskBefore)
	diskHash := hex.EncodeToString(digest[:])
	if receipt.FileHashes[target] != diskHash {
		return nil, fmt.Errorf("recovered receipt hash %q does not match disk hash %q", receipt.FileHashes[target], diskHash)
	}
	if cmd.Expected != "" && receipt.TransactionID != cmd.Expected {
		return nil, fmt.Errorf("recovered transaction id %q differs from %q", receipt.TransactionID, cmd.Expected)
	}
	diff := services.FileDiff{
		Path:       target,
		OldContent: "old line\n",
		NewContent: "new line\n",
	}
	second := s.services.Diff.ApplyDiff([]services.FileDiff{diff})
	diskAfter, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"receiptRecovered":        true,
		"transactionIdStable":     cmd.Expected == "" || receipt.TransactionID == cmd.Expected,
		"fileHashesMatchDisk":     receipt.FileHashes[target] == diskHash,
		"duplicateRejected":       !second.Applied,
		"diskUnchangedOnReject":   string(diskBefore) == string(diskAfter),
		"receiptWorkspaceMatches": receipt.WorkspaceRoot == cmd.Workspace,
	}, nil
}
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
