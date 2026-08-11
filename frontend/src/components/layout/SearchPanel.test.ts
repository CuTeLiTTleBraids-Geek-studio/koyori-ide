import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";

const {
  search,
  previewReplace,
  applyReplacePreview,
  applyMultiFileReplace,
  messageSuccess,
} = vi.hoisted(() => ({
  search: vi.fn(),
  previewReplace: vi.fn(),
  applyReplacePreview: vi.fn(),
  applyMultiFileReplace: vi.fn(),
  messageSuccess: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  searchService: {
    search,
    previewReplace,
    applyReplacePreview,
    applyMultiFileReplace,
    replace: vi.fn(),
  },
}));

vi.mock("@/stores/editor", () => ({
  openFileFromPath: vi.fn(),
}));

vi.mock("element-plus", () => ({
  ElMessage: { success: messageSuccess, error: vi.fn() },
  ElNotification: vi.fn(),
}));

import SearchPanel from "./SearchPanel.vue";
import { appState } from "@/stores/app";
import { cancelReplacePreview, searchState } from "@/stores/search";

describe("SearchPanel filters and replace preview", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    appState.currentProject = "/repo";
    searchState.query = "hello";
    searchState.results = [{ path: "src/a.ts", matches: [{ line: 1, column: 1, preview: "hello" }] }];
    searchState.loading = false;
    searchState.error = null;
    searchState.includeGlob = "";
    searchState.excludeGlob = "";
    cancelReplacePreview();
    search.mockResolvedValue(searchState.results);
    previewReplace.mockResolvedValue({
      path: "/repo/src/a.ts",
      originalHash: "hash-a",
      originalContent: "hello",
      modifiedContent: "hi",
      replacements: 1,
    });
    applyMultiFileReplace.mockResolvedValue({ applied: true, conflicts: [] });
  });

  it("binds include and exclude glob inputs to the global search store", async () => {
    const wrapper = mount(SearchPanel, { global: { stubs: { "el-icon": true } } });
    const filters = wrapper.findAll(".search-panel__filter-input");
    await filters[0].setValue("src/**/*.ts");
    await filters[1].setValue("**/*.test.ts");

    expect(searchState.includeGlob).toBe("src/**/*.ts");
    expect(searchState.excludeGlob).toBe("**/*.test.ts");
    wrapper.unmount();
  });

  it("renders a selectable preview and cancels it without applying", async () => {
    const wrapper = mount(SearchPanel, { global: { stubs: { "el-icon": true } } });
    await wrapper.get(".search-panel__toggle-replace").trigger("click");
    await wrapper.get(".search-panel__replace-input").setValue("hi");
    await wrapper.get(".search-panel__replace-btn").trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("/repo/src/a.ts");
    expect(wrapper.find('input[type="checkbox"]').exists()).toBe(true);
    expect(applyReplacePreview).not.toHaveBeenCalled();

    const actions = wrapper.findAll(".search-panel__preview-action");
    await actions[1].trigger("click");
    expect(searchState.replacePreviews).toEqual([]);
    wrapper.unmount();
  });

  it("applies selected previews through the batch transaction UI path", async () => {
    const wrapper = mount(SearchPanel, { global: { stubs: { "el-icon": true } } });
    await wrapper.get(".search-panel__toggle-replace").trigger("click");
    await wrapper.get(".search-panel__replace-input").setValue("hi");
    await wrapper.get(".search-panel__replace-btn").trigger("click");
    await flushPromises();

    await wrapper.findAll(".search-panel__preview-action")[0].trigger("click");
    await flushPromises();

    expect(applyMultiFileReplace).toHaveBeenCalledOnce();
    expect(applyReplacePreview).not.toHaveBeenCalled();
    expect(messageSuccess).toHaveBeenCalledOnce();
    wrapper.unmount();
  });
});
