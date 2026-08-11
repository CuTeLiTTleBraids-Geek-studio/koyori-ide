package services

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// --- DAP protocol helpers ---

func dapInitializeCapabilitiesForRun(request dapRunRequest) (DebugAdapterCapabilities, error) {
	return dapInitializeCapabilitiesForRunWithAdapter(request, "go")
}

func dapInitializeCapabilitiesForRunWithAdapter(request dapRunRequest, adapterID string) (DebugAdapterCapabilities, error) {
	if adapterID == "" {
		return DebugAdapterCapabilities{}, fmt.Errorf("debugger adapter id is empty")
	}
	body, err := request("initialize", map[string]interface{}{
		"clientID":                     "koyori-ide",
		"clientName":                   "koyori-ide",
		"adapterID":                    adapterID,
		"pathFormat":                   "path",
		"linesStartAt1":                true,
		"columnsStartAt1":              true,
		"supportsVariableType":         true,
		"supportsVariablePaging":       false,
		"supportsRunInTerminalRequest": false,
	})
	if err != nil {
		return DebugAdapterCapabilities{}, err
	}
	var capabilities DebugAdapterCapabilities
	if err := json.Unmarshal(body, &capabilities); err != nil {
		return DebugAdapterCapabilities{}, fmt.Errorf("decode dap initialize response: %w", err)
	}
	return capabilities, nil
}

func initializeDAPSessionForRun(owner *DebugSession, generation uint64, request dapRunRequest) error {
	return initializeDAPSessionForRunWithAdapter(owner, generation, request, "go")
}

func initializeDAPSessionForRunWithAdapter(owner *DebugSession, generation uint64, request dapRunRequest, adapterID string) error {
	capabilities, err := dapInitializeCapabilitiesForRunWithAdapter(request, adapterID)
	if err != nil {
		return err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.runGeneration != generation {
		return fmt.Errorf("debug run changed")
	}
	owner.supportsDelayedStackTraceLoading = capabilities.SupportsDelayedStackTraceLoading
	return nil
}

func dapInitializeForRun(request dapRunRequest) error {
	_, err := dapInitializeCapabilitiesForRun(request)
	return err
}

func (d *DebugService) dapInitialize() error {
	return dapInitializeForRun(d.dapRequestBody)
}

func (d *DebugService) applyAllBreakpoints(bps []DebugBreakpoint) error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	generation := owner.runGeneration
	conn := owner.conn
	owner.mu.Unlock()
	return d.applyAllBreakpointsForRun(owner, generation, conn, bps)
}

func (d *DebugService) applyAllBreakpointsForRun(owner *DebugSession, generation uint64, conn net.Conn, bps []DebugBreakpoint) error {
	byFile := map[string][]DebugBreakpoint{}
	for _, b := range bps {
		f := filepath.Clean(b.File)
		byFile[f] = append(byFile[f], b)
	}
	var verified []DebugBreakpoint
	for file, list := range byFile {
		src := map[string]interface{}{"path": file}
		bpsArgs := make([]map[string]interface{}, 0, len(list))
		for _, b := range list {
			arg := map[string]interface{}{"line": b.Line}
			if b.Condition != "" {
				arg["condition"] = b.Condition
			}
			if b.LogMessage != "" {
				arg["logMessage"] = b.LogMessage
			}
			bpsArgs = append(bpsArgs, arg)
		}
		body, err := d.dapRequestBodyForRun(owner, generation, conn, "setBreakpoints", map[string]interface{}{
			"source":      src,
			"breakpoints": bpsArgs,
		})
		if err != nil {
			if !dapRunCurrent(owner, generation, conn) {
				return err
			}
			for _, b := range list {
				b.Verified = false
				b.Message = err.Error()
				verified = append(verified, b)
			}
			continue
		}
		var resp struct {
			Breakpoints []struct {
				ID       int    `json:"id"`
				Line     int    `json:"line"`
				Verified bool   `json:"verified"`
				Message  string `json:"message"`
			} `json:"breakpoints"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return fmt.Errorf("decode dap setBreakpoints response: %w", err)
		}
		for i, b := range list {
			bp := b
			if i < len(resp.Breakpoints) {
				bp.ID = resp.Breakpoints[i].ID
				bp.Verified = resp.Breakpoints[i].Verified
				bp.Message = resp.Breakpoints[i].Message
				if !bp.Verified && bp.Message == "" {
					bp.Message = "unverified"
				}
				if resp.Breakpoints[i].Line > 0 {
					bp.Line = resp.Breakpoints[i].Line
				}
			} else {
				bp.Verified = false
				bp.Message = "no adapter response"
			}
			verified = append(verified, bp)
		}
	}
	owner.mu.Lock()
	if owner.runGeneration != generation || owner.conn != conn {
		owner.mu.Unlock()
		return fmt.Errorf("debug run changed")
	}
	owner.breakpoints = verified
	owner.mu.Unlock()
	return nil
}

// Evaluate runs DAP/CDP evaluate for expression (watch / REPL) (prompt-12/13).
func (d *DebugService) Evaluate(expression string) (DebugVariable, error) {
	return d.evaluate(expression, "watch")
}

// EvaluateWatch evaluates an expression using the DAP watch context.
func (d *DebugService) EvaluateWatch(expression string) (string, error) {
	value, err := d.evaluate(expression, "watch")
	if err != nil {
		return "", err
	}
	return value.Value, nil
}

// EvaluateREPL evaluates an expression using the DAP repl context.
func (d *DebugService) EvaluateREPL(expression string) (string, error) {
	value, err := d.evaluate(expression, "repl")
	if err != nil {
		return "", err
	}
	return value.Value, nil
}

func (d *DebugService) evaluate(expression, evaluateContext string) (DebugVariable, error) {
	owner := d.activeSession()
	if owner == nil {
		return DebugVariable{}, fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	generation := owner.runGeneration
	conn := owner.conn
	cdp := owner.cdp
	owner.mu.Unlock()
	value, err := d.evaluateForRunContext(owner, generation, conn, cdp, expression, evaluateContext)
	owner.mu.Lock()
	if owner.runGeneration == generation {
		if err != nil {
			owner.lastError = "evaluate: " + err.Error()
		} else {
			owner.lastError = ""
		}
	}
	owner.mu.Unlock()
	return value, err
}

func (d *DebugService) evaluateForRun(owner *DebugSession, generation uint64, conn net.Conn, evaluator nodeRunEvaluator, expression string) (DebugVariable, error) {
	return d.evaluateForRunContext(owner, generation, conn, evaluator, expression, "watch")
}

func (d *DebugService) evaluateForRunContext(owner *DebugSession, generation uint64, conn net.Conn, evaluator nodeRunEvaluator, expression, evaluateContext string) (DebugVariable, error) {
	expr := strings.TrimSpace(expression)
	if expr == "" {
		return DebugVariable{}, fmt.Errorf("empty expression")
	}
	owner.mu.Lock()
	if owner.runGeneration != generation {
		owner.mu.Unlock()
		return DebugVariable{}, fmt.Errorf("debug run changed")
	}
	mode := owner.mode
	frameID := 0
	if len(owner.stack) > 0 && owner.stack[0].ID > 0 {
		frameID = owner.stack[0].ID
	}
	owner.mu.Unlock()
	if isCDPDebugMode(mode) && evaluator != nil {
		return evaluator.Evaluate(expr)
	}
	args := map[string]interface{}{
		"expression": expr,
		"context":    evaluateContext,
	}
	if frameID > 0 {
		args["frameId"] = frameID
	}
	body, err := d.dapRequestBodyForRun(owner, generation, conn, "evaluate", args)
	if err != nil {
		return DebugVariable{Name: expr, Value: err.Error(), Type: "error"}, err
	}
	var resp struct {
		Result string `json:"result"`
		Type   string `json:"type"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return DebugVariable{Name: expr, Value: "invalid dap response", Type: "error"}, fmt.Errorf("decode dap evaluate response: %w", err)
	}
	return DebugVariable{Name: expr, Value: resp.Result, Type: resp.Type}, nil
}

// AddWatch adds an expression to the watch list and evaluates if stopped.
func (d *DebugService) AddWatch(expression string) ([]DebugVariable, error) {
	expr := strings.TrimSpace(expression)
	if expr == "" {
		return nil, fmt.Errorf("empty expression")
	}
	owner := d.activeSession()
	if owner == nil {
		return nil, fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	for _, w := range owner.watches {
		if w == expr {
			owner.mu.Unlock()
			return d.RefreshWatches()
		}
	}
	owner.watches = append(owner.watches, expr)
	owner.mu.Unlock()
	return d.RefreshWatches()
}

// RemoveWatch removes a watch expression.
func (d *DebugService) RemoveWatch(expression string) ([]DebugVariable, error) {
	owner := d.activeSession()
	if owner == nil {
		return nil, fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	var next []string
	for _, w := range owner.watches {
		if w != expression {
			next = append(next, w)
		}
	}
	owner.watches = next
	owner.mu.Unlock()
	return d.RefreshWatches()
}

// RefreshWatches re-evaluates all watches.
func (d *DebugService) RefreshWatches() ([]DebugVariable, error) {
	owner := d.activeSession()
	if owner == nil {
		return nil, fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	generation := owner.runGeneration
	conn := owner.conn
	cdp := owner.cdp
	owner.mu.Unlock()
	evaluate := func(expression string) (DebugVariable, error) {
		return d.evaluateForRun(owner, generation, conn, cdp, expression)
	}
	return d.refreshWatchesForRun(owner, generation, evaluate)
}

func (d *DebugService) refreshWatchesForRun(owner *DebugSession, generation uint64, evaluate debugRunEvaluate) ([]DebugVariable, error) {
	owner.mu.Lock()
	if owner.runGeneration != generation {
		owner.mu.Unlock()
		return nil, nil
	}
	exprs := append([]string(nil), owner.watches...)
	owner.mu.Unlock()
	var out []DebugVariable
	lastError := ""
	updateLastError := false
	for _, e := range exprs {
		owner.mu.Lock()
		current := owner.runGeneration == generation
		owner.mu.Unlock()
		if !current {
			return append([]DebugVariable(nil), out...), nil
		}
		v, err := evaluate(e)
		updateLastError = true
		if err != nil {
			out = append(out, DebugVariable{Name: e, Value: err.Error(), Type: "error"})
			lastError = "evaluate: " + err.Error()
		} else {
			out = append(out, v)
			lastError = ""
		}
	}
	owner.mu.Lock()
	if owner.runGeneration == generation {
		owner.watchValues = out
		if updateLastError {
			owner.lastError = lastError
		}
	}
	owner.mu.Unlock()
	return append([]DebugVariable(nil), out...), nil
}

// ListWatches returns last evaluated watch values.
func (d *DebugService) ListWatches() []DebugVariable {
	owner := d.activeSession()
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return append([]DebugVariable(nil), owner.watchValues...)
}

// --- prompt-5: 调试器增强 (function breakpoints / setVariable / restartFrame / inlineValues) ---

// SetFunctionBreakpoints sends the DAP setFunctionBreakpoints request for the
// active session and persists the list on the session (prompt-5).
func (d *DebugService) SetFunctionBreakpoints(breakpoints []FunctionBreakpoint) error {
	if len(breakpoints) == 0 {
		return fmt.Errorf("no function breakpoints")
	}
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	owner.functionBreakpoints = append([]FunctionBreakpoint(nil), breakpoints...)
	conn := owner.conn
	generation := owner.runGeneration
	owner.mu.Unlock()
	if conn == nil {
		// offline: store only, will be applied on next launch
		return nil
	}
	args := map[string]interface{}{
		"breakpoints": breakpoints,
	}
	_, err := d.dapRequestBodyForRun(owner, generation, conn, "setFunctionBreakpoints", args)
	return err
}

// ListFunctionBreakpoints returns the function breakpoints persisted on the
// active session (prompt-5).
func (d *DebugService) ListFunctionBreakpoints() []FunctionBreakpoint {
	owner := d.activeSession()
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return append([]FunctionBreakpoint(nil), owner.functionBreakpoints...)
}

// SetVariable sends the DAP setVariable request and returns the new value
// string (prompt-5).
func (d *DebugService) SetVariable(variablesReference int, name string, value string) (string, error) {
	if variablesReference <= 0 {
		return "", fmt.Errorf("invalid variablesReference")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("empty variable name")
	}
	body, err := d.dapRequestBody("setVariable", map[string]interface{}{
		"variablesReference": variablesReference,
		"name":               name,
		"value":              value,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decode dap setVariable response: %w", err)
	}
	if resp.Value == "" {
		// adapter did not echo a value — return the requested value
		return value, nil
	}
	return resp.Value, nil
}

// RestartFrame sends the DAP restartFrame request for the given frame id
// (prompt-5).
func (d *DebugService) RestartFrame(frameId int) error {
	if frameId <= 0 {
		return fmt.Errorf("invalid frameId")
	}
	return d.dapRequest("restartFrame", map[string]interface{}{
		"frameId": frameId,
	})
}

// GetInlineValues sends the DAP inlineValues request for a frame; if the
// adapter does not support it, falls back to a variables request on
// variablesReference to compute inline values (prompt-5).
func (d *DebugService) GetInlineValues(frameId int, variablesReference int) ([]InlineValue, error) {
	if frameId <= 0 && variablesReference <= 0 {
		return nil, fmt.Errorf("invalid frameId and variablesReference")
	}
	if frameId > 0 {
		body, err := d.dapRequestBody("inlineValues", map[string]interface{}{
			"frameId": frameId,
		})
		if err == nil {
			var resp struct {
				InlineValues []struct {
					Type              string `json:"type"`
					Name              string `json:"name"`
					Value             string `json:"value"`
					VariableReference int    `json:"variableReference"`
				} `json:"inlineValues"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return nil, fmt.Errorf("decode dap inlineValues response: %w", err)
			}
			out := make([]InlineValue, 0, len(resp.InlineValues))
			for _, v := range resp.InlineValues {
				text := v.Value
				if text == "" {
					text = v.Name
				}
				out = append(out, InlineValue{
					Type:              v.Type,
					Text:              text,
					VariableReference: v.VariableReference,
				})
			}
			if len(out) > 0 {
				return out, nil
			}
		}
	}
	// Fallback: compute inline values from a variables request (prompt-5).
	if variablesReference <= 0 {
		return nil, nil
	}
	vbody, verr := d.dapRequestBody("variables", map[string]interface{}{
		"variablesReference": variablesReference,
	})
	if verr != nil {
		return nil, verr
	}
	var vresp struct {
		Variables []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
			Type  string `json:"type"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(vbody, &vresp); err != nil {
		return nil, fmt.Errorf("decode dap variables response for inline values: %w", err)
	}
	out := make([]InlineValue, 0, len(vresp.Variables))
	for _, v := range vresp.Variables {
		out = append(out, InlineValue{
			Type: "variable",
			Text: fmt.Sprintf("%s = %s", v.Name, v.Value),
		})
	}
	return out, nil
}

func (d *DebugService) dapRequest(command string, args map[string]interface{}) error {
	_, err := d.dapRequestBody(command, args)
	return err
}

func (d *DebugService) dapRequestBody(command string, args map[string]interface{}) (json.RawMessage, error) {
	owner := d.activeSession()
	if owner == nil {
		return nil, fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	generation := owner.runGeneration
	conn := owner.conn
	owner.mu.Unlock()
	return d.dapRequestBodyForRun(owner, generation, conn, command, args)
}

func (d *DebugService) dapRequestBodyForRun(owner *DebugSession, generation uint64, conn net.Conn, command string, args map[string]interface{}) (json.RawMessage, error) {
	owner.mu.Lock()
	if owner.runGeneration != generation || owner.conn != conn {
		owner.mu.Unlock()
		return nil, fmt.Errorf("debug run changed")
	}
	if conn == nil {
		owner.mu.Unlock()
		return nil, fmt.Errorf("no dap connection")
	}
	seq, ch := reserveDAPPendingRequestLocked(owner)
	owner.mu.Unlock()
	return d.completeDAPRequest(owner, generation, conn, seq, ch, command, args)
}

func reserveDAPPendingRequestLocked(owner *DebugSession) (int, chan dapMessage) {
	seq := int(atomic.AddInt64(&owner.seq, 1))
	response := make(chan dapMessage, 1)
	if owner.pending == nil {
		owner.pending = make(map[int]chan dapMessage)
	}
	owner.pending[seq] = response
	return seq, response
}

func (d *DebugService) completeDAPRequest(
	owner *DebugSession,
	generation uint64,
	conn net.Conn,
	seq int,
	response chan dapMessage,
	command string,
	args map[string]interface{},
) (json.RawMessage, error) {
	payload := map[string]interface{}{
		"seq":       seq,
		"type":      "request",
		"command":   command,
		"arguments": args,
	}
	owner.logProtocol("DAP", "->", payload)
	owner.writeMu.Lock()
	err := writeDAPMessage(conn, payload)
	owner.writeMu.Unlock()
	if err != nil {
		owner.mu.Lock()
		if owner.runGeneration == generation && owner.pending[seq] == response {
			delete(owner.pending, seq)
		}
		owner.mu.Unlock()
		return nil, err
	}

	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case resp, ok := <-response:
		if !ok {
			return nil, fmt.Errorf("dap connection closed")
		}
		owner.mu.Lock()
		current := owner.runGeneration == generation && owner.conn == conn
		owner.mu.Unlock()
		if !current {
			return nil, fmt.Errorf("debug run changed")
		}
		if !resp.Success && resp.Type == "response" {
			msg := resp.Message
			if msg == "" {
				msg = "adapter returned an unsuccessful response"
			}
			return resp.Body, fmt.Errorf("dap %s request failed: %s", command, msg)
		}
		return resp.Body, nil
	case <-timer.C:
		owner.mu.Lock()
		if owner.runGeneration == generation && owner.pending[seq] == response {
			delete(owner.pending, seq)
		}
		owner.mu.Unlock()
		return nil, fmt.Errorf("dap timeout: %s", command)
	}
}

// sendRequestUnlocked sends without waiting (best-effort during shutdown).
// On *DebugSession so StopSession can fire it on a non-active session (prompt-5);
// legacy callers using d.sendRequestUnlocked(...) resolve via embedding promotion.
func (s *DebugSession) sendRequestUnlocked(conn net.Conn, command string, args map[string]interface{}) error {
	seq := int(atomic.AddInt64(&s.seq, 1))
	payload := map[string]interface{}{
		"seq": seq, "type": "request", "command": command, "arguments": args,
	}
	s.logProtocol("DAP", "->", payload)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return writeDAPMessage(conn, payload)
}

func writeDAPMessage(w io.Writer, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode dap message: %w", err)
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := io.WriteString(w, header); err != nil {
		return fmt.Errorf("write dap header: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write dap body: %w", err)
	}
	return nil
}

func (s *DebugSession) logProtocol(protocol, direction string, payload any) {
	if s != nil && s.protocolLog != nil {
		s.protocolLog(protocol, direction, payload)
	}
}

func (d *DebugService) readLoop(owner *DebugSession, generation uint64, conn net.Conn, done chan struct{}, doneOnce *sync.Once) {
	defer func() {
		if owner != nil {
			owner.finishDAPRun(generation, conn)
		}
		if done != nil && doneOnce != nil {
			doneOnce.Do(func() { close(done) })
		}
	}()
	if owner == nil || conn == nil {
		return
	}

	reader := bufio.NewReader(conn)
	for {
		msg, err := readDAPMessage(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				slog.Debug("read dap message failed", "err", err)
				owner.logProtocol("DAP", "<-", map[string]any{
					"parseError": err.Error(),
				})
			}
			return
		}
		owner.logProtocol("DAP", "<-", msg)
		d.handleDAPMessage(owner, generation, msg)
	}
}

func (d *DebugService) startDAPReadLoop(
	owner *DebugSession,
	generation uint64,
	conn net.Conn,
	done chan struct{},
	doneOnce *sync.Once,
	wg *sync.WaitGroup,
) {
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		d.readLoop(owner, generation, conn, done, doneOnce)
	}()
}

func dapSessionInfoLocked(owner *DebugSession) DebugSessionInfo {
	if !owner.running {
		return DebugSessionInfo{Running: false, Message: "DAP session ended"}
	}
	message := fmt.Sprintf("DAP session on %s (%s)", owner.addr, owner.mode)
	if owner.stopped {
		message = fmt.Sprintf("Paused: %s - %s", owner.stopReason, owner.addr)
	}
	return DebugSessionInfo{
		Running: owner.running, Address: owner.addr, Mode: owner.mode, Message: message,
		Stopped: owner.stopped, StopReason: owner.stopReason, ThreadID: owner.threadID,
		AdapterID: owner.adapterID, SourcePackID: owner.sourcePackID, SourcePackVersion: owner.sourcePackVersion,
	}
}

// maxDAPContentLength is the upper limit for a single DAP message body
// (M-9). 16 MB is generous for any legitimate DAP payload while
// preventing unbounded allocation from a malformed or malicious
// Content-Length header.
const (
	maxDAPContentLength    = 16 * 1024 * 1024
	maxDAPHeaderLineLength = 4 * 1024
	maxDAPHeaderLines      = 64
)

func readDAPMessage(r *bufio.Reader) (dapMessage, error) {
	var contentLen int
	var contentLenSet bool
	for headerLine := 0; ; headerLine++ {
		if headerLine >= maxDAPHeaderLines {
			return dapMessage{}, fmt.Errorf("too many dap header lines")
		}
		lineBytes, err := r.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				return dapMessage{}, fmt.Errorf("dap header line exceeds %d bytes", maxDAPHeaderLineLength)
			}
			return dapMessage{}, fmt.Errorf("read dap header: %w", err)
		}
		if len(lineBytes) > maxDAPHeaderLineLength {
			return dapMessage{}, fmt.Errorf("dap header line exceeds %d bytes", maxDAPHeaderLineLength)
		}
		line := string(lineBytes)
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		// M-9: robust Content-Length parsing — use strings.Contains for
		// case-insensitive detection, then trim/normalize the value.
		// Don't ignore strconv.Atoi errors.
		lower := strings.ToLower(line)
		if strings.Contains(lower, "content-length:") {
			idx := strings.Index(lower, "content-length:")
			n := strings.TrimSpace(line[idx+len("content-length:"):])
			parsed, err := strconv.Atoi(n)
			if err != nil {
				return dapMessage{}, fmt.Errorf("invalid Content-Length %q: %w", n, err)
			}
			contentLen = parsed
			contentLenSet = true
		}
	}
	if !contentLenSet || contentLen <= 0 {
		return dapMessage{}, fmt.Errorf("missing or invalid content-length")
	}
	// M-9: reject oversized messages to prevent OOM.
	if contentLen > maxDAPContentLength {
		return dapMessage{}, fmt.Errorf("content-length %d exceeds maximum %d", contentLen, maxDAPContentLength)
	}
	buf := make([]byte, contentLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return dapMessage{}, fmt.Errorf("read dap body: %w", err)
	}
	var msg dapMessage
	if err := json.Unmarshal(buf, &msg); err != nil {
		return dapMessage{}, fmt.Errorf("decode dap message: %w", err)
	}
	return msg, nil
}

func (d *DebugService) handleDAPMessage(owner *DebugSession, generation uint64, msg dapMessage) {
	owner.mu.Lock()
	current := owner.runGeneration == generation
	owner.mu.Unlock()
	if !current {
		return
	}
	switch msg.Type {
	case "response":
		owner.mu.Lock()
		if owner.runGeneration != generation {
			owner.mu.Unlock()
			return
		}
		ch := owner.pending[msg.RequestSeq]
		delete(owner.pending, msg.RequestSeq)
		if ch != nil {
			select {
			case ch <- msg:
			default:
			}
		}
		owner.mu.Unlock()
	case "event":
		d.handleDAPEvent(owner, generation, msg)
	}
}

func (d *DebugService) handleDAPEvent(owner *DebugSession, generation uint64, msg dapMessage) {
	switch msg.Event {
	case "stopped":
		var body struct {
			Reason   string `json:"reason"`
			ThreadID int    `json:"threadId"`
		}
		if err := json.Unmarshal(msg.Body, &body); err != nil {
			slog.Debug("parse dap stopped event", "err", err)
		}
		owner.mu.Lock()
		if owner.runGeneration != generation {
			owner.mu.Unlock()
			return
		}
		owner.stopped = true
		// GOAL-P1-03: every entry into the stopped state is a new stop. A
		// step-in target menu fetched during an earlier stop must not be
		// redeemable now: the frame it described no longer exists, so its
		// target IDs would either be rejected by the adapter or, worse,
		// silently match a different call site.
		owner.stopSequence++
		owner.stopReason = body.Reason
		if owner.stopReason == "" {
			owner.stopReason = "paused"
		}
		if body.ThreadID != 0 {
			owner.threadID = body.ThreadID
		}
		owner.touchDebugThreadsStateLocked()
		conn := owner.conn
		owner.mu.Unlock()
		// best-effort refresh stack/locals/watches
		request := func(command string, args map[string]interface{}) (json.RawMessage, error) {
			return d.dapRequestBodyForRun(owner, generation, conn, command, args)
		}
		evaluate := func(expression string) (DebugVariable, error) {
			return d.evaluateForRun(owner, generation, conn, nil, expression)
		}
		go func() {
			if err := d.refreshStackAndLocalsForRun(owner, generation, request); err != nil {
				slog.Debug("debug: refresh stack after stop failed", "err", err)
			}
			if _, err := d.refreshWatchesForRun(owner, generation, evaluate); err != nil {
				slog.Debug("debug: refresh watches after stop failed", "err", err)
			}
		}()
	case "continued":
		owner.mu.Lock()
		if owner.runGeneration != generation {
			owner.mu.Unlock()
			return
		}
		owner.stopped = false
		owner.stopReason = ""
		owner.touchDebugThreadsStateLocked()
		owner.mu.Unlock()
	case "terminated", "exited":
		owner.mu.Lock()
		if owner.runGeneration != generation {
			owner.mu.Unlock()
			return
		}
		owner.stopped = false
		owner.stopReason = msg.Event
		owner.touchDebugThreadsStateLocked()
		owner.mu.Unlock()
	case "thread":
		owner.mu.Lock()
		if owner.runGeneration != generation {
			owner.mu.Unlock()
			return
		}
		owner.touchDebugThreadsStateLocked()
		owner.mu.Unlock()
	case "output":
		var body struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal(msg.Body, &body); err != nil {
			slog.Debug("parse dap output event", "err", err)
		}
		if body.Output != "" {
			slog.Debug("dap output", "bytes", len([]byte(body.Output)))
		}
	case "initialized":
		owner.mu.Lock()
		if owner.runGeneration != generation {
			owner.mu.Unlock()
			return
		}
		initialized := owner.dapInitialized
		initializedOnce := owner.dapInitializedOnce
		owner.mu.Unlock()
		if initialized != nil && initializedOnce != nil {
			initializedOnce.Do(func() { close(initialized) })
		}
	}
}
