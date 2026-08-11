//go:build e2e

package services

// RuntimeRoleStatsForE2E exposes an immutable diagnostic snapshot only to the
// opt-in packaged E2E harness. It is not a Wails service method.
func RuntimeRoleStatsForE2E(window *WindowService) RuntimeRoleStats {
	return window.runtimeRoleStats()
}
