package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// WindowService exposes native window controls to the frontend.
// It manages both the main editor window and the independent AI companion
// window (prompt-4 Task 1).
//
// N-133: window fields are guarded with a sync.RWMutex so concurrent
// SetWindow / OpenAIWindow / method calls cannot race.
type aiWindowHandle interface {
	Show() application.Window
	Hide() application.Window
	Close()
	IsVisible() bool
	SetAlwaysOnTop(bool) application.Window
	Minimise() application.Window
	UnMinimise()
	ToggleMaximise()
	IsMaximised() bool
	Focus()
}

type WindowService struct {
	mu                   sync.RWMutex
	workspaceContext     *WorkspaceContext
	recoveryService      *RecoveryService
	startCommand         func(string, ...string) error
	window               *application.WebviewWindow // main editor window
	aiWindow             aiWindowHandle             // independent AI companion window
	app                  *application.App           // needed to (re)create the AI window
	aiAlwaysOnTop        bool                       // default true (prompt-4 Task 6)
	aiWindowReady        chan struct{}
	aiWindowGeneration   uint64
	aiWindowUsers        map[aiWindowHandle]int
	aiWindowPendingClose map[aiWindowHandle]struct{}
	runtimeRoleTokens    map[string]runtimeRoleToken
	runtimeRoleIssued    map[RuntimeRole]int
	runtimeRoleResolved  map[RuntimeRole]int
	runtimeRoleInvalid   int
	aiWindowsCreated     int
	aiWindowsClosed      int
	removeRecoveryHook   func()
}

func NewWindowServiceWithWorkspaceContext(workspaceContext *WorkspaceContext) *WindowService {
	return &WindowService{workspaceContext: workspaceContext}
}

// setWorkspaceContext binds renderer-requested external paths to the active
// workspace. Non-workspace window controls remain available without a root.
//
//wails:ignore
func (w *WindowService) setWorkspaceContext(ctx *WorkspaceContext) {
	w.mu.Lock()
	w.workspaceContext = ctx
	w.mu.Unlock()
}

//wails:ignore
func (w *WindowService) setRecoveryService(recovery *RecoveryService) {
	w.mu.Lock()
	w.recoveryService = recovery
	window := w.window
	remove := w.removeRecoveryHook
	w.removeRecoveryHook = nil
	w.mu.Unlock()
	if remove != nil {
		remove()
	}
	w.installRecoveryCloseHook(window, recovery)
}

const launchedCommandTimeout = 10 * time.Second

func startLaunchedCommand(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), launchedCommandTimeout)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := cmd.Process.Kill()
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			slog.Warn("kill timed-out launched command", "command", name, "err", err)
		}
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	go func() {
		defer cancel()
		if err := cmd.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				slog.Debug("launched command timed out", "command", name, "err", err)
				return
			}
			slog.Debug("wait for launched command", "command", name, "err", err)
		}
	}()
	return nil
}

// setApp injects the application handle so OpenAIWindow can create windows.
//
//wails:ignore
func (w *WindowService) setApp(app *application.App) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.app = app
	w.aiAlwaysOnTop = true // prompt-4: 置顶默认开启
	w.aiWindowGeneration++
}

// setWindow injects the main window. Called from main.go after creation.
//
//wails:ignore
func (w *WindowService) setWindow(window *application.WebviewWindow) {
	w.mu.Lock()
	remove := w.removeRecoveryHook
	w.removeRecoveryHook = nil
	w.window = window
	recovery := w.recoveryService
	w.mu.Unlock()
	if remove != nil {
		remove()
	}
	w.installRecoveryCloseHook(window, recovery)
}

func (w *WindowService) installRecoveryCloseHook(
	window *application.WebviewWindow,
	recovery *RecoveryService,
) {
	if window == nil || recovery == nil {
		return
	}
	remove := window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if recovery.requireResolved("window close") != nil {
			event.Cancel()
		}
	})
	w.mu.Lock()
	if w.window == window && w.recoveryService == recovery {
		w.removeRecoveryHook = remove
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	remove()
}

// setAIWindow injects the AI companion window (or nil on close).
//
//wails:ignore
func (w *WindowService) setAIWindow(win *application.WebviewWindow) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.aiWindowGeneration++
	if win == nil {
		w.aiWindow = nil
		return
	}
	w.aiWindow = win
}

// aiWindowHandle returns the current AI window handle (may be nil).
//
//wails:ignore
func (w *WindowService) aiWindowHandle() *application.WebviewWindow {
	w.mu.RLock()
	defer w.mu.RUnlock()
	win, _ := w.aiWindow.(*application.WebviewWindow)
	return win
}

// currentWindow returns the main window under a read lock.
func (w *WindowService) currentWindow() *application.WebviewWindow {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.window
}

func (w *WindowService) currentAIWindow() aiWindowHandle {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.aiWindow
}

// Minimise minimises the main window.
func (w *WindowService) Minimise() {
	if win := w.currentWindow(); win != nil {
		win.Minimise()
	}
}

// Maximise toggles the maximised state of the main window.
func (w *WindowService) Maximise() {
	if win := w.currentWindow(); win != nil {
		win.Maximise()
	}
}

// ToggleMaximise toggles between maximised and restored state.
func (w *WindowService) ToggleMaximise() {
	if win := w.currentWindow(); win != nil {
		win.ToggleMaximise()
	}
}

// IsMaximised returns whether the main window is currently maximised.
func (w *WindowService) IsMaximised() bool {
	if win := w.currentWindow(); win != nil {
		return win.IsMaximised()
	}
	return false
}

// Close closes the main window (which should also tear down the AI window).
// Startup recovery is checked before any close side effect so a title-bar click
// cannot trigger blur/close cleanup while an older journal is unresolved.
func (w *WindowService) Close() error {
	w.mu.RLock()
	recovery := w.recoveryService
	w.mu.RUnlock()
	if recovery != nil {
		if err := recovery.requireResolved("window close"); err != nil {
			return err
		}
	}
	// Close AI first so it does not outlive the main process lifecycle.
	w.CloseAIWindow()
	if win := w.currentWindow(); win != nil {
		win.Close()
	}
	return nil
}

// ToggleFullscreen toggles fullscreen mode on the main window.
func (w *WindowService) ToggleFullscreen() {
	if win := w.currentWindow(); win != nil {
		win.ToggleFullscreen()
	}
}

// SetTitle updates the main window title bar text.
func (w *WindowService) SetTitle(title string) {
	if win := w.currentWindow(); win != nil {
		win.SetTitle(title)
	}
}

// ---- AI companion window (prompt-4 Task 1 / 5 / 6) ----

// createAIWindow creates a new AI WebviewWindow without holding w.mu.
func (w *WindowService) createAIWindow(app *application.App, alwaysOnTop bool) *application.WebviewWindow {
	if app == nil {
		return nil
	}
	// BUG6: Frameless = true 移除 Windows 原生标题栏，使用前端
	// AiWindowView.vue 的 header 作为自定义标题栏（含拖拽区 + 最小化/
	// 最大化/关闭按钮）。Windows 上保留 FramelessWindowDecorations
	// （Aero 阴影 + Win11 圆角 + 原生 resize 把手），与主窗口一致。
	aiWin := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:        "ai",
		Title:       "koyori-ide AI",
		Width:       1200,
		Height:      780,
		MinWidth:    900,
		MinHeight:   560,
		Frameless:   true,
		AlwaysOnTop: alwaysOnTop,
		Windows: application.WindowsWindow{
			// false = 保留 Aero 阴影、Win11 圆角和原生 resize 八个把手
			DisableFramelessWindowDecorations: false,
		},
		// Hash-router SPA: load with the /ai-window hash so the companion
		// view mounts without reusing the main-window layout.
		URL:              RuntimeRoleURL(w, RuntimeRoleAI, "/#/ai-window"),
		BackgroundColour: application.NewRGB(6, 7, 15),
	})
	w.mu.Lock()
	w.aiWindowsCreated++
	w.mu.Unlock()
	// When the user closes the AI window, clear our handle so OpenAIWindow
	// can recreate it later.
	aiWin.OnWindowEvent(events.Common.WindowClosing, func(_ *application.WindowEvent) {
		w.mu.Lock()
		w.aiWindowsClosed++
		if w.aiWindow == aiWin {
			w.aiWindow = nil
			w.aiWindowGeneration++
		}
		delete(w.aiWindowPendingClose, aiWin)
		w.mu.Unlock()
	})
	// BUG6: 监听 AI 窗口的最大化/还原事件，向前端推送 ai-window:maximised
	// 布尔状态。前端标题栏据此切换放大 ↔ 还原图标，无需轮询后端。
	// 使用独立的事件名 ai-window:maximised，避免与主窗口的 window:maximised
	// 事件混淆（两个窗口的 maximize 状态是独立的）。
	aiWin.OnWindowEvent(events.Common.WindowMaximise, func(_ *application.WindowEvent) {
		app.Event.Emit("ai-window:maximised", true)
	})
	aiWin.OnWindowEvent(events.Common.WindowRestore, func(_ *application.WindowEvent) {
		app.Event.Emit("ai-window:maximised", false)
	})
	aiWin.OnWindowEvent(events.Common.WindowUnMaximise, func(_ *application.WindowEvent) {
		app.Event.Emit("ai-window:maximised", false)
	})
	return aiWin
}

func (w *WindowService) retainAIWindowLocked(win aiWindowHandle) {
	if w.aiWindowUsers == nil {
		w.aiWindowUsers = make(map[aiWindowHandle]int)
	}
	w.aiWindowUsers[win]++
}

func (w *WindowService) releaseAIWindow(win aiWindowHandle) {
	var closeWindow bool
	w.mu.Lock()
	if users := w.aiWindowUsers[win]; users <= 1 {
		delete(w.aiWindowUsers, win)
		if _, pending := w.aiWindowPendingClose[win]; pending {
			delete(w.aiWindowPendingClose, win)
			closeWindow = true
		}
	} else {
		w.aiWindowUsers[win] = users - 1
	}
	w.mu.Unlock()
	if closeWindow {
		win.Close()
	}
}

func (w *WindowService) acquireAIWindow(create bool) (aiWindowHandle, func()) {
	w.mu.Lock()
	requestGeneration := w.aiWindowGeneration
	for {
		// A close or app replacement that happened after this request wins.
		// A later request captures the new generation and may create again.
		if w.aiWindowGeneration != requestGeneration {
			w.mu.Unlock()
			return nil, func() {}
		}
		if win := w.aiWindow; win != nil {
			w.retainAIWindowLocked(win)
			w.mu.Unlock()
			return win, func() { w.releaseAIWindow(win) }
		}
		if !create || w.app == nil {
			w.mu.Unlock()
			return nil, func() {}
		}
		if ready := w.aiWindowReady; ready != nil {
			w.mu.Unlock()
			<-ready
			w.mu.Lock()
			continue
		}

		ready := make(chan struct{})
		w.aiWindowReady = ready
		app := w.app
		alwaysOnTop := w.aiAlwaysOnTop
		w.mu.Unlock()

		created := w.createAIWindow(app, alwaysOnTop)
		var discard aiWindowHandle
		w.mu.Lock()
		if created != nil && requestGeneration == w.aiWindowGeneration && w.aiWindow == nil {
			w.aiWindow = created
		} else if created != nil {
			discard = created
		}
		win := w.aiWindow
		if win != nil {
			w.retainAIWindowLocked(win)
		}
		if w.aiWindowReady == ready {
			w.aiWindowReady = nil
			close(ready)
		}
		w.mu.Unlock()

		if discard != nil {
			discard.Close()
		}
		if win == nil {
			return nil, func() {}
		}
		return win, func() { w.releaseAIWindow(win) }
	}
}

// OpenAIWindow creates the AI companion window if missing/closed, otherwise focuses it.
func (w *WindowService) OpenAIWindow() {
	aiWin, release := w.acquireAIWindow(true)
	defer release()
	if aiWin == nil {
		return
	}
	aiWin.Show()
	aiWin.UnMinimise()
	aiWin.Focus()
}

// CloseAIWindow closes the AI companion window if it exists.
func (w *WindowService) CloseAIWindow() {
	w.mu.Lock()
	w.aiWindowGeneration++
	aiWin := w.aiWindow
	w.aiWindow = nil
	if aiWin != nil && w.aiWindowUsers[aiWin] > 0 {
		if w.aiWindowPendingClose == nil {
			w.aiWindowPendingClose = make(map[aiWindowHandle]struct{})
		}
		w.aiWindowPendingClose[aiWin] = struct{}{}
		aiWin = nil
	}
	w.mu.Unlock()
	if aiWin != nil {
		aiWin.Close()
	}
}

// ToggleAIWindow shows the AI window if hidden/closed, or hides it if visible.
func (w *WindowService) ToggleAIWindow() {
	aiWin, release := w.acquireAIWindow(true)
	defer release()
	if aiWin == nil {
		return
	}
	visible := aiWin.IsVisible()
	if visible {
		aiWin.Hide()
		return
	}
	aiWin.Show()
	aiWin.UnMinimise()
	aiWin.Focus()
}

// IsAIWindowOpen reports whether the AI companion window currently exists.
func (w *WindowService) IsAIWindowOpen() bool {
	return w.currentAIWindow() != nil
}

// IsAIWindowVisible reports whether the AI companion window currently exists
// and is visible. A hidden window remains open but must not keep the editor's
// activity-bar AI item highlighted.
func (w *WindowService) IsAIWindowVisible() bool {
	aiWin, release := w.acquireAIWindow(false)
	defer release()
	return aiWin != nil && aiWin.IsVisible()
}

// SetAIAlwaysOnTop enables/disables always-on-top for the AI window (prompt-4 Task 6).
// Default is true. Persists the preference for future recreations.
func (w *WindowService) SetAIAlwaysOnTop(onTop bool) {
	w.mu.Lock()
	w.aiAlwaysOnTop = onTop
	aiWin := w.aiWindow
	if aiWin != nil {
		w.retainAIWindowLocked(aiWin)
	}
	w.mu.Unlock()
	if aiWin != nil {
		defer w.releaseAIWindow(aiWin)
		aiWin.SetAlwaysOnTop(onTop)
	}
}

// IsAIAlwaysOnTop returns the current always-on-top preference.
func (w *WindowService) IsAIAlwaysOnTop() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.aiAlwaysOnTop
}

// BUG6: AI 窗口控制方法。Frameless 模式下，最小化/最大化/关闭由前端
// 自定义标题栏的按钮调用这些方法实现。

// MinimiseAIWindow minimises the AI companion window.
func (w *WindowService) MinimiseAIWindow() {
	win, release := w.acquireAIWindow(false)
	defer release()
	if win != nil {
		win.Minimise()
	}
}

// ToggleMaximiseAIWindow toggles the AI companion window between maximised
// and restored state.
func (w *WindowService) ToggleMaximiseAIWindow() {
	win, release := w.acquireAIWindow(false)
	defer release()
	if win != nil {
		win.ToggleMaximise()
	}
}

// IsAIWindowMaximised returns whether the AI companion window is currently
// maximised. Used by the frontend to initialise the maximise/restore icon
// on mount.
func (w *WindowService) IsAIWindowMaximised() bool {
	win, release := w.acquireAIWindow(false)
	defer release()
	if win != nil {
		return win.IsMaximised()
	}
	return false
}

// SelectionPayload is the payload for SendSelectionToAI / ai:selection events.
type SelectionPayload struct {
	Code     string `json:"code"`
	Language string `json:"language"`
	FilePath string `json:"filePath"`
}

// SendSelectionToAI opens (or focuses) the AI window and emits an "ai:selection"
// event so the AI window can inject a code-context user message (prompt-4 Task 5).
func (w *WindowService) SendSelectionToAI(code string, language string, filePath string) {
	if code == "" {
		return
	}
	// Ensure the AI window is open and focused first.
	w.OpenAIWindow()

	w.mu.RLock()
	app := w.app
	w.mu.RUnlock()
	if app == nil {
		return
	}
	app.Event.Emit("ai:selection", map[string]string{
		"code":     code,
		"language": language,
		"filePath": filePath,
	})
}

// validateOpenPath rejects empty, relative, or non-existent paths before
// handing them to OS launchers (prompt-5 Task E / BUG-M6).
func validateOpenPath(path string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute: %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("path does not exist: %w", err)
	}
	return nil
}

// OpenPathInExplorer opens the given path in the OS file manager
// (Windows: explorer, macOS: open, Linux: xdg-open).
func (w *WindowService) OpenPathInExplorer(path string) error {
	path, lease, err := w.validateWorkspaceOpenPath(path)
	if err != nil {
		return err
	}
	var name string
	switch runtime.GOOS {
	case "windows":
		name = "explorer.exe"
	case "darwin":
		name = "open"
	default:
		name = "xdg-open"
	}
	if err := w.startWorkspaceCommand(lease, name, path); err != nil {
		return fmt.Errorf("open explorer: %w", err)
	}
	return nil
}

// OpenPathInVSCode opens the given path in VS Code via the `code` CLI.
func (w *WindowService) OpenPathInVSCode(path string) error {
	path, lease, err := w.validateWorkspaceOpenPath(path)
	if err != nil {
		return err
	}
	if err := w.startWorkspaceCommand(lease, "code", path); err != nil {
		return fmt.Errorf("open vscode: %w", err)
	}
	return nil
}

func (w *WindowService) validateWorkspaceOpenPath(path string) (string, workspaceLease, error) {
	if err := validateOpenPath(path); err != nil {
		return "", workspaceLease{}, err
	}
	w.mu.RLock()
	ctx := w.workspaceContext
	w.mu.RUnlock()
	if ctx == nil {
		return path, workspaceLease{root: path}, nil
	}
	lease, err := acquireWorkspaceLease(ctx, "", 0)
	if err != nil {
		return "", workspaceLease{}, err
	}
	resolved, err := ValidatePathWithinRoot(lease.root, path)
	if err != nil {
		return "", workspaceLease{}, fmt.Errorf("path is outside the active workspace: %w", err)
	}
	if err := lease.validateCurrent(); err != nil {
		return "", workspaceLease{}, err
	}
	return resolved, lease, nil
}

func (w *WindowService) startWorkspaceCommand(lease workspaceLease, name string, args ...string) error {
	w.mu.RLock()
	startCommand := w.startCommand
	w.mu.RUnlock()
	if startCommand == nil {
		startCommand = startLaunchedCommand
	}
	return lease.withCurrent(func() error { return startCommand(name, args...) })
}

// FocusMainWindow brings the main editor window to the front.
func (w *WindowService) FocusMainWindow() {
	if win := w.currentWindow(); win != nil {
		win.Show()
		win.UnMinimise()
		win.Focus()
	}
}
