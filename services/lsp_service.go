package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// G-FEAT-02 + prompt-8: Offline language intelligence via LSP.
//
// Go: gopls. TypeScript/JavaScript: typescript-language-server or vtsls
// (NOT raw tsserver — that speaks a proprietary protocol, BUG-IDE-02).
//
// Document sync: syncDocument sends didOpen / didChange with monotonic
// versions so completions reflect the live buffer (BUG-IDE-01).
//
// Graceful fallback: if a server is not installed or not running, query
// methods return empty results (not errors) so the editor degrades smoothly.

// LSPServerStatus reports the availability and state of a language server.
type LSPServerStatus struct {
	Language          string `json:"language"`
	Available         bool   `json:"available"`
	Running           bool   `json:"running"`
	ServerPath        string `json:"serverPath"`
	Version           string `json:"version"`
	SourcePackID      string `json:"sourcePackId,omitempty"`
	SourcePackVersion string `json:"sourcePackVersion,omitempty"`
	// Framework identifies workspace-aware configuration without inventing a
	// separate React language server. Values are vue, angular or react.
	Framework string `json:"framework,omitempty"`
	// WorkspaceRoot is the root that supplied the project-local server.
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	// InstallHint is an explicit, user-run command for missing servers. The
	// service never installs language servers automatically.
	InstallHint string `json:"installHint,omitempty"`
	// LastError is a short human message when start/initialize last failed
	// (prompt-8 Task 8-D). Empty when healthy / never started.
	LastError string `json:"lastError,omitempty"`
	// ServerKind labels the binary (gopls / typescript-language-server / vtsls).
	ServerKind string `json:"serverKind,omitempty"`
}

// LSPCompletionRequest is sent from the frontend to query an LSP server.
type LSPCompletionRequest struct {
	Language    string   `json:"language"`
	FilePath    string   `json:"filePath"`
	Line        int      `json:"line"`
	Column      int      `json:"column"`
	EndLine     int      `json:"endLine,omitempty"`
	EndColumn   int      `json:"endColumn,omitempty"`
	Only        []string `json:"only,omitempty"`
	Content     string   `json:"content"` // full file content
	TriggerKind int      `json:"triggerKind,omitempty"`
	TriggerChar string   `json:"triggerCharacter,omitempty"`
}

// LSPLabelDetails 对应 LSP CompletionItemLabelDetails。
//   - Detail：紧贴 label 显示的短签名（如 "(a, b string)"）。
//   - Description：附加描述（如返回类型、所属包），呈现到文档面板。
//
// Priority 2 (prompt-1.md)：与 frontend/src/types/index.ts 中
// LSPCompletionItem.labelDetails 字段保持结构一致。
type LSPLabelDetails struct {
	Detail      string `json:"detail,omitempty"`
	Description string `json:"description,omitempty"`
}

// LSPCompletionItem represents a single completion item returned by the LSP.
// AdditionalEdits carry auto-import / additionalTextEdits (prompt-10 10-I).
//
// Priority 2 (prompt-1.md)：InsertTextFormat / LabelDetails 用于端到端传递
// snippet 支持与函数签名信息。InsertTextFormat=2 表示 insertText 为 snippet。
type LSPCompletionItem struct {
	Label            string           `json:"label"`
	Kind             int              `json:"kind"`
	Detail           string           `json:"detail"`
	InsertText       *string          `json:"insertText,omitempty"`
	TextEditText     *string          `json:"textEditText,omitempty"`
	InsertTextFormat int              `json:"insertTextFormat,omitempty"`
	InsertTextMode   *int             `json:"insertTextMode,omitempty"`
	SortText         *string          `json:"sortText,omitempty"`
	FilterText       *string          `json:"filterText,omitempty"`
	Preselect        bool             `json:"preselect,omitempty"`
	Deprecated       bool             `json:"deprecated,omitempty"`
	Tags             []int            `json:"tags,omitempty"`
	Documentation    interface{}      `json:"documentation,omitempty"`
	Data             json.RawMessage  `json:"data,omitempty"`
	CommitCharacters *[]string        `json:"commitCharacters,omitempty"`
	TextEdit         *TextEdit        `json:"textEdit,omitempty"`
	LabelDetails     *LSPLabelDetails `json:"labelDetails,omitempty"`
	AdditionalEdits  []TextEdit       `json:"additionalEdits,omitempty"`
}

// LSPCompletionResponse preserves CompletionList metadata without changing the
// existing GetCompletions slice return type used by current Wails bindings.
type LSPCompletionResponse struct {
	Items        []LSPCompletionItem `json:"items"`
	IsIncomplete bool                `json:"isIncomplete"`
}

// Diagnostic represents a single LSP diagnostic (error/warning).
type Diagnostic struct {
	Line               int                               `json:"line"`
	Column             int                               `json:"column"`
	EndLine            int                               `json:"endLine"`
	EndCol             int                               `json:"endColumn"`
	Range              LSPRange                          `json:"range"`
	Severity           int                               `json:"severity"`
	Message            string                            `json:"message"`
	Source             string                            `json:"source"`
	Code               json.RawMessage                   `json:"code,omitempty"`
	CodeDescription    *LSPDiagnosticCodeDescription     `json:"codeDescription,omitempty"`
	RelatedInformation []LSPDiagnosticRelatedInformation `json:"relatedInformation,omitempty"`
	Tags               []int                             `json:"tags,omitempty"`
	Data               json.RawMessage                   `json:"data,omitempty"`
}

type LSPDiagnosticCodeDescription struct {
	Href string `json:"href"`
}

type LSPDiagnosticLocation struct {
	URI   string   `json:"uri"`
	Range LSPRange `json:"range"`
}

type LSPDiagnosticRelatedInformation struct {
	Location LSPDiagnosticLocation `json:"location"`
	Message  string                `json:"message"`
}

type lspDiagnosticJSON struct {
	Range              LSPRange                          `json:"range"`
	Severity           int                               `json:"severity"`
	Message            string                            `json:"message"`
	Source             string                            `json:"source"`
	Code               json.RawMessage                   `json:"code,omitempty"`
	CodeDescription    *LSPDiagnosticCodeDescription     `json:"codeDescription,omitempty"`
	RelatedInformation []LSPDiagnosticRelatedInformation `json:"relatedInformation,omitempty"`
	Tags               []int                             `json:"tags,omitempty"`
	Data               json.RawMessage                   `json:"data,omitempty"`
}

type lspDocumentDiagnosticReportJSON struct {
	Kind             string                     `json:"kind"`
	ResultID         string                     `json:"resultId"`
	Items            []lspDiagnosticJSON        `json:"items"`
	RelatedDocuments map[string]json.RawMessage `json:"relatedDocuments"`
}

type semanticTokenCacheEntry struct {
	ResultID string
	Data     []int
}

// lspServer wraps a running language server process and the JSON-RPC client
// used to talk to it over stdin/stdout.
type lspServer struct {
	cmd     *exec.Cmd
	process *lspProcess
	running bool
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	client  *jsonRPCClient
	managed bool

	// docVersions tracks open documents and their last-synced version
	// (prompt-8 Task 8-A / BUG-IDE-01). Keyed by file:// URI.
	docVersions map[string]int
	// docHashes last full-sync content hash (prompt-9 9-K throttle).
	docHashes map[string]string
	// docLastContent last synced full text (prompt-13 13-F incremental).
	docLastContent map[string]string
	// docLastSync last successful didChange time per URI.
	docLastSync map[string]time.Time
	// pendingChanges holds the trailing-edge debounce state per document.
	pendingChanges map[string]*pendingDocumentChange
	closing        bool
	closingErr     error
	// syncKind: 1=Full 2=Incremental (from server capabilities).
	syncKind int
	// codeActionSupported and codeActionKinds mirror the initialize result.
	// Refactor commands are never enabled from client-side assumptions.
	codeActionSupported      bool
	codeActionKinds          []string
	diagnosticProviderKnown  bool
	pullDiagnosticsSupported bool
	docMu                    sync.Mutex
	semanticMu               sync.RWMutex
	semanticTokenTypes       []string
	semanticTokenMods        []string
	semanticTokenResults     map[string]map[string][]int
	semanticTokenLatest      map[string]string
	semanticRequestSeq       uint64
	semanticLatestRequest    map[string]uint64

	// diagnostics cache (publishDiagnostics).
	diags              map[string][]Diagnostic
	diagResultIDs      map[string]string
	diagEpochs         map[string]uint64
	diagRequestSeq     uint64
	diagLatestRequests map[string]uint64
	diagsMu            sync.Mutex
}

const lspProcessStopTimeout = 2 * time.Second

const (
	lspRequestTimeout          = 10 * time.Second
	lspResponseDeliveryTimeout = 5 * time.Second
	lspDocumentDebounce        = 100 * time.Millisecond
	lspWriteFailureThreshold   = 3
)

var (
	errWorkspaceSwitching = errors.New("workspace switching")
	errLSPServerStopping  = errors.New("LSP server stopping")
)

var canonicalSemanticTokenTypes = []string{
	"namespace", "type", "class", "enum", "interface", "struct",
	"typeParameter", "parameter", "variable", "property", "enumMember",
	"event", "function", "method", "macro", "keyword", "modifier",
	"comment", "string", "number", "regexp", "operator", "decorator",
}

var canonicalSemanticTokenModifiers = []string{
	"declaration", "definition", "readonly", "static", "deprecated",
	"abstract", "async", "modification", "documentation", "defaultLibrary",
}

func parseStaticLSPCapability(raw json.RawMessage) (known, supported bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false, false
	}
	if bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("false")) {
		return true, false
	}
	if bytes.Equal(trimmed, []byte("true")) || trimmed[0] == '{' {
		return true, true
	}
	return true, false
}

func parseSemanticTokenLegend(raw json.RawMessage) ([]string, []string) {
	var provider struct {
		Legend struct {
			TokenTypes     []string `json:"tokenTypes"`
			TokenModifiers []string `json:"tokenModifiers"`
		} `json:"legend"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &provider) != nil {
		return nil, nil
	}
	return append([]string(nil), provider.Legend.TokenTypes...),
		append([]string(nil), provider.Legend.TokenModifiers...)
}

func (srv *lspServer) setSemanticTokenLegend(tokenTypes, tokenModifiers []string) {
	if srv == nil {
		return
	}
	srv.semanticMu.Lock()
	srv.semanticTokenTypes = append([]string(nil), tokenTypes...)
	srv.semanticTokenMods = append([]string(nil), tokenModifiers...)
	// Cached data has already been remapped through the previous legend and is
	// unsafe to reuse if a server changes or dynamically replaces its legend.
	srv.semanticRequestSeq++
	srv.semanticTokenResults = make(map[string]map[string][]int)
	srv.semanticTokenLatest = make(map[string]string)
	srv.semanticLatestRequest = make(map[string]uint64)
	srv.semanticMu.Unlock()
}

func (srv *lspServer) semanticTokenLegend() ([]string, []string) {
	if srv == nil {
		return append([]string(nil), canonicalSemanticTokenTypes...),
			append([]string(nil), canonicalSemanticTokenModifiers...)
	}
	srv.semanticMu.RLock()
	tokenTypes := append([]string(nil), srv.semanticTokenTypes...)
	tokenModifiers := append([]string(nil), srv.semanticTokenMods...)
	srv.semanticMu.RUnlock()
	if len(tokenTypes) == 0 {
		tokenTypes = append([]string(nil), canonicalSemanticTokenTypes...)
	}
	if len(tokenModifiers) == 0 {
		tokenModifiers = append([]string(nil), canonicalSemanticTokenModifiers...)
	}
	return tokenTypes, tokenModifiers
}

func (srv *lspServer) beginSemanticTokenRequest(uri, previousResultID string) (uint64, []int, bool) {
	if srv == nil {
		return 0, nil, false
	}
	srv.semanticMu.Lock()
	defer srv.semanticMu.Unlock()
	if srv.semanticTokenResults == nil {
		srv.semanticTokenResults = make(map[string]map[string][]int)
	}
	if srv.semanticTokenLatest == nil {
		srv.semanticTokenLatest = make(map[string]string)
	}
	if srv.semanticLatestRequest == nil {
		srv.semanticLatestRequest = make(map[string]uint64)
	}
	srv.semanticRequestSeq++
	sequence := srv.semanticRequestSeq
	srv.semanticLatestRequest[uri] = sequence
	if previousResultID == "" {
		return sequence, nil, false
	}
	data, ok := srv.semanticTokenResults[uri][previousResultID]
	return sequence, append([]int(nil), data...), ok
}

func (srv *lspServer) cacheSemanticTokenResult(uri string, sequence uint64, resultID string, data []int) {
	if srv == nil {
		return
	}
	srv.semanticMu.Lock()
	defer srv.semanticMu.Unlock()
	if srv.semanticLatestRequest[uri] != sequence {
		return
	}
	if srv.semanticTokenResults == nil {
		srv.semanticTokenResults = make(map[string]map[string][]int)
	}
	if srv.semanticTokenLatest == nil {
		srv.semanticTokenLatest = make(map[string]string)
	}
	if resultID == "" {
		delete(srv.semanticTokenResults, uri)
		delete(srv.semanticTokenLatest, uri)
		return
	}
	srv.semanticTokenResults[uri] = map[string][]int{
		resultID: append([]int(nil), data...),
	}
	srv.semanticTokenLatest[uri] = resultID
}

func (srv *lspServer) cachedSemanticTokenResult(uri string) (semanticTokenCacheEntry, bool) {
	if srv == nil {
		return semanticTokenCacheEntry{}, false
	}
	srv.semanticMu.RLock()
	defer srv.semanticMu.RUnlock()
	resultID := srv.semanticTokenLatest[uri]
	if resultID == "" {
		return semanticTokenCacheEntry{}, false
	}
	data, ok := srv.semanticTokenResults[uri][resultID]
	if !ok {
		return semanticTokenCacheEntry{}, false
	}
	return semanticTokenCacheEntry{ResultID: resultID, Data: append([]int(nil), data...)}, true
}

func (srv *lspServer) clearSemanticTokenResults(uri string) {
	if srv == nil {
		return
	}
	srv.semanticMu.Lock()
	delete(srv.semanticTokenResults, uri)
	delete(srv.semanticTokenLatest, uri)
	delete(srv.semanticLatestRequest, uri)
	srv.semanticMu.Unlock()
}

func mapLSPDiagnostics(items []lspDiagnosticJSON) []Diagnostic {
	if len(items) == 0 {
		return []Diagnostic{}
	}
	out := make([]Diagnostic, 0, len(items))
	for _, item := range items {
		out = append(out, Diagnostic{
			Line:               item.Range.Start.Line,
			Column:             item.Range.Start.Character,
			EndLine:            item.Range.End.Line,
			EndCol:             item.Range.End.Character,
			Range:              item.Range,
			Severity:           item.Severity,
			Message:            item.Message,
			Source:             item.Source,
			Code:               append(json.RawMessage(nil), item.Code...),
			CodeDescription:    cloneDiagnosticCodeDescription(item.CodeDescription),
			RelatedInformation: append([]LSPDiagnosticRelatedInformation(nil), item.RelatedInformation...),
			Tags:               append([]int(nil), item.Tags...),
			Data:               append(json.RawMessage(nil), item.Data...),
		})
	}
	return out
}

func cloneDiagnosticCodeDescription(value *LSPDiagnosticCodeDescription) *LSPDiagnosticCodeDescription {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneDiagnostic(item Diagnostic) Diagnostic {
	item.Code = append(json.RawMessage(nil), item.Code...)
	item.CodeDescription = cloneDiagnosticCodeDescription(item.CodeDescription)
	item.RelatedInformation = append([]LSPDiagnosticRelatedInformation(nil), item.RelatedInformation...)
	item.Tags = append([]int(nil), item.Tags...)
	item.Data = append(json.RawMessage(nil), item.Data...)
	return item
}

func cloneDiagnostics(items []Diagnostic) []Diagnostic {
	if len(items) == 0 {
		return []Diagnostic{}
	}
	out := make([]Diagnostic, len(items))
	for index := range items {
		out[index] = cloneDiagnostic(items[index])
	}
	return out
}

func (s *LSPService) notifyDiagnosticsRefresh(language string) uint64 {
	language = lspServerKey(language)
	s.mu.Lock()
	if s.diagnosticRefreshVersions == nil {
		s.diagnosticRefreshVersions = make(map[string]uint64)
	}
	s.diagnosticRefreshVersions[language]++
	version := s.diagnosticRefreshVersions[language]
	fsvc := s.fileSvc
	s.mu.Unlock()
	if fsvc != nil && fsvc.app != nil {
		payload := map[string]interface{}{"language": language, "version": version}
		app := fsvc.app
		go func() {
			app.Event.Emit("lsp:refresh-diagnostics", payload)
			app.Event.Emit("lsp:refreshDiagnostics", payload)
		}()
	}
	return version
}

// markDiagnosticsRefresh invalidates pull result ids while retaining the last
// push diagnostics as a visible fallback. No lock is held while the Wails
// event is emitted.
func (s *LSPService) markDiagnosticsRefresh(language string) uint64 {
	language = lspServerKey(language)
	s.mu.Lock()
	srv := s.servers[language]
	s.mu.Unlock()
	if srv != nil {
		srv.diagsMu.Lock()
		if srv.diagEpochs == nil {
			srv.diagEpochs = make(map[string]uint64)
		}
		seen := make(map[string]struct{})
		for uri := range srv.diagEpochs {
			srv.diagEpochs[uri]++
			seen[uri] = struct{}{}
		}
		for uri := range srv.diagResultIDs {
			if _, ok := seen[uri]; !ok {
				srv.diagEpochs[uri]++
				seen[uri] = struct{}{}
			}
		}
		for uri := range srv.diags {
			if _, ok := seen[uri]; !ok {
				srv.diagEpochs[uri]++
			}
		}
		srv.diagResultIDs = make(map[string]string)
		srv.diagsMu.Unlock()
	}
	return s.notifyDiagnosticsRefresh(language)
}

// GetDiagnosticsRefreshVersion returns the latest server-requested refresh
// generation. It is a polling fallback for headless or event-less clients.
func (s *LSPService) GetDiagnosticsRefreshVersion(language string) uint64 {
	language = lspServerKey(language)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.diagnosticRefreshVersions[language]
}

func (s *LSPService) handlePublishedDiagnostics(language string, srv *lspServer, params json.RawMessage) {
	if srv == nil {
		return
	}
	var notification struct {
		URI         string              `json:"uri"`
		Version     *int                `json:"version,omitempty"`
		Diagnostics []lspDiagnosticJSON `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &notification); err != nil || notification.URI == "" {
		slog.Warn("LSP publishDiagnostics: failed to parse params", "language", language, "err", err)
		return
	}
	diagnostics := mapLSPDiagnostics(notification.Diagnostics)
	if notification.Version != nil {
		srv.docMu.Lock()
		currentVersion := srv.docVersions[notification.URI]
		if currentVersion > 0 && *notification.Version < currentVersion {
			srv.docMu.Unlock()
			return
		}
		srv.diagsMu.Lock()
		storePublishedDiagnosticsLocked(srv, notification.URI, diagnostics)
		srv.diagsMu.Unlock()
		srv.docMu.Unlock()
	} else {
		srv.diagsMu.Lock()
		storePublishedDiagnosticsLocked(srv, notification.URI, diagnostics)
		srv.diagsMu.Unlock()
	}
	s.notifyDiagnosticsRefresh(language)
}

func storePublishedDiagnosticsLocked(srv *lspServer, uri string, diagnostics []Diagnostic) {
	if srv.diags == nil {
		srv.diags = make(map[string][]Diagnostic)
	}
	if srv.diagResultIDs == nil {
		srv.diagResultIDs = make(map[string]string)
	}
	if srv.diagEpochs == nil {
		srv.diagEpochs = make(map[string]uint64)
	}
	if srv.diagLatestRequests == nil {
		srv.diagLatestRequests = make(map[string]uint64)
	}
	srv.diags[uri] = cloneDiagnostics(diagnostics)
	delete(srv.diagResultIDs, uri)
	srv.diagEpochs[uri]++
}

// handleServerRequest 是 server→client request 的总分发器。F-2 (prompt-2.md)。
// 在 readLoop goroutine 中被调用，内部按需加 s.mu / s.lspConfigMu。
func (s *LSPService) handleServerRequest(method string, params json.RawMessage) (interface{}, error) {
	return s.handleServerRequestForLanguage("", method, params)
}

func (s *LSPService) handleServerRequestForLanguage(language, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "workspace/configuration":
		return s.handleWorkspaceConfiguration(params)
	case "workspace/applyEdit":
		return s.handleWorkspaceApplyEdit(params)
	case "workspace/workspaceFolders":
		return s.handleWorkspaceFolders(params)
	case "client/registerCapability":
		return handleClientCapabilityRegistration(params, "registrations")
	case "client/unregisterCapability":
		// The LSP field is misspelled as "unregisterations" for protocol
		// compatibility. Accept the corrected spelling used by some servers too.
		return handleClientCapabilityRegistration(params, "unregisterations", "unregistrations")
	case "workspace/diagnostic/refresh", "workspace/refreshDiagnostics":
		s.markDiagnosticsRefresh(language)
		return nil, nil
	default:
		return nil, fmt.Errorf("method not supported: %s", method)
	}
}

func handleClientCapabilityRegistration(params json.RawMessage, keys ...string) (interface{}, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(params, &envelope); err != nil {
		return nil, fmt.Errorf("parse client capability registration: %w", err)
	}
	var entries []struct {
		ID     string `json:"id"`
		Method string `json:"method"`
	}
	found := false
	for _, key := range keys {
		raw, ok := envelope[key]
		if !ok {
			continue
		}
		found = true
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("parse client capability %s: %w", key, err)
		}
		break
	}
	if !found {
		return nil, errors.New("client capability registration list is missing")
	}
	for _, entry := range entries {
		if entry.ID == "" || entry.Method == "" {
			return nil, errors.New("client capability registration requires id and method")
		}
	}
	// Monaco providers are registered eagerly. A successful response tells the
	// server that the matching client capability is ready for use.
	return nil, nil
}

// handleWorkspaceConfiguration 响应 server 请求的 per-resource 配置。
// 请求参数: { items: [{ section, scopeUri }] }；返回与 items 顺序对齐的配置值数组。
// 未注册的 section 返回 null。F-2 (prompt-2.md)。
func (s *LSPService) handleWorkspaceConfiguration(params json.RawMessage) (interface{}, error) {
	var req struct {
		Items []struct {
			Section  string `json:"section"`
			ScopeURI string `json:"scopeUri"`
		} `json:"items"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("parse workspace/configuration params: %w", err)
		}
	}
	s.lspConfigMu.RLock()
	configured := make(map[string]interface{}, len(s.lspConfig))
	for section, value := range s.lspConfig {
		configured[section] = value
	}
	s.lspConfigMu.RUnlock()
	s.mu.Lock()
	workspaceRoot := s.workspaceRoot
	s.mu.Unlock()
	out := make([]interface{}, 0, len(req.Items))
	for _, item := range req.Items {
		if cfg, ok := configured[item.Section]; ok {
			out = append(out, cfg)
			continue
		}
		if language, response, ok := languagePackConfiguration(item.Section); ok {
			options := lspInitializationOptions(language, workspaceRoot)
			if response == "preferences" {
				if preferences, exists := options["preferences"]; exists {
					out = append(out, preferences)
					continue
				}
			}
			out = append(out, options)
			continue
		}
		// A6: language servers commonly request these sections before the UI
		// has supplied overrides. Return the same defaults used at initialize.
		switch item.Section {
		case "python":
			options := lspInitializationOptions("python", workspaceRoot)
			out = append(out, options["python"])
		case "rust-analyzer", "rust":
			out = append(out, lspInitializationOptions("rust", workspaceRoot))
		default:
			out = append(out, nil)
		}
	}
	return out, nil
}

// handleWorkspaceApplyEdit 响应 server 请求应用 WorkspaceEdit。
// 请求参数: { label, edit: WorkspaceEdit }；返回 { applied, failureReason }。
// 支持 documentChanges 中的 TextDocumentEdit / CreateFile / DeleteFile / RenameFile。
// F-2 (prompt-2.md)。
func (s *LSPService) handleWorkspaceApplyEdit(params json.RawMessage) (interface{}, error) {
	var req struct {
		Label string          `json:"label"`
		Edit  json.RawMessage `json:"edit"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return map[string]interface{}{"applied": false, "failureReason": "parse params: " + err.Error()}, nil
		}
	}
	s.mu.Lock()
	fsvc := s.fileSvc
	s.mu.Unlock()
	if fsvc == nil {
		return map[string]interface{}{"applied": false, "failureReason": "no file service configured"}, nil
	}
	applied, reason := applyWorkspaceEdit(fsvc, req.Edit)
	return map[string]interface{}{"applied": applied, "failureReason": reason}, nil
}

// handleWorkspaceFolders 响应 server 请求的当前工作区文件夹列表。
// 返回 [{ uri, name }]。F-2 (prompt-2.md)。
func (s *LSPService) handleWorkspaceFolders(params json.RawMessage) (interface{}, error) {
	s.mu.Lock()
	root := s.workspaceRoot
	roots := append([]string(nil), s.workspaceRoots...)
	s.mu.Unlock()
	if len(roots) == 0 && root != "" {
		roots = []string{root}
	}
	return rootsToWorkspaceFolders(roots), nil
}

// applyWorkspaceEdit 将 LSP WorkspaceEdit 应用到文件系统。支持 documentChanges
// （推荐）与 changes （旧格式）两种形式。F-2 (prompt-2.md)。
// 返回 (applied, failureReason)。applied=true 当且仅当所有 edit 成功应用。
func applyWorkspaceEdit(fsvc *FileService, editRaw json.RawMessage) (bool, string) {
	if len(editRaw) == 0 {
		return true, ""
	}
	var edit struct {
		// changes: { [uri]: lspTextEditJSON[] }（旧格式）
		Changes map[string][]lspTextEditJSON `json:"changes"`
		// documentChanges: TextDocumentEdit | CreateFile | DeleteFile | RenameFile（推荐）
		DocumentChanges []json.RawMessage `json:"documentChanges"`
	}
	if err := json.Unmarshal(editRaw, &edit); err != nil {
		return false, "parse edit: " + err.Error()
	}
	// documentChanges 优先
	if len(edit.DocumentChanges) > 0 {
		for _, raw := range edit.DocumentChanges {
			var head struct {
				Kind string `json:"kind"`
			}
			_ = json.Unmarshal(raw, &head)
			switch head.Kind {
			case "create":
				var op struct {
					Kind string `json:"kind"`
					URI  string `json:"uri"`
				}
				if err := json.Unmarshal(raw, &op); err != nil {
					return false, "parse create: " + err.Error()
				}
				path := uriToPath(op.URI)
				if err := fsvc.CreateFile(path); err != nil {
					return false, "create " + path + ": " + err.Error()
				}
			case "delete":
				var op struct {
					Kind string `json:"kind"`
					URI  string `json:"uri"`
				}
				if err := json.Unmarshal(raw, &op); err != nil {
					return false, "parse delete: " + err.Error()
				}
				path := uriToPath(op.URI)
				if err := fsvc.DeletePath(path); err != nil {
					return false, "delete " + path + ": " + err.Error()
				}
			case "rename":
				var op struct {
					Kind   string `json:"kind"`
					OldURI string `json:"oldUri"`
					NewURI string `json:"newUri"`
				}
				if err := json.Unmarshal(raw, &op); err != nil {
					return false, "parse rename: " + err.Error()
				}
				if err := fsvc.RenamePath(uriToPath(op.OldURI), uriToPath(op.NewURI)); err != nil {
					return false, "rename: " + err.Error()
				}
			default:
				// TextDocumentEdit：无 kind 字段，按 textDocument + edits 处理
				var op struct {
					TextDocument struct {
						URI     string `json:"uri"`
						Version int    `json:"version"`
					} `json:"textDocument"`
					Edits []lspTextEditJSON `json:"edits"`
				}
				if err := json.Unmarshal(raw, &op); err != nil {
					return false, "parse textDocumentEdit: " + err.Error()
				}
				path := uriToPath(op.TextDocument.URI)
				content, err := fsvc.ReadFile(path)
				if err != nil {
					return false, "read " + path + ": " + err.Error()
				}
				newContent, err := applyTextEdits(content, op.Edits)
				if err != nil {
					return false, "apply edits " + path + ": " + err.Error()
				}
				if err := fsvc.WriteFile(path, newContent); err != nil {
					return false, "write " + path + ": " + err.Error()
				}
			}
		}
		return true, ""
	}
	// 旧格式 changes
	for uri, edits := range edit.Changes {
		path := uriToPath(uri)
		content, err := fsvc.ReadFile(path)
		if err != nil {
			return false, "read " + path + ": " + err.Error()
		}
		newContent, err := applyTextEdits(content, edits)
		if err != nil {
			return false, "apply edits " + path + ": " + err.Error()
		}
		if err := fsvc.WriteFile(path, newContent); err != nil {
			return false, "write " + path + ": " + err.Error()
		}
	}
	return true, ""
}

// applyTextEdits 将一组 LSP TextEdit（0-based line/character）应用到文本。
// edits 先按 start 位置降序排序，再从后往前应用，避免前面的 edit 影响后面
// 的行号/列号偏移。F-2 (prompt-2.md)。
func applyTextEdits(content string, edits []lspTextEditJSON) (string, error) {
	if len(edits) == 0 {
		return content, nil
	}
	lines := strings.Split(content, "\n")
	// 复制一份再排序，避免改动调用方切片。
	sorted := make([]lspTextEditJSON, len(edits))
	copy(sorted, edits)
	// 按 start 位置降序排序：从后往前应用。
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i].Range.Start, sorted[j].Range.Start
		if a.Line != b.Line {
			return a.Line > b.Line
		}
		return a.Character > b.Character
	})
	for _, e := range sorted {
		startLine, startCol := e.Range.Start.Line, e.Range.Start.Character
		endLine, endCol := e.Range.End.Line, e.Range.End.Character
		if startLine < 0 || startLine >= len(lines) {
			return "", fmt.Errorf("edit start line %d out of range (file has %d lines)", startLine, len(lines))
		}
		if endLine < 0 || endLine >= len(lines) {
			return "", fmt.Errorf("edit end line %d out of range (file has %d lines)", endLine, len(lines))
		}
		if startLine == endLine {
			line := lines[startLine]
			if startCol < 0 || startCol > len(line) || endCol < 0 || endCol > len(line) || startCol > endCol {
				return "", fmt.Errorf("edit column range [%d,%d) invalid on line %d (len=%d)", startCol, endCol, startLine, len(line))
			}
			lines[startLine] = line[:startCol] + e.NewText + line[endCol:]
		} else {
			// 多行 edit：替换 [startLine.startCol, endLine.endCol) 为 NewText
			first := lines[startLine][:startCol]
			last := lines[endLine][endCol:]
			mid := e.NewText
			// NewText 可能含 \n，拆分后拼接
			parts := strings.Split(mid, "\n")
			if len(parts) == 1 {
				lines[startLine] = first + parts[0] + last
				// 删除中间行 + endLine
				lines = append(lines[:startLine+1], lines[endLine+1:]...)
			} else {
				lines[startLine] = first + parts[0]
				lines[endLine] = parts[len(parts)-1] + last
				// 替换中间行（如果有）
				midCount := endLine - startLine - 1
				if len(parts)-2 == midCount {
					for i := 1; i < len(parts)-1; i++ {
						lines[startLine+i] = parts[i]
					}
				} else {
					// 行数不匹配：重建
					newLines := make([]string, 0, len(lines)-(endLine-startLine-1)+(len(parts)-2))
					newLines = append(newLines, lines[:startLine+1]...)
					for i := 1; i < len(parts)-1; i++ {
						newLines = append(newLines, parts[i])
					}
					newLines = append(newLines, lines[endLine:]...)
					lines = newLines
				}
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}

// setWorkspaceRoot updates the workspace root. Running servers are stopped
// because they were initialized against the previous root.
//
//wails:ignore
func (s *LSPService) setWorkspaceRoot(root string) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	roots := discoverLSPWorkspaceRoots(root)
	if len(roots) > 1 {
		s.switchWorkspaceLocked(root, roots)
		return
	}
	s.switchWorkspaceLocked(root, nil)
}

// setWorkspaceRoots 设置多根工作区列表（Priority 4 多根工作区）。
// 当 roots 非空时，LSP initialize 会向服务器声明所有 workspaceFolders，
// 后续变更通过 workspace/didChangeWorkspaceFolders 推送。
// 当 roots 为空时，退化到单根行为（使用 workspaceRoot 字段）。
// 为了向后兼容：若 roots 仅含一个元素，行为等同于 SetWorkspaceRoot。
//
//wails:ignore
func (s *LSPService) setWorkspaceRoots(roots []string) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	cleaned := dedupRoots(roots)
	s.mu.Lock()
	targetRoot := s.workspaceRoot
	s.mu.Unlock()
	if len(cleaned) == 1 {
		targetRoot = cleaned[0]
		cleaned = nil
	} else if len(cleaned) > 1 {
		targetRoot = cleaned[0]
	}
	s.switchWorkspaceLocked(targetRoot, cleaned)
}

// switchWorkspaceLocked performs the three A9 phases while lifecycleMu is
// held: detach under mu, stop without mu, then restart managed servers.
func (s *LSPService) switchWorkspaceLocked(root string, roots []string) {
	s.mu.Lock()
	if s.workspaceRoot == root && stringSlicesEqual(s.workspaceRoots, roots) {
		s.mu.Unlock()
		return
	}
	// FIX A9: publish the switching state and replace the map before waiting on
	// processes. Queries now fail fast instead of blocking behind the service lock.
	s.switching = true
	s.workspaceRoot = root
	s.workspaceRoots = append([]string(nil), roots...)
	oldServers := s.servers
	s.servers = make(map[string]*lspServer)
	restartLanguages := make([]string, 0, len(oldServers))
	for language, srv := range oldServers {
		if srv != nil && srv.managed && srv.running {
			restartLanguages = append(restartLanguages, language)
		}
	}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.switching = false
		s.mu.Unlock()
	}()
	stopLSPServersConcurrently(oldServers, errWorkspaceSwitching)
	if root == "" && len(roots) == 0 {
		return
	}
	sort.Strings(restartLanguages)
	for _, language := range restartLanguages {
		if err := s.startLSPServer(language, true); err != nil {
			slog.Warn("failed to restart LSP server after workspace change", "language", language, "err", err)
		}
	}
}

func stringSlicesEqual(a, b []string) bool {
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

// WorkspaceRoots 返回当前生效的工作区根列表。若多根模式未启用，
// 返回仅含 workspaceRoot 的切片（可能为空）。
func (s *LSPService) WorkspaceRoots() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.workspaceRoots) > 0 {
		out := make([]string, len(s.workspaceRoots))
		copy(out, s.workspaceRoots)
		return out
	}
	if s.workspaceRoot != "" {
		return []string{s.workspaceRoot}
	}
	return nil
}

// DidChangeWorkspaceFolders 在多根模式下，向所有已运行的 LSP 服务器推送
// workspace/didChangeWorkspaceFolders 通知（Priority 4）。单根模式下为空操作。
// added/removed 为相对上一次状态的差量；调用方负责维护整体一致性。
func (s *LSPService) DidChangeWorkspaceFolders(added, removed []string) {
	s.mu.Lock()
	if len(s.workspaceRoots) == 0 {
		s.mu.Unlock()
		return // 单根模式：LSP initialize 已通过 rootUri 表达，无需变更通知。
	}
	servers := make([]*lspServer, 0, len(s.servers))
	for _, srv := range s.servers {
		servers = append(servers, srv)
	}
	s.mu.Unlock()
	event := map[string]interface{}{
		"added":   rootsToWorkspaceFolders(added),
		"removed": rootsToWorkspaceFolders(removed),
	}
	for _, srv := range servers {
		if srv == nil || srv.client == nil {
			continue
		}
		_ = srv.client.notify("workspace/didChangeWorkspaceFolders", map[string]interface{}{
			"event": event,
		})
	}
}

// dedupRoots 去重并保留顺序。空字符串会被过滤掉。
func dedupRoots(roots []string) []string {
	seen := make(map[string]bool, len(roots))
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}
