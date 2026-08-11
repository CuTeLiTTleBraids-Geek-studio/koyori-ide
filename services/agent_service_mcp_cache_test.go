package services

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMCPTollLister is a test double for mcpToolLister that counts
// ListAgentMCPTools calls and returns a configurable tool list.
type fakeMCPToolLister struct {
	calls atomic.Int64
	tools []AgentMCPTool
	err   error
}

func (f *fakeMCPToolLister) ListAgentMCPTools(_ context.Context) ([]AgentMCPTool, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	// Return a copy to prevent callers from mutating the original.
	out := make([]AgentMCPTool, len(f.tools))
	copy(out, f.tools)
	return out, nil
}

// TestCheckMCPCommand_CacheHit verifies that two checkMCPCommand calls
// within the TTL result in only one ListAgentMCPTools fetch (M-7).
func TestCheckMCPCommand_CacheHit(t *testing.T) {
	lister := &fakeMCPToolLister{
		tools: []AgentMCPTool{
			{Server: "fs", Tool: "read", RiskLevel: RiskElevated},
		},
	}
	svc := &AgentService{
		mcpLister:   lister,
		mcpCacheTTL: 30 * time.Second,
	}

	// First call — should fetch (cache miss).
	check1 := svc.checkMCPCommand("mcp.fs.read")
	if check1.RiskLevel != RiskElevated {
		t.Errorf("first call: expected RiskElevated, got %q", check1.RiskLevel)
	}
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("expected 1 fetch after first call, got %d", got)
	}

	// Second call within TTL — should use cache (no new fetch).
	check2 := svc.checkMCPCommand("mcp.fs.read")
	if check2.RiskLevel != RiskElevated {
		t.Errorf("second call: expected RiskElevated, got %q", check2.RiskLevel)
	}
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("expected 1 fetch after second call (cache hit), got %d", got)
	}
}

// TestCheckMCPCommand_CacheExpiry verifies that after the TTL expires,
// checkMCPCommand fetches a fresh tool list (M-7).
func TestCheckMCPCommand_CacheExpiry(t *testing.T) {
	lister := &fakeMCPToolLister{
		tools: []AgentMCPTool{
			{Server: "fs", Tool: "read", RiskLevel: RiskElevated},
		},
	}
	svc := &AgentService{
		mcpLister:   lister,
		mcpCacheTTL: 50 * time.Millisecond, // very short TTL for testing
	}

	// First call — cache miss, fetches.
	_ = svc.checkMCPCommand("mcp.fs.read")
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("expected 1 fetch after first call, got %d", got)
	}

	// Wait for TTL to expire.
	time.Sleep(100 * time.Millisecond)

	// Second call after TTL — cache expired, should fetch again.
	_ = svc.checkMCPCommand("mcp.fs.read")
	if got := lister.calls.Load(); got != 2 {
		t.Fatalf("expected 2 fetches after TTL expiry, got %d", got)
	}
}

// TestCheckMCPCommand_InvalidateCache verifies that InvalidateMCPCache
// forces a fresh fetch on the next call (M-7).
func TestCheckMCPCommand_InvalidateCache(t *testing.T) {
	lister := &fakeMCPToolLister{
		tools: []AgentMCPTool{
			{Server: "fs", Tool: "read", RiskLevel: RiskElevated},
		},
	}
	svc := &AgentService{
		mcpLister:   lister,
		mcpCacheTTL: 30 * time.Second,
	}

	// First call — fetches.
	_ = svc.checkMCPCommand("mcp.fs.read")
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("expected 1 fetch, got %d", got)
	}

	// Invalidate — next call should fetch again.
	svc.InvalidateMCPCache()
	_ = svc.checkMCPCommand("mcp.fs.read")
	if got := lister.calls.Load(); got != 2 {
		t.Fatalf("expected 2 fetches after invalidation, got %d", got)
	}
}

// TestCheckMCPCommand_ToolNotFound verifies that an unknown tool is
// blocked and the cache is still used (no extra fetch).
func TestCheckMCPCommand_ToolNotFound(t *testing.T) {
	lister := &fakeMCPToolLister{
		tools: []AgentMCPTool{
			{Server: "fs", Tool: "read", RiskLevel: RiskElevated},
		},
	}
	svc := &AgentService{
		mcpLister:   lister,
		mcpCacheTTL: 30 * time.Second,
	}

	check := svc.checkMCPCommand("mcp.fs.unknown")
	if !check.Blocked {
		t.Error("expected unknown tool to be blocked")
	}
	if check.RiskLevel != RiskDangerous {
		t.Errorf("expected RiskDangerous for unknown tool, got %q", check.RiskLevel)
	}
}

// TestAgentService_M7_MCPCacheTTL 验证 MCP 工具列表的 TTL 缓存 (M-7):
// TTL 内的第二次调用命中缓存(不触发额外拉取),TTL 过期后缓存刷新
// (再次拉取)。使用短 TTL + time.Sleep 保持测试快速。
func TestAgentService_M7_MCPCacheTTL(t *testing.T) {
	lister := &fakeMCPToolLister{
		tools: []AgentMCPTool{
			{Server: "fs", Tool: "read", RiskLevel: RiskElevated},
		},
	}
	svc := &AgentService{
		mcpLister:   lister,
		mcpCacheTTL: 50 * time.Millisecond, // 短 TTL 加速测试
	}

	// 第一次调用 — 缓存未命中,触发拉取。
	check1 := svc.checkMCPCommand("mcp.fs.read")
	if check1.RiskLevel != RiskElevated {
		t.Errorf("first call: expected RiskElevated, got %q", check1.RiskLevel)
	}
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("expected 1 fetch after first call, got %d", got)
	}

	// TTL 内第二次调用 — 命中缓存,不应触发额外拉取。
	check2 := svc.checkMCPCommand("mcp.fs.read")
	if check2.RiskLevel != RiskElevated {
		t.Errorf("second call: expected RiskElevated, got %q", check2.RiskLevel)
	}
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("expected 1 fetch after second call (cache hit), got %d", got)
	}

	// 等待 TTL 过期。
	time.Sleep(100 * time.Millisecond)

	// TTL 过期后第三次调用 — 缓存刷新,应再次拉取。
	check3 := svc.checkMCPCommand("mcp.fs.read")
	if check3.RiskLevel != RiskElevated {
		t.Errorf("third call: expected RiskElevated, got %q", check3.RiskLevel)
	}
	if got := lister.calls.Load(); got != 2 {
		t.Fatalf("expected 2 fetches after TTL expiry (cache refresh), got %d", got)
	}
}
