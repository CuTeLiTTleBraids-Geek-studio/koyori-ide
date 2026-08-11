//go:build !windows

package services

import (
	"fmt"
	"os"
)

// ShowStartupError prints to stderr on non-Windows (console-capable).
func ShowStartupError(title, message string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, message)
}
