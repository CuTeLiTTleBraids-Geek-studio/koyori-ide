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

// ============================================================================
// G-COMP-02: Enhanced language intelligence — document outline, workspace
// symbol search, semantic tokens, and completion item resolution.
// ============================================================================

// LSPPosition is a zero-based line/character pair (LSP Position).
type LSPPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// LSPRange is a [start, end) range in a document (LSP Range).
type LSPRange struct {
	Start LSPPosition `json:"start"`
	End   LSPPosition `json:"end"`
}

// LSPDocumentSymbol is a single entry in a document outline (breadcrumb /
// outline tree). Mirrors LSP DocumentSymbol.
type LSPDocumentSymbol struct {
	Name           string              `json:"name"`
	Detail         string              `json:"detail,omitempty"`
	Kind           int                 `json:"kind"` // LSP SymbolKind (1-26)
	Range          LSPRange            `json:"range"`
	SelectionRange LSPRange            `json:"selectionRange"`
	Children       []LSPDocumentSymbol `json:"children,omitempty"`
}

// GetDocumentSymbols returns the document outline via textDocument/documentSymbol.
// Used for the outline/breadcrumb panel and Go-to-Symbol-in-file. Empty when
// the server is unavailable or the capability is unsupported.
func (s *LSPService) GetDocumentSymbols(req LSPCompletionRequest) ([]LSPDocumentSymbol, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPDocumentSymbol{}, err
	}
	if srv == nil {
		return []LSPDocumentSymbol{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
	}
	raw, err := srv.client.request(ctx, "textDocument/documentSymbol", params)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		slog.Warn("LSP documentSymbol failed", "language", req.Language, "err", err)
		return []LSPDocumentSymbol{}, nil
	}
	return parseDocumentSymbols(raw), nil
}

// LSPSymbolInformation is a workspace-wide symbol (workspace/symbol result).
// Used for Go-to-Symbol-in-workspace (Ctrl+T). File path is resolved from URI.
type LSPSymbolInformation struct {
	Name          string `json:"name"`
	Kind          int    `json:"kind"` // LSP SymbolKind (1-26)
	ContainerName string `json:"containerName,omitempty"`
	FilePath      string `json:"filePath"`
	Line          int    `json:"line"`
	Column        int    `json:"column"`
	EndLine       int    `json:"endLine"`
	EndColumn     int    `json:"endColumn"`
}

// GetWorkspaceSymbols searches the entire workspace for symbols matching the
// query (workspace/symbol). Used for "Go to Symbol in Workspace" (Ctrl+T).
// Returns empty when the server is unavailable.
func (s *LSPService) GetWorkspaceSymbols(language, query string) ([]LSPSymbolInformation, error) {
	language = lspServerKey(language)
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return []LSPSymbolInformation{}, errWorkspaceSwitching
	}
	srv, ok := s.servers[language]
	s.mu.Unlock()
	if !ok || srv == nil {
		return []LSPSymbolInformation{}, nil
	}
	return requestWorkspaceSymbols(srv, language, query)
}

// GetAllWorkspaceSymbols queries every initialized language server in
// parallel and merges results in stable language order.
func (s *LSPService) GetAllWorkspaceSymbols(query string) ([]LSPSymbolInformation, error) {
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return []LSPSymbolInformation{}, errWorkspaceSwitching
	}
	languages := make([]string, 0, len(s.servers))
	servers := make(map[string]*lspServer, len(s.servers))
	for language, srv := range s.servers {
		if srv == nil {
			continue
		}
		languages = append(languages, language)
		servers[language] = srv
	}
	s.mu.Unlock()
	sort.Strings(languages)

	results := make([][]LSPSymbolInformation, len(languages))
	errorsByIndex := make([]error, len(languages))
	var wg sync.WaitGroup
	for index, language := range languages {
		index, language := index, language
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := requestWorkspaceSymbols(servers[language], language, query)
			if err != nil {
				slog.Warn("LSP workspace/symbol failed", "language", language, "err", err)
				errorsByIndex[index] = err
				return
			}
			results[index] = items
		}()
	}
	wg.Wait()
	merged := make([]LSPSymbolInformation, 0)
	for _, items := range results {
		merged = append(merged, items...)
	}
	if len(merged) == 0 {
		for _, err := range errorsByIndex {
			if err != nil {
				return []LSPSymbolInformation{}, err
			}
		}
	}
	return merged, nil
}

// GetWorkspaceSymbolsAll is an explicit compatibility alias for integrations
// that use the noun-first method naming convention.
func (s *LSPService) GetWorkspaceSymbolsAll(query string) ([]LSPSymbolInformation, error) {
	return s.GetAllWorkspaceSymbols(query)
}

func (s *LSPService) GetWorkspaceSymbolsRaw(query string) (json.RawMessage, error) {
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return nil, errWorkspaceSwitching
	}
	languages := make([]string, 0, len(s.servers))
	servers := make(map[string]*lspServer, len(s.servers))
	for language, srv := range s.servers {
		if srv == nil {
			continue
		}
		languages = append(languages, language)
		servers[language] = srv
	}
	s.mu.Unlock()
	sort.Strings(languages)

	results := make([][]json.RawMessage, len(languages))
	errorsByIndex := make([]error, len(languages))
	var wg sync.WaitGroup
	for index, language := range languages {
		index, language := index, language
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := requestWorkspaceSymbolsRaw(servers[language], language, query)
			if err != nil {
				errorsByIndex[index] = err
				return
			}
			_ = json.Unmarshal(raw, &results[index])
		}()
	}
	wg.Wait()
	merged := make([]json.RawMessage, 0)
	for _, items := range results {
		merged = append(merged, items...)
	}
	if len(merged) == 0 {
		for _, err := range errorsByIndex {
			if err != nil {
				return nil, err
			}
		}
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func requestWorkspaceSymbols(srv *lspServer, language, query string) ([]LSPSymbolInformation, error) {
	raw, err := requestWorkspaceSymbolsRaw(srv, language, query)
	if err != nil {
		return []LSPSymbolInformation{}, err
	}
	return parseSymbolInformation(raw), nil
}

func requestWorkspaceSymbolsRaw(srv *lspServer, language, query string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	params := map[string]interface{}{"query": query}
	raw, err := srv.client.request(ctx, "workspace/symbol", params)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		slog.Warn("LSP workspace/symbol failed", "language", language, "err", err)
		return json.RawMessage(`[]`), err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return json.RawMessage(`[]`), err
	}
	return raw, nil
}

// SemanticToken is a single semantic token decoded from the LSP
// relative-position delta encoding. Used for semantic highlighting.
type SemanticToken struct {
	Line      int   `json:"line"`
	Column    int   `json:"column"`
	Length    int   `json:"length"`
	Type      int   `json:"type"` // index into tokenTypes
	Modifiers []int `json:"modifiers,omitempty"`
}

// LSPSemanticTokensEdit is one edit in a semantic-tokens delta response.
type LSPSemanticTokensEdit struct {
	Start       int   `json:"start"`
	DeleteCount int   `json:"deleteCount"`
	Data        []int `json:"data,omitempty"`
}

// LSPSemanticTokensResult preserves the opaque resultId and either a full
// relative token stream or delta edits returned by the language server.
type LSPSemanticTokensResult struct {
	ResultID string                  `json:"resultId,omitempty"`
	Data     []int                   `json:"data"`
	Edits    []LSPSemanticTokensEdit `json:"edits,omitempty"`
}

func semanticTokenIndex(names []string) map[string]int {
	indices := make(map[string]int, len(names))
	for index, name := range names {
		indices[name] = index
	}
	return indices
}

func remapSemanticModifierMask(mask int, serverModifiers []string, canonicalModifiers map[string]int) (int, error) {
	if mask < 0 {
		return 0, errors.New("semantic token modifier mask is negative")
	}
	remapped := 0
	for bit := 0; bit < 32; bit++ {
		if mask&(1<<uint(bit)) == 0 || bit >= len(serverModifiers) {
			continue
		}
		canonicalBit, ok := canonicalModifiers[serverModifiers[bit]]
		if !ok || canonicalBit >= 32 {
			continue
		}
		remapped |= 1 << uint(canonicalBit)
	}
	return remapped, nil
}

func remapSemanticTokenData(data []int, start int, serverTypes, serverModifiers []string) ([]int, error) {
	if start < 0 {
		return nil, errors.New("semantic token edit start is negative")
	}
	canonicalTypes := semanticTokenIndex(canonicalSemanticTokenTypes)
	canonicalModifiers := semanticTokenIndex(canonicalSemanticTokenModifiers)
	variableType := canonicalTypes["variable"]
	remapped := append([]int(nil), data...)
	for index, value := range remapped {
		position := ((start % 5) + (index % 5)) % 5
		switch position {
		case 0, 1, 2:
			if value < 0 {
				return nil, fmt.Errorf("semantic token tuple position %d is negative", position)
			}
		case 3:
			if value < 0 {
				return nil, errors.New("semantic token type index is negative")
			}
			remapped[index] = variableType
			if value < len(serverTypes) {
				if canonicalType, ok := canonicalTypes[serverTypes[value]]; ok {
					remapped[index] = canonicalType
				}
			}
		case 4:
			mappedMask, err := remapSemanticModifierMask(value, serverModifiers, canonicalModifiers)
			if err != nil {
				return nil, err
			}
			remapped[index] = mappedMask
		}
	}
	return remapped, nil
}

func remapSemanticTokensResult(
	result LSPSemanticTokensResult,
	deltaResponse bool,
	serverTypes, serverModifiers []string,
) (LSPSemanticTokensResult, error) {
	if result.Data != nil {
		if len(result.Data)%5 != 0 {
			return LSPSemanticTokensResult{}, errors.New("semantic token full data length is not divisible by five")
		}
		data, err := remapSemanticTokenData(result.Data, 0, serverTypes, serverModifiers)
		if err != nil {
			return LSPSemanticTokensResult{}, err
		}
		result.Data = data
		result.Edits = nil
		return result, nil
	}
	if !deltaResponse || result.Edits == nil {
		return LSPSemanticTokensResult{}, errors.New("semantic token response contains neither full data nor delta edits")
	}
	result.Edits = append([]LSPSemanticTokensEdit(nil), result.Edits...)
	for index := range result.Edits {
		edit := &result.Edits[index]
		if edit.Start < 0 || edit.DeleteCount < 0 {
			return LSPSemanticTokensResult{}, errors.New("semantic token delta edit has a negative range")
		}
		if edit.Data == nil {
			continue
		}
		data, err := remapSemanticTokenData(edit.Data, edit.Start, serverTypes, serverModifiers)
		if err != nil {
			return LSPSemanticTokensResult{}, err
		}
		edit.Data = data
	}
	return result, nil
}

func decodeSemanticTokensResult(
	raw json.RawMessage,
	deltaResponse bool,
	serverTypes, serverModifiers []string,
) (LSPSemanticTokensResult, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return LSPSemanticTokensResult{}, errors.New("semantic token response is empty")
	}
	var result LSPSemanticTokensResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return LSPSemanticTokensResult{}, err
	}
	return remapSemanticTokensResult(result, deltaResponse, serverTypes, serverModifiers)
}

func applySemanticTokenEdits(previous []int, edits []LSPSemanticTokensEdit) ([]int, error) {
	previousEditEnd := 0
	nextLength := len(previous)
	for _, edit := range edits {
		if edit.Start < previousEditEnd || edit.Start > len(previous) || edit.DeleteCount > len(previous)-edit.Start {
			return nil, errors.New("semantic token delta edits overlap or exceed the cached token stream")
		}
		editEnd := edit.Start + edit.DeleteCount
		previousEditEnd = editEnd
		nextLength += len(edit.Data) - edit.DeleteCount
	}
	if nextLength < 0 || nextLength%5 != 0 {
		return nil, errors.New("semantic token delta produces an invalid token stream length")
	}
	next := make([]int, nextLength)
	sourceEnd := len(previous)
	targetEnd := len(next)
	for index := len(edits) - 1; index >= 0; index-- {
		edit := edits[index]
		editEnd := edit.Start + edit.DeleteCount
		copyLength := sourceEnd - editEnd
		if copyLength > 0 {
			targetEnd -= copyLength
			copy(next[targetEnd:], previous[editEnd:sourceEnd])
		}
		if len(edit.Data) > 0 {
			targetEnd -= len(edit.Data)
			copy(next[targetEnd:], edit.Data)
		}
		sourceEnd = edit.Start
	}
	if sourceEnd > 0 {
		copy(next, previous[:sourceEnd])
	}
	return next, nil
}

// GetSemanticTokens returns semantic tokens for the whole document
// (textDocument/semanticTokens/full). Used for semantic syntax highlighting
// (e.g. distinguishing types from values, marking readonly fields). Empty when
// the server is unavailable or the capability is unsupported.
func (s *LSPService) GetSemanticTokens(req LSPCompletionRequest) ([]SemanticToken, error) {
	data, err := s.GetSemanticTokenData(req)
	if err != nil || len(data) == 0 {
		return []SemanticToken{}, err
	}
	raw, _ := json.Marshal(map[string]interface{}{"data": data})
	return parseSemanticTokens(raw), nil
}

// GetSemanticTokenData returns the original relative integer stream from
// textDocument/semanticTokens/full.
func (s *LSPService) GetSemanticTokenData(req LSPCompletionRequest) ([]int, error) {
	result, err := s.GetSemanticTokensDelta(req, "")
	if err != nil {
		return []int{}, err
	}
	return append([]int(nil), result.Data...), nil
}

// GetSemanticTokensDelta forwards full/delta semantic-token requests while
// preserving the server-owned resultId. A server that rejects delta is retried
// once with a full request so highlighting continues to degrade gracefully.
func (s *LSPService) GetSemanticTokensDelta(req LSPCompletionRequest, previousResultID string) (LSPSemanticTokensResult, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return LSPSemanticTokensResult{}, err
	}
	if srv == nil {
		return LSPSemanticTokensResult{}, nil
	}
	uri := pathToURI(req.FilePath)
	requestSequence, previousData, hasPreviousData := srv.beginSemanticTokenRequest(uri, previousResultID)

	request := func(method string, includePrevious bool) (json.RawMessage, error) {
		ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
		defer cancel()
		params := map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
		}
		if includePrevious {
			params["previousResultId"] = previousResultID
		}
		return srv.client.request(ctx, method, params)
	}
	serverTypes, serverModifiers := srv.semanticTokenLegend()
	requestFull := func() (LSPSemanticTokensResult, error) {
		raw, requestErr := request("textDocument/semanticTokens/full", false)
		if requestErr != nil {
			return LSPSemanticTokensResult{}, requestErr
		}
		return decodeSemanticTokensResult(raw, false, serverTypes, serverModifiers)
	}

	if previousResultID != "" {
		raw, requestErr := request("textDocument/semanticTokens/full/delta", true)
		if requestErr == nil {
			var result LSPSemanticTokensResult
			result, requestErr = decodeSemanticTokensResult(raw, true, serverTypes, serverModifiers)
			if requestErr == nil {
				if result.Data != nil {
					srv.cacheSemanticTokenResult(uri, requestSequence, result.ResultID, result.Data)
					return result, nil
				}
				if result.ResultID == "" {
					requestErr = errors.New("semantic token delta response is missing resultId")
				} else if !hasPreviousData {
					// A caller may own the previous stream (Monaco does) even when this
					// backend was restarted and lost its cache. Preserve the valid delta
					// response, but do not cache data that cannot be reconstructed.
					return result, nil
				} else {
					var combined []int
					combined, requestErr = applySemanticTokenEdits(previousData, result.Edits)
					if requestErr == nil {
						srv.cacheSemanticTokenResult(uri, requestSequence, result.ResultID, combined)
						return result, nil
					}
				}
			}
		}
		slog.Warn("LSP semanticTokens/full/delta failed; retrying full", "language", req.Language, "err", requestErr)
	}

	result, err := requestFull()
	if err != nil {
		slog.Warn("LSP semanticTokens request failed", "language", req.Language, "err", err)
		if cached, ok := srv.cachedSemanticTokenResult(uri); ok {
			return LSPSemanticTokensResult{ResultID: cached.ResultID, Data: cached.Data}, nil
		}
		return LSPSemanticTokensResult{}, nil
	}
	srv.cacheSemanticTokenResult(uri, requestSequence, result.ResultID, result.Data)
	return result, nil
}

// GetSemanticTokenDataForFile supplies the file-path-only compatibility API
// requested by prompt-1 without changing the existing GetSemanticTokens bind.
func (s *LSPService) GetSemanticTokenDataForFile(filePath string) ([]int, error) {
	language := lspLanguageForFilePath(filePath)
	if language == "" {
		return []int{}, nil
	}
	content, err := s.contentForLSPFile(language, filePath)
	if err != nil {
		return []int{}, nil
	}
	return s.GetSemanticTokenData(LSPCompletionRequest{
		Language: language,
		FilePath: filePath,
		Content:  content,
	})
}

func lspLanguageForFilePath(filePath string) string {
	lower := strings.ToLower(filePath)
	switch {
	case strings.HasSuffix(lower, ".go"):
		return "go"
	case strings.HasSuffix(lower, ".py"), strings.HasSuffix(lower, ".pyi"):
		return "python"
	case strings.HasSuffix(lower, ".rs"):
		return "rust"
	case strings.HasSuffix(lower, ".tsx"), strings.HasSuffix(lower, ".ts"):
		return "typescript"
	case strings.HasSuffix(lower, ".jsx"), strings.HasSuffix(lower, ".js"):
		return "javascript"
	case strings.HasSuffix(lower, ".json"), strings.HasSuffix(lower, ".jsonc"):
		return "json"
	case strings.HasSuffix(lower, ".css"), strings.HasSuffix(lower, ".scss"), strings.HasSuffix(lower, ".less"):
		return "css"
	case strings.HasSuffix(lower, ".html"), strings.HasSuffix(lower, ".htm"):
		return "html"
	case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
		return "yaml"
	case strings.HasSuffix(lower, ".vue"):
		return "vue"
	default:
		return ""
	}
}

func (s *LSPService) contentForLSPFile(language, filePath string) (string, error) {
	srv, err := s.serverForLanguage(language)
	if err != nil {
		return "", err
	}
	if srv != nil {
		uri := pathToURI(filePath)
		srv.docMu.Lock()
		if pending := srv.pendingChanges[uri]; pending != nil {
			content := pending.content
			srv.docMu.Unlock()
			return content, nil
		}
		if _, opened := srv.docVersions[uri]; opened {
			content := srv.docLastContent[uri]
			srv.docMu.Unlock()
			return content, nil
		}
		srv.docMu.Unlock()
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// ResolveCompletionItem resolves additional details (documentation, detail,
// additionalTextEdits) for a completion item via completionItem/resolve.
// Returns the original item unchanged when the server is unavailable or
// resolution fails (graceful degradation).
func (s *LSPService) ResolveCompletionItem(language string, item LSPCompletionItem) (LSPCompletionItem, error) {
	language = lspServerKey(language)
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return item, errWorkspaceSwitching
	}
	srv, ok := s.servers[language]
	s.mu.Unlock()
	if !ok || srv == nil {
		return item, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	// A2: round-trip the complete item, especially the server-owned data token.
	params := completionItemToJSON(item)
	raw, err := srv.client.request(ctx, "completionItem/resolve", params)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		slog.Warn("LSP completionItem/resolve failed", "language", language, "err", err)
		return item, nil
	}
	resolved := params
	if err := json.Unmarshal(raw, &resolved); err != nil {
		return item, nil
	}
	return mapCompletionItem(resolved), nil
}

// LSPCallStatus is the last call outcome for StatusBar (prompt-9 9-D).
type LSPCallStatus struct {
	Language string `json:"language"`
	Code     string `json:"code"` // ok | not_running | timeout | rpc | unavailable
	Message  string `json:"message"`
}

func (s *LSPService) setCallStatus(language, code, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastErrors == nil {
		s.lastErrors = make(map[string]string)
	}
	if code == "ok" || code == "" {
		delete(s.lastErrors, language)
	} else {
		s.lastErrors[language] = code + ": " + message
	}
}

// GetCallStatus returns the last non-ok status message for a language (9-D).
func (s *LSPService) GetCallStatus(language string) LSPCallStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := ""
	if s.lastErrors != nil {
		msg = s.lastErrors[language]
	}
	code := "ok"
	if msg != "" {
		if strings.HasPrefix(msg, "not_running") {
			code = "not_running"
		} else if strings.HasPrefix(msg, "timeout") {
			code = "timeout"
		} else if strings.HasPrefix(msg, "rpc") {
			code = "rpc"
		} else {
			code = "unavailable"
		}
	}
	return LSPCallStatus{Language: language, Code: code, Message: msg}
}
