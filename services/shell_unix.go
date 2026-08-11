//go:build !windows

package services

import "os"

func defaultShell() []string {
	shell := os.Getenv("SHELL")
	// HIGH-01: validate $SHELL against the whitelist. If the user's login
	// shell is a non-whitelisted binary (e.g. fish), fall back to bash so a
	// non-whitelisted binary is never launched as a terminal session.
	if shell == "" || !isAllowedShell(shell) {
		shell = "bash"
	}
	return []string{shell}
}
