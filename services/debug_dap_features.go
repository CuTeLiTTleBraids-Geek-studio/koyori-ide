package services

import (
	"encoding/json"
	"fmt"
)

// ---------------------------------------------------------------------------
// F-5 (prompt-2.md): Data Breakpoints
// ---------------------------------------------------------------------------

// DataBreakpointInfo queries the debugger for information about a variable
// that can have a data breakpoint set on it. Maps to DAP dataBreakpointInfoRequest.
// variablesReference is the reference from a Variables response; name is the
// variable name. Returns a list of data breakpoint info entries (usually one).
func (d *DebugService) DataBreakpointInfo(variablesReference int, name string) ([]DataBreakpointInfo, error) {
	args := map[string]interface{}{
		"variablesReference": variablesReference,
	}
	if name != "" {
		args["name"] = name
	}
	body, err := d.dapRequestBody("dataBreakpointInfo", args)
	if err != nil {
		return nil, err
	}
	var resp struct {
		DataID      string   `json:"dataId"`
		Description string   `json:"description"`
		AccessTypes []string `json:"accessTypes"`
		CanPersist  bool     `json:"canPersist"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode dap dataBreakpointInfo response: %w", err)
	}
	return []DataBreakpointInfo{{
		DataID:      resp.DataID,
		Description: resp.Description,
		AccessTypes: resp.AccessTypes,
		CanPersist:  resp.CanPersist,
	}}, nil
}

// SetDataBreakpoints replaces all data breakpoints with the given list.
// An empty list clears all data breakpoints. Maps to DAP setDataBreakpointsRequest.
func (d *DebugService) SetDataBreakpoints(breakpoints []DataBreakpoint) error {
	dapBps := make([]map[string]interface{}, 0, len(breakpoints))
	for _, bp := range breakpoints {
		entry := map[string]interface{}{
			"dataId":     bp.DataID,
			"accessType": bp.AccessType,
		}
		if bp.Condition != "" {
			entry["condition"] = bp.Condition
		}
		if bp.HitCondition != "" {
			entry["hitCondition"] = bp.HitCondition
		}
		dapBps = append(dapBps, entry)
	}
	return d.dapRequest("setDataBreakpoints", map[string]interface{}{
		"breakpoints": dapBps,
	})
}

// ---------------------------------------------------------------------------
// F-7 (prompt-2.md): Debug auxiliary methods
// ---------------------------------------------------------------------------

// ExceptionInfo returns information about the exception that caused the
// debuggee to stop on the given thread. Maps to DAP exceptionInfoRequest.
func (d *DebugService) ExceptionInfo(threadID int) (*ExceptionInfoResp, error) {
	body, err := d.dapRequestBody("exceptionInfo", map[string]interface{}{
		"threadId": threadID,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		ExceptionID string            `json:"exceptionId"`
		Description string            `json:"description"`
		BreakMode   string            `json:"breakMode"`
		Details     *ExceptionDetails `json:"details"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode dap exceptionInfo response: %w", err)
	}
	return &ExceptionInfoResp{
		ExceptionID: resp.ExceptionID,
		Description: resp.Description,
		BreakMode:   resp.BreakMode,
		Details:     resp.Details,
	}, nil
}

// LoadedSources returns the list of source files loaded in the debugger.
// Maps to DAP loadedSourcesRequest.
func (d *DebugService) LoadedSources() ([]DebugSource, error) {
	body, err := d.dapRequestBody("loadedSources", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Sources []DebugSource `json:"sources"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode dap loadedSources response: %w", err)
	}
	return resp.Sources, nil
}

// Modules returns the list of modules loaded in the debugger.
// Maps to DAP modulesRequest.
func (d *DebugService) Modules() ([]DebugModule, error) {
	body, err := d.dapRequestBody("modules", map[string]interface{}{
		"startModule": 0,
		"moduleCount": 0, // 0 = all modules
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Modules []DebugModule `json:"modules"`
		Total   int           `json:"totalModules"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode dap modules response: %w", err)
	}
	return resp.Modules, nil
}

// Completions returns completion items for the debug console at the given
// cursor position. frameID identifies the stack frame context; text is the
// text typed so far; column is the 1-based cursor column.
// Maps to DAP completionsRequest.
func (d *DebugService) Completions(frameID int, text string, column int) ([]DebugCompletionItem, error) {
	body, err := d.dapRequestBody("completions", map[string]interface{}{
		"frameId": frameID,
		"text":    text,
		"column":  column,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Targets []DebugCompletionItem `json:"targets"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode dap completions response: %w", err)
	}
	return resp.Targets, nil
}

// StepInTargets returns the list of possible step-in targets for the given
// frame (e.g. specific overloads on one line). Maps to DAP stepInTargetsRequest.
func (d *DebugService) StepInTargets(frameID int) ([]StepInTarget, error) {
	body, err := d.dapRequestBody("stepInTargets", map[string]interface{}{
		"frameId": frameID,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Targets []StepInTarget `json:"targets"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode dap stepInTargets response: %w", err)
	}
	return resp.Targets, nil
}

// BreakpointLocations returns the valid breakpoint locations within a line
// range in a source file. Maps to DAP breakpointLocationsRequest.
func (d *DebugService) BreakpointLocations(uri string, startLine, endLine int) ([]BreakpointLocation, error) {
	source := map[string]interface{}{
		"path": uri,
	}
	args := map[string]interface{}{
		"source": source,
		"line":   startLine,
	}
	if endLine > 0 {
		args["endLine"] = endLine
	}
	body, err := d.dapRequestBody("breakpointLocations", args)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Breakpoints []BreakpointLocation `json:"breakpoints"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode dap breakpointLocations response: %w", err)
	}
	return resp.Breakpoints, nil
}
