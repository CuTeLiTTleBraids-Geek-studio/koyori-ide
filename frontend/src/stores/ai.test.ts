import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("@/lib/monaco-themes", () => ({
  accentThemes: [],
  applyMonacoTheme: vi.fn(),
  registerAllThemes: vi.fn(),
}));

// Collect event handlers so tests can simulate backend events.
// vi.hoisted ensures this runs before mock factories are evaluated.
const { eventHandlers } = vi.hoisted(() => ({
  eventHandlers: {} as Record<string, ((...args: any[]) => void) | undefined>,
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
    stopStream: vi.fn().mockResolvedValue(undefined),
    send: vi.fn().mockResolvedValue({ Content: "ok", FinishReason: "stop" }),
    getPresetPrompt: vi.fn().mockResolvedValue("Explain this code."),
    getDefaultSystemPrompt: vi.fn().mockResolvedValue("default prompt"),
    listPresets: vi.fn().mockResolvedValue([]),
    generateTitleWithAI: vi.fn().mockResolvedValue("AI generated title"),
  },
  conversationService: {
    save: vi.fn().mockResolvedValue(undefined),
    load: vi.fn().mockResolvedValue({ id: "1", title: "test", created_at: 0, messages: [] }),
    generateId: vi.fn().mockResolvedValue("new-id"),
    generateTitle: vi.fn().mockResolvedValue("test title"),
  },
}));

vi.mock("@/lib/notifications", () => ({
  notify: vi.fn(),
  notifySuccess: vi.fn(),
  notifyError: vi.fn(),
  notifyWarning: vi.fn(),
  notifyInfo: vi.fn(),
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
  STREAM_TIMEOUT_MS,
  cleanupAIEventListeners,
  ensureAIEventListeners,
  handleAIChunkEvent,
  handleAIDoneEvent,
  handleAIErrorEvent,
  handleAIStreamBusyEvent,
  resetStreamState,
  addContextChip,
} from "./ai";

import { aiService } from "@/api/services";
import { personaState } from "./persona";
import { aiPlanState } from "./aiPlan";

eventHandlers["ai:chunk"] = handleAIChunkEvent;
eventHandlers["ai:done"] = handleAIDoneEvent;
eventHandlers["ai:error"] = handleAIErrorEvent;
eventHandlers["ai:stream-busy"] = handleAIStreamBusyEvent;

describe("ai store", () => {
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
    vi.mocked(aiService.setConfig).mockClear();
  });

  it("sends a message and appends assistant response via events", async () => {
    const promise = sendMessage("hi");
    await promise;
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
    clearMessages();
    expect(aiState.messages).toHaveLength(0);
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

  it("does not send while streaming", async () => {
    aiState.streaming = true;
    const before = aiState.messages.length;
    await sendMessage("hi");
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
    await sendMessage("hi");
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
    await sending;

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