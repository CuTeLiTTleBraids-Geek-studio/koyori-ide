//go:build ignore

// This file is a Wails iOS template hook. It is injected into the root main
// package via the -overlay mechanism during iOS builds (go build -tags ios).
// The root main.go does not call modifyOptionsForIos, so on non-iOS hosts this
// file is dead code. The //go:build ignore constraint keeps `go build ./...`
// from treating build/ios as an incomplete main package (it has no main func).
// iOS builds use -tags ios + overlay and are unaffected.

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// modifyOptionsForIos is a no-op on non-iOS platforms
func modifyOptionsForIos(opts *application.Options) {
	// No modifications needed for non-iOS platforms
}
