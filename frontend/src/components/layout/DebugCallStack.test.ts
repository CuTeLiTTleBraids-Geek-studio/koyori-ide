import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import DebugCallStack from "./DebugCallStack.vue";

const {
  debugState,
  loadMoreStackFrames,
  loadAsyncParentStack,
  selectDebugFrame,
} = vi.hoisted(() => ({
  loadMoreStackFrames: vi.fn(),
  loadAsyncParentStack: vi.fn(),
  selectDebugFrame: vi.fn(),
  debugState: {
    running: true,
    stopped: true,
    stack: [] as Array<{
      id: number;
      name: string;
      file: string;
      line: number;
      column: number;
      presentationHint?: string;
      asyncBoundary?: boolean;
    }>,
    supportsDelayedStackTraceLoading: false,
    supportsAsyncStackTrace: false,
    stackHasMore: false,
    stackPageLoading: false,
    asyncStackRootId: "",
    asyncStackSegments: [] as Array<{
      id: string;
      description: string;
      frames: Array<{ id: number; name: string; file: string; line: number; column: number }>;
      parentId: string;
    }>,
    asyncStackLoading: false,
  },
}));

vi.mock("@/stores/debug", () => ({
  debugState,
  loadMoreStackFrames,
  loadAsyncParentStack,
  selectDebugFrame,
}));

vi.mock("@/stores/editor", () => ({ openFileFromPath: vi.fn() }));
const { appState } = vi.hoisted(() => ({
  appState: { cursorLine: 1, cursorColumn: 1, editorJumpSeq: 0, language: "en" },
}));

vi.mock("@/stores/app", () => ({ appState }));

describe("DebugCallStack", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    debugState.stack = [
      { id: 1, name: "current", file: "/repo/app.js", line: 4, column: 1 },
      { id: 2, name: "await promise", file: "", line: 0, column: 0, presentationHint: "label", asyncBoundary: true },
    ];
    debugState.supportsDelayedStackTraceLoading = true;
    debugState.supportsAsyncStackTrace = true;
    debugState.stackHasMore = true;
    debugState.asyncStackRootId = "async-root";
    debugState.asyncStackSegments = [];
    debugState.asyncStackLoading = false;
    appState.language = "en";
  });

  it("renders adapter-provided async boundaries separately and expands lazily", async () => {
    const wrapper = mount(DebugCallStack);

    expect(wrapper.get("[data-testid='async-boundary']").text()).toContain("await promise");
    await wrapper.get("[data-testid='load-more-stack']").trigger("click");
    await wrapper.get("[data-testid='load-async-parent']").trigger("click");

    expect(loadMoreStackFrames).toHaveBeenCalledTimes(1);
    expect(loadAsyncParentStack).toHaveBeenCalledWith("async-root");
  });

  it("hides lazy controls when the adapter reports no support", () => {
    debugState.supportsDelayedStackTraceLoading = false;
    debugState.supportsAsyncStackTrace = false;
    const wrapper = mount(DebugCallStack);

    expect(wrapper.find("[data-testid='load-more-stack']").exists()).toBe(false);
    expect(wrapper.find("[data-testid='load-async-parent']").exists()).toBe(false);
  });

  it("renders each loaded CDP parent as an async boundary followed by frames", () => {
    debugState.asyncStackSegments = [{
      id: "async-root",
      description: "Promise.then",
      frames: [{ id: -1, name: "caller", file: "/repo/caller.js", line: 9, column: 2 }],
      parentId: "next",
    }];
    const wrapper = mount(DebugCallStack);

    expect(wrapper.get("[data-testid='async-segment-boundary']").text()).toContain("Promise.then");
    expect(wrapper.text()).toContain("caller");
  });

  it("localizes call-stack states through the shared dictionaries", () => {
    appState.language = "zh";
    debugState.stack = [];
    const wrapper = mount(DebugCallStack);

    expect(wrapper.text()).toContain("暂无调用帧");
  });

  it("keeps restart separate from the treeitem selection control", async () => {
    const wrapper = mount(DebugCallStack);
    const treeitem = wrapper.get('[role="treeitem"]');
    const restart = wrapper.get(".debug-call-stack__restart");
    expect(treeitem.element.contains(restart.element)).toBe(false);

    const space = new KeyboardEvent("keydown", {
      key: " ",
      bubbles: true,
      cancelable: true,
    });
    restart.element.dispatchEvent(space);
    expect(space.defaultPrevented).toBe(false);
    expect(wrapper.emitted("selectFrame")).toBeUndefined();

    await restart.trigger("click");
    expect(wrapper.emitted("restartFrame")?.[0]).toEqual([1]);
    expect(wrapper.emitted("selectFrame")).toBeUndefined();
  });
});
