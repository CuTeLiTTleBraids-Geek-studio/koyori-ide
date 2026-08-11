import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";

const {
  requestEditorJump,
  layoutState,
  callHierarchyQuery,
  openFileFromPath,
  prepareCallHierarchy,
  incomingCalls,
} = vi.hoisted(() => ({
  requestEditorJump: vi.fn(),
  layoutState: { tree: { activeLeafId: "editor-secondary" } },
  callHierarchyQuery: {
    value: {
      mode: "call" as const,
      language: "typescript",
      filePath: "C:\\repo\\root.ts",
      line: 0,
      column: 0,
      content: "",
    },
  },
  openFileFromPath: vi.fn().mockResolvedValue(undefined),
  prepareCallHierarchy: vi.fn(),
  incomingCalls: vi.fn(),
}));

vi.mock("@/stores/app", () => ({ requestEditorJump }));
vi.mock("@/stores/layout", () => ({ layoutState }));
vi.mock("@/stores/editor", () => ({ openFileFromPath }));
vi.mock("@/stores/lsp", () => ({
  callHierarchyQuery,
  prepareLSPCallHierarchy: prepareCallHierarchy,
  getLSPCallHierarchyIncomingCalls: incomingCalls,
  getLSPCallHierarchyOutgoingCalls: vi.fn().mockResolvedValue([]),
  prepareLSPTypeHierarchy: vi.fn().mockResolvedValue([]),
  getLSPTypeHierarchySupertypes: vi.fn().mockResolvedValue([]),
  getLSPTypeHierarchySubtypes: vi.fn().mockResolvedValue([]),
}));
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));
vi.mock("element-plus", () => ({
  ElMessage: { info: vi.fn(), warning: vi.fn() },
}));

import CallHierarchyPanel from "./CallHierarchyPanel.vue";

function item(name: string, line: number) {
  return {
    name,
    kind: 12,
    detail: `${name} detail`,
    filePath: `C:\\repo\\${name}.ts`,
    line,
    column: 1,
    endLine: line,
    endColumn: 8,
    selectionLine: line,
    selectionColumn: 2,
    selectionEndLine: line,
    selectionEndColumn: 7,
  };
}

function mountPanel() {
  return mount(CallHierarchyPanel, {
    global: {
      stubs: {
        ElButtonGroup: { template: "<div><slot /></div>" },
        ElButton: { template: "<button type='button' @click='$emit(\"click\")'><slot /></button>" },
        ElIcon: { template: "<span><slot /></span>" },
      },
    },
  });
}

describe("CallHierarchyPanel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    prepareCallHierarchy.mockResolvedValue([item("root", 0)]);
    incomingCalls.mockImplementation(async (_language, _path, _content, parent) => {
      if (parent.name === "root") return [{ from: item("caller", 4), fromRanges: [] }];
      if (parent.name === "caller") return [{ from: item("grandCaller", 9), fromRanges: [] }];
      return [];
    });
    layoutState.tree.activeLeafId = "editor-secondary";
  });

  it("recursively expands incoming calls as a tree", async () => {
    const wrapper = mountPanel();
    await flushPromises();
    expect(wrapper.findAll(".chp__item")).toHaveLength(1);

    await wrapper.findAll(".chp__item")[0].trigger("click");
    await flushPromises();
    expect(wrapper.findAll(".chp__item")).toHaveLength(2);
    expect(wrapper.text()).toContain("caller");

    await wrapper.findAll(".chp__item")[1].trigger("click");
    await flushPromises();
    expect(wrapper.findAll(".chp__item")).toHaveLength(3);
    expect(wrapper.text()).toContain("grandCaller");
  });

  it("jumps to a node on double click", async () => {
    const wrapper = mountPanel();
    await flushPromises();
    await wrapper.findAll(".chp__item")[0].trigger("click");
    await flushPromises();
    await wrapper.findAll(".chp__item")[1].trigger("dblclick");
    await flushPromises();

    expect(openFileFromPath).toHaveBeenCalledWith("C:\\repo\\caller.ts");
    expect(requestEditorJump).toHaveBeenCalledWith(
      "C:\\repo\\caller.ts",
      5,
      3,
      "editor-secondary",
    );
  });
});
