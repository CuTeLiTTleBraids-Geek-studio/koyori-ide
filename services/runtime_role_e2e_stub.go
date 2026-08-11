//go:build !e2e

package services

// RuntimeRoleStatsForE2E is unavailable in normal builds.
func RuntimeRoleStatsForE2E(window *WindowService) RuntimeRoleStats {
	return window.runtimeRoleStats()
}
