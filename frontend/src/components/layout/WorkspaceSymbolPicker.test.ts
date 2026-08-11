import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";

const { getWorkspaceSymbols, openFileFromPath, notifyError, requestEditorJump, layoutState } = vi.hoisted(() => ({
  getWorkspaceSymbols: vi.fn(),
  openFileFromPath: vi.fn().mockResolvedValue(undefined),
  notifyError: vi.fn(),
  requestEditorJump: vi.fn(),
  layoutState: { tree: { activeLeafId: "editor-primary" } },
}));

vi.mock("@/stores/app", () => ({ requestEditorJump }));
vi.mock("@/stores/layout", () => ({ layoutState }));
vi.mock("@/stores/lsp", () => ({ getWorkspaceSymbols }));
vi.mock("@/stores/editor", () => ({ openFileFromPath }));
vi.mock("@/lib/notifications", () => ({ notifyError }));
vi.mock("@/lib/errors", () => ({
  errorMessage: (value: unknown) => value instanceof Error ? value.message : String(value),
}));
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import WorkspaceSymbolPicker from "./WorkspaceSymbolPicker.vue";

function symbol(index: number) {
  return {
    name: `Symbol${index}`,
    kind: index % 2 === 0 ? 12 : 5,
    containerName: "pkg",
    location: {
      uri: `file:///C:/repo/src/file${index}.ts`,
      range: {
        start: { line: index, character: 2 },
        end: { line: index, character: 8 },
      },
    },
  };
}

function mountPicker() {
  return mount(WorkspaceSymbolPicker, {
    props: { visible: true },
    global: { plugins: [ElementPlus] },
  });
}

describe("WorkspaceSymbolPicker", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    getWorkspaceSymbols.mockResolvedValue(Array.from({ length: 100 }, (_, index) => symbol(index)));
    layoutState.tree.activeLeafId = "editor-primary";
  });

  afterEach(() => vi.useRealTimers());

  it("debounces workspace queries for 300ms and calls the aggregate API once", async () => {
    const wrapper = mountPicker();
    await wrapper.get(".workspace-symbol-picker__input").setValue("Symbol");

    await vi.advanceTimersByTimeAsync(299);
    expect(getWorkspaceSymbols).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    await flushPromises();

    expect(getWorkspaceSymbols).toHaveBeenCalledTimes(1);
    expect(getWorkspaceSymbols).toHaveBeenCalledWith("Symbol");
  });

  it("virtualizes large result sets while preserving the full scroll height", async () => {
    const wrapper = mountPicker();
    await wrapper.get(".workspace-symbol-picker__input").setValue("Symbol");
    await vi.advanceTimersByTimeAsync(300);
    await flushPromises();

    expect(wrapper.findAll(".workspace-symbol-picker__item").length).toBeLessThan(30);
    expect(wrapper.get(".workspace-symbol-picker__virtual").attributes("style")).toContain("5400px");
    expect(wrapper.get(".workspace-symbol-picker__meta").text()).toContain("file0.ts:1");
    expect(wrapper.find(".workspace-symbol-picker__kind").exists()).toBe(true);
    expect(
      wrapper.findAll('[role="option"]').every((item) => item.attributes("tabindex") === "-1"),
    ).toBe(true);
  });

  it("opens and jumps to the selected symbol", async () => {
    getWorkspaceSymbols.mockResolvedValue([symbol(7)]);
    const wrapper = mountPicker();
    await wrapper.get(".workspace-symbol-picker__input").setValue("Symbol7");
    await vi.advanceTimersByTimeAsync(300);
    await flushPromises();
    await wrapper.get(".workspace-symbol-picker__item").trigger("click");
    await flushPromises();

    expect(openFileFromPath).toHaveBeenCalledWith("C:\\repo\\src\\file7.ts");
    expect(requestEditorJump).toHaveBeenCalledWith(
      "C:\\repo\\src\\file7.ts",
      8,
      3,
      "editor-primary",
    );
    expect(wrapper.emitted("close")).toBeTruthy();
  });
});
