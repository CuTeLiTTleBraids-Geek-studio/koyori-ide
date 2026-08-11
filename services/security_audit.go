package services

import "log/slog"

// securityAudit emits security-relevant operations in a consistent,
// machine-readable shape. Callers must only pass non-sensitive metadata.
func securityAudit(event, outcome string, attrs ...any) {
	args := make([]any, 0, 4+len(attrs))
	args = append(args, "security_audit", true, "event", event, "outcome", outcome)
	args = append(args, attrs...)
	slog.Info("security audit", args...)
}
