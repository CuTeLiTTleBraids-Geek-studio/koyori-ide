import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import type { LSPDocumentSymbol } from "@/types";

const { getDocumentSymbols } = vi.hoisted(() => ({
  getDocumentSymbols: vi.fn(),
}));

vi.mock("@/stores/lsp", () => ({
  getLSPDocumentSymbols: getDocumentSymbols,
}));

import BreadcrumbBar from "./BreadcrumbBar.vue";
import { appState } from "@/stores/app";
import { editorState, type OpenFile } from "@/stores/editor";

enableAutoUnmount(afterEach);

function file(path: string, content = "package main"): OpenFile {
  return {
    path,
    name: path.split(/[/\\]/).pop() ?? path,
    content,
    originalContent: content,
    language: "go",
    isDirty: false,
  };
}

function symbol(
  name: string,
  startLine: number,
  endLine: number,
  children: LSPDocumentSymbol[] = [],
): LSPDocumentSymbol {
  return {
    name,
    kind: 12,
    range: {
      start: { line: startLine, character: 0 },
      end: { line: endLine, character: 100 },
    },
    selectionRange: {
      start: { line: startLine, character: 2 },
      end: { line: startLine, character: 6 },
    },
    children,
  };
}

describe("BreadcrumbBar", () => {
  beforeEach(() => {
    getDocumentSymbols.mockReset();
    appState.breadcrumbVisible = true;
    appState.currentProject = "C:/repo";
    appState.cursorLine = 8;
    appState.cursorColumn = 3;
    appState.editorJumpSeq = 0;
    editorState.openFiles = [file("C:/repo/src/main.go")];
    editorState.activeFilePath = "C:/repo/src/main.go";
  });

  it("renders workspace-relative path and the deepest symbol containing the cursor", async () => {
    getDocumentSymbols.mockResolvedValue([
      symbol("outer", 0, 20, [symbol("inner", 5, 10)]),
    ]);
    const wrapper = mount(BreadcrumbBar, { global: { stubs: { "el-icon": true } } });
    await flushPromises();

    expect(wrapper.text()).toContain("src");
    expect(wrapper.text()).toContain("main.go");
    expect(wrapper.text()).toContain("outer");
    expect(wrapper.text()).toContain("inner");
  });

  it("jumps to a symbol selection range", async () => {
    getDocumentSymbols.mockResolvedValue([symbol("target", 4, 10)]);
    const wrapper = mount(BreadcrumbBar, { global: { stubs: { "el-icon": true } } });
    await flushPromises();

    await wrapper.get('[data-symbol="target"]').trigger("click");

    expect(appState.cursorLine).toBe(5);
    expect(appState.cursorColumn).toBe(3);
    expect(appState.editorJumpSeq).toBe(1);
  });

  it("ignores document symbols that resolve after the active file changes", async () => {
    let resolveFirst!: (symbols: LSPDocumentSymbol[]) => void;
    const first = new Promise<LSPDocumentSymbol[]>((resolve) => { resolveFirst = resolve; });
    getDocumentSymbols
      .mockReturnValueOnce(first)
      .mockResolvedValueOnce([symbol("newFileSymbol", 0, 20)]);
    const wrapper = mount(BreadcrumbBar, { global: { stubs: { "el-icon": true } } });
    await vi.waitFor(() => expect(getDocumentSymbols).toHaveBeenCalledTimes(1));

    editorState.openFiles.push(file("C:/repo/src/new.go"));
    editorState.activeFilePath = "C:/repo/src/new.go";
    await vi.waitFor(() => expect(getDocumentSymbols).toHaveBeenCalledTimes(2));
    resolveFirst([symbol("staleSymbol", 0, 20)]);
    await flushPromises();

    expect(wrapper.text()).toContain("newFileSymbol");
    expect(wrapper.text()).not.toContain("staleSymbol");
  });

  it("hides when disabled or when there is no active file", async () => {
    getDocumentSymbols.mockResolvedValue([]);
    const wrapper = mount(BreadcrumbBar, { global: { stubs: { "el-icon": true } } });
    await flushPromises();

    appState.breadcrumbVisible = false;
    await wrapper.vm.$nextTick();
    expect(wrapper.find(".breadcrumb-bar").exists()).toBe(false);

    appState.breadcrumbVisible = true;
    editorState.activeFilePath = null;
    await wrapper.vm.$nextTick();
    expect(wrapper.find(".breadcrumb-bar").exists()).toBe(false);
  });
});
