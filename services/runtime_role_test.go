package services

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func tokenFromRoleURL(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse role URL: %v", err)
	}
	token := parsed.Query().Get(runtimeRoleQueryKey)
	if token == "" {
		t.Fatal("role URL did not contain a token")
	}
	return token
}

func TestRuntimeRoleTokenSingleUseAndForgeryFailClosed(t *testing.T) {
	window := NewWindowServiceWithWorkspaceContext(NewWorkspaceContext())
	token := tokenFromRoleURL(t, RuntimeRoleURL(window, RuntimeRoleAI, "/#/ai-window"))
	if got := window.ResolveRuntimeRole(token); got != RuntimeRoleAI {
		t.Fatalf("first resolve = %q, want %q", got, RuntimeRoleAI)
	}
	if got := window.ResolveRuntimeRole(token); got != RuntimeRoleMinimal {
		t.Fatalf("replay resolve = %q, want %q", got, RuntimeRoleMinimal)
	}
	if got := window.ResolveRuntimeRole(strings.Repeat("f", 64)); got != RuntimeRoleMinimal {
		t.Fatalf("forged resolve = %q, want %q", got, RuntimeRoleMinimal)
	}
	stats := window.runtimeRoleStats()
	if stats.ResolvedAI != 1 || stats.Rejected != 2 {
		t.Fatalf("unexpected role stats: %+v", stats)
	}
}

func TestRuntimeRoleTokenExpiryAndWorkspaceSwitchValidity(t *testing.T) {
	ctx := NewWorkspaceContext()
	window := NewWindowServiceWithWorkspaceContext(ctx)
	token := tokenFromRoleURL(t, RuntimeRoleURL(window, RuntimeRoleMain, "/"))
	window.mu.Lock()
	issued := window.runtimeRoleTokens[token]
	issued.issuedAt = time.Now().Add(-runtimeRoleTokenTTL - time.Second)
	window.runtimeRoleTokens[token] = issued
	window.mu.Unlock()
	if got := window.ResolveRuntimeRole(token); got != RuntimeRoleMinimal {
		t.Fatalf("expired resolve = %q, want %q", got, RuntimeRoleMinimal)
	}

	// P9-G06 AC3: the role token is a one-time bootstrap credential for the
	// window that carries it, not a workspace capability. The main window token
	// is minted before the persisted workspace is restored at startup, so an
	// unconsumed token must stay valid across a workspace switch. Workspace
	// authorization is enforced separately and generation-bound by
	// WorkspaceContext (G03/G05), not by the role token.
	if err := ctx.Set(t.TempDir()); err != nil {
		t.Fatalf("set workspace: %v", err)
	}
	aiToken := tokenFromRoleURL(t, RuntimeRoleURL(window, RuntimeRoleAI, "/#/ai-window"))
	if err := ctx.Set(t.TempDir()); err != nil {
		t.Fatalf("switch workspace: %v", err)
	}
	if got := window.ResolveRuntimeRole(aiToken); got != RuntimeRoleAI {
		t.Fatalf("post-switch resolve = %q, want %q", got, RuntimeRoleAI)
	}
}

func TestRuntimeRoleURLPreservesHashAndExistingQuery(t *testing.T) {
	window := NewWindowServiceWithWorkspaceContext(NewWorkspaceContext())
	raw := RuntimeRoleURL(window, RuntimeRoleSettings, "/settings?tab=ai#/settings")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	if parsed.Fragment != "/settings" || parsed.Query().Get("tab") != "ai" {
		t.Fatalf("role URL changed route/query: %q", raw)
	}
	if got := window.ResolveRuntimeRole(parsed.Query().Get(runtimeRoleQueryKey)); got != RuntimeRoleSettings {
		t.Fatalf("settings resolve = %q, want %q", got, RuntimeRoleSettings)
	}
}
