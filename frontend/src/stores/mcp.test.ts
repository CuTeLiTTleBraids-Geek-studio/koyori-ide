import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  callMcpTool,
  mcpState,
  setMcpBackend,
  type McpBackend,
  type MCPToolResult,
} from "@/stores/mcp";

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
    requestToolApproval: vi.fn().mockResolvedValue("tool-approval"),
    executeApprovedTool: vi.fn().mockResolvedValue({ content: [], isError: false }),
  };
}

describe("callMcpTool", () => {
  beforeEach(() => setMcpBackend(null));

  it("requests a capability before executing the exact tool call", async () => {
    const backend = createBackend();
    setMcpBackend(backend);
    const args = { path: "src/main.ts" };

    const result = await callMcpTool("workspace", "read_file", args);

    expect(backend.requestToolApproval).toHaveBeenCalledWith(
      "workspace",
      "read_file",
      args,
    );
    expect(backend.executeApprovedTool).toHaveBeenCalledWith(
      "workspace",
      "read_file",
      args,
      "tool-approval",
    );
    expect(
      vi.mocked(backend.requestToolApproval).mock.invocationCallOrder[0],
    ).toBeLessThan(
      vi.mocked(backend.executeApprovedTool).mock.invocationCallOrder[0],
    );
    expect(result).toEqual<MCPToolResult>({ content: [], isError: false });
  });

  it("does not execute when approval fails", async () => {
    const backend = createBackend();
    vi.mocked(backend.requestToolApproval).mockRejectedValue(new Error("denied"));
    setMcpBackend(backend);

    await expect(callMcpTool("workspace", "write_file", {})).resolves.toBeNull();
    expect(mcpState.error).toBe("denied");
    expect(backend.executeApprovedTool).not.toHaveBeenCalled();
  });
});
