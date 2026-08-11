import { afterEach, describe, expect, it, vi } from "vitest";
import type * as monacoEditor from "monaco-editor";

vi.mock("@/stores/lsp", () => ({
  getLSPCodeLenses: vi.fn(),
  monacoLanguageToLSP: vi.fn((language: string, filePath: string) =>
    filePath.endsWith(".vue") ? "vue" : language,
  ),
}));
vi.mock("@/stores/editor", () => ({
  editorState: { openFiles: [], activeFilePath: null },
}));
vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js", () => ({
  ExecuteRefactorCommand: vi.fn(),
  ResolveCodeLens: vi.fn(),
}));

import * as LSPServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js";
import { editorState } from "@/stores/editor";
import { getLSPCodeLenses } from "@/stores/lsp";
import {
  cleanupCodeLensProviders,
  CODE_LENS_EXECUTE_COMMAND_ID,
  registerCodeLensProvider,
  type CodeLensExecutionPayload,
} from "./codeLens";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function buildMonaco() {
  const providerDispose = vi.fn();
  const commandDispose = vi.fn();
  const registerProvider = vi.fn().mockReturnValue({ dispose: providerDispose });
  const registerCommand = vi.fn().mockReturnValue({ dispose: commandDispose });
  const getEditors = vi.fn().mockReturnValue([]);
  const monaco = {
    languages: { registerCodeLensProvider: registerProvider },
    editor: { registerCommand, getEditors },
  } as unknown as typeof import("monaco-editor");
  return {
    monaco,
    providerDispose,
    commandDispose,
    registerProvider,
    registerCommand,
    getEditors,
  };
}

function buildModel(path = "/workspace/main.go", content = "package main") {
  return {
    uri: { fsPath: path, path, toString: () => path },
    getValue: () => content,
  } as unknown as monacoEditor.editor.ITextModel;
}

function buildVirtualModel(content: string) {
  return {
    uri: {
      path: "inmemory://model/virtual",
      toString: () => "inmemory://model/virtual",
    },
    getValue: () => content,
  } as unknown as monacoEditor.editor.ITextModel;
}

function buildOpenFile(path: string, content: string) {
  return {
    path,
    name: path,
    content,
    originalContent: content,
    language: "go",
    isDirty: false,
  };
}

const cancellationToken = { isCancellationRequested: false } as monacoEditor.CancellationToken;

afterEach(() => {
  cleanupCodeLensProviders();
  editorState.openFiles.splice(0);
  editorState.activeFilePath = null;
  vi.clearAllMocks();
});

describe("code lens provider", () => {
  it("routes a virtual model through its unique open-file content match", async () => {
    const content = "package matched";
    editorState.openFiles.splice(
      0,
      editorState.openFiles.length,
      buildOpenFile("C:\\repo\\preferred.go", "package preferred"),
      buildOpenFile("C:\\repo\\matched.go", content),
    );
    editorState.activeFilePath = "C:\\repo\\preferred.go";
    vi.mocked(getLSPCodeLenses).mockResolvedValue([]);
    const mock = buildMonaco();
    registerCodeLensProvider(mock.monaco, "go", "C:\\repo\\preferred.go");
    const provider = mock.registerProvider.mock.calls[0][1] as monacoEditor.languages.CodeLensProvider;

    const result = await provider.provideCodeLenses(
      buildVirtualModel(content),
      cancellationToken,
    );

    expect(getLSPCodeLenses).toHaveBeenCalledWith(
      "go",
      "C:\\repo\\matched.go",
      content,
    );
    expect(result).toEqual({ lenses: [], dispose: expect.any(Function) });
  });

  it.each(["zero", "multiple"] as const)(
    "virtual model with %s content matches skips provide and resolve RPCs",
    async (matchCount) => {
      const content = "package unresolved";
      const openFiles =
        matchCount === "zero"
          ? [buildOpenFile("C:\\repo\\preferred.go", "package preferred")]
          : [
              buildOpenFile("C:\\repo\\preferred.go", content),
              buildOpenFile("C:\\repo\\other.go", content),
            ];
      editorState.openFiles.splice(0, editorState.openFiles.length, ...openFiles);
      editorState.activeFilePath = "C:\\repo\\preferred.go";
      vi.mocked(getLSPCodeLenses).mockClear();
      vi.mocked(LSPServiceBindings.ResolveCodeLens).mockClear();
      const mock = buildMonaco();
      registerCodeLensProvider(mock.monaco, "go", "C:\\repo\\preferred.go");
      const provider = mock.registerProvider.mock.calls[0][1] as monacoEditor.languages.CodeLensProvider;
      const model = buildVirtualModel(content);
      const lens = {
        range: {
          startLineNumber: 1,
          startColumn: 1,
          endLineNumber: 1,
          endColumn: 1,
        },
      } as monacoEditor.languages.CodeLens;

      const provided = await provider.provideCodeLenses(model, cancellationToken);
      const resolved = await provider.resolveCodeLens?.(
        model,
        lens,
        cancellationToken,
      );

      expect(provided).toBeNull();
      expect(resolved).toBeNull();
      expect(getLSPCodeLenses).not.toHaveBeenCalled();
      expect(LSPServiceBindings.ResolveCodeLens).not.toHaveBeenCalled();
    },
  );

  it("preserves a server command and arguments through the executable bridge", async () => {
    const mock = buildMonaco();
    vi.mocked(getLSPCodeLenses).mockResolvedValue([
      {
        range: {
          start: { line: 3, character: 2 },
          end: { line: 3, character: 8 },
        },
        command: {
          title: "Run test",
          command: "gopls.run_tests",
          arguments: ["TestMain", 42],
        },
        data: { id: 7 },
      },
    ] as never);
    registerCodeLensProvider(mock.monaco, "go", "C:\\repo\\main.go");
    const provider = mock.registerProvider.mock.calls[0][1] as monacoEditor.languages.CodeLensProvider;

    const result = await provider.provideCodeLenses(buildModel(), cancellationToken);
    const lens = (result as monacoEditor.languages.CodeLensList).lenses[0];

    expect(getLSPCodeLenses).toHaveBeenCalledWith(
      "go",
      "/workspace/main.go",
      "package main",
    );
    expect(lens).toEqual(
      expect.objectContaining({
        range: {
          startLineNumber: 4,
          startColumn: 3,
          endLineNumber: 4,
          endColumn: 9,
        },
        command: expect.objectContaining({
          id: CODE_LENS_EXECUTE_COMMAND_ID,
          title: "Run test",
        }),
      }),
    );
    expect(lens.command?.arguments?.[0]).toEqual(
      expect.objectContaining({
        language: "go",
        command: "gopls.run_tests",
        arguments: ["TestMain", 42],
      }),
    );
  });

  it("maps references and implementation aliases to clickable Monaco actions", async () => {
    const mock = buildMonaco();
    vi.mocked(getLSPCodeLenses).mockResolvedValue([
      { line: 1, column: 2, label: "3 references", command: "references" },
      { line: 4, column: 0, label: "implementation", command: "implementation" },
    ]);
    registerCodeLensProvider(mock.monaco, "go");
    const provider = mock.registerProvider.mock.calls[0][1] as monacoEditor.languages.CodeLensProvider;

    const result = await provider.provideCodeLenses(buildModel(), cancellationToken);
    const lenses = (result as monacoEditor.languages.CodeLensList).lenses;

    expect(lenses[0].command?.id).toBe(CODE_LENS_EXECUTE_COMMAND_ID);
    expect(lenses[1].command?.id).toBe(CODE_LENS_EXECUTE_COMMAND_ID);
    expect(
      (lenses[0].command?.arguments?.[0] as CodeLensExecutionPayload)
        .clientCommand,
    ).toBe("editor.action.referenceSearch.trigger");
    expect(
      (lenses[1].command?.arguments?.[0] as CodeLensExecutionPayload)
        .clientCommand,
    ).toBe("editor.action.goToImplementation");
  });

  it("registers one shared command bridge and forwards clicks to Wails", async () => {
    const mock = buildMonaco();
    vi.mocked(LSPServiceBindings.ExecuteRefactorCommand).mockResolvedValue(undefined);
    const first = registerCodeLensProvider(mock.monaco, "go");
    const second = registerCodeLensProvider(mock.monaco, "typescript");

    expect(mock.registerCommand).toHaveBeenCalledOnce();
    const handler = mock.registerCommand.mock.calls[0][1] as (
      accessor: unknown,
      payload: unknown,
    ) => void;
    const payload = {
      language: "go",
      command: "gopls.run_tests",
      arguments: ["TestMain"],
      filePath: "/workspace/main.go",
      line: 3,
      column: 2,
    };
    handler(null, payload);
    await Promise.resolve();

    expect(LSPServiceBindings.ExecuteRefactorCommand).toHaveBeenCalledWith(
      "go",
      "gopls.run_tests",
      ["TestMain"],
    );
    first.dispose();
    expect(mock.commandDispose).not.toHaveBeenCalled();
    second.dispose();
    expect(mock.commandDispose).toHaveBeenCalledOnce();
  });

  it("native references command 先定位到 lens 位置再触发 editor action", () => {
    const mock = buildMonaco();
    const setPosition = vi.fn();
    const focus = vi.fn();
    const trigger = vi.fn();
    mock.getEditors.mockReturnValue([
      {
        getModel: () => buildModel("/workspace/main.go"),
        hasTextFocus: () => false,
        setPosition,
        focus,
        trigger,
      },
    ]);
    const disposable = registerCodeLensProvider(mock.monaco, "go");
    const handler = mock.registerCommand.mock.calls[0][1] as (
      accessor: unknown,
      payload: CodeLensExecutionPayload,
    ) => void;

    handler(null, {
      language: "go",
      command: "references",
      clientCommand: "editor.action.referenceSearch.trigger",
      arguments: [],
      filePath: "/workspace/main.go",
      line: 6,
      column: 3,
    });

    expect(setPosition).toHaveBeenCalledWith({ lineNumber: 7, column: 4 });
    expect(focus).toHaveBeenCalledOnce();
    expect(trigger).toHaveBeenCalledWith(
      CODE_LENS_EXECUTE_COMMAND_ID,
      "editor.action.referenceSearch.trigger",
      undefined,
    );
    disposable.dispose();
  });

  it("drops an older response when a newer request for the same model wins", async () => {
    const mock = buildMonaco();
    const first = deferred<Array<{ line: number; column: number; label: string; command: string }>>();
    vi.mocked(getLSPCodeLenses)
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce([
        { line: 1, column: 1, label: "current", command: "references" },
      ]);
    registerCodeLensProvider(mock.monaco, "go");
    const provider = mock.registerProvider.mock.calls[0][1] as monacoEditor.languages.CodeLensProvider;
    const model = buildModel();

    const staleRequest = provider.provideCodeLenses(
      model,
      cancellationToken,
    ) as Promise<monacoEditor.languages.CodeLensList | null>;
    const currentResult = await provider.provideCodeLenses(model, cancellationToken);
    first.resolve([{ line: 0, column: 0, label: "stale", command: "references" }]);

    expect((currentResult as monacoEditor.languages.CodeLensList).lenses[0].command?.title).toBe("current");
    await expect(staleRequest).resolves.toBeNull();
  });

  it("resolves a lens through Wails and maps the returned command", async () => {
    const mock = buildMonaco();
    vi.mocked(getLSPCodeLenses).mockResolvedValue([
      { line: 2, column: 1, label: "loading", command: "" },
    ]);
    vi.mocked(LSPServiceBindings.ResolveCodeLens).mockResolvedValue({
      line: 2,
      column: 1,
      endLine: 2,
      endColumn: 5,
      label: "2 references",
      command: "references",
      arguments: ["symbol"],
    });
    registerCodeLensProvider(mock.monaco, "go");
    const provider = mock.registerProvider.mock.calls[0][1] as monacoEditor.languages.CodeLensProvider;
    const model = buildModel();
    const provided = await provider.provideCodeLenses(model, cancellationToken);
    const lens = (provided as monacoEditor.languages.CodeLensList).lenses[0];

    const resolved = await provider.resolveCodeLens?.(model, lens, cancellationToken);

    expect(LSPServiceBindings.ResolveCodeLens).toHaveBeenCalledWith(
      "go",
      {
        line: 2,
        column: 1,
        endLine: 2,
        endColumn: 1,
        label: "loading",
        command: "",
      },
    );
    expect(resolved?.command).toEqual({
      id: CODE_LENS_EXECUTE_COMMAND_ID,
      title: "2 references",
      arguments: [
        expect.objectContaining({
          command: "references",
          clientCommand: "editor.action.referenceSearch.trigger",
          arguments: ["symbol"],
        }),
      ],
    });
  });

  it("falls back to the original lens when best-effort resolve is unavailable", async () => {
    const mock = buildMonaco();
    vi.mocked(LSPServiceBindings.ResolveCodeLens).mockRejectedValue(
      new Error("method unavailable"),
    );
    registerCodeLensProvider(mock.monaco, "go");
    const provider = mock.registerProvider.mock.calls[0][1] as monacoEditor.languages.CodeLensProvider;
    const model = buildModel();
    const lens = {
      range: {
        startLineNumber: 1,
        startColumn: 1,
        endLineNumber: 1,
        endColumn: 1,
      },
    } as monacoEditor.languages.CodeLens;

    await expect(
      provider.resolveCodeLens?.(model, lens, cancellationToken),
    ).resolves.toBe(lens);
    expect(LSPServiceBindings.ResolveCodeLens).toHaveBeenCalledWith("go", {
      line: 0,
      column: 0,
      endLine: 0,
      endColumn: 0,
      label: "",
      command: "",
    });
  });
});
