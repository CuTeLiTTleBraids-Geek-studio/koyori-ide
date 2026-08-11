package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

// ClearLastError clears the last evaluate/breakpoint error banner.
func (d *DebugService) ClearLastError() {
	owner := d.activeSession()
	if owner == nil {
		return
	}
	owner.mu.Lock()
	owner.lastError = ""
	owner.mu.Unlock()
}

// SetBreakpoint adds or updates a source breakpoint (1-based line).
func (d *DebugService) SetBreakpoint(file string, line int) (DebugBreakpoint, error) {
	return d.SetBreakpointEx(file, line, "", "")
}

// SetConditionalBreakpoint sets a source breakpoint that only pauses when
// condition evaluates to true.
func (d *DebugService) SetConditionalBreakpoint(filePath string, line int, condition string) error {
	_, err := d.SetBreakpointEx(filePath, line, condition, "")
	return err
}

// SetLogpoint sets a source breakpoint that logs message without pausing.
func (d *DebugService) SetLogpoint(filePath string, line int, message string) error {
	_, err := d.SetBreakpointEx(filePath, line, "", message)
	return err
}

// SetBreakpointEx sets a breakpoint with optional condition / logpoint (prompt-12 12-B).
func (d *DebugService) SetBreakpointEx(file string, line int, condition, logMessage string) (DebugBreakpoint, error) {
	if line < 1 {
		return DebugBreakpoint{}, fmt.Errorf("invalid line")
	}
	owner := d.activeSession()
	if owner == nil {
		return DebugBreakpoint{}, fmt.Errorf("no debug session")
	}
	abs := file
	if a, err := filepath.Abs(file); err == nil {
		abs = a
	}
	owner.mu.Lock()
	found := false
	for i, b := range owner.breakpoints {
		if filepath.Clean(b.File) == filepath.Clean(abs) && b.Line == line {
			found = true
			owner.breakpoints[i].Verified = false
			owner.breakpoints[i].Condition = condition
			owner.breakpoints[i].LogMessage = logMessage
			owner.breakpoints[i].Message = ""
			break
		}
	}
	if !found {
		owner.breakpoints = append(owner.breakpoints, DebugBreakpoint{
			File: abs, Line: line, Condition: condition, LogMessage: logMessage,
		})
	}
	bps := append([]DebugBreakpoint(nil), owner.breakpoints...)
	generation := owner.runGeneration
	conn := owner.conn
	cdp := owner.cdp
	mode := owner.mode
	browserSpec := owner.browserConfig
	owner.mu.Unlock()

	if isCDPDebugMode(mode) && cdp != nil {
		breakpointURL := abs
		if mode == "browser" {
			breakpointURL = browserLocalPathToURL(abs, browserSpec)
		}
		_, verified, msg, err := cdp.setBreakpointByURL(breakpointURL, line, condition, logMessage)
		owner.mu.Lock()
		if owner.runGeneration != generation || owner.cdp != cdp {
			owner.mu.Unlock()
			return DebugBreakpoint{}, fmt.Errorf("debug run changed")
		}
		for i := range owner.breakpoints {
			if filepath.Clean(owner.breakpoints[i].File) == filepath.Clean(abs) && owner.breakpoints[i].Line == line {
				owner.breakpoints[i].Verified = verified
				owner.breakpoints[i].Message = msg
				if err != nil {
					owner.breakpoints[i].Verified = false
					owner.breakpoints[i].Message = err.Error()
					owner.lastError = "breakpoint: " + err.Error()
				} else if !verified {
					owner.lastError = "breakpoint: " + msg
				} else {
					owner.lastError = ""
				}
				bp := owner.breakpoints[i]
				owner.mu.Unlock()
				return bp, err
			}
		}
		owner.mu.Unlock()
	} else if conn != nil {
		if err := d.applyAllBreakpointsForRun(owner, generation, conn, bps); err != nil {
			owner.mu.Lock()
			if owner.runGeneration == generation {
				owner.lastError = "breakpoint: " + err.Error()
			}
			owner.mu.Unlock()
			return DebugBreakpoint{}, err
		}
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	for _, b := range owner.breakpoints {
		if filepath.Clean(b.File) == filepath.Clean(abs) && b.Line == line {
			return b, nil
		}
	}
	return DebugBreakpoint{File: abs, Line: line, Condition: condition, LogMessage: logMessage}, nil
}

// SetBreakpointCondition updates condition on an existing breakpoint (prompt-12 12-B).
func (d *DebugService) SetBreakpointCondition(file string, line int, condition string) (DebugBreakpoint, error) {
	owner := d.activeSession()
	if owner == nil {
		return DebugBreakpoint{}, fmt.Errorf("no debug session")
	}
	abs := file
	if a, err := filepath.Abs(file); err == nil {
		abs = a
	}
	owner.mu.Lock()
	var logMsg string
	found := false
	for _, b := range owner.breakpoints {
		if filepath.Clean(b.File) == filepath.Clean(abs) && b.Line == line {
			found = true
			logMsg = b.LogMessage
			break
		}
	}
	owner.mu.Unlock()
	if !found {
		return d.SetBreakpointEx(abs, line, condition, "")
	}
	return d.SetBreakpointEx(abs, line, condition, logMsg)
}

// RemoveBreakpoint removes a breakpoint at file:line (1-based).
func (d *DebugService) RemoveBreakpoint(file string, line int) error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	abs := file
	if a, err := filepath.Abs(file); err == nil {
		abs = a
	}
	owner.mu.Lock()
	var next []DebugBreakpoint
	for _, b := range owner.breakpoints {
		if filepath.Clean(b.File) == filepath.Clean(abs) && b.Line == line {
			continue
		}
		next = append(next, b)
	}
	owner.breakpoints = next
	bps := append([]DebugBreakpoint(nil), owner.breakpoints...)
	generation := owner.runGeneration
	conn := owner.conn
	owner.mu.Unlock()
	if conn != nil {
		return d.applyAllBreakpointsForRun(owner, generation, conn, bps)
	}
	return nil
}

// ToggleBreakpoint toggles a breakpoint; returns the resulting list for the file.
func (d *DebugService) ToggleBreakpoint(file string, line int) ([]DebugBreakpoint, error) {
	owner := d.activeSession()
	if owner == nil {
		return nil, fmt.Errorf("no debug session")
	}
	abs := file
	if a, err := filepath.Abs(file); err == nil {
		abs = a
	}
	owner.mu.Lock()
	exists := false
	for _, b := range owner.breakpoints {
		if filepath.Clean(b.File) == filepath.Clean(abs) && b.Line == line {
			exists = true
			break
		}
	}
	owner.mu.Unlock()
	if exists {
		if err := d.RemoveBreakpoint(abs, line); err != nil {
			return nil, err
		}
	} else {
		if _, err := d.SetBreakpoint(abs, line); err != nil {
			return nil, err
		}
	}
	return d.ListBreakpoints(), nil
}

// ListBreakpoints returns all breakpoints.
func (d *DebugService) ListBreakpoints() []DebugBreakpoint {
	owner := d.activeSession()
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return append([]DebugBreakpoint(nil), owner.breakpoints...)
}

// Continue resumes execution (DAP or Node CDP).
func (d *DebugService) Continue() error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	cdp := owner.cdp
	mode := owner.mode
	tid := owner.threadID
	if tid == 0 {
		tid = 1
	}
	owner.stopped = false
	owner.clearAsyncStackLocked()
	owner.touchDebugThreadsStateLocked()
	generation := owner.runGeneration
	conn := owner.conn
	owner.mu.Unlock()
	if isCDPDebugMode(mode) && cdp != nil {
		return cdp.Resume()
	}
	_, err := d.dapRequestBodyForRun(owner, generation, conn, "continue", map[string]interface{}{"threadId": tid})
	return err
}

// StepOver steps over the current line.
func (d *DebugService) StepOver() error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	cdp, mode := owner.cdp, owner.mode
	owner.mu.Unlock()
	if isCDPDebugMode(mode) && cdp != nil {
		owner.mu.Lock()
		owner.stopped = false
		owner.clearAsyncStackLocked()
		owner.touchDebugThreadsStateLocked()
		owner.mu.Unlock()
		return cdp.StepOver()
	}
	return d.stepSession(owner, "next")
}

// StepIn steps into a call.
func (d *DebugService) StepIn() error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	cdp, mode := owner.cdp, owner.mode
	owner.mu.Unlock()
	if isCDPDebugMode(mode) && cdp != nil {
		owner.mu.Lock()
		owner.stopped = false
		owner.clearAsyncStackLocked()
		owner.touchDebugThreadsStateLocked()
		owner.mu.Unlock()
		return cdp.StepInto()
	}
	return d.stepSession(owner, "stepIn")
}

// StepOut steps out of the current function.
func (d *DebugService) StepOut() error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	cdp, mode := owner.cdp, owner.mode
	owner.mu.Unlock()
	if isCDPDebugMode(mode) && cdp != nil {
		owner.mu.Lock()
		owner.stopped = false
		owner.clearAsyncStackLocked()
		owner.touchDebugThreadsStateLocked()
		owner.mu.Unlock()
		return cdp.StepOut()
	}
	return d.stepSession(owner, "stepOut")
}

// Pause requests a pause (if supported).
func (d *DebugService) Pause() error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	cdp, mode := owner.cdp, owner.mode
	tid := owner.threadID
	if tid == 0 {
		tid = 1
	}
	generation := owner.runGeneration
	conn := owner.conn
	owner.mu.Unlock()
	if isCDPDebugMode(mode) && cdp != nil {
		return cdp.Pause()
	}
	_, err := d.dapRequestBodyForRun(owner, generation, conn, "pause", map[string]interface{}{"threadId": tid})
	return err
}

func (d *DebugService) step(cmd string) error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	return d.stepSession(owner, cmd)
}

func (d *DebugService) stepSession(owner *DebugSession, cmd string) error {
	return d.stepSessionWithArgs(owner, cmd, nil)
}

// stepSessionWithArgs issues a step request with additional DAP arguments
// (GOAL-P1-03: `stepIn` needs an optional `targetId`).
//
// threadId is always set by this function and cannot be overridden by extra:
// the thread comes from session state, and letting a caller substitute one would
// step a thread the session is not tracking.
func (d *DebugService) stepSessionWithArgs(
	owner *DebugSession,
	cmd string,
	extra map[string]interface{},
) error {
	owner.mu.Lock()
	tid := owner.threadID
	if tid == 0 {
		tid = 1
	}
	owner.stopped = false
	owner.clearAsyncStackLocked()
	owner.touchDebugThreadsStateLocked()
	generation := owner.runGeneration
	conn := owner.conn
	owner.mu.Unlock()

	args := make(map[string]interface{}, len(extra)+1)
	for k, v := range extra {
		args[k] = v
	}
	args["threadId"] = tid

	_, err := d.dapRequestBodyForRun(owner, generation, conn, cmd, args)
	return err
}

// RefreshStackAndLocals pulls stack + top-frame locals (call after stop).
func (d *DebugService) RefreshStackAndLocals() error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	generation := owner.runGeneration
	conn := owner.conn
	owner.mu.Unlock()
	request := func(command string, args map[string]interface{}) (json.RawMessage, error) {
		return d.dapRequestBodyForRun(owner, generation, conn, command, args)
	}
	return d.refreshStackAndLocalsForRun(owner, generation, request)
}

func (d *DebugService) refreshStackAndLocalsForRun(owner *DebugSession, generation uint64, request dapRunRequest) error {
	owner.mu.Lock()
	if owner.runGeneration != generation {
		owner.mu.Unlock()
		return nil
	}
	levels := 0
	if owner.supportsDelayedStackTraceLoading {
		levels = 32
	}
	owner.mu.Unlock()

	page, err := d.loadDAPStackPageForRun(owner, generation, 0, levels, request)
	if err != nil {
		owner.mu.Lock()
		current := owner.runGeneration == generation
		owner.mu.Unlock()
		if !current {
			return nil
		}
		return err
	}
	frames := page.Frames
	owner.mu.Lock()
	if owner.runGeneration != generation {
		owner.mu.Unlock()
		return nil
	}
	owner.stack = frames
	owner.stackTotalFrames = page.TotalFrames
	owner.stackHasMore = page.HasMore
	if len(frames) == 0 {
		owner.locals = nil
		owner.mu.Unlock()
		return nil
	}
	owner.mu.Unlock()
	return d.loadLocalsForFrameForRun(owner, generation, frames[0].ID, request)
}

// GetVariables requests DAP variables for an adapter-owned
// variablesReference (G14: nested expansion + paging). Refuses non-positive
// references so stale/zero references are never sent to the adapter.
func (d *DebugService) GetVariables(variablesReference, start, count int) ([]DebugVariable, error) {
	if variablesReference <= 0 {
		return nil, fmt.Errorf("invalid variablesReference %d", variablesReference)
	}
	owner := d.activeSession()
	if owner == nil {
		return nil, fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	generation := owner.runGeneration
	conn := owner.conn
	cdp := owner.cdp
	owner.mu.Unlock()
	if cdp != nil {
		// Node/CDP adapter: expand through the connection-scoped object refs.
		children := cdp.getPropertiesByRef(variablesReference)
		if children == nil {
			return nil, fmt.Errorf("CDP variablesReference %d is stale or unknown", variablesReference)
		}
		return children, nil
	}
	args := map[string]interface{}{"variablesReference": variablesReference}
	if start > 0 {
		args["start"] = start
	}
	if count > 0 {
		args["count"] = count
	}
	body, err := d.dapRequestBodyForRun(owner, generation, conn, "variables", args)
	if err != nil {
		return nil, err
	}
	var vr struct {
		Variables []struct {
			Name               string `json:"name"`
			Value              string `json:"value"`
			Type               string `json:"type"`
			VariablesReference int    `json:"variablesReference"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(body, &vr); err != nil {
		return nil, fmt.Errorf("decode dap variables response: %w", err)
	}
	out := make([]DebugVariable, 0, len(vr.Variables))
	for _, v := range vr.Variables {
		out = append(out, DebugVariable{
			Name:               v.Name,
			Value:              v.Value,
			Type:               v.Type,
			VariablesReference: v.VariablesReference,
		})
	}
	return out, nil
}

// LoadStackFrames requests a delayed DAP stack page. It is intentionally
// unavailable unless the adapter advertised supportsDelayedStackTraceLoading.
func (d *DebugService) LoadStackFrames(ctx context.Context, expectedGeneration uint64, startFrame, levels int) (DebugStackPage, error) {
	if startFrame < 0 || levels < 1 || levels > 256 {
		return DebugStackPage{}, fmt.Errorf("invalid stack page: startFrame >= 0 and levels 1..256 required")
	}
	owner := d.activeSession()
	if owner == nil {
		return DebugStackPage{}, fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	if owner.runGeneration != expectedGeneration {
		owner.mu.Unlock()
		return DebugStackPage{}, fmt.Errorf("debug run changed")
	}
	if !owner.supportsDelayedStackTraceLoading {
		owner.mu.Unlock()
		return DebugStackPage{}, fmt.Errorf("adapter does not support delayed stack trace loading")
	}
	if !owner.stopped {
		owner.mu.Unlock()
		return DebugStackPage{}, fmt.Errorf("debug session is not paused")
	}
	conn := owner.conn
	owner.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return DebugStackPage{}, err
	}
	request := func(command string, args map[string]interface{}) (json.RawMessage, error) {
		return d.dapRequestBodyForRun(owner, expectedGeneration, conn, command, args)
	}
	type result struct {
		page DebugStackPage
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, err := d.loadDAPStackPageForRun(owner, expectedGeneration, startFrame, levels, request)
		done <- result{page: page, err: err}
	}()
	select {
	case <-ctx.Done():
		return DebugStackPage{}, ctx.Err()
	case loaded := <-done:
		if loaded.err == nil {
			owner.mu.Lock()
			if owner.runGeneration != expectedGeneration || !owner.stopped {
				owner.mu.Unlock()
				return DebugStackPage{}, fmt.Errorf("debug run changed")
			}
			if len(owner.stack) == startFrame {
				owner.stack = append(owner.stack, loaded.page.Frames...)
				owner.stackTotalFrames = loaded.page.TotalFrames
				owner.stackHasMore = loaded.page.HasMore
			}
			owner.mu.Unlock()
		}
		return loaded.page, loaded.err
	}
}

func (d *DebugService) loadDAPStackPageForRun(owner *DebugSession, generation uint64, startFrame, levels int, request dapRunRequest) (DebugStackPage, error) {
	owner.mu.Lock()
	if owner.runGeneration != generation {
		owner.mu.Unlock()
		return DebugStackPage{}, fmt.Errorf("debug run changed")
	}
	tid := owner.threadID
	if tid == 0 {
		tid = 1
	}
	delayed := owner.supportsDelayedStackTraceLoading
	owner.mu.Unlock()

	body, err := request("stackTrace", map[string]interface{}{
		"threadId":   tid,
		"startFrame": startFrame,
		"levels":     levels,
	})
	if err != nil {
		return DebugStackPage{}, err
	}
	var st struct {
		StackFrames []struct {
			ID               int    `json:"id"`
			Name             string `json:"name"`
			Line             int    `json:"line"`
			Column           int    `json:"column"`
			PresentationHint string `json:"presentationHint"`
			Source           *struct {
				Path string `json:"path"`
				Name string `json:"name"`
			} `json:"source"`
		} `json:"stackFrames"`
		TotalFrames int `json:"totalFrames"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return DebugStackPage{}, fmt.Errorf("decode dap stackTrace response: %w", err)
	}
	frames := make([]DebugStackFrame, 0, len(st.StackFrames))
	for _, f := range st.StackFrames {
		path := ""
		if f.Source != nil {
			path = f.Source.Path
			if path == "" {
				path = f.Source.Name
			}
		}
		frames = append(frames, DebugStackFrame{
			ID:               f.ID,
			Name:             f.Name,
			File:             path,
			Line:             f.Line,
			Column:           f.Column,
			PresentationHint: f.PresentationHint,
			AsyncBoundary:    delayed && f.PresentationHint == "label",
		})
	}
	totalFrames := st.TotalFrames
	if totalFrames == 0 {
		totalFrames = startFrame + len(frames)
	}
	hasMore := delayed && totalFrames > startFrame+len(frames)
	if delayed && st.TotalFrames == 0 && levels > 0 && len(frames) == levels {
		hasMore = true
	}
	owner.mu.Lock()
	current := owner.runGeneration == generation
	owner.mu.Unlock()
	if !current {
		return DebugStackPage{}, fmt.Errorf("debug run changed")
	}
	return DebugStackPage{
		Generation:  generation,
		Frames:      frames,
		TotalFrames: totalFrames,
		HasMore:     hasMore,
	}, nil
}

// SelectFrame loads locals for a stack frame id.
func (d *DebugService) SelectFrame(frameID int) error {
	return d.loadLocalsForFrame(frameID)
}

func (d *DebugService) loadLocalsForFrame(frameID int) error {
	owner := d.activeSession()
	if owner == nil {
		return fmt.Errorf("no debug session")
	}
	owner.mu.Lock()
	generation := owner.runGeneration
	conn := owner.conn
	owner.mu.Unlock()
	request := func(command string, args map[string]interface{}) (json.RawMessage, error) {
		return d.dapRequestBodyForRun(owner, generation, conn, command, args)
	}
	return d.loadLocalsForFrameForRun(owner, generation, frameID, request)
}

func (d *DebugService) loadLocalsForFrameForRun(owner *DebugSession, generation uint64, frameID int, request dapRunRequest) error {
	body, err := request("scopes", map[string]interface{}{"frameId": frameID})
	if err != nil {
		return err
	}
	var sc struct {
		Scopes []struct {
			Name               string `json:"name"`
			VariablesReference int    `json:"variablesReference"`
		} `json:"scopes"`
	}
	if err := json.Unmarshal(body, &sc); err != nil {
		return fmt.Errorf("decode dap scopes response: %w", err)
	}
	var locals []DebugVariable
	for _, s := range sc.Scopes {
		// Prefer Locals; still include others if no Locals.
		if !strings.EqualFold(s.Name, "Locals") && !strings.EqualFold(s.Name, "Local") && len(sc.Scopes) > 1 {
			// still load Locals-like scopes first
			if strings.Contains(strings.ToLower(s.Name), "local") {
				// ok
			} else if s.Name != "Arguments" && s.Name != "Args" {
				continue
			}
		}
		vb, err := request("variables", map[string]interface{}{
			"variablesReference": s.VariablesReference,
		})
		if err != nil {
			slog.Debug("load dap variables failed", "scope", s.Name, "err", err)
			continue
		}
		var vr struct {
			Variables []struct {
				Name               string `json:"name"`
				Value              string `json:"value"`
				Type               string `json:"type"`
				VariablesReference int    `json:"variablesReference"`
			} `json:"variables"`
		}
		if err := json.Unmarshal(vb, &vr); err != nil {
			return fmt.Errorf("decode dap variables response: %w", err)
		}
		for _, v := range vr.Variables {
			// G14: preserve the adapter-owned variablesReference so nested
			// expansion and setVariable/dataBreakpointInfo use the real id.
			locals = append(locals, DebugVariable{
				Name:               v.Name,
				Value:              v.Value,
				Type:               v.Type,
				VariablesReference: v.VariablesReference,
			})
		}
		if strings.Contains(strings.ToLower(s.Name), "local") {
			break
		}
	}
	owner.mu.Lock()
	if owner.runGeneration == generation {
		owner.locals = locals
	}
	owner.mu.Unlock()
	return nil
}
