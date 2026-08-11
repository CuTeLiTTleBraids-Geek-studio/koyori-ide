package services

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

// RuntimeRole identifies the least-privilege bootstrap contract for a native
// WebView. A renderer never selects this value directly; it presents a
// backend-issued, single-use token instead.
type RuntimeRole string

const (
	RuntimeRoleMain     RuntimeRole = "main"
	RuntimeRoleAI       RuntimeRole = "ai"
	RuntimeRoleSettings RuntimeRole = "settings"
	RuntimeRoleE2E      RuntimeRole = "e2e"
	RuntimeRoleMinimal  RuntimeRole = "minimal"
)

const (
	runtimeRoleQueryKey = "koyori-ide_runtime_role"
	runtimeRoleTokenTTL = 2 * time.Minute
)

type runtimeRoleToken struct {
	role     RuntimeRole
	issuedAt time.Time
}

// RuntimeRoleStats is an internal diagnostic snapshot used by packaged E2E
// probes. It intentionally has no renderer-facing binding surface.
type RuntimeRoleStats struct {
	IssuedMain       int `json:"issuedMain"`
	IssuedAI         int `json:"issuedAI"`
	IssuedSettings   int `json:"issuedSettings"`
	IssuedE2E        int `json:"issuedE2E"`
	ResolvedMain     int `json:"resolvedMain"`
	ResolvedAI       int `json:"resolvedAI"`
	ResolvedSettings int `json:"resolvedSettings"`
	ResolvedE2E      int `json:"resolvedE2E"`
	Rejected         int `json:"rejected"`
	AIWindowsCreated int `json:"aiWindowsCreated"`
	AIWindowsClosed  int `json:"aiWindowsClosed"`
}

func isRuntimeRole(role RuntimeRole) bool {
	switch role {
	case RuntimeRoleMain, RuntimeRoleAI, RuntimeRoleSettings, RuntimeRoleE2E:
		return true
	default:
		return false
	}
}

func (w *WindowService) issueRuntimeRoleToken(role RuntimeRole) string {
	if w == nil || !isRuntimeRole(role) {
		return ""
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		slog.Error("runtime role token generation failed", "role", role, "err", err)
		return ""
	}
	token := hex.EncodeToString(raw)
	generation := uint64(0)
	w.mu.Lock()
	if w.runtimeRoleTokens == nil {
		w.runtimeRoleTokens = make(map[string]runtimeRoleToken)
	}
	if w.workspaceContext != nil {
		generation = w.workspaceContext.Generation()
	}
	w.runtimeRoleTokens[token] = runtimeRoleToken{
		role:     role,
		issuedAt: time.Now(),
	}
	if w.runtimeRoleIssued == nil {
		w.runtimeRoleIssued = make(map[RuntimeRole]int)
	}
	w.runtimeRoleIssued[role]++
	issued := w.runtimeRoleIssued[role]
	w.mu.Unlock()
	slog.Info("runtime role token issued", "role", role, "generation", generation, "issued", issued)
	return token
}

// RuntimeRoleURL returns a URL containing a backend-minted role token. The
// token is placed before the hash so hash-router paths remain intact.
func RuntimeRoleURL(w *WindowService, role RuntimeRole, rawPath string) string {
	if w == nil {
		return rawPath
	}
	token := w.issueRuntimeRoleToken(role)
	if token == "" {
		return rawPath
	}
	parsed, err := url.Parse(rawPath)
	if err != nil {
		return rawPath
	}
	query := parsed.Query()
	query.Set(runtimeRoleQueryKey, token)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// ResolveRuntimeRole consumes a backend-issued role token exactly once. Any
// missing, forged, replayed, or expired token resolves to the minimal role and
// is logged as a rejection.
//
// The token is a one-time bootstrap credential for the window that carries it,
// not a workspace capability: the window role is chosen by the backend at
// window creation, and workspace authorization is enforced separately and
// generation-bound by WorkspaceContext (G03/G05). Deliberately NOT bound to the
// workspace generation: the main window token is minted before the persisted
// workspace is restored at startup, so binding it to generation would demote
// the main window to minimal on every startup with a saved workspace.
func (w *WindowService) ResolveRuntimeRole(token string) RuntimeRole {
	if w == nil {
		return RuntimeRoleMinimal
	}
	token = strings.TrimSpace(token)
	now := time.Now()
	w.mu.Lock()
	issued, ok := w.runtimeRoleTokens[token]
	if ok {
		// Delete before validation: even an expired token cannot be retried
		// after this call.
		delete(w.runtimeRoleTokens, token)
	}
	generation := uint64(0)
	if w.workspaceContext != nil {
		generation = w.workspaceContext.Generation()
	}
	if !ok || token == "" {
		w.runtimeRoleInvalid++
		w.mu.Unlock()
		slog.Warn("runtime role rejected", "reason", "missing-or-forged")
		return RuntimeRoleMinimal
	}
	if !isRuntimeRole(issued.role) {
		w.runtimeRoleInvalid++
		w.mu.Unlock()
		slog.Warn("runtime role rejected", "reason", "unknown-role")
		return RuntimeRoleMinimal
	}
	if now.Sub(issued.issuedAt) < 0 || now.Sub(issued.issuedAt) > runtimeRoleTokenTTL {
		w.runtimeRoleInvalid++
		w.mu.Unlock()
		slog.Warn("runtime role rejected", "reason", "expired", "role", issued.role)
		return RuntimeRoleMinimal
	}
	if w.runtimeRoleResolved == nil {
		w.runtimeRoleResolved = make(map[RuntimeRole]int)
	}
	w.runtimeRoleResolved[issued.role]++
	resolved := w.runtimeRoleResolved[issued.role]
	w.mu.Unlock()
	slog.Info("runtime role resolved", "role", issued.role, "generation", generation, "resolved", resolved)
	return issued.role
}

// runtimeRoleStats returns a consistent diagnostic snapshot without exposing
// mutable service state to callers.
func (w *WindowService) runtimeRoleStats() RuntimeRoleStats {
	if w == nil {
		return RuntimeRoleStats{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return RuntimeRoleStats{
		IssuedMain:       w.runtimeRoleIssued[RuntimeRoleMain],
		IssuedAI:         w.runtimeRoleIssued[RuntimeRoleAI],
		IssuedSettings:   w.runtimeRoleIssued[RuntimeRoleSettings],
		IssuedE2E:        w.runtimeRoleIssued[RuntimeRoleE2E],
		ResolvedMain:     w.runtimeRoleResolved[RuntimeRoleMain],
		ResolvedAI:       w.runtimeRoleResolved[RuntimeRoleAI],
		ResolvedSettings: w.runtimeRoleResolved[RuntimeRoleSettings],
		ResolvedE2E:      w.runtimeRoleResolved[RuntimeRoleE2E],
		Rejected:         w.runtimeRoleInvalid,
		AIWindowsCreated: w.aiWindowsCreated,
		AIWindowsClosed:  w.aiWindowsClosed,
	}
}
