// Package main 是 Koyori IDE 的入口点。
//
// Koyori IDE（こより IDE）是一个离线优先的桌面 AI 代码编辑器，
// 面向 Go / TypeScript / JavaScript 开发者。整个应用构建在
// Wails v3（alpha）之上：Go 后端负责能力与安全边界，Vue 3 +
// Monaco Editor 前端负责界面与交互。
//
// 本文件只负责进程级生命周期：
//  1. 单实例锁（防止重复启动）
//  2. 配置目录与设置加载
//  3. 服务装配（见 bootstrap_services.go）
//  4. 主窗口 / AI 伴侣窗口创建
//  5. 信号处理与优雅退出
//
// 所有事件类型（ai:chunk、terminal:output、file:saved 等）也在
// init() 中集中注册，保证前后端通过事件总线通信时的类型安全。
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/e2e"
	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed VERSION
var embeddedVersion string

type cleanupStack []func()

func (c *cleanupStack) add(cleanup func()) {
	*c = append(*c, cleanup)
}

func (c *cleanupStack) run() {
	for _, cleanup := range *c {
		defer cleanup()
	}
}

func emitTimeEvents(ctx context.Context, ticks <-chan time.Time, emit func(string)) {
	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-ticks:
			emit(tick.Format(time.RFC1123))
		}
	}
}

func stopOnSignal(ctx context.Context, signals <-chan os.Signal, stop func()) {
	select {
	case <-ctx.Done():
		return
	case <-signals:
		stop()
	}
}

func runAndReport(run func() error, report func(error)) {
	if err := run(); err != nil {
		report(err)
	}
}

func loadStartupSettings(
	load func() (services.Settings, error),
	warn func(string, ...any),
) (services.Settings, bool) {
	settings, err := load()
	if err != nil {
		warn("load startup settings failed", "err", err)
		return settings, false
	}
	return settings, true
}

func loadInstalledExtensionManifests(
	load func() ([]services.VSCodeExtensionManifest, error),
	warn func(string, ...any),
) ([]services.VSCodeExtensionManifest, bool) {
	manifests, err := load()
	if err != nil {
		warn("load installed extension manifests failed", "err", err)
		return nil, false
	}
	return manifests, true
}

func init() {
	application.RegisterEvent[string]("time")
	application.RegisterEvent[map[string]string]("terminal:output")
	// prompt-6 Task 2: AI stream events carry {streamId, data|busy|...}.
	application.RegisterEvent[map[string]interface{}]("ai:chunk")
	application.RegisterEvent[map[string]interface{}]("ai:done")
	application.RegisterEvent[map[string]interface{}]("ai:error")
	// Provider-declared reasoning summaries are delivered through the same
	// caller-window transport as chunks and tool calls. Hidden chain-of-thought
	// is never inferred or emitted; this channel carries explicit summaries only.
	application.RegisterEvent[map[string]interface{}]("ai:reasoning")
	// The process-wide busy event intentionally carries only {busy}; owner
	// stream IDs stay on per-window sensitive events.
	application.RegisterEvent[map[string]interface{}]("ai:stream-busy")
	// prompt-5 Task H: native tool_calls JSON (wrapped with streamId).
	application.RegisterEvent[map[string]interface{}]("ai:tool_calls")
	// Plan 65 / Proposal B: file:saved is emitted by FileService.WriteFile
	// so the frontend can trigger workflows with matching runOn triggers.
	application.RegisterEvent[string]("file:saved")
	// N-152: 前端标题栏根据 window:maximised 事件切换放大/还原图标。
	application.RegisterEvent[bool]("window:maximised")
	// BUG6: AI 窗口独立的最大化/还原事件，与主窗口分离。
	application.RegisterEvent[bool]("ai-window:maximised")
	// prompt-4 Task 5: 主窗口选中代码发送到 AI 独立窗口。
	application.RegisterEvent[map[string]string]("ai:selection")
	// prompt-4 Task 5: AI 窗口「应用到编辑器」→ 主窗口应用代码。
	application.RegisterEvent[map[string]string]("ai:apply-to-editor")
	// prompt-6 Task 1: dual-window SSOT sync bus.
	application.RegisterEvent[map[string]interface{}]("settings:changed")
	application.RegisterEvent[map[string]interface{}]("conversation:saved")
	application.RegisterEvent[map[string]interface{}]("ai:open-conversation")
	application.RegisterEvent[map[string]interface{}]("ai:open-conversation-ack")
	application.RegisterEvent[map[string]interface{}]("ai:open-settings")
	application.RegisterEvent[map[string]interface{}]("agent:pending-updated")
	application.RegisterEvent[services.ExtensionLifecycleRequest]("extension:lifecycle-request")
	application.RegisterEvent[services.ExtensionLifecycleResult]("extension:lifecycle-result")
	// P9-G05: every renderer receives the backend-owned workspace snapshot.
	// Registering the concrete payload keeps the Wails event boundary typed for
	// the main, AI, and settings views.
	application.RegisterEvent[services.WorkspaceSnapshot]("workspace:changed")
	// P9-G06: opt-in packaged role/bootstrap probe result.
	application.RegisterEvent[map[string]interface{}]("e2e:g06-runtime-role-result")
	// P9-G10: opt-in packaged Monaco probe result.
	application.RegisterEvent[map[string]interface{}]("e2e:g10-monaco-result")
	// P12-BUG-02: packaged dual-WebView conversation handoff result.
	application.RegisterEvent[map[string]interface{}]("e2e:conversation-handoff-result")
	// BUG1: project:removed is emitted by ProjectService.RemoveProject so
	// the frontend can close open files and clear the current project.
	application.RegisterEvent[map[string]string]("project:removed")
}

func main() {
	if err := enforceServerBindAddress(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Initialize structured logging (N-11) before any service is created
	// so all services inherit the configured default logger. The cleanup
	// function closes the log file on shutdown.
	closeLogger := services.InitLogger(slog.LevelInfo)
	// P9-G08: the embedded VERSION is the single source of truth for the
	// packaged runtime; GetCurrentVersion prefers it over build-info fallbacks.
	services.SetAppVersion(strings.TrimSpace(embeddedVersion))
	defer closeLogger()
	runAndReport(runMain, func(err error) {
		slog.Error("application stopped with an error", "err", err)
	})
}

type bootstrapConfig struct {
	configDir    string
	koyoriDir    string
	instanceLock *services.InstanceLock
}

type bootstrapServices struct {
	*appBundle
	app             *application.App
	window          *application.WebviewWindow
	startupSettings services.Settings
	settingsLoaded  bool
}

type backgroundJobs struct {
	cancelTime    context.CancelFunc
	timeTicker    *time.Ticker
	timeDone      <-chan struct{}
	cancelSignals context.CancelFunc
	serverSignals chan os.Signal
	signalDone    <-chan struct{}
}

const coreShutdownTimeout = 10 * time.Second

type shutdownAction struct {
	name string
	run  func() error
}

type shutdownResult struct {
	name string
	err  error
}

func runMain() error {
	cfg, err := prepareBootstrapConfig()
	if err != nil {
		return err
	}

	var cleanups cleanupStack
	cleanups.add(func() { releaseInstanceLock(cfg.instanceLock) })
	defer cleanups.run()

	serviceSet := buildCoreServices(cfg)
	var jobs *backgroundJobs
	cleanups.add(func() {
		ctx, cancel := context.WithTimeout(context.Background(), coreShutdownTimeout)
		defer cancel()
		shutdownCoreServices(ctx, serviceSet, jobs)
	})

	serviceSet.app = newApplication(serviceSet)
	createMainWindow(serviceSet)
	configureStartup(serviceSet)
	// E2E automation lives in internal/e2e; the endpoint compiles only under
	// the `e2e` build tag and stays dormant unless KOYORI_IDE_E2E=1 is set.
	e2eCleanup, err := e2e.Start(e2e.ServiceSet{
		Project:       serviceSet.Project,
		File:          serviceSet.File,
		Git:           serviceSet.Git,
		GitWorktree:   serviceSet.GitWorktree,
		GitRebase:     serviceSet.GitRebase,
		Settings:      serviceSet.Settings,
		Terminal:      serviceSet.Terminal,
		Search:        serviceSet.Search,
		Agent:         serviceSet.Agent,
		AI:            serviceSet.AI,
		LSP:           serviceSet.LSP,
		LanguagePacks: serviceSet.LanguagePacks,
		Toolchain:     serviceSet.Toolchain,
		Marketplace:   serviceSet.Marketplace,
		Debug:         serviceSet.Debug,
		Diff:          serviceSet.Diff,
		Recovery:      serviceSet.Recovery,
		Window:        serviceSet.Window,
		HTTPClient:    serviceSet.HTTPClient,
		ExecJS:        serviceSet.window.ExecJS,
		ExecAIJS:      func(script string) bool { return services.ExecAIWindowJSForE2E(serviceSet.Window, script) },
		CloseWindow:   serviceSet.window.Close,
	})
	if err != nil {
		return fmt.Errorf("start E2E automation: %w", err)
	}
	if e2eCleanup != nil {
		cleanups.add(e2eCleanup)
	}
	jobs = startBackgroundJobs(context.Background(), serviceSet)
	return serviceSet.app.Run()
}

func prepareBootstrapConfig() (bootstrapConfig, error) {
	configDir, _ := os.UserConfigDir()
	koyoriDir := filepath.Join(configDir, "koyori-ide")
	if err := os.MkdirAll(koyoriDir, 0o755); err != nil {
		return bootstrapConfig{}, fmt.Errorf("create config directory: %w", err)
	}

	instanceLock := services.NewInstanceLock(koyoriDir)
	if err := instanceLock.Acquire(); err != nil {
		msg := fmt.Sprintf(
			"%v\n\nIf no Koyori IDE window is open, delete:\n%s\nand try again.",
			err,
			instanceLock.LockPath(),
		)
		services.ShowStartupError("Koyori IDE 无法启动", msg)
		return bootstrapConfig{}, fmt.Errorf("G-QUAL-05: %w", err)
	}

	return bootstrapConfig{
		configDir:    configDir,
		koyoriDir:    koyoriDir,
		instanceLock: instanceLock,
	}, nil
}

func releaseInstanceLock(instanceLock *services.InstanceLock) {
	if instanceLock == nil {
		return
	}
	if err := instanceLock.Release(); err != nil {
		slog.Warn("instance lock release failed", "err", err)
	}
}

func buildCoreServices(cfg bootstrapConfig) *bootstrapServices {
	serviceSet := &bootstrapServices{appBundle: &appBundle{}}
	// GOAL-P0-02: create the shared workspace identity before any service, so
	// every holder is handed the same instance instead of a copied string.
	serviceSet.WorkspaceCtx = services.NewWorkspaceContext()
	buildFoundationServices(cfg, serviceSet)
	buildEditorServices(cfg, serviceSet)
	buildAgentServices(cfg, serviceSet)
	buildAnalysisServices(cfg, serviceSet)
	wireServiceDependencies(serviceSet)
	serviceSet.InstanceLock = cfg.instanceLock
	return serviceSet
}

func buildFoundationServices(cfg bootstrapConfig, serviceSet *bootstrapServices) {
	serviceSet.File = services.NewFileServiceWithWorkspaceContext(serviceSet.WorkspaceCtx)
	serviceSet.Terminal = services.NewTerminalServiceWithWorkspaceContext(serviceSet.WorkspaceCtx)
	serviceSet.Agent = services.NewAgentServiceWithWorkspaceContext(serviceSet.WorkspaceCtx)
	serviceSet.AI = services.NewAIService()
	serviceSet.Preset = services.NewPresetService(cfg.configDir)
	serviceSet.Project = services.NewProjectService(
		serviceSet.File,
		serviceSet.Terminal,
		serviceSet.Agent,
		serviceSet.AI,
	)

	serviceSet.Profile = services.NewProfileService(cfg.configDir)
	activeSettingsPath, err := serviceSet.Profile.ActiveSettingsPath()
	if err != nil || activeSettingsPath == "" {
		activeSettingsPath = filepath.Join(cfg.koyoriDir, "settings.json")
	}
	serviceSet.Settings = services.NewSettingsServiceWithPath(activeSettingsPath)
	serviceSet.HTTPClient = services.NewHTTPClientService(
		cfg.koyoriDir,
		services.NewSettingsHTTPSecretResolver(serviceSet.Settings),
	)
	serviceSet.Database = services.NewDatabaseService(
		services.NewSettingsDatabaseSecretResolver(serviceSet.Settings),
	)
	serviceSet.Layout = services.NewLayoutServiceWithPath(
		filepath.Join(filepath.Dir(activeSettingsPath), "layout.json"),
	)
	serviceSet.Window = services.NewWindowServiceWithWorkspaceContext(serviceSet.WorkspaceCtx)
	serviceSet.Git = services.NewGitService()
	serviceSet.GitWorktree, err = services.NewGitWorktreeServiceWithSafeRoots(
		serviceSet.Git,
		nil,
	)
	if err != nil {
		panic(fmt.Sprintf("construct GitWorktreeService: %v", err))
	}
	serviceSet.GitRebase = services.NewGitRebaseService(serviceSet.Git)
	services.WireFoundationServices(
		serviceSet.AI,
		serviceSet.Preset,
		serviceSet.Profile,
		serviceSet.Settings,
		serviceSet.Layout,
		serviceSet.GitWorktree,
		serviceSet.GitRebase,
		serviceSet.WorkspaceCtx,
	)
	serviceSet.PullRequest = services.NewPullRequestService(serviceSet.Git, serviceSet.Settings)
	serviceSet.Search = services.NewSearchService()
}

func buildEditorServices(cfg bootstrapConfig, serviceSet *bootstrapServices) {
	// Language packs are loaded and verified before any broker snapshots its
	// language definitions. Invalid persisted state leaves only built-ins.
	serviceSet.LanguagePacks = services.NewLanguagePackService(cfg.koyoriDir)
	serviceSet.LSP = services.NewLSPServiceWithWorkspaceContext(serviceSet.WorkspaceCtx)
	serviceSet.Toolchain = services.NewToolchainServiceWithWorkspaceContext(serviceSet.WorkspaceCtx)
	serviceSet.Marketplace = services.NewMarketplaceService(cfg.configDir)
	serviceSet.ExtensionSecurity = services.NewExtensionSecurityService(cfg.configDir)
	serviceSet.Activation = services.NewActivationService()
	services.WireEditorServices(
		serviceSet.LSP,
		serviceSet.File,
		serviceSet.Marketplace,
		serviceSet.ExtensionSecurity,
		serviceSet.Activation,
	)

	if installed, ok := loadInstalledExtensionManifests(
		serviceSet.Marketplace.GetInstalledExtensionManifests,
		slog.Warn,
	); ok {
		for _, manifest := range installed {
			extensionID := manifest.ExtensionID()
			if extensionID == "" || len(manifest.ActivationEvents) == 0 {
				continue
			}
			serviceSet.Activation.RegisterExtension(extensionID, manifest.ActivationEvents)
		}
	}

	serviceSet.Conversation = services.NewConversationService("")
	serviceSet.Task = services.NewTaskService(serviceSet.Agent)
	serviceSet.Workflow = services.NewWorkflowService()
	serviceSet.LogLevel = services.NewLogLevelService()
	serviceSet.Rules = services.NewRulesService(cfg.configDir)
	serviceSet.Plugin = services.NewPluginService(cfg.configDir)
}

func buildAgentServices(cfg bootstrapConfig, serviceSet *bootstrapServices) {
	serviceSet.MCP = services.NewMCPService()
	serviceSet.Skills = services.NewSkillsService(cfg.configDir)
	serviceSet.ComputerUse = services.NewComputerUseService(cfg.configDir)
	serviceSet.IM = services.NewIMService(cfg.configDir)
	serviceSet.Persona = services.NewPersonaService(cfg.configDir)
	serviceSet.AIPlan = services.NewAIPlanService()
	serviceSet.AIGoal = services.NewAIGoalService()
	serviceSet.AIPermission = services.NewAIPermissionService(cfg.configDir)
	services.SetAgentSettingsService(serviceSet.Agent, serviceSet.Settings)
	services.WireAgentServices(serviceSet.Agent, serviceSet.MCP, serviceSet.Skills)
	if err := services.WireAgentExecutionCore(
		serviceSet.Agent,
		serviceSet.File,
		serviceSet.Search,
		serviceSet.MCP,
		serviceSet.Skills,
		serviceSet.AIPermission,
		serviceSet.Git,
	); err != nil {
		panic(fmt.Sprintf("wire agent execution core: %v", err))
	}
	var err error
	serviceSet.AgentLifecycle, err = services.WireAgentLifecycle(
		serviceSet.Agent,
		serviceSet.AI,
		serviceSet.AIPlan,
		serviceSet.AIGoal,
		serviceSet.AIPermission,
		cfg.koyoriDir,
	)
	if err != nil {
		panic(fmt.Sprintf("wire agent lifecycle: %v", err))
	}
	if err := services.WireTaskAgentLifecycle(serviceSet.Task, serviceSet.AgentLifecycle); err != nil {
		panic(fmt.Sprintf("wire task agent lifecycle: %v", err))
	}
	if err := services.WireAgentExecutionAI(serviceSet.Agent, serviceSet.AI); err != nil {
		panic(fmt.Sprintf("wire agent AI execution: %v", err))
	}
	if err := services.WireAgentComputerUse(serviceSet.Agent, serviceSet.ComputerUse); err != nil {
		slog.Warn("wire agent computer use tools", "error", err)
	}
	if err := services.WireAgentWorkflowTools(serviceSet.Agent, serviceSet.Workflow); err != nil {
		slog.Warn("wire agent workflow tools", "error", err)
	}
	serviceSet.Diff = services.NewDiffServiceWithReceiptDir(
		filepath.Join(cfg.koyoriDir, "diff-receipts"),
	)
	serviceSet.Snapshot = services.NewSnapshotService(cfg.configDir)
}

func buildAnalysisServices(cfg bootstrapConfig, serviceSet *bootstrapServices) {
	serviceSet.Debug = services.NewDebugServiceWithWorkspaceContext(serviceSet.WorkspaceCtx)
	serviceSet.Coverage = services.NewCoverageServiceWithWorkspaceContext(serviceSet.WorkspaceCtx)
	serviceSet.Eslint = services.NewEslintServiceWithWorkspaceContext(serviceSet.WorkspaceCtx)
	serviceSet.SymbolIndex = services.NewSymbolIndexServiceWithWorkspaceContext(serviceSet.WorkspaceCtx)
	serviceSet.PProf = services.NewPProfService()
	serviceSet.Update = services.NewUpdateService()
	serviceSet.Crash = services.NewCrashService(serviceSet.Update)
	// P20 P1-05: recovered goroutine panics are persisted as crash reports
	// (fail-closed visibility) in addition to the guard's structured slog.
	services.SetGoroutinePanicSink(func(scope string, panicValue any, stack []byte) {
		if err := serviceSet.Crash.ReportCrash(services.CrashReport{
			Message:   fmt.Sprintf("goroutine panic in %s: %v", scope, panicValue),
			ErrorType: "goroutine-panic",
			Stack:     string(stack),
		}); err != nil {
			slog.Error("persist goroutine panic report failed", "scope", scope, "err", err)
		}
	})
	serviceSet.Remote = services.NewRemoteService()
	// GOAL-P0-03: editor dirty-buffer recovery. Kept separate from CrashService,
	// which only persists panic reports and is not a content backup.
	serviceSet.Recovery = services.NewRecoveryService(cfg.configDir)
}

func wireServiceDependencies(serviceSet *bootstrapServices) {
	wsCtx := serviceSet.WorkspaceCtx
	services.SetMCPWorkspaceContext(serviceSet.MCP, wsCtx)
	// GOAL-P0-02: the second argument stays "" on purpose. Bootstrap runs before
	// any project is open, so there is no root to capture here. The shared
	// context injected below is what actually resolves the root at call time.
	services.WireAnalysisServices(
		serviceSet.Snapshot,
		serviceSet.Git,
		serviceSet.AIPlan,
		serviceSet.AIGoal,
		serviceSet.Diff,
		serviceSet.Search,
		serviceSet.Recovery,
		serviceSet.File,
		wsCtx,
		services.NewDefaultStepExecutorWithContext(serviceSet.Agent, wsCtx),
		services.NewDefaultGoalExecutorWithContext(serviceSet.Agent, wsCtx),
		services.NewDefaultSecurityCheckerWithContext(serviceSet.Agent, wsCtx),
	)
	services.WireRecoveryGuards(serviceSet.Project, serviceSet.Window, serviceSet.Recovery)
	// GOAL-P0-03: the recovery journal is keyed by workspace, so it needs the
	// same shared identity. With no workspace open it fails closed rather than
	// writing dirty buffers to an unscoped location.
}

func bindWorkspaceRoots(serviceSet *bootstrapServices, settings services.Settings) {
	// GOAL-P0-02: linking the shared context makes it part of OpenProject's
	// two-phase commit, so Plan / Goal / Diff / executor switch workspaces
	// atomically with FileService and the rest.
	services.WireWorkspaceServices(
		serviceSet.Project,
		serviceSet.WorkspaceCtx,
		serviceSet.Git,
		serviceSet.Search,
		serviceSet.LSP,
		serviceSet.Toolchain,
		serviceSet.SymbolIndex,
		serviceSet.Coverage,
		serviceSet.Eslint,
		serviceSet.MCP,
		serviceSet.PProf,
	)
	for section, config := range settings.LSPConfigs {
		serviceSet.LSP.SetLSPConfig(section, config)
	}
}

func registerWailsServices(app *application.Options, serviceSet *bootstrapServices) {
	app.Services = serviceSet.wailsServices()
}

func setupHTTPRoutes(app *application.Options, serviceSet *bootstrapServices) {
	middleware := assetMiddleware(serviceSet.Plugin)
	if guard := serverTransportMiddleware(); guard != nil {
		middleware = application.ChainMiddleware(guard, middleware)
	}
	app.Assets = application.AssetOptions{
		Handler:    application.AssetFileServerFS(assets),
		Middleware: middleware,
	}
}

func newApplication(serviceSet *bootstrapServices) *application.App {
	options := application.Options{
		Name:        "Koyori IDE",
		Description: "AI-Powered Code Editor",
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	}
	registerWailsServices(&options, serviceSet)
	setupHTTPRoutes(&options, serviceSet)
	return application.New(options)
}

func assetMiddleware(pluginService *services.PluginService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleAssetRequest(next, pluginService, w, r)
		})
	}
}

func handleAssetRequest(
	next http.Handler,
	pluginService *services.PluginService,
	w http.ResponseWriter,
	r *http.Request,
) {
	if strings.HasPrefix(r.URL.Path, pluginAssetPathPrefix) {
		handlePluginAssetRequest(pluginService, w, r)
		return
	}

	recorder := &responseRecorder{
		ResponseWriter: w,
		buf:            &bytes.Buffer{},
		statusCode:     http.StatusOK,
	}
	next.ServeHTTP(recorder, r)
	if !recorder.bufferHTML {
		if !recorder.headerWritten {
			recorder.WriteHeader(http.StatusOK)
		}
		return
	}
	writeHTMLAssetResponse(w, recorder)
}

func handlePluginAssetRequest(
	pluginService *services.PluginService,
	w http.ResponseWriter,
	r *http.Request,
) {
	if strings.HasSuffix(r.URL.Path, ".html") {
		w.Header().Set(
			"Content-Security-Policy",
			"default-src 'none'; "+
				"script-src 'unsafe-inline' 'self'; "+
				"style-src 'unsafe-inline'; "+
				"img-src data: https:; "+
				"font-src data:; "+
				"connect-src 'none'; "+
				"frame-ancestors 'self'",
		)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
	} else {
		applySecurityHeaders(w)
	}
	servePluginAsset(w, r, pluginService)
}

func writeHTMLAssetResponse(w http.ResponseWriter, recorder *responseRecorder) {
	status := recorder.statusCode
	if status == 0 {
		status = http.StatusOK
	}

	nonce, err := generateNonce()
	if err != nil {
		slog.Error("CSP nonce generation failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	body := injectNonceIntoHTML(recorder.buf.Bytes(), nonce)
	w.Header().Set(
		"Content-Security-Policy",
		fmt.Sprintf(contentSecurityPolicyWithNonce, nonce),
	)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func createMainWindow(serviceSet *bootstrapServices) {
	serviceSet.window = serviceSet.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "Koyori IDE",
		Width:     1000,
		Height:    618,
		Frameless: true,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: false,
		},
		BackgroundColour: application.NewRGB(6, 7, 15),
		URL:              services.RuntimeRoleURL(serviceSet.Window, services.RuntimeRoleMain, "/"),
	})
	services.AttachWindowService(serviceSet.Window, serviceSet.app, serviceSet.window)
	registerMainWindowEvents(serviceSet)
}

func registerMainWindowEvents(serviceSet *bootstrapServices) {
	serviceSet.window.OnWindowEvent(events.Common.WindowMaximise, func(_ *application.WindowEvent) {
		serviceSet.app.Event.Emit("window:maximised", true)
	})
	serviceSet.window.OnWindowEvent(events.Common.WindowRestore, func(_ *application.WindowEvent) {
		serviceSet.app.Event.Emit("window:maximised", false)
	})
	serviceSet.window.OnWindowEvent(events.Common.WindowUnMaximise, func(_ *application.WindowEvent) {
		serviceSet.app.Event.Emit("window:maximised", false)
	})
	serviceSet.window.OnWindowEvent(events.Common.WindowClosing, func(_ *application.WindowEvent) {
		serviceSet.Window.CloseAIWindow()
	})
}

func configureStartup(serviceSet *bootstrapServices) {
	serviceSet.startupSettings, serviceSet.settingsLoaded = loadStartupSettings(
		serviceSet.Settings.LoadSettings,
		slog.Warn,
	)
	settingsForBinding := services.Settings{}
	if serviceSet.settingsLoaded && serviceSet.startupSettings.OpenAIWindowOnStartup {
		serviceSet.Window.OpenAIWindow()
	}
	if serviceSet.settingsLoaded {
		settingsForBinding = serviceSet.startupSettings
	}
	bindWorkspaceRoots(serviceSet, settingsForBinding)
	connectApplicationServices(serviceSet)
}

func connectApplicationServices(serviceSet *bootstrapServices) {
	services.AttachApplicationServices(
		serviceSet.app,
		serviceSet.Marketplace,
		serviceSet.AI,
		serviceSet.Settings,
		serviceSet.AIPermission,
		serviceSet.Terminal,
		serviceSet.File,
		serviceSet.Project,
	)
}

func startBackgroundJobs(ctx context.Context, serviceSet *bootstrapServices) *backgroundJobs {
	jobs := &backgroundJobs{}
	timeContext, cancelTime := context.WithCancel(ctx)
	jobs.cancelTime = cancelTime
	jobs.timeTicker = time.NewTicker(time.Second)
	timeDone := make(chan struct{})
	jobs.timeDone = timeDone
	go func() {
		defer services.RecoverGoroutinePanic("main:time-pump")
		defer close(timeDone)
		emitTimeEvents(timeContext, jobs.timeTicker.C, func(value string) {
			serviceSet.app.Event.Emit("time", value)
		})
	}()

	if application.System.IsServer() {
		signalContext, cancelSignals := context.WithCancel(ctx)
		jobs.cancelSignals = cancelSignals
		jobs.serverSignals = make(chan os.Signal, 1)
		signal.Notify(jobs.serverSignals, syscall.SIGINT, syscall.SIGTERM)
		signalDone := make(chan struct{})
		jobs.signalDone = signalDone
		go func() {
			defer close(signalDone)
			stopOnSignal(signalContext, jobs.serverSignals, serviceSet.app.Quit)
		}()
	}
	return jobs
}

func (jobs *backgroundJobs) stop() {
	if jobs.serverSignals != nil {
		signal.Stop(jobs.serverSignals)
	}
	if jobs.cancelSignals != nil {
		jobs.cancelSignals()
	}
	if jobs.signalDone != nil {
		<-jobs.signalDone
	}
	if jobs.cancelTime != nil {
		jobs.cancelTime()
	}
	if jobs.timeTicker != nil {
		jobs.timeTicker.Stop()
	}
	if jobs.timeDone != nil {
		<-jobs.timeDone
	}
}

func runShutdownActions(ctx context.Context, actions []shutdownAction, warn func(string, ...any)) {
	results := make(chan shutdownResult, len(actions))
	pending := make(map[string]struct{}, len(actions))
	for _, action := range actions {
		action := action
		pending[action.name] = struct{}{}
		go func() {
			results <- shutdownResult{name: action.name, err: action.run()}
		}()
	}

	for len(pending) > 0 {
		select {
		case result := <-results:
			delete(pending, result.name)
			if result.err != nil {
				warn("service shutdown failed", "service", result.name, "err", result.err)
			}
		case <-ctx.Done():
			names := make([]string, 0, len(pending))
			for name := range pending {
				names = append(names, name)
			}
			sort.Strings(names)
			warn("shutdown deadline exceeded", "pending", strings.Join(names, ", "), "err", ctx.Err())
			return
		}
	}
}

func shutdownCoreServices(ctx context.Context, serviceSet *bootstrapServices, jobs *backgroundJobs) {
	taskDone := make(chan struct{})
	actions := []shutdownAction{
		{name: "file", run: serviceSet.File.ServiceShutdown},
		{name: "terminal", run: func() error { serviceSet.Terminal.Shutdown(); return nil }},
		{name: "task", run: func() error { defer close(taskDone); return serviceSet.Task.Shutdown() }},
		{name: "agent", run: func() error {
			select {
			case <-taskDone:
				if err := ctx.Err(); err != nil {
					return err
				}
				return serviceSet.Agent.Close()
			case <-ctx.Done():
				return ctx.Err()
			}
		}},
		{name: "debug", run: serviceSet.Debug.Shutdown},
		{name: "remote", run: serviceSet.Remote.Close},
		{name: "mcp", run: serviceSet.MCP.Close},
		{name: "pprof", run: serviceSet.PProf.Close},
		{name: "database", run: serviceSet.Database.Close},
		{name: "lsp", run: func() error { serviceSet.LSP.StopAll(); return nil }},
	}
	var stopJobs func()
	if jobs != nil {
		stopJobs = jobs.stop
	}
	runCoreShutdown(ctx, stopJobs, actions, slog.Warn)
}

func runCoreShutdown(
	ctx context.Context,
	stopJobs func(),
	actions []shutdownAction,
	warn func(string, ...any),
) {
	if stopJobs != nil {
		actions = append(actions, shutdownAction{
			name: "background jobs",
			run:  func() error { stopJobs(); return nil },
		})
	}
	runShutdownActions(ctx, actions, warn)
}

// pluginAssetPathPrefix is the URL path prefix for plugin asset requests.
const pluginAssetPathPrefix = "/_plugins/"

// contentSecurityPolicyWithNonce is the CSP template applied to HTML
// responses (Plan 66 / N-14, N-34). Each HTML response gets a fresh
// per-request nonce that is injected into inline <script> tags and the
// CSP header's script-src directive, replacing 'unsafe-inline'.
//
// The policy is intentionally strict:
//   - default-src 'self'        — base default, restricted to same origin
//   - script-src 'self' 'nonce-<N>' blob: — Vite-built scripts, nonce-tagged
//     inline scripts, and blob: workers (Monaco)
//   - style-src 'self' 'unsafe-inline' — Vue scoped styles and theme CSS
//     (style-src keeps 'unsafe-inline' because Vue's scoped-style runtime
//     injects <style> tags dynamically; nonceing styles is invasive and
//     not the security win that nonceing scripts is)
//   - img-src 'self' data: blob: — data-URI icons and generated previews
//   - font-src 'self' data: — embedded fonts
//   - connect-src 'self' — same-origin only; AI/network calls go through Go
//   - worker-src 'self' blob: — Monaco and other web workers
//   - frame-ancestors 'none' — disallow embedding the app in any frame
//
// N-34 (prompt-4.md): replaces the previous 'unsafe-inline' for script-src
// with a per-request nonce. The nonce is generated fresh on every HTML
// response, so an attacker who learns one nonce cannot reuse it on a
// subsequent request. Non-HTML responses (JS/CSS/assets) keep the static
// CSP without the nonce, since they don't contain inline scripts.
const contentSecurityPolicyWithNonce = "default-src 'self'; " +
	"script-src 'self' 'nonce-%s' blob:; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"worker-src 'self' blob:; " +
	"frame-ancestors 'none'"

// contentSecurityPolicyStatic is the CSP applied to non-HTML responses
// (JS/CSS/assets/plugin assets). These responses don't contain inline
// scripts, so they don't need the nonce. 'unsafe-inline' is omitted from
// script-src entirely — only 'self' and blob: are allowed.
const contentSecurityPolicyStatic = "default-src 'self'; " +
	"script-src 'self' blob:; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"worker-src 'self' blob:; " +
	"frame-ancestors 'none'"

// applySecurityHeaders injects security-related HTTP headers on the
// response. It is called by the asset middleware for every request,
// including plugin asset requests. The headers tighten the webview's
// security posture (Plan 66 / N-14):
//   - Content-Security-Policy: restricts resource loading to same origin
//   - X-Content-Type-Options: nosniff — prevents MIME-type sniffing
//   - X-Frame-Options: DENY — legacy frame-embedding guard (CSP
//     frame-ancestors is the modern equivalent, but X-Frame-Options is
//     kept for older webviews)
//   - Referrer-Policy: no-referrer — never leak the app's origin/path
func applySecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", contentSecurityPolicyStatic)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// generateNonce returns a fresh 16-byte hex-encoded random nonce for use
// in CSP script-src 'nonce-<N>' and inline <script nonce="<N>"> tags.
// Each HTML response gets its own nonce; nonces are not reused across
// requests so an attacker who learns one cannot inject scripts later.
//
// G-SEC-10: If crypto/rand.Read fails we return an error instead of
// falling back to a predictable time-derived nonce. A predictable nonce
// defeats the purpose of CSP, so the caller must refuse to serve the
// page (HTTP 500) rather than ship a weak nonce.
//
// N-34 (prompt-4.md).
func generateNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate CSP nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// scriptTagPattern matches <script ...> opening tags so we can inject a
// nonce attribute. Handles <script>, <script type="module">, and
// <script src="...">. Captures the tag's attributes in group 1.
var scriptTagPattern = regexp.MustCompile(`<script(\s[^>]*)?>`)

// injectNonceIntoHTML replaces the CSP nonce placeholder in the given HTML
// body and adds a nonce attribute to every <script> tag. This is the core
// of N-34: it lets us drop 'unsafe-inline' from script-src in production.
//
// The function is applied to HTML responses only (content-type text/html).
// JS/CSS/asset responses are passed through unchanged.
func injectNonceIntoHTML(body []byte, nonce string) []byte {
	// Add nonce="..." to every <script> tag that doesn't already have one.
	injected := scriptTagPattern.ReplaceAllFunc(body, func(match []byte) []byte {
		// If the tag already has a nonce attribute, leave it alone.
		if bytes.Contains(match, []byte("nonce=")) {
			return match
		}
		// Insert nonce after "<script".
		insert := ` nonce="` + nonce + `"`
		// Find the position right after "<script".
		insertPos := 7 // len("<script")
		result := make([]byte, 0, len(match)+len(insert))
		result = append(result, match[:insertPos]...)
		result = append(result, []byte(insert)...)
		result = append(result, match[insertPos:]...)
		return result
	})
	return injected
}

// responseRecorder buffers HTML so the middleware can inject CSP nonces.
// Non-HTML headers and body chunks are forwarded immediately to avoid holding
// large asset or streaming responses in memory.
//
// N-34 (prompt-4.md): Content-Type selects HTML buffering before the first
// body write. If the handler omits Content-Type, Write detects it from the
// first chunk before deciding whether to buffer or stream.
//
// prompt-6 Task 6 / BUG-M10: statusCode is preserved and written through
// so AssetServer 404/500 is not rewritten as 200.
type responseRecorder struct {
	http.ResponseWriter
	buf           *bytes.Buffer
	statusCode    int
	headerWritten bool
	bufferHTML    bool
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.headerWritten {
		if r.Header().Get("Content-Type") == "" {
			r.Header().Set("Content-Type", http.DetectContentType(b))
		}
		status := r.statusCode
		if status == 0 {
			status = http.StatusOK
		}
		r.WriteHeader(status)
	}
	if r.bufferHTML {
		return r.buf.Write(b)
	}
	return r.ResponseWriter.Write(b)
}

// WriteHeader records HTML status for later nonce injection. Non-HTML status
// is forwarded immediately with the static security headers.
func (r *responseRecorder) WriteHeader(statusCode int) {
	if r.headerWritten {
		return
	}
	r.statusCode = statusCode
	r.headerWritten = true
	if strings.HasPrefix(r.Header().Get("Content-Type"), "text/html") {
		r.bufferHTML = true
		return
	}
	applySecurityHeaders(r.ResponseWriter)
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *responseRecorder) Flush() {
	if r.bufferHTML {
		return
	}
	if !r.headerWritten {
		r.WriteHeader(http.StatusOK)
	}
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// servePluginAsset handles /_plugins/<plugin-name>/<rel-path> requests by
// delegating to PluginService.ServePluginAsset. It reads the current
// project root from the appState (forwarded as a query parameter by the
// frontend) so project-scoped plugins can be resolved.
//
// Plan 58 / N-21: This is the runtime side of the plugin protocol handler.
// Wails v3 alpha2.111 does not expose a public API for registering custom
// URL schemes, so we intercept requests on the existing asset handler's
// scheme (http://wails.localhost on Windows, wails://localhost on
// macOS/Linux) via AssetOptions.Middleware.
func servePluginAsset(w http.ResponseWriter, r *http.Request, svc *services.PluginService) {
	// Strip the prefix to get "<plugin-name>/<rel-path>".
	rest := strings.TrimPrefix(r.URL.Path, pluginAssetPathPrefix)
	if rest == "" {
		http.Error(w, "plugin name is required", http.StatusBadRequest)
		return
	}
	// Split into plugin name and relative path. The first path segment
	// is the plugin name; the rest is the relative path within the plugin
	// directory. We use strings.SplitN to handle rel-paths that contain
	// "/" separators.
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "plugin name is required", http.StatusBadRequest)
		return
	}
	pluginName := parts[0]
	if len(parts) < 2 || parts[1] == "" {
		http.Error(w, "file path is required", http.StatusBadRequest)
		return
	}
	relPath := parts[1]
	// The project root is forwarded by the frontend as a query parameter
	// so project-scoped plugins can be resolved. Empty means user-global only.
	projectRoot := r.URL.Query().Get("projectRoot")
	data, mime, err := svc.ServePluginAsset(pluginName, relPath, projectRoot)
	if err != nil {
		slog.Error("plugin asset serve failed",
			"plugin", pluginName,
			"path", relPath,
			"error", err)
		http.Error(w, "plugin asset not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", mime)
	// Allow dynamic import() of plugin scripts. Without
	// Cross-Origin-Resource-Policy, some webview versions block the
	// response from being used as a module.
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := w.Write(data); err != nil {
		slog.Warn("plugin asset write failed", "error", err)
	}
}
