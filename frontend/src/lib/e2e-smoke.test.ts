/**
 * prompt-5 Task J — lightweight E2E-style smoke (no real Wails/app window).
 * Exercises the core store flow: open project path → open file → send terminal
 * echo path via mocks. Blocks silent breakage of the primary IDE loop.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";

const { readFileMock, writeFileMock, listDirMock, execMock } = vi.hoisted(() => ({
  readFileMock: vi.fn().mockResolvedValue("package.json content"),
  writeFileMock: vi.fn().mockResolvedValue(undefined),
  listDirMock: vi.fn().mockResolvedValue([
    { name: "package.json", path: "/proj/package.json", isDir: false, size: 10, modified: 0 },
  ]),
  execMock: vi.fn().mockResolvedValue({
    command: "echo ok",
    exitCode: 0,
    stdout: "ok\n",
    stderr: "",
    durationMs: 1,
  }),
}));

vi.mock("@/api/services", () => ({
  fileService: {
    readFile: readFileMock,
    writeFile: writeFileMock,
    listDirectory: listDirMock,
  },
  agentService: {
    requestCommandApproval: vi.fn().mockResolvedValue("smoke-approval"),
    executeApprovedCommand: execMock,
    checkCommand: vi.fn().mockResolvedValue({ riskLevel: "elevated", blockReason: "" }),
  },
  searchService: {
    search: vi.fn().mockResolvedValue([]),
  },
  aiService: {
    setConfig: vi.fn(),
    startStream: vi.fn(),
    stopStream: vi.fn(),
    getAgentSystemPrompt: vi.fn().mockResolvedValue("agent"),
  },
  conversationService: {
    generateConversationID: vi.fn(),
    saveConversation: vi.fn(),
  },
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifyWarning: vi.fn(),
  notifySuccess: vi.fn(),
}));

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn(() => () => undefined), Emit: vi.fn() },
}));

import { openFileFromPath, editorState, updateContent } from "@/stores/editor";
import { appState } from "@/stores/app";
import { executeToolCall, type ToolCall } from "@/stores/agent";

describe("e2e smoke (prompt-5 Task J)", () => {
  beforeEach(() => {
    editorState.openFiles = [];
    editorState.activeFilePath = null;
    appState.currentProject = "/proj";
    readFileMock.mockClear();
    execMock.mockClear();
  });

  it("open project file → edit buffer → run echo tool", async () => {
    // 1. Open file (like FileTree select)
    await openFileFromPath("/proj/package.json");
    expect(editorState.openFiles).toHaveLength(1);
    expect(editorState.openFiles[0].content).toBe("package.json content");

    // 2. Edit in memory
    expect(updateContent("/proj/package.json", '{ "name": "demo" }')).toBe(true);
    expect(editorState.openFiles[0].isDirty).toBe(true);

    // 3. Terminal-like agent run (echo)
    const tc: ToolCall = {
      id: "smoke-1",
      kind: "run",
      target: "echo ok",
      status: "pending",
    };
    const obs = await executeToolCall(tc);
    expect(execMock).toHaveBeenCalledWith("echo ok", "/proj", "smoke-approval");
    expect(obs).toMatch(/Exit code:\s*0/i);
  });
});
