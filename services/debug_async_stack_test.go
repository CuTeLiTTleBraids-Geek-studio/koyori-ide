package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestDAPInitializeCapabilitiesDetectsDelayedStackLoading(t *testing.T) {
	request := func(command string, args map[string]interface{}) (json.RawMessage, error) {
		if command != "initialize" {
			return nil, fmt.Errorf("unexpected command %q", command)
		}
		if args["supportsVariablePaging"] != false {
			t.Fatalf("initialize client capabilities changed: %+v", args)
		}
		return json.RawMessage(`{"supportsDelayedStackTraceLoading":true}`), nil
	}

	capabilities, err := dapInitializeCapabilitiesForRun(request)
	if err != nil {
		t.Fatalf("initialize capabilities: %v", err)
	}
	if !capabilities.SupportsDelayedStackTraceLoading {
		t.Fatal("supportsDelayedStackTraceLoading was not detected")
	}
}

func TestDAPInitializeCapabilitiesAreStoredInRunSnapshot(t *testing.T) {
	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.mu.Unlock()
	request := func(string, map[string]interface{}) (json.RawMessage, error) {
		return json.RawMessage(`{"supportsDelayedStackTraceLoading":true}`), nil
	}

	if err := initializeDAPSessionForRun(owner, generation, request); err != nil {
		t.Fatalf("initialize DAP session: %v", err)
	}
	snapshot := d.GetState()
	if snapshot.Generation != generation || !snapshot.SupportsDelayedStackTraceLoading {
		t.Fatalf("capability missing from snapshot: %+v", snapshot)
	}
}

func TestDAPAdapterWithoutDelayedLoadingRequestsCompleteStack(t *testing.T) {
	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.threadID = 3
	owner.mu.Unlock()
	request := func(command string, args map[string]interface{}) (json.RawMessage, error) {
		if command != "stackTrace" || args["levels"] != 0 {
			return nil, fmt.Errorf("expected complete stackTrace request, got %q %+v", command, args)
		}
		return json.RawMessage(`{"stackFrames":[],"totalFrames":0}`), nil
	}

	if err := d.refreshStackAndLocalsForRun(owner, generation, request); err != nil {
		t.Fatalf("refresh complete stack: %v", err)
	}
	if snapshot := d.GetState(); snapshot.StackHasMore {
		t.Fatalf("unsupported adapter exposed delayed loading: %+v", snapshot)
	}
}

func TestDAPStackPageForRunUsesPaginationAndPreservesAdapterBoundaries(t *testing.T) {
	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.threadID = 7
	owner.supportsDelayedStackTraceLoading = true
	owner.mu.Unlock()

	request := func(command string, args map[string]interface{}) (json.RawMessage, error) {
		if command != "stackTrace" {
			return nil, fmt.Errorf("unexpected command %q", command)
		}
		want := map[string]interface{}{"threadId": 7, "startFrame": 16, "levels": 2}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("stackTrace args = %#v, want %#v", args, want)
		}
		return json.RawMessage(`{
			"stackFrames":[
				{"id":21,"name":"await fetch","line":0,"column":0,"presentationHint":"label"},
				{"id":22,"name":"parent","line":9,"column":3,"source":{"path":"/repo/parent.ts"}}
			],
			"totalFrames":19
		}`), nil
	}

	page, err := d.loadDAPStackPageForRun(owner, generation, 16, 2, request)
	if err != nil {
		t.Fatalf("load DAP stack page: %v", err)
	}
	if page.Generation != generation || page.TotalFrames != 19 || !page.HasMore {
		t.Fatalf("unexpected page metadata: %+v", page)
	}
	if len(page.Frames) != 2 || page.Frames[0].PresentationHint != "label" || !page.Frames[0].AsyncBoundary {
		t.Fatalf("adapter async boundary was not preserved: %+v", page.Frames)
	}
}

func TestDAPStackPageForRunRejectsSameSessionRestartResponse(t *testing.T) {
	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.threadID = 1
	owner.supportsDelayedStackTraceLoading = true
	owner.mu.Unlock()

	request := func(string, map[string]interface{}) (json.RawMessage, error) {
		owner.mu.Lock()
		owner.beginRunLocked()
		owner.mu.Unlock()
		return json.RawMessage(`{"stackFrames":[{"id":1,"name":"old"}],"totalFrames":1}`), nil
	}

	if _, err := d.loadDAPStackPageForRun(owner, generation, 0, 16, request); err == nil {
		t.Fatal("stale DAP stack page from the previous run was accepted")
	}
}

func TestNodeAsyncStackSegmentsFollowEmbeddedAndParentIDChain(t *testing.T) {
	requested := make([]string, 0, 1)
	client := &nodeCDPClient{
		asyncStackTraceSupported: true,
		callResultHook: func(_ context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
			if method != "Runtime.getStackTrace" {
				return nil, fmt.Errorf("unexpected method %q", method)
			}
			id := params["stackTraceId"].(nodeStackTraceID).ID
			requested = append(requested, id)
			return json.RawMessage(`{
				"stackTrace":{
					"description":"timer",
					"callFrames":[{"functionName":"scheduled","url":"file:///repo/timer.js","lineNumber":8,"columnNumber":2}]
				}
			}`), nil
		},
	}
	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.cdp = client
	owner.supportsAsyncStackTrace = true
	owner.stopped = true
	owner.mu.Unlock()

	root := &nodeAsyncStackTrace{
		Description: "promise",
		CallFrames:  []nodeAsyncCallFrame{{FunctionName: "then", URL: "file:///repo/app.js", LineNumber: 3, ColumnNumber: 1}},
		Parent: &nodeAsyncStackTrace{
			Description: "await",
			CallFrames:  []nodeAsyncCallFrame{{FunctionName: "main", URL: "file:///repo/main.js", LineNumber: 1}},
			ParentID:    &nodeStackTraceID{ID: "remote-parent", DebuggerID: "debugger-1"},
		},
	}
	rootID := d.registerNodeAsyncStackForRun(owner, generation, root)
	if rootID == "" {
		t.Fatal("root continuation was not registered")
	}

	first, err := d.LoadAsyncStack(context.Background(), generation, rootID)
	if err != nil {
		t.Fatalf("load root async segment: %v", err)
	}
	if first.Description != "promise" || len(first.Frames) != 1 || first.ParentID == "" {
		t.Fatalf("unexpected root segment: %+v", first)
	}
	second, err := d.LoadAsyncStack(context.Background(), generation, first.ParentID)
	if err != nil {
		t.Fatalf("load embedded parent: %v", err)
	}
	if second.Description != "await" || second.ParentID == "" {
		t.Fatalf("unexpected embedded parent: %+v", second)
	}
	third, err := d.LoadAsyncStack(context.Background(), generation, second.ParentID)
	if err != nil {
		t.Fatalf("load parentId continuation: %v", err)
	}
	if third.Description != "timer" || len(third.Frames) != 1 || third.ParentID != "" {
		t.Fatalf("unexpected fetched parent: %+v", third)
	}
	if !reflect.DeepEqual(requested, []string{"remote-parent"}) {
		t.Fatalf("Runtime.getStackTrace ids = %+v", requested)
	}
}

func TestNodePausedEventPublishesOnlyAdapterProvidedAsyncStack(t *testing.T) {
	var trace *nodeAsyncStackTrace
	client := &nodeCDPClient{
		asyncStackTraceSupported: true,
		onAsyncStack: func(got *nodeAsyncStackTrace, _ *nodeStackTraceID) {
			trace = got
		},
	}
	client.handleEvent(cdpResponse{
		Method: "Debugger.paused",
		Params: json.RawMessage(`{
			"reason":"other",
			"callFrames":[],
			"asyncStackTrace":{
				"description":"Promise.then",
				"callFrames":[{"functionName":"caller","url":"file:///repo/app.js","lineNumber":4,"columnNumber":2}],
				"parentId":{"id":"next","debuggerId":"debugger-1"}
			}
		}`),
	})
	if trace == nil || trace.Description != "Promise.then" || len(trace.CallFrames) != 1 || trace.ParentID == nil || trace.ParentID.ID != "next" {
		t.Fatalf("paused async stack was not preserved: %+v", trace)
	}

	trace = nil
	client.mu.Lock()
	client.asyncStackTraceSupported = false
	client.mu.Unlock()
	client.handleEvent(cdpResponse{
		Method: "Debugger.paused",
		Params: json.RawMessage(`{"callFrames":[],"asyncStackTrace":{"description":"must stay hidden","callFrames":[]}}`),
	})
	if trace != nil {
		t.Fatalf("unsupported CDP client published async stack: %+v", trace)
	}
}

func TestNodeAsyncStackLoadHonorsCancellationAndGeneration(t *testing.T) {
	started := make(chan struct{})
	client := &nodeCDPClient{
		asyncStackTraceSupported: true,
		callResultHook: func(ctx context.Context, _ string, _ map[string]interface{}) (json.RawMessage, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.cdp = client
	owner.supportsAsyncStackTrace = true
	owner.stopped = true
	owner.mu.Unlock()
	continuation := d.registerNodeAsyncParentIDForRun(owner, generation, nodeStackTraceID{ID: "blocked"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := d.LoadAsyncStack(ctx, generation, continuation)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled load error = %v, want context.Canceled", err)
	}

	owner.mu.Lock()
	owner.beginRunLocked()
	owner.mu.Unlock()
	if _, err := d.LoadAsyncStack(context.Background(), generation, continuation); err == nil {
		t.Fatal("old continuation remained valid after same-session restart")
	}
}

func TestNodeAsyncStackLateResponseCannotCrossSameSessionRestart(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	client := &nodeCDPClient{
		asyncStackTraceSupported: true,
		callResultHook: func(context.Context, string, map[string]interface{}) (json.RawMessage, error) {
			close(started)
			<-release
			return json.RawMessage(`{"stackTrace":{"description":"old","callFrames":[]}}`), nil
		},
	}
	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.cdp = client
	owner.supportsAsyncStackTrace = true
	owner.stopped = true
	owner.mu.Unlock()
	continuation := d.registerNodeAsyncParentIDForRun(owner, generation, nodeStackTraceID{ID: "old"})
	done := make(chan error, 1)
	go func() {
		_, err := d.LoadAsyncStack(context.Background(), generation, continuation)
		done <- err
	}()
	<-started
	owner.mu.Lock()
	owner.beginRunLocked()
	owner.supportsAsyncStackTrace = true
	owner.stopped = true
	owner.asyncStackRootID = "new-root"
	owner.mu.Unlock()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("old CDP Runtime.getStackTrace response crossed restart")
	}
	if snapshot := d.GetState(); snapshot.AsyncStackRootID != "new-root" {
		t.Fatalf("late response changed new run snapshot: %+v", snapshot)
	}
}

func TestNodeAsyncContinuationIDsDoNotRepeatAcrossPausesInOneRun(t *testing.T) {
	d := NewDebugService()
	owner := d.DebugSession
	owner.mu.Lock()
	generation := owner.beginRunLocked()
	owner.supportsAsyncStackTrace = true
	owner.stopped = true
	owner.mu.Unlock()
	trace := &nodeAsyncStackTrace{Description: "promise"}
	first := d.registerNodeAsyncStackForRun(owner, generation, trace)
	owner.mu.Lock()
	owner.clearAsyncStackLocked()
	owner.mu.Unlock()
	second := d.registerNodeAsyncStackForRun(owner, generation, trace)
	if first == "" || second == "" || first == second {
		t.Fatalf("continuation IDs were reused across pauses: first=%q second=%q", first, second)
	}
}
