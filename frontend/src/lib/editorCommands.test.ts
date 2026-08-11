import { beforeEach, describe, expect, it, vi } from "vitest";
import { commandRegistry } from "./commands";
import { registerEditorCommands, type EditorCommandLabels } from "./editorCommands";

const labels: EditorCommandLabels = {
  addSelectionToNextMatch: "next match",
  selectAllMatches: "all matches",
  insertCursorAbove: "cursor above",
  insertCursorBelow: "cursor below",
  insertCursorAtLineEnds: "line ends",
  cursorUndo: "cursor undo",
  splitHorizontal: "split horizontal",
  splitVertical: "split vertical",
};

function createHarness() {
  const actions: Array<{ id: string; keybindings: number[]; run: (editor: unknown) => void }> = [];
  const trigger = vi.fn();
  const focusListeners: Array<() => void> = [];
  const editor = {
    addAction: (action: typeof actions[number]) => {
      actions.push(action);
      return { dispose: vi.fn() };
    },
    trigger,
    onDidFocusEditorWidget: (listener: () => void) => {
      focusListeners.push(listener);
      return { dispose: vi.fn() };
    },
  };
  const monaco = {
    KeyMod: {
      CtrlCmd: 1 << 11,
      Shift: 1 << 10,
      Alt: 1 << 9,
      chord: (first: number, second: number) => ((first & 0xffff) << 16) | (second & 0xffff),
    },
    KeyCode: {
      KeyD: 24,
      KeyI: 29,
      KeyK: 33,
      KeyL: 34,
      KeyU: 47,
      UpArrow: 16,
      DownArrow: 18,
      Backslash: 88,
    },
  };
  return { actions, editor, monaco, trigger, focusListeners };
}

describe("editor commands", () => {
  beforeEach(() => commandRegistry.clear());

  it("registers all multi-cursor and split keybindings", () => {
    const harness = createHarness();
    const splitEditor = vi.fn();
    const disposables = registerEditorCommands(
      harness.editor as never,
      harness.monaco as never,
      { labels, splitEditor },
    );

    expect(harness.actions).toHaveLength(8);
    expect(harness.actions.map((action) => action.id)).toEqual(expect.arrayContaining([
      "koyori-ide.editor.addSelectionToNextFindMatch",
      "koyori-ide.editor.selectAllMatches",
      "koyori-ide.editor.insertCursorAbove",
      "koyori-ide.editor.insertCursorBelow",
      "koyori-ide.editor.insertCursorAtLineEnds",
      "koyori-ide.editor.cursorUndo",
      "koyori-ide.editor.splitHorizontal",
      "koyori-ide.editor.splitVertical",
    ]));
    expect(new Set(harness.actions.flatMap((action) => action.keybindings)).size).toBe(8);
    expect(commandRegistry.list().map((command) => command.id)).toEqual(expect.arrayContaining([
      "editor.addSelectionToNextFindMatch",
      "editor.selectAllMatches",
      "editor.splitHorizontal",
      "editor.splitVertical",
    ]));
    expect(disposables.length).toBeGreaterThan(harness.actions.length);
  });

  it("delegates to Monaco built-ins and split callbacks", () => {
    const harness = createHarness();
    const splitEditor = vi.fn();
    registerEditorCommands(
      harness.editor as never,
      harness.monaco as never,
      { labels, splitEditor },
    );

    harness.actions.find((action) => action.id.endsWith("addSelectionToNextFindMatch"))?.run(harness.editor);
    harness.actions.find((action) => action.id.endsWith("selectAllMatches"))?.run(harness.editor);
    harness.actions.find((action) => action.id.endsWith("splitHorizontal"))?.run(harness.editor);
    harness.actions.find((action) => action.id.endsWith("splitVertical"))?.run(harness.editor);

    expect(harness.trigger).toHaveBeenCalledWith(
      "keyboard",
      "editor.action.addSelectionToNextFindMatch",
      null,
    );
    expect(harness.trigger).toHaveBeenCalledWith("keyboard", "editor.action.selectHighlights", null);
    expect(splitEditor).toHaveBeenNthCalledWith(1, "horizontal");
    expect(splitEditor).toHaveBeenNthCalledWith(2, "vertical");
  });
});
