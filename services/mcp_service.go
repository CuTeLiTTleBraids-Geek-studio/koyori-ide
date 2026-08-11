package services

import (
	"bufio"
	"bytes"
	"context"
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adrg/xdg"
	"github.com/wailsapp/wails/v3/pkg/application"
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
//     RiskDangerous. No tool is auto-approved unless AutoApprove is set
//     on the server config AND the tool name is in the AutoApprove list.
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
// G-SEC-02: AutoApprove is an explicit allowlist of tool names that may
// execute without user approval. It defaults to empty (no auto-approve).
// Even with AutoApprove, the tool still appears in the audit log.
type MCPServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // "stdio" | "sse" | "http"
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   bool              `json:"enabled"`
	// AutoApprove is retained only for decoding old settings. It is never
	// trusted from renderer input; approvals are always issued by the backend.
	AutoApprove []string `json:"autoApprove,omitempty"`
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

// jsonrpcRequest is a JSON-RPC 2.0 request/notification.
type jsonrpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"` // nil for notifications
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// jsonrpcResponse is a JSON-RPC 2.0 response.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
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
	Send(ctx context.Context, req *jsonrpcRequest) error
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
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	stdoutPipe io.ReadCloser
	sendMu     sync.Mutex
	closed     atomic.Bool
	closeOnce  sync.Once
	closeErr   error
}

func newStdioTransport(ctx context.Context, cfg MCPServerConfig) (*stdioTransport, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("stdio transport requires a command: %w", ErrInvalidInput)
	}
	cmd := commandContext(ctx, cfg.Command, cfg.Args...)
	// Inherit a minimal env: parent env + user-provided overrides.
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
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
	return &stdioTransport{
		cmd:        cmd,
		stdin:      stdin,
		stdout:     bufio.NewReader(stdout),
		stdoutPipe: stdout,
	}, nil
}

func (t *stdioTransport) Send(ctx context.Context, req *jsonrpcRequest) error {
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
		processTerminated := false
		if t.stdin != nil {
			if err := t.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				closeErrs = append(closeErrs, fmt.Errorf("close stdio stdin: %w", err))
			}
		}
		if t.cmd != nil && t.cmd.Process != nil && t.cmd.ProcessState == nil {
			if err := t.cmd.Process.Kill(); err != nil {
				if errors.Is(err, os.ErrProcessDone) {
					processTerminated = true
				} else {
					closeErrs = append(closeErrs, fmt.Errorf("kill stdio process: %w", err))
				}
			} else {
				processTerminated = true
			}
		}
		// Closing the read side unblocks a concurrent Recv even if process
		// termination failed, so shutdown never depends on server cooperation.
		if t.stdoutPipe != nil {
			if err := t.stdoutPipe.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				closeErrs = append(closeErrs, fmt.Errorf("close stdio stdout: %w", err))
			}
		}
		if t.cmd != nil && t.cmd.Process != nil && t.cmd.ProcessState == nil {
			if err := waitForMCPCommand(t.cmd, mcpTransportCloseTimeout); err != nil {
				var exitErr *exec.ExitError
				if !processTerminated || !errors.As(err, &exitErr) {
					closeErrs = append(closeErrs, fmt.Errorf("wait for stdio process: %w", err))
				}
			}
		}
		t.closeErr = errors.Join(closeErrs...)
	})
	return t.closeErr
}

func waitForMCPCommand(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("timed out after %s: %w", timeout, context.DeadlineExceeded)
	}
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

func newHTTPTransport(cfg MCPServerConfig) *httpTransport {
	return &httpTransport{
		url:     cfg.URL,
		headers: cfg.Headers,
		// C-1: use SSRF-safe transport so the actual dial re-validates the
		// resolved IP, defeating DNS rebinding between SaveServer's URL
		// validation and the HTTP request.
		client: &http.Client{Timeout: 60 * time.Second, Transport: NewSSRFSafeTransport()},
	}
}

func (t *httpTransport) Send(ctx context.Context, req *jsonrpcRequest) error {
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
func (t *httpTransport) postRequest(ctx context.Context, req *jsonrpcRequest) (*jsonrpcResponse, error) {
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
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("mcp server returned %d: %s", resp.StatusCode, string(body))
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
	return &sseTransport{
		url:     cfg.URL,
		headers: cfg.Headers,
		// C-1: SSRF-safe transport (same rationale as newHTTPTransport).
		client:        &http.Client{Timeout: 0, Transport: NewSSRFSafeTransport()}, // no timeout for SSE
		events:        make(chan jsonrpcResponse, 16),
		done:          make(chan struct{}),
		endpointReady: make(chan struct{}),
	}
}

func (t *sseTransport) Send(ctx context.Context, req *jsonrpcRequest) error {
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
	if resp.StatusCode >= 400 {
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

// validateSSEPostURL enforces C-2: the postURL advertised by an SSE server
// must be same-origin (scheme + host + port) with the URL we initially
// connected to, and must independently pass the SSRF check — otherwise a
// malicious server could funnel JSON-RPC bodies (which may include file
// contents or Authorization headers) to an attacker or an internal service.
func validateSSEPostURL(baseURL, postURL string) error {
	_, err := normalizeSSEPostURL(baseURL, postURL)
	return err
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
type mcpCallResult struct {
	response *jsonrpcResponse
	err      error
}

type mcpClientRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	calls  sync.WaitGroup
}

func newMCPClientRun() *mcpClientRun {
	ctx, cancel := context.WithCancel(context.Background())
	return &mcpClientRun{ctx: ctx, cancel: cancel}
}

type MCPClient struct {
	cfg              MCPServerConfig
	transport        mcpTransport
	nextID           int64
	mu               sync.Mutex
	sendMu           sync.Mutex
	closed           bool
	run              *mcpClientRun
	pending          map[string]chan mcpCallResult
	dispatchCancel   context.CancelFunc
	dispatchDone     chan struct{}
	dispatchErr      error
	toolsCache       []MCPTool
	toolsCachedAt    time.Time
	toolsCacheValid  bool
	toolsRefreshDone chan struct{}
}

const mcpToolsCacheTTL = 30 * time.Second

// newMCPClient creates a client for the given config but does not connect.
func newMCPClient(cfg MCPServerConfig) *MCPClient {
	return &MCPClient{
		cfg:     cfg,
		pending: make(map[string]chan mcpCallResult),
	}
}

// StartServer establishes the connection and performs the MCP initialize
// handshake (Step 2).
func (c *MCPClient) StartServer(ctx context.Context) error {
	var transport mcpTransport
	switch c.cfg.Transport {
	case "stdio":
		t, err := newStdioTransport(ctx, c.cfg)
		if err != nil {
			return err
		}
		transport = t
	case "sse":
		t := newSSETransport(c.cfg)
		if err := t.connect(ctx); err != nil {
			return err
		}
		transport = t
	case "http":
		transport = newHTTPTransport(c.cfg)
	default:
		return fmt.Errorf("unknown transport %q: %w", c.cfg.Transport, ErrInvalidInput)
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.Join(fmt.Errorf("client stopped: %w", ErrInvalidInput), transport.Close())
	}
	if c.transport != nil {
		c.mu.Unlock()
		return errors.Join(fmt.Errorf("client already started: %w", ErrAlreadyExists), transport.Close())
	}
	c.transport = transport
	c.run = newMCPClientRun()
	c.dispatchErr = nil
	c.toolsCache = nil
	c.toolsCachedAt = time.Time{}
	c.toolsCacheValid = false
	if _, ok := transport.(*httpTransport); !ok {
		c.startResponseDispatcherLocked(transport)
	}
	c.mu.Unlock()

	// Perform initialize handshake.
	if err := c.initialize(ctx); err != nil {
		return errors.Join(err, c.stopTransport(false))
	}
	return nil
}

// startResponseDispatcherLocked starts the single reader for transports whose
// responses arrive independently from Send. c.mu must be held by the caller.
func (c *MCPClient) startResponseDispatcherLocked(transport mcpTransport) {
	// FIX B1: call previously held c.mu while blocking in Recv, serializing all
	// requests. A dedicated reader now dispatches responses to buffered waiters.
	dispatchCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.dispatchCancel = cancel
	c.dispatchDone = done
	go c.dispatchResponses(dispatchCtx, transport, done)
}

func (c *MCPClient) dispatchResponses(ctx context.Context, transport mcpTransport, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			c.failPendingForTransport(transport, fmt.Errorf("response dispatcher stopped: %w", ctx.Err()))
			return
		default:
		}

		resp, err := transport.Recv()
		if err != nil {
			c.failPendingForTransport(transport, fmt.Errorf("receive response: %w", err))
			return
		}
		if resp == nil {
			continue
		}

		key := fmt.Sprint(resp.ID)
		c.mu.Lock()
		if c.transport != transport {
			c.mu.Unlock()
			return
		}
		responseCh, ok := c.pending[key]
		if ok {
			delete(c.pending, key)
		}
		c.mu.Unlock()
		if ok {
			// The channel is buffered(1), so an abandoned caller cannot block
			// the one transport reader and starve unrelated requests.
			responseCh <- mcpCallResult{response: resp}
		}
	}
}

func (c *MCPClient) failPendingForTransport(transport mcpTransport, err error) {
	c.mu.Lock()
	if c.transport != transport {
		c.mu.Unlock()
		return
	}
	c.dispatchErr = err
	pending := c.pending
	c.pending = make(map[string]chan mcpCallResult)
	c.mu.Unlock()
	deliverMCPPending(pending, err)
}

func deliverMCPPending(pending map[string]chan mcpCallResult, err error) {
	for _, responseCh := range pending {
		responseCh <- mcpCallResult{err: err}
	}
}

func contextForMCPRun(ctx context.Context, run *mcpClientRun) (context.Context, context.CancelFunc) {
	requestCtx, cancel := context.WithCancel(ctx)
	if run == nil {
		return requestCtx, cancel
	}
	stopLifecycleCancel := context.AfterFunc(run.ctx, cancel)
	return requestCtx, func() {
		stopLifecycleCancel()
		cancel()
	}
}

// initialize sends the MCP initialize request + initialized notification.
func (c *MCPClient) initialize(ctx context.Context) error {
	resp, err := c.call(ctx, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "koyori-ide",
			"version": "1.0",
		},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	_ = resp // server capabilities; we accept all
	// Send initialized notification (no response expected).
	return c.notify(ctx, "notifications/initialized", map[string]interface{}{})
}

// call sends a JSON-RPC request and waits for the response.
func (c *MCPClient) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed || c.transport == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("client not started: %w", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("call %s: %w", method, err)
	}
	transport := c.transport
	run := c.run
	if run == nil {
		run = newMCPClientRun()
		c.run = run
	}
	run.calls.Add(1)
	id := atomic.AddInt64(&c.nextID, 1)
	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	// HTTP transport uses a combined send+recv via postRequest.
	if ht, ok := transport.(*httpTransport); ok {
		c.mu.Unlock()
		defer run.calls.Done()
		requestCtx, cancel := contextForMCPRun(ctx, run)
		defer cancel()
		resp, err := ht.postRequest(requestCtx, req)
		if err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
	if c.dispatchErr != nil {
		err := c.dispatchErr
		c.mu.Unlock()
		run.calls.Done()
		return nil, fmt.Errorf("response dispatcher unavailable: %w", err)
	}

	// stdio / SSE: register before Send so a fast response cannot outrun
	// pending registration. FIX B1: transport I/O must happen outside c.mu;
	// otherwise one blocked Send prevents the dispatcher and StopServer from
	// acquiring the state lock, deadlocking every in-flight request.
	responseCh := make(chan mcpCallResult, 1)
	key := fmt.Sprint(id)
	c.pending[key] = responseCh
	c.mu.Unlock()
	defer run.calls.Done()

	sendCtx, cancelSend := contextForMCPRun(ctx, run)
	c.sendMu.Lock()
	err := transport.Send(sendCtx, req)
	c.sendMu.Unlock()
	cancelSend()
	if err != nil {
		c.mu.Lock()
		if pendingCh, ok := c.pending[key]; ok && pendingCh == responseCh {
			delete(c.pending, key)
		}
		c.mu.Unlock()
		return nil, fmt.Errorf("send %s: %w", method, err)
	}

	var result mcpCallResult
	select {
	case result = <-responseCh:
	case <-ctx.Done():
		c.mu.Lock()
		if pendingCh, ok := c.pending[key]; ok && pendingCh == responseCh {
			delete(c.pending, key)
		}
		c.mu.Unlock()
		return nil, fmt.Errorf("call %s: %w", method, ctx.Err())
	}
	if result.err != nil {
		return nil, fmt.Errorf("receive %s: %w", method, result.err)
	}
	resp := result.response
	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// notify sends a JSON-RPC notification (no id, no response).
func (c *MCPClient) notify(ctx context.Context, method string, params interface{}) error {
	c.mu.Lock()
	if c.closed || c.transport == nil {
		c.mu.Unlock()
		return fmt.Errorf("client not started: %w", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("notify %s: %w", method, err)
	}
	if c.dispatchErr != nil {
		err := c.dispatchErr
		c.mu.Unlock()
		return fmt.Errorf("response dispatcher unavailable: %w", err)
	}
	transport := c.transport
	run := c.run
	if run == nil {
		run = newMCPClientRun()
		c.run = run
	}
	run.calls.Add(1)
	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	c.mu.Unlock()
	defer run.calls.Done()

	sendCtx, cancelSend := contextForMCPRun(ctx, run)
	c.sendMu.Lock()
	err := transport.Send(sendCtx, req)
	c.sendMu.Unlock()
	cancelSend()
	if err != nil {
		return fmt.Errorf("notify %s: %w", method, err)
	}
	return nil
}

// ListTools returns the tools exposed by the server (Step 2).
func (c *MCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	for {
		now := time.Now()
		c.mu.Lock()
		if c.toolsCacheValid {
			age := now.Sub(c.toolsCachedAt)
			if age < mcpToolsCacheTTL {
				tools := cloneMCPTools(c.toolsCache)
				server := c.cfg.Name
				c.mu.Unlock()
				slog.Debug("mcp tools cache hit", "server", server, "count", len(tools), "age", age, "ttl", mcpToolsCacheTTL)
				return tools, nil
			}
		}
		if refreshDone := c.toolsRefreshDone; refreshDone != nil {
			server := c.cfg.Name
			c.mu.Unlock()
			slog.Debug("mcp tools refresh pending", "server", server)
			select {
			case <-refreshDone:
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("list tools: %w", ctx.Err())
			}
		}

		cacheWasValid := c.toolsCacheValid
		cacheAge := now.Sub(c.toolsCachedAt)
		refreshDone := make(chan struct{})
		c.toolsRefreshDone = refreshDone
		server := c.cfg.Name
		c.mu.Unlock()

		if cacheWasValid {
			slog.Debug("mcp tools cache expired", "server", server, "age", cacheAge, "ttl", mcpToolsCacheTTL)
		} else {
			slog.Debug("mcp tools cache miss", "server", server, "ttl", mcpToolsCacheTTL)
		}

		tools, err := c.fetchTools(ctx)
		c.mu.Lock()
		if err == nil && !c.closed && c.transport != nil {
			c.toolsCache = tools
			c.toolsCachedAt = time.Now()
			c.toolsCacheValid = true
		}
		if c.toolsRefreshDone == refreshDone {
			c.toolsRefreshDone = nil
			close(refreshDone)
		}
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		slog.Debug("mcp tools refreshed", "server", server, "count", len(tools))
		return cloneMCPTools(tools), nil
	}
}

func (c *MCPClient) fetchTools(ctx context.Context) ([]MCPTool, error) {
	raw, err := c.call(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	var result struct {
		Tools []MCPTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tools: %w", err)
	}
	return result.Tools, nil
}

func cloneMCPTools(tools []MCPTool) []MCPTool {
	if tools == nil {
		return nil
	}
	cloned := make([]MCPTool, len(tools))
	for i, tool := range tools {
		cloned[i] = tool
		cloned[i].InputSchema = cloneMCPInputSchema(tool.InputSchema)
	}
	return cloned
}

func cloneMCPInputSchema(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(schema))
	for key, value := range schema {
		cloned[key] = cloneMCPJSONValue(value)
	}
	return cloned
}

func cloneMCPJSONValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneMCPInputSchema(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for i, item := range typed {
			cloned[i] = cloneMCPJSONValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case map[string]string:
		return cloneMCPStringMap(typed)
	default:
		return typed
	}
}

// CallTool invokes a tool on the server (Step 2).
func (c *MCPClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (*MCPToolResult, error) {
	raw, err := c.call(ctx, "tools/call", map[string]interface{}{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, fmt.Errorf("call tool %q: %w", name, err)
	}
	var result MCPToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tool result: %w", err)
	}
	return &result, nil
}

// ListResources returns the resources exposed by the server (Step 2).
func (c *MCPClient) ListResources(ctx context.Context) ([]MCPResource, error) {
	raw, err := c.call(ctx, "resources/list", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	var result struct {
		Resources []MCPResource `json:"resources"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal resources: %w", err)
	}
	return result.Resources, nil
}

// ReadResource reads a resource by URI (Step 2).
func (c *MCPClient) ReadResource(ctx context.Context, uri string) (string, error) {
	raw, err := c.call(ctx, "resources/read", map[string]interface{}{"uri": uri})
	if err != nil {
		return "", fmt.Errorf("read resource: %w", err)
	}
	var result struct {
		Contents []struct {
			URI  string `json:"uri"`
			Text string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("unmarshal resource: %w", err)
	}
	if len(result.Contents) == 0 {
		return "", fmt.Errorf("empty resource: %w", ErrNotFound)
	}
	return result.Contents[0].Text, nil
}

// ListPrompts returns the prompt templates exposed by the server (Step 2).
func (c *MCPClient) ListPrompts(ctx context.Context) ([]MCPPrompt, error) {
	raw, err := c.call(ctx, "prompts/list", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("list prompts: %w", err)
	}
	var result struct {
		Prompts []MCPPrompt `json:"prompts"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal prompts: %w", err)
	}
	return result.Prompts, nil
}

// GetPrompt renders a prompt template by name (Step 2).
func (c *MCPClient) GetPrompt(ctx context.Context, name string, args map[string]string) ([]MCPContent, error) {
	raw, err := c.call(ctx, "prompts/get", map[string]interface{}{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, fmt.Errorf("get prompt %q: %w", name, err)
	}
	var result struct {
		Messages []struct {
			Role    string     `json:"role"`
			Content MCPContent `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal prompt: %w", err)
	}
	var contents []MCPContent
	for _, m := range result.Messages {
		contents = append(contents, m.Content)
	}
	return contents, nil
}

// StopServer closes the connection (Step 2).
func (c *MCPClient) StopServer() error {
	return c.stopTransport(true)
}

func (c *MCPClient) stopTransport(markClosed bool) error {
	c.mu.Lock()
	if markClosed && c.closed {
		c.mu.Unlock()
		return nil
	}
	if markClosed {
		c.closed = true
	}
	transport := c.transport
	run := c.run
	dispatchCancel := c.dispatchCancel
	dispatchDone := c.dispatchDone
	pending := c.pending
	c.transport = nil
	c.run = nil
	c.dispatchCancel = nil
	c.dispatchDone = nil
	c.dispatchErr = nil
	c.pending = make(map[string]chan mcpCallResult)
	c.toolsCache = nil
	c.toolsCachedAt = time.Time{}
	c.toolsCacheValid = false
	c.mu.Unlock()

	if dispatchCancel != nil {
		dispatchCancel()
	}
	if run != nil {
		run.cancel()
	}
	deliverMCPPending(pending, fmt.Errorf("client stopped: %w", context.Canceled))

	var closeErr error
	if transport != nil {
		closeErr = transport.Close()
	}
	if dispatchDone != nil {
		<-dispatchDone
	}
	if run != nil {
		run.calls.Wait()
	}
	return closeErr
}

// ---------------------------------------------------------------------------
// MCPService (Step 4/9)
// ---------------------------------------------------------------------------

// MCPService manages multiple MCP server connections and persists their
// configuration. It is the Wails-bound entry point for the frontend.
//
// G-SEC-09: config is persisted via atomicWriteJSON with 0600 permissions.
// G-SEC-12: MCP servers are treated as Restricted — Enabled defaults to
// false and activation requires explicit user approval.
type MCPService struct {
	mu           sync.RWMutex
	auditMu      sync.Mutex
	auditWG      sync.WaitGroup
	connectWG    sync.WaitGroup
	toolCallWG   sync.WaitGroup
	disconnectWG sync.WaitGroup
	config       MCPConfig
	// persistedConfig retains the encrypted representation corresponding to
	// config so unchanged secrets can be reused without mutating the keyring.
	persistedConfig MCPConfig
	// persistTail serializes config and workspace-root mutations without
	// holding mu while waiting for an earlier atomic write to finish.
	persistTail              <-chan struct{}
	persistConfig            func(MCPConfig) error
	encryptSecret            func(string, string) (string, error)
	deleteSecret             func(string) error
	cfgPath                  string
	clients                  map[string]*MCPClient
	rootDir                  string // workspace root for path validation (empty = no sandbox)
	rootGeneration           uint64
	workspaceContext         *WorkspaceContext
	onWorkspaceLeaseAcquired func()
	// lifecycleGeneration invalidates outstanding tool approvals whenever a
	// server connection or configuration can change what a server name means.
	lifecycleGeneration uint64
	onToolsChanged      func()
	auditLog            mcpAuditLog
	closed              bool
	shutdownCtx         context.Context
	shutdownCancel      context.CancelFunc
	approvalMu          sync.Mutex
	approvals           map[string]mcpToolApproval
	approveTool         func(server, tool, args string, risk RiskLevel) bool
}

// MCPServiceRootSetter is the narrow internal capability ProjectService uses
// to update the MCP workspace sandbox.
type MCPServiceRootSetter interface {
	setWorkspaceRoot(string) error
}

var _ MCPServiceRootSetter = (*MCPService)(nil)

type mcpToolApproval struct {
	server              string
	tool                string
	argsJSON            string
	rootGeneration      uint64
	lifecycleGeneration uint64
	expiresAt           time.Time
}

type mcpAuditLog interface {
	WriteString(string) (int, error)
	Close() error
}

// NewMCPService creates a new MCPService. The config is loaded from
// <configDir>/koyori-ide/mcp-servers.json (G-SEC-09: 0600).
func NewMCPService() *MCPService {
	cfgPath := filepath.Join(xdg.ConfigHome, "koyori-ide", "mcp-servers.json")
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	s := &MCPService{
		cfgPath:        cfgPath,
		clients:        make(map[string]*MCPClient),
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
		approveTool:    nativeMCPToolApproval,
	}
	if err := s.load(); err != nil {
		slog.Warn("mcp: failed to load config", "error", err, "path", cfgPath)
	}
	// Best-effort audit log (matches AgentService pattern).
	logPath := filepath.Join(xdg.CacheHome, "koyori-ide", "mcp-audit.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		slog.Warn("mcp: create audit log directory failed", "error", err)
	} else if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
		slog.Warn("mcp: open audit log failed", "error", err)
	} else {
		s.auditLog = f
	}
	return s
}

func nativeMCPToolApproval(server, tool, args string, risk RiskLevel) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Approve MCP tool call").SetMessage(
		fmt.Sprintf("Risk: %s\nServer: %s\nTool: %s\n\nArguments:\n%s", risk, server, tool, args),
	)
	dialog.AddButton("Yes").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("No").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

// setWorkspaceRoot sets the workspace root for stdio command path validation.
// When set, stdio Command paths must resolve within this root (unless they
// are absolute system paths like /usr/bin or C:\Windows\System32).
// The unexported capability is intentionally reachable only through
// ProjectService, never through a renderer binding.
func (s *MCPService) setWorkspaceRoot(root string) error {
	if root == "" {
		return fmt.Errorf("MCP workspace root is required: %w", ErrInvalidInput)
	}
	return s.applyWorkspaceRoot(root)
}

// setWorkspaceContext injects the shared workspace identity. MCP keeps its
// local root only to stop transports during coordinated switches; permission
// checks read the shared root and generation at call time.
//
//wails:ignore
func (s *MCPService) setWorkspaceContext(ctx *WorkspaceContext) {
	s.mu.Lock()
	s.workspaceContext = ctx
	s.mu.Unlock()
}

func (s *MCPService) acquireWorkspaceLease() (workspaceLease, uint64, error) {
	s.mu.RLock()
	ctx := s.workspaceContext
	root := s.rootDir
	rootGeneration := s.rootGeneration
	s.mu.RUnlock()
	if ctx == nil && root == "" {
		return workspaceLease{allowUnscoped: true}, rootGeneration, nil
	}
	lease, err := acquireWorkspaceLease(ctx, root, rootGeneration)
	if err != nil {
		return workspaceLease{}, 0, err
	}
	if ctx != nil && (root == "" || !sameWorkspaceIdentityPath(root, lease.root)) {
		return workspaceLease{}, 0, fmt.Errorf("MCP workspace switch is not committed: %w", ErrNotAllowed)
	}
	return lease, rootGeneration, nil
}

func (s *MCPService) notifyWorkspaceLeaseAcquired() {
	s.mu.RLock()
	hook := s.onWorkspaceLeaseAcquired
	s.mu.RUnlock()
	if hook != nil {
		hook()
	}
}

// restoreWorkspaceRoot is used only by ProjectService rollback. It can restore
// the pre-project empty state without exposing an empty-root sandbox bypass as
// a service method or Wails binding.
func (s *MCPService) restoreWorkspaceRoot(root string) error {
	return s.applyWorkspaceRoot(root)
}

func (s *MCPService) applyWorkspaceRoot(root string) error {
	if root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return fmt.Errorf("resolve MCP workspace root: %w", err)
		}
		root = filepath.Clean(abs)
	}
	previous, done := s.reserveConfigWrite()
	<-previous
	s.mu.Lock()
	if s.rootDir == root {
		s.mu.Unlock()
		close(done)
		return nil
	}
	s.rootDir = root
	s.rootGeneration++
	s.lifecycleGeneration++
	clients := s.clients
	s.clients = make(map[string]*MCPClient)
	callback := s.onToolsChanged
	s.mu.Unlock()
	close(done)
	for name, client := range clients {
		if err := client.StopServer(); err != nil {
			slog.Warn("mcp: disconnect after workspace change failed", "server", name, "err", err)
		}
	}
	if callback != nil {
		callback()
	}
	return nil
}

func (s *MCPService) WorkspaceRoot() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rootDir
}

//wails:ignore
func (s *MCPService) setOnToolsChanged(callback func()) {
	s.mu.Lock()
	s.onToolsChanged = callback
	s.mu.Unlock()
}

func (s *MCPService) notifyToolsChanged() {
	s.mu.RLock()
	callback := s.onToolsChanged
	s.mu.RUnlock()
	if callback != nil {
		callback()
	}
}

func (s *MCPService) shutdownContextLocked() context.Context {
	if s.shutdownCtx == nil {
		s.shutdownCtx, s.shutdownCancel = context.WithCancel(context.Background())
	}
	return s.shutdownCtx
}

// load reads the MCP config from disk. Missing file is not an error.
// G-SEC-07: Headers/Env values are stored encrypted on disk; after
// unmarshal we decrypt them into the in-memory config so MCP clients
// can use the real values when connecting.
func (s *MCPService) load() error {
	data, err := os.ReadFile(s.cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // fresh start — no config yet
		}
		return fmt.Errorf("read mcp config: %w", err)
	}
	var persisted MCPConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("parse mcp config: %w", err)
	}
	s.persistedConfig = MCPConfig{Servers: cloneMCPServerConfigs(persisted.Servers)}
	s.config = MCPConfig{Servers: cloneMCPServerConfigs(persisted.Servers)}
	// Decrypt secret-bearing maps into the in-memory (plaintext) config.
	for i := range s.config.Servers {
		decryptServerSecrets(&s.config.Servers[i])
		// AutoApprove is retained only for decoding legacy files. It is not a
		// backend-issued approval capability and must never survive loading.
		s.config.Servers[i].AutoApprove = nil
	}
	return nil
}

// persistConfigSnapshot persists an exact copy-on-write candidate via
// atomicWriteJSON with 0600 (G-SEC-09) and returns its encrypted form.
// G-SEC-07: Headers/Env values are encrypted before writing so secrets are
// never stored as plaintext on disk. The in-memory config retains
// plaintext for use by running MCP connections.
func (s *MCPService) persistConfigSnapshot(candidate MCPConfig) (MCPConfig, error) {
	s.mu.RLock()
	previousConfig := MCPConfig{Servers: cloneMCPServerConfigs(s.config.Servers)}
	previousPersisted := MCPConfig{Servers: cloneMCPServerConfigs(s.persistedConfig.Servers)}
	encryptSecret := s.encryptSecret
	deleteSecret := s.deleteSecret
	s.mu.RUnlock()
	if encryptSecret == nil {
		encryptSecret = EncryptSecret
	}
	if deleteSecret == nil {
		deleteSecret = DeleteSecret
	}

	previousPlaintext := mcpSecretValues(previousConfig)
	previousStored := mcpSecretValues(previousPersisted)
	servers := cloneMCPServerConfigs(candidate.Servers)
	var rollbackEntries []mcpSecretRollback
	for i := range servers {
		if err := encryptServerSecretsForDisk(&servers[i], previousPlaintext, previousStored, encryptSecret, &rollbackEntries); err != nil {
			encryptErr := fmt.Errorf("encrypt mcp server %q secrets: %w", servers[i].Name, err)
			if rollbackErr := rollbackMCPSecretWrites(rollbackEntries, encryptSecret, deleteSecret); rollbackErr != nil {
				return MCPConfig{}, fmt.Errorf("%w (keyring rollback failed: %v)", encryptErr, rollbackErr)
			}
			return MCPConfig{}, encryptErr
		}
	}
	enc := MCPConfig{Servers: servers}
	if s.persistConfig != nil {
		if err := s.persistConfig(enc); err != nil {
			persistErr := fmt.Errorf("write mcp config: %w", err)
			if rollbackErr := rollbackMCPSecretWrites(rollbackEntries, encryptSecret, deleteSecret); rollbackErr != nil {
				return MCPConfig{}, fmt.Errorf("%w (keyring rollback failed: %v)", persistErr, rollbackErr)
			}
			return MCPConfig{}, persistErr
		}
		return enc, nil
	}
	if err := atomicWriteJSON(s.cfgPath, enc, 0600); err != nil {
		persistErr := fmt.Errorf("write mcp config: %w", err)
		if rollbackErr := rollbackMCPSecretWrites(rollbackEntries, encryptSecret, deleteSecret); rollbackErr != nil {
			return MCPConfig{}, fmt.Errorf("%w (keyring rollback failed: %v)", persistErr, rollbackErr)
		}
		return MCPConfig{}, persistErr
	}
	return enc, nil
}

func (s *MCPService) reserveConfigWrite() (<-chan struct{}, chan struct{}) {
	s.mu.Lock()
	previous := s.persistTail
	if previous == nil {
		ready := make(chan struct{})
		close(ready)
		previous = ready
	}
	done := make(chan struct{})
	s.persistTail = done
	s.mu.Unlock()
	return previous, done
}

func cloneMCPServerConfigs(servers []MCPServerConfig) []MCPServerConfig {
	out := make([]MCPServerConfig, len(servers))
	for i, server := range servers {
		out[i] = cloneMCPServerConfig(server)
	}
	return out
}

func cloneMCPServerConfig(server MCPServerConfig) MCPServerConfig {
	out := server
	out.Args = append([]string(nil), server.Args...)
	out.AutoApprove = append([]string(nil), server.AutoApprove...)
	out.Env = cloneMCPStringMap(server.Env)
	out.Headers = cloneMCPStringMap(server.Headers)
	return out
}

func cloneMCPStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

const (
	// mcpSecretMask is the placeholder returned to the frontend in place of a
	// real secret value. The UI does not display Headers/Env, so masking is
	// invisible to the user while keeping plaintext out of the JS heap.
	mcpSecretMask = "***"

	mcpSecretAccountPrefix = "mcp:v1:"
	mcpSecretMapHeaders    = "headers"
	mcpSecretMapEnv        = "env"
)

// maskServerSecretsForView returns a copy of cfg with non-empty
// Headers/Env values replaced by mcpSecretMask. Empty values stay empty.
func maskServerSecretsForView(cfg MCPServerConfig) MCPServerConfig {
	out := cfg
	out.AutoApprove = nil
	out.Headers = maskSecretMap(cfg.Headers)
	out.Env = maskSecretMap(cfg.Env)
	return out
}

func maskSecretMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	masked := make(map[string]string, len(m))
	for k, v := range m {
		if v == "" {
			masked[k] = ""
		} else {
			masked[k] = mcpSecretMask
		}
	}
	return masked
}

// isMaskedSecret reports whether a value is the frontend mask placeholder.
func isMaskedSecret(v string) bool { return v == mcpSecretMask }

// mergeSecretMap merges incoming secret map onto existing. For each key:
//   - if the incoming value is the mask placeholder or empty AND the
//     existing value is non-empty, preserve the existing decrypted value
//     (the frontend did not change it — it only round-tripped the mask);
//   - otherwise adopt the incoming value (including newly-set plaintext).
func mergeSecretMap(existing, incoming map[string]string) map[string]string {
	if incoming == nil {
		// An omitted map means the caller did not edit this secret collection.
		return existing
	}
	out := make(map[string]string, len(incoming))
	for k, v := range incoming {
		if (v == "" || isMaskedSecret(v)) && existing[k] != "" {
			out[k] = existing[k]
			continue
		}
		out[k] = v
	}
	return out
}

type mcpSecretRollback struct {
	account           string
	previousPlaintext string
	previousExisted   bool
}

// encryptServerSecretsForDisk encrypts non-empty Headers/Env values in place
// for the on-disk copy. Unchanged encrypted values are reused so native
// keyring entries are not rewritten by unrelated config mutations.
func encryptServerSecretsForDisk(
	cfg *MCPServerConfig,
	previousPlaintext, previousStored map[string]string,
	encryptSecret func(string, string) (string, error),
	rollbackEntries *[]mcpSecretRollback,
) error {
	headers, err := encryptSecretMap(cfg.Name, mcpSecretMapHeaders, cfg.Headers, previousPlaintext, previousStored, encryptSecret, rollbackEntries)
	if err != nil {
		return fmt.Errorf("encrypt headers: %w", err)
	}
	env, err := encryptSecretMap(cfg.Name, mcpSecretMapEnv, cfg.Env, previousPlaintext, previousStored, encryptSecret, rollbackEntries)
	if err != nil {
		return fmt.Errorf("encrypt environment: %w", err)
	}
	cfg.Headers = headers
	cfg.Env = env
	return nil
}

func encryptSecretMap(
	serverName, mapKind string,
	m, previousPlaintext, previousStored map[string]string,
	encryptSecret func(string, string) (string, error),
	rollbackEntries *[]mcpSecretRollback,
) (map[string]string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v == "" || isMaskedSecret(v) || IsSecretEncrypted(v) {
			out[k] = v
			continue
		}
		account := mcpSecretAccount(serverName, mapKind, k)
		storedBefore := previousStored[account]
		if v == previousPlaintext[account] && IsSecretEncrypted(storedBefore) {
			out[k] = storedBefore
			continue
		}
		rollback := mcpSecretRollback{account: account}
		if strings.HasPrefix(storedBefore, secretPrefixKeyring) {
			rollback.previousPlaintext = previousPlaintext[account]
			if rollback.previousPlaintext == "" {
				return nil, fmt.Errorf("read existing secret %q before update: plaintext unavailable", k)
			}
			rollback.previousExisted = true
		}
		enc, err := encryptSecret(account, v)
		if err != nil {
			return nil, fmt.Errorf("encrypt secret %q: %w", k, err)
		}
		if strings.HasPrefix(enc, secretPrefixKeyring) {
			*rollbackEntries = append(*rollbackEntries, rollback)
		}
		out[k] = enc
	}
	return out, nil
}

func mcpSecretValues(config MCPConfig) map[string]string {
	values := make(map[string]string)
	for _, server := range config.Servers {
		for key, value := range server.Headers {
			values[mcpSecretAccount(server.Name, mcpSecretMapHeaders, key)] = value
		}
		for key, value := range server.Env {
			values[mcpSecretAccount(server.Name, mcpSecretMapEnv, key)] = value
		}
	}
	return values
}

func rollbackMCPSecretWrites(
	entries []mcpSecretRollback,
	encryptSecret func(string, string) (string, error),
	deleteSecret func(string) error,
) error {
	var rollbackErr error
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.previousExisted {
			stored, err := encryptSecret(entry.account, entry.previousPlaintext)
			if err == nil && !strings.HasPrefix(stored, secretPrefixKeyring) {
				err = fmt.Errorf("platform did not restore native keyring storage")
			}
			rollbackErr = errors.Join(rollbackErr, err)
			continue
		}
		rollbackErr = errors.Join(rollbackErr, deleteSecret(entry.account))
	}
	return rollbackErr
}

// decryptServerSecrets decrypts Headers/Env values in place (used after
// loading from disk to restore plaintext for in-memory use).
func decryptServerSecrets(cfg *MCPServerConfig) {
	var unsafeSecret bool
	cfg.Headers, unsafeSecret = decryptSecretMap(cfg.Name, mcpSecretMapHeaders, cfg.Headers)
	var unsafeEnvSecret bool
	cfg.Env, unsafeEnvSecret = decryptSecretMap(cfg.Name, mcpSecretMapEnv, cfg.Env)
	if unsafeSecret || unsafeEnvSecret {
		cfg.Enabled = false
	}
}

func decryptSecretMap(serverName, mapKind string, m map[string]string) (map[string]string, bool) {
	if len(m) == 0 {
		return nil, false
	}
	out := make(map[string]string, len(m))
	unsafeSecret := false
	for k, v := range m {
		account := mcpSecretAccount(serverName, mapKind, k)
		isEncrypted := IsSecretEncrypted(v)
		isKeyringMarker := strings.HasPrefix(v, secretPrefixKeyring)
		if isKeyringMarker {
			markerAccount, ok := mcpKeyringMarkerAccount(v)
			if !ok || markerAccount != account {
				slog.Warn("mcp: rejected unscoped keyring marker", "server", serverName, "kind", mapKind, "key", k)
				out[k] = ""
				unsafeSecret = true
				continue
			}
		}
		dec, err := DecryptSecret(account, v)
		if err != nil {
			slog.Debug("mcp: decrypt stored secret failed", "key", k, "err", err)
			if isEncrypted {
				out[k] = ""
				unsafeSecret = true
				continue
			}
			out[k] = v // best-effort: keep raw if decrypt fails
			continue
		}
		out[k] = dec
	}
	return out, unsafeSecret
}

func mcpSecretAccount(serverName, mapKind, key string) string {
	sum := sha256.Sum256([]byte(serverName + "\x00" + mapKind + "\x00" + key))
	return mcpSecretAccountPrefix + fmt.Sprintf("%x", sum)
}

func mcpKeyringMarkerAccount(stored string) (string, bool) {
	if !strings.HasPrefix(stored, secretPrefixKeyring) {
		return "", false
	}
	marker := strings.TrimPrefix(stored, secretPrefixKeyring)
	decoded, err := base64.StdEncoding.DecodeString(marker)
	if err != nil || len(decoded) == 0 {
		return "", false
	}
	return string(decoded), true
}

type mcpSecretRef struct {
	serverName string
	mapKind    string
	key        string
}

func removedMCPSecretRefs(oldCfg, newCfg MCPServerConfig) []mcpSecretRef {
	refs := removedMCPSecretMapRefs(oldCfg.Name, mcpSecretMapHeaders, oldCfg.Headers, newCfg.Headers)
	return append(refs, removedMCPSecretMapRefs(oldCfg.Name, mcpSecretMapEnv, oldCfg.Env, newCfg.Env)...)
}

func mcpSecretRefsForServer(cfg MCPServerConfig) []mcpSecretRef {
	refs := removedMCPSecretMapRefs(cfg.Name, mcpSecretMapHeaders, cfg.Headers, nil)
	return append(refs, removedMCPSecretMapRefs(cfg.Name, mcpSecretMapEnv, cfg.Env, nil)...)
}

func removedMCPSecretMapRefs(serverName, mapKind string, oldMap, newMap map[string]string) []mcpSecretRef {
	refs := make([]mcpSecretRef, 0)
	for key, value := range oldMap {
		if value == "" {
			continue
		}
		if _, stillPresent := newMap[key]; stillPresent {
			continue
		}
		refs = append(refs, mcpSecretRef{serverName: serverName, mapKind: mapKind, key: key})
	}
	return refs
}

func (s *MCPService) cleanupMCPSecrets(refs []mcpSecretRef) {
	if len(refs) == 0 {
		return
	}
	deleteSecret := s.deleteSecret
	if deleteSecret == nil {
		deleteSecret = DeleteSecret
	}
	for _, ref := range refs {
		// The existence check uses only a short state lock. DeleteSecret performs
		// keyring I/O after that lock has been released.
		if s.mcpSecretFieldExists(ref) {
			continue
		}
		if err := deleteSecret(mcpSecretAccount(ref.serverName, ref.mapKind, ref.key)); err != nil {
			slog.Warn("mcp: delete orphaned keyring secret failed", "server", ref.serverName, "kind", ref.mapKind, "key", ref.key, "err", err)
		}
	}
}

func (s *MCPService) mcpSecretFieldExists(ref mcpSecretRef) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, server := range s.config.Servers {
		if server.Name != ref.serverName {
			continue
		}
		var secrets map[string]string
		switch ref.mapKind {
		case mcpSecretMapHeaders:
			secrets = server.Headers
		case mcpSecretMapEnv:
			secrets = server.Env
		default:
			return false
		}
		_, exists := secrets[ref.key]
		return exists
	}
	return false
}

// ListServers returns all configured MCP servers. G-SEC-07: Headers/Env
// secret values are masked so plaintext never crosses the Wails binding.
func (s *MCPService) ListServers() []MCPServerConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MCPServerConfig, len(s.config.Servers))
	for i, srv := range s.config.Servers {
		out[i] = maskServerSecretsForView(srv)
	}
	return out
}

// GetServer returns a single server config by name. G-SEC-07: Headers/Env
// secret values are masked in the returned copy.
func (s *MCPService) GetServer(name string) (MCPServerConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, srv := range s.config.Servers {
		if srv.Name == name {
			return maskServerSecretsForView(srv), nil
		}
	}
	return MCPServerConfig{}, fmt.Errorf("mcp server %q: %w", name, ErrNotFound)
}

// SaveServer adds or updates a server config. Names must be unique.
// G-SEC-12: new servers default to Enabled=false (Restricted).
func (s *MCPService) SaveServer(cfg MCPServerConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("server name required: %w", ErrInvalidInput)
	}
	if cfg.Transport != "stdio" && cfg.Transport != "sse" && cfg.Transport != "http" {
		return fmt.Errorf("invalid transport %q: %w", cfg.Transport, ErrInvalidInput)
	}
	// Auto approval is a capability, not a renderer-controlled configuration
	// bit. Ignore it on every save, including updates from legacy clients.
	cfg = cloneMCPServerConfig(cfg)
	cfg.AutoApprove = nil
	// C-1: validate http/sse URLs before acquiring the mutex — URL validation
	// may perform DNS resolution and must not block other callers. This also
	// defangs the SSRF vector (http://169.254.169.254/...) and prevents the
	// HTTP transport from later carrying an Authorization header to an
	// internal endpoint.
	if cfg.Transport == "http" || cfg.Transport == "sse" {
		if _, err := ValidateNonPrivateURL(cfg.URL); err != nil {
			return fmt.Errorf("mcp %s url: %w", cfg.Transport, err)
		}
	}
	previous, done := s.reserveConfigWrite()
	release := func() {
		if done != nil {
			close(done)
			done = nil
		}
	}
	defer release()
	<-previous

	s.mu.RLock()
	root := s.rootDir
	servers := cloneMCPServerConfigs(s.config.Servers)
	s.mu.RUnlock()
	// Validate stdio command path if a workspace root is set.
	if cfg.Transport == "stdio" && root != "" && cfg.Command != "" {
		if _, err := ValidatePathWithinRoot(root, cfg.Command); err != nil {
			return fmt.Errorf("stdio command path outside workspace: %w", err)
		}
	}
	// Upsert.
	found := false
	var cleanupRefs []mcpSecretRef
	for i, srv := range servers {
		if srv.Name == cfg.Name {
			// Enabled is changed only through SetServerEnabled so false is never
			// confused with an omitted patch field.
			cfg.Enabled = srv.Enabled
			// G-SEC-07: ListServers masks Headers/Env. When the frontend
			// round-trips a masked/empty value back through SaveServer,
			// preserve the existing decrypted secret rather than overwriting
			// it with the mask placeholder.
			cfg.Headers = mergeSecretMap(srv.Headers, cfg.Headers)
			cfg.Env = mergeSecretMap(srv.Env, cfg.Env)
			cleanupRefs = removedMCPSecretRefs(srv, cfg)
			servers[i] = cfg
			found = true
			break
		}
	}
	if !found {
		// G-SEC-12: new servers start disabled.
		cfg.Enabled = false
		servers = append(servers, cfg)
	}
	candidate := MCPConfig{Servers: servers}
	persisted, err := s.persistConfigSnapshot(candidate)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.config = candidate
	s.persistedConfig = persisted
	s.lifecycleGeneration++
	s.mu.Unlock()
	s.cleanupMCPSecrets(cleanupRefs)
	release()
	s.notifyToolsChanged()
	return nil
}

// SetServerEnabled applies the explicit Restricted-capability decision.
// Disabling also disconnects the running client before returning.
func (s *MCPService) SetServerEnabled(name string, enabled bool) error {
	previous, done := s.reserveConfigWrite()
	release := func() {
		if done != nil {
			close(done)
			done = nil
		}
	}
	defer release()
	<-previous

	s.mu.RLock()
	servers := cloneMCPServerConfigs(s.config.Servers)
	s.mu.RUnlock()
	idx := -1
	for i := range servers {
		if servers[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("mcp server %q: %w", name, ErrNotFound)
	}
	servers[idx].Enabled = enabled
	candidate := MCPConfig{Servers: servers}
	persisted, err := s.persistConfigSnapshot(candidate)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.config = candidate
	s.persistedConfig = persisted
	s.lifecycleGeneration++
	var client *MCPClient
	if !enabled {
		client = s.clients[name]
		delete(s.clients, name)
	}
	s.mu.Unlock()
	release()
	var stopErr error
	if client != nil {
		stopErr = client.StopServer()
	}
	s.notifyToolsChanged()
	return stopErr
}

// DeleteServer removes a server config and stops its client if running.
func (s *MCPService) DeleteServer(name string) error {
	previous, done := s.reserveConfigWrite()
	release := func() {
		if done != nil {
			close(done)
			done = nil
		}
	}
	defer release()
	<-previous

	s.mu.RLock()
	servers := cloneMCPServerConfigs(s.config.Servers)
	s.mu.RUnlock()
	idx := -1
	for i, srv := range servers {
		if srv.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("mcp server %q: %w", name, ErrNotFound)
	}
	cleanupRefs := mcpSecretRefsForServer(servers[idx])
	servers = append(servers[:idx], servers[idx+1:]...)
	candidate := MCPConfig{Servers: servers}
	persisted, err := s.persistConfigSnapshot(candidate)
	if err != nil {
		return err
	}
	s.mu.Lock()
	// Detach the client while holding only the state lock. FIX B1: stopping a
	// transport may block on process/network shutdown and must happen outside
	// s.mu so unrelated service operations remain available.
	client := s.clients[name]
	if client != nil {
		delete(s.clients, name)
	}
	s.config = candidate
	s.persistedConfig = persisted
	s.lifecycleGeneration++
	s.mu.Unlock()
	s.cleanupMCPSecrets(cleanupRefs)
	release()
	var stopErr error
	if client != nil {
		stopErr = client.StopServer()
		slog.Info("mcp transport", "server", name, "event", "disconnected")
	}
	s.notifyToolsChanged()
	return stopErr
}

// ConnectServer starts the MCP client for a configured server.
// G-SEC-12: the caller must have explicitly set Enabled=true via SaveServer
// (which requires user approval in the UI).
func (s *MCPService) ConnectServer(ctx context.Context, name string) error {
	lease, rootGeneration, err := s.acquireWorkspaceLease()
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("mcp service closed: %w", ErrInvalidInput)
	}
	cfg, err := s.getServerLocked(name)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if !cfg.Enabled {
		s.mu.Unlock()
		return fmt.Errorf("server %q not enabled (G-SEC-12): %w", name, ErrUnauthorized)
	}
	if _, ok := s.clients[name]; ok {
		s.mu.Unlock()
		return fmt.Errorf("server %q already connected: %w", name, ErrAlreadyExists)
	}
	cfg = cloneMCPServerConfig(cfg)
	root := lease.root
	shutdownCtx := s.shutdownContextLocked()
	s.connectWG.Add(1)
	s.mu.Unlock()
	defer s.connectWG.Done()
	connectCtx, cancelConnect := context.WithCancel(shutdownCtx)
	callerCancelDone := make(chan struct{})
	var callerCancelDoneOnce sync.Once
	finishCallerCancel := func() {
		callerCancelDoneOnce.Do(func() { close(callerCancelDone) })
	}
	stopCallerCancel := context.AfterFunc(ctx, func() {
		cancelConnect()
		finishCallerCancel()
	})
	if ctx.Err() != nil {
		cancelConnect()
	}
	callerHookDetached := false
	detachCallerHook := func() {
		if callerHookDetached {
			return
		}
		if stopCallerCancel() {
			finishCallerCancel()
		}
		<-callerCancelDone
		callerHookDetached = true
	}
	installed := false
	defer func() {
		detachCallerHook()
		if !installed {
			cancelConnect()
		}
	}()
	client := newMCPClient(cfg)
	if cfg.Transport == "stdio" && root != "" && cfg.Command != "" {
		if _, err := ValidatePathWithinRoot(root, cfg.Command); err != nil {
			return fmt.Errorf("stdio command path outside current workspace: %w", err)
		}
	}
	if err := lease.validateCurrent(); err != nil {
		return err
	}
	if err := client.StartServer(connectCtx); err != nil {
		return fmt.Errorf("start server %q: %w", name, err)
	}
	detachCallerHook()
	if err := connectCtx.Err(); err != nil {
		callerErr := ctx.Err()
		if callerErr == nil {
			callerErr = err
		}
		return errors.Join(
			fmt.Errorf("connect server %q canceled: %w", name, callerErr),
			client.StopServer(),
		)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("mcp service closed during connect: %w", ErrInvalidInput),
			client.StopServer(),
		)
	}
	if s.rootGeneration != rootGeneration || !sameWorkspaceIdentityPath(s.rootDir, root) {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("workspace changed during MCP connect: %w", ErrInvalidInput),
			client.StopServer(),
		)
	}
	if err := lease.validateCurrent(); err != nil {
		s.mu.Unlock()
		return errors.Join(err, client.StopServer())
	}
	currentCfg, err := s.getServerLocked(name)
	if err != nil {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("server %q was deleted during connect: %w", name, ErrNotFound),
			client.StopServer(),
		)
	}
	if !currentCfg.Enabled {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("server %q was disabled during connect: %w", name, ErrUnauthorized),
			client.StopServer(),
		)
	}
	if !reflect.DeepEqual(currentCfg, cfg) {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("server %q configuration changed during connect: %w", name, ErrInvalidInput),
			client.StopServer(),
		)
	}
	if _, ok := s.clients[name]; ok {
		s.mu.Unlock()
		return errors.Join(
			fmt.Errorf("server %q already connected (concurrent): %w", name, ErrAlreadyExists),
			client.StopServer(),
		)
	}
	s.clients[name] = client
	s.lifecycleGeneration++
	installed = true
	s.mu.Unlock()
	slog.Info("mcp transport", "server", name, "event", "connected")
	s.audit("connect", name, "")
	s.notifyToolsChanged()
	return nil
}

// DisconnectServer stops a running MCP client.
func (s *MCPService) DisconnectServer(name string) error {
	s.mu.Lock()
	client, ok := s.clients[name]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("server %q not connected: %w", name, ErrNotFound)
	}
	s.disconnectWG.Add(1)
	delete(s.clients, name)
	s.lifecycleGeneration++
	s.mu.Unlock()
	defer s.disconnectWG.Done()
	err := client.StopServer()
	slog.Info("mcp transport", "server", name, "event", "disconnected")
	s.audit("disconnect", name, "")
	s.notifyToolsChanged()
	if err != nil {
		return fmt.Errorf("disconnect server %q: %w", name, err)
	}
	return nil
}

// ListTools queries a connected server for its tools.
func (s *MCPService) ListTools(ctx context.Context, name string) ([]MCPTool, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	s.notifyWorkspaceLeaseAcquired()
	client, err := s.getClient(name)
	if err != nil {
		return nil, err
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	return tools, nil
}

// CallTool is kept as a deny-only Wails endpoint. Renderer calls must use a
// backend-issued approval capability via RequestToolApproval and
// ExecuteApprovedTool.
func (s *MCPService) CallTool(ctx context.Context, server, tool string, args map[string]interface{}) (*MCPToolResult, error) {
	return nil, fmt.Errorf("backend MCP approval token required: %w", ErrInvalidInput)
}

// RequestToolApproval creates a short-lived, single-use capability bound to
// one connected server, tool, argument payload, and workspace generation.
func (s *MCPService) RequestToolApproval(ctx context.Context, server, tool string, args map[string]interface{}) (string, error) {
	if strings.TrimSpace(server) == "" || strings.TrimSpace(tool) == "" {
		return "", fmt.Errorf("server and tool are required: %w", ErrInvalidInput)
	}
	argsBytes, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("encode MCP tool arguments: %w", err)
	}
	lease, rootGeneration, err := s.acquireWorkspaceLease()
	if err != nil {
		return "", err
	}

	s.mu.RLock()
	var cfg *MCPServerConfig
	for i := range s.config.Servers {
		if s.config.Servers[i].Name == server {
			copy := s.config.Servers[i]
			cfg = &copy
			break
		}
	}
	_, connected := s.clients[server]
	lifecycleGeneration := s.lifecycleGeneration
	s.mu.RUnlock()
	if cfg == nil || !cfg.Enabled || !connected {
		return "", fmt.Errorf("MCP server %q is not enabled and connected: %w", server, ErrNotAllowed)
	}

	tools, err := s.ListTools(ctx, server)
	if err != nil {
		return "", err
	}
	description := ""
	found := false
	for _, candidate := range tools {
		if candidate.Name == tool {
			description = candidate.Description
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("MCP tool %q not found on server %q: %w", tool, server, ErrNotFound)
	}
	if s.approveTool == nil || !s.approveTool(server, tool, string(argsBytes), ClassifyMCPToolRisk(tool, description)) {
		return "", fmt.Errorf("MCP tool call was not approved: %w", ErrNotAllowed)
	}
	if err := lease.validateCurrent(); err != nil {
		return "", err
	}

	raw := make([]byte, 32)
	if _, err := crypto_rand.Read(raw); err != nil {
		return "", fmt.Errorf("create MCP approval token: %w", err)
	}
	token := hex.EncodeToString(raw)
	s.approvalMu.Lock()
	if s.approvals == nil {
		s.approvals = make(map[string]mcpToolApproval)
	}
	s.approvals[token] = mcpToolApproval{
		server: server, tool: tool, argsJSON: string(argsBytes),
		rootGeneration: rootGeneration, lifecycleGeneration: lifecycleGeneration,
		expiresAt: time.Now().Add(2 * time.Minute),
	}
	s.approvalMu.Unlock()
	return token, nil
}

// ExecuteApprovedTool consumes a backend-issued capability and invokes the
// exact tool call it authorizes. Tokens cannot be replayed.
func (s *MCPService) ExecuteApprovedTool(ctx context.Context, server, tool string, args map[string]interface{}, approvalToken string) (*MCPToolResult, error) {
	argsBytes, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode MCP tool arguments: %w", err)
	}
	lease, rootGeneration, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	lifecycleGeneration := s.lifecycleGeneration
	s.mu.RUnlock()
	s.approvalMu.Lock()
	approval, ok := s.approvals[approvalToken]
	if ok {
		delete(s.approvals, approvalToken)
	}
	s.approvalMu.Unlock()
	if !ok || approvalToken == "" || time.Now().After(approval.expiresAt) ||
		approval.server != server || approval.tool != tool || approval.argsJSON != string(argsBytes) ||
		approval.rootGeneration != rootGeneration || approval.lifecycleGeneration != lifecycleGeneration {
		return nil, fmt.Errorf("invalid, expired, or mismatched MCP approval: %w", ErrInvalidInput)
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	return s.callToolWithLease(ctx, server, tool, args, lease)
}

func (s *MCPService) callTool(ctx context.Context, server, tool string, args map[string]interface{}) (*MCPToolResult, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	return s.callToolWithLease(ctx, server, tool, args, lease)
}

func (s *MCPService) callToolWithLease(ctx context.Context, server, tool string, args map[string]interface{}, lease workspaceLease) (*MCPToolResult, error) {
	started := time.Now()
	var client *MCPClient
	var finishCall func()
	err := lease.withCurrent(func() error {
		var beginErr error
		client, finishCall, beginErr = s.beginToolCall(server)
		return beginErr
	})
	if finishCall != nil {
		defer finishCall()
	}
	if err != nil {
		slog.Warn("mcp tool call failed", "server", server, "tool", tool, "duration", time.Since(started), "error", err)
		s.audit("call_tool_failed", server, tool)
		return nil, err
	}
	result, err := client.CallTool(ctx, tool, args)
	duration := time.Since(started)
	if err != nil {
		slog.Warn("mcp tool call failed", "server", server, "tool", tool, "duration", duration, "error", redactMCPToolCallError(err, client.cfg, args))
		s.audit("call_tool_failed", server, tool)
		return nil, err
	}
	if err := lease.validateCurrent(); err != nil {
		slog.Warn("mcp tool result rejected after workspace switch", "server", server, "tool", tool, "duration", duration, "error", err)
		s.audit("call_tool_failed", server, tool)
		return nil, err
	}
	if result != nil && result.IsError {
		slog.Warn("mcp tool call failed", "server", server, "tool", tool, "duration", duration, "error", "tool returned an error result")
		s.audit("call_tool_failed", server, tool)
		return result, nil
	}
	slog.Info("mcp tool call", "server", server, "tool", tool, "duration", duration)
	s.audit("call_tool", server, tool)
	return result, nil
}

func (s *MCPService) beginToolCall(server string) (*MCPClient, func(), error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, nil, fmt.Errorf("mcp service closed: %w", ErrInvalidInput)
	}
	s.toolCallWG.Add(1)
	client, ok := s.clients[server]
	s.mu.RUnlock()

	var once sync.Once
	finish := func() { once.Do(s.toolCallWG.Done) }
	if !ok {
		return nil, finish, fmt.Errorf("server %q not connected: %w", server, ErrNotFound)
	}
	return client, finish, nil
}

func redactMCPToolCallError(err error, cfg MCPServerConfig, args map[string]interface{}) string {
	if err == nil {
		return ""
	}
	secretValues := make([]string, 0, len(cfg.Headers)+len(cfg.Env))
	for _, value := range cfg.Headers {
		secretValues = append(secretValues, value)
	}
	for _, value := range cfg.Env {
		secretValues = append(secretValues, value)
	}
	appendMCPArgumentStrings(&secretValues, args)
	return sanitizeHTTPError(err.Error(), secretValues, cfg.URL)
}

func appendMCPArgumentStrings(values *[]string, value interface{}) {
	switch typed := value.(type) {
	case string:
		*values = append(*values, typed)
	case map[string]interface{}:
		for _, nested := range typed {
			appendMCPArgumentStrings(values, nested)
		}
	case []interface{}:
		for _, nested := range typed {
			appendMCPArgumentStrings(values, nested)
		}
	case map[string]string:
		for _, nested := range typed {
			*values = append(*values, nested)
		}
	case []string:
		*values = append(*values, typed...)
	}
}

// ListResources queries a connected server for its resources.
func (s *MCPService) ListResources(ctx context.Context, name string) ([]MCPResource, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	client, err := s.getClient(name)
	if err != nil {
		return nil, err
	}
	resources, err := client.ListResources(ctx)
	if err != nil {
		return nil, err
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	return resources, nil
}

// ReadResource reads a resource by URI from a connected server.
func (s *MCPService) ReadResource(ctx context.Context, name, uri string) (string, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return "", err
	}
	client, err := s.getClient(name)
	if err != nil {
		return "", err
	}
	resource, err := client.ReadResource(ctx, uri)
	if err != nil {
		return "", err
	}
	if err := lease.validateCurrent(); err != nil {
		return "", err
	}
	return resource, nil
}

// ListPrompts queries a connected server for its prompts.
func (s *MCPService) ListPrompts(ctx context.Context, name string) ([]MCPPrompt, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	client, err := s.getClient(name)
	if err != nil {
		return nil, err
	}
	prompts, err := client.ListPrompts(ctx)
	if err != nil {
		return nil, err
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	return prompts, nil
}

// GetPrompt renders a prompt template from a connected server.
func (s *MCPService) GetPrompt(ctx context.Context, name, prompt string, args map[string]string) ([]MCPContent, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	client, err := s.getClient(name)
	if err != nil {
		return nil, err
	}
	contents, err := client.GetPrompt(ctx, prompt, args)
	if err != nil {
		return nil, err
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	return contents, nil
}

// Close shuts down all running MCP clients. Called on app shutdown.
func (s *MCPService) Close() error {
	s.mu.Lock()
	s.closed = true
	shutdownCancel := s.shutdownCancel
	type namedClient struct {
		name   string
		client *MCPClient
	}
	clients := make([]namedClient, 0, len(s.clients))
	for name, client := range s.clients {
		clients = append(clients, namedClient{name: name, client: client})
	}
	s.clients = make(map[string]*MCPClient)
	s.lifecycleGeneration++
	s.mu.Unlock()
	if shutdownCancel != nil {
		shutdownCancel()
	}

	var closeErrs []error
	for _, entry := range clients {
		if err := entry.client.StopServer(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("stop mcp server %q: %w", entry.name, err))
		}
		slog.Info("mcp transport", "server", entry.name, "event", "disconnected")
	}
	// ConnectServer reserves connectWG while holding s.mu before starting any
	// transport. The closed gate prevents new reservations after this point.
	s.connectWG.Wait()
	// CallTool reserves toolCallWG under s.mu before looking up its client. This
	// covers response decoding, structured logging, and the final audit record,
	// all of which happen after the client's transport-level call has finished.
	s.toolCallWG.Wait()
	s.disconnectWG.Wait()
	s.auditMu.Lock()
	auditLog := s.auditLog
	s.auditLog = nil
	s.auditMu.Unlock()
	if auditLog != nil {
		s.auditWG.Wait()
		if err := auditLog.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("close mcp audit log: %w", err))
		}
	}
	return errors.Join(closeErrs...)
}

// ---------------------------------------------------------------------------
// Agent integration (Step 6/8)
// ---------------------------------------------------------------------------

// AgentMCPTool describes an MCP tool registered with the agent. The name
// follows the mcp.<server>.<tool> namespace (Step 6).
type AgentMCPTool struct {
	Namespace    string                 `json:"namespace"` // mcp.<server>.<tool>
	Server       string                 `json:"server"`
	Tool         string                 `json:"tool"`
	Description  string                 `json:"description"`
	InputSchema  map[string]interface{} `json:"inputSchema"`
	RiskLevel    RiskLevel              `json:"riskLevel"`
	AutoApproved bool                   `json:"autoApproved"`
}

// ListAgentMCPTools returns all tools from all connected MCP servers,
// namespaced as mcp.<server>.<tool> (Step 6).
func (s *MCPService) ListAgentMCPTools(ctx context.Context) ([]AgentMCPTool, error) {
	lease, _, err := s.acquireWorkspaceLease()
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	names := make([]string, 0, len(s.clients))
	for name := range s.clients {
		names = append(names, name)
	}
	s.mu.RUnlock()
	var tools []AgentMCPTool
	for _, server := range names {
		client, err := s.getClient(server)
		if err != nil {
			continue
		}
		serverTools, err := client.ListTools(ctx)
		if err != nil {
			slog.Warn("mcp: list tools failed", "server", server, "error", err)
			continue
		}
		for _, t := range serverTools {
			risk := ClassifyMCPToolRisk(t.Name, t.Description)
			tools = append(tools, AgentMCPTool{
				Namespace:    fmt.Sprintf("mcp.%s.%s", server, t.Name),
				Server:       server,
				Tool:         t.Name,
				Description:  t.Description,
				InputSchema:  t.InputSchema,
				RiskLevel:    risk,
				AutoApproved: false,
			})
		}
		if err := lease.validateCurrent(); err != nil {
			return nil, err
		}
	}
	if err := lease.validateCurrent(); err != nil {
		return nil, err
	}
	return tools, nil
}

// ClassifyMCPToolRisk determines the risk level of an MCP tool (Step 8).
//
// G-SEC-02: all MCP tools default to RiskElevated. Tools whose names or
// descriptions suggest write/network/exec operations are classified as
// RiskDangerous. No MCP tool is ever classified as RiskSafe — even with
// AutoApprove, the audit log records every call.
func ClassifyMCPToolRisk(name, description string) RiskLevel {
	combined := strings.ToLower(name + " " + description)
	// RiskDangerous: write/exec/network/file operations.
	dangerousKeywords := []string{
		"write", "create", "delete", "remove", "exec", "run", "shell",
		"command", "spawn", "kill", "move", "rename", "upload", "download",
		"fetch", "request", "post", "put", "patch",
	}
	for _, kw := range dangerousKeywords {
		if strings.Contains(combined, kw) {
			return RiskDangerous
		}
	}
	return RiskElevated
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (s *MCPService) getClient(name string) (*MCPClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	client, ok := s.clients[name]
	if !ok {
		return nil, fmt.Errorf("server %q not connected: %w", name, ErrNotFound)
	}
	return client, nil
}

func (s *MCPService) getServerLocked(name string) (MCPServerConfig, error) {
	for _, srv := range s.config.Servers {
		if srv.Name == name {
			return srv, nil
		}
	}
	return MCPServerConfig{}, fmt.Errorf("mcp server %q: %w", name, ErrNotFound)
}

func (s *MCPService) audit(action, server, tool string) {
	s.auditMu.Lock()
	auditLog := s.auditLog
	if auditLog != nil {
		s.auditWG.Add(1)
	}
	s.auditMu.Unlock()
	if auditLog == nil {
		return
	}
	defer s.auditWG.Done()
	ts := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf("%s\t%s\tserver=%q\ttool=%q\n", ts, action, server, tool)
	_, err := auditLog.WriteString(line)
	if err != nil {
		slog.Warn("mcp: write audit log failed", "error", err, "action", action, "server", server, "tool", tool)
	}
}
