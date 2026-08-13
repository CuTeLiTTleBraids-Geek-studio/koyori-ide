package services

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf16"
)

// CloseDocument sends textDocument/didClose (prompt-8 Task 8-A).
func (s *LSPService) CloseDocument(language, filePath string) error {
	language = lspServerKey(language)
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return errWorkspaceSwitching
	}
	srv, ok := s.servers[language]
	s.mu.Unlock()
	if !ok || srv == nil {
		return nil
	}
	uri := pathToURI(filePath)
	srv.docMu.Lock()
	if pending := srv.pendingChanges[uri]; pending != nil {
		delete(srv.pendingChanges, uri)
		pending.complete(context.Canceled)
	}
	delete(srv.docVersions, uri)
	delete(srv.docHashes, uri)
	delete(srv.docLastContent, uri)
	delete(srv.docLastSync, uri)
	srv.docMu.Unlock()
	srv.clearSemanticTokenResults(uri)
	srv.diagsMu.Lock()
	if srv.diagEpochs == nil {
		srv.diagEpochs = make(map[string]uint64)
	}
	delete(srv.diags, uri)
	delete(srv.diagResultIDs, uri)
	delete(srv.diagLatestRequests, uri)
	srv.diagEpochs[uri]++
	srv.diagsMu.Unlock()
	return srv.client.notify("textDocument/didClose", map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	})
}

// DidSaveDocument notifies the server of a disk save (prompt-8 Task 8-A).
func (s *LSPService) DidSaveDocument(req LSPCompletionRequest) error {
	srv, err := s.syncDocument(req)
	if err != nil {
		return err
	}
	if srv == nil {
		return nil
	}
	return srv.client.notify("textDocument/didSave", map[string]interface{}{
		"textDocument": map[string]string{"uri": pathToURI(req.FilePath)},
		"text":         req.Content,
	})
}

// syncDocument ensures the live buffer is known to the server via didOpen or
// a debounced didChange with a monotonic version.
// Returns (nil, nil) if the server is not running.
func (s *LSPService) syncDocument(req LSPCompletionRequest) (*lspServer, error) {
	serverKey := lspServerKey(req.Language)
	if serverKey == "" {
		return nil, nil
	}
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return nil, errWorkspaceSwitching
	}
	srv, ok := s.servers[serverKey]
	s.mu.Unlock()
	if !ok || srv == nil {
		return nil, nil
	}

	uri := pathToURI(req.FilePath)
	langID := lspLanguageID(req.Language, req.FilePath)

	srv.docMu.Lock()
	if srv.docVersions == nil {
		srv.docVersions = make(map[string]int)
	}
	if srv.docHashes == nil {
		srv.docHashes = make(map[string]string)
	}
	if srv.docLastContent == nil {
		srv.docLastContent = make(map[string]string)
	}
	if srv.docLastSync == nil {
		srv.docLastSync = make(map[string]time.Time)
	}
	if srv.pendingChanges == nil {
		srv.pendingChanges = make(map[string]*pendingDocumentChange)
	}
	if srv.closing {
		err := srv.closingErr
		if err == nil {
			err = errLSPServerStopping
		}
		srv.docMu.Unlock()
		return srv, s.workspaceSwitchingError(err)
	}
	if pending := srv.pendingChanges[uri]; pending != nil {
		shouldSchedule := false
		if pending.content != req.Content {
			pending.content = req.Content
			pending.generation++
			shouldSchedule = !pending.opening && !pending.flushing
		}
		generation := pending.generation
		srv.docMu.Unlock()
		if shouldSchedule {
			go srv.flushDocumentChange(uri, pending, generation)
		}
		return srv, s.workspaceSwitchingError(srv.waitDocumentChange(pending))
	}
	_, opened := srv.docVersions[uri]
	if !opened {
		pending := &pendingDocumentChange{
			content:    req.Content,
			generation: 1,
			done:       make(chan struct{}),
			opening:    true,
			flushing:   true,
		}
		srv.pendingChanges[uri] = pending
		params := map[string]interface{}{
			"textDocument": map[string]interface{}{
				"uri":        uri,
				"languageId": langID,
				"version":    1,
				"text":       req.Content,
			},
		}
		srv.docMu.Unlock()
		go srv.sendDocumentOpen(uri, req.Content, params, pending)
		return srv, s.workspaceSwitchingError(srv.waitDocumentChange(pending))
	}
	if srv.docLastContent[uri] == req.Content {
		srv.docMu.Unlock()
		return srv, nil
	}
	// FIX A17: maintain a separate pending state and wait on its barrier. This
	// coalesces rapid edits without allowing completion/hover requests to pass
	// the didChange notification that makes their buffer version visible.
	pending := &pendingDocumentChange{
		content:    req.Content,
		generation: 1,
		done:       make(chan struct{}),
	}
	srv.pendingChanges[uri] = pending
	generation := pending.generation
	srv.docMu.Unlock()
	go srv.flushDocumentChange(uri, pending, generation)
	return srv, s.workspaceSwitchingError(srv.waitDocumentChange(pending))
}

func (s *LSPService) workspaceSwitchingError(err error) error {
	if err == nil {
		return nil
	}
	s.mu.Lock()
	switching := s.switching
	s.mu.Unlock()
	if switching {
		return errWorkspaceSwitching
	}
	return err
}

func (srv *lspServer) flushDocumentChange(uri string, pending *pendingDocumentChange, generation uint64) {
	timer := time.NewTimer(lspDocumentDebounce)
	defer timer.Stop()
	<-timer.C

	srv.docMu.Lock()
	current := srv.pendingChanges[uri]
	if current != pending || current.generation != generation || current.opening || current.flushing {
		srv.docMu.Unlock()
		return
	}
	if srv.closing {
		err := srv.closingErr
		delete(srv.pendingChanges, uri)
		srv.docMu.Unlock()
		current.complete(err)
		return
	}
	oldContent := srv.docLastContent[uri]
	newContent := current.content
	if oldContent == newContent {
		delete(srv.pendingChanges, uri)
		srv.docMu.Unlock()
		current.complete(nil)
		return
	}
	nextVersion := srv.docVersions[uri] + 1
	current.flushing = true
	var changes []map[string]interface{}
	if srv.syncKind == 2 {
		changes = []map[string]interface{}{buildIncrementalChange(oldContent, newContent)}
	} else {
		changes = []map[string]interface{}{{"text": newContent}}
	}
	params := map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri":     uri,
			"version": nextVersion,
		},
		"contentChanges": changes,
	}
	srv.docMu.Unlock()

	err := srv.client.notify("textDocument/didChange", params)
	if err != nil {
		slog.Warn("LSP didChange failed", "uri", uri, "err", err)
	}
	srv.finishDocumentChange(uri, current, generation, newContent, nextVersion, err)
}

func (srv *lspServer) sendDocumentOpen(
	uri string,
	content string,
	params map[string]interface{},
	pending *pendingDocumentChange,
) {
	err := srv.client.notify("textDocument/didOpen", params)
	if err != nil {
		slog.Warn("LSP didOpen failed", "uri", uri, "err", err)
	}
	srv.docMu.Lock()
	current := srv.pendingChanges[uri]
	if current != pending {
		srv.docMu.Unlock()
		return
	}
	if srv.closing {
		closingErr := srv.closingErr
		delete(srv.pendingChanges, uri)
		srv.docMu.Unlock()
		pending.complete(closingErr)
		return
	}
	if err != nil {
		delete(srv.pendingChanges, uri)
		srv.docMu.Unlock()
		pending.complete(err)
		return
	}
	srv.docVersions[uri] = 1
	srv.docLastContent[uri] = content
	srv.docLastSync[uri] = time.Now()
	pending.opening = false
	pending.flushing = false
	if pending.content == content {
		delete(srv.pendingChanges, uri)
		srv.docMu.Unlock()
		pending.complete(nil)
		return
	}
	generation := pending.generation
	srv.docMu.Unlock()
	go srv.flushDocumentChange(uri, pending, generation)
}

func (srv *lspServer) finishDocumentChange(
	uri string,
	pending *pendingDocumentChange,
	generation uint64,
	content string,
	version int,
	notifyErr error,
) {
	srv.docMu.Lock()
	current := srv.pendingChanges[uri]
	if current != pending {
		srv.docMu.Unlock()
		return
	}
	if srv.closing {
		closingErr := srv.closingErr
		delete(srv.pendingChanges, uri)
		srv.docMu.Unlock()
		pending.complete(closingErr)
		return
	}
	if notifyErr != nil {
		delete(srv.pendingChanges, uri)
		srv.docMu.Unlock()
		pending.complete(notifyErr)
		return
	}
	srv.docVersions[uri] = version
	srv.docLastContent[uri] = content
	srv.docLastSync[uri] = time.Now()
	pending.flushing = false
	if pending.generation == generation && pending.content == content {
		delete(srv.pendingChanges, uri)
		srv.docMu.Unlock()
		pending.complete(nil)
		return
	}
	nextGeneration := pending.generation
	srv.docMu.Unlock()
	go srv.flushDocumentChange(uri, pending, nextGeneration)
}

func (srv *lspServer) waitDocumentChange(pending *pendingDocumentChange) error {
	timer := time.NewTimer(lspRequestTimeout + lspDocumentDebounce)
	defer timer.Stop()
	select {
	case <-pending.done:
		return pending.err
	case <-timer.C:
		// The waiter may time out, but the in-flight notification still owns the
		// shared version state. Removing it here would let a successful late write
		// reuse a version and compute the next incremental range from stale text.
		return context.DeadlineExceeded
	}
}

func (srv *lspServer) beginClosing(reason error) {
	if srv == nil {
		return
	}
	if reason == nil {
		reason = errLSPServerStopping
	}
	srv.docMu.Lock()
	srv.closing = true
	srv.closingErr = reason
	for uri, pending := range srv.pendingChanges {
		delete(srv.pendingChanges, uri)
		pending.complete(reason)
	}
	srv.docMu.Unlock()
}

// buildIncrementalChange returns a UTF-16 TextDocumentContentChangeEvent.
func buildIncrementalChange(oldText, newText string) map[string]interface{} {
	if oldText == newText {
		return nil
	}
	oldRunes := []rune(oldText)
	newRunes := []rune(newText)
	prefix := 0
	minLen := len(oldRunes)
	if len(newRunes) < minLen {
		minLen = len(newRunes)
	}
	for prefix < minLen && oldRunes[prefix] == newRunes[prefix] {
		prefix++
	}
	if splitsCRLF(oldRunes, prefix) || splitsCRLF(newRunes, prefix) {
		prefix--
	}
	suffix := 0
	for prefix+suffix < len(oldRunes) && prefix+suffix < len(newRunes) &&
		oldRunes[len(oldRunes)-1-suffix] == newRunes[len(newRunes)-1-suffix] {
		suffix++
	}
	for suffix > 0 {
		oldEnd := len(oldRunes) - suffix
		newEnd := len(newRunes) - suffix
		if !splitsCRLF(oldRunes, oldEnd) && !splitsCRLF(newRunes, newEnd) {
			break
		}
		suffix--
	}
	oldEnd := len(oldRunes) - suffix
	newEnd := len(newRunes) - suffix
	startLine, startCol := runeOffsetToLineCol(oldRunes, prefix)
	endLine, endCol := runeOffsetToLineCol(oldRunes, oldEnd)
	return map[string]interface{}{
		"range": map[string]interface{}{
			"start": map[string]int{"line": startLine, "character": startCol},
			"end":   map[string]int{"line": endLine, "character": endCol},
		},
		"text": string(newRunes[prefix:newEnd]),
	}
}

func splitsCRLF(runes []rune, offset int) bool {
	return offset > 0 && offset < len(runes) && runes[offset-1] == '\r' && runes[offset] == '\n'
}

func runeOffsetToLineCol(runes []rune, offset int) (line, col int) {
	if offset > len(runes) {
		offset = len(runes)
	}
	for index := 0; index < offset; index++ {
		r := runes[index]
		switch r {
		case '\r':
			line++
			col = 0
			if index+1 < offset && runes[index+1] == '\n' {
				index++
			}
		case '\n':
			line++
			col = 0
		default:
			col += utf16.RuneLen(r)
		}
	}
	return line, col
}

// lspLanguageID maps language + path to LSP languageId (tsx/jsx aware).
func lspLanguageID(language, filePath string) string {
	if definition, ok := lspDefinitionForLanguage(language); ok {
		if languageID := languagePackDocumentLanguage(definition, filePath); languageID != "" {
			return languageID
		}
	}
	lower := strings.ToLower(filePath)
	switch lspServerKey(language) {
	case "python":
		return "python"
	case "rust":
		return "rust"
	case "json":
		if strings.HasSuffix(lower, ".jsonc") {
			return "jsonc"
		}
		return "json"
	case "css":
		if strings.HasSuffix(lower, ".scss") {
			return "scss"
		}
		if strings.HasSuffix(lower, ".less") {
			return "less"
		}
		return "css"
	case "html":
		return "html"
	case "vue":
		return "vue"
	case "angular":
		switch {
		case strings.HasSuffix(lower, ".html"):
			return "html"
		case strings.HasSuffix(lower, ".tsx"):
			return "typescriptreact"
		default:
			return "typescript"
		}
	case "yaml":
		return "yaml"
	case "eslint":
		switch {
		case strings.HasSuffix(lower, ".tsx"):
			return "typescriptreact"
		case strings.HasSuffix(lower, ".ts"):
			return "typescript"
		case strings.HasSuffix(lower, ".jsx"):
			return "javascriptreact"
		case strings.HasSuffix(lower, ".vue"):
			return "vue"
		default:
			return "javascript"
		}
	}
	return language
}
