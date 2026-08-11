import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn() },
}));

vi.mock("@/api/services", () => ({
  fileService: {
    readFile: vi.fn(),
    writeFile: vi.fn(),
  },
  searchService: {
    search: vi.fn(),
  },
  agentService: {
    requestCommandApproval: vi.fn().mockResolvedValue("approval-token"),
    executeApprovedCommand: vi.fn(),
    checkCommand: vi.fn(),
    // GOAL-P1-02: budget methods added to the facade — mock so refreshToolBudget
    // does not throw during tests that set toolCallCount directly.
    getToolBudget: vi.fn().mockResolvedValue({ spent: 0, limit: 20, remaining: 20, exhausted: false, timedOut: false, epoch: 1, startedAt: "", expiresAt: "" }),
    startNewToolBudgetEpoch: vi.fn(),
    requestWriteApproval: vi.fn().mockResolvedValue("write-token"),
    executeApprovedWrite: vi.fn(),
  },
  aiService: {
    getAgentSystemPrompt: vi.fn(),
  },
}));

vi.mock("@/stores/app", () => ({
  appState: {
    currentProject: "/proj",
    toolApprovalConfig: {} as Record<string, string>,
    // Plan 54: agent prompt override (empty = use built-in).
    aiAgentSystemPrompt: "",
  },
}));

vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifySuccess: vi.fn(),
  notifyWarning: vi.fn(),
}));

vi.mock("@/stores/ai", () => ({
  sendMessage: vi.fn().mockResolvedValue(undefined),
}));

import {
  agentState,
  isAgentMode,
  hasPendingToolCalls,
  setMode,
  toggleMode,
  parseToolCalls,
  extractToolCallBlocks,
  executeToolCall,
  approveToolCall,
  rejectToolCall,
  clearPendingToolCalls,
  onAssistantFinished,
  getAgentSystemPrompt,
  approveAndFeed,
  rejectAndFeed,
  listRegisteredTools,
  registerTool,
  unregisterTool,
  getRegisteredTools,
  getToolSchemaList,
  maxIterationsReached,
  getApprovalPolicy,
  shouldAutoApprove,
  applyApprovalPolicy,
  __resetAgentPromptCacheForTests,
  parseNativeToolCalls,
  buildNativeToolDefs,
  onNativeToolCalls,
  initAgentPendingSyncListener,
  cleanupAgentPendingSyncListener,
  handleAgentPendingUpdatedEvent,
  type ToolCall,
} from "./agent";
import { Events } from "@wailsio/runtime";
import { fileService, searchService, agentService, aiService } from "@/api/services";
import { appState } from "@/stores/app";
import { pushOutput } from "@/stores/output";
import { notifyWarning } from "@/lib/notifications";

describe("agent store", () => {
  beforeEach(() => {
    cleanupAgentPendingSyncListener();
    vi.clearAllMocks();
    agentState.mode = "chat";
    agentState.pendingToolCalls = [];
    agentState.toolCallCount = 0;
    // GOAL-P1-02: the backend budget status must be reset too. Leaving a status
    // from a previous test makes `maxIterationsReached` read that instead of the
    // local counter these tests set, which silently breaks their premise.
    agentState.budget = null;
    // Reset approval policy config between tests (Plan 47).
    appState.toolApprovalConfig = {};
    // Plan 54: reset the agent prompt override between tests.
    appState.aiAgentSystemPrompt = "";
    __resetAgentPromptCacheForTests();
  });

  afterEach(cleanupAgentPendingSyncListener);

  describe("pending sync handler lifecycle", () => {
    it("is enabled once, disabled by cleanup, and does not register Wails listeners", () => {
      initAgentPendingSyncListener();
      initAgentPendingSyncListener();
      expect(Events.On).not.toHaveBeenCalled();

      handleAgentPendingUpdatedEvent({ data: { origin: "peer", count: 1 } });
      expect(notifyWarning).toHaveBeenCalledTimes(1);
      expect(agentState.pendingToolCalls).toHaveLength(0);

      cleanupAgentPendingSyncListener();
      cleanupAgentPendingSyncListener();
      handleAgentPendingUpdatedEvent({ data: { origin: "peer", count: 1 } });
      expect(notifyWarning).toHaveBeenCalledTimes(1);

      initAgentPendingSyncListener();
      expect(Events.On).not.toHaveBeenCalled();
    });

    it("keeps approvals only in the originating window", () => {
      initAgentPendingSyncListener();
      agentState.pendingToolCalls = [
        { id: "local", kind: "write", target: "local.ts", status: "pending" },
      ];

      handleAgentPendingUpdatedEvent({
        data: {
          origin: "peer-window",
          count: 2,
          kinds: ["run", "write"],
          approveOnlyOnOrigin: true,
        },
      });

      expect(agentState.pendingToolCalls).toEqual([
        { id: "local", kind: "write", target: "local.ts", status: "pending" },
      ]);
      expect(notifyWarning).toHaveBeenCalledTimes(1);
    });
  });

  describe("mode state", () => {
    it("starts in chat mode", () => {
      expect(isAgentMode.value).toBe(false);
    });

    it("setMode switches to agent", () => {
      setMode("agent");
      expect(isAgentMode.value).toBe(true);
      expect(agentState.mode).toBe("agent");
    });

    it("toggleMode flips between modes", () => {
      expect(agentState.mode).toBe("chat");
      toggleMode();
      expect(agentState.mode).toBe("agent");
      toggleMode();
      expect(agentState.mode).toBe("chat");
    });

    it("toggleMode clears pending tool calls", () => {
      agentState.pendingToolCalls = [
        { id: "x", kind: "read", target: "a.txt", status: "pending" },
      ];
      toggleMode();
      expect(agentState.pendingToolCalls).toEqual([]);
    });
  });

  describe("hasPendingToolCalls computed", () => {
    it("is false when no pending calls", () => {
      expect(hasPendingToolCalls.value).toBe(false);
    });
    it("is true when at least one pending call exists", () => {
      agentState.pendingToolCalls = [
        { id: "x", kind: "read", target: "a", status: "pending" },
      ];
      expect(hasPendingToolCalls.value).toBe(true);
    });
    it("is false when calls are all executed", () => {
      agentState.pendingToolCalls = [
        { id: "x", kind: "read", target: "a", status: "executed" },
      ];
      expect(hasPendingToolCalls.value).toBe(false);
    });
  });

  describe("parseToolCalls", () => {
    it("returns empty for empty message", () => {
      expect(parseToolCalls("")).toEqual([]);
    });

    it("returns empty when no tool-call blocks present", () => {
      const msg = "Here is some code:\n```ts\nconst x = 1;\n```\nDone.";
      expect(parseToolCalls(msg)).toEqual([]);
    });

    it("parses a read tool call", () => {
      const msg = "Let me read the file:\n```\nread: src/main.ts\n```";
      const calls = parseToolCalls(msg);
      expect(calls).toHaveLength(1);
      expect(calls[0].kind).toBe("read");
      expect(calls[0].target).toBe("src/main.ts");
      expect(calls[0].status).toBe("pending");
      expect(calls[0].content).toBeUndefined();
    });

    it("parses a write tool call with content", () => {
      const msg =
        "Creating the file:\n```\nwrite: src/new.ts\nconsole.log('hi');\n```";
      const calls = parseToolCalls(msg);
      expect(calls).toHaveLength(1);
      expect(calls[0].kind).toBe("write");
      expect(calls[0].target).toBe("src/new.ts");
      expect(calls[0].content).toBe("console.log('hi');");
    });

    it("parses a run tool call", () => {
      const msg = "Running tests:\n```\nrun: go test ./...\n```";
      const calls = parseToolCalls(msg);
      expect(calls).toHaveLength(1);
      expect(calls[0].kind).toBe("run");
      expect(calls[0].target).toBe("go test ./...");
    });

    it("parses a search tool call", () => {
      const msg = "Searching:\n```\nsearch: TODO\n```";
      const calls = parseToolCalls(msg);
      expect(calls).toHaveLength(1);
      expect(calls[0].kind).toBe("search");
      expect(calls[0].target).toBe("TODO");
    });

    it("parses multiple tool calls in one message", () => {
      const msg =
        "```\nread: a.ts\n```\nSome text in between.\n```\nrun: ls\n```";
      const calls = parseToolCalls(msg);
      expect(calls).toHaveLength(2);
      expect(calls[0].target).toBe("a.ts");
      expect(calls[1].target).toBe("ls");
    });

    it("ignores code blocks with language tag that aren't tool calls", () => {
      const msg = "```\nread: a.ts\n```\n```ts\nconst x = 1;\n```";
      const calls = parseToolCalls(msg);
      expect(calls).toHaveLength(1);
      expect(calls[0].target).toBe("a.ts");
    });

    it("assigns unique ids", () => {
      const msg = "```\nread: a.ts\n```\n```\nread: b.ts\n```";
      const calls = parseToolCalls(msg);
      expect(calls[0].id).not.toBe(calls[1].id);
    });

    it("parses tool calls with ~~~ fences (N-3)", () => {
      const msg = "Reading:\n~~~\nread: a.ts\n~~~";
      const calls = parseToolCalls(msg);
      expect(calls).toHaveLength(1);
      expect(calls[0].kind).toBe("read");
      expect(calls[0].target).toBe("a.ts");
    });

    it("parses write tool call with ~~~ fence and content", () => {
      const msg = "~~~\nwrite: b.ts\nhello world\n~~~";
      const calls = parseToolCalls(msg);
      expect(calls).toHaveLength(1);
      expect(calls[0].kind).toBe("write");
      expect(calls[0].target).toBe("b.ts");
      expect(calls[0].content).toBe("hello world");
    });

    it("does not match mismatched fences (``` open with ~~~ close)", () => {
      const msg = "```\nread: a.ts\n~~~";
      const calls = parseToolCalls(msg);
      expect(calls).toHaveLength(0);
    });

    it("parses tool calls with language tag on fence", () => {
      const msg = "```ts\nread: a.ts\n```";
      const calls = parseToolCalls(msg);
      expect(calls).toHaveLength(1);
      expect(calls[0].target).toBe("a.ts");
    });
  });

  describe("extractToolCallBlocks", () => {
    it("returns tool calls and cleaned message", () => {
      const msg =
        "I will read the file.\n```\nread: a.ts\n```\nThen proceed.";
      const { toolCalls, cleanedMessage } = extractToolCallBlocks(msg);
      expect(toolCalls).toHaveLength(1);
      expect(toolCalls[0].target).toBe("a.ts");
      expect(cleanedMessage).toBe("I will read the file.\nThen proceed.");
    });

    it("leaves normal code blocks intact in cleaned message", () => {
      const msg =
        "```\nread: a.ts\n```\nCode:\n```ts\nconst x = 1;\n```";
      const { toolCalls, cleanedMessage } = extractToolCallBlocks(msg);
      expect(toolCalls).toHaveLength(1);
      expect(cleanedMessage).toContain("```ts");
      expect(cleanedMessage).toContain("const x = 1;");
    });

    it("returns empty tool calls when none present", () => {
      const msg = "Just a normal message.";
      const { toolCalls, cleanedMessage } = extractToolCallBlocks(msg);
      expect(toolCalls).toEqual([]);
      expect(cleanedMessage).toBe("Just a normal message.");
    });
  });

  describe("executeToolCall", () => {
    it("reads a file and returns its content", async () => {
      (fileService.readFile as any).mockResolvedValue("file content");
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "a.ts",
        status: "pending",
      };
      const out = await executeToolCall(tc);
      expect(fileService.readFile).toHaveBeenCalledWith("/proj/a.ts");
      expect(out).toContain("Read a.ts:");
      expect(out).toContain("file content");
    });

    it("truncates very large file content", async () => {
      const big = "x".repeat(10000);
      (fileService.readFile as any).mockResolvedValue(big);
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "big.txt",
        status: "pending",
      };
      const out = await executeToolCall(tc);
      expect(out).toContain("[truncated");
      expect(out.length).toBeLessThan(big.length + 200);
    });

    it("requests a bound capability before writing a file", async () => {
      const tc: ToolCall = {
        id: "1",
        kind: "write",
        target: "out.ts",
        content: "console.log('hi');",
        status: "pending",
      };
      const out = await executeToolCall(tc);
      expect(agentService.requestWriteApproval).toHaveBeenCalledWith(
        "/proj/out.ts",
        "57b43dfc3c26aaf74f190e1dcba9dc34cb03e7686f9aeaad2d22747cae8c76bf",
        18,
      );
      expect(agentService.executeApprovedWrite).toHaveBeenCalledWith(
        "/proj/out.ts",
        "console.log('hi');",
        "write-token",
      );
      expect(fileService.writeFile).not.toHaveBeenCalled();
      expect(out).toContain("Wrote out.ts");
    });

    it("does not execute a write when backend approval is refused", async () => {
      vi.mocked(agentService.requestWriteApproval).mockRejectedValueOnce(
        new Error("file write was not approved"),
      );
      const tc: ToolCall = {
        id: "1",
        kind: "write",
        target: "out.ts",
        content: "blocked",
        status: "pending",
      };

      await expect(executeToolCall(tc)).rejects.toThrow(/not approved/);
      expect(agentService.executeApprovedWrite).not.toHaveBeenCalled();
      expect(fileService.writeFile).not.toHaveBeenCalled();
    });

    it("reports UTF-8 byte size when requesting write approval", async () => {
      const tc: ToolCall = {
        id: "1",
        kind: "write",
        target: "unicode.txt",
        content: "猫",
        status: "pending",
      };

      await executeToolCall(tc);

      expect(agentService.requestWriteApproval).toHaveBeenCalledWith(
        "/proj/unicode.txt",
        expect.any(String),
        3,
      );
    });

    it("surfaces an expired write token without falling back to direct write", async () => {
      vi.mocked(agentService.executeApprovedWrite).mockRejectedValueOnce(
        new Error("invalid or expired write approval token"),
      );
      const tc: ToolCall = {
        id: "1",
        kind: "write",
        target: "out.ts",
        content: "expired",
        status: "pending",
      };

      await expect(executeToolCall(tc)).rejects.toThrow(/expired/);
      expect(fileService.writeFile).not.toHaveBeenCalled();
    });

    it("throws when write has no content", async () => {
      const tc: ToolCall = {
        id: "1",
        kind: "write",
        target: "out.ts",
        status: "pending",
      };
      await expect(executeToolCall(tc)).rejects.toThrow(
        /missing file content/,
      );
    });

    it("rejects absolute paths in read tool (N-3 path validation)", async () => {
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "/etc/passwd",
        status: "pending",
      };
      await expect(executeToolCall(tc)).rejects.toThrow(/Absolute paths are not allowed/);
      expect(fileService.readFile).not.toHaveBeenCalled();
    });

    it("rejects Windows absolute paths in read tool", async () => {
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "C:\\Windows\\system32\\config\\SAM",
        status: "pending",
      };
      await expect(executeToolCall(tc)).rejects.toThrow(/Absolute paths are not allowed/);
    });

    it("rejects parent traversal in write tool", async () => {
      const tc: ToolCall = {
        id: "1",
        kind: "write",
        target: "../../etc/passwd",
        content: "malicious",
        status: "pending",
      };
      await expect(executeToolCall(tc)).rejects.toThrow(/escapes project root/);
      expect(fileService.writeFile).not.toHaveBeenCalled();
    });

    it("allows relative paths within project", async () => {
      (fileService.readFile as any).mockResolvedValue("ok");
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "src/sub/file.ts",
        status: "pending",
      };
      const out = await executeToolCall(tc);
      expect(fileService.readFile).toHaveBeenCalledWith("/proj/src/sub/file.ts");
      expect(out).toContain("Read src/sub/file.ts");
    });

    it("normalizes ./ prefix in paths", async () => {
      (fileService.readFile as any).mockResolvedValue("ok");
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "./src/a.ts",
        status: "pending",
      };
      await executeToolCall(tc);
      expect(fileService.readFile).toHaveBeenCalledWith("/proj/src/a.ts");
    });

    it("allows .. within project bounds (src/../lib/b.ts → lib/b.ts)", async () => {
      (fileService.readFile as any).mockResolvedValue("ok");
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "src/../lib/b.ts",
        status: "pending",
      };
      await executeToolCall(tc);
      expect(fileService.readFile).toHaveBeenCalledWith("/proj/lib/b.ts");
    });

    it("listRegisteredTools returns all built-in tool kinds", () => {
      const kinds = listRegisteredTools();
      expect(kinds).toContain("read");
      expect(kinds).toContain("write");
      expect(kinds).toContain("run");
      expect(kinds).toContain("search");
    });

    it("runs a command and returns the result summary", async () => {
      (agentService.executeApprovedCommand as any).mockResolvedValue({
        command: "go test",
        stdout: "ok",
        stderr: "",
        exitCode: 0,
        durationMs: 100,
      });
      const tc: ToolCall = {
        id: "1",
        kind: "run",
        target: "go test",
        status: "pending",
      };
      const out = await executeToolCall(tc);
      expect(agentService.requestCommandApproval).toHaveBeenCalledWith(
        "go test",
        "/proj",
      );
      expect(agentService.executeApprovedCommand).toHaveBeenCalledWith(
        "go test",
        "/proj",
        "approval-token",
      );
      expect(out).toContain("Ran: go test");
      expect(out).toContain("Exit code: 0");
      expect(out).toContain("stdout:");
      expect(out).toContain("ok");
    });

    it("searches and returns match summary", async () => {
      (searchService.search as any).mockResolvedValue([
        { path: "a.ts", matches: [{ line: 1, column: 0, preview: "TODO: fix" }] },
      ]);
      const tc: ToolCall = {
        id: "1",
        kind: "search",
        target: "TODO",
        status: "pending",
      };
      const out = await executeToolCall(tc);
      expect(searchService.search).toHaveBeenCalledWith(
        "/proj",
        "TODO",
        true,
      );
      expect(out).toContain("Found 1 match");
      expect(out).toContain("a.ts:1:0");
    });

    it("returns no-matches message when search finds nothing", async () => {
      (searchService.search as any).mockResolvedValue([]);
      const tc: ToolCall = {
        id: "1",
        kind: "search",
        target: "nothing",
        status: "pending",
      };
      const out = await executeToolCall(tc);
      expect(out).toContain("No matches");
    });
  });

  describe("approveToolCall", () => {
    it("executes and marks tool call as executed", async () => {
      (fileService.readFile as any).mockResolvedValue("content");
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "a.ts",
        status: "pending",
      };
      const obs = await approveToolCall(tc);
      expect(tc.status).toBe("executed");
      expect(tc.result).toBeTruthy();
      expect(obs).toContain("Read a.ts");
    });

    it("marks tool call as error on failure and returns error message", async () => {
      (fileService.readFile as any).mockRejectedValue(
        new Error("not found"),
      );
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "missing.ts",
        status: "pending",
      };
      const obs = await approveToolCall(tc);
      expect(tc.status).toBe("error");
      expect(tc.error).toContain("not found");
      expect(obs).toContain("Error executing");
      expect(obs).toContain("missing.ts");
    });
  });

  describe("rejectToolCall", () => {
    it("marks tool call as rejected and returns guidance message", () => {
      const tc: ToolCall = {
        id: "1",
        kind: "write",
        target: "out.ts",
        status: "pending",
      };
      const msg = rejectToolCall(tc);
      expect(tc.status).toBe("rejected");
      expect(msg).toContain("rejected");
      expect(msg).toContain("write");
      expect(msg).toContain("out.ts");
    });
  });

  describe("clearPendingToolCalls", () => {
    it("removes all pending tool calls", () => {
      agentState.pendingToolCalls = [
        { id: "a", kind: "read", target: "x", status: "pending" },
        { id: "b", kind: "run", target: "y", status: "executed" },
      ];
      clearPendingToolCalls();
      expect(agentState.pendingToolCalls).toEqual([]);
    });
  });

  describe("getAgentSystemPrompt", () => {
    it("fetches the agent prompt from the backend on first call", async () => {
      (aiService.getAgentSystemPrompt as any).mockResolvedValue("AGENT PROMPT");
      const result = await getAgentSystemPrompt();
      expect(aiService.getAgentSystemPrompt).toHaveBeenCalledTimes(1);
      expect(result).toContain("AGENT PROMPT");
      // N-16: the tool list is appended to the system prompt.
      expect(result).toContain("Available tools:");
      expect(result).toContain("`read:`");
      expect(result).toContain("`write:`");
    });

    it("caches the prompt on subsequent calls", async () => {
      (aiService.getAgentSystemPrompt as any).mockResolvedValue("CACHED");
      await getAgentSystemPrompt();
      await getAgentSystemPrompt();
      // Second call should hit the cache, not the backend.
      expect(aiService.getAgentSystemPrompt).toHaveBeenCalledTimes(1);
    });

    it("falls back to localized prompt on fetch failure (N-59)", async () => {
      (aiService.getAgentSystemPrompt as any).mockRejectedValue(new Error("nope"));
      const result = await getAgentSystemPrompt();
      // N-59: the catch block returns the localized agent prompt from i18n
      // (non-empty) plus the tool list, instead of an empty string.
      expect(result.length).toBeGreaterThan(0);
      expect(result).toContain("Available tools:");
      // The rejected backend value must NOT appear in the fallback.
      expect(result).not.toContain("nope");
    });

    // --- Plan 54: user-configured agent prompt override ---

    it("uses the appState.aiAgentSystemPrompt override when set", async () => {
      appState.aiAgentSystemPrompt = "MY CUSTOM AGENT PROMPT";
      const result = await getAgentSystemPrompt();
      expect(result).toContain("MY CUSTOM AGENT PROMPT");
      // The override is NOT cached — it's read fresh on every call.
      // The tool list should still be appended.
      expect(result).toContain("Available tools:");
      // The backend should NOT be called when an override is set.
      expect(aiService.getAgentSystemPrompt).not.toHaveBeenCalled();
    });

    it("override is not cached — changes apply on the next call", async () => {
      appState.aiAgentSystemPrompt = "FIRST OVERRIDE";
      const first = await getAgentSystemPrompt();
      expect(first).toContain("FIRST OVERRIDE");
      appState.aiAgentSystemPrompt = "SECOND OVERRIDE";
      const second = await getAgentSystemPrompt();
      expect(second).toContain("SECOND OVERRIDE");
      expect(second).not.toContain("FIRST OVERRIDE");
    });

    it("whitespace-only override falls back to the built-in", async () => {
      (aiService.getAgentSystemPrompt as any).mockResolvedValue("BUILTIN");
      appState.aiAgentSystemPrompt = "   \n\t  ";
      const result = await getAgentSystemPrompt();
      expect(result).toContain("BUILTIN");
      expect(result).not.toContain("   \n\t  ");
    });

    it("empty override falls back to the built-in (cached)", async () => {
      (aiService.getAgentSystemPrompt as any).mockResolvedValue("BUILTIN");
      appState.aiAgentSystemPrompt = "";
      const result = await getAgentSystemPrompt();
      expect(result).toContain("BUILTIN");
    });
  });

  describe("onAssistantFinished", () => {
    it("returns 0 and adds nothing for empty content", () => {
      expect(onAssistantFinished("")).toBe(0);
      expect(agentState.pendingToolCalls).toEqual([]);
    });

    it("returns 0 when message has no tool-call blocks", () => {
      expect(onAssistantFinished("just a regular reply")).toBe(0);
      expect(agentState.pendingToolCalls).toEqual([]);
    });

    it("parses tool calls and appends to pendingToolCalls", () => {
      const msg = "```\nread: a.ts\n```\n```\nrun: ls\n```";
      const count = onAssistantFinished(msg);
      expect(count).toBe(2);
      expect(agentState.pendingToolCalls).toHaveLength(2);
      expect(agentState.pendingToolCalls[0].target).toBe("a.ts");
      expect(agentState.pendingToolCalls[1].target).toBe("ls");
      expect(agentState.pendingToolCalls[0].status).toBe("pending");
    });

    it("pushes an output log entry when tool calls are added", () => {
      onAssistantFinished("```\nread: a.ts\n```");
      expect(pushOutput).toHaveBeenCalledWith(
        "agent",
        "info",
        expect.stringContaining("1 tool call"),
      );
    });
  });

  describe("checkRunRisk", () => {
    it("calls checkCommand and populates riskLevel for a run tool call", async () => {
      (agentService.checkCommand as any).mockResolvedValue({
        riskLevel: "elevated",
        blocked: false,
      });
      const tc: ToolCall = {
        id: "1",
        kind: "run",
        target: "npm install",
        status: "pending",
      };
      const { checkRunRisk } = await import("@/stores/agent");
      await checkRunRisk(tc);
      expect(agentService.checkCommand).toHaveBeenCalledWith("npm install");
      expect(tc.riskLevel).toBe("elevated");
      expect(tc.blockReason).toBeUndefined();
    });

    it("populates blockReason when the command is blocked", async () => {
      (agentService.checkCommand as any).mockResolvedValue({
        riskLevel: "dangerous",
        blocked: true,
        blockReason: "rm -rf (recursive force delete)",
      });
      const tc: ToolCall = {
        id: "2",
        kind: "run",
        target: "rm -rf /",
        status: "pending",
      };
      const { checkRunRisk } = await import("@/stores/agent");
      await checkRunRisk(tc);
      expect(tc.riskLevel).toBe("dangerous");
      expect(tc.blockReason).toBe("rm -rf (recursive force delete)");
    });

    it("leaves riskLevel undefined when checkCommand fails", async () => {
      (agentService.checkCommand as any).mockRejectedValue(new Error("network"));
      const tc: ToolCall = {
        id: "3",
        kind: "run",
        target: "echo hello",
        status: "pending",
      };
      const { checkRunRisk } = await import("@/stores/agent");
      await checkRunRisk(tc);
      expect(tc.riskLevel).toBeUndefined();
    });
  });

  describe("approveAndFeed", () => {
    it("executes the tool call and feeds the observation back to AI", async () => {
      (fileService.readFile as any).mockResolvedValue("file body");
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "a.ts",
        status: "pending",
      };
      const { sendMessage } = await import("@/stores/ai");
      await approveAndFeed(tc);
      expect(tc.status).toBe("executed");
      expect(sendMessage).toHaveBeenCalledTimes(1);
      const sentArg = (sendMessage as any).mock.calls[0][0] as string;
      expect(sentArg).toContain("[Observation]");
      expect(sentArg).toContain("Read a.ts");
    });

    it("feeds the error observation back to AI when execution fails", async () => {
      // approveToolCall returns a non-null error string even on failure,
      // so we expect sendMessage to be called with the error observation.
      (fileService.readFile as any).mockRejectedValue(new Error("boom"));
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "missing.ts",
        status: "pending",
      };
      const { sendMessage } = await import("@/stores/ai");
      await approveAndFeed(tc);
      expect(tc.status).toBe("error");
      expect(sendMessage).toHaveBeenCalledTimes(1);
      const sentArg = (sendMessage as any).mock.calls[0][0] as string;
      expect(sentArg).toContain("Error executing");
    });
  });

  describe("rejectAndFeed", () => {
    it("marks the tool call as rejected and feeds the rejection back to AI", async () => {
      const tc: ToolCall = {
        id: "1",
        kind: "write",
        target: "out.ts",
        status: "pending",
      };
      const { sendMessage } = await import("@/stores/ai");
      await rejectAndFeed(tc);
      expect(tc.status).toBe("rejected");
      expect(sendMessage).toHaveBeenCalledTimes(1);
      const sentArg = (sendMessage as any).mock.calls[0][0] as string;
      expect(sentArg).toContain("[Rejection]");
      expect(sentArg).toContain("rejected");
    });
  });

  describe("N-10 max iteration protection", () => {
    // MAX_TOOL_CALLS is 20 in agent.ts. Tests reference the literal value
    // to verify the threshold behavior without importing the constant.
    const MAX_TOOL_CALLS = 20;

    it("maxIterationsReached is false when toolCallCount is below threshold", () => {
      agentState.toolCallCount = 0;
      expect(maxIterationsReached.value).toBe(false);
      agentState.toolCallCount = MAX_TOOL_CALLS - 1;
      expect(maxIterationsReached.value).toBe(false);
    });

    it("maxIterationsReached is true when toolCallCount reaches threshold", () => {
      agentState.toolCallCount = MAX_TOOL_CALLS;
      expect(maxIterationsReached.value).toBe(true);
    });

    it("maxIterationsReached is true when toolCallCount exceeds threshold", () => {
      agentState.toolCallCount = MAX_TOOL_CALLS + 5;
      expect(maxIterationsReached.value).toBe(true);
    });

    it("onAssistantFinished increments toolCallCount by number of calls", () => {
      const msg = "```\nread: a.ts\n```\n```\nread: b.ts\n```\n```\nread: c.ts\n```";
      const count = onAssistantFinished(msg);
      expect(count).toBe(3);
      expect(agentState.toolCallCount).toBe(3);
    });

    it("does not call notifyWarning below the threshold", () => {
      agentState.toolCallCount = MAX_TOOL_CALLS - 2;
      onAssistantFinished("```\nread: a.ts\n```");
      expect(notifyWarning).not.toHaveBeenCalled();
    });

    it("calls notifyWarning when threshold is reached", () => {
      agentState.toolCallCount = MAX_TOOL_CALLS - 1;
      onAssistantFinished("```\nread: a.ts\n```");
      expect(notifyWarning).toHaveBeenCalledTimes(1);
      const arg = (notifyWarning as any).mock.calls[0][0] as string;
      expect(arg).toContain("tool calls");
    });

    it("pushes a warn-level output when threshold is reached", () => {
      agentState.toolCallCount = MAX_TOOL_CALLS - 1;
      onAssistantFinished("```\nread: a.ts\n```");
      // The warn push should be among the pushOutput calls.
      const warnCalls = (pushOutput as any).mock.calls.filter(
        (c: unknown[]) => c[0] === "agent" && c[1] === "warn",
      );
      expect(warnCalls.length).toBeGreaterThanOrEqual(1);
      const warnArg = warnCalls[0][2] as string;
      // GOAL-P1-02: the message now states a backend refusal rather than
      // offering advice. The old text ("Max iteration threshold reached ...
      // Consider starting a new conversation") described a limit the user
      // could ignore, which is exactly what the frontend-only ceiling was.
      expect(warnArg).toContain("Tool budget exhausted");
      expect(warnArg).toContain("backend will refuse");
      expect(warnArg).toContain(`${MAX_TOOL_CALLS}`);
    });

    it("does not push warn output below the threshold", () => {
      agentState.toolCallCount = 0;
      onAssistantFinished("```\nread: a.ts\n```");
      const warnCalls = (pushOutput as any).mock.calls.filter(
        (c: unknown[]) => c[0] === "agent" && c[1] === "warn",
      );
      expect(warnCalls).toHaveLength(0);
    });

    it("clearPendingToolCalls resets toolCallCount to 0", () => {
      agentState.toolCallCount = 25;
      agentState.pendingToolCalls = [
        { id: "x", kind: "read", target: "a", status: "pending" },
      ];
      clearPendingToolCalls();
      expect(agentState.toolCallCount).toBe(0);
      expect(agentState.pendingToolCalls).toEqual([]);
    });

    it("accumulates toolCallCount across multiple onAssistantFinished calls", () => {
      onAssistantFinished("```\nread: a.ts\n```");
      expect(agentState.toolCallCount).toBe(1);
      onAssistantFinished("```\nread: b.ts\n```");
      expect(agentState.toolCallCount).toBe(2);
      onAssistantFinished("```\nread: c.ts\n```\n```\nread: d.ts\n```");
      expect(agentState.toolCallCount).toBe(4);
      expect(maxIterationsReached.value).toBe(false);
    });

    it("warns on the call that crosses the threshold", () => {
      // Set count to 19, then emit 2 calls → crosses 20.
      agentState.toolCallCount = MAX_TOOL_CALLS - 1;
      const msg = "```\nread: a.ts\n```\n```\nread: b.ts\n```";
      onAssistantFinished(msg);
      expect(agentState.toolCallCount).toBe(MAX_TOOL_CALLS + 1);
      expect(notifyWarning).toHaveBeenCalledTimes(1);
      expect(maxIterationsReached.value).toBe(true);
    });
  });

  describe("N-16 ToolRegistry", () => {
    it("built-in tools are registered with schemas", () => {
      const tools = getRegisteredTools();
      const kinds = tools.map((t) => t.kind);
      expect(kinds).toContain("read");
      expect(kinds).toContain("write");
      expect(kinds).toContain("run");
      expect(kinds).toContain("search");
      const readTool = tools.find((t) => t.kind === "read")!;
      expect(readTool.schema.description).toBeTruthy();
      expect(readTool.schema.dangerLevel).toBe("safe");
      const runTool = tools.find((t) => t.kind === "run")!;
      expect(runTool.schema.dangerLevel).toBe("elevated");
    });

    it("registerTool adds a custom tool and it appears in listRegisteredTools", () => {
      registerTool({
        kind: "edit",
        schema: { description: "Edit a file with search/replace", dangerLevel: "elevated" },
        execute: async () => "edited",
      });
      const kinds = listRegisteredTools();
      expect(kinds).toContain("edit");
      // Clean up
      unregisterTool("edit");
    });

    it("custom tool is recognized by parseToolCalls (dynamic regex)", () => {
      registerTool({
        kind: "lint",
        schema: { description: "Run linter", dangerLevel: "safe" },
        execute: async () => "lint ok",
      });
      const msg = "```\nlint: src/\n```";
      const calls = parseToolCalls(msg);
      expect(calls).toHaveLength(1);
      expect(calls[0].kind).toBe("lint");
      expect(calls[0].target).toBe("src/");
      // Clean up
      unregisterTool("lint");
    });

    it("after unregistering a custom tool, it is no longer parsed", () => {
      registerTool({
        kind: "temp",
        schema: { description: "Temporary tool", dangerLevel: "safe" },
        execute: async () => "temp",
      });
      expect(parseToolCalls("```\ntemp: x\n```")).toHaveLength(1);
      unregisterTool("temp");
      expect(parseToolCalls("```\ntemp: x\n```")).toHaveLength(0);
    });

    it("unregisterTool returns false for unknown kind", () => {
      expect(unregisterTool("nonexistent")).toBe(false);
    });

    it("getToolSchemaList includes all registered tools with descriptions", () => {
      const list = getToolSchemaList();
      expect(list).toContain("Available tools:");
      expect(list).toContain("`read:`");
      expect(list).toContain("`write:`");
      expect(list).toContain("`run:`");
      expect(list).toContain("`search:`");
      expect(list).toContain("risk: safe");
      expect(list).toContain("risk: elevated");
    });

    it("getToolSchemaList includes custom tools after registration", () => {
      registerTool({
        kind: "migrate",
        schema: { description: "Run database migration", dangerLevel: "dangerous" },
        execute: async () => "migrated",
      });
      const list = getToolSchemaList();
      expect(list).toContain("`migrate:`");
      expect(list).toContain("risk: dangerous");
      unregisterTool("migrate");
    });

    it("custom tool executor is dispatched via executeToolCall", async () => {
      registerTool({
        kind: "echo",
        schema: { description: "Echo tool", dangerLevel: "safe" },
        execute: async (tc) => `echoed: ${tc.target}`,
      });
      const tc: ToolCall = {
        id: "1",
        kind: "echo",
        target: "hello",
        status: "pending",
      };
      const out = await executeToolCall(tc);
      expect(out).toBe("echoed: hello");
      unregisterTool("echo");
    });

    it("registerTool invalidates the prompt cache so new tools appear", async () => {
      // First fetch populates the cache with the current tool list.
      (aiService.getAgentSystemPrompt as any).mockResolvedValue("BASE");
      const first = await getAgentSystemPrompt();
      expect(first).toContain("`read:`");
      expect(first).not.toContain("`custom2:`");

      // Register a new tool — cache should be invalidated.
      registerTool({
        kind: "custom2",
        schema: { description: "Custom 2", dangerLevel: "safe" },
        execute: async () => "ok",
      });
      const second = await getAgentSystemPrompt();
      expect(second).toContain("`custom2:`");
      unregisterTool("custom2");
    });
  });

  describe("Plan 47 approval policy", () => {
    describe("getApprovalPolicy", () => {
      it("returns 'always-ask' by default when no config is set", () => {
        expect(getApprovalPolicy("read")).toBe("always-ask");
        expect(getApprovalPolicy("write")).toBe("always-ask");
        expect(getApprovalPolicy("run")).toBe("always-ask");
        expect(getApprovalPolicy("search")).toBe("always-ask");
      });

      it("returns 'auto-approve' when configured", () => {
        appState.toolApprovalConfig = { read: "auto-approve" };
        expect(getApprovalPolicy("read")).toBe("auto-approve");
      });

      it("returns 'never-approve' when configured", () => {
        appState.toolApprovalConfig = { write: "never-approve" };
        expect(getApprovalPolicy("write")).toBe("never-approve");
      });

      it("returns 'always-ask' for unknown kinds without config", () => {
        expect(getApprovalPolicy("custom-tool")).toBe("always-ask");
      });

      it("returns 'always-ask' when config has an invalid value", () => {
        appState.toolApprovalConfig = { read: "invalid-policy" as any };
        expect(getApprovalPolicy("read")).toBe("always-ask");
      });

      it("returns configured policy for custom tool kinds", () => {
        appState.toolApprovalConfig = { lint: "auto-approve" };
        expect(getApprovalPolicy("lint")).toBe("auto-approve");
      });

      it("does not leak policy between kinds", () => {
        appState.toolApprovalConfig = { read: "auto-approve" };
        expect(getApprovalPolicy("read")).toBe("auto-approve");
        expect(getApprovalPolicy("write")).toBe("always-ask");
      });
    });

    describe("shouldAutoApprove", () => {
      it("returns false for 'always-ask' policy", () => {
        const tc: ToolCall = {
          id: "1",
          kind: "read",
          target: "a.ts",
          status: "pending",
        };
        expect(shouldAutoApprove(tc)).toBe(false);
      });

      it("returns true for 'auto-approve' policy on non-run tool", () => {
        appState.toolApprovalConfig = { read: "auto-approve" };
        const tc: ToolCall = {
          id: "1",
          kind: "read",
          target: "a.ts",
          status: "pending",
        };
        expect(shouldAutoApprove(tc)).toBe(true);
      });

      it("returns false for 'never-approve' policy", () => {
        appState.toolApprovalConfig = { write: "never-approve" };
        const tc: ToolCall = {
          id: "1",
          kind: "write",
          target: "out.ts",
          status: "pending",
        };
        expect(shouldAutoApprove(tc)).toBe(false);
      });

      it("G-SEC-02: returns false for 'auto-approve' run tool without blockReason (never auto-approve commands)", () => {
        appState.toolApprovalConfig = { run: "auto-approve" };
        const tc: ToolCall = {
          id: "1",
          kind: "run",
          target: "ls",
          status: "pending",
        };
        // G-SEC-02: run tools are never auto-approved, even with auto-approve
        // policy and no blockReason. All commands require manual approval.
        expect(shouldAutoApprove(tc)).toBe(false);
      });

      it("prompt-5 Task E: returns false for 'auto-approve' write tool (never auto-approve writes)", () => {
        appState.toolApprovalConfig = { write: "auto-approve" };
        const tc: ToolCall = {
          id: "1",
          kind: "write",
          target: "out.ts",
          content: "x",
          status: "pending",
        };
        expect(shouldAutoApprove(tc)).toBe(false);
      });

      it("G-SEC-02: returns false for 'auto-approve' run tool with blockReason (denylist)", () => {
        appState.toolApprovalConfig = { run: "auto-approve" };
        const tc: ToolCall = {
          id: "1",
          kind: "run",
          target: "rm -rf /",
          status: "pending",
          blockReason: "rm -rf (recursive force delete)",
        };
        expect(shouldAutoApprove(tc)).toBe(false);
      });
    });

    describe("native tool protocol (prompt-5 Task H)", () => {
      it("buildNativeToolDefs includes builtin tools with schemas", () => {
        const defs = buildNativeToolDefs();
        const names = defs.map((d) => d.function.name);
        expect(names).toContain("read");
        expect(names).toContain("write");
        expect(names).toContain("run");
        expect(names).toContain("search");
        expect(defs.every((d) => d.type === "function")).toBe(true);
      });

      it("parseNativeToolCalls maps OpenAI-style payloads to ToolCall", () => {
        const calls = parseNativeToolCalls([
          { id: "c1", name: "read", arguments: JSON.stringify({ path: "main.go" }) },
          { id: "c2", name: "write", arguments: JSON.stringify({ path: "a.ts", content: "x" }) },
          { id: "c3", name: "run", arguments: JSON.stringify({ command: "go test" }) },
        ]);
        expect(calls).toHaveLength(3);
        expect(calls[0]).toMatchObject({ kind: "read", target: "main.go", status: "pending" });
        expect(calls[1]).toMatchObject({ kind: "write", target: "a.ts", content: "x" });
        expect(calls[2]).toMatchObject({ kind: "run", target: "go test" });
      });

      it("onNativeToolCalls enqueues pending tools", () => {
        agentState.mode = "agent";
        const n = onNativeToolCalls([
          { name: "search", arguments: JSON.stringify({ query: "TODO" }) },
        ]);
        expect(n).toBe(1);
        expect(agentState.pendingToolCalls).toHaveLength(1);
        expect(agentState.pendingToolCalls[0].kind).toBe("search");
        expect(agentState.toolCallCount).toBe(1);
      });
    });

    describe("applyApprovalPolicy", () => {
      it("auto-approves and feeds observation for auto-approve policy", async () => {
        (fileService.readFile as any).mockResolvedValue("content");
        appState.toolApprovalConfig = { read: "auto-approve" };
        const tc: ToolCall = {
          id: "1",
          kind: "read",
          target: "a.ts",
          status: "pending",
        };
        const { sendMessage } = await import("@/stores/ai");
        await applyApprovalPolicy(tc);
        expect(tc.status).toBe("executed");
        expect(pushOutput).toHaveBeenCalledWith(
          "agent",
          "info",
          expect.stringContaining("Auto-approving"),
        );
        expect(sendMessage).toHaveBeenCalledTimes(1);
      });

      it("auto-rejects and feeds rejection for never-approve policy", async () => {
        appState.toolApprovalConfig = { write: "never-approve" };
        const tc: ToolCall = {
          id: "1",
          kind: "write",
          target: "out.ts",
          status: "pending",
        };
        const { sendMessage } = await import("@/stores/ai");
        await applyApprovalPolicy(tc);
        expect(tc.status).toBe("rejected");
        expect(pushOutput).toHaveBeenCalledWith(
          "agent",
          "info",
          expect.stringContaining("Auto-rejecting"),
        );
        expect(sendMessage).toHaveBeenCalledTimes(1);
        const sentArg = (sendMessage as any).mock.calls[0][0] as string;
        expect(sentArg).toContain("[Rejection]");
      });

      it("is a no-op for 'always-ask' policy (call stays pending)", async () => {
        const tc: ToolCall = {
          id: "1",
          kind: "read",
          target: "a.ts",
          status: "pending",
        };
        const { sendMessage } = await import("@/stores/ai");
        await applyApprovalPolicy(tc);
        expect(tc.status).toBe("pending");
        expect(sendMessage).not.toHaveBeenCalled();
      });

      it("G-SEC-02: does not auto-approve blocked run commands (respects denylist)", async () => {
        appState.toolApprovalConfig = { run: "auto-approve" };
        const tc: ToolCall = {
          id: "1",
          kind: "run",
          target: "rm -rf /",
          status: "pending",
          blockReason: "rm -rf (recursive force delete)",
        };
        const { sendMessage } = await import("@/stores/ai");
        await applyApprovalPolicy(tc);
        // Should stay pending so the user sees the block reason.
        expect(tc.status).toBe("pending");
        expect(sendMessage).not.toHaveBeenCalled();
      });

      it("G-SEC-02: does not auto-approve non-blocked run commands (always manual approval)", async () => {
        // G-SEC-02: even non-blocked run commands with auto-approve policy
        // must stay pending for manual approval. The denylist is not a
        // security boundary, so no command bypasses approval.
        appState.toolApprovalConfig = { run: "auto-approve" };
        const tc: ToolCall = {
          id: "1",
          kind: "run",
          target: "ls -la",
          status: "pending",
          // No blockReason — command is not on the denylist.
        };
        const { sendMessage } = await import("@/stores/ai");
        await applyApprovalPolicy(tc);
        expect(tc.status).toBe("pending");
        expect(sendMessage).not.toHaveBeenCalled();
      });

      it("G-SEC-02: awaits _riskCheckPromise for run tools but never auto-approves", async () => {
        appState.toolApprovalConfig = { run: "auto-approve" };
        (agentService.executeApprovedCommand as any).mockResolvedValue({
          command: "ls",
          stdout: "ok",
          stderr: "",
          exitCode: 0,
          durationMs: 10,
        });
        let resolveRisk!: () => void;
        const riskPromise = new Promise<void>((resolve) => {
          resolveRisk = resolve;
        });
        const tc: ToolCall = {
          id: "1",
          kind: "run",
          target: "ls",
          status: "pending",
          _riskCheckPromise: riskPromise,
        };
        const { sendMessage } = await import("@/stores/ai");
        // applyApprovalPolicy should not complete until riskPromise resolves.
        let completed = false;
        const promise = applyApprovalPolicy(tc).then(() => {
          completed = true;
        });
        // Yield to the event loop — the function should still be waiting.
        await Promise.resolve();
        await Promise.resolve();
        expect(completed).toBe(false);
        expect(tc.status).toBe("pending");
        // Now resolve the risk check — G-SEC-02: the policy must NOT
        // auto-approve. The call stays pending for manual approval.
        resolveRisk();
        await promise;
        expect(tc.status).toBe("pending");
        expect(sendMessage).not.toHaveBeenCalled();
      });

      it("is a no-op when status is not pending (already handled)", async () => {
        appState.toolApprovalConfig = { read: "auto-approve" };
        const tc: ToolCall = {
          id: "1",
          kind: "read",
          target: "a.ts",
          status: "executed",
        };
        const { sendMessage } = await import("@/stores/ai");
        await applyApprovalPolicy(tc);
        expect(tc.status).toBe("executed");
        expect(sendMessage).not.toHaveBeenCalled();
      });

      it("auto-approves non-run tools without _riskCheckPromise", async () => {
        (fileService.readFile as any).mockResolvedValue("content");
        appState.toolApprovalConfig = { read: "auto-approve" };
        const tc: ToolCall = {
          id: "1",
          kind: "read",
          target: "a.ts",
          status: "pending",
          // Note: no _riskCheckPromise — only run tools set this.
        };
        await applyApprovalPolicy(tc);
        expect(tc.status).toBe("executed");
      });
    });
  });
});
