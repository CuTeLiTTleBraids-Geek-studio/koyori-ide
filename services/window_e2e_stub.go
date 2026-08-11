//go:build !e2e

package services

// ExecAIWindowJSForE2E is unavailable in normal builds.
func ExecAIWindowJSForE2E(_ *WindowService, _ string) bool { return false }
