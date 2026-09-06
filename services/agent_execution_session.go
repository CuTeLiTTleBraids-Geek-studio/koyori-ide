package services

import "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"

// executionSessionID resolves a domain-facing lifecycle ID to the runtime
// authority ID used by agentcore. Plan and Goal sessions deliberately use an
// opaque runtime ID; adapters must not send their renderer-visible logical ID
// directly to the capability runtime.
func (s *AgentService) executionSessionID(kind agentcore.SessionKind, id string) string {
	logical := lifecycleSessionID(kind, id)
	deps := executionDependenciesFor(s)
	deps.mu.RLock()
	lifecycle := deps.lifecycle
	deps.mu.RUnlock()
	if lifecycle == nil {
		return logical
	}
	return lifecycle.runtimeSessionID(kind, logical)
}
