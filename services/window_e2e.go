//go:build e2e

package services

import "github.com/wailsapp/wails/v3/pkg/application"

// ExecAIWindowJSForE2E evaluates JavaScript in the real AI companion window.
// It is compiled only into the opt-in packaged E2E artifact and is not a
// Wails service method, so production renderers cannot request arbitrary JS.
func ExecAIWindowJSForE2E(window *WindowService, script string) bool {
	if window == nil || script == "" {
		return false
	}
	aiWindow, release := window.acquireAIWindow(false)
	defer release()
	webview, ok := aiWindow.(*application.WebviewWindow)
	if !ok || webview == nil {
		return false
	}
	webview.ExecJS(script)
	return true
}
