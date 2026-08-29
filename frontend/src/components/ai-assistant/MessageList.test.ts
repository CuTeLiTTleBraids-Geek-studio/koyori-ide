import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import type { ChatMessage } from "@/types";
import type { AgentToolCatalog } from "@/api/automation";

type TestMessage = ChatMessage & { id: string };

// vi.hoisted: mock 引用需在 vi.mock 工厂中使用，必须提升到顶部避免 TDZ。
const { aiStateObj, timelineStateObj } = vi.hoisted(() => ({
  aiStateObj: {
    messages: [] as TestMessage[],
  },
  timelineStateObj: {
    entries: [] as Array<Record<string, unknown>>,
  },
}));

// Mock @/stores/ai：用 reactive 包装，使 computed 能响应 messages 变化。
vi.mock("@/stores/ai", async () => {
  const { reactive } = await import("vue");
  return {
    aiState: reactive(aiStateObj),
  };
});

vi.mock("@/stores/app", () => ({
  appState: { personalization: { bubbleStyle: "rounded" } },
}));

vi.mock("@/stores/agentTimeline", async () => {
  const { reactive } = await import("vue");
  return {
    agentTimelineState: reactive(timelineStateObj),
    bindAgentState: vi.fn(),
    recordToolObservation: vi.fn(),
    recordToolRequested: vi.fn(),
    recordToolStage: vi.fn(),
    resetAgentTimeline: vi.fn(),
  };
});

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => key,
    locale: { value: "en" },
  }),
}));

vi.mock("@/lib/markdown", () => ({
  renderMarkdown: (md: string) => md,
}));

// MarkdownContent 桩：直接渲染 html prop 内容
vi.mock("@/components/common/MarkdownContent.vue", () => ({
  default: {
    name: "MarkdownContent",
    props: { html: String },
    template: '<div class="markdown-content">{{ html }}</div>',
  },
}));

vi.mock("@/components/ai-assistant/AgentToolCalls.vue", () => ({
  default: {
    name: "AgentToolCalls",
    template: "<div class=\"agent-tool-calls-stub\" />",
  },
}));

import MessageList from "./MessageList.vue";
const { aiState } = await import("@/stores/ai");
const { agentState, __setAgentToolCatalogForTests } = await import("@/stores/agent");
const { agentTimelineState } = await import("@/stores/agentTimeline");

let resizeCallback: ResizeObserverCallback | null = null;
const observeMock = vi.fn();
const unobserveMock = vi.fn();
const disconnectMock = vi.fn();
const resizeObserverArgument: ResizeObserver = {
  observe: (target, options) => observeMock(target, options),
  unobserve: (target) => unobserveMock(target),
  disconnect: () => disconnectMock(),
};

function setScrollMetrics(
  element: HTMLElement,
  metrics: { scrollHeight: number; clientHeight: number },
): void {
  Object.defineProperty(element, "scrollHeight", {
    configurable: true,
    get: () => metrics.scrollHeight,
  });
  Object.defineProperty(element, "clientHeight", {
    configurable: true,
    get: () => metrics.clientHeight,
  });
}

function resizeEntry(target: Element, height: number): ResizeObserverEntry {
  return {
    target,
    contentRect: new DOMRectReadOnly(0, 0, 0, height),
    borderBoxSize: [],
    contentBoxSize: [],
    devicePixelContentBoxSize: [],
  };
}

function notifyResize(entries: ResizeObserverEntry[]): void {
  if (!resizeCallback) {
    throw new Error("ResizeObserver was not initialized");
  }
  resizeCallback(entries, resizeObserverArgument);
}

async function flushTailFollow(wrapper: ReturnType<typeof mount>): Promise<void> {
  await wrapper.vm.$nextTick();
  await wrapper.vm.$nextTick();
}

function builtinCatalog(): AgentToolCatalog {
  return {
    revision: 1,
    tools: [{
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
    }],
  };
}

describe("MessageList — M-25 虚拟滚动", () => {
  beforeEach(() => {
    aiStateObj.messages = [];
    agentState.mode = "agent";
    agentState.pendingToolCalls = [];
    agentState.toolCallCount = 0;
    __setAgentToolCatalogForTests(builtinCatalog());
    agentTimelineState.entries = [];
    resizeCallback = null;
    observeMock.mockClear();
    unobserveMock.mockClear();
    disconnectMock.mockClear();
    vi.stubGlobal("ResizeObserver", class implements ResizeObserver {
      constructor(callback: ResizeObserverCallback) {
        resizeCallback = callback;
      }
      observe = observeMock;
      unobserve = unobserveMock;
      disconnect = disconnectMock;
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("无消息时显示空提示", () => {
    const wrapper = mount(MessageList);
    expect(wrapper.find(".ai-msg-list__empty").exists()).toBe(true);
    expect(wrapper.findAll(".ai-msg")).toHaveLength(0);
    wrapper.unmount();
  });

  it("首个 ai:chunk 到达后在当前挂载窗口立即显示 assistant 内容", async () => {
    // sendMessage creates the assistant row before the first event arrives.
    // The chunk handler then mutates that same reactive message in place; the
    // mounted virtual row must update without remounting MessageList.
    aiStateObj.messages.push({
      role: "assistant",
      content: "",
      id: "streaming-assistant",
    });

    const wrapper = mount(MessageList);
    await wrapper.vm.$nextTick();
    expect(wrapper.findAllComponents({ name: "AgentExecutionTimeline" })).toHaveLength(1);
    const rowBeforeChunk = wrapper.find('[data-message-id="streaming-assistant"]').element;
    expect(wrapper.find(".markdown-content").text()).toBe("");

    aiState.messages[0].content = "首个 chunk 已显示";
    await wrapper.vm.$nextTick();

    const rowAfterChunk = wrapper.find('[data-message-id="streaming-assistant"]').element;
    expect(rowAfterChunk).toBe(rowBeforeChunk);
    expect(wrapper.find(".markdown-content").text()).toBe("首个 chunk 已显示");
    wrapper.unmount();
  });

  it("仅隐藏 assistant fenced tool protocol，并保留普通代码块", async () => {
    const providerContent = [
      "I will inspect the file.",
      "```",
      "read: src/main.ts",
      "```",
      "Normal example:",
      "```ts",
      "const answer = 42;",
      "```",
    ].join("\n");
    aiState.messages.push({ role: "assistant", content: providerContent, id: "tool-protocol" });

    const wrapper = mount(MessageList);
    await wrapper.vm.$nextTick();

    const rendered = wrapper.find(".markdown-content").text();
    expect(rendered).not.toContain("read: src/main.ts");
    expect(rendered).toContain("I will inspect the file.");
    expect(rendered).toContain("Normal example:");
    expect(rendered).toContain("const answer = 42;");
    expect(rendered).toContain("```ts");
    wrapper.unmount();
  });

  it("原样显示 native 响应中额外出现的 fenced tool 文本", async () => {
    aiState.messages.push({
      role: "assistant",
      content: "Native call plus unexpected text:\n```\nread: duplicate.ts\n```",
      id: "mixed-native-protocol",
      toolCalls: [{ id: "native-read", name: "read", arguments: '{"path":"a.ts"}' }],
    });

    const wrapper = mount(MessageList);
    await wrapper.vm.$nextTick();

    expect(wrapper.find(".markdown-content").text()).toContain("read: duplicate.ts");
    expect(wrapper.find('[data-testid="message-tool-calls"]').text()).toContain("read");
    wrapper.unmount();
  });

  it("在 near-bottom 时跟随挂载、chunk、追加消息和 activity 增长", async () => {
    aiState.messages.push({ role: "assistant", content: "start", id: "follow-tail" });
    const wrapper = mount(MessageList);
    const list = wrapper.get(".ai-msg-list");
    const metrics = { scrollHeight: 1_000, clientHeight: 200 };
    setScrollMetrics(list.element as HTMLElement, metrics);
    await flushTailFollow(wrapper);
    expect((list.element as HTMLElement).scrollTop).toBe(800);

    (list.element as HTMLElement).scrollTop = 800;
    await list.trigger("scroll");
    metrics.scrollHeight = 1_120;
    aiState.messages[0].content += " streamed chunk";
    await flushTailFollow(wrapper);
    expect((list.element as HTMLElement).scrollTop).toBe(920);

    metrics.scrollHeight = 1_220;
    aiState.messages.push({ role: "assistant", content: "next turn", id: "next-turn" });
    await flushTailFollow(wrapper);
    expect((list.element as HTMLElement).scrollTop).toBe(1_020);

    metrics.scrollHeight = 1_300;
    agentTimelineState.entries.push({
      id: "timeline-1",
      kind: "tool",
      stage: "requested",
      createdAt: 1,
      updatedAt: 1,
    });
    await wrapper.vm.$nextTick();
    const activity = wrapper.get(".ai-msg-list__activity").element;
    notifyResize([resizeEntry(activity, 80)]);
    await flushTailFollow(wrapper);
    expect((list.element as HTMLElement).scrollTop).toBe(1_100);
    wrapper.unmount();
  });

  it("用户已向上滚动时不因新 chunk 强制拉回底部", async () => {
    aiState.messages.push({ role: "assistant", content: "start", id: "keep-position" });
    const wrapper = mount(MessageList);
    const list = wrapper.get(".ai-msg-list");
    const metrics = { scrollHeight: 1_000, clientHeight: 200 };
    setScrollMetrics(list.element as HTMLElement, metrics);
    await flushTailFollow(wrapper);

    (list.element as HTMLElement).scrollTop = 300;
    await list.trigger("scroll");
    metrics.scrollHeight = 1_120;
    aiState.messages[0].content += " streamed chunk";
    await flushTailFollow(wrapper);

    expect((list.element as HTMLElement).scrollTop).toBe(300);
    wrapper.unmount();
  });

  it("500 条消息时仅渲染有界数量的消息元素（< 50），而非全量 500", async () => {
    // 填充 500 条消息
    for (let i = 0; i < 500; i++) {
      aiStateObj.messages.push({
        role: i % 2 === 0 ? "user" : "assistant",
        content: `Message ${i}`,
        id: `msg-${i}`,
      });
    }

    const wrapper = mount(MessageList);
    await wrapper.vm.$nextTick();

    const msgElements = wrapper.findAll(".ai-msg");
    // 虚拟滚动：渲染数量应有界，远少于 500
    expect(msgElements.length).toBeLessThan(50);
    // 但至少渲染了一些消息（非空）
    expect(msgElements.length).toBeGreaterThan(0);
    // 动态高度虚拟画布负责撑起未渲染区域。
    expect(wrapper.find(".ai-msg-list__virtual").attributes("style")).toContain("height:");
    wrapper.unmount();
  });

  it("少量消息（< 视口容量）时全部渲染，无 bottom spacer", async () => {
    for (let i = 0; i < 3; i++) {
      aiStateObj.messages.push({
        role: "user",
        content: `Short ${i}`,
        id: `s-${i}`,
      });
    }

    const wrapper = mount(MessageList);
    await wrapper.vm.$nextTick();

    // 3 条消息全部渲染
    expect(wrapper.findAll(".ai-msg")).toHaveLength(3);
    wrapper.unmount();
  });

  it("通过 ResizeObserver 缓存实际高度并更新后续消息偏移", async () => {
    for (let i = 0; i < 3; i++) {
      aiStateObj.messages.push({
        role: "assistant",
        content: `Variable height ${i}`,
        id: `height-${i}`,
      });
    }
    const wrapper = mount(MessageList);
    await wrapper.vm.$nextTick();

    const first = wrapper.find('[data-message-id="height-0"]').element;
    const second = wrapper.find('[data-message-id="height-1"]').element;
    notifyResize([
      resizeEntry(first, 40),
      resizeEntry(second, 240),
    ]);
    await wrapper.vm.$nextTick();

    expect(
      wrapper.find('[data-message-id="height-2"]').attributes("style"),
    ).toContain("translateY(304px)");
    wrapper.unmount();
    expect(disconnectMock).toHaveBeenCalledTimes(1);
  });

  it("视口上方的 overscan 消息变高时保持滚动锚点", async () => {
    for (let i = 0; i < 50; i++) {
      aiStateObj.messages.push({
        role: "assistant",
        content: `Anchor message ${i}`,
        id: `anchor-${i}`,
      });
    }
    const wrapper = mount(MessageList);
    await wrapper.vm.$nextTick();

    const list = wrapper.find(".ai-msg-list");
    setScrollMetrics(list.element as HTMLElement, {
      scrollHeight: 5_400,
      clientHeight: 600,
    });
    (list.element as HTMLElement).scrollTop = 1_000;
    await list.trigger("scroll");
    await wrapper.vm.$nextTick();

    // message 5 is above the real viewport anchor, but remains mounted by the
    // 600px overscan window. Growing it by 100px must preserve the viewport.
    const overscanRow = wrapper.find('[data-message-id="anchor-5"]').element;
    notifyResize([resizeEntry(overscanRow, 196)]);
    await wrapper.vm.$nextTick();

    expect((list.element as HTMLElement).scrollTop).toBe(1_100);
    wrapper.unmount();
  });

  it("10,000 条消息在 500ms 预算内挂载且首屏与深滚动保持有界 DOM", async () => {
    for (let i = 0; i < 10_000; i++) {
      aiStateObj.messages.push({
        role: i % 2 === 0 ? "user" : "assistant",
        content: `Performance message ${i}`,
        id: `perf-${i}`,
      });
    }

    const mountStarted = performance.now();
    const wrapper = mount(MessageList);
    await wrapper.vm.$nextTick();
    expect(performance.now() - mountStarted).toBeLessThan(500);
    expect(wrapper.findAll(".ai-msg").length).toBeLessThan(50);
    expect(wrapper.find(".ai-msg-list__virtual").attributes("style")).toContain("1079988px");

    const list = wrapper.find(".ai-msg-list");
    (list.element as HTMLElement).scrollTop = 1_079_388;
    await list.trigger("scroll");
    await wrapper.vm.$nextTick();
    // Browser FPS cannot be inferred from jsdom wall-clock time. The stable
    // regression signal is that per-scroll DOM work stays independent of the
    // total message count and old rows are detached from ResizeObserver.
    expect(wrapper.findAll(".ai-msg").length).toBeLessThan(50);
    expect(Number(wrapper.find(".ai-msg-list__item").attributes("data-message-index"))).toBeGreaterThan(9_900);
    expect(unobserveMock).toHaveBeenCalled();
    wrapper.unmount();
  });
});
