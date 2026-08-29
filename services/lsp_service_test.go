package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func lspTestString(value string) *string {
	return &value
}

func lspTestStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func lspTestStringSliceValue(value *[]string) []string {
	if value == nil {
		return nil
	}
	return *value
}

func lspTestContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestLSPService_DetectAllLSPServers_ReturnsAllLanguages verifies that the
// opt-in extended detector returns a status entry for every supported language
// (go, python, rust, typescript, javascript, json, css, html, yaml, eslint),
// regardless of whether the servers are installed. Available/Running reflect
// the actual environment.
func TestLSPService_DetectAllLSPServers_ReturnsAllLanguages(t *testing.T) {
	svc := NewLSPService("")
	statuses := svc.DetectAllLSPServers()

	wantLanguages := []string{"go", "python", "rust", "typescript", "javascript", "json", "css", "html", "yaml", "eslint"}
	if len(statuses) != len(wantLanguages) {
		t.Fatalf("expected %d status entries, got %d", len(wantLanguages), len(statuses))
	}

	seen := map[string]bool{}
	for _, st := range statuses {
		seen[st.Language] = true
	}
	for _, lang := range wantLanguages {
		if !seen[lang] {
			t.Errorf("missing status for language %q", lang)
		}
	}

	// None should be running since we never started any server.
	for _, st := range statuses {
		if st.Running {
			t.Errorf("language %q should not be Running on a fresh service", st.Language)
		}
	}
}

func TestA10_DetectPythonRustFromWorkspace(t *testing.T) {
	root := t.TempDir()
	svc := NewLSPService(root)
	hasLanguage := func(statuses []LSPServerStatus, language string) bool {
		for _, status := range statuses {
			if status.Language == language {
				return true
			}
		}
		return false
	}

	statuses := svc.DetectLSPServers()
	if hasLanguage(statuses, "python") || hasLanguage(statuses, "rust") {
		t.Fatalf("empty workspace exposed optional languages: %+v", statuses)
	}

	sourceDir := filepath.Join(root, "src", "nested")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "app.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	statuses = svc.DetectLSPServers()
	if !hasLanguage(statuses, "python") || hasLanguage(statuses, "rust") {
		t.Fatalf("Python workspace statuses = %+v", statuses)
	}

	if err := os.WriteFile(filepath.Join(sourceDir, "lib.rs"), []byte("pub fn ok() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	statuses = svc.DetectLSPServers()
	if !hasLanguage(statuses, "python") || !hasLanguage(statuses, "rust") {
		t.Fatalf("Python/Rust workspace statuses = %+v", statuses)
	}
}

// TestLSPService_DetectLSPServers_GoReflectsGoplsAvailability checks that the
// "go" status's Available flag matches whether gopls is on PATH. This test
// skips gracefully if gopls is not installed.
func TestLSPService_DetectLSPServers_GoReflectsGoplsAvailability(t *testing.T) {
	svc := NewLSPService("")
	statuses := svc.DetectLSPServers()

	var goStatus LSPServerStatus
	for _, st := range statuses {
		if st.Language == "go" {
			goStatus = st
		}
	}

	_, err := exec.LookPath("gopls")
	goplsInstalled := err == nil

	if goStatus.Available != goplsInstalled {
		t.Errorf("go Available=%v but goplsInstalled=%v", goStatus.Available, goplsInstalled)
	}
	if goplsInstalled && goStatus.ServerPath == "" {
		t.Errorf("gopls is installed but ServerPath is empty")
	}
}

// TestLSPService_GetCompletions_EmptyWhenNotRunning verifies the graceful
// fallback: GetCompletions returns an empty (non-nil) slice and no error when
// no LSP server is running for the requested language.
func TestLSPService_GetCompletions_EmptyWhenNotRunning(t *testing.T) {
	svc := NewLSPService("")
	items, err := svc.GetCompletions(LSPCompletionRequest{
		Language: "go",
		FilePath: "/tmp/main.go",
		Line:     0,
		Column:   0,
		Content:  "package main\n",
	})
	if err != nil {
		t.Fatalf("expected no error when server not running, got: %v", err)
	}
	if items == nil {
		t.Fatal("expected non-nil items slice when server not running")
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items when server not running, got %d", len(items))
	}
}

// TestLSPService_GetHover_EmptyWhenNotRunning verifies GetHover returns an
// empty string and no error when no server is running.
func TestLSPService_GetHover_EmptyWhenNotRunning(t *testing.T) {
	svc := NewLSPService("")
	hover, err := svc.GetHover(LSPCompletionRequest{
		Language: "typescript",
		FilePath: "/tmp/a.ts",
		Line:     0,
		Column:   0,
		Content:  "const x = 1;\n",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if hover != "" {
		t.Errorf("expected empty hover, got %q", hover)
	}
}

// TestLSPService_GetDiagnostics_EmptyWhenNotRunning verifies GetDiagnostics
// returns an empty slice and no error when no server is running.
func TestLSPService_GetDiagnostics_EmptyWhenNotRunning(t *testing.T) {
	svc := NewLSPService("")
	diags, err := svc.GetDiagnostics(LSPCompletionRequest{
		Language: "javascript",
		FilePath: "/tmp/a.js",
		Line:     0,
		Column:   0,
		Content:  "const x = 1;\n",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if diags == nil {
		t.Fatal("expected non-nil diagnostics slice")
	}
	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics, got %d", len(diags))
	}
}

func TestLSPPublishDiagnosticsPreservesPushCacheAndRefreshes(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	filePath := filepath.Join(t.TempDir(), "main.go")
	uri := pathToURI(filePath)
	srv := &lspServer{
		docVersions:   map[string]int{uri: 2},
		diags:         map[string][]Diagnostic{uri: {{Message: "old"}}},
		diagResultIDs: map[string]string{uri: "pull-1"},
		diagEpochs:    map[string]uint64{uri: 4},
	}
	svc.servers["go"] = srv
	before := svc.GetDiagnosticsRefreshVersion("go")
	svc.handlePublishedDiagnostics("go", srv, json.RawMessage(fmt.Sprintf(`{
		"uri":%q,
		"version":2,
		"diagnostics":[{"range":{"start":{"line":3,"character":1},"end":{"line":3,"character":5}},"severity":1,"message":"push","source":"gopls"}]
	}`, uri)))

	srv.diagsMu.Lock()
	cached := cloneDiagnostics(srv.diags[uri])
	_, hasResultID := srv.diagResultIDs[uri]
	epoch := srv.diagEpochs[uri]
	srv.diagsMu.Unlock()
	if len(cached) != 1 || cached[0].Message != "push" || cached[0].Line != 3 {
		t.Fatalf("publish cache = %+v", cached)
	}
	if hasResultID || epoch != 5 {
		t.Fatalf("publish resultId/epoch = %v/%d", hasResultID, epoch)
	}
	if got := svc.GetDiagnosticsRefreshVersion("go"); got != before+1 {
		t.Fatalf("refresh version = %d, want %d", got, before+1)
	}

	// A versioned publish older than the open document must not replace the
	// current cache or trigger another refresh.
	svc.handlePublishedDiagnostics("go", srv, json.RawMessage(fmt.Sprintf(`{"uri":%q,"version":1,"diagnostics":[]}`, uri)))
	if got := svc.GetDiagnosticsRefreshVersion("go"); got != before+1 {
		t.Fatalf("stale publish advanced refresh version to %d", got)
	}
	if got := srv.cachedDiagnostics(uri); len(got) != 1 || got[0].Message != "push" {
		t.Fatalf("stale publish replaced cache: %+v", got)
	}
}

func TestLSPWorkspaceDiagnosticsRefreshKeepsPushCache(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	uri := pathToURI(filepath.Join(t.TempDir(), "main.go"))
	srv := &lspServer{
		diags:         map[string][]Diagnostic{uri: {{Message: "push"}}},
		diagResultIDs: map[string]string{uri: "pull-1"},
		diagEpochs:    map[string]uint64{uri: 9},
	}
	svc.servers["go"] = srv
	before := svc.GetDiagnosticsRefreshVersion("go")
	if _, err := svc.handleServerRequestForLanguage("go", "workspace/diagnostic/refresh", nil); err != nil {
		t.Fatalf("workspace/diagnostic/refresh: %v", err)
	}
	srv.diagsMu.Lock()
	cached := cloneDiagnostics(srv.diags[uri])
	_, hasResultID := srv.diagResultIDs[uri]
	epoch := srv.diagEpochs[uri]
	srv.diagsMu.Unlock()
	if len(cached) != 1 || cached[0].Message != "push" {
		t.Fatalf("workspace refresh cleared push cache: %+v", cached)
	}
	if hasResultID || epoch != 10 {
		t.Fatalf("workspace refresh resultId/epoch = %v/%d", hasResultID, epoch)
	}
	if got := svc.GetDiagnosticsRefreshVersion("go"); got != before+1 {
		t.Fatalf("refresh version = %d, want %d", got, before+1)
	}
}

func TestLSPPullDiagnosticsReturnsLatestPublishAfterDelayedResponse(t *testing.T) {
	for _, test := range []struct {
		name   string
		result interface{}
	}{
		{
			name: "old full response",
			result: map[string]interface{}{
				"kind":     "full",
				"resultId": "old-pull",
				"items": []map[string]interface{}{{
					"range":    map[string]interface{}{"start": map[string]int{"line": 0, "character": 0}, "end": map[string]int{"line": 0, "character": 1}},
					"severity": 2,
					"message":  "old pull",
				}},
			},
		},
		{name: "null response", result: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := NewLSPService(t.TempDir())
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			srv := addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
				"textDocument/diagnostic": func(map[string]interface{}) interface{} {
					started <- struct{}{}
					<-release
					return test.result
				},
			})
			filePath := filepath.Join(t.TempDir(), "main.go")
			uri := pathToURI(filePath)
			resultCh := make(chan []Diagnostic, 1)
			go func() {
				diagnostics, _ := svc.GetPullDiagnostics(LSPCompletionRequest{
					Language: "go",
					FilePath: filePath,
					Content:  "package main\n",
				})
				resultCh <- diagnostics
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("pull request did not start")
			}
			svc.handlePublishedDiagnostics("go", srv, json.RawMessage(fmt.Sprintf(`{
				"uri":%q,
				"diagnostics":[{"range":{"start":{"line":4,"character":0},"end":{"line":4,"character":3}},"severity":1,"message":"new push"}]
			}`, uri)))
			close(release)
			select {
			case diagnostics := <-resultCh:
				if len(diagnostics) != 1 || diagnostics[0].Message != "new push" {
					t.Fatalf("pull returned %+v, want latest push", diagnostics)
				}
			case <-time.After(time.Second):
				t.Fatal("pull request did not finish")
			}
			srv.diagsMu.Lock()
			_, hasOldResultID := srv.diagResultIDs[uri]
			srv.diagsMu.Unlock()
			if hasOldResultID {
				t.Fatal("stale pull resultId replaced publish state")
			}
		})
	}
}

func TestLSPPullDiagnosticsLatestConcurrentRequestWins(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	firstRequestSeen := make(chan struct{})
	secondResponseSent := make(chan struct{})
	allowFirstResponse := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer serverW.Close()
		reader := bufio.NewReader(serverR)
		var diagnosticIDs []interface{}
		for {
			message, err := readLSPTestWireMessage(reader)
			if err != nil {
				return
			}
			if message["method"] != "textDocument/diagnostic" {
				continue
			}
			diagnosticIDs = append(diagnosticIDs, message["id"])
			if len(diagnosticIDs) == 1 {
				close(firstRequestSeen)
				continue
			}
			if len(diagnosticIDs) != 2 {
				continue
			}
			_ = writeLSPTestWireMessage(serverW, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      diagnosticIDs[1],
				"result": map[string]interface{}{
					"kind":     "full",
					"resultId": "new-pull",
					"items": []map[string]interface{}{{
						"range":   map[string]interface{}{"start": map[string]int{"line": 2, "character": 0}, "end": map[string]int{"line": 2, "character": 1}},
						"message": "new pull",
					}},
				},
			})
			close(secondResponseSent)
			<-allowFirstResponse
			_ = writeLSPTestWireMessage(serverW, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      diagnosticIDs[0],
				"result": map[string]interface{}{
					"kind":     "full",
					"resultId": "old-pull",
					"items": []map[string]interface{}{{
						"range":   map[string]interface{}{"start": map[string]int{"line": 1, "character": 0}, "end": map[string]int{"line": 1, "character": 1}},
						"message": "old pull",
					}},
				},
			})
		}
	}()
	client := newJSONRPCClient(clientR, clientW)
	srv := &lspServer{
		client:         client,
		running:        true,
		docVersions:    make(map[string]int),
		docHashes:      make(map[string]string),
		docLastContent: make(map[string]string),
		docLastSync:    make(map[string]time.Time),
		pendingChanges: make(map[string]*pendingDocumentChange),
		diags:          make(map[string][]Diagnostic),
		diagResultIDs:  make(map[string]string),
		diagEpochs:     make(map[string]uint64),
	}
	svc := NewLSPService(t.TempDir())
	svc.servers["go"] = srv
	t.Cleanup(func() {
		_ = clientW.Close()
		_ = clientR.Close()
		_ = serverW.Close()
		_ = serverR.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
		}
	})

	filePath := filepath.Join(t.TempDir(), "main.go")
	request := LSPCompletionRequest{Language: "go", FilePath: filePath, Content: "package main\n"}
	firstResult := make(chan []Diagnostic, 1)
	secondResult := make(chan []Diagnostic, 1)
	go func() {
		diagnostics, _ := svc.GetPullDiagnostics(request)
		firstResult <- diagnostics
	}()
	select {
	case <-firstRequestSeen:
	case <-time.After(time.Second):
		t.Fatal("first pull request did not start")
	}
	go func() {
		diagnostics, _ := svc.GetPullDiagnostics(request)
		secondResult <- diagnostics
	}()
	select {
	case <-secondResponseSent:
	case <-time.After(time.Second):
		t.Fatal("second pull response was not sent")
	}
	select {
	case diagnostics := <-secondResult:
		if len(diagnostics) != 1 || diagnostics[0].Message != "new pull" {
			t.Fatalf("second pull = %+v", diagnostics)
		}
	case <-time.After(time.Second):
		t.Fatal("second pull did not finish")
	}
	close(allowFirstResponse)
	select {
	case diagnostics := <-firstResult:
		if len(diagnostics) != 1 || diagnostics[0].Message != "new pull" {
			t.Fatalf("stale first pull = %+v, want current cache", diagnostics)
		}
	case <-time.After(time.Second):
		t.Fatal("first pull did not finish")
	}
}

// TestLSPService_StartLSPServer_ErrorWhenNotInstalled verifies that
// StartLSPServer returns an error (not a panic) when the language server is
// not installed. Skips if gopls happens to be installed.
func TestLSPService_StartLSPServer_ErrorWhenNotInstalled(t *testing.T) {
	// Pick a language whose server is guaranteed not installed by using an
	// unsupported language name.
	svc := NewLSPService("")
	err := svc.StartLSPServer("cobol")
	if err == nil {
		t.Fatal("expected error for unsupported language, got nil")
	}
}

// TestLSPService_StopLSPServer_NoopWhenNotRunning verifies StopLSPServer is a
// no-op (returns nil) when no server is running.
func TestLSPService_StopLSPServer_NoopWhenNotRunning(t *testing.T) {
	svc := NewLSPService("")
	if err := svc.StopLSPServer("go"); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	// Stopping again should still be a no-op.
	if err := svc.StopLSPServer("go"); err != nil {
		t.Errorf("expected nil error on second stop, got: %v", err)
	}
}

// TestLSPService_SetWorkspaceRoot_NoopOnSameRoot verifies that setting the
// same workspace root does nothing (and doesn't panic on empty servers map).
func TestLSPService_SetWorkspaceRoot_NoopOnSameRoot(t *testing.T) {
	svc := NewLSPService("/tmp")
	svc.setWorkspaceRoot("/tmp")   // same root — should be a no-op
	svc.setWorkspaceRoot("/other") // different root — stops nothing (no servers)
}

// TestLSP_pathToURI verifies file:// URI construction (prompt-8 Task 8-C).
func TestLSP_pathToURI(t *testing.T) {
	if got := pathToURI(""); got != "" {
		t.Errorf("empty → %q", got)
	}
	// POSIX absolute (preserved even on Windows).
	if got := pathToURI("/home/user/main.go"); got != "file:///home/user/main.go" {
		t.Errorf("posix abs: got %q", got)
	}
	// Windows drive path — only meaningful on Windows where the OS treats backslash
	// as a separator and the path is absolute.
	if runtime.GOOS == "windows" {
		if got := pathToURI(`C:\Users\main.go`); got != "file:///C:/Users/main.go" {
			t.Errorf("windows drive: got %q", got)
		}
	}
	// Absolute path round-trip via Abs for a real temp file path.
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	got := pathToURI(p)
	if !strings.HasPrefix(got, "file://") {
		t.Fatalf("want file:// prefix, got %q", got)
	}
	if !strings.Contains(got, "main.go") {
		t.Errorf("uri missing basename: %q", got)
	}
}

type capturedLSPRequest struct {
	Method string
	Params map[string]interface{}
}

// newTestJSONRPCClient connects the production client to an in-memory LSP
// peer. Requests are captured after JSON encoding so assertions cover the
// actual wire shape rather than an internal map before serialization.
func newTestJSONRPCClient(
	t *testing.T,
	handler func(method string, params map[string]interface{}) interface{},
) (*jsonRPCClient, <-chan capturedLSPRequest) {
	t.Helper()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	requests := make(chan capturedLSPRequest, 16)

	t.Cleanup(func() {
		_ = clientW.Close()
		_ = clientR.Close()
		_ = serverW.Close()
		_ = serverR.Close()
	})

	go func() {
		defer close(requests)
		reader := bufio.NewReader(serverR)
		for {
			var contentLength int
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				if line == "" {
					break
				}
				if strings.HasPrefix(line, "Content-Length:") {
					contentLength, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
				}
			}
			if contentLength <= 0 {
				return
			}
			body := make([]byte, contentLength)
			if _, err := io.ReadFull(reader, body); err != nil {
				return
			}

			var message map[string]interface{}
			if err := json.Unmarshal(body, &message); err != nil {
				continue
			}
			method, _ := message["method"].(string)
			id, isRequest := message["id"]
			if !isRequest || method == "" {
				continue
			}
			params, _ := message["params"].(map[string]interface{})
			requests <- capturedLSPRequest{Method: method, Params: params}

			var result interface{} = map[string]interface{}{}
			if handler != nil {
				result = handler(method, params)
			}
			response, err := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  result,
			})
			if err != nil {
				return
			}
			header := "Content-Length: " + strconv.Itoa(len(response)) + "\r\n\r\n"
			if _, err := serverW.Write([]byte(header)); err != nil {
				return
			}
			if _, err := serverW.Write(response); err != nil {
				return
			}
		}
	}()

	return newJSONRPCClient(clientR, clientW), requests
}

func newCompletionTestService(
	t *testing.T,
	completionResult interface{},
) (*LSPService, <-chan capturedLSPRequest) {
	t.Helper()
	client, requests := newTestJSONRPCClient(t, func(method string, _ map[string]interface{}) interface{} {
		if method == "textDocument/completion" {
			return completionResult
		}
		return map[string]interface{}{}
	})
	svc := NewLSPService(t.TempDir())
	svc.servers["go"] = &lspServer{client: client}
	return svc, requests
}

func awaitLSPRequest(t *testing.T, requests <-chan capturedLSPRequest) capturedLSPRequest {
	t.Helper()
	select {
	case request, ok := <-requests:
		if !ok {
			t.Fatal("mock LSP connection closed before a request was captured")
		}
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for mock LSP request")
		return capturedLSPRequest{}
	}
}

func TestLSP_A1_CompletionContextWireShape(t *testing.T) {
	tests := []struct {
		name            string
		triggerKind     int
		triggerChar     string
		wantContext     bool
		wantTriggerChar bool
	}{
		{name: "trigger character", triggerKind: 2, triggerChar: ".", wantContext: true, wantTriggerChar: true},
		{name: "invoked", triggerKind: 1, wantContext: true},
		{name: "zero omits context", triggerKind: 0, triggerChar: "."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, requests := newCompletionTestService(t, map[string]interface{}{
				"isIncomplete": false,
				"items":        []interface{}{},
			})
			_, err := svc.GetCompletionList(LSPCompletionRequest{
				Language:    "go",
				FilePath:    filepath.Join(t.TempDir(), "main.go"),
				Content:     "package main\n",
				Line:        0,
				Column:      4,
				TriggerKind: tc.triggerKind,
				TriggerChar: tc.triggerChar,
			})
			if err != nil {
				t.Fatalf("GetCompletionList: %v", err)
			}

			request := awaitLSPRequest(t, requests)
			if request.Method != "textDocument/completion" {
				t.Fatalf("method = %q, want textDocument/completion", request.Method)
			}
			contextValue, hasContext := request.Params["context"]
			if hasContext != tc.wantContext {
				t.Fatalf("context present = %v, want %v; params=%+v", hasContext, tc.wantContext, request.Params)
			}
			if !tc.wantContext {
				return
			}
			contextMap, ok := contextValue.(map[string]interface{})
			if !ok {
				t.Fatalf("context = %T, want map", contextValue)
			}
			triggerKind, ok := contextMap["triggerKind"].(float64)
			if !ok {
				t.Fatalf("context.triggerKind = %v (%T), want number", contextMap["triggerKind"], contextMap["triggerKind"])
			}
			if got := int(triggerKind); got != tc.triggerKind {
				t.Errorf("context.triggerKind = %d, want %d", got, tc.triggerKind)
			}
			gotChar, hasTriggerChar := contextMap["triggerCharacter"]
			if hasTriggerChar != tc.wantTriggerChar {
				t.Fatalf("triggerCharacter present = %v, want %v; context=%+v", hasTriggerChar, tc.wantTriggerChar, contextMap)
			}
			if tc.wantTriggerChar && gotChar != tc.triggerChar {
				t.Errorf("context.triggerCharacter = %v, want %q", gotChar, tc.triggerChar)
			}
		})
	}
}

func TestLSP_A2_GetCompletionListPreservesIncompleteAndMetadata(t *testing.T) {
	var result interface{}
	if err := json.Unmarshal([]byte(`{
		"isIncomplete": true,
		"items": [{
			"label": "Println",
			"kind": 3,
			"detail": "func(a ...any)",
			"insertText": "Println(${1:a})",
			"insertTextFormat": 2,
			"insertTextMode": 2,
			"sortText": "0001",
			"filterText": "Println",
			"preselect": true,
			"deprecated": true,
			"tags": [1],
			"documentation": {"kind": "markdown", "value": "Println writes output."},
			"data": {"token": 42, "source": "gopls"},
			"commitCharacters": ["(", "."],
			"textEdit": {
				"range": {"start": {"line": 4, "character": 2}, "end": {"line": 4, "character": 5}},
				"newText": "Println"
			},
			"labelDetails": {"detail": "(a ...any)", "description": "fmt"}
		}]
	}`), &result); err != nil {
		t.Fatalf("prepare completion result: %v", err)
	}

	svc, _ := newCompletionTestService(t, result)
	response, err := svc.GetCompletionList(LSPCompletionRequest{
		Language: "go",
		FilePath: filepath.Join(t.TempDir(), "main.go"),
		Content:  "package main\n",
	})
	if err != nil {
		t.Fatalf("GetCompletionList: %v", err)
	}
	if !response.IsIncomplete {
		t.Fatal("IsIncomplete = false, want true")
	}
	if len(response.Items) != 1 {
		t.Fatalf("item count = %d, want 1", len(response.Items))
	}
	item := response.Items[0]
	if item.Label != "Println" || item.Kind != 3 || item.Detail != "func(a ...any)" {
		t.Errorf("basic metadata was not preserved: %+v", item)
	}
	if lspTestStringValue(item.InsertText) != "Println(${1:a})" || item.InsertTextFormat != 2 || item.InsertTextMode == nil || *item.InsertTextMode != 2 {
		t.Errorf("insert metadata was not preserved: %+v", item)
	}
	if lspTestStringValue(item.SortText) != "0001" || lspTestStringValue(item.FilterText) != "Println" || !item.Preselect || !item.Deprecated {
		t.Errorf("sorting metadata was not preserved: %+v", item)
	}
	if !reflect.DeepEqual(item.Tags, []int{1}) || !reflect.DeepEqual(lspTestStringSliceValue(item.CommitCharacters), []string{"(", "."}) {
		t.Errorf("tags/commit characters = %v/%v", item.Tags, item.CommitCharacters)
	}
	documentation, ok := item.Documentation.(map[string]interface{})
	if !ok || documentation["kind"] != "markdown" || documentation["value"] != "Println writes output." {
		t.Errorf("documentation = %#v", item.Documentation)
	}
	if item.TextEdit == nil || item.TextEdit.StartLine != 4 || item.TextEdit.StartCol != 2 || item.TextEdit.NewText != "Println" {
		t.Errorf("textEdit = %+v", item.TextEdit)
	}
	if item.LabelDetails == nil || item.LabelDetails.Detail != "(a ...any)" || item.LabelDetails.Description != "fmt" {
		t.Errorf("labelDetails = %+v", item.LabelDetails)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(item.Data, &data); err != nil {
		t.Fatalf("unmarshal completion data: %v", err)
	}
	if data["token"] != float64(42) || data["source"] != "gopls" {
		t.Errorf("data = %+v", data)
	}
}

func TestLSP_A3_GoplsInitializationOptions(t *testing.T) {
	want := map[string]interface{}{
		"completion.usePlaceholders":  true,
		"completion.deep":             true,
		"completion.budget":           "100ms",
		"completion.matcher":          "fuzzy",
		"staticcheck":                 true,
		"ui.completion.documentation": true,
		"ui.diagnostic.staticcheck":   true,
		"allExperiments":              true,
	}
	if got := lspInitializationOptions("go", t.TempDir()); !reflect.DeepEqual(got, want) {
		t.Errorf("gopls initializationOptions = %#v, want %#v", got, want)
	}
}

func assertTypeScriptPreferences(t *testing.T, options map[string]interface{}, wantReact bool) {
	t.Helper()
	if options == nil {
		t.Fatal("TypeScript initializationOptions must not be nil")
	}
	if options["hostInfo"] != "koyori-ide" {
		t.Errorf("hostInfo = %v, want koyori-ide", options["hostInfo"])
	}
	preferences, ok := options["preferences"].(map[string]interface{})
	if !ok {
		t.Fatalf("preferences = %T, want map", options["preferences"])
	}
	for _, key := range []string{
		"includeCompletionsForImportStatements",
		"includeCompletionsForModuleExports",
		"includeCompletionsForSnippetText",
		"includeCompletionsWithInsertText",
		"includeCompletionsWithClassMemberSnippets",
		"includeAutomaticOptionalChainCompletions",
		"allowIncompleteCompletions",
	} {
		if value, ok := preferences[key].(bool); !ok || !value {
			t.Errorf("preferences.%s = %v, want true", key, preferences[key])
		}
	}
	jsxStyle, hasJSXStyle := preferences["jsxAttributeCompletionStyle"]
	if wantReact {
		if !hasJSXStyle || jsxStyle != "auto" {
			t.Errorf("jsxAttributeCompletionStyle = %v, want auto", jsxStyle)
		}
	} else if hasJSXStyle {
		t.Errorf("non-React preferences unexpectedly contain jsxAttributeCompletionStyle=%v", jsxStyle)
	}
}

func TestLSP_A4_TypeScriptInitializationOptions(t *testing.T) {
	t.Run("non React workspace", func(t *testing.T) {
		assertTypeScriptPreferences(t, lspInitializationOptions("typescript", t.TempDir()), false)
	})

	t.Run("React workspace", func(t *testing.T) {
		root := t.TempDir()
		manifest := []byte(`{"dependencies":{"react":"^19.0.0"}}`)
		if err := os.WriteFile(filepath.Join(root, "package.json"), manifest, 0o600); err != nil {
			t.Fatalf("write package.json: %v", err)
		}
		assertTypeScriptPreferences(t, lspInitializationOptions("typescriptreact", root), true)
	})
}

func TestLSP_A5_ClientCapabilities(t *testing.T) {
	td := tdTextDocumentCaps(t)

	completion, ok := td["completion"].(map[string]interface{})
	if !ok {
		t.Fatalf("textDocument.completion = %T, want map", td["completion"])
	}
	completionItem, ok := completion["completionItem"].(map[string]interface{})
	if !ok {
		t.Fatalf("completion.completionItem = %T, want map", completion["completionItem"])
	}
	for _, key := range []string{
		"snippetSupport",
		"commitCharactersSupport",
		"preselectSupport",
		"insertReplaceSupport",
		"labelDetailsSupport",
	} {
		if value, ok := completionItem[key].(bool); !ok || !value {
			t.Errorf("completionItem.%s = %v, want true", key, completionItem[key])
		}
	}
	if got, want := completionItem["documentationFormat"], []string{"markdown", "plaintext"}; !reflect.DeepEqual(got, want) {
		t.Errorf("documentationFormat = %#v, want %#v", got, want)
	}
	tagSupport, ok := completionItem["tagSupport"].(map[string]interface{})
	if !ok || !reflect.DeepEqual(tagSupport["valueSet"], []int{1}) {
		t.Errorf("tagSupport = %#v, want valueSet [1]", completionItem["tagSupport"])
	}
	insertTextModes, ok := completionItem["insertTextModeSupport"].(map[string]interface{})
	if !ok || !reflect.DeepEqual(insertTextModes["valueSet"], []int{1, 2}) {
		t.Errorf("insertTextModeSupport = %#v, want valueSet [1 2]", completionItem["insertTextModeSupport"])
	}
	resolveSupport, ok := completionItem["resolveSupport"].(map[string]interface{})
	wantResolveProperties := []string{"documentation", "detail", "additionalTextEdits", "labelDetails"}
	if !ok || !reflect.DeepEqual(resolveSupport["properties"], wantResolveProperties) {
		t.Errorf("completion resolveSupport = %#v, want properties %#v", completionItem["resolveSupport"], wantResolveProperties)
	}
	completionList, ok := completion["completionList"].(map[string]interface{})
	wantItemDefaults := []string{"commitCharacters", "editRange", "insertTextFormat", "insertTextMode"}
	if !ok || !reflect.DeepEqual(completionList["itemDefaults"], wantItemDefaults) {
		t.Errorf("completionList = %#v, want itemDefaults %#v", completion["completionList"], wantItemDefaults)
	}

	semantic, ok := td["semanticTokens"].(map[string]interface{})
	if !ok {
		t.Fatalf("textDocument.semanticTokens = %T, want map", td["semanticTokens"])
	}
	if dynamic, ok := semantic["dynamicRegistration"].(bool); !ok || dynamic {
		t.Errorf("semanticTokens.dynamicRegistration = %v, want false", semantic["dynamicRegistration"])
	}
	wantTokenTypes := []string{
		"namespace", "type", "class", "enum", "interface", "struct", "typeParameter", "parameter",
		"variable", "property", "enumMember", "event", "function", "method", "macro", "keyword",
		"modifier", "comment", "string", "number", "regexp", "operator", "decorator",
	}
	if !reflect.DeepEqual(semantic["tokenTypes"], wantTokenTypes) {
		t.Errorf("semanticTokens.tokenTypes = %#v, want %#v", semantic["tokenTypes"], wantTokenTypes)
	}
	wantTokenModifiers := []string{
		"declaration", "definition", "readonly", "static", "deprecated", "abstract", "async",
		"modification", "documentation", "defaultLibrary",
	}
	if !reflect.DeepEqual(semantic["tokenModifiers"], wantTokenModifiers) {
		t.Errorf("semanticTokens.tokenModifiers = %#v, want %#v", semantic["tokenModifiers"], wantTokenModifiers)
	}
	if !reflect.DeepEqual(semantic["formats"], []string{"relative"}) {
		t.Errorf("semanticTokens.formats = %#v, want [relative]", semantic["formats"])
	}
	requests, ok := semantic["requests"].(map[string]interface{})
	if !ok {
		t.Fatalf("semanticTokens.requests = %T, want map", semantic["requests"])
	}
	full, ok := requests["full"].(map[string]interface{})
	if !ok || full["delta"] != true || requests["range"] != true {
		t.Errorf("semanticTokens.requests = %#v, want range=true and full.delta=true", requests)
	}
	if semantic["multilineTokenSupport"] != true || semantic["overlappingTokenSupport"] != false {
		t.Errorf("semantic token overlap flags = multiline:%v overlapping:%v", semantic["multilineTokenSupport"], semantic["overlappingTokenSupport"])
	}

	inlay, ok := td["inlayHint"].(map[string]interface{})
	if !ok || inlay["dynamicRegistration"] != true {
		t.Fatalf("textDocument.inlayHint = %#v, want dynamicRegistration=true", td["inlayHint"])
	}
	inlayResolve, ok := inlay["resolveSupport"].(map[string]interface{})
	wantInlayProperties := []string{"tooltip", "textEdits", "label.tooltip", "label.location"}
	if !ok || !reflect.DeepEqual(inlayResolve["properties"], wantInlayProperties) {
		t.Errorf("inlayHint.resolveSupport = %#v, want %#v", inlay["resolveSupport"], wantInlayProperties)
	}

	callHierarchy, ok := td["callHierarchy"].(map[string]interface{})
	if !ok || callHierarchy["dynamicRegistration"] != false {
		t.Errorf("callHierarchy = %#v, want dynamicRegistration=false", td["callHierarchy"])
	}
	codeLens, ok := td["codeLens"].(map[string]interface{})
	if !ok || codeLens["dynamicRegistration"] != true {
		t.Fatalf("codeLens = %#v, want dynamicRegistration=true", td["codeLens"])
	}
	codeLensResolve, ok := codeLens["resolveSupport"].(map[string]interface{})
	if !ok || !reflect.DeepEqual(codeLensResolve["properties"], []string{"command"}) {
		t.Errorf("codeLens.resolveSupport = %#v, want properties [command]", codeLens["resolveSupport"])
	}
}

func TestLSP_A6_WorkspaceConfigurationDefaultsAndOverrides(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		svc := NewLSPService(t.TempDir())
		params := json.RawMessage(`{"items":[{"section":"gopls"},{"section":"typescript"},{"section":"javascript"},{"section":"unknown"}]}`)
		result, err := svc.handleServerRequest("workspace/configuration", params)
		if err != nil {
			t.Fatalf("workspace/configuration: %v", err)
		}
		items, ok := result.([]interface{})
		if !ok || len(items) != 4 {
			t.Fatalf("result = %#v, want four configuration entries", result)
		}
		gopls, ok := items[0].(map[string]interface{})
		if !ok || gopls["completion.usePlaceholders"] != true || gopls["completion.deep"] != true || gopls["staticcheck"] != true {
			t.Errorf("default gopls config = %#v", items[0])
		}
		for index, section := range []string{"typescript", "javascript"} {
			preferences, ok := items[index+1].(map[string]interface{})
			if !ok || preferences["includeCompletionsForImportStatements"] != true || preferences["allowIncompleteCompletions"] != true {
				t.Errorf("default %s config = %#v", section, items[index+1])
			}
		}
		if items[3] != nil {
			t.Errorf("unknown section = %#v, want nil", items[3])
		}
	})

	t.Run("explicit overrides win", func(t *testing.T) {
		svc := NewLSPService(t.TempDir())
		goplsOverride := map[string]interface{}{"buildFlags": []string{"-tags=integration"}}
		typescriptOverride := map[string]interface{}{"includeCompletionsForImportStatements": false, "format.enable": false}
		svc.SetLSPConfig("gopls", goplsOverride)
		svc.SetLSPConfig("typescript", typescriptOverride)
		params := json.RawMessage(`{"items":[{"section":"gopls"},{"section":"typescript"}]}`)
		result, err := svc.handleServerRequest("workspace/configuration", params)
		if err != nil {
			t.Fatalf("workspace/configuration: %v", err)
		}
		items, ok := result.([]interface{})
		if !ok || len(items) != 2 {
			t.Fatalf("result = %#v, want two configuration entries", result)
		}
		if !reflect.DeepEqual(items[0], goplsOverride) {
			t.Errorf("gopls override = %#v, want %#v", items[0], goplsOverride)
		}
		if !reflect.DeepEqual(items[1], typescriptOverride) {
			t.Errorf("typescript override = %#v, want %#v", items[1], typescriptOverride)
		}
	})
}

func TestLSP_A7_InitializeCapturesTriggerCharactersDefensively(t *testing.T) {
	client, requests := newTestJSONRPCClient(t, func(method string, _ map[string]interface{}) interface{} {
		if method == "initialize" {
			return map[string]interface{}{
				"capabilities": map[string]interface{}{
					"completionProvider": map[string]interface{}{
						"triggerCharacters": []string{".", ":"},
					},
				},
			}
		}
		return map[string]interface{}{}
	})
	srv := &lspServer{client: client, process: &lspProcess{}}
	svc := NewLSPService(t.TempDir())
	svc.servers["typescript"] = srv
	if err := svc.initializeLocked(srv, "typescript", svc.workspaceRoot, nil); err != nil {
		t.Fatalf("initializeLocked: %v", err)
	}
	request := awaitLSPRequest(t, requests)
	if request.Method != "initialize" {
		t.Fatalf("method = %q, want initialize", request.Method)
	}

	got := svc.GetTriggerCharacters("typescriptreact")
	if !reflect.DeepEqual(got, []string{".", ":"}) {
		t.Fatalf("GetTriggerCharacters = %v, want [. :]", got)
	}
	got[0] = "mutated"
	gotAgain := svc.GetTriggerCharacters("typescript")
	if !reflect.DeepEqual(gotAgain, []string{".", ":"}) {
		t.Errorf("GetTriggerCharacters exposed internal slice: %v", gotAgain)
	}
}

func TestLSP_InitializeCapturesStaticSemanticTokenLegend(t *testing.T) {
	client, requests := newTestJSONRPCClient(t, func(method string, _ map[string]interface{}) interface{} {
		if method != "initialize" {
			return map[string]interface{}{}
		}
		return map[string]interface{}{
			"capabilities": map[string]interface{}{
				"semanticTokensProvider": map[string]interface{}{
					"legend": map[string]interface{}{
						"tokenTypes":     []string{"function", "decorator", "serverOnly"},
						"tokenModifiers": []string{"readonly", "declaration", "serverOnly"},
					},
					"full": map[string]interface{}{"delta": true},
				},
			},
		}
	})
	srv := &lspServer{client: client, process: &lspProcess{}}
	svc := NewLSPService(t.TempDir())
	if err := svc.initializeLocked(srv, "go", svc.workspaceRoot, nil); err != nil {
		t.Fatalf("initializeLocked: %v", err)
	}
	request := awaitLSPRequest(t, requests)
	if request.Method != "initialize" {
		t.Fatalf("method = %q, want initialize", request.Method)
	}
	tokenTypes, tokenModifiers := srv.semanticTokenLegend()
	if !reflect.DeepEqual(tokenTypes, []string{"function", "decorator", "serverOnly"}) {
		t.Fatalf("semantic token types = %v", tokenTypes)
	}
	if !reflect.DeepEqual(tokenModifiers, []string{"readonly", "declaration", "serverOnly"}) {
		t.Fatalf("semantic token modifiers = %v", tokenModifiers)
	}
	tokenTypes[0] = "mutated"
	again, _ := srv.semanticTokenLegend()
	if again[0] != "function" {
		t.Fatalf("semantic token legend exposed internal slice: %v", again)
	}
}

// TestLSP_syncDocument_DidOpenThenDidChange uses an in-process mock LSP server
// (prompt-8 Task 8-E) to assert didOpen on first sync and didChange on second.
func TestLSP_syncDocument_DidOpenThenDidChange(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()

	var mu sync.Mutex
	var methods []string
	var versions []int
	var texts []string

	// Fake LSP server: respond to initialize; record notifications.
	go func() {
		defer serverW.Close()
		r := bufio.NewReader(serverR)
		for {
			// Read Content-Length framed message
			var contentLength int
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				if line == "" {
					break
				}
				if strings.HasPrefix(line, "Content-Length:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
					contentLength, _ = strconv.Atoi(v)
				}
			}
			if contentLength <= 0 {
				return
			}
			body := make([]byte, contentLength)
			if _, err := io.ReadFull(r, body); err != nil {
				return
			}
			var msg map[string]interface{}
			if json.Unmarshal(body, &msg) != nil {
				continue
			}
			method, _ := msg["method"].(string)
			mu.Lock()
			methods = append(methods, method)
			if method == "textDocument/didOpen" || method == "textDocument/didChange" {
				params, _ := msg["params"].(map[string]interface{})
				if method == "textDocument/didOpen" {
					td, _ := params["textDocument"].(map[string]interface{})
					if v, ok := td["version"].(float64); ok {
						versions = append(versions, int(v))
					}
					if txt, ok := td["text"].(string); ok {
						texts = append(texts, txt)
					}
				} else {
					td, _ := params["textDocument"].(map[string]interface{})
					if v, ok := td["version"].(float64); ok {
						versions = append(versions, int(v))
					}
					if ch, ok := params["contentChanges"].([]interface{}); ok && len(ch) > 0 {
						if m, ok := ch[0].(map[string]interface{}); ok {
							if txt, ok := m["text"].(string); ok {
								texts = append(texts, txt)
							}
						}
					}
				}
			}
			mu.Unlock()

			// Respond to requests (initialize / completion).
			if id, ok := msg["id"]; ok && method != "" {
				var resultObj interface{}
				switch method {
				case "initialize":
					_ = json.Unmarshal([]byte(`{"capabilities":{}}`), &resultObj)
				case "textDocument/completion":
					_ = json.Unmarshal([]byte(`{"items":[{"label":"Hello","kind":1,"insertText":"Hello"}]}`), &resultObj)
				default:
					resultObj = map[string]interface{}{}
				}
				resp, _ := json.Marshal(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      id,
					"result":  resultObj,
				})
				header := "Content-Length: " + strconv.Itoa(len(resp)) + "\r\n\r\n"
				_, _ = serverW.Write([]byte(header))
				_, _ = serverW.Write(resp)
			}
		}
	}()

	client := newJSONRPCClient(clientR, clientW)
	srv := &lspServer{
		client:      client,
		docVersions: make(map[string]int),
		docHashes:   make(map[string]string),
		docLastSync: make(map[string]time.Time),
		diags:       make(map[string][]Diagnostic),
	}
	svc := NewLSPService("/tmp/ws")
	svc.mu.Lock()
	svc.servers["go"] = srv
	svc.mu.Unlock()

	// Initialize handshake
	if err := svc.initializeLocked(srv, "go", "/tmp/ws", nil); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	req1 := LSPCompletionRequest{
		Language: "go",
		FilePath: "/tmp/ws/main.go",
		Line:     0,
		Column:   0,
		Content:  "package main\n",
	}
	if _, err := svc.syncDocument(req1); err != nil {
		t.Fatalf("sync1: %v", err)
	}
	req2 := req1
	req2.Content = "package main\nfunc Hello() {}\n"
	if _, err := svc.syncDocument(req2); err != nil {
		t.Fatalf("sync2: %v", err)
	}

	// Allow async reads
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(versions)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	gotMethods := append([]string{}, methods...)
	gotVersions := append([]int{}, versions...)
	gotTexts := append([]string{}, texts...)
	mu.Unlock()
	if len(gotVersions) < 2 {
		t.Fatalf("expected didOpen+didChange versions, got methods=%v versions=%v", gotMethods, gotVersions)
	}
	if gotVersions[0] != 1 || gotVersions[1] != 2 {
		t.Errorf("versions = %v, want [1,2,...]", gotVersions)
	}
	if len(gotTexts) < 2 || gotTexts[1] != req2.Content {
		t.Errorf("didChange text = %q, want updated content", gotTexts)
	}
	// Completion should still work after sync (must not hold mu).
	items, err := svc.GetCompletions(req2)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Label != "Hello" {
		t.Errorf("completions = %+v", items)
	}
	_ = clientW.Close()
	_ = serverR.Close()
}

// TestLSP_parseCompletionItems_ParsesList verifies that parseCompletionItems
// handles a CompletionList-shaped response.
func TestLSP_parseCompletionItems_ParsesList(t *testing.T) {
	raw := json.RawMessage(`{
		"items": [
			{"label": "fmt", "kind": 9, "detail": "package", "insertText": "fmt"},
			{"label": "Println", "kind": 3, "detail": "func()", "insertText": "Println"}
		]
	}`)
	wireItems, incomplete, err := parseCompletionItems(raw)
	if err != nil {
		t.Fatalf("parseCompletionItems: %v", err)
	}
	if incomplete {
		t.Fatal("plain CompletionList without isIncomplete should be complete")
	}
	items := mapCompletionItems(wireItems)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Label != "fmt" || items[0].Kind != 9 {
		t.Errorf("unexpected first item: %+v", items[0])
	}
	if items[1].Label != "Println" || lspTestStringValue(items[1].InsertText) != "Println" {
		t.Errorf("unexpected second item: %+v", items[1])
	}
}

// TestLSP_parseCompletionItems_ParsesArray verifies that parseCompletionItems
// handles a plain JSON array response (some servers return this).
func TestLSP_parseCompletionItems_ParsesArray(t *testing.T) {
	raw := json.RawMessage(`[
		{"label": "foo", "kind": 2, "detail": "var", "insertText": "foo"}
	]`)
	wireItems, incomplete, err := parseCompletionItems(raw)
	if err != nil {
		t.Fatalf("parseCompletionItems: %v", err)
	}
	if incomplete {
		t.Fatal("completion item arrays cannot report isIncomplete")
	}
	items := mapCompletionItems(wireItems)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Label != "foo" {
		t.Errorf("unexpected item label: %q", items[0].Label)
	}
}

// TestLSP_parseCompletionItems_EmptyOnNil verifies that an empty/nil raw
// payload yields an empty (non-nil) slice.
func TestLSP_parseCompletionItems_EmptyOnNil(t *testing.T) {
	items, incomplete, err := parseCompletionItems(nil)
	if err != nil {
		t.Fatalf("parseCompletionItems(nil): %v", err)
	}
	if incomplete {
		t.Fatal("nil completion response should be complete")
	}
	if items == nil || len(items) != 0 {
		t.Errorf("expected empty non-nil slice, got %v (len=%d)", items, len(items))
	}
	items, incomplete, err = parseCompletionItems(json.RawMessage(``))
	if err != nil {
		t.Fatalf("parseCompletionItems(empty): %v", err)
	}
	if incomplete {
		t.Fatal("empty completion response should be complete")
	}
	if items == nil || len(items) != 0 {
		t.Errorf("expected empty non-nil slice for empty input, got %v", items)
	}
}

// TestLSP_parseCompletionItems_AdditionalTextEdits covers auto-import (prompt-10 10-I).
func TestLSP_parseCompletionItems_AdditionalTextEdits(t *testing.T) {
	raw := json.RawMessage(`{
		"items": [{
			"label": "join",
			"kind": 3,
			"detail": "func join",
			"insertText": "join",
			"additionalTextEdits": [{
				"range": {"start": {"line": 0, "character": 0}, "end": {"line": 0, "character": 0}},
				"newText": "import { join } from 'path'\n"
			}]
		}]
	}`)
	wireItems, incomplete, err := parseCompletionItems(raw)
	if err != nil {
		t.Fatalf("parseCompletionItems: %v", err)
	}
	if incomplete {
		t.Fatal("completion list should default isIncomplete to false")
	}
	items := mapCompletionItems(wireItems)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].AdditionalEdits) != 1 {
		t.Fatalf("expected 1 additional edit, got %d", len(items[0].AdditionalEdits))
	}
	if items[0].AdditionalEdits[0].NewText == "" {
		t.Error("expected non-empty newText for auto-import")
	}
	if items[0].AdditionalEdits[0].StartLine != 0 {
		t.Errorf("startLine=%d", items[0].AdditionalEdits[0].StartLine)
	}
}

func TestLSP_parseCompletionItems_PreservesMetadataAndIncomplete(t *testing.T) {
	raw := json.RawMessage(`{
		"isIncomplete": true,
		"items": [{
			"label": "Println",
			"kind": 3,
			"detail": "func(a ...any)",
			"insertText": "Println(${1:a})",
			"insertTextFormat": 2,
			"insertTextMode": 2,
			"sortText": "0001",
			"filterText": "Println",
			"preselect": true,
			"deprecated": true,
			"tags": [1],
			"documentation": {"kind": "markdown", "value": "Println writes output."},
			"data": {"token": 42, "source": "gopls"},
			"commitCharacters": ["(", "."],
			"textEdit": {
				"range": {"start": {"line": 4, "character": 2}, "end": {"line": 4, "character": 5}},
				"newText": "Println"
			},
			"labelDetails": {"detail": "(a ...any)", "description": "fmt"}
		}]
	}`)

	wireItems, incomplete, err := parseCompletionItems(raw)
	if err != nil {
		t.Fatalf("parseCompletionItems: %v", err)
	}
	if !incomplete {
		t.Fatal("isIncomplete = false, want true")
	}
	if len(wireItems) != 1 {
		t.Fatalf("wire item count = %d, want 1", len(wireItems))
	}
	wire := wireItems[0]
	if lspTestStringValue(wire.SortText) != "0001" || lspTestStringValue(wire.FilterText) != "Println" || !wire.Preselect || !wire.Deprecated {
		t.Errorf("wire metadata was not preserved: %+v", wire)
	}
	if wire.InsertTextMode == nil || *wire.InsertTextMode != 2 {
		t.Errorf("wire insertTextMode = %v, want 2", wire.InsertTextMode)
	}
	if !reflect.DeepEqual(wire.Tags, []int{1}) || !reflect.DeepEqual(lspTestStringSliceValue(wire.CommitChars), []string{"(", "."}) {
		t.Errorf("wire tags/commit chars = %v/%v", wire.Tags, wire.CommitChars)
	}
	if wire.TextEdit == nil || wire.TextEdit.NewText != "Println" || wire.TextEdit.Range == nil || wire.TextEdit.Range.Start.Line != 4 {
		t.Errorf("wire textEdit = %+v", wire.TextEdit)
	}
	if wire.LabelDetails == nil || wire.LabelDetails.Description != "fmt" {
		t.Errorf("wire labelDetails = %+v", wire.LabelDetails)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(wire.Data, &data); err != nil {
		t.Fatalf("unmarshal wire data: %v", err)
	}
	if data["token"] != float64(42) || data["source"] != "gopls" {
		t.Errorf("wire data = %+v", data)
	}

	item := mapCompletionItem(wire)
	if lspTestStringValue(item.SortText) != lspTestStringValue(wire.SortText) || lspTestStringValue(item.FilterText) != lspTestStringValue(wire.FilterText) || !item.Preselect || !item.Deprecated {
		t.Errorf("mapped metadata = %+v", item)
	}
	if item.TextEdit == nil || item.TextEdit.StartLine != 4 || item.TextEdit.NewText != "Println" {
		t.Errorf("mapped textEdit = %+v", item.TextEdit)
	}
	if !json.Valid(item.Data) || string(item.Data) != string(wire.Data) {
		t.Errorf("mapped data = %s, wire data = %s", item.Data, wire.Data)
	}
}

func TestLSPCompletion_InsertReplaceEditPreservesWireAndResolveRoundTrip(t *testing.T) {
	raw := json.RawMessage(`[{"label":"Println","textEdit":{"newText":"Println","insert":{"start":{"line":3,"character":2},"end":{"line":3,"character":5}},"replace":{"start":{"line":3,"character":2},"end":{"line":3,"character":9}}}}]`)
	wireItems, _, err := parseCompletionItems(raw)
	if err != nil || len(wireItems) != 1 {
		t.Fatalf("parseCompletionItems = %v, %v", wireItems, err)
	}
	item := mapCompletionItem(wireItems[0])
	if item.TextEdit == nil || item.TextEdit.Insert == nil || item.TextEdit.Replace == nil {
		t.Fatalf("mapped InsertReplaceEdit = %+v", item.TextEdit)
	}
	if item.TextEdit.StartLine != 3 || item.TextEdit.StartCol != 2 || item.TextEdit.EndLine != 3 || item.TextEdit.EndCol != 9 {
		t.Fatalf("legacy flattened replace range = %+v", item.TextEdit)
	}
	if item.TextEdit.Insert.End.Character != 5 || item.TextEdit.Replace.End.Character != 9 {
		t.Fatalf("insert/replace ranges = %+v/%+v", item.TextEdit.Insert, item.TextEdit.Replace)
	}

	roundTrip := completionItemToJSON(item)
	if roundTrip.TextEdit == nil || roundTrip.TextEdit.Insert == nil || roundTrip.TextEdit.Replace == nil || roundTrip.TextEdit.Range != nil {
		t.Fatalf("completionItemToJSON lost InsertReplaceEdit: %+v", roundTrip.TextEdit)
	}

	svc := NewLSPService(t.TempDir())
	paramsCh := make(chan map[string]interface{}, 1)
	addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
		"completionItem/resolve": func(params map[string]interface{}) interface{} {
			paramsCh <- params
			return params
		},
	})
	resolved, err := svc.ResolveCompletionItem("go", item)
	if err != nil {
		t.Fatalf("ResolveCompletionItem: %v", err)
	}
	params := <-paramsCh
	textEdit := lspProtocolTestMap(t, params["textEdit"], "completionItem/resolve.textEdit")
	if _, ok := textEdit["insert"]; !ok {
		t.Fatalf("resolve payload insert = %#v", textEdit)
	}
	if _, ok := textEdit["replace"]; !ok {
		t.Fatalf("resolve payload replace = %#v", textEdit)
	}
	if _, ok := textEdit["range"]; ok {
		t.Fatalf("resolve payload used ordinary range for InsertReplaceEdit: %#v", textEdit)
	}
	if resolved.TextEdit == nil || resolved.TextEdit.Insert == nil || resolved.TextEdit.Replace == nil {
		t.Fatalf("resolved InsertReplaceEdit = %+v", resolved.TextEdit)
	}
}

func TestLSPCompletion_TextEditSupportsOrdinaryAndLegacyRanges(t *testing.T) {
	wireItems, _, err := parseCompletionItems(json.RawMessage(`[{"label":"value","textEdit":{"newText":"value","range":{"start":{"line":2,"character":1},"end":{"line":2,"character":6}}}}]`))
	if err != nil || len(wireItems) != 1 {
		t.Fatalf("parseCompletionItems = %v, %v", wireItems, err)
	}
	item := mapCompletionItem(wireItems[0])
	if item.TextEdit == nil || item.TextEdit.Range == nil || item.TextEdit.Insert != nil || item.TextEdit.Replace != nil {
		t.Fatalf("ordinary text edit = %+v", item.TextEdit)
	}
	if item.TextEdit.StartLine != 2 || item.TextEdit.StartCol != 1 || item.TextEdit.EndCol != 6 {
		t.Fatalf("ordinary flattened range = %+v", item.TextEdit)
	}

	legacy := LSPCompletionItem{
		Label: "legacy",
		TextEdit: &TextEdit{
			StartLine: 4,
			StartCol:  3,
			EndLine:   4,
			EndCol:    8,
			NewText:   "legacy",
		},
	}
	legacyWire := completionItemToJSON(legacy)
	if legacyWire.TextEdit == nil || legacyWire.TextEdit.Range == nil {
		t.Fatalf("legacy flattened edit did not produce an ordinary LSP range: %+v", legacyWire.TextEdit)
	}
	if legacyWire.TextEdit.Range.Start.Line != 4 || legacyWire.TextEdit.Range.End.Character != 8 {
		t.Fatalf("legacy wire range = %+v", legacyWire.TextEdit.Range)
	}
}

func TestLSPCompletion_ItemDefaultsPreserveExplicitEmptyValues(t *testing.T) {
	raw := json.RawMessage(`{
		"itemDefaults": {
			"commitCharacters": ["."],
			"editRange": {
				"insert": {"start":{"line":1,"character":2},"end":{"line":1,"character":4}},
				"replace": {"start":{"line":1,"character":2},"end":{"line":1,"character":8}}
			}
		},
		"items": [
			{"label":"explicit","insertText":"fallback","textEditText":"","sortText":"","filterText":"","commitCharacters":[]},
			{"label":"inherited"},
			{"label":"emptyInsert","insertText":""}
		]
	}`)
	wireItems, _, err := parseCompletionItems(raw)
	if err != nil || len(wireItems) != 3 {
		t.Fatalf("parseCompletionItems = %v, %v", wireItems, err)
	}
	if wireItems[0].CommitChars == nil || len(*wireItems[0].CommitChars) != 0 {
		t.Fatalf("explicit empty commitCharacters inherited defaults: %#v", wireItems[0].CommitChars)
	}
	if wireItems[1].CommitChars == nil || !reflect.DeepEqual(*wireItems[1].CommitChars, []string{"."}) {
		t.Fatalf("missing commitCharacters did not inherit defaults: %#v", wireItems[1].CommitChars)
	}
	for index, wantText := range []string{"", "inherited", ""} {
		edit := wireItems[index].TextEdit
		if edit == nil || edit.Insert == nil || edit.Replace == nil || edit.Range != nil || edit.NewText != wantText {
			t.Fatalf("item %d default edit = %+v, want newText %q", index, edit, wantText)
		}
	}

	item := mapCompletionItem(wireItems[0])
	if item.InsertText == nil || *item.InsertText != "fallback" || item.TextEditText == nil || *item.TextEditText != "" {
		t.Fatalf("insertText/textEditText presence = %+v", item)
	}
	if item.SortText == nil || *item.SortText != "" || item.FilterText == nil || *item.FilterText != "" {
		t.Fatalf("sort/filter empty presence = %+v", item)
	}
	if item.CommitCharacters == nil || len(*item.CommitCharacters) != 0 {
		t.Fatalf("mapped explicit empty commitCharacters = %#v", item.CommitCharacters)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal completion item: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal completion item: %v", err)
	}
	if value, ok := payload["textEditText"]; !ok || value != "" {
		t.Fatalf("textEditText empty presence = %#v", payload)
	}
	if value, ok := payload["sortText"]; !ok || value != "" {
		t.Fatalf("sortText empty presence = %#v", payload)
	}
	if value, ok := payload["filterText"]; !ok || value != "" {
		t.Fatalf("filterText empty presence = %#v", payload)
	}
	if value, ok := payload["commitCharacters"]; !ok || !reflect.DeepEqual(value, []interface{}{}) {
		t.Fatalf("commitCharacters empty presence = %#v", payload)
	}
}

func TestLSP_parseCompletionItems_RejectsMalformedResponse(t *testing.T) {
	items, incomplete, err := parseCompletionItems(json.RawMessage(`{"items": [`))
	if err == nil {
		t.Fatal("expected malformed completion response to return an error")
	}
	if items == nil || len(items) != 0 || incomplete {
		t.Errorf("malformed response fallback = items:%v incomplete:%v", items, incomplete)
	}
}

// TestLSP_parseHover_ParsesMarkupContent verifies hover parsing.
func TestLSP_parseHover_ParsesMarkupContent(t *testing.T) {
	raw := json.RawMessage(`{"contents":{"kind":"markdown","value":"# fmt\n"}}`)
	hover := parseHover(raw)
	if !strings.Contains(hover, "fmt") {
		t.Errorf("expected hover to contain 'fmt', got %q", hover)
	}
}

// TestLSP_parseHover_EmptyOnNil verifies empty hover on nil input.
func TestLSP_parseHover_EmptyOnNil(t *testing.T) {
	if got := parseHover(nil); got != "" {
		t.Errorf("expected empty hover, got %q", got)
	}
}

// TestLSP_jsonRPCClient_writeMessage_FramesWithContentLength verifies that
// writeMessage produces a valid LSP base-protocol frame with a Content-Length
// header followed by the JSON body. No real LSP server is needed — we write
// into a bytes.Buffer and inspect the output.
func TestLSP_jsonRPCClient_writeMessage_FramesWithContentLength(t *testing.T) {
	var buf bytes.Buffer
	// Build a client whose reader is a closed reader so the readLoop exits
	// immediately; we only exercise writeMessage here.
	c := &jsonRPCClient{
		w:       &buf,
		r:       bufio.NewReader(strings.NewReader("")),
		pending: make(map[int64]chan *rpcResponse),
		notifs:  make(map[string][]func(json.RawMessage)),
		done:    make(chan struct{}),
	}
	if err := c.writeMessage(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "test/method",
		"params":  map[string]string{"hello": "world"},
	}); err != nil {
		t.Fatalf("writeMessage failed: %v", err)
	}

	out := buf.String()
	if !strings.HasPrefix(out, "Content-Length:") {
		t.Errorf("expected output to start with Content-Length header, got: %q", out)
	}
	if !strings.Contains(out, "\r\n\r\n") {
		t.Errorf("expected header/body separator, got: %q", out)
	}
	// The body after the separator must be valid JSON with the method.
	idx := strings.Index(out, "\r\n\r\n")
	body := out[idx+4:]
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("body is not valid JSON: %v (body=%q)", err, body)
	}
	if parsed["method"] != "test/method" {
		t.Errorf("expected method 'test/method', got %v", parsed["method"])
	}
}

// TestLSP_jsonRPCClient_readMessage_ParsesFrame verifies the readMessage
// parser can decode a Content-Length-framed message from a reader.
func TestLSP_jsonRPCClient_readMessage_ParsesFrame(t *testing.T) {
	body := `{"jsonrpc":"2.0","method":"foo","params":{}}`
	frame := "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	c := &jsonRPCClient{
		r:       bufio.NewReader(strings.NewReader(frame)),
		pending: make(map[int64]chan *rpcResponse),
		notifs:  make(map[string][]func(json.RawMessage)),
		done:    make(chan struct{}),
	}
	msg, err := c.readMessage()
	if err != nil {
		t.Fatalf("readMessage failed: %v", err)
	}
	if !strings.Contains(string(msg), `"method":"foo"`) {
		t.Errorf("unexpected message body: %s", string(msg))
	}
}

type lspBodyProbeReader struct {
	header    []byte
	offset    int
	bodyReads int
}

func (r *lspBodyProbeReader) Read(p []byte) (int, error) {
	if r.offset < len(r.header) {
		n := copy(p, r.header[r.offset:])
		r.offset += n
		return n, nil
	}
	r.bodyReads++
	return 0, fmt.Errorf("body probe reached")
}

func TestLSP_jsonRPCClient_readMessage_ContentLengthLimit(t *testing.T) {
	const maxMessageSize = 64 * 1024 * 1024

	t.Run("exact limit proceeds to body", func(t *testing.T) {
		probe := &lspBodyProbeReader{header: []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", maxMessageSize))}
		c := &jsonRPCClient{r: bufio.NewReader(probe)}
		_, err := c.readMessage()
		if err == nil || err.Error() != "body probe reached" {
			t.Fatalf("readMessage error = %v, want body probe error", err)
		}
		if probe.bodyReads != 1 {
			t.Fatalf("body reads = %d, want 1", probe.bodyReads)
		}
	})

	t.Run("over limit rejected before body read", func(t *testing.T) {
		probe := &lspBodyProbeReader{header: []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", maxMessageSize+1))}
		c := &jsonRPCClient{r: bufio.NewReader(probe)}
		_, err := c.readMessage()
		if err == nil || !strings.Contains(err.Error(), "exceeds 67108864") {
			t.Fatalf("readMessage error = %v, want size limit error", err)
		}
		if probe.bodyReads != 0 {
			t.Fatalf("body reads = %d, want 0", probe.bodyReads)
		}
	})
}

func TestLSPProcessWaitHelper(t *testing.T) {
	mode := os.Getenv("KOYORI_IDE_LSP_WAIT_HELPER")
	if mode == "" {
		return
	}
	if mode == "exit" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

const lspTestProcessTimeout = 5 * time.Second

func startLSPTestProcess(t *testing.T, mode string) (*exec.Cmd, *lspProcess) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLSPProcessWaitHelper$")
	cmd.Env = append(os.Environ(), "KOYORI_IDE_LSP_WAIT_HELPER="+mode)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	process := newLSPProcess(cmd)
	t.Cleanup(func() {
		_ = process.stop(lspTestProcessTimeout)
	})
	return cmd, process
}

func installLSPTestProcess(svc *LSPService, language string, cmd *exec.Cmd, process *lspProcess) *lspServer {
	srv := &lspServer{
		cmd:     cmd,
		process: process,
		running: true,
		client: &jsonRPCClient{
			w: io.Discard,
		},
	}
	svc.mu.Lock()
	svc.servers[language] = srv
	svc.mu.Unlock()
	go svc.observeLSPProcess(language, srv)
	return srv
}

type blockingLSPWriter struct {
	blocked     chan struct{}
	release     chan struct{}
	blockedOnce sync.Once
	releaseOnce sync.Once
}

type observedBlockingLSPWriter struct {
	writer *blockingLSPWriter
	mu     sync.Mutex
	writes int
	closes int
}

func newObservedBlockingLSPWriter() *observedBlockingLSPWriter {
	return &observedBlockingLSPWriter{writer: newBlockingLSPWriter()}
}

func (w *observedBlockingLSPWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	return w.writer.Write(p)
}

func (w *observedBlockingLSPWriter) Close() error {
	w.mu.Lock()
	w.closes++
	w.mu.Unlock()
	w.writer.unblock()
	return nil
}

func (w *observedBlockingLSPWriter) counts() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes, w.closes
}

type toggledLSPWriter struct {
	mu     sync.Mutex
	err    error
	writes int
}

func (w *toggledLSPWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	if w.err != nil {
		return 0, w.err
	}
	return len(p), nil
}

func (w *toggledLSPWriter) setError(err error) {
	w.mu.Lock()
	w.err = err
	w.mu.Unlock()
}

func (w *toggledLSPWriter) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

func newBlockingLSPWriter() *blockingLSPWriter {
	return &blockingLSPWriter{
		blocked: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *blockingLSPWriter) Write(p []byte) (int, error) {
	w.blockedOnce.Do(func() { close(w.blocked) })
	<-w.release
	return len(p), nil
}

func (w *blockingLSPWriter) unblock() {
	w.releaseOnce.Do(func() { close(w.release) })
}

func TestLSPService_NaturalProcessExitRemovesServer(t *testing.T) {
	svc := NewLSPService("")
	cmd, process := startLSPTestProcess(t, "exit")
	installLSPTestProcess(svc, "go", cmd, process)

	select {
	case <-process.done:
	case <-time.After(lspTestProcessTimeout):
		t.Fatal("natural child exit was not reaped")
	}
	deadline := time.Now().Add(lspTestProcessTimeout)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		_, exists := svc.servers["go"]
		svc.mu.Unlock()
		if !exists {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("naturally exited LSP server remained registered")
}

func TestLSPService_StopUsesSingleWaitOwnerAndIsIdempotent(t *testing.T) {
	svc := NewLSPService("")
	cmd, process := startLSPTestProcess(t, "block")
	_, unrelated := startLSPTestProcess(t, "block")
	installLSPTestProcess(svc, "go", cmd, process)

	if err := svc.StopLSPServer("go"); err != nil {
		t.Fatalf("first StopLSPServer: %v", err)
	}
	select {
	case <-process.done:
	case <-time.After(lspTestProcessTimeout):
		t.Fatal("explicit stop did not await process owner")
	}
	if cmd.ProcessState == nil {
		t.Fatal("explicit stop returned before the process was reaped")
	}
	select {
	case <-unrelated.done:
		t.Fatal("stopping a managed LSP server terminated an unrelated process")
	default:
	}
	if err := svc.StopLSPServer("go"); err != nil {
		t.Fatalf("repeat StopLSPServer: %v", err)
	}
}

func TestWaitForLSPProcessExitPreservesTerminationFailure(t *testing.T) {
	done := make(chan struct{})
	close(done)
	terminationErr := errors.New("termination failed")

	if err := waitForLSPProcessExit(done, time.Second, terminationErr); !errors.Is(err, terminationErr) {
		t.Fatalf("wait error = %v, want termination failure", err)
	}
}

func TestLSPService_StopLSPServer_DoesNotBlockOnClientWriter(t *testing.T) {
	svc := NewLSPService("")
	cmd, process := startLSPTestProcess(t, "block")
	srv := installLSPTestProcess(svc, "go", cmd, process)
	writer := newBlockingLSPWriter()
	writerDeadline := lspProcessStopTimeout + time.Second
	srv.client = &jsonRPCClient{w: writer}
	t.Cleanup(writer.unblock)

	result := make(chan error, 1)
	go func() {
		result <- svc.StopLSPServer("go")
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("StopLSPServer: %v", err)
		}
	case <-time.After(writerDeadline):
		writer.unblock()
		select {
		case <-result:
		case <-time.After(lspTestProcessTimeout):
		}
		t.Fatalf("StopLSPServer blocked on the client writer for more than %v", writerDeadline)
	}

	select {
	case <-process.done:
	case <-time.After(lspTestProcessTimeout):
		t.Fatal("StopLSPServer returned before the process was reaped")
	}
}

func TestLSPService_SetWorkspaceRoot_KillsAndWaitsOldProcess(t *testing.T) {
	cmd, process := startLSPTestProcess(t, "block")

	svc := NewLSPService("old-root")
	installLSPTestProcess(svc, "go", cmd, process)
	svc.setWorkspaceRoot("new-root")
	select {
	case <-process.done:
	case <-time.After(lspTestProcessTimeout):
		t.Fatal("workspace change did not await process owner")
	}
	if cmd.ProcessState == nil {
		t.Fatal("old LSP process was killed but not waited")
	}
}

func TestLSPService_SetWorkspaceRoot_DoesNotBlockOnClientWriter(t *testing.T) {
	cmd, process := startLSPTestProcess(t, "block")
	svc := NewLSPService("old-root")
	srv := installLSPTestProcess(svc, "go", cmd, process)
	writer := newBlockingLSPWriter()
	writerDeadline := lspProcessStopTimeout + time.Second
	srv.client = &jsonRPCClient{w: writer}
	t.Cleanup(writer.unblock)

	returned := make(chan struct{})
	go func() {
		svc.setWorkspaceRoot("new-root")
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(writerDeadline):
		writer.unblock()
		select {
		case <-returned:
		case <-time.After(lspTestProcessTimeout):
		}
		t.Fatalf("SetWorkspaceRoot blocked on the client writer for more than %v", writerDeadline)
	}

	select {
	case <-process.done:
	case <-time.After(lspTestProcessTimeout):
		t.Fatal("SetWorkspaceRoot returned before the process was reaped")
	}
}

// ============================================================================
// G-COMP-02: tests for documentSymbol, workspace/symbol, semanticTokens,
// and ResolveCompletionItem graceful degradation.
// ============================================================================

// TestLSP_parseDocumentSymbols_Hierarchical verifies parsing of the
// hierarchical DocumentSymbol[] shape (modern gopls / typescript-language-server).
func TestLSP_parseDocumentSymbols_Hierarchical(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"name": "main",
			"detail": "func main()",
			"kind": 12,
			"range": {"start": {"line": 5, "character": 0}, "end": {"line": 10, "character": 1}},
			"selectionRange": {"start": {"line": 5, "character": 5}, "end": {"line": 5, "character": 9}},
			"children": [
				{
					"name": "err",
					"kind": 13,
					"range": {"start": {"line": 6, "character": 2}, "end": {"line": 6, "character": 20}},
					"selectionRange": {"start": {"line": 6, "character": 2}, "end": {"line": 6, "character": 5}}
				}
			]
		},
		{
			"name": "Config",
			"detail": "type Config struct",
			"kind": 5,
			"range": {"start": {"line": 0, "character": 0}, "end": {"line": 3, "character": 1}},
			"selectionRange": {"start": {"line": 0, "character": 6}, "end": {"line": 0, "character": 12}}
		}
	]`)
	syms := parseDocumentSymbols(raw)
	if len(syms) != 2 {
		t.Fatalf("expected 2 top-level symbols, got %d", len(syms))
	}
	if syms[0].Name != "main" || syms[0].Kind != 12 {
		t.Errorf("first symbol: name=%q kind=%d", syms[0].Name, syms[0].Kind)
	}
	if syms[0].SelectionRange.Start.Character != 5 {
		t.Errorf("selectionRange start char=%d", syms[0].SelectionRange.Start.Character)
	}
	if len(syms[0].Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(syms[0].Children))
	}
	if syms[0].Children[0].Name != "err" {
		t.Errorf("child name=%q", syms[0].Children[0].Name)
	}
	if syms[1].Name != "Config" {
		t.Errorf("second symbol name=%q", syms[1].Name)
	}
}

// TestLSP_parseDocumentSymbols_FlatSymbolInformation verifies parsing of the
// legacy flat SymbolInformation[] shape (older servers).
func TestLSP_parseDocumentSymbols_FlatSymbolInformation(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"name": "Foo",
			"kind": 12,
			"containerName": "main",
			"location": {
				"uri": "file:///tmp/main.go",
				"range": {"start": {"line": 3, "character": 0}, "end": {"line": 3, "character": 10}}
			}
		}
	]`)
	syms := parseDocumentSymbols(raw)
	if len(syms) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(syms))
	}
	if syms[0].Name != "Foo" || syms[0].Kind != 12 {
		t.Errorf("symbol: name=%q kind=%d", syms[0].Name, syms[0].Kind)
	}
	if syms[0].Range.Start.Line != 3 {
		t.Errorf("range start line=%d", syms[0].Range.Start.Line)
	}
}

// TestLSP_parseDocumentSymbols_EmptyOnNil verifies graceful degradation.
func TestLSP_parseDocumentSymbols_EmptyOnNil(t *testing.T) {
	syms := parseDocumentSymbols(nil)
	if syms == nil || len(syms) != 0 {
		t.Errorf("expected empty non-nil slice for nil input")
	}
	syms = parseDocumentSymbols(json.RawMessage(`null`))
	if syms == nil || len(syms) != 0 {
		t.Errorf("expected empty non-nil slice for null input")
	}
}

// TestLSP_parseSymbolInformation verifies workspace/symbol parsing.
func TestLSP_parseSymbolInformation(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"name": "Println",
			"kind": 12,
			"containerName": "fmt",
			"location": {
				"uri": "file:///go/src/fmt/print.go",
				"range": {"start": {"line": 100, "character": 5}, "end": {"line": 100, "character": 15}}
			}
		},
		{
			"name": "Config",
			"kind": 5,
			"location": {
				"uri": "file:///tmp/config.go",
				"range": {"start": {"line": 0, "character": 6}, "end": {"line": 0, "character": 12}}
			}
		}
	]`)
	syms := parseSymbolInformation(raw)
	if len(syms) != 2 {
		t.Fatalf("expected 2 symbols, got %d", len(syms))
	}
	if syms[0].Name != "Println" || syms[0].ContainerName != "fmt" {
		t.Errorf("first: name=%q container=%q", syms[0].Name, syms[0].ContainerName)
	}
	if syms[0].FilePath == "" || !strings.HasSuffix(syms[0].FilePath, "print.go") {
		t.Errorf("filePath not resolved: %q", syms[0].FilePath)
	}
	if syms[0].Line != 100 || syms[0].Column != 5 {
		t.Errorf("line/col: %d/%d", syms[0].Line, syms[0].Column)
	}
	if syms[1].Name != "Config" {
		t.Errorf("second name=%q", syms[1].Name)
	}
}

// TestLSP_parseSymbolInformation_EmptyOnNil verifies graceful degradation.
func TestLSP_parseSymbolInformation_EmptyOnNil(t *testing.T) {
	syms := parseSymbolInformation(nil)
	if syms == nil || len(syms) != 0 {
		t.Errorf("expected empty non-nil slice for nil input")
	}
}

// TestLSP_parseSemanticTokens verifies delta-encoding decoding.
// Input: 2 tokens on the same line, third token on the next line.
// Token layout (delta-encoded):
//
//	[0, 0, 5, 1, 0]  → line 0 col 0 length 5 type 1 mods 0  ("const")
//	[0, 6, 1, 3, 0]  → line 0 col 6 length 1 type 3 mods 0  ("x")
//	[1, 0, 1, 2, 0]  → line 1 col 0 length 1 type 2 mods 0  ("y")
func TestLSP_parseSemanticTokens(t *testing.T) {
	raw := json.RawMessage(`{"data": [0, 0, 5, 1, 0, 0, 6, 1, 3, 0, 1, 0, 1, 2, 0]}`)
	tokens := parseSemanticTokens(raw)
	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d", len(tokens))
	}
	if tokens[0].Line != 0 || tokens[0].Column != 0 || tokens[0].Length != 5 || tokens[0].Type != 1 {
		t.Errorf("token 0: %+v", tokens[0])
	}
	if tokens[1].Line != 0 || tokens[1].Column != 6 || tokens[1].Length != 1 || tokens[1].Type != 3 {
		t.Errorf("token 1: %+v", tokens[1])
	}
	if tokens[2].Line != 1 || tokens[2].Column != 0 {
		t.Errorf("token 2 (new line): %+v", tokens[2])
	}
}

// TestLSP_parseSemanticTokens_WithModifiers verifies modifier bitmask decoding.
// bitmask 3 = bits 0 and 1 set → modifiers [0, 1].
func TestLSP_parseSemanticTokens_WithModifiers(t *testing.T) {
	raw := json.RawMessage(`{"data": [0, 0, 3, 0, 3]}`)
	tokens := parseSemanticTokens(raw)
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
	if len(tokens[0].Modifiers) != 2 {
		t.Fatalf("expected 2 modifiers, got %d", len(tokens[0].Modifiers))
	}
	if tokens[0].Modifiers[0] != 0 || tokens[0].Modifiers[1] != 1 {
		t.Errorf("modifiers: %v", tokens[0].Modifiers)
	}
}

// TestLSP_parseSemanticTokens_EmptyOnNil verifies graceful degradation.
func TestLSP_parseSemanticTokens_EmptyOnNil(t *testing.T) {
	tokens := parseSemanticTokens(nil)
	if tokens == nil || len(tokens) != 0 {
		t.Errorf("expected empty non-nil slice for nil input")
	}
	tokens = parseSemanticTokens(json.RawMessage(`null`))
	if tokens == nil || len(tokens) != 0 {
		t.Errorf("expected empty non-nil slice for null input")
	}
}

// TestLSP_decodeTokenModifiers verifies bitmask decoding.
func TestLSP_decodeTokenModifiers(t *testing.T) {
	if mods := decodeTokenModifiers(0); mods != nil {
		t.Errorf("expected nil for 0, got %v", mods)
	}
	// bitmask 5 = bits 0 and 2 set.
	mods := decodeTokenModifiers(5)
	if len(mods) != 2 || mods[0] != 0 || mods[1] != 2 {
		t.Errorf("bitmask 5: expected [0, 2], got %v", mods)
	}
}

// TestLSP_GetDocumentSymbols_EmptyWhenNotRunning verifies graceful degradation
// when no LSP server is running.
func TestLSP_GetDocumentSymbols_EmptyWhenNotRunning(t *testing.T) {
	svc := NewLSPService("")
	syms, err := svc.GetDocumentSymbols(LSPCompletionRequest{
		Language: "go",
		FilePath: "/tmp/main.go",
		Content:  "package main\nfunc main() {}\n",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if syms == nil || len(syms) != 0 {
		t.Errorf("expected empty non-nil slice, got %d symbols", len(syms))
	}
}

// TestLSP_GetWorkspaceSymbols_EmptyWhenNotRunning verifies graceful degradation.
func TestLSP_GetWorkspaceSymbols_EmptyWhenNotRunning(t *testing.T) {
	svc := NewLSPService("")
	syms, err := svc.GetWorkspaceSymbols("go", "main")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if syms == nil || len(syms) != 0 {
		t.Errorf("expected empty non-nil slice, got %d symbols", len(syms))
	}
}

// TestLSP_GetSemanticTokens_EmptyWhenNotRunning verifies graceful degradation.
func TestLSP_GetSemanticTokens_EmptyWhenNotRunning(t *testing.T) {
	svc := NewLSPService("")
	tokens, err := svc.GetSemanticTokens(LSPCompletionRequest{
		Language: "typescript",
		FilePath: "/tmp/a.ts",
		Content:  "const x = 1;\n",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tokens == nil || len(tokens) != 0 {
		t.Errorf("expected empty non-nil slice, got %d tokens", len(tokens))
	}
}

// TestLSP_ResolveCompletionItem_ReturnsOriginalWhenNotRunning verifies that
// resolution returns the original item when no server is running.
func TestLSP_ResolveCompletionItem_ReturnsOriginalWhenNotRunning(t *testing.T) {
	svc := NewLSPService("")
	original := LSPCompletionItem{Label: "Foo", Kind: 12, Detail: "func()", InsertText: lspTestString("Foo")}
	resolved, err := svc.ResolveCompletionItem("go", original)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resolved.Label != original.Label || resolved.Kind != original.Kind {
		t.Errorf("expected original item returned unchanged, got %+v", resolved)
	}
}

// G-HL-01: Test parseSignatureHelp with Markdown documentation and
// per-parameter documentation fields.
func TestLSP_parseSignatureHelp_ParsesDocumentation(t *testing.T) {
	raw := json.RawMessage(`{
		"signatures": [{
			"label": "hello(name string, greeting string) string",
			"documentation": {"value": "Greet a person by name.\n\nReturns a greeting string."},
			"parameters": [
				{"label": "name string", "documentation": "The name of the person to greet."},
				{"label": "greeting string", "documentation": {"value": "The greeting word to use."}}
			]
		}],
		"activeSignature": 0,
		"activeParameter": 1
	}`)
	result := parseSignatureHelp(raw)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Label != "hello(name string, greeting string) string" {
		t.Errorf("expected label, got %q", result.Label)
	}
	if result.Documentation != "Greet a person by name.\n\nReturns a greeting string." {
		t.Errorf("expected markdown documentation, got %q", result.Documentation)
	}
	if len(result.Parameters) != 2 {
		t.Fatalf("expected 2 parameters, got %d", len(result.Parameters))
	}
	if result.Parameters[0].Label != "name string" {
		t.Errorf("expected param 0 label, got %q", result.Parameters[0].Label)
	}
	if result.Parameters[0].Documentation != "The name of the person to greet." {
		t.Errorf("expected param 0 doc string, got %q", result.Parameters[0].Documentation)
	}
	if result.Parameters[1].Label != "greeting string" {
		t.Errorf("expected param 1 label, got %q", result.Parameters[1].Label)
	}
	if result.Parameters[1].Documentation != "The greeting word to use." {
		t.Errorf("expected param 1 doc from MarkupContent, got %q", result.Parameters[1].Documentation)
	}
	if result.ActiveParameter != 1 {
		t.Errorf("expected activeParameter=1, got %d", result.ActiveParameter)
	}
}

func TestLSP_parseSignatureHelp_EmptyOnNil(t *testing.T) {
	if result := parseSignatureHelp(nil); result != nil {
		t.Errorf("expected nil for nil input, got %+v", result)
	}
	if result := parseSignatureHelp(json.RawMessage("null")); result != nil {
		t.Errorf("expected nil for null input, got %+v", result)
	}
	if result := parseSignatureHelp(json.RawMessage("[]")); result != nil {
		t.Errorf("expected nil for empty array, got %+v", result)
	}
}

// G-HL-02: Test parseCodeLenses extracts lens entries with titles and positions.
func TestLSP_parseCodeLenses_ParsesEntries(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"range": {
				"start": {"line": 5, "character": 0}
			},
			"command": {
				"title": "3 references",
				"command": "references"
			}
		},
		{
			"range": {
				"start": {"line": 10, "character": 4}
			},
			"command": {
				"title": "2 implementations",
				"command": "implementations"
			}
		}
	]`)
	result := parseCodeLenses(raw)
	if len(result) != 2 {
		t.Fatalf("expected 2 code lenses, got %d", len(result))
	}
	if result[0].Line != 5 || result[0].Column != 0 {
		t.Errorf("expected lens 0 at line 5 col 0, got line %d col %d", result[0].Line, result[0].Column)
	}
	if result[0].Label != "3 references" {
		t.Errorf("expected lens 0 label, got %q", result[0].Label)
	}
	if result[0].Command != "references" {
		t.Errorf("expected lens 0 command, got %q", result[0].Command)
	}
	if result[1].Line != 10 || result[1].Column != 4 {
		t.Errorf("expected lens 1 at line 10 col 4, got line %d col %d", result[1].Line, result[1].Column)
	}
	if result[1].Label != "2 implementations" {
		t.Errorf("expected lens 1 label, got %q", result[1].Label)
	}
}

func TestLSP_parseCodeLenses_EmptyOnNil(t *testing.T) {
	if result := parseCodeLenses(nil); len(result) != 0 {
		t.Errorf("expected empty for nil input, got %d entries", len(result))
	}
	if result := parseCodeLenses(json.RawMessage("null")); len(result) != 0 {
		t.Errorf("expected empty for null input, got %d entries", len(result))
	}
}

func TestLSP_parseCodeLenses_SkipsEmptyTitles(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"range": {"start": {"line": 0, "character": 0}},
			"command": {"title": "", "command": ""}
		},
		{
			"range": {"start": {"line": 1, "character": 0}},
			"command": {"title": "valid", "command": "test"}
		}
	]`)
	result := parseCodeLenses(raw)
	if len(result) != 1 {
		t.Fatalf("expected 1 valid lens (skip empty title), got %d", len(result))
	}
	if result[0].Label != "valid" {
		t.Errorf("expected valid label, got %q", result[0].Label)
	}
}

// ============================================================================
// Priority 4 (prompt-1.md): 多根工作区 Workspace Folders 测试
// ============================================================================

// TestLSPService_P4_WorkspaceFoldersCapability verifies that the LSP initialize
// request declares the protocol-defined boolean workspace folder capability.
func TestLSPService_P4_WorkspaceFoldersCapability(t *testing.T) {
	caps := buildLSPClientCapabilities()
	ws, ok := caps["workspace"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected caps[workspace] to be a map, got %T", caps["workspace"])
	}
	wf, ok := ws["workspaceFolders"].(bool)
	if !ok || !wf {
		t.Errorf("workspace.workspaceFolders = %v (%T), want true", ws["workspaceFolders"], ws["workspaceFolders"])
	}
}

// TestLSPService_P4_SetWorkspaceRoots_MultiRoot 验证 SetWorkspaceRoots 在
// 多根场景下正确更新内部状态：WorkspaceRoots() 返回所有根（顺序保留），
// 且去重生效。
func TestLSPService_P4_SetWorkspaceRoots_MultiRoot(t *testing.T) {
	svc := NewLSPService("")
	rootA := t.TempDir()
	rootB := t.TempDir()
	absA, _ := filepath.Abs(rootA)
	absB, _ := filepath.Abs(rootB)
	svc.setWorkspaceRoots([]string{absA, absB, absA}) // 重复项应被去重
	got := svc.WorkspaceRoots()
	if len(got) != 2 {
		t.Fatalf("expected 2 roots after dedup, got %d (%v)", len(got), got)
	}
	if got[0] != absA || got[1] != absB {
		t.Errorf("WorkspaceRoots = %v, want [%q, %q]", got, absA, absB)
	}
}

// TestLSPService_P4_SetWorkspaceRoots_SingleDegradation 验证 SetWorkspaceRoots
// 在传入单个根时退化为单根模式：workspaceRoots 为空、workspaceRoot 被设。
func TestLSPService_P4_SetWorkspaceRoots_SingleDegradation(t *testing.T) {
	svc := NewLSPService("")
	root := t.TempDir()
	absRoot, _ := filepath.Abs(root)
	svc.setWorkspaceRoots([]string{absRoot})
	svc.mu.Lock()
	multiRoots := append([]string(nil), svc.workspaceRoots...)
	primary := svc.workspaceRoot
	svc.mu.Unlock()
	if len(multiRoots) != 0 {
		t.Errorf("expected workspaceRoots empty in single-root mode, got %v", multiRoots)
	}
	if primary != absRoot {
		t.Errorf("workspaceRoot = %q, want %q", primary, absRoot)
	}
	got := svc.WorkspaceRoots()
	if len(got) != 1 || got[0] != absRoot {
		t.Errorf("WorkspaceRoots = %v, want [%q]", got, absRoot)
	}
}

// TestLSPService_P4_DidChangeWorkspaceFolders_NoOpOnSingleRoot 验证在单根
// 模式下，DidChangeWorkspaceFolders 是空操作（不发送通知）。
func TestLSPService_P4_DidChangeWorkspaceFolders_NoOpOnSingleRoot(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	// 不应 panic，也不应发送通知（无服务器运行）。
	svc.DidChangeWorkspaceFolders([]string{t.TempDir()}, nil)
}

// ============================================================================
// Architecture C (prompt-1.md 491-500): LSP 客户端能力补全测试
// 验证 buildLSPClientCapabilities() 声明了 callHierarchy / typeHierarchy /
// declaration / documentLink / selectionRange / semanticTokens.range=true /
// inlineValue 等能力，使服务器启用对应功能。
// ============================================================================

// tdTextDocumentCaps 提取 capabilities["textDocument"] 子映射，便于各测试复用。
func tdTextDocumentCaps(t *testing.T) map[string]interface{} {
	t.Helper()
	caps := buildLSPClientCapabilities()
	td, ok := caps["textDocument"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected caps[textDocument] to be a map, got %T", caps["textDocument"])
	}
	return td
}

// TestLSPService_ArchC_CallHierarchyCapability verifies the static
// call-hierarchy capability requested by prompt-1.
func TestLSPService_ArchC_CallHierarchyCapability(t *testing.T) {
	td := tdTextDocumentCaps(t)
	ch, ok := td["callHierarchy"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected textDocument.callHierarchy to be a map, got %T", td["callHierarchy"])
	}
	dyn, ok := ch["dynamicRegistration"].(bool)
	if !ok || dyn {
		t.Errorf("textDocument.callHierarchy.dynamicRegistration = %v, want false", ch["dynamicRegistration"])
	}
}

// TestLSPService_ArchC_TypeHierarchyCapability 验证 textDocument.typeHierarchy
// 能力已声明且 dynamicRegistration=true。
func TestLSPService_ArchC_TypeHierarchyCapability(t *testing.T) {
	td := tdTextDocumentCaps(t)
	th, ok := td["typeHierarchy"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected textDocument.typeHierarchy to be a map, got %T", td["typeHierarchy"])
	}
	dyn, ok := th["dynamicRegistration"].(bool)
	if !ok || !dyn {
		t.Errorf("textDocument.typeHierarchy.dynamicRegistration = %v, want true", th["dynamicRegistration"])
	}
}

// TestLSPService_ArchC_DeclarationCapability 验证 textDocument.declaration
// 能力已声明，dynamicRegistration=true 且 linkSupport=true。
func TestLSPService_ArchC_DeclarationCapability(t *testing.T) {
	td := tdTextDocumentCaps(t)
	decl, ok := td["declaration"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected textDocument.declaration to be a map, got %T", td["declaration"])
	}
	dyn, ok := decl["dynamicRegistration"].(bool)
	if !ok || !dyn {
		t.Errorf("textDocument.declaration.dynamicRegistration = %v, want true", decl["dynamicRegistration"])
	}
	link, ok := decl["linkSupport"].(bool)
	if !ok || !link {
		t.Errorf("textDocument.declaration.linkSupport = %v, want true", decl["linkSupport"])
	}
}

// TestLSPService_ArchC_DocumentLinkCapability 验证 textDocument.documentLink
// 能力已声明，dynamicRegistration=true 且 tooltipSupport=true。
func TestLSPService_ArchC_DocumentLinkCapability(t *testing.T) {
	td := tdTextDocumentCaps(t)
	dl, ok := td["documentLink"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected textDocument.documentLink to be a map, got %T", td["documentLink"])
	}
	dyn, ok := dl["dynamicRegistration"].(bool)
	if !ok || !dyn {
		t.Errorf("textDocument.documentLink.dynamicRegistration = %v, want true", dl["dynamicRegistration"])
	}
	tooltip, ok := dl["tooltipSupport"].(bool)
	if !ok || !tooltip {
		t.Errorf("textDocument.documentLink.tooltipSupport = %v, want true", dl["tooltipSupport"])
	}
}

// TestLSPService_ArchC_SelectionRangeCapability 验证 textDocument.selectionRange
// 能力已声明且 dynamicRegistration=true。
func TestLSPService_ArchC_SelectionRangeCapability(t *testing.T) {
	td := tdTextDocumentCaps(t)
	sr, ok := td["selectionRange"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected textDocument.selectionRange to be a map, got %T", td["selectionRange"])
	}
	dyn, ok := sr["dynamicRegistration"].(bool)
	if !ok || !dyn {
		t.Errorf("textDocument.selectionRange.dynamicRegistration = %v, want true", sr["dynamicRegistration"])
	}
}

// TestLSPService_ArchC_SemanticTokensRange 验证 textDocument.semanticTokens.requests.range
// 为 true（Architecture C 将其从 false 改为 true）。
func TestLSPService_ArchC_SemanticTokensRange(t *testing.T) {
	td := tdTextDocumentCaps(t)
	st, ok := td["semanticTokens"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected textDocument.semanticTokens to be a map, got %T", td["semanticTokens"])
	}
	reqs, ok := st["requests"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected semanticTokens.requests to be a map, got %T", st["requests"])
	}
	rng, ok := reqs["range"].(bool)
	if !ok || !rng {
		t.Errorf("semanticTokens.requests.range = %v, want true", reqs["range"])
	}
	if dynamic, ok := st["dynamicRegistration"].(bool); !ok || dynamic {
		t.Errorf("semanticTokens.dynamicRegistration = %v, want false", st["dynamicRegistration"])
	}
	tokenTypes, ok := st["tokenTypes"].([]string)
	if !ok || !lspTestContainsString(tokenTypes, "decorator") {
		t.Errorf("semanticTokens.tokenTypes = %#v, want decorator", st["tokenTypes"])
	}
}

// TestLSPService_ArchC_InlineValueCapability 验证 textDocument.inlineValue
// 能力已声明且 dynamicRegistration=true（用于调试时内联值显示）。
func TestLSPService_ArchC_InlineValueCapability(t *testing.T) {
	td := tdTextDocumentCaps(t)
	iv, ok := td["inlineValue"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected textDocument.inlineValue to be a map, got %T", td["inlineValue"])
	}
	dyn, ok := iv["dynamicRegistration"].(bool)
	if !ok || !dyn {
		t.Errorf("textDocument.inlineValue.dynamicRegistration = %v, want true", iv["dynamicRegistration"])
	}
}

// --- F-1: Call Hierarchy / Type Hierarchy 测试 ---

// newMockLSPServer 启动一个 mock LSP server，根据 methodHandlers 回调响应请求。
// 返回已初始化的 LSPService（servers["go"] 已注入）。
func newMockLSPServer(t *testing.T, methodHandlers map[string]func(params interface{}) interface{}) *LSPService {
	t.Helper()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()

	go func() {
		defer serverW.Close()
		r := bufio.NewReader(serverR)
		for {
			var contentLength int
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				if line == "" {
					break
				}
				if strings.HasPrefix(line, "Content-Length:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
					contentLength, _ = strconv.Atoi(v)
				}
			}
			if contentLength <= 0 {
				return
			}
			body := make([]byte, contentLength)
			if _, err := io.ReadFull(r, body); err != nil {
				return
			}
			var msg map[string]interface{}
			if json.Unmarshal(body, &msg) != nil {
				continue
			}
			method, _ := msg["method"].(string)
			if id, ok := msg["id"]; ok && method != "" {
				var resultObj interface{}
				if method == "initialize" {
					_ = json.Unmarshal([]byte(`{"capabilities":{}}`), &resultObj)
				} else if h, found := methodHandlers[method]; found {
					resultObj = h(msg["params"])
				} else {
					resultObj = map[string]interface{}{}
				}
				resp, _ := json.Marshal(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      id,
					"result":  resultObj,
				})
				header := "Content-Length: " + strconv.Itoa(len(resp)) + "\r\n\r\n"
				_, _ = serverW.Write([]byte(header))
				_, _ = serverW.Write(resp)
			}
		}
	}()

	client := newJSONRPCClient(clientR, clientW)
	srv := &lspServer{
		client:      client,
		docVersions: make(map[string]int),
		docHashes:   make(map[string]string),
		docLastSync: make(map[string]time.Time),
		diags:       make(map[string][]Diagnostic),
	}
	svc := NewLSPService("/tmp/ws")
	svc.mu.Lock()
	svc.servers["go"] = srv
	svc.mu.Unlock()
	if err := svc.initializeLocked(srv, "go", "/tmp/ws", nil); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return svc
}

// TestF1_PrepareCallHierarchy 验证 prepareCallHierarchy 解析 SymbolInformation 列表。
func TestF1_PrepareCallHierarchy(t *testing.T) {
	svc := newMockLSPServer(t, map[string]func(params interface{}) interface{}{
		"textDocument/prepareCallHierarchy": func(params interface{}) interface{} {
			return []map[string]interface{}{
				{
					"name": "Foo",
					"kind": 12, // Function
					"uri":  "file:///tmp/ws/main.go",
					"range": map[string]interface{}{
						"start": map[string]int{"line": 5, "character": 6},
						"end":   map[string]int{"line": 5, "character": 9},
					},
					"selectionRange": map[string]interface{}{
						"start": map[string]int{"line": 5, "character": 6},
						"end":   map[string]int{"line": 5, "character": 9},
					},
				},
			}
		},
	})
	req := LSPCompletionRequest{
		Language: "go", FilePath: "/tmp/ws/main.go", Line: 5, Column: 6, Content: "package main\nfunc Foo() {}\n",
	}
	items, err := svc.PrepareCallHierarchy(req)
	if err != nil {
		t.Fatalf("PrepareCallHierarchy: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Name != "Foo" || items[0].Kind != 12 {
		t.Errorf("item = %+v", items[0])
	}
	wantPath := filepath.FromSlash("/tmp/ws/main.go")
	if items[0].FilePath != wantPath {
		t.Errorf("FilePath = %q, want %q", items[0].FilePath, wantPath)
	}
	if items[0].Line != 5 || items[0].Column != 6 {
		t.Errorf("range start = (%d,%d)", items[0].Line, items[0].Column)
	}
	if items[0].SelectionLine != 5 || items[0].SelectionCol != 6 {
		t.Errorf("selection start = (%d,%d)", items[0].SelectionLine, items[0].SelectionCol)
	}
}

// TestF1_CallHierarchyIncomingCalls 验证 incomingCalls 解析 from + fromRanges。
func TestF1_CallHierarchyIncomingCalls(t *testing.T) {
	svc := newMockLSPServer(t, map[string]func(params interface{}) interface{}{
		"callHierarchy/incomingCalls": func(params interface{}) interface{} {
			return []map[string]interface{}{
				{
					"from": map[string]interface{}{
						"name": "Bar",
						"kind": 12,
						"uri":  "file:///tmp/ws/bar.go",
						"range": map[string]interface{}{
							"start": map[string]int{"line": 10, "character": 0},
							"end":   map[string]int{"line": 10, "character": 20},
						},
						"selectionRange": map[string]interface{}{
							"start": map[string]int{"line": 10, "character": 4},
							"end":   map[string]int{"line": 10, "character": 7},
						},
					},
					"fromRanges": []map[string]interface{}{
						{
							"start": map[string]int{"line": 11, "character": 2},
							"end":   map[string]int{"line": 11, "character": 5},
						},
					},
				},
			}
		},
	})
	req := LSPCompletionRequest{
		Language: "go", FilePath: "/tmp/ws/main.go", Line: 5, Column: 6, Content: "package main\n",
	}
	item := LSPCallHierarchyItem{Name: "Foo", Kind: 12, FilePath: "/tmp/ws/main.go"}
	calls, err := svc.CallHierarchyIncomingCalls(req, item)
	if err != nil {
		t.Fatalf("CallHierarchyIncomingCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].From.Name != "Bar" {
		t.Errorf("from.Name = %q", calls[0].From.Name)
	}
	wantFrom := filepath.FromSlash("/tmp/ws/bar.go")
	if calls[0].From.FilePath != wantFrom {
		t.Errorf("from.FilePath = %q, want %q", calls[0].From.FilePath, wantFrom)
	}
	if len(calls[0].FromRanges) != 1 {
		t.Fatalf("expected 1 fromRange, got %d", len(calls[0].FromRanges))
	}
	if calls[0].FromRanges[0].Line != 11 || calls[0].FromRanges[0].Column != 2 {
		t.Errorf("fromRange start = (%d,%d)", calls[0].FromRanges[0].Line, calls[0].FromRanges[0].Column)
	}
}

// TestF1_CallHierarchyOutgoingCalls 验证 outgoingCalls 解析 to + fromRanges。
func TestF1_CallHierarchyOutgoingCalls(t *testing.T) {
	svc := newMockLSPServer(t, map[string]func(params interface{}) interface{}{
		"callHierarchy/outgoingCalls": func(params interface{}) interface{} {
			return []map[string]interface{}{
				{
					"to": map[string]interface{}{
						"name": "Baz",
						"kind": 12,
						"uri":  "file:///tmp/ws/baz.go",
						"range": map[string]interface{}{
							"start": map[string]int{"line": 1, "character": 0},
							"end":   map[string]int{"line": 1, "character": 10},
						},
						"selectionRange": map[string]interface{}{
							"start": map[string]int{"line": 1, "character": 4},
							"end":   map[string]int{"line": 1, "character": 7},
						},
					},
					"fromRanges": []map[string]interface{}{
						{
							"start": map[string]int{"line": 6, "character": 2},
							"end":   map[string]int{"line": 6, "character": 5},
						},
					},
				},
			}
		},
	})
	req := LSPCompletionRequest{
		Language: "go", FilePath: "/tmp/ws/main.go", Line: 5, Column: 6, Content: "package main\n",
	}
	item := LSPCallHierarchyItem{Name: "Foo", Kind: 12, FilePath: "/tmp/ws/main.go"}
	calls, err := svc.CallHierarchyOutgoingCalls(req, item)
	if err != nil {
		t.Fatalf("CallHierarchyOutgoingCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].To.Name != "Baz" {
		t.Errorf("to.Name = %q", calls[0].To.Name)
	}
	wantTo := filepath.FromSlash("/tmp/ws/baz.go")
	if calls[0].To.FilePath != wantTo {
		t.Errorf("to.FilePath = %q, want %q", calls[0].To.FilePath, wantTo)
	}
}

// TestF1_PrepareTypeHierarchy 验证 prepareTypeHierarchy 解析。
func TestF1_PrepareTypeHierarchy(t *testing.T) {
	svc := newMockLSPServer(t, map[string]func(params interface{}) interface{}{
		"textDocument/prepareTypeHierarchy": func(params interface{}) interface{} {
			return []map[string]interface{}{
				{
					"name": "MyInterface",
					"kind": 11, // Interface
					"uri":  "file:///tmp/ws/iface.go",
					"range": map[string]interface{}{
						"start": map[string]int{"line": 0, "character": 0},
						"end":   map[string]int{"line": 0, "character": 20},
					},
					"selectionRange": map[string]interface{}{
						"start": map[string]int{"line": 0, "character": 10},
						"end":   map[string]int{"line": 0, "character": 22},
					},
				},
			}
		},
	})
	req := LSPCompletionRequest{
		Language: "go", FilePath: "/tmp/ws/iface.go", Line: 0, Column: 10, Content: "package main\n",
	}
	items, err := svc.PrepareTypeHierarchy(req)
	if err != nil {
		t.Fatalf("PrepareTypeHierarchy: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Name != "MyInterface" || items[0].Kind != 11 {
		t.Errorf("item = %+v", items[0])
	}
	wantPath := filepath.FromSlash("/tmp/ws/iface.go")
	if items[0].FilePath != wantPath {
		t.Errorf("FilePath = %q, want %q", items[0].FilePath, wantPath)
	}
}

// TestF1_TypeHierarchySupertypesAndSubtypes 验证 supertypes/subtypes 解析。
func TestF1_TypeHierarchySupertypesAndSubtypes(t *testing.T) {
	svc := newMockLSPServer(t, map[string]func(params interface{}) interface{}{
		"typeHierarchy/supertypes": func(params interface{}) interface{} {
			return []map[string]interface{}{
				{
					"name": "Parent",
					"kind": 11,
					"uri":  "file:///tmp/ws/parent.go",
					"range": map[string]interface{}{
						"start": map[string]int{"line": 0, "character": 0},
						"end":   map[string]int{"line": 0, "character": 10},
					},
					"selectionRange": map[string]interface{}{
						"start": map[string]int{"line": 0, "character": 0},
						"end":   map[string]int{"line": 0, "character": 10},
					},
				},
			}
		},
		"typeHierarchy/subtypes": func(params interface{}) interface{} {
			return []map[string]interface{}{
				{
					"name": "Child",
					"kind": 11,
					"uri":  "file:///tmp/ws/child.go",
					"range": map[string]interface{}{
						"start": map[string]int{"line": 0, "character": 0},
						"end":   map[string]int{"line": 0, "character": 10},
					},
					"selectionRange": map[string]interface{}{
						"start": map[string]int{"line": 0, "character": 0},
						"end":   map[string]int{"line": 0, "character": 10},
					},
				},
			}
		},
	})
	req := LSPCompletionRequest{
		Language: "go", FilePath: "/tmp/ws/iface.go", Line: 0, Column: 10, Content: "package main\n",
	}
	item := LSPTypeHierarchyItem{Name: "MyInterface", Kind: 11, FilePath: "/tmp/ws/iface.go"}
	supers, err := svc.TypeHierarchySupertypes(req, item)
	if err != nil {
		t.Fatalf("TypeHierarchySupertypes: %v", err)
	}
	if len(supers) != 1 || supers[0].Name != "Parent" {
		t.Errorf("supertypes = %+v", supers)
	}
	subs, err := svc.TypeHierarchySubtypes(req, item)
	if err != nil {
		t.Fatalf("TypeHierarchySubtypes: %v", err)
	}
	if len(subs) != 1 || subs[0].Name != "Child" {
		t.Errorf("subtypes = %+v", subs)
	}
}

// TestF1_CallHierarchy_EmptyWhenNotRunning 验证 server 未运行时优雅降级返回空切片。
func TestF1_CallHierarchy_EmptyWhenNotRunning(t *testing.T) {
	svc := NewLSPService("/tmp/ws") // 无 server
	req := LSPCompletionRequest{Language: "go", FilePath: "/tmp/ws/main.go", Line: 0, Column: 0, Content: ""}
	items, err := svc.PrepareCallHierarchy(req)
	if err != nil {
		t.Fatalf("PrepareCallHierarchy: %v", err)
	}
	if items == nil {
		t.Error("expected non-nil empty slice")
	}
	calls, err := svc.CallHierarchyIncomingCalls(req, LSPCallHierarchyItem{})
	if err != nil {
		t.Fatalf("CallHierarchyIncomingCalls: %v", err)
	}
	if calls == nil {
		t.Error("expected non-nil empty slice")
	}
	typeItems, err := svc.PrepareTypeHierarchy(req)
	if err != nil {
		t.Fatalf("PrepareTypeHierarchy: %v", err)
	}
	if typeItems == nil {
		t.Error("expected non-nil empty slice")
	}
}

// TestF1_parseCallHierarchyItems_JSON 校验解析 helper 直接处理 JSON RawMessage。
func TestF1_parseCallHierarchyItems_JSON(t *testing.T) {
	raw := json.RawMessage(`[{"name":"A","kind":12,"uri":"file:///x.go","range":{"start":{"line":1,"character":2},"end":{"line":1,"character":5}},"selectionRange":{"start":{"line":1,"character":2},"end":{"line":1,"character":5}},"data":{"foo":"bar"}}]`)
	items := parseCallHierarchyItems(raw)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Name != "A" {
		t.Errorf("item name = %+v", items[0])
	}
	wantPath := filepath.FromSlash("/x.go")
	if items[0].FilePath != wantPath {
		t.Errorf("FilePath = %q, want %q", items[0].FilePath, wantPath)
	}
	if len(items[0].Data) == 0 {
		t.Error("Data should be preserved")
	}
	// 空 / null 输入
	if got := parseCallHierarchyItems(json.RawMessage(`null`)); len(got) != 0 {
		t.Errorf("null should return empty, got %d", len(got))
	}
	if got := parseCallHierarchyItems(json.RawMessage(`[]`)); len(got) != 0 {
		t.Errorf("[] should return empty, got %d", len(got))
	}
}

// TestF1_parseTypeHierarchyItems_JSON 校验解析 helper。
func TestF1_parseTypeHierarchyItems_JSON(t *testing.T) {
	raw := json.RawMessage(`[{"name":"T","kind":11,"uri":"file:///t.go","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":5}},"selectionRange":{"start":{"line":0,"character":0},"end":{"line":0,"character":5}}}]`)
	items := parseTypeHierarchyItems(raw)
	if len(items) != 1 || items[0].Name != "T" || items[0].Kind != 11 {
		t.Errorf("items = %+v", items)
	}
}

// === F-2 (prompt-2.md): workspace/configuration / applyEdit / workspaceFolders ===

// TestF2_ClientCapabilities_DeclaresConfigAndApplyEdit 验证 buildLSPClientCapabilities
// 声明了 workspace.configuration 与 workspace.applyEdit，使服务器会发起对应请求。
func TestF2_ClientCapabilities_DeclaresConfigAndApplyEdit(t *testing.T) {
	caps := buildLSPClientCapabilities()
	ws, ok := caps["workspace"].(map[string]interface{})
	if !ok {
		t.Fatal("workspace capability missing")
	}
	if v, ok := ws["configuration"].(bool); !ok || !v {
		t.Errorf("workspace.configuration = %v, want true", ws["configuration"])
	}
	if v, ok := ws["applyEdit"].(bool); !ok || !v {
		t.Errorf("workspace.applyEdit = %v, want true", ws["applyEdit"])
	}
}

// TestF2_WorkspaceConfiguration 验证 handleWorkspaceConfiguration 按 items
// 顺序返回已注册的 section 配置，未注册的返回 null。
func TestF2_WorkspaceConfiguration(t *testing.T) {
	svc := NewLSPService("/tmp/ws")
	svc.SetLSPConfig("gopls", map[string]interface{}{"buildFlags": []string{"-tags=integration"}})
	svc.SetLSPConfig("typescript", map[string]interface{}{"format.enable": false})

	params := json.RawMessage(`{"items":[{"section":"gopls","scopeUri":"file:///a.go"},{"section":"typescript"},{"section":"unknown"}]}`)
	result, err := svc.handleWorkspaceConfiguration(params)
	if err != nil {
		t.Fatalf("handleWorkspaceConfiguration: %v", err)
	}
	arr, ok := result.([]interface{})
	if !ok || len(arr) != 3 {
		t.Fatalf("result = %+v, want 3-element array", result)
	}
	gopls, ok := arr[0].(map[string]interface{})
	if !ok {
		t.Errorf("arr[0] = %+v, want gopls config map", arr[0])
	} else if flags, _ := gopls["buildFlags"].([]string); len(flags) != 1 || flags[0] != "-tags=integration" {
		// JSON unmarshal of []string becomes []interface{}
		if flagsAny, ok := gopls["buildFlags"].([]interface{}); !ok || len(flagsAny) != 1 || flagsAny[0] != "-tags=integration" {
			t.Errorf("gopls.buildFlags = %+v, want [-tags=integration]", gopls["buildFlags"])
		}
	}
	if arr[1] == nil {
		t.Errorf("arr[1] = nil, want typescript config")
	}
	if arr[2] != nil {
		t.Errorf("arr[2] = %+v, want nil for unknown section", arr[2])
	}
}

// TestF2_WorkspaceFolders 验证 handleWorkspaceFolders 返回当前工作区。
func TestF2_WorkspaceFolders(t *testing.T) {
	svc := NewLSPService(filepath.FromSlash("/tmp/ws"))
	result, err := svc.handleWorkspaceFolders(nil)
	if err != nil {
		t.Fatalf("handleWorkspaceFolders: %v", err)
	}
	arr, ok := result.([]map[string]string)
	if !ok || len(arr) != 1 {
		t.Fatalf("result = %+v, want 1-element array", result)
	}
	if arr[0]["uri"] == "" {
		t.Errorf("uri is empty")
	}
	if arr[0]["name"] != "ws" {
		t.Errorf("name = %q, want ws", arr[0]["name"])
	}
}

// TestF2_WorkspaceFolders_MultiRoot 验证多根工作区返回多个文件夹。
func TestF2_WorkspaceFolders_MultiRoot(t *testing.T) {
	svc := NewLSPService("")
	svc.mu.Lock()
	svc.workspaceRoots = []string{filepath.FromSlash("/tmp/a"), filepath.FromSlash("/tmp/b")}
	svc.mu.Unlock()
	result, err := svc.handleWorkspaceFolders(nil)
	if err != nil {
		t.Fatalf("handleWorkspaceFolders: %v", err)
	}
	arr, ok := result.([]map[string]string)
	if !ok || len(arr) != 2 {
		t.Fatalf("result = %+v, want 2-element array", result)
	}
}

// TestF2_WorkspaceApplyEdit_NoFileService 验证未注入 FileService 时
// applyEdit 返回 applied=false。
func TestF2_WorkspaceApplyEdit_NoFileService(t *testing.T) {
	svc := NewLSPService("/tmp/ws")
	params := json.RawMessage(`{"label":"test","edit":{"changes":{}}}`)
	result, err := svc.handleWorkspaceApplyEdit(params)
	if err != nil {
		t.Fatalf("handleWorkspaceApplyEdit: %v", err)
	}
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("result = %+v, want map", result)
	}
	if applied, _ := m["applied"].(bool); applied {
		t.Errorf("applied = true, want false when no file service")
	}
}

// TestF2_WorkspaceApplyEdit_TextDocumentEdit 验证 documentChanges 中的
// TextDocumentEdit 能正确应用编辑到文件。
func TestF2_WorkspaceApplyEdit_TextDocumentEdit(t *testing.T) {
	tmp := t.TempDir()
	fsvc := NewFileService()
	if err := fsvc.setWorkspaceRoot(tmp); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	target := filepath.Join(tmp, "a.go")
	if err := fsvc.WriteFile(target, "line0\nline1\nline2"); err != nil {
		t.Fatalf("write: %v", err)
	}
	svc := NewLSPService(tmp)
	svc.setFileService(fsvc)
	uri := pathToURI(target)
	// 替换 line1 的 "line1" 为 "EDITED"
	editJSON := fmt.Sprintf(`{"label":"test","edit":{"documentChanges":[{"textDocument":{"uri":%q,"version":1},"edits":[{"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":5}},"newText":"EDITED"}]}]}}`, uri)
	params := json.RawMessage(editJSON)
	result, err := svc.handleWorkspaceApplyEdit(params)
	if err != nil {
		t.Fatalf("handleWorkspaceApplyEdit: %v", err)
	}
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("result = %+v, want map", result)
	}
	if applied, _ := m["applied"].(bool); !applied {
		t.Fatalf("applied = false, reason = %v", m["failureReason"])
	}
	content, _ := fsvc.ReadFile(target)
	want := "line0\nEDITED\nline2"
	if content != want {
		t.Errorf("content = %q, want %q", content, want)
	}
}

// TestF2_WorkspaceApplyEdit_CreateFile 验证 documentChanges 中的 CreateFile。
func TestF2_WorkspaceApplyEdit_CreateFile(t *testing.T) {
	tmp := t.TempDir()
	fsvc := NewFileService()
	if err := fsvc.setWorkspaceRoot(tmp); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	svc := NewLSPService(tmp)
	svc.setFileService(fsvc)
	target := filepath.Join(tmp, "new.txt")
	uri := pathToURI(target)
	editJSON := fmt.Sprintf(`{"label":"create","edit":{"documentChanges":[{"kind":"create","uri":%q}]}}`, uri)
	params := json.RawMessage(editJSON)
	result, err := svc.handleWorkspaceApplyEdit(params)
	if err != nil {
		t.Fatalf("handleWorkspaceApplyEdit: %v", err)
	}
	m, _ := result.(map[string]interface{})
	if applied, _ := m["applied"].(bool); !applied {
		t.Fatalf("applied = false, reason = %v", m["failureReason"])
	}
	if _, err := fsvc.ReadFile(target); err != nil {
		t.Errorf("new.txt not created: %v", err)
	}
}

// TestF2_ApplyTextEdits_SingleLine 验证单行 edit。
func TestF2_ApplyTextEdits_SingleLine(t *testing.T) {
	edits := []lspTextEditJSON{{
		Range: struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
			End struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"end"`
		}{
			Start: struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			}{Line: 0, Character: 0},
			End: struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			}{Line: 0, Character: 3},
		},
		NewText: "HELLO",
	}}
	result, err := applyTextEdits("old world", edits)
	if err != nil {
		t.Fatalf("applyTextEdits: %v", err)
	}
	want := "HELLO world"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}
}

// TestF2_ApplyTextEdits_MultipleEdits 验证多个 edit 倒序应用。
func TestF2_ApplyTextEdits_MultipleEdits(t *testing.T) {
	mkEdit := func(line, startCol, endCol int, newText string) lspTextEditJSON {
		return lspTextEditJSON{
			Range: struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
				End struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"end"`
			}{
				Start: struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				}{Line: line, Character: startCol},
				End: struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				}{Line: line, Character: endCol},
			},
			NewText: newText,
		}
	}
	edits := []lspTextEditJSON{
		mkEdit(0, 0, 3, "AAA"), // 替换 "aaa"
		mkEdit(1, 0, 3, "BBB"), // 替换 "bbb"
	}
	result, err := applyTextEdits("aaa xxx\nbbb yyy", edits)
	if err != nil {
		t.Fatalf("applyTextEdits: %v", err)
	}
	want := "AAA xxx\nBBB yyy"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}
}

// TestF2_ApplyTextEdits_MultiLine 验证多行 edit（替换跨行区域）。
func TestF2_ApplyTextEdits_MultiLine(t *testing.T) {
	edits := []lspTextEditJSON{{
		Range: struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
			End struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"end"`
		}{
			Start: struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			}{Line: 0, Character: 2},
			End: struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			}{Line: 1, Character: 2},
		},
		NewText: "X\nY",
	}}
	result, err := applyTextEdits("ab cd\nef gh", edits)
	if err != nil {
		t.Fatalf("applyTextEdits: %v", err)
	}
	want := "abX\nY gh"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}
}

// TestF2_DispatchRoutesServerRequest 验证 dispatch 将 server→client request
// 路由到 requestHandler 并写回 JSON-RPC response。
func TestF2_DispatchRoutesServerRequest(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	client := newJSONRPCClient(clientR, clientW)
	client.requestHandler = func(method string, params json.RawMessage) (interface{}, error) {
		if method == "workspace/configuration" {
			return []interface{}{map[string]interface{}{"buildFlags": []string{"-tags=x"}}}, nil
		}
		return nil, fmt.Errorf("unsupported method %s", method)
	}
	// 模拟 server 发送 workspace/configuration request
	go func() {
		defer serverW.Close()
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      42,
			"method":  "workspace/configuration",
			"params":  map[string]interface{}{"items": []map[string]string{{"section": "gopls"}}},
		}
		data, _ := json.Marshal(req)
		header := "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n"
		_, _ = serverW.Write([]byte(header))
		_, _ = serverW.Write(data)
	}()

	// 从 clientW 读取 client 写回的 response
	r := bufio.NewReader(serverR)
	var contentLength int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			contentLength, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
		}
	}
	if contentLength <= 0 {
		t.Fatal("no Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if string(resp.ID) != "42" {
		t.Errorf("id = %s, want 42", string(resp.ID))
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	// result 应为 [ { "buildFlags": ["-tags=x"] } ]
	var arr []map[string]interface{}
	if err := json.Unmarshal(resp.Result, &arr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(arr) != 1 {
		t.Fatalf("result array len = %d, want 1", len(arr))
	}
}

// TestF2_DispatchRoutesServerRequest_NoHandler 验证未注册 requestHandler 时
// 回写 method-not-found 错误。
func TestF2_DispatchRoutesServerRequest_NoHandler(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	client := newJSONRPCClient(clientR, clientW)
	// 不设置 requestHandler，client 仍需存活以驱动 readLoop。
	_ = client
	go func() {
		defer serverW.Close()
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      7,
			"method":  "workspace/workspaceFolders",
		}
		data, _ := json.Marshal(req)
		header := "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n"
		_, _ = serverW.Write([]byte(header))
		_, _ = serverW.Write(data)
	}()
	r := bufio.NewReader(serverR)
	var contentLength int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			contentLength, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
		}
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	var resp struct {
		ID    json.RawMessage `json:"id"`
		Error *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error response, got nil")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", resp.Error.Code)
	}
}

func TestF2_ApplyEditDoesNotBlockDispatchAndPreservesStringIDs(t *testing.T) {
	serverR, clientW := io.Pipe()
	defer serverR.Close()
	defer clientW.Close()
	applyStarted := make(chan struct{})
	releaseApply := make(chan struct{})
	var releaseApplyOnce sync.Once
	release := func() { releaseApplyOnce.Do(func() { close(releaseApply) }) }
	defer release()
	client := &jsonRPCClient{
		w:       clientW,
		pending: make(map[int64]chan *rpcResponse),
		notifs:  make(map[string][]func(json.RawMessage)),
		done:    make(chan struct{}),
		requestHandler: func(method string, _ json.RawMessage) (interface{}, error) {
			if method == "workspace/applyEdit" {
				close(applyStarted)
				<-releaseApply
				return map[string]interface{}{"applied": true}, nil
			}
			return map[string]interface{}{"ok": true}, nil
		},
	}
	client.dispatch(json.RawMessage(`{"jsonrpc":"2.0","id":"apply-1","method":"workspace/applyEdit","params":{"edit":{}}}`))
	select {
	case <-applyStarted:
	case <-time.After(time.Second):
		t.Fatal("applyEdit handler did not start")
	}
	client.dispatch(json.RawMessage(`{"jsonrpc":"2.0","id":"fast-2","method":"workspace/configuration","params":{"items":[]}}`))

	reader := bufio.NewReader(serverR)
	readMessage := func() <-chan map[string]interface{} {
		result := make(chan map[string]interface{}, 1)
		go func() {
			message, _ := readLSPTestWireMessage(reader)
			result <- message
		}()
		return result
	}
	select {
	case response := <-readMessage():
		if response["id"] != "fast-2" {
			t.Fatalf("first response id = %#v, want fast-2", response["id"])
		}
	case <-time.After(time.Second):
		t.Fatal("fast request was blocked by workspace/applyEdit")
	}
	release()
	select {
	case response := <-readMessage():
		if response["id"] != "apply-1" {
			t.Fatalf("apply response id = %#v, want apply-1", response["id"])
		}
		result := lspProtocolTestMap(t, response["result"], "workspace/applyEdit.result")
		if result["applied"] != true {
			t.Fatalf("workspace/applyEdit result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("apply response did not finish")
	}
}

// --- prompt-1 A8/A9/A17 concurrency and document-sync regressions ---

func TestA8_DispatchWaitsForLateReceiverAndIgnoresDuplicate(t *testing.T) {
	c := &jsonRPCClient{pending: make(map[int64]chan *rpcResponse)}
	ch := make(chan *rpcResponse)
	c.pending[7] = ch
	msg := json.RawMessage(`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`)

	c.dispatch(msg)
	c.dispatch(msg) // The first dispatch owns the channel; duplicates are ignored.
	time.Sleep(20 * time.Millisecond)

	select {
	case resp, ok := <-ch:
		if !ok || resp == nil {
			t.Fatal("late receiver got a closed or nil response")
		}
		if string(resp.Result) != `{"ok":true}` {
			t.Fatalf("response result = %s, want {\"ok\":true}", resp.Result)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("dispatch dropped the response before the receiver was ready")
	}

	select {
	case resp := <-ch:
		t.Fatalf("duplicate response was delivered: %+v", resp)
	case <-time.After(30 * time.Millisecond):
	}
	c.pendingMu.Lock()
	pendingCount := len(c.pending)
	c.pendingMu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending count = %d, want 0", pendingCount)
	}
}

func TestA8_DeliverResponseTimeoutClosesChannel(t *testing.T) {
	ch := make(chan *rpcResponse)
	timeout := 25 * time.Millisecond
	started := time.Now()
	if delivered := deliverRPCResponse(ch, &rpcResponse{}, 9, timeout); delivered {
		t.Fatal("response unexpectedly delivered without a receiver")
	}
	if elapsed := time.Since(started); elapsed < timeout {
		t.Fatalf("delivery returned after %v, before timeout %v", elapsed, timeout)
	}
	if _, open := <-ch; open {
		t.Fatal("timed-out response channel remained open")
	}
}

func TestA8_RequestOnClosedClientDoesNotLeakPending(t *testing.T) {
	done := make(chan struct{})
	close(done)
	c := &jsonRPCClient{
		w:       io.Discard,
		pending: make(map[int64]chan *rpcResponse),
		done:    done,
	}
	started := time.Now()
	_, err := c.request(context.Background(), "test/closed", map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "connection closed") {
		t.Fatalf("request error = %v, want connection closed", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("closed-client request took %v, want a fast failure", elapsed)
	}
	c.pendingMu.Lock()
	pendingCount := len(c.pending)
	c.pendingMu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending count = %d after closed-client request, want 0", pendingCount)
	}
}

func TestLSPWriteMessageContextCanceledBeforeCallDoesNotWrite(t *testing.T) {
	for _, test := range []struct {
		name         string
		context      func() (context.Context, context.CancelFunc)
		wantFailures int32
	}{
		{
			name: "canceled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
		},
		{
			name: "deadline exceeded",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &toggledLSPWriter{}
			client := &jsonRPCClient{w: writer, done: make(chan struct{})}
			ctx, cancel := test.context()
			defer cancel()
			if err := client.writeMessageContext(ctx, map[string]interface{}{"jsonrpc": "2.0"}); err == nil {
				t.Fatal("pre-canceled write unexpectedly succeeded")
			}
			if writes := writer.writeCount(); writes != 0 {
				t.Fatalf("pre-canceled context invoked writer %d times", writes)
			}
			if failures := client.writeFailures.Load(); failures != test.wantFailures {
				t.Fatalf("write failure count = %d, want %d", failures, test.wantFailures)
			}
		})
	}
}

func TestP2_2_CanceledRequestNotifiesServerAndClearsPending(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	client := newJSONRPCClient(clientR, clientW)
	t.Cleanup(func() {
		_ = clientW.Close()
		_ = serverR.Close()
		_ = serverW.Close()
		_ = clientR.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.request(ctx, "textDocument/hover", map[string]interface{}{})
		result <- err
	}()
	r := bufio.NewReader(serverR)
	request, err := readLSPTestWireMessage(r)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("request error = %v, want context canceled", err)
	}
	cancellation, err := readLSPTestWireMessage(r)
	if err != nil {
		t.Fatalf("read cancellation: %v", err)
	}
	if cancellation["method"] != "$/cancelRequest" {
		t.Fatalf("cancellation method = %v", cancellation["method"])
	}
	params := lspProtocolTestMap(t, cancellation["params"], "cancel params")
	if params["id"] != request["id"] {
		t.Fatalf("cancel id = %v, request id = %v", params["id"], request["id"])
	}
	client.pendingMu.Lock()
	pending := len(client.pending)
	client.pendingMu.Unlock()
	if pending != 0 {
		t.Fatalf("pending requests = %d, want 0", pending)
	}
}

func TestP2_5_DiscoversGoWorkAndPnpmWorkspaceRoots(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"go-app", filepath.Join("packages", "web"), filepath.Join("packages", "api")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.25\n\nuse (\n\t./go-app\n)\n"), 0o600); err != nil {
		t.Fatalf("write go.work: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "pnpm-workspace.yaml"), []byte("packages:\n  - 'packages/*'\n"), 0o600); err != nil {
		t.Fatalf("write pnpm-workspace.yaml: %v", err)
	}

	svc := NewLSPService("")
	svc.setWorkspaceRoot(root)
	want := []string{
		root,
		filepath.Join(root, "go-app"),
		filepath.Join(root, "packages", "api"),
		filepath.Join(root, "packages", "web"),
	}
	if got := svc.WorkspaceRoots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("WorkspaceRoots = %v, want %v", got, want)
	}
}

func TestLSPWriteMessageContextTimeoutDoesNotCloseConnection(t *testing.T) {
	writer := newObservedBlockingLSPWriter()
	defer writer.writer.unblock()
	client := &jsonRPCClient{w: writer, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := client.writeMessageContext(ctx, map[string]interface{}{"jsonrpc": "2.0"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("writeMessageContext error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("write timeout took %v", elapsed)
	}
	select {
	case <-client.done:
		t.Fatal("single write timeout closed the LSP connection")
	default:
	}
	_, closes := writer.counts()
	if closes != 0 {
		t.Fatalf("single write timeout closed the transport %d times", closes)
	}
}

func TestLSPWriteMessageContextThresholdTriggersHandlerOnce(t *testing.T) {
	writer := newObservedBlockingLSPWriter()
	defer writer.writer.unblock()
	client := &jsonRPCClient{w: writer, done: make(chan struct{})}
	handlerCalls := make(chan error, 2)
	client.setWriteFailureHandler(func(err error) { handlerCalls <- err })
	for attempt := 0; attempt < lspWriteFailureThreshold; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
		err := client.writeMessageContext(ctx, map[string]interface{}{"attempt": attempt})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
	}
	select {
	case <-handlerCalls:
	case <-time.After(time.Second):
		t.Fatal("write failure threshold did not trigger handler")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	_ = client.writeMessageContext(ctx, map[string]interface{}{"attempt": "extra"})
	cancel()
	select {
	case err := <-handlerCalls:
		t.Fatalf("write failure handler ran more than once: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	_, closes := writer.counts()
	if closes != 0 {
		t.Fatalf("write failure threshold closed transport %d times", closes)
	}
}

func TestLSPWriteMessageContextSuccessResetsConsecutiveFailures(t *testing.T) {
	writer := &toggledLSPWriter{}
	client := &jsonRPCClient{w: writer, done: make(chan struct{})}
	handlerCalls := make(chan error, 1)
	client.setWriteFailureHandler(func(err error) { handlerCalls <- err })
	writeErr := errors.New("write failed")
	write := func(wantErr bool) {
		err := client.writeMessageContext(context.Background(), map[string]interface{}{"jsonrpc": "2.0"})
		if wantErr && !errors.Is(err, writeErr) {
			t.Fatalf("write error = %v, want %v", err, writeErr)
		}
		if !wantErr && err != nil {
			t.Fatalf("successful write error = %v", err)
		}
	}
	writer.setError(writeErr)
	write(true)
	write(true)
	writer.setError(nil)
	write(false)
	writer.setError(writeErr)
	write(true)
	write(true)
	select {
	case err := <-handlerCalls:
		t.Fatalf("two failures after success triggered handler: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	write(true)
	select {
	case <-handlerCalls:
	case <-time.After(time.Second):
		t.Fatal("three consecutive failures after success did not trigger handler")
	}
}

type gatedLSPTestProcess struct {
	cmd         *exec.Cmd
	process     *lspProcess
	killed      chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func startGatedLSPTestProcess(t *testing.T) *gatedLSPTestProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLSPProcessWaitHelper$")
	cmd.Env = append(os.Environ(), "KOYORI_IDE_LSP_WAIT_HELPER=block")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start gated helper process: %v", err)
	}
	gated := &gatedLSPTestProcess{
		cmd:     cmd,
		process: &lspProcess{cmd: cmd, done: make(chan struct{})},
		killed:  make(chan struct{}),
		release: make(chan struct{}),
	}
	go func() {
		err := cmd.Wait()
		gated.process.mu.Lock()
		gated.process.waitErr = err
		gated.process.mu.Unlock()
		close(gated.killed)
		<-gated.release
		close(gated.process.done)
	}()
	t.Cleanup(func() {
		gated.releaseWait()
		_ = gated.process.stop(lspTestProcessTimeout)
	})
	return gated
}

func (p *gatedLSPTestProcess) releaseWait() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func waitTestSignal(t *testing.T, ch <-chan struct{}, timeout time.Duration, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatal(message)
	}
}

func TestA9_WorkspaceSwitchFailsFastAndDetachesServers(t *testing.T) {
	gated := startGatedLSPTestProcess(t)
	svc := NewLSPService("old-root")
	installLSPTestProcess(svc, "go", gated.cmd, gated.process)

	switched := make(chan struct{})
	go func() {
		svc.setWorkspaceRoot("new-root")
		close(switched)
	}()
	waitTestSignal(t, gated.killed, time.Second, "workspace switch did not start stopping the old process")

	svc.mu.Lock()
	switching := svc.switching
	serverCount := len(svc.servers)
	svc.mu.Unlock()
	if !switching || serverCount != 0 {
		t.Fatalf("switch state = %v, server count = %d; want true and 0", switching, serverCount)
	}

	hoverDone := make(chan error, 1)
	go func() {
		_, err := svc.GetHover(LSPCompletionRequest{
			Language: "go",
			FilePath: "main.go",
			Content:  "package main\n",
		})
		hoverDone <- err
	}()
	select {
	case err := <-hoverDone:
		if !errors.Is(err, errWorkspaceSwitching) {
			t.Fatalf("GetHover error = %v, want workspace switching", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("GetHover blocked during workspace switching")
	}

	started := make(chan error, 1)
	go func() { started <- svc.StartLSPServer("go") }()
	select {
	case err := <-started:
		if !errors.Is(err, errWorkspaceSwitching) {
			t.Fatalf("StartLSPServer error = %v, want workspace switching", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("StartLSPServer blocked during workspace switching")
	}

	gated.releaseWait()
	waitTestSignal(t, switched, time.Second, "workspace switch did not finish after process release")
	svc.mu.Lock()
	switching = svc.switching
	svc.mu.Unlock()
	if switching {
		t.Fatal("workspace switching state remained set after completion")
	}
}

func TestA9_StopServersRunsConcurrently(t *testing.T) {
	first := startGatedLSPTestProcess(t)
	second := startGatedLSPTestProcess(t)
	servers := map[string]*lspServer{
		"go":         {cmd: first.cmd, process: first.process, running: true},
		"typescript": {cmd: second.cmd, process: second.process, running: true},
	}
	stopped := make(chan struct{})
	go func() {
		stopLSPServersConcurrently(servers, errWorkspaceSwitching)
		close(stopped)
	}()

	waitTestSignal(t, first.killed, time.Second, "first process was not killed")
	waitTestSignal(t, second.killed, time.Second, "second process was not killed concurrently")
	first.releaseWait()
	second.releaseWait()
	waitTestSignal(t, stopped, time.Second, "parallel stop did not finish after both processes were released")
}

func TestLSPService_LanguageLifecycleLocks(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	goLock := svc.languageLifecycleLock("go")
	if goLock != svc.languageLifecycleLock("go") {
		t.Fatal("same language returned different lifecycle locks")
	}
	if goLock == svc.languageLifecycleLock("typescript") {
		t.Fatal("different languages shared one lifecycle lock")
	}

	goLock.Lock()
	acquired := make(chan struct{})
	go func() {
		svc.languageLifecycleLock("typescript").Lock()
		close(acquired)
		svc.languageLifecycleLock("typescript").Unlock()
	}()
	waitTestSignal(t, acquired, time.Second, "different language lifecycle lock did not proceed concurrently")
	goLock.Unlock()
}

func TestA17_BuildIncrementalChangeUTF16AndCRLF(t *testing.T) {
	tests := []struct {
		name      string
		oldText   string
		newText   string
		startLine int
		startCol  int
		endLine   int
		endCol    int
		text      string
	}{
		{name: "accent", oldText: "aéz", newText: "aêz", startCol: 1, endCol: 2, text: "ê"},
		{name: "astral", oldText: "a😀z", newText: "a😃z", startCol: 1, endCol: 3, text: "😃"},
		{name: "crlf to lf", oldText: "a\r\nb", newText: "a\nb", startCol: 1, endLine: 1, endCol: 0, text: "\n"},
		{name: "lone cr line", oldText: "a\rb", newText: "a\rc", startLine: 1, startCol: 0, endLine: 1, endCol: 1, text: "c"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			change := buildIncrementalChange(tc.oldText, tc.newText)
			if change == nil {
				t.Fatal("buildIncrementalChange returned nil")
			}
			rng, ok := change["range"].(map[string]interface{})
			if !ok {
				t.Fatalf("range = %T, want map", change["range"])
			}
			start, ok := rng["start"].(map[string]int)
			if !ok {
				t.Fatalf("start = %T, want map[string]int", rng["start"])
			}
			end, ok := rng["end"].(map[string]int)
			if !ok {
				t.Fatalf("end = %T, want map[string]int", rng["end"])
			}
			if start["line"] != tc.startLine || start["character"] != tc.startCol ||
				end["line"] != tc.endLine || end["character"] != tc.endCol {
				t.Fatalf("range = (%d,%d)-(%d,%d), want (%d,%d)-(%d,%d)",
					start["line"], start["character"], end["line"], end["character"],
					tc.startLine, tc.startCol, tc.endLine, tc.endCol)
			}
			if text, _ := change["text"].(string); text != tc.text {
				t.Fatalf("text = %q, want %q", text, tc.text)
			}
		})
	}
}

type lspTestWireEvent struct {
	method string
	params map[string]interface{}
	at     time.Time
}

func readLSPTestWireMessage(r *bufio.Reader) (map[string]interface{}, error) {
	contentLength := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			contentLength, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
			if err != nil {
				return nil, err
			}
		}
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("missing Content-Length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	var msg map[string]interface{}
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func writeLSPTestWireMessage(w io.Writer, msg map[string]interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "Content-Length: "+strconv.Itoa(len(data))+"\r\n\r\n"); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func waitLSPTestWireEvent(t *testing.T, events <-chan lspTestWireEvent, method string, timeout time.Duration) lspTestWireEvent {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.method == method {
				return event
			}
			t.Fatalf("got LSP method %q while waiting for %q", event.method, method)
		case <-timer.C:
			t.Fatalf("timed out waiting for LSP method %q", method)
		}
	}
}

func waitPendingDocumentContent(t *testing.T, srv *lspServer, uri, content string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		srv.docMu.Lock()
		pending := srv.pendingChanges[uri]
		matched := pending != nil && pending.content == content
		srv.docMu.Unlock()
		if matched {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending content did not become %q", content)
}

func TestA17_DebouncesDidChangeBeforeCompletion(t *testing.T) {
	if lspDocumentDebounce != 100*time.Millisecond {
		t.Fatalf("lspDocumentDebounce = %v, want 100ms", lspDocumentDebounce)
	}
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	events := make(chan lspTestWireEvent, 16)
	serverErr := make(chan error, 1)
	go func() {
		r := bufio.NewReader(serverR)
		for {
			msg, err := readLSPTestWireMessage(r)
			if err != nil {
				serverErr <- err
				return
			}
			method, _ := msg["method"].(string)
			params, _ := msg["params"].(map[string]interface{})
			events <- lspTestWireEvent{method: method, params: params, at: time.Now()}
			id, isRequest := msg["id"]
			if !isRequest {
				continue
			}
			result := interface{}(map[string]interface{}{})
			if method == "textDocument/completion" {
				result = map[string]interface{}{
					"items": []map[string]interface{}{{"label": "Final", "kind": 1, "insertText": "Final"}},
				}
			}
			if err := writeLSPTestWireMessage(serverW, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  result,
			}); err != nil {
				serverErr <- err
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = clientW.Close()
		_ = serverR.Close()
		_ = serverW.Close()
		_ = clientR.Close()
	})

	client := newJSONRPCClient(clientR, clientW)
	srv := &lspServer{
		client:         client,
		running:        true,
		docVersions:    make(map[string]int),
		docHashes:      make(map[string]string),
		docLastContent: make(map[string]string),
		docLastSync:    make(map[string]time.Time),
		pendingChanges: make(map[string]*pendingDocumentChange),
		syncKind:       2,
		diags:          make(map[string][]Diagnostic),
	}
	svc := NewLSPService("/tmp/ws")
	svc.mu.Lock()
	svc.servers["go"] = srv
	svc.mu.Unlock()

	base := LSPCompletionRequest{
		Language: "go",
		FilePath: "/tmp/ws/main.go",
		Content:  "package main\n",
	}
	if _, err := svc.syncDocument(base); err != nil {
		t.Fatalf("didOpen sync: %v", err)
	}
	waitLSPTestWireEvent(t, events, "textDocument/didOpen", time.Second)

	first := base
	first.Content = "package main\nvar x = 1\n"
	firstDone := make(chan error, 1)
	go func() {
		_, err := svc.syncDocument(first)
		firstDone <- err
	}()
	uri := pathToURI(base.FilePath)
	waitPendingDocumentContent(t, srv, uri, first.Content, time.Second)
	time.Sleep(25 * time.Millisecond)

	final := base
	final.Content = "package main\nvar x = 2\n"
	finalQueuedAt := time.Now()
	type completionResult struct {
		items []LSPCompletionItem
		err   error
	}
	completionDone := make(chan completionResult, 1)
	go func() {
		items, err := svc.GetCompletions(final)
		completionDone <- completionResult{items: items, err: err}
	}()
	waitPendingDocumentContent(t, srv, uri, final.Content, time.Second)

	select {
	case event := <-events:
		t.Fatalf("LSP method %q arrived before the debounce window", event.method)
	case <-time.After(70 * time.Millisecond):
	}
	changeEvent := waitLSPTestWireEvent(t, events, "textDocument/didChange", time.Second)
	if elapsed := changeEvent.at.Sub(finalQueuedAt); elapsed < 80*time.Millisecond {
		t.Fatalf("didChange arrived after %v, want approximately 100ms trailing debounce", elapsed)
	}
	changes, ok := changeEvent.params["contentChanges"].([]interface{})
	if !ok || len(changes) != 1 {
		t.Fatalf("contentChanges = %#v, want one change", changeEvent.params["contentChanges"])
	}
	change, ok := changes[0].(map[string]interface{})
	if !ok {
		t.Fatalf("content change = %T, want map", changes[0])
	}
	if text, _ := change["text"].(string); text != "var x = 2\n" {
		t.Fatalf("incremental text = %q, want final coalesced content", text)
	}
	if _, ok := change["range"].(map[string]interface{}); !ok {
		t.Fatalf("incremental change omitted range: %#v", change)
	}

	waitLSPTestWireEvent(t, events, "textDocument/completion", time.Second)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first sync: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first debounced sync did not finish")
	}
	select {
	case result := <-completionDone:
		if result.err != nil {
			t.Fatalf("GetCompletions: %v", result.err)
		}
		if len(result.items) != 1 || result.items[0].Label != "Final" {
			t.Fatalf("completion items = %+v, want Final", result.items)
		}
	case <-time.After(time.Second):
		t.Fatal("completion request did not finish")
	}

	select {
	case err := <-serverErr:
		if !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, io.EOF) {
			t.Fatalf("mock LSP server failed: %v", err)
		}
	default:
	}
}

type lspProtocolTestHandler func(map[string]interface{}) interface{}

func addLSPProtocolTestServer(
	t *testing.T,
	svc *LSPService,
	language string,
	handlers map[string]lspProtocolTestHandler,
) *lspServer {
	t.Helper()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	serverDone := make(chan struct{})

	go func() {
		defer close(serverDone)
		defer serverW.Close()
		r := bufio.NewReader(serverR)
		for {
			msg, err := readLSPTestWireMessage(r)
			if err != nil {
				return
			}
			id, isRequest := msg["id"]
			if !isRequest {
				continue
			}
			method, _ := msg["method"].(string)
			params, _ := msg["params"].(map[string]interface{})
			var result interface{} = map[string]interface{}{}
			if handler := handlers[method]; handler != nil {
				result = handler(params)
			}
			if err := writeLSPTestWireMessage(serverW, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  result,
			}); err != nil {
				return
			}
		}
	}()

	client := newJSONRPCClient(clientR, clientW)
	srv := &lspServer{
		client:         client,
		running:        true,
		docVersions:    make(map[string]int),
		docHashes:      make(map[string]string),
		docLastContent: make(map[string]string),
		docLastSync:    make(map[string]time.Time),
		pendingChanges: make(map[string]*pendingDocumentChange),
		syncKind:       1,
		diags:          make(map[string][]Diagnostic),
	}
	key := lspServerKey(language)
	if key == "" {
		t.Fatalf("unsupported test language %q", language)
	}
	svc.mu.Lock()
	svc.servers[key] = srv
	svc.mu.Unlock()

	t.Cleanup(func() {
		_ = clientW.Close()
		_ = serverW.Close()
		_ = serverR.Close()
		_ = clientR.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
		}
	})
	return srv
}

func lspProtocolTestMap(t *testing.T, value interface{}, name string) map[string]interface{} {
	t.Helper()
	result, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("%s = %T, want object", name, value)
	}
	return result
}

func lspProtocolTestSlice(t *testing.T, value interface{}, name string) []interface{} {
	t.Helper()
	result, ok := value.([]interface{})
	if !ok {
		t.Fatalf("%s = %T, want array", name, value)
	}
	return result
}

func TestA10_PythonRustDefinitionsAndInitialization(t *testing.T) {
	python, ok := lspDefinitionForLanguage("python")
	if !ok {
		t.Fatal("python LSP definition missing")
	}
	if len(python.candidates) != 1 || python.candidates[0].name != "pyright-langserver" {
		t.Fatalf("python candidates = %+v", python.candidates)
	}
	if !reflect.DeepEqual(python.args, []string{"--stdio"}) {
		t.Fatalf("python args = %v, want [--stdio]", python.args)
	}
	if !reflect.DeepEqual(python.extensions, []string{".py", ".pyi"}) {
		t.Fatalf("python extensions = %v", python.extensions)
	}
	if !python.workspaceNode {
		t.Fatal("python definition must search workspace node_modules/.bin")
	}

	rust, ok := lspDefinitionForLanguage("rust")
	if !ok {
		t.Fatal("rust LSP definition missing")
	}
	if len(rust.candidates) != 1 || rust.candidates[0].name != "rust-analyzer" {
		t.Fatalf("rust candidates = %+v", rust.candidates)
	}
	if len(rust.args) != 0 {
		t.Fatalf("rust args = %v, want none", rust.args)
	}
	if !reflect.DeepEqual(rust.extensions, []string{".rs"}) {
		t.Fatalf("rust extensions = %v", rust.extensions)
	}

	for input, want := range map[string]string{
		" python ": "python",
		"PY":       "python",
		"rust":     "rust",
	} {
		if got := lspServerKey(input); got != want {
			t.Errorf("lspServerKey(%q) = %q, want %q", input, got, want)
		}
	}
	if got := lspLanguageID("python", "types.pyi"); got != "python" {
		t.Errorf("python languageId = %q", got)
	}
	if got := lspLanguageID("rust", "main.rs"); got != "rust" {
		t.Errorf("rust languageId = %q", got)
	}

	pythonOptions := lspInitializationOptions("python", "")
	pythonSettings := lspProtocolTestMap(t, pythonOptions["python"], "python initializationOptions.python")
	analysis := lspProtocolTestMap(t, pythonSettings["analysis"], "python.analysis")
	for _, key := range []string{"autoImportCompletions", "autoSearchPaths", "useLibraryCodeForTypes"} {
		if enabled, _ := analysis[key].(bool); !enabled {
			t.Errorf("python.analysis.%s = %v, want true", key, analysis[key])
		}
	}
	if analysis["diagnosticMode"] != "workspace" {
		t.Errorf("python.analysis.diagnosticMode = %v", analysis["diagnosticMode"])
	}

	rustOptions := lspInitializationOptions("rust", "")
	cargo := lspProtocolTestMap(t, rustOptions["cargo"], "rust initializationOptions.cargo")
	if enabled, _ := cargo["allFeatures"].(bool); !enabled {
		t.Errorf("rust cargo.allFeatures = %v, want true", cargo["allFeatures"])
	}
	procMacro := lspProtocolTestMap(t, rustOptions["procMacro"], "rust initializationOptions.procMacro")
	if enabled, _ := procMacro["enable"].(bool); !enabled {
		t.Errorf("rust procMacro.enable = %v, want true", procMacro["enable"])
	}
}

func TestA10_PythonRustMissingPATHDegradesGracefully(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, language := range []string{"python", "rust"} {
		t.Run(language, func(t *testing.T) {
			svc := NewLSPService(t.TempDir())
			if err := svc.StartLSPServer(language); err != nil {
				t.Fatalf("StartLSPServer(%q) = %v, want nil", language, err)
			}
			svc.mu.Lock()
			_, started := svc.servers[language]
			lastError := svc.lastErrors[language]
			svc.mu.Unlock()
			if started {
				t.Fatalf("%s server inserted despite missing executable", language)
			}
			if !strings.Contains(lastError, "not installed") {
				t.Fatalf("%s lastError = %q, want install guidance", language, lastError)
			}
			visible := false
			for _, status := range svc.DetectLSPServers() {
				if status.Language != language {
					continue
				}
				visible = true
				if status.Available || status.Running || status.LastError == "" {
					t.Fatalf("%s fallback status = %+v", language, status)
				}
			}
			if !visible {
				t.Fatalf("%s status hidden after explicit start attempt", language)
			}
		})
	}
}

func TestA11_GetSemanticTokenDataForwardsFullAndPreservesRelativeData(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	paramsCh := make(chan map[string]interface{}, 1)
	want := []int{0, 0, 5, 1, 0, 0, 6, 1, 3, 2, 2, 4, 7, 5, 1}
	addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
		"textDocument/semanticTokens/full": func(params map[string]interface{}) interface{} {
			paramsCh <- params
			return map[string]interface{}{"data": want, "resultId": "semantic-1"}
		},
	})
	filePath := filepath.Join(t.TempDir(), "main.go")
	got, err := svc.GetSemanticTokenData(LSPCompletionRequest{
		Language: "go",
		FilePath: filePath,
		Content:  "package main\n",
	})
	if err != nil {
		t.Fatalf("GetSemanticTokenData: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("semantic token data = %v, want %v", got, want)
	}
	params := <-paramsCh
	doc := lspProtocolTestMap(t, params["textDocument"], "semanticTokens.textDocument")
	if doc["uri"] != pathToURI(filePath) {
		t.Fatalf("semanticTokens uri = %v, want %q", doc["uri"], pathToURI(filePath))
	}
}

func TestLSP_SemanticTokensRemapFullUsingServerLegend(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	srv := addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
		"textDocument/semanticTokens/full": func(map[string]interface{}) interface{} {
			return map[string]interface{}{
				"resultId": "full-1",
				"data":     []int{0, 0, 3, 0, 7, 0, 4, 2, 1, 2},
			}
		},
	})
	srv.setSemanticTokenLegend(
		[]string{"function", "serverOnly"},
		[]string{"readonly", "serverOnly", "declaration"},
	)
	result, err := svc.GetSemanticTokensDelta(LSPCompletionRequest{
		Language: "go",
		FilePath: filepath.Join(t.TempDir(), "main.go"),
		Content:  "package main\n",
	}, "")
	if err != nil {
		t.Fatalf("GetSemanticTokensDelta: %v", err)
	}
	want := []int{0, 0, 3, 12, 5, 0, 4, 2, 8, 0}
	if result.ResultID != "full-1" || !reflect.DeepEqual(result.Data, want) {
		t.Fatalf("remapped full semantic tokens = %+v, want %v", result, want)
	}
}

func TestLSP_SemanticTokensDeltaUsesAbsoluteEditOffset(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	paramsCh := make(chan map[string]interface{}, 1)
	srv := addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
		"textDocument/semanticTokens/full/delta": func(params map[string]interface{}) interface{} {
			paramsCh <- params
			return map[string]interface{}{
				"resultId": "delta-2",
				"edits": []map[string]interface{}{{
					"start":       3,
					"deleteCount": 2,
					"data":        []int{0, 7},
				}},
			}
		},
	})
	srv.setSemanticTokenLegend(
		[]string{"function"},
		[]string{"readonly", "serverOnly", "declaration"},
	)
	result, err := svc.GetSemanticTokensDelta(LSPCompletionRequest{
		Language: "go",
		FilePath: filepath.Join(t.TempDir(), "main.go"),
		Content:  "package main\n",
	}, "delta-1")
	if err != nil {
		t.Fatalf("GetSemanticTokensDelta: %v", err)
	}
	params := <-paramsCh
	if params["previousResultId"] != "delta-1" {
		t.Fatalf("previousResultId = %#v", params["previousResultId"])
	}
	if result.ResultID != "delta-2" || len(result.Edits) != 1 {
		t.Fatalf("delta result = %+v", result)
	}
	edit := result.Edits[0]
	if edit.Start != 3 || edit.DeleteCount != 2 || !reflect.DeepEqual(edit.Data, []int{12, 5}) {
		t.Fatalf("remapped delta edit = %+v", edit)
	}
}

func TestLSP_SemanticTokensMalformedDeltaFallsBackToFull(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	var fullCalls int
	srv := addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
		"textDocument/semanticTokens/full/delta": func(map[string]interface{}) interface{} {
			return map[string]interface{}{
				"resultId": "bad-delta",
				"edits": []map[string]interface{}{{
					"start":       0,
					"deleteCount": 0,
					"data":        []int{-1},
				}},
			}
		},
		"textDocument/semanticTokens/full": func(map[string]interface{}) interface{} {
			fullCalls++
			return map[string]interface{}{"resultId": "full-after-bad-delta", "data": []int{0, 0, 2, 0, 0}}
		},
	})
	srv.setSemanticTokenLegend([]string{"function"}, nil)
	result, err := svc.GetSemanticTokensDelta(LSPCompletionRequest{
		Language: "go",
		FilePath: filepath.Join(t.TempDir(), "main.go"),
		Content:  "package main\n",
	}, "previous")
	if err != nil {
		t.Fatalf("GetSemanticTokensDelta: %v", err)
	}
	if fullCalls != 1 || result.ResultID != "full-after-bad-delta" || !reflect.DeepEqual(result.Data, []int{0, 0, 2, 12, 0}) {
		t.Fatalf("delta fallback result = %+v, full calls = %d", result, fullCalls)
	}
}

func TestLSP_SemanticTokensMalformedFullSoftFails(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
		"textDocument/semanticTokens/full": func(map[string]interface{}) interface{} {
			return map[string]interface{}{"resultId": "bad-full", "data": []int{0, 0, 2, 1}}
		},
	})
	result, err := svc.GetSemanticTokensDelta(LSPCompletionRequest{
		Language: "go",
		FilePath: filepath.Join(t.TempDir(), "main.go"),
		Content:  "package main\n",
	}, "")
	if err != nil {
		t.Fatalf("GetSemanticTokensDelta: %v", err)
	}
	if result.ResultID != "" || result.Data != nil || result.Edits != nil {
		t.Fatalf("malformed full did not soft fail: %+v", result)
	}
}

func TestA12_InlayHintsPayloadParsingAndResolve(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	requestParams := make(chan map[string]interface{}, 1)
	resolveParams := make(chan map[string]interface{}, 1)
	filePath := filepath.Join(t.TempDir(), "main.go")
	labelLocation := map[string]interface{}{
		"uri": pathToURI(filePath),
		"range": map[string]interface{}{
			"start": map[string]int{"line": 4, "character": 7},
			"end":   map[string]int{"line": 4, "character": 8},
		},
	}
	addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
		"textDocument/inlayHint": func(params map[string]interface{}) interface{} {
			requestParams <- params
			return []map[string]interface{}{{
				"position": map[string]int{"line": 4, "character": 7},
				"label": []map[string]interface{}{
					{"value": "value", "tooltip": "label tooltip", "location": labelLocation},
					{"value": ": int"},
				},
				"kind":    1,
				"tooltip": map[string]interface{}{"kind": "markdown", "value": "**type**"},
				"textEdits": []map[string]interface{}{{
					"range": map[string]interface{}{
						"start": map[string]int{"line": 4, "character": 7},
						"end":   map[string]int{"line": 4, "character": 7},
					},
					"newText": "int ",
				}},
				"paddingLeft":  true,
				"paddingRight": true,
				"data":         map[string]interface{}{"token": "hint-1"},
			}}
		},
		"inlayHint/resolve": func(params map[string]interface{}) interface{} {
			resolveParams <- params
			return map[string]interface{}{
				"position": map[string]int{"line": 4, "character": 7},
				"label":    []map[string]interface{}{{"value": "value"}, {"value": ": resolved"}},
				"kind":     1,
				"tooltip":  map[string]interface{}{"kind": "markdown", "value": "resolved tooltip"},
				"textEdits": []map[string]interface{}{{
					"range": map[string]interface{}{
						"start": map[string]int{"line": 4, "character": 7},
						"end":   map[string]int{"line": 4, "character": 7},
					},
					"newText": "resolved ",
				}},
				"paddingLeft":  true,
				"paddingRight": true,
				"data":         map[string]interface{}{"token": "hint-1"},
			}
		},
	})

	raw, err := svc.GetInlayHintsRaw(LSPCompletionRequest{
		Language: "go",
		FilePath: filePath,
		Line:     3,
		EndLine:  9,
		Content:  "package main\nfunc main() {}\n",
	})
	if err != nil {
		t.Fatalf("GetInlayHintsRaw: %v", err)
	}
	params := <-requestParams
	rangeParams := lspProtocolTestMap(t, params["range"], "inlayHint.range")
	start := lspProtocolTestMap(t, rangeParams["start"], "inlayHint.range.start")
	end := lspProtocolTestMap(t, rangeParams["end"], "inlayHint.range.end")
	if start["line"] != float64(3) || start["character"] != float64(0) {
		t.Fatalf("inlay start = %#v", start)
	}
	if end["line"] != float64(9) || end["character"] != float64(0) {
		t.Fatalf("inlay end = %#v", end)
	}

	hints := parseInlayHints(raw)
	if len(hints) != 1 {
		t.Fatalf("inlay hints = %+v", hints)
	}
	hint := hints[0]
	if hint.Line != 4 || hint.Column != 7 || hint.Label != "value: int" || hint.Kind != 1 {
		t.Fatalf("parsed inlay hint = %+v", hint)
	}
	if !hint.PaddingLeft || !hint.PaddingRight || len(hint.TextEdits) != 1 || hint.TextEdits[0].NewText != "int " {
		t.Fatalf("inlay optional fields = %+v", hint)
	}
	if len(hint.RawLabel) == 0 || !bytes.Contains(hint.RawLabel, []byte("location")) {
		t.Fatalf("raw label lost label-part fields: %s", hint.RawLabel)
	}
	var hintData map[string]interface{}
	if err := json.Unmarshal(hint.Data, &hintData); err != nil || hintData["token"] != "hint-1" {
		t.Fatalf("inlay data = %s, err=%v", hint.Data, err)
	}

	resolved, err := svc.ResolveInlayHint("go", hint)
	if err != nil {
		t.Fatalf("ResolveInlayHint: %v", err)
	}
	resolvePayload := <-resolveParams
	resolveData := lspProtocolTestMap(t, resolvePayload["data"], "inlayHint/resolve.data")
	if resolveData["token"] != "hint-1" {
		t.Fatalf("resolve data = %#v", resolveData)
	}
	if _, ok := resolvePayload["label"].([]interface{}); !ok {
		t.Fatalf("resolve label = %T, want original label parts", resolvePayload["label"])
	}
	if resolved.Label != "value: resolved" || len(resolved.TextEdits) != 1 || resolved.TextEdits[0].NewText != "resolved " {
		t.Fatalf("resolved inlay hint = %+v", resolved)
	}
}

func TestA13_WorkspaceSymbolsParallelRawAggregation(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	type startedRequest struct {
		language string
		query    interface{}
	}
	started := make(chan startedRequest, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseServers := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseServers()

	addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
		"workspace/symbol": func(params map[string]interface{}) interface{} {
			started <- startedRequest{language: "go", query: params["query"]}
			<-release
			return []map[string]interface{}{{
				"name": "GoSymbol",
				"kind": 12,
				"tags": []int{1},
				"location": map[string]interface{}{
					"uri": pathToURI(filepath.Join(t.TempDir(), "go.go")),
					"range": map[string]interface{}{
						"start": map[string]int{"line": 1, "character": 2},
						"end":   map[string]int{"line": 1, "character": 4},
					},
				},
				"data": map[string]interface{}{"server": "go"},
			}}
		},
	})
	addLSPProtocolTestServer(t, svc, "rust", map[string]lspProtocolTestHandler{
		"workspace/symbol": func(params map[string]interface{}) interface{} {
			started <- startedRequest{language: "rust", query: params["query"]}
			<-release
			return []map[string]interface{}{{
				"name":     "RustSymbol",
				"kind":     23,
				"tags":     []int{1},
				"location": map[string]interface{}{"uri": pathToURI(filepath.Join(t.TempDir(), "lib.rs"))},
				"data":     map[string]interface{}{"server": "rust"},
			}}
		},
	})

	type rawResult struct {
		raw json.RawMessage
		err error
	}
	resultCh := make(chan rawResult, 1)
	go func() {
		raw, err := svc.GetWorkspaceSymbolsRaw("Needle")
		resultCh <- rawResult{raw: raw, err: err}
	}()

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case request := <-started:
			seen[request.language] = true
			if request.query != "Needle" {
				t.Fatalf("%s query = %v", request.language, request.query)
			}
		case <-time.After(time.Second):
			t.Fatalf("workspace/symbol requests were not parallel; started=%v", seen)
		}
	}
	releaseServers()

	var result rawResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("GetWorkspaceSymbolsRaw did not finish")
	}
	if result.err != nil {
		t.Fatalf("GetWorkspaceSymbolsRaw: %v", result.err)
	}
	var symbols []map[string]json.RawMessage
	if err := json.Unmarshal(result.raw, &symbols); err != nil {
		t.Fatalf("unmarshal raw workspace symbols: %v\n%s", err, result.raw)
	}
	if len(symbols) != 2 {
		t.Fatalf("raw symbols = %s", result.raw)
	}
	byName := make(map[string]map[string]json.RawMessage, len(symbols))
	for _, symbol := range symbols {
		var name string
		if err := json.Unmarshal(symbol["name"], &name); err != nil {
			t.Fatalf("symbol name: %v", err)
		}
		byName[name] = symbol
	}
	for name, server := range map[string]string{"GoSymbol": "go", "RustSymbol": "rust"} {
		symbol := byName[name]
		if symbol == nil {
			t.Fatalf("missing %s in %s", name, result.raw)
		}
		if len(symbol["tags"]) == 0 || len(symbol["location"]) == 0 || len(symbol["data"]) == 0 {
			t.Fatalf("%s lost raw fields: %s", name, result.raw)
		}
		var data map[string]interface{}
		if err := json.Unmarshal(symbol["data"], &data); err != nil || data["server"] != server {
			t.Fatalf("%s data = %s, err=%v", name, symbol["data"], err)
		}
	}
	var rustLocation map[string]json.RawMessage
	if err := json.Unmarshal(byName["RustSymbol"]["location"], &rustLocation); err != nil {
		t.Fatalf("rust location: %v", err)
	}
	if _, hasRange := rustLocation["range"]; hasRange {
		t.Fatalf("raw WorkspaceSymbol location shape changed: %s", byName["RustSymbol"]["location"])
	}
}

func TestA14_CallHierarchyCompatibilityWrappers(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	incomingParams := make(chan map[string]interface{}, 1)
	outgoingParams := make(chan map[string]interface{}, 1)
	root := t.TempDir()
	mainPath := filepath.Join(root, "main.go")
	callerPath := filepath.Join(root, "caller.go")
	calleePath := filepath.Join(root, "callee.go")
	itemWire := func(name, uri string) map[string]interface{} {
		return map[string]interface{}{
			"name": name,
			"kind": 12,
			"uri":  uri,
			"range": map[string]interface{}{
				"start": map[string]int{"line": 1, "character": 0},
				"end":   map[string]int{"line": 1, "character": 5},
			},
			"selectionRange": map[string]interface{}{
				"start": map[string]int{"line": 1, "character": 0},
				"end":   map[string]int{"line": 1, "character": 5},
			},
			"data": map[string]interface{}{"owner": name},
		}
	}
	addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
		"callHierarchy/incomingCalls": func(params map[string]interface{}) interface{} {
			incomingParams <- params
			return []map[string]interface{}{{
				"from": itemWire("Caller", pathToURI(callerPath)),
				"fromRanges": []map[string]interface{}{{
					"start": map[string]int{"line": 2, "character": 1},
					"end":   map[string]int{"line": 2, "character": 4},
				}},
			}}
		},
		"callHierarchy/outgoingCalls": func(params map[string]interface{}) interface{} {
			outgoingParams <- params
			return []map[string]interface{}{{
				"to": itemWire("Callee", pathToURI(calleePath)),
				"fromRanges": []map[string]interface{}{{
					"start": map[string]int{"line": 3, "character": 1},
					"end":   map[string]int{"line": 3, "character": 4},
				}},
			}}
		},
	})
	req := LSPCompletionRequest{Language: "go", FilePath: mainPath, Content: "package main\n"}
	item := LSPCallHierarchyItem{
		Name: "Main", Kind: 12, FilePath: mainPath,
		EndLine: 1, EndColumn: 4, SelectionEndLn: 1, SelectionEndCo: 4,
		Data: json.RawMessage(`{"opaque":7}`),
	}
	incoming, err := svc.GetCallHierarchyIncoming(req, item)
	if err != nil || len(incoming) != 1 || incoming[0].From.Name != "Caller" {
		t.Fatalf("GetCallHierarchyIncoming = %+v, err=%v", incoming, err)
	}
	outgoing, err := svc.GetCallHierarchyOutgoing(req, item)
	if err != nil || len(outgoing) != 1 || outgoing[0].To.Name != "Callee" {
		t.Fatalf("GetCallHierarchyOutgoing = %+v, err=%v", outgoing, err)
	}
	if len(outgoing[0].FromRanges) != 1 || outgoing[0].FromRanges[0].FilePath != mainPath {
		t.Fatalf("outgoing fromRanges = %+v, want source file %q", outgoing[0].FromRanges, mainPath)
	}
	for name, params := range map[string]map[string]interface{}{
		"incoming": <-incomingParams,
		"outgoing": <-outgoingParams,
	} {
		wireItem := lspProtocolTestMap(t, params["item"], name+".item")
		data := lspProtocolTestMap(t, wireItem["data"], name+".item.data")
		if data["opaque"] != float64(7) {
			t.Fatalf("%s data = %#v", name, data)
		}
	}
}

func TestA15_OrganizeImportsProtocolVariants(t *testing.T) {
	t.Run("edit and command", func(t *testing.T) {
		svc := NewLSPService(t.TempDir())
		codeActionParams := make(chan map[string]interface{}, 1)
		executeParams := make(chan map[string]interface{}, 1)
		filePath := filepath.Join(t.TempDir(), "main.go")
		uri := pathToURI(filePath)
		addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
			"textDocument/codeAction": func(params map[string]interface{}) interface{} {
				codeActionParams <- params
				return []map[string]interface{}{{
					"title": "Organize Imports",
					"kind":  "source.organizeImports",
					"edit": map[string]interface{}{
						"changes": map[string]interface{}{
							uri: []map[string]interface{}{{
								"range": map[string]interface{}{
									"start": map[string]int{"line": 0, "character": 0},
									"end":   map[string]int{"line": 0, "character": 0},
								},
								"newText": "import \"fmt\"\n",
							}},
						},
					},
					"command": map[string]interface{}{
						"title": "After Edit", "command": "organize.afterEdit", "arguments": []interface{}{"done"},
					},
				}}
			},
			"workspace/executeCommand": func(params map[string]interface{}) interface{} {
				executeParams <- params
				return nil
			},
		})
		edits, err := svc.OrganizeImports(LSPCompletionRequest{
			Language: "go", FilePath: filePath, Content: "package main\n",
		})
		if err != nil || len(edits) != 1 || edits[0].NewText != "import \"fmt\"\n" {
			t.Fatalf("OrganizeImports edits = %+v, err=%v", edits, err)
		}
		contextParams := lspProtocolTestMap(t, (<-codeActionParams)["context"], "codeAction.context")
		only := lspProtocolTestSlice(t, contextParams["only"], "codeAction.context.only")
		if len(only) != 1 || only[0] != "source.organizeImports" {
			t.Fatalf("codeAction only = %#v", only)
		}
		exec := <-executeParams
		if exec["command"] != "organize.afterEdit" {
			t.Fatalf("executeCommand = %#v", exec)
		}
	})

	t.Run("direct Command", func(t *testing.T) {
		svc := NewLSPService(t.TempDir())
		executeParams := make(chan map[string]interface{}, 1)
		filePath := filepath.Join(t.TempDir(), "main.go")
		addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
			"textDocument/codeAction": func(params map[string]interface{}) interface{} {
				return []map[string]interface{}{{
					"title": "Direct Organize", "command": "organize.direct", "arguments": []interface{}{"arg"},
				}}
			},
			"workspace/executeCommand": func(params map[string]interface{}) interface{} {
				executeParams <- params
				return nil
			},
		})
		edits, err := svc.OrganizeImports(LSPCompletionRequest{
			Language: "go", FilePath: filePath, Content: "package main\n",
		})
		if err != nil || len(edits) != 0 {
			t.Fatalf("direct Command edits = %+v, err=%v", edits, err)
		}
		exec := <-executeParams
		if exec["command"] != "organize.direct" {
			t.Fatalf("direct executeCommand = %#v", exec)
		}
	})

	t.Run("data only resolve", func(t *testing.T) {
		svc := NewLSPService(t.TempDir())
		resolveParams := make(chan map[string]interface{}, 1)
		filePath := filepath.Join(t.TempDir(), "main.go")
		uri := pathToURI(filePath)
		addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
			"textDocument/codeAction": func(params map[string]interface{}) interface{} {
				return []map[string]interface{}{{
					"title": "Lazy Organize", "kind": "source.organizeImports", "data": map[string]interface{}{"token": "action-1"},
				}}
			},
			"codeAction/resolve": func(params map[string]interface{}) interface{} {
				resolveParams <- params
				edit := map[string]interface{}{
					"documentChanges": []interface{}{
						map[string]interface{}{
							"textDocument": map[string]interface{}{"uri": uri, "version": 1},
							"edits": []interface{}{
								map[string]interface{}{
									"range": map[string]interface{}{
										"start": map[string]int{"line": 0, "character": 0},
										"end":   map[string]int{"line": 0, "character": 0},
									},
									"newText": "// organized\n",
								},
							},
						},
					},
				}
				return map[string]interface{}{
					"title": "Lazy Organize",
					"kind":  "source.organizeImports",
					"data":  map[string]interface{}{"token": "action-1"},
					"edit":  edit,
				}
			},
		})
		edits, err := svc.OrganizeImports(LSPCompletionRequest{
			Language: "go", FilePath: filePath, Content: "package main\n",
		})
		if err != nil || len(edits) != 1 || edits[0].NewText != "// organized\n" {
			t.Fatalf("resolved organize edits = %+v, err=%v", edits, err)
		}
		resolvedPayload := <-resolveParams
		data := lspProtocolTestMap(t, resolvedPayload["data"], "codeAction/resolve.data")
		if data["token"] != "action-1" {
			t.Fatalf("codeAction resolve payload = %#v", resolvedPayload)
		}
	})
}

func TestA16_CodeLensUnresolvedResolvePayloadAndResult(t *testing.T) {
	svc := NewLSPService(t.TempDir())
	requestParams := make(chan map[string]interface{}, 1)
	resolveParams := make(chan map[string]interface{}, 1)
	filePath := filepath.Join(t.TempDir(), "main.go")
	addLSPProtocolTestServer(t, svc, "go", map[string]lspProtocolTestHandler{
		"textDocument/codeLens": func(params map[string]interface{}) interface{} {
			requestParams <- params
			return []map[string]interface{}{
				{
					"range": map[string]interface{}{
						"start": map[string]int{"line": 5, "character": 1},
						"end":   map[string]int{"line": 5, "character": 8},
					},
					"data": map[string]interface{}{"token": "lens-1"},
				},
			}
		},
		"codeLens/resolve": func(params map[string]interface{}) interface{} {
			resolveParams <- params
			return map[string]interface{}{
				"range": params["range"],
				"data":  params["data"],
				"command": map[string]interface{}{
					"title": "3 references", "command": "editor.showReferences", "arguments": []interface{}{pathToURI(filePath), 5},
				},
			}
		},
	})
	lenses, err := svc.GetCodeLenses(LSPCompletionRequest{
		Language: "go", FilePath: filePath, Content: "package main\n",
	})
	if err != nil {
		t.Fatalf("GetCodeLenses: %v", err)
	}
	request := <-requestParams
	doc := lspProtocolTestMap(t, request["textDocument"], "codeLens.textDocument")
	if doc["uri"] != pathToURI(filePath) {
		t.Fatalf("codeLens uri = %v", doc["uri"])
	}
	resolvedPayload := <-resolveParams
	if _, hasCommand := resolvedPayload["command"]; hasCommand {
		t.Fatalf("unresolved code lens unexpectedly included command: %#v", resolvedPayload)
	}
	data := lspProtocolTestMap(t, resolvedPayload["data"], "codeLens/resolve.data")
	if data["token"] != "lens-1" {
		t.Fatalf("codeLens resolve data = %#v", data)
	}
	rangePayload := lspProtocolTestMap(t, resolvedPayload["range"], "codeLens/resolve.range")
	start := lspProtocolTestMap(t, rangePayload["start"], "codeLens/resolve.range.start")
	end := lspProtocolTestMap(t, rangePayload["end"], "codeLens/resolve.range.end")
	if start["line"] != float64(5) || start["character"] != float64(1) || end["character"] != float64(8) {
		t.Fatalf("codeLens resolve range = %#v", rangePayload)
	}
	if len(lenses) != 1 {
		t.Fatalf("resolved code lenses = %+v", lenses)
	}
	lens := lenses[0]
	if lens.Line != 5 || lens.Column != 1 || lens.EndLine != 5 || lens.EndColumn != 8 ||
		lens.Label != "3 references" || lens.Command != "editor.showReferences" || len(lens.Arguments) != 2 {
		t.Fatalf("resolved code lens = %+v", lens)
	}
	var resultData map[string]interface{}
	if err := json.Unmarshal(lens.Data, &resultData); err != nil || resultData["token"] != "lens-1" {
		t.Fatalf("resolved code lens data = %s, err=%v", lens.Data, err)
	}
}
