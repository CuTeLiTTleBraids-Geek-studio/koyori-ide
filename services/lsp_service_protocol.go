package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// --- response parsing helpers ---

// completionItemJSON is the wire shape of an LSP CompletionItem (subset).
// Priority 2 (prompt-1.md)：新增 InsertTextFormat / LabelDetails 字段以接收
// LSP server 返回的 snippet 标记与函数签名详情。
type completionItemJSON struct {
	Label               string                  `json:"label"`
	Kind                int                     `json:"kind,omitempty"`
	Detail              string                  `json:"detail,omitempty"`
	InsertText          *string                 `json:"insertText,omitempty"`
	TextEditText        *string                 `json:"textEditText,omitempty"`
	InsertTextFormat    int                     `json:"insertTextFormat,omitempty"`
	InsertTextMode      *int                    `json:"insertTextMode,omitempty"`
	SortText            *string                 `json:"sortText,omitempty"`
	FilterText          *string                 `json:"filterText,omitempty"`
	Preselect           bool                    `json:"preselect,omitempty"`
	Deprecated          bool                    `json:"deprecated,omitempty"`
	Tags                []int                   `json:"tags,omitempty"`
	Documentation       interface{}             `json:"documentation,omitempty"`
	Data                json.RawMessage         `json:"data,omitempty"`
	CommitChars         *[]string               `json:"commitCharacters,omitempty"`
	TextEdit            *completionTextEditJSON `json:"textEdit,omitempty"`
	LabelDetails        *LSPLabelDetails        `json:"labelDetails,omitempty"`
	AdditionalTextEdits []lspTextEditJSON       `json:"additionalTextEdits,omitempty"`
}

type completionItemDefaultsJSON struct {
	CommitChars      *[]string       `json:"commitCharacters,omitempty"`
	EditRange        json.RawMessage `json:"editRange,omitempty"`
	InsertTextFormat *int            `json:"insertTextFormat,omitempty"`
	InsertTextMode   *int            `json:"insertTextMode,omitempty"`
}

type completionTextEditJSON struct {
	Range   *lspRangeJSON `json:"range,omitempty"`
	Insert  *lspRangeJSON `json:"insert,omitempty"`
	Replace *lspRangeJSON `json:"replace,omitempty"`
	NewText string        `json:"newText"`
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneStringSlicePointer(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	cloned := append([]string{}, (*value)...)
	return &cloned
}

func completionTextEditFromJSON(edit completionTextEditJSON) TextEdit {
	mapped := TextEdit{NewText: edit.NewText}
	if edit.Range != nil {
		rangeValue := jsonRangeToLSP(*edit.Range)
		mapped.Range = &rangeValue
		mapped.StartLine = rangeValue.Start.Line
		mapped.StartCol = rangeValue.Start.Character
		mapped.EndLine = rangeValue.End.Line
		mapped.EndCol = rangeValue.End.Character
	}
	if edit.Insert != nil {
		insert := jsonRangeToLSP(*edit.Insert)
		mapped.Insert = &insert
	}
	if edit.Replace != nil {
		replace := jsonRangeToLSP(*edit.Replace)
		mapped.Replace = &replace
		mapped.StartLine = replace.Start.Line
		mapped.StartCol = replace.Start.Character
		mapped.EndLine = replace.End.Line
		mapped.EndCol = replace.End.Character
	} else if mapped.Insert != nil && mapped.Range == nil {
		mapped.StartLine = mapped.Insert.Start.Line
		mapped.StartCol = mapped.Insert.Start.Character
		mapped.EndLine = mapped.Insert.End.Line
		mapped.EndCol = mapped.Insert.End.Character
	}
	return mapped
}

func lspRangeJSONFromRange(value LSPRange) *lspRangeJSON {
	wire := &lspRangeJSON{}
	wire.Start.Line = value.Start.Line
	wire.Start.Character = value.Start.Character
	wire.End.Line = value.End.Line
	wire.End.Character = value.End.Character
	return wire
}

func completionTextEditJSONFromTextEdit(edit TextEdit) *completionTextEditJSON {
	wire := &completionTextEditJSON{NewText: edit.NewText}
	if edit.Insert != nil || edit.Replace != nil {
		if edit.Insert != nil {
			wire.Insert = lspRangeJSONFromRange(*edit.Insert)
		}
		if edit.Replace != nil {
			wire.Replace = lspRangeJSONFromRange(*edit.Replace)
		}
		return wire
	}
	if edit.Range != nil {
		wire.Range = lspRangeJSONFromRange(*edit.Range)
		return wire
	}
	wire.Range = lspRangeJSONFromRange(LSPRange{
		Start: LSPPosition{Line: edit.StartLine, Character: edit.StartCol},
		End:   LSPPosition{Line: edit.EndLine, Character: edit.EndCol},
	})
	return wire
}

func mapCompletionItem(it completionItemJSON) LSPCompletionItem {
	item := LSPCompletionItem{
		Label:            it.Label,
		Kind:             it.Kind,
		Detail:           it.Detail,
		InsertText:       cloneStringPointer(it.InsertText),
		TextEditText:     cloneStringPointer(it.TextEditText),
		InsertTextFormat: it.InsertTextFormat,
		InsertTextMode:   it.InsertTextMode,
		SortText:         cloneStringPointer(it.SortText),
		FilterText:       cloneStringPointer(it.FilterText),
		Preselect:        it.Preselect,
		Deprecated:       it.Deprecated,
		Tags:             append([]int(nil), it.Tags...),
		Documentation:    it.Documentation,
		Data:             append(json.RawMessage(nil), it.Data...),
		CommitCharacters: cloneStringSlicePointer(it.CommitChars),
		LabelDetails:     it.LabelDetails,
	}
	if it.TextEdit != nil {
		mapped := completionTextEditFromJSON(*it.TextEdit)
		item.TextEdit = &mapped
	}
	if len(it.AdditionalTextEdits) > 0 {
		item.AdditionalEdits = textEditsFromJSON(it.AdditionalTextEdits)
	}
	return item
}

func completionItemToJSON(item LSPCompletionItem) completionItemJSON {
	wire := completionItemJSON{
		Label:            item.Label,
		Kind:             item.Kind,
		Detail:           item.Detail,
		InsertText:       cloneStringPointer(item.InsertText),
		TextEditText:     cloneStringPointer(item.TextEditText),
		InsertTextFormat: item.InsertTextFormat,
		InsertTextMode:   item.InsertTextMode,
		SortText:         cloneStringPointer(item.SortText),
		FilterText:       cloneStringPointer(item.FilterText),
		Preselect:        item.Preselect,
		Deprecated:       item.Deprecated,
		Tags:             append([]int(nil), item.Tags...),
		Documentation:    item.Documentation,
		Data:             append(json.RawMessage(nil), item.Data...),
		CommitChars:      cloneStringSlicePointer(item.CommitCharacters),
		LabelDetails:     item.LabelDetails,
	}
	if item.TextEdit != nil {
		wire.TextEdit = completionTextEditJSONFromTextEdit(*item.TextEdit)
	}
	if len(item.AdditionalEdits) > 0 {
		wire.AdditionalTextEdits = make([]lspTextEditJSON, 0, len(item.AdditionalEdits))
		for _, edit := range item.AdditionalEdits {
			wire.AdditionalTextEdits = append(wire.AdditionalTextEdits, *lspTextEditJSONFromTextEdit(edit))
		}
	}
	return wire
}

// parseCompletionItems extracts completion items from an LSP completion
// response. The response may be a list of items or a CompletionList object.
// prompt-10 10-I: includes additionalTextEdits (auto-import).
func parseCompletionItems(raw json.RawMessage) ([]completionItemJSON, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []completionItemJSON{}, false, nil
	}
	switch trimmed[0] {
	case '{':
		var list struct {
			Items        []completionItemJSON       `json:"items"`
			IsIncomplete bool                       `json:"isIncomplete"`
			ItemDefaults completionItemDefaultsJSON `json:"itemDefaults"`
		}
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return []completionItemJSON{}, false, fmt.Errorf("parse completion list: %w", err)
		}
		if list.Items == nil {
			list.Items = []completionItemJSON{}
		}
		for index := range list.Items {
			applyCompletionItemDefaults(&list.Items[index], list.ItemDefaults)
		}
		return list.Items, list.IsIncomplete, nil
	case '[':
		var items []completionItemJSON
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return []completionItemJSON{}, false, fmt.Errorf("parse completion items: %w", err)
		}
		if items == nil {
			items = []completionItemJSON{}
		}
		return items, false, nil
	default:
		return []completionItemJSON{}, false, fmt.Errorf("parse completion response: unexpected JSON token %q", trimmed[0])
	}
}

func applyCompletionItemDefaults(item *completionItemJSON, defaults completionItemDefaultsJSON) {
	if item == nil {
		return
	}
	if item.CommitChars == nil && defaults.CommitChars != nil {
		item.CommitChars = cloneStringSlicePointer(defaults.CommitChars)
	}
	if item.InsertTextFormat == 0 && defaults.InsertTextFormat != nil {
		item.InsertTextFormat = *defaults.InsertTextFormat
	}
	if item.InsertTextMode == nil && defaults.InsertTextMode != nil {
		mode := *defaults.InsertTextMode
		item.InsertTextMode = &mode
	}
	if item.TextEdit != nil || len(defaults.EditRange) == 0 {
		return
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(defaults.EditRange, &shape) != nil {
		return
	}
	edit := &completionTextEditJSON{}
	if _, direct := shape["start"]; direct {
		var rangeValue lspRangeJSON
		if json.Unmarshal(defaults.EditRange, &rangeValue) != nil {
			return
		}
		edit.Range = &rangeValue
	} else {
		insertRaw, hasInsert := shape["insert"]
		replaceRaw, hasReplace := shape["replace"]
		if !hasInsert || !hasReplace {
			return
		}
		var insertRange, replaceRange lspRangeJSON
		if json.Unmarshal(insertRaw, &insertRange) != nil || json.Unmarshal(replaceRaw, &replaceRange) != nil {
			return
		}
		edit.Insert = &insertRange
		edit.Replace = &replaceRange
	}
	newText := item.Label
	if item.InsertText != nil {
		newText = *item.InsertText
	}
	if item.TextEditText != nil {
		newText = *item.TextEditText
	}
	edit.NewText = newText
	item.TextEdit = edit
}

func mapCompletionItems(items []completionItemJSON) []LSPCompletionItem {
	out := make([]LSPCompletionItem, 0, len(items))
	for _, item := range items {
		out = append(out, mapCompletionItem(item))
	}
	return out
}

// parseHover extracts the markdown string from an LSP hover response.
func parseHover(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var hover struct {
		Contents struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &hover); err == nil {
		return hover.Contents.Value
	}
	// Contents may be a plain string or a MarkupContent.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

// pathToURI converts a filesystem path to a file:// URI (prompt-8 Task 8-C).
// Absolute paths are preferred; relative paths are Abs'd against the process
// cwd. Windows drive letters become file:///C:/...
func pathToURI(p string) string {
	if p == "" {
		return ""
	}
	// Strip accidental file:// prefix from callers.
	if strings.HasPrefix(p, "file://") {
		p = strings.TrimPrefix(p, "file://")
		// Windows: /C:/... after strip
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
	}
	// Resolve relative paths only. Do not Abs POSIX-style absolute paths on
	// Windows (filepath.Abs would incorrectly prefix the drive).
	if !filepath.IsAbs(p) {
		isPOSIXAbs := strings.HasPrefix(p, "/")
		if runtime.GOOS != "windows" || !isPOSIXAbs {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
		}
	}
	cleaned := filepath.ToSlash(p)
	// Windows: C:/foo → /C:/foo for file URI form
	if len(cleaned) >= 2 && cleaned[1] == ':' {
		cleaned = "/" + cleaned
	} else if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	return "file://" + cleaned
}

// uriToPath converts a file:// URI back to a filesystem path.
func uriToPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}
	p := strings.TrimPrefix(uri, "file://")
	// file:///C:/... → C:/...
	if len(p) >= 3 && p[0] == '/' && ((p[1] >= 'A' && p[1] <= 'Z') || (p[1] >= 'a' && p[1] <= 'z')) && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

func parseLocations(raw json.RawMessage) []LSPLocation {
	if len(raw) == 0 || string(raw) == "null" {
		return []LSPLocation{}
	}
	type loc struct {
		URI   string `json:"uri"`
		Range struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
			End struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"end"`
		} `json:"range"`
	}
	// Single Location
	var one loc
	if err := json.Unmarshal(raw, &one); err == nil && one.URI != "" {
		return []LSPLocation{{
			FilePath:  uriToPath(one.URI),
			Line:      one.Range.Start.Line,
			Column:    one.Range.Start.Character,
			EndLine:   one.Range.End.Line,
			EndColumn: one.Range.End.Character,
		}}
	}
	// Location[] or LocationLink[]
	var many []loc
	if err := json.Unmarshal(raw, &many); err == nil {
		out := make([]LSPLocation, 0, len(many))
		for _, l := range many {
			if l.URI == "" {
				continue
			}
			out = append(out, LSPLocation{
				FilePath:  uriToPath(l.URI),
				Line:      l.Range.Start.Line,
				Column:    l.Range.Start.Character,
				EndLine:   l.Range.End.Line,
				EndColumn: l.Range.End.Character,
			})
		}
		return out
	}
	// LocationLink[]
	var links []struct {
		TargetURI   string `json:"targetUri"`
		TargetRange struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
			End struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"end"`
		} `json:"targetRange"`
	}
	if err := json.Unmarshal(raw, &links); err == nil {
		out := make([]LSPLocation, 0, len(links))
		for _, l := range links {
			if l.TargetURI == "" {
				continue
			}
			out = append(out, LSPLocation{
				FilePath:  uriToPath(l.TargetURI),
				Line:      l.TargetRange.Start.Line,
				Column:    l.TargetRange.Start.Character,
				EndLine:   l.TargetRange.End.Line,
				EndColumn: l.TargetRange.End.Character,
			})
		}
		return out
	}
	return []LSPLocation{}
}

func parseTextEdits(raw json.RawMessage) []TextEdit {
	if len(raw) == 0 || string(raw) == "null" {
		return []TextEdit{}
	}
	var edits []struct {
		Range struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
			End struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"end"`
		} `json:"range"`
		NewText string `json:"newText"`
	}
	if err := json.Unmarshal(raw, &edits); err != nil {
		return []TextEdit{}
	}
	out := make([]TextEdit, 0, len(edits))
	for _, e := range edits {
		out = append(out, TextEdit{
			StartLine: e.Range.Start.Line,
			StartCol:  e.Range.Start.Character,
			EndLine:   e.Range.End.Line,
			EndCol:    e.Range.End.Character,
			NewText:   e.NewText,
		})
	}
	return out
}

type lspTextEditJSON struct {
	Range struct {
		Start struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"start"`
		End struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"end"`
	} `json:"range"`
	Insert  *lspRangeJSON `json:"insert,omitempty"`
	Replace *lspRangeJSON `json:"replace,omitempty"`
	NewText string        `json:"newText"`
}

func textEditsFromJSON(edits []lspTextEditJSON) []TextEdit {
	out := make([]TextEdit, 0, len(edits))
	for _, e := range edits {
		startLine := e.Range.Start.Line
		startCol := e.Range.Start.Character
		endLine := e.Range.End.Line
		endCol := e.Range.End.Character
		if e.Replace != nil {
			startLine = e.Replace.Start.Line
			startCol = e.Replace.Start.Character
			endLine = e.Replace.End.Line
			endCol = e.Replace.End.Character
		} else if e.Insert != nil {
			startLine = e.Insert.Start.Line
			startCol = e.Insert.Start.Character
			endLine = e.Insert.End.Line
			endCol = e.Insert.End.Character
		}
		out = append(out, TextEdit{
			StartLine: startLine,
			StartCol:  startCol,
			EndLine:   endLine,
			EndCol:    endCol,
			NewText:   e.NewText,
		})
	}
	return out
}

func lspTextEditJSONFromTextEdit(edit TextEdit) *lspTextEditJSON {
	wire := &lspTextEditJSON{NewText: edit.NewText}
	wire.Range.Start.Line = edit.StartLine
	wire.Range.Start.Character = edit.StartCol
	wire.Range.End.Line = edit.EndLine
	wire.Range.End.Character = edit.EndCol
	return wire
}

func parseWorkspaceEditsForURI(raw json.RawMessage, wantURI string) []TextEdit {
	all := parseWorkspaceEditsAll(raw)
	for _, f := range all {
		if pathToURI(f.FilePath) == wantURI || f.FilePath == uriToPath(wantURI) {
			return f.Edits
		}
	}
	return []TextEdit{}
}

// parseWorkspaceEditsAll returns edits for every file in a WorkspaceEdit (9-B).
func parseWorkspaceEditsAll(raw json.RawMessage) []FileTextEdits {
	if len(raw) == 0 || string(raw) == "null" {
		return []FileTextEdits{}
	}
	var we struct {
		Changes         map[string][]lspTextEditJSON `json:"changes"`
		DocumentChanges []struct {
			TextDocument struct {
				URI     string `json:"uri"`
				Version *int   `json:"version"`
			} `json:"textDocument"`
			Edits []lspTextEditJSON `json:"edits"`
		} `json:"documentChanges"`
	}
	if err := json.Unmarshal(raw, &we); err != nil {
		return []FileTextEdits{}
	}
	var out []FileTextEdits
	for uri, edits := range we.Changes {
		out = append(out, FileTextEdits{
			FilePath: uriToPath(uri),
			Edits:    textEditsFromJSON(edits),
		})
	}
	for _, dc := range we.DocumentChanges {
		if dc.TextDocument.URI == "" {
			continue
		}
		out = append(out, FileTextEdits{
			FilePath: uriToPath(dc.TextDocument.URI),
			Version:  dc.TextDocument.Version,
			Edits:    textEditsFromJSON(dc.Edits),
		})
	}
	return out
}

func parseSignatureHelp(raw json.RawMessage) *SignatureHelpResult {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var sh struct {
		Signatures []struct {
			Label         string      `json:"label"`
			Documentation interface{} `json:"documentation"`
			Parameters    []struct {
				Label         interface{} `json:"label"`
				Documentation interface{} `json:"documentation"`
			} `json:"parameters"`
		} `json:"signatures"`
		ActiveSignature *int `json:"activeSignature"`
		ActiveParameter *int `json:"activeParameter"`
	}
	if err := json.Unmarshal(raw, &sh); err != nil || len(sh.Signatures) == 0 {
		return nil
	}
	asi := 0
	if sh.ActiveSignature != nil {
		asi = *sh.ActiveSignature
	}
	if asi < 0 || asi >= len(sh.Signatures) {
		asi = 0
	}
	sig := sh.Signatures[asi]
	params := make([]ParameterInfo, 0, len(sig.Parameters))
	for _, p := range sig.Parameters {
		pLabel := ""
		switch v := p.Label.(type) {
		case string:
			pLabel = v
		case []interface{}:
			// LSP allows label as [start, end] character offsets — use empty string.
			pLabel = ""
		}
		pDoc := ""
		switch v := p.Documentation.(type) {
		case string:
			pDoc = v
		case map[string]interface{}:
			if s, ok := v["value"].(string); ok {
				pDoc = s
			}
		}
		params = append(params, ParameterInfo{Label: pLabel, Documentation: pDoc})
	}
	doc := ""
	switch v := sig.Documentation.(type) {
	case string:
		doc = v
	case map[string]interface{}:
		if s, ok := v["value"].(string); ok {
			doc = s
		}
	}
	ap := 0
	if sh.ActiveParameter != nil {
		ap = *sh.ActiveParameter
	}
	return &SignatureHelpResult{
		Label:           sig.Label,
		Documentation:   doc,
		Parameters:      params,
		ActiveParameter: ap,
		ActiveSignature: asi,
	}
}

func parseCodeActionEdits(raw json.RawMessage, wantURI string) []TextEdit {
	if len(raw) == 0 || string(raw) == "null" {
		return []TextEdit{}
	}
	// CodeAction[] may embed either WorkspaceEdit.changes or documentChanges.
	var actions []struct {
		Edit json.RawMessage `json:"edit"`
	}
	if err := json.Unmarshal(raw, &actions); err != nil {
		return []TextEdit{}
	}
	for _, a := range actions {
		if len(a.Edit) == 0 || string(a.Edit) == "null" {
			continue
		}
		if edits := parseWorkspaceEditsForURI(a.Edit, wantURI); len(edits) > 0 {
			return edits
		}
	}
	return []TextEdit{}
}

func resolveOrganizeImportActions(srv *lspServer, raw json.RawMessage, wantURI string) ([]TextEdit, error) {
	actions := codeActionItems(raw)
	for _, actionRaw := range actions {
		var fields map[string]json.RawMessage
		if json.Unmarshal(actionRaw, &fields) != nil {
			continue
		}
		if disabled, ok := fields["disabled"]; ok && len(disabled) > 0 && string(disabled) != "null" {
			continue
		}
		listRaw := json.RawMessage(append(append([]byte{'['}, actionRaw...), ']'))
		edits := parseCodeActionEdits(listRaw, wantURI)
		command, arguments := parseCodeActionCommand(actionRaw)
		if len(edits) == 0 && command == "" && len(fields["data"]) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
			resolved, err := srv.client.request(ctx, "codeAction/resolve", actionRaw)
			cancel()
			if err != nil {
				return []TextEdit{}, err
			}
			listRaw = json.RawMessage(append(append([]byte{'['}, resolved...), ']'))
			edits = parseCodeActionEdits(listRaw, wantURI)
			command, arguments = parseCodeActionCommand(resolved)
		}
		if len(edits) == 0 && command == "" {
			continue
		}
		if command != "" {
			ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
			_, err := srv.client.request(ctx, "workspace/executeCommand", map[string]interface{}{
				"command": command, "arguments": arguments,
			})
			cancel()
			if err != nil {
				return []TextEdit{}, err
			}
		}
		return edits, nil
	}
	return []TextEdit{}, nil
}

func codeActionItems(raw json.RawMessage) []json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(trimmed, &items) == nil {
			return items
		}
		return nil
	}
	return []json.RawMessage{append(json.RawMessage(nil), trimmed...)}
}

func parseCodeActionCommand(raw json.RawMessage) (string, []interface{}) {
	for _, actionRaw := range codeActionItems(raw) {
		var fields map[string]json.RawMessage
		if json.Unmarshal(actionRaw, &fields) != nil {
			continue
		}
		commandRaw := bytes.TrimSpace(fields["command"])
		if len(commandRaw) == 0 || bytes.Equal(commandRaw, []byte("null")) {
			continue
		}
		if commandRaw[0] == '"' {
			var command string
			var arguments []interface{}
			if json.Unmarshal(commandRaw, &command) == nil {
				_ = json.Unmarshal(fields["arguments"], &arguments)
				return command, arguments
			}
			continue
		}
		var nested struct {
			Command   string        `json:"command"`
			Arguments []interface{} `json:"arguments"`
		}
		if json.Unmarshal(commandRaw, &nested) == nil && nested.Command != "" {
			return nested.Command, nested.Arguments
		}
	}
	return "", nil
}

// ============================================================================
// G-COMP-02: parsers for documentSymbol, workspace/symbol, semanticTokens.
// ============================================================================

// parseDocumentSymbols parses textDocument/documentSymbol responses. Supports
// both hierarchical DocumentSymbol[] and flat SymbolInformation[] shapes.
// We detect the shape by probing for the "location" field (SymbolInformation)
// vs the "selectionRange" field (DocumentSymbol).
func parseDocumentSymbols(raw json.RawMessage) []LSPDocumentSymbol {
	if len(raw) == 0 || string(raw) == "null" {
		return []LSPDocumentSymbol{}
	}
	// Probe: if the first element has a "location" field, it's flat
	// SymbolInformation[]; otherwise treat it as hierarchical DocumentSymbol[].
	var probe []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || len(probe) == 0 {
		return []LSPDocumentSymbol{}
	}
	_, hasLocation := probe[0]["location"]
	if hasLocation {
		return parseFlatSymbolInformation(raw)
	}
	return parseHierarchicalDocumentSymbols(raw)
}

// parseHierarchicalDocumentSymbols parses the DocumentSymbol[] shape.
func parseHierarchicalDocumentSymbols(raw json.RawMessage) []LSPDocumentSymbol {
	var docSyms []struct {
		Name           string            `json:"name"`
		Detail         string            `json:"detail"`
		Kind           int               `json:"kind"`
		Range          lspRangeJSON      `json:"range"`
		SelectionRange lspRangeJSON      `json:"selectionRange"`
		Children       []json.RawMessage `json:"children"`
	}
	if err := json.Unmarshal(raw, &docSyms); err != nil || len(docSyms) == 0 {
		return []LSPDocumentSymbol{}
	}
	out := make([]LSPDocumentSymbol, 0, len(docSyms))
	for _, d := range docSyms {
		sym := LSPDocumentSymbol{
			Name:           d.Name,
			Detail:         d.Detail,
			Kind:           d.Kind,
			Range:          jsonRangeToLSP(d.Range),
			SelectionRange: jsonRangeToLSP(d.SelectionRange),
		}
		if len(d.Children) > 0 {
			childBytes, _ := json.Marshal(d.Children)
			sym.Children = parseDocumentSymbols(childBytes)
		}
		out = append(out, sym)
	}
	return out
}

// parseFlatSymbolInformation parses the legacy SymbolInformation[] shape.
func parseFlatSymbolInformation(raw json.RawMessage) []LSPDocumentSymbol {
	var symInfos []struct {
		Name          string       `json:"name"`
		Kind          int          `json:"kind"`
		ContainerName string       `json:"containerName"`
		Location      locationJSON `json:"location"`
	}
	if err := json.Unmarshal(raw, &symInfos); err != nil || len(symInfos) == 0 {
		return []LSPDocumentSymbol{}
	}
	out := make([]LSPDocumentSymbol, 0, len(symInfos))
	for _, si := range symInfos {
		r := jsonRangeToLSP(si.Location.Range)
		out = append(out, LSPDocumentSymbol{
			Name:           si.Name,
			Kind:           si.Kind,
			Range:          r,
			SelectionRange: r,
		})
	}
	return out
}

// parseSymbolInformation parses workspace/symbol responses (SymbolInformation[]).
func parseSymbolInformation(raw json.RawMessage) []LSPSymbolInformation {
	if len(raw) == 0 || string(raw) == "null" {
		return []LSPSymbolInformation{}
	}
	var syms []struct {
		Name          string       `json:"name"`
		Kind          int          `json:"kind"`
		ContainerName string       `json:"containerName"`
		Location      locationJSON `json:"location"`
	}
	if err := json.Unmarshal(raw, &syms); err != nil || len(syms) == 0 {
		return []LSPSymbolInformation{}
	}
	out := make([]LSPSymbolInformation, 0, len(syms))
	for _, si := range syms {
		if si.Location.URI == "" {
			continue
		}
		out = append(out, LSPSymbolInformation{
			Name:          si.Name,
			Kind:          si.Kind,
			ContainerName: si.ContainerName,
			FilePath:      uriToPath(si.Location.URI),
			Line:          si.Location.Range.Start.Line,
			Column:        si.Location.Range.Start.Character,
			EndLine:       si.Location.Range.End.Line,
			EndColumn:     si.Location.Range.End.Character,
		})
	}
	return out
}

// parseSemanticTokens decodes the LSP semanticTokens relative-position delta
// encoding. Each token is 5 integers: [deltaLine, deltaStart, length,
// tokenType, tokenModifiers]. We convert to absolute positions.
func parseSemanticTokens(raw json.RawMessage) []SemanticToken {
	if len(raw) == 0 || string(raw) == "null" {
		return []SemanticToken{}
	}
	var res struct {
		Data []int `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || len(res.Data) == 0 {
		return []SemanticToken{}
	}
	data := res.Data
	if len(data)%5 != 0 {
		return []SemanticToken{}
	}
	out := make([]SemanticToken, 0, len(data)/5)
	prevLine, prevCol := 0, 0
	for i := 0; i+4 < len(data); i += 5 {
		deltaLine := data[i]
		deltaCol := data[i+1]
		length := data[i+2]
		tokenType := data[i+3]
		tokenMods := data[i+4]
		line := prevLine + deltaLine
		var col int
		if deltaLine == 0 {
			col = prevCol + deltaCol
		} else {
			col = deltaCol
		}
		prevLine = line
		prevCol = col
		mods := decodeTokenModifiers(tokenMods)
		out = append(out, SemanticToken{
			Line:      line,
			Column:    col,
			Length:    length,
			Type:      tokenType,
			Modifiers: mods,
		})
	}
	return out
}

// decodeTokenModifiers converts the bitmask into a list of modifier indices.
func decodeTokenModifiers(bitmask int) []int {
	if bitmask == 0 {
		return nil
	}
	var mods []int
	for i := 0; i < 32; i++ {
		if bitmask&(1<<uint(i)) != 0 {
			mods = append(mods, i)
		}
	}
	return mods
}

// lspRangeJSON mirrors LSP Range for JSON unmarshaling.
type lspRangeJSON struct {
	Start struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"start"`
	End struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"end"`
}

// locationJSON mirrors LSP Location for JSON unmarshaling.
type locationJSON struct {
	URI   string       `json:"uri"`
	Range lspRangeJSON `json:"range"`
}

// jsonRangeToLSP converts the JSON shape to LSPRange.
func jsonRangeToLSP(r lspRangeJSON) LSPRange {
	return LSPRange{
		Start: LSPPosition{Line: r.Start.Line, Character: r.Start.Character},
		End:   LSPPosition{Line: r.End.Line, Character: r.End.Character},
	}
}

// ============================================================================
// F-8 (prompt-2.md 517-535): LSP colorProvider / linkedEditingRange。
// 客户端能力已在 buildLSPClientCapabilities 第 916-928 行声明
// （colorProvider / linkedEditingRange），此处补齐后端方法 + 类型 + 解析。
//
// 与现有方法（GetCompletions 等）的差异：
//   - 签名使用 uri string / position（非 LSPCompletionRequest），因为
//     colorProvider / linkedEditingRange 由 Monaco 在已同步文档上触发，
//     无需再次 syncDocument。
//   - 重要约定 (task-2.md 105-108)：无 server 运行时返回 error（非空切片），
//     使前端能感知并降级。
//   - Range 复用 LSPRange；Position 复用 LSPPosition；TextEdit 复用现有类型。
// ============================================================================

// Color 表示 LSP RGBA 颜色（0.0~1.0 浮点分量）。F-8。
type Color struct {
	Red   float64 `json:"red"`
	Green float64 `json:"green"`
	Blue  float64 `json:"blue"`
	Alpha float64 `json:"alpha"`
}

// ColorInformation 是文档中一处颜色及其所在范围。F-8。
// 对应 LSP ColorInformation，Range 使用扁平 LSPRange。
type ColorInformation struct {
	Range LSPRange `json:"range"`
	Color Color    `json:"color"`
}

// ColorPresentation 是颜色的某种文本表示形式（如 #ff0000 / rgb(255,0,0)）。
// F-8。TextEdit 复用现有 TextEdit 类型（0-based line/col）。
type ColorPresentation struct {
	Label               string     `json:"label"`
	TextEdit            *TextEdit  `json:"textEdit,omitempty"`
	AdditionalTextEdits []TextEdit `json:"additionalTextEdits,omitempty"`
}

// LinkedEditRange 是可同步编辑的范围之一（如 HTML 起始/结束标签）。
// F-8。对应 LSP LinkedEditRanges.ranges[] 元素，Range 使用 LSPRange。
type LinkedEditRange struct {
	Range LSPRange `json:"range"`
}

// languageFromURI 通过 URI 的文件扩展名推断 LSP server key。
func languageFromURI(uri string) (string, error) {
	path := uriToPath(uri)
	if language := languagePackServerForPath(path); language != "" {
		return language, nil
	}
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".py"), strings.HasSuffix(lower, ".pyi"):
		return "python", nil
	case strings.HasSuffix(lower, ".rs"):
		return "rust", nil
	case strings.HasSuffix(lower, ".json"), strings.HasSuffix(lower, ".jsonc"):
		return "json", nil
	case strings.HasSuffix(lower, ".css"), strings.HasSuffix(lower, ".scss"), strings.HasSuffix(lower, ".less"):
		return "css", nil
	case strings.HasSuffix(lower, ".vue"):
		return "vue", nil
	case strings.HasSuffix(lower, ".html"), strings.HasSuffix(lower, ".htm"):
		return "html", nil
	case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
		return "yaml", nil
	}
	return "", fmt.Errorf("unsupported language for URI: %s", uri)
}

// GetDocumentColors 查询文档中的颜色信息（textDocument/documentColor）。F-8。
// 参数 uri 为 file:// URI；返回 ColorInformation 列表。
// 重要约定：无 LSP server 运行时返回 error（区别于 GetCompletions 等的优雅降级）。
func (s *LSPService) GetDocumentColors(uri string) ([]ColorInformation, error) {
	language, err := languageFromURI(uri)
	if err != nil {
		return nil, err
	}
	srv, err := s.serverForLanguage(language)
	if err != nil {
		return nil, err
	}
	if srv == nil {
		return nil, fmt.Errorf("not_running: language server not running for %s", language)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	}
	raw, err := srv.client.request(ctx, "textDocument/documentColor", params)
	if err != nil {
		slog.Warn("LSP documentColor failed", "language", language, "err", err)
		return nil, fmt.Errorf("rpc: %w", err)
	}
	return parseDocumentColors(raw), nil
}

// parseDocumentColors 解析 textDocument/documentColor 响应为 ColorInformation[]。F-8。
func parseDocumentColors(raw json.RawMessage) []ColorInformation {
	if len(raw) == 0 || string(raw) == "null" {
		return []ColorInformation{}
	}
	var arr []struct {
		Range lspRangeJSON `json:"range"`
		Color Color        `json:"color"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return []ColorInformation{}
	}
	out := make([]ColorInformation, 0, len(arr))
	for _, c := range arr {
		out = append(out, ColorInformation{
			Range: jsonRangeToLSP(c.Range),
			Color: c.Color,
		})
	}
	return out
}

// GetColorPresentations 查询颜色的各种文本表示形式（textDocument/colorPresentation）。F-8。
// 参数 uri 为 file:// URI；color 为 RGBA 颜色；rng 为颜色所在范围。
// 返回 ColorPresentation 列表（如 #ff0000 / rgb(255,0,0)）。
// 重要约定：无 LSP server 运行时返回 error。
func (s *LSPService) GetColorPresentations(uri string, color Color, rng LSPRange) ([]ColorPresentation, error) {
	language, err := languageFromURI(uri)
	if err != nil {
		return nil, err
	}
	srv, err := s.serverForLanguage(language)
	if err != nil {
		return nil, err
	}
	if srv == nil {
		return nil, fmt.Errorf("not_running: language server not running for %s", language)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"color": map[string]interface{}{
			"red":   color.Red,
			"green": color.Green,
			"blue":  color.Blue,
			"alpha": color.Alpha,
		},
		"range": map[string]interface{}{
			"start": map[string]int{"line": rng.Start.Line, "character": rng.Start.Character},
			"end":   map[string]int{"line": rng.End.Line, "character": rng.End.Character},
		},
	}
	raw, err := srv.client.request(ctx, "textDocument/colorPresentation", params)
	if err != nil {
		slog.Warn("LSP colorPresentation failed", "language", language, "err", err)
		return nil, fmt.Errorf("rpc: %w", err)
	}
	return parseColorPresentations(raw), nil
}

// parseColorPresentations 解析 textDocument/colorPresentation 响应为
// ColorPresentation[]。F-8。将 LSP wire 格式的 TextEdit（含 range 结构）转换
// 为现有扁平 TextEdit 类型（0-based line/col）。
func parseColorPresentations(raw json.RawMessage) []ColorPresentation {
	if len(raw) == 0 || string(raw) == "null" {
		return []ColorPresentation{}
	}
	var arr []struct {
		Label               string            `json:"label"`
		TextEdit            *lspTextEditJSON  `json:"textEdit"`
		AdditionalTextEdits []lspTextEditJSON `json:"additionalTextEdits"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return []ColorPresentation{}
	}
	out := make([]ColorPresentation, 0, len(arr))
	for _, p := range arr {
		cp := ColorPresentation{Label: p.Label}
		if p.TextEdit != nil {
			te := textEditsFromJSON([]lspTextEditJSON{*p.TextEdit})
			if len(te) > 0 {
				cp.TextEdit = &te[0]
			}
		}
		if len(p.AdditionalTextEdits) > 0 {
			cp.AdditionalTextEdits = textEditsFromJSON(p.AdditionalTextEdits)
		}
		out = append(out, cp)
	}
	return out
}

// PrepareLinkedEdits 准备同步编辑范围（textDocument/prepareLinkedEdits）。F-8。
// 非标准 LSP（VSCode 扩展），用于 HTML 标签等同步编辑场景。
// 参数 uri 为 file:// URI；position 为光标位置。
// 返回可同步编辑的范围列表（如起始/结束标签）。
// 重要约定：无 LSP server 运行时返回 error。
func (s *LSPService) PrepareLinkedEdits(uri string, position LSPPosition) ([]LinkedEditRange, error) {
	language, err := languageFromURI(uri)
	if err != nil {
		return nil, err
	}
	srv, err := s.serverForLanguage(language)
	if err != nil {
		return nil, err
	}
	if srv == nil {
		return nil, fmt.Errorf("not_running: language server not running for %s", language)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": position.Line, "character": position.Character},
	}
	raw, err := srv.client.request(ctx, "textDocument/prepareLinkedEdits", params)
	if err != nil {
		slog.Warn("LSP prepareLinkedEdits failed", "language", language, "err", err)
		return nil, fmt.Errorf("rpc: %w", err)
	}
	return parseLinkedEditRanges(raw), nil
}

// parseLinkedEditRanges 解析 textDocument/prepareLinkedEdits 响应为
// LinkedEditRange[]。F-8。LSP 响应形如 { "ranges": Range[] }，需提取 ranges 字段。
func parseLinkedEditRanges(raw json.RawMessage) []LinkedEditRange {
	if len(raw) == 0 || string(raw) == "null" {
		return []LinkedEditRange{}
	}
	var obj struct {
		Ranges []lspRangeJSON `json:"ranges"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return []LinkedEditRange{}
	}
	out := make([]LinkedEditRange, 0, len(obj.Ranges))
	for _, r := range obj.Ranges {
		out = append(out, LinkedEditRange{
			Range: jsonRangeToLSP(r),
		})
	}
	return out
}
