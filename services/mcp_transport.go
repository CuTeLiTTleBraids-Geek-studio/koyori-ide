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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Plan 11 Task 4 — MCP (Model Context Protocol) client service.
//
// This file implements a self-contained MCP client that speaks JSON-RPC 2.0
// over three transports: stdio (subprocess), SSE, and streamable HTTP.
// It does NOT depend on github.com/mark3labs/mcp-go because the build
// environment is offline (Step 1: go get failed — network unreachable).
// The implementation follows the MCP 2024-11-05 specification:
//   https://spec.modelcontextprotocol.io/specification/2024-11-05/
//
// Security (G-SEC-02/09/12):
//   - All MCP server configs persisted via atomicWriteJSON with 0600.
//   - MCP tools default to RiskElevated; write/network/exec tools are
//     RiskDangerous. Tool approval is decided by the backend session policy.
//   - MCP servers are treated as Restricted extensions (G-SEC-12): they
//     require explicitApproval before activation.
//   - stdio command paths are validated against the workspace root when
//     a root is set (ValidatePathWithinRoot).

// ---------------------------------------------------------------------------
// Schema (Step 4)
// ---------------------------------------------------------------------------

// MCPConfig is the on-disk configuration for all MCP servers.
type MCPConfig struct {
	Servers []MCPServerConfig `json:"servers"`
}

// MCPServerConfig describes a single MCP server connection.
//
// Transport selects how the client talks to the server:
//   - "stdio": spawn Command with Args + Env, communicate over stdin/stdout.
//   - "sse":   connect to URL, receive Server-Sent Events, POST requests back.
//   - "http":  streamable HTTP — POST JSON-RPC to URL and read the response.
//
// Tool approval is a backend session-level decision. Legacy autoApprove keys
// in persisted JSON are ignored by standard JSON decoding.
type MCPServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // "stdio" | "sse" | "http"
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   bool              `json:"enabled"`
	// workDir is backend-owned execution context. It is intentionally not
	// serialized or exposed to the renderer.
	workDir            string
	executableIdentity *mcpExecutableIdentity
}

// MCPTool is a tool exposed by an MCP server.
type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
}

// MCPResource is a resource exposed by an MCP server.
type MCPResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// MCPPrompt is a prompt template exposed by an MCP server.
type MCPPrompt struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description,omitempty"`
	Arguments   []map[string]interface{} `json:"arguments,omitempty"`
}

// MCPToolResult is the result of calling an MCP tool.
type MCPToolResult struct {
	Content []MCPContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// MCPContent is a content block in a tool result.
type MCPContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Future: image/audio content types.
}

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 protocol (Step 2/3)
// ---------------------------------------------------------------------------

// jsonrpcOutboundMessage is any JSON-RPC 2.0 message this client writes: a
// request (ID + Method), a notification (Method, no ID), a success response
// to a server request (ID + Result), or the error response rejecting an
// unimplemented server-to-client request (ID + Error).
type jsonrpcOutboundMessage struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      interface{}   `json:"id,omitempty"` // nil for notifications
	Method  string        `json:"method,omitempty"`
	Params  interface{}   `json:"params,omitempty"`
	Error   *jsonrpcError `json:"error,omitempty"`
	Result  interface{}   `json:"result,omitempty"`
}

// jsonrpcResponse is a JSON-RPC 2.0 response. When a message carries a
// Method it is a server-to-client notification (ID nil) or request (ID set);
// the client dispatcher classifies incoming messages by these fields.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Transport interface (Step 3)
// ---------------------------------------------------------------------------

// mcpTransport is the interface for a JSON-RPC transport layer.
type mcpTransport interface {
	// Send writes a JSON-RPC message. For notifications (id == nil), no
	// response is expected.
	Send(ctx context.Context, req *jsonrpcOutboundMessage) error
	// Recv reads the next JSON-RPC response. Returns io.EOF on close.
	Recv() (*jsonrpcResponse, error)
	// Close releases transport resources.
	Close() error
}

const mcpTransportCloseTimeout = 2 * time.Second

// ---------------------------------------------------------------------------
// stdio transport (Step 3a)
// ---------------------------------------------------------------------------

// stdioTransport communicates with an MCP server over a subprocess's
// stdin/stdout. Each JSON-RPC message is a single newline-delimited line.
type stdioTransport struct {
	cmd         *exec.Cmd
	processTree lspProcessTree
	stdin       io.WriteCloser
	stdout      *bufio.Reader
	stdoutPipe  io.ReadCloser
	processDone <-chan mcpProcessWaitResult
	sendMu      sync.Mutex
	closed      atomic.Bool
	closeOnce   sync.Once
	closeErr    error
}

func newStdioTransport(ctx context.Context, cfg MCPServerConfig) (*stdioTransport, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("stdio transport requires a command: %w", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start mcp server: %w", err)
	}
	// The transport is the sole owner of the long-lived subprocess. Using
	// exec.CommandContext here gives the connect context a second asynchronous
	// Process.Kill path that races transport.Close on Windows. Initialize and
	// shutdown cancellation are already propagated through MCPClient, which
	// closes this transport and reaps the process synchronously.
	cmd := command(cfg.Command, cfg.Args...)
	if cfg.workDir != "" {
		cmd.Dir = cfg.workDir
	}
	if cfg.executableIdentity != nil {
		if err := verifyMCPExecutableIdentity(cfg.executableIdentity); err != nil {
			return nil, fmt.Errorf("stdio executable changed before start: %w", err)
		}
	}
	// The transport owns the entire process tree, not just the immediate
	// cmd.exe/script host. On Unix this establishes a process group; on
	// Windows attachMCPProcessTree binds a kill-on-close Job Object.
	configureCoverageProcessTree(cmd)
	// Do not inherit the IDE's complete environment. In particular, cloud
	// credentials, SSH helpers, package-manager tokens, and proxy credentials
	// must not become ambient authority for a workspace MCP process.
	cmd.Env = mcpChildEnvironment(cfg.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		closeErr := stdin.Close()
		return nil, errors.Join(fmt.Errorf("stdout pipe: %w", err), closeErr)
	}
	cmd.Stderr = nil // discard stderr; MCP servers should log via protocol
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("start mcp server: %w", err),
			stdin.Close(),
			stdout.Close(),
		)
	}
	processTree, err := attachLSPProcessTree(cmd)
	if err != nil {
		// attachLSPProcessTree owns the Job handle cleanup. Kill only through
		// the original os.Process handle here; a PID-based taskkill fallback
		// could target an unrelated process if the short-lived cmd shim's PID
		// was reused while descendant discovery was running.
		terminateErr := cmd.Process.Kill()
		if errors.Is(terminateErr, os.ErrProcessDone) {
			terminateErr = nil
		}
		waitErr := cmd.Wait()
		return nil, errors.Join(
			fmt.Errorf("attach MCP process tree: %w", err),
			terminateErr,
			waitErr,
			stdin.Close(),
			stdout.Close(),
		)
	}
	transport := &stdioTransport{
		cmd:         cmd,
		processTree: processTree,
		stdin:       stdin,
		stdout:      bufio.NewReader(stdout),
		stdoutPipe:  stdout,
		processDone: startMCPProcessWaiter(cmd),
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(fmt.Errorf("start mcp server: %w", err), transport.Close())
	}
	return transport, nil
}

func (t *stdioTransport) Send(ctx context.Context, req *jsonrpcOutboundMessage) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')

	// A client normally serializes Send calls, but the transport also owns
	// this invariant so direct callers cannot interleave newline-delimited
	// JSON messages. Close intentionally does not take sendMu: closing the
	// pipe must be able to unblock a writer stalled on an unresponsive server.
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	if t.closed.Load() || t.stdin == nil {
		return fmt.Errorf("send request: %w", io.ErrClosedPipe)
	}
	if _, err := t.stdin.Write(data); err != nil {
		return fmt.Errorf("write request: %w", err)
	}
	return nil
}

func (t *stdioTransport) Recv() (*jsonrpcResponse, error) {
	line, err := t.stdout.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var resp jsonrpcResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &resp, nil
}

func (t *stdioTransport) Close() error {
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		var closeErrs []error
		processDone := t.processDone
		if processDone == nil && t.cmd != nil && t.cmd.Process != nil {
			processDone = startMCPProcessWaiter(t.cmd)
		}
		var processResult *mcpProcessWaitResult
		if processDone != nil {
			select {
			case result := <-processDone:
				processResult = &result
			default:
			}
		}
		if t.stdin != nil {
			if err := t.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				closeErrs = append(closeErrs, fmt.Errorf("close stdio stdin: %w", err))
			}
		}
		killIssued := false
		if t.cmd != nil && t.cmd.Process != nil {
			var terminateErr error
			if t.processTree != nil {
				terminateErr = t.processTree.terminateAndWait(mcpTransportCloseTimeout)
			} else {
				terminateErr = terminateCoverageProcessTree(t.cmd.Process)
			}
			if terminateErr != nil {
				closeErrs = append(closeErrs, fmt.Errorf("terminate stdio process tree: %w", terminateErr))
			} else {
				killIssued = true
			}
		}
		// Closing the read side unblocks a concurrent Recv even if process
		// termination failed, so shutdown never depends on server cooperation.
		if t.stdoutPipe != nil {
			if err := t.stdoutPipe.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				closeErrs = append(closeErrs, fmt.Errorf("close stdio stdout: %w", err))
			}
		}
		if processResult == nil && processDone != nil {
			timer := time.NewTimer(mcpTransportCloseTimeout)
			select {
			case result := <-processDone:
				processResult = &result
			case <-timer.C:
				closeErrs = append(closeErrs, fmt.Errorf("wait for stdio process: timed out after %s: %w", mcpTransportCloseTimeout, context.DeadlineExceeded))
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		if processResult != nil && processResult.err != nil {
			if !killIssued || !mcpProcessExitMatchesKill(processResult.state) {
				closeErrs = append(closeErrs, fmt.Errorf("wait for stdio process: %w", processResult.err))
			} else {
				var exitErr *exec.ExitError
				if !errors.As(processResult.err, &exitErr) {
					closeErrs = append(closeErrs, fmt.Errorf("wait for stdio process: %w", processResult.err))
				}
			}
		}
		if processResult != nil && t.cmd != nil {
			t.cmd.ProcessState = processResult.state
		}
		t.closeErr = errors.Join(closeErrs...)
	})
	return t.closeErr
}

type mcpProcessWaitResult struct {
	state *os.ProcessState
	err   error
}

func startMCPProcessWaiter(cmd *exec.Cmd) <-chan mcpProcessWaitResult {
	done := make(chan mcpProcessWaitResult, 1)
	go func() {
		state, err := cmd.Process.Wait()
		if err == nil && state != nil && !state.Success() {
			err = &exec.ExitError{ProcessState: state}
		}
		done <- mcpProcessWaitResult{state: state, err: err}
	}()
	return done
}

// ---------------------------------------------------------------------------
// HTTP transport (Step 3c — streamable HTTP)
// ---------------------------------------------------------------------------

// httpTransport sends JSON-RPC requests via HTTP POST and reads the response
// from the same response body. This is the "streamable HTTP" transport from
// the MCP 2024-11-05 spec.
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client
}

// newMCPHTTPClient installs the production HTTP client for the streamable
// HTTP transport. It is a package variable only so tests can point the
// transport at a real local httptest fixture; the loopback refusal of the
// SSRF guard (C-1) must never be weakened outside tests.
var newMCPHTTPClient = func() *http.Client {
	return &http.Client{
		Timeout:       60 * time.Second,
		CheckRedirect: noRedirectPolicy,
		// C-1: use SSRF-safe transport so the actual dial re-validates the
		// resolved IP, defeating DNS rebinding between SaveServer's URL
		// validation and the HTTP request.
		Transport: NewSSRFSafeTransport(),
	}
}

func newHTTPTransport(cfg MCPServerConfig) *httpTransport {
	return &httpTransport{
		url:     cfg.URL,
		headers: cfg.Headers,
		client:  newMCPHTTPClient(),
	}
}

func (t *httpTransport) Send(ctx context.Context, req *jsonrpcOutboundMessage) error {
	resp, err := t.postRequest(ctx, req)
	if err != nil {
		return err
	}
	if resp != nil && resp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return nil
}

func (t *httpTransport) Recv() (*jsonrpcResponse, error) {
	// httpTransport uses a combined send+recv model via postRequest.
	// This Recv() is unused for HTTP; the client calls postRequest directly.
	return nil, fmt.Errorf("http transport uses postRequest, not Recv: %w", ErrInvalidInput)
}

func (t *httpTransport) Close() error {
	t.client.CloseIdleConnections()
	return nil
}

// postRequest sends a JSON-RPC request and returns the parsed response.
func (t *httpTransport) postRequest(ctx context.Context, req *jsonrpcOutboundMessage) (*jsonrpcResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.url, strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			slog.Debug("mcp: close http response body failed", "err", err)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit (G-SEC: bounded reads)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		if resp.StatusCode < http.StatusBadRequest {
			return nil, fmt.Errorf("MCP server redirect rejected with status %d: %w", resp.StatusCode, ErrNotAllowed)
		}
		// P19 P2: bound the body embedded in the error message at 4096 bytes,
		// matching the SSE-side cap; the 1MB read above exists for successful
		// JSON/SSE payloads, not for error text.
		snippet := body
		if len(snippet) > 4096 {
			snippet = snippet[:4096]
		}
		return nil, fmt.Errorf("mcp server returned %d: %s", resp.StatusCode, string(snippet))
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return &jsonrpcResponse{}, nil
	}
	// The server may respond with application/json (single JSON-RPC object)
	// or text/event-stream (SSE frames). Parse both.
	contentType := resp.Header.Get("Content-Type")
	var rpcResp jsonrpcResponse
	if strings.Contains(contentType, "text/event-stream") {
		// Join every data: line in the SSE event before decoding it.
		rpcResp, err = parseSSEFrame(body)
		if err != nil {
			return nil, fmt.Errorf("parse sse response: %w", err)
		}
	} else {
		if err := json.Unmarshal(body, &rpcResp); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}
	}
	return &rpcResp, nil
}

// parseSSEFrame decodes the first SSE event containing data fields.
func parseSSEFrame(body []byte) (jsonrpcResponse, error) {
	var dataLines []string
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		if line == "" {
			if len(dataLines) > 0 {
				return decodeSSEDataLines(dataLines)
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, hasColon := strings.Cut(line, ":")
		if field != "data" {
			continue
		}
		if !hasColon {
			value = ""
		} else if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		dataLines = append(dataLines, value)
	}
	if len(dataLines) == 0 {
		var resp jsonrpcResponse
		return resp, fmt.Errorf("no data frame in sse response: %w", ErrNotFound)
	}
	return decodeSSEDataLines(dataLines)
}

func decodeSSEDataLines(dataLines []string) (jsonrpcResponse, error) {
	var resp jsonrpcResponse
	payload := strings.Join(dataLines, "\n")
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		return resp, fmt.Errorf("unmarshal sse data: %w", err)
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// SSE transport (Step 3b)
// ---------------------------------------------------------------------------

// sseTransport connects to an MCP server via Server-Sent Events. It opens
// a long-lived SSE connection for server→client messages and POSTs client
// →server messages to an endpoint URL provided by the server.
type sseTransport struct {
	url     string
	headers map[string]string
	client  *http.Client
	// postURL is the endpoint the server tells us to POST messages to.
	// It's discovered from the SSE stream's first "endpoint" event.
	postURL string
	// postURLErr is set when the server-supplied endpoint URL fails the
	// same-origin / SSRF check (C-2). readLoop writes it before closing
	// done; Recv surfaces it so callers see why the stream died.
	postURLErr    error
	endpointReady chan struct{}
	endpointOnce  sync.Once
	events        chan jsonrpcResponse
	done          chan struct{}
	once          sync.Once
	closeOnce     sync.Once
	closeErr      error
	readDoneOnce  sync.Once
	bodyCloseOnce sync.Once
	bodyCloseDone chan struct{}
	bodyCloseErr  error
	// H-6: 保存 SSE 响应体引用，Close 时关闭以解除 readLoop 在
	// scanner.Scan() 上的阻塞（否则 close(done) 无法唤醒被阻塞的 Scan）。
	body     io.ReadCloser
	readDone chan struct{}
	// H-6: 等待 readLoop 退出，避免 goroutine 泄漏。
	wg sync.WaitGroup
	// N-2: mu 保护 body / wg / postURL / postURLErr 字段，防止并发
	// connect/Close 导致 race（Close 读 body==nil 时跳过 wg.Wait → goroutine 泄漏；
	// 或 wg.Wait 在 Add 之前调用 → panic）。锁内不调用任何阻塞操作
	//（body.Close、wg.Wait 均在锁外执行）。
	mu sync.Mutex
}

func newSSETransport(cfg MCPServerConfig) *sseTransport {
	// P19 P2: response headers must arrive promptly even though the SSE
	// stream itself must never time out (Timeout: 0). Without
	// ResponseHeaderTimeout a hung server stalls connect indefinitely.
	// NewSSRFSafeTransport returns a fresh instance per call, so tuning this
	// copy leaves every other SSRF-safe transport untouched.
	ssrfTransport := NewSSRFSafeTransport()
	ssrfTransport.ResponseHeaderTimeout = 30 * time.Second
	return &sseTransport{
		url:     cfg.URL,
		headers: cfg.Headers,
		// C-1: SSRF-safe transport (same rationale as newHTTPTransport).
		client: &http.Client{
			Timeout:       0,
			CheckRedirect: noRedirectPolicy,
			Transport:     ssrfTransport,
		}, // no timeout for SSE
		events:        make(chan jsonrpcResponse, 16),
		done:          make(chan struct{}),
		endpointReady: make(chan struct{}),
	}
}

func (t *sseTransport) Send(ctx context.Context, req *jsonrpcOutboundMessage) error {
	postURL, err := t.waitForPostURL(ctx)
	if err != nil {
		return err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", postURL, strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("post to sse endpoint: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			slog.Debug("mcp: close sse post response body failed", "err", err)
		}
	}()
	if resp.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			return fmt.Errorf("sse post returned %d and response read failed: %w", resp.StatusCode, readErr)
		}
		return fmt.Errorf("sse post returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (t *sseTransport) Recv() (*jsonrpcResponse, error) {
	// Prefer already-buffered responses over stream termination. The read loop
	// queues the final event before closing done, so returning EOF first would
	// nondeterministically discard a valid tail event.
	select {
	case resp := <-t.events:
		return &resp, nil
	default:
	}
	select {
	case resp := <-t.events:
		return &resp, nil
	case <-t.done:
		// If the event and done became ready together, the select above may have
		// chosen done. Drain once more now that readLoop completion guarantees
		// any successfully queued tail event is visible.
		select {
		case resp := <-t.events:
			return &resp, nil
		default:
		}
		// C-2: surface the reason the stream was torn down if the
		// server-supplied endpoint URL failed validation.
		// N-2: 加锁读取 postURLErr，避免与 readLoop 写入并发 race
		t.mu.Lock()
		err := t.postURLErr
		t.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
}

func (t *sseTransport) Close() error {
	t.closeOnce.Do(func() {
		t.once.Do(func() { close(t.done) })
		t.endpointOnce.Do(func() { close(t.endpointReady) })
		t.mu.Lock()
		body := t.body
		var readDone <-chan struct{} = t.readDone
		wg := &t.wg
		t.mu.Unlock()

		bodyDone := t.startSSEBodyClose(body)
		readDone = sseReadLoopDone(readDone, wg)
		timer := time.NewTimer(mcpTransportCloseTimeout)
		defer timer.Stop()

		var closeErrs []error
		bodyPending := bodyDone != nil
		readPending := readDone != nil
		for bodyPending || readPending {
			select {
			case <-bodyDone:
				bodyDone = nil
				bodyPending = false
				if err := t.sseBodyCloseError(); err != nil {
					closeErrs = append(closeErrs, fmt.Errorf("close sse response body: %w", err))
				}
			case <-readDone:
				readDone = nil
				readPending = false
			case <-timer.C:
				if bodyPending {
					closeErrs = append(closeErrs, fmt.Errorf("timed out closing sse response body after %s", mcpTransportCloseTimeout))
				}
				if readPending {
					closeErrs = append(closeErrs, fmt.Errorf("timed out waiting for sse read loop after %s", mcpTransportCloseTimeout))
				}
				bodyPending = false
				readPending = false
			}
		}
		if t.client != nil {
			t.client.CloseIdleConnections()
		}
		t.closeErr = errors.Join(closeErrs...)
	})
	return t.closeErr
}

func (t *sseTransport) startSSEBodyClose(body io.ReadCloser) <-chan struct{} {
	if body == nil {
		return nil
	}
	t.bodyCloseOnce.Do(func() {
		done := make(chan struct{})
		t.mu.Lock()
		t.bodyCloseDone = done
		t.mu.Unlock()
		go func() {
			err := body.Close()
			if errors.Is(err, os.ErrClosed) {
				err = nil
			}
			t.mu.Lock()
			t.bodyCloseErr = err
			t.mu.Unlock()
			close(done)
		}()
	})
	t.mu.Lock()
	done := t.bodyCloseDone
	t.mu.Unlock()
	return done
}

func (t *sseTransport) sseBodyCloseError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bodyCloseErr
}

func sseReadLoopDone(readDone <-chan struct{}, wg *sync.WaitGroup) <-chan struct{} {
	if readDone != nil {
		return readDone
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}

// connect opens the SSE stream and starts reading events. The first
// "endpoint" event tells us where to POST messages.
func (t *sseTransport) connect(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", t.url, nil)
	if err != nil {
		return fmt.Errorf("create sse request: %w", err)
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("connect sse: %w", err)
	}
	if resp.StatusCode != 200 {
		return errors.Join(fmt.Errorf("sse connect returned %d", resp.StatusCode), resp.Body.Close())
	}
	// N-2: 加锁保护 body / wg 字段。Close 可能并发执行：
	// 若 Close 在 connect 设置 body 之前读取 body==nil，会跳过 wg.Wait，
	// 但此时 wg.Add(1) 也未执行 → goroutine 启动后 wg.Done 触发 panic（wg 负数），
	// 或 goroutine 泄漏。锁保证 Close 看到一致的 body/wg 状态。
	// 锁内不调用阻塞操作；resp.Body.Close 在 Close() 锁外执行。
	t.mu.Lock()
	// 边界检查：若 Close 已先于 connect 完成被调用，done 已关闭。
	// 此时若仍设置 body 并启动 readLoop goroutine，Close 不会再调用 body.Close
	// （它已跑过），readLoop 会在 scanner.Scan() 上永久阻塞 → goroutine 泄漏。
	// 因此发现 done 已关闭时立即关闭 resp.Body 并返回。
	select {
	case <-t.done:
		t.mu.Unlock()
		return errors.Join(
			fmt.Errorf("sse transport: closed during connect: %w", ErrInvalidInput),
			resp.Body.Close(),
		)
	default:
	}
	readDone := make(chan struct{})
	t.body = resp.Body
	t.readDone = readDone
	t.wg.Add(1)
	t.mu.Unlock()
	go func() {
		defer t.wg.Done()
		t.readLoop(resp.Body)
	}()
	if _, err := t.waitForPostURL(ctx); err != nil {
		return errors.Join(err, t.Close())
	}
	return nil
}

func (t *sseTransport) readLoop(body io.ReadCloser) {
	t.mu.Lock()
	readDone := t.readDone
	if readDone == nil {
		readDone = make(chan struct{})
		t.readDone = readDone
	}
	t.mu.Unlock()
	defer t.readDoneOnce.Do(func() { close(readDone) })
	defer t.startSSEBodyClose(body)
	// FIX B1: closing done on remote EOF lets the client response dispatcher
	// leave Recv instead of leaking after the SSE producer disappears.
	defer t.once.Do(func() { close(t.done) })
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20) // 1MB max line
	var eventName string
	var dataLines []string
	flushEvent := func() bool {
		if len(dataLines) == 0 {
			eventName = ""
			return true
		}
		ok := t.dispatchSSEEvent(eventName, dataLines)
		eventName = ""
		dataLines = nil
		return ok
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !flushEvent() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, hasColon := strings.Cut(line, ":")
		if hasColon && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "event":
			eventName = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if len(dataLines) > 0 {
		_ = flushEvent()
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
		slog.Debug("mcp: scan sse stream failed", "err", err)
	}
}

func (t *sseTransport) dispatchSSEEvent(eventName string, dataLines []string) bool {
	payload := strings.Join(dataLines, "\n")
	endpointPayload := strings.EqualFold(strings.TrimSpace(eventName), "endpoint")
	if endpointPayload || strings.HasPrefix(payload, "endpoint:") {
		candidate := strings.TrimSpace(payload)
		if !endpointPayload {
			candidate = strings.TrimSpace(strings.TrimPrefix(payload, "endpoint:"))
		}
		normalized, err := normalizeSSEPostURL(t.url, candidate)
		if err != nil {
			t.mu.Lock()
			t.postURLErr = fmt.Errorf("sse endpoint rejected: %w", err)
			t.mu.Unlock()
			t.endpointOnce.Do(func() { close(t.endpointReady) })
			t.once.Do(func() { close(t.done) })
			return false
		}
		t.mu.Lock()
		t.postURL = normalized
		t.mu.Unlock()
		t.endpointOnce.Do(func() { close(t.endpointReady) })
		return true
	}
	var resp jsonrpcResponse
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		slog.Debug("mcp: unmarshal sse event failed", "err", err)
		return true
	}
	select {
	case t.events <- resp:
		return true
	case <-t.done:
		return false
	}
}

func (t *sseTransport) waitForPostURL(ctx context.Context) (string, error) {
	for {
		t.mu.Lock()
		postURL := t.postURL
		postErr := t.postURLErr
		t.mu.Unlock()
		if postErr != nil {
			return "", postErr
		}
		select {
		case <-t.done:
			return "", fmt.Errorf("sse transport closed: %w", io.EOF)
		default:
		}
		if postURL != "" {
			return postURL, nil
		}
		select {
		case <-t.endpointReady:
		case <-t.done:
		case <-ctx.Done():
			return "", fmt.Errorf("wait for sse endpoint: %w", ctx.Err())
		}
	}
}

func normalizeSSEPostURL(baseURL, postURL string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	post, err := url.Parse(strings.TrimSpace(postURL))
	if err != nil {
		return "", fmt.Errorf("parse post url: %w", err)
	}
	if post.String() == "" {
		return "", fmt.Errorf("post url is empty")
	}
	if !post.IsAbs() {
		post = base.ResolveReference(post)
	}
	if !sameOriginURL(base, post) {
		return "", fmt.Errorf("post url %q is not same-origin with %q (scheme/host/port differ)", postURL, baseURL)
	}
	resolved := post.String()
	if _, err := ValidateNonPrivateURL(resolved); err != nil {
		return "", fmt.Errorf("post url: %w", err)
	}
	return resolved, nil
}

// sameOriginURL reports whether two URLs share scheme (case-insensitive),
// host (case-insensitive), and port. Default ports for the scheme are
// canonicalized so that "https://h" and "https://h:443" count as same-origin.
// Path/query are ignored.
func sameOriginURL(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	if !strings.EqualFold(a.Scheme, b.Scheme) {
		return false
	}
	if !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	return canonicalPort(a.Scheme, a.Port()) == canonicalPort(b.Scheme, b.Port())
}

// canonicalPort returns the explicit port, or the scheme's default if empty.
func canonicalPort(scheme, port string) string {
	if port != "" {
		return port
	}
	switch strings.ToLower(scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// ---------------------------------------------------------------------------
// MCPClient (Step 2)
// ---------------------------------------------------------------------------

// MCPClient manages a single MCP server connection.
