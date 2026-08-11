package services

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// GetCompletions queries the LSP server for completions at the given position.
// prompt-9 9-D: sets call status; returns empty items when not running (graceful)
// but records not_running/rpc for StatusBar. RPC errors are still soft for UI.
func (s *LSPService) GetCompletions(req LSPCompletionRequest) ([]LSPCompletionItem, error) {
	response, err := s.getCompletionResponse(req)
	return response.Items, err
}

// GetCompletionList exposes CompletionList.isIncomplete for newer callers
// while GetCompletions remains source-compatible with existing bindings.
func (s *LSPService) GetCompletionList(req LSPCompletionRequest) (LSPCompletionResponse, error) {
	return s.getCompletionResponse(req)
}

func (s *LSPService) getCompletionResponse(req LSPCompletionRequest) (LSPCompletionResponse, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		s.setCallStatus(req.Language, "rpc", err.Error())
		return LSPCompletionResponse{Items: []LSPCompletionItem{}}, err
	}
	if srv == nil {
		s.setCallStatus(req.Language, "not_running", "language server not running")
		return LSPCompletionResponse{Items: []LSPCompletionItem{}}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"position":     map[string]int{"line": req.Line, "character": req.Column},
	}
	if req.TriggerKind > 0 {
		completionContext := map[string]interface{}{"triggerKind": req.TriggerKind}
		if req.TriggerChar != "" {
			completionContext["triggerCharacter"] = req.TriggerChar
		}
		params["context"] = completionContext
	}
	raw, err := srv.client.request(ctx, "textDocument/completion", params)
	if err != nil {
		code := "rpc"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			code = "timeout"
		}
		s.setCallStatus(req.Language, code, err.Error())
		slog.Warn("LSP completion request failed", "language", req.Language, "err", err)
		return LSPCompletionResponse{Items: []LSPCompletionItem{}}, nil
	}
	wireItems, incomplete, parseErr := parseCompletionItems(raw)
	if parseErr != nil {
		s.setCallStatus(req.Language, "rpc", parseErr.Error())
		slog.Warn("LSP completion response parse failed", "language", req.Language, "err", parseErr)
		return LSPCompletionResponse{Items: []LSPCompletionItem{}}, nil
	}
	s.setCallStatus(req.Language, "ok", "")
	return LSPCompletionResponse{Items: mapCompletionItems(wireItems), IsIncomplete: incomplete}, nil
}

// GetHover returns hover information at the given position as a markdown string.
// Returns an empty string if the server is not running.
func (s *LSPService) GetHover(req LSPCompletionRequest) (string, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return "", err
	}
	if srv == nil {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"position":     map[string]int{"line": req.Line, "character": req.Column},
	}
	raw, err := srv.client.request(ctx, "textDocument/hover", params)
	if err != nil {
		slog.Warn("LSP hover request failed", "language", req.Language, "err", err)
		return "", nil
	}
	return parseHover(raw), nil
}

// GetDiagnostics returns diagnostics for a file. Returns an empty slice if
// the server is not running. Note: LSP servers publish diagnostics via
// notifications; this method returns the most recent set received for the
// file (if any), since the textDocument/publishDiagnostics notification is
// server→client only.
func (s *LSPService) GetDiagnostics(req LSPCompletionRequest) ([]Diagnostic, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []Diagnostic{}, err
	}
	if srv == nil {
		return []Diagnostic{}, nil
	}
	uri := pathToURI(req.FilePath)
	srv.diagsMu.Lock()
	cached := srv.diags[uri]
	srv.diagsMu.Unlock()
	return cloneDiagnostics(cached), nil
}

// GetPullDiagnostics requests an LSP 3.17 document diagnostic report. The
// previous result id is cached per URI so unchanged reports can reuse the last
// full result. Unsupported servers degrade to their latest push diagnostics.
func (s *LSPService) GetPullDiagnostics(req LSPCompletionRequest) ([]Diagnostic, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []Diagnostic{}, err
	}
	if srv == nil {
		return []Diagnostic{}, nil
	}

	uri := pathToURI(req.FilePath)
	if srv.diagnosticProviderKnown && !srv.pullDiagnosticsSupported {
		return srv.cachedDiagnostics(uri), nil
	}

	srv.docMu.Lock()
	documentVersion := srv.docVersions[uri]
	srv.diagsMu.Lock()
	if srv.diagResultIDs == nil {
		srv.diagResultIDs = make(map[string]string)
	}
	if srv.diagEpochs == nil {
		srv.diagEpochs = make(map[string]uint64)
	}
	if srv.diagLatestRequests == nil {
		srv.diagLatestRequests = make(map[string]uint64)
	}
	srv.diagEpochs[uri]++
	srv.diagRequestSeq++
	requestSequence := srv.diagRequestSeq
	srv.diagLatestRequests[uri] = requestSequence
	epochs := make(map[string]uint64, len(srv.diagEpochs))
	for cachedURI, epoch := range srv.diagEpochs {
		epochs[cachedURI] = epoch
	}
	previousResultID := srv.diagResultIDs[uri]
	srv.diagsMu.Unlock()
	srv.docMu.Unlock()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	}
	if previousResultID != "" {
		params["previousResultId"] = previousResultID
	}

	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	raw, err := srv.client.request(ctx, "textDocument/diagnostic", params)
	if err != nil {
		slog.Warn("LSP textDocument/diagnostic failed", "language", req.Language, "err", err)
		return srv.cachedDiagnostics(uri), nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return srv.cachedDiagnostics(uri), nil
	}
	var report lspDocumentDiagnosticReportJSON
	if err := json.Unmarshal(raw, &report); err != nil {
		slog.Warn("LSP textDocument/diagnostic response parse failed", "language", req.Language, "err", err)
		return srv.cachedDiagnostics(uri), nil
	}
	if report.Kind != "" && report.Kind != "full" && report.Kind != "unchanged" {
		slog.Warn("LSP textDocument/diagnostic returned an unknown report kind", "language", req.Language, "kind", report.Kind)
		return srv.cachedDiagnostics(uri), nil
	}
	related := make(map[string]lspDocumentDiagnosticReportJSON, len(report.RelatedDocuments))
	for relatedURI, relatedRaw := range report.RelatedDocuments {
		var relatedReport lspDocumentDiagnosticReportJSON
		if err := json.Unmarshal(relatedRaw, &relatedReport); err != nil {
			slog.Warn("LSP related diagnostic response parse failed", "language", req.Language, "uri", relatedURI, "err", err)
			continue
		}
		if relatedReport.Kind != "" && relatedReport.Kind != "full" && relatedReport.Kind != "unchanged" {
			slog.Warn("LSP related diagnostic response has an unknown report kind", "language", req.Language, "uri", relatedURI, "kind", relatedReport.Kind)
			continue
		}
		related[relatedURI] = relatedReport
	}
	return srv.commitPullDiagnostics(uri, documentVersion, requestSequence, epochs, report, related), nil
}

func (srv *lspServer) cachedDiagnostics(uri string) []Diagnostic {
	srv.diagsMu.Lock()
	cached := cloneDiagnostics(srv.diags[uri])
	srv.diagsMu.Unlock()
	return cached
}

func (srv *lspServer) commitPullDiagnostics(
	uri string,
	documentVersion int,
	requestSequence uint64,
	epochs map[string]uint64,
	report lspDocumentDiagnosticReportJSON,
	related map[string]lspDocumentDiagnosticReportJSON,
) []Diagnostic {
	srv.docMu.Lock()
	srv.diagsMu.Lock()
	defer srv.diagsMu.Unlock()
	defer srv.docMu.Unlock()
	if srv.docVersions[uri] != documentVersion ||
		srv.diagEpochs[uri] != epochs[uri] ||
		srv.diagLatestRequests[uri] != requestSequence {
		return cloneDiagnostics(srv.diags[uri])
	}
	current := srv.commitDocumentDiagnosticReportLocked(uri, report)
	for relatedURI, relatedReport := range related {
		if relatedURI == "" || relatedURI == uri {
			continue
		}
		if srv.diagEpochs[relatedURI] != epochs[relatedURI] || srv.diagLatestRequests[relatedURI] > requestSequence {
			continue
		}
		srv.diagLatestRequests[relatedURI] = requestSequence
		srv.commitDocumentDiagnosticReportLocked(relatedURI, relatedReport)
	}
	return current
}

func (srv *lspServer) commitDocumentDiagnosticReportLocked(uri string, report lspDocumentDiagnosticReportJSON) []Diagnostic {
	if srv.diags == nil {
		srv.diags = make(map[string][]Diagnostic)
	}
	if srv.diagResultIDs == nil {
		srv.diagResultIDs = make(map[string]string)
	}
	if srv.diagLatestRequests == nil {
		srv.diagLatestRequests = make(map[string]uint64)
	}
	if report.Kind == "unchanged" {
		if report.ResultID != "" {
			srv.diagResultIDs[uri] = report.ResultID
		}
		return cloneDiagnostics(srv.diags[uri])
	}
	diagnostics := mapLSPDiagnostics(report.Items)
	srv.diags[uri] = cloneDiagnostics(diagnostics)
	if report.ResultID == "" {
		delete(srv.diagResultIDs, uri)
	} else {
		srv.diagResultIDs[uri] = report.ResultID
	}
	return diagnostics
}

// LSPLocation is a file+range for definition/references (prompt-8 Task 8-F).
type LSPLocation struct {
	FilePath  string `json:"filePath"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"endLine"`
	EndColumn int    `json:"endColumn"`
}

// GetDefinition returns go-to-definition locations (prompt-8 Task 8-F).
func (s *LSPService) GetDefinition(req LSPCompletionRequest) ([]LSPLocation, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPLocation{}, err
	}
	if srv == nil {
		return []LSPLocation{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"position":     map[string]int{"line": req.Line, "character": req.Column},
	}
	raw, err := srv.client.request(ctx, "textDocument/definition", params)
	if err != nil {
		slog.Warn("LSP definition failed", "language", req.Language, "err", err)
		return []LSPLocation{}, nil
	}
	return parseLocations(raw), nil
}

// GetReferences returns find-references locations (prompt-8 Task 8-F).
func (s *LSPService) GetReferences(req LSPCompletionRequest) ([]LSPLocation, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPLocation{}, err
	}
	if srv == nil {
		return []LSPLocation{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"position":     map[string]int{"line": req.Line, "character": req.Column},
		"context":      map[string]bool{"includeDeclaration": true},
	}
	raw, err := srv.client.request(ctx, "textDocument/references", params)
	if err != nil {
		slog.Warn("LSP references failed", "err", err)
		return []LSPLocation{}, nil
	}
	return parseLocations(raw), nil
}

// GetImplementation returns implementation locations (textDocument/implementation).
// Like Go to Definition but jumps to the concrete implementation of an interface method.
func (s *LSPService) GetImplementation(req LSPCompletionRequest) ([]LSPLocation, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPLocation{}, err
	}
	if srv == nil {
		return []LSPLocation{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"position":     map[string]int{"line": req.Line, "character": req.Column},
	}
	raw, err := srv.client.request(ctx, "textDocument/implementation", params)
	if err != nil {
		slog.Warn("LSP implementation failed", "err", err)
		return []LSPLocation{}, nil
	}
	return parseLocations(raw), nil
}

// ============================================================================
// Architecture C (prompt-1.md 491-500): LSP 请求转发 — declaration /
// typeDefinition / documentLink / selectionRange / foldingRange。
// 对应前端 Monaco Provider 注册（registerDeclarationProvider 等）。
// ============================================================================

// GetDeclaration returns declaration locations (textDocument/declaration).
// Like Go to Definition but jumps to the symbol's declaration site.
// Architecture C (prompt-1.md 498).
func (s *LSPService) GetDeclaration(req LSPCompletionRequest) ([]LSPLocation, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPLocation{}, err
	}
	if srv == nil {
		return []LSPLocation{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"position":     map[string]int{"line": req.Line, "character": req.Column},
	}
	raw, err := srv.client.request(ctx, "textDocument/declaration", params)
	if err != nil {
		slog.Warn("LSP declaration failed", "err", err)
		return []LSPLocation{}, nil
	}
	return parseLocations(raw), nil
}

// GetTypeDefinition returns type definition locations (textDocument/typeDefinition).
// Jumps to the type definition of the symbol under the cursor.
// Architecture C (prompt-1.md 497 typeHierarchy 配套导航).
func (s *LSPService) GetTypeDefinition(req LSPCompletionRequest) ([]LSPLocation, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPLocation{}, err
	}
	if srv == nil {
		return []LSPLocation{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"position":     map[string]int{"line": req.Line, "character": req.Column},
	}
	raw, err := srv.client.request(ctx, "textDocument/typeDefinition", params)
	if err != nil {
		slog.Warn("LSP typeDefinition failed", "err", err)
		return []LSPLocation{}, nil
	}
	return parseLocations(raw), nil
}

// ============================================================================
// F-1 (prompt-2.md): Call Hierarchy 与 Type Hierarchy 实现。
// 客户端能力已在 buildLSPClientCapabilities 第 588-598 行声明。
// 此处补齐 6 个请求转发方法 + 类型 + 解析 helper。
// ============================================================================

// LSPCallHierarchyItem 是 Call Hierarchy 的单个符号项（F-1）。
// 镜像 LSP wire 协议 CallHierarchyItem，但 range 用扁平字段方便前端使用。
type LSPCallHierarchyItem struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	Detail         string `json:"detail,omitempty"`
	FilePath       string `json:"filePath"`
	Line           int    `json:"line"`
	Column         int    `json:"column"`
	EndLine        int    `json:"endLine"`
	EndColumn      int    `json:"endColumn"`
	SelectionLine  int    `json:"selectionLine"`
	SelectionCol   int    `json:"selectionColumn"`
	SelectionEndLn int    `json:"selectionEndLine"`
	SelectionEndCo int    `json:"selectionEndColumn"`
	// Data 是 server 透传给后续 incoming/outgoing 请求的不透明字段。
	// 用 json.RawMessage 保留原样回传，避免类型损失。
	Data json.RawMessage `json:"data,omitempty"`
}

// LSPCallHierarchyIncomingCall 表示一个调用 item 的符号及其调用位置（F-1）。
type LSPCallHierarchyIncomingCall struct {
	From       LSPCallHierarchyItem `json:"from"`
	FromRanges []LSPLocation        `json:"fromRanges"`
}

// LSPCallHierarchyOutgoingCall 表示 item 调用的目标符号及其调用位置（F-1）。
type LSPCallHierarchyOutgoingCall struct {
	To         LSPCallHierarchyItem `json:"to"`
	FromRanges []LSPLocation        `json:"fromRanges"`
}

// LSPTypeHierarchyItem 是 Type Hierarchy 的单个类型项（F-1）。
// 字段结构与 LSPCallHierarchyItem 一致，单独定义以保留未来扩展（如 parents）。
type LSPTypeHierarchyItem struct {
	Name           string          `json:"name"`
	Kind           int             `json:"kind"`
	Detail         string          `json:"detail,omitempty"`
	FilePath       string          `json:"filePath"`
	Line           int             `json:"line"`
	Column         int             `json:"column"`
	EndLine        int             `json:"endLine"`
	EndColumn      int             `json:"endColumn"`
	SelectionLine  int             `json:"selectionLine"`
	SelectionCol   int             `json:"selectionColumn"`
	SelectionEndLn int             `json:"selectionEndLine"`
	SelectionEndCo int             `json:"selectionEndColumn"`
	Data           json.RawMessage `json:"data,omitempty"`
}

// PrepareCallHierarchy 准备 Call Hierarchy：返回光标位置的符号项（F-1）。
// 对应 LSP textDocument/prepareCallHierarchy。
func (s *LSPService) PrepareCallHierarchy(req LSPCompletionRequest) ([]LSPCallHierarchyItem, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPCallHierarchyItem{}, err
	}
	if srv == nil {
		return []LSPCallHierarchyItem{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"position":     map[string]int{"line": req.Line, "character": req.Column},
	}
	raw, err := srv.client.request(ctx, "textDocument/prepareCallHierarchy", params)
	if err != nil {
		slog.Warn("LSP prepareCallHierarchy failed", "err", err)
		return []LSPCallHierarchyItem{}, nil
	}
	return parseCallHierarchyItems(raw), nil
}

// CallHierarchyIncomingCalls 返回调用 item 的符号列表（F-1）。
// 对应 LSP callHierarchy/incomingCalls。item 由 PrepareCallHierarchy 返回。
// req 用于定位 server 并同步文档状态（确保 server 已知该文件）。
func (s *LSPService) CallHierarchyIncomingCalls(req LSPCompletionRequest, item LSPCallHierarchyItem) ([]LSPCallHierarchyIncomingCall, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPCallHierarchyIncomingCall{}, err
	}
	if srv == nil {
		return []LSPCallHierarchyIncomingCall{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	params := map[string]interface{}{
		"item": callHierarchyItemToWire(item),
	}
	raw, err := srv.client.request(ctx, "callHierarchy/incomingCalls", params)
	if err != nil {
		slog.Warn("LSP callHierarchy/incomingCalls failed", "err", err)
		return []LSPCallHierarchyIncomingCall{}, nil
	}
	return parseCallHierarchyIncomingCalls(raw), nil
}

// CallHierarchyOutgoingCalls 返回 item 调用的目标符号列表（F-1）。
// 对应 LSP callHierarchy/outgoingCalls。
func (s *LSPService) CallHierarchyOutgoingCalls(req LSPCompletionRequest, item LSPCallHierarchyItem) ([]LSPCallHierarchyOutgoingCall, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPCallHierarchyOutgoingCall{}, err
	}
	if srv == nil {
		return []LSPCallHierarchyOutgoingCall{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	params := map[string]interface{}{
		"item": callHierarchyItemToWire(item),
	}
	raw, err := srv.client.request(ctx, "callHierarchy/outgoingCalls", params)
	if err != nil {
		slog.Warn("LSP callHierarchy/outgoingCalls failed", "err", err)
		return []LSPCallHierarchyOutgoingCall{}, nil
	}
	return parseCallHierarchyOutgoingCalls(raw, item.FilePath), nil
}

// GetCallHierarchyIncoming/Outgoing are the prompt-1 API names. The original
// CallHierarchy* methods remain for existing Wails bindings.
func (s *LSPService) GetCallHierarchyIncoming(req LSPCompletionRequest, item LSPCallHierarchyItem) ([]LSPCallHierarchyIncomingCall, error) {
	return s.CallHierarchyIncomingCalls(req, item)
}

func (s *LSPService) GetCallHierarchyOutgoing(req LSPCompletionRequest, item LSPCallHierarchyItem) ([]LSPCallHierarchyOutgoingCall, error) {
	return s.CallHierarchyOutgoingCalls(req, item)
}

// PrepareTypeHierarchy 准备 Type Hierarchy：返回光标位置的类型项（F-1）。
// 对应 LSP textDocument/prepareTypeHierarchy。
func (s *LSPService) PrepareTypeHierarchy(req LSPCompletionRequest) ([]LSPTypeHierarchyItem, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPTypeHierarchyItem{}, err
	}
	if srv == nil {
		return []LSPTypeHierarchyItem{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"position":     map[string]int{"line": req.Line, "character": req.Column},
	}
	raw, err := srv.client.request(ctx, "textDocument/prepareTypeHierarchy", params)
	if err != nil {
		slog.Warn("LSP prepareTypeHierarchy failed", "err", err)
		return []LSPTypeHierarchyItem{}, nil
	}
	return parseTypeHierarchyItems(raw), nil
}

// TypeHierarchySupertypes 返回 item 的父类型列表（F-1）。
// 对应 LSP typeHierarchy/supertypes。
func (s *LSPService) TypeHierarchySupertypes(req LSPCompletionRequest, item LSPTypeHierarchyItem) ([]LSPTypeHierarchyItem, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPTypeHierarchyItem{}, err
	}
	if srv == nil {
		return []LSPTypeHierarchyItem{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"item": typeHierarchyItemToWire(item),
	}
	raw, err := srv.client.request(ctx, "typeHierarchy/supertypes", params)
	if err != nil {
		slog.Warn("LSP typeHierarchy/supertypes failed", "err", err)
		return []LSPTypeHierarchyItem{}, nil
	}
	return parseTypeHierarchyItems(raw), nil
}

// TypeHierarchySubtypes 返回 item 的子类型列表（F-1）。
// 对应 LSP typeHierarchy/subtypes。
func (s *LSPService) TypeHierarchySubtypes(req LSPCompletionRequest, item LSPTypeHierarchyItem) ([]LSPTypeHierarchyItem, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPTypeHierarchyItem{}, err
	}
	if srv == nil {
		return []LSPTypeHierarchyItem{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"item": typeHierarchyItemToWire(item),
	}
	raw, err := srv.client.request(ctx, "typeHierarchy/subtypes", params)
	if err != nil {
		slog.Warn("LSP typeHierarchy/subtypes failed", "err", err)
		return []LSPTypeHierarchyItem{}, nil
	}
	return parseTypeHierarchyItems(raw), nil
}

// callHierarchyItemToWire 把扁平的 LSPCallHierarchyItem 转回 LSP wire 协议格式。
// 用于 incoming/outgoing 请求的 item 参数。
func callHierarchyItemToWire(item LSPCallHierarchyItem) map[string]interface{} {
	out := map[string]interface{}{
		"name": item.Name,
		"kind": item.Kind,
		"uri":  pathToURI(item.FilePath),
		"range": map[string]interface{}{
			"start": map[string]int{"line": item.Line, "character": item.Column},
			"end":   map[string]int{"line": item.EndLine, "character": item.EndColumn},
		},
		"selectionRange": map[string]interface{}{
			"start": map[string]int{"line": item.SelectionLine, "character": item.SelectionCol},
			"end":   map[string]int{"line": item.SelectionEndLn, "character": item.SelectionEndCo},
		},
	}
	if item.Detail != "" {
		out["detail"] = item.Detail
	}
	if len(item.Data) > 0 {
		var data interface{}
		if json.Unmarshal(item.Data, &data) == nil {
			out["data"] = data
		}
	}
	return out
}

// typeHierarchyItemToWire 把扁平的 LSPTypeHierarchyItem 转回 LSP wire 协议格式。
func typeHierarchyItemToWire(item LSPTypeHierarchyItem) map[string]interface{} {
	out := map[string]interface{}{
		"name": item.Name,
		"kind": item.Kind,
		"uri":  pathToURI(item.FilePath),
		"range": map[string]interface{}{
			"start": map[string]int{"line": item.Line, "character": item.Column},
			"end":   map[string]int{"line": item.EndLine, "character": item.EndColumn},
		},
		"selectionRange": map[string]interface{}{
			"start": map[string]int{"line": item.SelectionLine, "character": item.SelectionCol},
			"end":   map[string]int{"line": item.SelectionEndLn, "character": item.SelectionEndCo},
		},
	}
	if item.Detail != "" {
		out["detail"] = item.Detail
	}
	if len(item.Data) > 0 {
		var data interface{}
		if json.Unmarshal(item.Data, &data) == nil {
			out["data"] = data
		}
	}
	return out
}

// parseCallHierarchyItems 解析 prepareCallHierarchy 响应为扁平 item 列表（F-1）。
func parseCallHierarchyItems(raw json.RawMessage) []LSPCallHierarchyItem {
	if len(raw) == 0 || string(raw) == "null" {
		return []LSPCallHierarchyItem{}
	}
	type wireItem struct {
		Name   string          `json:"name"`
		Kind   int             `json:"kind"`
		Detail string          `json:"detail"`
		URI    string          `json:"uri"`
		Range  lspRangeJSON    `json:"range"`
		Sel    lspRangeJSON    `json:"selectionRange"`
		Data   json.RawMessage `json:"data"`
	}
	var items []wireItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return []LSPCallHierarchyItem{}
	}
	out := make([]LSPCallHierarchyItem, 0, len(items))
	for _, it := range items {
		if it.URI == "" {
			continue
		}
		out = append(out, LSPCallHierarchyItem{
			Name:           it.Name,
			Kind:           it.Kind,
			Detail:         it.Detail,
			FilePath:       uriToPath(it.URI),
			Line:           it.Range.Start.Line,
			Column:         it.Range.Start.Character,
			EndLine:        it.Range.End.Line,
			EndColumn:      it.Range.End.Character,
			SelectionLine:  it.Sel.Start.Line,
			SelectionCol:   it.Sel.Start.Character,
			SelectionEndLn: it.Sel.End.Line,
			SelectionEndCo: it.Sel.End.Character,
			Data:           it.Data,
		})
	}
	return out
}

// parseCallHierarchyIncomingCalls 解析 callHierarchy/incomingCalls 响应（F-1）。
func parseCallHierarchyIncomingCalls(raw json.RawMessage) []LSPCallHierarchyIncomingCall {
	if len(raw) == 0 || string(raw) == "null" {
		return []LSPCallHierarchyIncomingCall{}
	}
	type wireCall struct {
		From struct {
			Name   string          `json:"name"`
			Kind   int             `json:"kind"`
			Detail string          `json:"detail"`
			URI    string          `json:"uri"`
			Range  lspRangeJSON    `json:"range"`
			Sel    lspRangeJSON    `json:"selectionRange"`
			Data   json.RawMessage `json:"data"`
		} `json:"from"`
		FromRanges []lspRangeJSON `json:"fromRanges"`
	}
	var calls []wireCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return []LSPCallHierarchyIncomingCall{}
	}
	out := make([]LSPCallHierarchyIncomingCall, 0, len(calls))
	for _, c := range calls {
		if c.From.URI == "" {
			continue
		}
		ranges := make([]LSPLocation, 0, len(c.FromRanges))
		for _, r := range c.FromRanges {
			ranges = append(ranges, LSPLocation{
				FilePath:  uriToPath(c.From.URI),
				Line:      r.Start.Line,
				Column:    r.Start.Character,
				EndLine:   r.End.Line,
				EndColumn: r.End.Character,
			})
		}
		out = append(out, LSPCallHierarchyIncomingCall{
			From: LSPCallHierarchyItem{
				Name:           c.From.Name,
				Kind:           c.From.Kind,
				Detail:         c.From.Detail,
				FilePath:       uriToPath(c.From.URI),
				Line:           c.From.Range.Start.Line,
				Column:         c.From.Range.Start.Character,
				EndLine:        c.From.Range.End.Line,
				EndColumn:      c.From.Range.End.Character,
				SelectionLine:  c.From.Sel.Start.Line,
				SelectionCol:   c.From.Sel.Start.Character,
				SelectionEndLn: c.From.Sel.End.Line,
				SelectionEndCo: c.From.Sel.End.Character,
				Data:           c.From.Data,
			},
			FromRanges: ranges,
		})
	}
	return out
}

// parseCallHierarchyOutgoingCalls 解析 callHierarchy/outgoingCalls 响应（F-1）。
func parseCallHierarchyOutgoingCalls(raw json.RawMessage, sourceFilePath string) []LSPCallHierarchyOutgoingCall {
	if len(raw) == 0 || string(raw) == "null" {
		return []LSPCallHierarchyOutgoingCall{}
	}
	type wireCall struct {
		To struct {
			Name   string          `json:"name"`
			Kind   int             `json:"kind"`
			Detail string          `json:"detail"`
			URI    string          `json:"uri"`
			Range  lspRangeJSON    `json:"range"`
			Sel    lspRangeJSON    `json:"selectionRange"`
			Data   json.RawMessage `json:"data"`
		} `json:"to"`
		FromRanges []lspRangeJSON `json:"fromRanges"`
	}
	var calls []wireCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return []LSPCallHierarchyOutgoingCall{}
	}
	out := make([]LSPCallHierarchyOutgoingCall, 0, len(calls))
	for _, c := range calls {
		if c.To.URI == "" {
			continue
		}
		ranges := make([]LSPLocation, 0, len(c.FromRanges))
		for _, r := range c.FromRanges {
			ranges = append(ranges, LSPLocation{
				FilePath:  sourceFilePath,
				Line:      r.Start.Line,
				Column:    r.Start.Character,
				EndLine:   r.End.Line,
				EndColumn: r.End.Character,
			})
		}
		out = append(out, LSPCallHierarchyOutgoingCall{
			To: LSPCallHierarchyItem{
				Name:           c.To.Name,
				Kind:           c.To.Kind,
				Detail:         c.To.Detail,
				FilePath:       uriToPath(c.To.URI),
				Line:           c.To.Range.Start.Line,
				Column:         c.To.Range.Start.Character,
				EndLine:        c.To.Range.End.Line,
				EndColumn:      c.To.Range.End.Character,
				SelectionLine:  c.To.Sel.Start.Line,
				SelectionCol:   c.To.Sel.Start.Character,
				SelectionEndLn: c.To.Sel.End.Line,
				SelectionEndCo: c.To.Sel.End.Character,
				Data:           c.To.Data,
			},
			FromRanges: ranges,
		})
	}
	return out
}

// parseTypeHierarchyItems 解析 prepareTypeHierarchy / supertypes / subtypes 响应（F-1）。
func parseTypeHierarchyItems(raw json.RawMessage) []LSPTypeHierarchyItem {
	if len(raw) == 0 || string(raw) == "null" {
		return []LSPTypeHierarchyItem{}
	}
	type wireItem struct {
		Name   string          `json:"name"`
		Kind   int             `json:"kind"`
		Detail string          `json:"detail"`
		URI    string          `json:"uri"`
		Range  lspRangeJSON    `json:"range"`
		Sel    lspRangeJSON    `json:"selectionRange"`
		Data   json.RawMessage `json:"data"`
	}
	var items []wireItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return []LSPTypeHierarchyItem{}
	}
	out := make([]LSPTypeHierarchyItem, 0, len(items))
	for _, it := range items {
		if it.URI == "" {
			continue
		}
		out = append(out, LSPTypeHierarchyItem{
			Name:           it.Name,
			Kind:           it.Kind,
			Detail:         it.Detail,
			FilePath:       uriToPath(it.URI),
			Line:           it.Range.Start.Line,
			Column:         it.Range.Start.Character,
			EndLine:        it.Range.End.Line,
			EndColumn:      it.Range.End.Character,
			SelectionLine:  it.Sel.Start.Line,
			SelectionCol:   it.Sel.Start.Character,
			SelectionEndLn: it.Sel.End.Line,
			SelectionEndCo: it.Sel.End.Character,
			Data:           it.Data,
		})
	}
	return out
}

// LSPDocumentLink is a clickable link in the document (textDocument/documentLink).
// Architecture C (prompt-1.md 498).
type LSPDocumentLink struct {
	StartLine int    `json:"startLine"`
	StartCol  int    `json:"startColumn"`
	EndLine   int    `json:"endLine"`
	EndCol    int    `json:"endColumn"`
	Target    string `json:"target,omitempty"`
	Tooltip   string `json:"tooltip,omitempty"`
}

// GetDocumentLinks returns clickable links in the document (textDocument/documentLink).
// Architecture C (prompt-1.md 498).
func (s *LSPService) GetDocumentLinks(req LSPCompletionRequest) ([]LSPDocumentLink, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPDocumentLink{}, err
	}
	if srv == nil {
		return []LSPDocumentLink{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
	}
	raw, err := srv.client.request(ctx, "textDocument/documentLink", params)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		slog.Warn("LSP documentLink failed", "err", err)
		return []LSPDocumentLink{}, nil
	}
	var links []struct {
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
		Target  string `json:"target"`
		Tooltip string `json:"tooltip"`
	}
	if json.Unmarshal(raw, &links) != nil {
		return []LSPDocumentLink{}, nil
	}
	out := make([]LSPDocumentLink, 0, len(links))
	for _, l := range links {
		out = append(out, LSPDocumentLink{
			StartLine: l.Range.Start.Line,
			StartCol:  l.Range.Start.Character,
			EndLine:   l.Range.End.Line,
			EndCol:    l.Range.End.Character,
			Target:    l.Target,
			Tooltip:   l.Tooltip,
		})
	}
	return out, nil
}

// LSPSelectionRange is a nested selection range (textDocument/selectionRange).
// Used for expand/shrink selection. Architecture C (prompt-1.md 498).
type LSPSelectionRange struct {
	StartLine int                `json:"startLine"`
	StartCol  int                `json:"startColumn"`
	EndLine   int                `json:"endLine"`
	EndCol    int                `json:"endColumn"`
	Parent    *LSPSelectionRange `json:"parent,omitempty"`
}

// GetSelectionRanges returns nested selection ranges for the given positions
// (textDocument/selectionRange). Used for expand/shrink selection.
// Architecture C (prompt-1.md 498).
func (s *LSPService) GetSelectionRanges(req LSPCompletionRequest) ([]LSPSelectionRange, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPSelectionRange{}, err
	}
	if srv == nil {
		return []LSPSelectionRange{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"positions":    []map[string]int{{"line": req.Line, "character": req.Column}},
	}
	raw, err := srv.client.request(ctx, "textDocument/selectionRange", params)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		slog.Warn("LSP selectionRange failed", "err", err)
		return []LSPSelectionRange{}, nil
	}
	return parseSelectionRanges(raw), nil
}

// parseSelectionRanges decodes the nested SelectionRange[] response.
func parseSelectionRanges(raw json.RawMessage) []LSPSelectionRange {
	if len(raw) == 0 || string(raw) == "null" {
		return []LSPSelectionRange{}
	}
	var arr []struct {
		Range  lspRangeJSON     `json:"range"`
		Parent *json.RawMessage `json:"parent"`
	}
	if json.Unmarshal(raw, &arr) != nil {
		return []LSPSelectionRange{}
	}
	out := make([]LSPSelectionRange, 0, len(arr))
	for _, sr := range arr {
		item := LSPSelectionRange{
			StartLine: sr.Range.Start.Line,
			StartCol:  sr.Range.Start.Character,
			EndLine:   sr.Range.End.Line,
			EndCol:    sr.Range.End.Character,
		}
		if sr.Parent != nil && len(*sr.Parent) > 0 && string(*sr.Parent) != "null" {
			parents := parseSelectionRanges(*sr.Parent)
			if len(parents) > 0 {
				item.Parent = &parents[0]
			}
		}
		out = append(out, item)
	}
	return out
}

// LSPFoldingRange is a foldable code region (textDocument/foldingRange).
// Architecture C (prompt-1.md 493).
type LSPFoldingRange struct {
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Kind      string `json:"kind,omitempty"`
}

// GetFoldingRanges returns foldable regions in the document
// (textDocument/foldingRange). Architecture C (prompt-1.md 493).
func (s *LSPService) GetFoldingRanges(req LSPCompletionRequest) ([]LSPFoldingRange, error) {
	srv, err := s.syncDocument(req)
	if err != nil {
		return []LSPFoldingRange{}, err
	}
	if srv == nil {
		return []LSPFoldingRange{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
	}
	raw, err := srv.client.request(ctx, "textDocument/foldingRange", params)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		slog.Warn("LSP foldingRange failed", "err", err)
		return []LSPFoldingRange{}, nil
	}
	var arr []struct {
		StartLine int    `json:"startLine"`
		EndLine   int    `json:"endLine"`
		Kind      string `json:"kind"`
	}
	if json.Unmarshal(raw, &arr) != nil {
		return []LSPFoldingRange{}, nil
	}
	out := make([]LSPFoldingRange, 0, len(arr))
	for _, f := range arr {
		out = append(out, LSPFoldingRange{
			StartLine: f.StartLine,
			EndLine:   f.EndLine,
			Kind:      f.Kind,
		})
	}
	return out, nil
}
