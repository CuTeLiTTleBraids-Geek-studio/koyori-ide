package services

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type lifecycleTestAIWindow struct {
	service         *WindowService
	visible         bool
	closed          bool
	usedWithLock    bool
	usedAfterClose  bool
	operations      []string
	hideCalls       int
	showCalls       int
	unminimiseCalls int
	focusCalls      int
}

func (f *lifecycleTestAIWindow) recordUse(operation string) bool {
	unlocked := f.service.mu.TryLock()
	if unlocked {
		f.service.mu.Unlock()
	} else {
		f.usedWithLock = true
	}
	if f.closed {
		f.usedAfterClose = true
	}
	f.operations = append(f.operations, operation)
	return unlocked
}

func (f *lifecycleTestAIWindow) Show() application.Window {
	f.recordUse("show")
	f.showCalls++
	return nil
}

func (f *lifecycleTestAIWindow) Hide() application.Window {
	f.recordUse("hide")
	f.hideCalls++
	return nil
}

func (f *lifecycleTestAIWindow) Close() {
	f.recordUse("close")
	f.closed = true
}

func (f *lifecycleTestAIWindow) IsVisible() bool {
	if f.recordUse("is-visible") {
		f.service.CloseAIWindow()
	}
	return f.visible
}

func (f *lifecycleTestAIWindow) SetAlwaysOnTop(bool) application.Window {
	f.recordUse("set-always-on-top")
	return nil
}

func (f *lifecycleTestAIWindow) Minimise() application.Window {
	f.recordUse("minimise")
	return nil
}

func (f *lifecycleTestAIWindow) UnMinimise() {
	f.recordUse("unminimise")
	f.unminimiseCalls++
}
func (f *lifecycleTestAIWindow) ToggleMaximise() { f.recordUse("toggle-maximise") }
func (f *lifecycleTestAIWindow) Focus() {
	f.recordUse("focus")
	f.focusCalls++
}

func (f *lifecycleTestAIWindow) IsMaximised() bool {
	f.recordUse("is-maximised")
	return false
}

// N-133 / N-135: WindowService methods must be safe to call before
// SetWindow is invoked (window is nil) and after SetWindow(nil).
// These tests verify the nil-guard and the new mutex behavior without
// requiring a real *application.WebviewWindow (which can only be created
// by a running Wails app).
func TestWindowService_NilWindowDoesNotPanic(t *testing.T) {
	t.Parallel()
	w := &WindowService{}
	// All methods should be no-ops when window is nil.
	w.Minimise()
	w.Maximise()
	w.Close()
	w.ToggleFullscreen()
	w.SetTitle("test")
	w.OpenAIWindow()
	w.CloseAIWindow()
	w.ToggleAIWindow()
	w.SetAIAlwaysOnTop(true)
	w.SendSelectionToAI("code", "go", "main.go")
	w.FocusMainWindow()
	if w.IsAIWindowOpen() {
		t.Error("expected AI window closed when no app")
	}
	if w.IsAIWindowVisible() {
		t.Error("expected nil AI window not visible")
	}
	if !w.IsAIAlwaysOnTop() {
		// Default is true after SetApp; without SetApp, zero-value is false.
		// Ensure SetAIAlwaysOnTop works without a window.
		w.SetAIAlwaysOnTop(true)
		if !w.IsAIAlwaysOnTop() {
			t.Error("expected always-on-top true after SetAIAlwaysOnTop")
		}
	}
}

func TestWindowService_SetWindowNilThenCall(t *testing.T) {
	t.Parallel()
	w := &WindowService{}
	w.setWindow(nil)
	w.setAIWindow(nil)
	w.Minimise()
	w.Maximise()
	w.Close()
	w.ToggleFullscreen()
	w.SetTitle("test")
	w.CloseAIWindow()
}

func TestWindowService_SetWindowTwice(t *testing.T) {
	t.Parallel()
	w := &WindowService{}
	w.setWindow(nil)
	w.setWindow(nil)
	if got := w.currentWindow(); got != nil {
		t.Errorf("expected nil window, got %v", got)
	}
	if got := w.aiWindowHandle(); got != nil {
		t.Errorf("expected nil AI window, got %v", got)
	}
}

// N-133: concurrent SetWindow + method calls must not race.
// Run with -race to verify the RWMutex protects the field.
func TestWindowService_ConcurrentAccessNoRace(t *testing.T) {
	t.Parallel()
	w := &WindowService{}
	done := make(chan struct{})

	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			w.setWindow(nil)
			w.setAIWindow(nil)
			w.SetAIAlwaysOnTop(i%2 == 0)
		}
	}()

	for i := 0; i < 200; i++ {
		w.Minimise()
		w.SetTitle("x")
		_ = w.IsAIWindowOpen()
		_ = w.IsAIAlwaysOnTop()
		w.ToggleAIWindow()
		w.SendSelectionToAI("x", "go", "a.go")
	}

	<-done
}

func TestWindowService_ToggleAIWindowSerializesEveryHandleOperation(t *testing.T) {
	tests := []struct {
		name    string
		visible bool
	}{
		{name: "visible hides", visible: true},
		{name: "hidden shows focuses", visible: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &WindowService{}
			fake := &lifecycleTestAIWindow{service: w, visible: tt.visible}
			w.mu.Lock()
			w.aiWindow = fake
			w.mu.Unlock()

			w.ToggleAIWindow()
			w.CloseAIWindow()

			if !fake.closed {
				t.Fatal("leased AI window was not closed after ToggleAIWindow released it")
			}
			if fake.usedWithLock {
				t.Fatal("ToggleAIWindow invoked an AI window operation while holding w.mu")
			}
			if fake.usedAfterClose {
				t.Fatal("ToggleAIWindow used the AI window after CloseAIWindow removed and closed it")
			}
			if w.IsAIWindowOpen() {
				t.Fatal("CloseAIWindow did not remove the leased AI window")
			}
			if tt.visible {
				wantOperations := []string{"is-visible", "hide", "close"}
				if !reflect.DeepEqual(fake.operations, wantOperations) {
					t.Fatalf("visible toggle operation order = %v, want %v", fake.operations, wantOperations)
				}
				if fake.hideCalls != 1 || fake.showCalls != 0 || fake.unminimiseCalls != 0 || fake.focusCalls != 0 {
					t.Fatalf("visible toggle calls: hide=%d show=%d unminimise=%d focus=%d",
						fake.hideCalls, fake.showCalls, fake.unminimiseCalls, fake.focusCalls)
				}
				return
			}
			wantOperations := []string{"is-visible", "show", "unminimise", "focus", "close"}
			if !reflect.DeepEqual(fake.operations, wantOperations) {
				t.Fatalf("hidden toggle operation order = %v, want %v", fake.operations, wantOperations)
			}
			if fake.hideCalls != 0 || fake.showCalls != 1 || fake.unminimiseCalls != 1 || fake.focusCalls != 1 {
				t.Fatalf("hidden toggle calls: hide=%d show=%d unminimise=%d focus=%d",
					fake.hideCalls, fake.showCalls, fake.unminimiseCalls, fake.focusCalls)
			}
		})
	}
}

func TestWindowService_SendSelectionToAI_EmptyCodeNoOp(t *testing.T) {
	t.Parallel()
	w := &WindowService{}
	// Must not panic with empty code even without app.
	w.SendSelectionToAI("", "go", "main.go")
}

func TestWindowService_OpenPathValidation(t *testing.T) {
	t.Parallel()
	w := &WindowService{}
	if err := w.OpenPathInExplorer(""); err == nil {
		t.Error("expected error for empty explorer path")
	}
	if err := w.OpenPathInVSCode(""); err == nil {
		t.Error("expected error for empty vscode path")
	}
	// prompt-5 Task E: relative paths rejected.
	if err := w.OpenPathInExplorer("relative/path"); err == nil {
		t.Error("expected error for relative explorer path")
	}
	if err := w.OpenPathInVSCode("relative/path"); err == nil {
		t.Error("expected error for relative vscode path")
	}
	// Non-existent absolute path rejected (OS-specific abs form).
	missing := filepath.Join(t.TempDir(), "does-not-exist-xyz")
	if err := w.OpenPathInExplorer(missing); err == nil {
		t.Error("expected error for non-existent explorer path")
	}
}

func TestWindowService_DefaultAlwaysOnTopAfterSetApp(t *testing.T) {
	t.Parallel()
	w := &WindowService{}
	w.setApp(nil) // still sets the default preference
	if !w.IsAIAlwaysOnTop() {
		t.Error("expected default always-on-top true after SetApp")
	}
}

// P9-G06 AC3: closing the main window first must close the AI companion and
// clear its handle so the companion cannot outlive the main process lifecycle
// (leaking a background WebView). WindowService.Close() mirrors the main-window
// closing path wired in main.go registerMainWindowEvents.
func TestWindowService_MainWindowCloseClosesAIWindowFirst(t *testing.T) {
	w := &WindowService{}
	fake := &lifecycleTestAIWindow{service: w, visible: true}
	w.mu.Lock()
	w.aiWindow = fake
	w.mu.Unlock()

	if err := w.Close(); err != nil {
		t.Fatalf("main-window Close returned error: %v", err)
	}
	if !fake.closed {
		t.Fatal("main-window close did not close the AI companion window")
	}
	if w.IsAIWindowOpen() {
		t.Fatal("AI window handle still present after main-window close")
	}
	if w.aiWindowHandle() != nil {
		t.Fatal("AI window handle was not cleared after main-window close")
	}
}
