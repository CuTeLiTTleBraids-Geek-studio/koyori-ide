package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestConnectNodeCDPRejectsMalformedLocalAddressBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port := parsed.Port()

	for _, hostPort := range []string{
		"localhost:" + port + "/json",
		"user@localhost:" + port,
	} {
		if _, err := connectNodeCDP(hostPort, 25*time.Millisecond); err == nil {
			t.Fatalf("connectNodeCDP(%q) expected an error", hostPort)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("inspector requests = %d, want 0", calls.Load())
	}
}

func TestConnectNodeCDPRejectsRemoteAndInvalidAddresses(t *testing.T) {
	tests := []string{
		"",
		"localhost",
		"localhost:0",
		"localhost:65536",
		"example.com:9229",
		"192.0.2.1:9229",
		"10.0.0.1:9229",
		"http://localhost:9229",
	}
	for _, hostPort := range tests {
		t.Run(strings.ReplaceAll(hostPort, "/", "_"), func(t *testing.T) {
			_, err := connectNodeCDP(hostPort, 0)
			if err == nil || !strings.Contains(err.Error(), "invalid node inspector address") {
				t.Fatalf("connectNodeCDP(%q) error = %v, want validation error", hostPort, err)
			}
		})
	}
}

func TestConnectNodeCDPInspectorRequestHonorsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = connectNodeCDP(parsed.Host, 50*time.Millisecond)
	if err == nil {
		t.Fatal("connectNodeCDP expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("inspector request ignored timeout: %v", elapsed)
	}
}

func TestConnectNodeCDPRejectsRemoteWebSocketURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"webSocketDebuggerUrl":"ws://192.0.2.1:9229/devtools/page/1"}]`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = connectNodeCDP(parsed.Host, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "invalid node inspector websocket") {
		t.Fatalf("connectNodeCDP error = %v, want websocket validation error", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("remote websocket was dialed before rejection: %v", elapsed)
	}
}

func TestConnectNodeCDPBoundsInspectorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1024*1024+1)))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	_, err = connectNodeCDP(parsed.Host, time.Second)
	if err == nil || !strings.Contains(err.Error(), "exceeds 1048576") {
		t.Fatalf("connectNodeCDP error = %v, want response size error", err)
	}
}

func TestConnectNodeCDPWebSocketHandshakeRejectsRedirect(t *testing.T) {
	var redirectedRequests atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedRequests.Add(1)
		http.Error(w, "must not be reached", http.StatusForbidden)
	}))
	defer redirectTarget.Close()

	handshake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
	}))
	defer handshake.Close()
	handshakeURL, err := url.Parse(handshake.URL)
	if err != nil {
		t.Fatal(err)
	}

	inspector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"webSocketDebuggerUrl":"ws://` + handshakeURL.Host + `/devtools/page/1"}]`))
	}))
	defer inspector.Close()
	inspectorURL, err := url.Parse(inspector.URL)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := connectNodeCDP(inspectorURL.Host, time.Second); err == nil {
		t.Fatal("connectNodeCDP expected redirected handshake to fail")
	}
	if got := redirectedRequests.Load(); got != 0 {
		t.Fatalf("redirect target requests = %d, want 0", got)
	}
}

func TestNodeCDPProtocolLogHookCoversRequestAndResponse(t *testing.T) {
	d := NewDebugService()
	d.SetDebugProtocolLog(false)
	events := make(chan string, 4)
	d.protocolMu.Lock()
	d.protocolEmitter = func(_ string, payload any) {
		events <- payload.(map[string]any)["text"].(string)
	}
	d.protocolMu.Unlock()

	client := &nodeCDPClient{
		pending: make(map[int64]chan cdpResponse),
		callResultHook: func(_ context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
			if method != "Runtime.evaluate" || params["expression"] != "secret-expression" {
				t.Fatalf("unexpected hook call: method=%q params=%v", method, params)
			}
			return json.RawMessage(`{"result":{"type":"string","value":"secret-value"}}`), nil
		},
	}
	client.setProtocolLogger(d.emitDebugProtocol)

	if _, err := client.callResult("Runtime.evaluate", map[string]interface{}{"expression": "secret-expression"}); err != nil {
		t.Fatalf("hook call while logging disabled: %v", err)
	}
	select {
	case <-events:
		t.Fatal("cdp protocol event emitted while logging was disabled")
	default:
	}

	d.SetDebugProtocolLog(true)
	if _, err := client.callResult("Runtime.evaluate", map[string]interface{}{"expression": "secret-expression"}); err != nil {
		t.Fatalf("hook call while logging enabled: %v", err)
	}
	seenOutbound, seenInbound := false, false
	for i := 0; i < 2; i++ {
		select {
		case text := <-events:
			seenOutbound = seenOutbound || strings.HasPrefix(text, "CDP -> ")
			seenInbound = seenInbound || strings.HasPrefix(text, "CDP <- ")
			if strings.Contains(text, "secret-expression") || strings.Contains(text, "secret-value") {
				t.Fatalf("cdp protocol log leaked sensitive data: %s", text)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for cdp protocol events")
		}
	}
	if !seenOutbound || !seenInbound {
		t.Fatalf("cdp protocol events outbound=%v inbound=%v", seenOutbound, seenInbound)
	}
}

func TestNodeCDPUnexpectedExitUnblocksPendingCallAndCleansState(t *testing.T) {
	requestSeen := make(chan struct{})
	serverDone := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			serverDone <- err
			return
		}
		_, _, err = conn.Read(context.Background())
		if err == nil {
			close(requestSeen)
			err = conn.CloseNow()
		}
		serverDone <- err
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial test websocket: %v", err)
	}
	client := &nodeCDPClient{
		conn:       conn,
		pending:    make(map[int64]chan cdpResponse),
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	go client.readLoop()

	callDone := make(chan error, 1)
	go func() {
		_, err := client.callResult("Runtime.evaluate", map[string]interface{}{"expression": "1+1"})
		callDone <- err
	}()
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("cdp server did not receive the request")
	}
	select {
	case err := <-callDone:
		if err == nil || !strings.Contains(err.Error(), "cdp connection closed") {
			t.Fatalf("pending call error = %v, want closed connection", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending cdp call did not unblock after peer exit")
	}
	client.mu.Lock()
	closed := client.closed
	pending := len(client.pending)
	client.mu.Unlock()
	if !closed || pending != 0 {
		t.Fatalf("client state after exit: closed=%v pending=%d", closed, pending)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close after peer exit: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("websocket server: %v", err)
	}
}

func TestNodeCDPCloseWaitsForReaderAndUnblocksPendingCall(t *testing.T) {
	requestSeen := make(chan struct{})
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			close(serverDone)
			return
		}
		if _, _, err := conn.Read(context.Background()); err == nil {
			close(requestSeen)
		}
		_, _, _ = conn.Read(context.Background())
		close(serverDone)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial test websocket: %v", err)
	}
	client := &nodeCDPClient{
		conn:       conn,
		pending:    make(map[int64]chan cdpResponse),
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	go client.readLoop()
	callDone := make(chan error, 1)
	go func() {
		_, err := client.callResult("Debugger.pause", map[string]interface{}{})
		callDone <- err
	}()
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("cdp server did not receive the request")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-client.readerDone:
	default:
		t.Fatal("Close returned before the cdp reader exited")
	}
	select {
	case err := <-callDone:
		if err == nil || !strings.Contains(err.Error(), "cdp closed") {
			t.Fatalf("pending call error = %v, want cdp closed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock the pending cdp call")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("websocket server did not observe client close")
	}
}

func TestNodeCDPPausedEventCanLoadLocalsWithoutBlockingReader(t *testing.T) {
	serverDone := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.CloseNow()
		paused := []byte(`{"method":"Debugger.paused","params":{"reason":"breakpoint","callFrames":[{"callFrameId":"frame-1","functionName":"main","url":"file:///tmp/main.js","location":{"lineNumber":2,"columnNumber":3},"scopeChain":[{"type":"local","object":{"objectId":"scope-1"}}]}]}}`)
		if err := conn.Write(context.Background(), websocket.MessageText, paused); err != nil {
			serverDone <- err
			return
		}
		_, requestData, err := conn.Read(context.Background())
		if err != nil {
			serverDone <- err
			return
		}
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(requestData, &request); err != nil {
			serverDone <- err
			return
		}
		if request.Method != "Runtime.getProperties" {
			serverDone <- fmt.Errorf("method = %q, want Runtime.getProperties", request.Method)
			return
		}
		response, err := json.Marshal(map[string]any{
			"id": request.ID,
			"result": map[string]any{"result": []map[string]any{{
				"name": "count", "value": map[string]any{"type": "number", "value": 2},
			}}},
		})
		if err == nil {
			err = conn.Write(context.Background(), websocket.MessageText, response)
		}
		if err != nil {
			serverDone <- err
			return
		}
		_, _, _ = conn.Read(context.Background())
		serverDone <- nil
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial test websocket: %v", err)
	}
	paused := make(chan []DebugVariable, 1)
	client := &nodeCDPClient{
		conn:       conn,
		pending:    make(map[int64]chan cdpResponse),
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
		onPaused: func(_ string, _ []DebugStackFrame, locals []DebugVariable) {
			paused <- locals
		},
	}
	go client.readLoop()
	select {
	case locals := <-paused:
		if len(locals) != 1 || locals[0].Name != "count" || locals[0].Value != "2" {
			t.Fatalf("paused locals = %+v", locals)
		}
	case <-time.After(time.Second):
		t.Fatal("paused event blocked waiting for a response on the reader goroutine")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("websocket server: %v", err)
	}
}

func TestNodeCDPPausedEventMergesESMModuleScopeWithoutBreakingShadowing(t *testing.T) {
	var locals []DebugVariable
	client := &nodeCDPClient{
		pending:    make(map[int64]chan cdpResponse),
		objectRefs: make(map[int]string),
		callResultHook: func(_ context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
			if method != "Runtime.getProperties" {
				t.Fatalf("method = %q", method)
			}
			switch params["objectId"] {
			case "local-scope":
				return json.RawMessage(`{"result":[{"name":"shadowed","value":{"type":"string","value":"local","description":"local"}},{"name":"localOnly","value":{"type":"number","value":1}}]}`), nil
			case "module-scope":
				return json.RawMessage(`{"result":[{"name":"shadowed","value":{"type":"string","value":"module","description":"module"}},{"name":"outer","value":{"type":"object","description":"Object","objectId":"outer-object"}}]}`), nil
			default:
				t.Fatalf("unexpected objectId: %v", params["objectId"])
				return nil, nil
			}
		},
		onPaused: func(_ string, _ []DebugStackFrame, values []DebugVariable) {
			locals = values
		},
	}
	client.handleEvent(cdpResponse{
		Method: "Debugger.paused",
		Params: json.RawMessage(`{"reason":"breakpoint","callFrames":[{"callFrameId":"frame-1","functionName":"main","url":"file:///tmp/main.mjs","location":{"lineNumber":2,"columnNumber":0},"scopeChain":[{"type":"local","object":{"objectId":"local-scope"}},{"type":"module","object":{"objectId":"module-scope"}},{"type":"global","object":{"objectId":"global-scope"}}]}]}`),
	})

	if len(locals) != 3 {
		t.Fatalf("locals = %+v", locals)
	}
	if locals[0].Name != "shadowed" || locals[0].Value != "local" {
		t.Fatalf("inner shadowed variable lost precedence: %+v", locals)
	}
	if locals[2].Name != "outer" || locals[2].VariablesReference == 0 {
		t.Fatalf("ESM module variable missing: %+v", locals)
	}
}

func TestNodeCDPEvaluateUsesPausedTopFrameAndAwaitsPromise(t *testing.T) {
	client := &nodeCDPClient{
		pending:           make(map[int64]chan cdpResponse),
		pausedCallFrameID: "frame-1",
		callResultHook: func(_ context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
			if method != "Debugger.evaluateOnCallFrame" {
				t.Fatalf("method = %q, want Debugger.evaluateOnCallFrame", method)
			}
			if params["callFrameId"] != "frame-1" || params["awaitPromise"] != true {
				t.Fatalf("evaluate params = %#v", params)
			}
			return json.RawMessage(`{"result":{"type":"number","value":2}}`), nil
		},
	}

	result, err := client.Evaluate("Promise.resolve(2)")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Value != "2" || result.Type != "number" {
		t.Fatalf("Evaluate result = %+v", result)
	}
}

func TestNodeCDPEvaluateUsesRuntimeWhenRunning(t *testing.T) {
	client := &nodeCDPClient{
		pending: make(map[int64]chan cdpResponse),
		callResultHook: func(_ context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
			if method != "Runtime.evaluate" || params["awaitPromise"] != true {
				t.Fatalf("method = %q params = %#v", method, params)
			}
			if _, exists := params["callFrameId"]; exists {
				t.Fatalf("running evaluation included callFrameId: %#v", params)
			}
			return json.RawMessage(`{"result":{"type":"undefined","description":"undefined"}}`), nil
		},
	}

	if _, err := client.Evaluate("work()"); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
}

func TestNodeCDPResumeClearsPausedFrameAfterSuccess(t *testing.T) {
	client := &nodeCDPClient{
		pending:           make(map[int64]chan cdpResponse),
		pausedCallFrameID: "frame-1",
		callResultHook: func(_ context.Context, method string, _ map[string]interface{}) (json.RawMessage, error) {
			if method != "Debugger.resume" {
				t.Fatalf("method = %q, want Debugger.resume", method)
			}
			return json.RawMessage(`{}`), nil
		},
	}

	if err := client.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	client.mu.Lock()
	frameID := client.pausedCallFrameID
	client.mu.Unlock()
	if frameID != "" {
		t.Fatalf("paused frame after resume = %q", frameID)
	}
}

func TestNodeCDPLogpointUsesLiteralMessageAndDoesNotPause(t *testing.T) {
	client := &nodeCDPClient{
		pending: make(map[int64]chan cdpResponse),
		callResultHook: func(_ context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
			if method != "Debugger.setBreakpointByUrl" {
				t.Fatalf("method = %q, want Debugger.setBreakpointByUrl", method)
			}
			condition, _ := params["condition"].(string)
			want := `(count > 1) && (console.log("value\"); globalThis.injected = true; //"), false)`
			if condition != want {
				t.Fatalf("condition = %q, want %q", condition, want)
			}
			return json.RawMessage(`{"breakpointId":"bp-1","locations":[{"lineNumber":4}]}`), nil
		},
	}

	id, verified, _, err := client.setBreakpointByURL(
		"/tmp/main.js",
		5,
		"count > 1",
		`value"); globalThis.injected = true; //`,
	)
	if err != nil {
		t.Fatalf("setBreakpointByURL: %v", err)
	}
	if id != "bp-1" || !verified {
		t.Fatalf("breakpoint id=%q verified=%v", id, verified)
	}
}

func TestNodeCDPRegularBreakpointConditionIsUnchanged(t *testing.T) {
	condition, err := nodeBreakpointCondition("count === 3", "")
	if err != nil {
		t.Fatalf("nodeBreakpointCondition: %v", err)
	}
	if condition != "count === 3" {
		t.Fatalf("condition = %q", condition)
	}
}
