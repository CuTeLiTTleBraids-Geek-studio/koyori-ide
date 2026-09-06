import { describe, it, expect, beforeEach, vi } from "vitest";
import { nextTick, watch } from "vue";

vi.mock("@/lib/monaco-themes", () => ({
  accentThemes: [],
  applyMonacoTheme: vi.fn(),
  registerAllThemes: vi.fn(),
}));

// Collect event handlers so tests can simulate backend events.
// vi.hoisted ensures this runs before mock factories are evaluated.
const { eventHandlers, testAgentCatalog } = vi.hoisted(() => ({
  eventHandlers: {} as Record<string, ((...args: any[]) => void) | undefined>,
  testAgentCatalog: {
    revision: 1,
    tools: [
      {
        id: "read",
        wireName: "read",
        description: "Read a file",
        inputSchema: {
          type: "object",
          properties: { path: { type: "string", minLength: 1 } },
          required: ["path"],
          additionalProperties: false,
        },
        source: "builtin",
        risk: "read-only",
        approval: "backend-policy",
        mutation: "none",
      },
    ],
  },
}));

vi.mock("@wailsio/runtime", () => ({
  Events: {
    On: vi.fn((event: string, handler: (...args: any[]) => void) => {
      eventHandlers[event] = handler;
      return () => undefined;
    }),
    Emit: vi.fn(),
  },
}));

vi.mock("@/api/services", () => ({
  aiService: {
    setConfig: vi.fn().mockResolvedValue(undefined),
    startStream: vi.fn().mockResolvedValue("stream-test-1"),
    startAgentStream: vi.fn().mockResolvedValue({
      streamId: "agent-stream-test-1",
      sessionId: "agent-session-test-1",
    }),
    stopStream: vi.fn().mockResolvedValue(undefined),
    send: vi.fn().mockResolvedValue({ Content: "ok", FinishReason: "stop" }),
    getPresetPrompt: vi.fn().mockResolvedValue("Explain this code."),
    getDefaultSystemPrompt: vi.fn().mockResolvedValue("default prompt"),
    listPresets: vi.fn().mockResolvedValue([]),
    generateTitleWithAI: vi.fn().mockResolvedValue("AI generated title"),
    getAgentSystemPrompt: vi.fn().mockResolvedValue("agent prompt"),
  },
  agentService: {
    getToolCatalog: vi.fn().mockResolvedValue(testAgentCatalog),
  },
  conversationService: {
    save: vi.fn().mockResolvedValue(undefined),
    load: vi.fn().mockResolvedValue({ id: "1", title: "test", created_at: 0, messages: [] }),
    generateId: vi.fn().mockResolvedValue("new-id"),
    generateTitle: vi.fn().mockResolvedValue("test title"),
  },
  searchService: {
    search: vi.fn().mockResolvedValue([]),
  },
}));

vi.mock("@/lib/notifications", () => ({
  notify: vi.fn(),
  notifySuccess: vi.fn(),
  notifyError: vi.fn(),
  notifyWarning: vi.fn(),
  notifyInfo: vi.fn(),
}));

vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
}));

import {
  aiState,
  sendMessage,
  stopGeneration,
  attachContext,
  clearContext,
  runAIAction,
  clearMessages,
  setSystemPromptOverride,
  loadConversation,
  parseAIStreamPayload,
  isOwnedStreamEvent,
  MAX_AI_MESSAGES,
  MAX_PRE_ADMISSION_STREAM_EVENTS,
  MAX_PRE_ADMISSION_STREAM_BYTES,
  STREAM_TIMEOUT_MS,
  cleanupAIEventListeners,
  ensureAIEventListeners,
  handleAIChunkEvent,
  handleAIDoneEvent,
  handleAIErrorEvent,
  handleAIToolCallsEvent,
  handleAIReasoningEvent,
  handleAIStreamBusyEvent,
  handleConversationSavedEvent,
  sendNativeToolResults,
  flushConversationPersistence,
  persistConversationNow,
  resetStreamState,
  addContextChip,
  resolveCodebaseChips,
} from "./ai";

import { aiService, conversationService, searchService } from "@/api/services";
import { notifyError } from "@/lib/notifications";
import { pushOutput } from "@/stores/output";
import { agentState } from "./agent";
import { agentTimelineState, resetAgentTimeline } from "./agentTimeline";
import { personaState } from "./persona";
import { aiPlanState } from "./aiPlan";
import { appState } from "@/stores/app";
import type { Conversation } from "@/types";

eventHandlers["ai:chunk"] = handleAIChunkEvent;
eventHandlers["ai:done"] = handleAIDoneEvent;
eventHandlers["ai:error"] = handleAIErrorEvent;
eventHandlers["ai:tool_calls"] = handleAIToolCallsEvent;
eventHandlers["ai:reasoning"] = handleAIReasoningEvent;
eventHandlers["ai:stream-busy"] = handleAIStreamBusyEvent;

describe("ai store", () => {
  beforeEach(() => {
    resetStreamState();
    ensureAIEventListeners();
    aiState.messages = [];
    aiState.streaming = false;
    aiState.globalStreamBusy = false;
    aiState.activeStreamId = null;
    aiState.error = null;
    aiState.context = null;
    aiState.currentConversationId = null;
    aiState.currentConversationTitle = null;
    aiState.mentionedFiles = [];
    aiState.currentSystemPromptOverride = null;
    // M-16: 重置 stale 标志与 revision，避免跨用例污染。
    aiState.conversationStaleWhileStreaming = false;
    aiState.conversationRevision = 0;
    aiState.contextChips = [];
    // G12: 重置 persona/plan 状态，避免跨用例污染。
    personaState.personas = [];
    personaState.activePersonaId = null;
    aiPlanState.activePlan = null;
    agentState.mode = "chat";
    agentState.pendingToolCalls = [];
    agentState.toolCallCount = 0;
    vi.mocked(aiService.setConfig).mockClear();
    vi.mocked(aiService.startStream).mockReset().mockResolvedValue("stream-test-1");
    vi.mocked(aiService.startAgentStream).mockReset().mockResolvedValue({
      streamId: "agent-stream-test-1",
      sessionId: "agent-session-test-1",
    });
    vi.mocked(aiService.stopStream).mockReset().mockResolvedValue(undefined);
    vi.mocked(conversationService.load).mockReset().mockResolvedValue({
      id: "1",
      title: "test",
      created_at: 0,
      updated_at: 0,
      revision: 0,
      messages: [],
    });
    vi.mocked(notifyError).mockClear();
    vi.mocked(pushOutput).mockClear();
  });

  it("waits for backend config admission before starting a stream", async () => {
    let resolveConfig!: () => void;
    const deferredConfig = new Promise<void>((resolve) => { resolveConfig = resolve; });
    vi.mocked(aiService.setConfig).mockImplementationOnce(
      () => deferredConfig as unknown as ReturnType<typeof aiService.setConfig>,
    );

    const sending = sendMessage("wait for config");
    await vi.waitFor(() => expect(aiService.setConfig).toHaveBeenCalledOnce());
    expect(await sendMessage("duplicate while admitting")).toBe(false);
    expect(aiService.startStream).not.toHaveBeenCalled();
    expect(aiService.startAgentStream).not.toHaveBeenCalled();

    resolveConfig();
    expect(await sending).toBe(true);

    expect(aiService.startStream).toHaveBeenCalledOnce();
  });

  it("fails closed when backend config admission rejects", async () => {
    vi.mocked(aiService.setConfig).mockRejectedValueOnce(new Error("config rejected"));

    expect(await sendMessage("do not start")).toBe(false);

    expect(aiService.startStream).not.toHaveBeenCalled();
    expect(aiService.startAgentStream).not.toHaveBeenCalled();
    expect(aiState.streaming).toBe(false);
    expect(aiState.activeStreamId).toBeNull();
    expect(notifyError).toHaveBeenCalledWith("config rejected", "AI Error");
    expect(aiState.messages).toEqual([]);
  });

  it("routes the first stream chunk through the reactive assistant message", async () => {
    const sending = sendMessage("show the first token");
    await sending;

    const assistant = aiState.messages[aiState.messages.length - 1];
    expect(assistant?.role).toBe("assistant");
    const changes: string[] = [];
    const stop = watch(
      () => assistant?.content,
      (content) => { if (typeof content === "string") changes.push(content); },
    );

    handleAIChunkEvent({ data: { streamId: "stream-test-1", data: "first token" } });
    await nextTick();

    expect(assistant?.content).toBe("first token");
    expect(changes).toEqual(["first token"]);
    stop();
    handleAIDoneEvent({ data: { streamId: "stream-test-1", data: "" } });
  });

  it("rejects a conversation load after the first chunk and keeps rendering the owned stream", async () => {
    await sendMessage("keep this turn visible");
    handleAIChunkEvent({ data: { streamId: "stream-test-1", data: "first" } });
    const visibleAssistant = aiState.messages.at(-1);

    expect(await loadConversation("other-conversation")).toBe(false);
    expect(conversationService.load).not.toHaveBeenCalled();
    handleAIChunkEvent({ data: { streamId: "stream-test-1", data: " second" } });

    expect(aiState.messages.at(-1)).toBe(visibleAssistant);
    expect(aiState.messages.at(-1)?.content).toBe("first second");
    handleAIDoneEvent({ data: { streamId: "stream-test-1" } });
    handleAIStreamBusyEvent({ data: { streamId: "stream-test-1", busy: false } });
  });

  it("buffers pre-return events and replays only the returned stream in order", async () => {
    let resolveStart!: (streamId: string) => void;
    vi.mocked(aiService.startStream).mockImplementationOnce(
      () => new Promise<string>((resolve) => { resolveStart = resolve; }),
    );
    const save = vi.mocked((await import("@/api/services")).conversationService.save);
    save.mockClear();

    const sending = sendMessage("buffer events");
    await vi.waitFor(() => expect(aiState.streaming).toBe(true));
    eventHandlers["ai:chunk"]?.({ data: "legacy-no-id" });
    eventHandlers["ai:done"]?.({ data: "" });
    eventHandlers["ai:chunk"]?.({ data: { streamId: "foreign", data: "LEAK" } });
    eventHandlers["ai:error"]?.({ data: { streamId: "foreign", data: "foreign error" } });
    eventHandlers["ai:done"]?.({ data: { streamId: "foreign" } });
    eventHandlers["ai:chunk"]?.({ data: { streamId: "owned", data: "first" } });
    eventHandlers["ai:chunk"]?.({ data: { streamId: "owned", data: " second" } });
    eventHandlers["ai:done"]?.({ data: { streamId: "owned" } });

    expect(aiState.messages.at(-1)?.content).toBe("");
    expect(aiState.streaming).toBe(true);
    expect(aiState.error).toBeNull();
    expect(save).not.toHaveBeenCalled();
    expect(notifyError).not.toHaveBeenCalled();
    expect(pushOutput).not.toHaveBeenCalledWith("ai", "error", expect.any(String));

    resolveStart("owned");
    await sending;
    await vi.waitFor(() => expect(save).toHaveBeenCalled());

    expect(aiState.messages.at(-1)?.content).toBe("first second");
    expect(aiState.messages.at(-1)?.content).not.toContain("LEAK");
    expect(aiState.streaming).toBe(false);
    expect(aiState.error).toBeNull();
    expect(notifyError).not.toHaveBeenCalled();
  });

  it("shows only provider-declared reasoning summaries immediately", () => {
    agentState.mode = "agent";
    aiState.streaming = true;
    aiState.activeStreamId = "reasoning-stream";
    eventHandlers["ai:reasoning"]?.({ data: { streamId: "reasoning-stream", data: "checking files" } });
    eventHandlers["ai:reasoning"]?.({ data: { streamId: "foreign", data: "must not leak" } });
    expect(aiState.messages).toHaveLength(0);
    agentState.mode = "chat";
  });

  it("does not report a buffered matching error until stream admission", async () => {
    let resolveStart!: (streamId: string) => void;
    vi.mocked(aiService.startStream).mockImplementationOnce(
      () => new Promise<string>((resolve) => { resolveStart = resolve; }),
    );

    const sending = sendMessage("buffer error");
    await vi.waitFor(() => expect(aiState.streaming).toBe(true));
    eventHandlers["ai:error"]?.({ data: { streamId: "owned-error", data: "provider failed" } });

    expect(aiState.error).toBeNull();
    expect(aiState.streaming).toBe(true);
    expect(notifyError).not.toHaveBeenCalled();

    resolveStart("owned-error");
    await sending;

    expect(aiState.error).toBe("provider failed");
    expect(aiState.streaming).toBe(false);
    expect(notifyError).toHaveBeenCalledWith("provider failed", "AI Error");
  });

  it("buffers Agent tool calls until startAgentStream returns", async () => {
    let resolveStart!: (value: { streamId: string; sessionId: string }) => void;
    vi.mocked(aiService.startAgentStream).mockImplementationOnce(
      () => new Promise((resolve) => { resolveStart = resolve; }),
    );
    agentState.mode = "agent";

    const sending = sendMessage("use a tool");
    await vi.waitFor(() => expect(aiService.startAgentStream).toHaveBeenCalled());
    const call = [{ id: "call-1", name: "read", arguments: '{"path":"README.md"}' }];
    eventHandlers["ai:tool_calls"]?.({ data: { streamId: "foreign", data: call } });
    eventHandlers["ai:tool_calls"]?.({ data: { streamId: "agent-owned", data: call } });

    expect(agentState.pendingToolCalls).toHaveLength(0);
    expect(agentState.toolCallCount).toBe(0);

    resolveStart({ streamId: "agent-owned", sessionId: "agent-session-owned" });
    await sending;

    expect(agentState.pendingToolCalls).toHaveLength(1);
    expect(agentState.pendingToolCalls[0].target).toBe("README.md");
    expect(agentState.toolCallCount).toBe(1);
    eventHandlers["ai:done"]?.({ data: { streamId: "agent-owned" } });
    const assistant = aiState.messages.find((message) => message.role === "assistant");
    expect(assistant).toMatchObject({
      content: "",
      toolCalls: [{ id: "call-1", name: "read", arguments: '{"path":"README.md"}' }],
    });
    agentState.mode = "chat";
  });

  it("does not execute a fenced call when the same assistant turn emitted native calls", async () => {
    let resolveStart!: (value: { streamId: string; sessionId: string }) => void;
    vi.mocked(aiService.startAgentStream).mockImplementationOnce(
      () => new Promise((resolve) => { resolveStart = resolve; }),
    );
    agentState.mode = "agent";

    const sending = sendMessage("use native tools");
    await vi.waitFor(() => expect(aiService.startAgentStream).toHaveBeenCalled());
    resolveStart({ streamId: "native-mixed-stream", sessionId: "native-mixed-session" });
    await sending;

    eventHandlers["ai:tool_calls"]?.({
      data: {
        streamId: "native-mixed-stream",
        data: [{ id: "native-mixed-call", name: "read", arguments: '{"path":"a.ts"}' }],
      },
    });
    eventHandlers["ai:chunk"]?.({
      data: { streamId: "native-mixed-stream", data: "```\nread: duplicate.ts\n```" },
    });
    eventHandlers["ai:done"]?.({ data: { streamId: "native-mixed-stream" } });

    expect(agentState.pendingToolCalls).toHaveLength(1);
    expect(agentState.pendingToolCalls[0].source).toBe("native");
    expect(agentState.pendingToolCalls[0].target).toBe("a.ts");
    expect(agentState.toolCallCount).toBe(1);
    expect(aiState.messages.find((message) => message.role === "assistant")?.content)
      .toContain("duplicate.ts");
    agentState.mode = "chat";
  });

  it("fails closed on malformed native calls without using a fenced fallback", async () => {
    let resolveStart!: (value: { streamId: string; sessionId: string }) => void;
    vi.mocked(aiService.startAgentStream).mockImplementationOnce(
      () => new Promise((resolve) => { resolveStart = resolve; }),
    );
    agentState.mode = "agent";

    const sending = sendMessage("reject malformed native");
    await vi.waitFor(() => expect(aiService.startAgentStream).toHaveBeenCalled());
    resolveStart({ streamId: "malformed-native-stream", sessionId: "malformed-native-session" });
    await sending;

    eventHandlers["ai:tool_calls"]?.({
      data: {
        streamId: "malformed-native-stream",
        data: [{ id: "bad-native-call", name: "read", arguments: "{" }],
      },
    });
    eventHandlers["ai:chunk"]?.({
      data: { streamId: "malformed-native-stream", data: "```\nread: should-not-run.ts\n```" },
    });
    eventHandlers["ai:done"]?.({ data: { streamId: "malformed-native-stream" } });

    expect(agentState.pendingToolCalls).toEqual([]);
    expect(agentState.toolCallCount).toBe(0);
    expect(aiState.error).toBe("Provider returned an invalid native tool-call batch");
    agentState.mode = "chat";
  });

  it("continues an Agent turn with structured native tool results", async () => {
    agentState.mode = "agent";
    aiState.messages = [{
      id: "assistant-tool-call",
      role: "assistant",
      content: "",
      toolCalls: [{ id: "call-read", name: "read", arguments: '{"path":"README.md"}' }],
    }];

    expect(await sendNativeToolResults([{
      toolCallId: "call-read",
      content: "file body",
      isError: false,
    }])).toBe(true);

    expect(aiService.startAgentStream).toHaveBeenCalledWith(
      expect.any(String),
      [
        expect.objectContaining({
          role: "assistant",
          toolCalls: [{ id: "call-read", name: "read", arguments: '{"path":"README.md"}' }],
        }),
        expect.objectContaining({
          role: "tool",
          toolResults: [{ toolCallId: "call-read", content: "file body", isError: false }],
        }),
      ],
    );
    expect(aiState.messages.some((message) => message.role === "user" && message.content.includes("Observation"))).toBe(false);
  });

  it("rolls back rejected native tool-result admissions without removing provider history", async () => {
    agentState.mode = "agent";
    aiState.messages = [{
      id: "assistant-native-call",
      role: "assistant",
      content: "",
      toolCalls: [{ id: "call-rejected-result", name: "read", arguments: '{"path":"README.md"}' }],
    }];
    vi.mocked(aiService.startAgentStream)
      .mockRejectedValueOnce(new Error("stream busy"))
      .mockRejectedValueOnce(new Error("stream busy"));

    expect(await sendNativeToolResults([{
      toolCallId: "call-rejected-result",
      content: "provider result",
      isError: false,
    }])).toBe(false);

    expect(aiState.messages).toEqual([expect.objectContaining({
      id: "assistant-native-call",
      toolCalls: [expect.objectContaining({ id: "call-rejected-result" })],
    })]);
    expect(aiState.messages.some((message) => message.role === "tool")).toBe(false);
    expect(aiState.messages.some((message) => message.role === "assistant" && !message.toolCalls?.length)).toBe(false);
  });

  it("discards buffered events when stream start rejects and keeps the next send clean", async () => {
    let rejectStart!: (reason: Error) => void;
    vi.mocked(aiService.startStream).mockImplementationOnce(
      () => new Promise<string>((_resolve, reject) => { rejectStart = reject; }),
    );

    const failed = sendMessage("first attempt");
    await vi.waitFor(() => expect(aiState.streaming).toBe(true));
    eventHandlers["ai:chunk"]?.({ data: { streamId: "old", data: "STALE" } });
    rejectStart(new Error("stream busy"));
    expect(await failed).toBe(false);
    expect(aiState.messages.some((message) => message.content === "first attempt")).toBe(false);

    vi.mocked(aiService.startStream).mockResolvedValueOnce("next");
    await sendMessage("second attempt");
    eventHandlers["ai:chunk"]?.({ data: { streamId: "next", data: "fresh" } });
    eventHandlers["ai:done"]?.({ data: { streamId: "next" } });

    expect(aiState.messages.some((message) => message.content.includes("STALE"))).toBe(false);
    expect(aiState.messages.at(-1)?.content).toBe("fresh");
  });

  it("discards buffered events when reset invalidates a pending admission", async () => {
    let resolveStart!: (streamId: string) => void;
    vi.mocked(aiService.startStream).mockImplementationOnce(
      () => new Promise<string>((resolve) => { resolveStart = resolve; }),
    );
    const sending = sendMessage("reset attempt");
    await vi.waitFor(() => expect(aiState.streaming).toBe(true));
    eventHandlers["ai:chunk"]?.({ data: { streamId: "old", data: "STALE" } });
    resetStreamState();
    resolveStart("old");
    expect(await sending).toBe(false);
    await sending;

    expect(aiState.messages.some((message) => message.content.includes("STALE"))).toBe(false);
    expect(aiState.messages.some((message) => message.content === "reset attempt")).toBe(false);
    expect(aiState.streaming).toBe(false);
    expect(aiState.activeStreamId).toBeNull();
  });

  it("drops pre-return events when stopGeneration invalidates the stream", async () => {
    let resolveStart!: (streamId: string) => void;
    vi.mocked(aiService.startStream).mockImplementationOnce(
      () => new Promise<string>((resolve) => { resolveStart = resolve; }),
    );

    const sending = sendMessage("stop attempt");
    await vi.waitFor(() => expect(aiState.streaming).toBe(true));
    eventHandlers["ai:chunk"]?.({ data: { streamId: "stopped", data: "STALE" } });
    await stopGeneration();
    resolveStart("stopped");
    await sending;

    expect(aiState.messages.some((message) => message.content.includes("STALE"))).toBe(false);
    expect(aiState.streaming).toBe(false);
    expect(aiState.activeStreamId).toBeNull();
  });

  it("fails closed without partial replay when the pre-return event limit is exceeded", async () => {
    let resolveStart!: (streamId: string) => void;
    vi.mocked(aiService.startStream).mockImplementationOnce(
      () => new Promise<string>((resolve) => { resolveStart = resolve; }),
    );

    const sending = sendMessage("too many events");
    await vi.waitFor(() => expect(aiState.streaming).toBe(true));
    for (let i = 0; i <= MAX_PRE_ADMISSION_STREAM_EVENTS; i += 1) {
      eventHandlers["ai:chunk"]?.({ data: { streamId: "owned", data: `chunk-${i}` } });
    }
    expect(aiState.messages.at(-1)?.content).toBe("");

    resolveStart("owned");
    await sending;

    expect(aiState.messages.some((message) => message.content.includes("chunk-"))).toBe(false);
    expect(aiState.streaming).toBe(false);
    expect(aiState.activeStreamId).toBeNull();
    expect(aiService.stopStream).toHaveBeenCalledTimes(1);
  });

  it("fails closed without partial replay when the pre-return byte limit is exceeded", async () => {
    let resolveStart!: (streamId: string) => void;
    vi.mocked(aiService.startStream).mockImplementationOnce(
      () => new Promise<string>((resolve) => { resolveStart = resolve; }),
    );

    const sending = sendMessage("oversized event");
    await vi.waitFor(() => expect(aiState.streaming).toBe(true));
    eventHandlers["ai:chunk"]?.({
      data: { streamId: "owned", data: "x".repeat(MAX_PRE_ADMISSION_STREAM_BYTES + 1) },
    });
    expect(aiState.messages.at(-1)?.content).toBe("");

    resolveStart("owned");
    await sending;

    expect(aiState.messages.some((message) => message.content.includes("x"))).toBe(false);
    expect(aiState.streaming).toBe(false);
    expect(aiService.stopStream).toHaveBeenCalledTimes(1);
  });

  it("sends a message and appends assistant response via events", async () => {
    const previousProvider = appState.aiProvider;
    const previousModel = appState.aiModel;
    const previousReasoningEffort = appState.reasoningEffort;
    appState.aiProvider = "openai";
    appState.aiModel = "gpt-5";
    appState.reasoningEffort = "high";
    try {
      const promise = sendMessage("hi");
      expect(await promise).toBe(true);
      expect(aiService.setConfig).toHaveBeenCalledWith(expect.objectContaining({
        provider: "openai",
        model: "gpt-5",
        reasoningEffort: "high",
        protocol: "openai",
      }));
      // prompt-6 Task 2: structured payloads with streamId
      const sid = aiState.activeStreamId || "stream-test-1";
      eventHandlers["ai:chunk"]?.({ data: { streamId: sid, data: "hello" } });
      eventHandlers["ai:chunk"]?.({ data: { streamId: sid, data: " world" } });
      eventHandlers["ai:done"]?.({ data: { streamId: sid, data: "" } });
      eventHandlers["ai:stream-busy"]?.({ data: { streamId: sid, busy: false } });

      expect(aiState.messages.length).toBe(2);
      expect(aiState.messages[0].role).toBe("user");
      expect(aiState.messages[1].role).toBe("assistant");
      expect(aiState.messages[1].content).toBe("hello world");
    } finally {
      appState.aiProvider = previousProvider;
      appState.aiModel = previousModel;
      appState.reasoningEffort = previousReasoningEffort;
    }
  });

  it("assigns a durable ID for a new conversation before the stream finishes", async () => {
    const sending = sendMessage("persist while streaming");
    await sending;
    expect(aiState.streaming).toBe(true);
    expect(aiState.currentConversationId).toBeNull();

    const persistedId = await persistConversationNow();

    expect(persistedId).toBe("new-id");
    expect(aiState.currentConversationId).toBe("new-id");
    expect(conversationService.save).toHaveBeenCalledWith(
      expect.objectContaining({ id: "new-id" }),
    );
    handleAIDoneEvent({ data: { streamId: "stream-test-1" } });
  });

  it("ignores chunks for a foreign streamId (prompt-6 Task 2)", async () => {
    const promise = sendMessage("hi");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    eventHandlers["ai:chunk"]?.({ data: { streamId: "other-stream", data: "LEAK" } });
    eventHandlers["ai:chunk"]?.({ data: { streamId: sid, data: "ok" } });
    eventHandlers["ai:done"]?.({ data: { streamId: sid, data: "" } });
    expect(aiState.messages[1].content).toBe("ok");
    expect(aiState.messages[1].content).not.toContain("LEAK");
  });

  it("includes context prefix when attached", async () => {
    attachContext({
      kind: "selection",
      filePath: "/test.ts",
      language: "typescript",
      content: "const x = 1;",
      startLine: 1,
      endLine: 1,
    });
    const promise = sendMessage("explain");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    eventHandlers["ai:done"]?.({ data: { streamId: sid, data: "" } });

    expect(aiState.messages[0].content).toContain("const x = 1");
    expect(aiState.messages[0].content).toContain("/test.ts");
    expect(aiState.messages[0].content).toContain("explain");
  });

  it("stops generation without clearing globalStreamBusy locally (Task 5)", async () => {
    aiState.streaming = true;
    aiState.globalStreamBusy = true;
    await stopGeneration();
    expect(aiState.streaming).toBe(false);
    // busy only cleared by backend event
    expect(aiState.globalStreamBusy).toBe(true);
    eventHandlers["ai:stream-busy"]?.({ data: { streamId: "x", busy: false } });
    expect(aiState.globalStreamBusy).toBe(false);
  });

  it("keeps stream ownership and the pending draft when stop fails so the user can retry", async () => {
    await sendMessage("long request");
    const streamId = aiState.activeStreamId;
    const assistant = aiState.messages.at(-1);
    vi.mocked(aiService.stopStream).mockRejectedValueOnce(new Error("stop denied"));

    expect(await stopGeneration()).toBe(false);
    expect(aiState.streaming).toBe(true);
    expect(aiState.activeStreamId).toBe(streamId);
    handleAIChunkEvent({ data: { streamId, data: "still owned" } });
    expect(aiState.messages.at(-1)).toBe(assistant);
    expect(assistant?.content).toBe("still owned");

    expect(await stopGeneration()).toBe(true);
    expect(aiState.streaming).toBe(false);
    expect(aiState.activeStreamId).toBeNull();
    expect(aiService.stopStream).toHaveBeenCalledTimes(2);
  });

  it("handles error event", async () => {
    const promise = sendMessage("hi");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    eventHandlers["ai:error"]?.({ data: { streamId: sid, data: "network error" } });

    expect(aiState.error).toBe("network error");
    expect(aiState.streaming).toBe(false);
  });

  it("parseAIStreamPayload accepts legacy string chunks", () => {
    const p = parseAIStreamPayload({ data: "token" });
    expect(p.data).toBe("token");
    expect(p.streamId).toBe("");
  });

  it("isOwnedStreamEvent rejects mismatch when activeStreamId set", () => {
    aiState.activeStreamId = "mine";
    expect(isOwnedStreamEvent("mine")).toBe(true);
    expect(isOwnedStreamEvent("theirs")).toBe(false);
    expect(isOwnedStreamEvent("")).toBe(true); // legacy while active
  });

  it("clears context", () => {
    aiState.context = { kind: "file", filePath: "/x", language: "go", content: "x" };
    clearContext();
    expect(aiState.context).toBe(null);
  });

  it("clears messages", () => {
    aiState.messages = [{ role: "user", content: "x", id: "message-to-clear" }];
    agentState.pendingToolCalls = [
      { id: "stale-tool", kind: "read", target: "README.md", status: "pending" },
    ];
    agentState.toolCallCount = 1;
    expect(clearMessages()).toBe(true);
    expect(aiState.messages).toHaveLength(0);
    expect(agentState.pendingToolCalls).toEqual([]);
    expect(agentState.toolCallCount).toBe(0);
  });

  // N-60: clearMessages also resets the system prompt override.
  it("clearMessages resets systemPromptOverride (N-60)", () => {
    aiState.currentSystemPromptOverride = "custom prompt";
    clearMessages();
    expect(aiState.currentSystemPromptOverride).toBeNull();
  });

  // N-60: setSystemPromptOverride sets a non-empty prompt.
  it("setSystemPromptOverride sets non-empty prompt (N-60)", () => {
    setSystemPromptOverride("You are a code reviewer.");
    expect(aiState.currentSystemPromptOverride).toBe("You are a code reviewer.");
  });

  // N-60: setSystemPromptOverride with null resets to null.
  it("setSystemPromptOverride with null resets to null (N-60)", () => {
    aiState.currentSystemPromptOverride = "custom";
    setSystemPromptOverride(null);
    expect(aiState.currentSystemPromptOverride).toBeNull();
  });

  // N-60: setSystemPromptOverride with empty/whitespace resets to null.
  it("setSystemPromptOverride with empty string resets to null (N-60)", () => {
    aiState.currentSystemPromptOverride = "custom";
    setSystemPromptOverride("   ");
    expect(aiState.currentSystemPromptOverride).toBeNull();
  });

  // N-60: loadConversation restores the override from the loaded conversation.
  it("loadConversation restores systemPromptOverride (N-60)", async () => {
    const { conversationService } = await import("@/api/services");
    (conversationService.load as any).mockResolvedValue({
      id: "conv-1",
      title: "test",
      created_at: 0,
      messages: [{ role: "user", content: "hi" }],
      system_prompt_override: "You are a senior engineer.",
    });
    await loadConversation("conv-1");
    expect(aiState.currentSystemPromptOverride).toBe("You are a senior engineer.");
  });

  // N-60: loadConversation with no override field sets null.
  it("loadConversation with no override sets null (N-60)", async () => {
    const { conversationService } = await import("@/api/services");
    (conversationService.load as any).mockResolvedValue({
      id: "conv-2",
      title: "test",
      created_at: 0,
      messages: [],
    });
    await loadConversation("conv-2");
    expect(aiState.currentSystemPromptOverride).toBeNull();
  });

  it("does not let an older conversation load overwrite a newer selection", async () => {
    const resolvers = new Map<string, (conversation: Conversation) => void>();
    vi.mocked(conversationService.load).mockImplementation(
      (id: string) => new Promise<Conversation>((resolve) => resolvers.set(id, resolve)),
    );

    const older = loadConversation("conv-older");
    const newer = loadConversation("conv-newer");
    resolvers.get("conv-newer")?.({
      id: "conv-newer",
      title: "newer",
      created_at: 2,
      updated_at: 2,
      revision: 1,
      messages: [{ role: "assistant", content: "new content" }],
    });
    await newer;
    resolvers.get("conv-older")?.({
      id: "conv-older",
      title: "older",
      created_at: 1,
      updated_at: 1,
      revision: 1,
      messages: [{ role: "assistant", content: "stale content" }],
    });
    await older;

    expect(aiState.currentConversationId).toBe("conv-newer");
    expect(aiState.messages.map((message) => message.content)).toEqual(["new content"]);
  });

  it("keeps a cleared conversation empty when an earlier load resolves late", async () => {
    let resolveLoad: ((conversation: Conversation) => void) | undefined;
    vi.mocked(conversationService.load).mockImplementationOnce(
      () => new Promise<Conversation>((resolve) => { resolveLoad = resolve; }),
    );

    const loading = loadConversation("conv-late");
    clearMessages();
    resolveLoad?.({
      id: "conv-late",
      title: "late",
      created_at: 1,
      updated_at: 1,
      revision: 1,
      messages: [{ role: "assistant", content: "must stay hidden" }],
    });
    await loading;

    expect(aiState.currentConversationId).toBeNull();
    expect(aiState.messages).toEqual([]);
  });

  it("does not let a late persist publish identity into a newly loaded conversation", async () => {
    let resolveGeneratedId!: (id: string) => void;
    vi.mocked(conversationService.generateId).mockImplementationOnce(
      () => new Promise<string>((resolve) => { resolveGeneratedId = resolve; }),
    );
    vi.mocked(conversationService.load).mockResolvedValueOnce({
      id: "target-conversation",
      title: "target title",
      created_at: 2,
      updated_at: 2,
      revision: 7,
      messages: [{ role: "assistant", content: "target content" }],
    });

    await sendMessage("old unsaved turn");
    handleAIChunkEvent({ data: { streamId: "stream-test-1", data: "old answer" } });
    handleAIDoneEvent({ data: { streamId: "stream-test-1", data: "" } });
    handleAIStreamBusyEvent({ data: { busy: false } });
    await vi.waitFor(() => expect(conversationService.generateId).toHaveBeenCalled());

    await loadConversation("target-conversation");
    resolveGeneratedId("late-old-id");
    await flushConversationPersistence();

    expect(aiState.currentConversationId).toBe("target-conversation");
    expect(aiState.currentConversationTitle).toBe("target title");
    expect(aiState.conversationRevision).toBe(7);
    expect(aiState.messages.map((message) => message.content)).toEqual(["target content"]);
  });

  it("does not let conversation:saved for the old identity preempt an in-flight target load", async () => {
    aiState.currentConversationId = "old-conversation";
    const loadCountBefore = vi.mocked(conversationService.load).mock.calls.length;
    let resolveTarget!: (conversation: Conversation) => void;
    vi.mocked(conversationService.load).mockImplementationOnce(
      () => new Promise<Conversation>((resolve) => { resolveTarget = resolve; }),
    );

    const loading = loadConversation("target-conversation");
    handleConversationSavedEvent({
      data: { origin: "peer-window", id: "old-conversation", revision: 9 },
    });

    expect(conversationService.load).toHaveBeenCalledTimes(loadCountBefore + 1);
    resolveTarget({
      id: "target-conversation",
      title: "target",
      created_at: 2,
      updated_at: 2,
      revision: 1,
      messages: [{ role: "assistant", content: "target wins" }],
    });
    await loading;
    expect(aiState.currentConversationId).toBe("target-conversation");
  });

  it("reloads a target when its saved revision arrives during the initial load", async () => {
    const loadCountBefore = vi.mocked(conversationService.load).mock.calls.length;
    let resolveInitial!: (conversation: Conversation) => void;
    vi.mocked(conversationService.load)
      .mockImplementationOnce(
        () => new Promise<Conversation>((resolve) => { resolveInitial = resolve; }),
      )
      .mockResolvedValueOnce({
        id: "target-live",
        title: "complete",
        created_at: 2,
        updated_at: 3,
        revision: 2,
        messages: [{ role: "assistant", content: "final streamed content" }],
      });

    const loading = loadConversation("target-live");
    handleConversationSavedEvent({
      data: { origin: "peer-window", id: "target-live", revision: 2 },
    });
    resolveInitial({
      id: "target-live",
      title: "partial",
      created_at: 2,
      updated_at: 2,
      revision: 1,
      messages: [{ role: "assistant", content: "partial streamed content" }],
    });

    expect(await loading).toBe(true);
    expect(conversationService.load).toHaveBeenCalledTimes(loadCountBefore + 2);
    expect(aiState.conversationRevision).toBe(2);
    expect(aiState.messages.map((message) => message.content)).toEqual([
      "final streamed content",
    ]);
  });

  it("coalesces saved revisions observed during one target load", async () => {
    const loadCountBefore = vi.mocked(conversationService.load).mock.calls.length;
    let resolveInitial!: (conversation: Conversation) => void;
    vi.mocked(conversationService.load)
      .mockImplementationOnce(
        () => new Promise<Conversation>((resolve) => { resolveInitial = resolve; }),
      )
      .mockResolvedValueOnce({
        id: "target-coalesced",
        title: "latest",
        created_at: 2,
        updated_at: 4,
        revision: 4,
        messages: [{ role: "assistant", content: "latest content" }],
      });

    const loading = loadConversation("target-coalesced");
    handleConversationSavedEvent({
      data: { origin: "peer-window", id: "target-coalesced", revision: 2 },
    });
    handleConversationSavedEvent({
      data: { origin: "peer-window", id: "target-coalesced", revision: 4 },
    });
    handleConversationSavedEvent({
      data: { origin: "peer-window", id: "target-coalesced", revision: 3 },
    });
    resolveInitial({
      id: "target-coalesced",
      title: "partial",
      created_at: 2,
      updated_at: 2,
      revision: 1,
      messages: [{ role: "assistant", content: "partial content" }],
    });

    expect(await loading).toBe(true);
    expect(conversationService.load).toHaveBeenCalledTimes(loadCountBefore + 2);
    expect(aiState.conversationRevision).toBe(4);
    expect(aiState.messages.map((message) => message.content)).toEqual(["latest content"]);
  });

  it("does not send while streaming", async () => {
    aiState.streaming = true;
    const before = aiState.messages.length;
    expect(await sendMessage("hi")).toBe(false);
    expect(aiState.messages.length).toBe(before);
  });

  it("runAIAction attaches context and sends", async () => {
    const promise = runAIAction("explain", "func foo() {}", "go", "/main.go");
    eventHandlers["ai:done"]?.();
    await promise;

    expect(aiState.messages.length).toBe(2);
    expect(aiState.messages[0].content).toContain("func foo() {}");
    expect(aiState.messages[0].content).toContain("Explain this code.");
  });

  // M-16: stream 异常中断时 conversationStaleWhileStreaming 标志应被重置。
  // 模拟：流式过程中 peer 保存了同一会话（stale 标志置 true），
  // 随后 ai:error 事件到达 —— 标志必须被无条件重置，避免后续 persist 误判。
  it("resets conversationStaleWhileStreaming on stream error (M-16)", async () => {
    const promise = sendMessage("hi");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    // 模拟流式过程中 peer 保存了同一会话
    aiState.conversationStaleWhileStreaming = true;
    // stream 异常中断
    eventHandlers["ai:error"]?.({ data: { streamId: sid, data: "stream failed" } });
    // M-16: stale 标志应被重置
    expect(aiState.conversationStaleWhileStreaming).toBe(false);
    expect(aiState.streaming).toBe(false);
  });

  // M-16: 超时兜底 —— 后端既未发 ai:done 也未发 ai:error 时，
  // STREAM_TIMEOUT_MS 后强制清理 streaming 状态。
  it("stream timeout fallback clears streaming state (M-16)", async () => {
    vi.useFakeTimers();
    try {
      const promise = sendMessage("hi");
      await promise;
      // 流已发起，streaming=true，超时定时器已启动
      expect(aiState.streaming).toBe(true);
      expect(aiState.globalStreamBusy).toBe(true);
      // 推进时间到超时之前 —— 状态不变
      vi.advanceTimersByTime(STREAM_TIMEOUT_MS - 1);
      expect(aiState.streaming).toBe(true);
      // 推进时间越过超时 —— 兜底逻辑强制清理
      vi.advanceTimersByTime(1);
      expect(aiState.streaming).toBe(false);
      expect(aiState.globalStreamBusy).toBe(false);
      expect(aiState.activeStreamId).toBe(null);
      expect(aiState.conversationStaleWhileStreaming).toBe(false);
      expect(aiState.error).toContain("timed out");
    } finally {
      vi.useRealTimers();
    }
  });

  // M-17: sendMessage 乐观设置 globalStreamBusy=true，当 startStream 失败时，
  // catch 块应重置 globalStreamBusy=false，避免 UI 永久卡死。
  it("resets globalStreamBusy on sendMessage failure (M-17)", async () => {
    const { aiService } = await import("@/api/services");
    (aiService.startStream as any).mockRejectedValueOnce(new Error("network down"));
    expect(await sendMessage("hi")).toBe(false);
    // M-17: 失败后 globalStreamBusy 必须被重置
    expect(aiState.globalStreamBusy).toBe(false);
    expect(aiState.streaming).toBe(false);
    expect(aiState.activeStreamId).toBe(null);
    expect(aiState.error).toContain("network down");
  });

  // N-NEW-1: cleanupAIEventListeners 必须重置流状态，避免 HMR / 测试 teardown
  // 后 UI 卡在 streaming=true（ai:done / ai:error 永不再到达）。
  it("cleanupAIEventListeners resets streaming state (N-NEW-1)", async () => {
    // 模拟流式传输中：streaming=true, globalStreamBusy=true, messages 中有空 assistant 占位
    const promise = sendMessage("hi");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    // 收到 chunk 但未收到 done → 流式中
    eventHandlers["ai:chunk"]?.({ data: { streamId: sid, data: "partial" } });
    eventHandlers["ai:stream-busy"]?.({ data: { streamId: sid, busy: true } });

    expect(aiState.streaming).toBe(true);
    expect(aiState.globalStreamBusy).toBe(true);
    expect(aiState.messages.length).toBeGreaterThanOrEqual(2);
    const assistantMsg = aiState.messages[aiState.messages.length - 1];
    expect(assistantMsg.role).toBe("assistant");

    // 调用 cleanup（模拟 HMR / teardown）— 不应留下 streaming=true
    cleanupAIEventListeners();

    expect(aiState.streaming).toBe(false);
    expect(aiState.globalStreamBusy).toBe(false);
  });

  it("cleanupAIEventListeners clears the active stream timeout (D4)", async () => {
    vi.useFakeTimers();
    try {
      await sendMessage("hi");
      expect(vi.getTimerCount()).toBeGreaterThan(0);

      cleanupAIEventListeners();

      expect(vi.getTimerCount()).toBe(0);
      expect(aiState.streaming).toBe(false);
      expect(aiState.activeStreamId).toBeNull();
    } finally {
      resetStreamState();
      vi.useRealTimers();
    }
  });

  it("cleanupAIEventListeners removes empty pending assistant message (N-NEW-1)", async () => {
    // 流式发起但还未收到任何 chunk → pendingAssistantMessage.content === ""
    const promise = sendMessage("hi");
    await promise;

    const messagesBefore = aiState.messages.length;
    // 不派发任何 chunk，直接 cleanup
    cleanupAIEventListeners();

    // 空 pending 消息应被移除（与 ai:done / ai:error 处理逻辑一致）
    expect(aiState.messages.length).toBeLessThanOrEqual(messagesBefore);
    expect(aiState.streaming).toBe(false);
    expect(aiState.globalStreamBusy).toBe(false);
  });

  it("does not revive a reset stream when startStream resolves late", async () => {
    const { aiService } = await import("@/api/services");
    let resolveStart!: (streamId: string) => void;
    (aiService.startStream as ReturnType<typeof vi.fn>).mockImplementationOnce(
      () => new Promise<string>((resolve) => { resolveStart = resolve; }),
    );

    const sending = sendMessage("hi");
    await vi.waitFor(() => expect(aiState.streaming).toBe(true));
    resetStreamState();
    resolveStart("late-stream");
    expect(await sending).toBe(false);

    expect(aiState.streaming).toBe(false);
    expect(aiState.globalStreamBusy).toBe(false);
    expect(aiState.activeStreamId).toBeNull();
  });

  it("ignores stale event callbacks after listener cleanup", async () => {
    const sending = sendMessage("hi");
    await sending;
    const staleChunk = eventHandlers["ai:chunk"];

    cleanupAIEventListeners();
    staleChunk?.({ data: { streamId: "stream-test-1", data: "late" } });

    expect(aiState.messages.some((message) => message.content.includes("late"))).toBe(false);
    expect(aiState.streaming).toBe(false);
    expect(aiState.activeStreamId).toBeNull();
  });

  it("re-enables centralized handlers after cleanup without registering Wails listeners", async () => {
    const { Events } = await import("@wailsio/runtime");
    const on = vi.mocked(Events.On);

    cleanupAIEventListeners();
    on.mockClear();
    ensureAIEventListeners();
    expect(on).not.toHaveBeenCalled();
    ensureAIEventListeners();
    expect(on).not.toHaveBeenCalled();
  });

  it("persists an existing conversation with expected_revision", async () => {
    const { conversationService } = await import("@/api/services");
    const save = vi.mocked(conversationService.save);
    save.mockClear();
    aiState.currentConversationId = "conv-cas";
    aiState.currentConversationTitle = "CAS";
    aiState.conversationRevision = 4;

    await sendMessage("update");
    const sid = aiState.activeStreamId || "stream-test-1";
    handleAIChunkEvent({ data: { streamId: sid, data: "answer" } });
    handleAIDoneEvent({ data: { streamId: sid, data: "" } });

    await vi.waitFor(() => expect(save).toHaveBeenCalled());
    expect(save).toHaveBeenCalledWith(expect.objectContaining({
      id: "conv-cas",
      expected_revision: 4,
    }));
  });

  it("forks local messages when conversation CAS conflicts", async () => {
    const { conversationService } = await import("@/api/services");
    const save = vi.mocked(conversationService.save);
    const generateId = vi.mocked(conversationService.generateId);
    save.mockReset();
    save.mockRejectedValueOnce(new Error("conversation revision conflict"));
    save.mockResolvedValueOnce(undefined);
    generateId.mockResolvedValueOnce("forked-conversation");
    aiState.currentConversationId = "shared-conversation";
    aiState.currentConversationTitle = "Shared";
    aiState.conversationRevision = 3;

    await sendMessage("local update");
    const sid = aiState.activeStreamId || "stream-test-1";
    handleAIChunkEvent({ data: { streamId: sid, data: "local answer" } });
    handleAIDoneEvent({ data: { streamId: sid, data: "" } });

    await vi.waitFor(() => expect(aiState.currentConversationId).toBe("forked-conversation"));
    expect(save).toHaveBeenNthCalledWith(1, expect.objectContaining({
      id: "shared-conversation",
      expected_revision: 3,
    }));
    expect(save).toHaveBeenNthCalledWith(2, expect.objectContaining({
      id: "forked-conversation",
    }));
    expect(aiState.conversationRevision).toBe(1);
  });
});

// C-6: MessageList 索引 key 与 FIFO drop 冲突修复测试
describe("C-6: MessageList 稳定 id 与 FIFO drop", () => {
  beforeEach(() => {
    aiState.messages = [];
    aiState.streaming = false;
    aiState.globalStreamBusy = false;
    aiState.activeStreamId = null;
    aiState.error = null;
    aiState.context = null;
    aiState.currentConversationId = null;
    aiState.currentConversationTitle = null;
    aiState.mentionedFiles = [];
    aiState.currentSystemPromptOverride = null;
    // M-16: 重置 stale 标志与 revision，避免跨用例污染。
    aiState.conversationStaleWhileStreaming = false;
    aiState.conversationRevision = 0;
    aiState.contextChips = [];
    // G12: 重置 persona/plan 状态，避免跨用例污染。
    personaState.personas = [];
    personaState.activePersonaId = null;
    aiPlanState.activePlan = null;
    agentState.mode = "chat";
    vi.mocked(aiService.startStream).mockReset().mockResolvedValue("stream-test-1");
    vi.mocked(aiService.setConfig).mockClear();
  });

  // C-6 规范 1：sendMessage 生成的 user/assistant 消息必须带稳定 id
  it("sendMessage 在后端流返回前立即生成 user/assistant UUID (D2)", async () => {
    const { aiService } = await import("@/api/services");
    let resolveStart!: (streamId: string) => void;
    (aiService.startStream as ReturnType<typeof vi.fn>).mockImplementationOnce(
      () => new Promise<string>((resolve) => { resolveStart = resolve; }),
    );

    const sending = sendMessage("immediate ids");

    expect(aiState.messages).toHaveLength(2);
    expect(aiState.messages[0].id).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i,
    );
    expect(aiState.messages[1].id).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i,
    );
    expect(aiState.messages[0].id).not.toBe(aiState.messages[1].id);

    // SetConfig is an awaited backend admission, so the stream mock is
    // installed on the next microtask rather than synchronously.
    await vi.waitFor(() => expect(aiService.startStream).toHaveBeenCalledOnce());
    resolveStart("immediate-stream");
    await sending;
    eventHandlers["ai:done"]?.({ data: { streamId: "immediate-stream", data: "" } });
    eventHandlers["ai:stream-busy"]?.({ data: { streamId: "immediate-stream", busy: false } });
  });

  it("sendMessage 为 user 和 assistant 消息生成稳定 id (C-6)", async () => {
    const promise = sendMessage("hi");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    eventHandlers["ai:chunk"]?.({ data: { streamId: sid, data: "answer" } });
    eventHandlers["ai:done"]?.({ data: { streamId: sid, data: "" } });

    expect(aiState.messages.length).toBe(2);
    // 两条消息都有非空 id
    expect(aiState.messages[0].id).toBeTruthy();
    expect(aiState.messages[1].id).toBeTruthy();
    // 两条消息的 id 不同
    expect(aiState.messages[0].id).not.toBe(aiState.messages[1].id);
  });

  // C-6 规范 3：超过 200 条消息触发 FIFO drop 后，渲染内容与数据一一对应
  it("FIFO drop 后保留消息的 id 与 content 一一对应 (C-6)", async () => {
    // 预填充 205 条带稳定 id 与 content 的消息（content 与索引绑定便于验证）
    const originalCount = MAX_AI_MESSAGES + 5;
    const prebuilt: { role: "user" | "assistant"; content: string; id: string }[] = [];
    for (let i = 0; i < originalCount; i++) {
      const id = `msg-${i}-${i * 7 + 3}`;
      prebuilt.push({
        role: i % 2 === 0 ? "user" : "assistant",
        content: `content-${i}`,
        id,
      });
    }
    aiState.messages = [...prebuilt];

    // 触发 sendMessage：会 push 2 条新消息（user+assistant），共 207 条，
    // trimMessagesIfNeeded 会 drop 前 7 条，保留后 200 条。
    const promise = sendMessage("trigger-drop");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    // 给 assistant 消息填充 content，避免 ai:done 因空 content 移除它
    eventHandlers["ai:chunk"]?.({ data: { streamId: sid, data: "drop-handled" } });
    eventHandlers["ai:done"]?.({ data: { streamId: sid, data: "" } });

    // 1) 数量被裁剪到 MAX_AI_MESSAGES
    expect(aiState.messages.length).toBe(MAX_AI_MESSAGES);

    // 2) 被保留的原始消息是后 198 条（索引 7..204），加上新增的 2 条
    //    验证 id 与 content 配对仍严格一致（无错位）
    for (let i = 0; i < aiState.messages.length; i++) {
      const m = aiState.messages[i];
      expect(m.id).toBeTruthy();
      // 若该消息是预填充的（非 trigger-drop 新增），其 id 与 content 必须严格配对
      if (m.content.startsWith("content-")) {
        const idx = parseInt(m.content.replace("content-", ""), 10);
        const expectedId = prebuilt[idx].id;
        expect(m.id).toBe(expectedId);
      }
    }

    // 3) 最早被 drop 的 7 条消息（索引 0..6）不应再出现在保留列表中
    for (let i = 0; i < 7; i++) {
      const droppedId = prebuilt[i].id;
      const stillPresent = aiState.messages.some((m) => m.id === droppedId);
      expect(stillPresent).toBe(false);
    }

    // 4) 索引 7 的消息（drop 边界）必须仍然存在，验证边界正确
    const boundaryId = prebuilt[7].id;
    expect(aiState.messages.some((m) => m.id === boundaryId)).toBe(true);

    // 5) 所有保留消息的 id 互不相同（v-for key 稳定唯一）
    const ids = aiState.messages.map((m) => m.id);
    const uniqueIds = new Set(ids);
    expect(uniqueIds.size).toBe(ids.length);
  });

  // C-6 规范 3 扩展：loadConversation 加载的持久化消息也带稳定 id
  it("loadConversation 为持久化消息生成稳定 id (C-6)", async () => {
    const { conversationService } = await import("@/api/services");
    const persisted = {
      id: "conv-c6",
      title: "test",
      created_at: 0,
      messages: [
        { role: "user", content: "persisted-user-1" },
        { role: "assistant", content: "persisted-assistant-1" },
        { role: "user", content: "persisted-user-2" },
      ],
    };
    (conversationService.load as any).mockResolvedValue(persisted);

    await loadConversation("conv-c6");

    expect(aiState.messages.length).toBe(3);
    // 每条消息都有非空且唯一的 id
    const ids = aiState.messages.map((m) => m.id);
    for (const id of ids) {
      expect(id).toBeTruthy();
    }
    const uniqueIds = new Set(ids);
    expect(uniqueIds.size).toBe(ids.length);
    // content 与原持久化数据一致
    expect(aiState.messages[0].content).toBe("persisted-user-1");
    expect(aiState.messages[1].content).toBe("persisted-assistant-1");
    expect(aiState.messages[2].content).toBe("persisted-user-2");
  });

  it("loads an unfinished native tool round as interrupted without restoring execution authority", async () => {
    vi.mocked(conversationService.load).mockResolvedValueOnce({
      id: "interrupted-native-round",
      title: "Interrupted tool round",
      created_at: 0,
      updated_at: 0,
      revision: 3,
      messages: [
        { role: "user", content: "read the file" },
        {
          role: "assistant",
          content: "",
          toolCalls: [{ id: "provider-call-1", name: "read", arguments: '{"path":"README.md"}' }],
        },
      ],
    });
    agentState.mode = "agent";
    vi.mocked(aiService.startAgentStream).mockClear();

    expect(await loadConversation("interrupted-native-round")).toBe(true);

    expect(agentState.pendingToolCalls).toEqual([]);
    expect(aiState.messages).toHaveLength(3);
    expect(aiState.messages[2]).toMatchObject({
      role: "tool",
      toolResults: [{
        toolCallId: "provider-call-1",
        isError: true,
      }],
    });
    expect(aiState.messages[2].content).toContain("interrupted");
    expect(agentTimelineState.entries.map((entry) => entry.stage)).toEqual([
      "requested",
      "result",
      "observation",
    ]);
    expect(agentTimelineState.entries.some((entry) => entry.stage === "approval")).toBe(false);

    await sendMessage("continue safely");
    const history = vi.mocked(aiService.startAgentStream).mock.calls[0]?.[1];
    expect(history).toEqual(expect.arrayContaining([
      expect.objectContaining({
        role: "tool",
        toolResults: [expect.objectContaining({
          toolCallId: "provider-call-1",
          isError: true,
        })],
      }),
    ]));
    expect(agentState.pendingToolCalls).toEqual([]);
    agentState.mode = "chat";
  });
});

describe("G12: AI 请求上下文与 Plan 真接线", () => {
  beforeEach(() => {
    aiState.messages = [];
    aiState.streaming = false;
    aiState.globalStreamBusy = false;
    aiState.activeStreamId = null;
    aiState.error = null;
    aiState.context = null;
    aiState.currentConversationId = null;
    aiState.currentConversationTitle = null;
    aiState.mentionedFiles = [];
    aiState.currentSystemPromptOverride = null;
    aiState.conversationStaleWhileStreaming = false;
    aiState.conversationRevision = 0;
    aiState.contextChips = [];
    personaState.personas = [];
    personaState.activePersonaId = null;
    aiPlanState.activePlan = null;
    agentState.mode = "chat";
    vi.mocked(aiService.setConfig).mockClear();
  });

  it("serializes a persona chip into the message prefix", async () => {
    addContextChip({ id: "p1", kind: "persona", label: "Senior Go Reviewer" });
    const promise = sendMessage("review this");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    eventHandlers["ai:done"]?.({ data: { streamId: sid, data: "" } });
    expect(aiState.messages[0].content).toContain("Persona: Senior Go Reviewer");
    expect(aiState.messages[0].content).toContain("review this");
  });

  it("resolves @codebase chips with text-search hits, not open files", async () => {
    vi.mocked(searchService.search).mockResolvedValueOnce([
      { path: "note.txt", matches: [{ line: 3, column: 1, preview: "needle here" }] },
    ] as any);
    addContextChip({ id: "cb1", kind: "codebase", label: "Codebase", query: "needle" });
    const promise = sendMessage("find needle");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    eventHandlers["ai:done"]?.({ data: { streamId: sid, data: "" } });
    expect(aiState.messages[0].content).toContain("Codebase text search (not a vector index)");
    expect(aiState.messages[0].content).toContain("note.txt:3: needle here");
    expect(aiState.messages[0].content).not.toContain("openFiles");
  });

  it("says honestly when codebase search has no hits", async () => {
    vi.mocked(searchService.search).mockResolvedValueOnce([]);
    addContextChip({ id: "cb2", kind: "codebase", label: "Codebase", query: "missing-token" });
    const promise = sendMessage("look for missing-token");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    eventHandlers["ai:done"]?.({ data: { streamId: sid, data: "" } });
    expect(aiState.messages[0].content).toMatch(/no matches/i);
  });

  it("sends image chips as structured attachments on the user message", async () => {
    addContextChip({
      id: "img1",
      kind: "image",
      label: "shot.png",
      imageUrl: "data:image/png;base64,aGVsbG8=",
    });
    const promise = sendMessage("what is in the screenshot?");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    eventHandlers["ai:done"]?.({ data: { streamId: sid, data: "" } });
    const user = aiState.messages[0] as { images?: string[] };
    expect(user.images).toEqual(["data:image/png;base64,aGVsbG8="]);
    // 反例：普通文本仍可读，且 chip 在发送后被清空。
    expect(aiState.messages[0].content).toContain("what is in the screenshot?");
    expect(aiState.contextChips.length).toBe(0);
  });

  it("leaves images undefined when no image chip is attached (reverse)", async () => {
    const promise = sendMessage("plain text only");
    await promise;
    const sid = aiState.activeStreamId || "stream-test-1";
    eventHandlers["ai:done"]?.({ data: { streamId: sid, data: "" } });
    const user = aiState.messages[0] as { images?: string[] };
    expect(user.images).toBeUndefined();
  });

  it("injects the active plan into the provider system prompt", async () => {
    aiPlanState.activePlan = {
      id: "plan-1",
      goal: "Refactor the HTTP client",
      steps: [
        { title: "Extract client", description: "Move transport to its own type", status: "approved" },
        { title: "Add retry", description: "", status: "pending" },
      ],
      status: "pending",
      createdAt: "2026-08-07T00:00:00Z",
    };
    const promise = sendMessage("continue");
    await promise;
    expect(vi.mocked(aiService.setConfig)).toHaveBeenCalled();
    const calls = vi.mocked(aiService.setConfig).mock.calls;
    const lastConfig = calls[calls.length - 1][0] as { systemPrompt?: string };
    expect(lastConfig.systemPrompt).toContain("Active plan");
    expect(lastConfig.systemPrompt).toContain("Refactor the HTTP client");
    expect(lastConfig.systemPrompt).toContain("Extract client");
    expect(lastConfig.systemPrompt).toContain("Add retry");
  });

  it("omits the plan block when no active plan exists (reverse)", async () => {
    const promise = sendMessage("continue");
    await promise;
    const calls = vi.mocked(aiService.setConfig).mock.calls;
    const lastConfig = calls[calls.length - 1][0] as { systemPrompt?: string };
    expect(lastConfig.systemPrompt).not.toContain("Active plan");
  });

  it("uses the active persona prompt for the provider request", async () => {
    personaState.personas = [
      {
        id: "senior-go",
        name: "Senior Go",
        systemPrompt: "You are a senior Go engineer who writes idiomatic code.",
        builtIn: true,
      },
    ];
    personaState.activePersonaId = "senior-go";
    const promise = sendMessage("review");
    await promise;
    const calls = vi.mocked(aiService.setConfig).mock.calls;
    const lastConfig = calls[calls.length - 1][0] as { systemPrompt?: string };
    expect(lastConfig.systemPrompt).toContain("You are a senior Go engineer");
  });

  it("keeps the global prompt when no persona is active (reverse)", async () => {
    const promise = sendMessage("review");
    await promise;
    const calls = vi.mocked(aiService.setConfig).mock.calls;
    const lastConfig = calls[calls.length - 1][0] as { systemPrompt?: string };
    expect(lastConfig.systemPrompt).not.toContain("You are a senior Go engineer");
  });

  it("serializes an mcp chip with its namespace", async () => {
    addContextChip({ id: "m1", kind: "mcp", label: "filesystem/read" });
    const promise = sendMessage("use the tool");
    await promise;
    expect(aiState.messages[0].content).toContain("MCP tool: filesystem/read");
    expect(aiState.messages[0].content).toContain("use the tool");
  });

  it("omits MCP text when no mcp chip is attached (reverse)", async () => {
    const promise = sendMessage("use the tool");
    await promise;
    expect(aiState.messages[0].content).not.toContain("MCP tool:");
  });

  it("serializes injected MCP context with its server provenance", async () => {
    addContextChip({
      id: "mcp-res:fs:fixture://notes",
      kind: "mcp",
      label: "fixture://notes",
      content: "fixture notes body",
      mcpServer: "fs",
      mcpUri: "fixture://notes",
      mcpGeneration: 3,
    });
    const promise = sendMessage("summarize the resource");
    await promise;
    expect(aiState.messages[0].content).toContain("MCP context from fs (fixture://notes):");
    expect(aiState.messages[0].content).toContain("fixture notes body");
    expect(aiState.messages[0].content).toContain("summarize the resource");
  });

  it("serializes a skill chip with its name and content", async () => {
    addContextChip({
      id: "s1",
      kind: "skill",
      label: "code-review",
      content: "Review for concurrency bugs",
    });
    const promise = sendMessage("review");
    await promise;
    expect(aiState.messages[0].content).toContain("code-review");
    expect(aiState.messages[0].content).toContain("Review for concurrency bugs");
  });

  it("omits Skill text when no skill chip is attached (reverse)", async () => {
    const promise = sendMessage("review");
    await promise;
    expect(aiState.messages[0].content).not.toContain("Active Skill");
  });
});
