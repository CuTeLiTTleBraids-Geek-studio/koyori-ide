import { beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";

const mocks = vi.hoisted(() => ({
  run: vi.fn(),
  preview: vi.fn(),
  apply: vi.fn(),
  cancelSearch: vi.fn(),
  cancelPreview: vi.fn(),
  clear: vi.fn(),
  openFile: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  state: {
    query: "class:User > method:get*",
    replacement: "displayName",
    results: [] as Array<Record<string, unknown>>,
    previews: [] as Array<Record<string, unknown>>,
    loading: false,
    previewLoading: false,
    applying: false,
    error: null as string | null,
    truncated: false,
    scannedFiles: 0,
    skippedFiles: 0,
  },
}));

vi.mock("@/stores/structuralSearch", () => ({
  structuralSearchState: mocks.state,
  runStructuralSearch: mocks.run,
  previewStructuralReplacements: mocks.preview,
  applySelectedStructuralPreviews: mocks.apply,
  cancelStructuralSearch: mocks.cancelSearch,
  cancelStructuralPreview: mocks.cancelPreview,
  clearStructuralSearch: mocks.clear,
  parseStructuralGlobInput: (value: string) => value.split(",").map((item) => item.trim()).filter(Boolean),
}));

vi.mock("@/stores/editor", () => ({ openFileFromPath: mocks.openFile }));
vi.mock("element-plus", () => ({ ElMessage: { success: mocks.success, error: mocks.error } }));

import StructuralSearchView from "./StructuralSearchView.vue";
import { appState } from "@/stores/app";

describe("StructuralSearchView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.state.query = "class:User > method:get*";
    mocks.state.replacement = "displayName";
    mocks.state.results = [];
    mocks.state.previews = [];
    mocks.state.loading = false;
    mocks.state.error = null;
    mocks.run.mockResolvedValue(undefined);
    mocks.preview.mockResolvedValue(undefined);
    mocks.apply.mockResolvedValue(1);
  });

  function mountView() {
    return mount(StructuralSearchView, {
      props: {
        repoPath: "/repo",
        includeGlob: "**/*.ts",
        excludeGlob: "**/*.test.ts",
        caseSensitive: true,
      },
      global: { stubs: { "el-icon": true } },
    });
  }

  it("runs a symbol-chain query with the active file filters", async () => {
    const wrapper = mountView();
    await wrapper.get('.structural-search-view__icon-button[aria-label="Search symbols"]').trigger("click");

    expect(mocks.run).toHaveBeenCalledWith("/repo", "class:User > method:get*", {
      caseSensitive: true,
      includeGlobs: ["**/*.ts"],
      excludeGlobs: ["**/*.test.ts"],
    });
    wrapper.unmount();
  });

  it("emits filter and case changes for the shared search state", async () => {
    const wrapper = mountView();
    const filters = wrapper.findAll(".structural-search-view__filter-input");
    await filters[0].setValue("src/**/*.go");
    await filters[1].setValue("**/*_test.go");
    await wrapper.get(".structural-search-view__case-button").trigger("click");

    expect(wrapper.emitted("update:includeGlob")?.at(-1)).toEqual(["src/**/*.go"]);
    expect(wrapper.emitted("update:excludeGlob")?.at(-1)).toEqual(["**/*_test.go"]);
    expect(wrapper.emitted("update:caseSensitive")?.at(-1)).toEqual([false]);
    wrapper.unmount();
  });

  it("renders selectable symbol paths and jumps to the exact selection", async () => {
    mocks.state.results = [{
      path: "src/user.ts",
      name: "getName",
      kind: 6,
      kindLabel: "method",
      symbolPath: ["User", "getName"],
      selectionRange: { start: { line: 4, character: 2 }, end: { line: 4, character: 9 } },
      selected: true,
    }];
    mocks.openFile.mockResolvedValue(undefined);
    const wrapper = mountView();

    expect(wrapper.text()).toContain("User > getName");
    await wrapper.get(".structural-search-view__result-target").trigger("click");
    expect(mocks.openFile).toHaveBeenCalledWith("/repo/src/user.ts");
    expect(appState.cursorLine).toBe(5);
    expect(appState.cursorColumn).toBe(3);
    wrapper.unmount();
  });

  it("previews only selected symbols before applying", async () => {
    mocks.state.results = [{
      path: "src/user.ts",
      name: "getName",
      kind: 6,
      kindLabel: "method",
      symbolPath: ["User", "getName"],
      selectionRange: { start: { line: 1, character: 2 }, end: { line: 1, character: 9 } },
      selected: true,
    }];
    const wrapper = mountView();
    const commands = wrapper.findAll(".structural-search-view__command");
    await commands[0].trigger("click");

    expect(mocks.preview).toHaveBeenCalledWith("/repo", "displayName");
    wrapper.unmount();
  });

  it("labels preview action icons", () => {
    mocks.state.previews = [{
      path: "src/user.ts",
      originalContent: "old",
      modifiedContent: "new",
      replacements: 1,
      selected: true,
    }];
    const wrapper = mountView();
    const actions = wrapper.findAll(".structural-search-view__preview-actions button");
    expect(actions[0].attributes("aria-label")).toBeTruthy();
    expect(actions[1].attributes("aria-label")).toBe("Cancel");
    wrapper.unmount();
  });
});
