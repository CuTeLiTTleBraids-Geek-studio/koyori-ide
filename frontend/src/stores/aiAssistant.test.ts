/**
 * Plan 11 Task 1 Step 9 — aiAssistant store 测试。
 * 覆盖 openStandalonePage/switchMode/setSidebarWidth/toggleContextPanel/
 * setActiveConversation，以及与 aiState.currentConversationId 的同步。
 * mock @/stores/ai 以隔离 Wails bindings 依赖链。
 */
import { describe, it, expect, beforeEach, vi } from "vitest";

const {
  setAgentMode,
  openAIWindowMock,
  toggleAIWindowMock,
  isAIWindowVisibleMock,
  emitMock,
  sendSelectionToAIMock,
  flushConversationPersistenceMock,
  persistConversationNowMock,
} = vi.hoisted(() => ({
  setAgentMode: vi.fn(),
  openAIWindowMock: vi.fn(),
  toggleAIWindowMock: vi.fn(),
  isAIWindowVisibleMock: vi.fn(),
  emitMock: vi.fn(),
  sendSelectionToAIMock: vi.fn(),
  flushConversationPersistenceMock: vi.fn(),
  persistConversationNowMock: vi.fn(),
}));

// 必须在 import store 之前 mock，避免触发 @/stores/ai → @/api/services
// → ../../bindings 的真实依赖链（Wails bindings 为构建生成物，测试期缺失）。
vi.mock("@/stores/ai", () => ({
  aiState: {
    currentConversationId: null as string | null,
    messages: [] as Array<{ role: string; content: string }>,
  },
  flushConversationPersistence: flushConversationPersistenceMock,
  persistConversationNow: persistConversationNowMock,
}));
vi.mock("@/stores/agent", () => ({
  setMode: setAgentMode,
}));
vi.mock("@wailsio/runtime", () => ({
  Events: { Emit: emitMock },
}));
vi.mock("@/api/services", () => ({
  windowService: {
    openAIWindow: openAIWindowMock,
    toggleAIWindow: toggleAIWindowMock,
    isAIWindowVisible: isAIWindowVisibleMock,
    sendSelectionToAI: sendSelectionToAIMock,
  },
}));

import {
  AI_CONVERSATION_TARGET_KEY,
  AI_CONVERSATION_TARGET_ACK_KEY,
  aiAssistantState,
  acknowledgeAIConversationTarget,
  handleAIConversationTargetAckEvent,
  consumePendingAIConversationTarget,
  registerAIConversationTargetReceiver,
  openAIDesktopWindow,
  sendSelectionToAIDesktopWindow,
  toggleAIDesktopWindow,
  openStandalonePage,
  parseAIConversationTargetEvent,
  switchMode,
  setSidebarWidth,
  toggleContextPanel,
  setActiveConversation,
} from "@/stores/aiAssistant";
import { aiState } from "@/stores/ai";

describe("aiAssistant store (Plan 11 Task 1)", () => {
  beforeEach(() => {
    aiAssistantState.mode = "chat";
    aiAssistantState.sidebarWidth = 260;
    aiAssistantState.contextPanelCollapsed = false;
    aiAssistantState.activeConversationId = null;
    aiState.currentConversationId = null;
    aiState.messages = [];
    // 重置 hash，避免上一个测试的导航残留影响断言。
    window.location.hash = "";
    setAgentMode.mockClear();
    openAIWindowMock.mockReset().mockResolvedValue(undefined);
    toggleAIWindowMock.mockReset().mockResolvedValue(undefined);
    isAIWindowVisibleMock.mockReset().mockResolvedValue(false);
    emitMock.mockClear();
    sendSelectionToAIMock.mockReset().mockResolvedValue(undefined);
    flushConversationPersistenceMock.mockReset().mockResolvedValue(undefined);
    persistConversationNowMock.mockReset().mockResolvedValue(null);
    localStorage.clear();
  });

  it("defaults to chat mode with sidebar 260 and expanded context panel", () => {
    expect(aiAssistantState.mode).toBe("chat");
    expect(aiAssistantState.sidebarWidth).toBe(260);
    expect(aiAssistantState.contextPanelCollapsed).toBe(false);
    expect(aiAssistantState.activeConversationId).toBeNull();
  });

  it("keeps only complete input modes active", () => {
    switchMode("plan");
    expect(aiAssistantState.mode).toBe("chat");
    switchMode("goal");
    expect(aiAssistantState.mode).toBe("goal");
    switchMode("agent");
    expect(aiAssistantState.mode).toBe("agent");
    switchMode("chat");
    expect(aiAssistantState.mode).toBe("chat");
  });

  it("keeps the backend-facing Agent mode aligned with the visible selector", () => {
    switchMode("agent");
    expect(setAgentMode).toHaveBeenLastCalledWith("agent");
    switchMode("plan");
    expect(setAgentMode).toHaveBeenLastCalledWith("chat");
  });

  it("setSidebarWidth clamps to the 200–480 range", () => {
    setSidebarWidth(100);
    expect(aiAssistantState.sidebarWidth).toBe(200);
    setSidebarWidth(999);
    expect(aiAssistantState.sidebarWidth).toBe(480);
    setSidebarWidth(320);
    expect(aiAssistantState.sidebarWidth).toBe(320);
  });

  it("toggleContextPanel flips the collapsed flag", () => {
    expect(aiAssistantState.contextPanelCollapsed).toBe(false);
    toggleContextPanel();
    expect(aiAssistantState.contextPanelCollapsed).toBe(true);
    toggleContextPanel();
    expect(aiAssistantState.contextPanelCollapsed).toBe(false);
  });

  it("setActiveConversation sets the active conversation id", () => {
    setActiveConversation("conv-abc");
    expect(aiAssistantState.activeConversationId).toBe("conv-abc");
    setActiveConversation(null);
    expect(aiAssistantState.activeConversationId).toBeNull();
  });

  it("openStandalonePage syncs currentConversationId and navigates to #/ai", () => {
    aiState.currentConversationId = "conv-from-embedded";
    openStandalonePage();
    expect(aiAssistantState.activeConversationId).toBe("conv-from-embedded");
    expect(window.location.hash).toBe("#/ai");
  });

  it("openStandalonePage does not reassign hash when already at #/ai", () => {
    aiState.currentConversationId = "conv-x";
    window.location.hash = "#/ai";
    openStandalonePage();
    expect(aiAssistantState.activeConversationId).toBe("conv-x");
    // hash 保持 #/ai，无重复触发。
    expect(window.location.hash).toBe("#/ai");
  });

  it("hands the active conversation and Agent mode to the desktop window", async () => {
    aiState.currentConversationId = "conv-desktop";
    switchMode("agent");

    await openAIDesktopWindow();

    expect(openAIWindowMock).toHaveBeenCalledTimes(1);
    const emitted = emitMock.mock.calls.filter((call) => call[0] === "ai:open-conversation").at(-1);
    const target = parseAIConversationTargetEvent({ data: emitted?.[1] });
    expect(target).toMatchObject({
      protocol: 1,
      target: "ai-window",
      conversationId: "conv-desktop",
      mode: "agent",
    });
    expect(target?.requestId).toBeTruthy();
    expect(target?.sourceEpoch).toBeTruthy();
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).not.toBeNull();
    expect(consumePendingAIConversationTarget(target?.sequence)).toEqual(target);
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).toBeNull();
  });

  it("does not let an older event consume a newer startup handoff", async () => {
    aiState.currentConversationId = "conv-newest";
    await openAIDesktopWindow();
    const emitted = emitMock.mock.calls.filter((call) => call[0] === "ai:open-conversation").at(-1);
    const target = parseAIConversationTargetEvent({ data: emitted?.[1] });
    expect(target).toBeDefined();

    expect(consumePendingAIConversationTarget((target?.sequence ?? 0) - 1)).toBeUndefined();
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).not.toBeNull();
    expect(consumePendingAIConversationTarget(target?.sequence)).toEqual(target);
  });

  it("rejects expired or malformed conversation targets", () => {
    localStorage.setItem(AI_CONVERSATION_TARGET_KEY, JSON.stringify({
      conversationId: "conv-expired",
      mode: "agent",
      sequence: 1,
      createdAt: Date.now() - 3 * 60 * 1000,
    }));
    expect(consumePendingAIConversationTarget()).toBeUndefined();
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).toBeNull();
    expect(parseAIConversationTargetEvent({
      data: { conversationId: "conv", mode: "invalid", sequence: 2, createdAt: Date.now() },
    })).toBeUndefined();
    localStorage.setItem(AI_CONVERSATION_TARGET_KEY, "{not-json");
    expect(consumePendingAIConversationTarget()).toBeUndefined();
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).toBeNull();
  });

  it("rejects non-target, old-recipient, and sequence-poisoning payloads", async () => {
    const receiverEpoch = registerAIConversationTargetReceiver();
    aiState.currentConversationId = "conv-safe";
    await openAIDesktopWindow();
    const emitted = emitMock.mock.calls.filter((call) => call[0] === "ai:open-conversation").at(-1);
    const target = parseAIConversationTargetEvent({ data: emitted?.[1] }, receiverEpoch);
    expect(target).toBeDefined();

    expect(parseAIConversationTargetEvent({
      data: { ...target, target: "main-window" },
    }, receiverEpoch)).toBeUndefined();
    expect(parseAIConversationTargetEvent({
      data: { ...target, recipientEpoch: "retired-ai-window" },
    }, receiverEpoch)).toBeUndefined();
    expect(parseAIConversationTargetEvent({
      data: { ...target, sequence: Number.MAX_SAFE_INTEGER },
    }, receiverEpoch)).toBeUndefined();
  });

  it("removes the durable fallback only after the receiver acknowledges the exact target", async () => {
    const receiverEpoch = registerAIConversationTargetReceiver();
    aiState.currentConversationId = "conv-ack";
    await openAIDesktopWindow();
    const emitted = emitMock.mock.calls.filter((call) => call[0] === "ai:open-conversation").at(-1);
    const target = parseAIConversationTargetEvent({ data: emitted?.[1] }, receiverEpoch);
    expect(target).toBeDefined();
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).not.toBeNull();

    acknowledgeAIConversationTarget(target!, receiverEpoch);

    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).toBeNull();
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_ACK_KEY)).toContain(target!.requestId);
    expect(emitMock).toHaveBeenCalledWith(
      "ai:open-conversation-ack",
      expect.objectContaining({ requestId: target!.requestId, receiverEpoch }),
    );
  });

  it("keeps the durable target when live delivery fails", async () => {
    emitMock.mockRejectedValueOnce(new Error("event transport unavailable"));
    aiState.currentConversationId = "conv-durable";

    await openAIDesktopWindow();
    await Promise.resolve();

    const raw = localStorage.getItem(AI_CONVERSATION_TARGET_KEY);
    expect(raw).not.toBeNull();
    const target = parseAIConversationTargetEvent({ data: JSON.parse(raw!) });
    expect(target?.conversationId).toBe("conv-durable");
    acknowledgeAIConversationTarget(target!, target!.recipientEpoch ?? "receiver_fallback");
  });

  it("keeps explicit toggle semantics isolated from the open/focus handoff entry", async () => {
    aiState.currentConversationId = "conv-visible";
    switchMode("agent");
    isAIWindowVisibleMock.mockResolvedValue(true);

    await toggleAIDesktopWindow();

    expect(toggleAIWindowMock).toHaveBeenCalledTimes(1);
    expect(openAIWindowMock).not.toHaveBeenCalled();
    expect(emitMock.mock.calls.some((call) => call[0] === "ai:open-conversation")).toBe(false);
  });

  it("persists a live new conversation before handing a non-null ID to the AI window", async () => {
    aiState.messages = [{ id: "message-live", role: "user", content: "work in progress" }];
    persistConversationNowMock.mockResolvedValueOnce("conv-first-save");

    await openAIDesktopWindow();

    expect(persistConversationNowMock).toHaveBeenCalledOnce();
    expect(persistConversationNowMock.mock.invocationCallOrder[0]).toBeLessThan(
      openAIWindowMock.mock.invocationCallOrder[0]!,
    );
    const emitted = emitMock.mock.calls.filter((call) => call[0] === "ai:open-conversation").at(-1);
    expect(parseAIConversationTargetEvent({ data: emitted?.[1] })).toMatchObject({
      conversationId: "conv-first-save",
      mode: "chat",
    });
  });

  it("fails closed when a conversation with content cannot be persisted for handoff", async () => {
    aiState.messages = [{ id: "message-failed", role: "user", content: "must not disappear" }];
    persistConversationNowMock.mockResolvedValueOnce(null);

    await expect(openAIDesktopWindow()).rejects.toThrow(
      "Failed to persist the AI conversation before opening its window",
    );

    expect(openAIWindowMock).not.toHaveBeenCalled();
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).toBeNull();
  });

  it("keeps the invocation snapshot atomic while persistence is in flight", async () => {
    aiState.currentConversationId = "conv-a";
    aiState.messages = [{ id: "message-a", role: "user", content: "conversation A" }];
    switchMode("chat");
    let resolvePersist!: (id: string | null) => void;
    persistConversationNowMock.mockImplementationOnce(
      () => new Promise<string | null>((resolve) => { resolvePersist = resolve; }),
    );

    const opening = openAIDesktopWindow();
    await vi.waitFor(() => expect(persistConversationNowMock).toHaveBeenCalledOnce());
    aiState.currentConversationId = "conv-b";
    aiState.messages = [{ id: "message-b", role: "user", content: "conversation B" }];
    switchMode("agent");
    resolvePersist("conv-a");
    await opening;

    const emitted = emitMock.mock.calls.filter((call) => call[0] === "ai:open-conversation").at(-1);
    expect(parseAIConversationTargetEvent({ data: emitted?.[1] })).toMatchObject({
      conversationId: "conv-a",
      mode: "chat",
    });
    expect(aiAssistantState.activeConversationId).toBe("conv-a");
  });

  it("keeps the handoff snapshot atomic while the native window opens", async () => {
    aiState.currentConversationId = "conv-a";
    switchMode("chat");
    let resolveOpen!: () => void;
    openAIWindowMock.mockImplementationOnce(() => new Promise<void>((resolve) => { resolveOpen = resolve; }));

    const opening = openAIDesktopWindow();
    await vi.waitFor(() => expect(openAIWindowMock).toHaveBeenCalledTimes(1));
    aiState.currentConversationId = "conv-b";
    switchMode("agent");
    resolveOpen();
    await opening;

    const emitted = emitMock.mock.calls.filter((call) => call[0] === "ai:open-conversation").at(-1);
    const target = parseAIConversationTargetEvent({ data: emitted?.[1] });
    expect(target).toMatchObject({ conversationId: "conv-a", mode: "chat" });
    expect(aiAssistantState.activeConversationId).toBe("conv-a");
  });

  it("does not let an older slow open overwrite the newest user handoff", async () => {
    let resolveFirstPersist!: (id: string | null) => void;
    persistConversationNowMock
      .mockImplementationOnce(
        () => new Promise<string | null>((resolve) => { resolveFirstPersist = resolve; }),
      )
      .mockResolvedValueOnce("conv-newest");

    aiState.currentConversationId = "conv-older";
    aiState.messages = [{ id: "message-old", role: "user", content: "older" }];
    const olderOpen = openAIDesktopWindow();
    await vi.waitFor(() => expect(persistConversationNowMock).toHaveBeenCalledTimes(1));

    aiState.currentConversationId = "conv-newest";
    aiState.messages = [{ id: "message-new", role: "user", content: "newest" }];
    switchMode("agent");
    const newestOpen = openAIDesktopWindow();
    await newestOpen;

    resolveFirstPersist("conv-older");
    await olderOpen;

    const targets = emitMock.mock.calls
      .filter((call) => call[0] === "ai:open-conversation")
      .map((call) => parseAIConversationTargetEvent({ data: call[1] }))
      .filter(Boolean);
    expect(targets).not.toContainEqual(expect.objectContaining({ conversationId: "conv-older" }));
    expect(targets.at(-1)).toMatchObject({ conversationId: "conv-newest", mode: "agent" });
    expect(aiAssistantState.activeConversationId).toBe("conv-newest");
  });

  it("waits for the exact conversation acknowledgement before sending editor selection", async () => {
    aiState.currentConversationId = "conv-selection";
    const sending = sendSelectionToAIDesktopWindow(
      "const selected = true;",
      "typescript",
      "C:/repo/main.ts",
    );
    await vi.waitFor(() => {
      expect(emitMock.mock.calls.some((call) => call[0] === "ai:open-conversation")).toBe(true);
    });
    expect(sendSelectionToAIMock).not.toHaveBeenCalled();

    const emitted = emitMock.mock.calls.find((call) => call[0] === "ai:open-conversation");
    const target = parseAIConversationTargetEvent({ data: emitted?.[1] });
    expect(target).toBeDefined();
    handleAIConversationTargetAckEvent({
      data: {
        protocol: 1,
        target: "main-window",
        requestId: target!.requestId,
        sourceOrigin: target!.sourceOrigin,
        sourceEpoch: "source_wrong_ack",
        receiverEpoch: target!.recipientEpoch ?? "receiver_selection",
        sequence: target!.sequence,
        createdAt: Date.now(),
      },
    });
    await Promise.resolve();
    expect(sendSelectionToAIMock).not.toHaveBeenCalled();

    handleAIConversationTargetAckEvent({
      data: {
        protocol: 1,
        target: "main-window",
        requestId: target!.requestId,
        sourceOrigin: target!.sourceOrigin,
        sourceEpoch: target!.sourceEpoch,
        receiverEpoch: target!.recipientEpoch ?? "receiver_selection",
        sequence: target!.sequence,
        createdAt: Date.now(),
      },
    });
    await sending;

    expect(sendSelectionToAIMock).toHaveBeenCalledWith(
      "const selected = true;",
      "typescript",
      "C:/repo/main.ts",
    );
  });

  it("fails closed when a newer window handoff supersedes editor selection", async () => {
    let resolveSelectionFlush!: () => void;
    flushConversationPersistenceMock
      .mockImplementationOnce(
        () => new Promise<void>((resolve) => { resolveSelectionFlush = resolve; }),
      )
      .mockResolvedValueOnce(undefined);
    aiState.currentConversationId = "conv-selection-old";

    const sending = sendSelectionToAIDesktopWindow("old selection", "text", "C:/repo/old.txt");
    await vi.waitFor(() => expect(flushConversationPersistenceMock).toHaveBeenCalledTimes(1));

    aiState.currentConversationId = "conv-newest";
    switchMode("agent");
    await openAIDesktopWindow();
    resolveSelectionFlush();

    await expect(sending).rejects.toThrow("superseded");
    expect(sendSelectionToAIMock).not.toHaveBeenCalled();
  });

  it("fails closed when neither durable storage nor live delivery can carry the handoff", async () => {
    vi.useFakeTimers();
    const storage = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    emitMock.mockRejectedValue(new Error("event transport unavailable"));
    aiState.currentConversationId = "conv-unreachable";

    try {
      const opening = openAIDesktopWindow();
      const rejected = expect(opening).rejects.toThrow("could not be delivered");
      await vi.advanceTimersByTimeAsync(2_000);
      await rejected;
    } finally {
      storage.mockRestore();
      vi.useRealTimers();
    }
  });

  it("does not reuse a durable target after the companion receiver epoch changes", async () => {
    vi.useFakeTimers();
    const originalSetItem = Storage.prototype.setItem;
    let rejectUpdatedTarget = false;
    const storage = vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (
      this: Storage,
      key: string,
      value: string,
    ) {
      if (rejectUpdatedTarget && key === AI_CONVERSATION_TARGET_KEY) {
        throw new Error("updated target storage unavailable");
      }
      return originalSetItem.call(this, key, value);
    });
    registerAIConversationTargetReceiver();
    emitMock.mockImplementation((event: string) => {
      if (event === "ai:open-conversation" && !rejectUpdatedTarget) {
        registerAIConversationTargetReceiver();
        rejectUpdatedTarget = true;
      }
      return Promise.reject(new Error("event transport unavailable"));
    });
    aiState.currentConversationId = "conv-receiver-restart";

    try {
      const opening = openAIDesktopWindow();
      const rejected = expect(opening).rejects.toThrow("could not be delivered");
      await vi.advanceTimersByTimeAsync(2_000);
      await rejected;
    } finally {
      storage.mockRestore();
      vi.useRealTimers();
    }
  });
});
