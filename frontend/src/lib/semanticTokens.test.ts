import { afterEach, describe, expect, it, vi } from "vitest";
import type * as monacoEditor from "monaco-editor";

const mocks = vi.hoisted(() => ({
  getLSPSemanticTokens: vi.fn(),
  ensureLSPRunning: vi.fn().mockResolvedValue(true),
  monacoLanguageToLSP: vi.fn((language: string) => language),
  getSemanticTokensDelta: vi.fn(),
}));

vi.mock("@/stores/lsp", () => ({
  getLSPSemanticTokens: mocks.getLSPSemanticTokens,
  ensureLSPRunning: mocks.ensureLSPRunning,
  monacoLanguageToLSP: mocks.monacoLanguageToLSP,
}));
vi.mock("@/stores/editor", () => ({
  editorState: { openFiles: [], activeFilePath: null },
}));
vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js", () => ({
  GetSemanticTokensDelta: mocks.getSemanticTokensDelta,
}));

import { getLSPSemanticTokens } from "@/stores/lsp";
import { editorState } from "@/stores/editor";
import {
  cleanupSemanticTokensProviders,
  encodeSemanticTokens,
  registerSemanticTokensProvider,
  SEMANTIC_TOKEN_MODIFIERS,
  SEMANTIC_TOKEN_TYPES,
} from "./semanticTokens";

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
    languages: { registerDocumentSemanticTokensProvider: register },
  } as unknown as typeof import("monaco-editor");
  return { monaco, rawDispose, register };
}

function buildModel(
  path = "/workspace/main.go",
  content = "package main",
  state: { disposed: boolean; version: number } = {
    disposed: false,
    version: 1,
  },
) {
  return {
    uri: { fsPath: path, path, toString: () => path },
    getValue: () => content,
    getVersionId: () => state.version,
    isDisposed: () => state.disposed,
  } as unknown as monacoEditor.editor.ITextModel;
}

const cancellationToken = { isCancellationRequested: false } as monacoEditor.CancellationToken;

afterEach(() => {
  cleanupSemanticTokensProviders();
  editorState.openFiles.splice(0);
  editorState.activeFilePath = null;
  vi.clearAllMocks();
});

describe("semantic tokens provider", () => {
  it("uses the complete canonical legend including decorators", () => {
    expect(SEMANTIC_TOKEN_TYPES.at(-1)).toBe("decorator");
  });

  it("encodes absolute tokens into Monaco relative five-tuples", () => {
    const encoded = encodeSemanticTokens([
      { line: 0, column: 2, length: 4, type: 2, modifiers: [0, 2] },
      { line: 2, column: 1, length: 3, type: 12, modifiers: [6] },
      { line: 2, column: 7, length: 1, type: 8 },
    ]);

    expect(Array.from(encoded)).toEqual([
      0, 2, 4, 2, 5,
      2, 1, 3, 12, 64,
      0, 6, 1, 8, 0,
    ]);
  });

  it("registers the Monaco provider, exposes the LSP legend and cleans up", async () => {
    const mock = buildMonaco();
    vi.mocked(getLSPSemanticTokens).mockResolvedValue([
      { line: 0, column: 0, length: 7, type: 15 },
    ] as never);

    registerSemanticTokensProvider(mock.monaco, "go", "C:\\repo\\main.go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.DocumentSemanticTokensProvider;

    expect(provider.getLegend()).toEqual({
      tokenTypes: [...SEMANTIC_TOKEN_TYPES],
      tokenModifiers: [...SEMANTIC_TOKEN_MODIFIERS],
    });
    const result = await provider.provideDocumentSemanticTokens(
      buildModel(),
      null,
      cancellationToken,
    );
    expect(getLSPSemanticTokens).toHaveBeenCalledWith(
      "go",
      "/workspace/main.go",
      "package main",
    );
    expect(Array.from((result as monacoEditor.languages.SemanticTokens).data)).toEqual([
      0, 0, 7, 15, 0,
    ]);

    cleanupSemanticTokensProviders();
    expect(mock.rawDispose).toHaveBeenCalledOnce();
  });

  it("carries backend result ids from a full response into the next delta request", async () => {
    const mock = buildMonaco();
    mocks.getSemanticTokensDelta
      .mockResolvedValueOnce({
        resultId: "semantic-1",
        data: [0, 0, 4, 22, 0],
      })
      .mockResolvedValueOnce({
        resultId: "semantic-2",
        edits: [
          {
            start: 2,
            deleteCount: 3,
            data: [2, 8, 1],
          },
        ],
      });
    registerSemanticTokensProvider(mock.monaco, "go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.DocumentSemanticTokensProvider;
    const model = buildModel();

    const full = await provider.provideDocumentSemanticTokens(
      model,
      null,
      cancellationToken,
    );
    expect(full).toMatchObject({ resultId: "semantic-1" });
    expect(
      Array.from((full as monacoEditor.languages.SemanticTokens).data),
    ).toEqual([0, 0, 4, 22, 0]);

    const delta = await provider.provideDocumentSemanticTokens(
      model,
      "semantic-1",
      cancellationToken,
    );
    expect(mocks.getSemanticTokensDelta).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ language: "go" }),
      "semantic-1",
    );
    expect(delta).toMatchObject({
      resultId: "semantic-2",
      edits: [
        {
          start: 2,
          deleteCount: 3,
        },
      ],
    });
    const deltaData = (
      delta as monacoEditor.languages.SemanticTokensEdits
    ).edits[0].data;
    expect(Array.from(deltaData ?? [])).toEqual([2, 8, 1]);
  });

  it.each([
    { name: "Monaco supplies the previous result id", lastResultId: "semantic-a" },
    { name: "the provider considers its cached result", lastResultId: null },
  ])(
    "does not reuse a result id after a virtual model changes paths when $name",
    async ({ lastResultId }) => {
      const mock = buildMonaco();
      editorState.openFiles.splice(
        0,
        editorState.openFiles.length,
        {
          path: "/workspace/a.ts",
          content: "const fileA = 1;",
        } as never,
        {
          path: "/workspace/b.ts",
          content: "const fileB = 2;",
        } as never,
      );
      mocks.getSemanticTokensDelta
        .mockResolvedValueOnce({
          resultId: "semantic-a",
          data: [0, 0, 4, 8, 0],
        })
        .mockResolvedValueOnce({
          resultId: "semantic-b",
          data: [0, 0, 4, 8, 0],
        });
      registerSemanticTokensProvider(mock.monaco, "typescript");
      const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.DocumentSemanticTokensProvider;
      let content = "const fileA = 1;";
      const model = {
        uri: {
          fsPath: "",
          path: "/model/1",
          toString: () => "inmemory://model/1",
        },
        getValue: () => content,
        getVersionId: () => 1,
        isDisposed: () => false,
      } as unknown as monacoEditor.editor.ITextModel;

      await provider.provideDocumentSemanticTokens(
        model,
        null,
        cancellationToken,
      );
      content = "const fileB = 2;";
      await provider.provideDocumentSemanticTokens(
        model,
        lastResultId,
        cancellationToken,
      );

      expect(mocks.getSemanticTokensDelta).toHaveBeenNthCalledWith(
        2,
        expect.objectContaining({
          filePath: "/workspace/b.ts",
          content: "const fileB = 2;",
        }),
        "",
      );
    },
  );

  it("forgets a released result id instead of reusing it for delta", async () => {
    const mock = buildMonaco();
    mocks.getSemanticTokensDelta
      .mockResolvedValueOnce({
        resultId: "semantic-release",
        data: [0, 0, 1, 8, 0],
      })
      .mockResolvedValueOnce({
        resultId: "semantic-next",
        data: [0, 1, 1, 8, 0],
      });
    registerSemanticTokensProvider(mock.monaco, "go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.DocumentSemanticTokensProvider;
    const model = buildModel();

    await provider.provideDocumentSemanticTokens(
      model,
      null,
      cancellationToken,
    );
    provider.releaseDocumentSemanticTokens?.("semantic-release");
    await provider.provideDocumentSemanticTokens(
      model,
      "semantic-release",
      cancellationToken,
    );

    expect(mocks.getSemanticTokensDelta).toHaveBeenNthCalledWith(
      2,
      expect.any(Object),
      "",
    );
  });

  it("falls back to the legacy full-token path when the delta RPC fails", async () => {
    const mock = buildMonaco();
    mocks.getSemanticTokensDelta
      .mockResolvedValueOnce({
        resultId: "semantic-before-failure",
        data: [0, 0, 1, 8, 0],
      })
      .mockRejectedValueOnce(new Error("delta unavailable"));
    vi.mocked(getLSPSemanticTokens).mockResolvedValueOnce([
      { line: 0, column: 3, length: 2, type: 12 },
    ] as never);
    registerSemanticTokensProvider(mock.monaco, "go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.DocumentSemanticTokensProvider;
    const model = buildModel();

    await provider.provideDocumentSemanticTokens(
      model,
      null,
      cancellationToken,
    );
    const fallback = await provider.provideDocumentSemanticTokens(
      model,
      "semantic-before-failure",
      cancellationToken,
    );

    expect(getLSPSemanticTokens).toHaveBeenCalledOnce();
    expect(
      Array.from((fallback as monacoEditor.languages.SemanticTokens).data),
    ).toEqual([0, 3, 2, 12, 0]);
  });

  it("drops a pending semantic response when the model is disposed", async () => {
    const mock = buildMonaco();
    const pending = deferred<{
      resultId: string;
      data: number[];
    }>();
    mocks.getSemanticTokensDelta.mockReturnValueOnce(pending.promise);
    const state = { disposed: false, version: 1 };
    registerSemanticTokensProvider(mock.monaco, "go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.DocumentSemanticTokensProvider;
    const result = provider.provideDocumentSemanticTokens(
      buildModel("/workspace/main.go", "package main", state),
      null,
      cancellationToken,
    );

    state.disposed = true;
    pending.resolve({
      resultId: "semantic-disposed",
      data: [0, 0, 1, 8, 0],
    });

    await expect(result).resolves.toBeNull();
  });

  it("drops an older response when a newer request for the same model wins", async () => {
    const mock = buildMonaco();
    const first = deferred<Array<{ line: number; column: number; length: number; type: number }>>();
    vi.mocked(getLSPSemanticTokens)
      .mockReturnValueOnce(first.promise as never)
      .mockResolvedValueOnce([
        { line: 0, column: 1, length: 2, type: 8 },
      ] as never);
    registerSemanticTokensProvider(mock.monaco, "go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.DocumentSemanticTokensProvider;
    const model = buildModel();

    const staleRequest = provider.provideDocumentSemanticTokens(
      model,
      null,
      cancellationToken,
    ) as Promise<monacoEditor.languages.SemanticTokens | null>;
    const currentResult = await provider.provideDocumentSemanticTokens(
      model,
      null,
      cancellationToken,
    );
    first.resolve([{ line: 0, column: 0, length: 1, type: 15 }]);

    expect(Array.from((currentResult as monacoEditor.languages.SemanticTokens).data)).toEqual([
      0, 1, 2, 8, 0,
    ]);
    await expect(staleRequest).resolves.toBeNull();
  });

  it("invalidates a pending request when its registration is disposed", async () => {
    const mock = buildMonaco();
    const pending = deferred<Array<{ line: number; column: number; length: number; type: number }>>();
    vi.mocked(getLSPSemanticTokens).mockReturnValue(pending.promise as never);
    const disposable = registerSemanticTokensProvider(mock.monaco, "go");
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.DocumentSemanticTokensProvider;

    const result = provider.provideDocumentSemanticTokens(
      buildModel(),
      null,
      cancellationToken,
    ) as Promise<monacoEditor.languages.SemanticTokens | null>;
    disposable.dispose();
    pending.resolve([{ line: 0, column: 0, length: 1, type: 8 }]);

    await expect(result).resolves.toBeNull();
    expect(mock.rawDispose).toHaveBeenCalledOnce();
  });

  it("virtual model 切换文件后不使用首次注册的 preferredPath", async () => {
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
    editorState.activeFilePath = "/workspace/second.ts";
    vi.mocked(getLSPSemanticTokens).mockResolvedValueOnce([]);
    const disposable = registerSemanticTokensProvider(
      mock.monaco,
      "typescript",
      "/workspace/first.ts",
    );
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.DocumentSemanticTokensProvider;
    const model = {
      uri: {
        fsPath: "",
        path: "inmemory://model/1",
        toString: () => "inmemory://model/1",
      },
      getValue: () => "const second = 2;",
    } as unknown as monacoEditor.editor.ITextModel;

    await provider.provideDocumentSemanticTokens(model, null, cancellationToken);

    expect(getLSPSemanticTokens).toHaveBeenCalledWith(
      "typescript",
      "/workspace/second.ts",
      "const second = 2;",
    );
    disposable.dispose();
    editorState.openFiles.splice(0);
    editorState.activeFilePath = null;
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
    registerSemanticTokensProvider(
      mock.monaco,
      "typescript",
      "/workspace/preferred.ts",
    );
    const provider = mock.register.mock.calls[0][1] as monacoEditor.languages.DocumentSemanticTokensProvider;
    const model = {
      uri: {
        fsPath: "",
        path: "/model/1",
        toString: () => "inmemory://model/1",
      },
      getValue: () => "const shared = 1;",
      getVersionId: () => 1,
      isDisposed: () => false,
    } as unknown as monacoEditor.editor.ITextModel;

    await expect(
      provider.provideDocumentSemanticTokens(model, null, cancellationToken),
    ).resolves.toBeNull();
    expect(mocks.getSemanticTokensDelta).not.toHaveBeenCalled();
    expect(getLSPSemanticTokens).not.toHaveBeenCalled();
  });
});
