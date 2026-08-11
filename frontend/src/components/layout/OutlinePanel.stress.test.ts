import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import type { LSPDocumentSymbol } from "@/types";

// GOAL-P0-08 stress / regression fixtures for the Outline panel.
//
// The user-reported symptom is "clicking a symbol hangs". Rather than assuming a
// single root cause, these fixtures pin every independently-reachable failure
// mode found by tracing the click chain:
//
//   OutlinePanel.jumpToSymbol
//     -> appState.cursorLine/cursorColumn/editorJumpSeq
//     -> CodeEditor watch(editorJumpSeq) -> setPosition + revealLineInCenter
//     -> Monaco onDidChangeCursorPosition -> handleCursorChange
//     -> appState.cursorLine/cursorColumn  (does NOT bump editorJumpSeq)
//
// The chain does not close into an infinite loop, so the hang is not a jump
// feedback cycle. What it does contain: unbounded recursion over
// `symbol.children` in four separate walks, one LSP request per keystroke, a
// full-tree walk per cursor move, and expansion state discarded on every edit.

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

/** A symbol whose children array contains itself. A hostile or buggy language
 * server can produce this; an unguarded recursive walk never terminates. */
function cyclicSymbol(): LSPDocumentSymbol {
  const node = symbol("cyclic", 0, 100);
  node.children = [node];
  return node;
}

/** A linear chain `depth` levels deep. Recursion without a depth cap overflows
 * the stack; the panel must still render. */
function deepChain(depth: number): LSPDocumentSymbol {
  let node = symbol(`leaf`, depth, depth);
  for (let level = depth - 1; level >= 0; level -= 1) {
    node = symbol(`level${level}`, level, depth + 1, [node]);
  }
  return node;
}

/** `count` sibling symbols, each with one child. */
function wideTree(count: number): LSPDocumentSymbol[] {
  return Array.from({ length: count }, (_unused, index) =>
    symbol(`fn${index}`, index * 3, index * 3 + 2, [
      symbol(`inner${index}`, index * 3 + 1, index * 3 + 1),
    ]),
  );
}

function mountPanel() {
  return mount(OutlinePanel, { global: { stubs: { "el-icon": true } } });
}

describe("OutlinePanel stress fixtures (GOAL-P0-08)", () => {
  beforeEach(() => {
    getDocumentSymbols.mockReset();
    appState.language = "en";
    appState.cursorLine = 1;
    appState.cursorColumn = 1;
    appState.editorJumpSeq = 0;
    editorState.openFiles = [file("C:/repo/main.go")];
    editorState.activeFilePath = "C:/repo/main.go";
  });

  // -------------------------------------------------------------------------
  // Hard hang: unbounded recursion
  // -------------------------------------------------------------------------

  it("renders a cyclic symbol tree without unbounded recursion", async () => {
    getDocumentSymbols.mockResolvedValue([cyclicSymbol()]);

    const wrapper = mountPanel();
    await flushPromises();

    // The panel must produce a bounded row set. Before the fix, collectRows and
    // collectBranchIds recursed through the self-reference until the stack blew.
    const rows = wrapper.findAll(".outline-panel__row");
    expect(rows.length).toBeGreaterThan(0);
    expect(rows.length).toBeLessThan(200);
  });

  it("survives a cursor move over a cyclic tree", async () => {
    getDocumentSymbols.mockResolvedValue([cyclicSymbol()]);
    const wrapper = mountPanel();
    await flushPromises();

    // activeSymbolId walks the tree on every cursor change. A cycle inside a
    // containing range recursed forever.
    appState.cursorLine = 5;
    appState.cursorColumn = 1;
    await flushPromises();

    expect(wrapper.findAll(".outline-panel__row").length).toBeGreaterThan(0);
  });

  it("renders a very deep symbol chain without overflowing the stack", async () => {
    getDocumentSymbols.mockResolvedValue([deepChain(20000)]);

    const wrapper = mountPanel();
    await flushPromises();

    expect(wrapper.findAll(".outline-panel__row").length).toBeGreaterThan(0);
  });

  it("survives filtering a very deep symbol chain", async () => {
    getDocumentSymbols.mockResolvedValue([deepChain(20000)]);
    const wrapper = mountPanel();
    await flushPromises();

    // symbolMatches recurses independently of collectRows.
    await wrapper.get('[data-test="outline-filter"]').setValue("leaf");
    await flushPromises();

    expect(wrapper.find(".outline-panel").exists()).toBe(true);
  });

  // -------------------------------------------------------------------------
  // Request storm (AC 3)
  // -------------------------------------------------------------------------

  it("coalesces rapid edits into a single document-symbol request", async () => {
    getDocumentSymbols.mockResolvedValue([symbol("outer", 0, 20)]);
    mountPanel();
    await flushPromises();
    const afterMount = getDocumentSymbols.mock.calls.length;

    const open = editorState.openFiles[0]!;
    for (let keystroke = 0; keystroke < 25; keystroke += 1) {
      open.content = `package main // ${keystroke}`;
      await Promise.resolve();
    }
    await flushPromises();

    // Before the fix this issued one request per keystroke.
    const issued = getDocumentSymbols.mock.calls.length - afterMount;
    expect(issued).toBeLessThanOrEqual(2);
  });

  it("does not request document symbols when a symbol is clicked", async () => {
    getDocumentSymbols.mockResolvedValue([
      symbol("outer", 0, 20, [symbol("inner", 5, 10)]),
    ]);
    const wrapper = mountPanel();
    await flushPromises();
    const before = getDocumentSymbols.mock.calls.length;

    await wrapper.get('[data-symbol="inner"]').trigger("click");
    await flushPromises();

    // A click changes cursor state only. It must never re-request symbols for an
    // unchanged document version.
    expect(getDocumentSymbols.mock.calls.length).toBe(before);
  });

  it("keeps expansion state across edits to the same file", async () => {
    getDocumentSymbols.mockResolvedValue([
      symbol("outer", 0, 20, [symbol("inner", 5, 10)]),
    ]);
    const wrapper = mountPanel();
    await flushPromises();

    // Collapse, then type. The collapse must survive: discarding expansion on
    // every keystroke makes the outline unusable while editing.
    await wrapper.get('[data-toggle="0"]').trigger("click");
    expect(wrapper.find('[data-symbol="inner"]').exists()).toBe(false);

    editorState.openFiles[0]!.content = "package main // edited";
    await flushPromises();
    await vi.waitFor(() =>
      expect(wrapper.find('[data-symbol="outer"]').exists()).toBe(true),
    );

    expect(wrapper.get('[data-toggle="0"]').attributes("aria-expanded")).toBe("false");
  });

  // -------------------------------------------------------------------------
  // Bounded work per click (AC 5 proxy: auditable counts, not wall-clock)
  // -------------------------------------------------------------------------

  it("issues no extra symbol requests per click on a large tree", async () => {
    // 200 roots × 2 nodes = 400 DOM nodes — small enough to render quickly in
    // jsdom, large enough to prove the flat-index linear scan replaced the
    // O(n) per-click recursive descent.
    //
    // AC 5 asks for a renderer long-task budget and explicitly allows
    // auditable event/request caps when timing cannot be measured reliably.
    // The request-count assertion is the load-bearing evidence; wall-clock
    // timing is deliberately not asserted because jsdom measures DOM
    // throughput, not symbol-resolution cost. The timing half of AC 5 stays
    // `U` until it runs against real Monaco in packaged E2E.
    const COUNT = 200;
    getDocumentSymbols.mockResolvedValue(wideTree(COUNT));
    const wrapper = mountPanel();
    await flushPromises();

    expect(wrapper.find(`[data-symbol="fn${COUNT - 1}"]`).exists()).toBe(true);
    const requestsAfterLoad = getDocumentSymbols.mock.calls.length;

    const CLICKS = 3;
    let lastClicked = -1;
    for (let click = 0; click < CLICKS; click += 1) {
      await wrapper.get(`[data-symbol="fn${click}"]`).trigger("click");
      lastClicked = click;
    }

    // AC 3: a click must not re-request document symbols.
    expect(getDocumentSymbols.mock.calls.length).toBe(requestsAfterLoad);
    expect(appState.editorJumpSeq).toBe(CLICKS);
    // The cursor landed on the last clicked symbol.
    expect(appState.cursorLine).toBe(lastClicked * 3 + 1);
  }, 30000);

  // -------------------------------------------------------------------------
  // Malformed data must fail soft (AC 6)
  // -------------------------------------------------------------------------

  it("fails soft on symbols with missing or invalid ranges", async () => {
    const broken = symbol("broken", 0, 5);
    // A server that violates the protocol: absent selectionRange.
    delete (broken as { selectionRange?: unknown }).selectionRange;
    const negative = symbol("negative", -5, -1);
    getDocumentSymbols.mockResolvedValue([broken, negative, symbol("ok", 6, 8)]);

    const wrapper = mountPanel();
    await flushPromises();

    // The panel must still render and remain interactive.
    expect(wrapper.find('[data-symbol="ok"]').exists()).toBe(true);

    // Clicking a symbol with no selectionRange must not throw, and must not
    // drive the editor to a nonsensical position.
    await wrapper.get('[data-symbol="broken"]').trigger("click");
    expect(appState.cursorLine).toBeGreaterThanOrEqual(1);
    expect(appState.cursorColumn).toBeGreaterThanOrEqual(1);

    await wrapper.get('[data-symbol="negative"]').trigger("click");
    expect(appState.cursorLine).toBeGreaterThanOrEqual(1);
    expect(appState.cursorColumn).toBeGreaterThanOrEqual(1);
  });

  it("clears loading when the symbol request rejects", async () => {
    getDocumentSymbols.mockRejectedValue(new Error("lsp exploded"));

    const wrapper = mountPanel();
    await flushPromises();

    // A rejected request must not leave the panel spinning forever.
    expect(wrapper.find('[data-test="outline-loading"]').exists()).toBe(false);
  });

  it("recovers after a failed request when the next request succeeds", async () => {
    getDocumentSymbols
      .mockRejectedValueOnce(new Error("lsp exploded"))
      .mockResolvedValue([symbol("recovered", 0, 3)]);

    const wrapper = mountPanel();
    await flushPromises();

    editorState.openFiles.push(file("C:/repo/other.go"));
    editorState.activeFilePath = "C:/repo/other.go";
    await flushPromises();
    await vi.waitFor(() =>
      expect(wrapper.find('[data-symbol="recovered"]').exists()).toBe(true),
    );
  });
});
