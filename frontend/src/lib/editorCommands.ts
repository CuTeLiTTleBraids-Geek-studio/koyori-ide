// Koyori IDE 模块 · Editor Commands。
// 喵，这是 Koyori IDE 的 Editor Commands 模块（前端实现）~
import type * as Monaco from "monaco-editor";
import { commandRegistry, type RegisteredCommand } from "@/lib/commands";

export interface EditorCommandLabels {
  addSelectionToNextMatch: string;
  selectAllMatches: string;
  insertCursorAbove: string;
  insertCursorBelow: string;
  insertCursorAtLineEnds: string;
  cursorUndo: string;
  splitHorizontal: string;
  splitVertical: string;
}

export interface EditorCommandContext {
  labels: EditorCommandLabels;
  splitEditor: (orientation: "horizontal" | "vertical") => void;
}

interface EditorCommandSpec {
  id: string;
  label: keyof EditorCommandLabels;
  keybinding: string;
  keys: (monaco: typeof import("monaco-editor")) => number;
  run: (editor: Monaco.editor.ICodeEditor) => void;
}

const specs: EditorCommandSpec[] = [
  {
    id: "editor.addSelectionToNextFindMatch",
    label: "addSelectionToNextMatch",
    keybinding: "Ctrl+D",
    keys: (monaco) => monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyD,
    run: (editor) => editor.trigger("keyboard", "editor.action.addSelectionToNextFindMatch", null),
  },
  {
    id: "editor.selectAllMatches",
    label: "selectAllMatches",
    keybinding: "Ctrl+Shift+L",
    keys: (monaco) => monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.KeyL,
    run: (editor) => editor.trigger("keyboard", "editor.action.selectHighlights", null),
  },
  {
    id: "editor.insertCursorAbove",
    label: "insertCursorAbove",
    keybinding: "Ctrl+Alt+Up",
    keys: (monaco) => monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.UpArrow,
    run: (editor) => editor.trigger("keyboard", "editor.action.insertCursorAbove", null),
  },
  {
    id: "editor.insertCursorBelow",
    label: "insertCursorBelow",
    keybinding: "Ctrl+Alt+Down",
    keys: (monaco) => monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.DownArrow,
    run: (editor) => editor.trigger("keyboard", "editor.action.insertCursorBelow", null),
  },
  {
    id: "editor.insertCursorAtLineEnds",
    label: "insertCursorAtLineEnds",
    keybinding: "Shift+Alt+I",
    keys: (monaco) => monaco.KeyMod.Shift | monaco.KeyMod.Alt | monaco.KeyCode.KeyI,
    run: (editor) => editor.trigger("keyboard", "editor.action.insertCursorAtEndOfEachLineSelected", null),
  },
  {
    id: "editor.cursorUndo",
    label: "cursorUndo",
    keybinding: "Ctrl+U",
    keys: (monaco) => monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyU,
    run: (editor) => editor.trigger("keyboard", "cursorUndo", null),
  },
];

let focusedRegistrationOwner: symbol | null = null;

export function registerEditorCommands(
  editor: Monaco.editor.IStandaloneCodeEditor,
  monaco: typeof import("monaco-editor"),
  context: EditorCommandContext,
): Monaco.IDisposable[] {
  const owner = Symbol("editor-command-owner");
  const disposables: Monaco.IDisposable[] = specs.map((spec) => editor.addAction({
    id: `koyori-ide.${spec.id}`,
    label: context.labels[spec.label],
    keybindings: [spec.keys(monaco)],
    run: (activeEditor) => spec.run(activeEditor),
  }));

  const splitSpecs: Array<{
    id: string;
    label: keyof EditorCommandLabels;
    keybinding: string;
    keys: number;
    orientation: "horizontal" | "vertical";
  }> = [
    {
      id: "editor.splitHorizontal",
      label: "splitHorizontal",
      keybinding: "Ctrl+\\",
      keys: monaco.KeyMod.CtrlCmd | monaco.KeyCode.Backslash,
      orientation: "horizontal",
    },
    {
      id: "editor.splitVertical",
      label: "splitVertical",
      keybinding: "Ctrl+K Ctrl+\\",
      keys: monaco.KeyMod.chord(
        monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyK,
        monaco.KeyMod.CtrlCmd | monaco.KeyCode.Backslash,
      ),
      orientation: "vertical",
    },
  ];

  for (const spec of splitSpecs) {
    disposables.push(editor.addAction({
      id: `koyori-ide.${spec.id}`,
      label: context.labels[spec.label],
      keybindings: [spec.keys],
      run: () => context.splitEditor(spec.orientation),
    }));
  }

  const publishFocusedCommands = () => {
    focusedRegistrationOwner = owner;
    const registered: RegisteredCommand[] = [
      ...specs.map((spec) => ({
        id: spec.id,
        title: context.labels[spec.label],
        keybinding: spec.keybinding,
        category: "editor" as const,
        handler: () => spec.run(editor),
      })),
      ...splitSpecs.map((spec) => ({
        id: spec.id,
        title: context.labels[spec.label],
        keybinding: spec.keybinding,
        category: "view" as const,
        handler: () => context.splitEditor(spec.orientation),
      })),
    ];
    commandRegistry.replaceSource("focused-editor", registered);
  };

  publishFocusedCommands();
  disposables.push(editor.onDidFocusEditorWidget(publishFocusedCommands));
  disposables.push({
    dispose: () => {
      if (focusedRegistrationOwner !== owner) return;
      focusedRegistrationOwner = null;
      commandRegistry.replaceSource("focused-editor", []);
    },
  });

  return disposables;
}
