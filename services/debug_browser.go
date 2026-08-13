package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	maxBrowserTargetResponseSize = 1024 * 1024
	maxBrowserCDPTargets         = 256
	maxBrowserTargetTimeout      = 10 * time.Second
)

// browserDebugConfig is deliberately independent from DebugLaunchConfig.
// DebugService integration can translate persisted launch.json fields without
// coupling browser process ownership to configuration parsing.
type browserDebugConfig struct {
	Browser        string
	Request        string
	ExecutablePath string
	URL            string
	Address        string
	RuntimeArgs    []string
	TargetID       string
	WebRoot        string
	SourceMaps     bool
	PathMappings   map[string]string
}

type browserDebugSpec struct {
	Browser        string
	Request        string
	ExecutablePath string
	URL            string
	Address        string
	RuntimeArgs    []string
	TargetID       string
	WebRoot        string
	SourceMaps     bool
	PathMappings   map[string]string
}

type browserProcess interface {
	Wait() error
	Kill() error
}

type browserDebugDeps struct {
	goos         string
	getenv       func(string) string
	lookPath     func(string) (string, error)
	isFile       func(string) bool
	mkdirTemp    func(string, string) (string, error)
	chmod        func(string, os.FileMode) error
	removeAll    func(string) error
	allocatePort func() (int, error)
	startProcess func(string, []string) (browserProcess, error)
}

func (d browserDebugDeps) withDefaults() browserDebugDeps {
	if d.goos == "" {
		d.goos = runtime.GOOS
	}
	if d.getenv == nil {
		d.getenv = os.Getenv
	}
	if d.lookPath == nil {
		d.lookPath = exec.LookPath
	}
	if d.isFile == nil {
		d.isFile = func(name string) bool {
			info, err := os.Stat(name)
			return err == nil && info.Mode().IsRegular()
		}
	}
	if d.mkdirTemp == nil {
		d.mkdirTemp = os.MkdirTemp
	}
	if d.chmod == nil {
		d.chmod = os.Chmod
	}
	if d.removeAll == nil {
		d.removeAll = os.RemoveAll
	}
	if d.allocatePort == nil {
		d.allocatePort = allocateBrowserDebugPort
	}
	if d.startProcess == nil {
		d.startProcess = startBrowserProcess
	}
	return d
}

func normalizeBrowserName(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "chrome", "pwa-chrome":
		return "chrome", nil
	case "edge", "msedge", "pwa-msedge":
		return "edge", nil
	default:
		return "", fmt.Errorf("browser must be chrome or edge")
	}
}

func validateBrowserDebugConfig(cfg browserDebugConfig) (browserDebugSpec, error) {
	browser, err := normalizeBrowserName(cfg.Browser)
	if err != nil {
		return browserDebugSpec{}, err
	}
	request := strings.ToLower(strings.TrimSpace(cfg.Request))
	if request != "launch" && request != "attach" {
		return browserDebugSpec{}, fmt.Errorf("browser request must be launch or attach")
	}

	address := strings.TrimSpace(cfg.Address)
	if request == "attach" && address == "" {
		return browserDebugSpec{}, fmt.Errorf("browser attach requires a debugger address")
	}
	if address != "" {
		address, err = validateNodeCDPHostPort(address)
		if err != nil {
			return browserDebugSpec{}, fmt.Errorf("invalid browser debugger address: %w", err)
		}
	}

	rawURL := strings.TrimSpace(cfg.URL)
	if request == "launch" {
		if rawURL == "" {
			return browserDebugSpec{}, fmt.Errorf("browser launch requires a URL")
		}
		parsed, parseErr := url.Parse(rawURL)
		if parseErr != nil || parsed.Scheme == "" || parsed.User != nil {
			return browserDebugSpec{}, fmt.Errorf("invalid browser launch URL")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "file":
		case "about":
			if !strings.EqualFold(rawURL, "about:blank") {
				return browserDebugSpec{}, fmt.Errorf("browser launch URL scheme is not allowed")
			}
		default:
			return browserDebugSpec{}, fmt.Errorf("browser launch URL scheme is not allowed")
		}
	}

	runtimeArgs := append([]string(nil), cfg.RuntimeArgs...)
	reserved := map[string]bool{
		"--remote-debugging-address": true,
		"--remote-debugging-port":    true,
		"--remote-debugging-pipe":    true,
		"--user-data-dir":            true,
	}
	for _, arg := range runtimeArgs {
		flag := strings.ToLower(strings.TrimSpace(arg))
		if i := strings.IndexByte(flag, '='); i >= 0 {
			flag = flag[:i]
		}
		if reserved[flag] {
			return browserDebugSpec{}, fmt.Errorf("browser runtime argument %q overrides a reserved security flag", arg)
		}
	}

	pathMappings := make(map[string]string, len(cfg.PathMappings))
	for from, to := range cfg.PathMappings {
		pathMappings[from] = to
	}
	return browserDebugSpec{
		Browser:        browser,
		Request:        request,
		ExecutablePath: strings.TrimSpace(cfg.ExecutablePath),
		URL:            rawURL,
		Address:        address,
		RuntimeArgs:    runtimeArgs,
		TargetID:       cfg.TargetID,
		WebRoot:        cfg.WebRoot,
		SourceMaps:     cfg.SourceMaps,
		PathMappings:   pathMappings,
	}, nil
}

func discoverBrowserExecutable(cfg browserDebugConfig, deps browserDebugDeps) (string, error) {
	deps = deps.withDefaults()
	browser, err := normalizeBrowserName(cfg.Browser)
	if err != nil {
		return "", err
	}
	if explicit := strings.TrimSpace(cfg.ExecutablePath); explicit != "" {
		if !deps.isFile(explicit) {
			return "", fmt.Errorf("browser executable is not a regular file: %s", explicit)
		}
		return explicit, nil
	}

	switch deps.goos {
	case "windows":
		for _, candidate := range windowsBrowserCandidates(browser, deps.getenv) {
			if deps.isFile(candidate) {
				return candidate, nil
			}
		}
	case "darwin":
		for _, candidate := range darwinBrowserCandidates(browser, deps.getenv("HOME")) {
			if deps.isFile(candidate) {
				return candidate, nil
			}
		}
	default:
		for _, name := range linuxBrowserCandidates(browser) {
			if executable, lookErr := deps.lookPath(name); lookErr == nil && executable != "" {
				return executable, nil
			}
		}
	}
	return "", fmt.Errorf("%s browser executable not found", browser)
}

func windowsBrowserCandidates(browser string, getenv func(string) string) []string {
	type rootSuffix struct {
		root   string
		suffix string
	}
	var entries []rootSuffix
	if browser == "edge" {
		entries = []rootSuffix{
			{getenv("PROGRAMFILES(X86)"), `Microsoft\Edge\Application\msedge.exe`},
			{getenv("PROGRAMFILES"), `Microsoft\Edge\Application\msedge.exe`},
			{getenv("LOCALAPPDATA"), `Microsoft\Edge\Application\msedge.exe`},
		}
	} else {
		entries = []rootSuffix{
			{getenv("PROGRAMFILES"), `Google\Chrome\Application\chrome.exe`},
			{getenv("PROGRAMFILES(X86)"), `Google\Chrome\Application\chrome.exe`},
			{getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`},
		}
	}
	candidates := make([]string, 0, len(entries))
	for _, entry := range entries {
		root := strings.TrimRight(entry.root, `/\`)
		if root != "" {
			candidates = append(candidates, root+`\`+entry.suffix)
		}
	}
	return candidates
}

func darwinBrowserCandidates(browser, home string) []string {
	appPath := "Google Chrome.app/Contents/MacOS/Google Chrome"
	if browser == "edge" {
		appPath = "Microsoft Edge.app/Contents/MacOS/Microsoft Edge"
	}
	candidates := []string{path.Join("/Applications", appPath)}
	if home = strings.TrimSpace(home); home != "" {
		candidates = append(candidates, path.Join(home, "Applications", appPath))
	}
	return candidates
}

func linuxBrowserCandidates(browser string) []string {
	if browser == "edge" {
		return []string{"microsoft-edge-stable", "microsoft-edge", "microsoft-edge-beta", "microsoft-edge-dev"}
	}
	return []string{"google-chrome-stable", "google-chrome", "chromium", "chromium-browser"}
}

func allocateBrowserDebugPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcpAddr.Port < 1 || tcpAddr.Port > 65535 {
		return 0, fmt.Errorf("invalid allocated browser debugger port")
	}
	return tcpAddr.Port, nil
}

func buildBrowserArgv(spec browserDebugSpec, profileDir string) ([]string, error) {
	if spec.Request != "launch" {
		return nil, fmt.Errorf("browser argv is only valid for launch requests")
	}
	if strings.TrimSpace(profileDir) == "" {
		return nil, fmt.Errorf("browser profile directory is required")
	}
	address, err := validateNodeCDPHostPort(spec.Address)
	if err != nil {
		return nil, fmt.Errorf("invalid browser debugger address: %w", err)
	}
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	argv := []string{
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=" + portText,
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
	}
	argv = append(argv, spec.RuntimeArgs...)
	argv = append(argv, spec.URL)
	return argv, nil
}

func launchBrowserDebug(cfg browserDebugConfig, deps browserDebugDeps) (*browserLaunch, string, error) {
	deps = deps.withDefaults()
	spec, err := validateBrowserDebugConfig(cfg)
	if err != nil {
		return nil, "", err
	}
	if spec.Request != "launch" {
		return nil, "", fmt.Errorf("browser attach does not launch a process")
	}
	executable, err := discoverBrowserExecutable(cfg, deps)
	if err != nil {
		return nil, "", err
	}
	if spec.Address == "" {
		port, portErr := deps.allocatePort()
		if portErr != nil {
			return nil, "", fmt.Errorf("allocate browser debugger port: %w", portErr)
		}
		if port < 1 || port > 65535 {
			return nil, "", fmt.Errorf("allocated browser debugger port is out of range")
		}
		spec.Address = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}

	profileDir, err := deps.mkdirTemp("", "koyori-ide-browser-*")
	if err != nil {
		return nil, "", fmt.Errorf("create browser profile: %w", err)
	}
	cleanupFailure := func(cause error) (*browserLaunch, string, error) {
		cleanupErr := deps.removeAll(profileDir)
		if cleanupErr != nil {
			cause = errors.Join(cause, fmt.Errorf("clean browser profile: %w", cleanupErr))
		}
		return nil, "", cause
	}
	if err := deps.chmod(profileDir, 0o700); err != nil {
		return cleanupFailure(fmt.Errorf("secure browser profile: %w", err))
	}
	argv, err := buildBrowserArgv(spec, profileDir)
	if err != nil {
		return cleanupFailure(err)
	}
	process, err := deps.startProcess(executable, argv)
	if err != nil {
		return cleanupFailure(fmt.Errorf("start browser: %w", err))
	}
	if process == nil {
		return cleanupFailure(fmt.Errorf("start browser: no process returned"))
	}
	return newBrowserLaunch(process, profileDir, deps.removeAll), spec.Address, nil
}

type execBrowserProcess struct {
	cmd *exec.Cmd
}

func (p *execBrowserProcess) Wait() error {
	return p.cmd.Wait()
}

func (p *execBrowserProcess) Kill() error {
	if p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Kill()
}

func startBrowserProcess(executable string, argv []string) (browserProcess, error) {
	cmd := command(executable, argv...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execBrowserProcess{cmd: cmd}, nil
}

// browserLaunch has exactly one Wait owner. Stop never calls Wait; it requests
// termination and waits for the owner to publish process exit and cleanup.
type browserLaunch struct {
	process    browserProcess
	profileDir string
	removeAll  func(string) error
	done       chan struct{}
	stopOnce   sync.Once

	mu         sync.Mutex
	waitErr    error
	cleanupErr error
	killErr    error
}

func newBrowserLaunch(process browserProcess, profileDir string, removeAll func(string) error) *browserLaunch {
	launch := &browserLaunch{
		process:    process,
		profileDir: profileDir,
		removeAll:  removeAll,
		done:       make(chan struct{}),
	}
	go func() {
		waitErr := process.Wait()
		cleanupErr := removeAll(profileDir)
		launch.mu.Lock()
		launch.waitErr = waitErr
		if cleanupErr != nil {
			launch.cleanupErr = fmt.Errorf("clean browser profile: %w", cleanupErr)
		}
		launch.mu.Unlock()
		close(launch.done)
	}()
	return launch
}

func (l *browserLaunch) Done() <-chan struct{} {
	return l.done
}

func (l *browserLaunch) Wait(ctx context.Context) error {
	select {
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		return errors.Join(l.waitErr, l.cleanupErr)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *browserLaunch) Stop(ctx context.Context) error {
	select {
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.cleanupErr
	default:
	}

	l.stopOnce.Do(func() {
		err := l.process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			err = nil
		}
		l.mu.Lock()
		l.killErr = err
		l.mu.Unlock()
	})
	select {
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		return errors.Join(l.killErr, l.cleanupErr)
	case <-ctx.Done():
		l.mu.Lock()
		killErr := l.killErr
		l.mu.Unlock()
		return errors.Join(killErr, ctx.Err())
	}
}

type BrowserTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// browserTarget is retained as a package-local alias for the low-level helper
// tests; BrowserTarget is the Wails-facing DTO used by DebugStateSnapshot.
type browserTarget = BrowserTarget

type BrowserConsoleEntry struct {
	Generation uint64  `json:"generation"`
	Level      string  `json:"level"`
	Text       string  `json:"text"`
	URL        string  `json:"url,omitempty"`
	Line       int     `json:"line,omitempty"`
	Timestamp  float64 `json:"timestamp,omitempty"`
}

type BrowserNetworkEntry struct {
	Generation uint64  `json:"generation"`
	RequestID  string  `json:"requestId"`
	Phase      string  `json:"phase"`
	Method     string  `json:"method,omitempty"`
	URL        string  `json:"url,omitempty"`
	Status     int     `json:"status,omitempty"`
	MIMEType   string  `json:"mimeType,omitempty"`
	Error      string  `json:"error,omitempty"`
	Timestamp  float64 `json:"timestamp,omitempty"`
}

func enumerateBrowserTargets(ctx context.Context, address string, timeout time.Duration) ([]BrowserTarget, error) {
	validatedAddress, err := validateNodeCDPHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid browser debugger address: %w", err)
	}
	if timeout <= 0 || timeout > maxBrowserTargetTimeout {
		timeout = maxBrowserTargetTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("browser target redirects are not allowed")
		},
	}
	endpoint := (&url.URL{Scheme: "http", Host: validatedAddress, Path: "/json/list"}).String()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enumerate browser targets: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBrowserTargetResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read browser targets: %w", err)
	}
	if len(body) > maxBrowserTargetResponseSize {
		return nil, fmt.Errorf("browser target response too large: exceeds %d bytes", maxBrowserTargetResponseSize)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("browser target endpoint returned HTTP %d", resp.StatusCode)
	}
	var rawTargets []BrowserTarget
	if err := json.Unmarshal(body, &rawTargets); err != nil {
		return nil, fmt.Errorf("parse browser targets: %w", err)
	}
	if len(rawTargets) > maxBrowserCDPTargets {
		return nil, fmt.Errorf("too many browser targets: %d exceeds %d", len(rawTargets), maxBrowserCDPTargets)
	}

	pageTargets := make([]BrowserTarget, 0, len(rawTargets))
	seenIDs := make(map[string]struct{}, len(rawTargets))
	for _, target := range rawTargets {
		if !strings.EqualFold(target.Type, "page") || target.ID == "" || target.WebSocketDebuggerURL == "" {
			continue
		}
		if _, duplicate := seenIDs[target.ID]; duplicate {
			return nil, fmt.Errorf("duplicate browser target id %q", target.ID)
		}
		wsURL, err := normalizeNodeCDPWebSocketURL(target.WebSocketDebuggerURL, validatedAddress)
		if err != nil {
			return nil, fmt.Errorf("invalid browser target websocket: %w", err)
		}
		target.WebSocketDebuggerURL = wsURL
		seenIDs[target.ID] = struct{}{}
		pageTargets = append(pageTargets, target)
	}
	return pageTargets, nil
}

func selectBrowserTarget(targets []BrowserTarget, targetID string) (BrowserTarget, error) {
	if len(targets) == 0 {
		return BrowserTarget{}, fmt.Errorf("no browser page targets available")
	}
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return targets[0], nil
	}
	for _, target := range targets {
		if target.ID == targetID {
			return target, nil
		}
	}
	return BrowserTarget{}, fmt.Errorf("browser target %q not found", targetID)
}

func waitForBrowserTargets(ctx context.Context, address string, timeout time.Duration) ([]BrowserTarget, error) {
	if timeout <= 0 || timeout > maxBrowserTargetTimeout {
		timeout = maxBrowserTargetTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for {
		targets, err := enumerateBrowserTargets(waitCtx, address, timeout)
		if err == nil && len(targets) > 0 {
			return targets, nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-waitCtx.Done():
			if lastErr != nil {
				return nil, fmt.Errorf("browser targets not ready: %w", lastErr)
			}
			return nil, fmt.Errorf("browser targets not ready: %w", waitCtx.Err())
		case <-time.After(80 * time.Millisecond):
		}
	}
}

func connectBrowserTarget(target BrowserTarget, timeout time.Duration) (*nodeCDPClient, error) {
	rawURL := strings.TrimSpace(target.WebSocketDebuggerURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid browser target websocket URL")
	}
	validatedURL, err := normalizeNodeCDPWebSocketURL(rawURL, parsed.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid browser target websocket: %w", err)
	}
	if timeout <= 0 || timeout > maxBrowserTargetTimeout {
		timeout = maxBrowserTargetTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, validatedURL, &websocket.DialOptions{
		HTTPClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return fmt.Errorf("browser websocket redirects are not allowed")
			},
		},
	})
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("browser CDP dial: %w", err)
	}
	client := &nodeCDPClient{conn: conn, pending: make(map[int64]chan cdpResponse)}
	go client.readLoop()
	for _, method := range []string{"Debugger.enable", "Runtime.enable"} {
		if err := client.call(method, map[string]interface{}{}); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("browser CDP %s: %w", method, err)
		}
	}
	_ = client.call("Page.enable", map[string]interface{}{})
	_ = client.call("Log.enable", map[string]interface{}{})
	_ = client.call("Network.enable", map[string]interface{}{})
	if err := client.call("Debugger.setAsyncCallStackDepth", map[string]interface{}{"maxDepth": 32}); err == nil {
		client.mu.Lock()
		client.asyncStackTraceSupported = true
		client.mu.Unlock()
	}
	return client, nil
}

func browserSourceURLToLocal(rawURL string, spec browserDebugSpec) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" {
		return rawURL
	}
	sourcePath := path.Clean("/" + strings.TrimPrefix(parsed.Path, "/"))
	for _, remoteRoot := range sortedMappingKeys(spec.PathMappings) {
		cleanRemote := path.Clean("/" + strings.TrimPrefix(remoteRoot, "/"))
		if sourcePath != cleanRemote && !strings.HasPrefix(sourcePath, strings.TrimSuffix(cleanRemote, "/")+"/") {
			continue
		}
		rel := strings.TrimPrefix(sourcePath, cleanRemote)
		return filepath.Join(spec.PathMappings[remoteRoot], filepath.FromSlash(strings.TrimPrefix(rel, "/")))
	}
	if strings.TrimSpace(spec.WebRoot) != "" {
		return filepath.Join(spec.WebRoot, filepath.FromSlash(strings.TrimPrefix(sourcePath, "/")))
	}
	return rawURL
}

func browserLocalPathToURL(file string, spec browserDebugSpec) string {
	for _, remoteRoot := range sortedMappingKeys(spec.PathMappings) {
		if rel, ok := pathWithinRoot(file, spec.PathMappings[remoteRoot]); ok {
			return browserURLForPath(spec.URL, path.Join(remoteRoot, filepath.ToSlash(rel)))
		}
	}
	if rel, ok := pathWithinRoot(file, spec.WebRoot); ok {
		return browserURLForPath(spec.URL, "/"+filepath.ToSlash(rel))
	}
	return fileToFileURL(file)
}

func sortedMappingKeys(mappings map[string]string) []string {
	keys := make([]string, 0, len(mappings))
	for key := range mappings {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) == len(keys[j]) {
			return keys[i] < keys[j]
		}
		return len(keys[i]) > len(keys[j])
	})
	return keys
}

func pathWithinRoot(file, root string) (string, bool) {
	if strings.TrimSpace(root) == "" {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(file))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

func browserURLForPath(launchURL, sourcePath string) string {
	base, err := url.Parse(strings.TrimSpace(launchURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return sourcePath
	}
	return (&url.URL{Scheme: base.Scheme, Host: base.Host, Path: path.Clean("/" + strings.TrimPrefix(sourcePath, "/"))}).String()
}

func cloneBrowserStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	copyMap := make(map[string]string, len(input))
	for key, value := range input {
		copyMap[key] = value
	}
	return copyMap
}
