package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// CodeActionResult represents a single code action returned by textDocument/codeAction.
// It includes the title (shown in the lightbulb menu), kind (e.g. "refactor.extract"),
// and optional workspace edits to apply when the user selects it.
type CodeActionResult struct {
	Title            string                 `json:"title"`
	Kind             string                 `json:"kind,omitempty"`
	Command          string                 `json:"command,omitempty"`
	CommandTitle     string                 `json:"commandTitle,omitempty"`
	CommandArguments []interface{}          `json:"commandArguments,omitempty"`
	Tooltip          string                 `json:"tooltip,omitempty"`
	Edit             []FileTextEdits        `json:"edit,omitempty"`
	Preview          *WorkspaceEditPreview  `json:"preview,omitempty"`
	IsPreferred      bool                   `json:"isPreferred,omitempty"`
	Disabled         bool                   `json:"disabled,omitempty"`
	DisabledReason   string                 `json:"disabledReason,omitempty"`
	Diagnostics      []Diagnostic           `json:"diagnostics,omitempty"`
	Data             interface{}            `json:"data,omitempty"`
	Payload          map[string]interface{} `json:"payload,omitempty"`
	rawEdit          json.RawMessage
}

// GetCodeActions returns available code actions (quick fixes, refactors) for a
// range in the document. Powers the lightbulb (CodeActionProvider) in Monaco.
// Returns actions with their associated workspace edits so the frontend can
// apply them when the user picks one.
func (s *LSPService) GetCodeActions(req LSPCompletionRequest) ([]CodeActionResult, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []CodeActionResult{}, err
	}
	if srv == nil {
		return []CodeActionResult{}, nil
	}
	if !srv.codeActionSupported || !codeActionKindsSupported(srv.codeActionKinds, req.Only) {
		return []CodeActionResult{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := srv.client.request(ctx, "textDocument/codeAction", buildCodeActionParams(req))
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		slog.Warn("LSP codeAction failed", "language", req.Language, "err", err)
		return []CodeActionResult{}, nil
	}
	actions := parseCodeActions(raw)
	for i := range actions {
		if len(actions[i].rawEdit) == 0 {
			continue
		}
		preview, previewErr := s.buildWorkspaceEditPreview(srv, req, actions[i].rawEdit)
		if previewErr != nil {
			actions[i].Disabled = true
			actions[i].DisabledReason = previewErr.Error()
			continue
		}
		actions[i].Preview = &preview
	}
	return actions, nil
}

// ResolveCodeAction fills a data-only code action via codeAction/resolve. The
// complete request is accepted so workspace-edit previews use the unsaved
// buffer as their baseline. Resolution errors are intentionally soft and
// return the original action.
func (s *LSPService) ResolveCodeAction(req LSPCompletionRequest, action CodeActionResult) (CodeActionResult, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		slog.Warn("LSP codeAction/resolve document sync failed", "language", req.Language, "err", err)
		return action, nil
	}
	if srv == nil {
		return action, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	raw, err := srv.client.request(ctx, "codeAction/resolve", codeActionResolvePayload(action))
	if err != nil || len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		slog.Warn("LSP codeAction/resolve failed", "language", req.Language, "err", err)
		return action, nil
	}
	parsed := parseCodeActions(raw)
	if len(parsed) == 0 {
		slog.Warn("LSP codeAction/resolve response parse failed", "language", req.Language)
		return action, nil
	}
	resolved := mergeCodeActionResult(action, parsed[0])
	if len(resolved.rawEdit) > 0 {
		preview, previewErr := s.buildWorkspaceEditPreview(srv, req, resolved.rawEdit)
		if previewErr != nil {
			resolved.Disabled = true
			resolved.DisabledReason = previewErr.Error()
		} else {
			resolved.Preview = &preview
		}
	}
	return resolved, nil
}

func buildCodeActionParams(req LSPCompletionRequest) map[string]interface{} {
	endLine, endColumn := req.EndLine, req.EndColumn
	if endLine == 0 && endColumn == 0 {
		endLine, endColumn = req.Line, req.Column
	}
	contextParam := map[string]interface{}{
		"diagnostics": []interface{}{},
	}
	if len(req.Only) > 0 {
		contextParam["only"] = append([]string(nil), req.Only...)
	}
	return map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"range": map[string]interface{}{
			"start": map[string]int{"line": req.Line, "character": req.Column},
			"end":   map[string]int{"line": endLine, "character": endColumn},
		},
		"context": contextParam,
	}

}

func parseCodeActionCapability(raw json.RawMessage) (bool, []string) {
	var capabilities struct {
		CodeActionProvider json.RawMessage `json:"codeActionProvider"`
	}
	if json.Unmarshal(raw, &capabilities) != nil || len(capabilities.CodeActionProvider) == 0 {
		return false, nil
	}
	var enabled bool
	if json.Unmarshal(capabilities.CodeActionProvider, &enabled) == nil {
		return enabled, nil
	}
	var options struct {
		CodeActionKinds []string `json:"codeActionKinds"`
	}
	if json.Unmarshal(capabilities.CodeActionProvider, &options) != nil {
		return false, nil
	}
	return true, append([]string(nil), options.CodeActionKinds...)
}

func codeActionKindsSupported(declared, requested []string) bool {
	if len(requested) == 0 || len(declared) == 0 {
		return true
	}
	for _, want := range requested {
		for _, have := range declared {
			if have == want || strings.HasPrefix(want, have+".") || strings.HasPrefix(have, want+".") {
				return true
			}
		}
	}
	return false
}

// parseCodeActions parses a textDocument/codeAction response into CodeActionResult[].
// Handles both Command[] and CodeAction[] response shapes per LSP spec.
func parseCodeActions(raw json.RawMessage) []CodeActionResult {
	items := codeActionItems(raw)
	if len(items) == 0 {
		return []CodeActionResult{}
	}
	out := make([]CodeActionResult, 0, len(items))
	for _, itemRaw := range items {
		var action struct {
			Title       string              `json:"title"`
			Kind        string              `json:"kind"`
			Tooltip     string              `json:"tooltip"`
			IsPreferred bool                `json:"isPreferred"`
			Diagnostics []lspDiagnosticJSON `json:"diagnostics"`
			Data        json.RawMessage     `json:"data"`
			Disabled    *struct {
				Reason string `json:"reason"`
			} `json:"disabled"`
			Command *struct {
				Title     string        `json:"title"`
				Command   string        `json:"command"`
				Arguments []interface{} `json:"arguments,omitempty"`
			} `json:"command"`
			Edit json.RawMessage `json:"edit"`
		}
		if err := json.Unmarshal(itemRaw, &action); err != nil {
			// A bare Command has a string command field, which intentionally does
			// not fit the nested CodeAction command shape.
			var command struct {
				Title     string        `json:"title"`
				Command   string        `json:"command"`
				Arguments []interface{} `json:"arguments"`
			}
			if json.Unmarshal(itemRaw, &command) != nil || command.Command == "" {
				continue
			}
			var payload map[string]interface{}
			_ = json.Unmarshal(itemRaw, &payload)
			out = append(out, CodeActionResult{
				Title:            command.Title,
				Command:          command.Command,
				CommandTitle:     command.Title,
				CommandArguments: append([]interface{}(nil), command.Arguments...),
				Payload:          payload,
			})
			continue
		}
		result := CodeActionResult{
			Title:            action.Title,
			Kind:             action.Kind,
			Tooltip:          action.Tooltip,
			IsPreferred:      action.IsPreferred,
			Disabled:         action.Disabled != nil,
			Diagnostics:      mapLSPDiagnostics(action.Diagnostics),
			CommandArguments: []interface{}{},
		}
		_ = json.Unmarshal(itemRaw, &result.Payload)
		if len(action.Data) > 0 && !bytes.Equal(bytes.TrimSpace(action.Data), []byte("null")) {
			_ = json.Unmarshal(action.Data, &result.Data)
		}
		if action.Disabled != nil {
			result.DisabledReason = action.Disabled.Reason
		}
		if action.Command != nil {
			result.Command = action.Command.Command
			result.CommandTitle = action.Command.Title
			result.CommandArguments = append([]interface{}(nil), action.Command.Arguments...)
		}
		if len(action.Edit) > 0 && !bytes.Equal(bytes.TrimSpace(action.Edit), []byte("null")) {
			result.rawEdit = append(json.RawMessage(nil), action.Edit...)
			result.Edit = parseWorkspaceEditsAll(action.Edit)
		}
		out = append(out, result)
	}
	return out
}

func codeActionResolvePayload(action CodeActionResult) map[string]interface{} {
	if len(action.Payload) > 0 {
		payload := make(map[string]interface{}, len(action.Payload))
		for key, value := range action.Payload {
			payload[key] = value
		}
		return payload
	}
	payload := map[string]interface{}{"title": action.Title}
	if action.Kind != "" {
		payload["kind"] = action.Kind
	}
	if action.Data != nil {
		payload["data"] = action.Data
	}
	if len(action.Diagnostics) > 0 {
		payload["diagnostics"] = action.Diagnostics
	}
	if action.IsPreferred {
		payload["isPreferred"] = true
	}
	if action.Disabled {
		payload["disabled"] = map[string]string{"reason": action.DisabledReason}
	}
	if action.Command != "" {
		commandTitle := action.CommandTitle
		if commandTitle == "" {
			commandTitle = action.Title
		}
		payload["command"] = map[string]interface{}{
			"title":     commandTitle,
			"command":   action.Command,
			"arguments": append([]interface{}(nil), action.CommandArguments...),
		}
	}
	return payload
}

func mergeCodeActionResult(original, resolved CodeActionResult) CodeActionResult {
	if resolved.Title == "" {
		resolved.Title = original.Title
	}
	if resolved.Kind == "" {
		resolved.Kind = original.Kind
	}
	if resolved.Tooltip == "" {
		resolved.Tooltip = original.Tooltip
	}
	if resolved.Command == "" {
		resolved.Command = original.Command
		resolved.CommandTitle = original.CommandTitle
		resolved.CommandArguments = append([]interface{}(nil), original.CommandArguments...)
	}
	if len(resolved.rawEdit) == 0 {
		resolved.rawEdit = append(json.RawMessage(nil), original.rawEdit...)
		resolved.Edit = append([]FileTextEdits(nil), original.Edit...)
		resolved.Preview = original.Preview
	}
	if resolved.Data == nil {
		resolved.Data = original.Data
	}
	if len(resolved.Diagnostics) == 0 {
		resolved.Diagnostics = cloneDiagnostics(original.Diagnostics)
	}
	if len(resolved.Payload) == 0 {
		resolved.Payload = original.Payload
	}
	if _, hasDisabled := resolved.Payload["disabled"]; !hasDisabled && original.Disabled {
		resolved.Disabled = true
		resolved.DisabledReason = original.DisabledReason
	}
	resolved.IsPreferred = resolved.IsPreferred || original.IsPreferred
	return resolved
}

// TextEdit is a range replacement for format/rename (prompt-8 Task 8-G/H).
type TextEdit struct {
	StartLine int       `json:"startLine"`
	StartCol  int       `json:"startCol"`
	EndLine   int       `json:"endLine"`
	EndCol    int       `json:"endCol"`
	Range     *LSPRange `json:"range,omitempty"`
	Insert    *LSPRange `json:"insert,omitempty"`
	Replace   *LSPRange `json:"replace,omitempty"`
	NewText   string    `json:"newText"`
}

// FormatDocument runs textDocument/formatting and returns edits for the buffer
// (prompt-8 Task 8-G). Empty when server unavailable.
func (s *LSPService) FormatDocument(req LSPCompletionRequest) ([]TextEdit, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []TextEdit{}, err
	}
	if srv == nil {
		return []TextEdit{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"options": map[string]interface{}{
			"tabSize":      4,
			"insertSpaces": false,
		},
	}
	if req.Language == "typescript" || req.Language == "javascript" {
		params["options"] = map[string]interface{}{"tabSize": 2, "insertSpaces": true}
	}
	raw, err := srv.client.request(ctx, "textDocument/formatting", params)
	if err != nil {
		slog.Warn("LSP format failed", "err", err)
		return []TextEdit{}, nil
	}
	return parseTextEdits(raw), nil
}

// FileTextEdits is a path + list of text edits (prompt-9 Task 9-B multi-file rename).
type FileTextEdits struct {
	FilePath string     `json:"filePath"`
	Version  *int       `json:"version,omitempty"`
	Edits    []TextEdit `json:"edits"`
}

type WorkspaceEditPreviewFile struct {
	FilePath        string `json:"filePath"`
	Version         *int   `json:"version,omitempty"`
	BaselineHash    string `json:"baselineHash"`
	OriginalContent string `json:"originalContent"`
	ModifiedContent string `json:"modifiedContent"`
}

type WorkspaceEditPreview struct {
	Files []WorkspaceEditPreviewFile `json:"files"`
}

type WorkspaceEditApplyResult struct {
	Applied           bool     `json:"applied"`
	AppliedFiles      []string `json:"appliedFiles,omitempty"`
	FailureReason     string   `json:"failureReason,omitempty"`
	Conflicts         []string `json:"conflicts,omitempty"`
	RollbackAttempted bool     `json:"rollbackAttempted,omitempty"`
	RolledBack        bool     `json:"rolledBack,omitempty"`
	// G18: commit receipt. TransactionID identifies the committed transaction
	// and FileHashes are the on-disk content hashes after commit, so the UI
	// can distinguish "not committed" from "committed but UI sync failed" and
	// recover from receipt/disk without re-running the disk transaction.
	TransactionID string            `json:"transactionId,omitempty"`
	FileHashes    map[string]string `json:"fileHashes,omitempty"`
	Err           error             `json:"-"`
}

func buildWorkspaceEditPreview(
	raw json.RawMessage,
	read func(path string) (string, error),
) (WorkspaceEditPreview, error) {
	var envelope struct {
		DocumentChanges []struct {
			Kind string `json:"kind"`
		} `json:"documentChanges"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return WorkspaceEditPreview{}, fmt.Errorf("parse workspace edit: %w", err)
	}
	for _, change := range envelope.DocumentChanges {
		if change.Kind != "" {
			return WorkspaceEditPreview{}, fmt.Errorf("workspace resource operation %q cannot be previewed safely", change.Kind)
		}
	}
	files := parseWorkspaceEditsAll(raw)
	sort.Slice(files, func(i, j int) bool { return files[i].FilePath < files[j].FilePath })
	preview := WorkspaceEditPreview{Files: make([]WorkspaceEditPreviewFile, 0, len(files))}
	for _, file := range files {
		original, err := read(file.FilePath)
		if err != nil {
			return WorkspaceEditPreview{}, fmt.Errorf("read %s: %w", file.FilePath, err)
		}
		modified, err := applyLSPTextEditsUTF16(original, file.Edits)
		if err != nil {
			return WorkspaceEditPreview{}, fmt.Errorf("preview %s: %w", file.FilePath, err)
		}
		preview.Files = append(preview.Files, WorkspaceEditPreviewFile{
			FilePath:        file.FilePath,
			Version:         file.Version,
			BaselineHash:    contentHash([]byte(original)),
			OriginalContent: original,
			ModifiedContent: modified,
		})
	}
	return preview, nil
}

func applyLSPTextEditsUTF16(content string, edits []TextEdit) (string, error) {
	type resolvedEdit struct {
		start   int
		end     int
		newText string
	}
	data := []byte(content)
	resolved := make([]resolvedEdit, 0, len(edits))
	for _, edit := range edits {
		start, err := lspPositionByteOffset(data, edit.StartLine, edit.StartCol)
		if err != nil {
			return "", err
		}
		end, err := lspPositionByteOffset(data, edit.EndLine, edit.EndCol)
		if err != nil {
			return "", err
		}
		if end < start {
			return "", fmt.Errorf("edit end precedes start")
		}
		resolved = append(resolved, resolvedEdit{start: start, end: end, newText: edit.NewText})
	}
	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].start == resolved[j].start {
			return resolved[i].end > resolved[j].end
		}
		return resolved[i].start > resolved[j].start
	})
	lastStart := len(data) + 1
	for _, edit := range resolved {
		if edit.end > lastStart {
			return "", fmt.Errorf("overlapping workspace edits")
		}
		data = append(append(append([]byte(nil), data[:edit.start]...), []byte(edit.newText)...), data[edit.end:]...)
		lastStart = edit.start
	}
	return string(data), nil
}

func (s *LSPService) buildWorkspaceEditPreview(
	srv *lspServer,
	req LSPCompletionRequest,
	raw json.RawMessage,
) (WorkspaceEditPreview, error) {
	s.mu.Lock()
	fsvc := s.fileSvc
	s.mu.Unlock()
	return buildWorkspaceEditPreview(raw, func(path string) (string, error) {
		if pathToURI(path) == pathToURI(req.FilePath) {
			return req.Content, nil
		}
		uri := pathToURI(path)
		srv.docMu.Lock()
		content, ok := srv.docLastContent[uri]
		srv.docMu.Unlock()
		if ok {
			return content, nil
		}
		if fsvc == nil {
			return "", fmt.Errorf("no file service configured")
		}
		return fsvc.ReadFile(path)
	})
}

// applyWorkspaceEditPreviewTransaction is kept for backwards compatibility.
// It delegates to the unified applyEditTransaction (workspace_edit_transaction.go)
// with no resource operations and no dirty-buffer check (the LSP path manages
// those through LSP document versions instead).
func applyWorkspaceEditPreviewTransaction(
	ctx context.Context,
	preview WorkspaceEditPreview,
	version func(path string) (int, bool),
	read func(path string) (string, error),
	write func(path, content string) error,
) WorkspaceEditApplyResult {
	// Root is not supplied here because ApplyRefactorWorkspaceEdit already
	// validates every path via fsvc.validateMutatingPath before reaching this
	// function. Passing "" would reject all paths; we pass a sentinel that
	// skips the root check while preserving all other preconditions.
	return applyEditTransaction(ctx, EditTransaction{TextEdits: preview}, EditTransactionOptions{
		Root:    lspTransactionRootSentinel,
		Version: version,
		Read:    read,
		Write:   write,
	})
}

// lspTransactionRootSentinel is an internal marker used only by the LSP path
// to bypass the root check in applyEditTransaction. The LSP path performs its
// own path validation (fsvc.validateMutatingPath) before calling this function,
// so a second root check would be redundant and would break calls where the
// workspace root is not yet available.
//
// This sentinel must never be passed by callers outside this file.
const lspTransactionRootSentinel = "\x00lsp-pre-validated\x00"

func (s *LSPService) ApplyRefactorWorkspaceEdit(ctx context.Context, language string, preview WorkspaceEditPreview) WorkspaceEditApplyResult {
	srv, serverErr := s.serverForLanguage(language)
	s.mu.Lock()
	fsvc := s.fileSvc
	s.mu.Unlock()
	if serverErr != nil {
		return WorkspaceEditApplyResult{Err: serverErr, FailureReason: serverErr.Error()}
	}
	if srv == nil || fsvc == nil {
		return WorkspaceEditApplyResult{FailureReason: "language server or file service is unavailable"}
	}
	paths := make(map[string]string, len(preview.Files))
	modes := make(map[string]os.FileMode, len(preview.Files))
	for _, file := range preview.Files {
		abs, err := fsvc.validateMutatingPath(file.FilePath)
		if err != nil {
			return WorkspaceEditApplyResult{FailureReason: err.Error()}
		}
		mode := os.FileMode(0o644)
		if info, statErr := os.Stat(abs); statErr == nil {
			mode = info.Mode().Perm()
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return WorkspaceEditApplyResult{FailureReason: statErr.Error()}
		}
		paths[file.FilePath] = abs
		modes[file.FilePath] = mode
	}
	version := func(path string) (int, bool) {
		srv.docMu.Lock()
		defer srv.docMu.Unlock()
		value, ok := srv.docVersions[pathToURI(path)]
		return value, ok
	}
	write := func(path, content string) error {
		abs, ok := paths[path]
		if !ok {
			return fmt.Errorf("workspace edit path was not prevalidated: %s", path)
		}
		if err := atomicWriteFile(abs, []byte(content), modes[path]); err != nil {
			return err
		}
		if fsvc.app != nil {
			fsvc.app.Event.Emit("file:saved", abs)
		}
		return nil
	}
	return applyWorkspaceEditPreviewTransaction(ctx, preview, version, fsvc.ReadFile, write)
}

func (s *LSPService) ExecuteRefactorCommand(language, command string, arguments []interface{}) error {
	if command == "" {
		return fmt.Errorf("refactor command is empty")
	}
	srv, err := s.serverForLanguage(language)
	if err != nil {
		return err
	}
	if srv == nil || !srv.codeActionSupported {
		return fmt.Errorf("language server does not support code actions")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = srv.client.request(ctx, "workspace/executeCommand", map[string]interface{}{
		"command":   command,
		"arguments": arguments,
	})
	return err
}

// RenameSymbol runs textDocument/rename for the *current file only* (compat).
func (s *LSPService) RenameSymbol(req LSPCompletionRequest, newName string) ([]TextEdit, error) {
	files, err := s.RenameSymbolWorkspace(req, newName)
	if err != nil || len(files) == 0 {
		return []TextEdit{}, err
	}
	want := pathToURI(req.FilePath)
	for _, f := range files {
		if pathToURI(f.FilePath) == want || f.FilePath == req.FilePath {
			return f.Edits, nil
		}
	}
	return files[0].Edits, nil
}

// RenameSymbolWorkspace returns WorkspaceEdit across all touched files (prompt-9 9-B).
func (s *LSPService) RenameSymbolWorkspace(req LSPCompletionRequest, newName string) ([]FileTextEdits, error) {
	if newName == "" {
		return []FileTextEdits{}, nil
	}
	srv, err := s.syncDocument(req)
	if err != nil {
		return nil, err
	}
	if srv == nil {
		return nil, fmt.Errorf("not_running: language server not running for %s", req.Language)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"position":     map[string]int{"line": req.Line, "character": req.Column},
		"newName":      newName,
	}
	raw, err := srv.client.request(ctx, "textDocument/rename", params)
	if err != nil {
		slog.Warn("LSP rename failed", "language", req.Language, "err", err)
		s.setCallStatus(req.Language, "rpc", err.Error())
		return nil, fmt.Errorf("rpc: %w", err)
	}
	s.setCallStatus(req.Language, "ok", "")
	return parseWorkspaceEditsAll(raw), nil
}

// SignatureHelpResult is a simplified signature help payload (prompt-9 9-G).
// G-HL-01: Parameters upgraded to ParameterInfo with per-parameter documentation.
type SignatureHelpResult struct {
	Label           string          `json:"label"`
	Documentation   string          `json:"documentation"`
	Parameters      []ParameterInfo `json:"parameters"`
	ActiveParameter int             `json:"activeParameter"`
	ActiveSignature int             `json:"activeSignature"`
}

// ParameterInfo holds a single parameter's label and optional documentation.
type ParameterInfo struct {
	Label         string `json:"label"`
	Documentation string `json:"documentation"`
}

// GetSignatureHelp queries textDocument/signatureHelp (prompt-9 9-G).
func (s *LSPService) GetSignatureHelp(req LSPCompletionRequest) (*SignatureHelpResult, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return nil, err
	}
	if srv == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"position":     map[string]int{"line": req.Line, "character": req.Column},
	}
	raw, err := srv.client.request(ctx, "textDocument/signatureHelp", params)
	if err != nil {
		slog.Warn("LSP signatureHelp failed", "language", req.Language, "err", err)
		s.setCallStatus(req.Language, "rpc", err.Error())
		return nil, nil
	}
	return parseSignatureHelp(raw), nil
}

// OrganizeImports runs source.organizeImports code action (prompt-9 9-G).
func (s *LSPService) OrganizeImports(req LSPCompletionRequest) ([]TextEdit, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []TextEdit{}, err
	}
	if srv == nil {
		return []TextEdit{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"range": map[string]interface{}{
			"start": map[string]int{"line": 0, "character": 0},
			"end":   map[string]int{"line": 0, "character": 0},
		},
		"context": map[string]interface{}{
			"diagnostics": []interface{}{},
			"only":        []string{"source.organizeImports"},
		},
	}
	raw, err := srv.client.request(ctx, "textDocument/codeAction", params)
	if err != nil {
		slog.Warn("LSP organizeImports failed", "err", err)
		return []TextEdit{}, err
	}
	return resolveOrganizeImportActions(srv, raw, pathToURI(req.FilePath))
}

// OrganizeImportsFile is the file-path compatibility entry point. It applies
// the returned edits atomically while the original method continues returning
// edits for the existing frontend workflow.
func (s *LSPService) OrganizeImportsFile(filePath, language string) error {
	content, err := s.contentForLSPFile(language, filePath)
	if err != nil {
		return err
	}
	edits, err := s.OrganizeImports(LSPCompletionRequest{
		Language: language,
		FilePath: filePath,
		Content:  content,
	})
	if err != nil || len(edits) == 0 {
		return err
	}
	updated, err := applyLSPTextEditsUTF16(content, edits)
	if err != nil {
		return err
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	return atomicWriteFile(filePath, []byte(updated), info.Mode().Perm())
}

// InlayHint is a simplified inlay hint (prompt-12 12-L optional).
type InlayHint struct {
	Line         int             `json:"line"`
	Column       int             `json:"column"`
	Label        string          `json:"label"`
	Kind         int             `json:"kind"` // 1=type 2=parameter
	Tooltip      interface{}     `json:"tooltip,omitempty"`
	TextEdits    []TextEdit      `json:"textEdits,omitempty"`
	PaddingLeft  bool            `json:"paddingLeft,omitempty"`
	PaddingRight bool            `json:"paddingRight,omitempty"`
	Data         json.RawMessage `json:"data,omitempty"`
	RawLabel     json.RawMessage `json:"rawLabel,omitempty"`
}

type inlayHintJSON struct {
	Position     LSPPosition       `json:"position"`
	Label        json.RawMessage   `json:"label"`
	Kind         int               `json:"kind,omitempty"`
	Tooltip      interface{}       `json:"tooltip,omitempty"`
	TextEdits    []lspTextEditJSON `json:"textEdits,omitempty"`
	PaddingLeft  bool              `json:"paddingLeft,omitempty"`
	PaddingRight bool              `json:"paddingRight,omitempty"`
	Data         json.RawMessage   `json:"data,omitempty"`
}

// GetInlayHints requests textDocument/inlayHint when the server supports it.
// Returns empty slice when unsupported — never errors for UI toggle paths.
func (s *LSPService) GetInlayHints(req LSPCompletionRequest) ([]InlayHint, error) {
	raw, err := s.GetInlayHintsRaw(req)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return []InlayHint{}, err
	}
	return parseInlayHints(raw), nil
}

// GetInlayHintsRaw preserves the complete protocol response for callers that
// need label parts, tooltip locations or lazy resolve data.
func (s *LSPService) GetInlayHintsRaw(req LSPCompletionRequest) (json.RawMessage, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return nil, err
	}
	if srv == nil {
		return json.RawMessage(`[]`), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	startLine := req.Line
	if startLine < 0 {
		startLine = 0
	}
	endLine := req.EndLine
	if endLine <= startLine {
		endLine = 100000
	}
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"range": map[string]interface{}{
			"start": map[string]int{"line": startLine, "character": 0},
			"end":   map[string]int{"line": endLine, "character": 0},
		},
	}
	raw, err := srv.client.request(ctx, "textDocument/inlayHint", params)
	if err != nil {
		slog.Warn("LSP inlayHint failed", "language", req.Language, "err", err)
		return json.RawMessage(`[]`), nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`[]`), nil
	}
	return raw, nil
}

func (s *LSPService) GetInlayHintsForFile(filePath string, startLine, endLine int) (json.RawMessage, error) {
	language := lspLanguageForFilePath(filePath)
	if language == "" {
		return json.RawMessage(`[]`), nil
	}
	content, err := s.contentForLSPFile(language, filePath)
	if err != nil {
		return json.RawMessage(`[]`), nil
	}
	if startLine < 0 {
		startLine = 0
	}
	if endLine <= startLine {
		endLine = startLine + 1
	}
	return s.GetInlayHintsRaw(LSPCompletionRequest{
		Language: language,
		FilePath: filePath,
		Line:     startLine,
		EndLine:  endLine,
		Content:  content,
	})
}

func (s *LSPService) ResolveInlayHint(language string, hint InlayHint) (InlayHint, error) {
	language = lspServerKey(language)
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return hint, errWorkspaceSwitching
	}
	srv := s.servers[language]
	s.mu.Unlock()
	if srv == nil {
		return hint, nil
	}
	wire := inlayHintToJSON(hint)
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	raw, err := srv.client.request(ctx, "inlayHint/resolve", wire)
	if err != nil {
		slog.Warn("LSP inlayHint/resolve failed", "language", language, "err", err)
		return hint, nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return hint, nil
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return hint, nil
	}
	return mapInlayHint(wire), nil
}

func parseInlayHints(raw json.RawMessage) []InlayHint {
	var wire []inlayHintJSON
	if err := json.Unmarshal(raw, &wire); err != nil {
		return []InlayHint{}
	}
	out := make([]InlayHint, 0, len(wire))
	for _, hint := range wire {
		out = append(out, mapInlayHint(hint))
	}
	return out
}

func mapInlayHint(hint inlayHintJSON) InlayHint {
	label := ""
	if err := json.Unmarshal(hint.Label, &label); err != nil {
		var parts []struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(hint.Label, &parts) == nil {
			var builder strings.Builder
			for _, part := range parts {
				builder.WriteString(part.Value)
			}
			label = builder.String()
		} else {
			label = string(hint.Label)
		}
	}
	return InlayHint{
		Line:         hint.Position.Line,
		Column:       hint.Position.Character,
		Label:        label,
		Kind:         hint.Kind,
		Tooltip:      hint.Tooltip,
		TextEdits:    textEditsFromJSON(hint.TextEdits),
		PaddingLeft:  hint.PaddingLeft,
		PaddingRight: hint.PaddingRight,
		Data:         append(json.RawMessage(nil), hint.Data...),
		RawLabel:     append(json.RawMessage(nil), hint.Label...),
	}
}

func inlayHintToJSON(hint InlayHint) inlayHintJSON {
	label := append(json.RawMessage(nil), hint.RawLabel...)
	if len(label) == 0 {
		label, _ = json.Marshal(hint.Label)
	}
	wire := inlayHintJSON{
		Position:     LSPPosition{Line: hint.Line, Character: hint.Column},
		Label:        label,
		Kind:         hint.Kind,
		Tooltip:      hint.Tooltip,
		PaddingLeft:  hint.PaddingLeft,
		PaddingRight: hint.PaddingRight,
		Data:         append(json.RawMessage(nil), hint.Data...),
	}
	for _, edit := range hint.TextEdits {
		wire.TextEdits = append(wire.TextEdits, *lspTextEditJSONFromTextEdit(edit))
	}
	return wire
}

// ============================================================================
// G-HL-02: Code Lens support — shows reference counts, implementations, etc.
// ============================================================================

// CodeLensResult is a simplified code lens payload for the frontend.
type CodeLensResult struct {
	Line      int             `json:"line"`
	Column    int             `json:"column"`
	EndLine   int             `json:"endLine,omitempty"`
	EndColumn int             `json:"endColumn,omitempty"`
	Label     string          `json:"label"`
	Command   string          `json:"command"`
	Arguments []interface{}   `json:"arguments,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type codeLensJSON struct {
	Range   LSPRange `json:"range"`
	Command *struct {
		Title     string        `json:"title"`
		Command   string        `json:"command"`
		Arguments []interface{} `json:"arguments,omitempty"`
	} `json:"command,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// GetCodeLenses requests textDocument/codeLens (G-HL-02).
// Returns empty slice when unsupported — never errors for UI paths.
func (s *LSPService) GetCodeLenses(req LSPCompletionRequest) ([]CodeLensResult, error) {
	raw, srv, err := s.getCodeLensesRaw(req)
	if err != nil || srv == nil || len(raw) == 0 || string(raw) == "null" {
		return []CodeLensResult{}, err
	}
	var lenses []codeLensJSON
	if err := json.Unmarshal(raw, &lenses); err != nil {
		return []CodeLensResult{}, nil
	}
	semaphore := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i := range lenses {
		if lenses[i].Command != nil && lenses[i].Command.Title != "" {
			continue
		}
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			resolved, resolveErr := resolveCodeLensJSON(srv, req.Language, lenses[i])
			if resolveErr == nil {
				lenses[i] = resolved
			}
		}()
	}
	wg.Wait()
	return mapCodeLenses(lenses), nil
}

// GetCodeLensesRaw preserves unresolved data for protocol-level integrations.
func (s *LSPService) GetCodeLensesRaw(req LSPCompletionRequest) (json.RawMessage, error) {
	raw, _, err := s.getCodeLensesRaw(req)
	return raw, err
}

func (s *LSPService) GetCodeLensesForFile(filePath string) (json.RawMessage, error) {
	language := lspLanguageForFilePath(filePath)
	if language == "" {
		return json.RawMessage(`[]`), nil
	}
	content, err := s.contentForLSPFile(language, filePath)
	if err != nil {
		return json.RawMessage(`[]`), nil
	}
	return s.GetCodeLensesRaw(LSPCompletionRequest{
		Language: language,
		FilePath: filePath,
		Content:  content,
	})
}

func (s *LSPService) getCodeLensesRaw(req LSPCompletionRequest) (json.RawMessage, *lspServer, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return nil, nil, err
	}
	if srv == nil {
		return json.RawMessage(`[]`), nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
	}
	raw, err := srv.client.request(ctx, "textDocument/codeLens", params)
	if err != nil {
		slog.Warn("LSP codeLens failed", "language", req.Language, "err", err)
		return json.RawMessage(`[]`), srv, nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`[]`), srv, nil
	}
	return raw, srv, nil
}

func (s *LSPService) ResolveCodeLens(language string, lens CodeLensResult) (CodeLensResult, error) {
	language = lspServerKey(language)
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return lens, errWorkspaceSwitching
	}
	srv := s.servers[language]
	s.mu.Unlock()
	if srv == nil {
		return lens, nil
	}
	resolved, err := resolveCodeLensJSON(srv, language, codeLensToJSON(lens))
	if err != nil {
		return lens, nil
	}
	mapped := mapCodeLenses([]codeLensJSON{resolved})
	if len(mapped) == 0 {
		return lens, nil
	}
	return mapped[0], nil
}

func resolveCodeLensJSON(srv *lspServer, language string, lens codeLensJSON) (codeLensJSON, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	raw, err := srv.client.request(ctx, "codeLens/resolve", lens)
	if err != nil {
		slog.Warn("LSP codeLens/resolve failed", "language", language, "err", err)
		return lens, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return lens, err
	}
	if err := json.Unmarshal(raw, &lens); err != nil {
		return lens, err
	}
	return lens, nil
}

// parseCodeLenses extracts code lens entries from the LSP response.
func parseCodeLenses(raw json.RawMessage) []CodeLensResult {
	var arr []codeLensJSON
	if json.Unmarshal(raw, &arr) != nil {
		return []CodeLensResult{}
	}
	return mapCodeLenses(arr)
}

func mapCodeLenses(lenses []codeLensJSON) []CodeLensResult {
	out := make([]CodeLensResult, 0, len(lenses))
	for _, lens := range lenses {
		if lens.Command == nil || lens.Command.Title == "" {
			continue // skip lenses without a title
		}
		out = append(out, CodeLensResult{
			Line:      lens.Range.Start.Line,
			Column:    lens.Range.Start.Character,
			EndLine:   lens.Range.End.Line,
			EndColumn: lens.Range.End.Character,
			Label:     lens.Command.Title,
			Command:   lens.Command.Command,
			Arguments: append([]interface{}(nil), lens.Command.Arguments...),
			Data:      append(json.RawMessage(nil), lens.Data...),
		})
	}
	return out
}

func codeLensToJSON(lens CodeLensResult) codeLensJSON {
	wire := codeLensJSON{
		Range: LSPRange{
			Start: LSPPosition{Line: lens.Line, Character: lens.Column},
			End:   LSPPosition{Line: lens.EndLine, Character: lens.EndColumn},
		},
		Data: append(json.RawMessage(nil), lens.Data...),
	}
	if lens.Label != "" || lens.Command != "" {
		wire.Command = &struct {
			Title     string        `json:"title"`
			Command   string        `json:"command"`
			Arguments []interface{} `json:"arguments,omitempty"`
		}{Title: lens.Label, Command: lens.Command, Arguments: append([]interface{}(nil), lens.Arguments...)}
	}
	return wire
}
