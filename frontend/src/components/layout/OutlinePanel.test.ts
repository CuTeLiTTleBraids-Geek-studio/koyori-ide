import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import type { LSPDocumentSymbol } from "@/types";

const { getDocumentSymbols } = vi.hoisted(() => ({
  getDocumentSymbols: vi.fn(),
}));

vi.mock("@/stores/lsp", () => ({
  getLSPDocumentSymbols: getDocumentSymbols,
}));

import OutlinePanel from "./OutlinePanel.vue";
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
    detail: `${name} detail`,
    kind: 12,
    range: {
      start: { line: startLine, character: 0 },
      end: { line: endLine, character: 100 },
    },
    selectionRange: {
      start: { line: startLine, character: 2 },
      end: { line: startLine, character: 8 },
    },
    children,
  };
}

function mountPanel() {
  return mount(OutlinePanel, {
    global: {
      stubs: {
        "el-icon": true,
      },
    },
  });
}

describe("OutlinePanel", () => {
  beforeEach(() => {
    getDocumentSymbols.mockReset();
    appState.language = "en";
    appState.cursorLine = 1;
    appState.cursorColumn = 1;
    appState.editorJumpSeq = 0;
    editorState.openFiles = [file("C:/repo/main.go")];
    editorState.activeFilePath = "C:/repo/main.go";
  });

  it("loads and renders a nested document-symbol tree", async () => {
    getDocumentSymbols.mockResolvedValue([
      symbol("outer", 0, 20, [symbol("inner", 5, 10)]),
    ]);

    const wrapper = mountPanel();
    await flushPromises();

    expect(getDocumentSymbols).toHaveBeenCalledWith(
      "go",
      "C:/repo/main.go",
      "package main",
    );
    expect(wrapper.get('[data-symbol="outer"]').attributes("data-depth")).toBe("0");
    expect(wrapper.get('[data-symbol="inner"]').attributes("data-depth")).toBe("1");
    expect(wrapper.get('[data-symbol="outer"]').attributes("aria-current")).toBe("location");
    expect(wrapper.get(".outline-panel__tree").element.tagName).toBe("UL");
    expect(wrapper.find('[role="tree"]').exists()).toBe(false);
    expect(wrapper.find('[role="treeitem"]').exists()).toBe(false);
    expect(wrapper.findAll(".outline-panel__row").every((row) => row.element.tagName === "LI"))
      .toBe(true);
  });

  it("collapses a branch and jumps to a selected symbol", async () => {
    getDocumentSymbols.mockResolvedValue([
      symbol("outer", 0, 20, [symbol("inner", 5, 10)]),
    ]);
    const wrapper = mountPanel();
    await flushPromises();

    const toggle = wrapper.get<HTMLButtonElement>('[data-toggle="0"]');
    expect(toggle.attributes("aria-expanded")).toBe("true");
    await toggle.trigger("click");
    expect(toggle.attributes("aria-expanded")).toBe("false");
    expect(wrapper.find('[data-symbol="inner"]').exists()).toBe(false);
    await toggle.trigger("click");
    expect(toggle.attributes("aria-expanded")).toBe("true");
    await wrapper.get('[data-symbol="inner"]').trigger("click");

    expect(appState.cursorLine).toBe(6);
    expect(appState.cursorColumn).toBe(3);
    expect(appState.editorJumpSeq).toBe(1);
  });

  it("keeps outline row actions as separate focusable native buttons", async () => {
    getDocumentSymbols.mockResolvedValue([
      symbol("outer", 0, 20, [symbol("inner", 5, 10)]),
    ]);
    const wrapper = mount(OutlinePanel, {
      attachTo: document.body,
      global: { stubs: { "el-icon": true } },
    });
    await flushPromises();

    const row = wrapper.findAll(".outline-panel__row")[0];
    const toggle = row.get<HTMLButtonElement>('[data-toggle="0"]');
    const symbolButton = row.get<HTMLButtonElement>('[data-symbol="outer"]');
    expect(row.find("button button").exists()).toBe(false);

    toggle.element.focus();
    expect(document.activeElement).toBe(toggle.element);
    symbolButton.element.focus();
    expect(document.activeElement).toBe(symbolButton.element);
  });

  it("filters descendants while retaining their ancestor path", async () => {
    getDocumentSymbols.mockResolvedValue([
      symbol("outer", 0, 20, [
        symbol("wantedChild", 5, 10),
        symbol("otherChild", 11, 15),
      ]),
    ]);
    const wrapper = mountPanel();
    await flushPromises();

    await wrapper.get('[data-test="outline-filter"]').setValue("wanted");

    expect(wrapper.find('[data-symbol="outer"]').exists()).toBe(true);
    expect(wrapper.get('[data-toggle="0"]').attributes("aria-expanded")).toBe("true");
    expect(wrapper.find('[data-symbol="wantedChild"]').exists()).toBe(true);
    expect(wrapper.find('[data-symbol="otherChild"]').exists()).toBe(false);
  });

  it("ignores symbols returned for a file that is no longer active", async () => {
    let resolveFirst!: (symbols: LSPDocumentSymbol[]) => void;
    const first = new Promise<LSPDocumentSymbol[]>((resolve) => { resolveFirst = resolve; });
    getDocumentSymbols
      .mockReturnValueOnce(first)
      .mockResolvedValueOnce([symbol("newFileSymbol", 0, 2)]);
    const wrapper = mountPanel();
    await vi.waitFor(() => expect(getDocumentSymbols).toHaveBeenCalledTimes(1));

    editorState.openFiles.push(file("C:/repo/new.go"));
    editorState.activeFilePath = "C:/repo/new.go";
    await vi.waitFor(() => expect(getDocumentSymbols).toHaveBeenCalledTimes(2));
    resolveFirst([symbol("staleSymbol", 0, 2)]);
    await flushPromises();

    expect(wrapper.find('[data-symbol="newFileSymbol"]').exists()).toBe(true);
    expect(wrapper.find('[data-symbol="staleSymbol"]').exists()).toBe(false);
  });

  it("shows the no-file state without requesting symbols", async () => {
    editorState.openFiles = [];
    editorState.activeFilePath = null;
    getDocumentSymbols.mockResolvedValue([]);

    const wrapper = mountPanel();
    await flushPromises();

    expect(wrapper.get('[data-test="outline-empty"]').text()).toContain("Open a file");
    expect(getDocumentSymbols).not.toHaveBeenCalled();
  });
});
