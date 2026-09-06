import { nextTick, watchEffect } from "vue";
import { describe, it, expect, beforeEach, afterEach, vi, type Mock } from "vitest";
import type { AgentToolCatalog } from "@/api/automation";

const { aiStreamingState } = vi.hoisted(() => ({
  aiStreamingState: { streaming: false, globalStreamBusy: false },
}));

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
		getToolCatalog: vi.fn(),
		executeAgentTool: vi.fn().mockResolvedValue({
			observation: "backend observation",
			metadata: {},
			usage: { unitId: "u1", sessionId: "chat-test", unitKind: "tool", operation: "read", cost: 0, costBasis: "not-applicable", estimated: false, success: true },
		}),
    checkCommand: vi.fn(),
    // GOAL-P1-02: budget methods added to the facade — mock so refreshToolBudget
    // does not throw during tests that set toolCallCount directly.
    getToolBudget: vi.fn().mockResolvedValue({ spent: 0, limit: 20, remaining: 20, exhausted: false, timedOut: false, epoch: 1, startedAt: "", expiresAt: "" }),
    startNewToolBudgetEpoch: vi.fn(),
  },
  aiService: {
    getAgentSystemPrompt: vi.fn(),
  },
}));

vi.mock("@/stores/app", () => ({
  appState: {
    currentProject: "/proj",
    workspaceGeneration: 0,
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
  aiState: aiStreamingState,
  sendMessage: vi.fn().mockResolvedValue(undefined),
  sendNativeToolResults: vi.fn().mockResolvedValue(undefined),
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
	ensureAgentSession,
	resetAgentSession,
  onAssistantFinished,
  getAgentSystemPrompt,
  approveAndFeed,
  rejectAndFeed,
  listRegisteredTools,
  registerTool,
  unregisterTool,
  getRegisteredTools,
  maxIterationsReached,
  __resetAgentPromptCacheForTests,
	__setAgentToolCatalogForTests,
	refreshAgentToolCatalog,
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
import { notifyError, notifyWarning } from "@/lib/notifications";

function builtinCatalog(revision = 1): AgentToolCatalog {
	return {
		revision,
		tools: [
			{ id: "read", wireName: "read", description: "Read a file", inputSchema: { type: "object", properties: { path: { type: "string", minLength: 1 } }, required: ["path"], additionalProperties: false }, source: "builtin" as const, risk: "read-only" as const, approval: "backend-policy" as const, mutation: "none" as const },
			{ id: "write", wireName: "write", description: "Write a file", inputSchema: { type: "object", properties: { path: { type: "string", minLength: 1 }, content: { type: "string" }, selectedHunks: { type: "array", items: { type: "integer" } } }, required: ["path", "content"], additionalProperties: false }, source: "builtin" as const, risk: "elevated" as const, approval: "manual" as const, mutation: "workspace-transaction" as const },
			{ id: "run", wireName: "run", description: "Run a command", inputSchema: { type: "object", properties: { command: { type: "string", minLength: 1 }, cwd: { type: "string" } }, required: ["command"], additionalProperties: false }, source: "builtin" as const, risk: "elevated" as const, approval: "manual" as const, mutation: "external" as const },
			{ id: "search", wireName: "search", description: "Search files", inputSchema: { type: "object", properties: { query: { type: "string", minLength: 1 }, ignoreCase: { type: "boolean" } }, required: ["query"], additionalProperties: false }, source: "builtin" as const, risk: "read-only" as const, approval: "backend-policy" as const, mutation: "none" as const },
			{ id: "codebase", wireName: "codebase", description: "Search the workspace", inputSchema: { type: "object", properties: { query: { type: "string", minLength: 1 }, ignoreCase: { type: "boolean" } }, required: ["query"], additionalProperties: false }, source: "builtin" as const, risk: "read-only" as const, approval: "backend-policy" as const, mutation: "none" as const },
			{ id: "git.status", wireName: "git.status", description: "Read Git status", inputSchema: { type: "object", properties: {}, additionalProperties: false }, source: "builtin" as const, risk: "read-only" as const, approval: "backend-policy" as const, mutation: "none" as const },
			{ id: "git.diff", wireName: "git.diff", description: "Read a Git diff", inputSchema: { type: "object", properties: { path: { type: "string", minLength: 1 } }, required: ["path"], additionalProperties: false }, source: "builtin" as const, risk: "read-only" as const, approval: "backend-policy" as const, mutation: "none" as const },
			{ id: "plan", wireName: "plan", description: "Create a plan", inputSchema: { type: "object", properties: { goal: { type: "string", minLength: 1 }, constraints: { type: "string" } }, required: ["goal"], additionalProperties: false }, source: "builtin" as const, risk: "read-only" as const, approval: "backend-policy" as const, mutation: "none" as const },
		],
	};
}

function backendExecutionResult(observation: string) {
	return {
		observation,
		metadata: {},
		usage: {
			unitId: "u-test",
			sessionId: "chat-test",
			unitKind: "tool",
			operation: "test",
			cost: 0,
			costBasis: "not-applicable",
			estimated: false,
			success: true,
		},
	};
}

describe("agent store", () => {
  beforeEach(() => {
    cleanupAgentPendingSyncListener();
    vi.clearAllMocks();
		(agentService as any).createSession = undefined;
		(agentService as any).closeSession = undefined;
		resetAgentSession();
    agentState.mode = "chat";
    agentState.pendingToolCalls = [];
    agentState.toolCallCount = 0;
    agentState.budget = null;
    // GOAL-P1-02: the backend budget status must be reset too. Leaving a status
    // from a previous test makes `maxIterationsReached` read that instead of the
    // local counter these tests set, which silently breaks their premise.
    appState.agentPermissionMode = "always-ask";
    appState.workspaceGeneration = 0;
    // Plan 54: reset the agent prompt override between tests.
    appState.aiAgentSystemPrompt = "";
		aiStreamingState.streaming = false;
		aiStreamingState.globalStreamBusy = false;
    __resetAgentPromptCacheForTests();
		__setAgentToolCatalogForTests(builtinCatalog());
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

    it("setMode burns a queued auto-approval before it can execute", async () => {
      setMode("agent");
      appState.agentPermissionMode = "assist";
      aiStreamingState.streaming = true;
      onNativeToolCalls([{
        id: "queued-before-mode-switch",
        name: "read",
        arguments: '{"path":"README.md"}',
      }]);

      setMode("chat");
      aiStreamingState.streaming = false;
      await Promise.resolve();
      await Promise.resolve();

      const { sendMessage, sendNativeToolResults } = await import("@/stores/ai");
      expect(agentState.pendingToolCalls).toEqual([]);
      expect(agentService.executeAgentTool).not.toHaveBeenCalled();
      expect(sendMessage).not.toHaveBeenCalled();
      expect(sendNativeToolResults).not.toHaveBeenCalled();
    });
  });

	describe("workspace-bound backend session", () => {
		it("rotates the cached chat session when workspace generation changes", async () => {
			(agentService as any).createSession = vi.fn();
			(agentService as any).closeSession = vi.fn().mockResolvedValue(undefined);
			const createSession = vi.mocked((agentService as any).createSession);
			createSession.mockResolvedValueOnce("chat:workspace-a").mockResolvedValueOnce("chat:workspace-b");
			expect(await ensureAgentSession()).toBe("chat:workspace-a");
			appState.workspaceGeneration = 1;
			expect(await ensureAgentSession()).toBe("chat:workspace-b");
			expect(agentService.closeSession).toHaveBeenCalledWith("chat:workspace-a");
		});

		it("does not publish a session created across a workspace change", async () => {
			(agentService as any).createSession = vi.fn();
			(agentService as any).closeSession = vi.fn().mockResolvedValue(undefined);
			const createSession = vi.mocked((agentService as any).createSession);
			let resolve!: (id: string) => void;
			createSession.mockReturnValueOnce(new Promise<string>((done) => { resolve = done; }));
			const pending = ensureAgentSession();
			appState.workspaceGeneration = 2;
			resolve("chat:stale");
			await expect(pending).rejects.toThrow(/session changed/);
			expect(agentService.closeSession).toHaveBeenCalledWith("chat:stale");
		});

		it("reset clears the backend authority cache", async () => {
			(agentService as any).createSession = vi.fn();
			(agentService as any).closeSession = vi.fn().mockResolvedValue(undefined);
			const createSession = vi.mocked((agentService as any).createSession);
			createSession.mockResolvedValueOnce("chat:reset-a").mockResolvedValueOnce("chat:reset-b");
			expect(await ensureAgentSession()).toBe("chat:reset-a");
			resetAgentSession();
			expect(await ensureAgentSession()).toBe("chat:reset-b");
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
      expect(calls[0].source).toBe("fence");
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

    it("keeps a schema-invalid tool block visible instead of silently deleting it", () => {
      const msg = "Before\n```\nread: {\"bad\"\n```\nAfter";
      const { toolCalls, cleanedMessage } = extractToolCallBlocks(msg);
      expect(toolCalls).toHaveLength(0);
      expect(cleanedMessage).toContain("read:");
      expect(cleanedMessage).toContain("Before");
      expect(cleanedMessage).toContain("After");
    });
  });

  describe("executeToolCall", () => {
    it("retains the typed backend receipt and the session used for execution", async () => {
			const executionResult = backendExecutionResult("Read a.ts: receipt body");
			executionResult.usage.operation = "read";
			(agentService.executeAgentTool as Mock).mockResolvedValueOnce(executionResult);
			const tc: ToolCall = {
				id: "receipt-1",
				kind: "read",
				target: "a.ts",
				sessionId: "chat-test",
				status: "pending",
			};

			await executeToolCall(tc);

			expect(tc.execution).toEqual({
				requestSessionId: "chat-test",
				result: executionResult,
			});
			expect(tc.execution?.result.usage).toMatchObject({
				unitId: "u-test",
				sessionId: "chat-test",
				operation: "read",
				success: true,
			});
		});

    it("reads a file and returns its content", async () => {
			(agentService.executeAgentTool as Mock).mockResolvedValueOnce(
				backendExecutionResult("Read a.ts:\nfile content"),
			);
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "a.ts",
        status: "pending",
      };
      const out = await executeToolCall(tc);
			expect(agentService.executeAgentTool).toHaveBeenCalledWith(expect.objectContaining({
				toolId: "read", arguments: { path: "a.ts" },
			}));
			expect(fileService.readFile).not.toHaveBeenCalled();
      expect(out).toContain("Read a.ts:");
      expect(out).toContain("file content");
    });

    it("truncates very large file content", async () => {
      const big = "x".repeat(10000);
			(agentService.executeAgentTool as Mock).mockResolvedValueOnce(
				backendExecutionResult(`Read big.txt:\n${big.slice(0, 8000)}\n... [truncated, 10000 total chars]`),
			);
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
			(agentService.executeAgentTool as Mock).mockResolvedValueOnce(
				backendExecutionResult("Wrote out.ts (18 bytes)."),
			);
      const tc: ToolCall = {
        id: "1",
        kind: "write",
        target: "out.ts",
        content: "console.log('hi');",
        status: "pending",
      };
      const out = await executeToolCall(tc);
			expect(agentService.executeAgentTool).toHaveBeenCalledWith(expect.objectContaining({
				toolId: "write",
				arguments: { path: "out.ts", content: "console.log('hi');" },
			}));
      expect(fileService.writeFile).not.toHaveBeenCalled();
      expect(out).toContain("Wrote out.ts");
    });

    it("does not execute a write when backend approval is refused", async () => {
			(agentService.executeAgentTool as Mock).mockRejectedValueOnce(
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
			expect(agentService.executeAgentTool).toHaveBeenCalledTimes(1);
      expect(fileService.writeFile).not.toHaveBeenCalled();
    });

    it("sends exact UTF-8 content for backend byte accounting", async () => {
      const tc: ToolCall = {
        id: "1",
        kind: "write",
        target: "unicode.txt",
        content: "猫",
        status: "pending",
      };

      await executeToolCall(tc);

			expect(agentService.executeAgentTool).toHaveBeenCalledWith(expect.objectContaining({
				arguments: { path: "unicode.txt", content: "猫" },
			}));
    });

    it("surfaces an expired write token without falling back to direct write", async () => {
			(agentService.executeAgentTool as Mock).mockRejectedValueOnce(
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

    it("rejects a structured write that omits required content", async () => {
      const tc: ToolCall = {
        id: "1",
        kind: "write",
        target: "out.ts",
				arguments: { path: "out.ts" },
				catalogRevision: 1,
				wireName: "write",
				sessionId: "chat-test",
        status: "pending",
      };
      await expect(executeToolCall(tc)).rejects.toThrow(
				/missing its backend catalog binding/,
      );
			expect(agentService.executeAgentTool).not.toHaveBeenCalled();
    });

    it("rejects absolute paths in read tool (N-3 path validation)", async () => {
			(agentService.executeAgentTool as Mock).mockRejectedValueOnce(
				new Error("Absolute paths are not allowed"),
			);
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
			(agentService.executeAgentTool as Mock).mockRejectedValueOnce(
				new Error("Absolute paths are not allowed"),
			);
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "C:\\Windows\\system32\\config\\SAM",
        status: "pending",
      };
      await expect(executeToolCall(tc)).rejects.toThrow(/Absolute paths are not allowed/);
    });

    it("rejects parent traversal in write tool", async () => {
			(agentService.executeAgentTool as Mock).mockRejectedValueOnce(
				new Error("path escapes project root"),
			);
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
			(agentService.executeAgentTool as Mock).mockResolvedValueOnce(
				backendExecutionResult("Read src/sub/file.ts: ok"),
			);
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "src/sub/file.ts",
        status: "pending",
      };
      const out = await executeToolCall(tc);
			expect(agentService.executeAgentTool).toHaveBeenCalledWith(expect.objectContaining({
				arguments: { path: "src/sub/file.ts" },
			}));
			expect(fileService.readFile).not.toHaveBeenCalled();
      expect(out).toContain("Read src/sub/file.ts");
    });

    it("normalizes ./ prefix in paths", async () => {
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "./src/a.ts",
        status: "pending",
      };
      await executeToolCall(tc);
			expect(agentService.executeAgentTool).toHaveBeenCalledWith(expect.objectContaining({
				arguments: { path: "./src/a.ts" },
			}));
    });

    it("allows .. within project bounds (src/../lib/b.ts → lib/b.ts)", async () => {
      const tc: ToolCall = {
        id: "1",
        kind: "read",
        target: "src/../lib/b.ts",
        status: "pending",
      };
      await executeToolCall(tc);
			expect(agentService.executeAgentTool).toHaveBeenCalledWith(expect.objectContaining({
				arguments: { path: "src/../lib/b.ts" },
			}));
    });

    it("listRegisteredTools returns all built-in tool kinds", () => {
      const kinds = listRegisteredTools();
      expect(kinds).toContain("read");
      expect(kinds).toContain("write");
      expect(kinds).toContain("run");
      expect(kinds).toContain("search");
    });

    it("runs a command and returns the result summary", async () => {
			(agentService.executeAgentTool as Mock).mockResolvedValueOnce(
				backendExecutionResult("Ran: go test\nExit code: 0 (100ms)\nstdout:\nok"),
			);
      const tc: ToolCall = {
        id: "1",
        kind: "run",
        target: "go test",
        status: "pending",
      };
      const out = await executeToolCall(tc);
			expect(agentService.executeAgentTool).toHaveBeenCalledWith(expect.objectContaining({
				toolId: "run", arguments: { command: "go test" },
			}));
      expect(out).toContain("Ran: go test");
      expect(out).toContain("Exit code: 0");
      expect(out).toContain("stdout:");
      expect(out).toContain("ok");
    });

    it("searches and returns match summary", async () => {
			(agentService.executeAgentTool as Mock).mockResolvedValueOnce(
				backendExecutionResult("Found 1 match for TODO: a.ts:1:0 TODO: fix"),
			);
      const tc: ToolCall = {
        id: "1",
        kind: "search",
        target: "TODO",
        status: "pending",
      };
      const out = await executeToolCall(tc);
			expect(agentService.executeAgentTool).toHaveBeenCalledWith(expect.objectContaining({
				toolId: "search", arguments: { query: "TODO", ignoreCase: true },
			}));
			expect(searchService.search).not.toHaveBeenCalled();
      expect(out).toContain("Found 1 match");
      expect(out).toContain("a.ts:1:0");
    });

    it("returns no-matches message when search finds nothing", async () => {
			(agentService.executeAgentTool as Mock).mockResolvedValueOnce(
				backendExecutionResult("No matches for nothing"),
			);
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
			(agentService.executeAgentTool as Mock).mockResolvedValueOnce(
				backendExecutionResult("Read a.ts: content"),
			);
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
			(agentService.executeAgentTool as Mock).mockRejectedValueOnce(
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
      expect(result).toContain("Available tools:");
      expect(result).toContain("`read`");
      expect(result).toContain("`write`");
      expect(result).not.toContain("`read:`");
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
    it("does not execute a stale tool-call object after its conversation authority is reset", async () => {
      onNativeToolCalls([{
        id: "stale-after-reset",
        name: "read",
        arguments: '{"path":"README.md"}',
      }]);
      const staleCall = agentState.pendingToolCalls[0];
      clearPendingToolCalls();
      vi.clearAllMocks();

      await approveAndFeed(staleCall);

      const { sendMessage, sendNativeToolResults } = await import("@/stores/ai");
      expect(agentService.executeAgentTool).not.toHaveBeenCalled();
      expect(sendMessage).not.toHaveBeenCalled();
      expect(sendNativeToolResults).not.toHaveBeenCalled();
      expect(staleCall.status).toBe("pending");
    });

    it("waits for the busy terminal event after done before sending one observation", async () => {
      vi.useFakeTimers();
      try {
        // ai:done clears streaming first; ai:stream-busy=false is dispatched
        // afterward by the backend cleanup defer.
        aiStreamingState.streaming = false;
        aiStreamingState.globalStreamBusy = true;
        (agentService.executeAgentTool as Mock).mockResolvedValueOnce(
          backendExecutionResult("Read a.ts: terminal barrier"),
        );
        const tc: ToolCall = {
          id: "done-before-busy-false",
          kind: "read",
          target: "a.ts",
          status: "pending",
        };
        const { sendMessage } = await import("@/stores/ai");

        const approval = approveAndFeed(tc);
        await vi.advanceTimersByTimeAsync(64);

        expect(agentService.executeAgentTool).not.toHaveBeenCalled();
        expect(sendMessage).not.toHaveBeenCalled();

        aiStreamingState.globalStreamBusy = false;
        await vi.advanceTimersByTimeAsync(16);
        await approval;

        expect(agentService.executeAgentTool).toHaveBeenCalledTimes(1);
        expect(sendMessage).toHaveBeenCalledTimes(1);
        expect(sendMessage).toHaveBeenCalledWith(
          "[Observation]\nRead a.ts: terminal barrier",
        );
      } finally {
        vi.useRealTimers();
      }
    });

    it("fails visibly instead of waiting forever when the busy terminal event is lost", async () => {
      vi.useFakeTimers();
      try {
        aiStreamingState.streaming = false;
        aiStreamingState.globalStreamBusy = true;
        const tc: ToolCall = {
          id: "lost-busy-false",
          kind: "read",
          target: "a.ts",
          status: "pending",
        };
        const { sendMessage } = await import("@/stores/ai");

        const approval = approveAndFeed(tc);
        await vi.advanceTimersByTimeAsync(5_100);
        await approval;

        expect(agentService.executeAgentTool).not.toHaveBeenCalled();
        expect(sendMessage).not.toHaveBeenCalled();
        expect(tc.status).toBe("pending");
        expect(notifyError).toHaveBeenCalledWith(
          expect.stringContaining("stream cleanup did not complete"),
          "Agent Error",
        );
      } finally {
        vi.useRealTimers();
      }
    });

    it("does not lose a manual observation while the previous stream is active", async () => {
      aiStreamingState.streaming = true;
      (agentService.executeAgentTool as Mock).mockResolvedValueOnce(
        backendExecutionResult("Read a.ts: manual body"),
      );
      const tc: ToolCall = {
        id: "manual-active-turn",
        kind: "read",
        target: "a.ts",
        status: "pending",
      };
      const { sendMessage } = await import("@/stores/ai");
      const approval = approveAndFeed(tc);
      await Promise.resolve();
      expect(tc.status).toBe("pending");
      expect(agentService.executeAgentTool).not.toHaveBeenCalled();
      expect(sendMessage).not.toHaveBeenCalled();

      aiStreamingState.streaming = false;
      await approval;

      expect(tc.status).toBe("executed");
      expect(tc.result).toBe("Read a.ts: manual body");
      expect(sendMessage).toHaveBeenCalledTimes(1);
      expect(sendMessage).toHaveBeenCalledWith(
        "[Observation]\nRead a.ts: manual body",
      );
    });

    it("drops a queued observation when the agent turn is reset", async () => {
      aiStreamingState.streaming = true;
      const tc: ToolCall = {
        id: "stale-turn",
        kind: "read",
        target: "old.ts",
        status: "pending",
      };
      const { sendMessage } = await import("@/stores/ai");
      const approval = approveAndFeed(tc);
      await Promise.resolve();

      // Switching conversation/workspace invalidates queued work. The old
      // task may still be waiting for the provider's terminal event, but it
      // must not execute or feed its result into the new turn.
      resetAgentSession();
      aiStreamingState.streaming = false;
      await approval;

      expect(agentService.executeAgentTool).not.toHaveBeenCalled();
      expect(sendMessage).not.toHaveBeenCalled();
      expect(tc.status).toBe("pending");
    });

    it("executes the tool call and feeds the observation back to AI", async () => {
			(agentService.executeAgentTool as Mock).mockResolvedValueOnce(
				backendExecutionResult("Read a.ts: file body"),
			);
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
			(agentService.executeAgentTool as Mock).mockRejectedValueOnce(new Error("boom"));
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

  describe("G33 backend ToolDef catalog", () => {
    it("projects built-in definitions and schemas from one backend snapshot", () => {
      const tools = getRegisteredTools();
      expect(tools.map((tool) => tool.kind)).toEqual(["read", "write", "run", "search", "codebase", "git.status", "git.diff", "plan"]);
      expect(tools.find((tool) => tool.kind === "read")?.schema).toMatchObject({
        dangerLevel: "safe",
        approval: "backend-policy",
        mutation: "none",
      });
      expect(buildNativeToolDefs().find((tool) => tool.function.name === "write")?.function.parameters)
        .toEqual(tools.find((tool) => tool.kind === "write")?.schema.inputSchema);
    });

    it("forbids renderer registration and replacement", () => {
      expect(() => registerTool({ kind: "read", schema: { description: "forged" } }))
        .toThrow(/renderer tool registration is forbidden/);
      expect(() => unregisterTool("read")).toThrow(/renderer tool unregistration is forbidden/);
      expect(listRegisteredTools()).toEqual(["read", "write", "run", "search", "codebase", "git.status", "git.diff", "plan"]);
    });

    it("uses the same nested schema for native and JSON fence calls", () => {
      const catalog = builtinCatalog(9);
      catalog.tools.push({
        id: "mcp.server.echo.tool",
        wireName: "mcp_server_echo_abcd",
        description: "Echo nested payload",
        inputSchema: {
          type: "object",
          properties: {
            payload: {
              type: "object",
              properties: { text: { type: "string" } },
              required: ["text"],
              additionalProperties: false,
            },
          },
          required: ["payload"],
          additionalProperties: false,
        },
        source: "mcp",
        risk: "elevated",
        approval: "manual",
        mutation: "external",
      });
      __setAgentToolCatalogForTests(catalog);
      const native = buildNativeToolDefs().find((tool) => tool.function.name === "mcp_server_echo_abcd");
      expect(native?.function.parameters).toEqual(catalog.tools.find((tool) => tool.id === "mcp.server.echo.tool")?.inputSchema);
      const [fence] = parseToolCalls("```tool\nmcp_server_echo_abcd: {\"payload\":{\"text\":\"hello\"}}\n```");
      const [nativeCall] = parseNativeToolCalls([{ id: "mcp-call", name: "mcp_server_echo_abcd", arguments: "{\"payload\":{\"text\":\"hello\"}}" }]);
      expect(fence.arguments).toEqual(nativeCall.arguments);
      expect(fence.kind).toBe("mcp.server.echo.tool");
      expect(fence.catalogRevision).toBe(9);
    });

    it("rejects unknown, malformed, and schema-invalid native calls", () => {
      expect(parseNativeToolCalls([{ id: "unknown", name: "missing", arguments: "{}" }])).toEqual([]);
      expect(parseNativeToolCalls([{ id: "malformed", name: "read", arguments: "{" }])).toEqual([]);
      expect(parseNativeToolCalls([{ id: "missing-path", name: "read", arguments: "{}" }])).toEqual([]);
      expect(parseNativeToolCalls([{ id: "extra", name: "read", arguments: "{\"path\":\"a\",\"extra\":true}" }])).toEqual([]);
      expect(parseNativeToolCalls([{ name: "read", arguments: "{\"path\":\"a\"}" }])).toEqual([]);
    });

    it("dispatches only through the unified backend facade", async () => {
      const [call] = parseNativeToolCalls([{ id: "readme-call", name: "read", arguments: "{\"path\":\"README.md\"}" }]);
      const output = await executeToolCall(call);
      expect(output).toBe("backend observation");
      expect(agentService.executeAgentTool).toHaveBeenCalledWith({
        sessionId: agentState.sessionId,
        catalogRevision: 1,
        toolId: "read",
        arguments: { path: "README.md" },
      });
      expect(fileService.readFile).not.toHaveBeenCalled();
      expect(searchService.search).not.toHaveBeenCalled();
    });

    it("clears the projection when catalog refresh fails", async () => {
      (agentService.getToolCatalog as Mock).mockRejectedValueOnce(new Error("offline"));
      await expect(refreshAgentToolCatalog()).rejects.toThrow("offline");
      expect(listRegisteredTools()).toEqual([]);
      expect(agentState.catalogLoaded).toBe(false);
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
        expect(calls[0]).toMatchObject({ kind: "read", target: "main.go", status: "pending", source: "native" });
        expect(calls[1]).toMatchObject({ kind: "write", target: "a.ts", content: "x", source: "native" });
        expect(calls[2]).toMatchObject({ kind: "run", target: "go test", source: "native" });
      });

      it("onNativeToolCalls enqueues pending tools", () => {
        agentState.mode = "agent";
        const n = onNativeToolCalls([
          { id: "search-call", name: "search", arguments: JSON.stringify({ query: "TODO" }) },
        ]);
        expect(n).toBe(1);
        expect(agentState.pendingToolCalls).toHaveLength(1);
        expect(agentState.pendingToolCalls[0].kind).toBe("search");
        expect(agentState.toolCallCount).toBe(1);
      });

      it("treats an exact native event replay as idempotent", () => {
        agentState.mode = "agent";
        const payload = [
          { id: "replayed-call", name: "read", arguments: JSON.stringify({ path: "README.md" }) },
        ];

        expect(onNativeToolCalls(payload)).toBe(1);
        expect(onNativeToolCalls(payload)).toBe(0);
        expect(agentState.pendingToolCalls).toHaveLength(1);
        expect(agentState.pendingToolCalls[0].source).toBe("native");
        expect(agentState.toolCallCount).toBe(1);
      });

      it("fails closed when a provider reuses a native call ID", () => {
        agentState.mode = "agent";
        expect(onNativeToolCalls([
          { id: "conflicting-call", name: "read", arguments: JSON.stringify({ path: "a.ts" }) },
        ])).toBe(1);

        expect(onNativeToolCalls([
          { id: "conflicting-call", name: "read", arguments: JSON.stringify({ path: "b.ts" }) },
        ])).toBe(-1);
        expect(agentState.pendingToolCalls).toHaveLength(1);
        expect(agentState.pendingToolCalls[0].target).toBe("a.ts");
        expect(agentState.toolCallCount).toBe(1);
      });

      it("does not parse fenced compatibility calls after a native event", () => {
        const count = onAssistantFinished("```\nread: duplicate.ts\n```", true);
        expect(count).toBe(0);
        expect(agentState.pendingToolCalls).toEqual([]);
      });

      it("holds native auto-approval until the AI turn is done and feeds once", async () => {
        agentState.mode = "agent";
        appState.agentPermissionMode = "assist";
        aiStreamingState.streaming = true;

        let resolveExecution!: (value: ReturnType<typeof backendExecutionResult>) => void;
        (agentService.executeAgentTool as Mock).mockImplementationOnce(
          () => new Promise((resolve) => { resolveExecution = resolve; }),
        );
        const { sendMessage, sendNativeToolResults } = await import("@/stores/ai");
        const observedStates: string[] = [];
        const stopWatching = watchEffect(() => {
          const call = agentState.pendingToolCalls[0];
          if (call) observedStates.push(`${call.status}:${call.result ?? ""}`);
        });

        expect(onNativeToolCalls([
          { id: "native-1", name: "read", arguments: JSON.stringify({ path: "a.ts" }) },
        ])).toBe(1);
        await nextTick();
        expect(agentState.pendingToolCalls[0].status).toBe("pending");
        expect(agentService.executeAgentTool).not.toHaveBeenCalled();
        expect(sendMessage).not.toHaveBeenCalled();

        aiStreamingState.streaming = false;
        await vi.waitFor(() => {
          expect(agentState.pendingToolCalls[0].status).toBe("approved");
        });
        expect(agentService.executeAgentTool).toHaveBeenCalledTimes(1);
        expect(sendMessage).not.toHaveBeenCalled();
        expect(observedStates).toContain("approved:");

        resolveExecution(backendExecutionResult("Read a.ts: file body"));
        await vi.waitFor(() => {
          expect(agentState.pendingToolCalls[0].status).toBe("executed");
        });
        await vi.waitFor(() => expect(sendNativeToolResults).toHaveBeenCalledTimes(1));
        expect(agentState.pendingToolCalls[0].result).toBe("Read a.ts: file body");
        expect(sendNativeToolResults).toHaveBeenCalledWith([{
          toolCallId: "native-1",
          content: "Read a.ts: file body",
          isError: false,
        }]);
        expect(sendMessage).not.toHaveBeenCalled();
        expect(observedStates.some((state) => state.startsWith("executed:Read a.ts"))).toBe(true);
        stopWatching();
      });

		it("batches one native turn into one ordered call-id observation", async () => {
			agentState.mode = "agent";
            appState.agentPermissionMode = "assist";
			aiStreamingState.streaming = true;
			const order: string[] = [];
			(agentService.executeAgentTool as Mock)
				.mockImplementationOnce(async (request: { toolId: string }) => {
					order.push(`execute:${request.toolId}`);
					return backendExecutionResult("Read a.ts: alpha");
				})
				.mockImplementationOnce(async (request: { toolId: string }) => {
					order.push(`execute:${request.toolId}`);
					return backendExecutionResult("Search TODO: beta");
				});
			const { sendMessage, sendNativeToolResults } = await import("@/stores/ai");
			(sendNativeToolResults as Mock).mockImplementationOnce(async () => {
				order.push("send");
			});

			expect(onNativeToolCalls([
				{ id: "multi-read", name: "read", arguments: JSON.stringify({ path: "a.ts" }) },
				{ id: "multi-search", name: "search", arguments: JSON.stringify({ query: "TODO", ignoreCase: true }) },
			])).toBe(2);
			await nextTick();
			expect(agentService.executeAgentTool).not.toHaveBeenCalled();
			expect(sendMessage).not.toHaveBeenCalled();

			aiStreamingState.streaming = false;
			await vi.waitFor(() => expect(agentService.executeAgentTool).toHaveBeenCalledTimes(2));
			await vi.waitFor(() => expect(sendNativeToolResults).toHaveBeenCalledTimes(1));

			expect(order).toEqual(["execute:read", "execute:search", "send"]);
			expect(sendMessage).not.toHaveBeenCalled();
			expect(sendNativeToolResults).toHaveBeenCalledWith([
				{ toolCallId: "multi-read", content: "Read a.ts: alpha", isError: false },
				{ toolCallId: "multi-search", content: "Search TODO: beta", isError: false },
			]);
		});

		it("waits for manual calls before feeding a mixed native batch", async () => {
			agentState.mode = "agent";
            appState.agentPermissionMode = "assist";
			(agentService.executeAgentTool as Mock)
				.mockResolvedValueOnce(backendExecutionResult("Read a.ts: before"))
				.mockResolvedValueOnce(backendExecutionResult("Wrote a.ts"));
			const { sendMessage, sendNativeToolResults } = await import("@/stores/ai");

			expect(onNativeToolCalls([
				{ id: "mixed-read", name: "read", arguments: JSON.stringify({ path: "a.ts" }) },
				{ id: "mixed-write", name: "write", arguments: JSON.stringify({ path: "a.ts", content: "after" }) },
			])).toBe(2);
			await vi.waitFor(() => expect(agentState.pendingToolCalls[0].status).toBe("executed"));
			expect(agentState.pendingToolCalls[1].status).toBe("pending");
			expect(agentService.executeAgentTool).toHaveBeenCalledTimes(1);
			expect(sendMessage).not.toHaveBeenCalled();

			await approveAndFeed(agentState.pendingToolCalls[1]);

			expect(agentState.pendingToolCalls[1].status).toBe("executed");
			expect(agentService.executeAgentTool).toHaveBeenCalledTimes(2);
			expect(sendMessage).not.toHaveBeenCalled();
			expect(sendNativeToolResults).toHaveBeenCalledWith([
				{ toolCallId: "mixed-read", content: "Read a.ts: before", isError: false },
				{ toolCallId: "mixed-write", content: "Wrote a.ts", isError: false },
			]);
		});

		it("includes a manual rejection in the single batched observation", async () => {
			agentState.mode = "agent";
            appState.agentPermissionMode = "assist";
			(agentService.executeAgentTool as Mock).mockResolvedValueOnce(
				backendExecutionResult("Read a.ts: unchanged"),
			);
			const { sendMessage, sendNativeToolResults } = await import("@/stores/ai");

			expect(onNativeToolCalls([
				{ id: "reject-read", name: "read", arguments: JSON.stringify({ path: "a.ts" }) },
				{ id: "reject-write", name: "write", arguments: JSON.stringify({ path: "a.ts", content: "unsafe" }) },
			])).toBe(2);
			await vi.waitFor(() => expect(agentState.pendingToolCalls[0].status).toBe("executed"));
			expect(sendMessage).not.toHaveBeenCalled();

			await rejectAndFeed(agentState.pendingToolCalls[1]);

			expect(agentState.pendingToolCalls[1].status).toBe("rejected");
			expect(agentService.executeAgentTool).toHaveBeenCalledTimes(1);
			expect(sendMessage).not.toHaveBeenCalled();
			expect(sendNativeToolResults).toHaveBeenCalledWith([
				{ toolCallId: "reject-read", content: "Read a.ts: unchanged", isError: false },
				expect.objectContaining({
					toolCallId: "reject-write",
					content: expect.stringContaining("User rejected the write action"),
					isError: true,
				}),
			]);
		});
      it("rejects a single native write into an error result without fallback messaging", async () => {
        agentState.mode = "agent";
        appState.agentPermissionMode = "always-ask";
        const { sendMessage, sendNativeToolResults } = await import("@/stores/ai");

        expect(onNativeToolCalls([
          { id: "native-reject-write", name: "write", arguments: JSON.stringify({ path: "a.ts", content: "unsafe" }) },
        ])).toBe(1);
        await rejectAndFeed(agentState.pendingToolCalls[0]);

        expect(agentState.pendingToolCalls[0].status).toBe("rejected");
        expect(agentService.executeAgentTool).not.toHaveBeenCalled();
        expect(sendMessage).not.toHaveBeenCalled();
        expect(sendNativeToolResults).toHaveBeenCalledWith([expect.objectContaining({
          toolCallId: "native-reject-write",
          content: expect.stringContaining("User rejected the write action"),
          isError: true,
        })]);
      });

      it("feeds a backend execution error for a single native read without fallback messaging", async () => {
        agentState.mode = "agent";
        appState.agentPermissionMode = "assist";
        (agentService.executeAgentTool as Mock).mockRejectedValueOnce(new Error("backend read failed"));
        const { sendMessage, sendNativeToolResults } = await import("@/stores/ai");

        expect(onNativeToolCalls([
          { id: "native-error-read", name: "read", arguments: JSON.stringify({ path: "missing.ts" }) },
        ])).toBe(1);
        await vi.waitFor(() => expect(agentState.pendingToolCalls[0].status).toBe("error"));
        await vi.waitFor(() => expect(sendNativeToolResults).toHaveBeenCalledTimes(1));

        expect(agentService.executeAgentTool).toHaveBeenCalledTimes(1);
        expect(agentState.pendingToolCalls[0].error).toContain("backend read failed");
        expect(sendMessage).not.toHaveBeenCalled();
        expect(sendNativeToolResults).toHaveBeenCalledWith([expect.objectContaining({
          toolCallId: "native-error-read",
          content: expect.stringContaining("Error executing read"),
          isError: true,
        })]);
      });

      it("drops a native call waiting for stream idle after the agent session is reset", async () => {
        vi.useFakeTimers();
        try {
          agentState.mode = "agent";
          appState.agentPermissionMode = "assist";
          aiStreamingState.streaming = true;
          const { sendMessage, sendNativeToolResults } = await import("@/stores/ai");

          expect(onNativeToolCalls([
            { id: "native-reset-read", name: "read", arguments: JSON.stringify({ path: "stale.ts" }) },
          ])).toBe(1);
          await nextTick();
          expect(agentService.executeAgentTool).not.toHaveBeenCalled();

          resetAgentSession();
          aiStreamingState.streaming = false;
          await vi.advanceTimersByTimeAsync(100);

          expect(agentService.executeAgentTool).not.toHaveBeenCalled();
          expect(sendMessage).not.toHaveBeenCalled();
          expect(sendNativeToolResults).not.toHaveBeenCalled();
          expect(agentState.pendingToolCalls[0].status).toBe("pending");
        } finally {
          vi.useRealTimers();
        }
      });
     });


  it("allows a completed tool to be proposed again in a later turn", async () => {
    __setAgentToolCatalogForTests(builtinCatalog());
    const first = onAssistantFinished("```\nread: a.ts\n```");
    expect(first).toBe(1);
    const completed = agentState.pendingToolCalls[0];
    expect(completed).toBeDefined();
    completed.status = "executed";
    completed.result = "same file, refreshed contents";

    const second = onAssistantFinished("```\nread: a.ts\n```");
    expect(second).toBe(1);
    expect(agentState.pendingToolCalls).toHaveLength(2);
  });
});
