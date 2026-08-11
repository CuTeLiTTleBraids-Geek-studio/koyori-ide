package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// --- JSON-RPC 2.0 client over LSP base protocol ---

// jsonRPCClient is a minimal JSON-RPC 2.0 client that frames messages with the
// LSP Content-Length header over an io.Reader/io.Writer pair.
type jsonRPCClient struct {
	w       io.Writer
	r       *bufio.Reader
	writeMu sync.Mutex

	writeFailureMu      sync.RWMutex
	writeFailureHandler func(error)
	writeFailures       atomic.Int32
	rebuildScheduled    atomic.Bool

	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[int64]chan *rpcResponse

	// notification handlers (server→client notifications)
	notifMu sync.Mutex
	notifs  map[string][]func(json.RawMessage)
	done    chan struct{}
	started bool

	// F-2 (prompt-2.md): requestHandler 处理 server→client 的 request
	// （method + id 同时非 nil）。返回 (result, error)；若 error 非 nil
	// 则回写 JSON-RPC error response。nil 时回写 method-not-found 错误。
	requestMu      sync.RWMutex
	requestHandler func(method string, params json.RawMessage) (interface{}, error)
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// newJSONRPCClient creates a client and starts a background goroutine that
// reads responses/notifications from the server's stdout.
func newJSONRPCClient(r io.Reader, w io.Writer) *jsonRPCClient {
	return newJSONRPCClientWithHandler(r, w, nil)
}

func newJSONRPCClientWithHandler(
	r io.Reader,
	w io.Writer,
	handler func(method string, params json.RawMessage) (interface{}, error),
) *jsonRPCClient {
	c := &jsonRPCClient{
		w:              w,
		r:              bufio.NewReader(r),
		pending:        make(map[int64]chan *rpcResponse),
		notifs:         make(map[string][]func(json.RawMessage)),
		done:           make(chan struct{}),
		requestHandler: handler,
	}
	c.started = true
	go c.readLoop()
	return c
}

func (c *jsonRPCClient) setRequestHandler(handler func(method string, params json.RawMessage) (interface{}, error)) {
	c.requestMu.Lock()
	c.requestHandler = handler
	c.requestMu.Unlock()
}

func (c *jsonRPCClient) getRequestHandler() func(method string, params json.RawMessage) (interface{}, error) {
	c.requestMu.RLock()
	defer c.requestMu.RUnlock()
	return c.requestHandler
}

func (c *jsonRPCClient) setWriteFailureHandler(handler func(error)) {
	c.writeFailureMu.Lock()
	c.writeFailureHandler = handler
	c.writeFailureMu.Unlock()
}

func (c *jsonRPCClient) recordWriteResult(err error) {
	if err == nil {
		c.writeFailures.Store(0)
		return
	}
	if c.writeFailures.Add(1) < lspWriteFailureThreshold {
		return
	}
	c.writeFailureMu.RLock()
	handler := c.writeFailureHandler
	c.writeFailureMu.RUnlock()
	if handler == nil || !c.rebuildScheduled.CompareAndSwap(false, true) {
		return
	}
	go handler(err)
}

// request sends a JSON-RPC request and waits for the response.
func (c *jsonRPCClient) request(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if c.isDone() {
		return nil, fmt.Errorf("LSP request %s: connection closed", method)
	}
	id := c.nextID.Add(1)
	// A single buffered slot transfers response ownership without coupling the
	// read loop to a caller that may concurrently time out.
	ch := make(chan *rpcResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	if c.isDone() {
		c.removePending(id, ch)
		return nil, fmt.Errorf("LSP request %s: connection closed", method)
	}

	if err := c.writeMessageContext(ctx, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		if c.removePending(id, ch) && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			// The write may have completed just before the context fired (race).
			// Defensively send $/cancelRequest so the server can clean up.
			// Per LSP spec, unknown IDs are silently ignored.
			c.cancelRequest(id)
		} else {
			c.removePending(id, ch)
		}
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp == nil {
			return nil, fmt.Errorf("LSP request %s: connection closed", method)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("LSP request %s failed (%d): %s", method, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-ctx.Done():
		if c.removePending(id, ch) {
			c.cancelRequest(id)
		}
		return nil, ctx.Err()
	case <-c.done:
		c.removePending(id, ch)
		return nil, fmt.Errorf("LSP request %s: connection closed", method)
	}
}

func (c *jsonRPCClient) cancelRequest(id int64) {
	// Send $/cancelRequest as a best-effort background notification. The caller
	// (request → ctx.Done branch) must not block waiting for this write because
	// the pipe reader may not be scheduled until after request() returns, causing
	// a deadlock: write blocks until someone reads, but nobody reads until request
	// returns, and request waits for cancelRequest to return.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.writeMessageContext(ctx, map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "$/cancelRequest",
			"params":  map[string]int64{"id": id},
		})
	}()
}

func (c *jsonRPCClient) isDone() bool {
	if c.done == nil {
		return false
	}
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *jsonRPCClient) writeMessageContext(ctx context.Context, msg map[string]interface{}) error {
	if err := ctx.Err(); err != nil {
		// No transport write was attempted, so this is a caller deadline rather
		// than evidence that the LSP connection is unhealthy.
		return err
	}
	if c.isDone() {
		return errors.New("LSP connection closed")
	}
	done := make(chan error, 1)
	var recordOnce sync.Once
	record := func(err error) {
		recordOnce.Do(func() {
			c.recordWriteResult(err)
		})
	}
	go func() {
		err := c.writeMessageUntracked(msg)
		record(err)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Prefer a concurrently-completed write over the deadline. Without this
		// non-blocking drain, a context cancellation that races with the write
		// completing would cause callers (e.g. request → cancelRequest) to skip
		// sending $/cancelRequest even though the original message was sent.
		select {
		case err := <-done:
			return err
		default:
		}
		// A slow write does not prove the transport is dead. Leave the stream
		// intact; repeated write deadlines are counted and trigger a managed
		// server rebuild only after the configured threshold.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			record(ctx.Err())
		}
		return ctx.Err()
	case <-c.done:
		return errors.New("LSP connection closed")
	}
}

func (c *jsonRPCClient) removePending(id int64, expected chan *rpcResponse) bool {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	ch, ok := c.pending[id]
	if !ok || ch != expected {
		return false
	}
	delete(c.pending, id)
	return true
}

func (c *jsonRPCClient) takePending(id int64) (chan *rpcResponse, bool) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	return ch, ok
}

func (c *jsonRPCClient) takeAllPending() map[int64]chan *rpcResponse {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	pending := c.pending
	c.pending = make(map[int64]chan *rpcResponse)
	return pending
}

// notify sends a JSON-RPC notification (no response expected).
func (c *jsonRPCClient) notify(method string, params interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
	defer cancel()
	return c.writeMessageContext(ctx, map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

// onNotification registers a handler for a server→client notification with
// the given method (e.g. "textDocument/publishDiagnostics"). The handler is
// invoked from the readLoop goroutine; it must not block and must not call
// back into the client (no reentrant write). G-FEAT-02 uses this to collect
// published diagnostics.
func (c *jsonRPCClient) onNotification(method string, handler func(json.RawMessage)) {
	c.notifMu.Lock()
	defer c.notifMu.Unlock()
	c.notifs[method] = append(c.notifs[method], handler)
}

// writeMessage frames a JSON-RPC message with the Content-Length header and
// writes it to the server's stdin.
func (c *jsonRPCClient) writeMessage(msg map[string]interface{}) error {
	err := c.writeMessageUntracked(msg)
	c.recordWriteResult(err)
	return err
}

func (c *jsonRPCClient) writeMessageUntracked(msg map[string]interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	header := "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n"
	if _, err := c.w.Write([]byte(header)); err != nil {
		return err
	}
	if _, err := c.w.Write(data); err != nil {
		return err
	}
	return nil
}

// readLoop reads framed JSON-RPC messages from the server's stdout and
// dispatches responses to waiting requesters and notifications to handlers.
func (c *jsonRPCClient) readLoop() {
	for {
		msg, err := c.readMessage()
		if err != nil {
			// Connection closed or error — fail all pending requests.
			for _, ch := range c.takeAllPending() {
				close(ch)
			}
			close(c.done)
			return
		}
		c.dispatch(msg)
	}
}

const maxLSPMessageSize int64 = 64 * 1024 * 1024

// readMessage reads one LSP-framed message (Content-Length header + body).
func (c *jsonRPCClient) readMessage() (json.RawMessage, error) {
	var contentLength int64
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		if strings.HasPrefix(line, "Content-Length:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLength, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid LSP Content-Length %q: %w", v, err)
			}
			if contentLength > maxLSPMessageSize {
				return nil, fmt.Errorf("LSP Content-Length %d exceeds %d byte limit", contentLength, maxLSPMessageSize)
			}
		}
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("LSP message missing Content-Length")
	}
	var body bytes.Buffer
	if _, err := io.CopyN(&body, c.r, contentLength); err != nil {
		return nil, err
	}
	return json.RawMessage(body.Bytes()), nil
}

// dispatch routes a parsed JSON-RPC message to its handler (response or
// notification).
func (c *jsonRPCClient) dispatch(msg json.RawMessage) {
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Params json.RawMessage `json:"params"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(msg, &envelope); err != nil {
		return
	}
	idBytes := bytes.TrimSpace(envelope.ID)
	hasID := len(idBytes) > 0 && !bytes.Equal(idBytes, []byte("null"))
	if hasID && envelope.Method == "" {
		// Response to a request.
		var responseID int64
		if err := json.Unmarshal(idBytes, &responseID); err != nil {
			slog.Warn("LSP response has an unexpected id", "id", string(idBytes), "err", err)
			return
		}
		ch, ok := c.takePending(responseID)
		if ok {
			resp := &rpcResponse{Result: envelope.Result, Error: envelope.Error}
			// FIX A8: the old default branch silently discarded valid responses.
			// Ownership is removed atomically before a bounded asynchronous send,
			// so duplicate responses cannot race with timeout channel closure.
			go deliverRPCResponse(ch, resp, responseID, lspResponseDeliveryTimeout)
		}
		return
	}
	if envelope.Method != "" && !hasID {
		// Notification from the server.
		c.notifMu.Lock()
		handlers := append([]func(json.RawMessage){}, c.notifs[envelope.Method]...)
		c.notifMu.Unlock()
		for _, h := range handlers {
			h(envelope.Params)
		}
		return
	}
	// F-2 (prompt-2.md): server→client request (method + id 同时非 nil)。
	// 调用 requestHandler 并通过 writeMessage 回写 response。
	if envelope.Method != "" && hasID {
		// Copy the id and params before detaching. workspace/applyEdit may perform
		// multi-file IO and must never stall response/notification processing.
		id := append(json.RawMessage(nil), idBytes...)
		method := envelope.Method
		params := append(json.RawMessage(nil), envelope.Params...)
		go c.respondToServerRequest(id, method, params)
		return
	}
}

func deliverRPCResponse(ch chan *rpcResponse, resp *rpcResponse, id int64, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = lspResponseDeliveryTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ch <- resp:
		return true
	case <-timer.C:
		slog.Warn("LSP response dropped: caller timeout", "id", id)
		close(ch)
		return false
	}
}

// respondToServerRequest 构造 JSON-RPC response 并写回 server stdin。
// requestHandler 为 nil 时回写 method-not-found (-32601)；返回 error 时
// 回写 internal-error (-32603)。F-2 (prompt-2.md)。
func (c *jsonRPCClient) respondToServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
	}
	handler := c.getRequestHandler()
	if handler == nil {
		resp["error"] = map[string]interface{}{
			"code":    -32601, // Method not found
			"message": "no request handler registered for " + method,
		}
	} else {
		result, err := handler(method, params)
		if err != nil {
			resp["error"] = map[string]interface{}{
				"code":    -32603, // Internal error
				"message": err.Error(),
			}
		} else {
			resp["result"] = result
		}
	}
	// writeMessage 已加 writeMu，readLoop 调用本方法时不会与 request/notify 冲突。
	if err := c.writeMessage(resp); err != nil {
		slog.Warn("failed to write LSP server-request response", "method", method, "id", string(id), "err", err)
	}
}
