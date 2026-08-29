import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  aiState,
  removeContextChip,
} from "@/stores/ai";
import {
  callMcpTool,
  clearMcpServerContext,
  clearStaleMcpContexts,
  connectMcpServer,
  deleteMcpServer,
  disconnectMcpServer,
  injectMcpPromptContext,
  injectMcpResourceContext,
  markMcpWorkspaceChanged,
  mcpContextInjectionBudget,
  mcpState,
  readMcpResource,
  refreshMcpServerContext,
  resetMcpStore,
  setMcpBackend,
  setMcpContextSweeper,
  toggleMcpServerEnabled,
  type McpBackend,
  type MCPCapabilitySnapshot,
} from "@/stores/mcp";

function fixtureCapabilities(overrides?: Partial<MCPCapabilitySnapshot>): MCPCapabilitySnapshot {
  return {
    protocolVersion: "2024-11-05",
    serverInfo: { name: "fs", version: "1.0" },
    capabilities: {
      tools: { state: "supported", declared: true, listChanged: true },
      resources: { state: "supported", declared: true, listChanged: true },
      prompts: { state: "supported", declared: true, listChanged: false },
      sampling: { state: "unsupported", declared: false },
      elicitation: { state: "unsupported", declared: false },
      logging: { state: "unsupported", declared: false },
    },
    serverName: "fs",
    workspaceRoot: "/ws",
    rootGeneration: 1,
    lifecycleGeneration: 3,
    run: 7,
    establishedAt: "2026-08-28T00:00:00Z",
    ...overrides,
  };
}

function createBackend(): McpBackend {
  return {
    listServers: vi.fn().mockResolvedValue([]),
    getServer: vi.fn(),
    saveServer: vi.fn(),
    setServerEnabled: vi.fn(),
    deleteServer: vi.fn(),
    connectServer: vi.fn(),
    disconnectServer: vi.fn(),
    listTools: vi.fn().mockResolvedValue([]),
    listAgentMCPTools: vi.fn().mockResolvedValue([]),
    serverCapabilities: vi.fn().mockResolvedValue(fixtureCapabilities()),
    listResources: vi.fn().mockResolvedValue([]),
    readResource: vi.fn().mockRejectedValue(new Error("read not configured")),
    listPrompts: vi.fn().mockResolvedValue([]),
    getPrompt: vi.fn().mockRejectedValue(new Error("get prompt not configured")),
  };
}

describe("callMcpTool", () => {
  beforeEach(() => {
    resetMcpStore();
    setMcpBackend(null);
  });

  it("keeps the legacy renderer execution path deny-only", async () => {
    const backend = createBackend();
    setMcpBackend(backend);
    const args = { path: "src/main.ts" };

    const result = await callMcpTool("workspace", "read_file", args);

    expect(result).toBeNull();
    expect(mcpState.error).toMatch(/unified Agent tool catalog/);
  });

  it("does not revive the old path when its backend mock would approve", async () => {
    const backend = createBackend();
    setMcpBackend(backend);

    await expect(callMcpTool("workspace", "write_file", {})).resolves.toBeNull();
    expect(mcpState.error).toMatch(/unified Agent tool catalog/);
  });

  it("reconciles committed server state after a failed delete teardown", async () => {
    const backend = createBackend();
    backend.deleteServer = vi.fn().mockRejectedValue(new Error("stop failed"));
    backend.listServers = vi.fn().mockResolvedValue([]);
    setMcpBackend(backend);
    mcpState.connected.srv = true;

    await expect(deleteMcpServer("srv")).resolves.toBe(false);
    expect(backend.listServers).toHaveBeenCalledTimes(1);
    expect(backend.listAgentMCPTools).toHaveBeenCalledTimes(1);
    expect(mcpState.servers).toEqual([]);
    expect(mcpState.connected.srv).toBeUndefined();
    expect(mcpState.error).toMatch(/stop failed/);
  });

  it("reconciles committed disabled state after a failed disable teardown", async () => {
    const backend = createBackend();
    backend.setServerEnabled = vi.fn().mockRejectedValue(new Error("disable failed"));
    backend.listServers = vi.fn().mockResolvedValue([
      { name: "srv", transport: "stdio", enabled: false },
    ]);
    setMcpBackend(backend);
    mcpState.connected.srv = true;

    await expect(toggleMcpServerEnabled("srv", false)).resolves.toBe(false);
    expect(backend.listServers).toHaveBeenCalledTimes(1);
    expect(backend.listAgentMCPTools).toHaveBeenCalledTimes(1);
    expect(mcpState.servers).toEqual([
      { name: "srv", transport: "stdio", enabled: false },
    ]);
    // The backend config snapshot proves that the server is disabled, so
    // stale connected state is removed even though teardown returned an
    // error.
    expect(mcpState.connected.srv).toBeUndefined();
    expect(mcpState.error).toMatch(/disable failed/);
  });

  it("clears connected state when backend detached before reporting disconnect failure", async () => {
    const backend = createBackend();
    backend.disconnectServer = vi.fn().mockRejectedValue(new Error("transport close failed"));
    backend.listServers = vi.fn().mockResolvedValue([
      { name: "srv", transport: "stdio", enabled: true },
    ]);
    setMcpBackend(backend);
    mcpState.connected.srv = true;

    await expect(disconnectMcpServer("srv")).resolves.toBe(false);
    expect(mcpState.connected.srv).toBeUndefined();
    expect(mcpState.error).toMatch(/transport close failed/);
  });

  it("does not retain a stale connection after a failed connect", async () => {
    const backend = createBackend();
    backend.connectServer = vi.fn().mockRejectedValue(new Error("start failed"));
    backend.listServers = vi.fn().mockResolvedValue([
      { name: "srv", transport: "stdio", enabled: true },
    ]);
    setMcpBackend(backend);
    mcpState.connected.srv = true;

    await expect(connectMcpServer("srv")).resolves.toBe(false);
    expect(mcpState.connected.srv).toBeUndefined();
    expect(mcpState.error).toMatch(/start failed/);
  });
});

describe("refreshMcpServerContext", () => {
  beforeEach(() => {
    resetMcpStore();
    setMcpBackend(null);
  });

  it("loads capabilities, resources, and prompts into per-family states", async () => {
    const backend = createBackend();
    backend.listResources = vi.fn().mockResolvedValue([
      { uri: "fixture://notes", name: "notes", mimeType: "text/plain" },
    ]);
    backend.listPrompts = vi.fn().mockResolvedValue([
      { name: "greet", description: "greets" },
    ]);
    setMcpBackend(backend);

    await refreshMcpServerContext("fs");

    const ctx = mcpState.serverContexts.fs;
    expect(ctx.status).toBe("loaded");
    expect(ctx.capabilities?.serverName).toBe("fs");
    expect(ctx.lifecycleGeneration).toBe(3);
    expect(ctx.resourcesStatus).toBe("loaded");
    expect(ctx.resources).toHaveLength(1);
    expect(ctx.promptsStatus).toBe("loaded");
    expect(ctx.prompts).toHaveLength(1);
  });

  it("reports empty instead of loaded when the server lists nothing", async () => {
    setMcpBackend(createBackend());

    await refreshMcpServerContext("fs");

    const ctx = mcpState.serverContexts.fs;
    expect(ctx.status).toBe("loaded");
    expect(ctx.resourcesStatus).toBe("empty");
    expect(ctx.promptsStatus).toBe("empty");
  });

  it("marks families unsupported without calling list when the server never declared them", async () => {
    const backend = createBackend();
    backend.serverCapabilities = vi.fn().mockResolvedValue(
      fixtureCapabilities({
        capabilities: {
          tools: { state: "supported", declared: true },
          resources: { state: "missing", declared: false },
          prompts: { state: "missing", declared: false },
          sampling: { state: "unsupported", declared: false },
          elicitation: { state: "unsupported", declared: false },
          logging: { state: "unsupported", declared: false },
        },
      }),
    );
    setMcpBackend(backend);

    await refreshMcpServerContext("fs");

    const ctx = mcpState.serverContexts.fs;
    expect(ctx.status).toBe("loaded");
    expect(ctx.resourcesStatus).toBe("unsupported");
    expect(ctx.resources).toEqual([]);
    expect(ctx.promptsStatus).toBe("unsupported");
    expect(backend.listResources).not.toHaveBeenCalled();
    expect(backend.listPrompts).not.toHaveBeenCalled();
  });

  it("keeps a diagnosable error instead of faking an empty success", async () => {
    const backend = createBackend();
    backend.listResources = vi.fn().mockRejectedValue(new Error("rpc error -32000: boom"));
    setMcpBackend(backend);

    await refreshMcpServerContext("fs");

    const ctx = mcpState.serverContexts.fs;
    expect(ctx.resourcesStatus).toBe("error");
    expect(ctx.resourcesError).toMatch(/rpc error -32000: boom/);
    // The prompts family is independent and must not be poisoned by the
    // resources failure.
    expect(ctx.promptsStatus).toBe("empty");
    expect(ctx.status).toBe("loaded");
  });

  it("fails the whole refresh when the capability snapshot is rejected", async () => {
    const backend = createBackend();
    backend.serverCapabilities = vi
      .fn()
      .mockRejectedValue(new Error("capability snapshot for server \"fs\" is bound to an older workspace or lifecycle generation"));
    setMcpBackend(backend);

    await refreshMcpServerContext("fs");

    const ctx = mcpState.serverContexts.fs;
    expect(ctx.status).toBe("error");
    expect(ctx.error).toMatch(/older workspace or lifecycle generation/);
    expect(backend.listResources).not.toHaveBeenCalled();
  });
});

describe("MCP context injection", () => {
  beforeEach(() => {
    resetMcpStore();
    setMcpBackend(null);
    aiState.contextChips = [];
  });

  function connectFixture(backend: McpBackend): void {
    setMcpBackend(backend);
    mcpState.connected.fs = true;
    mcpState.serverContexts.fs = {
      status: "loaded",
      resourcesStatus: "loaded",
      resources: [],
      resourcesError: null,
      promptsStatus: "loaded",
      prompts: [],
      promptsError: null,
      capabilities: fixtureCapabilities(),
      error: null,
      lifecycleGeneration: 3,
    };
  }

  it("injects a resource with full provenance and deduplicates by source", async () => {
    const backend = createBackend();
    backend.readResource = vi.fn().mockResolvedValueOnce({
      server: "fs",
      uri: "fixture://notes",
      contents: [{ uri: "fixture://notes", mimeType: "text/plain", text: "first body" }],
      rootGeneration: 1,
      lifecycleGeneration: 3,
    }).mockResolvedValueOnce({
      server: "fs",
      uri: "fixture://notes",
      contents: [{ uri: "fixture://notes", mimeType: "text/plain", text: "second body" }],
      rootGeneration: 1,
      lifecycleGeneration: 4,
    });
    connectFixture(backend);

    await expect(injectMcpResourceContext("fs", "fixture://notes")).resolves.toBe(true);
    await expect(injectMcpResourceContext("fs", "fixture://notes")).resolves.toBe(true);

    const chips = aiState.contextChips.filter((chip) => chip.kind === "mcp");
    expect(chips).toHaveLength(1);
    expect(chips[0].id).toBe("mcp-res:fs:fixture://notes");
    expect(chips[0].mcpServer).toBe("fs");
    expect(chips[0].mcpUri).toBe("fixture://notes");
    expect(chips[0].mcpGeneration).toBe(4);
    expect(chips[0].content).toBe("second body");
  });

  it("injects prompt messages with role markers preserved", async () => {
    const backend = createBackend();
    backend.getPrompt = vi.fn().mockResolvedValue({
      server: "fs",
      prompt: "greet",
      messages: [
        { role: "user", content: { type: "text", text: "hello" } },
        { role: "assistant", content: { type: "text", text: "hi" } },
      ],
      rootGeneration: 1,
      lifecycleGeneration: 3,
    });
    connectFixture(backend);

    await expect(injectMcpPromptContext("fs", "greet")).resolves.toBe(true);

    const chip = aiState.contextChips.find((candidate) => candidate.id === "mcp-prompt:fs:greet");
    expect(chip?.mcpServer).toBe("fs");
    expect(chip?.mcpPrompt).toBe("greet");
    expect(chip?.content).toContain("[user]\nhello");
    expect(chip?.content).toContain("[assistant]\nhi");
  });

  it("truncates oversized content at the injection budget with an explicit marker", async () => {
    const backend = createBackend();
    backend.readResource = vi.fn().mockResolvedValue({
      server: "fs",
      uri: "fixture://huge",
      contents: [{ uri: "fixture://huge", text: "x".repeat(mcpContextInjectionBudget + 500) }],
      rootGeneration: 1,
      lifecycleGeneration: 3,
    });
    connectFixture(backend);

    await expect(injectMcpResourceContext("fs", "fixture://huge")).resolves.toBe(true);

    const chip = aiState.contextChips.find((candidate) => candidate.id === "mcp-res:fs:fixture://huge");
    expect(chip?.content?.length).toBeLessThanOrEqual(mcpContextInjectionBudget + 200);
    expect(chip?.content).toContain("truncated at the");
  });

  it("refuses injection when the server is not connected or the context is stale", async () => {
    const backend = createBackend();
    backend.readResource = vi.fn().mockResolvedValue({
      server: "fs",
      uri: "fixture://notes",
      contents: [{ uri: "fixture://notes", text: "body" }],
      rootGeneration: 1,
      lifecycleGeneration: 3,
    });
    setMcpBackend(backend);
    mcpState.connected.fs = true;

    // No discovery state at all: injection is still allowed while connected.
    await expect(injectMcpResourceContext("fs", "fixture://notes")).resolves.toBe(true);

    mcpState.serverContexts.fs = {
      status: "stale",
      resourcesStatus: "stale",
      resources: [],
      resourcesError: null,
      promptsStatus: "stale",
      prompts: [],
      promptsError: null,
      capabilities: null,
      error: null,
      lifecycleGeneration: 3,
    };
    await expect(injectMcpResourceContext("fs", "fixture://notes")).resolves.toBe(false);
    expect(mcpState.error).toMatch(/stale; refresh before injecting/);

    delete mcpState.connected.fs;
    await expect(injectMcpResourceContext("fs", "fixture://notes")).resolves.toBe(false);
    expect(mcpState.error).toMatch(/not connected; context injection refused/);
    expect(backend.readResource).toHaveBeenCalledTimes(1);
  });

  it("marks contexts stale and sweeps injected chips on server disconnect", async () => {
    const swept: Array<string | null> = [];
    setMcpContextSweeper((server) => {
      swept.push(server);
      for (const chip of [...aiState.contextChips]) {
        if (chip.kind === "mcp" && (server === null || chip.mcpServer === server)) {
          removeContextChip(chip.id);
        }
      }
    });
    const backend = createBackend();
    backend.readResource = vi.fn().mockResolvedValue({
      server: "fs",
      uri: "fixture://notes",
      contents: [{ uri: "fixture://notes", text: "body" }],
      rootGeneration: 1,
      lifecycleGeneration: 3,
    });
    connectFixture(backend);
    await injectMcpResourceContext("fs", "fixture://notes");
    expect(aiState.contextChips.some((chip) => chip.mcpServer === "fs")).toBe(true);

    backend.listServers = vi.fn().mockResolvedValue([
      { name: "fs", transport: "stdio", enabled: true },
    ]);
    await disconnectMcpServer("fs");

    expect(mcpState.serverContexts.fs?.status).toBe("stale");
    expect(mcpState.serverContexts.fs?.resources).toEqual([]);
    expect(aiState.contextChips.some((chip) => chip.mcpServer === "fs")).toBe(false);
    expect(swept).toContain("fs");
  });

  it("stales every server and sweeps all MCP chips on a workspace switch", async () => {
    const swept: Array<string | null> = [];
    setMcpContextSweeper((server) => {
      swept.push(server);
      for (const chip of [...aiState.contextChips]) {
        if (chip.kind === "mcp" && (server === null || chip.mcpServer === server)) {
          removeContextChip(chip.id);
        }
      }
    });
    const backend = createBackend();
    backend.readResource = vi.fn().mockResolvedValue({
      server: "fs",
      uri: "fixture://notes",
      contents: [{ uri: "fixture://notes", text: "body" }],
      rootGeneration: 1,
      lifecycleGeneration: 3,
    });
    connectFixture(backend);
    mcpState.serverContexts.other = {
      status: "loaded",
      resourcesStatus: "empty",
      resources: [],
      resourcesError: null,
      promptsStatus: "empty",
      prompts: [],
      promptsError: null,
      capabilities: fixtureCapabilities({ serverName: "other" }),
      error: null,
      lifecycleGeneration: 2,
    };
    await injectMcpResourceContext("fs", "fixture://notes");
    expect(aiState.contextChips.some((chip) => chip.kind === "mcp")).toBe(true);

    markMcpWorkspaceChanged("/next-workspace");

    expect(mcpState.contextsWorkspaceRoot).toBe("/next-workspace");
    expect(mcpState.serverContexts.fs?.status).toBe("stale");
    expect(mcpState.serverContexts.other?.status).toBe("stale");
    expect(aiState.contextChips.some((chip) => chip.kind === "mcp")).toBe(false);
    expect(swept).toContain(null);

    // The same root again is a no-op (no double sweep, no state change).
    const sweepCount = swept.length;
    markMcpWorkspaceChanged("/next-workspace");
    expect(swept).toHaveLength(sweepCount);
  });

  it("clears stale contexts and single-server contexts on demand", async () => {
    const backend = createBackend();
    connectFixture(backend);
    mcpState.serverContexts.fs.status = "stale";
    mcpState.serverContexts.kept = {
      status: "loaded",
      resourcesStatus: "loaded",
      resources: [],
      resourcesError: null,
      promptsStatus: "loaded",
      prompts: [],
      promptsError: null,
      capabilities: fixtureCapabilities({ serverName: "kept" }),
      error: null,
      lifecycleGeneration: 1,
    };

    clearStaleMcpContexts();
    expect(mcpState.serverContexts.fs).toBeUndefined();
    expect(mcpState.serverContexts.kept).toBeDefined();

    clearMcpServerContext("kept");
    expect(mcpState.serverContexts.kept).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// P1-01: refreshMcpServerContext stale 竞态守卫
// ---------------------------------------------------------------------------

describe("refreshMcpServerContext stale 守卫", () => {
  beforeEach(() => {
    resetMcpStore();
    setMcpBackend(null);
  });

  it("重复刷新后迟到的旧 capability 快照不回写", async () => {
    const backend = createBackend();
    let resolveOld!: (v: MCPCapabilitySnapshot) => void;
    (backend.serverCapabilities as any)
      .mockImplementationOnce(
        () => new Promise<MCPCapabilitySnapshot>((resolve) => { resolveOld = resolve; }),
      )
      .mockResolvedValueOnce(fixtureCapabilities({ lifecycleGeneration: 9, run: 11 }));
    setMcpBackend(backend);

    const stale = refreshMcpServerContext("fs");
    await refreshMcpServerContext("fs");
    const ctx = mcpState.serverContexts.fs;
    expect(ctx.status).toBe("loaded");
    expect(ctx.lifecycleGeneration).toBe(9);

    resolveOld(fixtureCapabilities({ lifecycleGeneration: 3, run: 7 }));
    await stale;

    expect(ctx.lifecycleGeneration).toBe(9);
    expect(ctx.status).toBe("loaded");
    expect(ctx.error).toBeNull();
  });

  it("迟到的旧刷新失败也不回写 error 状态", async () => {
    const backend = createBackend();
    let rejectOld!: (e: Error) => void;
    (backend.serverCapabilities as any)
      .mockImplementationOnce(
        () => new Promise<MCPCapabilitySnapshot>((_, reject) => { rejectOld = reject; }),
      )
      .mockResolvedValue(fixtureCapabilities({ lifecycleGeneration: 9 }));
    setMcpBackend(backend);

    const stale = refreshMcpServerContext("fs");
    await refreshMcpServerContext("fs");

    rejectOld(new Error("old transport died"));
    await stale;

    const ctx = mcpState.serverContexts.fs;
    expect(ctx.status).toBe("loaded");
    expect(ctx.error).toBeNull();
  });
});
