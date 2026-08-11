import { beforeEach, describe, expect, it, vi } from "vitest";
import type * as monacoEditor from "monaco-editor";

const { emmetHTML, emmetCSS, emmetJSX, helperDisposers } = vi.hoisted(() => {
  const helperDisposers: Array<ReturnType<typeof vi.fn>> = [];
  const makeHelper = () => vi.fn(() => {
    const dispose = vi.fn();
    helperDisposers.push(dispose);
    return dispose;
  });
  return {
    emmetHTML: makeHelper(),
    emmetCSS: makeHelper(),
    emmetJSX: makeHelper(),
    helperDisposers,
  };
});

vi.mock("emmet-monaco-es", () => ({ emmetHTML, emmetCSS, emmetJSX }));

import {
  DEFAULT_EMMET_INCLUDE_LANGUAGES,
  registerEmmetProviders,
} from "./emmet";

class FakeRange {
  constructor(
    readonly startLineNumber: number,
    readonly startColumn: number,
    readonly endLineNumber: number,
    readonly endColumn: number,
  ) {}
}

class FakeSelection extends FakeRange {
  readonly selectionStartLineNumber: number;
  readonly selectionStartColumn: number;
  readonly positionLineNumber: number;
  readonly positionColumn: number;

  constructor(
    selectionStartLineNumber: number,
    selectionStartColumn: number,
    positionLineNumber: number,
    positionColumn: number,
  ) {
    super(selectionStartLineNumber, selectionStartColumn, positionLineNumber, positionColumn);
    this.selectionStartLineNumber = selectionStartLineNumber;
    this.selectionStartColumn = selectionStartColumn;
    this.positionLineNumber = positionLineNumber;
    this.positionColumn = positionColumn;
  }

  isEmpty() {
    return this.selectionStartLineNumber === this.positionLineNumber
      && this.selectionStartColumn === this.positionColumn;
  }
}

function createBareMonaco() {
  const createListeners = new Set<(editor: monacoEditor.editor.ICodeEditor) => void>();
  const createListenerDispose = vi.fn();
  return {
    monaco: {
      languages: {},
      Range: FakeRange,
      Selection: FakeSelection,
      editor: {
        getEditors: () => [],
        onDidCreateEditor: (listener: (editor: monacoEditor.editor.ICodeEditor) => void) => {
          createListeners.add(listener);
          return {
            dispose: () => {
              createListeners.delete(listener);
              createListenerDispose();
            },
          };
        },
      },
    } as unknown as typeof import("monaco-editor"),
    createListenerDispose,
  };
}

interface EditorHarness {
  monaco: typeof import("monaco-editor");
  type(text: string): void;
  overtype(text: string): void;
  value(): string;
  cursorColumn(): number;
  executeEdits: ReturnType<typeof vi.fn>;
  listenerCount(): number;
}

function createEditorHarness(
  language: string,
  initialValue: string,
  initialOffset = initialValue.length,
): EditorHarness {
  let value = initialValue;
  let version = 1;
  let cursorOffset = initialOffset;
  const disposed = false;
  let selection = new FakeSelection(1, cursorOffset + 1, 1, cursorOffset + 1);
  const contentListeners = new Set<(event: monacoEditor.editor.IModelContentChangedEvent) => void>();
  const typeListeners = new Set<(text: string) => void>();
  const disposeListeners = new Set<() => void>();

  const model = {
    getLanguageId: () => language,
    isDisposed: () => disposed,
    getVersionId: () => version,
    getValueLength: () => value.length,
    getOffsetAt: (position: monacoEditor.IPosition) => position.column - 1,
    getPositionAt: (offset: number) => ({ lineNumber: 1, column: offset + 1 }),
    getValueInRange: (range: monacoEditor.IRange) => value.slice(range.startColumn - 1, range.endColumn - 1),
  } as unknown as monacoEditor.editor.ITextModel;

  const fireChange = (event: monacoEditor.editor.IModelContentChangedEvent) => {
    for (const listener of [...contentListeners]) listener(event);
  };

  const executeEdits = vi.fn((
    _source: string,
    edits: monacoEditor.editor.IIdentifiedSingleEditOperation[],
    endSelections?: monacoEditor.Selection[],
  ) => {
    for (const edit of edits) {
      const offset = edit.range.startColumn - 1;
      value = value.slice(0, offset) + edit.text + value.slice(offset);
      version += 1;
      fireChange({
        changes: [{
          range: edit.range,
          rangeLength: 0,
          rangeOffset: offset,
          text: edit.text,
        }],
        eol: "\n",
        isEolChange: false,
        isFlush: false,
        isRedoing: false,
        isUndoing: false,
        versionId: version,
      } as unknown as monacoEditor.editor.IModelContentChangedEvent);
    }
    if (endSelections?.[0]) {
      selection = endSelections[0] as unknown as FakeSelection;
      cursorOffset = selection.positionColumn - 1;
    }
    return true;
  });

  const editor = {
    getModel: () => model,
    getPosition: () => ({ lineNumber: 1, column: cursorOffset + 1 }),
    hasTextFocus: () => true,
    getSelections: () => [selection],
    executeEdits,
    onDidChangeModelContent: (listener: (event: monacoEditor.editor.IModelContentChangedEvent) => void) => {
      contentListeners.add(listener);
      return { dispose: () => contentListeners.delete(listener) };
    },
    onDidDispose: (listener: () => void) => {
      disposeListeners.add(listener);
      return { dispose: () => disposeListeners.delete(listener) };
    },
    onDidType: (listener: (text: string) => void) => {
      typeListeners.add(listener);
      return { dispose: () => typeListeners.delete(listener) };
    },
  } as unknown as monacoEditor.editor.ICodeEditor;

  const monaco = {
    languages: {},
    Range: FakeRange,
    Selection: FakeSelection,
    editor: {
      getEditors: () => [editor],
      onDidCreateEditor: () => ({ dispose: () => undefined }),
    },
  } as unknown as typeof import("monaco-editor");

  return {
    monaco,
    type(text: string) {
      const range = new FakeRange(1, cursorOffset + 1, 1, cursorOffset + 1);
      const rangeOffset = cursorOffset;
      value = value.slice(0, cursorOffset) + text + value.slice(cursorOffset);
      cursorOffset += text.length;
      selection = new FakeSelection(1, cursorOffset + 1, 1, cursorOffset + 1);
      version += 1;
      fireChange({
        changes: [{ range, rangeLength: 0, rangeOffset, text }],
        eol: "\n",
        isEolChange: false,
        isFlush: false,
        isRedoing: false,
        isUndoing: false,
        versionId: version,
      } as unknown as monacoEditor.editor.IModelContentChangedEvent);
      for (const listener of [...typeListeners]) listener(text);
    },
    overtype(text: string) {
      if (value.slice(cursorOffset, cursorOffset + text.length) !== text) {
        throw new Error(`cannot overtype ${JSON.stringify(text)} at ${cursorOffset}`);
      }
      cursorOffset += text.length;
      selection = new FakeSelection(1, cursorOffset + 1, 1, cursorOffset + 1);
      for (const listener of [...typeListeners]) listener(text);
    },
    value: () => value,
    cursorColumn: () => selection.positionColumn,
    executeEdits,
    listenerCount: () => contentListeners.size,
  };
}

const { monaco } = createBareMonaco();

describe("Emmet Monaco providers", () => {
  beforeEach(() => {
    emmetHTML.mockClear();
    emmetCSS.mockClear();
    emmetJSX.mockClear();
    helperDisposers.splice(0);
  });

  it("registers the supported markup, stylesheet, JSX and Vue languages", () => {
    const registration = registerEmmetProviders(monaco, {
      enabled: true,
      includeLanguages: DEFAULT_EMMET_INCLUDE_LANGUAGES,
    });

    expect(emmetHTML.mock.calls).toEqual([
      [monaco, ["html"], { tokenizer: "standard" }],
      [monaco, ["vue"], { tokenizer: "standard" }],
    ]);
    expect(emmetCSS.mock.calls).toEqual([
      [monaco, ["css"], { tokenizer: "standard" }],
      [monaco, ["scss"], { tokenizer: "standard" }],
      [monaco, ["less"], { tokenizer: "standard" }],
    ]);
    expect(emmetJSX.mock.calls).toEqual([
      [monaco, ["javascriptreact"], { tokenizer: "standard" }],
      [monaco, ["typescriptreact"], { tokenizer: "standard" }],
    ]);

    registration.dispose();
  });

  it("uses includeLanguages to map additional Monaco languages", () => {
    const registration = registerEmmetProviders(monaco, {
      enabled: true,
      includeLanguages: {
        templ: "html",
        javascript: "javascriptreact",
        postcss: "css",
      },
    });

    expect(emmetHTML).toHaveBeenCalledWith(monaco, ["templ"], { tokenizer: "standard" });
    expect(emmetJSX).toHaveBeenCalledWith(monaco, ["javascript"], { tokenizer: "standard" });
    expect(emmetCSS).toHaveBeenCalledWith(monaco, ["postcss"], { tokenizer: "standard" });

    registration.dispose();
  });

  it("registers each language once and disposes it after the final editor releases it", () => {
    const first = registerEmmetProviders(monaco, { enabled: true });
    const second = registerEmmetProviders(monaco, { enabled: true });

    expect(emmetHTML).toHaveBeenCalledTimes(2);
    expect(emmetCSS).toHaveBeenCalledTimes(3);
    expect(emmetJSX).toHaveBeenCalledTimes(2);

    first.dispose();
    expect(helperDisposers.every((dispose) => dispose.mock.calls.length === 0)).toBe(true);

    second.dispose();
    expect(helperDisposers.every((dispose) => dispose.mock.calls.length === 1)).toBe(true);

    second.dispose();
    expect(helperDisposers.every((dispose) => dispose.mock.calls.length === 1)).toBe(true);
  });

  it("does not register providers when Emmet is disabled", () => {
    const registration = registerEmmetProviders(monaco, { enabled: false });

    expect(emmetHTML).not.toHaveBeenCalled();
    expect(emmetCSS).not.toHaveBeenCalled();
    expect(emmetJSX).not.toHaveBeenCalled();
    expect(() => registration.dispose()).not.toThrow();
  });

  it("inserts an HTML closing tag after a focused single-cursor > input", async () => {
    const harness = createEditorHarness("html", "<div");
    const registration = registerEmmetProviders(harness.monaco, { enabled: true });

    harness.type(">");
    await Promise.resolve();

    expect(harness.value()).toBe("<div></div>");
    expect(harness.cursorColumn()).toBe(6);
    expect(harness.executeEdits).toHaveBeenCalledTimes(1);
    expect(harness.executeEdits).toHaveBeenCalledWith(
      "emmet.auto-close-tag",
      [expect.objectContaining({ text: "</div>" })],
      expect.any(Array),
    );
    registration.dispose();
  });

  it("closes a tag when > overtypes Monaco's existing <> auto-closing pair", async () => {
    const harness = createEditorHarness("html", "<div>", 4);
    const registration = registerEmmetProviders(harness.monaco, { enabled: true });

    harness.overtype(">");
    await Promise.resolve();

    expect(harness.value()).toBe("<div></div>");
    expect(harness.cursorColumn()).toBe(6);
    expect(harness.executeEdits).toHaveBeenCalledTimes(1);
    registration.dispose();
  });

  it("supports Vue components, including names that match HTML void elements", async () => {
    for (const component of ["MyCard", "Input", "Link"]) {
      const vue = createEditorHarness("vue", `<template><${component}`);
      const registration = registerEmmetProviders(vue.monaco, { enabled: true });
      vue.type(">");
      await Promise.resolve();
      expect(vue.value()).toBe(`<template><${component}></${component}>`);
      registration.dispose();
    }
  });

  it("keeps JSX Emmet completion without applying HTML tag autoclose to JSX or TSX", async () => {
    const cases = [
      { language: "javascriptreact", value: "const view = <UI.Button" },
      { language: "typescriptreact", value: "const render = <T extends unknown" },
    ];
    for (const entry of cases) {
      const harness = createEditorHarness(entry.language, entry.value);
      const registration = registerEmmetProviders(harness.monaco, { enabled: true });
      harness.type(">");
      await Promise.resolve();
      expect(harness.value()).toBe(`${entry.value}>`);
      expect(harness.executeEdits).not.toHaveBeenCalled();
      registration.dispose();
    }
  });

  it("does not close void tags, self-closing tags, or an existing closing tag", async () => {
    const cases = [
      { language: "html", value: "<img", expected: "<img>" },
      { language: "html", value: "<INPUT", expected: "<INPUT>" },
      { language: "vue", value: "<input", expected: "<input>" },
      { language: "html", value: "<section /", expected: "<section />" },
      { language: "html", value: "<div</div>", offset: 4, expected: "<div></div>" },
    ];
    for (const entry of cases) {
      const harness = createEditorHarness(entry.language, entry.value, entry.offset);
      const registration = registerEmmetProviders(harness.monaco, { enabled: true });
      harness.type(">");
      await Promise.resolve();
      expect(harness.value()).toBe(entry.expected);
      expect(harness.executeEdits).not.toHaveBeenCalled();
      registration.dispose();
    }
  });

  it("does not mistake an outer same-name closing tag for the new nested tag", async () => {
    const direct = createEditorHarness("html", "<div><div</div>", 9);
    const directRegistration = registerEmmetProviders(direct.monaco, { enabled: true });
    direct.type(">");
    await Promise.resolve();
    expect(direct.value()).toBe("<div><div></div></div>");
    expect(direct.executeEdits).toHaveBeenCalledTimes(1);
    directRegistration.dispose();

    const overtype = createEditorHarness("html", "<div><div></div>", 9);
    const overtypeRegistration = registerEmmetProviders(overtype.monaco, { enabled: true });
    overtype.overtype(">");
    await Promise.resolve();
    expect(overtype.value()).toBe("<div><div></div></div>");
    expect(overtype.executeEdits).toHaveBeenCalledTimes(1);
    overtypeRegistration.dispose();
  });

  it("preserves a complete same-name closing structure and ignores non-markup tag text", async () => {
    const complete = createEditorHarness("html", "<div><div</div></div>", 9);
    const completeRegistration = registerEmmetProviders(complete.monaco, { enabled: true });
    complete.type(">");
    await Promise.resolve();
    expect(complete.value()).toBe("<div><div></div></div>");
    expect(complete.executeEdits).not.toHaveBeenCalled();
    completeRegistration.dispose();

    const commentValue = "<!-- <div> --><div</div>";
    const comment = createEditorHarness("html", commentValue, commentValue.indexOf("</div>"));
    const commentRegistration = registerEmmetProviders(comment.monaco, { enabled: true });
    comment.type(">");
    await Promise.resolve();
    expect(comment.value()).toBe("<!-- <div> --><div></div>");
    expect(comment.executeEdits).not.toHaveBeenCalled();
    commentRegistration.dispose();

    const rawTextValue = "<textarea><div></textarea><div</div>";
    const rawText = createEditorHarness(
      "html",
      rawTextValue,
      rawTextValue.lastIndexOf("</div>"),
    );
    const rawTextRegistration = registerEmmetProviders(rawText.monaco, { enabled: true });
    rawText.type(">");
    await Promise.resolve();
    expect(rawText.value()).toBe("<textarea><div></textarea><div></div>");
    expect(rawText.executeEdits).not.toHaveBeenCalled();
    rawTextRegistration.dispose();

    const interpolationValue = "<template>{{ '<div>' }}<div</div>";
    const interpolation = createEditorHarness(
      "vue",
      interpolationValue,
      interpolationValue.lastIndexOf("</div>"),
    );
    const interpolationRegistration = registerEmmetProviders(interpolation.monaco, { enabled: true });
    interpolation.type(">");
    await Promise.resolve();
    expect(interpolation.value()).toBe("<template>{{ '<div>' }}<div></div>");
    expect(interpolation.executeEdits).not.toHaveBeenCalled();
    interpolationRegistration.dispose();
  });

  it("ignores tag-like text in HTML script blocks and JSX strings", async () => {
    const html = createEditorHarness("html", "<script>const value = \"<div");
    const htmlRegistration = registerEmmetProviders(html.monaco, { enabled: true });
    html.type(">");
    await Promise.resolve();
    expect(html.value()).toBe("<script>const value = \"<div>");
    expect(html.executeEdits).not.toHaveBeenCalled();
    htmlRegistration.dispose();

    const attribute = createEditorHarness("vue", "<div :title=\"'<span");
    const attributeRegistration = registerEmmetProviders(attribute.monaco, { enabled: true });
    attribute.type(">");
    await Promise.resolve();
    expect(attribute.value()).toBe("<div :title=\"'<span>");
    expect(attribute.executeEdits).not.toHaveBeenCalled();
    attributeRegistration.dispose();

    const jsx = createEditorHarness("typescriptreact", "const value = \"<div");
    const jsxRegistration = registerEmmetProviders(jsx.monaco, { enabled: true });
    jsx.type(">");
    await Promise.resolve();
    expect(jsx.value()).toBe("const value = \"<div>");
    expect(jsx.executeEdits).not.toHaveBeenCalled();
    jsxRegistration.dispose();
  });

  it("keeps the shared editor listener until the final lease is disposed", async () => {
    const harness = createEditorHarness("html", "<div");
    const first = registerEmmetProviders(harness.monaco, { enabled: true });
    const second = registerEmmetProviders(harness.monaco, { enabled: true });
    expect(harness.listenerCount()).toBe(1);

    first.dispose();
    expect(harness.listenerCount()).toBe(1);
    second.dispose();
    expect(harness.listenerCount()).toBe(0);

    harness.type(">");
    await Promise.resolve();
    expect(harness.value()).toBe("<div>");
  });
});
