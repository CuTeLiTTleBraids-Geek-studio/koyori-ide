package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testBrowserCDPClient() *nodeCDPClient {
	return &nodeCDPClient{
		pending: make(map[int64]chan cdpResponse),
		callResultHook: func(context.Context, string, map[string]interface{}) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}
}

func testBrowserServiceDeps(processes ...*fakeBrowserProcess) (browserDebugDeps, *atomic.Int32) {
	var next atomic.Int32
	cleanupCalls := new(atomic.Int32)
	return browserDebugDeps{
		isFile:       func(string) bool { return true },
		mkdirTemp:    func(string, string) (string, error) { return "/tmp/browser-profile", nil },
		chmod:        func(string, os.FileMode) error { return nil },
		removeAll:    func(string) error { cleanupCalls.Add(1); return nil },
		allocatePort: func() (int, error) { return 9222 + int(next.Load()), nil },
		startProcess: func(string, []string) (browserProcess, error) {
			i := int(next.Add(1)) - 1
			if i >= len(processes) {
				return nil, errors.New("unexpected browser start")
			}
			return processes[i], nil
		},
	}, cleanupCalls
}

func configureBrowserService(d *DebugService, deps browserDebugDeps, clients *[]*nodeCDPClient) {
	d.browserDeps = deps
	d.browserEnumerate = func(context.Context, string, time.Duration) ([]BrowserTarget, error) {
		return []BrowserTarget{
			{ID: "page-a", Type: "page", Title: "A", URL: "http://localhost/a", WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/a"},
			{ID: "page-b", Type: "page", Title: "B", URL: "http://localhost/b", WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/b"},
		}, nil
	}
	d.browserConnect = func(target BrowserTarget, _ time.Duration) (*nodeCDPClient, error) {
		client := testBrowserCDPClient()
		*clients = append(*clients, client)
		return client, nil
	}
}

func browserLaunchConfig() DebugLaunchConfig {
	return DebugLaunchConfig{
		Name:           "Chrome app",
		Kind:           "browser",
		Browser:        "chrome",
		Request:        "launch",
		ExecutablePath: "/opt/chrome",
		URL:            "http://localhost:3000",
		WebRoot:        "/repo/web",
		SourceMaps:     true,
		PathMappings:   map[string]string{"/src": "/repo/src"},
	}
}

func TestDebugServiceBrowserLaunchRestartAndNaturalExitAreGenerationBound(t *testing.T) {
	first := newFakeBrowserProcess(true)
	second := newFakeBrowserProcess(false)
	deps, cleanupCalls := testBrowserServiceDeps(first, second)
	d := NewDebugService()
	var clients []*nodeCDPClient
	configureBrowserService(d, deps, &clients)

	info, err := d.LaunchWithConfig(browserLaunchConfig())
	if err != nil {
		t.Fatalf("browser launch: %v", err)
	}
	if !info.Running || info.Mode != "browser" || info.Address != "127.0.0.1:9222" {
		t.Fatalf("browser session = %+v", info)
	}
	firstSnapshot := d.GetState()
	if firstSnapshot.Generation == 0 || firstSnapshot.BrowserTargetID != "page-a" {
		t.Fatalf("first snapshot = %+v", firstSnapshot)
	}
	if got := targetInfoIDs(firstSnapshot.BrowserTargets); !reflect.DeepEqual(got, []string{"page-a", "page-b"}) {
		t.Fatalf("browser targets = %#v", got)
	}
	oldConsole := clients[0].onBrowserConsole

	restarted, err := d.Restart()
	if err != nil {
		t.Fatalf("browser restart: %v", err)
	}
	if !restarted.Running || restarted.Mode != "browser" || restarted.Address != "127.0.0.1:9223" {
		t.Fatalf("restarted session = %+v", restarted)
	}
	secondSnapshot := d.GetState()
	if secondSnapshot.Generation <= firstSnapshot.Generation {
		t.Fatalf("restart generation = %d, want > %d", secondSnapshot.Generation, firstSnapshot.Generation)
	}
	if got := first.waitCalls.Load(); got != 1 {
		t.Fatalf("first process Wait calls = %d, want 1", got)
	}
	if got := first.killCalls.Load(); got != 1 {
		t.Fatalf("first process Kill calls = %d, want 1", got)
	}
	oldConsole(BrowserConsoleEntry{Level: "log", Text: "stale"})
	if got := d.GetState().BrowserConsole; len(got) != 0 {
		t.Fatalf("old generation console crossed restart: %+v", got)
	}

	second.releaseProcess()
	waitForBrowserCondition(t, func() bool { return !d.GetState().Session.Running })
	if got := second.waitCalls.Load(); got != 1 {
		t.Fatalf("second process Wait calls = %d, want 1", got)
	}
	if got := cleanupCalls.Load(); got != 2 {
		t.Fatalf("profile cleanup calls = %d, want 2", got)
	}
}

func TestDebugServiceBrowserTargetSelectionUsesEpochAndRejectsStaleReconnect(t *testing.T) {
	d := NewDebugService()
	d.browserDeps = browserDebugDeps{}
	d.browserEnumerate = func(context.Context, string, time.Duration) ([]BrowserTarget, error) {
		return []BrowserTarget{
			{ID: "page-a", Type: "page", WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/a"},
			{ID: "page-b", Type: "page", WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/b"},
		}, nil
	}
	var mu sync.Mutex
	clients := make(map[string][]*nodeCDPClient)
	d.browserConnect = func(target BrowserTarget, _ time.Duration) (*nodeCDPClient, error) {
		client := testBrowserCDPClient()
		mu.Lock()
		clients[target.ID] = append(clients[target.ID], client)
		mu.Unlock()
		return client, nil
	}

	attach := browserLaunchConfig()
	attach.Request = "attach"
	attach.Address = "127.0.0.1:9222"
	attach.ExecutablePath = ""
	attach.URL = ""
	if _, err := d.LaunchWithConfig(attach); err != nil {
		t.Fatalf("browser attach: %v", err)
	}
	mu.Lock()
	clientA := clients["page-a"][0]
	mu.Unlock()
	clientA.onBrowserConsole(BrowserConsoleEntry{Level: "log", Text: "from-a"})
	clientA.onBrowserNetwork(BrowserNetworkEntry{RequestID: "a", Phase: "request", URL: "http://localhost/a"})

	if err := d.SelectBrowserTarget("page-b"); err != nil {
		t.Fatalf("select page-b: %v", err)
	}
	snapshot := d.GetState()
	if snapshot.BrowserTargetID != "page-b" || len(snapshot.BrowserConsole) != 0 || len(snapshot.BrowserNetwork) != 0 {
		t.Fatalf("selected target snapshot = %+v", snapshot)
	}
	mu.Lock()
	clientB := clients["page-b"][0]
	mu.Unlock()
	clientA.onBrowserConsole(BrowserConsoleEntry{Level: "warning", Text: "stale-a"})
	clientB.onBrowserConsole(BrowserConsoleEntry{Level: "log", Text: "from-b"})
	if got := d.GetState().BrowserConsole; len(got) != 1 || got[0].Text != "from-b" {
		t.Fatalf("target epoch console = %+v", got)
	}

	connectStarted := make(chan struct{})
	releaseConnect := make(chan struct{})
	d.browserConnect = func(target BrowserTarget, _ time.Duration) (*nodeCDPClient, error) {
		if target.ID == "page-a" {
			close(connectStarted)
			<-releaseConnect
		}
		return testBrowserCDPClient(), nil
	}
	selectDone := make(chan error, 1)
	go func() { selectDone <- d.SelectBrowserTarget("page-a") }()
	<-connectStarted
	d.mu.Lock()
	d.beginRunLocked()
	d.mu.Unlock()
	close(releaseConnect)
	if err := <-selectDone; err == nil || !strings.Contains(err.Error(), "run changed") {
		t.Fatalf("stale target reconnect error = %v", err)
	}
	if got := d.GetState().BrowserTargetID; got == "page-a" {
		t.Fatalf("stale reconnect selected old-run target %q", got)
	}
}

func TestDebugServiceBrowserEventRingsAreBoundedAndCopied(t *testing.T) {
	d := NewDebugService()
	d.mu.Lock()
	generation := d.beginRunLocked()
	d.mode = "browser"
	d.running = true
	d.browserTargetID = "page"
	d.browserTargetEpoch++
	epoch := d.browserTargetEpoch
	d.mu.Unlock()

	console := d.browserConsoleHandler(d.DebugSession, generation, epoch)
	network := d.browserNetworkHandler(d.DebugSession, generation, epoch)
	for i := 0; i < maxBrowserConsoleEntries+3; i++ {
		console(BrowserConsoleEntry{Level: "log", Text: fmt.Sprintf("console-%d", i)})
	}
	for i := 0; i < maxBrowserNetworkEntries+2; i++ {
		network(BrowserNetworkEntry{RequestID: fmt.Sprintf("request-%d", i), Phase: "request"})
	}
	snapshot := d.GetState()
	if len(snapshot.BrowserConsole) != maxBrowserConsoleEntries || snapshot.BrowserConsole[0].Text != "console-3" {
		t.Fatalf("console ring length/first = %d/%+v", len(snapshot.BrowserConsole), snapshot.BrowserConsole[0])
	}
	if len(snapshot.BrowserNetwork) != maxBrowserNetworkEntries || snapshot.BrowserNetwork[0].RequestID != "request-2" {
		t.Fatalf("network ring length/first = %d/%+v", len(snapshot.BrowserNetwork), snapshot.BrowserNetwork[0])
	}
	snapshot.BrowserConsole[0].Text = "mutated"
	snapshot.BrowserNetwork[0].RequestID = "mutated"
	again := d.GetState()
	if again.BrowserConsole[0].Text == "mutated" || again.BrowserNetwork[0].RequestID == "mutated" {
		t.Fatal("GetState exposed browser event backing storage")
	}
}

func TestNodeCDPClientParsesBrowserConsoleAndNetworkEvents(t *testing.T) {
	client := testBrowserCDPClient()
	var console []BrowserConsoleEntry
	var network []BrowserNetworkEntry
	client.onBrowserConsole = func(entry BrowserConsoleEntry) { console = append(console, entry) }
	client.onBrowserNetwork = func(entry BrowserNetworkEntry) { network = append(network, entry) }

	client.handleEvent(cdpResponse{Method: "Runtime.consoleAPICalled", Params: json.RawMessage(`{
		"type":"warning","timestamp":123.5,
		"args":[{"type":"string","value":"watch out"},{"type":"number","value":7}],
		"stackTrace":{"callFrames":[{"url":"http://localhost/app.js","lineNumber":4,"columnNumber":2}]}
	}`)})
	client.handleEvent(cdpResponse{Method: "Network.requestWillBeSent", Params: json.RawMessage(`{
		"requestId":"req-1","type":"Fetch","timestamp":10,
		"request":{"url":"http://localhost/api","method":"POST"}
	}`)})
	client.handleEvent(cdpResponse{Method: "Network.responseReceived", Params: json.RawMessage(`{
		"requestId":"req-1","type":"Fetch","timestamp":11,
		"response":{"url":"http://localhost/api","status":201,"mimeType":"application/json"}
	}`)})
	client.handleEvent(cdpResponse{Method: "Network.loadingFailed", Params: json.RawMessage(`{
		"requestId":"req-2","timestamp":12,"errorText":"net::ERR_FAILED","canceled":false
	}`)})

	if len(console) != 1 || console[0].Level != "warning" || console[0].Text != "watch out 7" || console[0].URL != "http://localhost/app.js" || console[0].Line != 5 {
		t.Fatalf("console events = %+v", console)
	}
	if len(network) != 3 {
		t.Fatalf("network events = %+v", network)
	}
	if network[0].Phase != "request" || network[0].Method != "POST" || network[1].Phase != "response" || network[1].Status != 201 || network[2].Phase != "failed" || network[2].Error != "net::ERR_FAILED" {
		t.Fatalf("parsed network events = %+v", network)
	}
}

func TestBrowserSourcePathMappingsRoundTrip(t *testing.T) {
	spec := browserDebugSpec{
		URL:          "http://localhost:3000/app",
		WebRoot:      "/repo/web",
		PathMappings: map[string]string{"/src": "/repo/src"},
	}
	if got := browserSourceURLToLocal("http://localhost:3000/src/pkg/main.ts", spec); got != filepath.FromSlash("/repo/src/pkg/main.ts") {
		t.Fatalf("mapped source path = %q", got)
	}
	if got := browserSourceURLToLocal("http://localhost:3000/js/app.js", spec); got != filepath.FromSlash("/repo/web/js/app.js") {
		t.Fatalf("webRoot source path = %q", got)
	}
	if got := browserLocalPathToURL(filepath.FromSlash("/repo/src/pkg/main.ts"), spec); got != "http://localhost:3000/src/pkg/main.ts" {
		t.Fatalf("mapped breakpoint URL = %q", got)
	}
	if got := browserLocalPathToURL(filepath.FromSlash("/repo/web/js/app.js"), spec); got != "http://localhost:3000/js/app.js" {
		t.Fatalf("webRoot breakpoint URL = %q", got)
	}
}

func targetInfoIDs(targets []BrowserTarget) []string {
	ids := make([]string, len(targets))
	for i := range targets {
		ids[i] = targets[i].ID
	}
	return ids
}

func waitForBrowserCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
