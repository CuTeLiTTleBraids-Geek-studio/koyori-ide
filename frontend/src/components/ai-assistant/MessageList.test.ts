import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import type { ChatMessage } from "@/types";

type TestMessage = ChatMessage & { id: string };

// vi.hoisted: mock 引用需在 vi.mock 工厂中使用，必须提升到顶部避免 TDZ。
const { aiStateObj } = vi.hoisted(() => ({
  aiStateObj: {
    messages: [] as TestMessage[],
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

import MessageList from "./MessageList.vue";

let resizeCallback: ResizeObserverCallback | null = null;
const observeMock = vi.fn();
const unobserveMock = vi.fn();
const disconnectMock = vi.fn();

describe("MessageList — M-25 虚拟滚动", () => {
  beforeEach(() => {
    aiStateObj.messages = [];
    resizeCallback = null;
    observeMock.mockClear();
    unobserveMock.mockClear();
    disconnectMock.mockClear();
    vi.stubGlobal("ResizeObserver", class {
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
    resizeCallback?.(
      [
        { target: first, contentRect: { height: 40 } },
        { target: second, contentRect: { height: 240 } },
      ] as unknown as ResizeObserverEntry[],
      {} as ResizeObserver,
    );
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
    (list.element as HTMLElement).scrollTop = 1_000;
    await list.trigger("scroll");
    await wrapper.vm.$nextTick();

    // message 5 is above the real viewport anchor, but remains mounted by the
    // 600px overscan window. Growing it by 100px must preserve the viewport.
    const overscanRow = wrapper.find('[data-message-id="anchor-5"]').element;
    resizeCallback?.(
      [{ target: overscanRow, contentRect: { height: 196 } }] as unknown as ResizeObserverEntry[],
      {} as ResizeObserver,
    );
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
