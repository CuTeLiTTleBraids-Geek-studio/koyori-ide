import { beforeEach, describe, expect, it, vi } from "vitest";

const {
  aiStateMock,
  aiAssistantStateMock,
  clearMessagesMock,
  openAIDesktopWindowMock,
  switchModeMock,
  emitMock,
} = vi.hoisted(() => {
  const aiState = {
    messages: [] as Array<{ id: string; role: "user" | "assistant"; content: string }>,
    streaming: false,
    globalStreamBusy: false,
    currentConversationId: null as string | null,
    conversationRevision: 0,
  };
  return {
    aiStateMock: aiState,
    aiAssistantStateMock: {
      mode: "chat" as "chat" | "plan" | "goal" | "agent",
      activeConversationId: null as string | null,
    },
    clearMessagesMock: vi.fn(() => {
      aiState.messages = [];
      aiState.currentConversationId = null;
      aiState.conversationRevision = 0;
    }),
    openAIDesktopWindowMock: vi.fn(),
    switchModeMock: vi.fn(),
    emitMock: vi.fn(),
  };
});

vi.mock("@wailsio/runtime", () => ({ Events: { Emit: emitMock } }));
vi.mock("@/stores/ai", () => ({
  aiState: aiStateMock,
  clearMessages: clearMessagesMock,
}));
vi.mock("@/stores/aiAssistant", () => ({
  aiAssistantState: aiAssistantStateMock,
  openAIDesktopWindow: openAIDesktopWindowMock,
  switchMode: switchModeMock,
}));

import { installConversationHandoffProbe } from "./conversationHandoffProbe";

type ProbeGlobal = typeof globalThis & {
  __koyoriIdeRuntimeRole?: string;
  __koyoriIdeRunConversationHandoffProbe?: (config: Record<string, unknown>) => Promise<void>;
};

describe("conversationHandoffProbe", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    aiStateMock.messages = [];
    aiStateMock.streaming = false;
    aiStateMock.globalStreamBusy = false;
    aiStateMock.currentConversationId = null;
    aiStateMock.conversationRevision = 0;
    aiAssistantStateMock.mode = "chat";
    aiAssistantStateMock.activeConversationId = null;
    clearMessagesMock.mockClear();
    switchModeMock.mockReset().mockImplementation((mode: typeof aiAssistantStateMock.mode) => {
      aiAssistantStateMock.mode = mode;
    });
    openAIDesktopWindowMock.mockReset().mockImplementation(async () => {
      aiStateMock.currentConversationId = "conv-packaged-a";
      aiStateMock.conversationRevision = 1;
      aiAssistantStateMock.activeConversationId = "conv-packaged-a";
      return {
        requestId: "request_packaged_a",
        sourceOrigin: "origin_packaged_main",
        sourceEpoch: "source_packaged_main",
        sequence: 1,
        recipientEpoch: "receiver_packaged_ai",
        receiverEpoch: "receiver_packaged_ai",
        acknowledged: true,
        durable: false,
      };
    });
    emitMock.mockReset().mockResolvedValue(undefined);
    installConversationHandoffProbe();
  });

  it("persists and hands off a main-window conversation through the product entry", async () => {
    const runtime = globalThis as ProbeGlobal;
    runtime.__koyoriIdeRuntimeRole = "main";

    await runtime.__koyoriIdeRunConversationHandoffProbe?.({
      runId: "run_handoff_main",
      action: "handoff",
      marker: "PACKAGED_HANDOFF_A",
      mode: "agent",
    });

    expect(clearMessagesMock).toHaveBeenCalledOnce();
    expect(switchModeMock).toHaveBeenCalledWith("agent");
    expect(openAIDesktopWindowMock).toHaveBeenCalledWith(undefined, {
      requireConversationAck: true,
    });
    expect(emitMock).toHaveBeenCalledWith(
      "e2e:conversation-handoff-result",
      expect.objectContaining({
        ok: true,
        role: "main",
        conversationId: "conv-packaged-a",
        revision: 1,
        markerObserved: true,
        acknowledged: true,
        requestId: "request_packaged_a",
        receiverEpoch: "receiver_packaged_ai",
      }),
    );
  });

  it("keeps one AI renderer instance while inspecting two conversation targets", async () => {
    const runtime = globalThis as ProbeGlobal;
    runtime.__koyoriIdeRuntimeRole = "ai";
    document.body.innerHTML = '<main class="ai-window"><section class="ai-window__messages"></section></main>';

    await runtime.__koyoriIdeRunConversationHandoffProbe?.({
      runId: "run_ready",
      action: "ready",
    });
    const ready = emitMock.mock.calls.at(-1)?.[1] as { rendererInstanceId: string };

    for (const [id, marker, mode] of [
      ["conv-packaged-a", "PACKAGED_HANDOFF_A", "chat"],
      ["conv-packaged-b", "PACKAGED_HANDOFF_B", "agent"],
    ] as const) {
      aiStateMock.currentConversationId = id;
      aiStateMock.conversationRevision = 1;
      aiStateMock.messages = [{ id: `message-${id}`, role: "user", content: marker }];
      aiAssistantStateMock.activeConversationId = id;
      aiAssistantStateMock.mode = mode;
      document.querySelector(".ai-window__messages")!.textContent = marker;
      await runtime.__koyoriIdeRunConversationHandoffProbe?.({
        runId: `run_${id}`,
        action: "inspect",
        marker,
        mode,
        expectedConversationId: id,
        expectedRevision: 1,
        expectedRendererInstanceId: ready.rendererInstanceId,
        forbiddenMarker: id.endsWith("b") ? "PACKAGED_HANDOFF_A" : undefined,
      });
      expect(emitMock.mock.calls.at(-1)?.[1]).toEqual(expect.objectContaining({
        ok: true,
        role: "ai",
        rendererInstanceId: ready.rendererInstanceId,
        conversationId: id,
        markerObserved: true,
        domMarkerObserved: true,
        activeConversationMatches: true,
        windowMounted: true,
      }));
    }
  });

  it("fails closed before opening when another AI stream owns the process", async () => {
    const runtime = globalThis as ProbeGlobal;
    runtime.__koyoriIdeRuntimeRole = "main";
    aiStateMock.globalStreamBusy = true;

    await runtime.__koyoriIdeRunConversationHandoffProbe?.({
      runId: "run_busy",
      action: "handoff",
      marker: "PACKAGED_HANDOFF_BUSY",
      mode: "chat",
    });

    expect(openAIDesktopWindowMock).not.toHaveBeenCalled();
    expect(emitMock).toHaveBeenCalledWith(
      "e2e:conversation-handoff-result",
      expect.objectContaining({ ok: false, error: expect.stringContaining("busy") }),
    );
  });
});
