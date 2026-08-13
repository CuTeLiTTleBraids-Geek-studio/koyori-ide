package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoverBrowserExecutableSearchOrder(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		browser   string
		env       map[string]string
		foundPath string
		wantTried []string
	}{
		{
			name:      "windows chrome",
			goos:      "windows",
			browser:   "chrome",
			env:       map[string]string{"PROGRAMFILES": `C:\Program Files`, "PROGRAMFILES(X86)": `C:\Program Files (x86)`, "LOCALAPPDATA": `C:\Users\dev\AppData\Local`},
			foundPath: `C:\Users\dev\AppData\Local\Google\Chrome\Application\chrome.exe`,
			wantTried: []string{
				`C:\Program Files\Google\Chrome\Application\chrome.exe`,
				`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
				`C:\Users\dev\AppData\Local\Google\Chrome\Application\chrome.exe`,
			},
		},
		{
			name:      "windows edge",
			goos:      "windows",
			browser:   "edge",
			env:       map[string]string{"PROGRAMFILES": `C:\Program Files`, "PROGRAMFILES(X86)": `C:\Program Files (x86)`, "LOCALAPPDATA": `C:\Users\dev\AppData\Local`},
			foundPath: `C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			wantTried: []string{
				`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
				`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			},
		},
		{
			name:      "darwin chrome",
			goos:      "darwin",
			browser:   "chrome",
			env:       map[string]string{"HOME": "/Users/dev"},
			foundPath: "/Users/dev/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			wantTried: []string{
				"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
				"/Users/dev/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			},
		},
		{
			name:      "darwin edge",
			goos:      "darwin",
			browser:   "edge",
			env:       map[string]string{"HOME": "/Users/dev"},
			foundPath: "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			wantTried: []string{
				"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			},
		},
		{
			name:      "linux chrome",
			goos:      "linux",
			browser:   "chrome",
			foundPath: "/usr/bin/chromium",
			wantTried: []string{"google-chrome-stable", "google-chrome", "chromium", "chromium-browser"},
		},
		{
			name:      "linux edge",
			goos:      "linux",
			browser:   "edge",
			foundPath: "/opt/microsoft/msedge/microsoft-edge",
			wantTried: []string{"microsoft-edge-stable", "microsoft-edge", "microsoft-edge-beta", "microsoft-edge-dev"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tried []string
			deps := browserDebugDeps{
				goos: tt.goos,
				getenv: func(key string) string {
					return tt.env[key]
				},
				isFile: func(path string) bool {
					tried = append(tried, path)
					return path == tt.foundPath
				},
				lookPath: func(name string) (string, error) {
					tried = append(tried, name)
					if name == lastPathComponent(tt.foundPath) ||
						(tt.goos == "linux" && len(tried) == len(tt.wantTried)-1) {
						return tt.foundPath, nil
					}
					return "", errors.New("not found")
				},
			}

			got, err := discoverBrowserExecutable(browserDebugConfig{Browser: tt.browser}, deps)
			if err != nil {
				t.Fatalf("discoverBrowserExecutable: %v", err)
			}
			if got != tt.foundPath {
				t.Fatalf("executable = %q, want %q", got, tt.foundPath)
			}
			if !reflect.DeepEqual(tried, tt.wantTried[:len(tried)]) {
				t.Fatalf("search order = %#v, want prefix %#v", tried, tt.wantTried)
			}
			if tried[len(tried)-1] != tt.wantTried[len(tried)-1] {
				t.Fatalf("last candidate = %q, want %q", tried[len(tried)-1], tt.wantTried[len(tried)-1])
			}
		})
	}
}

func TestDiscoverBrowserExecutableValidatesExplicitOverride(t *testing.T) {
	var checked []string
	deps := browserDebugDeps{
		isFile: func(path string) bool {
			checked = append(checked, path)
			return path == `/opt/browser/chrome`
		},
		lookPath: func(string) (string, error) {
			t.Fatal("PATH discovery must not run for an explicit executable")
			return "", nil
		},
	}

	got, err := discoverBrowserExecutable(browserDebugConfig{
		Browser:        "chrome",
		ExecutablePath: `/opt/browser/chrome`,
	}, deps)
	if err != nil || got != `/opt/browser/chrome` {
		t.Fatalf("explicit executable = %q, %v", got, err)
	}
	if !reflect.DeepEqual(checked, []string{`/opt/browser/chrome`}) {
		t.Fatalf("checked paths = %#v", checked)
	}

	_, err = discoverBrowserExecutable(browserDebugConfig{
		Browser:        "chrome",
		ExecutablePath: `/missing/chrome`,
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "executable") {
		t.Fatalf("missing explicit executable error = %v", err)
	}
}

func TestValidateBrowserDebugConfigLaunchAndAttach(t *testing.T) {
	tests := []struct {
		name    string
		cfg     browserDebugConfig
		wantErr string
	}{
		{name: "browser type", cfg: browserDebugConfig{Browser: "firefox", Request: "launch", URL: "https://example.test"}, wantErr: "browser"},
		{name: "request", cfg: browserDebugConfig{Browser: "chrome", Request: "restart", URL: "https://example.test"}, wantErr: "request"},
		{name: "launch URL", cfg: browserDebugConfig{Browser: "chrome", Request: "launch"}, wantErr: "URL"},
		{name: "launch URL scheme", cfg: browserDebugConfig{Browser: "chrome", Request: "launch", URL: "javascript:alert(1)"}, wantErr: "scheme"},
		{name: "launch remote debugger", cfg: browserDebugConfig{Browser: "chrome", Request: "launch", URL: "https://example.test", Address: "192.0.2.1:9222"}, wantErr: "loopback"},
		{name: "attach address", cfg: browserDebugConfig{Browser: "edge", Request: "attach"}, wantErr: "address"},
		{name: "attach bad port", cfg: browserDebugConfig{Browser: "edge", Request: "attach", Address: "localhost:0"}, wantErr: "range"},
		{name: "attach remote", cfg: browserDebugConfig{Browser: "edge", Request: "attach", Address: "example.com:9222"}, wantErr: "loopback"},
		{name: "attach URL not required", cfg: browserDebugConfig{Browser: "pwa-msedge", Request: "attach", Address: "localhost:9222"}},
		{name: "launch aliases", cfg: browserDebugConfig{Browser: "pwa-chrome", Request: "launch", URL: "http://localhost:3000"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := validateBrowserDebugConfig(tt.cfg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wantErr)) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateBrowserDebugConfig: %v", err)
			}
			if tt.cfg.Browser == "pwa-msedge" && spec.Browser != "edge" {
				t.Fatalf("browser = %q, want edge", spec.Browser)
			}
			if tt.cfg.Browser == "pwa-chrome" && spec.Browser != "chrome" {
				t.Fatalf("browser = %q, want chrome", spec.Browser)
			}
		})
	}
}

func TestValidateBrowserDebugConfigRejectsSecurityFlagOverrides(t *testing.T) {
	for _, arg := range []string{
		"--remote-debugging-address=0.0.0.0",
		"--remote-debugging-port=9229",
		"--remote-debugging-pipe",
		"--user-data-dir=/shared/profile",
	} {
		t.Run(arg, func(t *testing.T) {
			_, err := validateBrowserDebugConfig(browserDebugConfig{
				Browser:     "chrome",
				Request:     "launch",
				URL:         "http://localhost:3000",
				RuntimeArgs: []string{arg},
			})
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("argument %q error = %v", arg, err)
			}
		})
	}
}

func TestBuildBrowserArgvUsesSeparateTokensAndLoopback(t *testing.T) {
	spec, err := validateBrowserDebugConfig(browserDebugConfig{
		Browser:     "edge",
		Request:     "launch",
		URL:         "http://localhost:3000/path with spaces?q=a&b=c",
		Address:     "localhost:9333",
		RuntimeArgs: []string{"--incognito", "--lang=en-US"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := buildBrowserArgv(spec, `C:\Temp\profile with spaces`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=9333",
		`--user-data-dir=C:\Temp\profile with spaces`,
		"--no-first-run",
		"--no-default-browser-check",
		"--incognito",
		"--lang=en-US",
		"http://localhost:3000/path with spaces?q=a&b=c",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

type fakeBrowserProcess struct {
	release      chan struct{}
	releaseOnce  sync.Once
	waitErr      error
	killErr      error
	killReleases bool
	waitCalls    atomic.Int32
	killCalls    atomic.Int32
}

func newFakeBrowserProcess(killReleases bool) *fakeBrowserProcess {
	return &fakeBrowserProcess{release: make(chan struct{}), killReleases: killReleases}
}

func (p *fakeBrowserProcess) Wait() error {
	p.waitCalls.Add(1)
	<-p.release
	return p.waitErr
}

func (p *fakeBrowserProcess) Kill() error {
	p.killCalls.Add(1)
	if p.killReleases {
		p.releaseProcess()
	}
	return p.killErr
}

func (p *fakeBrowserProcess) releaseProcess() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func testBrowserLaunchConfig() browserDebugConfig {
	return browserDebugConfig{
		Browser:        "chrome",
		Request:        "launch",
		ExecutablePath: `C:\Browser\chrome.exe`,
		URL:            "http://localhost:3000",
	}
}

func TestLaunchBrowserDebugCleansProfileAfterStartFailure(t *testing.T) {
	var chmodMode os.FileMode
	var removed []string
	deps := browserDebugDeps{
		isFile:       func(string) bool { return true },
		mkdirTemp:    func(string, string) (string, error) { return `C:\Temp\profile`, nil },
		chmod:        func(_ string, mode os.FileMode) error { chmodMode = mode; return nil },
		removeAll:    func(path string) error { removed = append(removed, path); return nil },
		allocatePort: func() (int, error) { return 9222, nil },
		startProcess: func(string, []string) (browserProcess, error) { return nil, errors.New("start failed") },
	}

	launch, _, err := launchBrowserDebug(testBrowserLaunchConfig(), deps)
	if err == nil || !strings.Contains(err.Error(), "start browser") {
		t.Fatalf("launch error = %v", err)
	}
	if launch != nil {
		t.Fatal("launch must be nil after process start failure")
	}
	if chmodMode != 0o700 {
		t.Fatalf("profile chmod = %#o, want 0700", chmodMode)
	}
	if !reflect.DeepEqual(removed, []string{`C:\Temp\profile`}) {
		t.Fatalf("removed profiles = %#v", removed)
	}
}

func TestLaunchBrowserDebugNaturalExitHasSingleWaitOwnerAndCleanup(t *testing.T) {
	process := newFakeBrowserProcess(false)
	var removeCalls atomic.Int32
	deps := browserDebugDeps{
		isFile:       func(string) bool { return true },
		mkdirTemp:    func(string, string) (string, error) { return "/tmp/browser-profile", nil },
		chmod:        func(string, os.FileMode) error { return nil },
		removeAll:    func(string) error { removeCalls.Add(1); return nil },
		allocatePort: func() (int, error) { return 9222, nil },
		startProcess: func(_ string, argv []string) (browserProcess, error) {
			if !containsArg(argv, "--user-data-dir=/tmp/browser-profile") {
				t.Fatalf("argv missing profile: %#v", argv)
			}
			return process, nil
		},
	}

	launch, endpoint, err := launchBrowserDebug(testBrowserLaunchConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "127.0.0.1:9222" {
		t.Fatalf("endpoint = %q", endpoint)
	}
	process.releaseProcess()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := launch.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := launch.Stop(ctx); err != nil {
		t.Fatalf("Stop after natural exit: %v", err)
	}
	if got := process.waitCalls.Load(); got != 1 {
		t.Fatalf("Wait calls = %d, want 1", got)
	}
	if got := process.killCalls.Load(); got != 0 {
		t.Fatalf("Kill calls = %d, want 0", got)
	}
	if got := removeCalls.Load(); got != 1 {
		t.Fatalf("profile cleanup calls = %d, want 1", got)
	}
}

func TestBrowserLaunchStopIsBoundedAndCleanupWaitsForExit(t *testing.T) {
	process := newFakeBrowserProcess(false)
	var removeCalls atomic.Int32
	deps := browserDebugDeps{
		isFile:       func(string) bool { return true },
		mkdirTemp:    func(string, string) (string, error) { return "/tmp/browser-profile", nil },
		chmod:        func(string, os.FileMode) error { return nil },
		removeAll:    func(string) error { removeCalls.Add(1); return nil },
		allocatePort: func() (int, error) { return 9222, nil },
		startProcess: func(string, []string) (browserProcess, error) { return process, nil },
	}
	launch, _, err := launchBrowserDebug(testBrowserLaunchConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = launch.Stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("Stop was not bounded: %v", elapsed)
	}
	if got := removeCalls.Load(); got != 0 {
		t.Fatalf("profile cleaned before process exit: %d", got)
	}
	if got := process.waitCalls.Load(); got != 1 {
		t.Fatalf("Wait calls = %d, want 1", got)
	}

	process.releaseProcess()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := launch.Wait(waitCtx); err != nil {
		t.Fatalf("Wait after release: %v", err)
	}
	if got := removeCalls.Load(); got != 1 {
		t.Fatalf("profile cleanup calls = %d, want 1", got)
	}
}

func TestBrowserLaunchStopKillsOnceAndCleansOnce(t *testing.T) {
	process := newFakeBrowserProcess(true)
	var removeCalls atomic.Int32
	deps := browserDebugDeps{
		isFile:       func(string) bool { return true },
		mkdirTemp:    func(string, string) (string, error) { return "/tmp/browser-profile", nil },
		chmod:        func(string, os.FileMode) error { return nil },
		removeAll:    func(string) error { removeCalls.Add(1); return nil },
		allocatePort: func() (int, error) { return 9222, nil },
		startProcess: func(string, []string) (browserProcess, error) { return process, nil },
	}
	launch, _, err := launchBrowserDebug(testBrowserLaunchConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := launch.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := launch.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if got := process.killCalls.Load(); got != 1 {
		t.Fatalf("Kill calls = %d, want 1", got)
	}
	if got := process.waitCalls.Load(); got != 1 {
		t.Fatalf("Wait calls = %d, want 1", got)
	}
	if got := removeCalls.Load(); got != 1 {
		t.Fatalf("cleanup calls = %d, want 1", got)
	}
}

func TestEnumerateBrowserTargetsFiltersPagesAndSelectsStableTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"worker","type":"service_worker","title":"worker","url":"http://localhost/sw.js","webSocketDebuggerUrl":"/devtools/worker/worker"},
			{"id":"page-a","type":"page","title":"A","url":"http://localhost/a","webSocketDebuggerUrl":"/devtools/page/a"},
			{"id":"page-b","type":"page","title":"B","url":"http://localhost/b","webSocketDebuggerUrl":"/devtools/page/b"}
		]`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	targets, err := enumerateBrowserTargets(context.Background(), parsed.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetIDs(targets); !reflect.DeepEqual(got, []string{"page-a", "page-b"}) {
		t.Fatalf("page targets = %#v", got)
	}
	if !strings.HasPrefix(targets[0].WebSocketDebuggerURL, "ws://"+parsed.Host+"/") {
		t.Fatalf("normalized websocket URL = %q", targets[0].WebSocketDebuggerURL)
	}

	selected, err := selectBrowserTarget(targets, "page-b")
	if err != nil || selected.ID != "page-b" {
		t.Fatalf("explicit selection = %+v, %v", selected, err)
	}
	selected, err = selectBrowserTarget(targets, "")
	if err != nil || selected.ID != "page-a" {
		t.Fatalf("default selection = %+v, %v", selected, err)
	}
	if _, err := selectBrowserTarget(targets, "missing"); err == nil {
		t.Fatal("missing explicit target must fail rather than select another tab")
	}
}

func TestEnumerateBrowserTargetsRejectsRemoteEndpoints(t *testing.T) {
	if _, err := enumerateBrowserTargets(context.Background(), "192.0.2.1:9222", 20*time.Millisecond); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("remote inspector error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"page","type":"page","webSocketDebuggerUrl":"ws://192.0.2.2:9222/devtools/page/1"}]`))
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	if _, err := enumerateBrowserTargets(context.Background(), parsed.Host, time.Second); err == nil || !strings.Contains(err.Error(), "websocket") {
		t.Fatalf("remote websocket error = %v", err)
	}
}

func TestEnumerateBrowserTargetsBoundsResponseSizeCountAndTime(t *testing.T) {
	t.Run("bytes", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", maxBrowserTargetResponseSize+1)))
		}))
		defer server.Close()
		parsed, _ := url.Parse(server.URL)
		_, err := enumerateBrowserTargets(context.Background(), parsed.Host, time.Second)
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("size error = %v", err)
		}
	})

	t.Run("count", func(t *testing.T) {
		entries := make([]browserTarget, maxBrowserCDPTargets+1)
		for i := range entries {
			entries[i] = browserTarget{ID: fmt.Sprintf("target-%d", i), Type: "page", WebSocketDebuggerURL: "/devtools/page/x"}
		}
		body, err := json.Marshal(entries)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(body)
		}))
		defer server.Close()
		parsed, _ := url.Parse(server.URL)
		_, err = enumerateBrowserTargets(context.Background(), parsed.Host, time.Second)
		if err == nil || !strings.Contains(err.Error(), "too many") {
			t.Fatalf("count error = %v", err)
		}
	})

	t.Run("time", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
				_, _ = w.Write([]byte("[]"))
			}
		}))
		defer server.Close()
		parsed, _ := url.Parse(server.URL)
		start := time.Now()
		_, err := enumerateBrowserTargets(context.Background(), parsed.Host, 30*time.Millisecond)
		if err == nil {
			t.Fatal("target enumeration must time out")
		}
		if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
			t.Fatalf("target enumeration ignored timeout: %v", elapsed)
		}
	})
}

func TestEnumerateBrowserTargetsRejectsRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		_, _ = w.Write([]byte("[]"))
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)

	if _, err := enumerateBrowserTargets(context.Background(), parsed.Host, time.Second); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect error = %v", err)
	}
	if got := redirected.Load(); got != 0 {
		t.Fatalf("redirect target requests = %d, want 0", got)
	}
}

func containsArg(argv []string, want string) bool {
	for _, arg := range argv {
		if arg == want {
			return true
		}
	}
	return false
}

func targetIDs(targets []browserTarget) []string {
	ids := make([]string, len(targets))
	for i := range targets {
		ids[i] = targets[i].ID
	}
	return ids
}

func lastPathComponent(path string) string {
	path = strings.TrimRight(path, `/\`)
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}
