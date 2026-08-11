import { afterEach, describe, expect, it, vi } from "vitest";
import type * as monacoEditor from "monaco-editor";

vi.mock("@/stores/lsp", () => ({
  getLSPInlayHints: vi.fn(),
  monacoLanguageToLSP: vi.fn((language: string, filePath: string) =>
    filePath.endsWith(".vue") ? "vue" : language,
  ),
}));
vi.mock("@/stores/editor", () => ({
  editorState: { openFiles: [], activeFilePath: null },
}));
vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js", () => ({
  ResolveInlayHint: vi.fn(),
}));

import * as LSPServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js";
import { editorState } from "@/stores/editor";
import { getLSPInlayHints } from "@/stores/lsp";
import {
  cleanupInlayHintsProviders,
  mapLSPInlayHintToMonaco,
  registerInlayHintsProvider,
} from "./inlayHints";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function buildMonaco() {
  const rawDispose = vi.fn();
  const register = vi.fn().mockReturnValue({ dispose: rawDispose });
  const monaco = {
    languages: { registerInlayHintsProvider: register },
    Uri: {
      parse: vi.fn((value: string) => ({ value, kind: "uri" })),
      file: vi.fn((value: string) => ({ value, kind: "file" })),
    },
  } as unknown as typeof import("monaco-editor");
  return { monaco, rawDispose, register };
}

function buildModel(path = "/workspace/main.go", content = "package main") {
  return {
    uri: { fsPath: path, path, toString: () => path },
    getValue: () => content,
  } as unknown as monacoEditor.editor.ITextModel;
}

const fullRange = {
  startLineNumber: 1,
  startColumn: 1,
  endLineNumber: 100,
  endColumn: 100,
} as monacoEditor.Range;
const cancellationToken = { isCancellationRequested: false } as monacoEditor.CancellationToken;

afterEach(() => {
  cleanupInlayHintsProviders();
  editorState.openFiles.splice(0);
  editorState.activeFilePath = null;
  vi.clearAllMocks();
});

describe("inlay hints provider", () => {
  it("maps rich LSP labels, tooltip, padding and text edits", () => {
    const mock = buildMonaco();
    const hint = mapLSPInlayHintToMonaco(
      {
        position: { line: 4, character: 7 },
        label: [
          {
            value: ": string",
            tooltip: { value: "resolved type" },
            location: {
              uri: "file:///workspace/types.go",
              range: {
                start: { line: 1, character: 2 },
                end: { line: 1, character: 8 },
              },
            },
          },
        ],
        kind: 1,
        paddingLeft: true,
        tooltip: { value: "type hint" },
        textEdits: [
          {
            range: {
              start: { line: 4, character: 7 },
              end: { line: 4, character: 7 },
            },
            newText: ": string",
          },
        ],
      },
      mock.monaco,
    );

    expect(hint).toEqual({
      label: [
        {
          label: ": string",
          tooltip: { value: "resolved type" },
          location: {
            uri: { value: "file:///workspace/types.go", kind: "uri" },
            range: {
              startLineNumber: 2,
              startColumn: 3,
              endLineNumber: 2,
              endColumn: 9,
            },
          },
        },
      ],
      position: { lineNumber: 5, column: 8 },
      kind: 1,
      paddingLeft: true,
      paddingRight: true,
      tooltip: { value: "type hint" },
      textEdits: [
        {
          range: {
            startLineNumber: 5,
            startColumn: 8,
            endLineNumber: 5,
            endColumn: 8,
          },
          text: ": string",
        },
      ],
    });
  });

  it("preserves fenced Markdown for both the hint and individual label parts", () => {
    const mock = buildMonaco();
    const hint = mapLSPInlayHintToMonaco(
      {
        position: { line: 1, character: 2 },
        label: [
          {
            value: ": Result",
            tooltip: {
              kind: "markdown",
              value: "```go\nresult := call()\n```",
            },
          },
        ],
        tooltip: {
          kind: "markdown",
          value: "```typescript\nconst value = await call();\n```",
        },
      },
      mock.monaco,
    );

    expect(hint?.tooltip).toEqual({
      value: "```typescript\nconst value = await call();\n```",
      isTrusted: false,
      supportHtml: false,
    });
    expect(
      (hint?.label as monacoEditor.languages.InlayHintLabelPart[])[0].tooltip,
    ).toEqual({
      value: "```go\nresult := call()\n```",
      isTrusted: false,
      supportHtml: false,
    });
  });

  it("keeps explicit plaintext tooltips out of Monaco's Markdown renderer", () => {
    const hint = mapLSPInlayHintToMonaco({
      position: { line: 0, character: 0 },
      label: ": string",
      tooltip: {
        kind: "plaintext",
        value: "```go\nnot markdown\n```",
      },
    });

    expect(hint?.tooltip).toBe("```go\nnot markdown\n```");
  });

  it("registers, maps simplified backend hints, filters the requested range and cleans up", async () => {
    const mock = buildMonaco();
    vi.mocked(getLSPInlayHints).mockResolvedValue([
      { line: 4, column: 7, label: ": string", kind: 1 },
      { line: 20, column: 2, label: "name:", kind: 2 },
    ] as never);
    registerInlayHintsProvider(mock.monaco, "go", "C:\\repo\\main.go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.InlayHintsProvider;

    const result = await provider.provideInlayHints(
      buildModel(),
      { ...fullRange, endLineNumber: 10 } as monacoEditor.Range,
      cancellationToken,
    );
    expect(getLSPInlayHints).toHaveBeenCalledWith(
      "go",
      "/workspace/main.go",
      "package main",
      0,
      10,
    );
    expect((result as monacoEditor.languages.InlayHintList).hints).toEqual([
      {
        label: ": string",
        position: { lineNumber: 5, column: 8 },
        kind: 1,
        paddingLeft: false,
        paddingRight: true,
        tooltip: undefined,
      },
    ]);

    cleanupInlayHintsProviders();
    expect(mock.rawDispose).toHaveBeenCalledOnce();
  });

  it("drops an older response when a newer request for the same model wins", async () => {
    const mock = buildMonaco();
    const first = deferred<Array<{ line: number; column: number; label: string; kind: number }>>();
    vi.mocked(getLSPInlayHints)
      .mockReturnValueOnce(first.promise as never)
      .mockResolvedValueOnce([
        { line: 1, column: 1, label: ": int", kind: 1 },
      ] as never);
    registerInlayHintsProvider(mock.monaco, "go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.InlayHintsProvider;
    const model = buildModel();

    const staleRequest = provider.provideInlayHints(
      model,
      fullRange,
      cancellationToken,
    ) as Promise<monacoEditor.languages.InlayHintList | null>;
    const currentResult = await provider.provideInlayHints(
      model,
      fullRange,
      cancellationToken,
    );
    first.resolve([{ line: 0, column: 0, label: "stale", kind: 2 }]);

    expect((currentResult as monacoEditor.languages.InlayHintList).hints[0].label).toBe(": int");
    await expect(staleRequest).resolves.toBeNull();
  });

  it("maps a virtual model only when its content uniquely identifies an open file", async () => {
    const mock = buildMonaco();
    editorState.openFiles.splice(
      0,
      editorState.openFiles.length,
      {
        path: "/workspace/first.ts",
        content: "const first = 1;",
      } as never,
      {
        path: "/workspace/second.ts",
        content: "const second = 2;",
      } as never,
    );
    editorState.activeFilePath = "/workspace/first.ts";
    vi.mocked(getLSPInlayHints).mockResolvedValueOnce([]);
    registerInlayHintsProvider(
      mock.monaco,
      "typescript",
      "/workspace/first.ts",
    );
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.InlayHintsProvider;
    const model = {
      uri: {
        fsPath: "",
        path: "/model/1",
        toString: () => "inmemory://model/1",
      },
      getValue: () => "const second = 2;",
      isDisposed: () => false,
    } as unknown as monacoEditor.editor.ITextModel;

    await provider.provideInlayHints(model, fullRange, cancellationToken);

    expect(getLSPInlayHints).toHaveBeenCalledWith(
      "typescript",
      "/workspace/second.ts",
      "const second = 2;",
      0,
      100,
    );
  });

  it.each([
    {
      name: "no open file has matching content",
      openFiles: [{ path: "/workspace/other.ts", content: "const other = 1;" }],
    },
    {
      name: "multiple open files have matching content",
      openFiles: [
        { path: "/workspace/first.ts", content: "const shared = 1;" },
        { path: "/workspace/second.ts", content: "const shared = 1;" },
      ],
    },
  ])("skips an unresolved virtual model when $name", async ({ openFiles }) => {
    const mock = buildMonaco();
    editorState.openFiles.splice(
      0,
      editorState.openFiles.length,
      ...openFiles.map((file) => file as never),
    );
    editorState.activeFilePath = openFiles[0]?.path ?? null;
    registerInlayHintsProvider(mock.monaco, "typescript", "/workspace/preferred.ts");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.InlayHintsProvider;
    const model = {
      uri: {
        fsPath: "",
        path: "/model/1",
        toString: () => "inmemory://model/1",
      },
      getValue: () => "const shared = 1;",
      isDisposed: () => false,
    } as unknown as monacoEditor.editor.ITextModel;

    await expect(
      provider.provideInlayHints(model, fullRange, cancellationToken),
    ).resolves.toBeNull();
    expect(getLSPInlayHints).not.toHaveBeenCalled();
  });

  it("resolves a hint through Wails and maps the enriched response", async () => {
    const mock = buildMonaco();
    vi.mocked(getLSPInlayHints).mockResolvedValue([
      { line: 0, column: 0, label: ": int", kind: 1 },
    ] as never);
    vi.mocked(LSPServiceBindings.ResolveInlayHint).mockResolvedValue({
      line: 0,
      column: 0,
      label: ": number",
      kind: 1,
      tooltip: "resolved",
      textEdits: [],
      paddingLeft: false,
      paddingRight: true,
    });
    registerInlayHintsProvider(mock.monaco, "go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.InlayHintsProvider;
    const provided = await provider.provideInlayHints(
      buildModel(),
      fullRange,
      cancellationToken,
    );
    const hint = (provided as monacoEditor.languages.InlayHintList).hints[0];

    const resolved = await provider.resolveInlayHint?.(hint, cancellationToken);

    expect(LSPServiceBindings.ResolveInlayHint).toHaveBeenCalledWith(
      "go",
      { line: 0, column: 0, label: ": int", kind: 1 },
    );
    expect(resolved).toEqual(
      expect.objectContaining({ label: ": number", tooltip: "resolved" }),
    );
  });

  it("resolve 时把 canonical label parts 显式转换为后端扁平 DTO", async () => {
    const mock = buildMonaco();
    const labelParts = [
      { value: "name:", tooltip: "parameter name" },
      { value: " " },
    ];
    vi.mocked(getLSPInlayHints).mockResolvedValue([
      {
        position: { line: 3, character: 4 },
        label: labelParts,
        kind: 2,
        data: { resolveID: 9 },
      },
    ]);
    vi.mocked(LSPServiceBindings.ResolveInlayHint).mockResolvedValue({
      line: 3,
      column: 4,
      label: "name: ",
      kind: 2,
      rawLabel: labelParts,
      data: { resolveID: 9 },
    });
    registerInlayHintsProvider(mock.monaco, "go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.InlayHintsProvider;
    const provided = await provider.provideInlayHints(
      buildModel(),
      fullRange,
      cancellationToken,
    );
    const hint = (provided as monacoEditor.languages.InlayHintList).hints[0];

    const resolved = await provider.resolveInlayHint?.(hint, cancellationToken);

    expect(LSPServiceBindings.ResolveInlayHint).toHaveBeenCalledWith("go", {
      line: 3,
      column: 4,
      label: "name: ",
      kind: 2,
      rawLabel: labelParts,
      data: { resolveID: 9 },
    });
    expect(resolved?.label).toEqual([
      expect.objectContaining({ label: "name:" }),
      expect.objectContaining({ label: " " }),
    ]);
  });

  it("falls back to the original hint when best-effort resolve is unavailable", async () => {
    const mock = buildMonaco();
    vi.mocked(LSPServiceBindings.ResolveInlayHint).mockRejectedValue(
      new Error("method unavailable"),
    );
    registerInlayHintsProvider(mock.monaco, "go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.InlayHintsProvider;
    const hint = {
      label: ": int",
      position: { lineNumber: 1, column: 1 },
    } as monacoEditor.languages.InlayHint;

    await expect(provider.resolveInlayHint?.(hint, cancellationToken)).resolves.toBe(hint);
  });
});
