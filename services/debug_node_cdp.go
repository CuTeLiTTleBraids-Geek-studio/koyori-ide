package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// nodeCDPClient speaks Chrome DevTools Protocol over WebSocket (prompt-13 13-A).
// Powers Node/TS debugging inside the same Debug panel as Delve DAP.
type nodeCDPClient struct {
	mu                       sync.Mutex
	writeMu                  sync.Mutex
	conn                     *websocket.Conn
	seq                      int64
	pending                  map[int64]chan cdpResponse
	closed                   bool
	closeErr                 error
	done                     chan struct{}
	doneOnce                 sync.Once
	readerDone               chan struct{}
	readerDoneOnce           sync.Once
	onPaused                 func(reason string, frames []DebugStackFrame, locals []DebugVariable)
	onAsyncStack             func(trace *nodeAsyncStackTrace, traceID *nodeStackTraceID)
	onBrowserConsole         func(BrowserConsoleEntry)
	onBrowserNetwork         func(BrowserNetworkEntry)
	pausedCallFrameID        string
	asyncStackTraceSupported bool
	callResultHook           func(context.Context, string, map[string]interface{}) (json.RawMessage, error)
	protocolLog              debugProtocolLogger
	// G14: local variablesReference -> CDP objectId so nested objects can be
	// expanded through the same GetVariables path as DAP. The map is per
	// connection and cleared on Close, so references from a previous run are
	// stale by construction (never honored).
	objectRefs    map[int]string
	nextObjectRef int
}

func (c *nodeCDPClient) setProtocolLogger(logger debugProtocolLogger) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.protocolLog = logger
	c.mu.Unlock()
}

func (c *nodeCDPClient) logProtocol(direction string, payload any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	logger := c.protocolLog
	c.mu.Unlock()
	if logger != nil {
		logger("CDP", direction, payload)
	}
}

type nodeStackTraceID struct {
	ID         string `json:"id"`
	DebuggerID string `json:"debuggerId,omitempty"`
}

type nodeAsyncCallFrame struct {
	FunctionName string `json:"functionName"`
	ScriptID     string `json:"scriptId"`
	URL          string `json:"url"`
	LineNumber   int    `json:"lineNumber"`
	ColumnNumber int    `json:"columnNumber"`
}

type nodeAsyncStackTrace struct {
	Description string               `json:"description"`
	CallFrames  []nodeAsyncCallFrame `json:"callFrames"`
	Parent      *nodeAsyncStackTrace `json:"parent,omitempty"`
	ParentID    *nodeStackTraceID    `json:"parentId,omitempty"`
}

type nodeAsyncStackContinuation struct {
	Trace    *nodeAsyncStackTrace
	ParentID *nodeStackTraceID
}

type cdpResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

const (
	maxNodeInspectorResponseSize = 1024 * 1024
	maxNodeInspectorHTTPTimeout  = 10 * time.Second
)

var errNodeInspectorResponseTooLarge = errors.New("node inspector response too large")

func validateNodeCDPHostPort(hostPort string) (string, error) {
	hostPort = strings.TrimSpace(hostPort)
	host, portText, err := net.SplitHostPort(hostPort)
	if err != nil {
		return "", err
	}
	if portText == "" || strings.Trim(portText, "0123456789") != "" {
		return "", fmt.Errorf("port must be a number")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("port must be in range 1..65535")
	}
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("host must be localhost or a loopback IP literal")
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func newNodeInspectorHTTPClient(timeout time.Duration) *http.Client {
	requestTimeout := timeout
	if requestTimeout <= 0 || requestTimeout > maxNodeInspectorHTTPTimeout {
		requestTimeout = maxNodeInspectorHTTPTimeout
	}
	return &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("node inspector redirects are not allowed")
		},
	}
}

func readNodeInspectorWebSocketURL(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Debug("close node inspector response body failed", "err", closeErr)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxNodeInspectorResponseSize+1))
	if err != nil {
		return "", fmt.Errorf("read node inspector response: %w", err)
	}
	if len(body) > maxNodeInspectorResponseSize {
		return "", fmt.Errorf("%w: exceeds %d byte limit", errNodeInspectorResponseTooLarge, maxNodeInspectorResponseSize)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("node inspector returned HTTP %d", resp.StatusCode)
	}
	var list []struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", fmt.Errorf("parse node inspector response: %w", err)
	}
	for _, entry := range list {
		if entry.WebSocketDebuggerURL != "" {
			return entry.WebSocketDebuggerURL, nil
		}
	}
	return "", nil
}

func normalizeNodeCDPWebSocketURL(rawURL, inspectorHostPort string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", err
	}
	if u.Scheme == "" {
		if u.Host != "" || u.User != nil {
			return "", fmt.Errorf("relative websocket URL must not contain a host")
		}
		if !strings.HasPrefix(u.Path, "/") {
			u.Path = "/" + u.Path
		}
		u.Scheme = "ws"
		u.Host = inspectorHostPort
		return u.String(), nil
	}
	if u.Scheme != "ws" || u.User != nil || u.Host == "" {
		return "", fmt.Errorf("websocket URL must use ws without credentials")
	}
	validatedHostPort, err := validateNodeCDPHostPort(u.Host)
	if err != nil {
		return "", err
	}
	u.Host = validatedHostPort
	return u.String(), nil
}

// connectNodeCDP waits for inspector HTTP, then opens the CDP websocket.
func connectNodeCDP(hostPort string, timeout time.Duration) (*nodeCDPClient, error) {
	return connectNodeCDPWithLogger(hostPort, timeout, nil)
}

func connectNodeCDPWithLogger(hostPort string, timeout time.Duration, logger debugProtocolLogger) (*nodeCDPClient, error) {
	validatedHostPort, err := validateNodeCDPHostPort(hostPort)
	if err != nil {
		return nil, fmt.Errorf("invalid node inspector address %q: %w", hostPort, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client := newNodeInspectorHTTPClient(timeout)
	paths := []string{"/json/list", "/json"}
	var lastErr error
	var wsURL string
	for ctx.Err() == nil {
		for _, path := range paths {
			endpoint := (&url.URL{Scheme: "http", Host: validatedHostPort, Path: path}).String()
			wsURL, err = readNodeInspectorWebSocketURL(ctx, client, endpoint)
			if err != nil {
				lastErr = err
				if errors.Is(err, errNodeInspectorResponseTooLarge) {
					return nil, fmt.Errorf("node inspector websocket not ready on %s: %w", validatedHostPort, err)
				}
				continue
			}
			if wsURL != "" {
				break
			}
		}
		if wsURL != "" {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(80 * time.Millisecond):
		}
	}
	if wsURL == "" {
		if lastErr != nil {
			return nil, fmt.Errorf("node inspector websocket not ready on %s: %w", validatedHostPort, lastErr)
		}
		return nil, fmt.Errorf("node inspector websocket not ready on %s", hostPort)
	}
	wsURL, err = normalizeNodeCDPWebSocketURL(wsURL, validatedHostPort)
	if err != nil {
		return nil, fmt.Errorf("invalid node inspector websocket: %w", err)
	}

	dialTimeout := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < dialTimeout {
		dialTimeout = time.Until(deadline)
	}
	if dialTimeout <= 0 {
		return nil, fmt.Errorf("node inspector websocket not ready on %s", validatedHostPort)
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, dialTimeout)
	defer dialCancel()
	conn, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPClient: newNodeInspectorHTTPClient(dialTimeout),
	})
	if err != nil {
		if resp != nil && resp.Body != nil {
			if closeErr := resp.Body.Close(); closeErr != nil {
				slog.Debug("close failed node inspector response", "err", closeErr)
			}
		}
		return nil, fmt.Errorf("cdp dial: %w", err)
	}
	c := &nodeCDPClient{
		conn:        conn,
		pending:     make(map[int64]chan cdpResponse),
		done:        make(chan struct{}),
		readerDone:  make(chan struct{}),
		protocolLog: logger,
		objectRefs:  make(map[int]string),
	}
	go c.readLoop()
	if err := c.call("Debugger.enable", map[string]interface{}{}); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	if err := c.call("Runtime.enable", map[string]interface{}{}); err != nil {
		slog.Debug("enable cdp runtime failed", "err", err)
	}
	if err := c.call("Debugger.setAsyncCallStackDepth", map[string]interface{}{"maxDepth": 32}); err == nil {
		c.mu.Lock()
		c.asyncStackTraceSupported = true
		c.mu.Unlock()
	} else {
		slog.Debug("enable cdp async stack traces failed", "err", err)
	}
	return c, nil
}

func (c *nodeCDPClient) SupportsAsyncStackTrace() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.asyncStackTraceSupported
}

func (c *nodeCDPClient) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	conn := c.conn
	if conn != nil && c.readerDone == nil {
		c.readerDone = make(chan struct{})
	}
	c.conn = nil
	if !c.closed {
		c.closed = true
	}
	c.ensureLifecycleLocked()
	c.doneOnce.Do(func() { close(c.done) })
	c.pending = make(map[int64]chan cdpResponse)
	// G14: references die with the connection — stale refs are never honored.
	c.objectRefs = make(map[int]string)
	c.nextObjectRef = 0
	readerDone := c.readerDone
	c.mu.Unlock()

	var closeErr error
	if conn != nil {
		if err := conn.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErr = fmt.Errorf("close cdp connection: %w", err)
		}
	}
	if readerDone != nil {
		<-readerDone
	}
	return closeErr
}

func (c *nodeCDPClient) ensureLifecycleLocked() {
	if c.done == nil {
		c.done = make(chan struct{})
	}
}

func (c *nodeCDPClient) failConnection(readErr error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = readErr
	conn := c.conn
	c.conn = nil
	c.ensureLifecycleLocked()
	c.doneOnce.Do(func() { close(c.done) })
	c.pending = make(map[int64]chan cdpResponse)
	c.mu.Unlock()
	if conn != nil {
		if err := conn.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Debug("close failed cdp connection", "err", err)
		}
	}
}

func (c *nodeCDPClient) call(method string, params map[string]interface{}) error {
	_, err := c.callResult(method, params)
	return err
}

func (c *nodeCDPClient) callResult(method string, params map[string]interface{}) (json.RawMessage, error) {
	return c.callResultContext(context.Background(), method, params)
}

func (c *nodeCDPClient) callResultContext(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("cdp %s request canceled: %w", method, err)
	}
	c.mu.Lock()
	hook := c.callResultHook
	closed := c.closed
	closeErr := c.closeErr
	c.mu.Unlock()
	if closed {
		if closeErr != nil {
			return nil, fmt.Errorf("cdp connection closed: %w", closeErr)
		}
		return nil, fmt.Errorf("cdp closed")
	}
	if hook != nil {
		request := map[string]interface{}{"method": method}
		if params != nil {
			request["params"] = params
		}
		c.logProtocol("->", request)
		result, err := hook(ctx, method, params)
		if err != nil {
			c.logProtocol("<-", map[string]interface{}{"method": method, "error": err.Error()})
			return nil, err
		}
		c.logProtocol("<-", map[string]interface{}{"method": method, "result": result})
		return result, nil
	}
	id := atomic.AddInt64(&c.seq, 1)
	ch := make(chan cdpResponse, 1)
	c.mu.Lock()
	if c.closed || c.conn == nil {
		closeErr := c.closeErr
		c.mu.Unlock()
		if closeErr != nil {
			return nil, fmt.Errorf("cdp connection closed: %w", closeErr)
		}
		return nil, fmt.Errorf("cdp closed")
	}
	c.ensureLifecycleLocked()
	c.pending[id] = ch
	conn := c.conn
	done := c.done
	c.mu.Unlock()

	msg := map[string]interface{}{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	data, err := json.Marshal(msg)
	if err != nil {
		c.mu.Lock()
		if c.pending[id] == ch {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		return nil, fmt.Errorf("encode cdp %s request: %w", method, err)
	}
	c.logProtocol("->", msg)
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c.writeMu.Lock()
	err = conn.Write(callCtx, websocket.MessageText, data)
	c.writeMu.Unlock()
	if err != nil {
		c.mu.Lock()
		if c.pending[id] == ch {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		return nil, fmt.Errorf("write cdp %s request: %w", method, err)
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("cdp %s request failed: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	case <-done:
		c.mu.Lock()
		closeErr := c.closeErr
		c.mu.Unlock()
		if closeErr != nil {
			return nil, fmt.Errorf("cdp connection closed: %w", closeErr)
		}
		return nil, fmt.Errorf("cdp closed")
	case <-callCtx.Done():
		c.mu.Lock()
		if c.pending[id] == ch {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("cdp %s request timed out: %w", method, callCtx.Err())
		}
		return nil, fmt.Errorf("cdp %s request canceled: %w", method, callCtx.Err())
	}
}

func (c *nodeCDPClient) readLoop() {
	c.mu.Lock()
	if c.readerDone == nil {
		c.readerDone = make(chan struct{})
	}
	readerDone := c.readerDone
	c.mu.Unlock()
	pausedEvents := make(chan cdpResponse, 1)
	pausedWorkerDone := make(chan struct{})
	go func() {
		defer close(pausedWorkerDone)
		for event := range pausedEvents {
			c.handleEvent(event)
		}
	}()
	defer func() {
		close(pausedEvents)
		<-pausedWorkerDone
		c.readerDoneOnce.Do(func() { close(readerDone) })
	}()
	for {
		c.mu.Lock()
		conn := c.conn
		closed := c.closed
		c.mu.Unlock()
		if closed || conn == nil {
			return
		}
		ctx := context.Background()
		_, data, err := conn.Read(ctx)
		if err != nil {
			c.failConnection(fmt.Errorf("read cdp message: %w", err))
			return
		}
		c.logProtocol("<-", debugProtocolRawMessage(data))
		var resp cdpResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			slog.Debug("decode cdp message failed", "err", err)
			continue
		}
		if resp.Method != "" {
			if resp.Method == "Debugger.paused" {
				select {
				case pausedEvents <- resp:
				default:
					slog.Debug("drop duplicate cdp paused event while one is pending")
				}
			} else {
				c.handleEvent(resp)
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- resp:
			default:
			}
		}
	}
}

func (c *nodeCDPClient) handleEvent(ev cdpResponse) {
	switch ev.Method {
	case "Runtime.consoleAPICalled", "Log.entryAdded":
		if entry, ok := parseBrowserConsoleEvent(ev); ok {
			c.mu.Lock()
			callback := c.onBrowserConsole
			c.mu.Unlock()
			if callback != nil {
				callback(entry)
			}
		}
		return
	case "Network.requestWillBeSent", "Network.responseReceived", "Network.loadingFailed":
		if entry, ok := parseBrowserNetworkEvent(ev); ok {
			c.mu.Lock()
			callback := c.onBrowserNetwork
			c.mu.Unlock()
			if callback != nil {
				callback(entry)
			}
		}
		return
	}
	if ev.Method != "Debugger.paused" {
		if ev.Method == "Debugger.resumed" {
			c.mu.Lock()
			c.pausedCallFrameID = ""
			c.mu.Unlock()
		}
		return
	}
	var params struct {
		Reason            string               `json:"reason"`
		AsyncStackTrace   *nodeAsyncStackTrace `json:"asyncStackTrace"`
		AsyncStackTraceID *nodeStackTraceID    `json:"asyncStackTraceId"`
		CallFrames        []struct {
			CallFrameID  string `json:"callFrameId"`
			FunctionName string `json:"functionName"`
			URL          string `json:"url"`
			Location     struct {
				ScriptID     string `json:"scriptId"`
				LineNumber   int    `json:"lineNumber"` // 0-based
				ColumnNumber int    `json:"columnNumber"`
			} `json:"location"`
			ScopeChain []struct {
				Type   string `json:"type"`
				Object struct {
					ObjectID string `json:"objectId"`
				} `json:"object"`
			} `json:"scopeChain"`
		} `json:"callFrames"`
	}
	if err := json.Unmarshal(ev.Params, &params); err != nil {
		slog.Debug("decode cdp paused event failed", "err", err)
		return
	}
	frames := make([]DebugStackFrame, 0, len(params.CallFrames))
	var locals []DebugVariable
	for i, f := range params.CallFrames {
		path := f.URL
		if strings.HasPrefix(path, "file:///") {
			path = strings.TrimPrefix(path, "file:///")
			// windows 形如 file:///C:/...（盘符冒号），unix 形如 /...；
			// 两种形态在后续处理中一致对待，无需分支。
		} else if strings.HasPrefix(path, "file://") {
			path = strings.TrimPrefix(path, "file://")
		}
		name := f.FunctionName
		if name == "" {
			name = "(anonymous)"
		}
		frames = append(frames, DebugStackFrame{
			ID: i + 1, Name: name, File: path,
			Line: f.Location.LineNumber + 1, Column: f.Location.ColumnNumber + 1,
		})
		if i == 0 {
			// CDP reports top-level ESM bindings in a module scope rather than a
			// local scope. Merge lexical scopes from inner to outer and preserve
			// the first value when a name is shadowed.
			seen := make(map[string]struct{})
			for _, sc := range f.ScopeChain {
				if !nodeCDPLexicalScope(sc.Type) || sc.Object.ObjectID == "" {
					continue
				}
				for _, variable := range c.getProperties(sc.Object.ObjectID) {
					if _, exists := seen[variable.Name]; exists {
						continue
					}
					seen[variable.Name] = struct{}{}
					locals = append(locals, variable)
				}
			}
		}
	}
	c.mu.Lock()
	c.pausedCallFrameID = ""
	if len(params.CallFrames) > 0 {
		c.pausedCallFrameID = params.CallFrames[0].CallFrameID
	}
	cb := c.onPaused
	asyncCB := c.onAsyncStack
	asyncSupported := c.asyncStackTraceSupported
	c.mu.Unlock()
	if cb != nil {
		cb(params.Reason, frames, locals)
	}
	if asyncSupported && asyncCB != nil && (params.AsyncStackTrace != nil || params.AsyncStackTraceID != nil) {
		asyncCB(params.AsyncStackTrace, params.AsyncStackTraceID)
	}
}

func nodeCDPLexicalScope(scopeType string) bool {
	switch scopeType {
	case "local", "block", "catch", "closure", "script", "module":
		return true
	default:
		return false
	}
}

func parseBrowserConsoleEvent(ev cdpResponse) (BrowserConsoleEntry, bool) {
	if ev.Method == "Log.entryAdded" {
		var params struct {
			Entry struct {
				Level     string  `json:"level"`
				Text      string  `json:"text"`
				URL       string  `json:"url"`
				Line      int     `json:"lineNumber"`
				Timestamp float64 `json:"timestamp"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(ev.Params, &params); err != nil {
			slog.Debug("decode cdp log entry failed", "err", err)
			return BrowserConsoleEntry{}, false
		}
		return BrowserConsoleEntry{
			Level: params.Entry.Level, Text: params.Entry.Text, URL: params.Entry.URL,
			Line: params.Entry.Line, Timestamp: params.Entry.Timestamp,
		}, true
	}
	var params struct {
		Type      string  `json:"type"`
		Timestamp float64 `json:"timestamp"`
		Args      []struct {
			Value       interface{} `json:"value"`
			Description string      `json:"description"`
		} `json:"args"`
		StackTrace struct {
			CallFrames []struct {
				URL        string `json:"url"`
				LineNumber int    `json:"lineNumber"`
			} `json:"callFrames"`
		} `json:"stackTrace"`
	}
	if err := json.Unmarshal(ev.Params, &params); err != nil {
		slog.Debug("decode cdp console event failed", "err", err)
		return BrowserConsoleEntry{}, false
	}
	parts := make([]string, 0, len(params.Args))
	for _, arg := range params.Args {
		if arg.Value != nil {
			parts = append(parts, fmt.Sprint(arg.Value))
		} else if arg.Description != "" {
			parts = append(parts, arg.Description)
		}
	}
	entry := BrowserConsoleEntry{Level: params.Type, Text: strings.Join(parts, " "), Timestamp: params.Timestamp}
	if len(params.StackTrace.CallFrames) > 0 {
		entry.URL = params.StackTrace.CallFrames[0].URL
		entry.Line = params.StackTrace.CallFrames[0].LineNumber + 1
	}
	return entry, true
}

func parseBrowserNetworkEvent(ev cdpResponse) (BrowserNetworkEntry, bool) {
	switch ev.Method {
	case "Network.requestWillBeSent":
		var params struct {
			RequestID string  `json:"requestId"`
			Timestamp float64 `json:"timestamp"`
			Request   struct {
				URL    string `json:"url"`
				Method string `json:"method"`
			} `json:"request"`
		}
		if err := json.Unmarshal(ev.Params, &params); err != nil {
			slog.Debug("decode cdp network request failed", "err", err)
			return BrowserNetworkEntry{}, false
		}
		if params.RequestID == "" {
			return BrowserNetworkEntry{}, false
		}
		return BrowserNetworkEntry{
			RequestID: params.RequestID, Phase: "request", Method: params.Request.Method,
			URL: params.Request.URL, Timestamp: params.Timestamp,
		}, true
	case "Network.responseReceived":
		var params struct {
			RequestID string  `json:"requestId"`
			Timestamp float64 `json:"timestamp"`
			Response  struct {
				URL      string  `json:"url"`
				Status   float64 `json:"status"`
				MIMEType string  `json:"mimeType"`
			} `json:"response"`
		}
		if err := json.Unmarshal(ev.Params, &params); err != nil {
			slog.Debug("decode cdp network response failed", "err", err)
			return BrowserNetworkEntry{}, false
		}
		if params.RequestID == "" {
			return BrowserNetworkEntry{}, false
		}
		return BrowserNetworkEntry{
			RequestID: params.RequestID, Phase: "response", URL: params.Response.URL,
			Status: int(params.Response.Status), MIMEType: params.Response.MIMEType, Timestamp: params.Timestamp,
		}, true
	case "Network.loadingFailed":
		var params struct {
			RequestID string  `json:"requestId"`
			Timestamp float64 `json:"timestamp"`
			ErrorText string  `json:"errorText"`
		}
		if err := json.Unmarshal(ev.Params, &params); err != nil {
			slog.Debug("decode cdp network failure failed", "err", err)
			return BrowserNetworkEntry{}, false
		}
		if params.RequestID == "" {
			return BrowserNetworkEntry{}, false
		}
		return BrowserNetworkEntry{
			RequestID: params.RequestID, Phase: "failed", Error: params.ErrorText, Timestamp: params.Timestamp,
		}, true
	}
	return BrowserNetworkEntry{}, false
}

func (c *nodeCDPClient) getProperties(objectID string) []DebugVariable {
	raw, err := c.callResult("Runtime.getProperties", map[string]interface{}{
		"objectId":               objectID,
		"ownProperties":          true,
		"accessorPropertiesOnly": false,
	})
	if err != nil {
		slog.Debug("get cdp object properties failed", "err", err)
		return nil
	}
	var res struct {
		Result []struct {
			Name  string `json:"name"`
			Value *struct {
				Type        string      `json:"type"`
				Value       interface{} `json:"value"`
				Description string      `json:"description"`
				ObjectID    string      `json:"objectId"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		slog.Debug("decode cdp object properties failed", "err", err)
		return nil
	}
	var out []DebugVariable
	for _, p := range res.Result {
		if strings.HasPrefix(p.Name, "__") {
			continue
		}
		val := ""
		typ := ""
		if p.Value != nil {
			typ = p.Value.Type
			if p.Value.Description != "" {
				val = p.Value.Description
			} else {
				b, err := json.Marshal(p.Value.Value)
				if err != nil {
					slog.Debug("encode cdp property value failed", "err", err)
				} else {
					val = string(b)
				}
			}
		}
		ref := 0
		if p.Value != nil && p.Value.ObjectID != "" {
			// G14: map this object to a local variablesReference so the UI can
			// expand it via GetVariables (same path as DAP).
			c.mu.Lock()
			ref = c.nextObjectRef + 1
			c.nextObjectRef = ref
			c.objectRefs[ref] = p.Value.ObjectID
			c.mu.Unlock()
		}
		out = append(out, DebugVariable{Name: p.Name, Value: val, Type: typ, VariablesReference: ref})
		if len(out) >= 40 {
			break
		}
	}
	return out
}

// getPropertiesByRef resolves a local variablesReference to the CDP objectId
// and fetches its properties (G14 nested expansion for the Node adapter).
// Stale/unknown references return nil and are rejected by the caller.
func (c *nodeCDPClient) getPropertiesByRef(ref int) []DebugVariable {
	if c == nil || ref <= 0 {
		return nil
	}
	c.mu.Lock()
	objectID := c.objectRefs[ref]
	c.mu.Unlock()
	if objectID == "" {
		return nil
	}
	return c.getProperties(objectID)
}

func (c *nodeCDPClient) Resume() error {
	return c.resumeCommand("Debugger.resume")
}

func (c *nodeCDPClient) StepOver() error {
	return c.resumeCommand("Debugger.stepOver")
}

func (c *nodeCDPClient) StepInto() error {
	return c.resumeCommand("Debugger.stepInto")
}

func (c *nodeCDPClient) StepOut() error {
	return c.resumeCommand("Debugger.stepOut")
}

func (c *nodeCDPClient) resumeCommand(method string) error {
	if err := c.call(method, map[string]interface{}{}); err != nil {
		return err
	}
	c.mu.Lock()
	c.pausedCallFrameID = ""
	c.mu.Unlock()
	return nil
}

func (c *nodeCDPClient) Pause() error {
	return c.call("Debugger.pause", map[string]interface{}{})
}

// SetBreakpointByURL sets a breakpoint; line is 1-based.
func (c *nodeCDPClient) SetBreakpointByURL(file string, line int, condition string) (id string, verified bool, message string, err error) {
	return c.setBreakpointByURL(file, line, condition, "")
}

func (c *nodeCDPClient) setBreakpointByURL(file string, line int, condition, logMessage string) (id string, verified bool, message string, err error) {
	// CDP uses 0-based lines
	// also try file:// form
	params := map[string]interface{}{
		"lineNumber": line - 1,
		"url":        fileToFileURL(file),
	}
	breakpointCondition, err := nodeBreakpointCondition(condition, logMessage)
	if err != nil {
		return "", false, "invalid logpoint message", err
	}
	if breakpointCondition != "" {
		params["condition"] = breakpointCondition
	}
	raw, err := c.callResult("Debugger.setBreakpointByUrl", params)
	if err != nil {
		// fallback urlRegex
		params2 := map[string]interface{}{
			"lineNumber": line - 1,
			"urlRegex":   ".*" + regexpQuoteMeta(filepathBase(file)) + ".*",
		}
		if breakpointCondition != "" {
			params2["condition"] = breakpointCondition
		}
		raw, err = c.callResult("Debugger.setBreakpointByUrl", params2)
		if err != nil {
			return "", false, err.Error(), err
		}
	}
	var res struct {
		BreakpointID string `json:"breakpointId"`
		Locations    []struct {
			LineNumber int `json:"lineNumber"`
		} `json:"locations"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", false, "invalid cdp breakpoint response", fmt.Errorf("decode cdp breakpoint response: %w", err)
	}
	verified = len(res.Locations) > 0
	if !verified {
		message = "unverified (no matching script location yet)"
	}
	return res.BreakpointID, verified, message, nil
}

// nodeBreakpointCondition implements literal logpoints with CDP's conditional
// breakpoint primitive. The message is JSON encoded so it cannot become code.
func nodeBreakpointCondition(condition, logMessage string) (string, error) {
	if logMessage == "" {
		return condition, nil
	}
	literal, err := json.Marshal(logMessage)
	if err != nil {
		return "", fmt.Errorf("encode node logpoint message: %w", err)
	}
	logExpression := "(console.log(" + string(literal) + "), false)"
	if strings.TrimSpace(condition) == "" {
		return logExpression, nil
	}
	return "(" + condition + ") && " + logExpression, nil
}

func (c *nodeCDPClient) Evaluate(expr string) (DebugVariable, error) {
	c.mu.Lock()
	callFrameID := c.pausedCallFrameID
	c.mu.Unlock()
	method := "Runtime.evaluate"
	params := map[string]interface{}{
		"expression":    expr,
		"returnByValue": true,
		"awaitPromise":  true,
	}
	if callFrameID != "" {
		method = "Debugger.evaluateOnCallFrame"
		params["callFrameId"] = callFrameID
	}
	raw, err := c.callResult(method, params)
	if err != nil {
		return DebugVariable{Name: expr, Value: err.Error(), Type: "error"}, err
	}
	var res struct {
		Result struct {
			Type        string      `json:"type"`
			Value       interface{} `json:"value"`
			Description string      `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return DebugVariable{Name: expr, Value: "invalid cdp response", Type: "error"}, fmt.Errorf("decode cdp evaluate response: %w", err)
	}
	if res.ExceptionDetails != nil {
		msg := res.ExceptionDetails.Text
		return DebugVariable{Name: expr, Value: msg, Type: "error"}, fmt.Errorf("cdp evaluate failed: %s", msg)
	}
	val := res.Result.Description
	if val == "" {
		b, err := json.Marshal(res.Result.Value)
		if err != nil {
			return DebugVariable{Name: expr, Value: "invalid cdp result", Type: "error"}, fmt.Errorf("encode cdp evaluate result: %w", err)
		}
		val = string(b)
	}
	return DebugVariable{Name: expr, Value: val, Type: res.Result.Type}, nil
}

func fileToFileURL(path string) string {
	p := strings.ReplaceAll(path, "\\", "/")
	if len(p) >= 2 && p[1] == ':' {
		return "file:///" + p
	}
	if strings.HasPrefix(p, "/") {
		return "file://" + p
	}
	return "file:///" + p
}

func filepathBase(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return p
	}
	return p[i+1:]
}

func regexpQuoteMeta(s string) string {
	// minimal escape for file name in regex
	repl := []string{`.`, `\`, `+`, `*`, `?`, `(`, `)`, `[`, `]`, `{`, `}`, `^`, `$`, `|`}
	out := s
	for _, c := range repl {
		out = strings.ReplaceAll(out, c, `\`+c)
	}
	return out
}

// AttachDelve attaches to an existing headless/dlv dap listen address (prompt-13 13-E).
func (d *DebugService) AttachDelve(addr string) (DebugSessionInfo, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return DebugSessionInfo{}, fmt.Errorf("address required (host:port)")
	}
	if !strings.Contains(addr, ":") {
		addr = "127.0.0.1:" + addr
	}
	return d.ConnectMockDAP(addr, map[string]interface{}{
		"request": "attach",
		// delve dap often uses launch; for attach-style headless JSON-RPC this is best-effort
		"mode": "debug",
	})
}

// ProbeDelveTCP checks if a TCP port accepts connections (remote/container probe).
func (d *DebugService) ProbeDelveTCP(addr string) map[string]interface{} {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return map[string]interface{}{"ok": false, "message": "empty address"}
	}
	if !strings.Contains(addr, ":") {
		addr = "127.0.0.1:" + addr
	}
	conn, err := net.DialTimeout("tcp", addr, 800*time.Millisecond)
	if err != nil {
		return map[string]interface{}{"ok": false, "message": err.Error(), "address": addr}
	}
	if err := conn.Close(); err != nil {
		slog.Debug("close delve probe connection failed", "err", err)
	}
	return map[string]interface{}{"ok": true, "message": "port open — use Attach Delve", "address": addr}
}
