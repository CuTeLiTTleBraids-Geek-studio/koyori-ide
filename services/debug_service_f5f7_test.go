package services

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockDAPAdapterF5F7 is a minimal DAP server that understands the F-5/F-7
// commands (dataBreakpointInfo, setDataBreakpoints, exceptionInfo,
// loadedSources, modules, completions, stepInTargets, breakpointLocations).
// It records the arguments it receives so tests can assert on them.
type mockDAPAdapterF5F7 struct {
	ln  net.Listener
	seq int32
	mu  sync.Mutex

	// recorded arguments for assertions
	dataBpInfoVarsRef int
	dataBpInfoName    string
	setDataBps        []map[string]interface{}
	exceptionThreadID int
	completionsFrame  int
	completionsText   string
	completionsColumn int
	stepInFrame       int
	// GOAL-P1-03: records what stepIn actually received. The default case
	// answers stepIn with success but recorded nothing, so a dropped targetId
	// was indistinguishable from a delivered one.
	stepInTargetID      int
	stepInTargetPresent bool
	stepInCalls         int
	// GOAL-P1-03: when set, the adapter rejects stepInTargets the way an adapter
	// that does not implement the request does. Used to prove StepInTargetsForStop
	// reports Supported=false instead of failing the call.
	rejectStepInTargets bool
	bpLocsURI           string
	bpLocsStartLine     int
	bpLocsEndLine       int
}

func startMockDAPF5F7(t *testing.T) (*mockDAPAdapterF5F7, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := &mockDAPAdapterF5F7{ln: ln}
	go m.serve()
	return m, ln.Addr().String()
}

// startMockDAPNoStepInTargets starts an adapter that rejects stepInTargets,
// mimicking a real adapter that does not implement the request (GOAL-P1-03 AC 3).
//
// It still advertises supportsStepInTargetsRequest during initialize, which is
// deliberately the harsher case: an adapter that claims support and then fails
// the request must degrade to "no menu" rather than surfacing an error.
func startMockDAPNoStepInTargets(t *testing.T) (*mockDAPAdapterF5F7, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := &mockDAPAdapterF5F7{ln: ln, rejectStepInTargets: true}
	go m.serve()
	return m, ln.Addr().String()
}

func (m *mockDAPAdapterF5F7) close() { _ = m.ln.Close() }

func (m *mockDAPAdapterF5F7) serve() {
	conn, err := m.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		msg, err := readDAPMessage(r)
		if err != nil {
			return
		}
		if msg.Type != "request" {
			continue
		}
		switch msg.Command {
		case "initialize":
			m.respond(conn, msg, true, map[string]interface{}{
				"supportsDataBreakpoints":            true,
				"supportsExceptionInfoRequest":       true,
				"supportsLoadedSourcesRequest":       true,
				"supportsModulesRequest":             true,
				"supportsCompletionsRequest":         true,
				"supportsStepInTargetsRequest":       true,
				"supportsBreakpointLocationsRequest": true,
			})
			m.event(conn, "initialized", map[string]interface{}{})
		case "launch":
			m.respond(conn, msg, true, map[string]interface{}{})
		case "configurationDone":
			m.respond(conn, msg, true, map[string]interface{}{})
			m.event(conn, "stopped", map[string]interface{}{
				"reason": "entry", "threadId": 1,
			})
		case "disconnect":
			m.respond(conn, msg, true, map[string]interface{}{})
			return
		case "dataBreakpointInfo":
			var args struct {
				VarsRef int    `json:"variablesReference"`
				Name    string `json:"name"`
			}
			_ = json.Unmarshal(msg.Arguments, &args)
			m.mu.Lock()
			m.dataBpInfoVarsRef = args.VarsRef
			m.dataBpInfoName = args.Name
			m.mu.Unlock()
			m.respond(conn, msg, true, map[string]interface{}{
				"dataId":      "var:x",
				"description": "Break when value of 'x' changes",
				"accessTypes": []string{"read", "write", "readWrite"},
				"canPersist":  false,
			})
		case "setDataBreakpoints":
			var args struct {
				Breakpoints []map[string]interface{} `json:"breakpoints"`
			}
			_ = json.Unmarshal(msg.Arguments, &args)
			m.mu.Lock()
			m.setDataBps = args.Breakpoints
			m.mu.Unlock()
			out := make([]map[string]interface{}, 0, len(args.Breakpoints))
			for i := range args.Breakpoints {
				out = append(out, map[string]interface{}{
					"id": i + 1, "verified": true,
				})
			}
			m.respond(conn, msg, true, map[string]interface{}{"breakpoints": out})
		case "exceptionInfo":
			var args struct {
				ThreadID int `json:"threadId"`
			}
			_ = json.Unmarshal(msg.Arguments, &args)
			m.mu.Lock()
			m.exceptionThreadID = args.ThreadID
			m.mu.Unlock()
			m.respond(conn, msg, true, map[string]interface{}{
				"exceptionId": "runtime.error: nil pointer",
				"description": "nil pointer dereference",
				"breakMode":   "always",
				"details": map[string]interface{}{
					"message":    "runtime error: invalid memory address",
					"typeName":   "runtime.error",
					"stackTrace": "main.go:10",
				},
			})
		case "loadedSources":
			m.respond(conn, msg, true, map[string]interface{}{
				"sources": []map[string]interface{}{
					{"name": "main.go", "path": "/tmp/main.go"},
					{"name": "lib.go", "path": "/tmp/lib.go", "sourceReference": 0},
				},
			})
		case "modules":
			m.respond(conn, msg, true, map[string]interface{}{
				"modules": []map[string]interface{}{
					{"id": 1, "name": "main", "path": "/tmp/main", "version": "v1.0.0"},
				},
				"totalModules": 1,
			})
		case "completions":
			var args struct {
				FrameID int    `json:"frameId"`
				Text    string `json:"text"`
				Column  int    `json:"column"`
			}
			_ = json.Unmarshal(msg.Arguments, &args)
			m.mu.Lock()
			m.completionsFrame = args.FrameID
			m.completionsText = args.Text
			m.completionsColumn = args.Column
			m.mu.Unlock()
			m.respond(conn, msg, true, map[string]interface{}{
				"targets": []map[string]interface{}{
					{"label": "x", "type": "variable"},
					{"label": "y", "type": "variable"},
				},
			})
		case "stepInTargets":
			var args struct {
				FrameID int `json:"frameId"`
			}
			_ = json.Unmarshal(msg.Arguments, &args)
			m.mu.Lock()
			m.stepInFrame = args.FrameID
			reject := m.rejectStepInTargets
			m.mu.Unlock()
			// GOAL-P1-03: an adapter without stepInTargets support answers with
			// an error response. Simulating that is how AC 3 (no regression on
			// unsupported adapters) becomes testable.
			if reject {
				m.respond(conn, msg, false, map[string]interface{}{})
				continue
			}
			m.respond(conn, msg, true, map[string]interface{}{
				"targets": []map[string]interface{}{
					{"id": 1, "label": "main.foo"},
					{"id": 2, "label": "main.bar"},
				},
			})
		case "stepIn":
			// GOAL-P1-03: record whether targetId arrived. A pointer
			// distinguishes "absent" (default step-in) from "present but zero",
			// which a plain int cannot express.
			var args struct {
				ThreadID int  `json:"threadId"`
				TargetID *int `json:"targetId"`
			}
			_ = json.Unmarshal(msg.Arguments, &args)
			m.mu.Lock()
			m.stepInCalls++
			m.stepInTargetPresent = args.TargetID != nil
			if args.TargetID != nil {
				m.stepInTargetID = *args.TargetID
			} else {
				m.stepInTargetID = 0
			}
			m.mu.Unlock()
			m.respond(conn, msg, true, map[string]interface{}{})
			// A real adapter stops again after a step. Emitting the event keeps
			// the session's stop lifecycle (and stopSequence) realistic.
			m.event(conn, "stopped", map[string]interface{}{
				"reason": "step", "threadId": 1,
			})
		case "breakpointLocations":
			var args struct {
				Source struct {
					Path string `json:"path"`
				} `json:"source"`
				Line    int `json:"line"`
				EndLine int `json:"endLine"`
			}
			_ = json.Unmarshal(msg.Arguments, &args)
			m.mu.Lock()
			m.bpLocsURI = args.Source.Path
			m.bpLocsStartLine = args.Line
			m.bpLocsEndLine = args.EndLine
			m.mu.Unlock()
			m.respond(conn, msg, true, map[string]interface{}{
				"breakpoints": []map[string]interface{}{
					{"line": args.Line, "endLine": args.Line, "column": 1, "endColumn": 1},
				},
			})
		default:
			m.respond(conn, msg, true, map[string]interface{}{})
		}
	}
}

func (m *mockDAPAdapterF5F7) nextSeq() int {
	return int(atomic.AddInt32(&m.seq, 1))
}

func (m *mockDAPAdapterF5F7) respond(w io.Writer, req dapMessage, ok bool, body map[string]interface{}) {
	payload := map[string]interface{}{
		"seq":         m.nextSeq(),
		"type":        "response",
		"request_seq": req.Seq,
		"success":     ok,
		"command":     req.Command,
		"body":        body,
	}
	_ = writeDAPMessage(w, payload)
}

func (m *mockDAPAdapterF5F7) event(w io.Writer, name string, body map[string]interface{}) {
	payload := map[string]interface{}{
		"seq":   m.nextSeq(),
		"type":  "event",
		"event": name,
		"body":  body,
	}
	_ = writeDAPMessage(w, payload)
}

// waitStoppedF5F7 polls the debug state until the session reports stopped
// or the deadline elapses.
func waitStoppedF5F7(t *testing.T, d *DebugService, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st := d.GetState()
		if st.Session.Stopped || st.StopReason == "entry" || st.Session.StopReason == "entry" {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("debug session did not reach stopped state within %v", timeout)
}

// connectF5F7Mock is a helper that connects a fresh DebugService to the F5F7
// mock and waits for the entry stopped event.
func connectF5F7Mock(t *testing.T, addr string) *DebugService {
	t.Helper()
	d := NewDebugService()
	if _, err := d.ConnectMockDAP(addr, map[string]interface{}{
		"request": "launch", "program": ".",
	}); err != nil {
		t.Fatalf("ConnectMockDAP: %v", err)
	}
	waitStoppedF5F7(t, d, 3*time.Second)
	return d
}

// ---------------------------------------------------------------------------
// F-5 Data Breakpoint tests
// ---------------------------------------------------------------------------

func TestDataBreakpointInfo_ValidArgs(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	infos, err := d.DataBreakpointInfo(10, "x")
	if err != nil {
		t.Fatalf("DataBreakpointInfo: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(infos))
	}
	if infos[0].DataID != "var:x" {
		t.Errorf("dataId=%q want var:x", infos[0].DataID)
	}
	if infos[0].Description == "" {
		t.Error("empty description")
	}
	if len(infos[0].AccessTypes) == 0 {
		t.Error("empty accessTypes")
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.dataBpInfoVarsRef != 10 {
		t.Errorf("recorded varsRef=%d want 10", mock.dataBpInfoVarsRef)
	}
	if mock.dataBpInfoName != "x" {
		t.Errorf("recorded name=%q want x", mock.dataBpInfoName)
	}
}

func TestDataBreakpointInfo_InvalidReference(t *testing.T) {
	// No DAP connection established — dapRequestBody must return an error.
	d := NewDebugService()
	_, err := d.DataBreakpointInfo(0, "")
	if err == nil {
		t.Fatal("expected error when no DAP connection is established")
	}
}

func TestSetDataBreakpoints_EmptyList(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	if err := d.SetDataBreakpoints(nil); err != nil {
		t.Fatalf("SetDataBreakpoints(nil): %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.setDataBps) != 0 {
		t.Errorf("expected empty list forwarded, got %d", len(mock.setDataBps))
	}
}

func TestSetDataBreakpoints_ValidBreakpoints(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	bps := []DataBreakpoint{
		{DataID: "var:x", AccessType: "write"},
		{DataID: "var:y", AccessType: "readWrite", Condition: "y > 0", HitCondition: ">=2"},
	}
	if err := d.SetDataBreakpoints(bps); err != nil {
		t.Fatalf("SetDataBreakpoints: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.setDataBps) != 2 {
		t.Fatalf("expected 2 forwarded, got %d", len(mock.setDataBps))
	}
	if mock.setDataBps[0]["dataId"] != "var:x" {
		t.Errorf("bp0 dataId=%v want var:x", mock.setDataBps[0]["dataId"])
	}
	if mock.setDataBps[0]["accessType"] != "write" {
		t.Errorf("bp0 accessType=%v want write", mock.setDataBps[0]["accessType"])
	}
	if mock.setDataBps[1]["condition"] != "y > 0" {
		t.Errorf("bp1 condition=%v", mock.setDataBps[1]["condition"])
	}
	if mock.setDataBps[1]["hitCondition"] != ">=2" {
		t.Errorf("bp1 hitCondition=%v", mock.setDataBps[1]["hitCondition"])
	}
}

// ---------------------------------------------------------------------------
// F-7 Debug auxiliary tests
// ---------------------------------------------------------------------------

func TestExceptionInfo_ValidThread(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	info, err := d.ExceptionInfo(1)
	if err != nil {
		t.Fatalf("ExceptionInfo: %v", err)
	}
	if info == nil {
		t.Fatal("nil exception info")
	}
	if info.ExceptionID == "" {
		t.Error("empty exceptionId")
	}
	if info.BreakMode == "" {
		t.Error("empty breakMode")
	}
	if info.Details == nil {
		t.Error("nil details")
	}
	if info.Details.Message == "" {
		t.Error("empty details.message")
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.exceptionThreadID != 1 {
		t.Errorf("recorded threadId=%d want 1", mock.exceptionThreadID)
	}
}

func TestLoadedSources_NoSession(t *testing.T) {
	d := NewDebugService()
	_, err := d.LoadedSources()
	if err == nil {
		t.Fatal("expected error when no DAP connection is established")
	}
}

func TestModules_NoSession(t *testing.T) {
	d := NewDebugService()
	_, err := d.Modules()
	if err == nil {
		t.Fatal("expected error when no DAP connection is established")
	}
}

func TestCompletions_ValidFrame(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	items, err := d.Completions(1, "x", 2)
	if err != nil {
		t.Fatalf("Completions: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Label != "x" {
		t.Errorf("item0 label=%q want x", items[0].Label)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.completionsFrame != 1 {
		t.Errorf("recorded frame=%d want 1", mock.completionsFrame)
	}
	if mock.completionsText != "x" {
		t.Errorf("recorded text=%q want x", mock.completionsText)
	}
	if mock.completionsColumn != 2 {
		t.Errorf("recorded column=%d want 2", mock.completionsColumn)
	}
}

func TestStepInTargets_ValidFrame(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	targets, err := d.StepInTargets(1)
	if err != nil {
		t.Fatalf("StepInTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	if targets[0].ID != 1 || targets[0].Label == "" {
		t.Errorf("target0=%+v", targets[0])
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.stepInFrame != 1 {
		t.Errorf("recorded frame=%d want 1", mock.stepInFrame)
	}
}

func TestBreakpointLocations_ValidUri(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	locs, err := d.BreakpointLocations("/tmp/main.go", 5, 10)
	if err != nil {
		t.Fatalf("BreakpointLocations: %v", err)
	}
	if len(locs) == 0 {
		t.Fatal("expected at least one location")
	}
	if locs[0].Line == 0 {
		t.Error("empty line in first location")
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.bpLocsURI != "/tmp/main.go" {
		t.Errorf("recorded uri=%q want /tmp/main.go", mock.bpLocsURI)
	}
	if mock.bpLocsStartLine != 5 {
		t.Errorf("recorded startLine=%d want 5", mock.bpLocsStartLine)
	}
	if mock.bpLocsEndLine != 10 {
		t.Errorf("recorded endLine=%d want 10", mock.bpLocsEndLine)
	}
}

// TestDebugAux_F5F7Smoke is an aggregate smoke test so the
// `go test -run TestDebugAux` acceptance command from task-1.md has matching
// tests. It exercises every F-7 auxiliary method once on a live mock session.
func TestDebugAux_F5F7Smoke(t *testing.T) {
	mock, addr := startMockDAPF5F7(t)
	defer mock.close()
	d := connectF5F7Mock(t, addr)
	defer d.Stop()

	if _, err := d.ExceptionInfo(1); err != nil {
		t.Errorf("ExceptionInfo: %v", err)
	}
	if _, err := d.LoadedSources(); err != nil {
		t.Errorf("LoadedSources: %v", err)
	}
	if _, err := d.Modules(); err != nil {
		t.Errorf("Modules: %v", err)
	}
	if _, err := d.Completions(1, "x", 1); err != nil {
		t.Errorf("Completions: %v", err)
	}
	if _, err := d.StepInTargets(1); err != nil {
		t.Errorf("StepInTargets: %v", err)
	}
	if _, err := d.BreakpointLocations("/tmp/main.go", 1, 5); err != nil {
		t.Errorf("BreakpointLocations: %v", err)
	}
}
