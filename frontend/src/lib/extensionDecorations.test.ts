import { afterEach, describe, expect, it, vi } from "vitest";
import {
  Position,
  Range,
  Selection,
  type TextDocument,
} from "@/lib/extensionHost/vscodeApi";
import {
  applyExtensionDecorations,
  createExtensionDecorationType,
  createExtensionTextEditor,
  registerExtensionEditorSurface,
} from "@/lib/extensionDecorations";

class FakeRange {
  constructor(
    readonly startLineNumber: number,
    readonly startColumn: number,
    readonly endLineNumber: number,
    readonly endColumn: number,
  ) {}
}

class FakeSelection extends FakeRange {
  constructor(
    startLineNumber: number,
    startColumn: number,
    endLineNumber: number,
    endColumn: number,
  ) {
    super(startLineNumber, startColumn, endLineNumber, endColumn);
  }
}

const fakeMonaco = {
  Range: FakeRange,
  Selection: FakeSelection,
} as unknown as typeof import("monaco-editor");

type FakeEditor = {
  calls: Array<{ oldIds: string[]; decorations: unknown[] }>;
  selection: {
    selectionStartLineNumber: number;
    selectionStartColumn: number;
    positionLineNumber: number;
    positionColumn: number;
  };
  getSelection(): FakeEditor["selection"];
  setSelection(value: unknown): void;
  deltaDecorations(oldIds: string[], decorations: unknown[]): string[];
  revealRange: ReturnType<typeof vi.fn>;
  revealRangeInCenter: ReturnType<typeof vi.fn>;
  revealRangeInCenterIfOutsideViewport: ReturnType<typeof vi.fn>;
  revealRangeAtTop: ReturnType<typeof vi.fn>;
};

function makeEditor(): FakeEditor {
  let nextId = 1;
  const editor: FakeEditor = {
    calls: [],
    selection: {
      selectionStartLineNumber: 2,
      selectionStartColumn: 3,
      positionLineNumber: 4,
      positionColumn: 5,
    },
    getSelection() {
      return this.selection;
    },
    setSelection(value: unknown) {
      const selection = value as FakeSelection;
      this.selection = {
        selectionStartLineNumber: selection.startLineNumber,
        selectionStartColumn: selection.startColumn,
        positionLineNumber: selection.endLineNumber,
        positionColumn: selection.endColumn,
      };
    },
    deltaDecorations(oldIds, decorations) {
      this.calls.push({ oldIds: [...oldIds], decorations: [...decorations] });
      return decorations.map(() => "decoration-" + nextId++);
    },
    revealRange: vi.fn(),
    revealRangeInCenter: vi.fn(),
    revealRangeInCenterIfOutsideViewport: vi.fn(),
    revealRangeAtTop: vi.fn(),
  };
  return editor;
}

const documentSnapshot = {
  uri: { fsPath: "C:/workspace/sample.ts", path: "/workspace/sample.ts", scheme: "file" },
} as unknown as TextDocument;
const range = new Range(new Position(0, 1), new Position(1, 4));

function register(path: string, editor: FakeEditor) {
  return registerExtensionEditorSurface(
    path,
    editor as unknown as import("monaco-editor").editor.IStandaloneCodeEditor,
    fakeMonaco,
  );
}

afterEach(() => {
  document.head.replaceChildren();
});

describe("extension decoration adapter", () => {
  it("rejects CSS injection values while preserving safe declarations", () => {
    const editor = makeEditor();
    const surface = register("C:\\workspace\\sample.ts", editor);
    const type = createExtensionDecorationType("acme.safe", {
      backgroundColor: "#123456",
      color: "red; background-image:url(https://attacker.invalid)",
      borderStyle: "solid;content:bad",
    });

    const style = document.head.querySelector("style[data-koyori-extension-decoration]");
    expect(style).not.toBeNull();
    expect(style?.textContent).toContain("background-color:#123456;");
    expect(style?.textContent).not.toContain("attacker.invalid");
    expect(style?.textContent).not.toContain("content:bad");

    type.dispose();
    surface.dispose();
  });

  it("keeps same-path split surfaces independent when one is disposed", () => {
    const first = makeEditor();
    const second = makeEditor();
    const firstSurface = register("C:\\workspace\\sample.ts", first);
    const secondSurface = register("C:\\workspace\\sample.ts", second);
    const type = createExtensionDecorationType("acme.split", { color: "#fff" });

    applyExtensionDecorations("C:/workspace/sample.ts", type.key, [range]);
    expect(second.calls).toHaveLength(1);
    expect(first.calls).toHaveLength(0);

    secondSurface.dispose();
    applyExtensionDecorations("C:/workspace/sample.ts", type.key, [range]);
    expect(first.calls).toHaveLength(1);
    expect(second.calls.at(-1)?.decorations).toEqual([]);

    type.dispose();
    firstSurface.dispose();
  });

  it("clears Monaco decorations and the style element on type disposal", () => {
    const editor = makeEditor();
    const surface = register("/workspace/sample.ts", editor);
    const type = createExtensionDecorationType("acme.dispose", { color: "#0f0" });

    applyExtensionDecorations("/workspace/sample.ts", type.key, [range]);
    expect(document.head.querySelector("style[data-koyori-extension-decoration]")).not.toBeNull();
    type.dispose();

    expect(editor.calls.at(-1)?.oldIds).toEqual(["decoration-1"]);
    expect(editor.calls.at(-1)?.decorations).toEqual([]);
    expect(document.head.querySelector("style[data-koyori-extension-decoration]")).toBeNull();
    surface.dispose();
    expect(() => applyExtensionDecorations("/workspace/sample.ts", type.key, [range])).toThrow(/disposed/);
  });

  it("maps selection coordinates and reveal modes to Monaco", () => {
    const editor = makeEditor();
    const surface = register("/workspace/sample.ts", editor);
    const textEditor = createExtensionTextEditor("/workspace/sample.ts", documentSnapshot);

    expect(textEditor.selection?.anchor).toEqual(new Position(1, 2));
    textEditor.selection = new Selection(
      new Position(2, 3),
      new Position(4, 5),
    );
    expect(editor.selection).toEqual({
      selectionStartLineNumber: 3,
      selectionStartColumn: 4,
      positionLineNumber: 5,
      positionColumn: 6,
    });
    textEditor.revealRange(range, 2);
    expect(editor.revealRangeInCenterIfOutsideViewport).toHaveBeenCalledTimes(1);

    surface.dispose();
  });
});
